// Package transport holds the shared vocabulary for choosing which client
// implementation opens a Mosh or EternalTerminal Session: the bundled
// packaged client, or the application-owned in-process client.
//
// The choice is persisted globally and per Host, so the values are stable.
// An unknown persisted value is not a reason to fall back: Resolve reports a
// typed error and the Session factory fails closed.
package transport

import (
	"errors"
	"fmt"
)

// Implementation names one client implementation for Mosh and
// EternalTerminal Sessions. The string values are persisted in Host records
// and in the preferences document.
type Implementation string

const (
	// Unspecified is the zero value. A Host uses it to inherit the global
	// choice; globally it means the release default from ReleasePolicy.
	Unspecified Implementation = ""
	// Bundled is the packaged mosh-client or et client.
	Bundled Implementation = "bundled"
	// InProcess is the application-owned, pure-Go client.
	InProcess Implementation = "in_process"
)

// ErrUnknownImplementation reports a persisted value this build does not
// recognize. Callers must fail closed instead of falling back to another
// implementation.
var ErrUnknownImplementation = errors.New("transport: unknown implementation")

// ErrImplementationUnavailable reports a known implementation this build
// cannot supply, such as an in-process client a packaging variant omitted.
// Callers must fail closed instead of silently using the bundled client.
var ErrImplementationUnavailable = errors.New("transport: implementation unavailable")

// Known reports whether value names an implementation this build recognizes.
// It does not report whether the implementation can actually open a Session:
// the in-process clients are recognized so the factory can report a typed
// unavailable error rather than an unknown value.
func Known(value Implementation) bool {
	return value == Bundled || value == InProcess
}

// Resolve applies a Host's override to the persisted global choice. An empty
// override inherits the global choice, and an empty global choice means the
// release default from ReleasePolicy, so a Host or profile saved before the
// setting existed keeps working. A non-empty value this build does not
// recognize fails closed rather than falling back to the bundled client.
func Resolve(global, override Implementation) (Implementation, error) {
	if override != Unspecified {
		return validate(override)
	}
	if global == Unspecified {
		return CurrentDefault(), nil
	}
	return validate(global)
}

func validate(value Implementation) (Implementation, error) {
	if Known(value) {
		return value, nil
	}
	return Unspecified, fmt.Errorf("%w: %q", ErrUnknownImplementation, value)
}
