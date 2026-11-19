// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCurrentIdentifier  = "com.example.product"
	testPreviousIdentifier = "com.example.previous"
)

func TestComposeAppendsTheNoteOnTheFirstRelease(t *testing.T) {
	generated := []byte("## What's Changed\n\n* One change by @someone\n")
	fragment := []byte("## Application identifier change\n\nRe-grant permissions.\n")

	got, _, err := compose(generated, fragment, "", testCurrentIdentifier)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	want := string(generated) + "\n" + string(fragment)
	if string(got) != want {
		t.Fatalf("composed body = %q, want %q", got, want)
	}
}

func TestComposeAppendsTheNoteWhenThePreviousIdentifierDiffers(t *testing.T) {
	generated := []byte("notes\n")
	fragment := []byte("migration note\n")

	got, _, err := compose(generated, fragment, testPreviousIdentifier, testCurrentIdentifier)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if !strings.Contains(string(got), string(fragment)) {
		t.Fatalf("composed body %q does not contain the note", got)
	}
}

func TestComposeKeepsGeneratedNotesOnceTheIdentifierIsUnchanged(t *testing.T) {
	generated := []byte("notes\n")
	fragment := []byte("migration note\n")

	got, _, err := compose(generated, fragment, testCurrentIdentifier, testCurrentIdentifier)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if string(got) != string(generated) {
		t.Fatalf("composed body = %q, want the generated notes unchanged", got)
	}
}

func TestComposeDoesNotRepeatANoteAlreadyInTheGeneratedBody(t *testing.T) {
	fragment := []byte("migration note\n")
	generated := []byte("intro\n\n" + string(fragment))

	got, _, err := compose(generated, fragment, "", testCurrentIdentifier)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if string(got) != string(generated) {
		t.Fatalf("composed body = %q, want no duplicate note", got)
	}
}

func TestComposeRejectsAManifestWithoutAnIdentifier(t *testing.T) {
	if _, _, err := compose([]byte("notes\n"), []byte("note\n"), "", ""); err == nil {
		t.Fatal("compose accepted an empty current identifier")
	}
}

func TestComposeRejectsAnEmptyFragment(t *testing.T) {
	if _, _, err := compose([]byte("notes\n"), []byte(" \n"), "", testCurrentIdentifier); err == nil {
		t.Fatal("compose accepted an empty migration note")
	}
}

func TestRunWritesTheComposedBody(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "mygo.json")
	if err := os.WriteFile(manifest, []byte(`{"identifier":"`+testCurrentIdentifier+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(dir, "previous.json")
	if err := os.WriteFile(previous, []byte(`{"identifier":"`+testPreviousIdentifier+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(notes, []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fragment := filepath.Join(dir, "note.md")
	if err := os.WriteFile(fragment, []byte("migration note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{
		"-notes", notes,
		"-out", notes,
		"-manifest", manifest,
		"-previous-manifest", previous,
		"-fragment", fragment,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	body, err := os.ReadFile(notes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "generated") || !strings.Contains(string(body), "migration note") {
		t.Fatalf("output = %q, want the generated notes and the migration note", body)
	}
}

func TestRunToleratesAMissingPreviousManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "mygo.json")
	if err := os.WriteFile(manifest, []byte(`{"identifier":"`+testCurrentIdentifier+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(notes, []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fragment := filepath.Join(dir, "note.md")
	if err := os.WriteFile(fragment, []byte("migration note\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := run([]string{
		"-notes", notes,
		"-out", notes,
		"-manifest", manifest,
		"-previous-manifest", filepath.Join(dir, "absent.json"),
		"-fragment", fragment,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	body, err := os.ReadFile(notes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "migration note") {
		t.Fatalf("output = %q, want a first-release note", body)
	}
}

func TestPreviousIdentifierTreatsUnreadableInputAsUnknown(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"empty.json":     "",
		"truncated.json": `{"identifier":"com.exa`,
		"other.json":     `{"name":"No identifier"}`,
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := previousIdentifier(path); got != "" {
			t.Fatalf("%s: previous identifier = %q, want empty", name, got)
		}
	}
}

func TestAppendNoteAddsOnce(t *testing.T) {
	note := []byte("## Android APK\n\nInstall the APK from the release file.\n")
	got, added, err := appendNote([]byte("generated\n"), note)
	if err != nil {
		t.Fatalf("appendNote: %v", err)
	}
	if !added {
		t.Fatal("appendNote did not report adding the note")
	}
	want := "generated\n\n" + string(note)
	if string(got) != want {
		t.Fatalf("appended body = %q, want %q", got, want)
	}
	again, added, err := appendNote(got, note)
	if err != nil {
		t.Fatalf("appendNote again: %v", err)
	}
	if added {
		t.Fatal("appendNote added an identical note twice")
	}
	if string(again) != string(got) {
		t.Fatalf("second append changed the body: %q", again)
	}
}

func TestAppendNoteRejectsAnEmptyNote(t *testing.T) {
	if _, _, err := appendNote([]byte("generated\n"), []byte(" \n")); err == nil {
		t.Fatal("appendNote accepted an empty note")
	}
}

func TestRunAlwaysAppendsTheRequestedNotes(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "mygo.json")
	if err := os.WriteFile(manifest, []byte(`{"identifier":"`+testCurrentIdentifier+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(notes, []byte("generated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fragment := filepath.Join(dir, "migration.md")
	if err := os.WriteFile(fragment, []byte("migration note\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	android := filepath.Join(dir, "android.md")
	if err := os.WriteFile(android, []byte("Install the APK from a file.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{
		"-notes", notes,
		"-out", notes,
		"-manifest", manifest,
		"-previous-manifest", manifest,
		"-fragment", fragment,
		"-append", android,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	body, err := os.ReadFile(notes)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "generated") || !strings.Contains(text, "Install the APK from a file.") {
		t.Fatalf("output = %q, want the generated notes and the Android note", body)
	}
	if strings.Contains(text, "migration note") {
		t.Fatalf("output = %q, want no migration note once the identifier is unchanged", body)
	}
}
