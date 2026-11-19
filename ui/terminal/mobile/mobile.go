//go:build ios || android

// Package mobile links libghostty-vt into a phone build.
//
// On iOS the library arrives as a *_ios_arm64.syso object staged next to this
// file by scripts/mobile/ios-syso.sh: the object is already built for the
// device or the simulator, so #cgo LDFLAGS stays empty and MyGo's final
// Swift link is untouched. On Android each Go arch names its own archive, and
// MyGo's c-shared link applies these LDFLAGS.
//
// addr.c takes the address of every symbol the Go bindings look up, so
// -dead_strip keeps them and the purego call mechanism works unchanged: the
// only difference from the desktop loader is where the addresses come from.
package mobile

/*
#cgo android,arm64 LDFLAGS: ${SRCDIR}/lib/android/arm64-v8a/libghostty-vt.a
#cgo android,arm LDFLAGS: ${SRCDIR}/lib/android/armeabi-v7a/libghostty-vt.a
#cgo android,amd64 LDFLAGS: ${SRCDIR}/lib/android/x86_64/libghostty-vt.a

#include <stdint.h>
#include <stdlib.h>
#include "addr.h"
*/
import "C"

import "unsafe"

// Addr returns the address of a ghostty_* symbol linked into the app, or 0
// when the library does not export it.
func Addr(name string) uintptr {
	c := C.CString(name)
	defer C.free(unsafe.Pointer(c))
	return uintptr(C.natcatty_ghostty_addr(c))
}
