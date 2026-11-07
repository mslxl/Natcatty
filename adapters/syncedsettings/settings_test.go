package syncedsettings_test

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	adaptersyncedsettings "github.com/mslxl/natcatty/adapters/syncedsettings"
	appbrowser "github.com/mslxl/natcatty/application/browser"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appthemes "github.com/mslxl/natcatty/application/themes"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

type memoryPreferences struct{ value domainpreferences.Settings }

func (m *memoryPreferences) Load(context.Context) (domainpreferences.Settings, error) {
	return m.value, nil
}
func (m *memoryPreferences) Save(_ context.Context, value domainpreferences.Settings) error {
	m.value = value
	return nil
}

type memoryTerminal struct{ value domainterminal.Settings }

func (m *memoryTerminal) Load(context.Context) (domainterminal.Settings, error) {
	return m.value, nil
}
func (m *memoryTerminal) Save(_ context.Context, value domainterminal.Settings) error {
	m.value = value
	return nil
}

type memoryKeybindings struct{ value appkeybindings.State }

func (m *memoryKeybindings) Load(context.Context) (appkeybindings.State, error) {
	return m.value, nil
}
func (m *memoryKeybindings) Save(_ context.Context, value appkeybindings.State) error {
	m.value = value
	return nil
}

type memoryThemes struct{ value []domaintheme.TerminalTheme }

func (m *memoryThemes) Load(context.Context) ([]domaintheme.TerminalTheme, error) {
	return append([]domaintheme.TerminalTheme(nil), m.value...), nil
}
func (m *memoryThemes) Save(_ context.Context, value []domaintheme.TerminalTheme) error {
	m.value = append([]domaintheme.TerminalTheme(nil), value...)
	return nil
}

type memoryBrowser struct{ value []domainbrowser.Bookmark }

func (m *memoryBrowser) Load() ([]domainbrowser.Bookmark, error) {
	return append([]domainbrowser.Bookmark(nil), m.value...), nil
}
func (m *memoryBrowser) Save(value []domainbrowser.Bookmark) error {
	m.value = append([]domainbrowser.Bookmark(nil), value...)
	return nil
}

type memorySFTP struct{ value []domainsftp.Bookmark }

func (m *memorySFTP) LoadBookmarks(context.Context) ([]domainsftp.Bookmark, error) {
	return append([]domainsftp.Bookmark(nil), m.value...), nil
}
func (m *memorySFTP) SaveBookmarks(_ context.Context, value []domainsftp.Bookmark) error {
	m.value = append([]domainsftp.Bookmark(nil), value...)
	return nil
}

type fixture struct {
	adapter     *adaptersyncedsettings.Settings
	preferences *apppreferences.Service
	terminal    *appterminalsettings.Service
	keybindings *appkeybindings.Service
	themes      *appthemes.Service
	browser     *appbrowser.Bookmarks
	sftp        *memorySFTP
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()

	preferenceStore := &memoryPreferences{value: domainpreferences.Defaults()}
	preferences := apppreferences.NewService(preferenceStore)
	if err := preferences.Load(ctx); err != nil {
		t.Fatal(err)
	}
	terminalStore := &memoryTerminal{value: domainterminal.Defaults()}
	terminal := appterminalsettings.NewService(terminalStore)
	if err := terminal.Load(ctx); err != nil {
		t.Fatal(err)
	}
	keyStore := &memoryKeybindings{value: appkeybindings.State{
		Scheme: domainkeybindings.DefaultScheme("windows"),
	}}
	keybindings := appkeybindings.NewService(keyStore, appkeybindings.WithGOOS("windows"))
	if err := keybindings.Load(ctx); err != nil {
		t.Fatal(err)
	}
	themeStore := &memoryThemes{}
	themes := appthemes.NewService(themeStore)
	if err := themes.Load(ctx); err != nil {
		t.Fatal(err)
	}
	browserStore := &memoryBrowser{}
	browser, err := appbrowser.NewBookmarks(browserStore,
		func() time.Time { return time.UnixMilli(1000) },
		func() string { return "generated-id" })
	if err != nil {
		t.Fatal(err)
	}
	sftp := &memorySFTP{}
	return &fixture{
		adapter:     adaptersyncedsettings.New(preferences, terminal, keybindings, themes, sftp, browser),
		preferences: preferences,
		terminal:    terminal,
		keybindings: keybindings,
		themes:      themes,
		browser:     browser,
		sftp:        sftp,
	}
}

