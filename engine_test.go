// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

func TestResolveURL(t *testing.T) {
	cases := []struct {
		base, ref, want string
		ok              bool
	}{
		{"https://a.com/x/y", "z.png", "https://a.com/x/z.png", true},
		{"https://a.com/", "https://b.com/q", "https://b.com/q", true},
		{"https://a.com/", "//cdn.com/i.png", "https://cdn.com/i.png", true},
		{"https://a.com/", "  ", "", false},
		{"://bad", "x", "", false},
	}
	for _, c := range cases {
		got, ok := resolveURL(c.base, c.ref)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("resolveURL(%q,%q) = %q,%v want %q,%v", c.base, c.ref, got, ok, c.want, c.ok)
		}
	}
}

func TestDecodeDataURI(t *testing.T) {
	// base64 payload "Hi" = "SGk="
	if b, ok := decodeDataURI("data:text/plain;base64,SGk="); !ok || string(b) != "Hi" {
		t.Errorf("base64 data uri = %q %v", b, ok)
	}
	if b, ok := decodeDataURI("data:text/plain,Hi"); !ok || string(b) != "Hi" {
		t.Errorf("plain data uri = %q %v", b, ok)
	}
	if _, ok := decodeDataURI("data:base64,%%%"); ok {
		t.Error("bad base64 should fail")
	}
	if _, ok := decodeDataURI("data:nocomma"); ok {
		t.Error("no-comma should fail")
	}
}

func TestDecodeCharset(t *testing.T) {
	out, err := decodeCharset([]byte("hello"), "text/html; charset=utf-8")
	if err != nil || string(out) != "hello" {
		t.Errorf("utf8 = %q %v", out, err)
	}
	// Latin-1 0xE9 (é) declared via header is converted to UTF-8 (0xC3 0xA9).
	out, err = decodeCharset([]byte{0xE9}, "text/html; charset=iso-8859-1")
	if err != nil || !bytes.Equal(out, []byte{0xC3, 0xA9}) {
		t.Errorf("latin1 = %v %v", out, err)
	}
}

func TestFillHelpers(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	fillWhite(img)
	if c := img.RGBAAt(0, 0); c.R != 255 || c.A != 255 {
		t.Errorf("fillWhite = %+v", c)
	}
	fillColor(img, css.Color{R: 10, G: 20, B: 30, A: 255})
	if c := img.RGBAAt(1, 1); c.R != 10 || c.G != 20 || c.B != 30 || c.A != 255 {
		t.Errorf("fillColor = %+v", c)
	}
}

func TestPageBackground(t *testing.T) {
	root, _ := dom.Parse(`<html><body style="background-color:#eee">x</body></html>`)
	sm := css.Cascade(root)
	if c, ok := pageBackground(root, sm); !ok || c.R != 0xee {
		t.Errorf("body bg = %v %v", c, ok)
	}
	// html background used when body has none.
	root2, _ := dom.Parse(`<html style="background-color:navy"><body>x</body></html>`)
	sm2 := css.Cascade(root2)
	if c, ok := pageBackground(root2, sm2); !ok || c != (css.Color{R: 0, G: 0, B: 128, A: 255}) {
		t.Errorf("html bg = %v %v", c, ok)
	}
	// none set → not ok.
	root3, _ := dom.Parse(`<html><body>x</body></html>`)
	if _, ok := pageBackground(root3, css.Cascade(root3)); ok {
		t.Error("expected no background")
	}
}

func TestEncodePNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	b, err := EncodePNG(img)
	if err != nil || len(b) == 0 {
		t.Fatalf("encode = %d %v", len(b), err)
	}
	if got, _, err := image.Decode(bytes.NewReader(b)); err != nil || got.Bounds().Dx() != 3 {
		t.Errorf("decode roundtrip failed: %v", err)
	}
}

const goldenFixture = `<html><head><title>Fix</title>` +
	`<style>body{background-color:#eeeeee} h1{color:#003366} a{color:#0000ee}</style>` +
	`</head><body style="margin:10px">` +
	`<h1>Hi</h1><p>Hello <strong>world</strong> and <a href="#">link</a>.</p>` +
	`</body></html>`

func TestRenderDocumentGolden(t *testing.T) {
	root, err := dom.Parse(goldenFixture)
	if err != nil {
		t.Fatal(err)
	}
	doc := &Document{URL: "https://fixture.test/", Title: dom.Title(root), Root: root, HTML: goldenFixture}

	e := New()
	img, info, err := e.RenderDocument(context.Background(), doc, image.Rect(0, 0, 320, 160))
	if err != nil {
		t.Fatal(err)
	}
	// Precise, glyph-independent invariants.
	if img.Rect.Dx() != 320 {
		t.Errorf("width = %d", img.Rect.Dx())
	}
	if info.Title != "Fix" {
		t.Errorf("title = %q", info.Title)
	}
	if info.ContentHeight <= 0 {
		t.Errorf("content height = %d", info.ContentHeight)
	}
	// Backdrop (#eeeeee) fills the viewport, including the top-left corner.
	if c := img.RGBAAt(1, 1); c.R != 0xee || c.G != 0xee || c.B != 0xee {
		t.Errorf("backdrop pixel = %+v want #eeeeee", c)
	}

	png, err := EncodePNG(img)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "golden", "fixture.png")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, png, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden updated")
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_GOLDEN=1 to create): %v", err)
	}
	if !bytes.Equal(png, want) {
		t.Errorf("render does not match golden fixture.png (%d vs %d bytes)", len(png), len(want))
	}
}

