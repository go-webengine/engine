// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package metrics

import (
	"image"
	"testing"
)

func solid(w, h int, r, g, b uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = r
		img.Pix[i+1] = g
		img.Pix[i+2] = b
		img.Pix[i+3] = 255
	}
	return img
}

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := img.PixOffset(x, y)
			v := uint8((x*255)/max(1, w-1)) ^ uint8((y*97)&0xff)
			img.Pix[o+0] = v
			img.Pix[o+1] = v
			img.Pix[o+2] = v
			img.Pix[o+3] = 255
		}
	}
	return img
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestSSIMIdentical(t *testing.T) {
	a := gradient(64, 48)
	b := gradient(64, 48)
	s := SSIM(a, b, 8, 4)
	if s < 0.999 {
		t.Fatalf("identical images should have SSIM ~1, got %v", s)
	}
}

func TestSSIMDifferent(t *testing.T) {
	a := solid(64, 48, 0, 0, 0)
	b := solid(64, 48, 255, 255, 255)
	s := SSIM(a, b, 8, 4)
	if s > 0.05 {
		t.Fatalf("black vs white should have low SSIM, got %v", s)
	}
}

func TestSSIMTinyFallback(t *testing.T) {
	a := solid(4, 4, 10, 10, 10)
	b := solid(4, 4, 10, 10, 10)
	if s := SSIM(a, b, 8, 4); s < 0.999 {
		t.Fatalf("tiny identical fallback SSIM should be ~1, got %v", s)
	}
}

func TestPixDiffIdentical(t *testing.T) {
	a := gradient(32, 32)
	b := gradient(32, 32)
	if pd := PixDiff(a, b, 16); pd != 0 {
		t.Fatalf("identical PixDiff should be 0, got %v", pd)
	}
}

func TestPixDiffAll(t *testing.T) {
	a := solid(10, 10, 0, 0, 0)
	b := solid(10, 10, 255, 255, 255)
	if pd := PixDiff(a, b, 16); pd != 1 {
		t.Fatalf("fully different PixDiff should be 1, got %v", pd)
	}
}

func TestPixDiffThreshold(t *testing.T) {
	a := solid(10, 10, 100, 100, 100)
	b := solid(10, 10, 110, 110, 110) // delta 10, below threshold 16
	if pd := PixDiff(a, b, 16); pd != 0 {
		t.Fatalf("sub-threshold delta should count as 0, got %v", pd)
	}
	if pd := PixDiff(a, b, 5); pd != 1 { // delta 10 > 5
		t.Fatalf("supra-threshold delta should count as 1, got %v", pd)
	}
}

func TestCommonRegion(t *testing.T) {
	a := solid(100, 200, 1, 2, 3)
	b := solid(80, 300, 4, 5, 6)
	ca, cb := CommonRegion(a, b)
	if ca.Bounds().Dx() != 80 || ca.Bounds().Dy() != 200 {
		t.Fatalf("common region a = %v, want 80x200", ca.Bounds())
	}
	if cb.Bounds().Dx() != 80 || cb.Bounds().Dy() != 200 {
		t.Fatalf("common region b = %v, want 80x200", cb.Bounds())
	}
}

func TestCapHeight(t *testing.T) {
	img := solid(50, 400, 1, 2, 3)
	if got := CapHeight(img, 100); got.Bounds().Dy() != 100 || got.Bounds().Dx() != 50 {
		t.Fatalf("CapHeight(400,100) = %v, want 50x100", got.Bounds())
	}
	if got := CapHeight(img, 0); got.Bounds().Dy() != 400 {
		t.Fatalf("CapHeight maxH=0 should be unchanged, got %v", got.Bounds())
	}
	if got := CapHeight(img, 900); got.Bounds().Dy() != 400 {
		t.Fatalf("CapHeight above height should be unchanged, got %v", got.Bounds())
	}
}

func TestHeatmapAndMontage(t *testing.T) {
	a := solid(40, 30, 0, 0, 0)
	b := solid(40, 30, 255, 255, 255)
	heat := Heatmap(a, b)
	// Maximal diff -> saturated red: G and B channels near 0.
	o := heat.PixOffset(5, 5)
	if heat.Pix[o+0] != 255 || heat.Pix[o+1] > 5 || heat.Pix[o+2] > 5 {
		t.Fatalf("max-diff heat pixel = %v,%v,%v want red", heat.Pix[o], heat.Pix[o+1], heat.Pix[o+2])
	}
	m := Montage(a, b, heat)
	wantW := 40*3 + 4*2
	wantH := 30 + labelBand
	if m.Bounds().Dx() != wantW || m.Bounds().Dy() != wantH {
		t.Fatalf("montage size = %v, want %dx%d", m.Bounds(), wantW, wantH)
	}
}

func TestHeatmapIdenticalWhite(t *testing.T) {
	a := gradient(20, 20)
	heat := Heatmap(a, a)
	o := heat.PixOffset(10, 10)
	if heat.Pix[o+0] != 255 || heat.Pix[o+1] != 255 || heat.Pix[o+2] != 255 {
		t.Fatalf("identical heat pixel should be white, got %v,%v,%v", heat.Pix[o], heat.Pix[o+1], heat.Pix[o+2])
	}
}
