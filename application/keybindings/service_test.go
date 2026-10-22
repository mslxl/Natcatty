package keybindings

import (
	"context"
	"errors"
	"testing"

	domain "github.com/mslxl/natcatty/domain/keybindings"
)

type fakeStore struct {
	state State
	err   error
	saves int
}

func (f *fakeStore) Load(context.Context) (State, error) { return f.state, f.err }
func (f *fakeStore) Save(_ context.Context, state State) error {
	f.state = state
	f.saves++
	return f.err
}

func newService(t *testing.T, options ...Option) (*Service, *fakeStore) {
	t.Helper()
	store := &fakeStore{}
	opts := append([]Option{WithGOOS("windows")}, options...)
	service := NewService(store, opts...)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	return service, store
}

func TestDefaultSchemeIsPCOnWindowsAndMacOnDarwin(t *testing.T) {
	service, _ := newService(t)
	if got := service.Scheme(); got != domain.SchemePC {
		t.Errorf("windows scheme = %q", got)
	}
	store := &fakeStore{}
	darwin := NewService(store, WithGOOS("darwin"))
	_ = darwin.Load(context.Background())
	if got := darwin.Scheme(); got != domain.SchemeMac {
		t.Errorf("darwin scheme = %q", got)
	}
}

func TestSetSchemePersistsAndPublishes(t *testing.T) {
	service, store := newService(t)
	var published domain.Scheme
	service.SetChangeHook(func(state State) { published = state.Scheme })
	if err := service.SetScheme(domain.SchemeMac); err != nil {
		t.Fatalf("SetScheme: %v", err)
	}
	if service.Scheme() != domain.SchemeMac || store.state.Scheme != domain.SchemeMac || published != domain.SchemeMac {
		t.Errorf("scheme = %q, stored = %q, published = %q", service.Scheme(), store.state.Scheme, published)
	}
	if err := service.SetScheme("nonsense"); !errors.Is(err, ErrInvalidScheme) {
		t.Errorf("invalid scheme error = %v", err)
	}
}

func TestSetBindingRejectsConflictsUntilResolvedByReplace(t *testing.T) {
	service, store := newService(t)
	menus := []domain.MenuShortcut{{ID: "menu.quit", Label: "Quit", Accelerator: "CmdOrCtrl+Q"}}

	conflicts, err := service.SetBinding("open-local", domain.SchemePC, "Ctrl + T", menus)
	if err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if len(conflicts) == 0 {
		t.Fatal("Ctrl+T did not conflict with New Tab")
	}
	if store.saves != 0 {
		t.Fatal("a conflicting binding was persisted without resolution")
	}
	if service.State().Overrides.Has("open-local", domain.SchemePC) {
		t.Fatal("a conflicting binding reached the service state")
	}

	if err := service.ReplaceBinding("open-local", domain.SchemePC, "Ctrl + T", menus); err != nil {
		t.Fatalf("ReplaceBinding: %v", err)
	}
	if got := service.Chord("open-local", domain.SchemePC); got != "Ctrl + T" {
		t.Errorf("open-local = %q", got)
	}
	if got := service.Chord("new-tab", domain.SchemePC); got != domain.Disabled {
		t.Errorf("the displaced new-tab binding = %q, want Disabled", got)
	}
	if store.saves != 1 {
		t.Errorf("saves = %d", store.saves)
	}
}

func TestSetBindingRejectsMenuConflictAndReplaceSuppressesTheMenu(t *testing.T) {
	service, _ := newService(t)
	menus := []domain.MenuShortcut{{ID: "menu.quit", Label: "Quit", Accelerator: "CmdOrCtrl+Q"}}
	conflicts, err := service.SetBinding("new-workspace", domain.SchemePC, "Ctrl + Q", menus)
	if err != nil || len(conflicts) == 0 || !conflicts[0].HasMenu() {
		t.Fatalf("conflicts = %+v, err = %v", conflicts, err)
	}
	if err := service.ReplaceBinding("new-workspace", domain.SchemePC, "Ctrl + Q", menus); err != nil {
		t.Fatalf("ReplaceBinding: %v", err)
	}
	if !service.MenuSuppressed("menu.quit") {
		t.Error("the menu accelerator was not suppressed")
	}
	if conflicts := service.Conflicts(menus); len(conflicts) != 0 {
		t.Errorf("conflicts after replacement = %+v", conflicts)
	}
}

func TestResetBindingAndResetAll(t *testing.T) {
	service, _ := newService(t)
	if _, err := service.SetBinding("open-local", domain.SchemeMac, "⌘ + Shift + L", nil); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
	if got := service.Chord("open-local", domain.SchemeMac); got != "⌘ + Shift + L" {
		t.Fatalf("override = %q", got)
	}
	if err := service.ResetBinding("open-local", domain.SchemeMac); err != nil {
		t.Fatalf("ResetBinding: %v", err)
	}
	if got := service.Chord("open-local", domain.SchemeMac); got != "⌘ + L" {
		t.Errorf("after reset = %q", got)
	}
	if _, err := service.SetBinding("open-local", domain.SchemeMac, "⌘ + Shift + L", nil); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetAll(); err != nil {
		t.Fatalf("ResetAll: %v", err)
	}
	if service.State().Overrides.Has("open-local", domain.SchemeMac) {
		t.Error("ResetAll left an override")
	}
	if service.MenuSuppressed("menu.quit") {
		t.Error("ResetAll left a suppressed menu")
	}
}

func TestSetBindingRejectsMalformedChords(t *testing.T) {
	service, _ := newService(t)
	if _, err := service.SetBinding("open-local", domain.SchemePC, "Ctrl +", nil); !errors.Is(err, ErrInvalidChord) {
		t.Errorf("error = %v", err)
	}
	if _, err := service.SetBinding("missing", domain.SchemePC, "Ctrl + L", nil); !errors.Is(err, ErrUnknownBinding) {
		t.Errorf("error = %v", err)
	}
}

func TestActiveFollowsTheScheme(t *testing.T) {
	service, _ := newService(t)
	pc := service.Active()
	if len(pc) != 40 {
		t.Errorf("PC active = %d", len(pc))
	}
	if err := service.SetScheme(domain.SchemeDisabled); err != nil {
		t.Fatal(err)
	}
	if active := service.Active(); len(active) != 0 {
		t.Errorf("disabled active = %d", len(active))
	}
}
