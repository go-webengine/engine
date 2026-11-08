// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"encoding/binary"
	"fmt"
	"strconv"

	"github.com/dop251/goja"
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

// gpuGuard runs fn, catching ONLY a panicValidation-raised error (see its
// own doc comment for why that is the one panic shape this intercepts,
// never an ordinary TypeError or an unrelated Go panic, both of which
// propagate through unchanged — a real bug must still crash). Per
// WebGPU's own error model (§22 "Errors & Debugging"): a caught error is
// handed to the INNERMOST open error scope whose own filter matches it
// (walking outward — a scope with a non-matching filter lets it pass
// through to the next one), captured there only if that scope does not
// already hold an error; if no open scope matches at all, it fires
// "uncapturederror" on the device instead. Either way, fn's own call
// still "succeeds" as far as the script can tell — invalidResult stands
// in for whatever the real result would have been, matching spec's own
// "return an invalid object" contract (never a throw, never a rejected
// promise, for this error class specifically).
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
	if st != nil {
		for i := len(st.scopes) - 1; i >= 0; i-- {
			sc := st.scopes[i]
			if sc.filter != class {
				continue
			}
			if sc.captured == nil {
				sc.captured = caught
			}
			return invalidResult
		}
		if st.onUncapturedError != nil {
			if fn, ok := goja.AssertFunction(st.onUncapturedError); ok {
				ev := b.newEvent("uncapturederror")
				ev.Set("error", caught)
				b.callSafely(fn, goja.Undefined(), ev)
			}
		}
	}
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

