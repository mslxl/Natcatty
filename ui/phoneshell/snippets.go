package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// The Snippets page is the phone's editor and runner for the shared Vault's
// Snippets and Snippet Packages. It never keeps a second copy of a Snippet:
// every read, write, validation, variable substitution, and target decision
// goes through the application Vault service the desktop uses, so a change on
// either device reaches the other on the next reload.

// snippetUnavailable is the page's state when the Vault service was built
// without Snippet persistence. No production root leaves it unset.
const snippetUnavailable = "Snippets are not available on this device."

// snippetRunHere and snippetRunHosts are the run form's destination order.
const (
	snippetRunHere = iota
	snippetRunHosts
)

// snippetModePasteIndex and snippetModeLineDelayIndex are the editor's
// multi-line mode choice.
const (
	snippetModePasteIndex = iota
	snippetModeLineDelayIndex
)

// snippetRunTimeout bounds one target's connection during a multi-target run.
// A target that does not connect in time is reported as failed; every other
// target is still attempted. It is a variable so a test can shorten it.
var snippetRunTimeout = 30 * time.Second

// sessionInputter is implemented by a Session handle that accepts bytes
// without a terminal view. The application's phone Session handle implements
// it, so a Snippet can run in a Session the Vault page does not render.
type sessionInputter interface {
	InsertInput(data []byte) bool
}

// snippetDraftForm is the plaintext editor of one Snippet.
type snippetDraftForm struct {
	id      string
	editing bool

	label        string
	command      string
	packagePath  string
	tags         string
	noAutoRun    bool
	modeIndex    int
	allHosts     bool
	targetHosts  map[string]bool
	targetGroups map[string]bool
}

// snippetPackageDraftForm is the editor of one Snippet package path.
type snippetPackageDraftForm struct {
	originalPath string
	path         string
}

// snippetRunVariable is one {{variable}} the run form asks for, in first
// occurrence order.
type snippetRunVariable struct {
	name         string
	value        string
	defaultValue string
	hasDefault   bool
	remembered   string
}

// snippetRunForm holds the values and selected targets for a pending run.
type snippetRunForm struct {
	id               string
	destinationIndex int
	variables        []snippetRunVariable
	candidates       []domainvault.Host
	selected         map[string]bool
}

// snippetTargetResult is one target's outcome. An empty err means the command
// reached it; every target the run attempted appears exactly once.
type snippetTargetResult struct {
	target string
	err    string
}

// snippetPages owns the Snippets section's list, editors, and run form. Its
// state belongs to the Vault section, so navigating away and back preserves
// an open editor without keeping a copy of any record.
type snippetPages struct {
	shell *Shell
	store vaultstate.Snippets

	status   vaultStatus
	problem  string
	snippets []domainvault.Snippet
	packages []domainvault.SnippetPackage

	draft          *snippetDraftForm
	draftProblem   string
	packageDraft   *snippetPackageDraftForm
	packageProblem string

	pendingSnippet string
	pendingPackage string

	run        *snippetRunForm
	runProblem string
	runMissing []string

	// mu guards the run fields a background multi-target run writes after its
	// network calls return.
	mu         sync.Mutex
	runBusy    bool
	runResults []snippetTargetResult
	runDone    chan struct{}
	// runReload asks the next frame to re-read the Snippet list on the UI
	// thread, so the values a finished run remembered are shown as saved.
	runReload bool
}

func newSnippetPages(shell *Shell) *snippetPages {
	return &snippetPages{shell: shell, status: vaultLoading}
}

// loadSnippets binds the shared Vault service and replaces both lists. A
// service built without Snippet persistence is this page's own unavailable
// state, never the Shell's Load failure.
func (v *vaultPages) loadSnippets(ctx context.Context) {
	p := v.snippets
	if p == nil {
		return
	}
	p.pendingSnippet = ""
	p.pendingPackage = ""
	if store, ok := v.store.(vaultstate.Snippets); ok {
		p.store = store
	}
	if p.store == nil {
		p.status, p.problem = vaultFailed, snippetUnavailable
		return
	}
	_ = p.reload(ctx)
}

// reload re-reads only the Snippet lists, so a save does not disturb the other
// Vault pages' state.
func (p *snippetPages) reload(ctx context.Context) error {
	if p.store == nil {
		p.status, p.problem = vaultFailed, snippetUnavailable
		return errors.New("snippets are unavailable")
	}
	p.status = vaultLoading
	snippets, err := p.store.ListSnippets(ctx)
	if err != nil {
		p.status, p.problem = vaultFailed, "Could not load Snippets."
		return err
	}
	packages, err := p.store.ListSnippetPackages(ctx)
	if err != nil {
		p.status, p.problem = vaultFailed, "Could not load Snippet packages."
		return err
	}
	p.snippets, p.packages, p.status, p.problem = snippets, packages, vaultReady, ""
	return nil
}

