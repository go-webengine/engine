// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"strconv"
	"strings"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// tableRow is one row and its cells (both with resolved styles), plus each
// cell's starting column index and column span (colspan="N", default 1).
// These can diverge from a cell's raw position in cells: a row mixing a
// spanning cell with plain ones — e.g. news.ycombinator.com's own real
// markup, a title row of 3 plain cells followed by a subtext row of
// `<td colspan=2></td><td class=subtext>...</td>` — must still align its
// non-spanning cells with the SAME columns every other row uses.
type tableRow struct {
	node     *dom.Node
	style    *css.Style
	cells    []*dom.Node
	colStart []int
	colSpan  []int
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

// table lays out a table box with basic auto layout: column widths are derived
// from the max-content width of the cells in each column, then scaled to fill
// the table's content width; each row's height is the tallest cell. Returns the
// content bottom y.
func (l *layouter) table(box *Box, node *dom.Node, st *css.Style, cx, cw, top float64, b *bfc) float64 {
	rows := l.collectRows(node)
	if len(rows) == 0 {
		return top
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

	// Natural (max-content) width per column. A colspan>1 cell is excluded —
	// distributing its content need across the columns it spans is the full
	// spec algorithm's job, well past this table layout's own documented
	// "basic auto layout" scope — but it still OCCUPIES those columns (see
	// the row-layout loop below), so it does not shift a later plain cell in
	// the same row out of alignment with the other rows' columns.
	natural := make([]float64, ncols)
	for _, r := range rows {
		for j, cell := range r.cells {
			if r.colSpan[j] != 1 {
				continue
			}
			cs := l.cellStyle(cell)
			w := l.preferredWidth(cell, cs) + cs.Margin.Left + cs.Margin.Right
			if col := r.colStart[j]; w > natural[col] {
				natural[col] = w
			}
		}
	}
	var sum float64
	for _, n := range natural {
		sum += n
	}
	colW := make([]float64, ncols)
	if sum <= 0 {
		for j := range colW {
			colW[j] = cw / float64(ncols)
		}
	} else {
		scale := cw / sum
		for j := range colW {
			colW[j] = natural[j] * scale
		}
	}
	colX := make([]float64, ncols)
	acc := cx
	for j := range colW {
		colX[j] = acc
		acc += colW[j]
	}

	y := top
	for _, r := range rows {
		rowBox := &Box{Node: r.node, Style: r.style, X: cx, Y: y, W: cw, ContentX: cx, ContentY: y, ContentW: cw}
		var cellBoxes []*Box
		var rowH float64
		for j, cell := range r.cells {
			cs := l.cellStyle(cell)
			bw := cs.Border.Widths()
			hEdges := bw.Left + bw.Right + cs.Padding.Left + cs.Padding.Right
			var spanW float64
			for k := 0; k < r.colSpan[j]; k++ {
				spanW += colW[r.colStart[j]+k]
			}
			contentW := spanW - hEdges - cs.Margin.Left - cs.Margin.Right
			cbox := l.layoutIsolated(cell, cs, contentW)
			cellBoxes = append(cellBoxes, cbox)
			if h := cbox.H + cs.Margin.Top + cs.Margin.Bottom; h > rowH {
				rowH = h
			}
		}
		for j, cbox := range cellBoxes {
			cs := l.cellStyle(r.cells[j])
			translateBox(cbox, (colX[r.colStart[j]]+cs.Margin.Left)-cbox.X, (y+cs.Margin.Top)-cbox.Y)
			// Stretch the cell to the row height (content stays top-aligned).
			cbox.H = rowH - cs.Margin.Top - cs.Margin.Bottom
			rowBox.Children = append(rowBox.Children, cbox)
		}
		rowBox.H = rowH
		rowBox.ContentH = rowH
		box.Children = append(box.Children, rowBox)
		y += rowH
	}
	return y
}

// collectRows gathers the table's rows, descending into row groups.
func (l *layouter) collectRows(node *dom.Node) []tableRow {
	var rows []tableRow
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
			rows = append(rows, l.makeRow(c, cs))
		case css.DisplayTableRowGroup:
			for _, r := range l.renderedChildren(c) {
				if r.Type != dom.Element {
					continue
				}
				rs := l.sm[r]
				if rs != nil && rs.Display == css.DisplayTableRow {
					rows = append(rows, l.makeRow(r, rs))
				}
			}
		}
	}
	return rows
}

// makeRow collects the table-cell children of a row, tracking each one's
// starting column index by advancing a running cursor by the PRECEDING
// cells' own colspan — not by cell position — so a colspan>1 cell shifts
// every cell after it into the correct later column.
func (l *layouter) makeRow(node *dom.Node, st *css.Style) tableRow {
	row := tableRow{node: node, style: st}
	col := 0
	for _, c := range l.renderedChildren(node) {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs != nil && cs.Display == css.DisplayTableCell {
			span := cellColSpan(c)
			row.cells = append(row.cells, c)
			row.colStart = append(row.colStart, col)
			row.colSpan = append(row.colSpan, span)
			col += span
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
		Height: css.Length{Auto: true}}
}
