package vault

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// viewSnippets is the Snippet seam for the Vault view tests. It applies the
// domain rules and records what the view saved.
type viewSnippets struct {
	items    []domainvault.Snippet
	packages []domainvault.SnippetPackage
	hosts    *fakeHosts
	nextID   int
}

func (f *viewSnippets) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	return append([]domainvault.Snippet(nil), f.items...), nil
}

func (f *viewSnippets) CreateSnippet(_ context.Context, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	f.nextID++
	snippet := domainvault.Snippet{
		ID:               "snippet-" + strconv.Itoa(f.nextID),
		Label:            normalized.Label,
		Command:          normalized.Command,
		Package:          normalized.Package,
		Tags:             normalized.Tags,
		Targets:          normalized.Targets,
		TargetGroups:     normalized.TargetGroups,
		TargetsAllHosts:  normalized.TargetsAllHosts,
		NoAutoRun:        normalized.NoAutoRun,
		MultiLineRunMode: normalized.MultiLineRunMode,
	}
	f.items = append(f.items, snippet)
	return snippet, nil
}

func (f *viewSnippets) UpdateSnippet(_ context.Context, id string, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for i := range f.items {
		if f.items[i].ID != id {
			continue
		}
		f.items[i].Label = normalized.Label
		f.items[i].Command = normalized.Command
		f.items[i].Package = normalized.Package
		f.items[i].Tags = normalized.Tags
		f.items[i].Targets = normalized.Targets
		f.items[i].TargetGroups = normalized.TargetGroups
		f.items[i].TargetsAllHosts = normalized.TargetsAllHosts
		f.items[i].NoAutoRun = normalized.NoAutoRun
		f.items[i].MultiLineRunMode = normalized.MultiLineRunMode
		return f.items[i], nil
	}
	return domainvault.Snippet{}, appvault.ErrNotFound
}

