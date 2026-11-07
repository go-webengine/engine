// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"fmt"
	"strings"

	"github.com/go-gfx/wgsl"
)

// cppReserved names identifiers WGSL permits (go-gfx/wgsl's own reserved-word
// set is the WGSL spec's, a different list) but that collide with a Metal
// Shading Language or host C++ keyword this package's own generated code
// also uses (device/constant/threadgroup/kernel/…) or that MSL inherits
// from C++. A WGSL identifier equal to one of these is mangled (identMSL)
// rather than ever emitted verbatim.
var cppReserved = map[string]bool{
	"main": true, "class": true, "template": true, "namespace": true,
	"using": true, "typename": true, "operator": true, "new": true,
	"delete": true, "this": true, "nullptr": true, "friend": true,
	"public": true, "private": true, "protected": true, "virtual": true,
	"explicit": true, "mutable": true, "register": true, "restrict": true,
	"signed": true, "unsigned": true, "union": true, "volatile": true,
	"device": true, "constant": true, "threadgroup": true, "thread": true,
	"kernel": true, "vertex": true, "fragment": true, "metal": true,
	"half": true, "char": true, "short": true, "long": true, "wchar_t": true,
}

// identMSL is name, mangled if it would otherwise collide with an MSL/C++
// keyword this package's own generated code relies on meaning something
// else. Applied to every WGSL identifier this package emits — variables,
// parameters and the entry point's own function name alike — so a WGSL
// kernel legally named e.g. `main` or `device` (both valid WGSL
// identifiers, neither a WGSL keyword) still compiles.
func identMSL(name string) string {
	if cppReserved[name] {
		return name + "_"
	}
	return name
}

// builtinAttr maps a WGSL entry-point builtin input name (spec "Built-in
// Values", restricted to the compute-shader-relevant subset) to its Metal
// Shading Language attribute.
func builtinAttr(name string) (string, error) {
	switch name {
	case "global_invocation_id":
		return "thread_position_in_grid", nil
	case "local_invocation_id":
		return "thread_position_in_threadgroup", nil
	case "local_invocation_index":
		return "thread_index_in_threadgroup", nil
	case "workgroup_id":
		return "threadgroup_position_in_grid", nil
	case "num_workgroups":
		return "threadgroups_per_grid", nil
	default:
		return "", unsupported("compute entry point builtin %q", name)
	}
}

// translator holds the state of one Translate call: the type/object
// information go-gfx/wgsl's Check produced, the resource bindings
// discovered so far (in Metal buffer index order), and the output buffer.
type translator struct {
	info       *wgsl.Info
	bindings   []Binding
	resources  map[*wgsl.VarObj]int // VarObj -> index into bindings
	sizesCount int                  // number of BindingStorageArray resources seen so far
	indent     int
	buf        strings.Builder
}

func (tr *translator) line(format string, a ...interface{}) {
	tr.buf.WriteString(strings.Repeat("    ", tr.indent))
	fmt.Fprintf(&tr.buf, format, a...)
	tr.buf.WriteByte('\n')
}

// translateEntryPoint is the whole pipeline for one already-selected
// @compute entry point: resolve and bounds-validate its resources, emit the
// synthesised buffer-sizes struct (if any resource needs bounds checking),
// then the kernel function itself.
func (tr *translator) translateEntryPoint(ep *wgsl.EntryPoint) (*Result, error) {
	if err := tr.resolveResources(ep); err != nil {
		return nil, err
	}

	tr.line("#include <metal_stdlib>")
	tr.line("#include <simd/simd.h>")
	tr.line("using namespace metal;")
	tr.buf.WriteByte('\n')

	sizeIndex := -1
	if tr.sizesCount > 0 {
		sizeIndex = len(tr.bindings)
		tr.line("struct _Sizes {")
		tr.indent++
		for i := 0; i < tr.sizesCount; i++ {
			tr.line("uint s%d;", i)
		}
		tr.indent--
		tr.line("};")
		tr.buf.WriteByte('\n')
	}

	entryName := identMSL(ep.Name)
	ws := ep.WorkgroupSize
	for i, e := range ep.WorkgroupSizeExprs {
		if e != nil {
			return nil, unsupported("override-expression workgroup_size dimension %d", i)
		}
	}

	params, err := tr.entryParams(ep, sizeIndex)
	if err != nil {
		return nil, err
	}

	tr.line("kernel void %s(", entryName)
	tr.indent++
	for i, p := range params {
		sep := ","
		if i == len(params)-1 {
			sep = ""
		}
		tr.line("%s%s", p, sep)
	}
	tr.indent--
	tr.line(") {")
	tr.indent++
	if err := tr.stmtBlock(ep.Func.Decl.Body); err != nil {
		return nil, err
	}
	tr.indent--
	tr.line("}")

	return &Result{
		MSL:            tr.buf.String(),
		EntryPointName: entryName,
		WorkgroupSize:  [3]int{int(ws[0]), int(ws[1]), int(ws[2])},
		Bindings:       tr.bindings,
		SizeMetalIndex: sizeIndex,
		SizesCount:     tr.sizesCount,
	}, nil
}

