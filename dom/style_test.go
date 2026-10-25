// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package dom

import (
	"reflect"
	"testing"
)

func TestParseInlineStyle(t *testing.T) {
	got := ParseInlineStyle(" Color : red ;; nocolon ; : x ; margin:0 1px;")
	want := [][2]string{{"color", "red"}, {"margin", "0 1px"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q", got)
	}
	if s := SerializeInlineStyle(want); s != "color: red; margin: 0 1px" {
		t.Fatal(s)
	}
	if ParseInlineStyle("") != nil || SerializeInlineStyle(nil) != "" {
		t.Fatal("empty style")
	}
}

func TestSetStyleProperty(t *testing.T) {
	n := &Node{Type: Element, Tag: "h1"}
	if n.StyleProperty("font-size") != "" {
		t.Fatal("a property on a node with no attributes")
	}
	n.SetStyleProperty(" Font-Size ", "40px") // a node with no attributes gets a map
	n.SetStyleProperty("color", "red")
	if n.Attr["style"] != "font-size: 40px; color: red" || n.StyleProperty("font-size") != "40px" {
		t.Fatalf("%q", n.Attr["style"])
	}
	n.SetStyleProperty("font-size", "53.5px") // replaced in place, order kept
	if n.Attr["style"] != "font-size: 53.5px; color: red" {
		t.Fatalf("%q", n.Attr["style"])
	}
	n.SetStyleProperty("font-size", "") // an empty value removes it
	if n.Attr["style"] != "color: red" || n.StyleProperty("font-size") != "" {
		t.Fatalf("%q", n.Attr["style"])
	}
	n.SetStyleProperty("  ", "x") // no property name: nothing changes
	if n.Attr["style"] != "color: red" {
		t.Fatalf("%q", n.Attr["style"])
	}
}
