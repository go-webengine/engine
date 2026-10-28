// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"math"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
	"github.com/go-widgets/painter"
)

// VectorStats says what PaintVector drew as vectors and what it had to
// rasterise, so that a caller can tell a page that came out all vector from
// one that leaned on bitmaps.
type VectorStats struct {
	// Rasterized counts the pieces drawn as bitmaps because no vector form
	// exists for them here: a subtree under opacity < 1, a filter, a
	// mask-image or a rotation; a blurred box-shadow; a gradient whose
	// opacity fades between two stops; a form control; a background image
	// repeated more than maxVectorTiles times.
	Rasterized int
	// RasterPixels is their total area, in device pixels.
	RasterPixels int
	// Skipped counts pieces that needed a bitmap and got none, because the
	// page is larger than maxVectorScratchPixels.
	Skipped int
	// Images counts the bitmaps placed (<img>, background tiles), Glyphs the
	// glyphs drawn as text.
	Images, Glyphs int
}

// maxVectorTiles bounds how many times one background image is placed as a
// vector image. Beyond it — a 2-pixel pattern tiled over a whole page would be
// hundreds of thousands of placements — the layer is rasterised once instead.
const maxVectorTiles = 1024

// maxVectorScratchPixels bounds the page-sized buffer the raster fallback
// paints into (64 Mpx, 256 MiB). A page larger than that gets no fallback:
// the pieces that need one are left out and counted in VectorStats.Skipped.
const maxVectorScratchPixels = 64 << 20

// PaintVector draws the box tree through c: the same painting order, geometry
// and clipping as PaintFull, but backgrounds, borders and box shadows as
// paths, gradients as shadings, text as positioned glyphs of the very faces
// the layout measured with, and images as images placed at their layout box
// — so a PDF made from it has selectable text and edges that stay sharp at
// any zoom.
//
// page is the device-pixel rectangle of the page (the viewport): what
// cannot be drawn as vectors (see VectorStats.Rasterized) is painted by the
// raster painter into a buffer of that size, cropped to what it painted, and
// placed as an image at the same position — a per-element fallback, so one
// filtered icon does not turn the whole page into a bitmap.
//
// The page background (the canvas colour) is the caller's to paint first, as
// for PaintFull.
func PaintVector(c Canvas, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image, page image.Rectangle) VectorStats {
	v := &vpainter{c: c, f: f, imgs: imgs, bgImgs: bgImgs, page: page}
	v.sc, _ = c.(StructuredCanvas)
	v.box(box, page)
	return v.stats
}

type vpainter struct {
	c      Canvas
	sc     StructuredCanvas // c, when it keeps the structure (mark.go)
	f      *Fonts
	imgs   map[*dom.Node]image.Image
	bgImgs map[string]image.Image
	page   image.Rectangle
	stats  VectorStats

	// scratch is the raster fallback's page-sized buffer, allocated on first
	// use and cleared back to transparent after each use (only where it was
	// painted).
	scratch *image.RGBA
	spp     *painter.PixelPainter
}

// box is paintBox: a subtree the vector forms cannot express goes to the
// raster fallback whole, as the raster painter's own group pass would take it.
func (v *vpainter) box(box *layout.Box, clip image.Rectangle) {
	if box == nil || clip.Empty() {
		return
	}
	if st := box.Style; st != nil {
		if st.HasOpacity && st.Opacity <= 0 {
			return
		}
		group := (st.HasOpacity && st.Opacity < 1) || len(st.Filters) > 0 ||
			(st.MaskImage != "" && v.bgImgs[st.MaskImage] != nil) ||
			st.RotateDeg != 0 || len(st.BackdropFilters) > 0
		if group {
			v.raster(clip, func(dst *image.RGBA, pp *painter.PixelPainter) {
				paintBox(dst, pp, box, v.f, v.imgs, v.bgImgs, clip)
			})
			// The bitmap holds the subtree's text as pixels; lay the text
			// itself over it, unpainted, so that it can still be found and
			// copied. A rotated box's text is not where the layout put it,
			// so it gets none.
			if st.RotateDeg == 0 {
				v.ghostText(box, clip)
			}
			return
		}
	}
	v.content(box, clip)
}

