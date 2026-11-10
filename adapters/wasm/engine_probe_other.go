//go:build !darwin && !linux && !windows

package wasm

import (
	"errors"
	"runtime"
)

// probeExecutableMemory reports that this build has no supported plugin
// engine. PlatformSupported already reports unavailable for these targets, so
// this is only a compile-time companion for unsupported builds.
func probeExecutableMemory() error {
	return errors.New("executable memory is unsupported on " + runtime.GOOS)
}
