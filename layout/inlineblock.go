// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// layoutNestedInlineBlock lays out a display:inline-block element as a
// self-contained nested box for InlineItem.NestedBox (see
// appendElementInline) — the same mechanism layoutNestedInlineFlex already
// uses for display:inline-flex: an atomic inline-level item whose CONTENT is
// laid out as a genuine nested formatting context, since inline-vs-block only
// changes how the PARENT places this box, never how it lays out its OWN
// children. Sized at its explicit width if the author gave it one —
// preferredWidth's own definite-width branch already returns that outright,
// checked before any display-specific branching — or shrink-to-fit
// otherwise, exactly like layoutNestedInlineFlex's own sizing. The isolated
// clone forces plain DisplayBlock so contents()'s default (non-flex/grid/
// table) dispatch lays out its children: a NORMAL block formatting context
// is exactly what a real inline-block's content area is.
//
// Confirmed live on en.wikipedia.org: Vector-2022's own top-nav icons
// (`<span class="vector-icon ...">`, EMPTY — no children at all) are sized
// entirely by CSS (`display:inline-block;width:1rem;height:1rem`) and
// painted via `mask-image`+`background-color` — mask-image only ever applies
// through the ordinary Box-paint path (paintBox's own hasMask check reads
// box.Style, never an InlineItem directly), which a plain inline element
// (this engine's ENTIRE previous treatment of display:inline-block — the
// value parsed correctly into css.Style but was never referenced by
// isBlockLevel or appendElementInline, so it silently fell through to plain
// `display:inline` handling) never reaches: the search and language-switcher
// icons in Wikipedia's own header rendered as invisible empty space, sized
// to nothing since an empty plain-inline span has no content to measure
// either.
func (l *layouter) layoutNestedInlineBlock(el *dom.Node, st *css.Style) *Box {
	clone := *st
	clone.Display = css.DisplayBlock
	// preferredWidth returns an outer (border+padding-inclusive) measurement
	// — see its own doc comment — while layoutIsolated wants a pure content
	// width (it lays the node out at BoxSizing:ContentBox), so the edges it
	// added have to come back off here, the same adjustment
	// layoutNestedInlineFlex already makes.
	bw := clone.Border.Widths()
	edges := bw.Left + bw.Right + clone.Padding.Left + clone.Padding.Right
	w := l.preferredWidth(el, &clone) - edges
	if w < 0 {
		w = 0
	}
	return l.layoutIsolated(el, &clone, w)
}

// inlineBlockAscent is how far an inline-block's baseline lies below the top
// of its box, which is where the line it sits on aligns it (CSS 2.1 §10.8):
// "The baseline of an 'inline-block' is the baseline of its last line box in
// the normal flow, unless it has either no in-flow line boxes or if its
// 'overflow' property has a computed value other than 'visible', in which
// case the baseline is the bottom margin edge."
//
// The engine used the bottom edge in every case, so an inline-block holding
// text sat with its text raised by its descent and half its leading
// (§10.8.1: A' = A + L/2 above the baseline, D' = D + L/2 below): in a
// code block at line-height 1.45, about a fifth of an em above the line's
// own text.
func inlineBlockAscent(box *Box, st *css.Style) float64 {
	if st.OverflowX.Clips() || st.OverflowY.Clips() {
		return box.H
	}
	if y, ok := lastLineBaseline(box); ok {
		return y - box.Y
	}
	return box.H
}

// lastLineBaseline finds the baseline of the last in-flow line box in b,
// looking into its last in-flow children (a block holding blocks), in the
// box's own coordinates.
func lastLineBaseline(b *Box) (float64, bool) {
	for i := len(b.Lines) - 1; i >= 0; i-- {
		// placeLine put every item at top + baseline - Ascent + shift (a
		// line box holds no BlockBreak sentinel: placeInlineSegments takes
		// them out before lines are placed).
		if items := b.Lines[i].Items; len(items) > 0 {
			it := items[0]
			return it.Y + it.Ascent - it.BaselineShift, true
		}
	}
	for i := len(b.Children) - 1; i >= 0; i-- {
		c := b.Children[i]
		if c.Float != css.FloatNone || c.Position == css.PositionAbsolute || c.Position == css.PositionFixed {
			continue
		}
		if y, ok := lastLineBaseline(c); ok {
			return y, true
		}
	}
	return 0, false
}
