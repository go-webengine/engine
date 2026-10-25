// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_fontsext

package paint

// Built with the webengine_fontsext tag, paint embeds none of its bundled
// families (about 6 MiB): the host serves BundledFontNames and hands them
// over through SetFontSource, and a render fetches only the faces it draws.
func embeddedFont(string) []byte { return nil }
