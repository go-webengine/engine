// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package wgslmsl translates a WGSL compute shader to Metal Shading
// Language, in pure Go, for go-webengine/engine's WebGPU support
// (navigator.gpu, M5 of the go-wasmcontainers WebGPU-readiness roadmap,
// engine#345).
//
// Parsing and type-checking are go-gfx/wgsl's own job — a CTS-conformant
// WGSL frontend already in this fleet (confirmed zero CTS false rejects by
// go-wasmcontainers) — this package consumes its *wgsl.Info exactly as that
// package's own doc comment says a back end should ("Info is the result of
// name resolution and type checking. It is what a back end... consumes next
// to the AST."). An earlier spike proved an alternative (compiling naga,
// the reference WGSL compiler, to wasm32-unknown-unknown and running it
// through this engine's own WebAssembly support) WORKS end to end, verified
// bit-for-bit against real Metal — but ships a ~1.8MB opaque Rust-compiled
// binary blob and needs a Rust/wasm32 toolchain to ever rebuild, which does
// not fit this fleet's pure-Go, sovereign-build convention nearly as well
// as a small backend over an already-maintained, already-validated pure-Go
// frontend does.
//
// Deliberately scoped to the compute-shader subset a real GPGPU kernel
// needs, not the whole WGSL language — see the package's own README-style
// "Honest limits" in doc comments throughout this file rather than a
// separate document. Expand only on a confirmed real need (this package's
// own history should read like the rest of this engine's: every capability
// here was asked for).
package wgslmsl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-gfx/wgsl"
)

// Options configures Translate.
type Options struct {
	// EntryPoint selects which @compute entry point to translate when the
	// module declares more than one. Empty selects the module's only
	// compute entry point (an error if there is more than one or none).
	EntryPoint string
}

// BindingKind classifies what the host (js/webgpu.go) needs to do for one
// resolved Binding — the three resource shapes this package supports, each
// needing a different MSL parameter form and a different (or no) bounds
// accounting.
type BindingKind int

const (
	// BindingStorageArray is `var<storage, read|read_write> name: array<T>`
	// — emitted as a `device T*`/`const device T*` pointer, every index
	// clamped against this binding's own SizesIndex field of the
	// synthesised _Sizes struct (see Result.SizeMetalIndex).
	BindingStorageArray BindingKind = iota
	// BindingAtomic is `var<storage, read_write> name: atomic<u32|i32>` —
	// emitted as a `device atomic_uint*`/`device atomic_int*` pointer,
	// touched only through the WGSL atomic* built-in functions (never
	// indexed, so it needs no bounds accounting of its own).
	BindingAtomic
	// BindingUniform is `var<uniform> name: T` (T a scalar or vector) —
	// emitted as a read-only `constant T&` reference. WGSL forbids a
	// runtime-sized array in uniform address space (confirmed from the
	// spec, not assumed), so a uniform resource never needs bounds
	// accounting either.
	BindingUniform
)

func (k BindingKind) String() string {
	switch k {
	case BindingStorageArray:
		return "storage-array"
	case BindingAtomic:
		return "atomic"
	case BindingUniform:
		return "uniform"
	default:
		return "unknown"
	}
}

// Binding is one resource this translation bound to a real Metal buffer
// index, in Resources' own order (go-gfx/wgsl's EntryPoint.Resources is
// already sorted by group then binding — the same deterministic policy the
// earlier naga spike used, now simply inherited rather than recomputed).
// The GPUDevice binding code (js/webgpu.go) must bind the real GPUBuffer
// backing (Group, BindingIndex) at Metal buffer index MetalIndex when
// dispatching this kernel.
type Binding struct {
	Group, BindingIndex int
	Name                string // the WGSL variable name, for diagnostics
	MetalIndex          int
	Kind                BindingKind
	// ElemBytes is the resource's element byte size (4 for f32/i32/u32/
	// atomic<u32|i32>, 8 for vec2<f32>, 16 for vec4<f32>, …). Meaningful for
	// BindingStorageArray (the host needs it to compute this binding's own
	// SizesIndex field from the bound GPUBuffer's real byte length) and
	// reported for the other kinds too, for diagnostics, though nothing
	// consumes it there.
	ElemBytes int
	// SizesIndex is this binding's own field index within the synthesised
	// _Sizes struct (Result.SizeMetalIndex) — meaningful ONLY when Kind ==
	// BindingStorageArray; -1 for every other kind, which needs no bounds
	// accounting at all.
	SizesIndex int
}

