// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"context"
	"math/big"
	"strconv"

	"github.com/dop251/goja"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Requested directly by the user (not a corpus-found gap): go-webengine ran a
// page's JavaScript but could not run a page's own WebAssembly — the global
// was entirely absent, so `WebAssembly.instantiate(...)` threw a
// ReferenceError, the same silent-whole-script-abort shape
// newCustomElementRegistry's own doc comment already documents for a missing
// global.
//
// Bibliography first: the WebAssembly JS API spec
// (webassembly.github.io/spec/js-api/) for the exact method/property shapes
// and the ToWebAssemblyValue/ToJSValue type-coercion rules (i32 via ToInt32,
// i64 requires an actual BigInt — ToBigInt does NOT implicitly convert a
// plain Number, f32/f64 via ToNumber); wazero
// (github.com/tetratelabs/wazero, pure Go, zero dependencies beyond
// golang.org/x/sys, Apache-2.0 — compatible with this fleet's CGO=0
// doctrine) for the actual WASM execution. This file is a JS-facing binding
// layer over it, the same shape js/net.go is over net/http or
// js/interfaces.go is over the dom package — not a WASM interpreter written
// from scratch. wazero's own "Compiler" engine (native-code generation) only
// covers amd64/arm64; its "Interpreter" engine is pure Go and runs on every
// architecture this project's own CI cross-builds for (loong64/ppc64le/
// riscv64/s390x) — wazero.NewRuntime's own default already falls back to the
// Interpreter where the Compiler isn't supported, so no explicit engine
// selection is needed here.
//
// Scope, deliberately narrower than the full spec:
//   - WebAssembly.compile/instantiate/validate are implemented.
//     `new WebAssembly.Module(...)`/`new WebAssembly.Instance(...)` (the
//     synchronous constructor forms) are NOT — no confirmed real-world
//     container uses the sync form over the async one, which covers the
//     same need.
//   - A module's own exported function and exported memory are modelled.
//     Table and Global imports/exports are NOT — wazero's own table/global
//     API would need its own separate marshaling layer, and no confirmed
//     container needs them yet.
//   - `new WebAssembly.Memory(...)`/`new WebAssembly.Table(...)` (standalone,
//     not tied to any module) are NOT implemented — wazero has no API to
//     allocate a bare memory/table outside a module context; only a
//     module's OWN declared memory, reached through `instance.exports`, is
//     modelled.
//   - WASI (`wasi_snapshot_preview1` — stdio, filesystem, clocks) is NOT
//     wired. A module compiled assuming a WASI host fails to instantiate
//     with a LinkError naming the missing import — the honest outcome for a
//     capability this engine does not model, not a silent wrong answer.
//   - compileStreaming/instantiateStreaming are not implemented: this engine
//     has no Response/ReadableStream-based streaming fetch path to stream
//     from; compile()/instantiate() with a plain BufferSource cover the
//     same need for a module already fully in memory.
//   - A WASM call honours this render's own wall-clock budget (b.deadman,
//     the same one JS execution is bound by) via wazero's own
//     WithCloseOnContextDone, so a runaway module cannot outrun the page's
//     time budget the way an infinite JS loop already cannot.
//   - A module's own memory.grow is bounded (maxWasmMemoryPages) against a
//     hostile module exhausting host memory, the same budget discipline
//     this project's other resource caps (MaxImages, maxRequests,
//     maxCloneNodes) already follow.

const (
	// maxWasmModuleBytes bounds a single compile() call's input size — large
	// enough for a genuine compute module, small enough that a page cannot
	// use it to force an unbounded allocation.
	maxWasmModuleBytes = 32 << 20 // 32 MiB
	// maxWasmMemoryPages bounds a module's own linear memory growth (each
	// page is 64 KiB) — 256 pages = 16 MiB, generous for a real computation
	// without letting a hostile module's memory.grow loop exhaust the host.
	maxWasmMemoryPages = 256
)

