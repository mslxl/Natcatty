// Package vault renders the Vault surface: the Host list, the Host editor,
// and the delete confirmation. It resolves localized text; the state package
// holds behavior and never contains user-visible strings.
package vault

import (
	"net"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainbackup "github.com/mslxl/natcatty/domain/vaultbackup"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	uiforwarding "github.com/mslxl/natcatty/ui/forwarding"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// Option customizes the Vault surface.
type Option func(*options)

type options struct {
	forwarding *uiforwarding.State
	search     *uisearch.State
}

// WithForwarding embeds the Port Forwarding Rule list and editor in the Vault.
func WithForwarding(s *uiforwarding.State) Option {
	return func(o *options) { o.forwarding = s }
}

// withSearch injects the search state. In-package tests use it to type into
// the field through the state; the app leaves it nil and the Vault owns one.
func withSearch(s *uisearch.State) Option {
	return func(o *options) { o.search = s }
}

// surface names what the Vault is showing: the list or one editor.
type surface int

const (
	surfaceList surface = iota
	surfaceForwarding
	surfaceHost
	surfaceKey
	surfaceIdentity
	surfaceGroup
	surfaceProxy
	surfaceSnippetRun
	surfaceSnippet
	surfaceSnippetPackage
	surfaceNote
	surfaceNoteView
	surfaceNoteGroup
	surfaceImport
	surfaceExport
	surfaceBackups
)

// scrolls keeps the list and editor scroll offsets apart. They share one
// layout slot, so without separate state an editor opened while the list is
// scrolled down would start at the list's offset instead of its top. It also
// tracks the surface change itself, which tells an editor opening that its
// own content starts fresh.
type scrolls struct {
	list   ui.ScrollState
	editor ui.ScrollState
	viewer ui.ScrollState
	last   surface
	// justOpened is set for the frame an editor opens. A text area starts with
	// its caret at the end of its value, which would scroll a long Note's
	// source there, so the Note editor puts the caret and the view back at the
	// start once per open.
	justOpened bool
}

// trackEditor resets the editor offset whenever a different editor opens, so
// every editor starts at its top even after the list or a previous editor was
// scrolled.
func (p *scrolls) trackEditor(now surface) {
	p.justOpened = now != p.last && now != surfaceList
	if p.justOpened {
		p.editor.X, p.editor.Y = 0, 0
		p.viewer.X, p.viewer.Y = 0, 0
	}
	p.last = now
}

// editorSurface returns the editor the Vault is showing, or the list.
func editorSurface(s *vaultstate.State, forwarding *uiforwarding.State) surface {
	switch {
	case forwarding != nil && forwarding.Editing():
		return surfaceForwarding
	case s.ImportOpen():
		return surfaceImport
	case s.ExportOpen():
		return surfaceExport
	case s.BackupsOpen():
		return surfaceBackups
	case s.SnippetRunOpen():
		return surfaceSnippetRun
	case s.ViewingNote():
		return surfaceNoteView
	case s.Editing():
		return surfaceHost
	case s.EditingKey():
		return surfaceKey
	case s.EditingIdentity():
		return surfaceIdentity
	case s.EditingGroup():
		return surfaceGroup
	case s.EditingProxyProfile():
		return surfaceProxy
	case s.EditingSnippetPackage():
		return surfaceSnippetPackage
	case s.EditingSnippet():
		return surfaceSnippet
	case s.EditingNoteGroup():
		return surfaceNoteGroup
	case s.EditingNote():
		return surfaceNote
	default:
		return surfaceList
	}
}

// View returns the Vault surface.
func View(s *vaultstate.State, opts ...Option) func(*ui.Context) {
	var configured options
	for _, opt := range opts {
		opt(&configured)
	}
	var positions scrolls
	// The Vault's inline search survives frames with the view; its records
	// read the live Vault state each build.
	search := configured.search
	if search == nil {
		search = newVaultSearch(s)
	}
	return func(c *ui.Context) {
		// Test connection attempts complete off the UI thread.
		s.FlushTests()
		// The OpenSSH agent probe completes off the UI thread.
		s.FlushSecurityKeys()
		// A host-key trust decision persists off the UI thread and marks the
		// Known Host list stale; apply it here, on the UI thread.
		s.FlushKnownHosts()
		// A sync-applied cloud copy rewrites the Vault families off the UI
		// thread; apply the stale flag here so the lists refresh in this
		// frame. An open editor form is left intact.
		s.FlushLists()
		if configured.forwarding != nil {
			// Runtime status arrives on the forwarding manager's goroutine;
			// apply it on the UI thread before rendering.
			configured.forwarding.Flush()
		}

		m := locales.In(c.Locale())
		t := c.Theme()

		current := editorSurface(s, configured.forwarding)
		positions.trackEditor(current)

		ui.Column(c).Fill().Grow(1).Gap(12).Children(func() {
			switch current {
			case surfaceForwarding:
				uiforwarding.Editor(c, configured.forwarding, &positions.editor)
			case surfaceHost:
				editor(c, s, m, t, &positions.editor)
			case surfaceKey:
				keyEditor(c, s, m, t, &positions.editor)
			case surfaceIdentity:
				identityEditor(c, s, m, t, &positions.editor)
			case surfaceGroup:
				groupEditor(c, s, m, t, &positions.editor)
			case surfaceProxy:
				proxyEditor(c, s, m, t, &positions.editor)
			case surfaceSnippetRun:
				snippetRunEditor(c, s, m, t, &positions.editor)
			case surfaceSnippet:
				snippetEditor(c, s, m, t, &positions.editor)
			case surfaceSnippetPackage:
				snippetPackageEditor(c, s, m, t, &positions.editor)
			case surfaceNote:
				noteEditor(c, s, m, t, &positions.editor, positions.justOpened)
			case surfaceNoteView:
				noteViewer(c, s, m, t, &positions.viewer)
			case surfaceNoteGroup:
				noteGroupEditor(c, s, m, t, &positions.editor)
			case surfaceImport:
				importPanel(c, s, m, t, &positions.editor)
			case surfaceExport:
				exportPanel(c, s, m, t, &positions.editor)
			case surfaceBackups:
				backupPanel(c, s, m, t, &positions.editor)
			default:
				list(c, s, configured.forwarding, search, m, t, &positions.list)
			}
		})
	}
}

func list(c *ui.Context, s *vaultstate.State, forwarding *uiforwarding.State,
	search *uisearch.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	// Wrap, so the title keeps one line and every action stays inside the
	// content column at the 720x480 minimum: the actions move to the next
	// line instead of being squeezed or clipped at the right edge.
	ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
		ui.Text(c, m.VaultHostListTitle(s.HostCount())).FontSize(20).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultImportTitle()).Clicked() {
			s.BeginImport()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultExportTitle()).Clicked() {
			s.BeginExport()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultBackupOpen()).Clicked() {
			s.BeginBackups()
			c.Invalidate()
		}
		if ui.PrimaryButton(c, m.VaultHostNew()).Clicked() {
			s.BeginCreate()
			c.Invalidate()
		}
	})

	searchBar(c, search, m)

	problem(c, s, t)
	secretBanner(c, s, m, t)

	if pending := s.PendingDelete(); pending != "" {
		message := m.VaultHostDeleteConfirmUnknown()
		for _, host := range s.Items() {
			if host.ID == pending {
				message = m.VaultHostDeleteConfirm(host.Label)
				break
			}
		}
		ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
			ui.Text(c, message).Grow(1).TextColor(uitheme.DangerText(t))
			if ui.Button(c, m.VaultHostCancel()).Clicked() {
				s.CancelDelete()
				c.Invalidate()
			}
			if ui.Button(c, m.VaultHostDelete()).Clicked() {
				_ = s.ConfirmDelete()
				c.Invalidate()
			}
		})
	}

	keyDeleteBanner(c, s, m, t)
	identityDeleteBanner(c, s, m, t)
	groupDeleteBanner(c, s, m, t)
	proxyDeleteBanner(c, s, m, t)
	knownHostDeleteBanner(c, s, m, t)
	snippetDeleteBanner(c, s, m, t)
	snippetPackageDeleteBanner(c, s, m, t)
	noteDeleteBanner(c, s, m, t)
	noteGroupDeleteBanner(c, s, m, t)
	noteExportBanner(c, s, m, t)
	importOutcomeBanner(c, s, m, t)
	exportOutcomeBanner(c, s, m, t)

	// Reserve the overlay scroll bar's gutter so a row's last action does not
	// run under it, as the Settings viewport does.
	ui.Scroll(c).Grow(1).PaddingEnd(scrollbarGutter(t)).TrackScroll(scroll).Children(func() {
		if strings.TrimSpace(search.Query()) != "" {
			vaultSearchResults(c, search, m, t)
			return
		}
		hostSections(c, s, m, t)
		keySection(c, s, m, t)
		identitySection(c, s, m, t)
		proxySection(c, s, m, t)
		knownHostSection(c, s, m, t)
		snippetSection(c, s, m, t)
		notesSection(c, s, m, t)
		if forwarding != nil {
			uiforwarding.Section(c, forwarding)
		}
	})
}

