// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// refCanvas is a reference rasteriser for the Canvas operations: it paints
// what PaintVector emits into an image, point-sampling every pixel centre (no
// antialiasing), so that a vector page can be compared with the raster
// painter's image of the same boxes. It also records the calls.
type refCanvas struct {
	img   *image.RGBA
	clip  []bool
	stack [][]bool

	fills, clips, shades, images int
	shadings                     []Shading
	runs                         []*GlyphRun
	imageRects                   []Rect
}

func newRefCanvas(w, h int) *refCanvas {
	c := &refCanvas{img: white(w, h), clip: make([]bool, w*h)}
	for i := range c.clip {
		c.clip[i] = true
	}
	return c
}

func (c *refCanvas) Save() { c.stack = append(c.stack, append([]bool(nil), c.clip...)) }
func (c *refCanvas) Restore() {
	c.clip = c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
}

// polys flattens a path into closed polygons.
func polys(p *Path) [][]Point {
	var out [][]Point
	var cur []Point
	for _, op := range p.Ops {
		switch op.Kind {
		case PathMoveTo:
			if len(cur) > 0 {
				out = append(out, cur)
			}
			cur = []Point{op.P[0]}
		case PathLineTo:
			cur = append(cur, op.P[0])
		case PathCubeTo:
			p0 := cur[len(cur)-1]
			for i := 1; i <= 16; i++ {
				t := float64(i) / 16
				u := 1 - t
				cur = append(cur, Point{
					u*u*u*p0.X + 3*u*u*t*op.P[0].X + 3*u*t*t*op.P[1].X + t*t*t*op.P[2].X,
					u*u*u*p0.Y + 3*u*u*t*op.P[0].Y + 3*u*t*t*op.P[1].Y + t*t*t*op.P[2].Y,
				})
			}
		case PathClose:
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// inside is the winding test at (x,y).
func inside(ps [][]Point, x, y float64, evenOdd bool) bool {
	wind := 0
	for _, poly := range ps {
		for i := range poly {
			a, b := poly[i], poly[(i+1)%len(poly)]
			if a.Y <= y && b.Y > y && (b.X-a.X)*(y-a.Y)-(x-a.X)*(b.Y-a.Y) > 0 {
				wind++
			} else if a.Y > y && b.Y <= y && (b.X-a.X)*(y-a.Y)-(x-a.X)*(b.Y-a.Y) < 0 {
				wind--
			}
		}
	}
	if evenOdd {
		return wind%2 != 0
	}
	return wind != 0
}

func (c *refCanvas) each(f func(x, y int)) {
	b := c.img.Rect
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			if c.clip[y*b.Dx()+x] {
				f(x, y)
			}
		}
	}
}

func (c *refCanvas) Clip(p *Path, evenOdd bool) {
	c.clips++
	ps := polys(p)
	w := c.img.Rect.Dx()
	for i := range c.clip {
		x, y := i%w, i/w
		if c.clip[i] && !inside(ps, float64(x)+0.5, float64(y)+0.5, evenOdd) {
			c.clip[i] = false
		}
	}
}

func (c *refCanvas) Fill(p *Path, col css.Color, evenOdd bool) {
	c.fills++
	ps := polys(p)
	c.each(func(x, y int) {
		if inside(ps, float64(x)+0.5, float64(y)+0.5, evenOdd) {
			blendPixel(c.img, x, y, col, 255)
		}
	})
}

func (c *refCanvas) Shade(s Shading) {
	c.shades++
	c.shadings = append(c.shadings, s)
	c.each(func(x, y int) {
		px, py := float64(x)+0.5, float64(y)+0.5
		var t float64
		if s.Radial {
			d := math.Hypot((px-s.CX)/s.RX, (py-s.CY)/s.RY)
			t = (d - s.R0) / (s.R1 - s.R0)
		} else {
			ax, ay := s.X1-s.X0, s.Y1-s.Y0
			t = ((px-s.X0)*ax + (py-s.Y0)*ay) / (ax*ax + ay*ay)
		}
		if (t < 0 && !s.ExtendStart) || (t > 1 && !s.ExtendEnd) {
			return
		}
		t = math.Max(0, math.Min(1, t))
		col := s.Stops[len(s.Stops)-1].Color
		for i := 0; i+1 < len(s.Stops); i++ {
			a, b := s.Stops[i], s.Stops[i+1]
			if t <= b.Pos {
				f := 0.0
				if b.Pos > a.Pos {
					f = math.Max(0, (t-a.Pos)/(b.Pos-a.Pos))
				}
				lerp := func(u, v uint8) uint8 { return uint8(float64(u) + (float64(v)-float64(u))*f + 0.5) }
				col = css.Color{R: lerp(a.Color.R, b.Color.R), G: lerp(a.Color.G, b.Color.G), B: lerp(a.Color.B, b.Color.B), A: a.Color.A}
				break
			}
		}
		blendPixel(c.img, x, y, col, 255)
	})
}

func (c *refCanvas) Image(img image.Image, dst Rect) {
	c.images++
	c.imageRects = append(c.imageRects, dst)
	sb := img.Bounds()
	c.each(func(x, y int) {
		px, py := float64(x)+0.5, float64(y)+0.5
		if px < dst.X || py < dst.Y || px >= dst.X+dst.W || py >= dst.Y+dst.H {
			return
		}
		sx := sb.Min.X + int((px-dst.X)/dst.W*float64(sb.Dx()))
		sy := sb.Min.Y + int((py-dst.Y)/dst.H*float64(sb.Dy()))
		n := color.NRGBAModel.Convert(img.At(sx, sy)).(color.NRGBA)
		if n.A > 0 {
			blendPixel(c.img, x, y, css.Color{R: n.R, G: n.G, B: n.B, A: 255}, n.A)
		}
	})
}

func (c *refCanvas) Glyphs(run *GlyphRun) {
	c.runs = append(c.runs, run)
	if run.Invisible {
		return
	}
	face := run.Font.NewFace(int(run.Size + 0.5))
	w := c.img.Rect.Dx()
	for _, g := range run.Glyphs {
		bounds, mask, mp, _, ok := face.GlyphMaskIndex(g.ID, int(math.Round(run.X+g.X)), int(math.Round(run.Y)))
		if !ok || mask == nil {
			continue
		}
		r := bounds.Intersect(c.img.Rect)
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if a := mask.AlphaAt(mp.X+x-bounds.Min.X, mp.Y+y-bounds.Min.Y).A; a > 0 && c.clip[y*w+x] {
					blendPixel(c.img, x, y, run.Color, a)
				}
			}
		}
	}
}

