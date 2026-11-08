// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"fmt"
	"sort"
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
	info        *wgsl.Info
	bindings    []Binding
	resources   map[*wgsl.VarObj]int // VarObj -> index into bindings
	userFuncs   map[*wgsl.FuncObj]bool
	helperExtra map[*wgsl.FuncObj]*helperExtraParams
	sizesCount  int // number of BindingStorageArray resources seen so far
	indent      int
	buf         strings.Builder
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

	if err := tr.emitHelperFuncs(ep); err != nil {
		return nil, err
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

// helperExtraParams is what emitHelperFuncs threads into a helper's own MSL
// signature, beyond its WGSL-declared parameters, so it can reach whatever
// resource it (or anything it itself calls) touches — a resource is NEVER
// visible to an ordinary MSL function just because it is in scope in WGSL
// (module-scope there, but Metal only ever binds a buffer to an ENTRY
// kernel's own [[buffer(N)]] parameter list; every other function sees one
// only if it is handed one, like any other argument).
type helperExtraParams struct {
	// resources is every resource this function (or, transitively, a
	// function it calls) needs, in tr.bindings order (a deterministic,
	// binding-ordered parameter list, not source order).
	resources []*wgsl.VarObj
	// needsSizes is true when any entry of resources is BindingStorageArray
	// — such a resource's own index clamp (indexExpr) always reads _sizes,
	// so whatever function reaches it (directly, or only by passing the
	// resource on to a callee) must receive _sizes too.
	needsSizes bool
}

// emitHelperFuncs emits, as real MSL functions, every WGSL function the
// entry point (transitively) calls — in callee-before-caller order, since
// MSL/C++ requires a function be declared above any call to it, unlike
// WGSL, which has no such restriction (a function may call one declared
// later in the source). A helper that touches a resource (directly, or via
// a callee that needs one) gets it threaded through as an extra parameter
// (helperExtraParams) — go-wasmcontainers' own confirmed real shape: an
// "index math" helper reading a module-scope storage/uniform buffer
// directly, not through its own WGSL parameter list. Populates tr.userFuncs
// so callExpr can recognize a call to one of these, rather than rejecting
// it as unknown.
func (tr *translator) emitHelperFuncs(ep *wgsl.EntryPoint) error {
	tr.userFuncs = map[*wgsl.FuncObj]bool{}
	tr.helperExtra = map[*wgsl.FuncObj]*helperExtraParams{}
	for _, f := range orderedHelperFuncs(tr.info, ep) {
		extra, err := tr.resolveHelperExtra(f)
		if err != nil {
			return err
		}
		tr.helperExtra[f] = extra

		retMSL := "void"
		if f.Ret != wgsl.Void {
			m, err := mslType(f.Ret)
			if err != nil {
				return err
			}
			retMSL = m
		}
		params := make([]string, 0, len(f.Params)+len(extra.resources)+1)
		for _, p := range f.Params {
			m, err := mslType(p.Type)
			if err != nil {
				return err
			}
			params = append(params, fmt.Sprintf("%s %s", m, identMSL(p.Name)))
		}
		for _, vo := range extra.resources {
			decl, err := tr.resourceParamDecl(vo)
			if err != nil {
				return err
			}
			params = append(params, fmt.Sprintf("%s %s", decl, identMSL(vo.Name)))
		}
		if extra.needsSizes {
			params = append(params, "constant _Sizes& _sizes")
		}
		tr.line("%s %s(%s) {", retMSL, identMSL(f.Name), strings.Join(params, ", "))
		tr.indent++
		if err := tr.stmtBlock(f.Decl.Body); err != nil {
			return err
		}
		tr.indent--
		tr.line("}")
		tr.buf.WriteByte('\n')
		tr.userFuncs[f] = true
	}
	return nil
}

// resolveHelperExtra computes f's own helperExtraParams: the resources f
// directly references by identifier, UNION the resources every function f
// calls already needs (tr.helperExtra, already resolved — orderedHelperFuncs
// guarantees every callee is processed before f). Ordered by tr.bindings
// index (Metal buffer order), not encounter order, so two helpers needing
// the same two resources always list them the same way — not meaningful on
// its own, just a deterministic choice over an arbitrary one.
func (tr *translator) resolveHelperExtra(f *wgsl.FuncObj) (*helperExtraParams, error) {
	need := map[*wgsl.VarObj]bool{}
	for _, vo := range tr.directResources(f) {
		need[vo] = true
	}
	for _, callee := range funcCallees(tr.info, f) {
		if ci := tr.helperExtra[callee]; ci != nil {
			for _, vo := range ci.resources {
				need[vo] = true
			}
		}
	}
	resources := make([]*wgsl.VarObj, 0, len(need))
	for vo := range need {
		resources = append(resources, vo)
	}
	sort.Slice(resources, func(i, j int) bool {
		return tr.resources[resources[i]] < tr.resources[resources[j]]
	})
	needsSizes := false
	for _, vo := range resources {
		if tr.bindings[tr.resources[vo]].Kind == BindingStorageArray {
			needsSizes = true
		}
	}
	return &helperExtraParams{resources: resources, needsSizes: needsSizes}, nil
}

// directResources returns the distinct resource VarObjs f's own body
// directly references by identifier (never transitively through a callee —
// resolveHelperExtra's own job), in encounter order.
func (tr *translator) directResources(f *wgsl.FuncObj) []*wgsl.VarObj {
	var out []*wgsl.VarObj
	seen := map[*wgsl.VarObj]bool{}
	walkFuncBody(f.Decl.Body, func(e wgsl.Expr) {
		id, ok := e.(*wgsl.Ident)
		if !ok {
			return
		}
		vo, ok := tr.info.Objects[id].(*wgsl.VarObj)
		if !ok || seen[vo] {
			return
		}
		if _, tracked := tr.resources[vo]; !tracked {
			return
		}
		seen[vo] = true
		out = append(out, vo)
	})
	return out
}

// orderedHelperFuncs returns every non-entry function ep's entry point
// (transitively) calls, each AFTER every function it itself calls — a
// postorder walk of the call graph, dropping the entry function itself
// (never called, by go-gfx/wgsl's own "entry points must not be called"
// rule, so it is always the walk's own last, outermost frame). Built from
// funcCallees, not FuncObj.Calls: go-gfx/wgsl's own msl package (the
// upstream, non-entry-point MSL emitter for a library of plain functions)
// documents, at its own equivalent call site, that "the checker fills
// FuncObj.Calls only for the functions of an entry point" — confirmed
// directly here too (a *wgsl.FuncObj's own .Calls is empty even for a
// function this package's own entry point actually calls).
func orderedHelperFuncs(info *wgsl.Info, ep *wgsl.EntryPoint) []*wgsl.FuncObj {
	var order []*wgsl.FuncObj
	seen := map[*wgsl.FuncObj]bool{}
	var visit func(f *wgsl.FuncObj)
	visit = func(f *wgsl.FuncObj) {
		if seen[f] {
			return
		}
		seen[f] = true
		for _, callee := range funcCallees(info, f) {
			visit(callee)
		}
		order = append(order, f)
	}
	visit(ep.Func)
	return order[:len(order)-1]
}

// funcCallees returns the FuncObjs f's body directly calls, deduplicated,
// in the order first encountered — found by walking the body (see
// orderedHelperFuncs's own doc comment for why FuncObj.Calls cannot be
// trusted here).
func funcCallees(info *wgsl.Info, f *wgsl.FuncObj) []*wgsl.FuncObj {
	var callees []*wgsl.FuncObj
	seen := map[*wgsl.FuncObj]bool{}
	walkFuncBody(f.Decl.Body, func(e wgsl.Expr) {
		c, ok := e.(*wgsl.Call)
		if !ok {
			return
		}
		g, ok := info.Objects[c.Fn].(*wgsl.FuncObj)
		if !ok || seen[g] {
			return
		}
		seen[g] = true
		callees = append(callees, g)
	})
	return callees
}

// walkFuncBody calls visit on every expression reachable from b, however
// deeply nested in a statement or another expression — mirroring stmt.go's
// and expr.go's own recursion exactly (every statement and expression kind
// either file's own switch recognizes), since this package's bounded scope
// means anything NOT covered here is already rejected by stmt/expr before
// the result could matter: a construct added to either file's own switch
// should be added here too.
func walkFuncBody(b *wgsl.Block, visit func(wgsl.Expr)) {
	for _, s := range b.Stmts {
		walkFuncStmt(s, visit)
	}
}

func walkFuncStmt(s wgsl.Stmt, visit func(wgsl.Expr)) {
	switch st := s.(type) {
	case *wgsl.Block:
		walkFuncBody(st, visit)
	case *wgsl.DeclStmt:
		switch d := st.Decl.(type) {
		case *wgsl.Var:
			walkFuncExpr(d.Init, visit)
		case *wgsl.Value:
			walkFuncExpr(d.Init, visit)
		}
	case *wgsl.Assign:
		walkFuncExpr(st.LHS, visit)
		walkFuncExpr(st.RHS, visit)
	case *wgsl.IncDec:
		walkFuncExpr(st.X, visit)
	case *wgsl.If:
		walkFuncExpr(st.Cond, visit)
		walkFuncStmt(st.Then, visit)
		if st.Else != nil {
			walkFuncStmt(st.Else, visit)
		}
	case *wgsl.For:
		if st.Init != nil {
			walkFuncStmt(st.Init, visit)
		}
		walkFuncExpr(st.Cond, visit)
		if st.Update != nil {
			walkFuncStmt(st.Update, visit)
		}
		walkFuncBody(st.Body, visit)
	case *wgsl.While:
		walkFuncExpr(st.Cond, visit)
		walkFuncBody(st.Body, visit)
	case *wgsl.Loop:
		for _, s := range st.Body {
			walkFuncStmt(s, visit)
		}
		if st.Cont != nil {
			for _, s := range st.Cont.Stmts {
				if bi, ok := s.(*wgsl.BreakIf); ok {
					walkFuncExpr(bi.Cond, visit)
					continue
				}
				walkFuncStmt(s, visit)
			}
		}
	case *wgsl.Return:
		walkFuncExpr(st.X, visit)
	case *wgsl.CallStmt:
		walkFuncExpr(st.Call, visit)
	}
	// *wgsl.Empty, *wgsl.AssertStmt, *wgsl.Break, *wgsl.Continue: no
	// expression of interest (const_assert is compile-time-only, matching
	// stmt.go's own treatment).
}

func walkFuncExpr(e wgsl.Expr, visit func(wgsl.Expr)) {
	if e == nil {
		return
	}
	visit(e)
	switch x := e.(type) {
	case *wgsl.Paren:
		walkFuncExpr(x.X, visit)
	case *wgsl.Unary:
		walkFuncExpr(x.X, visit)
	case *wgsl.Binary:
		walkFuncExpr(x.X, visit)
		walkFuncExpr(x.Y, visit)
	case *wgsl.Index:
		walkFuncExpr(x.X, visit)
		walkFuncExpr(x.I, visit)
	case *wgsl.Member:
		walkFuncExpr(x.X, visit)
	case *wgsl.Call:
		for _, a := range x.Args {
			walkFuncExpr(a, visit)
		}
	}
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

// resourceParamDecl is r's own MSL parameter TYPE (qualifier, element type,
// pointer/reference sigil — no name, no [[buffer(N)]] attribute, which only
// ever belongs on the entry kernel's own top-level parameter list, never on
// an ordinary MSL function): the one per-Kind shape every place that binds
// r as a parameter needs (entryParams for the kernel itself, emitHelperFuncs
// for a helper function that needs r threaded through to reach it).
func (tr *translator) resourceParamDecl(r *wgsl.VarObj) (string, error) {
	b := tr.bindings[tr.resources[r]]
	switch b.Kind {
	case BindingStorageArray:
		arr := derefType(r.Store).(*wgsl.ArrayType)
		elemMSL, err := mslType(arr.Elem)
		if err != nil {
			return "", err
		}
		qualifier := "device"
		if !r.Access.Writable() {
			qualifier = "const device"
		}
		return fmt.Sprintf("%s %s*", qualifier, elemMSL), nil
	case BindingAtomic:
		at := derefType(r.Store).(*wgsl.AtomicType)
		atomicMSL, err := atomicMSLType(at)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("device %s*", atomicMSL), nil
	case BindingUniform:
		uMSL, err := mslType(derefType(r.Store))
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("constant %s&", uMSL), nil
	}
	return "", unsupported("resource %q: unknown binding kind", r.Name)
}

// entryParams builds the kernel function's own parameter list: one
// `[[buffer(N)]]` parameter per resolved resource (its own MSL shape from
// resourceParamDecl), the `_Sizes` struct if sizeIndex >= 0, then one
// builtin parameter per ep.Inputs entry.
func (tr *translator) entryParams(ep *wgsl.EntryPoint, sizeIndex int) ([]string, error) {
	var params []string
	for _, r := range ep.Resources {
		decl, err := tr.resourceParamDecl(r)
		if err != nil {
			return nil, err
		}
		b := tr.bindings[tr.resources[r]]
		params = append(params, fmt.Sprintf("%s %s [[buffer(%d)]]", decl, identMSL(r.Name), b.MetalIndex))
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
