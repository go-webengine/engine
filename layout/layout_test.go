// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// fakeMeasurer gives every rune a 10px advance (independent of size), a space
// 10px, ascent 8 and line height 20 — so geometry is exactly predictable.
type fakeMeasurer struct{}

func (fakeMeasurer) Measure(text string, _ css.FontFamily, _ float64, _ int) float64 {
	return float64(len([]rune(text)) * 10)
}
func (fakeMeasurer) Metrics(css.FontFamily, float64, int) (float64, float64) { return 8, 20 }

func layoutHTML(t *testing.T, src string, vpW float64) *Box {
	t.Helper()
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sm := css.Cascade(root)
	box, _ := LayoutDocument(root, sm, vpW, fakeMeasurer{}, nil)
	return box
}

func findBox(b *Box, tag string) *Box {
	if b == nil {
		return nil
	}
	if b.Node != nil && b.Node.Type == dom.Element && b.Node.Tag == tag {
		return b
	}
	for _, c := range b.Children {
		if got := findBox(c, tag); got != nil {
			return got
		}
	}
	return nil
}

func firstLineItems(b *Box) []*InlineItem {
	if b == nil {
		return nil
	}
	if len(b.Lines) > 0 {
		return b.Lines[0].Items
	}
	for _, c := range b.Children {
		if items := firstLineItems(c); items != nil {
			return items
		}
	}
	return nil
}

func TestBlockBoxMetrics(t *testing.T) {
	src := `<html><body style="margin:0"><div style="width:100px;padding:10px;margin:5px">hi</div></body></html>`
	root := layoutHTML(t, src, 1024)
	div := findBox(root, "div")
	if div == nil {
		t.Fatal("no div box")
	}
	// margin 5, padding 10, content width 100.
	assertF(t, "div.X", div.X, 5)
	assertF(t, "div.Y", div.Y, 5)
	assertF(t, "div.W", div.W, 120) // 10 + 100 + 10
	assertF(t, "div.H", div.H, 40)  // 10 + 20(line) + 10
	assertF(t, "div.ContentX", div.ContentX, 15)
	assertF(t, "div.ContentY", div.ContentY, 15)
	assertF(t, "div.ContentW", div.ContentW, 100)

	// The single word "hi" (2 runes → 20px) sits at the content origin, on the
	// shared baseline (ascent==line ascent → top offset 0).
	items := div.Lines
	if len(items) != 1 || len(items[0].Items) != 1 {
		t.Fatalf("expected one line with one item, got %v", items)
	}
	it := items[0].Items[0]
	assertF(t, "hi.X", it.X, 15)
	assertF(t, "hi.Y", it.Y, 15)
	assertF(t, "hi.Width", it.Width, 20)
}

func TestBlockAutoWidthAndNesting(t *testing.T) {
	// body has UA margin 8; html margin 0. At viewport 200, body content width
	// is 200 - 8 - 8 = 184, and a block child fills it.
	src := `<html><body><div>x</div></body></html>`
	root := layoutHTML(t, src, 200)
	body := findBox(root, "body")
	assertF(t, "body.X", body.X, 8)
	assertF(t, "body.ContentW", body.ContentW, 184)
	div := findBox(root, "div")
	assertF(t, "div.ContentW", div.ContentW, 184)
	assertF(t, "div.X", div.X, 8)
}

func TestInlineWrappingInFlow(t *testing.T) {
	// content width chosen so exactly two words fit per line.
	// words a b c d each 10px, space 10px. "a"+" "+"b" = 30. +" "+"c" = 50 > 35.
	src := `<html><body style="margin:0"><div style="width:35px">a b c d</div></body></html>`
	root := layoutHTML(t, src, 1024)
	div := findBox(root, "div")
	if len(div.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(div.Lines))
	}
	// second line first item positioned at content origin (x=0 content-relative).
	l2 := div.Lines[1]
	assertF(t, "line2.item0.X", l2.Items[0].X, div.ContentX)
	assertF(t, "div.H", div.H, 40) // two 20px lines
}