// wasmRT returns this render's WebAssembly runtime, creating it lazily on
// first use so a page that never touches WebAssembly pays nothing for it.
// Released by Session.Close.
func (b *binder) wasmRT() wazero.Runtime {
	if b.wasmRuntime == nil {
		cfg := wazero.NewRuntimeConfig().
			WithMemoryLimitPages(maxWasmMemoryPages).
			WithCloseOnContextDone(true)
		b.wasmRuntime = wazero.NewRuntimeWithConfig(context.Background(), cfg)
	}
	return b.wasmRuntime
}

// wasmCtx derives a context deadlined at b.deadman — the SAME wall-clock
// budget JS execution is already bound by — so a WASM call cannot outrun
// the page's own time budget. Paired with wasmRT's WithCloseOnContextDone,
// a call that outlives the deadline is stopped, not left to spin forever.
func (b *binder) wasmCtx() (context.Context, context.CancelFunc) {
	parent := b.opt.Ctx
	if parent == nil {
		parent = context.Background()
	}
	return context.WithDeadline(parent, b.deadman)
}

// newWebAssembly builds the window.WebAssembly namespace object.
func (b *binder) newWebAssembly() *goja.Object {
	o := b.vm.NewObject()
	o.Set("validate", func(call goja.FunctionCall) goja.Value {
		data, ok := b.wasmBytes(call.Argument(0))
		if !ok {
			return b.vm.ToValue(false)
		}
		ctx, cancel := b.wasmCtx()
		defer cancel()
		compiled, err := b.wasmRT().CompileModule(ctx, data)
		if err != nil {
			return b.vm.ToValue(false)
		}
		_ = compiled.Close(ctx)
		return b.vm.ToValue(true)
	})
	o.Set("compile", func(call goja.FunctionCall) goja.Value {
		data, ok := b.wasmBytes(call.Argument(0))
		if !ok {
			return b.rejected(b.wasmError("CompileError", "compile: argument is not a BufferSource"))
		}
		ctx, cancel := b.wasmCtx()
		defer cancel()
		compiled, err := b.wasmRT().CompileModule(ctx, data)
		if err != nil {
			return b.rejected(b.wasmError("CompileError", err.Error()))
		}
		return b.resolved(b.wrapWasmModule(compiled))
	})
	o.Set("instantiate", func(call goja.FunctionCall) goja.Value {
		importsArg := call.Argument(1)
		if compiled, ok := b.wasmModuleOf(call.Argument(0)); ok {
			inst, err := b.instantiateWasm(compiled, importsArg)
			if err != nil {
				return b.rejected(err)
			}
			return b.resolved(inst)
		}
		data, ok := b.wasmBytes(call.Argument(0))
		if !ok {
			return b.rejected(b.wasmError("TypeError", "instantiate: first argument is not a BufferSource or a WebAssembly.Module"))
		}
		ctx, cancel := b.wasmCtx()
		defer cancel()
		compiled, err := b.wasmRT().CompileModule(ctx, data)
		if err != nil {
			return b.rejected(b.wasmError("CompileError", err.Error()))
		}
		inst, wErr := b.instantiateWasm(compiled, importsArg)
		if wErr != nil {
			return b.rejected(wErr)
		}
		result := b.vm.NewObject()
		result.Set("module", b.wrapWasmModule(compiled))
		result.Set("instance", inst)
		return b.resolved(result)
	})
	return o
}

// wasmBytes extracts the raw module bytes from a BufferSource argument,
// bounded by maxWasmModuleBytes.
func (b *binder) wasmBytes(v goja.Value) ([]byte, bool) {
	data, ok := b.bufferSourceBytes(v)
	if !ok || len(data) == 0 || len(data) > maxWasmModuleBytes {
		return nil, false
	}
	return data, true
}

