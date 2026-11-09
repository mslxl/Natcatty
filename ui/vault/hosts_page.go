package vault

import (
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/domain/model"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// hostCardMinWidth is the narrowest a Host or group card gets before the grid
// drops a column.
const hostCardMinWidth float32 = 236

// gridGap is the room between cards in a grid.
const gridGap float32 = 10

// rowHeight is the height of a Host or group row in the list and tree views.
const rowHeight float32 = 44

// treeIndent is how far one level of the group tree indents its rows.
const treeIndent float32 = 18

// treeToggleSize is the side of a tree group's chevron. A row without one, a
// Host or an empty group, keeps its room so the avatars line up by level.
const treeToggleSize float32 = 20

// recentHostLimit is how many Recently Connected Hosts the root shows, the
// same six as the original.
const recentHostLimit = 6

// browse is the Hosts page's place in the group tree: the group path it
// shows, "" for every Host. It lives with the view, as the original kept the
// chosen group in the Vault's own state.
type browse struct {
	group string
	// dragging is set while a Host card drags; dragged is last frame's value,
	// which shows the Vault root target only while a Host can be dropped there.
	dragging, dragged bool
	// focused is the card the select click behavior is focusing. The next
	// click on it activates.
	focused model.HostID
	// collapsed holds the group paths the tree view collapsed. A path missing
	// from it is expanded, so a fresh Vault shows its contents.
	collapsed map[string]bool
}

// hostsToolbar is the Hosts page's top row, as the original's: the search
// field, the view and sort switches, New Host with its menu of the Vault-wide
// actions, and the local terminal and serial shortcuts.
func hostsToolbar(c *ui.Context, s *vaultstate.State, field func(), opts options, at *browse,
	m locales.Messages, t *ui.Theme) {
	layout := opts.layout()
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Row(c).Grow(1).MinWidth(180).Children(field)
		viewModeToggle(c, opts, layout, m, t)
		sortSelect(c, opts, layout, m, t)
		ui.Row(c).Gap(1).Shrink(0).AlignItems(ui.Center).Children(func() {
			if kit.PrimaryButton(c, icons.Plus, m.VaultHostNew()).Clicked() {
				s.BeginCreate()
				c.Invalidate()
			}
			more := ui.ButtonBase(c).Label(m.VaultMoreActions()).Tooltip(m.VaultMoreActions()).
				Size(28, kit.M(c).Control.Large).Radius(kit.M(c).Radius.Control).Justify(ui.Center).AlignItems(ui.Center).
				Background(t.Accent).TextColor(t.AccentText)
			if more.Hovered() {
				more.Background(t.AccentHover)
			}
			more.Children(func() { kit.IconSized(c, icons.ChevronDown, kit.M(c).Icon.Medium) })
			more.Menu(func(menu *ui.Menu) {
				if menu.Item(m.VaultGroupNew()).Chosen() {
					s.BeginCreateGroup()
					c.Invalidate()
				}
				menu.Separator()
				if menu.Item(m.VaultImportTitle()).Chosen() {
					s.BeginImport()
					c.Invalidate()
				}
				if menu.Item(m.VaultExportTitle()).Chosen() {
					s.BeginExport()
					c.Invalidate()
				}
				if menu.Item(m.VaultBackupOpen()).Chosen() {
					s.BeginBackups()
					c.Invalidate()
				}
			})
		})
		if opts.localTerminal != nil {
			if kit.SecondaryButton(c, icons.SquareTerminal, m.VaultLocalTerminal()).Clicked() {
				opts.localTerminal()
				c.Invalidate()
			}
		}
		if opts.serial != nil {
			if kit.SecondaryButton(c, icons.Cable, m.VaultSerial()).Clicked() {
				opts.serial()
				c.Invalidate()
			}
		}
	})
}

// viewOption is one entry of the view switch.
type viewOption struct {
	mode  domainpreferences.HostViewMode
	icon  string
	label string
}

func viewOptions(m locales.Messages) []viewOption {
	return []viewOption{
		{domainpreferences.HostViewGrid, icons.LayoutGrid, m.VaultViewGrid()},
		{domainpreferences.HostViewList, icons.List, m.VaultViewList()},
		{domainpreferences.HostViewTree, icons.FolderTree, m.VaultViewTree()},
	}
}

// viewModeToggle is the original's grid/list/tree switch.
func viewModeToggle(c *ui.Context, opts options, layout domainpreferences.Layout, m locales.Messages, t *ui.Theme) {
	options := viewOptions(m)
	segments := make([]kit.Segment, len(options))
	chosen := -1
	for i, option := range options {
		segments[i] = kit.Segment{Key: "vault-view-" + string(option.mode), Label: option.label, Icon: option.icon}
		if option.mode == layout.HostViewMode {
			chosen = i
		}
	}
	if kit.Segmented(c, kit.SegmentedIcons, &chosen, "", segments) {
		mode := options[chosen].mode
		opts.setLayout(func(next *domainpreferences.Layout) { next.HostViewMode = mode })
		c.Invalidate()
	}
}

