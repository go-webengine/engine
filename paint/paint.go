// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"math"

	"github.com/go-gfx/gfx/raster"
	"github.com/go-gfx/gfx/resample"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
	"github.com/go-widgets/painter"
)

// Paint draws the box tree onto dst. imgs supplies decoded <img> bitmaps keyed
// by node (already scaled to the layout size); a missing entry is skipped. The
// caller owns clearing dst to its base (e.g. white) beforehand. It is equivalent
// to PaintFull with no background-image bitmaps.
func Paint(dst *image.RGBA, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image) {
	PaintFull(dst, box, f, imgs, nil)
}

// PaintFull draws the box tree onto dst, additionally painting CSS
// background-image layers (gradients and url() bitmaps). bgImgs supplies decoded
// background bitmaps keyed by their raw url() token; gradients need no bitmap.
func PaintFull(dst *image.RGBA, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image) {
	pp := painter.NewPixelPainter(dst.Pix, dst.Rect.Dx(), dst.Rect.Dy())
	// The initial clip is the whole image; every draw already stays within it, so
	// pages with no overflow container render byte-identically to before.
	paintBox(dst, pp, box, f, imgs, bgImgs, dst.Rect)
}

// paintBox paints one box, wrapping the real work in a group-opacity pass when
// the box has 0 < opacity < 1 (opacity 0 skips the subtree entirely). clip is
// the pixel rectangle painting is confined to (an ancestor's overflow clip);
// it is always a sub-rectangle of the image bounds.
func paintBox(dst *image.RGBA, pp *painter.PixelPainter, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image, clip image.Rectangle) {
	if box == nil || clip.Empty() {
		return
	}
	op := 1.0
	hasFilter := false
	if box.Style != nil {
		if box.Style.HasOpacity {
			op = box.Style.Opacity
			if op <= 0 {
				return // fully transparent: paint nothing
			}
		}
		hasFilter = len(box.Style.Filters) > 0
	}
	// A group pass (render to an offscreen buffer, then composite) is needed when
	// the box has a `filter` chain and/or a fractional opacity. Filters are
	// applied to the group's rendered output before it is composited, so blur /
	// drop-shadow spread and colour transforms see the whole subtree at once.
	if hasFilter || op < 1 {
		// The group buffer only needs to cover the rows this box's subtree can
		// actually paint into (its ink bounds, expanded for any blur/drop-shadow
		// spread), not the whole page: a page has a fixed, modest width but can be
		// tens of thousands of pixels tall, and a `filter`/fractional-opacity box is
		// typically a small icon or card. dst's PixelPainter still requires a
		// buffer starting at row 0 (it has no notion of a row origin), so the
		// PAINT step allocates [0, y1) — but the expensive per-pixel work (colour-
		// matrix filters, blur, composite) runs on a COPY of just [y0, y1),
		// re-anchored to row 0. The copy (not a zero-copy reslice) is deliberate:
		// go-images' AdjustContrast/GaussianBlur re-anchor any non-zero-origin
		// image.RGBA to (0,0) internally (see ToRGBA/newLike), which would silently
		// discard a shared reslice's row offset and paint the filtered result at
		// the top of the page. Re-anchoring ourselves, up front, and tracking the
		// offset separately through compositeGroupAt sidesteps that entirely.
		y0, y1 := groupRows(box, hasFilter, dst.Rect, clip)
		if y1 <= y0 {
			return // the box's ink bounds don't intersect the visible clip at all
		}
		tmp := image.NewRGBA(image.Rect(dst.Rect.Min.X, dst.Rect.Min.Y, dst.Rect.Max.X, y1))
		tpp := painter.NewPixelPainter(tmp.Pix, tmp.Rect.Dx(), tmp.Rect.Dy())
		paintBoxContent(tmp, tpp, box, f, imgs, bgImgs, clip)
		group := copyRows(tmp, y0, y1)
		if hasFilter {
			group = applyFilters(group, box.Style.Filters, box.Style.Color)
		}
		compositeGroupAt(dst, group, op, y0)
		return
	}
	paintBoxContent(dst, pp, box, f, imgs, bgImgs, clip)
}

