// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/paint"
)

// PageLayout is a document laid out and ready to paint: the result of the
// cascade → JavaScript settle → layout pipeline Render runs, kept so that it
// can be painted somewhere other than an image. Painting it with Paint gives
// the very image RenderDocument returns; PaintVector draws the same boxes as
// vectors — a PDF page with real text, for one.
//
// Laying out and painting are split so that a caller can lay out several
// documents concurrently and paint them, in order, into one output that is
// not safe for concurrent use. A PageLayout is not itself safe for
// concurrent use: paint it from one goroutine at a time.
type PageLayout struct {
	e        *Engine
	doc      *Document
	rp       *renderPass
	fonts    *paint.Fonts
	viewport image.Rectangle // CSS px, as asked
	vpW      int
}

// LayoutHTML lays out an HTML string at the given viewport (CSS px), as
// RenderHTML does, without painting it.
func (e *Engine) LayoutHTML(ctx context.Context, htmlSrc, baseURL string, viewport image.Rectangle) (*PageLayout, error) {
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		return nil, err
	}
	return e.LayoutDocument(ctx, &Document{URL: baseURL, Title: dom.Title(root), Root: root, HTML: htmlSrc}, viewport), nil
}

// LayoutDocument lays out an already-fetched Document, as RenderDocument
// does, without painting it.
func (e *Engine) LayoutDocument(ctx context.Context, doc *Document, viewport image.Rectangle) *PageLayout {
	fonts := paint.NewFonts()
	vpW, vpH := viewportSize(viewport)
	rp := e.renderCore(ctx, doc, vpW, vpH, fonts)
	return &PageLayout{e: e, doc: doc, rp: rp, fonts: fonts, viewport: viewport, vpW: vpW}
}

// Info is the render information RenderDocument would report.
func (l *PageLayout) Info() *RenderInfo { return renderInfo(l.doc, l.rp) }

// Links is the anchor hit-map of the layout, as RenderDocumentWithLinks
// returns it, in device pixels.
func (l *PageLayout) Links() []Link { return LinksFromBox(l.rp.box, l.doc.URL) }

// Paint rasterises the layout: the image RenderDocument returns.
func (l *PageLayout) Paint() *image.RGBA {
	img := l.e.newCanvas(l.doc, l.rp, l.viewport, l.vpW)
	paint.PaintFull(img, l.rp.box, l.fonts, l.rp.imgs, l.rp.bgImgs)
	return img
}

// Page is the device-pixel rectangle PaintVector draws: the viewport, at
// Engine.DeviceScale. Unlike Paint's image, it does not grow with the
// content — a page has the size it was given.
func (l *PageLayout) Page() image.Rectangle {
	return l.e.deviceViewport(image.Rect(0, 0, l.vpW, viewportHeight(l.viewport)))
}

// viewportHeight is viewportSize's height.
func viewportHeight(viewport image.Rectangle) int {
	_, h := viewportSize(viewport)
	return h
}

// PaintVector draws the layout through c (see paint.PaintVector): first the
// page backdrop Paint starts from — Engine.Backdrop or white, then the
// document's own html/body background — over the whole page, then the boxes.
// Coordinates are device pixels; what lies outside Page is the canvas's to
// clip.
func (l *PageLayout) PaintVector(c paint.Canvas) paint.VectorStats {
	page := l.Page()
	base := css.Color{R: 255, G: 255, B: 255, A: 255}
	if l.e.Backdrop.A > 0 {
		base = l.e.Backdrop
		base.A = 255
	}
	if bg, ok := pageBackground(l.doc.Root, l.rp.sm); ok {
		// fillColor paints the page colour opaque over the base, whatever
		// its own alpha.
		base = bg
		base.A = 255
	}
	var p paint.Path
	p.RoundRect(paint.Rect{W: float64(page.Dx()), H: float64(page.Dy())}, 0)
	c.Fill(&p, base, false)
	return paint.PaintVector(c, l.rp.box, l.fonts, l.rp.imgs, l.rp.bgImgs, page)
}