// sortOption is one entry of the sort menu.
type sortOption struct {
	mode  domainpreferences.HostSortMode
	label string
}

func sortOptions(m locales.Messages) []sortOption {
	return []sortOption{
		{domainpreferences.HostSortManual, m.VaultSortManual()},
		{domainpreferences.HostSortAZ, m.VaultSortAZ()},
		{domainpreferences.HostSortZA, m.VaultSortZA()},
		{domainpreferences.HostSortNewest, m.VaultSortNewest()},
		{domainpreferences.HostSortOldest, m.VaultSortOldest()},
		{domainpreferences.HostSortGroup, m.VaultSortGroup()},
		{domainpreferences.HostSortIP, m.VaultSortIP()},
	}
}

// sortSelect is the original's Host sort menu.
func sortSelect(c *ui.Context, opts options, layout domainpreferences.Layout, m locales.Messages, t *ui.Theme) {
	options := sortOptions(m)
	labels := make([]string, 0, len(options))
	current := options[0].label
	for _, option := range options {
		labels = append(labels, option.label)
		if option.mode == layout.HostSortMode {
			current = option.label
		}
	}
	chosen := current
	if ui.Select(c, &chosen, labels).Width(128).Tooltip(m.VaultSortTitle()).Changed() {
		for _, option := range options {
			if option.label != chosen {
				continue
			}
			mode := option.mode
			opts.setLayout(func(next *domainpreferences.Layout) { next.HostSortMode = mode })
			c.Invalidate()
		}
	}
}

// hostsPage draws the Vault's Hosts page in the mode the user chose: cards,
// rows, or the whole group tree.
func hostsPage(c *ui.Context, s *vaultstate.State, opts options, at *browse, m locales.Messages, t *ui.Theme) {
	layout := opts.layout()
	sections, ungrouped := groupSections(s)
	// A group renamed or deleted elsewhere takes the page back to the top.
	if at.group != "" && !groupShown(sections, at.group) {
		at.group = ""
	}

	if layout.HostViewMode == domainpreferences.HostViewTree {
		hostTree(c, s, opts, at, m, t, layout, sections, ungrouped)
		return
	}
	if at.group != "" {
		browsedGroup(c, s, opts, at, m, t, layout, sections)
		return
	}
	browsedRoot(c, s, opts, at, m, t, layout, sections, ungrouped)
}

// browsedRoot draws the Vault root: the Pinned and Recently Connected
// sections the original lifts out of the list, the root groups, and every
// Host the root lists.
func browsedRoot(c *ui.Context, s *vaultstate.State, opts options, at *browse, m locales.Messages, t *ui.Theme,
	layout domainpreferences.Layout, sections []hostGroupSection, ungrouped []domainvault.Host) {
	groups := sortSections(childGroups(sections, ""), layout.HostSortMode)
	rootHosts := s.EffectiveItems()
	if layout.ShowOnlyUngroupedHostsInRoot {
		rootHosts = ungrouped
	}
	pinned := pinnedHosts(s)
	recent := recentHosts(s, layout)
	hidden := make(map[model.HostID]bool, len(pinned)+len(recent))
	for _, host := range pinned {
		hidden[host.ID] = true
	}
	if layout.ShowRecentHosts {
		for _, host := range recent {
			hidden[host.ID] = true
		}
	}
	visible := make([]domainvault.Host, 0, len(rootHosts))
	for _, host := range rootHosts {
		if !hidden[host.ID] {
			visible = append(visible, host)
		}
	}
	visible = sortHosts(visible, layout.HostSortMode)

	dragged := at.dragging
	if dragged != at.dragged {
		c.Invalidate()
	}
	at.dragged, at.dragging = dragged, false
	if dragged {
		// While a Host drags, the Vault root is a drop target, so a Host can
		// always leave its group.
		rootDropTarget(c, s, m, t)
	}

	if len(pinned) > 0 {
		sectionHeading(c, t, m.VaultHostsPinned())
		renderHosts(c, s, opts, pinned, at, m, t, layout, 0, "pinned-hosts")
	}
	if len(recent) > 0 {
		sectionHeading(c, t, m.VaultHostsRecent())
		renderHosts(c, s, opts, recent, at, m, t, layout, 0, "recent-hosts")
	}
	if len(groups) > 0 {
		sectionHeading(c, t, m.VaultGroupListTitle(len(groups)))
		renderGroups(c, s, groups, at, m, t, layout)
	}
	// An empty Host list is only worth a section when the page has nothing
	// else; the original hides it behind Pinned, Recent, and the groups.
	if len(visible) > 0 || (len(groups) == 0 && len(pinned) == 0 && len(recent) == 0) {
		sectionHeading(c, t, m.VaultHostListTitle(len(visible)))
		if len(visible) == 0 {
			kit.EmptyHint(c, m.VaultHostEmpty())
		} else {
			renderHosts(c, s, opts, visible, at, m, t, layout, 0, "hosts")
		}
	}
}

