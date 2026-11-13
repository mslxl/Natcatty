package settings

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakeDesktop struct {
	settings       desktop.Settings
	caps           desktop.Capabilities
	hotkeyErr      error
	err            error
	showTray       []bool
	closeTray      []bool
	deepLinks      []bool
	hotkey         []string
	modes          []desktop.HotkeyMode
	sources        []desktop.QuakeSource
	quakeHeights   []heightCall
	topdownHeights []heightCall
	hideOnBlur     []bool
	hideOnEscape   []bool
	hideOnOutside  []bool
	autoStart      []bool
	notified       map[desktop.Event]bool
	resetCalls     int
}

// heightCall records one height setter invocation.
type heightCall struct {
	value int
	unit  desktop.HeightUnit
}

func newFakeDesktop() *fakeDesktop {
	return &fakeDesktop{
		settings: desktop.Defaults("windows"),
		caps: desktop.Capabilities{
			Tray: true, Notifications: true, AutoStart: true,
			GlobalHotkey: true, Dropdown: true,
		},
		notified: map[desktop.Event]bool{},
	}
}

func (f *fakeDesktop) Settings() desktop.Settings         { return f.settings }
func (f *fakeDesktop) Capabilities() desktop.Capabilities { return f.caps }
func (f *fakeDesktop) HotkeyProblem() error               { return f.hotkeyErr }

func (f *fakeDesktop) SetShowTrayIcon(show bool) error {
	if f.err != nil {
		return f.err
	}
	f.showTray = append(f.showTray, show)
	f.settings.ShowTrayIcon = show
	return nil
}

func (f *fakeDesktop) SetCloseToTray(enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.closeTray = append(f.closeTray, enabled)
	f.settings.CloseToTray = enabled
	return nil
}

func (f *fakeDesktop) SetHotkeyMode(mode desktop.HotkeyMode) error {
	if f.err != nil {
		return f.err
	}
	f.modes = append(f.modes, mode)
	f.settings.HotkeyMode = mode
	return nil
}

func (f *fakeDesktop) SetQuakeSource(source desktop.QuakeSource) error {
	if f.err != nil {
		return f.err
	}
	f.sources = append(f.sources, source)
	f.settings.QuakeSource = source
	return nil
}

func (f *fakeDesktop) SetQuakeHeight(value int, unit desktop.HeightUnit) error {
	if f.err != nil {
		return f.err
	}
	if err := desktop.ValidateHeight(value, unit); err != nil {
		return err
	}
	f.quakeHeights = append(f.quakeHeights, heightCall{value: value, unit: unit})
	f.settings.QuakeHeight, f.settings.QuakeHeightUnit = value, unit
	return nil
}

func (f *fakeDesktop) SetTopdownHeight(value int, unit desktop.HeightUnit) error {
	if f.err != nil {
		return f.err
	}
	if err := desktop.ValidateHeight(value, unit); err != nil {
		return err
	}
	f.topdownHeights = append(f.topdownHeights, heightCall{value: value, unit: unit})
	f.settings.TopdownHeight, f.settings.TopdownHeightUnit = value, unit
	return nil
}

func (f *fakeDesktop) SetHideOnBlur(enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.hideOnBlur = append(f.hideOnBlur, enabled)
	f.settings.HideOnBlur = enabled
	return nil
}

func (f *fakeDesktop) SetHideOnEscape(enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.hideOnEscape = append(f.hideOnEscape, enabled)
	f.settings.HideOnEscape = enabled
	return nil
}

func (f *fakeDesktop) SetHideOnClickOutside(enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.hideOnOutside = append(f.hideOnOutside, enabled)
	f.settings.HideOnClickOutside = enabled
	return nil
}

func (f *fakeDesktop) SetToggleWindowHotkey(accelerator string) error {
	if f.err != nil {
		return f.err
	}
	f.hotkey = append(f.hotkey, accelerator)
	f.settings.ToggleWindowHotkey = accelerator
	return nil
}

