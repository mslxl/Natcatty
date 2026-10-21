//go:build selftest

package app

// Ticket 21 self-test. Press F11, then 2, then 1. The check proves Snippets,
// Snippet packages, variable substitution, and both run targets through the
// real SQLite store, the real application service, the real Vault view, and a
// real shell State backed by a recording Session transport:
//
//   - Snippet and package CRUD: create a package and two Snippets through the
//     Vault view, update one, delete the other, and rename the package.
//   - Variables: a run with a blank required variable names it and sends
//     nothing; filling the value sends the exact substituted bytes, while a
//     Go template action in the same command survives unchanged.
//   - Targets: Run here writes to the focused Session; Run on targets opens a
//     Tab per selected Host and writes the same resolved command to each.
//   - Layout: the real shell with sidebar and tab strip renders at 1100x720
//     and 720x480 with no horizontal overflow.
//
// Result: %TEMP%\natcatty-selftest-21.json. Every resource is closed and the
// temporary directory removed, so F11,2,1 passes twice in one process.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/adapters/store"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest21Ticket = 21

type selftest21 struct {
	dir      string
	store    *store.Store
	service  *appvault.Service
	sessions *appsession.Service
	factory  *selftest21Factory
	shell    *shellstate.State
	state    *vaultstate.State
	tester   *ui.Tester

	hostA domainvault.Host
	hostB domainvault.Host

	hostsStarted  bool
	hostsDeadline time.Time
}

func (t *selftest21) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest21Ticket,
		Name:   "Snippets, packages, variables, and targets",
		Steps: []selftest.Step{
			{Name: "snippet-and-package-crud", Run: t.crudStep},
			{Name: "missing-variable", Run: t.missingVariableStep},
			{Name: "run-active-session", Run: t.runActiveStep},
			{Name: "run-target-hosts", Run: t.runHostsStep},
			{Name: "layout", Run: t.layoutStep},
		},
	}
}

