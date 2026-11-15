// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math"
	"strconv"
	"strings"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// tableRow is one row and its cells (both with resolved styles), plus each
// cell's starting column index, column span (colspan="N", default 1), and
// row span (rowspan="N", default 1). colStart can diverge from a cell's raw
// position in cells: a row mixing a spanning cell with plain ones — e.g.
// news.ycombinator.com's own real markup, a title row of 3 plain cells
// followed by a subtext row of `<td colspan=2></td><td class=subtext>...</td>`
// — must still align its non-spanning cells with the SAME columns every
// other row uses, and so must a row whose earlier sibling row left a
// rowspan>1 cell still occupying one of its columns.
type tableRow struct {
	node     *dom.Node
	style    *css.Style
	cells    []*dom.Node
	colStart []int
	colSpan  []int
	rowSpan  []int
}

// cellColSpan returns a table cell's colspan attribute, defaulting to 1 for a
// missing, non-numeric, or non-positive value (the HTML spec's own "invalid
// value default" for this attribute).
func cellColSpan(cell *dom.Node) int {
	v, ok := cell.Attribute("colspan")
	if !ok {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// cellRowSpan is cellColSpan for the rowspan attribute — same defaulting
// rule, same HTML spec provision. Confirmed real trigger: Wikipedia's own
// chemical-element infobox template (`{{Infobox element}}`, e.g. the Carbon
// article) uses rowspan repeatedly for its multi-row property groupings —
// without it, every cell below a spanning one shifts one column left of
// where a real browser puts it, visibly misaligning the whole infobox.
func cellRowSpan(cell *dom.Node) int {
	v, ok := cell.Attribute("rowspan")
	if !ok {
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// column is one table column's width constraints, gathered from its
// single-span cells (CSS 2.1 §17.5.2.2).
type column struct {
	// min is the widest unbreakable unit in the column, max the widest line;
	// a cell's declared definite width counts toward both, so a fixed column
	// has min == max unless a word is longer than the declaration.
	min, max float64
	// pct is the largest percentage width declared on a cell, 0..1 (0: none).
	pct float64
	// fixed marks a column some cell sized with a definite width: it is
	// handed exactly that width while the table can afford it, and only
	// shares in surplus space when no auto column is left to take it.
	fixed bool
}

// table lays out a table box with the automatic table layout of CSS 2.1
// §17.5.2.2: each column collects the minimum and maximum widths of its
// cells (see column), and the table's content width is distributed so that
// no column is ever narrower than its minimum — a column that cannot hold
// its longest word would let that word run into the next column. Surplus
// beyond every maximum goes to the auto columns in proportion to their
// maximums, which is also what the earlier "scale the max-content widths to
// fill the table" did whenever the table was wide enough for all of them, so
// that case is unchanged. Each row's height is the tallest cell. Returns the
// content bottom y.
//
// A display:table box with no real table-row/table-cell descendant at all —
// collectRows returns none — falls back to plain block-in-flow layout of its
// children (blockOrInlineContents) rather than this function dropping the
// whole box (returning top unchanged, as if it had no content whatsoever).
// CSS's own anonymous-table-object generation would wrap such children in
// implicit rows/cells instead; this engine doesn't implement that synthesis,
// but MediaWiki's own thumbnail-figure markup relies on exactly this gap
// being handled somehow: `figure[typeof~='mw:File/Thumb']{display:table}`
// wraps a plain `display:block` `.mw-file-description` anchor (the image) and
// a `figcaption` (itself a `display:table-caption` this engine doesn't
// recognise either, so it stays the block its UA default already set it to)
// — NEITHER child is ever a table-row or table-cell. Before this fallback,
// the whole figure (image AND caption) rendered as empty space: confirmed
// live on en.wikipedia.org's "Go (programming language)" article, whose
// "Branding and styling" section lost its gopher-mascot drawing entirely.
func (l *layouter) table(box *Box, node *dom.Node, st *css.Style, cx, cw, top float64, b *bfc) float64 {
	rows := l.collectRows(node)
	if len(rows) == 0 {
		return l.blockOrInlineContents(box, node, st, cx, cw, top, b)
	}
	ncols := 0
	for _, r := range rows {
		for j := range r.cells {
			if end := r.colStart[j] + r.colSpan[j]; end > ncols {
				ncols = end
			}
		}
	}
	if ncols == 0 {
		return top
	}

	cols := l.tableColumns(rows, ncols)
	// border-spacing (see css.Style.BorderSpacingH/V's own doc comment for
	// scope): a gap of BorderSpacingH BEFORE the first column, BETWEEN every
	// pair of columns, and AFTER the last — ncols+1 gaps total, taken out of
	// the width columns are distributed over, matching the separate-borders
	// model's own box accounting. BorderSpacingV works identically down the
	// rows. Zero for a table that never sets border-spacing, so this is a
	// no-op reproducing the exact previous flush-column/row layout.
	hs, vs := st.BorderSpacingH, st.BorderSpacingV
	avail := cw - hs*float64(ncols+1)
	if avail < 0 {
		avail = 0
	}
	colW := distributeColumns(cols, avail)
	colX := make([]float64, ncols)
	acc := cx + hs
	for j := range colW {
		colX[j] = acc
		acc += colW[j] + hs
	}

	return l.tableRows(box, rows, cx, cw, top, hs, vs, colW, colX)
}

// tableColumns gathers each column's width constraints. A colspan>1 cell is
// excluded — distributing its content need across the columns it spans is the
// full spec algorithm's job, past this table layout's scope — but it still
// OCCUPIES those columns (see tableRows), so it does not shift a later plain
// cell in the same row out of alignment with the other rows' columns.
func (l *layouter) tableColumns(rows []tableRow, ncols int) []column {
	cols := make([]column, ncols)
	for _, r := range rows {
		for j, cell := range r.cells {
			if r.colSpan[j] != 1 {
				continue
			}
			cs := l.cellStyle(cell)
			m := cs.Margin.Left + cs.Margin.Right
			col := &cols[r.colStart[j]]
			max := l.preferredWidth(cell, cs) + m
			// The cell's own declared width is a floor under both widths, not
			// a ceiling on the minimum: "if the specified width of the cell
			// is greater than the minimum content width, it becomes the
			// minimum cell width" (§17.5.2.2) — a word longer than the
			// declaration still wins, so it is measured with the width
			// treated as auto and the declaration applied afterwards.
			content := *cs
			content.Width = css.Length{Auto: true}
			min := l.minContentWidth(cell, &content) + m
			switch {
			case cs.Width.IsPercent:
				if cs.Width.Percent > col.pct {
					col.pct = cs.Width.Percent
				}
			case !cs.Width.Auto:
				col.fixed = true
				if max > min {
					min = max
				}
			}
			if max < min {
				max = min
			}
			if min > col.min {
				col.min = min
			}
			if max > col.max {
				col.max = max
			}
		}
	}
	return cols
}

// tableMaxContent is the max-content width of the table formed by node's rows
// (sum of the columns' maximum widths plus border-spacing), 0 when it has none.
func (l *layouter) tableMaxContent(node *dom.Node, st *css.Style) float64 {
	rows := l.collectRows(node)
	ncols := 0
	for _, r := range rows {
		for j := range r.cells {
			if end := r.colStart[j] + r.colSpan[j]; end > ncols {
				ncols = end
			}
		}
	}
	if ncols == 0 {
		return 0
	}
	w := st.BorderSpacingH * float64(ncols+1)
	for _, c := range l.tableColumns(rows, ncols) {
		w += c.max
	}
	return w
}

// isAnonTableParent reports whether node is a non-table box (display:block)
// whose element children are all table rows or row groups, with no text of
// its own: CSS 2.1 §17.2.1 wraps such misparented rows in an anonymous table,
// so they keep table layout. Mixed content (other block children, text) is not
// handled and stays ordinary block flow.
func (l *layouter) isAnonTableParent(node *dom.Node, st *css.Style) bool {
	if node.Type != dom.Element || st.Display != css.DisplayBlock {
		return false
	}
	found := false
	for _, c := range l.renderedChildren(node) {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs == nil || cs.Display == css.DisplayNone {
			continue
		}
		if cs.Display != css.DisplayTableRow && cs.Display != css.DisplayTableRowGroup {
			return false
		}
		found = true
	}
	return found && !l.hasDirectText(node)
}

// rowspanDeficit is a rowSpan>1 cell whose own natural height was computed in
// tableRows' first pass but could not yet be reconciled against the rows it
// spans, since later rows' own heights weren't known yet.
type rowspanDeficit struct {
	rowIdx   int // the cell's own starting row
	span     int // rowspan count
	naturalH float64
}

// tableRows places the rows of a table whose column widths are colW at colX,
// in two passes. Pass 1 computes every row's own height (tallest ordinary,
// rowSpan==1 cell, or the row's own CSS height) while deferring any
// rowSpan>1 cell's natural height into rowspanDeficit — a spanning cell
// cannot grow ITS OWN starting row without also shifting every other
// ordinary cell beside it; MDN's own HTML table spec examples agree the
// usual behaviour browsers converge on is to grow the row the span ENDS on
// instead, so any height a span needs beyond what its spanned rows already
// give it (CSS 2.1 doesn't define this split; this is a disclosed
// simplification versus a real browser's own proportional distribution
// across the spanned rows) is added there once every row's base height is
// known. Pass 2 places every cell: an ordinary cell within its own row's
// slot exactly as before rowspan existed; a rowSpan>1 cell across the SUM of
// its spanned rows' heights plus their internal border-spacing gaps —
// mirroring exactly how a colspan>1 cell already sums colW across the
// columns it spans, just down rows instead of across columns.
func (l *layouter) tableRows(box *Box, rows []tableRow, cx, cw, top, hs, vs float64, colW, colX []float64) float64 {
	rowH := make([]float64, len(rows))
	cellBoxes := make([][]*Box, len(rows))
	var deficits []rowspanDeficit
	for i, r := range rows {
		boxes := make([]*Box, len(r.cells))
		var h float64
		for j, cell := range r.cells {
			cs := l.cellStyle(cell)
			bw := cs.Border.Widths()
			hEdges := bw.Left + bw.Right + cs.Padding.Left + cs.Padding.Right
			var spanW float64
			for k := 0; k < r.colSpan[j]; k++ {
				spanW += colW[r.colStart[j]+k]
			}
			if r.colSpan[j] > 1 {
				// The span's own internal gaps belong to it, same as a
				// plain cell's single column already has none to add.
				spanW += hs * float64(r.colSpan[j]-1)
			}
			contentW := spanW - hEdges - cs.Margin.Left - cs.Margin.Right
			cbox := l.layoutIsolated(cell, cs, contentW)
			boxes[j] = cbox
			cellH := cbox.H + cs.Margin.Top + cs.Margin.Bottom
			if r.rowSpan[j] > 1 {
				deficits = append(deficits, rowspanDeficit{rowIdx: i, span: r.rowSpan[j], naturalH: cellH})
				continue
			}
			if cellH > h {
				h = cellH
			}
		}
		cellBoxes[i] = boxes
		// A row's own CSS height is a minimum (CSS 2.1 §17.5.3), not just its
		// cells' content. Confirmed live: news.ycombinator.com's spacer rows are
		// `<tr class="spacer" style="height:5px">`, which laid out at 0px.
		if rs := r.style; rs != nil && !rs.Height.Auto && !rs.Height.IsPercent && rs.Height.Px > h {
			h = rs.Height.Px
		}
		rowH[i] = h
	}
	for _, d := range deficits {
		end := d.rowIdx + d.span
		if end > len(rowH) {
			// Malformed markup: a rowspan reaching past the table's last
			// row. Clamp to what actually exists rather than indexing past it.
			end = len(rowH)
		}
		var spanH float64
		for i := d.rowIdx; i < end; i++ {
			spanH += rowH[i]
		}
		spanH += vs * float64(end-d.rowIdx-1)
		if d.naturalH > spanH {
			rowH[end-1] += d.naturalH - spanH
		}
	}

	rowY := make([]float64, len(rows))
	y := top + vs
	for i, h := range rowH {
		rowY[i] = y
		y += h + vs
	}

	for i, r := range rows {
		rowBox := &Box{Node: r.node, Style: r.style, X: cx, Y: rowY[i], W: cw, ContentX: cx, ContentY: rowY[i], ContentW: cw}
		for j, cbox := range cellBoxes[i] {
			cs := l.cellStyle(r.cells[j])
			slotH := rowH[i]
			if r.rowSpan[j] > 1 {
				end := i + r.rowSpan[j]
				if end > len(rowH) {
					end = len(rowH)
				}
				slotH = 0
				for k := i; k < end; k++ {
					slotH += rowH[k]
				}
				slotH += vs * float64(end-i-1)
			}
			naturalH := cbox.H
			translateBox(cbox, (colX[r.colStart[j]]+cs.Margin.Left)-cbox.X, (rowY[i]+cs.Margin.Top)-cbox.Y)
			// vertical-align:middle/bottom shifts the cell's own CONTENT down
			// within its slot — the box's own outer rect (its
			// background/border, painted from cbox.X/Y/W/H directly) still
			// spans the FULL slot height regardless, matching a real
			// browser's own table box model, where vertical-align never
			// affects a cell's own background fill, only where its content
			// sits inside it. Only top/middle/bottom move anything; every
			// other keyword (baseline and its own row-wide first-line
			// alignment, text-top/text-bottom/sub/super) has no confirmed
			// real trigger on a table cell and keeps the pre-existing
			// top-aligned behaviour, same as this field's own general
			// doc comment already discloses for non-table contexts.
			if slack := slotH - cs.Margin.Top - cs.Margin.Bottom - naturalH; slack > 0 {
				var offset float64
				switch cs.VerticalAlign {
				case css.VAlignMiddle:
					offset = slack / 2
				case css.VAlignBottom:
					offset = slack
				}
				if offset > 0 {
					translateBox(cbox, 0, offset)
					cbox.Y -= offset
					cbox.ContentY -= offset
				}
			}
			// Stretch the cell to its slot height (content stays aligned per
			// the offset just applied above, top by default).
			cbox.H = slotH - cs.Margin.Top - cs.Margin.Bottom
			rowBox.Children = append(rowBox.Children, cbox)
		}
		rowBox.H = rowH[i]
		rowBox.ContentH = rowH[i]
		box.Children = append(box.Children, rowBox)
	}
	return y
}

// collectRows gathers the table's rows, descending into row groups. rowOccupancy
// is shared across every row in the table (not reset per row group — a
// rowspan cell in the last row of a <thead> occupying a column in the first
// row of <tbody> is real markup this engine has no reason to special-case
// against) so a rowspan>1 cell's occupied column is correctly skipped by
// every later row's own makeRow call, however many rows down it reaches.
func (l *layouter) collectRows(node *dom.Node) []tableRow {
	var rows []tableRow
	occ := map[int]int{} // column -> how many MORE rows after the current one it stays blocked
	for _, c := range l.renderedChildren(node) {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs == nil || cs.Display == css.DisplayNone {
			continue
		}
		switch cs.Display {
		case css.DisplayTableRow:
			rows = append(rows, l.makeRow(c, cs, occ))
		case css.DisplayTableRowGroup:
			for _, r := range l.renderedChildren(c) {
				if r.Type != dom.Element {
					continue
				}
				rs := l.sm[r]
				if rs != nil && rs.Display == css.DisplayTableRow {
					rows = append(rows, l.makeRow(r, rs, occ))
				}
			}
		}
	}
	return rows
}

// makeRow collects the table-cell children of a row, tracking each one's
// starting column index by advancing a running cursor by the PRECEDING
// cells' own colspan — not by cell position — so a colspan>1 cell shifts
// every cell after it into the correct later column. occ carries forward
// which columns an EARLIER row's rowspan>1 cell still occupies, as "rows
// after THIS one that stay blocked": consulted as-is while placing this
// row's own cells (so a column an earlier row is still blocking is skipped
// in THIS row too), extended for any NEW rowspan>1 cell this row starts,
// and only THEN decremented — and only the entries that already existed
// when this row began, never ones this same row just added, which must
// survive unconsumed into the row right after this one.
func (l *layouter) makeRow(node *dom.Node, st *css.Style, occ map[int]int) tableRow {
	existing := make(map[int]bool, len(occ))
	for col := range occ {
		existing[col] = true
	}
	row := tableRow{node: node, style: st}
	col := 0
	for _, c := range l.renderedChildren(node) {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs != nil && cs.Display == css.DisplayTableCell {
			for occ[col] > 0 {
				col++
			}
			span := cellColSpan(c)
			rspan := cellRowSpan(c)
			row.cells = append(row.cells, c)
			row.colStart = append(row.colStart, col)
			row.colSpan = append(row.colSpan, span)
			row.rowSpan = append(row.rowSpan, rspan)
			if rspan > 1 {
				for k := 0; k < span; k++ {
					occ[col+k] = rspan - 1
				}
			}
			col += span
		}
	}
	for col := range existing {
		if occ[col] <= 1 {
			delete(occ, col)
		} else {
			occ[col]--
		}
	}
	return row
}

// cellStyle returns a cell node's style, falling back to a default table-cell
// style when absent.
func (l *layouter) cellStyle(cell *dom.Node) *css.Style {
	if cs := l.sm[cell]; cs != nil {
		return cs
	}
	return &css.Style{Display: css.DisplayTableCell, Width: css.Length{Auto: true},
		MinWidth: css.Length{Auto: true}, MaxWidth: css.Length{Auto: true},
		Height: css.Length{Auto: true}, ColumnWidth: css.Length{Auto: true}}
}

// distributeColumns turns the columns' constraints into widths summing to W
// (CSS 2.1 §17.5.2.2, refined the way browsers do). Percentage columns are
// served first — their share of W, never less than their minimum — and the
// rest of the width goes to the remaining columns: each gets its minimum when
// the table cannot afford more (the table then overflows, as a browser's
// does); between the minimums and the maximums every column grows from its
// minimum in proportion to how much it can still grow; beyond every maximum
// the surplus goes to the auto columns in proportion to their maximums, to
// the fixed ones only when no auto column is left to take it, and in equal
// shares when no column has any content to be proportional to.
func distributeColumns(cols []column, W float64) []float64 {
	w := make([]float64, len(cols))
	rest := W
	var free []int // the non-percentage columns
	for j, c := range cols {
		if c.pct > 0 {
			w[j] = math.Max(c.pct*W, c.min)
			rest -= w[j]
			continue
		}
		free = append(free, j)
	}
	if len(free) == 0 {
		// Every column is a percentage: what they leave over is shared in
		// proportion to their widths, so the table still fills W. (Each
		// width is at least its share of a positive W, so their sum is
		// positive whenever anything is left over.)
		if rest > 0 {
			var sum float64
			for _, v := range w {
				sum += v
			}
			for j := range w {
				w[j] += rest * w[j] / sum
			}
		}
		return w
	}
	var summin, summax float64
	for _, j := range free {
		summin += cols[j].min
		summax += cols[j].max
	}
	switch {
	case rest <= summin:
		for _, j := range free {
			w[j] = cols[j].min
		}
	case rest < summax:
		f := (rest - summin) / (summax - summin)
		for _, j := range free {
			w[j] = cols[j].min + (cols[j].max-cols[j].min)*f
		}
	default:
		extra := rest - summax
		var recv []int
		var base float64
		for _, j := range free {
			w[j] = cols[j].max
			if !cols[j].fixed && cols[j].max > 0 {
				recv = append(recv, j)
				base += cols[j].max
			}
		}
		if len(recv) == 0 {
			for _, j := range free {
				if cols[j].max > 0 {
					recv = append(recv, j)
					base += cols[j].max
				}
			}
		}
		if len(recv) == 0 {
			for _, j := range free {
				w[j] += extra / float64(len(free))
			}
			return w
		}
		if base == summax {
			// Every content column receives: scaling is the same arithmetic
			// the pre-§17.5.2.2 layout used, kept so identical inputs give
			// bit-identical widths.
			scale := rest / summax
			for _, j := range recv {
				w[j] = cols[j].max * scale
			}
			return w
		}
		for _, j := range recv {
			w[j] += extra * cols[j].max / base
		}
	}
	return w
}
