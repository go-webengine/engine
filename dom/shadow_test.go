// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import "testing"

// parseShadow is a small helper: Parse wraps htmlSrc's body content for these
// tests (Parse already returns a full document tree; callers use Find to
// reach the element they care about).
func parseShadow(t *testing.T, htmlSrc string) *Node {
	t.Helper()
	root, err := Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDeclarativeShadowRootOpen(t *testing.T) {
	root := parseShadow(t, `<body><my-elem>`+
		`<template shadowrootmode="open"><div class="wrap"><slot></slot></div></template>`+
		`<span>light child</span>`+
		`</my-elem></body>`)
	host := Find(root, "my-elem")
	if host == nil {
		t.Fatal("no <my-elem>")
	}
	if host.Shadow == nil {
		t.Fatal("shadow root not attached")
	}
	if host.Shadow.Host != host {
		t.Errorf("Shadow.Host = %v, want host itself", host.Shadow.Host)
	}
	// The <template> itself must not remain a light-DOM child.
	for _, c := range host.Children {
		if c.Type == Element && c.Tag == "template" {
			t.Error("template still present in light DOM")
		}
	}
	// The light-DOM <span> child remains (unslotted rendering is a
	// layout-level concern, not a DOM-attachment one).
	span := Find(host, "span")
	if span == nil || span.Parent != host {
		t.Fatal("light-DOM span child missing or reparented")
	}
	if len(host.Shadow.Children) != 1 || host.Shadow.Children[0].Tag != "div" {
		t.Fatalf("shadow content = %v", host.Shadow.Children)
	}
	div := host.Shadow.Children[0]
	if div.Parent != nil {
		t.Errorf("top-level shadow node Parent = %v, want nil", div.Parent)
	}
	if div.ShadowHost != host {
		t.Errorf("div.ShadowHost = %v, want host", div.ShadowHost)
	}
	slot := Find(div, "slot")
	if slot == nil {
		t.Fatal("no <slot> in shadow content")
	}
	if slot.ShadowHost != host {
		t.Errorf("slot.ShadowHost = %v, want host", slot.ShadowHost)
	}
	if slot.Parent != div {
		t.Errorf("slot.Parent = %v, want div (normal internal parent chain)", slot.Parent)
	}
}

func TestDeclarativeShadowRootClosed(t *testing.T) {
	root := parseShadow(t, `<body><my-elem>`+
		`<template shadowrootmode="closed"><p>closed content</p></template>`+
		`</my-elem></body>`)
	host := Find(root, "my-elem")
	if host.Shadow == nil {
		t.Fatal("closed shadow root not attached")
	}
	// "closed" is rendered identically to "open" — no mode is recorded.
	if len(host.Shadow.Children) != 1 || host.Shadow.Children[0].Tag != "p" {
		t.Fatalf("shadow content = %v", host.Shadow.Children)
	}
}

func TestTemplateWithoutShadowRootModeStaysPlainTemplate(t *testing.T) {
	root := parseShadow(t, `<body><my-elem><template><p>inert</p></template></my-elem></body>`)
	host := Find(root, "my-elem")
	if host.Shadow != nil {
		t.Fatal("shadow root attached without shadowrootmode")
	}
	tmpl := Find(host, "template")
	if tmpl == nil {
		t.Fatal("plain <template> should remain a light-DOM child")
	}
	// A <template>'s real content lives in its Content fragment, never in its
	// own Children (see Node.Content's doc comment) — this originally
	// asserted the content sat directly in tmpl.Children, which quietly
	// encoded the old, spec-incomplete model rather than testing the real
	// <template shadowrootmode> vs plain <template> distinction it's named
	// for; corrected to check both halves of the real shape.
	if len(tmpl.Children) != 0 {
		t.Fatalf("template's own Children = %v, want none (content lives in .Content)", tmpl.Children)
	}
	if tmpl.Content == nil || len(tmpl.Content.Children) != 1 || tmpl.Content.Children[0].Tag != "p" {
		t.Fatalf("template content = %v", tmpl.Content)
	}
}

func TestTemplateWithInvalidShadowRootModeStaysPlainTemplate(t *testing.T) {
	root := parseShadow(t, `<body><my-elem><template shadowrootmode="bogus"><p>x</p></template></my-elem></body>`)
	host := Find(root, "my-elem")
	if host.Shadow != nil {
		t.Fatal("shadow root attached for an invalid shadowrootmode value")
	}
}

func TestTemplateNotFirstElementChildIsNotDeclarativeShadowRoot(t *testing.T) {
	root := parseShadow(t, `<body><my-elem><div>first</div>`+
		`<template shadowrootmode="open"><p>x</p></template></my-elem></body>`)
	host := Find(root, "my-elem")
	if host.Shadow != nil {
		t.Fatal("shadow root attached when template was not the first element child")
	}
	// Both children remain in the light DOM, untouched.
	if len(host.Children) != 2 {
		t.Fatalf("host.Children = %v", host.Children)
	}
}

func TestDeclarativeShadowRootSkipsLeadingWhitespaceText(t *testing.T) {
	// A text-only (whitespace) node before the template does not disqualify
	// it: firstElementChild looks at the first ELEMENT child, matching how a
	// real parser is not tripped up by insignificant whitespace.
	root := parseShadow(t, "<body><my-elem>\n  <template shadowrootmode=\"open\"><p>x</p></template></my-elem></body>")
	host := Find(root, "my-elem")
	if host.Shadow == nil {
		t.Fatal("shadow root not attached past leading whitespace text")
	}
}

func TestNestedDeclarativeShadowRoots(t *testing.T) {
	root := parseShadow(t, `<body><outer-elem>`+
		`<template shadowrootmode="open">`+
		`<inner-elem><template shadowrootmode="open"><em>nested</em></template></inner-elem>`+
		`</template>`+
		`</outer-elem></body>`)
	outer := Find(root, "outer-elem")
	if outer.Shadow == nil {
		t.Fatal("outer shadow root not attached")
	}
	// Find is a light-DOM-only utility (deliberately: see its doc comment's
	// intent as a generic DOM helper) and does not descend into a Shadow
	// tree, so inner-elem is read directly off outer's shadow content rather
	// than located via Find(outer, "inner-elem").
	if len(outer.Shadow.Children) != 1 || outer.Shadow.Children[0].Tag != "inner-elem" {
		t.Fatalf("outer shadow content = %v", outer.Shadow.Children)
	}
	inner := outer.Shadow.Children[0]
	if inner.ShadowHost != outer {
		t.Errorf("inner-elem.ShadowHost = %v, want outer", inner.ShadowHost)
	}
	if inner.Shadow == nil {
		t.Fatal("nested shadow root not attached")
	}
	if inner.Shadow.Host != inner {
		t.Errorf("inner.Shadow.Host = %v, want inner itself", inner.Shadow.Host)
	}
	em := inner.Shadow.Children[0]
	if em.Tag != "em" {
		t.Fatalf("nested shadow content = %v", inner.Shadow.Children)
	}
	// The nested shadow content's ShadowHost must be the INNER host, not the
	// outer one it was transiently marked with before being hoisted.
	if em.ShadowHost != inner {
		t.Errorf("em.ShadowHost = %v, want inner (not outer)", em.ShadowHost)
	}
}

func TestElementWithNoChildrenHasNoFirstElementChild(t *testing.T) {
	root := parseShadow(t, `<body><br></body>`)
	br := Find(root, "br")
	if br == nil {
		t.Fatal("no <br>")
	}
	if got := firstElementChild(br); got != nil {
		t.Errorf("firstElementChild(<br>) = %v, want nil", got)
	}
	if br.Shadow != nil {
		t.Error("void element unexpectedly grew a shadow root")
	}
}

func TestPlainElementHasNilShadowFields(t *testing.T) {
	root := parseShadow(t, `<body><p>hi</p></body>`)
	p := Find(root, "p")
	if p.Shadow != nil || p.ShadowHost != nil {
		t.Errorf("plain element has shadow fields set: Shadow=%v ShadowHost=%v", p.Shadow, p.ShadowHost)
	}
}

// TestShadowRootAppendChildPlainNode covers the imperative counterpart to
// declarative shadow DOM (round 183, engine#319): a plain node appended to
// an empty ShadowRoot ends up in Children with a nil Parent and the right
// ShadowHost, exactly as attachDeclarativeShadowRoots's own construction
// leaves its content.
func TestShadowRootAppendChildPlainNode(t *testing.T) {
	host := NewElement("my-widget")
	sr := &ShadowRoot{Host: host}
	child := NewElement("span")
	sr.AppendChild(child)
	if len(sr.Children) != 1 || sr.Children[0] != child {
		t.Fatalf("Children = %v, want [child]", sr.Children)
	}
	if child.Parent != nil {
		t.Errorf("child.Parent = %v, want nil", child.Parent)
	}
	if child.ShadowHost != host {
		t.Errorf("child.ShadowHost = %v, want %v", child.ShadowHost, host)
	}
}

// TestShadowRootAppendChildDetachesFromExistingParent covers that moving an
// already-attached node into a shadow tree removes it from its old parent's
// light-DOM Children first — the same "move, not copy" semantics the
// light-DOM AppendChild already has.
func TestShadowRootAppendChildDetachesFromExistingParent(t *testing.T) {
	oldParent := NewElement("div")
	child := NewElement("span")
	AppendChild(oldParent, child)
	host := NewElement("my-widget")
	sr := &ShadowRoot{Host: host}
	sr.AppendChild(child)
	if len(oldParent.Children) != 0 {
		t.Errorf("child not removed from its old parent: %v", oldParent.Children)
	}
	if len(sr.Children) != 1 || sr.Children[0] != child {
		t.Fatalf("Children = %v, want [child]", sr.Children)
	}
}

// TestShadowRootAppendChildUnwrapsFragment covers the actual confirmed real
// trigger this round closes: `<template>.content.cloneNode(true)` returns a
// fresh #fragment wrapping the real clone (cloneNode's own documented
// template-content handling), and appending it must unwrap it into
// Children directly — exactly as the light-DOM AppendChild already does for
// a DocumentFragment — or the real content ends up one level too deep for
// cascade/layout's renderedChildren to ever reach.
func TestShadowRootAppendChildUnwrapsFragment(t *testing.T) {
	host := NewElement("my-widget")
	sr := &ShadowRoot{Host: host}
	frag := &Node{Type: Element, Tag: "#fragment", Attr: map[string]string{}}
	a := NewElement("span")
	b := NewElement("em")
	AppendChild(frag, a)
	AppendChild(frag, b)
	sr.AppendChild(frag)
	if len(sr.Children) != 2 || sr.Children[0] != a || sr.Children[1] != b {
		t.Fatalf("Children = %v, want [a, b] (the fragment unwrapped, not itself appended)", sr.Children)
	}
	if a.Parent != nil || b.Parent != nil {
		t.Errorf("unwrapped children still have a Parent: a=%v b=%v", a.Parent, b.Parent)
	}
	if a.ShadowHost != host || b.ShadowHost != host {
		t.Errorf("unwrapped children missing ShadowHost: a=%v b=%v want %v", a.ShadowHost, b.ShadowHost, host)
	}
}

// TestShadowRootAppendChildNilIsNoOp covers the nil guard.
func TestShadowRootAppendChildNilIsNoOp(t *testing.T) {
	sr := &ShadowRoot{Host: NewElement("my-widget")}
	sr.AppendChild(nil)
	if len(sr.Children) != 0 {
		t.Errorf("Children = %v, want empty", sr.Children)
	}
}
