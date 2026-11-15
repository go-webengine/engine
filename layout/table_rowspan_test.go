// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestTableRowspanAlignsFollowingRowColumn covers rowspan's own column-
// occupancy interaction with colStart, the vertical analog of the existing
// colspan test (TestTableColspanAlignsFollowingCellWithLaterRow): a
// rowspan="2" cell in row 0's first column must still occupy that column in
// row 1, so row 1's own lone cell lands in column 1, not column 0.
// Confirmed real trigger: Wikipedia's chemical-element infobox template
// (e.g. the Carbon article) rowspans its property-group cells down several
// rows of otherwise single cells.
func TestTableRowspanAlignsFollowingRowColumn(t *testing.T) {
	src := `<html><body style="margin:0"><table style="width:100px">` +
		`<tr><td rowspan="2" style="padding:0;width:20px;height:10px">A</td>` +
		`<td style="padding:0;width:80px;height:10px">B</td></tr>` +
		`<tr><td style="padding:0;height:15px">C</td></tr>` +
		`</table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	if len(tbl.Children) != 2 {
		t.Fatalf("rows = %d, want 2", len(tbl.Children))
	}
	row0, row1 := tbl.Children[0], tbl.Children[1]
	if len(row0.Children) != 2 || len(row1.Children) != 1 {
		t.Fatalf("cells = %d, %d, want 2, 1", len(row0.Children), len(row1.Children))
	}
	cCell := row1.Children[0]
	assertF(t, "C cell X", cCell.X, 20) // column 1, not column 0 (occupied by A)
	assertF(t, "C cell W", cCell.W, 80)
}

// TestTableRowspanCellSpansSummedRowHeights covers a rowspan cell's own box
// height: the sum of its spanned rows' heights, the vertical analog of how
// a colspan cell's own box width already sums colW across the columns it
// spans. When the spanning cell's natural content height exceeds what its
// spanned rows already provide, the deficit grows the LAST spanned row (a
// disclosed simplification versus a real browser's own proportional
// distribution — CSS 2.1 does not define the split), which in turn stretches
// every ordinary cell sharing that row.
func TestTableRowspanCellSpansSummedRowHeights(t *testing.T) {
	src := `<html><body style="margin:0"><table style="width:100px">` +
		`<tr><td rowspan="2" style="padding:0;width:20px;height:40px">A</td>` +
		`<td style="padding:0;width:80px;height:10px">B</td></tr>` +
		`<tr><td style="padding:0;height:15px">C</td></tr>` +
		`</table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	row0, row1 := tbl.Children[0], tbl.Children[1]
	aCell, bCell := row0.Children[0], row0.Children[1]
	cCell := row1.Children[0]

	// Row 0's own height stays 10 (B's height; A's natural height is
	// deferred, never inflating its OWN starting row).
	assertF(t, "row0.H", row0.H, 10)
	// A's deficit (40 - (10+15)) = 15 grows row 1 to 15+15 = 30.
	assertF(t, "row1.H", row1.H, 30)
	// A's own box spans both rows: 10 + 30 = 40, matching its natural height
	// exactly (no leftover slack).
	assertF(t, "A cell H", aCell.H, 40)
	assertF(t, "B cell H", bCell.H, 10)
	// C, an ordinary cell sharing row 1 with no rowspan of its own, is
	// stretched along with the row it sits in.
	assertF(t, "C cell H", cCell.H, 30)
}

// TestTableRowspanThreeRowsDecrementsWithoutClearing covers the occupancy
// map's own multi-row decrement: a rowspan="3" cell must still block its
// column in BOTH of the two rows after its own, so the second of those rows
// is reached with the column's remaining-block count decremented but not yet
// zero (the makeRow case rowSpan leaves behind without deleting the entry).
func TestTableRowspanThreeRowsDecrementsWithoutClearing(t *testing.T) {
	src := `<html><body style="margin:0"><table style="width:100px">` +
		`<tr><td rowspan="3" style="padding:0;width:20px">A</td>` +
		`<td style="padding:0;width:80px">B</td></tr>` +
		`<tr><td style="padding:0">C</td></tr>` +
		`<tr><td style="padding:0">D</td></tr>` +
		`</table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	if len(tbl.Children) != 3 {
		t.Fatalf("rows = %d, want 3", len(tbl.Children))
	}
	for i, row := range tbl.Children[1:] {
		if len(row.Children) != 1 {
			t.Fatalf("row %d cells = %d, want 1", i+1, len(row.Children))
		}
		assertF(t, "row second/third row cell X", row.Children[0].X, 20) // column 1, still blocked at column 0
	}
}

// TestTableRowspanPastLastRowClamps covers a rowspan reaching past the
// table's own last row (malformed markup — no HTML spec behaviour to match,
// just mustn't index out of range): the span is clamped to the rows that
// actually exist, both when distributing its own height deficit and when
// placing its box.
func TestTableRowspanPastLastRowClamps(t *testing.T) {
	src := `<html><body style="margin:0"><table style="width:100px">` +
		`<tr><td rowspan="5" style="padding:0;width:20px;height:50px">A</td>` +
		`<td style="padding:0;width:80px;height:10px">B</td></tr>` +
		`<tr><td style="padding:0;height:10px">C</td></tr>` +
		`</table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	row0, row1 := tbl.Children[0], tbl.Children[1]
	assertF(t, "row0.H", row0.H, 10)
	assertF(t, "row1.H", row1.H, 40) // 50 - (10+10) deficit added here
	assertF(t, "A cell H", row0.Children[0].H, 50)
}

// TestCellRowSpanInvalidValueDefault covers cellRowSpan's "invalid value
// default" (a non-numeric or non-positive rowspan is treated as absent, per
// the HTML spec), the rowspan analog of TestCellColSpanInvalidValueDefault:
// an invalid rowspan cell must not occupy any column in a later row.
func TestCellRowSpanInvalidValueDefault(t *testing.T) {
	src := `<html><body style="margin:0"><table style="width:100px">` +
		`<tr><td rowspan="bogus" style="padding:0;width:20px">A</td>` +
		`<td style="padding:0;width:80px">B</td></tr>` +
		`<tr><td rowspan="0" style="padding:0;width:20px">C</td>` +
		`<td style="padding:0;width:80px">D</td></tr>` +
		`</table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	if len(tbl.Children) != 2 {
		t.Fatalf("rows = %d, want 2", len(tbl.Children))
	}
	for i, row := range tbl.Children {
		if len(row.Children) != 2 {
			t.Fatalf("row %d cells = %d, want 2 (an invalid rowspan must not occupy a column)", i, len(row.Children))
		}
		assertF(t, "row second cell X", row.Children[1].X, 20)
	}
}
