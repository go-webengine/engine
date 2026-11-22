// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-gfx/wgsl"
)

// TranslateRender translates src's @vertex and @fragment entry points to
// Metal Shading Language — the render-pipeline counterpart to Translate's
// own @compute path, added for engine#415 (WebGPU roadmap M7: textures and
// render pipelines), under the SAME bounded-scope discipline: an
// unsupported WGSL shape is a named rejection (translateError), never a
// silently wrong translation.
//
// Honest limits, confirmed from the go-gfx/wgsl v0.4.0 source
// (builtins_custom.go, check.go, types.go) rather than assumed:
//   - Vertex inputs: a @builtin(vertex_index)/@builtin(instance_index)
//     scalar stays its own bare parameter; a @location(N) scalar/vector
//     attribute (declared as an individual parameter or as a struct
//     parameter's field, either way) is instead GROUPED into one
//     synthesized [[stage_in]] struct (stageEntryParams) — confirmed the
//     opposite, individually-parameterized shape this package's @compute
//     path uses is invalid MSL for a vertex entry's own [[attribute(N)]]
//     inputs specifically ("invalid type ... for input declaration in a
//     vertex function" from the real compiler).
//   - Vertex/fragment outputs: either a user-declared struct mixing
//     @builtin(position) and @location(N) fields, or a bare (non-struct)
//     single value — but ONLY in the one shape Metal gives an IMPLICIT
//     attribute to, since a non-struct return type has no attribute syntax
//     at all in MSL (measured directly against the real compiler: every
//     spelling tried — after the parameter list, between the return type
//     and the function name, before the return type — was rejected with
//     "attribute cannot be applied to types"; only a struct FIELD's own
//     attribute compiles). That leaves exactly `-> @builtin(position)
//     vec4<f32>` for a vertex entry and `-> @location(0) T` for a fragment
//     entry as viable bare outputs (Metal's own implicit default for each
//     stage); any other bare output (a vertex entry without position, a
//     fragment @location(N) for N != 0) is rejected — a struct return is
//     always available for those instead.
//   - Fragment inputs mirror a vertex entry's own output struct fields in
//     shape (one @location(N) or @builtin(position) per field), but a
//     @builtin(position) input stays its own bare parameter while every
//     @location(N) varying is GROUPED into its own synthesized
//     [[stage_in]] struct — a different struct TYPE from the vertex
//     entry's output struct, never shared between the two MSL functions.
//     Confirmed the opposite, individually-parameterized shape *compiles*
//     but silently never receives the rasterizer-interpolated value (an
//     actual render + pixel-readback test, not just a compile check,
//     caught this: every pixel read back the clear colour, as if the
//     quad never rendered).
//   - A fragment entry point may read ONE resource kind beyond uniform/
//     storage: a non-arrayed `texture_2d<u32>` resource, through
//     textureLoad only (no sampler, no textureSample*) — go-macos/metal's
//     RenderEncoder has no vertex-stage texture binding at all, so the
//     same resource in a @vertex entry is rejected.
//   - No runtime-sized storage array in either stage (this package's own
//     bounds-check machinery, Result.SizeMetalIndex, is a @compute-only
//     concept; a render pipeline's own resources are small, fixed-size
//     uniforms/textures in every confirmed real shape so far).
//   - Exactly the same struct restriction as Translate's own @compute
//     path otherwise: the ONE struct type ever recognized is an entry
//     point's own return-type struct (see mslTypeTR, memberExpr,
//     callExpr's struct-constructor case) — no other user struct, and a
//     struct-typed INPUT parameter (a fragment entry taking the vertex
//     stage's own output struct) is never emitted as a real MSL struct at
//     all: each field becomes its own flattened, individually decorated
//     scalar/vector parameter (stageEntryParams, tr.inputFlatten) — doing
//     otherwise would need one MSL struct type shared, by C type
//     identity, between two independently generated MSL functions, which
//     this package deliberately avoids.
type RenderOptions struct {
	// VertexEntryPoint and FragmentEntryPoint select which @vertex/
	// @fragment entry point to translate when the module declares more
	// than one of that stage. Empty selects the module's only entry point
	// of that stage (an error if there is more than one or none).
	VertexEntryPoint, FragmentEntryPoint string
}

