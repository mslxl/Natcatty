//go:build uitour

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mslxl/natcatty/adapters/store"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// TestVaultLayoutSurvivesRestart writes the Vault layout through the same
// store the app opens, then loads a fresh service from that file, as a
// restart does.
func TestVaultLayoutSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preferences.json")
	first := apppreferences.NewService(store.OpenPreferences(path))
	if err := first.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := first.Update(func(s *domainpreferences.Settings) {
		s.Layout.HostViewMode = domainpreferences.HostViewTree
		s.Layout.HostSortMode = domainpreferences.HostSortZA
		s.Layout.HostClickBehavior = domainpreferences.HostClickSelect
		s.Layout.ShowOnlyUngroupedHostsInRoot = true
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	t.Logf("saved: %s", data)

	second := apppreferences.NewService(store.OpenPreferences(path))
	if err := second.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := second.Settings().Layout
	if got.HostViewMode != domainpreferences.HostViewTree || got.HostSortMode != domainpreferences.HostSortZA ||
		got.HostClickBehavior != domainpreferences.HostClickSelect || !got.ShowOnlyUngroupedHostsInRoot {
		t.Fatalf("after restart layout = %+v", got)
	}
}
