// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"
	"testing"
)

// redBBox returns the bounding box of the pure-red pixels of img.
func redBBox(img *image.RGBA) image.Rectangle {
	r := image.Rectangle{}
	first := true
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if c := img.RGBAAt(x, y); c.R == 0xff && c.G == 0 && c.B == 0 {
				p := image.Rect(x, y, x+1, y+1)
				if first {
					r, first = p, false
				} else {
					r = r.Union(p)
				}
			}
		}
	}
	return r
}

// TestSVGWidthHeightAttributes: on an outermost <svg>, width and height are
// presentation attributes for the CSS width/height properties (SVG 2 §7.8),
// so they take any CSS length — %, em, rem, px — not just a bare number.
func TestSVGWidthHeightAttributes(t *testing.T) {
	const rect = `viewBox="0 0 100 100"><rect width="100" height="100" fill="#ff0000"/></svg>`
	page := func(body string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}body{font-size:20px}</style></head><body>` + body + `</body></html>`
	}
	for _, tc := range []struct {
		name, body string
		w, h       int
	}{
		{"percent in a sized box", `<span style="display:inline-block;width:2em;height:2em"><svg width="100%" height="100%" ` + rect + `</span>`, 40, 40},
		{"em", `<svg style="display:block" width="2em" height="2em" ` + rect, 40, 40},
		{"rem", `<svg style="display:block" width="3rem" height="1rem" preserveAspectRatio="none" ` + rect, 48, 16},
		{"px unit", `<svg style="display:block" width="30px" height="30px" ` + rect, 30, 30},
		{"bare number (unchanged)", `<svg style="display:block" width="40" height="40" ` + rect, 40, 40},
		{"style wins over attribute", `<svg style="display:block;width:25px;height:25px" width="2em" height="2em" ` + rect, 25, 25},
		{"auto keeps the viewBox size", `<svg style="display:block" width="auto" height="auto" ` + rect, 100, 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, page(tc.body), 200, 200)
			bb := redBBox(img)
			if bb.Dx() != tc.w || bb.Dy() != tc.h {
				t.Errorf("red box %v (%dx%d), want %dx%d", bb, bb.Dx(), bb.Dy(), tc.w, tc.h)
			}
		})
	}
}
