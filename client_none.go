// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_nonet && webengine_nojs

package engine

// httpClient is Engine.Client's type in a build with neither network nor
// JavaScript: a placeholder, so that net/http is not linked at all. The
// host's Engine.Fetcher is the only way to the network.
type httpClient struct{}
