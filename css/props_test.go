// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

func newStyle() *Style {
	s := initialStyle()
	return &s
}

func applyOn(s *Style, prop, val string, em float64) { s.apply(Declaration{prop, val}, em) }

func TestApplyDisplayValues(t *testing.T) {
	cases := map[string]Display{
		"block": DisplayBlock, "flow-root": DisplayBlock, "grid": DisplayBlock,
		"flex": DisplayFlex, "inline-flex": DisplayFlex,
		"table": DisplayTable, "inline-table": DisplayTable,
		"table-row": DisplayTableRow, "table-cell": DisplayTableCell,
		"table-row-group": DisplayTableRowGroup, "table-header-group": DisplayTableRowGroup,
		"inline-block": DisplayInlineBlock, "inline": DisplayInline, "none": DisplayNone,
	}
	for v, want := range cases {
		s := newStyle()
		applyOn(s, "display", v, 16)
		if s.Display != want {
			t.Errorf("display:%s = %v want %v", v, s.Display, want)
		}
	}
}

func TestApplyBoxSizing(t *testing.T) {
	s := newStyle()
	applyOn(s, "box-sizing", "border-box", 16)
	if s.BoxSizing != BorderBox {
		t.Error("border-box")
	}
	applyOn(s, "box-sizing", "content-box", 16)
	if s.BoxSizing != ContentBox {
		t.Error("content-box")
	}
}

func TestApplyMinMaxWidth(t *testing.T) {
	s := newStyle()
	applyOn(s, "min-width", "100px", 16)
	if s.MinWidth.Px != 100 {
		t.Errorf("min-width = %v", s.MinWidth)
	}
	applyOn(s, "max-width", "600px", 16)
	if s.MaxWidth.Px != 600 {
		t.Errorf("max-width = %v", s.MaxWidth)
	}
	applyOn(s, "max-width", "none", 16)
	if !s.MaxWidth.Auto {
		t.Error("max-width:none should reset to auto")
	}
	applyOn(s, "min-width", "none", 16)
	if !s.MinWidth.Auto {
		t.Error("min-width:none should reset to auto")
	}
	applyOn(s, "height", "50px", 16)
	if s.Height.Px != 50 {
		t.Errorf("height = %v", s.Height)
	}
}

func TestApplyLineHeight(t *testing.T) {
	s := newStyle()
	applyOn(s, "line-height", "normal", 16)
	if !s.LineHeight.Normal {
		t.Error("normal")
	}
	applyOn(s, "line-height", "2", 16)
	if s.LineHeight.Normal || s.LineHeight.Px != 32 {
		t.Errorf("multiplier = %v", s.LineHeight)
	}
	applyOn(s, "line-height", "24px", 16)
	if s.LineHeight.Px != 24 {
		t.Errorf("px = %v", s.LineHeight)
	}
	applyOn(s, "line-height", "150%", 16)
	if s.LineHeight.Px != 24 {
		t.Errorf("pct = %v", s.LineHeight)
	}
	applyOn(s, "line-height", "1.5em", 16)
	if s.LineHeight.Px != 24 {
		t.Errorf("em = %v", s.LineHeight)
	}
	// Invalid is ignored (keeps previous).
	prev := s.LineHeight
	applyOn(s, "line-height", "garbage", 16)
	if s.LineHeight != prev {
		t.Errorf("garbage changed line-height to %v", s.LineHeight)
	}
	// Negative is rejected.
	if _, ok := parseLineHeight("-1", 16); ok {
		t.Error("negative multiplier should fail")
	}
}

func TestApplyFloatClear(t *testing.T) {
	s := newStyle()
	for v, want := range map[string]Float{"left": FloatLeft, "right": FloatRight, "none": FloatNone} {
		applyOn(s, "float", v, 16)
		if s.Float != want {
			t.Errorf("float:%s = %v", v, s.Float)
		}
	}
	for v, want := range map[string]Clear{"left": ClearLeft, "right": ClearRight, "both": ClearBoth, "none": ClearNone} {
		applyOn(s, "clear", v, 16)
		if s.Clear != want {
			t.Errorf("clear:%s = %v", v, s.Clear)
		}
	}
}

