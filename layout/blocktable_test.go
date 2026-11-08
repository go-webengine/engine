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

// A block table whose content is wider than its container is capped at the
// container (the anonymous table shrinks, it never overflows by choice), and a
// block table of empty rows lays out to nothing without panicking.
func TestBlockTableEdges(t *testing.T) {
	wide := `<html><body style="margin:0"><table style="display:block"><tr><td>aaaaaaaaaaaaaaaaaaaa</td><td>bbbbbbbbbbbbbbbbbbbb</td></tr></table></body></html>`
	tbl := findBox(layoutHTML(t, wide, 100), "table")
	if tbl == nil || tbl.W > 100 {
		t.Fatalf("wide block table = %v", tbl)
	}
	empty := `<html><body style="margin:0"><table style="display:block"><tr></tr></table></body></html>`
	if tbl := findBox(layoutHTML(t, empty, 100), "table"); tbl == nil || tbl.H != 0 {
		t.Fatalf("empty-row block table = %v", tbl)
	}
}

// width: min-content / fit-content on a block (CSS Sizing 3 §4 keywords).
func TestWidthSizingKeywordsLayout(t *testing.T) {
	for _, tc := range []struct {
		kw      string
		wantMin float64
		wantMax float64
	}{
		{"min-content", 50, 50},   // "hello" / "world": the longest word (5 chars x 10px)
		{"max-content", 110, 110}, // the whole line "hello world" (11 chars)
		{"fit-content", 110, 110}, // fits in 300
	} {
		src := `<html><body style="margin:0"><div id="d" style="width:` + tc.kw + `">hello world</div></body></html>`
		d := findBoxByID(layoutHTML(t, src, 300), "d")
		if d == nil || d.W < tc.wantMin || d.W > tc.wantMax {
			t.Errorf("%s: box %v, want width %v", tc.kw, d, tc.wantMin)
		}
	}
	// fit-content never exceeds the container.
	src := `<html><body style="margin:0"><div id="d" style="width:fit-content">hello world hello world</div></body></html>`
	if d := findBoxByID(layoutHTML(t, src, 100), "d"); d == nil || d.W > 100 {
		t.Errorf("fit-content overflow: %v", d)
	}
}

// fit-content is floored at min-content: a word longer than the container wins.
func TestFitContentMinFloor(t *testing.T) {
	src := `<html><body style="margin:0"><div id="d" style="width:fit-content">aaaaaaaaaaaaaaaa</div></body></html>`
	if d := findBoxByID(layoutHTML(t, src, 100), "d"); d == nil || d.W != 160 {
		t.Errorf("fit-content with a 160px word in 100px: %v", d)
	}
}

// Border-spacing wider than the container leaves no room for columns (avail
// is floored at zero) on a block table as on a real one.
func TestBlockTableSpacingWiderThanContainer(t *testing.T) {
	src := `<html><body style="margin:0"><table style="display:block;border-spacing:200px"><tr><td>a</td></tr></table></body></html>`
	if tbl := findBox(layoutHTML(t, src, 100), "table"); tbl == nil {
		t.Fatal("no table")
	}
}
