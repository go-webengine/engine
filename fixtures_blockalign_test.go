// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"
	"testing"
)

// redRows returns the first and last rows of column x painted pure red.
func redRows(img *image.RGBA, x int) (first, last int) {
	first, last = -1, -1
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		if c := img.RGBAAt(x, y); c.R == 0xff && c.G == 0 && c.B == 0 {
			if first < 0 {
				first = y
			}
			last = y
		}
	}
	return first, last
}

// TestBlockAlignContent pins align-content on a BLOCK container (CSS Box
// Alignment 3 §5.1.1): the block's whole content, as a unit, is aligned in
// its content box along the block axis; normal behaves as start; a
// <content-distribution> value uses its fallback (space-between → start,
// space-around/space-evenly → center, §4.3); without an <overflow-position>
// it is safe, so overflowing content stays at the start edge. Marp's gaia
// theme centres `section.lead` this way: `place-content: safe center center`
// on a display:block 720px section. A scroll container (overflow:hidden) is
// still a block container and is aligned too; a table cell keeps its
// vertical-align behaviour.
func TestBlockAlignContent(t *testing.T) {
	page := func(box, inner string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
			`<div style="width:200px;height:200px;` + box + `">` + inner + `</div></body></html>`
	}
	const child = `<div style="height:20px;background:#ff0000"></div>`
	cases := []struct {
		name, box, inner string
		y0, y1           int
	}{
		{"center", "align-content:center", child, 90, 109},
		{"end", "align-content:end", child, 180, 199},
		{"flex-end", "align-content:flex-end", child, 180, 199},
		{"normal", "align-content:normal", child, 0, 19},
		{"start", "align-content:start", child, 0, 19},
		{"space-between", "align-content:space-between", child, 0, 19},
		{"space-around", "align-content:space-around", child, 90, 109},
		{"space-evenly", "align-content:space-evenly", child, 90, 109},
		{"place-content safe center center", "place-content:safe center center", child, 90, 109},
		{"unsafe center", "align-content:unsafe center", child, 90, 109},
		{"padding", "box-sizing:border-box;padding:10px;align-content:center", child, 90, 109},
		{"two children", "align-content:center", child + child, 80, 119},
		{"overflow hidden", "overflow:hidden;align-content:center", child, 90, 109},
		{"inline content", "align-content:center;line-height:20px;font-size:16px", `<span style="display:inline-block;width:20px;height:20px;background:#ff0000;vertical-align:top"></span>`, 90, 109},
		// Overflowing content: safe → stays at the start edge (and, being
		// scrollable overflow, extends the full-page capture to 300px).
		{"overflow safe", "align-content:center", `<div style="height:300px;background:#ff0000"></div>`, 0, 299},
		// Auto height: no free space, nothing moves.
		{"auto height", "height:auto;align-content:center", child, 0, 19},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, page(tc.box, tc.inner), 200, 200)
			x := 10
			if y0, y1 := redRows(img, x); y0 != tc.y0 || y1 != tc.y1 {
				t.Errorf("red rows at x=%d: %d..%d, want %d..%d", x, y0, y1, tc.y0, tc.y1)
			}
		})
	}

	// A table cell keeps vertical-align: middle centres, top stays put, and
	// align-content on the cell does not double the shift.
	cell := func(style string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}td{padding:0}</style></head><body>` +
			`<table style="border-spacing:0"><tr style="height:200px"><td style="width:200px;` + style + `">` +
			`<div style="height:20px;background:#ff0000"></div></td></tr></table></body></html>`
	}
	for _, tc := range []struct {
		style  string
		y0, y1 int
	}{
		{"vertical-align:middle", 90, 109},
		{"vertical-align:top", 0, 19},
		{"vertical-align:middle;align-content:center", 90, 109},
	} {
		img := renderString(t, cell(tc.style), 200, 200)
		if y0, y1 := redRows(img, 10); y0 != tc.y0 || y1 != tc.y1 {
			t.Errorf("table cell %s: red rows %d..%d, want %d..%d", tc.style, y0, y1, tc.y0, tc.y1)
		}
	}
}
