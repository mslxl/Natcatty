package settings

import (
	"context"
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"

	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	"github.com/mslxl/natcatty/ui/locales"
)

// newShortcutFixtureOverrides returns a shortcut view over the given persisted
// state, with the live Backups menu accelerator. It only uses APIs that exist
// before the rework, so the same test can run against the old tree and show
// the failure it guards.
func newShortcutFixtureOverrides(t *testing.T,
	overrides domainkeybindings.Overrides) (*ShortcutsState, *testKeyStore) {
	t.Helper()
	return newShortcutFixtureMenu(t, overrides, "Local backups")
}

// newShortcutFixtureMenu is the fixture with a menu label in a chosen
// language, so a localized conflict message is exercised with a localized
// menu item, as the app builds it.
func newShortcutFixtureMenu(t *testing.T, overrides domainkeybindings.Overrides,
	menuLabel string) (*ShortcutsState, *testKeyStore) {
	t.Helper()
	store := &testKeyStore{state: appkeybindings.State{Overrides: overrides}}
	service := appkeybindings.NewService(store, appkeybindings.WithGOOS("windows"))
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	state := NewShortcutsState(service,
		WithMenuShortcuts(func() []domainkeybindings.MenuShortcut {
			return []domainkeybindings.MenuShortcut{
				{ID: "menu.backups", Label: menuLabel, Accelerator: "CmdOrCtrl+Shift+B"},
			}
		}))
	return state, store
}

func bindingLabelByID(t *testing.T, m locales.Messages, id string) string {
	t.Helper()
	binding, ok := domainkeybindings.Lookup(id)
	if !ok {
		t.Fatalf("unknown binding %q", id)
	}
	return bindingLabel(m, binding)
}

// insideSettingsContent reports whether r is a laid-out box inside the
// content region of a w×h Settings window. The header and search field end
// above y=100 and the sidebar is 168 DIPs plus the window padding wide.
func insideSettingsContent(r ui.Rect, w, h int) bool {
	const top, sidebar = 100, 190
	return r.W > 0 && r.H > 0 &&
		r.X >= sidebar && r.Y >= top &&
		r.X+r.W <= float32(w)+0.5 && r.Y+r.H <= float32(h)+0.5
}

// intrinsicSettingsTextWidth measures one line of the Settings theme's text
// at an unconstrained width. A nav label's laid-out box narrower than this is
// shortened with an ellipsis by MyGo's SingleLine text.
func intrinsicSettingsTextWidth(t *testing.T, text string) float32 {
	t.Helper()
	var width float32
	tester := ui.NewTester(func(c *ui.Context) {
		width, _ = c.MeasureText(0, ui.Span{Text: text, Size: ui.LightTheme().FontSize})
	}, 200, 100)
	tester.Frame()
	if width <= 0 {
		t.Fatalf("the reference label %q measured %v", text, width)
	}
	return width
}

// measuredSettingsTextHeight measures the height one laid-out line of
// Settings text takes in the tester's own font metrics. GitHub runners
// legitimately measure 17px where Windows measures 18px for the same theme
// size, so a test that calls a box shorter than a full line must compare it
// with this measurement rather than a literal pixel height. MeasureText
// reports the font's line box, which is not the height a Text element
// occupies, so this renders a reference Text node and reads its bounds.
func measuredSettingsTextHeight(t *testing.T, text string, size float32) float32 {
	t.Helper()
	var rect ui.Rect
	var ok bool
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() {
			ui.Text(c, text).FontSize(size)
		})
	}, 1200, 200)
	tester.Frame()
	rect, ok = tester.Find(text)
	if !ok || rect.H <= 0 {
		t.Fatalf("the reference label %q at size %v was laid out as %+v (found=%v)", text, size, rect, ok)
	}
	return rect.H
}

