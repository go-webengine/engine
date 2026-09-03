// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

func TestFlexSkipsTextAndHidden(t *testing.T) {
	// A text node and a display:none child are not flex items.
	src := `<html><body style="margin:0"><div style="display:flex">` +
		`text<span style="display:none">H</span>` +
		`<div style="width:50px">A</div></div></body></html>`
	outer := findBox(layoutHTML(t, src, 300), "div")
	if len(outer.Children) != 1 {
		t.Fatalf("flex items = %d want 1", len(outer.Children))
	}
}

// TestFlexContainerBareTextOnly covers a real regression: flexItems only
// ever collects ELEMENT children, so a `display:flex` element whose content
// is bare text with NO element children at all (e.g.
// `<h1 style="display:flex">React</h1>`, confirmed live on react.dev's hero
// heading and its two CTA buttons) collected ZERO flex items and flex()
// returned immediately having laid out nothing — collapsing the whole
// element to zero width AND height. It must fall back to the same
// inline-formatting-context path a plain, non-flex, no-block-child element
// already uses.
func TestFlexContainerBareTextOnly(t *testing.T) {
	src := `<html><body style="margin:0">` +
		`<h1 style="display:flex;font-size:20px">React</h1>` +
		`</body></html>`
	h1 := findBox(layoutHTML(t, src, 300), "h1")
	if h1.W <= 0 || h1.H <= 0 {
		t.Fatalf("bare-text flex container = W=%v H=%v, want both > 0", h1.W, h1.H)
	}
	if len(h1.Lines) != 1 || len(h1.Lines[0].Items) != 1 || h1.Lines[0].Items[0].Text != "React" {
		t.Fatalf("bare-text flex container lines = %+v, want one line with the text", h1.Lines)
	}
}

// TestFlexContainerEmptyStaysCollapsed covers that the bare-text fallback
// does not paper over a GENUINELY empty flex container (no element children
// AND no text either) — it must still collapse to zero, not synthesize
// content that was never there.
func TestFlexContainerEmptyStaysCollapsed(t *testing.T) {
	src := `<html><body style="margin:0"><div style="display:flex"></div></body></html>`
	d := findBox(layoutHTML(t, src, 300), "div")
	if d.W != 0 && d.H != 0 {
		t.Errorf("genuinely empty flex container = W=%v H=%v, want both 0", d.W, d.H)
	}
}

// TestSelectOptionsDoNotLeakIntoLayout covers a real regression: a real
// <select> is a replaced, OS-native control that shows only its selected
// option on one line, entirely opaque to CSS box layout — but this engine had
// no special-casing for <option> at all, so its text flowed as ordinary
// inline content. Observed live on pkg.go.dev/net/http: a version/tab-switcher
// <select> holding dozens of <option>s (one holding the page's entire
// alphabetical symbol index as option text) rendered every option's text
// wrapped across hundreds of pixels at the select's DOM position, instead of
// the empty single-line box a real browser's native widget shows.
func TestSelectOptionsDoNotLeakIntoLayout(t *testing.T) {
	src := `<html><body style="margin:0">before<select>` +
		`<option>AAAAAAAAAA</option><option>BBBBBBBBBB</option></select>after</body></html>`
	root := layoutHTML(t, src, 300)
	var walk func(b *Box)
	walk = func(b *Box) {
		for _, ln := range b.Lines {
			for _, it := range ln.Items {
				if it.Text == "AAAAAAAAAA" || it.Text == "BBBBBBBBBB" {
					t.Fatalf("option text leaked into layout: %q", it.Text)
				}
			}
		}
		for _, c := range b.Children {
			walk(c)
		}
	}
	walk(root)
}

func TestFlexItemAutoBasisPreferred(t *testing.T) {
	// A flex item with no width/basis uses its max-content width ("xxx" = 30).
	src := `<html><body style="margin:0"><div style="display:flex">` +
		`<div style="padding:0">xxx</div><div style="width:50px">B</div></div></body></html>`
	outer := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "autobasis.A.W", outer.Children[0].W, 30)
	assertF(t, "autobasis.B.X", outer.Children[1].X, 30)
}

func TestFlexItemBorderBoxBasis(t *testing.T) {
	// flex-basis with border-box: basis 100 includes the 10px padding each side.
	src := `<html><body style="margin:0"><div style="display:flex">` +
		`<div style="box-sizing:border-box;flex-basis:100px;padding:10px">A</div></div></body></html>`
	outer := findBox(layoutHTML(t, src, 300), "div")
	// item main outer (border box) = 100; content width = 80.
	assertF(t, "bbbasis.A.W", outer.Children[0].W, 100)
	assertF(t, "bbbasis.A.ContentW", outer.Children[0].ContentW, 80)
}

func TestFlexItemWidthBorderBox(t *testing.T) {
	src := `<html><body style="margin:0"><div style="display:flex">` +
		`<div style="box-sizing:border-box;width:120px;padding:10px">A</div></div></body></html>`
	outer := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "bbwidth.A.W", outer.Children[0].W, 120)
}

