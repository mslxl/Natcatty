package terminalsettings

import (
	"context"
	"errors"
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

type fakeStore struct {
	settings domainterminal.Settings
	loadErr  error
	saveErr  error
	saves    int
}

func (s *fakeStore) Load(context.Context) (domainterminal.Settings, error) {
	return s.settings, s.loadErr
}

func (s *fakeStore) Save(_ context.Context, settings domainterminal.Settings) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.settings = settings
	s.saves++
	return nil
}

func TestLoadUsesOriginalDefaultsForMissingValues(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	settings := service.Settings()
	if settings.LogFormat != domainterminal.LogFormatText ||
		settings.OSC52 != domainterminal.ClipboardWriteOnly ||
		settings.OSCNotifications != domainterminal.NotificationAlways {
		t.Fatalf("settings = %+v", settings)
	}
}

func TestUpdatePersistsAndPublishes(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	var published []domainterminal.Settings
	service.onChange = func(settings domainterminal.Settings) {
		published = append(published, settings)
	}
	if err := service.SetLogFormat(domainterminal.LogFormatHTML); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || store.settings.LogFormat != domainterminal.LogFormatHTML {
		t.Fatalf("store = %+v, saves = %d", store.settings, store.saves)
	}
	if len(published) != 1 || published[0].LogFormat != domainterminal.LogFormatHTML {
		t.Fatalf("published = %+v", published)
	}
	if service.Settings().LogFormat != domainterminal.LogFormatHTML {
		t.Fatalf("service settings = %+v", service.Settings())
	}
}

func TestFailedSaveKeepsThePreviousValue(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store)
	if err := service.SetOSC52(domainterminal.ClipboardPrompt); err != nil {
		t.Fatal(err)
	}
	store.saveErr = errors.New("disk full")
	if err := service.SetOSC52(domainterminal.ClipboardOff); err == nil {
		t.Fatal("a failed save was reported as success")
	}
	if got := service.Settings().OSC52; got != domainterminal.ClipboardPrompt {
		t.Fatalf("OSC52 after a failed save = %q, want %q", got, domainterminal.ClipboardPrompt)
	}
}

func TestEverySetterUpdatesItsField(t *testing.T) {
	service := NewService(&fakeStore{})
	steps := []func() error{
		func() error { return service.SetLoggingEnabled(true) },
		func() error { return service.SetLogDirectory(`C:\logs`) },
		func() error { return service.SetLogFormat(domainterminal.LogFormatRaw) },
		func() error { return service.SetLogTimestamps(true) },
		func() error { return service.SetOSC52(domainterminal.ClipboardReadWrite) },
		func() error { return service.SetOSCNotifications(domainterminal.NotificationOff) },
	}
	for index, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", index, err)
		}
	}
	settings := service.Settings()
	if !settings.LoggingEnabled || settings.LogDirectory != `C:\logs` ||
		settings.LogFormat != domainterminal.LogFormatRaw || !settings.LogTimestamps ||
		settings.OSC52 != domainterminal.ClipboardReadWrite ||
		settings.OSCNotifications != domainterminal.NotificationOff {
		t.Fatalf("settings = %+v", settings)
	}
}
