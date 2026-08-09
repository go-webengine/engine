// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package engine is a pure-Go (CGO=0) web rendering engine: it fetches a URL,
// parses the HTML into a DOM, applies a real CSS subset (cascade, inheritance,
// var(), @media, dark-mode, gradients), runs the page's JavaScript against a
// real DOM binding, lays the content out with a full box model (block, inline,
// float, flex, grid, table, position), and paints anti-aliased text,
// backgrounds, gradients, box-shadows, images and SVG to an image.RGBA. A
// settle-then-render loop re-lays-out after scripts mutate the DOM, so
// script-driven changes are reflected in the output; set Engine.DisableJS to
// render the static, no-JavaScript document instead.
package engine

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-browserhttp/browserhttp"
	"golang.org/x/net/html/charset"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/js"
	"github.com/go-webengine/engine/layout"
	"github.com/go-webengine/engine/paint"
)

// Document is a fetched and parsed page.
type Document struct {
	URL   string    // final URL after redirects
	Title string    // <title> text
	Root  *dom.Node // owned DOM tree
	HTML  string    // decoded (UTF-8) source
}

// RenderInfo carries metadata about a completed render.
type RenderInfo struct {
	Title         string
	URL           string
	ContentHeight int
}

// Engine holds the HTTP client and render configuration.
type Engine struct {
	Client    *http.Client
	UserAgent string
	// MaxImages bounds how many RASTER (non-SVG) <img>/background images are
	// fetched and decoded per render — the expensive network + large-decode work.
	MaxImages int
	// MaxVectorImages bounds how many VECTOR images (inline <svg> and
	// <img src="*.svg"> / data:image/svg+xml) are rasterised per render. Vector
	// chrome (nav/footer/social/UI icons) is cheap but plentiful — real design
	// systems place many dozens ahead of the content — so it gets its OWN, more
	// generous budget. Without this split a wall of decorative icons early in the
	// DOM exhausts a single shared budget and starves the actual content photos
	// (raster) further down (the DeepMind hero black-void bug).
	MaxVectorImages int

	// DisableJS turns off the JavaScript pass. Offline fixture tests that must
	// stay byte-deterministic set this; the default (false) runs page scripts.
	DisableJS bool
	// JSTimeout bounds the total script + timer budget per render. Zero selects
	// js.DefaultTimeout.
	JSTimeout time.Duration
	// MaxJSHeapBytes bounds process heap growth during the JavaScript settle
	// stage (script execution AND the esbuild module-bundle fetch/parse). Some
	// real pages ship huge module graphs (GitHub, large SPAs) whose bundling
	// balloons memory into the gigabytes before any time budget trips; a watchdog
	// samples the heap and, once growth from the stage's start exceeds this bound,
	// cancels the stage so it falls back to the already-computed pre-script layout
	// instead of driving the process toward OOM. Zero selects a sane default; a
	// negative value disables the guard.
	MaxJSHeapBytes int64
	// JSLog, if non-nil, receives console.* and diagnostic lines from the script
	// pass (used for debugging; nil discards them).
	JSLog func(string)
	// MetaFallback, when true, renders a clean readable card synthesised from a
	// page's OpenGraph/meta tags (og:title/og:description/og:image, <title>) as a
	// LAST RESORT — only when the real render is empty (a JavaScript SPA the engine
	// cannot hydrate: Mastodon, X, app-shell sites). Default false: it fabricates
	// content that is not in the page's rendered DOM, so it is opt-in; a
	// link-preview / reader consumer enables it, while callers wanting a faithful
	// render (or to detect the blank SPA themselves) are unaffected. It never
	// triggers on a page that renders any real content, so normal pages are
	// byte-identical whether it is on or off.
	MetaFallback bool
}

// New returns an Engine with a browser-like HTTP client (Chrome TLS
// fingerprint, cookie jar, redirect following).
func New() *Engine {
	return &Engine{
		Client:    browserhttp.NewClient(30 * time.Second),
		UserAgent: browserhttp.DefaultUserAgent,
		MaxImages: 40,
		// 400 comfortably covers icon-dense design systems (Material, Primer,
		// Font Awesome pages carry well under a few hundred distinct icons in the
		// initial DOM) while still bounding a pathological page — each vector
		// rasterisation is bounded work, so 400 caps runaway cost.
		MaxVectorImages: 400,
		// A normal page's whole render stays well under a few hundred MB; 768MB of
		// growth during the script stage means a runaway module bundle, so cut it
		// off there and keep the good pre-script layout.
		MaxJSHeapBytes: 768 << 20,
	}
}

