// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// floatRect is a placed float's margin box in absolute page coordinates.
type floatRect struct {
	x, y, w, h float64
}

func (r floatRect) right() float64  { return r.x + r.w }
func (r floatRect) bottom() float64 { return r.y + r.h }

// floatCtx tracks placed left/right floats so that line boxes and later floats
// avoid them. It is a single context in absolute coordinates for the whole
// document, which reproduces the common "content flows beside a floated box"
// layout without per-BFC bookkeeping.
type floatCtx struct {
	lefts  []floatRect
	rights []floatRect
}

// overlaps reports whether a float vertically intersects the band [yTop,yBot).
func overlaps(r floatRect, yTop, yBot float64) bool {
	return r.y < yBot && r.bottom() > yTop
}

// available returns the left and right content edges available in the vertical
// band [yTop,yBot) within the region [regionLeft,regionRight], after subtracting
// any floats that intrude on that band.
func (f *floatCtx) available(yTop, yBot, regionLeft, regionRight float64) (left, right float64) {
	left, right = regionLeft, regionRight
	for _, r := range f.lefts {
		if overlaps(r, yTop, yBot) && r.right() > left {
			left = r.right()
		}
	}
	for _, r := range f.rights {
		if overlaps(r, yTop, yBot) && r.x < right {
			right = r.x
		}
	}
	if right < left {
		right = left
	}
	return left, right
}

// nextEdge returns the smallest float bottom strictly below y (the next point
// at which more horizontal space frees up), or y if no float extends past y.
func (f *floatCtx) nextEdge(y, regionLeft, regionRight float64) float64 {
	best := math.Inf(1)
	consider := func(rs []floatRect) {
		for _, r := range rs {
			if r.bottom() > y && r.bottom() < best {
				best = r.bottom()
			}
		}
	}
	consider(f.lefts)
	consider(f.rights)
	if math.IsInf(best, 1) {
		return y
	}
	return best
}

// clearY returns the y below which no relevant float remains, for a clear value.
func (f *floatCtx) clearY(c css.Clear, y float64) float64 {
	out := y
	bump := func(rs []floatRect) {
		for _, r := range rs {
			if r.bottom() > out {
				out = r.bottom()
			}
		}
	}
	if c == css.ClearLeft || c == css.ClearBoth {
		bump(f.lefts)
	}
	if c == css.ClearRight || c == css.ClearBoth {
		bump(f.rights)
	}
	return out
}

// bottom returns the lowest bottom edge of any float (0 when there are none).
func (f *floatCtx) bottom() float64 {
	var b float64
	for _, r := range append(append([]floatRect{}, f.lefts...), f.rights...) {
		if r.bottom() > b {
			b = r.bottom()
		}
	}
	return b
}

// add records a placed float on the given side.
func (f *floatCtx) add(side css.Float, r floatRect) {
	if side == css.FloatRight {
		f.rights = append(f.rights, r)
	} else {
		f.lefts = append(f.lefts, r)
	}
}

// findSlot returns the (x,y) top-left of a float margin box of size outerW×outerH
// on the given side, at or below yTop, within [regionLeft,regionRight], that does
// not overlap existing floats.
func (f *floatCtx) findSlot(side css.Float, yTop, outerW, outerH, regionLeft, regionRight float64) (x, y float64) {
	// Scan downward from yTop, stepping to each float's bottom edge, until a band
	// is wide enough for the box.
	best := yTop
	for {
		left, right := f.available(best, best+outerH, regionLeft, regionRight)
		// Accept the band once it is wide enough for the box. A band that is too
		// narrow — including one fully blocked by earlier floats (right-left <= 0)
		// — is NOT a valid slot: per CSS a float that does not fit drops to a lower
		// band rather than being placed at the blocked edge (which, for a
		// full-width left float behind another, is the container's right edge and
		// pushes the box off-screen). The nextEdge fallback below still terminates
		// the scan for a box wider than the region (no float can ever free enough
		// room, so it lands at the current left/right edge below all floats).
		if right-left >= outerW {
			if side == css.FloatRight {
				return right - outerW, best
			}
			return left, best
		}
		// Move to the next float edge below the current band.
		ny := f.nextEdge(best, regionLeft, regionRight)
		if ny <= best {
			if side == css.FloatRight {
				return right - outerW, best
			}
			return left, best
		}
		best = ny
	}
}

