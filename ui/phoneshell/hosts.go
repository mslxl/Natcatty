package phoneshell

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// HostStore is the Vault read surface the Hosts section needs. The application
// Vault service implements it over the same SQLite store the desktop reads, so
// a Host added, edited, or removed on the desktop appears here after a reload.
// The Host Group half is the same operation the desktop uses to move Hosts in
// one all-or-nothing transaction.
//
// The phone lists only remote Hosts. A Local Host would run a shell on the
// phone itself, which the phone has no transport for, and a serial device is
// not a Host record: both stay in the Vault for the desktop and never appear
// on a phone surface.
type HostStore interface {
	HostGroupStore
	List(ctx context.Context) ([]domainvault.Host, error)
}

// hostsStatus is the state of the Hosts list.
type hostsStatus int

const (
	hostsLoading hostsStatus = iota
	hostsReady
	hostsFailed
)

// hostPages owns the Hosts section: the remote-only list, its detail page, and
// the connect action. The Vault's Hosts family draws the same list with the
// group drop targets and the multi-select Move action; the Hosts tab keeps its
// plain list, where a tap still connects.
type hostPages struct {
	store HostStore
	// create is the Vault write surface. The editor uses the same application
	// service through the shared form state; a Shell without it shows that
	// this build cannot edit Hosts.
	create HostCreator
	// editor is the full Host editor shared with the desktop form state. It is
	// nil when the build has no Host write surface.
	editor *hostEditor
	// newHost opens the editor for a manual Host. editHost opens it for one
	// listed record. The Shell sets both, because it owns the router.
	newHost  func()
	editHost func(domainvault.Host)
	// open opens a Host Session. The Shell sets it so a row tap switches to
	// Sessions and pushes the terminal page there.
	open func(domainvault.Host)
	// details pushes one Host's detail page on the Hosts section's own
	// router. The Shell sets it, because the section owns the router.
	details func(domainvault.Host)
	// saveDone navigates after a save: to the saved Host's page, to the new
	// Session, or away from the editor when the list refresh failed.
	saveDone func(domainvault.Host, bool, bool)
	// detailProblem is a failed Copy action's inline message on a detail page.
	detailProblem string
	// detailHost is the Host that message belongs to, so moving to another
	// Host's page never shows the previous Host's failure.
	detailHost string

	status  hostsStatus
	problem string
	hosts   []domainvault.Host

	// groups are the Host Group drop targets the Vault's Hosts page shows.
	// The Hosts tab ignores them, so a group read failure cannot hide a Host.
	groups       []domainvault.Group
	groupStatus  hostsStatus
	groupProblem string

	// selecting is the Vault Hosts page's multi-select mode. The Hosts tab
	// never activates it, so a plain tap there always connects.
	selecting bool
	selected  map[model.HostID]struct{}
	// moveOpen shows the Host Group picker.
	moveOpen bool
	// pendingMove is the failed move a Retry re-runs, with the selection kept.
	pendingMove *moveRequest
	moveProblem string
}

// moveRequest is a batch move that failed and can be retried unchanged.
type moveRequest struct {
	IDs        []model.HostID
	GroupID    string
	GroupLabel string
}

// hostDrag is the value a Host row drags onto a Host Group row. It carries
// the selected Hosts (or just the dragged one) so both move gestures call the
// same batch operation.
type hostDrag struct {
	IDs []model.HostID
}

func newHostPages() *hostPages {
	return &hostPages{status: hostsLoading, groupStatus: hostsLoading}
}

// load replaces the list from the shared store. Local Hosts are dropped here,
// at the phone's composition seam, rather than in the domain: the desktop
// still sees and edits them, and a phone edit can never reach them because
// they never enter the phone's list.
func (h *hostPages) load(ctx context.Context) error {
	if h.store == nil {
		const unavailable = "The Vault is not available on this device."
		h.status, h.problem = hostsFailed, unavailable
		h.groupStatus, h.groupProblem = hostsFailed, unavailable
		return nil
	}
	hostErr := h.loadHosts(ctx)
	// A group failure is the Vault Hosts page's own recoverable state; the
	// Host list, the Hosts tab, and every other Vault section stay usable.
	_ = h.loadGroups(ctx)
	h.pruneSelection()
	return hostErr
}

