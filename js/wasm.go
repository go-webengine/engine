// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"context"
	"math/big"
	"strconv"
	"sync"

	"github.com/dop251/goja"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
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
//   - `new WebAssembly.Memory(descriptor)` (standalone, not tied to any
//     module) IS implemented — confirmed directly needed by
//     go-wasmcontainers: every one of their guest containers
//     constructs a shared Memory in one Worker before instantiating
//     anything, then posts that SAME object to a second, plain-JS worker
//     that services host calls by reading/writing its bytes directly while
//     the guest blocks in Atomics.wait. See newWasmMemory's own doc
//     comment for the implementation (it does not go through wazero at
//     all — a standalone Memory has no module to belong to).
//   - `new WebAssembly.Table(...)` is NOT implemented — no confirmed need.
//   - Passing a standalone `WebAssembly.Memory` as a module's memory
//     IMPORT (the common toolchain pattern for a module compiled with
//     `--import-memory`, e.g. Emscripten with pthreads) is NOT supported:
//     wazero's public API (confirmed by direct inspection of
//     wazero.HostModuleBuilder and api.MemoryDefinition/api.Memory,
//     v1.8.2) has no way to tell whether a module's own declared memory is
//     imported vs. local, no way to supply host-owned bytes to satisfy a
//     memory import, and no public method to detect the WASM threads
//     proposal's `shared` flag on a memory type at all — only a module's
//     OWN locally-declared, exported memory is modelled (wasmExports/
//     wrapWasmMemory below). A module that imports memory fails today with
//     whatever LinkError wazero's own InstantiateModule produces for an
//     unresolved import; this is a real upstream wazero limitation, not a
//     binding gap this file's own code can route around.
//   - WASI (`wasi_snapshot_preview1` — stdio, filesystem, clocks) is NOT
//     wired. A module compiled assuming a WASI host fails to instantiate
//     with a LinkError naming the missing import — the honest outcome for a
//     capability this engine does not model, not a silent wrong answer.
//   - compileStreaming/instantiateStreaming are implemented (round N+1 of
//     the go-wasmcontainers WebGPU-readiness roadmap, milestone 1), but not
//     via genuine incremental streaming: this engine's fetch() already
//     fully buffers a response before resolving it (js/net.go), so there is
//     no partial-bytes-as-they-arrive path to feed wazero's own
//     CompileModule([]byte), which itself takes a complete byte slice, not
//     a stream. The JS-facing contract (Promise-based, takes a Response or
//     a promise of one) is satisfied exactly; only the internal compile is
//     buffer-then-compile rather than incrementally streamed. See
//     streamingWasmBytes' own doc comment for how a Response/promise
//     argument is resolved to bytes.
//   - A WASM call honours this render's own wall-clock budget (b.deadman,
//     the same one JS execution is bound by) via wazero's own
//     WithCloseOnContextDone, so a runaway module cannot outrun the page's
//     time budget the way an infinite JS loop already cannot.
//   - A module's own size (Options.MaxWasmModuleBytes) and memory growth
//     (Options.MaxWasmMemoryBytes) are each bounded by a CONFIGURABLE quota
//     (zero selects a sane default — 32 MiB / 16 MiB respectively), not a
//     hardcoded ceiling — a caller with genuinely larger real modules (Go-
//     compiled WebAssembly binaries reaching into the hundreds of MiB) can
//     raise either toward the wasm32 4 GiB address-space ceiling, which
//     this engine clamps to rather than ever handing wazero a value its
//     own WithMemoryLimitPages would panic on. The same budget discipline
//     this project's other resource caps (MaxImages, maxRequests,
//     maxCloneNodes) already follow — always bounded, never unlimited.

const (
	// defaultMaxWasmModuleBytes is Options.MaxWasmModuleBytes' zero-value
	// default — large enough for a genuine compute module, small enough
	// that a page cannot use it to force an unbounded allocation. A caller
	// with genuinely larger real-world modules (Go-compiled WebAssembly
	// binaries commonly reach into the tens to low hundreds of MiB) raises
	// Options.MaxWasmModuleBytes explicitly; there is no "disable" value,
	// matching wazero's own CompileModule, which has no unlimited-size mode.
	defaultMaxWasmModuleBytes = 32 << 20 // 32 MiB
	// defaultMaxWasmMemoryBytes is Options.MaxWasmMemoryBytes' zero-value
	// default — generous for a real computation without letting a hostile
	// module's memory.grow loop exhaust the host.
	defaultMaxWasmMemoryBytes = 16 << 20 // 16 MiB
	// wasmPageSize is WebAssembly's own fixed linear-memory page size (not
	// configurable, spec-mandated).
	wasmPageSize = 64 << 10 // 64 KiB
	// maxWasmMemoryPages is the wasm32 address-space ceiling (2^32 bytes /
	// 64 KiB per page) — the largest page count any 32-bit module could
	// ever actually use, so a configured byte value past this is clamped
	// rather than handed to wazero as a meaninglessly large request.
	maxWasmMemoryPages = (1 << 32) / wasmPageSize // 65536 pages = 4 GiB
)