// VertexInputVar is one @location vertex-stage input this translation
// resolved — what the host (a later js/webgpu.go PR) needs to build a
// matching MTLVertexDescriptor attribute: NComponents and ScalarKind
// together pick the MTLVertexFormat (e.g. 4 components of KF32 ->
// float4), Location the WGSL/MSL [[attribute(N)]] index.
type VertexInputVar struct {
	Location    int
	NComponents int
	ScalarKind  wgsl.ScalarKind
}

// RenderResult is a successful TranslateRender.
type RenderResult struct {
	VertexMSL, FragmentMSL                       string
	VertexEntryPointName, FragmentEntryPointName string
	// VertexInputs is every @location vertex-stage input, in ep.Inputs
	// order, for the host's own MTLVertexDescriptor.
	VertexInputs []VertexInputVar
	// VertexBindings and FragmentBindings are each stage's OWN resolved
	// resource bindings (Binding's own doc comment) — two independent
	// lists: a vertex and a fragment entry point are two separate MSL
	// functions, each with its own [[buffer(N)]]/[[texture(N)]] index
	// space, even when both read what is, in WGSL source, "the same"
	// module-scope resource.
	VertexBindings, FragmentBindings []Binding
}

// TranslateRender parses, validates (go-gfx/wgsl) and translates src's
// @vertex and @fragment entry points, selected by opts, to two independent
// Metal Shading Language functions.
func TranslateRender(src string, opts RenderOptions) (*RenderResult, error) {
	m, err := wgsl.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("wgslmsl: parse: %w", err)
	}
	info, errs := wgsl.Check(m)
	if err := errs.Err(); err != nil {
		return nil, fmt.Errorf("wgslmsl: check: %w", err)
	}

	vep, err := findEntryPoint(info, wgsl.StageVertex, opts.VertexEntryPoint, "VertexEntryPoint")
	if err != nil {
		return nil, err
	}
	fep, err := findEntryPoint(info, wgsl.StageFragment, opts.FragmentEntryPoint, "FragmentEntryPoint")
	if err != nil {
		return nil, err
	}

	vtr := &translator{info: info}
	vMSL, vInputs, err := vtr.translateStageEntry(vep)
	if err != nil {
		return nil, fmt.Errorf("wgslmsl: vertex entry %q: %w", vep.Name, err)
	}

	ftr := &translator{info: info}
	fMSL, _, err := ftr.translateStageEntry(fep)
	if err != nil {
		return nil, fmt.Errorf("wgslmsl: fragment entry %q: %w", fep.Name, err)
	}

	return &RenderResult{
		VertexMSL:              vMSL,
		FragmentMSL:            fMSL,
		VertexEntryPointName:   identMSL(vep.Name),
		FragmentEntryPointName: identMSL(fep.Name),
		VertexInputs:           vInputs,
		VertexBindings:         vtr.bindings,
		FragmentBindings:       ftr.bindings,
	}, nil
}

// findEntryPoint resolves name (or the module's only entry point of stage
// when name is empty) against info.EntryPoints — the RenderOptions
// counterpart to wgslmsl.go's own selectEntryPoint, parameterized by stage
// since TranslateRender needs this done twice (vertex and fragment).
// optField names the RenderOptions field to mention when more than one
// entry point of stage exists, so the error tells the caller exactly what
// to set.
func findEntryPoint(info *wgsl.Info, stage wgsl.Stage, name, optField string) (*wgsl.EntryPoint, error) {
	if name != "" {
		for _, ep := range info.EntryPoints {
			if ep.Name == name {
				if ep.Stage != stage {
					return nil, fmt.Errorf("wgslmsl: entry point %q is a @%s shader, not @%s", name, ep.Stage, stage)
				}
				return ep, nil
			}
		}
		return nil, fmt.Errorf("wgslmsl: no entry point named %q", name)
	}
	var matched []*wgsl.EntryPoint
	for _, ep := range info.EntryPoints {
		if ep.Stage == stage {
			matched = append(matched, ep)
		}
	}
	switch len(matched) {
	case 0:
		return nil, fmt.Errorf("wgslmsl: module has no @%s entry point", stage)
	case 1:
		return matched[0], nil
	default:
		names := make([]string, len(matched))
		for i, ep := range matched {
			names[i] = ep.Name
		}
		sort.Strings(names)
		return nil, fmt.Errorf("wgslmsl: module has %d @%s entry points (%s); pass RenderOptions.%s to pick one", len(matched), stage, strings.Join(names, ", "), optField)
	}
}

