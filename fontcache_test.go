// Copyright (c) the go-webengine authors. All rights reserved.
// Use of this source code is governed by a BSD-3-Clause license that can be
// found in the LICENSE file at the root of this repository.

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"sync"
	"testing"

	"github.com/go-opentype/fonts/cabin"
	"github.com/go-opentype/fonts/lora"

	"github.com/go-webengine/engine/paint"
)

func TestFontCacheHitsAndBounds(t *testing.T) {
	c := newFontCache(2, 1<<30)
	a1, err := c.parse(cabin.TTF)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := c.parse(bytes.Clone(cabin.TTF)) // the same bytes, another slice
	if err != nil {
		t.Fatal(err)
	}
	if a1 != a2 {
		t.Error("the same bytes were parsed twice")
	}
	// A third distinct font evicts the least recently used: cabin was used
	// after lora, so lora goes.
	l1, _ := c.parse(lora.TTF)
	if _, err := c.parse(cabin.TTF); err != nil {
		t.Fatal(err)
	}
	if _, err := c.parse(lora.BoldTTF); err != nil {
		t.Fatal(err)
	}
	if c.lru.Len() != 2 {
		t.Fatalf("%d entries, bound 2", c.lru.Len())
	}
	if l2, _ := c.parse(lora.TTF); l2 == l1 {
		t.Error("the least recently used font was kept past the bound")
	}

	// The byte bound: room for one of the two, so the older goes.
	b := newFontCache(10, len(cabin.TTF)+len(lora.TTF)-1)
	b.parse(cabin.TTF)
	b.parse(lora.TTF)
	if b.lru.Len() != 1 || b.bytes != len(lora.TTF) {
		t.Errorf("%d entries, %d bytes, want lora alone", b.lru.Len(), b.bytes)
	}
	// Larger than the whole cache: parsed and returned, not kept.
	s := newFontCache(10, 10)
	if ft, err := s.parse(cabin.TTF); err != nil || ft == nil {
		t.Fatalf("oversized: %v", err)
	}
	if s.lru.Len() != 0 {
		t.Error("a font larger than the cache was kept")
	}
	// A file that does not parse is refused and not kept.
	if _, err := s.parse([]byte("not a font at all")); err == nil {
		t.Error("garbage parsed")
	}
	if s.lru.Len() != 0 {
		t.Error("a failed parse was kept")
	}
}

// TestFontCacheConcurrentMiss: renders missing at once all get a font and
// leave one entry.
func TestFontCacheConcurrentMiss(t *testing.T) {
	c := newFontCache(4, 1<<30)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.parse(cabin.TTF); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if c.lru.Len() != 1 {
		t.Errorf("%d entries for one font", c.lru.Len())
	}
}

// TestSharedFontsRenderConcurrently renders one page using a data: URI
// face from several goroutines on one Engine: the cached parsed font and the
// shared bundled fonts are read concurrently (go test -race), and every
// render matches a serial one.
func TestSharedFontsRenderConcurrently(t *testing.T) {
	uri := "data:font/ttf;base64," + base64.StdEncoding.EncodeToString(cabin.TTF)
	page := `<!DOCTYPE html><html><head><style>@font-face{font-family:'Shared';src:url(` + uri + `) format('truetype')}` +
		`html,body{margin:0}p{font-size:30px;font-family:'Shared';margin:0}em{font-family:serif}</style></head>` +
		`<body><p>Hamburgefonstiv <em>serif</em> →</p></body></html>`
	e := New()
	e.DisableJS = true
	want, _, err := e.RenderHTML(context.Background(), page, "", image.Rect(0, 0, 400, 60))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			got, _, err := e.RenderHTML(context.Background(), page, "", image.Rect(0, 0, 400, 60))
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(got.Pix, want.Pix) {
				t.Error("a concurrent render differs from the serial one")
			}
		})
	}
	wg.Wait()
}

