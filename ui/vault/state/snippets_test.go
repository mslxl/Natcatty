package state

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeSnippets is the Snippet use-case seam. It applies the real domain rules
// and keeps all state in memory.
type fakeSnippets struct {
	snippets    []domainvault.Snippet
	packages    []domainvault.SnippetPackage
	hosts       *fakeHosts
	nextID      int
	savedValues map[string]map[string]string
}

func (f *fakeSnippets) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	return append([]domainvault.Snippet(nil), f.snippets...), nil
}

func (f *fakeSnippets) CreateSnippet(_ context.Context, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
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
	f.snippets = append(f.snippets, snippet)
	return snippet, nil
}

func (f *fakeSnippets) UpdateSnippet(_ context.Context, id string, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for index := range f.snippets {
		if f.snippets[index].ID != id {
			continue
		}
		f.snippets[index].Label = normalized.Label
		f.snippets[index].Command = normalized.Command
		f.snippets[index].Package = normalized.Package
		f.snippets[index].Tags = normalized.Tags
		f.snippets[index].Targets = normalized.Targets
		f.snippets[index].TargetGroups = normalized.TargetGroups
		f.snippets[index].TargetsAllHosts = normalized.TargetsAllHosts
		f.snippets[index].NoAutoRun = normalized.NoAutoRun
		f.snippets[index].MultiLineRunMode = normalized.MultiLineRunMode
		return f.snippets[index], nil
	}
	return domainvault.Snippet{}, appvault.ErrNotFound
}