// vecShape reports t's own component count and scalar kind — for a
// vertex-stage @location input (VertexInputVar), which the host needs to
// pick a matching MTLVertexFormat. t must be a scalar or vector, exactly
// mslType's own "scalars and vectors" scope.
func vecShape(t wgsl.Type) (n int, kind wgsl.ScalarKind, err error) {
	switch tt := derefType(t).(type) {
	case *wgsl.ScalarType:
		return 1, tt.Kind, nil
	case *wgsl.VecType:
		return tt.N, tt.Elem.Kind, nil
	default:
		return 0, 0, unsupported("vertex input type %s (only a scalar or vector)", t.String())
	}
}

// outputIOAttr is io's own MSL output attribute: [[position]] for the
// position builtin, [[color(N)]] for a fragment's @location(N) (Metal's
// own fragment-output attribute, distinct from a vertex/fragment-input
// varying's [[user(locnN)]] — see ioAttr's own use in stageEntryParams for
// the input side of that same @location), or [[user(locnN)]] (plus
// ", flat" when WGSL's own @interpolate(flat) applies — integer-typed
// varyings always carry it, by WGSL's own spec rule, so this never needs
// to special-case the type) for a vertex's @location(N) output.
func outputIOAttr(io wgsl.IOVar, stage wgsl.Stage) (string, error) {
	if io.Builtin == "position" {
		return "position", nil
	}
	if io.Builtin != "" {
		return "", unsupported("entry point output builtin %q", io.Builtin)
	}
	// io.Location == nil is unreachable here: go-gfx/wgsl's entryIO (the
	// "io-missing" check) already rejects, with a checker ERROR, any entry
	// point IO that has neither a @builtin nor a @location — and
	// TranslateRender returns before any translation when wgsl.Check reports
	// an error. By the time an IOVar with io.Builtin == "" reaches here,
	// io.Location is guaranteed non-nil. Hand-verified by mutation
	// (engine#447): deleting this check left it unreachable, never SURVIVED.
	loc := int(*io.Location)
	if stage == wgsl.StageFragment {
		return fmt.Sprintf("color(%d)", loc), nil
	}
	attr := fmt.Sprintf("user(locn%d)", loc)
	if io.Interpolate[0] == "flat" {
		attr += ", flat"
	}
	return attr, nil
}

// emitIOStruct emits the MSL struct backing an entry point's own
// struct-typed return value (translateStageEntry's own tr.outputStructType
// case): one field per ep.Outputs entry, named after its own struct member
// (io.Member) — a bare, non-struct-member output never reaches here at
// all (translateStageEntry's other branch handles that shape directly, no
// synthesized struct involved).
func (tr *translator) emitIOStruct(name string, vars []wgsl.IOVar, stage wgsl.Stage) error {
	tr.line("struct %s {", name)
	tr.indent++
	for _, io := range vars {
		attr, err := outputIOAttr(io, stage)
		if err != nil {
			return err
		}
		fieldMSL, err := mslType(io.Type)
		if err != nil {
			return err
		}
		tr.line("%s %s [[%s]];", fieldMSL, identMSL(io.Member), attr)
	}
	tr.indent--
	tr.line("};")
	tr.buf.WriteByte('\n')
	return nil
}

