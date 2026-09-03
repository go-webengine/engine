// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import "testing"

// TestFormControlExplicitCSSSize covers the common real-world case: an
// author-styled control (width/height in CSS, the way any real form does
// it) uses exactly that size, not a UA default.
func TestFormControlExplicitCSSSize(t *testing.T) {
	src := `<html><body><input id="e" style="width:200px;height:24px"></body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "body"))
	if len(items) != 1 || items[0].FormControl == nil {
		t.Fatalf("expected one form-control item, got %v", items)
	}
	assertF(t, "width", items[0].Width, 200)
	assertF(t, "height", items[0].LineHeight, 24)
}

// TestFormControlSpaceBeforeWhenPrecededByText covers the case a bare
// "<input>" fixture never exercises: a control preceded by inline text
// with trailing whitespace ("Label <input>", the normal shape of a real
// form) needs its own SpaceBefore, the same collapsible-space handling
// img/svg items already get, or the label and field would render glued
// together with no gap.
func TestFormControlSpaceBeforeWhenPrecededByText(t *testing.T) {
	src := `<html><body>Label <input id="e"></body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "body"))
	if len(items) != 2 || items[1].FormControl == nil {
		t.Fatalf("expected [text, form-control], got %v", texts(items))
	}
	if items[1].SpaceBefore <= 0 {
		t.Fatalf("SpaceBefore = %v, want > 0 (a space separated \"Label\" from the input)", items[1].SpaceBefore)
	}
}

// TestFormControlDefaultSizes covers the UA-default fallback per control
// kind, for a page that (like plenty of real ones) leaves an input unstyled.
func TestFormControlDefaultSizes(t *testing.T) {
	cases := []struct {
		name       string
		src        string
		wantW      float64
		wantH      float64
		wantHExact bool
	}{
		{"text input", `<input id="e">`, 170, 0, false},
		{"checkbox", `<input id="e" type="checkbox">`, 13, 13, true},
		{"radio", `<input id="e" type="radio">`, 13, 13, true},
		{"select", `<select id="e"></select>`, 170, 0, false},
		{"textarea", `<textarea id="e"></textarea>`, 200, 60, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := `<html><body>` + c.src + `</body></html>`
			items := firstLineItems(findBox(layoutHTML(t, src, 1024), "body"))
			if len(items) != 1 || items[0].FormControl == nil {
				t.Fatalf("%s: expected one form-control item, got %v", c.name, items)
			}
			assertF(t, c.name+" width", items[0].Width, c.wantW)
			if c.wantHExact {
				assertF(t, c.name+" height", items[0].LineHeight, c.wantH)
			} else if items[0].LineHeight <= 0 {
				t.Errorf("%s: height = %v, want > 0", c.name, items[0].LineHeight)
			}
		})
	}
}