func (h *hostPages) loadHosts(ctx context.Context) error {
	h.status = hostsLoading
	all, err := h.store.List(ctx)
	if err != nil {
		h.status = hostsFailed
		h.problem = "Could not load Hosts."
		return err
	}
	remote := make([]domainvault.Host, 0, len(all))
	for _, host := range all {
		if host.IsRemote() {
			remote = append(remote, host)
		}
	}
	sort.SliceStable(remote, func(i, j int) bool {
		if remote[i].Pinned != remote[j].Pinned {
			return remote[i].Pinned
		}
		return strings.ToLower(remote[i].Label) < strings.ToLower(remote[j].Label)
	})
	h.hosts = remote
	h.status = hostsReady
	h.problem = ""
	return nil
}

// loadGroups reads the Host Groups the Vault's Hosts page offers as drop
// targets. The list is sorted by path so the same tree reads the same way on
// both devices; the service already materializes the paths.
func (h *hostPages) loadGroups(ctx context.Context) error {
	h.groupStatus = hostsLoading
	groups, err := h.store.ListGroups(ctx)
	if err != nil {
		h.groupStatus = hostsFailed
		h.groupProblem = "Could not load Host Groups."
		return err
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Path) < strings.ToLower(groups[j].Path)
	})
	h.groups = groups
	h.groupStatus = hostsReady
	h.groupProblem = ""
	return nil
}

// summary is the Hosts section's one-line description on any surface that
// reports it.
func (h *hostPages) summary() string {
	switch h.status {
	case hostsLoading:
		return "Loading…"
	case hostsFailed:
		return "Unavailable"
	}
	switch len(h.hosts) {
	case 0:
		return "No remote Hosts"
	case 1:
		return "1 remote Host"
	default:
		return fmt.Sprintf("%d remote Hosts", len(h.hosts))
	}
}

// host finds a Host by identity in the loaded list.
func (h *hostPages) host(id string) (domainvault.Host, bool) {
	for _, host := range h.hosts {
		if string(host.ID) == id {
			return host, true
		}
	}
	return domainvault.Host{}, false
}

// drawRoot renders the Hosts list. Tapping a row opens the Host; the info
// button pushes its detail page, which is what gives the section a stack of
// its own.
func (h *hostPages) drawRoot(c *ui.Context) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			switch h.status {
			case hostsLoading:
				kit.EmptyHint(c, "Loading Hosts…")
				return
			case hostsFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = h.load(context.Background())
					}
				})
				return
			}
			kit.SectionBar(c, "Remote Hosts", func() {
				if h.newHost != nil {
					if kit.GhostButton(c, icons.Plus, "New Host").Clicked() {
						h.newHost()
					}
				}
				if kit.GhostButton(c, icons.RefreshCw, "Refresh").Clicked() {
					_ = h.load(context.Background())
				}
			})
			if h.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.problem, nil)
			}
			if len(h.hosts) == 0 {
				kit.EmptyHint(c, "No remote Hosts yet. Hosts you add on the desktop appear here.")
				return
			}
			for _, host := range h.hosts {
				host := host
				row := kit.ListRow(c).Label("Open " + hostName(host))
				row.Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, hostName(host)).FontWeight(600).SingleLine()
						ui.Text(c, hostDetail(host)).FontSize(12).
							TextColor(c.Theme().TextMuted).SingleLine()
					})
					if kit.IconButton(c, icons.Info, "Details for "+hostName(host)).Clicked() {
						h.pushDetails(host)
						return
					}
					ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
				})
				if row.Clicked() {
					if h.open != nil {
						h.open(host)
					}
				}
			}
		})
	})
}

// drawVaultRoot renders the Vault's Hosts family: the Host Groups as drop
// targets, the remote Hosts, and the multi-select Move action. A tap outside
// selection mode still connects, so this page adds organizing actions without
// taking the Hosts tab's primary gesture away.
func (h *hostPages) drawVaultRoot(c *ui.Context, details func(domainvault.Host)) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			switch h.status {
			case hostsLoading:
				kit.EmptyHint(c, "Loading Hosts…")
				return
			case hostsFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = h.load(context.Background())
					}
				})
				return
			}
			h.drawVaultActions(c)
			if h.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.problem, nil)
			}
			if h.moveProblem != "" && h.pendingMove != nil {
				h.drawMoveProblem(c)
			}
			h.drawGroups(c)
			if h.moveOpen {
				h.drawMovePicker(c)
			}
			if len(h.hosts) == 0 {
				kit.EmptyHint(c, "No remote Hosts yet. Hosts you add on the desktop appear here.")
				return
			}
			for _, host := range h.hosts {
				host := host
				h.drawVaultHostRow(c, host, details)
			}
		})
	})
}

