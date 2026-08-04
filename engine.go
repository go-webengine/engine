// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package engine is a pure-Go (CGO=0) static web rendering engine: it fetches a
// URL, parses the HTML into a DOM, applies a minimal-but-real CSS subset with
// cascade and inheritance, lays the content out in block-and-inline flow, and
// paints anti-aliased text, backgrounds and images to an image.RGBA. Phase 0
// runs no JavaScript; script-driven content is therefore not populated.
package engine

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-browserhttp/browserhttp"
	"golang.org/x/net/html/charset"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
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
	MaxImages int
}

// New returns an Engine with a browser-like HTTP client (Chrome TLS
// fingerprint, cookie jar, redirect following).
func New() *Engine {
	return &Engine{
		Client:    browserhttp.NewClient(30 * time.Second),
		UserAgent: browserhttp.DefaultUserAgent,
		MaxImages: 40,
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
	sm := css.Cascade(doc.Root)
	fonts := paint.NewFonts()

	vpW := viewport.Dx()
	if vpW <= 0 {
		vpW = 1024
	}
	imgSize, imgs := e.loadImages(ctx, doc, sm, vpW)

	box, height := layout.LayoutDocument(doc.Root, sm, float64(vpW), fonts, imgSize)

	canvasH := viewport.Dy()
	if int(height) > canvasH {
		canvasH = int(height)
	}
	if canvasH <= 0 {
		canvasH = 1
	}
	img := image.NewRGBA(image.Rect(0, 0, vpW, canvasH))
	fillWhite(img)
	// The canvas base (the page "backdrop") is the body's background — or the
	// html element's — extended over the whole viewport, matching a browser.
	if bg, ok := pageBackground(doc.Root, sm); ok {
		fillColor(img, bg)
	}
	paint.Paint(img, box, fonts, imgs)

	return img, &RenderInfo{
		Title:         doc.Title,
		URL:           doc.URL,
		ContentHeight: int(height),
	}, nil
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
