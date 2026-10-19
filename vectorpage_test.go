// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"image"
	"strings"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/paint"
)

// recCanvas records what PaintVector draws.
type recCanvas struct {
	fills []css.Color
	text  strings.Builder
}

func (c *recCanvas) Save()                                     {}
func (c *recCanvas) Restore()                                  {}
func (c *recCanvas) Clip(*paint.Path, bool)                    {}
func (c *recCanvas) Fill(_ *paint.Path, col css.Color, _ bool) { c.fills = append(c.fills, col) }
func (c *recCanvas) Shade(paint.Shading)                       {}
func (c *recCanvas) Image(image.Image, paint.Rect)             {}
func (c *recCanvas) Glyphs(r *paint.GlyphRun) {
	for _, g := range r.Glyphs {
		c.text.WriteString(g.Text)
	}
}

const vectorPageHTML = `<!DOCTYPE html><html><head><style>
body { margin: 0; background: #102030; color: #fff; font: 20px sans-serif }
</style></head><body><p>Hello <a href="https://example.com/x">vector</a></p></body></html>`

// TestPageLayoutPaintIsRenderHTML: painting a layout to an image gives the
// very image RenderHTML returns; the layout's links and info are the ones
// RenderDocumentWithLinks reports.
func TestPageLayoutPaintIsRenderHTML(t *testing.T) {
	e := New()
	e.DisableJS = true
	view := image.Rect(0, 0, 320, 200)
	want, info, err := e.RenderHTML(context.Background(), vectorPageHTML, "https://example.com/", view)
	if err != nil {
		t.Fatal(err)
	}
	l, err := e.LayoutHTML(context.Background(), vectorPageHTML, "https://example.com/", view)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Paint(); !bytes.Equal(got.Pix, want.Pix) || got.Rect != want.Rect {
		t.Error("PageLayout.Paint differs from RenderHTML")
	}
	if got := l.Info(); got.ContentHeight != info.ContentHeight || got.URL != info.URL {
		t.Errorf("Info = %+v, want %+v", got, info)
	}
	if ls := l.Links(); len(ls) != 1 || ls[0].Href != "https://example.com/x" {
		t.Errorf("Links = %+v", ls)
	}
	if p := l.Page(); p != view {
		t.Errorf("Page = %v, want the viewport %v", p, view)
	}
	if _, err := e.LayoutHTML(context.Background(), "\x00<", "", view); err != nil {
		t.Logf("a hostile source is laid out or refused, never a panic: %v", err)
	}
}

// TestPageLayoutPaintVector: the page backdrop first — the body's
// background, opaque — then the boxes, the text as glyphs.
func TestPageLayoutPaintVector(t *testing.T) {
	e := New()
	e.DisableJS = true
	e.DeviceScale = 2
	l, err := e.LayoutHTML(context.Background(), vectorPageHTML, "", image.Rect(0, 0, 320, 200))
	if err != nil {
		t.Fatal(err)
	}
	if p := l.Page(); p != image.Rect(0, 0, 640, 400) {
		t.Errorf("Page at device scale 2 = %v", p)
	}
	var c recCanvas
	st := l.PaintVector(&c)
	if len(c.fills) == 0 || c.fills[0] != (css.Color{R: 0x10, G: 0x20, B: 0x30, A: 255}) {
		t.Errorf("first fill = %v, want the body background", c.fills)
	}
	// Words are the layout's items, the spaces between them gaps: a PDF
	// reader puts them back from the gaps, as pdftotext does.
	if got := c.text.String(); got != "Hellovector" {
		t.Errorf("text = %q", got)
	}
	if st.Rasterized != 0 || st.Glyphs == 0 {
		t.Errorf("stats = %+v", st)
	}
	// No page background: the engine's backdrop, else white.
	e.Backdrop = css.Color{R: 1, G: 2, B: 3, A: 9}
	l, _ = e.LayoutHTML(context.Background(), "<p>x</p>", "", image.Rect(0, 0, 0, 0))
	c = recCanvas{}
	l.PaintVector(&c)
	if c.fills[0] != (css.Color{R: 1, G: 2, B: 3, A: 255}) {
		t.Errorf("backdrop fill = %v", c.fills[0])
	}
	e.Backdrop = css.Color{}
	l, _ = e.LayoutHTML(context.Background(), "<p>x</p>", "", image.Rect(0, 0, 0, 0))
	c = recCanvas{}
	l.PaintVector(&c)
	if c.fills[0] != (css.Color{R: 255, G: 255, B: 255, A: 255}) {
		t.Errorf("default fill = %v", c.fills[0])
	}
	if l.Page() != image.Rect(0, 0, 2048, 1536) {
		t.Errorf("default viewport page = %v", l.Page())
	}
}