// snippetsSummary is the Vault landing row's second line.
func (v *vaultPages) snippetsSummary() string {
	if v.snippets == nil {
		return "Unavailable"
	}
	switch v.snippets.status {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(v.snippets.snippets) {
	case 0:
		return "No Snippets yet"
	case 1:
		return "1 Snippet"
	default:
		return fmt.Sprintf("%d Snippets", len(v.snippets.snippets))
	}
}

// drawSnippets renders the list, an editor, or the run form. Opening one
// replaces the list so a half-filled form cannot be mistaken for a saved
// Snippet.
func (v *vaultPages) drawSnippets(c *ui.Context) {
	p := v.snippets
	if p == nil || p.store == nil {
		width := pageContentWidth(c)
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, snippetUnavailable, nil)
			})
		})
		return
	}
	switch {
	case p.draft != nil:
		p.drawSnippetDraft(c)
	case p.packageDraft != nil:
		p.drawSnippetPackageDraft(c)
	case p.run != nil:
		p.drawSnippetRun(c)
	default:
		v.drawSnippetList(c)
	}
}

func (v *vaultPages) drawSnippetList(c *ui.Context) {
	p := v.snippets
	if p.consumeRunReload() {
		_ = p.reload(context.Background())
	}
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			v.refreshBar(c, "Snippets")
			if kit.GhostButton(c, icons.Plus, "Add snippet").Clicked() {
				p.beginCreateSnippet()
			}
			if kit.GhostButton(c, icons.Plus, "Add package").Clicked() {
				p.beginCreatePackage()
			}
			if busy, results := p.runSnapshot(); busy {
				kit.EmptyHint(c, "Running the Snippet on the selected Hosts…")
			} else if len(results) > 0 {
				drawSnippetRunResults(c, results)
			}
			if p.runProblem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.runProblem, nil)
			}
			switch p.status {
			case vaultLoading:
				kit.EmptyHint(c, "Loading Snippets…")
				return
			case vaultFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = p.reload(context.Background())
					}
				})
				if len(p.snippets) == 0 && len(p.packages) == 0 {
					return
				}
			default:
				if p.problem != "" {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.problem, nil)
				}
			}
			if len(p.packages) > 0 {
				ui.Text(c, "Packages").FontSize(13).FontWeight(600)
				for _, pkg := range p.packages {
					pkg := pkg
					p.drawPackageRow(c, pkg)
				}
			}
			ui.Text(c, "Snippets").FontSize(13).FontWeight(600)
			if len(p.snippets) == 0 {
				kit.EmptyHint(c, "No Snippets yet. Snippets added on the desktop appear here.")
			}
			for _, snippet := range p.snippets {
				snippet := snippet
				p.drawSnippetRow(c, snippet)
			}
			if p.pendingSnippet != "" {
				p.drawSnippetDelete(c)
			}
			if p.pendingPackage != "" {
				p.drawPackageDelete(c)
			}
		})
	})
}

func (p *snippetPages) drawPackageRow(c *ui.Context, pkg domainvault.SnippetPackage) {
	name := pkg.Path
	row := kit.ListRow(c).Label("Edit package " + name)
	handled := false
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).FontWeight(600).SingleLine()
			ui.Text(c, p.packageSummary(pkg.Path)).FontSize(12).
				TextColor(c.Theme().TextMuted).SingleLine()
		})
		if kit.IconButton(c, icons.Trash, "Delete package "+name).Clicked() {
			p.pendingPackage = pkg.Path
			p.pendingSnippet = ""
			handled = true
		}
		ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
	})
	if !handled && row.Clicked() {
		p.beginEditPackage(pkg.Path)
	}
}

func (p *snippetPages) drawSnippetRow(c *ui.Context, snippet domainvault.Snippet) {
	name := snippetName(snippet)
	row := kit.ListRow(c).Label("Edit snippet " + name)
	handled := false
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).FontWeight(600).SingleLine()
			ui.Text(c, snippetSummaryText(snippet)).FontSize(12).
				TextColor(c.Theme().TextMuted).SingleLine()
			ui.Text(c, snippetScopeText(snippet)).FontSize(12).
				TextColor(c.Theme().TextMuted).SingleLine()
		})
		if kit.IconButton(c, icons.Play, "Run snippet "+name).Clicked() {
			p.beginRun(snippet)
			handled = true
		}
		if kit.IconButton(c, icons.Trash, "Delete snippet "+name).Clicked() {
			p.pendingSnippet = snippet.ID
			p.pendingPackage = ""
			handled = true
		}
		ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
	})
	if !handled && row.Clicked() {
		p.beginEditSnippet(snippet)
	}
}

func (p *snippetPages) drawSnippetDelete(c *ui.Context) {
	name := p.pendingSnippetName()
	cancelled, confirmed := kit.ConfirmBar(c,
		"Delete the Snippet "+name+"?",
		func() {
			kit.EmptyHint(c, "The Snippet is removed from the shared Vault on every device.")
		},
		"Cancel", "Delete")
	switch {
	case cancelled:
		p.pendingSnippet = ""
	case confirmed:
		p.deleteSnippet(context.Background(), p.pendingSnippet)
	}
}

func (p *snippetPages) drawPackageDelete(c *ui.Context) {
	name := p.pendingPackage
	cancelled, confirmed := kit.ConfirmBar(c,
		"Delete the package "+name+"?",
		func() {
			kit.EmptyHint(c, "Snippets in the package are kept and move to the Vault root.")
		},
		"Cancel", "Delete")
	switch {
	case cancelled:
		p.pendingPackage = ""
	case confirmed:
		p.deletePackage(context.Background(), p.pendingPackage)
	}
}

