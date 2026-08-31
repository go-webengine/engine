// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"encoding/base64"
	"image"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"

	"github.com/go-gfx/gfx/codec"
	"github.com/go-gfx/gfx/raster"
	"github.com/go-gfx/gfx/resample"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// resampleMode maps a computed style's image-rendering to a go-gfx resampling
// filter. The default is Bicubic (a smooth, antialiasing Keys/Catmull-Rom
// resample — sharp on enlargement, low-pass on reduction); `image-rendering:
// pixelated`/`crisp-edges` opts into Nearest for hard-edged pixel art.
func resampleMode(st *css.Style) resample.Mode {
	if st != nil && st.ImageRendering == css.IRPixelated {
		return resample.Nearest
	}
	return resample.Bicubic
}

// resizeRaster scales src to w×h with mode, filtering colour in premultiplied-
// alpha space so a transparent pixel's colour cannot bleed into the visible
// edge of a cut-out (a logo/icon fringe). It falls back to the unscaled source
// when go-gfx rejects the target (non-positive dimensions), so a caller need not
// re-check what it already validated.
func resizeRaster(src *raster.Image, w, h int, mode resample.Mode) *raster.Image {
	out, err := resample.ResizePremultiplied(src, w, h, mode)
	if err != nil {
		return src
	}
	return out
}

// imgWorkers is the concurrency bound for image fetch+decode: enough to hide
// per-image network latency (the dominant cost) without unbounded fan-out.
func imgWorkers(n int) int {
	w := min(8, runtime.GOMAXPROCS(0))
	if w > n {
		w = n
	}
	return w
}

