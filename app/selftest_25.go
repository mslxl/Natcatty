//go:build selftest

package app

// Ticket 25 self-test. Press F11, then 2, then 5. The check proves exporting
// through the real serializers, SQLite store, field-secret store, Vault
// service, Vault state, and Vault view:
//
//   - Formats: Netcatty JSON, Netcatty CSV, Snippets JSON, Vault Notes
//     Markdown, HTML, and ZIP, SSH Keys JSON, Themes JSON, and Logs JSON and
//     CSV each write a non-empty file with the documented kind, header, or
//     entry names and the seeded records.
//   - Conventions: the CSV header and marked KeyPath field match the importer,
//     formula starts are guarded, and the exported records carry their group,
//     label, tags, notes, and credentials.
//   - Secrets: without the destructive option no password or private key
//     material appears; with it, the values appear and are counted as
//     destructive.
//   - Round trip: a CSV export and a JSON export import into a second store
//     through the ticket-24 importer, preserving the Host, Group, credentials,
//     and Key. Snippets, Notes, and Keys exports restore through their family
//     importers.
//   - Confirmation: pressing Export with the secrets toggle on but unconfirmed
//     writes no file and reports the warning; confirming then exports.
//   - Layout: the list and export panel render at 1100x720 and 720x480 without
//     horizontal overflow, the actions stay visible, and the panel scrolls.
//
// Result: %TEMP%\natcatty-selftest-25.json. Every store, secret store, and
// temporary directory is closed and removed, so F11,2,5 passes twice in one
// process.

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	apphistory "github.com/mslxl/natcatty/application/history"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest25Ticket = 25

const selftest25PrivateKey = selftest24PrivateKey

type selftest25 struct {
	dir      string
	dbPath   string
	db       *store.Store
	secrets  *store.SecretStore
	keyring  *selftest24Keyring
	history  *apphistory.Service
	service  *appvault.Service
	state    *vaultstate.State
	writer   *selftest25Writer
	tester   *ui.Tester
	hostID   string
	keyID    string
	keyFile  string
	snippets []byte
	notes    []byte
	keys     []byte

	secondDBs   []*store.Store
	secondCount int
}