// wasmError builds a plain Error object carrying the given spec error name
// (CompileError/LinkError/RuntimeError/TypeError) — this engine does not
// model the four as distinct constructable classes/prototypes (no confirmed
// page does `instanceof WebAssembly.CompileError`), only their `.name`/
// `.message`, the shape real error-handling code actually reads.
func (b *binder) wasmError(name, msg string) goja.Value {
	e := b.vm.NewGoError(errString(msg))
	_ = e.Set("name", name)
	_ = e.Set("message", msg)
	return e
}

// wasmModules caches each WebAssembly.Module JS wrapper's underlying
// wazero.CompiledModule by JS object identity, mirroring b.cache's own
// node<->wrapper identity role.
func (b *binder) wrapWasmModule(compiled wazero.CompiledModule) *goja.Object {
	o := b.vm.NewObject()
	if b.wasmModules == nil {
		b.wasmModules = map[*goja.Object]wazero.CompiledModule{}
	}
	b.wasmModules[o] = compiled
	return o
}

// wasmModuleOf returns the wazero.CompiledModule a WebAssembly.Module JS
// wrapper carries, or false if v is not one.
func (b *binder) wasmModuleOf(v goja.Value) (wazero.CompiledModule, bool) {
	obj, ok := v.(*goja.Object)
	if !ok {
		return nil, false
	}
	c, ok := b.wasmModules[obj]
	return c, ok
}

// instantiateWasm wires importsArg's functions as host imports, instantiates
// compiled, and returns the resulting instance's exports object.
func (b *binder) instantiateWasm(compiled wazero.CompiledModule, importsArg goja.Value) (*goja.Object, goja.Value) {
	ctx, cancel := b.wasmCtx()
	defer cancel()
	if err := b.buildHostImports(ctx, compiled, importsArg); err != nil {
		return nil, err
	}
	mod, err := b.wasmRT().InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		return nil, b.wasmError("LinkError", err.Error())
	}
	inst := b.vm.NewObject()
	inst.Set("exports", b.wasmExports(mod, compiled))
	return inst, nil
}

// buildHostImports groups compiled's imported functions by their own module
// namespace and registers each as a wazero host function that calls the
// matching JS function (importsArg[moduleName][name]), instantiating each
// host namespace before the caller instantiates compiled itself (wazero's
// own required ordering).
func (b *binder) buildHostImports(ctx context.Context, compiled wazero.CompiledModule, importsArg goja.Value) goja.Value {
	imports := compiled.ImportedFunctions()
	if len(imports) == 0 {
		return nil
	}
	importsObj, _ := importsArg.(*goja.Object)
	byModule := map[string][]api.FunctionDefinition{}
	var order []string
	for _, def := range imports {
		mn, _, _ := def.Import()
		if _, seen := byModule[mn]; !seen {
			order = append(order, mn)
		}
		byModule[mn] = append(byModule[mn], def)
	}
	for _, moduleName := range order {
		var nsObj *goja.Object
		if importsObj != nil {
			nsObj, _ = importsObj.Get(moduleName).(*goja.Object)
		}
		builder := b.wasmRT().NewHostModuleBuilder(moduleName)
		for _, def := range byModule[moduleName] {
			_, name, _ := def.Import()
			var fn goja.Callable
			if nsObj != nil {
				fn, _ = goja.AssertFunction(nsObj.Get(name))
			}
			if fn == nil {
				// Matches a real browser's own LinkError here ("imports
				// don't match, such as a non-callable function import") —
				// caught at instantiate time, not deferred to the first
				// call, the same way a genuinely missing import (no WASI,
				// or the page's own importObject is incomplete) should
				// fail loudly rather than silently.
				return b.wasmError("LinkError", "import "+moduleName+"."+name+" is not a function")
			}
			builder.NewFunctionBuilder().
				WithGoModuleFunction(b.hostImportFunc(fn, def), def.ParamTypes(), def.ResultTypes()).
				Export(name)
		}
		if _, err := builder.Instantiate(ctx); err != nil {
			return b.wasmError("LinkError", err.Error())
		}
	}
	return nil
}

