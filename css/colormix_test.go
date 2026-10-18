// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

func near(a, b uint8) bool {
	d := int(a) - int(b)
	return d >= -1 && d <= 1
}

// TestParseColorMix covers color-mix() (CSS Color 5 §3) per interpolation
// space, the percentage normalisation of CSS Values 5 §6.1 (omitted
// percentages, a sum above 100%, a leftover below 100% becoming alpha),
// premultiplied alpha, hue interpolation methods and powerless hues.
func TestParseColorMix(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Color
	}{
		{"color-mix(in srgb, #ff0000 50%, #0000ff 50%)", Color{128, 0, 128, 255}},
		{"color-mix(in srgb, red, blue)", Color{128, 0, 128, 255}},
		{"color-mix(in srgb, 25% red, blue)", Color{64, 0, 191, 255}},
		{"color-mix(in srgb, red 100%, blue 100%)", Color{128, 0, 128, 255}}, // scaled to 50/50
		{"color-mix(in srgb, red 25%, blue 25%)", Color{128, 0, 128, 128}},   // leftover 50% → alpha ×0.5
		{"color-mix(in srgb, red 0%, blue 0%)", Color{128, 0, 128, 0}},       // leftover 100%, progress 0.5
		{"color-mix(in srgb, red 50%, transparent)", Color{255, 0, 0, 128}},  // premultiplied: stays red
		{"color-mix(in srgb, red)", Color{255, 0, 0, 255}},                   // single item
		{"color-mix(in srgb, red, lime, blue)", Color{85, 85, 85, 255}},      // three items, 1/3 each
		{"color-mix(in srgb, color-mix(in srgb, red, blue) 50%, white)", Color{192, 128, 192, 255}},
		{"color-mix(in srgb-linear, red, blue)", Color{188, 0, 188, 255}},
		{"color-mix(in oklab, white, black)", Color{99, 99, 99, 255}},
		{"color-mix(white, black)", Color{99, 99, 99, 255}}, // Oklab when no method (§3.1)
		{"color-mix(in oklch, white, black)", Color{99, 99, 99, 255}},
		{"color-mix(in oklch, black, transparent)", Color{0, 0, 0, 128}},
		{"color-mix(in hsl, hsl(10 100% 50%), hsl(350 100% 50%))", Color{255, 0, 0, 255}},
		{"color-mix(in hsl shorter hue, hsl(10 100% 50%), hsl(350 100% 50%))", Color{255, 0, 0, 255}},
		{"color-mix(in hsl longer hue, hsl(10 100% 50%), hsl(350 100% 50%))", Color{0, 255, 255, 255}},
		{"color-mix(in hsl longer hue, hsl(350 100% 50%), hsl(10 100% 50%))", Color{0, 255, 255, 255}},
		{"color-mix(in hsl longer hue, hsl(10 100% 50%), hsl(50 100% 50%))", Color{0, 128, 255, 255}},
		{"color-mix(in hsl longer hue, hsl(50 100% 50%), hsl(10 100% 50%))", Color{0, 128, 255, 255}},
		{"color-mix(in hsl increasing hue, hsl(10 100% 50%), hsl(350 100% 50%))", Color{0, 255, 255, 255}},
		{"color-mix(in hsl increasing hue, hsl(350 100% 50%), hsl(10 100% 50%))", Color{255, 0, 0, 255}},
		{"color-mix(in hsl decreasing hue, hsl(10 100% 50%), hsl(350 100% 50%))", Color{255, 0, 0, 255}},
		{"color-mix(in hsl decreasing hue, hsl(350 100% 50%), hsl(10 100% 50%))", Color{0, 255, 255, 255}},
		{"color-mix(in hsl shorter hue, hsl(350 100% 50%), hsl(10 100% 50%))", Color{255, 0, 0, 255}},
		// A grey's hue is powerless: the other colour's hue is used.
		{"color-mix(in hsl, #808080, hsl(120 100% 50%))", Color{64, 191, 64, 255}},
		{"color-mix(in hsl, hsl(120 100% 50%), #808080)", Color{64, 191, 64, 255}},
	} {
		got, ok := parseColor(tc.in)
		if !ok || !near(got.R, tc.want.R) || !near(got.G, tc.want.G) || !near(got.B, tc.want.B) || !near(got.A, tc.want.A) {
			t.Errorf("parseColor(%q) = %v,%v want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, bad := range []string{
		"color-mix(in lab, red, blue)",                // space not supported here
		"color-mix(in srgb, red 150%, blue)",          // percentage outside 0..100
		"color-mix(in srgb, red -5%, blue)",           // negative
		"color-mix(in srgb, red abc%, blue)",          // not a number
		"color-mix(in srgb)",                          // no colour
		"color-mix(in srgb, red blue)",                // two colours in one item
		"color-mix(in srgb, 50%)",                     // percentage without a colour
		"color-mix(in srgb, red 10% 20%)",             // two percentages
		"color-mix(in srgb, red 1% 2% 3%)",            // too many tokens
		"color-mix(in srgb, , red)",                   // empty item
		"color-mix(in srgb, foo, red)",                // not a colour
		"color-mix(in srgb, red, blue",                // unterminated
		"color-mix(in srgb, red, blue) x",             // trailing junk
		"color-mix(in srgb shorter hue, red, blue)",   // hue method on a rectangular space
		"color-mix(in oklch sideways hue, red, blue)", // unknown hue method
		"color-mix(in oklch shorter, red, blue)",      // missing "hue"
		"color-mix(in oklch shorter hues, red, blue)", // not "hue"
		"color-mix(in, red, blue)",                    // "in" without a space
	} {
		if c, ok := parseColor(bad); ok {
			t.Errorf("parseColor(%q) = %v, want invalid", bad, c)
		}
	}
	if _, ok := parseColorMix("rgb(1,2,3)"); ok {
		t.Error("parseColorMix accepted a non-color-mix value")
	}
	if _, _, ok := parseMixMethod("srgb"); ok {
		t.Error(`parseMixMethod("srgb") accepted a method without "in"`)
	}
}

// TestColorMixInGradientAndShorthand: a colour function with its own
// spaces and commas survives the gradient stop split and the background
// shorthand's colour-token scan.
func TestColorMixInGradientAndShorthand(t *testing.T) {
	var s Style
	s.apply(Declaration{Property: "background", Value: "color-mix(in srgb, rgb(255 0 0), blue) no-repeat"}, 16, nil)
	if s.Background != (Color{128, 0, 128, 255}) {
		t.Errorf("background shorthand colour = %v", s.Background)
	}
	s = Style{}
	s.apply(Declaration{Property: "background-image", Value: "linear-gradient(rgb(255 0 0) 0%, color-mix(in srgb, red, blue) 100%)"}, 16, nil)
	if len(s.BackgroundImages) != 1 || s.BackgroundImages[0].Grad == nil || len(s.BackgroundImages[0].Grad.Stops) != 2 {
		t.Fatalf("gradient = %+v", s.BackgroundImages)
	}
	if got := s.BackgroundImages[0].Grad.Stops[1].Color; got != (Color{128, 0, 128, 255}) {
		t.Errorf("second stop = %v", got)
	}
}
