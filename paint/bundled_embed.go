// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_fontsext

package paint

import "github.com/go-webengine/engine/paint/bundledfonts"

// BundledFontData is the embedded file of one of BundledFontNames, nil for
// another name: what a host that builds a webengine_fontsext module serves,
// byte for byte, so that its renders match this build's.
func BundledFontData(name string) []byte { return embeddedFont(name) }

func embeddedFont(name string) []byte { return bundledfonts.Data(name) }
