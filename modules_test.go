// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-webengine/engine/dom"
)

// findByID returns the first element whose id attribute equals id.
func findByID(root *dom.Node, id string) *dom.Node {
	var found *dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if found != nil {
			return
		}
		if n.Type == dom.Element && n.ID() == id {
			found = n
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return found
}

// moduleFixtureServer serves an HTML page whose ES-module entry imports a second
// local module and mutates the DOM, plus the two module files. It is fully
// offline (localhost) and deterministic.
func moduleFixtureServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>mod</title></head>
<body><div id="app">PLACEHOLDER</div>
<script type="module" src="/entry.js"></script>
</body></html>`))
	})
	mux.HandleFunc("/entry.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		// Static import of a second module + a named export consumed here.
		_, _ = w.Write([]byte(`import { greeting } from "./lib.js";
document.getElementById("app").textContent = greeting + " from module";`))
	})
	mux.HandleFunc("/lib.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(`export const greeting = "HELLO";`))
	})
	return httptest.NewServer(mux)
}

// TestModuleGraphExecutesAndMutatesDOM proves the esbuild transpile path runs an
// ES-module graph end-to-end: the entry module imports a second module and its
// DOM mutation is present in the rendered document.
func TestModuleGraphExecutesAndMutatesDOM(t *testing.T) {
	srv := moduleFixtureServer()
	defer srv.Close()

	e := New()
	e.Client = srv.Client() // reach the httptest server

	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = e.RenderDocument(context.Background(), doc, image.Rect(0, 0, 400, 300))
	if err != nil {
		t.Fatal(err)
	}

	app := findByID(doc.Root, "app")
	if app == nil {
		t.Fatal("no #app node")
	}
	got := strings.TrimSpace(dom.TextContent(app))
	if got != "HELLO from module" {
		t.Fatalf("module DOM mutation not applied: #app = %q, want %q", got, "HELLO from module")
	}
}

// TestInlineModuleExecutes proves an INLINE `<script type=module>` (no src) that
// imports a local module is bundled and run, mutating the DOM.
func TestInlineModuleExecutes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div id="app">x</div>` +
			`<script type="module">import { g } from "/lib.js"; document.getElementById("app").textContent = g;</script>` +
			`</body></html>`))
	})
	mux.HandleFunc("/lib.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(`export const g = "INLINE-OK";`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.RenderDocument(context.Background(), doc, image.Rect(0, 0, 300, 200)); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(dom.TextContent(findByID(doc.Root, "app"))); got != "INLINE-OK" {
		t.Fatalf("inline module did not run: #app = %q", got)
	}
}

