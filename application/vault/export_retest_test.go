package vault

import (
	"context"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

func previewHasIssueCode(preview ImportPreview, code vaultimport.IssueCode) *vaultimport.Issue {
	for index := range preview.Issues {
		if preview.Issues[index].Code == code {
			return &preview.Issues[index]
		}
	}
	return nil
}

// TestCSVExportsStartWithUTF8BOMAndRoundTrip covers the original exporter's
// byte contract: both CSVs begin EF BB BF, and the BOM is not part of the
// header or the first Host's fields.
func TestCSVExportsStartWithUTF8BOMAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	for _, format := range []ExportFormat{ExportCSV, ExportLogsCSV} {
		exported, err := source.service.ExportVault(ctx, ExportOptions{
			Format: format, ExportedAt: fixedFamilyTime(),
		})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if len(exported.Content) < 3 || string(exported.Content[:3]) != "\xef\xbb\xbf" {
			t.Fatalf("%s does not start with a UTF-8 BOM: %q", format, exported.Content[:3])
		}
		wantHeader := "G"
		if format == ExportLogsCSV {
			wantHeader = "K"
		}
		if string(exported.Content[3:4]) != wantHeader {
			t.Fatalf("%s header after the BOM = %q", format, exported.Content[3:12])
		}
	}
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportCSV, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{FileName: "netcatty-hosts.csv"})
	if err != nil {
		t.Fatalf("the BOM broke CSV detection: %v", err)
	}
	if string(preview.Format) != string(ExportCSV) || len(preview.Rows) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	if preview.Rows[0].Label != "Web One" || preview.Rows[0].Group != "Prod/Web" {
		t.Fatalf("the BOM leaked into the first row: %+v", preview.Rows[0])
	}
}

// TestKeysJSONWithoutSecretsRestoresMetadataKey is the secrets-off Keys
// path: the exported Key is metadata only, but it is still an exported record,
// so it is restored with HasPrivateKey=false and a warning, never dropped.
func TestKeysJSONWithoutSecretsRestoresMetadataKey(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportKeys, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if preview.FamilyCounts.Keys != 1 {
		t.Fatalf("preview Keys = %+v", preview.FamilyCounts)
	}
	issue := previewHasIssueCode(preview, vaultimport.IssueCode("key_without_material"))
	if issue == nil || issue.Severity != vaultimport.SeverityWarning {
		t.Fatalf("preview issues = %+v", preview.Issues)
	}
	result, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{})
	if err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if result.FamilyCreated != 1 || result.FamilySkipped != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(destination.keys.keys) != 1 {
		t.Fatalf("Keys = %+v", destination.keys.keys)
	}
	key := destination.keys.keys[0]
	if key.Label != "deploy key" || key.PublicKey == "" || key.HasPrivateKey {
		t.Fatalf("metadata Key = %+v", key)
	}
}

// TestFamilyOnlyImportsReportCreatedCounts covers the banner contract: every
// restored non-Host record is counted, so a family-only import cannot report
// zero imported and zero skipped.
func TestFamilyOnlyImportsReportCreatedCounts(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	cases := []struct {
		format ExportFormat
		want   int
	}{
		{ExportKeys, 1},
		{ExportSnippets, 4}, // two Snippet packages plus two Snippets
		{ExportNotesZIP, 5}, // two Note Groups plus three Notes
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
			preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
				PreviewImportOptions{})
			if err != nil {
				t.Fatalf("PreviewImport: %v", err)
			}
			result, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{})
			if err != nil {
				t.Fatalf("CommitImport: %v", err)
			}
			if result.FamilyCreated != tc.want {
				t.Fatalf("FamilyCreated = %d, want %d (preview %+v)",
					result.FamilyCreated, tc.want, preview.FamilyCounts)
			}
			if result.FamilySkipped != 0 {
				t.Fatalf("FamilySkipped = %d, want 0", result.FamilySkipped)
			}
		})
	}
}

// TestVaultJSONCountsFamiliesAndHostsTogether covers the aggregate banner:
// family rows and Host Groups together account for the preview total.
func TestVaultJSONCountsFamiliesAndHostsTogether(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	got := result.FamilyCreated + len(result.GroupsCreated)
	if got != preview.FamilyCounts.Total() {
		t.Fatalf("family %d + groups %d = %d, preview total %d",
			result.FamilyCreated, len(result.GroupsCreated), got,
			preview.FamilyCounts.Total())
	}
}

// TestNotesZIPKeepsEmptyNoteGroups covers a Note Group with no Note: the
// manifest carries the group list, not only per-note paths.
func TestNotesZIPKeepsEmptyNoteGroups(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	source.notes.notes = nil
	source.notes.groups = []domainvault.NoteGroup{
		{Path: "Empty", OrderKey: 1000},
		{Path: "Empty/Child", OrderKey: 2000},
	}
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportNotesZIP, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{})
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	result, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{})
	if err != nil {
		t.Fatalf("CommitImport: %v", err)
	}
	if result.FamilyCreated != 2 {
		t.Fatalf("FamilyCreated = %d, want the two empty groups", result.FamilyCreated)
	}
	if !containsNoteGroup(destination.notes.groups, "Empty") ||
		!containsNoteGroup(destination.notes.groups, "Empty/Child") {
		t.Fatalf("Note Groups = %+v", destination.notes.groups)
	}
}

// TestSnippetsJSONWarnsAboutDanglingTargets covers both sides of the target
// contract: a target restored to a real Host is silent, while an unresolved
// target is preserved and named in a warning.
func TestSnippetsJSONWarnsAboutDanglingTargets(t *testing.T) {
	ctx := context.Background()
	source := newFamilySource(t)
	exported, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportSnippets, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	destination := newDestination(t)
	preview, err := destination.service.PreviewImport(ctx, string(exported.Content),
		PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	issue := previewHasIssueCode(preview, vaultimport.IssueCode("dangling_snippet_target"))
	if issue == nil || issue.Severity != vaultimport.SeverityWarning ||
		issue.Value != "host-1" {
		t.Fatalf("dangling target issue = %+v, issues = %+v", issue, preview.Issues)
	}
	if _, err := destination.service.CommitImport(ctx, preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, snippet := range destination.snippets.snippets {
		if snippet.Label == "Deploy" && (len(snippet.Targets) != 1 || snippet.Targets[0] != "host-1") {
			t.Fatalf("dangling target was not preserved: %+v", snippet.Targets)
		}
	}

	// With the target Host present the reference is remapped, so no warning.
	jsonExport, err := source.service.ExportVault(ctx, ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedFamilyTime(),
	})
	if err != nil {
		t.Fatal(err)
	}
	remapped := newDestination(t)
	jsonPreview, err := remapped.service.PreviewImport(ctx, string(jsonExport.Content),
		PreviewImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if issue := previewHasIssueCode(jsonPreview, vaultimport.IssueCode("dangling_snippet_target")); issue != nil {
		t.Fatalf("resolved target still warned: %+v", issue)
	}
}