// TestEngineFontFaces: a face the host provides sets the text exactly as the
// same file declared by the page as a data: URI does; a page declaring its
// own face for the family keeps it; a host face that does not parse is
// skipped.
func TestEngineFontFaces(t *testing.T) {
	uri := func(ttf []byte) string { return "data:font/ttf;base64," + base64.StdEncoding.EncodeToString(ttf) }
	page := func(rule string) string {
		return `<!DOCTYPE html><html><head><style>` + rule + `html,body{margin:0}p{font-size:40px;font-family:'Host Face', serif;margin:0}</style></head>` +
			`<body><p>Hamburgefonstiv</p></body></html>`
	}
	render := func(e *Engine, html string) []byte {
		t.Helper()
		img, _, err := e.RenderHTML(context.Background(), html, "", image.Rect(0, 0, 400, 60))
		if err != nil {
			t.Fatal(err)
		}
		return img.Pix
	}
	plain := New()
	plain.DisableJS = true
	declared := render(plain, page(`@font-face{font-family:'Host Face';src:url(`+uri(cabin.TTF)+`)}`))
	fallback := render(plain, page(""))

	host := New()
	host.DisableJS = true
	host.FontFaces = []LoadedFontFace{
		{Family: "Broken", Weight: 400, Data: []byte("not a font")},
		{Family: "Host Face", Weight: 400, Data: cabin.TTF},
	}
	if got := render(host, page("")); !bytes.Equal(got, declared) {
		t.Error("a host face does not render like the same face declared by the page")
	}
	parsed, err := ParseFontFace("Host Face", 400, false, cabin.TTF)
	if err != nil {
		t.Fatal(err)
	}
	host.FontFaces = []LoadedFontFace{parsed}
	if got := render(host, page("")); !bytes.Equal(got, declared) {
		t.Error("a face from ParseFontFace does not render like the same face declared by the page")
	}
	if _, err := ParseFontFace("x", 400, false, []byte("not a font")); err == nil {
		t.Error("ParseFontFace accepted garbage")
	}
	if bytes.Equal(declared, fallback) {
		t.Fatal("control: the face changes nothing")
	}
	own := render(plain, page(`@font-face{font-family:'Host Face';src:url(`+uri(lora.TTF)+`)}`))
	if got := render(host, page(`@font-face{font-family:'Host Face';src:url(`+uri(lora.TTF)+`)}`)); !bytes.Equal(got, own) {
		t.Error("the page's own face for the family did not win over the host's")
	}
}

// BenchmarkRenderInlineFontFace renders a small page that carries its
// typeface as a data: URI, as a slide deck exported for offline rendering
// does on every slide.
func BenchmarkRenderInlineFontFace(b *testing.B) {
	uri := "data:font/ttf;base64," + base64.StdEncoding.EncodeToString(cabin.TTF)
	page := `<!DOCTYPE html><html><head><style>@font-face{font-family:'Inline';src:url(` + uri + `) format('truetype')}` +
		`html,body{margin:0}h1{font:bold 48px 'Inline'}p{font-size:24px}</style></head>` +
		`<body><h1>A slide title</h1><p>Some text in the bundled sans, and <code>code</code>.</p></body></html>`
	e := New()
	e.DisableJS = true
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := e.RenderHTML(context.Background(), page, "", image.Rect(0, 0, 1280, 720)); err != nil {
			b.Fatal(err)
		}
	}
}

// TestRegisterFontFacesUsesParsedFont: a face LoadFontFaces parsed is
// registered without parsing again, and a face built by a caller (no
// parsed font) still registers from its bytes.
func TestRegisterFontFacesUsesParsedFont(t *testing.T) {
	ft, err := parsedFonts.parse(cabin.TTF)
	if err != nil {
		t.Fatal(err)
	}
	fonts := paint.NewFonts()
	n := RegisterFontFaces(fonts, []LoadedFontFace{
		{Family: "parsed", Weight: 400, Data: []byte("unused when the font is there"), font: ft},
		{Family: "bytes", Weight: 400, Data: cabin.TTF},
		{Family: "broken", Weight: 400, Data: []byte("not a font")},
	})
	if n != 2 || !fonts.Registered("parsed") || !fonts.Registered("bytes") || fonts.Registered("broken") {
		t.Errorf("registered %d", n)
	}
}
