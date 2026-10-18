// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import "testing"

// TestBackgroundShorthandResetsOmittedLonghands pins CSS Backgrounds 3
// §2.10 and CSS Cascade 4 §3: the `background` shorthand sets EVERY
// sub-property — the omitted ones to their initial values (no image,
// transparent colour, repeat, 0% 0%, auto) — and `!important` on it applies
// to all of them. Marp's layered backgrounds rely on it:
// `background: transparent !important` must remove the theme's
// background-image gradient, not just its colour.
func TestBackgroundShorthandResetsOmittedLonghands(t *testing.T) {
	page := func(rule string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}body{background:#00ff00}` +
			`section{display:block;width:200px;height:200px;background-color:#ff0000;` +
			`background-image:linear-gradient(#ff0000,#ff0000)}` + rule +
			`</style></head><body><section class="t"></section></body></html>`
	}
	for _, tc := range []struct {
		name, rule string
		r, g, b    uint8
	}{
		{"transparent", `section.t{background:transparent}`, 0, 0xff, 0},
		{"none", `section.t{background:none}`, 0, 0xff, 0},
		{"transparent !important", `section.t{background:transparent!important}section{background-image:linear-gradient(#ff0000,#ff0000)!important}`, 0, 0xff, 0},
		{"important beats a later normal longhand", `section.t{background:transparent !important}section.t{background-image:linear-gradient(#ff0000,#ff0000)}`, 0, 0xff, 0},
		{"colour only", `section.t{background:#0000ff}`, 0, 0, 0xff},
		{"initial", `section.t{background:initial}`, 0, 0xff, 0},
		// A longhand AFTER the shorthand still wins (cascade order per longhand).
		{"longhand after shorthand", `section.t{background:transparent;background-image:linear-gradient(#0000ff,#0000ff)}`, 0, 0, 0xff},
	} {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, page(tc.rule), 200, 200)
			assertPixel(t, img, 100, 100, tc.r, tc.g, tc.b, tc.name)
		})
	}

	// background-size / -position / -repeat omitted from a later shorthand
	// are reset too: a 10x10 tile set by longhands, then `background:
	// linear-gradient(...)` must cover the whole box again (size auto, repeat).
	img := renderString(t, `<!DOCTYPE html><html><head><style>html,body{margin:0}`+
		`section{display:block;width:200px;height:200px;background-size:10px 10px;background-repeat:no-repeat;background-position:50px 50px}`+
		`section.t{background:linear-gradient(#0000ff,#0000ff)}</style></head><body><section class="t"></section></body></html>`, 200, 200)
	assertPixel(t, img, 5, 5, 0, 0, 0xff, "size/position/repeat reset by the shorthand")
	assertPixel(t, img, 190, 190, 0, 0, 0xff, "size/position/repeat reset by the shorthand (far corner)")
}

// TestOtherShorthandsReset checks the same property of the other shorthands
// the engine supports.
func TestOtherShorthandsReset(t *testing.T) {
	page := func(rule, inner string) string {
		return `<!DOCTYPE html><html><head><style>html,body{margin:0}` + rule +
			`</style></head><body>` + inner + `</body></html>`
	}
	// border: none after a 20px red border → no border at all.
	img := renderString(t, page(`div{width:100px;height:100px;border:20px solid #ff0000}div.t{border:none}`, `<div class="t"></div>`), 200, 200)
	assertPixel(t, img, 5, 5, 0xff, 0xff, 0xff, "border:none resets width/colour")
	// border: 0 likewise.
	img = renderString(t, page(`div{width:100px;height:100px;border:20px solid #ff0000}div.t{border:0}`, `<div class="t"></div>`), 200, 200)
	assertPixel(t, img, 5, 5, 0xff, 0xff, 0xff, "border:0 resets")
	// border-top shorthand resets that side only.
	img = renderString(t, page(`div{width:100px;height:100px;border:20px solid #ff0000}div.t{border-top:none}`, `<div class="t"></div>`), 200, 200)
	assertPixel(t, img, 5, 60, 0xff, 0, 0, "border-top:none leaves the left side")
	assertPixel(t, img, 60, 2, 0xff, 0xff, 0xff, "border-top:none removes the top")
	// margin: 0 after margin-left.
	img = renderString(t, page(`div{margin-left:50px;width:20px;height:20px;background:#ff0000}div.t{margin:0}`, `<div class="t"></div>`), 200, 200)
	assertPixel(t, img, 5, 5, 0xff, 0, 0, "margin:0 resets margin-left")
	// flex: none after flex-grow:1.
	img = renderString(t, page(`.f{display:flex;width:200px}.i{flex-grow:1;width:20px;height:20px;background:#ff0000}.i.t{flex:none}`, `<div class="f"><div class="i t"></div></div>`), 200, 200)
	assertPixel(t, img, 100, 5, 0xff, 0xff, 0xff, "flex:none resets flex-grow")
}
