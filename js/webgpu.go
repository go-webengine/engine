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

// newGPUSupportedFeatures returns an empty, setlike GPUSupportedFeatures:
// this binding does not yet implement any optional WebGPU feature
// (timestamp-query, shader-f16, …), so `adapter.features.has(x)` correctly
// reads false for every x rather than claiming something unimplemented.
func (b *binder) newGPUSupportedFeatures() *goja.Object {
	o := b.vm.NewObject()
	o.Set("size", 0)
	o.Set("has", func(goja.FunctionCall) goja.Value { return b.vm.ToValue(false) })
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
		info := b.vm.NewObject()
		info.Set("reason", "destroyed")
		info.Set("message", "device was destroyed")
		_ = lostResolve(info)
		return goja.Undefined()
	})
	o.Set("lost", lostPromise)

	o.Set("createBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateBuffer(dev, call)
	})

	o.Set("createShaderModule", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateShaderModule(call)
	})
	o.Set("createComputePipeline", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateComputePipeline(dev, call)
	})
	o.Set("createBindGroup", func(call goja.FunctionCall) goja.Value {
		return b.gpuCreateBindGroup(call)
	})
	o.Set("createCommandEncoder", func(call goja.FunctionCall) goja.Value {
		return b.wrapGPUCommandEncoder(&gpuCommandEncoderState{dev: dev})
	})

	queue := b.vm.NewObject()
	queue.Set("writeBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuQueueWriteBuffer(call)
	})
	queue.Set("submit", func(call goja.FunctionCall) goja.Value {
		return b.gpuQueueSubmit(dev, call)
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
		panic(b.domException("OperationError", err.Error()))
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
		panic(b.vm.NewTypeError("writeBuffer: range out of bounds"))
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
}

// gpuCreateComputePipeline implements GPUDevice.createComputePipeline(descriptor).
// Only `layout: "auto"` is supported (no explicit GPUBindGroupLayout/
// GPUPipelineLayout construction yet) — the one layout mode a script using
// wgslmsl's own inferred bindings (every realistic simple compute shader)
// actually needs; getBindGroupLayout(index) below is what a script then
// calls to build a matching GPUBindGroup.
func (b *binder) gpuCreateComputePipeline(dev *metal.Device, call goja.FunctionCall) goja.Value {
	desc, _ := call.Argument(0).(*goja.Object)
	if desc == nil {
		panic(b.vm.NewTypeError("createComputePipeline: a GPUComputePipelineDescriptor is required"))
	}
	if layout := desc.Get("layout"); layout == nil || layout.String() != "auto" {
		panic(unsupportedJS(b, "createComputePipeline: only layout: \"auto\" is supported (no explicit GPUBindGroupLayout/GPUPipelineLayout yet)"))
	}
	computeObj, _ := desc.Get("compute").(*goja.Object)
	if computeObj == nil {
		panic(b.vm.NewTypeError("createComputePipeline: a compute stage (GPUProgrammableStage) is required"))
	}
	moduleObj, _ := computeObj.Get("module").(*goja.Object)
	modState := b.gpuShaderModules[moduleObj]
	if modState == nil {
		panic(b.vm.NewTypeError("createComputePipeline: compute.module must be a GPUShaderModule"))
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
			panic(b.domException("OperationError", "createComputePipeline: "+err.Error()))
		}
		result = r
	} else if modState.err != nil {
		panic(b.domException("OperationError", "createComputePipeline: shader module failed to compile: "+modState.err.Error()))
	} else {
		result = modState.result
	}
	lib, err := dev.Compile(result.MSL)
	if err != nil {
		panic(b.domException("OperationError", "createComputePipeline: Metal rejected the generated shader: "+err.Error()))
	}
	pipe, err := lib.Pipeline(result.EntryPointName)
	if err != nil {
		panic(b.domException("OperationError", err.Error()))
	}
	return b.wrapGPUComputePipeline(&gpuComputePipelineState{pipe: pipe, result: result})
}

