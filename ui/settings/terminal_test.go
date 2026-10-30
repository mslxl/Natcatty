package settings

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	"github.com/mslxl/natcatty/ui/locales"
)

type fakeTerminal struct {
	settings domainterminal.Settings
	err      error
	saves    int
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{settings: domainterminal.Defaults()}
}

func (t *fakeTerminal) Settings() domainterminal.Settings { return t.settings }

func (t *fakeTerminal) update(change func(*domainterminal.Settings)) error {
	if t.err != nil {
		return t.err
	}
	change(&t.settings)
	t.saves++
	return nil
}

func (t *fakeTerminal) SetLoggingEnabled(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.LoggingEnabled = enabled })
}

func (t *fakeTerminal) SetLogDirectory(directory string) error {
	return t.update(func(settings *domainterminal.Settings) { settings.LogDirectory = directory })
}

func (t *fakeTerminal) SetLogFormat(format domainterminal.LogFormat) error {
	return t.update(func(settings *domainterminal.Settings) { settings.LogFormat = format })
}

func (t *fakeTerminal) SetLogTimestamps(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.LogTimestamps = enabled })
}

func (t *fakeTerminal) SetOSC52(access domainterminal.ClipboardAccess) error {
	return t.update(func(settings *domainterminal.Settings) { settings.OSC52 = access })
}

func (t *fakeTerminal) SetOSCNotifications(mode domainterminal.NotificationMode) error {
	return t.update(func(settings *domainterminal.Settings) { settings.OSCNotifications = mode })
}

func (t *fakeTerminal) SetEncoding(encoding domainterminal.Encoding) error {
	return t.update(func(settings *domainterminal.Settings) { settings.Encoding = encoding })
}

func (t *fakeTerminal) SetPasteProtection(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.PasteProtection = enabled })
}

func (t *fakeTerminal) SetPasteMinLines(lines int) error {
	return t.update(func(settings *domainterminal.Settings) { settings.PasteMinLines = lines })
}

func (t *fakeTerminal) SetDropAction(action domainterminal.DropAction) error {
	return t.update(func(settings *domainterminal.Settings) { settings.DropAction = action })
}

func (t *fakeTerminal) SetAutocompleteEnabled(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompleteEnabled = enabled })
}

func (t *fakeTerminal) SetAutocompleteGhostText(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompleteGhostText = enabled })
}

func (t *fakeTerminal) SetAutocompletePopupMenu(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompletePopupMenu = enabled })
}

func (t *fakeTerminal) SetAutocompleteDebounceMs(milliseconds int) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompleteDebounceMs = milliseconds })
}

func (t *fakeTerminal) SetAutocompleteMinChars(characters int) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompleteMinChars = characters })
}

func (t *fakeTerminal) SetAutocompleteMaxSuggestions(count int) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompleteMaxSuggestions = count })
}

func (t *fakeTerminal) SetAutocompleteHistoryScope(scope domainterminal.AutocompleteHistoryScope) error {
	return t.update(func(settings *domainterminal.Settings) { settings.AutocompleteHistoryScope = scope })
}

func (t *fakeTerminal) SetKeywordHighlightEnabled(enabled bool) error {
	return t.update(func(settings *domainterminal.Settings) { settings.KeywordHighlightEnabled = enabled })
}

func (t *fakeTerminal) SetKeywordHighlightRules(rules []domainterminal.KeywordHighlightRule) error {
	return t.update(func(settings *domainterminal.Settings) { settings.KeywordHighlightRules = rules })
}

func (t *fakeTerminal) SetConnectionLogRetention(count, days int) error {
	return t.update(func(settings *domainterminal.Settings) {
		settings.ConnectionLogRetentionCount = count
		settings.ConnectionLogRetentionDays = days
	})
}

func (t *fakeTerminal) SetHistoryRetention(count, days int) error {
	return t.update(func(settings *domainterminal.Settings) {
		settings.HistoryRetentionCount = count
		settings.HistoryRetentionDays = days
	})
}

