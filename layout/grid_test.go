// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// ---- track sizing ----------------------------------------------------------

func TestGridThreeFrColumns(t *testing.T) {
	// Three equal fr columns split a 300px grid into 100px cells; items stretch.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:1fr 1fr 1fr">` +
		`<div>A</div><div>B</div><div>C</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	if len(g.Children) != 3 {
		t.Fatalf("children = %d", len(g.Children))
	}
	assertF(t, "fr.A.X", g.Children[0].X, 0)
	assertF(t, "fr.A.W", g.Children[0].W, 100)
	assertF(t, "fr.B.X", g.Children[1].X, 100)
	assertF(t, "fr.C.X", g.Children[2].X, 200)
	assertF(t, "fr.row.Y", g.Children[2].Y, 0)
	assertF(t, "fr.container.H", g.H, 20)
}

func TestGridFixedColumnsWithColumnGap(t *testing.T) {
	// Fixed px tracks + a 20px column gap.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:100px 200px;column-gap:20px">` +
		`<div>A</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 500), "div")
	assertF(t, "fx.A.X", g.Children[0].X, 0)
	assertF(t, "fx.A.W", g.Children[0].W, 100)
	assertF(t, "fx.B.X", g.Children[1].X, 120)
	assertF(t, "fx.B.W", g.Children[1].W, 200)
}

func TestGridRepeatAutoFlowWraps(t *testing.T) {
	// repeat(2,100px): the third item flows onto row 2.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:repeat(2, 100px)">` +
		`<div>A</div><div>B</div><div>C</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	assertF(t, "rep.C.X", g.Children[2].X, 0)
	assertF(t, "rep.C.Y", g.Children[2].Y, 20) // row 2 below the 20px first row
	assertF(t, "rep.container.H", g.H, 40)
}

func TestGridMinmaxWithFr(t *testing.T) {
	// minmax(100px,1fr) 1fr over 300px: col0 base 100 (+100 fr share) = 200,
	// col1 = 100.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:minmax(100px,1fr) 1fr">` +
		`<div>A</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "mm.A.W", g.Children[0].W, 200)
	assertF(t, "mm.B.X", g.Children[1].X, 200)
	assertF(t, "mm.B.W", g.Children[1].W, 100)
}

func TestGridMinmaxFixedMaxClamps(t *testing.T) {
	// minmax(auto,150px): the content column cannot exceed 150 even though the
	// content ("xxxxxxxxxxxxxxxxxxxx" = 200px) is wider.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:minmax(auto,150px) 1fr;width:400px">` +
		`<div>xxxxxxxxxxxxxxxxxxxx</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 500), "div")
	assertF(t, "mmc.A.W", g.Children[0].W, 150)
	assertF(t, "mmc.B.X", g.Children[1].X, 150)
}

func TestGridMinmaxNonFrGrowsToFillFreeSpace(t *testing.T) {
	// minmax(0,800px) (no fr unit) between two 40px fixed columns, over a
	// 1024px grid: the CSS "Maximize Tracks" step must grow it to absorb the
	// 944px of leftover space, same as a browser — this is the shape of a
	// typical sidebar/content/sidebar layout (e.g. tailwindcss.com's own page
	// shell), and a track with no fr unit was previously never grown past its
	// base size, leaving the whole grid stuck at its 80px minimum and centred
	// in the middle of the page.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:40px minmax(0,1536px) 40px;width:1024px">` +
		`<div>A</div><div>B</div><div>C</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 1024), "div")
	assertF(t, "mmg.A.W", g.Children[0].W, 40)
	assertF(t, "mmg.B.X", g.Children[1].X, 40)
	assertF(t, "mmg.B.W", g.Children[1].W, 944)
	assertF(t, "mmg.C.X", g.Children[2].X, 984)
	assertF(t, "mmg.C.W", g.Children[2].W, 40)
}

func TestGridMinmaxNonFrRespectsCap(t *testing.T) {
	// Same shape, but the cap (300px) is narrower than the free space (944px):
	// the track grows only up to its cap, and the remaining free space is
	// simply left over (no fr track to absorb it).
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:40px minmax(0,300px) 40px;width:1024px">` +
		`<div>A</div><div>B</div><div>C</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 1024), "div")
	assertF(t, "mmcap.B.W", g.Children[1].W, 300)
}

// ---- explicit placement + span --------------------------------------------

func TestGridExplicitColumnSpan(t *testing.T) {
	// A spans columns 1..3 (span 2 → 200px); B and C auto-flow around it.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:repeat(3, 100px)">` +
		`<div style="grid-column:1 / span 2">A</div>` +
		`<div>B</div><div>C</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	byText := map[string]*Box{}
	for _, c := range g.Children {
		byText[boxText(c)] = c
	}
	assertF(t, "span.A.X", byText["A"].X, 0)
	assertF(t, "span.A.W", byText["A"].W, 200)
	assertF(t, "span.B.X", byText["B"].X, 200) // column 3
	assertF(t, "span.B.Y", byText["B"].Y, 0)
	assertF(t, "span.C.X", byText["C"].X, 0) // wraps to row 2
	assertF(t, "span.C.Y", byText["C"].Y, 20)
}

func TestGridExplicitLineNumbers(t *testing.T) {
	// grid-column:2 / 4 places the item in columns 2..3 (span 2).
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:repeat(4, 50px)">` +
		`<div style="grid-column:2 / 4;grid-row:1">A</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	assertF(t, "lines.A.X", g.Children[0].X, 50)  // column 2 starts at 50
	assertF(t, "lines.A.W", g.Children[0].W, 100) // columns 2+3
}

func TestGridRowSpan(t *testing.T) {
	// A spans two rows; B and C occupy the remaining cells of a 2-col grid.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:repeat(2,100px);grid-template-rows:30px 40px">` +
		`<div style="grid-row:1 / span 2">A</div>` +
		`<div>B</div><div>C</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	byText := map[string]*Box{}
	for _, c := range g.Children {
		byText[boxText(c)] = c
	}
	assertF(t, "rspan.A.X", byText["A"].X, 0)
	assertF(t, "rspan.A.Y", byText["A"].Y, 0)
	assertF(t, "rspan.B.X", byText["B"].X, 100)
	assertF(t, "rspan.B.Y", byText["B"].Y, 0)
	assertF(t, "rspan.C.X", byText["C"].X, 100)
	assertF(t, "rspan.C.Y", byText["C"].Y, 30) // second row
}

func TestGridRowSpanFullNegativeLineReservesOccupancy(t *testing.T) {
	// `grid-row: 1 / -1` (Tailwind's row-span-full) in a 3-row explicit grid
	// must span all 3 rows and reserve column 0 in every row, exactly like a
	// browser. The row axis previously resolved a negative end line against
	// an unknown track count, silently collapsing the span to zero rows: the
	// item reserved no occupancy, so the next auto-placed item slid into
	// column 0 instead of column 1 (this is what left tailwindcss.com's main
	// content column stuck in its 40px decorative gutter track).
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:40px 100px;grid-template-rows:20px 20px 20px">` +
		`<div style="grid-row:1 / -1;grid-column:1">A</div>` +
		`<div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	byText := map[string]*Box{}
	for _, c := range g.Children {
		byText[boxText(c)] = c
	}
	assertF(t, "rsfull.A.X", byText["A"].X, 0)
	assertF(t, "rsfull.A.W", byText["A"].W, 40)
	assertF(t, "rsfull.A.H", byText["A"].H, 60) // spans all 3 rows: 3*20px
	// B has no explicit placement; column 0 is occupied for every row by A,
	// so B must land in column 1, not overlap A in column 0.
	assertF(t, "rsfull.B.X", byText["B"].X, 40)
	assertF(t, "rsfull.B.Y", byText["B"].Y, 0)
}

// ---- row sizing ------------------------------------------------------------

func TestGridExplicitRowHeights(t *testing.T) {
	// grid-template-rows with fixed px heights positions row 2 below row 1.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:100px;grid-template-rows:50px 30px">` +
		`<div>A</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "rows.A.Y", g.Children[0].Y, 0)
	assertF(t, "rows.B.Y", g.Children[1].Y, 50)
	assertF(t, "rows.container.H", g.H, 80)
}

func TestGridRowGap(t *testing.T) {
	// row-gap separates the two auto rows by 10px.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:100px;row-gap:10px">` +
		`<div>A</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "rg.B.Y", g.Children[1].Y, 30) // 20 row + 10 gap
	assertF(t, "rg.container.H", g.H, 50)
}

// ---- alignment -------------------------------------------------------------

func TestGridJustifyAndAlignItems(t *testing.T) {
	// A 100x60 cell; a 40x20 item centred on both axes sits at (30,20).
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:100px;grid-template-rows:60px;justify-items:center;align-items:center">` +
		`<div style="width:40px;height:20px">A</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	a := g.Children[0]
	assertF(t, "ji.A.X", a.X, 30) // (100-40)/2
	assertF(t, "ji.A.Y", a.Y, 20) // (60-20)/2
	assertF(t, "ji.A.W", a.W, 40)
	assertF(t, "ji.A.H", a.H, 20)
}

func TestGridStretchDefault(t *testing.T) {
	// Default justify/align is stretch: the item fills the whole 100x50 cell.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:100px;grid-template-rows:50px">` +
		`<div>A</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	a := g.Children[0]
	assertF(t, "st.A.W", a.W, 100)
	assertF(t, "st.A.H", a.H, 50)
}

func TestGridAlignSelfOverridesItems(t *testing.T) {
	// align-self:end on one item overrides the container's align-items:start.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:100px;grid-template-rows:60px;align-items:start">` +
		`<div style="height:20px;align-self:end">A</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "gas.A.Y", g.Children[0].Y, 40) // 60-20
}

// ---- template areas --------------------------------------------------------

func TestGridTemplateAreas(t *testing.T) {
	// A classic header/sidebar/main areas grid.
	src := `<html><body style="margin:0"><div style="display:grid;` +
		`grid-template-columns:100px 100px;` +
		`grid-template-areas:'h h' 's m'">` +
		`<div style="grid-area:h">H</div>` +
		`<div style="grid-area:s">S</div>` +
		`<div style="grid-area:m">M</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	byText := map[string]*Box{}
	for _, c := range g.Children {
		byText[boxText(c)] = c
	}
	assertF(t, "area.H.X", byText["H"].X, 0)
	assertF(t, "area.H.W", byText["H"].W, 200) // spans both columns
	assertF(t, "area.H.Y", byText["H"].Y, 0)
	assertF(t, "area.S.X", byText["S"].X, 0)
	assertF(t, "area.S.Y", byText["S"].Y, 20) // second row
	assertF(t, "area.M.X", byText["M"].X, 100)
	assertF(t, "area.M.Y", byText["M"].Y, 20)
}

// ---- implicit single column + auto rows -----------------------------------

func TestGridImplicitSingleColumn(t *testing.T) {
	// display:grid with no template columns → one column, items stacked in rows.
	src := `<html><body style="margin:0"><div style="display:grid;width:200px">` +
		`<div>A</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "imp.A.Y", g.Children[0].Y, 0)
	assertF(t, "imp.B.Y", g.Children[1].Y, 20)
	assertF(t, "imp.A.W", g.Children[0].W, 200) // fills the single column
}

func TestGridEmpty(t *testing.T) {
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:1fr 1fr"></div></body></html>`
	g := findBox(layoutHTML(t, src, 300), "div")
	if len(g.Children) != 0 {
		t.Errorf("empty grid children = %d", len(g.Children))
	}
}

func TestGridJustifyContentCenter(t *testing.T) {
	// Two 80px fixed columns in a 300px grid, justify-content:center → the whole
	// track band (160px) is centred, leaving 70px on each side.
	src := `<html><body style="margin:0"><div style="display:grid;grid-template-columns:80px 80px;justify-content:center;width:300px">` +
		`<div>A</div><div>B</div></div></body></html>`
	g := findBox(layoutHTML(t, src, 400), "div")
	assertF(t, "jc.A.X", g.Children[0].X, 70)
	assertF(t, "jc.B.X", g.Children[1].X, 150)
}