// TestSettingsSidebarLabelsAreNotEllipsized keeps every area name whole in
// the sidebar at both supported window sizes and in every catalog. The old
// "Security & App Lock" label did not fit and rendered as
// "Security & App Lo..."; a label box narrower than its own measured text is
// the same defect. The sidebar takes the width its widest localized name
// needs on the running platform, so the label must also stay on the sidebar
// side of the content viewport.
func TestSettingsSidebarLabelsAreNotEllipsized(t *testing.T) {
	for _, tag := range []string{"en", "es", "ru", "zh-CN", "zh-TW"} {
		m := locales.In(tag)
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			t.Run(fmt.Sprintf("%s/%dx%d", tag, size[0], size[1]), func(t *testing.T) {
				var layout viewLayout
				var context *ui.Context
				view := viewWithLayout(Config{Search: NewSearchState()}, &layout)
				tester := ui.NewTester(func(c *ui.Context) {
					context = c
					view(c)
				}, size[0], size[1])
				tester.SetLocale(tag)
				tester.Frame()
				viewport := layout.Content.Bounds(context)
				if viewport.W <= 0 {
					t.Fatalf("%s/%dx%d: the content viewport has no width: %+v",
						tag, size[0], size[1], viewport)
				}
				for _, area := range Areas() {
					label := AreaTitle(m, area)
					needed := intrinsicSettingsTextWidth(t, label)
					rect, ok := tester.Find(label)
					if !ok {
						t.Fatalf("%q is not rendered; texts %q", label, tester.Texts())
					}
					// The sidebar scrolls at the minimum size, so a later area
					// starts below the viewport. Wheel it into view the way a
					// user would before judging its label.
					for step := 0; step < 40 && rect.Y+rect.H > float32(size[1]); step++ {
						tester.Scroll(80, float32(size[1])*0.6, 0, 40)
						rect, ok = tester.Find(label)
						if !ok {
							t.Fatalf("%q disappeared while scrolling the sidebar", label)
						}
					}
					if rect.W <= 0 || rect.H <= 0 || rect.Y < 0 || rect.Y+rect.H > float32(size[1])+0.5 {
						t.Fatalf("%q is not laid out inside the window: %+v", label, rect)
					}
					if rect.X+rect.W > viewport.X-0.5 {
						t.Fatalf("%q runs into the content viewport %+v: %+v", label, viewport, rect)
					}
					if rect.W+0.5 < needed {
						t.Fatalf("%q needs %.1fpx on one line but its box is %.1fpx wide, so it is ellipsized",
							label, needed, rect.W)
					}
				}
			})
		}
	}
}

// TestSettingsShortcutPaneScrollsEveryRowIntoView renders the Shortcuts area
// at both supported sizes and wheels through it. Every one of the 42 binding
// rows must pass through the content viewport, the last row must end inside
// the window at the bottom, and PageUp must scroll back with the keyboard.
func TestSettingsShortcutPaneScrollsEveryRowIntoView(t *testing.T) {
	m := locales.In("en")
	bindings := catalogBindings()
	first := bindingLabelByID(t, m, bindings[0].ID)
	last := bindingLabelByID(t, m, bindings[len(bindings)-1].ID)

	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		w, h := size[0], size[1]
		name := fmt.Sprintf("%dx%d", w, h)
		t.Run(name, func(t *testing.T) {
			state, _ := newShortcutFixtureOverrides(t, nil)
			search := NewSearchState()
			search.SetArea(AreaShortcuts)
			tester := ui.NewTester(ViewWithSettings(Config{Shortcuts: state, Search: search}), w, h)
			tester.SetLocale("en")
			tester.Frame()

			// The pane starts at its top, with the first row in view.
			firstRect, ok := tester.Find(first)
			if !ok || !insideSettingsContent(firstRect, w, h) {
				t.Fatalf("the first shortcut row is not visible: %+v, texts %q",
					firstRect, tester.Texts())
			}

			seen := make(map[string]bool, len(bindings))
			record := func() {
				for _, binding := range bindings {
					rect, ok := tester.Find(bindingLabel(m, binding))
					if ok && insideSettingsContent(rect, w, h) {
						seen[binding.ID] = true
					}
				}
			}
			record()

			// Wheel with the pointer over the content, as a user scrolls it.
			x, y := float32(w)*3/5, float32(h)*3/5
			for step := 0; step < 160 && len(seen) < len(bindings); step++ {
				tester.Scroll(x, y, 0, 40)
				record()
			}
			if len(seen) != len(bindings) {
				var missing []string
				for _, binding := range bindings {
					if !seen[binding.ID] {
						missing = append(missing, binding.ID)
					}
				}
				t.Fatalf("wheel scrolling left %d of %d shortcut rows unreachable: %v",
					len(missing), len(bindings), missing)
			}

			// Scrolled to the bottom, the last row is a real box inside the
			// window rather than a Scroll that merely exists.
			lastRect, ok := tester.Find(last)
			if !ok {
				t.Fatalf("the last shortcut row was never laid out: %q", last)
			}
			if lastRect.W <= 0 || lastRect.H <= 0 {
				t.Fatalf("the last shortcut row has an empty box: %+v", lastRect)
			}
			if !insideSettingsContent(lastRect, w, h) {
				t.Fatalf("the last shortcut row is outside the content viewport: %+v", lastRect)
			}

			// PageUp reaches the pane with the pointer over it and scrolls
			// back to the first row.
			for press := 0; press < 40; press++ {
				tester.Key(0, ui.KeyPageUp)
			}
			back, ok := tester.Find(first)
			if !ok || !insideSettingsContent(back, w, h) {
				t.Fatalf("PageUp did not scroll back to the first row: %+v", back)
			}
		})
	}
}