// maxWasmModuleBytes resolves Options.MaxWasmModuleBytes: zero selects
// defaultMaxWasmModuleBytes, matching Options.Timeout's own "zero selects
// the default" convention.
func (b *binder) maxWasmModuleBytes() int64 {
	if b.opt.MaxWasmModuleBytes <= 0 {
		return defaultMaxWasmModuleBytes
	}
	return b.opt.MaxWasmModuleBytes
}

// maxWasmMemoryPages resolves Options.MaxWasmMemoryBytes to a wazero page
// count: zero selects defaultMaxWasmMemoryBytes, a value not already a
// whole number of pages rounds UP (a cap must cover at least the byte count
// asked for, never less), and the result is clamped to the wasm32 ceiling —
// wazero's own WithMemoryLimitPages takes a page count and always needs a
// concrete, finite value, so there is no byte value that means "unlimited".
func (b *binder) maxWasmMemoryPages() uint32 {
	bytes := b.opt.MaxWasmMemoryBytes
	if bytes <= 0 {
		bytes = defaultMaxWasmMemoryBytes
	}
	// No lower-bound check needed here: bytes is always >= 1 at this point
	// (the branch above already redirected any <= 0 value to the default),
	// and ceiling-dividing a positive numerator by a positive denominator
	// is always >= 1 — pages can never come out below 1.
	pages := (bytes + wasmPageSize - 1) / wasmPageSize
	if pages > maxWasmMemoryPages {
		pages = maxWasmMemoryPages
	}
	return uint32(pages)
}

// wasmRT returns this render's WebAssembly runtime, creating it lazily on
// first use so a page that never touches WebAssembly pays nothing for it.
// Released by Session.Close.
//
// The WASM threads proposal's CoreFeaturesThreads is enabled ONLY when
// Options.CrossOriginIsolated — confirmed directly needed by
// go-wasmcontainers: their guest modules declare their OWN memory (never
// import it) with the threads proposal's `shared` flag set, which wazero's
// own decoder (confirmed by reading internal/wasm/binary/memory.go)
// otherwise rejects outright with "shared memory requested but threads
// feature not enabled" — a CompileError this binding's existing error
// handling already surfaces correctly, with no separate pre-check needed.
// Gating it on cross-origin isolation, rather than enabling it
// unconditionally, matches the SAME restriction a real browser enforces
// for shared memory, and keeps this one consistent with the EXISTING
// SharedArrayBuffer/Atomics gate (atomics.go/window.go) — a module that
// could read/write memory a Worker then shares via postMessage is exactly
// the capability that gate exists to withhold outside an isolated context.
func (b *binder) wasmRT() wazero.Runtime {
	if b.rootBinder != nil {
		return b.rootBinder.wasmRT()
	}
	if b.wasmRuntime == nil {
		features := api.CoreFeaturesV2
		if b.opt.CrossOriginIsolated {
			features = features.SetEnabled(experimental.CoreFeaturesThreads, true)
		}
		cfg := wazero.NewRuntimeConfigInterpreter().
			WithCoreFeatures(features).
			WithMemoryLimitPages(b.maxWasmMemoryPages()).
			WithCloseOnContextDone(true)
		b.wasmRuntime = wazero.NewRuntimeWithConfig(context.Background(), cfg)
	}
	return b.wasmRuntime
}

