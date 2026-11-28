package vt

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/application/lens/model"
)

type recordingHooks struct {
	osc []string
}

func (h *recordingHooks) Begin(model.Cmd) {}
func (h *recordingHooks) Line(model.Line) {}
func (h *recordingHooks) End(int)         {}
func (h *recordingHooks) OSC(code string, _ []string) {
	h.osc = append(h.osc, code)
}

func TestTapUnderstandsNatcattyExecMarker(t *testing.T) {
	store := model.NewStore(nil)
	tap := New(store).WithCols(80)
	enc := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	tap.Feed([]byte("\x1b]1337;NetcattyExec;powershell;" +
		enc(`C:\work`) + ";" + enc("git status --short") + "\x07"))
	tap.Feed([]byte("\x1b]133;C\x07 M file.txt\r\n\x1b]133;D;0\x07"))

	blocks := store.Snapshot()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := blocks[0].Cmd.Typed; got != "git status --short" {
		t.Fatalf("typed = %q", got)
	}
	if got := blocks[0].Cmd.Cwd; got != `C:\work` {
		t.Fatalf("cwd = %q", got)
	}
	if got := blocks[0].Raw; got != " M file.txt\n" {
		t.Fatalf("raw = %q", got)
	}
}

// Nushell writes its own bare 133;C immediately after the hook's 133;C. The
// second mark used to reset the capture screen and end the command, so the
// block kept the command line and lost all of its output (raw was empty).
func TestTapKeepsNushellOutputAfterItsNativeCommandMark(t *testing.T) {
	enc := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	store := model.NewStore(nil)
	tap := New(store).WithCols(80)
	tap.Feed([]byte("\x1b]1337;NetcattyExec;nu;" + enc("/tmp") + ";" + enc("echo probe_hello") + "\x07"))
	tap.Feed([]byte("\x1b]133;C\x07")) // the hook's mark
	tap.Feed([]byte("\x1b]133;C\x07")) // nushell's own mark
	tap.Feed([]byte("probe_hello\r\n"))
	tap.Feed([]byte("\x1b]133;D;0\x07\x1b]133;D;0\x07"))

	blocks := store.Snapshot()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := blocks[0].Cmd.Typed; got != "echo probe_hello" {
		t.Fatalf("typed = %q", got)
	}
	if got := blocks[0].Raw; got != "probe_hello\n" {
		t.Fatalf("raw = %q, want the command output", got)
	}
	if !blocks[0].Exited || blocks[0].Status != 0 {
		t.Fatalf("block did not finish cleanly: %+v", blocks[0])
	}
}

func TestTapRejectsMalformedNatcattyExecMarker(t *testing.T) {
	enc := func(value string) string {
		return base64.RawURLEncoding.EncodeToString([]byte(value))
	}
	cases := []string{
		"\x1b]1337;NetcattyExec;zsh;/tmp\x07",
		"\x1b]1337;NetcattyExec;zsh;" + enc("/tmp") + ";" + enc("make") + ";extra\x07",
		"\x1b]1337;NetcattyExec;zsh;!!!!;" + enc("make") + "\x07",
		"\x1b]1337;NetcattyExec;zsh;" + enc("/tmp") + ";!!!!\x07",
		"\x1b]1337;NetcattyExec;zsh;" + enc("/tmp") + ";\x07",
		"\x1b]1337;NetcattyExec;bad shell;" + enc("/tmp") + ";" + enc("make") + "\x07",
	}
	for _, sequence := range cases {
		t.Run(sequence, func(t *testing.T) {
			store := model.NewStore(nil)
			tap := New(store).WithCols(80)
			tap.Feed([]byte(sequence))
			tap.Feed([]byte("\x1b]133;C;echo fallback\x07output\r\n\x1b]133;D;0\x07"))
			blocks := store.Snapshot()
			if len(blocks) != 1 {
				t.Fatalf("blocks = %d, want 1", len(blocks))
			}
			if got := blocks[0].Cmd.Typed; got != "echo fallback" {
				t.Fatalf("typed = %q, want OSC 133 command", got)
			}
			if got := blocks[0].Cmd.Cwd; got != "" {
				t.Fatalf("cwd = %q, want no malformed private cwd", got)
			}
		})
	}
}

