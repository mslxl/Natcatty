package vault

import (
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

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

// browse is the Hosts page's place in the group tree: the group path it
// shows, "" for every Host. It lives with the view, as the original kept the
// chosen group in the Vault's own state.
type browse struct {
	group string
	// dragging is set while a Host card drags; dragged is last frame's value,
	// which shows the Vault root target only while a Host can be dropped there.
	dragging, dragged bool
}

// hostsToolbar is the Hosts page's top row, as the original's: the search
// field, New Host with its menu of the Vault-wide actions, and the local
// terminal and serial shortcuts.
func hostsToolbar(c *ui.Context, s *vaultstate.State, field func(), opts options, at *browse,
	m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		ui.Row(c).Grow(1).MinWidth(220).Children(field)
		ui.Row(c).Gap(1).Shrink(0).AlignItems(ui.Center).Children(func() {
			if kit.PrimaryButton(c, icons.Plus, m.VaultHostNew()).Clicked() {
				s.BeginCreate()
				c.Invalidate()
			}
			more := ui.ButtonBase(c).Label(m.VaultMoreActions()).Tooltip(m.VaultMoreActions()).
				Size(28, kit.ControlHeight).Radius(kit.RadiusControl).Justify(ui.Center).AlignItems(ui.Center).
				Background(t.Accent).TextColor(t.AccentText)
			if more.Hovered() {
				more.Background(t.AccentHover)
			}
			more.Children(func() { ui.Icon(c, icons.Lucide(icons.ChevronDown)).Size(14, 14) })
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

// hostsPage draws the Host Groups and Hosts at the browsed group: a
// breadcrumb back to every Host, the group's subgroups as cards, and its
// Hosts as cards. At the top it lists the root groups and every Host.
func hostsPage(c *ui.Context, s *vaultstate.State, at *browse, m locales.Messages, t *ui.Theme) {
	sections, _ := groupSections(s)
	// A group renamed or deleted elsewhere takes the page back to the top.
	if at.group != "" && !groupShown(sections, at.group) {
		at.group = ""
	}
	if at.group != "" {
		breadcrumb(c, s, at, m, t)
	}

	groups := childGroups(sections, at.group)
	// As the original's, a group lists the Hosts in it and in every group
	// below it, and the top lists every Host.
	var hosts []domainvault.Host
	for _, host := range s.EffectiveItems() {
		if at.group == "" || domainvault.IsGroupAtOrBelow(host.Group, at.group) {
			hosts = append(hosts, host)
		}
	}

	if len(groups) > 0 || at.group == "" {
		sectionHeading(c, t, m.VaultGroupListTitle(len(groups)))
		if len(groups) == 0 {
			ui.Text(c, m.VaultGroupEmpty()).FontSize(12).TextColor(t.TextMuted).Padding(0, 4)
		}
		cardGrid(c, "groups", len(groups), func(i int) { groupCard(c, s, groups[i], at, m, t) })
	}
	dragged := at.dragging
	if dragged != at.dragged {
		c.Invalidate()
	}
	at.dragged, at.dragging = dragged, false
	if at.group == "" && dragged {
		// While a Host drags, the Vault root is a drop target, so a Host can
		// always leave its group.
		rootDropTarget(c, s, m, t)
	}

	sectionHeading(c, t, m.VaultHostListTitle(len(hosts)))
	if len(hosts) == 0 {
		ui.Text(c, m.VaultHostEmpty()).FontSize(12).TextColor(t.TextMuted).Padding(0, 4)
	}
	cardGrid(c, "hosts", len(hosts), func(i int) { hostCard(c, s, hosts[i], at, m, t) })
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
	row := ui.Row(c).FillWidth().Padding(4).Radius(kit.RadiusControl).Border(1, ui.Transparent)
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

// sectionHeading names a grid of cards.
func sectionHeading(c *ui.Context, t *ui.Theme, title string) {
	ui.Text(c, title).FontSize(13).FontWeight(600).TextColor(t.TextMuted).Padding(10, 4, 2, 4).SingleLine()
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
	card := ui.Row(c).Key(key).MinWidth(0).Height(64).Padding(0, 8, 0, 12).Gap(12).Radius(kit.RadiusCard).
		Border(1, t.Border).Background(t.Surface).AlignItems(ui.Center).
		Shadow(0, 1, 2, 0, kit.CardShadow(t))
	if card.Hovered() {
		card.Background(kit.Muted(t)).Border(1, t.Border.Mix(t.Text, 0.15))
	}
	return card
}

// groupCard is one Host Group: double-click or click its name to open it, drop
// a Host on it to move the Host in. Its menu edits or deletes a saved group.
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
	actions := func(menu *ui.Menu) {
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
	card.ContextMenu(actions)
	name := section.path
	if at := strings.LastIndex(name, "/"); at >= 0 {
		name = name[at+1:]
	}
	card.Children(func() {
		kit.HostAvatar(c, "", kit.AvatarGroup, section.path, 40)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).FontSize(14).FontWeight(600).SingleLine()
			ui.Text(c, m.VaultGroupMemberCount(s.GroupMemberCount(section.path))).
				FontSize(12).TextColor(t.TextMuted).SingleLine()
		})
		cardControl(c, "menu", icons.EllipsisVertical, m.VaultItemActions(name)).Menu(actions)
	})
}

// hostCard is one Host, as the original's: its avatar, label, and address.
// A double-click connects; the pencil, shown while hovered, edits; the menu,
// also on a right-click, holds every other action. The card drags the Host to
// a group.
func hostCard(c *ui.Context, s *vaultstate.State, host domainvault.Host, at *browse, m locales.Messages, t *ui.Theme) {
	remote := host.Protocol == domainvault.ProtocolSSH ||
		host.Protocol == domainvault.ProtocolMosh ||
		host.Protocol == domainvault.ProtocolET
	pin := m.VaultHostPin()
	if host.Pinned {
		pin = m.VaultHostUnpin()
	}
	testLabel := m.VaultHostTest()
	if s.Testing(host.ID) {
		testLabel = m.VaultHostTesting()
	}
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
	if remote && card.DoubleClicked() {
		s.Connect(host.ID)
		c.Invalidate()
	}
	actions := func(menu *ui.Menu) {
		if remote {
			if menu.Item(m.VaultHostConnect()).Chosen() {
				s.Connect(host.ID)
				c.Invalidate()
			}
			if menu.Item(testLabel).Disabled(s.Testing(host.ID)).Chosen() {
				s.Test(host.ID)
				c.Invalidate()
			}
			menu.Separator()
		}
		if menu.Item(m.VaultHostEdit()).Chosen() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
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
	card.ContextMenu(actions)
	card.Children(func() {
		kit.HostAvatar(c, "", hostAvatarKind(host.Protocol), host.Label, 40)
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, host.Label).FontSize(14).FontWeight(600).SingleLine().MinWidth(0).Shrink(1)
				if host.Pinned {
					ui.Icon(c, icons.Lucide(icons.Pin)).Size(12, 12).TextColor(t.Accent).Shrink(0)
				}
			})
			hostCardDetail(c, s, host, m, t)
		})
		if hovered && cardControl(c, "edit", icons.Pencil, m.VaultHostEdit()).Clicked() {
			s.BeginEdit(host.ID)
			c.Invalidate()
		}
		cardControl(c, "menu", icons.EllipsisVertical, m.VaultItemActions(host.Label)).Menu(actions)
	})
}

// hostCardDetail is the line under a Host's label: the outcome of a connection
// test while there is one, else where the Host connects.
func hostCardDetail(c *ui.Context, s *vaultstate.State, host domainvault.Host, m locales.Messages, t *ui.Theme) {
	line := func(text string, color ui.Color) {
		ui.Text(c, text).FontSize(12).TextColor(color).SingleLine().MinWidth(0)
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

// cardControl is a small icon button inside a card. The key keeps it the same
// element when a control before it shows only while the card is hovered.
func cardControl(c *ui.Context, key, icon, label string) ui.Element {
	t := c.Theme()
	button := ui.ButtonBase(c).Key(key).Label(label).Tooltip(label).Size(28, 28).Radius(kit.RadiusControl).
		Justify(ui.Center).AlignItems(ui.Center).TextColor(t.TextMuted).Shrink(0)
	switch {
	case button.Pressed():
		button.Background(kit.Strong(t)).TextColor(t.Text)
	case button.Hovered():
		button.Background(kit.Subtle(t)).TextColor(t.Text)
	}
	button.Children(func() { ui.Icon(c, icons.Lucide(icon)).Size(15, 15) })
	return button
}
