package app

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/command"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
)

type wiringTransport struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func (t *wiringTransport) Read([]byte) (int, error) {
	<-t.closed
	return 0, io.EOF
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

type wiringSurface struct{}

func (wiringSurface) View(c *ui.Context) ui.Element { return ui.Text(c, "terminal").Fill() }
func (wiringSurface) Close()                        {}

func newWiringState(t *testing.T) *state.State {
	t.Helper()
	service := appsession.NewService(&wiringFactory{transport: &wiringTransport{closed: make(chan struct{})}})
	t.Cleanup(service.CloseAll)
	return state.NewState(state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
		return wiringSurface{}, nil
	}))
}

func TestCommandsDriveTheShellAndQuit(t *testing.T) {
	var locales []string
	service := appsession.NewService(&wiringFactory{transport: &wiringTransport{closed: make(chan struct{})}})
	t.Cleanup(service.CloseAll)
	shellState := state.NewState(
		state.WithLocaleRequest(func(tag string) { locales = append(locales, tag) }),
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return wiringSurface{}, nil
		}),
	)
	quits := 0
	registry := newRegistry(shellState, func() { quits++ })

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

	if err := registry.Dispatch(context.Background(), command.Quit); err != nil {
		t.Fatalf("Quit: %v", err)
	}
	if quits != 1 {
		t.Fatalf("quits = %d, want 1", quits)
	}
}

func TestRegistryKnowsEveryMenuCommand(t *testing.T) {
	shellState := newWiringState(t)
	registry := newRegistry(shellState, func() {})

	ids := []command.ID{command.NewTab, command.CloseTab, command.Quit}
	for _, tag := range locales.Tags() {
		ids = append(ids, command.LocaleCommandID(tag))
	}
	for _, id := range ids {
		if !registry.Has(id) {
			t.Errorf("command %q is not registered", id)
		}
	}
}
