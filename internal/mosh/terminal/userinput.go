// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

// UserInput translates the client's keyboard bytes for the server. The local
// terminal is always forced into application-cursor-key mode by Display.open;
// when the remote terminal is not in that mode, SS3 A-D must be rewritten to
// CSI A-D before they are sent to the server.
type UserInput struct {
	state userInputState
}

type userInputState uint8

const (
	userInputGround userInputState = iota
	userInputESC
	userInputSS3
)

// NewUserInput returns a user input translator in the ground state.
func NewUserInput() *UserInput { return &UserInput{} }

// Input translates one user byte and returns the bytes to send. It mirrors
// UserInput::input, including the one-byte SS3 lookahead.
func (u *UserInput) Input(c byte, applicationModeCursorKeys bool) string {
	switch u.state {
	case userInputGround:
		if c == 0x1b {
			u.state = userInputESC
		}
		return string([]byte{c})

	case userInputESC:
		if c == 'O' {
			u.state = userInputSS3
			return ""
		}
		u.state = userInputGround
		return string([]byte{c})

	case userInputSS3:
		u.state = userInputGround
		if !applicationModeCursorKeys && c >= 'A' && c <= 'D' {
			return string([]byte{'[', c})
		}
		return string([]byte{'O', c})

	default:
		u.state = userInputGround
		return ""
	}
}

// Reset returns the translator to its initial state.
func (u *UserInput) Reset() { u.state = userInputGround }

// StateEqual reports whether two translators are in the same state, matching
// UserInput::operator==.
func (u *UserInput) StateEqual(other *UserInput) bool { return u.state == other.state }
