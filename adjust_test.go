// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"bytes"
	"context"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/go-webengine/engine/dom"
)

// fitDoc has two of Marp's fitting headings (a heading marked
// data-auto-scaling around a data-fit-content span): one too wide for its
// box, one narrower.
const fitDoc = `<!DOCTYPE html><html><head><style>
body{margin:0;font-family:sans-serif}
h1{font-size:40px;padding:0 24px;width:600px;margin:0}
h1>span{display:inline-block;white-space:nowrap}
</style></head><body>
<h1 data-auto-scaling><span data-fit-content>A heading far too long to fit the width of its slide</span></h1>
<p>between</p>
<h1 data-auto-scaling><span data-fit-content>Short</span></h1>
</body></html>`

// fitScript is go-slides' deck.FitScript (Marp's fitting header): each
// fitting heading's font size is scaled by its box's width over its
// content's.
const fitScript = `(()=>{for(const c of document.querySelectorAll('[data-auto-scaling]>[data-fit-content]')){` +
	`const h=c.parentElement,s=getComputedStyle(h),` +
	`avail=h.clientWidth-parseFloat(s.paddingLeft||0)-parseFloat(s.paddingRight||0),` +
	`natural=c.offsetWidth;` +
	`if(natural>0&&avail>0)h.style.fontSize=(parseFloat(s.fontSize)*avail/natural)+'px'}})();`

// nativeFit is fitScript in Go, through Geometry: the same reads, in the
// same order, and the same arithmetic.
func nativeFit(_ context.Context, root *dom.Node, g *Geometry) {
	var targets []*dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		for _, c := range n.Children {
			if c.Type == dom.Element {
				if _, fit := c.Attribute("data-fit-content"); fit && c.Parent != nil {
					if _, auto := c.Parent.Attribute("data-auto-scaling"); auto {
						targets = append(targets, c)
					}
				}
			}
			walk(c)
		}
	}
	walk(root)
	or0 := func(s string) float64 {
		if s == "" {
			return 0
		}
		return JSParseFloat(s)
	}
	for _, c := range targets {
		h := c.Parent
		avail := float64(g.ClientWidth(h)) - or0(g.Computed(h, "padding-left")) - or0(g.Computed(h, "padding-right"))
		natural := float64(g.OffsetWidth(c))
		if natural > 0 && avail > 0 {
			h.SetStyleProperty("font-size", JSNumberString(JSParseFloat(g.Computed(h, "font-size"))*avail/natural)+"px")
		}
	}
}

func paintHTML(t *testing.T, e *Engine, html string) *image.RGBA {
	t.Helper()
	l, err := e.LayoutHTML(context.Background(), html, "about:blank", image.Rect(0, 0, 800, 400))
	if err != nil {
		t.Fatal(err)
	}
	return l.Paint()
}

// An Adjust that changes the DOM is laid out again: the fitted headings
// are drawn at their new size, unlike the unadjusted render.
func TestAdjustRelaysOut(t *testing.T) {
	plain := New()
	plain.DisableJS = true
	fitted := New()
	fitted.DisableJS = true
	fitted.Adjust = nativeFit
	a, b := paintHTML(t, plain, fitDoc), paintHTML(t, fitted, fitDoc)
	if bytes.Equal(a.Pix, b.Pix) {
		t.Fatal("the adjusted render is the plain one")
	}
	// an Adjust that changes nothing leaves the render as it was
	idle := New()
	idle.DisableJS = true
	calls := 0
	idle.Adjust = func(context.Context, *dom.Node, *Geometry) { calls++ }
	if c := paintHTML(t, idle, fitDoc); calls != 1 || !bytes.Equal(a.Pix, c.Pix) {
		t.Fatalf("calls %d, idle render differs: %v", calls, !bytes.Equal(a.Pix, c.Pix))
	}
}

// Geometry answers what a script reads: offsetWidth and clientWidth
// rounded, getComputedStyle's used values, and the geometry of the DOM as
// mutated, laid out again before the read.
func TestGeometryReads(t *testing.T) {
	e := New()
	e.DisableJS = true
	var got []string
	e.Adjust = func(_ context.Context, root *dom.Node, g *Geometry) {
		h := dom.Find(root, "h1")
		span := h.Children[0]
		got = append(got, g.Computed(h, "padding-left"), g.Computed(h, "font-size"), JSNumberString(float64(g.ClientWidth(h))))
		w0 := g.OffsetWidth(span)
		h.SetStyleProperty("font-size", "20px")
		w1 := g.OffsetWidth(span) // re-laid out: half the size, about half the width
		if !(w1 > 0 && w1 < w0) {
			t.Errorf("offsetWidth %d after the font shrank, %d before", w1, w0)
		}
		got = append(got, g.Computed(h, "font-size"), g.Computed(h, "no-such-property"))
		if _, _, _, _, ok := g.Rect(dom.Find(root, "head")); ok {
			t.Error("the head has a box")
		}
	}
	paintHTML(t, e, fitDoc)
	want := []string{"24px", "40px", "648", "20px", ""}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestJSNumbers(t *testing.T) {
	for _, c := range []struct {
		s    string
		want float64
	}{
		{"64px", 64}, {"  53.25px", 53.25}, {"-1.5e2x", -150}, {"1e", 1}, {"5.", 5}, {".5em", 0.5},
		{"+Infinityx", math.Inf(1)}, {"-Infinity", math.Inf(-1)}, {"1e400", math.Inf(1)},
	} {
		if got := JSParseFloat(c.s); got != c.want {
			t.Errorf("JSParseFloat(%q) = %v, want %v", c.s, got, c.want)
		}
	}
	for _, s := range []string{"", "px", ".", "-", "e5", "auto"} {
		if got := JSParseFloat(s); !math.IsNaN(got) {
			t.Errorf("JSParseFloat(%q) = %v, want NaN", s, got)
		}
	}
	tenth, fifth := 0.1, 0.2 // variables: a constant sum would be exact
	for _, c := range []struct {
		v    float64
		want string
	}{
		// ECMA-262 Number::toString, as V8 prints them
		{64, "64"}, {53.333333333333336, "53.333333333333336"}, {tenth + fifth, "0.30000000000000004"},
		{1234567, "1234567"}, {1e20, "100000000000000000000"}, {1e21, "1e+21"}, {1.5e22, "1.5e+22"},
		{1e-6, "0.000001"}, {1.5e-6, "0.0000015"}, {1e-7, "1e-7"}, {1.5e-8, "1.5e-8"},
		{0.000001234, "0.000001234"}, {math.Copysign(0, -1), "0"}, {math.NaN(), "NaN"},
		{math.Inf(1), "Infinity"}, {math.Inf(-1), "-Infinity"}, {-2.5, "-2.5"}, {-1e-7, "-1e-7"},
		{123.456, "123.456"}, {5e-324, "5e-324"},
	} {
		if got := JSNumberString(c.v); got != c.want {
			t.Errorf("JSNumberString(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}