// paintBoxContent paints a box's shadows, background, borders, inline content
// and children (recursing through paintBox so nested opacity groups compose).
// Everything this box paints is confined to clip; its own inline content and
// child boxes are additionally confined to descendantClip, which intersects clip
// with this box's padding box on any axis whose overflow is not visible.
func paintBoxContent(dst *image.RGBA, pp *painter.PixelPainter, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image, clip image.Rectangle) {
	drawable := box.Style != nil && box.W > 0 && box.H > 0
	rad := 0
	if drawable {
		rad = boxRadius(box)
	}
	// 1. Drop (outset) box-shadows paint behind the box.
	if drawable {
		for i := len(box.Style.BoxShadows) - 1; i >= 0; i-- {
			if sh := box.Style.BoxShadows[i]; !sh.Inset {
				paintDropShadow(dst, box, sh, rad, clip)
			}
		}
	}
	// 2. Solid background colour.
	if drawable && box.Style.Background.A > 0 {
		r := painter.Rect{X: int(box.X), Y: int(box.Y), W: int(box.W), H: int(box.H)}
		if rad > 0 {
			fillRoundRectClipped(dst, pp, r, rad, box.Style.Background, clip)
		} else {
			fillRectClipped(pp, r, box.Style.Background, clip)
		}
	}
	// 3. Background-image layers (last-listed painted first; first on top).
	if drawable && len(box.Style.BackgroundImages) > 0 {
		paintBackgroundLayers(dst, box, rad, bgImgs, clip)
	}
	// 4. Inset box-shadows paint over the background, under the border/content.
	if drawable {
		for i := len(box.Style.BoxShadows) - 1; i >= 0; i-- {
			if sh := box.Style.BoxShadows[i]; sh.Inset {
				paintInsetShadow(dst, box, sh, rad, clip)
			}
		}
	}
	// 5. Borders (real element boxes only; anonymous boxes carry no border).
	if box.Style != nil && !box.Anonymous && box.W > 0 && box.H > 0 {
		paintBorders(pp, box, clip)
	}
	// A box's own inline content and children are clipped to its padding box when
	// it establishes an overflow clip (overflow != visible on either axis). This
	// is what confines the sr-only / visually-hidden pattern's overflowing text
	// to its 1×1 box instead of painting it at full size.
	inner := descendantClip(clip, box)
	// 5.5 List-item marker (bullet or ordinal) in the indent left of the content.
	if box.Marker != nil {
		paintMarker(dst, pp, box.Marker, f, inner)
	}
	// 6. Inline content.
	for _, line := range box.Lines {
		for i, it := range line.Items {
			paintInlineBackground(pp, box, line, i, inner)
			paintItem(dst, it, f, imgs, inner)
		}
	}
	// 7. Children.
	for _, ch := range box.Children {
		paintBox(dst, pp, ch, f, imgs, bgImgs, inner)
	}
}

// clipsContent reports whether a box should clip its descendants' painting.
// It requires BOTH a non-visible overflow AND an author-set definite height.
//
// The height gate is a deliberate conservatism: the engine's block layout still
// under-sizes some auto-height containers (a collapsed float/flex row can come
// out 0-tall). Clipping to such a wrongly-tiny box would HIDE real article body
// text — a far worse defect than failing to hide some off-screen chrome. When
// the author set an explicit height, the box's size is author-determined (not an
// engine guess), so clipping to it is safe and matches the browser. The
// universal sr-only / visually-hidden pattern — the whole point of this clip —
// sets an explicit `height:1px`, so it is covered; an engine-collapsed
// auto-height container is not. Percentage heights are excluded because the
// engine resolves them as auto (no definite basis), so they are not trustworthy.
func clipsContent(box *layout.Box) bool {
	st := box.Style
	if st == nil {
		return false
	}
	if !st.OverflowX.Clips() && !st.OverflowY.Clips() {
		return false
	}
	return !st.Height.Auto && !st.Height.IsPercent
}

// descendantClip narrows clip to box's padding box on each axis whose overflow
// clips. A visible box (the overwhelmingly common case) returns clip unchanged,
// so non-overflow pages are unaffected.
func descendantClip(clip image.Rectangle, box *layout.Box) image.Rectangle {
	if !clipsContent(box) {
		return clip
	}
	cx, cy := box.Style.OverflowX.Clips(), box.Style.OverflowY.Clips()
	bw := box.Style.Border.Widths()
	pb := image.Rect(
		int(box.X+bw.Left), int(box.Y+bw.Top),
		int(box.X+box.W-bw.Right), int(box.Y+box.H-bw.Bottom),
	)
	out := clip
	if cx {
		if pb.Min.X > out.Min.X {
			out.Min.X = pb.Min.X
		}
		if pb.Max.X < out.Max.X {
			out.Max.X = pb.Max.X
		}
	}
	if cy {
		if pb.Min.Y > out.Min.Y {
			out.Min.Y = pb.Min.Y
		}
		if pb.Max.Y < out.Max.Y {
			out.Max.Y = pb.Max.Y
		}
	}
	if out.Max.X < out.Min.X {
		out.Max.X = out.Min.X
	}
	if out.Max.Y < out.Min.Y {
		out.Max.Y = out.Min.Y
	}
	return out
}

// fillRectClipped fills r∩clip with a solid colour. Intersecting the rect (not
// the pixels) is exact for an axis-aligned fill and keeps the common
// clip==image-bounds case identical to an unclipped fill.
func fillRectClipped(pp *painter.PixelPainter, r painter.Rect, c css.Color, clip image.Rectangle) {
	if r.W <= 0 || r.H <= 0 || c.A == 0 {
		return
	}
	ir := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H).Intersect(clip)
	if ir.Empty() {
		return
	}
	pp.FillRect(painter.Rect{X: ir.Min.X, Y: ir.Min.Y, W: ir.Dx(), H: ir.Dy()}, toPainter(c))
}

