// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import "testing"

// TestBlockTableGrid renders testdata/block_table.html, a table with
// display:block; width:max-content (the default Marp theme's rule), and asserts
// the 2x2 grid Chrome paints: CSS 2.1 §17.2.1 wraps the rows in an anonymous
// table, so cells keep table layout inside a box as wide as max-content. The
// fixture is text-free; the engine's render was compared with Google Chrome
// headless on the same file and is pixel-identical (0 differing pixels).
// go-webengine/engine#398.
func TestBlockTableGrid(t *testing.T) {
	img := renderFixture(t, "block_table.html", 200, 120)
	// Column 1 is 40px wide (5+30+5), column 2 60px (5+50+5); rows are 30px.
	assertPixel(t, img, 2, 2, 0x99, 0xcc, 0xff, "cell (0,0) padding")
	assertPixel(t, img, 10, 10, 0xcc, 0x33, 0x33, "cell (0,0) content")
	assertPixel(t, img, 42, 10, 0x99, 0xcc, 0xff, "cell (0,1) padding")
	assertPixel(t, img, 50, 10, 0xcc, 0x33, 0x33, "cell (0,1) content")
	assertPixel(t, img, 10, 40, 0xcc, 0x33, 0x33, "row 2 under row 1")
	assertPixel(t, img, 50, 40, 0xcc, 0x33, 0x33, "row 2 col 2 under row 1")
	assertPixel(t, img, 10, 31, 0x99, 0xcc, 0xff, "row 2 padding")
	// The grid is content-wide (100px) and two rows (60px) tall: nothing
	// stretches to the 200px viewport, nothing stacks below the second row.
	assertPixel(t, img, 101, 10, 0xff, 0xff, 0xff, "right of the grid")
	assertPixel(t, img, 10, 61, 0xff, 0xff, 0xff, "below the grid")
}
