// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/dop251/goja"
	"github.com/go-gfx/wgsl"
	"github.com/go-macos/metal"

	"github.com/go-webengine/engine/wgslmsl"
)

// closeGPUDevices releases every *metal.Device this binder's own
// navigator.gpu.requestAdapter() calls created — a real GPU handle, unlike
// everything else a render allocates, so it must not leak across many
// renders in a long-lived host process. Called from Session.Close and
// stopWorkers; a no-op (nil gpuDevices) for the overwhelming majority of
// pages that never touch navigator.gpu.
func (b *binder) closeGPUDevices() {
	for _, dev := range b.gpuDevices {
		dev.Close()
	}
	b.gpuDevices = nil
}

// gpuScope is one open device.pushErrorScope(filter) call: the filter it
// was pushed with ("validation"/"out-of-memory"/"internal") and the
// first matching error it has captured, if any — per spec, a scope holds
// AT MOST one error; a later matching error while one is already
// captured is simply discarded, never overwrites it.
type gpuScope struct {
	filter   string
	captured goja.Value
}

// gpuErrorScopeState is one GPUDevice's own error-scope stack and
// "uncapturederror" listener — see gpuGuard's own doc comment for how an
// error actually lands here.
type gpuErrorScopeState struct {
	scopes            []*gpuScope
	onUncapturedError goja.Value
}

// gpuErrorProto returns (building once, lazily, per binder/VM and per
// class name) the .prototype every GPU error object of that class is
// stamped with — confirmed directly needed by go-wasmcontainers:
// go-gfx/webgpu reads `err.constructor.name` on whatever popErrorScope()
// resolves with to tell GPUValidationError/GPUOutOfMemoryError/
// GPUInternalError apart. These are never exposed as globals (no
// confirmed need for a script to call `new GPUValidationError(...)`
// itself — only to read one popErrorScope() already handed it), so the
// constructor function built here exists purely to give `.constructor`/
// `.constructor.name` the right identity, the same minimal-cost pattern
// wasmClassCtor (wasm.go) already established for WebAssembly.Module/
// Instance.
func (b *binder) gpuErrorProto(className string) *goja.Object {
	if p, ok := b.gpuErrorProtos[className]; ok {
		return p
	}
	if b.gpuErrorProtos == nil {
		b.gpuErrorProtos = map[string]*goja.Object{}
	}
	proto := b.vm.NewObject()
	ctor := b.vm.ToValue(func(goja.ConstructorCall) *goja.Object { return nil })
	if ctorObj, ok := ctor.(*goja.Object); ok {
		_ = ctorObj.DefineDataProperty("name", b.vm.ToValue(className), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	}
	_ = proto.DefineDataProperty("constructor", ctor, goja.FLAG_TRUE, goja.FLAG_FALSE, goja.FLAG_FALSE)
	_ = proto.DefineDataPropertySymbol(goja.SymToStringTag, b.vm.ToValue(className), goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_TRUE)
	b.gpuErrorProtos[className] = proto
	return proto
}

// newGPUError builds one GPUError-shaped object ({message}, prototyped
// so .constructor.name reads className).
func (b *binder) newGPUError(className, message string) *goja.Object {
	o := b.vm.NewObject()
	_ = o.SetPrototype(b.gpuErrorProto(className))
	o.Set("message", message)
	return o
}

func (b *binder) newGPUValidationError(message string) *goja.Object {
	return b.newGPUError("GPUValidationError", message)
}

// gpuValidationPanic wraps a GPU error object (and the error-scope filter
// class it belongs under — "validation" or "out-of-memory", WebGPU's own
// two concrete GPUError subtypes this binding ever generates) so
// gpuGuard's own recover can tell "generate a validation error, per
// WebGPU's own error model (§22)" apart from an ordinary thrown
// TypeError (a WebIDL-level argument-shape problem — wrong type, missing
// required field — which must still throw synchronously regardless of
// any open error scope; this binding keeps that distinction rather than
// routing every panic through the scope mechanism). Both are otherwise
// goja.Value, indistinguishable by type alone, which is why this exists
// as its own Go type instead of relying on a value comparison.
type gpuValidationPanic struct {
	err   *goja.Object
	class string
}

// panicValidation is what every call site that represents GPU CONTENT
// validation (as opposed to WebIDL argument misuse) panics with —
// confirmed directly needed by go-wasmcontainers' own three negative
// tests (bad WGSL, a dispatch missing a bind group for a declared
// @group, an out-of-range queue.writeBuffer): their own go-gfx/webgpu
// wraps calls in pushErrorScope("validation")/popErrorScope() and
// expects the captured error back, not a thrown exception that kills
// the whole guest.
func (b *binder) panicValidation(message string) {
	panic(gpuValidationPanic{err: b.newGPUValidationError(message), class: "validation"})
}

// panicOutOfMemory is panicValidation's own counterpart for a real
// allocation failure (createBuffer against go-macos/metal) — the OTHER
// concrete GPUError subtype a pushErrorScope("out-of-memory") is meant to
// catch. Routed through the SAME gpuGuard mechanism, matched against a
// scope's own filter by class, not lumped under "validation" just
// because no confirmed test exercises this one yet.
func (b *binder) panicOutOfMemory(message string) {
	panic(gpuValidationPanic{err: b.newGPUError("GPUOutOfMemoryError", message), class: "out-of-memory"})
}

// gpuDeliverError is gpuGuard's own scope-walking core, factored out so
// createShaderModule can reuse it directly: per WebGPU's own error model
// (§22 "Errors & Debugging"), caught is handed to the INNERMOST open
// error scope whose own filter matches class (walking outward — a scope
// with a non-matching filter lets it pass through to the next one),
// captured there only if that scope does not already hold an error; if no
// open scope matches at all, it fires "uncapturederror" on the device
// instead. Unlike gpuGuard's own caller, createShaderModule never panics
// to reach this — it calls it directly, since shader module creation must
// still return a real (if invalid) module object either way, never an
// invalidResult substitute (gpuCreateShaderModule's own doc comment).
func (b *binder) gpuDeliverError(st *gpuErrorScopeState, caught *goja.Object, class string) {
	if st == nil {
		return
	}
	for i := len(st.scopes) - 1; i >= 0; i-- {
		sc := st.scopes[i]
		if sc.filter != class {
			continue
		}
		if sc.captured == nil {
			sc.captured = caught
		}
		return
	}
	if st.onUncapturedError != nil {
		if fn, ok := goja.AssertFunction(st.onUncapturedError); ok {
			ev := b.newEvent("uncapturederror")
			ev.Set("error", caught)
			b.callSafely(fn, goja.Undefined(), ev)
		}
	}
}

// gpuGuard runs fn, catching ONLY a panicValidation-raised error (see its
// own doc comment for why that is the one panic shape this intercepts,
// never an ordinary TypeError or an unrelated Go panic, both of which
// propagate through unchanged — a real bug must still crash). Either way,
// fn's own call still "succeeds" as far as the script can tell —
// invalidResult stands in for whatever the real result would have been,
// matching spec's own "return an invalid object" contract (never a
// throw, never a rejected promise, for this error class specifically).
func (b *binder) gpuGuard(dev *metal.Device, invalidResult goja.Value, fn func() goja.Value) goja.Value {
	st := b.gpuErrorScopes[dev]
	var result goja.Value
	var caught *goja.Object
	var class string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			vp, ok := r.(gpuValidationPanic)
			if !ok {
				panic(r)
			}
			caught, class = vp.err, vp.class
		}()
		result = fn()
	}()
	if caught == nil {
		return result
	}
	b.gpuDeliverError(st, caught, class)
	return invalidResult
}

// gpuLimitDefaults is the WebGPU Specification's own "Limit default and
// better values" table (§3.6.2 Limits, W3C Working Draft read for this
// binding), used verbatim: this binding does not yet query Metal for the
// hardware's own per-limit capability, so it reports the spec's documented
// defaults rather than a guessed or fabricated number. Revisit once a real
// page's own requiredLimits actually needs a number this binding cannot
// honour.
var gpuLimitDefaults = map[string]int64{
	"maxTextureDimension1D":                     8192,
	"maxTextureDimension2D":                     8192,
	"maxTextureDimension3D":                     2048,
	"maxTextureArrayLayers":                     256,
	"maxBindGroups":                             4,
	"maxBindingsPerBindGroup":                   1000,
	"maxDynamicUniformBuffersPerPipelineLayout": 8,
	"maxDynamicStorageBuffersPerPipelineLayout": 4,
	"maxSampledTexturesPerShaderStage":          16,
	"maxSamplersPerShaderStage":                 16,
	"maxStorageBuffersPerShaderStage":           8,
	"maxStorageTexturesPerShaderStage":          4,
	"maxUniformBuffersPerShaderStage":           12,
	"maxUniformBufferBindingSize":               65536,
	"maxStorageBufferBindingSize":               134217728,
	"minUniformBufferOffsetAlignment":           256,
	"minStorageBufferOffsetAlignment":           256,
	"maxVertexBuffers":                          8,
	"maxBufferSize":                             268435456,
	"maxVertexAttributes":                       16,
	"maxVertexBufferArrayStride":                2048,
	"maxInterStageShaderVariables":              16,
	"maxColorAttachments":                       8,
	"maxColorAttachmentBytesPerSample":          32,
	"maxComputeWorkgroupStorageSize":            16384,
	"maxComputeInvocationsPerWorkgroup":         256,
	"maxComputeWorkgroupSizeX":                  256,
	"maxComputeWorkgroupSizeY":                  256,
	"maxComputeWorkgroupSizeZ":                  64,
	"maxComputeWorkgroupsPerDimension":          65535,
}

func (b *binder) newGPUSupportedLimits() *goja.Object {
	o := b.vm.NewObject()
	for name, v := range gpuLimitDefaults {
		o.Set(name, v)
	}
	return o
}

