// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package js

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/go-webengine/esbuildsandbox"
)

// Classic-script fallback: goja has no ES-module support (modules.go's own
// doc comment), but a CLASSIC (non-module) script may legally contain a bare
// dynamic import() EXPRESSION too — that is not restricted to module scripts
// by the spec, unlike the static import/export DECLARATIONS
// collectModuleScripts' own type="module" gate exists for. webpack/
// Turbopack's own runtime/chunk-loading code, emitted as a plain classic
// <script src> (confirmed directly: github.com's own Support app ships this
// way — engine#412/#413), uses exactly this: a bare import() as its chunk
// loader, which goja's parser rejects outright ("Unexpected reserved word").
//
// transpileModernJS re-emits such a script as plain ES2017 via esbuild — the
// same pure-Go, CGO-free tool modules.go already uses for module graphs —
// fetching any nested dynamic import() target through the same http.Client a
// real browser's own chunk loader would use. It is tried ONLY when goja's own
// Compile already failed: the overwhelming majority of scripts use nothing
// goja cannot parse and never reach this at all, so there is no behaviour or
// performance change for them. Deliberately NOT the same code path as
// bundleModuleScripts (a multi-entry module GRAPH declared by the page's own
// <script type="module"> tags) — this is a single already-in-hand script
// being re-emitted, not a graph being discovered — even though the esbuild
// plugin wiring looks similar; revisit sharing it if a third call site needs
// the same shape.
//
// A dynamic import() with a STRING LITERAL specifier (import("./x.js")) is
// what esbuild's Bundle:true can resolve and inline at transpile time, here,
// directly — but React Router's own route loader on github.com's Support
// app (the real engine#412/#413 trigger) uses a VARIABLE specifier,
// `await import(j.module)`: a value only known once the script is already
// RUNNING. esbuild cannot statically resolve that, and (confirmed directly,
// against both IIFE and CommonJS output) leaves it as a literal import(...)
// call in its output either way — which goja still can't parse, so the
// retry failed for exactly this shape even after this fallback shipped.
// Marking "dynamic-import" as an unsupported target feature (Supported
// below) makes esbuild lower EVERY import() call — literal or variable
// specifier alike — into a plain require() call instead (confirmed
// directly: this is how esbuild already handles a target that lacks native
// dynamic import at all), which is ordinary ES2017 syntax goja parses fine.
// require.go's requireModule then backs that require() call for real:
// fetch, transpile, execute, return its exports — the one thing esbuild
// itself cannot do for a specifier it cannot see ahead of time.
const (
	// maxTranspileFetches bounds how many chunks the WHOLE fallback may pull
	// in across one render — not per script (see requireFetched's own doc
	// comment in js.go): smaller than modules.go's own maxModuleFetches
	// (1200), since this is a fallback path, not a whole page's own
	// declared module graph.
	maxTranspileFetches = 200
	// maxTranspileSourceBytes bounds the total fetched source the whole
	// fallback may pull in across one render.
	maxTranspileSourceBytes = 16 << 20
	// transpileTimeout bounds one transpileModernJS attempt — short, since
	// this is a best-effort retry path, not the page's own primary script
	// budget. requireBuildTimeout (require.go) bounds a require() call the
	// same way.
	transpileTimeout = 5 * time.Second
	// transpileFetchTimeout bounds a single nested chunk fetch — reused by
	// require.go for its own fetches, the same shape.
	transpileFetchTimeout = 4 * time.Second
	// requireGlobalName is the VM global transpileModernJS's own wrapper
	// passes into the transformed script as its local "require" parameter
	// (never left as a bare global the script's own top-level scope could
	// see under this name — see transpileModernJS's own return statement).
	requireGlobalName = "__webengineRequire"
)

