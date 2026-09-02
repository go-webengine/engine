// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func styleOf(t *testing.T, htmlSrc, tag string) *Style {
	t.Helper()
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	n := dom.Find(root, tag)
	if n == nil {
		t.Fatalf("no <%s>", tag)
	}
	st := sm[n]
	if st == nil {
		t.Fatalf("no style for <%s>", tag)
	}
	return st
}

func TestCascadeUADefaults(t *testing.T) {
	h1 := styleOf(t, `<html><body><h1>x</h1></body></html>`, "h1")
	if h1.Display != DisplayBlock {
		t.Error("h1 not block")
	}
	if h1.FontSize != 32 {
		t.Errorf("h1 font-size = %v", h1.FontSize)
	}
	if !h1.Bold() {
		t.Error("h1 not bold")
	}
	a := styleOf(t, `<html><body><a>x</a></body></html>`, "a")
	if a.Color != (Color{0, 0, 238, 255}) {
		t.Errorf("a color = %v", a.Color)
	}
	if a.Display != DisplayInline {
		t.Error("a should be inline")
	}
	pre := styleOf(t, `<html><body><pre>x</pre></body></html>`, "pre")
	if pre.WhiteSpace != WSPre || pre.FontFamily != Mono {
		t.Errorf("pre = %+v", pre)
	}
	style := styleOf(t, `<html><body><p>x</p><style>p{color:red}</style></body></html>`, "style")
	if style.Display != DisplayNone {
		t.Error("style element should be display:none")
	}
}

func TestCascadeInheritance(t *testing.T) {
	// color and font-size inherit; background does not.
	st := styleOf(t, `<html><body style="color:green;font-size:20px;background-color:red">`+
		`<div><span>hi</span></div></body></html>`, "span")
	if st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("inherited color = %v", st.Color)
	}
	if st.FontSize != 20 {
		t.Errorf("inherited font-size = %v", st.FontSize)
	}
	if st.Background.A != 0 {
		t.Errorf("background should not inherit, got %v", st.Background)
	}
}

func TestCascadeImageRenderingInherits(t *testing.T) {
	// image-rendering is an inherited property: a value set on an ancestor reaches
	// a descendant <img> that never declares it.
	st := styleOf(t, `<html><body style="image-rendering:pixelated">`+
		`<div><img></div></body></html>`, "img")
	if st.ImageRendering != IRPixelated {
		t.Errorf("inherited image-rendering = %v, want IRPixelated", st.ImageRendering)
	}
	// The initial value is IRAuto (smooth) when nobody sets it.
	def := styleOf(t, `<html><body><img></body></html>`, "img")
	if def.ImageRendering != IRAuto {
		t.Errorf("default image-rendering = %v, want IRAuto", def.ImageRendering)
	}
}

func TestCascadeVisibilityInheritsAndIsOverridable(t *testing.T) {
	// visibility is inherited: a hidden ancestor's descendant is hidden too if it
	// never declares its own value.
	hidden := styleOf(t, `<html><body style="visibility:hidden">`+
		`<div><span>x</span></div></body></html>`, "span")
	if hidden.Visibility != VisibilityHidden {
		t.Errorf("inherited visibility = %v, want VisibilityHidden", hidden.Visibility)
	}
	// Unlike display:none, a descendant can override visibility back to visible
	// — this is the exact idiom real sites use for a "reveal on interaction"
	// element nested inside a permanently visibility:hidden wrapper.
	reshown := styleOf(t, `<html><body style="visibility:hidden">`+
		`<div><span style="visibility:visible">x</span></div></body></html>`, "span")
	if reshown.Visibility != VisibilityVisible {
		t.Errorf("overridden visibility = %v, want VisibilityVisible", reshown.Visibility)
	}
	// The initial value is visible when nobody sets it.
	def := styleOf(t, `<html><body><span>x</span></body></html>`, "span")
	if def.Visibility != VisibilityVisible {
		t.Errorf("default visibility = %v, want VisibilityVisible", def.Visibility)
	}
}

