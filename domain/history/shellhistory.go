package history

import (
	"strings"
	"time"
)

// ShellHistoryCommand is one command parsed from a Shell History File for the
// one-time import. Shell is the file's shell ("zsh", "bash", or "fish").
// RecordedAt is zero when the file format does not store a timestamp.
type ShellHistoryCommand struct {
	Command    string
	Shell      string
	RecordedAt time.Time
}

// ShellHistoryDuplicate reports whether candidate duplicates an existing
// journal row under the one-time Shell History File import rule.
//
// A candidate that carries a timestamp duplicates a row with the same command
// and a timestamp within one second of it. A candidate without a timestamp
// duplicates a row with the same command and scope. The existing row may be
// soft-deleted: an import must never resurrect a command the user deleted.
func ShellHistoryDuplicate(candidate, existing Entry) bool {
	command := strings.TrimSpace(candidate.Command)
	if command == "" || strings.TrimSpace(existing.Command) != command {
		return false
	}
	if candidate.RecordedAt.IsZero() {
		return candidate.Scope != "" && candidate.Scope == existing.Scope
	}
	if existing.RecordedAt.IsZero() {
		return false
	}
	delta := candidate.RecordedAt.Sub(existing.RecordedAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= time.Second
}