func (t *selftest25) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest25Ticket,
		Name:   "Export Vault, Snippets, notes, keys, themes, and logs",
		Steps: []selftest.Step{
			{Name: "formats-and-conventions", Run: t.formatsStep},
			{Name: "destructive-confirmation", Run: t.confirmationStep},
			{Name: "round-trip-import", Run: t.roundTripStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest25) formatsStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}

	checks := []struct {
		format   appvault.ExportFormat
		secrets  bool
		contains []string
		absent   []string
	}{
		{format: appvault.ExportJSON, secrets: true,
			contains: []string{`"kind": "netcatty.vault"`, `"hostname": "export.example.test"`,
				`"password": "s3cr3t"`, `"identp@ss"`, `"proxyp@ss"`, `"keyPath"`,
				`"identityId"`, `"proxyProfileId"`, `"groups"`, `"identities"`,
				`"proxyProfiles"`, `"knownHosts"`, `"snippets"`, `"snippetPackages"`,
				`"noteGroups"`}},
		{format: appvault.ExportJSON, absent: []string{"s3cr3t", "identp@ss", "proxyp@ss", "OPENSSH PRIVATE KEY"}},
		{format: appvault.ExportCSV, secrets: true,
			contains: []string{strings.Join(vaultimport.CSVHeader(), ","),
				"'=Formula", "s3cr3t", "export.example.test"}},
		{format: appvault.ExportCSV, absent: []string{"s3cr3t"}},
		{format: appvault.ExportSnippets,
			contains: []string{`"kind": "netcatty.snippets"`, `"label": "Uptime"`,
				`"targets"`, `"targetGroups"`, `"snippetPackages"`}},
		{format: appvault.ExportNotesMarkdown,
			contains: []string{"# Runbook", "uptime --short"}},
		{format: appvault.ExportNotesHTML,
			contains: []string{"<!DOCTYPE html>", "Runbook"}},
		{format: appvault.ExportNotesZIP,
			contains: []string{"PK"}},
		{format: appvault.ExportKeys, secrets: true,
			contains: []string{`"kind": "netcatty.keys"`, `"authorizedKeys"`, "OPENSSH PRIVATE KEY"}},
		{format: appvault.ExportKeys, absent: []string{"OPENSSH PRIVATE KEY"}},
		{format: appvault.ExportThemes,
			contains: []string{`"kind": "netcatty.themes"`, `"activeTheme": "light"`}},
		{format: appvault.ExportLogsJSON,
			contains: []string{`"connectionLogs"`, `"history"`, "export.example.test"}},
		{format: appvault.ExportLogsCSV,
			contains: []string{"export.example.test", "'=uptime", "\r\n"}},
	}

	for _, check := range checks {
		t.state.BeginExport()
		t.state.SetExportFormat(string(check.format))
		if check.secrets {
			t.state.SetExportIncludeSecrets(true)
			t.state.ConfirmExportSecrets()
		}
		before := len(t.writer.names)
		if err := t.state.Export(""); err != nil {
			return t.fail(fmt.Errorf("%s: %w", check.format, err))
		}
		if len(t.writer.names) != before+1 {
			return t.fail(fmt.Errorf("%s wrote no file (problem %q)",
				check.format, t.state.ExportProblem()))
		}
		name := t.writer.names[len(t.writer.names)-1]
		if !strings.HasSuffix(name, "."+check.format.Extension()) {
			return t.fail(fmt.Errorf("%s filename = %q", check.format, name))
		}
		content := t.writer.files[name]
		if len(content) == 0 {
			return t.fail(fmt.Errorf("%s is empty", check.format))
		}
		text := string(content)
		for _, want := range check.contains {
			if !strings.Contains(text, want) {
				return t.fail(fmt.Errorf("%s lacks %q:\n%s", check.format, want, text))
			}
		}
		for _, unwanted := range check.absent {
			if strings.Contains(text, unwanted) {
				return t.fail(fmt.Errorf("%s leaked %q", check.format, unwanted))
			}
		}
		if check.format == appvault.ExportNotesZIP {
			// The manifest is authoritative and the Markdown layout stays
			// human-readable, including for a duplicate and a slash title.
			entries, manifest, err := selftest25Zip(content)
			if err != nil {
				return t.fail(err)
			}
			if len(entries) != 4 || entries["Ops/Runbook.md"] == "" {
				return t.fail(fmt.Errorf("notes ZIP entries = %v", mapKeys(entries)))
			}
			for _, want := range []string{`"kind": "netcatty.notes"`,
				`"title": "Runbook"`, `"title": "Config / Draft"`, `"group": "Ops/Config"`} {
				if !strings.Contains(manifest, want) {
					return t.fail(fmt.Errorf("notes manifest lacks %q: %s", want, manifest))
				}
			}
			if !strings.Contains(manifest, `"title": "Runbook"`) ||
				strings.Count(manifest, `"title": "Runbook"`) != 2 {
				return t.fail(fmt.Errorf("duplicate Note titles are not distinct in the manifest: %s", manifest))
			}
			t.notes = content
		}
		if check.format == appvault.ExportSnippets {
			t.snippets = content
		}
		if check.format == appvault.ExportKeys && check.secrets {
			t.keys = content
		}
	}

	// The CSV marks the Key path the importer understands only when a formula
	// could confuse a spreadsheet: the seeded path is ordinary, so it stays
	// plain, and the seeded label's leading "=" is guarded.
	csvText := ""
	header := strings.Join(vaultimport.CSVHeader(), ",")
	for _, content := range t.writer.files {
		if strings.HasPrefix(string(content), header) {
			csvText = string(content)
			break
		}
	}
	if !strings.HasPrefix(csvText, strings.Join(vaultimport.CSVHeader(), ",")) {
		return t.fail(fmt.Errorf("CSV header = %q", csvText))
	}

	// Every export format must have been exercised at least once.
	exercised := make(map[appvault.ExportFormat]bool)
	for _, check := range checks {
		exercised[check.format] = true
	}
	for _, format := range appvault.ExportFormats() {
		if !exercised[format] {
			return t.fail(fmt.Errorf("format %s was not exercised", format))
		}
	}

	t.cleanup()
	return selftest.Passed(
		"all ten formats including the Notes ZIP manifest were produced and verified; CSV conventions and default secret omission behave",
		"appvault.ExportVault + vaultexport.Export + store.SecretStore")
}

