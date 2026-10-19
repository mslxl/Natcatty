// Package terminal holds the terminal-behavior settings shared by Session
// logging, OSC 52 clipboard access, and OSC notification delivery. It has no
// transport or UI knowledge; application/session and ui/settings translate
// these values into their own vocabulary.
package terminal

// LogFormat is the file form a Session log takes.
type LogFormat string

const (
	// LogFormatText is a readable text rendering of the terminal screen.
	LogFormatText LogFormat = "txt"
	// LogFormatRaw is the transport's output bytes, unfiltered.
	LogFormatRaw LogFormat = "raw"
	// LogFormatHTML is a readable HTML rendering with SGR styles preserved.
	LogFormatHTML LogFormat = "html"
)

// ClipboardAccess is the OSC 52 policy. It matches the original's four modes:
// programs may never touch the clipboard, may write it, may read and write it,
// or must ask the user before a read.
type ClipboardAccess string

const (
	// ClipboardOff refuses OSC 52 reads and writes.
	ClipboardOff ClipboardAccess = "off"
	// ClipboardWriteOnly lets programs write the clipboard but refuses reads.
	// It is the original default.
	ClipboardWriteOnly ClipboardAccess = "write-only"
	// ClipboardReadWrite lets programs read and write the clipboard.
	ClipboardReadWrite ClipboardAccess = "read-write"
	// ClipboardPrompt lets programs write, and asks the user before a read.
	ClipboardPrompt ClipboardAccess = "prompt"
)

// DropAction is what dropping files on a terminal does.
type DropAction string

const (
	// DropInsert inserts the shell-quoted file paths into the Session's input.
	// It is the default and needs no upload transport.
	DropInsert DropAction = "insert"
	// DropUpload sends the files through the upload seam. Without a seam the
	// drop is refused visibly rather than silently doing nothing.
	DropUpload DropAction = "upload"
)

// Valid reports whether the drop action is one the application knows.
func (action DropAction) Valid() bool {
	return action == DropInsert || action == DropUpload
}

// NotificationMode is when an OSC 9/777/99 notification reaches the desktop.
type NotificationMode string

const (
	// NotificationOff drops program notifications.
	NotificationOff NotificationMode = "off"
	// NotificationUnfocused notifies only while the window or Session is not
	// focused. It is the original's middle setting.
	NotificationUnfocused NotificationMode = "unfocused"
	// NotificationAlways notifies whether or not the window is focused. It is
	// the original default.
	NotificationAlways NotificationMode = "always"
)

// DefaultLogFormat is the original default log format.
const DefaultLogFormat = LogFormatText

// Settings is the persisted terminal behavior group.
type Settings struct {
	// LoggingEnabled starts a log automatically for each new Session when
	// LogDirectory is set. It is off by default, as in the original.
	LoggingEnabled bool
	// LogDirectory is the root log directory. Empty disables automatic logs,
	// as the original skips logging without a directory.
	LogDirectory string
	// LogFormat is the automatic log format.
	LogFormat LogFormat
	// LogTimestamps prefixes non-empty text and HTML lines with a local
	// timestamp.
	LogTimestamps bool
	// OSC52 is the clipboard policy for remote programs.
	OSC52 ClipboardAccess
	// OSCNotifications is the desktop notification mode for OSC 9/777/99.
	OSCNotifications NotificationMode
	// Encoding is the default terminal encoding for a new Session. The
	// original default is UTF-8.
	Encoding Encoding
	// Encodings remembers a chosen encoding by scope, so a Host reopens with
	// the encoding its Session last used. Scope is "local" or "host:<id>".
	Encodings map[string]Encoding
	// PasteProtection asks before pasting multiple lines. It is off by
	// default, as in the original.
	PasteProtection bool
	// PasteMinLines is the line count at which PasteProtection asks. The
	// original default is 2.
	PasteMinLines int
	// DropAction is what dropping files on a terminal does.
	DropAction DropAction
}

// EncodingScopeLocal is the encoding scope of local shell Sessions.
const EncodingScopeLocal = "local"

// EncodingScopeHost returns the encoding scope of a Host's Sessions.
func EncodingScopeHost(hostID string) string {
	if hostID == "" {
		return EncodingScopeLocal
	}
	return "host:" + hostID
}

// Defaults returns the original defaults: logging off with a text format and
// no timestamps, OSC 52 write-only, and notifications always.
func Defaults() Settings {
	return Settings{
		LoggingEnabled:   false,
		LogDirectory:     "",
		LogFormat:        DefaultLogFormat,
		LogTimestamps:    false,
		OSC52:            ClipboardWriteOnly,
		OSCNotifications: NotificationAlways,
		Encoding:         EncodingUTF8,
		Encodings:        nil,
		PasteProtection:  false,
		PasteMinLines:    DefaultPasteMinLines,
		DropAction:       DropInsert,
	}
}

// Normalize validates a stored value, falling back to the original default
// for an unknown enum. It does not trim the directory: a user may store a
// path with meaningful spaces.
func Normalize(settings Settings) Settings {
	settings.LogFormat = normalizeLogFormat(settings.LogFormat)
	if settings.OSC52 == "" {
		settings.OSC52 = ClipboardWriteOnly
	} else if !settings.OSC52.Valid() {
		settings.OSC52 = ClipboardWriteOnly
	}
	if settings.OSCNotifications == "" {
		settings.OSCNotifications = NotificationAlways
	} else if !settings.OSCNotifications.Valid() {
		settings.OSCNotifications = NotificationAlways
	}
	settings.Encoding = NormalizeEncoding(settings.Encoding)
	settings.PasteMinLines = NormalizePasteMinLines(settings.PasteMinLines)
	if !settings.DropAction.Valid() {
		settings.DropAction = DropInsert
	}
	if len(settings.Encodings) > 0 {
		encodings := make(map[string]Encoding, len(settings.Encodings))
		for scope, encoding := range settings.Encodings {
			if scope == "" || !encoding.Valid() {
				continue
			}
			encodings[scope] = encoding
		}
		if len(encodings) == 0 {
			encodings = nil
		}
		settings.Encodings = encodings
	} else {
		settings.Encodings = nil
	}
	return settings
}

func normalizeLogFormat(format LogFormat) LogFormat {
	switch format {
	case LogFormatText, LogFormatRaw, LogFormatHTML:
		return format
	default:
		return DefaultLogFormat
	}
}

// Valid reports whether the clipboard policy is one the application knows.
func (access ClipboardAccess) Valid() bool {
	switch access {
	case ClipboardOff, ClipboardWriteOnly, ClipboardReadWrite, ClipboardPrompt:
		return true
	default:
		return false
	}
}

// WritesAllowed reports whether a program may write the clipboard.
func (access ClipboardAccess) WritesAllowed() bool { return access != ClipboardOff }

// ReadsAllowed reports whether a program may read the clipboard without a
// prompt.
func (access ClipboardAccess) ReadsAllowed() bool { return access == ClipboardReadWrite }

// AsksOnRead reports whether a read waits for the user's decision.
func (access ClipboardAccess) AsksOnRead() bool { return access == ClipboardPrompt }

// Valid reports whether the notification mode is one the application knows.
func (mode NotificationMode) Valid() bool {
	switch mode {
	case NotificationOff, NotificationUnfocused, NotificationAlways:
		return true
	default:
		return false
	}
}