// TestFormControlButtonSizesToLabel covers a button-like control's intrinsic
// (content-sized) default: with fakeMeasurer's exact 10px/char, the width is
// predictable, unlike a real font — this is what actually proves the sizing
// reaches the label text, not just a fixed constant.
func TestFormControlButtonSizesToLabel(t *testing.T) {
	src := `<html><body><input id="e" type="submit" value="Log in"></body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "body"))
	if len(items) != 1 || items[0].FormControl == nil {
		t.Fatal("expected one form-control item")
	}
	// "Log in" = 6 runes * 10px (fakeMeasurer) + 2*formControlPadX(12) = 84.
	assertF(t, "submit button width", items[0].Width, 84)

	src2 := `<html><body><input id="e" type="submit" value="A much longer label"></body></html>`
	items2 := firstLineItems(findBox(layoutHTML(t, src2, 1024), "body"))
	if items2[0].Width <= items[0].Width {
		t.Fatalf("a longer label did not widen the button: %v vs %v", items2[0].Width, items[0].Width)
	}
}

// TestFormControlSubmitDefaultLabel covers the UA-default label text ("Submit")
// an <input type=submit> with no value attribute gets — indirectly, through
// its effect on sizing (an empty label would be narrower).
func TestFormControlSubmitDefaultLabel(t *testing.T) {
	withValue := firstLineItems(findBox(layoutHTML(t,
		`<html><body><input id="e" type="submit" value="Submit"></body></html>`, 1024), "body"))
	noValue := firstLineItems(findBox(layoutHTML(t,
		`<html><body><input id="e" type="submit"></body></html>`, 1024), "body"))
	assertF(t, "default-label width", noValue[0].Width, withValue[0].Width)
}

// TestFormControlResetAndButtonDefaultLabels covers controlLabel's other UA
// defaults ("Reset" for type=reset, "Submit" for type=button — every major
// UA gives a plain <input type=button> with no value an empty label, but
// this engine treats it the same as submit for simplicity, documented
// there) via their effect on sizing, same technique as the submit case.
func TestFormControlResetAndButtonDefaultLabels(t *testing.T) {
	reset := firstLineItems(findBox(layoutHTML(t,
		`<html><body><input id="e" type="reset"></body></html>`, 1024), "body"))
	// "Reset" (5 runes) * 10px + 24 padding = 74.
	assertF(t, "reset default width", reset[0].Width, 74)

	btn := firstLineItems(findBox(layoutHTML(t,
		`<html><body><input id="e" type="button"></body></html>`, 1024), "body"))
	// "Submit" (6 runes) * 10px + 24 padding = 84.
	assertF(t, "button default width", btn[0].Width, 84)
}

// TestFormControlButtonTagUsesTextContent covers the <button>...</button>
// tag (distinct from <input type=button>): its label is its TEXT CONTENT,
// not a value attribute, and an empty one falls back to "Submit" — the
// real-world unstyled-button case go-aiquota's own login flow uses.
func TestFormControlButtonTagUsesTextContent(t *testing.T) {
	labeled := firstLineItems(findBox(layoutHTML(t,
		`<html><body><button id="e">Log in</button></body></html>`, 1024), "body"))
	if len(labeled) != 1 || labeled[0].FormControl == nil {
		t.Fatalf("expected one form-control item, got %v", labeled)
	}
	// "Log in" = 6 runes * 10px + 24 padding = 84.
	assertF(t, "<button>Log in</button> width", labeled[0].Width, 84)

	empty := firstLineItems(findBox(layoutHTML(t,
		`<html><body><button id="e"></button></body></html>`, 1024), "body"))
	// Falls back to "Submit" (6 runes): same width as the labeled case above
	// since both labels happen to be 6 characters.
	assertF(t, "empty <button> width", empty[0].Width, 84)
}

// TestFormControlHiddenInputTakesNoBox covers the one control kind that must
// NOT get a box at all: a hidden input, matching real UA behavior (an
// invisible, un-clickable field carries no visible box or click target).
func TestFormControlHiddenInputTakesNoBox(t *testing.T) {
	src := `<html><body><input id="e" type="hidden" value="csrf-token">visible text</body></html>`
	items := firstLineItems(findBox(layoutHTML(t, src, 1024), "body"))
	for _, it := range items {
		if it.FormControl != nil {
			t.Fatalf("a hidden input produced a box: %v", it)
		}
	}
	if len(items) != 1 || items[0].Text != "visible" {
		// "visible text" is two words after fakeMeasurer's word-splitting;
		// just confirm the surrounding text still lays out normally.
		found := false
		for _, it := range items {
			if it.Text == "visible" {
				found = true
			}
		}
		if !found {
			t.Fatalf("surrounding text missing around the hidden input: %v", texts(items))
		}
	}
}

// TestFormControlDisplayBlockRoutesThroughContents covers the rarer path:
// author CSS giving a control `display:block` routes through the SAME
// sizing (contents()' own isFormControlTag branch), not appendElementInline
// — proving the two entry points agree rather than one being dead code that
// silently diverges from the other.
func TestFormControlDisplayBlockRoutesThroughContents(t *testing.T) {
	src := `<html><body><input id="e" style="display:block;width:150px;height:20px"></body></html>`
	box := findBox(layoutHTML(t, src, 1024), "input")
	if box == nil {
		t.Fatal("display:block input did not get its own box")
	}
	items := firstLineItems(box)
	if len(items) != 1 || items[0].FormControl == nil {
		t.Fatalf("display:block input's own box has no form-control item: %v", items)
	}
	assertF(t, "display:block width", items[0].Width, 150)
	assertF(t, "display:block height", items[0].LineHeight, 20)
}

// TestFormControlDisplayBlockHiddenTakesNoBox is
// TestFormControlHiddenInputTakesNoBox's counterpart for the display:block
// entry point (contents(), not appendElementInline) — the hidden check is
// duplicated at both entry points (see layout.go), and both need covering.
func TestFormControlDisplayBlockHiddenTakesNoBox(t *testing.T) {
	src := `<html><body><input id="e" type="hidden" style="display:block" value="csrf-token"></body></html>`
	box := findBox(layoutHTML(t, src, 1024), "input")
	if box != nil && len(firstLineItems(box)) > 0 {
		t.Fatalf("a display:block hidden input produced content: %v", firstLineItems(box))
	}
}
