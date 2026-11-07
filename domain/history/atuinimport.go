package history

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ErrInvalidAtuinRow reports a Host Atuin history row that cannot be imported
// without guessing. The whole copy is rejected so the import can retry on a
// later Session instead of silently dropping data.
var ErrInvalidAtuinRow = errors.New("history: invalid Atuin row")

// AtuinRow is one row read from a Host's read-only Atuin history database.
// Nil optional fields mean the column was absent or held NULL, which is
// different from an empty value: the mapping labels the former as unknown.
// Unknown carries columns this build does not understand; they are preserved
// on the entry and labelled instead of being discarded.
type AtuinRow struct {
	ID         string
	Timestamp  int64
	Duration   *int64
	Exit       *int64
	Command    string
	CWD        *string
	Session    *string
	Hostname   *string
	Author     *string
	AuthorKind *string
	Intent     *string
	Shell      *string
	DeletedAt  *int64
	Unknown    map[string]string
}

// Import label values. They describe fields an imported row could not fill so
// the History surface can explain a partial record instead of showing an
// indistinguishable empty value.
const (
	ImportLabelDerivedID         = "id:derived"
	ImportLabelDerivedOrigin     = "origin:derived"
	ImportLabelUnknownCWD        = "cwd:unknown"
	ImportLabelUnknownSession    = "session:unknown"
	ImportLabelUnknownDuration   = "duration:unknown"
	ImportLabelUnknownExit       = "exit:unknown"
	ImportLabelUnknownAuthor     = "author:unknown"
	ImportLabelUnknownAuthorKind = "authorKind:unknown"
	ImportLabelUnknownIntent     = "intent:unknown"
	ImportLabelUnknownShell      = "shell:unknown"
	ImportLabelUnknownValue      = "value:unrecognized"
)

// knownAtuinShells is the shell vocabulary the local journal uses. An Atuin
// row with another value keeps the raw value and is labelled unrecognized.
var knownAtuinShells = map[string]bool{
	"zsh": true, "bash": true, "fish": true, "powershell": true,
	"nu": true, "xonsh": true,
}

