package app

import (
	"context"
	"strconv"
	"strings"

	"github.com/mslxl/natcatty/application/command"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	appsearch "github.com/mslxl/natcatty/application/search"
	domain "github.com/mslxl/natcatty/domain/keybindings"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	shellui "github.com/mslxl/natcatty/ui/shell"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// palette feeds the Quick Switcher and the command palette. It reads live
// state on every frame, so a Host added or a Tab opened appears without the
// app rebuilding the overlay.
type palette struct {
	vault    *vaultstate.State
	shell    func() *state.State
	registry func() *command.Registry
	bindings *appkeybindings.Service
}

// records returns the source for one mode. The Quick Switcher lists every
// Host and every open Session; the palette lists the registered commands.
func (p *palette) records(mode uisearch.Mode, locale string) []appsearch.Record {
	m := locales.In(locale)
	switch mode {
	case uisearch.ModeCommandPalette:
		return p.commands(m)
	case uisearch.ModeQuickSwitcher:
		return p.switcher(m)
	default:
		return nil
	}
}

// open runs the chosen record. A Host connects in a new Tab, a Session
// focuses its Tab, and a command dispatches through the one registry that the
// menus also use.
func (p *palette) open(_ uisearch.Mode, result appsearch.Result) bool {
	switch result.Record.Kind {
	case appsearch.KindHost:
		host, ok := p.host(result.Record.ID)
		if !ok {
			return false
		}
		shell := p.currentShell()
		if shell == nil {
			return false
		}
		return shell.OpenHost(host) == nil
	case appsearch.KindSession:
		index, err := strconv.Atoi(strings.TrimPrefix(result.Record.ID, "tab:"))
		if err != nil {
			return false
		}
		shell := p.currentShell()
		if shell == nil {
			return false
		}
		shell.SelectTab(index)
		shell.RequestRedraw()
		return true
	case appsearch.KindCommand:
		registry := p.registry()
		if registry == nil {
			return false
		}
		return registry.Dispatch(context.Background(), command.ID(result.Record.ID)) == nil
	default:
		return false
	}
}

func (p *palette) currentShell() *state.State {
	if p.shell == nil {
		return nil
	}
	return p.shell()
}

func (p *palette) host(id string) (domainvault.Host, bool) {
	if p.vault == nil {
		return domainvault.Host{}, false
	}
	for _, candidate := range p.vault.Items() {
		if string(candidate.ID) == id {
			return candidate, true
		}
	}
	return domainvault.Host{}, false
}

// switcher builds Hosts first, then Sessions, in the Vault's own order. The
// empty query keeps that order, as the original Quick Switcher did; typing
// ranks inside each section.
func (p *palette) switcher(m locales.Messages) []appsearch.Record {
	records := make([]appsearch.Record, 0, 32)
	if p.vault != nil {
		for _, host := range p.vault.Items() {
			fields := []appsearch.Field{
				{Text: host.Label, Weight: appsearch.WeightLabel},
				{Text: host.Hostname, Weight: appsearch.WeightHostname},
			}
			if host.Group != "" {
				fields = append(fields, appsearch.Field{Text: host.Group, Weight: appsearch.WeightGroup})
			}
			for _, tag := range host.Tags {
				fields = append(fields, appsearch.Field{Text: tag, Weight: appsearch.WeightTag})
			}
			records = append(records, appsearch.Record{
				Kind:     appsearch.KindHost,
				ID:       string(host.ID),
				Title:    host.Label,
				Subtitle: host.Hostname,
				Fields:   fields,
			})
		}
	}
	shell := p.currentShell()
	if shell == nil {
		return records
	}
	for _, summary := range shell.TabSummaries() {
		if summary.Detached || summary.TabIndex < 0 {
			continue
		}
		title := shellui.TabTitle(m, summary)
		records = append(records, appsearch.Record{
			Kind:     appsearch.KindSession,
			ID:       "tab:" + strconv.Itoa(summary.TabIndex),
			Title:    title,
			Subtitle: "#" + strconv.Itoa(summary.Number),
			Fields: []appsearch.Field{
				{Text: title, Weight: appsearch.WeightTitle},
				{Text: "tab " + strconv.Itoa(summary.Number), Weight: appsearch.WeightKeyword},
			},
		})
	}
	return records
}

// searchSection groups palette records by command category and every other
// surface's records by kind, so the view localizes one token either way.
func searchSection(record appsearch.Record) string {
	if record.Kind == appsearch.KindCommand {
		return record.Group
	}
	return string(record.Kind)
}

// commandSpec is a command's user-visible presentation.
type commandSpec struct {
	title    string
	category string
	// binding is the catalog binding whose effective chord the palette shows,
	// or "" when the command has no single chord.
	binding  string
	keywords []string
}

// commandPresentation names a registered command. A false result keeps a
// command out of the palette: the locale commands are synthetic, and a new
// command without a label must not appear as its raw ID.
func commandPresentation(m locales.Messages, id command.ID) (commandSpec, bool) {
	switch id {
	case command.Quit:
		return commandSpec{title: m.MenuQuit(), category: uisearch.CategoryApp,
			keywords: []string{"quit", "exit", "close"}}, true
	case command.LockApp:
		return commandSpec{title: m.MenuLockApp(), category: uisearch.CategoryApp,
			keywords: []string{"lock", "app", "security"}}, true
	case command.OpenSettings:
		return commandSpec{title: m.NavSettings(), category: uisearch.CategoryApp,
			binding: "open-settings", keywords: []string{"settings", "preferences"}}, true
	case command.OpenMonitor:
		return commandSpec{title: m.NavMonitor(), category: uisearch.CategoryApp,
			keywords: []string{"monitoring", "monitor", "metrics", "cpu", "memory", "disk"}}, true
	case command.OpenSystemManager:
		return commandSpec{title: m.NavSysManager(), category: uisearch.CategoryApp,
			keywords: []string{"system", "processes", "ports", "services", "systemctl", "kill"}}, true
	case command.NewTab:
		return commandSpec{title: m.MenuNewTab(), category: uisearch.CategoryTab,
			binding: "new-tab", keywords: []string{"new", "tab", "terminal", "local"}}, true
	case command.ConnectSerial:
		return commandSpec{title: m.MenuSerial(), category: uisearch.CategoryTab,
			keywords: []string{"serial", "port", "device", "connect"}}, true
	case command.CloseTab:
		return commandSpec{title: m.MenuCloseTab(), category: uisearch.CategoryTab,
			binding: "close-tab", keywords: []string{"close", "tab"}}, true
	case command.CloseOtherTabs:
		return commandSpec{title: m.TabCloseOthers(), category: uisearch.CategoryTab,
			keywords: []string{"close", "other", "tabs"}}, true
	case command.CloseTabsRight:
		return commandSpec{title: m.TabCloseRight(), category: uisearch.CategoryTab,
			keywords: []string{"close", "tabs", "right"}}, true
	case command.DuplicateTab:
		return commandSpec{title: m.TabDuplicate(), category: uisearch.CategoryTab,
			keywords: []string{"duplicate", "clone", "tab"}}, true
	case command.DetachTab:
		return commandSpec{title: m.TabDetach(), category: uisearch.CategoryTab,
			keywords: []string{"detach", "tab", "window"}}, true
	case command.CopyTabToNewWindow:
		return commandSpec{title: m.TabCopyToNewWindow(), category: uisearch.CategoryTab,
			keywords: []string{"copy", "tab", "new", "window"}}, true
	case command.SplitPaneRight:
		return commandSpec{title: m.PaneSplitRight(), category: uisearch.CategoryPane,
			binding: "split-vertical", keywords: []string{"split", "right", "vertical", "pane"}}, true
	case command.SplitPaneDown:
		return commandSpec{title: m.PaneSplitDown(), category: uisearch.CategoryPane,
			binding: "split-horizontal", keywords: []string{"split", "down", "horizontal", "pane"}}, true
	case command.ClosePane:
		return commandSpec{title: m.PaneClose(), category: uisearch.CategoryPane,
			binding: "close-session", keywords: []string{"close", "pane", "session"}}, true
	case command.DetachPane:
		return commandSpec{title: m.PaneDetach(), category: uisearch.CategoryPane,
			keywords: []string{"detach", "pane", "session", "window"}}, true
	case command.FocusPaneLeft:
		return commandSpec{title: m.PaneFocusLeft(), category: uisearch.CategoryPane,
			keywords: []string{"focus", "pane", "left", "navigation"}}, true
	case command.FocusPaneRight:
		return commandSpec{title: m.PaneFocusRight(), category: uisearch.CategoryPane,
			keywords: []string{"focus", "pane", "right", "navigation"}}, true
	case command.FocusPaneUp:
		return commandSpec{title: m.PaneFocusUp(), category: uisearch.CategoryPane,
			keywords: []string{"focus", "pane", "up", "navigation"}}, true
	case command.FocusPaneDown:
		return commandSpec{title: m.PaneFocusDown(), category: uisearch.CategoryPane,
			keywords: []string{"focus", "pane", "down", "navigation"}}, true
	case command.OpenBackups:
		return commandSpec{title: m.VaultBackupTitle(), category: uisearch.CategoryVault,
			keywords: []string{"backups", "backup", "restore", "vault"}}, true
	default:
		return commandSpec{}, false
	}
}

// commandCategories is the palette's section order: the application first,
// then the Tab and Pane commands, then the Vault's. Sections keep the order
// their records first appear in, so the source fixes this order instead of
// leaving it to the lexical order of command IDs.
var commandCategories = []string{
	uisearch.CategoryApp,
	uisearch.CategoryTab,
	uisearch.CategoryPane,
	uisearch.CategoryVault,
}

// commands builds the palette's records. Every registered command with a
// presentation appears; the searchable fields include the raw ID and English
// keywords, so a command is found whatever the UI language.
func (p *palette) commands(m locales.Messages) []appsearch.Record {
	registry := p.registry()
	if registry == nil {
		return nil
	}
	ids := registry.IDs()
	byCategory := make(map[string][]appsearch.Record, len(commandCategories))
	for _, id := range ids {
		spec, ok := commandPresentation(m, id)
		if !ok {
			continue
		}
		fields := []appsearch.Field{
			{Text: spec.title, Weight: appsearch.WeightTitle},
			{Text: string(id), Weight: appsearch.WeightKeyword},
			{Text: uisearch.SectionTitle(m, spec.category), Weight: appsearch.WeightKeyword},
			{Text: strings.Join(spec.keywords, " "), Weight: appsearch.WeightTag},
		}
		byCategory[spec.category] = append(byCategory[spec.category], appsearch.Record{
			Kind:     appsearch.KindCommand,
			ID:       string(id),
			Title:    spec.title,
			Group:    spec.category,
			Shortcut: p.shortcut(spec.binding),
			Fields:   fields,
		})
	}
	records := make([]appsearch.Record, 0, len(ids))
	for _, category := range commandCategories {
		records = append(records, byCategory[category]...)
	}
	return records
}

// shortcut renders the effective chord of a binding, with the catalog's
// spaced notation collapsed to the compact form the palette shows.
func (p *palette) shortcut(id string) string {
	if p.bindings == nil || id == "" {
		return ""
	}
	chord := p.bindings.Chord(id, p.bindings.Scheme())
	if chord == "" || chord == domain.Disabled {
		return ""
	}
	return strings.ReplaceAll(chord, " + ", "+")
}
