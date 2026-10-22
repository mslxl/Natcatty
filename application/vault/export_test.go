package vault

import (
	"context"
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

var fixedExportTime = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

// exportService builds a Service with every export source and one Host whose
// credentials live in the fake secret store.
func exportService(t *testing.T) *Service {
	t.Helper()
	service, hosts, _, secrets, _ := newImportService(t)
	keys := &fakeKeys{}
	groups := &fakeGroups{items: []domainvault.Group{{Path: "Prod"}}}
	service.keys = keys
	service.groups = groups
	service.inspector = &fakeInspector{}
	service.importFiles = fakeImportFiles{"/keys/id": "PRIVATE"}
	service.snippets = &fakeSnippets{snippets: []domainvault.Snippet{{
		ID: "s1", Label: "Uptime", Command: "uptime", Package: "Ops", OrderKey: 1000,
	}}}
	service.notes = &fakeNotes{notes: []domainvault.Note{{
		ID: "n1", Title: "Runbook", Content: "body\r\n", Group: "Ops", OrderKey: 1000,
	}}}
	service.history = &fakeHistory{
		logs: []domainhistory.ConnectionLog{{
			ID: "l1", Kind: "ssh", Status: domainhistory.ConnectionConnected,
		}},
		entries: []domainhistory.Entry{{
			ID: "e1", Command: "ls", Scope: "global",
		}},
	}
	host := domainvault.Host{
		ID: "h1", Label: "=Web", Hostname: "10.0.0.1", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, Group: "Prod",
		Tags: []string{"web"}, Notes: "notes", OrderKey: 1000, HasPassword: true,
		KeyID: "k1",
	}
	hosts.hosts = append(hosts.hosts, host)
	key := domainvault.Key{
		ID: "k1", Label: "id", Type: domainvault.KeyTypeED25519,
		PublicKey: "ssh-ed25519 AAAA id", Source: domainvault.KeySourceImported,
		FilePath: "/keys/id", OrderKey: 1000,
	}
	keys.keys = append(keys.keys, key)
	secrets.values[HostSecretRef("h1", FieldPassword)] = "secret"
	secrets.values[KeySecretRef("k1", FieldPrivateKey)] = "PRIVATE"
	return service
}

type fakeHistory struct {
	logs    []domainhistory.ConnectionLog
	entries []domainhistory.Entry
}

func (f *fakeHistory) ConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error) {
	return append([]domainhistory.ConnectionLog(nil), f.logs...), nil
}

func (f *fakeHistory) Entries(context.Context) ([]domainhistory.Entry, error) {
	return append([]domainhistory.Entry(nil), f.entries...), nil
}

func TestExportProducesEveryFormat(t *testing.T) {
	service := exportService(t)
	for _, format := range ExportFormats() {
		export, err := service.ExportVault(context.Background(), ExportOptions{
			Format: format, IncludeSecrets: true,
			ExportedAt: fixedExportTime,
		})
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if len(export.Content) == 0 {
			t.Errorf("%s produced no content", format)
		}
		if export.Filename == "" || !strings.HasSuffix(export.Filename, "."+format.Extension()) {
			t.Errorf("%s filename = %q", format, export.Filename)
		}
	}
}