func (f *viewSnippets) DeleteSnippet(_ context.Context, id string) error {
	for i := range f.items {
		if f.items[i].ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (f *viewSnippets) PrepareSnippet(_ context.Context, id string, values map[string]string) (appvault.SnippetRun, error) {
	for _, snippet := range f.items {
		if snippet.ID != id {
			continue
		}
		command, missing := domainvault.ApplySnippetVariables(snippet.Command, values)
		if len(missing) > 0 {
			return appvault.SnippetRun{}, &appvault.Error{
				Kind:  appvault.KindValidation,
				Cause: &appvault.SnippetVariableError{Missing: missing},
			}
		}
		hosts, _ := f.hosts.List(context.Background())
		return appvault.SnippetRun{
			Snippet: snippet,
			Command: command,
			Values:  values,
			Hosts:   domainvault.SnippetRunnableHosts(snippet, hosts),
		}, nil
	}
	return appvault.SnippetRun{}, appvault.ErrNotFound
}

func (f *viewSnippets) SaveSnippetVariableValues(_ context.Context, id string, values map[string]string) error {
	for i := range f.items {
		if f.items[i].ID != id {
			continue
		}
		if f.items[i].VariableValues == nil {
			f.items[i].VariableValues = make(map[string]string)
		}
		for name, value := range values {
			f.items[i].VariableValues[name] = value
		}
		return nil
	}
	return appvault.ErrNotFound
}

func (f *viewSnippets) ListSnippetPackages(context.Context) ([]domainvault.SnippetPackage, error) {
	saved := make(map[string]domainvault.SnippetPackage, len(f.packages))
	paths := make([]string, 0, len(f.packages))
	for _, pkg := range f.packages {
		saved[pkg.Path] = pkg
		paths = append(paths, pkg.Path)
	}
	out := make([]domainvault.SnippetPackage, 0, len(paths))
	for _, path := range domainvault.CollectSnippetPackagePaths(paths, f.items) {
		if pkg, ok := saved[path]; ok {
			out = append(out, pkg)
			continue
		}
		out = append(out, domainvault.SnippetPackage{Path: path})
	}
	return out, nil
}

func (f *viewSnippets) CreateSnippetPackage(_ context.Context, draft domainvault.SnippetPackageDraft) (domainvault.SnippetPackage, error) {
	normalized, err := domainvault.NormalizeSnippetPackageDraft(draft)
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	pkg := domainvault.SnippetPackage{Path: normalized.Path}
	f.packages = append(f.packages, pkg)
	return pkg, nil
}

func (f *viewSnippets) RenameSnippetPackage(_ context.Context, path, newPath string) (domainvault.SnippetPackage, error) {
	oldPath, err := domainvault.NormalizeSnippetPackagePath(path)
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	normalized, err := domainvault.NormalizeSnippetPackageDraft(domainvault.SnippetPackageDraft{Path: newPath})
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for i := range f.packages {
		if !domainvault.SnippetPackageAtOrBelow(f.packages[i].Path, oldPath) {
			continue
		}
		f.packages[i].Path = domainvault.ReplaceSnippetPackagePrefix(
			f.packages[i].Path, oldPath, normalized.Path)
	}
	for i := range f.items {
		if domainvault.SnippetPackageAtOrBelow(f.items[i].Package, oldPath) {
			f.items[i].Package = domainvault.ReplaceSnippetPackagePrefix(
				f.items[i].Package, oldPath, normalized.Path)
		}
	}
	return domainvault.SnippetPackage{Path: normalized.Path}, nil
}

func (f *viewSnippets) DeleteSnippetPackage(_ context.Context, path string) error {
	root, err := domainvault.NormalizeSnippetPackagePath(path)
	if err != nil {
		return &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	kept := make([]domainvault.SnippetPackage, 0, len(f.packages))
	for _, pkg := range f.packages {
		if !domainvault.SnippetPackageAtOrBelow(pkg.Path, root) {
			kept = append(kept, pkg)
		}
	}
	f.packages = kept
	for i := range f.items {
		if domainvault.SnippetPackageAtOrBelow(f.items[i].Package, root) {
			f.items[i].Package = ""
		}
	}
	return nil
}

type viewRunner struct {
	activeHost    model.HostID
	hasHost       bool
	activeSent    []string
	hostSent      []string
	hostIDs       []model.HostID
	failTransport bool
}

func (r *viewRunner) HasActiveSession() bool { return r.hasHost }

func (r *viewRunner) ActiveHostID() (model.HostID, bool) { return r.activeHost, r.hasHost }

func (r *viewRunner) SendToActiveSession(_ domainvault.Snippet, command string) error {
	if r.failTransport {
		return errors.New("transport failed")
	}
	r.activeSent = append(r.activeSent, command)
	return nil
}

func (r *viewRunner) SendToHosts(hosts []domainvault.Host, _ domainvault.Snippet, command string) error {
	if r.failTransport {
		return errors.New("transport failed")
	}
	for _, host := range hosts {
		r.hostIDs = append(r.hostIDs, host.ID)
	}
	r.hostSent = append(r.hostSent, command)
	return nil
}

func newSnippetView(t *testing.T, hostItems []domainvault.Host, snippets ...domainvault.Snippet) (*vaultstate.State, *viewSnippets, *viewRunner, *ui.Tester) {
	t.Helper()
	hosts := &fakeHosts{items: hostItems}
	repo := &viewSnippets{items: snippets, hosts: hosts}
	runner := &viewRunner{}
	state := vaultstate.NewState(hosts,
		vaultstate.WithSnippets(repo),
		vaultstate.WithSnippetRunner(runner),
	)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, repo, runner, ui.NewTester(View(state), 900, 700)
}

func TestVaultViewListsSnippetsAndPackages(t *testing.T) {
	m := locales.In("en")
	_, _, _, tt := newSnippetView(t, nil,
		domainvault.Snippet{
			ID: "snippet-1", Label: "Restart nginx", Command: "systemctl restart {{service:nginx}}",
			Package: "ops/linux", Tags: []string{"web"},
			TargetGroups: []string{"prod"}, MultiLineRunMode: domainvault.SnippetRunLineDelay,
		},
	)
	if !tt.HasText(m.VaultSnippetListTitle(1)) {
		t.Fatalf("Snippet heading missing, texts %q", tt.Texts())
	}
	for _, want := range []string{
		"ops/linux", "Restart nginx", m.VaultSnippetSummaryTargets("prod"),
		m.VaultSnippetVariableCount(1), m.VaultSnippetLineDelay(),
		m.VaultSnippetRunActive(), m.VaultSnippetRunHosts(), "web",
	} {
		if !tt.HasText(want) {
			t.Errorf("Vault does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestVaultViewCreatesASnippetThroughTheEditor(t *testing.T) {
	m := locales.In("en")
	state, repo, _, tt := newSnippetView(t, nil)
	scrollToEnd(tt, 900, 700)

	if err := tt.Click(m.VaultSnippetNew()); err != nil {
		t.Fatalf("click New Snippet: %v", err)
	}
	tt.Frame()
	for _, want := range []string{
		m.VaultSnippetFormCreateTitle(), m.VaultSnippetLabel(), m.VaultSnippetCommand(),
		m.VaultSnippetPackage(), m.VaultSnippetAllHosts(), m.VaultHostSave(),
	} {
		if !tt.HasText(want) {
			t.Errorf("Snippet editor does not show %q, texts %q", want, tt.Texts())
		}
	}

	form := state.SnippetForm()
	form.Label = "Deploy"
	form.Command = "deploy --service {{service:api}}"
	form.Package = "ops"
	tt.Frame()
	if err := tt.Click(m.VaultHostSave()); err != nil {
		t.Fatalf("click Save: %v", err)
	}
	tt.Frame()
	if state.EditingSnippet() {
		t.Fatal("Save did not close the Snippet editor")
	}
	if len(repo.items) != 1 || repo.items[0].Label != "Deploy" || repo.items[0].Package != "ops" {
		t.Fatalf("saved Snippet = %+v", repo.items)
	}
	if !tt.HasText("Deploy") || !tt.HasText("ops") {
		t.Fatalf("the saved Snippet is not listed, texts %q", tt.Texts())
	}
}

func TestVaultViewCreatesASnippetPackageThroughTheEditor(t *testing.T) {
	m := locales.In("en")
	state, repo, _, tt := newSnippetView(t, nil)
	scrollToEnd(tt, 900, 700)

	if err := tt.Click(m.VaultSnippetPackageNew()); err != nil {
		t.Fatalf("click New package: %v", err)
	}
	tt.Frame()
	if !tt.HasText(m.VaultSnippetPackageFormCreateTitle()) {
		t.Fatalf("package editor is not shown, texts %q", tt.Texts())
	}
	state.SnippetPackageForm().Path = "ops/linux"
	tt.Frame()
	if err := tt.Click(m.VaultHostSave()); err != nil {
		t.Fatalf("click Save: %v", err)
	}
	tt.Frame()
	if len(repo.packages) != 1 || repo.packages[0].Path != "ops/linux" {
		t.Fatalf("saved packages = %+v", repo.packages)
	}
	if !tt.HasText("ops/linux") {
		t.Fatalf("the saved package is not listed, texts %q", tt.Texts())
	}
}

func TestVaultViewRunPanelSubstitutesAndShowsThePreview(t *testing.T) {
	m := locales.In("en")
	state, repo, runner, tt := newSnippetView(t, nil, domainvault.Snippet{
		ID: "snippet-1", Label: "Deploy", Command: "deploy --service {{service:api}}",
	})
	runner.hasHost = true
	runner.activeHost = "host-1"
	scrollToEnd(tt, 900, 700)

	if err := tt.Click(m.VaultSnippetRunActive()); err != nil {
		t.Fatalf("click Run here: %v", err)
	}
	tt.Frame()
	for _, want := range []string{
		m.VaultSnippetRunTitle("Deploy"), m.VaultSnippetRunDestinationActive(),
		m.VaultSnippetRunVariable("service"), m.VaultSnippetRunVariableDefault("api"),
		m.VaultSnippetRunPreview(), "deploy --service api", m.VaultSnippetVariablesHint(),
		m.VaultSnippetRunConfirm(),
	} {
		if !tt.HasText(want) {
			t.Errorf("run panel does not show %q, texts %q", want, tt.Texts())
		}
	}

	state.SnippetRunForm().SetValue("service", "billing")
	tt.Frame()
	if !tt.HasText("deploy --service billing") {
		t.Fatalf("the preview did not follow the input, texts %q", tt.Texts())
	}
	if err := tt.Click(m.VaultSnippetRunConfirm()); err != nil {
		t.Fatalf("click Run now: %v", err)
	}
	tt.Frame()
	if state.SnippetRunOpen() {
		t.Fatal("the run panel stayed open after a successful run")
	}
	if len(runner.activeSent) != 1 || runner.activeSent[0] != "deploy --service billing" {
		t.Fatalf("sent commands = %v", runner.activeSent)
	}
	if repo.items[0].VariableValues["service"] != "billing" {
		t.Fatalf("remembered values = %v", repo.items[0].VariableValues)
	}
}

func TestVaultViewRunPanelShowsMissingVariablesAndSendsNothing(t *testing.T) {
	m := locales.In("en")
	state, _, runner, tt := newSnippetView(t, nil, domainvault.Snippet{
		ID: "snippet-1", Label: "Login", Command: "ssh {{user}}@{{host}}",
	})
	runner.hasHost = true
	runner.activeHost = "host-1"
	scrollToEnd(tt, 900, 700)

	if err := tt.Click(m.VaultSnippetRunActive()); err != nil {
		t.Fatalf("click Run here: %v", err)
	}
	tt.Frame()
	if err := tt.Click(m.VaultSnippetRunConfirm()); err != nil {
		t.Fatalf("click Run now: %v", err)
	}
	tt.Frame()
	if !state.SnippetRunOpen() {
		t.Fatal("the run panel closed after a missing variable")
	}
	if !tt.HasText(m.VaultSnippetMissingVariables("user, host")) {
		t.Fatalf("the missing variables are not named, texts %q", tt.Texts())
	}
	if !tt.HasText(m.VaultSnippetProblemMissingVariables()) {
		t.Fatalf("the localized missing-variable problem is not shown, texts %q", tt.Texts())
	}
	if len(runner.activeSent) != 0 {
		t.Fatalf("an incomplete command reached the Session: %v", runner.activeSent)
	}
}

func TestVaultViewRunHereWithoutASessionShowsTheLocalizedProblem(t *testing.T) {
	m := locales.In("en")
	state, _, runner, tt := newSnippetView(t, nil, domainvault.Snippet{
		ID: "snippet-1", Label: "Uptime", Command: "uptime",
	})
	scrollToEnd(tt, 900, 700)

	if err := tt.Click(m.VaultSnippetRunActive()); err != nil {
		t.Fatalf("click Run here: %v", err)
	}
	tt.Frame()
	if state.Problem() != vaultstate.ProblemSnippetNoSession {
		t.Fatalf("problem = %q", state.Problem())
	}
	if !tt.HasText(m.VaultSnippetProblemNoSession()) {
		t.Fatalf("the localized no-Session problem is not shown, texts %q", tt.Texts())
	}
	if len(runner.activeSent) != 0 {
		t.Fatalf("a command reached a Session: %v", runner.activeSent)
	}
}

func TestVaultViewRunOnTargetsUsesTheResolvedHosts(t *testing.T) {
	m := locales.In("en")
	state, _, runner, tt := newSnippetView(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH, Group: "prod"},
		{ID: "host-2", Label: "db", Hostname: "10.0.0.2", Protocol: domainvault.ProtocolSSH, Group: "dev"},
	}, domainvault.Snippet{
		ID: "snippet-1", Label: "Restart", Command: "systemctl restart app",
		TargetGroups: []string{"prod"},
	})
	scrollToEnd(tt, 900, 700)

	if err := tt.Click(m.VaultSnippetRunHosts()); err != nil {
		t.Fatalf("click Run on targets: %v", err)
	}
	tt.Frame()
	if state.Problem() != vaultstate.ProblemNone {
		t.Fatalf("run problem = %q", state.Problem())
	}
	if len(runner.hostIDs) != 1 || runner.hostIDs[0] != "host-1" {
		t.Fatalf("target Hosts = %v, want only the prod Host", runner.hostIDs)
	}
	if len(runner.hostSent) != 1 || runner.hostSent[0] != "systemctl restart app" {
		t.Fatalf("host commands = %v", runner.hostSent)
	}
}

func TestSnippetEditorScrollsToItsActionsAtBothSizes(t *testing.T) {
	m := locales.In("en")
	for _, size := range editorSizes {
		t.Run(strconv.Itoa(size.width)+"x"+strconv.Itoa(size.height), func(t *testing.T) {
			state, _, _, tt := newSnippetView(t, nil, domainvault.Snippet{
				ID: "snippet-1", Label: "A very long label for a Snippet that must still lay out",
				Command: "echo one\necho two\necho three",
				Package: "ops/linux", Tags: []string{"prod", "web", "linux", "long-tag"},
			})
			tt = ui.NewTester(View(state), size.width, size.height)
			if !state.BeginEditSnippet("snippet-1") {
				t.Fatal("BeginEditSnippet rejected the listed Snippet")
			}
			tt.Frame()
			scrollToEnd(tt, size.width, size.height)
			assertVisible(t, tt, m.VaultSnippetNoAutoRunLabel(), size.width, size.height)
			assertVisible(t, tt, m.VaultHostSave(), size.width, size.height)
			assertVisible(t, tt, m.VaultHostCancel(), size.width, size.height)
			assertNoHorizontalOverflow(t, tt, size.width, size.height)
		})
	}
}

func TestSnippetListFitsAt720x480(t *testing.T) {
	m := locales.In("en")
	state, _, _, _ := newSnippetView(t, []domainvault.Host{
		{ID: "host-1", Label: "a-host-with-a-long-name-for-the-target-summary",
			Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH, Group: "prod/web"},
	}, domainvault.Snippet{
		ID: "snippet-1", Label: "A very long Snippet label that must wrap instead of clipping",
		Command: "echo one\necho two", Package: "ops/linux",
		Tags: []string{"production", "web", "linux"}, Targets: []model.HostID{"host-1"},
		TargetGroups: []string{"prod/web"},
	})
	tt := ui.NewTester(View(state), 720, 480)
	scrollToEnd(tt, 720, 480)
	assertVisible(t, tt, m.VaultSnippetRunActive(), 720, 480)
	assertVisible(t, tt, "A very long Snippet label that must wrap instead of clipping", 720, 480)
	assertVisible(t, tt, "ops/linux", 720, 480)
	assertNoHorizontalOverflow(t, tt, 720, 480)
}
