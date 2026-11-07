// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
	"github.com/tetratelabs/wazero/api"

	"github.com/go-webengine/engine/dom"
)

// wasmFixtureScript reads js/testdata/add.wasm (see gen_add_wasm.go's own
// doc comment for the module's WAT-equivalent source) and renders it as a
// JS `new Uint8Array([...])` literal, byte values spelled out in decimal —
// deliberately NOT atob()+Uint8Array.from(string, mapFn), which sidesteps
// any question about that combination's own behaviour and keeps this test
// focused solely on the WebAssembly binding it exists to exercise.
func wasmFixtureScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/add.wasm")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	parts := make([]string, len(data))
	for i, b := range data {
		parts[i] = strconv.Itoa(int(b))
	}
	return "var bytes = new Uint8Array([" + strings.Join(parts, ",") + "]);"
}

// TestWebAssemblyInstantiateRoundTrip is the confirmed real need (requested
// directly, not corpus-found): go-webengine ran a page's JavaScript but
// could not run a page's own WebAssembly at all — the global was absent
// outright. This exercises the full round trip the fixture was built for:
// an i32 host import called FROM wasm (ToJSValue direction), an i32
// exported function called BY script (ToWebAssemblyValue direction), an f64
// exported function (the other numeric type most real compute modules use),
// and the module's own exported linear memory.
func TestWebAssemblyInstantiateRoundTrip(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		WebAssembly.instantiate(bytes, {env: {host_offset: function(x) { return x + 1000; }}}).then(function(result) {
			var instance = result.instance;
			console.log('isModule=' + (result.module !== undefined));
			console.log('add=' + instance.exports.add(2, 3));
			console.log('addf=' + instance.exports.addf(1.5, 2.25));
			var mem = new Uint8Array(instance.exports.memory.buffer, 0, 10);
			var s = '';
			for (var i = 0; i < 10; i++) { s += String.fromCharCode(mem[i]); }
			console.log('mem=' + s);
		}, function(err) {
			console.log('unexpected-rejection=' + err);
		});
	</script></body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs, "isModule=true", "add=1005", "addf=3.75", "mem=hello wasm")
}

// TestWebAssemblyValidate covers validate() on both a genuine module and
// garbage bytes.
func TestWebAssemblyValidate(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		console.log('valid=' + WebAssembly.validate(bytes));
		console.log('invalid=' + WebAssembly.validate(new Uint8Array([1, 2, 3])));
	</script></body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs, "valid=true", "invalid=false")
}

// TestWebAssemblyCompileThenInstantiate covers the two-step compile()-then-
// instantiate(module) form (as opposed to the one-step instantiate(bytes)
// form TestWebAssemblyInstantiateRoundTrip already covers), proving a
// WebAssembly.Module object round-trips correctly through
// wasmModuleOf/wrapWasmModule's own identity map.
func TestWebAssemblyCompileThenInstantiate(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		WebAssembly.compile(bytes).then(function(mod) {
			return WebAssembly.instantiate(mod, {env: {host_offset: function(x) { return x; }}});
		}).then(function(instance) {
			console.log('add=' + instance.exports.add(10, 20));
		});
	</script></body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs, "add=30")
}