func TestCSVExportRoundTripsThroughImport(t *testing.T) {
	service := exportService(t)
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportCSV, IncludeSecrets: true, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PreviewImport(context.Background(), string(export.Content),
		PreviewImportOptions{Format: "csv"})
	if err != nil {
		t.Fatal(err)
	}
	// The Host already exists, so the merge key conflicts rather than creating
	// a second record.
	if preview.Counts.Conflicts != 1 {
		t.Fatalf("preview counts = %+v", preview.Counts)
	}
	row := preview.Rows[0]
	if row.Label != "=Web" || row.Hostname != "10.0.0.1" || row.Group != "Prod" ||
		row.KeyLabel != "id" {
		t.Fatalf("label=%q host=%q group=%q key=%q", row.Label, row.Hostname, row.Group, row.KeyLabel)
	}

	// Importing into an empty Vault preserves every field and credential.
	_, hosts, _, secrets, _ := newImportService(t)
	importer := NewService(hosts,
		WithGroups(&fakeGroups{}),
		WithSecrets(secrets),
		WithKeys(&fakeKeys{}),
		WithKeyInspector(&fakeInspector{}),
		WithImportFiles(fakeImportFiles{"/keys/id": "PRIVATE"}),
		WithImportStore(&fakeImportStore{hosts: hosts, groups: &fakeGroups{}}),
		WithIDGenerator(func() model.HostID { return "new" }),
	)
	fresh, err := importer.PreviewImport(context.Background(), string(export.Content),
		PreviewImportOptions{Format: "csv"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importer.CommitImport(context.Background(), fresh, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(hosts.hosts) != 1 {
		t.Fatalf("imported %d Hosts", len(hosts.hosts))
	}
	imported := hosts.hosts[0]
	if imported.Label != "=Web" || imported.Hostname != "10.0.0.1" ||
		imported.Notes != "notes" || len(imported.Tags) != 1 {
		t.Fatalf("imported Host = %+v", imported)
	}
	if password := secrets.values[HostSecretRef(imported.ID, FieldPassword)]; password != "secret" {
		t.Fatalf("password = %q", password)
	}
	keys, err := importer.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].Label != "id" {
		t.Fatalf("imported Keys = %+v, %v", keys, err)
	}
}

func TestJSONExportRoundTripsThroughImport(t *testing.T) {
	service := exportService(t)
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, hosts, _, secrets, store := newImportService(t)
	importer := NewService(hosts,
		WithSecrets(secrets),
		WithKeys(&fakeKeys{}),
		WithImportStore(store),
		WithKeyInspector(&fakeInspector{}),
		WithGroups(store.groups),
		WithIDGenerator(func() model.HostID { return "new" }),
	)
	preview, err := importer.PreviewImport(context.Background(), string(export.Content),
		PreviewImportOptions{Format: "netcatty_json"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := importer.CommitImport(context.Background(), preview, ImportCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Imported) != 1 || len(hosts.hosts) != 1 {
		t.Fatalf("imported = %+v, hosts = %d", result, len(hosts.hosts))
	}
	if !hosts.hosts[0].HasPassword || hosts.hosts[0].KeyID == "" {
		t.Fatalf("password=%v key=%q", hosts.hosts[0].HasPassword, hosts.hosts[0].KeyID)
	}
}

func TestSnippetAndNoteExportRoundTrip(t *testing.T) {
	service := exportService(t)
	snippets, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportSnippets, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	importer := NewService(&fakeHosts{}, WithSnippets(&fakeSnippets{}))
	result, err := importer.ImportSnippetsJSON(context.Background(), string(snippets.Content))
	if err != nil || result.Created != 1 {
		t.Fatalf("snippets = %+v, %v", result, err)
	}

	notes, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportNotesZIP, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	noteImporter := NewService(&fakeHosts{}, WithNotes(&fakeNotes{}))
	restored, err := noteImporter.ImportNotesZIP(context.Background(), notes.Content)
	if err != nil || restored.Created != 1 {
		t.Fatalf("notes = %+v, %v", restored, err)
	}
}

func TestExportWithNoSecretsHidesCredentials(t *testing.T) {
	service := exportService(t)
	for _, format := range []ExportFormat{ExportCSV, ExportJSON, ExportKeys} {
		export, err := service.ExportVault(context.Background(), ExportOptions{
			Format: format, ExportedAt: fixedExportTime,
		})
		if err != nil {
			t.Fatal(err)
		}
		text := string(export.Content)
		if format != ExportJSON && strings.Contains(text, "PRIVATE") {
			t.Errorf("%s leaked key material", format)
		}
		if format == ExportCSV && strings.Contains(text, "secret") {
			t.Errorf("%s leaked a password", format)
		}
	}
}