// Fetch retrieves and parses url into a Document.
func (e *Engine) Fetch(ctx context.Context, rawurl string) (*Document, error) {
	body, finalURL, ctype, err := e.get(ctx, rawurl)
	if err != nil {
		return nil, err
	}
	utf8, err := decodeCharset(body, ctype)
	if err != nil {
		utf8 = body // fall back to raw bytes on decode failure
	}
	root, err := dom.Parse(string(utf8))
	if err != nil {
		return nil, err
	}
	return &Document{
		URL:   finalURL,
		Title: dom.Title(root),
		Root:  root,
		HTML:  string(utf8),
	}, nil
}

// get performs the HTTP GET, returning body, final URL and content-type.
func (e *Engine) get(ctx context.Context, rawurl string) ([]byte, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", e.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, "", "", err
	}
	final := rawurl
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return body, final, resp.Header.Get("Content-Type"), nil
}

// decodeCharset converts body to UTF-8, honouring the Content-Type charset and
// any <meta charset> sniffed from the prefix.
func decodeCharset(body []byte, contentType string) ([]byte, error) {
	r, err := charset.NewReader(bytes.NewReader(body), contentType)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// Render fetches url and renders it into an image at the given viewport width
// (the height is grown to fit the full page, at least the viewport height).
func (e *Engine) Render(ctx context.Context, rawurl string, viewport image.Rectangle) (*image.RGBA, *RenderInfo, error) {
	doc, err := e.Fetch(ctx, rawurl)
	if err != nil {
		return nil, nil, err
	}
	return e.RenderDocument(ctx, doc, viewport)
}

// RenderDocument renders an already-fetched Document. It is the offline entry
// point used by fixtures and tests (image sub-resources are still fetched from
// the network if their src is absolute/resolvable).
func (e *Engine) RenderDocument(ctx context.Context, doc *Document, viewport image.Rectangle) (*image.RGBA, *RenderInfo, error) {
	fonts := paint.NewFonts()
	vpW, vpH := viewportSize(viewport)
	rp := e.renderCore(ctx, doc, vpW, vpH, fonts)
	img := newCanvas(doc, rp, viewport, vpW)
	paint.PaintFull(img, rp.box, fonts, rp.imgs, rp.bgImgs)
	return img, renderInfo(doc, rp), nil
}

// viewportSize resolves the render width/height, applying the 1024×768 defaults
// for a non-positive extent (a common "auto-height" caller passes Dy()==0).
func viewportSize(viewport image.Rectangle) (vpW, vpH int) {
	vpW, vpH = viewport.Dx(), viewport.Dy()
	if vpW <= 0 {
		vpW = 1024
	}
	if vpH <= 0 {
		vpH = 768
	}
	return vpW, vpH
}

// renderCore runs the shared cascade → JavaScript settle → layout pipeline for
// doc at the given viewport, returning the settled render pass. It is used by
// both RenderDocument and RenderDocumentWithLinks so the painted image and the
// anchor hit-map always describe the exact same JS-settled DOM and geometry. On
// return doc.Title reflects any document.title a script set.
func (e *Engine) renderCore(ctx context.Context, doc *Document, vpW, vpH int, fonts *paint.Fonts) *renderPass {
	return e.renderCoreStaged(ctx, doc, vpW, vpH, fonts, nil)
}

// renderCoreStaged is renderCore with an optional per-stage hook that drives
// progressive rendering. onStage (when non-nil) is called with "initial" for the
// text-first frame (styled layout BEFORE any image is fetched), then "images"
// once the images are loaded and re-laid-out, then "settle" after each settle
// pass that changed the geometry; the caller emits the "final" frame from the
// returned pass. onStage==nil reproduces the original batch pipeline exactly (a
// single images-then-layout pass, no wasted pre-image layout), so RenderDocument
// / RenderDocumentWithLinks are byte-identical to before.
func (e *Engine) renderCoreStaged(ctx context.Context, doc *Document, vpW, vpH int, fonts *paint.Fonts, onStage func(stage string, rp *renderPass)) *renderPass {
	// Set the JS-enabled signal (client-nojs → client-js on <html>) BEFORE the
	// initial cascade so the first layout — the geometry scripts read back — is
	// already the JS-enabled one. DisableJS leaves the no-JS fallback in place.
	if !e.DisableJS {
		js.MarkJSEnabled(doc.Root)
	}

	// Initial pass: fetch <link rel="stylesheet"> sheets (the dominant fidelity
	// factor for real sites) and cascade at the render width (so @media width
	// queries and external theme/layout rules resolve).
	rp := &renderPass{}
	rp.sheets = e.fetchExternalSheets(ctx, doc, float64(vpW))
	rp.sm = css.CascadeVW(doc.Root, float64(vpW), rp.sheets)

	// TEXT-FIRST progressive frame (progressive path ONLY): lay out and emit the
	// fully-styled page BEFORE the slow, network-bound image fetch. Images reserve
	// their boxes from width/height attributes or CSS sizes (layout's imageSize
	// falls back to those on an imgSize miss), so the text geometry is already
	// sane; the image regions paint as page background until the refinement frame.
	// This is the big perceived-latency win — sequential image fetch no longer
	// gates the first styled paint. The batch path (onStage==nil) skips this so it
	// does exactly one layout, keeping its output byte-identical to before.
	if onStage != nil {
		rp.box, rp.height = layout.LayoutDocument(doc.Root, rp.sm, float64(vpW), fonts, nil)
		onStage("initial", rp)
	}

	// Load images + background images (bounded, fetched concurrently) and lay out
	// with the real intrinsic sizes. This is the geometry the page's scripts see
	// through getBoundingClientRect / offset* / getComputedStyle, so it runs
	// BEFORE the JS settle loop in both paths.
	rp.imgSize, rp.imgs = e.loadImages(ctx, doc, rp.sm, vpW)
	rp.bgImgs = e.loadBackgroundImages(ctx, doc, rp.sm)

	start := time.Now()
	rp.box, rp.height = layout.LayoutDocument(doc.Root, rp.sm, float64(vpW), fonts, rp.imgSize)
	initialLayout := time.Since(start)

	// Refinement frame: the same styled page WITH images placed. The caller dedups
	// it against the text-first frame by geomSig, so a page whose image intrinsic
	// sizes moved nothing (or a page with no images) yields just initial+final.
	if onStage != nil {
		onStage("images", rp)
	}

	// Settle-then-render: run scripts against the real geometry, let them mutate
	// the DOM / inject <script>/<style>, and iterate cascade→layout to a bounded
	// fixpoint. A script error never aborts the render; the pass is bounded by a
	// wall-clock budget and a pass cap, so it can never hang. A heap watchdog
	// additionally cancels the whole stage (aborting a runaway module bundle or
	// script) once memory balloons past MaxJSHeapBytes, so a page like GitHub's
	// huge module graph falls back to its already-good pre-script layout instead
	// of driving the process toward OOM.
	if !e.DisableJS {
		sctx, stop := e.heapGuardedContext(ctx)
		e.settle(sctx, doc, vpW, vpH, fonts, rp, initialLayout, onStage)
		stop()
	}

	// A script may have set document.title; re-derive it so RenderInfo reports the
	// post-script title (matching what a browser tab would show).
	doc.Title = dom.Title(doc.Root)

	// Last-resort meta/OG fallback: an un-hydratable SPA lays out to nothing
	// (no visible text or images). Rather than return a blank page, synthesise a
	// readable card from the page's OpenGraph/meta tags and render THAT instead.
	// Only replaces a genuinely empty render, so a page that rendered any real
	// content is untouched. Applied after settle, so the batch render and the
	// progressive "final" frame both carry the card (the pre-settle "initial"
	// frame still reflects the raw page).
	if e.MetaFallback && renderedEmpty(rp.box, rp.height) {
		if fb := e.buildMetaFallback(ctx, doc, vpW, fonts); fb != nil {
			rp = fb
		}
	}
	return rp
}

// heapGuardedContext derives a context from parent that is cancelled once the
// process heap grows by more than MaxJSHeapBytes from the moment of the call — a
// runaway-memory backstop for the JavaScript settle stage (script execution and
// the esbuild module bundle). The returned stop function must be called to
// release the watchdog; it also cancels the derived context. A negative
// MaxJSHeapBytes disables the guard (the returned context is a plain cancel
// child); zero selects the 768MB default.
func (e *Engine) heapGuardedContext(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	limit := e.MaxJSHeapBytes
	if limit == 0 {
		limit = 768 << 20
	}
	if limit < 0 {
		return ctx, cancel // guard disabled
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	ceiling := ms.HeapAlloc + uint64(limit)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > ceiling {
					cancel() // heap ballooned: abort the settle stage
					return
				}
			}
		}
	}()
	return ctx, func() {
		close(done)
		cancel()
	}
}