// TestWebAssemblyCompileRejectsGarbage covers compile()'s own CompileError
// rejection path for bytes that are not a valid module.
func TestWebAssemblyCompileRejectsGarbage(t *testing.T) {
	html := `<html><body><script>
		WebAssembly.compile(new Uint8Array([9, 9, 9, 9])).then(function() {
			console.log('should-not-resolve');
		}, function(err) {
			console.log('rejected=' + err.name);
		});
	</script></body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs, "rejected=CompileError")
	if has(logs, "should-not-resolve") {
		t.Error("compile() resolved on garbage bytes")
	}
}

// TestWebAssemblyInstantiateMissingImportIsLinkError covers instantiate()'s
// own LinkError when the importObject doesn't supply a function the module
// actually imports — the honest "this engine doesn't model WASI, or the
// page's own importObject is incomplete" outcome this file's own top doc
// comment discloses, not a silent wrong answer.
func TestWebAssemblyInstantiateMissingImportIsLinkError(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		WebAssembly.instantiate(bytes, {}).then(function() {
			console.log('should-not-resolve');
		}, function(err) {
			console.log('rejected=' + err.name);
		});
	</script></body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs, "rejected=LinkError")
	if has(logs, "should-not-resolve") {
		t.Error("instantiate() resolved despite a missing import")
	}
}

// TestToWasmValueI64RequiresBigInt covers ToWebAssemblyValue's own i64 rule
// directly at the unit responsible for it: a plain Number is NOT implicitly
// convertible to a BigInt (unlike i32/f32/f64, which all accept a plain
// Number via ToInt32/ToNumber), so an i64 parameter given a bare number must
// throw TypeError — the fixture module has no i64 export to exercise this
// through a real call, so this goes straight at the conversion function.
func TestToWasmValueI64RequiresBigInt(t *testing.T) {
	b := &binder{vm: newTestVM(t)}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic (TypeError) for a plain Number i64 argument, got none")
		}
		obj, ok := r.(*goja.Object)
		if !ok {
			t.Fatalf("panic value = %T, want *goja.Object (a TypeError)", r)
		}
		if name := obj.Get("name"); name == nil || name.String() != "TypeError" {
			t.Errorf("panic .name = %v, want TypeError", name)
		}
	}()
	b.toWasmValue(b.vm.ToValue(42), api.ValueTypeI64)
}

// TestToWasmValueI64AcceptsBigInt confirms the positive case: a genuine
// BigInt converts correctly, including a negative value (ToBigInt64's own
// two's-complement wraparound, wrapBigIntTo64).
func TestToWasmValueI64AcceptsBigInt(t *testing.T) {
	b := &binder{vm: newTestVM(t)}
	bi, err := b.vm.RunString("-5n")
	if err != nil {
		t.Fatal(err)
	}
	got := b.toWasmValue(bi, api.ValueTypeI64)
	if int64(got) != -5 {
		t.Errorf("toWasmValue(-5n, i64) = %d, want -5", int64(got))
	}
}

// newTestVM returns a bare goja runtime with just enough installed
// (TypeError/BigInt are goja builtins, no DOM binding needed) for
// toWasmValue's own unit tests above.
func newTestVM(t *testing.T) *goja.Runtime {
	t.Helper()
	return goja.New()
}

// TestMaxWasmModuleBytesResolution covers Options.MaxWasmModuleBytes'
// own "zero selects the default" resolution (go-wasmcontainers
// WebGPU-readiness roadmap, milestone 1): a page needing a larger real
// module (Go-compiled WebAssembly binaries commonly reach into the tens to
// low hundreds of MiB) can raise it; a page that never sets it keeps
// today's 32 MiB behaviour unchanged.
func TestMaxWasmModuleBytesResolution(t *testing.T) {
	cases := []struct {
		name string
		opt  int64
		want int64
	}{
		{"unset", 0, defaultMaxWasmModuleBytes},
		{"negative treated as unset", -1, defaultMaxWasmModuleBytes},
		{"explicit value kept exactly", 300 << 20, 300 << 20},
	}
	for _, c := range cases {
		b := &binder{opt: Options{MaxWasmModuleBytes: c.opt}}
		if got := b.maxWasmModuleBytes(); got != c.want {
			t.Errorf("%s: maxWasmModuleBytes() = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestMaxWasmMemoryPagesResolution covers Options.MaxWasmMemoryBytes'
// byte-to-page conversion: zero selects the default, a value already a
// whole number of pages converts exactly, a value that ISN'T rounds UP
// (a cap must cover at least the bytes asked for, never less), and a
// value past the wasm32 address-space ceiling is clamped rather than
// handed to wazero's own WithMemoryLimitPages, which panics past it.
func TestMaxWasmMemoryPagesResolution(t *testing.T) {
	const page = 64 << 10
	cases := []struct {
		name string
		opt  int64
		want uint32
	}{
		{"unset selects default", 0, defaultMaxWasmMemoryBytes / page},
		{"exact multiple of the page size", 10 * page, 10},
		{"rounds up a non-multiple", 10*page + 1, 11},
		{"tiny positive value floors to one page", 1, 1},
		{"past the wasm32 ceiling is clamped", 1 << 40, maxWasmMemoryPages},
		{"exactly the ceiling is not clamped down", maxWasmMemoryPages * page, maxWasmMemoryPages},
	}
	for _, c := range cases {
		b := &binder{opt: Options{MaxWasmMemoryBytes: c.opt}}
		if got := b.maxWasmMemoryPages(); got != c.want {
			t.Errorf("%s: maxWasmMemoryPages() = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestWasmModuleSizeQuotaEnforcedEndToEnd proves the configured quota is
// actually consulted by the real compile() path, not just by the resolver
// function in isolation: the same real fixture TestWebAssemblyValidate
// accepts under default options is rejected once MaxWasmModuleBytes is
// configured smaller than the fixture's own 123 bytes.
func TestWasmModuleSizeQuotaEnforcedEndToEnd(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		console.log('valid=' + WebAssembly.validate(bytes));
	</script></body></html>`
	root, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	Run(root, Options{
		PageURL:            testURL,
		UserAgent:          "TestUA/1.0",
		Client:             http.DefaultClient,
		Timeout:            3 * time.Second,
		Log:                func(s string) { logs = append(logs, s) },
		MaxWasmModuleBytes: 10, // well under the 123-byte real fixture
	})
	mustHave(t, logs, "valid=false")
}