// TestModuleFetch404Fallback covers the fetch-failure path: an import of a
// missing (404) module fails the bundle gracefully, leaving the DOM untouched.
func TestModuleFetch404Fallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div id="app">keep</div>` +
			`<script type="module" src="/entry.js"></script></body></html>`))
	})
	mux.HandleFunc("/entry.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(`import "/does-not-exist.js";`))
	})
	// /does-not-exist.js is intentionally unhandled -> 404.
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	_, stats, ok := e.bundleModuleScripts(context.Background(), doc)
	if ok {
		t.Fatal("expected bundle to fail on a 404 import")
	}
	if stats.entries == 0 {
		t.Fatal("expected entries>0")
	}
	if _, _, err := e.RenderDocument(context.Background(), doc, image.Rect(0, 0, 200, 100)); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(dom.TextContent(findByID(doc.Root, "app"))); got != "keep" {
		t.Fatalf("failed bundle should leave DOM untouched: #app = %q", got)
	}
}

// TestModuleGraphDisabledJS confirms the module path is gated by DisableJS: with
// JS off, the module graph is not bundled and the DOM is untouched.
func TestModuleGraphDisabledJS(t *testing.T) {
	srv := moduleFixtureServer()
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	e.DisableJS = true

	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = e.RenderDocument(context.Background(), doc, image.Rect(0, 0, 400, 300)); err != nil {
		t.Fatal(err)
	}
	app := findByID(doc.Root, "app")
	if got := strings.TrimSpace(dom.TextContent(app)); got != "PLACEHOLDER" {
		t.Fatalf("DisableJS should leave the module DOM untouched: #app = %q", got)
	}
}

// TestClassicScriptUnaffectedByModulePath confirms a page with only a classic
// script behaves identically (no module bundling triggered).
func TestClassicScriptUnaffectedByModulePath(t *testing.T) {
	src := `<html><head><title>t</title></head><body><div id="app">before</div>` +
		`<script>document.getElementById('app').textContent='after';</script></body></html>`
	_, _, err := New().RenderHTML(context.Background(), src, "https://example.com/", image.Rect(0, 0, 200, 100))
	if err != nil {
		t.Fatal(err)
	}
	// Re-parse+render to read the mutated node deterministically.
	doc, _ := parseDoc(src)
	if _, _, err := New().RenderDocument(context.Background(), doc, image.Rect(0, 0, 200, 100)); err != nil {
		t.Fatal(err)
	}
	app := findByID(doc.Root, "app")
	if got := strings.TrimSpace(dom.TextContent(app)); got != "after" {
		t.Fatalf("classic script path changed: #app = %q, want %q", got, "after")
	}
}

// TestNoModuleScriptsSkipsBundler proves bundleModuleScripts is a no-op (ok=false,
// zero entries) for a page with no module scripts — the non-module fast path.
func TestNoModuleScriptsSkipsBundler(t *testing.T) {
	doc, _ := parseDoc(`<html><body><script>var x=1;</script></body></html>`)
	_, stats, ok := New().bundleModuleScripts(context.Background(), doc)
	if ok || stats.entries != 0 {
		t.Fatalf("expected no-op for non-module page: ok=%v entries=%d", ok, stats.entries)
	}
}

// TestResolveModuleSpecifier covers the resolver's branches.
func TestResolveModuleSpecifier(t *testing.T) {
	const page = "https://ex.com/app/index.html"
	cases := []struct {
		importer, spec, want string
		ok                   bool
	}{
		{"", "https://cdn.ex/x.js", "https://cdn.ex/x.js", true},          // absolute
		{"", "/packs/a.js", "https://ex.com/packs/a.js", true},            // root-relative vs page
		{"https://ex.com/packs/a.js", "./b.js", "https://ex.com/packs/b.js", true}, // relative vs importer
		{"https://ex.com/packs/sub/a.js", "../c.js", "https://ex.com/packs/c.js", true},
		{"", "react", "", false},   // bare specifier
		{"", "", "", false},        // empty
	}
	for _, c := range cases {
		got, ok := resolveModuleSpecifier(page, c.importer, c.spec)
		if ok != c.ok || got != c.want {
			t.Errorf("resolveModuleSpecifier(%q,%q,%q) = %q,%v want %q,%v",
				page, c.importer, c.spec, got, ok, c.want, c.ok)
		}
	}
}

// TestModuleBundleFailureFallback proves a module page whose import graph cannot
// be resolved (bare specifier) fails the bundle gracefully: no panic, DOM
// unchanged, ok=false with entries>0.
func TestModuleBundleFailureFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><div id="app">keep</div>` +
			`<script type="module" src="/e.js"></script></body></html>`))
	})
	mux.HandleFunc("/e.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		_, _ = w.Write([]byte(`import x from "totally-bare-specifier"; x();`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e := New()
	e.Client = srv.Client()
	doc, err := e.Fetch(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	_, stats, ok := e.bundleModuleScripts(context.Background(), doc)
	if ok {
		t.Fatal("expected bundle to fail on a bare specifier")
	}
	if stats.entries == 0 {
		t.Fatal("expected entries>0 for a module page")
	}
	// Full render must still succeed and leave the DOM untouched.
	if _, _, err := e.RenderDocument(context.Background(), doc, image.Rect(0, 0, 200, 100)); err != nil {
		t.Fatal(err)
	}
	app := findByID(doc.Root, "app")
	if got := strings.TrimSpace(dom.TextContent(app)); got != "keep" {
		t.Fatalf("failed bundle should leave DOM untouched: #app = %q", got)
	}
}