func (t *selftest21) crudStep(*ui.Context) selftest.Outcome {
	if err := t.start(); err != nil {
		return t.fail(err)
	}
	m := locales.In("en")

	// Create a package through the real editor.
	t.scrollVault()
	if err := t.tester.Click(m.VaultSnippetPackageNew()); err != nil {
		return t.fail(fmt.Errorf("open the package editor: %w", err))
	}
	t.tester.Frame()
	t.state.SnippetPackageForm().Path = "ops/linux"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the package: %w", err))
	}
	t.tester.Frame()
	if !hasPackage(t.service, "ops/linux") {
		return t.fail(fmt.Errorf("package ops/linux was not persisted"))
	}

	// Create the v1 Snippet through the real editor.
	t.scrollVault()
	if err := t.tester.Click(m.VaultSnippetNew()); err != nil {
		return t.fail(fmt.Errorf("open the Snippet editor: %w", err))
	}
	t.tester.Frame()
	form := t.state.SnippetForm()
	form.Label = "Deploy"
	form.Command = "deploy {{service:api}} {{token}} {{.Names}}"
	form.Package = "ops/linux"
	form.Tags = "web, ops"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the Snippet: %w", err))
	}
	t.tester.Frame()
	deploy, ok := snippetNamed(t.state, "Deploy")
	if !ok {
		return t.fail(fmt.Errorf("Deploy is not listed: %v", t.tester.Texts()))
	}
	if deploy.Command != "deploy {{service:api}} {{token}} {{.Names}}" ||
		deploy.Package != "ops/linux" ||
		domainvault.SnippetHasRunTargets(deploy) {
		return t.fail(fmt.Errorf("stored Snippet = %+v", deploy))
	}

	// Create a second Snippet solely to exercise delete.
	t.scrollVault()
	if err := t.tester.Click(m.VaultSnippetNew()); err != nil {
		return t.fail(fmt.Errorf("open the second Snippet editor: %w", err))
	}
	t.tester.Frame()
	second := t.state.SnippetForm()
	second.Label = "Delete me"
	second.Command = "true"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the second Snippet: %w", err))
	}
	t.tester.Frame()
	throwaway, ok := snippetNamed(t.state, "Delete me")
	if !ok {
		return t.fail(fmt.Errorf("the second Snippet is not listed"))
	}

	// Update the first Snippet through the real editor's Save action.
	if !t.state.BeginEditSnippet(deploy.ID) {
		return t.fail(fmt.Errorf("BeginEditSnippet rejected the listed Snippet"))
	}
	t.state.SnippetForm().Label = "Deploy app"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the updated Snippet: %w", err))
	}
	t.tester.Frame()
	if _, ok := snippetNamed(t.state, "Deploy app"); !ok {
		return t.fail(fmt.Errorf("the updated label is not listed"))
	}

	// Delete the throwaway Snippet through its confirmation.
	t.state.RequestDeleteSnippet(throwaway.ID)
	t.tester.Frame()
	if !t.tester.HasText(m.VaultSnippetDeleteConfirm("Delete me")) {
		return t.fail(fmt.Errorf("the Snippet delete confirmation is missing: %v",
			t.tester.Texts()))
	}
	if err := t.tester.Click(m.VaultHostDelete()); err != nil {
		return t.fail(fmt.Errorf("confirm the Snippet delete: %w", err))
	}
	t.tester.Frame()
	if _, ok := snippetNamed(t.state, "Delete me"); ok {
		return t.fail(fmt.Errorf("the deleted Snippet is still listed"))
	}
	if len(t.state.Snippets()) != 1 {
		return t.fail(fmt.Errorf("Snippets after delete = %d, want 1", len(t.state.Snippets())))
	}

	// Rename the package; the Snippet follows the new path.
	if !t.state.BeginEditSnippetPackage("ops/linux") {
		return t.fail(fmt.Errorf("BeginEditSnippetPackage rejected the saved package"))
	}
	t.state.SnippetPackageForm().Path = "ops/prod"
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the package rename: %w", err))
	}
	t.tester.Frame()
	if hasPackage(t.service, "ops/linux") || !hasPackage(t.service, "ops/prod") {
		return t.fail(fmt.Errorf("package rename did not move ops/linux to ops/prod"))
	}
	if deploy, ok := snippetNamed(t.state, "Deploy app"); !ok || deploy.Package != "ops/prod" {
		return t.fail(fmt.Errorf("Snippet package after rename = %q", deploy.Package))
	}

	// Create the target-scoped Snippet used by Run on targets.
	t.scrollVault()
	if err := t.tester.Click(m.VaultSnippetNew()); err != nil {
		return t.fail(fmt.Errorf("open the target Snippet editor: %w", err))
	}
	t.tester.Frame()
	targeted := t.state.SnippetForm()
	targeted.Label = "Restart"
	targeted.Command = "systemctl restart {{service:api}}"
	targeted.TargetHosts[string(t.hostA.ID)] = true
	targeted.TargetHosts[string(t.hostB.ID)] = true
	t.tester.Frame()
	if err := t.tester.Click(m.VaultHostSave()); err != nil {
		return t.fail(fmt.Errorf("save the target Snippet: %w", err))
	}
	t.tester.Frame()
	if _, ok := snippetNamed(t.state, "Restart"); !ok {
		return t.fail(fmt.Errorf("the target Snippet is not listed"))
	}
	return selftest.Passed(
		"real store and Vault view: package create/rename, Snippet create/update/delete, targets persisted",
		"appvault.Service + store.Store + vaultui.View + ui.Tester")
}

func (t *selftest21) missingVariableStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	deploy, ok := snippetNamed(t.state, "Deploy app")
	if !ok {
		return t.fail(fmt.Errorf("Deploy app is missing"))
	}
	if !t.state.BeginSnippetRun(deploy.ID, vaultstate.SnippetDestinationActive) {
		return t.fail(fmt.Errorf("BeginSnippetRun rejected the Snippet"))
	}
	t.tester.Frame()
	if !t.state.SnippetRunOpen() {
		return t.fail(fmt.Errorf("a Snippet with variables ran without asking for them"))
	}
	if err := t.tester.Click(m.VaultSnippetRunConfirm()); err != nil {
		return t.fail(fmt.Errorf("click Run now with a blank variable: %w", err))
	}
	t.tester.Frame()
	if t.state.SnippetRunProblem() != vaultstate.ProblemSnippetMissingVariables {
		return t.fail(fmt.Errorf("run problem = %q, want %q",
			t.state.SnippetRunProblem(), vaultstate.ProblemSnippetMissingVariables))
	}
	if !t.tester.HasText(m.VaultSnippetMissingVariables("token")) {
		return t.fail(fmt.Errorf("the missing variable is not named: %v", t.tester.Texts()))
	}
	if !t.tester.HasText(m.VaultSnippetProblemMissingVariables()) {
		return t.fail(fmt.Errorf("the localized missing-variable problem is not shown: %v",
			t.tester.Texts()))
	}
	if got := t.factory.writesFor(""); len(got) != 0 {
		return t.fail(fmt.Errorf("an incomplete command reached the Session: %q", got))
	}
	t.state.CancelSnippetRun()
	t.tester.Frame()
	return selftest.Passed(
		"a blank required variable is named, localized, and sends nothing",
		"vaultstate.State + vaultui.View + ui.Tester")
}