func (f *fakeSnippets) DeleteSnippet(_ context.Context, id string) error {
	for index := range f.snippets {
		if f.snippets[index].ID == id {
			f.snippets = append(f.snippets[:index], f.snippets[index+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (f *fakeSnippets) PrepareSnippet(_ context.Context, id string, values map[string]string) (appvault.SnippetRun, error) {
	for _, snippet := range f.snippets {
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

func (f *fakeSnippets) SaveSnippetVariableValues(_ context.Context, id string, values map[string]string) error {
	for index := range f.snippets {
		if f.snippets[index].ID != id {
			continue
		}
		if f.snippets[index].VariableValues == nil {
			f.snippets[index].VariableValues = make(map[string]string)
		}
		for name, value := range values {
			f.snippets[index].VariableValues[name] = value
		}
		return nil
	}
	return appvault.ErrNotFound
}

func (f *fakeSnippets) ListSnippetPackages(context.Context) ([]domainvault.SnippetPackage, error) {
	saved := make(map[string]domainvault.SnippetPackage, len(f.packages))
	paths := make([]string, 0, len(f.packages))
	for _, pkg := range f.packages {
		saved[pkg.Path] = pkg
		paths = append(paths, pkg.Path)
	}
	all := domainvault.CollectSnippetPackagePaths(paths, f.snippets)
	out := make([]domainvault.SnippetPackage, 0, len(all))
	for _, path := range all {
		if pkg, ok := saved[path]; ok {
			out = append(out, pkg)
			continue
		}
		out = append(out, domainvault.SnippetPackage{Path: path})
	}
	return out, nil
}

func (f *fakeSnippets) CreateSnippetPackage(_ context.Context, draft domainvault.SnippetPackageDraft) (domainvault.SnippetPackage, error) {
	normalized, err := domainvault.NormalizeSnippetPackageDraft(draft)
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for _, pkg := range f.packages {
		if pkg.Path == normalized.Path {
			return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindConflict}
		}
	}
	pkg := domainvault.SnippetPackage{Path: normalized.Path}
	f.packages = append(f.packages, pkg)
	return pkg, nil
}

func (f *fakeSnippets) RenameSnippetPackage(_ context.Context, path, newPath string) (domainvault.SnippetPackage, error) {
	oldPath, err := domainvault.NormalizeSnippetPackagePath(path)
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	normalized, err := domainvault.NormalizeSnippetPackageDraft(domainvault.SnippetPackageDraft{Path: newPath})
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	renamed := domainvault.SnippetPackage{Path: normalized.Path}
	kept := make([]domainvault.SnippetPackage, 0, len(f.packages))
	for _, pkg := range f.packages {
		if !domainvault.SnippetPackageAtOrBelow(pkg.Path, oldPath) {
			kept = append(kept, pkg)
			continue
		}
		next := pkg
		next.Path = domainvault.ReplaceSnippetPackagePrefix(pkg.Path, oldPath, normalized.Path)
		kept = append(kept, next)
	}
	f.packages = kept
	for index := range f.snippets {
		if !domainvault.SnippetPackageAtOrBelow(f.snippets[index].Package, oldPath) ||
			f.snippets[index].Package == "" {
			continue
		}
		f.snippets[index].Package = domainvault.ReplaceSnippetPackagePrefix(
			f.snippets[index].Package, oldPath, normalized.Path)
	}
	return renamed, nil
}

func (f *fakeSnippets) DeleteSnippetPackage(_ context.Context, path string) error {
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
	for index := range f.snippets {
		if domainvault.SnippetPackageAtOrBelow(f.snippets[index].Package, root) {
			f.snippets[index].Package = ""
		}
	}
	return nil
}

type recordingRunner struct {
	activeHost       model.HostID
	hasActiveHost    bool
	hasActiveSession bool
	activeCommands   []string
	hostIDs          []model.HostID
	hostCommands     []string
	err              error
}

func (r *recordingRunner) HasActiveSession() bool {
	return r.hasActiveSession || r.hasActiveHost
}

func (r *recordingRunner) ActiveHostID() (model.HostID, bool) {
	return r.activeHost, r.hasActiveHost
}

func (r *recordingRunner) SendToActiveSession(_ domainvault.Snippet, command string) error {
	if r.err != nil {
		return r.err
	}
	r.activeCommands = append(r.activeCommands, command)
	return nil
}

func (r *recordingRunner) SendToHosts(hosts []domainvault.Host, _ domainvault.Snippet, command string) error {
	if r.err != nil {
		return r.err
	}
	for _, host := range hosts {
		r.hostIDs = append(r.hostIDs, host.ID)
	}
	r.hostCommands = append(r.hostCommands, command)
	return nil
}

func snippetPackagePaths(state *State) []string {
	paths := make([]string, 0, state.SnippetPackageCount())
	for _, pkg := range state.SnippetPackages() {
		paths = append(paths, pkg.Path)
	}
	return paths
}

func newSnippetState(t *testing.T, hostItems []domainvault.Host) (*State, *fakeSnippets, *recordingRunner) {
	t.Helper()
	hosts := &fakeHosts{items: hostItems}
	repo := &fakeSnippets{hosts: hosts}
	runner := &recordingRunner{}
	state := NewState(hosts, WithSnippets(repo), WithSnippetRunner(runner))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	return state, repo, runner
}

func TestSnippetCRUDPersistsThroughTheStateSeam(t *testing.T) {
	state, repo, _ := newSnippetState(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH, Group: "prod"},
	})

	state.BeginCreateSnippet()
	if !state.EditingSnippet() {
		t.Fatal("BeginCreateSnippet did not open the editor")
	}
	form := state.SnippetForm()
	form.Label = "Restart nginx"
	form.Command = "systemctl restart nginx"
	form.Package = "ops/linux"
	form.Tags = "web, ops"
	form.NoAutoRun = true
	form.MultiLineRunMode = string(domainvault.SnippetRunLineDelay)
	form.TargetHosts["host-1"] = true
	form.TargetGroups["prod"] = true
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	if state.EditingSnippet() {
		t.Fatal("SaveSnippet left the editor open")
	}
	if got := state.SnippetCount(); got != 1 {
		t.Fatalf("SnippetCount = %d, want 1", got)
	}
	snippet := state.Snippets()[0]
	if snippet.Label != "Restart nginx" || snippet.Package != "ops/linux" ||
		!reflect.DeepEqual(snippet.Tags, []string{"web", "ops"}) ||
		!snippet.NoAutoRun || snippet.MultiLineRunMode != domainvault.SnippetRunLineDelay {
		t.Fatalf("saved Snippet = %+v", snippet)
	}
	if !reflect.DeepEqual(snippet.Targets, []model.HostID{"host-1"}) ||
		!reflect.DeepEqual(snippet.TargetGroups, []string{"prod"}) {
		t.Fatalf("saved targets = %v / %v", snippet.Targets, snippet.TargetGroups)
	}

	if !state.BeginEditSnippet(snippet.ID) {
		t.Fatal("BeginEditSnippet rejected a listed Snippet")
	}
	state.SnippetForm().Label = "Restart nginx now"
	state.SnippetForm().TargetsAllHosts = true
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("update SaveSnippet: %v", err)
	}
	if got := state.Snippets()[0]; got.Label != "Restart nginx now" || !got.TargetsAllHosts ||
		len(got.Targets) != 0 || len(got.TargetGroups) != 0 {
		t.Fatalf("updated Snippet = %+v", got)
	}

	state.BeginCreateSnippetPackage()
	state.SnippetPackageForm().Path = "ops/linux"
	if err := state.SaveSnippetPackage(); err != nil {
		t.Fatalf("SaveSnippetPackage: %v", err)
	}
	if !reflect.DeepEqual(snippetPackagePaths(state), []string{"ops", "ops/linux"}) {
		t.Fatalf("packages = %v, want the saved path and its ancestor", snippetPackagePaths(state))
	}

	if !state.BeginEditSnippetPackage("ops/linux") {
		t.Fatal("BeginEditSnippetPackage rejected an existing package")
	}
	state.SnippetPackageForm().Path = "ops/web"
	if err := state.SaveSnippetPackage(); err != nil {
		t.Fatalf("rename SaveSnippetPackage: %v", err)
	}
	if got := state.Snippets()[0].Package; got != "ops/web" {
		t.Fatalf("Snippet package after rename = %q, want ops/web", got)
	}

	state.RequestDeleteSnippet(snippet.ID)
	if state.PendingDeleteSnippet() != snippet.ID {
		t.Fatal("RequestDeleteSnippet did not ask for confirmation")
	}
	if err := state.ConfirmDeleteSnippet(); err != nil {
		t.Fatalf("ConfirmDeleteSnippet: %v", err)
	}
	if state.SnippetCount() != 0 || len(repo.snippets) != 0 {
		t.Fatalf("Snippet count after delete = %d", state.SnippetCount())
	}

	state.RequestDeleteSnippetPackage("ops/web")
	if err := state.ConfirmDeleteSnippetPackage(); err != nil {
		t.Fatalf("ConfirmDeleteSnippetPackage: %v", err)
	}
	if len(snippetPackagePaths(state)) != 0 {
		t.Fatalf("packages after delete = %v", snippetPackagePaths(state))
	}
}

func TestSnippetValidationProblemsAreLocalizable(t *testing.T) {
	state, _, _ := newSnippetState(t, nil)

	state.BeginCreateSnippet()
	state.SnippetForm().Command = "ls"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	if state.Problem() != ProblemSnippet || !state.EditingSnippet() {
		t.Fatalf("blank-label problem = %q, editing = %v", state.Problem(), state.EditingSnippet())
	}

	state.SnippetForm().Label = "List"
	state.SnippetForm().Package = "bad package"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	if state.Problem() != ProblemSnippetPackage {
		t.Fatalf("invalid-package problem = %q, want %q", state.Problem(), ProblemSnippetPackage)
	}

	state.CancelSnippetEdit()
	state.BeginCreateSnippetPackage()
	state.SnippetPackageForm().Path = "bad package"
	if err := state.SaveSnippetPackage(); err != nil {
		t.Fatalf("SaveSnippetPackage: %v", err)
	}
	if state.Problem() != ProblemSnippetPackage || !state.EditingSnippetPackage() {
		t.Fatalf("package problem = %q, editing = %v", state.Problem(), state.EditingSnippetPackage())
	}
}

func TestSnippetRunSubstitutesVariablesBeforeSending(t *testing.T) {
	state, repo, runner := newSnippetState(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH},
	})
	runner.hasActiveHost = true
	runner.activeHost = "host-1"

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Deploy"
	state.SnippetForm().Command = "deploy {{service:api}} --pod {{.Names}} --literal '{{name}}'"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	snippet := state.Snippets()[0]

	if !state.BeginSnippetRun(snippet.ID, SnippetDestinationActive) {
		t.Fatal("BeginSnippetRun did not open the variable form")
	}
	if !state.SnippetRunOpen() {
		t.Fatal("a Snippet with variables must not run before substitution")
	}
	if len(runner.activeCommands) != 0 {
		t.Fatalf("a command reached the Session before confirmation: %v", runner.activeCommands)
	}
	form := state.SnippetRunForm()
	if got := form.Value("service"); got != "api" {
		t.Fatalf("prefilled service = %q, want the default api", got)
	}
	if got := form.Value("name"); got != "" {
		t.Fatalf("unknown variable name was prefilled with %q", got)
	}

	form.SetValue("service", "billing")
	form.SetValue("name", "web-1")
	if !state.ConfirmSnippetRun() {
		t.Fatalf("ConfirmSnippetRun failed: %v", state.SnippetRunProblem())
	}
	want := "deploy billing --pod {{.Names}} --literal 'web-1'"
	if len(runner.activeCommands) != 1 || runner.activeCommands[0] != want {
		t.Fatalf("sent commands = %v, want exactly [%q]", runner.activeCommands, want)
	}
	if state.SnippetRunOpen() {
		t.Fatal("the run form stayed open after a successful run")
	}
	if repo.snippets[0].VariableValues["service"] != "billing" ||
		repo.snippets[0].VariableValues["name"] != "web-1" {
		t.Fatalf("remembered values = %v", repo.snippets[0].VariableValues)
	}
}

func TestSnippetRunWithoutVariablesExecutesImmediately(t *testing.T) {
	state, _, runner := newSnippetState(t, nil)
	runner.hasActiveHost = true
	runner.activeHost = "host-1"

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Uptime"
	state.SnippetForm().Command = "uptime"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	if !state.BeginSnippetRun(state.Snippets()[0].ID, SnippetDestinationActive) {
		t.Fatal("BeginSnippetRun rejected a plain Snippet")
	}
	if state.SnippetRunOpen() {
		t.Fatal("a Snippet without variables opened the run form")
	}
	if !reflect.DeepEqual(runner.activeCommands, []string{"uptime"}) {
		t.Fatalf("sent commands = %v", runner.activeCommands)
	}
}

func TestSnippetRunNamesEveryMissingVariableAndSendsNothing(t *testing.T) {
	state, _, runner := newSnippetState(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH},
	})
	runner.hasActiveHost = true
	runner.activeHost = "host-1"

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Login"
	state.SnippetForm().Command = "ssh {{user}}@{{host}}"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	snippet := state.Snippets()[0]
	if !state.BeginSnippetRun(snippet.ID, SnippetDestinationActive) {
		t.Fatal("BeginSnippetRun rejected the Snippet")
	}

	state.SnippetRunForm().SetValue("user", "deploy")
	if state.ConfirmSnippetRun() {
		t.Fatal("ConfirmSnippetRun accepted a blank required variable")
	}
	if state.SnippetRunProblem() != ProblemSnippetMissingVariables ||
		state.Problem() != ProblemSnippetMissingVariables {
		t.Fatalf("problem = %q / %q", state.SnippetRunProblem(), state.Problem())
	}
	if !reflect.DeepEqual(state.SnippetRunMissing(), []string{"host"}) {
		t.Fatalf("missing variables = %v, want [host]", state.SnippetRunMissing())
	}
	if !state.SnippetRunOpen() {
		t.Fatal("the form closed after a missing variable")
	}
	if len(runner.activeCommands) != 0 {
		t.Fatalf("an incomplete command reached the Session: %v", runner.activeCommands)
	}

	state.SnippetRunForm().SetValue("host", "db.example.com")
	if !state.ConfirmSnippetRun() {
		t.Fatalf("ConfirmSnippetRun failed after filling every value: %v", state.SnippetRunProblem())
	}
	if !reflect.DeepEqual(runner.activeCommands, []string{"ssh deploy@db.example.com"}) {
		t.Fatalf("sent commands = %v", runner.activeCommands)
	}
}

