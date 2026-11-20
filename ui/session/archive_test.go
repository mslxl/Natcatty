package sessionui

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

// surfaceFrameTop is the toolbar height surfaceFrame puts above the terminal,
// so a link rectangle, which HoveredLink reports relative to the view, maps
// back to window DIPs.
const surfaceFrameTop = 30

// archiveFlushMarker is written through the terminal's own input queue after a
// click. InsertInput and that queue share one FIFO, so once the marker arrives
// every fill queued by the click has arrived too.
const archiveFlushMarker = "archive-test-flush"

func newArchiveSurface(t *testing.T) (*Surface, *recordingTransport) {
	t.Helper()
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session, WithRedraw(func() {}))
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	return surface, transport
}

// archiveLinkPoint finds the printed name through the pane's link state and
// returns the center of the rectangle it reports. The terminal's Move helper
// cannot hold a modifier (it resets the held state on each move), so the probe
// is a Command-click; the test feeds the name at the start of row 0, where the
// first cell-sized step lands and the scan stops. The click fills the command,
// so callers that need a second click wait for the fill first. The rectangle is
// in view coordinates; surfaceFrameTop maps it to window DIPs.
func archiveLinkPoint(t *testing.T, tester *ui.Tester, surface *Surface, want string) (float32, float32) {
	t.Helper()
	// The live grid is flowPadY (10) inside the flow and livePad (6) inside
	// the terminal's rounded background, so the first row starts 16 DIP below
	// the terminal view and twenty DIPs land on it whatever the cell height.
	// The probe stays inside that inset on the left too: a press in the
	// padding is the flow's, not the grid's.
	const firstRowY = surfaceFrameTop + 20
	for x := float32(20); x < 300; x += 2 {
		tester.ClickAtWith(ui.Cmd, x, firstRowY)
		link, path, at := surface.HoveredLink()
		if link != want {
			continue
		}
		if !path || at.W <= 0 || at.H <= 0 {
			t.Fatalf("link %q has path %v rect %+v", want, path, at)
		}
		return at.X + at.W/2, surfaceFrameTop + at.Y + at.H/2
	}
	t.Fatalf("no Command-clickable link %q on the first row", want)
	return 0, 0
}

// waitArchiveTransport waits until the transport holds want. A fill is queued
// to the Session's write goroutine, so an immediate read races the writer.
func waitArchiveTransport(t *testing.T, transport *recordingTransport, want string) string {
	t.Helper()
	got := ""
	if !waitFor(t, func() bool { got = transport.input(); return got == want }) {
		t.Fatalf("transport input = %q, want %q", got, want)
	}
	return got
}

// flushArchiveInput queues marker behind whatever the click path queued and
// waits for it to be written. The FIFO orders the marker after any archive
// command, so the negative assertions are condition waits on a real event, not
// sleeps.
func flushArchiveInput(t *testing.T, surface *Surface, transport *recordingTransport, marker string) string {
	t.Helper()
	surface.terminal.Send([]byte(marker))
	got := ""
	if !waitFor(t, func() bool { got = transport.input(); return strings.Contains(got, marker) }) {
		t.Fatalf("the flush marker never reached the transport: %q", got)
	}
	return got
}

// The reporting gate reads the emulator's own mode state, and a later reset of
// one mode does not mask another that is still on.
func TestMouseReportingEnabledReadsTheVTSnapshot(t *testing.T) {
	cases := []struct {
		snapshot string
		want     bool
	}{
		{"", false},
		{"plain text", false},
		{"\x1b[?1000h", true},
		{"\x1b[?1001h", true},
		{"\x1b[?1002h\x1b[?1006h", true},
		{"\x1b[?9h\x1b[?9l", false},
		{"\x1b[?1000h\x1b[?1002l", true},
		{"\x1b[?1006h", false},
	}
	for _, testCase := range cases {
		if got := mouseReportingEnabled([]byte(testCase.snapshot)); got != testCase.want {
			t.Errorf("mouseReportingEnabled(%q) = %v, want %v", testCase.snapshot, got, testCase.want)
		}
	}
}

// A printed archive name shows as a link and a Command+click fills its
// extraction command at the line editor without submitting it.
func TestArchiveNameHoverAndClickFillsTheCommand(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	archiveLinkPoint(t, tester, surface, "release.tar.gz")
	want := "tar -xzf 'release.tar.gz'"
	got := waitArchiveTransport(t, transport, want)
	if strings.ContainsAny(got, "\r\n") {
		t.Fatalf("the filled command was submitted: %q", got)
	}
}