// scrollbarGutter is the width of the overlay scroll bar's track for theme t:
// the thumb (t.ScrollbarWidth DIPs, 6 when unset) plus the 6 DIPs it is inset
// and padded by in MyGo's scrollBars geometry. Reserving it keeps a viewport's
// rows and fields from running under the bar.
func scrollbarGutter(t *ui.Theme) float32 {
	width := t.ScrollbarWidth
	if width <= 0 {
		width = 6
	}
	return width + 6
}

// hostGroupSection is one Host Group, or one group path a Host references
// without a saved group record, and the Hosts directly in it.
type hostGroupSection struct {
	path   string
	group  domainvault.Group
	stored bool
	depth  int
	hosts  []domainvault.Host
}

func hostSections(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultGroupListTitle(s.GroupCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultGroupNew()).Clicked() {
			s.BeginCreateGroup()
			c.Invalidate()
		}
	})
	if s.GroupCount() == 0 {
		ui.Text(c, m.VaultGroupEmpty()).FontSize(12).TextColor(t.TextMuted)
	}

	sections, rootHosts := groupSections(s)
	for _, section := range sections {
		if section.stored {
			groupRow(c, s, section, m, t)
		} else {
			implicitGroupRow(c, s, section, m, t)
		}
		for _, host := range section.hosts {
			hostRow(c, s, host, m, t, section.depth+1)
		}
	}

	// The Vault root is always a drop target, even with no ungrouped Host, so
	// every Host can be moved out of a group.
	rootDropTarget(c, s, m, t)
	for _, host := range rootHosts {
		hostRow(c, s, host, m, t, 0)
	}
	if s.HostCount() == 0 {
		ui.Text(c, m.VaultHostEmpty()).TextColor(t.TextMuted)
	}
}

// groupSections arranges the listed Hosts under their Host Group. A group a
// Host references without a saved record still gets a section, so an implicit
// path never disguises itself as the Vault root. An implicit section is
// inserted below its deepest listed ancestor, which keeps it inside that
// group's subtree instead of after every unrelated root group.
func groupSections(s *vaultstate.State) ([]hostGroupSection, []domainvault.Host) {
	hosts := s.EffectiveItems()
	sections := make([]hostGroupSection, 0, s.GroupCount())
	for _, group := range s.Groups() {
		sections = append(sections, hostGroupSection{
			path: group.Path, group: group, stored: true,
			depth: groupDepth(group.Path),
		})
	}
	var root []domainvault.Host
	for _, host := range hosts {
		path := domainvault.NormalizeGroup(host.Group)
		if path == "" {
			root = append(root, host)
			continue
		}
		at := sectionIndex(sections, path)
		if at < 0 {
			at = implicitSectionIndex(sections, path)
			sections = append(sections, hostGroupSection{})
			copy(sections[at+1:], sections[at:])
			sections[at] = hostGroupSection{path: path, depth: groupDepth(path)}
		}
		sections[at].hosts = append(sections[at].hosts, host)
	}
	return sections, root
}

func sectionIndex(sections []hostGroupSection, path string) int {
	for i, section := range sections {
		if section.path == path {
			return i
		}
	}
	return -1
}

// implicitSectionIndex returns where a section for path belongs: after its
// deepest listed ancestor, or at the end when no listed section contains it.
func implicitSectionIndex(sections []hostGroupSection, path string) int {
	at := len(sections)
	for i, section := range sections {
		if section.path != path && domainvault.IsGroupAtOrBelow(path, section.path) {
			at = i + 1
		}
	}
	return at
}

// groupDepth counts a group path's nesting level, which sets its indent.
func groupDepth(path string) int {
	path = domainvault.NormalizeGroup(path)
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

func groupIndent(depth int) float32 {
	return 8 + float32(depth)*16
}

func groupRow(c *ui.Context, s *vaultstate.State, section hostGroupSection, m locales.Messages, t *ui.Theme) {
	group := section.group
	row := ui.Row(c).FillWidth().Gap(8).Padding(8).PaddingStart(groupIndent(section.depth)).
		Background(t.Surface)
	if _, over := ui.DragOver[vaultstate.DragItem](row); over {
		row.Border(1, t.Accent)
	}
	if drag, dropped := ui.Drop[vaultstate.DragItem](row); dropped && drag.HostID != "" {
		s.MoveHostToGroup(drag.HostID, group.Path)
		c.Invalidate()
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, group.Path).Bold()
			tokenRow(c, groupSummary(s, group, m), t)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditGroup(group.Path)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteGroup(group.Path)
			c.Invalidate()
		}
	})
}

// implicitGroupRow shows a group path only Hosts reference. It has no stored
// defaults to edit, so it carries no edit or delete action, but it is still a
// drop target like every other visible group row.
func implicitGroupRow(c *ui.Context, s *vaultstate.State, section hostGroupSection, m locales.Messages, t *ui.Theme) {
	row := ui.Row(c).FillWidth().Gap(8).Padding(8).PaddingStart(groupIndent(section.depth)).
		Background(t.Surface)
	if _, over := ui.DragOver[vaultstate.DragItem](row); over {
		row.Border(1, t.Accent)
	}
	if drag, dropped := ui.Drop[vaultstate.DragItem](row); dropped && drag.HostID != "" {
		s.MoveHostToGroup(drag.HostID, section.path)
		c.Invalidate()
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, section.path).Bold()
			ui.Text(c, m.VaultGroupMemberCount(s.GroupMemberCount(section.path))).
				FontSize(12).TextColor(t.TextMuted)
		})
	})
}

func groupSummary(s *vaultstate.State, group domainvault.Group, m locales.Messages) []string {
	parts := make([]string, 0, 5)
	parts = append(parts, m.VaultGroupMemberCount(s.GroupMemberCount(group.Path)))
	if group.Username != "" {
		parts = append(parts, group.Username)
	}
	if group.Port != 0 {
		parts = append(parts, strconv.Itoa(group.Port))
	}
	if profile, ok := s.ProxyProfileByID(group.ProxyProfileID); ok {
		parts = append(parts, profile.Label)
	}
	return parts
}

// rootDropTarget accepts a dragged Host and moves it out of every group. It
// stays visible with no groups so a Host can always return to the root, and it
// is tall enough that a dragged row does not hide its label.
func rootDropTarget(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	row := ui.Row(c).FillWidth().Gap(8).Padding(8).Radius(t.Radius).Border(1, t.Border)
	if _, over := ui.DragOver[vaultstate.DragItem](row); over {
		row.Background(t.Surface).Border(2, t.Accent)
	}
	if drag, dropped := ui.Drop[vaultstate.DragItem](row); dropped && drag.HostID != "" {
		s.MoveHostToGroup(drag.HostID, "")
		c.Invalidate()
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, m.VaultGroupUngrouped()).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, m.VaultGroupMemberCount(s.RootHostCount())).
				FontSize(12).TextColor(t.TextMuted)
		})
	})
}

func groupDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteGroup() == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultGroupDeleteConfirm(s.PendingDeleteGroup())).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteGroup()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteGroup()
			c.Invalidate()
		}
	})
}

