// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

package officecanvas

import (
	"encoding/binary"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/go-opentype/opentype"
)

// names reads a font's family (name ID 1) and subfamily (name ID 2) from its
// 'name' table, preferring the Windows Unicode records — the names an office
// application matches a run's typeface against (OpenType 'name' table; the
// legacy family, so that "Lato Black" stays a family of its own rather than
// a weight of "Lato" that a viewer would draw bold).
func names(f *opentype.Font) (family, sub string) {
	t, ok := f.Table("name")
	if !ok || len(t) < 6 {
		return "", ""
	}
	count := int(binary.BigEndian.Uint16(t[2:]))
	strOff := int(binary.BigEndian.Uint16(t[4:]))
	var best [3]int // per name ID 1,2: score of the record kept
	var out [3]string
	for i := 0; i < count; i++ {
		r := 6 + 12*i
		if r+12 > len(t) {
			break
		}
		platform := binary.BigEndian.Uint16(t[r:])
		encoding := binary.BigEndian.Uint16(t[r+2:])
		lang := binary.BigEndian.Uint16(t[r+4:])
		id := binary.BigEndian.Uint16(t[r+6:])
		length := int(binary.BigEndian.Uint16(t[r+8:]))
		off := strOff + int(binary.BigEndian.Uint16(t[r+10:]))
		if id < 1 || id > 2 || off+length > len(t) || length > 512 {
			continue
		}
		raw := t[off : off+length]
		score, s := 0, ""
		switch {
		case platform == 3 && (encoding == 1 || encoding == 10):
			score, s = 2, utf16be(raw)
			if lang == 0x0409 {
				score = 3
			}
		case platform == 0:
			score, s = 2, utf16be(raw)
		case platform == 1 && encoding == 0:
			score, s = 1, latin1(raw)
		}
		if score > best[id] && s != "" {
			best[id], out[id] = score, s
		}
	}
	return out[1], out[2]
}

func utf16be(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func latin1(b []byte) string {
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// embeddable reads the OS/2 fsType (OpenType 'OS/2' table, offset 8): a font
// may go into an editable document when it is installable (0) or editable
// (bit 3); restricted (bit 1) and preview & print (bit 2) forbid it. A font
// without the table makes no restriction.
func embeddable(f *opentype.Font) bool {
	t, ok := f.Table("OS/2")
	if !ok || len(t) < 10 {
		return true
	}
	fs := binary.BigEndian.Uint16(t[8:])
	if fs&0x0008 != 0 {
		return true
	}
	return fs&0x0006 == 0
}

func lower(s string) string       { return strings.ToLower(s) }
func contains(s, sub string) bool { return strings.Contains(s, sub) }

// maxSFNTBytes bounds a reassembled font file.
const maxSFNTBytes = 32 << 20

// SFNT reassembles a parsed font into a font file — the offset table, the
// table directory with each table's checksum, the tables padded to four
// bytes — from the tables it carries (OpenType "Organization of an OpenType
// Font"), so that the very font a page was laid out with can be embedded
// whole. It returns nil for a font past 32 MiB.
func SFNT(f *opentype.Font) []byte {
	tags := f.TableTags()
	sort.Strings(tags)
	type tbl struct {
		tag  string
		data []byte
	}
	var tables []tbl
	total := 12 + 16*len(tags)
	cff := false
	for _, tag := range tags {
		if len(tag) != 4 {
			continue
		}
		d, ok := f.Table(tag)
		if !ok {
			continue
		}
		if tag == "CFF " || tag == "CFF2" {
			cff = true
		}
		tables = append(tables, tbl{tag, d})
		total += (len(d) + 3) &^ 3
		if total > maxSFNTBytes {
			return nil
		}
	}
	n := len(tables)
	out := make([]byte, 12+16*n, total)
	if cff {
		copy(out, "OTTO")
	} else {
		binary.BigEndian.PutUint32(out, 0x00010000)
	}
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	es := 0
	for 1<<(es+1) <= n {
		es++
	}
	sr := (1 << es) * 16
	binary.BigEndian.PutUint16(out[6:], uint16(sr))
	binary.BigEndian.PutUint16(out[8:], uint16(es))
	binary.BigEndian.PutUint16(out[10:], uint16(n*16-sr))
	headAt := -1
	for i, t := range tables {
		off := len(out)
		data := t.data
		if t.tag == "head" && len(data) >= 12 {
			data = append([]byte(nil), data...)
			binary.BigEndian.PutUint32(data[8:], 0) // checkSumAdjustment, set below
			headAt = off
		}
		out = append(out, data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		d := out[12+16*i:]
		copy(d, t.tag)
		binary.BigEndian.PutUint32(d[4:], checksum(out[off:off+len(data)]))
		binary.BigEndian.PutUint32(d[8:], uint32(off))
		binary.BigEndian.PutUint32(d[12:], uint32(len(data)))
	}
	if headAt >= 0 {
		binary.BigEndian.PutUint32(out[headAt+8:], 0xB1B0AFBA-checksum(out))
	}
	return out
}

func checksum(b []byte) uint32 {
	var s uint32
	for i := 0; i < len(b); i += 4 {
		var w [4]byte
		copy(w[:], b[i:])
		s += binary.BigEndian.Uint32(w[:])
	}
	return s
}