// content is paintBoxContent.
func (v *vpainter) content(box *layout.Box, clip image.Rectangle) {
	if box.Style != nil && box.Style.HasClip && box.Position.OutOfFlow() {
		if nc := intersectClipRect(clip, box); nc != clip {
			if nc.Empty() {
				return
			}
			v.c.Save()
			defer v.c.Restore()
			v.clipRect(nc)
			clip = nc
		}
	}
	st := box.Style
	hidden := st != nil && st.Visibility != css.VisibilityVisible
	drawable := !hidden && st != nil && box.W > 0 && box.H > 0
	own := Rect{X: box.X, Y: box.Y, W: box.W, H: box.H}
	rad := 0.0
	if drawable {
		rad = radiusOf(st, box.W, box.H)
	}
	visible := drawable && overlaps(own, clip)
	// The box's own painting, shadows to borders, is decoration.
	v.mark(artifactMark)
	// 1. Outset box shadows, behind the box.
	if visible {
		for i := len(st.BoxShadows) - 1; i >= 0; i-- {
			if sh := st.BoxShadows[i]; !sh.Inset {
				v.dropShadow(box, sh, own, rad, clip)
			}
		}
	}
	// 2. Background colour.
	if visible && st.Background.A > 0 {
		var p Path
		p.RoundRect(own, rad)
		v.c.Fill(&p, st.Background, false)
	}
	// 3. Background-image layers, last listed first.
	if visible && len(st.BackgroundImages) > 0 {
		v.backgroundLayers(st, own, rad, clip)
	}
	// 4. Inset shadows, over the background.
	if visible {
		for i := len(st.BoxShadows) - 1; i >= 0; i-- {
			if sh := st.BoxShadows[i]; sh.Inset {
				v.insetShadow(box, sh, own, rad, clip)
			}
		}
	}
	// 5. Borders.
	if !hidden && st != nil && !box.Anonymous && box.W > 0 && box.H > 0 && overlaps(own, clip) {
		v.edges(st.Border, own, radiusOf(st, box.W, box.H), true, true)
	}
	inner := descendantClip(clip, box)
	if inner != clip {
		if inner.Empty() {
			return
		}
		v.c.Save()
		defer v.c.Restore()
		v.clipRect(inner)
	}
	// 5.5 List marker: the item's label.
	if !hidden && box.Marker != nil {
		v.mark(Mark{Node: box.Node, Label: true})
		v.marker(box.Marker, inner)
	}
	// 6. Inline content: decorations, then words and atoms.
	if !hidden {
		for _, line := range box.Lines {
			for i := range line.Inlines {
				v.fragment(&line.Inlines[i])
			}
			for _, it := range line.Items {
				v.item(it, inner)
			}
		}
	}
	// 7. Children.
	for _, ch := range box.Children {
		v.box(ch, inner)
	}
}

// overlaps reports whether r intersects the integer clip rectangle.
func overlaps(r Rect, clip image.Rectangle) bool {
	return r.X < float64(clip.Max.X) && r.X+r.W > float64(clip.Min.X) &&
		r.Y < float64(clip.Max.Y) && r.Y+r.H > float64(clip.Min.Y)
}

// radiusOf is styleRadius without the rounding to whole pixels: a vector
// corner can be as round as the style says.
func radiusOf(st *css.Style, w, h float64) float64 {
	if st == nil || st.BorderRadius.Auto {
		return 0
	}
	l := st.BorderRadius
	r := l.Px
	if l.IsPercent {
		r = l.Percent * math.Min(w, h)
	}
	return math.Max(r, 0)
}

func (v *vpainter) clipRect(r image.Rectangle) {
	var p Path
	p.RoundRect(rectOfImage(r), 0)
	v.c.Clip(&p, false)
}

