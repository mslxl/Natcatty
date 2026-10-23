// Package transfer holds the file-browser domain: directory entries, their
// ordering and visibility rules, permission-bit parsing, bookmarks, and the
// shape of file operations. It has no transport, filesystem, or UI knowledge;
// adapters list real directories and application services run operations.
package transfer

import (
	"fmt"
	"strings"
	"time"
)

// Kind is an entry's resolved type: a symbolic link is reported as the kind
// it points at when the target is known, with Symlink set.
type Kind string

const (
	// KindFile is a regular file.
	KindFile Kind = "file"
	// KindDir is a directory.
	KindDir Kind = "dir"
	// KindLink is a symlink whose target could not be resolved.
	KindLink Kind = "link"
	// KindOther is a device, socket, FIFO, or unknown type.
	KindOther Kind = "other"
)

// IsDir reports whether the entry opens as a directory.
func (k Kind) IsDir() bool { return k == KindDir }

// Entry is one directory listing row. Path is the entry's absolute path in
// its own filesystem's notation. Hidden is the platform's hidden attribute
// (Windows); a dotfile is hidden by Name regardless.
type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       Kind      `json:"kind"`
	Symlink    bool      `json:"symlink,omitempty"`
	LinkTarget string    `json:"linkTarget,omitempty"`
	Size       int64     `json:"size"`
	Mode       uint32    `json:"mode"`
	ModTime    time.Time `json:"modified"`
	Owner      string    `json:"owner,omitempty"`
	Group      string    `json:"group,omitempty"`
	Hidden     bool      `json:"hidden,omitempty"`
}

// IsDir reports whether the entry opens as a directory.
func (e Entry) IsDir() bool { return e.Kind.IsDir() }

// Navigable reports whether activating the entry changes the directory. A
// symlink to a directory is navigable; a dangling symlink is not.
func (e Entry) Navigable() bool { return e.Kind == KindDir }

// HiddenName reports whether a name follows the dotfile convention. The
// parent entry ".." is never hidden, matching the original browser.
func HiddenName(name string) bool {
	return name != ".." && strings.HasPrefix(name, ".")
}

// Visible reports whether an entry is shown at the current hidden-file
// setting. A Windows hidden attribute hides it too; ".." is always shown.
func Visible(entry Entry, showHidden bool) bool {
	if showHidden || entry.Name == ".." {
		return true
	}
	return !entry.Hidden && !HiddenName(entry.Name)
}

// FilterVisible returns the entries shown at the current hidden-file setting.
func FilterVisible(entries []Entry, showHidden bool) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if Visible(entry, showHidden) {
			out = append(out, entry)
		}
	}
	return out
}

// FormatSize renders a byte count with binary units, as the original's
// Size column does. The value is display string data, so it is language
// neutral.
func FormatSize(size int64) string {
	if size < 0 {
		size = 0
	}
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"KB", "MB", "GB", "TB", "PB", "EB"}[exp]
	value := float64(size) / float64(div)
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, suffix)
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