// newGPUSupportedFeatures returns an empty, setlike<DOMString>
// GPUSupportedFeatures (WebGPU §3.5): this binding does not yet implement
// any optional WebGPU feature (timestamp-query, shader-f16, …), so
// `adapter.features.has(x)` correctly reads false for every x rather than
// claiming something unimplemented. `values`/`keys`/`entries`/`forEach`/
// `[Symbol.iterator]` (confirmed directly needed: go-gfx/webgpu's own
// gpu_js.go reads `adapter.features`/`device.features` with `.values()`
// then manually drives the iterator) all correctly report EMPTY, matching
// `size === 0` — there is nothing to iterate, not a missing method.
func (b *binder) newGPUSupportedFeatures() *goja.Object {
	o := b.vm.NewObject()
	o.Set("size", 0)
	o.Set("has", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	empty := func(goja.FunctionCall) goja.Value { return b.newArrayIterator(nil) }
	o.Set("values", empty)
	o.Set("keys", empty)
	o.Set("entries", empty)
	o.Set("forEach", func(goja.FunctionCall) goja.Value { return goja.Undefined() })
	_ = o.DefineDataPropertySymbol(goja.SymIterator, b.vm.ToValue(empty), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	return o
}

// newGPUBufferUsage/newGPUMapMode build the GPUBufferUsage/GPUMapMode
// namespaces (WebGPU: `[Exposed=(Window, Worker)] namespace GPUBufferUsage
// { const GPUFlagsConstant MAP_READ = 0x0001; … }`), global objects
// alongside navigator.gpu rather than properties of it — installed only
// when Options.EnableWebGPU is set, the same gate as navigator.gpu itself.
func (b *binder) newGPUBufferUsage() *goja.Object {
	o := b.vm.NewObject()
	o.Set("MAP_READ", 0x0001)
	o.Set("MAP_WRITE", 0x0002)
	o.Set("COPY_SRC", 0x0004)
	o.Set("COPY_DST", 0x0008)
	o.Set("INDEX", 0x0010)
	o.Set("VERTEX", 0x0020)
	o.Set("UNIFORM", 0x0040)
	o.Set("STORAGE", 0x0080)
	o.Set("INDIRECT", 0x0100)
	o.Set("QUERY_RESOLVE", 0x0200)
	return o
}

func (b *binder) newGPUMapMode() *goja.Object {
	o := b.vm.NewObject()
	o.Set("READ", gpuMapModeRead)
	o.Set("WRITE", gpuMapModeWrite)
	return o
}

// newGPUShaderStage builds the GPUShaderStage namespace (WebGPU §3.3:
// `namespace GPUShaderStage { const GPUFlagsConstant VERTEX = 0x1; … }`) —
// confirmed directly needed by go-wasmcontainers: go-gfx/webgpu's own
// createBindGroupLayout descriptor construction reads
// GPUShaderStage.COMPUTE, which threw "GPUShaderStage is not defined"
// (the global did not exist at all, unlike its GPUBufferUsage/GPUMapMode
// siblings).
func (b *binder) newGPUShaderStage() *goja.Object {
	o := b.vm.NewObject()
	o.Set("VERTEX", 0x1)
	o.Set("FRAGMENT", 0x2)
	o.Set("COMPUTE", 0x4)
	return o
}

// gpuTextureUsage* are WebGPU's own GPUTextureUsage flag values (WebGPU
// §3.3: `namespace GPUTextureUsage { const GPUFlagsConstant COPY_SRC =
// 0x01; … }`), named here (rather than inlined) so gpuCreateTexture's own
// usage -> metal.TextureUsage mapping and newGPUTextureUsage's own object
// literal cannot drift apart.
const (
	gpuTextureUsageCopySrc          = 0x01
	gpuTextureUsageCopyDst          = 0x02
	gpuTextureUsageTextureBinding   = 0x04
	gpuTextureUsageStorageBinding   = 0x08
	gpuTextureUsageRenderAttachment = 0x10
)

// newGPUTextureUsage builds the GPUTextureUsage namespace — engine#415's
// own counterpart to newGPUBufferUsage, installed the same way (a global
// alongside navigator.gpu, gated on Options.EnableWebGPU).
func (b *binder) newGPUTextureUsage() *goja.Object {
	o := b.vm.NewObject()
	o.Set("COPY_SRC", gpuTextureUsageCopySrc)
	o.Set("COPY_DST", gpuTextureUsageCopyDst)
	o.Set("TEXTURE_BINDING", gpuTextureUsageTextureBinding)
	o.Set("STORAGE_BINDING", gpuTextureUsageStorageBinding)
	o.Set("RENDER_ATTACHMENT", gpuTextureUsageRenderAttachment)
	return o
}

// newGPU builds navigator.gpu (HTML/WebGPU: `[Exposed=(Window, Worker)]
// interface GPU`). Installed only when Options.EnableWebGPU is set — see
// its own doc comment for the opt-in rationale.
func (b *binder) newGPU() goja.Value {
	o := b.vm.NewObject()
	o.Set("requestAdapter", func(call goja.FunctionCall) goja.Value {
		return b.gpuRequestAdapter(call)
	})
	o.Set("getPreferredCanvasFormat", func(goja.FunctionCall) goja.Value {
		return b.vm.ToValue("bgra8unorm")
	})
	// wgslLanguageFeatures is a setlike<DOMString>; this binding declares
	// none (WGSLLanguageFeatures' spec entries are all optional extensions
	// wgslmsl does not implement — see its own doc comment for scope).
	features := b.vm.NewObject()
	features.Set("size", 0)
	features.Set("has", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
	o.Set("wgslLanguageFeatures", features)
	return o
}

// gpuRequestAdapter implements GPU.requestAdapter(options): per spec,
// this NEVER rejects — it resolves with null when no adapter is available
// (no usable GPU, or go-macos/metal's own ErrUnsupported on a non-darwin
// host, which it reports on every platform so this engine cross-builds
// without a build tag of its own — confirmed directly by cross-building
// this package for every GOARCH this repo's CI already checks), OR when
// options.forceFallbackAdapter is true: WebGPU §4.4.1 requires THAT to
// resolve with a software adapter "if one exists, else null" — never the
// hardware adapter instead. This binding has no software adapter at all
// (go-macos/metal only ever wraps the real device), so the answer is
// always null (confirmed directly needed: go-wasmcontainers' own
// TestBatchFallbackRefused expects null here, to then correctly observe
// "no adapter" rather than treating the real GPU as the forced fallback).
func (b *binder) gpuRequestAdapter(call goja.FunctionCall) goja.Value {
	promise, resolve, _ := b.vm.NewPromise()
	if opts, ok := call.Argument(0).(*goja.Object); ok {
		if ff := opts.Get("forceFallbackAdapter"); ff != nil && ff.ToBoolean() {
			_ = resolve(goja.Null())
			return b.vm.ToValue(promise)
		}
	}
	dev, err := metal.Default()
	if err != nil {
		_ = resolve(goja.Null())
		return b.vm.ToValue(promise)
	}
	b.gpuDevices = append(b.gpuDevices, dev)
	_ = resolve(b.newGPUAdapter(dev))
	return b.vm.ToValue(promise)
}

// newGPUAdapter wraps dev as a GPUAdapter. go-macos/metal has no separate
// adapter/device split (one *metal.Device is both) — this binding's own
// GPUAdapter and GPUDevice are therefore two distinct JS wrapper objects
// over the SAME underlying dev, matching the one real split that matters
// here: requestDevice() is spec'd one-time-per-adapter, which this binding
// enforces itself (requested tracks it) rather than relying on
// go-macos/metal to.
func (b *binder) newGPUAdapter(dev *metal.Device) *goja.Object {
	o := b.vm.NewObject()
	o.Set("features", b.newGPUSupportedFeatures())
	o.Set("limits", b.newGPUSupportedLimits())
	info := b.vm.NewObject()
	info.Set("vendor", "")
	info.Set("architecture", "")
	info.Set("device", dev.Name())
	info.Set("description", "")
	info.Set("isFallbackAdapter", false)
	o.Set("info", info)

	requested := false
	o.Set("requestDevice", func(call goja.FunctionCall) goja.Value {
		promise, resolve, reject := b.vm.NewPromise()
		if requested {
			_ = reject(b.domException("OperationError", "requestDevice: this adapter has already been consumed"))
			return b.vm.ToValue(promise)
		}
		requested = true
		_ = resolve(b.newGPUDevice(dev))
		return b.vm.ToValue(promise)
	})
	return o
}

// newGPUDevice wraps dev as a GPUDevice.
func (b *binder) newGPUDevice(dev *metal.Device) *goja.Object {
	o := b.vm.NewObject()
	o.Set("features", b.newGPUSupportedFeatures())
	o.Set("limits", b.newGPUSupportedLimits())

	lostPromise, lostResolve, _ := b.vm.NewPromise()
	destroyed := false
	o.Set("destroy", func(call goja.FunctionCall) goja.Value {
		if destroyed {
			return goja.Undefined()
		}
		destroyed = true
		// The real native resource, not just a JS-visible flag: per spec,
		// destroy() must free every resource the device holds, and this
		// device's own MTLDevice/MTLCommandQueue (allocated fresh by
		// requestAdapter/requestDevice, never cached or shared, see
		// gpuRequestAdapter's own doc comment) is the one unlike anything
		// else this engine allocates — an OS-limited handle, not memory a
		// GC will ever reclaim on its own. Before this fix, destroy() only
		// resolved the `lost` promise, a purely cosmetic signal nothing
		// else here ever checked (createBuffer/createShaderModule/etc. ran
		// against the device exactly as before): every requestDevice()
		// call leaked a full native device forever, bounded only by
		// Session.Close's own end-of-render closeGPUDevices sweep. A
		// workload issuing many short-lived devices in sequence (confirmed
		// directly: go-wasmcontainers' own ~123 ONNX models, each loading
		// a fresh graph/device and "destroying" it between models) leaked
		// that many live native devices simultaneously, and crashed deep
		// inside Apple's own Metal/Foundation frameworks once some
		// internal, OS-level resource ceiling was crossed -- a real
		// native-process crash this engine's own Go-level safety cannot
		// catch or convert into a graceful error, confirmed root cause of
		// a reported SIGSEGV two earlier, independently-real fixes (an
		// objc.AutoreleasePool gap, a worker-teardown race) did not
		// address because neither was this bug.
		dev.Close()
		for i, d := range b.gpuDevices {
			if d == dev {
				b.gpuDevices = append(b.gpuDevices[:i], b.gpuDevices[i+1:]...)
				break
			}
		}
		info := b.vm.NewObject()
		info.Set("reason", "destroyed")
		info.Set("message", "device was destroyed")
		_ = lostResolve(info)
		return goja.Undefined()
	})
	o.Set("lost", lostPromise)

	// pushErrorScope/popErrorScope/"uncapturederror" (WebGPU §22 "Errors &
	// Debugging"): a REAL error-scope stack, confirmed directly needed by
	// go-wasmcontainers — go-gfx/webgpu wraps every creation in
	// pushErrorScope("validation")/popErrorScope() and turns the popped
	// GPUValidationError's own message into a Go error, so their guest's
	// bad WGSL must come back as a captured error, not a thrown exception
	// that kills the worker (the original, simpler design here DID throw
	// synchronously for every GPU error, which this round corrects for
	// the specific error class WebGPU itself documents as "generate a
	// [validation] error", never a throw — see gpuGuard's own doc comment
	// for exactly which call sites changed and which stayed real
	// synchronous TypeErrors, a WebIDL-level distinction this binding
	// still keeps).
	errState := &gpuErrorScopeState{}
	if b.gpuErrorScopes == nil {
		b.gpuErrorScopes = map[*metal.Device]*gpuErrorScopeState{}
	}
	b.gpuErrorScopes[dev] = errState
	o.Set("pushErrorScope", func(call goja.FunctionCall) goja.Value {
		errState.scopes = append(errState.scopes, &gpuScope{filter: call.Argument(0).String()})
		return goja.Undefined()
	})
	o.Set("popErrorScope", func(call goja.FunctionCall) goja.Value {
		if len(errState.scopes) == 0 {
			return b.rejected(b.domException("OperationError", "popErrorScope: no matching pushErrorScope"))
		}
		n := len(errState.scopes) - 1
		sc := errState.scopes[n]
		errState.scopes = errState.scopes[:n]
		if sc.captured == nil {
			return b.resolved(goja.Null())
		}
		return b.resolved(sc.captured)
	})
	o.Set("addEventListener", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() == "uncapturederror" {
			errState.onUncapturedError = call.Argument(1)
		}
		return goja.Undefined()
	})
	o.Set("removeEventListener", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).String() == "uncapturederror" {
			errState.onUncapturedError = nil
		}
		return goja.Undefined()
	})

	o.Set("createBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuCreateBuffer(dev, call)
		})
	})

	o.Set("createShaderModule", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateShaderModule(dev, call)
	})
	o.Set("createComputePipeline", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuCreateComputePipeline(dev, call)
		})
	})
	o.Set("createComputePipelineAsync", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateComputePipelineAsync(dev, call)
	})
	o.Set("createBindGroupLayout", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateBindGroupLayout(call)
	})
	o.Set("createPipelineLayout", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreatePipelineLayout(call)
	})
	o.Set("createBindGroup", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateBindGroup(call)
	})
	o.Set("createCommandEncoder", func(call goja.FunctionCall) goja.Value {
		return b.wrapGPUCommandEncoder(&gpuCommandEncoderState{dev: dev})
	})
	o.Set("createTexture", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuCreateTexture(dev, call)
		})
	})
	o.Set("createRenderPipeline", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuCreateRenderPipeline(dev, call)
		})
	})

	queue := b.vm.NewObject()
	queue.Set("writeBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuQueueWriteBuffer(call)
		})
	})
	queue.Set("writeTexture", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuQueueWriteTexture(call)
		})
	})
	queue.Set("submit", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuQueueSubmit(dev, call)
		})
	})
	queue.Set("onSubmittedWorkDone", func(call goja.FunctionCall) goja.Value {
		// Nothing can EVER be pending when this resolves: go-macos/metal's
		// own Device.Run (which queue.submit uses, see gpuQueueSubmit)
		// blocks until the GPU has finished, so by the time a submit() call
		// returns, every unit of work it carried has already completed —
		// matching this whole binding's own "settles synchronously"
		// convention for every other Promise.
		return b.resolved(goja.Undefined())
	})
	o.Set("queue", queue)

	return o
}

// gpuBufferState is the Go-level state behind one GPUBuffer: the real
// Metal-backed memory, its own usage/size, mapping state machine, and every
// ArrayBuffer view getMappedRange has handed out (so unmap/destroy can
// Detach each — spec: "all the matching ArrayBuffer objects... are detached").
type gpuBufferState struct {
	buf       *metal.Buffer
	size      int
	usage     int
	mapState  string // "unmapped", "mapped" ("pending" never observed: mapAsync settles synchronously)
	mapMode   int
	mapOffset int
	mapSize   int
	views     []goja.ArrayBuffer
}

const (
	gpuMapModeRead  = 0x0001
	gpuMapModeWrite = 0x0002

	gpuBufferUsageMapRead  = 0x0001
	gpuBufferUsageMapWrite = 0x0002
)

// gpuCreateBuffer implements GPUDevice.createBuffer(descriptor).
func (b *binder) gpuCreateBuffer(dev *metal.Device, call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createBuffer: a GPUBufferDescriptor is required"))
	}
	sizeVal := desc.Get("size")
	if sizeVal == nil || goja.IsUndefined(sizeVal) {
		panic(b.vm.NewTypeError("createBuffer: size is required"))
	}
	usageVal := desc.Get("usage")
	if usageVal == nil || goja.IsUndefined(usageVal) {
		panic(b.vm.NewTypeError("createBuffer: usage is required"))
	}
	size := int(sizeVal.ToInteger())
	usage := int(usageVal.ToInteger())
	mappedAtCreation := optBool(desc, "mappedAtCreation")
	if size <= 0 {
		panic(b.vm.NewTypeError("createBuffer: size must be positive"))
	}
	if mappedAtCreation && size%4 != 0 {
		panic(b.vm.NewTypeError("createBuffer: mappedAtCreation requires size to be a multiple of 4"))
	}
	buf, err := dev.NewBuffer(size)
	if err != nil {
		b.panicOutOfMemory(err.Error())
	}
	clear(buf.Bytes()) // spec: device memory is allocated and zeroed
	st := &gpuBufferState{buf: buf, size: size, usage: usage, mapState: "unmapped"}
	if mappedAtCreation {
		st.mapState = "mapped"
		st.mapOffset, st.mapSize = 0, size
	}
	return b.wrapGPUBuffer(st)
}

// wrapGPUBuffer builds the JS GPUBuffer surface over st, registering it in
// b.gpuBuffers (lazily created) so gpuQueueWriteBuffer can recover the real
// Go-level state (the zero-copy backing memory, the size to bound-check
// against) from the JS object identity alone — the same by-identity
// registry shape atomics.go's sharedBufferRegistry uses, kept per-binder
// here since a GPUBuffer is never transferred across VMs the way a
// SharedArrayBuffer is.
func (b *binder) wrapGPUBuffer(st *gpuBufferState) *goja.Object {
	o := b.vm.NewObject()
	if b.gpuBuffers == nil {
		b.gpuBuffers = map[*goja.Object]*gpuBufferState{}
	}
	b.gpuBuffers[o] = st
	o.Set("size", st.size)
	o.Set("usage", st.usage)
	b.accessor(o, "mapState", func() goja.Value { return b.vm.ToValue(st.mapState) }, nil)

	o.Set("mapAsync", func(call goja.FunctionCall) goja.Value {
		return b.gpuBufferMapAsync(st, call)
	})
	o.Set("getMappedRange", func(call goja.FunctionCall) goja.Value {
		return b.gpuBufferGetMappedRange(st, call)
	})
	o.Set("unmap", func(call goja.FunctionCall) goja.Value {
		b.gpuBufferUnmap(st)
		return goja.Undefined()
	})
	o.Set("destroy", func(call goja.FunctionCall) goja.Value {
		b.gpuBufferUnmap(st)
		st.buf.Close()
		return goja.Undefined()
	})
	return o
}

func (b *binder) gpuBufferMapAsync(st *gpuBufferState, call goja.FunctionCall) goja.Value {
	promise, resolve, reject := b.vm.NewPromise()
	if st.mapState != "unmapped" {
		_ = reject(b.domException("OperationError", "mapAsync: buffer is not unmapped"))
		return b.vm.ToValue(promise)
	}
	mode := int(call.Argument(0).ToInteger())
	offset := 0
	if a := call.Argument(1); a != nil && !goja.IsUndefined(a) {
		offset = int(a.ToInteger())
	}
	size := st.size - offset
	if a := call.Argument(2); a != nil && !goja.IsUndefined(a) {
		size = int(a.ToInteger())
	}
	switch {
	case offset%8 != 0 || size%4 != 0:
		_ = reject(b.domException("OperationError", "mapAsync: offset must be a multiple of 8 and size a multiple of 4"))
	case offset < 0 || size < 0 || offset+size > st.size:
		_ = reject(b.domException("OperationError", "mapAsync: range out of bounds"))
	case mode == gpuMapModeRead && st.usage&gpuBufferUsageMapRead == 0:
		_ = reject(b.domException("OperationError", "mapAsync: buffer usage does not include MAP_READ"))
	case mode == gpuMapModeWrite && st.usage&gpuBufferUsageMapWrite == 0:
		_ = reject(b.domException("OperationError", "mapAsync: buffer usage does not include MAP_WRITE"))
	case mode != gpuMapModeRead && mode != gpuMapModeWrite:
		_ = reject(b.domException("OperationError", "mapAsync: mode must be exactly one of GPUMapMode.READ or GPUMapMode.WRITE"))
	default:
		st.mapState = "mapped"
		st.mapMode, st.mapOffset, st.mapSize = mode, offset, size
		_ = resolve(goja.Undefined())
	}
	return b.vm.ToValue(promise)
}

func (b *binder) gpuBufferGetMappedRange(st *gpuBufferState, call goja.FunctionCall) goja.Value {
	if st.mapState != "mapped" {
		panic(b.domException("OperationError", "getMappedRange: buffer is not mapped"))
	}
	offset := 0
	if a := call.Argument(0); a != nil && !goja.IsUndefined(a) {
		offset = int(a.ToInteger())
	}
	size := st.mapSize - (offset - st.mapOffset)
	if a := call.Argument(1); a != nil && !goja.IsUndefined(a) {
		size = int(a.ToInteger())
	}
	if offset%8 != 0 || size%4 != 0 || offset < st.mapOffset || offset+size > st.mapOffset+st.mapSize {
		panic(b.domException("OperationError", "getMappedRange: range outside the mapped range"))
	}
	// A real, zero-copy view over the Metal buffer's own unified memory —
	// writing here IS writing the GPU-visible bytes, matching
	// go-macos/metal's own documented no-upload advantage.
	ab := b.vm.NewArrayBuffer(st.buf.Bytes()[offset : offset+size])
	st.views = append(st.views, ab)
	return b.vm.ToValue(ab)
}

func (b *binder) gpuBufferUnmap(st *gpuBufferState) {
	for _, v := range st.views {
		v.Detach()
	}
	st.views = nil
	st.mapState = "unmapped"
}

