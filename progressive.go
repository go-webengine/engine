// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// This file adds STAGED progressive rendering on top of the batch pipeline. A
// browser paints before all resources/scripts are done and re-flows
// incrementally; the engine emulates that with a small number of fully-styled
// frames (external CSS already applied, so no unstyled flash):
//
//   - "initial": the first layout, right after external sheets are fetched and
//     cascaded and images placed, BEFORE the JS settle loop — the big
//     perceived-latency win on script-heavy pages.
//   - "settle": after each settle pass that visibly changed the geometry
//     (deduped; bounded by the settle pass cap) — the interruptible-reflow
//     analogue.
//   - "final" (Final=true): the fully-settled render, byte-identical to
//     RenderWithLinks for the same input.
//
// Every frame carries an INDEPENDENT image snapshot (a fresh canvas per frame),
// so a consumer may retain it after onFrame returns.
package engine

import (
	"context"
	"hash/fnv"
	"image"
	"math"

	"github.com/go-webengine/engine/layout"
	"github.com/go-webengine/engine/paint"
)

// ProgressiveFrame is one staged snapshot delivered to a RenderProgressive
// callback. Img is an independent allocation; Links matches RenderWithLinks's
// shape; Stage is "initial" | "settle" | "final"; Final is true exactly once,
// on the last frame.
type ProgressiveFrame struct {
	Img   *image.RGBA
	Links []Link
	Info  *RenderInfo
	Stage string
	Final bool
}

// RenderProgressive fetches rawurl and renders it progressively, invoking
// onFrame for each staged frame. It runs the same cascade → JS settle → layout
// → paint pipeline as RenderWithLinks; the Final frame's image, links and
// RenderInfo are identical to what RenderWithLinks returns for the same input.
// onFrame is always called at least once and the last call has Final=true.
func (e *Engine) RenderProgressive(ctx context.Context, rawurl string, viewport image.Rectangle, onFrame func(ProgressiveFrame)) (*RenderInfo, error) {
	doc, err := e.Fetch(ctx, rawurl)
	if err != nil {
		return nil, err
	}
	return e.RenderDocumentProgressive(ctx, doc, viewport, onFrame)
}

// RenderDocumentProgressive renders an already-fetched Document progressively.
// See RenderProgressive.
func (e *Engine) RenderDocumentProgressive(ctx context.Context, doc *Document, viewport image.Rectangle, onFrame func(ProgressiveFrame)) (*RenderInfo, error) {
	fonts := paint.NewFonts()
	vpW, vpH := viewportSize(viewport)

	var (
		lastSig  uint64
		haveLast bool
	)
	// emit paints the CURRENT pass into a FRESH canvas, gathers its links and
	// delivers one frame. Intermediate "settle" frames whose geometry is
	// unchanged from the previous emitted frame are skipped; "initial" and
	// "final" always emit (so even a trivial page yields initial+final).
	emit := func(stage string, rp *renderPass, final bool) {
		sig := geomSig(rp)
		if stage == "settle" && haveLast && sig == lastSig {
			return
		}
		lastSig, haveLast = sig, true
		img := newCanvas(doc, rp, viewport, vpW)
		paint.PaintFull(img, rp.box, fonts, rp.imgs, rp.bgImgs)
		onFrame(ProgressiveFrame{
			Img:   img,
			Links: LinksFromBox(rp.box, doc.URL),
			Info:  renderInfo(doc, rp),
			Stage: stage,
			Final: final,
		})
	}

	rp := e.renderCoreStaged(ctx, doc, vpW, vpH, fonts, func(stage string, rp *renderPass) {
		emit(stage, rp, false)
	})
	// The fully-settled frame. Byte-identical to RenderWithLinks (same rp, same
	// newCanvas + PaintFull + LinksFromBox), always emitted and always last.
	emit("final", rp, true)
	return renderInfo(doc, rp), nil
}

// geomSig is a digest of a pass's visible geometry: the page height plus every
// box's border-box rect and every inline item's position/size in document
// order. Two passes with the same signature paint the same frame, so an
// intermediate settle pass that did not move anything is deduped.
func geomSig(rp *renderPass) uint64 {
	h := fnv.New64a()
	var b8 [8]byte
	put := func(v float64) {
		// Quantise to whole pixels (the paint resolution) so sub-pixel jitter that
		// cannot change the rendered image does not defeat the dedup.
		u := uint64(int64(math.Round(v)))
		for i := 0; i < 8; i++ {
			b8[i] = byte(u >> (8 * i))
		}
		_, _ = h.Write(b8[:])
	}
	put(rp.height)
	var walk func(b *layout.Box)
	walk = func(bx *layout.Box) {
		if bx == nil {
			return
		}
		put(bx.X)
		put(bx.Y)
		put(bx.W)
		put(bx.H)
		for _, ln := range bx.Lines {
			for _, it := range ln.Items {
				put(it.X)
				put(it.Y)
				put(it.Width)
				put(it.LineHeight)
			}
		}
		for _, c := range bx.Children {
			walk(c)
		}
	}
	walk(rp.box)
	return h.Sum64()
}
