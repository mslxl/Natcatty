package sessionui

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
)

// actionTransport keeps a real Session alive for a headless Surface. The
// terminal plugin renders without a display, so these tests exercise the
// production menu, modal, and banners rather than a replica.
type actionTransport struct {
	closed chan struct{}
	once   sync.Once
}

func newActionTransport() *actionTransport { return &actionTransport{closed: make(chan struct{})} }

func (t *actionTransport) Read([]byte) (int, error) { <-t.closed; return 0, io.EOF }
func (t *actionTransport) Write(p []byte) (int, error) {
	return len(p), nil
}
func (t *actionTransport) Resize(int, int) error { return nil }
func (t *actionTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

type actionFactory struct{ transport *actionTransport }

func (f actionFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

func newActionSurface(t *testing.T, options ...Option) *Surface {
	t.Helper()
	service := appsession.NewService(actionFactory{transport: newActionTransport()})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	options = append(options, WithRedraw(func() {}))
	surface, err := New(session, options...)
	if err != nil {
		t.Fatalf("New surface: %v", err)
	}
	t.Cleanup(surface.Close)
	return surface
}

func TestSaveScreenWritesAndDismissesItsBanner(t *testing.T) {
	m := locales.In("en")
	called := false
	surface := newActionSurface(t, WithSaveScreen(func(text string) (string, error) {
		called = true
		return "screen.txt", nil
	}))
	surface.SetPaneActions(PaneActions{
		Title: "Local Session 1", MenuLabel: m.SessionMenuFor(1),
	})
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuSaveScreen()); err != nil {
		t.Fatalf("Save current screen: %v", err)
	}
	if !called {
		// An empty terminal still writes an empty file; the writer must run.
		t.Fatal("Save current screen did not call the writer")
	}
	tester.Frame()
	if !tester.HasText(m.SessionScreenSaved()) {
		t.Fatalf("the save result is not shown: %v", tester.Texts())
	}
	if err := tester.Click(m.SessionDismiss()); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if tester.HasText(m.SessionScreenSaved()) {
		t.Fatalf("the save result survived Dismiss: %v", tester.Texts())
	}
}

func TestRenameMenuOpensTheDialogAndAppliesTheTitle(t *testing.T) {
	m := locales.In("en")
	var renamed string
	surface := newActionSurface(t)
	surface.SetPaneActions(PaneActions{
		Title: "Local Session 1", MenuLabel: m.SessionMenuFor(1),
		Rename: func(title string) { renamed = title },
	})
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()

	if err := tester.Click(m.SessionMenuFor(1)); err != nil {
		t.Fatal(err)
	}
	if err := tester.ChooseMenuItem(m.SessionMenuRename()); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	tester.Frame()
	if !tester.HasText(m.SessionMenuRenameTitle()) {
		t.Fatalf("the Rename dialog did not open: %v", tester.Texts())
	}
	if err := tester.Click(m.SessionRenameField()); err != nil {
		t.Fatalf("focus the name field: %v", err)
	}
	// Select all with the platform's shortcut: Command+A on macOS, Control+A
	// elsewhere. On macOS Control+A is emacs beginning-of-line, which leaves
	// the old title in place and would buffer "prod" in front of it.
	tester.Key(ui.Cmd, ui.KeyA)
	tester.Type("prod")
	if err := tester.Click(m.SessionMenuRenameConfirm()); err != nil {
		t.Fatal(err)
	}
	if renamed != "prod" {
		t.Fatalf("renamed to %q, want prod", renamed)
	}
	tester.Frame()
	if tester.HasText(m.SessionMenuRenameTitle()) {
		t.Fatalf("the Rename dialog stayed open: %v", tester.Texts())
	}
}
