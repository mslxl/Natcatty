package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/store"
	"github.com/mslxl/natcatty/ui/phoneshell"
)

// TestPhoneSnippetsWriteTheSharedVault drives Snippet and package Add, Edit,
// and Delete through the real phone Vault service and SQLite store. A second
// Shell on the same store is the desktop reload: it lists the phone's changes.
// The Snippet's remembered variable values are stored by the shared service and
// never rendered on the list page.
func TestPhoneSnippetsWriteTheSharedVault(t *testing.T) {
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
	tt := phonePage(t, shell, phoneshell.Vault, "Snippets")

	// Add a package.
	if err := tt.Click("Add package"); err != nil {
		t.Fatalf("click Add package: %v", err)
	}
	if err := tt.Click("Package path"); err != nil {
		t.Fatalf("focus Package path: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Save package"); err != nil {
		t.Fatalf("save package: %v", err)
	}
	packages, err := db.ListSnippetPackages(ctx)
	if err != nil {
		t.Fatalf("list packages: %v", err)
	}
	if len(packages) != 1 || packages[0].Path != "ops" {
		t.Fatalf("stored packages = %+v, want ops", packages)
	}

	// Add a Snippet with a variable, in that package.
	if err := tt.Click("Add snippet"); err != nil {
		t.Fatalf("click Add snippet: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("Phone snippet")
	if err := tt.Click("Command"); err != nil {
		t.Fatalf("focus Command: %v", err)
	}
	tt.Type("echo {{secret:value}}")
	if err := tt.Click("Package"); err != nil {
		t.Fatalf("focus Package: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Save snippet"); err != nil {
		t.Fatalf("save snippet: %v", err)
	}
	snippets, err := db.ListSnippets(ctx)
	if err != nil {
		t.Fatalf("list Snippets: %v", err)
	}
	if len(snippets) != 1 {
		t.Fatalf("stored Snippets = %+v, want one", snippets)
	}
	added := snippets[0]
	if added.Label != "Phone snippet" || added.Package != "ops" ||
		added.Command != "echo {{secret:value}}" {
		t.Fatalf("stored Snippet = %+v, want the phone's draft", added)
	}

	// The shared service substitutes the variable; the phone never implements
	// that rule itself.
	if err := vault.SaveSnippetVariableValues(ctx, added.ID, map[string]string{"secret": "hunter2"}); err != nil {
		t.Fatalf("save variable values: %v", err)
	}
	run, err := vault.PrepareSnippet(ctx, added.ID, map[string]string{"secret": "hunter2"})
	if err != nil {
		t.Fatalf("prepare Snippet: %v", err)
	}
	if run.Command != "echo hunter2" {
		t.Fatalf("prepared command = %q, want the substituted echo hunter2", run.Command)
	}

	// A reload of the same store sees the phone's records, as the desktop does.
	desktop := phoneshell.New(phoneshell.WithVault(vault), phoneshell.WithHosts(vault))
	if err := desktop.Load(ctx); err != nil {
		t.Fatalf("desktop Load: %v", err)
	}
	reloaded := phonePage(t, desktop, phoneshell.Vault, "Snippets")
	for _, want := range []string{"Phone snippet", "ops"} {
		if !reloaded.HasText(want) {
			t.Fatalf("a reloaded Shell does not show %q; texts: %v", want, reloaded.Texts())
		}
	}
	for _, text := range reloaded.Texts() {
		if strings.Contains(text, "hunter2") {
			t.Fatalf("the list rendered a remembered variable value: %q", text)
		}
	}

	// Edit through the first Shell; the shared store receives it.
	if err := tt.Click("Edit snippet Phone snippet"); err != nil {
		t.Fatalf("click Edit snippet: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label in edit: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Save snippet"); err != nil {
		t.Fatalf("save edited Snippet: %v", err)
	}
	snippets, err = db.ListSnippets(ctx)
	if err != nil {
		t.Fatalf("list Snippets after edit: %v", err)
	}
	if len(snippets) != 1 || snippets[0].Label != "Phone snippet v2" {
		t.Fatalf("edited Snippets = %+v, want the new label", snippets)
	}

	// Delete the Snippet and its package through the phone.
	if err := tt.Click("Delete snippet Phone snippet v2"); err != nil {
		t.Fatalf("click Delete snippet: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm Snippet delete: %v", err)
	}
	snippets, err = db.ListSnippets(ctx)
	if err != nil {
		t.Fatalf("list Snippets after delete: %v", err)
	}
	if len(snippets) != 0 {
		t.Fatalf("the deleted Snippet is still in the shared store: %+v", snippets)
	}
	if err := tt.Click("Delete package ops"); err != nil {
		t.Fatalf("click Delete package: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm package delete: %v", err)
	}
	packages, err = db.ListSnippetPackages(ctx)
	if err != nil {
		t.Fatalf("list packages after delete: %v", err)
	}
	if len(packages) != 0 {
		t.Fatalf("the deleted package is still in the shared store: %+v", packages)
	}
}
