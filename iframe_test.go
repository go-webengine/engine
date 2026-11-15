// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIframeSrcdocRendersIntoParentPage is the real end-to-end path: an
// <iframe srcdoc="..."> is parsed as its own document, laid out and painted
// with no network involved, and composited into the final page exactly like
// an <img>'s bitmap — reusing the SAME replaced-element pipeline canvas.go
// added for <canvas>.
func TestIframeSrcdocRendersIntoParentPage(t *testing.T) {
	html := `<html><body style="margin:0">
		<iframe id="f" width="40" height="40" srcdoc="&lt;body style=&quot;margin:0;background:#ff0000&quot;&gt;&lt;/body&gt;"></iframe>
	</body></html>`
	e := &Engine{MaxIframes: 4}
	img, info, err := e.RenderHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 60, 60))
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	if info.ContentHeight < 40 {
		t.Fatalf("contentHeight = %d, want at least 40 (the iframe's own height)", info.ContentHeight)
	}
	r, g, b, a := img.At(10, 10).RGBA()
	if r>>8 != 255 || g>>8 != 0 || b>>8 != 0 || a>>8 != 255 {
		t.Fatalf("pixel at (10,10) = (%d,%d,%d,%d), want opaque red — the iframe's own content never reached the final page bitmap", r>>8, g>>8, b>>8, a>>8)
	}
}

// TestIframeSrcFetchesAndRendersARealDocument confirms the src= path, which
// recurses into this engine's own Fetch, not just srcdoc's inline parse.
func TestIframeSrcFetchesAndRendersARealDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<body style="margin:0;background:#00ff00"></body>`))
	}))
	defer srv.Close()

	html := `<html><body style="margin:0"><iframe id="f" width="30" height="30" src="` + srv.URL + `/frame.html"></iframe></body></html>`
	e := &Engine{Client: newHTTPClient(), MaxIframes: 4}
	img, _, err := e.RenderHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 60, 60))
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	r, g, b, a := img.At(10, 10).RGBA()
	if r>>8 != 0 || g>>8 != 255 || b>>8 != 0 || a>>8 != 255 {
		t.Fatalf("pixel at (10,10) = (%d,%d,%d,%d), want opaque green — the fetched iframe document never reached the final page bitmap", r>>8, g>>8, b>>8, a>>8)
	}
}

// TestMaxIframesBoundsHowManyAreRendered is the budget this engine's own
// "always bounded, never silently wrong" convention requires for anything
// this expensive — a real recursive render per iframe, not a plain fetch.
func TestMaxIframesBoundsHowManyAreRendered(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<body style="background:#00ff00"></body>`))
	}))
	defer srv.Close()

	html := `<html><body>` +
		`<iframe width="10" height="10" src="` + srv.URL + `/a"></iframe>` +
		`<iframe width="10" height="10" src="` + srv.URL + `/b"></iframe>` +
		`<iframe width="10" height="10" src="` + srv.URL + `/c"></iframe>` +
		`</body></html>`
	e := &Engine{Client: newHTTPClient(), MaxIframes: 1}
	_, _, err := e.RenderHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 100, 100))
	if err != nil {
		t.Fatalf("RenderHTML error: %v", err)
	}
	if hits != 1 {
		t.Fatalf("server hit %d times, want exactly 1 (MaxIframes: 1)", hits)
	}
}

// TestIframeDepthCapStopsSelfEmbedding is the real safety property: a page
// whose iframe src points back at ITSELF (an accident or a tracking-pixel
// style construction, either way a real hazard) must not recurse past
// maxIframeDepth, however many requests budget would otherwise allow.
func TestIframeDepthCapStopsSelfEmbedding(t *testing.T) {
	var hits int
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<body><iframe width="10" height="10" src="` + srv.URL + `/"></iframe></body>`))
	}))
	defer srv.Close()

	e := &Engine{Client: newHTTPClient(), MaxIframes: 4}
	ctx, cancel := context.WithTimeout(context.Background(), 10_000_000_000) // 10s
	defer cancel()
	_, _, err := e.Render(ctx, srv.URL+"/", image.Rect(0, 0, 100, 100))
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	// Top-level fetch (1) + one nested iframe render (1): depth cap stops
	// before a second level of nesting is ever fetched.
	if hits > 2 {
		t.Fatalf("server hit %d times, want at most 2 (depth cap should stop self-embedding)", hits)
	}
}
