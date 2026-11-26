package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneNotesWriteTheSharedVault drives Note Group and Note create, edit,
// and delete through the real phone Vault service and SQLite store. A second
// Shell on the same service is the desktop reload: it lists the phone's
// changes, including the Note Group the phone created.
func TestPhoneNotesWriteTheSharedVault(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "natcatty.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	vault := newPhoneVault(db, nil)
	shell := phoneshell.New(phoneshell.WithVault(vault), phoneshell.WithHosts(vault))
	if err := shell.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tt := phonePage(t, shell, phoneshell.Vault, "Notes")

	// Create a Note Group through the phone.
	if err := tt.Click("Add group"); err != nil {
		t.Fatalf("click Add group: %v", err)
	}
	if err := tt.Click("Group path"); err != nil {
		t.Fatalf("focus Group path: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Save group"); err != nil {
		t.Fatalf("save the Note Group: %v", err)
	}
	groups, err := db.ListNoteGroups(ctx)
	if err != nil {
		t.Fatalf("list Note Groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Path != "ops" {
		t.Fatalf("stored Note Groups = %+v, want ops", groups)
	}

	// Create a Note in that group.
	if err := tt.Click("Add note"); err != nil {
		t.Fatalf("click Add note: %v", err)
	}
	if err := tt.Click("Title"); err != nil {
		t.Fatalf("focus Title: %v", err)
	}
	tt.Type("Runbook")
	if err := tt.Click("Group"); err != nil {
		t.Fatalf("focus Group: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Content"); err != nil {
		t.Fatalf("focus Content: %v", err)
	}
	tt.Type("# Restart\nrun the service")
	if err := tt.Click("Save note"); err != nil {
		t.Fatalf("save the Note: %v", err)
	}
	notes, err := db.ListNotes(ctx)
	if err != nil {
		t.Fatalf("list Notes: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("stored Notes = %+v, want one", notes)
	}
	added := notes[0]
	if added.Title != "Runbook" || added.Group != "ops" ||
		!strings.Contains(added.Content, "run the service") {
		t.Fatalf("stored Note = %+v, want the phone's draft", added)
	}

	// A reload of the same store sees the phone's records, as the desktop does.
	desktop := phoneshell.New(phoneshell.WithVault(vault), phoneshell.WithHosts(vault))
	if err := desktop.Load(ctx); err != nil {
		t.Fatalf("desktop Load: %v", err)
	}
	reloaded := phonePage(t, desktop, phoneshell.Vault, "Notes")
	for _, want := range []string{"ops", "Runbook"} {
		if !reloaded.HasText(want) {
			t.Fatalf("a reloaded Shell does not show %q; texts: %v", want, reloaded.Texts())
		}
	}

	// Edit the Note through the first Shell; the shared store receives it.
	if err := tt.Click("Edit note Runbook"); err != nil {
		t.Fatalf("click Edit note: %v", err)
	}
	if err := tt.Click("Content"); err != nil {
		t.Fatalf("focus Content in edit: %v", err)
	}
	tt.Type(" and check logs")
	if err := tt.Click("Save note"); err != nil {
		t.Fatalf("save the edited Note: %v", err)
	}
	notes, err = db.ListNotes(ctx)
	if err != nil {
		t.Fatalf("list Notes after edit: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Content, "and check logs") {
		t.Fatalf("edited Notes = %+v, want the new content", notes)
	}

	// Delete the Note, then its group; the store reflects both.
	if err := tt.Click("Delete note Runbook"); err != nil {
		t.Fatalf("click Delete note: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm Note delete: %v", err)
	}
	notes, err = db.ListNotes(ctx)
	if err != nil {
		t.Fatalf("list Notes after delete: %v", err)
	}
	if len(notes) != 0 {
		t.Fatalf("the deleted Note is still in the shared store: %+v", notes)
	}
	if err := tt.Click("Delete group ops"); err != nil {
		t.Fatalf("click Delete group: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm group delete: %v", err)
	}
	groups, err = db.ListNoteGroups(ctx)
	if err != nil {
		t.Fatalf("list Note Groups after delete: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("the deleted Note Group is still in the shared store: %+v", groups)
	}
}
