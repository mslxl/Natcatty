package app

import (
	"context"
	"testing"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

type recordingStartupShell struct {
	calls []string
	// restoreTabs is how many Tabs the fake Workspace restores.
	restoreTabs int
	tabs        int
}

func (s *recordingStartupShell) Restore(context.Context) {
	s.calls = append(s.calls, "restore")
	s.tabs = s.restoreTabs
}
func (s *recordingStartupShell) TabCount() int { return s.tabs }
func (s *recordingStartupShell) ShowMonitor()  { s.calls = append(s.calls, "monitor") }
func (s *recordingStartupShell) ShowVault()    { s.calls = append(s.calls, "vault") }
func (s *recordingStartupShell) NewTab() error {
	s.calls = append(s.calls, "terminal")
	return nil
}

func TestApplyStartupLandingShowsTheChosenSurface(t *testing.T) {
	cases := []struct {
		name        string
		layout      domainpreferences.Layout
		restoreTabs int
		want        []string
	}{
		{"vault", domainpreferences.Layout{StartupLanding: domainpreferences.StartupVault}, 0, []string{"vault"}},
		{"monitoring", domainpreferences.Layout{StartupLanding: domainpreferences.StartupMonitoring}, 0, []string{"monitor"}},
		{"local terminal", domainpreferences.Layout{StartupLanding: domainpreferences.StartupTerminal}, 0, []string{"terminal"}},
		{"last session", domainpreferences.Layout{StartupLanding: domainpreferences.StartupLast}, 0, []string{"terminal"}},
		{"unknown landing", domainpreferences.Layout{StartupLanding: "future"}, 0, []string{"vault"}},
		{
			"restore wins over a landing",
			domainpreferences.Layout{
				RestorePreviousSession: true,
				StartupLanding:         domainpreferences.StartupMonitoring,
			},
			1,
			[]string{"restore"},
		},
		{
			"nothing to restore falls back to monitoring",
			domainpreferences.Layout{
				RestorePreviousSession: true,
				StartupLanding:         domainpreferences.StartupMonitoring,
			},
			0,
			[]string{"restore", "monitor"},
		},
		{
			"nothing to restore falls back to a local terminal",
			domainpreferences.Layout{
				RestorePreviousSession: true,
				StartupLanding:         domainpreferences.StartupTerminal,
			},
			0,
			[]string{"restore", "terminal"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			shell := &recordingStartupShell{restoreTabs: test.restoreTabs}
			applyStartupLanding(test.layout, shell)
			if len(shell.calls) != len(test.want) {
				t.Fatalf("calls = %v, want %v", shell.calls, test.want)
			}
			for i, want := range test.want {
				if shell.calls[i] != want {
					t.Fatalf("calls = %v, want %v", shell.calls, test.want)
				}
			}
		})
	}
}
