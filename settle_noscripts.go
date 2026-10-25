// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_nojs

package engine

import (
	"context"
	"time"

	"github.com/go-webengine/engine/dom"
	"github.com/go-webengine/engine/paint"
)

// Built with the webengine_nojs tag, the engine has no JavaScript: no goja,
// no esbuild, and none of the interactive API (Engine.Open, LiveDocument,
// form submission) that drives a script session. Page scripts are not run,
// whatever DisableJS says, and the render is the one DisableJS gives;
// Engine.Adjust still runs. A host that renders documents it made itself (a
// slide deck exported in a browser's WebAssembly worker) saves the
// download of a JavaScript engine it never needs.

// jsSession stands in for the script session the engine does not have.
type jsSession struct{}

// Close is a no-op.
func (*jsSession) Close() {}

// markJSEnabled leaves the no-JS fallback in place: no script runs.
func markJSEnabled(*dom.Node) {}

// settle runs no script: the render stays the initial layout.
func (e *Engine) settle(context.Context, *Document, int, int, *paint.Fonts, *renderPass, time.Duration, func(string, *renderPass)) *jsSession {
	return nil
}
