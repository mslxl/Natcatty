package transfer

import (
	"fmt"
	"time"
)

// ExistingKind classifies the entry that already occupies a destination. The
// values are stable tokens shared with the UI and persistence; unknown fails
// closed because a replacement must never destroy data whose type it cannot
// establish.
type ExistingKind string

const (
	// ExistingFile is a regular file.
	ExistingFile ExistingKind = "file"
	// ExistingDirectory is a real directory.
	ExistingDirectory ExistingKind = "directory"
	// ExistingSymlink is a symbolic link, whatever its target resolves to.
	ExistingSymlink ExistingKind = "symlink"
	// ExistingUnknown is a destination whose type could not be established.
	ExistingUnknown ExistingKind = "unknown"
)

// Conflict describes one destination that already exists when a top-level
// source is about to be written. It carries everything the prompt shows and
// the type key apply-to-all groups by.
type Conflict struct {
	SourcePath string `json:"sourcePath"`
	TargetPath string `json:"targetPath"`
	Name       string `json:"name"`
	// IsDirectory says the incoming entry is a directory.
	IsDirectory  bool         `json:"isDirectory"`
	ExistingKind ExistingKind `json:"existingKind"`
	ExistingSize int64        `json:"existingSize"`
	// ExistingModified is the existing entry's modification time. A zero value
	// means the adapter did not provide one.
	ExistingModified time.Time `json:"existingModified,omitempty"`
	NewSize          int64     `json:"newSize"`
	NewModified      time.Time `json:"newModified,omitempty"`
	// Remaining is how many top-level sources in this request still need a
	// conflict decision after this one. Apply-to-all uses it for its count.
	Remaining int `json:"remaining,omitempty"`
	// ApplyToAllCount is the number of conflicts one answer would cover,
	// including this one. The queue fills it from the live batch.
	ApplyToAllCount int `json:"applyToAllCount,omitempty"`
}

// TypeKey groups conflicts that one apply-to-all answer may cover: the same
// incoming shape against the same existing shape.
func (c Conflict) TypeKey() string {
	return ConflictTypeKey(c.IsDirectory, c.ExistingKind)
}

// ConflictTypeKey builds the apply-to-all key for one incoming shape against
// one existing shape.
func ConflictTypeKey(isDirectory bool, existing ExistingKind) string {
	incoming := "file"
	if isDirectory {
		incoming = "directory"
	}
	if existing == "" {
		existing = ExistingUnknown
	}
	return fmt.Sprintf("%s:%s", incoming, existing)
}

// CanReplace reports whether replacement is safe for this pair. A symlink may
// be replaced by either incoming shape because the link itself is moved aside;
// an unknown type is refused so a folder of destination-only data cannot be
// destroyed by a guess.
func (c Conflict) CanReplace() bool {
	switch c.ExistingKind {
	case ExistingSymlink:
		return true
	case ExistingFile:
		return !c.IsDirectory
	case ExistingDirectory:
		return c.IsDirectory
	default:
		return false
	}
}

// CanMerge reports whether the incoming directory can be merged into the
// existing one. A merge keeps destination-only entries, so it never applies
// to a file or an unresolved link.
func (c Conflict) CanMerge() bool {
	return c.IsDirectory && c.ExistingKind == ExistingDirectory
}

// ExistingKindOf classifies a destination entry. A symlink keeps its link
// identity even when Stat resolved its target for navigation.
func ExistingKindOf(entry Entry) ExistingKind {
	if entry.Symlink {
		return ExistingSymlink
	}
	switch entry.Kind {
	case KindDir:
		return ExistingDirectory
	case KindFile:
		return ExistingFile
	default:
		return ExistingUnknown
	}
}

// NormalizeMtimeSeconds reduces a millisecond timestamp to the whole seconds
// SFTP reports. A missing or non-positive timestamp becomes 0, which never
// matches: absence of metadata must not read as "unchanged".
func NormalizeMtimeSeconds(unixMilli int64) int64 {
	if unixMilli <= 0 {
		return 0
	}
	return unixMilli / 1000
}

// UnchangedTransfer reports whether a file already at the destination is the
// same size and has the same whole-second modification time as its source.
// This is the original rsync-style generator check: SFTP mtimes are commonly
// second-precision, so a local millisecond timestamp must not defeat an
// otherwise identical remote. A missing mtime or a negative size is unknown,
// not unchanged.
func UnchangedTransfer(sourceSize int64, sourceModified time.Time,
	targetSize int64, targetModified time.Time) bool {
	if sourceSize < 0 || targetSize < 0 || sourceSize != targetSize {
		return false
	}
	sourceSeconds := NormalizeMtimeSeconds(sourceModified.UnixMilli())
	targetSeconds := NormalizeMtimeSeconds(targetModified.UnixMilli())
	if sourceSeconds <= 0 || targetSeconds <= 0 {
		return false
	}
	return sourceSeconds == targetSeconds
}
