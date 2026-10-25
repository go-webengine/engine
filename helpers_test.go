// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package engine

import "github.com/go-webengine/engine/dom"

// findByID returns the first element whose id attribute equals id.
func findByID(root *dom.Node, id string) *dom.Node {
	var found *dom.Node
	var walk func(n *dom.Node)
	walk = func(n *dom.Node) {
		if found != nil {
			return
		}
		if n.Type == dom.Element && n.ID() == id {
			found = n
			return
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(root)
	return found
}
