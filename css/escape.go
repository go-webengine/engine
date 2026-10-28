// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package css

import (
	"strings"
	"unicode/utf8"
)

// CSS escapes, per CSS Syntax Module Level 3 (https://www.w3.org/TR/css-syntax-3/):
//
//   - §4.3.8 "check if two code points are a valid escape": a backslash
//     followed by anything but a newline (or EOF) starts an escape;
//   - §4.3.7 "consume an escaped code point": 1 to 6 hex digits, then ONE
//     optional whitespace code point, decode to that code point — or to
//     U+FFFD when it is zero, a surrogate, or above U+10FFFF; EOF right after
//     the backslash is also U+FFFD; any other code point stands for itself;
//   - §4.3.5 "consume a string token": inside a string, a backslash followed
//     by a newline is a line continuation (both are dropped), a backslash
//     followed by EOF is dropped, and an unescaped newline makes a bad string.
//
// This engine's parser is string-based rather than a token stream, so the
// decoding happens at the two places where text enters it: a declaration's
// value (ParseDeclarations, via normalizeEscapes) and a selector prelude
// (ParseSelectorList, via normalizeSelectorEscapes). Both rewrite every hex
// escape into a form the downstream string-based parsers already read
// correctly, so every string and identifier — not only `content` — sees the
// decoded code point.

// maxHexEscapeDigits is the spec's bound on an escape's hex digits; it is
// also what keeps the decoder linear: an escape never reads more than 6
// digits plus one whitespace, whatever follows.
const maxHexEscapeDigits = 6

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexVal(c byte) rune {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0')
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10
	default:
		return rune(c-'A') + 10
	}
}

// isCSSNewline is a CSS newline after preprocessing (§3.3 turns CR, FF and
// CRLF into LF; this parser sees unpreprocessed text, so it accepts all three).
func isCSSNewline(c byte) bool { return c == '\n' || c == '\r' || c == '\f' }

// isCSSWhitespace is §4.2 whitespace: newline, tab or space.
func isCSSWhitespace(c byte) bool { return c == ' ' || c == '\t' || isCSSNewline(c) }

// validEscapeAt reports whether s[i] is a backslash that starts a valid
// escape (§4.3.8). A backslash as the very last byte is NOT reported valid
// here: callers handle that EOF case themselves, since its meaning differs
// between a string (dropped) and the rest of a stylesheet (a lone delim).
func validEscapeAt(s string, i int) bool {
	return i+1 < len(s) && s[i] == '\\' && !isCSSNewline(s[i+1])
}

// consumeEscape decodes the escape whose backslash is at s[i-1]: i is the
// index of the first code point after it (§4.3.7). It returns the decoded
// code point, whether it came from hex digits, and the index just past the
// escape (including the one whitespace a hex escape swallows; a CRLF counts
// as one whitespace, as preprocessing would have made it).
func consumeEscape(s string, i int) (r rune, hex bool, next int) {
	if i >= len(s) {
		return utf8.RuneError, false, i // EOF: parse error, U+FFFD
	}
	if !isHexDigit(s[i]) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == 0 {
			r = utf8.RuneError // preprocessing (§3.3) made a NUL U+FFFD
		}
		return r, false, i + size
	}
	var v rune
	j := i
	for j < len(s) && j-i < maxHexEscapeDigits && isHexDigit(s[j]) {
		v = v<<4 | hexVal(s[j])
		j++
	}
	if j < len(s) && isCSSWhitespace(s[j]) {
		if s[j] == '\r' && j+1 < len(s) && s[j+1] == '\n' {
			j++
		}
		j++
	}
	if v == 0 || (v >= 0xD800 && v <= 0xDFFF) || v > utf8.MaxRune {
		v = utf8.RuneError
	}
	return v, true, j
}

