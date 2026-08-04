// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

func autoStyle() *css.Style {
	return &css.Style{Display: css.DisplayBlock,
		Width: css.Length{Auto: true}, MinWidth: css.Length{Auto: true},
		MaxWidth: css.Length{Auto: true}, Height: css.Length{Auto: true},
		FlexBasis: css.Length{Auto: true}, LineHeight: css.LineHeight{Normal: true}}
}

func TestLayoutIsolatedNegativeWidthClamped(t *testing.T) {
	l := &layouter{sm: css.StyleMap{}, m: fakeMeasurer{}, floats: &floatCtx{}}
	node := &dom.Node{Type: dom.Element, Tag: "div"}
	box := l.layoutIsolated(node, autoStyle(), -5)
	assertF(t, "iso.ContentW", box.ContentW, 0) // negative content width clamps to 0
}

func TestDistributeZeroItems(t *testing.T) {
	off, gap := distribute(css.JustifyStart, 100, 0)
	if off != 0 || gap != 0 {
		t.Errorf("distribute(_,100,0) = %v,%v want 0,0", off, gap)
	}
}

func TestFindSlotBoxWiderThanRegion(t *testing.T) {
	fc := &floatCtx{}
	// A box wider than the whole region cannot fit and nextEdge cannot help
	// (no floats), so it is placed at the top on each side.
	x, y := fc.findSlot(css.FloatLeft, 0, 400, 20, 0, 300)
	assertF(t, "wide.left.x", x, 0)
	assertF(t, "wide.left.y", y, 0)
	xr, yr := fc.findSlot(css.FloatRight, 0, 400, 20, 0, 300)
	assertF(t, "wide.right.x", xr, -100) // 300 - 400
	assertF(t, "wide.right.y", yr, 0)
}

func TestFloatAutoWidthMarginsExceedContainer(t *testing.T) {
	// float:left, width auto, with horizontal margins larger than the container
	// clamps the available width to 0.
	src := `<html><body style="margin:0">` +
		`<div style="float:left;margin-left:200px;margin-right:200px" id="f">x</div>` +
		`<p style="margin:0">a</p></body></html>`
	f := findBoxByID(layoutHTML(t, src, 300), "f")
	if f == nil {
		t.Fatal("no float box")
	}
	assertF(t, "autowide.ContentW", f.ContentW, 0)
}

func TestFloatBorderBoxWidthUnderPaddingClamped(t *testing.T) {
	// border-box float whose width is smaller than its padding clamps to 0.
	src := `<html><body style="margin:0">` +
		`<div style="float:left;box-sizing:border-box;width:5px;padding:20px;height:10px" id="f">x</div>` +
		`<p style="margin:0">a</p></body></html>`
	f := findBoxByID(layoutHTML(t, src, 300), "f")
	assertF(t, "bbunder.ContentW", f.ContentW, 0)
}

func TestRightFloatBlockChildTranslated(t *testing.T) {
	// A right float (translated by dx>0) containing a block child exercises the
	// recursive child translation.
	src := `<html><body style="margin:0">` +
		`<div style="float:right;width:100px" id="f"><p style="margin:0;padding:0">inner</p></div>` +
		`<p style="margin:0">a</p></body></html>`
	f := findBoxByID(layoutHTML(t, src, 300), "f")
	if f == nil || len(f.Children) == 0 {
		t.Fatal("right float should have a translated block child")
	}
	assertF(t, "rfbc.f.X", f.X, 200)                 // 300 - 100 (right float)
	assertF(t, "rfbc.child.X", f.Children[0].X, 200) // inner block moved with the float
}

func TestBoxWithOnlyDownwardFloatChild(t *testing.T) {
	// A block whose sole content is a float pushed below (no in-flow content)
	// gets its border-top clamped to the float's top; its height is 0.
	// Float A leaves only a 50px sliver, too narrow for the 300px-wide float B,
	// so B drops below A while the inner block has no in-flow content.
	src := `<html><body style="margin:0">` +
		`<div style="float:left;width:250px;height:50px" id="a">A</div>` +
		`<div style="margin:0;padding:0" id="inner">` +
		`<div style="float:left;width:300px;height:20px" id="b">B</div></div>` +
		`</body></html>`
	root := layoutHTML(t, src, 300)
	inner := findBoxByID(root, "inner")
	b := findBoxByID(root, "b")
	assertF(t, "downfloat.b.Y", b.Y, 50)         // dropped below float A
	assertF(t, "downfloat.inner.Y", inner.Y, 50) // border-top clamped to child float
	assertF(t, "downfloat.inner.H", inner.H, 0)  // no in-flow height
}

func TestNegativeMarginContentHeightClamped(t *testing.T) {
	// A child with a large negative top margin pulls content above the parent's
	// content origin, driving content height negative → clamped to 0.
	src := `<html><body style="margin:0;padding:0">` +
		`<div style="margin:0;padding:0" id="d"><p style="margin:-100px 0 0 0">x</p></div></body></html>`
	d := findBoxByID(layoutHTML(t, src, 300), "d")
	assertF(t, "negmar.ContentH", d.ContentH, 0)
}

func TestWidthBoundBorderBoxNegative(t *testing.T) {
	// A border-box bound smaller than the horizontal edges clamps to 0.
	v, ok := widthBound(css.Length{Px: 5}, 100, 40, css.BorderBox)
	if !ok || v != 0 {
		t.Errorf("widthBound border-box negative = %v,%v want 0,true", v, ok)
	}
	// The auto bound reports no constraint.
	if _, ok := widthBound(css.Length{Auto: true}, 100, 40, css.ContentBox); ok {
		t.Error("auto bound should report ok=false")
	}
}

func TestForceOneSkipsLeadingBreak(t *testing.T) {
	items := []*InlineItem{{LineBreak: true}, {Text: "w", Width: 10}}
	line, consumed := forceOne(items)
	if consumed != 2 {
		t.Errorf("forceOne consumed = %d want 2", consumed)
	}
	if len(line.Items) != 1 || line.Items[0].Text != "w" {
		t.Errorf("forceOne line = %v", texts(line.Items))
	}
}
