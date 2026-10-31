// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package pdfcanvas

import (
	"github.com/go-pdfkit/pdfkit"
	"github.com/go-webengine/engine/paint"
)

var _ paint.VectorCanvas = (*Canvas)(nil)

// formKey names a vector image's form in one document.
type formKey struct {
	doc *pdfkit.Document
	key string
}

// VectorImage implements paint.VectorCanvas: the drawing is a form XObject
// (ISO 32000-1 §8.10), made the first time the document shows it and
// painted with one Do at each use, so a formula or an emoji used thirty
// times is one stream in the file. It is placed in the marked content of
// the element it stands for, which a form painted many times must be
// (§14.7.4.2): a Figure or a Formula with its alternate description. A
// Formula's drawing is outlines, not text (go-tex/math draws its glyphs as
// paths), so it also carries the formula's description as its ActualText
// (§14.9.4), what a reader copies and a search finds.
func (c *Canvas) VectorImage(v *paint.VectorImage, dst paint.Rect) bool {
	if v == nil || dst.W <= 0 || dst.H <= 0 {
		return false
	}
	doc := c.p.Document()
	if c.fonts.forms == nil {
		c.fonts.forms = map[formKey]*pdfkit.Form{}
	}
	k := formKey{doc: doc, key: v.Key}
	f, ok := c.fonts.forms[k]
	if !ok {
		f = buildForm(doc, v)
		c.fonts.forms[k] = f
	}
	c.begin()
	if e := c.curElem; c.open && !c.curArtifact && e != nil && e.Type == pdfkit.StructFormula && e.Alt != "" {
		c.p.BeginActualText(e.Alt)
		defer c.p.EndMarkedContent()
	}
	if err := c.p.DrawForm(f, pdfkit.Rect{X: c.x(dst.X), Y: c.y(dst.Y + dst.H), Width: dst.W * c.s, Height: dst.H * c.s}); err != nil {
		c.fail(err)
	}
	return true
}

// buildForm draws v into a new form whose space is v's own coordinates with
// y turned upwards: (x, y) is (x, −y), so that DrawForm, mapping the box
// onto the image's rectangle on the page, puts the drawing's top at the
// top.
func buildForm(doc *pdfkit.Document, v *paint.VectorImage) *pdfkit.Form {
	vb := v.ViewBox
	f := doc.NewForm(pdfkit.Rect{X: vb.X, Y: -(vb.Y + vb.H), Width: vb.W, Height: vb.H})
	for i := range v.Shapes {
		sh := &v.Shapes[i]
		clipped := len(sh.Clips) > 0
		if clipped {
			f.Save()
			for j := range sh.Clips {
				formPath(f, &sh.Clips[j].Path)
				if sh.Clips[j].EvenOdd {
					f.ClipEvenOdd()
				} else {
					f.Clip()
				}
				f.EndPath()
			}
		}
		if sh.Gradient == nil {
			formAlpha(f, sh.Color.A, func() {
				f.SetFillColor(rgb(sh.Color))
				formPath(f, &sh.Path)
				if sh.EvenOdd {
					f.FillEvenOdd()
				} else {
					f.Fill()
				}
			})
		} else {
			formShade(f, sh)
		}
		if clipped {
			f.Restore()
		}
	}
	return f
}

// formPath writes p in form space.
func formPath(f *pdfkit.Form, p *paint.Path) {
	for _, op := range p.Ops {
		switch op.Kind {
		case paint.PathMoveTo:
			f.MoveTo(op.P[0].X, -op.P[0].Y)
		case paint.PathLineTo:
			f.LineTo(op.P[0].X, -op.P[0].Y)
		case paint.PathCubeTo:
			f.CurveTo(op.P[0].X, -op.P[0].Y, op.P[1].X, -op.P[1].Y, op.P[2].X, -op.P[2].Y)
		case paint.PathClose:
			f.ClosePath()
		}
	}
}

// formAlpha runs draw under alpha a, as withAlpha does on a page.
func formAlpha(f *pdfkit.Form, a uint8, draw func()) {
	switch a {
	case 0:
	case 255:
		draw()
	default:
		f.Save()
		f.SetAlpha(float64(a)/255, float64(a)/255)
		draw()
		f.Restore()
	}
}

// formShade paints a gradient-filled shape: its path as the clip, the
// shading through it (axial, or radial under the transform that makes its
// circles the gradient's ellipses), at the gradient's one opacity.
func formShade(f *pdfkit.Form, sh *paint.VectorShape) {
	g := sh.Gradient
	if len(g.Stops) < 2 {
		return
	}
	stops := make([]pdfkit.Stop, len(g.Stops))
	for i, st := range g.Stops {
		stops[i] = pdfkit.Stop{Offset: st.Pos, Color: rgb(st.Color)}
	}
	s := pdfkit.Shading{Stops: stops, ExtendStart: g.ExtendStart, ExtendEnd: g.ExtendEnd}
	if g.Radial {
		if g.RX <= 0 || g.RY <= 0 || g.R1 <= 0 {
			return
		}
		s.Radial, s.R0, s.R1 = true, g.R0, g.R1
	} else if g.X0 == g.X1 && g.Y0 == g.Y1 {
		return
	} else {
		s.X0, s.Y0, s.X1, s.Y1 = g.X0, -g.Y0, g.X1, -g.Y1
	}
	f.Save()
	defer f.Restore()
	formPath(f, &sh.Path)
	if sh.EvenOdd {
		f.ClipEvenOdd()
	} else {
		f.Clip()
	}
	f.EndPath()
	formAlpha(f, g.Stops[0].Color.A, func() {
		if g.Radial {
			f.Save()
			defer f.Restore()
			f.Transform(g.RX, 0, 0, g.RY, g.CX, -g.CY)
		}
		_ = f.Shade(s) // the stops are increasing and at least two: nothing to refuse
	})
}
