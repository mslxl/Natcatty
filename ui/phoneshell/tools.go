package phoneshell

// The Session Tools surface. It is opened from the terminal page's header and
// drawn in place of the terminal, never as a routed page: the Session's route
// does not change, so the Pane keeps its one live terminal view and closing the
// surface returns to the same Pane at the same terminal position.
//
// The surface is a small catalog of tools. Command History is the first real
// one; Files, Port Forwarding, System Management, Session Log, and Notes are
// listed now and replaced by their own tickets through drawTool. Each Pane owns
// its surface state, so switching focus cannot carry one Pane's overlay into
// another, and closing a Pane discards its overlay with it.
//
// Host-key and credential prompts are never covered: a Pane that is connecting
// draws the prompt page, not the terminal page, and the Tools entry exists only
// on the terminal page. A surface state left open across a reconnect is
// therefore not drawn until the Pane is ready again.

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// CommandHistoryService is the phone Tools surface's seam onto Command
// History. The application wires it to the same history.Service the desktop
// History surface and Atuin sync use, so search, this-device clear, matching
// clear, and delete-everywhere read and write the one journal.
type CommandHistoryService interface {
	// Entries lists the live commands, newest first.
	Entries(ctx context.Context) ([]domainhistory.Entry, error)
	// ClearThisDevice removes the rows this install created and leaves rows
	// imported or synced from another writer.
	ClearThisDevice(ctx context.Context) (int, error)
	// PruneHistoryEntries removes exactly the listed rows. The surface passes
	// the identities its confirmation counted, so an entry arriving between
	// the confirmation and its answer is never removed unseen.
	PruneHistoryEntries(ctx context.Context, ids []string) (int, error)
	// DeleteEverywhere removes every live row this device knows and queues the
	// matching Atuin delete records.
	DeleteEverywhere(ctx context.Context) (int, error)
}

// toolID names one entry of the Tools surface.
type toolID int

const (
	// toolNone is the catalog itself.
	toolNone toolID = iota
	toolHistory
	toolBrowser
	toolFiles
	toolForwarding
	toolSystem
	toolSessionLog
	toolNotes
)

// toolSpec is one catalog row: its identity, title, and what it does.
type toolSpec struct {
	id    toolID
	title string
}

// toolCatalog is the Tools surface's order. New tools land by implementing
// their branch of drawTool; the catalog and the per-Pane state already carry
// them.
var toolCatalog = []toolSpec{
	{id: toolHistory, title: "Command history"},
	{id: toolBrowser, title: "Browser"},
	{id: toolFiles, title: "Files"},
	{id: toolForwarding, title: "Port forwarding"},
	{id: toolSystem, title: "System management"},
	{id: toolSessionLog, title: "Session log"},
	{id: toolNotes, title: "Notes"},
}

// toolPlaceholder is the reason a catalog row has no page yet.
func toolPlaceholder(id toolID) string {
	switch id {
	case toolFiles:
		return "Files from this Session are not on the phone yet."
	case toolSessionLog:
		return "Session logs are not on the phone yet."
	}
	return "This tool is not on the phone yet."
}

// toolHistoryLimit bounds how many matching commands one frame lists. The
// search narrows the list; the cap only keeps a long journal from building
// thousands of rows.
const toolHistoryLimit = 50

// clearScope is one of the three independent history clears. Each has its own
// button and its own confirmation, and cancelling one deletes nothing.
type clearScope int

const (
	clearThisDevice clearScope = iota
	clearMatching
	clearEverywhere
)

// toolsClear is one pending clear confirmation. ids is the matching scope's
// frozen identity list: the confirmation names the rows it will delete.
type toolsClear struct {
	scope clearScope
	ids   []string
	count int
}