// beginCreateSnippet opens the editor with an empty draft.
func (p *snippetPages) beginCreateSnippet() {
	p.draft = &snippetDraftForm{
		modeIndex:    snippetModePasteIndex,
		targetHosts:  make(map[string]bool),
		targetGroups: make(map[string]bool),
	}
	p.draftProblem = ""
	p.pendingSnippet = ""
	p.pendingPackage = ""
}

// beginEditSnippet opens the editor with a listed Snippet's editable fields and
// its saved targets, so an edit never silently clears a desktop decision.
func (p *snippetPages) beginEditSnippet(snippet domainvault.Snippet) {
	form := &snippetDraftForm{
		id:           snippet.ID,
		editing:      true,
		label:        snippet.Label,
		command:      snippet.Command,
		packagePath:  snippet.Package,
		tags:         strings.Join(snippet.Tags, ", "),
		noAutoRun:    snippet.NoAutoRun,
		modeIndex:    snippetModeIndex(snippet.MultiLineRunMode),
		allHosts:     snippet.TargetsAllHosts,
		targetHosts:  make(map[string]bool, len(snippet.Targets)),
		targetGroups: make(map[string]bool, len(snippet.TargetGroups)),
	}
	for _, target := range snippet.Targets {
		form.targetHosts[string(target)] = true
	}
	for _, group := range snippet.TargetGroups {
		form.targetGroups[group] = true
	}
	p.draft = form
	p.draftProblem = ""
	p.pendingSnippet = ""
	p.pendingPackage = ""
}

func (p *snippetPages) cancelSnippetDraft() {
	p.draft = nil
	p.draftProblem = ""
}

func (p *snippetPages) drawSnippetDraft(c *ui.Context) {
	f := p.draft
	title := "New Snippet"
	if f.editing {
		title = "Edit Snippet"
	}
	ui.Column(c).Fill().Gap(10).Children(func() {
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(12).Children(func() {
				kit.SectionBar(c, title, nil)
				if p.draftProblem != "" {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.draftProblem, nil)
				}
				kit.FormRow(c, "Label", func() {
					ui.TextInput(c, &f.label).Placeholder("A name you recognize").FillWidth()
				})
				kit.FormRow(c, "Command", func() {
					ui.TextArea(c, &f.command).Height(120).
						Placeholder("docker ps --filter name={{name}}")
				}).Description("Use {{name}} for a variable and {{name:default}} for an optional one with a default.")
				kit.FormRow(c, "Package", func() {
					ui.TextInput(c, &f.packagePath).Placeholder("ops/prod").FillWidth()
				}).Description("A slash-separated package path. Leave blank for the Vault root.")
				kit.FormRow(c, "Tags", func() {
					ui.TextInput(c, &f.tags).Placeholder("ops, prod").FillWidth()
				}).Description("Comma-separated tags.")
				kit.FormRow(c, "Multi-line", func() {
					ui.Segmented(c, &f.modeIndex, "Paste", "Line delay")
				}).Description("Paste sends the whole command at once; line delay submits each line.")
				ui.Checkbox(c, &f.noAutoRun, "Do not press Enter after running")
				ui.Text(c, "Targets").FontSize(14).FontWeight(600)
				ui.Checkbox(c, &f.allHosts, "Run on all Hosts")
				if !f.allHosts {
					p.drawSnippetTargetPickers(c)
				}
			})
		})
		ui.Column(c).FillWidth().Padding(12, 16).Gap(8).Children(func() {
			if kit.PrimaryButton(c, "", "Save snippet").Clicked() {
				p.submitSnippet()
			}
			if kit.SecondaryButton(c, "", "Cancel").Clicked() {
				p.cancelSnippetDraft()
			}
		})
	})
}

func (p *snippetPages) drawSnippetTargetPickers(c *ui.Context) {
	f := p.draft
	hosts := p.shellHosts()
	if len(hosts) == 0 {
		kit.EmptyHint(c, "No remote Hosts yet. Add Hosts before targeting one.")
	} else {
		for _, host := range hosts {
			key := string(host.ID)
			checked := f.targetHosts[key]
			ui.Checkbox(c, &checked, "Target "+hostName(host))
			f.targetHosts[key] = checked
		}
	}
	for _, path := range p.targetGroupPaths() {
		path := path
		checked := f.targetGroups[path]
		ui.Checkbox(c, &checked, "Target group "+path)
		f.targetGroups[path] = checked
	}
}

// submitSnippet writes the draft through the shared Vault service. Validation
// is the domain's, so the phone accepts exactly what the desktop accepts.
func (p *snippetPages) submitSnippet() {
	f := p.draft
	if f == nil {
		return
	}
	if p.store == nil {
		p.draftProblem = snippetUnavailable
		return
	}
	draft := p.snippetDraft()
	var err error
	if f.id == "" {
		_, err = p.store.CreateSnippet(context.Background(), draft)
	} else {
		_, err = p.store.UpdateSnippet(context.Background(), f.id, draft)
	}
	if err != nil {
		p.draftProblem = snippetSaveProblemText(err)
		return
	}
	p.cancelSnippetDraft()
	_ = p.reload(context.Background())
}