// placeFloat lays out a floated element out of normal flow, positions it against
// existing floats, records it in the float context and appends it to parent.
func (l *layouter) placeFloat(parent *Box, node *dom.Node, st *css.Style, cx, cw float64, b *bfc) {
	bw := st.Border.Widths()
	hExtra := bw.Left + bw.Right + st.Padding.Left + st.Padding.Right

	var fcw float64
	if st.Width.Auto {
		avail := cw - st.Margin.Left - st.Margin.Right - hExtra
		if avail < 0 {
			avail = 0
		}
		fcw = math.Min(l.preferredWidth(node, st), avail)
	} else {
		fcw = st.Width.Resolve(cw)
		if st.BoxSizing == css.BorderBox {
			fcw -= hExtra
		}
	}
	fcw = clampWidth(fcw, st, cw, hExtra)
	if fcw < 0 {
		fcw = 0
	}

	// Lay the float out in isolation (its own float context, discarded) at a
	// local origin, with a fixed content width and no outer margins.
	saved := l.floats
	l.floats = &floatCtx{}
	clone := *st
	clone.Width = css.Length{Px: fcw}
	clone.MinWidth = css.Length{Auto: true}
	clone.MaxWidth = css.Length{Auto: true}
	clone.BoxSizing = css.ContentBox
	clone.Float = css.FloatNone
	clone.Margin = css.Edges{}
	clone.MarginLeftAuto, clone.MarginRightAuto = false, false
	fb := &bfc{}
	box := l.place(node, &clone, 0, fcw+hExtra, fb)
	fb.commit()
	l.floats = saved

	outerW := box.W + st.Margin.Left + st.Margin.Right
	outerH := box.H + st.Margin.Top + st.Margin.Bottom

	yTop := b.y + math.Max(b.carry, 0)
	x, y := l.floats.findSlot(st.Float, yTop, outerW, outerH, cx, cx+cw)

	translateBox(box, (x+st.Margin.Left)-box.X, (y+st.Margin.Top)-box.Y)
	l.floats.add(st.Float, floatRect{x: x, y: y, w: outerW, h: outerH})
	box.Float = st.Float
	parent.Children = append(parent.Children, box)
}