// newCanvas allocates the output image, fills the white base and paints the page
// backdrop (body/html background) over the whole viewport, matching a browser.
// The height is grown to the laid-out content, at least the viewport height, and
// clamped to a minimum of one pixel.
func newCanvas(doc *Document, rp *renderPass, viewport image.Rectangle, vpW int) *image.RGBA {
	canvasH := viewport.Dy()
	if int(rp.height) > canvasH {
		canvasH = int(rp.height)
	}
	if canvasH <= 0 {
		canvasH = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, vpW, canvasH))
	fillWhite(img)
	if bg, ok := pageBackground(doc.Root, rp.sm); ok {
		fillColor(img, bg)
	}
	return img
}

// renderInfo builds the RenderInfo for a completed render pass.
func renderInfo(doc *Document, rp *renderPass) *RenderInfo {
	return &RenderInfo{
		Title:         doc.Title,
		URL:           doc.URL,
		ContentHeight: int(rp.height),
	}
}

// RenderHTML renders an HTML string (with baseURL used to resolve relative
// image sources) into an image at the given viewport. It is the offline entry
// point for local fixtures and demos, running the same cascade/layout/paint
// pipeline as Render but without fetching the page itself.
func (e *Engine) RenderHTML(ctx context.Context, htmlSrc, baseURL string, viewport image.Rectangle) (*image.RGBA, *RenderInfo, error) {
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		return nil, nil, err
	}
	doc := &Document{URL: baseURL, Title: dom.Title(root), Root: root, HTML: htmlSrc}
	return e.RenderDocument(ctx, doc, viewport)
}