// ---- backgrounds ----

func (v *vpainter) backgroundLayers(st *css.Style, own Rect, rad float64, clip image.Rectangle) {
	for i := len(st.BackgroundImages) - 1; i >= 0; i-- {
		layer := st.BackgroundImages[i]
		switch layer.Kind {
		case css.BgGradient:
			v.gradient(own, rad, layer.Grad, clip)
		case css.BgURL:
			if src := v.bgImgs[layer.URL]; src != nil {
				v.bgBitmap(st, own, rad, src, nthSize(st.BackgroundSize, i), nthPosition(st.BackgroundPosition, i), nthRepeat(st.BackgroundRepeat, i), clip)
			}
		}
	}
}

// gradient paints a gradient layer as shadings through the box's rounded
// clip. A shading has one constant opacity, so the stops are split into runs
// of equal alpha; runs meet only at a hard stop (two stops at one position),
// and a run whose alpha is 0 is not painted — which covers the usual
// "colour, then transparent from here on" corner of a theme. A gradient
// whose opacity changes gradually between two stops has no such split and is
// rasterised.
func (v *vpainter) gradient(own Rect, rad float64, g *css.Gradient, clip image.Rectangle) {
	if g == nil {
		return
	}
	geo := g.Sampler(own.W, own.H).Geometry()
	runs, ok := alphaRuns(geo.Stops)
	if !ok {
		bx := image.Rect(int(own.X), int(own.Y), int(own.X)+int(own.W), int(own.Y)+int(own.H))
		irad := int(rad + 0.5)
		v.raster(clip, func(dst *image.RGBA, _ *painter.PixelPainter) {
			paintGradient(dst, bx, irad, g, clip)
		})
		return
	}
	v.c.Save()
	defer v.c.Restore()
	var p Path
	p.RoundRect(own, rad)
	v.c.Clip(&p, false)
	for k, run := range runs {
		if run[0].Color.A == 0 {
			continue
		}
		first, last := k == 0, k == len(runs)-1
		lo, hi := run[0].Pos, run[len(run)-1].Pos
		stops := make([]GradientStop, 0, len(run))
		for _, s := range run {
			stops = append(stops, GradientStop{Pos: s.Pos, Color: s.Color})
		}
		if hi <= lo {
			// One position: a solid band reaching out to whichever end of
			// the gradient this run is at, or nothing at all in between.
			switch {
			case first && last:
				lo, hi = lo-1, lo+1
			case first:
				lo = hi - 1
			case last:
				hi = lo + 1
			default:
				continue
			}
			c := stops[len(stops)-1].Color
			if first {
				c = stops[0].Color
			}
			stops = []GradientStop{{0, c}, {1, c}}
		} else {
			for i := range stops {
				stops[i].Pos = (stops[i].Pos - lo) / (hi - lo)
			}
			stops = strictlyIncreasing(stops)
		}
		s := Shading{Radial: geo.Radial, Stops: stops, ExtendStart: first, ExtendEnd: last}
		if geo.Radial {
			s.CX, s.CY, s.RX, s.RY = own.X+geo.CX, own.Y+geo.CY, geo.RX, geo.RY
			s.R0, s.R1 = math.Max(lo, 0), hi
			if s.R1 <= 0 {
				continue
			}
		} else {
			ax, ay := geo.X1-geo.X0, geo.Y1-geo.Y0
			s.X0, s.Y0 = own.X+geo.X0+ax*lo, own.Y+geo.Y0+ay*lo
			s.X1, s.Y1 = own.X+geo.X0+ax*hi, own.Y+geo.Y0+ay*hi
		}
		v.c.Shade(s)
	}
}

