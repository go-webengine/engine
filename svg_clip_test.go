// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image/color"
	"testing"
)

// TestRasterizeSVGClipPath: an SVG clipPath is honoured (the rasteriser is
// go-gfx/oksvg, which implements it; srwiley/oksvg dropped the property).
func TestRasterizeSVGClipPath(t *testing.T) {
	src := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100">
<defs><clipPath id="c"><circle cx="50" cy="50" r="30"/></clipPath></defs>
<rect width="100" height="100" fill="#ff0000" clip-path="url(#c)"/></svg>`
	img, ok := RasterizeSVG([]byte(src), 100, 100, "")
	if !ok {
		t.Fatal("not rasterised")
	}
	alpha := func(x, y int) uint8 { return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A }
	if a := alpha(50, 50); a != 255 {
		t.Errorf("inside the clip: alpha %d", a)
	}
	for _, p := range [][2]int{{5, 5}, {95, 5}, {50, 90}} {
		if a := alpha(p[0], p[1]); a != 0 {
			t.Errorf("outside the clip at %v: alpha %d", p, a)
		}
	}
}
