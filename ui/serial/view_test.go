package serial

import (
	"errors"
	"fmt"
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

// capabilityErrorTester renders the dialog in the state independent
// acceptance F exercised: 1.5 stop bits (which shows the warning) with 8 data
// bits on Windows, where Connect rejects the combination and keeps the dialog
// open with the platform error.
func capabilityErrorTester(t *testing.T, locale string, width, height int) (*ui.Tester, locales.Messages) {
	t.Helper()
	state := NewState(
		fakeLister([]domainserial.PortInfo{{Path: "COM1"}, {Path: "COM5"}}, nil),
		nil,
		WithCapabilities(domainserial.CapabilitiesFor("windows")),
	)
	state.Show()
	state.SetStopBits("1.5")
	state.SetParity("even")
	state.SetFlowControl("xon/xoff")
	if err := state.Connect(); !errors.Is(err, domainserial.ErrUnsupported) {
		t.Fatalf("Connect error = %v, want the typed unsupported error", err)
	}
	tt := ui.NewTester(View(state), width, height)
	tt.SetLocale(locale)
	tt.Frame()
	return tt, locales.In(locale)
}

// TestViewFitsTheCapabilityErrorAtTheMinimumWindow is ticket 39's F
// regression: the warning and error rows pushed Connect and Cancel below the
// modal card at 720x480, and Cancel 1 px outside the client. The action row
// must stay inside the card and the client in every locale, while the fields
// stay legible and visibly labeled (the labels used to be accessibility names
// only).
func TestViewFitsTheCapabilityErrorAtTheMinimumWindow(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			t.Run(fmt.Sprintf("%s-%dx%d", locale, size[0], size[1]), func(t *testing.T) {
				tt, m := capabilityErrorTester(t, locale, size[0], size[1])
				if bounds := tt.Image().Bounds(); bounds.Dx() != size[0] || bounds.Dy() != size[1] {
					t.Fatalf("frame is %v, want %dx%d DIPs", bounds, size[0], size[1])
				}
				client := ui.Rect{W: float32(size[0]), H: float32(size[1])}
				card := cardBounds(t, tt, m.SerialTitle())
				assertInside(t, "modal card", card, client)

				for _, want := range []string{
					m.SerialTitle(), m.SerialDescription(),
					m.SerialStopBitsWarning(), m.SerialUnsupported(),
					m.SerialConnect(), m.SerialCancel(),
				} {
					rect, ok := tt.Find(want)
					if !ok {
						t.Fatalf("%q is not shown, texts %q", want, tt.Texts())
					}
					assertInside(t, fmt.Sprintf("%q", want), rect, client)
				}
				for _, want := range []string{
					m.SerialStopBitsWarning(), m.SerialUnsupported(),
					m.SerialConnect(), m.SerialCancel(),
				} {
					rect, _ := tt.Find(want)
					assertInside(t, fmt.Sprintf("%q", want), rect, card)
				}

				connect, _ := tt.Find(m.SerialConnect())
				cancel, _ := tt.Find(m.SerialCancel())
				if overlaps(connect, cancel) {
					t.Errorf("Connect %+v overlaps Cancel %+v", connect, cancel)
				}
				if diff := connect.Y - cancel.Y; diff < -4 || diff > 4 {
					t.Errorf("actions are not on one row: Connect %+v Cancel %+v", connect, cancel)
				}
				problem, _ := tt.Find(m.SerialUnsupported())
				if problem.Y+problem.H > connect.Y {
					t.Errorf("status row %+v overlaps the action row %+v", problem, connect)
				}

				// At the minimum size the form scrolls; the first field is
				// visible before scrolling, and the rest after scrolling to the
				// end. At 1100x720 every field is visible at once. The baud
				// input's text is painted by the editor rather than a text
				// element, so its visible drop-down chevron anchors the label.
				checkFieldLabel(t, tt, m.SerialBaudRate(), "▾", card, client)
				if size[1] < 720 {
					description, _ := tt.Find(m.SerialDescription())
					tt.Scroll(float32(size[0])/2, description.Y+description.H+20, 0, 400)
					tt.Frame()
					card = cardBounds(t, tt, m.SerialTitle())
					assertInside(t, "modal card after scrolling", card, client)
					for _, action := range []ui.Rect{connect, cancel} {
						assertInside(t, "action after scrolling", action, card)
					}
				}
				for _, field := range []struct{ label, value string }{
					{m.SerialDataBits(), "8"},
					{m.SerialStopBits(), "1.5"},
					{m.SerialParity(), m.SerialParityEven()},
					{m.SerialFlowControl(), m.SerialFlowXONXOFF()},
				} {
					checkFieldLabel(t, tt, field.label, field.value, card, client)
				}
			})
		}
	}
}