func TestSnippetRunTargetsSelectedHostsAndTheActiveSession(t *testing.T) {
	state, _, runner := newSnippetState(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH, Group: "prod"},
		{ID: "host-2", Label: "db", Hostname: "10.0.0.2", Protocol: domainvault.ProtocolSSH, Group: "dev"},
		{ID: "host-3", Label: "cache", Hostname: "10.0.0.3", Protocol: domainvault.ProtocolSSH, Group: "stage"},
	})

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Restart"
	state.SnippetForm().Command = "systemctl restart app"
	state.SnippetForm().TargetHosts["host-2"] = true
	state.SnippetForm().TargetGroups["prod"] = true
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	snippet := state.Snippets()[0]

	if !state.BeginSnippetRun(snippet.ID, SnippetDestinationHosts) {
		t.Fatal("BeginSnippetRun (hosts) rejected the Snippet")
	}
	if !reflect.DeepEqual(runner.hostIDs, []model.HostID{"host-1", "host-2"}) {
		t.Fatalf("target Hosts = %v, want host-1 (group) then host-2 (explicit)", runner.hostIDs)
	}
	if !reflect.DeepEqual(runner.hostCommands, []string{"systemctl restart app"}) {
		t.Fatalf("host commands = %v", runner.hostCommands)
	}

	runner.hasActiveHost = true
	runner.activeHost = "host-3"
	if state.BeginSnippetRun(snippet.ID, SnippetDestinationActive) {
		t.Fatal("the Snippet ran in an out-of-scope active Session")
	}
	if state.Problem() != ProblemSnippetTargets {
		t.Fatalf("out-of-scope problem = %q", state.Problem())
	}
	if len(runner.activeCommands) != 0 {
		t.Fatalf("an out-of-scope command reached the Session: %v", runner.activeCommands)
	}

	runner.activeHost = "host-1"
	if !state.BeginSnippetRun(snippet.ID, SnippetDestinationActive) {
		t.Fatal("the Snippet did not run in a targeted active Session")
	}
	if !reflect.DeepEqual(runner.activeCommands, []string{"systemctl restart app"}) {
		t.Fatalf("active commands = %v", runner.activeCommands)
	}
}

