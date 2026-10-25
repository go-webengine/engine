// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !webengine_fontsext

package paint

import (
	"strings"
	"testing"

	"github.com/go-opentype/opentype"
)

// testFontData is the bundled files as this build has them: embedded.
var testFontData = embeddedFont

// The names and the embedded files agree, and every one parses.
func TestBundledFontNames(t *testing.T) {
	names := BundledFontNames()
	if len(names) != 13 {
		t.Fatalf("%d names", len(names))
	}
	for _, n := range names {
		b := BundledFontData(n)
		if b == nil {
			t.Fatalf("%s is not embedded", n)
		}
		if _, err := opentype.Parse(b); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	if BundledFontData("lato/lato.ttf") != nil {
		t.Error("a file paint does not bundle")
	}
	if _, err := bundledBytes("lato/lato.ttf"); err == nil || !strings.Contains(err.Error(), "SetFontSource") {
		t.Errorf("no source for a file paint does not bundle: %v", err)
	}
}