func (b *binder) wrapGPUComputePipeline(st *gpuComputePipelineState) *goja.Object {
	o := b.vm.NewObject()
	o.Set("getBindGroupLayout", func(call goja.FunctionCall) goja.Value {
		idx := int(call.Argument(0).ToInteger())
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

// gpuBindGroupLayoutInfo is what pipeline.getBindGroupLayout(index) returns
// a handle to: which pipeline and which @group(index) it names. It carries
// no resource-type validation of its own (the one real check — that every
// binding a dispatch actually needs is present — happens at
// dispatchWorkgroups time, against wgslmsl.Result.Bindings directly).
type gpuBindGroupLayoutInfo struct {
	pipeline *gpuComputePipelineState
	group    int
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
	dev        *metal.Device
	passOpen   bool
	passEnded  bool
	finished   bool
	dispatches []gpuRecordedDispatch
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
		return b.gpuDispatchWorkgroups(enc, curPipeline, curBindGroups, call)
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
		panic(b.domException("OperationError", "dispatchWorkgroups: no pipeline is set"))
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
			panic(b.domException("OperationError", fmt.Sprintf("dispatchWorkgroups: no bind group set for @group(%d)", bnd.Group)))
		}
		bst, ok := bg.bindings[bnd.BindingIndex]
		if !ok {
			panic(b.domException("OperationError", fmt.Sprintf("dispatchWorkgroups: bind group for @group(%d) has no binding for @binding(%d)", bnd.Group, bnd.BindingIndex)))
		}
		buffers[bnd.MetalIndex] = bst.buf
	}
	var sizesBuf *metal.Buffer
	if pipeline.result.SizeMetalIndex >= 0 {
		sb, err := enc.dev.NewBuffer(4 * len(pipeline.result.Bindings))
		if err != nil {
			panic(b.domException("OperationError", err.Error()))
		}
		for i, bnd := range pipeline.result.Bindings {
			bst := bindGroups[bnd.Group].bindings[bnd.BindingIndex]
			count := uint32(bst.size / bnd.ElemBytes)
			binary.LittleEndian.PutUint32(sb.Bytes()[i*4:], count)
		}
		buffers[pipeline.result.SizeMetalIndex] = sb
		sizesBuf = sb
	}
	enc.dispatches = append(enc.dispatches, gpuRecordedDispatch{
		pipeline: pipeline,
		buffers:  buffers,
		sizesBuf: sizesBuf,
		x:        x * ws[0],
		y:        y * ws[1],
		z:        z * ws[2],
	})
	return goja.Undefined()
}

// gpuQueueSubmit implements GPUQueue.submit(commandBuffers): every recorded
// dispatch, across every command buffer given, replays through ONE real
// go-macos/metal Device.Run call (which blocks until the GPU has actually
// finished — see onSubmittedWorkDone's own doc comment for why that makes
// it correct to resolve immediately), then releases every ephemeral sizes
// buffer a dispatch allocated.
func (b *binder) gpuQueueSubmit(dev *metal.Device, call goja.FunctionCall) goja.Value {
	arr, _ := call.Argument(0).(*goja.Object)
	if arr == nil {
		panic(b.vm.NewTypeError("submit: expected a sequence of GPUCommandBuffer"))
	}
	n := int(arr.Get("length").ToInteger())
	var dispatches []gpuRecordedDispatch
	for i := 0; i < n; i++ {
		cbObj, _ := arr.Get(strconv.Itoa(i)).(*goja.Object)
		cb := b.gpuCommandBuffers[cbObj]
		if cb == nil {
			panic(b.vm.NewTypeError("submit: expected a GPUCommandBuffer"))
		}
		dispatches = append(dispatches, cb.dispatches...)
	}
	err := dev.Run(func(e *metal.Encoder) {
		for _, d := range dispatches {
			e.Use(d.pipeline.pipe)
			for idx, buf := range d.buffers {
				e.Buffer(idx, buf)
			}
			e.Dispatch(d.x, d.y, d.z)
		}
	})
	for _, d := range dispatches {
		if d.sizesBuf != nil {
			d.sizesBuf.Close()
		}
	}
	if err != nil {
		panic(b.domException("OperationError", err.Error()))
	}
	return goja.Undefined()
}

// unsupportedJS reports a construct this binding's own deliberately bounded
// WebGPU scope does not (yet) support, matching wgslmsl's own "clear, named
// rejection, never a silently wrong or partial translation" convention.
func unsupportedJS(b *binder, msg string) goja.Value {
	return b.vm.NewTypeError("webgpu: unsupported: " + msg)
}