func (f *fakeDesktop) ResetToggleHotkey() error {
	if f.err != nil {
		return f.err
	}
	f.resetCalls++
	f.settings.ToggleWindowHotkey = desktop.DefaultToggleHotkey("windows")
	return nil
}

func (f *fakeDesktop) SetAutoStart(enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.autoStart = append(f.autoStart, enabled)
	f.settings.AutoStart = enabled
	return nil
}

func (f *fakeDesktop) SetSSHDeepLinks(enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.deepLinks = append(f.deepLinks, enabled)
	f.settings.SSHDeepLinks = enabled
	return nil
}

func (f *fakeDesktop) SetNotification(event desktop.Event, enabled bool) error {
	if f.err != nil {
		return f.err
	}
	f.notified[event] = enabled
	switch event {
	case desktop.EventSessionEnded:
		f.settings.NotifySessionEnded = enabled
	case desktop.EventConnectionFailed:
		f.settings.NotifyConnectionFailed = enabled
	case desktop.EventForwardingFailed:
		f.settings.NotifyForwardingFailed = enabled
	}
	return nil
}

func TestDesktopStateAppliesSettingsAndUsesErrors(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)

	if !state.SetShowTrayIcon(false) || len(fake.showTray) != 1 || fake.showTray[0] {
		t.Fatal("SetShowTrayIcon did not reach the service")
	}
	if !state.SetCloseToTray(false) || len(fake.closeTray) != 1 {
		t.Fatal("SetCloseToTray did not reach the service")
	}
	if !state.SetSSHDeepLinks(false) || len(fake.deepLinks) != 1 {
		t.Fatal("SetSSHDeepLinks did not reach the service")
	}
	if !state.SetNotification(desktop.EventForwardingFailed, false) ||
		fake.notified[desktop.EventForwardingFailed] {
		t.Fatal("SetNotification did not reach the service")
	}

	*state.Hotkey() = "Ctrl+Shift+Space"
	if !state.ApplyHotkey() || fake.settings.ToggleWindowHotkey != "Ctrl+Shift+Space" {
		t.Fatal("ApplyHotkey did not register and persist the value")
	}
	if !state.ResetHotkey() || fake.resetCalls != 1 || *state.Hotkey() != "Ctrl + `" {
		t.Fatalf("ResetHotkey = %q, resets %d", *state.Hotkey(), fake.resetCalls)
	}

	fake.err = desktop.ErrHotkey
	if state.ApplyHotkey() || state.Problem() != DesktopProblemHotkey {
		t.Fatalf("hotkey problem = %q", state.Problem())
	}
	fake.err = desktop.ErrUnsupported
	if state.SetAutoStart(true) || state.Problem() != DesktopProblemUnsupported {
		t.Fatalf("unsupported problem = %q", state.Problem())
	}
	fake.err = desktop.ErrAutoStart
	if state.SetAutoStart(true) || state.Problem() != DesktopProblemApply {
		t.Fatalf("apply problem = %q", state.Problem())
	}
	fake.err = errors.New("disk full")
	if state.SetCloseToTray(true) || state.Problem() != DesktopProblemSave {
		t.Fatalf("save problem = %q", state.Problem())
	}
}

func TestDesktopStateShowsRegistrationFailureAtLoad(t *testing.T) {
	fake := newFakeDesktop()
	fake.hotkeyErr = desktop.ErrHotkey
	state := NewDesktopState(fake)
	if state.Problem() != DesktopProblemHotkey {
		t.Fatalf("problem = %q", state.Problem())
	}
}

