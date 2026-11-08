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

// uint8ArrayLiteral renders data as a JS `new Uint8Array([...])` literal
// assigned to varName — the same decimal-byte-list style wasmFixtureScript
// uses, factored out for a hand-built module's own bytes rather than a
// committed .wasm file.
func uint8ArrayLiteral(varName string, data []byte) string {
	parts := make([]string, len(data))
	for i, b := range data {
		parts[i] = strconv.Itoa(int(b))
	}
	return "var " + varName + " = new Uint8Array([" + strings.Join(parts, ",") + "]);"
}

// memoryOnlyModuleBytes hand-builds the smallest valid WASM module that
// declares and exports exactly one memory and nothing else — no functions
// needed to exercise moduleHasSharedMemory or the resulting
// instance.exports.memory wrapper, since this round's own confirmed need
// (go-wasmcontainers) is entirely about the Memory object itself, read and
// written through plain JS typed arrays. Encodes exactly the two sections
// a real module needs for this (Memory id=5, Export id=7), per the
// WebAssembly binary format and its threads-proposal limits extension
// (confirmed by reading wazero's own decoder, internal/wasm/binary/
// limits.go, directly — flag byte 0x03 = {min,max,shared}, 0x01 =
// {min,max}, not shared).
func memoryOnlyModuleBytes(min, max byte, shared bool) []byte {
	flag := byte(0x01)
	if shared {
		flag = 0x03
	}
	memSection := []byte{0x01, flag, min, max} // count=1, memtype
	exportSection := []byte{0x01, 0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00}
	out := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00} // \0asm + version 1
	out = append(out, 0x05, byte(len(memSection)))
	out = append(out, memSection...)
	out = append(out, 0x07, byte(len(exportSection)))
	out = append(out, exportSection...)
	return out
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