// fillRoundRectClipped fills a rounded rect confined to clip. When clip already
// contains the rect (the common case) the rounded fill is drawn as-is; when clip
// cuts it, the fill is masked per-pixel to clip so overflow content stays inside
// the clipping ancestor.
func fillRoundRectClipped(dst *image.RGBA, pp *painter.PixelPainter, r painter.Rect, rad int, c css.Color, clip image.Rectangle) {
	full := image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H)
	if clip.Intersect(full) == full {
		pp.FillRoundRect(r, rad, toPainter(c))
		return
	}
	ir := full.Intersect(clip)
	for y := ir.Min.Y; y < ir.Max.Y; y++ {
		for x := ir.Min.X; x < ir.Max.X; x++ {
			if insideRoundRect(x, y, full, rad) {
				blendPixel(dst, x, y, c, c.A)
			}
		}
	}
}

// boxRadius returns the used corner radius (in pixels) for a box, resolving a
// percentage against the box's smaller side. The painter clamps it to half the
// smaller side, so pill/circle radii (large px or 50%) render correctly.
func boxRadius(box *layout.Box) int {
	if box.Style == nil {
		return 0
	}
	l := box.Style.BorderRadius
	if l.Auto {
		return 0
	}
	var r float64
	if l.IsPercent {
		r = l.Percent * math.Min(box.W, box.H)
	} else {
		r = l.Px
	}
	if r <= 0 {
		return 0
	}
	return int(r + 0.5)
}

// paintBackgroundLayers paints the box's background-image layers into its border
// box, clipped to the rounded-rect shape.
func paintBackgroundLayers(dst *image.RGBA, box *layout.Box, rad int, bgImgs map[string]image.Image, clip image.Rectangle) {
	st := box.Style
	bx := rectOf(box)
	for i := len(st.BackgroundImages) - 1; i >= 0; i-- {
		layer := st.BackgroundImages[i]
		switch layer.Kind {
		case css.BgGradient:
			paintGradient(dst, bx, rad, layer.Grad, clip)
		case css.BgURL:
			src := bgImgs[layer.URL]
			if src == nil {
				continue
			}
			size := nthSize(st.BackgroundSize, i)
			pos := nthPosition(st.BackgroundPosition, i)
			rep := nthRepeat(st.BackgroundRepeat, i)
			paintBgBitmap(dst, bx, rad, src, size, pos, rep, clip, st)
		}
	}
}

// paintGradient fills the box rect with a gradient, clipped to the rounded rect
// and to clip (an ancestor's overflow clip).
func paintGradient(dst *image.RGBA, bx image.Rectangle, rad int, g *css.Gradient, clip image.Rectangle) {
	if g == nil {
		return
	}
	w, h := float64(bx.Dx()), float64(bx.Dy())
	s := g.Sampler(w, h)
	region := bx.Intersect(dst.Rect).Intersect(clip)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if !insideRoundRect(x, y, bx, rad) {
				continue
			}
			c := s.At(float64(x-bx.Min.X)+0.5, float64(y-bx.Min.Y)+0.5)
			if c.A == 0 {
				continue
			}
			blendPixel(dst, x, y, c, 255)
		}
	}
}

// paintBgBitmap paints a decoded background bitmap into the box rect honouring
// background-size, background-position and background-repeat, clipped to the
// rounded rect. The tile is resampled ONCE to its drawn pixel size with a
// high-quality filter (see scaleTile) and then stamped, so the image on screen
// is antialiased rather than the nearest-neighbour blocks a per-pixel sampler
// would produce.
func paintBgBitmap(dst *image.RGBA, bx image.Rectangle, rad int, src image.Image, size css.BgSize, pos css.BgPosition, rep css.BgRepeat, clip image.Rectangle, st *css.Style) {
	iw, ih := src.Bounds().Dx(), src.Bounds().Dy()
	if iw <= 0 || ih <= 0 {
		return
	}
	bw, bh := float64(bx.Dx()), float64(bx.Dy())
	dw, dh := bgTileSize(size, float64(iw), float64(ih), bw, bh)
	tw, th := int(math.Round(dw)), int(math.Round(dh))
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	// Resample the source to the drawn tile size once, up front (bicubic by
	// default; nearest for image-rendering:pixelated). Every stamp below is then
	// a plain 1:1 copy — the resampling quality lives here, not in the blitter.
	tile := scaleTile(src, tw, th, bgMode(st))
	// Position: percentage aligns the image's p% point to the box's p% point.
	ox := resolvePos(pos.X, bw-float64(tw))
	oy := resolvePos(pos.Y, bh-float64(th))
	// Repeat: compute the tile index range covering the box.
	iMin, iMax := 0, 0
	jMin, jMax := 0, 0
	if rep == css.RepeatBoth || rep == css.RepeatX {
		iMin = int(math.Floor((0 - ox) / float64(tw)))
		iMax = int(math.Ceil((bw - ox) / float64(tw)))
	}
	if rep == css.RepeatBoth || rep == css.RepeatY {
		jMin = int(math.Floor((0 - oy) / float64(th)))
		jMax = int(math.Ceil((bh - oy) / float64(th)))
	}
	for j := jMin; j <= jMax; j++ {
		for i := iMin; i <= iMax; i++ {
			tileX := int(math.Round(float64(bx.Min.X) + ox + float64(i*tw)))
			tileY := int(math.Round(float64(bx.Min.Y) + oy + float64(j*th)))
			blitTileClipped(dst, tile, tileX, tileY, bx, rad, clip)
		}
	}
}

