package preferences

import "testing"

// The Vault's view and sort are per-device: a card grid chosen on a desktop
// must not follow the user to a narrow laptop, while the root-filter setting is
// a Vault preference and does travel.
func TestVaultViewAndSortStayOnTheDevice(t *testing.T) {
	settings := Defaults()
	settings.Layout.HostViewMode = HostViewTree
	settings.Layout.HostSortMode = HostSortZA
	settings.Layout.ShowOnlyUngroupedHostsInRoot = true

	portable := Portable(settings)
	if portable.Layout.HostViewMode != "" || portable.Layout.HostSortMode != "" {
		t.Fatalf("portable layout = %+v, want the view fields stripped", portable.Layout)
	}
	if !portable.Layout.ShowOnlyUngroupedHostsInRoot {
		t.Fatal("the root filter did not travel")
	}

	merged := MergePortable(settings, portable)
	if merged.Layout.HostViewMode != HostViewTree || merged.Layout.HostSortMode != HostSortZA {
		t.Fatalf("merge lost the local layout: %+v", merged.Layout)
	}
}

func TestNormalizeKeepsValidVaultLayoutOnly(t *testing.T) {
	settings := Defaults()
	settings.Layout.HostViewMode = HostViewList
	settings.Layout.HostSortMode = HostSortIP
	settings.Layout.HostClickBehavior = HostClickSelect
	if got := Normalize(settings); !Equal(got, settings) {
		t.Fatalf("a valid layout changed: %+v", got.Layout)
	}

	settings.Layout.HostViewMode = "carousel"
	settings.Layout.HostSortMode = "sideways"
	settings.Layout.HostClickBehavior = "hover"
	normalized := Normalize(settings).Layout
	if normalized.HostViewMode != HostViewGrid || normalized.HostSortMode != HostSortManual ||
		normalized.HostClickBehavior != HostClickConnect {
		t.Fatalf("invalid layout normalized to %+v", normalized)
	}
}