// alphaRuns splits stops into maximal runs of equal alpha. It fails when two
// neighbouring stops of different alpha sit at different positions: the
// opacity then fades, which one constant-opacity shading cannot paint.
func alphaRuns(stops []css.GradientStop) ([][]css.GradientStop, bool) {
	if len(stops) == 0 {
		return nil, false
	}
	var runs [][]css.GradientStop
	start := 0
	for i := 1; i < len(stops); i++ {
		if stops[i].Color.A == stops[i-1].Color.A {
			continue
		}
		if stops[i].Pos != stops[i-1].Pos {
			return nil, false
		}
		runs = append(runs, stops[start:i])
		start = i
	}
	return append(runs, stops[start:]), true
}

// strictlyIncreasing nudges stops that share a position (a hard stop) apart
// by a millionth of the gradient, keeping them in [0,1].
func strictlyIncreasing(stops []GradientStop) []GradientStop {
	const eps = 1e-6
	for i := 1; i < len(stops); i++ {
		if stops[i].Pos <= stops[i-1].Pos {
			stops[i].Pos = stops[i-1].Pos + eps
		}
	}
	if last := stops[len(stops)-1].Pos; last > 1 {
		for i := range stops {
			stops[i].Pos /= last
		}
	}
	return stops
}

// bgBitmap places a background image's tiles as images, with the geometry
// paintBgBitmap uses but unrounded — each tile is the source bitmap itself,
// scaled by the placement, so a deck that uses one picture on many pages
// embeds it once.
func (v *vpainter) bgBitmap(st *css.Style, own Rect, rad float64, src image.Image, size css.BgSize, pos css.BgPosition, rep css.BgRepeat, clip image.Rectangle) {
	iw, ih := src.Bounds().Dx(), src.Bounds().Dy()
	if iw <= 0 || ih <= 0 {
		return
	}
	dw, dh := bgTileSize(size, float64(iw), float64(ih), own.W, own.H)
	if math.IsNaN(dw) || math.IsNaN(dh) || math.IsInf(dw, 0) || math.IsInf(dh, 0) {
		return
	}
	// The raster painter never draws a tile under one pixel.
	dw, dh = math.Max(dw, 1), math.Max(dh, 1)
	ox := resolvePos(pos.X, own.W-dw)
	oy := resolvePos(pos.Y, own.H-dh)
	iMin, iMax, jMin, jMax := 0, 0, 0, 0
	if rep == css.RepeatBoth || rep == css.RepeatX {
		iMin, iMax = int(math.Floor(-ox/dw)), int(math.Ceil((own.W-ox)/dw))
	}
	if rep == css.RepeatBoth || rep == css.RepeatY {
		jMin, jMax = int(math.Floor(-oy/dh)), int(math.Ceil((own.H-oy)/dh))
	}
	if tiles := float64(iMax-iMin+1) * float64(jMax-jMin+1); tiles > maxVectorTiles {
		bx := image.Rect(int(own.X), int(own.Y), int(own.X)+int(own.W), int(own.Y)+int(own.H))
		irad := int(rad + 0.5)
		v.raster(clip, func(dst *image.RGBA, _ *painter.PixelPainter) {
			paintBgBitmap(dst, bx, irad, src, size, pos, rep, clip, st)
		})
		return
	}
	v.c.Save()
	defer v.c.Restore()
	var p Path
	p.RoundRect(own, rad)
	v.c.Clip(&p, false)
	for j := jMin; j <= jMax; j++ {
		for i := iMin; i <= iMax; i++ {
			r := Rect{X: own.X + ox + float64(i)*dw, Y: own.Y + oy + float64(j)*dh, W: dw, H: dh}
			if !overlaps(r, clip) || r.X >= own.X+own.W || r.Y >= own.Y+own.H || r.X+r.W <= own.X || r.Y+r.H <= own.Y {
				continue
			}
			v.c.Image(src, r)
			v.stats.Images++
		}
	}
}

// ---- shadows ----