// toolsState is one Pane's Tools surface state. The zero value is a closed
// surface showing the catalog.
type toolsState struct {
	open bool
	tool toolID
	// query is the Command History search text. Matching runs over the loaded
	// entries each frame, so typing never touches the journal.
	query string
	// entries is the last successful load. loaded records that a load was
	// attempted, so a nil service is a reported state rather than an endless
	// retry.
	entries []domainhistory.Entry
	loaded  bool
	// loadProblem is the reason history cannot be used. The surface still
	// opens and every other catalog row stays available.
	loadProblem string
	// pending is the clear awaiting an answer, if any.
	pending *toolsClear
	// browser is this Pane's Browser surface, made when the Browser tool is
	// entered. It holds the Host's dedicated channel lease; leaving the tool
	// closes it, so the Session's own transport is never touched.
	browser *browserSurface
	// notes is this Pane's Notes tool view state. The Note records live in the
	// shared notePages; only the search, open viewer, and draft are per Pane,
	// so one Pane's half-filled editor never appears in another.
	notes notesViewState
	// forwarding is this Pane's Port Forwarding view. It is made when the tool
	// is entered and reads the shared runtime through the Session opener, so a
	// Pane that never opens it costs nothing.
	forwarding *forwardingPages
	// files is this Pane's Files view. It is made when the tool is entered and
	// holds its own directory, selection, and SFTP transport, so two Panes
	// never share a browse location.
	files *filesPages
	// sessionLog is this Pane's Session Log page state: the saved-log list and
	// the last outcome. The live recording state stays on the Session handle.
	sessionLog sessionLogViewState
	// result is the last clear's outcome; resultProblem marks it a failure.
	result        string
	resultProblem bool
}

// openTools shows one Pane's Tools surface at the catalog. The journal is not
// read until the Command History tool is entered, so a catalog of later tools
// never pays for it.
func (t *sessionTabs) openTools(st *toolsState) {
	if st == nil {
		return
	}
	st.open = true
	st.tool = toolNone
	st.pending = nil
	st.result = ""
	st.resultProblem = false
}

// closeTools hides one Pane's Tools surface. Nothing else changes: the
// terminal stayed attached, so the Pane and its scroll position are untouched.
func (t *sessionTabs) closeTools(st *toolsState) {
	if st == nil {
		return
	}
	st.open = false
	st.pending = nil
	// Leaving the Tools surface releases a Browser or Files view opened from
	// it. The terminal was never detached, so the Pane is exactly as it was.
	st.closeBrowser()
	st.closeFiles()
}

// closeFiles releases one Pane's Files transport. It is safe when Files was
// never opened.
func (st *toolsState) closeFiles() {
	if st == nil || st.files == nil {
		return
	}
	st.files.release()
	st.files = nil
}

// closeBrowser releases one Pane's Browser and its dedicated channel lease.
// It is safe when no Browser was ever opened.
func (st *toolsState) closeBrowser() {
	if st == nil || st.browser == nil {
		return
	}
	surface := st.browser
	st.browser = nil
	surface.close()
}

// loadToolHistory refreshes the journal behind the Command History tool. A
// nil or failing service records the reason; the surface stays usable.
func (t *sessionTabs) loadToolHistory(st *toolsState) {
	if st == nil {
		return
	}
	st.loaded = true
	st.entries = nil
	st.loadProblem = ""
	if t.history == nil {
		st.loadProblem = "Command history is not available in this build."
		return
	}
	entries, err := t.history.Entries(context.Background())
	if err != nil {
		st.loadProblem = "Command history is unavailable: " + err.Error()
		return
	}
	st.entries = entries
}

// toolMatches is the Command History result set. The query matches the command
// and the Host label, case-insensitively; an empty query lists every entry.
func (st *toolsState) toolMatches() []domainhistory.Entry {
	if st == nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(st.query))
	if query == "" {
		return st.entries
	}
	matched := make([]domainhistory.Entry, 0, len(st.entries))
	for _, entry := range st.entries {
		if strings.Contains(strings.ToLower(entry.Command), query) ||
			strings.Contains(strings.ToLower(entry.HostLabel), query) {
			matched = append(matched, entry)
		}
	}
	return matched
}

// requestToolClear opens the confirmation for one clear scope. The matching
// scope freezes the identities it counted, so the answer deletes exactly what
// the confirmation showed.
func (t *sessionTabs) requestToolClear(st *toolsState, scope clearScope) {
	if st == nil || st.loadProblem != "" {
		return
	}
	request := &toolsClear{scope: scope}
	if scope == clearMatching {
		for _, entry := range st.toolMatches() {
			request.ids = append(request.ids, entry.ID)
		}
		request.count = len(request.ids)
	}
	st.pending = request
	st.result = ""
	st.resultProblem = false
}

