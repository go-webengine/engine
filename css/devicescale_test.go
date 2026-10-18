// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func TestScaleLengths(t *testing.T) {
	for _, tc := range []struct {
		prop, in, want string
	}{
		{"width", "10px", "20px"},
		{"margin", "1px -2px .5px +3PX", "2px -4px 1px 6px"},
		{"width", "calc(100% - 1in)", "calc(100% - 192px)"},
		{"font-size", "1rem", "32px"},
		{"font-size", "2em", "2em"},   // relative: follows from font-size
		{"width", "50%", "50%"},       // relative
		{"width", "10vw", "10vw"},     // relative to the (device) viewport
		{"line-height", "1.5", "1.5"}, // unitless
		{"transform", "rotate(45deg)", "rotate(45deg)"},
		{"color", "#123px", "#123px"}, // hex digits, not a length
		{"grid-area", "h1px", "h1px"}, // identifier
		{"background-image", `url(img-10px.png)`, `url(img-10px.png)`},
		{"background-image", `URL("a 1px.png") 2px`, `URL("a 1px.png") 4px`},
		{"content", `"10px" 'it\'s 1px'`, `"10px" 'it\'s 1px'`},
		{"content", `"unterminated 1px`, `"unterminated 1px`},
		{"content", `"ends in escape\`, `"ends in escape\`},
		{"background", `url(x 1px`, `url(x 1px`}, // unterminated url(
		{"border", "thin solid red", "2px solid red"},
		{"border-top-width", "medium", "6px"},
		{"outline", "thick dotted", "10px dotted"},
		{"font", "medium sans-serif", "medium sans-serif"}, // not a line width here
		{"border", "#thin", "#thin"},
		{"width", "12q", ""},            // 12Q = 3mm: only checked to be rewritten (see below)
		{"width", "1e", "1e"},           // not a unit
		{"width", "5", "5"},             // unitless number
		{"width", "1.2.3px", "1.2.3px"}, // not a number
	} {
		got := scaleLengths(tc.prop, tc.in, 2)
		if tc.in == "12q" {
			if got == tc.in {
				t.Errorf("Q unit not scaled")
			}
			continue
		}
		if got != tc.want {
			t.Errorf("scaleLengths(%q, %q, 2) = %q, want %q", tc.prop, tc.in, got, tc.want)
		}
	}
	if got := scaleLengths("width", "10px", 1); got != "10px" {
		t.Errorf("scale 1 changed the value: %q", got)
	}
}

func TestResolutionMediaFeatures(t *testing.T) {
	for _, tc := range []struct {
		q     string
		scale float64
		want  bool
	}{
		{"(resolution: 2dppx)", 2, true},
		{"(resolution: 2dppx)", 1, false},
		{"(resolution: 2x)", 2, true},
		{"(min-resolution: 192dpi)", 2, true},
		{"(min-resolution: 192dpi)", 1.5, false},
		{"(max-resolution: 1dppx)", 0, true}, // 0 = 1
		{"(max-resolution: 1dppx)", 2, false},
		{"(min-resolution: 75dpcm)", 2, true}, // 75dpcm = 1.984dppx
		{"(-webkit-min-device-pixel-ratio: 2)", 2, true},
		{"(-webkit-max-device-pixel-ratio: 1.5)", 2, false},
		{"(-webkit-device-pixel-ratio: 3)", 3, true},
		{"(resolution >= 2dppx)", 2, true},
		{"(resolution > 2dppx)", 2, false},
		{"(resolution < 2dppx)", 1, true},
		{"(resolution <= 1dppx)", 2, false},
		{"screen and (min-width: 100px) and (min-resolution: 2dppx)", 2, true},
	} {
		if got := MediaAppliesTo(tc.q, Media{Width: 200, Scale: tc.scale}); got != tc.want {
			t.Errorf("%q at %vx = %v, want %v", tc.q, tc.scale, got, tc.want)
		}
	}
}

// TestCascadeDeviceScale: a scaled cascade computes lengths in device px —
// including the initial font size and lengths reached through var() —
// while media width features stay in CSS px.
func TestCascadeDeviceScale(t *testing.T) {
	root, err := dom.Parse(`<html><head><style>:root{--w:10px}p{width:var(--w);margin:1px;font-size:1.5em;border:thin solid}` +
		`@media (min-width:150px){p{padding:2px}}</style></head><body><p>x</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	sm := CascadeMedia(root, Media{Width: 200, Scale: 2}, nil)
	p := sm[dom.Find(root, "p")]
	if p.Width.Px != 20 || p.Margin.Top != 2 || p.FontSize != 48 || p.Border.Widths().Top != 2 || p.Padding.Top != 4 {
		t.Errorf("scaled p: width %v margin %v font %v border %v padding %v, want 20 2 48 2 4",
			p.Width.Px, p.Margin.Top, p.FontSize, p.Border.Widths().Top, p.Padding.Top)
	}
}