// gpuQueueWriteBuffer implements GPUQueue.writeBuffer(buffer, bufferOffset,
// data, [dataOffset, [size]]) — a direct copy into the buffer's own
// zero-copy backing memory, valid at any time regardless of the buffer's
// own mapState (spec: this is the one way to write an unmapped buffer).
func (b *binder) gpuQueueWriteBuffer(call goja.FunctionCall) goja.Value {
	bufObj, _ := call.Argument(0).(*goja.Object)
	var st *gpuBufferState
	if bufObj != nil {
		st = b.gpuBuffers[bufObj]
	}
	if st == nil {
		panic(b.vm.NewTypeError("writeBuffer: expected a GPUBuffer"))
	}
	bufferOffset := int(call.Argument(1).ToInteger())
	data, ok := bufferSourceBytes(call.Argument(2))
	if !ok {
		panic(b.vm.NewTypeError("writeBuffer: expected an ArrayBuffer or typed array view"))
	}
	dataOffset := 0
	if a := call.Argument(3); a != nil && !goja.IsUndefined(a) {
		dataOffset = int(a.ToInteger())
	}
	size := len(data) - dataOffset
	if a := call.Argument(4); a != nil && !goja.IsUndefined(a) {
		size = int(a.ToInteger())
	}
	if bufferOffset < 0 || size < 0 || dataOffset < 0 || dataOffset+size > len(data) || bufferOffset+size > st.size {
		// A range/bounds problem is GPU CONTENT validation per spec
		// (WebGPU's own writeBuffer algorithm "generates a validation
		// error"), not a WebIDL argument-shape issue — confirmed directly
		// needed by go-wasmcontainers' own "out-of-range writes" negative
		// test, which expects this captured by an open error scope, not
		// thrown.
		b.panicValidation("writeBuffer: range out of bounds")
	}
	copy(st.buf.Bytes()[bufferOffset:bufferOffset+size], data[dataOffset:dataOffset+size])
	return goja.Undefined()
}

// gpuShaderModuleState is the Go-level state behind one GPUShaderModule:
// the WGSL source (kept, not just the translation, so createComputePipeline
// can re-translate selecting a DIFFERENT entry point — see its own doc
// comment), the translation this binder already ran eagerly at
// createShaderModule time against the module's own default entry-point
// selection, and — separately — whether the WGSL itself is genuinely
// invalid per spec (wgslErr; gpuCreateShaderModule's own doc comment for
// why that is NOT the same question as "did this translator's own
// narrower backend accept it").
type gpuShaderModuleState struct {
	src     string
	result  *wgslmsl.Result
	err     error
	wgslErr error
}

// gpuCreateShaderModule implements GPUDevice.createShaderModule(descriptor).
// Translation happens eagerly, right here — this binding settles everything
// synchronously — but per spec, shader module creation always succeeds
// with a USABLE (if invalid) module object; it never rejects or throws.
//
// Two genuinely different questions, kept separate (confirmed directly
// needed: go-wasmcontainers' own batch-shaders case feeds a module with a
// reserved word, `meta`, used as an identifier — real, invalid WGSL by
// spec, which every other engine refuses right here, not later):
//   - "Is this real WGSL at all?" — go-gfx/wgsl's own Validate (the CTS-
//     conformant frontend wgslmsl itself consumes, confirmed zero CTS
//     false rejects) answers this. A genuine WGSL error (a reserved word,
//     a syntax error, a type error) is a real WebGPU shader-creation
//     error — WebGPU §9.3: reported through the device's error scopes
//     (gpuDeliverError, same mechanism gpuGuard uses elsewhere) AT THIS
//     CALL, with the real line/column getCompilationInfo() needs, not
//     deferred to whenever a pipeline first tries to use this module.
//   - "Can THIS translator's own deliberately bounded backend run it?" —
//     wgslmsl.Translate answers this, independently. A module this
//     package doesn't support (a switch statement, a pointer parameter)
//     is genuinely valid WGSL a real browser would compile fine — so its
//     own failure here is NOT reported as a spec-level shader-creation
//     error; it stays exactly as before, surfaced only via
//     getCompilationInfo() (as a best-effort diagnostic, unpositioned)
//     or a later createComputePipeline throw.
func (b *binder) gpuCreateShaderModule(dev *metal.Device, call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createShaderModule: a GPUShaderModuleDescriptor is required"))
	}
	src := desc.Get("code").String()
	var wgslErr error
	if err := wgsl.Validate(src); err != nil {
		wgslErr = err
		b.gpuDeliverError(b.gpuErrorScopes[dev], b.newGPUValidationError("createShaderModule: "+err.Error()), "validation")
	}
	res, err := wgslmsl.Translate(src, wgslmsl.Options{})
	return b.wrapGPUShaderModule(&gpuShaderModuleState{src: src, result: res, err: err, wgslErr: wgslErr})
}

// wgslCompilationMessages builds getCompilationInfo()'s own messages array
// from a go-gfx/wgsl error (always *wgsl.Error or wgsl.ErrorList — Parse
// and Check's own only two failure shapes), each with the real source
// position Validate's own error already carries, rather than the
// unpositioned, best-effort single message a wgslmsl.Translate failure
// gets (gpuCreateShaderModule's own doc comment).
func (b *binder) wgslCompilationMessages(err error) []interface{} {
	var errs []*wgsl.Error
	switch e := err.(type) {
	case wgsl.ErrorList:
		errs = e
	case *wgsl.Error:
		errs = []*wgsl.Error{e}
	}
	messages := make([]interface{}, len(errs))
	for i, e := range errs {
		msg := b.vm.NewObject()
		msg.Set("message", e.Msg)
		msg.Set("type", "error")
		msg.Set("lineNum", e.Pos.Line)
		msg.Set("linePos", e.Pos.Col)
		msg.Set("offset", e.Pos.Offset)
		msg.Set("length", 0)
		messages[i] = msg
	}
	return messages
}

func (b *binder) wrapGPUShaderModule(st *gpuShaderModuleState) *goja.Object {
	o := b.vm.NewObject()
	o.Set("getCompilationInfo", func(call goja.FunctionCall) goja.Value {
		var messages []interface{}
		switch {
		case st.wgslErr != nil:
			messages = b.wgslCompilationMessages(st.wgslErr)
		case st.err != nil:
			msg := b.vm.NewObject()
			msg.Set("message", st.err.Error())
			msg.Set("type", "error")
			msg.Set("lineNum", 0)
			msg.Set("linePos", 0)
			msg.Set("offset", 0)
			msg.Set("length", 0)
			messages = append(messages, msg)
		}
		info := b.vm.NewObject()
		info.Set("messages", b.vm.NewArray(messages...))
		return b.resolved(info)
	})
	if b.gpuShaderModules == nil {
		b.gpuShaderModules = map[*goja.Object]*gpuShaderModuleState{}
	}
	b.gpuShaderModules[o] = st
	return o
}

// gpuComputePipelineState is the Go-level state behind one
// GPUComputePipeline: the compiled, ready-to-dispatch go-macos/metal
// pipeline and the exact wgslmsl.Result it was built from — dispatch-time
// code (gpuComputePassState's own dispatchWorkgroups) needs the latter's
// own Bindings (for the group/binding -> Metal buffer index map) and
// WorkgroupSize (to convert a workgroup COUNT into the total THREAD count
// go-macos/metal's own Dispatch expects — see dispatchWorkgroups' own doc
// comment for the one stated gap this causes).
type gpuComputePipelineState struct {
	pipe   *metal.Pipeline
	result *wgslmsl.Result
	// explicitLayouts is nil for a `layout: "auto"` pipeline (the existing
	// path: getBindGroupLayout(idx) synthesizes a fresh, pipeline-scoped
	// handle per index on demand). Non-nil for a pipeline built from an
	// explicit GPUPipelineLayout (gpuCreatePipelineLayout's own ordered
	// bindGroupLayouts) — getBindGroupLayout(idx) then returns the SAME
	// object identity the script passed into createPipelineLayout at that
	// index, matching spec (a non-auto pipeline's own layout is fixed,
	// not pipeline-specific).
	explicitLayouts []*goja.Object
}

// gpuBuildComputePipeline is createComputePipeline/createComputePipelineAsync's
// shared core: translate (or re-translate for an explicit entryPoint),
// compile through Metal, and resolve the pipeline's own layout — either
// `"auto"` (unchanged) or an explicit GPUPipelineLayout object from
// gpuCreatePipelineLayout (confirmed directly needed by go-wasmcontainers:
// their own batch compute path binds a loop-budget group next to the
// guest's own groups, which `layout: "auto"`'s single-pipeline inference
// cannot express).
//
// Two distinct failure shapes, deliberately NOT unified into one: a
// WebIDL-level argument-shape problem (missing descriptor, wrong object
// type) is RETURNED as a TypeError — the sync caller panics with it (a
// real synchronous throw, correct regardless of any open error scope)
// and the async caller rejects with it directly. A GPU CONTENT
// validation failure (bad WGSL, a shader Metal itself rejects) instead
// PANICS via panicValidation — confirmed directly needed by
// go-wasmcontainers' own "bad WGSL must come back as a captured error,
// not kill the worker": the sync caller runs inside gpuGuard (its own
// outer closure in newGPUDevice), which routes this panic through the
// device's error scopes; the async caller recovers it itself and
// rejects directly, bypassing scopes entirely — matching spec's own
// special-cased behaviour for createComputePipelineAsync specifically
// (its own promise reports a compile failure directly, never through
// pushErrorScope/popErrorScope).
func (b *binder) gpuBuildComputePipeline(dev *metal.Device, call goja.FunctionCall) (*gpuComputePipelineState, goja.Value) {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		return nil, b.vm.NewTypeError("createComputePipeline: a GPUComputePipelineDescriptor is required")
	}
	var explicitLayouts []*goja.Object
	layout := desc.Get("layout")
	if layoutObj, ok := layout.(*goja.Object); ok {
		layouts, found := b.gpuPipelineLayouts[layoutObj]
		if !found {
			return nil, b.vm.NewTypeError("createComputePipeline: layout must be \"auto\" or a GPUPipelineLayout from createPipelineLayout()")
		}
		explicitLayouts = layouts
	} else if layout == nil || layout.String() != "auto" {
		return nil, b.vm.NewTypeError("createComputePipeline: layout must be \"auto\" or a GPUPipelineLayout from createPipelineLayout()")
	}
	computeObj, _ := desc.Get("compute").(*goja.Object)
	if computeObj == nil {
		return nil, b.vm.NewTypeError("createComputePipeline: a compute stage (GPUProgrammableStage) is required")
	}
	moduleObj, _ := computeObj.Get("module").(*goja.Object)
	modState := b.gpuShaderModules[moduleObj]
	if modState == nil {
		return nil, b.vm.NewTypeError("createComputePipeline: compute.module must be a GPUShaderModule")
	}
	entryPoint := ""
	if ep := computeObj.Get("entryPoint"); ep != nil && !goja.IsUndefined(ep) {
		entryPoint = ep.String()
	}
	var result *wgslmsl.Result
	if entryPoint != "" {
		// Re-translate selecting the requested entry point by its WGSL
		// SOURCE name: the module may declare more than one compute entry
		// point, in which case createShaderModule's own eager translation
		// already failed with an ambiguity error (modState.err) — this is
		// precisely how a script resolves that ambiguity, so modState's
		// own cached result/error is deliberately NOT consulted here.
		r, err := wgslmsl.Translate(modState.src, wgslmsl.Options{EntryPoint: entryPoint})
		if err != nil {
			b.panicValidation("createComputePipeline: " + err.Error())
		}
		result = r
	} else if modState.err != nil {
		b.panicValidation("createComputePipeline: shader module failed to compile: " + modState.err.Error())
	} else {
		result = modState.result
	}
	// engine#447 review bug #5: wgslmsl itself legitimately accepts a
	// texture_2d<u32> handle resource in a @compute entry point (a direct
	// go-macos/metal caller can bind a real Metal texture at its
	// resolved index — see wgslmsl's own TestTranslateTextureLoadEndToEnd),
	// but THIS binding's own dispatch path cannot: gpuDispatchWorkgroups
	// treats every resolved Binding as a Metal buffer, and
	// gpuCreateBindGroup only ever accepts a GPUBufferBinding resource
	// (its own "samplers/textures are not supported" rejection below).
	// Left unrejected, a texture resource's Metal index — allocated from
	// the SEPARATE tr.textureCount counter inside wgslmsl, independent of
	// tr.bufferCount — collides with a real buffer resource assigned the
	// same index, and gpuDispatchWorkgroups' buffers map silently
	// delivers one resource's bytes under the other's binding instead of
	// failing. Named rejection here, exactly like
	// gpuCreateRenderPipeline's own "a shader resource is not supported"
	// check just below it.
	for _, bnd := range result.Bindings {
		if bnd.Kind == wgslmsl.BindingTexture {
			return nil, b.vm.NewTypeError("createComputePipeline: a texture resource (@group/@binding) is not supported in a compute pipeline")
		}
	}
	lib, err := dev.Compile(result.MSL)
	if err != nil {
		b.panicValidation("createComputePipeline: Metal rejected the generated shader: " + err.Error())
	}
	pipe, err := lib.Pipeline(result.EntryPointName)
	if err != nil {
		b.panicValidation(err.Error())
	}
	return &gpuComputePipelineState{pipe: pipe, result: result, explicitLayouts: explicitLayouts}, nil
}

// gpuCreateComputePipeline implements GPUDevice.createComputePipeline(descriptor).
// A real GPU-validation failure (panicValidation, inside
// gpuBuildComputePipeline) propagates past this function unchanged — its
// own caller (newGPUDevice's "createComputePipeline" closure) runs this
// inside gpuGuard, which is what actually routes it to an error scope.
func (b *binder) gpuCreateComputePipeline(dev *metal.Device, call goja.FunctionCall) goja.Value {
	st, errVal := b.gpuBuildComputePipeline(dev, call)
	if errVal != nil {
		panic(errVal)
	}
	return b.wrapGPUComputePipeline(st)
}

// gpuCreateComputePipelineAsync implements
// GPUDevice.createComputePipelineAsync(descriptor) — the SAME translate/
// compile work as the sync form (this binding settles everything
// synchronously; there is no real background compile to await), wrapped
// in an already-settled Promise instead of panicking. Deliberately does
// NOT run through gpuGuard/error scopes — per spec, this one call always
// reports a compile failure by rejecting ITS OWN promise directly, never
// through pushErrorScope/popErrorScope (see gpuBuildComputePipeline's
// own doc comment) — so a panicValidation from inside it is recovered
// HERE, locally, converting it straight to a rejection.
func (b *binder) gpuCreateComputePipelineAsync(dev *metal.Device, call goja.FunctionCall) goja.Value {
	var st *gpuComputePipelineState
	var errVal goja.Value
	var validationErr *goja.Object
	func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			vp, ok := r.(gpuValidationPanic)
			if !ok {
				panic(r)
			}
			validationErr = vp.err
		}()
		st, errVal = b.gpuBuildComputePipeline(dev, call)
	}()
	if validationErr != nil {
		return b.rejected(validationErr)
	}
	if errVal != nil {
		return b.rejected(errVal)
	}
	return b.resolved(b.wrapGPUComputePipeline(st))
}

func (b *binder) wrapGPUComputePipeline(st *gpuComputePipelineState) *goja.Object {
	o := b.vm.NewObject()
	o.Set("getBindGroupLayout", func(call goja.FunctionCall) goja.Value {
		idx := int(call.Argument(0).ToInteger())
		if st.explicitLayouts != nil {
			if idx < 0 || idx >= len(st.explicitLayouts) {
				panic(b.vm.NewTypeError("getBindGroupLayout: index out of range for this pipeline's explicit layout"))
			}
			return st.explicitLayouts[idx]
		}
		layoutObj := b.vm.NewObject()
		if b.gpuBindGroupLayouts == nil {
			b.gpuBindGroupLayouts = map[*goja.Object]*gpuBindGroupLayoutInfo{}
		}
		b.gpuBindGroupLayouts[layoutObj] = &gpuBindGroupLayoutInfo{pipeline: st, group: idx}
		return layoutObj
	})
	if b.gpuComputePipelines == nil {
		b.gpuComputePipelines = map[*goja.Object]*gpuComputePipelineState{}
	}
	b.gpuComputePipelines[o] = st
	return o
}