func TestCascadeUnitlessLineHeightInheritsAsFactor(t *testing.T) {
	// A unitless line-height set on an ancestor must inherit as the NUMBER, so a
	// larger-font descendant resolves to factor × its OWN font-size — not the
	// ancestor's collapsed pixel value. This is what stops big inline text from
	// overlapping adjacent lines.
	src := `<html><body style="font-size:16px;line-height:1.5">` +
		`<p style="font-size:16px">body-size <span style="font-size:32px">big</span></p>` +
		`</body></html>`
	sm := Cascade(mustParse(t, src))
	body := styleOf(t, src, "body")
	if body.LineHeight.Factor != 1.5 || body.LineHeight.Px != 0 {
		t.Fatalf("body line-height = %+v, want factor 1.5", body.LineHeight)
	}
	// The <span> inherits the factor and resolves against its own 32px font-size.
	var span *Style
	for n, st := range sm {
		if n.Tag == "span" {
			span = st
		}
	}
	if span == nil {
		t.Fatal("no span style")
	}
	if span.LineHeight.Factor != 1.5 {
		t.Fatalf("span inherited factor = %v, want 1.5", span.LineHeight.Factor)
	}
	if px, ok := span.LineHeight.Resolve(span.FontSize); !ok || px != 48 {
		t.Errorf("span line-height Resolve = %v,%v want 48,true (1.5×32)", px, ok)
	}
	// The 16px paragraph text still resolves to 24 with the same inherited factor.
	p := styleOf(t, src, "p")
	if px, ok := p.LineHeight.Resolve(p.FontSize); !ok || px != 24 {
		t.Errorf("p line-height Resolve = %v,%v want 24,true (1.5×16)", px, ok)
	}
}

func TestCascadeSpecificityOrdering(t *testing.T) {
	src := `<html><head><style>
		p { color: red }
		.c { color: green }
		#i { color: blue }
	</style></head><body>
		<p id="i" class="c" style="color: black">x</p>
	</body></html>`
	// inline (black) beats id (blue) beats class beats tag.
	if st := styleOf(t, src, "p"); st.Color != (Color{0, 0, 0, 255}) {
		t.Errorf("inline should win, got %v", st.Color)
	}

	// Without inline, id wins.
	src2 := `<html><head><style>
		p { color: red } .c { color: green } #i { color: blue }
	</style></head><body><p id="i" class="c">x</p></body></html>`
	if st := styleOf(t, src2, "p"); st.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("id should win, got %v", st.Color)
	}

	// Equal specificity: later source rule wins.
	src3 := `<html><head><style>.c{color:red}.c{color:green}</style></head>` +
		`<body><p class="c">x</p></body></html>`
	if st := styleOf(t, src3, "p"); st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("later rule should win, got %v", st.Color)
	}
}

// TestCascadeImportantBeatsSpecificity covers the cascade tier added for
// `!important`: a low-specificity class rule marked !important must win over a
// higher-specificity id rule that is not — the opposite of the normal
// specificity order proven in TestCascadeSpecificityOrdering above. This is
// exactly the shape a real site (MDN) relies on to force a component hidden
// (`.left-sidebar{display:none!important}`) regardless of any later, more
// specific rule that would otherwise reveal it.
func TestCascadeImportantBeatsSpecificity(t *testing.T) {
	src := `<html><head><style>
		.c { color: green !important }
		#i { color: blue }
	</style></head><body><p id="i" class="c">x</p></body></html>`
	if st := styleOf(t, src, "p"); st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("!important class should beat non-important id, got %v", st.Color)
	}
}

// TestCascadeImportantOrderingWithinTier covers that two !important
// declarations still resolve by the normal specificity/order rules AMONG
// themselves — !important lifts a declaration into a higher tier, it does not
// exempt it from the ordinary cascade within that tier.
func TestCascadeImportantOrderingWithinTier(t *testing.T) {
	// Higher specificity important wins over lower specificity important.
	src := `<html><head><style>
		.c { color: red !important }
		#i { color: blue !important }
	</style></head><body><p id="i" class="c">x</p></body></html>`
	if st := styleOf(t, src, "p"); st.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("higher-specificity !important should win, got %v", st.Color)
	}

	// Equal specificity: the later !important rule wins.
	src2 := `<html><head><style>
		.c { color: red !important }
		.c { color: green !important }
	</style></head><body><p class="c">x</p></body></html>`
	if st := styleOf(t, src2, "p"); st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("later !important rule should win, got %v", st.Color)
	}
}

// TestCascadeImportantAbsentUnaffected proves a stylesheet with no !important
// at all sorts identically to before — the new comparator branch is a pure
// addition, not a behaviour change for the common case.
func TestCascadeImportantAbsentUnaffected(t *testing.T) {
	src := `<html><head><style>p{color:red}.c{color:green}#i{color:blue}</style></head>` +
		`<body><p id="i" class="c">x</p></body></html>`
	if st := styleOf(t, src, "p"); st.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("id should still win with no !important present, got %v", st.Color)
	}
}

func TestCascadeAuthorOverridesUA(t *testing.T) {
	// Author rule beats UA default (a's blue link colour).
	src := `<html><head><style>a{color:red}</style></head><body><a>x</a></body></html>`
	if st := styleOf(t, src, "a"); st.Color != (Color{255, 0, 0, 255}) {
		t.Errorf("author should override UA, got %v", st.Color)
	}
}

