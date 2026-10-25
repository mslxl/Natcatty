//go:build selftest

package app

// Ticket 37 self-test. Press F11, then 3, then 7. The check proves file
// associations and the default opener through the app's real seams:
//
//   - Associations: the real Settings view creates, renames, and removes an
//     extension Association through its controls. The application picker is
//     scripted (it would otherwise open a native dialog), and the result is
//     written by the real preferences service.
//   - Default opener: the internal editor, system editor, and external
//     application are each selected and persisted; the rendered Settings row
//     shows the chosen application and its reset control restores the default.
//   - Opening with an association: the real transfer surface opens a local file
//     and the application service resolves the extension's external
//     application instead of the default.
//   - Unknown extensions: a file with an unlisted extension and a file with no
//     extension both use the default application, and a fresh preferences
//     service reads the whole document back (the restart path).
//
// The launcher is a recording fake, so no real editor starts during the check.
// Every wait polls the real surface state or the recorded launcher under a
// 30-second deadline; there is no sleep.
//
// Result: %TEMP%\natcatty-selftest-37.json.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	adaptersstore "github.com/mslxl/natcatty/adapters/store"
	appfileopen "github.com/mslxl/natcatty/application/fileopen"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/settings"
	transferstate "github.com/mslxl/natcatty/ui/transfer/state"
)

const selftest37Ticket = 37

const selftest37Wait = 30 * time.Second

type selftest37 struct {
	workdir         string
	preferencesPath string
	preferences     *apppreferences.Service
	associations    *settings.AssociationsState
	settingsView    *ui.Tester
	preferencesView *settings.PreferencesState
	transfer        *transferstate.State
	picker          *selftest37Picker
	launcher        *selftest37Launcher
	cleanup         func()

	deadline time.Time
	// openPhase and fallbackPhase make the asynchronous steps idempotent: the
	// harness calls a step once per frame until it reports an outcome, so the
	// one-time association setup and file opens must run only on the first
	// call.
	openPhase       int
	openStarted     bool
	fallbackPhase   int
	fallbackStarted bool
}

func (t *selftest37) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest37Ticket,
		Name:   "File associations and default opener",
		Steps: []selftest.Step{
			{Name: "associations-create-edit-remove", Run: t.associationsStep},
			{Name: "default-opener-kinds", Run: t.defaultOpenerStep},
			{Name: "open-uses-the-association", Run: t.openAssociationStep},
			{Name: "unknown-extensions-use-the-default", Run: t.defaultFallbackStep},
		},
	}
}

// selftest37Launcher records what the application service asked to start. It
// never runs a process, so the check cannot disturb the user's desktop.
type selftest37Launcher struct {
	mu    sync.Mutex
	calls []selftest37Call
}

type selftest37Call struct {
	system      bool
	application string
	path        string
}

func (l *selftest37Launcher) OpenSystem(_ context.Context, path string) error {
	l.record(selftest37Call{system: true, path: path})
	return nil
}

func (l *selftest37Launcher) OpenApplication(_ context.Context, application, path string) error {
	l.record(selftest37Call{application: application, path: path})
	return nil
}

func (l *selftest37Launcher) record(call selftest37Call) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}

func (l *selftest37Launcher) snapshot() []selftest37Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]selftest37Call(nil), l.calls...)
}

func (l *selftest37Launcher) reset() {
	l.mu.Lock()
	l.calls = nil
	l.mu.Unlock()
}

// selftest37Picker is the application-picker seam. It returns a scripted
// executable instead of opening the native dialog.
type selftest37Picker struct {
	mu      sync.Mutex
	answers []domainpreferences.ExternalApplication
	index   int
}

func (p *selftest37Picker) pick() (domainpreferences.ExternalApplication, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.index >= len(p.answers) {
		return domainpreferences.ExternalApplication{}, false
	}
	answer := p.answers[p.index]
	p.index++
	return answer, true
}

