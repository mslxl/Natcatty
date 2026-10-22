package vaultexport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

var stamp = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

func testDataset() Dataset {
	return Dataset{
		Groups: []domainvault.Group{{Path: "Acme/Prod", OrderKey: 1000}},
		Hosts: []Host{
			{
				Host: domainvault.Host{
					ID: "h2", Label: "=SUM(A1)", Hostname: "2001:db8::1",
					Port: 2222, Username: "deploy", Protocol: domainvault.ProtocolSSH,
					Group: "Acme/Prod", Tags: []string{"web", "prod"},
					Notes: "line1\r\nline2, \"quoted\"", OrderKey: 2000,
					KeyID: "k1",
					Proxy: &domainvault.ProxyConfig{
						Type: domainvault.ProxySOCKS5, Host: "127.0.0.1", Port: 1080,
						Username: "proxyuser", HasPassword: true,
					},
				},
				Password: "p@ss,1", KeyPath: "=C:/keys/id_ed25519",
				Passphrase: "pa ss", ProxyPassword: "p@ss",
			},
			{
				Host: domainvault.Host{
					ID: "h1", Label: "Web", Hostname: "web.example.test",
					Port: 22, Username: "root", Protocol: domainvault.ProtocolTelnet,
					Tags: []string{"edge"}, OrderKey: 1000,
				},
			},
		},
		Keys: []Key{{
			Key: domainvault.Key{
				ID: "k1", Label: "id_ed25519", Type: domainvault.KeyTypeED25519,
				PublicKey: "ssh-ed25519 AAAA test", Source: domainvault.KeySourceImported,
				FilePath: "/home/u/.ssh/id_ed25519", OrderKey: 1000,
			},
			PrivateKey: "PRIVATE", Passphrase: "pa ss",
		}},
		Snippets: []domainvault.Snippet{{
			ID: "s1", Label: "Uptime", Command: "uptime", Package: "Ops/Linux",
			Tags: []string{"ops"}, TargetGroups: []string{"Acme/Prod"},
			OrderKey: 1000,
		}},
		Notes: []domainvault.Note{
			{ID: "n1", Title: "Runbook", Content: "# Body\r\nkeep\r\n", Group: "Ops", OrderKey: 1000},
			{ID: "n2", Title: "Runbook", Content: "second", OrderKey: 2000},
		},
		Logs: []domainhistory.ConnectionLog{{
			ID: "l1", HostLabel: "Web", Hostname: "web.example.test", Kind: "ssh",
			Status: domainhistory.ConnectionConnected, StartedAt: stamp.Add(-time.Minute),
			EndedAt: stamp,
		}},
		History: []domainhistory.Entry{{
			ID: "e1", Command: "=cmd", Scope: "global", RecordedAt: stamp,
		}},
	}
}

func exportFor(t *testing.T, format Format, opts Options) []byte {
	t.Helper()
	opts.ExportedAt = stamp
	content, err := Export(testDataset(), format, opts)
	if err != nil {
		t.Fatalf("export %s: %v", format, err)
	}
	return content
}

func TestCSVUsesHeaderMarkedFieldsAndGuards(t *testing.T) {
	csvText := string(exportFor(t, FormatCSV, Options{IncludeSecrets: true}))
	lines := strings.Split(csvText, "\r\n")
	if lines[0] != strings.Join(vaultimport.CSVHeader(), ",") {
		t.Fatalf("header = %q", lines[0])
	}
	// Order: h1 (order 1000) first, h2 second.
	if !strings.Contains(csvText, "Acme/Prod,'=SUM(A1),\"web,prod\",") {
		t.Fatalf("formula guard or tags missing:\n%s", csvText)
	}
	if !strings.Contains(csvText, "[2001:db8::1]") {
		t.Fatalf("IPv6 was not bracketed:\n%s", csvText)
	}
	if !strings.Contains(csvText, vaultimport.MarkerKeyPath+vaultimport.EscapeComponent("=C:/keys/id_ed25519")) {
		t.Fatalf("key path was not marked:\n%s", csvText)
	}
	if !strings.Contains(csvText, "p@ss,1") || !strings.Contains(csvText, "socks5://proxyuser:p%40ss@127.0.0.1:1080") {
		t.Fatalf("credentials missing:\n%s", csvText)
	}
	if !strings.HasSuffix(csvText, "\r\n") {
		t.Fatal("CSV does not end with CRLF")
	}
}