// gpuBindGroupLayoutInfo is what pipeline.getBindGroupLayout(index) (the
// "auto" path) or device.createBindGroupLayout() (the explicit path)
// returns a handle to. It carries no resource-type validation of its own
// either way — the one real check — that every binding a dispatch
// actually needs is present — happens at dispatchWorkgroups time, against
// wgslmsl.Result.Bindings directly, which is keyed by the WGSL source's
// own @group/@binding numbers (set at setBindGroup time), never by
// anything a GPUBindGroupLayout/GPUPipelineLayout object itself declares.
// pipeline/group are therefore only meaningful for an "auto" layout (kept
// for that path's own identity, even though dispatch does not read them
// either); an explicit layout (gpuCreateBindGroupLayout) leaves both zero.
type gpuBindGroupLayoutInfo struct {
	pipeline *gpuComputePipelineState
	group    int
}

// gpuCreateBindGroupLayout implements
// GPUDevice.createBindGroupLayout(descriptor) — confirmed directly needed
// by go-wasmcontainers for their own explicit-layout batch compute path.
// The descriptor's own entries (binding/visibility/buffer type) carry no
// enforcement here, for the SAME reason gpuBindGroupLayoutInfo's own doc
// comment gives: this binding validates resource bindings against the
// WGSL source itself, at dispatch time, not against anything a
// JS-constructed layout object declares — so a layout object's only real
// job, in this binding, is to be a recognizable handle createBindGroup()
// and createPipelineLayout() accept.
func (b *binder) gpuCreateBindGroupLayout(call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createBindGroupLayout: a GPUBindGroupLayoutDescriptor is required"))
	}
	layoutObj := b.vm.NewObject()
	if b.gpuBindGroupLayouts == nil {
		b.gpuBindGroupLayouts = map[*goja.Object]*gpuBindGroupLayoutInfo{}
	}
	b.gpuBindGroupLayouts[layoutObj] = &gpuBindGroupLayoutInfo{group: -1}
	return layoutObj
}

// gpuCreatePipelineLayout implements
// GPUDevice.createPipelineLayout(descriptor): descriptor.bindGroupLayouts
// is an ordered sequence of GPUBindGroupLayout objects (array position =
// @group number a pipeline built from this layout expects at that index —
// WebGPU §4.3), each of which must already be a real GPUBindGroupLayout
// this binding issued (createBindGroupLayout, or another pipeline's own
// getBindGroupLayout). Registered by object identity in
// b.gpuPipelineLayouts so createComputePipeline/createComputePipelineAsync
// can recognize it later (gpuBuildComputePipeline).
func (b *binder) gpuCreatePipelineLayout(call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createPipelineLayout: a GPUPipelineLayoutDescriptor is required"))
	}
	arr, _ := desc.Get("bindGroupLayouts").(*goja.Object)
	if arr == nil {
		panic(b.vm.NewTypeError("createPipelineLayout: bindGroupLayouts is required"))
	}
	n := int(arr.Get("length").ToInteger())
	layouts := make([]*goja.Object, n)
	for i := 0; i < n; i++ {
		layoutObj, _ := arr.Get(strconv.Itoa(i)).(*goja.Object)
		if layoutObj == nil || b.gpuBindGroupLayouts[layoutObj] == nil {
			panic(b.vm.NewTypeError("createPipelineLayout: bindGroupLayouts entries must be GPUBindGroupLayout objects"))
		}
		layouts[i] = layoutObj
	}
	pipelineLayoutObj := b.vm.NewObject()
	if b.gpuPipelineLayouts == nil {
		b.gpuPipelineLayouts = map[*goja.Object][]*goja.Object{}
	}
	b.gpuPipelineLayouts[pipelineLayoutObj] = layouts
	return pipelineLayoutObj
}

// gpuBoundBuffer is the resolved resource behind one GPUBindGroupEntry: the
// real buffer plus the byte range this particular binding covers — its own
// GPUBufferBinding.offset/size, defaulted and bounds-checked against the
// buffer's own size per WebGPU §8.2.1's "resides inside the buffer" rule,
// never the buffer's whole size by assumption. size is what
// gpuDispatchWorkgroups' own bounds-check sizes buffer must divide by
// bnd.ElemBytes — the whole reason this is its own type and not still
// *gpuBufferState: two bindings of the SAME buffer at different sub-ranges
// must report different sizes there.
type gpuBoundBuffer struct {
	buf    *metal.Buffer
	offset int
	size   int
}

// gpuBindGroupInfo is the Go-level state behind one GPUBindGroup: which
// @group(N) it was built for, and the real buffer (sub-)range bound at
// each WGSL `binding` index within it.
type gpuBindGroupInfo struct {
	group    int
	bindings map[int]gpuBoundBuffer
}

// gpuCreateBindGroup implements GPUDevice.createBindGroup(descriptor).
// Every entry's resource must be a `{buffer}` GPUBufferBinding (no
// sampler/texture/external-texture resources yet — the one resource kind
// wgslmsl's own storage-array-only scope ever needs); offset/size may now
// describe any sub-range that resides inside the buffer (WebGPU §8.2.1),
// not only the whole thing.
func (b *binder) gpuCreateBindGroup(call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createBindGroup: a GPUBindGroupDescriptor is required"))
	}
	layoutObj, _ := desc.Get("layout").(*goja.Object)
	info := b.gpuBindGroupLayouts[layoutObj]
	if info == nil {
		panic(b.vm.NewTypeError("createBindGroup: layout must come from pipeline.getBindGroupLayout()"))
	}
	entriesObj, _ := desc.Get("entries").(*goja.Object)
	if entriesObj == nil {
		panic(b.vm.NewTypeError("createBindGroup: entries is required"))
	}
	n := int(entriesObj.Get("length").ToInteger())
	bindings := map[int]gpuBoundBuffer{}
	for i := 0; i < n; i++ {
		entry, _ := entriesObj.Get(strconv.Itoa(i)).(*goja.Object)
		if entry == nil {
			panic(b.vm.NewTypeError("createBindGroup: entries must be GPUBindGroupEntry objects"))
		}
		bindingIdx := int(entry.Get("binding").ToInteger())
		resObj, _ := entry.Get("resource").(*goja.Object)
		if resObj == nil {
			panic(unsupportedJS(b, "createBindGroup: entry.resource must be a GPUBufferBinding ({buffer}) — samplers/textures are not supported"))
		}
		bufObj, _ := resObj.Get("buffer").(*goja.Object)
		st := b.gpuBuffers[bufObj]
		if st == nil {
			panic(b.vm.NewTypeError("createBindGroup: entry.resource.buffer must be a GPUBuffer"))
		}
		offset := 0
		if off := resObj.Get("offset"); off != nil && !goja.IsUndefined(off) {
			offset = int(off.ToInteger())
		}
		size := st.size - offset
		if sz := resObj.Get("size"); sz != nil && !goja.IsUndefined(sz) {
			size = int(sz.ToInteger())
		}
		if offset < 0 || size <= 0 || offset+size > st.size {
			panic(b.vm.NewTypeError("createBindGroup: entry.resource.offset/size must describe a non-empty range that resides inside the buffer"))
		}
		// WebGPU §4.2.1: every storage-buffer binding's offset must be a
		// multiple of minStorageBufferOffsetAlignment, and its effective
		// size a multiple of 4 — this binding's only buffer-resource kind
		// is a storage array (createBindGroup's own doc comment), so both
		// apply unconditionally here.
		if align := int(gpuLimitDefaults["minStorageBufferOffsetAlignment"]); offset%align != 0 {
			panic(unsupportedJS(b, fmt.Sprintf("createBindGroup: entry.resource.offset must be a multiple of %d (minStorageBufferOffsetAlignment)", align)))
		}
		if size%4 != 0 {
			panic(unsupportedJS(b, "createBindGroup: entry.resource.size must be a multiple of 4"))
		}
		bindings[bindingIdx] = gpuBoundBuffer{buf: st.buf, offset: offset, size: size}
	}
	bgObj := b.vm.NewObject()
	if b.gpuBindGroups == nil {
		b.gpuBindGroups = map[*goja.Object]*gpuBindGroupInfo{}
	}
	b.gpuBindGroups[bgObj] = &gpuBindGroupInfo{group: info.group, bindings: bindings}
	return bgObj
}

// gpuCommandEncoderState is the Go-level state behind one
// GPUCommandEncoder/GPUCommandBuffer — this binding uses the SAME Go value
// for both (finish() just stops further recording and hands back a new JS
// object identity over the identical state), since nothing here needs them
// to be distinct beyond that. Deliberately scoped to AT MOST ONE pass per
// encoder, compute OR render (engine#415 M7 added the render half) — the
// realistic shape every test and real usage needs so far; a second
// beginComputePass()/beginRenderPass() is a clear, named rejection, not a
// silent no-op.
type gpuCommandEncoderState struct {
	dev       *metal.Device
	passOpen  bool
	passEnded bool
	finished  bool
	commands  []gpuRecordedCommand
}

// gpuRecordedCommand is one command recorded on a GPUCommandEncoder, IN
// THE EXACT ORDER it was encoded — dispatchWorkgroups (inside a compute
// pass) and copyBufferToBuffer (directly on the encoder, confirmed
// directly needed by go-wasmcontainers for their own readback-via-
// mapAsync batch path) interleave freely per spec, so both go through
// this ONE ordered list rather than two separate ones gpuQueueSubmit
// would otherwise have to merge by guesswork. engine#415 M7 added render
// and textureCopy alongside the original dispatch/copy — exactly one of
// the four is non-nil.
type gpuRecordedCommand struct {
	dispatch    *gpuRecordedDispatch
	copy        *gpuRecordedCopy
	render      *gpuRecordedRenderPass
	textureCopy *gpuRecordedTextureCopy
}

// gpuRecordedDispatch is one dispatchWorkgroups() call, fully resolved at
// the moment it was recorded: every WGSL resource binding the pipeline
// needs is already mapped to its real *metal.Buffer and byte offset
// (including the synthesised bounds-checking sizes buffer, sizesBuf, freed
// once gpuQueueSubmit's own dev.Run has finished using it, always at
// offset 0 since it is allocated fresh here, never user-visible), and the
// workgroup count has already been converted to the TOTAL thread count
// go-macos/metal's own Dispatch expects.
type gpuRecordedDispatch struct {
	pipeline *gpuComputePipelineState
	buffers  map[int]gpuBoundBuffer
	sizesBuf *metal.Buffer
	x, y, z  int
}

// gpuRecordedCopy is one copyBufferToBuffer() call. Executed as a plain
// Go `copy()` over go-macos/metal's own zero-copy unified-memory backing
// arrays — correct ONLY once every dispatch recorded BEFORE it has
// actually finished running on the GPU, which is why gpuQueueSubmit does
// NOT batch every command into one dev.Run call: it flushes (and BLOCKS
// on, per dev.Run's own documented behaviour) every pending dispatch
// before executing a copy, so a copy reading a dispatch's own output
// always sees the real, GPU-written bytes, never a stale pre-dispatch
// buffer the naive "one dev.Run for everything" shape would risk (Go's
// own copy() runs at CLOSURE-RECORDING time, not at the GPU's own command-
// buffer-execution time, if it were interleaved inside a single dev.Run
// closure instead).
type gpuRecordedCopy struct {
	src, dst             *metal.Buffer
	srcOffset, dstOffset int
	size                 int
}

func (b *binder) wrapGPUCommandEncoder(st *gpuCommandEncoderState) *goja.Object {
	o := b.vm.NewObject()
	o.Set("beginComputePass", func(call goja.FunctionCall) goja.Value {
		if st.finished {
			panic(b.vm.NewTypeError("beginComputePass: this command encoder has already finished"))
		}
		if st.passOpen || st.passEnded {
			panic(unsupportedJS(b, "beginComputePass: only one compute pass per command encoder is supported"))
		}
		st.passOpen = true
		return b.wrapGPUComputePassEncoder(st)
	})
	o.Set("copyBufferToBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuCopyBufferToBuffer(st, call)
	})
	o.Set("beginRenderPass", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(st.dev, goja.Undefined(), func() goja.Value {
			return b.gpuBeginRenderPass(st, call)
		})
	})
	o.Set("copyTextureToBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(st.dev, goja.Undefined(), func() goja.Value {
			return b.gpuCopyTextureToBuffer(st, call)
		})
	})
	o.Set("finish", func(call goja.FunctionCall) goja.Value {
		if st.finished {
			panic(b.vm.NewTypeError("finish: this command encoder has already finished"))
		}
		// engine#447 review bug #7: a pass begun but never end()ed must
		// not be silently dropped — every dispatch/draw recorded against
		// it is either already in st.commands (dispatchWorkgroups) or
		// still sitting unflushed in the render pass's own state.draws,
		// about to be lost the moment finish() hands back a command
		// buffer with no reference to it at all.
		if st.passOpen {
			panic(b.vm.NewTypeError("finish: a pass (compute or render) is still open"))
		}
		st.finished = true
		cbObj := b.vm.NewObject()
		if b.gpuCommandBuffers == nil {
			b.gpuCommandBuffers = map[*goja.Object]*gpuCommandEncoderState{}
		}
		b.gpuCommandBuffers[cbObj] = st
		return cbObj
	})
	return o
}

// wrapGPUComputePassEncoder builds the GPUComputePassEncoder surface. Bind
// state (the current pipeline and every bind group set so far) persists
// across multiple dispatchWorkgroups() calls within the SAME pass, exactly
// as the spec requires — setPipeline/setBindGroup only ever update this
// closure-captured state, and dispatchWorkgroups reads whatever is current
// at the moment it is called.
func (b *binder) wrapGPUComputePassEncoder(enc *gpuCommandEncoderState) *goja.Object {
	var curPipeline *gpuComputePipelineState
	curBindGroups := map[int]*gpuBindGroupInfo{}

	o := b.vm.NewObject()
	o.Set("setPipeline", func(call goja.FunctionCall) goja.Value {
		pObj, _ := call.Argument(0).(*goja.Object)
		p := b.gpuComputePipelines[pObj]
		if p == nil {
			panic(b.vm.NewTypeError("setPipeline: expected a GPUComputePipeline"))
		}
		curPipeline = p
		return goja.Undefined()
	})
	o.Set("setBindGroup", func(call goja.FunctionCall) goja.Value {
		idx := int(call.Argument(0).ToInteger())
		bgObj, _ := call.Argument(1).(*goja.Object)
		bg := b.gpuBindGroups[bgObj]
		if bg == nil {
			panic(b.vm.NewTypeError("setBindGroup: expected a GPUBindGroup"))
		}
		curBindGroups[idx] = bg
		return goja.Undefined()
	})
	o.Set("dispatchWorkgroups", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(enc.dev, goja.Undefined(), func() goja.Value {
			// engine#447 review bug #7: dispatchWorkgroups records
			// directly into enc.commands (gpuDispatchWorkgroups's own
			// append), so unlike a render pass's draws a call after
			// end() would not just be ignored — it would still run,
			// against an encoder whose pass has already been handed to
			// gpuQueueSubmit's own command list.
			if !enc.passOpen {
				panic(b.vm.NewTypeError("dispatchWorkgroups: this pass has already ended"))
			}
			return b.gpuDispatchWorkgroups(enc, curPipeline, curBindGroups, call)
		})
	})
	o.Set("end", func(call goja.FunctionCall) goja.Value {
		// engine#447 review bug #7: without this, end() called twice on
		// the same pass is silently accepted both times (passOpen/
		// passEnded already both idempotent-looking booleans), matching
		// exactly the named "end() twice re-runs the pass" defect for
		// the render-pass encoder just below.
		if !enc.passOpen {
			panic(b.vm.NewTypeError("end: this pass has already ended"))
		}
		enc.passOpen = false
		enc.passEnded = true
		return goja.Undefined()
	})
	return o
}