// bgMode maps a computed style's image-rendering to a resampling filter for
// background images: bicubic (smooth) by default, nearest for pixelated.
func bgMode(st *css.Style) resample.Mode {
	if st != nil && st.ImageRendering == css.IRPixelated {
		return resample.Nearest
	}
	return resample.Bicubic
}

// scaleTile returns src resampled to w×h using mode, in premultiplied-alpha
// space so a transparent pixel's colour does not bleed into a cut-out's edge.
// It returns src unchanged when it is already w×h (the common repeat/auto case),
// and falls back to src if go-gfx rejects the size (defensive: w,h are >= 1).
func scaleTile(src image.Image, w, h int, mode resample.Mode) image.Image {
	if src.Bounds().Dx() == w && src.Bounds().Dy() == h {
		return src
	}
	out, err := resample.ResizePremultiplied(raster.FromImage(src), w, h, mode)
	if err != nil {
		return src
	}
	return out.ToNRGBA()
}

// bgTileSize computes the drawn tile size for a background image.
func bgTileSize(size css.BgSize, iw, ih, bw, bh float64) (float64, float64) {
	switch size.Kind {
	case css.SizeCover:
		return coverContain(iw, ih, bw, bh, true)
	case css.SizeContain:
		return coverContain(iw, ih, bw, bh, false)
	case css.SizeExplicit:
		return explicitSize(size, iw, ih, bw, bh)
	default: // auto: intrinsic size
		return iw, ih
	}
}

// coverContain scales (iw,ih) to cover (true) or contain (false) the box.
func coverContain(iw, ih, bw, bh float64, cover bool) (float64, float64) {
	sx, sy := bw/iw, bh/ih
	var s float64
	if cover {
		s = math.Max(sx, sy)
	} else {
		s = math.Min(sx, sy)
	}
	return iw * s, ih * s
}

// explicitSize resolves an explicit background-size (lengths/percents, auto
// keeps the aspect ratio of the definite axis).
func explicitSize(size css.BgSize, iw, ih, bw, bh float64) (float64, float64) {
	wAuto, hAuto := size.W.Auto, size.H.Auto
	switch {
	case wAuto && hAuto:
		return iw, ih
	case hAuto:
		w := size.W.Resolve(bw)
		return w, ih * (w / iw)
	case wAuto:
		h := size.H.Resolve(bh)
		return iw * (h / ih), h
	default:
		return size.W.Resolve(bw), size.H.Resolve(bh)
	}
}

// resolvePos resolves a background-position component against the free space
// (box size minus tile size): a percentage p maps to p*free; px is literal.
func resolvePos(l css.Length, free float64) float64 {
	if l.IsPercent {
		return l.Percent * free
	}
	return l.Px
}

// blitTileClipped stamps an already-scaled tile at integer offset (dx,dy) with a
// 1:1 pixel copy, clipping each pixel to the rounded box rect (and to clip and
// the destination bounds). A source pixel with zero alpha is skipped; others are
// blended straight over the destination at their own alpha as coverage.
func blitTileClipped(dst *image.RGBA, tile image.Image, dx, dy int, bx image.Rectangle, rad int, clip image.Rectangle) {
	b := tile.Bounds()
	region := image.Rect(dx, dy, dx+b.Dx(), dy+b.Dy()).Intersect(bx).Intersect(dst.Rect).Intersect(clip)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if !insideRoundRect(x, y, bx, rad) {
				continue
			}
			r, g, bl, a := tile.At(b.Min.X+(x-dx), b.Min.Y+(y-dy)).RGBA()
			cov := uint8(a >> 8)
			if cov == 0 {
				continue
			}
			blendPixel(dst, x, y, css.Color{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: 255}, cov)
		}
	}
}

// ---- box-shadow ----

// paintDropShadow paints an outset box-shadow: a soft rect the size of the
// border box, expanded by spread and offset, Gaussian-blurred by the blur
// radius (approximated with an exact erf box; rounded corners are ignored).
func paintDropShadow(dst *image.RGBA, box *layout.Box, sh css.BoxShadow, rad int, clip image.Rectangle) {
	if sh.Color.A == 0 {
		return
	}
	x0 := box.X + sh.OffsetX - sh.Spread
	y0 := box.Y + sh.OffsetY - sh.Spread
	x1 := box.X + box.W + sh.OffsetX + sh.Spread
	y1 := box.Y + box.H + sh.OffsetY + sh.Spread
	sigma := sh.Blur / 2
	pad := int(math.Ceil(sigma*3)) + 1
	area := image.Rect(int(x0)-pad, int(y0)-pad, int(x1)+pad+1, int(y1)+pad+1).Intersect(dst.Rect).Intersect(clip)
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			cov := erfBoxCoverage(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, sigma)
			if cov <= 0 {
				continue
			}
			blendPixel(dst, x, y, sh.Color, uint8(cov*float64(sh.Color.A)/255*255+0.5))
		}
	}
}

