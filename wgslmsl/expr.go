// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/go-gfx/wgsl"
)

// exprType is e's effective type for codegen purposes: the type its context
// converts it to (info.Conversions), when go-gfx/wgsl recorded one, else its
// own static type (info.Types), dereferenced (derefType) past the RefType a
// variable or memory-view expression carries.
func (tr *translator) exprType(e wgsl.Expr) wgsl.Type {
	if t, ok := tr.info.Conversions[e]; ok {
		return derefType(t)
	}
	return derefType(tr.info.Types[e])
}

// mslBuiltinFn maps a WGSL built-in function name to its Metal Shading
// Language spelling — identical for the overwhelming majority (both
// languages' numeric built-ins trace to the same C-family math library
// lineage), renamed for the few that differ. Anything not listed here is
// out of this package's own bounded scope (texture/sampler/atomic/pack/
// bit-manipulation built-ins, and the handful of numeric ones with no
// confirmed real use yet) and is rejected by name, never silently passed
// through — an unmapped function name reaching Metal's compiler as-is could
// accidentally compile to something ELSE if Metal happens to define a
// same-named symbol with different semantics.
var mslBuiltinFn = map[string]string{
	"abs": "abs", "min": "min", "max": "max", "clamp": "clamp",
	"floor": "floor", "ceil": "ceil", "round": "round", "trunc": "trunc",
	"fract": "fract", "sqrt": "sqrt", "pow": "pow", "exp": "exp",
	"exp2": "exp2", "log": "log", "log2": "log2", "sin": "sin", "cos": "cos",
	"tan": "tan", "asin": "asin", "acos": "acos", "atan": "atan",
	"atan2": "atan2", "sign": "sign", "step": "step", "mix": "mix",
	"dot": "dot", "cross": "cross", "length": "length",
	"normalize": "normalize", "distance": "distance", "select": "select",
	"all": "all", "any": "any", "inverseSqrt": "rsqrt",
}

func (tr *translator) expr(e wgsl.Expr) (string, error) {
	switch x := e.(type) {
	case *wgsl.IntLit:
		return tr.litMSL(e, x.Text)
	case *wgsl.FloatLit:
		return tr.litMSL(e, x.Text)
	case *wgsl.BoolLit:
		if x.Value {
			return "true", nil
		}
		return "false", nil
	case *wgsl.Ident:
		return identMSL(x.Name), nil
	case *wgsl.Paren:
		inner, err := tr.expr(x.X)
		if err != nil {
			return "", err
		}
		return "(" + inner + ")", nil
	case *wgsl.Unary:
		return tr.unaryExpr(x)
	case *wgsl.Binary:
		return tr.binaryExpr(x)
	case *wgsl.Index:
		return tr.indexExpr(x)
	case *wgsl.Member:
		return tr.memberExpr(x)
	case *wgsl.Call:
		return tr.callExpr(x)
	default:
		return "", unsupported("expression %T", e)
	}
}

