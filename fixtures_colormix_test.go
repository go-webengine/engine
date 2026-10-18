// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import "testing"

// TestColorMixPaints pins color-mix() (CSS Color 5 §3) end to end, in the
// places Marp's gaia theme uses it: inside a linear-gradient() colour stop
// (`linear-gradient(color-mix(in srgb, var(--color-background) 98%, #888 2%), …)`)
// and as a plain background-color. Before color-mix() was understood the
// gradient's stop failed to parse and the whole background was dropped
// (white).
func TestColorMixPaints(t *testing.T) {
	page := func(style string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}</style></head><body>` +
			`<div style="width:200px;height:200px;` + style + `"></div></body></html>`
	}
	const half = "color-mix(in srgb, #ff0000 50%, #0000ff 50%)"
	cases := []struct {
		name, style string
		r, g, b     uint8
	}{
		// sRGB midpoint of red and blue: rgb(127.5 0 127.5) → #800080.
		{"background-color", "background-color:" + half, 0x80, 0, 0x80},
		{"background shorthand", "background:" + half, 0x80, 0, 0x80},
		{"gradient stops", "background-image:linear-gradient(" + half + "," + half + ")", 0x80, 0, 0x80},
		{"gradient stops with positions", "background-image:linear-gradient(" + half + " 0%," + half + " 100%)", 0x80, 0, 0x80},
		// gaia's shape, through a custom property: 98% #fff8e1 + 2% #888.
		// sRGB: r = .98*255 + .02*136 = 252.62, g = .98*248+.02*136 = 245.76,
		// b = .98*225 + .02*136 = 223.22 → #fdf6df.
		{"gaia gradient via var", "--bg:#fff8e1;background-image:linear-gradient(color-mix(in srgb, var(--bg) 98%, #888 2%), color-mix(in srgb, var(--bg) 98%, #888 2%))", 0xfd, 0xf6, 0xdf},
		// Omitted percentages split the remainder (CSS Values 5 §6.1).
		{"one percentage omitted", "background-color:color-mix(in srgb, #ff0000 25%, #0000ff)", 0x40, 0, 0xbf},
		{"both omitted", "background-color:color-mix(in srgb, #ff0000, #0000ff)", 0x80, 0, 0x80},
		// srgb-linear: (0.5 linear) → sRGB 188.
		{"srgb-linear", "background-color:color-mix(in srgb-linear, #ff0000, #0000ff)", 0xbc, 0, 0xbc},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, page(tc.style), 200, 200)
			assertPixel(t, img, 100, 100, tc.r, tc.g, tc.b, tc.name)
		})
	}
}