// paintInsetShadow paints an inset box-shadow: a soft dark band inside the box
// edges (the complement of a blurred inner rect), clipped to the box.
func paintInsetShadow(dst *image.RGBA, box *layout.Box, sh css.BoxShadow, rad int, clip image.Rectangle) {
	if sh.Color.A == 0 {
		return
	}
	bx := rectOf(box)
	x0 := box.X + sh.Spread + sh.OffsetX
	y0 := box.Y + sh.Spread + sh.OffsetY
	x1 := box.X + box.W - sh.Spread + sh.OffsetX
	y1 := box.Y + box.H - sh.Spread + sh.OffsetY
	sigma := sh.Blur / 2
	region := bx.Intersect(dst.Rect).Intersect(clip)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if !insideRoundRect(x, y, bx, rad) {
				continue
			}
			inner := erfBoxCoverage(float64(x)+0.5, float64(y)+0.5, x0, y0, x1, y1, sigma)
			cov := 1 - inner
			if cov <= 0 {
				continue
			}
			blendPixel(dst, x, y, sh.Color, uint8(cov*float64(sh.Color.A)/255*255+0.5))
		}
	}
}

// erfBoxCoverage returns the coverage in [0,1] at point (px,py) of a box
// [x0,x1]×[y0,y1] blurred by a Gaussian of standard deviation sigma. With
// sigma<=0 it is a hard inside test.
func erfBoxCoverage(px, py, x0, y0, x1, y1, sigma float64) float64 {
	if sigma <= 0 {
		if px >= x0 && px <= x1 && py >= y0 && py <= y1 {
			return 1
		}
		return 0
	}
	k := 1 / (sigma * math.Sqrt2)
	// cx,cy are each in [0,1] (erf is monotonic and x1>=x0, y1>=y0, and erf
	// saturates to ±1), so their product is already in [0,1].
	cx := 0.5 * (math.Erf((x1-px)*k) - math.Erf((x0-px)*k))
	cy := 0.5 * (math.Erf((y1-py)*k) - math.Erf((y0-py)*k))
	return cx * cy
}

// ---- geometry helpers ----

func rectOf(box *layout.Box) image.Rectangle {
	return image.Rect(int(box.X), int(box.Y), int(box.X)+int(box.W), int(box.Y)+int(box.H))
}

// insideRoundRect reports whether the pixel centre (x,y) lies within rect r with
// corner radius rad (rad<=0 is a plain rectangle test).
func insideRoundRect(x, y int, r image.Rectangle, rad int) bool {
	fx, fy := float64(x)+0.5, float64(y)+0.5
	if fx < float64(r.Min.X) || fx >= float64(r.Max.X) || fy < float64(r.Min.Y) || fy >= float64(r.Max.Y) {
		return false
	}
	if rad <= 0 {
		return true
	}
	rf := float64(rad)
	// Clamp radius to half the smaller side (matches the painter).
	half := math.Min(float64(r.Dx()), float64(r.Dy())) / 2
	if rf > half {
		rf = half
	}
	// Corner circle centres.
	lx := float64(r.Min.X) + rf
	rx := float64(r.Max.X) - rf
	ty := float64(r.Min.Y) + rf
	by := float64(r.Max.Y) - rf
	var dx, dy float64
	switch {
	case fx < lx:
		dx = lx - fx
	case fx > rx:
		dx = fx - rx
	}
	switch {
	case fy < ty:
		dy = ty - fy
	case fy > by:
		dy = fy - by
	}
	if dx == 0 || dy == 0 {
		return true
	}
	return dx*dx+dy*dy <= rf*rf
}

// paintBorders draws the four border edges of a box. When the box has a corner
// radius and a single uniform visible border (same width/style/colour on all
// four sides), it is stroked as one rounded rectangle; otherwise each edge is
// drawn as a straight solid rectangle.
func paintBorders(pp *painter.PixelPainter, box *layout.Box, clip image.Rectangle) {
	bd := box.Style.Border
	x, y := int(box.X), int(box.Y)
	w, h := int(box.W), int(box.H)
	if rad := boxRadius(box); rad > 0 && uniformBorder(bd) && paintsSide(bd.Top) {
		// A rounded uniform border is stroked as one rounded rect; skip it only
		// when it lies entirely outside the clip (thin strokes are not per-pixel
		// masked — an ancestor rarely clips a rounded-border box mid-edge).
		full := image.Rect(x, y, x+w, y+h)
		if !clip.Intersect(full).Empty() {
			pp.StrokeRoundRect(painter.Rect{X: x, Y: y, W: w, H: h}, rad,
				toPainter(bd.Top.Color), iround(bd.Top.Width))
		}
		return
	}
	fill := func(rx, ry, rw, rh int, c css.Color) {
		fillRectClipped(pp, painter.Rect{X: rx, Y: ry, W: rw, H: rh}, c, clip)
	}
	if paintsSide(bd.Top) {
		fill(x, y, w, iround(bd.Top.Width), bd.Top.Color)
	}
	if paintsSide(bd.Bottom) {
		bw := iround(bd.Bottom.Width)
		fill(x, y+h-bw, w, bw, bd.Bottom.Color)
	}
	if paintsSide(bd.Left) {
		fill(x, y, iround(bd.Left.Width), h, bd.Left.Color)
	}
	if paintsSide(bd.Right) {
		bw := iround(bd.Right.Width)
		fill(x+w-bw, y, bw, h, bd.Right.Color)
	}
}

