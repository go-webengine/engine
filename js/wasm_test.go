// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/dop251/goja"
	"github.com/tetratelabs/wazero/api"
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