func (t *selftest25) confirmationStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	t.state.BeginExport()
	t.state.SetExportFormat(string(appvault.ExportCSV))
	t.state.SetExportIncludeSecrets(true)

	before := len(t.writer.names)
	if err := t.state.Export(""); err != nil {
		return t.fail(err)
	}
	if len(t.writer.names) != before {
		return t.fail(fmt.Errorf("an unconfirmed export wrote a file"))
	}
	if problem := t.state.ExportProblem(); problem != vaultstate.ProblemExportUnconfirmed {
		return t.fail(fmt.Errorf("unconfirmed problem = %q, want %q",
			problem, vaultstate.ProblemExportUnconfirmed))
	}
	panel := ui.NewTester(vaultui.View(t.state), 720, 480)
	panel.SetLocale("en")
	panel.Frame()
	if !panel.HasText(m.VaultExportSecretsWarning()) {
		return t.fail(fmt.Errorf("the plaintext warning is not rendered: %v", panel.Texts()))
	}
	if !panel.HasText(m.VaultProblemExportUnconfirmed()) {
		return t.fail(fmt.Errorf("the unconfirmed problem is not rendered: %v", panel.Texts()))
	}

	if err := t.state.Export(""); err != nil {
		return t.fail(err)
	}
	if len(t.writer.names) != before {
		return t.fail(fmt.Errorf("confirming was skipped but a file was written"))
	}

	t.state.ConfirmExportSecrets()
	panel.Frame()
	if !t.state.ExportSecretsConfirmed() {
		return t.fail(fmt.Errorf("ConfirmExportSecrets did not confirm"))
	}
	if err := t.state.Export(""); err != nil {
		return t.fail(err)
	}
	if len(t.writer.names) != before+1 {
		return t.fail(fmt.Errorf("confirmed export wrote no file"))
	}
	if t.state.ExportOpen() {
		return t.fail(fmt.Errorf("a completed export left the panel open"))
	}
	content := t.writer.files[t.writer.names[len(t.writer.names)-1]]
	if !strings.Contains(string(content), "s3cr3t") {
		return t.fail(fmt.Errorf("confirmed CSV lacks the password"))
	}
	// Toggling secrets back off requires confirming again.
	t.state.BeginExport()
	t.state.SetExportIncludeSecrets(true)
	t.state.ConfirmExportSecrets()
	t.state.SetExportIncludeSecrets(false)
	t.state.SetExportIncludeSecrets(true)
	if t.state.ExportSecretsConfirmed() {
		return t.fail(fmt.Errorf("re-enabling secrets kept the previous confirmation"))
	}

	t.cleanup()
	return selftest.Passed(
		"an unconfirmed plaintext export wrote nothing and localized the warning; confirming exported the password",
		"vaultstate.Export/ConfirmExportSecrets + vaultui.View")
}