// gpuDispatchWorkgroups implements GPUComputePassEncoder.dispatchWorkgroups.
//
// The one stated, go-macos/metal-level gap: go-macos/metal's own Dispatch
// takes a TOTAL thread-grid size and picks the threadgroup SHAPE itself
// (from the hardware's own reported limits), with no way for this binding
// to force it to match the WGSL shader's own declared @workgroup_size. This
// is harmless for the realistic subset wgslmsl itself supports today (a
// kernel keyed purely off global_invocation_id — confirmed correct
// end-to-end by wgslmsl's own tests, since dispatchThreads semantics always
// gives the right GLOBAL thread position regardless of internal grouping):
// but local_invocation_id/local_invocation_index/workgroup_id, though
// accepted as entry-point parameters, may not reflect the actually-executed
// threadgroup shape when it differs from the WGSL source's own declared
// size — stated here rather than silently wrong, since go-macos/metal has
// no lower-level dispatch call this binding could use instead.
func (b *binder) gpuDispatchWorkgroups(enc *gpuCommandEncoderState, pipeline *gpuComputePipelineState, bindGroups map[int]*gpuBindGroupInfo, call goja.FunctionCall) goja.Value {
	if pipeline == nil {
		b.panicValidation("dispatchWorkgroups: no pipeline is set")
	}
	x := int(call.Argument(0).ToInteger())
	y := 1
	if a := call.Argument(1); a != nil && !goja.IsUndefined(a) {
		y = int(a.ToInteger())
	}
	z := 1
	if a := call.Argument(2); a != nil && !goja.IsUndefined(a) {
		z = int(a.ToInteger())
	}
	ws := pipeline.result.WorkgroupSize

	buffers := map[int]gpuBoundBuffer{}
	for _, bnd := range pipeline.result.Bindings {
		bg, ok := bindGroups[bnd.Group]
		if !ok {
			b.panicValidation(fmt.Sprintf("dispatchWorkgroups: no bind group set for @group(%d)", bnd.Group))
		}
		bst, ok := bg.bindings[bnd.BindingIndex]
		if !ok {
			b.panicValidation(fmt.Sprintf("dispatchWorkgroups: bind group for @group(%d) has no binding for @binding(%d)", bnd.Group, bnd.BindingIndex))
		}
		buffers[bnd.MetalIndex] = bst
	}
	var sizesBuf *metal.Buffer
	if pipeline.result.SizeMetalIndex >= 0 {
		sb, err := enc.dev.NewBuffer(4 * pipeline.result.SizesCount)
		if err != nil {
			b.panicValidation(err.Error())
		}
		for _, bnd := range pipeline.result.Bindings {
			if bnd.Kind != wgslmsl.BindingStorageArray {
				continue // only an indexed storage array needs a bounds-check slot
			}
			bst := bindGroups[bnd.Group].bindings[bnd.BindingIndex]
			count := uint32(bst.size / bnd.ElemBytes)
			binary.LittleEndian.PutUint32(sb.Bytes()[bnd.SizesIndex*4:], count)
		}
		buffers[pipeline.result.SizeMetalIndex] = gpuBoundBuffer{buf: sb, size: 4 * pipeline.result.SizesCount}
		sizesBuf = sb
	}
	enc.commands = append(enc.commands, gpuRecordedCommand{dispatch: &gpuRecordedDispatch{
		pipeline: pipeline,
		buffers:  buffers,
		sizesBuf: sizesBuf,
		x:        x * ws[0],
		y:        y * ws[1],
		z:        z * ws[2],
	}})
	return goja.Undefined()
}

// gpuCopyBufferToBuffer implements
// GPUCommandEncoder.copyBufferToBuffer(source, sourceOffset, destination,
// destinationOffset, size) — confirmed directly needed by
// go-wasmcontainers for their own batch compute readback path (a storage
// buffer's result copied into a MAP_READ buffer, then read back via
// mapAsync/getMappedRange, since a buffer used as a compute shader
// storage target cannot ALSO be directly mapped). Offsets/size must each
// be a multiple of 4 per spec. Recorded into enc.commands, not copied
// immediately — see gpuRecordedCopy's own doc comment for why ordering
// relative to dispatches matters and how gpuQueueSubmit preserves it.
func (b *binder) gpuCopyBufferToBuffer(enc *gpuCommandEncoderState, call goja.FunctionCall) goja.Value {
	if enc.finished {
		panic(b.vm.NewTypeError("copyBufferToBuffer: this command encoder has already finished"))
	}
	srcObj, _ := call.Argument(0).(*goja.Object)
	srcSt := b.gpuBuffers[srcObj]
	if srcSt == nil {
		panic(b.vm.NewTypeError("copyBufferToBuffer: source must be a GPUBuffer"))
	}
	srcOffset := int(call.Argument(1).ToInteger())
	dstObj, _ := call.Argument(2).(*goja.Object)
	dstSt := b.gpuBuffers[dstObj]
	if dstSt == nil {
		panic(b.vm.NewTypeError("copyBufferToBuffer: destination must be a GPUBuffer"))
	}
	dstOffset := int(call.Argument(3).ToInteger())
	size := int(call.Argument(4).ToInteger())
	if srcOffset%4 != 0 || dstOffset%4 != 0 || size%4 != 0 {
		panic(b.vm.NewTypeError("copyBufferToBuffer: sourceOffset/destinationOffset/size must each be a multiple of 4"))
	}
	if srcOffset < 0 || dstOffset < 0 || size < 0 || srcOffset+size > srcSt.size || dstOffset+size > dstSt.size {
		panic(b.vm.NewTypeError("copyBufferToBuffer: range out of bounds"))
	}
	enc.commands = append(enc.commands, gpuRecordedCommand{copy: &gpuRecordedCopy{
		src: srcSt.buf, dst: dstSt.buf, srcOffset: srcOffset, dstOffset: dstOffset, size: size,
	}})
	return goja.Undefined()
}

// gpuQueueSubmit implements GPUQueue.submit(commandBuffers). Every
// recorded command, across every command buffer given, replays in ORDER
// — but NOT as one single dev.Run call spanning everything: a run of
// consecutive dispatches is flushed through dev.Run (which BLOCKS until
// the GPU has actually finished — see onSubmittedWorkDone's own doc
// comment for why that makes it correct to resolve immediately) before
// each copyBufferToBuffer, so a copy reading a dispatch's own output
// always sees the real, post-dispatch bytes (see gpuRecordedCopy's own
// doc comment for why batching a copy INSIDE the same dev.Run as the
// dispatch it depends on would be a real, silent correctness bug, not
// just a style choice).
func (b *binder) gpuQueueSubmit(dev *metal.Device, call goja.FunctionCall) goja.Value {
	arr, _ := call.Argument(0).(*goja.Object)
	if arr == nil {
		panic(b.vm.NewTypeError("submit: expected a sequence of GPUCommandBuffer"))
	}
	n := int(arr.Get("length").ToInteger())
	var commands []gpuRecordedCommand
	for i := 0; i < n; i++ {
		cbObj, _ := arr.Get(strconv.Itoa(i)).(*goja.Object)
		cb := b.gpuCommandBuffers[cbObj]
		if cb == nil {
			panic(b.vm.NewTypeError("submit: expected a GPUCommandBuffer"))
		}
		commands = append(commands, cb.commands...)
	}
	var pending []gpuRecordedDispatch
	runPending := func() error {
		if len(pending) == 0 {
			return nil
		}
		err := dev.Run(func(e *metal.Encoder) {
			for _, d := range pending {
				e.Use(d.pipeline.pipe)
				for idx, bb := range d.buffers {
					e.BufferAt(idx, bb.buf, bb.offset)
				}
				e.Dispatch(d.x, d.y, d.z)
			}
		})
		for _, d := range pending {
			if d.sizesBuf != nil {
				d.sizesBuf.Close()
			}
		}
		pending = nil
		return err
	}
	for _, c := range commands {
		if c.dispatch != nil {
			pending = append(pending, *c.dispatch)
			continue
		}
		if err := runPending(); err != nil {
			b.panicValidation(err.Error())
		}
		switch {
		case c.copy != nil:
			copy(c.copy.dst.Bytes()[c.copy.dstOffset:c.copy.dstOffset+c.copy.size],
				c.copy.src.Bytes()[c.copy.srcOffset:c.copy.srcOffset+c.copy.size])
		case c.render != nil:
			err := dev.RunRender(metal.RenderPassDescriptor{
				Target: c.render.target,
				Load:   c.render.load,
				Store:  c.render.store,
				Clear:  c.render.clear,
			}, func(e *metal.RenderEncoder) {
				for _, d := range c.render.draws {
					e.Use(d.pipeline)
					for idx, vb := range d.vertexBuffers {
						e.VertexBuffer(idx, vb.buf, vb.offset)
					}
					if d.indexed {
						e.DrawIndexed(d.indexCount, d.instanceCount, d.indexBuffer, d.indexBufferOffset, d.baseVertex, d.firstInstance)
					} else {
						e.Draw(d.vertexCount, d.instanceCount, d.firstVertex, d.firstInstance)
					}
				}
			})
			if err != nil {
				b.panicValidation(err.Error())
			}
		case c.textureCopy != nil:
			tc := c.textureCopy
			if err := tc.tex.GetBytes(tc.dst.Bytes()[tc.dstOffset : tc.dstOffset+tc.size]); err != nil {
				b.panicValidation(err.Error())
			}
		}
	}
	if err := runPending(); err != nil {
		b.panicValidation(err.Error())
	}
	return goja.Undefined()
}

// unsupportedJS reports a construct this binding's own deliberately bounded
// WebGPU scope does not (yet) support, matching wgslmsl's own "clear, named
// rejection, never a silently wrong or partial translation" convention.
func unsupportedJS(b *binder, msg string) goja.Value {
	return b.vm.NewTypeError("webgpu: unsupported: " + msg)
}

// ---- engine#415 M7: textures and render pipelines ----

// gpuTextureState is the Go-level state behind one GPUTexture: the real
// Metal-backed 2D texture and the descriptor fields a GPUTexture's own
// property getters report back. Deliberately bounded scope (see
// gpuCreateTexture's own doc comment for exactly where each restriction
// comes from): a single 2D texture, one mip level, one array layer,
// go-macos/metal's own PixelFormatR8Uint.
type gpuTextureState struct {
	tex    *metal.Texture
	width  int
	height int
	format string
	usage  int
}

// gpuExtent is a resolved GPUExtent3D (`{width, height,
// depthOrArrayLayers}` or the equivalent `[width, height,
// depthOrArrayLayers]` sequence form — WebGPU lets either through its own
// dictionary/union coercion) — shared by createTexture's own `size`,
// writeTexture's own `size`, and copyTextureToBuffer's own `copySize`, so
// the two argument shapes are resolved in exactly one place.
type gpuExtent struct{ w, h, d int }

// gpuGetInt reads an integer property that may be legitimately absent —
// an optional dictionary member (GPUExtent3DDict.height/depthOrArrayLayers
// both default to 1 per spec) or a short GPUExtent3D/GPUOrigin3D sequence
// form. goja's own Object.Get returns a genuine Go nil for a missing
// property (as opposed to goja.Undefined(), a non-nil Value, for one
// explicitly set to undefined) — calling ToInteger() on that nil
// interface panics, so every spec-optional numeric read goes through
// this instead of a raw obj.Get(key).ToInteger().
func gpuGetInt(obj *goja.Object, key string, def int) int {
	v := obj.Get(key)
	if v == nil || goja.IsUndefined(v) {
		return def
	}
	return int(v.ToInteger())
}

// gpuGetFloat is gpuGetInt's float counterpart, for GPUColor's own r/g/b/a.
func gpuGetFloat(obj *goja.Object, key string, def float64) float64 {
	v := obj.Get(key)
	if v == nil || goja.IsUndefined(v) {
		return def
	}
	return v.ToFloat()
}

func gpuReadExtent(v goja.Value) gpuExtent {
	obj, ok := v.(*goja.Object)
	if !ok {
		return gpuExtent{}
	}
	if w := obj.Get("width"); w != nil && !goja.IsUndefined(w) {
		return gpuExtent{
			w: int(w.ToInteger()),
			h: gpuGetInt(obj, "height", 1),
			d: gpuGetInt(obj, "depthOrArrayLayers", 1),
		}
	}
	return gpuExtent{
		w: gpuGetInt(obj, "0", 0),
		h: gpuGetInt(obj, "1", 1),
		d: gpuGetInt(obj, "2", 1),
	}
}

// gpuIsZeroOrigin reports whether v (a GPUOrigin3D: `{x, y, z}` or
// `[x, y, z]`, every field defaulting to 0) is the all-zero origin — the
// only one writeTexture/copyTextureToBuffer accept.
func gpuIsZeroOrigin(v goja.Value) bool {
	obj, ok := v.(*goja.Object)
	if !ok {
		return true
	}
	get := func(k string) int64 {
		x := obj.Get(k)
		if x == nil || goja.IsUndefined(x) {
			return 0
		}
		return x.ToInteger()
	}
	return get("x") == 0 && get("y") == 0 && get("z") == 0 &&
		get("0") == 0 && get("1") == 0 && get("2") == 0
}

// gpuVertexFormat maps a GPUVertexFormat string (WebGPU §9.1) to the
// go-macos/metal MTLVertexFormat it corresponds to, plus the component
// count/scalar kind wgslmsl.RenderResult.VertexInputs reports for the
// shader side of the same attribute — the only 4 formats go-macos/metal
// itself recognizes (metal.VertexFormat's own doc comment), so every other
// GPUVertexFormat string is a named rejection here, never a silently wrong
// MTLVertexFormat guess.
func gpuVertexFormat(format string) (mf metal.VertexFormat, nComponents int, kind wgsl.ScalarKind, ok bool) {
	switch format {
	case "float32x2":
		return metal.VertexFormatFloat32x2, 2, wgsl.KF32, true
	case "float32x3":
		return metal.VertexFormatFloat32x3, 3, wgsl.KF32, true
	case "uint32":
		return metal.VertexFormatUint32, 1, wgsl.KU32, true
	case "uint32x4":
		return metal.VertexFormatUint32x4, 4, wgsl.KU32, true
	default:
		return 0, 0, 0, false
	}
}

// gpuReadColor resolves a GPUColor (`{r, g, b, a}` or `[r, g, b, a]`) —
// clearValue's own two accepted shapes (WebGPU §19.1's own GPUColor
// union), for beginRenderPass's own colorAttachments[0].clearValue.
func gpuReadColor(v goja.Value) [4]float64 {
	obj, ok := v.(*goja.Object)
	if !ok {
		return [4]float64{}
	}
	if r := obj.Get("r"); r != nil && !goja.IsUndefined(r) {
		return [4]float64{r.ToFloat(), gpuGetFloat(obj, "g", 0), gpuGetFloat(obj, "b", 0), gpuGetFloat(obj, "a", 0)}
	}
	return [4]float64{gpuGetFloat(obj, "0", 0), gpuGetFloat(obj, "1", 0), gpuGetFloat(obj, "2", 0), gpuGetFloat(obj, "3", 0)}
}