// TestSettingsSidebarKeepsTheLastAreaReachable checks the smallest window:
// the Shortcuts entry is inside the window and clicking it still switches
// areas. If a longer translation ever pushes it out, the sidebar scroll must
// bring it back before the click.
func TestSettingsSidebarKeepsTheLastAreaReachable(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		w, h := size[0], size[1]
		search := NewSearchState() // starts on Appearance
		tester := ui.NewTester(ViewWithSettings(Config{Search: search}), w, h)
		tester.SetLocale("en")
		tester.Frame()

		label := m.SettingsAreaShortcuts()
		rect, ok := tester.Find(label)
		if !ok {
			t.Fatalf("%dx%d: the sidebar lost %q: %q", w, h, label, tester.Texts())
		}
		// The sidebar is the left column; its own scroll reveals a clipped
		// entry. A fixed delay is never used: each iteration is a real wheel
		// step with a hard bound.
		for step := 0; step < 20 && rect.Y+rect.H > float32(h); step++ {
			tester.Scroll(100, float32(h)/2, 0, 30)
			rect, ok = tester.Find(label)
			if !ok {
				t.Fatalf("%dx%d: the sidebar lost %q while scrolling", w, h, label)
			}
		}
		if rect.Y+rect.H > float32(h)+0.5 {
			t.Fatalf("%dx%d: the last sidebar entry is clipped by the window edge: %+v", w, h, rect)
		}
		if err := tester.Click(label); err != nil {
			t.Fatalf("%dx%d: click the last sidebar entry: %v", w, h, err)
		}
		tester.Frame()
		if search.Area() != AreaShortcuts {
			t.Fatalf("%dx%d: the last sidebar entry opened %q, want shortcuts", w, h, search.Area())
		}
	}
}

