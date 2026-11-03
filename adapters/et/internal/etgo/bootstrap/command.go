// SPDX-License-Identifier: Apache-2.0

package bootstrap

import "fmt"

const (
	// DefaultTerm is the TERM etterminal exports for the remote shell.
	DefaultTerm = "xterm-256color"
	// DefaultTerminalPath is the remote etterminal program.
	DefaultTerminalPath = "etterminal"

	// termExtra and terminalPathExtra are the bytes besides ASCII letters and
	// digits allowed in the two configurable values interpolated into the
	// remote shell command. Neither admits a quote, space, glob, '$' or
	// command separator; terminalPathExtra admits '~', which the remote shell
	// expands on purpose.
	//
	// termExtra also excludes '_': etterminal splits its stdin line on '_' and
	// aborts unless there are exactly two tokens, so a TERM containing '_'
	// kills the session start (upstream
	// src/terminal/TerminalMain.cpp:111-124).
	termExtra         = ".+-"
	terminalPathExtra = "._/~-"
)

// RemoteCommand is the command the bootstrap exec channel runs. It mirrors
// upstream's genCommand (src/terminal/SshSetupHandler.cpp): etterminal reads
// "<id>/<passkey>_<TERM>" on stdin. Every interpolated value is validated or
// generated from [A-Za-z0-9].
func RemoteCommand(id, passkey, term, terminalPath string) (string, error) {
	if term == "" {
		term = DefaultTerm
	}
	if terminalPath == "" {
		terminalPath = DefaultTerminalPath
	}
	if !onlyAlnumOr(id, "") || !onlyAlnumOr(passkey, "") {
		return "", fmt.Errorf("%w: id and passkey must be alphanumeric", ErrInvalidConfig)
	}
	if !onlyAlnumOr(term, termExtra) {
		return "", fmt.Errorf("%w: TERM %q must be letters, digits and %s only", ErrInvalidConfig, term, termExtra)
	}
	if !onlyAlnumOr(terminalPath, terminalPathExtra) {
		return "", fmt.Errorf("%w: terminal path %q must be letters, digits and %s only", ErrInvalidConfig, terminalPath, terminalPathExtra)
	}
	return "echo '" + id + "/" + passkey + "_" + term + "' | " + terminalPath + " --verbose=0", nil
}
