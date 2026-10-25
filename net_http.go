// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_nonet

package engine

import (
	"net/http"
	"time"

	"github.com/go-browserhttp/browserhttp"
)

// defaultUserAgent is the User-Agent New gives an Engine.
const defaultUserAgent = browserhttp.DefaultUserAgent

// newHTTPClient is the client New gives an Engine: browser-like (Chrome TLS
// fingerprint, cookie jar, redirect following), at most
// maxPerHostConcurrency requests in flight per host.
func newHTTPClient() *http.Client {
	client := browserhttp.NewClient(30 * time.Second)
	client.Transport = newPerHostLimitedTransport(client.Transport, maxPerHostConcurrency)
	return client
}

// clientDo sends req through client: every request the engine makes goes
// through here, so that a build without network (webengine_nonet) can leave
// net/http's transport out.
func clientDo(client *http.Client, req *http.Request) (*http.Response, error) {
	return client.Do(req)
}
