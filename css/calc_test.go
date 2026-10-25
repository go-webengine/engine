// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strings"
	"testing"
)

func TestResolveCalcNoOccurrence(t *testing.T) {
	got, ok := resolveCalc("1px solid red")
	if !ok || got != "1px solid red" {
		t.Fatalf("resolveCalc no-op = %q,%v", got, ok)
	}
}

// TestResolveCalcSpacingScaleMultiply is the real, live shape found on
// tailwindcss.com: its entire numeric spacing scale (`size-48`, `h-48`,
// `p-4`, `gap-4`, `mt-4`, …) compiles to `calc(var(--spacing) * N)`, with
// var() already substituted to a literal length by the time resolveCalc
// runs. Without this, every one of those utilities failed to parse as a
// length and the declaration was dropped — e.g. a `size-48` avatar (meant to
// be 192×192px) fell back to its raw intrinsic image size, many times too
// large.
func TestResolveCalcSpacingScaleMultiply(t *testing.T) {
	got, ok := resolveCalc("calc(.25rem * 48)")
	if !ok || got != "192px" {
		t.Fatalf("resolveCalc(spacing*48) = %q,%v want 192px,true", got, ok)
	}
	// Order shouldn't matter: number * length.
	got, ok = resolveCalc("calc(48 * .25rem)")
	if !ok || got != "192px" {
		t.Fatalf("resolveCalc(48*spacing) = %q,%v want 192px,true", got, ok)
	}
}

func TestResolveCalcSubtractAndDivide(t *testing.T) {
	got, ok := resolveCalc("calc(48rem - 2px)")
	if !ok || got != "766px" {
		t.Fatalf("resolveCalc subtract = %q,%v want 766px,true", got, ok)
	}
	got, ok = resolveCalc("calc(100px / 4)")
	if !ok || got != "25px" {
		t.Fatalf("resolveCalc divide = %q,%v want 25px,true", got, ok)
	}
}

func TestResolveCalcNestedParens(t *testing.T) {
	got, ok := resolveCalc("calc((10px + 2px) * 3)")
	if !ok || got != "36px" {
		t.Fatalf("resolveCalc nested = %q,%v want 36px,true", got, ok)
	}
}

func TestResolveCalcDoesNotCrashOnPathologicallyDeepParens(t *testing.T) {
	// A style="..." attribute on the main document (itself capped only at
	// 32MB, see engine.go) can trivially carry a calc() value with millions
	// of nested parens. Before maxCalcDepth existed, factor's own recursion
	// into expr for each '(' crashed with an unrecoverable "fatal error:
	// stack overflow" (not a panic — recover() cannot catch it) somewhere
	// between 1,000,000 and 3,000,000 levels; this uses 10,000, comfortably
	// past maxCalcDepth(256) while staying fast to build and run.
	const depth = 10000
	value := "calc(" + strings.Repeat("(", depth) + "1" + strings.Repeat(")", depth) + ")"
	if _, ok := resolveCalc(value); ok {
		t.Error("want ok=false past the depth cap, got true")
	}
}

func TestResolveCalcMultipleInOneValue(t *testing.T) {
	got, ok := resolveCalc("calc(1px * 2) solid calc(4px + 4px)")
	if !ok || got != "2px solid 8px" {
		t.Fatalf("resolveCalc multi = %q,%v", got, ok)
	}
}

func TestResolveCalcPercentUnresolvable(t *testing.T) {
	// A percentage cannot be collapsed to a single number at this text-level
	// stage (it depends on the containing block at layout time) — the
	// declaration must be dropped, same as before calc() was understood.
	if _, ok := resolveCalc("calc(50% - 10px)"); ok {
		t.Error("percent-mixed calc() should be unresolvable")
	}
	if _, ok := resolveCalc("calc(50% - 1em)"); ok {
		t.Error("percent-mixed calc() should be unresolvable, in any length unit")
	}
	if _, ok := resolveCalc("calc(50% + 2)"); ok {
		t.Error("a percentage plus a bare number is invalid")
	}
	for _, bad := range []string{"calc(5px%)", "calc(1.2.3%)"} {
		if _, ok := resolveCalc(bad); ok {
			t.Errorf("%s is not a number", bad)
		}
	}
	if _, ok := resolveCalc("calc(50% * 50%)"); ok {
		t.Error("percentage times percentage is invalid")
	}
	if _, ok := resolveCalc("calc(10 / 50%)"); ok {
		t.Error("dividing by a percentage is invalid")
	}
}

