// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"strings"
	"testing"
)

func rowRunes(fb *Framebuffer, row int) string {
	var out strings.Builder
	for col := 0; col < fb.DS.Width(); col++ {
		fb.Cell(row, col).PrintGrapheme(&out)
	}
	return out.String()
}

func TestRuneWidth(t *testing.T) {
	cases := []struct {
		name string
		r    rune
		want int
	}{
		{"ascii", 'A', 1},
		{"latin", 'é', 1},
		{"nbhspace", 0xa0, 1},
		{"ambiguous", 0x00b7, 1},
		{"cjk-wide", '界', 2},
		{"fullwidth", 'Ａ', 2},
		{"emoji", '😀', 2},
		{"combining", 0x0301, 0},
		{"zwj-format", 0x200d, 0},
		{"nul", 0, -1},
		{"bell", 0x07, -1},
		{"del", 0x7f, -1},
		{"c1", 0x85, -1},
	}
	for _, testCase := range cases {
		if got := RuneWidth(testCase.r); got != testCase.want {
			t.Errorf("%s: RuneWidth(%U) = %d, want %d", testCase.name, testCase.r, got, testCase.want)
		}
	}
}

// TestCompleteChunkingIsByteInvariant proves parser, UTF-8, dispatcher, and OSC
// state survive any host-state boundary. Stock receives terminal octets in
// arbitrary state diffs, so every split must have the same terminal state.
func TestCompleteChunkingIsByteInvariant(t *testing.T) {
	inputs := []string{
		"A界é\x1b[31;1mR\x1b[0m\r\n" +
			"\x1b]2;title界\x07\x1b[?2004hX\x1b[?2004l" +
			"\x1b[2;3H\x1b[4hY\x1b[4l\x84Z\xc3\xa9\xe2\x82\xac\xf0\x9f\x98\x80",
		"\xffA\xc3B\xe2\x82C\xf0\x9f\x92D\xed\xa0\x80\xc0\xaf\xe0\x80\xaf",
		"\x1b]52;c;Y2xpcGJvYXJk\x07\x1b[38;5;196mQ\x1b[?1000h\x1b[?1006h",
	}
	for index, input := range inputs {
		whole := NewComplete(12, 4)
		whole.Act(input)
		want := dumpFramebuffer(whole.Framebuffer())

		bytewise := NewComplete(12, 4)
		for i := 0; i < len(input); i++ {
			bytewise.Act(input[i : i+1])
		}
		if got := dumpFramebuffer(bytewise.Framebuffer()); got != want {
			t.Fatalf("input %d: byte-at-a-time state differs\n got:\n%s\nwant:\n%s", index, got, want)
		}
		for split := 1; split < len(input); split++ {
			splitTerminal := NewComplete(12, 4)
			splitTerminal.Act(input[:split])
			splitTerminal.Act(input[split:])
			if got := dumpFramebuffer(splitTerminal.Framebuffer()); got != want {
				t.Fatalf("input %d split at %d: state differs\n got:\n%s\nwant:\n%s",
					index, split, got, want)
			}
		}
	}
}

func TestCompleteCloneIsIndependent(t *testing.T) {
	base := NewComplete(10, 3)
	base.Act("AB")
	clone := base.Clone()
	base.Act("C")
	clone.Act("D")
	if got := rowRunes(base.Framebuffer(), 0); !strings.HasPrefix(got, "ABC") {
		t.Fatalf("base row = %q", got)
	}
	if got := rowRunes(clone.Framebuffer(), 0); !strings.HasPrefix(got, "ABD") {
		t.Fatalf("clone row = %q", got)
	}

	// A clone taken inside a CSI sequence must carry the dispatcher's collected
	// parameters, not merely the visible framebuffer.
	base = NewComplete(10, 3)
	base.Act("\x1b[38;5;")
	clone = base.Clone()
	base.Act("1mX")
	clone.Act("2mY")
	baseCell := base.Framebuffer().Cell(0, 0)
	cloneCell := clone.Framebuffer().Cell(0, 0)
	if baseCell.Renditions.SGR() == cloneCell.Renditions.SGR() {
		t.Fatalf("clone reused the dispatcher state: both SGR %q", baseCell.Renditions.SGR())
	}
	if baseCell.Contents != "X" || cloneCell.Contents != "Y" {
		t.Fatalf("cells = %q/%q, want X/Y", baseCell.Contents, cloneCell.Contents)
	}

	// OSC payload chunks are dispatcher state too.
	base = NewComplete(10, 3)
	base.Act("\x1b]2;par")
	clone = base.Clone()
	base.Act("tial\x07")
	clone.Act("tial2\x07")
	if string(base.Framebuffer().WindowTitle) != "partial" ||
		string(clone.Framebuffer().WindowTitle) != "partial2" {
		t.Fatalf("titles = %q/%q", string(base.Framebuffer().WindowTitle),
			string(clone.Framebuffer().WindowTitle))
	}
}

