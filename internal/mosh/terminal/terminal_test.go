// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

import (
	"strings"
	"testing"
	"time"
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
		// Expected values come from glibc 2.39 wcwidth(3) in the Ubuntu
		// 24.04 C.UTF-8 locale, the stock parity oracle. unicode.IsPrint and
		// x/text/width disagree with it for several of these code points.
		{"ogham-space", 0x1680, 1},
		{"en-quad", 0x2000, 1},
		{"hair-space", 0x200a, 1},
		{"narrow-nbsp", 0x202f, 1},
		{"medium-math-space", 0x205f, 1},
		{"ideo-space-width", 0x3000, 2},
		{"soft-hyphen", 0x00ad, 1},
		{"private-use-low", 0xe000, 1},
		{"private-use-high", 0xf8ff, 1},
		{"supplementary-pua-a", 0xf0000, 1},
		{"supplementary-pua-b", 0x10fffd, 1},
		{"hangul-leading-jamo", 0x1100, 2},
		{"hangul-leading-jamo-last", 0x115f, 2},
		{"hangul-medial-jamo", 0x1160, 0},
		{"hangul-medial-jamo-mid", 0x1161, 0},
		{"hangul-final-jamo", 0x11a8, 0},
		{"hangul-jamo-end", 0x11ff, 0},
		{"enclosing-mark", 0x20dd, 0},
		{"variation-selector-16", 0xfe0f, 0},
		{"variation-selector-supplementary", 0xe0100, 0},
		{"zero-width-space", 0x200b, 0},
		{"word-joiner", 0x2060, 0},
		{"interlinear-annotation", 0xfff9, 0},
		{"emoji-wide", 0x1f600, 2},
		{"regional-indicator", 0x1f1e6, 1},
		{"thai-sara-am", 0x0e33, 1},
		{"unassigned", 0x0378, -1},
		{"unassigned-cyrillic", 0x05ff, -1},
		{"noncharacter", 0x10ffff, -1},
		{"out-of-range", 0x110000, -1},
	}
	for _, testCase := range cases {
		if got := RuneWidth(testCase.r); got != testCase.want {
			t.Errorf("%s: RuneWidth(%U) = %d, want %d", testCase.name, testCase.r, got, testCase.want)
		}
	}
}

// TestWcwidthTableIsWellFormed guards the generated table: sorted,
// non-overlapping, in range, and with only the three widths stock handles.
func TestWcwidthTableIsWellFormed(t *testing.T) {
	previous := rune(-1)
	for i, interval := range wcwidthRanges {
		if interval.lo > interval.hi {
			t.Fatalf("interval %d is reversed: %+v", i, interval)
		}
		if interval.lo <= previous {
			t.Fatalf("interval %d overlaps or is unsorted: %+v after U+%04X", i, interval, previous)
		}
		if interval.hi > 0x10FFFF {
			t.Fatalf("interval %d exceeds Unicode: %+v", i, interval)
		}
		switch interval.width {
		case -1, 0, 2:
		default:
			t.Fatalf("interval %d has unexpected width %d", i, interval.width)
		}
		previous = interval.hi
	}
	// RuneWidth must agree with the oracle for every code point the table
	// covers and return width 1 in the gaps.
	for _, interval := range wcwidthRanges {
		for _, r := range []rune{interval.lo, interval.hi} {
			if r == 0 {
				continue // stock's NUL special case
			}
			if got := RuneWidth(r); got != int(interval.width) {
				t.Fatalf("RuneWidth(U+%04X) = %d, want table width %d", r, got, interval.width)
			}
		}
	}
}

// TestMonotonicMillisAt pins the transport timestamp conversion: stock stores
// transport send timestamps on the same monotonic clock its overlay uses,
// while Go's transport stores time.Time values. Zero and pre-start times have
// no in-process meaning and map to zero.
func TestMonotonicMillisAt(t *testing.T) {
	if got := MonotonicMillisAt(time.Time{}); got != 0 {
		t.Fatalf("zero time = %d, want 0", got)
	}
	if got := MonotonicMillisAt(processStart.Add(-time.Second)); got != 0 {
		t.Fatalf("pre-start time = %d, want 0", got)
	}
	at := processStart.Add(1234*time.Millisecond + 900*time.Microsecond)
	if got := MonotonicMillisAt(at); got != 1234 {
		t.Fatalf("converted timestamp = %d, want 1234", got)
	}
}

// TestSGRTrueColorOutOfRangeMatchesStock pins stock's unmasked packing. A
// 25-bit bitfield stores the OR of the unmasked components, so an SGR 300 blue
// component bleeds into green and serializes as 3;44, exactly as the Ubuntu
// 24.04 stock oracle emits. Masking each component would print 2;44 instead.
func TestSGRTrueColorOutOfRangeMatchesStock(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Renditions)
		want string
	}{
		{
			name: "foreground-blue-overflow",
			set: func(r *Renditions) {
				r.SetForegroundColor(MakeTrueColor(1, 2, 300))
			},
			want: "\033[0;38;2;1;3;44m",
		},
		{
			name: "background-blue-overflow",
			set: func(r *Renditions) {
				r.SetBackgroundColor(MakeTrueColor(255, 2, 511))
			},
			want: "\033[0;48;2;255;3;255m",
		},
		{
			name: "foreground-green-overflow",
			set: func(r *Renditions) {
				r.SetForegroundColor(MakeTrueColor(0, 0x1ff, 0))
			},
			want: "\033[0;38;2;1;255;0m",
		},
	}
	for _, testCase := range cases {
		var r Renditions
		testCase.set(&r)
		if got := r.SGR(); got != testCase.want {
			t.Errorf("%s: SGR = %q, want %q", testCase.name, got, testCase.want)
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

func TestOverlayManagerUsesAPredictionEngine(t *testing.T) {
	manager := NewOverlayManager(func() uint64 { return 0 })
	if got := manager.WaitTime(); got != (1<<31)-1 {
		t.Fatalf("overlay manager WaitTime = %d, want notification's cap", got)
	}
	engine := manager.Predictions()
	if engine == nil {
		t.Fatal("overlay manager has no prediction engine")
	}
	fb := NewFramebuffer(5, 2)
	fb.DS.SetCursor(0, 0)
	engine.SetDisplayPreference(DisplayAlways)
	engine.Cull(fb)
	engine.confirmedEpoch = engine.predictionEpoch
	engine.SetLocalFrameSent(0)
	engine.NewUserByte('x', fb)
	display := fb.Clone()
	manager.Apply(display)
	if display.Cell(0, 0).Contents != "x" {
		t.Fatal("overlay manager's prediction engine is not wired for display")
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
