// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/go-opentype/fonts/cabin"
)

// A host's Fetcher answers every http(s) URL a render needs: the page, its
// style sheet, its image and its font, in a build with network or without.
func TestFetcher(t *testing.T) {
	var img bytes.Buffer
	red := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range red.Pix {
		red.Pix[i] = []byte{255, 0, 0, 255}[i%4]
	}
	if err := png.Encode(&img, red); err != nil {
		t.Fatal(err)
	}
	site := map[string]struct {
		body  []byte
		ctype string
	}{
		"https://site.test/":          {[]byte(`<!DOCTYPE html><html><head><link rel="stylesheet" href="s.css"></head><body><p>Partenaires</p><img src="r.png" width="40" height="40"></body></html>`), "text/html; charset=utf-8"},
		"https://site.test/s.css":     {[]byte(`@font-face{font-family:Cab;src:url(c.ttf)}body{margin:0;background:#00f}p{font-family:Cab;font-size:30px;color:#fff;margin:0}`), "text/css"},
		"https://site.test/r.png":     {img.Bytes(), "image/png"},
		"https://site.test/c.ttf":     {cabin.TTF, "font/ttf"},
		"https://site.test/huge.html": {bytes.Repeat([]byte("x"), maxPageBytes+10), "text/html"},
	}
	var mu sync.Mutex
	asked := map[string]int{}
	e := New()
	e.DisableJS = true
	e.Fetcher = func(_ context.Context, url string) ([]byte, string, error) {
		mu.Lock()
		asked[url]++
		mu.Unlock()
		r, ok := site[url]
		if !ok {
			return nil, "", errors.New("404 " + url)
		}
		return r.body, r.ctype, nil
	}
	out, info, err := e.Render(context.Background(), "https://site.test/", image.Rect(0, 0, 300, 100))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"https://site.test/", "https://site.test/s.css", "https://site.test/r.png", "https://site.test/c.ttf"} {
		if asked[u] == 0 {
			t.Errorf("%s was not fetched: %v", u, asked)
		}
	}
	if info.URL != "https://site.test/" {
		t.Errorf("URL %q", info.URL)
	}
	// the sheet's blue background and the image's red pixels are drawn
	reds, blues := 0, 0
	for y := 0; y < out.Bounds().Dy(); y++ {
		for x := 0; x < out.Bounds().Dx(); x++ {
			switch c := out.RGBAAt(x, y); c {
			case color.RGBA{255, 0, 0, 255}:
				reds++
			case color.RGBA{0, 0, 255, 255}:
				blues++
			}
		}
	}
	if reds < 100 || blues < 1000 {
		t.Fatalf("%d red and %d blue pixels", reds, blues)
	}
	// a body is bounded as the client's is
	doc, err := e.Fetch(context.Background(), "https://site.test/huge.html")
	if err != nil || len(doc.HTML) > maxPageBytes {
		t.Fatalf("huge page: %v, %d bytes", err, len(doc.HTML))
	}
	if _, err := e.Fetch(context.Background(), "https://site.test/missing"); err == nil {
		t.Fatal("a failed fetch gave a document")
	}
	if b, ok := e.fetchBytes(context.Background(), "https://site.test/missing", 10); ok || b != nil {
		t.Fatal("a failed sub-resource was had")
	}
	if b, ok := e.fetchBytes(context.Background(), "https://site.test/c.ttf", 10); !ok || len(b) != 10 {
		t.Fatalf("a sub-resource over its bound: %d bytes, %v", len(b), ok)
	}
}