// TestInstantiateStreamingAndCompileStreaming is the confirmed real need
// (go-wasmcontainers WebGPU-readiness roadmap, milestone 1):
// instantiateStreaming/compileStreaming against a REAL fetch()-returned
// Response, the overwhelmingly common real shape
// (`instantiateStreaming(fetch(url))`), not the in-memory BufferSource
// literal TestWebAssemblyInstantiateRoundTrip already covers. Exercises
// both the direct-Response and promise-of-Response argument forms.
func TestInstantiateStreamingAndCompileStreaming(t *testing.T) {
	srv := netServer()
	defer srv.Close()
	logs, _ := runNet(t, srv, http.DefaultClient, `
		WebAssembly.compileStreaming(fetch('/add.wasm')).then(function(mod) {
			console.log('compileStreaming-ok=' + (mod !== undefined));
		}, function(err) { console.log('compileStreaming-rejected=' + err); });
		WebAssembly.instantiateStreaming(fetch('/add.wasm'), {env: {host_offset: function(x) { return x + 1000; }}}).then(function(result) {
			console.log('instantiateStreaming-add=' + result.instance.exports.add(2, 3));
		}, function(err) { console.log('instantiateStreaming-rejected=' + err); });
	`)
	mustHave(t, logs, "compileStreaming-ok=true", "instantiateStreaming-add=1005")
}

// TestStreamingWasmBytesRejectsVariousShapes covers every error path
// streamingWasmBytes/awaitSettled fall through, not just the "plain
// non-Response" one: a genuinely rejected promise (the one this engine's
// OWN fetch() never produces — see awaitSettled's own doc comment — but a
// caller can construct directly), a promise resolving to a non-object, an
// object with no arrayBuffer() method at all, and a bare non-promise,
// non-Response value. All must reject with a TypeError, never panic.
func TestStreamingWasmBytesRejectsVariousShapes(t *testing.T) {
	html := `<html><body><script>
		function check(name, source) {
			WebAssembly.instantiateStreaming(source).then(function() {
				console.log(name + '=should-not-resolve');
			}, function(err) { console.log(name + '=rejected:' + err.name); });
		}
		check('plainValue', 42);
		check('rejectedPromise', Promise.reject(new Error('boom')));
		check('resolvedToNonObject', Promise.resolve(42));
		check('objectWithoutArrayBuffer', Promise.resolve({}));
		// The spec's OTHER overload: a direct Response-shaped object, not
		// wrapped in a promise at all (exercises awaitSettled's own
		// not-a-Promise passthrough, not just its Promise-handling path).
		check('directObjectNoArrayBuffer', {});
		check('arrayBufferMethodThrows', {arrayBuffer: function(){ throw new Error('x'); }});
		check('arrayBufferReturnsRejected', {arrayBuffer: function(){ return Promise.reject(new Error('y')); }});
	</script></body></html>`
	_, logs, _ := runJS(t, html)
	mustHave(t, logs,
		"plainValue=rejected:TypeError",
		"rejectedPromise=rejected:TypeError",
		"resolvedToNonObject=rejected:TypeError",
		"objectWithoutArrayBuffer=rejected:TypeError",
		"directObjectNoArrayBuffer=rejected:TypeError",
		"arrayBufferMethodThrows=rejected:TypeError",
		"arrayBufferReturnsRejected=rejected:TypeError")
}