// TestModuleHasSharedMemoryDetection is a direct, JS-free unit test of
// moduleHasSharedMemory against hand-built module bytes — confirming it
// reads the EXACT flag byte wazero's own decoder does (0x02/0x03 shared,
// 0x00/0x01 not), rather than assuming the encoding from the spec text
// alone. Includes the REAL add.wasm fixture (confirmed, by
// TestWebAssemblyInstantiateRoundTrip, to declare a plain non-shared
// memory) and deliberately-truncated/empty inputs, which must report
// false rather than panic — this function runs on bytes CompileModule
// will separately validate, but must never be the one to crash on bad
// input.
func TestModuleHasSharedMemoryDetection(t *testing.T) {
	addWasm, err := os.ReadFile("testdata/add.wasm")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"shared with max", memoryOnlyModuleBytes(1, 2, true), true},
		{"not shared", memoryOnlyModuleBytes(1, 2, false), false},
		{"real add.wasm fixture (non-shared)", addWasm, false},
		{"empty", nil, false},
		{"only the header, no sections", []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}, false},
		{"header truncated", []byte{0x00, 0x61, 0x73}, false},
		{"memory section truncated mid-content", []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, 0x05, 0x04, 0x01, 0x03}, false},
	}
	for _, c := range cases {
		if got := moduleHasSharedMemory(c.data); got != c.want {
			t.Errorf("%s: moduleHasSharedMemory() = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWasmModuleDeclaredSharedMemoryEndToEnd covers go-wasmcontainers' own
// exact confirmed shape: a guest module DECLARES (never imports) its own
// memory with the threads-proposal shared flag set, and
// instance.exports.memory must come back as a real, SharedArrayBuffer-
// classed, growable Memory — not the plain-ArrayBuffer wrapping a
// non-shared module's own memory already gets.
func TestWasmModuleDeclaredSharedMemoryEndToEnd(t *testing.T) {
	html := `<html><body><script>
		` + uint8ArrayLiteral("bytes", memoryOnlyModuleBytes(1, 2, true)) + `
		WebAssembly.instantiate(bytes, {}).then(function(result) {
			var mem = result.instance.exports.memory;
			console.log('tag=' + Object.prototype.toString.call(mem));
			console.log('isSAB=' + (mem.buffer instanceof SharedArrayBuffer));
			console.log('byteLength=' + mem.buffer.byteLength);
			var view = new Int32Array(mem.buffer);
			view[0] = 999;
			console.log('prev=' + mem.grow(1));
			var grownView = new Int32Array(mem.buffer);
			console.log('grownByteLength=' + mem.buffer.byteLength);
			console.log('preserved=' + grownView[0]);
		}, function(err) { console.log('unexpected-rejection=' + err); });
	</script></body></html>`
	root, err := dom.Parse(html)
	if err != nil {
		t.Fatal(err)
	}
	var logs []string
	res := Run(root, Options{
		PageURL:             testURL,
		Client:              http.DefaultClient,
		Timeout:             3 * time.Second,
		Log:                 func(s string) { logs = append(logs, s) },
		CrossOriginIsolated: true,
	})
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"tag=[object WebAssembly.Memory]",
		"isSAB=true",
		"byteLength=65536",
		"prev=1",
		"grownByteLength=131072",
		"preserved=999",
	)
}

// TestWasmModuleSharedMemoryRequiresCrossOriginIsolation proves the gate
// wasmRT applies: the SAME shared-memory module instantiated WITHOUT
// CrossOriginIsolated fails — wazero's own decoder rejects the module's
// shared memory type outright once CoreFeaturesThreads is left disabled,
// surfacing through this binding's existing CompileError handling, the
// same denial shape a real browser gives rather than a more permissive
// shortcut.
func TestWasmModuleSharedMemoryRequiresCrossOriginIsolation(t *testing.T) {
	html := `<html><body><script>
		` + uint8ArrayLiteral("bytes", memoryOnlyModuleBytes(1, 2, true)) + `
		WebAssembly.instantiate(bytes, {}).then(function(result) {
			console.log('unexpected-resolution');
		}, function(err) { console.log('rejected:' + err.name); });
	</script></body></html>`
	_, logs, res := runJS(t, html)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "rejected:CompileError")
}

// TestWasmInstanceAndModuleInstanceofHold covers the real, confirmed need:
// Go's own wasm_exec.js (Go.run) does `if (!(instance instanceof
// WebAssembly.Instance)) throw ...` — before WebAssembly.Instance/.Module
// existed as real constructors with their own .prototype, that `instanceof`
// itself threw "Value is not an object: undefined" rather than evaluating
// to false, aborting the whole guest.
func TestWasmInstanceAndModuleInstanceofHold(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		WebAssembly.instantiate(bytes, {env: {host_offset: function(x) { return x + 1000; }}}).then(function(result) {
			console.log('oneStepInstance=' + (result.instance instanceof WebAssembly.Instance));
			console.log('oneStepModule=' + (result.module instanceof WebAssembly.Module));
			console.log('ctorName=' + WebAssembly.Instance.name + ',' + WebAssembly.Module.name);
			try {
				new WebAssembly.Module(bytes);
				console.log('syncModuleCtor=did-not-throw');
			} catch (e) { console.log('syncModuleCtor=' + e.name); }
			try {
				new WebAssembly.Instance(result.module, {});
				console.log('syncInstanceCtor=did-not-throw');
			} catch (e) { console.log('syncInstanceCtor=' + e.name); }
		}, function(err) { console.log('unexpected-rejection=' + err); });
	</script></body></html>`
	_, logs, res := runJS(t, html)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"oneStepInstance=true",
		"oneStepModule=true",
		"ctorName=Instance,Module",
		"syncModuleCtor=TypeError",
		"syncInstanceCtor=TypeError",
	)
}

// TestWasmMemoryInstanceofHolds covers WebAssembly.Memory's own identity —
// the SAME gap TestWasmInstanceAndModuleInstanceofHold already covers for
// Instance/Module, but Memory never got the matching treatment: every
// Memory wrapper this file builds (the standalone constructor, shared or
// not, and a module's own exported memory) was stamped with
// Symbol.toStringTag only, never a real prototype, so `instanceof
// WebAssembly.Memory` was always false — masked because every existing
// test checked Object.prototype.toString.call() instead. Confirmed needed
// directly: go-wasmcontainers' own runtime/browser/worker.js validates a
// guest's exported memory with exactly `!(memory instanceof
// WebAssembly.Memory)`, which always fired, reported as "the module
// exports no memory" even though instance.exports.memory was a real,
// present object.
func TestWasmMemoryInstanceofHolds(t *testing.T) {
	html := `<html><body><script>
		` + uint8ArrayLiteral("bytes", memoryOnlyModuleBytes(1, 2, false)) + `
		var nonShared = new WebAssembly.Memory({initial: 1});
		console.log('standaloneNonShared=' + (nonShared instanceof WebAssembly.Memory));
		console.log('ctorName=' + WebAssembly.Memory.name);
		WebAssembly.instantiate(bytes, {}).then(function(result) {
			console.log('moduleExported=' + (result.instance.exports.memory instanceof WebAssembly.Memory));
		}, function(err) { console.log('unexpected-rejection=' + err); });
	</script></body></html>`
	_, logs, res := runJS(t, html)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs,
		"standaloneNonShared=true",
		"ctorName=Memory",
		"moduleExported=true",
	)
}

// explodingImportModuleBytes hand-builds a module that imports env.explode
// () -> () and exports "run" () -> (), whose only instruction calls the
// import — the smallest shape that lets a host import's own thrown
// exception unwind through a real wasm call.
func explodingImportModuleBytes() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic + version
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type section: () -> ()
		0x02, 0x0f, 0x01, 0x03, 0x65, 0x6e, 0x76, 0x07, 0x65, 0x78, 0x70, 0x6c, 0x6f, 0x64, 0x65, 0x00, 0x00, // import env.explode func type0
		0x03, 0x02, 0x01, 0x00, // function section: 1 func, type0
		0x07, 0x07, 0x01, 0x03, 0x72, 0x75, 0x6e, 0x00, 0x01, // export "run" func idx1
		0x0a, 0x06, 0x01, 0x04, 0x00, 0x10, 0x00, 0x0b, // code: call func0 (explode); end
	}
}