// resolveToolClear answers the pending confirmation. Cancel deletes nothing;
// confirm applies exactly that scope and reloads the journal.
func (t *sessionTabs) resolveToolClear(st *toolsState, confirmed bool) {
	if st == nil || st.pending == nil {
		return
	}
	request := st.pending
	st.pending = nil
	if !confirmed {
		return
	}
	if t.history == nil {
		st.result = "Command history is not available in this build."
		st.resultProblem = true
		return
	}
	ctx := context.Background()
	var (
		removed int
		err     error
	)
	switch request.scope {
	case clearThisDevice:
		removed, err = t.history.ClearThisDevice(ctx)
	case clearMatching:
		removed, err = t.history.PruneHistoryEntries(ctx, request.ids)
	case clearEverywhere:
		removed, err = t.history.DeleteEverywhere(ctx)
	}
	if err != nil {
		st.result = "Could not delete history: " + err.Error()
		st.resultProblem = true
	} else {
		st.result = fmt.Sprintf("Deleted %d entries.", removed)
		st.resultProblem = false
	}
	// Reload without clearing the result the user just caused.
	t.loadToolHistory(st)
}

// useToolCommand writes one history command into the focused Pane. Fill leaves
// it at the prompt; run adds Enter through the terminal's own key encoder, so
// the shell decides what Enter means. The surface closes either way, returning
// the user to the terminal it acted on.
func useToolCommand(st *toolsState, screen TerminalScreen, command string, run bool) {
	if st == nil {
		return
	}
	if screen == nil {
		st.result = "The terminal is not ready."
		st.resultProblem = true
		return
	}
	screen.PasteData(transfer.TextData(command))
	if run {
		screen.SendKey(ui.KeyEnter, 0)
	}
	st.open = false
	st.pending = nil
}

// drawTools renders the focused Pane's Tools surface in place of the terminal.
// The terminal screen stays attached; only its drawing is skipped.
func (t *sessionTabs) drawTools(c *ui.Context, view tabView) {
	st := view.tools
	if st == nil {
		return
	}
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).FillWidth().Padding(16).Gap(12).Children(func() {
			kit.SectionBar(c, "Tools", func() {
				if kit.GhostButton(c, icons.X, "Done").Clicked() {
					t.closeTools(st)
				}
			})
			switch st.tool {
			case toolNone:
				t.drawToolCatalog(c, st, view)
			case toolHistory:
				t.drawToolHistory(c, st, view.screen)
			case toolBrowser:
				t.drawToolBrowser(c, st, view)
			case toolForwarding:
				t.drawToolForwarding(c, st, view)
			case toolFiles:
				t.drawToolFiles(c, st, view)
			case toolSystem:
				t.drawToolSystem(c, st, view)
			case toolSessionLog:
				t.drawToolSessionLog(c, st, view)
			case toolNotes:
				t.drawToolNotes(c, st)
			default:
				t.drawToolPage(c, st)
			}
		})
	})
}

