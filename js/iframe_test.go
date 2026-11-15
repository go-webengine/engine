// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

func iframePage(script string) string {
	return `<html><body><iframe id="f"></iframe><script>` + script + `</script></body></html>`
}

// setIframeDoc simulates what engine's loadIframe does: parse the nested
// HTML and attach it to the iframe node's own IframeDoc field.
func setIframeDoc(root *dom.Node, iframeID, nestedHTML string) {
	n := byID(root, iframeID)
	nested, err := dom.Parse(nestedHTML)
	if err != nil {
		panic(err)
	}
	n.IframeDoc = nested
}

func TestIframeContentDocumentNilBeforeLoad(t *testing.T) {
	_, logs, _ := runJS(t, iframePage(`
		var f = document.getElementById('f');
		console.log('doc=' + f.contentDocument);
		console.log('win=' + f.contentWindow);
	`))
	mustHave(t, logs, "doc=null", "win=null")
}

func TestIframeContentDocumentReadsNestedDOM(t *testing.T) {
	root, err := dom.Parse(iframePage(`
		var d = document.getElementById('f').contentDocument;
		console.log('title=' + d.title);
		console.log('p=' + d.getElementById('p').textContent);
		console.log('qs=' + d.querySelector('.msg').textContent);
	`))
	if err != nil {
		t.Fatal(err)
	}
	setIframeDoc(root, "f", `<html><head><title>Nested</title></head><body>
		<p id="p">hello from the iframe</p>
		<span class="msg">a message</span>
	</body></html>`)
	var logs []string
	Run(root, Options{PageURL: testURL, Log: func(s string) { logs = append(logs, s) }})
	mustHave(t, logs, "title=Nested", "p=hello from the iframe", "qs=a message")
}

func TestIframeContentWindowExposesDocumentAndLocation(t *testing.T) {
	root, err := dom.Parse(iframePage(`
		var f = document.getElementById('f');
		f.src = '/frame.html';
		var w = f.contentWindow;
		console.log('href=' + w.location.href);
		console.log('body=' + w.document.body.textContent);
	`))
	if err != nil {
		t.Fatal(err)
	}
	setIframeDoc(root, "f", `<html><body>nested body text</body></html>`)
	var logs []string
	Run(root, Options{PageURL: testURL, Log: func(s string) { logs = append(logs, s) }})
	mustHave(t, logs, "href=/frame.html", "body=nested body text")
}

func TestIframeContentDocumentIdentityIsStable(t *testing.T) {
	root, err := dom.Parse(iframePage(`
		var f = document.getElementById('f');
		console.log('same-doc=' + (f.contentDocument === f.contentDocument));
		console.log('same-win=' + (f.contentWindow === f.contentWindow));
	`))
	if err != nil {
		t.Fatal(err)
	}
	setIframeDoc(root, "f", `<html><body></body></html>`)
	var logs []string
	Run(root, Options{PageURL: testURL, Log: func(s string) { logs = append(logs, s) }})
	mustHave(t, logs, "same-doc=true", "same-win=true")
}

// TestIframeContentDocumentMutationIsLive confirms mutating a node reached
// through contentDocument changes the SAME underlying *dom.Node the iframe's
// own render used — real, live access, not a copy.
func TestIframeContentDocumentMutationIsLive(t *testing.T) {
	root, err := dom.Parse(iframePage(`
		document.getElementById('f').contentDocument.getElementById('p').textContent = 'changed';
	`))
	if err != nil {
		t.Fatal(err)
	}
	fNode := byID(root, "f")
	nested, err := dom.Parse(`<html><body><p id="p">original</p></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	fNode.IframeDoc = nested
	Run(root, Options{PageURL: testURL})
	p := byID(nested, "p")
	if p == nil {
		t.Fatal("expected the nested <p> to still exist")
	}
	if dom.TextContent(p) != "changed" {
		t.Fatalf("nested <p> text = %q, want %q (the mutation should hit the real node)", dom.TextContent(p), "changed")
	}
}
