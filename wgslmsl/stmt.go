// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package wgslmsl

import (
	"fmt"

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
		saved := tr.loopFlag
		tr.loopFlag = "" // WGSL's while has no continuing clause; MSL's own break/continue are already correct
		err = tr.stmtBlock(st.Body)
		tr.loopFlag = saved
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
		// Inside forStmt's/loopStmt's own do{}while(false) wrapper, a bare
		// MSL break would only exit THAT wrapper, not the real loop — set
		// the flag first so the code right after the wrapper also breaks
		// the real (outer) loop. See forStmt's own doc comment.
		if tr.loopFlag != "" {
			tr.line("%s = true;", tr.loopFlag)
		}
		tr.line("break;")
		return nil
	case *wgsl.Continue:
		if tr.loopFlag != "" {
			// A bare MSL continue would skip the for-loop's own update (or
			// the loop statement's own continuing block) outright — Metal
			// has no goto/labels to jump there instead, so forStmt/loopStmt
			// wrap the body in do{}while(false) and this break just exits
			// THAT, falling through to update/continuing normally. See
			// forStmt's own doc comment.
			tr.line("break;")
		} else {
			tr.line("continue;")
		}
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

// forStmt emits WGSL's for (init; cond; update) body as a while loop, not
// MSL's own native for(;;) — WGSL's Init/Update are full statements
// (potentially a DeclStmt or Assign), while C's for-header wants bare
// clauses, and this package's own bounded scope only ever needs a simple
// counting loop.
//
// A bare `continue` inside body must still run update before the next
// condition check (WGSL spec §9.4.6: a for-loop's update IS its own
// continuing block, and "a continue statement... executes the continuing
// statement" — MSL's native for(;;update) already has this built in,
// which plain `while(true){ if(!cond) break; body; update; }` does not: a
// bare MSL `continue` there jumps straight back to `while(true)`, skipping
// update entirely — an infinite loop whenever body's own continue fires
// before the loop variable would otherwise advance (confirmed directly:
// go-wasmcontainers' own padded conv/pool kernels skip out-of-range input
// positions this way, and hit their host's own loop-budget guard instead
// of advancing).
//
// Fixed WITHOUT goto: Metal has no labeled statements at all (confirmed
// directly — "error: labeled statements are not supported in Metal" from
// the real compiler, on an earlier version of this fix that tried one).
// body is wrapped in `do { ... } while (false)`: a continue inside it
// (rewritten to a bare `break`, stmt.go's own Continue case) exits only
// that inner wrapper, falling through to update as if nothing happened —
// exactly a for-loop's own continue semantics. A real break (rewritten to
// set loopFlag's own flag variable, then break) ALSO only exits the inner
// wrapper on its own, so the line right after it tests the flag and
// breaks the real, outer loop too — without it, every `break` inside body
// would wrongly become a continue instead.
func (tr *translator) forStmt(f *wgsl.For) error {
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
	flag := tr.newLoopFlag()
	tr.line("bool %s = false;", flag)
	tr.line("do {")
	tr.indent++
	saved := tr.loopFlag
	tr.loopFlag = flag
	err := tr.stmtBlock(f.Body)
	tr.loopFlag = saved
	tr.indent--
	tr.line("} while (false);")
	if err != nil {
		return err
	}
	tr.line("if (%s) break;", flag)
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

// newLoopFlag returns a fresh, unique break-flag variable name for one
// for/loop construct's own break/continue rewriting (loopFlag's own doc
// comment) — unique across the WHOLE translation, not just this function,
// since a sibling loop elsewhere in the same kernel must never collide
// with one still in scope (nested) when this one was allocated.
func (tr *translator) newLoopFlag() string {
	tr.flagCounter++
	return fmt.Sprintf("_brk%d", tr.flagCounter)
}

// loopStmt emits WGSL's `loop { … continuing { … } }` as an unconditional
// while with the continuing block's statements appended at the bottom of
// each iteration (and its own `break if`, when present, translated to a
// plain conditional break) — exactly WGSL's own defined semantics (spec
// "loop Statement": the continuing block runs every iteration, after the
// body, before testing any break-if).
//
// A bare `continue` inside Body must still run the continuing block first
// — the identical reason, and the identical do{}while(false)+flag fix
// (Metal has no goto/labels at all), as forStmt's own doc comment: a
// plain MSL continue would jump straight back to `while(true)`, skipping
// Cont (and its own break-if) entirely.
func (tr *translator) loopStmt(l *wgsl.Loop) error {
	tr.line("while (true) {")
	tr.indent++
	flag := tr.newLoopFlag()
	tr.line("bool %s = false;", flag)
	tr.line("do {")
	tr.indent++
	saved := tr.loopFlag
	tr.loopFlag = flag
	var bodyErr error
	for _, s := range l.Body {
		if err := tr.stmt(s); err != nil {
			bodyErr = err
			break
		}
	}
	tr.loopFlag = saved
	tr.indent--
	tr.line("} while (false);")
	if bodyErr != nil {
		return bodyErr
	}
	tr.line("if (%s) break;", flag)
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
