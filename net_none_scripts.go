// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_nonet && !webengine_nojs

package engine

import "net/http"

// clientDo refuses every request a script makes (a module's source, a
// form's submission): there is no network.
func clientDo(*http.Client, *http.Request) (*http.Response, error) { return nil, ErrNoNetwork }
