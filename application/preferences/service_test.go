package preferences

import (
	"context"
	"testing"

	domain "github.com/mslxl/natcatty/domain/preferences"
)

type fakeStore struct {
	settings domain.Settings
	err      error
	saves    int
}

func (f *fakeStore) Load(context.Context) (domain.Settings, error) { return f.settings, f.err }
func (f *fakeStore) Save(_ context.Context, settings domain.Settings) error {
	f.settings = settings
	f.saves++
	return f.err
}

func TestUpdatePersistsNormalizesAndPublishes(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	var published domain.Settings
	service.SetChangeHook(func(settings domain.Settings) { published = settings })
	if err := service.Update(func(settings *domain.Settings) {
		settings.Appearance.WindowOpacity = 0.2
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if service.Settings().Appearance.WindowOpacity != 0.5 {
		t.Errorf("normalized opacity = %v", service.Settings().Appearance.WindowOpacity)
	}
	if store.settings.Appearance.WindowOpacity != 0.5 || store.saves != 1 {
		t.Errorf("stored = %v, saves = %d", store.settings.Appearance.WindowOpacity, store.saves)
	}
	if published.Appearance.WindowOpacity != 0.5 {
		t.Errorf("published = %v", published.Appearance.WindowOpacity)
	}
}

func TestResetGroupAndResetAll(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	_ = service.Load(context.Background())
	_ = service.Update(func(settings *domain.Settings) {
		settings.Appearance.WindowOpacity = 0.7
		settings.Layout.ShowTabNumberBadges = false
	})
	if err := service.ResetGroup(domain.GroupAppearance); err != nil {
		t.Fatal(err)
	}
	if service.Settings().Appearance.WindowOpacity != 1 || service.Settings().Layout.ShowTabNumberBadges {
		t.Errorf("group reset = %+v", service.Settings())
	}
	if err := service.ResetAll(); err != nil {
		t.Fatal(err)
	}
	if got := service.Settings(); got != domain.Defaults() {
		t.Errorf("reset all = %+v", got)
	}
}

func TestSaveFailureLeavesTheOldValue(t *testing.T) {
	store := &fakeStore{err: context.DeadlineExceeded}
	service := NewService(store)
	_ = service.Load(context.Background())
	if err := service.Update(func(settings *domain.Settings) {
		settings.Appearance.WindowOpacity = 0.6
	}); err == nil {
		t.Fatal("Update hid the store failure")
	}
	if service.Settings().Appearance.WindowOpacity != 1 {
		t.Errorf("failed save changed the value to %v", service.Settings().Appearance.WindowOpacity)
	}
}
