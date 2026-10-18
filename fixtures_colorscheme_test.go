// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import "testing"

// colorSchemePage wraps one 200x200 box in a margin-less page.
func colorSchemePage(boxStyle string) string {
	return `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
		`<div style="width:200px;height:200px;` + boxStyle + `"></div></body></html>`
}

// TestLightDarkFollowsColorScheme pins light-dark() to the element's used
// colour scheme (CSS Color 5 §7; CSS Color Adjust 1 §2.3): `color-scheme:
// light` picks the first argument, `dark` the second, an inherited value
// applies to descendants, and a custom property holding light-dark() resolves
// at the element that USES it. Marp's gaia theme is exactly this shape:
// `section { color-scheme: light }` and `section.invert { color-scheme: dark }`
// with every colour behind a light-dark() custom property.
func TestLightDarkFollowsColorScheme(t *testing.T) {
	const green, blue = "#00ff00", "#0000ff"
	cases := []struct {
		name, style string
		r, g, b     uint8
	}{
		{"light", "color-scheme:light;background:light-dark(" + green + "," + blue + ")", 0, 0xff, 0},
		{"dark", "color-scheme:dark;background:light-dark(" + green + "," + blue + ")", 0, 0, 0xff},
		{"only light", "color-scheme:only light;background:light-dark(" + green + "," + blue + ")", 0, 0xff, 0},
		// No color-scheme at all: the UA default scheme, light (Gecko's
		// LookAndFeel::ColorSchemeForStyle "default content to light").
		{"normal", "background:light-dark(" + green + "," + blue + ")", 0, 0xff, 0},
		{"via var light", "color-scheme:light;--c:light-dark(" + green + "," + blue + ");background-color:var(--c)", 0, 0xff, 0},
		{"via var dark", "color-scheme:dark;--c:light-dark(" + green + "," + blue + ");background-color:var(--c)", 0, 0, 0xff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, colorSchemePage(tc.style), 200, 200)
			assertPixel(t, img, 100, 100, tc.r, tc.g, tc.b, tc.name)
		})
	}

	// Inheritance + use-site resolution: the custom property is declared on
	// the light parent, the dark child uses it and must get the dark branch.
	page := `<!DOCTYPE html><html><head><style>html,body{margin:0}` +
		`section{color-scheme:light;--bg:light-dark(#00ff00,#0000ff)}` +
		`section.invert{color-scheme:dark}` +
		`div{width:100px;height:100px;background:var(--bg)}</style></head><body>` +
		`<section><div></div></section><section class="invert"><div></div></section></body></html>`
	img := renderString(t, page, 200, 200)
	assertPixel(t, img, 50, 50, 0, 0xff, 0, "light section")
	assertPixel(t, img, 50, 150, 0, 0, 0xff, "inverted (dark) section")
}
