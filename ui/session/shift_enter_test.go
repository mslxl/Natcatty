package sessionui

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

// newShiftEnterSurface opens a Surface over a recording transport with the
// Shift+Enter preference fixed, as the Settings document would report it.
func newShiftEnterSurface(t *testing.T, newline bool) (*Surface, *recordingTransport) {
	t.Helper()
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithShiftEnterNewline(func() bool { return newline }),
	)
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	return surface, transport
}

// TestShiftEnterSendsEscCR proves the preference's on state: Shift+Enter is
// taken before MyGo encodes it and sends exactly 0x1b 0x0d, while plain Enter
// keeps its ordinary carriage return.
func TestShiftEnterSendsEscCR(t *testing.T) {
	surface, transport := newShiftEnterSurface(t, true)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Frame()

	tester.Key(ui.Shift, ui.KeyEnter)
	if !waitFor(t, func() bool { return strings.Contains(transport.input(), "\x1b\r") }) {
		t.Fatalf("Shift+Enter sent %q, want ESC CR", transport.input())
	}

	tester.Key(0, ui.KeyEnter)
	if !waitFor(t, func() bool { return transport.input() == "\x1b\r\r" }) {
		t.Fatalf("plain Enter sent %q, want it to keep the terminal's carriage return",
			transport.input())
	}
}

// TestShiftEnterPreferenceOffKeepsTodaysEncoding proves the off state: the
// handler declines the key and MyGo encodes it, without an ESC CR sequence.
func TestShiftEnterPreferenceOffKeepsTodaysEncoding(t *testing.T) {
	surface, transport := newShiftEnterSurface(t, false)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, true) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Frame()

	tester.Key(ui.Shift, ui.KeyEnter)
	if !waitFor(t, func() bool { return transport.input() != "" }) {
		t.Fatal("Shift+Enter sent nothing with the preference off")
	}
	if got := transport.input(); strings.Contains(got, "\x1b\r") {
		t.Fatalf("Shift+Enter sent %q with the preference off, want the terminal's own encoding", got)
	}
}