func TestSnippetRunWithoutAFocusedSessionReportsNoSession(t *testing.T) {
	state, _, runner := newSnippetState(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH},
	})

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Uptime"
	state.SnippetForm().Command = "uptime"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	snippet := state.Snippets()[0]

	if state.BeginSnippetRun(snippet.ID, SnippetDestinationActive) {
		t.Fatal("Run here succeeded without a focused Session")
	}
	if state.Problem() != ProblemSnippetNoSession {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemSnippetNoSession)
	}
	if len(runner.activeCommands) != 0 {
		t.Fatalf("a command reached a Session: %v", runner.activeCommands)
	}
}

func TestSnippetRunAllHostsOpensEveryConnectableHost(t *testing.T) {
	state, _, runner := newSnippetState(t, []domainvault.Host{
		{ID: "host-1", Label: "web", Hostname: "10.0.0.1", Protocol: domainvault.ProtocolSSH},
		{ID: "host-2", Label: "db", Hostname: "10.0.0.2", Protocol: domainvault.ProtocolTelnet},
	})

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Uptime"
	state.SnippetForm().Command = "uptime"
	state.SnippetForm().TargetsAllHosts = true
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	if !state.BeginSnippetRun(state.Snippets()[0].ID, SnippetDestinationHosts) {
		t.Fatal("an all-hosts Snippet without variables did not run immediately")
	}
	if !reflect.DeepEqual(runner.hostIDs, []model.HostID{"host-1", "host-2"}) {
		t.Fatalf("all-hosts targets = %v", runner.hostIDs)
	}
}