func (t *selftest21) runActiveStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	deploy, ok := snippetNamed(t.state, "Deploy app")
	if !ok {
		return t.fail(fmt.Errorf("Deploy app is missing"))
	}
	if !t.state.BeginSnippetRun(deploy.ID, vaultstate.SnippetDestinationActive) {
		return t.fail(fmt.Errorf("BeginSnippetRun rejected the Snippet"))
	}
	t.state.SnippetRunForm().SetValue("token", "s3cret")
	t.tester.Frame()
	if !t.tester.HasText("deploy api s3cret {{.Names}}") {
		return t.fail(fmt.Errorf("the preview does not show the substituted command: %v",
			t.tester.Texts()))
	}
	if got := t.factory.writesFor(""); len(got) != 0 {
		return t.fail(fmt.Errorf("a command was sent before Run: %q", got))
	}
	if err := t.tester.Click(m.VaultSnippetRunConfirm()); err != nil {
		return t.fail(fmt.Errorf("click Run now: %w", err))
	}
	t.tester.Frame()
	if t.state.SnippetRunOpen() {
		return t.fail(fmt.Errorf("the run form stayed open: %v", t.state.SnippetRunProblem()))
	}
	want := []string{"deploy api s3cret {{.Names}}\r"}
	got := t.factory.writesFor("")
	if !slices.Equal(got, want) {
		return t.fail(fmt.Errorf("active Session received %q, want %q", got, want))
	}
	stored, err := t.service.ListSnippets(context.Background())
	if err != nil {
		return t.fail(fmt.Errorf("list Snippets: %w", err))
	}
	for _, snippet := range stored {
		if snippet.ID == deploy.ID && snippet.VariableValues["token"] != "s3cret" {
			return t.fail(fmt.Errorf("remembered values = %v", snippet.VariableValues))
		}
	}
	return selftest.Passed(
		"the focused local Session receives exactly the substituted bytes; Go template actions are untouched",
		"snippetRunner + appsession.Service + recording Transport + vaultui.View")
}

func (t *selftest21) runHostsStep(*ui.Context) selftest.Outcome {
	if !t.hostsStarted {
		targeted, ok := snippetNamed(t.state, "Restart")
		if !ok {
			return t.fail(fmt.Errorf("Restart is missing"))
		}
		if !t.state.BeginSnippetRun(targeted.ID, vaultstate.SnippetDestinationHosts) {
			return t.fail(fmt.Errorf("BeginSnippetRun rejected the target Snippet"))
		}
		t.tester.Frame()
		if !t.state.ConfirmSnippetRun() {
			return t.fail(fmt.Errorf("ConfirmSnippetRun failed: %v", t.state.SnippetRunProblem()))
		}
		t.hostsStarted = true
		t.hostsDeadline = time.Now().Add(selftestTimeout)
	}
	t.shell.Flush()
	want := "systemctl restart api\r"
	gotA := t.factory.writesFor(t.hostA.ID)
	gotB := t.factory.writesFor(t.hostB.ID)
	if len(gotA) == 1 && gotA[0] == want && len(gotB) == 1 && gotB[0] == want {
		if t.shell.TabCount() < 3 {
			return t.fail(fmt.Errorf("tabs = %d, want the local Tab and two Host Tabs",
				t.shell.TabCount()))
		}
		return selftest.Passed(
			"one Tab per selected Host receives exactly the resolved command",
			"snippetRunner + shellstate.OpenHostWithStartup + appsession.Service + recording Transport")
	}
	if len(gotA) > 1 || len(gotB) > 1 {
		return t.fail(fmt.Errorf("target writes = %q / %q, want one each", gotA, gotB))
	}
	return pending(t.hostsDeadline, "the two target Host Sessions")
}

