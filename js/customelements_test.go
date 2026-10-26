// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"testing"

	"github.com/go-webengine/engine/dom"
)

// TestCustomElementUpgradeAttachesShadowFromTemplate is the confirmed real
// trigger (round 183, engine#319): developer.apple.com's own vendor bundle
// defines a custom element, AFTER the matching tag is already parsed into
// the static HTML, whose constructor calls attachShadow() and clones a
// <template>'s content into it. Before this round neither attachShadow()
// nor a real customElements.define() existed, so the shadow tree this
// pattern depends on was never built at all.
func TestCustomElementUpgradeAttachesShadowFromTemplate(t *testing.T) {
	html := `<html><head></head><body>
		<template id="tpl"><span class="greeting">hello</span></template>
		<my-widget></my-widget>
		<script>
			class MyWidget extends HTMLElement {
				constructor() {
					super();
					var tpl = document.getElementById('tpl');
					var shadow = this.attachShadow({mode: 'open'});
					shadow.appendChild(tpl.content.cloneNode(true));
				}
			}
			customElements.define('my-widget', MyWidget);
			var host = document.querySelector('my-widget');
			console.log('hasShadow=' + (host.shadowRoot !== null));
			console.log('hostIsRealElement=' + (host.tagName === 'MY-WIDGET'));
		</script>
	</body></html>`
	root, logs, _ := runJS(t, html)
	mustHave(t, logs, "hasShadow=true", "hostIsRealElement=true")

	host := dom.Find(root, "my-widget")
	if host == nil {
		t.Fatal("no <my-widget> element in the parsed tree")
	}
	if host.Shadow == nil {
		t.Fatal("attachShadow() did not attach a dom.ShadowRoot to the real element")
	}
	if host.Shadow.Host != host {
		t.Errorf("ShadowRoot.Host = %p, want %p (the host itself)", host.Shadow.Host, host)
	}
	if len(host.Shadow.Children) != 1 {
		t.Fatalf("expected exactly one cloned child in the shadow tree, got %d", len(host.Shadow.Children))
	}
	span := host.Shadow.Children[0]
	if span.Tag != "span" || dom.TextContent(span) != "hello" {
		t.Errorf("cloned shadow content = tag %q text %q, want span %q", span.Tag, dom.TextContent(span), "hello")
	}
	if span.Parent != nil {
		t.Errorf("a shadow tree's own top-level node must have a nil Parent (see ShadowRoot's doc comment), got %v", span.Parent)
	}
	if span.ShadowHost != host {
		t.Errorf("cloned shadow content's ShadowHost = %p, want %p", span.ShadowHost, host)
	}
}

// TestCustomElementNotUpgradedWithoutMatchingTag covers that define() for a
// tag with no matching element in the document is a safe no-op (the
// existing TestCustomElementsRegistry's own 'x-foo' case, generalised): no
// panic, no phantom construction.
func TestCustomElementNotUpgradedWithoutMatchingTag(t *testing.T) {
	_, logs, _ := runJS(t, page(`
		function Ctor() { console.log('constructed'); }
		Ctor.prototype = Object.create(HTMLElement.prototype);
		customElements.define('never-present', Ctor);
	`))
	if has(logs, "constructed") {
		t.Error("constructor ran for a tag with no matching element")
	}
}

// TestCustomElementUpgradeConstructorThrowDoesNotAbortScript covers that a
// throwing constructor is contained (upgradeElement's own recover/err
// handling), matching every other best-effort callback path in this
// package — one bad custom element must not lose the rest of the script.
func TestCustomElementUpgradeConstructorThrowDoesNotAbortScript(t *testing.T) {
	html := `<html><body>
		<bad-widget></bad-widget>
		<script>
			class BadWidget extends HTMLElement {
				constructor() { super(); throw new Error('boom'); }
			}
			customElements.define('bad-widget', BadWidget);
			console.log('after-define');
		</script>
	</body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs, "after-define")
}

// TestAttachShadowIsIdempotent covers calling attachShadow() twice on the
// same host (not a confirmed real trigger, but a cheap, obviously-correct
// guard against a double call silently orphaning the first shadow tree) —
// returns the SAME ShadowRoot rather than throwing or replacing it.
func TestAttachShadowIsIdempotent(t *testing.T) {
	_, logs, _ := runJS(t, page(`
		var el = document.getElementById('d');
		var s1 = el.attachShadow({mode: 'open'});
		var s2 = el.attachShadow({mode: 'open'});
		console.log('same=' + (s1 === s2));
		console.log('hostSame=' + (s1.host === el));
	`))
	mustHave(t, logs, "same=true", "hostSame=true")
}
