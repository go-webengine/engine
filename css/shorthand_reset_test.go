// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

// A shorthand sets all of its longhands; the omitted ones get their initial
// values (CSS Cascade 4 §3; CSS Backgrounds 3 §2.10).
func TestShorthandsResetOmittedLonghands(t *testing.T) {
	s := newStyle()
	applyOn(s, "background-image", "linear-gradient(red,red)", 16)
	applyOn(s, "background-size", "10px 10px", 16)
	applyOn(s, "background-repeat", "no-repeat", 16)
	applyOn(s, "background-position", "5px 5px", 16)
	applyOn(s, "background", "transparent", 16)
	if s.BackgroundImages != nil || s.BackgroundSize != nil || s.BackgroundRepeat != nil || s.BackgroundPosition != nil || s.Background != Transparent {
		t.Errorf("background:transparent left %+v %v %v %v %v", s.BackgroundImages, s.BackgroundSize, s.BackgroundRepeat, s.BackgroundPosition, s.Background)
	}

	s = newStyle()
	applyOn(s, "flex-wrap", "wrap", 16)
	applyOn(s, "flex-flow", "column", 16)
	if s.FlexDirection != FlexColumn || s.FlexWrap != FlexNoWrap {
		t.Errorf("flex-flow:column = %v %v, want column nowrap", s.FlexDirection, s.FlexWrap)
	}
	applyOn(s, "flex-flow", "wrap bogus", 16) // invalid: no change
	applyOn(s, "flex-flow", "", 16)           // empty: no change
	if s.FlexDirection != FlexColumn || s.FlexWrap != FlexNoWrap {
		t.Errorf("an invalid flex-flow changed the style: %v %v", s.FlexDirection, s.FlexWrap)
	}
	applyOn(s, "flex-flow", "wrap", 16)
	if s.FlexDirection != FlexRow || s.FlexWrap != FlexWrapOn {
		t.Errorf("flex-flow:wrap = %v %v, want row wrap", s.FlexDirection, s.FlexWrap)
	}

	s = newStyle()
	applyOn(s, "list-style-position", "inside", 16)
	applyOn(s, "list-style", "square", 16)
	if s.ListStyleType != ListSquare || s.ListStylePosition != ListOutside {
		t.Errorf("list-style:square = %v %v, want square outside", s.ListStyleType, s.ListStylePosition)
	}
	applyOn(s, "list-style", "inside url(a b.png) decimal", 16)
	if s.ListStyleType != ListDecimal || s.ListStylePosition != ListInside {
		t.Errorf("list-style with url = %v %v", s.ListStyleType, s.ListStylePosition)
	}
	applyOn(s, "list-style", "bogus", 16) // invalid: no change
	applyOn(s, "list-style", "", 16)      // empty: no change
	if s.ListStyleType != ListDecimal || s.ListStylePosition != ListInside {
		t.Errorf("an invalid list-style changed the style: %v %v", s.ListStyleType, s.ListStylePosition)
	}
	applyOn(s, "list-style", "outside", 16)
	if s.ListStyleType != ListDisc {
		t.Errorf("list-style:outside type = %v, want disc", s.ListStyleType)
	}
}
