//go:build tinygo.wasm

// Command hello-command-tinygo is the TinyGo command sample. It imports the
// checked-in generated bindings in ./bindings and exports the required v1
// reactor surface. The generated bindings.GuestInit performs the init-time ABI
// negotiation; this file is only the plugin's glue.
package main

import (
	"unsafe"

	bindings "natcatty.example/hello-tinygo/bindings"
)

// hello and scratch are static byte arrays so the sample links under the
// freestanding target's -gc=none and needs neither the runtime allocator nor
// package init.
var hello = [...]byte{'h', 'e', 'l', 'l', 'o', ' ', 'f', 'r', 'o', 'm', ' ', 't', 'i', 'n', 'y', 'g', 'o'}
var scratch [4096]byte

//export natcatty_abi_version
func natcattyABIVersion() int32 { return bindings.ABIVersion }

//export natcatty_init
func natcattyInit() int32 { return bindings.GuestInit() }

//export natcatty_dispatch
func natcattyDispatch(commandPtr uint32, commandLen uint32) int32 {
	_ = commandPtr
	_ = commandLen
	if bindings.HostOutputWrite(hello[:]) < 0 {
		return -1
	}
	return 0
}

//export natcatty_scratch
func natcattyScratch() uint32 { return uint32(uintptr(unsafe.Pointer(&scratch[0]))) }

//export natcatty_poll
func natcattyPoll() int32 { return 0 }

//export natcatty_shutdown
func natcattyShutdown() int32 { return 0 }

func main() {}
