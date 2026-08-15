// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"math"

	gfxcolor "github.com/go-gfx/gfx/color"
	"github.com/go-images/images"

	"github.com/go-webengine/engine/css"
)

// applyFilters applies a CSS `filter` function chain to a group buffer (the
// element's rendered output on transparent, straight-alpha RGBA) in order,
// returning the filtered buffer. curColor is the element's used `color`,
// substituted for a drop-shadow whose colour is currentColor.
//
// The heavy primitives are REUSED, not reimplemented: the Gaussian blur is
// go-images' separable GaussianBlur (blur/drop-shadow), contrast is go-images'
// AdjustContrast, and premultiplied-alpha handling is go-gfx's colour helpers.
// The component-transfer / colour-matrix filters (brightness, saturate,
// grayscale, sepia, hue-rotate, invert) are the CSS Filter Effects spec
// matrices — go-images has no equivalent (its Grayscale/Invert use different,
// full-strength-only coefficients), so they are computed from the spec constants.
func applyFilters(src *image.RGBA, filters []css.Filter, curColor css.Color) *image.RGBA {
	img := src
	for _, f := range filters {
		switch f.Kind {
		case css.FilterBlur:
			img = blurFilter(img, f.Amount)
		case css.FilterContrast:
			// go-images AdjustContrast scales each channel about the 128 mid-point
			// by the factor — exactly CSS contrast(<amount>).
			img = images.AdjustContrast(img, f.Amount)
		case css.FilterBrightness:
			img = applyMatrix(img, brightnessMatrix(f.Amount))
		case css.FilterSaturate:
			img = applyMatrix(img, saturateMatrix(f.Amount, 0.213, 0.715, 0.072))
		case css.FilterGrayscale:
			img = applyMatrix(img, saturateMatrix(1-f.Amount, 0.2126, 0.7152, 0.0722))
		case css.FilterSepia:
			img = applyMatrix(img, sepiaMatrix(f.Amount))
		case css.FilterHueRotate:
			img = applyMatrix(img, hueRotateMatrix(f.Amount))
		case css.FilterInvert:
			img = applyMatrix(img, invertMatrix(f.Amount))
		case css.FilterDropShadow:
			img = dropShadowFilter(img, f, curColor)
		}
	}
	return img
}

// colorMatrix is a 3x4 colour transform: three rows of [cr cg cb offset]. The
// colour channels are computed as cr*R + cg*G + cb*B + offset*255 (offsets are
// in the 0..1 range, matching the spec), clamped to [0,255]; alpha is preserved.
type colorMatrix [3][4]float64

// applyMatrix applies m to every pixel of src, returning a new buffer.
func applyMatrix(src *image.RGBA, m colorMatrix) *image.RGBA {
	dst := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		r := float64(src.Pix[i])
		g := float64(src.Pix[i+1])
		b := float64(src.Pix[i+2])
		dst.Pix[i] = clamp8(m[0][0]*r + m[0][1]*g + m[0][2]*b + m[0][3]*255)
		dst.Pix[i+1] = clamp8(m[1][0]*r + m[1][1]*g + m[1][2]*b + m[1][3]*255)
		dst.Pix[i+2] = clamp8(m[2][0]*r + m[2][1]*g + m[2][2]*b + m[2][3]*255)
		dst.Pix[i+3] = src.Pix[i+3]
	}
	return dst
}

// brightnessMatrix scales each channel by a (CSS brightness is a multiply, so a
// negative delta / additive AdjustBrightness would be wrong here).
func brightnessMatrix(a float64) colorMatrix {
	return colorMatrix{
		{a, 0, 0, 0},
		{0, a, 0, 0},
		{0, 0, a, 0},
	}
}

// invertMatrix interpolates towards the negative by a: out = (1-2a)*in + a.
func invertMatrix(a float64) colorMatrix {
	d := 1 - 2*a
	return colorMatrix{
		{d, 0, 0, a},
		{0, d, 0, a},
		{0, 0, d, a},
	}
}

// saturateMatrix is the CSS/SVG saturation matrix for factor s with luminance
// coefficients (lr,lg,lb). saturate() uses (0.213,0.715,0.072); grayscale(a) is
// this matrix at s = 1-a with (0.2126,0.7152,0.0722).
func saturateMatrix(s, lr, lg, lb float64) colorMatrix {
	return colorMatrix{
		{lr + s*(1-lr), lg - s*lg, lb - s*lb, 0},
		{lr - s*lr, lg + s*(1-lg), lb - s*lb, 0},
		{lr - s*lr, lg - s*lg, lb + s*(1-lb), 0},
	}
}

// sepiaMatrix is the CSS sepia() matrix at amount a (a=1 is full sepia).
func sepiaMatrix(a float64) colorMatrix {
	t := 1 - a
	return colorMatrix{
		{0.393 + 0.607*t, 0.769 - 0.769*t, 0.189 - 0.189*t, 0},
		{0.349 - 0.349*t, 0.686 + 0.314*t, 0.168 - 0.168*t, 0},
		{0.272 - 0.272*t, 0.534 - 0.534*t, 0.131 + 0.869*t, 0},
	}
}

