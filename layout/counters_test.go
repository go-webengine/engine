// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"strings"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// sizedMeasurer is fakeMeasurer with an ascent proportional to the font
// size, so a marker in a smaller font than its item has a smaller ascent.
type sizedMeasurer struct{ fakeMeasurer }

func (sizedMeasurer) Metrics(_ css.FontFamily, size float64, _ int, _ bool) (float64, float64) {
	return size * 0.8, size * 1.2
}

// A ::marker in a smaller font than its item still sits on the item's
// baseline: its line top moves down by the difference in ascent.
func TestMarkerSmallerFontSharesBaseline(t *testing.T) {
	root, err := dom.Parse(`<html><head><style>li::marker { font-size: 8px }</style></head>` +
		`<body style="margin:0;font-size:20px"><ol><li>a</li></ol></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := LayoutDocument(root, css.Cascade(root), 400, sizedMeasurer{}, nil)
	li := findBox(box, "li")
	m := li.Marker
	if m == nil {
		t.Fatal("no marker")
	}
	line := firstLineBox(li)
	assertF(t, "marker baseline", m.TextY+m.Ascent, line.Y+20*0.8)
	assertF(t, "marker ascent", m.Ascent, 8*0.8)
}

// attachMarker on a style no counters pass resolved (a hand-made css.Style)
// still numbers by the list walk's ordinal, in every style.
func TestAttachMarkerWithoutCountersPass(t *testing.T) {
	l := &layouter{m: fakeMeasurer{}}
	base := css.Style{FontSize: 16}
	cases := []struct {
		typ     css.ListStyleType
		pos     css.ListStylePosition
		str     string
		ordinal int
		want    string // "" = no marker
		glyph   bool
	}{
		{css.ListDecimal, css.ListOutside, "", 7, "7.", false},
		{css.ListUpperRoman, css.ListOutside, "", 4, "IV.", false},
		{css.ListDisc, css.ListOutside, "", 1, "•", true},
		{css.ListString, css.ListOutside, "-", 1, "-", false},
		{css.ListString, css.ListOutside, "", 1, "", false},
		{css.ListNone, css.ListOutside, "", 1, "", false},
		{css.ListDecimal, css.ListInside, "", 1, "", false},
		{css.ListDecimal, css.ListOutside, "", 1 << 40, "2147483647.", false},
		{css.ListDecimal, css.ListOutside, "", -(1 << 40), "-2147483648.", false},
	}
	for _, c := range cases {
		st := base
		st.ListStyleType, st.ListStylePosition, st.ListStyleString = c.typ, c.pos, c.str
		box := &Box{ContentX: 100}
		l.attachMarker(box, &st, c.ordinal)
		got := ""
		if box.Marker != nil {
			got = box.Marker.Text
			if box.Marker.IsGlyph() != c.glyph {
				t.Errorf("%v: glyph = %v", c.typ, box.Marker.IsGlyph())
			}
		}
		if got != c.want {
			t.Errorf("type %v pos %v ordinal %d: marker %q, want %q", c.typ, c.pos, c.ordinal, got, c.want)
		}
	}
	// A resolved inside marker attaches no outside box.
	st := base
	st.Marker = &css.MarkerInfo{Text: "1. ", Inside: true}
	box := &Box{}
	l.attachMarker(box, &st, 1)
	if box.Marker != nil {
		t.Errorf("an inside marker got an outside box: %+v", box.Marker)
	}
}

// firstLineText is the text of a box's first line, items joined by "|".
func firstLineText(b *Box) string {
	line := firstLineBox(b)
	if line == nil {
		return ""
	}
	var parts []string
	for _, it := range line.Items {
		if it.Text != "" {
			parts = append(parts, it.Text)
		}
	}
	return strings.Join(parts, "|")
}

// The three defects reported through html2pdf on 2026-10-28, at the layout
// tree every backend walks: every outside marker carries its text (the
// bullet too, so a text-only backend gets it), start= and list-style-type
// are honoured, an inside marker is the first inline item of the item's
// first line, and ::before content with escapes or counter() becomes text.
func TestMarkersAndCountersInLayout(t *testing.T) {
	root := layoutHTML(t, `<html><head><style>
.esc { list-style: none } .esc li::before { content: "\2022\00a0" }
ol.c { counter-reset: item; list-style: none }
ol.c > li::before { content: counter(item) ". "; counter-increment: item }
</style></head><body style="margin:0">
<ul><li>a</li></ul>
<ol start="3"><li>a</li><li>b</li></ol>
<ol style="list-style-type: lower-roman"><li>a</li><li>b</li><li>c</li><li>d</li></ol>
<ul style="list-style-type: square"><li>a</li></ul>
<ul class="esc"><li>a</li></ul>
<ol class="c"><li>a</li><li>b</li></ol>
<ul style="list-style-position: inside"><li>a</li></ul>
<ol style="list-style: upper-alpha inside" start="2"><li>a</li></ol>
</body></html>`, 1024)
	lis := findAll(root, "li")
	if len(lis) != 13 {
		t.Fatalf("want 13 li, got %d", len(lis))
	}
	type want struct {
		marker string // outside marker text, "" for none
		glyph  bool
		line   string // first line's items
	}
	wants := []want{
		{"•", true, "a"},
		{"3.", false, "a"}, {"4.", false, "b"},
		{"i.", false, "a"}, {"ii.", false, "b"}, {"iii.", false, "c"}, {"iv.", false, "d"},
		{"▪", true, "a"},
		{"", false, "• |a"},
		{"", false, "1.|a"}, {"", false, "2.|b"},
		{"", false, "•|a"},
		{"", false, "B.|a"},
	}
	for i, w := range wants {
		li := lis[i]
		got := ""
		if li.Marker != nil {
			got = li.Marker.Text
			if li.Marker.IsGlyph() != w.glyph {
				t.Errorf("li[%d] glyph = %v, want %v", i, li.Marker.IsGlyph(), w.glyph)
			}
			if li.Marker.TextX+1 > li.ContentX || li.Marker.Ascent <= 0 {
				t.Errorf("li[%d] marker text at x=%v ascent=%v, content at %v", i, li.Marker.TextX, li.Marker.Ascent, li.ContentX)
			}
		}
		if got != w.marker {
			t.Errorf("li[%d] marker = %q, want %q", i, got, w.marker)
		}
		if line := strings.ReplaceAll(firstLineText(li), " ", ""); line != strings.ReplaceAll(w.line, " ", "") {
			t.Errorf("li[%d] first line = %q, want %q", i, line, w.line)
		}
	}
}
