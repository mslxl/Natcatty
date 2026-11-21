//go:build !ios && !android

package vt

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/ui/terminal/native"
)

// TestMain loads the libghostty-vt this package binds, the same way the
// terminal plugin does: from the manifest beside the package, or the cache it
// downloads into.
func TestMain(m *testing.M) {
	path, err := native.LibraryPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := Load(path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func newTestTerminal(t *testing.T) *Terminal {
	t.Helper()
	term, err := NewTerminal(80, 24, Effects{})
	if err != nil {
		t.Fatalf("NewTerminal: %v", err)
	}
	t.Cleanup(term.Free)
	return term
}

// highlightMouseOn writes the DEC 1001 sequence the fork records itself.
func highlightMouseOn(t *testing.T, term *Terminal) {
	t.Helper()
	term.Write([]byte("\x1b[?1001h"))
}

// TestHighlightMouseModeReportsTheRecordedBit pins Mode(1001): the C library
// drops the bit, so the answer comes from the fork's own record.
func TestHighlightMouseModeReportsTheRecordedBit(t *testing.T) {
	term := newTestTerminal(t)
	if term.Mode(ModeHighlightMouse) {
		t.Fatal("1001 reads as set before any output")
	}
	highlightMouseOn(t, term)
	if !term.Mode(ModeHighlightMouse) {
		t.Fatal("1001 was not recorded from the output")
	}
	term.Write([]byte("\x1b[?1001l"))
	if term.Mode(ModeHighlightMouse) {
		t.Fatal("1001 was not cleared by its reset")
	}
}

// TestMouseTrackingFollowsHighlightMouse pins MouseTracking's 1001
// short-circuit. The C library's own tracking bit is cleared directly, so the
// only source of the answer is the fork's record.
func TestMouseTrackingFollowsHighlightMouse(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	// Remove the synthetic 1000 the fork set, leaving only the recorded 1001.
	term.SetMode(Mode(1000), false)
	if term.Mode(Mode(1000)) {
		t.Fatal("the C tracking bit was not cleared for the probe")
	}
	if !term.MouseTracking() {
		t.Fatal("MouseTracking did not follow the recorded 1001")
	}
	term.Write([]byte("\x1b[?1001l"))
	if term.MouseTracking() {
		t.Fatal("MouseTracking stayed on after 1001 was reset")
	}
}

// TestVTReplaysHighlightMouse pins the snapshot: a replay must restore 1001,
// which the C library cannot write.
func TestVTReplaysHighlightMouse(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	if !bytes.Contains(term.VT(), []byte("\x1b[?1001h")) {
		t.Fatalf("VT() does not replay 1001 on: %q", term.VT())
	}
	term.Write([]byte("\x1b[?1001l"))
	if !bytes.Contains(term.VT(), []byte("\x1b[?1001l")) {
		t.Fatalf("VT() does not replay 1001 off: %q", term.VT())
	}
}

// TestResetClearsHighlightMouseShadow pins Reset's shadow clear: a full reset
// must leave no recorded mouse mode behind.
func TestResetClearsHighlightMouseShadow(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	term.Write([]byte("\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?9h"))
	term.Reset()
	if term.Mode(ModeHighlightMouse) {
		t.Fatal("Reset left the 1001 record set")
	}
	if term.MouseTracking() {
		t.Fatal("Reset left mouse tracking on")
	}
	if bytes.Contains(term.VT(), []byte("\x1b[?1001h")) {
		t.Fatalf("VT() replays 1001 after Reset: %q", term.VT())
	}
}

// TestProgram1000KeepsOwnershipWhen1001Ends pins the ownership transfer: once
// the program enables 1000 itself, resetting 1001 must not turn it off.
func TestProgram1000KeepsOwnershipWhen1001Ends(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)         // the fork sets a synthetic 1000
	term.Write([]byte("\x1b[?1000h")) // the program takes 1000 itself
	term.Write([]byte("\x1b[?1001l")) // 1001 ends
	if !term.Mode(Mode(1000)) {
		t.Fatal("ending 1001 turned off the program's 1000")
	}
	if !term.MouseTracking() {
		t.Fatal("the program's 1000 no longer takes the mouse")
	}
	if term.Mode(ModeHighlightMouse) {
		t.Fatal("1001 is still recorded after its reset")
	}
}

// TestProgram1000OffWhile1001OnRestoresSynthetic pins the reverse transfer:
// when the program turns 1000 off while 1001 is still on, the fork must put
// the synthetic mode back so the encoder keeps reporting.
func TestProgram1000OffWhile1001OnRestoresSynthetic(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	term.Write([]byte("\x1b[?1000h"))
	term.Write([]byte("\x1b[?1000l"))
	if !term.Mode(Mode(1000)) {
		t.Fatal("the synthetic 1000 was not restored while 1001 stayed on")
	}
	encoder, err := NewMouseEncoder()
	if err != nil {
		t.Fatalf("NewMouseEncoder: %v", err)
	}
	defer encoder.Free()
	encoder.Sync(term, 800, 600, 8, 16, 0, 0, false)
	if got := encoder.Encode(MousePress, ButtonLeft, 0, 20, 20); len(got) == 0 {
		t.Fatal("the encoder produced no report for a program that takes the mouse")
	}
}

// TestRISClearsTheHighlightMouseShadow pins the output RIS reset. The library
// resets every mode it keeps; the fork's shadow and synthetic bit must follow,
// and the snapshot must not replay a mode the terminal no longer has.
func TestRISClearsTheHighlightMouseShadow(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	if !term.Mode(Mode(1000)) {
		t.Fatal("the synthetic 1000 was not set")
	}
	term.Write([]byte("\x1bc"))
	if term.Mode(ModeHighlightMouse) {
		t.Fatal("RIS left the 1001 record set")
	}
	if term.MouseTracking() {
		t.Fatal("RIS left mouse tracking on")
	}
	if term.Mode(Mode(1000)) {
		t.Fatal("RIS did not leave the C tracking bit off")
	}
	snapshot := term.VT()
	if bytes.Contains(snapshot, []byte("\x1b[?1000h")) || bytes.Contains(snapshot, []byte("\x1b[?1001h")) {
		t.Fatalf("VT() replays a mouse mode after RIS: %q", snapshot)
	}
	if !bytes.Contains(snapshot, []byte("\x1b[?1001l")) {
		t.Fatalf("VT() does not mark 1001 off after RIS: %q", snapshot)
	}
}

// TestRISSplitAcrossReadsClearsTheShadow pins the carry: RIS may arrive as ESC
// in one read and c in the next, as any sequence may.
func TestRISSplitAcrossReadsClearsTheShadow(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	term.Write([]byte("\x1b"))
	term.Write([]byte("c"))
	if term.Mode(ModeHighlightMouse) || term.MouseTracking() || term.Mode(Mode(1000)) {
		t.Fatalf("split RIS left modes: 1001=%v mouse=%v 1000=%v",
			term.Mode(ModeHighlightMouse), term.MouseTracking(), term.Mode(Mode(1000)))
	}
}

// TestRISThenHighlightTrackingTakesTheMouseAgain proves a reset is not a
// one-way door: a program that asks for 1001 after RIS takes the mouse again.
func TestRISThenHighlightTrackingTakesTheMouseAgain(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	term.Write([]byte("\x1bc"))
	highlightMouseOn(t, term)
	if !term.Mode(ModeHighlightMouse) || !term.MouseTracking() {
		t.Fatalf("1001 after RIS: recorded=%v mouse=%v",
			term.Mode(ModeHighlightMouse), term.MouseTracking())
	}
	encoder, err := NewMouseEncoder()
	if err != nil {
		t.Fatalf("NewMouseEncoder: %v", err)
	}
	defer encoder.Free()
	encoder.Sync(term, 800, 600, 8, 16, 0, 0, false)
	if got := encoder.Encode(MousePress, ButtonLeft, 0, 20, 20); len(got) == 0 {
		t.Fatal("the encoder produced no report after RIS and a fresh 1001")
	}
}

// TestSplitSGRSurvivesHighlightTracking pins the parser-state bug: the fork
// used to inject its synthetic 1000 with raw VT bytes after every write, which
// aborted a CSI the C parser was holding. The split SGR must render as an SGR,
// not as a literal m.
func TestSplitSGRSurvivesHighlightTracking(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	term.Write([]byte("hello \x1b[31"))
	term.Write([]byte("m world"))
	text := term.Text()
	if strings.Contains(text, "hello m world") {
		t.Fatalf("the split SGR was aborted and its final byte rendered: %q", text)
	}
	if !strings.Contains(text, "hello  world") {
		t.Fatalf("the split SGR did not render its text: %q", text)
	}
}

// TestSplitMouseModeSurvivesHighlightTracking pins the dead-click bug: a
// 1002 sequence split across reads used to be aborted by the injected 1000,
// leaving the fork's shadow and the C parser disagreeing. The encoder reads
// the C parser, so it must still emit a report.
func TestSplitMouseModeSurvivesHighlightTracking(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	term.Write([]byte("\x1b[?1002"))
	term.Write([]byte("h"))
	if !term.Mode(Mode(1002)) {
		t.Fatal("the C parser did not receive the split 1002")
	}
	if !term.MouseTracking() {
		t.Fatal("MouseTracking is off for a split 1002")
	}
	encoder, err := NewMouseEncoder()
	if err != nil {
		t.Fatalf("NewMouseEncoder: %v", err)
	}
	defer encoder.Free()
	encoder.Sync(term, 800, 600, 8, 16, 0, 0, false)
	if got := encoder.Encode(MousePress, ButtonLeft, 0, 20, 20); len(got) == 0 {
		t.Fatal("the encoder produced no report for a split 1002 (dead clicks)")
	}
}

// TestSplitMouseModeWithoutHighlightTracking pins the same carry without 1001:
// the fork must not disturb a split mode even when it has no synthetic mode to
// inject.
func TestSplitMouseModeWithoutHighlightTracking(t *testing.T) {
	term := newTestTerminal(t)
	term.Write([]byte("\x1b[?1002"))
	term.Write([]byte("h\x1b[?1006h"))
	if !term.Mode(Mode(1002)) || !term.Mode(Mode(1006)) {
		t.Fatalf("split modes: 1002=%v 1006=%v", term.Mode(Mode(1002)), term.Mode(Mode(1006)))
	}
}

// TestDeferredSyntheticResetIsWrittenLater pins the deferral's bookkeeping: a
// reset that cannot be written while the parser holds a partial sequence must
// stay pending and be written when the parser is ground again. Clearing the
// pending bit before the write would leave the synthetic mode on for good.
func TestDeferredSyntheticResetIsWrittenLater(t *testing.T) {
	term := newTestTerminal(t)
	highlightMouseOn(t, term)
	if !term.Mode(Mode(1000)) {
		t.Fatal("the synthetic 1000 was not set")
	}
	// 1001 ends, and the write ends inside a CSI, so the reset is deferred.
	term.Write([]byte("\x1b[?1001l\x1b["))
	if !term.Mode(Mode(1000)) {
		t.Fatal("the synthetic reset was written into a partial sequence")
	}
	term.Write([]byte("31m"))
	if term.Mode(Mode(1000)) {
		t.Fatal("the deferred synthetic reset was never written")
	}
}

// mousePressReport is what the encoder writes for a left press, or nil when
// the program asked for no such event.
func mousePressReport(t *testing.T, term *Terminal) []byte {
	t.Helper()
	encoder, err := NewMouseEncoder()
	if err != nil {
		t.Fatalf("NewMouseEncoder: %v", err)
	}
	defer encoder.Free()
	encoder.Sync(term, 800, 600, 8, 16, 0, 0, false)
	return encoder.Encode(MousePress, ButtonLeft, 0, 20, 20)
}

// TestMouseTrackingAgreesWithTheEncoder pins the invariant a click relies on:
// whenever MouseTracking says a program takes the mouse, the encoder writes a
// report, and when it does not, the encoder is silent. The library's encoder
// keeps the mode of the last mouse-mode sequence, a reset included, while its
// aggregate bit stays set; the fork restores the mode a program still asked
// for, so a click after 1002 h then 1000 l is never dead.
func TestMouseTrackingAgreesWithTheEncoder(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   bool
	}{
		{"none", "", false},
		{"1000", "\x1b[?1000h", true},
		{"1002", "\x1b[?1002h", true},
		{"1002 then 1000 reset", "\x1b[?1002h\x1b[?1000l", true},
		{"1003 then 1000 reset", "\x1b[?1003h\x1b[?1000l", true},
		{"9 then 1000 reset", "\x1b[?9h\x1b[?1000l", true},
		{"1002 then 1002 reset", "\x1b[?1002h\x1b[?1002l", false},
		{"1000 then 1000 reset", "\x1b[?1000h\x1b[?1000l", false},
		{"1002 then 1000 set", "\x1b[?1002h\x1b[?1000h", true},
		{"1002 1000 then 1000 reset", "\x1b[?1002h\x1b[?1000h\x1b[?1000l", true},
		{"1001 only", "\x1b[?1001h", true},
		{"1001 then 1001 reset", "\x1b[?1001h\x1b[?1001l", false},
		{"1001 then 1002 then 1002 reset", "\x1b[?1001h\x1b[?1002h\x1b[?1002l", true},
		{"1001 then 1002 then 1000 reset", "\x1b[?1001h\x1b[?1002h\x1b[?1000l", true},
		{"1002 then 1000 reset then 1002 reset", "\x1b[?1002h\x1b[?1000l\x1b[?1002l", false},
		{"1001 then 1002 then 1001 reset", "\x1b[?1001h\x1b[?1002h\x1b[?1001l", true},
		{"1001 then 1000 then 1000 reset", "\x1b[?1001h\x1b[?1000h\x1b[?1000l", true},
		{"RIS", "\x1b[?1002h\x1bc", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			term := newTestTerminal(t)
			term.Write([]byte(testCase.output))
			got := term.MouseTracking()
			report := mousePressReport(t, term)
			if got != testCase.want {
				t.Fatalf("MouseTracking = %v, want %v", got, testCase.want)
			}
			if (len(report) > 0) != got {
				t.Fatalf("MouseTracking = %v but the encoder wrote %q", got, report)
			}
		})
	}
}