// wasmInstantiateMu delegates to the root binder exactly like wasmRT,
// and guards the import-binding window every instantiateWasm call opens:
// register a temporary host module under the GUEST's own required
// import-namespace name (e.g. "env"), instantiate the guest against it,
// then immediately close that host module again to free the name. On a
// Runtime now shared by every worker (see wasmRT's own doc comment),
// two guest modules that both import from "env" — go-wasmcontainers'
// own confirmed shape, one compiled guest instantiated many times
// concurrently — would otherwise collide on that name even though
// neither guest instance cares what the host module was called.
// Confirmed directly (scratchpad, not assumed): wazero binds an
// import's host function into the guest at InstantiateModule time, not
// by a live name re-lookup on every call, so closing the host module
// right after the guest instantiates does not disturb that guest's own
// already-bound calls — only the lock's own critical section needs to
// stay serialized against a second goroutine's host-module registration
// racing in under the same name before the first guest is bound.
func (b *binder) wasmInstantiateMu() *sync.Mutex {
	if b.rootBinder != nil {
		return b.rootBinder.wasmInstantiateMu()
	}
	return &b.wasmInstantiateLock
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
	b.installWasmClassIdentity(o)
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
		return b.resolved(b.wrapWasmModule(compiled, moduleHasSharedMemory(data)))
	})
	o.Set("instantiate", func(call goja.FunctionCall) goja.Value {
		importsArg := call.Argument(1)
		if compiled, isShared, ok := b.wasmModuleOf(call.Argument(0)); ok {
			inst, err := b.instantiateWasm(compiled, importsArg, isShared)
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
		isShared := moduleHasSharedMemory(data)
		inst, wErr := b.instantiateWasm(compiled, importsArg, isShared)
		if wErr != nil {
			return b.rejected(wErr)
		}
		result := b.vm.NewObject()
		result.Set("module", b.wrapWasmModule(compiled, isShared))
		result.Set("instance", inst)
		return b.resolved(result)
	})
	o.Set("compileStreaming", func(call goja.FunctionCall) goja.Value {
		data, ok := b.streamingWasmBytes(call.Argument(0))
		if !ok {
			return b.rejected(b.wasmError("TypeError", "compileStreaming: argument did not resolve to a Response with a WebAssembly BufferSource body"))
		}
		ctx, cancel := b.wasmCtx()
		defer cancel()
		compiled, err := b.wasmRT().CompileModule(ctx, data)
		if err != nil {
			return b.rejected(b.wasmError("CompileError", err.Error()))
		}
		return b.resolved(b.wrapWasmModule(compiled, moduleHasSharedMemory(data)))
	})
	o.Set("instantiateStreaming", func(call goja.FunctionCall) goja.Value {
		importsArg := call.Argument(1)
		data, ok := b.streamingWasmBytes(call.Argument(0))
		if !ok {
			return b.rejected(b.wasmError("TypeError", "instantiateStreaming: argument did not resolve to a Response with a WebAssembly BufferSource body"))
		}
		ctx, cancel := b.wasmCtx()
		defer cancel()
		compiled, err := b.wasmRT().CompileModule(ctx, data)
		if err != nil {
			return b.rejected(b.wasmError("CompileError", err.Error()))
		}
		isShared := moduleHasSharedMemory(data)
		inst, wErr := b.instantiateWasm(compiled, importsArg, isShared)
		if wErr != nil {
			return b.rejected(wErr)
		}
		result := b.vm.NewObject()
		result.Set("module", b.wrapWasmModule(compiled, isShared))
		result.Set("instance", inst)
		return b.resolved(result)
	})
	o.Set("Memory", func(call goja.ConstructorCall) *goja.Object { return b.newWasmMemory(call) })
	return o
}

// streamingWasmBytes resolves compileStreaming/instantiateStreaming's own
// argument — a Response, or (the overwhelmingly common real shape,
// `instantiateStreaming(fetch(url))`) a promise of one — to the module's
// raw bytes, bounded the same way a direct BufferSource argument already
// is (wasmBytes).
//
// This engine's OWN promises (js/net.go's resolved/rejected, which fetch()
// itself and Response.arrayBuffer() both return) are always already
// SETTLED by the time any JS code observes them — the real HTTP GET (or
// in-memory buffering) already happened inline before resolve() was ever
// called (see fetch()'s own doc comment). So "await" here is a direct,
// synchronous read of goja's own Promise.State()/Result(), not a
// continuation — there is no job-queue machinery to drive, and a promise
// that is NOT already settled (never produced by this engine's own code)
// is correctly reported as unresolved rather than guessed at.
func (b *binder) streamingWasmBytes(v goja.Value) ([]byte, bool) {
	respVal, ok := b.awaitSettled(v)
	if !ok {
		return nil, false
	}
	resp, ok := respVal.(*goja.Object)
	if !ok {
		return nil, false
	}
	arrayBuffer, ok := goja.AssertFunction(resp.Get("arrayBuffer"))
	if !ok {
		return nil, false
	}
	abPromise, err := arrayBuffer(resp)
	if err != nil {
		return nil, false
	}
	abVal, ok := b.awaitSettled(abPromise)
	if !ok {
		return nil, false
	}
	return b.wasmBytes(abVal)
}

// awaitSettled resolves v to its final value when v is a Promise this
// engine's own code already settled synchronously (see
// streamingWasmBytes' own doc comment for why that is the only kind this
// engine ever produces); a non-Promise value is returned unchanged. ok is
// false for a rejected promise or one that is still pending.
func (b *binder) awaitSettled(v goja.Value) (goja.Value, bool) {
	obj, isObj := v.(*goja.Object)
	if !isObj {
		return v, true
	}
	p, isPromise := obj.Export().(*goja.Promise)
	if !isPromise {
		return v, true
	}
	if p.State() != goja.PromiseStateFulfilled {
		return nil, false
	}
	return p.Result(), true
}

