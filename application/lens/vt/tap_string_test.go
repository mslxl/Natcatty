package vt

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/application/lens/model"
)

// TestTapSwallowsPictureProtocolPayloads is the regression for a command that
// prints an inline picture: the APC (Kitty) or DCS (Sixel) payload used to
// leak into the block's text after the introducer was dropped as a two-byte
// escape, so a block showed base64 ("Gf=100,a=T;iVBORw…") instead of only the
// command's real output. The graphics parser reads those bytes on its own;
// the tap must keep them out of the line capture.
func TestTapSwallowsPictureProtocolPayloads(t *testing.T) {
	cases := []struct {
		name  string
		seq   string
		split bool
	}{
		{"kitty-apc", "\x1b_Gf=100,a=T;iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB\x1b\\", false},
		{"sixel-dcs", "\x1bPq#0;2;0;0;0#0~~@@vv@@~~@@~~$\x1b\\", false},
		{"kitty-chunked", "\x1b_Gi=7,m=1;iVBORw0KGgo=\x1b\\", false},
		// A picture is printed over many reads, not one: the reader hands the
		// program's output to the tap 1024 bytes at a time, so the introducer
		// and the payload are never in the same call.
		{"kitty-apc-split", "\x1b_Gf=100,a=T;iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB\x1b\\", true},
		{"kitty-chunked-split", "\x1b_Gi=7,m=1;iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB\x1b\\", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := model.NewStore(nil)
			tap := New(store).WithCols(80)
			tap.Feed([]byte("\x1b]1337;NetcattyExec;zsh;" + base64.RawURLEncoding.EncodeToString([]byte("/tmp")) + ";" + base64.RawURLEncoding.EncodeToString([]byte("show")) + "\x07"))
			tap.Feed([]byte("\x1b]133;C\x07before\r\n"))
			if tc.split {
				for i := 0; i < len(tc.seq); i += 5 {
					end := i + 5
					if end > len(tc.seq) {
						end = len(tc.seq)
					}
					tap.Feed([]byte(tc.seq[i:end]))
				}
			} else {
				tap.Feed([]byte(tc.seq))
			}
			tap.Feed([]byte("after\r\n\x1b]133;D;0\x07"))
			blocks := store.Snapshot()
			if len(blocks) != 1 {
				t.Fatalf("blocks = %d", len(blocks))
			}
			raw := blocks[0].Raw
			if !strings.Contains(raw, "before") || !strings.Contains(raw, "after") {
				t.Fatalf("the command's own output was lost: %q", raw)
			}
			for _, leak := range []string{"Gf=", "iVBOR", "#0;2;", "~~@@", "m=1"} {
				if strings.Contains(raw, leak) {
					t.Fatalf("the picture payload leaked into the block text (%q): %q", leak, raw)
				}
			}
		})
	}
}