// dropShadow paints an outset box shadow. Without blur it is the shadow
// rectangle with the box's own shape cut out of it (as paintDropShadow
// excludes the box's footprint); a blurred one is rasterised.
func (v *vpainter) dropShadow(box *layout.Box, sh css.BoxShadow, own Rect, rad float64, clip image.Rectangle) {
	if sh.Color.A == 0 {
		return
	}
	if sh.Blur > 0 {
		irad := int(rad + 0.5)
		v.raster(clip, func(dst *image.RGBA, _ *painter.PixelPainter) {
			paintDropShadow(dst, box, sh, irad, clip)
		})
		return
	}
	shadow := Rect{X: own.X + sh.OffsetX - sh.Spread, Y: own.Y + sh.OffsetY - sh.Spread, W: own.W + 2*sh.Spread, H: own.H + 2*sh.Spread}
	if shadow.W <= 0 || shadow.H <= 0 {
		return
	}
	v.c.Save()
	defer v.c.Restore()
	var outside Path
	outside.RoundRect(rectOfImage(clip.Union(rectBounds(shadow)).Union(rectBounds(own))), 0)
	outside.RoundRect(own, rad)
	v.c.Clip(&outside, true)
	var p Path
	p.RoundRect(shadow, 0)
	v.c.Fill(&p, sh.Color, false)
}

// insetShadow paints an inset box shadow: inside the box, everything but the
// shifted, spread-reduced inner rectangle. A blurred one is rasterised.
func (v *vpainter) insetShadow(box *layout.Box, sh css.BoxShadow, own Rect, rad float64, clip image.Rectangle) {
	if sh.Color.A == 0 {
		return
	}
	if sh.Blur > 0 {
		irad := int(rad + 0.5)
		v.raster(clip, func(dst *image.RGBA, _ *painter.PixelPainter) {
			paintInsetShadow(dst, box, sh, irad, clip)
		})
		return
	}
	v.c.Save()
	defer v.c.Restore()
	var clipP Path
	clipP.RoundRect(own, rad)
	v.c.Clip(&clipP, false)
	inner := Rect{X: own.X + sh.Spread + sh.OffsetX, Y: own.Y + sh.Spread + sh.OffsetY, W: own.W - 2*sh.Spread, H: own.H - 2*sh.Spread}
	var p Path
	p.RoundRect(own, 0)
	if inner.W > 0 && inner.H > 0 {
		p.RoundRect(inner, 0)
	}
	v.c.Fill(&p, sh.Color, true)
}

// rectBounds rounds a Rect out to whole pixels.
func rectBounds(r Rect) image.Rectangle {
	return pixelRect(r.X, r.Y, r.X+r.W, r.Y+r.H)
}

// ---- borders, markers, fragments ----

// edges is paintEdges: a rounded uniform border is a ring between the outer
// rounded rectangle and the one inset by the border width; any other border
// is one rectangle per side.
func (v *vpainter) edges(bd css.Borders, r Rect, rad float64, left, right bool) {
	if rad > 0 && left && right && uniformBorder(bd) && paintsSide(bd.Top) {
		w := bd.Top.Width
		var p Path
		p.RoundRect(r, rad)
		in := Rect{X: r.X + w, Y: r.Y + w, W: r.W - 2*w, H: r.H - 2*w}
		if in.W > 0 && in.H > 0 {
			p.RoundRect(in, math.Max(math.Min(rad, math.Min(r.W, r.H)/2)-w, 0))
		}
		v.c.Fill(&p, bd.Top.Color, true)
		return
	}
	// Every side painted here has a width and lies on a box of some size
	// (paintsSide; the callers skip empty boxes).
	fill := func(x, y, w, h float64, c css.Color) {
		var p Path
		p.RoundRect(Rect{X: x, Y: y, W: w, H: h}, 0)
		v.c.Fill(&p, c, false)
	}
	if paintsSide(bd.Top) {
		fill(r.X, r.Y, r.W, bd.Top.Width, bd.Top.Color)
	}
	if paintsSide(bd.Bottom) {
		fill(r.X, r.Y+r.H-bd.Bottom.Width, r.W, bd.Bottom.Width, bd.Bottom.Color)
	}
	if left && paintsSide(bd.Left) {
		fill(r.X, r.Y, bd.Left.Width, r.H, bd.Left.Color)
	}
	if right && paintsSide(bd.Right) {
		fill(r.X+r.W-bd.Right.Width, r.Y, bd.Right.Width, r.H, bd.Right.Color)
	}
}

