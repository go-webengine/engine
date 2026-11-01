// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nonet

package engine

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// pictureHTML is the confirmed real trigger's exact shape (en.wikipedia.org's
// own footer "Wikimedia Foundation" logo, every article page, unconditional
// static markup): a <picture> whose single <source> carries a media query
// and a one-candidate srcset, and a fallback <img> with its own src.
const pictureHTML = `<html><body>
<picture><source media="(min-width: 500px)" srcset="/wide.png"><img src="/small.jpg" alt="logo"></picture>
</body></html>`

// TestPictureSourceWinsAboveBreakpoint is the real regression: before this
// round, <picture>/<source>/srcset were entirely unimplemented — every <img>
// was read by its own bare `src`, so this engine always fetched the 25x25
// fallback instead of the wider branded button a real browser's own
// (min-width:500px) match selects at a 1024px viewport.
func TestPictureSourceWinsAboveBreakpoint(t *testing.T) {
	files := map[string][]byte{"/wide.png": b64(fxPNGAlpha), "/small.jpg": b64(fxJPEG)}
	mux := http.NewServeMux()
	for path, data := range files {
		path, data := path, data
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(pictureHTML))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	sm := css.CascadeVW(doc.Root, 1024, nil)
	set := e.LoadImageSet(context.Background(), doc, sm, 1024)
	img := findTag(doc.Root, "img")
	li := set[img]
	if li == nil {
		t.Fatal("the <picture>'s <img> loaded nothing at all")
	}
	if !bytes.Equal(li.Data, files["/wide.png"]) {
		t.Errorf("at 1024px (above the 500px breakpoint): loaded %d bytes, want the <source>'s own /wide.png (%d bytes), not the <img> fallback /small.jpg", len(li.Data), len(files["/wide.png"]))
	}
}

// TestPictureFallsBackBelowBreakpoint covers the <source>'s own media query
// genuinely gating the choice, not a fixed "always prefer <source>" rule: at
// a narrower viewport than its (min-width:500px) match, the <img>'s own
// fallback src must win instead.
func TestPictureFallsBackBelowBreakpoint(t *testing.T) {
	files := map[string][]byte{"/wide.png": b64(fxPNGAlpha), "/small.jpg": b64(fxJPEG)}
	mux := http.NewServeMux()
	for path, data := range files {
		path, data := path, data
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { w.Write(data) })
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(pictureHTML))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	sm := css.CascadeVW(doc.Root, 300, nil)
	set := e.LoadImageSet(context.Background(), doc, sm, 300)
	img := findTag(doc.Root, "img")
	li := set[img]
	if li == nil {
		t.Fatal("the <picture>'s <img> loaded nothing at all")
	}
	if !bytes.Equal(li.Data, files["/small.jpg"]) {
		t.Errorf("at 300px (below the 500px breakpoint): loaded %d bytes, want the <img> fallback /small.jpg (%d bytes), not the <source>'s own /wide.png", len(li.Data), len(files["/small.jpg"]))
	}
}

// TestPlainImgOutsidePictureUnaffected covers effectiveImgSrc's own
// no-<picture>-parent early return: a plain <img src="..."> with no
// <picture> ancestor at all must take none of this new code path.
func TestPlainImgOutsidePictureUnaffected(t *testing.T) {
	n := &dom.Node{Type: dom.Element, Tag: "img", Attr: map[string]string{"src": "/plain.png"}}
	src, ok := effectiveImgSrc(n, 1024)
	if !ok || src != "/plain.png" {
		t.Errorf("effectiveImgSrc(no <picture> parent) = %q,%v want /plain.png,true", src, ok)
	}
}

// TestFirstSrcsetCandidate covers the deliberately narrow srcset parse: the
// first comma-separated candidate's URL, ignoring a trailing width/density
// descriptor (not evaluated — see effectiveImgSrc's own doc comment).
func TestFirstSrcsetCandidate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/wide.png", "/wide.png"},
		{"/wide.png 2x", "/wide.png"},
		{"/a.png 1x, /b.png 2x", "/a.png"},
		{"", ""},
		{"   ", ""},
	} {
		if got := firstSrcsetCandidate(tc.in); got != tc.want {
			t.Errorf("firstSrcsetCandidate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// findTag returns the first element with the given tag in document order, or
// nil if none exists.
func findTag(n *dom.Node, tag string) *dom.Node {
	if n.Type == dom.Element && n.Tag == tag {
		return n
	}
	for _, c := range n.Children {
		if f := findTag(c, tag); f != nil {
			return f
		}
	}
	return nil
}