// preferredWidth estimates the max-content width of a node's subtree: the widest
// unwrapped inline run, or the widest block child, plus horizontal edges.
func (l *layouter) preferredWidth(node *dom.Node, st *css.Style) float64 {
	bw := st.Border.Widths()
	edges := bw.Left + bw.Right + st.Padding.Left + st.Padding.Right
	if node.Type == dom.Element && node.Tag == "img" {
		w, _ := l.imageSize(node)
		return w + edges
	}
	// A definite (non-auto, non-percentage) width is the box's max-content
	// contribution outright — CSS doesn't fall back to measuring children once
	// the author has fixed the width, even when those children carry no text of
	// their own (a bar-chart fill, a spacer div). Skipping this check made such
	// a box report a 0 preferred width, which collapsed its table column to 0
	// and let the next column's cells overlap it. This takes priority over the
	// flex-row sum below: a flex row with its own explicit width doesn't need
	// its children measured either.
	if !st.Width.Auto && !st.Width.IsPercent {
		if st.BoxSizing == css.BorderBox {
			return st.Width.Px
		}
		return st.Width.Px + edges
	}
	// A flex ROW lays its items side by side, so its max-content main size is the
	// SUM of the items' outer main sizes plus the inter-item gaps — not the max of
	// them (the block / flex-column case handled below). Without this a
	// content-sized flex row (itself a flex item, float or inline-block) is
	// under-sized to its widest child, and the inner row then has negative free
	// space that flex-shrink collapses the items with, overlapping them
	// horizontally (the GitHub repo-nav "Code / Issues / Pull requests / …"
	// jumble).
	// This only covers a flex row with at least one ELEMENT child: a flex
	// container whose content is bare text (no wrapping element — e.g.
	// `<h1 style="display:flex">React</h1>`, confirmed live on react.dev's
	// hero heading and its two CTA buttons) forms a single anonymous flex
	// item from that text per spec, which this sum-of-element-children loop
	// cannot see at all (it skips every non-Element child), so n stays 0 and
	// this returned a bare `0 + edges` — collapsing the element to zero
	// width, which then collapses its actual layout (laid out at that zero
	// content width) to zero height too, since there is no room left to wrap
	// even one line of text. Falling through to the inline-measurement path
	// below (the same one a plain, non-flex text-only element already uses)
	// measures the text correctly.
	if node.Type == dom.Element && st.Display == css.DisplayFlex && st.FlexDirection == css.FlexRow {
		var sum float64
		n := 0
		for _, c := range l.renderedChildren(node) {
			if c.Type != dom.Element {
				continue
			}
			cs := l.sm[c]
			if cs == nil || cs.Display == css.DisplayNone {
				continue
			}
			sum += l.preferredWidth(c, cs) + cs.Margin.Left + cs.Margin.Right
			n++
		}
		if n > 0 {
			if n > 1 {
				sum += gapLen(st.ColumnGap) * float64(n-1)
			}
			return sum + edges
		}
	}
	if l.hasBlockLevelChild(node) {
		var max float64
		for _, c := range l.renderedChildren(node) {
			if c.Type != dom.Element {
				continue
			}
			cs := l.sm[c]
			if cs == nil || cs.Display == css.DisplayNone {
				continue
			}
			w := l.preferredWidth(c, cs) + cs.Margin.Left + cs.Margin.Right
			if w > max {
				max = w
			}
		}
		return max + edges
	}
	// Inline: max-content is everything on one line.
	items := l.collectInline(node, st, st.WhiteSpace == css.WSPre)
	var line float64
	for i, it := range items {
		// A BlockBreak sentinel (a promoted block-level element, see
		// InlineItem.BlockBreak) carries no Width/SpaceBefore of its own —
		// its content gets its own independent box, not part of this
		// max-content line estimate.
		if it.LineBreak || it.BlockBreak != nil {
			continue
		}
		if i > 0 {
			line += it.SpaceBefore
		}
		line += it.Width
	}
	return line + edges
}

// translateBox shifts a box subtree (and its lines/items) by (dx,dy).
func translateBox(box *Box, dx, dy float64) {
	if box == nil || (dx == 0 && dy == 0) {
		return
	}
	box.X += dx
	box.Y += dy
	box.ContentX += dx
	box.ContentY += dy
	// A list-item's marker is computed once, in attachMarker, from the box's
	// ContentX/first-line Y at the time layout ran — absolute coordinates, like
	// everything else in this package. Any box translated AFTER that (a flex
	// row/column item repositioned from its temporary local-origin layout,
	// flex.go:251/359; a grid item, grid.go; a table cell, table.go; a float,
	// placeFloat above) left its marker's X/Y stuck at the PRE-translation
	// position — observed live on caniuse.com: a `display:flex` 3-column
	// section's numbered list (`<ol style="list-style:decimal">`, a flex
	// child) had every marker's absolute X computed against its local,
	// near-zero pre-flex-placement ContentX, so after the real ~370px flex
	// shift the "1."/"2."/… markers rendered off in the gutter of column one
	// instead of beside their own list items — invisible in this specific
	// page's cramped indent, but wrong regardless of what happens to sit
	// there.
	if box.Marker != nil {
		box.Marker.X += dx
		box.Marker.Y += dy
	}
	for _, line := range box.Lines {
		line.X += dx
		line.Y += dy
		for _, it := range line.Items {
			it.X += dx
			it.Y += dy
		}
	}
	for _, ch := range box.Children {
		translateBox(ch, dx, dy)
	}
}
