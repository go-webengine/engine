// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !(webengine_nonet && webengine_nojs)

package engine

import "net/http"

// httpClient is Engine.Client's type: net/http's client.
type httpClient = http.Client