func keySection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultKeyListTitle(s.KeyCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultKeyNew()).Clicked() {
			s.BeginCreateKey()
			c.Invalidate()
		}
	})
	if s.KeyCount() == 0 {
		ui.Text(c, m.VaultKeyEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, key := range s.Keys() {
		keyRow(c, s, key, m, t)
	}
}

func keyRow(c *ui.Context, s *vaultstate.State, key domainvault.Key, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, key.Label).Bold()
			ui.Text(c, keySummary(key, m)).FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditKey(key.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteKey(key.ID)
			c.Invalidate()
		}
	})
}

func keySummary(key domainvault.Key, m locales.Messages) string {
	parts := make([]string, 0, 4)
	if key.Type != "" {
		parts = append(parts, string(key.Type))
	}
	if key.HasPrivateKey {
		parts = append(parts, m.VaultKeyStored())
	}
	if key.HasPassphrase {
		parts = append(parts, m.VaultKeyPassphraseStored())
	}
	if key.Certificate != "" {
		parts = append(parts, m.VaultKeyCertificateStored())
	}
	return strings.Join(parts, " · ")
}

func keyDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteKey() == "" {
		return
	}
	label := ""
	for _, key := range s.Keys() {
		if key.ID == s.PendingDeleteKey() {
			label = key.Label
			break
		}
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, message).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteKey()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteKey()
			c.Invalidate()
		}
	})
}

func identitySection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultIdentityListTitle(s.IdentityCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultIdentityNew()).Clicked() {
			s.BeginCreateIdentity()
			c.Invalidate()
		}
	})
	if s.IdentityCount() == 0 {
		ui.Text(c, m.VaultIdentityEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, identity := range s.Identities() {
		identityRow(c, s, identity, m, t)
	}
}

func identityRow(c *ui.Context, s *vaultstate.State, identity domainvault.Identity, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, identity.Label).Bold()
			ui.Text(c, identitySummary(identity, m)).FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditIdentity(identity.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteIdentity(identity.ID)
			c.Invalidate()
		}
	})
}

func identitySummary(identity domainvault.Identity, m locales.Messages) string {
	parts := make([]string, 0, 3)
	if identity.Username != "" {
		parts = append(parts, identity.Username)
	}
	parts = append(parts, identityMethodLabel(identity.AuthMethod, m))
	if identity.HasPassword {
		parts = append(parts, m.VaultHostPasswordStored())
	}
	return strings.Join(parts, " · ")
}

func identityMethodLabel(method domainvault.IdentityAuthMethod, m locales.Messages) string {
	switch method {
	case domainvault.IdentityAuthKey:
		return m.VaultIdentityMethodKey()
	case domainvault.IdentityAuthCertificate:
		return m.VaultIdentityMethodCertificate()
	default:
		return m.VaultIdentityMethodPassword()
	}
}

func identityDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteIdentity() == "" {
		return
	}
	label := ""
	for _, identity := range s.Identities() {
		if identity.ID == s.PendingDeleteIdentity() {
			label = identity.Label
			break
		}
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, message).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteIdentity()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteIdentity()
			c.Invalidate()
		}
	})
}

func proxySection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Children(func() {
		ui.Text(c, m.VaultProxyListTitle(s.ProxyProfileCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultProxyNew()).Clicked() {
			s.BeginCreateProxyProfile()
			c.Invalidate()
		}
	})
	if s.ProxyProfileCount() == 0 {
		ui.Text(c, m.VaultProxyEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, profile := range s.ProxyProfiles() {
		proxyRow(c, s, profile, m, t)
	}
}

func proxyRow(c *ui.Context, s *vaultstate.State, profile domainvault.ProxyProfile, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, profile.Label).Bold()
			ui.Text(c, proxySummary(m, profile)).
				FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditProxyProfile(profile.ID)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteProxyProfile(profile.ID)
			c.Invalidate()
		}
	})
}

// proxySummary names a profile's type and endpoint for a list row. It never
// includes a credential.
func proxySummary(m locales.Messages, profile domainvault.ProxyProfile) string {
	endpoint := profile.Config.Host
	if endpoint != "" {
		endpoint += ":" + strconv.Itoa(profile.Config.Port)
	}
	switch profile.Config.Type {
	case domainvault.ProxyCommand:
		return m.VaultProxyTypeCommand()
	case domainvault.ProxyHTTP:
		return m.VaultProxySummary(m.VaultProxyTypeHTTP(), endpoint)
	case domainvault.ProxySOCKS5:
		return m.VaultProxySummary(m.VaultProxyTypeSOCKS5(), endpoint)
	default:
		return endpoint
	}
}

func proxyDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteProxyProfile() == "" {
		return
	}
	label := ""
	if profile, ok := s.ProxyProfileByID(s.PendingDeleteProxyProfile()); ok {
		label = profile.Label
	}
	message := m.VaultDeleteConfirmUnknown()
	if label != "" {
		message = m.VaultDeleteConfirm(label)
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, message).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteProxyProfile()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteProxyProfile()
			c.Invalidate()
		}
	})
}

func knownHostSection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.VaultKnownHostsTitle(s.KnownHostCount())).FontSize(16).Bold()
	if s.KnownHostCount() == 0 {
		ui.Text(c, m.VaultKnownHostsEmpty()).FontSize(12).TextColor(t.TextMuted)
	}
	for _, record := range s.KnownHosts() {
		knownHostRow(c, s, record, m, t)
	}
}

func knownHostRow(c *ui.Context, s *vaultstate.State, record domainconn.KnownHost, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, net.JoinHostPort(record.Hostname, strconv.Itoa(record.Port))).Bold()
			ui.Text(c, record.KeyType+" · "+record.Fingerprint).
				FontSize(12).TextColor(t.TextMuted)
		})
		ui.Text(c, m.VaultKnownHostTrusted()).FontSize(12).TextColor(t.TextMuted)
		if ui.Button(c, m.VaultKnownHostRemove()).Clicked() {
			s.RequestDeleteKnownHost(record.ID)
			c.Invalidate()
		}
	})
}

func knownHostDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteKnownHost() == "" {
		return
	}
	record, ok := s.KnownHostByID(s.PendingDeleteKnownHost())
	if !ok {
		s.CancelDeleteKnownHost()
		return
	}
	endpoint := net.JoinHostPort(record.Hostname, strconv.Itoa(record.Port))
	ui.Row(c).FillWidth().Gap(8).Padding(8).Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultKnownHostRemoveConfirm(endpoint)).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteKnownHost()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteKnownHost()
			c.Invalidate()
		}
	})
}

func snippetSection(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(0, 8).Wrap().Children(func() {
		ui.Text(c, m.VaultSnippetListTitle(s.SnippetCount())).FontSize(16).Bold()
		ui.Column(c).Grow(1)
		if ui.Button(c, m.VaultSnippetPackageNew()).Clicked() {
			s.BeginCreateSnippetPackage()
			c.Invalidate()
		}
		if ui.PrimaryButton(c, m.VaultSnippetNew()).Clicked() {
			s.BeginCreateSnippet()
			c.Invalidate()
		}
	})
	if s.SnippetCount() == 0 && s.SnippetPackageCount() == 0 {
		ui.Text(c, m.VaultSnippetEmpty()).FontSize(12).TextColor(t.TextMuted)
		return
	}
	for _, pkg := range s.SnippetPackages() {
		snippetPackageRow(c, s, pkg, m, t)
		for _, snippet := range s.SnippetsInPackage(pkg.Path) {
			snippetRow(c, s, snippet, m, t, 1)
		}
	}
	root := s.SnippetsInPackage("")
	if len(root) > 0 {
		ui.Text(c, m.VaultGroupUngrouped()).FontSize(12).TextColor(t.TextMuted)
		for _, snippet := range root {
			snippetRow(c, s, snippet, m, t, 0)
		}
	}
}

func snippetPackageRow(c *ui.Context, s *vaultstate.State, pkg domainvault.SnippetPackage, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).Wrap().Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, pkg.Path).Bold()
			ui.Text(c, m.VaultSnippetPackageSnippetCount(len(s.SnippetsInPackage(pkg.Path)))).
				FontSize(12).TextColor(t.TextMuted)
		})
		if ui.Button(c, m.VaultHostEdit()).Clicked() {
			s.BeginEditSnippetPackage(pkg.Path)
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			s.RequestDeleteSnippetPackage(pkg.Path)
			c.Invalidate()
		}
	})
}

