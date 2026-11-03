// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "bytes"

// Marker precedes the credentials in etterminal's output.
const Marker = marker

// ParseCredentials extracts the first complete IDPASSKEY line from out. It
// returns ErrNoCredentials when the marker is absent or the value is
// malformed. Callers that are still collecting output can check
// HasMarker first and keep reading.
func ParseCredentials(out []byte) (Credentials, error) {
	return parseCredentials(out)
}

// HasMarker reports whether out holds the start of an IDPASSKEY line.
func HasMarker(out []byte) bool {
	return bytes.Contains(out, []byte(marker))
}

// Placeholder returns the throwaway id and passkey sent to etterminal. The
// "XXX" prefix makes a modern server discard both and print fresh ones.
func Placeholder() (id, passkey string) {
	return placeholder()
}