// TestRestoredMouseModeWaitsForTheParser pins the deferral: a 1000 reset that
// turns the encoder off while 1002 is still set cannot be answered until the
// program's split sequence ends; the fork restores 1002 on the next read.
func TestRestoredMouseModeWaitsForTheParser(t *testing.T) {
	term := newTestTerminal(t)
	term.Write([]byte("\x1b[?1002h"))
	term.Write([]byte("\x1b[?1000"))
	if got := mousePressReport(t, term); len(got) == 0 {
		t.Fatal("the encoder lost 1002 while the parser held a partial sequence")
	}
	term.Write([]byte("l"))
	if !term.MouseTracking() {
		t.Fatal("MouseTracking is off after 1000 l while 1002 is set")
	}
	if got := mousePressReport(t, term); len(got) == 0 {
		t.Fatal("the encoder stayed off after a split 1000 reset; clicks are dead")
	}
}

// TestSyntheticHighlightModeLeavesNoTrackingBitBehind pins the cleanup: a
// program that asks for 1001, then 1002, then resets 1002 must not leave the
// fork's synthetic 1000 behind to make MouseTracking true over a dead encoder.
func TestSyntheticHighlightModeLeavesNoTrackingBitBehind(t *testing.T) {
	term := newTestTerminal(t)
	term.Write([]byte("\x1b[?1001h\x1b[?1002h\x1b[?1002l"))
	// 1001 is still on, so the fork emulates it again.
	if !term.MouseTracking() {
		t.Fatal("1001 stopped taking the mouse after a program mode ended")
	}
	if got := mousePressReport(t, term); len(got) == 0 {
		t.Fatal("the emulated 1001 has no encoder report")
	}
	term.Write([]byte("\x1b[?1001l"))
	if term.MouseTracking() {
		t.Fatal("MouseTracking stayed on after every mode ended")
	}
	if got := mousePressReport(t, term); len(got) > 0 {
		t.Fatalf("the encoder still reports after every mode ended: %q", got)
	}
}
