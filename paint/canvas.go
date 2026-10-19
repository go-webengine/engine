// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"math"

	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
)

// Canvas is a vector drawing surface: what PaintVector draws a laid-out page
// through instead of rasterising it. Coordinates are the layout's own device
// pixels, origin at the page's top-left, y growing downwards; an
// implementation maps them to its own space (a PDF page: points, y up).
//
// The operations are the few the box painter needs, chosen so that each maps
// onto one construct of a page-description language: a filled path, a clip,
// a shading painted through the clip, an image placed in a rectangle and a
// run of positioned glyphs. Save and Restore bracket every Clip.
type Canvas interface {
	// Save pushes the graphics state (the clip); Restore pops it.
	Save()
	Restore()
	// Clip intersects the clip with p, by the nonzero or the even-odd rule.
	Clip(p *Path, evenOdd bool)
	// Fill fills p with c, by the nonzero or the even-odd rule. c's alpha is
	// a constant opacity.
	Fill(p *Path, c css.Color, evenOdd bool)
	// Shade paints a gradient through the current clip.
	Shade(s Shading)
	// Image paints img, scaled, into dst. img follows image.Image's own
	// convention (its RGBA method premultiplied); transparent pixels let the
	// page show through.
	Image(img image.Image, dst Rect)
	// Glyphs draws a run of positioned glyphs.
	Glyphs(run *GlyphRun)
}

// Rect is a rectangle in device pixels.
type Rect struct{ X, Y, W, H float64 }

// rectOfImage converts an integer pixel rectangle.
func rectOfImage(r image.Rectangle) Rect {
	return Rect{X: float64(r.Min.X), Y: float64(r.Min.Y), W: float64(r.Dx()), H: float64(r.Dy())}
}

// PathOpKind says what one PathOp does.
type PathOpKind uint8

// The path operations.
const (
	PathMoveTo PathOpKind = iota // start a subpath at P[0]
	PathLineTo                   // straight line to P[0]
	PathCubeTo                   // cubic Bézier through controls P[0], P[1] to P[2]
	PathClose                    // close the subpath
)

// Point is a point in device pixels.
type Point struct{ X, Y float64 }

// PathOp is one operation of a Path.
type PathOp struct {
	Kind PathOpKind
	P    [3]Point
}

// Path is an outline made of subpaths of lines and cubic Béziers.
type Path struct{ Ops []PathOp }

// MoveTo starts a subpath.
func (p *Path) MoveTo(x, y float64) {
	p.Ops = append(p.Ops, PathOp{Kind: PathMoveTo, P: [3]Point{{x, y}}})
}

// LineTo adds a straight segment.
func (p *Path) LineTo(x, y float64) {
	p.Ops = append(p.Ops, PathOp{Kind: PathLineTo, P: [3]Point{{x, y}}})
}

// CubeTo adds a cubic Bézier segment.
func (p *Path) CubeTo(x1, y1, x2, y2, x3, y3 float64) {
	p.Ops = append(p.Ops, PathOp{Kind: PathCubeTo, P: [3]Point{{x1, y1}, {x2, y2}, {x3, y3}}})
}

// Close closes the current subpath.
func (p *Path) Close() { p.Ops = append(p.Ops, PathOp{Kind: PathClose}) }

// kappa places the control points of the cubic that best approximates a
// quarter circle: 4/3·(√2−1).
const kappa = 0.5522847498307936

// RoundRect adds r as a closed subpath with corners of radius rad, clamped
// to half the shorter side as CSS and the raster painter clamp it; rad <= 0
// is a plain rectangle. The subpath runs clockwise on screen.
func (p *Path) RoundRect(r Rect, rad float64) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	rad = math.Min(rad, math.Min(r.W, r.H)/2)
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	if rad <= 0 {
		p.MoveTo(x0, y0)
		p.LineTo(x1, y0)
		p.LineTo(x1, y1)
		p.LineTo(x0, y1)
		p.Close()
		return
	}
	k := rad * kappa
	p.MoveTo(x0+rad, y0)
	p.LineTo(x1-rad, y0)
	p.CubeTo(x1-rad+k, y0, x1, y0+rad-k, x1, y0+rad)
	p.LineTo(x1, y1-rad)
	p.CubeTo(x1, y1-rad+k, x1-rad+k, y1, x1-rad, y1)
	p.LineTo(x0+rad, y1)
	p.CubeTo(x0+rad-k, y1, x0, y1-rad+k, x0, y1-rad)
	p.LineTo(x0, y0+rad)
	p.CubeTo(x0, y0+rad-k, x0+rad-k, y0, x0+rad, y0)
	p.Close()
}

// Ellipse adds the ellipse inscribed in r as a closed subpath.
func (p *Path) Ellipse(r Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	rx, ry := r.W/2, r.H/2
	cx, cy := r.X+rx, r.Y+ry
	kx, ky := rx*kappa, ry*kappa
	p.MoveTo(cx+rx, cy)
	p.CubeTo(cx+rx, cy+ky, cx+kx, cy+ry, cx, cy+ry)
	p.CubeTo(cx-kx, cy+ry, cx-rx, cy+ky, cx-rx, cy)
	p.CubeTo(cx-rx, cy-ky, cx-kx, cy-ry, cx, cy-ry)
	p.CubeTo(cx+kx, cy-ry, cx+rx, cy-ky, cx+rx, cy)
	p.Close()
}

// Shading is a gradient to paint through the clip. Its stops all carry the
// same alpha — a constant opacity for the whole shading — and strictly
// increasing positions in [0,1]; PaintVector splits a CSS gradient into
// such pieces.
//
// Linear: the colour at position t lies on the line through
// (X0,Y0)+t·((X1,Y1)−(X0,Y0)), perpendicular to that axis. Radial: on the
// ellipse centred on (CX,CY) whose radii are (RX,RY) scaled by R0+t·(R1−R0)
// — a circle of radius R0+t·(R1−R0) under a transform scaling x by RX and y
// by RY, which is how a PDF draws an ellipse. Extend says whether the end
// colours continue beyond t=0 and t=1.
type Shading struct {
	Radial         bool
	X0, Y0, X1, Y1 float64
	CX, CY, RX, RY float64
	R0, R1         float64 // radial: fractions of the ellipse at t=0 and t=1
	Stops          []GradientStop
	ExtendStart    bool
	ExtendEnd      bool
}

// GradientStop is one stop of a Shading.
type GradientStop struct {
	Pos   float64
	Color css.Color
}

// GlyphRun is a run of glyphs of one font, size and colour, positioned by the
// layout. X,Y is the run's baseline origin; each glyph's X is its offset
// along the baseline from it, all in device pixels.
//
// An Invisible run is text that is there but not painted — the text of an
// element PaintVector had to draw as a bitmap, laid over that bitmap so that
// it can still be selected, searched and copied (PDF's text rendering mode
// 3, ISO 32000-1 §9.3.6).
type GlyphRun struct {
	Font      *opentype.Font
	Size      float64
	Color     css.Color
	X, Y      float64
	Glyphs    []Glyph
	Invisible bool
}

// Glyph is one glyph of a GlyphRun: its id in the font, its origin's offset
// from the run origin, and the text it stands for (several characters for a
// ligature, empty for a glyph continuing an earlier one's cluster), which is
// what copying the text out of the document gives.
type Glyph struct {
	ID   opentype.GlyphIndex
	X    float64
	Text string
}
