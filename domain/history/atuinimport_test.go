package history

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func ptr[T any](value T) *T { return &value }

func TestMapAtuinRowPreservesValidIdentityAndEveryKnownField(t *testing.T) {
	const (
		id      = "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
		session = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f"
	)
	row := AtuinRow{
		ID:         id,
		Timestamp:  time.Date(2026, 3, 1, 9, 0, 0, 123456789, time.UTC).UnixNano(),
		Duration:   ptr(int64(250 * time.Millisecond)),
		Exit:       ptr(int64(7)),
		Command:    "  git status  ",
		CWD:        ptr("/srv/app"),
		Session:    ptr(session),
		Hostname:   ptr("web-1.example.test"),
		Author:     ptr("deploy"),
		AuthorKind: ptr(AuthorKindUser),
		Intent:     ptr("inspect"),
		Shell:      ptr("zsh"),
		DeletedAt:  ptr(time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC).UnixNano()),
	}

	entry, err := MapAtuinRow(row, "ignored:origin")
	if err != nil {
		t.Fatalf("MapAtuinRow: %v", err)
	}
	if entry.ID != id {
		t.Fatalf("ID = %q, want the Atuin UUID", entry.ID)
	}
	if entry.Source != SourceAtuinImport {
		t.Fatalf("source = %q, want %q", entry.Source, SourceAtuinImport)
	}
	if entry.Scope != "remote:web-1.example.test" {
		t.Fatalf("scope = %q", entry.Scope)
	}
	if entry.Origin != "web-1.example.test" {
		t.Fatalf("origin = %q, want the row hostname", entry.Origin)
	}
	if entry.Command != "git status" {
		t.Fatalf("command = %q, want the trimmed command", entry.Command)
	}
	if got, want := entry.RecordedAt, time.Unix(0, row.Timestamp).UTC(); !got.Equal(want) {
		t.Fatalf("recorded at = %v, want %v", got, want)
	}
	if entry.Duration != 250*time.Millisecond {
		t.Fatalf("duration = %v", entry.Duration)
	}
	if !entry.FinishedAt.Equal(entry.RecordedAt.Add(250 * time.Millisecond)) {
		t.Fatalf("finished at = %v, want recorded+duration", entry.FinishedAt)
	}
	if !entry.HasExitCode || entry.ExitCode != 7 {
		t.Fatalf("exit = %d/%t, want 7/true", entry.ExitCode, entry.HasExitCode)
	}
	if entry.CWD != "/srv/app" || entry.SessionUUID != session || entry.Shell != "zsh" {
		t.Fatalf("cwd/session/shell = %q/%q/%q", entry.CWD, entry.SessionUUID, entry.Shell)
	}
	if entry.Author != "deploy" || entry.AuthorKind != AuthorKindUser || entry.Intent != "inspect" {
		t.Fatalf("author/intent = %+v", entry)
	}
	if !entry.DeletedAt.Equal(time.Unix(0, *row.DeletedAt).UTC()) {
		t.Fatalf("deleted at = %v, want the soft-delete timestamp", entry.DeletedAt)
	}
	if entry.ImportLabels != nil || entry.ImportUnknown != nil {
		t.Fatalf("a complete row carried labels: %+v / %+v", entry.ImportLabels, entry.ImportUnknown)
	}
}

func TestMapAtuinRowDerivesStableIdentityAndLabelsUnknowns(t *testing.T) {
	row := AtuinRow{ID: "not-a-uuid", Timestamp: time.Date(2026, 3, 1, 9, 0, 0, 1, time.UTC).UnixNano()}
	row.Command = "ls -la"
	row.Duration = ptr(int64(-5))
	row.Exit = ptr(int64(-99))
	row.Shell = ptr("tcsh")
	row.Session = ptr("not-a-uuid")
	row.Unknown = map[string]string{"deprecated_field": "kept"}

	first, err := MapAtuinRow(row, "example.test:deploy")
	if err != nil {
		t.Fatalf("MapAtuinRow: %v", err)
	}
	second, err := MapAtuinRow(row, "example.test:deploy")
	if err != nil {
		t.Fatalf("MapAtuinRow second: %v", err)
	}
	if !IsUUID(first.ID) || first.ID[14] != '5' {
		t.Fatalf("derived id = %q, want a stable UUIDv5", first.ID)
	}
	if first.ID != second.ID {
		t.Fatalf("derived ids differ: %q vs %q", first.ID, second.ID)
	}
	if first.Scope != "remote:example.test:deploy" || first.Origin != "example.test:deploy" {
		t.Fatalf("origin fallback = %q / %q", first.Scope, first.Origin)
	}
	if first.Duration != 0 || !first.FinishedAt.IsZero() {
		t.Fatalf("a negative duration was invented: %v/%v", first.Duration, first.FinishedAt)
	}
	if first.HasExitCode {
		t.Fatalf("a negative exit was accepted: %+v", first)
	}
	if first.SessionUUID != "" {
		t.Fatalf("a malformed session identity was kept: %q", first.SessionUUID)
	}
	if first.ImportUnknown["deprecated_field"] != "kept" {
		t.Fatalf("unknown column = %+v", first.ImportUnknown)
	}
	want := []string{
		"author:unknown", "authorKind:unknown", "cwd:unknown",
		"duration:unknown", "exit:unknown", "field:deprecated_field",
		"id:derived", "intent:unknown", "origin:derived",
		"session:unknown", "shell:tcsh", "value:unrecognized",
	}
	if !reflect.DeepEqual(first.ImportLabels, want) {
		t.Fatalf("labels = %v, want %v", first.ImportLabels, want)
	}
}

