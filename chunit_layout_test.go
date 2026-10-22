// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"math"
	"testing"

	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
	"github.com/go-webengine/engine/paint"
)

// CSS Values 4 §6.1.1: ch is the advance of "0" in the element's font. Through
// the engine's own pipeline (cascade given the page's fonts, then layout), a
// box of Nch is as wide as N zeros set in that font — monospace or not.
func TestChIsAsWideAsZerosInTheRealFont(t *testing.T) {
	for _, fam := range []string{"monospace", "serif", "sans-serif"} {
		root, err := dom.Parse(`<html><body style="margin:0;font-size:20px;font-family:` + fam + `">` +
			`<div id="a" style="width:7ch">x</div></body></html>`)
		if err != nil {
			t.Fatal(err)
		}
		fonts := paint.NewFonts()
		e := New()
		sm := e.cascade(root, 800, nil, nil, fonts)
		a := findByID(root, "a")
		st := sm[a]
		want := fonts.Measure("0000000", st.FontFamily, st.FontSize, st.FontWeight, st.Italic)
		if want <= 7*20/2-1 && fam != "serif" && fam != "sans-serif" {
			// a monospace "0" is not 0.5em in the bundled fonts: this test
			// must be able to tell the measured advance from the fallback
			t.Logf("%s: advance %v", fam, want)
		}
		box, _ := layout.LayoutDocument(root, sm, 800, fonts, nil)
		r, ok := layout.BuildIndex(box)[a]
		if !ok {
			t.Fatalf("%s: no box", fam)
		}
		if math.Abs(r.W-want) > 0.01 {
			t.Errorf("%s: width:7ch laid out %v wide, seven zeros measure %v", fam, r.W, want)
		}
		// and the fallback is distinguishable: the measured advance of
		// monospace is not the 0.5em default
		if fam == "monospace" && math.Abs(want-7*20*0.5) < 0.01 {
			t.Errorf("monospace zero measures exactly 0.5em (%v): the test cannot tell ch from its fallback", want)
		}
	}
}

// The numbered-code gutter that found both gaps: a block flex item with one
// number a line is as wide as its widest number (forced breaks, CSS Sizing 3
// §5), and a min-width in ch floors it at that many digits.
func TestGutterWidthFromLinesAndCh(t *testing.T) {
	gutter := func(numbers, style string) float64 {
		t.Helper()
		root, err := dom.Parse(`<html><body style="margin:0;font:20px monospace">` +
			`<code style="display:flex;white-space:pre"><span id="g" style="display:block;flex:none;` + style + `">` + numbers + `</span>` +
			`<span style="display:block">code</span></code></body></html>`)
		if err != nil {
			t.Fatal(err)
		}
		fonts := paint.NewFonts()
		sm := New().cascade(root, 800, nil, nil, fonts)
		box, _ := layout.LayoutDocument(root, sm, 800, fonts, nil)
		r, ok := layout.BuildIndex(box)[findByID(root, "g")]
		if !ok {
			t.Fatal("no gutter box")
		}
		return r.W
	}
	one := gutter("8\n9\n", "")
	if one <= 0 {
		t.Fatalf("gutter has width %v", one)
	}
	assertNear(t, "1 digit x 5 lines vs 1 line", gutter("1\n2\n3\n4\n5\n", ""), gutter("1\n", ""))
	two := gutter("8\n9\n10\n11\n12\n", "")
	assertNear(t, "12 is the widest of 8..12", two, 2*one)
	three := gutter("98\n99\n100\n", "")
	assertNear(t, "100 is the widest of 98..100", three, 3*one)
	// a floor of 3ch widens a 1-digit gutter to three digits, and does not
	// narrow a wider one
	assertNear(t, "min-width:3ch under 1 digit", gutter("1\n2\n", "min-width:3ch"), 3*one)
	assertNear(t, "min-width:1ch under 3 digits", gutter("98\n99\n100\n", "min-width:1ch"), 3*one)
}

func assertNear(t *testing.T, what string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 0.01 {
		t.Errorf("%s: got %v, want %v", what, got, want)
	}
}