func snippetRow(c *ui.Context, s *vaultstate.State, snippet domainvault.Snippet, m locales.Messages, t *ui.Theme, indent int) {
	ui.Row(c).FillWidth().Gap(8).Padding(8).PaddingStart(groupIndent(indent)).
		Background(t.Surface).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
				ui.Text(c, snippet.Label).Bold()
				ui.Column(c).Grow(1)
				if ui.PrimaryButton(c, m.VaultSnippetRunActive()).Clicked() {
					s.BeginSnippetRun(snippet.ID, vaultstate.SnippetDestinationActive)
					c.Invalidate()
				}
				if ui.Button(c, m.VaultSnippetRunHosts()).Clicked() {
					s.BeginSnippetRun(snippet.ID, vaultstate.SnippetDestinationHosts)
					c.Invalidate()
				}
				if ui.Button(c, m.VaultHostEdit()).Clicked() {
					s.BeginEditSnippet(snippet.ID)
					c.Invalidate()
				}
				if ui.Button(c, m.VaultHostDelete()).Clicked() {
					s.RequestDeleteSnippet(snippet.ID)
					c.Invalidate()
				}
			})
			tokens := snippetSummary(s, snippet, m)
			tokenRow(c, tokens, t)
		})
	})
}

// snippetSummary names where a Snippet runs and how it is sent, so the list
// answers "will this go to the Session I am looking at?" without opening the
// editor.
func snippetSummary(s *vaultstate.State, snippet domainvault.Snippet, m locales.Messages) []string {
	parts := []string{m.VaultSnippetSummaryTargets(snippetTargetText(s, snippet, m))}
	if count := len(domainvault.ParseSnippetVariables(snippet.Command)); count > 0 {
		parts = append(parts, m.VaultSnippetVariableCount(count))
	}
	if snippet.MultiLineRunMode == domainvault.SnippetRunLineDelay {
		parts = append(parts, m.VaultSnippetLineDelay())
	}
	if snippet.NoAutoRun {
		parts = append(parts, m.VaultSnippetNoAutoRun())
	}
	if len(snippet.Tags) > 0 {
		parts = append(parts, strings.Join(snippet.Tags, ", "))
	}
	return parts
}

func snippetTargetText(s *vaultstate.State, snippet domainvault.Snippet, m locales.Messages) string {
	if snippet.TargetsAllHosts {
		return m.VaultSnippetSummaryAllHosts()
	}
	if !domainvault.SnippetHasRunTargets(snippet) {
		return m.VaultSnippetSummaryNoTargets()
	}
	parts := make([]string, 0, len(snippet.Targets)+len(snippet.TargetGroups))
	for _, path := range snippet.TargetGroups {
		parts = append(parts, path)
	}
	for _, id := range snippet.Targets {
		label := string(id)
		for _, host := range s.Items() {
			if host.ID == id {
				label = host.Label
				break
			}
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, ", ")
}

func snippetDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteSnippet() == "" {
		return
	}
	label := ""
	if snippet, ok := s.SnippetByID(s.PendingDeleteSnippet()); ok {
		label = snippet.Label
	}
	if label == "" {
		s.CancelDeleteSnippet()
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Wrap().Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultSnippetDeleteConfirm(label)).Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteSnippet()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteSnippet()
			c.Invalidate()
		}
	})
}

func snippetPackageDeleteBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	if s.PendingDeleteSnippetPackage() == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(8).Padding(8).Wrap().Background(t.Surface).Children(func() {
		ui.Text(c, m.VaultSnippetPackageDeleteConfirm(s.PendingDeleteSnippetPackage())).
			Grow(1).TextColor(uitheme.DangerText(t))
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelDeleteSnippetPackage()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostDelete()).Clicked() {
			_ = s.ConfirmDeleteSnippetPackage()
			c.Invalidate()
		}
	})
}

func snippetEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.SnippetForm()
	title := m.VaultSnippetFormCreateTitle()
	if form.ID != "" {
		title = m.VaultSnippetFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultSnippetLabel(), func() {
				ui.TextInput(c, &form.Label)
			})
			ui.Field(c, m.VaultSnippetCommand(), func() {
				ui.TextArea(c, &form.Command).Height(120)
			}).Description(m.VaultSnippetCommandHint())
			ui.Field(c, m.VaultSnippetPackage(), func() {
				ui.TextInput(c, &form.Package).Placeholder("ops/linux")
			}).Description(m.VaultSnippetPackageHint())
			ui.Field(c, m.VaultHostTags(), func() {
				ui.TextInput(c, &form.Tags).Placeholder("prod, web")
			})

			ui.Text(c, m.VaultSnippetTargets()).Bold()
			if ui.Checkbox(c, &form.TargetsAllHosts, m.VaultSnippetAllHosts()).Changed() {
				c.Invalidate()
			}
			if !form.TargetsAllHosts {
				ui.Text(c, m.VaultSnippetNoTargetsHint()).FontSize(12).TextColor(t.TextMuted)
				groups := s.SnippetTargetGroupPaths()
				if len(groups) > 0 {
					ui.Text(c, m.VaultSnippetTargetGroups()).FontSize(12).TextColor(t.TextMuted)
					ui.Row(c).Gap(8).Wrap().Children(func() {
						for _, path := range groups {
							checked := form.TargetGroups[path]
							if ui.Checkbox(c, &checked, path).Changed() {
								form.TargetGroups[path] = checked
								c.Invalidate()
							}
						}
					})
				}
				if s.HostCount() > 0 {
					ui.Row(c).Gap(8).Wrap().Children(func() {
						for _, host := range s.Items() {
							checked := form.TargetHosts[string(host.ID)]
							if ui.Checkbox(c, &checked, host.Label).Changed() {
								form.TargetHosts[string(host.ID)] = checked
								c.Invalidate()
							}
						}
					})
				}
			}

			ui.Field(c, m.VaultSnippetRunMode(), func() {
				ui.RadioGroup(c, func() {
					ui.Radio(c, &form.MultiLineRunMode,
						string(domainvault.SnippetRunPaste), m.VaultSnippetRunModePaste())
					ui.Radio(c, &form.MultiLineRunMode,
						string(domainvault.SnippetRunLineDelay), m.VaultSnippetRunModeLineDelay())
				})
			})
			ui.Checkbox(c, &form.NoAutoRun, m.VaultSnippetNoAutoRunLabel())
		})

	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveSnippet() }, s.CancelSnippetEdit, m)
}

func snippetPackageEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.SnippetPackageForm()
	title := m.VaultSnippetPackageFormCreateTitle()
	if form.OriginalPath != "" {
		title = m.VaultSnippetPackageFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()
		ui.Form(c, func() {
			ui.Field(c, m.VaultSnippetPackagePath(), func() {
				ui.TextInput(c, &form.Path).Placeholder("ops/linux")
			}).Description(m.VaultSnippetPackagePathHint())
		})
	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveSnippetPackage() }, s.CancelSnippetPackageEdit, m)
}

func snippetRunEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.SnippetRunForm()
	if form == nil {
		return
	}
	label := ""
	if snippet, ok := s.SnippetByID(form.SnippetID); ok {
		label = snippet.Label
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, m.VaultSnippetRunTitle(label)).FontSize(20).Bold()
		destination := m.VaultSnippetRunDestinationActive()
		if form.Destination == vaultstate.SnippetDestinationHosts {
			destination = m.VaultSnippetRunDestinationHosts()
		}
		ui.Text(c, destination).FontSize(12).TextColor(t.TextMuted)

		ui.Form(c, func() {
			for index := range form.Variables {
				variable := &form.Variables[index]
				ui.Field(c, m.VaultSnippetRunVariable(variable.Name), func() {
					ui.TextInput(c, &variable.Value)
				})
				if variable.HasDefault {
					ui.Text(c, m.VaultSnippetRunVariableDefault(variable.DefaultValue)).
						FontSize(12).TextColor(t.TextMuted)
				}
			}
			ui.Text(c, m.VaultSnippetRunPreview()).Bold()
			ui.Text(c, s.SnippetRunPreview()).FontSize(12).TextColor(t.TextMuted)
		})

		ui.Text(c, m.VaultSnippetVariablesHint()).FontSize(12).TextColor(t.TextMuted)
	})

	// A run failure must be visible without scrolling, like every editor's
	// save problem.
	snippetRunProblem(c, s, m, t)

	// The run form's primary action is Run, not Save: nothing is persisted here
	// except the remembered variable values, after a successful run.
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultSnippetRunConfirm()).Clicked() {
			s.ConfirmSnippetRun()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			s.CancelSnippetRun()
			c.Invalidate()
		}
	})
}