// cardBounds finds the modal card's inner box in the last frame. The card is
// the only undimmed surface, so its background is a uniform block: sample a
// row above the title, inside the panel's padding, then walk the solid run.
func cardBounds(t *testing.T, tt *ui.Tester, title string) ui.Rect {
	t.Helper()
	titleRect, ok := tt.Find(title)
	if !ok {
		t.Fatalf("%q is not shown", title)
	}
	img := tt.Image()
	bounds := img.Bounds()
	sampleX := bounds.Min.X + bounds.Dx()/2
	sampleY := int(titleRect.Y) - 6
	if sampleY <= bounds.Min.Y || sampleY >= bounds.Max.Y {
		t.Fatalf("cannot sample the card above the title %+v", titleRect)
	}
	card := img.RGBAAt(sampleX, sampleY)
	left, right := sampleX, sampleX
	for left > bounds.Min.X && img.RGBAAt(left-1, sampleY) == card {
		left--
	}
	for right < bounds.Max.X-1 && img.RGBAAt(right+1, sampleY) == card {
		right++
	}
	// Walk the vertical extent inside the panel's padding, where no rounded
	// corner or content interrupts the card's background.
	x := left + 24
	if x >= right {
		x = (left + right) / 2
	}
	top, bottom := sampleY, sampleY
	for top > bounds.Min.Y && img.RGBAAt(x, top-1) == card {
		top--
	}
	for bottom < bounds.Max.Y-1 && img.RGBAAt(x, bottom+1) == card {
		bottom++
	}
	return ui.Rect{X: float32(left), Y: float32(top), W: float32(right - left + 1), H: float32(bottom - top + 1)}
}

// checkFieldLabel asserts that label is a visible text line above its
// control's value, not merely the control's accessibility name.
func checkFieldLabel(t *testing.T, tt *ui.Tester, label, value string, card, client ui.Rect) {
	t.Helper()
	labelRect, ok := tt.Find(label)
	if !ok {
		t.Fatalf("field label %q is not shown, texts %q", label, tt.Texts())
	}
	valueRect, ok := tt.Find(value)
	if !ok {
		t.Fatalf("field value %q is not shown, texts %q", value, tt.Texts())
	}
	if labelRect.W <= 0 || labelRect.H <= 0 {
		t.Fatalf("field label %q is clipped: %+v", label, labelRect)
	}
	if labelRect.H > 24 {
		t.Fatalf("field label %q has a control's height (%.1f), so it is not a visible text line", label, labelRect.H)
	}
	if labelRect.Y+labelRect.H > valueRect.Y {
		t.Fatalf("field label %q is not above its value %q: label %+v value %+v",
			label, value, labelRect, valueRect)
	}
	assertInside(t, fmt.Sprintf("field label %q", label), labelRect, card)
	assertInside(t, fmt.Sprintf("field label %q", label), labelRect, client)
}

func assertInside(t *testing.T, name string, inner, outer ui.Rect) {
	t.Helper()
	const tolerance = 1
	if inner.X < outer.X-tolerance || inner.Y < outer.Y-tolerance ||
		inner.X+inner.W > outer.X+outer.W+tolerance ||
		inner.Y+inner.H > outer.Y+outer.H+tolerance {
		t.Errorf("%s at %+v is outside %+v", name, inner, outer)
	}
}

func overlaps(a, b ui.Rect) bool {
	return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
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