func TestTapKeepsITerm1337FileOutsideNatcattyCapture(t *testing.T) {
	hooks := &recordingHooks{}
	store := model.NewStore(hooks)
	tap := New(store).WithCols(80)

	// OSC 1337 is shared by iTerm2 extensions. A File payload must not be
	// mistaken for NatcattyExec, otherwise it could populate the next command's
	// typed line/cwd and disappear from the stream that the graphics parser sees.
	tap.Feed([]byte("\x1b]1337;File=name=cGljLnBuZw==;inline=1:payload\x07"))
	tap.Feed([]byte("\x1b]133;C;echo image\x07image output\r\n\x1b]133;D;0\x07"))

	blocks := store.Snapshot()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := blocks[0].Cmd.Typed; got != "echo image" {
		t.Fatalf("typed = %q, want command from OSC 133;C", got)
	}
	if got := blocks[0].Cmd.Cwd; got != "" {
		t.Fatalf("cwd = %q, want no pending NatcattyExec cwd", got)
	}
	if len(hooks.osc) == 0 || hooks.osc[0] != "1337" {
		t.Fatalf("OSC notifications = %#v, want 1337 File notification", hooks.osc)
	}
}

func TestTapRetiresParentBlockWhenAHookedChildShellStarts(t *testing.T) {
	store := model.NewStore(nil)
	tap := New(store).WithCols(80)
	tap.Feed([]byte("\x1b]133;C;pwsh\x07"))
	tap.Feed([]byte("child banner\r\n"))
	tap.Feed([]byte("\x1b]1337;NetcattyHook;powershell\x07"))
	if blocks := store.Snapshot(); len(blocks) != 1 || !blocks[0].Exited {
		t.Fatalf("parent block = %+v, want one finished block", blocks)
	}
	tap.Feed([]byte("\x1b]133;C;Get-ChildItem\x07Name\r\n\x1b]133;D;0\x07"))
	blocks := store.Snapshot()
	if len(blocks) != 2 {
		t.Fatalf("blocks = %+v, want parent and child", blocks)
	}
	if blocks[1].Cmd.Typed != "Get-ChildItem" || !blocks[1].Exited {
		t.Fatalf("child block = %+v", blocks[1])
	}
	if blocks[1].Raw != "Name\n" {
		t.Fatalf("child raw = %q", blocks[1].Raw)
	}
}

// A full-screen program draws on the alternate screen, which the terminal
// restores when the program quits: none of it is part of the command's output.
// The tap used to write the program's rows into the capture screen, so after
// :q the command's block held a monochrome copy of the vim screen (title,
// tildes, status line) above the next prompt. The tap keeps following the
// marks and the main screen, and ignores the program's painting in between.
func TestTapOmitsAlternateScreenPainting(t *testing.T) {
	store := model.NewStore(nil)
	tap := New(store).WithCols(20)
	tap.Feed([]byte("\x1b]133;A\x07\x1b]133;B\x07PS> "))
	tap.Feed([]byte("\x1b]133;C;vim\x07"))
	tap.Feed([]byte("before\r\n"))
	tap.Feed([]byte("\x1b[?1049h\x1b[2J\x1b[H"))
	tap.Feed([]byte("VIM TITLE\r\n~\r\n~\r\n:q\r\n"))
	tap.Feed([]byte("\x1b[?1049l"))
	tap.Feed([]byte("\x1b]133;D;0\x07\x1b]133;A\x07\x1b]133;B\x07PS> "))
	blocks := store.Snapshot()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if got := blocks[0].Raw; got != "before\n" {
		t.Fatalf("raw = %q, want only the main screen's output", got)
	}
	if strings.Contains(blocks[0].Raw, "VIM TITLE") {
		t.Fatalf("the alternate screen became command output: %q", blocks[0].Raw)
	}
}