func TestTerminalStateSavesEachSettingAndReportsFailure(t *testing.T) {
	fake := newFakeTerminal()
	state := NewTerminalState(fake)
	redraws := 0
	state.redraw = func() { redraws++ }

	if !state.SetLoggingEnabled(true) || !fake.settings.LoggingEnabled {
		t.Fatal("logging was not enabled")
	}
	if !state.SetLogDirectory(`C:\logs`) || fake.settings.LogDirectory != `C:\logs` {
		t.Fatal("the directory was not stored")
	}
	if !state.SetLogFormat(domainterminal.LogFormatHTML) ||
		fake.settings.LogFormat != domainterminal.LogFormatHTML {
		t.Fatal("the format was not stored")
	}
	if !state.SetLogTimestamps(true) || !fake.settings.LogTimestamps {
		t.Fatal("timestamps were not stored")
	}
	if !state.SetOSC52(domainterminal.ClipboardPrompt) ||
		fake.settings.OSC52 != domainterminal.ClipboardPrompt {
		t.Fatal("the OSC 52 policy was not stored")
	}
	if !state.SetOSCNotifications(domainterminal.NotificationUnfocused) ||
		fake.settings.OSCNotifications != domainterminal.NotificationUnfocused {
		t.Fatal("the notification mode was not stored")
	}
	if !state.SetEncoding(domainterminal.EncodingGB18030) ||
		fake.settings.Encoding != domainterminal.EncodingGB18030 {
		t.Fatal("the terminal encoding was not stored")
	}
	if !state.SetPasteProtection(true) || !fake.settings.PasteProtection {
		t.Fatal("paste protection was not stored")
	}
	if !state.SetPasteMinLines(4) || fake.settings.PasteMinLines != 4 {
		t.Fatal("the paste threshold was not stored")
	}
	if !state.SetDropAction(domainterminal.DropUpload) ||
		fake.settings.DropAction != domainterminal.DropUpload {
		t.Fatal("the drop action was not stored")
	}
	if !state.SetAutocompleteEnabled(false) || fake.settings.AutocompleteEnabled {
		t.Fatal("autocomplete was not disabled")
	}
	if !state.SetAutocompleteGhostText(true) || !fake.settings.AutocompleteGhostText {
		t.Fatal("ghost text was not enabled")
	}
	if !state.SetAutocompletePopupMenu(false) || fake.settings.AutocompletePopupMenu {
		t.Fatal("the popup menu was not disabled")
	}
	if !state.SetAutocompleteDebounceMs(250) || fake.settings.AutocompleteDebounceMs != 250 {
		t.Fatal("the debounce delay was not stored")
	}
	if !state.SetAutocompleteMinChars(3) || fake.settings.AutocompleteMinChars != 3 {
		t.Fatal("the minimum prefix was not stored")
	}
	if !state.SetAutocompleteMaxSuggestions(12) ||
		fake.settings.AutocompleteMaxSuggestions != 12 {
		t.Fatal("the suggestion cap was not stored")
	}
	if !state.SetAutocompleteHistoryScope(domainterminal.AutocompleteHistoryGlobal) ||
		fake.settings.AutocompleteHistoryScope != domainterminal.AutocompleteHistoryGlobal {
		t.Fatal("the history scope was not stored")
	}
	if !state.SetKeywordHighlightEnabled(false) || fake.settings.KeywordHighlightEnabled {
		t.Fatal("keyword highlighting was not disabled")
	}
	if !state.SetKeywordHighlightRuleEnabled("warning", false) ||
		fake.settings.KeywordHighlightRules[1].Enabled {
		t.Fatal("the warning rule was not disabled")
	}
	if !state.SetConnectionLogRetention(25, 7) ||
		fake.settings.ConnectionLogRetentionCount != 25 ||
		fake.settings.ConnectionLogRetentionDays != 7 {
		t.Fatal("connection log retention was not stored")
	}
	if !state.SetHistoryRetention(0, 30) ||
		fake.settings.HistoryRetentionCount != 0 ||
		fake.settings.HistoryRetentionDays != 30 {
		t.Fatal("history retention was not stored")
	}
	if state.Problem() || redraws != 21 {
		t.Fatalf("problem = %v, redraws = %d", state.Problem(), redraws)
	}

	fake.err = errors.New("disk full")
	if state.SetOSC52(domainterminal.ClipboardOff) {
		t.Fatal("a failed save reported success")
	}
	if !state.Problem() {
		t.Fatal("a failed save did not report a problem")
	}
	if fake.settings.OSC52 != domainterminal.ClipboardPrompt {
		t.Fatalf("failed save changed the value to %q", fake.settings.OSC52)
	}
}

