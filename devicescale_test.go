// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func renderScaled(t *testing.T, scale float64, js bool, html string, w, h int) (*image.RGBA, *RenderInfo) {
	t.Helper()
	e := New()
	e.DeviceScale = scale
	e.DisableJS = !js
	img, info, err := e.RenderHTML(context.Background(), html, "https://demo.test/", image.Rect(0, 0, w, h))
	if err != nil {
		t.Fatal(err)
	}
	return img, info
}

// diffFraction is the fraction of pixels whose RGB differ by more than tol
// on any channel. The images must have the same bounds.
func diffFraction(t *testing.T, a, b *image.RGBA, tol int) float64 {
	t.Helper()
	if a.Bounds() != b.Bounds() {
		t.Fatalf("bounds differ: %v vs %v", a.Bounds(), b.Bounds())
	}
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			d := int(a.Pix[i+c]) - int(b.Pix[i+c])
			if d > tol || d < -tol {
				n++
				break
			}
		}
	}
	return float64(n) / float64(len(a.Pix)/4)
}

// upscaleNearest doubles an image with nearest-neighbour sampling: what a
// renderer that paints at 1x and enlarges the bitmap would produce.
func upscaleNearest(src *image.RGBA, k int) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx()*k, b.Dy()*k))
	for y := 0; y < b.Dy()*k; y++ {
		for x := 0; x < b.Dx()*k; x++ {
			out.SetRGBA(x, y, src.RGBAAt(x/k, y/k))
		}
	}
	return out
}

// TestDeviceScaleGeometry: layout is in CSS px, the returned image in device
// px (CSSOM View: devicePixelRatio = CSS pixel size / device pixel size).
func TestDeviceScaleGeometry(t *testing.T) {
	page := `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<div style="width:50px;height:25px;background:#ff0000"></div>` +
		`<div style="width:50%;height:1in;background:#0000ff"></div></body></html>`
	img, _ := renderScaled(t, 2, false, page, 100, 200)
	if got := img.Bounds().Size(); got != (image.Point{200, 400}) {
		t.Fatalf("image size %v, want 200x400 (viewport 100x200 CSS px at 2x)", got)
	}
	assertPixel(t, img, 99, 49, 0xff, 0, 0, "red box last device pixel")
	assertPixel(t, img, 100, 20, 0xff, 0xff, 0xff, "right of red box")
	assertPixel(t, img, 10, 50+191, 0, 0, 0xff, "blue box: 1in = 96 CSS px = 192 device px")
	assertPixel(t, img, 99, 100, 0, 0, 0xff, "blue box 50% of 100 CSS px = 100 device px")
	assertPixel(t, img, 100, 100, 0xff, 0xff, 0xff, "right of blue box")

	// 0 and 1 are today's behaviour.
	one, _ := renderScaled(t, 1, false, page, 100, 200)
	zero, _ := renderScaled(t, 0, false, page, 100, 200)
	if !bytes.Equal(one.Pix, zero.Pix) || one.Bounds().Dx() != 100 {
		t.Error("DeviceScale 0 and 1 must render identically, at CSS size")
	}
}

// TestDeviceScaleText: text is rasterised at size x scale — a 2x render of
// 20px text is the 1x render of the same text at 40px, not an enlarged 20px
// bitmap.
func TestDeviceScaleText(t *testing.T) {
	page := func(px string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
			`<p style="margin:0;font-size:` + px + `;font-family:sans-serif">Hamburgefonstiv 0123</p></body></html>`
	}
	twoX, _ := renderScaled(t, 2, false, page("20px"), 200, 30)
	big, _ := renderScaled(t, 1, false, page("40px"), 400, 60)
	small, _ := renderScaled(t, 1, false, page("20px"), 200, 30)
	enlarged := upscaleNearest(small, 2)
	same := diffFraction(t, twoX, big, 8)
	blurred := diffFraction(t, twoX, enlarged, 8)
	t.Logf("2x@20px vs 1x@40px: %.4f differ; vs 1x@20px enlarged: %.4f differ", same, blurred)
	if same > 0.002 {
		t.Errorf("2x render of 20px text differs from 1x 40px text in %.2f%% of pixels, want ~0", same*100)
	}
	if blurred < 0.01 {
		t.Errorf("2x render looks like an enlarged 1x bitmap (%.2f%% differ)", blurred*100)
	}
}

// TestDeviceScaleMediaAndScript: @media width features stay in CSS px,
// `resolution` reflects the scale (Media Queries 4 §5.1), and scripts see
// devicePixelRatio and CSS-px geometry.
func TestDeviceScaleMediaAndScript(t *testing.T) {
	page := `<!DOCTYPE html><html><head><style>html,body{margin:0}div{width:10px;height:10px}` +
		`@media (min-width:90px){#w{background:#ff0000}}` +
		`@media (min-width:150px){#w{background:#00ff00}}` +
		`@media (resolution:2dppx){#r{background:#0000ff}}` +
		`@media (min-resolution:3dppx){#r{background:#00ff00}}` +
		`@media (-webkit-min-device-pixel-ratio:2){#k{background:#ff00ff}}` +
		`</style></head><body><div id="w"></div><div id="r"></div><div id="k"></div>` +
		`<script>var r=document.getElementById('w').getBoundingClientRect();` +
		`document.title=devicePixelRatio+','+r.width+','+innerWidth+','+matchMedia('(min-resolution: 2dppx)').matches;</script>` +
		`</body></html>`
	img, info := renderScaled(t, 2, true, page, 100, 100)
	assertPixel(t, img, 5, 5, 0xff, 0, 0, "min-width:90px holds at a 100 CSS px viewport")
	assertPixel(t, img, 5, 25, 0, 0, 0xff, "resolution:2dppx")
	assertPixel(t, img, 5, 45, 0xff, 0, 0xff, "-webkit-min-device-pixel-ratio:2")
	if info.Title != "2,10,100,true" {
		t.Errorf("script saw %q, want devicePixelRatio=2, a 10 CSS px wide rect, innerWidth 100, matchMedia true", info.Title)
	}
	one, _ := renderScaled(t, 1, false, page, 100, 100)
	assertPixel(t, one, 5, 15, 0xff, 0xff, 0xff, "resolution:2dppx does not hold at 1x")
}

// TestDeviceScaleImages: a raster image's intrinsic size is in CSS px, so a
// 4x4 PNG covers 8x8 device pixels at 2x.
func TestDeviceScaleImages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i], src.Pix[i+3] = 0xff, 0xff
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	page := `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<img src="` + uri + `" style="display:block">` +
		`<div style="width:4px;height:4px;background-image:url(` + uri + `);background-repeat:no-repeat"></div></body></html>`
	img, _ := renderScaled(t, 2, false, page, 50, 50)
	red := color.RGBA{0xff, 0, 0, 0xff}
	if c := img.RGBAAt(7, 7); c != red {
		t.Errorf("img device pixel (7,7) = %v, want red (4 CSS px = 8 device px)", c)
	}
	if c := img.RGBAAt(8, 3); c == red {
		t.Error("img wider than 8 device px")
	}
	if c := img.RGBAAt(7, 8+7); c != red {
		t.Errorf("background device pixel (7,15) = %v, want red", c)
	}
}