func snippetRunProblem(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	problem := s.SnippetRunProblem()
	if problem == vaultstate.ProblemNone {
		return
	}
	if missing := s.SnippetRunMissing(); len(missing) > 0 {
		ui.Text(c, m.VaultSnippetMissingVariables(strings.Join(missing, ", "))).
			TextColor(uitheme.DangerText(t))
	}
	if text := problemText(m, problem); text != "" {
		ui.Text(c, text).TextColor(uitheme.DangerText(t))
	}
}

// secretBanner offers the credential-protection choice when the OS store is
// unavailable, and unlocks a passphrase-wrapped store. Session-only mode is
// stated so the user knows credentials will not survive a restart.
func secretBanner(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	switch s.SecretMode() {
	case appvault.SecretModeUnavailable:
		ui.Column(c).Fill().Padding(12).Gap(8).Background(t.Surface).Children(func() {
			ui.Text(c, m.VaultSecretSetupTitle()).Bold()
			ui.Text(c, m.VaultSecretSetupBody()).FontSize(12).TextColor(t.TextMuted)
			ui.Field(c, m.VaultSecretPassphrase(), func() {
				ui.TextInput(c, s.SecretPassphrase()).Password().Width(260)
			})
			ui.Field(c, m.VaultSecretConfirm(), func() {
				ui.TextInput(c, s.SecretConfirmation()).Password().Width(260)
			})
			secretProblem(c, s, t)
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, m.VaultSecretUsePassphrase()).Clicked() {
					s.SetSecretPassphrase()
					c.Invalidate()
				}
				if ui.Button(c, m.VaultSecretUseSession()).Clicked() {
					s.UseSessionSecrets()
					c.Invalidate()
				}
			})
		})
	case appvault.SecretModePassphraseLocked:
		ui.Column(c).Fill().Padding(12).Gap(8).Background(t.Surface).Children(func() {
			ui.Text(c, m.VaultSecretLockedTitle()).Bold()
			ui.Text(c, m.VaultSecretLockedBody()).FontSize(12).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				ui.TextInput(c, s.SecretPassphrase()).Password().Width(260)
				if ui.PrimaryButton(c, m.VaultSecretUnlock()).Clicked() {
					s.UnlockSecrets()
					c.Invalidate()
				}
			})
			secretProblem(c, s, t)
		})
	case appvault.SecretModeSession:
		ui.Text(c, m.VaultSecretSessionNote()).FontSize(12).TextColor(t.TextMuted)
	}
}

func secretProblem(c *ui.Context, s *vaultstate.State, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), s.SecretProblem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(uitheme.DangerText(t))
}

func hostRow(c *ui.Context, s *vaultstate.State, host domainvault.Host, m locales.Messages, t *ui.Theme, indent int) {
	row := ui.Row(c).FillWidth().Gap(8).Padding(8).PaddingStart(groupIndent(indent)).
		Background(t.Surface)
	row.Children(func() {
		// The summary gets the column's full width below the label and actions,
		// so it stays at one or two lines at the minimum window size instead of
		// being squeezed into a narrow column beside five action buttons.
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).FillWidth().Gap(8).Children(func() {
				// Only the label drags, so the drag ghost stays a small copy of
				// the label instead of a full row that hides the drop target.
				ui.Text(c, host.Label).Bold().Drag(vaultstate.HostDrag(host.ID))
				ui.Column(c).Grow(1)
			})
			// The actions get their own wrapping line: at the minimum window
			// size a long label can no longer squeeze Delete to zero width, and
			// at the default size all five still share one line.
			ui.Row(c).FillWidth().Gap(8).Wrap().Children(func() {
				if host.Protocol == domainvault.ProtocolSSH ||
					host.Protocol == domainvault.ProtocolMosh ||
					host.Protocol == domainvault.ProtocolET {
					testLabel := m.VaultHostTest()
					if s.Testing(host.ID) {
						testLabel = m.VaultHostTesting()
					}
					if ui.Button(c, testLabel).Clicked() {
						s.Test(host.ID)
						c.Invalidate()
					}
					if ui.PrimaryButton(c, m.VaultHostConnect()).Clicked() {
						s.Connect(host.ID)
						c.Invalidate()
					}
				}
				if ui.Button(c, m.VaultHostEdit()).Clicked() {
					s.BeginEdit(host.ID)
					c.Invalidate()
				}
				pin := m.VaultHostPin()
				if host.Pinned {
					pin = m.VaultHostUnpin()
				}
				if ui.Button(c, pin).Clicked() {
					_ = s.TogglePinned(host.ID)
					c.Invalidate()
				}
				if ui.Button(c, m.VaultHostDelete()).Clicked() {
					s.RequestDelete(host.ID)
					c.Invalidate()
				}
			})

			summaryRow(c, s, host, m, t)
			if err := s.TestProblemError(host.ID); err != nil {
				if text := uiconnection.ProblemTextFor(m, err); text != "" {
					ui.Text(c, text).FontSize(12).TextColor(uitheme.DangerText(t))
				}
			} else if problem := s.TestProblem(host.ID); problem != "" {
				if text := uiconnection.ProblemText(m, problem); text != "" {
					ui.Text(c, text).FontSize(12).TextColor(uitheme.DangerText(t))
				}
			} else if s.TestSucceeded(host.ID) {
				ui.Text(c, m.VaultHostTestSuccess()).FontSize(12).TextColor(t.TextMuted)
			}
		})
	})
}

// securityKeyStatus reports the system OpenSSH agent's hardware identity state
// while the agent option is enabled, so the user learns about a missing
// installation or an empty agent before saving the Host.
func securityKeyStatus(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme) {
	summary, ok := s.SecurityKeyStatus()
	if !ok {
		return
	}
	switch {
	case !summary.Available:
		ui.Text(c, m.VaultHostSecurityKeyUnavailable()).FontSize(12).TextColor(uitheme.DangerText(t))
	case summary.Count == 0:
		ui.Text(c, m.VaultHostSecurityKeyEmpty()).FontSize(12).TextColor(t.TextMuted)
	default:
		ui.Text(c, m.VaultHostSecurityKeyReady(summary.Count)).FontSize(12).TextColor(t.TextMuted)
	}
}

// summaryRow lays the Host's summary out as separate wrapped tokens. No token
// is a bare separator, so a narrow window never starts a line with "·".
func summaryRow(c *ui.Context, s *vaultstate.State, host domainvault.Host, m locales.Messages, t *ui.Theme) {
	tokenRow(c, hostSummary(s, host, m), t)
}

// tokenRow wraps summary tokens without inserting separator glyphs of its own.
func tokenRow(c *ui.Context, parts []string, t *ui.Theme) {
	ui.Row(c).Wrap().Gap(6).Children(func() {
		for _, part := range parts {
			ui.Text(c, part).FontSize(12).TextColor(t.TextMuted)
		}
	})
}

func hostSummary(s *vaultstate.State, host domainvault.Host, m locales.Messages) []string {
	parts := make([]string, 0, 5)
	if host.Protocol == domainvault.ProtocolLocal {
		parts = append(parts, m.VaultHostLocal())
	} else {
		parts = append(parts, net.JoinHostPort(host.Hostname, strconv.Itoa(host.Port)))
	}
	if host.Username != "" {
		parts = append(parts, host.Username)
	}
	parts = append(parts, authSummary(s, host, m))
	if len(host.Tags) > 0 {
		parts = append(parts, strings.Join(host.Tags, ", "))
	}
	if host.Pinned {
		parts = append(parts, "★")
	}
	return parts
}