// gpuCreateTexture implements GPUDevice.createTexture(descriptor) —
// engine#415 M7's own deliberately bounded scope: a single 2D texture (no
// mip chain, no array layers, no 3D/cube), go-macos/metal's own only
// supported GPUTextureFormat ("r8uint" -> metal.PixelFormatR8Uint — see
// metal.PixelFormat's own doc comment for why that is the one format this
// package's whole render path needs so far). A format/dimension/
// mipLevelCount/sampleCount this binding does not support is a named
// rejection (unsupportedJS), never a silently wrong texture; size.width/
// height being non-positive is a WebIDL-level argument problem (TypeError,
// matching createBuffer's own size<=0 check); an actual allocation failure
// against go-macos/metal is GPU content validation (panicOutOfMemory,
// matching createBuffer's own).
func (b *binder) gpuCreateTexture(dev *metal.Device, call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createTexture: a GPUTextureDescriptor is required"))
	}
	fmtVal := desc.Get("format")
	if fmtVal == nil || goja.IsUndefined(fmtVal) {
		panic(b.vm.NewTypeError("createTexture: format is required"))
	}
	format := fmtVal.String()
	if format != "r8uint" {
		panic(unsupportedJS(b, "createTexture: unsupported texture format "+format))
	}
	if dim := desc.Get("dimension"); dim != nil && !goja.IsUndefined(dim) && dim.String() != "2d" {
		panic(unsupportedJS(b, "createTexture: only dimension \"2d\" is supported"))
	}
	if m := desc.Get("mipLevelCount"); m != nil && !goja.IsUndefined(m) && m.ToInteger() != 1 {
		panic(unsupportedJS(b, "createTexture: only mipLevelCount 1 is supported"))
	}
	if s := desc.Get("sampleCount"); s != nil && !goja.IsUndefined(s) && s.ToInteger() != 1 {
		panic(unsupportedJS(b, "createTexture: only sampleCount 1 is supported"))
	}
	size := gpuReadExtent(desc.Get("size"))
	if size.w <= 0 || size.h <= 0 {
		panic(b.vm.NewTypeError("createTexture: size.width and size.height must be positive"))
	}
	if size.d != 1 {
		panic(unsupportedJS(b, "createTexture: only a single 2D texture (depthOrArrayLayers 1) is supported"))
	}
	usageVal := desc.Get("usage")
	if usageVal == nil || goja.IsUndefined(usageVal) {
		panic(b.vm.NewTypeError("createTexture: usage is required"))
	}
	usage := int(usageVal.ToInteger())
	var musage metal.TextureUsage
	if usage&gpuTextureUsageTextureBinding != 0 {
		musage |= metal.TextureUsageShaderRead
	}
	if usage&gpuTextureUsageStorageBinding != 0 {
		musage |= metal.TextureUsageShaderWrite
	}
	if usage&gpuTextureUsageRenderAttachment != 0 {
		musage |= metal.TextureUsageRenderTarget
	}
	tex, err := dev.NewTexture(size.w, size.h, metal.PixelFormatR8Uint, musage)
	if err != nil {
		b.panicOutOfMemory(err.Error())
	}
	return b.wrapGPUTexture(&gpuTextureState{tex: tex, width: size.w, height: size.h, format: format, usage: usage})
}

// wrapGPUTexture builds the JS GPUTexture surface over st, registering it
// in b.gpuTextures (the same by-object-identity registry shape gpuBuffers
// uses) so createView/writeTexture/copyTextureToBuffer/beginRenderPass can
// all recover the real *metal.Texture from the JS object identity alone.
func (b *binder) wrapGPUTexture(st *gpuTextureState) *goja.Object {
	o := b.vm.NewObject()
	if b.gpuTextures == nil {
		b.gpuTextures = map[*goja.Object]*gpuTextureState{}
	}
	b.gpuTextures[o] = st
	o.Set("width", st.width)
	o.Set("height", st.height)
	o.Set("depthOrArrayLayers", 1)
	o.Set("format", st.format)
	o.Set("usage", st.usage)
	o.Set("dimension", "2d")
	o.Set("mipLevelCount", 1)
	o.Set("sampleCount", 1)
	o.Set("createView", func(call goja.FunctionCall) goja.Value {
		return b.gpuTextureCreateView(st, call)
	})
	o.Set("destroy", func(call goja.FunctionCall) goja.Value {
		st.tex.Close()
		return goja.Undefined()
	})
	return o
}

// gpuTextureCreateView implements GPUTexture.createView(descriptor) —
// engine#415 M7's own bounded scope has no mip chain, no array layers and
// no format reinterpretation, so a GPUTextureView is never independent
// Go-level state of its own: it shares st (registered again, under the
// view's own distinct JS object identity, in b.gpuTextureViews) exactly as
// a real one-mip/one-layer texture's only possible view would. Every
// descriptor field that would need this binding to behave differently from
// "the whole texture, as-is" is a named rejection (unsupportedJS).
func (b *binder) gpuTextureCreateView(st *gpuTextureState, call goja.FunctionCall) goja.Value {
	if desc, ok := call.Argument(0).(*goja.Object); ok {
		if f := desc.Get("format"); f != nil && !goja.IsUndefined(f) && f.String() != st.format {
			panic(unsupportedJS(b, "createView: a view format different from the texture's own format is not supported"))
		}
		if d := desc.Get("dimension"); d != nil && !goja.IsUndefined(d) && d.String() != "2d" {
			panic(unsupportedJS(b, "createView: only a \"2d\" view is supported"))
		}
		if a := desc.Get("aspect"); a != nil && !goja.IsUndefined(a) && a.String() != "all" {
			panic(unsupportedJS(b, "createView: only the \"all\" aspect is supported"))
		}
		if m := desc.Get("baseMipLevel"); m != nil && !goja.IsUndefined(m) && m.ToInteger() != 0 {
			panic(unsupportedJS(b, "createView: only baseMipLevel 0 is supported"))
		}
		if m := desc.Get("mipLevelCount"); m != nil && !goja.IsUndefined(m) && m.ToInteger() != 1 {
			panic(unsupportedJS(b, "createView: only a single mip level is supported"))
		}
		if a := desc.Get("baseArrayLayer"); a != nil && !goja.IsUndefined(a) && a.ToInteger() != 0 {
			panic(unsupportedJS(b, "createView: only baseArrayLayer 0 is supported"))
		}
		if a := desc.Get("arrayLayerCount"); a != nil && !goja.IsUndefined(a) && a.ToInteger() != 1 {
			panic(unsupportedJS(b, "createView: only a single array layer is supported"))
		}
	}
	o := b.vm.NewObject()
	if b.gpuTextureViews == nil {
		b.gpuTextureViews = map[*goja.Object]*gpuTextureState{}
	}
	b.gpuTextureViews[o] = st
	return o
}

// gpuQueueWriteTexture implements GPUQueue.writeTexture(destination, data,
// dataLayout, size) — restricted to one full-extent, tightly packed write
// at mip 0 with a zero origin, the only shape engine#415 M7 needs so far
// (go-macos/metal's own Texture.Replace has the same restriction — see its
// own doc comment). Anything else is a named rejection (unsupportedJS); a
// data size that does not match offset/copy size is GPU content validation
// (panicValidation), matching writeBuffer's own convention for the same
// kind of bounds mismatch.
func (b *binder) gpuQueueWriteTexture(call goja.FunctionCall) goja.Value {
	destObj, _ := call.Argument(0).(*goja.Object)
	if destObj == nil {
		panic(b.vm.NewTypeError("writeTexture: destination must be a GPUImageCopyTexture"))
	}
	texObj, _ := destObj.Get("texture").(*goja.Object)
	var st *gpuTextureState
	if texObj != nil {
		st = b.gpuTextures[texObj]
	}
	if st == nil {
		panic(b.vm.NewTypeError("writeTexture: destination.texture must be a GPUTexture"))
	}
	if mip := destObj.Get("mipLevel"); mip != nil && !goja.IsUndefined(mip) && mip.ToInteger() != 0 {
		panic(unsupportedJS(b, "writeTexture: only mipLevel 0 is supported"))
	}
	if origin := destObj.Get("origin"); origin != nil && !goja.IsUndefined(origin) && !gpuIsZeroOrigin(origin) {
		panic(unsupportedJS(b, "writeTexture: only a zero origin (a full-extent write) is supported"))
	}
	data, ok := bufferSourceBytes(call.Argument(1))
	if !ok {
		panic(b.vm.NewTypeError("writeTexture: data must be an ArrayBuffer or typed array view"))
	}
	layoutObj, _ := call.Argument(2).(*goja.Object)
	if layoutObj == nil {
		panic(b.vm.NewTypeError("writeTexture: dataLayout is required"))
	}
	offset := 0
	if o := layoutObj.Get("offset"); o != nil && !goja.IsUndefined(o) {
		offset = int(o.ToInteger())
	}
	bytesPerRow := st.width
	if bpr := layoutObj.Get("bytesPerRow"); bpr != nil && !goja.IsUndefined(bpr) {
		bytesPerRow = int(bpr.ToInteger())
	}
	if bytesPerRow != st.width {
		panic(unsupportedJS(b, "writeTexture: only a tightly packed write (bytesPerRow == texture width) is supported"))
	}
	size := gpuReadExtent(call.Argument(3))
	if size.w != st.width || size.h != st.height {
		panic(unsupportedJS(b, "writeTexture: only a full-extent write is supported"))
	}
	want := st.width * st.height
	if offset < 0 || offset+want > len(data) {
		b.panicValidation("writeTexture: data is too small for the given offset and copy size")
	}
	if err := st.tex.Replace(data[offset : offset+want]); err != nil {
		b.panicValidation(err.Error())
	}
	return goja.Undefined()
}

// gpuBoundVertexBuffer is one vertex- or index-buffer binding recorded by
// setVertexBuffer/setIndexBuffer: the real *metal.Buffer and the byte
// offset into it, copied by VALUE at bind time — so a later
// setVertexBuffer rebinding the same slot never retroactively changes a
// draw already recorded against the earlier binding (gpuDraw/
// gpuDrawIndexed each copy the current map before appending their own
// gpuRecordedDraw).
type gpuBoundVertexBuffer struct {
	buf    *metal.Buffer
	offset int
	// size is bst.size at bind time (engine#447 review bug #6): copied
	// alongside buf/offset so gpuValidateVertexBuffers/
	// gpuValidateIndexedVertexBuffers can bounds-check a draw against the
	// buffer's own real size without re-resolving b.gpuBuffers[bufObj] —
	// which setVertexBuffer/setIndexBuffer's own callers no longer have
	// once a draw is recorded, by the same by-value-copy reasoning this
	// struct's own doc comment above already gives for buf/offset.
	size int
}

// gpuRenderPipelineState is the Go-level state behind one
// GPURenderPipeline: just the compiled go-macos/metal pipeline — unlike
// gpuComputePipelineState, nothing here needs the wgslmsl.RenderResult
// again after creation, since this binding's bounded render scope has no
// bind groups (VertexBindings/FragmentBindings are required empty — see
// gpuCreateRenderPipeline's own doc comment) and so no getBindGroupLayout
// equivalent.
type gpuRenderPipelineState struct {
	pipe *metal.RenderPipeline
	// vertexSlots is every vertex.buffers[] entry's own buffer index and
	// byte stride (engine#447 review bug #6): draw/drawIndexed need this
	// at draw time to reject a draw with no buffer bound for a slot the
	// pipeline declares, or one too small for the vertices/indices about
	// to be read — go-macos/metal's own RenderEncoder does not validate
	// either itself, so an unvalidated draw reads past a real buffer's
	// end (undefined content, not a crash this binding can rely on
	// Metal to catch).
	vertexSlots []gpuVertexSlot
}

// gpuVertexSlot is one vertex.buffers[] entry's own buffer index/stride,
// recorded at createRenderPipeline time (gpuRenderPipelineState's own doc
// comment).
type gpuVertexSlot struct {
	index  int
	stride int
}

