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
		"ul ul ul { list-style-type: square }\n" +
		// A closed <details> (no "open" attribute) shows only its <summary>;
		// every OTHER direct child is native browser behaviour, not a CSS
		// property an author sets — confirmed load-bearing live: pkg.go.dev's
		// help tooltips are <details class="go-Tooltip"><summary>...</summary>
		// <p role="tooltip">the tooltip text</p></details>, toggled open only
		// by a click (a static render never triggers one); without this rule
		// every such tooltip's real text rendered permanently visible,
		// overlapping the page. An `open`-attributed <details> (or any author
		// rule targeting its children) is unaffected — this selector simply
		// never matches one.
		"details:not([open]) > :not(summary) { display: none }\n")

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
		d := []Declaration{{Property: "display", Value: "block"}}
		if tag == "body" {
			d = append(d, Declaration{Property: "margin", Value: "8px"})
		}
		if tag == "blockquote" || tag == "figure" {
			d = append(d, Declaration{Property: "margin", Value: "16px 40px"})
		}
		if tag == "dd" {
			d = append(d, Declaration{Property: "margin-left", Value: "40px"})
		}
		if tag == "pre" {
			d = append(d, Declaration{Property: "font-family", Value: "monospace"},
				Declaration{Property: "white-space", Value: "pre"}, Declaration{Property: "margin", Value: "13px 0"})
		}
		return d
	case "table":
		return []Declaration{{Property: "display", Value: "table"}, {Property: "border-color", Value: "gray"}}
	case "thead", "tbody", "tfoot":
		return []Declaration{{Property: "display", Value: "table-row-group"}}
	case "tr":
		return []Declaration{{Property: "display", Value: "table-row"}}
	case "td":
		return []Declaration{{Property: "display", Value: "table-cell"}, {Property: "padding", Value: "1px"}}
	case "th":
		return []Declaration{{Property: "display", Value: "table-cell"}, {Property: "padding", Value: "1px"},
			{Property: "font-weight", Value: "bold"}, {Property: "text-align", Value: "center"}}
	case "center":
		// The legacy <center> element: a block that centres its inline content and
		// its definite-width block/table children (see AlignCenterBlocks).
		return []Declaration{{Property: "display", Value: "block"}, {Property: "text-align", Value: "-webkit-center"}}
	case "font", "big", "nobr", "wbr", "strike":
		d := []Declaration{{Property: "display", Value: "inline"}}
		if tag == "big" {
			// `larger` ≈ 1.2× the parent size; expressed in em since the engine
			// resolves font-size against the parent.
			d = append(d, Declaration{Property: "font-size", Value: "1.2em"})
		}
		if tag == "strike" {
			d = append(d, Declaration{Property: "text-decoration", Value: "line-through"})
		}
		return d
	case "caption":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "text-align", Value: "center"}}
	case "p":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "margin", Value: "16px 0"}}
	case "h1":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "font-size", Value: "32px"}, {Property: "font-weight", Value: "bold"}, {Property: "margin", Value: "21px 0"}}
	case "h2":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "font-size", Value: "24px"}, {Property: "font-weight", Value: "bold"}, {Property: "margin", Value: "20px 0"}}
	case "h3":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "font-size", Value: "19px"}, {Property: "font-weight", Value: "bold"}, {Property: "margin", Value: "18px 0"}}
	case "h4":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "font-size", Value: "16px"}, {Property: "font-weight", Value: "bold"}, {Property: "margin", Value: "21px 0"}}
	case "h5":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "font-size", Value: "13px"}, {Property: "font-weight", Value: "bold"}, {Property: "margin", Value: "22px 0"}}
	case "h6":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "font-size", Value: "11px"}, {Property: "font-weight", Value: "bold"}, {Property: "margin", Value: "24px 0"}}
	case "ul":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "margin", Value: "16px 0"}, {Property: "padding-left", Value: "40px"},
			{Property: "list-style-type", Value: "disc"}}
	case "ol":
		return []Declaration{{Property: "display", Value: "block"}, {Property: "margin", Value: "16px 0"}, {Property: "padding-left", Value: "40px"},
			{Property: "list-style-type", Value: "decimal"}}
	case "li":
		return []Declaration{{Property: "display", Value: "list-item"}}
	case "a":
		return []Declaration{{Property: "color", Value: "#0000ee"}}
	case "strong", "b":
		return []Declaration{{Property: "font-weight", Value: "bold"}}
	case "em", "i", "cite", "var", "dfn":
		return []Declaration{{Property: "font-style", Value: "italic"}}
	case "small":
		return []Declaration{{Property: "font-size", Value: "13px"}}
	case "code", "kbd", "samp", "tt":
		return []Declaration{{Property: "font-family", Value: "monospace"}}
	case "span", "label", "abbr", "sup", "sub", "mark", "u", "s", "del", "ins",
		"time", "q", "img", "button", "input", "select", "textarea":
		return []Declaration{{Property: "display", Value: "inline"}}
	case "head", "title", "style", "script", "meta", "link", "base", "noscript":
		return []Declaration{{Property: "display", Value: "none"}}
	case "option":
		// A real <select> is a replaced, OS-native control: it shows only the
		// currently selected option's text, on one line, entirely opaque to CSS
		// box layout — <option>/<optgroup> children never participate in normal
		// document flow the way this engine's generic "select" (line 114,
		// display:inline) fallback implies. Observed live on pkg.go.dev/net/http:
		// its version/tab-switcher <select> (dozens of <option>s, one holding the
		// page's ENTIRE alphabetical symbol index as option text) rendered every
		// option's text as ordinary inline content, wrapping across 455px/~19
		// lines of concatenated identifiers stacked at the select's DOM position.
		// This engine has no native form-control rendering at all (an <input>'s
		// value is equally never shown — an existing, accepted simplification),
		// so hiding <option> content is the same honest-about-the-gap choice
		// already used for <template>'s subtree above, not a new kind of gap.
		return []Declaration{{Property: "display", Value: "none"}}
	case "template":
		// A <template>'s children are inert: per the HTML spec they live in the
		// element's .content DocumentFragment, never in the normal document tree,
		// so no browser lays them out — CSS cannot make them visible (unlike the
		// other display:none defaults above, this one is not really author-
		// overridable, it just happens to be modelled the same way). This engine
		// has no separate content-fragment concept and no Shadow DOM, so a real
		// page's <template shadowrootmode="open"> (declarative shadow DOM) would
		// otherwise have its shadow markup parsed straight into the light-DOM
		// tree and painted inline — observed live on developer.mozilla.org: 23
		// declarative-shadow templates rendered as unstyled, unscoped nav trees
		// stacked on top of the article text. Hiding the subtree is honest about
		// the gap (no content) rather than rendering something broken.
		return []Declaration{{Property: "display", Value: "none"}}
	default:
		return nil
	}
}
