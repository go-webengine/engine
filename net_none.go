// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_nonet

package engine

import (
	"context"
	"errors"
)

// Built with the webengine_nonet tag, the engine has no HTTP client: an
// http(s) URL — a page, a stylesheet, an image, a font, a module — goes to
// Engine.Fetcher when the host set one, and is refused with ErrNoNetwork
// otherwise, as by a browser offline, while data: URLs and file: URLs
// under Engine.FileRoot load as usual. New gives the Engine no Client, and
// none of browserhttp's TLS fingerprinting is linked in; with
// webengine_nojs too, no net/http at all. A host that renders
// self-contained documents, or whose platform fetches for it (a slide deck
// exported in a browser's WebAssembly worker), saves that download.

// ErrNoNetwork is the error a request gets in a build without network.
var ErrNoNetwork = errors.New("engine: built without network (webengine_nonet)")

// defaultUserAgent is browserhttp.DefaultUserAgent, which a script still
// reads as navigator.userAgent (net_none_test.go keeps the two equal).
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// newHTTPClient gives no client: there is no network.
func newHTTPClient() *httpClient { return nil }

// httpGet refuses: there is no network but the host's Fetcher.
func (e *Engine) httpGet(context.Context, string) ([]byte, string, string, error) {
	return nil, "", "", ErrNoNetwork
}

// httpBytes refuses: there is no network but the host's Fetcher.
func (e *Engine) httpBytes(context.Context, string, int64) ([]byte, bool) { return nil, false }
