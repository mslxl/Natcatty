package transfer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// UNIX permission bits carried in Entry.Mode. The low nine bits are the
// usual owner/group/other permissions; the three high bits are setuid,
// setgid, and sticky, matching the wire representation SFTP uses.
const (
	// ModeSetuid marks an owned executable that runs with the owner's id.
	ModeSetuid uint32 = 1 << 11
	// ModeSetgid marks an executable that runs with the group's id.
	ModeSetgid uint32 = 1 << 10
	// ModeSticky is the restricted-deletion bit on directories.
	ModeSticky uint32 = 1 << 9

	// MaxMode is the largest value the permission dialog accepts.
	MaxMode = ModeSetuid | ModeSetgid | ModeSticky | 0o777
)

// ErrInvalidMode reports a permission string that is not an octal mode
// between 0000 and 7777.
var ErrInvalidMode = errors.New("sftp: invalid permission mode")

// FormatMode renders permission bits the way ls does, for example
// "drwxr-xr-x" or "-rw-r--r--". A symlink that could not be resolved gets
// the "l" prefix.
func FormatMode(entry Entry) string {
	prefix := byte('-')
	switch {
	case entry.Kind == KindDir:
		prefix = 'd'
	case entry.Symlink && entry.Kind != KindDir:
		prefix = 'l'
	case entry.Kind == KindOther:
		prefix = '?'
	}
	return string(prefix) + FormatPermissions(entry.Mode)
}

// FormatPermissions renders the nine permission characters plus the
// setuid/setgid/sticky substitutions.
func FormatPermissions(mode uint32) string {
	var b strings.Builder
	b.Grow(9)
	const chars = "rwx"
	for i := 0; i < 9; i++ {
		bit := uint32(1) << (8 - i)
		if mode&bit == 0 {
			b.WriteByte('-')
			continue
		}
		b.WriteByte(chars[i%3])
	}
	out := []byte(b.String())
	// The special bits replace the execute position when set, as ls does.
	if mode&ModeSetuid != 0 {
		if out[2] == 'x' {
			out[2] = 's'
		} else {
			out[2] = 'S'
		}
	}
	if mode&ModeSetgid != 0 {
		if out[5] == 'x' {
			out[5] = 's'
		} else {
			out[5] = 'S'
		}
	}
	if mode&ModeSticky != 0 {
		if out[8] == 'x' {
			out[8] = 't'
		} else {
			out[8] = 'T'
		}
	}
	return string(out)
}

// FormatOctal renders a mode's permission and special bits as four octal
// digits, which is what the permission dialog edits.
func FormatOctal(mode uint32) string {
	return fmt.Sprintf("%04o", mode&MaxMode)
}

// ParseMode parses the permission dialog's value. It accepts three or four
// octal digits, an optional leading "0o", and a symbolic form such as
// "rwxr-xr-x". Anything else, or a value above 7777, is ErrInvalidMode.
func ParseMode(text string) (uint32, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, ErrInvalidMode
	}
	if len(text) == 9 || len(text) == 10 {
		if mode, ok := parseSymbolic(text); ok {
			return mode, nil
		}
	}
	digits := strings.TrimPrefix(strings.TrimPrefix(text, "0o"), "0O")
	if digits == "" {
		return 0, ErrInvalidMode
	}
	for _, r := range digits {
		if r < '0' || r > '7' {
			return 0, ErrInvalidMode
		}
	}
	value, err := strconv.ParseUint(digits, 8, 32)
	if err != nil || value > uint64(MaxMode) {
		return 0, ErrInvalidMode
	}
	return uint32(value), nil
}

// parseSymbolic parses "rwxr-xr-x" or "drwxr-xr-x"; the type character, when
// present, is ignored because it is not editable.
func parseSymbolic(text string) (uint32, bool) {
	if len(text) == 10 {
		text = text[1:]
	}
	if len(text) != 9 {
		return 0, false
	}
	var mode uint32
	for i := 0; i < 9; i++ {
		bit := uint32(1) << (8 - i)
		switch text[i] {
		case 'r', 'w':
			mode |= bit
		case 'x':
			mode |= bit
		case 's':
			mode |= bit
			if i == 2 {
				mode |= ModeSetuid
			} else if i == 5 {
				mode |= ModeSetgid
			}
		case 't':
			mode |= bit
			mode |= ModeSticky
		case 'S':
			if i == 2 {
				mode |= ModeSetuid
			} else if i == 5 {
				mode |= ModeSetgid
			}
		case 'T':
			mode |= ModeSticky
		case '-':
		default:
			return 0, false
		}
	}
	return mode, true
}