// hostImportFunc adapts a JS function into the wazero Go-module-function
// shape a WASM import expects: WASM's own argument values arrive via
// ToJSValue (the WASM-to-JS direction — the module is handing values TO the
// host here), the JS call's result goes back via ToWebAssemblyValue. fn is
// guaranteed non-nil here — buildHostImports already rejects a missing/
// non-callable import with a LinkError before ever registering it.
func (b *binder) hostImportFunc(fn goja.Callable, def api.FunctionDefinition) api.GoModuleFunc {
	params := def.ParamTypes()
	results := def.ResultTypes()
	return func(ctx context.Context, mod api.Module, stack []uint64) {
		args := make([]goja.Value, len(params))
		for i, t := range params {
			args[i] = b.fromWasmValue(stack[i], t)
		}
		ret, err := fn(goja.Undefined(), args...)
		if err != nil {
			panic(b.vm.NewGoError(err))
		}
		b.toWasmResults(stack, ret, results)
	}
}

// wasmExports builds mod's exports object: every exported function is a
// callable (see wrapExportedFunction), and a module's own exported memory
// (if it declares exactly one) is a live WebAssembly.Memory wrapper. Table
// and Global exports are omitted — see this file's own top doc comment.
func (b *binder) wasmExports(mod api.Module, compiled wazero.CompiledModule) *goja.Object {
	exp := b.vm.NewObject()
	for name := range compiled.ExportedFunctions() {
		fn := mod.ExportedFunction(name)
		if fn == nil {
			continue
		}
		exp.Set(name, b.wrapWasmFunction(fn))
	}
	for name := range compiled.ExportedMemories() {
		if mem := mod.Memory(); mem != nil {
			exp.Set(name, b.wrapWasmMemory(mem))
		}
	}
	return exp
}

// wrapWasmFunction wraps a WASM exported function as a JS callable: JS
// arguments convert to WASM parameter types via ToWebAssemblyValue, and the
// call's results convert back via ToJSValue — zero results is undefined,
// one result is that value, more than one is an array (per spec).
func (b *binder) wrapWasmFunction(fn api.Function) goja.Value {
	def := fn.Definition()
	params := def.ParamTypes()
	results := def.ResultTypes()
	return b.vm.ToValue(func(call goja.FunctionCall) goja.Value {
		args := make([]uint64, len(params))
		for i, t := range params {
			args[i] = b.toWasmValue(call.Argument(i), t)
		}
		ctx, cancel := b.wasmCtx()
		defer cancel()
		out, err := fn.Call(ctx, args...)
		if err != nil {
			panic(b.wasmError("RuntimeError", err.Error()))
		}
		switch len(results) {
		case 0:
			return goja.Undefined()
		case 1:
			return b.fromWasmValue(out[0], results[0])
		default:
			vals := make([]interface{}, len(results))
			for i, t := range results {
				vals[i] = b.fromWasmValue(out[i], t)
			}
			return b.vm.NewArray(vals...)
		}
	})
}

// wrapWasmMemory wraps a module's own linear memory as a WebAssembly.Memory:
// `.buffer` is a REAL, zero-copy ArrayBuffer view over wazero's own backing
// bytes (NewArrayBuffer wraps the slice directly, no copy — see its own doc
// comment), re-fetched whenever the memory's size no longer matches the
// cached view's own length, since a successful memory.grow can relocate
// wazero's backing array (Memory.Read's own doc comment: "any shared views
// from Read must be refreshed" after growing).
func (b *binder) wrapWasmMemory(mem api.Memory) *goja.Object {
	o := b.vm.NewObject()
	var cachedSize uint32 = 0xffffffff // force a fetch on first access
	var cachedBuf goja.ArrayBuffer
	refresh := func() goja.ArrayBuffer {
		size := mem.Size()
		if size != cachedSize {
			raw, _ := mem.Read(0, size)
			cachedBuf = b.vm.NewArrayBuffer(raw)
			cachedSize = size
		}
		return cachedBuf
	}
	b.accessor(o, "buffer", func() goja.Value { return b.vm.ToValue(refresh()) }, nil)
	o.Set("grow", func(call goja.FunctionCall) goja.Value {
		delta := uint32(call.Argument(0).ToInteger())
		prev, ok := mem.Grow(delta)
		if !ok {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: would exceed the memory's maximum"))
		}
		return b.vm.ToValue(prev)
	})
	return o
}

