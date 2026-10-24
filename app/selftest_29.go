//go:build selftest

package app

// Ticket 29 self-test. Press F11, then 2, then 9. The check proves search, the
// Quick Switcher, and the command palette through the real views, state, and
// dispatch path:
//
//   - Vault search: one field finds a Host by label and a Vault Note by its
//     body, the body match joins the Knowledge section, Escape restores the
//     unfiltered list, and Enter opens the matching editor.
//   - Quick Switcher: Ctrl+J's action opens the overlay from the binding
//     dispatcher, hosts and Sessions render in their sections, typing filters,
//     Enter on a Host opens its Session, and Enter on a Session focuses its
//     Tab.
//   - Command palette: Ctrl+K's action opens the palette, every presentable
//     registered command appears in its localized category with its effective
//     chord, the synthetic locale commands stay out, and Enter runs the
//     command through the registry.
//
// The Host connection is asynchronous in production, so the step waits on the
// new Tab with a hard deadline instead of a fixed delay.
//
// Every temporary service lives in its own directory, removed at the end of
// its step, so F11,2,9 can run twice in one process.
//
// Result: %TEMP%\natcatty-selftest-29.json.

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/command"
	appsearch "github.com/mslxl/natcatty/application/search"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uisearch "github.com/mslxl/natcatty/ui/search"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const selftest29Ticket = 29

type selftest29 struct {
	switcher *selftest29Switcher
	deadline time.Time
}

func (t *selftest29) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest29Ticket,
		Name:   "Search, Quick Switcher, and command palette",
		Steps: []selftest.Step{
			{Name: "vault-search-hosts-notes-knowledge", Run: t.vaultStep},
			{Name: "quick-switcher-opens-targets", Run: t.switcherStep},
			{Name: "command-palette-lists-and-runs", Run: t.paletteStep},
		},
	}
}

// selftest29Hosts is the Vault's Host port over a fixed list.
type selftest29Hosts struct{ items []domainvault.Host }