// text is the concatenated text of the recorded runs, visible or not.
func (c *refCanvas) text() string {
	var b strings.Builder
	for _, r := range c.runs {
		for _, g := range r.Glyphs {
			b.WriteString(g.Text)
		}
	}
	return b.String()
}

// diffFraction is the fraction of pixels where any channel differs by more
// than tol.
func diffFraction(a, b *image.RGBA, tol int) float64 {
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		for k := 0; k < 3; k++ {
			if absDiff(a.Pix[i+k], b.Pix[i+k]) > tol {
				n++
				break
			}
		}
	}
	return float64(n) / float64(len(a.Pix)/4)
}

// parity paints box both ways and fails when the vector page, rasterised by
// refCanvas, differs from PaintFull's image on more than frac of its pixels.
func parity(t *testing.T, box *layout.Box, w, h int, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image, frac float64) (*refCanvas, VectorStats) {
	t.Helper()
	want := white(w, h)
	PaintFull(want, box, NewFonts(), imgs, bgImgs)
	c := newRefCanvas(w, h)
	st := PaintVector(c, box, NewFonts(), imgs, bgImgs, image.Rect(0, 0, w, h))
	if d := diffFraction(want, c.img, 64); d > frac {
		t.Errorf("vector page differs from the raster one on %.2f %% of pixels (allowed %.2f %%)", 100*d, 100*frac)
	}
	return c, st
}

func rgb(r, g, b uint8) css.Color { return css.Color{R: r, G: g, B: b, A: 255} }

func div() *dom.Node { return &dom.Node{Type: dom.Element, Tag: "div"} }

func TestVectorBackgroundsBordersAndText(t *testing.T) {
	ts := &css.Style{Color: rgb(0, 0, 0), FontSize: 20, FontWeight: 400, FontFamily: css.Sans, Underline: true, LetterSpacing: 1}
	border := css.BorderSide{Width: 3, Style: css.BorderSolid, Color: rgb(0, 0, 200)}
	box := &layout.Box{
		Node: div(), Style: &css.Style{Background: rgb(230, 230, 230)}, W: 120, H: 80,
		Children: []*layout.Box{{
			Node: div(), X: 10, Y: 10, W: 100, H: 40,
			Style: &css.Style{Background: rgb(255, 255, 0), Border: css.Borders{Top: border, Right: border, Bottom: border, Left: css.BorderSide{Width: 6, Style: css.BorderSolid, Color: rgb(200, 0, 0)}}},
			Lines: []*layout.LineBox{{
				Inlines: []layout.InlineFragment{{Style: &css.Style{Background: rgb(0, 200, 0), BorderRadius: css.Length{Px: 4}, Border: css.Borders{Top: border}}, X: 16, Y: 14, W: 40, H: 26, First: true, Last: true}},
				Items:   []*layout.InlineItem{{Text: "Hi ─", Style: ts, X: 18, Y: 14, Ascent: 18, Width: 40, LineHeight: 26}},
			}},
		}},
	}
	c, st := parity(t, box, 120, 80, nil, nil, 0.03)
	if got := c.text(); got != "Hi ─" {
		t.Errorf("text = %q, want the item's own text, ─ from the fallback face", got)
	}
	if len(c.runs) != 2 || c.runs[1].Font == c.runs[0].Font {
		t.Errorf("want two runs, ─ in the fallback font: %d", len(c.runs))
	}
	if st.Rasterized != 0 || st.Glyphs != 4 {
		t.Errorf("stats = %+v, want all vector, four glyphs", st)
	}
}

