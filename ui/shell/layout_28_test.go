package shell

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/ui/locales"
	"github.com/mslxl/natcatty/ui/shell/state"
	vaultui "github.com/mslxl/natcatty/ui/vault"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// newVaultTester renders the real shell around an empty Vault at width×height.
func newVaultTester(t *testing.T, width, height int) (*ui.Tester, *vaultstate.State) {
	t.Helper()
	service := appsession.NewService(&fakeFactory{transport: newFakeTransport()})
	t.Cleanup(service.CloseAll)
	vault := vaultstate.NewState(emptyVaultHosts{})
	shellState := state.NewState(
		state.WithSessions(service, func(*appsession.Session) (state.Surface, error) {
			return fakeSurface{}, nil
		}),
		state.WithVault(vaultui.View(vault)),
	)
	tester := ui.NewTester(View(shellState), width, height)
	tester.Frame()
	tester.Frame()
	return tester, vault
}

// TestVaultToolbarFitsAtMinimumSize is the ticket-28 D1 regression for the
// Hosts toolbar: at the 720x480 minimum the host list title stays on one line
// and every toolbar control (the search field, New Host, and its More actions
// menu) stays inside the content in all five locales, without overlapping.
// At 8c4ce36 a non-wrapping header squeezed "Hosts (0)" onto two lines and
// pushed New Host past the content's right edge, where the window clipped it.
func TestVaultToolbarFitsAtMinimumSize(t *testing.T) {
	const epsilon = 0.5
	for _, tag := range locales.Tags() {
		m := locales.In(tag)
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			width, height := float32(size[0]), float32(size[1])
			content := homeContent(width, height)
			tester, _ := newVaultTester(t, size[0], size[1])
			tester.SetLocale(tag)
			tester.Frame()

			title, ok := tester.Find(m.VaultHostListTitle(0))
			if !ok {
				t.Fatalf("%s %dx%d does not show %q: %v", tag, size[0], size[1], m.VaultHostListTitle(0), tester.Texts())
			}
			// The 13 DIP heading with its padding is about 29 DIPs tall; two
			// lines are about 46.
			if title.H > 36 {
				t.Fatalf("%s %dx%d wraps the host list title onto two lines: %+v", tag, size[0], size[1], title)
			}

			actions := []string{m.VaultSearchPlaceholder(), m.VaultHostNew(), m.VaultMoreActions()}
			rects := make([]ui.Rect, 0, len(actions))
			for _, action := range actions {
				rect, ok := tester.Find(action)
				if !ok {
					t.Fatalf("%s %dx%d does not show %q: %v", tag, size[0], size[1], action, tester.Texts())
				}
				if rect.X < content.X-epsilon || rect.X+rect.W > content.X+content.W+epsilon ||
					rect.Y < content.Y-epsilon || rect.Y+rect.H > height+epsilon {
					t.Fatalf("%s %dx%d clips %q: box %+v, content %+v",
						tag, size[0], size[1], action, rect, content)
				}
				rects = append(rects, rect)
			}

			// Wrapped controls move to the next line; nothing may overlap.
			for i := range rects {
				if overlaps(rects[i], title) {
					t.Fatalf("%s %dx%d: %q overlaps the title: %+v, %+v",
						tag, size[0], size[1], actions[i], rects[i], title)
				}
				for j := i + 1; j < len(rects); j++ {
					if overlaps(rects[i], rects[j]) {
						t.Fatalf("%s %dx%d: %q overlaps %q: %+v, %+v",
							tag, size[0], size[1], actions[i], actions[j], rects[i], rects[j])
					}
				}
			}
		}
	}
}

// TestVaultToolbarActionsAreReachableAtMinimumSize proves every Vault-wide
// action is reachable at 720x480, not merely painted: New Host is clicked at
// its center, and the others are chosen from the More actions menu beside it.
// A clipped or overlapping control would not receive the pointer event.
func TestVaultToolbarActionsAreReachableAtMinimumSize(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		action string
		opened func(*vaultstate.State) bool
	}{
		{m.VaultGroupNew(), func(s *vaultstate.State) bool { return s.EditingGroup() }},
		{m.VaultImportTitle(), func(s *vaultstate.State) bool { return s.ImportOpen() }},
		{m.VaultExportTitle(), func(s *vaultstate.State) bool { return s.ExportOpen() }},
		// The empty test state has no backup service, so opening the panel
		// records the unavailable problem instead of the open flag; either
		// proves the menu reached Backups.
		{m.VaultBackupOpen(), func(s *vaultstate.State) bool {
			return s.BackupsOpen() || s.BackupProblem() != vaultstate.ProblemNone
		}},
	}
	for _, test := range cases {
		tester, vault := newVaultTester(t, 720, 480)
		tester.SetLocale("en")
		tester.Frame()
		if err := tester.Click(m.VaultMoreActions()); err != nil {
			t.Fatalf("click More actions at 720x480: %v", err)
		}
		if err := tester.ChooseMenuItem(test.action); err != nil {
			t.Fatalf("choose %q at 720x480: %v", test.action, err)
		}
		tester.Frame()
		if !test.opened(vault) {
			t.Fatalf("choosing %q at 720x480 did not open it", test.action)
		}
	}

	tester, vault := newVaultTester(t, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	if err := tester.Click(m.VaultHostNew()); err != nil {
		t.Fatalf("click New Host at 720x480: %v", err)
	}
	tester.Frame()
	if !vault.Editing() {
		t.Fatal("clicking New Host at 720x480 did not open the editor")
	}
}
