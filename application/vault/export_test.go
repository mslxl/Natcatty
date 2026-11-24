package vault

import (
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
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
		HasTOTP: true,
		KeyID:   "k1",
	}
	hosts.hosts = append(hosts.hosts, host)
	key := domainvault.Key{
		ID: "k1", Label: "id", Type: domainvault.KeyTypeED25519,
		PublicKey: "ssh-ed25519 AAAA id", Source: domainvault.KeySourceImported,
		FilePath: "/keys/id", OrderKey: 1000,
	}
	keys.keys = append(keys.keys, key)
	secrets.values[HostSecretRef("h1", FieldPassword)] = "secret"
	secrets.values[HostSecretRef("h1", FieldTOTP)] =
		"otpauth://totp/Acme:root?algorithm=SHA1&digits=6&issuer=Acme&period=30&secret=JBSWY3DPEHPK3PXP"
	secrets.values[KeySecretRef("k1", FieldPrivateKey)] = "PRIVATE"
	return service
}

type fakeHistory struct {
	logs       []domainhistory.ConnectionLog
	entries    []domainhistory.Entry
	entryCalls int
}

func (f *fakeHistory) ConnectionLogs(context.Context) ([]domainhistory.ConnectionLog, error) {
	return append([]domainhistory.ConnectionLog(nil), f.logs...), nil
}

// Entries is deliberately outside the export port. It is kept on the fake so
// the export boundary test can prove the service never asks for command
// history.
func (f *fakeHistory) Entries(context.Context) ([]domainhistory.Entry, error) {
	f.entryCalls++
	return append([]domainhistory.Entry(nil), f.entries...), nil
}

