// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// pngDataURI encodes a solid w×h PNG as a base64 data: URI.
func pngDataURI(t *testing.T, w, h int, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestLoadBackgroundImagesDataURI(t *testing.T) {
	uri := pngDataURI(t, 3, 3, color.RGBA{10, 20, 30, 255})
	html := `<html><body><div style="background-image: url('` + uri + `')">x</div></body></html>`
	root, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}
	doc := &Document{URL: "https://example.com/", Root: root}
	sm := css.CascadeVW(root, 1024, nil)

	e := New()
	got := e.loadBackgroundImages(context.Background(), doc, sm)
	if got == nil {
		t.Fatal("expected a background bitmap, got nil")
	}
	img, ok := got[uri]
	if !ok {
		t.Fatalf("no entry for the data URI; keys=%v", keys(got))
	}
	if img.Bounds().Dx() != 3 || img.Bounds().Dy() != 3 {
		t.Errorf("decoded size = %v want 3x3", img.Bounds())
	}
}

func TestLoadBackgroundImagesNoneAndGradient(t *testing.T) {
	// A gradient-only background needs no bitmap; the result is nil.
	html := `<html><body><div style="background-image: linear-gradient(red, blue)">x</div></body></html>`
	root, _ := dom.Parse(html)
	doc := &Document{URL: "https://example.com/", Root: root}
	sm := css.CascadeVW(root, 1024, nil)
	if got := New().loadBackgroundImages(context.Background(), doc, sm); got != nil {
		t.Errorf("gradient-only should need no bitmaps, got %v", keys(got))
	}
	// A document with no background-image at all also returns nil.
	root2, _ := dom.Parse(`<html><body><div>x</div></body></html>`)
	doc2 := &Document{URL: "https://example.com/", Root: root2}
	sm2 := css.CascadeVW(root2, 1024, nil)
	if got := New().loadBackgroundImages(context.Background(), doc2, sm2); got != nil {
		t.Errorf("no bg-image should return nil, got %v", keys(got))
	}
}

func TestLoadBackgroundImagesUndecodable(t *testing.T) {
	// A data URI whose payload is not a valid image fails to decode and is skipped.
	bad := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("not a png"))
	html := `<html><body><div style="background-image: url('` + bad + `')">x</div></body></html>`
	root, _ := dom.Parse(html)
	doc := &Document{URL: "https://example.com/", Root: root}
	sm := css.CascadeVW(root, 1024, nil)
	if got := New().loadBackgroundImages(context.Background(), doc, sm); got != nil {
		t.Errorf("undecodable image should be skipped (nil), got %v", keys(got))
	}
}

func TestLoadBackgroundImagesDedup(t *testing.T) {
	uri := pngDataURI(t, 2, 2, color.RGBA{1, 2, 3, 255})
	// Two elements referencing the same URL fetch/decode it once.
	html := `<html><body>` +
		`<div style="background-image: url('` + uri + `')">a</div>` +
		`<div style="background-image: url('` + uri + `')">b</div>` +
		`</body></html>`
	root, _ := dom.Parse(html)
	doc := &Document{URL: "https://example.com/", Root: root}
	sm := css.CascadeVW(root, 1024, nil)
	got := New().loadBackgroundImages(context.Background(), doc, sm)
	if len(got) != 1 {
		t.Errorf("dedup failed: %d entries, keys=%v", len(got), keys(got))
	}
}

func keys(m map[string]image.Image) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		if len(k) > 40 {
			k = k[:40] + "..."
		}
		out = append(out, k)
	}
	return out
}
