// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import "testing"

// TestUADeclarationsAllBranches exercises every arm of the user-agent
// stylesheet switch so its defaults are all covered and sane.
func TestUADeclarationsAllBranches(t *testing.T) {
	blockish := []string{"html", "body", "div", "section", "article", "header",
		"footer", "nav", "main", "aside", "figure", "table", "tr", "form", "hr",
		"blockquote", "pre", "address", "fieldset", "figcaption", "p",
		"h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "dd", "dt"}
	for _, tag := range blockish {
		decls := uaDeclarations(tag)
		if !hasDisplay(decls, "block") {
			t.Errorf("%s: expected display:block, got %v", tag, decls)
		}
	}
	inlineish := []string{"span", "label", "abbr", "sup", "sub", "mark", "u", "s",
		"del", "ins", "time", "q", "img", "button", "input", "select", "textarea"}
	for _, tag := range inlineish {
		if !hasDisplay(uaDeclarations(tag), "inline") {
			t.Errorf("%s: expected display:inline", tag)
		}
	}
	hidden := []string{"head", "title", "style", "script", "meta", "link", "base", "noscript"}
	for _, tag := range hidden {
		if !hasDisplay(uaDeclarations(tag), "none") {
			t.Errorf("%s: expected display:none", tag)
		}
	}
	// Text-styling tags.
	if !hasDecl(uaDeclarations("a"), "color", "#0000ee") {
		t.Error("a: expected link colour")
	}
	for _, tag := range []string{"strong", "b"} {
		if !hasDecl(uaDeclarations(tag), "font-weight", "bold") {
			t.Errorf("%s: expected bold", tag)
		}
	}
	for _, tag := range []string{"code", "kbd", "samp", "tt"} {
		if !hasDecl(uaDeclarations(tag), "font-family", "monospace") {
			t.Errorf("%s: expected monospace", tag)
		}
	}
	if !hasDecl(uaDeclarations("small"), "font-size", "13px") {
		t.Error("small font-size")
	}
	// Tags with no default declarations.
	for _, tag := range []string{"em", "i", "cite", "var", "unknowntag"} {
		if uaDeclarations(tag) != nil {
			t.Errorf("%s: expected nil declarations", tag)
		}
	}
	// Special block extras.
	if !hasDecl(uaDeclarations("body"), "margin", "8px") {
		t.Error("body margin")
	}
	if !hasDecl(uaDeclarations("blockquote"), "margin", "16px 40px") {
		t.Error("blockquote margin")
	}
	if !hasDecl(uaDeclarations("pre"), "white-space", "pre") {
		t.Error("pre white-space")
	}
	if !hasDecl(uaDeclarations("ul"), "padding-left", "40px") {
		t.Error("ul padding")
	}
}

func hasDisplay(decls []Declaration, v string) bool {
	return hasDecl(decls, "display", v)
}

func hasDecl(decls []Declaration, prop, val string) bool {
	for _, d := range decls {
		if d.Property == prop && d.Value == val {
			return true
		}
	}
	return false
}

func TestHexUppercase(t *testing.T) {
	if c, ok := parseColor("#ABCDEF"); !ok || c != (Color{0xAB, 0xCD, 0xEF, 255}) {
		t.Errorf("uppercase hex = %v %v", c, ok)
	}
}