func TestSnapshotAndApplyPreserveDeviceLocalSettings(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	// Local state carries a mix of portable values and device-local ones.
	if err := f.preferences.Update(func(next *domainpreferences.Settings) {
		next.Appearance.AppIcon = "bright"
		next.Appearance.TerminalFontSize = 18
		next.Layout.ShowSFTPTab = true
		next.SFTPEditor.DefaultOpener = domainpreferences.Opener{
			Kind:        domainpreferences.OpenerExternal,
			Application: domainpreferences.ExternalApplication{Path: "/local/editor"},
		}
		next.SFTPEditor.FileAssociations = []domainpreferences.FileAssociation{{
			Extension:   "md",
			Kind:        domainpreferences.OpenerExternal,
			Application: domainpreferences.ExternalApplication{Path: "/local/viewer"},
		}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.terminal.Update(func(next *domainterminal.Settings) {
		next.LogDirectory = "/local/logs"
		next.Encodings = map[string]domainterminal.Encoding{"local": domainterminal.EncodingUTF8}
		next.OSC52 = domainterminal.ClipboardReadWrite
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.keybindings.Replace(appkeybindings.State{
		Scheme:          f.keybindings.Scheme(),
		SuppressedMenus: []string{"local-menu"},
	}); err != nil {
		t.Fatal(err)
	}
	localTheme := customTheme(t, "custom-local", "Local")
	remoteTheme := customTheme(t, "custom-remote", "Remote")
	if err := f.themes.ReplaceCustom([]domaintheme.TerminalTheme{localTheme}); err != nil {
		t.Fatal(err)
	}
	if err := f.browser.Replace([]domainbrowser.Bookmark{{
		ID: "local-bookmark", Title: "Local", URL: "https://local.example",
		Scope: domainbrowser.ScopeAllHosts, IconMode: domainbrowser.IconModeAuto,
		Color: domainbrowser.DefaultBookmarkColor, Order: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	f.sftp.value = []domainsftp.Bookmark{{Path: "/local", Label: "Local"}}

	documents, err := f.adapter.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var portable domainpreferences.PortableSettings
	decode(t, documents[adaptersyncedsettings.KeyPreferences], &portable)
	if portable.Appearance.AppIcon != "" {
		t.Fatalf("snapshot leaked the app icon: %+v", portable.Appearance)
	}
	if portable.Appearance.TerminalFontSize != 18 {
		t.Fatalf("snapshot font size = %v", portable.Appearance.TerminalFontSize)
	}
	if portable.SFTPEditor.DefaultOpener.Kind != "" ||
		len(portable.SFTPEditor.FileAssociations) != 0 {
		t.Fatalf("snapshot leaked the device opener: %+v", portable.SFTPEditor)
	}
	var terminalDoc domainterminal.Settings
	decode(t, documents[adaptersyncedsettings.KeyTerminal], &terminalDoc)
	if terminalDoc.LogDirectory != "" || len(terminalDoc.Encodings) != 0 {
		t.Fatalf("snapshot leaked local terminal state: %+v", terminalDoc)
	}
	if terminalDoc.OSC52 != domainterminal.ClipboardReadWrite {
		t.Fatalf("snapshot OSC52 = %q", terminalDoc.OSC52)
	}

	// Build the remote documents from the portable snapshot, then change one
	// portable value per document and plant device-local values that the
	// download must not adopt.
	remote := map[string]json.RawMessage{}
	for key, value := range documents {
		remote[key] = value
	}
	portable.Appearance.AppIcon = "dark"
	portable.Appearance.TerminalFontSize = 22
	portable.Layout.ShowSFTPTab = false
	portable.SFTPEditor.DefaultOpener = domainpreferences.Opener{
		Kind: domainpreferences.OpenerSystem,
	}
	portable.SFTPEditor.FileAssociations = []domainpreferences.FileAssociation{{
		Extension: "txt",
		Kind:      domainpreferences.OpenerSystem,
	}}
	remote[adaptersyncedsettings.KeyPreferences] = marshal(t, portable)

	terminalDoc.OSC52 = domainterminal.ClipboardOff
	terminalDoc.LogDirectory = "/remote/logs"
	terminalDoc.Encodings = map[string]domainterminal.Encoding{"local": domainterminal.EncodingGB18030}
	remote[adaptersyncedsettings.KeyTerminal] = marshal(t, terminalDoc)
	remote[adaptersyncedsettings.KeyKeybindings] = json.RawMessage(`{"suppressedMenus":["help-menu"]}`)
	remote[adaptersyncedsettings.KeyThemes] = marshal(t, []domaintheme.TerminalTheme{remoteTheme})
	remote[adaptersyncedsettings.KeySFTPBookmarks] = marshal(t, []domainsftp.Bookmark{{
		Path: "/remote", Label: "Remote",
	}})
	remote[adaptersyncedsettings.KeyBrowserBookmarks] = marshal(t, []domainbrowser.Bookmark{{
		ID: "remote-bookmark", Title: "Remote", URL: "https://remote.example",
		Scope: domainbrowser.ScopeAllHosts, IconMode: domainbrowser.IconModeAuto,
		Color: domainbrowser.DefaultBookmarkColor, Order: 1,
	}})

	if err := f.adapter.Apply(ctx, remote); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	preferences := f.preferences.Settings()
	if preferences.Appearance.AppIcon != "bright" {
		t.Fatalf("app icon = %q, want the device-local value", preferences.Appearance.AppIcon)
	}
	if preferences.Appearance.TerminalFontSize != 22 {
		t.Fatalf("font size = %v, want the synced value", preferences.Appearance.TerminalFontSize)
	}
	if preferences.Layout.ShowSFTPTab {
		t.Fatal("layout was not applied")
	}
	if preferences.SFTPEditor.DefaultOpener.Application.Path != "/local/editor" ||
		len(preferences.SFTPEditor.FileAssociations) != 1 ||
		preferences.SFTPEditor.FileAssociations[0].Application.Path != "/local/viewer" {
		t.Fatalf("device opener was overwritten: %+v", preferences.SFTPEditor)
	}

	terminal := f.terminal.Settings()
	if terminal.OSC52 != domainterminal.ClipboardOff {
		t.Fatalf("OSC52 = %q, want the synced value", terminal.OSC52)
	}
	if terminal.LogDirectory != "/local/logs" {
		t.Fatalf("log directory = %q, want the device-local value", terminal.LogDirectory)
	}
	if len(terminal.Encodings) != 1 || terminal.Encodings["local"] != domainterminal.EncodingUTF8 {
		t.Fatalf("encodings = %+v, want the device-local map", terminal.Encodings)
	}

	state := f.keybindings.State()
	if state.Scheme != domainkeybindings.DefaultScheme("windows") {
		t.Fatalf("scheme = %q, want the device-local default", state.Scheme)
	}
	if len(state.SuppressedMenus) != 1 || state.SuppressedMenus[0] != "help-menu" {
		t.Fatalf("suppressed menus = %+v", state.SuppressedMenus)
	}

	themes := f.themes.CustomThemes()
	if len(themes) != 1 || themes[0].ID != remoteTheme.ID {
		t.Fatalf("custom themes = %+v", themes)
	}
	if len(f.sftp.value) != 1 || f.sftp.value[0].Path != "/remote" {
		t.Fatalf("sftp bookmarks = %+v", f.sftp.value)
	}
	bookmarks := f.browser.All()
	if len(bookmarks) != 1 || bookmarks[0].ID != "remote-bookmark" {
		t.Fatalf("browser bookmarks = %+v", bookmarks)
	}
}

func TestApplyRejectsAMalformedDocumentBeforeWritingAny(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	before := f.preferences.Settings().Appearance.TerminalFontSize
	documents := map[string]json.RawMessage{
		adaptersyncedsettings.KeyPreferences:      json.RawMessage(`{"appearance":{"terminalFontSize":30}}`),
		adaptersyncedsettings.KeyBrowserBookmarks: json.RawMessage(`{"not":"an array"}`),
	}
	if err := f.adapter.Apply(ctx, documents); err == nil {
		t.Fatal("Apply accepted a malformed bookmark document")
	}
	if got := f.preferences.Settings().Appearance.TerminalFontSize; got != before {
		t.Fatalf("preferences were written before validation: %v", got)
	}
	if len(f.browser.All()) != 0 {
		t.Fatalf("bookmarks were replaced: %+v", f.browser.All())
	}
}

// A synced terminal document from a build before the capture hook switch has
// no key for it. Downloading that document must leave the portable
// on-by-default setting on, while an explicit false from a newer build still
// syncs.
func TestApplyDefaultsAMissingCaptureHookToOn(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	if err := f.terminal.Update(func(next *domainterminal.Settings) {
		next.HistoryCaptureHookEnabled = true
		next.OSC52 = domainterminal.ClipboardReadWrite
	}); err != nil {
		t.Fatal(err)
	}

	documents := map[string]json.RawMessage{
		adaptersyncedsettings.KeyTerminal: json.RawMessage(`{"osc52":"off"}`),
	}
	if err := f.adapter.Apply(ctx, documents); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := f.terminal.Settings()
	if !got.HistoryCaptureHookEnabled {
		t.Fatal("a document without the capture hook key disabled the hook")
	}
	if got.OSC52 != domainterminal.ClipboardOff {
		t.Fatalf("OSC52 = %q, want the synced value", got.OSC52)
	}

	// Both spellings name the same JSON field: this build marshals its
	// settings with Go field names, while the stored and older synced
	// documents use the lower-camel key.
	for _, document := range []string{
		`{"historyCaptureHookEnabled":false}`,
		`{"HistoryCaptureHookEnabled":false}`,
	} {
		if err := f.terminal.Update(func(next *domainterminal.Settings) {
			next.HistoryCaptureHookEnabled = true
		}); err != nil {
			t.Fatal(err)
		}
		documents := map[string]json.RawMessage{
			adaptersyncedsettings.KeyTerminal: json.RawMessage(document),
		}
		if err := f.adapter.Apply(ctx, documents); err != nil {
			t.Fatalf("Apply %s: %v", document, err)
		}
		if f.terminal.Settings().HistoryCaptureHookEnabled {
			t.Fatalf("an explicit false was ignored in %s", document)
		}
	}
}

func customTheme(t *testing.T, id, name string) domaintheme.TerminalTheme {
	t.Helper()
	preset, ok := domaintheme.LookupTerminalTheme(domaintheme.DefaultTerminalTheme)
	if !ok {
		t.Fatalf("built-in theme %q is missing", domaintheme.DefaultTerminalTheme)
	}
	preset.ID = id
	preset.Name = name
	preset.Custom = true
	return preset
}

func decode(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatalf("document is missing")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode document: %v", err)
	}
}

func marshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode document: %v", err)
	}
	return data
}

// The Host Atuin preference is device-local: the portable payload must not
// carry it, and a remote document cannot change it in either direction.
func TestApplyLeavesTheDeviceLocalHostAtuinPreferenceAlone(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	for _, local := range []bool{true, false} {
		if err := f.terminal.Update(func(next *domainterminal.Settings) {
			next.HistoryPreferHostAtuin = local
		}); err != nil {
			t.Fatal(err)
		}
		documents := map[string]json.RawMessage{
			adaptersyncedsettings.KeyTerminal: json.RawMessage(
				`{"historyPreferHostAtuin":` + strconv.FormatBool(!local) + `,"osc52":"off"}`),
		}
		if err := f.adapter.Apply(ctx, documents); err != nil {
			t.Fatalf("Apply with local %t: %v", local, err)
		}
		got := f.terminal.Settings()
		if got.HistoryPreferHostAtuin != local {
			t.Fatalf("a remote document changed the device-local preference from %t to %t",
				local, got.HistoryPreferHostAtuin)
		}
		if got.OSC52 != domainterminal.ClipboardOff {
			t.Fatalf("OSC52 = %q, want the synced value", got.OSC52)
		}
	}

	snapshot, err := f.adapter.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var document map[string]any
	decode(t, snapshot[adaptersyncedsettings.KeyTerminal], &document)
	for _, key := range []string{"HistoryPreferHostAtuin", "historyPreferHostAtuin"} {
		if _, found := document[key]; found {
			t.Fatalf("the portable terminal document carries the device-local key %q", key)
		}
	}
}
