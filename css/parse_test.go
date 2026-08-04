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
	@media screen { p { color: red } }
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
	if s.Display != DisplayBlock {
		t.Error("flex→block")
	}
	apply("display", "inline-block", 16)
	if s.Display != DisplayInline {
		t.Error("inline-block→inline")
	}
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