// marker is paintMarker: an ordinal is text, a disc a filled circle, a
// circle a one-pixel ring, a square a filled square.
func (v *vpainter) marker(m *layout.Marker, clip image.Rectangle) {
	if !m.IsGlyph() {
		// as item draws a word, keeping the caller's mark (the item's label)
		if m.Text != "" && m.Style != nil && m.Style.Visibility == css.VisibilityVisible {
			v.text(m.Style, m.Text, m.X, m.Y+m.Ascent, m.Style.Color, false)
		}
		return
	}
	r := Rect{X: m.X, Y: m.Y, W: m.W, H: m.H}
	if !overlaps(r, clip) || m.Style == nil {
		return
	}
	var p Path
	switch m.Type {
	case css.ListSquare:
		p.RoundRect(r, 0)
		v.c.Fill(&p, m.Style.Color, false)
	case css.ListCircle:
		p.Ellipse(r)
		if r.W > 2 && r.H > 2 {
			p.Ellipse(Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2})
		}
		v.c.Fill(&p, m.Style.Color, true)
	default:
		p.Ellipse(r)
		v.c.Fill(&p, m.Style.Color, false)
	}
}

// fragment is paintInlineFragment.
func (v *vpainter) fragment(fr *layout.InlineFragment) {
	st := fr.Style
	if st == nil || fr.W <= 0 || fr.H <= 0 {
		return
	}
	v.mark(artifactMark) // an inline element's background and borders
	r := Rect{X: fr.X, Y: fr.Y, W: fr.W, H: fr.H}
	rad := radiusOf(st, fr.W, fr.H)
	if st.Background.A > 0 {
		var p Path
		p.RoundRect(r, rad)
		v.c.Fill(&p, st.Background, false)
	}
	v.edges(st.Border, r, rad, fr.First, fr.Last)
}

// ---- inline items ----

// item is paintItem.
func (v *vpainter) item(it *layout.InlineItem, clip image.Rectangle) {
	if it.NestedBox != nil {
		v.box(it.NestedBox, clip)
		return
	}
	if it.Style != nil && it.Style.Visibility != css.VisibilityVisible {
		return
	}
	if it.Image != nil {
		v.mark(contentMark(it.Image, it.Style))
		v.image(it, clip)
		return
	}
	if it.FormControl != nil {
		v.raster(clip, func(dst *image.RGBA, pp *painter.PixelPainter) {
			paintFormControl(dst, pp, it, v.f, v.imgs, v.bgImgs, clip)
		})
		return
	}
	if it.Text == "" || it.Style == nil {
		return
	}
	v.mark(contentMark(it.Node, it.Style))
	v.text(it.Style, it.Text, it.X, it.Y+it.Ascent, it.Style.Color, false)
}

