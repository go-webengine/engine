// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"testing"

	"github.com/go-webengine/engine/css"
)

func TestGradientTiles(t *testing.T) {
	bx := image.Rect(10, 20, 110, 70) // 100x50
	px := func(v float64) css.Length { return css.Length{Px: v} }
	pct := func(v float64) css.Length { return css.Length{Percent: v, IsPercent: true} }
	auto := css.Length{Auto: true}
	origin := css.BgPosition{X: pct(0), Y: pct(0)}

	// auto, cover and contain: one tile, the positioning area.
	for _, k := range []css.BgSizeKind{css.SizeAuto, css.SizeCover, css.SizeContain} {
		if got := gradientTiles(bx, css.BgSize{Kind: k}, origin, css.RepeatBoth); len(got) != 1 || got[0] != bx {
			t.Errorf("kind %v: %v", k, got)
		}
	}
	// 100% x 40px, no-repeat: a strip.
	got := gradientTiles(bx, css.BgSize{Kind: css.SizeExplicit, W: pct(1), H: px(40)}, origin, css.NoRepeat)
	if len(got) != 1 || got[0] != image.Rect(10, 20, 110, 60) {
		t.Errorf("strip: %v", got)
	}
	// 30x20 repeated both ways in 100x50: ceil(100/30)=4 x ceil(50/20)=3 tiles.
	got = gradientTiles(bx, css.BgSize{Kind: css.SizeExplicit, W: px(30), H: px(20)}, origin, css.RepeatBoth)
	if len(got) != 12 {
		t.Errorf("tiles = %d, want 12", len(got))
	}
	// auto height = positioning area, positioned at the bottom right.
	got = gradientTiles(bx, css.BgSize{Kind: css.SizeExplicit, W: pct(0.5), H: auto}, css.BgPosition{X: pct(1), Y: pct(1)}, css.NoRepeat)
	if len(got) != 1 || got[0] != image.Rect(60, 20, 110, 70) {
		t.Errorf("auto height: %v", got)
	}
	// A zero size paints nothing; a 1px size is capped, not unbounded.
	if got := gradientTiles(bx, css.BgSize{Kind: css.SizeExplicit, W: px(0), H: px(5)}, origin, css.RepeatBoth); got != nil {
		t.Errorf("zero width: %v", got)
	}
	huge := image.Rect(0, 0, 100000, 100000)
	if got := gradientTiles(huge, css.BgSize{Kind: css.SizeExplicit, W: px(1), H: px(1)}, origin, css.RepeatBoth); len(got) > maxGradientTiles {
		t.Errorf("tile cap exceeded: %d", len(got))
	}
}
