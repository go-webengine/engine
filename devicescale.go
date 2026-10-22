// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"
	"math"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// devScale returns the device pixel ratio a render uses: e.DeviceScale, with
// 0 (unset) and anything non-positive meaning 1.
func (e *Engine) devScale() float64 {
	if e.DeviceScale <= 0 {
		return 1
	}
	return e.DeviceScale
}

// media is the screen Media a render at CSS viewport width vpW cascades
// against: width in CSS px (so breakpoints are unaffected by the scale), and
// the device pixel ratio, which makes the cascade compute lengths in device
// px and answers the `resolution` media feature.
func (e *Engine) media(vpW int) css.Media {
	return css.Media{Width: float64(vpW), Scale: e.devScale()}
}

// cascade is css.CascadeMediaContainers at this engine's device scale. m is
// the measurer the page is laid out with: the font-relative ch unit is the
// advance of "0" in it (CSS Values 4 §6.1.1); nil measures ch as 0.5em.
func (e *Engine) cascade(root *dom.Node, vpW int, sheets []string, containers map[*dom.Node]css.ContainerSize, m layout.Measurer) css.StyleMap {
	media := e.media(vpW)
	if m != nil {
		media.Advance = m.Measure
	}
	return css.CascadeMediaContainers(root, media, sheets, containers)
}

// layoutDoc lays out a cascade computed by e.cascade: its lengths are in
// device px, so the viewport is too. imgSize already holds device-px sizes
// (the image loader works against the same scaled styles).
func (e *Engine) layoutDoc(root *dom.Node, sm css.StyleMap, vpW, vpH int, m layout.Measurer, imgSize map[*dom.Node][2]float64) (*layout.Box, float64) {
	s := e.devScale()
	return layout.LayoutDocumentViewport(root, sm, float64(vpW)*s, float64(vpH)*s, m, imgSize)
}

// deviceW is a CSS-px extent in device pixels, rounded up so a fractional
// scale never drops the last column.
func (e *Engine) deviceW(cssPx int) int {
	return int(math.Ceil(float64(cssPx) * e.devScale()))
}

// deviceViewport is a CSS-px viewport rectangle in device pixels.
func (e *Engine) deviceViewport(viewport image.Rectangle) image.Rectangle {
	return image.Rect(0, 0, e.deviceW(viewport.Dx()), e.deviceW(viewport.Dy()))
}