// browsedGroup draws one group: its subgroups as cards or rows, then the
// Hosts that belong to it directly. A subgroup's Hosts are not repeated here;
// the original shows one level at a time.
func browsedGroup(c *ui.Context, s *vaultstate.State, opts options, at *browse, m locales.Messages, t *ui.Theme,
	layout domainpreferences.Layout, sections []hostGroupSection) {
	breadcrumb(c, s, at, m, t)
	groups := sortSections(childGroups(sections, at.group), layout.HostSortMode)
	if len(groups) > 0 {
		sectionHeading(c, t, m.VaultGroupListTitle(len(groups)))
		renderGroups(c, s, groups, at, m, t, layout)
	}
	hosts := sortHosts(directHosts(sections, at.group), layout.HostSortMode)
	sectionHeading(c, t, m.VaultHostListTitle(len(hosts)))
	if len(hosts) == 0 {
		kit.EmptyHint(c, m.VaultHostEmpty())
		return
	}
	renderHosts(c, s, opts, hosts, at, m, t, layout, 0, "group-hosts")
}

// renderGroups draws a group list in the current view mode.
func renderGroups(c *ui.Context, s *vaultstate.State, groups []hostGroupSection, at *browse,
	m locales.Messages, t *ui.Theme, layout domainpreferences.Layout) {
	if layout.HostViewMode == domainpreferences.HostViewList || layout.HostViewMode == domainpreferences.HostViewTree {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			for _, section := range groups {
				groupRow(c, s, section, at, m, t, 0)
			}
		})
		return
	}
	cardGrid(c, "groups", len(groups), func(i int) { groupCard(c, s, groups[i], at, m, t) })
}

// renderHosts draws a Host list in the current view mode. The key names the
// section, so the Pinned, Recent, and main grids are distinct elements.
func renderHosts(c *ui.Context, s *vaultstate.State, opts options, hosts []domainvault.Host, at *browse,
	m locales.Messages, t *ui.Theme, layout domainpreferences.Layout, depth int, key string) {
	if layout.HostViewMode == domainpreferences.HostViewList || layout.HostViewMode == domainpreferences.HostViewTree {
		ui.Column(c).FillWidth().Gap(2).Children(func() {
			for _, host := range hosts {
				hostRow(c, s, opts, host, at, m, t, layout, depth)
			}
		})
		return
	}
	cardGrid(c, key, len(hosts), func(i int) { hostCard(c, s, opts, hosts[i], at, m, t, layout) })
}

// hostTree draws every group and ungrouped Host in one indented tree, which is
// the original's third view mode. The browsed group does not narrow it.
func hostTree(c *ui.Context, s *vaultstate.State, opts options, at *browse, m locales.Messages, t *ui.Theme,
	layout domainpreferences.Layout, sections []hostGroupSection, ungrouped []domainvault.Host) {
	hosts := s.EffectiveItems()
	sectionHeading(c, t, m.VaultHostListTitle(len(hosts)))
	if len(hosts) == 0 {
		kit.EmptyHint(c, m.VaultHostEmpty())
		return
	}
	ui.Column(c).FillWidth().Gap(1).Children(func() {
		for _, node := range sortTree(groupForest(sections), layout.HostSortMode) {
			treeGroup(c, s, opts, at, m, t, layout, node, 0)
		}
		for _, host := range sortHosts(ungrouped, layout.HostSortMode) {
			hostRow(c, s, opts, host, at, m, t, layout, 0)
		}
	})
}

// groupTreeNode is one level of the tree view.
type groupTreeNode struct {
	section  hostGroupSection
	children []*groupTreeNode
}

// groupForest materializes the whole group tree, adding the intermediate
// levels a Host path implies even when no record names them.
func groupForest(sections []hostGroupSection) []*groupTreeNode {
	nodes := make(map[string]*groupTreeNode, len(sections))
	var roots []*groupTreeNode
	var ensure func(path string) *groupTreeNode
	ensure = func(path string) *groupTreeNode {
		if node, ok := nodes[path]; ok {
			return node
		}
		node := &groupTreeNode{section: hostGroupSection{path: path, depth: groupDepth(path)}}
		nodes[path] = node
		if at := sectionIndex(sections, path); at >= 0 {
			node.section = sections[at]
		}
		if parent := parentGroup(path); parent != "" {
			up := ensure(parent)
			up.children = append(up.children, node)
		} else {
			roots = append(roots, node)
		}
		return node
	}
	for _, section := range sections {
		ensure(section.path)
	}
	return roots
}

// sortTree orders sibling groups by the active sort. Manual keeps the Vault's
// own group order; every other mode sorts by name, as the original's tree.
func sortTree(nodes []*groupTreeNode, mode domainpreferences.HostSortMode) []*groupTreeNode {
	if mode != domainpreferences.HostSortManual {
		sort.SliceStable(nodes, func(left, right int) bool {
			return strings.ToLower(groupName(nodes[left].section.path)) <
				strings.ToLower(groupName(nodes[right].section.path))
		})
	}
	for _, node := range nodes {
		sortTree(node.children, mode)
	}
	return nodes
}