// TestCascadeInheritKeywordCancelsUADefault covers the real regression this
// engine had: `inherit` was not understood as a value AT ALL, so
// `a{color:inherit}` (Tailwind's, and most modern frameworks', preflight
// reset — confirmed live on tailwindcss.com) failed to parse and was
// dropped, leaving the UA default `a{color:#0000ee}` (browser-default link
// blue) as the only surviving declaration. Every `fill="currentColor"` SVG
// icon inside such a reset anchor (a very common pattern for a linked logo)
// inherited that wrong blue too. `color` is already inherited BY DEFAULT
// (TestCascadeCustomPropertyInherited-style), but that default alone is not
// enough here: the UA rule runs earlier in the very same cascade and
// overwrites it, so `inherit` must explicitly copy the parent's value again
// to cancel that override, not merely no-op.
func TestCascadeInheritKeywordCancelsUADefault(t *testing.T) {
	src := `<html><head><style>a{color:inherit}</style></head>` +
		`<body style="color:green"><a>x</a></body></html>`
	if st := styleOf(t, src, "a"); st.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("a{color:inherit} should cancel the UA blue default and use the parent's green, got %v", st.Color)
	}
}

func TestCascadeInheritKeywordOtherProperties(t *testing.T) {
	// visibility, font-weight and text-align are not overridden by any UA
	// default the way color is, but inherit must still work for them.
	src := `<html><body style="visibility:hidden;font-weight:700;text-align:center">` +
		`<p style="visibility:inherit;font-weight:inherit;text-align:inherit">x</p></body></html>`
	st := styleOf(t, src, "p")
	if st.Visibility != VisibilityHidden {
		t.Errorf("visibility:inherit = %v, want hidden", st.Visibility)
	}
	if st.FontWeight != 700 {
		t.Errorf("font-weight:inherit = %v, want 700", st.FontWeight)
	}
	if st.TextAlign != AlignCenter {
		t.Errorf("text-align:inherit = %v, want center", st.TextAlign)
	}
}

func TestCascadeInheritKeywordRemainingProperties(t *testing.T) {
	src := `<html><body style="white-space:pre;line-height:2;list-style-type:square;list-style-position:inside">` +
		`<p style="white-space:inherit;line-height:inherit;list-style-type:inherit;list-style-position:inherit">x</p></body></html>`
	st := styleOf(t, src, "p")
	if st.WhiteSpace != WSPre {
		t.Errorf("white-space:inherit = %v, want WSPre", st.WhiteSpace)
	}
	if st.LineHeight != (LineHeight{Factor: 2}) {
		t.Errorf("line-height:inherit = %+v, want Factor 2", st.LineHeight)
	}
	if st.ListStyleType != ListSquare {
		t.Errorf("list-style-type:inherit = %v, want ListSquare", st.ListStyleType)
	}
	if st.ListStylePosition != ListInside {
		t.Errorf("list-style-position:inherit = %v, want ListInside", st.ListStylePosition)
	}
}

func TestCascadeInheritUnknownPropertyIsNoop(t *testing.T) {
	// background-color is NOT inherited per spec (transparent resets it), and
	// this engine does not implement `inherit` for it — the declaration must
	// be silently dropped rather than panic or corrupt the style.
	src := `<html><body style="background-color:red">` +
		`<p style="background-color:inherit">x</p></body></html>`
	st := styleOf(t, src, "p")
	if st.Background != Transparent {
		t.Errorf("background-color:inherit (unsupported) = %v, want the reset default transparent", st.Background)
	}
}

func TestCascadeFontSizeEm(t *testing.T) {
	// font-size:2em on a child is relative to the parent's font-size (20px→40px);
	// a margin in em is relative to the element's own computed font-size.
	src := `<html><body style="font-size:20px">` +
		`<p style="font-size:2em;margin:1em">x</p></body></html>`
	st := styleOf(t, src, "p")
	if st.FontSize != 40 {
		t.Errorf("font-size em = %v want 40", st.FontSize)
	}
	if st.Margin.Top != 40 {
		t.Errorf("margin em = %v want 40 (relative to own 40px)", st.Margin.Top)
	}
}

func TestCascadeSkipsNonElements(t *testing.T) {
	// A document with only text at the root still cascades without panic.
	root, _ := dom.Parse(`plain text`)
	sm := Cascade(root)
	if len(sm) == 0 {
		t.Error("expected some element styles (html/body auto-inserted)")
	}
}

