// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/layout"
)

// A marker with no style paints nothing (and does not panic); a text marker
// of a bullet type — ::marker content on a disc list — is painted as text,
// not as the disc's shape.
func TestPaintMarkerTextAndNilStyle(t *testing.T) {
	blank := paintMarkerOnly(60, 30, &layout.Marker{Type: css.ListDisc, Text: "•", Glyph: true, X: 10, Y: 10, W: 6, H: 6})
	for _, p := range blank.Pix {
		if p != 0xFF {
			t.Fatal("a marker with no style painted something")
		}
	}
	st := &css.Style{Color: markerBlack, FontSize: 16, FontWeight: 400, FontFamily: css.Sans, Visibility: css.VisibilityVisible}
	text := paintMarkerOnly(60, 30, &layout.Marker{Type: css.ListDisc, Text: "[x]", Style: st, X: 2, Y: 2, W: 30, Ascent: 14})
	ink := 0
	for i := 0; i < len(text.Pix); i += 4 {
		if text.Pix[i] < 0x80 {
			ink++
		}
	}
	if ink == 0 {
		t.Error("a text marker of a bullet type painted no text")
	}
}
