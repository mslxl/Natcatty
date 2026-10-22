package vault

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestFamilyDocumentsAreDetectedAndPreviewedThroughTheProductionPath ensures
// the Import screen can accept the three family documents without a format
// override.
func TestFamilyDocumentsAreDetectedAndPreviewedThroughTheProductionPath(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	cases := []struct {
		format ExportFormat
		want   string
		family func(ImportPreview) bool
	}{
		{ExportSnippets, "snippets", func(p ImportPreview) bool { return p.FamilyCounts.Snippets == 2 }},
		{ExportKeys, "keys", func(p ImportPreview) bool { return p.FamilyCounts.Keys == 1 }},
		{ExportNotesZIP, "notes_zip", func(p ImportPreview) bool { return p.FamilyCounts.Notes == 3 }},
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			exported, err := source.service.ExportVault(ctx, ExportOptions{
				Format: tc.format, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
			})
			if err != nil {
				t.Fatal(err)
			}
			destination := newDestination(t)
			preview, err := destination.service.PreviewImport(ctx, string(exported.Content), PreviewImportOptions{})
			if err != nil {
				t.Fatalf("PreviewImport: %v", err)
			}
			if string(preview.Format) != tc.want {
				t.Fatalf("detected format = %q, want %q", preview.Format, tc.want)
			}
			if !tc.family(preview) {
				t.Fatalf("family counts = %+v", preview.FamilyCounts)
			}
			if _, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{}); err != nil {
				t.Fatalf("CommitImport: %v", err)
			}
		})
	}
}

// TestExportReportsOmittedCredentialsWithoutIncludeSecrets covers the
// non-destructive default: the UI receives a count of what was left out, and
// none of the plaintext appears in the file.
func TestExportReportsOmittedCredentialsWithoutIncludeSecrets(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Host password, Key (material and passphrase count once), Identity
	// password, and Proxy Profile password.
	if exported.OmittedSecrets != 4 {
		t.Fatalf("OmittedSecrets = %d, want 4", exported.OmittedSecrets)
	}
	withSecrets, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if withSecrets.OmittedSecrets != 0 {
		t.Fatalf("OmittedSecrets with IncludeSecrets = %d", withSecrets.OmittedSecrets)
	}
}

// TestOmittedSecretsAreScopedToTheExportedFamilies keeps the omission count
// honest: a Notes or Themes document carries no credential, so it must not
// report Host or Identity secrets it never could have written.
func TestOmittedSecretsAreScopedToTheExportedFamilies(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	cases := []struct {
		format ExportFormat
		want   int
	}{
		{ExportJSON, 4},     // Host, Key, Identity, Proxy Profile
		{ExportCSV, 2},      // Host password and the referenced Key passphrase
		{ExportKeys, 1},     // Key material/passphrase
		{ExportSnippets, 0}, // no credential field
		{ExportNotesZIP, 0}, // no credential field
		{ExportThemes, 0},   // no credential field
	}
	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			exported, err := source.service.ExportVault(ctx, ExportOptions{
				Format: tc.format, ExportedAt: fixedFamilyTime(),
			})
			if err != nil {
				t.Fatalf("ExportVault: %v", err)
			}
			if exported.OmittedSecrets != tc.want {
				t.Fatalf("OmittedSecrets = %d, want %d", exported.OmittedSecrets, tc.want)
			}
		})
	}
}

// TestEmptyZIPNamedArchivePreviewsAsNotes covers the ".zip" half of format
// detection: an empty archive has no entry to sniff.
func TestEmptyZIPNamedArchivePreviewsAsNotes(t *testing.T) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(context.Background(), buffer.String(),
		PreviewImportOptions{FileName: "empty.zip"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.Format != "notes_zip" {
		t.Fatalf("detected format = %q", preview.Format)
	}
}

// TestFamilyHostGroupsAreCreatedWithoutImportedHosts covers a Vault JSON whose
// Host rows are skipped: the document's Host Groups are still part of the
// restore, as the preview advertises.
func TestFamilyHostGroupsAreCreatedWithoutImportedHosts(t *testing.T) {
	ctx := context.Background()
	document := `{"kind":"netcatty.vault","version":1,` +
		`"groups":[{"path":"Solo"},{"path":"Solo/Child"}],"hosts":[]}`
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, document,
		PreviewImportOptions{FileName: "netcatty-vault.json"})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.FamilyCounts.Groups != 2 {
		t.Fatalf("preview groups = %+v", preview.FamilyCounts)
	}
	if _, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{}); err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if !containsGroup(destination.groups.items, "Solo") ||
		!containsGroup(destination.groups.items, "Solo/Child") {
		t.Fatalf("Host Groups = %+v", destination.groups.items)
	}
}

// TestNotesZIPSuffixCollisionKeepsEveryNote covers the collision between a
// duplicate title's generated suffix and a real title: "A", "A", "A-2" must
// produce three distinct entries and three restored titles.
func TestNotesZIPSuffixCollisionKeepsEveryNote(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	source.notes.notes = []domainvault.Note{
		{ID: "note-a1", Title: "A", Content: "one", OrderKey: 1000},
		{ID: "note-a2", Title: "A", Content: "two", OrderKey: 2000},
		{ID: "note-a3", Title: "A-2", Content: "three", OrderKey: 3000},
	}
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportNotesZIP, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	if _, err := destination.service.ImportNotesZIP(ctx, exported.Content); err != nil {
		t.Fatalf("ImportNotesZIP: %v", err)
	}
	if len(destination.notes.notes) != 3 {
		t.Fatalf("Notes = %+v", destination.notes.notes)
	}
	titles := map[string]int{}
	contents := map[string]bool{}
	for _, note := range destination.notes.notes {
		titles[note.Title]++
		contents[note.Content] = true
	}
	if titles["A"] != 2 || titles["A-2"] != 1 {
		t.Fatalf("titles = %v", titles)
	}
	for _, want := range []string{"one", "two", "three"} {
		if !contents[want] {
			t.Fatalf("content %q lost; notes = %+v", want, destination.notes.notes)
		}
	}
}

// TestUpdateProxyProfileKeepsTheNewPasswordFlag covers the record update path:
// saving a new Proxy Profile password must leave HasPassword describing the
// credential that was just stored, not the previous record's fields.
func TestUpdateProxyProfileKeepsTheNewPasswordFlag(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	updated, err := source.service.UpdateProxyProfile(ctx, "proxy-1",
		domainvault.ProxyProfileDraft{
			Label: "Bastion",
			Config: domainvault.ProxyConfig{
				Type: domainvault.ProxySOCKS5, Host: "proxy.example.test", Port: 1080,
				Username: "proxyuser", Password: "newproxyp@ss",
			},
		})
	if err != nil {
		t.Fatalf("UpdateProxyProfile: %v", err)
	}
	if !updated.Config.HasPassword {
		t.Fatal("UpdateProxyProfile cleared HasPassword after storing a password")
	}
	if got := source.secrets.values[ProxyProfileSecretRef("proxy-1", FieldProxyPassword)]; got != "newproxyp@ss" {
		t.Fatalf("stored proxy password = %q", got)
	}
	effective, err := source.service.EffectiveHost(ctx, "host-1")
	if err != nil {
		t.Fatalf("EffectiveHost: %v", err)
	}
	if effective.Proxy == nil || effective.Proxy.Password != "newproxyp@ss" {
		t.Fatalf("effective proxy = %+v", effective.Proxy)
	}
}