// stageEntryParams builds a @vertex/@fragment entry's own MSL parameter
// list: its resources (resourceEntryParams, identical to the @compute
// path), then one parameter per ep.Inputs entry. A builtin
// (vertex_id/instance_id/position) is always its own bare, individually
// decorated parameter — confirmed valid directly against the real Metal
// compiler, no wrapping struct needed.
//
// A @location input, in EITHER stage, is never left as an individual bare
// parameter — confirmed directly against the real compiler, the hard way:
// a bare [[attribute(N)]] vertex parameter is rejected outright ("invalid
// type ... for input declaration in a vertex function"), and a bare
// [[user(locnN)]] fragment parameter *compiles* but silently never
// receives the rasterizer-interpolated value at all (every pixel read back
// the clear colour, as if the draw never happened — exactly the "compiles,
// renders wrong" failure this package's whole bounded-scope discipline
// exists to catch BEFORE it reaches a caller, not just a syntax check).
// Both stages' own @location inputs are instead grouped into ONE
// synthesized MSL struct marked [[stage_in]] (emitted here, right before
// returning) — never the SAME struct between a vertex and fragment entry
// (render.go's own doc comment on why), just the same grouping mechanism
// independently in each. The attribute text differs by stage:
// [[attribute(N)]] for a vertex input, [[user(locnN)]] (with no ", flat"
// — confirmed that qualifier is rejected outright on anything but a real
// struct FIELD, which this is) for a fragment one.
//
// A struct-typed WGSL input's own field (in.Member != "") is never given
// the struct's WGSL name: it gets its own flattened, uniquely-prefixed MSL
// identifier instead (translator.go's own inputFlatten doc comment says
// why), recorded in tr.inputFlatten so memberExpr can translate the body's
// `in.field` access back to it. A @location input declared as a plain
// top-level parameter instead (no enclosing struct to access through
// `in.field`) is recorded in tr.paramRename instead, keyed directly by its
// own ParamObj, since the WGSL body refers to it by a bare identifier.
func (tr *translator) stageEntryParams(ep *wgsl.EntryPoint) ([]string, []VertexInputVar, error) {
	params, err := tr.resourceEntryParams(ep.Resources)
	if err != nil {
		return nil, nil, err
	}

	tr.inputFlatten = map[*wgsl.ParamObj]map[string]string{}
	tr.paramRename = map[*wgsl.ParamObj]string{}
	var vInputs []VertexInputVar
	var stageInFields []string
	const stageInVar = "_in"
	stageInType := "_" + identMSL(ep.Name) + "In"

	for _, in := range ep.Inputs {
		paramMSL, err := tr.mslTypeTR(in.Type)
		if err != nil {
			return nil, nil, err
		}

		switch {
		case in.Builtin == "vertex_index":
			params = append(params, fmt.Sprintf("%s %s [[vertex_id]]", paramMSL, identMSL(in.Name)))
		case in.Builtin == "instance_index":
			params = append(params, fmt.Sprintf("%s %s [[instance_id]]", paramMSL, identMSL(in.Name)))
		case in.Builtin == "position":
			params = append(params, fmt.Sprintf("%s %s [[position]]", paramMSL, identMSL(in.Name)))
		case in.Builtin != "":
			return nil, nil, unsupported("entry point input builtin %q", in.Builtin)
		case in.Location != nil:
			loc := int(*in.Location)
			var attr string
			if ep.Stage == wgsl.StageVertex {
				attr = fmt.Sprintf("attribute(%d)", loc)
				// vecShape's own error case (anything but a *wgsl.ScalarType or
				// *wgsl.VecType) is unreachable here: go-gfx/wgsl's entryIO
				// already requires scalarOf(in.Type) != nil for a @location
				// input (the "location-type" checker error otherwise), and
				// scalarOf itself only ever returns non-nil for exactly those
				// two shapes — the same two vecShape switches on. Hand-verified
				// by mutation (engine#447): deleting this check left it
				// unreachable, never SURVIVED.
				n, kind, _ := vecShape(in.Type)
				vInputs = append(vInputs, VertexInputVar{Location: loc, NComponents: n, ScalarKind: kind})
			} else {
				attr = fmt.Sprintf("user(locn%d)", loc)
				if in.Interpolate[0] == "flat" {
					attr += ", flat"
				}
			}

			fieldName := identMSL(in.Name)
			po := ep.Func.Params[in.Param]
			if in.Member != "" {
				fieldName = fmt.Sprintf("_%s_%s", identMSL(po.Name), identMSL(in.Member))
			}
			stageInFields = append(stageInFields, fmt.Sprintf("%s %s [[%s]];", paramMSL, fieldName, attr))
			accessor := stageInVar + "." + fieldName
			if in.Member != "" {
				if tr.inputFlatten[po] == nil {
					tr.inputFlatten[po] = map[string]string{}
				}
				tr.inputFlatten[po][in.Member] = accessor
			} else {
				tr.paramRename[po] = accessor
			}
		default:
			return nil, nil, unsupported("entry point input %q has neither a builtin nor a @location", in.Name)
		}
	}

	if len(stageInFields) > 0 {
		tr.line("struct %s {", stageInType)
		tr.indent++
		for _, f := range stageInFields {
			tr.line("%s", f)
		}
		tr.indent--
		tr.line("};")
		tr.buf.WriteByte('\n')
		params = append(params, fmt.Sprintf("%s %s [[stage_in]]", stageInType, stageInVar))
	}

	return params, vInputs, nil
}