// TestHotkeySectionShowsFourModesAndDropsTheEnabledCheckbox pins the new
// control: the old on/off checkbox is gone and the four modes are all shown.
func TestHotkeySectionShowsFourModesAndDropsTheEnabledCheckbox(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()

	m := locales.In("en")
	for _, label := range []string{
		m.SettingsHotkeyMode(), m.SettingsHotkeyModeNone(),
		m.SettingsHotkeyModeToggle(), m.SettingsHotkeyModeQuake(),
		m.SettingsHotkeyModeTopdown(),
	} {
		if !tester.HasText(label) {
			t.Fatalf("the hotkey section does not show %q: %v", label, tester.Texts())
		}
	}
	if tester.HasText("Enable global hotkey") {
		t.Fatalf("the removed enable checkbox is still rendered: %v", tester.Texts())
	}
}

// TestHotkeyModeRadiosApplyTheSelectedMode drives the actual radios, including
// a switch back and forth, so each selection reaches the service.
func TestHotkeyModeRadiosApplyTheSelectedMode(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsHotkeyModeNone()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.modes) != 1 || fake.modes[0] != desktop.HotkeyModeNone {
		t.Fatalf("modes = %v after clicking none", fake.modes)
	}

	if err := tester.Click(m.SettingsHotkeyModeToggle()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.modes) != 2 || fake.modes[1] != desktop.HotkeyModeToggle {
		t.Fatalf("modes = %v after clicking toggle", fake.modes)
	}

	if err := tester.Click(m.SettingsHotkeyModeTopdown()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.modes) != 3 || fake.modes[2] != desktop.HotkeyModeTopdown {
		t.Fatalf("modes = %v after clicking topdown", fake.modes)
	}
}

