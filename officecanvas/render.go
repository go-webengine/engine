// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package officecanvas

import (
	"image"
	"image/color"
	"math"

	"golang.org/x/image/vector"

	"github.com/go-webengine/engine/paint"
)

// maxRenderPixels bounds a gradient drawn as a picture (16 Mpx, 64 MiB).
const maxRenderPixels = 16 << 20

// Render draws a gradient shape as a picture of its Bounds, scale pixels per
// device pixel: the shadings it was made from, through its outline,
// antialiased, transparent outside. It is for a format whose gradients cannot
// say what this one says (more than two stops, varying opacity). It returns
// nil for another kind of shape, or one past 16 Mpx.
func (s *Shape) Render(scale float64) *image.NRGBA {
	if s.Kind != KindGradient || s.Gradient == nil || !(scale > 0) {
		return nil
	}
	w, h := int(math.Ceil(s.Bounds.W*scale)), int(math.Ceil(s.Bounds.H*scale))
	if w <= 0 || h <= 0 || int64(w)*int64(h) > maxRenderPixels {
		return nil
	}
	// Coverage of the outline, from the path mapped to the picture.
	z := vector.NewRasterizer(w, h)
	mx := func(x float64) float32 { return float32((x - s.Bounds.X) * scale) }
	my := func(y float64) float32 { return float32((y - s.Bounds.Y) * scale) }
	for _, op := range s.Path.Ops {
		switch op.Kind {
		case paint.PathMoveTo:
			z.MoveTo(mx(op.P[0].X), my(op.P[0].Y))
		case paint.PathLineTo:
			z.LineTo(mx(op.P[0].X), my(op.P[0].Y))
		case paint.PathCubeTo:
			z.CubeTo(mx(op.P[0].X), my(op.P[0].Y), mx(op.P[1].X), my(op.P[1].Y), mx(op.P[2].X), my(op.P[2].Y))
		case paint.PathClose:
			z.ClosePath()
		}
	}
	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cov := mask.AlphaAt(x, y).A
			if cov == 0 {
				continue
			}
			px := s.Bounds.X + (float64(x)+0.5)/scale
			py := s.Bounds.Y + (float64(y)+0.5)/scale
			var r, g, b, a float64 // premultiplied, 0..1, composited over the shadings in order
			for _, sh := range s.Gradient.Shadings {
				c, ok := sample(sh, px, py)
				if !ok {
					continue
				}
				ca := float64(c.A) / 255
				r = float64(c.R)/255*ca + r*(1-ca)
				g = float64(c.G)/255*ca + g*(1-ca)
				b = float64(c.B)/255*ca + b*(1-ca)
				a = ca + a*(1-ca)
			}
			if a <= 0 {
				continue
			}
			a8 := a * float64(cov) / 255
			out.SetNRGBA(x, y, color.NRGBA{R: uint8(r/a*255 + 0.5), G: uint8(g/a*255 + 0.5), B: uint8(b/a*255 + 0.5), A: uint8(a8*255 + 0.5)})
		}
	}
	return out
}

// sample is a shading's colour at (x,y), and whether it paints there.
func sample(s paint.Shading, x, y float64) (cssColor, bool) {
	var t float64
	if s.Radial {
		if s.RX <= 0 || s.RY <= 0 || s.R1 == s.R0 {
			return cssColor{}, false
		}
		d := math.Hypot((x-s.CX)/s.RX, (y-s.CY)/s.RY)
		t = (d - s.R0) / (s.R1 - s.R0)
	} else {
		ax, ay := s.X1-s.X0, s.Y1-s.Y0
		l2 := ax*ax + ay*ay
		if l2 == 0 {
			return cssColor{}, false
		}
		t = ((x-s.X0)*ax + (y-s.Y0)*ay) / l2
	}
	if (t < 0 && !s.ExtendStart) || (t > 1 && !s.ExtendEnd) {
		return cssColor{}, false
	}
	t = clamp01(t)
	stops := s.Stops
	if t <= stops[0].Pos {
		c := stops[0].Color
		return cssColor{c.R, c.G, c.B, c.A}, true
	}
	for i := 0; i+1 < len(stops); i++ {
		a, b := stops[i], stops[i+1]
		if t <= b.Pos {
			f := 0.0
			if b.Pos > a.Pos {
				f = (t - a.Pos) / (b.Pos - a.Pos)
			}
			l := func(u, v uint8) uint8 { return uint8(float64(u) + (float64(v)-float64(u))*f + 0.5) }
			return cssColor{l(a.Color.R, b.Color.R), l(a.Color.G, b.Color.G), l(a.Color.B, b.Color.B), a.Color.A}, true
		}
	}
	c := stops[len(stops)-1].Color
	return cssColor{c.R, c.G, c.B, c.A}, true
}

type cssColor struct{ R, G, B, A uint8 }