// drawVaultActions is the page heading: Refresh and Select, or Done while a
// selection is active, with the Move action beside the selection count.
func (h *hostPages) drawVaultActions(c *ui.Context) {
	kit.SectionBar(c, "Remote Hosts", func() {
		if h.selecting {
			if kit.GhostButton(c, icons.X, "Done").Clicked() {
				h.clearSelection()
			}
			return
		}
		if kit.GhostButton(c, icons.RefreshCw, "Refresh").Clicked() {
			_ = h.load(context.Background())
		}
		if len(h.hosts) > 0 {
			if kit.GhostButton(c, icons.Check, "Select").Clicked() {
				h.selecting = true
			}
		}
	})
	if !h.selecting {
		return
	}
	count := len(h.selectedIDs())
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		if count > 0 {
			if kit.PrimaryButton(c, icons.FolderTree, "Move").Clicked() {
				h.moveOpen = true
			}
		}
		ui.Text(c, fmt.Sprintf("%d selected", count)).FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

// drawVaultHostRow is one Host row on the Vault page. In selection mode the
// row toggles instead of connecting and shows a checkbox; outside it, the
// whole row connects as on the Hosts tab. A row drags the selection when it is
// part of one, and only itself otherwise.
func (h *hostPages) drawVaultHostRow(c *ui.Context, host domainvault.Host, details func(domainvault.Host)) {
	label := "Open " + hostName(host)
	if h.selecting {
		label = "Select " + hostName(host)
	}
	row := kit.ListRow(c).Label(label)
	row.Drag(hostDrag{IDs: h.dragIDs(host)})
	handled := false
	row.Children(func() {
		if h.selecting {
			checked := h.isSelected(host.ID)
			if ui.Checkbox(c, &checked, "").Changed() {
				h.toggleHost(host.ID)
				handled = true
				c.Invalidate()
			}
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, hostName(host)).FontWeight(600).SingleLine()
			ui.Text(c, hostDetail(host)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
		})
		if !h.selecting {
			if kit.IconButton(c, icons.Info, "Details for "+hostName(host)).Clicked() {
				if details != nil {
					details(host)
				}
				handled = true
				return
			}
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		}
	})
	if handled || !row.Clicked() {
		return
	}
	if h.selecting {
		h.toggleHost(host.ID)
		return
	}
	if h.open != nil {
		h.open(host)
	}
}

// drawGroups renders the Host Groups as drop targets. The row accepts the
// value a Host row drags and performs the same batch move the Move picker
// does, so the two gestures cannot disagree about what a move means.
func (h *hostPages) drawGroups(c *ui.Context) {
	switch h.groupStatus {
	case hostsLoading:
		kit.EmptyHint(c, "Loading Host Groups…")
		return
	case hostsFailed:
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.groupProblem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				_ = h.loadGroups(context.Background())
			}
		})
		return
	}
	if len(h.groups) == 0 {
		return
	}
	kit.SectionBar(c, "Host Groups", nil)
	for _, group := range h.groups {
		group := group
		row := kit.ListRow(c).Label("Group " + group.Path)
		if _, ok := ui.DragOver[hostDrag](row); ok {
			row.Background(c.Theme().Accent.Alpha(0.12))
		}
		row.Children(func() {
			ui.Icon(c, icons.Lucide(icons.Folder)).TextColor(c.Theme().TextMuted)
			ui.Text(c, group.Path).Grow(1).MinWidth(0).SingleLine()
		})
		if drag, ok := ui.Drop[hostDrag](row); ok {
			h.applyMove(drag.IDs, group.ID, group.Path)
		}
	}
}

// drawMoveProblem shows the failed batch move with a Retry that re-runs the
// same target. The selection stays in place, so Retry needs no re-picking.
func (h *hostPages) drawMoveProblem(c *ui.Context) {
	kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.moveProblem, func() {
		if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
			request := *h.pendingMove
			h.applyMove(request.IDs, request.GroupID, request.GroupLabel)
		}
	})
}

