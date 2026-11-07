package sessionui

import (
	"bytes"
	"errors"
	"strings"

	appsession "github.com/mslxl/natcatty/application/session"
)

// ErrInsertUnsafe reports a multi-line command that cannot be inserted safely
// because the program did not enable bracketed paste mode. A bare newline
// would submit the line, so the surface refuses instead of typing it.
var ErrInsertUnsafe = errors.New("session: the shell cannot insert multi-line text safely")

// errInsertUnavailable reports a terminal that is gone or a Session whose
// transfer gate refused the insertion.
var errInsertUnavailable = errors.New("session: insertion is unavailable")

// InsertText puts a command into the terminal's line editor without submitting
// it. Single-line text is sent as typed. Text with a line break or a tab is
// sent as a bracketed paste when the program enabled bracketed paste mode, so
// the editor keeps every line in its buffer instead of executing it; without
// that mode the insertion is refused. A trailing newline is part of the pasted
// buffer and never a submit.
func (s *Surface) InsertText(text string) error {
	if s.terminal == nil || s.session == nil {
		return errInsertUnavailable
	}
	text = sanitizeInsertText(text)
	if text == "" {
		return errInsertUnavailable
	}
	if strings.ContainsAny(text, "\n\t") {
		if !s.bracketedPasteEnabled() {
			return ErrInsertUnsafe
		}
		payload := make([]byte, 0, len(text)+12)
		payload = append(payload, "\x1b[200~"...)
		payload = append(payload, text...)
		payload = append(payload, "\x1b[201~"...)
		if !s.session.InsertInput(payload) {
			return errInsertUnavailable
		}
		return nil
	}
	if !s.session.InsertInput([]byte(text)) {
		return errInsertUnavailable
	}
	return nil
}

// BracketedPasteEnabled reports whether the program in the terminal enabled
// DEC private mode 2004. InsertText requires it for multi-line text, and a
// caller can use it to explain why an insertion needs a copy instead.
func (s *Surface) BracketedPasteEnabled() bool {
	return s.terminal != nil && s.bracketedPasteEnabled()
}

// bracketedPasteEnabled reports whether the terminal's program enabled DEC
// private mode 2004. Terminal.Snapshot is the emulator's own VT state, so this
// is the same mode its Paste path uses rather than a second parser over the
// transport.
func (s *Surface) bracketedPasteEnabled() bool {
	return bracketedPasteEnabled(s.terminal.Snapshot())
}

// bracketedPasteEnabled reads the mode from a terminal's VT snapshot. The last
// private-mode sequence for parameter 2004 wins.
func bracketedPasteEnabled(snapshot []byte) bool {
	enabled := false
	for index := 0; index+3 < len(snapshot); index++ {
		if snapshot[index] != 0x1b || snapshot[index+1] != '[' || snapshot[index+2] != '?' {
			continue
		}
		end := index + 3
		for end < len(snapshot) && snapshot[end] >= 0x30 && snapshot[end] <= 0x3f {
			end++
		}
		if end >= len(snapshot) || (snapshot[end] != 'h' && snapshot[end] != 'l') {
			continue
		}
		for _, parameter := range bytes.Split(snapshot[index+3:end], []byte(";")) {
			if string(parameter) == "2004" {
				enabled = snapshot[end] == 'h'
			}
		}
		index = end
	}
	return enabled
}

// sanitizeInsertText normalizes line endings and removes control characters
// that would act as keystrokes or terminal escapes. Newline and tab stay,
// because they are part of the command text. Removing ESC is required for
// safety as well as correctness: a command containing the bracketed-paste end
// marker could otherwise leave the paste and submit the text that follows it.
func sanitizeInsertText(text string) string {
	text = appsession.SanitizeTerminalInput(text)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Map(func(character rune) rune {
		switch character {
		case '\n', '\t':
			return character
		}
		if character < 0x20 || character == 0x7f ||
			(character >= 0x80 && character <= 0x9f) {
			return -1
		}
		return character
	}, text)
}
