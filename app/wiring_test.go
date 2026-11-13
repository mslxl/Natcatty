package app

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/command"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type wiringTransport struct {
	closed chan struct{}
	// output feeds scripted terminal bytes, such as an OSC title, to the
	// Session. A nil channel keeps the transport silent until Close.
	output    chan []byte
	closeOnce sync.Once
}

func (t *wiringTransport) Read(p []byte) (int, error) {
	if t.output == nil {
		<-t.closed
		return 0, io.EOF
	}
	select {
	case data := <-t.output:
		return copy(p, data), nil
	case <-t.closed:
		return 0, io.EOF
	}
}
func (t *wiringTransport) Write(p []byte) (int, error) { return len(p), nil }
func (t *wiringTransport) Resize(int, int) error       { return nil }
func (t *wiringTransport) Wait() (int, error)          { return 0, nil }
func (t *wiringTransport) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

type wiringFactory struct{ transport *wiringTransport }

func (f *wiringFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// wiringSurface is non-empty so distinct surfaces have distinct pointers;
// Go may give distinct zero-size values the same address. It records the live
// theme, Interface Style, and focus requests the rendering window saw, so a
// window test can prove which renderer drew the Surface and that a rebuilt
// frame asks for the keyboard again.
type wiringSurface struct {
	id              int
	focuses         int
	style           kit.Style
	themeBackground ui.Color
}

func (w *wiringSurface) View(c *ui.Context) ui.Element {
	w.style = kit.CurrentStyle()
	w.themeBackground = c.Theme().Background
	return ui.Text(c, "terminal").Fill()
}

func (w *wiringSurface) ViewFocused(c *ui.Context, focused bool) ui.Element {
	if focused {
		w.focuses++
	}
	return w.View(c)
}

func (w *wiringSurface) Close() {}

func newWiringState(t *testing.T) *state.State {
	t.Helper()
	service := appsession.NewService(&wiringFactory{transport: &wiringTransport{closed: make(chan struct{})}})
	t.Cleanup(service.CloseAll)
	return state.NewState(state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
		return &wiringSurface{}, nil
	}))
}

func TestCommandsDriveTheShellAndQuit(t *testing.T) {
	var locales []string
	service := appsession.NewService(&wiringFactory{transport: &wiringTransport{closed: make(chan struct{})}})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithLocaleRequest(func(tag string) { locales = append(locales, tag) }),
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return &wiringSurface{}, nil
		}),
	)
	quits := 0
	locks := 0
	registry := newRegistry(shellState, func() { quits++ }, func() error {
		locks++
		return nil
	}, nil, func() {})

	if err := registry.Dispatch(context.Background(), command.NewTab); err != nil {
		t.Fatalf("NewTab: %v", err)
	}
	if shellState.TabCount() != 1 {
		t.Fatalf("tabs = %d after New Tab, want 1", shellState.TabCount())
	}
	if err := registry.Dispatch(context.Background(), command.CloseTab); err != nil {
		t.Fatalf("CloseTab: %v", err)
	}
	if shellState.TabCount() != 0 {
		t.Fatalf("tabs = %d after Close Tab, want 0", shellState.TabCount())
	}

	if err := registry.Dispatch(context.Background(), command.LocaleCommandID("zh-TW")); err != nil {
		t.Fatalf("LocaleCommandID: %v", err)
	}
	if len(locales) != 1 || locales[0] != "zh-TW" {
		t.Fatalf("requested locales = %v, want [zh-TW]", locales)
	}

	if err := registry.Dispatch(context.Background(), command.LockApp); err != nil {
		t.Fatalf("LockApp: %v", err)
	}
	if locks != 1 {
		t.Fatalf("locks = %d, want 1", locks)
	}

	if err := registry.Dispatch(context.Background(), command.Quit); err != nil {
		t.Fatalf("Quit: %v", err)
	}
	if quits != 1 {
		t.Fatalf("quits = %d, want 1", quits)
	}
}

func TestRegistryKnowsEveryMenuCommand(t *testing.T) {
	shellState := newWiringState(t)
	registry := newRegistry(shellState, func() {}, func() error { return nil }, nil, func() {})

	ids := []command.ID{
		command.NewTab,
		command.CloseTab,
		command.CloseOtherTabs,
		command.CloseTabsRight,
		command.DuplicateTab,
		command.DetachTab,
		command.CopyTabToNewWindow,
		command.SplitPaneRight,
		command.SplitPaneDown,
		command.ClosePane,
		command.DetachPane,
		command.FocusPaneLeft,
		command.FocusPaneRight,
		command.FocusPaneUp,
		command.FocusPaneDown,
		command.LockApp,
		command.OpenBackups,
		command.Quit,
	}
	for _, tag := range locales.Tags() {
		ids = append(ids, command.LocaleCommandID(tag))
	}
	for _, id := range ids {
		if !registry.Has(id) {
			t.Errorf("command %q is not registered", id)
		}
	}
}