func TestVectorRoundedBoxAndUniformBorderRing(t *testing.T) {
	side := css.BorderSide{Width: 4, Style: css.BorderSolid, Color: rgb(0, 0, 0)}
	box := &layout.Box{Node: div(), X: 5, Y: 5, W: 60, H: 40,
		Style: &css.Style{Background: rgb(250, 100, 0), BorderRadius: css.Length{Percent: 0.25, IsPercent: true}, Border: css.Borders{Top: side, Right: side, Bottom: side, Left: side}}}
	parity(t, box, 70, 50, nil, nil, 0.04)
	// A ring too thick for an inner edge is the outer shape alone.
	thick := css.BorderSide{Width: 30, Style: css.BorderSolid, Color: rgb(0, 0, 0)}
	c := newRefCanvas(70, 50)
	box.Style.Border = css.Borders{Top: thick, Right: thick, Bottom: thick, Left: thick}
	PaintVector(c, box, NewFonts(), nil, nil, image.Rect(0, 0, 70, 50))
	if px := c.img.RGBAAt(35, 25); px.R != 0 {
		t.Errorf("a border thicker than half the box fills it: %v", px)
	}
}

func TestVectorGradients(t *testing.T) {
	red, blue := rgb(255, 0, 0), rgb(0, 0, 255)
	clear := css.Color{}
	at := func(c css.Color, p float64) css.ColorStop {
		return css.ColorStop{Color: c, HasPos: true, Pos: css.Length{Percent: p, IsPercent: true}}
	}
	for _, tc := range []struct {
		name   string
		g      *css.Gradient
		shades int
		raster int
	}{
		{"two stops", &css.Gradient{AngleDeg: 90, Stops: []css.ColorStop{stop(red), stop(blue)}}, 1, 0},
		{"corner, hard stop", &css.Gradient{Corner: 2, Stops: []css.ColorStop{at(red, 0), at(red, .5), at(blue, .5), at(blue, 1)}}, 1, 0},
		{"hard stop to transparent", &css.Gradient{AngleDeg: -45, Stops: []css.ColorStop{at(red, .5), at(clear, .5)}}, 1, 0},
		{"transparent then colour", &css.Gradient{AngleDeg: 180, Stops: []css.ColorStop{at(clear, .3), at(blue, .3), at(red, 1)}}, 1, 0},
		{"a zero-width band between two hard stops", &css.Gradient{AngleDeg: 90, Stops: []css.ColorStop{at(red, 0), at(red, .3), at(css.Color{B: 255, A: 128}, .3), at(clear, .3), at(clear, 1)}}, 1, 0},
		{"hard stop to a translucent end", &css.Gradient{AngleDeg: 90, Stops: []css.ColorStop{at(red, 0), at(red, .5), at(css.Color{B: 255, A: 128}, .5)}}, 2, 0},
		{"one stop", &css.Gradient{AngleDeg: 90, Stops: []css.ColorStop{stop(red)}}, 1, 0},
		{"translucent", &css.Gradient{AngleDeg: 0, Stops: []css.ColorStop{stop(css.Color{R: 255, A: 128}), stop(css.Color{B: 255, A: 128})}}, 1, 0},
		{"fading", &css.Gradient{AngleDeg: 90, Stops: []css.ColorStop{stop(red), stop(clear)}}, 0, 1},
		{"radial circle", &css.Gradient{Radial: true, Shape: css.RadialCircle, PosX: css.Length{Percent: .5, IsPercent: true}, PosY: css.Length{Percent: .5, IsPercent: true}, Stops: []css.ColorStop{stop(rgb(255, 255, 255)), stop(blue)}}, 1, 0},
		{"radial ellipse, hard ring", &css.Gradient{Radial: true, PosX: css.Length{Percent: .3, IsPercent: true}, PosY: css.Length{Percent: .4, IsPercent: true}, Stops: []css.ColorStop{at(red, 0), at(red, .4), at(blue, .4), at(blue, 1)}}, 1, 0},
		{"radial, stops past the end", &css.Gradient{Radial: true, PosX: css.Length{Percent: .5, IsPercent: true}, PosY: css.Length{Percent: .5, IsPercent: true}, Stops: []css.ColorStop{at(red, 0), at(blue, 1.5)}}, 1, 0},
		{"radial, opaque only at the centre", &css.Gradient{Radial: true, PosX: css.Length{Percent: .5, IsPercent: true}, PosY: css.Length{Percent: .5, IsPercent: true}, Stops: []css.ColorStop{at(red, 0), at(clear, 0)}}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &css.Style{BackgroundImages: []css.BgImage{{Kind: css.BgGradient, Grad: tc.g}}, BorderRadius: css.Length{Px: 6}}
			box := &layout.Box{Node: div(), X: 4, Y: 4, W: 90, H: 50, Style: st}
			c, vs := parity(t, box, 100, 60, nil, nil, 0.04)
			if c.shades != tc.shades || vs.Rasterized != tc.raster {
				t.Errorf("shades %d, rasterised %d; want %d and %d", c.shades, vs.Rasterized, tc.shades, tc.raster)
			}
			for _, s := range c.shadings {
				for i := 1; i < len(s.Stops); i++ {
					if s.Stops[i].Pos <= s.Stops[i-1].Pos || s.Stops[i].Color.A != s.Stops[0].Color.A {
						t.Errorf("stops %v: positions must increase and share one alpha", s.Stops)
					}
				}
			}
		})
	}
	// A nil gradient layer paints nothing.
	c := newRefCanvas(10, 10)
	PaintVector(c, &layout.Box{Node: div(), W: 10, H: 10, Style: &css.Style{BackgroundImages: []css.BgImage{{Kind: css.BgGradient}}}}, NewFonts(), nil, nil, image.Rect(0, 0, 10, 10))
	if c.shades != 0 || c.fills != 0 {
		t.Error("a nil gradient drew")
	}
}