func TestTerminalSettingsFitAtDefaultAndMinimumSizes(t *testing.T) {
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		fake := newFakeTerminal()
		fake.settings.LogDirectory = `C:\Users\example\logs`
		state := NewTerminalState(fake)
		tester := ui.NewTester(viewAt(AreaTerminal, Config{
			Lock: NewAppLockState(&fakeAppLock{}), Terminal: state}), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()

		m := locales.In("en")
		wanted := []string{
			m.SettingsTerminalTitle(),
			m.SettingsTerminalLoggingTitle(),
			m.SettingsLogEnabled(),
			m.SettingsLogDirectory(),
			m.SettingsLogFormat(),
			m.SessionLogFormatText(),
			m.SettingsLogTimestamps(),
			m.SettingsOSC52Title(),
			m.SettingsOSC52WriteOnly(),
			m.SettingsOSCNotificationsTitle(),
			m.SettingsOSCNotificationsAlways(),
			m.SettingsTerminalEncoding(),
			m.SettingsPasteProtection(),
			m.SettingsPasteMinLines(),
			m.SettingsDropAction(),
		}
		const epsilon = 0.5
		for _, text := range wanted {
			rect, ok := tester.Find(text)
			if !ok {
				t.Fatalf("%dx%d does not show %q: %v", size[0], size[1], text, tester.Texts())
			}
			if rect.X < -epsilon || rect.X+rect.W > float32(size[0])+epsilon {
				t.Fatalf("%dx%d clips %q horizontally: %+v", size[0], size[1], text, rect)
			}
		}

		first, _ := tester.Find(m.SettingsTerminalTitle())
		second, _ := tester.Find(m.SettingsOSC52Title())
		third, _ := tester.Find(m.SettingsOSCNotificationsTitle())
		if !(first.Y < second.Y && second.Y < third.Y) {
			t.Fatalf("%dx%d section order = %+v %+v %+v",
				size[0], size[1], first, second, third)
		}
	}
}

func TestTerminalOptionLabelsMatchThePolicyValues(t *testing.T) {
	m := locales.In("en")
	for index, access := range []domainterminal.ClipboardAccess{
		domainterminal.ClipboardOff,
		domainterminal.ClipboardWriteOnly,
		domainterminal.ClipboardReadWrite,
		domainterminal.ClipboardPrompt,
	} {
		labels, accesses := osc52Options(m)
		if accesses[index] != access || labels[index] == "" {
			t.Fatalf("OSC 52 option %d = %q/%q", index, labels[index], accesses[index])
		}
		if got := labelForAccess(labels, accesses, access); got != labels[index] {
			t.Fatalf("labelForAccess(%q) = %q, want %q", access, got, labels[index])
		}
	}

	for index, mode := range []domainterminal.NotificationMode{
		domainterminal.NotificationOff,
		domainterminal.NotificationUnfocused,
		domainterminal.NotificationAlways,
	} {
		labels, modes := notificationOptions(m)
		if modes[index] != mode || labels[index] == "" {
			t.Fatalf("notification option %d = %q/%q", index, labels[index], modes[index])
		}
		if got := labelForMode(labels, modes, mode); got != labels[index] {
			t.Fatalf("labelForMode(%q) = %q, want %q", mode, got, labels[index])
		}
	}

	labels, formats := logFormatOptions(m)
	for index, format := range []domainterminal.LogFormat{
		domainterminal.LogFormatText,
		domainterminal.LogFormatRaw,
		domainterminal.LogFormatHTML,
	} {
		if formats[index] != format {
			t.Fatalf("log format option %d = %q, want %q", index, formats[index], format)
		}
		if got := labelForFormat(labels, formats, format); got != labels[index] {
			t.Fatalf("labelForFormat(%q) = %q, want %q", format, got, labels[index])
		}
	}
}