func TestCompleteEchoAck(t *testing.T) {
	terminal := NewComplete(10, 3)
	if !terminal.ApplyEchoAck(3) {
		t.Fatal("first echo ack did not change")
	}
	if terminal.ApplyEchoAck(2) {
		t.Fatal("stale echo ack changed the value")
	}
	if terminal.ApplyEchoAck(3) {
		t.Fatal("duplicate echo ack reported a change")
	}
	if got := terminal.EchoAck(); got != 3 {
		t.Fatalf("echo ack = %d, want 3", got)
	}

	terminal = NewComplete(10, 3)
	terminal.RegisterInputFrame(1, 100)
	terminal.RegisterInputFrame(2, 120)
	if !terminal.SetEchoAck(150) || terminal.EchoAck() != 1 {
		t.Fatalf("ack at 150 = %d, want 1", terminal.EchoAck())
	}
	if !terminal.SetEchoAck(200) || terminal.EchoAck() != 2 {
		t.Fatalf("ack at 200 = %d, want 2", terminal.EchoAck())
	}
	// SetEchoAck drops history older than the acknowledged frame, leaving one
	// timed entry, which has no next expiry.
	if got := terminal.WaitTime(200); got != int(^uint(0)>>1) {
		t.Fatalf("WaitTime after pruning = %d, want max", got)
	}

	terminal = NewComplete(10, 3)
	terminal.RegisterInputFrame(1, 100)
	terminal.RegisterInputFrame(2, 120)
	if got := terminal.WaitTime(150); got != 20 {
		t.Fatalf("WaitTime = %d, want 20", got)
	}
}

func TestUserInput(t *testing.T) {
	translate := func(appMode bool, chunks ...string) string {
		input := NewUserInput()
		var out strings.Builder
		for _, chunk := range chunks {
			for i := 0; i < len(chunk); i++ {
				out.WriteString(input.Input(chunk[i], appMode))
			}
		}
		return out.String()
	}

	if got := translate(false, "a"); got != "a" {
		t.Fatalf("plain byte = %q", got)
	}
	if got := translate(false, "\x1bOA"); got != "\x1b[A" {
		t.Fatalf("normal SS3 A = %q, want CSI A", got)
	}
	if got := translate(true, "\x1bOA"); got != "\x1bOA" {
		t.Fatalf("application SS3 A = %q, want SS3 A", got)
	}
	if got := translate(false, "\x1b", "O", "A"); got != "\x1b[A" {
		t.Fatalf("split SS3 A = %q, want CSI A", got)
	}
	if got := translate(false, "\x1bOP"); got != "\x1bOP" {
		t.Fatalf("PF1 = %q, want SS3 P unchanged", got)
	}
	if got := translate(false, "\x1bx"); got != "\x1bx" {
		t.Fatalf("non-SS3 escape = %q, want both bytes", got)
	}
	input := NewUserInput()
	for i := 0; i < len("\x1bO"); i++ {
		input.Input("\x1bO"[i], false)
	}
	input.Reset()
	if got := input.Input('A', false); got != "A" {
		t.Fatalf("Reset did not return to ground: %q", got)
	}
}

func TestNotificationEngine(t *testing.T) {
	now := uint64(0)
	engine := NewNotificationEngine(func() uint64 { return now })
	engine.SetEscapeKeyString("Ctrl-^")
	now = 1000
	engine.SetNotificationString([]rune("Hello界"), false, true)

	fb := NewFramebuffer(60, 2)
	engine.Apply(fb)
	// The wide character occupies two cells, and the bar prefilled both with
	// spaces; PrintGrapheme therefore emits the wide rune plus a trailing cell.
	want := "mosh: Hello界  [To quit: Ctrl-^ .]"
	if got := strings.TrimRight(rowRunes(fb, 0), " "); !strings.HasPrefix(got, want) {
		t.Fatalf("notification row = %q, want prefix %q", got, want)
	}
	if fb.DS.CursorVisible {
		t.Fatal("notification bar did not hide a cursor on row 0")
	}
	cell := fb.Cell(0, 0)
	if cell.Renditions.Foreground != 37 || cell.Renditions.Background != 44 ||
		!cell.Renditions.GetAttribute(AttrBold) {
		t.Fatalf("notification rendition = %+v", cell.Renditions)
	}

	// A non-permanent message expires one second after it was set.
	now = 2001
	fb = NewFramebuffer(30, 2)
	engine.AdjustMessage()
	engine.Apply(fb)
	if got := rowRunes(fb, 0); got != strings.Repeat(" ", 30) {
		t.Fatalf("expired message row = %q, want a blank row", got)
	}
	if len(engine.NotificationString()) != 0 {
		t.Fatalf("expired message = %q", string(engine.NotificationString()))
	}

	// With no message and no recent server word, stock draws the countup bar.
	now = 0
	engine = NewNotificationEngine(func() uint64 { return now })
	now = 8000
	fb = NewFramebuffer(40, 2)
	engine.Apply(fb)
	if got := rowRunes(fb, 0); !strings.HasPrefix(got, "mosh: Last contact 8 seconds ago.") {
		t.Fatalf("countup row = %q", got)
	}
	if got := engine.WaitTime(); got != 1000 {
		t.Fatalf("countup WaitTime = %d, want 1000", got)
	}
	now = 61001
	if got := engine.WaitTime(); got != 3000 {
		t.Fatalf("slow countup WaitTime = %d, want 3000", got)
	}

	// The quit hint follows showQuitKeystroke.
	now = 0
	engine = NewNotificationEngine(func() uint64 { return now })
	engine.SetEscapeKeyString("Ctrl-^")
	engine.SetNotificationString([]rune("bye"), true, false)
	fb = NewFramebuffer(30, 2)
	engine.Apply(fb)
	if got := rowRunes(fb, 0); !strings.HasPrefix(got, "mosh: bye") ||
		strings.Contains(got, "To quit") {
		t.Fatalf("permanent message row = %q", got)
	}
	if got := engine.WaitTime(); got != (1<<31)-1 {
		t.Fatalf("permanent message WaitTime = %d, want %d", got, (1<<31)-1)
	}
}

