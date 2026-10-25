// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"math"
	"testing"
)

// A run that reaches its line by the last bit of a float64 fits it: two sums of
// the same glyph advances (a box's max-content width, then the line's own) are
// not the same float. One that is really too wide still does not.
func TestWrapOneLineToleratesTheLastBitOfASum(t *testing.T) {
	const maxW = 117.61900000000001
	over := math.Nextafter(maxW, 1000) // one ulp wider
	for _, tc := range []struct {
		name  string
		width float64
		fits  bool
	}{
		{"exactly", maxW, true},
		{"one ulp over", over, true},
		{"a hundredth of a pixel over", maxW + 0.01, true},
		{"a sixty-fourth of a pixel over", maxW + fitEpsilon, true},
		{"a tenth of a pixel over", maxW + 0.1, false},
		{"a pixel over", maxW + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, consumed := wrapOneLine([]*InlineItem{{Text: "chapter", Width: tc.width}}, maxW)
			if got := consumed == 1 && len(line.Items) == 1; got != tc.fits {
				t.Errorf("a run %.9f wide on a line of %.9f: fits = %v, want %v", tc.width, maxW, got, tc.fits)
			}
		})
	}
	// a second word on the line: the sum of the two against the line
	items := []*InlineItem{{Text: "a", Width: 50}, {Text: "b", Width: maxW - 54 + 1e-12, SpaceBefore: 4}}
	if _, consumed := wrapOneLine(items, maxW); consumed != 2 {
		t.Errorf("two words that fill the line to the last bit were split: consumed %d", consumed)
	}
	items[1].Width = maxW - 54 + 0.5
	if _, consumed := wrapOneLine(items, maxW); consumed != 1 {
		t.Errorf("two words half a pixel too wide were kept together: consumed %d", consumed)
	}
}