func (t *selftest21) layoutStep(*ui.Context) selftest.Outcome {
	m := locales.In("en")
	// Finalize any pending target Tab so the tab strip shows the production
	// structure on every frame.
	t.shell.Flush()
	t.shell.ShowVault()
	view := shellui.View(t.shell)

	for _, size := range []struct{ width, height int }{{1100, 720}, {720, 480}} {
		tester := ui.NewTester(view, size.width, size.height)
		tester.SetLocale("en")
		tester.Frame()
		scrollShell21(tester, size.width, size.height)

		for _, want := range []string{
			"Vault", m.VaultSnippetListTitle(len(t.state.Snippets())),
			m.VaultSnippetNew(), "Deploy app", "Restart",
		} {
			if !tester.HasText(want) {
				return t.fail(fmt.Errorf("%dx%d: %q is not rendered: %v",
					size.width, size.height, want, tester.Texts()))
			}
		}
		if rect, ok := tester.Find(m.VaultSnippetRunActive()); !ok ||
			rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 ||
			rect.Y < -0.5 || rect.Y+rect.H > float32(size.height)+0.5 {
			return t.fail(fmt.Errorf("%dx%d: Run here is clipped: %+v, %v",
				size.width, size.height, rect, ok))
		}
		for _, text := range tester.Texts() {
			rect, ok := tester.Find(text)
			if !ok || rect.W <= 0 || rect.H <= 0 {
				continue
			}
			if rect.X < -0.5 || rect.X+rect.W > float32(size.width)+0.5 {
				return t.fail(fmt.Errorf("%dx%d: %q overflows horizontally: %+v",
					size.width, size.height, text, rect))
			}
		}
	}
	t.cleanup()
	return selftest.Passed(
		"the real shell, sidebar, and tab strip render the Snippet list at 1100x720 "+
			"and 720x480 without horizontal clipping",
		"shellui.View + vaultui.View + ui.Tester")
}

func (t *selftest21) start() error {
	if t.state != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "natcatty-selftest-21-")
	if err != nil {
		return fmt.Errorf("create temporary directory: %w", err)
	}
	db, err := store.Open(context.Background(), filepath.Join(dir, "vault.db"))
	if err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("open store: %w", err)
	}
	service := appvault.NewService(db,
		appvault.WithGroups(db),
		appvault.WithSnippets(db),
	)
	ctx := context.Background()
	if _, err := service.CreateGroup(ctx, domainvault.GroupDraft{Path: "prod"}); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("create Host Group: %w", err)
	}
	hostA, err := service.Create(ctx, domainvault.Draft{
		Label: "web-a", Hostname: "10.0.0.1", Username: "root",
		Protocol: domainvault.ProtocolSSH, Group: "prod",
	})
	if err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("create Host A: %w", err)
	}
	hostB, err := service.Create(ctx, domainvault.Draft{
		Label: "web-b", Hostname: "10.0.0.2", Username: "root",
		Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("create Host B: %w", err)
	}

	factory := &selftest21Factory{transports: make(map[*selftest21Transport]bool)}
	sessions := appsession.NewService(factory)
	var shell *shellstate.State
	var state *vaultstate.State
	var vaultView func(*ui.Context)
	shell = shellstate.NewState(
		shellstate.WithSessions(sessions, func(*appsession.Session) (shellstate.Surface, error) {
			return selftest21Surface{}, nil
		}),
		// Build the Vault view once: its closure owns the list scroll offset,
		// and rebuilding it per frame would reset that offset.
		shellstate.WithVault(func(c *ui.Context) { vaultView(c) }),
	)
	state = vaultstate.NewState(service,
		vaultstate.WithGroups(service),
		vaultstate.WithSnippets(service),
		vaultstate.WithSnippetRunner(snippetRunner{
			shell:    func() *shellstate.State { return shell },
			sessions: func() *appsession.Service { return sessions },
		}),
	)
	vaultView = vaultui.View(state)
	if err := state.Reload(); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("load the Vault: %w", err)
	}
	if err := shell.NewTab(); err != nil {
		_ = db.Close()
		_ = os.RemoveAll(dir)
		return fmt.Errorf("open the active local Session: %w", err)
	}
	shell.ShowVault()

	tester := ui.NewTester(vaultui.View(state), 1100, 900)
	tester.SetLocale("en")
	tester.Frame()

	t.dir, t.store, t.service = dir, db, service
	t.sessions, t.factory, t.shell, t.state, t.tester = sessions, factory, shell, state, tester
	t.hostA, t.hostB = hostA, hostB
	return nil
}