// ghostText draws the text of a subtree as invisible glyphs: what an element
// drawn as a bitmap still says (see GlyphRun.Invisible). It follows the
// subtree as content does — its visibility, its own clip — without painting
// anything.
func (v *vpainter) ghostText(box *layout.Box, clip image.Rectangle) {
	if box == nil || clip.Empty() || box.Style == nil {
		return
	}
	st := box.Style
	if st.HasOpacity && st.Opacity <= 0 {
		return
	}
	if st.HasClip && box.Position.OutOfFlow() {
		clip = intersectClipRect(clip, box)
	}
	inner := descendantClip(clip, box)
	if st.Visibility == css.VisibilityVisible {
		if m := box.Marker; m != nil && !m.IsGlyph() && m.Style != nil {
			v.mark(Mark{Node: box.Node, Label: true})
			v.text(m.Style, m.Text, m.X, m.Y+m.Ascent, m.Style.Color, true)
		}
	}
	for _, line := range box.Lines {
		for _, it := range line.Items {
			switch {
			case it.NestedBox != nil:
				v.ghostText(it.NestedBox, inner)
			case it.Text != "" && it.Style != nil && it.Style.Visibility == css.VisibilityVisible && it.Image == nil && it.FormControl == nil:
				if overlaps(Rect{X: it.X, Y: it.Y, W: math.Max(it.Width, 1), H: math.Max(it.LineHeight, 1)}, inner) {
					v.mark(contentMark(it.Node, it.Style))
					v.text(it.Style, it.Text, it.X, it.Y+it.Ascent, it.Style.Color, true)
				}
			}
		}
	}
	for _, ch := range box.Children {
		v.ghostText(ch, inner)
	}
}

// image places an <img>'s bitmap in its layout box, honouring object-fit as
// paintItem does; a filter is applied to the bitmap first.
func (v *vpainter) image(it *layout.InlineItem, clip image.Rectangle) {
	src, ok := v.imgs[it.Image]
	if !ok || src == nil {
		return
	}
	sb := src.Bounds()
	if sb.Dx() <= 0 || sb.Dy() <= 0 {
		return
	}
	w, h := it.Width, it.LineHeight
	if w <= 0 || h <= 0 {
		w, h = float64(sb.Dx()), float64(sb.Dy())
	}
	box := Rect{X: it.X, Y: it.Y, W: w, H: h}
	if !overlaps(box, clip) {
		return
	}
	if it.Style != nil && len(it.Style.Filters) > 0 {
		src = applyFilters(toRGBA(src), it.Style.Filters, it.Style.Color)
	}
	fit := css.ObjectFitFill
	if it.Style != nil {
		fit = it.Style.ObjectFit
	}
	if fit != css.ObjectFitFill && it.Width > 0 && it.LineHeight > 0 {
		tw, th := coverContain(float64(sb.Dx()), float64(sb.Dy()), w, h, fit == css.ObjectFitCover)
		v.c.Save()
		defer v.c.Restore()
		var p Path
		p.RoundRect(box, 0)
		v.c.Clip(&p, false)
		v.c.Image(src, Rect{X: it.X + (w-tw)/2, Y: it.Y + (h-th)/2, W: tw, H: th})
		v.stats.Images++
		return
	}
	v.c.Image(src, box)
	v.stats.Images++
}

// text is drawText with glyphs instead of masks: the runs Fonts.Runs splits
// the text into, each shaped with the face it is set in, every glyph placed
// at the exact advance the layout measured with (Measure) plus the
// letter-spacing, then the underline.
func (v *vpainter) text(st *css.Style, s string, x, baseline float64, col css.Color, invisible bool) {
	pen := x
	bold := st.FontWeight >= 600
	for _, run := range v.f.Runs(s, st.FontFamily, st.FontWeight, st.Italic) {
		font := v.f.font(styleKey{st.FontFamily, bold, st.Italic})
		if run.Fallback {
			font = v.f.fallbackFont(bold, st.Italic)
		}
		face := v.f.runFace(run, st.FontFamily, st.FontSize, st.FontWeight, st.Italic)
		upem := float64(font.UnitsPerEm())
		gids := face.Shape(run.Text, requiredLigatureFeature)
		runes := []rune(run.Text)
		aligned := len(gids) == len(runes)
		gr := &GlyphRun{Font: font, Size: st.FontSize, Color: col, X: pen, Y: baseline, Glyphs: make([]Glyph, 0, len(gids)), Invisible: invisible, Unselectable: st.UserSelectNone}
		for i, gid := range gids {
			if gid == 0 {
				continue // .notdef: drawn and measured as nothing (see drawText)
			}
			g := Glyph{ID: gid, X: pen - gr.X}
			switch {
			case aligned:
				g.Text = string(runes[i])
			case len(gr.Glyphs) == 0:
				g.Text = run.Text
			}
			gr.Glyphs = append(gr.Glyphs, g)
			pen += face.AdvanceIndexUnits(gid)*st.FontSize/upem + st.LetterSpacing
		}
		if len(gr.Glyphs) > 0 && (col.A > 0 || invisible) {
			v.c.Glyphs(gr)
			v.stats.Glyphs += len(gr.Glyphs)
		}
	}
	if st.Underline && pen > x && !invisible {
		thick := math.Max(float64(int(st.FontSize/14)), 1)
		var p Path
		p.RoundRect(Rect{X: x, Y: baseline + thick + 1, W: pen - x, H: thick}, 0)
		v.c.Fill(&p, col, false)
	}
}