// TestHotkeyModeFailureKeepsTheOldSelectionAndShowsTheProblem covers the
// registration failure path: the service rejects the change, the inline
// problem appears, and the old mode stays selected.
func TestHotkeyModeFailureKeepsTheOldSelectionAndShowsTheProblem(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	fake.err = desktop.ErrHotkey
	if err := tester.Click(m.SettingsHotkeyModeTopdown()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if state.Problem() != DesktopProblemHotkey {
		t.Fatalf("problem = %q, want the hotkey problem", state.Problem())
	}
	if fake.settings.HotkeyMode != desktop.HotkeyModeQuake {
		t.Fatalf("mode = %q after a failed change", fake.settings.HotkeyMode)
	}
	if !tester.HasText(m.SettingsHotkeyProblem()) {
		t.Fatalf("the inline hotkey problem is missing: %v", tester.Texts())
	}
}

// TestUnsupportedDropdownDisablesOnlyTheDropdownModes proves a host without
// Panel support hides the reason and keeps none and toggle usable.
func TestUnsupportedDropdownDisablesOnlyTheDropdownModes(t *testing.T) {
	fake := newFakeDesktop()
	fake.caps.Dropdown = false
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if !tester.HasText(m.SettingsHotkeyModeUnsupported()) {
		t.Fatalf("the unsupported reason is missing: %v", tester.Texts())
	}
	if err := tester.Click(m.SettingsHotkeyModeQuake()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.modes) != 0 {
		t.Fatalf("a disabled quake mode was applied: %v", fake.modes)
	}
	if err := tester.Click(m.SettingsHotkeyModeNone()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.modes) != 1 || fake.modes[0] != desktop.HotkeyModeNone {
		t.Fatalf("none was not usable without Dropdown support: %v", fake.modes)
	}
}

// TestHotkeyModesAreSearchable reaches the mode controls through Settings
// search in the source language.
func TestHotkeyModesAreSearchable(t *testing.T) {
	hits := SearchIndex(locales.In("en"))
	for _, query := range []string{"quake", "topdown", "top-down", "no action"} {
		found := false
		for _, hit := range FilterHits(hits, query) {
			if hit.Area == AreaSystem {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query %q does not reach the system area", query)
		}
	}
}

// TestQuakeSourceRadiosApplyTheSelectedSource drives the own/follow selector
// and pins the resize explanation the user needs before choosing follow.
func TestQuakeSourceRadiosApplyTheSelectedSource(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	for _, label := range []string{
		m.SettingsQuakeSource(),
		m.SettingsQuakeSourceOwn(),
		m.SettingsQuakeSourceFollow(),
		m.SettingsQuakeSourceResizeNote(),
	} {
		if !tester.HasText(label) {
			t.Fatalf("the Quake source controls do not show %q: %v", label, tester.Texts())
		}
	}
	if fake.settings.QuakeSource != desktop.QuakeSourceOwn {
		t.Fatalf("initial source = %q, want own", fake.settings.QuakeSource)
	}

	if err := tester.Click(m.SettingsQuakeSourceFollow()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.sources) != 1 || fake.sources[0] != desktop.QuakeSourceFollow {
		t.Fatalf("sources = %v after clicking follow", fake.sources)
	}

	if err := tester.Click(m.SettingsQuakeSourceOwn()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(fake.sources) != 2 || fake.sources[1] != desktop.QuakeSourceOwn {
		t.Fatalf("sources = %v after clicking own", fake.sources)
	}
}

// TestQuakeSourceIsSearchable reaches the source control by its content and by
// the follow wording.
func TestQuakeSourceIsSearchable(t *testing.T) {
	hits := SearchIndex(locales.In("en"))
	for _, query := range []string{"quake source", "content source", "follow"} {
		found := false
		for _, hit := range FilterHits(hits, query) {
			if hit.Area == AreaSystem {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query %q does not reach the system area", query)
		}
	}
}

// TestDropdownBehaviorSectionRendersGeometryAndHideSwitches pins the new
// controls: both heights with their unit selectors and the three independent
// hide switches are visible and start from the persisted values.
func TestDropdownBehaviorSectionRendersGeometryAndHideSwitches(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	for _, label := range []string{
		m.SettingsDropdownBehaviorTitle(),
		m.SettingsDropdownBehaviorDescription(),
		m.SettingsDropdownQuakeHeight(),
		m.SettingsDropdownTopdownHeight(),
		m.SettingsDropdownHideOnBlur(),
		m.SettingsDropdownHideOnEscape(),
		m.SettingsDropdownHideOnClickOutside(),
		m.SettingsDropdownClickOutsideNote(),
	} {
		if !tester.HasText(label) {
			t.Fatalf("the dropdown section does not show %q: %v", label, tester.Texts())
		}
	}
	if got := state.QuakeHeight(); got.Value != "50" || got.Unit != desktop.HeightUnitPercent {
		t.Fatalf("quake height draft = %+v, want 50 percent", got)
	}
	if got := state.TopdownHeight(); got.Value != "80" || got.Unit != desktop.HeightUnitPercent {
		t.Fatalf("topdown height draft = %+v, want 80 percent", got)
	}
}

// TestDropdownLooksAndHideSwitchesAreSearchable reaches the new controls
// through Settings search.
func TestDropdownLooksAndHideSwitchesAreSearchable(t *testing.T) {
	hits := SearchIndex(locales.In("en"))
	for _, query := range []string{"dropdown height", "blur", "escape", "click outside"} {
		found := false
		for _, hit := range FilterHits(hits, query) {
			if hit.Area == AreaSystem {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query %q does not reach the system area", query)
		}
	}
}

// TestHeightDraftAppliesValidValuesAndKeepsInvalidText covers the editable
// height contract: a valid pair reaches the service and is re-displayed from
// what was persisted, while an out-of-range value shows the inline error and
// is never sent to the service.
func TestHeightDraftAppliesValidValuesAndKeepsInvalidText(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)

	state.QuakeHeight().Value = "640"
	state.QuakeHeight().Unit = desktop.HeightUnitPX
	if !state.ApplyQuakeHeight() {
		t.Fatalf("a valid pixel height was rejected: %q", state.Problem())
	}
	if len(fake.quakeHeights) != 1 || fake.quakeHeights[0] != (heightCall{640, desktop.HeightUnitPX}) {
		t.Fatalf("quake height calls = %+v", fake.quakeHeights)
	}
	if state.QuakeHeight().Value != "640" || state.QuakeHeight().Unit != desktop.HeightUnitPX {
		t.Fatalf("draft = %+v after apply", state.QuakeHeight())
	}

	state.TopdownHeight().Value = "75"
	if !state.ApplyTopdownHeight() {
		t.Fatalf("a valid percent height was rejected: %q", state.Problem())
	}
	if len(fake.topdownHeights) != 1 || fake.topdownHeights[0] != (heightCall{75, desktop.HeightUnitPercent}) {
		t.Fatalf("topdown height calls = %+v", fake.topdownHeights)
	}

	state.QuakeHeight().Value = "5"
	if state.ApplyQuakeHeight() {
		t.Fatal("an out-of-range height was accepted")
	}
	if state.Problem() != DesktopProblemHeight {
		t.Fatalf("problem = %q, want the height problem", state.Problem())
	}
	if len(fake.quakeHeights) != 1 {
		t.Fatalf("an invalid height reached the service: %+v", fake.quakeHeights)
	}
	if state.QuakeHeight().Value != "5" {
		t.Fatalf("invalid text was discarded: %+v", state.QuakeHeight())
	}

	state.TopdownHeight().Value = "not a number"
	if state.ApplyTopdownHeight() || state.Problem() != DesktopProblemHeight {
		t.Fatalf("unparsable height = %q, problem = %q", state.TopdownHeight().Value, state.Problem())
	}
	if len(fake.topdownHeights) != 1 {
		t.Fatalf("an unparsable height reached the service: %+v", fake.topdownHeights)
	}

	// The inline error renders beside the controls.
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	if !tester.HasText(locales.In("en").SettingsDropdownHeightInvalid()) {
		t.Fatalf("the height validation error is missing: %v", tester.Texts())
	}
}

// TestHideSwitchesReachTheService proves each hide trigger is an independent
// checkbox writing its own field.
func TestHideSwitchesReachTheService(t *testing.T) {
	fake := newFakeDesktop()
	state := NewDesktopState(fake)
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), 1100, 720)
	tester.SetLocale("en")
	// The System area scrolls; the dropdown card sits below the fold at the
	// default height, so open it fully before clicking its controls.
	tester.SetSize(1100, 1500)
	tester.Frame()
	m := locales.In("en")

	for _, label := range []string{
		m.SettingsDropdownHideOnBlur(),
		m.SettingsDropdownHideOnEscape(),
		m.SettingsDropdownHideOnClickOutside(),
	} {
		if err := tester.Click(label); err != nil {
			t.Fatalf("clicking %q: %v", label, err)
		}
	}
	tester.Frame()
	if len(fake.hideOnBlur) != 1 || fake.hideOnBlur[0] {
		t.Fatalf("blur switch calls = %v", fake.hideOnBlur)
	}
	if len(fake.hideOnEscape) != 1 || fake.hideOnEscape[0] {
		t.Fatalf("escape switch calls = %v", fake.hideOnEscape)
	}
	if len(fake.hideOnOutside) != 1 || fake.hideOnOutside[0] {
		t.Fatalf("click-outside switch calls = %v", fake.hideOnOutside)
	}
}

// TestDesktopSettingsFitAtDefaultAndMinimumSizes renders the full Settings
// surface at 1100x720 and the 720x480 minimum. The scroll container keeps
// off-screen sections out of the viewport; every laid-out line must still fit
// the window's width, and the leading sections must stay ordered.
func TestDesktopSettingsFitAtDefaultAndMinimumSizes(t *testing.T) {
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		fake := newFakeDesktop()
		state := NewDesktopState(fake)
		tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		m := locales.In("en")
		for _, text := range []string{
			m.SettingsAutostartTitle(),
			m.SettingsHotkeyTitle(),
			m.SettingsDropdownBehaviorTitle(),
			m.SettingsTrayTitle(),
			m.SettingsDeepLinksTitle(),
			m.SettingsNotificationsTitle(),
		} {
			r, ok := tester.Find(text)
			if !ok {
				t.Fatalf("%dx%d does not show %q: %v", size[0], size[1], text, tester.Texts())
			}
			const epsilon = 0.5
			if r.X < -epsilon || r.X+r.W > float32(size[0])+epsilon {
				t.Fatalf("%dx%d clips %q horizontally: %+v", size[0], size[1], text, r)
			}
		}

		first, _ := tester.Find(m.SettingsAutostartTitle())
		second, _ := tester.Find(m.SettingsHotkeyTitle())
		third, _ := tester.Find(m.SettingsDropdownBehaviorTitle())
		fourth, _ := tester.Find(m.SettingsTrayTitle())
		if !(first.Y < second.Y && second.Y < third.Y && third.Y < fourth.Y) {
			t.Fatalf("%dx%d section order = %+v %+v %+v %+v", size[0], size[1], first, second, third, fourth)
		}
	}
}

func TestHotkeyProblemRendersBesideItsControlsAndClearsOnReset(t *testing.T) {
	m := locales.In("en")
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		fake := newFakeDesktop()
		fake.hotkeyErr = desktop.ErrHotkey
		state := NewDesktopState(fake)
		if state.Problem() != DesktopProblemHotkey {
			t.Fatalf("startup problem = %q", state.Problem())
		}
		tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: state}), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		problem, ok := tester.Find(m.SettingsHotkeyProblem())
		if !ok {
			t.Fatalf("%dx%d does not show the hotkey problem: %v", size[0], size[1], tester.Texts())
		}
		apply, ok := tester.Find(m.SettingsHotkeyApply())
		if !ok {
			t.Fatalf("%dx%d does not show Apply", size[0], size[1])
		}
		tray, ok := tester.Find(m.SettingsTrayTitle())
		if !ok {
			t.Fatalf("%dx%d does not lay out the tray section", size[0], size[1])
		}
		if problem.Y <= apply.Y || problem.Y >= tray.Y {
			t.Fatalf("%dx%d problem is not inside the hotkey section: problem=%+v apply=%+v tray=%+v",
				size[0], size[1], problem, apply, tray)
		}
		if problem.X < 0 || problem.X+problem.W > float32(size[0]) {
			t.Fatalf("%dx%d clips the hotkey problem: %+v", size[0], size[1], problem)
		}

		// The reset control may sit below the fold at the minimum height; the
		// interaction itself is unrelated to the visual fit checked above.
		tester.SetSize(1100, 720)
		tester.Frame()
		if err := tester.Click(m.SettingsHotkeyReset()); err != nil {
			t.Fatal(err)
		}
		tester.Frame()
		if state.Problem() != DesktopProblemNone {
			t.Fatalf("reset did not clear the problem: %q", state.Problem())
		}
		if tester.HasText(m.SettingsHotkeyProblem()) {
			t.Fatalf("the hotkey problem survived Reset: %v", tester.Texts())
		}
	}
}

func TestDesktopSettingsShowUnsupportedCapabilities(t *testing.T) {
	fake := newFakeDesktop()
	fake.caps = desktop.Capabilities{}
	tester := ui.NewTester(viewAt(AreaSystem, Config{Lock: NewAppLockState(&fakeAppLock{}), Desktop: NewDesktopState(fake)}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")
	if !tester.HasText(m.SettingsAutostartUnsupported()) ||
		!tester.HasText(m.SettingsHotkeyUnsupported()) ||
		!tester.HasText(m.SettingsTrayUnsupported()) ||
		!tester.HasText(m.SettingsNotificationsUnsupported()) {
		t.Fatalf("unsupported settings are not explained: %v", tester.Texts())
	}
}