func (p *snippetPages) snippetDraft() domainvault.SnippetDraft {
	f := p.draft
	draft := domainvault.SnippetDraft{
		Label:            f.label,
		Command:          f.command,
		Package:          f.packagePath,
		Tags:             splitSnippetList(f.tags),
		TargetsAllHosts:  f.allHosts,
		NoAutoRun:        f.noAutoRun,
		MultiLineRunMode: snippetModeAt(f.modeIndex),
	}
	if f.allHosts {
		return draft
	}
	for _, host := range p.shellHosts() {
		if f.targetHosts[string(host.ID)] {
			draft.Targets = append(draft.Targets, host.ID)
		}
	}
	for _, path := range p.targetGroupPaths() {
		if f.targetGroups[path] {
			draft.TargetGroups = append(draft.TargetGroups, path)
		}
	}
	return draft
}

// beginCreatePackage opens the package path editor for a new package.
func (p *snippetPages) beginCreatePackage() {
	p.packageDraft = &snippetPackageDraftForm{}
	p.packageProblem = ""
	p.pendingSnippet = ""
	p.pendingPackage = ""
}

// beginEditPackage opens the package path editor as a rename.
func (p *snippetPages) beginEditPackage(path string) {
	p.packageDraft = &snippetPackageDraftForm{originalPath: path, path: path}
	p.packageProblem = ""
	p.pendingSnippet = ""
	p.pendingPackage = ""
}

func (p *snippetPages) cancelPackageDraft() {
	p.packageDraft = nil
	p.packageProblem = ""
}

func (p *snippetPages) drawSnippetPackageDraft(c *ui.Context) {
	f := p.packageDraft
	title := "New Snippet Package"
	if f.originalPath != "" {
		title = "Rename Snippet Package"
	}
	kit.SectionBar(c, title, nil)
	if p.packageProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.packageProblem, nil)
	}
	kit.FormRow(c, "Package path", func() {
		ui.TextInput(c, &f.path).Placeholder("ops/prod").FillWidth()
	}).Description("Letters, digits, underscores, and dashes, separated by slashes.")
	if kit.PrimaryButton(c, "", "Save package").Clicked() {
		p.submitPackage()
	}
	if kit.SecondaryButton(c, "", "Cancel").Clicked() {
		p.cancelPackageDraft()
	}
}

func (p *snippetPages) submitPackage() {
	f := p.packageDraft
	if f == nil {
		return
	}
	if p.store == nil {
		p.packageProblem = snippetUnavailable
		return
	}
	var err error
	if f.originalPath == "" {
		_, err = p.store.CreateSnippetPackage(context.Background(),
			domainvault.SnippetPackageDraft{Path: f.path})
	} else {
		_, err = p.store.RenameSnippetPackage(context.Background(), f.originalPath, f.path)
	}
	if err != nil {
		p.packageProblem = snippetPackageProblemText(err)
		return
	}
	p.cancelPackageDraft()
	_ = p.reload(context.Background())
}

func (p *snippetPages) deleteSnippet(ctx context.Context, id string) {
	p.pendingSnippet = ""
	if p.store == nil {
		p.problem = snippetUnavailable
		return
	}
	if err := p.store.DeleteSnippet(ctx, id); err != nil {
		p.problem = "Could not delete the Snippet."
		return
	}
	_ = p.reload(ctx)
}

func (p *snippetPages) deletePackage(ctx context.Context, path string) {
	p.pendingPackage = ""
	if p.store == nil {
		p.problem = snippetUnavailable
		return
	}
	if err := p.store.DeleteSnippetPackage(ctx, path); err != nil {
		p.problem = "Could not delete the Snippet package."
		return
	}
	_ = p.reload(ctx)
}

// beginRun opens the run form. A Snippet without variables still opens the form
// so the user chooses the destination and, for Hosts, exactly which Hosts the
// command reaches.
func (p *snippetPages) beginRun(snippet domainvault.Snippet) {
	defs := domainvault.ParseSnippetVariables(snippet.Command)
	variables := make([]snippetRunVariable, 0, len(defs))
	for _, def := range defs {
		variable := snippetRunVariable{
			name:         def.Name,
			defaultValue: def.DefaultValue,
			hasDefault:   def.HasDefault,
		}
		if remembered, ok := snippet.VariableValues[def.Name]; ok {
			variable.remembered = remembered
			variable.value = remembered
		} else if def.HasDefault {
			variable.value = def.DefaultValue
		}
		variables = append(variables, variable)
	}
	candidates := p.candidateHosts(snippet)
	selected := make(map[string]bool, len(candidates))
	for _, host := range candidates {
		selected[string(host.ID)] = true
	}
	destination := snippetRunHere
	if _, host, ok := p.currentSession(); !ok {
		if len(candidates) > 0 {
			destination = snippetRunHosts
		}
	} else if !snippetAllowsHost(snippet, host) && len(candidates) > 0 {
		// The Session on screen cannot receive this scoped Snippet; its own
		// target Hosts are the useful default instead of a refusal.
		destination = snippetRunHosts
	}
	p.run = &snippetRunForm{
		id:               snippet.ID,
		destinationIndex: destination,
		variables:        variables,
		candidates:       candidates,
		selected:         selected,
	}
	p.mu.Lock()
	p.runResults = nil
	p.mu.Unlock()
	p.runProblem = ""
	p.runMissing = nil
	p.draft = nil
	p.packageDraft = nil
	p.pendingSnippet = ""
	p.pendingPackage = ""
}