// authSummary reports the method a connection will use after the Identity,
// explicit Key, and Host method are resolved. The precedence itself comes from
// the domain so the list never claims a credential that will not be tried.
func authSummary(s *vaultstate.State, host domainvault.Host, m locales.Messages) string {
	identities := s.Identities()
	var identity *domainvault.Identity
	if host.IdentityID != "" {
		for i := range identities {
			if identities[i].ID == host.IdentityID {
				identity = &identities[i]
				break
			}
		}
	}
	selection := domainvault.ResolveHostAuth(host, identity)
	// The original uses the Identity password first and the Host password as
	// the fallback, so either makes a password attempt possible.
	hasPassword := host.HasPassword || (identity != nil && identity.HasPassword)

	switch selection.Method {
	case domainvault.HostAuthPassword:
		return m.VaultAuthSummaryPassword()
	case domainvault.HostAuthKey, domainvault.HostAuthCertificate:
		return keyAuthSummary(s.Keys(), string(selection.KeyID), selection.Method, m)
	default:
		// Automatic mode: report the first material that will be tried.
		if host.UseSSHAgent {
			return m.VaultAuthSummaryAgent()
		}
		if selection.KeyID != "" {
			return keyAuthSummary(s.Keys(), string(selection.KeyID), domainvault.HostAuthKey, m)
		}
		if hasPassword {
			return m.VaultAuthSummaryPassword()
		}
		return m.VaultAuthSummaryKeyboard()
	}
}

func keyAuthSummary(keys []domainvault.Key, keyID string, method domainvault.HostAuthMethod, m locales.Messages) string {
	label := keyID
	certified := false
	for _, key := range keys {
		if string(key.ID) == keyID {
			label = key.Label
			certified = key.Certificate != ""
			break
		}
	}
	if method == domainvault.HostAuthCertificate || certified {
		return m.VaultAuthSummaryCertificate(label)
	}
	return m.VaultAuthSummaryKey(label)
}

func editor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.Form()
	title := m.VaultHostFormCreateTitle()
	if form.ID != "" {
		title = m.VaultHostFormEditTitle()
	}

	// The form scrolls independently of the actions, so Save and Cancel stay
	// on screen at every supported window size.
	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultHostLabel(), func() {
				ui.TextInput(c, &form.Label)
			})
			ui.Field(c, m.VaultHostHostname(), func() {
				ui.TextInput(c, &form.Hostname).Placeholder("example.com")
			})
			ui.Row(c).Gap(8).Wrap().Children(func() {
				portHint := ""
				if placeholder := s.HostPortPlaceholder(); placeholder != "" {
					portHint = m.VaultHostPortHint(placeholder)
				}
				portField := ui.Field(c, m.VaultHostPort(), func() {
					// The hint follows the protocol and group selection, so a
					// Telnet Host shows 23 rather than a hardcoded 22. A blank
					// field preserves inheritance; the hint shows what a
					// connection would use.
					ui.TextInput(c, &form.Port).
						Placeholder(s.HostPortPlaceholder()).Width(120)
				})
				if portHint != "" {
					portField.Description(portHint)
				}
				ui.Field(c, m.VaultHostUsername(), func() {
					ui.TextInput(c, &form.Username).Placeholder("root").Width(200)
				})
				ui.Field(c, m.VaultHostProtocol(), func() {
					changed := ui.Select(c, &form.Protocol, []string{
						string(domainvault.ProtocolSSH),
						string(domainvault.ProtocolMosh),
						string(domainvault.ProtocolET),
						string(domainvault.ProtocolTelnet),
						string(domainvault.ProtocolLocal),
					}).Width(140).Changed()
					if changed {
						// A hidden protocol-specific control must not keep a
						// value Save would persist.
						s.SetHostProtocol(form.Protocol)
						c.Invalidate()
					}
				})
			})
			ui.Field(c, m.VaultHostGroup(), func() {
				ui.TextInput(c, &form.Group).Placeholder("prod/web")
			}).Description(m.VaultHostGroupHint())
			ui.Field(c, m.VaultHostTags(), func() {
				ui.TextInput(c, &form.Tags).Placeholder("prod, web")
			}).Description(m.VaultHostTagsHint())
			ui.Field(c, m.VaultHostNotes(), func() {
				ui.TextArea(c, &form.Notes).Height(80)
			})
			passwordHint := m.VaultHostPasswordHint()
			if form.HasPassword {
				passwordHint = m.VaultHostPasswordKeep()
			}
			ui.Field(c, m.VaultHostPassword(), func() {
				// The input is plaintext only while the editor is open. It is
				// never loaded back from storage and never rendered again.
				ui.TextInput(c, &form.Password).Password()
			}).Description(passwordHint)

			if domainvault.Protocol(form.Protocol).UsesSSHAuth() {
				ui.Text(c, m.VaultHostAuthSection()).Bold()
				ui.Field(c, m.VaultHostIdentity(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.IdentityID, "", m.VaultNone())
						for _, identity := range s.Identities() {
							ui.Radio(c, &form.IdentityID, string(identity.ID), identity.Label)
						}
					})
				}).Description(m.VaultHostIdentityHint())
				ui.Field(c, m.VaultHostKey(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.KeyID, "", m.VaultNone())
						for _, key := range s.Keys() {
							ui.Radio(c, &form.KeyID, string(key.ID), key.Label)
						}
					})
				}).Description(m.VaultHostKeyHint())
				ui.Field(c, m.VaultHostAuthMethod(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthAuto), m.VaultHostAuthAuto())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthPassword), m.VaultHostAuthPassword())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthKey), m.VaultHostAuthKey())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthCertificate), m.VaultHostAuthCertificate())
					})
				})
				ui.Checkbox(c, &form.UseSSHAgent, m.VaultHostUseAgent())
				if form.UseSSHAgent {
					securityKeyStatus(c, s, m, t)
				}
				ui.Checkbox(c, &form.RequiresMFA, m.VaultHostRequiresMFA())
			}
			ui.Field(c, m.VaultHostProxyProfile(), func() {
				ui.RadioGroup(c, func() {
					ui.Radio(c, &form.ProxyProfileID, "", m.VaultNone())
					for _, profile := range s.ProxyProfiles() {
						ui.Radio(c, &form.ProxyProfileID, profile.ID, profile.Label)
					}
				})
			}).Description(m.VaultHostProxyProfileHint())
			if form.Protocol == string(domainvault.ProtocolSSH) {
				ui.Field(c, m.VaultHostFileProtocol(), func() {
					changed := ui.Select(c, &form.FileProtocol, []string{
						string(domainvault.FileProtocolAuto),
						string(domainvault.FileProtocolSFTP),
						string(domainvault.FileProtocolSCP),
					}).Width(160).Changed()
					if changed && form.FileProtocol == string(domainvault.FileProtocolSCP) {
						// SCP cannot elevate, so a contradictory sudo flag
						// must not survive the protocol change.
						form.SFTPSudo = false
						c.Invalidate()
					}
				}).Description(m.VaultHostFileProtocolHint())
				ui.Checkbox(c, &form.SFTPSudo, m.VaultHostSFTPSudo()).
					Disabled(form.FileProtocol == string(domainvault.FileProtocolSCP))
				ui.Text(c, m.VaultHostSFTPSudoHint()).FontSize(12).TextColor(t.TextMuted)
				if ui.Checkbox(c, &form.X11Forwarding, m.VaultHostX11Forwarding()).Changed() {
					// Turning X11 off clears the acknowledgement, so re-enabling it
					// always asks the user to accept the warning again.
					s.SetX11Forwarding(form.X11Forwarding)
					c.Invalidate()
				}
				if form.X11Forwarding {
					ui.Text(c, m.VaultHostX11Warning()).FontSize(12).TextColor(uitheme.DangerText(t))
					ui.Checkbox(c, &form.X11WarningAcknowledged, m.VaultHostX11Acknowledge())
				}
				ui.Checkbox(c, &form.NetworkDevice, m.VaultHostNetworkDevice())
				ui.Text(c, m.VaultHostNetworkDeviceHint()).FontSize(12).TextColor(t.TextMuted)
			}
			if form.Protocol == string(domainvault.ProtocolMosh) {
				ui.Field(c, m.VaultHostMoshServerPath(), func() {
					ui.TextInput(c, &form.MoshServerPath).Placeholder("mosh-server")
				}).Description(m.VaultHostMoshServerPathHint())
				ui.Text(c, m.VaultHostMoshRestrictionHint()).FontSize(12).TextColor(t.TextMuted)
			}
			if form.Protocol == string(domainvault.ProtocolET) {
				ui.Field(c, m.VaultHostETPort(), func() {
					ui.TextInput(c, &form.ETPort).Placeholder("2022").Width(120)
				}).Description(m.VaultHostETPortHint())
				ui.Text(c, m.VaultHostETRestrictionHint()).FontSize(12).TextColor(t.TextMuted)
			}
			ui.Checkbox(c, &form.Pinned, m.VaultHostPinned())
		})

		ui.Text(c, m.VaultHostCredentialsNote()).FontSize(12).TextColor(t.TextMuted)
	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.Save() }, s.CancelEdit, m)
}

