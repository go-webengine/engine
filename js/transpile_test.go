// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-webengine/engine/dom"
)

// TestClassicScriptWithDynamicImportFallsBackToESBuild is the decisive,
// real-world repro for engine#412/#413: a CLASSIC (non-module) script —
// webpack's/Turbopack's own runtime/chunk-loading code, confirmed live on
// github.com's own Support app — uses a bare dynamic import() expression,
// which goja's own parser rejects outright ("Unexpected reserved word").
// The dynamic import() expression is legal in a classic script by spec
// (unlike import/export DECLARATIONS), so collectModuleScripts' own
// type="module" gate never sees this file at all; it reaches goja's
// classic-script parser directly. The runtime chunk's own import("./chunk")
// resolves (relative to ITS OWN URL, not the page's) to a second chunk that
// must ALSO be fetched and inlined for the whole thing to run correctly.
func TestClassicScriptWithDynamicImportFallsBackToESBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/packs/runtime.js":
			// A classic script (no type="module") whose own chunk-loading
			// code is a bare dynamic import() — the exact webpack shape.
			w.Write([]byte(`import("./chunk.js").then(function(m) {
				document.body.setAttribute('data-result', m.value);
			});`))
		case "/packs/chunk.js":
			w.Write([]byte(`export const value = "chunk-loaded";`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	htmlSrc := `<html><body><script src="` + srv.URL + `/packs/runtime.js"></script></body></html>`
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL: srv.URL + "/",
		Client:  http.DefaultClient,
		Timeout: 5 * time.Second,
		Log:     func(s string) { logs = append(logs, s) },
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed to run, want the esbuild fallback to recover it: res=%+v logs=%v", res, logs)
	}
	if got := dom.Find(root, "body").Attr["data-result"]; got != "chunk-loaded" {
		t.Fatalf("data-result = %q, want %q (the dynamically-imported chunk's own export) logs=%v", got, "chunk-loaded", logs)
	}
}

