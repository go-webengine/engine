// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package officecanvas records a laid-out page as the editable objects an
// office presentation is made of — filled shapes, gradient-filled shapes,
// lines of text in text boxes, and pictures — instead of drawing it. It is a
// paint.Canvas: PageLayout.PaintVector draws through it exactly as it draws a
// PDF page through pdfcanvas, so an editable slide has the layout of the
// image and PDF exports by construction. The recorded Page is format-neutral,
// in device pixels; a writer maps it to DrawingML (PresentationML) or to ODF
// draw elements.
//
// What a slide format cannot hold the paint layer already turned into
// pictures (an element under opacity, a filter, a blurred shadow); those
// arrive here as images, with their text as invisible runs over them.
//
// Things a slide cannot do that a page can: clip. A gradient or a picture
// inside a clip takes the clip's outline (a rounded box) or is cropped to its
// bounds; a filled shape ignores the clips around it, which are the overflow
// clips of its ancestors and cut nothing on a slide that fits.
package officecanvas

import (
	"image"
	"math"

	"github.com/go-opentype/opentype"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

// Kind is what a recorded Shape is.
type Kind uint8

// The shapes.
const (
	KindFill     Kind = iota // Path filled with Color
	KindGradient             // Path filled with Gradient
	KindText                 // a line of Runs in Bounds
	KindImage                // Image stretched over Bounds, after Crop
	KindGroup                // the shapes of Group, one drawing (an SVG)
)

// Shape is one recorded object. Bounds is its box in device pixels.
type Shape struct {
	Kind   Kind
	Bounds paint.Rect

	// Path is the outline of a fill or gradient, absolute, and EvenOdd its
	// rule (overlaps are holes). Rect is set when the outline is Bounds
	// itself.
	Path    *paint.Path
	EvenOdd bool
	Rect    bool

	Color    css.Color // KindFill
	Gradient *Gradient // KindGradient

	Runs []Run // KindText

	Image image.Image // KindImage
	// Crop is what to remove from the picture's left, top, right and
	// bottom, as fractions, before it is stretched over Bounds.
	Crop [4]float64

	// Group is a KindGroup's shapes, in page coordinates like any shape,
	// drawn in order: an SVG drawing as vector shapes, which a slide keeps
	// together (a p:grpSp, a draw:g). Bounds is their union.
	Group []Shape
}

// Run is a stretch of a text line in one face, size and colour. Size is in
// device pixels. Invisible text is there to be found and copied over a
// picture of the same words; it is drawn with zero alpha.
type Run struct {
	Text      string
	Face      *Face
	Size      float64
	Color     css.Color
	Link      string
	Invisible bool
	// Spacing is the space added after each character beyond its own
	// advance (CSS letter-spacing), in device pixels.
	Spacing float64
}

// Face is a font face the runs are set in.
type Face struct {
	Font         *opentype.Font
	Family       string // the name a document names it by (name ID 1)
	Bold, Italic bool   // its style within that family (name ID 2)
	// Embeddable is false when the font's OS/2 fsType forbids embedding it
	// in an editable document (restricted, or preview & print only).
	Embeddable bool
}

// Gradient is a gradient fill in office terms: stops along a direction
// across the shape's box, or around a centre.
//
// Linear: Angle is the direction of colour change in degrees clockwise from
// the x axis (y down), and the stops run across the projection of the box on
// that direction — DrawingML's unscaled a:lin, and CSS's gradient line. Radial:
// the stops run from Center (fractions of the box) outwards.
//
// Shadings are the paint-level shadings it was made from, for a writer that
// cannot express it and draws it as a picture (Shape.Render).
type Gradient struct {
	Radial   bool
	Angle    float64
	Center   [2]float64
	Stops    []paint.GradientStop
	Shadings []paint.Shading
}

// Page is what was drawn on one page.
type Page struct {
	// Background is the colour of a fill covering the whole page drawn
	// before anything else: the slide's background. Nil when there is none.
	Background *css.Color
	Shapes     []Shape
}

// Link is a clickable area: text drawn inside it links to Href.
type Link struct {
	Rect image.Rectangle
	Href string
}

// Canvas records a page. Build one per page with New; it is not safe for
// concurrent use.
type Canvas struct {
	page  paint.Rect
	links []Link
	faces map[*opentype.Font]*Face
	out   Page

	clips []clip // the clip paths in force, innermost last
	saves []int  // len(clips) at each Save

	// gradOwner is the clip a run of shadings is being merged under.
	gradOwner *paint.Path

	// groups are the indices in out.Shapes where the open groups start.
	groups []int
}

type clip struct {
	path    *paint.Path
	evenOdd bool
}

// New starts recording a page of the given device-pixel size. links are the
// page's clickable areas (engine.PageLayout.Links). faces, shared across the
// pages of one document, maps each font to its Face; nil gets a fresh map.
func New(page image.Rectangle, links []Link, faces map[*opentype.Font]*Face) *Canvas {
	if faces == nil {
		faces = map[*opentype.Font]*Face{}
	}
	return &Canvas{page: paint.Rect{X: float64(page.Min.X), Y: float64(page.Min.Y), W: float64(page.Dx()), H: float64(page.Dy())}, links: links, faces: faces}
}

var _ paint.Canvas = (*Canvas)(nil)

// Page returns what was recorded.
func (c *Canvas) Page() Page { return c.out }

// Save implements paint.Canvas.
func (c *Canvas) Save() { c.saves = append(c.saves, len(c.clips)) }

// Restore implements paint.Canvas.
func (c *Canvas) Restore() {
	if len(c.saves) == 0 {
		return
	}
	n := c.saves[len(c.saves)-1]
	c.saves = c.saves[:len(c.saves)-1]
	c.clips = c.clips[:n]
}

// Clip implements paint.Canvas: the clip is remembered, to shape what is
// drawn in it.
func (c *Canvas) Clip(p *paint.Path, evenOdd bool) {
	c.clips = append(c.clips, clip{path: p, evenOdd: evenOdd})
}

// Fill implements paint.Canvas.
func (c *Canvas) Fill(p *paint.Path, col css.Color, evenOdd bool) {
	if p == nil || len(p.Ops) == 0 || col.A == 0 {
		return
	}
	b, ok := bounds(p)
	if !ok {
		return
	}
	rect := isRect(p, b)
	if rect && len(c.out.Shapes) == 0 && len(c.groups) == 0 && covers(b, c.page) {
		bg := col
		bg.A = 255
		c.out.Background = &bg
		return
	}
	c.out.Shapes = append(c.out.Shapes, Shape{Kind: KindFill, Bounds: b, Path: p, EvenOdd: evenOdd, Rect: rect, Color: col})
}

// Shade implements paint.Canvas: the gradient fills the innermost clip's
// outline. PaintVector splits one CSS gradient into shadings of constant
// opacity, all under one clip; they are merged back into one gradient here,
// a stop per stop, transparent across the gaps between them.
func (c *Canvas) Shade(s paint.Shading) {
	if len(s.Stops) < 2 || len(c.clips) == 0 {
		return
	}
	cl := c.clips[len(c.clips)-1]
	b, ok := bounds(cl.path)
	if !ok || b.W <= 0 || b.H <= 0 {
		return
	}
	var g *Gradient
	if n := len(c.out.Shapes); n > 0 && c.gradOwner == cl.path && c.out.Shapes[n-1].Kind == KindGradient {
		g = c.out.Shapes[n-1].Gradient
	} else {
		g = &Gradient{Radial: s.Radial}
		if s.Radial {
			g.Center = [2]float64{(s.CX - b.X) / b.W, (s.CY - b.Y) / b.H}
		} else {
			g.Angle = math.Atan2(s.Y1-s.Y0, s.X1-s.X0) * 180 / math.Pi
		}
		c.out.Shapes = append(c.out.Shapes, Shape{Kind: KindGradient, Bounds: b, Path: cl.path, EvenOdd: cl.evenOdd, Rect: isRect(cl.path, b), Gradient: g})
		c.gradOwner = cl.path
	}
	g.Shadings = append(g.Shadings, s)
	pos := func(t float64) float64 { return positionOf(s, t, b) }
	first, last := pos(s.Stops[0].Pos), pos(s.Stops[len(s.Stops)-1].Pos)
	if n := len(g.Stops); n > 0 && first-g.Stops[n-1].Pos > 1e-4 {
		// A gap: the previous run ended, nothing is painted until this one.
		end := g.Stops[n-1]
		end.Color.A = 0
		start := paint.GradientStop{Pos: first, Color: s.Stops[0].Color}
		start.Color.A = 0
		g.Stops = append(g.Stops, end, start)
	} else if n == 0 && !s.ExtendStart && first > 1e-4 {
		start := paint.GradientStop{Pos: first, Color: s.Stops[0].Color}
		start.Color.A = 0
		g.Stops = append(g.Stops, paint.GradientStop{Pos: 0, Color: start.Color}, start)
	}
	for _, st := range s.Stops {
		g.Stops = append(g.Stops, paint.GradientStop{Pos: clamp01(pos(st.Pos)), Color: st.Color})
	}
	if !s.ExtendEnd && last < 1-1e-4 {
		end := s.Stops[len(s.Stops)-1].Color
		end.A = 0
		g.Stops = append(g.Stops, paint.GradientStop{Pos: clamp01(last), Color: end}, paint.GradientStop{Pos: 1, Color: end})
	}
}

// positionOf maps position t of shading s to the office gradient's own
// position: across the box's projection on the gradient's direction for a
// linear one, as a fraction of the radius for a radial one.
func positionOf(s paint.Shading, t float64, b paint.Rect) float64 {
	if s.Radial {
		return s.R0 + t*(s.R1-s.R0)
	}
	dx, dy := s.X1-s.X0, s.Y1-s.Y0
	l := math.Hypot(dx, dy)
	if l == 0 {
		return t
	}
	ux, uy := dx/l, dy/l
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range [][2]float64{{b.X, b.Y}, {b.X + b.W, b.Y}, {b.X, b.Y + b.H}, {b.X + b.W, b.Y + b.H}} {
		d := p[0]*ux + p[1]*uy
		lo, hi = math.Min(lo, d), math.Max(hi, d)
	}
	if hi <= lo {
		return t
	}
	px, py := s.X0+t*dx, s.Y0+t*dy
	return (px*ux + py*uy - lo) / (hi - lo)
}

func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// Image implements paint.Canvas: a picture, cropped to the innermost clip's
// bounds when the clip cuts it (a background image sized to cover its box).
func (c *Canvas) Image(img image.Image, dst paint.Rect) {
	if img == nil || dst.W <= 0 || dst.H <= 0 || img.Bounds().Empty() {
		return
	}
	sh := Shape{Kind: KindImage, Bounds: dst, Image: img}
	if len(c.clips) > 0 {
		cb, ok := bounds(c.clips[len(c.clips)-1].path)
		if ok {
			x0, y0 := math.Max(dst.X, cb.X), math.Max(dst.Y, cb.Y)
			x1, y1 := math.Min(dst.X+dst.W, cb.X+cb.W), math.Min(dst.Y+dst.H, cb.Y+cb.H)
			if x1 <= x0 || y1 <= y0 {
				return
			}
			sh.Crop = [4]float64{(x0 - dst.X) / dst.W, (y0 - dst.Y) / dst.H, (dst.X + dst.W - x1) / dst.W, (dst.Y + dst.H - y1) / dst.H}
			for i := range sh.Crop {
				if sh.Crop[i] < 1e-6 {
					sh.Crop[i] = 0
				}
			}
			sh.Bounds = paint.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
		}
	}
	c.out.Shapes = append(c.out.Shapes, sh)
}

var (
	_ paint.GroupCanvas     = (*Canvas)(nil)
	_ paint.VectorSupporter = (*Canvas)(nil)
)

// VectorSupport implements paint.VectorSupporter. A slide's shape cannot be
// clipped, and an office radial gradient runs from its centre to the box's
// corners, not to a radius: a vector image that needs either is placed as
// its picture.
func (c *Canvas) VectorSupport() paint.VectorSupport { return paint.VectorSupport{} }

// BeginGroup implements paint.GroupCanvas: the shapes drawn until EndGroup
// are one drawing.
func (c *Canvas) BeginGroup() {
	c.groups = append(c.groups, len(c.out.Shapes))
	c.gradOwner = nil
}

// EndGroup implements paint.GroupCanvas: the shapes since BeginGroup become
// one KindGroup, or stay as they are when there is only one.
func (c *Canvas) EndGroup() {
	if len(c.groups) == 0 {
		return
	}
	start := c.groups[len(c.groups)-1]
	c.groups = c.groups[:len(c.groups)-1]
	c.gradOwner = nil
	members := c.out.Shapes[start:]
	if len(members) < 2 {
		return
	}
	b := members[0].Bounds
	for _, m := range members[1:] {
		b = union(b, m.Bounds)
	}
	g := Shape{Kind: KindGroup, Bounds: b, Group: append([]Shape(nil), members...)}
	c.out.Shapes = append(c.out.Shapes[:start], g)
}

func union(a, b paint.Rect) paint.Rect {
	x0, y0 := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	x1, y1 := math.Max(a.X+a.W, b.X+b.W), math.Max(a.Y+a.H, b.Y+b.H)
	return paint.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// Glyphs implements paint.Canvas: the run becomes text, joined to the line
// being built when it continues it — the same baseline, a word's space
// further — so that a line of words is one editable text box.
func (c *Canvas) Glyphs(run *paint.GlyphRun) {
	if run == nil || len(run.Glyphs) == 0 || run.Size <= 0 || run.Font == nil {
		return
	}
	face := c.face(run.Font)
	var text []rune
	for _, g := range run.Glyphs {
		text = append(text, []rune(g.Text)...)
	}
	if len(text) == 0 {
		return
	}
	upem := float64(run.Font.UnitsPerEm())
	last := run.Glyphs[len(run.Glyphs)-1]
	end := run.X + last.X + float64(run.Font.GlyphAdvance(last.ID))*run.Size/upem
	ascent := float64(run.Font.Ascent()) * run.Size / upem
	descent := -float64(run.Font.Descent()) * run.Size / upem
	if descent < 0 {
		descent = -descent
	}
	// The letter-spacing is what the layout put between glyphs beyond their
	// advances; one value for the run, rounded to a hundredth of a pixel so
	// that runs of one style compare equal.
	spacing := 0.0
	if n := len(run.Glyphs); n > 1 {
		var extra float64
		for i := 0; i+1 < n; i++ {
			g := run.Glyphs[i]
			extra += run.Glyphs[i+1].X - g.X - float64(run.Font.GlyphAdvance(g.ID))*run.Size/upem
		}
		spacing = math.Round(extra/float64(n-1)*100) / 100
		if math.Abs(spacing) < 0.05 {
			spacing = 0
		}
		end += spacing
	}
	r := Run{Text: string(text), Face: face, Size: run.Size, Color: run.Color, Invisible: run.Invisible, Spacing: spacing, Link: c.linkAt(run.X, run.Y-ascent/2, end)}
	if r.Invisible {
		r.Color.A = 0
	}
	top, h := run.Y-ascent, ascent+descent

	if n := len(c.out.Shapes); n > 0 {
		prev := &c.out.Shapes[n-1]
		if prev.Kind == KindText {
			lastRun := &prev.Runs[len(prev.Runs)-1]
			baseline := prev.Bounds.Y + prev.ascent()
			gap := run.X - (prev.Bounds.X + prev.Bounds.W)
			if math.Abs(baseline-run.Y) < 0.5 && gap > -1 && gap < 2*run.Size {
				// The gap between two words is the space the layout left.
				if gap > run.Size/20 {
					lastRun.Text += " "
				}
				if lastRun.Face == r.Face && lastRun.Size == r.Size && lastRun.Color == r.Color && lastRun.Link == r.Link && lastRun.Invisible == r.Invisible && lastRun.Spacing == r.Spacing {
					lastRun.Text += r.Text
				} else {
					prev.Runs = append(prev.Runs, r)
				}
				x0 := prev.Bounds.X
				y0 := math.Min(prev.Bounds.Y, top)
				y1 := math.Max(prev.Bounds.Y+prev.Bounds.H, top+h)
				prev.Bounds = paint.Rect{X: x0, Y: y0, W: end - x0, H: y1 - y0}
				return
			}
		}
	}
	c.out.Shapes = append(c.out.Shapes, Shape{Kind: KindText, Bounds: paint.Rect{X: run.X, Y: top, W: end - run.X, H: h}, Runs: []Run{r}})
}

// ascent is the distance from a text shape's top to its baseline: the
// largest of its runs' ascents, since the runs of a line share one baseline
// and the shape's top is the highest of their tops. The first run's alone was
// short when a smaller run began the line (a list's marker, a callout's
// number before its note), which put the baseline above the text and refused
// the line's next word, starting a second box in the middle of the line.
func (s *Shape) ascent() float64 {
	var a float64
	for _, r := range s.Runs {
		a = math.Max(a, float64(r.Face.Font.Ascent())*r.Size/float64(r.Face.Font.UnitsPerEm()))
	}
	return a
}

// Baseline is a text shape's baseline, in device pixels from the page top.
func (s *Shape) Baseline() float64 { return s.Bounds.Y + s.ascent() }

// linkAt is the link whose area holds the run's middle, or "".
func (c *Canvas) linkAt(x0, y, x1 float64) string {
	p := image.Pt(int((x0+x1)/2), int(y))
	for _, l := range c.links {
		if p.In(l.Rect) {
			return l.Href
		}
	}
	return ""
}

func (c *Canvas) face(f *opentype.Font) *Face {
	if fc, ok := c.faces[f]; ok {
		return fc
	}
	family, sub := names(f)
	lsub, lfam := lower(sub), lower(family)
	// The style is the subfamily's word for it, or — for a font whose name
	// table calls every face Regular — its OS/2 weight and slant, unless the
	// family name already carries the weight ("Lato Black" is a family of
	// its own, not a bold Lato).
	weighted := false
	for _, w := range []string{"thin", "light", "medium", "semibold", "bold", "black", "heavy"} {
		if contains(lfam, w) {
			weighted = true
		}
	}
	fc := &Face{Font: f, Family: family, Embeddable: embeddable(f),
		Bold:   contains(lsub, "bold") || (!weighted && f.WeightClass() >= 600),
		Italic: contains(lsub, "italic") || contains(lsub, "oblique") || (!contains(lfam, "italic") && f.IsItalic())}
	if family == "" {
		fc.Family = "sans-serif"
	}
	c.faces[f] = fc
	return fc
}

// bounds is a path's bounding box (control points included, which for the
// rounded rectangles and ellipses the painter makes is the shape's box).
func bounds(p *paint.Path) (paint.Rect, bool) {
	if p == nil || len(p.Ops) == 0 {
		return paint.Rect{}, false
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, op := range p.Ops {
		n := 0
		switch op.Kind {
		case paint.PathMoveTo, paint.PathLineTo:
			n = 1
		case paint.PathCubeTo:
			n = 3
		}
		for _, pt := range op.P[:n] {
			x0, y0 = math.Min(x0, pt.X), math.Min(y0, pt.Y)
			x1, y1 = math.Max(x1, pt.X), math.Max(y1, pt.Y)
		}
	}
	if x1 < x0 || y1 < y0 {
		return paint.Rect{}, false
	}
	return paint.Rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}, true
}

// isRect reports whether p is exactly the rectangle b: one subpath of four
// axis-aligned lines.
func isRect(p *paint.Path, b paint.Rect) bool {
	if len(p.Ops) != 5 || p.Ops[0].Kind != paint.PathMoveTo || p.Ops[4].Kind != paint.PathClose {
		return false
	}
	for _, op := range p.Ops[:4] {
		if op.Kind != paint.PathMoveTo && op.Kind != paint.PathLineTo {
			return false
		}
		x, y := op.P[0].X, op.P[0].Y
		if (x != b.X && x != b.X+b.W) || (y != b.Y && y != b.Y+b.H) {
			return false
		}
	}
	return true
}

func covers(b, page paint.Rect) bool {
	return b.X <= page.X+0.5 && b.Y <= page.Y+0.5 && b.X+b.W >= page.X+page.W-0.5 && b.Y+b.H >= page.Y+page.H-0.5
}
