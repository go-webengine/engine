// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"testing"

	"github.com/go-webengine/engine/css"
)

func TestCSSImageSize(t *testing.T) {
	// Intrinsic 900x298 (the go.dev Google wordmark shape).
	cases := []struct {
		name       string
		st         *css.Style
		iw, ih     int
		wantW      int
		wantH      int
		wantOK     bool
	}{
		{"nil style keeps intrinsic", nil, 900, 298, 0, 0, false},
		{"no CSS dims keeps intrinsic",
			&css.Style{Width: css.Length{Auto: true}, Height: css.Length{Auto: true}}, 900, 298, 0, 0, false},
		{"height only scales width by aspect",
			&css.Style{Width: css.Length{Auto: true}, Height: css.Length{Px: 24}}, 900, 298, 72, 24, true},
		{"width only scales height by aspect",
			&css.Style{Width: css.Length{Px: 100}, Height: css.Length{Auto: true}}, 200, 100, 100, 50, true},
		{"both axes set uses both",
			&css.Style{Width: css.Length{Px: 120}, Height: css.Length{Px: 40}}, 900, 298, 120, 40, true},
		{"percentage width is ignored (needs containing block)",
			&css.Style{Width: css.Length{Percent: 0.5, IsPercent: true}, Height: css.Length{Auto: true}}, 900, 298, 0, 0, false},
		{"zero intrinsic reports false",
			&css.Style{Height: css.Length{Px: 24}}, 0, 100, 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := cssImageSize(c.st, c.iw, c.ih)
		if ok != c.wantOK || (ok && (w != c.wantW || h != c.wantH)) {
			t.Errorf("%s: got (%d,%d,%v) want (%d,%d,%v)", c.name, w, h, ok, c.wantW, c.wantH, c.wantOK)
		}
	}
}

func TestIRound(t *testing.T) {
	cases := map[float64]int{0.0: 1, 0.4: 1, 0.6: 1, 1.4: 1, 1.6: 2, 71.5: 72, 72.4: 72}
	for in, want := range cases {
		if got := iround(in); got != want {
			t.Errorf("iround(%v) = %d want %d", in, got, want)
		}
	}
}