// treeGroup draws one group of the tree and, when expanded, its Hosts and
// child groups.
func treeGroup(c *ui.Context, s *vaultstate.State, opts options, at *browse, m locales.Messages, t *ui.Theme,
	layout domainpreferences.Layout, node *groupTreeNode, depth int) {
	path := node.section.path
	expanded := at.collapsed == nil || !at.collapsed[path]
	toggle := func() {
		if at.collapsed == nil {
			at.collapsed = map[string]bool{}
		}
		at.collapsed[path] = expanded
		c.Invalidate()
	}
	row := groupRowElement(c, t, path, depth)
	hostDropTarget(c, s, row, path, t)
	// A group row expands or collapses, as the original's tree does. Its
	// chevron and menu keep their own hit areas.
	if row.Clicked() {
		toggle()
	}
	if node.section.stored {
		section := node.section
		row.ContextMenu(func(menu *ui.Menu) {
			if menu.Item(m.VaultHostEdit()).Chosen() {
				s.BeginEditGroup(section.group.Path)
				c.Invalidate()
			}
			if menu.Item(m.VaultHostDelete()).Chosen() {
				s.RequestDeleteGroup(section.group.Path)
				c.Invalidate()
			}
		})
	}
	row.Children(func() {
		if len(node.children) == 0 && len(node.section.hosts) == 0 {
			ui.Box(c).Size(treeToggleSize, treeToggleSize).Shrink(0)
			groupRowBody(c, s, node.section, at, m, t)
			return
		}
		chevron := ui.ButtonBase(c).Key("tree-toggle-"+path).Label(m.VaultTreeToggle()).
			Size(treeToggleSize, treeToggleSize).Radius(kit.M(c).Radius.Control).Justify(ui.Center).AlignItems(ui.Center).
			TextColor(t.TextMuted).Shrink(0)
		if chevron.Hovered() {
			chevron.Background(kit.Subtle(t)).TextColor(t.Text)
		}
		if chevron.Clicked() {
			toggle()
		}
		icon := icons.ChevronRight
		if expanded {
			icon = icons.ChevronDown
		}
		chevron.Children(func() { kit.IconSized(c, icon, kit.M(c).Icon.Medium) })
		groupRowBody(c, s, node.section, at, m, t)
	})
	if !expanded {
		return
	}
	for _, child := range node.children {
		treeGroup(c, s, opts, at, m, t, layout, child, depth+1)
	}
	for _, host := range sortHosts(node.section.hosts, layout.HostSortMode) {
		hostRow(c, s, opts, host, at, m, t, layout, depth+1)
	}
}

// groupRowElement is a group's row shell: indented, hoverable, and a drop
// target for a dragged Host.
func groupRowElement(c *ui.Context, t *ui.Theme, path string, depth int) ui.Element {
	row := ui.Row(c).Key("group-row-" + path).FillWidth().Height(rowHeight).
		PaddingStart(8 + float32(depth)*treeIndent).PaddingEnd(8).
		Radius(kit.M(c).Radius.Control).AlignItems(ui.Center).Gap(8)
	if row.Hovered() {
		row.Background(kit.Subtle(t))
	}
	return row
}

// groupRow is one Host Group in the list and tree views: open it with a
// click, drop a Host on it to move the Host in. Its menu edits or deletes a
// saved group.
func groupRow(c *ui.Context, s *vaultstate.State, section hostGroupSection, at *browse,
	m locales.Messages, t *ui.Theme, depth int) {
	row := groupRowElement(c, t, section.path, depth)
	open := func() {
		at.group = section.path
		c.Invalidate()
	}
	actions := groupActions(s, section, open, m, c)
	if row.Clicked() {
		open()
	}
	row.ContextMenu(actions)
	hostDropTarget(c, s, row, section.path, t)
	row.Children(func() {
		groupRowBody(c, s, section, at, m, t)
	})
}

// hostDropTarget makes a group row accept a dragged Host. The card, list row,
// and tree row all accept the same drop.
func hostDropTarget(c *ui.Context, s *vaultstate.State, row ui.Element, path string, t *ui.Theme) {
	if _, over := ui.DragOver[vaultstate.DragItem](row); over {
		row.Border(1, t.Accent).Background(t.Accent.Alpha(0.08))
	}
	if drag, dropped := ui.Drop[vaultstate.DragItem](row); dropped && drag.HostID != "" {
		s.MoveHostToGroup(drag.HostID, path)
		c.Invalidate()
	}
}

// groupRowBody draws a group's icon, name, notes indicator, and member count.
func groupRowBody(c *ui.Context, s *vaultstate.State, section hostGroupSection, at *browse,
	m locales.Messages, t *ui.Theme) {
	kit.HostAvatar(c, "", kit.AvatarGroup, section.path, 28)
	ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, groupName(section.path)).FontSize(kit.M(c).Font.Body).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
			groupNotes(c, section, m, t)
		})
		ui.Text(c, m.VaultGroupMemberCount(s.GroupMemberCount(section.path))).
			FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine()
	})
	kit.IconButtonWith(c, icons.EllipsisVertical, m.VaultItemActions(groupName(section.path)), kit.IconOptions{Role: kit.IconCard, Key: "menu"}).
		Menu(groupActions(s, section, func() {
			at.group = section.path
			c.Invalidate()
		}, m, c))
}

