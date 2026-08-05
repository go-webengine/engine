// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

func TestStripComments(t *testing.T) {
	if got := stripComments("a/*x*/b/* y */c"); got != "abc" {
		t.Errorf("stripComments = %q", got)
	}
	if got := stripComments("a/*unterminated"); got != "a" {
		t.Errorf("unterminated = %q", got)
	}
	if got := stripComments("plain"); got != "plain" {
		t.Errorf("plain = %q", got)
	}
}

func TestParseDeclarations(t *testing.T) {
	d := ParseDeclarations("color: red; font-size:16px ; bad ; :novalue; prop: ; x:1 !important")
	// Expect color, font-size, x (bad, empty-prop, empty-value skipped).
	want := map[string]string{"color": "red", "font-size": "16px", "x": "1"}
	if len(d) != len(want) {
		t.Fatalf("got %d decls: %v", len(d), d)
	}
	for _, decl := range d {
		if want[decl.Property] != decl.Value {
			t.Errorf("decl %q = %q want %q", decl.Property, decl.Value, want[decl.Property])
		}
	}
}

func TestParseStylesheet(t *testing.T) {
	css := `
	/* a comment */
	h1, .big { font-size: 30px; color: blue }
	@font-face { src: url(x) }
	broken {
	`
	rules := ParseStylesheet(css)
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule (at-rules + unterminated skipped), got %d: %+v", len(rules), rules)
	}
	r := rules[0]
	if len(r.Selectors) != 2 {
		t.Errorf("selectors = %v", r.Selectors)
	}
	if len(r.Declarations) != 2 {
		t.Errorf("declarations = %v", r.Declarations)
	}
}

func TestParseStylesheetMediaQueries(t *testing.T) {
	css := `
	@media (min-width: 640px) { .infobox { float: right; width: 22em } }
	@media (max-width: 639px) { .infobox { float: none } }
	@media print { p { color: red } }
	@media screen { a { color: blue } }
	@supports (display:grid) { div { display: grid } }
	`
	// At vw=1024: min-width:640 matches, max-width:639 does not, print never,
	// screen matches, @supports skipped.
	rules := ParseStylesheetVW(css, 1024)
	got := map[string]string{}
	for _, r := range rules {
		for _, d := range r.Declarations {
			got[d.Property] = d.Value
		}
	}
	if got["float"] != "right" {
		t.Errorf("min-width infobox float should apply, rules=%+v", rules)
	}
	if got["color"] != "blue" {
		t.Error("screen media rule should apply")
	}
	if _, ok := got["display"]; ok {
		t.Error("@supports should be skipped")
	}
	// The max-width:639 (mobile) and print rules must be excluded: exactly the
	// two matching blocks (infobox + a) remain.
	if len(rules) != 2 {
		t.Errorf("expected 2 matching media rules, got %d: %+v", len(rules), rules)
	}

	// At a narrow viewport the mobile rule wins instead.
	mobile := ParseStylesheetVW(css, 480)
	var floatVal string
	for _, r := range mobile {
		for _, d := range r.Declarations {
			if d.Property == "float" {
				floatVal = d.Value
			}
		}
	}
	if floatVal != "none" {
		t.Errorf("at 480px the mobile float:none should apply, got %q", floatVal)
	}
}

func TestMediaMatches(t *testing.T) {
	if mediaMatches("print", 1024) {
		t.Error("print should not match")
	}
	if !mediaMatches("screen and (min-width: 640px)", 1024) {
		t.Error("min-width 640 should match at 1024")
	}
	if mediaMatches("(min-width: 1200px)", 1024) {
		t.Error("min-width 1200 should not match at 1024")
	}
	if !mediaMatches("(max-width: 1200px)", 1024) {
		t.Error("max-width 1200 should match at 1024")
	}
	if mediaMatches("(max-width: 800px)", 1024) {
		t.Error("max-width 800 should not match at 1024")
	}
	if !mediaMatches("all", 1024) {
		t.Error("all should match")
	}
	if !mediaMatches("(min-width: abc)", 1024) {
		t.Error("unparseable width feature is ignored (matches)")
	}
}

