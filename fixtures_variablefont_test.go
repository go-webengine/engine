// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// inkRows renders html (a body of weighted paragraphs set in the variable
// font) and returns the number of non-white pixels in each of n horizontal
// bands of height band: a heavier weight puts more ink in its band.
func variableFontInk(t *testing.T, html string, bands, band int) []int {
	t.Helper()
	dir, err := filepath.Abs("testdata/fonts")
	if err != nil {
		t.Fatal(err)
	}
	e := New()
	e.FileRoot = dir
	base := (&url.URL{Scheme: "file", Path: dir + "/"}).String()
	img, _, err := e.RenderHTML(context.Background(), html, base, image.Rect(0, 0, 420, bands*band))
	if err != nil {
		t.Fatal(err)
	}
	ink := make([]int, bands)
	for y := 0; y < bands*band; y++ {
		for x := 0; x < 420; x++ {
			if c := img.RGBAAt(x, y); c.R < 128 {
				ink[y/band]++
			}
		}
	}
	return ink
}

// TestVariableFontWeights: a variable font (Outfit, SIL OFL, wght 100..900)
// declared with a weight range is drawn at the weight each paragraph asks for,
// not at its default instance (go-webengine/engine#399). The text is measured
// and painted with the instanced advances and outlines, so ink grows with
// weight.
func TestVariableFontWeights(t *testing.T) {
	if _, err := os.Stat("testdata/fonts/Outfit-wght.ttf"); err != nil {
		t.Skip(err)
	}
	html := `<!doctype html><meta charset="utf-8"><style>
@font-face{font-family:V;src:url(Outfit-wght.ttf);font-weight:100 900}
body{margin:0;font-family:V,serif;font-size:32px;line-height:40px;color:#000}
p{margin:0;height:40px}
</style>
<p style="font-weight:100">Weight sample</p>
<p style="font-weight:400">Weight sample</p>
<p style="font-weight:700">Weight sample</p>
<p style="font-weight:900">Weight sample</p>`
	ink := variableFontInk(t, html, 4, 40)
	t.Logf("ink by weight 100/400/700/900: %v", ink)
	if ink[0] == 0 {
		t.Fatal("no text drawn: the @font-face did not load")
	}
	for i := 1; i < len(ink); i++ {
		if ink[i] <= ink[i-1]+ink[i-1]/10 {
			t.Errorf("weight step %d: ink %d not clearly above %d", i, ink[i], ink[i-1])
		}
	}
}