// groupNotes shows a saved group's notes indicator, as the original marks a
// group that carries a note.
func groupNotes(c *ui.Context, section hostGroupSection, m locales.Messages, t *ui.Theme) {
	if !section.stored || strings.TrimSpace(section.group.Notes) == "" {
		return
	}
	kit.IconSized(c, icons.FileText, kit.M(c).Icon.Small).TextColor(t.TextMuted).Shrink(0).
		Tooltip(m.VaultGroupNotes())
}

// groupActions builds a group's context menu.
func groupActions(s *vaultstate.State, section hostGroupSection, open func(), m locales.Messages, c *ui.Context) func(*ui.Menu) {
	return func(menu *ui.Menu) {
		if menu.Item(m.VaultGroupOpen()).Chosen() {
			open()
		}
		if !section.stored {
			return
		}
		menu.Separator()
		if menu.Item(m.VaultHostEdit()).Chosen() {
			s.BeginEditGroup(section.group.Path)
			c.Invalidate()
		}
		if menu.Item(m.VaultHostDelete()).Chosen() {
			s.RequestDeleteGroup(section.group.Path)
			c.Invalidate()
		}
	}
}

// groupName returns the last path segment of a group.
func groupName(path string) string {
	if at := strings.LastIndex(path, "/"); at >= 0 {
		return path[at+1:]
	}
	return path
}

// pinnedHosts returns the pinned Hosts the root lists above everything else.
func pinnedHosts(s *vaultstate.State) []domainvault.Host {
	var hosts []domainvault.Host
	for _, host := range s.EffectiveItems() {
		if host.Pinned {
			hosts = append(hosts, host)
		}
	}
	sort.SliceStable(hosts, func(left, right int) bool {
		return strings.ToLower(hosts[left].Label) < strings.ToLower(hosts[right].Label)
	})
	return hosts
}

// recentHosts returns the most recently connected Hosts for the root's
// Recently Connected section. A pinned Host keeps its place in the Pinned
// section instead of appearing twice.
func recentHosts(s *vaultstate.State, layout domainpreferences.Layout) []domainvault.Host {
	if !layout.ShowRecentHosts {
		return nil
	}
	var hosts []domainvault.Host
	for _, host := range s.EffectiveItems() {
		if host.Pinned || host.LastConnectedAt == 0 {
			continue
		}
		hosts = append(hosts, host)
	}
	sort.SliceStable(hosts, func(left, right int) bool {
		return hosts[left].LastConnectedAt > hosts[right].LastConnectedAt
	})
	if len(hosts) > recentHostLimit {
		hosts = hosts[:recentHostLimit]
	}
	return hosts
}

// directHosts returns the Hosts that belong to path itself. A subgroup's
// Hosts are deliberately absent: the original shows one level at a time.
func directHosts(sections []hostGroupSection, path string) []domainvault.Host {
	if at := sectionIndex(sections, path); at >= 0 {
		return sections[at].hosts
	}
	return nil
}

// sortHosts orders a Host list. Manual keeps the Vault's own order, which the
// store already returns with pinned Hosts first.
func sortHosts(hosts []domainvault.Host, mode domainpreferences.HostSortMode) []domainvault.Host {
	out := append([]domainvault.Host(nil), hosts...)
	switch mode {
	case domainpreferences.HostSortAZ:
		sort.SliceStable(out, func(left, right int) bool {
			return strings.ToLower(out[left].Label) < strings.ToLower(out[right].Label)
		})
	case domainpreferences.HostSortZA:
		sort.SliceStable(out, func(left, right int) bool {
			return strings.ToLower(out[left].Label) > strings.ToLower(out[right].Label)
		})
	case domainpreferences.HostSortNewest:
		sort.SliceStable(out, func(left, right int) bool {
			return out[left].CreatedAt.After(out[right].CreatedAt)
		})
	case domainpreferences.HostSortOldest:
		sort.SliceStable(out, func(left, right int) bool {
			return out[left].CreatedAt.Before(out[right].CreatedAt)
		})
	case domainpreferences.HostSortGroup:
		sort.SliceStable(out, func(left, right int) bool {
			leftGroup, rightGroup := strings.ToLower(out[left].Group), strings.ToLower(out[right].Group)
			if leftGroup != rightGroup {
				return leftGroup < rightGroup
			}
			return strings.ToLower(out[left].Label) < strings.ToLower(out[right].Label)
		})
	case domainpreferences.HostSortIP:
		sort.SliceStable(out, func(left, right int) bool {
			if order := compareAddresses(out[left].Hostname, out[right].Hostname); order != 0 {
				return order < 0
			}
			return strings.ToLower(out[left].Label) < strings.ToLower(out[right].Label)
		})
	}
	return out
}