// TestHostImportExceptionPropagatesUnchanged proves go-wasmcontainers' own
// confirmed need (engine#342 fallout): a host import that THROWS a JS
// exception must unwind the wasm frames and reach the exported function's
// own caller as the EXACT SAME value, not wrapped — per the WebAssembly JS
// API's own contract (only a genuine trap becomes a new RuntimeError).
// Their own real worker.js throws `new Exit(code)` from a host import when
// the guest calls proc_exit, and catches it back with `instanceof Exit`
// once `start()` rethrows it — before this fix, wazero's own panic
// recovery (which always intercepts a host import's panic, confirmed by
// reading wasmdebug.FromRecovered directly) left only a generic wrapped
// error reaching the caller, so `instanceof` against the ORIGINAL class
// always failed.
func TestHostImportExceptionPropagatesUnchanged(t *testing.T) {
	html := `<html><body><script>
		` + uint8ArrayLiteral("bytes", explodingImportModuleBytes()) + `
		function Boom(code) { this.code = code; }
		WebAssembly.instantiate(bytes, {env: {explode: function() { throw new Boom(42); }}}).then(function(result) {
			try {
				result.instance.exports.run();
				console.log('no-throw');
			} catch (e) {
				console.log('caught=' + (e instanceof Boom) + ' code=' + e.code);
			}
		}, function(err) { console.log('unexpected-rejection=' + err); });
	</script></body></html>`
	_, logs, res := runJS(t, html)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "caught=true code=42")
}