// drawToolCatalog lists the tools. Command History carries its live entry count
// or the availability reason; the Browser row is listed only for an eligible
// Host on a device whose WebView supports it, and every other row says it is
// not here yet.
func (t *sessionTabs) drawToolCatalog(c *ui.Context, st *toolsState, view tabView) {
	for _, spec := range toolCatalog {
		spec := spec
		if spec.id == toolBrowser && !t.browserToolAvailable(view.host) {
			continue
		}
		// Only an SSH-capable Host can carry a Port Forwarding Rule. A Telnet
		// or Local Host has no entry rather than a page that can only say no.
		if spec.id == toolForwarding && !view.host.SupportsForwarding() {
			continue
		}
		// File browsing opens the Host through the shared transfer service,
		// which resolves a saved Host by ID. A Quick Connect Host has no ID and
		// a restored Host can outlive its Vault record; both would show a tool
		// that can only fail, so only a saved, file-capable Host gets the entry.
		if spec.id == toolFiles && !t.filesToolEligible(view.host) {
			continue
		}
		// System management needs a saved Host with an SSH shell: the shared
		// executor resolves the Host by ID and every command runs over a
		// channel lease. A Quick Connect Host or a Telnet/Local Host would
		// only reach a page that cannot work, so the entry is hidden.
		if spec.id == toolSystem && !t.systemToolEligible(view.host) {
			continue
		}
		row := kit.ListRow(c).Label("Open " + spec.title)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, spec.title)
				ui.Text(c, t.toolSummary(st, spec.id, view)).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			})
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		})
		if row.Clicked() {
			st.tool = spec.id
			st.pending = nil
			st.result = ""
			st.resultProblem = false
			if spec.id == toolHistory {
				// Entering the tool reads the journal fresh; a command
				// completed since the last visit is part of the list.
				t.loadToolHistory(st)
			}
			if spec.id == toolNotes {
				// Entering Notes re-reads the shared Vault so a Note saved on
				// the desktop while this surface was open is visible. The
				// Pane's own selection and draft are kept, and the terminal
				// stays attached.
				if t.notes != nil {
					_ = t.notes.reload(context.Background())
				}
			}
			if spec.id == toolSessionLog {
				// Entering Session Log re-reads the saved-log directory, so a
				// log left by an earlier Session or an earlier app run is
				// reachable from the export entry.
				st.sessionLog.loaded = false
				st.sessionLog.result = ""
				st.sessionLog.resultProblem = false
			}
		}
	}
	// A device without the WebView capability still states why its entry is
	// absent, so a missing Browser reads as a fact about this phone rather
	// than a broken page. An ineligible Host needs no explanation: Telnet and
	// Local Hosts can never carry a forwarding channel.
	if browserEligible(view.host) && !t.browserCapability().Supported {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			browserReason(t.browserCapability()), nil)
	}
	// A Host that cannot use System management states the reason instead of
	// silently omitting the row, so the absence is understandable. A build
	// without the runtime still lists the row but says it is unavailable.
	if !t.systemToolEligible(view.host) {
		if reason := t.systemToolReason(view.host); reason != "" {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Warning, reason, nil)
		}
	} else if t.systemToolUnavailable() {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"System management is not available in this build.", nil)
	}
}

// toolSummary is the catalog row's second line.
func (t *sessionTabs) toolSummary(st *toolsState, id toolID, view tabView) string {
	host := view.host
	switch id {
	case toolHistory:
		if t.history == nil || st == nil || st.loadProblem != "" {
			return "Unavailable"
		}
		if !st.loaded {
			return "Search and reuse commands"
		}
		return fmt.Sprintf("%d entries", len(st.entries))
	case toolBrowser:
		if !t.browserToolAvailable(host) {
			return "Unavailable"
		}
		return "Browse this Host"
	case toolNotes:
		if t.notes == nil || t.notes.store == nil {
			return "Unavailable"
		}
		switch t.notes.status {
		case vaultLoading:
			return "Loading…"
		case vaultFailed:
			return "Unavailable"
		}
		switch len(t.notes.notes) {
		case 0:
			return "No Notes yet"
		case 1:
			return "1 Note"
		default:
			return fmt.Sprintf("%d Notes", len(t.notes.notes))
		}
	case toolFiles:
		if t.files == nil {
			return "Unavailable"
		}
		if !host.SupportsFileTransfer() {
			return "This Host cannot browse files"
		}
		if !t.filesToolEligible(host) {
			return "This Session's Host is no longer saved"
		}
		if st != nil && st.files != nil && st.files.ui.status == filesReady && st.files.ui.dir != "" {
			return st.files.ui.dir
		}
		return "Browse this Host's files"
	case toolSystem:
		if t.systemToolUnavailable() {
			return "Unavailable"
		}
		if reason := t.systemToolReason(host); reason != "" {
			return reason
		}
		return "Inspect and manage this Host"
	case toolForwarding:
		if !t.forwardingAvailable() {
			return "Unavailable"
		}
		if !host.SupportsForwarding() {
			return "This Host cannot forward ports"
		}
		if st != nil && st.forwarding != nil && st.forwarding.status == vaultReady {
			switch count := len(st.forwarding.rulesFor(host.ID)); count {
			case 0:
				return "No rules for this Host"
			case 1:
				return "1 rule for this Host"
			default:
				return fmt.Sprintf("%d rules for this Host", count)
			}
		}
		return "Start and stop this Host's rules"
	case toolSessionLog:
		if _, ok := t.sessionLogWorkspace(); !ok {
			return "Unavailable"
		}
		if handle := t.sessionLogHandle(view); handle != nil {
			status := handle.SessionLogStatus()
			if status.Active {
				return "Recording this Session"
			}
			if status.Problem != "" {
				return "Recording failed"
			}
		}
		if st != nil && st.sessionLog.loaded && len(st.sessionLog.files) > 0 {
			return sessionLogCount(len(st.sessionLog.files))
		}
		return "Record this Session to a file"
	}
	return "Not on the phone yet"
}