// toWasmValue converts a JS argument to its declared WASM parameter type's
// uint64 stack slot, per ToWebAssemblyValue: i32 via a 32-bit truncation
// (ToInt32's own modulo-2^32 behaviour), i64 requires an actual BigInt —
// ToBigInt does NOT implicitly convert a plain Number, so a page passing a
// bare number for an i64 parameter gets the same TypeError a real browser
// would — f32/f64 via ToNumber.
func (b *binder) toWasmValue(v goja.Value, t api.ValueType) uint64 {
	switch t {
	case api.ValueTypeI32:
		return uint64(uint32(v.ToInteger()))
	case api.ValueTypeI64:
		bi, ok := v.Export().(*big.Int)
		if !ok {
			panic(b.vm.NewTypeError("WebAssembly: an i64 parameter requires a BigInt, not a Number"))
		}
		return wrapBigIntTo64(bi)
	case api.ValueTypeF32:
		return api.EncodeF32(float32(v.ToFloat()))
	case api.ValueTypeF64:
		return api.EncodeF64(v.ToFloat())
	default:
		panic(b.vm.NewTypeError("WebAssembly: unsupported value type (only i32/i64/f32/f64 are modelled)"))
	}
}

// fromWasmValue converts one WASM stack slot back to a JS value per
// ToJSValue: i32/f32/f64 become a Number, i64 becomes a BigInt (goja's
// ToValue maps a *big.Int to a genuine JS BigInt).
func (b *binder) fromWasmValue(raw uint64, t api.ValueType) goja.Value {
	switch t {
	case api.ValueTypeI32:
		return b.vm.ToValue(api.DecodeI32(raw))
	case api.ValueTypeI64:
		return b.vm.ToValue(big.NewInt(int64(raw)))
	case api.ValueTypeF32:
		return b.vm.ToValue(api.DecodeF32(raw))
	case api.ValueTypeF64:
		return b.vm.ToValue(api.DecodeF64(raw))
	default:
		return goja.Undefined()
	}
}

// toWasmResults writes a JS host-import function's return value into stack
// per results' declared types — the same zero/one/many shape
// wrapWasmFunction's own results handling mirrors, the other direction.
func (b *binder) toWasmResults(stack []uint64, ret goja.Value, results []api.ValueType) {
	switch len(results) {
	case 0:
		return
	case 1:
		stack[0] = b.toWasmValue(ret, results[0])
	default:
		arr, ok := ret.(*goja.Object)
		if !ok {
			panic(b.vm.NewTypeError("WebAssembly: a multi-result import must return an array"))
		}
		for i, t := range results {
			stack[i] = b.toWasmValue(arr.Get(strconv.Itoa(i)), t)
		}
	}
}

// wrapBigIntTo64 reduces bi modulo 2^64 and returns its two's-complement bit
// pattern, the ToBigInt64 algorithm's own "asIntN(64, bigint)" semantics —
// correct for a BigInt outside the representable i64 range, not just the
// common in-range case.
func wrapBigIntTo64(bi *big.Int) uint64 {
	mod := new(big.Int).Mod(bi, wasm64Mask)
	return mod.Uint64()
}

var wasm64Mask = new(big.Int).Lsh(big.NewInt(1), 64)
