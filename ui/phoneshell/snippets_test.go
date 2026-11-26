package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// snippetTestVault is an in-memory Vault service for the Snippets page. Its
// Snippet half applies the shared domain normalization and substitution, so a
// test exercises the same rules the application service uses; only persistence
// is replaced.
type snippetTestVault struct {
	mu       sync.Mutex
	snippets []domainvault.Snippet
	packages []domainvault.SnippetPackage
	hosts    []domainvault.Host
	seq      int
}

func (v *snippetTestVault) List(ctx context.Context) ([]domainvault.Host, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]domainvault.Host(nil), v.hosts...), nil
}

func (v *snippetTestVault) ListGroups(context.Context) ([]domainvault.Group, error) {
	return nil, nil
}

func (v *snippetTestVault) AssignHostsGroup(context.Context, []model.HostID, string) error {
	return nil
}

func (v *snippetTestVault) ListKeys(context.Context) ([]domainvault.Key, error) { return nil, nil }
func (v *snippetTestVault) CreateKey(context.Context, domainvault.KeyDraft) (domainvault.Key, error) {
	return domainvault.Key{}, nil
}
func (v *snippetTestVault) UpdateKey(context.Context, model.KeyID, domainvault.KeyDraft) (domainvault.Key, error) {
	return domainvault.Key{}, nil
}
func (v *snippetTestVault) DeleteKey(context.Context, model.KeyID) error { return nil }
func (v *snippetTestVault) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return nil, nil
}
func (v *snippetTestVault) CreateIdentity(context.Context, domainvault.IdentityDraft) (domainvault.Identity, error) {
	return domainvault.Identity{}, nil
}
func (v *snippetTestVault) UpdateIdentity(context.Context, model.IdentityID, domainvault.IdentityDraft) (domainvault.Identity, error) {
	return domainvault.Identity{}, nil
}
func (v *snippetTestVault) DeleteIdentity(context.Context, model.IdentityID) error { return nil }
func (v *snippetTestVault) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	return nil, nil
}
func (v *snippetTestVault) DeleteKnownHost(context.Context, string) error { return nil }

func (v *snippetTestVault) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]domainvault.Snippet(nil), v.snippets...), nil
}

func (v *snippetTestVault) CreateSnippet(_ context.Context, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.seq++
	snippet := domainvault.Snippet{
		ID:               fmt.Sprintf("snip-%d", v.seq),
		Label:            normalized.Label,
		Command:          normalized.Command,
		Package:          normalized.Package,
		Tags:             normalized.Tags,
		Targets:          normalized.Targets,
		TargetGroups:     normalized.TargetGroups,
		TargetsAllHosts:  normalized.TargetsAllHosts,
		NoAutoRun:        normalized.NoAutoRun,
		MultiLineRunMode: normalized.MultiLineRunMode,
		OrderKey:         int64(v.seq),
	}
	v.snippets = append(v.snippets, snippet)
	return snippet, nil
}