func (t *selftest37) setup() error {
	if t.workdir != "" {
		return nil
	}
	workdir, err := os.MkdirTemp("", "natcatty-selftest-37-")
	if err != nil {
		return err
	}
	t.workdir = workdir
	t.preferencesPath = filepath.Join(workdir, "preferences.json")
	downloads := filepath.Join(workdir, "downloads")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		_ = os.RemoveAll(workdir)
		return err
	}
	for name, content := range map[string]string{
		"notes.txt":        "notes",
		"mystery.unknown":  "unknown",
		"LICENSE":          "license",
		"remote-notes.txt": "remote",
	} {
		if err := os.WriteFile(filepath.Join(workdir, name), []byte(content), 0o644); err != nil {
			_ = os.RemoveAll(workdir)
			return err
		}
	}

	t.preferences = apppreferences.NewService(adaptersstore.OpenPreferences(t.preferencesPath))
	if err := t.preferences.Load(context.Background()); err != nil {
		_ = os.RemoveAll(workdir)
		return err
	}
	t.launcher = &selftest37Launcher{}
	t.picker = &selftest37Picker{answers: []domainpreferences.ExternalApplication{
		{Path: filepath.Join(workdir, "viewer.exe"), Name: "Viewer"},
		{Path: filepath.Join(workdir, "viewer.exe"), Name: "Viewer"},
		{Path: filepath.Join(workdir, "viewer.exe"), Name: "Viewer"},
		{Path: filepath.Join(workdir, "other.exe"), Name: "Other"},
	}}
	t.associations = settings.NewAssociationsState(t.preferences,
		settings.WithApplicationPicker(t.picker.pick),
		settings.WithAssociationsRedrawRequest(func() {}))
	t.preferencesView = settings.NewPreferencesState(t.preferences)

	search := settings.NewSearchState()
	search.SetArea(settings.AreaSFTPEditor)
	t.settingsView = ui.NewTester(settings.ViewWithSettings(settings.Config{
		Preferences:  t.preferencesView,
		Associations: t.associations,
		Search:       search,
	}), 1100, 1400)
	t.settingsView.SetLocale("en")
	t.settingsView.Frame()

	openService := appfileopen.New(t.preferences, t.launcher,
		appfileopen.WithTempRoot(downloads))
	transferService := apptransfer.NewService(adaptersftp.NewLocal(), nil)
	t.transfer = transferstate.NewState(transferService,
		transferstate.WithFileOpener(openService.Open))
	t.deadline = time.Now().Add(selftest37Wait)
	t.cleanup = func() {
		if t.transfer != nil {
			t.transfer.Shutdown()
		}
		_ = os.RemoveAll(workdir)
	}
	return nil
}

func (t *selftest37) expired(what string) selftest.Outcome {
	if time.Now().After(t.deadline) {
		return selftest.Failed(fmt.Errorf("%s did not finish within %s", what, selftest37Wait))
	}
	return selftest.Pending
}

func (t *selftest37) flush() {
	if t.transfer != nil {
		t.transfer.Flush()
	}
}

