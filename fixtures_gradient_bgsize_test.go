// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import "testing"

// TestGradientBackgroundSize renders testdata/gradient_bgsize.html and asserts
// background-size on gradient layers (CSS Backgrounds 3 §2.9; a gradient has no
// natural size, so CSS Images 3 §4.3.1 sizes `auto` as the positioning area).
// The whole fixture was compared with Google Chrome headless: 6360 pixels differ
// by at most 1/255 (gradient rounding), none structurally.
// go-webengine/engine#400.
func TestGradientBackgroundSize(t *testing.T) {
	img := renderFixture(t, "gradient_bgsize.html", 340, 180)
	near := func(x, y int, r, g, b uint8, tol int, what string) {
		t.Helper()
		c := img.RGBAAt(x, y)
		d := func(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }
		if d(c.R, r) > tol || d(c.G, g) > tol || d(c.B, b) > tol {
			t.Errorf("%s: (%d,%d) = #%02x%02x%02x want ~#%02x%02x%02x", what, x, y, c.R, c.G, c.B, r, g, b)
		}
	}
	// a: 100% x 40px strip at the top of a 160x80 box, white below.
	near(10, 10, 0x00, 0x66, 0xcc, 1, "strip")
	near(10, 39, 0x00, 0x66, 0xcc, 1, "strip last row")
	near(10, 41, 0xff, 0xff, 0xff, 0, "below strip")
	// b: 40x40 tiles, each restarts red -> blue.
	near(171, 5, 0xc0, 0x00, 0x0c, 12, "tile 1 start red")
	near(209, 5, 0x0c, 0x00, 0xc0, 12, "tile 1 end blue")
	near(211, 5, 0xc0, 0x00, 0x0c, 12, "tile 2 restarts red")
	near(171, 45, 0xc0, 0x00, 0x0c, 12, "row 2 restarts red")
	// c: 50% x auto (auto = box height), bottom right, no-repeat.
	near(120, 130, 0x00, 0xaa, 0x00, 1, "right half green")
	near(30, 130, 0xff, 0xff, 0xff, 0, "left half empty")
	// d: 30x20 repeat-x only: one 20px strip, white below.
	near(175, 95, 0xaa, 0x00, 0xaa, 1, "repeat-x strip")
	near(175, 115, 0xff, 0xff, 0xff, 0, "no repeat-y")
}
