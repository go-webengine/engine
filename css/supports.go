// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"regexp"
	"strings"
)

// supportsConditionHolds evaluates an @supports condition (the text after
// "@supports", not yet parenthesis-checked), recognising only ONE specific
// feature test — whether this engine supports the light-dark() CSS function
// — rather than a general property/value feature-query evaluator.
//
// Real CSS ships exactly this test as postcss-preset-env's own light-dark()
// polyfill (confirmed live on developer.mozilla.org): it emits a POSITIVE
// `@supports (color: light-dark(red,red))` block using the native function
// directly, and a NEGATIVE `@supports not (color: light-dark(tan,tan))`
// fallback that instead threads the colour through an intermediate custom
// property (a "CSS toggle", see the doc comment on resolveOneVar in vars.go)
// — so that exactly one of the two blocks' declarations is meant to apply,
// depending on real support. This engine HAS a real, working native
// light-dark() (see lightdark.go, which always resolves to its dark branch —
// this engine's fixed rendering preference) — so before this existed, BOTH
// @supports blocks were skipped wholesale like any other unrecognised
// at-rule, leaving neither the correct native declaration NOR its own
// fallback in effect; only the polyfill's own UNCONDITIONAL default (a
// toggle variable that is only ever valid when a SEPARATE, real dark-mode
// selector also matched) determined the final value — and since that
// default is deliberately written to be invalid-at-computed-value-time on
// its own, resolving it fell through to variable's OWN outer fallback,
// which is the LIGHT-mode-appropriate colour, wrong for this engine's
// dark-mode-only rendering. Confirmed live: developer.mozilla.org's own
// `--color-border-primary` (used, among other places, by its "In this
// article" table-of-contents links' box-shadow-simulated left border)
// resolved to a light-mode grey instead of the correct, much more subtle
// dark-mode one, making every TOC link look like it had an unwanted solid
// highlight box.
//
// Any OTHER @supports condition (a property/value pair this engine does not
// specifically recognise, a selector() query, an unrelated feature test)
// still returns false here — the SAME "drop the whole block" behaviour as
// before this function existed. This is a narrow, honest answer for one
// capability this engine actually has, not a general feature-query engine.
func supportsConditionHolds(cond string) bool {
	cond = strings.TrimSpace(strings.ToLower(cond))
	negate := false
	if rest, ok := cutLeadingWord(cond, "not"); ok {
		negate, cond = true, strings.TrimSpace(rest)
	}
	holds := supportsLightDarkRe.MatchString(cond)
	if negate {
		return !holds
	}
	return holds
}

// supportsLightDarkRe matches "(color: light-dark(...))" — the feature under
// test is light-dark() itself, so the specific probe colours used (real CSS
// varies them: "red,red", "tan,tan", ...) and internal whitespace don't
// matter, only that a light-dark() call appears as color's value.
var supportsLightDarkRe = regexp.MustCompile(`^\(\s*color\s*:\s*light-dark\(`)