// ---- raster fallback ----

// raster paints one piece with the raster painter into the scratch buffer,
// confined to clip, and places what it painted — cropped to its ink — as an
// image. The buffer holds the painter's own straight-alpha pixels, so the
// crop is handed over as an *image.NRGBA.
func (v *vpainter) raster(clip image.Rectangle, paint func(dst *image.RGBA, pp *painter.PixelPainter)) {
	clip = clip.Intersect(v.page)
	if clip.Empty() {
		return
	}
	if v.scratch == nil {
		if v.page.Min != (image.Point{}) || int64(v.page.Dx())*int64(v.page.Dy()) > maxVectorScratchPixels {
			v.stats.Skipped++
			return
		}
		v.scratch = image.NewRGBA(image.Rect(0, 0, v.page.Dx(), v.page.Dy()))
		v.spp = painter.NewPixelPainter(v.scratch.Pix, v.page.Dx(), v.page.Dy())
	}
	paint(v.scratch, v.spp)
	// The painter may have written anywhere in the buffer (a shadow or a
	// rotated box can reach beyond clip, and the buffer is the page): find
	// the ink over the whole of it, then keep only what clip shows.
	ink := inkBounds(v.scratch, v.scratch.Rect)
	keep := ink.Intersect(clip)
	if !keep.Empty() {
		// the element's look; its text is drawn again over it, as content
		v.mark(artifactMark)
		out := image.NewNRGBA(image.Rect(0, 0, keep.Dx(), keep.Dy()))
		for y := keep.Min.Y; y < keep.Max.Y; y++ {
			copy(out.Pix[(y-keep.Min.Y)*out.Stride:], v.scratch.Pix[v.scratch.PixOffset(keep.Min.X, y):v.scratch.PixOffset(keep.Max.X, y)])
		}
		v.c.Image(out, rectOfImage(keep))
		v.stats.Rasterized++
		v.stats.RasterPixels += keep.Dx() * keep.Dy()
	}
	for y := ink.Min.Y; y < ink.Max.Y; y++ {
		clear(v.scratch.Pix[v.scratch.PixOffset(ink.Min.X, y):v.scratch.PixOffset(ink.Max.X, y)])
	}
}

// inkBounds is the smallest rectangle of r holding every pixel of img whose
// alpha is not zero.
func inkBounds(img *image.RGBA, r image.Rectangle) image.Rectangle {
	x0, y0, x1, y1 := r.Max.X, r.Max.Y, r.Min.X, r.Min.Y
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := img.Pix[img.PixOffset(r.Min.X, y):img.PixOffset(r.Max.X, y)]
		for i := 3; i < len(row); i += 4 {
			if row[i] == 0 {
				continue
			}
			// Plain comparisons: the package has its own min, over a slice.
			x := r.Min.X + i/4
			if x < x0 {
				x0 = x
			}
			if x+1 > x1 {
				x1 = x + 1
			}
			if y < y0 {
				y0 = y
			}
			y1 = y + 1
		}
	}
	if x1 <= x0 || y1 <= y0 {
		return image.Rectangle{}
	}
	return image.Rect(x0, y0, x1, y1)
}
