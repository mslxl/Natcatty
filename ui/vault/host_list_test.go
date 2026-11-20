package vault

import (
	"testing"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// TestHostListChordIsScopedToTheList proves the toggle is registered on the
// host list rather than the whole Vault. The list takes the keyboard after the
// chord shows it, and the search field - a text field outside the list's
// subtree - keeps the chord for typing.
func TestHostListChordIsScopedToTheList(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{
		{ID: "h1", Label: "prod", Hostname: "prod.example.com"},
	}}
	st := vaultstate.NewState(repo)
	if err := st.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	toggles := 0
	focus := true
	tester := ui.NewTester(View(st, WithHostListToggle(
		func() (ui.Modifiers, ui.Key, bool) {
			return ui.Ctrl | ui.Shift, ui.KeyB, true
		},
		func() bool { toggles++; return true },
		func() bool {
			if focus {
				focus = false
				return true
			}
			return false
		},
	)), 900, 600)
	tester.SetLocale("en")
	tester.Frame()

	// The list was asked for the keyboard, so the chord reaches the scoped
	// handler without a click.
	tester.Key(ui.Ctrl|ui.Shift, ui.KeyB)
	tester.Frame()
	if toggles != 1 {
		t.Fatalf("the focused host list did not run the chord: %d toggles", toggles)
	}

	// The search field keeps the chord: it is not a descendant of the list.
	if err := tester.Click(locales.In("en").VaultSearchPlaceholder()); err != nil {
		t.Fatalf("focus the search field: %v", err)
	}
	tester.Frame()
	tester.Key(ui.Ctrl|ui.Shift, ui.KeyB)
	tester.Frame()
	if toggles != 1 {
		t.Fatalf("the chord fired while the search field had focus: %d toggles", toggles)
	}
}
