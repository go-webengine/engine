// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package paint

import (
	"image"
	"image/color"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// controlStyle mirrors what css/ua.go's real UA defaults now give a text-
// like control (background-color:#ffffff; border:1px solid #767676) — paint
// no longer supplies these itself, it reads them from the cascaded style
// (an author reset like `background:0 0;border:0` must be honoured, see
// TestPaintFormControlHonoursAuthorReset below), so a hand-built test style
// needs to carry them explicitly to exercise the SAME visible-box contract
// the old hardcoded-colour version tested.
func controlStyle() *css.Style {
	return &css.Style{FontFamily: css.Sans, FontSize: 14, FontWeight: 400, Color: css.Color{A: 255},
		Background: formFieldBg,
		Border: css.Borders{
			Top: css.BorderSide{Width: 1, Style: css.BorderSolid, Color: formBorder},
		},
	}
}

// paintControl lays out a single form-control InlineItem at (0,0) sized
// w×h and paints it onto a fresh white dst, mirroring how background_paint_
// test.go hand-builds a Box rather than going through the full HTML
// pipeline (paint's own tests are pipeline-agnostic by convention).
func paintControl(t *testing.T, n *dom.Node, w, h float64) *image.RGBA {
	t.Helper()
	return paintControlStyled(t, n, w, h, controlStyle())
}

// paintControlStyled is paintControl with an explicit style, for a test that
// needs a specific cascaded Background/Border (e.g. a button's own default,
// or an author reset) rather than controlStyle's plain text-field look.
func paintControlStyled(t *testing.T, n *dom.Node, w, h float64, style *css.Style) *image.RGBA {
	t.Helper()
	dst := white(int(w)+20, int(h)+20)
	item := &layout.InlineItem{
		Node: n, FormControl: n, Style: style,
		Width: w, Ascent: h, LineHeight: h, X: 5, Y: 5,
	}
	box := &layout.Box{
		Lines: []*layout.LineBox{{X: 5, Y: 5, W: w, H: h, Items: []*layout.InlineItem{item}}},
		W:     w + 10, H: h + 10,
	}
	PaintFull(dst, box, NewFonts(), nil, nil)
	return dst
}

func elem(tag string, attrs map[string]string) *dom.Node {
	return &dom.Node{Type: dom.Element, Tag: tag, Attr: attrs}
}

// TestPaintFormControlDrawsBackgroundAndBorder covers the base visible-box
// contract every kind shares: a border-colored pixel at the edge, a
// background-colored (not border, not raw white-canvas) pixel inside.
func TestPaintFormControlDrawsBackgroundAndBorder(t *testing.T) {
	n := elem("input", map[string]string{"id": "e"})
	dst := paintControl(t, n, 100, 24)

	edge := dst.RGBAAt(5, 5)
	if got, want := (css.Color{R: edge.R, G: edge.G, B: edge.B, A: 255}), formBorder; got != want {
		t.Errorf("top-left edge = %+v, want border colour %+v", got, want)
	}
	inside := dst.RGBAAt(50, 15)
	if got, want := (css.Color{R: inside.R, G: inside.G, B: inside.B, A: 255}), formFieldBg; got != want {
		t.Errorf("interior = %+v, want field background %+v", got, want)
	}
}

// TestPaintFormControlButtonBackground covers the button-like kind's darker
// background (css/ua.go's own default for button/select), distinguishing it
// visually from a plain text field. Also exercises the button label's
// horizontal-centering draw path (an empty <button> — an icon-only submit
// button, e.g. — has no label at all since the fix for pkg.go.dev's
// "Submit" text wrongly appearing next to its search icon, so this needs an
// explicit Label to still reach that code path).
func TestPaintFormControlButtonBackground(t *testing.T) {
	n := elem("button", map[string]string{"id": "e"})
	style := &css.Style{FontFamily: css.Sans, FontSize: 14, FontWeight: 400, Color: css.Color{A: 255},
		Background: formButtonBg,
		Border:     css.Borders{Top: css.BorderSide{Width: 1, Style: css.BorderSolid, Color: formBorder}},
	}
	dst := white(100, 50)
	item := &layout.InlineItem{
		Node: n, FormControl: n, Style: style, Label: "Go",
		Width: 80, Ascent: 30, LineHeight: 30, X: 5, Y: 5,
	}
	box := &layout.Box{
		Lines: []*layout.LineBox{{X: 5, Y: 5, W: 80, H: 30, Items: []*layout.InlineItem{item}}},
		W:     90, H: 40,
	}
	PaintFull(dst, box, NewFonts(), nil, nil)
	// Near the top, above where the centered "Go" label's glyphs reach
	// (the label draw is centered vertically too, so avoid sampling where
	// an ascender could land).
	inside := dst.RGBAAt(40, 9)
	if got, want := (css.Color{R: inside.R, G: inside.G, B: inside.B, A: 255}), formButtonBg; got != want {
		t.Errorf("button interior = %+v, want button background %+v", got, want)
	}
}

// TestPaintFormControlHonoursAuthorReset covers a real regression: a
// button/select/input's background+border used to be a HARDCODED colour
// paint chose regardless of the element's own cascaded style, so an author
// reset (`background:0 0;border:0` — confirmed live on github.com's own top
// nav <button>s, styled to look like plain text links, not gray boxes) was
// always overridden by fake generic chrome. A zero-alpha Background and a
// BorderNone/zero-width Border (exactly what that reset cascades to) must
// now paint NOTHING for the box itself — only the canvas underneath.
func TestPaintFormControlHonoursAuthorReset(t *testing.T) {
	n := elem("button", map[string]string{"id": "e"})
	style := &css.Style{FontFamily: css.Sans, FontSize: 14, FontWeight: 400, Color: css.Color{A: 255}}
	// Background and Border are the zero value here — exactly what
	// `background:0 0;border:0` cascades to — deliberately, not an oversight.
	dst := paintControlStyled(t, n, 80, 30, style)

	edge := dst.RGBAAt(5, 5)
	if got := (css.Color{R: edge.R, G: edge.G, B: edge.B, A: 255}); got == formBorder {
		t.Errorf("edge = %+v, an author border:0 must not paint the UA border colour", got)
	}
	inside := dst.RGBAAt(40, 9)
	if got := (css.Color{R: inside.R, G: inside.G, B: inside.B, A: 255}); got == formButtonBg {
		t.Errorf("interior = %+v, an author background:0 must not paint the UA button colour", got)
	}
}

// TestPaintCheckboxCheckedVsUnchecked covers the one kind with a state-
// dependent fill: unchecked is the plain field background, checked is the
// accent colour — the visible signal a login "remember me" box relies on.
func TestPaintCheckboxCheckedVsUnchecked(t *testing.T) {
	unchecked := elem("input", map[string]string{"type": "checkbox"})
	dstU := paintControl(t, unchecked, 13, 13)
	cu := dstU.RGBAAt(9, 9) // avoid the 1px border at (5,5)/(6,6)
	if got, want := (css.Color{R: cu.R, G: cu.G, B: cu.B, A: 255}), formFieldBg; got != want {
		t.Errorf("unchecked interior = %+v, want %+v", got, want)
	}

	checked := elem("input", map[string]string{"type": "checkbox", "checked": ""})
	dstC := paintControl(t, checked, 13, 13)
	cc := dstC.RGBAAt(9, 9)
	if got, want := (css.Color{R: cc.R, G: cc.G, B: cc.B, A: 255}), formAccent; got != want {
		t.Errorf("checked interior = %+v, want accent %+v", got, want)
	}
}

// TestPaintFormControlDrawsSomeText covers that a control WITH a value
// actually draws glyphs (some non-background pixel inside), and one with
// none does not — the difference proves text painting is actually wired,
// not just the box.
func TestPaintFormControlDrawsSomeText(t *testing.T) {
	withValue := elem("input", map[string]string{"value": "hello"})
	dst := paintControl(t, withValue, 100, 24)
	if !hasNonBackgroundPixel(dst, formFieldBg) {
		t.Error("a valued input painted no glyphs at all")
	}

	empty := elem("input", map[string]string{})
	dstEmpty := paintControl(t, empty, 100, 24)
	if hasNonBackgroundPixel(dstEmpty, formFieldBg) {
		t.Error("an empty, placeholder-less input painted something other than its box")
	}

	// A placeholder (muted text) must ALSO paint glyphs — the muted colour
	// is a different draw color, not a skip.
	placeholder := elem("input", map[string]string{"placeholder": "Email"})
	dstPH := paintControl(t, placeholder, 100, 24)
	if !hasNonBackgroundPixel(dstPH, formFieldBg) {
		t.Error("a placeholder input painted no glyphs at all")
	}
}

// TestPaintFormControlNilStylePaintsBoxOnly guards paintFormControl's own
// nil-Style path (distinct from Text=="" — Style itself absent, which a box
// with no computed style at all would hit): the box/border must still
// paint without a nil-pointer panic; text painting is simply skipped.
func TestPaintFormControlNilStylePaintsBoxOnly(t *testing.T) {
	n := elem("input", map[string]string{"value": "hello"})
	dst := white(120, 40)
	item := &layout.InlineItem{Node: n, FormControl: n, Style: nil, Width: 100, Ascent: 24, LineHeight: 24, X: 5, Y: 5}
	box := &layout.Box{Lines: []*layout.LineBox{{X: 5, Y: 5, W: 100, H: 24, Items: []*layout.InlineItem{item}}}, W: 110, H: 34}
	PaintFull(dst, box, NewFonts(), nil, nil) // must not panic
	edge := dst.RGBAAt(5, 5)
	if got, want := (css.Color{R: edge.R, G: edge.G, B: edge.B, A: 255}), formBorder; got != want {
		t.Errorf("nil-Style control still painted no border: got %+v want %+v", got, want)
	}
}

// TestPaintFormControlIconDrawsBitmapCentered guards paintFormControl's
// InlineItem.Icon path: an icon-only button (Label=="") must blit its
// icon's own bitmap (looked up in imgs, the SAME map an ordinary Image item
// uses) centred in the control's box, not leave it empty the way a bare
// FormControl item with no text used to before this field existed.
func TestPaintFormControlIconDrawsBitmapCentered(t *testing.T) {
	n := elem("button", map[string]string{})
	iconNode := elem("svg", map[string]string{})
	icon := image.NewRGBA(image.Rect(0, 0, 10, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 10; x++ {
			icon.Set(x, y, color.RGBA{R: 0xff, A: 0xff})
		}
	}
	dst := white(50, 50)
	item := &layout.InlineItem{
		Node: n, FormControl: n, Icon: iconNode, Style: controlStyle(),
		Width: 30, Ascent: 30, LineHeight: 30, X: 5, Y: 5,
	}
	box := &layout.Box{
		Lines: []*layout.LineBox{{X: 5, Y: 5, W: 30, H: 30, Items: []*layout.InlineItem{item}}},
		W:     40, H: 40,
	}
	PaintFull(dst, box, NewFonts(), map[*dom.Node]image.Image{iconNode: icon}, nil)
	// Box spans x,y in [5,35); a 10x10 icon centred in it covers [15,25).
	center := dst.RGBAAt(20, 20)
	if center.R != 0xff || center.G != 0 || center.B != 0 {
		t.Errorf("icon center pixel = %+v, want opaque red", center)
	}
	// Outside the icon but still inside the box: the control's own
	// background, not the icon colour bleeding out.
	corner := dst.RGBAAt(6, 6)
	if corner.R == 0xff && corner.G == 0 && corner.B == 0 {
		t.Errorf("icon painted outside its own bounds: corner = %+v", corner)
	}
}

func hasNonBackgroundPixel(img *image.RGBA, bg css.Color) bool {
	for y := 7; y < 20; y++ { // inside the box, away from the border
		for x := 7; x < 90; x++ {
			c := img.RGBAAt(x, y)
			if c.R != bg.R || c.G != bg.G || c.B != bg.B {
				return true
			}
		}
	}
	return false
}

func TestFormControlKind(t *testing.T) {
	cases := []struct {
		n    *dom.Node
		want controlKind
	}{
		{elem("input", map[string]string{"type": "checkbox"}), controlCheckbox},
		{elem("input", map[string]string{"type": "CHECKBOX"}), controlCheckbox},
		{elem("input", map[string]string{"type": "radio"}), controlRadio},
		{elem("input", map[string]string{"type": "submit"}), controlButtonLike},
		{elem("input", map[string]string{"type": "button"}), controlButtonLike},
		{elem("input", map[string]string{"type": "reset"}), controlButtonLike},
		{elem("input", map[string]string{"type": "text"}), controlText},
		{elem("input", map[string]string{}), controlText},
		{elem("button", map[string]string{}), controlButtonLike},
		{elem("select", map[string]string{}), controlSelect},
		{elem("textarea", map[string]string{}), controlTextarea},
		{elem("span", nil), controlText},
	}
	for _, c := range cases {
		if got := formControlKind(c.n); got != c.want {
			t.Errorf("formControlKind(%s type=%q) = %v, want %v", c.n.Tag, c.n.Attr["type"], got, c.want)
		}
	}
}

func TestFormControlDisplayText(t *testing.T) {
	cases := []struct {
		name      string
		n         *dom.Node
		label     string // InlineItem.Label — only meaningful for "button"
		wantText  string
		wantMuted bool
	}{
		{"text value", elem("input", map[string]string{"value": "hi"}), "", "hi", false},
		{"password masks", elem("input", map[string]string{"type": "password", "value": "abc"}), "", "•••", false},
		{"placeholder is muted", elem("input", map[string]string{"placeholder": "Email"}), "", "Email", true},
		{"empty, no placeholder", elem("input", map[string]string{}), "", "", false},
		{"submit uses controlLabel", elem("input", map[string]string{"type": "submit"}), "", "Submit", false},
		{"button uses precomputed label", elem("button", nil), "Go", "Go", false},
		{"button tag empty has no label", elem("button", map[string]string{}), "", "", false},
		{"textarea value attr", elem("textarea", map[string]string{"value": "explicit"}), "", "explicit", false},
		{"textarea text content", func() *dom.Node {
			n := elem("textarea", nil)
			n.Children = []*dom.Node{{Type: dom.Text, Text: "content"}}
			return n
		}(), "", "content", false},
		{"textarea placeholder", elem("textarea", map[string]string{"placeholder": "Bio"}), "", "Bio", true},
		{"textarea completely empty", elem("textarea", map[string]string{}), "", "", false},
		{"select with options", func() *dom.Node {
			n := elem("select", nil)
			n.Children = []*dom.Node{
				elem("option", map[string]string{"value": "a"}),
			}
			n.Children[0].Children = []*dom.Node{{Type: dom.Text, Text: "A"}}
			return n
		}(), "", "A", false},
		{"select with no options", elem("select", nil), "", "", false},
		{"unknown tag", elem("span", nil), "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, muted := formControlDisplayText(c.n, c.label)
			if text != c.wantText || muted != c.wantMuted {
				t.Errorf("formControlDisplayText = (%q, %v), want (%q, %v)", text, muted, c.wantText, c.wantMuted)
			}
		})
	}
}

func TestControlLabelReset(t *testing.T) {
	if got := controlLabel(elem("input", map[string]string{"type": "reset"})); got != "Reset" {
		t.Errorf("controlLabel(reset) = %q, want Reset", got)
	}
	if got := controlLabel(elem("input", map[string]string{"value": "Go!"})); got != "Go!" {
		t.Errorf("controlLabel with a value = %q, want Go!", got)
	}
}

func TestSelectedOptionLabelNoOptions(t *testing.T) {
	if _, ok := selectedOptionLabel(elem("select", nil)); ok {
		t.Error("selectedOptionLabel with no <option> children: want ok=false")
	}
}

func TestSelectedOptionLabelPicksSelected(t *testing.T) {
	sel := elem("select", nil)
	a := elem("option", map[string]string{"value": "a"})
	a.Children = []*dom.Node{{Type: dom.Text, Text: "A"}}
	b := elem("option", map[string]string{"value": "b", "selected": ""})
	b.Children = []*dom.Node{{Type: dom.Text, Text: "B"}}
	sel.Children = []*dom.Node{a, b}

	got, ok := selectedOptionLabel(sel)
	if !ok || got != "B" {
		t.Fatalf("selectedOptionLabel = (%q, %v), want (B, true)", got, ok)
	}
}

// TestSelectedOptionLabelLastSelectedWins covers the HTML standard's option
// selectedness algorithm (https://html.spec.whatwg.org/multipage/form-elements.html#concept-option-selectedness):
// when several <option> elements carry `selected`, the LAST one in tree
// order wins for a non-multiple <select> — not the first.
func TestSelectedOptionLabelLastSelectedWins(t *testing.T) {
	a := elem("option", map[string]string{"selected": ""})
	a.Children = []*dom.Node{{Type: dom.Text, Text: "A"}}
	b := elem("option", map[string]string{"selected": ""})
	b.Children = []*dom.Node{{Type: dom.Text, Text: "B"}}
	sel := elem("select", nil)
	// A whitespace text node between sibling <option>s, exactly like real
	// HTML markup (indentation/newlines) always has, must be skipped rather
	// than mistaken for an element.
	sel.Children = []*dom.Node{a, {Type: dom.Text, Text: "\n\t"}, b}

	got, ok := selectedOptionLabel(sel)
	if !ok || got != "B" {
		t.Fatalf("selectedOptionLabel = (%q, %v), want (B, true) — last selected wins", got, ok)
	}
}

// TestSelectedOptionLabelSkipsDisabledDefault covers the standard's default
// (no explicit selection) rule: the FIRST option that is not itself disabled
// wins, not simply the first option in source order.
func TestSelectedOptionLabelSkipsDisabledDefault(t *testing.T) {
	skip := elem("option", map[string]string{"disabled": ""})
	skip.Children = []*dom.Node{{Type: dom.Text, Text: "SKIP"}}
	first := elem("option", nil)
	first.Children = []*dom.Node{{Type: dom.Text, Text: "FIRST"}}
	sel := elem("select", nil)
	sel.Children = []*dom.Node{skip, first}

	got, ok := selectedOptionLabel(sel)
	if !ok || got != "FIRST" {
		t.Fatalf("selectedOptionLabel = (%q, %v), want (FIRST, true) — disabled option skipped", got, ok)
	}
}

// TestSelectedOptionLabelOptgroupDisabledSkipsChildren covers that an
// <optgroup disabled> disables every option inside it for default selection,
// even though the option itself carries no `disabled` attribute of its own.
func TestSelectedOptionLabelOptgroupDisabledSkipsChildren(t *testing.T) {
	inGroup := elem("option", nil)
	inGroup.Children = []*dom.Node{{Type: dom.Text, Text: "A"}}
	group := elem("optgroup", map[string]string{"disabled": ""})
	group.Children = []*dom.Node{inGroup}
	after := elem("option", nil)
	after.Children = []*dom.Node{{Type: dom.Text, Text: "B"}}
	sel := elem("select", nil)
	sel.Children = []*dom.Node{group, after}

	got, ok := selectedOptionLabel(sel)
	if !ok || got != "B" {
		t.Fatalf("selectedOptionLabel = (%q, %v), want (B, true) — A is inside a disabled optgroup", got, ok)
	}
}

// TestSelectedOptionLabelAttributeOverridesText covers the option label rule
// (https://html.spec.whatwg.org/multipage/form-elements.html#the-option-element):
// a non-empty `label` attribute is shown instead of the element's text
// content.
func TestSelectedOptionLabelAttributeOverridesText(t *testing.T) {
	opt := elem("option", map[string]string{"selected": "", "label": "Custom"})
	opt.Children = []*dom.Node{{Type: dom.Text, Text: "ignored text"}}
	sel := elem("select", nil)
	sel.Children = []*dom.Node{opt}

	got, ok := selectedOptionLabel(sel)
	if !ok || got != "Custom" {
		t.Fatalf("selectedOptionLabel = (%q, %v), want (Custom, true)", got, ok)
	}
}
