// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package pdfcanvas

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"io"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/inter"
	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/pdfkit"
	"rsc.io/pdf"

	"github.com/go-webengine/engine"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

const page = `<!DOCTYPE html><html><head><style>
body { margin: 0; font: 24px sans-serif; color: #222 }
h1 { margin: 10px; padding: 10px; border: 3px solid #c00; border-radius: 12px;
     background: linear-gradient(90deg, #fc0, #f06) }
div.r { height: 60px; margin: 10px; background: radial-gradient(circle, #fff, #00f) }
p.ghost { opacity: .5 }
</style></head><body>
<h1>Vector héllo</h1>
<div class="r"></div>
<img src="IMG" width="40" height="40">
<p class="ghost">faded words</p>
</body></html>`

// pngURI is a 2×2 PNG as a data: URI.
func pngURI(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Pix[0] = 0
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// render lays page out and draws it into a one-page PDF, which it reads
// back with rsc.io/pdf, an independent reader.
func render(t *testing.T) (*pdf.Reader, string, *Canvas) {
	t.Helper()
	e := engine.New()
	e.DisableJS = true
	l, err := e.LayoutHTML(context.Background(), strings.Replace(page, "IMG", pngURI(t), 1), "", image.Rect(0, 0, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	doc := pdfkit.New(pdfkit.Options{Compress: true})
	pg := doc.AddPage(pdfkit.NewPageSize(300, 225))
	c := New(pg, 0.75, nil)
	st := l.PaintVector(c)
	if c.Err != nil {
		t.Fatal(c.Err)
	}
	if st.Rasterized != 1 {
		t.Errorf("rasterised %d, want the faded paragraph", st.Rasterized)
	}
	var buf bytes.Buffer
	if err := doc.Write(&buf); err != nil {
		t.Fatal(err)
	}
	r, err := pdf.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := io.ReadAll(r.Page(1).V.Key("Contents").Reader())
	if err != nil {
		t.Fatal(err)
	}
	return r, string(cs), c
}

func TestAPageIsVectorTextShadingsAndImages(t *testing.T) {
	r, content, _ := render(t)
	res := r.Page(1).V.Key("Resources")
	font := res.Key("Font").Key("F0")
	if font.Key("Subtype").Name() != "Type0" || font.Key("ToUnicode").Kind() == pdf.Null {
		t.Errorf("font F0: want a Type0 font with a /ToUnicode map, got %v", font)
	}
	types := map[int64]bool{}
	sh := res.Key("Shading")
	for _, k := range sh.Keys() {
		types[sh.Key(k).Key("ShadingType").Int64()] = true
	}
	if !types[2] || !types[3] {
		t.Errorf("shading types %v, want axial (2) and radial (3)", types)
	}
	if len(res.Key("XObject").Keys()) < 2 {
		t.Errorf("XObjects %v: want the <img> and the faded paragraph's bitmap", res.Key("XObject").Keys())
	}
	for _, want := range []string{" TJ", " sh", " Do", "3 Tr", "W\nn\n"} {
		if !strings.Contains(content, want) {
			t.Errorf("content stream lacks %q", want)
		}
	}
	// What copying gives: the /ToUnicode map has the characters drawn,
	// é and the faded paragraph's invisible words included (rsc.io/pdf
	// does not decode a Type0 font's text itself).
	var cmap []byte
	fonts := res.Key("Font")
	for _, k := range fonts.Keys() { // the bold heading's face and the paragraph's
		b, err := io.ReadAll(fonts.Key(k).Key("ToUnicode").Reader())
		if err != nil {
			t.Fatal(err)
		}
		cmap = append(cmap, b...)
	}
	for _, u := range []string{"<0056>", "<00E9>", "<0066>", "<0077>"} { // V é f w
		if !strings.Contains(string(cmap), u) {
			t.Errorf("ToUnicode lacks %s:\n%s", u, cmap)
		}
	}
}

// TestDegenerateCallsDrawNothing: what cannot be drawn is skipped, and what
// pdfkit refuses is reported once in Err.
func TestDegenerateCallsDrawNothing(t *testing.T) {
	doc := pdfkit.New(pdfkit.Options{})
	pg := doc.AddPage(pdfkit.NewPageSize(100, 100))
	c := New(pg, 1, NewFonts())
	var empty paint.Path
	c.Fill(&empty, css.Color{A: 255}, false)
	c.Fill(nil, css.Color{A: 255}, false)
	c.Image(nil, paint.Rect{W: 1, H: 1})
	c.Image(image.NewRGBA(image.Rect(0, 0, 1, 1)), paint.Rect{})
	c.Shade(paint.Shading{Stops: []paint.GradientStop{{Color: css.Color{A: 255}}}})
	two := []paint.GradientStop{{Pos: 0, Color: css.Color{A: 255}}, {Pos: 1, Color: css.Color{R: 255, A: 255}}}
	c.Shade(paint.Shading{Stops: two, X0: 5, Y0: 5, X1: 5, Y1: 5})
	c.Shade(paint.Shading{Stops: two, Radial: true, RX: 0, RY: 4, R1: 1})
	c.Glyphs(nil)
	c.Glyphs(&paint.GlyphRun{Size: 0, Glyphs: []paint.Glyph{{ID: 1}}})
	c.Fill(&empty, css.Color{}, false)
	if c.Err != nil {
		t.Errorf("Err = %v, want none for calls that draw nothing", c.Err)
	}
	// A shading whose stops do not increase is pdfkit's to refuse; the
	// first refusal is kept.
	c.Shade(paint.Shading{Stops: []paint.GradientStop{{Pos: .5, Color: css.Color{A: 255}}, {Pos: .5, Color: css.Color{A: 255}}}, X1: 10})
	first := c.Err
	c.Glyphs(&paint.GlyphRun{Size: 10, Glyphs: []paint.Glyph{{ID: 1}}}) // no font
	if first == nil || c.Err != first {
		t.Errorf("Err = %v, want the first refusal kept (%v)", c.Err, first)
	}
	// An empty clip path clips everything; a translucent fill sets an alpha.
	c.Clip(&empty, false)
	var sq paint.Path
	sq.RoundRect(paint.Rect{W: 10, H: 10}, 0)
	c.Fill(&sq, css.Color{R: 255, A: 128}, true)
	c.Fill(&sq, css.Color{}, false) // transparent: nothing
	c.Clip(&sq, true)
	// A glyph the font does not have is refused by pdfkit.
	ot, err := opentype.Parse(inter.TTF)
	if err != nil {
		t.Fatal(err)
	}
	c2 := New(doc.AddPage(pdfkit.NewPageSize(100, 100)), 1, nil)
	c2.Glyphs(&paint.GlyphRun{Font: ot, Size: 10, Color: css.Color{A: 255}, Glyphs: []paint.Glyph{{ID: 65535}}})
	if c2.Err == nil {
		t.Error("a glyph id past the font: no error")
	}
}

func TestFontsAreSharedAcrossPages(t *testing.T) {
	fs := NewFonts()
	e := engine.New()
	e.DisableJS = true
	l, err := e.LayoutHTML(context.Background(), "<p>same</p>", "", image.Rect(0, 0, 200, 100))
	if err != nil {
		t.Fatal(err)
	}
	doc := pdfkit.New(pdfkit.Options{})
	for range 2 {
		l.PaintVector(New(doc.AddPage(pdfkit.NewPageSize(150, 75)), 0.75, fs))
	}
	if len(fs.m) != 1 {
		t.Errorf("%d fonts for one face drawn on two pages", len(fs.m))
	}
}