// compareAddresses orders two Host addresses numerically when both are IP
// literals, then alphabetically, as the original's IP sort.
func compareAddresses(left, right string) int {
	leftIP, rightIP := net.ParseIP(strings.TrimSpace(left)), net.ParseIP(strings.TrimSpace(right))
	switch {
	case leftIP != nil && rightIP != nil:
		return strings.Compare(leftIP.To16().String(), rightIP.To16().String())
	case leftIP != nil:
		return -1
	case rightIP != nil:
		return 1
	}
	return strings.Compare(strings.ToLower(left), strings.ToLower(right))
}

// sortSections orders group cards. Manual keeps the Vault's group order;
// every other mode sorts by name.
func sortSections(sections []hostGroupSection, mode domainpreferences.HostSortMode) []hostGroupSection {
	if mode == domainpreferences.HostSortManual {
		return sections
	}
	out := append([]hostGroupSection(nil), sections...)
	sort.SliceStable(out, func(left, right int) bool {
		return strings.ToLower(groupName(out[left].path)) < strings.ToLower(groupName(out[right].path))
	})
	return out
}

// childGroups is the groups one level below parent, in the Vault's order. A
// level no record or Host names directly, as "prod" for a Host in
// "prod/web", is still listed so every group can be reached.
func childGroups(sections []hostGroupSection, parent string) []hostGroupSection {
	var groups []hostGroupSection
	seen := map[string]bool{}
	for _, section := range sections {
		path := section.path
		for path != "" && parentGroup(path) != parent {
			path = parentGroup(path)
		}
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if at := sectionIndex(sections, path); at >= 0 {
			groups = append(groups, sections[at])
			continue
		}
		groups = append(groups, hostGroupSection{path: path, depth: groupDepth(path)})
	}
	return groups
}

// groupShown reports whether path is a group or lies above one.
func groupShown(sections []hostGroupSection, path string) bool {
	for _, section := range sections {
		if domainvault.IsGroupAtOrBelow(section.path, path) {
			return true
		}
	}
	return false
}

// parentGroup is the group path one level above path, "" for a root group.
func parentGroup(path string) string {
	path = domainvault.NormalizeGroup(path)
	if at := strings.LastIndex(path, "/"); at >= 0 {
		return path[:at]
	}
	return ""
}

// breadcrumb leads from every Host to the browsed group. Each ancestor is a
// link, and the row takes a dropped Host out of every group.
func breadcrumb(c *ui.Context, s *vaultstate.State, at *browse, m locales.Messages, t *ui.Theme) {
	parts := strings.Split(at.group, "/")
	path := append([]string{m.VaultAllHosts()}, parts...)
	chosen := len(path) - 1
	row := ui.Row(c).FillWidth().Padding(4).Radius(kit.M(c).Radius.Control).Border(1, ui.Transparent)
	if _, over := ui.DragOver[vaultstate.DragItem](row); over {
		row.Border(1, t.Accent)
	}
	if drag, dropped := ui.Drop[vaultstate.DragItem](row); dropped && drag.HostID != "" {
		s.MoveHostToGroup(drag.HostID, "")
		c.Invalidate()
	}
	row.Children(func() {
		if ui.Breadcrumbs(c, path, &chosen).Label(m.VaultGroupListTitle(len(parts))).Changed() {
			at.group = strings.Join(parts[:chosen], "/")
			c.Invalidate()
		}
	})
}

// sectionHeading names a grid of cards or rows.
func sectionHeading(c *ui.Context, t *ui.Theme, title string) {
	kit.SectionBar(c, title, nil)
}

// cardGrid lays count cards out in as many equal columns as fit at
// hostCardMinWidth, measured from the grid's width in the previous frame.
// The key keeps the grid, and so its cards, the same element when a row
// appears above it, as the root drop target does once a drag starts.
func cardGrid(c *ui.Context, key string, count int, card func(int)) {
	if count == 0 {
		return
	}
	grid := ui.Grid(c).Key(key).FillWidth().Gap(gridGap)
	width := grid.Bounds().W
	if width <= 0 {
		// The first frame has no width yet; size from the window and settle
		// on the next frame.
		window, _ := c.Size()
		width = window - 320
		c.Invalidate()
	}
	columns := int((width + gridGap) / (hostCardMinWidth + gridGap))
	columns = max(1, min(columns, 6))
	grid.Columns(columns).Children(func() {
		for i := 0; i < count; i++ {
			card(i)
		}
	})
}

// cardShell is a Host or group card's rounded, bordered box. It lifts while
// hovered or dragged over. The key comes first, before any state is read, so
// the card stays the same element when the root drop target appears above it.
func cardShell(c *ui.Context, t *ui.Theme, key string) ui.Element {
	card := ui.Row(c).Key(key).MinWidth(0).Height(64).Padding(0, 8, 0, 12).Gap(12).Radius(kit.M(c).Radius.Card).
		Border(1, t.Border).Background(t.Surface).AlignItems(ui.Center).
		Shadow(0, 1, 2, 0, kit.CardShadow(t))
	if card.Hovered() {
		card.Background(kit.Muted(t)).Border(1, t.Border.Mix(t.Text, 0.15))
	}
	return card
}