// drawMovePicker lists the destination groups plus the Vault root. Choosing
// one runs the same batch operation a drop runs.
func (h *hostPages) drawMovePicker(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).FillWidth().Padding(12).Gap(8).Background(t.Surface).
		Radius(12).Border(1, t.Border).Children(func() {
		ui.Text(c, "Move to").FontWeight(600)
		targets := append([]moveTarget{{Label: "Vault root"}}, h.groupMoveTargets()...)
		for _, target := range targets {
			target := target
			row := ui.Row(c).FillWidth().Padding(8, 10).Gap(8).AlignItems(ui.Center).
				Background(kit.Muted(t)).Radius(8).Label("Move to " + target.Label)
			row.Children(func() {
				ui.Icon(c, icons.Lucide(icons.FolderTree)).TextColor(t.TextMuted)
				ui.Text(c, target.Label).Grow(1).MinWidth(0).SingleLine()
			})
			if row.Clicked() {
				h.applyMove(h.selectedIDs(), target.ID, target.Label)
			}
		}
		if kit.GhostButton(c, icons.X, "Cancel Move").Clicked() {
			h.moveOpen = false
		}
	})
}

// moveTarget is one destination in the Move picker. The empty ID is the Vault
// root, which clears a Host's group membership.
type moveTarget struct {
	ID    string
	Label string
}

func (h *hostPages) groupMoveTargets() []moveTarget {
	targets := make([]moveTarget, 0, len(h.groups))
	for _, group := range h.groups {
		targets = append(targets, moveTarget{ID: group.ID, Label: group.Path})
	}
	return targets
}

// applyMove is the one batch move behind both gestures. It keeps the
// selection when the shared operation fails, so the user can retry the same
// target instead of reconstructing the selection.
func (h *hostPages) applyMove(ids []model.HostID, groupID, groupLabel string) {
	if len(ids) == 0 {
		return
	}
	if h.store == nil {
		h.failMove(ids, groupID, groupLabel)
		return
	}
	if err := h.store.AssignHostsGroup(context.Background(), ids, groupID); err != nil {
		h.failMove(ids, groupID, groupLabel)
		return
	}
	h.pendingMove = nil
	h.moveProblem = ""
	h.moveOpen = false
	h.selecting = false
	h.selected = nil
	// Re-read the same store: the move's result is the desktop's, not a local
	// guess. A list failure keeps the page's own retry state.
	_ = h.load(context.Background())
}

func (h *hostPages) failMove(ids []model.HostID, groupID, groupLabel string) {
	h.pendingMove = &moveRequest{
		IDs:        append([]model.HostID(nil), ids...),
		GroupID:    groupID,
		GroupLabel: groupLabel,
	}
	h.moveProblem = "Could not move the Hosts to " + groupLabel + "."
	h.selecting = true
	h.moveOpen = false
	h.selected = make(map[model.HostID]struct{}, len(ids))
	for _, id := range ids {
		h.selected[id] = struct{}{}
	}
}

func (h *hostPages) isSelected(id model.HostID) bool {
	if h.selected == nil {
		return false
	}
	_, ok := h.selected[id]
	return ok
}

func (h *hostPages) toggleHost(id model.HostID) {
	if h.selected == nil {
		h.selected = make(map[model.HostID]struct{})
	}
	if _, ok := h.selected[id]; ok {
		delete(h.selected, id)
		return
	}
	h.selected[id] = struct{}{}
}

// selectedIDs returns the selection in list order so both the picker and a
// drag hand the service a stable, deterministic batch.
func (h *hostPages) selectedIDs() []model.HostID {
	ids := make([]model.HostID, 0, len(h.selected))
	for _, host := range h.hosts {
		if h.isSelected(host.ID) {
			ids = append(ids, host.ID)
		}
	}
	return ids
}

// dragIDs is what one Host row drags: the whole selection when the row is part
// of it, otherwise the row alone.
func (h *hostPages) dragIDs(host domainvault.Host) []model.HostID {
	if h.selecting && h.isSelected(host.ID) {
		if ids := h.selectedIDs(); len(ids) > 0 {
			return ids
		}
	}
	return []model.HostID{host.ID}
}

