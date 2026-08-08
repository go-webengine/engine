// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"strings"
	"testing"
)

func TestFragmentAndSvgPrototypes(t *testing.T) {
	logs := evalLog(t, `
		console.log('frag=' + (document.createDocumentFragment() instanceof DocumentFragment));
		console.log('svg=' + (document.createElementNS('http://www.w3.org/2000/svg','svg') instanceof SVGElement));
		console.log('unknownIsHTML=' + (document.createElement('whatever') instanceof HTMLElement));
	`)
	wantLog(t, logs, "frag=true")
	wantLog(t, logs, "svg=true")
	wantLog(t, logs, "unknownIsHTML=true")
}

func TestEventSubtypeFields(t *testing.T) {
	logs := evalLog(t, `
		console.log('prog=' + new ProgressEvent('p', { loaded: 3, total: 10, lengthComputable: true }).loaded);
		console.log('msg=' + new MessageEvent('m', { data: 'hi', origin: 'o' }).data);
		console.log('err=' + new ErrorEvent('e', { message: 'x', filename: 'f' }).filename);
		console.log('pop=' + JSON.stringify(new PopStateEvent('p', { state: { a: 1 } }).state));
		console.log('hash=' + new HashChangeEvent('h', { newURL: 'u' }).newURL);
		console.log('input=' + new InputEvent('i', { data: 'z' }).data);
		console.log('ctrl=' + new MouseEvent('m', { ctrlKey: true }).ctrlKey);
		console.log('initEvent=' + (function(){ var e = new Event('a'); e.initEvent('b', true, true); return e.type + ',' + e.bubbles; })());
		console.log('prevented=' + (function(){ var e = new Event('a'); e.preventDefault(); return e.defaultPrevented; })());
	`)
	wantLog(t, logs, "prog=3")
	wantLog(t, logs, "msg=hi")
	wantLog(t, logs, "err=f")
	wantLog(t, logs, `pop={"a":1}`)
	wantLog(t, logs, "hash=u")
	wantLog(t, logs, "input=z")
	wantLog(t, logs, "ctrl=true")
	wantLog(t, logs, "initEvent=b,true")
	wantLog(t, logs, "prevented=true")
}

func TestDOMExceptionUndefinedMessage(t *testing.T) {
	logs := evalLog(t, `
		var e = new DOMException();
		console.log('emptyName=' + e.name);
		console.log('emptyMsg=' + (e.message === ''));
		console.log('emptyStr=' + e.toString());
	`)
	wantLog(t, logs, "emptyName=Error")
	wantLog(t, logs, "emptyMsg=true")
	wantLog(t, logs, "emptyStr=Error")
}

func TestRangeObject(t *testing.T) {
	logs := evalLog(t, `
		var r = document.createRange();
		r.setStart(document.body, 0);
		r.selectNodeContents(document.body);
		console.log('collapsed=' + r.collapsed);
		console.log('clone=' + (r.cloneRange() !== null));
		console.log('frag=' + (r.cloneContents() instanceof DocumentFragment));
		console.log('ctxFrag=' + (r.createContextualFragment('<p>hi</p>') instanceof DocumentFragment));
		console.log('rect=' + (r.getBoundingClientRect().width === 0));
		console.log('rects=' + r.getClientRects().length);
		console.log('str=' + (r.toString() === ''));
	`)
	for _, k := range []string{"collapsed=true", "clone=true", "frag=true", "ctxFrag=true", "rect=true", "rects=0", "str=true"} {
		wantLog(t, logs, k)
	}
}

func TestDocumentExtraMethods(t *testing.T) {
	logs := evalLog(t, `
		console.log('focus=' + document.hasFocus());
		console.log('fromPoint=' + (document.elementFromPoint(1,1) === null));
		console.log('fromPoints=' + document.elementsFromPoint(1,1).length);
		console.log('sel=' + (document.getSelection() === null));
		console.log('evt=' + (document.createEvent('MouseEvent') instanceof Event));
		var imported = document.importNode(document.getElementById('d'), true);
		console.log('import=' + (imported instanceof HTMLElement));
		console.log('adopt=' + (document.adoptNode(document.body) === document.body));
		console.log('contains=' + document.contains(document.body));
		console.log('exec=' + document.execCommand('bold'));
		console.log('cmdState=' + document.queryCommandState('bold'));
	`)
	for _, k := range []string{"focus=true", "fromPoint=true", "fromPoints=0", "sel=true",
		"evt=true", "import=true", "adopt=true", "contains=true", "exec=false", "cmdState=false"} {
		wantLog(t, logs, k)
	}
}

// TestConsoleSurfacesStack proves console.error on an Error object surfaces its
// stack (the diagnostics improvement), while a plain object logs normally.
func TestConsoleSurfacesStack(t *testing.T) {
	_, logs, _ := runJS(t, page(`
		try { null.x; } catch (err) { console.error(err); }
		console.error({ plain: 1 });
		console.error('just a string');
	`))
	foundStack := false
	for _, l := range logs {
		if strings.Contains(l, "TypeError") && strings.Contains(l, "at ") {
			foundStack = true
		}
	}
	if !foundStack {
		t.Errorf("expected a surfaced stack in console.error output; got %v", logs)
	}
	if !has(logs, "just a string") {
		t.Errorf("plain string console.error not logged: %v", logs)
	}
}
