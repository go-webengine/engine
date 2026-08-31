// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package layout

import (
	"reflect"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
)

// layoutShadowHTML lays out src with an EMPTY StyleMap (no cascade involved —
// css.CascadeVW does not yet compute styles for shadow-tree content; that
// lands in a separate PR). Every node then has a nil style, which collapses
// the whole document to one inline formatting context (see contents() /
// hasBlockLevelChild(), both of which treat a nil *css.Style as "not
// block-level"): exactly what these tests want, since they are checking
// slot PROJECTION — which text ends up in the flattened rendered content,
// and in what order — not box geometry. firstLineItems(box) below therefore
// reads the line items directly off the root box.
func layoutShadowHTML(t *testing.T, src string) *Box {
	t.Helper()
	root, err := dom.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := LayoutDocument(root, css.StyleMap{}, 1024, fakeMeasurer{}, nil)
	return box
}

func TestSlotDefaultDistributionAndUnmatchedSlotAttrDropped(t *testing.T) {
	// The declarative shadow root's default <slot> falls back to "Fallback"
	// only when nothing is assigned to it — here the host has an assigned
	// text node ("Loose") and an assigned element (<b>A</b>), so "Fallback"
	// must NOT appear. <i slot="missing"> names a slot that does not exist in
	// this shadow tree, so per spec it renders NOTHING — not the default
	// slot, not anywhere.
	box := layoutShadowHTML(t, `<my-elem>`+
		`<template shadowrootmode="open"><div><slot>Fallback</slot></div></template>`+
		`Loose <b>A</b><i slot="missing">Unrendered</i>`+
		`</my-elem>`)
	got := texts(firstLineItems(box))
	want := []string{"Loose", "A"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("texts = %v, want %v", got, want)
	}
}

func TestSlotNamedDistributionFollowsShadowTreeOrder(t *testing.T) {
	// The shadow tree places <slot name="b"> BEFORE <slot name="a">, so the
	// rendered order must follow the SHADOW tree's slot order, not the light
	// DOM's source order (slot="a" is written first in the light DOM below).
	box := layoutShadowHTML(t, `<my-elem>`+
		`<template shadowrootmode="open"><div><slot name="b"></slot><slot name="a"></slot></div></template>`+
		`<span slot="a">First</span><span slot="b">Second</span>`+
		`</my-elem>`)
	got := texts(firstLineItems(box))
	want := []string{"Second", "First"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("texts = %v, want %v (shadow tree order, not light DOM order)", got, want)
	}
}

func TestSlotFallbackContentWhenNothingAssigned(t *testing.T) {
	box := layoutShadowHTML(t, `<my-elem>`+
		`<template shadowrootmode="open"><div><slot>Fallback</slot></div></template>`+
		`</my-elem>`)
	got := texts(firstLineItems(box))
	want := []string{"Fallback"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("texts = %v, want %v", got, want)
	}
}

func TestSlotOutsideShadowTreeRendersOwnContent(t *testing.T) {
	// A <slot> with no enclosing shadow root (ShadowHost nil) is just a plain,
	// unrecognised element: it shows its own children, like any other unknown
	// tag — assignedSlotNodes returns nil for it (exercised directly below),
	// and renderedChildren falls back to its own Children.
	box := layoutShadowHTML(t, `<body><slot>Plain</slot></body>`)
	got := texts(firstLineItems(box))
	want := []string{"Plain"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("texts = %v, want %v", got, want)
	}
}

func TestAssignedSlotNodesNilWithoutShadowHost(t *testing.T) {
	slot := dom.NewElement("slot")
	if got := assignedSlotNodes(slot); got != nil {
		t.Errorf("assignedSlotNodes(no ShadowHost) = %v, want nil", got)
	}
}

func TestRenderedChildrenPlainElementPassthrough(t *testing.T) {
	root, err := dom.Parse(`<body><div><span>a</span><span>b</span></div></body>`)
	if err != nil {
		t.Fatal(err)
	}
	div := dom.Find(root, "div")
	got := renderedChildren(div)
	if !reflect.DeepEqual(got, div.Children) {
		t.Errorf("renderedChildren(plain div) = %v, want div.Children (%v)", got, div.Children)
	}
}

func TestSlotWithoutNameReceivesUnnamedLightChildrenOnly(t *testing.T) {
	// A named-slot child ("named") must NOT be picked up by the default
	// (unnamed) slot even though the default slot is otherwise happy to take
	// any child with no "slot" attribute.
	box := layoutShadowHTML(t, `<my-elem>`+
		`<template shadowrootmode="open"><div><slot></slot></div></template>`+
		`<b>unnamed</b><i slot="x">named</i>`+
		`</my-elem>`)
	got := texts(firstLineItems(box))
	want := []string{"unnamed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("texts = %v, want %v", got, want)
	}
}
