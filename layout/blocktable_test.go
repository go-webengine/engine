// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// A <table style="display:block"> keeps table layout for its rows and cells:
// CSS 2.1 §17.2.1 wraps rows/cells whose parent is not a table in an
// anonymous table. go-webengine/engine#398 (the default Marp theme sets
// exactly this on every Markdown table).
const blockTableHTML = `<html><body style="margin:0">` +
	`<table style="display:block;width:max-content;max-width:100%;border-spacing:0;border-collapse:collapse">` +
	`<tr><th>aa</th><th>bbbb</th></tr><tr><td>1</td><td>2</td></tr></table></body></html>`

func TestBlockTableKeepsGrid(t *testing.T) {
	root := layoutHTML(t, blockTableHTML, 500)
	tbl := findBox(root, "table")
	if tbl == nil {
		t.Fatal("no table box")
	}
	var rows []*Box
	for _, c := range tbl.Children {
		rows = append(rows, c.Children...) // tbody -> rows, or rows directly
		if c.Node != nil && c.Node.Tag == "tr" {
			rows = append(rows[:len(rows)-len(c.Children)], c)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for i, r := range rows {
		if len(r.Children) != 2 {
			t.Fatalf("row %d cells = %d, want 2 (cells stacked on their own rows)", i, len(r.Children))
		}
	}
	// Row 2's cells sit under row 1's: same column x, and the columns are
	// content-wide (2 and 4 characters, 10px each, plus 1px padding each side
	// from the UA stylesheet), not stretched to the container.
	if rows[0].Children[0].X != rows[1].Children[0].X || rows[0].Children[1].X != rows[1].Children[1].X {
		t.Errorf("columns misaligned: %v/%v vs %v/%v", rows[0].Children[0].X, rows[0].Children[1].X, rows[1].Children[0].X, rows[1].Children[1].X)
	}
	if rows[0].Children[1].X <= rows[0].Children[0].X {
		t.Errorf("second column not to the right of the first")
	}
	if tbl.W >= 200 {
		t.Errorf("table width = %v, want max-content (< 200), not the 500px container", tbl.W)
	}
}