func TestTextAlignCenterAndRight(t *testing.T) {
	center := findBox(layoutHTML(t,
		`<html><body style="margin:0"><div style="width:100px;text-align:center">ab</div></body></html>`, 1024), "div")
	it := center.Lines[0].Items[0]
	// used width 20, cw 100 → offset 40.
	assertF(t, "center.offset", it.X-center.ContentX, 40)

	right := findBox(layoutHTML(t,
		`<html><body style="margin:0"><div style="width:100px;text-align:right">ab</div></body></html>`, 1024), "div")
	itr := right.Lines[0].Items[0]
	assertF(t, "right.offset", itr.X-right.ContentX, 80)
}

func TestPrePreservesWhitespaceAndNewlines(t *testing.T) {
	src := "<html><body style=\"margin:0\"><pre style=\"margin:0;padding:0\">x  y\nzz</pre></body></html>"
	pre := findBox(layoutHTML(t, src, 1024), "pre")
	if len(pre.Lines) != 2 {
		t.Fatalf("expected 2 pre lines, got %d", len(pre.Lines))
	}
	// "x  y" preserved (4 chars incl. two spaces → 40px), no wrapping.
	first := pre.Lines[0].Items[0]
	if first.Text != "x  y" {
		t.Errorf("pre line0 text = %q", first.Text)
	}
	assertF(t, "pre.line0.width", first.Width, 40)
	if pre.Lines[1].Items[0].Text != "zz" {
		t.Errorf("pre line1 = %q", pre.Lines[1].Items[0].Text)
	}
}

func TestPreDoesNotWrap(t *testing.T) {
	src := `<html><body><pre style="width:10px">aaaaaa</pre></body></html>`
	pre := findBox(layoutHTML(t, src, 1024), "pre")
	if len(pre.Lines) != 1 {
		t.Fatalf("pre must not wrap; got %d lines", len(pre.Lines))
	}
}

func TestDisplayNoneSkipped(t *testing.T) {
	src := `<html><body><div><span style="display:none">HIDDEN</span>y</div></body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "div"))
	if len(items) != 1 || items[0].Text != "y" {
		t.Fatalf("display:none not skipped: %v", texts(items))
	}
}

func TestInlineImageFromAttrs(t *testing.T) {
	src := `<html><body><p><img width="30" height="40px">after</p></body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "p"))
	if len(items) != 2 {
		t.Fatalf("expected image + word, got %v", texts(items))
	}
	img := items[0]
	if img.Image == nil {
		t.Fatal("first item should be an image")
	}
	assertF(t, "img.ImgW", img.ImgW, 30)
	assertF(t, "img.ImgH", img.ImgH, 40)
}

func TestImageSizeMapOverride(t *testing.T) {
	root, _ := dom.Parse(`<html><body><p><img></p></body></html>`)
	sm := css.Cascade(root)
	img := dom.Find(root, "img")
	sizes := map[*dom.Node][2]float64{img: {50, 60}}
	box, _ := LayoutDocument(root, sm, 1024, fakeMeasurer{}, sizes)
	items := firstLineItems(findBox(box, "p"))
	if len(items) != 1 || items[0].ImgW != 50 || items[0].ImgH != 60 {
		t.Fatalf("size map override failed: %v", items)
	}
}