// resolveResources walks ep.Resources (already sorted by group then binding
// — go-gfx/wgsl's own deterministic order) and assigns each a Metal buffer
// index in that same order. Three resource shapes are supported (see
// BindingKind's own doc comment for the full rationale of each): a
// runtime-sized storage array of scalar or vector element type, a bare
// storage atomic<u32|i32>, and a bare uniform scalar or vector — the shapes
// confirmed needed directly by go-wasmcontainers (engine#345 follow-up: a
// `var<uniform> budget: u32` loop-iteration budget paired with a
// `var<storage, read_write> flag: atomic<u32>` set via atomicStore before
// breaking, in every one of their looping compute kernels).
func (tr *translator) resolveResources(ep *wgsl.EntryPoint) error {
	tr.resources = map[*wgsl.VarObj]int{}
	for _, r := range ep.Resources {
		switch r.AS {
		case wgsl.ASStorage:
			switch store := derefType(r.Store).(type) {
			case *wgsl.ArrayType:
				if !store.Runtime {
					return unsupported("resource %q: a fixed-size storage array (only a runtime-sized one, e.g. array<f32>)", r.Name)
				}
				elemBytes, err := scalarOrVecElemBytes(store.Elem)
				if err != nil {
					return err
				}
				tr.addBinding(r, BindingStorageArray, elemBytes)
			case *wgsl.AtomicType:
				elemBytes, err := atomicElemBytes(store)
				if err != nil {
					return err
				}
				tr.addBinding(r, BindingAtomic, elemBytes)
			default:
				return unsupported("resource %q of type %s (only a runtime-sized storage array or a bare atomic<u32|i32>)", r.Name, r.Store.String())
			}
		case wgsl.ASUniform:
			elemBytes, err := scalarOrVecElemBytes(r.Store)
			if err != nil {
				return unsupported("resource %q of type %s (only a scalar or vector <uniform>)", r.Name, r.Store.String())
			}
			tr.addBinding(r, BindingUniform, elemBytes)
		default:
			return unsupported("resource %q in address space %s (only <storage> and <uniform>)", r.Name, r.AS)
		}
	}
	return nil
}

// addBinding records r as a resolved Binding of kind at the next Metal
// buffer index, allocating it a _Sizes struct slot too when kind ==
// BindingStorageArray.
func (tr *translator) addBinding(r *wgsl.VarObj, kind BindingKind, elemBytes int) {
	idx := len(tr.bindings)
	sizesIndex := -1
	if kind == BindingStorageArray {
		sizesIndex = tr.sizesCount
		tr.sizesCount++
	}
	tr.resources[r] = idx
	tr.bindings = append(tr.bindings, Binding{
		Group:        intOr(r.Group, 0),
		BindingIndex: intOr(r.Binding, 0),
		Name:         r.Name,
		MetalIndex:   idx,
		Kind:         kind,
		ElemBytes:    elemBytes,
		SizesIndex:   sizesIndex,
	})
}

func intOr(p *int64, def int) int {
	if p == nil {
		return def
	}
	return int(*p)
}

// entryParams builds the kernel function's own parameter list: one
// `[[buffer(N)]]` parameter per resolved resource (its own MSL shape
// depending on its Kind — see BindingKind's own doc comment), the `_Sizes`
// struct if sizeIndex >= 0, then one builtin parameter per ep.Inputs entry.
func (tr *translator) entryParams(ep *wgsl.EntryPoint, sizeIndex int) ([]string, error) {
	var params []string
	for _, r := range ep.Resources {
		b := tr.bindings[tr.resources[r]]
		switch b.Kind {
		case BindingStorageArray:
			arr := derefType(r.Store).(*wgsl.ArrayType)
			elemMSL, err := mslType(arr.Elem)
			if err != nil {
				return nil, err
			}
			qualifier := "device"
			if !r.Access.Writable() {
				qualifier = "const device"
			}
			params = append(params, fmt.Sprintf("%s %s* %s [[buffer(%d)]]", qualifier, elemMSL, identMSL(r.Name), b.MetalIndex))
		case BindingAtomic:
			at := derefType(r.Store).(*wgsl.AtomicType)
			atomicMSL, err := atomicMSLType(at)
			if err != nil {
				return nil, err
			}
			params = append(params, fmt.Sprintf("device %s* %s [[buffer(%d)]]", atomicMSL, identMSL(r.Name), b.MetalIndex))
		case BindingUniform:
			uMSL, err := mslType(derefType(r.Store))
			if err != nil {
				return nil, err
			}
			params = append(params, fmt.Sprintf("constant %s& %s [[buffer(%d)]]", uMSL, identMSL(r.Name), b.MetalIndex))
		}
	}
	if sizeIndex >= 0 {
		params = append(params, fmt.Sprintf("constant _Sizes& _sizes [[buffer(%d)]]", sizeIndex))
	}
	for _, in := range ep.Inputs {
		if in.Member != "" {
			return nil, unsupported("struct-typed entry point input %q", in.Name)
		}
		if in.Builtin == "" {
			return nil, unsupported("non-builtin entry point input %q", in.Name)
		}
		attr, err := builtinAttr(in.Builtin)
		if err != nil {
			return nil, err
		}
		paramMSL, err := mslType(in.Type)
		if err != nil {
			return nil, err
		}
		params = append(params, fmt.Sprintf("%s %s [[%s]]", paramMSL, identMSL(in.Name), attr))
	}
	return params, nil
}
