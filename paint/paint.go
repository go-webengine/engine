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

// Paint draws the box tree onto dst. imgs supplies decoded bitmaps keyed by
// <img> node (already scaled to the layout size); a missing entry is skipped.
// The caller owns clearing dst to its base (e.g. white) beforehand.
func Paint(dst *image.RGBA, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image) {
	pp := painter.NewPixelPainter(dst.Pix, dst.Rect.Dx(), dst.Rect.Dy())
	paintBox(dst, pp, box, f, imgs)
}

func paintBox(dst *image.RGBA, pp *painter.PixelPainter, box *layout.Box, f *Fonts, imgs map[*dom.Node]image.Image) {
	if box == nil {
		return
	}
	if box.Style != nil && box.Style.Background.A > 0 && box.W > 0 && box.H > 0 {
		r := painter.Rect{X: int(box.X), Y: int(box.Y), W: int(box.W), H: int(box.H)}
		if rad := boxRadius(box); rad > 0 {
			pp.FillRoundRect(r, rad, toPainter(box.Style.Background))
		} else {
			pp.FillRect(r, toPainter(box.Style.Background))
		}
	}
	// Borders paint on real element boxes only (anonymous boxes carry the
	// parent's style but no border of their own).
	if box.Style != nil && !box.Anonymous && box.W > 0 && box.H > 0 {
		paintBorders(pp, box)
	}
	for _, line := range box.Lines {
		for _, it := range line.Items {
			paintItem(dst, it, f, imgs)
		}
	}
	for _, ch := range box.Children {
		paintBox(dst, pp, ch, f, imgs)
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
	fc := f.face(st.FontFamily, st.FontSize)
	baseline := int(it.Y + it.Ascent)
	penX := int(it.X)
	col := st.Color
	bold := st.FontWeight >= 600
	for _, r := range it.Text {
		bounds, mask, maskp, advance, ok := fc.GlyphMask(r, penX, baseline)
		if ok && mask != nil {
			blitMask(dst, bounds, mask, maskp, col)
			if bold {
				blitMask(dst, bounds.Add(image.Pt(1, 0)), mask, maskp, col)
			}
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

// blendPixel does src-over compositing of col at coverage cov (0..255).
func blendPixel(dst *image.RGBA, x, y int, col css.Color, cov uint8) {
	i := dst.PixOffset(x, y)
	af := float64(cov) / 255 * float64(col.A) / 255
	inv := 1 - af
	dst.Pix[i+0] = uint8(float64(col.R)*af + float64(dst.Pix[i+0])*inv + 0.5)
	dst.Pix[i+1] = uint8(float64(col.G)*af + float64(dst.Pix[i+1])*inv + 0.5)
	dst.Pix[i+2] = uint8(float64(col.B)*af + float64(dst.Pix[i+2])*inv + 0.5)
	dst.Pix[i+3] = 255
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
