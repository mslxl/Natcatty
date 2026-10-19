package applock

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	domainapplock "github.com/mslxl/natcatty/domain/applock"
)

func TestLoadReturnsDefaultsWhenTheFileIsMissing(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "app-lock-settings.json"))
	settings, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings != domainapplock.DefaultSettings() {
		t.Fatalf("settings = %+v, want the defaults", settings)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	store := Open(filepath.Join(t.TempDir(), "app-lock-settings.json"))
	verifier, err := domainapplock.NewVerifier("s3cret", bytes.NewReader(bytes.Repeat([]byte{5}, domainapplock.SaltBytes)))
	if err != nil {
		t.Fatal(err)
	}
	want := domainapplock.Settings{
		Enabled:          true,
		TimeoutMinutes:   5,
		PasswordVerifier: verifier,
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.Enabled || got.TimeoutMinutes != 5 || got.PasswordVerifier == nil {
		t.Fatalf("settings = %+v, want the saved record", got)
	}
	if !got.PasswordVerifier.Verify("s3cret") {
		t.Error("the loaded verifier rejects the password")
	}
}

func TestSaveNormalizesTheRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	store := Open(path)
	if err := store.Save(context.Background(), domainapplock.Settings{
		Enabled:                       true,
		TimeoutMinutes:                7,
		SystemUnlockEnabled:           true,
		SystemUnlockAutoPromptEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.SystemUnlockEnabled || got.SystemUnlockAutoPromptEnabled {
		t.Fatalf("settings = %+v, want a disabled normalized record", got)
	}
	if got.TimeoutMinutes != domainapplock.DefaultTimeoutMinutes {
		t.Fatalf("timeout = %d, want the default", got.TimeoutMinutes)
	}
}

func TestMalformedJSONLoadsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := Open(path).Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if settings != domainapplock.DefaultSettings() {
		t.Fatalf("settings = %+v, want the defaults", settings)
	}
}

func TestLoadDoesNotTrustAnUnversionedVerifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	record := map[string]any{
		"enabled":        true,
		"timeoutMinutes": 15,
		"passwordVerifier": map[string]any{
			"version":    99,
			"algorithm":  "nope",
			"iterations": 1,
			"salt":       "AAAA",
			"hash":       "AAAA",
		},
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := Open(path).Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.PasswordVerifier != nil {
		t.Fatalf("settings = %+v, want the malformed verifier rejected", settings)
	}
}

func TestSaveIsAtomicAndLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-lock-settings.json")
	store := Open(path)
	if err := store.Save(context.Background(), domainapplock.Settings{TimeoutMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "app-lock-settings.json" {
		t.Fatalf("directory = %v, want only the settings file", names(entries))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Error("the settings file has no trailing newline")
	}
}

func TestSaveUsesOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not report Unix file modes")
	}
	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	store := Open(path)
	if err := store.Save(context.Background(), domainapplock.Settings{TimeoutMinutes: 1}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
}

func TestConcurrentSavesKeepTheFileValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-lock-settings.json")
	store := Open(path)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(minutes int) {
			defer wg.Done()
			if err := store.Save(context.Background(), domainapplock.Settings{TimeoutMinutes: minutes}); err != nil {
				t.Errorf("Save: %v", err)
			}
		}(domainapplock.TimeoutOptions[i%len(domainapplock.TimeoutOptions)])
	}
	wg.Wait()

	if _, err := store.Load(context.Background()); err != nil {
		t.Fatalf("Load after concurrent saves: %v", err)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, len(entries))
	for i, entry := range entries {
		out[i] = entry.Name()
	}
	return out
}