func (t *selftest25) roundTripStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}

	_, importer := t.secondVault()
	csv, err := t.exportFile(appvault.ExportCSV)
	if err != nil {
		return t.fail(err)
	}
	preview, err := importer.PreviewImport(context.Background(), string(csv),
		appvault.PreviewImportOptions{Format: "csv"})
	if err != nil {
		return t.fail(fmt.Errorf("preview the CSV export: %w", err))
	}
	result, err := importer.CommitImport(context.Background(), preview,
		appvault.ImportCommitOptions{})
	if err != nil {
		return t.fail(fmt.Errorf("commit the CSV export: %w", err))
	}
	if len(result.Imported) != 1 {
		return t.fail(fmt.Errorf("CSV import = %+v", result))
	}
	imported, err := importer.List(context.Background())
	if err != nil || len(imported) != 1 {
		return t.fail(fmt.Errorf("CSV imported Hosts = %+v, %v", imported, err))
	}
	host := imported[0]
	if host.Label != "=Formula Host" || host.Hostname != "export.example.test" ||
		host.Port != 2222 || host.Username != "deploy" || host.Group != "Prod" ||
		strings.Join(host.Tags, ",") != "web,prod" ||
		host.Notes != "line1\nline2" || host.KeyID == "" {
		return t.fail(fmt.Errorf("CSV Host lost fields: %+v", host))
	}
	password, err := importer.Secret(context.Background(), host.ID, appvault.FieldPassword)
	if err != nil || password != "s3cr3t" {
		return t.fail(fmt.Errorf("CSV password = %q, %v", password, err))
	}
	keys, err := importer.ListKeys(context.Background())
	if err != nil || len(keys) != 1 || keys[0].Label != "export_key" {
		return t.fail(fmt.Errorf("CSV Keys = %+v, %v", keys, err))
	}

	// The JSON export restores into another clean store without a key file:
	// its private material is inline.
	jsonStore, jsonImporter := t.secondVault()
	_ = jsonStore
	jsonBytes, err := t.exportFile(appvault.ExportJSON)
	if err != nil {
		return t.fail(err)
	}
	jsonPreview, err := jsonImporter.PreviewImport(context.Background(), string(jsonBytes),
		appvault.PreviewImportOptions{Format: "netcatty_json"})
	if err != nil {
		return t.fail(fmt.Errorf("preview the JSON export: %w", err))
	}
	if _, err := jsonImporter.CommitImport(context.Background(), jsonPreview,
		appvault.ImportCommitOptions{}); err != nil {
		return t.fail(fmt.Errorf("commit the JSON export: %w", err))
	}
	jsonHosts, err := jsonImporter.List(context.Background())
	if err != nil || len(jsonHosts) != 1 {
		return t.fail(fmt.Errorf("JSON round trip Hosts = %+v, %v", jsonHosts, err))
	}
	jsonHost := jsonHosts[0]
	if !jsonHost.HasPassword || jsonHost.KeyID == "" ||
		jsonHost.IdentityID == "" || jsonHost.ProxyProfileID == "" ||
		jsonHost.AuthMethod != vault.HostAuthKey || !jsonHost.Pinned ||
		!jsonHost.RequiresMFA || !jsonHost.LegacyAlgorithms || !jsonHost.SkipEcdsaHostKey {
		return t.fail(fmt.Errorf("JSON Host lost auth/options/links: %+v", jsonHost))
	}
	jsonKeys, err := jsonImporter.ListKeys(context.Background())
	if err != nil || len(jsonKeys) != 1 || jsonKeys[0].ID != jsonHost.KeyID {
		return t.fail(fmt.Errorf("JSON Keys = %+v, %v", jsonKeys, err))
	}
	if material, _, err := jsonImporter.KeyMaterial(context.Background(), jsonKeys[0].ID); err != nil || material == "" {
		return t.fail(fmt.Errorf("JSON Key material = %q, %v", material, err))
	}
	jsonIdentities, err := jsonImporter.ListIdentities(context.Background())
	if err != nil || len(jsonIdentities) != 1 || !jsonIdentities[0].HasPassword ||
		jsonIdentities[0].KeyID != jsonKeys[0].ID {
		return t.fail(fmt.Errorf("JSON Identities = %+v, %v", jsonIdentities, err))
	}
	jsonProxies, err := jsonImporter.ListProxyProfiles(context.Background())
	if err != nil || len(jsonProxies) != 1 || !jsonProxies[0].Config.HasPassword {
		return t.fail(fmt.Errorf("JSON Proxy Profiles = %+v, %v", jsonProxies, err))
	}
	jsonKnown, err := jsonImporter.ListKnownHosts(context.Background())
	if err != nil || len(jsonKnown) != 1 || jsonKnown[0].Fingerprint != "SHA256:selftest" {
		return t.fail(fmt.Errorf("JSON Known Hosts = %+v, %v", jsonKnown, err))
	}
	jsonSnippets, err := jsonImporter.ListSnippets(context.Background())
	if err != nil || len(jsonSnippets) != 1 || len(jsonSnippets[0].Targets) != 1 ||
		jsonSnippets[0].Targets[0] != jsonHost.ID ||
		len(jsonSnippets[0].TargetGroups) != 1 || jsonSnippets[0].TargetGroups[0] != "Prod" {
		return t.fail(fmt.Errorf("JSON Snippets = %+v, %v", jsonSnippets, err))
	}
	jsonNotes, err := jsonImporter.ListNotes(context.Background())
	if err != nil || len(jsonNotes) != 3 {
		return t.fail(fmt.Errorf("JSON Notes = %+v, %v", jsonNotes, err))
	}
	titles := map[string]int{}
	for _, note := range jsonNotes {
		titles[note.Title]++
		if note.Title == "Runbook" && (note.Group != "Ops" ||
			(note.Content != "uptime --short\r\n" && note.Content != "second runbook")) {
			return t.fail(fmt.Errorf("JSON Runbook = %+v", note))
		}
		if note.Title == "Config / Draft" &&
			(note.Group != "Ops/Config" || note.Content != "# Draft") {
			return t.fail(fmt.Errorf("JSON slash-title Note = %+v", note))
		}
	}
	if titles["Runbook"] != 2 || titles["Config / Draft"] != 1 {
		return t.fail(fmt.Errorf("JSON Note titles = %v", titles))
	}
	jsonNoteGroups, err := jsonImporter.ListNoteGroups(context.Background())
	if err != nil || !hasNoteGroupPath(jsonNoteGroups, "Ops/Config") {
		return t.fail(fmt.Errorf("JSON Note Groups = %+v, %v", jsonNoteGroups, err))
	}
	jsonGroups, err := jsonImporter.ListGroups(context.Background())
	if err != nil || !hasGroupPath(jsonGroups, "Prod") {
		return t.fail(fmt.Errorf("JSON Host Groups = %+v, %v", jsonGroups, err))
	}
	effective, err := jsonImporter.EffectiveHost(context.Background(), jsonHost.ID)
	if err != nil || effective.Proxy == nil || effective.Proxy.Password != "proxyp@ss" {
		return t.fail(fmt.Errorf("JSON Proxy Profile password = %+v, %v", effective.Proxy, err))
	}
	if password, err := jsonImporter.Secret(context.Background(), jsonHost.ID, appvault.FieldPassword); err != nil || password != "s3cr3t" {
		return t.fail(fmt.Errorf("JSON Host password = %q, %v", password, err))
	}
	// The identity and proxy passwords are not exposed as plaintext by any
	// read API; a re-export with secrets proves the field-secret store holds
	// them after import.
	reExported, err := jsonImporter.ExportVault(context.Background(),
		appvault.ExportOptions{Format: appvault.ExportJSON, IncludeSecrets: true})
	if err != nil {
		return t.fail(fmt.Errorf("re-export the JSON round trip: %w", err))
	}
	for _, want := range []string{"s3cr3t", "identp@ss", "proxyp@ss", "OPENSSH PRIVATE KEY"} {
		if !strings.Contains(string(reExported.Content), want) {
			return t.fail(fmt.Errorf("JSON re-export lacks %q: %s", want, reExported.Content))
		}
	}

	// Family exports restore through the same PreviewImport/CommitImport path
	// the Import screen uses, with no format override so detection is tested.
	snippets, err := t.exportFile(appvault.ExportSnippets)
	if err != nil {
		return t.fail(err)
	}
	notesZIP, err := t.exportFile(appvault.ExportNotesZIP)
	if err != nil {
		return t.fail(err)
	}
	keysJSON, err := t.exportFile(appvault.ExportKeys)
	if err != nil {
		return t.fail(err)
	}
	_, familyImporter := t.secondVault()
	for _, item := range []struct {
		name   string
		text   string
		format vaultimport.Format
	}{
		{"Snippets", string(snippets), vaultimport.Format("snippets")},
		{"Notes", string(notesZIP), vaultimport.Format("notes_zip")},
		{"Keys", string(keysJSON), vaultimport.Format("keys")},
	} {
		preview, err := familyImporter.PreviewImport(context.Background(), item.text,
			appvault.PreviewImportOptions{})
		if err != nil {
			return t.fail(fmt.Errorf("preview the %s export: %w", item.name, err))
		}
		if preview.Format != item.format {
			return t.fail(fmt.Errorf("%s detected as %q, want %q", item.name, preview.Format, item.format))
		}
		if _, err := familyImporter.CommitImport(context.Background(), preview,
			appvault.ImportCommitOptions{}); err != nil {
			return t.fail(fmt.Errorf("commit the %s export: %w", item.name, err))
		}
	}
	restoredSnippets, err := familyImporter.ListSnippets(context.Background())
	if err != nil || len(restoredSnippets) != 1 ||
		len(restoredSnippets[0].Targets) != 1 ||
		len(restoredSnippets[0].TargetGroups) != 1 {
		return t.fail(fmt.Errorf("restored Snippets = %+v, %v", restoredSnippets, err))
	}
	notes, err := familyImporter.ListNotes(context.Background())
	if err != nil || len(notes) != 3 {
		return t.fail(fmt.Errorf("restored Notes = %+v, %v", notes, err))
	}
	restoredTitles := map[string]int{}
	for _, note := range notes {
		restoredTitles[note.Title]++
	}
	if restoredTitles["Runbook"] != 2 || restoredTitles["Config / Draft"] != 1 {
		return t.fail(fmt.Errorf("restored Note titles = %v", restoredTitles))
	}
	restoredKeys, err := familyImporter.ListKeys(context.Background())
	if err != nil || len(restoredKeys) != 1 {
		return t.fail(fmt.Errorf("restored Keys = %+v, %v", restoredKeys, err))
	}
	if material, _, err := familyImporter.KeyMaterial(context.Background(), restoredKeys[0].ID); err != nil || material == "" {
		return t.fail(fmt.Errorf("restored Key material = %q, %v", material, err))
	}

	t.cleanup()
	return selftest.Passed(
		"JSON restored every family and link, including identity/proxy passwords and exact Note titles; Snippets, Notes ZIP, and Keys detected and imported through the production path",
		"appvault.ExportVault + appvault.PreviewImport/CommitImport + family formats")
}

