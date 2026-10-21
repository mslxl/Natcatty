package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestSnippetsAndPackagesPersistAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "natcatty.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0).UTC()
	packages := []domainvault.SnippetPackage{
		{Path: "ops/linux", OrderKey: 2000, CreatedAt: when, UpdatedAt: when},
		{Path: "ops", OrderKey: 1000, CreatedAt: when, UpdatedAt: when},
	}
	for _, pkg := range packages {
		if err := first.SaveSnippetPackage(ctx, pkg); err != nil {
			t.Fatalf("SaveSnippetPackage(%s): %v", pkg.Path, err)
		}
	}
	snippets := []domainvault.Snippet{
		{
			ID: "snippet-2", Label: "Disk", Command: "df -h", Package: "ops/linux",
			Targets:      []model.HostID{"host-2"},
			TargetGroups: []string{"prod"},
			NoAutoRun:    true,
			OrderKey:     2000, CreatedAt: when, UpdatedAt: when,
		},
		{
			ID: "snippet-1", Label: "Restart", Command: "systemctl restart nginx",
			Package: "ops", TargetsAllHosts: true,
			MultiLineRunMode: domainvault.SnippetRunLineDelay,
			VariableValues:   map[string]string{"app": "web"},
			OrderKey:         1000, CreatedAt: when, UpdatedAt: when,
		},
	}
	for _, snippet := range snippets {
		if err := first.SaveSnippet(ctx, snippet); err != nil {
			t.Fatalf("SaveSnippet(%s): %v", snippet.ID, err)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	loaded, err := second.ListSnippets(ctx)
	if err != nil {
		t.Fatalf("ListSnippets: %v", err)
	}
	if len(loaded) != 2 || loaded[0].ID != "snippet-1" || loaded[1].ID != "snippet-2" {
		t.Fatalf("snippets = %+v, want order snippet-1 then snippet-2", loaded)
	}
	if !loaded[1].NoAutoRun || !reflect.DeepEqual(loaded[1].Targets, []model.HostID{"host-2"}) ||
		!reflect.DeepEqual(loaded[1].TargetGroups, []string{"prod"}) {
		t.Fatalf("snippet-2 = %+v", loaded[1])
	}
	if !loaded[0].TargetsAllHosts || loaded[0].MultiLineRunMode != domainvault.SnippetRunLineDelay ||
		!reflect.DeepEqual(loaded[0].VariableValues, map[string]string{"app": "web"}) {
		t.Fatalf("snippet-1 = %+v", loaded[0])
	}

	loadedPackages, err := second.ListSnippetPackages(ctx)
	if err != nil {
		t.Fatalf("ListSnippetPackages: %v", err)
	}
	if len(loadedPackages) != 2 || loadedPackages[0].Path != "ops" ||
		loadedPackages[1].Path != "ops/linux" {
		t.Fatalf("packages = %+v, want order ops then ops/linux", loadedPackages)
	}

	loaded[1].Label = "Disk usage"
	if err := second.SaveSnippet(ctx, loaded[1]); err != nil {
		t.Fatalf("update SaveSnippet: %v", err)
	}
	if got, err := second.GetSnippet(ctx, "snippet-2"); err != nil || got.Label != "Disk usage" {
		t.Fatalf("GetSnippet = %+v, %v", got, err)
	}

	if err := second.DeleteSnippet(ctx, "snippet-2"); err != nil {
		t.Fatalf("DeleteSnippet: %v", err)
	}
	if _, err := second.GetSnippet(ctx, "snippet-2"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("deleted Snippet error = %v, want ErrNotFound", err)
	}
	if err := second.DeleteSnippetPackage(ctx, "ops/linux"); err != nil {
		t.Fatalf("DeleteSnippetPackage: %v", err)
	}
	if _, err := second.GetSnippetPackage(ctx, "ops/linux"); !errors.Is(err, appvault.ErrNotFound) {
		t.Fatalf("deleted package error = %v, want ErrNotFound", err)
	}
	if _, err := second.GetSnippetPackage(ctx, "ops"); err != nil {
		t.Fatalf("sibling package was removed: %v", err)
	}
}

func TestDeletingASnippetPackageDoesNotDeleteItsSnippets(t *testing.T) {
	ctx := context.Background()
	store, _ := openTestStore(t)
	when := time.Unix(1700000000, 0).UTC()
	if err := store.SaveSnippetPackage(ctx, domainvault.SnippetPackage{
		Path: "ops", OrderKey: 1000, CreatedAt: when, UpdatedAt: when,
	}); err != nil {
		t.Fatal(err)
	}
	snippet := domainvault.Snippet{
		ID: "snippet-1", Label: "Restart", Command: "systemctl restart nginx",
		Package: "ops", OrderKey: 1000, CreatedAt: when, UpdatedAt: when,
	}
	if err := store.SaveSnippet(ctx, snippet); err != nil {
		t.Fatal(err)
	}

	service := appvault.NewService(store, appvault.WithSnippets(store))
	if err := service.DeleteSnippetPackage(ctx, "ops"); err != nil {
		t.Fatalf("DeleteSnippetPackage: %v", err)
	}
	stored, err := store.GetSnippet(ctx, "snippet-1")
	if err != nil {
		t.Fatalf("the Snippet was deleted with its package: %v", err)
	}
	if stored.Package != "" {
		t.Fatalf("Snippet package = %q, want the Vault root", stored.Package)
	}
}