func (h *selftest29Hosts) List(context.Context) ([]domainvault.Host, error) {
	return append([]domainvault.Host(nil), h.items...), nil
}
func (h *selftest29Hosts) Create(context.Context, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (h *selftest29Hosts) Update(context.Context, model.HostID, domainvault.Draft) (domainvault.Host, error) {
	return domainvault.Host{}, nil
}
func (h *selftest29Hosts) Delete(context.Context, model.HostID) error { return nil }

// selftest29Notes is the Vault Note repository port over fixed lists.
type selftest29Notes struct {
	notes  []domainvault.Note
	groups []domainvault.NoteGroup
}

func (n *selftest29Notes) ListNotes(context.Context) ([]domainvault.Note, error) {
	return append([]domainvault.Note(nil), n.notes...), nil
}

func (n *selftest29Notes) GetNote(_ context.Context, id string) (domainvault.Note, error) {
	for _, note := range n.notes {
		if note.ID == id {
			return note, nil
		}
	}
	return domainvault.Note{}, appvault.ErrNotFound
}

func (n *selftest29Notes) SaveNote(_ context.Context, note domainvault.Note) error {
	for index := range n.notes {
		if n.notes[index].ID == note.ID {
			n.notes[index] = note
			return nil
		}
	}
	n.notes = append(n.notes, note)
	return nil
}

func (n *selftest29Notes) DeleteNote(_ context.Context, id string) error {
	for index := range n.notes {
		if n.notes[index].ID == id {
			n.notes = append(n.notes[:index], n.notes[index+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func (n *selftest29Notes) ListNoteGroups(context.Context) ([]domainvault.NoteGroup, error) {
	return append([]domainvault.NoteGroup(nil), n.groups...), nil
}

func (n *selftest29Notes) SaveNoteGroup(_ context.Context, group domainvault.NoteGroup) error {
	for index := range n.groups {
		if n.groups[index].Path == group.Path {
			n.groups[index] = group
			return nil
		}
	}
	n.groups = append(n.groups, group)
	return nil
}

func (n *selftest29Notes) DeleteNoteGroup(_ context.Context, path string) error {
	for index := range n.groups {
		if n.groups[index].Path == path {
			n.groups = append(n.groups[:index], n.groups[index+1:]...)
			return nil
		}
	}
	return appvault.ErrNotFound
}

// vaultStep drives the Vault's own search field: it ranks, separates a Note's
// body match into Knowledge, clears with Escape, and opens the chosen Host.
func (t *selftest29) vaultStep(*ui.Context) selftest.Outcome {
	notes := &selftest29Notes{
		notes: []domainvault.Note{
			{ID: "n1", Title: "Firewall Runbook",
				Content: "Open the firewall before deploying.", Group: "ops"},
			{ID: "n2", Title: "Onboarding", Content: "Read the Runbook first."},
		},
		groups: []domainvault.NoteGroup{{Path: "ops"}},
	}
	service := appvault.NewService(nil, appvault.WithNotes(notes))
	vault := vaultstate.NewState(&selftest29Hosts{items: []domainvault.Host{
		{ID: "h1", Label: "prod-db", Hostname: "10.0.0.1", Username: "postgres",
			Group: "prod", Tags: []string{"database"}},
		{ID: "h2", Label: "staging", Hostname: "prod-db.example.com"},
	}}, vaultstate.WithNotes(service))
	if err := vault.Reload(); err != nil {
		return selftest.Failed(fmt.Errorf("the Vault did not load: %v", err))
	}

	tester := ui.NewTester(vaultui.View(vault), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.VaultSearchPlaceholder()); err != nil {
		return selftest.Failed(fmt.Errorf("the Vault search field has no accessible name: %v", err))
	}
	tester.Type("deploying")
	tester.Frame()
	if !tester.HasText(m.SearchSectionKnowledge()) {
		return selftest.Failed(fmt.Errorf("a Note body match did not join Knowledge: %v",
			tester.Texts()))
	}
	if !tester.HasText("Firewall Runbook") {
		return selftest.Failed(fmt.Errorf("the matching Note is missing: %v", tester.Texts()))
	}

	// Escape clears the query and restores the unfiltered list.
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if !tester.HasText(m.VaultHostListTitle(2)) {
		return selftest.Failed(fmt.Errorf("Escape did not restore the Host list: %v",
			tester.Texts()))
	}

	if err := tester.Click(m.VaultSearchPlaceholder()); err != nil {
		return selftest.Failed(fmt.Errorf("the Vault search field lost its name: %v", err))
	}
	tester.Type("prod-db")
	tester.Frame()
	if !tester.HasText(m.SearchSectionHost()) {
		return selftest.Failed(fmt.Errorf("the Host label did not rank: %v", tester.Texts()))
	}
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if !vault.Editing() || vault.Form().ID != model.HostID("h1") {
		return selftest.Failed(fmt.Errorf("Enter did not open the label match: editing=%v id=%q",
			vault.Editing(), vault.Form().ID))
	}
	return selftest.Passed(
		"body match joined Knowledge, Escape restored the list, and Enter opened the label match",
		"vaultui.View + vaultstate.State + appvault.Service")
}

// selftest29Switcher is the Quick Switcher graph one step builds.
type selftest29Switcher struct {
	vault    *vaultstate.State
	shell    *state.State
	search   *uisearch.State
	tester   *ui.Tester
	sessions *appsession.Service
}

func (s *selftest29Switcher) close() {
	if s.shell != nil {
		s.shell.CloseAllSessions()
	}
	if s.sessions != nil {
		s.sessions.CloseAll()
	}
}

func selftest29NewSwitcher() (*selftest29Switcher, error) {
	vault := vaultstate.NewState(&selftest29Hosts{items: []domainvault.Host{
		{ID: "h1", Label: "prod-db", Hostname: "10.0.0.1"},
		{ID: "h2", Label: "staging", Hostname: "staging.example.com"},
	}})
	if err := vault.Reload(); err != nil {
		return nil, fmt.Errorf("the Vault did not load: %v", err)
	}
	sessions := appsession.NewService(&selftest29Factory{transport: newSelftest29Transport()})
	shell := state.NewState(state.WithSessions(sessions,
		func(*appsession.Session) (state.Surface, error) { return selftest29Surface{}, nil }))
	if err := shell.NewTab(); err != nil {
		sessions.CloseAll()
		return nil, fmt.Errorf("the local Session did not open: %v", err)
	}

	source := &palette{vault: vault, shell: func() *state.State { return shell }}
	search := uisearch.New(
		uisearch.WithSource(source.records),
		uisearch.WithOpen(source.open),
		uisearch.WithSection(searchSection),
	)
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { ui.Text(c, "shell") })
		uisearch.Overlay(search)(c)
	}, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	return &selftest29Switcher{
		vault: vault, shell: shell, search: search, tester: tester, sessions: sessions,
	}, nil
}

// switcherStep opens the Quick Switcher through the real binding action,
// filters to a Host, waits for its Session, then focuses a Session by keyboard.
func (t *selftest29) switcherStep(*ui.Context) selftest.Outcome {
	if t.switcher == nil {
		switcher, err := selftest29NewSwitcher()
		if err != nil {
			return selftest.Failed(err)
		}
		t.switcher = switcher
		t.deadline = time.Now().Add(10 * time.Second)

		action, ok := (&bindingDispatcher{
			search: switcher.search, shell: switcher.shell, focusMain: func() {},
		}).action("quick-switch")
		if !ok {
			switcher.close()
			return selftest.Failed(fmt.Errorf("the quick-switch binding has no action"))
		}
		action(0)
		if !switcher.search.IsOpen() || switcher.search.Mode() != uisearch.ModeQuickSwitcher {
			switcher.close()
			return selftest.Failed(fmt.Errorf("Ctrl+J's action did not open the Quick Switcher"))
		}
		switcher.tester.Frame()

		m := locales.In("en")
		for _, want := range []string{
			m.SearchJumpTo(), m.SearchSectionHost(), m.SearchSectionSession(),
			"prod-db", "Local Session 1",
		} {
			if !switcher.tester.HasText(want) {
				switcher.close()
				return selftest.Failed(fmt.Errorf("%q is missing from the Quick Switcher: %v",
					want, switcher.tester.Texts()))
			}
		}

		switcher.tester.Type("staging")
		switcher.tester.Frame()
		if switcher.tester.HasText("Local Session 1") {
			switcher.close()
			return selftest.Failed(fmt.Errorf("typing did not filter the Sessions: %v",
				switcher.tester.Texts()))
		}
		switcher.tester.Key(0, ui.KeyEnter)
		switcher.tester.Frame()
		if switcher.search.IsOpen() {
			switcher.close()
			return selftest.Failed(fmt.Errorf("the Quick Switcher stayed open after Enter"))
		}
		return selftest.Pending
	}

	switcher := t.switcher
	switcher.shell.Flush()
	switcher.tester.Frame()
	if switcher.shell.TabCount() < 2 {
		if time.Now().After(t.deadline) {
			switcher.close()
			t.switcher = nil
			return selftest.Failed(fmt.Errorf("the chosen Host's Session did not open"))
		}
		return selftest.Pending
	}

	// The Host Session is the second Tab. Focus the other one through the
	// Quick Switcher's keyboard, the way a user would.
	before := switcher.shell.ActiveTab()
	if before != 1 {
		switcher.close()
		t.switcher = nil
		return selftest.Failed(fmt.Errorf("the Host Session did not become active: active=%d", before))
	}
	action, ok := (&bindingDispatcher{
		search: switcher.search, shell: switcher.shell, focusMain: func() {},
	}).action("quick-switch")
	if !ok {
		switcher.close()
		t.switcher = nil
		return selftest.Failed(fmt.Errorf("the quick-switch binding lost its action"))
	}
	action(0)
	switcher.tester.Frame()

	// Two Hosts lead the list; the Sessions follow in Tab order.
	target := 2
	if before == 0 {
		target = 3
	}
	for step := 0; step < target; step++ {
		switcher.tester.Key(0, ui.KeyDown)
	}
	switcher.tester.Frame()
	switcher.tester.Key(0, ui.KeyEnter)
	switcher.tester.Frame()
	active := switcher.shell.ActiveTab()
	if active != 1-before {
		switcher.close()
		t.switcher = nil
		return selftest.Failed(fmt.Errorf("Enter focused Tab %d, want %d", active, 1-before))
	}
	if switcher.search.IsOpen() {
		switcher.close()
		t.switcher = nil
		return selftest.Failed(fmt.Errorf("the Quick Switcher stayed open after focusing a Session"))
	}
	switcher.close()
	t.switcher = nil
	return selftest.Passed(
		"Ctrl+J opened the sections, typing filtered, Enter opened the Host's Session and focused a Tab",
		"uisearch.State + bindingDispatcher.action + shell.State.TabSummaries")
}

// paletteStep proves the palette lists every presentable command with its
// chord, follows the locale, excludes synthetic commands, and runs the chosen
// one through the registry.
func (t *selftest29) paletteStep(*ui.Context) selftest.Outcome {
	dir, err := os.MkdirTemp("", "natcatty-selftest-29-palette-")
	if err != nil {
		return selftest.Failed(err)
	}
	defer os.RemoveAll(dir)

	dispatched := ""
	registry := command.NewRegistry()
	for id := range map[command.ID]bool{
		command.NewTab: true, command.OpenSettings: true, command.SplitPaneRight: true,
	} {
		id := id
		if err := registry.Register(id, func(context.Context) error {
			dispatched = string(id)
			return nil
		}); err != nil {
			return selftest.Failed(fmt.Errorf("register %s: %v", id, err))
		}
	}
	if err := registry.Register(command.LocaleCommandID("en"),
		func(context.Context) error { return nil }); err != nil {
		return selftest.Failed(fmt.Errorf("register the locale command: %v", err))
	}

	source := &palette{
		registry: func() *command.Registry { return registry },
		bindings: selftest27Bindings(dir),
	}
	records := source.records(uisearch.ModeCommandPalette, "en")
	if len(records) != 3 {
		return selftest.Failed(fmt.Errorf("the palette listed %d commands, want the 3 presentable ones",
			len(records)))
	}
	for _, record := range records {
		if record.Title == "" || record.Group == "" {
			return selftest.Failed(fmt.Errorf("%s has no title or category", record.ID))
		}
	}
	if title := selftest29TitleOf(records, command.NewTab); title != "New Tab" {
		return selftest.Failed(fmt.Errorf("the English New Tab title = %q", title))
	}
	if shortcut := selftest29ShortcutOf(records, command.NewTab); shortcut == "" ||
		strings.Contains(shortcut, " + ") {
		return selftest.Failed(fmt.Errorf("the New Tab chord = %q", shortcut))
	}

	chinese := source.records(uisearch.ModeCommandPalette, "zh-CN")
	if title := selftest29TitleOf(chinese, command.NewTab); title != "新建标签页" {
		return selftest.Failed(fmt.Errorf("the Chinese New Tab title = %q", title))
	}
	if results := appsearch.Rank("preferences", chinese); len(results) != 1 ||
		results[0].Record.ID != string(command.OpenSettings) {
		return selftest.Failed(fmt.Errorf("the English keyword did not find Settings: %v", results))
	}

	shell := state.NewState()
	search := uisearch.New(
		uisearch.WithSource(source.records),
		uisearch.WithOpen(source.open),
		uisearch.WithSection(searchSection),
	)
	action, ok := (&bindingDispatcher{
		search: search, shell: shell, focusMain: func() {},
	}).action("command-palette")
	if !ok {
		return selftest.Failed(fmt.Errorf("the command-palette binding has no action"))
	}
	action(0)
	if !search.IsOpen() || search.Mode() != uisearch.ModeCommandPalette {
		return selftest.Failed(fmt.Errorf("Ctrl+K's action did not open the command palette"))
	}

	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { ui.Text(c, "shell") })
		uisearch.Overlay(search)(c)
	}, 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	for _, want := range []string{
		m.SearchCategoryApp(), m.SearchCategoryTab(), "New Tab", m.NavSettings(),
	} {
		if !tester.HasText(want) {
			return selftest.Failed(fmt.Errorf("%q is missing from the palette: %v",
				want, tester.Texts()))
		}
	}
	tester.Type("settings")
	tester.Frame()
	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if dispatched != string(command.OpenSettings) {
		return selftest.Failed(fmt.Errorf("Enter dispatched %q, want %q", dispatched,
			command.OpenSettings))
	}
	if search.IsOpen() {
		return selftest.Failed(fmt.Errorf("the command palette stayed open after Enter"))
	}
	return selftest.Passed(
		"Ctrl+K opened 3 localized categories with chords; the synthetic locale command stayed out; Enter ran Settings",
		"command.Registry + appkeybindings.Service + uisearch.State")
}

func selftest29TitleOf(records []appsearch.Record, id command.ID) string {
	for _, record := range records {
		if record.ID == string(id) {
			return record.Title
		}
	}
	return ""
}

func selftest29ShortcutOf(records []appsearch.Record, id command.ID) string {
	for _, record := range records {
		if record.ID == string(id) {
			return record.Shortcut
		}
	}
	return ""
}

// selftest29Factory opens an in-memory transport, so a Session exists without
// a network or a process.
type selftest29Factory struct {
	transport *selftest29Transport
}

func (f *selftest29Factory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

type selftest29Transport struct {
	closed chan struct{}
	// mu guards once: the Session's read loop and the check can both close the
	// transport, and a double close panics.
	mu   sync.Mutex
	once bool
}

func newSelftest29Transport() *selftest29Transport {
	return &selftest29Transport{closed: make(chan struct{})}
}

func (f *selftest29Transport) Read([]byte) (int, error) {
	<-f.closed
	return 0, io.EOF
}
func (f *selftest29Transport) Write(p []byte) (int, error) { return len(p), nil }
func (f *selftest29Transport) Resize(int, int) error       { return nil }
func (f *selftest29Transport) Wait() (int, error)          { return 0, nil }
func (f *selftest29Transport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.once {
		f.once = true
		close(f.closed)
	}
	return nil
}

type selftest29Surface struct{}

func (selftest29Surface) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal").Fill() }
func (selftest29Surface) Close()                        {}
