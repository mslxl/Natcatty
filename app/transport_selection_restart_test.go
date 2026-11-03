package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	adapterstore "github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uisettings "github.com/mslxl/natcatty/ui/settings"
)

// TestInProcessSelectionFromSettingsSurvivesRestart drives the Settings seam
// and the Session factory together: choosing the in-process client persists
// through a fresh service over the same preferences file. ET has a pure-Go
// client, so it opens through it; Mosh has none yet, so it still fails closed
// with the typed unavailable error instead of using the bundled client.
func TestInProcessSelectionFromSettingsSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "preferences.json")

	first := apppreferences.NewService(adapterstore.OpenPreferences(path))
	if err := first.Load(ctx); err != nil {
		t.Fatal(err)
	}
	state := uisettings.NewPreferencesState(first)
	if !state.Update(func(settings *domainpreferences.Settings) {
		settings.Transport.Implementation = transportmodel.InProcess
	}) {
		t.Fatal("the Settings seam could not save the in-process choice")
	}

	restarted := apppreferences.NewService(adapterstore.OpenPreferences(path))
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Settings().Transport.Implementation; got != transportmodel.InProcess {
		t.Fatalf("implementation after restart = %q, want in_process", got)
	}

	hosts := fakeImplementationHosts{items: map[model.HostID]domainvault.Host{}}
	factory := newImplementationFactory(hosts, restarted.Settings())

	if _, err := factory.Open(ctx, appsession.StartSpec{Kind: appsession.KindMosh}); !errors.Is(err, transportmodel.ErrImplementationUnavailable) {
		t.Fatalf("Mosh Open error = %v, want ErrImplementationUnavailable", err)
	}
	transport, err := factory.Open(ctx, appsession.StartSpec{Kind: appsession.KindET})
	if err != nil {
		t.Fatalf("ET Open: %v", err)
	}
	_ = transport.Close()

	if factory.mosh.(*implementationFactory).calls != 0 ||
		factory.et.(*implementationFactory).calls != 0 {
		t.Fatal("a Session opened with the in-process selection silently used the bundled client")
	}
	if factory.etInProcess.(*implementationFactory).calls != 1 {
		t.Fatalf("in-process ET opens = %d, want 1",
			factory.etInProcess.(*implementationFactory).calls)
	}
}
