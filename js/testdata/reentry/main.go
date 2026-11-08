//go:build js

// Command reentry is the smallest real reproduction of a native crash
// go-wasmcontainers reported: it awaits an ordinary resolved Promise
// through a js.FuncOf callback (which resumes the guest from the event
// loop), then writes to stdout a second time. wasm_exec.js's own fs.write
// shim invokes its callback SYNCHRONOUSLY, re-entering the Go wasm
// instance (go._resume) from INSIDE an active host import call — fine the
// first time (before any event-loop resume has happened), but fatal the
// second time, once the guest has already been resumed from the event
// loop once. See js/wasm_test.go's TestReentrantResumeAfterEventLoopResume
// for what this proves and why.
package main

import (
	"fmt"
	"syscall/js"
)

func main() {
	fmt.Println("stdout before any promise")
	ch := make(chan js.Value, 1)
	f := js.FuncOf(func(_ js.Value, a []js.Value) any { ch <- a[0]; return nil })
	js.Global().Get("Promise").Call("resolve", 1).Call("then", f)
	<-ch
	f.Release()
	fmt.Println("stdout after the promise")
	js.Global().Get("console").Call("log", "REENTRY-OK")
}