// Screenshot renders url and encodes the result as PNG.
func (e *Engine) Screenshot(ctx context.Context, rawurl string, viewport image.Rectangle) ([]byte, error) {
	img, _, err := e.Render(ctx, rawurl, viewport)
	if err != nil {
		return nil, err
	}
	return EncodePNG(img)
}

// EncodePNG encodes an image as PNG bytes.
func EncodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func fillWhite(img *image.RGBA) {
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = 255
		img.Pix[i+1] = 255
		img.Pix[i+2] = 255
		img.Pix[i+3] = 255
	}
}

func fillColor(img *image.RGBA, c css.Color) {
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = 255
	}
}

// pageBackground returns the opaque backdrop colour to paint under the whole
// viewport: the body's background-color if set, else the html element's.
func pageBackground(root *dom.Node, sm css.StyleMap) (css.Color, bool) {
	for _, tag := range []string{"body", "html"} {
		if el := dom.Find(root, tag); el != nil {
			if st := sm[el]; st != nil && st.Background.A > 0 {
				return st.Background, true
			}
		}
	}
	return css.Color{}, false
}

// External-stylesheet fetch limits (safety: bounded work per page).
const (
	maxExternalSheets    = 20
	maxSheetBytes        = 4 << 20 // 4 MB per sheet
	maxImportDepth       = 2       // @import nesting levels honoured
	externalSheetTimeout = 10 * time.Second
)

// fetchExternalSheets resolves and fetches every applicable
// <link rel="stylesheet"> for doc, plus their leading @import chains, returning
// the sheet texts in cascade order (imports before importer, links in document
// order). Only http/https absolute URLs are fetched; failures degrade
// gracefully (that sheet is skipped, the page still renders). vw is used to
// drop media-query-excluded links (e.g. print-only).
func (e *Engine) fetchExternalSheets(ctx context.Context, doc *Document, vw float64) []string {
	links := css.StylesheetLinks(doc.Root)
	if len(links) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, externalSheetTimeout)
	defer cancel()

	// Prefetch the applicable top-level sheet bodies CONCURRENTLY into a cache to
	// cut time-to-first-styled-paint (the initial progressive frame waits on this
	// fetch). This does NOT change the cascade: the sequential walk below still
	// applies sheets and their @imports in document order — it just reads bodies
	// the concurrent prefetch already retrieved. Deterministic: goroutines write
	// only their own result slot; the cache map is populated single-threaded.
	cache := map[string]sheetResult{}
	var pre []string
	seenPre := map[string]bool{}
	for _, ln := range links {
		if len(pre) >= maxExternalSheets {
			break
		}
		if !css.MediaApplies(ln.Media, vw) {
			continue
		}
		if abs, ok := resolveURL(doc.URL, ln.Href); ok && !seenPre[abs] {
			seenPre[abs] = true
			pre = append(pre, abs)
		}
	}
	e.prefetchSheets(ctx, pre, cache)

	seen := map[string]bool{}
	var out []string
	for _, ln := range links {
		if len(out) >= maxExternalSheets {
			break
		}
		if !css.MediaApplies(ln.Media, vw) {
			continue
		}
		abs, ok := resolveURL(doc.URL, ln.Href)
		if !ok {
			continue
		}
		out = e.appendSheet(ctx, abs, vw, seen, out, 0, cache)
	}
	return out
}

