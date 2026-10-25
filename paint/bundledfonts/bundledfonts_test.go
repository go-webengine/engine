// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package bundledfonts

import "testing"

func TestSource(t *testing.T) {
	if len(files) != 13 {
		t.Fatalf("%d files", len(files))
	}
	for name := range files {
		b, err := Source(name)
		if err != nil || len(b) < 1000 || string(Data(name)) != string(b) {
			t.Fatalf("%s: %d bytes, %v", name, len(b), err)
		}
	}
	if _, err := Source("lato/lato.ttf"); err == nil || Data("lato/lato.ttf") != nil {
		t.Fatal("a file not bundled")
	}
}
