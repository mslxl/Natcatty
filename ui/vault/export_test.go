package vault

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

type fakeExports struct {
	result appvault.Export
}

func (f *fakeExports) ExportVault(_ context.Context, opts appvault.ExportOptions) (appvault.Export, error) {
	if f.result.Content == nil {
		f.result.Content = []byte("content")
	}
	f.result.Format = opts.Format
	f.result.Filename = "export." + opts.Format.Extension()
	return f.result, nil
}

type fakeExportWriter struct {
	path   string
	writes int
}

func (f *fakeExportWriter) Save(name string, _ []byte) (string, error) {
	f.writes++
	if f.path == "" {
		return "C:/Exports/" + name, nil
	}
	return f.path, nil
}

func exportView(t *testing.T) (*vaultstate.State, *ui.Tester, *fakeExportWriter) {
	t.Helper()
	writer := &fakeExportWriter{}
	var exports fakeExports
	state := vaultstate.NewState(&fakeHosts{items: []domainvault.Host{{
		ID: "h1", Label: "Web", Hostname: "web.example.test", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}}},
		vaultstate.WithExports(&exports),
		vaultstate.WithExportWriter(writer),
		vaultstate.WithRedrawRequest(func() {}),
	)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	state.Select(model.HostID("h1"))
	tester := ui.NewTester(View(state), 1100, 720)
	tester.SetLocale("en")
	return state, tester, writer
}

func TestExportEntryOpensThePanel(t *testing.T) {
	state, tester, _ := exportView(t)
	m := locales.In("en")
	if err := chooseVaultAction(tester, m, m.VaultExportTitle()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !state.ExportOpen() {
		t.Fatal("the Export entry did not open the panel")
	}
	for _, want := range []string{
		m.VaultExportTitle(), m.VaultExportFormat(), m.VaultExportScope(),
		m.VaultExportSelectedHost("Web"), m.VaultExportExport(), m.VaultHostCancel(),
	} {
		if !tester.HasText(want) {
			t.Errorf("the panel does not show %q: %v", want, tester.Texts())
		}
	}
}

func TestExportPanelWarnsAndConfirmsPlaintextSecrets(t *testing.T) {
	state, tester, writer := exportView(t)
	m := locales.In("en")
	state.BeginExport()
	tester.Frame()

	if err := tester.Click(m.VaultExportIncludeSecrets()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !state.ExportIncludesSecrets() {
		t.Fatal("the toggle did not enable secrets")
	}
	if !tester.HasText(m.VaultExportSecretsWarning()) ||
		!tester.HasText(m.VaultExportConfirmSecrets()) {
		t.Fatalf("the warning and confirmation are not rendered: %v", tester.Texts())
	}
	if err := tester.Click(m.VaultExportExport()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if writer.writes != 0 || state.ExportProblem() != vaultstate.ProblemExportUnconfirmed {
		t.Fatalf("writes = %d, problem = %q", writer.writes, state.ExportProblem())
	}
	if !tester.HasText(m.VaultProblemExportUnconfirmed()) {
		t.Fatalf("the unconfirmed problem is not rendered: %v", tester.Texts())
	}

	if err := tester.Click(m.VaultExportConfirmSecrets()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !state.ExportSecretsConfirmed() || !tester.HasText(m.VaultExportSecretsConfirmed()) {
		t.Fatalf("confirming did not take: confirmed %v, texts %v",
			state.ExportSecretsConfirmed(), tester.Texts())
	}
	if err := tester.Click(m.VaultExportExport()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if writer.writes != 1 || state.ExportOpen() {
		t.Fatalf("writes = %d, open = %v", writer.writes, state.ExportOpen())
	}
	if !tester.HasText(m.VaultExportDone("C:/Exports/export.json", len("content"))) {
		t.Fatalf("the outcome banner is missing: %v", tester.Texts())
	}
}

func TestExportPanelRendersAndScrollsAtBothWindowSizes(t *testing.T) {
	state, tester, _ := exportView(t)
	state.BeginExport()
	state.SetExportIncludeSecrets(true)
	state.ConfirmExportSecrets()
	m := locales.In("en")

	var secrets, viewport ui.Rect
	var gutter float32
	previous := exportLayoutProbe
	exportLayoutProbe = func(layout exportFieldLayout) {
		secrets, viewport, gutter = layout.secrets, layout.viewport, layout.gutter
	}
	defer func() { exportLayoutProbe = previous }()

	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		tester = ui.NewTester(View(state), size.width, size.height)
		tester.SetLocale("en")
		tester.Frame()

		if gutter <= 0 || viewport.W <= 0 || secrets.W <= 0 {
			t.Fatalf("%dx%d: missing geometry gutter=%v viewport=%+v toggle=%+v",
				size.width, size.height, gutter, viewport, secrets)
		}
		const epsilon = 0.5
		usableRight := viewport.X + viewport.W - gutter
		if secrets.X < viewport.X-epsilon || secrets.X+secrets.W > usableRight+epsilon {
			t.Fatalf("%dx%d: the secrets toggle runs under the bar: %+v right %.1f",
				size.width, size.height, secrets, usableRight)
		}
		for _, text := range tester.Texts() {
			rect, ok := tester.Find(text)
			if !ok || rect.W <= 0 || rect.H <= 0 {
				continue
			}
			if rect.X < -epsilon || rect.X+rect.W > float32(size.width)+epsilon {
				t.Errorf("%dx%d: %q overflows: %+v", size.width, size.height, text, rect)
			}
		}
		for _, want := range []string{
			m.VaultExportTitle(), m.VaultExportExport(), m.VaultHostCancel(),
		} {
			rect, ok := tester.Find(want)
			if !ok {
				t.Fatalf("%dx%d: %q is not rendered", size.width, size.height, want)
			}
			if rect.Y < -epsilon || rect.Y+rect.H > float32(size.height)+epsilon {
				t.Fatalf("%dx%d: %q is clipped: %+v", size.width, size.height, want, rect)
			}
		}
		tester.Move(float32(size.width)/2, float32(size.height)/2)
		tester.Scroll(float32(size.width)/2, float32(size.height)/2, 0, 5000)
		tester.Frame()
		for _, want := range []string{m.VaultExportExport(), m.VaultHostCancel()} {
			if !tester.HasText(want) {
				t.Fatalf("%dx%d: scrolling hid %q", size.width, size.height, want)
			}
		}
	}
}

func TestExportPanelLocalizesInEveryCatalog(t *testing.T) {
	state, _, _ := exportView(t)
	state.BeginExport()
	for _, locale := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(locale)
		tester := ui.NewTester(View(state), 720, 480)
		tester.SetLocale(locale)
		tester.Frame()
		for _, want := range []string{m.VaultExportTitle(), m.VaultExportExport()} {
			if !tester.HasText(want) {
				t.Errorf("%s: %q is not rendered: %v", locale, want, tester.Texts())
			}
		}
	}
}