func TestCSVRoundTripsThroughImporter(t *testing.T) {
	csvText := string(exportFor(t, FormatCSV, Options{IncludeSecrets: true}))
	parsed := vaultimport.Parse(vaultimport.FormatCSV, csvText, vaultimport.Options{})
	if len(parsed.Records) != 2 {
		t.Fatalf("parsed %d records, want 2: %+v", len(parsed.Records), parsed.Issues)
	}
	host := parsed.Records[1]
	if host.Label != "=SUM(A1)" || host.Hostname != "2001:db8::1" ||
		host.Port != 2222 || host.Group != "Acme/Prod" || host.Username != "deploy" ||
		strings.Join(host.Tags, ",") != "web,prod" || host.Notes != "line1\r\nline2, \"quoted\"" {
		t.Fatalf("record lost fields: %+v", host)
	}
	if host.Password != "p@ss,1" || host.KeyPath != "=C:/keys/id_ed25519" ||
		host.Passphrase != "pa ss" {
		t.Fatalf("credentials lost: %+v", host)
	}
	if host.Proxy == nil || host.Proxy.Type != "socks5" || host.Proxy.Port != 1080 ||
		host.Proxy.Password != "p@ss" {
		t.Fatalf("proxy lost: %+v", host.Proxy)
	}
}

func TestCSVWithoutSecretsOmitsCredentials(t *testing.T) {
	csvText := string(exportFor(t, FormatCSV, Options{}))
	if strings.Contains(csvText, "p@ss") || strings.Contains(csvText, "pa ss") {
		t.Fatalf("secret leaked without the destructive option:\n%s", csvText)
	}
	if !strings.Contains(csvText, "id_ed25519") {
		t.Fatal("key path should still be exported")
	}
}