func paintsSide(s css.BorderSide) bool {
	return s.Width > 0 && s.Style != css.BorderNone && s.Color.A > 0
}

// uniformBorder reports whether all four edges are identical (width, style and
// colour), so a rounded box can be stroked as a single rounded rectangle.
func uniformBorder(b css.Borders) bool {
	return b.Top == b.Right && b.Right == b.Bottom && b.Bottom == b.Left
}

func iround(f float64) int { return int(f + 0.5) }

// paintMarker draws a list-item marker. A decimal marker reuses the inline text
// painter (the ordinal string in the item's own face and colour); the bullet
// types map to painter primitives — a disc is a full-radius filled round rect, a
// hollow circle a stroked round rect, and a square a plain filled rect.
func paintMarker(dst *image.RGBA, pp *painter.PixelPainter, m *layout.Marker, f *Fonts, clip image.Rectangle) {
	if m.Type == css.ListDecimal {
		paintItem(dst, &layout.InlineItem{Text: m.Text, Style: m.Style, X: m.X, Y: m.Y, Ascent: m.Ascent}, f, nil, clip)
		return
	}
	r := markerRect(m)
	if image.Rect(r.X, r.Y, r.X+r.W, r.Y+r.H).Intersect(clip).Empty() {
		return
	}
	col := toPainter(m.Style.Color)
	switch m.Type {
	case css.ListSquare:
		fillRectClipped(pp, r, m.Style.Color, clip)
	case css.ListCircle:
		pp.StrokeRoundRect(r, r.W/2, col, 1)
	default: // css.ListDisc
		pp.FillRoundRect(r, r.W/2, col)
	}
}

// markerRect is a bullet marker's integer bounding rectangle in device pixels.
func markerRect(m *layout.Marker) painter.Rect {
	return painter.Rect{
		X: int(math.Round(m.X)),
		Y: int(math.Round(m.Y)),
		W: int(math.Round(m.W)),
		H: int(math.Round(m.H)),
	}
}

// paintInlineBackground fills the solid background colour of an inline-level
// element behind one of its inline items (a word). A block box paints its own
// background in step 2, but an inline element — a <span style="background:…">
// or a display:inline-block "pill" — owns no block box of its own, so without
// this its background never paints; any light text the author set against that
// background (the very common white-text-on-a-coloured-label pattern) then lands
// as light-on-white and vanishes entirely.
//
// The guard it.Style != box.Style skips the block's OWN direct text, which
// carries the block's Style pointer and whose background step 2 has already
// painted — so only genuine inline-descendant backgrounds are drawn here and a
// plain block with a background is never double-painted. When the previous item
// on the line comes from the same originating element, the space between the two
// words is internal to that element, so it is covered too and a multi-word
// inline background paints as one continuous band.
func paintInlineBackground(pp *painter.PixelPainter, box *layout.Box, line *layout.LineBox, i int, clip image.Rectangle) {
	it := line.Items[i]
	if it.Style == nil || it.Style.Background.A == 0 || it.Image != nil || it.LineBreak {
		return
	}
	if box.Style != nil && it.Style == box.Style {
		return // direct text of this block: its background is the box's own
	}
	left := int(it.X)
	if i > 0 {
		if prev := line.Items[i-1]; prev.Node != nil && prev.Node == it.Node {
			left = int(it.X - it.SpaceBefore) // internal space: extend the band left
		}
	}
	right := int(it.X + it.Width)
	r := painter.Rect{X: left, Y: int(it.Y), W: right - left, H: int(it.LineHeight)}
	fillRectClipped(pp, r, it.Style.Background, clip)
}

func paintItem(dst *image.RGBA, it *layout.InlineItem, f *Fonts, imgs map[*dom.Node]image.Image, clip image.Rectangle) {
	if it.Image != nil {
		if src, ok := imgs[it.Image]; ok {
			blitImage(dst, src, int(it.X), int(it.Y), clip)
		}
		return
	}
	if it.Text == "" || it.Style == nil {
		return
	}
	st := it.Style
	fc := f.styleFace(st.FontFamily, st.FontSize, st.FontWeight, st.Italic)
	baseline := int(it.Y + it.Ascent)
	penX := int(it.X)
	col := st.Color
	for _, r := range it.Text {
		bounds, mask, maskp, advance, ok := fc.GlyphMask(r, penX, baseline)
		if ok && mask != nil {
			blitMask(dst, bounds, mask, maskp, col, clip)
		}
		penX += advance
	}
}

// blitMask composites an 8-bit coverage mask in colour col onto dst, confined to
// clip (an ancestor's overflow clip).
func blitMask(dst *image.RGBA, bounds image.Rectangle, mask *image.Alpha, maskp image.Point, col css.Color, clip image.Rectangle) {
	region := bounds.Intersect(dst.Rect).Intersect(clip)
	for y := region.Min.Y; y < region.Max.Y; y++ {
		my := maskp.Y + (y - bounds.Min.Y)
		for x := region.Min.X; x < region.Max.X; x++ {
			mx := maskp.X + (x - bounds.Min.X)
			a := mask.AlphaAt(mx, my).A
			if a == 0 {
				continue
			}
			blendPixel(dst, x, y, col, a)
		}
	}
}

