// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func TestCascadePseudoElementAfterLiteralContent(t *testing.T) {
	// pkg.go.dev's own breadcrumb separator pattern — the confirmed real
	// trigger this feature closes: `li:not(:last-child):after{content:">"}`.
	root, err := dom.Parse(`<html><body>
		<style>li.sep::after{content:">";color:blue}</style>
		<ul><li class="sep" id="a">x</li></ul>
	</body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	sm := Cascade(root)
	li := dom.Find(root, "li")
	st := sm[li]
	if st == nil {
		t.Fatal("no style for <li>")
	}
	if st.After == nil {
		t.Fatal("expected an ::after pseudo-style, got nil")
	}
	if !st.After.ContentSet || st.After.Content != ">" {
		t.Errorf("after.Content = %q, ContentSet = %v, want \">\", true", st.After.Content, st.After.ContentSet)
	}
	if st.After.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("after.Color = %+v, want blue", st.After.Color)
	}
	if st.Before != nil {
		t.Errorf("expected no ::before, got %+v", st.Before)
	}
}

func TestCascadePseudoElementBeforeEmptyContentStillGeneratesABox(t *testing.T) {
	// The MDN/icon idiom: content:"" (not "no content at all") generates a
	// real, empty box whose own background/mask-image is the whole point —
	// ContentSet distinguishes this from "content never set".
	st := styleOf(t, `<html><head><style>
		.icon::before{content:"";background-color:red;width:10px;height:10px}
	</style></head><body><span class="icon">x</span></body></html>`, "span")
	if st.Before == nil {
		t.Fatal("expected a ::before pseudo-style, got nil")
	}
	if !st.Before.ContentSet || st.Before.Content != "" {
		t.Errorf("before.Content = %q, ContentSet = %v, want \"\", true", st.Before.Content, st.Before.ContentSet)
	}
	if st.Before.Background != (Color{255, 0, 0, 255}) {
		t.Errorf("before.Background = %+v, want red", st.Before.Background)
	}
}

func TestCascadeNoContentDeclarationGeneratesNoPseudoElement(t *testing.T) {
	// The overwhelming common case: a stylesheet targeting ::after for some
	// OTHER reason (sizing a scrollbar pseudo-element, say) without ever
	// setting `content` must not generate a box at all — content's initial
	// value is `normal`, which computes to `none` for ::before/::after.
	st := styleOf(t, `<html><head><style>
		p::after{color:red}
	</style></head><body><p>x</p></body></html>`, "p")
	if st.After != nil {
		t.Errorf("expected no ::after (content never set), got %+v", st.After)
	}
}

func TestCascadePseudoElementUnsupportedContentValueGeneratesNothing(t *testing.T) {
	// attr()/counter()/url() and the rest of the content grammar are not
	// understood — per spec an unsupported value invalidates the WHOLE
	// declaration, leaving content at its initial `normal` (no box), not a
	// box with garbage text.
	st := styleOf(t, `<html><head><style>
		a::after{content:attr(href)}
	</style></head><body><a href="https://example.com/">x</a></body></html>`, "a")
	if st.After != nil {
		t.Errorf("expected no ::after (unsupported content value), got %+v", st.After)
	}
}

func TestCascadePseudoElementSpecificityOrdering(t *testing.T) {
	// Two rules both targeting the same element's ::after: the higher-
	// specificity one's content wins, exactly like an ordinary property.
	st := styleOf(t, `<html><head><style>
		li::after{content:"-"}
		li.sep::after{content:">"}
	</style></head><body><ul><li class="sep">x</li></ul></body></html>`, "li")
	if st.After == nil || st.After.Content != ">" {
		t.Errorf("after = %+v, want Content \">\" (higher specificity)", st.After)
	}
}

func TestCascadePseudoElementImportantBeatsSpecificity(t *testing.T) {
	// Exercises computePseudoElement's own cascade sort: a lower-specificity
	// !important declaration must still beat a higher-specificity normal
	// one, exactly like an ordinary (non-pseudo-element) property.
	st := styleOf(t, `<html><head><style>
		li::after{content:"-" !important}
		li.sep::after{content:">"}
	</style></head><body><ul><li class="sep">x</li></ul></body></html>`, "li")
	if st.After == nil || st.After.Content != "-" {
		t.Errorf("after = %+v, want Content \"-\" (!important beats higher specificity)", st.After)
	}
}

func TestCascadePseudoElementFontSizeEm(t *testing.T) {
	// Exercises computePseudoElement's dedicated font-size pass: em in any
	// OTHER declaration on the same pseudo-element rule must resolve
	// against ITS OWN (just-set) font-size, not the originating element's.
	st := styleOf(t, `<html><head><style>
		li.sep::after{content:">";font-size:20px;margin-left:0.5em}
	</style></head><body><ul><li class="sep">x</li></ul></body></html>`, "li")
	if st.After == nil {
		t.Fatal("expected an ::after pseudo-style")
	}
	if st.After.FontSize != 20 {
		t.Fatalf("after.FontSize = %v, want 20", st.After.FontSize)
	}
	if st.After.Margin.Left != 10 {
		t.Errorf("after margin-left = %v, want 10px (0.5em of the pseudo-element's OWN 20px)", st.After.Margin.Left)
	}
}

func TestCascadePseudoElementColorSchemeAndLightDark(t *testing.T) {
	// Exercises computePseudoElement's dedicated color-scheme pass: a
	// light-dark() in another declaration on the SAME rule must resolve
	// against the pseudo-element's own color-scheme, set earlier in the
	// very same declaration block.
	st := styleOf(t, `<html><head><style>
		li.sep::after{content:">";color-scheme:dark;color:light-dark(black,white)}
	</style></head><body><ul><li class="sep">x</li></ul></body></html>`, "li")
	if st.After == nil {
		t.Fatal("expected an ::after pseudo-style")
	}
	if st.After.Color != (Color{255, 255, 255, 255}) {
		t.Errorf("after.Color = %+v, want white (dark scheme)", st.After.Color)
	}
}

func TestCascadePseudoElementUnresolvedVarDropsDeclaration(t *testing.T) {
	// A var() with no fallback, referencing a custom property that is
	// never declared anywhere, must drop just that one declaration —
	// content still generates (it resolved fine), and the undefined color
	// declaration never overrides the INHERITED colour (black, from the
	// UA default, inherited from the real <li> per
	// TestCascadePseudoElementInheritsFromOriginatingElement) rather than
	// falling to the Color zero value (fully transparent).
	st := styleOf(t, `<html><head><style>
		li.sep::after{content:">";color:var(--undefined-nowhere)}
	</style></head><body><ul><li class="sep">x</li></ul></body></html>`, "li")
	if st.After == nil || !st.After.ContentSet {
		t.Fatal("expected ::after to still generate (content resolved fine)")
	}
	if st.After.Color != (Color{0, 0, 0, 255}) {
		t.Errorf("after.Color = %+v, want inherited black (unresolved var dropped the declaration)", st.After.Color)
	}
}

func TestCascadePseudoElementMatchesThroughAncestorCombinator(t *testing.T) {
	// A multi-compound selector ("ul.breadcrumb li::after") exercises
	// MatchesPseudoElement's matchLeft delegation, not just a single bare
	// compound — the ancestor <ul> constraint must also be checked.
	st := styleOf(t, `<html><head><style>
		ul.breadcrumb li::after{content:">"}
	</style></head><body><ul class="breadcrumb"><li>x</li></ul></body></html>`, "li")
	if st.After == nil || st.After.Content != ">" {
		t.Errorf("after = %+v, want Content \">\" (ancestor combinator satisfied)", st.After)
	}

	// The same selector against an <li> under a DIFFERENT ancestor class
	// must NOT match — confirms the ancestor constraint is actually
	// enforced, not silently skipped.
	st2 := styleOf(t, `<html><head><style>
		ul.breadcrumb li::after{content:">"}
	</style></head><body><ul class="other"><li>x</li></ul></body></html>`, "li")
	if st2.After != nil {
		t.Errorf("after = %+v, want nil (ancestor class does not match)", st2.After)
	}
}

func TestCascadePseudoElementInheritsFromOriginatingElement(t *testing.T) {
	// A pseudo-element inherits FROM the real element it is generated on,
	// not from that element's DOM parent — color, un-set on the ::after
	// rule itself, should come from <li>'s own (inherited-from-body) green.
	st := styleOf(t, `<html><head><style>
		li.sep::after{content:">"}
	</style></head><body style="color:green"><ul><li class="sep">x</li></ul></body></html>`, "li")
	if st.After == nil {
		t.Fatal("expected an ::after pseudo-style")
	}
	if st.After.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("after.Color = %+v, want green (inherited from <li>)", st.After.Color)
	}
}