// wasmBytes extracts the raw module bytes from a BufferSource argument,
// bounded by maxWasmModuleBytes.
func (b *binder) wasmBytes(v goja.Value) ([]byte, bool) {
	data, ok := b.bufferSourceBytes(v)
	if !ok || len(data) == 0 || int64(len(data)) > b.maxWasmModuleBytes() {
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

// moduleHasSharedMemory reports whether raw's own FIRST locally-declared
// memory (this engine never supports a memory IMPORT — see this file's own
// top doc comment) sets the WASM threads proposal's `shared` limits flag.
// wazero's own public API (confirmed by direct inspection: api.
// MemoryDefinition only exposes Min/Max, api.Memory has no Shared method
// at all, v1.8.2) has no way to ask this once a module has compiled
// successfully, so this engine answers it itself by walking the RAW module
// bytes directly — using the EXACT rule wazero's own internal decoder
// applies (confirmed by reading internal/wasm/binary/limits.go, not
// guessed from the spec text alone): a limits value's leading flag byte is
// 0x02 (shared, no max — rejected elsewhere, see wasmRT's own
// CoreFeaturesThreads gate, but still decodes) or 0x03 (shared, with max);
// 0x00/0x01 are not shared. A module with no memory section, a memory
// section declaring zero memories, a decode error (CompileModule will
// already have rejected genuinely malformed bytes before this is ever
// called), or any memory section this walk cannot fully parse all report
// false rather than guessing or panicking — this is a best-effort read of
// an input CompileModule has ALREADY validated by the time this runs, not
// a second validator.
func moduleHasSharedMemory(raw []byte) bool {
	const headerLen = 8 // "\0asm" + a 4-byte version, before the first section
	const sectionIDMemory = 5
	if len(raw) < headerLen {
		return false
	}
	r := raw[headerLen:]
	for len(r) > 0 {
		id := r[0]
		r = r[1:]
		size, n, ok := decodeULEB32(r)
		if !ok || n > len(r) || uint64(size) > uint64(len(r)-n) {
			return false
		}
		r = r[n:]
		content := r[:size]
		r = r[size:]
		if id != sectionIDMemory {
			continue
		}
		count, n, ok := decodeULEB32(content)
		if !ok || count == 0 || n >= len(content) {
			return false
		}
		// Only the first declared memory is inspected — this engine's own
		// wasmExports already only ever models a SINGLE exported memory
		// (ExportedMemories' own doc comment: a module may have at most
		// one memory before the multi-memory proposal, which this binding
		// does not otherwise support), so a second entry could never be
		// reached through instance.exports.memory anyway.
		flag := content[n]
		return flag == 0x02 || flag == 0x03
	}
	return false
}

// decodeULEB32 decodes an unsigned LEB128-encoded uint32 from the start of
// b — the exact integer encoding every WASM section id's size, vector
// count, and limits field uses. Returns the value, how many bytes it
// consumed, and false if b ends before a terminating byte (continuation
// bit clear) within the first 5 bytes (the most a valid uint32 LEB128
// encoding ever needs) — a bounded loop, never unbounded, over an input
// slice of fixed length.
func decodeULEB32(b []byte) (v uint32, n int, ok bool) {
	var shift uint
	for n = 0; n < len(b) && n < 5; n++ {
		byt := b[n]
		v |= uint32(byt&0x7f) << shift
		if byt&0x80 == 0 {
			return v, n + 1, true
		}
		shift += 7
	}
	return 0, 0, false
}

// wasmModuleState pairs a compiled module with whether this engine detected
// — from the RAW module bytes, via moduleHasSharedMemory — that its own
// LOCALLY DECLARED memory (this engine does not support a memory import at
// all; see this file's own top doc comment) sets the WASM threads
// proposal's shared flag. Needed again at instantiate time, including the
// two-step `WebAssembly.compile(bytes).then(m => WebAssembly.instantiate(m,
// imports))` path where the raw bytes are no longer available — only the
// already-compiled module is — so it must be captured once, here, rather
// than re-derived from bytes that may no longer be in scope.
type wasmModuleState struct {
	compiled wazero.CompiledModule
	isShared bool
}

// installWasmClassIdentity wires WebAssembly.Module and WebAssembly.Instance
// as real constructor functions, each with its own .prototype — so
// `instanceof` holds for the objects compile()/instantiate() return, which
// are stamped with these same prototypes (wrapWasmModule, instantiateWasm).
// Confirmed directly needed by go-wasmcontainers: Go's own wasm_exec.js
// (Go.run, wasm_exec.js:480) does `if (!(instance instanceof
// WebAssembly.Instance)) throw ...` — the global didn't exist at all
// before this, so the `instanceof` itself threw "Value is not an object:
// undefined" rather than evaluating to false. The SYNCHRONOUS constructor
// forms (`new WebAssembly.Module(bytes)`/`new WebAssembly.Instance(mod,
// imports)`) remain deliberately unsupported — this file's own top doc
// comment already states no confirmed container uses them over the async
// compile()/instantiate() — so calling either via `new` throws a clear,
// named TypeError rather than silently doing nothing.
func (b *binder) installWasmClassIdentity(o *goja.Object) {
	b.wasmModuleProto = b.vm.NewObject()
	b.wasmInstanceProto = b.vm.NewObject()
	o.Set("Module", b.wasmClassCtor("Module", b.wasmModuleProto))
	o.Set("Instance", b.wasmClassCtor("Instance", b.wasmInstanceProto))
}

// wasmClassCtor builds one WebAssembly.{Module,Instance} constructor
// function, wiring its own `.name`/`.prototype` and that prototype's own
// `.constructor` back-reference — the same pattern installSharedArrayBuffer
// (atomics.go) already established for a constructor whose instances are
// built some OTHER way than calling it directly via `new`.
func (b *binder) wasmClassCtor(name string, proto *goja.Object) goja.Value {
	ctor := func(call goja.ConstructorCall) *goja.Object {
		panic(b.wasmError("TypeError", "WebAssembly."+name+": the synchronous constructor is not supported; use WebAssembly.compile/instantiate"))
	}
	ctorVal := b.vm.ToValue(ctor)
	if ctorObj, ok := ctorVal.(*goja.Object); ok {
		_ = ctorObj.DefineDataProperty("name", b.vm.ToValue(name), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
		_ = ctorObj.Set("prototype", proto)
		_ = proto.DefineDataProperty("constructor", ctorVal, goja.FLAG_TRUE, goja.FLAG_FALSE, goja.FLAG_FALSE)
	}
	return ctorVal
}

// wasmModules caches each WebAssembly.Module JS wrapper's underlying state
// by JS object identity, mirroring b.cache's own node<->wrapper identity
// role. The wrapper is stamped with wasmModuleProto so `instanceof
// WebAssembly.Module` holds (installWasmClassIdentity).
func (b *binder) wrapWasmModule(compiled wazero.CompiledModule, isShared bool) *goja.Object {
	o := b.vm.NewObject()
	_ = o.SetPrototype(b.wasmModuleProto)
	if b.wasmModules == nil {
		b.wasmModules = map[*goja.Object]wasmModuleState{}
	}
	b.wasmModules[o] = wasmModuleState{compiled: compiled, isShared: isShared}
	registerWasmModule(o, compiled, isShared)
	return o
}

// wasmModuleOf returns the wazero.CompiledModule and isShared bit a
// WebAssembly.Module JS wrapper carries, or false if v is not one.
func (b *binder) wasmModuleOf(v goja.Value) (compiled wazero.CompiledModule, isShared bool, ok bool) {
	obj, isObj := v.(*goja.Object)
	if !isObj {
		return nil, false, false
	}
	st, found := b.wasmModules[obj]
	return st.compiled, st.isShared, found
}

// wasmModuleRegistry mirrors wasmMemoryRegistry's own package-level,
// by-object-identity design — needed for the identical reason: cloneAt
// (clone.go) has no *binder to consult b.wasmModules with, and a
// WebAssembly.Module posted via postMessage must be recognized as one
// regardless of which binder's VM it was built in. Confirmed directly
// needed by go-wasmcontainers: their engine worker compiles a guest once
// and posts the resulting Module to a container worker, which must still
// recognize and instantiate it (WebAssembly JS API's own "Module is
// Serializable" contract, HTML's structured clone within one agent
// cluster).
var (
	wasmModuleRegistryMu sync.Mutex
	wasmModuleRegistry   = map[*goja.Object]wasmModuleState{}
)

func registerWasmModule(obj *goja.Object, compiled wazero.CompiledModule, isShared bool) {
	wasmModuleRegistryMu.Lock()
	wasmModuleRegistry[obj] = wasmModuleState{compiled: compiled, isShared: isShared}
	wasmModuleRegistryMu.Unlock()
}

// lookupWasmModule reports whether obj is a registered WebAssembly.Module
// wrapper (any binder's own), mirroring lookupWasmMemory's own shape.
func lookupWasmModule(obj *goja.Object) (compiled wazero.CompiledModule, isShared bool, ok bool) {
	wasmModuleRegistryMu.Lock()
	st, found := wasmModuleRegistry[obj]
	wasmModuleRegistryMu.Unlock()
	return st.compiled, st.isShared, found
}

// instantiateWasm wires importsArg's functions as host imports, instantiates
// compiled, and returns the resulting instance's exports object. isShared
// (from moduleHasSharedMemory/wasmModuleOf) decides whether
// instance.exports.memory, if the module declares one, is wrapped as a
// SharedArrayBuffer-classed, postMessage-able Memory (wasmExports/
// wrapWasmMemory).
//
// Always anonymous (WithName("")), never the module's own default/
// custom-section name: wazero's Runtime namespaces instances by name and
// refuses a second one under a name already live ("module[...] has
// already been instantiated") — confirmed directly. Harmless for the
// ordinary one-instance-per-module case this always was, but REQUIRED
// now that every worker (however many) shares ONE Runtime (see wasmRT's
// own doc comment): go-wasmcontainers' own confirmed shape instantiates
// the SAME compiled guest many times concurrently, one per container,
// every one sharing the same custom-section name — a second, named
// instance of the identical module on a shared Runtime would otherwise
// fail outright. Confirmed directly that repeated anonymous
// instantiation of the same CompiledModule on one Runtime works, and
// that closing one such instance does not affect a sibling instance of
// the same module.
func (b *binder) instantiateWasm(compiled wazero.CompiledModule, importsArg goja.Value, isShared bool) (*goja.Object, goja.Value) {
	ctx, cancel := b.wasmCtx()
	defer cancel()

	// Serialized across every worker sharing this Runtime (see
	// wasmInstantiateMu's own doc comment): the host-import module(s)
	// built below occupy their guest-required name only for this
	// critical section, then are closed again before the lock is
	// released, so two guests that both import e.g. "env" never
	// collide even though their host bindings are never visible to
	// each other.
	mu := b.wasmInstantiateMu()
	mu.Lock()
	defer mu.Unlock()

	hostMods, errv := b.buildHostImports(ctx, compiled, importsArg)
	defer func() {
		for _, hm := range hostMods {
			_ = hm.Close(ctx)
		}
	}()
	if errv != nil {
		return nil, errv
	}
	mod, err := b.wasmRT().InstantiateModule(ctx, compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, b.wasmError("LinkError", err.Error())
	}
	inst := b.vm.NewObject()
	_ = inst.SetPrototype(b.wasmInstanceProto)
	inst.Set("exports", b.wasmExports(mod, compiled, isShared))
	return inst, nil
}

// buildHostImports groups compiled's imported functions by their own module
// namespace and registers each as a wazero host function that calls the
// matching JS function (importsArg[moduleName][name]), instantiating each
// host namespace before the caller instantiates compiled itself (wazero's
// own required ordering). The returned host modules are already
// instantiated and live on the shared Runtime under the guest's required
// import name; the caller closes each one (freeing that name again) right
// after compiled itself is instantiated, whether or not instantiation
// succeeded, while still holding wasmInstantiateMu.
func (b *binder) buildHostImports(ctx context.Context, compiled wazero.CompiledModule, importsArg goja.Value) ([]api.Module, goja.Value) {
	imports := compiled.ImportedFunctions()
	if len(imports) == 0 {
		return nil, nil
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
	var hostMods []api.Module
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
				return hostMods, b.wasmError("LinkError", "import "+moduleName+"."+name+" is not a function")
			}
			builder.NewFunctionBuilder().
				WithGoModuleFunction(b.hostImportFunc(fn, def), def.ParamTypes(), def.ResultTypes()).
				Export(name)
		}
		hm, err := builder.Instantiate(ctx)
		if err != nil {
			return hostMods, b.wasmError("LinkError", err.Error())
		}
		hostMods = append(hostMods, hm)
	}
	return hostMods, nil
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
// (if it declares exactly one) is a live WebAssembly.Memory wrapper —
// SharedArrayBuffer-classed and postMessage-able when isShared, confirmed
// directly needed by go-wasmcontainers' own guest containers (they declare
// shared memory locally, read it back via instance.exports.memory, and
// postMessage THAT to a second worker). Table and Global exports are
// omitted — see this file's own top doc comment.
func (b *binder) wasmExports(mod api.Module, compiled wazero.CompiledModule, isShared bool) *goja.Object {
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
			exp.Set(name, b.wrapWasmMemory(mem, isShared))
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
// `.buffer` is a REAL, zero-copy view over wazero's own backing bytes
// (NewArrayBuffer wraps the slice directly, no copy — see its own doc
// comment), re-fetched whenever the memory's size no longer matches the
// cached view's own length, since a successful memory.grow can relocate
// wazero's backing array for a NON-shared memory (Memory.Read's own doc
// comment: "any shared views from Read must be refreshed" after growing —
// for a SHARED memory wazero itself never relocates on grow, confirmed by
// reading internal/wasm/memory.go directly: it pre-allocates to the
// declared maximum up front for exactly this reason, but refreshing on
// every size change is harmless and keeps one code path for both kinds).
//
// isShared (from moduleHasSharedMemory, via wasmExports) decides the
// WRAPPING of those same bytes, not whether grow() itself works — a
// shared memory's `.buffer` is stamped SharedArrayBuffer-classed and
// registered (wrapSharedArrayBuffer/wasmMemoryRegistry) so it reports
// `instanceof SharedArrayBuffer` correctly and is postMessage-able, the
// SAME treatment the standalone constructor's own shared path gets
// (newWasmMemory). Every Memory wrapper this function builds — shared or
// not — is also registered in wasmMemoryRegistry (nil for non-shared) so
// clone.go's postMessage(memory) path recognizes BOTH this module-exported
// kind and the standalone constructor's kind identically.
func (b *binder) wrapWasmMemory(mem api.Memory, isShared bool) *goja.Object {
	o := b.vm.NewObject()
	_ = o.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue("WebAssembly.Memory"), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
	var cachedSize uint32 = 0xffffffff // force a fetch on first access
	var cachedBuf goja.Value
	var cachedSB *sharedBuffer
	refresh := func() goja.Value {
		size := mem.Size()
		if size != cachedSize {
			raw, _ := mem.Read(0, size)
			if isShared {
				cachedSB = &sharedBuffer{data: raw}
				cachedBuf = b.vm.ToValue(b.wrapSharedArrayBuffer(cachedSB))
			} else {
				cachedBuf = b.vm.ToValue(b.vm.NewArrayBuffer(raw))
			}
			cachedSize = size
		}
		return cachedBuf
	}
	b.accessor(o, "buffer", func() goja.Value { return refresh() }, nil)
	o.Set("grow", func(call goja.FunctionCall) goja.Value {
		delta := uint32(call.Argument(0).ToInteger())
		prev, ok := mem.Grow(delta)
		if !ok {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: would exceed the memory's maximum"))
		}
		return b.vm.ToValue(prev)
	})
	if isShared {
		// Force one refresh now, even if no script has read `.buffer`
		// yet, so cachedSB is populated before this Memory could possibly
		// be postMessage'd — nothing should depend on `.buffer` having
		// been read first.
		refresh()
	}
	registerWasmMemory(o, cachedSB)
	b.ownedWasmMemories = append(b.ownedWasmMemories, o)
	return o
}

// wasmMemoryRegistry maps each `new WebAssembly.Memory(...)` wrapper object
// (in ANY binder's VM) to the Go-level *sharedBuffer behind it, mirroring
// sharedBufferRegistry's (atomics.go) own by-object-identity design and for
// the same reason: a *goja.Object pointer is already globally unique for
// the life of the process, so one package-level map correctly serves every
// binder's own objects. The value is nil for a registered but NON-shared
// Memory — still present so clone.go can tell "a Memory wrapper this
// binding does not know how to clone" apart from "not a Memory wrapper at
// all" and report the precise reason (construct with shared:true) rather
// than a generic "unsupported object" error.
var (
	wasmMemoryRegistryMu sync.Mutex
	wasmMemoryRegistry   = map[*goja.Object]*sharedBuffer{}
)

func registerWasmMemory(obj *goja.Object, sb *sharedBuffer) {
	wasmMemoryRegistryMu.Lock()
	wasmMemoryRegistry[obj] = sb
	wasmMemoryRegistryMu.Unlock()
}

// lookupWasmMemory reports whether obj is a registered Memory wrapper at
// all (ok), and if so, its backing *sharedBuffer (nil for a non-shared one).
func lookupWasmMemory(obj *goja.Object) (sb *sharedBuffer, ok bool) {
	wasmMemoryRegistryMu.Lock()
	sb, ok = wasmMemoryRegistry[obj]
	wasmMemoryRegistryMu.Unlock()
	return sb, ok
}

// forgetWasmMemories removes every object this binder ever registered from
// the global registry — mirrors forgetSharedBuffers exactly, called from
// the same two places (Session.Close, stopWorkers).
func (b *binder) forgetWasmMemories() {
	if len(b.ownedWasmMemories) == 0 {
		return
	}
	wasmMemoryRegistryMu.Lock()
	for _, obj := range b.ownedWasmMemories {
		delete(wasmMemoryRegistry, obj)
	}
	wasmMemoryRegistryMu.Unlock()
	b.ownedWasmMemories = nil
}

// newWasmMemory implements the standalone `new WebAssembly.Memory(descriptor)`
// constructor (webassembly.github.io/spec/js-api/#dom-memory-memory, extended
// by the threads proposal's own `shared` field — webassembly.github.io/
// threads/js-api/#memories). Confirmed directly needed by
// go-wasmcontainers: every one of their guest containers constructs a
// shared Memory in one Worker before instantiating anything, then posts
// that SAME object to a second, plain-JS "engine worker" that services
// host calls by reading/writing its bytes directly while the guest blocks
// in Atomics.wait — without this constructor existing at all, every one of
// their containers fails before its first syscall. Deliberately does NOT
// go through wazero: a standalone Memory has no module to belong to (see
// this file's own top doc comment for wazero's confirmed lack of a
// memory-import API, which is the separate, NOT-yet-solved half of making
// an actual guest module use this Memory as its own linear memory).
func (b *binder) newWasmMemory(call goja.ConstructorCall) *goja.Object {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("WebAssembly.Memory: descriptor must be an object"))
	}
	initialV := desc.Get("initial")
	if initialV == nil || goja.IsUndefined(initialV) {
		panic(b.vm.NewTypeError("WebAssembly.Memory: descriptor.initial is required"))
	}
	initial := int(initialV.ToInteger())
	if initial < 0 || uint32(initial) > maxWasmMemoryPages {
		panic(b.wasmError("RangeError", "WebAssembly.Memory: initial is out of range"))
	}
	maxV := desc.Get("maximum")
	hasMax := maxV != nil && !goja.IsUndefined(maxV)
	maximum := initial
	if hasMax {
		maximum = int(maxV.ToInteger())
		if maximum < initial || uint32(maximum) > maxWasmMemoryPages {
			panic(b.wasmError("RangeError", "WebAssembly.Memory: maximum is out of range"))
		}
	}
	sharedV := desc.Get("shared")
	shared := sharedV != nil && sharedV.ToBoolean()
	if shared && !b.opt.CrossOriginIsolated {
		// Matches the SAME gate SharedArrayBuffer/Atomics are already
		// behind (atomics.go/window.go) — a real browser denies a shared
		// Memory exactly as it denies a bare SharedArrayBuffer without
		// COOP/COEP, and go-wasmcontainers' own confirmed usage expects
		// this engine to behave the same way, not as a more permissive
		// shortcut.
		panic(b.vm.NewTypeError("WebAssembly.Memory: shared memory requires a cross-origin isolated context"))
	}
	if shared && !hasMax {
		// Per spec, a shared memory's descriptor must declare a maximum —
		// and this binding's own reason agrees: a growable SharedArrayBuffer
		// here pre-allocates its full capacity up front (sharedBuffer's own
		// max field doc comment explains why), which requires knowing that
		// capacity at construction time.
		panic(b.vm.NewTypeError("WebAssembly.Memory: a shared memory requires descriptor.maximum"))
	}
	capPages := initial
	if hasMax {
		capPages = maximum
	}
	// Bound by the SAME configurable quota as a module's own linear memory
	// (Options.MaxWasmMemoryBytes) — a standalone Memory is no less able to
	// exhaust the host than a module-declared one.
	if uint32(capPages) > b.maxWasmMemoryPages() {
		panic(b.wasmError("RangeError", "WebAssembly.Memory: maximum exceeds this engine's configured memory quota"))
	}
	if shared {
		sb := newGrowableSharedBuffer(initial*wasmPageSize, maximum*wasmPageSize)
		return b.wrapSharedWasmMemory(sb)
	}
	return b.wrapNonSharedWasmMemory(initial*wasmPageSize, hasMax, maximum*wasmPageSize)
}

// wrapSharedWasmMemory builds a Memory wrapper around an EXISTING
// *sharedBuffer — the path both newWasmMemory (a freshly allocated sb) and
// reconstructValue (clone.go, an sb arriving via postMessage) share, so a
// Memory posted to a Worker and one constructed directly in it behave
// identically. `.buffer` is cached and re-wrapped only when sb's own
// length changed (sb.grow was called, by any binder, since the last
// read) — the same refresh-on-size-change idiom wrapWasmMemory already
// uses above, needed for the same reason: a fresh read must reflect a
// grow that happened elsewhere, but re-wrapping on every single read
// would otherwise leave an unbounded number of now-useless objects in
// sharedBufferRegistry for the life of the render.
func (b *binder) wrapSharedWasmMemory(sb *sharedBuffer) *goja.Object {
	o := b.vm.NewObject()
	_ = o.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue("WebAssembly.Memory"), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
	cachedLen := -1
	var cachedBuf *goja.Object
	refresh := func() *goja.Object {
		sb.mu.Lock()
		n := len(sb.data)
		sb.mu.Unlock()
		if n != cachedLen {
			cachedBuf = b.wrapSharedArrayBuffer(sb)
			cachedLen = n
		}
		return cachedBuf
	}
	b.accessor(o, "buffer", func() goja.Value { return refresh() }, nil)
	o.Set("grow", func(call goja.FunctionCall) goja.Value {
		delta := int(call.Argument(0).ToInteger())
		if delta < 0 {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: delta must not be negative"))
		}
		sb.mu.Lock()
		prevPages := len(sb.data) / wasmPageSize
		sb.mu.Unlock()
		if !sb.grow((prevPages + delta) * wasmPageSize) {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: would exceed the memory's maximum"))
		}
		return b.vm.ToValue(prevPages)
	})
	registerWasmMemory(o, sb)
	b.ownedWasmMemories = append(b.ownedWasmMemories, o)
	return o
}

// wrapNonSharedWasmMemory builds a standalone, non-shared Memory wrapper:
// `.buffer` is a REAL, zero-copy ArrayBuffer (the same wrapWasmMemory
// pattern above), replaced wholesale (realloc + copy) on grow — safe here
// specifically because a non-shared memory's bytes are never registered
// for postMessage (lookupWasmMemory reports sb==nil for this object), so
// no OTHER VM can hold a reference that reallocating would invalidate.
func (b *binder) wrapNonSharedWasmMemory(initialLen int, hasMax bool, maxLen int) *goja.Object {
	o := b.vm.NewObject()
	_ = o.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue("WebAssembly.Memory"), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
	data := make([]byte, initialLen)
	cachedLen := -1
	var cachedBuf goja.ArrayBuffer
	refresh := func() goja.ArrayBuffer {
		if len(data) != cachedLen {
			cachedBuf = b.vm.NewArrayBuffer(data)
			cachedLen = len(data)
		}
		return cachedBuf
	}
	b.accessor(o, "buffer", func() goja.Value { return b.vm.ToValue(refresh()) }, nil)
	o.Set("grow", func(call goja.FunctionCall) goja.Value {
		delta := int(call.Argument(0).ToInteger())
		if delta < 0 {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: delta must not be negative"))
		}
		prevPages := len(data) / wasmPageSize
		newLen := (prevPages + delta) * wasmPageSize
		if hasMax && newLen > maxLen {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: would exceed the memory's maximum"))
		}
		if uint32(newLen/wasmPageSize) > b.maxWasmMemoryPages() {
			panic(b.wasmError("RangeError", "WebAssembly.Memory.grow: would exceed this engine's configured memory quota"))
		}
		grown := make([]byte, newLen)
		copy(grown, data)
		data = grown
		return b.vm.ToValue(prevPages)
	})
	registerWasmMemory(o, nil)
	b.ownedWasmMemories = append(b.ownedWasmMemories, o)
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