func TestJSONRoundTripsThroughImporter(t *testing.T) {
	document := exportFor(t, FormatJSON, Options{IncludeSecrets: true})
	var decoded struct {
		Kind  string `json:"kind"`
		Hosts []struct {
			Hostname string `json:"hostname"`
			Password string `json:"password"`
		} `json:"hosts"`
		Groups int `json:"-"`
	}
	if err := json.Unmarshal(document, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded.Kind != KindVault || len(decoded.Hosts) != 2 {
		t.Fatalf("document = %+v", decoded)
	}

	parsed := vaultimport.Parse(vaultimport.FormatNetcattyJSON, string(document), vaultimport.Options{})
	if len(parsed.Records) != 2 {
		t.Fatalf("parsed %d records: %+v", len(parsed.Records), parsed.Issues)
	}
	if parsed.Records[1].Password != "p@ss,1" ||
		parsed.Records[1].Proxy == nil || parsed.Records[1].Proxy.Password != "p@ss" {
		t.Fatalf("credentials lost: %+v", parsed.Records[1])
	}
	if parsed.Records[1].KeyMaterial != "PRIVATE" || parsed.Records[1].Passphrase != "pa ss" {
		t.Fatalf("inline key lost: %+v", parsed.Records[1])
	}
}

func TestJSONWithoutSecretsOmitsKeyMaterial(t *testing.T) {
	document := string(exportFor(t, FormatJSON, Options{}))
	if strings.Contains(document, "PRIVATE") || strings.Contains(document, `"password"`) {
		t.Fatalf("secret leaked:\n%s", document)
	}
	parsed := vaultimport.Parse(vaultimport.FormatNetcattyJSON, document, vaultimport.Options{})
	if len(parsed.Records) != 2 || parsed.Records[1].KeyMaterial != "" {
		t.Fatalf("non-destructive parse = %+v", parsed.Records)
	}
}

func TestSnippetsExportDropsHostTargets(t *testing.T) {
	var decoded struct {
		Kind            string   `json:"kind"`
		Version         int      `json:"version"`
		SnippetPackages []string `json:"snippetPackages"`
		Snippets        []struct {
			Label        string   `json:"label"`
			TargetGroups []string `json:"targetGroups"`
			Targets      []string `json:"targets"`
		} `json:"snippets"`
	}
	if err := json.Unmarshal(exportFor(t, FormatSnippets, Options{}), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Kind != KindSnippets || decoded.Version != 1 || len(decoded.Snippets) != 1 {
		t.Fatalf("document = %+v", decoded)
	}
	if len(decoded.Snippets[0].TargetGroups) != 1 || decoded.Snippets[0].Targets != nil {
		t.Fatalf("snippet targets = %+v", decoded.Snippets[0])
	}
	if !reflect.DeepEqual(decoded.SnippetPackages, []string{"Ops", "Ops/Linux"}) {
		t.Fatalf("packages = %v", decoded.SnippetPackages)
	}
}

func TestNotesZIPPreservesContentAndGroupsByteForByte(t *testing.T) {
	archive := exportFor(t, FormatNotesZIP, Options{})
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, entry := range reader.File {
		file, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buffer bytes.Buffer
		_, _ = buffer.ReadFrom(file)
		_ = file.Close()
		got[entry.Name] = buffer.String()
	}
	if got["Ops/Runbook.md"] != "# Body\r\nkeep\r\n" {
		t.Fatalf("grouped note = %q", got["Ops/Runbook.md"])
	}
	if got["Runbook.md"] != "second" {
		t.Fatalf("collision name = %v", got)
	}
}

func TestNotesMarkdownAndHTMLIncludeEveryNote(t *testing.T) {
	markdown := string(exportFor(t, FormatNotesMarkdown, Options{}))
	if !strings.Contains(markdown, "# Runbook") || !strings.Contains(markdown, "second") {
		t.Fatalf("markdown = %q", markdown)
	}
	html := string(exportFor(t, FormatNotesHTML, Options{}))
	if !strings.HasPrefix(html, "<!DOCTYPE html>") || !strings.Contains(html, "second") {
		t.Fatalf("html = %q", html[:80])
	}
	localized := string(exportFor(t, FormatNotesHTML, Options{Title: "Notas"}))
	if !strings.Contains(localized, "<title>Notas</title>") {
		t.Fatalf("custom title missing: %q", localized[:120])
	}
}

func TestKeysExportAuthorizedLineAndOptionalMaterial(t *testing.T) {
	var public struct {
		Kind           string `json:"kind"`
		AuthorizedKeys string `json:"authorizedKeys"`
		Keys           []struct {
			PrivateKey string `json:"privateKey"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(exportFor(t, FormatKeys, Options{}), &public); err != nil {
		t.Fatal(err)
	}
	if public.AuthorizedKeys != "ssh-ed25519 AAAA test id_ed25519\n" {
		t.Fatalf("authorizedKeys = %q", public.AuthorizedKeys)
	}
	if public.Keys[0].PrivateKey != "" {
		t.Fatal("private material written without the destructive option")
	}
	var private struct {
		Keys []struct {
			PrivateKey string `json:"privateKey"`
			Passphrase string `json:"passphrase"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(exportFor(t, FormatKeys, Options{IncludeSecrets: true}), &private); err != nil {
		t.Fatal(err)
	}
	if private.Keys[0].PrivateKey != "PRIVATE" || private.Keys[0].Passphrase != "pa ss" {
		t.Fatalf("private key = %+v", private.Keys[0])
	}
}

func TestLogsExportsBothFamiliesDeterministically(t *testing.T) {
	jsonDoc := string(exportFor(t, FormatLogsJSON, Options{}))
	if strings.Count(jsonDoc, `"connectionLogs"`) != 1 || !strings.Contains(jsonDoc, `"history"`) {
		t.Fatalf("log document = %s", jsonDoc)
	}
	csvDoc := string(exportFor(t, FormatLogsCSV, Options{}))
	if !strings.HasPrefix(csvDoc, strings.Join(logsHeader, ",")+"\r\n") {
		t.Fatalf("log header = %q", csvDoc)
	}
	if !strings.Contains(csvDoc, "'=cmd") {
		t.Fatalf("history command was not guarded: %s", csvDoc)
	}
}

func TestEveryExportIsDeterministic(t *testing.T) {
	for _, format := range Formats() {
		first := exportFor(t, format, Options{IncludeSecrets: true})
		second := exportFor(t, format, Options{IncludeSecrets: true})
		if !bytes.Equal(first, second) {
			t.Errorf("%s is not deterministic", format)
		}
	}
}

func TestThemesExportCarriesTheActiveTheme(t *testing.T) {
	dataset := testDataset()
	dataset.ActiveTheme = "light"
	content, err := Export(dataset, FormatThemes, Options{ExportedAt: stamp})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), `"activeTheme": "light"`) {
		t.Fatalf("themes = %s", content)
	}
}