// sheetResult is a fetched stylesheet body and whether the fetch succeeded.
type sheetResult struct {
	text string
	ok   bool
}

// prefetchSheets fetches urls concurrently, storing each result in cache keyed
// by URL. Each goroutine writes only its own slot, and the cache map is filled
// single-threaded after the fetches join, so the result is race-free and
// independent of completion order.
func (e *Engine) prefetchSheets(ctx context.Context, urls []string, cache map[string]sheetResult) {
	if len(urls) == 1 {
		t, ok := e.fetchSheetNet(ctx, urls[0])
		cache[urls[0]] = sheetResult{t, ok}
		return
	}
	res := make([]sheetResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			t, ok := e.fetchSheetNet(ctx, u)
			res[i] = sheetResult{t, ok}
		}(i, u)
	}
	wg.Wait()
	for i, u := range urls {
		cache[u] = res[i]
	}
}

// appendSheet fetches the sheet at absURL (if not already seen and within
// limits), recursively prepends its leading @import targets, then appends the
// sheet's own text. It returns the possibly-extended out slice.
func (e *Engine) appendSheet(ctx context.Context, absURL string, vw float64, seen map[string]bool, out []string, depth int, cache map[string]sheetResult) []string {
	if depth > maxImportDepth || len(out) >= maxExternalSheets || seen[absURL] {
		return out
	}
	seen[absURL] = true
	text, ok := e.fetchSheet(ctx, absURL, cache)
	if !ok {
		return out
	}
	// @imports load (and thus cascade) before the importing sheet's own rules.
	imports, medias := css.ImportURLs(text)
	for i, imp := range imports {
		if !css.MediaApplies(medias[i], vw) {
			continue
		}
		if abs, ok := resolveURL(absURL, imp); ok {
			out = e.appendSheet(ctx, abs, vw, seen, out, depth+1, cache)
		}
	}
	return append(out, text)
}

// fetchSheet returns a stylesheet body, preferring a value the concurrent
// prefetch already placed in cache and falling back to a direct network fetch
// (for @import targets, which are not prefetched).
func (e *Engine) fetchSheet(ctx context.Context, absURL string, cache map[string]sheetResult) (string, bool) {
	if r, ok := cache[absURL]; ok {
		return r.text, r.ok
	}
	return e.fetchSheetNet(ctx, absURL)
}

// fetchSheetNet performs the actual network fetch of a stylesheet body,
// bounded by maxSheetBytes. Non-2xx or oversized/undecodable responses report
// false.
func (e *Engine) fetchSheetNet(ctx context.Context, absURL string) (string, bool) {
	if !strings.HasPrefix(absURL, "http://") && !strings.HasPrefix(absURL, "https://") {
		return "", false
	}
	body, _, ctype, err := e.get(ctx, absURL)
	if err != nil {
		return "", false
	}
	if len(body) > maxSheetBytes {
		body = body[:maxSheetBytes]
	}
	utf8, err := decodeCharset(body, ctype)
	if err != nil {
		utf8 = body
	}
	return string(utf8), true
}

// resolveURL resolves ref against the document base URL.
func resolveURL(base, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	b, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	return b.ResolveReference(r).String(), true
}

// Package-level convenience wrappers using a fresh default Engine.

// Render renders url with a default engine.
func Render(ctx context.Context, rawurl string, viewport image.Rectangle) (*image.RGBA, *RenderInfo, error) {
	return New().Render(ctx, rawurl, viewport)
}

// Screenshot renders url with a default engine and returns PNG bytes.
func Screenshot(ctx context.Context, rawurl string, viewport image.Rectangle) ([]byte, error) {
	return New().Screenshot(ctx, rawurl, viewport)
}