// A sum of percentages is a percentage (CSS Values 4 §10.1): Marpit's
// split-background offset, calc(100% - var(--marpit-advanced-background-split)),
// is calc(100% - 40%).
func TestResolveCalcPercentOnly(t *testing.T) {
	for in, want := range map[string]string{
		"calc(100% - 40%)":                 "60%",
		"calc(50%)":                        "50%",
		"calc(10% + 20% + 5.5%)":           "35.5%",
		"calc(50% * 2)":                    "100%",
		"calc(2 * 25%)":                    "50%",
		"calc(100% / 4)":                   "25%",
		"calc((100% - 40%) / 2)":           "30%",
		"margin: calc(100% - 40%) 0":       "margin: 60% 0",
		"calc(100% - 40%) calc(10% + 10%)": "60% 20%",
	} {
		if got, ok := resolveCalc(in); !ok || got != want {
			t.Errorf("resolveCalc(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	// a media query's calc() is in pixels: a percentage is no breakpoint
	if _, hasUnit, ok := evalCalcExpr("100% - 40%"); ok || hasUnit {
		t.Errorf("evalCalcExpr takes no percentages: %v %v", hasUnit, ok)
	}
	if v, hasUnit, ok := evalCalcExpr("10px + 5px"); !ok || !hasUnit || v != 15 {
		t.Errorf("evalCalcExpr(10px + 5px) = %v %v %v", v, hasUnit, ok)
	}
}

func TestResolveCalcInvalidExpressions(t *testing.T) {
	cases := []string{
		"calc(1px + 1)",    // length + bare number: invalid, mismatched kinds
		"calc(1px * 1px)",  // length * length: invalid
		"calc(1px / 1px)",  // dividing by a length: invalid
		"calc(1px / 0)",    // division by zero
		"calc()",           // empty
		"calc(1px",         // unterminated
		"calc(1px +)",      // dangling operator
		"calc(1px * )",     // dangling operator, second factor of a term
		"calc(1px 2px)",    // two terms with no operator: trailing garbage
		"calc((1px 2px)",   // unterminated nested paren with trailing content
		"calc(50vw + 1px)", // vw approximates as a percentage — unresolvable
		"calc(50vh)",       // same, vh
		"calc((1px + 1))",  // nested parens wrapping an invalid inner expr
	}
	for _, c := range cases {
		if _, ok := resolveCalc(c); ok {
			t.Errorf("resolveCalc(%q) should be invalid", c)
		}
	}
}

func TestResolveCalcTrailingText(t *testing.T) {
	// Plain text after the last (successfully evaluated) calc() call must be
	// preserved verbatim — exercises the loop's "no further match" exit.
	got, ok := resolveCalc("calc(1px * 2) !important")
	if !ok || got != "2px !important" {
		t.Fatalf("resolveCalc trailing text = %q,%v", got, ok)
	}
}

// TestEvalCalcExprUnterminatedNestedParen covers factor()'s own "expected a
// closing ')'" failure. resolveCalc can never actually feed it unbalanced
// content (its outer matchParen already guarantees the substring handed to
// evalCalcExpr is itself paren-balanced), so this drives evalCalcExpr
// directly, the same way vars_test.go exercises resolveOneVar/substituteVars
// directly for cases resolveDeclValue's own callers cannot construct.
func TestEvalCalcExprUnterminatedNestedParen(t *testing.T) {
	if _, _, ok := evalCalcExpr("(1px"); ok {
		t.Error("an inner '(' with no matching ')' should be invalid")
	}
}

func TestResolveCalcBareNumberResult(t *testing.T) {
	// calc(2 * 3) has no unit anywhere — not a valid length result.
	if _, ok := resolveCalc("calc(2 * 3)"); ok {
		t.Error("a unitless calc() result should be invalid as a length")
	}
}

func TestFindCalcFuncIdentifierBoundary(t *testing.T) {
	if got := findCalcFunc("mycalc(1px)", 0); got != -1 {
		t.Errorf("mycalc( matched at %d", got)
	}
	if got := findCalcFunc("mycalc(1px) calc(2px)", 0); got != 12 {
		t.Errorf("calc( after mycalc( at %d, want 12", got)
	}
}

// TestCascadeCalcThroughVar covers the real end-to-end shape: a var()
// reference inside calc() must already be substituted before resolveCalc
// evaluates the expression.
func TestCascadeCalcThroughVar(t *testing.T) {
	st := styleOf(t, `<html><body style="--spacing:.25rem">`+
		`<p style="height:calc(var(--spacing) * 48)">x</p></body></html>`, "p")
	if st.Height.Px != 192 || st.Height.Auto {
		t.Errorf("height = %+v, want 192px", st.Height)
	}
}
