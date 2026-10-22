package layout

import "testing"

// CSS Sizing 3 §5.1: a box's max-content size is the size it takes in an
// infinite containing block, where only FORCED breaks (CSS Text 3 §5: a
// <br>, or a preserved newline under white-space:pre / pre-wrap / pre-line)
// break lines. It is the width of the widest line, not the sum of the lines.
func TestMaxContentIsTheWidestLineNotTheSum(t *testing.T) {
	width := func(name, inner, style string) float64 {
		t.Helper()
		src := `<html><body style="margin:0"><div style="float:left;` + style + `">` + inner + `</div></body></html>`
		div := findBox(layoutHTML(t, src, 800), "div")
		if div == nil {
			t.Fatalf("%s: no float box", name)
		}
		return div.W
	}
	one := width("one line", "88", "white-space:pre")
	if one <= 0 {
		t.Fatalf("one line has width %v", one)
	}
	for _, c := range []struct {
		name, inner, style string
	}{
		{"pre newlines", "88\n88\n88\n", "white-space:pre"},
		{"pre-wrap newlines", "88\n88\n88", "white-space:pre-wrap"},
		{"pre-line newlines", "88\n88\n88", "white-space:pre-line"},
		{"br", "88<br>88<br>88", ""},
	} {
		assertF(t, c.name+": three equal lines are as wide as one", width(c.name, c.inner, c.style), one)
	}
	// the widest line decides, wherever it is
	wide := width("wide", "88\n8888\n88", "white-space:pre")
	two := width("two", "8888", "white-space:pre")
	assertF(t, "the widest of three lines", wide, two)
	// a trailing newline adds no line of its own
	assertF(t, "trailing newline", width("trail", "8888\n", "white-space:pre"), two)
}

// The gutter case that found it: a block flex item holding one number a line
// is as wide as its widest number, so the code beside it keeps its room.
func TestFlexItemOfNumberedLinesIsAsWideAsTheWidestNumber(t *testing.T) {
	gutter := func(nums string) float64 {
		t.Helper()
		src := `<html><body style="margin:0"><div style="display:flex;white-space:pre">` +
			`<span id="g" style="display:block;flex:none">` + nums + `</span><span style="display:block">x</span></div></body></html>`
		g := findBox(layoutHTML(t, src, 800), "span")
		if g == nil {
			t.Fatal("no gutter box")
		}
		return g.W
	}
	one := gutter("8\n9\n")
	two := gutter("8\n9\n10\n11\n12\n")
	if !(two > one) {
		t.Fatalf("two digits (%v) must be wider than one (%v)", two, one)
	}
	// 12 is the widest of five lines: as wide as a lone 12, not the sum
	assertF(t, "five lines are as wide as the widest", two, gutter("12\n"))
	assertF(t, "three one-digit lines are as wide as one", gutter("1\n2\n3\n"), gutter("1\n"))
}