// filesToolEligible reports whether the Files tool can open this Session's
// Host right now. The shared opener resolves the destination by saved Host ID,
// so an Unsaved Host (Quick Connect) or a restored Host whose Vault record was
// deleted cannot open Files. The entry is hidden for those instead of shown
// broken; a missing transfer service is not part of this test, because that
// row still opens and states why the tool is unavailable.
func (t *sessionTabs) filesToolEligible(host domainvault.Host) bool {
	if !host.SupportsFileTransfer() {
		return false
	}
	if host.ID == "" || t == nil || t.fileHosts == nil {
		return false
	}
	for _, saved := range t.fileHosts() {
		if saved.ID == host.ID {
			return true
		}
	}
	return false
}

// systemToolEligible reports whether the System Management tool may be listed
// for this Session's Host. A Host's own eligibility decides the row; a build
// without the runtime still lists it and the page states that reason.
func (t *sessionTabs) systemToolEligible(host domainvault.Host) bool {
	if t == nil {
		return false
	}
	if t.systemEligible != nil {
		return t.systemEligible(host)
	}
	return host.ID != "" && host.Protocol.UsesSSHAuth()
}

// systemToolReason is the inline reason the System Management entry is
// absent for this Host, or "" when the row may be shown.
func (t *sessionTabs) systemToolReason(host domainvault.Host) string {
	if t == nil {
		return "System management is not available in this build."
	}
	if t.systemHostReason != nil {
		return t.systemHostReason(host)
	}
	if host.ID != "" && host.Protocol.UsesSSHAuth() {
		return ""
	}
	return "This Host has no SSH shell for system management."
}

// systemToolUnavailable is the build-level reason the tool is listed but
// cannot open. It is shown by the tool page and its catalog summary.
func (t *sessionTabs) systemToolUnavailable() bool {
	return t == nil || t.system == nil || !t.system.available()
}

// drawToolSystem renders the System Manager for this Session's Host. The
// terminal stays attached underneath; All tools returns to the catalog and
// Done returns to the Pane.
func (t *sessionTabs) drawToolSystem(c *ui.Context, st *toolsState, view tabView) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		return
	}
	if reason := t.systemToolReason(view.host); reason != "" {
		kit.EmptyHint(c, reason)
		return
	}
	if t.systemToolUnavailable() {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"System management is not available in this build.", nil)
		return
	}
	t.system.draw(c, view.host)
}

// browserToolAvailable reports whether this Pane's Host can offer the Browser
// right now. The entry is hidden, not disabled, when it is false.
func (t *sessionTabs) browserToolAvailable(host domainvault.Host) bool {
	return t != nil && t.browser != nil && browserEligible(host) && t.browserCapability().Supported
}

// browserCapability reads the provider's device capability. A sessionTabs with
// no provider fails closed with a stated reason.
func (t *sessionTabs) browserCapability() BrowserCapability {
	if t == nil || t.browser == nil {
		return BrowserCapability{Reason: "The Host Browser is not available in this build."}
	}
	capability := t.browser.Capability()
	if !capability.Supported {
		capability.Reason = browserReason(capability)
	}
	return capability
}

// drawToolBrowser renders the Browser inside the Tools surface. The browser
// gets a dedicated channel lease, so the Pane's terminal stays attached; Done
// closes the browser's gateway and lease and returns to the terminal.
func (t *sessionTabs) drawToolBrowser(c *ui.Context, st *toolsState, view tabView) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		st.closeBrowser()
		return
	}
	if !t.browserToolAvailable(view.host) {
		kit.EmptyHint(c, browserReason(t.browserCapability()))
		return
	}
	if st.browser == nil {
		browser, err := t.browser.Open(view.host, t.browserWindow())
		if err != nil {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Danger,
				"Could not open the Browser: "+err.Error(), nil)
			return
		}
		st.browser = newBrowserSurface(browser, nil)
	}
	kit.SectionBar(c, "Browser · "+hostName(view.host), nil)
	drawBrowserSurface(c, st.browser, "Close Browser", func() {
		// Close the whole surface: the user asked to leave the Browser, and
		// the terminal underneath was never detached.
		st.open = false
		st.pending = nil
		st.closeBrowser()
	})
}

