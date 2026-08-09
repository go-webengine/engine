// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

// uaDescendantRules are the user-agent stylesheet rules that need a descendant
// selector (which the per-tag uaDeclarations cannot express). They alternate the
// list marker glyph by nesting depth exactly like a browser's UA sheet: a `ul`
// at depth 1 is a disc (set by the tag rule), a `ul ul` at depth 2 a circle, and
// a `ul ul ul` at depth 3-or-deeper a square (the deepest matching, highest
// specificity, rule wins). They are matched at UA origin, so any author rule
// still overrides them.
var uaDescendantRules = ParseStylesheet(
	"ul ul { list-style-type: circle }\n" +
		"ul ul ul { list-style-type: square }\n")

// uaDeclarations returns the user-agent default declarations for a tag, as
// property:value pairs. These mirror a browser's default stylesheet for the
// common structural and text tags Phase 0 supports. Values in px are chosen to
// match typical browser defaults at a 16px root font-size.
func uaDeclarations(tag string) []Declaration {
	switch tag {
	case "html", "body", "div", "section", "article", "header", "footer",
		"nav", "main", "aside", "figure", "form", "hr",
		"blockquote", "pre", "address", "fieldset", "figcaption",
		"dl", "dd", "dt":
		d := []Declaration{{"display", "block"}}
		if tag == "body" {
			d = append(d, Declaration{"margin", "8px"})
		}
		if tag == "blockquote" || tag == "figure" {
			d = append(d, Declaration{"margin", "16px 40px"})
		}
		if tag == "dd" {
			d = append(d, Declaration{"margin-left", "40px"})
		}
		if tag == "pre" {
			d = append(d, Declaration{"font-family", "monospace"},
				Declaration{"white-space", "pre"}, Declaration{"margin", "13px 0"})
		}
		return d
	case "table":
		return []Declaration{{"display", "table"}, {"border-color", "gray"}}
	case "thead", "tbody", "tfoot":
		return []Declaration{{"display", "table-row-group"}}
	case "tr":
		return []Declaration{{"display", "table-row"}}
	case "td":
		return []Declaration{{"display", "table-cell"}, {"padding", "1px"}}
	case "th":
		return []Declaration{{"display", "table-cell"}, {"padding", "1px"},
			{"font-weight", "bold"}, {"text-align", "center"}}
	case "center":
		// The legacy <center> element: a block that centres its inline content and
		// its definite-width block/table children (see AlignCenterBlocks).
		return []Declaration{{"display", "block"}, {"text-align", "-webkit-center"}}
	case "font", "big", "nobr", "wbr", "strike":
		d := []Declaration{{"display", "inline"}}
		if tag == "big" {
			// `larger` ≈ 1.2× the parent size; expressed in em since the engine
			// resolves font-size against the parent.
			d = append(d, Declaration{"font-size", "1.2em"})
		}
		if tag == "strike" {
			d = append(d, Declaration{"text-decoration", "line-through"})
		}
		return d
	case "caption":
		return []Declaration{{"display", "block"}, {"text-align", "center"}}
	case "p":
		return []Declaration{{"display", "block"}, {"margin", "16px 0"}}
	case "h1":
		return []Declaration{{"display", "block"}, {"font-size", "32px"}, {"font-weight", "bold"}, {"margin", "21px 0"}}
	case "h2":
		return []Declaration{{"display", "block"}, {"font-size", "24px"}, {"font-weight", "bold"}, {"margin", "20px 0"}}
	case "h3":
		return []Declaration{{"display", "block"}, {"font-size", "19px"}, {"font-weight", "bold"}, {"margin", "18px 0"}}
	case "h4":
		return []Declaration{{"display", "block"}, {"font-size", "16px"}, {"font-weight", "bold"}, {"margin", "21px 0"}}
	case "h5":
		return []Declaration{{"display", "block"}, {"font-size", "13px"}, {"font-weight", "bold"}, {"margin", "22px 0"}}
	case "h6":
		return []Declaration{{"display", "block"}, {"font-size", "11px"}, {"font-weight", "bold"}, {"margin", "24px 0"}}
	case "ul":
		return []Declaration{{"display", "block"}, {"margin", "16px 0"}, {"padding-left", "40px"},
			{"list-style-type", "disc"}}
	case "ol":
		return []Declaration{{"display", "block"}, {"margin", "16px 0"}, {"padding-left", "40px"},
			{"list-style-type", "decimal"}}
	case "li":
		return []Declaration{{"display", "list-item"}}
	case "a":
		return []Declaration{{"color", "#0000ee"}}
	case "strong", "b":
		return []Declaration{{"font-weight", "bold"}}
	case "em", "i", "cite", "var", "dfn":
		return []Declaration{{"font-style", "italic"}}
	case "small":
		return []Declaration{{"font-size", "13px"}}
	case "code", "kbd", "samp", "tt":
		return []Declaration{{"font-family", "monospace"}}
	case "span", "label", "abbr", "sup", "sub", "mark", "u", "s", "del", "ins",
		"time", "q", "img", "button", "input", "select", "textarea":
		return []Declaration{{"display", "inline"}}
	case "head", "title", "style", "script", "meta", "link", "base", "noscript":
		return []Declaration{{"display", "none"}}
	default:
		return nil
	}
}