// currentSession reports the Session the Sessions section last showed. It is
// the Pane a "Current Session" run delivers to, even while the Vault section is
// on screen and that terminal is hibernated.
func (p *snippetPages) currentSession() (label string, host domainvault.Host, ok bool) {
	if p.shell == nil {
		return "", domainvault.Host{}, false
	}
	path := strings.TrimSuffix(p.shell.sections[Sessions].router.Path(), "/")
	id, found := strings.CutPrefix(path, "/session/")
	if !found || id == "" || strings.Contains(id, "/") {
		return "", domainvault.Host{}, false
	}
	p.shell.sessions.mu.Lock()
	defer p.shell.sessions.mu.Unlock()
	tab := p.shell.sessions.find(id)
	if tab == nil {
		return "", domainvault.Host{}, false
	}
	pane := tab.focusedPane()
	if pane == nil {
		return "", domainvault.Host{}, false
	}
	return pane.label, pane.host, true
}

// currentSessionHandle returns a sendable handle for the current Session. A
// Session that is still connecting, failed, or dormant is not runnable yet; the
// page explains that instead of silently dropping the run.
func (p *snippetPages) currentSessionHandle() (label string, host domainvault.Host, handle SessionHandle, problem string) {
	if p.shell == nil {
		return "", domainvault.Host{}, nil, "No current Session is open."
	}
	path := strings.TrimSuffix(p.shell.sections[Sessions].router.Path(), "/")
	id, found := strings.CutPrefix(path, "/session/")
	if !found || id == "" || strings.Contains(id, "/") {
		return "", domainvault.Host{}, nil, "No current Session is open. Open a Session, or run on target Hosts."
	}
	p.shell.sessions.mu.Lock()
	tab := p.shell.sessions.find(id)
	var pane *phonePane
	if tab != nil {
		pane = tab.focusedPane()
	}
	if pane == nil {
		p.shell.sessions.mu.Unlock()
		return "", domainvault.Host{}, nil, "The current Tab has no Session. Open a Host first."
	}
	label, host, status, candidate := pane.label, pane.host, pane.status, pane.handle
	p.shell.sessions.mu.Unlock()
	switch status {
	case tabReady, tabHibernated:
	default:
		return label, host, nil, "The current Session is not connected yet. Open it, then run the Snippet again."
	}
	if candidate == nil || sessionEnded(candidate) {
		return label, host, nil, "The current Session has ended. Open the Host again, or run on target Hosts."
	}
	if _, ok := candidate.(sessionInputter); !ok {
		return label, host, nil, "This build cannot send a command to an open Session."
	}
	return label, host, candidate, ""
}

// candidateHosts is the Hosts a run may target: the Snippet's saved targets for
// a scoped Snippet, or every remote Host for an unscoped one. The shared domain
// rule decides, so the phone matches the desktop.
func (p *snippetPages) candidateHosts(snippet domainvault.Snippet) []domainvault.Host {
	hosts := p.shellHosts()
	if domainvault.SnippetHasRunTargets(snippet) {
		return domainvault.SnippetRunnableHosts(snippet, hosts)
	}
	return hosts
}

func (p *snippetPages) shellHosts() []domainvault.Host {
	if p.shell == nil || p.shell.hosts == nil {
		return nil
	}
	return p.shell.hosts.hosts
}

// targetGroupPaths is every Host Group path the editor can target.
func (p *snippetPages) targetGroupPaths() []string {
	seen := make(map[string]bool)
	paths := make([]string, 0)
	add := func(path string) {
		path = domainvault.NormalizeGroup(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if p.shell != nil && p.shell.hosts != nil {
		for _, group := range p.shell.hosts.groups {
			add(group.Path)
		}
		for _, host := range p.shell.hosts.hosts {
			add(host.Group)
		}
	}
	return paths
}

// drawSnippetRun renders the destination choice, the variables, and the target
// Hosts. Nothing reaches a Session until Run is confirmed.
func (p *snippetPages) drawSnippetRun(c *ui.Context) {
	f := p.run
	snippet, ok := p.snippetByID(f.id)
	if !ok {
		p.run = nil
		p.runProblem = "The Snippet no longer exists."
		return
	}
	ui.Column(c).Fill().Gap(10).Children(func() {
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(12).Children(func() {
				kit.SectionBar(c, "Run "+snippetName(snippet), nil)
				if p.runProblem != "" {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, p.runProblem, nil)
				}
				if len(p.runMissing) > 0 {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
						"Fill in required variables: "+strings.Join(p.runMissing, ", ")+".", nil)
				}
				kit.FormRow(c, "Destination", func() {
					ui.Segmented(c, &f.destinationIndex, "Current Session", "Target Hosts")
				})
				preview := domainvault.PreviewSnippetCommand(snippet.Command, f.valueMap())
				if strings.TrimSpace(preview) != "" {
					ui.Text(c, "Command preview").FontSize(12).TextColor(c.Theme().TextMuted)
					ui.Text(c, preview).FontSize(12)
				}
				if f.destinationIndex == snippetRunHere {
					label, _, ok := p.currentSession()
					if ok {
						ui.Text(c, "Current Session: "+label).FontSize(12).
							TextColor(c.Theme().TextMuted)
					} else {
						kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
							"No current Session is open. Open a Session, or choose Target Hosts.", nil)
					}
				} else {
					p.drawSnippetRunTargets(c, f)
				}
				if len(f.variables) > 0 {
					ui.Text(c, "Variables").FontSize(14).FontWeight(600)
				}
				for index := range f.variables {
					p.drawSnippetRunVariable(c, index)
				}
			})
		})
		ui.Column(c).FillWidth().Padding(12, 16).Gap(8).Children(func() {
			if kit.PrimaryButton(c, "", "Run snippet").Clicked() {
				p.submitSnippetRun(c)
			}
			if kit.SecondaryButton(c, "", "Cancel").Clicked() {
				p.cancelSnippetRun()
			}
		})
	})
}