// groupCard is one Host Group card: click its name to open it, drop a Host on
// it to move the Host in. Its menu edits or deletes a saved group.
func groupCard(c *ui.Context, s *vaultstate.State, section hostGroupSection, at *browse,
	m locales.Messages, t *ui.Theme) {
	card := cardShell(c, t, "group-"+section.path)
	if _, over := ui.DragOver[vaultstate.DragItem](card); over {
		card.Border(1, t.Accent).Background(t.Accent.Alpha(0.08))
	}
	if drag, dropped := ui.Drop[vaultstate.DragItem](card); dropped && drag.HostID != "" {
		s.MoveHostToGroup(drag.HostID, section.path)
		c.Invalidate()
	}
	open := func() {
		at.group = section.path
		c.Invalidate()
	}
	if card.Clicked() {
		open()
	}
	actions := groupActions(s, section, open, m, c)
	card.ContextMenu(actions)
	card.Children(func() {
		kit.HostAvatar(c, "", kit.AvatarGroup, section.path, 40)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, groupName(section.path)).FontSize(kit.M(c).Font.Body).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
				groupNotes(c, section, m, t)
			})
			ui.Text(c, m.VaultGroupMemberCount(s.GroupMemberCount(section.path))).
				FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine()
		})
		kit.IconButtonWith(c, icons.EllipsisVertical, m.VaultItemActions(groupName(section.path)), kit.IconOptions{Role: kit.IconCard, Key: "menu"}).Menu(actions)
	})
}

// hostCard is one Host card, as the original's: its avatar, label, and
// address. A click connects; the pencil, shown while hovered, edits; the
// menu, also on a right-click, holds every other action. The card drags the
// Host to a group.
func hostCard(c *ui.Context, s *vaultstate.State, opts options, host domainvault.Host, at *browse,
	m locales.Messages, t *ui.Theme, layout domainpreferences.Layout) {
	card := cardShell(c, t, "host-"+string(host.ID)).Tooltip(strings.Join(hostSummary(s, host, m), "  ·  "))
	card.Drag(vaultstate.HostDrag(host.ID))
	hovered := card.Hovered()
	if card.Dragging() {
		at.dragging = true
		card.Opacity(0.5)
		if !at.dragged {
			// The page drew this frame without the root target; draw it now.
			c.Invalidate()
		}
	}
	activateHost(c, s, host, at, layout, card)
	if host.Pinned {
		card.Border(1, t.Accent.Alpha(0.4))
	}
	card.ContextMenu(hostActions(s, opts, host, m, c))
	card.Children(func() {
		kit.HostAvatar(c, "", hostAvatarKind(host.Protocol), host.Label, 40)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, host.Label).FontSize(kit.M(c).Font.Body).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
				if host.Pinned {
					kit.IconSized(c, icons.Pin, kit.M(c).Icon.Small).TextColor(t.Accent).Shrink(0)
				}
			})
			hostCardDetail(c, s, host, m, t)
		})
		if hovered && kit.IconButtonWith(c, icons.Pencil, m.VaultHostEdit(), kit.IconOptions{Role: kit.IconCard, Key: "edit"}).Clicked() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
		kit.IconButtonWith(c, icons.EllipsisVertical, m.VaultItemActions(host.Label), kit.IconOptions{Role: kit.IconCard, Key: "menu"}).Menu(hostActions(s, opts, host, m, c))
	})
}

// hostRow is one Host in the list and tree views.
func hostRow(c *ui.Context, s *vaultstate.State, opts options, host domainvault.Host, at *browse,
	m locales.Messages, t *ui.Theme, layout domainpreferences.Layout, depth int) {
	row := ui.Row(c).Key("host-row-" + string(host.ID)).FillWidth().Height(rowHeight).
		PaddingStart(8 + float32(depth)*treeIndent).PaddingEnd(8).
		Radius(kit.M(c).Radius.Control).AlignItems(ui.Center).Gap(8)
	if row.Hovered() {
		row.Background(kit.Subtle(t))
	}
	if at.focused == host.ID && layout.HostClickBehavior == domainpreferences.HostClickSelect {
		row.Border(1, t.Accent)
	}
	row.Drag(vaultstate.HostDrag(host.ID))
	if row.Dragging() {
		at.dragging = true
	}
	activateHost(c, s, host, at, layout, row)
	row.ContextMenu(hostActions(s, opts, host, m, c))
	row.Children(func() {
		if layout.HostViewMode == domainpreferences.HostViewTree {
			ui.Box(c).Size(treeToggleSize, treeToggleSize).Shrink(0)
		}
		kit.HostAvatar(c, "", hostAvatarKind(host.Protocol), host.Label, 28)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, host.Label).FontSize(kit.M(c).Font.Body).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
				if host.Pinned {
					kit.IconSized(c, icons.Pin, kit.M(c).Icon.Small).TextColor(t.Accent).Shrink(0)
				}
			})
			ui.Text(c, hostAddress(host, m)).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).SingleLine().MinWidth(0)
		})
		if row.Hovered() && kit.IconButtonWith(c, icons.Pencil, m.VaultHostEdit(), kit.IconOptions{Role: kit.IconCard, Key: "edit"}).Clicked() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
		kit.IconButtonWith(c, icons.EllipsisVertical, m.VaultItemActions(host.Label), kit.IconOptions{Role: kit.IconCard, Key: "menu"}).Menu(hostActions(s, opts, host, m, c))
	})
}

