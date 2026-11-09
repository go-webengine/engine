// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// chromeWords are the horizontal extents (start, end, in pixels, sub-pixel) of
// the five words of "Lorem ipsum dolor sit amet" in each row of
// testdata/subpixel_words.html, measured on a Google Chrome headless screenshot
// of that file (window 640x240) with wordEdges below: Open Sans variable at 16,
// 22.4 and 30.8 px, weights 400 and 700. go-webengine/engine#407.
var chromeWords = [6][5][2]float64{
	{{11.51, 57.09}, {63.76, 107.33}, {113.60, 152.00}, {157.32, 173.64}, {178.71, 216.15}},
	{{11.38, 60.33}, {66.69, 113.57}, {119.69, 160.71}, {165.67, 184.49}, {189.67, 230.49}},
	{{12.05, 76.00}, {85.18, 146.27}, {154.92, 208.87}, {216.00, 239.00}, {246.20, 298.54}},
	{{11.90, 80.42}, {89.35, 154.95}, {163.44, 221.00}, {228.15, 254.35}, {261.64, 318.59}},
	{{12.85, 100.67}, {113.33, 197.41}, {209.33, 283.74}, {293.41, 324.95}, {334.80, 406.67}},
	{{12.62, 106.87}, {119.13, 209.34}, {220.75, 300.00}, {309.76, 345.79}, {355.92, 434.27}},
}

// wordEdges returns the sub-pixel extents of the words in the 40px row of img:
// a word is a run of columns holding ink, ended by gap blank columns, its edges
// taken from the coverage of the first and last inked column.
func wordEdges(img *image.RGBA, row, gap int) [][2]float64 {
	w := img.Rect.Dx()
	cov := make([]float64, w)
	for x := 0; x < w; x++ {
		for y := row * 40; y < row*40+40; y++ {
			if c := 1 - float64(img.RGBAAt(x, y).R)/255; c > cov[x] {
				cov[x] = c
			}
		}
	}
	var out [][2]float64
	in, blank := false, 0
	var start float64
	for x := 0; x <= w; x++ {
		c := 0.0
		if x < w {
			c = cov[x]
		}
		switch {
		case c > 0.08:
			if !in {
				in, start = true, float64(x)+(1-c)
			}
			blank = 0
		case in:
			blank++
			if blank == gap {
				e := x - gap
				out = append(out, [2]float64{start, float64(e) + cov[e]})
				in = false
			}
		}
	}
	return out
}

// TestRasterTextMatchesChromeWords: the raster painter places each word, and
// sizes it, as Chrome does, to within half a pixel, at three sizes (one not a
// whole number of pixels) and two weights. It used to draw on the whole-pixel
// grid: words started where layout put them but came out up to 2px short, so
// the gaps between them looked uneven and too wide.
func TestRasterTextMatchesChromeWords(t *testing.T) {
	dir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	e := New()
	e.FileRoot = dir
	base := (&url.URL{Scheme: "file", Path: dir + "/"}).String()
	src, err := os.ReadFile(filepath.Join("testdata", "subpixel_words.html"))
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := e.RenderHTML(context.Background(), string(src), base, image.Rect(0, 0, 640, 240))
	if err != nil {
		t.Fatal(err)
	}
	gaps := [6]int{4, 4, 5, 5, 6, 6}
	// A word's start is held to half a pixel. Its width is the difference of
	// two edges, each estimated from one column's coverage (about a quarter
	// pixel of noise apiece, in Chrome's image as much as in ours), so it gets
	// 0.6.
	const tol, widthTol = 0.5, 0.6
	for row := 0; row < 6; row++ {
		got := wordEdges(img, row, gaps[row])
		if len(got) != 5 {
			t.Errorf("row %d: %d words, want 5: %v", row, len(got), got)
			continue
		}
		for i, g := range got {
			want := chromeWords[row][i]
			if math.Abs(g[0]-want[0]) > tol {
				t.Errorf("row %d word %d: starts at %.2f, Chrome %.2f", row, i, g[0], want[0])
			}
			if gw, ww := g[1]-g[0], want[1]-want[0]; math.Abs(gw-ww) > widthTol {
				t.Errorf("row %d word %d: %.2f px wide, Chrome %.2f", row, i, gw, ww)
			}
		}
	}
}