// gpuCreateRenderPipeline implements
// GPUDevice.createRenderPipeline(descriptor) — engine#415 M7's own
// deliberately bounded render-pipeline scope:
//   - layout must be "auto" (no explicit GPUPipelineLayout for a render
//     pipeline yet).
//   - Neither the vertex nor the fragment entry point may read a WGSL
//     resource (@group/@binding): go-macos/metal's own RenderEncoder has
//     no fragment-stage buffer binding and no bind-group mechanism at all
//     — a shader needing one is a named rejection (unsupportedJS), not a
//     silently dropped binding.
//   - Every vertex.buffers[].attributes[] entry's own GPUVertexFormat must
//     be one of the 4 go-macos/metal itself recognizes (gpuVertexFormat)
//     AND must match the shader's own @location input by both component
//     count and scalar kind — a genuine content mismatch between the
//     pipeline's own vertex state and its shader, so panicValidation
//     (captured by an open error scope), not a thrown TypeError.
//   - fragment.targets[0].format must be "r8uint" (go-macos/metal's own
//     only supported color format) and there may be no depthStencil
//     state (go-macos/metal's own RenderPipelineDescriptor has no
//     counterpart — named rejection).
func (b *binder) gpuCreateRenderPipeline(dev *metal.Device, call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createRenderPipeline: a GPURenderPipelineDescriptor is required"))
	}
	layout := desc.Get("layout")
	if _, isObj := layout.(*goja.Object); isObj {
		panic(unsupportedJS(b, "createRenderPipeline: an explicit GPUPipelineLayout is not supported for a render pipeline"))
	} else if layout == nil || layout.String() != "auto" {
		panic(b.vm.NewTypeError("createRenderPipeline: layout must be \"auto\""))
	}
	if ds := desc.Get("depthStencil"); ds != nil && !goja.IsUndefined(ds) {
		panic(unsupportedJS(b, "createRenderPipeline: depthStencil is not supported"))
	}
	if prim, ok := desc.Get("primitive").(*goja.Object); ok {
		if t := prim.Get("topology"); t != nil && !goja.IsUndefined(t) && t.String() != "triangle-list" {
			panic(unsupportedJS(b, "createRenderPipeline: only \"triangle-list\" topology is supported"))
		}
		// engine#447 review bug #8: cullMode/frontFace/unclippedDepth were
		// read nowhere in this function — go-macos/metal's own
		// RenderPipelineDescriptor has no counterpart field for any of
		// them, so a caller asking for back-face culling (say) would have
		// gotten a pipeline that silently never culls anything, with no
		// error at all. Named rejections instead, exactly like topology
		// above, for any value other than the spec's own default.
		if cm := prim.Get("cullMode"); cm != nil && !goja.IsUndefined(cm) && cm.String() != "none" {
			panic(unsupportedJS(b, "createRenderPipeline: only \"none\" primitive.cullMode is supported"))
		}
		if ff := prim.Get("frontFace"); ff != nil && !goja.IsUndefined(ff) && ff.String() != "ccw" {
			panic(unsupportedJS(b, "createRenderPipeline: only \"ccw\" primitive.frontFace is supported"))
		}
		if ud := prim.Get("unclippedDepth"); ud != nil && !goja.IsUndefined(ud) && ud.ToBoolean() {
			panic(unsupportedJS(b, "createRenderPipeline: primitive.unclippedDepth is not supported"))
		}
	}
	// engine#447 review bug #8: multisample was read nowhere in this
	// function either — a caller requesting a 4x MSAA pipeline (say) got a
	// single-sample one with no error, which then mismatches a multisample
	// render target's own sample count at draw time deep inside
	// go-macos/metal instead of being refused up front, at the one place
	// that actually knows the request was made.
	if ms, ok := desc.Get("multisample").(*goja.Object); ok {
		if c := ms.Get("count"); c != nil && !goja.IsUndefined(c) && c.ToInteger() != 1 {
			panic(unsupportedJS(b, "createRenderPipeline: only multisample.count of 1 is supported"))
		}
		if a := ms.Get("alphaToCoverageEnabled"); a != nil && !goja.IsUndefined(a) && a.ToBoolean() {
			panic(unsupportedJS(b, "createRenderPipeline: multisample.alphaToCoverageEnabled is not supported"))
		}
	}

	vertexObj, _ := desc.Get("vertex").(*goja.Object)
	if vertexObj == nil {
		panic(b.vm.NewTypeError("createRenderPipeline: a vertex stage (GPUVertexState) is required"))
	}
	vModObj, _ := vertexObj.Get("module").(*goja.Object)
	vModState := b.gpuShaderModules[vModObj]
	if vModState == nil {
		panic(b.vm.NewTypeError("createRenderPipeline: vertex.module must be a GPUShaderModule"))
	}
	vEntryPoint := ""
	if ep := vertexObj.Get("entryPoint"); ep != nil && !goja.IsUndefined(ep) {
		vEntryPoint = ep.String()
	}
	// engine#447 review bug #8: vertex.constants (pipeline-overridable
	// constants) was read nowhere in this function — a caller overriding a
	// WGSL `override` value would have had that override silently dropped,
	// with the shader always compiled using its own declared default
	// instead, no error at all.
	if c, ok := vertexObj.Get("constants").(*goja.Object); ok && c != nil {
		panic(unsupportedJS(b, "createRenderPipeline: vertex.constants is not supported"))
	}

	fragmentObj, _ := desc.Get("fragment").(*goja.Object)
	if fragmentObj == nil {
		panic(b.vm.NewTypeError("createRenderPipeline: a fragment stage (GPUFragmentState) is required"))
	}
	fModObj, _ := fragmentObj.Get("module").(*goja.Object)
	fModState := b.gpuShaderModules[fModObj]
	if fModState == nil {
		panic(b.vm.NewTypeError("createRenderPipeline: fragment.module must be a GPUShaderModule"))
	}
	fEntryPoint := ""
	if ep := fragmentObj.Get("entryPoint"); ep != nil && !goja.IsUndefined(ep) {
		fEntryPoint = ep.String()
	}
	if c, ok := fragmentObj.Get("constants").(*goja.Object); ok && c != nil {
		panic(unsupportedJS(b, "createRenderPipeline: fragment.constants is not supported"))
	}
	if vModState.src != fModState.src {
		panic(unsupportedJS(b, "createRenderPipeline: vertex.module and fragment.module must be the same GPUShaderModule"))
	}

	targetsArr, _ := fragmentObj.Get("targets").(*goja.Object)
	if targetsArr == nil || int(targetsArr.Get("length").ToInteger()) != 1 {
		panic(unsupportedJS(b, "createRenderPipeline: exactly one fragment.targets entry is supported"))
	}
	target0, _ := targetsArr.Get("0").(*goja.Object)
	if target0 == nil {
		panic(unsupportedJS(b, "createRenderPipeline: fragment.targets[0].format must be \"r8uint\""))
	}
	target0FormatVal := target0.Get("format")
	if target0FormatVal == nil || goja.IsUndefined(target0FormatVal) {
		panic(b.vm.NewTypeError("createRenderPipeline: fragment.targets[0].format is required"))
	}
	if target0FormatVal.String() != "r8uint" {
		panic(unsupportedJS(b, "createRenderPipeline: fragment.targets[0].format must be \"r8uint\""))
	}
	// engine#447 review bug #8: blend/writeMask were read nowhere in this
	// function — go-macos/metal's own RenderPipelineDescriptor has no
	// per-target blend or write-mask field at all, so a caller asking for
	// alpha blending (say) got a pipeline that silently always overwrites
	// (no blending, full write mask), with no error. writeMask's own
	// spec default is GPUColorWrite.ALL (0xF); blend's is "no blend state".
	if bl := target0.Get("blend"); bl != nil && !goja.IsUndefined(bl) {
		panic(unsupportedJS(b, "createRenderPipeline: fragment.targets[0].blend is not supported"))
	}
	if wm := target0.Get("writeMask"); wm != nil && !goja.IsUndefined(wm) && wm.ToInteger() != 0xF {
		panic(unsupportedJS(b, "createRenderPipeline: fragment.targets[0].writeMask must be GPUColorWrite.ALL"))
	}

	res, err := wgslmsl.TranslateRender(vModState.src, wgslmsl.RenderOptions{VertexEntryPoint: vEntryPoint, FragmentEntryPoint: fEntryPoint})
	if err != nil {
		b.panicValidation("createRenderPipeline: " + err.Error())
	}
	if len(res.VertexBindings) != 0 || len(res.FragmentBindings) != 0 {
		panic(unsupportedJS(b, "createRenderPipeline: a shader resource (@group/@binding) is not supported in a render pipeline"))
	}

	byLocation := map[int]wgslmsl.VertexInputVar{}
	for _, in := range res.VertexInputs {
		byLocation[in.Location] = in
	}
	seen := map[int]bool{}
	var attrs []metal.VertexAttribute
	var layouts []metal.VertexLayout
	buffersArr, _ := vertexObj.Get("buffers").(*goja.Object)
	if buffersArr != nil {
		n := int(buffersArr.Get("length").ToInteger())
		for bi := 0; bi < n; bi++ {
			bufLayout, _ := buffersArr.Get(strconv.Itoa(bi)).(*goja.Object)
			if bufLayout == nil {
				continue
			}
			strideVal := bufLayout.Get("arrayStride")
			if strideVal == nil || goja.IsUndefined(strideVal) {
				panic(b.vm.NewTypeError("createRenderPipeline: vertex.buffers[].arrayStride is required"))
			}
			// engine#447 review bug #8: stepMode was read nowhere in this
			// function — metal.VertexLayout has no per-buffer step-rate
			// field at all here, so a caller asking for "instance" step
			// mode (per-instance, not per-vertex, attribute advance) got
			// an ordinary per-vertex layout silently, no error, which only
			// shows up as visibly wrong instanced geometry at draw time.
			if sm := bufLayout.Get("stepMode"); sm != nil && !goja.IsUndefined(sm) && sm.String() != "vertex" {
				panic(unsupportedJS(b, "createRenderPipeline: only \"vertex\" vertex.buffers[].stepMode is supported"))
			}
			stride := int(strideVal.ToInteger())
			layouts = append(layouts, metal.VertexLayout{BufferIndex: bi, Stride: stride})
			attrsArr, _ := bufLayout.Get("attributes").(*goja.Object)
			if attrsArr == nil {
				continue
			}
			an := int(attrsArr.Get("length").ToInteger())
			for ai := 0; ai < an; ai++ {
				attr, _ := attrsArr.Get(strconv.Itoa(ai)).(*goja.Object)
				if attr == nil {
					continue
				}
				formatVal := attr.Get("format")
				if formatVal == nil || goja.IsUndefined(formatVal) {
					panic(b.vm.NewTypeError("createRenderPipeline: vertex attribute format is required"))
				}
				formatStr := formatVal.String()
				mf, nc, kind, ok := gpuVertexFormat(formatStr)
				if !ok {
					panic(unsupportedJS(b, "createRenderPipeline: unsupported vertex format "+formatStr))
				}
				locVal := attr.Get("shaderLocation")
				if locVal == nil || goja.IsUndefined(locVal) {
					panic(b.vm.NewTypeError("createRenderPipeline: vertex attribute shaderLocation is required"))
				}
				loc := int(locVal.ToInteger())
				in, found := byLocation[loc]
				if !found {
					b.panicValidation(fmt.Sprintf("createRenderPipeline: shader has no @location(%d) vertex input to match this attribute", loc))
				}
				if in.NComponents != nc || in.ScalarKind != kind {
					b.panicValidation(fmt.Sprintf("createRenderPipeline: attribute at @location(%d) does not match the shader's own input type", loc))
				}
				offsetVal := attr.Get("offset")
				if offsetVal == nil || goja.IsUndefined(offsetVal) {
					panic(b.vm.NewTypeError("createRenderPipeline: vertex attribute offset is required"))
				}
				seen[loc] = true
				attrs = append(attrs, metal.VertexAttribute{
					Index:       loc,
					Format:      mf,
					Offset:      int(offsetVal.ToInteger()),
					BufferIndex: bi,
				})
			}
		}
	}
	for _, in := range res.VertexInputs {
		if !seen[in.Location] {
			b.panicValidation(fmt.Sprintf("createRenderPipeline: no vertex attribute given for the shader's own @location(%d) input", in.Location))
		}
	}

	combinedMSL := res.VertexMSL + "\n" + res.FragmentMSL
	lib, err := dev.Compile(combinedMSL)
	if err != nil {
		b.panicValidation("createRenderPipeline: Metal rejected the generated shader: " + err.Error())
	}
	pipe, err := lib.RenderPipeline(metal.RenderPipelineDescriptor{
		VertexFunction:   res.VertexEntryPointName,
		FragmentFunction: res.FragmentEntryPointName,
		Attributes:       attrs,
		Layouts:          layouts,
		ColorFormat:      metal.PixelFormatR8Uint,
	})
	if err != nil {
		b.panicValidation(err.Error())
	}
	var vertexSlots []gpuVertexSlot
	for _, l := range layouts {
		vertexSlots = append(vertexSlots, gpuVertexSlot{index: l.BufferIndex, stride: l.Stride})
	}
	return b.wrapGPURenderPipeline(&gpuRenderPipelineState{pipe: pipe, vertexSlots: vertexSlots})
}

func (b *binder) wrapGPURenderPipeline(st *gpuRenderPipelineState) *goja.Object {
	o := b.vm.NewObject()
	if b.gpuRenderPipelines == nil {
		b.gpuRenderPipelines = map[*goja.Object]*gpuRenderPipelineState{}
	}
	b.gpuRenderPipelines[o] = st
	return o
}

// gpuRecordedRenderPass is one beginRenderPass()...end() span, recorded as
// a single entry in gpuCommandEncoderState's own ordered commands list
// (gpuRecordedCommand's own doc comment) — the render pass's own target/
// load/store/clear, plus every draw/drawIndexed call made before end(), in
// order.
type gpuRecordedRenderPass struct {
	target *metal.Texture
	load   metal.LoadAction
	store  metal.StoreAction
	clear  [4]float64
	draws  []gpuRecordedDraw
}

// gpuRecordedDraw is one draw()/drawIndexed() call, fully resolved at
// record time: the pipeline and every vertex buffer slot bound at that
// moment (gpuBoundVertexBuffer's own doc comment for why each is copied by
// value), plus either the non-indexed or the indexed draw's own arguments.
type gpuRecordedDraw struct {
	pipeline      *metal.RenderPipeline
	vertexBuffers map[int]gpuBoundVertexBuffer
	indexed       bool
	// non-indexed (draw):
	vertexCount, instanceCount, firstVertex, firstInstance int
	// indexed (drawIndexed) — firstInstance is shared with the non-indexed
	// fields above; indexBufferOffset already folds in firstIndex*4
	// (uint32 indices only — see setIndexBuffer's own doc comment).
	indexBuffer       *metal.Buffer
	indexBufferOffset int
	indexCount        int
	baseVertex        int
}

// gpuRecordedTextureCopy is one copyTextureToBuffer() call — the texture-
// side counterpart of gpuRecordedCopy, executed via go-macos/metal's own
// Texture.GetBytes directly into the destination buffer's own zero-copy
// backing memory at gpuQueueSubmit time (after flushing any pending
// dispatch, exactly like gpuRecordedCopy, so a copy reading a render
// pass's own output always sees the real, GPU-written bytes).
type gpuRecordedTextureCopy struct {
	tex       *metal.Texture
	dst       *metal.Buffer
	dstOffset int
	size      int
}

// gpuBeginRenderPass implements
// GPUCommandEncoder.beginRenderPass(descriptor) — engine#415 M7's own
// bounded scope: exactly one color attachment (go-macos/metal's own
// RenderPassDescriptor has a single Target, no depthStencilAttachment
// counterpart), loadOp/storeOp mapped 1:1 to metal.LoadAction/
// StoreAction, clearValue resolved by gpuReadColor. Reuses passOpen/
// passEnded exactly as beginComputePass does (gpuCommandEncoderState's own
// doc comment: at most one pass, compute OR render, per encoder).
func (b *binder) gpuBeginRenderPass(st *gpuCommandEncoderState, call goja.FunctionCall) goja.Value {
	if st.finished {
		panic(b.vm.NewTypeError("beginRenderPass: this command encoder has already finished"))
	}
	if st.passOpen || st.passEnded {
		panic(unsupportedJS(b, "beginRenderPass: only one pass (compute or render) per command encoder is supported"))
	}
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("beginRenderPass: a GPURenderPassDescriptor is required"))
	}
	if ds := desc.Get("depthStencilAttachment"); ds != nil && !goja.IsUndefined(ds) {
		panic(unsupportedJS(b, "beginRenderPass: depthStencilAttachment is not supported"))
	}
	colorsArr, _ := desc.Get("colorAttachments").(*goja.Object)
	if colorsArr == nil || int(colorsArr.Get("length").ToInteger()) != 1 {
		panic(unsupportedJS(b, "beginRenderPass: exactly one colorAttachments entry is supported"))
	}
	ca, _ := colorsArr.Get("0").(*goja.Object)
	if ca == nil {
		panic(b.vm.NewTypeError("beginRenderPass: colorAttachments[0] is required"))
	}
	viewObj, _ := ca.Get("view").(*goja.Object)
	var texSt *gpuTextureState
	if viewObj != nil {
		texSt = b.gpuTextureViews[viewObj]
		if texSt == nil {
			texSt = b.gpuTextures[viewObj]
		}
	}
	if texSt == nil {
		panic(b.vm.NewTypeError("beginRenderPass: colorAttachments[0].view must be a GPUTextureView"))
	}
	if texSt.usage&gpuTextureUsageRenderAttachment == 0 {
		b.panicValidation("beginRenderPass: the attachment's texture was not created with RENDER_ATTACHMENT usage")
	}
	load := metal.LoadActionLoad
	if lv := ca.Get("loadOp"); lv != nil && !goja.IsUndefined(lv) {
		switch lv.String() {
		case "clear":
			load = metal.LoadActionClear
		case "load":
			load = metal.LoadActionLoad
		default:
			panic(unsupportedJS(b, "beginRenderPass: unsupported loadOp "+lv.String()))
		}
	}
	store := metal.StoreActionStore
	if sv := ca.Get("storeOp"); sv != nil && !goja.IsUndefined(sv) {
		switch sv.String() {
		case "store":
			store = metal.StoreActionStore
		case "discard":
			store = metal.StoreActionDontCare
		default:
			panic(unsupportedJS(b, "beginRenderPass: unsupported storeOp "+sv.String()))
		}
	}
	var clear [4]float64
	if cv := ca.Get("clearValue"); cv != nil && !goja.IsUndefined(cv) {
		clear = gpuReadColor(cv)
	}
	st.passOpen = true
	rp := &gpuRecordedRenderPass{target: texSt.tex, load: load, store: store, clear: clear}
	return b.wrapGPURenderPassEncoder(st, rp)
}

// copyVertexBuffers copies vbs by value, so a draw recorded against the
// CURRENT bindings is never retroactively changed by a later
// setVertexBuffer on the same encoder (gpuBoundVertexBuffer's own doc
// comment).
func copyVertexBuffers(vbs map[int]gpuBoundVertexBuffer) map[int]gpuBoundVertexBuffer {
	out := make(map[int]gpuBoundVertexBuffer, len(vbs))
	for k, v := range vbs {
		out[k] = v
	}
	return out
}

