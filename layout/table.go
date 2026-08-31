// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// tableRow is one row and its cells (both with resolved styles).
type tableRow struct {
	node  *dom.Node
	style *css.Style
	cells []*dom.Node
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
		if len(r.cells) > ncols {
			ncols = len(r.cells)
		}
	}
	if ncols == 0 {
		return top
	}

	// Natural (max-content) width per column.
	natural := make([]float64, ncols)
	for _, r := range rows {
		for j, cell := range r.cells {
			cs := l.cellStyle(cell)
			w := l.preferredWidth(cell, cs) + cs.Margin.Left + cs.Margin.Right
			if w > natural[j] {
				natural[j] = w
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
			contentW := colW[j] - hEdges - cs.Margin.Left - cs.Margin.Right
			cbox := l.layoutIsolated(cell, cs, contentW)
			cellBoxes = append(cellBoxes, cbox)
			if h := cbox.H + cs.Margin.Top + cs.Margin.Bottom; h > rowH {
				rowH = h
			}
		}
		for j, cbox := range cellBoxes {
			cs := l.cellStyle(r.cells[j])
			translateBox(cbox, (colX[j]+cs.Margin.Left)-cbox.X, (y+cs.Margin.Top)-cbox.Y)
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
	for _, c := range renderedChildren(node) {
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
			for _, r := range renderedChildren(c) {
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

// makeRow collects the table-cell children of a row.
func (l *layouter) makeRow(node *dom.Node, st *css.Style) tableRow {
	row := tableRow{node: node, style: st}
	for _, c := range renderedChildren(node) {
		if c.Type != dom.Element {
			continue
		}
		cs := l.sm[c]
		if cs != nil && cs.Display == css.DisplayTableCell {
			row.cells = append(row.cells, c)
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
