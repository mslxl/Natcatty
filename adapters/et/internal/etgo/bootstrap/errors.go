// SPDX-License-Identifier: Apache-2.0

// Package bootstrap holds the parts of et-go's SSH bootstrap that the
// application reuses: the placeholder generation, the remote etterminal
// command, and the IDPASSKEY parser. The SSH process runner is intentionally
// absent; adapters/et runs the command through connection.Manager instead.
package bootstrap

import "errors"

// ErrNoCredentials means etterminal's output ended without a valid IDPASSKEY
// line, or the line was malformed.
var ErrNoCredentials = errors.New("bootstrap: no IDPASSKEY in etterminal output")

// ErrInvalidConfig means a remote command value failed validation.
var ErrInvalidConfig = errors.New("bootstrap: invalid configuration")