func (p *snippetPages) drawSnippetRunVariable(c *ui.Context, index int) {
	f := p.run
	variable := &f.variables[index]
	label := "Variable " + variable.name
	kit.FormRow(c, label, func() {
		ui.TextInput(c, &variable.value).FillWidth()
	}).Description(snippetVariableHint(*variable))
	if variable.remembered != "" && variable.remembered != variable.value {
		if kit.GhostButton(c, "", "Use saved value for "+variable.name).Clicked() {
			variable.value = variable.remembered
		}
	}
}

func (p *snippetPages) drawSnippetRunTargets(c *ui.Context, f *snippetRunForm) {
	if len(f.candidates) == 0 {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"This Snippet has no target Hosts in the Vault. Edit its targets or add Hosts.", nil)
		return
	}
	ui.Text(c, "Target Hosts").FontSize(14).FontWeight(600)
	for _, host := range f.candidates {
		key := string(host.ID)
		checked := f.selected[key]
		ui.Checkbox(c, &checked, "Run on "+hostName(host))
		f.selected[key] = checked
	}
}

func (p *snippetPages) cancelSnippetRun() {
	p.run = nil
	p.runProblem = ""
	p.runMissing = nil
}

// submitSnippetRun resolves the variables through the shared service, composes
// the command with the shared multi-line rule, and delivers to the chosen
// destination. For Hosts the delivery runs off the UI thread and reports one
// outcome per target.
func (p *snippetPages) submitSnippetRun(c *ui.Context) {
	f := p.run
	if f == nil {
		return
	}
	if p.store == nil {
		p.runProblem = snippetUnavailable
		return
	}
	values := f.valueMap()
	run, err := p.store.PrepareSnippet(context.Background(), f.id, values)
	if err != nil {
		var missing *appvault.SnippetVariableError
		if errors.As(err, &missing) {
			p.runMissing = append([]string(nil), missing.Missing...)
			p.runProblem = ""
			return
		}
		p.runProblem = snippetRunProblemText(err)
		return
	}
	if err := p.store.SaveSnippetVariableValues(context.Background(), f.id, values); err != nil {
		p.runProblem = "Could not remember the variable values. Try again."
		return
	}
	preview := snippetPreview(run.Snippet, run.Command)
	delay := time.Duration(0)
	if run.Snippet.MultiLineRunMode == domainvault.SnippetRunLineDelay {
		delay = appsession.ComposeLineDelay
	}
	if preview.Empty() {
		p.runProblem = "This Snippet has no command to run."
		return
	}
	if f.destinationIndex == snippetRunHere {
		p.runInCurrentSession(run, preview, delay)
		return
	}
	p.runOnHosts(c, run, preview, delay)
}

func (p *snippetPages) runInCurrentSession(run appvault.SnippetRun, preview appsession.ComposePreview, delay time.Duration) {
	label, host, handle, problem := p.currentSessionHandle()
	if problem != "" {
		p.runProblem = problem
		return
	}
	if !snippetAllowsHost(run.Snippet, host) {
		p.runProblem = "This Snippet is not targeted at " + hostName(host) +
			". Choose target Hosts, or edit the Snippet's targets."
		return
	}
	result := snippetTargetResult{target: "Current Session (" + label + ")"}
	if err := sendSnippetChunks(handle, preview.Chunks, delay); err != nil {
		result.err = "Connected, but the command was not accepted. " + err.Error() + "."
	}
	p.run = nil
	p.runMissing = nil
	p.runProblem = ""
	p.mu.Lock()
	p.runResults = []snippetTargetResult{result}
	p.mu.Unlock()
	_ = p.reload(context.Background())
}

func (p *snippetPages) runOnHosts(c *ui.Context, run appvault.SnippetRun, preview appsession.ComposePreview, delay time.Duration) {
	f := p.run
	selected := make([]domainvault.Host, 0, len(f.candidates))
	for _, host := range f.candidates {
		if f.selected[string(host.ID)] {
			selected = append(selected, host)
		}
	}
	if len(selected) == 0 {
		p.runProblem = "Select at least one target Host."
		return
	}
	p.run = nil
	p.runMissing = nil
	p.runProblem = ""
	done := make(chan struct{})
	p.mu.Lock()
	p.runBusy = true
	p.runResults = nil
	p.runDone = done
	p.mu.Unlock()
	services := c.Services()
	go func() {
		results := p.shell.runSnippetOnHosts(selected, run.Snippet, preview.Chunks, delay)
		p.mu.Lock()
		p.runResults = results
		p.runBusy = false
		p.runReload = true
		close(done)
		p.mu.Unlock()
		services.Invalidate()
	}()
}