// editorActions renders the fixed action bar every editor shares, below its
// scrolling form, so Save and Cancel stay reachable however long the form is.
func editorActions(c *ui.Context, save, cancel func(), m locales.Messages) {
	ui.Row(c).Gap(8).Children(func() {
		if ui.PrimaryButton(c, m.VaultHostSave()).Clicked() {
			save()
			c.Invalidate()
		}
		if ui.Button(c, m.VaultHostCancel()).Clicked() {
			cancel()
			c.Invalidate()
		}
	})
}

func keyEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.KeyForm()
	title := m.VaultKeyFormCreateTitle()
	if form.ID != "" {
		title = m.VaultKeyFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultHostLabel(), func() {
				ui.TextInput(c, &form.Label)
			})
			materialHint := m.VaultKeyMaterialHint()
			if form.HasPrivateKey {
				materialHint = m.VaultKeyMaterialKeep()
			}
			ui.Field(c, m.VaultKeyMaterial(), func() {
				ui.TextArea(c, &form.PrivateKey).Height(120)
			}).Description(materialHint)
			passphraseHint := m.VaultKeyPassphraseHint()
			if form.HasPassphrase {
				passphraseHint = m.VaultKeyPassphraseKeep()
			}
			ui.Field(c, m.VaultKeyPassphrase(), func() {
				ui.TextInput(c, &form.Passphrase).Password()
			}).Description(passphraseHint)
			ui.Checkbox(c, &form.SavePassphrase, m.VaultKeySavePassphrase())
			if form.HasPassphrase {
				ui.Checkbox(c, &form.ClearPassphrase, m.VaultKeyClearPassphrase())
			}
			ui.Field(c, m.VaultKeyCertificate(), func() {
				ui.TextArea(c, &form.Certificate).Height(60)
			}).Description(m.VaultKeyCertificateHint())
			if form.HasCertificate {
				ui.Checkbox(c, &form.ClearCertificate, m.VaultKeyClearCertificate())
			}
		})

		ui.Text(c, m.VaultKeyNote()).FontSize(12).TextColor(t.TextMuted)
	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveKey() }, s.CancelKeyEdit, m)
}

func identityEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.IdentityForm()
	title := m.VaultIdentityFormCreateTitle()
	if form.ID != "" {
		title = m.VaultIdentityFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultHostLabel(), func() {
				ui.TextInput(c, &form.Label)
			})
			ui.Field(c, m.VaultHostUsername(), func() {
				ui.TextInput(c, &form.Username).Placeholder("root")
			})
			ui.Field(c, m.VaultIdentityMethod(), func() {
				ui.RadioGroup(c, func() {
					ui.Radio(c, &form.AuthMethod, string(domainvault.IdentityAuthPassword), m.VaultIdentityMethodPassword())
					ui.Radio(c, &form.AuthMethod, string(domainvault.IdentityAuthKey), m.VaultIdentityMethodKey())
					ui.Radio(c, &form.AuthMethod, string(domainvault.IdentityAuthCertificate), m.VaultIdentityMethodCertificate())
				})
			})
			if form.AuthMethod != string(domainvault.IdentityAuthPassword) {
				ui.Field(c, m.VaultIdentityKey(), func() {
					ui.RadioGroup(c, func() {
						for _, key := range s.Keys() {
							ui.Radio(c, &form.KeyID, string(key.ID), key.Label)
						}
					})
				}).Description(m.VaultIdentityKeyHint())
			}
			if form.AuthMethod == string(domainvault.IdentityAuthPassword) {
				passwordHint := m.VaultHostPasswordHint()
				if form.HasPassword {
					passwordHint = m.VaultHostPasswordKeep()
				}
				ui.Field(c, m.VaultHostPassword(), func() {
					ui.TextInput(c, &form.Password).Password()
				}).Description(passwordHint)
				if form.HasPassword {
					ui.Checkbox(c, &form.ClearPassword, m.VaultIdentityClearPassword())
				}
			}
		})

		ui.Text(c, m.VaultIdentityNote()).FontSize(12).TextColor(t.TextMuted)
	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveIdentity() }, s.CancelIdentityEdit, m)
}

func groupEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.GroupForm()
	title := m.VaultGroupFormCreateTitle()
	if form.OriginalPath != "" {
		title = m.VaultGroupFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultHostGroup(), func() {
				ui.TextInput(c, &form.Path).Placeholder("prod/web")
			}).Description(m.VaultHostGroupHint())
			ui.Field(c, m.VaultHostNotes(), func() {
				ui.TextArea(c, &form.Notes).Height(60)
			})
			ui.Text(c, m.VaultGroupDefaults()).Bold()
			ui.Row(c).Gap(8).Wrap().Children(func() {
				portHint := ""
				if placeholder := s.GroupPortPlaceholder(); placeholder != "" {
					portHint = m.VaultHostPortHint(placeholder)
				}
				portField := ui.Field(c, m.VaultHostPort(), func() {
					// A group inherits its parent groups' port before the
					// protocol default, so the hint follows both.
					ui.TextInput(c, &form.Port).
						Placeholder(s.GroupPortPlaceholder()).Width(120)
				})
				if portHint != "" {
					portField.Description(portHint)
				}
				ui.Field(c, m.VaultHostUsername(), func() {
					ui.TextInput(c, &form.Username).Placeholder("root").Width(200)
				})
				ui.Field(c, m.VaultHostProtocol(), func() {
					if ui.Select(c, &form.Protocol, []string{
						string(domainvault.ProtocolSSH),
						string(domainvault.ProtocolTelnet),
					}).Width(140).Changed() {
						// Telnet does not authenticate over SSH; clear the
						// defaults an invisible control selected.
						s.SetGroupProtocol(form.Protocol)
						c.Invalidate()
					}
				})
			})
			if domainvault.Protocol(form.Protocol).UsesSSHAuth() {
				ui.Field(c, m.VaultHostAuthMethod(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.AuthMethod, "", m.VaultNone())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthAuto), m.VaultHostAuthAuto())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthPassword), m.VaultHostAuthPassword())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthKey), m.VaultHostAuthKey())
						ui.Radio(c, &form.AuthMethod, string(domainvault.HostAuthCertificate), m.VaultHostAuthCertificate())
					})
				})
				ui.Field(c, m.VaultHostIdentity(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.IdentityID, "", m.VaultNone())
						for _, identity := range s.Identities() {
							ui.Radio(c, &form.IdentityID, string(identity.ID), identity.Label)
						}
					})
				})
				ui.Field(c, m.VaultHostKey(), func() {
					ui.RadioGroup(c, func() {
						ui.Radio(c, &form.KeyID, "", m.VaultNone())
						for _, key := range s.Keys() {
							ui.Radio(c, &form.KeyID, string(key.ID), key.Label)
						}
					})
				})
			}
			ui.Field(c, m.VaultHostProxyProfile(), func() {
				ui.RadioGroup(c, func() {
					ui.Radio(c, &form.ProxyProfileID, "", m.VaultNone())
					for _, profile := range s.ProxyProfiles() {
						ui.Radio(c, &form.ProxyProfileID, profile.ID, profile.Label)
					}
				})
			})
			if domainvault.Protocol(form.Protocol).UsesSSHAuth() {
				ui.Checkbox(c, &form.UseSSHAgent, m.VaultHostUseAgent())
				if form.UseSSHAgent {
					securityKeyStatus(c, s, m, t)
				}
				ui.Checkbox(c, &form.RequiresMFA, m.VaultHostRequiresMFA())
			}
		})

	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveGroup() }, s.CancelGroupEdit, m)
}