// litMSL formats an integer or float literal's exact MSL spelling, honouring
// the CONCRETE type Check resolved it to (tr.exprType), never the literal's
// own source-text suffix alone: the same unsuffixed `0` can mean i32, u32 or
// f32 depending entirely on where it is used (WGSL's abstract-numeric
// concretization), and getting this wrong would silently change arithmetic
// (integer vs. floating-point division, in particular).
func (tr *translator) litMSL(e wgsl.Expr, text string) (string, error) {
	digits := strings.TrimRight(text, "iuhfIUHF")
	t := tr.exprType(e)
	scalar, ok := t.(*wgsl.ScalarType)
	if !ok {
		return "", unsupported("literal of non-scalar type %s", t.String())
	}
	switch scalar.Kind {
	case wgsl.KI32, wgsl.KAbstractInt:
		n, err := strconv.ParseInt(digits, 0, 64)
		if err != nil {
			return "", fmt.Errorf("wgslmsl: literal %q: %w", text, err)
		}
		return strconv.FormatInt(n, 10), nil
	case wgsl.KU32:
		n, err := strconv.ParseUint(digits, 0, 64)
		if err != nil {
			// A float-looking literal (e.g. "2.0") concretized to u32 would
			// land here as a parse failure — not valid WGSL in the first
			// place (Check would already have rejected it), so this is
			// defensive, not a reachable case for Check-passed input.
			return "", fmt.Errorf("wgslmsl: literal %q as u32: %w", text, err)
		}
		return strconv.FormatUint(n, 10) + "u", nil
	case wgsl.KF32, wgsl.KAbstractFloat:
		f, err := strconv.ParseFloat(digits, 64)
		if err != nil {
			// An integer-looking literal (e.g. "2") concretized to f32 is
			// valid WGSL (abstract-int converts to f32) — ParseFloat
			// accepts plain integer text too, so this only fails on a
			// genuinely malformed literal.
			return "", fmt.Errorf("wgslmsl: literal %q as f32: %w", text, err)
		}
		s := strconv.FormatFloat(f, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s + "f", nil
	default:
		return "", unsupported("literal of scalar kind %v", scalar.Kind)
	}
}

func (tr *translator) unaryExpr(u *wgsl.Unary) (string, error) {
	switch u.Op {
	case "-", "!", "~":
		x, err := tr.expr(u.X)
		if err != nil {
			return "", err
		}
		return u.Op + "(" + x + ")", nil
	default:
		return "", unsupported("unary operator %q (pointers are out of scope)", u.Op)
	}
}

func (tr *translator) binaryExpr(b *wgsl.Binary) (string, error) {
	x, err := tr.expr(b.X)
	if err != nil {
		return "", err
	}
	y, err := tr.expr(b.Y)
	if err != nil {
		return "", err
	}
	if b.Op == "%" && isFloaty(tr.exprType(b.X)) {
		// MSL/C++'s "%" is integer-only; WGSL's own "%" is defined on
		// floating-point operands too (spec: "truncated division
		// remainder"), which Metal's fmod matches exactly.
		return fmt.Sprintf("metal::fmod(%s, %s)", x, y), nil
	}
	return fmt.Sprintf("(%s %s %s)", x, b.Op, y), nil
}

func isFloaty(t wgsl.Type) bool {
	switch tt := t.(type) {
	case *wgsl.ScalarType:
		return tt.Kind == wgsl.KF32 || tt.Kind == wgsl.KAbstractFloat
	case *wgsl.VecType:
		return tt.Elem.Kind == wgsl.KF32
	}
	return false
}

// indexExpr emits x[i], CLAMPING i first — Metal performs no bounds
// checking of its own, so this is the one place a page's own (untrusted)
// WGSL could otherwise read or write memory outside its bound buffer via an
// out-of-range index. Two cases both get a real, runtime clamp: a resolved
// resource-array binding (clamped against its own entry of the synthesised
// _sizes struct — see Result.SizeMetalIndex's own doc comment) and a vector
// (clamped against its own, statically known, component count — WGSL's own
// spec requires dynamic vector-component access to be bounds-checked too,
// not just array access).
func (tr *translator) indexExpr(ix *wgsl.Index) (string, error) {
	i, err := tr.expr(ix.I)
	if err != nil {
		return "", err
	}
	if r, ok := tr.resourceOf(ix.X); ok {
		x, err := tr.expr(ix.X)
		if err != nil {
			return "", err
		}
		b := tr.bindings[tr.resources[r]]
		clamped := fmt.Sprintf("metal::min(uint(%s), metal::max(_sizes.s%d, 1u) - 1u)", i, b.SizesIndex)
		return fmt.Sprintf("%s[%s]", x, clamped), nil
	}
	x, err := tr.expr(ix.X)
	if err != nil {
		return "", err
	}
	if vt, ok := tr.exprType(ix.X).(*wgsl.VecType); ok {
		clamped := fmt.Sprintf("metal::clamp(uint(%s), 0u, %du)", i, vt.N-1)
		return fmt.Sprintf("%s[%s]", x, clamped), nil
	}
	return "", unsupported("indexing a %s (only resource-array and vector indexing are supported)", tr.exprType(ix.X).String())
}

// resourceOf reports whether e (after unwrapping any parentheses) is a
// direct reference to one of this translation's own resolved resource
// bindings — the one shape `resourceVar[i]` an Index's base is ever allowed
// to be in this package's bounded scope (a runtime-sized array can only be
// indexed directly, never read as a whole value or stored in a local, so no
// deeper expression shape needs to be checked here).
func (tr *translator) resourceOf(e wgsl.Expr) (*wgsl.VarObj, bool) {
	if p, ok := e.(*wgsl.Paren); ok {
		return tr.resourceOf(p.X)
	}
	id, ok := e.(*wgsl.Ident)
	if !ok {
		return nil, false
	}
	vo, ok := tr.info.Objects[id].(*wgsl.VarObj)
	if !ok {
		return nil, false
	}
	_, tracked := tr.resources[vo]
	return vo, tracked
}

// memberExpr emits x.name: a vector swizzle (the only member-access shape
// this package's bounded scope supports — no user structs).
func (tr *translator) memberExpr(m *wgsl.Member) (string, error) {
	if !isSwizzle(m.Name) {
		return "", unsupported("member access %q (only vector swizzles are supported; no user structs)", m.Name)
	}
	x, err := tr.expr(m.X)
	if err != nil {
		return "", err
	}
	return x + "." + m.Name, nil
}

func isSwizzle(name string) bool {
	if len(name) == 0 || len(name) > 4 {
		return false
	}
	for _, r := range name {
		switch r {
		case 'x', 'y', 'z', 'w', 'r', 'g', 'b', 'a':
		default:
			return false
		}
	}
	return true
}

// callExpr emits a type constructor (`vec3<f32>(...)`, `f32(x)`, …) or a
// built-in function call (mslBuiltinFn). A call to a user-defined function
// never reaches here: Translate already rejects any entry point that
// reaches more than its own one function (see the package doc comment).
// atomicBuiltinFn maps a WGSL atomic built-in function name to its Metal
// Shading Language spelling (metal_atomic) — confirmed by compiling every
// one of these against the real Metal compiler before this shipped, not
// assumed from documentation: exchange has no "fetch" in its MSL name
// (atomic_exchange_explicit), unlike the read-modify-write arithmetic/
// bitwise ops, which all do. atomicCompareExchangeWeak is deliberately not
// supported yet (its WGSL return type, a `__atomic_compare_exchange_result`
// struct, is a kind this package's own bounded scope has no story for) —
// no confirmed real need for it either, unlike every op below.
var atomicBuiltinFn = map[string]string{
	"atomicLoad": "atomic_load_explicit", "atomicStore": "atomic_store_explicit",
	"atomicAdd": "atomic_fetch_add_explicit", "atomicSub": "atomic_fetch_sub_explicit",
	"atomicMax": "atomic_fetch_max_explicit", "atomicMin": "atomic_fetch_min_explicit",
	"atomicAnd": "atomic_fetch_and_explicit", "atomicOr": "atomic_fetch_or_explicit",
	"atomicXor": "atomic_fetch_xor_explicit", "atomicExchange": "atomic_exchange_explicit",
}

// atomicCallExpr emits one WGSL atomic built-in call. Its first argument is
// always `&resource` (WGSL requires a pointer to the atomic object; spec
// "Atomic Built-in Functions": the atomic_ptr parameter type is
// ptr<AS, atomic<T>, read_write>) — since this package's own atomic
// resource is ALREADY emitted as a real MSL pointer parameter (device
// atomic_uint* name), translating `&resource` is simply the bare parameter
// name, never an actual address-of in the generated code. Every WGSL atomic
// built-in uses one single, fixed memory ordering (spec: "All atomic
// built-in functions use a relaxed memory ordering"), which is why every
// MSL call this emits hard-codes memory_order_relaxed rather than accepting
// one as an argument — there is no WGSL-level ordering to translate.
func (tr *translator) atomicCallExpr(mslName string, c *wgsl.Call) (string, error) {
	if len(c.Args) == 0 {
		return "", unsupported("%s: missing the atomic pointer argument", c.Fn.Name)
	}
	ptrExpr, ok := c.Args[0].(*wgsl.Unary)
	if !ok || ptrExpr.Op != "&" {
		return "", unsupported("%s: the first argument must be &resource (a pointer to a storage atomic<T>)", c.Fn.Name)
	}
	if _, tracked := tr.resourceOf(ptrExpr.X); !tracked {
		return "", unsupported("%s: the first argument must be a tracked atomic<T> storage resource", c.Fn.Name)
	}
	ptr, err := tr.expr(ptrExpr.X)
	if err != nil {
		return "", err
	}
	args := []string{ptr}
	for _, a := range c.Args[1:] {
		s, err := tr.expr(a)
		if err != nil {
			return "", err
		}
		args = append(args, s)
	}
	args = append(args, "memory_order_relaxed")
	return fmt.Sprintf("metal::%s(%s)", mslName, strings.Join(args, ", ")), nil
}

// bitcastExpr implements WGSL's bitcast<T>(e) (spec §17.2 Bit Reinterpretation
// Built-in Functions): a REINTERPRETATION of e's bits as T, same size, never
// a numeric conversion — bitcast<f32>(1u) is the float whose bits are the
// integer 1 (a tiny subnormal), not 1.0. callExpr's own generic "callee is a
// type name" branch would otherwise emit a C-style conversion (float(x)),
// giving silently wrong values whenever real WGSL content relies on bitcast
// (confirmed directly: go-wasmcontainers' own Gemm/MatMul kernels decode an
// f32 alpha stored as raw bits in a u32 uniform buffer, reading ~1e9×
// (0x3F800000, 1.0f's own bits read as an integer) too large before this
// fix). Metal's own as_type<T> is exactly this bit reinterpretation, for a
// scalar AND a same-arity vector alike, so one emission covers both.
func (tr *translator) bitcastExpr(c *wgsl.Call) (string, error) {
	if len(c.Args) != 1 {
		return "", unsupported("bitcast: exactly one argument is required")
	}
	arg, err := tr.expr(c.Args[0])
	if err != nil {
		return "", err
	}
	typeMSL, err := mslType(tr.exprType(c))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("as_type<%s>(%s)", typeMSL, arg), nil
}

// arrayLengthExpr implements WGSL's arrayLength(p: ptr<storage, array<T>>)
// -> u32 (spec §17.6): the runtime-sized storage array's own element
// count. General `&`/pointer support is deliberately out of this
// package's own bounded scope (confirmed directly needed only for this
// ONE shape: go-wasmcontainers' own batch/batch-grant kernel,
// `if (id.x < arrayLength(&out)) { ... }`, has no atomics and no
// ptr<function, T> parameters at all) — so `&` is recognized ONLY as
// arrayLength's own first argument, never as a generally-evaluable unary
// expression (unaryExpr's own default case still rejects it, matching
// atomicCallExpr's identical precedent for `&resource` inside an atomic
// built-in). The count is already sitting in the bounds-check sizes
// buffer this binding's own entry allocates (Result.SizeMetalIndex's own
// doc comment) — the SAME count `indexExpr`'s own clamp reads, correctly
// the bound SUB-RANGE's element count (go-webengine/engine#391), never
// the whole buffer's — so this is a direct field read, not a new
// mechanism.
func (tr *translator) arrayLengthExpr(c *wgsl.Call) (string, error) {
	if len(c.Args) != 1 {
		return "", unsupported("arrayLength: exactly one argument is required")
	}
	ptrExpr, ok := c.Args[0].(*wgsl.Unary)
	if !ok || ptrExpr.Op != "&" {
		return "", unsupported("arrayLength: the argument must be &resource (a pointer to a runtime-sized storage array)")
	}
	vo, tracked := tr.resourceOf(ptrExpr.X)
	if !tracked {
		return "", unsupported("arrayLength: the argument must be a tracked runtime-sized storage array resource")
	}
	b := tr.bindings[tr.resources[vo]]
	if b.Kind != BindingStorageArray {
		return "", unsupported("arrayLength: resource %q is not a runtime-sized storage array", vo.Name)
	}
	return fmt.Sprintf("_sizes.s%d", b.SizesIndex), nil
}

func (tr *translator) callExpr(c *wgsl.Call) (string, error) {
	if mslName, ok := atomicBuiltinFn[c.Fn.Name]; ok {
		return tr.atomicCallExpr(mslName, c)
	}
	if c.Fn.Name == "arrayLength" {
		return tr.arrayLengthExpr(c)
	}
	if c.Fn.Name == "bitcast" {
		return tr.bitcastExpr(c)
	}
	args := make([]string, len(c.Args))
	for i, a := range c.Args {
		s, err := tr.expr(a)
		if err != nil {
			return "", err
		}
		args[i] = s
	}
	joined := strings.Join(args, ", ")

	if _, isType := tr.info.TypeSpecs[c.Fn]; isType {
		resultType := tr.exprType(c)
		typeMSL, err := mslType(resultType)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s(%s)", typeMSL, joined), nil
	}

	if name, ok := mslBuiltinFn[c.Fn.Name]; ok {
		return fmt.Sprintf("metal::%s(%s)", name, joined), nil
	}

	if fo, ok := tr.info.Objects[c.Fn].(*wgsl.FuncObj); ok && tr.userFuncs[fo] {
		callArgs := append([]string{}, args...)
		if extra := tr.helperExtra[fo]; extra != nil {
			for _, vo := range extra.resources {
				callArgs = append(callArgs, identMSL(vo.Name))
			}
			if extra.needsSizes {
				callArgs = append(callArgs, "_sizes")
			}
		}
		return fmt.Sprintf("%s(%s)", identMSL(fo.Name), strings.Join(callArgs, ", ")), nil
	}

	return "", unsupported("call to %q (not a supported built-in, and this helper function cannot be translated)", c.Fn.Name)
}