func TestCascadeVWExternalSheetsAndMedia(t *testing.T) {
	root, err := dom.Parse(`<html><body><div class="box"><p class="lead">hi</p></div></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	external := []string{`.box { background-color: #eee } .lead { color: red }`}
	sm := CascadeVW(root, 1024, external)
	div := dom.Find(root, "div")
	if sm[div].Background != (Color{0xee, 0xee, 0xee, 255}) {
		t.Errorf("external .box background = %+v", sm[div].Background)
	}
	p := dom.Find(root, "p")
	if sm[p].Color != (Color{255, 0, 0, 255}) {
		t.Errorf("external .lead color = %+v", sm[p].Color)
	}
}

func TestCascadeVWMediaWidth(t *testing.T) {
	src := `<html><head><style>
	@media (min-width: 640px) { .side { float: right; width: 200px } }
	@media (max-width: 639px) { .side { float: none } }
	</style></head><body><div class="side">x</div></body></html>`
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	div := dom.Find(root, "div")
	// Desktop width: the min-width rule floats it right.
	if st := CascadeVW(root, 1024, nil)[div]; st.Float != FloatRight || st.Width.Px != 200 {
		t.Errorf("desktop side = float %v width %+v", st.Float, st.Width)
	}
	// Narrow width: the mobile rule wins (float none).
	if st := CascadeVW(root, 480, nil)[div]; st.Float != FloatNone {
		t.Errorf("mobile side float = %v want none", st.Float)
	}
}

func TestCascadeHiddenAttribute(t *testing.T) {
	// A bare `hidden` attribute maps to display:none at UA origin.
	if d := styleOf(t, `<html><body><div hidden>x</div></body></html>`, "div").Display; d != DisplayNone {
		t.Errorf("<div hidden> display = %v, want none", d)
	}
	// An inline author `display` beats the UA [hidden] rule (origin > specificity).
	if d := styleOf(t, `<html><body><p hidden style="display:block">x</p></body></html>`, "p").Display; d != DisplayBlock {
		t.Errorf("<p hidden style=display:block> = %v, want block (author wins)", d)
	}
	// Even a low-specificity author rule (tag selector) beats UA [hidden].
	if d := styleOf(t, `<html><head><style>span{display:flex}</style></head><body><span hidden>x</span></body></html>`, "span").Display; d != DisplayFlex {
		t.Errorf("span[hidden] with author span{display:flex} = %v, want flex (author wins)", d)
	}
	// hidden="until-found" is revealable content, NOT hidden.
	if d := styleOf(t, `<html><body><div hidden="until-found">x</div></body></html>`, "div").Display; d != DisplayBlock {
		t.Errorf(`<div hidden="until-found"> = %v, want block (not hidden)`, d)
	}
}

// TestCascadeClosedDetailsHidesNonSummaryChildren covers a real regression:
// <details>/<summary> semantics were entirely unimplemented, so a closed
// <details> (the default — no "open" attribute) rendered ALL its content,
// not just the <summary>. Confirmed load-bearing live: pkg.go.dev's help
// tooltips are <details class="go-Tooltip"><summary>...</summary>
// <p role="tooltip">the tooltip text</p></details>, only ever opened by a
// click a static render never triggers — every such tooltip's real text
// rendered permanently visible, overlapping the page.
func TestCascadeClosedDetailsHidesNonSummaryChildren(t *testing.T) {
	src := `<html><body><details>` +
		`<summary>Toggle</summary>` +
		`<p>hidden body</p>` +
		`</details></body></html>`
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	summary := dom.Find(root, "summary")
	p := dom.Find(root, "p")
	if sm[summary].Display == DisplayNone {
		t.Error("a closed <details>'s <summary> must stay visible")
	}
	if sm[p].Display != DisplayNone {
		t.Errorf("a closed <details>'s non-summary child must be hidden, got display=%v", sm[p].Display)
	}
}

func TestCascadeOpenDetailsShowsEverything(t *testing.T) {
	src := `<html><body><details open>` +
		`<summary>Toggle</summary>` +
		`<p>visible body</p>` +
		`</details></body></html>`
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	p := dom.Find(root, "p")
	if sm[p].Display == DisplayNone {
		t.Error("an OPEN <details>'s children must not be hidden")
	}
}

func TestCascadeDetailsAuthorOverride(t *testing.T) {
	// An author rule targeting the hidden child still beats the UA default,
	// same as [hidden] does — origin, not specificity, decides first.
	src := `<html><head><style>p{display:block}</style></head>` +
		`<body><details><summary>Toggle</summary><p>x</p></details></body></html>`
	if d := styleOf(t, src, "p"); d.Display != DisplayBlock {
		t.Errorf("author p{display:block} inside closed <details> = %v, want block (author wins)", d.Display)
	}
}
