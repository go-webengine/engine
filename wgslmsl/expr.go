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
		clamped := fmt.Sprintf("metal::min(uint(%s), metal::max(_sizes.s%d, 1u) - 1u)", i, b.MetalIndex)
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
func (tr *translator) callExpr(c *wgsl.Call) (string, error) {
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

	return "", unsupported("call to %q (not a supported built-in, and user-defined functions are out of scope)", c.Fn.Name)
}