func (t *selftest37) associationsStep(*ui.Context) selftest.Outcome {
	// A step is retried only until it reports an outcome, so this runs once at
	// the start of a check. Dropping any previous instance makes every run
	// start from fresh fixtures and a clean preferences file, even when the
	// previous run failed part-way.
	if t.cleanup != nil {
		t.cleanup()
	}
	*t = selftest37{}
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 37 fixtures were not created: %v", err))
	}
	t.deadline = time.Now().Add(selftest37Wait)
	m := locales.In("en")
	tester := t.settingsView

	if !tester.HasText(m.SettingsFileAssociations()) ||
		!tester.HasText(m.SettingsAssociationEmpty()) {
		return selftest.Failed(fmt.Errorf("the Settings window does not show the associations section: %q",
			tester.Texts()))
	}

	if err := tester.Click(m.SettingsAssociationExtension()); err != nil {
		return selftest.Failed(fmt.Errorf("the extension field was not reachable: %v", err))
	}
	tester.Type("txt")
	tester.Frame()
	if got := t.associations.ExtensionValue(); got != "txt" {
		return selftest.Failed(fmt.Errorf("typing into the extension field produced %q", got))
	}
	if err := tester.Click(m.SettingsAssociationAdd()); err != nil {
		return selftest.Failed(fmt.Errorf("the Add association control was not reachable: %v", err))
	}
	tester.Frame()
	saved, ok := t.preferences.Settings().SFTPEditor.AssociationFor("txt")
	if !ok || saved.Kind != domainpreferences.OpenerSystem {
		return selftest.Failed(fmt.Errorf("the created association = %+v, %v; want the system opener",
			saved, ok))
	}
	if !tester.HasText(".txt") {
		return selftest.Failed(fmt.Errorf("the created association is not listed: %q", tester.Texts()))
	}

	// Edit the created association: rename it and give it the external opener
	// through the same chooser seam a native dialog would use.
	if err := tester.Click(m.SettingsAssociationEdit()); err != nil {
		return selftest.Failed(fmt.Errorf("the Edit control was not reachable: %v", err))
	}
	tester.Frame()
	if !tester.HasText(m.SettingsAssociationEditing("txt")) {
		return selftest.Failed(fmt.Errorf("the edit form did not open: %q", tester.Texts()))
	}
	*t.associations.Extension() = "md"
	t.associations.SelectKind(domainpreferences.OpenerExternal)
	tester.Frame()
	if err := tester.Click(m.SettingsChooseApplication()); err != nil {
		return selftest.Failed(fmt.Errorf("the application chooser was not reachable: %v", err))
	}
	tester.Frame()
	if got := t.associations.Application().Name; got != "Viewer" {
		return selftest.Failed(fmt.Errorf("the scripted picker produced %q", got))
	}
	if err := tester.Click(m.SettingsAssociationSave()); err != nil {
		return selftest.Failed(fmt.Errorf("the Save association control was not reachable: %v", err))
	}
	tester.Frame()
	edited, ok := t.preferences.Settings().SFTPEditor.AssociationFor("md")
	if !ok || edited.Kind != domainpreferences.OpenerExternal ||
		edited.Application.Name != "Viewer" {
		return selftest.Failed(fmt.Errorf("the edited association = %+v, %v", edited, ok))
	}
	if _, stillThere := t.preferences.Settings().SFTPEditor.AssociationFor("txt"); stillThere {
		return selftest.Failed(fmt.Errorf("renaming the association left the old extension behind"))
	}

	if err := tester.Click(m.SettingsAssociationRemove()); err != nil {
		return selftest.Failed(fmt.Errorf("the Remove control was not reachable: %v", err))
	}
	tester.Frame()
	if len(t.preferences.Settings().SFTPEditor.FileAssociations) != 0 {
		return selftest.Failed(fmt.Errorf("removing the association left %+v",
			t.preferences.Settings().SFTPEditor.FileAssociations))
	}
	if !tester.HasText(m.SettingsAssociationEmpty()) {
		return selftest.Failed(fmt.Errorf("the empty association list is not shown: %q", tester.Texts()))
	}
	return selftest.Passed(
		"the real Settings view created, renamed, and removed an association through its controls; the application picker seam supplied the executable",
		"settings.ViewWithSettings -> settings.AssociationsState -> apppreferences.Service (scripted app picker)")
}