func TestParseStylesheetEmptyAndNoBrace(t *testing.T) {
	if r := ParseStylesheet(""); r != nil {
		t.Errorf("empty = %v", r)
	}
	if r := ParseStylesheet("p color red"); r != nil {
		t.Errorf("no-brace = %v", r)
	}
	// A rule whose block has no valid declarations is dropped.
	if r := ParseStylesheet("p { }"); r != nil {
		t.Errorf("empty block = %v", r)
	}
}

func TestApplyProperties(t *testing.T) {
	s := initialStyle()
	apply := func(p, v string, em float64) { s.apply(Declaration{p, v}, em) }
	apply("display", "none", 16)
	if s.Display != DisplayNone {
		t.Error("display none")
	}
	apply("display", "flex", 16)
	if s.Display != DisplayFlex {
		t.Error("display flex")
	}
	apply("display", "inline-block", 16)
	if s.Display != DisplayInlineBlock {
		t.Error("display inline-block")
	}
	apply("display", "table", 16)
	if s.Display != DisplayTable {
		t.Error("display table")
	}
	apply("display", "block", 16)
	apply("background", "  #fff other", 16)
	if s.Background != (Color{255, 255, 255, 255}) {
		t.Errorf("background = %v", s.Background)
	}
	apply("font-size", "150%", 16)
	if s.FontSize != 24 {
		t.Errorf("font-size %% = %v", s.FontSize)
	}
	apply("font-weight", "bold", 16)
	if s.FontWeight != 700 {
		t.Error("weight bold")
	}
	apply("font-weight", "lighter", 16)
	if s.FontWeight != 400 {
		t.Error("weight lighter")
	}
	apply("font-weight", "600", 16)
	if s.FontWeight != 600 {
		t.Error("weight 600")
	}
	apply("font-style", "italic", 16)
	if !s.Italic {
		t.Error("font-style italic")
	}
	apply("font-style", "oblique", 16)
	if !s.Italic {
		t.Error("font-style oblique")
	}
	apply("font-style", "normal", 16)
	if s.Italic {
		t.Error("font-style normal resets italic")
	}
	apply("text-align", "center", 16)
	if s.TextAlign != AlignCenter {
		t.Error("align center")
	}
	apply("text-align", "right", 16)
	if s.TextAlign != AlignRight {
		t.Error("align right")
	}
	apply("white-space", "pre", 16)
	if s.WhiteSpace != WSPre {
		t.Error("white-space pre")
	}
	apply("white-space", "normal", 16)
	if s.WhiteSpace != WSNormal {
		t.Error("white-space normal")
	}
	apply("width", "50%", 16)
	if !s.Width.IsPercent || s.Width.Percent != 0.5 {
		t.Errorf("width = %v", s.Width)
	}
	apply("margin", "3px", 16)
	if s.Margin != (Edges{3, 3, 3, 3}) {
		t.Errorf("margin = %v", s.Margin)
	}
	apply("margin-top", "7px", 16)
	apply("margin-right", "8px", 16)
	apply("margin-bottom", "9px", 16)
	apply("margin-left", "10px", 16)
	if s.Margin != (Edges{7, 8, 9, 10}) {
		t.Errorf("margin longhand = %v", s.Margin)
	}
	apply("padding", "2px", 16)
	apply("padding-top", "1px", 16)
	apply("padding-right", "2px", 16)
	apply("padding-bottom", "3px", 16)
	apply("padding-left", "4px", 16)
	if s.Padding != (Edges{1, 2, 3, 4}) {
		t.Errorf("padding longhand = %v", s.Padding)
	}
	apply("color", "notacolor", 16) // invalid ignored
	apply("unknown-prop", "x", 16)  // unknown ignored
}