func TestApplyFlexProperties(t *testing.T) {
	s := newStyle()
	applyOn(s, "flex-direction", "column", 16)
	if s.FlexDirection != FlexColumn {
		t.Error("column")
	}
	applyOn(s, "flex-direction", "row", 16)
	if s.FlexDirection != FlexRow {
		t.Error("row")
	}
	applyOn(s, "justify-content", "space-between", 16)
	if s.JustifyContent != JustifySpaceBetween {
		t.Error("space-between")
	}
	applyOn(s, "align-items", "center", 16)
	if s.AlignItems != AlignCenterItems {
		t.Error("align center")
	}
	applyOn(s, "flex-grow", "2", 16)
	if s.FlexGrow != 2 {
		t.Errorf("grow = %v", s.FlexGrow)
	}
	applyOn(s, "flex-shrink", "0", 16)
	if s.FlexShrink != 0 {
		t.Errorf("shrink = %v", s.FlexShrink)
	}
	applyOn(s, "flex-basis", "120px", 16)
	if s.FlexBasis.Px != 120 {
		t.Errorf("basis = %v", s.FlexBasis)
	}
	applyOn(s, "flex-basis", "content", 16)
	if !s.FlexBasis.Auto {
		t.Error("basis content → auto")
	}
	// Invalid factors are ignored.
	applyOn(s, "flex-grow", "-1", 16)
	if s.FlexGrow != 2 {
		t.Errorf("negative grow changed to %v", s.FlexGrow)
	}
}

func TestJustifyAndAlignParsing(t *testing.T) {
	jcases := map[string]Justify{
		"flex-start": JustifyStart, "start": JustifyStart, "left": JustifyStart, "normal": JustifyStart,
		"flex-end": JustifyEnd, "right": JustifyEnd, "center": JustifyCenter,
		"space-between": JustifySpaceBetween, "space-around": JustifySpaceAround,
		"space-evenly": JustifySpaceEvenly,
	}
	for v, want := range jcases {
		if got, ok := parseJustify(v); !ok || got != want {
			t.Errorf("justify %s = %v %v", v, got, ok)
		}
	}
	if _, ok := parseJustify("bogus"); ok {
		t.Error("bogus justify should fail")
	}
	acases := map[string]AlignItems{
		"stretch": AlignStretch, "normal": AlignStretch, "flex-start": AlignFlexStart,
		"self-start": AlignFlexStart, "flex-end": AlignFlexEnd, "self-end": AlignFlexEnd,
		"center": AlignCenterItems,
	}
	for v, want := range acases {
		if got, ok := parseAlignItems(v); !ok || got != want {
			t.Errorf("align %s = %v %v", v, got, ok)
		}
	}
	if _, ok := parseAlignItems("bogus"); ok {
		t.Error("bogus align should fail")
	}
}

func TestFlexShorthand(t *testing.T) {
	check := func(v string, g, sh float64, basisAuto bool, basisPx float64) {
		s := newStyle()
		applyOn(s, "flex", v, 16)
		if s.FlexGrow != g || s.FlexShrink != sh {
			t.Errorf("flex:%q grow/shrink = %v/%v want %v/%v", v, s.FlexGrow, s.FlexShrink, g, sh)
		}
		if s.FlexBasis.Auto != basisAuto || (!basisAuto && s.FlexBasis.Px != basisPx) {
			t.Errorf("flex:%q basis = %v want auto=%v px=%v", v, s.FlexBasis, basisAuto, basisPx)
		}
	}
	check("none", 0, 0, true, 0)
	check("auto", 1, 1, true, 0)
	check("initial", 0, 1, true, 0)
	check("1", 1, 1, false, 0)           // 1 → 1 1 0
	check("2 3", 2, 3, false, 0)         // grow shrink
	check("1 0 200px", 1, 0, false, 200) // grow shrink basis
	check("0 0 auto", 0, 0, true, 0)
}

func TestBorderShorthandParsing(t *testing.T) {
	s := newStyle()
	applyOn(s, "border", "3px solid red", 16)
	for _, side := range []BorderSide{s.Border.Top, s.Border.Right, s.Border.Bottom, s.Border.Left} {
		if side.Width != 3 || side.Style != BorderSolid || side.Color != (Color{255, 0, 0, 255}) {
			t.Errorf("border side = %+v", side)
		}
		if !side.paints() {
			t.Error("side should paint")
		}
	}
	// Keyword width and defaulted colour (currentColor).
	s2 := newStyle()
	s2.Color = Color{10, 20, 30, 255}
	applyOn(s2, "border", "thick dashed", 16)
	if s2.Border.Top.Width != 5 || s2.Border.Top.Style != BorderSolid {
		t.Errorf("thick dashed = %+v", s2.Border.Top)
	}
	if s2.Border.Top.Color != (Color{10, 20, 30, 255}) {
		t.Errorf("default colour = %+v", s2.Border.Top.Color)
	}
	// Per-side shorthand.
	s3 := newStyle()
	applyOn(s3, "border-left", "2px solid blue", 16)
	if s3.Border.Left.Width != 2 || s3.Border.Left.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("border-left = %+v", s3.Border.Left)
	}
	applyOn(s3, "border-top", "1px dotted green", 16)
	applyOn(s3, "border-right", "4px double gold", 16)
	applyOn(s3, "border-bottom", "medium groove black", 16)
	if s3.Border.Bottom.Width != 3 {
		t.Errorf("medium = %v", s3.Border.Bottom.Width)
	}
}

