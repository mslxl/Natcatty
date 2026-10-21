//go:build selftest

package app

// Ticket 24 self-test. Press F11, then 2, then 4. The check proves external
// inventory import through the real parsers, SQLite store, field-secret
// store, Vault service, Vault state, and Vault view:
//
//   - Formats: the Netcatty CSV, PuTTY registry export, MobaXterm INI,
//     FinalShell JSON, SecureCRT sessions, and OpenSSH ssh_config fixtures
//     each preview and import. Passwords, proxy credentials, and referenced
//     keys go through the field-secret path; the database file never holds
//     their plaintext.
//   - CSV: the fixture carries a UTF-8 BOM, CRLF rows, quoted commas, quotes
//     and newlines, marked key/passphrase/proxy fields, a guardless formula,
//     an exporter-guarded formula, a duplicate merge key, and invalid row
//     values. The preview reports each invalid row by source row number.
//   - Conflicts: a second preview reports every existing merge key as a
//     conflict, nothing is written before Confirm, Skip leaves the Host
//     untouched, and Overwrite updates one Host without creating a duplicate.
//   - Persistence and open: a second store handle sees the Hosts, Groups, and
//     Keys; selecting an imported Host opens the real editor with its imported
//     values, and saving it updates the stored record.
//   - Layout: 60 rows cross the 50-row commit chunk boundary. At 1100x720 and
//     720x480 the list and import panel render without horizontal overflow,
//     the panel's actions stay visible, and its scroll reaches the last row.
//
// Result: %TEMP%\natcatty-selftest-24.json. Every store, secret store, and
// temporary directory is closed and removed, so F11,2,4 passes twice in one
// process.

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/store"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/vaultimport"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest24Ticket = 24

// selftest24LayoutRows crosses the Vault state's 50-row import chunk.
const selftest24LayoutRows = 60

//go:embed testdata/selftest24/*
var selftest24Fixtures embed.FS