func TestAlphaRunsAndStrictlyIncreasing(t *testing.T) {
	if _, ok := alphaRuns(nil); ok {
		t.Error("no stops: ok")
	}
	s := strictlyIncreasing([]GradientStop{{Pos: 0}, {Pos: 1}, {Pos: 1}})
	if s[2].Pos > 1 || s[2].Pos <= s[1].Pos {
		t.Errorf("hard stop at the end: %v", s)
	}
}

func TestVectorBackgroundImages(t *testing.T) {
	src := twoTone(4, 4)
	for _, tc := range []struct {
		name   string
		size   css.BgSize
		rep    css.BgRepeat
		images int
		raster int
	}{
		{"cover", css.BgSize{Kind: css.SizeCover}, css.NoRepeat, 1, 0},
		{"repeat", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 20}, H: css.Length{Px: 20}}, css.RepeatBoth, 18, 0},
		{"repeat-x", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 30}, H: css.Length{Px: 30}}, css.RepeatX, 4, 0},
		{"repeat-y", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 30}, H: css.Length{Px: 30}}, css.RepeatY, 3, 0},
		// The raster fallback's one image.
		{"too many tiles", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 2}, H: css.Length{Px: 2}}, css.RepeatBoth, 1, 1},
		{"no size: one-pixel tiles, too many", css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: 0}, H: css.Length{Px: 0}}, css.RepeatBoth, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := urlStyle("u", tc.size, css.BgPosition{X: css.Length{Px: 5}, Y: css.Length{Percent: .5, IsPercent: true}}, tc.rep)
			box := &layout.Box{Node: div(), X: 0, Y: 0, W: 90, H: 40, Style: st}
			bg := map[string]image.Image{"u": src}
			c, vs := parity(t, box, 300, 300, nil, bg, 0.02)
			if c.images != tc.images || vs.Rasterized != tc.raster {
				t.Errorf("images %d, rasterised %d; want %d and %d", c.images, vs.Rasterized, tc.images, tc.raster)
			}
		})
	}
	// An empty bitmap, a layer whose bitmap is missing and a size that is
	// not a number draw nothing.
	c := newRefCanvas(10, 10)
	st := urlStyle("e", css.BgSize{Kind: css.SizeCover}, css.BgPosition{}, css.NoRepeat)
	st.BackgroundImages = append(st.BackgroundImages, css.BgImage{Kind: css.BgURL, URL: "missing"}, css.BgImage{Kind: css.BgURL, URL: "nan"})
	st.BackgroundSize = append(st.BackgroundSize, css.BgSize{Kind: css.SizeCover}, css.BgSize{Kind: css.SizeExplicit, W: css.Length{Px: math.NaN()}, H: css.Length{Px: 3}})
	PaintVector(c, &layout.Box{Node: div(), W: 10, H: 10, Style: st}, NewFonts(), nil, map[string]image.Image{"e": image.NewRGBA(image.Rect(0, 0, 0, 0)), "nan": src}, image.Rect(0, 0, 10, 10))
	if c.images != 0 {
		t.Error("an empty bitmap was placed")
	}
}