func (t *selftest37) defaultOpenerStep(*ui.Context) selftest.Outcome {
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 37 fixtures were not created: %v", err))
	}
	t.deadline = time.Now().Add(selftest37Wait)
	m := locales.In("en")
	tester := t.settingsView

	if !t.associations.SelectDefaultKind(domainpreferences.OpenerInternal) {
		return selftest.Failed(fmt.Errorf("selecting the internal editor failed"))
	}
	if got := t.preferences.Settings().SFTPEditor.DefaultOpener.Kind; got != domainpreferences.OpenerInternal {
		return selftest.Failed(fmt.Errorf("the internal default persisted as %q", got))
	}
	if !t.associations.SelectDefaultKind(domainpreferences.OpenerExternal) {
		return selftest.Failed(fmt.Errorf("selecting the external application failed"))
	}
	opener := t.preferences.Settings().SFTPEditor.DefaultOpener
	if opener.Kind != domainpreferences.OpenerExternal || opener.Application.Name != "Viewer" {
		return selftest.Failed(fmt.Errorf("the external default persisted as %+v", opener))
	}
	tester.Frame()
	if !tester.HasText("Viewer") {
		return selftest.Failed(fmt.Errorf("the chosen default application is not shown: %q", tester.Texts()))
	}

	// The reset control is the third kind's path back to the shipped default.
	if err := tester.Click(m.SettingsReset()); err != nil {
		return selftest.Failed(fmt.Errorf("the default opener's reset control was not reachable: %v", err))
	}
	tester.Frame()
	if got := t.preferences.Settings().SFTPEditor.DefaultOpener.Kind; got != domainpreferences.OpenerSystem {
		return selftest.Failed(fmt.Errorf("the reset control left the default as %q", got))
	}
	return selftest.Passed(
		"internal, external, and system openers were each selected and persisted; the rendered reset control restored the system default",
		"settings.AssociationsState.SelectDefaultKind -> apppreferences.Service (internal/system), scripted picker (external)")
}

func (t *selftest37) openAssociationStep(*ui.Context) selftest.Outcome {
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 37 fixtures were not created: %v", err))
	}
	if !t.openStarted {
		t.deadline = time.Now().Add(selftest37Wait)
		t.openStarted = true
	}
	t.flush()
	if t.openPhase == 0 {
		switch {
		case !t.localSettled():
			// EnsureStarted is idempotent; Navigate applies once the first
			// listing exists, so start it here and poll on later frames.
			if t.transfer.Tab(transferstate.Left) == nil {
				t.transfer.EnsureStarted()
			} else if tab := t.transfer.Tab(transferstate.Left); tab.FS != nil && !tab.Loading {
				t.transfer.Navigate(transferstate.Left, t.workdir)
			}
			return t.expired("the real local pane")
		case !samePath(t.transfer.Tab(transferstate.Left).Path, t.workdir):
			t.transfer.Navigate(transferstate.Left, t.workdir)
			return t.expired("the local fixture listing")
		}

		// Configure txt -> the external Viewer through the ordinary form.
		*t.associations.Extension() = "txt"
		t.associations.SelectKind(domainpreferences.OpenerExternal)
		if !t.associations.ChooseAssociationApplication() {
			return selftest.Failed(fmt.Errorf("the association application was not chosen"))
		}
		if !t.associations.Save() {
			return selftest.Failed(fmt.Errorf("the txt association was not saved: %s", t.associations.Problem()))
		}
		t.launcher.reset()
		entry, ok := entryByName(t.transfer, transferstate.Left, "notes.txt")
		if !ok {
			return selftest.Failed(fmt.Errorf("the local fixture listing has no notes.txt"))
		}
		t.transfer.Open(transferstate.Left, entry)
		t.openPhase = 1
		return t.expired("the association opener")
	}

	calls := t.launcher.snapshot()
	if len(calls) == 0 {
		return t.expired("the association opener")
	}
	if len(calls) != 1 || calls[0].system ||
		calls[0].application != filepath.Join(t.workdir, "viewer.exe") ||
		calls[0].path != filepath.Join(t.workdir, "notes.txt") {
		return selftest.Failed(fmt.Errorf("opening notes.txt launched %+v", calls))
	}
	// The launch is recorded before the UI post that publishes the notice, so
	// the notice can trail the call by one frame.
	switch got := t.transfer.Notice(); got {
	case "opened-external":
	default:
		if got == "" {
			return t.expired("the opened-external notice")
		}
		return selftest.Failed(fmt.Errorf("opening notes.txt reported %q", got))
	}
	return selftest.Passed(
		"the real SFTP surface opened notes.txt with the extension's external application instead of the default",
		"transferstate.State.Open -> application/fileopen.Service -> recorded launcher")
}

