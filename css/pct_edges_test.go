// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

// Percentage padding and margin-top/-bottom are kept as fractions of the
// containing block's width (CSS Box 3 section 5), a later length clears them.
func TestPercentEdgesParse(t *testing.T) {
	var s Style
	apply := func(p, v string) { s.apply(Declaration{Property: p, Value: v}, 16, nil) }
	if s.PaddingPct.Any() || s.MarginVPct.Any() {
		t.Fatal("a fresh style has no percentage edge")
	}
	apply("padding", "10% 20% 5% 1px")
	if !s.PaddingPct.Any() || s.PaddingPct.V != [4]float64{0.1, 0.2, 0.05, 0} ||
		s.PaddingPct.Set != [4]bool{true, true, true, false} || s.Padding.Left != 1 || s.Padding.Top != 0 {
		t.Errorf("padding = %+v %+v", s.PaddingPct, s.Padding)
	}
	// auto is 0 in the shorthand
	apply("padding", "auto 3px")
	if s.PaddingPct.Any() || s.Padding != (Edges{0, 3, 0, 3}) {
		t.Errorf("padding auto = %+v %+v", s.PaddingPct, s.Padding)
	}
	// a list that does not parse, or has five values, changes nothing
	apply("padding", "1px bogus")
	apply("padding", "1px 2px 3px 4px 5px")
	if s.Padding != (Edges{0, 3, 0, 3}) {
		t.Errorf("a bad padding list changed %+v", s.Padding)
	}
	apply("padding-left", "60%")
	if !s.PaddingPct.Set[3] || s.PaddingPct.V[3] != 0.6 || s.Padding.Left != 0 {
		t.Errorf("padding-left = %+v %+v", s.PaddingPct, s.Padding)
	}
	apply("padding-left", "9px")
	if s.PaddingPct.Set[3] || s.Padding.Left != 9 {
		t.Errorf("a length after a percentage: %+v %+v", s.PaddingPct, s.Padding)
	}
	apply("margin-top", "20%")
	apply("margin-bottom", "bogus")
	apply("margin-bottom", "auto")
	if !s.MarginVPct.Set[0] || s.MarginVPct.V[0] != 0.2 || s.MarginVPct.Set[2] {
		t.Errorf("margin-top = %+v", s.MarginVPct)
	}
	apply("margin", "5px")
	if s.MarginVPct.Any() || s.Margin.Top != 5 {
		t.Errorf("the margin shorthand resets: %+v %+v", s.MarginVPct, s.Margin)
	}
}