func TestVectorShadows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sh     css.BoxShadow
		raster int
		fills  int
	}{
		{"sharp outset", css.BoxShadow{OffsetX: 6, OffsetY: 4, Color: rgb(0, 120, 0)}, 0, 2},
		{"blurred outset", css.BoxShadow{OffsetY: 4, Blur: 8, Color: css.Color{A: 160}}, 1, 1},
		{"spread swallowing the box", css.BoxShadow{Spread: -40, Color: rgb(0, 120, 0)}, 0, 1},
		{"transparent", css.BoxShadow{OffsetX: 6, Color: css.Color{}}, 0, 1},
		{"sharp inset", css.BoxShadow{Inset: true, OffsetX: 3, OffsetY: 3, Spread: 2, Color: rgb(0, 0, 160)}, 0, 2},
		{"inset swallowing the box", css.BoxShadow{Inset: true, Spread: 40, Color: rgb(0, 0, 160)}, 0, 2},
		{"blurred inset", css.BoxShadow{Inset: true, Blur: 6, Color: css.Color{A: 200}}, 1, 1},
		{"transparent inset", css.BoxShadow{Inset: true, Color: css.Color{}}, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			box := &layout.Box{Node: div(), X: 20, Y: 15, W: 50, H: 30, Style: &css.Style{Background: rgb(240, 240, 255), BorderRadius: css.Length{Px: 5}, BoxShadows: []css.BoxShadow{tc.sh}}}
			c, vs := parity(t, box, 100, 70, nil, nil, 0.03)
			if vs.Rasterized != tc.raster || c.fills != tc.fills {
				t.Errorf("rasterised %d, fills %d; want %d and %d", vs.Rasterized, c.fills, tc.raster, tc.fills)
			}
		})
	}
}

func TestVectorMarkers(t *testing.T) {
	ms := &css.Style{Color: rgb(0, 0, 0), FontSize: 16, FontWeight: 400, FontFamily: css.Sans}
	for _, typ := range []css.ListStyleType{css.ListDisc, css.ListCircle, css.ListSquare, css.ListDecimal} {
		box := &layout.Box{Node: div(), X: 0, Y: 0, W: 60, H: 30, Style: &css.Style{},
			Marker: &layout.Marker{Type: typ, Style: ms, Text: "1.", X: 10, Y: 10, W: 8, H: 8, Ascent: 14}}
		parity(t, box, 60, 30, nil, nil, 0.03)
	}
	// A marker outside the clip, or with no style, draws nothing; a tiny
	// circle has no hole.
	c := newRefCanvas(40, 40)
	v := &vpainter{c: c, f: NewFonts(), page: image.Rect(0, 0, 40, 40)}
	v.marker(&layout.Marker{Type: css.ListDisc, Style: ms, X: 100, Y: 100, W: 8, H: 8}, v.page)
	v.marker(&layout.Marker{Type: css.ListDisc, X: 1, Y: 1, W: 8, H: 8}, v.page)
	v.marker(&layout.Marker{Type: css.ListCircle, Style: ms, X: 1, Y: 1, W: 2, H: 2}, v.page)
	if c.fills != 1 {
		t.Errorf("fills = %d, want only the tiny circle", c.fills)
	}
}

func TestVectorImages(t *testing.T) {
	n := &dom.Node{Type: dom.Element, Tag: "img"}
	src := twoTone(8, 4)
	item := func(st *css.Style, w, h float64) *layout.Box {
		return &layout.Box{Node: div(), W: 80, H: 60, Style: &css.Style{}, Lines: []*layout.LineBox{{Items: []*layout.InlineItem{{Image: n, Style: st, X: 10, Y: 10, Width: w, LineHeight: h}}}}}
	}
	imgs := map[*dom.Node]image.Image{n: src}
	for name, b := range map[string]*layout.Box{
		"fill":    item(&css.Style{}, 40, 20),
		"cover":   item(&css.Style{ObjectFit: css.ObjectFitCover}, 30, 30),
		"contain": item(&css.Style{ObjectFit: css.ObjectFitContain}, 30, 30),
		"natural": item(nil, 0, 0),
		"filter":  item(&css.Style{Filters: []css.Filter{{Kind: css.FilterInvert, Amount: 1}}}, 40, 20),
	} {
		t.Run(name, func(t *testing.T) {
			c, vs := parity(t, b, 80, 60, imgs, nil, 0.03)
			if c.images != 1 || vs.Images != 1 {
				t.Errorf("images = %d", c.images)
			}
		})
	}
	// Missing, empty and off-page images draw nothing.
	c := newRefCanvas(80, 60)
	PaintVector(c, item(nil, 40, 20), NewFonts(), nil, nil, image.Rect(0, 0, 80, 60))
	PaintVector(c, item(nil, 40, 20), NewFonts(), map[*dom.Node]image.Image{n: image.NewRGBA(image.Rect(0, 0, 0, 3))}, nil, image.Rect(0, 0, 80, 60))
	off := item(nil, 40, 20)
	off.Lines[0].Items[0].X = 500
	PaintVector(c, off, NewFonts(), imgs, nil, image.Rect(0, 0, 80, 60))
	if c.images != 0 {
		t.Errorf("images = %d, want none", c.images)
	}
}

