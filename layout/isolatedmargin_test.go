// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestIsolatedBoxAbsorbsChildMarginCollapsingThrough covers a real, confirmed
// defect: a block child's margin that collapses through an ordinary nested
// block all the way to the edge of an ISOLATED layout (layoutIsolated, used
// for table cells, flex/grid items, and replaced-element sizing) has nowhere
// further to go — CSS 2.1 §8.3.1's own collapsing rule stops at a box that
// establishes its own block formatting context — and must land inside that
// box's own height, both leading and trailing.
//
// Confirmed real trigger: news.ycombinator.com's own vote-arrow icon,
// `.votearrow{width:10px;height:10px;margin:3px 2px 6px}` inside a table
// cell's `<center><a>` wrapper (an <a> is not normally a block-margin
// context, but promoting a block child out of an inline ancestor — the
// BlockBreak mechanism, see layout.go's own "block in inline" handling —
// reaches this same isolated-layout code path). This engine rendered the
// cell 10px tall; a live Chromium comparison on the real page (viewport
// 1200px, cellpadding="0") gives 19px (3 top + 10 content + 6 bottom,
// exactly the margin box) — confirmed by extracting the real row's own
// outerHTML plus its real stylesheet into a standalone reproduction, not
// guessed at.
func TestIsolatedBoxAbsorbsChildMarginCollapsingThrough(t *testing.T) {
	src := `<html><body style="margin:0"><table cellpadding="0" cellspacing="0"><tr>` +
		`<td valign="top" style="padding:0"><center><a href="#">` +
		`<div id="va" style="width:10px;height:10px;margin:3px 2px 6px"></div>` +
		`</a></center></td>` +
		`</tr></table></body></html>`
	box := layoutHTML(t, src, 400)
	tbl := findBox(box, "table")
	if tbl == nil || len(tbl.Children) == 0 || len(tbl.Children[0].Children) == 0 {
		t.Fatal("no table row/cell laid out")
	}
	cell := tbl.Children[0].Children[0]
	assertF(t, "cell.H (3 top + 10 content + 6 bottom margin box)", cell.H, 19)

	// The div's own box is untouched by the fold: it still sits at its own
	// natural position and size, only the ANCESTOR cell's height changed.
	div := findBoxByID(box, "va")
	if div == nil {
		t.Fatal("no #va box")
	}
	assertF(t, "div.H", div.H, 10)
}

// TestIsolatedBoxLeadingMarginFoldDoesNotAffectInlineContent covers the
// scope of the fix above: an isolated box's own Y can ALSO be non-zero for
// an ordinary reason having nothing to do with a collapsed-through block
// margin — a plain line of inline text's own leading/ascent positioning
// (firstContentTop falls back to box.Lines[0].Y when there are no block
// Children at all). Folding THAT into height too would double-count it.
// Caught live: doing this unconditionally inflated a plain, marginless
// `<span>1.</span>` table cell (news.ycombinator.com's own story-rank
// column) from its correct height to a visibly taller one.
func TestIsolatedBoxLeadingMarginFoldDoesNotAffectInlineContent(t *testing.T) {
	plain := `<html><body style="margin:0"><table cellpadding="0" cellspacing="0"><tr>` +
		`<td style="padding:0"><span>1.</span></td>` +
		`</tr></table></body></html>`
	plainCell := findBox(layoutHTML(t, plain, 400), "table").Children[0].Children[0]
	if len(plainCell.Lines) == 0 {
		t.Fatal("plain cell has no line box")
	}
	ln := plainCell.Lines[0]
	// If the fix had folded this line's own (ordinary, unrelated-to-margin)
	// Y offset into height too, cell.H would overshoot the line's own
	// bottom edge (ln.Y+ln.H) by roughly another ln.Y worth of doubling.
	// Unfolded, it should sit at exactly that edge (plus any bottom
	// padding/border, zero here).
	assertF(t, "plain inline cell.H", plainCell.H, ln.Y+ln.H)
}

// TestFloatAbsorbsChildMarginCollapsingThrough is the float-layout analog
// of the table-cell case above (placeFloat, floats.go): a float establishes
// its own block formatting context too (CSS 2.1 §9.4.1), so the same
// collapsing-through fix applies there, mirrored from the same real cause.
func TestFloatAbsorbsChildMarginCollapsingThrough(t *testing.T) {
	src := `<html><body style="margin:0"><div style="float:left">` +
		`<div id="va" style="width:10px;height:10px;margin:3px 2px 6px"></div>` +
		`</div></body></html>`
	box := findFloatBox(layoutHTML(t, src, 400))
	if box == nil {
		t.Fatal("no float box laid out")
	}
	assertF(t, "float.H (3 top + 10 content + 6 bottom margin box)", box.H, 19)
}
