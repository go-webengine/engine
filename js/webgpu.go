// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"github.com/dop251/goja"
	"github.com/go-macos/metal"
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

	queue := b.vm.NewObject()
	queue.Set("writeBuffer", func(call goja.FunctionCall) goja.Value {
		return b.gpuQueueWriteBuffer(call)
	})
	queue.Set("onSubmittedWorkDone", func(call goja.FunctionCall) goja.Value {
		// Nothing can be pending: this binding has no queue.submit() yet
		// (the rest of the compute pipeline — shader modules, bind groups,
		// command encoding — is M5's own next round), so "every submitted
		// unit of work has completed" is vacuously, correctly true.
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
