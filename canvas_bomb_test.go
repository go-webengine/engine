// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"
	"testing"

	"github.com/go-webengine/engine/dom"
)

// newCanvas allocates a REAL, eagerly zero-filled buffer (confirmed live,
// round 172: NOT the cheap-until-painted sparse allocation it might look
// like) sized from the laid-out content height alone, with no upper bound
// before this test's own fix. A script can grow that height arbitrarily —
// `root.appendChild(root.cloneNode(true))` in a 25-iteration loop reached
// 1024×13,288,016, measured at ~20GB of real resident memory in under 10
// seconds — with no pathologically large string or deep recursion needed
// at all. maxCanvasPixels (engine.go) bounds the ALLOCATION regardless of
// how tall layout says the content is.
func TestNewCanvasCapsTotalPixelCount(t *testing.T) {
	e := New()
	doc := &Document{Root: &dom.Node{Type: dom.Document}}
	// A content height whose pixel count, at this viewport width, would
	// exceed maxCanvasPixels by a wide margin if uncapped.
	rp := &renderPass{height: 500_000}
	viewport := image.Rect(0, 0, 1024, 768)
	img := e.newCanvas(doc, rp, viewport, 1024)
	got := int64(img.Bounds().Dx()) * int64(img.Bounds().Dy())
	if got > maxCanvasPixels {
		t.Errorf("newCanvas allocated %d pixels, want <= %d (maxCanvasPixels)", got, maxCanvasPixels)
	}
}

// An ordinary page's content height is untouched by the cap.
func TestNewCanvasOrdinaryHeightUnaffected(t *testing.T) {
	e := New()
	doc := &Document{Root: &dom.Node{Type: dom.Document}}
	rp := &renderPass{height: 3000}
	viewport := image.Rect(0, 0, 1024, 768)
	img := e.newCanvas(doc, rp, viewport, 1024)
	if h := img.Bounds().Dy(); h != 3000 {
		t.Errorf("canvas height = %d, want 3000 (unaffected by the cap)", h)
	}
}

// A viewport wider than maxCanvasPixels itself leaves no room for even one
// row once divided; the clamp's own floor (1) must still hold rather than
// leaving the canvas zero-height.
func TestNewCanvasClampFloorHoldsWhenViewportAloneExceedsCap(t *testing.T) {
	e := New()
	doc := &Document{Root: &dom.Node{Type: dom.Document}}
	rp := &renderPass{height: 1}
	vpW := maxCanvasPixels + 1
	viewport := image.Rect(0, 0, vpW, 768)
	img := e.newCanvas(doc, rp, viewport, vpW)
	if h := img.Bounds().Dy(); h != 1 {
		t.Errorf("canvas height = %d, want 1 (the clamp's own floor)", h)
	}
}