// drawToolNotes renders the Notes tool inside the Tools surface. All tools
// returns to the catalog; the tool's own Back returns from a Note to this
// Pane's list. The terminal and its scroll are untouched throughout, and Done
// in the surface bar returns to the Pane at the same position.
func (t *sessionTabs) drawToolNotes(c *ui.Context, st *toolsState) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		return
	}
	if t.notes == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, noteUnavailable, nil)
		return
	}
	t.notes.drawTool(c, &st.notes)
}

// forwardingAvailable reports whether this build wired the Port Forwarding
// runtime through the Session opener.
func (t *sessionTabs) forwardingAvailable() bool {
	if t == nil {
		return false
	}
	provider, ok := t.opener.(ForwardingProvider)
	if !ok {
		return false
	}
	return provider.ForwardingService() != nil
}

// forwardingSurface returns this Pane's forwarding view, creating it on first
// use. It reads the service through the opener so the Tools surface does not
// need a second copy of the runtime.
func (t *sessionTabs) forwardingSurface(st *toolsState) (*forwardingPages, bool) {
	if st == nil || !t.forwardingAvailable() {
		return nil, false
	}
	provider := t.opener.(ForwardingProvider)
	if st.forwarding == nil {
		st.forwarding = newForwardingPages()
	}
	st.forwarding.service = provider.ForwardingService()
	return st.forwarding, st.forwarding.service != nil
}

// drawToolFiles renders this Session Host's files. The terminal stays attached
// underneath; All tools returns to the catalog and Done returns to the Pane.
// When the Session reports a working directory, the tool offers Follow
// terminal directory, which moves the browser whenever the shell changes
// directory.
func (t *sessionTabs) drawToolFiles(c *ui.Context, st *toolsState, view tabView) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		st.closeFiles()
		return
	}
	if t.files == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"Files is not available in this build.", nil)
		return
	}
	if st.files == nil {
		st.files = newFilesPages(t.files, t.fileHosts)
	}
	// Every Files surface shows the one global queue, so a copy started from a
	// Session tool appears on the Host detail page and the Sessions home too.
	st.files.transferQueue = t.queue
	if directory, ok := view.handle.(SessionDirectory); ok {
		st.files.followCWD = directory.SessionCWD
	} else {
		st.files.followCWD = nil
		st.files.ui.follow = false
	}
	if st.files.followCWD != nil {
		if kit.FormToggle(c, "Follow terminal directory", &st.files.ui.follow, nil) &&
			st.files.ui.follow {
			// Establish the baseline the next terminal directory is compared
			// against. If the terminal is already where the browser is, record
			// that directory rather than clearing the baseline: an empty
			// baseline would make the next manual move look like a terminal
			// change and snap the browser back once. When the terminal is
			// elsewhere, clearing it asks followTerminal to move there now.
			cwd := strings.TrimSpace(st.files.followCWD())
			if cwd != "" && cwd != st.files.ui.dir {
				st.files.ui.followTried = ""
			} else {
				st.files.ui.followTried = cwd
			}
		}
		if st.files.ui.follow && strings.TrimSpace(st.files.followCWD()) == "" {
			ui.Text(c, "The terminal has not reported a directory yet.").FontSize(12).
				TextColor(c.Theme().TextMuted)
		}
	}
	st.files.draw(c, view.host, true)
}

// drawToolForwarding renders this Session Host's rules. The terminal stays
// attached underneath; All tools returns to the catalog and Done returns to
// the Pane.
func (t *sessionTabs) drawToolForwarding(c *ui.Context, st *toolsState, view tabView) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		return
	}
	surface, ok := t.forwardingSurface(st)
	if !ok {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"Port forwarding is not available in this build.", nil)
		return
	}
	surface.drawHostRules(c, view.host)
}

// browserWindow returns the Sessions window the Browser's WebViews belong to.
// It is nil before Show, when Open then reports the missing window.
func (t *sessionTabs) browserWindow() *mygo.Window {
	if t == nil || t.window == nil {
		return nil
	}
	return t.window()
}

