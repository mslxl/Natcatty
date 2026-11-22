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
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
)

var stamp = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

const testTOTPURI = "otpauth://totp/Acme:deploy?secret=JBSWY3DPEHPK3PXP"

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
					KeyID:   "k1",
					HasTOTP: true,
					Proxy: &domainvault.ProxyConfig{
						Type: domainvault.ProxySOCKS5, Host: "127.0.0.1", Port: 1080,
						Username: "proxyuser", HasPassword: true,
					},
				},
				Password: "p@ss,1", KeyPath: "=C:/keys/id_ed25519",
				Passphrase: "pa ss", ProxyPassword: "p@ss",
				TOTP: testTOTPURI,
			},
			{
				Host: domainvault.Host{
					ID: "h1", Label: "Web", Hostname: "web.example.test",
					Port: 22, Username: "root", Protocol: domainvault.ProtocolTelnet,
					Tags: []string{"edge"}, OrderKey: 1000,
					StartupCommand: "cd /srv\n./run --watch",
					DirectProxy:    true,
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
	}
}

func exportFor(t *testing.T, format Format, opts Options) []byte {
	t.Helper()
	return exportDatasetFor(t, testDataset(), format, opts)
}

func exportDatasetFor(t *testing.T, d Dataset, format Format, opts Options) []byte {
	t.Helper()
	content, _ := exportDatasetForWithIssues(t, d, format, opts)
	return content
}

func exportDatasetForWithIssues(t *testing.T, d Dataset, format Format, opts Options) ([]byte, []Issue) {
	t.Helper()
	opts.ExportedAt = stamp
	content, issues, err := ExportWithIssues(d, format, opts)
	if err != nil {
		t.Fatalf("export %s: %v", format, err)
	}
	return content, issues
}

// jumpDataset is three SSH Hosts where Client reaches the network through
// Edge, which reaches it through Bastion.
func jumpDataset() Dataset {
	return Dataset{Hosts: []Host{
		{Host: domainvault.Host{
			ID: "bastion", Label: "Bastion", Hostname: "bastion.example.test",
			Port: 22, Username: "ops", Protocol: domainvault.ProtocolSSH, OrderKey: 1000,
		}},
		{Host: domainvault.Host{
			ID: "edge", Label: "Edge", Hostname: "edge.example.test",
			Port: 2200, Username: "deploy", Protocol: domainvault.ProtocolSSH, OrderKey: 2000,
			HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"bastion"}},
		}},
		{Host: domainvault.Host{
			ID: "client", Label: "Client", Hostname: "client.example.test",
			Port: 22, Protocol: domainvault.ProtocolSSH, OrderKey: 3000,
			HostChain: &domainvault.HostChain{
				HostIDs: []model.HostID{"edge", "bastion"},
			},
		}},
	}}
}

// csvCell finds the exported row for one Hostname and returns its value in the
// named column.
func csvCell(t *testing.T, rows [][]string, hostname, column string) string {
	t.Helper()
	columnAt := func(name string) int {
		for index, header := range rows[0] {
			if header == name {
				return index
			}
		}
		t.Fatalf("no %q column in %v", name, rows[0])
		return -1
	}
	hostIdx := columnAt("Hostname/IP")
	columnIdx := columnAt(column)
	for _, row := range rows[1:] {
		if hostIdx < len(row) && row[hostIdx] == hostname {
			if columnIdx >= len(row) {
				return ""
			}
			return row[columnIdx]
		}
	}
	t.Fatalf("no exported row for %q", hostname)
	return ""
}

// TestExportsPreserveAnUnsupportedTransport proves CSV writes the raw stored
// transport instead of a presentation label, so re-import rejects the record
// rather than silently turning it into plain SSH.
func TestExportsPreserveAnUnsupportedTransport(t *testing.T) {
	dataset := Dataset{Hosts: []Host{
		{Host: domainvault.Host{
			ID: "bad", Label: "bad", Hostname: "bad.example.test", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: "ssh2",
		}},
	}}
	csvText := string(exportDatasetFor(t, dataset, FormatCSV, Options{}))
	rows, issues := vaultimport.ParseCSV(csvText)
	if len(issues) != 0 {
		t.Fatalf("CSV parse issues = %+v", issues)
	}
	if got := csvCell(t, rows, "bad.example.test", "Transport"); got != "ssh2" {
		t.Fatalf("CSV Transport = %q, want the raw ssh2", got)
	}
	parsed := vaultimport.Parse(vaultimport.FormatCSV, csvText, vaultimport.Options{})
	if len(parsed.Records) != 0 {
		t.Fatalf("an unsupported transport was imported as %+v", parsed.Records)
	}
	if len(parsed.Issues) == 0 {
		t.Fatal("an unsupported transport was imported without an issue")
	}
}

