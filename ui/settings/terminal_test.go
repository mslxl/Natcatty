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
	if state.Problem() || redraws != 6 {
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
		tester := ui.NewTester(ViewWithDesktop(
			NewAppLockState(&fakeAppLock{}), nil, state), size[0], size[1])
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
			m.SettingsAppLockTitle(),
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
		fourth, _ := tester.Find(m.SettingsAppLockTitle())
		if !(first.Y < second.Y && second.Y < third.Y && third.Y < fourth.Y) {
			t.Fatalf("%dx%d section order = %+v %+v %+v %+v",
				size[0], size[1], first, second, third, fourth)
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

func TestTerminalSettingsInteractionUpdatesTheService(t *testing.T) {
	fake := newFakeTerminal()
	fake.settings.LogDirectory = `C:\logs`
	state := NewTerminalState(fake)
	tester := ui.NewTester(ViewWithDesktop(
		NewAppLockState(&fakeAppLock{}), nil, state), 1100, 720)
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
