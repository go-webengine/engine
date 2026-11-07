// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"fmt"

	"github.com/go-gfx/wgsl"
)

// derefType unwraps a RefType (the type go-gfx/wgsl records for a variable
// or memory-view expression — see wgsl.Info.Types' own doc comment: "The
// load rule is not recorded: it is implied by a RefType in Types") to the
// value type underneath. Every other type is returned unchanged.
func derefType(t wgsl.Type) wgsl.Type {
	if r, ok := t.(*wgsl.RefType); ok {
		return r.Elem
	}
	return t
}

// mslScalar names k's Metal Shading Language spelling. Only the four
// concrete scalar kinds a compute kernel's own buffers and arithmetic use
// are supported — f16 (a WGSL optional extension) and the abstract kinds
// (never a variable's real type, only a literal's before conversion) are
// not.
func mslScalar(k wgsl.ScalarKind) (string, error) {
	switch k {
	case wgsl.KBool:
		return "bool", nil
	case wgsl.KI32:
		return "int", nil
	case wgsl.KU32:
		return "uint", nil
	case wgsl.KF32:
		return "float", nil
	default:
		return "", unsupported("scalar type %v (only bool/i32/u32/f32)", k)
	}
}

// mslType names t's Metal Shading Language spelling, for the scalar and
// vector types this package's own bounded scope covers. See the package
// doc comment for what is deliberately out of scope (matrices, atomics,
// textures/samplers, user structs, pointers).
func mslType(t wgsl.Type) (string, error) {
	switch tt := derefType(t).(type) {
	case *wgsl.ScalarType:
		return mslScalar(tt.Kind)
	case *wgsl.VecType:
		base, err := mslScalar(tt.Elem.Kind)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s%d", base, tt.N), nil
	default:
		return "", unsupported("type %s (only scalars and vectors)", t.String())
	}
}

// scalarOrVecElemBytes is t's element byte size for the resource-binding
// size bookkeeping (Binding.ElemBytes / Result.SizeMetalIndex): 4 for any
// scalar (bool is never a storage buffer's element type in valid WGSL), 4×N
// for a vecN of one.
func scalarOrVecElemBytes(t wgsl.Type) (int, error) {
	switch tt := derefType(t).(type) {
	case *wgsl.ScalarType:
		if tt.Kind == wgsl.KBool {
			return 0, unsupported("bool as a storage buffer element type")
		}
		return 4, nil
	case *wgsl.VecType:
		if tt.Elem.Kind == wgsl.KBool {
			return 0, unsupported("vector of bool as a storage buffer element type")
		}
		return 4 * tt.N, nil
	default:
		return 0, unsupported("%s as a storage buffer element type (only scalars and vectors)", t.String())
	}
}

// atomicScalarKind returns a's own scalar kind, erroring unless it is one of
// the two kinds the WGSL spec itself permits for an atomic type ("Unless
// otherwise specified, T must be either u32 or i32" — Atomic Built-in
// Functions) — confirmed from the spec, not assumed, and confirmed again by
// compiling both kinds' own MSL spelling (atomic_uint/atomic_int) against
// the real Metal compiler before this shipped.
func atomicScalarKind(a *wgsl.AtomicType) (wgsl.ScalarKind, error) {
	s, ok := a.Elem.(*wgsl.ScalarType)
	if !ok || (s.Kind != wgsl.KU32 && s.Kind != wgsl.KI32) {
		return 0, unsupported("atomic<%s> (only atomic<u32> or atomic<i32>)", a.Elem.String())
	}
	return s.Kind, nil
}

// atomicElemBytes is a's element byte size — always 4, since only u32/i32
// are ever permitted (atomicScalarKind).
func atomicElemBytes(a *wgsl.AtomicType) (int, error) {
	if _, err := atomicScalarKind(a); err != nil {
		return 0, err
	}
	return 4, nil
}

// atomicMSLType names a's own Metal Shading Language spelling: atomic_uint
// or atomic_int (metal_atomic), the type every atomic_*_explicit built-in
// this package emits (expr.go's atomicCallExpr) operates on.
func atomicMSLType(a *wgsl.AtomicType) (string, error) {
	k, err := atomicScalarKind(a)
	if err != nil {
		return "", err
	}
	if k == wgsl.KU32 {
		return "atomic_uint", nil
	}
	return "atomic_int", nil
}