func TestSnippetRunFailureKeepsTheFormAndReportsALocalizableProblem(t *testing.T) {
	state, _, runner := newSnippetState(t, nil)
	runner.hasActiveHost = true
	runner.activeHost = "host-1"

	state.BeginCreateSnippet()
	state.SnippetForm().Label = "Deploy"
	state.SnippetForm().Command = "deploy {{service:api}}"
	if err := state.SaveSnippet(); err != nil {
		t.Fatalf("SaveSnippet: %v", err)
	}
	snippet := state.Snippets()[0]
	runner.err = errors.New("transport failed")
	if !state.BeginSnippetRun(snippet.ID, SnippetDestinationActive) {
		t.Fatal("BeginSnippetRun rejected the Snippet")
	}
	if state.ConfirmSnippetRun() {
		t.Fatal("ConfirmSnippetRun reported success when the transport failed")
	}
	if state.SnippetRunProblem() != ProblemSnippetRunFailed || !state.SnippetRunOpen() {
		t.Fatalf("problem = %q, form open = %v", state.SnippetRunProblem(), state.SnippetRunOpen())
	}

	state.CancelSnippetRun()
	if state.SnippetRunOpen() || state.SnippetRunProblem() != ProblemNone {
		t.Fatalf("CancelSnippetRun left state: open=%v problem=%q",
			state.SnippetRunOpen(), state.SnippetRunProblem())
	}
}

func TestBeginEditSnippetClosesOtherEditors(t *testing.T) {
	state, _, _ := newSnippetState(t, nil)
	state.BeginCreate()
	if !state.Editing() {
		t.Fatal("Host editor did not open")
	}
	state.BeginCreateSnippet()
	if state.Editing() || !state.EditingSnippet() {
		t.Fatalf("Snippet editor did not take over: host=%v snippet=%v",
			state.Editing(), state.EditingSnippet())
	}
	state.BeginCreate()
	if state.EditingSnippet() || !state.Editing() {
		t.Fatalf("Host editor did not take over: host=%v snippet=%v",
			state.Editing(), state.EditingSnippet())
	}
}
