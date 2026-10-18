// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"strings"
	"testing"
)

// TestWhiteSpaceWrappingValues pins the white-space values that preserve
// some white space AND wrap (CSS Text 3 §3 table, §4.1):
//
//	pre-wrap:     newlines and spaces preserved, wraps, trailing spaces hang
//	break-spaces: newlines and spaces preserved, wraps, spaces take room
//	pre-line:     newlines preserved, spaces collapse, wraps
//
// They were all parsed as `pre` (or not at all), so such a block never
// wrapped. Marp themes set `white-space: pre-wrap` on code blocks.
func TestWhiteSpaceWrappingValues(t *testing.T) {
	measure := func(body string) string {
		t.Helper()
		page := `<!DOCTYPE html><html><head><style>body{margin:0;font-size:16px;line-height:20px;font-family:monospace}` +
			`div{width:100px}span{display:inline-block}</style></head><body>` + body +
			`<script>document.title=[].map.call(document.querySelectorAll('[id]'),function(e){return e.id+'='+e.offsetWidth+'x'+e.offsetHeight}).join(' ')</script></body></html>`
		_, info, err := New().RenderHTML(context.Background(), page, "https://demo.test/", image.Rect(0, 0, 400, 400))
		if err != nil {
			t.Fatal(err)
		}
		return info.Title
	}
	dims := func(s string) map[string][2]int {
		out := map[string][2]int{}
		for _, f := range strings.Fields(s) {
			k, v, _ := strings.Cut(f, "=")
			var w, h int
			for i, p := range strings.Split(v, "x") {
				n := 0
				for _, c := range p {
					n = n*10 + int(c-'0')
				}
				if i == 0 {
					w = n
				} else {
					h = n
				}
			}
			out[k] = [2]int{w, h}
		}
		return out
	}
	const words = "aaa bbb ccc ddd eee fff ggg"
	got := dims(measure(
		`<div id="pre" style="white-space:pre">` + words + `</div>` +
			`<div id="prewrap" style="white-space:pre-wrap">` + words + `</div>` +
			`<div id="breakspaces" style="white-space:break-spaces">` + words + `</div>` +
			`<div id="preline" style="white-space:pre-line">` + words + `</div>` +
			`<span id="pw1" style="white-space:pre-wrap">a b</span>` +
			`<span id="pw4" style="white-space:pre-wrap">a    b</span>` +
			`<span id="pl4" style="white-space:pre-line">a    b</span>` +
			`<span id="pwi" style="white-space:pre-wrap">    b</span>` +
			`<span id="pwb" style="white-space:pre-wrap">b</span>` +
			`<div id="pwnl" style="white-space:pre-wrap">a` + "\n" + `b</div>` +
			`<div id="plnl" style="white-space:pre-line">a   ` + "\n" + `   b</div>` +
			`<div id="bsnl" style="white-space:break-spaces">a` + "\n" + `b</div>`))
	t.Logf("%v", got)
	if got["pre"][1] != 20 {
		t.Errorf("pre: height %d, want one 20px line (control)", got["pre"][1])
	}
	for _, id := range []string{"prewrap", "breakspaces", "preline"} {
		if got[id][1] <= 20 {
			t.Errorf("%s: height %d, want the 27-char line to wrap in 100px", id, got[id][1])
		}
	}
	if got["pw4"][0] <= got["pw1"][0] {
		t.Errorf("pre-wrap must preserve spaces: 'a    b' %d wide vs 'a b' %d", got["pw4"][0], got["pw1"][0])
	}
	if got["pl4"][0] != got["pw1"][0] {
		t.Errorf("pre-line must collapse spaces: 'a    b' %d wide, want %d ('a b')", got["pl4"][0], got["pw1"][0])
	}
	if got["pwi"][0] <= got["pwb"][0] {
		t.Errorf("pre-wrap must keep leading indentation: '    b' %d wide vs 'b' %d", got["pwi"][0], got["pwb"][0])
	}
	for _, id := range []string{"pwnl", "plnl", "bsnl"} {
		if got[id][1] != 40 {
			t.Errorf("%s: height %d, want 40 (the newline is preserved)", id, got[id][1])
		}
	}
}