func (p *snippetPages) consumeRunReload() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	reload := p.runReload
	p.runReload = false
	return reload
}

// runSnapshot reads the background run's state for a frame.
func (p *snippetPages) runSnapshot() (busy bool, results []snippetTargetResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runBusy, append([]snippetTargetResult(nil), p.runResults...)
}

// runSnippetOnHosts opens one Session per target, sends the command to each,
// and reports one outcome per target. Every selected Host is attempted; a
// failure of one cannot drop another.
func (s *Shell) runSnippetOnHosts(hosts []domainvault.Host, snippet domainvault.Snippet,
	chunks [][]byte, delay time.Duration) []snippetTargetResult {

	results := make([]snippetTargetResult, len(hosts))
	var wg sync.WaitGroup
	for index, host := range hosts {
		index, host := index, host
		results[index] = snippetTargetResult{target: hostName(host)}
		if !snippetAllowsHost(snippet, host) {
			results[index].err = "This Host is not a target of the Snippet. Edit its targets or choose another Host."
			continue
		}
		tab, pane := s.sessions.begin(host)
		ctx, cancel := context.WithTimeout(
			withPromptOwner(context.Background(), string(pane.id)), snippetRunTimeout)
		s.sessions.attachCancel(tab.id, string(pane.id), cancel)
		resumeKey := pane.resumeKey
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			handle, err := s.openSnippetTarget(ctx, host, resumeKey)
			if err != nil {
				s.sessions.finish(tab.id, string(pane.id), nil, err)
				results[index].err = snippetConnectProblem(host, err)
				return
			}
			sendErr := sendSnippetChunks(handle, chunks, delay)
			// A send failure keeps the connected Session; only the command did
			// not land, and that outcome is reported for this target.
			s.sessions.finish(tab.id, string(pane.id), handle, nil)
			if sendErr != nil {
				results[index].err = "Connected, but the command was not accepted. " + sendErr.Error() + "."
			}
		}()
	}
	wg.Wait()
	return results
}

// openSnippetTarget mirrors the shell's launch path: a restored resume key is
// passed to an opener that supports it, and every other open is a fresh Host
// open.
func (s *Shell) openSnippetTarget(ctx context.Context, host domainvault.Host, resumeKey string) (SessionHandle, error) {
	opener := s.sessions.opener
	if opener == nil {
		return nil, errNoOpener
	}
	if resumeKey != "" {
		if resuming, ok := opener.(ResumeKeyOpener); ok {
			return resuming.OpenHostWithResume(ctx, host, resumeKey)
		}
	}
	return opener.OpenHost(ctx, host)
}

// sendSnippetChunks writes each composed chunk in order. The chunks are the
// shared composition's exact bytes, so run-mode and NoAutoRun behavior match
// the desktop; only the delivery path differs.
func sendSnippetChunks(handle SessionHandle, chunks [][]byte, delay time.Duration) error {
	inputter, ok := handle.(sessionInputter)
	if !ok {
		return errors.New("this Session cannot receive a command")
	}
	for index, chunk := range chunks {
		if index > 0 && delay > 0 {
			time.Sleep(delay)
		}
		if !inputter.InsertInput(chunk) {
			return errors.New("the Session did not accept the bytes")
		}
	}
	return nil
}

// snippetPreview applies the shared multi-line behavior: paste by default, one
// write per line when the Snippet asks for line delay, and no trailing Enter
// when NoAutoRun is set.
func snippetPreview(snippet domainvault.Snippet, command string) appsession.ComposePreview {
	mode := appsession.ComposePaste
	if snippet.MultiLineRunMode == domainvault.SnippetRunLineDelay {
		mode = appsession.ComposeLineByLine
	}
	return appsession.TransformCompose(command, appsession.ComposeOptions{
		LineMode:  mode,
		NoAutoRun: snippet.NoAutoRun,
	})
}

// snippetAllowsHost applies the shared target rule. An unscoped Snippet runs
// anywhere; a scoped one only on the Hosts and groups it names.
func snippetAllowsHost(snippet domainvault.Snippet, host domainvault.Host) bool {
	if !domainvault.SnippetHasRunTargets(snippet) {
		return true
	}
	return domainvault.SnippetCanRunInTerminal(snippet,
		domainvault.SnippetTargetHost{ID: host.ID, Group: host.Group})
}

// snippetConnectProblem turns a failed open into the same actionable sentence
// the Sessions page uses.
func snippetConnectProblem(host domainvault.Host, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Could not connect to " + hostName(host) +
			" in time. Check the Host's address, credentials, and network, then try again."
	}
	return connectProblem(hostName(host), err)
}

// drawSnippetRunResults lists one outcome per target. A failed target names its
// reason instead of disappearing from the report.
func drawSnippetRunResults(c *ui.Context, results []snippetTargetResult) {
	for _, result := range results {
		if result.err == "" {
			kit.Notice(c, icons.CircleCheck, c.Theme().Success, "Sent to "+result.target+".", nil)
			continue
		}
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
			result.target+": "+result.err, nil)
	}
}

func (p *snippetPages) snippetByID(id string) (domainvault.Snippet, bool) {
	for _, snippet := range p.snippets {
		if snippet.ID == id {
			return snippet, true
		}
	}
	return domainvault.Snippet{}, false
}

