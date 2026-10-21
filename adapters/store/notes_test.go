package store

import (
	"context"
	"path/filepath"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestNotesMigrationCreatesTablesWithoutForeignKeys(t *testing.T) {
	store, _ := openTestStore(t)
	ctx := context.Background()

	if latestMigrationVersion() < 9 {
		t.Fatalf("latest migration = %d, want at least 9", latestMigrationVersion())
	}
	for _, table := range []string{"vault_notes", "note_groups"} {
		rows, err := store.db.Query(ctx, `PRAGMA foreign_key_list(`+table+`)`)
		if err != nil {
			t.Fatalf("foreign_key_list(%s): %v", table, err)
		}
		if len(rows.Values) != 0 {
			t.Fatalf("%s has foreign keys: %+v", table, rows.Values)
		}
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != latestMigrationVersion() {
		t.Fatalf("schema version = %d, want %d", version, latestMigrationVersion())
	}
}

func TestNotesPersistThroughTheRealStoreAndSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "natcatty.db")
	ctx := context.Background()
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	service := appvault.NewService(first,
		appvault.WithNotes(first),
		appvault.WithNoteIDGenerator(func() string { return "note-1" }),
	)
	if _, err := service.CreateNoteGroup(ctx, domainvault.NoteGroupDraft{Path: "ops/db"}); err != nil {
		t.Fatalf("CreateNoteGroup: %v", err)
	}
	note, err := service.CreateNote(ctx, domainvault.NoteDraft{
		Title: "Backup", Content: "# Backup\n\n`pg_dump`", Group: "ops/db",
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	reopened := appvault.NewService(second, appvault.WithNotes(second))
	notes, err := reopened.ListNotes(ctx)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != note.ID || notes[0].Content != note.Content ||
		notes[0].Group != "ops/db" {
		t.Fatalf("notes after reopen = %+v", notes)
	}
	groups, err := reopened.ListNoteGroups(ctx)
	if err != nil {
		t.Fatalf("ListNoteGroups: %v", err)
	}
	if len(groups) != 2 || groups[0].Path != "ops" || groups[1].Path != "ops/db" {
		t.Fatalf("groups after reopen = %+v", groups)
	}

	// Deleting the group removes only the group records; the Note stays and
	// moves to the root, with no foreign-key violation.
	if err := reopened.DeleteNoteGroup(ctx, "ops"); err != nil {
		t.Fatalf("DeleteNoteGroup: %v", err)
	}
	notes, err = reopened.ListNotes(ctx)
	if err != nil {
		t.Fatalf("ListNotes after group delete: %v", err)
	}
	if len(notes) != 1 || notes[0].ID != note.ID || notes[0].Group != "" {
		t.Fatalf("notes after group delete = %+v", notes)
	}
}
