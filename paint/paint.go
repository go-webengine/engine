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
	paintBox(dst, pp, box, f, imgs, bgImgs)
}

// paintBox paints one box, wrapping the real work in a group-opacity pass when
// the box has 0 < opacity < 1 (opacity 0 skips the subtree entirely).
func paintBox(dst *image.RGBA, pp *painter.PixelPainter, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image) {
	if box == nil {
		return
	}
	if box.Style != nil && box.Style.HasOpacity {
		op := box.Style.Opacity
		if op <= 0 {
			return // fully transparent: paint nothing
		}
		if op < 1 {
			tmp := image.NewRGBA(dst.Rect)
			tpp := painter.NewPixelPainter(tmp.Pix, tmp.Rect.Dx(), tmp.Rect.Dy())
			paintBoxContent(tmp, tpp, box, f, imgs, bgImgs)
			compositeGroup(dst, tmp, op)
			return
		}
	}
	paintBoxContent(dst, pp, box, f, imgs, bgImgs)
}

// paintBoxContent paints a box's shadows, background, borders, inline content
// and children (recursing through paintBox so nested opacity groups compose).
func paintBoxContent(dst *image.RGBA, pp *painter.PixelPainter, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image, bgImgs map[string]image.Image) {
	drawable := box.Style != nil && box.W > 0 && box.H > 0
	rad := 0
	if drawable {
		rad = boxRadius(box)
	}
	// 1. Drop (outset) box-shadows paint behind the box.
	if drawable {
		for i := len(box.Style.BoxShadows) - 1; i >= 0; i-- {
			if sh := box.Style.BoxShadows[i]; !sh.Inset {
				paintDropShadow(dst, box, sh, rad)
			}
		}
	}
	// 2. Solid background colour.
	if drawable && box.Style.Background.A > 0 {
		r := painter.Rect{X: int(box.X), Y: int(box.Y), W: int(box.W), H: int(box.H)}
		if rad > 0 {
			pp.FillRoundRect(r, rad, toPainter(box.Style.Background))
		} else {
			pp.FillRect(r, toPainter(box.Style.Background))
		}
	}
	// 3. Background-image layers (last-listed painted first; first on top).
	if drawable && len(box.Style.BackgroundImages) > 0 {
		paintBackgroundLayers(dst, box, rad, bgImgs)
	}
	// 4. Inset box-shadows paint over the background, under the border/content.
	if drawable {
		for i := len(box.Style.BoxShadows) - 1; i >= 0; i-- {
			if sh := box.Style.BoxShadows[i]; sh.Inset {
				paintInsetShadow(dst, box, sh, rad)
			}
		}
	}
	// 5. Borders (real element boxes only; anonymous boxes carry no border).
	if box.Style != nil && !box.Anonymous && box.W > 0 && box.H > 0 {
		paintBorders(pp, box)
	}
	// 6. Inline content.
	for _, line := range box.Lines {
		for _, it := range line.Items {
			paintItem(dst, it, f, imgs)
		}
	}
	// 7. Children.
	for _, ch := range box.Children {
		paintBox(dst, pp, ch, f, imgs, bgImgs)
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
func paintBackgroundLayers(dst *image.RGBA, box *layout.Box, rad int, bgImgs map[string]image.Image) {
	st := box.Style
	bx := rectOf(box)
	for i := len(st.BackgroundImages) - 1; i >= 0; i-- {
		layer := st.BackgroundImages[i]
		switch layer.Kind {
		case css.BgGradient:
			paintGradient(dst, bx, rad, layer.Grad)
		case css.BgURL:
			src := bgImgs[layer.URL]
			if src == nil {
				continue
			}
			size := nthSize(st.BackgroundSize, i)
			pos := nthPosition(st.BackgroundPosition, i)
			rep := nthRepeat(st.BackgroundRepeat, i)
			paintBgBitmap(dst, bx, rad, src, size, pos, rep)
		}
	}
}

// paintGradient fills the box rect with a gradient, clipped to the rounded rect.
func paintGradient(dst *image.RGBA, bx image.Rectangle, rad int, g *css.Gradient) {
	if g == nil {
		return
	}
	w, h := float64(bx.Dx()), float64(bx.Dy())
	s := g.Sampler(w, h)
	clip := bx.Intersect(dst.Rect)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		for x := clip.Min.X; x < clip.Max.X; x++ {
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
// rounded rect.
func paintBgBitmap(dst *image.RGBA, bx image.Rectangle, rad int, src image.Image, size css.BgSize, pos css.BgPosition, rep css.BgRepeat) {
	iw, ih := src.Bounds().Dx(), src.Bounds().Dy()
	if iw <= 0 || ih <= 0 {
		return
	}
	bw, bh := float64(bx.Dx()), float64(bx.Dy())
	dw, dh := bgTileSize(size, float64(iw), float64(ih), bw, bh)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	// Position: percentage aligns the image's p% point to the box's p% point.
	ox := resolvePos(pos.X, bw-dw)
	oy := resolvePos(pos.Y, bh-dh)
	// Repeat: compute the tile index range covering the box.
	iMin, iMax := 0, 0
	jMin, jMax := 0, 0
	if rep == css.RepeatBoth || rep == css.RepeatX {
		iMin = int(math.Floor((0 - ox) / dw))
		iMax = int(math.Ceil((bw - ox) / dw))
	}
	if rep == css.RepeatBoth || rep == css.RepeatY {
		jMin = int(math.Floor((0 - oy) / dh))
		jMax = int(math.Ceil((bh - oy) / dh))
	}
	for j := jMin; j <= jMax; j++ {
		for i := iMin; i <= iMax; i++ {
			tileX := float64(bx.Min.X) + ox + float64(i)*dw
			tileY := float64(bx.Min.Y) + oy + float64(j)*dh
			blitScaledClipped(dst, src, tileX, tileY, dw, dh, bx, rad)
		}
	}
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

// blitScaledClipped draws src scaled to dw×dh at (dx,dy), sampling nearest, and
// clips each pixel to the rounded box rect.
func blitScaledClipped(dst *image.RGBA, src image.Image, dx, dy, dw, dh float64, bx image.Rectangle, rad int) {
	b := src.Bounds()
	iw, ih := b.Dx(), b.Dy()
	x0 := int(math.Floor(dx))
	y0 := int(math.Floor(dy))
	x1 := int(math.Ceil(dx + dw))
	y1 := int(math.Ceil(dy + dh))
	clip := image.Rect(x0, y0, x1, y1).Intersect(bx).Intersect(dst.Rect)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		for x := clip.Min.X; x < clip.Max.X; x++ {
			if !insideRoundRect(x, y, bx, rad) {
				continue
			}
			// Map dst pixel back to source pixel (nearest).
			u := (float64(x) + 0.5 - dx) / dw * float64(iw)
			v := (float64(y) + 0.5 - dy) / dh * float64(ih)
			sx := b.Min.X + int(u)
			sy := b.Min.Y + int(v)
			if sx < b.Min.X || sx >= b.Max.X || sy < b.Min.Y || sy >= b.Max.Y {
				continue
			}
			r, g, bl, a := src.At(sx, sy).RGBA()
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
func paintDropShadow(dst *image.RGBA, box *layout.Box, sh css.BoxShadow, rad int) {
	if sh.Color.A == 0 {
		return
	}
	x0 := box.X + sh.OffsetX - sh.Spread
	y0 := box.Y + sh.OffsetY - sh.Spread
	x1 := box.X + box.W + sh.OffsetX + sh.Spread
	y1 := box.Y + box.H + sh.OffsetY + sh.Spread
	sigma := sh.Blur / 2
	pad := int(math.Ceil(sigma*3)) + 1
	area := image.Rect(int(x0)-pad, int(y0)-pad, int(x1)+pad+1, int(y1)+pad+1).Intersect(dst.Rect)
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
func paintInsetShadow(dst *image.RGBA, box *layout.Box, sh css.BoxShadow, rad int) {
	if sh.Color.A == 0 {
		return
	}
	bx := rectOf(box)
	x0 := box.X + sh.Spread + sh.OffsetX
	y0 := box.Y + sh.Spread + sh.OffsetY
	x1 := box.X + box.W - sh.Spread + sh.OffsetX
	y1 := box.Y + box.H - sh.Spread + sh.OffsetY
	sigma := sh.Blur / 2
	clip := bx.Intersect(dst.Rect)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		for x := clip.Min.X; x < clip.Max.X; x++ {
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
func paintBorders(pp *painter.PixelPainter, box *layout.Box) {
	bd := box.Style.Border
	x, y := int(box.X), int(box.Y)
	w, h := int(box.W), int(box.H)
	if rad := boxRadius(box); rad > 0 && uniformBorder(bd) && paintsSide(bd.Top) {
		pp.StrokeRoundRect(painter.Rect{X: x, Y: y, W: w, H: h}, rad,
			toPainter(bd.Top.Color), iround(bd.Top.Width))
		return
	}
	fill := func(rx, ry, rw, rh int, c css.Color) {
		if rw <= 0 || rh <= 0 || c.A == 0 {
			return
		}
		pp.FillRect(painter.Rect{X: rx, Y: ry, W: rw, H: rh}, toPainter(c))
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

func paintItem(dst *image.RGBA, it *layout.InlineItem, f *Fonts, imgs map[*dom.Node]image.Image) {
	if it.Image != nil {
		if src, ok := imgs[it.Image]; ok {
			blitImage(dst, src, int(it.X), int(it.Y))
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
			blitMask(dst, bounds, mask, maskp, col)
		}
		penX += advance
	}
}

// blitMask composites an 8-bit coverage mask in colour col onto dst.
func blitMask(dst *image.RGBA, bounds image.Rectangle, mask *image.Alpha, maskp image.Point, col css.Color) {
	clip := bounds.Intersect(dst.Rect)
	for y := clip.Min.Y; y < clip.Max.Y; y++ {
		my := maskp.Y + (y - bounds.Min.Y)
		for x := clip.Min.X; x < clip.Max.X; x++ {
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

// compositeGroup composites a transparent group buffer src over dst, scaling the
// group's alpha by the group opacity (0..1).
func compositeGroup(dst, src *image.RGBA, opacity float64) {
	cov := uint8(opacity*255 + 0.5)
	for y := src.Rect.Min.Y; y < src.Rect.Max.Y; y++ {
		for x := src.Rect.Min.X; x < src.Rect.Max.X; x++ {
			i := src.PixOffset(x, y)
			a := src.Pix[i+3]
			if a == 0 {
				continue
			}
			blendPixel(dst, x, y, css.Color{R: src.Pix[i+0], G: src.Pix[i+1], B: src.Pix[i+2], A: a}, cov)
		}
	}
}

// blitImage draws src onto dst with its top-left at (dx, dy), clipped.
func blitImage(dst *image.RGBA, src image.Image, dx, dy int) {
	b := src.Bounds()
	for sy := b.Min.Y; sy < b.Max.Y; sy++ {
		ty := dy + (sy - b.Min.Y)
		if ty < dst.Rect.Min.Y || ty >= dst.Rect.Max.Y {
			continue
		}
		for sx := b.Min.X; sx < b.Max.X; sx++ {
			tx := dx + (sx - b.Min.X)
			if tx < dst.Rect.Min.X || tx >= dst.Rect.Max.X {
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
