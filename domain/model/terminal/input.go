package terminal

// InputCompatibility is the per-Host byte-level translation a Session applies
// to typed Enter and Backspace keys before they reach the transport. It is
// local input planning only: the modes never enter the wire protocol and the
// remote peer cannot observe which one is selected.
//
// The zero value is today's behavior, so a Host that never configured a mode
// sends exactly the bytes it always did.
type InputCompatibility struct {
	Newline     NewlineMode
	Backspace   BackspaceMode
	LineEditing LineEditingMode
}

// IsDefault reports whether the translation leaves every byte unchanged.
func (c InputCompatibility) IsDefault() bool {
	return c.Newline == NewlineCR && c.Backspace == BackspaceDEL &&
		c.LineEditing == LineEditingCharacter
}

// Normalize maps unknown values to the default so a record written by a newer
// version, or a malformed form value, never changes the bytes sent.
func (c InputCompatibility) Normalize() InputCompatibility {
	return InputCompatibility{
		Newline:     NormalizeNewlineMode(c.Newline),
		Backspace:   NormalizeBackspaceMode(c.Backspace),
		LineEditing: NormalizeLineEditingMode(c.LineEditing),
	}
}

// NewlineMode selects the bytes an Enter key contributes. Its values mirror
// the terminal's line-feed/new-line mode: the Return key sends CR when the
// mode is reset and CR LF when it is set.
type NewlineMode string

const (
	// NewlineCR sends a bare carriage return. It is the default and matches
	// an xterm Enter key.
	NewlineCR NewlineMode = ""
	// NewlineLF sends a bare line feed, for peers that treat CR as a literal
	// carriage movement.
	NewlineLF NewlineMode = "lf"
	// NewlineCRLF sends a carriage return followed by a line feed.
	NewlineCRLF NewlineMode = "crlf"
)

// Sequence returns the bytes an Enter key sends.
func (m NewlineMode) Sequence() []byte {
	switch NormalizeNewlineMode(m) {
	case NewlineLF:
		return []byte{'\n'}
	case NewlineCRLF:
		return []byte{'\r', '\n'}
	default:
		return []byte{'\r'}
	}
}

// NormalizeNewlineMode falls back to the bare-CR default.
func NormalizeNewlineMode(m NewlineMode) NewlineMode {
	switch m {
	case NewlineLF, NewlineCRLF:
		return m
	default:
		return NewlineCR
	}
}

// BackspaceMode selects the erase byte a Backspace key sends.
type BackspaceMode string

const (
	// BackspaceDEL sends 0x7F (Delete). It is the default xterm Backspace.
	BackspaceDEL BackspaceMode = ""
	// BackspaceCtrlH sends 0x08 (Control-H), for line editors that bind the
	// erase character to BS rather than DEL.
	BackspaceCtrlH BackspaceMode = "ctrl-h"
)

// Byte returns the erase byte the mode sends.
func (m BackspaceMode) Byte() byte {
	if NormalizeBackspaceMode(m) == BackspaceCtrlH {
		return 0x08
	}
	return 0x7f
}

// NormalizeBackspaceMode falls back to DEL.
func NormalizeBackspaceMode(m BackspaceMode) BackspaceMode {
	if m == BackspaceCtrlH {
		return BackspaceCtrlH
	}
	return BackspaceDEL
}

// LineEditingMode selects how many erase bytes one Backspace sends for the
// character it removes. A character-aware line editor (readline, ZLE) removes
// a whole code point for one erase byte; a byte-oriented editor on an embedded
// or network device removes one wire byte per erase byte.
type LineEditingMode string

const (
	// LineEditingCharacter sends one erase sequence per Backspace key. It is
	// the default and matches today's behavior.
	LineEditingCharacter LineEditingMode = ""
	// LineEditingByte repeats the erase sequence once for every wire byte of
	// the character being removed, so a multibyte character disappears
	// completely on a byte-oriented editor.
	LineEditingByte LineEditingMode = "byte"
)

// NormalizeLineEditingMode falls back to character-oriented deletion.
func NormalizeLineEditingMode(m LineEditingMode) LineEditingMode {
	if m == LineEditingByte {
		return LineEditingByte
	}
	return LineEditingCharacter
}
