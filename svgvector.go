// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"image"
	"image/color"
	"math"

	"github.com/go-gfx/oksvg"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

// svgVectorSource is the vector form of a parsed SVG document, made the
// first time a vector canvas asks for it: oksvg's Shapes, which follow what
// its Draw paints (strokes as their outlines, gradients and clip paths
// resolved), in the document's viewBox. A drawing Shapes cannot give exactly
// (a gradient that fades, a reflected one, a clipPath of clipped pieces) has
// no form, and is placed as its bitmap; so has one past paint.MaxVectorOps.
func svgVectorSource(icon *oksvg.SvgIcon) *paint.VectorSource {
	vb := icon.ViewBox
	return paint.NewVectorSource(func() (img *paint.VectorImage) {
		defer func() {
			if recover() != nil { // rasterx's stroker panics on some geometry
				img = nil
			}
		}()
		shapes, exact := icon.Shapes()
		if !exact {
			return nil
		}
		out := make([]paint.VectorShape, 0, len(shapes))
		for _, sh := range shapes {
			if flat(sh.Path) {
				// an open line's fill (SVG fills every path, black by
				// default): no area, nothing painted
				continue
			}
			vs := paint.VectorShape{Path: vectorPath(sh.Path), EvenOdd: sh.EvenOdd, Color: cssColor(sh.Color)}
			if g := sh.Gradient; g != nil {
				s := &paint.Shading{Radial: g.Radial, ExtendStart: true, ExtendEnd: true}
				if g.Radial {
					s.CX, s.CY, s.RX, s.RY, s.R0, s.R1 = g.CX, g.CY, g.RX, g.RY, 0, 1
				} else {
					s.X0, s.Y0, s.X1, s.Y1 = g.X1, g.Y1, g.X2, g.Y2
				}
				for _, st := range g.Stops {
					s.Stops = append(s.Stops, paint.GradientStop{Pos: st.Offset, Color: cssColor(st.Color)})
				}
				vs.Gradient = s
			}
			for _, c := range sh.Clips {
				vs.Clips = append(vs.Clips, paint.VectorClip{Path: vectorPath(c.Path), EvenOdd: c.EvenOdd})
			}
			out = append(out, vs)
		}
		return paint.NewVectorImage(paint.Rect{X: vb.X, Y: vb.Y, W: vb.W, H: vb.H}, out)
	})
}

// vectorPath converts oksvg's path, raising its quadratic curves to cubic
// ones (the same curve: controls two thirds of the way to the quadratic's).
// Every subpath is closed: every shape is a fill, which closes an open
// subpath (SVG 1.1 §11.3: it "fills open subpaths by performing the fill
// operation as if an additional "closepath" command were added"), and a
// writer may not (LibreOffice leaves an open ODF draw:path unfilled; a
// Twemoji face is an open path).
func vectorPath(ops []oksvg.PathOp) paint.Path {
	p := paint.Path{Ops: make([]paint.PathOp, 0, len(ops)+1)}
	var cur, start [2]float64
	open := false
	for _, op := range ops {
		switch op.Op {
		case 'M':
			if open {
				p.Close()
			}
			p.MoveTo(op.Pts[0][0], op.Pts[0][1])
			cur, start, open = op.Pts[0], op.Pts[0], true
		case 'L':
			p.LineTo(op.Pts[0][0], op.Pts[0][1])
			cur = op.Pts[0]
		case 'Q':
			c, e := op.Pts[0], op.Pts[1]
			p.CubeTo(cur[0]+2.0/3*(c[0]-cur[0]), cur[1]+2.0/3*(c[1]-cur[1]),
				e[0]+2.0/3*(c[0]-e[0]), e[1]+2.0/3*(c[1]-e[1]), e[0], e[1])
			cur = e
		case 'C':
			p.CubeTo(op.Pts[0][0], op.Pts[0][1], op.Pts[1][0], op.Pts[1][1], op.Pts[2][0], op.Pts[2][1])
			cur = op.Pts[2]
		case 'Z':
			p.Close()
			cur, open = start, false
		}
	}
	if open {
		p.Close()
	}
	return p
}

// flat reports a path whose points all lie on one horizontal or vertical
// line (or one point): its fill covers nothing. A curve's control points
// bound it, so a path whose controls are flat is flat.
func flat(ops []oksvg.PathOp) bool {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, op := range ops {
		n := map[byte]int{'M': 1, 'L': 1, 'Q': 2, 'C': 3}[op.Op]
		for _, p := range op.Pts[:n] {
			minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
			minY, maxY = math.Min(minY, p[1]), math.Max(maxY, p[1])
		}
	}
	return !(maxX-minX > 1e-9 && maxY-minY > 1e-9)
}

func cssColor(c color.NRGBA) css.Color { return css.Color{R: c.R, G: c.G, B: c.B, A: c.A} }

// addVector records the vector form of an SVG bitmap, keyed by the bitmap
// the image maps hold.
func addVector(vecs paint.VectorImages, img image.Image, src *paint.VectorSource) {
	if img != nil && src != nil {
		vecs[img] = src
	}
}
