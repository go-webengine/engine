// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build webengine_fontsext

package paint

import (
	"os"
	"strings"
	"testing"

	"github.com/go-webengine/engine/paint/bundledfonts"
)

// testFontData is what a host serves: go-opentype/fonts' files.
var testFontData = bundledfonts.Data

// The package's other tests run against a host's source, as a build with
// the tag must.
func TestMain(m *testing.M) {
	SetFontSource(bundledfonts.Source)
	os.Exit(m.Run())
}

// Nothing is embedded: without a source, a bundled face cannot be had.
func TestNothingEmbedded(t *testing.T) {
	for _, n := range BundledFontNames() {
		if embeddedFont(n) != nil {
			t.Fatalf("%s is embedded", n)
		}
		if testFontData(n) == nil {
			t.Fatalf("the test serves no %s", n)
		}
	}
	defer SetFontSource(fontSource())
	SetFontSource(nil)
	if _, err := bundledBytes(fileInter); err == nil || !strings.Contains(err.Error(), "webengine_fontsext") {
		t.Fatalf("no source: %v", err)
	}
}
