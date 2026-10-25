// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package pdfcanvas draws a go-webengine layout into a go-pdfkit page: a
// paint.Canvas whose paths, shadings, images and glyphs become the PDF's
// own, so the page keeps its text as text (subset-embedded fonts, with a
// /ToUnicode map, so it can be selected, searched and copied) and its edges
// as vectors.
//
//	doc := pdfkit.New(pdfkit.Options{Compress: true})
//	fonts := pdfcanvas.NewFonts()
//	l, _ := eng.LayoutHTML(ctx, html, base, image.Rect(0, 0, 1280, 720))
//	page := doc.AddPage(pdfkit.NewPageSize(960, 540))
//	l.PaintVector(pdfcanvas.New(page, 0.75, fonts))
//
// A device pixel is mapped to scale points (0.75 for a CSS pixel at device
// scale 1: 1/96 in against 1/72 in) and the y axis is flipped, the page's top
// left being the layout's origin.
package pdfcanvas

import (
	"image"

	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/pdfkit"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/paint"
)

// Fonts converts the layout's parsed fonts into pdfkit fonts, once each. Use
// one per document (a pdfkit.Font may be shared, but a document is where the
// set of fonts is bounded); it is not safe for concurrent use.
type Fonts struct {
	m map[*opentype.Font]*pdfkit.Font
}

// NewFonts returns an empty font map.
func NewFonts() *Fonts { return &Fonts{m: map[*opentype.Font]*pdfkit.Font{}} }

func (fs *Fonts) get(ot *opentype.Font) (*pdfkit.Font, error) {
	if f, ok := fs.m[ot]; ok {
		return f, nil
	}
	f, err := pdfkit.NewFont(ot)
	if err != nil {
		return nil, err
	}
	fs.m[ot] = f
	return f, nil
}

// Canvas is a paint.Canvas drawing into one pdfkit page.
type Canvas struct {
	p     *pdfkit.Page
	s     float64 // points per device pixel
	h     float64 // page height, points
	fonts *Fonts

	// Err is the first error a drawing call met (a font pdfkit cannot embed,
	// a shading it refuses); the call that met it drew nothing, the others
	// went on.
	Err error

	// The structure, with Tag (tags.go): the root element, the elements
	// made for DOM nodes, the list items' Lbl and LBody, the pending mark
	// and the open sequence's.
	root        *pdfkit.StructElem
	elems       map[*dom.Node]*pdfkit.StructElem
	lists       map[*dom.Node]*listItem
	next, cur   paint.Mark
	open        bool
	curElem     *pdfkit.StructElem
	curArtifact bool
}

var _ paint.Canvas = (*Canvas)(nil)

// New returns a canvas drawing into p, scale points per device pixel, with
// fonts shared across the document's pages. A nil fonts gets a map of its
// own.
func New(p *pdfkit.Page, scale float64, fonts *Fonts) *Canvas {
	if fonts == nil {
		fonts = NewFonts()
	}
	return &Canvas{p: p, s: scale, h: p.Height(), fonts: fonts}
}

func (c *Canvas) x(v float64) float64 { return v * c.s }
func (c *Canvas) y(v float64) float64 { return c.h - v*c.s }

func (c *Canvas) fail(err error) {
	if c.Err == nil {
		c.Err = err
	}
}

// Save implements paint.Canvas.
func (c *Canvas) Save() { c.p.Save() }

// Restore implements paint.Canvas.
func (c *Canvas) Restore() { c.p.Restore() }

// path writes p's segments, mapped to the page.
func (c *Canvas) path(p *paint.Path) bool {
	if p == nil || len(p.Ops) == 0 {
		return false
	}
	for _, op := range p.Ops {
		switch op.Kind {
		case paint.PathMoveTo:
			c.p.MoveTo(c.x(op.P[0].X), c.y(op.P[0].Y))
		case paint.PathLineTo:
			c.p.LineTo(c.x(op.P[0].X), c.y(op.P[0].Y))
		case paint.PathCubeTo:
			c.p.CurveTo(c.x(op.P[0].X), c.y(op.P[0].Y), c.x(op.P[1].X), c.y(op.P[1].Y), c.x(op.P[2].X), c.y(op.P[2].Y))
		case paint.PathClose:
			c.p.ClosePath()
		}
	}
	return true
}

// Clip implements paint.Canvas: the path, then W (or W*) and n, the clipping
// path operators of ISO 32000-1 §8.5.4.
func (c *Canvas) Clip(p *paint.Path, evenOdd bool) {
	if !c.path(p) {
		// An empty clip path clips everything away.
		c.p.Rectangle(pdfkit.Rect{})
	}
	if evenOdd {
		c.p.ClipEvenOdd()
	} else {
		c.p.Clip()
	}
	c.p.EndPath()
}

