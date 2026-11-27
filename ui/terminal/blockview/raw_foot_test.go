package blockview

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

// A raw view is the program's output as it came out, with nothing added after
// it: the exit status and how long the command took are block chrome, and a
// command no lens claimed (plain `ls`) has no chrome at all. Only the native
// view keeps the foot.
func TestRawViewEndsWithTheOutputOnly(t *testing.T) {
	store := model.NewStore(nil)
	store.Begin(model.Cmd{Line: "ls -l", Typed: "ls -l"})
	store.Append([]byte("\ntotal 0\r\n-rw-r--r--  1 me  staff  0 Jan  1 00:00 a.txt\r\n"))
	store.End(0)
	store.SetNative(store.LastID(), "natcatty-test", nil,
		lens.Text{Spans: []lens.Span{{Text: "native"}}}, nil)
	blocks := store.Snapshot()
	b := &blocks[0]
	b.Mode = model.ViewRaw
	b.Started = time.Now().Add(-time.Second)
	b.Ended = time.Now()

	var scroll ui.ScrollState
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().TrackScroll(&scroll).Children(func() {
			ui.Column(c).Padding(10).Children(func() {
				RenderCommand(c, store, nil, b, ui.Rect{}, func(string) {}, nil)
			})
		})
	}, 600, 300)
	tester.Frame()
	tester.Frame()

	texts := tester.Texts()
	if !hasPrefix(texts, "total 0") {
		t.Fatalf("the raw output is not drawn: %v", texts)
	}
	for _, badge := range []string{"exit ", "took "} {
		if hasPrefix(texts, badge) {
			t.Fatalf("the raw view carries the block's %q foot: %v", badge, texts)
		}
	}

	// The native view is the block's, and keeps its foot.
	b.Mode = model.ViewNative
	tester.Frame()
	tester.Frame()
	texts = tester.Texts()
	if !hasPrefix(texts, "exit 0") {
		t.Fatalf("the native view lost its exit badge: %v", texts)
	}
	if !hasPrefix(texts, "took 1s") {
		t.Fatalf("the native view lost its elapsed time: %v", texts)
	}
}

func hasPrefix(texts []string, prefix string) bool {
	for _, s := range texts {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// Switching a binary picture block to Raw must render the sanitized raw bytes
// (mojibake like a normal terminal) and keep the card tall enough for the hover
// toolbar so the user can hover and click Native to switch back.
func TestBinaryRawViewShowsMojibakeAndSwitchesBackToNative(t *testing.T) {
	store := model.NewStore(nil)
	id := store.Begin(model.Cmd{Line: "cat pic.png", Typed: "cat pic.png"})
	store.MarkBinary(id)
	store.AppendBytes([]byte("\x1b]2;cat pic.png\x07\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\xff\xfe\xfdPAYLOAD"))
	store.End(0)
	store.SetNativePicture(id, "view.file", nil,
		lens.Text{Spans: []lens.Span{{Text: "picture-native"}}}, nil)

	// Confirm the switch to Raw (pictures require confirmation).
	if store.RequestRaw(id) {
		t.Fatal("picture RequestRaw switched without confirmation")
	}
	if !store.ConfirmRaw() {
		t.Fatal("ConfirmRaw returned false")
	}

	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Padding(10).FillWidth().Children(func() {
				Render(c, store, nil, ui.Rect{W: 600, H: 300}, func(string) {}, nil)
			})
		})
	}, 600, 300)
	tester.Frame()
	tester.Frame()

	joined := strings.Join(tester.Texts(), "\n")
	if !strings.Contains(joined, "\uFFFD"+"PNG") || !strings.Contains(joined, "IHDR") {
		t.Fatalf("binary raw view missing mojibake output: %v", tester.Texts())
	}
	if strings.ContainsRune(joined, 0) {
		t.Fatalf("binary raw view contains NUL bytes: %q", joined)
	}

	// Hover the block card so the [Native | Raw] bar appears, then click Native.
	tester.Move(300, 20)
	tester.Frame()
	tester.Frame()
	nativeRect, ok := tester.Find("Native")
	if !ok {
		t.Fatalf("Native toggle not visible on hover: %v", tester.Texts())
	}
	tester.Move(nativeRect.X+nativeRect.W/2, nativeRect.Y+nativeRect.H/2)
	tester.Frame()
	tester.ClickAt(nativeRect.X+nativeRect.W/2, nativeRect.Y+nativeRect.H/2)
	tester.Frame()
	tester.Frame()

	blocks := store.Snapshot()
	if got := store.EffectiveView(&blocks[0]); got != model.ViewNative {
		t.Fatalf("view after clicking Native = %v, want ViewNative", got)
	}
	if !tester.HasText("picture-native") {
		t.Fatalf("native view did not return after clicking Native: %v", tester.Texts())
	}

	// Verify large binary output (>256 KiB) is preserved in full without truncation.
	largeID := store.Begin(model.Cmd{Line: "cat big.png", Typed: "cat big.png"})
	store.MarkBinary(largeID)
	bigPayload := make([]byte, 256<<10)
	for i := range bigPayload {
		bigPayload[i] = byte('a' + (i % 26))
	}
	bigPayload = append(bigPayload, []byte("END_MARKER_256K")...)
	store.AppendBytes(bigPayload)
	store.End(0)
	if got := store.Raw(largeID); !strings.Contains(got, "END_MARKER_256K") {
		t.Fatalf("large binary raw output was truncated (len=%d)", len(got))
	}
}

// Even a block whose Raw output is completely empty must stay tall enough for
// the hover bar so switching to Raw never traps the user out of Native.
func TestEmptyRawViewKeepsHoverBarClickableToReturnToNative(t *testing.T) {
	store := model.NewStore(nil)
	id := store.Begin(model.Cmd{Line: "empty", Typed: "empty"})
	store.End(0)
	store.SetNative(id, "test.empty", nil,
		lens.Text{Spans: []lens.Span{{Text: "empty-native"}}}, nil)
	store.SetMode(id, model.ViewRaw)

	tester := ui.NewTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Padding(10).FillWidth().Children(func() {
				Render(c, store, nil, ui.Rect{W: 600, H: 300}, func(string) {}, nil)
			})
		})
	}, 600, 300)
	tester.Frame()
	tester.Frame()

	tester.Move(300, 20)
	tester.Frame()
	tester.Frame()
	nativeRect, ok := tester.Find("Native")
	if !ok {
		t.Fatalf("Native toggle missing on empty raw block: %v", tester.Texts())
	}
	tester.Move(nativeRect.X+nativeRect.W/2, nativeRect.Y+nativeRect.H/2)
	tester.Frame()
	tester.ClickAt(nativeRect.X+nativeRect.W/2, nativeRect.Y+nativeRect.H/2)
	tester.Frame()
	tester.Frame()

	blocks := store.Snapshot()
	if got := store.EffectiveView(&blocks[0]); got != model.ViewNative {
		t.Fatalf("view after clicking Native on empty raw block = %v, want ViewNative", got)
	}
}
