// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"image"
	"math"
	"sync"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// VectorImage is an SVG drawing as the vector shapes it paints: what
// PaintVector draws in place of the drawing's bitmap, so that a formula, an
// emoji or an <img src="x.svg"> stays a vector in a PDF or an editable
// slide, sharp at any zoom.
//
// The shapes are in the drawing's own coordinates, ViewBox being the part
// that is mapped onto the box the image is placed in (each axis scaled on
// its own, as the bitmap is stretched). Strokes are already outlines, so
// every shape is a filled path: a solid colour, or a gradient (a Shading in
// the same coordinates, padded at both ends, of one opacity), shown where it
// is inside every one of its clips.
type VectorImage struct {
	ViewBox Rect
	Shapes  []VectorShape
	// Key is a content address: two images with the same key draw the same,
	// so a canvas may draw one once and place it at each use (a PDF form).
	Key string

	clipped, radial bool
	ops             int
}

// VectorShape is one filled path of a VectorImage.
type VectorShape struct {
	Path     Path
	EvenOdd  bool
	Color    css.Color // when Gradient is nil
	Gradient *Shading
	Clips    []VectorClip
}

// VectorClip is one clip path of a VectorShape.
type VectorClip struct {
	Path    Path
	EvenOdd bool
}

// MaxVectorOps bounds a VectorImage: a drawing of more path operations than
// this is placed as its bitmap. A canvas writes every operation (a PDF its
// content stream, a slide its geometry), where a bitmap costs its pixels
// whatever the SVG holds.
const MaxVectorOps = 200000

// NewVectorImage returns the image of shapes over viewBox, nil when there is
// nothing to draw, the box is empty or the drawing passes MaxVectorOps.
func NewVectorImage(viewBox Rect, shapes []VectorShape) *VectorImage {
	if !(viewBox.W > 0 && viewBox.H > 0) || len(shapes) == 0 || !finiteRect(viewBox) {
		return nil
	}
	v := &VectorImage{ViewBox: viewBox, Shapes: shapes}
	h := sha256.New()
	var buf [8]byte
	f := func(x float64) {
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(x))
		h.Write(buf[:])
	}
	path := func(p *Path, evenOdd bool) bool {
		if evenOdd {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		for _, op := range p.Ops {
			h.Write([]byte{byte(op.Kind)})
			for _, pt := range op.P {
				if !finite(pt.X) || !finite(pt.Y) {
					return false
				}
				f(pt.X)
				f(pt.Y)
			}
		}
		v.ops += len(p.Ops)
		return true
	}
	f(viewBox.X)
	f(viewBox.Y)
	f(viewBox.W)
	f(viewBox.H)
	for i := range shapes {
		sh := &shapes[i]
		h.Write([]byte{'S'})
		if !path(&sh.Path, sh.EvenOdd) {
			return nil
		}
		if g := sh.Gradient; g != nil {
			if g.Stops = normalStops(g.Stops); len(g.Stops) < 2 {
				return nil
			}
			if g.Radial {
				v.radial = true
				h.Write([]byte{'R'})
			} else {
				h.Write([]byte{'L'})
			}
			for _, x := range [...]float64{g.X0, g.Y0, g.X1, g.Y1, g.CX, g.CY, g.RX, g.RY, g.R0, g.R1} {
				if !finite(x) {
					return nil
				}
				f(x)
			}
			for _, s := range g.Stops {
				f(s.Pos)
				h.Write([]byte{s.Color.R, s.Color.G, s.Color.B, s.Color.A})
			}
		} else {
			h.Write([]byte{'C', sh.Color.R, sh.Color.G, sh.Color.B, sh.Color.A})
		}
		for j := range sh.Clips {
			v.clipped = true
			h.Write([]byte{'K'})
			if !path(&sh.Clips[j].Path, sh.Clips[j].EvenOdd) {
				return nil
			}
		}
		if v.ops > MaxVectorOps {
			return nil
		}
	}
	v.Key = hex.EncodeToString(h.Sum(nil)[:16])
	return v
}

