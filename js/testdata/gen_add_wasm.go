//go:build ignore

// Copyright (c) the go-webengine/engine authors.
// SPDX-License-Identifier: BSD-3-Clause

// gen_add_wasm.go hand-assembles add.wasm, the fixture wasm_test.go uses to
// exercise this package's WebAssembly binding end-to-end. Run it with
// `go run gen_add_wasm.go` from this directory to regenerate the file.
//
// The module is the binary-format equivalent of this WAT source (hand-
// assembled rather than run through wat2wasm/wasm-tools, neither of which
// this environment had working at the time — verified correct by compiling
// and running it through wazero directly, independent of this package's own
// binding code, before committing it):
//
//	(module
//	  (import "env" "host_offset" (func $host_offset (param i32) (result i32)))
//	  (memory (export "memory") 1)
//	  (func $add (export "add") (param $a i32) (param $b i32) (result i32)
//	    local.get $a
//	    local.get $b
//	    i32.add
//	    call $host_offset)
//	  (func $addf (export "addf") (param $a f64) (param $b f64) (result f64)
//	    local.get $a
//	    local.get $b
//	    f64.add)
//	  (data (i32.const 0) "hello wasm"))
//
// Bibliography: the WebAssembly binary format spec
// (webassembly.github.io/spec/core/binary/) for the section layout, LEB128
// integer encoding, and instruction opcodes (i32.add=0x6A, f64.add=0xA0 —
// not 0xA1, an off-by-one this generator's own first draft got wrong and
// which running it through wazero caught immediately: addf(1.5,2.25)
// answered -0.75, not 3.75).
package main

import (
	"bytes"
	"encoding/binary"
	"os"
)

func uleb(buf *bytes.Buffer, v uint32) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		buf.WriteByte(b)
		if v == 0 {
			return
		}
	}
}

func section(id byte, content []byte) []byte {
	var out bytes.Buffer
	out.WriteByte(id)
	var sz bytes.Buffer
	uleb(&sz, uint32(len(content)))
	out.Write(sz.Bytes())
	out.Write(content)
	return out.Bytes()
}

func vec(items [][]byte) []byte {
	var out bytes.Buffer
	uleb(&out, uint32(len(items)))
	for _, it := range items {
		out.Write(it)
	}
	return out.Bytes()
}

func nameBytes(s string) []byte {
	var out bytes.Buffer
	uleb(&out, uint32(len(s)))
	out.WriteString(s)
	return out.Bytes()
}

const (
	i32 = 0x7f
	f64 = 0x7c
)

func funcType(params, results []byte) []byte {
	var out bytes.Buffer
	out.WriteByte(0x60)
	uleb(&out, uint32(len(params)))
	out.Write(params)
	uleb(&out, uint32(len(results)))
	out.Write(results)
	return out.Bytes()
}

func main() {
	var m bytes.Buffer
	m.WriteString("\x00asm")
	binary.Write(&m, binary.LittleEndian, uint32(1))

	// Type section: 0 = (i32,i32)->i32 ; 1 = (i32)->i32 ; 2 = (f64,f64)->f64
	types := vec([][]byte{
		funcType([]byte{i32, i32}, []byte{i32}),
		funcType([]byte{i32}, []byte{i32}),
		funcType([]byte{f64, f64}, []byte{f64}),
	})
	m.Write(section(1, types))

	// Import section: env.host_offset : type 1 (func import index 0)
	var importFn bytes.Buffer
	importFn.Write(nameBytes("env"))
	importFn.Write(nameBytes("host_offset"))
	importFn.WriteByte(0x00) // func import
	uleb(&importFn, 1)       // type index 1
	imports := vec([][]byte{importFn.Bytes()})
	m.Write(section(2, imports))

	// Function section: two more functions (indices 1 and 2, since 0 is the
	// import): func index 1 -> type 0 (add), func index 2 -> type 2 (addf)
	funcs := vec([][]byte{{0}, {2}})
	m.Write(section(3, funcs))

	// Memory section: 1 memory, min 1 page, no max
	var mem bytes.Buffer
	mem.WriteByte(0x00) // flags: no max
	uleb(&mem, 1)       // min pages
	mems := vec([][]byte{mem.Bytes()})
	m.Write(section(5, mems))

	// Export section: "add" func idx 1, "addf" func idx 2, "memory" mem idx 0
	expAdd := append(nameBytes("add"), 0x00)
	expAdd = append(expAdd, 1)
	expAddf := append(nameBytes("addf"), 0x00)
	expAddf = append(expAddf, 2)
	expMem := append(nameBytes("memory"), 0x02)
	expMem = append(expMem, 0)
	exports := vec([][]byte{expAdd, expAddf, expMem})
	m.Write(section(7, exports))

	// Code section
	var addCode bytes.Buffer
	uleb(&addCode, 0)                                   // 0 local decl groups
	addCode.Write([]byte{0x20, 0x00, 0x20, 0x01, 0x6a})  // local.get 0; local.get 1; i32.add
	addCode.Write([]byte{0x10, 0x00})                    // call func idx 0 (host_offset)
	addCode.WriteByte(0x0b)                              // end
	var addCodeWrapped bytes.Buffer
	uleb(&addCodeWrapped, uint32(addCode.Len()))
	addCodeWrapped.Write(addCode.Bytes())

	var addfCode bytes.Buffer
	uleb(&addfCode, 0)
	addfCode.Write([]byte{0x20, 0x00, 0x20, 0x01, 0xa0}) // local.get 0; local.get 1; f64.add
	addfCode.WriteByte(0x0b)
	var addfCodeWrapped bytes.Buffer
	uleb(&addfCodeWrapped, uint32(addfCode.Len()))
	addfCodeWrapped.Write(addfCode.Bytes())

	code := vec([][]byte{addCodeWrapped.Bytes(), addfCodeWrapped.Bytes()})
	m.Write(section(10, code))

	// Data section: "hello wasm" at offset 0
	var data bytes.Buffer
	data.WriteByte(0x00)                 // active, memory 0 implied
	data.Write([]byte{0x41, 0x00, 0x0b}) // i32.const 0 ; end  (offset expr)
	payload := []byte("hello wasm")
	uleb(&data, uint32(len(payload)))
	data.Write(payload)
	datas := vec([][]byte{data.Bytes()})
	m.Write(section(11, datas))

	if err := os.WriteFile("add.wasm", m.Bytes(), 0644); err != nil {
		panic(err)
	}
}
