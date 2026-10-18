// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"fmt"
	"image"
	"testing"
)

// inkRows returns the first and last rows of column x painted pure red.
func inkRows(img *image.RGBA, x int) (first, last int) {
	first, last = -1, -1
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		c := img.RGBAAt(x, y)
		if c.R == 0xff && c.G == 0 && c.B == 0 {
			if first < 0 {
				first = y
			}
			last = y
		}
	}
	return first, last
}

// inkCols returns the first and last columns of row y painted pure red.
func inkCols(img *image.RGBA, y int) (first, last int) {
	first, last = -1, -1
	for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
		c := img.RGBAAt(x, y)
		if c.R == 0xff && c.G == 0 && c.B == 0 {
			if first < 0 {
				first = x
			}
			last = x
		}
	}
	return first, last
}

// TestFlexCentringOnDefiniteContainer pins main- and cross-axis centring of a
// single item in a 200x200 flex container (CSS Flexbox 1 §8.2 justify-content,
// §8.3 align-items; CSS Box Alignment 3 place-content shorthand and the
// safe/unsafe overflow-position keywords). Marp's default/gaia/uncover themes
// centre every slide this way: `section { display:flex; flex-flow:column
// nowrap; place-content: safe center center }`.
func TestFlexCentringOnDefiniteContainer(t *testing.T) {
	const page = `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<div style="width:200px;height:200px;%s"><div style="%s;background:#ff0000"></div></div></body></html>`
	cases := []struct {
		name, box, child string
		// want vertical span (rows) at x=probeX, horizontal span at y=probeY
		y0, y1, x0, x1 int
	}{
		{"column justify center", "display:flex;flex-direction:column;justify-content:center", "height:20px", 90, 109, 0, 199},
		{"row align-items center", "display:flex;align-items:center", "width:20px;height:20px", 90, 109, 0, 19},
		{"row justify+align center", "display:flex;justify-content:center;align-items:center", "width:20px;height:20px", 90, 109, 90, 109},
		{"column place-content center", "display:flex;flex-flow:column nowrap;place-content:center", "height:20px", 90, 109, 0, 199},
		{"column place-content safe center center", "display:flex;flex-flow:column nowrap;place-content:safe center center", "height:20px", 90, 109, 0, 199},
		{"column justify safe center", "display:flex;flex-direction:column;justify-content:safe center", "height:20px", 90, 109, 0, 199},
		{"column justify unsafe center", "display:flex;flex-direction:column;justify-content:unsafe center", "height:20px", 90, 109, 0, 199},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			html := fmt.Sprintf(page, tc.box, tc.child)
			img := renderString(t, html, 200, 200)
			probeX, probeY := (tc.x0+tc.x1)/2, (tc.y0+tc.y1)/2
			if y0, y1 := inkRows(img, probeX); y0 != tc.y0 || y1 != tc.y1 {
				t.Errorf("rows at x=%d: %d..%d, want %d..%d", probeX, y0, y1, tc.y0, tc.y1)
			}
			if x0, x1 := inkCols(img, probeY); x0 != tc.x0 || x1 != tc.x1 {
				t.Errorf("cols at y=%d: %d..%d, want %d..%d", probeY, x0, x1, tc.x0, tc.x1)
			}
		})
	}
}