func TestImageZeroSizeSkipped(t *testing.T) {
	// No width/height and no size map → intrinsic 0 → not laid out.
	src := `<html><body><p><img src="x.png">t</p></body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "p"))
	if len(items) != 1 || items[0].Text != "t" {
		t.Fatalf("zero-size image should be skipped: %v", texts(items))
	}
}

func TestBrInFlow(t *testing.T) {
	src := `<html><body style="margin:0"><div style="margin:0;padding:0">a<br>b</div></body></html>`
	div := findBox(layoutHTML(t, src, 1024), "div")
	if len(div.Lines) != 2 {
		t.Fatalf("br should force 2 lines, got %d", len(div.Lines))
	}
	assertF(t, "div.H", div.H, 40)
}

func TestEmptyDocument(t *testing.T) {
	// A document with no element produces an empty box and zero height.
	root := &dom.Node{Type: dom.Document}
	box, h := LayoutDocument(root, css.StyleMap{}, 100, fakeMeasurer{}, nil)
	if box == nil || h != 0 {
		t.Fatalf("empty doc: box=%v h=%v", box, h)
	}
}

func TestAttrFloat(t *testing.T) {
	el := &dom.Node{Type: dom.Element, Tag: "img", Attr: map[string]string{
		"width": "12px", "bad": "xyz",
	}}
	if got := attrFloat(el, "width"); got != 12 {
		t.Errorf("width = %v", got)
	}
	if got := attrFloat(el, "bad"); got != 0 {
		t.Errorf("bad = %v", got)
	}
	if got := attrFloat(el, "missing"); got != 0 {
		t.Errorf("missing = %v", got)
	}
}

func TestMixedInlineAndBlockChildren(t *testing.T) {
	// A div with inline text, then a block <p>, then more inline text generates
	// two anonymous inline boxes around the block child.
	src := `<html><body style="margin:0"><div style="margin:0;padding:0">before<p style="margin:0">mid</p>after</div></body></html>`
	div := findBox(layoutHTML(t, src, 1024), "div")
	// Children: anon(before), p(mid), anon(after) → 3 boxes, stacked.
	if len(div.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(div.Children))
	}
	if !div.Children[0].Anonymous || div.Children[1].Node == nil || !div.Children[2].Anonymous {
		t.Errorf("child kinds = %+v", div.Children)
	}
	// Vertical stacking: before at y=0, p next, after last.
	assertF(t, "before.Y", div.Children[0].Y, 0)
	assertF(t, "before.H", div.Children[0].H, 20)
	assertF(t, "p.Y", div.Children[1].Y, 20)
	assertF(t, "after.Y", div.Children[2].Y, 40)
	if div.Children[0].Lines[0].Items[0].Text != "before" {
		t.Errorf("anon0 text = %q", div.Children[0].Lines[0].Items[0].Text)
	}
}

func TestMixedRunWithInlineElement(t *testing.T) {
	// An inline element (<strong>) shares an anonymous box with adjacent text,
	// alongside a block sibling — exercising element handling in a mixed run.
	src := `<html><body style="margin:0"><div style="margin:0;padding:0">` +
		`x<strong>bold</strong><p style="margin:0">para</p>tail</div></body></html>`
	div := findBox(layoutHTML(t, src, 1024), "div")
	if len(div.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(div.Children))
	}
	anon0 := div.Children[0]
	got := texts(anon0.Lines[0].Items)
	if len(got) != 2 || got[0] != "x" || got[1] != "bold" {
		t.Fatalf("anon0 items = %v", got)
	}
	if !anon0.Lines[0].Items[1].Style.Bold() {
		t.Error("<strong> item should be bold")
	}
}

func TestNegativeContentWidthClamped(t *testing.T) {
	// Margins larger than the viewport must clamp content width to 0, not panic.
	src := `<html><body style="margin:0"><div style="margin:2000px">x</div></body></html>`
	div := findBox(layoutHTML(t, src, 100), "div")
	assertF(t, "clamped ContentW", div.ContentW, 0)
}

func TestTrailingBrEmptyLine(t *testing.T) {
	// A trailing <br> yields a final empty line with fallback height.
	src := `<html><body style="margin:0"><div style="margin:0;padding:0">a<br></div></body></html>`
	div := findBox(layoutHTML(t, src, 1024), "div")
	if len(div.Lines) != 2 || len(div.Lines[1].Items) != 0 {
		t.Fatalf("expected trailing empty line, got %d lines", len(div.Lines))
	}
	assertF(t, "empty line height", div.Lines[1].H, 20) // fallback metrics
}

func TestPreDoubleNewline(t *testing.T) {
	src := "<html><body><pre style=\"margin:0;padding:0\">a\n\nb</pre></body></html>"
	pre := findBox(layoutHTML(t, src, 1024), "pre")
	if len(pre.Lines) != 3 {
		t.Fatalf("expected 3 lines (a, blank, b), got %d", len(pre.Lines))
	}
	if len(pre.Lines[1].Items) != 0 {
		t.Errorf("middle line should be blank")
	}
}

func TestMissingStyleFallback(t *testing.T) {
	// Elements absent from the StyleMap fall back to a default block style
	// without panicking.
	root, _ := dom.Parse(`<html><body><div>hi</div></body></html>`)
	box, h := LayoutDocument(root, css.StyleMap{}, 200, fakeMeasurer{}, nil)
	if box == nil || h <= 0 {
		t.Fatalf("fallback layout failed: box=%v h=%v", box, h)
	}
}

func assertF(t *testing.T, name string, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func texts(items []*InlineItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Text)
	}
	return out
}
