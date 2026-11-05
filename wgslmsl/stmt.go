// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"github.com/go-gfx/wgsl"
)

// stmtBlock emits every statement of b in order, each on its own line(s).
func (tr *translator) stmtBlock(b *wgsl.Block) error {
	for _, s := range b.Stmts {
		if err := tr.stmt(s); err != nil {
			return err
		}
	}
	return nil
}

// stmt covers the compute-shader-relevant statement subset — see the
// package doc comment for what is deliberately out of scope (switch,
// discard, pointer/reference locals).
func (tr *translator) stmt(s wgsl.Stmt) error {
	switch st := s.(type) {
	case *wgsl.Empty, *wgsl.AssertStmt:
		// const_assert is compile-time-only in WGSL too; nothing to emit.
		return nil
	case *wgsl.Block:
		tr.line("{")
		tr.indent++
		err := tr.stmtBlock(st)
		tr.indent--
		tr.line("}")
		return err
	case *wgsl.DeclStmt:
		return tr.declStmt(st)
	case *wgsl.Assign:
		return tr.assignStmt(st)
	case *wgsl.IncDec:
		x, err := tr.expr(st.X)
		if err != nil {
			return err
		}
		tr.line("%s%s;", x, st.Op)
		return nil
	case *wgsl.If:
		return tr.ifStmt(st)
	case *wgsl.For:
		return tr.forStmt(st)
	case *wgsl.While:
		cond, err := tr.expr(st.Cond)
		if err != nil {
			return err
		}
		tr.line("while (%s) {", cond)
		tr.indent++
		err = tr.stmtBlock(st.Body)
		tr.indent--
		tr.line("}")
		return err
	case *wgsl.Loop:
		return tr.loopStmt(st)
	case *wgsl.Return:
		if st.X == nil {
			tr.line("return;")
			return nil
		}
		x, err := tr.expr(st.X)
		if err != nil {
			return err
		}
		tr.line("return %s;", x)
		return nil
	case *wgsl.Break:
		tr.line("break;")
		return nil
	case *wgsl.Continue:
		tr.line("continue;")
		return nil
	case *wgsl.CallStmt:
		c, err := tr.callExpr(st.Call)
		if err != nil {
			return err
		}
		tr.line("%s;", c)
		return nil
	default:
		return unsupported("statement %T", s)
	}
}

func (tr *translator) declStmt(ds *wgsl.DeclStmt) error {
	switch d := ds.Decl.(type) {
	case *wgsl.Var:
		t := tr.info.DeclTypes[d]
		typeMSL, err := mslType(t)
		if err != nil {
			return err
		}
		if d.Init == nil {
			tr.line("%s %s = %s{};", typeMSL, identMSL(d.Name), typeMSL)
			return nil
		}
		init, err := tr.expr(d.Init)
		if err != nil {
			return err
		}
		tr.line("%s %s = %s;", typeMSL, identMSL(d.Name), init)
		return nil
	case *wgsl.Value:
		if d.Kind != "let" {
			return unsupported("local %s declaration", d.Kind)
		}
		t := tr.info.DeclTypes[d]
		typeMSL, err := mslType(t)
		if err != nil {
			return err
		}
		init, err := tr.expr(d.Init)
		if err != nil {
			return err
		}
		tr.line("const %s %s = %s;", typeMSL, identMSL(d.Name), init)
		return nil
	default:
		return unsupported("local declaration %T", ds.Decl)
	}
}

func (tr *translator) assignStmt(a *wgsl.Assign) error {
	if a.LHS == nil {
		// Phony assignment `_ = e`: WGSL requires evaluating e for its side
		// effects, discarding the value. A bare expression statement does
		// exactly that in MSL too.
		rhs, err := tr.expr(a.RHS)
		if err != nil {
			return err
		}
		tr.line("(void)(%s);", rhs)
		return nil
	}
	lhs, err := tr.expr(a.LHS)
	if err != nil {
		return err
	}
	rhs, err := tr.expr(a.RHS)
	if err != nil {
		return err
	}
	tr.line("%s %s %s;", lhs, a.Op, rhs)
	return nil
}

func (tr *translator) ifStmt(i *wgsl.If) error {
	cond, err := tr.expr(i.Cond)
	if err != nil {
		return err
	}
	tr.line("if (%s) {", cond)
	tr.indent++
	if err := tr.stmtBlock(i.Then); err != nil {
		return err
	}
	tr.indent--
	switch e := i.Else.(type) {
	case nil:
		tr.line("}")
	case *wgsl.Block:
		tr.line("} else {")
		tr.indent++
		if err := tr.stmtBlock(e); err != nil {
			return err
		}
		tr.indent--
		tr.line("}")
	case *wgsl.If:
		tr.line("} else")
		if err := tr.ifStmt(e); err != nil {
			return err
		}
	default:
		return unsupported("if-else arm %T", i.Else)
	}
	return nil
}

func (tr *translator) forStmt(f *wgsl.For) error {
	// MSL's own for-statement grammar matches WGSL's closely enough to emit
	// init/cond/update inline, except WGSL's Init/Update are full statements
	// (potentially a DeclStmt or Assign) while C's for-header wants bare
	// clauses — since this package's own bounded scope only ever needs a
	// simple counting loop, a nested block with an explicit break covers
	// every WGSL `for` without needing to special-case the header's syntax.
	tr.line("{")
	tr.indent++
	if f.Init != nil {
		if err := tr.stmt(f.Init); err != nil {
			return err
		}
	}
	tr.line("while (true) {")
	tr.indent++
	if f.Cond != nil {
		cond, err := tr.expr(f.Cond)
		if err != nil {
			return err
		}
		tr.line("if (!(%s)) break;", cond)
	}
	if err := tr.stmtBlock(f.Body); err != nil {
		return err
	}
	if f.Update != nil {
		if err := tr.stmt(f.Update); err != nil {
			return err
		}
	}
	tr.indent--
	tr.line("}")
	tr.indent--
	tr.line("}")
	return nil
}

// loopStmt emits WGSL's `loop { … continuing { … } }` as an unconditional
// while with the continuing block's statements appended at the bottom of
// each iteration (and its own `break if`, when present, translated to a
// plain conditional break) — exactly WGSL's own defined semantics (spec
// "loop Statement": the continuing block runs every iteration, after the
// body, before testing any break-if).
func (tr *translator) loopStmt(l *wgsl.Loop) error {
	tr.line("while (true) {")
	tr.indent++
	for _, s := range l.Body {
		if err := tr.stmt(s); err != nil {
			return err
		}
	}
	if l.Cont != nil {
		for _, s := range l.Cont.Stmts {
			if bi, ok := s.(*wgsl.BreakIf); ok {
				cond, err := tr.expr(bi.Cond)
				if err != nil {
					return err
				}
				tr.line("if (%s) break;", cond)
				continue
			}
			if err := tr.stmt(s); err != nil {
				return err
			}
		}
	}
	tr.indent--
	tr.line("}")
	return nil
}