func TestTitlePrefixOverlay(t *testing.T) {
	manager := NewOverlayManager(func() uint64 { return 0 })
	manager.SetTitlePrefix([]rune("[mosh] "))
	fb := NewFramebuffer(10, 2)
	fb.WindowTitle = []rune("remote")
	fb.TitleInitialized = true
	manager.Apply(fb)
	if got := string(fb.WindowTitle); got != "[mosh] remote" {
		t.Fatalf("prefixed title = %q", got)
	}
	// Every frame starts from the remote framebuffer, so applying the overlay
	// again to a fresh copy does not accumulate prefixes.
	next := NewFramebuffer(10, 2)
	next.WindowTitle = []rune("remote")
	next.TitleInitialized = true
	manager.Apply(next)
	if got := string(next.WindowTitle); got != "[mosh] remote" {
		t.Fatalf("second prefixed title = %q", got)
	}
}

func TestPredictionScaffoldIsInert(t *testing.T) {
	engine := NewPredictionEngine()
	if engine.DisplayPreference() != DisplayAdaptive {
		t.Fatalf("default display preference = %d", engine.DisplayPreference())
	}
	if engine.Active() {
		t.Fatal("inert prediction engine reports active")
	}
	if got := engine.WaitTime(); got != int(^uint(0)>>1) {
		t.Fatalf("inert WaitTime = %d, want max", got)
	}
	fb := NewFramebuffer(5, 2)
	fb.Cell(0, 0).Contents = "x"
	before := fb.Clone()
	engine.Cull(fb)
	engine.Apply(fb)
	if !fb.Equal(before) {
		t.Fatal("inert prediction engine modified the framebuffer")
	}
	manager := NewOverlayManager(func() uint64 { return 0 })
	if got := manager.WaitTime(); got != (1<<31)-1 {
		t.Fatalf("overlay manager WaitTime = %d, want notification's cap", got)
	}
}

func TestDisplayClose(t *testing.T) {
	if got := NewDisplay(DefaultDisplayOptions()).Open(); got != "\x1b[?1h" {
		t.Fatalf("default open = %q", got)
	}
	got := NewDisplay(DefaultDisplayOptions()).Close()
	want := "\x1b[?1l\x1b[0m\x1b[?25h" +
		"\x1b[?1003l\x1b[?1002l\x1b[?1001l\x1b[?1000l" +
		"\x1b[?1015l\x1b[?1006l\x1b[?1005l"
	if got != want {
		t.Fatalf("default close = %q, want %q", got, want)
	}
	if got := NewDisplay(XtermDisplayOptions()).Open(); got != "\x1b[?1049h\x1b[?1h" {
		t.Fatalf("xterm open = %q", got)
	}
	if got := NewDisplay(XtermDisplayOptions()).Close(); !strings.HasSuffix(got, "\x1b[?1049l") {
		t.Fatalf("xterm close = %q", got)
	}
}

func TestCompleteResize(t *testing.T) {
	terminal := NewComplete(4, 2)
	terminal.Act("abcd")
	terminal.Resize(2, 1)
	width, height := terminal.Framebuffer().DS.Size()
	if width != 2 || height != 1 {
		t.Fatalf("resized framebuffer = %dx%d", width, height)
	}
	terminal.Act("界")
	width, height = terminal.Framebuffer().DS.Size()
	if width != 2 || height != 1 {
		t.Fatalf("framebuffer after wide write = %dx%d", width, height)
	}
}