// rgb is a css.Color's colour, without its alpha.
func rgb(col css.Color) pdfkit.RGB {
	return pdfkit.RGB{R: float64(col.R) / 255, G: float64(col.G) / 255, B: float64(col.B) / 255}
}

// withAlpha runs draw under a constant opacity (the ca/CA of an ExtGState,
// ISO 32000-1 §11.6.4.4) when a is below opaque.
func (c *Canvas) withAlpha(a uint8, draw func()) {
	if a == 0 {
		return
	}
	if a == 255 {
		draw()
		return
	}
	c.p.Save()
	f := float64(a) / 255
	c.p.SetAlpha(f, f)
	draw()
	c.p.Restore()
}

// Fill implements paint.Canvas.
func (c *Canvas) Fill(p *paint.Path, col css.Color, evenOdd bool) {
	if p == nil || len(p.Ops) == 0 || col.A == 0 {
		return
	}
	c.begin()
	c.withAlpha(col.A, func() {
		c.p.SetFillColor(rgb(col))
		c.path(p)
		if evenOdd {
			c.p.FillEvenOdd()
		} else {
			c.p.Fill()
		}
	})
}

// Shade implements paint.Canvas: an axial shading (type 2) for a linear
// gradient, a radial one (type 3) drawn under a transform that makes its
// circles the gradient's ellipses.
func (c *Canvas) Shade(s paint.Shading) {
	if len(s.Stops) < 2 {
		return
	}
	stops := make([]pdfkit.Stop, len(s.Stops))
	for i, st := range s.Stops {
		stops[i] = pdfkit.Stop{Offset: st.Pos, Color: rgb(st.Color)}
	}
	sh := pdfkit.Shading{Stops: stops, ExtendStart: s.ExtendStart, ExtendEnd: s.ExtendEnd}
	c.begin()
	c.withAlpha(s.Stops[0].Color.A, func() {
		if s.Radial {
			if s.RX <= 0 || s.RY <= 0 || s.R1 <= 0 {
				return
			}
			c.p.Save()
			defer c.p.Restore()
			// Unit space: the ellipse of radii (RX,RY) is the unit circle.
			c.p.Transform(s.RX*c.s, 0, 0, -s.RY*c.s, c.x(s.CX), c.y(s.CY))
			sh.Radial, sh.R0, sh.R1 = true, s.R0, s.R1
		} else {
			if s.X0 == s.X1 && s.Y0 == s.Y1 {
				return
			}
			sh.X0, sh.Y0, sh.X1, sh.Y1 = c.x(s.X0), c.y(s.Y0), c.x(s.X1), c.y(s.Y1)
		}
		if err := c.p.Shade(sh); err != nil {
			c.fail(err)
		}
	})
}

// Image implements paint.Canvas. Pixel-identical images are embedded once per
// document (pdfkit content-addresses them).
func (c *Canvas) Image(img image.Image, dst paint.Rect) {
	if img == nil || dst.W <= 0 || dst.H <= 0 || img.Bounds().Empty() {
		return
	}
	c.begin()
	c.p.DrawImage(img, pdfkit.Rect{X: c.x(dst.X), Y: c.y(dst.Y + dst.H), Width: dst.W * c.s, Height: dst.H * c.s})
}

// Glyphs implements paint.Canvas: the run's font, subset-embedded with a
// /ToUnicode map, and its glyphs at the layout's positions.
func (c *Canvas) Glyphs(run *paint.GlyphRun) {
	if run == nil || len(run.Glyphs) == 0 || run.Size <= 0 {
		return
	}
	f, err := c.fonts.get(run.Font)
	if err != nil {
		c.fail(err)
		return
	}
	gs := make([]pdfkit.Glyph, len(run.Glyphs))
	for i, g := range run.Glyphs {
		gs[i] = pdfkit.Glyph{ID: g.ID, X: g.X * c.s, Text: g.Text}
	}
	c.begin()
	show := func() {
		if run.Unselectable {
			// user-select:none: drawn, but not the document's text — an
			// empty ActualText (ISO 32000-1 §14.9.4) keeps it out of what
			// pdftotext extracts and a reader copies.
			c.p.BeginActualText("")
			defer c.p.EndMarkedContent()
		}
		c.p.SetFont(f, run.Size*c.s)
		if err := c.p.ShowGlyphs(c.x(run.X), c.y(run.Y), gs); err != nil {
			c.fail(err)
		}
	}
	if run.Invisible {
		// Text rendering mode 3, neither filled nor stroked (ISO 32000-1
		// §9.3.6): text to find and copy over a bitmap of the same words.
		c.p.SetRenderMode(pdfkit.RenderInvisible)
		show()
		c.p.SetRenderMode(pdfkit.RenderFill)
		return
	}
	c.withAlpha(run.Color.A, func() {
		c.p.SetFillColor(rgb(run.Color))
		show()
	})
}