type binaryCaptureHooks struct {
	store    *model.Store
	captured []byte
	ended    bool
	status   int
}

func (h *binaryCaptureHooks) Begin(cmd model.Cmd) {
	if strings.Contains(cmd.Line, ".png") {
		h.store.MarkBinary(h.store.LastID())
	}
}
func (h *binaryCaptureHooks) Line(model.Line)      {}
func (h *binaryCaptureHooks) OSC(string, []string) {}
func (h *binaryCaptureHooks) Bytes(b []byte) {
	h.captured = append(h.captured, b...)
}
func (h *binaryCaptureHooks) End(status int) {
	h.ended = true
	h.status = status
}

// FeedFilter must strip binary-captured payloads inline (so zero payload bytes
// reach downstream terminal consumers) while keeping OSC 133;C, a cross-chunk
// OSC 133;D terminator, and subsequent multi-byte UTF-8 prompts completely intact.
func TestTapFeedFilterStripsBinaryPayloadAndHandlesSplitOSC133D(t *testing.T) {
	hooks := &binaryCaptureHooks{}
	store := model.NewStore(hooks)
	hooks.store = store
	tap := New(store).WithCols(80)

	// Payload deliberately contains ESC Z (DECID), ESC [ c (DA1), BEL, NUL,
	// high bytes, and an incomplete ESC]133;D; without a valid terminator.
	payload := []byte("\x89PNG\r\n\x1a\n\x1bZ\x1b[c\x07\x00\xff\x1b]133;D;\xfe\x1b[6nPAYLOAD_END")

	chunk1 := append([]byte("\x1b]133;A\x07prompt$ \x1b]133;B\x07cat photo.png\r\n\x1b]133;C;cmdline=cat\\ photo.png\x07"), payload[:18]...)
	// Split OSC 133;D;0\x07 right across chunk2 and chunk3.
	chunk2 := append(append([]byte(nil), payload[18:]...), []byte("\x1b]133;D;")...)
	chunk3 := []byte("0\x07\x1b]133;A\x07❯ 完成 \x1b]133;B\x07")

	var out bytes.Buffer
	out.Write(tap.FeedFilter(chunk1))
	out.Write(tap.FeedFilter(chunk2))
	out.Write(tap.FeedFilter(chunk3))

	if !hooks.ended || hooks.status != 0 {
		t.Fatalf("binary command did not finish cleanly: ended=%v status=%d", hooks.ended, hooks.status)
	}
	if !bytes.Equal(hooks.captured, payload) {
		t.Fatalf("captured payload mismatch:\n got: %x\nwant: %x", hooks.captured, payload)
	}
	wantOut := "\x1b]133;A\x07prompt$ \x1b]133;B\x07cat photo.png\r\n\x1b]133;C;cmdline=cat\\ photo.png\x07\x1b]133;D;0\x07\x1b]133;A\x07❯ 完成 \x1b]133;B\x07"
	if got := out.String(); got != wantOut {
		t.Fatalf("filtered terminal output mismatch:\n got: %q\nwant: %q", got, wantOut)
	}

	// Follow-up text command passes through untouched.
	follow := []byte("\x1b]133;C;cmdline=echo\\ ok\x07ok\r\n\x1b]133;D;0\x07")
	if got := string(tap.FeedFilter(follow)); got != string(follow) {
		t.Fatalf("follow-up output = %q, want %q", got, string(follow))
	}
	blocks := store.Snapshot()
	if len(blocks) != 2 || !blocks[0].Binary || !blocks[0].Exited || blocks[1].Raw != "ok\n" {
		t.Fatalf("unexpected blocks: %+v", blocks)
	}
}