func (v *snippetTestVault) UpdateSnippet(_ context.Context, id string, draft domainvault.SnippetDraft) (domainvault.Snippet, error) {
	normalized, err := domainvault.NormalizeSnippetDraft(draft)
	if err != nil {
		return domainvault.Snippet{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for index := range v.snippets {
		if v.snippets[index].ID != id {
			continue
		}
		snippet := &v.snippets[index]
		snippet.Label = normalized.Label
		snippet.Command = normalized.Command
		snippet.Package = normalized.Package
		snippet.Tags = normalized.Tags
		snippet.Targets = normalized.Targets
		snippet.TargetGroups = normalized.TargetGroups
		snippet.TargetsAllHosts = normalized.TargetsAllHosts
		snippet.NoAutoRun = normalized.NoAutoRun
		snippet.MultiLineRunMode = normalized.MultiLineRunMode
		return *snippet, nil
	}
	return domainvault.Snippet{}, appvault.ErrNotFound
}

func (v *snippetTestVault) DeleteSnippet(_ context.Context, id string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for index := range v.snippets {
		if v.snippets[index].ID == id {
			v.snippets = append(v.snippets[:index], v.snippets[index+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (v *snippetTestVault) PrepareSnippet(_ context.Context, id string, values map[string]string) (appvault.SnippetRun, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, snippet := range v.snippets {
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
		run := appvault.SnippetRun{Snippet: snippet, Command: command, Values: values}
		run.Hosts = domainvault.SnippetRunnableHosts(snippet, v.hosts)
		return run, nil
	}
	return appvault.SnippetRun{}, appvault.ErrNotFound
}

func (v *snippetTestVault) SaveSnippetVariableValues(_ context.Context, id string, values map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	for index := range v.snippets {
		if v.snippets[index].ID != id {
			continue
		}
		if v.snippets[index].VariableValues == nil {
			v.snippets[index].VariableValues = make(map[string]string, len(values))
		}
		for name, value := range values {
			v.snippets[index].VariableValues[name] = value
		}
		return nil
	}
	return appvault.ErrNotFound
}

func (v *snippetTestVault) ListSnippetPackages(context.Context) ([]domainvault.SnippetPackage, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	saved := make([]string, 0, len(v.packages))
	for _, pkg := range v.packages {
		saved = append(saved, pkg.Path)
	}
	paths := domainvault.CollectSnippetPackagePaths(saved, v.snippets)
	out := make([]domainvault.SnippetPackage, 0, len(paths))
	for _, path := range paths {
		out = append(out, domainvault.SnippetPackage{Path: path})
	}
	return out, nil
}

func (v *snippetTestVault) CreateSnippetPackage(_ context.Context, draft domainvault.SnippetPackageDraft) (domainvault.SnippetPackage, error) {
	normalized, err := domainvault.NormalizeSnippetPackageDraft(draft)
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, pkg := range v.packages {
		if pkg.Path == normalized.Path {
			return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindConflict}
		}
	}
	pkg := domainvault.SnippetPackage{Path: normalized.Path}
	v.packages = append(v.packages, pkg)
	return pkg, nil
}

func (v *snippetTestVault) RenameSnippetPackage(_ context.Context, path, newPath string) (domainvault.SnippetPackage, error) {
	normalized, err := domainvault.NormalizeSnippetPackageDraft(domainvault.SnippetPackageDraft{Path: newPath})
	if err != nil {
		return domainvault.SnippetPackage{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for index := range v.packages {
		if v.packages[index].Path == path {
			v.packages[index].Path = normalized.Path
		}
	}
	for index := range v.snippets {
		if domainvault.SnippetPackageAtOrBelow(v.snippets[index].Package, path) {
			v.snippets[index].Package = domainvault.ReplaceSnippetPackagePrefix(
				v.snippets[index].Package, path, normalized.Path)
		}
	}
	return domainvault.SnippetPackage{Path: normalized.Path}, nil
}

func (v *snippetTestVault) DeleteSnippetPackage(_ context.Context, path string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	kept := v.packages[:0]
	for _, pkg := range v.packages {
		if !domainvault.SnippetPackageAtOrBelow(pkg.Path, path) {
			kept = append(kept, pkg)
		}
	}
	v.packages = kept
	for index := range v.snippets {
		if domainvault.SnippetPackageAtOrBelow(v.snippets[index].Package, path) {
			v.snippets[index].Package = ""
		}
	}
	return nil
}

// snippetTestHandle records the bytes a run delivered and can reject them.
type snippetTestHandle struct {
	mu     sync.Mutex
	data   [][]byte
	accept bool
	ended  bool
	done   chan struct{}
}

func newSnippetTestHandle() *snippetTestHandle {
	return &snippetTestHandle{accept: true, done: make(chan struct{})}
}

func (h *snippetTestHandle) Attach() (io.ReadWriteCloser, error) {
	return nil, errors.New("the test does not attach a screen")
}
func (h *snippetTestHandle) Hibernate() bool { return true }
func (h *snippetTestHandle) Resume() (io.ReadWriteCloser, error) {
	return nil, errors.New("the test does not resume a screen")
}
func (h *snippetTestHandle) Done() <-chan struct{} { return h.done }
func (h *snippetTestHandle) End() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.ended {
		h.ended = true
		close(h.done)
	}
	return nil
}

func (h *snippetTestHandle) InsertInput(data []byte) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.accept {
		return false
	}
	h.data = append(h.data, append([]byte(nil), data...))
	return true
}

func (h *snippetTestHandle) chunks() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.data))
	for _, chunk := range h.data {
		out = append(out, string(chunk))
	}
	return out
}

// snippetTestOpener answers Opens from a per-Host handle or error and counts
// every attempt, so a test can prove no target was silently dropped.
type snippetTestOpener struct {
	mu      sync.Mutex
	handles map[model.HostID]*snippetTestHandle
	errs    map[model.HostID]error
	calls   map[model.HostID]int
}

func newSnippetTestOpener() *snippetTestOpener {
	return &snippetTestOpener{
		handles: make(map[model.HostID]*snippetTestHandle),
		errs:    make(map[model.HostID]error),
		calls:   make(map[model.HostID]int),
	}
}

func (o *snippetTestOpener) OpenHost(_ context.Context, host domainvault.Host) (SessionHandle, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls[host.ID]++
	if err := o.errs[host.ID]; err != nil {
		return nil, err
	}
	handle := o.handles[host.ID]
	if handle == nil {
		handle = newSnippetTestHandle()
		o.handles[host.ID] = handle
	}
	return handle, nil
}

func (o *snippetTestOpener) callsFor(id model.HostID) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.calls[id]
}

func (o *snippetTestOpener) handle(id model.HostID) *snippetTestHandle {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.handles[id]
}

func snippetTestHosts() (domainvault.Host, domainvault.Host) {
	prod := domainvault.Host{
		ID: "host-prod", Label: "prod box", Hostname: "prod.example.com",
		Protocol: domainvault.ProtocolSSH, Group: "prod",
	}
	db := domainvault.Host{
		ID: "host-db", Label: "db box", Hostname: "db.example.com",
		Protocol: domainvault.ProtocolSSH, Group: "prod",
	}
	return prod, db
}

func newSnippetTestShell(t *testing.T, vault *snippetTestVault, opener *snippetTestOpener) *Shell {
	t.Helper()
	options := []Option{WithVault(vault), WithHosts(vault)}
	if opener != nil {
		options = append(options, WithSessionOpener(opener))
	}
	shell := New(options...)
	if err := shell.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return shell
}

func openSnippetPage(t *testing.T, shell *Shell) *ui.Tester {
	t.Helper()
	shell.Select(Vault)
	tt := ui.NewTester(shell.View(Vault), 390, 844)
	if err := tt.Click("Snippets"); err != nil {
		t.Fatalf("click Snippets: %v", err)
	}
	tt.Advance(600 * time.Millisecond)
	return tt
}

func waitSnippetRun(t *testing.T, p *snippetPages) []snippetTargetResult {
	t.Helper()
	p.mu.Lock()
	done := p.runDone
	p.mu.Unlock()
	if done == nil {
		t.Fatal("the run did not start")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not finish within its deadline")
	}
	_, results := p.runSnapshot()
	return results
}

// TestPhoneSnippetsCRUDVariablesAndTargets drives package and Snippet Add, Edit,
// and Delete through the page, fills and chooses a variable, and runs an
// unscoped Snippet on two selected Hosts with one target failing.
func TestPhoneSnippetsCRUDVariablesAndTargets(t *testing.T) {
	prod, db := snippetTestHosts()
	vault := &snippetTestVault{hosts: []domainvault.Host{prod, db}}
	opener := newSnippetTestOpener()
	opener.errs[db.ID] = errors.New("connection refused")
	shell := newSnippetTestShell(t, vault, opener)
	tt := openSnippetPage(t, shell)

	// Package create.
	if err := tt.Click("Add package"); err != nil {
		t.Fatalf("click Add package: %v", err)
	}
	if err := tt.Click("Package path"); err != nil {
		t.Fatalf("focus Package path: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Save package"); err != nil {
		t.Fatalf("save package: %v", err)
	}
	if !tt.HasText("ops") {
		t.Fatalf("the package is not listed; texts: %v", tt.Texts())
	}

	// Snippet create with a variable and a package.
	if err := tt.Click("Add snippet"); err != nil {
		t.Fatalf("click Add snippet: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label: %v", err)
	}
	tt.Type("Disk check")
	if err := tt.Click("Command"); err != nil {
		t.Fatalf("focus Command: %v", err)
	}
	tt.Type("df -h {{path:/}}")
	if err := tt.Click("Package"); err != nil {
		t.Fatalf("focus Package: %v", err)
	}
	tt.Type("ops")
	if err := tt.Click("Save snippet"); err != nil {
		t.Fatalf("save snippet: %v", err)
	}
	if !tt.HasText("Disk check") {
		t.Fatalf("the Snippet is not listed; texts: %v", tt.Texts())
	}
	snippets, _ := vault.ListSnippets(context.Background())
	if len(snippets) != 1 || snippets[0].Package != "ops" {
		t.Fatalf("stored Snippets = %+v, want one in package ops", snippets)
	}

	// Edit the label.
	if err := tt.Click("Edit snippet Disk check"); err != nil {
		t.Fatalf("click Edit snippet: %v", err)
	}
	if err := tt.Click("Label"); err != nil {
		t.Fatalf("focus Label in edit: %v", err)
	}
	tt.Type(" v2")
	if err := tt.Click("Save snippet"); err != nil {
		t.Fatalf("save edited Snippet: %v", err)
	}
	snippets, _ = vault.ListSnippets(context.Background())
	if len(snippets) != 1 || snippets[0].Label != "Disk check v2" {
		t.Fatalf("edited Snippets = %+v, want the new label", snippets)
	}

	// The run form fills the variable's default and offers both Hosts.
	if err := tt.Click("Run snippet Disk check v2"); err != nil {
		t.Fatalf("click Run snippet: %v", err)
	}
	page := shell.vault.snippets
	if page.run == nil {
		t.Fatal("the run form did not open")
	}
	if len(page.run.variables) != 1 || page.run.variables[0].value != "/" {
		t.Fatalf("run variables = %+v, want path prefilled with its default /", page.run.variables)
	}
	if len(page.run.candidates) != 2 {
		t.Fatalf("run candidates = %+v, want both Hosts", page.run.candidates)
	}
	// Choose the saved value when one exists and the field was cleared.
	page.run.variables[0].remembered = "srv1"
	page.run.variables[0].value = ""
	tt.Frame()
	if err := tt.Click("Use saved value for path"); err != nil {
		t.Fatalf("click Use saved value: %v", err)
	}
	if page.run.variables[0].value != "srv1" {
		t.Fatalf("variable value = %q, want the chosen saved value srv1", page.run.variables[0].value)
	}
	// Selecting Hosts as the destination runs on every selected candidate.
	if err := tt.Click("Target Hosts"); err != nil {
		t.Fatalf("choose Target Hosts: %v", err)
	}
	if err := tt.Click("Run snippet"); err != nil {
		t.Fatalf("confirm the run: %v", err)
	}
	results := waitSnippetRun(t, page)
	if len(results) != 2 {
		t.Fatalf("run results = %+v, want one outcome per target", results)
	}
	if opener.callsFor(prod.ID) != 1 || opener.callsFor(db.ID) != 1 {
		t.Fatalf("opener calls = %v/%v, want both targets attempted",
			opener.callsFor(prod.ID), opener.callsFor(db.ID))
	}
	var prodResult, dbResult *snippetTargetResult
	for index := range results {
		switch results[index].target {
		case "prod box":
			prodResult = &results[index]
		case "db box":
			dbResult = &results[index]
		}
	}
	if prodResult == nil || prodResult.err != "" {
		t.Fatalf("prod outcome = %+v, want a successful send", prodResult)
	}
	if dbResult == nil || !strings.Contains(dbResult.err, "Could not connect to db box") {
		t.Fatalf("db outcome = %+v, want an actionable connection failure", dbResult)
	}
	if got := opener.handle(prod.ID).chunks(); !slices.Equal(got, []string{"df -h srv1\r"}) {
		t.Fatalf("prod received %q, want the substituted command once", got)
	}
	if opener.handle(db.ID) != nil {
		t.Fatal("a target whose connection failed received bytes")
	}
	tt.Frame()
	if !tt.HasText("Sent to prod box.") || !tt.HasText("Could not connect to db box") {
		t.Fatalf("per-target outcomes are not shown; texts: %v", tt.Texts())
	}

	// A value entered for a variable is remembered by the shared service and
	// never rendered on the list page.
	snippets, _ = vault.ListSnippets(context.Background())
	if len(snippets) != 1 || snippets[0].VariableValues["path"] != "srv1" {
		t.Fatalf("remembered values = %+v, want srv1", snippets)
	}
	for _, text := range tt.Texts() {
		if strings.Contains(text, "srv1") {
			t.Fatalf("the list rendered a remembered variable value: %q", text)
		}
	}

	// Delete the Snippet, then the package.
	if err := tt.Click("Delete snippet Disk check v2"); err != nil {
		t.Fatalf("click Delete snippet: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm Snippet delete: %v", err)
	}
	snippets, _ = vault.ListSnippets(context.Background())
	if len(snippets) != 0 {
		t.Fatalf("the deleted Snippet is still stored: %+v", snippets)
	}
	if err := tt.Click("Delete package ops"); err != nil {
		t.Fatalf("click Delete package: %v", err)
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatalf("confirm package delete: %v", err)
	}
	packages, _ := vault.ListSnippetPackages(context.Background())
	if len(packages) != 0 {
		t.Fatalf("the deleted package is still stored: %+v", packages)
	}
}

// TestPhoneSnippetMissingVariableKeepsTheForm proves a required variable that
// cannot be filled never reaches a Session and keeps the form open with the
// names the service reported.
func TestPhoneSnippetMissingVariableKeepsTheForm(t *testing.T) {
	prod, db := snippetTestHosts()
	vault := &snippetTestVault{hosts: []domainvault.Host{prod, db}}
	opener := newSnippetTestOpener()
	shell := newSnippetTestShell(t, vault, opener)
	if _, err := vault.CreateSnippet(context.Background(), domainvault.SnippetDraft{
		Label: "Needs a host", Command: "ssh {{host}}",
	}); err != nil {
		t.Fatalf("create Snippet: %v", err)
	}
	if err := shell.vault.snippets.reload(context.Background()); err != nil {
		t.Fatalf("reload snippets: %v", err)
	}
	tt := openSnippetPage(t, shell)
	tt.Frame()

	if err := tt.Click("Run snippet Needs a host"); err != nil {
		t.Fatalf("click Run snippet: %v", err)
	}
	if err := tt.Click("Run snippet"); err != nil {
		t.Fatalf("confirm the run: %v", err)
	}
	page := shell.vault.snippets
	if page.run == nil {
		t.Fatal("a missing required variable closed the run form")
	}
	if !slices.Equal(page.runMissing, []string{"host"}) {
		t.Fatalf("runMissing = %v, want [host]", page.runMissing)
	}
	if !tt.HasText("Fill in required variables: host.") {
		t.Fatalf("the missing variable is not named; texts: %v", tt.Texts())
	}
	if opener.callsFor(prod.ID) != 0 || opener.callsFor(db.ID) != 0 {
		t.Fatal("a half-filled Snippet reached a target")
	}
}

// TestPhoneSnippetRunsInCurrentSession proves a Snippet can run in the Session
// the Sessions section last showed even while the Vault section is on screen,
// and that the shared target rule still refuses an unrelated Host.
func TestPhoneSnippetRunsInCurrentSession(t *testing.T) {
	prod, db := snippetTestHosts()
	vault := &snippetTestVault{hosts: []domainvault.Host{prod, db}}
	opener := newSnippetTestOpener()
	shell := newSnippetTestShell(t, vault, opener)

	handle := newSnippetTestHandle()
	opener.handles[prod.ID] = handle
	tab, pane := shell.sessions.begin(prod)
	shell.sessions.finish(tab.id, string(pane.id), handle, nil)
	shell.Router(Sessions).Push("/session/" + tab.id)

	if _, err := vault.CreateSnippet(context.Background(), domainvault.SnippetDraft{
		Label: "Uptime", Command: "uptime",
	}); err != nil {
		t.Fatalf("create Snippet: %v", err)
	}
	if err := shell.vault.snippets.reload(context.Background()); err != nil {
		t.Fatalf("reload snippets: %v", err)
	}
	tt := openSnippetPage(t, shell)
	tt.Frame()

	if err := tt.Click("Run snippet Uptime"); err != nil {
		t.Fatalf("click Run snippet: %v", err)
	}
	page := shell.vault.snippets
	if page.run.destinationIndex != snippetRunHere {
		t.Fatalf("destination = %d, want the current Session", page.run.destinationIndex)
	}
	if err := tt.Click("Run snippet"); err != nil {
		t.Fatalf("confirm the run: %v", err)
	}
	results := page.runResultsCopy()
	if len(results) != 1 || results[0].err != "" {
		shell.sessions.mu.Lock()
		liveTab := shell.sessions.find(tab.id)
		livePane := liveTab.focusedPane()
		status, hasHandle := livePane.status, livePane.handle != nil
		shell.sessions.mu.Unlock()
		t.Fatalf("current-Session results = %+v (run open=%v, problem=%q, status=%d, handle=%v), want one success",
			results, page.run != nil, page.runProblem, status, hasHandle)
	}
	if !strings.Contains(results[0].target, "Current Session (prod box)") {
		t.Fatalf("result target = %q, want the current Session label", results[0].target)
	}
	if got := handle.chunks(); !slices.Equal(got, []string{"uptime\r"}) {
		t.Fatalf("the current Session received %q, want uptime once", got)
	}

	// A Snippet scoped to another Host must not run here.
	scoped, err := vault.CreateSnippet(context.Background(), domainvault.SnippetDraft{
		Label: "DB only", Command: "echo db", Targets: []model.HostID{db.ID},
	})
	if err != nil {
		t.Fatalf("create scoped Snippet: %v", err)
	}
	if err := shell.vault.snippets.reload(context.Background()); err != nil {
		t.Fatalf("reload snippets: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Run snippet DB only"); err != nil {
		t.Fatalf("click scoped Run: %v", err)
	}
	if err := tt.Click("Current Session"); err != nil {
		t.Fatalf("choose Current Session: %v", err)
	}
	if err := tt.Click("Run snippet"); err != nil {
		t.Fatalf("confirm scoped run: %v", err)
	}
	_ = scoped
	page = shell.vault.snippets
	if page.run == nil || !strings.Contains(page.runProblem, "not targeted at prod box") {
		t.Fatalf("runProblem = %q, want an actionable target refusal", page.runProblem)
	}
	if got := handle.chunks(); !slices.Equal(got, []string{"uptime\r"}) {
		t.Fatalf("the refused Snippet delivered %q", got)
	}
}

func (p *snippetPages) runResultsCopy() []snippetTargetResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]snippetTargetResult(nil), p.runResults...)
}