func TestBorderLonghands(t *testing.T) {
	s := newStyle()
	applyOn(s, "border-width", "1px 2px 3px 4px", 16)
	if s.Border.Top.Width != 1 || s.Border.Right.Width != 2 || s.Border.Bottom.Width != 3 || s.Border.Left.Width != 4 {
		t.Errorf("border-width 4 = %+v", s.Border)
	}
	applyOn(s, "border-width", "thin", 16)
	if s.Border.Top.Width != 1 || s.Border.Left.Width != 1 {
		t.Errorf("border-width keyword = %+v", s.Border)
	}
	applyOn(s, "border-style", "solid", 16)
	if s.Border.Top.Style != BorderSolid {
		t.Error("border-style")
	}
	applyOn(s, "border-color", "red green blue", 16) // top/right/bottom/left = r/g/b/g
	if s.Border.Top.Color != (Color{255, 0, 0, 255}) || s.Border.Bottom.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("border-color 3 = %+v", s.Border)
	}
	applyOn(s, "border-top-width", "6px", 16)
	applyOn(s, "border-right-width", "thick", 16)
	applyOn(s, "border-bottom-width", "7px", 16)
	applyOn(s, "border-left-width", "8px", 16)
	if s.Border.Top.Width != 6 || s.Border.Right.Width != 5 || s.Border.Bottom.Width != 7 || s.Border.Left.Width != 8 {
		t.Errorf("border edge widths = %+v", s.Border)
	}
	// Invalid longhands are ignored.
	applyOn(s, "border-width", "notalength", 16)
	applyOn(s, "border-style", "notastyle", 16)
	applyOn(s, "border-color", "notacolor", 16)
}

func TestBorderWidthsHelper(t *testing.T) {
	b := Borders{
		Top:    BorderSide{Width: 2, Style: BorderSolid, Color: Color{0, 0, 0, 255}},
		Bottom: BorderSide{Width: 4, Style: BorderNone, Color: Color{0, 0, 0, 255}},
	}
	w := b.Widths()
	if w.Top != 2 {
		t.Errorf("top width = %v", w.Top)
	}
	if w.Bottom != 0 {
		t.Errorf("none-style bottom should contribute 0, got %v", w.Bottom)
	}
	if b.Top.paints() != true || b.Bottom.paints() != false {
		t.Error("paints() wrong")
	}
}

func TestMarginAutoParsing(t *testing.T) {
	s := newStyle()
	applyOn(s, "margin", "0 auto", 16)
	if !s.MarginLeftAuto || !s.MarginRightAuto {
		t.Errorf("margin 0 auto: autos = %v %v", s.MarginLeftAuto, s.MarginRightAuto)
	}
	if s.Margin.Top != 0 || s.Margin.Left != 0 {
		t.Errorf("margin 0 auto edges = %+v", s.Margin)
	}
	s2 := newStyle()
	applyOn(s2, "margin-left", "auto", 16)
	if !s2.MarginLeftAuto {
		t.Error("margin-left:auto")
	}
	applyOn(s2, "margin-right", "12px", 16)
	if s2.MarginRightAuto || s2.Margin.Right != 12 {
		t.Errorf("margin-right = %v %v", s2.MarginRightAuto, s2.Margin.Right)
	}
	// 4-value margin with autos.
	s3 := newStyle()
	applyOn(s3, "margin", "1px auto 2px auto", 16)
	if s3.Margin.Top != 1 || s3.Margin.Bottom != 2 || !s3.MarginLeftAuto || !s3.MarginRightAuto {
		t.Errorf("4-val margin = %+v autos %v %v", s3.Margin, s3.MarginLeftAuto, s3.MarginRightAuto)
	}
	// Percentage margins collapse to 0.
	s4 := newStyle()
	applyOn(s4, "margin", "10%", 16)
	if s4.Margin.Top != 0 {
		t.Errorf("percent margin = %v", s4.Margin)
	}
	// Invalid margin token ignored.
	s5 := newStyle()
	applyOn(s5, "margin", "1px bad", 16)
	if s5.Margin.Top != 0 {
		t.Errorf("invalid margin applied: %+v", s5.Margin)
	}
}

func TestFourValuesEmpty(t *testing.T) {
	if _, _, _, _, ok := fourValues([]int{}); ok {
		t.Error("empty should fail")
	}
	if _, _, _, _, ok := fourValues([]int{1, 2, 3, 4, 5}); ok {
		t.Error("five should fail")
	}
}
