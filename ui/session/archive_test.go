package sessionui

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
)

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

// clickArchive finds the printed name by asking the widget through its own
// link state: a Command+click on a link fills the command and leaves the hover
// marked. The probe does not assume fonts or padding; the pointer condition is
// the same one a user's hover and click satisfy.
func clickArchive(t *testing.T, tester *ui.Tester, surface *Surface, transport *recordingTransport, want, command string) (float32, float32) {
	t.Helper()
	for y := float32(30); y < 120; y += 4 {
		for x := float32(2); x < 200; x += 4 {
			tester.ClickAtWith(ui.Cmd, x, y)
			if transport.input() != command {
				continue
			}
			link, path, at := surface.terminal.HoveredLink()
			if link != want || !path || at.W <= 0 {
				t.Fatalf("click on %q left hover %q path %v rect %+v", want, link, path, at)
			}
			return x, y
		}
	}
	t.Fatalf("no clickable link %q on the terminal", want)
	return 0, 0
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

	want := "tar -xzf 'release.tar.gz'"
	clickArchive(t, tester, surface, transport, "release.tar.gz", want)
	if got := transport.input(); got != want {
		t.Fatalf("click filled %q, want %q", got, want)
	}
	if strings.ContainsAny(transport.input(), "\r\n") {
		t.Fatalf("the filled command was submitted: %q", transport.input())
	}
}

// While a program takes the mouse, the click is the program's report and no
// archive command is filled.
func TestArchiveClickDoesNothingWhileTheProgramTakesTheMouse(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	command := "tar -xzf 'release.tar.gz'"
	x, y := clickArchive(t, tester, surface, transport, "release.tar.gz", command)
	before := transport.input()

	surface.terminal.Feed([]byte("\x1b[?1002h\x1b[?1006h"))
	tester.Frame()
	tester.ClickAtWith(ui.Cmd, x, y)
	if got := transport.input(); got != before || strings.Count(got, command) != 1 {
		t.Fatalf("a reporting program received the archive command: %q", got)
	}
}

// A drag that is selecting keeps the archive link from taking the press.
func TestArchiveClickDoesNothingWhileSelecting(t *testing.T) {
	surface, transport := newArchiveSurface(t)
	surface.terminal.Feed([]byte("release.tar.gz\r\n$ "))
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	command := "tar -xzf 'release.tar.gz'"
	x, y := clickArchive(t, tester, surface, transport, "release.tar.gz", command)

	tester.Press(x, y)
	tester.Move(x+40, y)
	tester.ClickAtWith(ui.Cmd, x, y)
	tester.Release(x+40, y)
	if got := transport.input(); strings.Count(got, command) != 1 {
		t.Fatalf("a selection took the archive command: %q", got)
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
	before := transport.input()
	if !surface.openArchiveLink("release.tar.gz") {
		t.Fatal("an archive was not recognized while a command ran")
	}
	if after := transport.input(); after != before {
		t.Fatalf("a running command got %q", strings.TrimPrefix(after, before))
	}
	if surface.openArchiveLink("notes.txt") {
		t.Fatal("a non-archive path was handled as an archive")
	}
	if after := transport.input(); after != before {
		t.Fatalf("a non-archive changed the input: %q", strings.TrimPrefix(after, before))
	}
}