// drawToolHistory renders the Command History tool: search, the three clears,
// the pending confirmation, the last result, and the matching commands.
func (t *sessionTabs) drawToolHistory(c *ui.Context, st *toolsState, screen TerminalScreen) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		st.pending = nil
		return
	}
	if st.loadProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, st.loadProblem, nil)
		return
	}
	if !st.loaded {
		t.loadToolHistory(st)
	}
	kit.FormRow(c, "Search", func() {
		ui.TextInput(c, &st.query).Placeholder("Commands or hosts").FillWidth()
	})
	matches := st.toolMatches()
	ui.Text(c, fmt.Sprintf("%d matching entries", len(matches))).FontSize(12).
		TextColor(c.Theme().TextMuted)
	t.drawToolClears(c, st, len(matches))
	if st.pending != nil {
		t.drawToolConfirm(c, st)
	}
	if st.result != "" {
		tone := c.Theme().Success
		icon := icons.CircleCheck
		if st.resultProblem {
			tone, icon = c.Theme().Danger, icons.TriangleAlert
		}
		kit.Notice(c, icon, tone, st.result, nil)
	}
	if len(matches) == 0 {
		kit.EmptyHint(c, "No commands match this search.")
		return
	}
	if len(matches) > toolHistoryLimit {
		ui.Text(c, fmt.Sprintf("Showing the first %d. Search to narrow the list.", toolHistoryLimit)).
			FontSize(12).TextColor(c.Theme().TextMuted)
		matches = matches[:toolHistoryLimit]
	}
	for _, entry := range matches {
		entry := entry
		row := kit.ListRow(c)
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, entry.Command).FontSize(13).SingleLine()
				meta := entry.HostLabel
				if meta == "" {
					meta = "This device"
				}
				ui.Text(c, meta).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.CornerDownLeft, "Fill: "+entry.Command).Clicked() {
				useToolCommand(st, screen, entry.Command, false)
				return
			}
			if kit.IconButton(c, icons.Play, "Run: "+entry.Command).Clicked() {
				useToolCommand(st, screen, entry.Command, true)
			}
		})
	}
}

// drawToolClears renders the three independent destructive actions. Each one
// opens its own confirmation; none of them deletes on the tap itself.
func (t *sessionTabs) drawToolClears(c *ui.Context, st *toolsState, matches int) {
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.GhostButton(c, icons.Trash, "Clear this device").Clicked() {
			t.requestToolClear(st, clearThisDevice)
		}
		if kit.GhostButton(c, icons.Trash, "Clear matching entries").Clicked() {
			t.requestToolClear(st, clearMatching)
		}
		if kit.GhostButton(c, icons.Trash, "Delete everywhere").Clicked() {
			t.requestToolClear(st, clearEverywhere)
		}
	})
	if matches == 0 {
		ui.Text(c, "The matching clear has nothing to delete.").FontSize(12).
			TextColor(c.Theme().TextMuted)
	}
}

// drawToolConfirm renders the one pending clear confirmation. The detail names
// the exact scope, so the user can tell the three actions apart.
func (t *sessionTabs) drawToolConfirm(c *ui.Context, st *toolsState) {
	request := st.pending
	text, detail := "", ""
	switch request.scope {
	case clearThisDevice:
		text = "Clear history recorded on this device?"
		detail = "Imported and synced entries from other devices stay."
	case clearMatching:
		text = fmt.Sprintf("Delete %d matching entries?", request.count)
		detail = "Only the entries matching the current search are removed."
	case clearEverywhere:
		text = "Delete history everywhere?"
		detail = "Every entry this device knows is removed and the deletion syncs through Atuin."
	}
	cancelled, confirmed := kit.ConfirmBar(c, text, func() {
		ui.Text(c, detail).FontSize(12).TextColor(c.Theme().TextMuted)
	}, "Cancel", "Delete")
	switch {
	case cancelled:
		t.resolveToolClear(st, false)
	case confirmed:
		t.resolveToolClear(st, true)
	}
}

// drawToolPage renders a catalog row whose own ticket has not landed. The page
// is the skeleton later tickets replace with their real content.
func (t *sessionTabs) drawToolPage(c *ui.Context, st *toolsState) {
	if kit.GhostButton(c, icons.ChevronLeft, "All tools").Clicked() {
		st.tool = toolNone
		return
	}
	title := ""
	for _, spec := range toolCatalog {
		if spec.id == st.tool {
			title = spec.title
			break
		}
	}
	kit.SectionBar(c, title, nil)
	kit.EmptyHint(c, toolPlaceholder(st.tool))
}