// A known zero duration is a very fast command, not an unknown one: its finish
// time is the recorded time and the row is not labelled duration:unknown.
func TestMapAtuinRowPreservesAKnownZeroDuration(t *testing.T) {
	recorded := time.Date(2026, 3, 1, 9, 0, 0, 999, time.UTC)
	row := AtuinRow{
		ID:        "9b2f3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d",
		Timestamp: recorded.UnixNano(),
		Duration:  ptr(int64(0)),
		Command:   "true",
	}
	entry, err := MapAtuinRow(row, "example.test:me")
	if err != nil {
		t.Fatalf("MapAtuinRow: %v", err)
	}
	if entry.Duration != 0 {
		t.Fatalf("duration = %v, want the known zero", entry.Duration)
	}
	if !entry.FinishedAt.Equal(recorded) {
		t.Fatalf("finished at = %v, want the recorded time", entry.FinishedAt)
	}
	for _, label := range entry.ImportLabels {
		if label == ImportLabelUnknownDuration || label == ImportLabelUnknownValue {
			t.Fatalf("a known zero duration was labelled: %v", entry.ImportLabels)
		}
	}
}

func TestMapAtuinRowFailsClosedRatherThanGuessing(t *testing.T) {
	tests := []struct {
		name string
		row  AtuinRow
	}{
		{name: "empty command", row: AtuinRow{ID: "id", Timestamp: 1, Command: "   "}},
		{name: "no timestamp", row: AtuinRow{ID: "id", Command: "ls"}},
		{name: "negative timestamp", row: AtuinRow{ID: "id", Timestamp: -1, Command: "ls"}},
	}
	for _, test := range tests {
		if _, err := MapAtuinRow(test.row, "example.test:me"); !errors.Is(err, ErrInvalidAtuinRow) {
			t.Errorf("%s: error = %v, want ErrInvalidAtuinRow", test.name, err)
		}
	}
}

func TestCompareImportCheckpointOrdersByTimestampThenIdentity(t *testing.T) {
	early := ImportCheckpoint{Timestamp: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), HistoryID: "b"}
	late := ImportCheckpoint{Timestamp: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), HistoryID: "c"}
	later := ImportCheckpoint{Timestamp: time.Date(2026, 3, 1, 9, 0, 0, 1, time.UTC), HistoryID: "a"}

	if got := CompareImportCheckpoint(early, early); got != 0 {
		t.Fatalf("equal checkpoints compare = %d", got)
	}
	if got := CompareImportCheckpoint(early, late); got >= 0 {
		t.Fatalf("same timestamp, smaller id compares = %d", got)
	}
	if got := CompareImportCheckpoint(late, early); got <= 0 {
		t.Fatalf("same timestamp, larger id compares = %d", got)
	}
	if got := CompareImportCheckpoint(early, later); got >= 0 {
		t.Fatalf("older timestamp compares = %d", got)
	}
	if got := CompareImportCheckpoint(later, early); got <= 0 {
		t.Fatalf("newer timestamp compares = %d", got)
	}

	entry := Entry{ID: "c", RecordedAt: early.Timestamp}
	if !CheckpointAfter(late, entry) {
		t.Fatal("a checkpoint with the same timestamp and a later id does not cover the entry")
	}
	if CheckpointAfter(early, entry) {
		t.Fatal("a checkpoint with the same timestamp and an earlier id covered the entry")
	}
	older := Entry{ID: "z", RecordedAt: early.Timestamp.Add(-time.Nanosecond)}
	if !CheckpointAfter(early, older) {
		t.Fatal("a newer checkpoint does not cover an older entry")
	}
}