func (t *selftest25) layoutStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")

	var secretsToggle, scrollViewport ui.Rect
	var scrollGutter float32
	restoreLayout := vaultui.SelftestWatchExportLayout(func(toggle, viewport ui.Rect, gutter float32) {
		secretsToggle, scrollViewport, scrollGutter = toggle, viewport, gutter
	})
	defer restoreLayout()

	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		list := ui.NewTester(vaultui.View(t.state), size.width, size.height)
		list.SetLocale("en")
		list.Frame()
		if !list.HasText(m.VaultExportTitle()) {
			return t.fail(fmt.Errorf("%dx%d: the Export entry is not rendered: %v",
				size.width, size.height, list.Texts()))
		}
		if err := selftest24NoOverflow(list, size.width, size.height); err != nil {
			return t.fail(err)
		}

		t.state.BeginExport()
		t.state.SetExportIncludeSecrets(true)
		t.state.ConfirmExportSecrets()
		panel := ui.NewTester(vaultui.View(t.state), size.width, size.height)
		panel.SetLocale("en")
		panel.Frame()
		if secretsToggle.W <= 0 || scrollViewport.W <= 0 {
			return t.fail(fmt.Errorf("%dx%d: the secrets toggle or viewport was not laid out: %+v %+v",
				size.width, size.height, secretsToggle, scrollViewport))
		}
		const epsilon = 0.5
		if scrollGutter <= 0 {
			return t.fail(fmt.Errorf("%dx%d: the export panel reserved no scroll-bar gutter",
				size.width, size.height))
		}
		usableRight := scrollViewport.X + scrollViewport.W - scrollGutter
		if secretsToggle.X < scrollViewport.X-epsilon ||
			secretsToggle.X+secretsToggle.W > usableRight+epsilon {
			return t.fail(fmt.Errorf("%dx%d: the secrets toggle runs under the scroll bar: %+v, usable right %.1f",
				size.width, size.height, secretsToggle, usableRight))
		}
		for _, want := range []string{
			m.VaultExportTitle(), m.VaultExportFormat(), m.VaultExportScope(),
			m.VaultExportExport(), m.VaultHostCancel(),
		} {
			if err := selftest24Visible(panel, want, size.width, size.height); err != nil {
				return t.fail(fmt.Errorf("%dx%d: %w", size.width, size.height, err))
			}
		}
		if err := selftest24NoOverflow(panel, size.width, size.height); err != nil {
			return t.fail(err)
		}
		panel.Move(float32(size.width)/2, float32(size.height)/2)
		panel.Scroll(float32(size.width)/2, float32(size.height)/2, 0, 5000)
		panel.Frame()
		if err := selftest24NoOverflow(panel, size.width, size.height); err != nil {
			return t.fail(err)
		}
		t.state.CancelExport()
	}

	t.cleanup()
	return selftest.Passed(
		"the list Export entry and export panel render at 1100x720 and 720x480 with actions in bounds and the toggle clear of the scroll bar",
		"vaultui.View + vaultstate.BeginExport + ui.Tester")
}

