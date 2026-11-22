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