// TestPortableExportNeverCarriesCommandHistory pins spec O5 for every export
// format: a planted command must not reach the document, and the export must
// not even request the command-history family.
func TestPortableExportNeverCarriesCommandHistory(t *testing.T) {
	service := exportService(t)
	history, ok := service.history.(*fakeHistory)
	if !ok {
		t.Fatalf("export history source = %T, want the test fake", service.history)
	}
	const planted = "guqzv-planted-command-that-must-not-export"
	history.entries = []domainhistory.Entry{{
		ID: "planted-entry", Command: planted, Scope: "global",
	}}
	for _, format := range ExportFormats() {
		exported, err := service.ExportVault(context.Background(), ExportOptions{
			Format: format, IncludeSecrets: true, ExportedAt: fixedExportTime,
		})
		if err != nil {
			t.Fatalf("ExportVault(%s): %v", format, err)
		}
		if strings.Contains(string(exported.Content), planted) {
			t.Fatalf("%s export contains a planted command", format)
		}
	}
	if history.entryCalls != 0 {
		t.Fatalf("a portable export asked for command history %d time(s)", history.entryCalls)
	}
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

// TestCSVExportAppliesGroupJumpChainDefaults proves a Host with no own chain
// writes the chain its Group default supplies. The CSV row must carry the same
// ProxyJump cell a Host-owned chain would, with no issue, and importing the
// file must restore the two hops as the Host's own chain. JSON keeps carrying
// the group record itself, so only the CSV path resolves the default.
func TestCSVExportAppliesGroupJumpChainDefaults(t *testing.T) {
	service, hosts, groups, _, _ := newImportService(t)
	groups.items = []domainvault.Group{{
		Path: "Prod",
		HostChain: &domainvault.HostChain{
			HostIDs: []model.HostID{"jump-1", "jump-2"},
		},
	}}
	hosts.hosts = []domainvault.Host{
		{ID: "jump-1", Label: "bastion", Hostname: "bastion.example.test",
			Protocol: domainvault.ProtocolSSH},
		{ID: "jump-2", Label: "edge", Hostname: "edge.example.test", Port: 2222,
			Protocol: domainvault.ProtocolSSH},
		{ID: "client", Label: "client", Hostname: "client.example.test",
			Protocol: domainvault.ProtocolSSH, Group: "Prod"},
	}

	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportCSV, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(export.Issues) != 0 {
		t.Fatalf("a group-inherited chain produced issues: %+v", export.Issues)
	}

	records, err := csv.NewReader(strings.NewReader(
		string(export.Content))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	columns := make(map[string]int, len(records[0]))
	for index, name := range records[0] {
		columns[name] = index
	}
	var proxyJump string
	for _, row := range records[1:] {
		if row[columns["Hostname/IP"]] == "client.example.test" {
			proxyJump = vaultimport.DecodeMarkedField(row[columns["ProxyJump"]],
				vaultimport.MarkerProxyJump)
		}
	}
	if proxyJump != "bastion.example.test,edge.example.test:2222" {
		t.Fatalf("ProxyJump = %q, want the Group's two hops", proxyJump)
	}
	// The stored Host must still have no own chain; only the exported row
	// resolves the default.
	if hosts.hosts[2].HostChain != nil {
		t.Fatalf("the export mutated the stored Host: %+v", hosts.hosts[2])
	}

	_, importedHosts, _, _, importStore := newImportService(t)
	var restored int
	importer := NewService(importedHosts,
		WithGroups(importStore.groups),
		WithImportStore(importStore),
		WithIDGenerator(func() model.HostID {
			restored++
			return model.HostID("restored-" + strconv.Itoa(restored))
		}),
	)
	preview, err := importer.PreviewImport(context.Background(), string(export.Content),
		PreviewImportOptions{Format: "csv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Issues) != 0 {
		t.Fatalf("import issues: %+v", preview.Issues)
	}
	if _, err := importer.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	byLabel := make(map[string]domainvault.Host, len(importedHosts.hosts))
	for _, host := range importedHosts.hosts {
		byLabel[host.Label] = host
	}
	chain := byLabel["client"].HostChain
	if chain == nil || len(chain.HostIDs) != 2 {
		t.Fatalf("imported chain = %+v, want two hops", chain)
	}
	if chain.HostIDs[0] != byLabel["bastion"].ID || chain.HostIDs[1] != byLabel["edge"].ID {
		t.Fatalf("imported hops = %v, want bastion then edge", chain.HostIDs)
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
	if !hosts.hosts[0].HasTOTP {
		t.Fatal("TOTP marker did not survive the JSON round trip")
	}
	if totp := secrets.values[HostSecretRef(hosts.hosts[0].ID, FieldTOTP)]; totp !=
		"otpauth://totp/Acme:root?algorithm=SHA1&digits=6&issuer=Acme&period=30&secret=JBSWY3DPEHPK3PXP" {
		t.Fatalf("TOTP = %q", totp)
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

// vncFixtureHost is one Host the VNC portability test seeds. Password and
// VNCPassword are the plaintext in their own secret slots; HasPassword and
// HasVNCPassword are the record markers that promise the value exists.
type vncFixtureHost struct {
	host        domainvault.Host
	password    string
	vncPassword string
}

// newVNCPortabilityService seeds Hosts and field secrets in their own slots,
// so an export reads exactly what the records advertise.
func newVNCPortabilityService(t *testing.T) (*Service, map[string]vncFixtureHost) {
	t.Helper()
	service, hosts, _, secrets, _ := newImportService(t)
	fixtures := map[string]vncFixtureHost{}
	add := func(fixture vncFixtureHost) {
		hosts.hosts = append(hosts.hosts, fixture.host)
		fixtures[fixture.host.Label] = fixture
		if fixture.password != "" {
			secrets.values[HostSecretRef(fixture.host.ID, FieldPassword)] = fixture.password
		}
		if fixture.vncPassword != "" {
			secrets.values[HostSecretRef(fixture.host.ID, FieldVNCPassword)] = fixture.vncPassword
		}
	}
	add(vncFixtureHost{
		host: domainvault.Host{
			ID: "vnc-display", Label: "Desktop Fit", Hostname: "fit.example.test",
			Port: 5901, Protocol: domainvault.ProtocolVNC, OrderKey: 1000,
			HasVNCPassword: true,
			VNC: &domainvault.VNCConfig{
				DisplayMode: domainvault.VNCDisplayNumber, FitMode: domainvault.VNCFit,
				Shared: true, CursorMode: domainvault.VNCCursorLocal,
			},
		},
		vncPassword: "vnc-fit-alpha",
	})
	add(vncFixtureHost{
		host: domainvault.Host{
			ID: "vnc-port", Label: "Desktop OneToOne", Hostname: "one.example.test",
			Port: 5999, Protocol: domainvault.ProtocolVNC, OrderKey: 2000,
			HasVNCPassword: true,
			VNC: &domainvault.VNCConfig{
				DisplayMode: domainvault.VNCPort, FitMode: domainvault.VNCOneToOne,
				Shared: false, CursorMode: domainvault.VNCCursorServer,
				PlaintextWarningAcknowledged: true,
			},
		},
		vncPassword: "vnc-one-beta",
	})
	add(vncFixtureHost{
		host: domainvault.Host{
			ID: "vnc-clip", Label: "Desktop Clip", Hostname: "clip.example.test",
			Port: 5902, Protocol: domainvault.ProtocolVNC, OrderKey: 3000,
			HasVNCPassword: true,
			VNC: &domainvault.VNCConfig{
				DisplayMode: domainvault.VNCDisplayNumber, FitMode: domainvault.VNCClip,
				Shared: true, CursorMode: domainvault.VNCCursorServer, ViewOnly: true,
			},
		},
		vncPassword: "vnc-clip-gamma",
	})
	// A VNC Host whose legacy record also advertises the SSH slot. Import must
	// keep the VNC credential in its own slot and never write the SSH password
	// slot for a VNC Host.
	add(vncFixtureHost{
		host: domainvault.Host{
			ID: "vnc-both", Label: "Desktop Both", Hostname: "both-vnc.example.test",
			Port: 5903, Protocol: domainvault.ProtocolVNC, OrderKey: 4000,
			HasPassword: true, HasVNCPassword: true,
			VNC: &domainvault.VNCConfig{
				DisplayMode: domainvault.VNCPort, FitMode: domainvault.VNCFit,
				Shared: true, CursorMode: domainvault.VNCCursorLocal,
			},
		},
		password: "vnc-both-ssh-slot", vncPassword: "vnc-both-vnc-slot",
	})
	// An SSH Host whose record also advertises a VNC credential. Import must
	// keep the SSH password in its own slot and drop the VNC one rather than
	// copying it across.
	add(vncFixtureHost{
		host: domainvault.Host{
			ID: "ssh-both", Label: "SSH Both", Hostname: "both-ssh.example.test",
			Port: 22, Protocol: domainvault.ProtocolSSH, OrderKey: 5000,
			HasPassword: true, HasVNCPassword: true,
		},
		password: "ssh-host-ssh-slot", vncPassword: "ssh-host-vnc-slot",
	})
	return service, fixtures
}

// TestJSONExportRoundTripsVNCConfigurationAndPassword exports a Vault with
// several VNC Hosts, imports it into a fresh Vault through the real service
// APIs, and proves every viewer field and the dedicated VNC password arrive.
func TestJSONExportRoundTripsVNCConfigurationAndPassword(t *testing.T) {
	service, fixtures := newVNCPortabilityService(t)
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, IncludeSecrets: true, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(export.Content), `"vncPassword"`) {
		t.Fatal("a destructive export did not carry the VNC password field")
	}

	fresh, importedHosts, _, secrets, _ := newImportService(t)
	preview, err := fresh.PreviewImport(context.Background(), string(export.Content),
		PreviewImportOptions{Format: "netcatty_json"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(importedHosts.hosts) != len(fixtures) {
		t.Fatalf("imported %d Hosts, want %d", len(importedHosts.hosts), len(fixtures))
	}
	byLabel := make(map[string]domainvault.Host, len(importedHosts.hosts))
	for _, host := range importedHosts.hosts {
		byLabel[host.Label] = host
	}

	for label, fixture := range fixtures {
		restored, ok := byLabel[label]
		if !ok {
			t.Errorf("%q did not survive the round trip", label)
			continue
		}
		if restored.Hostname != fixture.host.Hostname || restored.Port != fixture.host.Port ||
			restored.Protocol != fixture.host.Protocol {
			t.Errorf("%q endpoint = %s:%d %s", label, restored.Hostname, restored.Port, restored.Protocol)
		}
		if fixture.host.Protocol != domainvault.ProtocolVNC {
			continue
		}
		if restored.VNC == nil {
			t.Fatalf("%q lost its VNC configuration", label)
		}
		if *restored.VNC != *fixture.host.VNC {
			t.Errorf("%q VNC = %+v, want %+v", label, *restored.VNC, *fixture.host.VNC)
		}
		if !restored.HasVNCPassword {
			t.Errorf("%q HasVNCPassword = false", label)
		}
		if got := secrets.values[HostSecretRef(restored.ID, FieldVNCPassword)]; got != fixture.vncPassword {
			t.Errorf("%q VNC password = %q, want %q", label, got, fixture.vncPassword)
		}
		// A VNC Host never reads or writes the SSH password slot, even when
		// the source record carried both.
		if _, found := secrets.values[HostSecretRef(restored.ID, FieldPassword)]; found {
			t.Errorf("%q wrote a value into the SSH password slot", label)
		}
		if restored.HasPassword {
			t.Errorf("%q imported HasPassword = true", label)
		}
	}

	sshBoth := byLabel["SSH Both"]
	if !sshBoth.HasPassword || sshBoth.HasVNCPassword {
		t.Fatalf("SSH Host markers = password %v, vnc %v", sshBoth.HasPassword, sshBoth.HasVNCPassword)
	}
	if got := secrets.values[HostSecretRef(sshBoth.ID, FieldPassword)]; got != fixtures["SSH Both"].password {
		t.Fatalf("SSH password = %q, want its own slot value", got)
	}
	if _, found := secrets.values[HostSecretRef(sshBoth.ID, FieldVNCPassword)]; found {
		t.Fatal("an SSH Host imported a value into the VNC password slot")
	}
	if sshBoth.VNC != nil {
		t.Fatal("an SSH Host imported a VNC configuration")
	}
}

// TestNonDestructiveJSONExportOmitsTheVNCPassword proves the ordinary export
// keeps the viewer configuration and the marker but leaves the credential out,
// and that the omission is reported.
func TestNonDestructiveJSONExportOmitsTheVNCPassword(t *testing.T) {
	service, fixtures := newVNCPortabilityService(t)
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportJSON, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(export.Content)
	wantOmitted := 0
	for _, fixture := range fixtures {
		if fixture.password != "" {
			wantOmitted++
			if strings.Contains(text, fixture.password) {
				t.Fatal("a non-destructive export leaked an SSH password")
			}
		}
		if fixture.vncPassword != "" {
			wantOmitted++
			if strings.Contains(text, fixture.vncPassword) {
				t.Fatal("a non-destructive export leaked a VNC password")
			}
		}
	}
	if !strings.Contains(text, `"fitMode"`) || !strings.Contains(text, `"hasVncPassword"`) {
		t.Fatal("the ordinary export dropped the VNC configuration or its marker")
	}
	if export.OmittedSecrets != wantOmitted {
		t.Fatalf("OmittedSecrets = %d, want %d stored Host credentials", export.OmittedSecrets, wantOmitted)
	}
}

// csvColumnCell reads one exported CSV cell by Hostname and exact header name.
func csvColumnCell(t *testing.T, text, hostname, column string) string {
	t.Helper()
	rows, issues := vaultimport.ParseCSV(text)
	if len(issues) != 0 {
		t.Fatalf("CSV parse issues = %+v", issues)
	}
	columnAt, hostAt := -1, -1
	for index, header := range rows[0] {
		switch header {
		case column:
			columnAt = index
		case "Hostname/IP":
			hostAt = index
		}
	}
	if columnAt < 0 || hostAt < 0 {
		t.Fatalf("missing %q or Hostname/IP in %v", column, rows[0])
	}
	for _, row := range rows[1:] {
		if row[hostAt] == hostname {
			if columnAt >= len(row) {
				return ""
			}
			return row[columnAt]
		}
	}
	t.Fatalf("no exported row for %q", hostname)
	return ""
}

// TestCSVExportRoundTripsVNCConfigurationAndPassword drives the real export
// and import services: every viewer field and the dedicated VNC password
// survive, a VNC Host's SSH Password cell stays empty, and an SSH Host never
// receives VNC data.
func TestCSVExportRoundTripsVNCConfigurationAndPassword(t *testing.T) {
	service, fixtures := newVNCPortabilityService(t)
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportCSV, IncludeSecrets: true, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(export.Content)
	if got := csvColumnCell(t, text, "both-vnc.example.test", "Password"); got != "" {
		t.Fatalf("a VNC Host exported an SSH Password cell %q", got)
	}
	if got := csvColumnCell(t, text, "both-vnc.example.test", "VNCPassword"); got != fixtures["Desktop Both"].vncPassword {
		t.Fatalf("VNC Password cell = %q", got)
	}
	if got := csvColumnCell(t, text, "both-vnc.example.test", "VNC"); !strings.HasPrefix(got, vaultimport.MarkerVNC) {
		t.Fatalf("VNC cell = %q, want a marked configuration", got)
	}

	fresh, importedHosts, _, secrets, _ := newImportService(t)
	preview, err := fresh.PreviewImport(context.Background(), text,
		PreviewImportOptions{Format: vaultimport.FormatCSV})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(importedHosts.hosts) != len(fixtures) {
		t.Fatalf("imported %d Hosts, want %d", len(importedHosts.hosts), len(fixtures))
	}
	byLabel := make(map[string]domainvault.Host, len(importedHosts.hosts))
	for _, host := range importedHosts.hosts {
		byLabel[host.Label] = host
	}
	for label, fixture := range fixtures {
		restored, ok := byLabel[label]
		if !ok {
			t.Errorf("%q did not survive the CSV round trip", label)
			continue
		}
		if restored.Hostname != fixture.host.Hostname || restored.Port != fixture.host.Port ||
			restored.Protocol != fixture.host.Protocol {
			t.Errorf("%q endpoint = %s:%d %s", label, restored.Hostname, restored.Port, restored.Protocol)
		}
		if fixture.host.Protocol != domainvault.ProtocolVNC {
			continue
		}
		if restored.VNC == nil || *restored.VNC != *fixture.host.VNC {
			t.Errorf("%q VNC = %+v, want %+v", label, restored.VNC, fixture.host.VNC)
		}
		if !restored.HasVNCPassword {
			t.Errorf("%q HasVNCPassword = false", label)
		}
		if got := secrets.values[HostSecretRef(restored.ID, FieldVNCPassword)]; got != fixture.vncPassword {
			t.Errorf("%q VNC password = %q, want %q", label, got, fixture.vncPassword)
		}
		if _, found := secrets.values[HostSecretRef(restored.ID, FieldPassword)]; found {
			t.Errorf("%q wrote a value into the SSH password slot", label)
		}
		if restored.HasPassword {
			t.Errorf("%q imported HasPassword = true", label)
		}
	}
	sshBoth := byLabel["SSH Both"]
	if !sshBoth.HasPassword || sshBoth.HasVNCPassword {
		t.Fatalf("SSH Host markers = password %v, vnc %v", sshBoth.HasPassword, sshBoth.HasVNCPassword)
	}
	if got := secrets.values[HostSecretRef(sshBoth.ID, FieldPassword)]; got != fixtures["SSH Both"].password {
		t.Fatalf("SSH password = %q, want its own slot value", got)
	}
	if _, found := secrets.values[HostSecretRef(sshBoth.ID, FieldVNCPassword)]; found {
		t.Fatal("an SSH Host imported a value into the VNC password slot")
	}
	if sshBoth.VNC != nil {
		t.Fatal("an SSH Host imported a VNC configuration")
	}
}

// TestNonDestructiveCSVExportOmitsTheVNCPassword proves the ordinary export
// keeps the viewer configuration but leaves both credential cells empty and
// reports them as omitted.
func TestNonDestructiveCSVExportOmitsTheVNCPassword(t *testing.T) {
	service, fixtures := newVNCPortabilityService(t)
	export, err := service.ExportVault(context.Background(), ExportOptions{
		Format: ExportCSV, ExportedAt: fixedExportTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(export.Content)
	wantOmitted := 0
	for _, fixture := range fixtures {
		if fixture.password != "" {
			wantOmitted++
			if strings.Contains(text, fixture.password) {
				t.Fatal("a non-destructive CSV export leaked an SSH password")
			}
		}
		if fixture.vncPassword != "" {
			wantOmitted++
			if strings.Contains(text, fixture.vncPassword) {
				t.Fatal("a non-destructive CSV export leaked a VNC password")
			}
		}
	}
	if got := csvColumnCell(t, text, "fit.example.test", "VNCPassword"); got != "" {
		t.Fatalf("VNCPassword cell = %q, want empty", got)
	}
	if got := csvColumnCell(t, text, "fit.example.test", "VNC"); !strings.Contains(got, "fitMode=fit") {
		t.Fatalf("ordinary export dropped the VNC configuration: %q", got)
	}
	if export.OmittedSecrets != wantOmitted {
		t.Fatalf("OmittedSecrets = %d, want %d stored Host credentials", export.OmittedSecrets, wantOmitted)
	}
}

// TestCSVOldFourteenColumnFileImports proves a file written before the VNC
// columns existed still imports, and the VNC Host starts from the viewer
// defaults.
func TestCSVOldFourteenColumnFileImports(t *testing.T) {
	headers := []string{
		"Groups", "Label", "Tags", "Notes", "Hostname/IP", "Protocol",
		"Port", "Username", "Password", "KeyPath", "Passphrase", "Proxy",
		"ProxyJump", "LocalShell",
	}
	row := []string{
		"Prod", "Legacy", "", "", "legacy.example.test", "vnc", "5901",
		"", "", "", "", "", "", "",
	}
	text := "\xef\xbb\xbf" + strings.Join([]string{
		strings.Join(headers, ","), strings.Join(row, ","),
	}, "\r\n")

	service, hosts, _, secrets, _ := newImportService(t)
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{
		Format: vaultimport.FormatCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(hosts.hosts) != 1 {
		t.Fatalf("imported %+v", hosts.hosts)
	}
	restored := hosts.hosts[0]
	want := domainvault.DefaultVNCConfig()
	if restored.VNC == nil || *restored.VNC != want {
		t.Fatalf("VNC = %+v, want defaults %+v", restored.VNC, want)
	}
	if restored.HasVNCPassword {
		t.Fatal("a file without a VNC password column imported a credential")
	}
	if len(secrets.values) != 0 {
		t.Fatalf("secrets = %+v, want none", secrets.values)
	}
}

// TestCSVPartialVNCCellDefaultsSharedTrue proves a hand-written cell that lists
// only some keys imports with the viewer defaults, sharing included.
func TestCSVPartialVNCCellDefaultsSharedTrue(t *testing.T) {
	row := []string{
		"", "Desk", "", "", "desk.example.test", "vnc", "5901", "",
		"", "", "", "", "", "", vaultimport.MarkerVNC + "viewOnly=true", "",
	}
	text := strings.Join([]string{
		strings.Join(vaultimport.CSVHeader(), ","), strings.Join(row, ","),
	}, "\r\n")

	service, hosts, _, _, _ := newImportService(t)
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{
		Format: vaultimport.FormatCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(hosts.hosts) != 1 || hosts.hosts[0].VNC == nil {
		t.Fatalf("imported %+v", hosts.hosts)
	}
	config := hosts.hosts[0].VNC
	if !config.Shared || !config.ViewOnly {
		t.Fatalf("VNC = %+v, want shared=true and viewOnly=true", config)
	}
	if config.DisplayMode != domainvault.VNCDisplayNumber || config.FitMode != domainvault.VNCFit ||
		config.CursorMode != domainvault.VNCCursorLocal {
		t.Fatalf("missing keys did not default: %+v", config)
	}
}

// TestCSVPasswordsNeverCrossSlots proves VNC-only, SSH-only, and both-marker
// documents each land their credentials in the right slot. A VNC Host's generic
// Password cell is discarded by normalization.
func TestCSVPasswordsNeverCrossSlots(t *testing.T) {
	text := strings.Join([]string{
		"Hostname/IP,Label,Protocol,Port,Password,VNCPassword",
		"vnc-only.example.test,VNC only,vnc,5901,,vnc-secret",
		"ssh-only.example.test,SSH only,ssh,22,ssh-secret,",
		"both.example.test,Both,vnc,5902,ssh-other,vnc-other",
	}, "\r\n")

	service, hosts, _, secrets, _ := newImportService(t)
	preview, err := service.PreviewImport(context.Background(), text, PreviewImportOptions{
		Format: vaultimport.FormatCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CommitImport(context.Background(), preview, ImportCommitOptions{}); err != nil {
		t.Fatal(err)
	}
	byLabel := make(map[string]domainvault.Host, len(hosts.hosts))
	for _, host := range hosts.hosts {
		byLabel[host.Label] = host
	}

	vncOnly := byLabel["VNC only"]
	if !vncOnly.HasVNCPassword || vncOnly.HasPassword {
		t.Fatalf("VNC-only markers = vnc %v, ssh %v", vncOnly.HasVNCPassword, vncOnly.HasPassword)
	}
	if got := secrets.values[HostSecretRef(vncOnly.ID, FieldVNCPassword)]; got != "vnc-secret" {
		t.Fatalf("VNC-only VNC slot = %q", got)
	}
	if _, found := secrets.values[HostSecretRef(vncOnly.ID, FieldPassword)]; found {
		t.Fatal("VNC-only wrote the SSH slot")
	}

	sshOnly := byLabel["SSH only"]
	if !sshOnly.HasPassword || sshOnly.HasVNCPassword {
		t.Fatalf("SSH-only markers = ssh %v, vnc %v", sshOnly.HasPassword, sshOnly.HasVNCPassword)
	}
	if got := secrets.values[HostSecretRef(sshOnly.ID, FieldPassword)]; got != "ssh-secret" {
		t.Fatalf("SSH-only SSH slot = %q", got)
	}
	if _, found := secrets.values[HostSecretRef(sshOnly.ID, FieldVNCPassword)]; found {
		t.Fatal("SSH-only wrote the VNC slot")
	}

	both := byLabel["Both"]
	if !both.HasVNCPassword || both.HasPassword {
		t.Fatalf("both markers = vnc %v, ssh %v", both.HasVNCPassword, both.HasPassword)
	}
	if got := secrets.values[HostSecretRef(both.ID, FieldVNCPassword)]; got != "vnc-other" {
		t.Fatalf("both VNC slot = %q", got)
	}
	if _, found := secrets.values[HostSecretRef(both.ID, FieldPassword)]; found {
		t.Fatal("a VNC Host kept the generic Password cell as an SSH credential")
	}
}