// hueRotateMatrix is the CSS hue-rotate() matrix for an angle of rad radians.
func hueRotateMatrix(rad float64) colorMatrix {
	c := math.Cos(rad)
	s := math.Sin(rad)
	return colorMatrix{
		{0.213 + c*0.787 - s*0.213, 0.715 - c*0.715 - s*0.715, 0.072 - c*0.072 + s*0.928, 0},
		{0.213 - c*0.213 + s*0.143, 0.715 + c*0.285 + s*0.140, 0.072 - c*0.072 - s*0.283, 0},
		{0.213 - c*0.213 - s*0.787, 0.715 - c*0.715 + s*0.715, 0.072 + c*0.928 + s*0.072, 0},
	}
}

// blurFilter applies a Gaussian blur of standard deviation sigma to a
// straight-alpha buffer, blurring in PREMULTIPLIED space so transparent edges do
// not leak dark fringes. The premultiplied colour and the alpha are each blurred
// with the same separable Gaussian (go-images GaussianBlur carries alpha through
// unchanged, so the alpha plane is blurred as a separate grayscale pass), then
// the result is divided back out. sigma <= 0 is a no-op.
func blurFilter(src *image.RGBA, sigma float64) *image.RGBA {
	if sigma <= 0 {
		return src
	}
	// Premultiplied colour, blurred (alpha rides along unchanged, discarded).
	pm := image.NewRGBA(src.Rect)
	copy(pm.Pix, gfxcolor.Premultiply(src.Pix))
	bColor, _ := images.GaussianBlur(pm, sigma) // err only for sigma<=0, guarded above
	// Alpha plane as grayscale, blurred with the same kernel.
	alpha := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		a := src.Pix[i+3]
		alpha.Pix[i], alpha.Pix[i+1], alpha.Pix[i+2], alpha.Pix[i+3] = a, a, a, 255
	}
	bAlpha, _ := images.GaussianBlur(alpha, sigma)
	// Recombine premultiplied colour with blurred alpha, then unpremultiply.
	out := image.NewRGBA(src.Rect)
	for i := 0; i < len(out.Pix); i += 4 {
		out.Pix[i] = bColor.Pix[i]
		out.Pix[i+1] = bColor.Pix[i+1]
		out.Pix[i+2] = bColor.Pix[i+2]
		out.Pix[i+3] = bAlpha.Pix[i]
	}
	gfxcolor.UnpremultiplyInPlace(out.Pix)
	return out
}

// dropShadowFilter paints a blurred, offset, coloured copy of the element's
// alpha behind the element. The shadow's shape is the element's alpha channel
// blurred by a Gaussian (standard deviation = blur/2, matching the box-shadow
// convention), tinted by the shadow colour, offset by (OffsetX,OffsetY); the
// original element is composited on top (source-over).
func dropShadowFilter(src *image.RGBA, f css.Filter, curColor css.Color) *image.RGBA {
	col := f.Color
	if f.UseCurrentColor {
		col = curColor
	}
	// Blurred alpha mask of the element.
	mask := image.NewRGBA(src.Rect)
	for i := 0; i < len(src.Pix); i += 4 {
		a := src.Pix[i+3]
		mask.Pix[i], mask.Pix[i+1], mask.Pix[i+2], mask.Pix[i+3] = a, a, a, 255
	}
	if sigma := f.Blur / 2; sigma > 0 {
		mask, _ = images.GaussianBlur(mask, sigma)
	}
	ox := int(math.Round(f.OffsetX))
	oy := int(math.Round(f.OffsetY))
	w, h := src.Rect.Dx(), src.Rect.Dy()
	out := image.NewRGBA(src.Rect)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			di := out.PixOffset(src.Rect.Min.X+x, src.Rect.Min.Y+y)
			// Shadow contribution: the mask sampled at the pre-offset position.
			var sr, sg, sb, sa float64
			sx, sy := x-ox, y-oy
			if sx >= 0 && sx < w && sy >= 0 && sy < h {
				mi := mask.PixOffset(src.Rect.Min.X+sx, src.Rect.Min.Y+sy)
				ma := float64(mask.Pix[mi]) / 255 * float64(col.A) / 255
				sr, sg, sb, sa = float64(col.R)/255, float64(col.G)/255, float64(col.B)/255, ma
			}
			// Element (source) over the shadow.
			er := float64(src.Pix[di]) / 255
			eg := float64(src.Pix[di+1]) / 255
			eb := float64(src.Pix[di+2]) / 255
			ea := float64(src.Pix[di+3]) / 255
			or, og, ob, oa := srcOver(er, eg, eb, ea, sr, sg, sb, sa)
			out.Pix[di] = clamp8(or * 255)
			out.Pix[di+1] = clamp8(og * 255)
			out.Pix[di+2] = clamp8(ob * 255)
			out.Pix[di+3] = clamp8(oa * 255)
		}
	}
	return out
}

// srcOver composites straight-alpha colour (fr,fg,fb,fa) over (br,bg,bb,ba),
// returning straight-alpha out. All components are in [0,1].
func srcOver(fr, fg, fb, fa, br, bg, bb, ba float64) (r, g, b, a float64) {
	oa := fa + ba*(1-fa)
	if oa <= 0 {
		return 0, 0, 0, 0
	}
	r = (fr*fa + br*ba*(1-fa)) / oa
	g = (fg*fa + bg*ba*(1-fa)) / oa
	b = (fb*fa + bb*ba*(1-fa)) / oa
	return r, g, b, oa
}

// clamp8 rounds v to the nearest byte, clamping to [0,255].
func clamp8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