// Result is a successful translation.
type Result struct {
	MSL string
	// EntryPointName is the MSL function name to pass to Library.Pipeline —
	// NOT always the WGSL entry point's own name (a WGSL entry point named
	// "main" collides with Metal/C++'s own reserved main, so it is renamed;
	// see identMSL).
	EntryPointName string
	WorkgroupSize  [3]int
	Bindings       []Binding
	// SizeMetalIndex is the Metal buffer index of the synthesised "_Sizes"
	// struct every kernel with at least one BindingStorageArray resource
	// takes — one uint32 per such binding (at its own SizesIndex), each the
	// bound buffer's real element COUNT (byte length / ElemBytes). This is
	// what every array index this package emits is clamped against (see
	// indexExpr) — Metal performs no bounds checking of its own, so this is
	// a real security boundary, not a correctness nicety: without it, a
	// page's own WGSL could read or write adjacent GPU memory outside its
	// bound buffer via an out-of-range index. -1 when no binding needs it
	// (no BindingStorageArray resource in this kernel).
	SizeMetalIndex int
	// SizesCount is the number of uint32 fields in the synthesised _Sizes
	// struct (0 when SizeMetalIndex is -1) — the host allocates a buffer of
	// exactly SizesCount*4 bytes and fills each BindingStorageArray
	// binding's own SizesIndex slot in it.
	SizesCount int
}

// translateError is returned for anything in src this package's own
// deliberately bounded scope does not (yet) support — a clear, named
// rejection, never a silently wrong translation.
type translateError struct{ msg string }

func (e *translateError) Error() string { return e.msg }

func unsupported(format string, a ...interface{}) error {
	return &translateError{"wgslmsl: unsupported: " + fmt.Sprintf(format, a...)}
}

// Translate parses, validates (go-gfx/wgsl) and translates src's compute
// entry point to Metal Shading Language.
func Translate(src string, opts Options) (*Result, error) {
	m, err := wgsl.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("wgslmsl: parse: %w", err)
	}
	info, errs := wgsl.Check(m)
	if err := errs.Err(); err != nil {
		return nil, fmt.Errorf("wgslmsl: check: %w", err)
	}

	ep, err := selectEntryPoint(info, opts.EntryPoint)
	if err != nil {
		return nil, err
	}
	if ep.Stage != wgsl.StageCompute {
		return nil, unsupported("entry point %q is not a @compute shader", ep.Name)
	}

	tr := &translator{info: info}
	return tr.translateEntryPoint(ep)
}

// selectEntryPoint resolves name (or the module's only compute entry point
// when name is empty) against info.EntryPoints.
func selectEntryPoint(info *wgsl.Info, name string) (*wgsl.EntryPoint, error) {
	if name != "" {
		for _, ep := range info.EntryPoints {
			if ep.Name == name {
				return ep, nil
			}
		}
		return nil, fmt.Errorf("wgslmsl: no entry point named %q", name)
	}
	var compute []*wgsl.EntryPoint
	for _, ep := range info.EntryPoints {
		if ep.Stage == wgsl.StageCompute {
			compute = append(compute, ep)
		}
	}
	switch len(compute) {
	case 0:
		return nil, fmt.Errorf("wgslmsl: module has no @compute entry point")
	case 1:
		return compute[0], nil
	default:
		names := make([]string, len(compute))
		for i, ep := range compute {
			names[i] = ep.Name
		}
		sort.Strings(names)
		return nil, fmt.Errorf("wgslmsl: module has %d @compute entry points (%s); pass Options.EntryPoint to pick one", len(compute), strings.Join(names, ", "))
	}
}