func (t *selftest21) fail(err error) selftest.Outcome {
	t.cleanup()
	return selftest.Failed(err)
}

func (t *selftest21) cleanup() {
	if t.shell != nil {
		t.shell.Shutdown()
		t.shell.CloseAllSessions()
	}
	if t.sessions != nil {
		t.sessions.CloseAll()
	}
	if t.factory != nil {
		t.factory.closeAll()
	}
	if t.store != nil {
		_ = t.store.Close()
	}
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
	}
	t.dir, t.store, t.service = "", nil, nil
	t.sessions, t.factory, t.shell, t.state, t.tester = nil, nil, nil, nil, nil
	t.hostA, t.hostB = domainvault.Host{}, domainvault.Host{}
	t.hostsStarted = false
}

func (t *selftest21) scrollVault() {
	scrollShell21(t.tester, 1100, 900)
}

func scrollShell21(tester *ui.Tester, width, height int) {
	tester.Move(float32(width)/2, float32(height)/2)
	tester.Scroll(float32(width)/2, float32(height)/2, 0, 10000)
	tester.Frame()
}

func hasPackage(service *appvault.Service, path string) bool {
	packages, err := service.ListSnippetPackages(context.Background())
	if err != nil {
		return false
	}
	for _, pkg := range packages {
		if pkg.Path == path {
			return true
		}
	}
	return false
}

func snippetNamed(state *vaultstate.State, label string) (domainvault.Snippet, bool) {
	for _, snippet := range state.Snippets() {
		if snippet.Label == label {
			return snippet, true
		}
	}
	return domainvault.Snippet{}, false
}

// selftest21Factory records each Session's writes by Host so the target steps
// can assert exact bytes. Local Sessions use the empty Host id.
type selftest21Factory struct {
	mu         sync.Mutex
	transports map[*selftest21Transport]bool
	writes     map[model.HostID][]string
}

func (f *selftest21Factory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	transport := &selftest21Transport{
		factory: f,
		hostID:  spec.HostID,
		output:  make(chan []byte),
		closed:  make(chan struct{}),
	}
	f.mu.Lock()
	if f.transports == nil {
		f.transports = make(map[*selftest21Transport]bool)
	}
	f.transports[transport] = true
	f.mu.Unlock()
	return transport, nil
}

func (f *selftest21Factory) record(hostID model.HostID, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes == nil {
		f.writes = make(map[model.HostID][]string)
	}
	f.writes[hostID] = append(f.writes[hostID], string(data))
}

func (f *selftest21Factory) writesFor(hostID model.HostID) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes[hostID]...)
}

func (f *selftest21Factory) closeAll() {
	f.mu.Lock()
	transports := make([]*selftest21Transport, 0, len(f.transports))
	for transport := range f.transports {
		transports = append(transports, transport)
	}
	f.mu.Unlock()
	for _, transport := range transports {
		_ = transport.Close()
	}
}

type selftest21Transport struct {
	factory *selftest21Factory
	hostID  model.HostID
	output  chan []byte
	closed  chan struct{}
	once    sync.Once
}

func (f *selftest21Transport) Read(p []byte) (int, error) {
	select {
	case data := <-f.output:
		return copy(p, data), nil
	case <-f.closed:
		return 0, io.EOF
	}
}

func (f *selftest21Transport) Write(p []byte) (int, error) {
	f.factory.record(f.hostID, append([]byte(nil), p...))
	return len(p), nil
}

func (f *selftest21Transport) Resize(int, int) error { return nil }
func (f *selftest21Transport) Wait() (int, error)    { return 0, nil }

func (f *selftest21Transport) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

type selftest21Surface struct{}

func (selftest21Surface) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal") }
func (selftest21Surface) Close()                        {}
