package vt

import (
	"encoding/base64"
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