// TestSettingsAreasShowFieldLabels asserts every preference field carries its
// localized label at both sizes. Before the rework fieldRow dropped the
// label, so selects, numbers, and text inputs appeared without context.
func TestSettingsAreasShowFieldLabels(t *testing.T) {
	cases := []struct {
		area   Area
		labels func(locales.Messages) []string
	}{
		{AreaAppearance, func(m locales.Messages) []string {
			return []string{
				m.SettingsThemeMode(), m.SettingsAccentMode(), m.SettingsAccentColor(),
				m.SettingsUIFont(), m.SettingsWindowOpacity(), m.SettingsTerminalTheme(),
				m.SettingsTerminalFontFamily(), m.SettingsTerminalFontSize(),
			}
		}},
		{AreaTerminal, func(m locales.Messages) []string {
			return []string{
				m.SettingsLogDirectory(), m.SettingsLogFormat(), m.SettingsOSC52Title(),
				m.SettingsTerminalEncoding(), m.SettingsPasteMinLines(), m.SettingsDropAction(),
			}
		}},
		{AreaLayout, func(m locales.Messages) []string {
			return []string{
				m.SettingsShowHostTreeSidebar(), m.SettingsShowSFTPTab(), m.SettingsShowRecentHosts(),
				m.SettingsTabBarPosition(), m.SettingsShellOnlyTabNumberShortcuts(),
				m.SettingsShowTabNumberBadges(), m.SettingsRestorePreviousSession(),
				m.SettingsStartupLanding(), m.SettingsSidePanelDock(),
			}
		}},
		{AreaSFTPEditor, func(m locales.Messages) []string {
			return []string{
				m.SettingsDoubleClickBehavior(), m.SettingsAutoSync(), m.SettingsShowHiddenFiles(),
				m.SettingsUseCompressedUpload(), m.SettingsAutoOpenSidebar(),
				m.SettingsFollowTerminalCwd(), m.SettingsDefaultViewMode(),
				m.SettingsEditorWordWrap(), m.SettingsEditorFontSize(),
			}
		}},
		{AreaLogs, func(m locales.Messages) []string { return []string{m.SettingsSSHDebug()} }},
		{AreaSystem, func(m locales.Messages) []string {
			return []string{
				m.SettingsHotkeyAccelerator(), m.SettingsShowTrayIcon(),
				m.SettingsCloseToTray(), m.SettingsSSHDeepLinks(),
			}
		}},
		{AreaSync, func(m locales.Messages) []string {
			return []string{
				m.SettingsSyncMasterPassword(), m.SettingsSyncAutoSync(),
				m.SettingsSyncConflictStrategy(),
			}
		}},
		{AreaSecurity, func(m locales.Messages) []string {
			return []string{m.SettingsConfirmCloseSessions(), m.SettingsConfirmDestructive()}
		}},
	}
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{1100, 720}, {720, 480}} {
			w, h := size[0], size[1]
			for _, test := range cases {
				name := fmt.Sprintf("%s/%s/%dx%d", test.area, locale, w, h)
				t.Run(name, func(t *testing.T) {
					m := locales.In(locale)
					config := Config{Preferences: NewPreferencesState(newFakePreferences())}
					switch test.area {
					case AreaTerminal:
						config.Terminal = NewTerminalState(newFakeTerminal())
					case AreaSystem:
						config.Desktop = NewDesktopState(newFakeDesktop())
					case AreaSecurity:
						config.Lock = NewAppLockState(&fakeAppLock{})
					case AreaSync:
						config.Sync = NewSyncState(newFakeSyncService())
					}
					tester := ui.NewTester(viewAt(test.area, config), w, h)
					tester.SetLocale(locale)
					tester.Frame()
					for _, label := range test.labels(m) {
						if !tester.HasText(label) {
							t.Errorf("%q has no label: %q", label, tester.Texts())
							continue
						}
						rect, ok := tester.Find(label)
						// Scrolling down through the pane reaches a field below
						// the fold; a label clipped out of the scene reports an
						// empty box until it scrolls in.
						x, y := float32(w)*3/5, float32(h)*3/5
						for step := 0; step < 80 && (!ok || rect.W <= 0 || rect.H <= 0); step++ {
							tester.Scroll(x, y, 0, 40)
							rect, ok = tester.Find(label)
						}
						if !ok {
							t.Errorf("%q has no label: %q", label, tester.Texts())
							continue
						}
						if rect.W <= 0 || rect.H <= 0 {
							t.Errorf("%q never scrolled into view: %+v", label, rect)
							continue
						}
						if rect.X < -0.5 || rect.X+rect.W > float32(w)+0.5 {
							t.Errorf("%q does not fit horizontally at %d DIPs: %+v", label, w, rect)
						}
						if rect.Y < 0 || rect.Y+rect.H > float32(h)+0.5 {
							t.Errorf("%q is clipped vertically at %d DIPs: %+v", label, h, rect)
						}
					}
				})
			}
		}
	}
}

