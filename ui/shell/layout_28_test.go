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

// TestVaultHeaderFitsAtMinimumSize is the ticket-28 D1 regression: at the
// 720x480 minimum the host list title stays on one line and every header
// action (Import Hosts, Export Vault, Backups, New Host) stays inside the
// content column in all five locales. At 8c4ce36 the non-wrapping header row
// squeezed "Hosts (0)" onto two lines and pushed New Host past the content's
// right edge, where the window clipped it.
func TestVaultHeaderFitsAtMinimumSize(t *testing.T) {
	const epsilon = 0.5
	// contentInset mirrors the content column's 16 DIP padding. It is spelled
	// here rather than imported so the test also compiles against 8c4ce36,
	// where the header regression lived.
	const contentInset float32 = 16
	for _, tag := range locales.Tags() {
		m := locales.In(tag)
		for _, size := range [][2]int{{720, 480}, {1100, 720}} {
			width, height := float32(size[0]), float32(size[1])
			tester, _ := newVaultTester(t, size[0], size[1])
			tester.SetLocale(tag)
			tester.Frame()

			title, ok := tester.Find(m.VaultHostListTitle(0))
			if !ok {
				t.Fatalf("%s %dx%d does not show %q: %v", tag, size[0], size[1], m.VaultHostListTitle(0), tester.Texts())
			}
			// A 20 DIP label is about 27 DIPs tall; two lines are about 53.
			if title.H > 34 {
				t.Fatalf("%s %dx%d wraps the host list title onto two lines: %+v", tag, size[0], size[1], title)
			}

			actions := []string{
				m.VaultImportTitle(),
				m.VaultExportTitle(),
				m.VaultBackupOpen(),
				m.VaultHostNew(),
			}
			rects := make([]ui.Rect, 0, len(actions))
			for _, action := range actions {
				rect, ok := tester.Find(action)
				if !ok {
					t.Fatalf("%s %dx%d does not show %q: %v", tag, size[0], size[1], action, tester.Texts())
				}
				if rect.X < sidebarWidth+contentInset-epsilon ||
					rect.X+rect.W > width-contentInset+epsilon ||
					rect.Y < -epsilon || rect.Y+rect.H > height+epsilon {
					t.Fatalf("%s %dx%d clips %q: box %+v, content column is x=%.1f..%.1f",
						tag, size[0], size[1], action, rect, sidebarWidth, width-contentInset)
				}
				rects = append(rects, rect)
			}

			// Wrapped actions move to the next line; nothing may overlap.
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

// TestVaultHeaderActionsAreClickableAtMinimumSize proves every header action
// is reachable at 720x480, not merely painted: a click at its center hits it
// and opens its surface. A clipped or overlapping action would not receive the
// pointer event.
func TestVaultHeaderActionsAreClickableAtMinimumSize(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		action string
		opened func(*vaultstate.State) bool
	}{
		{m.VaultImportTitle(), func(s *vaultstate.State) bool { return s.ImportOpen() }},
		{m.VaultExportTitle(), func(s *vaultstate.State) bool { return s.ExportOpen() }},
		// The empty test state has no backup service, so opening the panel
		// records the unavailable problem instead of the open flag; either
		// proves the click reached Backups.
		{m.VaultBackupOpen(), func(s *vaultstate.State) bool {
			return s.BackupsOpen() || s.BackupProblem() != vaultstate.ProblemNone
		}},
		{m.VaultHostNew(), func(s *vaultstate.State) bool { return s.Editing() }},
	}
	for _, test := range cases {
		tester, vault := newVaultTester(t, 720, 480)
		tester.SetLocale("en")
		tester.Frame()
		if err := tester.Click(test.action); err != nil {
			t.Fatalf("click %q at 720x480: %v", test.action, err)
		}
		tester.Frame()
		if !test.opened(vault) {
			t.Fatalf("clicking %q at 720x480 did not open it", test.action)
		}
	}
}