// TestVectorGroupsAreRasterisedWithTheirTextKept: what has no vector form
// is drawn as a bitmap of that element only — and its text is laid over it,
// invisible, so it can still be selected.
func TestVectorGroupsAreRasterisedWithTheirTextKept(t *testing.T) {
	ts := &css.Style{Color: rgb(0, 0, 0), FontSize: 18, FontWeight: 400, FontFamily: css.Sans}
	hidden := &css.Style{Color: rgb(0, 0, 0), FontSize: 18, FontWeight: 400, FontFamily: css.Sans, Visibility: css.VisibilityHidden}
	mk := func(st *css.Style) *layout.Box {
		inner := &layout.Box{Node: div(), X: 10, Y: 40, W: 40, H: 20, Style: &css.Style{Background: rgb(0, 0, 255)},
			Lines: []*layout.LineBox{{Items: []*layout.InlineItem{{Text: "in", Style: ts, X: 12, Y: 40, Ascent: 15, Width: 20, LineHeight: 20}}}}}
		nested := &layout.Box{Node: div(), Style: &css.Style{}, Lines: []*layout.LineBox{{Items: []*layout.InlineItem{{Text: "nest", Style: ts, X: 60, Y: 40, Ascent: 15, Width: 30, LineHeight: 20}}}}}
		return &layout.Box{Node: div(), X: 5, Y: 5, W: 100, H: 70, Style: st,
			Marker: &layout.Marker{Type: css.ListDecimal, Style: ts, Text: "1.", X: 0, Y: 5, Ascent: 15},
			Lines: []*layout.LineBox{{Items: []*layout.InlineItem{
				{Text: "Group", Style: ts, X: 10, Y: 8, Ascent: 15, Width: 50, LineHeight: 22},
				{Text: "gone", Style: hidden, X: 70, Y: 8, Ascent: 15, Width: 30, LineHeight: 22},
				{Text: "far", Style: ts, X: 900, Y: 8, Ascent: 15, Width: 30, LineHeight: 22},
				{NestedBox: nested},
			}}},
			Children: []*layout.Box{inner, {Node: div(), Style: &css.Style{HasOpacity: true, Opacity: 0}}, nil}}
	}
	for name, st := range map[string]*css.Style{
		"opacity": {Background: rgb(255, 200, 0), HasOpacity: true, Opacity: 0.5},
		"filter":  {Background: rgb(255, 200, 0), Filters: []css.Filter{{Kind: css.FilterGrayscale, Amount: 1}}},
		"clip":    {Background: rgb(255, 200, 0), HasOpacity: true, Opacity: 0.5, HasClip: true, ClipRect: css.Edges{Right: 1000, Bottom: 1000}},
	} {
		t.Run(name, func(t *testing.T) {
			b := mk(st)
			if name == "clip" {
				b.Position = css.PositionAbsolute
			}
			c, vs := parity(t, b, 120, 80, nil, nil, 0.03)
			if vs.Rasterized != 1 {
				t.Errorf("rasterised %d, want the one group", vs.Rasterized)
			}
			if got := c.text(); got != "1.Groupnestin" {
				t.Errorf("ghost text = %q", got)
			}
			for _, r := range c.runs {
				if !r.Invisible {
					t.Error("a group's text must be invisible over its bitmap")
				}
			}
		})
	}
	// A rotated box keeps no ghost text: its words are not where the
	// layout put them.
	b := mk(&css.Style{Background: rgb(255, 200, 0), RotateDeg: 10})
	c, vs := parity(t, b, 120, 80, nil, nil, 1)
	if vs.Rasterized != 1 || len(c.runs) != 0 {
		t.Errorf("rotated: rasterised %d, runs %d", vs.Rasterized, len(c.runs))
	}
	// Opacity 0 draws nothing at all, and so does ghostText of nothing.
	c = newRefCanvas(10, 10)
	PaintVector(c, &layout.Box{Style: &css.Style{HasOpacity: true, Opacity: 0, Background: rgb(1, 2, 3)}, W: 5, H: 5}, NewFonts(), nil, nil, image.Rect(0, 0, 10, 10))
	v := &vpainter{c: c, f: NewFonts(), page: image.Rect(0, 0, 10, 10)}
	v.ghostText(nil, v.page)
	v.ghostText(&layout.Box{}, v.page)
	v.ghostText(&layout.Box{Style: &css.Style{HasOpacity: true}}, v.page)
	if c.fills+len(c.runs) != 0 {
		t.Error("nothing should have been drawn")
	}
}