// TestWasmCompileThenInstantiateModuleInstanceofHolds is the two-step
// `WebAssembly.compile(bytes).then(m => WebAssembly.instantiate(m,
// imports))` path's own counterpart, kept as its own test (rather than
// chained onto the one-step test above) because instantiating the SAME
// compiled module's host-import namespace twice in one render hits an
// unrelated wazero constraint ("module[env] has already been
// instantiated") — out of scope for this round, not something
// go-wasmcontainers reported.
func TestWasmCompileThenInstantiateModuleInstanceofHolds(t *testing.T) {
	html := `<html><body><script>
		` + wasmFixtureScript(t) + `
		WebAssembly.compile(bytes).then(function(mod) {
			console.log('compileModule=' + (mod instanceof WebAssembly.Module));
			return WebAssembly.instantiate(mod, {env: {host_offset: function(x) { return x + 1000; }}});
		}).then(function(inst) {
			console.log('twoStepInstance=' + (inst instanceof WebAssembly.Instance));
		}, function(err) { console.log('unexpected-rejection=' + err); });
	</script></body></html>`
	_, logs, res := runJS(t, html)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "compileModule=true", "twoStepInstance=true")
}

// startExportModuleBytes hand-builds the smallest valid module that imports
// env.touch () -> () and exports it as "_start" — deliberately an ordinary
// EXPORT, not the binary format's own internal start SECTION (an unrelated,
// unnamed mechanism almost no real module uses), to isolate exactly the
// distinction go-wasmcontainers flagged: the WebAssembly JS API's own
// instantiate algorithm runs the start section, never an exported function
// merely named "_start".
func startExportModuleBytes() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00, // magic + version
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type section: () -> ()
		0x02, 0x0d, 0x01, 0x03, 0x65, 0x6e, 0x76, 0x05, 0x74, 0x6f, 0x75, 0x63, 0x68, 0x00, 0x00, // import env.touch func type0
		0x03, 0x02, 0x01, 0x00, // function section: 1 func, type0
		0x07, 0x0a, 0x01, 0x06, 0x5f, 0x73, 0x74, 0x61, 0x72, 0x74, 0x00, 0x01, // export "_start" func idx1
		0x0a, 0x06, 0x01, 0x04, 0x00, 0x10, 0x00, 0x0b, // code: call func0 (touch); end
	}
}

// TestInstantiateDoesNotAutoRunAnExportNamedStart proves go-wasmcontainers'
// own confirmed fix (engine#342 fallout): WebAssembly.instantiate() must not
// call an exported "_start" itself — only the binary's own internal start
// SECTION, which this module deliberately has none of. wazero's own
// InstantiateModule defaults to calling an exported "_start" (confirmed by
// reading wazero's config.go: startFunctions defaults to []string{"_start"}
// — a WASI-CLI convenience, not a JS API behaviour), which this engine must
// clear. Their own real worker.js instantiates, THEN explicitly calls
// instance.exports._start() itself — running it a first time during
// instantiate() reached their guest's own I/O before its shared memory had
// even been posted to its creator, the exact cause of their reported
// "runtime.write ← runtime.writeErrData ← ... (repeating)" trap.
func TestInstantiateDoesNotAutoRunAnExportNamedStart(t *testing.T) {
	html := `<html><body><script>
		` + uint8ArrayLiteral("bytes", startExportModuleBytes()) + `
		var touched = 0;
		WebAssembly.instantiate(bytes, {env: {touch: function() { touched++; }}}).then(function(r) {
			console.log('after-instantiate touched=' + touched);
			r.instance.exports._start();
			console.log('after-explicit-call touched=' + touched);
		}, function(err) { console.log('unexpected-rejection=' + err); });
	</script></body></html>`
	_, logs, res := runJS(t, html)
	if res.ScriptsFailed != 0 {
		t.Fatalf("script failed: %+v logs=%v", res, logs)
	}
	mustHave(t, logs, "after-instantiate touched=0", "after-explicit-call touched=1")
}