// TestLiveRender exercises the full network path when connectivity is
// available; it skips (not fails) when the network is unreachable.
func TestLiveRender(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	img, info, err := New().Render(ctx, "https://example.com/", image.Rect(0, 0, 1024, 768))
	if err != nil {
		t.Skipf("network unavailable: %v", err)
	}
	if img.Rect.Dx() != 1024 {
		t.Errorf("width = %d", img.Rect.Dx())
	}
	if info.Title == "" {
		t.Error("expected a non-empty title")
	}
	if b, err := New().Screenshot(ctx, "https://example.com/", image.Rect(0, 0, 400, 300)); err != nil || len(b) == 0 {
		t.Errorf("screenshot = %d %v", len(b), err)
	}
}

func TestRenderDocumentWithDataImage(t *testing.T) {
	// Build a data: URI PNG so the whole image pipeline runs offline: an 8x8
	// image at a 4px viewport also exercises the downscale-to-fit branch.
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 200, 0, 0, 255
	}
	png, err := EncodePNG(src)
	if err != nil {
		t.Fatal(err)
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	html := `<html><body style="margin:0"><p><img src="` + uri + `"></p>` +
		`<img style="display:none" src="` + uri + `"><img></body></html>`
	root, _ := dom.Parse(html)
	doc := &Document{URL: "https://x.test/", Root: root}

	img, _, err := New().RenderDocument(context.Background(), doc, image.Rect(0, 0, 4, 30))
	if err != nil {
		t.Fatal(err)
	}
	// The 8x8 red image, scaled to fit width 4, drew reddish pixels somewhere
	// (its exact y depends on the <p> UA top margin).
	if !hasReddish(img) {
		t.Error("expected a reddish pixel from the scaled data-URI image")
	}
}

func hasReddish(img *image.RGBA) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c := img.RGBAAt(x, y); c.R > 150 && c.G < 60 && c.B < 60 {
				return true
			}
		}
	}
	return false
}

func TestMaxImagesCap(t *testing.T) {
	e := New()
	e.MaxImages = 0 // no images loaded regardless of markup
	root, _ := dom.Parse(`<html><body><img src="data:image/png;base64,AAAA"></body></html>`)
	doc := &Document{URL: "https://x.test/", Root: root}
	sizes, bitmaps := e.loadImages(context.Background(), doc, css.Cascade(root), 100)
	if len(sizes) != 0 || len(bitmaps) != 0 {
		t.Errorf("MaxImages=0 should load nothing, got %d/%d", len(sizes), len(bitmaps))
	}
}

func TestFetchImageBytesSchemes(t *testing.T) {
	e := New()
	// data: URI path.
	if b, ok := e.fetchImageBytes(context.Background(), "https://a/", "data:text/plain,Hi"); !ok || string(b) != "Hi" {
		t.Errorf("data uri = %q %v", b, ok)
	}
	// Non-http scheme is rejected without a network call.
	if _, ok := e.fetchImageBytes(context.Background(), "https://a/", "ftp://x/i.png"); ok {
		t.Error("ftp scheme should be rejected")
	}
	// Empty/unresolvable src rejected.
	if _, ok := e.fetchImageBytes(context.Background(), "https://a/", "   "); ok {
		t.Error("empty src should be rejected")
	}
}

// mapImageCache is a tiny in-memory ImageCache for the hook test.
type mapImageCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *mapImageCache) Get(url string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[url]
	return b, ok
}
func (c *mapImageCache) Put(url string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[url] = data
}

func TestFetchImageBytesUsesCache(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	e.ImageCache = &mapImageCache{m: map[string][]byte{}}
	ctx := context.Background()

	// Miss → downloads and populates the cache.
	if b, ok := e.fetchImageBytes(ctx, srv.URL+"/", srv.URL+"/a.png"); !ok || string(b) != "PNGDATA" {
		t.Fatalf("first fetch = %q %v", b, ok)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("first fetch hits = %d, want 1", hits)
	}
	// Hit → served from the cache with no network.
	if b, ok := e.fetchImageBytes(ctx, srv.URL+"/", srv.URL+"/a.png"); !ok || string(b) != "PNGDATA" {
		t.Fatalf("cached fetch = %q %v", b, ok)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("second fetch hit the network (%d requests); cache should have served it", n)
	}
}

func TestPackageWrappersBadURL(t *testing.T) {
	// Package-level wrappers propagate errors from an unresolvable URL.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := Render(ctx, "http://nonexistent.invalid.", image.Rect(0, 0, 10, 10)); err == nil {
		t.Skip("resolver unexpectedly succeeded")
	}
	if _, err := Screenshot(ctx, "://bad-url", image.Rect(0, 0, 10, 10)); err == nil {
		t.Error("expected error for bad url")
	}
}

func TestRenderHTMLOffline(t *testing.T) {
	e := New()
	src := `<html><head><title>Offline</title></head><body style="margin:0">` +
		`<div style="display:flex"><div style="width:100px">A</div>` +
		`<div style="width:100px">B</div></div></body></html>`
	img, info, err := e.RenderHTML(context.Background(), src, "https://demo.test/", image.Rect(0, 0, 400, 200))
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "Offline" {
		t.Errorf("title = %q", info.Title)
	}
	if img.Rect.Dx() != 400 {
		t.Errorf("width = %d", img.Rect.Dx())
	}
}

func TestRenderHTMLParseError(t *testing.T) {
	// html.Parse tolerates almost anything, so verify a normal string succeeds
	// and the entry point is exercised end-to-end without a network fetch.
	if _, _, err := New().RenderHTML(context.Background(), "<p>hi</p>", "", image.Rect(0, 0, 50, 50)); err != nil {
		t.Fatalf("RenderHTML minimal doc: %v", err)
	}
}