// blendPixel does src-over compositing of col at coverage cov (0..255) over the
// existing pixel, preserving/accumulating alpha so it is correct on both an
// opaque canvas (alpha stays 255) and a transparent group buffer.
func blendPixel(dst *image.RGBA, x, y int, col css.Color, cov uint8) {
	sa := float64(cov) / 255 * float64(col.A) / 255
	if sa <= 0 {
		return
	}
	i := dst.PixOffset(x, y)
	da := float64(dst.Pix[i+3]) / 255
	// sa>0 here (guarded above), so outA = sa + da*(1-sa) > 0 always.
	outA := sa + da*(1-sa)
	inv := da * (1 - sa)
	dst.Pix[i+0] = uint8((float64(col.R)*sa+float64(dst.Pix[i+0])*inv)/outA + 0.5)
	dst.Pix[i+1] = uint8((float64(col.G)*sa+float64(dst.Pix[i+1])*inv)/outA + 0.5)
	dst.Pix[i+2] = uint8((float64(col.B)*sa+float64(dst.Pix[i+2])*inv)/outA + 0.5)
	dst.Pix[i+3] = uint8(outA*255 + 0.5)
}

// compositeGroupAt composites a transparent, zero-origin group buffer src over
// dst at row offset yOffset (src's row 0 lands on dst row yOffset), scaling the
// group's alpha by the group opacity (0..1). The offset is threaded through
// explicitly rather than carried in src.Rect.Min because src has already been
// re-anchored to (0,0) by copyRows (see the comment on that function).
func compositeGroupAt(dst, src *image.RGBA, opacity float64, yOffset int) {
	cov := uint8(opacity*255 + 0.5)
	for y := src.Rect.Min.Y; y < src.Rect.Max.Y; y++ {
		for x := src.Rect.Min.X; x < src.Rect.Max.X; x++ {
			i := src.PixOffset(x, y)
			a := src.Pix[i+3]
			if a == 0 {
				continue
			}
			blendPixel(dst, x, y+yOffset, css.Color{R: src.Pix[i+0], G: src.Pix[i+1], B: src.Pix[i+2], A: a}, cov)
		}
	}
}

// groupRows returns the row range [y0, y1) a filter/opacity group buffer for
// box actually needs to cover: box's own ink bounds (its subtree's painted
// extent, which can exceed its border box — a shrink-wrapped float, a negative
// margin, an outset box-shadow), expanded for any blur/drop-shadow spread in
// its filter chain, intersected with the ancestor clip and the canvas. Only
// rows are bounded (not columns): a page is typically a fixed, modest width but
// can be tens of thousands of pixels tall, so that is where the win is: X is
// left at the canvas's full width since narrowing it would buy little for the
// extra bookkeeping.
func groupRows(box *layout.Box, hasFilter bool, canvas, clip image.Rectangle) (y0, y1 int) {
	ext := subtreeExtent(box)
	if hasFilter {
		if m := filterMargin(box.Style.Filters); m > 0 {
			ext.Min.Y -= int(m)
			ext.Max.Y += int(m)
		}
	}
	rows := image.Rect(canvas.Min.X, ext.Min.Y, canvas.Max.X, ext.Max.Y).Intersect(clip).Intersect(canvas)
	return rows.Min.Y, rows.Max.Y
}

// subtreeExtent returns the smallest axis-aligned rectangle (in absolute
// document pixels) covering everything box's paintBoxContent recursion can
// draw: the box's own border box plus any outset box-shadow spread, its list
// marker, its line boxes, and — recursively — every child. A block box's own
// W/H is not always a safe upper bound on its own (a shrink-wrapped
// float/negative margin can place a descendant outside it — see the comment on
// clipsContent), so this walks the real subtree rather than trusting box.H.
func subtreeExtent(box *layout.Box) image.Rectangle {
	if box == nil {
		return image.Rectangle{}
	}
	r := shadowExpandedRect(box)
	if box.Marker != nil {
		m := box.Marker
		r = r.Union(pixelRect(m.X, m.Y, m.X+m.W, m.Y+m.H))
	}
	for _, line := range box.Lines {
		r = r.Union(pixelRect(line.X, line.Y, line.X+line.W, line.Y+line.H))
	}
	for _, ch := range box.Children {
		r = r.Union(subtreeExtent(ch))
	}
	return r
}

// shadowExpandedRect is box's own border box, expanded by the spread of any
// outset (non-inset) box-shadow it carries — the same geometry paintDropShadow
// paints into.
func shadowExpandedRect(box *layout.Box) image.Rectangle {
	x0, y0 := box.X, box.Y
	x1, y1 := box.X+box.W, box.Y+box.H
	if box.Style != nil {
		for _, sh := range box.Style.BoxShadows {
			if sh.Inset {
				continue
			}
			sigma := sh.Blur / 2
			pad := math.Ceil(sigma*3) + 1
			x0 = math.Min(x0, box.X+sh.OffsetX-sh.Spread-pad)
			y0 = math.Min(y0, box.Y+sh.OffsetY-sh.Spread-pad)
			x1 = math.Max(x1, box.X+box.W+sh.OffsetX+sh.Spread+pad)
			y1 = math.Max(y1, box.Y+box.H+sh.OffsetY+sh.Spread+pad)
		}
	}
	return pixelRect(x0, y0, x1, y1)
}