// pruneSelection forgets Hosts that left the list, so a selection cannot name
// a Host the page no longer shows.
func (h *hostPages) pruneSelection() {
	if len(h.selected) == 0 {
		return
	}
	present := make(map[model.HostID]struct{}, len(h.hosts))
	for _, host := range h.hosts {
		present[host.ID] = struct{}{}
	}
	for id := range h.selected {
		if _, ok := present[id]; !ok {
			delete(h.selected, id)
		}
	}
}

func (h *hostPages) clearSelection() {
	h.selecting = false
	h.selected = nil
	h.moveOpen = false
	h.moveProblem = ""
	h.pendingMove = nil
}

// pushDetails moves the Hosts section to one Host's detail page. The Shell owns
// the router, so this is a callback rather than a direct router call.
func (h *hostPages) pushDetails(host domainvault.Host) {
	if h.details != nil {
		h.details(host)
	}
}

// drawDetail renders one Host's non-secret record and its Connect action.
func (h *hostPages) drawDetail(c *ui.Context, id string) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			host, ok := h.host(id)
			if !ok {
				kit.EmptyHint(c, "This Host is no longer in the Vault. Refresh the Hosts list.")
				return
			}
			if h.detailHost != string(host.ID) {
				h.detailHost = string(host.ID)
				h.detailProblem = ""
			}
			kit.SectionBar(c, hostName(host), nil)
			h.detailColumn(c, host)
			if h.detailProblem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, h.detailProblem, nil)
			}
			if h.editHost != nil {
				edit := kit.ListRow(c).Label("Edit " + hostName(host))
				edit.Children(func() {
					ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icons.Lucide(icons.Pencil)).TextColor(c.Theme().Accent)
						ui.Text(c, "Edit").FontWeight(600)
					})
					ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
				})
				if edit.Clicked() {
					h.editHostEntry(host)
				}
			}
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				if kit.GhostButton(c, icons.Copy, "Copy hostname").Clicked() {
					c.Services().WriteClipboard(host.Hostname)
				}
				if kit.GhostButton(c, icons.KeyRound, "Copy credentials").Clicked() {
					if text, ok := h.copyCredentials(host.ID); ok {
						c.Services().WriteClipboard(text)
					}
					c.Invalidate()
				}
			})
			// The whole row is the tap target, as every other phone action is;
			// a bare button's text node gives the tester no box to click.
			action := kit.ListRow(c).Label("Connect to " + hostName(host))
			action.Children(func() {
				ui.Row(c).Grow(1).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icons.Lucide(icons.Terminal)).TextColor(c.Theme().Accent)
					ui.Text(c, "Connect").FontWeight(600)
				})
				ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
			})
			if action.Clicked() && h.open != nil {
				h.open(host)
			}
		})
	})
}

// detailColumn shows the effective connection path and the non-secret fields
// that let a user tell two Hosts apart. The endpoint is the resolved one a
// Session would dial, and the jump chain says whether the Host owns it or
// inherits it from a Host Group, so the page explains the actual route.
func (h *hostPages) detailColumn(c *ui.Context, host domainvault.Host) {
	effective := h.effectiveHost(host)
	chain, chainSource := h.effectiveChain(host)
	ui.Column(c).FillWidth().PaddingY(12).Gap(6).Background(c.Theme().Surface).Radius(12).
		Children(func() {
			detailLine(c, "Endpoint", hostEndpoint(effective))
			detailLine(c, "Protocol", string(protocolOf(effective)))
			if effective.Group != "" {
				detailLine(c, "Group", effective.Group)
			}
			chainText := "Direct"
			if len(chain) > 0 {
				chainText = strings.Join(h.hopLabels(chain), " → ")
			}
			detailLine(c, "Jump chain", chainText)
			detailLine(c, "Source", chainSource)
			if len(host.Tags) > 0 {
				detailLine(c, "Tags", strings.Join(host.Tags, ", "))
			}
			if host.Notes != "" {
				detailLine(c, "Notes", host.Notes)
			}
		})
}

// effectiveHost applies the same group defaults and protocol port fallback the
// Session's route resolver uses. The stored record is not changed.
func (h *hostPages) effectiveHost(host domainvault.Host) domainvault.Host {
	effective := domainvault.MaterializeHostGroup(host, h.groups)
	effective = domainvault.ApplyGroupDefaults(effective, h.groupDefaults(host))
	effective.Port = domainvault.EffectivePort(effective.Protocol, effective.Port)
	return effective
}

