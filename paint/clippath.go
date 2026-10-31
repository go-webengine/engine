// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/layout"
)

// applyClipPathMask stencils group (see applyMask's own doc comment for the
// zero-origin/yOffset convention it shares) by box's `clip-path:
// polygon(...)`, zeroing every pixel outside the polygon — the same
// "everything outside the border box is masked out entirely" treatment
// applyMask already gives mask-image, since clip-path likewise affects
// everything the element paints (background, border, text, children), not
// one layer. For the confirmed real trigger (vercel.com's own polygon,
// every vertex within [0,100]% of the border box) that restriction has no
// visible effect; it would only matter for a polygon whose vertices fall
// outside the border box, which no confirmed trigger does.
//
// Unlike mask-image there is no bitmap to sample: each pixel's membership
// is decided by a point-in-polygon test against the vertices
// css.ResolveClipPathPolygon resolves against the element's own border-box
// width/height (CSS Shapes 1 §2.2). This gives a hard edge with no
// antialiasing — the same simplification the legacy `clip:rect()` path
// (intersectClipRect, paintBoxContent) already makes for its own,
// rectangular case.
func applyClipPathMask(group *image.RGBA, box *layout.Box, yOffset int) {
	bx := rectOf(box)
	bw, bh := bx.Dx(), bx.Dy()
	if bw <= 0 || bh <= 0 {
		clearAlpha(group)
		return
	}
	// parse.go's `case "clip-path"` already ran this exact resolver to
	// decide whether to accept the declaration — a value that reached
	// Style.ClipPath is guaranteed resolvable, so ok is not checked here.
	points, evenOdd, _ := css.ResolveClipPathPolygon(box.Style.ClipPath, float64(bw), float64(bh))
	for y := group.Rect.Min.Y; y < group.Rect.Max.Y; y++ {
		canvasY := y + yOffset
		inRow := canvasY >= bx.Min.Y && canvasY < bx.Max.Y
		for x := group.Rect.Min.X; x < group.Rect.Max.X; x++ {
			i := group.PixOffset(x, y)
			if group.Pix[i+3] == 0 {
				continue
			}
			px, py := float64(x-bx.Min.X)+0.5, float64(canvasY-bx.Min.Y)+0.5
			if !inRow || x < bx.Min.X || x >= bx.Max.X || !pointInPolygon(points, evenOdd, px, py) {
				group.Pix[i+0], group.Pix[i+1], group.Pix[i+2], group.Pix[i+3] = 0, 0, 0, 0
			}
		}
	}
}

// pointInPolygon reports whether (x,y) lies inside the polygon under the
// given fill rule — polygon()'s own optional leading <'fill-rule'> selects
// between CSS Masking 1 / SVG 2's nonzero and evenodd. Both are the
// standard ray-casting point-in-polygon tests: a horizontal ray cast from
// (x,y) in the +X direction, counting edge crossings — evenodd by their
// parity, nonzero by their signed winding number. They agree on every
// simple (non-self-intersecting) polygon, which is all a hard-edge clip
// realistically needs.
func pointInPolygon(pts []css.ClipPathPoint, evenOdd bool, x, y float64) bool {
	n := len(pts)
	winding := 0
	crossings := 0
	for i := 0; i < n; i++ {
		a, b := pts[i], pts[(i+1)%n]
		if (a.Y <= y) != (b.Y <= y) {
			t := (y - a.Y) / (b.Y - a.Y)
			xCross := a.X + t*(b.X-a.X)
			if xCross > x {
				crossings++
				if b.Y > a.Y {
					winding++
				} else {
					winding--
				}
			}
		}
	}
	if evenOdd {
		return crossings%2 != 0
	}
	return winding != 0
}