func (p *snippetPages) pendingSnippetName() string {
	for _, snippet := range p.snippets {
		if snippet.ID == p.pendingSnippet {
			return snippetName(snippet)
		}
	}
	return p.pendingSnippet
}

// packageSummary counts the Snippets directly in one package.
func (p *snippetPages) packageSummary(path string) string {
	count := 0
	for _, snippet := range p.snippets {
		if snippet.Package == path {
			count++
		}
	}
	switch count {
	case 0:
		return "No Snippets"
	case 1:
		return "1 Snippet"
	default:
		return fmt.Sprintf("%d Snippets", count)
	}
}

func (f *snippetRunForm) valueMap() map[string]string {
	if len(f.variables) == 0 {
		return nil
	}
	values := make(map[string]string, len(f.variables))
	for _, variable := range f.variables {
		values[variable.name] = variable.value
	}
	return values
}

func snippetVariableHint(variable snippetRunVariable) string {
	switch {
	case variable.hasDefault && variable.remembered != "":
		return "Required unless left blank; blank uses " + variable.defaultValue + ". The saved value is filled in."
	case variable.hasDefault:
		return "Optional; a blank value uses " + variable.defaultValue + "."
	case variable.remembered != "":
		return "Required. The saved value is filled in."
	default:
		return "Required."
	}
}

// snippetName is the label a user recognizes, or the ID for an unlabelled
// Snippet.
func snippetName(snippet domainvault.Snippet) string {
	if label := strings.TrimSpace(snippet.Label); label != "" {
		return label
	}
	return snippet.ID
}

// snippetSummaryText is the list row's command line, kept to one line.
func snippetSummaryText(snippet domainvault.Snippet) string {
	command := strings.TrimSpace(snippet.Command)
	if command == "" {
		return "No command"
	}
	if index := strings.IndexByte(command, '\n'); index >= 0 {
		command = command[:index]
	}
	if len(command) > 72 {
		command = command[:72] + "…"
	}
	return command
}

// snippetScopeText names where the Snippet may run and the package it sits in.
func snippetScopeText(snippet domainvault.Snippet) string {
	parts := make([]string, 0, 3)
	if snippet.Package != "" {
		parts = append(parts, snippet.Package)
	}
	switch {
	case snippet.TargetsAllHosts:
		parts = append(parts, "All Hosts")
	case len(snippet.Targets) > 0 || len(snippet.TargetGroups) > 0:
		parts = append(parts, fmt.Sprintf("%d target(s)", len(snippet.Targets)+len(snippet.TargetGroups)))
	default:
		parts = append(parts, "Runs anywhere")
	}
	if n := len(domainvault.ParseSnippetVariables(snippet.Command)); n > 0 {
		parts = append(parts, fmt.Sprintf("%d variable(s)", n))
	}
	return strings.Join(parts, " · ")
}

func snippetModeIndex(mode domainvault.SnippetMultiLineRunMode) int {
	if mode == domainvault.SnippetRunLineDelay {
		return snippetModeLineDelayIndex
	}
	return snippetModePasteIndex
}

func snippetModeAt(index int) domainvault.SnippetMultiLineRunMode {
	if index == snippetModeLineDelayIndex {
		return domainvault.SnippetRunLineDelay
	}
	return domainvault.SnippetRunPaste
}

// splitSnippetList splits a comma- or newline-separated editor field.
func splitSnippetList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// snippetSaveProblemText explains a Snippet save failure using the shared
// validation fields.
func snippetSaveProblemText(err error) string {
	var invalid *domainvault.ValidationError
	if errors.As(err, &invalid) {
		switch invalid.Field {
		case domainvault.FieldSnippet:
			if invalid.Rule == domainvault.RuleRequired {
				return "Enter a name for the Snippet."
			}
			return "Check the Snippet's name and multi-line run mode."
		case domainvault.FieldSnippetCommand:
			return "The command is too long."
		case domainvault.FieldSnippetPackage:
			return "Check the Snippet package path. Use letters, digits, underscores, and dashes separated by slashes."
		}
	}
	var typed *appvault.Error
	if errors.As(err, &typed) && typed.Kind == appvault.KindConflict {
		return "That Snippet conflicts with an existing record."
	}
	return "Could not save the Snippet. Check the fields and try again."
}

func snippetPackageProblemText(err error) string {
	var invalid *domainvault.ValidationError
	if errors.As(err, &invalid) {
		switch invalid.Rule {
		case domainvault.RuleRequired:
			return "Enter a package path."
		case domainvault.RuleTooLong:
			return "The package path is too long."
		default:
			return "Check the package path. Use letters, digits, underscores, and dashes separated by slashes."
		}
	}
	var typed *appvault.Error
	if errors.As(err, &typed) && typed.Kind == appvault.KindConflict {
		return "A package already uses that path."
	}
	return "Could not save the Snippet package. Check the path and try again."
}

func snippetRunProblemText(err error) string {
	var invalid *domainvault.ValidationError
	if errors.As(err, &invalid) {
		return "The Snippet could not be prepared. Check its command and targets."
	}
	var typed *appvault.Error
	if errors.As(err, &typed) && typed.Kind == appvault.KindNotFound {
		return "The Snippet no longer exists. Refresh the list."
	}
	return "Could not prepare the Snippet. Try again."
}