func proxyEditor(c *ui.Context, s *vaultstate.State, m locales.Messages, t *ui.Theme, scroll *ui.ScrollState) {
	form := s.ProxyForm()
	title := m.VaultProxyFormCreateTitle()
	if form.ID != "" {
		title = m.VaultProxyFormEditTitle()
	}

	ui.Scroll(c).Grow(1).FillWidth().TrackScroll(scroll).Children(func() {
		ui.Text(c, title).FontSize(20).Bold()

		ui.Form(c, func() {
			ui.Field(c, m.VaultHostLabel(), func() {
				ui.TextInput(c, &form.Label)
			})
			ui.Field(c, m.VaultProxyType(), func() {
				ui.Select(c, &form.Type, []string{
					string(domainvault.ProxyCommand),
					string(domainvault.ProxyHTTP),
					string(domainvault.ProxySOCKS5),
				}).Width(160)
			})
			switch domainvault.ProxyType(form.Type) {
			case domainvault.ProxyCommand:
				ui.Field(c, m.VaultProxyCommand(), func() {
					ui.TextInput(c, &form.Command).Placeholder("ssh -W %h:%p jump")
				})
			default:
				ui.Row(c).Gap(8).Wrap().Children(func() {
					ui.Field(c, m.VaultProxyHost(), func() {
						ui.TextInput(c, &form.Host).Placeholder("proxy.example.com")
					})
					ui.Field(c, m.VaultHostPort(), func() {
						ui.TextInput(c, &form.Port).Placeholder("1080").Width(120)
					})
					ui.Field(c, m.VaultProxyUsername(), func() {
						ui.TextInput(c, &form.Username).Width(180)
					})
				})
			}
		})

	})

	problem(c, s, t)
	editorActions(c, func() { _ = s.SaveProxyProfile() }, s.CancelProxyEdit, m)
}

// problem renders the current problem's localized message. An unknown problem
// renders nothing, so the UI never shows an untranslated token.
func problem(c *ui.Context, s *vaultstate.State, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), s.Problem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(uitheme.DangerText(t))
}

func problemText(m locales.Messages, problem vaultstate.Problem) string {
	switch problem {
	case vaultstate.ProblemHostnameRequired:
		return m.VaultProblemHostnameRequired()
	case vaultstate.ProblemHostnameInvalid:
		return m.VaultProblemHostnameInvalid()
	case vaultstate.ProblemLabelRequired:
		return m.VaultProblemLabelRequired()
	case vaultstate.ProblemLabelInvalid:
		return m.VaultProblemLabelInvalid()
	case vaultstate.ProblemPortInvalid:
		return m.VaultProblemPortInvalid()
	case vaultstate.ProblemPortRange:
		return m.VaultProblemPortRange()
	case vaultstate.ProblemProtocol:
		return m.VaultProblemProtocol()
	case vaultstate.ProblemUsername:
		return m.VaultProblemUsername()
	case vaultstate.ProblemGroup:
		return m.VaultProblemGroup()
	case vaultstate.ProblemTags:
		return m.VaultProblemTags()
	case vaultstate.ProblemNotes:
		return m.VaultProblemNotes()
	case vaultstate.ProblemDuplicate:
		return m.VaultProblemDuplicate()
	case vaultstate.ProblemNotFound:
		return m.VaultProblemNotFound()
	case vaultstate.ProblemInternal:
		return m.VaultProblemInternal()
	case vaultstate.ProblemSecretField:
		return m.VaultProblemSecretField()
	case vaultstate.ProblemSecretsUnavailable:
		return m.VaultProblemSecretsUnavailable()
	case vaultstate.ProblemSecretsLocked:
		return m.VaultProblemSecretsLocked()
	case vaultstate.ProblemSecretWrongPassphrase:
		return m.VaultProblemSecretWrongPassphrase()
	case vaultstate.ProblemSecretPassphraseRequired:
		return m.VaultProblemSecretPassphraseRequired()
	case vaultstate.ProblemSecretPassphraseMismatch:
		return m.VaultProblemSecretPassphraseMismatch()
	case vaultstate.ProblemSecretUnreadable:
		return m.VaultProblemSecretUnreadable()
	case vaultstate.ProblemSecretSetupFailed:
		return m.VaultProblemSecretSetupFailed()
	case vaultstate.ProblemKeyRequired:
		return m.VaultProblemKeyRequired()
	case vaultstate.ProblemKeyMaterial:
		return m.VaultProblemKeyMaterial()
	case vaultstate.ProblemKeyPassphrase:
		return m.VaultProblemKeyPassphrase()
	case vaultstate.ProblemKeyCertificate:
		return m.VaultProblemKeyCertificate()
	case vaultstate.ProblemKeyUnsupported:
		return m.VaultProblemKeyUnsupported()
	case vaultstate.ProblemIdentityKeyRequired:
		return m.VaultProblemIdentityKeyRequired()
	case vaultstate.ProblemIdentityUsernameRequired:
		return m.VaultProblemIdentityUsernameRequired()
	case vaultstate.ProblemAuthMethod:
		return m.VaultProblemAuthMethod()
	case vaultstate.ProblemProxy:
		return m.VaultProblemProxy()
	case vaultstate.ProblemAlgorithms:
		return m.VaultProblemAlgorithms()
	case vaultstate.ProblemX11:
		return m.VaultProblemX11()
	case vaultstate.ProblemX11Acknowledged:
		return m.VaultProblemX11Acknowledged()
	case vaultstate.ProblemFileProtocol:
		return m.VaultProblemFileProtocol()
	case vaultstate.ProblemSudoUnsupported:
		return m.VaultProblemSudoUnsupported()
	case vaultstate.ProblemETPort:
		return m.VaultProblemETPort()
	case vaultstate.ProblemSnippet:
		return m.VaultSnippetProblem()
	case vaultstate.ProblemSnippetPackage:
		return m.VaultSnippetProblemPackage()
	case vaultstate.ProblemSnippetMissingVariables:
		return m.VaultSnippetProblemMissingVariables()
	case vaultstate.ProblemSnippetTargets:
		return m.VaultSnippetProblemTargets()
	case vaultstate.ProblemSnippetNoSession:
		return m.VaultSnippetProblemNoSession()
	case vaultstate.ProblemSnippetRunFailed:
		return m.VaultSnippetProblemRun()
	case vaultstate.ProblemNote:
		return m.VaultNoteProblem()
	case vaultstate.ProblemNoteContent:
		return m.VaultNoteProblemContent()
	case vaultstate.ProblemNoteGroup:
		return m.VaultNoteProblemGroup()
	case vaultstate.ProblemImportFile:
		return m.VaultProblemImportFile()
	case vaultstate.ProblemImportParse:
		return m.VaultProblemImportParse()
	case vaultstate.ProblemImportStore:
		return m.VaultProblemImportStore()
	case vaultstate.ProblemImportUnavailable:
		return m.VaultProblemImportUnavailable()
	case vaultstate.ProblemExportUnavailable:
		return m.VaultProblemExportUnavailable()
	case vaultstate.ProblemExportWriter:
		return m.VaultProblemExportWriter()
	case vaultstate.ProblemExportFormat:
		return m.VaultProblemExportFormat()
	case vaultstate.ProblemExportUnconfirmed:
		return m.VaultProblemExportUnconfirmed()
	case vaultstate.ProblemExportNoHost:
		return m.VaultProblemExportNoHost()
	case vaultstate.ProblemBackupList:
		return m.VaultBackupProblemList()
	case vaultstate.ProblemBackupRetention:
		return m.VaultBackupProblemRetention()
	case vaultstate.ProblemBackupRetentionInvalid:
		return m.VaultBackupRetentionInvalid(domainbackup.MinRetention, domainbackup.MaxRetention)
	case vaultstate.ProblemBackupRestore:
		return m.VaultBackupProblemRestore()
	case vaultstate.ProblemBackupProtective:
		return m.VaultBackupProblemProtective()
	case vaultstate.ProblemBackupCorrupt:
		return m.VaultBackupProblemCorrupt()
	case vaultstate.ProblemBackupUnavailable:
		return m.VaultBackupUnavailable()
	case vaultstate.ProblemBackupClear:
		return m.VaultBackupProblemClear()
	}
	return ""
}
