// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"math"
	"testing"

	"github.com/go-webengine/engine/dom"
)

// CSS Values 4 §6.1.1: ch is the used advance measure of the "0" glyph in the
// element's font; where that cannot be determined it is 0.5em wide.

// zeroIs7 is a font whose "0" is 7px wide at 10px (0.7em) and whose other
// glyphs are wider, so a measurement of anything but "0" shows.
func zeroIs7(text string, _ FontFamily, size float64, _ int, _ bool) float64 {
	if text == "0" {
		return 0.7 * size
	}
	return 99
}

func cascadeWith(t *testing.T, src string, adv Advance) (StyleMap, *dom.Node) {
	t.Helper()
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	return CascadeMediaContainers(root, Media{Width: 800, Advance: adv}, nil, nil), root
}

func TestChIsTheAdvanceOfZeroInTheElementsFont(t *testing.T) {
	sm, root := cascadeWith(t,
		`<html><body><div style="font-size:10px;width:3ch;padding-left:2ch;margin-left:1ch">x</div></body></html>`, zeroIs7)
	div := dom.Find(root, "div")
	st := sm[div]
	if st == nil {
		t.Fatal("no style")
	}
	if got, want := st.Width.Px, 3*7.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("width:3ch = %v, want %v", got, want)
	}
	if got, want := st.Padding.Left, 2*7.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("padding-left:2ch = %v, want %v", got, want)
	}
	if got, want := st.Margin.Left, 7.0; math.Abs(got-want) > 1e-9 {
		t.Errorf("margin-left:1ch = %v, want %v", got, want)
	}
}

// ch is measured in the element's OWN font, even when the font-size is
// declared after the width and the family is inherited.
func TestChUsesTheElementsOwnFontSize(t *testing.T) {
	sm, root := cascadeWith(t,
		`<html><body style="font-size:20px"><div style="width:2ch;font-size:10px">x</div><p style="width:2ch">y</p></body></html>`, zeroIs7)
	if got := sm[dom.Find(root, "div")].Width.Px; math.Abs(got-14) > 1e-9 {
		t.Errorf("div 2ch at 10px = %v, want 14", got)
	}
	if got := sm[dom.Find(root, "p")].Width.Px; math.Abs(got-28) > 1e-9 {
		t.Errorf("p 2ch at 20px = %v, want 28", got)
	}
}

// With no font to ask (or a font that measures nothing) ch is 0.5em, the
// fallback the spec prescribes.
func TestChFallsBackToHalfAnEm(t *testing.T) {
	for name, adv := range map[string]Advance{
		"no measurer": nil,
		"zero advance": func(string, FontFamily, float64, int, bool) float64 {
			return 0
		},
	} {
		sm, root := cascadeWith(t, `<html><body><div style="font-size:10px;width:4ch">x</div></body></html>`, adv)
		if got := sm[dom.Find(root, "div")].Width.Px; math.Abs(got-20) > 1e-9 {
			t.Errorf("%s: 4ch at 10px = %v, want 20", name, got)
		}
	}
}

func TestResolveChLeavesTextAndIdentifiersAlone(t *testing.T) {
	st := &Style{FontSize: 10}
	for in, want := range map[string]string{
		"3ch":                      "15px",
		"3CH":                      "15px",
		"-1.5ch":                   "-7.5px",
		"calc(100% - 2ch)":         "calc(100% - 10px)",
		"1ch 2ch":                  "5px 10px",
		`"3ch"`:                    `"3ch"`,
		"url(a3ch.png)":            "url(a3ch.png)",
		"touch 5px":                "touch 5px",
		"#3ch":                     "#3ch",
		"3chx":                     "3chx",
		"none":                     "none",
		`"a\"3ch" 1ch`:             `"a\"3ch" 5px`, // an escaped quote does not end the string
		`"open 3ch\`:               `"open 3ch\`,   // unterminated, ends in a backslash
		"url(3ch":                  "url(3ch",      // unterminated url()
		"var(--a) 1ch":             "var(--a) 5px",
		"2px":                      "2px",
		"counter(chapter) \"3ch\"": "counter(chapter) \"3ch\"",
	} {
		if got := resolveCh(in, nil, st); got != want {
			t.Errorf("resolveCh(%q) = %q, want %q", in, got, want)
		}
	}
}

// At a device scale the font-size is already in device pixels, so ch comes
// out in device pixels with the rest of the cascade.
func TestChAtDeviceScale(t *testing.T) {
	root, err := dom.Parse(`<html><body><div style="font-size:10px;width:2ch">x</div></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	sm := CascadeMediaContainers(root, Media{Width: 800, Scale: 2, Advance: zeroIs7}, nil, nil)
	if got := sm[dom.Find(root, "div")].Width.Px; math.Abs(got-28) > 1e-9 {
		t.Errorf("2ch at 20 device px = %v, want 28", got)
	}
}