// transpileModernJS attempts to re-emit src (whose compile already failed)
// as plain ES2017. name is the script's own resolved src ("inline#N" for an
// inline script — scriptName's own doc comment); b.opt.PageURL anchors a
// relative dynamic import() the same way an inline module script's own
// imports resolve against the page (modules.go's own bundleModuleScripts
// doc comment), since "inline#N" is not a real URL a relative specifier
// could resolve against.
func (b *binder) transpileModernJS(src, name string) (string, bool) {
	if b.opt.Client == nil {
		return "", false
	}
	scriptURL := name
	if !strings.HasPrefix(scriptURL, "http") {
		scriptURL = b.opt.PageURL
	}

	tctx, cancel := context.WithTimeout(b.opt.Ctx, transpileTimeout)
	defer cancel()

	overBudget := func() bool {
		return tctx.Err() != nil || b.requireFetched >= maxTranspileFetches || b.requireBytes >= maxTranspileSourceBytes
	}
	plugin := api.Plugin{
		Name: "webengine-classic-script-fallback",
		Setup: func(build api.PluginBuild) {
			build.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(args api.OnResolveArgs) (api.OnResolveResult, error) {
				if overBudget() {
					return api.OnResolveResult{}, fmt.Errorf("transpile fallback budget exhausted")
				}
				base := args.Importer
				if base == "" || !strings.HasPrefix(base, "http") {
					base = scriptURL
				}
				abs, ok := resolveURL(base, args.Path)
				if !ok {
					return api.OnResolveResult{}, fmt.Errorf("cannot resolve %q", args.Path)
				}
				return api.OnResolveResult{Path: abs, Namespace: "http"}, nil
			})
			build.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "http"}, func(args api.OnLoadArgs) (api.OnLoadResult, error) {
				if overBudget() {
					return api.OnLoadResult{}, fmt.Errorf("transpile fallback budget exhausted")
				}
				chunk, ok := b.fetchTranspileChunk(tctx, args.Path)
				if !ok {
					// A single unfetchable nested chunk must not abort the whole
					// fallback: serve it empty, matching modules.go's own
					// bundleModuleScripts rationale for the identical case.
					empty := ""
					return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS, ResolveDir: esbuildsandbox.ResolveDir()}, nil
				}
				b.requireFetched++
				b.requireBytes += len(chunk)
				return api.OnLoadResult{Contents: &chunk, Loader: api.LoaderJS, ResolveDir: esbuildsandbox.ResolveDir()}, nil
			})
		},
	}

	type buildOut struct {
		res api.BuildResult
	}
	done := make(chan buildOut, 1)
	go func() {
		done <- buildOut{res: api.Build(api.BuildOptions{
			Stdin: &api.StdinOptions{
				Contents:   src,
				ResolveDir: esbuildsandbox.ResolveDir(),
				Sourcefile: scriptURL,
				Loader:     api.LoaderJS,
			},
			Bundle:        true,
			Format:        api.FormatCommonJS,
			Target:        api.ES2017,
			Write:         false,
			LogLevel:      api.LogLevelSilent,
			Plugins:       []api.Plugin{plugin},
			Sourcemap:     api.SourceMapNone,
			LegalComments: api.LegalCommentsNone,
			Supported:     map[string]bool{"import-meta": false, "dynamic-import": false},
		})}
	}()
	// api.Build is synchronous and takes no context (modules.go's own
	// awaitBounded doc comment covers the identical constraint): stop
	// WAITING once the budget is spent, rather than block transpileTimeout
	// past it. The abandoned goroutine/build finishes and is collected on
	// its own; it can no longer hold up this script's own execute().
	select {
	case out := <-done:
		if len(out.res.Errors) > 0 || len(out.res.OutputFiles) == 0 {
			return "", false
		}
		// Format:CommonJS (above) emits bare require(x) calls for every
		// import() — literal or variable specifier — Supported lowered;
		// wrapping in a function that takes require as its OWN parameter
		// (rather than ever leaving "require" as a true global the
		// script's own top-level scope could observe under this name)
		// keeps every other script's global scope exactly as it was
		// before this fallback existed. requireGlobalName is reset to a
		// fresh closure bound to THIS script's own URL right before this
		// returns, since execute() runs one script at a time on this
		// binder's own single goroutine — safe without a lock.
		b.vm.Set(requireGlobalName, b.makeRequireFn(scriptURL))
		wrapped := "(function(require){\n" + string(out.res.OutputFiles[0].Contents) + "\n})(" + requireGlobalName + ");"
		return wrapped, true
	case <-tctx.Done():
		return "", false
	}
}

// fetchTranspileChunk retrieves one nested dynamic-import target's source,
// bounded — the same shape as fetchScript, parameterized by ctx instead of
// b.opt.Ctx directly since the fallback's own shorter transpileTimeout
// governs here, not the page's whole script budget.
func (b *binder) fetchTranspileChunk(ctx context.Context, abs string) (string, bool) {
	fctx, cancel := context.WithTimeout(ctx, transpileFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, abs, nil)
	if err != nil {
		return "", false
	}
	if b.opt.UserAgent != "" {
		req.Header.Set("User-Agent", b.opt.UserAgent)
	}
	resp, err := b.opt.Client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTranspileSourceBytes))
	if err != nil {
		return "", false
	}
	return string(body), true
}