func TestVectorClipsOverflowAndClipRect(t *testing.T) {
	ts := &css.Style{Color: rgb(0, 0, 0), FontSize: 18, FontWeight: 400, FontFamily: css.Sans}
	child := &layout.Box{Node: div(), X: 0, Y: 0, W: 200, H: 200, Style: &css.Style{Background: rgb(255, 0, 0)},
		Lines: []*layout.LineBox{{Items: []*layout.InlineItem{{Text: "overflowing text", Style: ts, X: 5, Y: 5, Ascent: 15}}}}}
	box := &layout.Box{Node: div(), X: 10, Y: 10, W: 40, H: 30, Children: []*layout.Box{child},
		Style: &css.Style{OverflowX: css.OverflowHidden, OverflowY: css.OverflowHidden, Height: css.Length{Px: 30}}}
	c, _ := parity(t, box, 100, 100, nil, nil, 0.03)
	if c.clips == 0 {
		t.Error("an overflow clip must become a clip path")
	}
	// clip: rect(0 0 0 0), the visually-hidden idiom, draws nothing.
	sr := &layout.Box{Node: div(), X: 10, Y: 10, W: 40, H: 30, Position: css.PositionAbsolute,
		Style: &css.Style{Background: rgb(255, 0, 0), HasClip: true}}
	c = newRefCanvas(60, 60)
	PaintVector(c, sr, NewFonts(), nil, nil, image.Rect(0, 0, 60, 60))
	if c.fills != 0 {
		t.Error("clip: rect(0 0 0 0) painted")
	}
	// An overflow clip of zero size hides the children.
	zero := &layout.Box{Node: div(), X: 10, Y: 10, W: 0, H: 0, Children: []*layout.Box{child},
		Style: &css.Style{OverflowX: css.OverflowHidden, OverflowY: css.OverflowHidden, Height: css.Length{Px: 0}}}
	c = newRefCanvas(60, 60)
	PaintVector(c, zero, NewFonts(), nil, nil, image.Rect(0, 0, 60, 60))
	if c.fills != 0 {
		t.Error("a zero-sized clip let its children through")
	}
	// A narrower clip rect is a clip path.
	part := &layout.Box{Node: div(), X: 10, Y: 10, W: 40, H: 30, Position: css.PositionAbsolute,
		Style: &css.Style{Background: rgb(255, 0, 0), HasClip: true, ClipRect: css.Edges{Right: 20, Bottom: 30}}}
	parity(t, part, 60, 60, nil, nil, 0.03)
}

func TestVectorInlineContent(t *testing.T) {
	ts := &css.Style{Color: rgb(0, 0, 0), FontSize: 18, FontWeight: 400, FontFamily: css.Sans}
	nested := &layout.Box{Node: div(), X: 40, Y: 5, W: 30, H: 20, Style: &css.Style{Background: rgb(0, 160, 0)}}
	box := &layout.Box{Node: div(), W: 100, H: 40, Style: &css.Style{}, Lines: []*layout.LineBox{{
		Inlines: []layout.InlineFragment{{Style: nil, W: 10, H: 10}, {Style: &css.Style{Background: rgb(9, 9, 9)}, W: 0, H: 10}},
		Items: []*layout.InlineItem{
			{NestedBox: nested},
			{Text: "x", Style: &css.Style{Color: rgb(0, 0, 0), FontSize: 18, FontFamily: css.Sans, Visibility: css.VisibilityHidden}, X: 2, Y: 2, Ascent: 15},
			{Text: "", Style: ts},
			{Text: "y"},
			{Text: "中", Style: ts, X: 80, Y: 2, Ascent: 15},
			{Text: "z", Style: &css.Style{Color: css.Color{}, FontSize: 18, FontFamily: css.Sans}, X: 90, Y: 2, Ascent: 15},
		},
	}}}
	c, _ := parity(t, box, 100, 40, nil, nil, 0.02)
	if c.text() != "" {
		t.Errorf("text = %q, want none drawn: hidden, empty, unstyled, uncovered, transparent", c.text())
	}
}

