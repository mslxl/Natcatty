package settings

import (
	"context"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakePreferences struct {
	settings domain.Settings
	updates  int
	resets   int
	resetAll int
	err      error
}

func newFakePreferences() *fakePreferences {
	return &fakePreferences{settings: domain.Defaults()}
}

func (f *fakePreferences) Settings() domain.Settings { return f.settings }
func (f *fakePreferences) Update(change func(*domain.Settings)) error {
	if f.err != nil {
		return f.err
	}
	next := f.settings
	change(&next)
	f.settings = domain.Normalize(next)
	f.updates++
	return nil
}
func (f *fakePreferences) ResetGroup(group domain.Group) error {
	if f.err != nil {
		return f.err
	}
	f.settings = domain.ResetGroup(f.settings, group)
	f.resets++
	return nil
}
func (f *fakePreferences) ResetAll() error {
	if f.err != nil {
		return f.err
	}
	f.settings = domain.ResetAll()
	f.resetAll++
	return nil
}

func TestSearchFiltersAcrossAreasAndKeyboardOnlyOpensTheMatch(t *testing.T) {
	search := NewSearchState()
	config := Config{Lock: NewAppLockState(&fakeAppLock{}), Search: search}
	tester := ui.NewTester(ViewWithSettings(config), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	// The search field is the only focusable control in this configuration,
	// so one Tab reaches it without a pointer.
	tester.Key(0, ui.KeyTab)
	tester.Type("app lock")
	tester.Frame()
	if !search.Filtering() {
		t.Fatalf("typing did not reach the search field: %v", tester.Texts())
	}
	if !tester.HasText(m.SettingsAppLockTitle()) {
		t.Fatalf("search did not cross areas: %v", tester.Texts())
	}

	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if search.Area() != AreaSecurity {
		t.Fatalf("Enter opened %q, want security", search.Area())
	}
	if search.Filtering() {
		t.Fatal("opening a match left the query active")
	}
	if !tester.HasText(m.SettingsAppLockTitle()) {
		t.Fatalf("the matched area did not open: %v", tester.Texts())
	}
}

// TestSearchIndexesEveryTerminalSetting covers the whole tree: the Terminal
// area has no typed preference fields, so its controls are indexed by their
// localized labels and stable English keywords.
func TestSearchIndexesEveryTerminalSetting(t *testing.T) {
	hits := SearchIndex(locales.In("en"))
	for _, query := range []string{"osc", "paste protection", "autocomplete",
		"retention", "encoding", "session statistics"} {
		found := false
		for _, hit := range FilterHits(hits, query) {
			if hit.Area == AreaTerminal {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%q found no Terminal setting", query)
		}
	}
}

// TestEveryBindingHasALocalizedLabel keeps the label contract in the UI after
// ADR-0009 removed the English fallback from the domain catalog.
func TestEveryBindingHasALocalizedLabel(t *testing.T) {
	for _, tag := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(tag)
		for _, binding := range catalogBindings() {
			if bindingLabel(m, binding) == "" {
				t.Errorf("%s: %s has no label", tag, binding.ID)
			}
		}
	}
}

func TestSearchArrowKeysMoveTheHighlight(t *testing.T) {
	search := NewSearchState()
	config := Config{Search: search}
	tester := ui.NewTester(ViewWithSettings(config), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	tester.Key(0, ui.KeyTab)
	tester.Type("font")
	tester.Frame()
	first := search.Selected()
	tester.Key(0, ui.KeyDown)
	tester.Frame()
	if search.Selected() == first {
		t.Fatal("Down did not move the search highlight")
	}
	tester.Key(0, ui.KeyUp)
	tester.Frame()
	if search.Selected() != first {
		t.Fatal("Up did not restore the highlight")
	}
	tester.Key(0, ui.KeyEscape)
	tester.Frame()
	if search.Filtering() {
		t.Fatal("Escape did not clear the search")
	}
}

func TestPreferenceChangesApplyLiveAndResetIndividually(t *testing.T) {
	fake := newFakePreferences()
	state := NewPreferencesState(fake)
	tester := ui.NewTester(viewAt(AreaLayout, Config{Preferences: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsShowSFTPTab()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if fake.settings.Layout.ShowSFTPTab {
		t.Fatal("the checkbox did not reach the preferences service")
	}
	if fake.updates != 1 {
		t.Fatalf("updates = %d", fake.updates)
	}

	// The changed field offers a reset control; restoring it is one update.
	if err := tester.Click(m.SettingsReset()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !fake.settings.Layout.ShowSFTPTab {
		t.Fatal("the field reset did not restore the default")
	}
	if fake.updates != 2 {
		t.Fatalf("updates after reset = %d", fake.updates)
	}
}

func TestResetAllAsksBeforeRestoringEverything(t *testing.T) {
	fake := newFakePreferences()
	fake.settings.Appearance.WindowOpacity = 0.6
	fake.settings.Layout.ShowTabNumberBadges = false
	state := NewPreferencesState(fake)
	tester := ui.NewTester(viewAt(AreaAppearance, Config{Preferences: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsResetAll()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if fake.resetAll != 0 {
		t.Fatal("reset all happened before confirmation")
	}
	if !tester.HasText(m.SettingsResetAllBody()) {
		t.Fatalf("the confirmation is not visible: %v", tester.Texts())
	}
	if err := tester.Click(m.SettingsResetAllConfirm()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if fake.resetAll != 1 {
		t.Fatalf("resetAll = %d", fake.resetAll)
	}
	if fake.settings.Layout.ShowTabNumberBadges != true || fake.settings.Appearance.WindowOpacity != 1 {
		t.Fatalf("settings after reset = %+v", fake.settings)
	}
}

type testKeyStore struct {
	state appkeybindings.State
	saves int
}

func (s *testKeyStore) Load(context.Context) (appkeybindings.State, error) { return s.state, nil }
func (s *testKeyStore) Save(_ context.Context, state appkeybindings.State) error {
	s.state = state
	s.saves++
	return nil
}

func newShortcutFixture(t *testing.T) (*ShortcutsState, *testKeyStore) {
	t.Helper()
	store := &testKeyStore{}
	service := appkeybindings.NewService(store, appkeybindings.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := NewShortcutsState(service,
		WithMenuShortcuts(func() []domainkeybindings.MenuShortcut {
			return []domainkeybindings.MenuShortcut{
				{ID: "menu.backups", Label: "Backups", Accelerator: "CmdOrCtrl+Shift+B"},
			}
		}))
	return state, store
}

func TestShortcutRecorderRequiresReplaceOnConflict(t *testing.T) {
	state, store := newShortcutFixture(t)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	// Ctrl+B already belongs to the Broadcast binding in the same domain.
	state.BeginRecording("open-local")
	if !state.RecordKey("open-local", ui.KeyB, ui.Ctrl) {
		t.Fatal("the recorder did not consume the key")
	}
	tester.Frame()
	pending, ok := state.Pending()
	if !ok {
		t.Fatalf("no conflict was reported: %v", tester.Texts())
	}
	if pending.Chord != "Ctrl + B" {
		t.Fatalf("pending chord = %q", pending.Chord)
	}
	if !tester.HasText(m.SettingsShortcutConflictTitle()) ||
		!tester.HasText(m.SettingsBindingBroadcast()) {
		t.Fatalf("the conflict is not shown: %v", tester.Texts())
	}
	if store.saves != 0 {
		t.Fatal("a conflicting chord was persisted before replacing")
	}

	if err := tester.Click(m.SettingsShortcutConflictReplace()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if got := state.Chord("open-local"); got != "Ctrl + B" {
		t.Fatalf("open-local = %q", got)
	}
	if got := state.Chord("broadcast"); got != domainkeybindings.Disabled {
		t.Fatalf("broadcast = %q, want Disabled", got)
	}
	if store.saves == 0 || store.state.Overrides["open-local"]["pc"] != "Ctrl + B" {
		t.Fatalf("override was not persisted: %+v", store.state)
	}
}

// TestShortcutRecorderTakesAConflictingChordFromTheKeyboard reaches a chord
// field with Tab only, then records Ctrl+B, which the Broadcast binding owns.
// A pointer is never used, so the keyboard-only path through MyGo's key
// routing must deliver the chord to the field rather than losing it.
func TestShortcutRecorderTakesAConflictingChordFromTheKeyboard(t *testing.T) {
	state, store := newShortcutFixture(t)
	m := locales.In("en")
	tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	for press := 0; press < 200; press++ {
		tester.Key(ui.Ctrl, ui.KeyB)
		tester.Frame()
		pending, ok := state.Pending()
		if !ok {
			tester.Key(0, ui.KeyTab)
			tester.Frame()
			continue
		}
		if pending.Chord != "Ctrl + B" {
			t.Fatalf("pending chord = %q", pending.Chord)
		}
		if len(pending.Conflicts) == 0 {
			t.Fatal("Ctrl+B produced no conflict")
		}
		if store.saves != 0 {
			t.Fatal("a conflicting chord was persisted before replacing")
		}
		if !tester.HasText(m.SettingsShortcutConflictTitle()) {
			t.Fatalf("the conflict is not shown: %v", tester.Texts())
		}
		return
	}
	t.Fatalf("the keyboard never reached a chord field: %v", tester.Texts())
}

func TestShortcutCancelLeavesEveryBindingAlone(t *testing.T) {
	state, store := newShortcutFixture(t)
	state.BeginRecording("open-local")
	state.RecordKey("open-local", ui.KeyB, ui.Ctrl)
	if _, ok := state.Pending(); !ok {
		t.Fatal("expected a conflict")
	}
	state.CancelConflict()
	if _, ok := state.Pending(); ok {
		t.Fatal("Cancel left the conflict pending")
	}
	if got := state.Chord("open-local"); got != domainkeybindings.Disabled {
		t.Fatalf("open-local = %q", got)
	}
	if store.saves != 0 {
		t.Fatal("Cancel persisted a binding")
	}
}

func TestShortcutSchemeSwitchAndDisabledScheme(t *testing.T) {
	state, _ := newShortcutFixture(t)
	if state.Scheme() != domainkeybindings.SchemePC {
		t.Fatalf("scheme = %q", state.Scheme())
	}
	if !state.SetScheme(domainkeybindings.SchemeMac) {
		t.Fatal("SetScheme failed")
	}
	if got := state.Chord("copy"); got != "⌘ + C" {
		t.Fatalf("mac copy = %q", got)
	}
	if !state.SetScheme(domainkeybindings.SchemeDisabled) {
		t.Fatal("disabling failed")
	}
	if got := state.Chord("copy"); got != domainkeybindings.Disabled {
		t.Fatalf("disabled copy = %q", got)
	}
	if len(state.Conflicts()) != 0 {
		t.Fatal("a disabled scheme reported conflicts")
	}
}

func TestShortcutUnavailableBindingKeepsItsRow(t *testing.T) {
	state, _ := newShortcutFixture(t)
	state.available = func(id string) bool { return id != "open-sftp" }
	tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	if !tester.HasText(m.SettingsBindingOpenSFTP()) {
		t.Fatalf("the unavailable binding is missing from the catalog: %v", tester.Texts())
	}
	if !tester.HasText(m.SettingsShortcutUnavailable()) {
		t.Fatal("the unavailable marker is not shown")
	}
}

// TestSettingsScreenshotsAndClipping renders both window sizes in English and
// Chinese with a pending shortcut conflict, checks that every laid-out line
// fits the window, and leaves PNGs for the review report.
func TestSettingsScreenshotsAndClipping(t *testing.T) {
	outDir := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if outDir == "" {
		outDir = filepath.Join(os.TempDir(), "natcatty-settings-screenshots")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {720, 480}} {
			state, _ := newShortcutFixture(t)
			state.BeginRecording("open-local")
			state.RecordKey("open-local", ui.KeyB, ui.Ctrl)
			prefs := NewPreferencesState(newFakePreferences())
			search := NewSearchState()
			search.SetArea(AreaShortcuts)
			tester := ui.NewTester(ViewWithSettings(Config{
				Lock: NewAppLockState(&fakeAppLock{}), Preferences: prefs,
				Shortcuts: state, Search: search,
			}), size[0], size[1])
			tester.SetLocale(locale)
			tester.Frame()

			m := locales.In(locale)
			for _, text := range []string{
				m.NavSettings(), m.SettingsSearchPlaceholder(),
				m.SettingsShortcutsTitle(), m.SettingsShortcutConflictTitle(),
			} {
				rect, ok := tester.Find(text)
				if !ok {
					t.Fatalf("%s %dx%d: %q missing: %v", locale, size[0], size[1], text, tester.Texts())
				}
				if rect.X < -0.5 || rect.X+rect.W > float32(size[0])+0.5 ||
					rect.Y < -0.5 || rect.Y+rect.H > float32(size[1])+0.5 {
					t.Fatalf("%s %dx%d: %q is clipped: %+v", locale, size[0], size[1], text, rect)
				}
			}

			path := filepath.Join(outDir, fmt.Sprintf("settings-%s-%dx%d.png", locale, size[0], size[1]))
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(file, tester.Image()); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(path); err != nil || info.Size() == 0 {
				t.Fatalf("screenshot %s was not written: %v", path, err)
			}
			t.Logf("screenshot: %s", path)
		}
	}
}

func TestSearchIndexCoversEveryArea(t *testing.T) {
	m := locales.In("en")
	hits := SearchIndex(m)
	seen := map[Area]bool{}
	for _, hit := range hits {
		seen[hit.Area] = true
	}
	for _, area := range Areas() {
		if !seen[area] {
			t.Errorf("area %q has no searchable settings", area)
		}
	}
	if len(FilterHits(hits, "opacity")) == 0 {
		t.Error("opacity does not match")
	}
	if len(FilterHits(hits, "透明度")) != 0 {
		t.Error("English catalog unexpectedly matches Chinese keywords")
	}
	hitsZh := SearchIndex(locales.In("zh-CN"))
	if len(FilterHits(hitsZh, "透明度")) == 0 {
		t.Error("the localized Chinese label is not searchable")
	}
	if !strings.Contains(strings.ToLower(hitsZh[0].AreaID), "appearance") {
		t.Error("area identifiers are not searchable keywords")
	}
}