// effectiveChain reports the jump chain the route would use and where it came
// from. A Host's own non-nil chain wins whole, including an explicit empty
// chain that means direct; otherwise the nearest group default applies.
func (h *hostPages) effectiveChain(host domainvault.Host) ([]model.HostID, string) {
	if host.HostChain != nil {
		return host.HostChain.IDs(), "This Host"
	}
	if protocolOf(host) == domainvault.ProtocolLocal {
		return nil, "Direct connection"
	}
	defaults := h.groupDefaults(host)
	if defaults.HostChain == nil {
		return nil, "Direct connection"
	}
	owner := h.chainOwner(host)
	if owner == "" {
		return defaults.HostChain.IDs(), "Inherited from a Host Group"
	}
	return defaults.HostChain.IDs(), "Inherited from " + owner
}

// groupDefaults resolves the merged group defaults for a Host, by stable ID
// when it has one and by legacy path otherwise.
func (h *hostPages) groupDefaults(host domainvault.Host) domainvault.GroupDefaults {
	if host.GroupID != "" {
		return domainvault.ResolveGroupDefaultsByID(h.groups, host.GroupID)
	}
	if host.Group != "" {
		return domainvault.ResolveGroupDefaults(h.groups, host.Group)
	}
	return domainvault.GroupDefaults{}
}

// chainOwner is the nearest Host Group whose own record sets a jump chain.
func (h *hostPages) chainOwner(host domainvault.Host) string {
	var chain []domainvault.Group
	switch {
	case host.GroupID != "":
		chain = domainvault.GroupChainByID(h.groups, host.GroupID)
	case host.Group != "":
		if group, ok := domainvault.GroupByPath(h.groups, host.Group); ok {
			chain = domainvault.GroupChainByID(h.groups, group.ID)
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].HostChain != nil {
			return chain[i].Path
		}
	}
	return ""
}

// hopLabels names the jump Hosts a user recognizes, falling back to the ID for
// a dangling reference a non-cascading delete left behind.
func (h *hostPages) hopLabels(ids []model.HostID) []string {
	labels := make([]string, 0, len(ids))
	for _, id := range ids {
		if host, ok := h.host(string(id)); ok {
			labels = append(labels, hostName(host))
			continue
		}
		labels = append(labels, string(id))
	}
	return labels
}

func detailLine(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().Gap(12).Children(func() {
		ui.Text(c, label).Width(84).FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, value).Grow(1).MinWidth(0)
	})
}

// hostName is the label a user recognizes, or the hostname for an unlabelled
// Host, matching the desktop's Tab label.
func hostName(host domainvault.Host) string {
	if label := strings.TrimSpace(host.Label); label != "" {
		return label
	}
	if host.Hostname != "" {
		return host.Hostname
	}
	return string(host.ID)
}

// hostDetail is the second line that distinguishes rows: the account and
// endpoint, plus the protocol when it is not plain SSH.
func hostDetail(host domainvault.Host) string {
	parts := []string{hostEndpoint(host)}
	if protocol := protocolOf(host); protocol != domainvault.ProtocolSSH {
		parts = append(parts, string(protocol))
	}
	if host.Proxy != nil || host.ProxyProfileID != "" {
		parts = append(parts, "via proxy")
	}
	if host.HostChain != nil && len(host.HostChain.HostIDs) > 0 {
		parts = append(parts, fmt.Sprintf("via %d jump Host(s)", len(host.HostChain.HostIDs)))
	}
	if host.NetworkProfileID != "" {
		parts = append(parts, "network profile")
	}
	return strings.Join(parts, " · ")
}

func hostEndpoint(host domainvault.Host) string {
	endpoint := host.Hostname
	if host.Port != 0 {
		endpoint = fmt.Sprintf("%s:%d", host.Hostname, host.Port)
	}
	if host.Username != "" {
		return host.Username + "@" + endpoint
	}
	return endpoint
}

// protocolOf reports the protocol a legacy record stored without one would
// have received at normalization: SSH.
func protocolOf(host domainvault.Host) domainvault.Protocol {
	if host.Protocol == "" {
		return domainvault.ProtocolSSH
	}
	return host.Protocol
}
