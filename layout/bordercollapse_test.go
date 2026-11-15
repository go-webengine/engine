// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestBorderCollapseForcesSpacingToZero covers CSS 2.1's "border-spacing has
// no effect under border-collapse:collapse" rule: a table that declares BOTH
// must ignore the spacing, not apply it as border-collapse:separate would.
func TestBorderCollapseForcesSpacingToZero(t *testing.T) {
	src := `<html><body style="margin:0"><table style="border-collapse:collapse;border-spacing:5px;width:100px">` +
		`<tr><td style="padding:0;width:50px">a</td><td style="padding:0;width:50px">b</td></tr>` +
		`</table></body></html>`
	tbl := findBox(layoutHTML(t, src, 400), "table")
	row0 := tbl.Children[0].Children
	assertF(t, "row0.c0.X", row0[0].X, tbl.ContentX)
	assertF(t, "row0.c1.X", row0[1].X, row0[0].X+row0[0].W)
}

// TestBorderCollapseSuppressesOwnBorderOnlyWithCells covers Box.
// SuppressOwnBorder: set only once the table actually lays out a real cell
// (nothing to double against otherwise), never on a border-collapse:separate
// table (the default, and the pre-existing behaviour every other table test
// already relies on).
func TestBorderCollapseSuppressesOwnBorderOnlyWithCells(t *testing.T) {
	collapsed := findBox(layoutHTML(t, `<html><body style="margin:0">`+
		`<table style="border-collapse:collapse;border:1px solid black"><tr><td>a</td></tr></table>`+
		`</body></html>`, 400), "table")
	if !collapsed.SuppressOwnBorder {
		t.Error("a border-collapse:collapse table with a real cell: SuppressOwnBorder = false, want true")
	}

	separate := findBox(layoutHTML(t, `<html><body style="margin:0">`+
		`<table style="border:1px solid black"><tr><td>a</td></tr></table>`+
		`</body></html>`, 400), "table")
	if separate.SuppressOwnBorder {
		t.Error("a border-collapse:separate (default) table: SuppressOwnBorder = true, want false")
	}

	empty := findBox(layoutHTML(t, `<html><body style="margin:0">`+
		`<table style="border-collapse:collapse;border:1px solid black"></table>`+
		`</body></html>`, 400), "table")
	if empty != nil && empty.SuppressOwnBorder {
		t.Error("a border-collapse:collapse table with NO cells: SuppressOwnBorder = true, want false (nothing to double against)")
	}
}