// parallelDo runs fn over indices [0,n) using a bounded worker pool. fn must
// write only to its own index i (no shared state), so the result is independent
// of scheduling and completion order — the determinism the callers rely on. fn
// is expected to short-circuit on a cancelled context itself.
func parallelDo(n int, fn func(i int)) {
	if n == 0 {
		return
	}
	ch := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < imgWorkers(n); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				fn(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
}

// loadImages fetches and decodes every <img> in the document (best-effort),
// returning intrinsic sizes for layout and decoded bitmaps for paint. Images
// wider than the viewport are scaled down proportionally. Failures are skipped.
func (e *Engine) loadImages(ctx context.Context, doc *Document, sm css.StyleMap, viewportW int) (map[*dom.Node][2]float64, map[*dom.Node]image.Image) {
	sizes := map[*dom.Node][2]float64{}
	bitmaps := map[*dom.Node]image.Image{}

	// Collect replaced elements: raster/SVG <img> and inline <svg>. An inline
	// <svg> is a replaced box: it is collected and its subtree is not descended
	// into (its children are SVG primitives, not flow content).
	var reps []*dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element && (n.Tag == "img" || n.Tag == "svg") {
			if st := sm[n]; st == nil || st.Display != css.DisplayNone {
				reps = append(reps, n)
			}
			if n.Tag == "svg" {
				return // treat the SVG subtree as an opaque replaced element
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)

	// Separate budgets: cheap-but-plentiful vector chrome (inline <svg> and
	// <img src="*.svg">) must not starve the expensive raster content photos.
	// Each item is charged to its budget when it passes the gate, in DOCUMENT
	// ORDER, BEFORE any fetch is dispatched — so the accepted set is exactly the
	// same regardless of the concurrent scheduling below (determinism), and both
	// fetch and decode work stay bounded even on a page of only-failing images.
	raster, vector := 0, 0
	var jobs []*dom.Node
	for _, n := range reps {
		if n.Tag == "svg" {
			// Inline <svg>: no src, charged to the vector budget.
			if vector >= e.MaxVectorImages {
				continue
			}
			vector++
			jobs = append(jobs, n)
			continue
		}
		src, ok := n.Attribute("src")
		if !ok {
			continue // a src-less <img> is skipped without charging any budget
		}
		// Classify vector vs raster from the src alone so a wall of *.svg icons
		// spends only the vector budget, never the raster one.
		if srcLooksLikeSVG(src) {
			if vector >= e.MaxVectorImages {
				continue
			}
			vector++
		} else {
			if raster >= e.MaxImages {
				continue
			}
			raster++
		}
		jobs = append(jobs, n)
	}

	// Fetch+decode the accepted set CONCURRENTLY (network latency is the dominant
	// cost). Each worker writes only its own result slot; the maps are then filled
	// single-threaded in document order, so the output is byte-identical to the
	// sequential version for any fixture.
	type result struct {
		size [2]float64
		bmp  image.Image
		ok   bool
	}
	results := make([]result, len(jobs))
	parallelDo(len(jobs), func(i int) {
		size, bmp, ok := e.loadOneImage(ctx, doc, sm, viewportW, jobs[i])
		results[i] = result{size, bmp, ok}
	})
	for i, n := range jobs {
		if results[i].ok {
			sizes[n] = results[i].size
			bitmaps[n] = results[i].bmp
		}
	}
	return sizes, bitmaps
}

// loadOneImage fetches and decodes one accepted replaced element (inline <svg>,
// raster <img>, or <img src="*.svg">), returning its intrinsic size and bitmap.
// ok=false means skip it (no maps entry) — a cancelled context, a failed fetch,
// or an undecodable payload. It is pure with respect to its node, so it is safe
// to run concurrently for distinct nodes.
func (e *Engine) loadOneImage(ctx context.Context, doc *Document, sm css.StyleMap, viewportW int, n *dom.Node) (size [2]float64, bmp image.Image, ok bool) {
	if ctx.Err() != nil {
		return size, nil, false // respect cancellation before any work
	}
	// Inline <svg>: serialise the subtree and rasterise it.
	if n.Tag == "svg" {
		data := []byte(serializeSVG(n))
		b, w, h, ok := e.svgToBitmap(data, sm[n], attrDim(n, "width"), attrDim(n, "height"), viewportW, colorHex(sm[n]))
		if !ok {
			return size, nil, false
		}
		return [2]float64{float64(w), float64(h)}, b, true
	}
	src, _ := n.Attribute("src") // presence was checked in the gate
	data, ok := e.fetchImageBytes(ctx, doc.URL, src)
	if !ok {
		return size, nil, false
	}
	// <img src="*.svg"> and data:image/svg+xml: rasterise via the SVG path.
	if looksLikeSVG(data, src) {
		b, w, h, ok := e.svgToBitmap(data, sm[n], attrDim(n, "width"), attrDim(n, "height"), viewportW, colorHex(sm[n]))
		if !ok {
			return size, nil, false
		}
		return [2]float64{float64(w), float64(h)}, b, true
	}
	src0, err := codec.Decode(data)
	if err != nil {
		return size, nil, false
	}
	w, h := src0.W, src0.H
	if w <= 0 || h <= 0 {
		return size, nil, false
	}
	mode := resampleMode(sm[n])
	// Apply a single-axis CSS width/height as a browser does: the specified axis
	// is used and the other is scaled by the intrinsic aspect ratio (so e.g. a
	// wide logo constrained to height:1.5rem is ~72px wide, not its full intrinsic
	// width). Both axes set uses both; neither keeps intrinsic.
	if cw, ch, ok := cssImageSize(sm[n], w, h); ok {
		if cw != w || ch != h {
			src0 = resizeRaster(src0, cw, ch, mode)
			w, h = cw, ch
		}
	}
	// A PERCENTAGE CSS width sizes the image to that fraction of the viewport,
	// scaling UP as well as down (a browser's `width:100%`). The layout engine
	// draws a replaced element at exactly the size returned here, so an image
	// meant to fill its container — most plainly a standalone image document,
	// synthesised as `<img style="width:100%">` — must be scaled to the viewport
	// here; otherwise a small image would render at its native size instead of
	// spanning the pane. cssImageSize handles only DEFINITE px sizes, so a
	// percentage is resolved separately, against the viewport width.
	if pw, ok := percentImageWidth(sm[n], viewportW); ok && pw != w {
		nh := int(float64(h) * float64(pw) / float64(w))
		if nh < 1 {
			nh = 1
		}
		src0 = resizeRaster(src0, pw, nh, mode)
		w, h = pw, nh
	}
	// Scale down to fit the viewport width, preserving aspect ratio.
	if w > viewportW && viewportW > 0 {
		nh := int(float64(h) * float64(viewportW) / float64(w))
		if nh < 1 {
			nh = 1
		}
		src0 = resizeRaster(src0, viewportW, nh, mode)
		w, h = viewportW, nh
	}
	return [2]float64{float64(w), float64(h)}, src0.ToNRGBA(), true
}

// loadBackgroundImages fetches and decodes every distinct CSS
// `background-image: url(...)` referenced by a styled element (best-effort),
// returning intrinsic bitmaps keyed by their raw url() token (the same key the
// paint layer stores). Gradient layers need no bitmap. Failures (fetch/decode)
// are skipped; the count is bounded by MaxImages, and each distinct URL is
// fetched at most once.
func (e *Engine) loadBackgroundImages(ctx context.Context, doc *Document, sm css.StyleMap) map[string]image.Image {
	// Collect distinct raw url() tokens in document order.
	seen := map[string]bool{}
	var urls []string
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element {
			if st := sm[n]; st != nil {
				for _, layer := range st.BackgroundImages {
					if layer.Kind == css.BgURL && layer.URL != "" && !seen[layer.URL] {
						seen[layer.URL] = true
						urls = append(urls, layer.URL)
					}
				}
			}
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(doc.Root)
	if len(urls) == 0 {
		return nil
	}

	// Same raster/vector budget split as loadImages, charged in document order
	// BEFORE any fetch so a wall of decorative SVG backgrounds never spends the
	// raster budget and the accepted set is independent of the concurrent
	// scheduling below (and the fetch work stays bounded even when every url fails
	// to decode). Vector backgrounds are not rasterised here (only raster formats
	// decode), but they are still gated so they cannot exhaust the raster budget.
	raster, vector := 0, 0
	var accepted []string
	for _, raw := range urls {
		if srcLooksLikeSVG(raw) {
			if vector >= e.MaxVectorImages {
				continue
			}
			vector++
		} else {
			if raster >= e.MaxImages {
				continue
			}
			raster++
		}
		accepted = append(accepted, raw)
	}

	// Fetch+decode the accepted set CONCURRENTLY; each worker writes only its own
	// result slot and the map is filled single-threaded in document order, so the
	// output is byte-identical to the sequential version.
	results := make([]image.Image, len(accepted))
	parallelDo(len(accepted), func(i int) {
		results[i] = e.loadOneBackground(ctx, doc, accepted[i])
	})
	out := map[string]image.Image{}
	for i, raw := range accepted {
		if results[i] != nil {
			out[raw] = results[i]
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// loadOneBackground fetches and decodes one accepted background-image url,
// returning nil on a cancelled context, a failed fetch, or an undecodable /
// zero-size payload. Safe to run concurrently for distinct urls.
func (e *Engine) loadOneBackground(ctx context.Context, doc *Document, raw string) image.Image {
	if ctx.Err() != nil {
		return nil
	}
	data, ok := e.fetchImageBytes(ctx, doc.URL, raw)
	if !ok {
		return nil
	}
	img, err := codec.Decode(data)
	if err != nil {
		return nil
	}
	if img.W <= 0 || img.H <= 0 {
		return nil
	}
	return img.ToNRGBA()
}

// cssImageSize resolves the used pixel dimensions of an image given its style
// and intrinsic size (iw×ih). A definite (non-auto, non-percentage) CSS width
// and/or height override the intrinsic size; when only one axis is definite the
// other is derived from the intrinsic aspect ratio. It reports false when the
// style is nil or specifies neither axis definitely (keep the intrinsic size).
func cssImageSize(st *css.Style, iw, ih int) (w, h int, ok bool) {
	if st == nil || iw <= 0 || ih <= 0 {
		return 0, 0, false
	}
	definite := func(l css.Length) (float64, bool) {
		if l.Auto || l.IsPercent || l.Px <= 0 {
			return 0, false
		}
		return l.Px, true
	}
	cw, hasW := definite(st.Width)
	ch, hasH := definite(st.Height)
	switch {
	case hasW && hasH:
		return iround(cw), iround(ch), true
	case hasW:
		return iround(cw), iround(cw * float64(ih) / float64(iw)), true
	case hasH:
		return iround(ch * float64(iw) / float64(ih)), iround(ch), true
	default:
		return 0, 0, false
	}
}

// percentImageWidth resolves a PERCENTAGE CSS width on an image against the
// viewport width, returning the target pixel width (>= 1). It reports false
// when the style has no percentage width, when the percentage is non-positive,
// or when the viewport width is unknown — so only an explicit `width:N%` opts
// in and everything else keeps its intrinsic/definite sizing.
func percentImageWidth(st *css.Style, viewportW int) (int, bool) {
	if st == nil || viewportW <= 0 {
		return 0, false
	}
	if !st.Width.IsPercent || st.Width.Percent <= 0 {
		return 0, false
	}
	return iround(st.Width.Percent * float64(viewportW)), true
}

// iround rounds a non-negative float to the nearest int (>= 1).
func iround(f float64) int {
	n := int(f + 0.5)
	if n < 1 {
		n = 1
	}
	return n
}

// fetchImageBytes returns the raw bytes for an image src, handling data: URIs
// and absolute/relative http(s) URLs.
func (e *Engine) fetchImageBytes(ctx context.Context, base, src string) ([]byte, bool) {
	src = strings.TrimSpace(src)
	if strings.HasPrefix(src, "data:") {
		return decodeDataURI(src)
	}
	abs, ok := resolveURL(base, src)
	if !ok || !(strings.HasPrefix(abs, "http://") || strings.HasPrefix(abs, "https://")) {
		return nil, false
	}
	// The per-render cache (see withImgByteCache) is checked first: it is always
	// present here (renderCoreStaged installs one unconditionally) and catches a
	// URL already fetched earlier in THIS render — most commonly the same image
	// reloaded after a settle pass — with no network call and no e.ImageCache
	// dispatch either.
	render := imgByteCacheFrom(ctx)
	if render != nil {
		if data, ok := render.get(abs); ok {
			return data, true
		}
	}
	// A configured cache serves a previously-fetched image with no network, so a
	// re-render (or another page using the same asset) is instant.
	if e.ImageCache != nil {
		if data, ok := e.ImageCache.Get(abs); ok {
			if render != nil {
				render.put(abs, data)
			}
			return data, true
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, abs, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", e.UserAgent)
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, false
	}
	if e.ImageCache != nil {
		e.ImageCache.Put(abs, data)
	}
	if render != nil {
		render.put(abs, data)
	}
	return data, true
}

// imgByteCache is an ephemeral, in-memory, per-render cache of fetched image
// bytes keyed by absolute URL. It exists purely to dedupe repeat fetches
// WITHIN one render (e.g. the settle loop reloading images after a script
// mutates the DOM) — it is created fresh per render (see renderCoreStaged)
// and never stored on Engine, which is shared and used concurrently across
// unrelated renders. Safe for concurrent use: loadImages/loadBackgroundImages
// fetch concurrently via parallelDo.
type imgByteCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newImgByteCache() *imgByteCache { return &imgByteCache{m: map[string][]byte{}} }

func (c *imgByteCache) get(url string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.m[url]
	return data, ok
}

func (c *imgByteCache) put(url string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[url] = data
}

type imgByteCacheKey struct{}

// withImgByteCache returns a context carrying c for fetchImageBytes to find via
// imgByteCacheFrom. ctx already threads unchanged through every image-fetching
// call in a render (the initial pass, the settle loop, the meta-fallback
// path), so installing it once at the top of renderCoreStaged reaches all of
// them with no signature change at each call site.
func withImgByteCache(ctx context.Context, c *imgByteCache) context.Context {
	return context.WithValue(ctx, imgByteCacheKey{}, c)
}

// imgByteCacheFrom returns the render-scoped cache installed by
// withImgByteCache, or nil if none was (e.g. a direct unit test call).
func imgByteCacheFrom(ctx context.Context) *imgByteCache {
	c, _ := ctx.Value(imgByteCacheKey{}).(*imgByteCache)
	return c
}

// decodeDataURI decodes a base64 data: URI's payload.
func decodeDataURI(s string) ([]byte, bool) {
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return nil, false
	}
	meta, payload := s[5:comma], s[comma+1:]
	if strings.Contains(meta, "base64") {
		b, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return nil, false
		}
		return b, true
	}
	// A non-base64 data URI payload is percent-encoded (RFC 2397); decode it so
	// e.g. `data:image/svg+xml,%3Csvg...` yields real SVG bytes. PathUnescape
	// (unlike QueryUnescape) leaves '+' literal, which SVG data needs. An
	// undecodable payload falls back to the raw bytes.
	if dec, err := url.PathUnescape(payload); err == nil {
		return []byte(dec), true
	}
	return []byte(payload), true
}