// MapAtuinRow maps one Host Atuin row onto the local journal. A valid Atuin
// UUID is preserved; otherwise the row gets a stable derived UUID. hostOrigin
// is the Host's captured host:user origin used when the row has no hostname
// column. The mapping never invents a duration, exit code, or session: those
// stay empty and are labelled unknown.
func MapAtuinRow(row AtuinRow, hostOrigin string) (Entry, error) {
	command := strings.TrimSpace(row.Command)
	if len(command) > MaxCommandLength {
		command = command[:MaxCommandLength]
	}
	if command == "" {
		return Entry{}, fmt.Errorf("%w: empty command", ErrInvalidAtuinRow)
	}
	if row.Timestamp <= 0 {
		return Entry{}, fmt.Errorf("%w: timestamp %d", ErrInvalidAtuinRow, row.Timestamp)
	}

	labels := make([]string, 0, 8)
	origin := ""
	if row.Hostname != nil {
		origin = strings.TrimSpace(*row.Hostname)
	}
	if origin == "" {
		origin = strings.TrimSpace(hostOrigin)
		labels = append(labels, ImportLabelDerivedOrigin)
	}
	if origin == "" {
		origin = "unknown"
	}

	entry := Entry{
		SessionUUID: strings.TrimSpace(stringValue(row.Session)),
		Scope:       "remote:" + origin,
		Command:     command,
		RecordedAt:  time.Unix(0, row.Timestamp).UTC(),
		Origin:      origin,
		Source:      SourceAtuinImport,
	}
	entry.CWD = strings.TrimSpace(stringValue(row.CWD))
	if entry.CWD == "" {
		labels = append(labels, ImportLabelUnknownCWD)
	}
	if entry.SessionUUID == "" {
		labels = append(labels, ImportLabelUnknownSession)
	} else if !IsUUID(entry.SessionUUID) {
		// A malformed session identity is not a UUID this client can join on.
		// Keep it out of the wire field and label the row instead of guessing.
		entry.SessionUUID = ""
		labels = append(labels, ImportLabelUnknownSession, ImportLabelUnknownValue)
	}

	// A known Atuin duration is preserved even when it is zero: a very fast
	// command has a real finish time, not an unknown one. Only a missing or
	// negative duration leaves FinishedAt zero and is labelled unknown.
	if row.Duration != nil && *row.Duration >= 0 {
		entry.Duration = time.Duration(*row.Duration)
		entry.FinishedAt = entry.RecordedAt.Add(entry.Duration)
	} else {
		labels = append(labels, ImportLabelUnknownDuration)
		if row.Duration != nil && *row.Duration < 0 {
			labels = append(labels, ImportLabelUnknownValue)
		}
	}
	if row.Exit != nil && *row.Exit >= 0 {
		entry.ExitCode = int(*row.Exit)
		entry.HasExitCode = true
	} else {
		labels = append(labels, ImportLabelUnknownExit)
		if row.Exit != nil && *row.Exit < -1 {
			labels = append(labels, ImportLabelUnknownValue)
		}
	}

	entry.Author = strings.TrimSpace(stringValue(row.Author))
	if entry.Author == "" {
		labels = append(labels, ImportLabelUnknownAuthor)
	}
	if row.AuthorKind == nil {
		labels = append(labels, ImportLabelUnknownAuthorKind)
	} else {
		entry.AuthorKind = strings.TrimSpace(*row.AuthorKind)
	}
	if row.Intent == nil {
		labels = append(labels, ImportLabelUnknownIntent)
	} else {
		entry.Intent = strings.TrimSpace(*row.Intent)
	}
	if row.Shell == nil {
		labels = append(labels, ImportLabelUnknownShell)
	} else if shell := strings.ToLower(strings.TrimSpace(*row.Shell)); shell == "" {
		labels = append(labels, ImportLabelUnknownShell)
	} else if knownAtuinShells[shell] {
		entry.Shell = shell
	} else {
		entry.Shell = shell
		labels = append(labels, ImportLabelUnknownValue, "shell:"+shell)
	}
	if row.DeletedAt != nil && *row.DeletedAt > 0 {
		entry.DeletedAt = time.Unix(0, *row.DeletedAt).UTC()
	}

	if len(row.Unknown) > 0 {
		entry.ImportUnknown = make(map[string]string, len(row.Unknown))
		for column, value := range row.Unknown {
			entry.ImportUnknown[column] = boundedImportValue(value)
			labels = append(labels, "field:"+column)
		}
	}

	rawID := strings.TrimSpace(row.ID)
	switch {
	case IsUUID(rawID):
		entry.ID = rawID
	case rawID == "":
		entry.ID = DerivedEntryUUID(entry)
		labels = append(labels, ImportLabelDerivedID)
	default:
		// A non-UUID identity cannot be represented on the Atuin wire, but the
		// same row must still map to one journal row across imports.
		entry.ID = DerivedEntryUUID(entry)
		labels = append(labels, ImportLabelDerivedID, ImportLabelUnknownValue)
	}

	entry = NormalizeEntry(entry)
	if !entry.Valid() {
		return Entry{}, fmt.Errorf("%w: row does not form a valid entry", ErrInvalidAtuinRow)
	}
	entry.ImportLabels = normalizeImportLabels(labels)
	return entry, nil
}

// CompareImportCheckpoint orders two (timestamp, history id) checkpoints. It
// reports a negative value when a is older, zero when equal, and a positive
// value when a is newer.
func CompareImportCheckpoint(a, b ImportCheckpoint) int {
	switch {
	case a.Timestamp.Before(b.Timestamp):
		return -1
	case a.Timestamp.After(b.Timestamp):
		return 1
	}
	return strings.Compare(a.HistoryID, b.HistoryID)
}

// CheckpointAfter reports whether checkpoint covers entry.
func CheckpointAfter(checkpoint ImportCheckpoint, entry Entry) bool {
	return CompareImportCheckpoint(checkpoint, ImportCheckpoint{
		Timestamp: entry.RecordedAt,
		HistoryID: entry.ID,
	}) >= 0
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func normalizeImportLabels(labels []string) []string {
	if len(labels) == 0 {
		return nil
	}
	sort.Strings(labels)
	out := labels[:0]
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1] == label {
			continue
		}
		out = append(out, label)
	}
	if len(out) == 0 {
		return nil
	}
	return append([]string(nil), out...)
}

func boundedImportValue(value string) string {
	if len(value) > MaxCommandLength {
		return value[:MaxCommandLength]
	}
	return value
}
