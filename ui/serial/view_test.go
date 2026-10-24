package serial

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	"github.com/mslxl/natcatty/ui/locales"
)

func dialogTester(t *testing.T, state *State) *ui.Tester {
	t.Helper()
	tt := ui.NewTester(View(state), 900, 760)
	tt.SetLocale("en")
	tt.Frame()
	return tt
}

func TestViewShowsTheFormAndDetectedPorts(t *testing.T) {
	state := NewState(fakeLister([]domainserial.PortInfo{
		{Path: "/dev/ttyUSB0", Manufacturer: "FTDI"},
		{Path: "/dev/ttyACM0"},
	}, nil), nil)
	state.Show()
	tt := dialogTester(t, state)
	m := locales.In("en")

	for _, text := range tt.Texts() {
		rect, ok := tt.Find(text)
		if !ok {
			continue
		}
		if rect.X < -0.5 || rect.X+rect.W > 900.5 {
			t.Errorf("%q overflows the dialog horizontally: %+v", text, rect)
		}
	}
	for _, want := range []string{
		m.SerialTitle(),
		m.SerialDescription(),
		m.SerialPort(),
		m.SerialBaudRate(),
		m.SerialDataBits(),
		m.SerialStopBits(),
		m.SerialParity(),
		m.SerialFlowControl(),
		m.SerialRefresh(),
		m.SerialConnect(),
		m.SerialCancel(),
		"/dev/ttyUSB0",
		"/dev/ttyUSB0 — FTDI",
		"/dev/ttyACM0",
	} {
		if !tt.HasText(want) {
			t.Errorf("dialog does not show %q, texts %q", want, tt.Texts())
		}
	}
}

func TestViewShowsTheEmptyAndFailedEnumerationStates(t *testing.T) {
	empty := NewState(fakeLister(nil, nil), nil)
	empty.Show()
	tt := dialogTester(t, empty)
	if !tt.HasText(locales.In("en").SerialNoPorts()) {
		t.Fatalf("empty dialog does not explain that no port was detected, texts %q", tt.Texts())
	}

	failed := NewState(fakeLister(nil, errors.New("registry unavailable")), nil)
	failed.Show()
	tt = dialogTester(t, failed)
	if !tt.HasText(locales.In("en").SerialPortsFailed()) {
		t.Errorf("failed enumeration does not show its localized message, texts %q", tt.Texts())
	}
}

func TestViewChoosesADetectedPort(t *testing.T) {
	state := NewState(fakeLister([]domainserial.PortInfo{
		{Path: "/dev/ttyUSB0"},
		{Path: "/dev/ttyACM0"},
	}, nil), nil)
	state.Show()
	tt := dialogTester(t, state)

	if err := tt.Click("/dev/ttyACM0"); err != nil {
		t.Fatalf("click second port: %v", err)
	}
	tt.Frame()
	if state.Port() != "/dev/ttyACM0" {
		t.Fatalf("port = %q, want /dev/ttyACM0", state.Port())
	}
}

func TestViewSelectsFlowControlAndParity(t *testing.T) {
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyUSB0"}}, nil), nil)
	state.Show()
	state.SetParity("even")
	tt := dialogTester(t, state)

	// The parity trigger now reads "Even", so the only "None" is the flow
	// control's current value.
	if err := tt.Click("None"); err != nil {
		t.Fatalf("open flow control: %v", err)
	}
	tt.Frame()
	if err := tt.Click("RTS/CTS (Hardware)"); err != nil {
		t.Fatalf("choose RTS/CTS: %v", err)
	}
	tt.Frame()
	if state.FlowControl() != "rts/cts" {
		t.Fatalf("flow control = %q, want rts/cts", state.FlowControl())
	}

	// The flow trigger now reads "XON/XOFF (Software)" and the parity trigger
	// is back to "None", so the parity control is the only one that shows it.
	state.SetFlowControl("xon/xoff")
	state.SetParity("none")
	tt.Frame()
	if err := tt.Click("None"); err != nil {
		t.Fatalf("open parity: %v", err)
	}
	tt.Frame()
	if err := tt.Click("Mark"); err != nil {
		t.Fatalf("choose Mark: %v", err)
	}
	tt.Frame()
	if state.Parity() != "mark" {
		t.Fatalf("parity = %q, want mark", state.Parity())
	}
}

func TestViewSelectsStopBitsAndWarnsAboutOneAndAHalf(t *testing.T) {
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyUSB0"}}, nil), nil)
	state.Show()
	tt := dialogTester(t, state)

	if err := tt.Click("1"); err != nil {
		t.Fatalf("open stop bits: %v", err)
	}
	tt.Frame()
	if err := tt.Click("1.5"); err != nil {
		t.Fatalf("choose 1.5: %v", err)
	}
	tt.Frame()
	if state.StopBits() != "1.5" {
		t.Fatalf("stop bits = %q, want 1.5", state.StopBits())
	}
	if !tt.HasText(locales.In("en").SerialStopBitsWarning()) {
		t.Fatalf("dialog does not warn about 1.5 stop bits, texts %q", tt.Texts())
	}
}

func TestViewConnectOpensTheSessionAndCloses(t *testing.T) {
	opener := &recordedOpen{}
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyUSB0"}}, nil), opener.open)
	state.Show()
	state.SetFlowControl("xon/xoff")
	tt := dialogTester(t, state)

	if err := tt.Click(locales.In("en").SerialConnect()); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	tt.Frame()
	if opener.count() != 1 {
		t.Fatalf("opener calls = %d, want 1", opener.count())
	}
	cfg := opener.last(t)
	if cfg.Path != "/dev/ttyUSB0" || cfg.FlowControl != domainserial.FlowXONXOFF {
		t.Fatalf("opened config = %+v", cfg)
	}
	if state.Visible() {
		t.Fatal("Connect left the dialog visible")
	}
}

func TestViewConnectShowsAValidationProblemAndStaysOpen(t *testing.T) {
	opener := &recordedOpen{}
	state := NewState(fakeLister(nil, nil), opener.open)
	state.Show()
	tt := dialogTester(t, state)

	if err := tt.Click(locales.In("en").SerialConnect()); err != nil {
		t.Fatalf("click Connect: %v", err)
	}
	tt.Frame()
	if !state.Visible() {
		t.Fatal("invalid Connect closed the dialog")
	}
	if opener.count() != 0 {
		t.Fatalf("opener calls = %d, want 0", opener.count())
	}
	if !tt.HasText(locales.In("en").SerialInvalidConfiguration()) {
		t.Fatalf("dialog does not show the validation problem, texts %q", tt.Texts())
	}
}

func TestViewCancelClosesWithoutOpening(t *testing.T) {
	opener := &recordedOpen{}
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyUSB0"}}, nil), opener.open)
	state.Show()
	tt := dialogTester(t, state)

	if err := tt.Click(locales.In("en").SerialCancel()); err != nil {
		t.Fatalf("click Cancel: %v", err)
	}
	tt.Frame()
	if state.Visible() {
		t.Fatal("Cancel left the dialog visible")
	}
	if opener.count() != 0 {
		t.Fatalf("opener calls = %d, want 0", opener.count())
	}
}
