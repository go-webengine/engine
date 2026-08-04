// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package metrics implements the pure-Go fidelity metrics used by the
// webengine-vs-Chromium comparison harness: windowed SSIM over luminance, a
// pixel-diff percentage, and a diff heat-map / side-by-side montage builder.
//
// It depends only on the standard library, golang.org/x/image (for montage
// labels) and github.com/go-images/images (grayscale/resize/crop), so it never
// touches the CGO=0 engine module's dependency surface beyond what the engine
// already uses.
package metrics

import (
	"image"
	"image/color"

	goimages "github.com/go-images/images"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// CommonRegion crops a and b to their common top-left region (the minimum of
// their widths and heights). Real renderers produce different full-page heights;
// comparing the overlapping region is the honest, well-defined thing to do.
func CommonRegion(a, b image.Image) (*image.RGBA, *image.RGBA) {
	ra := goimages.ToRGBA(a)
	rb := goimages.ToRGBA(b)
	w := min(ra.Bounds().Dx(), rb.Bounds().Dx())
	h := min(ra.Bounds().Dy(), rb.Bounds().Dy())
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	ca, _ := goimages.Crop(ra, image.Rect(0, 0, w, h))
	cb, _ := goimages.Crop(rb, image.Rect(0, 0, w, h))
	return ca, cb
}

// CapHeight crops img to at most maxH rows from the top (maxH<=0 leaves it
// unchanged). Used to bound the fidelity region and montage size on very tall
// pages to a well-defined, well-past-the-fold slice.
func CapHeight(img *image.RGBA, maxH int) *image.RGBA {
	if maxH <= 0 || img.Bounds().Dy() <= maxH {
		return img
	}
	c, _ := goimages.Crop(img, image.Rect(0, 0, img.Bounds().Dx(), maxH))
	return c
}

// luma extracts the per-pixel luminance plane (0..255 float64) from an RGBA
// image using go-images' grayscale (Rec.601 weights), reading the red channel
// of the grayscale result (R==G==B there).
func luma(img *image.RGBA) ([]float64, int, int) {
	g := goimages.Grayscale(img)
	w, h := g.Bounds().Dx(), g.Bounds().Dy()
	out := make([]float64, w*h)
	for y := 0; y < h; y++ {
		row := g.PixOffset(g.Bounds().Min.X, g.Bounds().Min.Y+y)
		for x := 0; x < w; x++ {
			out[y*w+x] = float64(g.Pix[row+x*4])
		}
	}
	return out, w, h
}

// SSIM returns the mean structural-similarity index (in [-1,1], 1 == identical)
// between a and b over their luminance planes, using a sliding window of the
// given size and step. Panics-free: mismatched sizes are cropped to the common
// region by the caller (CommonRegion); here a and b must already match.
//
// Uses the standard SSIM constants for 8-bit data: C1=(0.01*255)^2,
// C2=(0.03*255)^2.
func SSIM(a, b *image.RGBA, window, step int) float64 {
	if window < 2 {
		window = 8
	}
	if step < 1 {
		step = window / 2
	}
	if step < 1 {
		step = 1
	}
	la, wa, ha := luma(a)
	lb, wb, hb := luma(b)
	w, h := min(wa, wb), min(ha, hb)
	if w < window || h < window {
		// Too small for a window: fall back to a single global SSIM.
		return ssimWindow(la, lb, wa, wb, 0, 0, min(w, wa), min(h, ha))
	}
	var sum float64
	var n int
	for y := 0; y+window <= h; y += step {
		for x := 0; x+window <= w; x += step {
			sum += ssimWindowCorr(la, lb, wa, wb, x, y, window, window)
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return sum / float64(n)
}

// ssimWindowCorr computes SSIM over one rectangular window with top-left (x0,y0)
// and size (bw,bh), reading la/lb whose row strides are wa/wb.
func ssimWindowCorr(la, lb []float64, wa, wb, x0, y0, bw, bh int) float64 {
	const c1 = (0.01 * 255) * (0.01 * 255)
	const c2 = (0.03 * 255) * (0.03 * 255)
	var muA, muB float64
	nn := float64(bw * bh)
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			muA += la[(y0+y)*wa+(x0+x)]
			muB += lb[(y0+y)*wb+(x0+x)]
		}
	}
	muA /= nn
	muB /= nn
	var varA, varB, cov float64
	for y := 0; y < bh; y++ {
		for x := 0; x < bw; x++ {
			da := la[(y0+y)*wa+(x0+x)] - muA
			db := lb[(y0+y)*wb+(x0+x)] - muB
			varA += da * da
			varB += db * db
			cov += da * db
		}
	}
	// Sample variance/covariance (unbiased) matches the reference implementation.
	if nn > 1 {
		varA /= nn - 1
		varB /= nn - 1
		cov /= nn - 1
	}
	num := (2*muA*muB + c1) * (2*cov + c2)
	den := (muA*muA + muB*muB + c1) * (varA + varB + c2)
	if den == 0 {
		return 1
	}
	return num / den
}

// ssimWindow is the single-window fallback for tiny images.
func ssimWindow(la, lb []float64, wa, wb, x0, y0, bw, bh int) float64 {
	if bw < 1 || bh < 1 {
		return 1
	}
	return ssimWindowCorr(la, lb, wa, wb, x0, y0, bw, bh)
}

// PixDiff returns the fraction (0..1) of pixels whose maximum per-channel
// absolute difference exceeds threshold (0..255). a and b must be the same size.
func PixDiff(a, b *image.RGBA, threshold uint8) float64 {
	w := min(a.Bounds().Dx(), b.Bounds().Dx())
	h := min(a.Bounds().Dy(), b.Bounds().Dy())
	if w == 0 || h == 0 {
		return 0
	}
	var diff int
	for y := 0; y < h; y++ {
		ra := a.PixOffset(a.Bounds().Min.X, a.Bounds().Min.Y+y)
		rb := b.PixOffset(b.Bounds().Min.X, b.Bounds().Min.Y+y)
		for x := 0; x < w; x++ {
			ia := ra + x*4
			ib := rb + x*4
			d := absDiff(a.Pix[ia], b.Pix[ib])
			if g := absDiff(a.Pix[ia+1], b.Pix[ib+1]); g > d {
				d = g
			}
			if bl := absDiff(a.Pix[ia+2], b.Pix[ib+2]); bl > d {
				d = bl
			}
			if uint8(d) > threshold {
				diff++
			}
		}
	}
	return float64(diff) / float64(w*h)
}

func absDiff(x, y uint8) int {
	if x > y {
		return int(x) - int(y)
	}
	return int(y) - int(x)
}

// Heatmap renders a per-pixel difference magnitude map: white == identical,
// deepening red == larger max-channel difference. a and b must be the same size.
func Heatmap(a, b *image.RGBA) *image.RGBA {
	w := min(a.Bounds().Dx(), b.Bounds().Dx())
	h := min(a.Bounds().Dy(), b.Bounds().Dy())
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		ra := a.PixOffset(a.Bounds().Min.X, a.Bounds().Min.Y+y)
		rb := b.PixOffset(b.Bounds().Min.X, b.Bounds().Min.Y+y)
		for x := 0; x < w; x++ {
			ia := ra + x*4
			ib := rb + x*4
			d := absDiff(a.Pix[ia], b.Pix[ib])
			if g := absDiff(a.Pix[ia+1], b.Pix[ib+1]); g > d {
				d = g
			}
			if bl := absDiff(a.Pix[ia+2], b.Pix[ib+2]); bl > d {
				d = bl
			}
			o := out.PixOffset(x, y)
			// White base, subtract green+blue proportional to diff so identical
			// stays white and maximal diff is saturated red.
			f := uint8(255 - d)
			out.Pix[o+0] = 255
			out.Pix[o+1] = f
			out.Pix[o+2] = f
			out.Pix[o+3] = 255
		}
	}
	return out
}

