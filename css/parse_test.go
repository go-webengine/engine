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
		// Only x carries !important; the marker must never leak into Value, and
		// no other declaration should pick it up.
		wantImportant := decl.Property == "x"
		if decl.Important != wantImportant {
			t.Errorf("decl %q Important = %v want %v", decl.Property, decl.Important, wantImportant)
		}
	}
}

// TestParseDeclarationsImportantCaseAndSpacing covers case-insensitivity and
// whitespace around the !important marker, and that a plain declaration is
// unaffected.
func TestParseDeclarationsImportantCaseAndSpacing(t *testing.T) {
	d := ParseDeclarations("a: 1 !IMPORTANT; b: 2  !important  ; c: 3")
	want := map[string]struct {
		val       string
		important bool
	}{
		"a": {"1", true},
		"b": {"2", true},
		"c": {"3", false},
	}
	if len(d) != len(want) {
		t.Fatalf("got %d decls: %v", len(d), d)
	}
	for _, decl := range d {
		w := want[decl.Property]
		if decl.Value != w.val || decl.Important != w.important {
			t.Errorf("decl %q = %q,important=%v want %q,important=%v",
				decl.Property, decl.Value, decl.Important, w.val, w.important)
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

// declValues returns a property->value map flattening every declaration of
// every rule, for the common "did this apply" style of assertion below.
func declValues(rules []Rule) map[string]string {
	got := map[string]string{}
	for _, r := range rules {
		for _, d := range r.Declarations {
			got[d.Property] = d.Value
		}
	}
	return got
}

// TestParseStylesheetLayerBasic covers the core fix: a named @layer's rules
// must be included, not dropped — Tailwind v4's default output (and many
// other frameworks) puts nearly all of its CSS inside @layer utilities, and
// this engine silently discarding it meant almost the whole stylesheet never
// reached the cascade (observed live: tailwindcss.com had 690KB of CSS but
// only 181 rules parsed out of it before this fix — 5334 after).
func TestParseStylesheetLayerBasic(t *testing.T) {
	rules := ParseStylesheetVW(`@layer utilities { .flex { display: flex } }`, 1024)
	if got := declValues(rules); got["display"] != "flex" {
		t.Errorf("@layer utilities content should be included, got %+v", got)
	}
}

// TestParseStylesheetLayerAnonymousAndNested covers an anonymous @layer (no
// name) and @media nested inside @layer (the shape Tailwind actually emits
// for responsive/dark-mode variants defined as utilities).
func TestParseStylesheetLayerAnonymousAndNested(t *testing.T) {
	css := `
	@layer { .anon { color: red } }
	@layer utilities {
		@media (min-width: 640px) { .sm\:block { display: block } }
	}
	`
	got := declValues(ParseStylesheetVW(css, 1024))
	if got["color"] != "red" {
		t.Errorf("anonymous @layer content should be included, got %+v", got)
	}
	if got["display"] != "block" {
		t.Errorf("@media nested inside @layer should still be evaluated, got %+v", got)
	}
}

// TestParseStylesheetLayerBareDeclaration covers the bare "@layer a, b, c;"
// order-declaration form (no body of its own — just establishes priority,
// which this engine does not model). It must not itself contribute rules,
// and — critically — must not swallow whatever real construct follows it,
// since a bare declaration has no '{' and so shares its textual prelude with
// the next brace found (the exact shape Tailwind emits:
// "@layer theme, base, components, utilities;" followed immediately by
// "@layer properties{...}").
func TestParseStylesheetLayerBareDeclaration(t *testing.T) {
	css := `
	@layer theme, base, utilities;
	@layer utilities { .grid { display: grid } }
	`
	got := declValues(ParseStylesheetVW(css, 1024))
	if got["display"] != "grid" {
		t.Errorf("layer after a bare order-declaration should still parse, got %+v", got)
	}
}

// TestParseStylesheetBareDeclarationBeforeNormalRule covers the same bare-
// declaration hazard when what follows is an ordinary rule, not another
// at-rule — the case a purely prefix-based check (without splitting at the
// last ';') would get wrong: it would see a prelude like
// "@layer a, b;\n.foo" and skip it wholesale, silently dropping ".foo".
func TestParseStylesheetBareDeclarationBeforeNormalRule(t *testing.T) {
	css := `
	@layer a, b;
	.foo { color: green }
	`
	got := declValues(ParseStylesheetVW(css, 1024))
	if got["color"] != "green" {
		t.Errorf("a normal rule after a bare @layer declaration must still parse, got %+v", got)
	}
}

// TestParseStylesheetOtherAtRulesStillSkipped is the regression guard: the
// @layer fix must not accidentally start recursing into unrelated at-rules
// that were correctly skipped before.
func TestParseStylesheetOtherAtRulesStillSkipped(t *testing.T) {
	css := `
	@font-face { font-family: X; src: url(x) }
	@keyframes spin { from { transform: none } to { transform: none } }
	@supports (display: grid) { .g { display: grid } }
	`
	rules := ParseStylesheetVW(css, 1024)
	if len(rules) != 0 {
		t.Errorf("expected @font-face/@keyframes/@supports to still be skipped wholesale, got %+v", rules)
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

// TestMediaMatchesRemUnits covers Tailwind v4's default breakpoints, which are
// expressed in rem, not px ("min-width:80rem" for its "xl" variant). Before
// this, an unrecognised unit was silently ignored entirely rather than just
// failing to convert — every rem-based breakpoint matched unconditionally
// regardless of viewport width, so ALL of a page's responsive font-size/
// spacing steps applied at once and the cascade fell back to picking
// whichever was declared last (usually the largest). 1rem == 16px, matching
// parseLength's own rem conversion.
func TestMediaMatchesRemUnits(t *testing.T) {
	// 1024px viewport == 64rem: exactly the Tailwind "lg" breakpoint.
	if !mediaMatches("(min-width:64rem)", 1024) {
		t.Error("min-width:64rem (1024px) should match at exactly 1024px")
	}
	if mediaMatches("(min-width:80rem)", 1024) {
		t.Error("min-width:80rem (1280px) should NOT match at 1024px")
	}
	if !mediaMatches("(max-width:80rem)", 1024) {
		t.Error("max-width:80rem (1280px) should match at 1024px")
	}
	if mediaMatches("(max-width:40rem)", 1024) {
		t.Error("max-width:40rem (640px) should NOT match at 1024px")
	}
	// A narrower viewport sits below "lg" (64rem = 1024px) but at/above "sm"
	// (40rem = 640px).
	if mediaMatches("(min-width:64rem)", 800) {
		t.Error("min-width:64rem (1024px) should not match at 800px")
	}
	if !mediaMatches("(min-width:40rem)", 800) {
		t.Error("min-width:40rem (640px) should match at 800px")
	}
}

// TestMediaMatchesRangeComparisonSyntax covers the CSS Media Queries Level 4
// range-comparison syntax ("width<=X", "width>=X", and the value-first order
// "X<=width") GitHub's Primer design system uses for its PageLayout
// breakpoints — as invisible to a colon-only min-width:/max-width: matcher as
// the missing "rem" unit was, for the same reason (falls through to "unknown
// feature, assume it matches").
func TestMediaMatchesRangeComparisonSyntax(t *testing.T) {
	if !mediaMatches("(width<=1024px)", 1024) {
		t.Error("width<=1024px should match at exactly 1024px")
	}
	if mediaMatches("(width<=1024px)", 1025) {
		t.Error("width<=1024px should NOT match at 1025px")
	}
	if !mediaMatches("(width>=1024px)", 1024) {
		t.Error("width>=1024px should match at exactly 1024px")
	}
	if mediaMatches("(width>=1024px)", 1023) {
		t.Error("width>=1024px should NOT match at 1023px")
	}
	if mediaMatches("(width<800px)", 1024) {
		t.Error("width<800px should not match at 1024px")
	}
	if !mediaMatches("(width>800px)", 1024) {
		t.Error("width>800px should match at 1024px")
	}
	// GitHub's actual breakpoints, in rem.
	if mediaMatches("(width>=48rem)", 700) {
		t.Error("width>=48rem (768px) should not match at 700px")
	}
	if !mediaMatches("(width>=48rem)", 1024) {
		t.Error("width>=48rem (768px) should match at 1024px")
	}
	// Value-first order says the same thing as width-first, for every operator
	// (each exercises a different flipCmp branch).
	if !mediaMatches("(48rem<=width)", 1024) {
		t.Error("48rem<=width should mean the same as width>=48rem")
	}
	if mediaMatches("(48rem<=width)", 700) {
		t.Error("48rem<=width should not match at 700px")
	}
	if !mediaMatches("(1024px>=width)", 1024) {
		t.Error("1024px>=width should mean the same as width<=1024px")
	}
	if mediaMatches("(1024px>=width)", 1025) {
		t.Error("1024px>=width should not match at 1025px")
	}
	if mediaMatches("(1024px<width)", 1024) {
		t.Error("1024px<width should mean the same as width>1024px: not at 1024px")
	}
	if !mediaMatches("(1024px<width)", 1025) {
		t.Error("1024px<width should match at 1025px")
	}
	if !mediaMatches("(1024px>width)", 1023) {
		t.Error("1024px>width should mean the same as width<1024px: matches at 1023px")
	}
}

// TestMediaMatchesSimpleCalc covers GitHub's exact pattern: a "calc(A - B)"
// media-feature value opening a hair's-width gap below the next breakpoint up,
// so two adjacent responsive ranges never both match the same viewport width.
func TestMediaMatchesSimpleCalc(t *testing.T) {
	if !mediaMatches("(width<=calc(48rem - .02px))", 767) {
		t.Error("width<=calc(48rem - .02px) (~767.98px) should match at 767px")
	}
	if mediaMatches("(width<=calc(48rem - .02px))", 1024) {
		t.Error("width<=calc(48rem - .02px) should NOT match at 1024px")
	}
	if !mediaMatches("(width<=calc(1000px + 24px))", 1024) {
		t.Error("width<=calc(1000px + 24px) (1024px) should match at exactly 1024px")
	}
	if mediaMatches("(width<=calc(1000px + 24px))", 1025) {
		t.Error("width<=calc(1000px + 24px) should NOT match at 1025px")
	}
	// A more complex calc() (more than two terms) is left unparsed, same as any
	// other value this simplified matcher cannot handle — the condition simply
	// finds no width feature and matches optimistically, rather than panicking
	// or matching incorrectly.
	if !mediaMatches("(width<=calc(1px + 2px + 3px))", 999999) {
		t.Error("an unparseable calc() should fall through to match optimistically")
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
	apply := func(p, v string, em float64) { s.apply(Declaration{Property: p, Value: v}, em) }
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
	for _, kw := range []string{"pixelated", "crisp-edges", "-webkit-optimize-contrast"} {
		s.ImageRendering = IRAuto
		apply("image-rendering", kw, 16)
		if s.ImageRendering != IRPixelated {
			t.Errorf("image-rendering %q should be pixelated", kw)
		}
	}
	for _, kw := range []string{"auto", "smooth", "high-quality", "optimizeQuality"} {
		s.ImageRendering = IRPixelated
		apply("image-rendering", kw, 16)
		if s.ImageRendering != IRAuto {
			t.Errorf("image-rendering %q should be auto", kw)
		}
	}
	// An unrecognised keyword leaves the value untouched.
	s.ImageRendering = IRPixelated
	apply("image-rendering", "bogus", 16)
	if s.ImageRendering != IRPixelated {
		t.Error("unknown image-rendering keyword should be ignored")
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