func TestFlexColumnAutoWidthFlexStart(t *testing.T) {
	// Column, flex-start, item with auto width → uses preferred content width.
	src := `<html><body style="margin:0"><div style="display:flex;flex-direction:column;align-items:flex-start">` +
		`<div style="padding:0;height:10px">xx</div></div></body></html>`
	outer := findBox(layoutHTML(t, src, 200), "div")
	assertF(t, "colauto.A.W", outer.Children[0].W, 20) // "xx" = 20
}

func TestInlineTextFlowsBelowFloat(t *testing.T) {
	// A left float 250 wide leaves 50px beside it; a 60px first word cannot fit
	// and drops below the 40px-tall float.
	src := `<html><body style="margin:0">` +
		`<div style="float:left;width:250px;height:40px">F</div>` +
		`<p style="margin:0">wxyzab tail</p></body></html>`
	root := layoutHTML(t, src, 300)
	p := findBox(root, "p")
	// First line drops to y=40 (below the float) where full width is available.
	assertF(t, "drop.line0.Y", p.Lines[0].Y, 40)
	assertF(t, "drop.line0.X", p.Lines[0].X, 0)
}

func TestPreTextAlignRight(t *testing.T) {
	src := "<html><body style=\"margin:0\"><pre style=\"margin:0;padding:0;text-align:right;width:100px\">ab</pre></body></html>"
	pre := findBox(layoutHTML(t, src, 300), "pre")
	it := pre.Lines[0].Items[0]
	assertF(t, "preright.offset", it.X-pre.ContentX, 80) // 100-20
}

func TestEmptyBlockZeroHeight(t *testing.T) {
	// A block with no content and no border/padding is zero height and its
	// border-top falls at the fallback position.
	src := `<html><body style="margin:0;padding:0"><div style="margin:0;padding:0"></div></body></html>`
	div := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "empty.H", div.H, 0)
	assertF(t, "empty.Y", div.Y, 0)
}

func TestCenteringNoLeftoverNoShift(t *testing.T) {
	// margin:0 auto with a width equal to the container leaves no room to centre.
	src := `<html><body style="margin:0"><div style="width:300px;margin:0 auto">x</div></body></html>`
	div := findBox(layoutHTML(t, src, 300), "div")
	assertF(t, "noshift.X", div.X, 0)
}

func TestNegativeContentHeightClamp(t *testing.T) {
	// Explicit border-box height smaller than the borders clamps content to 0.
	src := `<html><body style="margin:0"><div style="box-sizing:border-box;height:4px;border:5px solid black;margin:0">x</div></body></html>`
	div := findBox(layoutHTML(t, src, 300), "div")
	// border box height is at least the borders (10px); content clamped to 0.
	assertF(t, "negh.H", div.H, 10)
}

func TestCollectInlineFromNilStyle(t *testing.T) {
	// An element absent from the style map falls back to the run's style.
	l := &layouter{sm: css.StyleMap{}, m: fakeMeasurer{}, floats: &floatCtx{}}
	span := &dom.Node{Type: dom.Element, Tag: "span"}
	span.Children = []*dom.Node{{Type: dom.Text, Text: "hi"}}
	st := &css.Style{FontSize: 16, LineHeight: css.LineHeight{Normal: true}}
	items := l.collectInlineFrom([]*dom.Node{span}, st, false)
	if len(items) != 1 || items[0].Text != "hi" {
		t.Fatalf("nil-style inline = %v", texts(items))
	}
}

func TestTableAllEmptyCellsEqualSplit(t *testing.T) {
	// Cells with no text have zero natural width → equal split across columns.
	src := `<html><body style="margin:0"><table style="width:200px"><tr>` +
		`<td style="padding:0"></td><td style="padding:0"></td></tr></table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	row := tbl.Children[0]
	assertF(t, "eqsplit.c0.W", row.Children[0].W, 100)
	assertF(t, "eqsplit.c1.W", row.Children[1].W, 100)
}

func TestTableRowWithNoCells(t *testing.T) {
	// A row present but with no table-cell children yields ncols 0 → no layout.
	src := `<html><body style="margin:0"><table style="width:100px"><tr></tr></table></body></html>`
	tbl := findBox(layoutHTML(t, src, 300), "table")
	if len(tbl.Children) != 0 {
		t.Errorf("no-cell rows should not lay out, got %d", len(tbl.Children))
	}
}

func TestTranslateNoOp(t *testing.T) {
	b := &Box{X: 5, Y: 5}
	translateBox(b, 0, 0) // no-op path
	if b.X != 5 || b.Y != 5 {
		t.Errorf("no-op translate changed box: %+v", b)
	}
	translateBox(nil, 1, 1) // nil-safe
}

func TestPreferredWidthImage(t *testing.T) {
	l := &layouter{m: fakeMeasurer{}, floats: &floatCtx{},
		imgSize: map[*dom.Node][2]float64{}}
	img := &dom.Node{Type: dom.Element, Tag: "img"}
	l.imgSize[img] = [2]float64{70, 30}
	st := &css.Style{}
	if got := l.preferredWidth(img, st); got != 70 {
		t.Errorf("img preferred = %v want 70", got)
	}
}
