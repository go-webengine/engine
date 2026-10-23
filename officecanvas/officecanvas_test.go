// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package officecanvas

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/go-opentype/fonts/inter"
	"github.com/go-opentype/opentype"

	"github.com/go-webengine/engine"
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

func pngURI(t *testing.T, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// record lays html out at 400×300 and records it.
func record(t *testing.T, html string) (Page, map[*opentype.Font]*Face) {
	t.Helper()
	e := engine.New()
	e.DisableJS = true
	l, err := e.LayoutHTML(context.Background(), html, "https://example.com/", image.Rect(0, 0, 400, 300))
	if err != nil {
		t.Fatal(err)
	}
	var links []Link
	for _, ln := range l.Links() {
		links = append(links, Link{Rect: ln.Rect, Href: ln.Href})
	}
	faces := map[*opentype.Font]*Face{}
	c := New(l.Page(), links, faces)
	l.PaintVector(c)
	return c.Page(), faces
}

// A line that begins with a smaller run, then larger words, is one text box
// with one baseline: the second word of the larger text joins it. Before, the
// shape's baseline was its top plus the FIRST run's ascent; once the larger
// word raised the top, that baseline was above the text and the next word
// started a box of its own. Found by go-slides' callout notes ("2 Reads" /
// "the input.").
func TestAMixedSizeLineIsOneBox(t *testing.T) {
	pg, _ := record(t, `<html><body style="margin:0;font:20px/1.5 sans-serif"><p style="margin:10px"><span style="font-size:12px">2</span> Reads the input.</p></body></html>`)
	var boxes []string
	for _, s := range pg.Shapes {
		if s.Kind != KindText {
			continue
		}
		var line []string
		for _, r := range s.Runs {
			line = append(line, r.Text)
		}
		boxes = append(boxes, strings.Join(line, "|"))
		lr := s.Runs[len(s.Runs)-1]
		big := float64(lr.Face.Font.Ascent()) * lr.Size / float64(lr.Face.Font.UnitsPerEm())
		if d := s.Baseline() - (s.Bounds.Y + big); d > 0.01 || d < -0.01 {
			t.Errorf("baseline %v, want the top plus the larger text's ascent %v", s.Baseline(), s.Bounds.Y+big)
		}
	}
	if got := strings.Join(boxes, " / "); got != "2 |Reads the input." {
		t.Errorf("boxes = %q, want one box: the small run, then the line", got)
	}
}

func TestAPageBecomesShapes(t *testing.T) {
	html := `<html><body style="margin:0;background:#102030;color:#fff;font:20px sans-serif">
<p style="margin:10px">Hello <b>bold</b> <a href="/x" style="color:#ff0">a link</a></p>
<p style="margin:10px;letter-spacing:3px">spaced</p>
<div style="margin:10px;height:40px;border-radius:10px;background:linear-gradient(90deg,#f00 50%,transparent 50%)"></div>
<div style="margin:10px;width:100px;height:50px;background:url(IMG) center/cover"></div>
</body></html>`
	pg, faces := record(t, strings.Replace(html, "IMG", pngURI(t, 40, 10), 1))
	if pg.Background == nil || *pg.Background != (css.Color{R: 0x10, G: 0x20, B: 0x30, A: 255}) {
		t.Fatalf("background = %v, want the body's colour", pg.Background)
	}
	var text []string
	var grad *Shape
	var img *Shape
	for i := range pg.Shapes {
		s := &pg.Shapes[i]
		switch s.Kind {
		case KindText:
			var line []string
			for _, r := range s.Runs {
				line = append(line, r.Text)
			}
			text = append(text, strings.Join(line, "|"))
			if strings.Contains(strings.Join(line, ""), "link") {
				last := s.Runs[len(s.Runs)-1]
				if last.Link != "https://example.com/x" {
					t.Errorf("the link run links to %q", last.Link)
				}
				if !s.Runs[1].Face.Bold || s.Runs[0].Face.Bold {
					t.Error("only the bold run is in a bold face")
				}
			}
			if s.Runs[0].Text == "spaced" && !(s.Runs[0].Spacing > 2.5 && s.Runs[0].Spacing < 3.5) {
				t.Errorf("letter spacing = %v, want 3", s.Runs[0].Spacing)
			}
			if b := s.Baseline(); b <= s.Bounds.Y || b >= s.Bounds.Y+s.Bounds.H {
				t.Errorf("baseline %v is outside the box %+v", b, s.Bounds)
			}
		case KindGradient:
			grad = s
		case KindImage:
			img = s
		}
	}
	if got := strings.Join(text, " / "); got != "Hello |bold |a link / spaced" {
		t.Errorf("lines = %q, want one box a line, runs split by style", got)
	}
	if grad == nil {
		t.Fatal("no gradient shape")
	}
	if grad.Rect || grad.Gradient.Radial {
		t.Error("a rounded linear gradient is not a plain rectangle, nor radial")
	}
	// Red to the middle, a hard edge, transparent after.
	st := grad.Gradient.Stops
	if len(st) < 4 || st[0].Color.A != 255 || st[len(st)-1].Color.A != 0 {
		t.Errorf("stops = %+v, want opaque red then transparent", st)
	}
	if r := grad.Render(1); r == nil || r.NRGBAAt(1, 1).A != 0 || r.NRGBAAt(r.Bounds().Dx()/4, r.Bounds().Dy()/2).R < 200 {
		t.Error("the gradient's picture should be clear in the rounded corner and red on the left")
	}
	if img == nil || img.Crop[0] <= 0 || img.Crop[2] <= 0 || img.Bounds.W != 100 {
		t.Errorf("a wide picture covering its box should be cropped left and right: %+v", img)
	}
	for _, f := range faces {
		if f.Family == "" || !f.Embeddable {
			t.Errorf("face %+v: want a family, embeddable", f)
		}
	}
}

func TestInvisibleTextIsTransparent(t *testing.T) {
	pg, _ := record(t, `<body style="margin:0"><p style="opacity:.5">ghost</p></body>`)
	found := false
	for _, s := range pg.Shapes {
		if s.Kind == KindText && s.Runs[0].Text == "ghost" {
			found = s.Runs[0].Invisible && s.Runs[0].Color.A == 0
		}
	}
	if !found {
		t.Error("the text of a picture should be an invisible run with zero alpha")
	}
}

func TestSFNTIsTheFontAgain(t *testing.T) {
	ot, err := opentype.Parse(inter.TTF)
	if err != nil {
		t.Fatal(err)
	}
	data := SFNT(ot)
	back, err := opentype.Parse(data)
	if err != nil {
		t.Fatalf("the reassembled font does not parse: %v", err)
	}
	if back.NumGlyphs() != ot.NumGlyphs() || back.UnitsPerEm() != ot.UnitsPerEm() {
		t.Error("the reassembled font is not the same font")
	}
	if fam, sub := names(back); fam != "Inter" || sub != "Regular" {
		t.Errorf("names = %q %q", fam, sub)
	}
	if !embeddable(back) {
		t.Error("Inter is OFL: embeddable")
	}
}

func TestEdgeCasesDrawNothing(t *testing.T) {
	c := New(image.Rect(0, 0, 100, 100), nil, nil)
	var empty paint.Path
	c.Restore() // with nothing saved
	c.Fill(&empty, css.Color{A: 255}, false)
	c.Fill(nil, css.Color{A: 255}, false)
	c.Shade(paint.Shading{Stops: []paint.GradientStop{{}, {Pos: 1}}}) // no clip
	c.Image(nil, paint.Rect{W: 1, H: 1})
	c.Glyphs(nil)
	c.Glyphs(&paint.GlyphRun{Size: 10})
	var sq paint.Path
	sq.RoundRect(paint.Rect{X: 10, Y: 10, W: 10, H: 10}, 0)
	c.Save()
	c.Clip(&sq, false)
	c.Image(image.NewRGBA(image.Rect(0, 0, 4, 4)), paint.Rect{X: 50, Y: 50, W: 10, H: 10}) // outside the clip
	c.Restore()
	if n := len(c.Page().Shapes); n != 0 || c.Page().Background != nil {
		t.Errorf("%d shapes recorded from nothing", n)
	}
	s := Shape{Kind: KindFill}
	if s.Render(1) != nil {
		t.Error("only a gradient renders")
	}
}