// TestTerminalStateEditsRulePatternsPerLine keeps RE2 repetition counts such
// as {1,7} intact: the editor separates patterns with newlines, not commas.
func TestTerminalStateEditsRulePatternsPerLine(t *testing.T) {
	fake := newFakeTerminal()
	fake.settings.KeywordHighlightRules = []domainterminal.KeywordHighlightRule{
		{ID: "ip", Color: "#EC4899", Enabled: true,
			Patterns: []string{`\b(?:[0-9A-Fa-f]{1,4}:){1,7}:`, `\b10\.0\.0\.1\b`}},
	}
	state := NewTerminalState(fake)
	patterns := state.RulePatterns("ip")
	if *patterns != "\\b(?:[0-9A-Fa-f]{1,4}:){1,7}:\n\\b10\\.0\\.0\\.1\\b" {
		t.Fatalf("draft = %q", *patterns)
	}
	if !state.SetRulePatterns("ip", "\\b{2,4}\\b\n\n  \\bwarn\\b  ") {
		t.Fatal("the patterns were not saved")
	}
	rules := fake.settings.KeywordHighlightRules
	if len(rules) != 1 {
		t.Fatalf("rules = %+v", rules)
	}
	want := []string{`\b{2,4}\b`, `\bwarn\b`}
	if len(rules[0].Patterns) != len(want) {
		t.Fatalf("patterns = %q", rules[0].Patterns)
	}
	for index := range want {
		if rules[0].Patterns[index] != want[index] {
			t.Fatalf("pattern %d = %q, want %q", index, rules[0].Patterns[index], want[index])
		}
	}
	// A blank draft removes the rule instead of leaving an empty pattern.
	if !state.SetRulePatterns("ip", "  \n ") {
		t.Fatal("blank patterns reported failure")
	}
	if len(fake.settings.KeywordHighlightRules) != 0 {
		t.Fatalf("blank patterns kept the rule: %+v", fake.settings.KeywordHighlightRules)
	}
}

func TestTerminalSettingsShowThePowerUxControls(t *testing.T) {
	for _, size := range [][2]int{{1100, 720}, {720, 480}} {
		fake := newFakeTerminal()
		fake.settings.LogDirectory = `C:\logs`
		state := NewTerminalState(fake)
		tester := ui.NewTester(viewAt(AreaTerminal, Config{
			Lock: NewAppLockState(&fakeAppLock{}), Terminal: state}), size[0], size[1])
		tester.SetLocale("en")
		tester.Frame()
		m := locales.In("en")
		for _, text := range []string{
			m.SettingsAutocompleteTitle(),
			m.SettingsAutocompleteEnabled(),
			m.SettingsAutocompletePopupMenu(),
			m.SettingsAutocompleteGhostText(),
			m.SettingsAutocompleteMinChars(),
			m.SettingsAutocompleteMaxSuggestions(),
			m.SettingsAutocompleteDebounce(),
			m.SettingsAutocompleteHistoryScope(),
			m.SettingsAutocompleteScopeHost(),
			m.SettingsKeywordHighlightTitle(),
			m.SettingsKeywordHighlightEnabled(),
			m.SettingsKeywordHighlightPatterns(),
			"Error",
		} {
			if !tester.HasText(text) {
				t.Fatalf("%dx%d does not show %q: %v", size[0], size[1], text, tester.Texts())
			}
		}
	}
}

func TestTerminalSettingsInteractionUpdatesTheService(t *testing.T) {
	fake := newFakeTerminal()
	fake.settings.LogDirectory = `C:\logs`
	state := NewTerminalState(fake)
	tester := ui.NewTester(viewAt(AreaTerminal, Config{Lock: NewAppLockState(&fakeAppLock{}), Terminal: state}), 1100, 720)
	tester.SetLocale("en")
	tester.Frame()
	m := locales.In("en")

	if err := tester.Click(m.SettingsLogEnabled()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !fake.settings.LoggingEnabled {
		t.Fatal("the logging checkbox did not reach the service")
	}

	if err := tester.Click(m.SettingsLogTimestamps()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !fake.settings.LogTimestamps {
		t.Fatal("the timestamps checkbox did not reach the service")
	}

	if err := tester.Click(m.SettingsOSC52WriteOnly()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if !tester.HasText(m.SettingsOSC52Off()) || !tester.HasText(m.SettingsOSC52ReadWrite()) ||
		!tester.HasText(m.SettingsOSC52Prompt()) {
		t.Fatalf("the OSC 52 menu did not open: %v", tester.Texts())
	}
	if err := tester.Click(m.SettingsOSC52Prompt()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if fake.settings.OSC52 != domainterminal.ClipboardPrompt {
		t.Fatalf("OSC 52 policy = %q, want prompt", fake.settings.OSC52)
	}
}
