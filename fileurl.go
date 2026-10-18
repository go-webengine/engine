// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import (
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// isFileURL reports whether abs is a file: URL.
func isFileURL(abs string) bool {
	return len(abs) >= 5 && strings.EqualFold(abs[:5], "file:")
}

// fileURLPath converts a file: URL into a local filesystem path, reporting
// false for one this engine will not read: a host other than "" or
// "localhost" (a UNC share is not a local file), or no path. A Windows drive
// letter (WHATWG URL §4.2: an ASCII alpha followed by ':' or '|') loses the
// leading slash the URL form puts before it, and a '|' is normalised to ':'
// as the URL parser does.
func fileURLPath(abs string) (string, bool) {
	u, err := url.Parse(abs)
	if err != nil || !strings.EqualFold(u.Scheme, "file") {
		return "", false
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return "", false
	}
	p := u.Path
	if p == "" {
		return "", false
	}
	if len(p) >= 3 && p[0] == '/' && isASCIIAlpha(p[1]) && (p[2] == ':' || p[2] == '|') {
		p = p[1:2] + ":" + p[3:]
	}
	return filepath.FromSlash(p), true
}

func isASCIIAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// readFileURL reads at most limit bytes of the file a file: URL names, only
// when e.FileRoot is set and the file — after every symbolic link in its path
// is resolved — lies under the (equally resolved) root. The read itself then
// goes through an os.Root opened at that root, which refuses any path that
// would leave it, so a link swapped in between the check and the open cannot
// escape either.
func (e *Engine) readFileURL(abs string, limit int64) ([]byte, bool) {
	if e.FileRoot == "" {
		return nil, false
	}
	p, ok := fileURLPath(abs)
	if !ok {
		return nil, false
	}
	realRoot, err := filepath.EvalSymlinks(e.FileRoot)
	if err != nil {
		return nil, false
	}
	realPath, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, false
	}
	rel, err := filepath.Rel(realRoot, realPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, false
	}
	r, err := os.OpenRoot(realRoot)
	if err != nil {
		return nil, false
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, false
	}
	return data, true
}
