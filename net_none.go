// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_nonet

package engine

import (
	"errors"
	"net/http"
)

// Built with the webengine_nonet tag, the engine makes no network request:
// an http(s) URL — a page, a stylesheet, an image, a font, a module — is
// refused with ErrNoNetwork, as by a browser offline, while data: URLs and
// file: URLs under Engine.FileRoot load as usual. New gives the Engine no
// Client, and none of the HTTP stack (browserhttp's TLS fingerprinting,
// net/http's transport) is linked in. A host that renders self-contained
// documents (a slide deck exported in a browser's WebAssembly worker) saves
// that download.

// ErrNoNetwork is the error a request gets in a build without network.
var ErrNoNetwork = errors.New("engine: built without network (webengine_nonet)")

// defaultUserAgent is browserhttp.DefaultUserAgent, which a script still
// reads as navigator.userAgent (net_none_test.go keeps the two equal).
const defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// newHTTPClient gives no client: there is no network.
func newHTTPClient() *http.Client { return nil }

// clientDo refuses every request.
func clientDo(*http.Client, *http.Request) (*http.Response, error) { return nil, ErrNoNetwork }
