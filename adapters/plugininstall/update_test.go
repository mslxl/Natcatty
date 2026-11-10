package plugininstall_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/plugininstall"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

func versionedPackage(version, hash string) appplugin.Package {
	pkg := testPackage()
	pkg.Manifest.Version = version
	pkg.SHA256 = hash
	return pkg
}

func installVersion(t *testing.T, store *plugininstall.Store, pkg appplugin.Package,
	trustedSource string) domainplugin.InstallRecord {
	t.Helper()
	record, err := store.Install(context.Background(), pkg, appplugin.InstallOptions{
		Source: domainplugin.SourceRegistry, TrustedSource: trustedSource,
	}, time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Install %s: %v", pkg.Manifest.Version, err)
	}
	return record
}

// nextRecord is the metadata the manager hands to PromoteVersion for the
// package being installed.
func nextRecord(record domainplugin.InstallRecord, version, hash string) domainplugin.InstallRecord {
	next := record
	next.Version = version
	next.PackageSHA256 = hash
	return next
}

func TestTrustPinsAndPreferencesPersistAcrossStoreInstances(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	ctx := context.Background()

	if _, ok, err := store.SourcePin(ctx, "acme.notes"); err != nil || ok {
		t.Fatalf("empty trust store = ok %v, err %v", ok, err)
	}
	pin := domainplugin.SourcePin{
		ID: "acme.notes", Source: "https://plugins.example",
		FirstSeen: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}
	if err := store.SaveSourcePin(ctx, pin); err != nil {
		t.Fatalf("SaveSourcePin: %v", err)
	}
	preferences, err := store.RegistryPreferences(ctx)
	if err != nil || preferences.AutomaticUpdates {
		t.Fatalf("default preferences = %+v, %v; automatic updates must be off", preferences, err)
	}
	preferences.AutomaticUpdates = true
	preferences.SetChannel("acme.notes", domainplugin.ChannelBeta)
	if err := store.SaveRegistryPreferences(ctx, preferences); err != nil {
		t.Fatalf("SaveRegistryPreferences: %v", err)
	}

	reopened := plugininstall.Open(userData)
	loaded, ok, err := reopened.SourcePin(ctx, "acme.notes")
	if err != nil || !ok || loaded.Source != pin.Source || !loaded.FirstSeen.Equal(pin.FirstSeen) {
		t.Fatalf("persisted pin = %+v, ok %v, err %v", loaded, ok, err)
	}
	loadedPreferences, err := reopened.RegistryPreferences(ctx)
	if err != nil || !loadedPreferences.AutomaticUpdates ||
		loadedPreferences.ChannelFor("acme.notes") != domainplugin.ChannelBeta {
		t.Fatalf("persisted preferences = %+v, %v", loadedPreferences, err)
	}
	if loadedPreferences.ChannelFor("acme.other") != domainplugin.ChannelStable {
		t.Fatal("an unopted plugin was not stable by default")
	}
}

func TestPromoteKeepsOnePreviousVersionAndRemovesOlderOnes(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	ctx := context.Background()
	record := installVersion(t, store, versionedPackage("1.2.3", "hash-1"), "registry-one")

	record.Enabled = true
	record.Grants = []domainplugin.PermissionGrant{{
		Permission: domainplugin.PermissionCommands, GrantedAt: time.Now().UTC(),
	}}
	promoted, err := store.PromoteVersion(ctx, versionedPackage("1.3.0", "hash-2"),
		nextRecord(record, "1.3.0", "hash-2"), time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PromoteVersion: %v", err)
	}
	if promoted.Version != "1.3.0" || promoted.Previous == nil ||
		promoted.Previous.Version != "1.2.3" || !promoted.Enabled {
		t.Fatalf("promoted record = %+v", promoted)
	}
	if promoted.TrustedSource != "registry-one" {
		t.Fatalf("trusted source = %q", promoted.TrustedSource)
	}
	for _, version := range []string{"1.2.3", "1.3.0"} {
		if _, err := os.Stat(filepath.Join(userData, "plugins", "acme.notes", version)); err != nil {
			t.Fatalf("version directory %s: %v", version, err)
		}
	}

	promoted, err = store.PromoteVersion(ctx, versionedPackage("1.4.0", "hash-3"),
		nextRecord(promoted, "1.4.0", "hash-3"), time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second PromoteVersion: %v", err)
	}
	if promoted.Previous.Version != "1.3.0" {
		t.Fatalf("previous after second update = %+v", promoted.Previous)
	}
	if _, err := os.Stat(filepath.Join(userData, "plugins", "acme.notes", "1.2.3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("older version was not pruned: %v", err)
	}
}

func TestRestorePreviousSwapsVersionsAndIsReversible(t *testing.T) {
	store := plugininstall.Open(t.TempDir())
	ctx := context.Background()
	record := installVersion(t, store, versionedPackage("1.2.3", "hash-1"), "registry-one")
	record.Enabled = true
	promoted, err := store.PromoteVersion(ctx, versionedPackage("1.3.0", "hash-2"),
		nextRecord(record, "1.3.0", "hash-2"), time.Now().UTC())
	if err != nil {
		t.Fatalf("PromoteVersion: %v", err)
	}
	if _, err := store.PackageDir(ctx, promoted.ID); err != nil {
		t.Fatalf("PackageDir after update: %v", err)
	}

	restored, err := store.RestorePrevious(ctx, promoted.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("RestorePrevious: %v", err)
	}
	if restored.Version != "1.2.3" || restored.PackageSHA256 != "hash-1" {
		t.Fatalf("restored = %+v", restored)
	}
	if restored.Previous == nil || restored.Previous.Version != "1.3.0" {
		t.Fatalf("previous after restore = %+v", restored.Previous)
	}
	dir, err := store.PackageDir(ctx, restored.ID)
	if err != nil || filepath.Base(dir) != "1.2.3" {
		t.Fatalf("PackageDir = %q, %v", dir, err)
	}

	back, err := store.RestorePrevious(ctx, restored.ID, time.Now().UTC())
	if err != nil {
		t.Fatalf("second RestorePrevious: %v", err)
	}
	if back.Version != "1.3.0" {
		t.Fatalf("second restore version = %s, want 1.3.0", back.Version)
	}
}

func TestPromoteVersionFailureLeavesTheInstalledVersionIntact(t *testing.T) {
	userData := t.TempDir()
	store := plugininstall.Open(userData)
	ctx := context.Background()
	record := installVersion(t, store, versionedPackage("1.2.3", "hash-1"), "registry-one")

	parent := filepath.Join(userData, "plugins", "acme.notes")
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if _, err := store.PromoteVersion(ctx, versionedPackage("1.3.0", "hash-2"),
		nextRecord(record, "1.3.0", "hash-2"), time.Now().UTC()); err == nil {
		t.Fatal("PromoteVersion succeeded with an unwritable plugin directory")
	}
	stored, ok, err := store.Record(ctx, "acme.notes")
	if err != nil || !ok {
		t.Fatalf("Record after failed update: ok %v, err %v", ok, err)
	}
	if stored.Version != "1.2.3" || stored.Previous != nil {
		t.Fatalf("record changed by failed update: %+v", stored)
	}
	if _, err := os.Stat(filepath.Join(parent, "1.2.3", "plugin.toml")); err != nil {
		t.Fatalf("previous version files are gone: %v", err)
	}
}