// ParseString consumes a CSS string token starting at s[0] (which must be
// '"' or '\”), per §4.3.5. It returns the decoded value and the index just
// past the closing quote. ok is false for a bad string (an unescaped newline)
// or when s does not start with a quote. A string that runs to EOF without
// its closing quote is returned as-is (a parse error the spec still accepts).
func ParseString(s string) (val string, end int, ok bool) {
	if s == "" || (s[0] != '"' && s[0] != '\'') {
		return "", 0, false
	}
	quote := s[0]
	// Fast path: no escape before the closing quote.
	if k := strings.IndexByte(s[1:], quote); k >= 0 {
		body := s[1 : 1+k]
		if strings.IndexByte(body, '\\') < 0 && strings.IndexAny(body, "\n\r\f") < 0 {
			return body, k + 2, true
		}
	}
	var b strings.Builder
	i := 1
	for i < len(s) {
		c := s[i]
		switch {
		case c == quote:
			return b.String(), i + 1, true
		case isCSSNewline(c):
			return "", i, false // bad string
		case c == '\\':
			if i+1 >= len(s) {
				i++ // backslash then EOF: do nothing
				continue
			}
			if isCSSNewline(s[i+1]) { // line continuation
				i += 2
				if s[i-1] == '\r' && i < len(s) && s[i] == '\n' {
					i++
				}
				continue
			}
			r, _, next := consumeEscape(s, i+1)
			b.WriteRune(r)
			i = next
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), len(s), true // EOF before the closing quote
}

// unescapeIdent decodes every escape in an identifier-like run (a counter
// name, a counter-style name). Text with no backslash is returned unchanged.
func unescapeIdent(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if validEscapeAt(s, i) {
			r, _, next := consumeEscape(s, i+1)
			b.WriteRune(r)
			i = next
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// unescapeSimple drops the backslash of every simple escape (`\:` → `:`), for
// selector text that normalizeSelectorEscapes already rid of hex escapes.
func unescapeSimple(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isASCIILetterOrUnderscore is the ASCII part of an ident-start code point.
func isASCIILetterOrUnderscore(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_'
}

// writeHexEscape writes the canonical hex form of r: backslash, lowercase
// hex digits, and one terminating space (so a following hex digit can never
// be read as part of it).
func writeHexEscape(b *strings.Builder, r rune) {
	const digits = "0123456789abcdef"
	var buf [8]byte
	n := len(buf)
	for {
		n--
		buf[n] = digits[r&0xF]
		r >>= 4
		if r == 0 {
			break
		}
	}
	b.WriteByte('\\')
	b.Write(buf[n:])
	b.WriteByte(' ')
}

// normalizeEscapes rewrites the escapes in a declaration value into a
// canonical form that every value parser in this package reads correctly,
// and that is a fixed point (normalising twice changes nothing — a custom
// property's value is normalised once in its own declaration, then again in
// the declaration it is substituted into):
//
//   - a non-ASCII code point (including the U+FFFD of an invalid escape) and
//     an ASCII letter or '_' are written as themselves — both mean the same
//     thing escaped or not, in an identifier and in a string;
//   - an ASCII digit and an ASCII control are written as a canonical hex
//     escape (`\31 `): written as itself, a digit could start a number
//     where the escape made it part of an identifier;
//   - any other ASCII code point (punctuation, space, a quote, the
//     backslash) is written as a simple escape (`\;`, `\"`), so it can never
//     close a string, end a declaration or open a block;
//   - inside a string, a backslash-newline line continuation is removed and
//     a trailing backslash at EOF is dropped (§4.3.5).
//
// A value with no backslash — virtually every real one — is returned as is,
// without allocating.
func normalizeEscapes(v string) string {
	if strings.IndexByte(v, '\\') < 0 {
		return v
	}
	var b strings.Builder
	b.Grow(len(v))
	var quote byte
	for i := 0; i < len(v); {
		c := v[i]
		if quote != 0 {
			switch {
			case c == quote:
				quote = 0
			case isCSSNewline(c):
				quote = 0 // a bad string ends at the newline; keep the text
			case c == '\\':
				if i+1 >= len(v) {
					i++ // backslash then EOF inside a string: dropped
					continue
				}
				if isCSSNewline(v[i+1]) { // line continuation: dropped
					i += 2
					if v[i-1] == '\r' && i < len(v) && v[i] == '\n' {
						i++
					}
					continue
				}
				i = writeCanonicalEscape(&b, v, i)
				continue
			}
			b.WriteByte(c)
			i++
			continue
		}
		switch {
		case c == '"' || c == '\'':
			quote = c
		case validEscapeAt(v, i):
			i = writeCanonicalEscape(&b, v, i)
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// writeCanonicalEscape decodes the escape whose backslash is at v[i], writes
// its canonical form (see normalizeEscapes), and returns the index after it.
func writeCanonicalEscape(b *strings.Builder, v string, i int) int {
	r, _, next := consumeEscape(v, i+1)
	switch {
	case r >= utf8.RuneSelf || isASCIILetterOrUnderscore(r):
		b.WriteRune(r)
	case (r >= '0' && r <= '9') || r < 0x20 || r == 0x7F:
		writeHexEscape(b, r)
	default:
		b.WriteByte('\\')
		b.WriteByte(byte(r))
	}
	return next
}

// normalizeSelectorEscapes rewrites the hex escapes of a selector prelude
// into the form the selector parser reads: it treats a backslash as "the
// next byte is literal", which is right for a simple escape (`.sm\:p-4`) but
// wrong for a hex one (`.\31 0` is the class "10": the space belongs to the
// escape, not a descendant combinator). A decoded non-ASCII code point is
// written as itself, an ASCII one as a simple escape of that byte. The
// selector parser never decodes hex itself, so this runs exactly once, at
// ParseSelectorList.
func normalizeSelectorEscapes(s string) string {
	if strings.IndexByte(s, '\\') < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	var quote byte
	for i := 0; i < len(s); {
		c := s[i]
		if quote != 0 && c == quote {
			quote = 0
		} else if quote == 0 && (c == '"' || c == '\'') {
			quote = c
		}
		if quote != 0 && validEscapeAt(s, i) {
			// Inside an attribute selector's quoted value: the value form,
			// which unquoteAttrValue decodes with ParseString.
			i = writeCanonicalEscape(&b, s, i)
			continue
		}
		if validEscapeAt(s, i) && isHexDigit(s[i+1]) {
			r, _, next := consumeEscape(s, i+1)
			if r >= utf8.RuneSelf {
				b.WriteRune(r)
			} else {
				b.WriteByte('\\')
				b.WriteByte(byte(r))
			}
			i = next
			continue
		}
		if c == '\\' && i+1 < len(s) {
			// A simple escape: copy the pair, so an escaped quote never
			// toggles the quote state above.
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}
