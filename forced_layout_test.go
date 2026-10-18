// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"strconv"
	"strings"
	"testing"

	"github.com/go-webengine/engine/css"
	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/layout"
)

// scriptTitle renders page with scripts enabled and returns document.title.
func scriptTitle(t *testing.T, page string) string {
	t.Helper()
	img, info, err := New().RenderHTML(context.Background(), page, "https://demo.test/", image.Rect(0, 0, 400, 200))
	if err != nil || img == nil {
		t.Fatalf("render: %v", err)
	}
	return info.Title
}

// TestForcedSynchronousLayout: geometry read after a DOM mutation reflects
// the mutation (CSSOM View: getBoundingClientRect, offset*, client* and
// scroll* answer from an up-to-date layout — the "forced synchronous
// layout" every engine performs). Marp's fitting headings wrap a heading's
// children in a new <span> and measure it straight away.
func TestForcedSynchronousLayout(t *testing.T) {
	static := scriptTitle(t, `<!DOCTYPE html><html><head><style>body{margin:0}h1{font-size:32px}</style></head><body>`+
		`<h1><span id="s">Short</span></h1><script>document.title=String(document.getElementById('s').getBoundingClientRect().width)</script></body></html>`)
	dynamic := scriptTitle(t, `<!DOCTYPE html><html><head><style>body{margin:0}h1{font-size:32px}</style></head><body>`+
		`<h1>Short</h1><script>var h=document.querySelector('h1'),s=document.createElement('span');`+
		`while(h.firstChild)s.appendChild(h.firstChild);h.appendChild(s);`+
		`document.title=String(s.getBoundingClientRect().width)+'|'+s.offsetWidth</script></body></html>`)
	sw, err := strconv.ParseFloat(static, 64)
	if err != nil || sw <= 0 {
		t.Fatalf("static span width %q", static)
	}
	parts := strings.Split(dynamic, "|")
	if parts[0] != static {
		t.Errorf("span created by script measures %q, want %q (same as the static span)", parts[0], static)
	}
	if parts[1] == "0" {
		t.Errorf("offsetWidth of the script-created span = 0")
	}

	// A style change is a mutation too, and so is text.
	got := scriptTitle(t, `<!DOCTYPE html><html><head><style>body{margin:0}</style></head><body>`+
		`<div id="d" style="width:100px;height:10px"></div><p id="p" style="display:inline-block">a</p><script>var d=document.getElementById('d'),p=document.getElementById('p');`+
		`var w0=d.offsetWidth;d.style.width='150px';var w1=d.offsetWidth;d.setAttribute('style','width:30px;height:10px');var w2=d.getBoundingClientRect().width;`+
		`var p0=p.offsetWidth;p.textContent='aaaaaaaaaa';var p1=p.offsetWidth;`+
		`document.title=w0+','+w1+','+w2+','+(p1>p0)</script></body></html>`)
	if got != "100,150,30,true" {
		t.Errorf("widths after style/text mutations = %q, want 100,150,30,true", got)
	}
}

// TestTreeSignature: stable for an unchanged DOM (so a read without a
// mutation never re-lays out), sensitive to attributes, text and moves.
func TestTreeSignature(t *testing.T) {
	root, err := dom.Parse(`<html><body><h1 class="a" id="h">Short</h1><p>x</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	s0 := treeSignature(root)
	for i := 0; i < 5; i++ {
		if treeSignature(root) != s0 {
			t.Fatal("signature not stable for an unchanged tree (attribute map order?)")
		}
	}
	h := dom.Find(root, "h1")
	p := dom.Find(root, "p")
	h.Attr["class"] = "b"
	s1 := treeSignature(root)
	if s1 == s0 {
		t.Error("attribute change not detected")
	}
	p.Children[0].Text = "y"
	s2 := treeSignature(root)
	if s2 == s1 {
		t.Error("text change not detected")
	}
	span := dom.NewElement("span")
	txt := h.Children[0]
	dom.RemoveChild(h, txt)
	dom.AppendChild(span, txt)
	dom.AppendChild(h, span)
	if treeSignature(root) == s2 {
		t.Error("wrapping the heading's text in a span not detected")
	}

	m := &layoutMetrics{root: root, sig: treeSignature(root)}
	calls := 0
	m.relayout = func() (*layout.Box, css.StyleMap) { calls++; return nil, nil }
	m.Refresh()
	if calls != 0 {
		t.Error("re-laid out an unchanged DOM")
	}
	h.Attr["class"] = "c"
	m.Refresh()
	m.Refresh()
	if calls != 1 {
		t.Errorf("relayout calls = %d, want 1 (once per change)", calls)
	}
	(&layoutMetrics{}).Refresh() // a static snapshot: no-op
}