func TestCSVUsesHeaderMarkedFieldsAndGuards(t *testing.T) {
	csvText := string(exportFor(t, FormatCSV, Options{IncludeSecrets: true}))
	if !strings.HasPrefix(csvText, "\xef\xbb\xbf") {
		t.Fatalf("CSV does not start with a UTF-8 BOM: %q", csvText[:min(len(csvText), 3)])
	}
	csvText = strings.TrimPrefix(csvText, "\xef\xbb\xbf")
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

// TestExportsCarryTheSSHTransport proves Mosh and ET leave an SSH Host as a
// transport field, not as a protocol, in both portable formats.
func TestExportsCarryTheSSHTransport(t *testing.T) {
	dataset := Dataset{Hosts: []Host{
		{Host: domainvault.Host{
			ID: "m", Label: "mosh", Hostname: "mosh.example.test", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: domainvault.TransportMosh,
		}},
		{Host: domainvault.Host{
			ID: "e", Label: "et", Hostname: "et.example.test", Port: 22,
			Protocol: domainvault.ProtocolSSH, Transport: domainvault.TransportET,
		}},
		{Host: domainvault.Host{
			ID: "p", Label: "plain", Hostname: "plain.example.test", Port: 22,
			Protocol: domainvault.ProtocolSSH,
		}},
	}}

	csvText := string(exportDatasetFor(t, dataset, FormatCSV, Options{}))
	rows, issues := vaultimport.ParseCSV(csvText)
	if len(issues) != 0 {
		t.Fatalf("CSV parse issues = %+v", issues)
	}
	for hostname, want := range map[string]string{
		"mosh.example.test":  "mosh",
		"et.example.test":    "et",
		"plain.example.test": "",
	} {
		if got := csvCell(t, rows, hostname, "Transport"); got != want {
			t.Errorf("%s CSV Transport = %q, want %q", hostname, got, want)
		}
		if got := csvCell(t, rows, hostname, "Protocol"); got != "ssh" {
			t.Errorf("%s CSV Protocol = %q, want ssh", hostname, got)
		}
	}
	parsedCSV := vaultimport.Parse(vaultimport.FormatCSV, csvText, vaultimport.Options{})
	if len(parsedCSV.Records) != 3 {
		t.Fatalf("parsed CSV records = %+v", parsedCSV.Records)
	}

	document := string(exportDatasetFor(t, dataset, FormatJSON, Options{}))
	parsedJSON := vaultimport.Parse(vaultimport.FormatNetcattyJSON, document, vaultimport.Options{})
	if len(parsedJSON.Records) != 3 {
		t.Fatalf("parsed JSON records = %+v, issues = %+v", parsedJSON.Records, parsedJSON.Issues)
	}
	for hostname, want := range map[string]string{
		"mosh.example.test":  "mosh",
		"et.example.test":    "et",
		"plain.example.test": "",
	} {
		found := false
		for _, record := range parsedJSON.Records {
			if record.Hostname != hostname {
				continue
			}
			found = true
			if record.Transport != want {
				t.Errorf("%s JSON transport = %q, want %q", hostname, record.Transport, want)
			}
			if record.Protocol != "ssh" {
				t.Errorf("%s JSON protocol = %q, want ssh", hostname, record.Protocol)
			}
		}
		if !found {
			t.Errorf("%s missing from JSON export", hostname)
		}
	}
	if strings.Contains(document, `"transport": ""`) {
		t.Fatalf("plain SSH wrote an empty transport:\n%s", document)
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

// TestCSVWritesAProxyJumpChainBeforeLocalShell pins the column position and
// the OpenSSH hop shape: port 22 is omitted and the first hop is closest to
// the client.
func TestCSVWritesAProxyJumpChainBeforeLocalShell(t *testing.T) {
	content, warnings := exportDatasetForWithIssues(t, jumpDataset(), FormatCSV, Options{})
	if len(warnings) != 0 {
		t.Fatalf("a fully representable chain warned: %+v", warnings)
	}
	rows, issues := vaultimport.ParseCSV(string(content))
	if len(issues) != 0 {
		t.Fatalf("CSV parse issues = %+v", issues)
	}
	jumpIdx, shellIdx := -1, -1
	for index, header := range rows[0] {
		switch header {
		case "ProxyJump":
			jumpIdx = index
		case "LocalShell":
			shellIdx = index
		}
	}
	if jumpIdx < 0 || shellIdx != jumpIdx+1 {
		t.Fatalf("ProxyJump must sit immediately before LocalShell: %v", rows[0])
	}
	if got := csvCell(t, rows, "client.example.test", "ProxyJump"); got !=
		"deploy@edge.example.test:2200,ops@bastion.example.test" {
		t.Fatalf("client ProxyJump = %q", got)
	}
	if got := csvCell(t, rows, "bastion.example.test", "ProxyJump"); got != "" {
		t.Fatalf("a Host without a chain wrote %q", got)
	}
}

// TestCSVRoundTripsATwoHopJumpChain proves an export imports back as the same
// chain, both for the client and for an intermediate hop.
func TestCSVRoundTripsATwoHopJumpChain(t *testing.T) {
	csvText := string(exportDatasetFor(t, jumpDataset(), FormatCSV, Options{}))
	parsed := vaultimport.Parse(vaultimport.FormatCSV, csvText, vaultimport.Options{})
	if len(parsed.Issues) != 0 {
		t.Fatalf("issues = %+v", parsed.Issues)
	}
	if len(parsed.Records) != 3 {
		t.Fatalf("records = %d: %+v", len(parsed.Records), parsed.Records)
	}
	client := parsed.Records[2]
	if client.Hostname != "client.example.test" || len(client.JumpRefs) != 2 {
		t.Fatalf("client = %+v", client)
	}
	if got := parsed.Records[client.JumpRefs[0]].Hostname; got != "edge.example.test" {
		t.Fatalf("first hop = %q, want Edge", got)
	}
	if got := parsed.Records[client.JumpRefs[1]].Hostname; got != "bastion.example.test" {
		t.Fatalf("second hop = %q, want Bastion", got)
	}
	if refs := parsed.Records[1].JumpRefs; len(refs) != 1 || refs[0] != 0 {
		t.Fatalf("edge JumpRefs = %v, want [0]", refs)
	}
}

// TestCSVOmitsAChainWithAnUnrepresentableHop proves a chain is written whole
// or not at all: a telnet hop cannot carry a session, so the client keeps an
// empty cell instead of a shortened chain, and the export names the Host and
// the hop that were dropped.
func TestCSVOmitsAChainWithAnUnrepresentableHop(t *testing.T) {
	dataset := jumpDataset()
	dataset.Hosts = append(dataset.Hosts, Host{Host: domainvault.Host{
		ID: "telnet", Label: "Telnet", Hostname: "telnet.example.test",
		Port: 23, Protocol: domainvault.ProtocolTelnet, OrderKey: 4000,
	}})
	dataset.Hosts[2].Host.HostChain = &domainvault.HostChain{
		HostIDs: []model.HostID{"edge", "telnet"},
	}
	content, warnings := exportDatasetForWithIssues(t, dataset, FormatCSV, Options{})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want one unrepresentable-hop warning", warnings)
	}
	if want := (Issue{Host: "Client", Code: IssueJumpHopUnrepresentable, Hop: "Telnet"}); warnings[0] != want {
		t.Fatalf("warning = %+v, want %+v", warnings[0], want)
	}
	rows, issues := vaultimport.ParseCSV(string(content))
	if len(issues) != 0 {
		t.Fatalf("CSV parse issues = %+v", issues)
	}
	if got := csvCell(t, rows, "client.example.test", "ProxyJump"); got != "" {
		t.Fatalf("a partly representable chain was written as %q", got)
	}
}

// TestCSVWarnsAboutAJumpHopMissingFromTheExport proves a chain referencing a
// Host outside the selected export is reported, not silently blanked.
func TestCSVWarnsAboutAJumpHopMissingFromTheExport(t *testing.T) {
	dataset := jumpDataset()
	dataset.Hosts[2].Host.HostChain = &domainvault.HostChain{
		HostIDs: []model.HostID{"edge", "ghost"},
	}
	content, warnings := exportDatasetForWithIssues(t, dataset, FormatCSV, Options{})
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v, want one missing-hop warning", warnings)
	}
	if want := (Issue{Host: "Client", Code: IssueJumpHopMissing, Hop: "ghost"}); warnings[0] != want {
		t.Fatalf("warning = %+v, want %+v", warnings[0], want)
	}
	rows, issues := vaultimport.ParseCSV(string(content))
	if len(issues) != 0 {
		t.Fatalf("CSV parse issues = %+v", issues)
	}
	if got := csvCell(t, rows, "client.example.test", "ProxyJump"); got != "" {
		t.Fatalf("a chain with a missing hop was written as %q", got)
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
	if parsed.Records[1].TOTP != testTOTPURI {
		t.Fatalf("TOTP lost: %+v", parsed.Records[1])
	}
	// The portable document carries the startup command (newlines included)
	// and an explicit direct-proxy choice; the importer Record must represent
	// both instead of dropping them.
	if !strings.Contains(string(document), `"startupCommand"`) ||
		!strings.Contains(string(document), `"directProxy": true`) {
		t.Fatalf("document does not carry startupCommand/directProxy:\n%s", document)
	}
	host := parsed.Records[0]
	if host.StartupCommand != "cd /srv\n./run --watch" || !host.DirectProxy {
		t.Fatalf("startup command / direct proxy lost: %+v", host)
	}
}

func TestJSONWithoutSecretsOmitsKeyMaterial(t *testing.T) {
	document := string(exportFor(t, FormatJSON, Options{}))
	if strings.Contains(document, "PRIVATE") || strings.Contains(document, `"password"`) ||
		strings.Contains(document, `"totp"`) {
		t.Fatalf("secret leaked:\n%s", document)
	}
	parsed := vaultimport.Parse(vaultimport.FormatNetcattyJSON, document, vaultimport.Options{})
	if len(parsed.Records) != 2 || parsed.Records[1].KeyMaterial != "" ||
		parsed.Records[1].TOTP != "" {
		t.Fatalf("non-destructive parse = %+v", parsed.Records)
	}
}

// TestOrdinaryNetworkProfileDocumentMatchesTheDomainRecord pins the portable
// Network Profile shape to the domain record. The export document uses its own
// struct to add optional secret fields, so a new domain field must not silently
// disappear from an ordinary export.
func TestOrdinaryNetworkProfileDocumentMatchesTheDomainRecord(t *testing.T) {
	profile := domainvault.NetworkProfile{
		ID: "net-1", Label: "Everything", Kind: domainvault.NetworkKindWireGuard,
		Tailscale: &domainvault.TailscaleNetwork{
			Hostname: "node", ControlURL: "https://control.example.test",
			Ephemeral: true, Tags: []string{"tag:server"}, HasAuthKey: true,
		},
		WireGuard: &domainvault.WireGuardNetwork{
			Addresses: []string{"10.0.0.2/32"}, DNS: []string{"10.0.0.1"},
			MTU: 1420, ListenPort: 51820, HasPrivateKey: true,
			Peers: []domainvault.WireGuardPeer{{
				ID: "peer-1", PublicKey: "peer-key", Endpoint: "vpn.example.test:51820",
				AllowedIPs: []string{"10.0.0.0/24"}, PersistentKeepalive: 25,
				HasPresharedKey: true,
			}},
		},
		OrderKey: 1000, CreatedAt: stamp, UpdatedAt: stamp.Add(time.Minute),
	}

	documents := networkProfileDocuments(
		[]domainvault.NetworkProfile{profile}, nil, false)
	if len(documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(documents))
	}
	domainJSON, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	documentJSON, err := json.Marshal(documents[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(documentJSON) != string(domainJSON) {
		t.Fatalf("ordinary export changed the Network Profile shape:\n got %s\nwant %s",
			documentJSON, domainJSON)
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

// TestLogsExportConnectionLogsWithoutCommandHistory pins spec O5: Connection
// Logs are a portable export family, but Command History entries never are.
func TestLogsExportConnectionLogsWithoutCommandHistory(t *testing.T) {
	jsonDoc := string(exportFor(t, FormatLogsJSON, Options{}))
	if strings.Count(jsonDoc, `"connectionLogs"`) != 1 || strings.Contains(jsonDoc, `"history"`) {
		t.Fatalf("log document = %s", jsonDoc)
	}
	csvDoc := string(exportFor(t, FormatLogsCSV, Options{}))
	if !strings.HasPrefix(csvDoc, "\xef\xbb\xbf") {
		t.Fatalf("logs CSV does not start with a UTF-8 BOM: %q", csvDoc[:min(len(csvDoc), 3)])
	}
	csvDoc = strings.TrimPrefix(csvDoc, "\xef\xbb\xbf")
	if !strings.HasPrefix(csvDoc, strings.Join(logsHeader, ",")+"\r\n") {
		t.Fatalf("log header = %q", csvDoc)
	}
	if strings.Contains(csvDoc, ",history,") || strings.Contains(csvDoc, "'=cmd") {
		t.Fatalf("logs CSV carried a command-history row: %s", csvDoc)
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

// TestCSVKeepsVNCConfigAndCredentialByteExact proves the VNC configuration
// cell keeps its marker payload and the VNC password is never formula-guarded,
// while the generic Password cell stays empty for a VNC Host.
func TestCSVKeepsVNCConfigAndCredentialByteExact(t *testing.T) {
	config := domainvault.VNCConfig{
		DisplayMode: domainvault.VNCPort, FitMode: domainvault.VNCClip,
		Shared: false, CursorMode: domainvault.VNCCursorServer,
	}
	dataset := Dataset{Hosts: []Host{{
		Host: domainvault.Host{
			ID: "vnc", Label: "Desk", Hostname: "desk.example.test",
			Port: 5901, Protocol: domainvault.ProtocolVNC, OrderKey: 1000,
			HasPassword: true, HasVNCPassword: true, VNC: &config,
		},
		Password: "ssh-slot-value", VNCPassword: "=1+2",
	}}}
	content := exportDatasetFor(t, dataset, FormatCSV, Options{IncludeSecrets: true})
	rows, issues := vaultimport.ParseCSV(string(content))
	if len(issues) != 0 {
		t.Fatalf("parse issues = %+v", issues)
	}
	configCell := csvCell(t, rows, "desk.example.test", "VNC")
	wantConfig := vaultimport.MarkerVNC +
		"displayMode=port;fitMode=clip;viewOnly=false;shared=false;" +
		"cursorMode=server;plaintextWarningAcknowledged=false"
	if configCell != wantConfig {
		t.Fatalf("VNC cell = %q, want %q", configCell, wantConfig)
	}
	if got := csvCell(t, rows, "desk.example.test", "Password"); got != "" {
		t.Fatalf("Password cell = %q, want empty for a VNC Host", got)
	}
	if got := csvCell(t, rows, "desk.example.test", "VNCPassword"); got != "=1+2" {
		t.Fatalf("VNCPassword cell = %q, want the raw credential", got)
	}

	parsed := vaultimport.Parse(vaultimport.FormatCSV, string(content), vaultimport.Options{})
	if len(parsed.Records) != 1 {
		t.Fatalf("records = %+v", parsed.Records)
	}
	record := parsed.Records[0]
	if record.VNCPassword != "=1+2" || record.Password != "" {
		t.Fatalf("parsed credentials = password %q, vnc %q", record.Password, record.VNCPassword)
	}
	if record.VNC == nil || record.VNC.Shared || record.VNC.FitMode != "clip" {
		t.Fatalf("parsed VNC = %+v", record.VNC)
	}
}