// While a program takes the mouse, the click is the program's mouse report:
// the fork suppresses the link, so nothing is filled and the program receives
// the press it asked for.
func TestArchiveClickIsForwardedWhileTheProgramTakesTheMouse(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	x, y := archiveLinkPoint(t, tester, surface, "release.tar.gz")
	command := "tar -xzf 'release.tar.gz'"
	// The probe's click filled the command once. Wait for that fill so the
	// baseline below is stable before tracking takes the pointer.
	waitArchiveTransport(t, transport, command)

	// 1002 is button-event tracking, 1006 makes the report SGR-encoded.
	surface.terminal.Feed([]byte("\x1b[?1002h\x1b[?1006h"))
	tester.Frame()
	before := transport.input()
	tester.ClickAtWith(ui.Cmd, x, y)

	if link, path, at := surface.HoveredLink(); link != "" || path || at.W != 0 {
		t.Fatalf("mouse tracking still offered link %q path %v rect %+v", link, path, at)
	}
	added := ""
	if !waitFor(t, func() bool {
		added = strings.TrimPrefix(transport.input(), before)
		return strings.Contains(added, "\x1b[<")
	}) {
		t.Fatalf("Command+click was not reported to the program: %q", added)
	}
	if strings.Count(transport.input(), command) != 1 {
		t.Fatalf("a mouse-tracking program received the archive command: %q", transport.input())
	}
}

// DEC 1001 (highlight tracking) is a mode the C parser drops, so the fork
// records it itself. A program that asks only for 1001 still takes the mouse:
// the click is reported to it and the archive command is not filled. The
// sequence is split across two feeds to pin the fork's carry across reads.
// Resetting 1001 gives the link back.
func TestArchiveClickIsForwardedUnderHighlightTracking(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	x, y := archiveLinkPoint(t, tester, surface, "release.tar.gz")
	command := "tar -xzf 'release.tar.gz'"
	// The probe's click filled the command once; wait for it before the
	// baseline below, as the other reporting test does.
	waitArchiveTransport(t, transport, command)

	// 1001 is split mid-parameter. 1006 makes the report SGR-encoded, so its
	// presence proves the fork fed the synthetic tracking mode to the encoder.
	surface.terminal.Feed([]byte("\x1b[?10"))
	surface.terminal.Feed([]byte("01h\x1b[?1006h"))
	tester.Frame()
	before := transport.input()
	tester.ClickAtWith(ui.Cmd, x, y)

	if link, path, at := surface.HoveredLink(); link != "" || path || at.W != 0 {
		t.Fatalf("highlight tracking still offered link %q path %v rect %+v", link, path, at)
	}
	added := ""
	if !waitFor(t, func() bool {
		added = strings.TrimPrefix(transport.input(), before)
		return strings.Contains(added, "\x1b[<")
	}) {
		t.Fatalf("Command+click under 1001 was not reported to the program: %q", added)
	}
	if strings.Count(transport.input(), command) != 1 {
		t.Fatalf("a 1001 program received the archive command: %q", transport.input())
	}

	// 1001 off: the link is the terminal's again.
	surface.terminal.Feed([]byte("\x1b[?1001l\x1b[?1006l"))
	tester.Frame()
	tester.ClickAtWith(ui.Cmd, x, y)
	got := flushArchiveInput(t, surface, transport, archiveFlushMarker+"-1001-off")
	if strings.Count(got, command) != 2 {
		t.Fatalf("with 1001 off the click did not fill the command again: %q", got)
	}
}