// TestVectorLigatureKeepsItsWholeText: a glyph standing for several
// characters carries all of them, so copying an icon-font word gives the word.
func TestVectorLigatureKeepsItsWholeText(t *testing.T) {
	f, fam := materialIconsFamily(t)
	st := &css.Style{FontFamily: fam, FontSize: 48, FontWeight: 400, Color: rgb(0, 0, 0)}
	c := newRefCanvas(80, 80)
	v := &vpainter{c: c, f: f, page: image.Rect(0, 0, 80, 80)}
	v.text(st, "arrow_drop_down", 4, 60, st.Color, false)
	if len(c.runs) != 1 || len(c.runs[0].Glyphs) != 1 || c.runs[0].Glyphs[0].Text != "arrow_drop_down" {
		t.Errorf("runs = %+v, want one glyph carrying the word", c.runs)
	}
}

func TestVectorFormControlIsRasterised(t *testing.T) {
	root, err := dom.Parse("<button>OK</button>")
	if err != nil {
		t.Fatal(err)
	}
	btn := dom.Find(root, "button")
	st := &css.Style{Color: rgb(0, 0, 0), FontSize: 14, FontFamily: css.Sans, Background: rgb(239, 239, 239)}
	box := &layout.Box{Node: div(), W: 100, H: 40, Style: &css.Style{}, Lines: []*layout.LineBox{{Items: []*layout.InlineItem{
		{FormControl: btn, Style: st, X: 10, Y: 5, Width: 60, LineHeight: 26, Ascent: 18},
	}}}}
	_, vs := parity(t, box, 100, 40, nil, nil, 0.02)
	if vs.Rasterized != 1 {
		t.Errorf("rasterised %d, want the control", vs.Rasterized)
	}
}

func TestVectorRasterFallbackBounds(t *testing.T) {
	c := newRefCanvas(10, 10)
	// A page that does not start at the origin, or is too large, gets no
	// scratch buffer: the piece is skipped and counted.
	for _, page := range []image.Rectangle{image.Rect(1, 1, 10, 10), image.Rect(0, 0, 1<<14, 1<<14)} {
		v := &vpainter{c: c, f: NewFonts(), page: page}
		v.box(&layout.Box{Node: div(), X: 2, Y: 2, W: 5, H: 5, Style: &css.Style{Background: rgb(1, 2, 3), HasOpacity: true, Opacity: .5}}, page)
		if v.stats.Skipped != 1 || v.stats.Rasterized != 0 {
			t.Errorf("page %v: stats %+v", page, v.stats)
		}
	}
	// Outside the page, and painting nothing, place nothing; nor do no box
	// and an empty clip.
	v := &vpainter{c: c, f: NewFonts(), page: image.Rect(0, 0, 10, 10)}
	v.box(nil, v.page)
	v.box(&layout.Box{Style: &css.Style{Background: rgb(1, 2, 3)}, W: 5, H: 5}, image.Rectangle{})
	v.box(&layout.Box{Node: div(), X: 50, Y: 50, W: 5, H: 5, Style: &css.Style{Background: rgb(1, 2, 3), HasOpacity: true, Opacity: .5}}, image.Rect(20, 20, 30, 30))
	v.box(&layout.Box{Node: div(), X: 2, Y: 2, W: 5, H: 5, Style: &css.Style{HasOpacity: true, Opacity: .5}}, v.page)
	if c.images != 0 || v.stats.Rasterized != 0 {
		t.Errorf("images %d, stats %+v", c.images, v.stats)
	}
	if !inkBounds(image.NewRGBA(image.Rect(0, 0, 4, 4)), image.Rect(0, 0, 4, 4)).Empty() {
		t.Error("an empty buffer has ink")
	}
}

func TestPathShapes(t *testing.T) {
	var p Path
	p.RoundRect(Rect{W: 0, H: 5}, 2)
	p.Ellipse(Rect{W: 5, H: 0})
	if len(p.Ops) != 0 {
		t.Errorf("empty shapes added %d ops", len(p.Ops))
	}
	p.RoundRect(Rect{W: 10, H: 4}, 50) // clamped to 2
	if got := p.Ops[1].P[0].X; got != 8 {
		t.Errorf("radius not clamped to half the short side: line ends at %v", got)
	}
	if r := rectOfImage(image.Rect(1, 2, 4, 8)); r != (Rect{X: 1, Y: 2, W: 3, H: 6}) {
		t.Errorf("rectOfImage = %+v", r)
	}
	if radiusOf(nil, 1, 1) != 0 || radiusOf(&css.Style{BorderRadius: css.Length{Auto: true, Px: 3}}, 1, 1) != 0 || radiusOf(&css.Style{BorderRadius: css.Length{Px: -3}}, 1, 1) != 0 {
		t.Error("radiusOf: no style, auto and negative are 0")
	}
}