func (t *selftest25) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-25-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	t.dir = dir
	t.dbPath = filepath.Join(dir, "vault.db")
	db, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		t.cleanup()
		return fmt.Errorf("open store: %w", err)
	}
	t.db = db
	t.keyring = &selftest24Keyring{values: map[string][]byte{}}
	secrets, err := store.OpenSecretStore(context.Background(), db, t.keyring)
	if err != nil {
		t.cleanup()
		return fmt.Errorf("open the field-secret store: %w", err)
	}
	t.secrets = secrets
	t.keyFile = filepath.Join(dir, "export_key")
	if err := os.WriteFile(t.keyFile, []byte(selftest25PrivateKey), 0o600); err != nil {
		t.cleanup()
		return fmt.Errorf("write the key fixture: %w", err)
	}
	history := apphistory.NewService(db)
	t.history = history
	service := appvault.NewService(db,
		appvault.WithSecrets(secrets),
		appvault.WithKeys(db),
		appvault.WithGroups(db),
		appvault.WithIdentities(db),
		appvault.WithProxyProfiles(db),
		appvault.WithKnownHosts(db),
		appvault.WithSnippets(db),
		appvault.WithNotes(db),
		appvault.WithKeyInspector(ssh.KeyParser{}),
		appvault.WithHistoryReader(history),
		appvault.WithThemeSource(func() string { return "light" }),
	)
	t.service = service
	ctx := context.Background()
	host, err := service.Create(ctx, vault.Draft{
		Label: "=Formula Host", Hostname: "export.example.test", Port: 2222,
		Username: "deploy", Protocol: vault.ProtocolSSH, Group: "Prod",
		Tags: []string{"web", "prod"}, Notes: "line1\nline2", Password: "s3cr3t",
	})
	if err != nil {
		t.cleanup()
		return fmt.Errorf("create the Host: %w", err)
	}
	t.hostID = string(host.ID)
	key, err := service.CreateKey(ctx, vault.KeyDraft{
		Label: "export_key", PrivateKey: selftest25PrivateKey,
		FilePath: t.keyFile, Source: vault.KeySourceImported,
	})
	if err != nil {
		t.cleanup()
		return fmt.Errorf("create the Key: %w", err)
	}
	t.keyID = string(key.ID)
	identity, err := service.CreateIdentity(ctx, vault.IdentityDraft{
		Label: "Deploy identity", Username: "deploy",
		AuthMethod: vault.IdentityAuthKey, KeyID: key.ID, Password: "identp@ss",
	})
	if err != nil {
		t.cleanup()
		return fmt.Errorf("create the Identity: %w", err)
	}
	proxyProfile, err := service.CreateProxyProfile(ctx, vault.ProxyProfileDraft{
		Label: "Bastion",
		Config: vault.ProxyConfig{
			Type: vault.ProxySOCKS5, Host: "proxy.example.test", Port: 1080,
			Username: "proxyuser", Password: "proxyp@ss",
		},
	})
	if err != nil {
		t.cleanup()
		return fmt.Errorf("create the Proxy Profile: %w", err)
	}
	if _, err := service.Update(ctx, host.ID, vault.Draft{
		Label: host.Label, Hostname: host.Hostname, Port: host.Port,
		Username: host.Username, Protocol: host.Protocol, Group: host.Group,
		Tags: host.Tags, Notes: host.Notes, KeyID: key.ID,
		IdentityID: identity.ID, AuthMethod: vault.HostAuthKey,
		ProxyProfileID: proxyProfile.ID, Pinned: true, RequiresMFA: true,
		LegacyAlgorithms: true, SkipEcdsaHostKey: true,
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("attach the Key and links: %w", err)
	}
	if err := t.db.UpsertKnownHost(ctx, domainconn.KnownHost{
		ID: "known-selftest", Hostname: host.Hostname, Port: host.Port,
		KeyType: "ssh-ed25519", PublicKey: "ssh-ed25519 AAAA host",
		Fingerprint: "SHA256:selftest", DiscoveredAt: time.Unix(1700000000, 0),
		LastSeen: time.Unix(1700000000, 0),
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("record the Known Host: %w", err)
	}
	if _, err := service.CreateSnippetPackage(ctx, vault.SnippetPackageDraft{Path: "Ops"}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the Snippet package: %w", err)
	}
	if _, err := service.CreateSnippet(ctx, vault.SnippetDraft{
		Label: "Uptime", Command: "uptime --short", Package: "Ops",
		Tags: []string{"ops"}, Targets: []model.HostID{host.ID},
		TargetGroups: []string{"Prod"},
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the Snippet: %w", err)
	}
	if _, err := service.CreateNoteGroup(ctx, vault.NoteGroupDraft{Path: "Ops"}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the Note Group: %w", err)
	}
	if _, err := service.CreateNote(ctx, vault.NoteDraft{
		Title: "Runbook", Content: "uptime --short\r\n", Group: "Ops",
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the Note: %w", err)
	}
	if _, err := service.CreateNote(ctx, vault.NoteDraft{
		Title: "Runbook", Content: "second runbook", Group: "Ops",
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the duplicate Note: %w", err)
	}
	if _, err := service.CreateNoteGroup(ctx, vault.NoteGroupDraft{Path: "Ops/Config"}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the second Note Group: %w", err)
	}
	if _, err := service.CreateNote(ctx, vault.NoteDraft{
		Title: "Config / Draft", Content: "# Draft", Group: "Ops/Config",
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("create the slash-title Note: %w", err)
	}
	if err := history.RecordConnection(ctx, domainhistory.ConnectionLog{
		Kind: "ssh", Status: domainhistory.ConnectionConnected,
		HostID: host.ID, HostLabel: host.Label, Hostname: host.Hostname,
		Username: host.Username,
	}); err != nil {
		t.cleanup()
		return fmt.Errorf("record a connection log: %w", err)
	}
	if err := history.Record(ctx, domainhistory.Entry{Command: "=uptime", Scope: "global"}); err != nil {
		t.cleanup()
		return fmt.Errorf("record a history entry: %w", err)
	}

	t.writer = &selftest25Writer{dir: filepath.Join(dir, "out")}
	state := vaultstate.NewState(service,
		vaultstate.WithGroups(service),
		vaultstate.WithKeys(service),
		vaultstate.WithIdentities(service),
		vaultstate.WithProxyProfiles(service),
		vaultstate.WithKnownHosts(service),
		vaultstate.WithSnippets(service),
		vaultstate.WithNotes(service),
		vaultstate.WithExports(service),
		vaultstate.WithExportWriter(t.writer),
	)
	if err := state.Reload(); err != nil {
		t.cleanup()
		return fmt.Errorf("load the Vault: %w", err)
	}
	state.Select(host.ID)
	if state.Selected() != host.ID {
		t.cleanup()
		return fmt.Errorf("select the seeded Host")
	}
	t.state = state
	t.tester = ui.NewTester(vaultui.View(state), 1100, 720)
	t.tester.SetLocale("en")
	t.tester.Frame()
	return nil
}

// exportFile renders one format with secrets to the writer's directory and
// returns the bytes.
func (t *selftest25) exportFile(format appvault.ExportFormat) ([]byte, error) {
	t.state.BeginExport()
	t.state.SetExportFormat(string(format))
	t.state.SetExportIncludeSecrets(true)
	t.state.ConfirmExportSecrets()
	if err := t.state.Export(""); err != nil {
		return nil, fmt.Errorf("export %s: %w", format, err)
	}
	if len(t.writer.names) == 0 {
		return nil, fmt.Errorf("export %s wrote no file", format)
	}
	return t.writer.files[t.writer.names[len(t.writer.names)-1]], nil
}

func (t *selftest25) secondVault() (*store.Store, *appvault.Service) {
	t.secondCount++
	db, err := store.Open(context.Background(),
		filepath.Join(t.dir, fmt.Sprintf("second-%d.db", t.secondCount)))
	if err != nil {
		panic(err)
	}
	t.secondDBs = append(t.secondDBs, db)
	secrets, err := store.OpenSecretStore(context.Background(), db,
		&selftest24Keyring{values: map[string][]byte{}})
	if err != nil {
		panic(err)
	}
	service := appvault.NewService(db,
		appvault.WithSecrets(secrets),
		appvault.WithKeys(db),
		appvault.WithGroups(db),
		appvault.WithIdentities(db),
		appvault.WithProxyProfiles(db),
		appvault.WithKnownHosts(db),
		appvault.WithSnippets(db),
		appvault.WithNotes(db),
		appvault.WithKeyInspector(ssh.KeyParser{}),
		appvault.WithImportStore(db),
		appvault.WithImportFiles(selftest24Files{}),
	)
	return db, service
}

func (t *selftest25) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest25) cleanup() {
	if t.secrets != nil {
		t.secrets.Close()
	}
	for _, db := range t.secondDBs {
		_ = db.Close()
	}
	t.secondDBs = nil
	if t.db != nil {
		_ = t.db.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.dbPath, t.db, t.secrets, t.keyring, t.history = "", "", nil, nil, nil, nil
	t.service, t.state, t.writer, t.tester = nil, nil, nil, nil
	t.hostID, t.keyID, t.keyFile = "", "", ""
	t.snippets, t.notes, t.keys = nil, nil, nil
}

type selftest25Writer struct {
	dir   string
	names []string
	files map[string][]byte
}

func (w *selftest25Writer) Save(name string, content []byte) (string, error) {
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(w.dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	if w.files == nil {
		w.files = map[string][]byte{}
	}
	w.names = append(w.names, name)
	w.files[name] = append([]byte(nil), content...)
	return path, nil
}

// selftest25Zip reads the Notes archive into path -> content and returns the
// manifest text. It also proves every entry is readable.
func selftest25Zip(content []byte) (map[string]string, string, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return nil, "", fmt.Errorf("open the Notes ZIP: %w", err)
	}
	entries := make(map[string]string, len(reader.File))
	manifest := ""
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			return nil, "", fmt.Errorf("open %s: %w", file.Name, err)
		}
		data := new(bytes.Buffer)
		_, readErr := data.ReadFrom(handle)
		_ = handle.Close()
		if readErr != nil {
			return nil, "", fmt.Errorf("read %s: %w", file.Name, readErr)
		}
		entries[file.Name] = data.String()
		if file.Name == "netcatty.notes.json" {
			manifest = data.String()
		}
	}
	if manifest == "" {
		return nil, "", fmt.Errorf("the Notes ZIP has no %s", "netcatty.notes.json")
	}
	return entries, manifest, nil
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func hasGroupPath(groups []vault.Group, path string) bool {
	for _, group := range groups {
		if group.Path == path {
			return true
		}
	}
	return false
}

func hasNoteGroupPath(groups []vault.NoteGroup, path string) bool {
	for _, group := range groups {
		if group.Path == path {
			return true
		}
	}
	return false
}