// Clipped reports whether a shape of the image is clipped.
func (v *VectorImage) Clipped() bool { return v.clipped }

// Radial reports whether a shape of the image is filled with a radial
// gradient.
func (v *VectorImage) Radial() bool { return v.radial }

// normalStops makes stops a Shading takes: offsets clamped to [0,1] and
// never below the one before (SVG 1.1 §13.2.4), then strictly increasing;
// one stop is a band of its colour.
func normalStops(in []GradientStop) []GradientStop {
	if len(in) == 0 {
		return nil
	}
	out := make([]GradientStop, len(in))
	prev := 0.0
	for i, s := range in {
		p := math.Max(0, math.Min(1, s.Pos))
		if math.IsNaN(s.Pos) || p < prev {
			p = prev
		}
		out[i], prev = GradientStop{Pos: p, Color: s.Color}, p
	}
	if len(out) == 1 {
		out = []GradientStop{{Pos: 0, Color: out[0].Color}, {Pos: 1, Color: out[0].Color}}
	}
	return strictlyIncreasing(out)
}

func finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

func finiteRect(r Rect) bool { return finite(r.X) && finite(r.Y) && finite(r.W) && finite(r.H) }

// VectorImages are the vector forms of the bitmaps an SVG was drawn as, by
// bitmap: the image an <img>, an inline <svg> or a background holds in the
// maps PaintVector takes.
type VectorImages map[image.Image]*VectorSource

// VectorSource makes a vector form the first time it is asked for, so that
// a page only ever rendered as a bitmap does not pay for it. It is safe for
// concurrent use.
type VectorSource struct {
	once  sync.Once
	build func() *VectorImage
	img   *VectorImage
}

// NewVectorSource returns the source whose form build makes; build may
// return nil (no exact form), and runs at most once.
func NewVectorSource(build func() *VectorImage) *VectorSource {
	return &VectorSource{build: build}
}

// VectorOf returns the form made of img, nil when there is none.
func VectorOf(img *VectorImage) *VectorSource {
	s := &VectorSource{img: img}
	s.once.Do(func() {})
	return s
}

// Image returns the vector form, nil when there is none.
func (s *VectorSource) Image() *VectorImage {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if s.build != nil {
			s.img = s.build()
		}
	})
	return s.img
}

// VectorCanvas is a Canvas that places a vector image itself: pdfcanvas
// draws each one once, as a form, and paints it at every use. It returns
// false when it does not take this one; the painter then draws the shapes
// through the Canvas operations.
type VectorCanvas interface {
	Canvas
	VectorImage(v *VectorImage, dst Rect) bool
}

// GroupCanvas is a Canvas that keeps drawings together: what PaintVector
// draws between BeginGroup and EndGroup is one vector image, which an
// editable slide makes one group a viewer selects and moves as a whole.
type GroupCanvas interface {
	Canvas
	BeginGroup()
	EndGroup()
}

// VectorSupport says what of a vector image a canvas draws as the page
// does. A canvas that does not implement VectorSupporter draws all of it.
type VectorSupport struct {
	// Clips: a Clip cuts what is drawn after it, a Fill included. A slide
	// cannot clip a shape; an image whose shapes are clipped, or whose box
	// clips it (object-fit: cover, a background tile cut by its box), is then
	// placed as its bitmap.
	Clips bool
	// RadialGradients: a radial Shading's radii are where its last stop is.
	// An office gradient runs from its centre to the corners of the box.
	RadialGradients bool
}

// VectorSupporter is a Canvas that says what it draws of a vector image.
type VectorSupporter interface {
	VectorSupport() VectorSupport
}

