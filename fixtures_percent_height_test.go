// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"
	"testing"
)

// redSpan returns the first and last rows of column x painted pure red.
func redSpan(img *image.RGBA, x int) (first, last int) {
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

// TestPercentageHeights pins CSS 2.1 §10.5: a percentage height resolves
// against the containing block's height when that height is definite (an
// explicit height; an absolutely positioned box's height, from height or
// from top+bottom — whose percentages resolve against the containing block's
// padding box; the initial containing block, i.e. the viewport; a stretched
// flex item, CSS Flexbox 1 §9.8), and computes to auto otherwise. Marp's
// layered backgrounds: `section[data-marpit-advanced-background-split]
// {width:100%;height:100%}` inside an absolutely positioned 1280x720 box,
// and `flex:auto` figures in a `display:flex;height:100%` container that
// first gets `all:initial`.
func TestPercentageHeights(t *testing.T) {
	page := func(body string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` + body + `</body></html>`
	}
	const red = "background:#ff0000"
	cases := []struct {
		name, body string
		y0, y1     int
	}{
		{"explicit parent", `<div style="height:200px"><div style="height:100%;` + red + `"></div></div>`, 0, 199},
		{"half", `<div style="height:200px"><div style="height:50%;` + red + `"></div></div>`, 0, 99},
		{"nested", `<div style="height:200px"><div style="height:50%"><div style="height:50%;` + red + `"></div></div></div>`, 0, 49},
		{"border-box", `<div style="height:200px"><div style="box-sizing:border-box;padding-top:20px;height:50%;` + red + `"></div></div>`, 0, 99},
		{"auto parent: computes to auto", `<div><div style="height:100%;` + red + `">&nbsp;</div></div>`, 0, -1},
		{"abspos parent", `<div style="position:absolute;top:0;left:0;width:200px;height:200px"><div style="width:100%;height:100%;` + red + `"></div></div>`, 0, 199},
		{"abspos child of relative", `<div style="position:relative;height:200px"><div style="position:absolute;top:0;left:0;width:100%;height:100%;` + red + `"></div></div>`, 0, 199},
		{"abspos top+bottom", `<div style="position:relative;height:200px"><div style="position:absolute;top:50px;bottom:50px;left:0;right:0"><div style="height:100%;` + red + `"></div></div></div>`, 50, 149},
		{"abspos height from top+bottom paints", `<div style="position:relative;height:200px"><div style="position:absolute;top:50px;bottom:50px;left:0;right:0;` + red + `"></div></div>`, 50, 149},
		{"viewport (html/body 100%)", `<style>html,body{height:100%}</style><div style="height:100%;` + red + `"></div>`, 0, 199},
		{"flex:auto child of flex 100%", `<div style="height:200px"><div style="display:flex;height:100%"><div style="flex:auto;` + red + `"></div></div></div>`, 0, 199},
		{"percent inside stretched flex item", `<div style="display:flex;height:200px"><div style="flex:auto"><div style="height:100%;` + red + `"></div></div></div>`, 0, 199},
		{"all:initial then display:flex", `<style>.c{all:initial;display:flex;height:100%}</style><div style="height:200px"><div class="c"><div style="flex:auto;` + red + `"></div></div></div>`, 0, 199},
		{"marp split shape", `<div style="position:absolute;top:0;left:0;width:200px;height:200px"><section style="display:block;width:100%;height:100%"><div style="display:flex;height:100%"><figure style="margin:0;flex:auto;` + red + `"></figure></div></section></div>`, 0, 199},
	}
	// A full-page capture of the very common `html,body{height:100%}` page:
	// the body is now viewport-tall, and the content overflowing it must
	// still be in the document height (scrollable overflow), not cut off at
	// the viewport — whereas content clipped by overflow:hidden is not.
	tall := renderString(t, page(`<style>html,body{height:100%}</style><div style="height:500px;`+red+`"></div>`), 200, 200)
	if h := tall.Bounds().Dy(); h < 500 {
		t.Errorf("html,body{height:100%%} page with 500px of content captured %dpx tall, want >= 500", h)
	}
	if y0, y1 := redSpan(tall, 100); y0 != 0 || y1 != 499 {
		t.Errorf("tall page red rows %d..%d, want 0..499", y0, y1)
	}
	clipped := renderString(t, page(`<div style="height:100px;overflow:hidden"><div style="height:500px;`+red+`"></div></div>`), 200, 200)
	if h := clipped.Bounds().Dy(); h != 200 {
		t.Errorf("clipped overflow grew the capture to %dpx, want the 200px viewport", h)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, page(tc.body), 200, 200)
			y0, y1 := redSpan(img, 100)
			if tc.y1 < 0 {
				// auto: only the &nbsp; line box is red, well short of 200.
				if y1 >= 100 {
					t.Errorf("red rows %d..%d: a percentage against an auto-height parent must compute to auto", y0, y1)
				}
				return
			}
			if y0 != tc.y0 || y1 != tc.y1 {
				t.Errorf("red rows at x=100: %d..%d, want %d..%d", y0, y1, tc.y0, tc.y1)
			}
		})
	}
}