func (t *selftest37) defaultFallbackStep(*ui.Context) selftest.Outcome {
	if err := t.setup(); err != nil {
		return selftest.Failed(fmt.Errorf("the ticket 37 fixtures were not created: %v", err))
	}
	if !t.fallbackStarted {
		t.deadline = time.Now().Add(selftest37Wait)
		t.fallbackStarted = true
	}
	t.flush()
	if !t.localSettled() || !samePath(t.transfer.Tab(transferstate.Left).Path, t.workdir) {
		return t.expired("the local fixture listing")
	}

	if t.fallbackPhase == 0 {
		// The default is an external application; neither file's extension has
		// an association, so both must use it.
		if !t.associations.ChooseDefaultApplication() {
			return selftest.Failed(fmt.Errorf("the default application was not chosen"))
		}
		if got := t.preferences.Settings().SFTPEditor.DefaultOpener.Application.Name; got != "Other" {
			return selftest.Failed(fmt.Errorf("the default application persisted as %q", got))
		}
		t.launcher.reset()
		for _, name := range []string{"mystery.unknown", "LICENSE"} {
			entry, ok := entryByName(t.transfer, transferstate.Left, name)
			if !ok {
				return selftest.Failed(fmt.Errorf("the local fixture listing has no %s", name))
			}
			t.transfer.Open(transferstate.Left, entry)
		}
		t.fallbackPhase = 1
		return t.expired("the default opener for both unknown files")
	}

	calls := t.launcher.snapshot()
	if len(calls) != 2 {
		return t.expired("the default opener for both unknown files")
	}
	want := filepath.Join(t.workdir, "other.exe")
	for index, name := range []string{"mystery.unknown", "LICENSE"} {
		if calls[index].system || calls[index].application != want ||
			calls[index].path != filepath.Join(t.workdir, name) {
			return selftest.Failed(fmt.Errorf("%s launched %+v, want the default application %q",
				name, calls[index], want))
		}
	}

	// A fresh service over the same file is the restart path.
	restarted := apppreferences.NewService(adaptersstore.OpenPreferences(t.preferencesPath))
	if err := restarted.Load(context.Background()); err != nil {
		return selftest.Failed(fmt.Errorf("the preferences file did not load after a restart: %v", err))
	}
	after := restarted.Settings().SFTPEditor
	if after.DefaultOpener.Kind != domainpreferences.OpenerExternal ||
		after.DefaultOpener.Application.Name != "Other" {
		return selftest.Failed(fmt.Errorf("the default opener after a restart = %+v", after.DefaultOpener))
	}
	if association, ok := after.AssociationFor("txt"); !ok ||
		association.Kind != domainpreferences.OpenerExternal ||
		association.Application.Name != "Viewer" {
		return selftest.Failed(fmt.Errorf("the txt association after a restart = %+v, %v", association, ok))
	}
	t.settingsView.Frame()
	if !t.settingsView.HasText("Other") {
		return selftest.Failed(fmt.Errorf("the Settings window does not show the default application: %q",
			t.settingsView.Texts()))
	}

	if t.cleanup != nil {
		t.cleanup()
		t.cleanup = nil
	}
	return selftest.Passed(
		"an unlisted extension and an extensionless file both used the default application, and a fresh preferences service read the default and the association back",
		"transferstate.State.Open -> application/fileopen.Service (default fallback) + adapters/store.PreferencesStore (restart)")
}

func (t *selftest37) localSettled() bool {
	tab := t.transfer.Tab(transferstate.Left)
	return tab != nil && tab.FS != nil && !tab.Loading && tab.Path != ""
}

func samePath(left, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func entryByName(state *transferstate.State, side transferstate.Side,
	name string) (domainsftp.Entry, bool) {
	tab := state.Tab(side)
	if tab == nil {
		return domainsftp.Entry{}, false
	}
	for _, entry := range tab.Entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return domainsftp.Entry{}, false
}