// PaintVectorWith is PaintVector with the vector forms of the SVG bitmaps:
// where imgs or bgImgs holds a bitmap vecs has a form of, that form is drawn
// instead, as vector shapes, unless the canvas cannot draw it as the page
// does (VectorSupport) or a filter applies to the image.
func PaintVectorWith(c Canvas, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image, vecs VectorImages, page image.Rectangle) VectorStats {
	v := &vpainter{c: c, f: f, imgs: imgs, bgImgs: bgImgs, vecs: vecs, page: page}
	v.sc, _ = c.(StructuredCanvas)
	v.support = VectorSupport{Clips: true, RadialGradients: true}
	if s, ok := c.(VectorSupporter); ok {
		v.support = s.VectorSupport()
	}
	v.box(box, page)
	return v.stats
}

// vector returns the vector form of src the canvas can draw, nil for none.
// cut says the box it is placed in cuts it.
func (v *vpainter) vector(src image.Image, cut bool) *VectorImage {
	if len(v.vecs) == 0 || src == nil {
		return nil
	}
	vec := v.vecs[src].Image()
	if vec == nil {
		return nil
	}
	if !v.support.Clips && (cut || vec.Clipped()) {
		return nil
	}
	if !v.support.RadialGradients && vec.Radial() {
		return nil
	}
	return vec
}

// vectorImage draws vec mapped onto dst: through the canvas's own
// VectorImage when it has one that takes it, else shape by shape.
func (v *vpainter) vectorImage(vec *VectorImage, dst Rect) {
	v.stats.Vectors++
	if vc, ok := v.c.(VectorCanvas); ok && vc.VectorImage(vec, dst) {
		return
	}
	if gc, ok := v.c.(GroupCanvas); ok {
		gc.BeginGroup()
		defer gc.EndGroup()
	}
	m := vec.To(dst)
	for i := range vec.Shapes {
		sh := &vec.Shapes[i]
		if len(sh.Clips) > 0 {
			v.c.Save()
			for j := range sh.Clips {
				v.c.Clip(m.Path(&sh.Clips[j].Path), sh.Clips[j].EvenOdd)
			}
		}
		p := m.Path(&sh.Path)
		if sh.Gradient == nil {
			v.c.Fill(p, sh.Color, sh.EvenOdd)
		} else {
			v.c.Save()
			v.c.Clip(p, sh.EvenOdd)
			v.c.Shade(m.Shading(*sh.Gradient))
			v.c.Restore()
		}
		if len(sh.Clips) > 0 {
			v.c.Restore()
		}
	}
}

// VectorMap maps a vector image's coordinates onto a box: x' = X + x·SX,
// y' = Y + y·SY.
type VectorMap struct{ SX, SY, X, Y float64 }

// To is the map of v's view box onto dst.
func (v *VectorImage) To(dst Rect) VectorMap {
	sx, sy := dst.W/v.ViewBox.W, dst.H/v.ViewBox.H
	return VectorMap{SX: sx, SY: sy, X: dst.X - v.ViewBox.X*sx, Y: dst.Y - v.ViewBox.Y*sy}
}

// Point maps a point.
func (m VectorMap) Point(p Point) Point { return Point{X: m.X + p.X*m.SX, Y: m.Y + p.Y*m.SY} }

// Path maps a path into a new one.
func (m VectorMap) Path(p *Path) *Path {
	out := &Path{Ops: make([]PathOp, len(p.Ops))}
	for i, op := range p.Ops {
		out.Ops[i] = PathOp{Kind: op.Kind, P: [3]Point{m.Point(op.P[0]), m.Point(op.P[1]), m.Point(op.P[2])}}
	}
	return out
}

// Shading maps a gradient: its axis, or its centre and radii.
func (m VectorMap) Shading(s Shading) Shading {
	p0, p1 := m.Point(Point{s.X0, s.Y0}), m.Point(Point{s.X1, s.Y1})
	c := m.Point(Point{s.CX, s.CY})
	s.X0, s.Y0, s.X1, s.Y1 = p0.X, p0.Y, p1.X, p1.Y
	s.CX, s.CY = c.X, c.Y
	s.RX, s.RY = s.RX*math.Abs(m.SX), s.RY*math.Abs(m.SY)
	return s
}
