// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"fmt"
	"testing"
)

// TestCalcPercentMargin pins calc() over percentages on a margin (CSS Values 4
// §10.1: a sum of percentages is a percentage, resolved against the containing
// block's width, CSS Box 3 §5): a 100px box in a 500px container offset by 60%
// stands at columns 300..399. Marpit's `![bg right:40%]` is exactly this:
// margin-left: calc(100% - var(--marpit-advanced-background-split, 50%)) on the
// background layer, with the split a custom property of the section. The
// engine dropped the declaration, so the image of a right split was drawn on
// the left in PNG, PDF and PPTX.
func TestCalcPercentMargin(t *testing.T) {
	const page = `<!DOCTYPE html><html><head><style>html,body{margin:0}%s</style></head><body>` +
		`<section %s style="%s;display:block;width:500px;height:100px;position:relative"><div class="c"><i></i></div></section></body></html>`
	const box = `i{display:block;height:100px;background:#f00}`
	cases := []struct{ name, css, attr, style string }{
		{"percent", `.c{width:100px;height:100px;margin-left:60%}` + box, "", ""},
		{"calc of percentages", `.c{width:100px;height:100px;margin-left:calc(100% - 40%)}` + box, "", ""},
		{"calc of percentages, scaled", `.c{width:100px;height:100px;margin-left:calc(30% * 2)}` + box, "", ""},
		{"calc with var", `.c{width:100px;height:100px;margin-left:calc(100% - var(--s, 50%))}` + box, "", "--s:40%"},
		{"calc with var's fallback", `.c{width:100px;height:100px;margin-left:calc(100% - var(--s, 40%))}` + box, "", ""},
		{"flex container", `.c{display:flex;flex-direction:row;width:100px;height:100px;margin-left:calc(100% - var(--s, 50%))}i{flex:auto;height:100px;background:#f00}`, "", "--s:40%"},
		{"after all:initial, overflow hidden", `.c{all:initial;display:flex;width:100px;height:100px;overflow:hidden}.c{margin-left:calc(100% - var(--s, 50%))}i{flex:auto;display:block;height:100px;background:#f00}`, "", "--s:40%"},
		{"under an attribute selector", `section[data-x]>.c{width:100px;height:100px}section[data-x="right"]>.c{margin-left:calc(100% - var(--s, 50%))}` + box, `data-x="right"`, "--s:40%"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := renderString(t, fmt.Sprintf(page, tc.css, tc.attr, tc.style), 500, 100)
			x0, x1 := inkCols(img, 50)
			if x0 != 300 || x1 != 399 {
				t.Errorf("ink columns %d..%d, want 300..399", x0, x1)
			}
		})
	}
	// a calc() that mixes a percentage with a length is still not understood: the
	// declaration is dropped, as before
	html := fmt.Sprintf(page, `.c{width:100px;height:100px;margin-left:calc(100% - 10px)}`+box, "", "")
	if x0, _ := inkCols(renderString(t, html, 500, 100), 50); x0 != 0 {
		t.Errorf("a percentage mixed with a length is not resolved yet: ink starts at %d", x0)
	}
}
