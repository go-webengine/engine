// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"

	"github.com/go-webengine/engine/dom"
)

// refreshCanvasImages re-reads every <canvas> element's CURRENT drawn
// bitmap into rp's own replaced-element maps, after the settle loop has run
// every script. loadAllImages (images.go's loadImageSet) already has a
// canvas branch, but it only runs ONCE, before scripts execute — correct
// for every OTHER replaced element (an <img>'s src is decided by the DOM
// script mutates, so loading it before the settle loop is what lets a
// script read its real geometry back), but a canvas has no "src" at all:
// its whole bitmap comes from script calling getContext("2d") and drawing,
// which happens DURING settle, after loadAllImages already ran. Without
// this second, cheap (no fetch, no decode — just reading bytes already in
// memory) pass, a page that only draws to its canvas after the first layout
// would paint nothing, which is the overwhelmingly common real case (round
// 38's own stripe.com finding: every confirmed real canvas usage draws from
// a <script>, never from static markup alone).
func refreshCanvasImages(root *dom.Node, imgSize map[*dom.Node][2]float64, imgs map[*dom.Node]image.Image) {
	if imgSize == nil || imgs == nil {
		return
	}
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if n.Type == dom.Element && n.Tag == "canvas" {
			if len(n.CanvasPixels) > 0 && n.CanvasW > 0 && n.CanvasH > 0 {
				imgSize[n] = [2]float64{float64(n.CanvasW), float64(n.CanvasH)}
				imgs[n] = &image.NRGBA{
					Pix:    n.CanvasPixels,
					Stride: n.CanvasW * 4,
					Rect:   image.Rect(0, 0, n.CanvasW, n.CanvasH),
				}
			}
			return // opaque replaced element, matching loadImageSet's own walk
		}
		for _, c := range n.Children {
			walk(c)
		}
		if n.Shadow != nil {
			for _, c := range n.Shadow.Children {
				walk(c)
			}
		}
	}
	walk(root)
}