// TestWasmMemoryNonSharedBasics covers the standalone `new
// WebAssembly.Memory(...)` constructor's non-shared path: no
// cross-origin-isolation needed, .buffer is a real, growable ArrayBuffer,
// and grow() both returns the previous page count and enforces `maximum`.
func TestWasmMemoryNonSharedBasics(t *testing.T) {
	_, logs, res := runJS(t, page(`
		var mem = new WebAssembly.Memory({initial: 1, maximum: 2});
		console.log('tag=' + Object.prototype.toString.call(mem));
		console.log('byteLength=' + mem.buffer.byteLength);
		var view = new Uint8Array(mem.buffer);
		view[0] = 42;
		console.log('prev=' + mem.grow(1));
		console.log('grownByteLength=' + mem.buffer.byteLength);
		try {
			mem.grow(1);
			console.log('overgrow=did-not-throw');
		} catch (e) {
			console.log('overgrow=' + e.name);
		}
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"tag=[object WebAssembly.Memory]",
		"byteLength=65536",
		"prev=1",
		"grownByteLength=131072",
		"overgrow=RangeError",
	)
}

// TestWasmMemorySharedRequiresMaximumAndCrossOriginIsolation covers the two
// preconditions a shared Memory's descriptor must satisfy — both throw
// TypeError, matching a real browser: no maximum at all (a growable
// SharedArrayBuffer's full capacity must be known up front), and shared
// without cross-origin isolation (the SAME gate SharedArrayBuffer/Atomics
// are already behind — see atomics_test.go).
func TestWasmMemorySharedRequiresMaximumAndCrossOriginIsolation(t *testing.T) {
	_, logs, res := runJS(t, page(`
		try {
			new WebAssembly.Memory({initial: 1, shared: true});
			console.log('noMax=did-not-throw');
		} catch (e) { console.log('noMax=' + e.name); }
		try {
			new WebAssembly.Memory({initial: 1, maximum: 2, shared: true});
			console.log('noCOI=did-not-throw');
		} catch (e) { console.log('noCOI=' + e.name); }
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "noMax=TypeError", "noCOI=TypeError")
}

// TestWasmMemorySharedBasics covers the shared path under cross-origin
// isolation: .buffer reports as a genuine SharedArrayBuffer (confirmed
// needed directly — go-wasmcontainers' own code branches on `instanceof
// SharedArrayBuffer`, exactly as atomics_test.go's
// TestSharedArrayBufferHasItsOwnIdentity already proves for the bare
// constructor), and grow is visible through a FRESH `.buffer` read without
// losing previously-written bytes — the in-place, pre-allocated-capacity
// re-slice sharedBuffer.grow implements, not a realloc-and-copy that would
// break sharing with another agent.
func TestWasmMemorySharedBasics(t *testing.T) {
	_, logs, res := runJSCOI(t, page(`
		var mem = new WebAssembly.Memory({initial: 1, maximum: 2, shared: true});
		console.log('tag=' + Object.prototype.toString.call(mem));
		console.log('isSAB=' + (mem.buffer instanceof SharedArrayBuffer));
		console.log('byteLength=' + mem.buffer.byteLength);
		var view = new Int32Array(mem.buffer);
		view[0] = 777;
		console.log('prev=' + mem.grow(1));
		var grownBuf = mem.buffer; // MUST re-read after grow (documented limitation)
		console.log('grownByteLength=' + grownBuf.byteLength);
		var grownView = new Int32Array(grownBuf);
		console.log('preservedAfterGrow=' + grownView[0]);
		console.log('sameBacking=' + (Atomics.load(grownView, 0) === 777));
	`))
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"tag=[object WebAssembly.Memory]",
		"isSAB=true",
		"byteLength=65536",
		"prev=1",
		"grownByteLength=131072",
		"preservedAfterGrow=777",
		"sameBacking=true",
	)
}