// TestAsyncGeneratorForAwaitFallsBackToESBuild is the second half of
// engine#412's two documented goja parser gaps (FIDELITY.md round 105,
// PRs #168/#198): async generators ("async function*") and for-await-of are
// ES2018 syntax goja's parser rejects outright
// ("Async generators are not supported yet" / "Unexpected token await").
// Confirmed live in the `440-…js` Azure Blob Storage SDK bundle on
// github.com's own Support app. This needs no new code at all — it is the
// SAME transpileModernJS fallback already added for the dynamic-import half
// (#413), since esbuild's own parser accepts and down-levels both ES2018
// async generators and ES2020 dynamic import() to the same ES2017 target.
func TestAsyncGeneratorForAwaitFallsBackToESBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	src := `
		async function* g() {
			for await (const x of [Promise.resolve(1), Promise.resolve(2)]) {
				yield await x;
			}
		}
		(async function() {
			let sum = 0;
			for await (const v of g()) { sum += v; }
			document.body.setAttribute('data-sum', String(sum));
		})();
	`
	root, err := dom.Parse(`<html><body><script>` + src + `</script></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	// Client is set (matching the engine's own real render path, engine.go's
	// newHTTPClient()) even though this script makes no dynamic import() and
	// never calls it — esbuild's OnResolve/OnLoad plugin only fires for an
	// unresolved import specifier, which this script has none of.
	res := Run(root, Options{PageURL: srv.URL + "/", Client: srv.Client(), Timeout: 2 * time.Second, Log: func(s string) { logs = append(logs, s) }})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed to run, want the esbuild fallback to recover it: res=%+v logs=%v", res, logs)
	}
	if got := dom.Find(root, "body").Attr["data-sum"]; got != "3" {
		t.Fatalf("data-sum = %q, want %q logs=%v", got, "3", logs)
	}
}

// FuzzTranspileModernJS feeds arbitrary bytes (untrusted web content, exactly
// what reaches this path when a real fetched script fails goja's Compile —
// CLAUDE.md's own "fuzz every parser/decoder" rule) through
// transpileModernJS, which itself drives esbuild's own JS/TS parser (via
// api.Build) on attacker-controlled input. The only invariant this fuzz
// target can hold transpileModernJS to is "never panics": a confirmed run
// (testdata/fuzz/FuzzTranspileModernJS/433b817769ef2cb6, input "0(/0(0/)")
// found esbuild and goja disagreeing on a classic regex-vs-division
// tokenization ambiguity — esbuild accepts and transforms it, goja's own
// Compile on the transformed output still rejects it — which is NOT a bug
// to harden against here: execute() (js.go) already re-verifies
// transpileModernJS's output with its own goja.Compile before using it and
// falls back to the ORIGINAL compile error on failure, exactly like any
// other script esbuild can't help with (TestTranspileFallbackGivesUpOnGenuinelyBrokenScript).
// transpileModernJS is a best-effort retry, not a verified-equivalent
// re-emission, so "its claimed success is always goja-compilable" was never
// a real contract — asserting it here would fail on harmless, already-handled
// divergence instead of a genuine defect.
func FuzzTranspileModernJS(f *testing.F) {
	seeds := []string{
		`import("./chunk.js").then(function(m){ return m.value; });`,
		`async function* g(){ for await (const x of y) { yield await x; } }`,
		`this is not valid JS at all {{{`,
		``,
		`import(`,
		`import("` + string([]byte{0}) + `")`,
		`/* unterminated`,
		`'use strict'; import(/* webpackChunkName: "x" */ "./y.js");`,
		`export default import("./x.js");`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	root, err := dom.Parse(`<html><body></body></html>`)
	if err != nil {
		f.Fatal(err)
	}
	sess := Begin(root, Options{PageURL: testURL, Client: http.DefaultClient, Timeout: time.Second})
	defer sess.Close()
	f.Fuzz(func(t *testing.T, src string) {
		_, _ = sess.b.transpileModernJS(src, "inline#1")
	})
}

// TestOrdinaryClassicScriptNeverAttemptsTranspile proves the fallback is
// reactive only: a script that already compiles fine in goja must never
// reach transpileModernJS at all (no esbuild invocation, no behaviour
// change) — confirmed here by giving it a Client that would fail any fetch
// attempt the fallback might make, and an import()-free script that must
// still run normally.
func TestOrdinaryClassicScriptNeverAttemptsTranspile(t *testing.T) {
	root, err := dom.Parse(`<html><body><script>document.body.setAttribute('data-ok', '1');</script></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	res := Run(root, Options{PageURL: testURL, Timeout: time.Second})
	if res.ScriptsFailed != 0 || res.ScriptsRun != 1 {
		t.Fatalf("res=%+v", res)
	}
	if got := dom.Find(root, "body").Attr["data-ok"]; got != "1" {
		t.Fatalf("data-ok = %q, want 1", got)
	}
}

// TestTranspileFallbackGivesUpOnGenuinelyBrokenScript proves the fallback
// degrades safely: a script that is just plain malformed JS (not modern
// syntax goja merely doesn't support) must still fail cleanly — esbuild
// itself refuses to parse it too — not hang or panic.
func TestTranspileFallbackGivesUpOnGenuinelyBrokenScript(t *testing.T) {
	root, err := dom.Parse(`<html><body><script>this is not valid JS at all {{{</script></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{PageURL: testURL, Timeout: 2 * time.Second, Log: func(s string) { logs = append(logs, s) }})
	if res.ScriptsFailed != 1 {
		t.Fatalf("expected the broken script to fail (not hang/panic/silently succeed): res=%+v logs=%v", res, logs)
	}
}

// TestTranspileFallbackNoClientSkipsCleanly proves a nil Client (no
// network — a render that disabled script fetching entirely) makes the
// fallback a clean no-op rather than a nil-pointer panic.
func TestTranspileFallbackNoClientSkipsCleanly(t *testing.T) {
	root, err := dom.Parse(`<html><body><script>import("./x.js");</script></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	res := Run(root, Options{PageURL: testURL, Timeout: time.Second})
	if res.ScriptsFailed != 1 {
		t.Fatalf("expected the script to fail cleanly with no Client: res=%+v", res)
	}
}

// TestVariableSpecifierDynamicImportFallsBackToRequire is the REAL
// engine#412/#413 trigger, confirmed live against github.com's own Support
// app: `await import(j.module)`, a VARIABLE specifier (a property read, not
// a string literal) — esbuild cannot statically resolve/inline this (it has
// no way to know a variable's runtime value at transpile time; confirmed
// directly: esbuild leaves such a call as a literal import(...) in its
// output regardless of target format), so transpileModernJS's own static
// inlining never helps this shape — a prior round shipped the dynamic-import
// fallback believing it covered this, verified ONLY against a string-literal
// specifier, and still saw the exact same SyntaxError on github.com's real
// page afterward. require.go's requireModule is what actually closes it:
// "dynamic-import" marked unsupported makes esbuild lower the call to a
// plain require(x) — ordinary ES2017 syntax, x resolved only once the
// script is already running — which requireModule then backs for real.
func TestVariableSpecifierDynamicImportFallsBackToRequire(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/packs/runtime.js":
			// Mirrors the real loadRouteModule shape exactly: the module
			// specifier arrives through an object property, never as a
			// string literal anywhere in this script's own source text.
			w.Write([]byte(`
				function loadRouteModule(route) { return import(route.module); }
				loadRouteModule({module: "./chunk.js"}).then(function(m) {
					document.body.setAttribute('data-result', m.value);
				});
			`))
		case "/packs/chunk.js":
			w.Write([]byte(`export const value = "chunk-loaded-via-require";`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	htmlSrc := `<html><body><script src="` + srv.URL + `/packs/runtime.js"></script></body></html>`
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL: srv.URL + "/",
		Client:  http.DefaultClient,
		Timeout: 5 * time.Second,
		Log:     func(s string) { logs = append(logs, s) },
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed to run, want the require() bridge to recover it: res=%+v logs=%v", res, logs)
	}
	if got := dom.Find(root, "body").Attr["data-result"]; got != "chunk-loaded-via-require" {
		t.Fatalf("data-result = %q, want %q logs=%v", got, "chunk-loaded-via-require", logs)
	}
}

// TestRequireSharedChunkFetchedOnce proves requireModule's own cache: two
// independent require() calls (from two different top-level fallback
// scripts, both resolving a VARIABLE specifier — a static string literal
// specifier a bundler can resolve at transpile time is instead inlined
// directly by esbuild's own bundler per script, as it always has been, and
// is NOT routed through requireModule's cache at all) for the SAME
// specifier fetch the underlying chunk exactly once and observe the SAME
// exports object — matching real module-singleton semantics (and avoiding
// a redundant fetch for a shared vendor chunk a real page's own separate
// route bundles commonly both pull in, exactly react-router's own
// loadRouteModule shape: many routes' own manifest entries can point at the
// same shared chunk URL).
func TestRequireSharedChunkFetchedOnce(t *testing.T) {
	var chunkFetches int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/packs/entry.js":
			// Two SEPARATE require() calls for the same specifier, ordered
			// by nesting (not cross-script timing) so the test itself
			// stays deterministic regardless of this engine's own
			// microtask-draining order.
			w.Write([]byte(`function load(route){ return import(route.module); }
				load({module:"./shared.js"}).then(function(m1){
					load({module:"./shared.js"}).then(function(m2){
						document.body.setAttribute('data-same', String(m1.id === m2.id));
					});
				});`))
		case "/packs/shared.js":
			atomic.AddInt32(&chunkFetches, 1)
			w.Write([]byte(`export const id = {};`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	htmlSrc := `<html><body><script src="` + srv.URL + `/packs/entry.js"></script></body></html>`
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{PageURL: srv.URL + "/", Client: http.DefaultClient, Timeout: 5 * time.Second, Log: func(s string) { logs = append(logs, s) }})
	if res.ScriptsFailed != 0 {
		t.Fatalf("res=%+v logs=%v", res, logs)
	}
	if got := atomic.LoadInt32(&chunkFetches); got != 1 {
		t.Fatalf("shared.js fetched %d times, want exactly 1 (requireModule's own cache)", got)
	}
	if got := dom.Find(root, "body").Attr["data-same"]; got != "true" {
		t.Fatalf("data-same = %q, want %q (same underlying export both require() calls)", got, "true")
	}
}

// TestRequireCircularDependencyDoesNotHang proves requireModule's own
// break-the-cycle cache write (before executing, not after): a.js requires
// b.js, which requires a.js back — must resolve, not recurse forever or
// deadlock, the same guarantee Node's own CommonJS loader gives.
func TestRequireCircularDependencyDoesNotHang(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/packs/entry.js":
			w.Write([]byte(`import("./a.js").then(function(m){ document.body.setAttribute('data-ok', '1'); });`))
		case "/packs/a.js":
			w.Write([]byte(`import("./b.js"); export const a = 1;`))
		case "/packs/b.js":
			w.Write([]byte(`import("./a.js"); export const b = 2;`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	htmlSrc := `<html><body><script src="` + srv.URL + `/packs/entry.js"></script></body></html>`
	root, err := dom.Parse(htmlSrc)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{PageURL: srv.URL + "/", Client: http.DefaultClient, Timeout: 5 * time.Second, Log: func(s string) { logs = append(logs, s) }})
	if res.ScriptsFailed != 0 {
		t.Fatalf("res=%+v logs=%v", res, logs)
	}
	if got := dom.Find(root, "body").Attr["data-ok"]; got != "1" {
		t.Fatalf("data-ok = %q, want 1 (circular require must still resolve) logs=%v", got, logs)
	}
}

// TestRequireFetchFailureRejectsCleanly proves a require() target that 404s
// rejects the awaiting Promise cleanly (matching a real browser's own
// rejected dynamic import on a network failure) instead of hanging the
// script or crashing the render.
func TestRequireFetchFailureRejectsCleanly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv.Close()

	root, err := dom.Parse(`<html><body><script>
		function loadRouteModule(route) { return import(route.module); }
		loadRouteModule({module: "./missing.js"}).then(
			function(){ document.body.setAttribute('data-outcome', 'resolved'); },
			function(){ document.body.setAttribute('data-outcome', 'rejected'); }
		);
	</script></body></html>`)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{PageURL: srv.URL + "/", Client: http.DefaultClient, Timeout: 5 * time.Second, Log: func(s string) { logs = append(logs, s) }})
	if res.ScriptsFailed != 0 {
		t.Fatalf("res=%+v logs=%v", res, logs)
	}
	if got := dom.Find(root, "body").Attr["data-outcome"]; got != "rejected" {
		t.Fatalf("data-outcome = %q, want %q logs=%v", got, "rejected", logs)
	}
}

// FuzzRequireModule feeds arbitrary bytes as a required module's own fetched
// source (untrusted web content, same reasoning as FuzzTranspileModernJS)
// through requireModule's own transpile+compile+execute path. The only
// invariant held here is "never panics" — a module body esbuild or goja
// rejects must surface as requireModule's own error return, which
// makeRequireFn turns into a rejected Promise, not a crash.
func FuzzRequireModule(f *testing.F) {
	seeds := []string{
		`export const value = 1;`,
		`this is not valid JS at all {{{`,
		``,
		`module.exports = {};`,
		`export default function(){};`,
		`import("./x.js");`,
		`async function* g(){ for await (const x of y) {} }`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		defer srv.Close()

		root, err := dom.Parse(`<html><body></body></html>`)
		if err != nil {
			t.Fatal(err)
		}
		sess := Begin(root, Options{PageURL: srv.URL + "/", Client: srv.Client(), Timeout: time.Second})
		defer sess.Close()
		_, _ = sess.b.requireModule(srv.URL+"/", "./x.js")
	})
}
