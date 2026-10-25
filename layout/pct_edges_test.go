// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"testing"

	"github.com/go-webengine/engine/css"
)

func TestResolvePercentEdges(t *testing.T) {
	plain := &css.Style{}
	if got := resolvePercentEdges(plain, 500); got != plain {
		t.Error("a style with no percentage edge is returned as is")
	}
	st := &css.Style{}
	st.PaddingPct = css.Pct4{Set: [4]bool{true, true, true, true}, V: [4]float64{0.1, 0.2, 0.3, 0.4}}
	st.MarginVPct = css.Pct4{Set: [4]bool{true, false, true, false}, V: [4]float64{0.5, 0, 0.25, 0}}
	st.Margin.Right = 7
	got := resolvePercentEdges(st, 200)
	if got.Padding != (css.Edges{Top: 20, Right: 40, Bottom: 60, Left: 80}) {
		t.Errorf("padding = %+v", got.Padding)
	}
	if got.Margin != (css.Edges{Top: 100, Right: 7, Bottom: 50}) {
		t.Errorf("margin = %+v", got.Margin)
	}
	if got.PaddingPct.Any() || got.MarginVPct.Any() {
		t.Error("the resolved style keeps no percentage")
	}
	if !st.PaddingPct.Any() || st.Padding != (css.Edges{}) {
		t.Error("the shared style was modified")
	}
	// a negative containing width (over-constrained) resolves to 0
	if got := resolvePercentEdges(st, -30); got.Padding != (css.Edges{}) {
		t.Errorf("negative basis: %+v", got.Padding)
	}
}