// A DEC mouse mode split across reads must still reach the emulator's parser
// while 1001 is on. The fork used to inject its synthetic 1000 as raw VT bytes
// after every feed, aborting the partial sequence: the program's 1002 was lost,
// the fork still suppressed the link, and the click became a dead click that
// neither filled the command nor reached the program.
func TestArchiveClickIsForwardedAfterASplitMouseMode(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	x, y := archiveLinkPoint(t, tester, surface, "release.tar.gz")
	command := "tar -xzf 'release.tar.gz'"
	waitArchiveTransport(t, transport, command)

	// 1001 turns the synthetic mode on; the 1002 sequence is then split so the
	// C parser holds a partial CSI across the next feed. 1006 makes the report
	// SGR-encoded, which proves the encoder read the parser's real 1002.
	surface.terminal.Feed([]byte("\x1b[?1001h"))
	surface.terminal.Feed([]byte("\x1b[?1002"))
	surface.terminal.Feed([]byte("h\x1b[?1006h"))
	tester.Frame()
	before := transport.input()
	tester.ClickAtWith(ui.Cmd, x, y)

	if link, path, at := surface.HoveredLink(); link != "" || path || at.W != 0 {
		t.Fatalf("mouse tracking still offered link %q path %v rect %+v", link, path, at)
	}
	added := ""
	if !waitFor(t, func() bool {
		added = strings.TrimPrefix(transport.input(), before)
		return strings.Contains(added, "\x1b[<")
	}) {
		t.Fatalf("a split 1002 left the click dead: %q", added)
	}
	if strings.Count(transport.input(), command) != 1 {
		t.Fatalf("a mouse-tracking program received the archive command: %q", transport.input())
	}
}

// A drag that is selecting keeps the archive link from taking the press.
func TestArchiveClickDoesNothingWhileSelecting(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	x, y := archiveLinkPoint(t, tester, surface, "release.tar.gz")
	command := "tar -xzf 'release.tar.gz'"
	// The probe's click filled the command once.
	waitArchiveTransport(t, transport, command)

	tester.Press(x, y)
	tester.Move(x+40, y)
	tester.ClickAtWith(ui.Cmd, x, y)
	tester.Release(x+40, y)
	got := flushArchiveInput(t, surface, transport, archiveFlushMarker)
	if strings.Count(got, command) != 1 {
		t.Fatalf("a selection filled the archive command again: %q", got)
	}
}

// The fill is a prompt action: a running command keeps its input, and a link
// that is not an archive falls through untouched.
func TestArchiveFillWaitsForThePromptAndIgnoresOtherLinks(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Type("sleep 5")
	tester.Key(0, ui.KeyEnter)
	if !waitFor(t, func() bool { return surface.session.Snapshot().Command.Running }) {
		t.Fatal("the command did not start")
	}

	if !surface.openArchiveLink("release.tar.gz") {
		t.Fatal("an archive was not recognized while a command ran")
	}
	got := flushArchiveInput(t, surface, transport, archiveFlushMarker+"-running")
	if strings.Contains(got, "tar -xzf") {
		t.Fatalf("a running command got an archive fill: %q", got)
	}

	if surface.openArchiveLink("notes.txt") {
		t.Fatal("a non-archive path was handled as an archive")
	}
	got = flushArchiveInput(t, surface, transport, archiveFlushMarker+"-other")
	if strings.Contains(got, "tar -xzf") {
		t.Fatalf("a non-archive changed the input: %q", got)
	}
}

// A program that keeps 1002 set while resetting 1000 still takes the mouse:
// the library's encoder follows the last sequence, so a reset of 1000 used to
// leave it dead while the 1002 bit stayed set. The click then neither filled
// the archive command nor reached the program. The fork restores 1002, so the
// press is reported and the link stays suppressed.
func TestArchiveClickIsForwardedAfterAProgramResetsAnotherMouseMode(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	x, y := archiveLinkPoint(t, tester, surface, "release.tar.gz")
	command := "tar -xzf 'release.tar.gz'"
	// The probe's click filled the command once; wait for it before the
	// baseline below, as the other reporting tests do.
	waitArchiveTransport(t, transport, command)

	// 1002 is button-event tracking and 1006 makes the report SGR-encoded.
	// The program then resets 1000, which is not the mode it is using.
	surface.terminal.Feed([]byte("\x1b[?1002h\x1b[?1006h"))
	surface.terminal.Feed([]byte("\x1b[?1000l"))
	tester.Frame()
	before := transport.input()
	tester.ClickAtWith(ui.Cmd, x, y)

	if link, path, at := surface.HoveredLink(); link != "" || path || at.W != 0 {
		t.Fatalf("mouse tracking still offered link %q path %v rect %+v", link, path, at)
	}
	added := ""
	if !waitFor(t, func() bool {
		added = strings.TrimPrefix(transport.input(), before)
		return strings.Contains(added, "\x1b[<")
	}) {
		t.Fatalf("a 1000 reset left the 1002 click dead: %q", added)
	}
	if strings.Count(transport.input(), command) != 1 {
		t.Fatalf("a mouse-tracking program received the archive command: %q", transport.input())
	}
}