// newGPU builds navigator.gpu (HTML/WebGPU: `[Exposed=(Window, Worker)]
// interface GPU`). Installed only when Options.EnableWebGPU is set — see
// its own doc comment for the opt-in rationale.
func (b *binder) newGPU() goja.Value {
	o := b.vm.NewObject()
	o.Set("requestAdapter", func(call goja.FunctionCall) goja.Value {
		return b.gpuRequestAdapter()
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

// gpuRequestAdapter implements GPU.requestAdapter(): per spec, this NEVER
// rejects — it resolves with null when no adapter is available (no usable
// GPU, or go-macos/metal's own ErrUnsupported on a non-darwin host, which
// it reports on every platform so this engine cross-builds without a
// build tag of its own — confirmed directly by cross-building this package
// for every GOARCH this repo's CI already checks).
func (b *binder) gpuRequestAdapter() goja.Value {
	promise, resolve, _ := b.vm.NewPromise()
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
		return b.gpuCreateShaderModule(call)
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

	queue := b.vm.NewObject()
	queue.Set("writeBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuGuard(dev, goja.Undefined(), func() goja.Value {
			return b.gpuQueueWriteBuffer(call)
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
	size := int(desc.Get("size").ToInteger())
	usage := int(desc.Get("usage").ToInteger())
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
// comment) and the translation this binder already ran eagerly at
// createShaderModule time against the module's own default entry-point
// selection.
type gpuShaderModuleState struct {
	src    string
	result *wgslmsl.Result
	err    error
}

// gpuCreateShaderModule implements GPUDevice.createShaderModule(descriptor).
// Translation happens eagerly, right here — this binding settles everything
// synchronously — but a translation failure does NOT reject or throw: per
// spec, shader module creation always succeeds with a USABLE (if invalid)
// module object, and the error surfaces later, via getCompilationInfo() or
// (in this binding's own simplified, synchronous model, which has no
// device-timeline validation-error/device-lost event stream to report it
// through otherwise) a clear throw from createComputePipeline.
func (b *binder) gpuCreateShaderModule(call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createShaderModule: a GPUShaderModuleDescriptor is required"))
	}
	src := desc.Get("code").String()
	res, err := wgslmsl.Translate(src, wgslmsl.Options{})
	return b.wrapGPUShaderModule(&gpuShaderModuleState{src: src, result: res, err: err})
}

func (b *binder) wrapGPUShaderModule(st *gpuShaderModuleState) *goja.Object {
	o := b.vm.NewObject()
	o.Set("getCompilationInfo", func(call goja.FunctionCall) goja.Value {
		var messages []interface{}
		if st.err != nil {
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

// gpuBindGroupInfo is the Go-level state behind one GPUBindGroup: which
// @group(N) it was built for, and the real buffer bound at each WGSL
// `binding` index within it.
type gpuBindGroupInfo struct {
	group    int
	bindings map[int]*gpuBufferState
}

// gpuCreateBindGroup implements GPUDevice.createBindGroup(descriptor).
// Every entry's resource must be a plain `{buffer}` GPUBufferBinding over
// the WHOLE buffer (no offset/size, no sampler/texture/external-texture
// resources yet) — the one shape wgslmsl's own storage-array-only resource
// scope ever needs.
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
	bindings := map[int]*gpuBufferState{}
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
		if off := resObj.Get("offset"); off != nil && !goja.IsUndefined(off) && off.ToInteger() != 0 {
			panic(unsupportedJS(b, "createBindGroup: a non-zero GPUBufferBinding offset is not supported"))
		}
		if sz := resObj.Get("size"); sz != nil && !goja.IsUndefined(sz) && int(sz.ToInteger()) != st.size {
			panic(unsupportedJS(b, "createBindGroup: a GPUBufferBinding size other than the whole buffer is not supported"))
		}
		bindings[bindingIdx] = st
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
// to be distinct beyond that. Deliberately scoped to AT MOST ONE compute
// pass per encoder — the realistic shape every test and real usage needs so
// far; a second beginComputePass() is a clear, named rejection, not a
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
// would otherwise have to merge by guesswork. Exactly one of
// dispatch/copy is non-nil.
type gpuRecordedCommand struct {
	dispatch *gpuRecordedDispatch
	copy     *gpuRecordedCopy
}

// gpuRecordedDispatch is one dispatchWorkgroups() call, fully resolved at
// the moment it was recorded: every WGSL resource binding the pipeline
// needs is already mapped to its real *metal.Buffer (including the
// synthesised bounds-checking sizes buffer, sizesBuf, freed once
// gpuQueueSubmit's own dev.Run has finished using it), and the workgroup
// count has already been converted to the TOTAL thread count
// go-macos/metal's own Dispatch expects.
type gpuRecordedDispatch struct {
	pipeline *gpuComputePipelineState
	buffers  map[int]*metal.Buffer
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
	o.Set("finish", func(call goja.FunctionCall) goja.Value {
		if st.finished {
			panic(b.vm.NewTypeError("finish: this command encoder has already finished"))
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
			return b.gpuDispatchWorkgroups(enc, curPipeline, curBindGroups, call)
		})
	})
	o.Set("end", func(call goja.FunctionCall) goja.Value {
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

	buffers := map[int]*metal.Buffer{}
	for _, bnd := range pipeline.result.Bindings {
		bg, ok := bindGroups[bnd.Group]
		if !ok {
			b.panicValidation(fmt.Sprintf("dispatchWorkgroups: no bind group set for @group(%d)", bnd.Group))
		}
		bst, ok := bg.bindings[bnd.BindingIndex]
		if !ok {
			b.panicValidation(fmt.Sprintf("dispatchWorkgroups: bind group for @group(%d) has no binding for @binding(%d)", bnd.Group, bnd.BindingIndex))
		}
		buffers[bnd.MetalIndex] = bst.buf
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
		buffers[pipeline.result.SizeMetalIndex] = sb
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
				for idx, buf := range d.buffers {
					e.Buffer(idx, buf)
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
		copy(c.copy.dst.Bytes()[c.copy.dstOffset:c.copy.dstOffset+c.copy.size],
			c.copy.src.Bytes()[c.copy.srcOffset:c.copy.srcOffset+c.copy.size])
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
