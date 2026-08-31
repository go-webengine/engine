// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/go-webengine/engine/css"
)

func TestCSSImageSize(t *testing.T) {
	// Intrinsic 900x298 (the go.dev Google wordmark shape).
	cases := []struct {
		name       string
		st         *css.Style
		iw, ih     int
		wantW      int
		wantH      int
		wantOK     bool
	}{
		{"nil style keeps intrinsic", nil, 900, 298, 0, 0, false},
		{"no CSS dims keeps intrinsic",
			&css.Style{Width: css.Length{Auto: true}, Height: css.Length{Auto: true}}, 900, 298, 0, 0, false},
		{"height only scales width by aspect",
			&css.Style{Width: css.Length{Auto: true}, Height: css.Length{Px: 24}}, 900, 298, 72, 24, true},
		{"width only scales height by aspect",
			&css.Style{Width: css.Length{Px: 100}, Height: css.Length{Auto: true}}, 200, 100, 100, 50, true},
		{"both axes set uses both",
			&css.Style{Width: css.Length{Px: 120}, Height: css.Length{Px: 40}}, 900, 298, 120, 40, true},
		{"percentage width is ignored (needs containing block)",
			&css.Style{Width: css.Length{Percent: 0.5, IsPercent: true}, Height: css.Length{Auto: true}}, 900, 298, 0, 0, false},
		{"zero intrinsic reports false",
			&css.Style{Height: css.Length{Px: 24}}, 0, 100, 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := cssImageSize(c.st, c.iw, c.ih)
		if ok != c.wantOK || (ok && (w != c.wantW || h != c.wantH)) {
			t.Errorf("%s: got (%d,%d,%v) want (%d,%d,%v)", c.name, w, h, ok, c.wantW, c.wantH, c.wantOK)
		}
	}
}

func TestIRound(t *testing.T) {
	cases := map[float64]int{0.0: 1, 0.4: 1, 0.6: 1, 1.4: 1, 1.6: 2, 71.5: 72, 72.4: 72}
	for in, want := range cases {
		if got := iround(in); got != want {
			t.Errorf("iround(%v) = %d want %d", in, got, want)
		}
	}
}

// TestImgByteCacheDedupesWithinRender is the regression guard for the
// duplicate-fetch bug found live on caniuse.com: the settle loop reloads
// images after any script-driven relayout (dynamic.go), so without a
// render-scoped cache the SAME url was fetched over the network again even
// though nothing about it had changed — three images cost a second full
// round trip apiece for no reason (measured: ~500ms wasted on that page
// alone). e.ImageCache is opt-in and nil by default (cross-render reuse is a
// separate, deliberate choice — see its doc comment), so this cache must
// work with no ImageCache configured at all.
func TestImgByteCacheDedupesWithinRender(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("pretend-image-bytes"))
	}))
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	ctx := withImgByteCache(context.Background(), newImgByteCache())

	for i := 0; i < 3; i++ {
		data, ok := e.fetchImageBytes(ctx, srv.URL+"/", "/pic.png")
		if !ok {
			t.Fatalf("call %d: fetch failed", i)
		}
		if string(data) != "pretend-image-bytes" {
			t.Fatalf("call %d: data = %q", i, data)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server saw %d requests, want exactly 1 (later calls should hit the render cache)", got)
	}
}

// TestImgByteCacheScopedPerContextNotGlobal proves the cache is NOT shared
// across renders that don't share a context — e.ImageCache (nil by default)
// governs cross-render reuse, not this mechanism; a fresh render must still
// fetch, exactly as before this fix.
func TestImgByteCacheScopedPerContextNotGlobal(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("pretend-image-bytes"))
	}))
	defer srv.Close()

	e := New()
	e.Client = srv.Client()

	for i := 0; i < 2; i++ {
		ctx := withImgByteCache(context.Background(), newImgByteCache())
		if _, ok := e.fetchImageBytes(ctx, srv.URL+"/", "/pic.png"); !ok {
			t.Fatalf("render %d: fetch failed", i)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("server saw %d requests, want 2 (one per independent render)", got)
	}
}
