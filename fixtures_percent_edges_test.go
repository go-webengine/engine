// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"fmt"
	"image"
	"testing"
)

// redBox is the bounding box of the pure red pixels of img: first and last column
// and row, -1 when there are none.
func redBox(img *image.RGBA) (x0, x1, y0, y1 int) {
	x0, x1, y0, y1 = -1, -1, -1, -1
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c := img.RGBAAt(x, y); c.R == 0xff && c.G == 0 && c.B == 0 {
				if x0 < 0 || x < x0 {
					x0 = x
				}
				if x > x1 {
					x1 = x
				}
				if y0 < 0 {
					y0 = y
				}
				y1 = y
			}
		}
	}
	return
}

// TestPercentPaddingAndMargins pins the percentage basis of padding and margin:
// the containing block's WIDTH, on all four sides (CSS Box 3 §5; CSS 2.1 §8.3
// "the percentage is calculated with respect to the width of the generated box's
// containing block", §8.4 the same for padding), the containing block being the
// content box of the nearest block ancestor, and border-box widths taking the
// padding out of the box. The container is 500px wide; the red box is painted
// by its background, which covers its padding. Before this the engine kept a
// padding percentage as 0 and a margin-top/-bottom percentage as 0 (only
// margin-left and -right were resolved), so Marpit's `padding` and offsets in
// percent were lost.
func TestPercentPaddingAndMargins(t *testing.T) {
	// width is the box's own; the container is 500 wide, the box 100 by 20
	const box = "width:100px;height:20px;background:#f00;"
	cases := []struct {
		name, css      string
		x0, x1, y0, y1 int
	}{
		{"padding-left 60%", box + "padding-left:60%", 0, 399, 0, 19},
		{"padding-right 60%", box + "padding-right:60%", 0, 399, 0, 19},
		{"padding-top 20%", box + "padding-top:20%", 0, 99, 0, 119},
		{"padding-bottom 20%", box + "padding-bottom:20%", 0, 99, 0, 119},
		{"padding 10% on all four sides", box + "padding:10%", 0, 199, 0, 119},
		{"padding 10% 20%", box + "padding:10% 20%", 0, 299, 0, 119},
		{"padding 10% 20% 5%", box + "padding:10% 20% 5%", 0, 299, 0, 94},
		{"padding 1% 2% 3% 4%", box + "padding:1% 2% 3% 4%", 0, 129, 0, 39},
		{"padding-inline 10%", box + "padding-inline:10%", 0, 199, 0, 19},
		{"padding-block 10%", box + "padding-block:10%", 0, 99, 0, 119},
		{"padding-inline-start 20%", box + "padding-inline-start:20%", 0, 199, 0, 19},
		{"padding-block-end 20%", box + "padding-block-end:20%", 0, 99, 0, 119},
		{"calc(100% - 40%)", box + "padding-left:calc(100% - 40%)", 0, 399, 0, 19},
		{"a percentage then a length", box + "padding-left:60%;padding-left:10px", 0, 109, 0, 19},
		{"a length then a percentage", box + "padding-left:10px;padding-left:20%", 0, 199, 0, 19},
		{"the shorthand resets a side", box + "padding-left:60%;padding:0", 0, 99, 0, 19},
		{"border-box takes the padding out of the width", "box-sizing:border-box;width:300px;height:20px;background:#f00;padding-left:40%", 0, 299, 0, 19},
		{"margin-left 60%", box + "margin-left:60%", 300, 399, 0, 19},
		{"margin-top 20%", box + "margin-top:20%", 0, 99, 100, 119},
		{"margin 10% 0 0 20%", box + "margin:10% 0 0 20%", 100, 199, 50, 69},
		{"margin-block-start 20%", box + "margin-block-start:20%", 0, 99, 100, 119},
		{"margin-block 20% 0", box + "margin-block:20% 0", 0, 99, 100, 119},
		{"a float's padding is of its container", "float:left;" + box + "padding-left:20%", 0, 199, 0, 19},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// the 1px child gives the box a content edge to carry its top margin (an
			// empty box with a fixed height drops it, with px margins too); the
			// 1px top padding keeps the first child's margin from collapsing
			// out of the section: the red box's rows are one down
			html := fmt.Sprintf(`<!DOCTYPE html><html><head><style>html,body{margin:0}section{width:500px;padding-top:1px}</style></head><body><section><div style="%s"><i style="display:block;height:1px"></i></div></section></body></html>`, tc.css)
			x0, x1, y0, y1 := redBox(renderString(t, html, 500, 300))
			y0, y1 = y0-1, y1-1
			if x0 != tc.x0 || x1 != tc.x1 || y0 != tc.y0 || y1 != tc.y1 {
				t.Errorf("red box columns %d..%d rows %d..%d, want %d..%d and %d..%d", x0, x1, y0, y1, tc.x0, tc.x1, tc.y0, tc.y1)
			}
		})
	}
}

// The basis is the containing block, not the box and not the page: a child of a
// 400px block is padded by 50% of 400.
func TestPercentPaddingNestedContainingBlocks(t *testing.T) {
	const page = `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<section style="width:500px"><div style="width:400px;overflow:hidden"><div style="padding-left:50%;width:100px"><i style="display:block;width:50px;height:10px;background:#f00"></i></div></div></section></body></html>`
	x0, x1, _, _ := redBox(renderString(t, page, 500, 100))
	if x0 != 200 || x1 != 249 {
		t.Errorf("the child's ink is at columns %d..%d, want 200..249 (50%% of the 400px block)", x0, x1)
	}
	// two levels: 10% of a 250px block (itself 50% of 500) is 25px
	const two = `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<section style="width:500px"><div style="width:50%"><div style="padding-left:10%"><i style="display:block;width:50px;height:10px;background:#f00"></i></div></div></section></body></html>`
	x0, x1, _, _ = redBox(renderString(t, two, 500, 100))
	if x0 != 25 || x1 != 74 {
		t.Errorf("two levels: the child's ink is at columns %d..%d, want 25..74", x0, x1)
	}
	// the box's own width is not the basis: a 100px box padded 50% is padded by 250
	// of the 500px container, not by 50
	const own = `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<section style="width:500px"><div style="width:100px;padding-left:50%"><i style="display:block;width:10px;height:10px;background:#f00"></i></div></section></body></html>`
	x0, _, _, _ = redBox(renderString(t, own, 500, 100))
	if x0 != 250 {
		t.Errorf("the box's own width is the basis: ink at column %d, want 250", x0)
	}
}

// A percentage padding in a flex item or an inline-block is, for now, 0: their
// layout is isolated from the containing block it would be a percentage of. The
// page still renders, with no padding that a wrong basis would have made.
func TestPercentPaddingInIsolatedLayoutsIsNotWrong(t *testing.T) {
	for name, outer := range map[string]string{"flex item": "display:flex", "inline-block": "display:block"} {
		inner := "padding-left:50%;width:100px"
		if name == "inline-block" {
			inner += ";display:inline-block"
		}
		page := fmt.Sprintf(`<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body><section style="width:500px;%s"><div style="%s"><i style="display:block;width:10px;height:10px;background:#f00"></i></div></section></body></html>`, outer, inner)
		x0, _, _, _ := redBox(renderString(t, page, 500, 100))
		if x0 < 0 || x0 > 260 {
			t.Errorf("%s: ink at column %d: the padding is not a plausible one", name, x0)
		}
	}
}