// wrapGPURenderPassEncoder builds the GPURenderPassEncoder surface —
// engine#415 M7's own counterpart to wrapGPUComputePassEncoder. Bind
// state (the current pipeline, every vertex buffer slot, the index
// buffer) persists across multiple draw()/drawIndexed() calls within the
// SAME pass, exactly as setPipeline/setBindGroup do for a compute pass.
func (b *binder) wrapGPURenderPassEncoder(enc *gpuCommandEncoderState, state *gpuRecordedRenderPass) *goja.Object {
	var curPipeline *gpuRenderPipelineState
	vertexBuffers := map[int]gpuBoundVertexBuffer{}
	var curIndexBuffer *gpuBoundVertexBuffer

	o := b.vm.NewObject()
	o.Set("setPipeline", func(call goja.FunctionCall) goja.Value {
		pObj, _ := call.Argument(0).(*goja.Object)
		p := b.gpuRenderPipelines[pObj]
		if p == nil {
			panic(b.vm.NewTypeError("setPipeline: expected a GPURenderPipeline"))
		}
		curPipeline = p
		return goja.Undefined()
	})
	o.Set("setVertexBuffer", func(call goja.FunctionCall) goja.Value {
		slot := int(call.Argument(0).ToInteger())
		bufObj, _ := call.Argument(1).(*goja.Object)
		bst := b.gpuBuffers[bufObj]
		if bst == nil {
			panic(b.vm.NewTypeError("setVertexBuffer: expected a GPUBuffer"))
		}
		offset := 0
		if a := call.Argument(2); a != nil && !goja.IsUndefined(a) {
			offset = int(a.ToInteger())
		}
		if offset < 0 || offset > bst.size {
			panic(b.vm.NewTypeError("setVertexBuffer: offset out of range for this buffer"))
		}
		vertexBuffers[slot] = gpuBoundVertexBuffer{buf: bst.buf, offset: offset, size: bst.size}
		return goja.Undefined()
	})
	o.Set("setIndexBuffer", func(call goja.FunctionCall) goja.Value {
		bufObj, _ := call.Argument(0).(*goja.Object)
		bst := b.gpuBuffers[bufObj]
		if bst == nil {
			panic(b.vm.NewTypeError("setIndexBuffer: expected a GPUBuffer"))
		}
		format := call.Argument(1).String()
		if format != "uint32" {
			panic(unsupportedJS(b, "setIndexBuffer: only \"uint32\" indices are supported"))
		}
		offset := 0
		if a := call.Argument(2); a != nil && !goja.IsUndefined(a) {
			offset = int(a.ToInteger())
		}
		if offset < 0 || offset > bst.size {
			panic(b.vm.NewTypeError("setIndexBuffer: offset out of range for this buffer"))
		}
		ib := gpuBoundVertexBuffer{buf: bst.buf, offset: offset, size: bst.size}
		curIndexBuffer = &ib
		return goja.Undefined()
	})
	o.Set("draw", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(enc.dev, goja.Undefined(), func() goja.Value {
			// engine#447 review bug #7: draw()/drawIndexed() after end()
			// must not still execute — state.draws is only flushed into
			// enc.commands by end() (below), but a call reaching here
			// after that has already happened would still append to
			// state.draws, silently growing a render pass gpuQueueSubmit
			// believes is already fully recorded.
			if !enc.passOpen {
				panic(b.vm.NewTypeError("draw: this pass has already ended"))
			}
			return b.gpuDraw(state, curPipeline, vertexBuffers, call)
		})
	})
	o.Set("drawIndexed", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(enc.dev, goja.Undefined(), func() goja.Value {
			if !enc.passOpen {
				panic(b.vm.NewTypeError("drawIndexed: this pass has already ended"))
			}
			return b.gpuDrawIndexed(state, curPipeline, vertexBuffers, curIndexBuffer, call)
		})
	})
	o.Set("end", func(call goja.FunctionCall) goja.Value {
		// engine#447 review bug #7: end() called twice must not re-run
		// the pass — without this guard, a second end() re-appends the
		// SAME state (and its by-now-possibly-grown state.draws, if a
		// draw after the first end() was also left unguarded) as a
		// second gpuRecordedCommand, executing every one of its draws
		// all over again at gpuQueueSubmit time.
		if !enc.passOpen {
			panic(b.vm.NewTypeError("end: this pass has already ended"))
		}
		enc.passOpen = false
		enc.passEnded = true
		enc.commands = append(enc.commands, gpuRecordedCommand{render: state})
		return goja.Undefined()
	})
	return o
}

// gpuDraw implements GPURenderPassEncoder.draw(vertexCount,
// [instanceCount, [firstVertex, [firstInstance]]]).
func (b *binder) gpuDraw(state *gpuRecordedRenderPass, pipeline *gpuRenderPipelineState, vbs map[int]gpuBoundVertexBuffer, call goja.FunctionCall) goja.Value {
	if pipeline == nil {
		b.panicValidation("draw: no pipeline is set")
	}
	vertexCount := int(call.Argument(0).ToInteger())
	instanceCount := 1
	if a := call.Argument(1); a != nil && !goja.IsUndefined(a) {
		instanceCount = int(a.ToInteger())
	}
	firstVertex := 0
	if a := call.Argument(2); a != nil && !goja.IsUndefined(a) {
		firstVertex = int(a.ToInteger())
	}
	firstInstance := 0
	if a := call.Argument(3); a != nil && !goja.IsUndefined(a) {
		firstInstance = int(a.ToInteger())
	}
	if vertexCount < 0 || instanceCount < 0 || firstVertex < 0 || firstInstance < 0 {
		b.panicValidation("draw: vertexCount, instanceCount, firstVertex and firstInstance must not be negative")
	}
	b.gpuValidateVertexBuffers("draw", pipeline, vbs, firstVertex, vertexCount)
	state.draws = append(state.draws, gpuRecordedDraw{
		pipeline: pipeline.pipe, vertexBuffers: copyVertexBuffers(vbs),
		vertexCount: vertexCount, instanceCount: instanceCount,
		firstVertex: firstVertex, firstInstance: firstInstance,
	})
	return goja.Undefined()
}

// gpuValidateVertexBuffers is draw's own bounds check (engine#447 review
// bug #6): every slot the pipeline declares (gpuRenderPipelineState's own
// vertexSlots) must have a vertex buffer bound, regardless of
// vertexCount, and — when vertexCount > 0 — that buffer must be large
// enough to supply every vertex in [firstVertex, firstVertex+vertexCount)
// at its own declared stride. Content validation (panicValidation, per
// spec's own error model — never a thrown TypeError), called from BOTH
// draw (firstVertex-based range) and drawIndexed (gpuValidateIndexedVertexBuffers's
// own per-index range, after resolving each actual vertex index through
// baseVertex).
func (b *binder) gpuValidateVertexBuffers(who string, pipeline *gpuRenderPipelineState, vbs map[int]gpuBoundVertexBuffer, firstVertex, vertexCount int) {
	for _, slot := range pipeline.vertexSlots {
		vb, ok := vbs[slot.index]
		if !ok {
			b.panicValidation(fmt.Sprintf("%s: no vertex buffer bound for slot %d", who, slot.index))
		}
		if vertexCount <= 0 || slot.stride <= 0 {
			continue
		}
		maxIndex := firstVertex + vertexCount - 1
		needed := vb.offset + (maxIndex+1)*slot.stride
		if needed > vb.size {
			b.panicValidation(fmt.Sprintf("%s: vertex buffer at slot %d is too small for this draw (need %d bytes, have %d)", who, slot.index, needed, vb.size))
		}
	}
}

// gpuValidateIndexedVertexBuffers is drawIndexed's own bounds check
// (engine#447 review bug #6): first confirms every pipeline-declared slot
// has a vertex buffer bound (exactly like gpuValidateVertexBuffers),
// then — when indexCount > 0 — reads every uint32 index the draw is about
// to use directly from the index buffer's own CPU-visible bytes
// (go-macos/metal buffers are unified memory; gpuDispatchWorkgroups
// already reads/writes one the same way for its own _Sizes buffer) and
// checks, for every slot, that baseVertex+index is never negative and
// never reads past that slot's bound buffer. Content validation
// (panicValidation): the buffers, the pipeline and every individual index
// value are each independently legitimate; only the COMBINATION — THIS
// index against THIS buffer — can be out of range, which is exactly what
// go-macos/metal's own RenderEncoder does not check.
func (b *binder) gpuValidateIndexedVertexBuffers(pipeline *gpuRenderPipelineState, vbs map[int]gpuBoundVertexBuffer, ib *gpuBoundVertexBuffer, byteOffset, indexCount, baseVertex int) {
	for _, slot := range pipeline.vertexSlots {
		if _, ok := vbs[slot.index]; !ok {
			b.panicValidation(fmt.Sprintf("drawIndexed: no vertex buffer bound for slot %d", slot.index))
		}
	}
	// No "indexCount <= 0: return" guard here (mutsweep flagged its
	// absence as unobserved, not the other way around — deleted as
	// proven dead, not waived): both gpuDrawIndexed call sites below
	// already guarantee the two things such a guard would protect. `ib`
	// is never nil here (gpuDrawIndexed's own "no index buffer is set"
	// check, just above its call to this function, panics first), so
	// ib.buf.Bytes() is always safe to call; and indexCount is never
	// negative here (gpuDrawIndexed's own negative-value check, also
	// above its call to this function, panics first), so the for loop
	// below already runs zero iterations on its own for indexCount == 0 —
	// exactly what an early return would have produced, with no
	// observable difference either way.
	raw := ib.buf.Bytes()
	for i := 0; i < indexCount; i++ {
		off := byteOffset + i*4
		idx := int(int64(baseVertex) + int64(binary.LittleEndian.Uint32(raw[off:off+4])))
		if idx < 0 {
			b.panicValidation("drawIndexed: an index plus baseVertex is negative")
		}
		for _, slot := range pipeline.vertexSlots {
			if slot.stride <= 0 {
				continue
			}
			vb := vbs[slot.index]
			needed := vb.offset + (idx+1)*slot.stride
			if needed > vb.size {
				b.panicValidation(fmt.Sprintf("drawIndexed: vertex buffer at slot %d is too small for this draw (need %d bytes, have %d)", slot.index, needed, vb.size))
			}
		}
	}
}

// gpuDrawIndexed implements GPURenderPassEncoder.drawIndexed(indexCount,
// [instanceCount, [firstIndex, [baseVertex, [firstInstance]]]]).
// firstIndex*4 is folded into the recorded indexBufferOffset (uint32
// indices only, this binding's own setIndexBuffer restriction) since
// go-macos/metal's own RenderEncoder.DrawIndexed has no separate
// firstIndex parameter.
func (b *binder) gpuDrawIndexed(state *gpuRecordedRenderPass, pipeline *gpuRenderPipelineState, vbs map[int]gpuBoundVertexBuffer, ib *gpuBoundVertexBuffer, call goja.FunctionCall) goja.Value {
	if pipeline == nil {
		b.panicValidation("drawIndexed: no pipeline is set")
	}
	if ib == nil {
		b.panicValidation("drawIndexed: no index buffer is set")
	}
	indexCount := int(call.Argument(0).ToInteger())
	instanceCount := 1
	if a := call.Argument(1); a != nil && !goja.IsUndefined(a) {
		instanceCount = int(a.ToInteger())
	}
	firstIndex := 0
	if a := call.Argument(2); a != nil && !goja.IsUndefined(a) {
		firstIndex = int(a.ToInteger())
	}
	baseVertex := 0
	if a := call.Argument(3); a != nil && !goja.IsUndefined(a) {
		baseVertex = int(a.ToInteger())
	}
	firstInstance := 0
	if a := call.Argument(4); a != nil && !goja.IsUndefined(a) {
		firstInstance = int(a.ToInteger())
	}
	if indexCount < 0 || instanceCount < 0 || firstIndex < 0 || firstInstance < 0 {
		b.panicValidation("drawIndexed: indexCount, instanceCount, firstIndex and firstInstance must not be negative")
	}
	indexByteOffset := ib.offset + firstIndex*4
	if indexCount > 0 {
		if need := indexByteOffset + indexCount*4; need > ib.size {
			b.panicValidation(fmt.Sprintf("drawIndexed: index buffer is too small for this draw (need %d bytes, have %d)", need, ib.size))
		}
	}
	b.gpuValidateIndexedVertexBuffers(pipeline, vbs, ib, indexByteOffset, indexCount, baseVertex)
	state.draws = append(state.draws, gpuRecordedDraw{
		pipeline: pipeline.pipe, vertexBuffers: copyVertexBuffers(vbs), indexed: true,
		indexBuffer: ib.buf, indexBufferOffset: indexByteOffset,
		indexCount: indexCount, instanceCount: instanceCount,
		baseVertex: baseVertex, firstInstance: firstInstance,
	})
	return goja.Undefined()
}

// gpuCopyTextureToBuffer implements
// GPUCommandEncoder.copyTextureToBuffer(source, destination, copySize) —
// the texture-side counterpart of gpuCopyBufferToBuffer, with the same
// "only a tightly packed, full-extent, mip-0, zero-origin copy"
// restriction writeTexture itself has (see its own doc comment). Recorded
// into enc.commands, not copied immediately — gpuRecordedTextureCopy's
// own doc comment for why ordering relative to a render pass matters.
func (b *binder) gpuCopyTextureToBuffer(enc *gpuCommandEncoderState, call goja.FunctionCall) goja.Value {
	if enc.finished {
		panic(b.vm.NewTypeError("copyTextureToBuffer: this command encoder has already finished"))
	}
	srcObj, _ := call.Argument(0).(*goja.Object)
	if srcObj == nil {
		panic(b.vm.NewTypeError("copyTextureToBuffer: source must be a GPUImageCopyTexture"))
	}
	texObj, _ := srcObj.Get("texture").(*goja.Object)
	var texSt *gpuTextureState
	if texObj != nil {
		texSt = b.gpuTextures[texObj]
	}
	if texSt == nil {
		panic(b.vm.NewTypeError("copyTextureToBuffer: source.texture must be a GPUTexture"))
	}
	if mip := srcObj.Get("mipLevel"); mip != nil && !goja.IsUndefined(mip) && mip.ToInteger() != 0 {
		panic(unsupportedJS(b, "copyTextureToBuffer: only mipLevel 0 is supported"))
	}
	if origin := srcObj.Get("origin"); origin != nil && !goja.IsUndefined(origin) && !gpuIsZeroOrigin(origin) {
		panic(unsupportedJS(b, "copyTextureToBuffer: only a zero origin (a full-extent copy) is supported"))
	}
	dstObj, _ := call.Argument(1).(*goja.Object)
	if dstObj == nil {
		panic(b.vm.NewTypeError("copyTextureToBuffer: destination must be a GPUImageCopyBuffer"))
	}
	bufObj, _ := dstObj.Get("buffer").(*goja.Object)
	var bufSt *gpuBufferState
	if bufObj != nil {
		bufSt = b.gpuBuffers[bufObj]
	}
	if bufSt == nil {
		panic(b.vm.NewTypeError("copyTextureToBuffer: destination.buffer must be a GPUBuffer"))
	}
	offset := 0
	if o := dstObj.Get("offset"); o != nil && !goja.IsUndefined(o) {
		offset = int(o.ToInteger())
	}
	bytesPerRow := texSt.width
	if bpr := dstObj.Get("bytesPerRow"); bpr != nil && !goja.IsUndefined(bpr) {
		bytesPerRow = int(bpr.ToInteger())
	}
	if bytesPerRow != texSt.width {
		panic(unsupportedJS(b, "copyTextureToBuffer: only a tightly packed copy (bytesPerRow == texture width) is supported"))
	}
	size := gpuReadExtent(call.Argument(2))
	if size.w != texSt.width || size.h != texSt.height {
		panic(unsupportedJS(b, "copyTextureToBuffer: only a full-extent copy is supported"))
	}
	want := texSt.width * texSt.height
	if offset < 0 || offset+want > bufSt.size {
		panic(b.vm.NewTypeError("copyTextureToBuffer: destination range out of bounds"))
	}
	enc.commands = append(enc.commands, gpuRecordedCommand{textureCopy: &gpuRecordedTextureCopy{
		tex: texSt.tex, dst: bufSt.buf, dstOffset: offset, size: want,
	}})
	return goja.Undefined()
}