// translateStageEntry translates one @vertex or @fragment entry point to
// a complete MSL source file — the render-pipeline counterpart to
// translator.go's own translateEntryPoint, sharing resolveResources,
// emitHelperFuncs and resourceEntryParams with the @compute path, and
// stmtBlock for the body with every other statement/expression kind
// (stmt.go, expr.go), under the exact same bounded-scope rejections.
func (tr *translator) translateStageEntry(ep *wgsl.EntryPoint) (string, []VertexInputVar, error) {
	if err := tr.resolveResources(ep); err != nil {
		return "", nil, err
	}
	if tr.sizesCount > 0 {
		return "", nil, unsupported("entry point %q: a runtime-sized storage array resource is not supported in a vertex or fragment stage", ep.Name)
	}
	if ep.Stage == wgsl.StageVertex {
		for _, b := range tr.bindings {
			if b.Kind == BindingTexture {
				return "", nil, unsupported("entry point %q: a vertex stage cannot read a texture (go-macos/metal's RenderEncoder has no vertex-stage texture binding)", ep.Name)
			}
		}
	}

	tr.line("#include <metal_stdlib>")
	tr.line("#include <simd/simd.h>")
	tr.line("using namespace metal;")
	tr.buf.WriteByte('\n')

	if err := tr.emitHelperFuncs(ep); err != nil {
		return "", nil, err
	}

	var retMSL string
	switch {
	case ep.Func.Ret == wgsl.Void:
		retMSL = "void"
	default:
		if st, ok := ep.Func.Ret.(*wgsl.StructType); ok {
			tr.outputStructType = st
			tr.outputStructName = "_" + identMSL(ep.Name) + "Out"
			if err := tr.emitIOStruct(tr.outputStructName, ep.Outputs, ep.Stage); err != nil {
				return "", nil, err
			}
			retMSL = tr.outputStructName
			break
		}
		m, err := mslType(ep.Func.Ret)
		if err != nil {
			return "", nil, err
		}
		retMSL = m
		// len(ep.Outputs) != 1 is unreachable here: this branch is reached
		// only when ep.Func.Ret is NOT a *wgsl.StructType (the struct branch,
		// above, already returned). go-gfx/wgsl's entryIO calls addIO exactly
		// once for a non-struct return value (handle's own struct/non-struct
		// dispatch mirrors this branch's — a Predeclared struct, the one case
		// addIO itself would still treat as non-struct, can never be
		// ep.Func.Ret because its name "cannot be spelled in source"), and
		// addIO unconditionally appends to ep.Outputs — so a non-struct,
		// non-void return always leaves exactly one entry there. Hand-verified
		// by mutation (engine#447): deleting this check left it unreachable,
		// never SURVIVED.
		// A non-struct MSL return type has NO attribute syntax at all —
		// measured directly against the real Metal compiler (every
		// spelling tried was rejected with "attribute cannot be applied to
		// types"; see this function's own doc comment and
		// RenderOptions' doc comment). Metal gives exactly one implicit
		// attribute per stage to a bare return: [[position]] for a vertex
		// entry, [[color(0)]] for a fragment entry. Anything else needs a
		// struct return instead (emitIOStruct's own branch, above).
		out := ep.Outputs[0]
		switch {
		case ep.Stage == wgsl.StageVertex && out.Builtin == "position":
			// Implicit; no attribute to emit.
		case ep.Stage == wgsl.StageFragment && out.Builtin == "" && out.Location != nil && int(*out.Location) == 0:
			// Implicit; no attribute to emit.
		default:
			return "", nil, unsupported("entry point %q: a bare (non-struct) output must be @builtin(position) in a vertex entry or @location(0) in a fragment entry — Metal has no attribute syntax for any other non-struct return, only these two implicit defaults; use a struct return instead", ep.Name)
		}
	}

	params, vInputs, err := tr.stageEntryParams(ep)
	if err != nil {
		return "", nil, err
	}

	qualifier := "vertex"
	if ep.Stage == wgsl.StageFragment {
		qualifier = "fragment"
	}
	entryName := identMSL(ep.Name)
	tr.line("%s %s %s(", qualifier, retMSL, entryName)
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
		return "", nil, err
	}
	tr.indent--
	tr.line("}")

	return tr.buf.String(), vInputs, nil
}