// TestSettingsSearchMatchesChordsAndOverrides types a chord, including one
// the user overrode, and asserts the matching binding row appears. Enter opens
// the Shortcuts area.
func TestSettingsSearchMatchesChordsAndOverrides(t *testing.T) {
	m := locales.In("en")
	overrides := domainkeybindings.Overrides{
		"open-local": {"pc": "Ctrl + Shift + B"},
		"open-sftp":  {"pc": "Ctrl + Alt + O"},
	}
	state, _ := newShortcutFixtureOverrides(t, overrides)
	search := NewSearchState()
	tester := ui.NewTester(ViewWithSettings(Config{Search: search, Shortcuts: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	tester.Key(0, ui.KeyTab)
	tester.Type("Ctrl+Shift+B")
	tester.Frame()
	if !search.Filtering() {
		t.Fatalf("typing did not reach the search field: %q", tester.Texts())
	}
	for _, want := range []string{
		bindingLabelByID(t, m, "open-local"), // the user's override
		"Local backups",                      // the menu accelerator, Ctrl+Shift+B
	} {
		if !tester.HasText(want) {
			t.Errorf("the chord query did not match %q: %q", want, tester.Texts())
		}
	}
	if tester.HasText(m.SettingsSearchNoResults()) {
		t.Fatalf("a chord query reported no matches: %q", tester.Texts())
	}

	tester.Key(0, ui.KeyEnter)
	tester.Frame()
	if search.Filtering() {
		t.Fatal("opening a chord match left the query active")
	}
	if search.Area() != AreaShortcuts {
		t.Fatalf("a chord match opened %q, want shortcuts", search.Area())
	}

	// Spaces around the plus signs do not hide the menu accelerator.
	search.Clear()
	tester.Frame()
	tester.Type("Ctrl + Shift + B")
	tester.Frame()
	for _, want := range []string{
		bindingLabelByID(t, m, "open-local"),
		"Local backups",
	} {
		if !tester.HasText(want) {
			t.Errorf("the spaced chord query did not match %q: %q", want, tester.Texts())
		}
	}

	// A chord the user replaced no longer reaches the binding. Open SFTP's
	// PC default is Ctrl+Shift+O; the override above retires it.
	search.Clear()
	tester.Frame()
	tester.Type("Ctrl+Shift+O")
	tester.Frame()
	if tester.HasText(m.SettingsBindingOpenSFTP()) {
		t.Fatalf("the overridden-away default still matches Open SFTP: %q", tester.Texts())
	}

	// A catalog default in the inactive column matches too.
	search.Clear()
	tester.Frame()
	tester.Type("Ctrl+Shift+D")
	tester.Frame()
	if !tester.HasText(m.SettingsBindingSplitHorizontal()) {
		t.Fatalf("the default PC chord did not match Split Horizontal: %q", tester.Texts())
	}
}

// TestMenuConflictNamesTheMenuItem records a chord the Backups menu owns and
// asserts the dialog names that menu item, not just "the application menu".
func TestMenuConflictNamesTheMenuItem(t *testing.T) {
	tests := map[string]struct{ label, named, generic string }{
		"en":    {"Local backups", "the menu item Local backups", "the application menu"},
		"zh-CN": {"本地备份", "菜单项“本地备份”", "应用菜单"},
	}
	for locale, want := range tests {
		t.Run(locale, func(t *testing.T) {
			state, _ := newShortcutFixtureMenu(t, nil, want.label)
			tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
			tester.SetLocale(locale)
			tester.Frame()

			state.BeginRecording("open-local")
			if !state.RecordKey("open-local", ui.KeyB, ui.Ctrl|ui.Shift) {
				t.Fatal("the recorder did not consume Ctrl+Shift+B")
			}
			tester.Frame()
			pending, ok := state.Pending()
			if !ok {
				t.Fatalf("Ctrl+Shift+B reported no conflict: %q", tester.Texts())
			}
			if pending.Chord != "Ctrl + Shift + B" {
				t.Fatalf("pending chord = %q", pending.Chord)
			}
			foundMenu := false
			for _, conflict := range pending.Conflicts {
				if len(conflict.Menu) > 0 {
					foundMenu = true
				}
			}
			if !foundMenu {
				t.Fatalf("the conflict model lost the menu item: %+v", pending.Conflicts)
			}
			if !tester.HasText(want.named) {
				t.Errorf("the dialog does not name the menu item: want %q, texts %q",
					want.named, tester.Texts())
			}
			if tester.HasText(want.generic) {
				t.Errorf("the dialog still says only %q: %q", want.generic, tester.Texts())
			}
		})
	}
}

// TestBindingConflictNamesTheOtherBinding keeps the duplicate-binding half of
// the conflict message naming the displaced binding.
func TestBindingConflictNamesTheOtherBinding(t *testing.T) {
	m := locales.In("en")
	state, _ := newShortcutFixtureOverrides(t, nil)
	tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	state.BeginRecording("open-local")
	state.RecordKey("open-local", ui.KeyB, ui.Ctrl)
	tester.Frame()
	if _, ok := state.Pending(); !ok {
		t.Fatalf("Ctrl+B reported no conflict: %q", tester.Texts())
	}
	if !tester.HasText(m.SettingsBindingBroadcast()) {
		t.Fatalf("the conflict does not name the other binding: %q", tester.Texts())
	}
}

// TestShortcutResetAllUsesShortcutWording asserts the shortcut reset speaks
// about keyboard overrides only, not the preferences reset. The literal texts
// are the catalog copy in each language, so this also compiles against the old
// tree and shows the wrong wording it printed.
func TestShortcutResetAllUsesShortcutWording(t *testing.T) {
	tests := map[string][]string{
		"en": {
			"Reset all shortcuts?",
			"This restores every keyboard override to its default. It cannot be undone.",
			"Reset shortcuts",
		},
		"zh-CN": {
			"重置所有快捷键？",
			"这会将每一项快捷键覆盖恢复为默认值。此操作无法撤销。",
			"重置快捷键",
		},
	}
	overrides := domainkeybindings.Overrides{"open-local": {"pc": "Alt + Shift + Q"}}
	for locale, want := range tests {
		t.Run(locale, func(t *testing.T) {
			state, _ := newShortcutFixtureOverrides(t, overrides)
			m := locales.In(locale)
			tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
			tester.SetLocale(locale)
			tester.Frame()

			if err := tester.Click(m.SettingsShortcutResetAll()); err != nil {
				t.Fatalf("click Reset all shortcuts: %v", err)
			}
			tester.Frame()
			for _, text := range want {
				if !tester.HasText(text) {
					t.Errorf("the shortcut reset is missing %q: %q", text, tester.Texts())
				}
			}
			for _, stale := range []string{
				"every preference and keyboard override",
				"所有偏好设置和键盘快捷键",
				"Reset everything",
				"全部重置",
			} {
				if tester.HasText(stale) {
					t.Errorf("the shortcut reset reuses the preferences wording %q", stale)
				}
			}
			if err := tester.Click(want[2]); err != nil {
				t.Fatalf("click the shortcut reset confirmation: %v", err)
			}
			tester.Frame()
			if got := state.Chord("open-local"); got != domainkeybindings.Disabled {
				t.Fatalf("open-local after reset = %q, want Disabled", got)
			}
		})
	}
}

// TestSFTPEditorAreaDescribesLiveControls pins the SFTP & Editor copy to the
// controls this build actually ships. The area configures the live SFTP
// browser and editor, so a banner or description that calls it unavailable is
// a lie. The description literal is the required replacement and the stale
// literals are the retired strings, so this test compiles against the old tree
// and shows both the copy it rejects and the copy it requires.
func TestSFTPEditorAreaDescribesLiveControls(t *testing.T) {
	tests := map[string]struct {
		description string
		stale       []string
	}{
		"en": {
			description: "File browser and editor defaults, including compressed uploads, automatic directory sync, and following the terminal directory.",
			stale: []string{
				"SFTP is not available in this build.",
				"The SFTP surface is not part of this build yet; these values persist for it.",
			},
		},
		"zh-CN": {
			description: "文件浏览器与编辑器默认值，包括压缩上传、自动同步目录以及跟随终端目录。",
			stale: []string{
				"此构建不提供 SFTP。",
				"此构建尚未包含 SFTP 界面；这些值会为其保留。",
			},
		},
	}
	for locale, want := range tests {
		t.Run(locale, func(t *testing.T) {
			tester := ui.NewTester(viewAt(AreaSFTPEditor,
				Config{Preferences: NewPreferencesState(newFakePreferences())}), 1100, 720)
			tester.SetLocale(locale)
			tester.Frame()

			for _, stale := range want.stale {
				if tester.HasText(stale) {
					t.Errorf("the SFTP & Editor pane still says %q: %q", stale, tester.Texts())
				}
			}
			if !tester.HasText(want.description) {
				t.Errorf("the SFTP & Editor pane does not describe the live surface: want %q, texts %q",
					want.description, tester.Texts())
			}
		})
	}
}

// TestEmptySearchPrintsNoResultsOnce guards the duplicate empty-state line.
func TestEmptySearchPrintsNoResultsOnce(t *testing.T) {
	m := locales.In("en")
	search := NewSearchState()
	tester := ui.NewTester(ViewWithSettings(Config{Search: search}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	tester.Key(0, ui.KeyTab)
	tester.Type("zzzz-no-such-setting")
	tester.Frame()
	if !search.Filtering() {
		t.Fatalf("typing did not reach the search field: %q", tester.Texts())
	}
	count := 0
	for _, text := range tester.Texts() {
		if text == m.SettingsSearchNoResults() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the empty state appears %d times, want once: %q", count, tester.Texts())
	}
}

// TestShortcutsAreaNotesThatTerminalChangesNeedANewTerminal documents the
// MyGo v0.5.5 limitation in the UI: terminal bindings are applied when a
// terminal is created, so open terminals keep their old table.
func TestShortcutsAreaNotesThatTerminalChangesNeedANewTerminal(t *testing.T) {
	tests := map[string]string{
		"en":    "apply to new terminals",
		"zh-CN": "应用于新建的终端",
	}
	for locale, want := range tests {
		t.Run(locale, func(t *testing.T) {
			state, _ := newShortcutFixtureOverrides(t, nil)
			tester := ui.NewTester(viewAt(AreaShortcuts, Config{Shortcuts: state}), 1100, 720)
			tester.SetLocale(locale)
			tester.Frame()
			if !tester.HasText(want) {
				t.Fatalf("the Shortcuts area does not say terminal changes need a new terminal: %q",
					tester.Texts())
			}
		})
	}
}