// activateHost applies the click behavior to one Host card or row. Connect is
// the original's default; Select focuses the first click and activates the
// next one on the same Host.
func activateHost(c *ui.Context, s *vaultstate.State, host domainvault.Host, at *browse,
	layout domainpreferences.Layout, element ui.Element) bool {
	if !element.Clicked() && !element.DoubleClicked() {
		return false
	}
	if layout.HostClickBehavior == domainpreferences.HostClickSelect && element.Clicked() && !element.DoubleClicked() {
		if at.focused != host.ID {
			at.focused = host.ID
			c.Invalidate()
			return false
		}
	}
	at.focused = host.ID
	s.Connect(host.ID)
	c.Invalidate()
	return true
}

// hostActions builds a Host's context menu.
func hostActions(s *vaultstate.State, opts options, host domainvault.Host, m locales.Messages, c *ui.Context) func(*ui.Menu) {
	pin := m.VaultHostPin()
	if host.Pinned {
		pin = m.VaultHostUnpin()
	}
	testLabel := m.VaultHostTest()
	if s.Testing(host.ID) {
		testLabel = m.VaultHostTesting()
	}
	return func(menu *ui.Menu) {
		if menu.Item(m.VaultHostConnect()).Chosen() {
			s.Connect(host.ID)
			c.Invalidate()
		}
		if opts.openSFTP != nil && host.Protocol.UsesSSHAuth() &&
			(opts.sftpEnabled == nil || opts.sftpEnabled()) {
			if menu.Item(m.VaultHostOpenSftp()).Chosen() {
				opts.openSFTP(host)
			}
		}
		if opts.openBrowser != nil && host.Protocol == domainvault.ProtocolSSH {
			if menu.Item(m.VaultHostOpenBrowser()).Chosen() {
				opts.openBrowser(host)
			}
		}
		if menu.Item(testLabel).Disabled(s.Testing(host.ID)).Chosen() {
			s.Test(host.ID)
			c.Invalidate()
		}
		menu.Separator()
		if menu.Item(m.VaultHostEdit()).Chosen() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
		if menu.Item(m.VaultHostDuplicate()).Chosen() {
			s.BeginDuplicate(host.ID, m.SessionMenuCopy())
			c.Invalidate()
		}
		if host.Hostname != "" && menu.Item(m.VaultHostCopyHostname()).Chosen() {
			opts.copyText(host.Hostname)
		}
		if menu.Item(m.VaultHostCopyCredentials()).Chosen() {
			if text, ok := s.CopyCredentials(host.ID); ok {
				opts.copyText(text)
			}
			c.Invalidate()
		}
		menu.Separator()
		if menu.Item(pin).Chosen() {
			_ = s.TogglePinned(host.ID)
			c.Invalidate()
		}
		menu.Separator()
		if menu.Item(m.VaultHostDelete()).Chosen() {
			s.RequestDelete(host.ID)
			c.Invalidate()
		}
	}
}

// hostCardDetail is the line under a Host's label: the outcome of a connection
// test while there is one, else where the Host connects.
func hostCardDetail(c *ui.Context, s *vaultstate.State, host domainvault.Host, m locales.Messages, t *ui.Theme) {
	line := func(text string, color ui.Color) {
		ui.Text(c, text).FontSize(kit.M(c).Font.Footnote).TextColor(color).SingleLine().MinWidth(0)
	}
	switch {
	case s.Testing(host.ID):
		line(m.VaultHostTesting(), t.TextMuted)
	case s.TestProblemError(host.ID) != nil:
		line(uiconnection.ProblemTextFor(m, s.TestProblemError(host.ID)), uitheme.DangerText(t))
	case s.TestProblem(host.ID) != "":
		line(uiconnection.ProblemText(m, s.TestProblem(host.ID)), uitheme.DangerText(t))
	case s.TestSucceeded(host.ID):
		line(m.VaultHostTestSuccess(), t.Success)
	default:
		line(hostAddress(host, m), t.TextMuted)
	}
}

// hostAddress is the Host's user@host, as the original's card shows it, or
// the local terminal's name.
func hostAddress(host domainvault.Host, m locales.Messages) string {
	if host.Protocol == domainvault.ProtocolLocal {
		return m.VaultHostLocal()
	}
	address := host.Hostname
	if host.Port != 0 && host.Port != 22 {
		address = net.JoinHostPort(host.Hostname, strconv.Itoa(host.Port))
	}
	if host.Username != "" {
		return host.Username + "@" + address
	}
	return address
}