// pixelRect converts a document-space rect to an integer pixel rect, rounding
// outward so a fractional edge is never clipped.
func pixelRect(x0, y0, x1, y1 float64) image.Rectangle {
	return image.Rect(int(math.Floor(x0)), int(math.Floor(y0)), int(math.Ceil(x1)), int(math.Ceil(y1)))
}

// filterMargin returns the pixel margin a filter chain's spatial effects
// (blur, drop-shadow) can spread beyond the element's own bounds — the same
// 3-sigma-plus-one convention paintDropShadow uses for CSS box-shadow. Purely
// pointwise filters (brightness, contrast, …) contribute nothing.
func filterMargin(filters []css.Filter) float64 {
	var m float64
	for _, f := range filters {
		var v float64
		switch f.Kind {
		case css.FilterBlur:
			v = math.Ceil(f.Amount*3) + 1
		case css.FilterDropShadow:
			sigma := f.Blur / 2
			v = math.Abs(f.OffsetX) + math.Abs(f.OffsetY) + math.Ceil(sigma*3) + 1
		}
		if v > m {
			m = v
		}
	}
	return m
}

// copyRows returns a fresh, zero-origin image.RGBA holding a COPY of img's row
// range [y0, y1) (full width). It deliberately copies rather than reslicing
// img's backing array in place: a reslice would keep img's non-zero Rect.Min,
// and go-images' AdjustContrast/GaussianBlur (used by the CSS contrast and
// blur/drop-shadow filters) re-anchor any non-zero-origin image.RGBA to (0,0)
// internally (ToRGBA/newLike) before processing it — silently discarding a
// reslice's row offset and making the filtered result land at the top of the
// page instead of at the box's real position. Returning an explicitly
// re-anchored copy up front, with the caller tracking y0 separately (see
// compositeGroupAt), avoids relying on every current and future pixel
// primitive to preserve a rect it has no reason to.
func copyRows(img *image.RGBA, y0, y1 int) *image.RGBA {
	if y0 < img.Rect.Min.Y {
		y0 = img.Rect.Min.Y
	}
	if y1 > img.Rect.Max.Y {
		y1 = img.Rect.Max.Y
	}
	w := img.Rect.Dx()
	if y1 <= y0 {
		return image.NewRGBA(image.Rect(0, 0, w, 0))
	}
	out := image.NewRGBA(image.Rect(0, 0, w, y1-y0))
	lo := (y0 - img.Rect.Min.Y) * img.Stride
	hi := (y1 - img.Rect.Min.Y) * img.Stride
	copy(out.Pix, img.Pix[lo:hi])
	return out
}

// blitImage draws src onto dst with its top-left at (dx, dy), confined to clip
// (an ancestor's overflow clip; always within the image bounds).
func blitImage(dst *image.RGBA, src image.Image, dx, dy int, clip image.Rectangle) {
	b := src.Bounds()
	for sy := b.Min.Y; sy < b.Max.Y; sy++ {
		ty := dy + (sy - b.Min.Y)
		if ty < clip.Min.Y || ty >= clip.Max.Y {
			continue
		}
		for sx := b.Min.X; sx < b.Max.X; sx++ {
			tx := dx + (sx - b.Min.X)
			if tx < clip.Min.X || tx >= clip.Max.X {
				continue
			}
			r, g, bl, a := src.At(sx, sy).RGBA()
			cov := uint8(a >> 8)
			blendPixel(dst, tx, ty, css.Color{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: 255}, cov)
		}
	}
}

func toPainter(c css.Color) painter.RGBA {
	return painter.RGBA{R: c.R, G: c.G, B: c.B, A: c.A}
}

// nthSize returns the size for layer i (repeating the last, default auto).
func nthSize(list []css.BgSize, i int) css.BgSize {
	if len(list) == 0 {
		return css.BgSize{Kind: css.SizeAuto}
	}
	if i >= len(list) {
		i = len(list) - 1
	}
	return list[i]
}

// nthPosition returns the position for layer i (default top-left 0%,0%).
func nthPosition(list []css.BgPosition, i int) css.BgPosition {
	if len(list) == 0 {
		return css.BgPosition{X: css.Length{Percent: 0, IsPercent: true}, Y: css.Length{Percent: 0, IsPercent: true}}
	}
	if i >= len(list) {
		i = len(list) - 1
	}
	return list[i]
}

// nthRepeat returns the repeat for layer i (default repeat).
func nthRepeat(list []css.BgRepeat, i int) css.BgRepeat {
	if len(list) == 0 {
		return css.RepeatBoth
	}
	if i >= len(list) {
		i = len(list) - 1
	}
	return list[i]
}