// selftest24PrivateKey is a throwaway ed25519 key used to prove that imported
// IdentityFile references travel through the Key inspector and the secret
// store. It grants access to nothing.
const selftest24PrivateKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACD5fupEpzAFND0cnSL5zlPwo5ZvPXoQnl3wuCARbTv80QAAAJBzTjikc044
pAAAAAtzc2gtZWQyNTUxOQAAACD5fupEpzAFND0cnSL5zlPwo5ZvPXoQnl3wuCARbTv80Q
AAAEAdtyxmNJA3QK4YI93GzbpbxLT0L8f5rRhBk8mnA0ob7fl+6kSnMAU0PRydIvnOU/Cj
lm89ehCeXfC4IBFtO/zRAAAACnNlbGZ0ZXN0MjQBAgM=
-----END OPENSSH PRIVATE KEY-----
`

type selftest24 struct {
	dir     string
	dbPath  string
	db      *store.Store
	secrets *store.SecretStore
	keyring *selftest24Keyring
	service *appvault.Service
	state   *vaultstate.State
	picker  *selftest24Picker
	tester  *ui.Tester
}

func (t *selftest24) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest24Ticket,
		Name:   "Import from external tools and CSV",
		Steps: []selftest.Step{
			{Name: "formats-and-persistence", Run: t.formatsStep},
			{Name: "csv-preview-and-merge", Run: t.previewStep},
			{Name: "open-imported-host", Run: t.openStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

type selftest24Format struct {
	name     string
	format   vaultimport.Format
	total    int
	added    int
	conflict int
	dupes    int
	invalid  int
	imported int
	skipped  int
	groups   int
}

// selftest24Formats are the representative fixture per supported format and
// its expected preview and commit result against an empty Vault.
var selftest24Formats = []selftest24Format{
	{name: "netcatty.csv", format: vaultimport.FormatCSV,
		total: 6, added: 4, dupes: 1, invalid: 1, imported: 4, skipped: 1, groups: 4},
	{name: "putty.reg", format: vaultimport.FormatPuTTY,
		total: 3, added: 2, invalid: 1, imported: 2},
	{name: "mobaxterm.ini", format: vaultimport.FormatMobaXterm,
		total: 4, added: 3, invalid: 1, imported: 3, groups: 2},
	{name: "finalshell.json", format: vaultimport.FormatFinalShell,
		total: 1, added: 1, imported: 1},
	{name: "securecrt.ini", format: vaultimport.FormatSecureCRT,
		total: 3, added: 2, invalid: 1, imported: 2},
	{name: "ssh_config", format: vaultimport.FormatSSHConfig,
		total: 5, added: 5, imported: 5},
}

func (t *selftest24) formatsStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}

	for _, expected := range selftest24Formats {
		preview, _, err := t.chooseFixture(expected.name, expected.format)
		if err != nil {
			return t.fail(err)
		}
		if preview.Format != expected.format {
			return t.fail(fmt.Errorf("%s parsed as %q, want %q",
				expected.name, preview.Format, expected.format))
		}
		counts := preview.Counts
		if counts.Total != expected.total || counts.New != expected.added ||
			counts.Conflicts != expected.conflict || counts.Duplicates != expected.dupes ||
			counts.Invalid != expected.invalid {
			return t.fail(fmt.Errorf("%s counts = %+v, want total %d new %d conflicts %d duplicates %d invalid %d",
				expected.name, counts, expected.total, expected.added,
				expected.conflict, expected.dupes, expected.invalid))
		}
		if expected.name == "netcatty.csv" {
			if err := selftest24CheckCSVPreview(preview); err != nil {
				return t.fail(err)
			}
		}
		outcome, err := t.confirm()
		if err != nil {
			return t.fail(fmt.Errorf("%s: %w", expected.name, err))
		}
		if outcome.Imported != expected.imported ||
			outcome.Overwritten != 0 || outcome.Skipped != expected.skipped ||
			outcome.Groups != expected.groups {
			return t.fail(fmt.Errorf("%s outcome = %+v, want imported %d skipped %d groups %d",
				expected.name, outcome, expected.imported, expected.skipped, expected.groups))
		}
	}

	// Every referenced IdentityFile went through the Key inspector and the
	// field-secret store, so both fixtures created usable Keys.
	keys, err := t.service.ListKeys(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list imported Keys: %w", err))
	}
	if len(keys) != 2 {
		return t.fail(fmt.Errorf("imported %d Keys, want 2", len(keys)))
	}
	for _, key := range keys {
		if key.PublicKey == "" || !key.HasPrivateKey {
			return t.fail(fmt.Errorf("imported Key is not usable: %+v", key))
		}
	}

	hosts, err := t.service.List(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list imported Hosts: %w", err))
	}
	if len(hosts) != 17 {
		return t.fail(fmt.Errorf("imported %d Hosts, want 17", len(hosts)))
	}
	web, ok := selftest24Host(hosts, "10.0.0.10")
	if !ok {
		return t.fail(fmt.Errorf("the CSV Host 10.0.0.10 was not imported"))
	}
	if web.Label != `Web, "edge"` || web.Port != 2222 || web.Username != "deploy" ||
		web.Group != "Acme/Prod" || web.Notes != "First line\nSecond line" ||
		strings.Join(web.Tags, ",") != "web,prod" || web.KeyID == "" || !web.HasPassword {
		return t.fail(fmt.Errorf("CSV Host lost fields: %+v", web))
	}
	if web.Proxy == nil || web.Proxy.Type != vault.ProxySOCKS5 ||
		web.Proxy.Host != "127.0.0.1" || web.Proxy.Port != 1080 ||
		web.Proxy.Username != "proxyuser" || !web.Proxy.HasPassword {
		return t.fail(fmt.Errorf("CSV Host proxy = %+v", web.Proxy))
	}
	password, err := t.service.Secret(context.Background(), web.ID, appvault.FieldPassword)
	if err != nil || password != "s3cr3t" {
		return t.fail(fmt.Errorf("CSV password = %q, %v", password, err))
	}
	proxyPassword, err := t.service.Secret(context.Background(), web.ID, appvault.FieldProxyPassword)
	if err != nil || proxyPassword != "p@ss" {
		return t.fail(fmt.Errorf("CSV proxy password = %q, %v", proxyPassword, err))
	}

	formula, ok := selftest24Host(hosts, "dev.example.test")
	if !ok {
		return t.fail(fmt.Errorf("the formula CSV Host was not imported"))
	}
	if formula.Label != `=HYPERLINK("http://example.test")` {
		return t.fail(fmt.Errorf("formula label changed: %q", formula.Label))
	}
	if formula.Notes != "=SUM(A1:A2)" {
		return t.fail(fmt.Errorf("formula guard was not stripped: %q", formula.Notes))
	}
	sshKeyHost, ok := selftest24HostAtPort(hosts, "web1.example.test", 2222)
	if !ok || sshKeyHost.KeyID == "" || !sshKeyHost.UseSSHAgent {
		return t.fail(fmt.Errorf("ssh_config Key/agent fields were not imported: %+v", sshKeyHost))
	}
	app, ok := selftest24Host(hosts, "app.example.test")
	if !ok {
		return t.fail(fmt.Errorf("the ProxyJump target was not imported"))
	}
	bastion, ok := selftest24Host(hosts, "bastion.example.test")
	if !ok || app.HostChain == nil || len(app.HostChain.HostIDs) != 1 ||
		app.HostChain.HostIDs[0] != bastion.ID {
		return t.fail(fmt.Errorf("ProxyJump chain = %+v, bastion = %q",
			app.HostChain, bastion.ID))
	}

	// The credentials only live in the field-secret store. Reading the raw
	// database proves they never leaked into a Host record or key material.
	raw, err := os.ReadFile(t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("read the database: %w", err))
	}
	for _, secret := range []string{"s3cr3t", "p@ss", "pa ss"} {
		if bytes.Contains(raw, []byte(secret)) {
			return t.fail(fmt.Errorf("the database contains plaintext credential %q", secret))
		}
	}

	// A second handle sees every Host, Group, and Key, so import persisted
	// through SQLite rather than only the in-memory preview.
	second, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("reopen the store: %w", err))
	}
	reader := appvault.NewService(second, appvault.WithKeys(second), appvault.WithGroups(second))
	reopenedHosts, hostsErr := reader.List(context.Background())
	reopenedGroups, groupsErr := reader.ListGroups(context.Background())
	reopenedKeys, keysErr := reader.ListKeys(context.Background())
	_ = second.Close()
	if hostsErr != nil || groupsErr != nil || keysErr != nil {
		return t.fail(fmt.Errorf("read the reopened Vault: %v / %v / %v",
			hostsErr, groupsErr, keysErr))
	}
	if len(reopenedHosts) != 17 || len(reopenedGroups) != 6 || len(reopenedKeys) != 2 {
		return t.fail(fmt.Errorf("reopened Vault has %d Hosts, %d Groups, %d Keys; want 17, 6, 2",
			len(reopenedHosts), len(reopenedGroups), len(reopenedKeys)))
	}

	t.cleanup()
	return selftest.Passed(
		"six formats imported 17 Hosts, 6 Groups, and 2 Keys; CSV BOM, CRLF, quoting, marked fields, and formulas survived; plaintext stayed out of SQLite",
		"vaultimport.Parse + appvault.Service + store.Store + store.SecretStore")
}

func (t *selftest24) previewStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	if _, _, err := t.chooseFixture("netcatty.csv", vaultimport.FormatCSV); err != nil {
		return t.fail(err)
	}
	if _, err := t.confirm(); err != nil {
		return t.fail(err)
	}
	before, err := t.countHosts()
	if err != nil {
		return t.fail(err)
	}
	if before != 4 {
		return t.fail(fmt.Errorf("imported %d Hosts, want 4", before))
	}

	// The same file now reports four conflicts and the same in-file duplicate.
	preview, _, err := t.chooseFixture("netcatty.csv", vaultimport.FormatCSV)
	if err != nil {
		return t.fail(err)
	}
	if preview.Counts.Total != 6 || preview.Counts.New != 0 ||
		preview.Counts.Conflicts != 4 || preview.Counts.Duplicates != 1 ||
		preview.Counts.Invalid != 1 {
		return t.fail(fmt.Errorf("conflict preview = %+v", preview.Counts))
	}
	for index := 0; index < 4; index++ {
		row := preview.Rows[index]
		if row.Status != appvault.ImportStatusConflict || row.Existing == "" ||
			row.ExistingLabel == "" {
			return t.fail(fmt.Errorf("row %d is not a described conflict: %+v", index, row))
		}
	}
	if preview.Rows[4].Status != appvault.ImportStatusDuplicate {
		return t.fail(fmt.Errorf("duplicate row status = %q", preview.Rows[4].Status))
	}
	for _, want := range []struct {
		code vaultimport.IssueCode
		row  int
	}{
		{vaultimport.IssueInvalidHostname, 5},
		{vaultimport.IssueInvalidPort, 6},
		{vaultimport.IssueDuplicateInFile, 7},
	} {
		if !selftest24HasIssue(preview.Issues, want.code, want.row) {
			return t.fail(fmt.Errorf("preview does not report %s on row %d: %+v",
				want.code, want.row, preview.Issues))
		}
	}

	// A preview never writes, and neither does a second handle see a change.
	afterPreview, err := t.countHosts()
	if err != nil {
		return t.fail(err)
	}
	if afterPreview != before {
		return t.fail(fmt.Errorf("preview wrote Hosts: %d then %d", before, afterPreview))
	}
	second, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("reopen after preview: %w", err))
	}
	reader := appvault.NewService(second, appvault.WithGroups(second))
	persisted, readErr := reader.List(context.Background())
	_ = second.Close()
	if readErr != nil {
		return t.fail(fmt.Errorf("read the reopened Vault: %w", readErr))
	}
	if len(persisted) != before {
		return t.fail(fmt.Errorf("a preview reached the store: %d Hosts, want %d",
			len(persisted), before))
	}

	// Skip keeps the existing Host untouched.
	rename := selftest24RenameCSV()
	if _, _, err := t.chooseContent("rename.csv", rename, vaultimport.FormatCSV); err != nil {
		return t.fail(err)
	}
	outcome, err := t.confirm()
	if err != nil {
		return t.fail(err)
	}
	if outcome.Imported != 0 || outcome.Overwritten != 0 || outcome.Skipped != 1 {
		return t.fail(fmt.Errorf("skip outcome = %+v", outcome))
	}
	web, err := t.hostByHostname("10.0.0.10")
	if err != nil {
		return t.fail(err)
	}
	if web.Label != `Web, "edge"` {
		return t.fail(fmt.Errorf("Skip changed the Host label to %q", web.Label))
	}

	// Overwrite updates that merge key in place: no fifth Host appears and
	// the stored password survives because the import supplied no new one.
	if _, _, err := t.chooseContent("rename.csv", rename, vaultimport.FormatCSV); err != nil {
		return t.fail(err)
	}
	t.state.SetImportMerge(string(appvault.ImportMergeOverwrite))
	outcome, err = t.confirm()
	if err != nil {
		return t.fail(err)
	}
	if outcome.Overwritten != 1 || outcome.Imported != 0 || outcome.Skipped != 0 {
		return t.fail(fmt.Errorf("overwrite outcome = %+v", outcome))
	}
	afterOverwrite, err := t.countHosts()
	if err != nil {
		return t.fail(err)
	}
	if afterOverwrite != before {
		return t.fail(fmt.Errorf("overwrite created a duplicate: %d Hosts, want %d",
			afterOverwrite, before))
	}
	web, err = t.hostByHostname("10.0.0.10")
	if err != nil {
		return t.fail(err)
	}
	if web.Label != "Web renamed" || web.Notes != "new notes" {
		return t.fail(fmt.Errorf("overwrite did not update the Host: %+v", web))
	}
	password, err := t.service.Secret(context.Background(), web.ID, appvault.FieldPassword)
	if err != nil || password != "s3cr3t" {
		return t.fail(fmt.Errorf("overwrite lost the stored password: %q, %v", password, err))
	}

	// The overwrite is persisted, not just visible in memory.
	second, err = store.Open(context.Background(), t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("reopen after overwrite: %w", err))
	}
	reader = appvault.NewService(second,
		appvault.WithSecrets(t.secrets), appvault.WithGroups(second))
	reopened, readErr := reader.List(context.Background())
	_ = second.Close()
	if readErr != nil {
		return t.fail(fmt.Errorf("read the overwritten Vault: %w", readErr))
	}
	renamed, ok := selftest24Host(reopened, "10.0.0.10")
	if !ok || renamed.Label != "Web renamed" || len(reopened) != before {
		return t.fail(fmt.Errorf("reopened overwrite = %+v (%d Hosts)", renamed, len(reopened)))
	}

	t.cleanup()
	return selftest.Passed(
		"4 conflicts and 3 invalid/duplicate rows reported by row number; preview wrote nothing; Skip kept and Overwrite updated one merge key",
		"vaultstate.ImportPreview/CommitImport + appvault.Service + store.Store")
}

func (t *selftest24) openStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	if _, _, err := t.chooseFixture("finalshell.json", vaultimport.FormatFinalShell); err != nil {
		return t.fail(err)
	}
	outcome, err := t.confirm()
	if err != nil {
		return t.fail(err)
	}
	if outcome.Imported != 1 {
		return t.fail(fmt.Errorf("FinalShell outcome = %+v", outcome))
	}

	host, err := t.hostByHostname("fs.example.test")
	if err != nil {
		return t.fail(err)
	}
	if !t.state.BeginEdit(host.ID) {
		return t.fail(fmt.Errorf("the listed imported Host could not be opened"))

	}
	t.tester.Frame()
	if !t.state.Editing() {
		return t.fail(fmt.Errorf("BeginEdit did not open the Host editor"))
	}
	form := t.state.Form()
	if form.ID != host.ID || form.Label != "FinalShell Web" ||
		form.Hostname != "fs.example.test" || form.Port != "2222" ||
		form.Username != "tester" || form.Protocol != "ssh" {
		return t.fail(fmt.Errorf("the editor opened with %+v", *form))
	}
	for _, want := range []string{
		m.VaultHostFormEditTitle(),
		m.VaultHostSave(), m.VaultHostCancel(),
	} {
		if !t.tester.HasText(want) {
			return t.fail(fmt.Errorf("%q is not rendered in the imported Host editor: %v",
				want, t.tester.Texts()))
		}
	}

	// The imported record is editable through the same production save path.
	form.Label = "FinalShell Web (edited)"
	if err := t.state.Save(); err != nil {
		return t.fail(fmt.Errorf("save the imported Host: %w", err))
	}
	t.tester.Frame()
	edited, err := t.hostByHostname("fs.example.test")
	if err != nil {
		return t.fail(err)
	}
	if edited.Label != "FinalShell Web (edited)" {
		return t.fail(fmt.Errorf("saved label = %q", edited.Label))
	}

	second, err := store.Open(context.Background(), t.dbPath)
	if err != nil {
		return t.fail(fmt.Errorf("reopen after editing: %w", err))
	}
	reader := appvault.NewService(second)
	reopened, readErr := reader.List(context.Background())
	_ = second.Close()
	if readErr != nil {
		return t.fail(fmt.Errorf("read the edited Vault: %w", readErr))
	}
	if len(reopened) != 1 || reopened[0].Label != "FinalShell Web (edited)" {
		return t.fail(fmt.Errorf("reopened edited Hosts = %+v", reopened))
	}

	t.cleanup()
	return selftest.Passed(
		"the imported FinalShell Host opened with its label, endpoint, port, and username, then saved to a reopened store",
		"vaultstate.BeginEdit + vaultui.View + appvault.Service + store.Store")
}

func (t *selftest24) layoutStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")
	rows := selftest24LayoutCSV(selftest24LayoutRows)
	if _, _, err := t.chooseContent("layout.csv", rows, vaultimport.FormatCSV); err != nil {
		return t.fail(err)
	}
	outcome, err := t.confirm()
	if err != nil {
		return t.fail(err)
	}
	if outcome.Imported != selftest24LayoutRows || outcome.Groups != 1 {
		return t.fail(fmt.Errorf("layout import outcome = %+v, want %d Hosts in 1 Group",
			outcome, selftest24LayoutRows))
	}
	if got := t.state.HostCount(); got != selftest24LayoutRows {
		return t.fail(fmt.Errorf("the list has %d Hosts, want %d", got, selftest24LayoutRows))
	}

	lastHost := fmt.Sprintf("layout-%02d", selftest24LayoutRows-1)
	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		// The list itself scrolls to the last imported Host.
		t.state.CancelImport()
		list := ui.NewTester(vaultui.View(t.state), size.width, size.height)
		list.SetLocale("en")
		list.Frame()
		if !list.HasText(m.VaultImportTitle()) || !list.HasText(m.VaultHostConnect()) {
			return t.fail(fmt.Errorf("%dx%d: the Vault list is not rendered: %v",
				size.width, size.height, list.Texts()))
		}
		if err := selftest24NoOverflow(list, size.width, size.height); err != nil {
			return t.fail(err)
		}
		list.Move(float32(size.width)/2, float32(size.height)/2)
		list.Scroll(float32(size.width)/2, float32(size.height)/2, 0, 5000)
		list.Key(0, ui.KeyEnd)
		list.Frame()
		if !list.HasText(lastHost) {
			return t.fail(fmt.Errorf("%dx%d: the list did not reach %q: %v",
				size.width, size.height, lastHost, list.Texts()))
		}
		if err := selftest24Visible(list, lastHost, size.width, size.height); err != nil {
			return t.fail(err)
		}

		// A fresh preview of the same 60 rows fills the panel. Its fixed
		// actions stay inside the window, and the row list scrolls.
		if _, _, err := t.chooseContent("layout.csv", rows, vaultimport.FormatCSV); err != nil {
			return t.fail(err)
		}
		panel := ui.NewTester(vaultui.View(t.state), size.width, size.height)
		panel.SetLocale("en")
		panel.Frame()
		for _, want := range []string{
			m.VaultImportTitle(), m.VaultImportSummary(selftest24LayoutRows, 0, selftest24LayoutRows, 0, 0),
			m.VaultImportImport(), m.VaultHostCancel(),
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
		panel.Key(0, ui.KeyEnd)
		panel.Frame()
		if !panel.HasText(lastHost) {
			return t.fail(fmt.Errorf("%dx%d: the import panel did not reach %q: %v",
				size.width, size.height, lastHost, panel.Texts()))
		}
		if err := selftest24Visible(panel, lastHost, size.width, size.height); err != nil {
			return t.fail(err)
		}
		if err := selftest24NoOverflow(panel, size.width, size.height); err != nil {
			return t.fail(err)
		}
	}

	t.cleanup()
	return selftest.Passed(
		"60 rows crossed the commit chunk; 1100x720 and 720x480 list and panel scroll to the last row with fixed actions in bounds",
		"vaultui.View + vaultstate.ConfirmImport + ui.Tester")
}

func (t *selftest24) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-24-")
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
	service := appvault.NewService(db,
		appvault.WithSecrets(secrets),
		appvault.WithKeys(db),
		appvault.WithGroups(db),
		appvault.WithProxyProfiles(db),
		appvault.WithKeyInspector(ssh.KeyParser{}),
		appvault.WithImportStore(db),
		appvault.WithImportFiles(selftest24Files{}),
	)
	t.service = service
	t.picker = &selftest24Picker{}
	state := vaultstate.NewState(service,
		vaultstate.WithGroups(service),
		vaultstate.WithKeys(service),
		vaultstate.WithProxyProfiles(service),
		vaultstate.WithImports(service),
		vaultstate.WithImportPicker(t.picker),
	)
	if err := state.Reload(); err != nil {
		t.cleanup()
		return fmt.Errorf("load the Vault: %w", err)
	}
	t.state = state
	t.tester = ui.NewTester(vaultui.View(state), 1100, 720)
	t.tester.SetLocale("en")
	t.tester.Frame()
	return nil
}

func (t *selftest24) chooseFixture(name string,
	format vaultimport.Format) (appvault.ImportPreview, vaultstate.ImportOutcome, error) {

	data, err := selftest24Fixtures.ReadFile("testdata/selftest24/" + name)
	if err != nil {
		return appvault.ImportPreview{}, vaultstate.ImportOutcome{},
			fmt.Errorf("read the %s fixture: %w", name, err)
	}
	return t.chooseContent(name, data, format)
}

func (t *selftest24) chooseContent(name string, data []byte,
	format vaultimport.Format) (appvault.ImportPreview, vaultstate.ImportOutcome, error) {

	t.state.BeginImport()
	t.picker.name, t.picker.content = name, data
	if !t.state.ChooseImportFile() {
		return appvault.ImportPreview{}, vaultstate.ImportOutcome{},
			fmt.Errorf("preview %s failed with problem %q", name, t.state.ImportProblem())
	}
	if !t.state.SetImportFormat(string(format)) && t.state.ImportFormatValue() != string(format) {
		return appvault.ImportPreview{}, vaultstate.ImportOutcome{},
			fmt.Errorf("select format %q for %s", format, name)
	}
	preview := t.state.ImportPreview()
	if preview == nil {
		return appvault.ImportPreview{}, vaultstate.ImportOutcome{},
			fmt.Errorf("%s produced no preview", name)
	}
	return *preview, vaultstate.ImportOutcome{}, nil
}

func (t *selftest24) confirm() (vaultstate.ImportOutcome, error) {
	if err := t.state.ConfirmImport(); err != nil {
		return vaultstate.ImportOutcome{}, fmt.Errorf("commit import: %w", err)
	}
	outcome, ok := t.state.ImportOutcome()
	if !ok {
		return vaultstate.ImportOutcome{}, fmt.Errorf("commit produced no outcome")
	}
	return outcome, nil
}

func (t *selftest24) countHosts() (int, error) {
	hosts, err := t.service.List(context.Background())
	if err != nil {
		return 0, fmt.Errorf("list Hosts: %w", err)
	}
	return len(hosts), nil
}

func (t *selftest24) hostByHostname(hostname string) (vault.Host, error) {
	hosts, err := t.service.List(context.Background())
	if err != nil {
		return vault.Host{}, fmt.Errorf("list Hosts: %w", err)
	}
	host, ok := selftest24Host(hosts, hostname)
	if !ok {
		return vault.Host{}, fmt.Errorf("Host %q is not listed", hostname)
	}
	return host, nil
}

func (t *selftest24) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest24) cleanup() {
	if t.secrets != nil {
		t.secrets.Close()
	}
	if t.db != nil {
		_ = t.db.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.dbPath, t.db, t.secrets, t.keyring = "", "", nil, nil, nil
	t.service, t.state, t.picker, t.tester = nil, nil, nil, nil
}

type selftest24Picker struct {
	name    string
	content []byte
}

func (p *selftest24Picker) PickImportFile() (string, []byte, error) {
	if p.name == "" {
		return "", nil, nil
	}
	return p.name, p.content, nil
}

// selftest24Files returns the throwaway Key for any IdentityFile reference.
// It stands in for the app's filesystem reader, which the self-test must not
// depend on.
type selftest24Files struct{}

func (selftest24Files) ReadImportFile(string) ([]byte, error) {
	return []byte(selftest24PrivateKey), nil
}

// selftest24Keyring is an in-memory platform credential store. It is the
// platform seam, not the field-secret seam: the real SecretStore still
// encrypts every value into SQLite.
type selftest24Keyring struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (k *selftest24Keyring) Get(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	value, ok := k.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (k *selftest24Keyring) Set(key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[key] = append([]byte(nil), value...)
	return nil
}

func (k *selftest24Keyring) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.values, key)
	return nil
}

func selftest24CheckCSVPreview(preview appvault.ImportPreview) error {
	if len(preview.Rows) != 5 {
		return fmt.Errorf("CSV preview has %d rows, want 5", len(preview.Rows))
	}
	web := preview.Rows[0]
	if web.Label != `Web, "edge"` || web.Hostname != "10.0.0.10" || web.Port != 2222 ||
		web.Username != "deploy" || web.Group != "Acme/Prod" ||
		web.KeyLabel != "id_ed25519" || !web.HasProxy {
		return fmt.Errorf("CSV row 0 = %+v", web)
	}
	if preview.Rows[1].Label != `=HYPERLINK("http://example.test")` {
		return fmt.Errorf("CSV formula label = %q", preview.Rows[1].Label)
	}
	if preview.Rows[2].Protocol != "telnet" || !preview.Rows[2].HasProxy {
		return fmt.Errorf("CSV telnet/proxy row = %+v", preview.Rows[2])
	}
	if preview.Rows[4].Status != appvault.ImportStatusDuplicate || preview.Rows[4].Selected {
		return fmt.Errorf("CSV duplicate row = %+v", preview.Rows[4])
	}
	return nil
}

func selftest24HasIssue(issues []vaultimport.Issue, code vaultimport.IssueCode, row int) bool {
	for _, issue := range issues {
		if issue.Code == code && issue.Row == row {
			return true
		}
	}
	return false
}

func selftest24Host(hosts []vault.Host, hostname string) (vault.Host, bool) {
	for _, host := range hosts {
		if host.Hostname == hostname {
			return host, true
		}
	}
	return vault.Host{}, false
}

func selftest24HostAtPort(hosts []vault.Host, hostname string, port int) (vault.Host, bool) {
	for _, host := range hosts {
		if host.Hostname == hostname && host.Port == port {
			return host, true
		}
	}
	return vault.Host{}, false
}

// selftest24RenameCSV changes one existing merge key's label and notes without
// changing its group, endpoint, protocol, port, or username.
func selftest24RenameCSV() []byte {
	return []byte("Groups,Label,Tags,Notes,Hostname/IP,Protocol,Port,Username,Password,KeyPath,Passphrase,Proxy,LocalShell\n" +
		"Acme/Prod,Web renamed,,new notes,10.0.0.10,ssh,2222,deploy,,,,,\n")
}

func selftest24LayoutCSV(count int) []byte {
	var out strings.Builder
	out.WriteString("Groups,Label,Tags,Notes,Hostname/IP,Protocol,Port,Username,Password,KeyPath,Passphrase,Proxy,LocalShell\n")
	for index := 0; index < count; index++ {
		fmt.Fprintf(&out, "Layout,layout-%02d,,,layout%02d.example.test,ssh,22,user%d,,,,,\n",
			index, index, index)
	}
	return []byte(out.String())
}

func selftest24Visible(tester *ui.Tester, text string, width, height int) error {
	rect, ok := tester.Find(text)
	if !ok {
		return fmt.Errorf("%q is not rendered", text)
	}
	if rect.Y < -0.5 || rect.Y+rect.H > float32(height)+0.5 ||
		rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
		return fmt.Errorf("%q is outside %dx%d: %+v", text, width, height, rect)
	}
	return nil
}

func selftest24NoOverflow(tester *ui.Tester, width, height int) error {
	for _, text := range tester.Texts() {
		rect, ok := tester.Find(text)
		if !ok || rect.W <= 0 || rect.H <= 0 {
			continue
		}
		if rect.X < -0.5 || rect.X+rect.W > float32(width)+0.5 {
			return fmt.Errorf("%dx%d: %q overflows horizontally: %+v",
				width, height, text, rect)
		}
	}
	return nil
}
