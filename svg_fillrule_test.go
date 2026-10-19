// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image/color"
	"testing"
)

// TestRasterizeSVGFillRuleAndArcFlags: go-gfx/oksvg v0.2.0 honours fill-rule,
// and reads arc flags written without separators (the octocat's
// "A9.564 9.564 0 0110 4.844"), which srwiley/oksvg did not.
func TestRasterizeSVGFillRuleAndArcFlags(t *testing.T) {
	alpha := func(src string, x, y int) uint8 {
		img, ok := RasterizeSVG([]byte(src), 100, 100, "")
		if !ok {
			t.Fatal("not rasterised")
		}
		return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A
	}
	donut := func(rule string) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><path fill="#000"` + rule + ` d="M10 10H90V90H10Z M30 30H70V70H30Z"/></svg>`
	}
	if a := alpha(donut(` fill-rule="evenodd"`), 50, 50); a != 0 {
		t.Errorf("evenodd: the hole is filled (alpha %d)", a)
	}
	if a := alpha(donut(""), 50, 50); a != 255 {
		t.Errorf("nonzero, same direction: the hole is not filled (alpha %d)", a)
	}
	arc := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><path d="M0 50A50 50 0 0110 0L10 50Z"/></svg>`
	if a := alpha(arc, 5, 40); a != 255 {
		t.Errorf("a path with compact arc flags is cut short (alpha %d)", a)
	}
}