const labelBand = 18 // px tall header strip for panel labels

// Montage lays webengine | chrome | diff-heatmap side by side, each panel under
// a labelled header band, separated by 4px gutters. All three inputs must share
// the same dimensions (use CommonRegion + Heatmap first).
func Montage(webengine, chrome, heat *image.RGBA) *image.RGBA {
	w := webengine.Bounds().Dx()
	h := webengine.Bounds().Dy()
	const gutter = 4
	totalW := w*3 + gutter*2
	totalH := h + labelBand
	out := image.NewRGBA(image.Rect(0, 0, totalW, totalH))
	// Fill background light grey.
	fill(out, out.Bounds(), color.RGBA{40, 40, 40, 255})
	panels := []struct {
		img   *image.RGBA
		label string
	}{
		{webengine, "webengine"},
		{chrome, "chrome"},
		{heat, "diff (red=delta)"},
	}
	for i, p := range panels {
		x0 := i * (w + gutter)
		drawLabel(out, x0, 0, w, p.label)
		blit(out, p.img, x0, labelBand)
	}
	return out
}

// blit copies src into dst at (dx,dy).
func blit(dst *image.RGBA, src *image.RGBA, dx, dy int) {
	sw, sh := src.Bounds().Dx(), src.Bounds().Dy()
	for y := 0; y < sh; y++ {
		so := src.PixOffset(src.Bounds().Min.X, src.Bounds().Min.Y+y)
		do := dst.PixOffset(dx, dy+y)
		copy(dst.Pix[do:do+sw*4], src.Pix[so:so+sw*4])
	}
}

func fill(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			o := dst.PixOffset(x, y)
			dst.Pix[o+0] = c.R
			dst.Pix[o+1] = c.G
			dst.Pix[o+2] = c.B
			dst.Pix[o+3] = c.A
		}
	}
}

// drawLabel writes a white label onto the dark header band at column x0.
func drawLabel(dst *image.RGBA, x0, y0, w int, s string) {
	fill(dst, image.Rect(x0, y0, x0+w, y0+labelBand), color.RGBA{40, 40, 40, 255})
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.RGBA{235, 235, 235, 255}),
		Face: basicfont.Face7x13,
		Dot:  fixed.P(x0+4, y0+13),
	}
	d.DrawString(s)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
