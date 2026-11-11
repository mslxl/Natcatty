package sysmanager

import (
	"testing"

	"github.com/egoist/mygo/ui"

	appsysmanager "github.com/mslxl/natcatty/application/sysmanager"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestSideViewShowsTmuxAndDockerTabs proves the side panel's System Tool keeps
// tmux and Docker as System Tabs with their existing content, in both locales.
// They are not separate side panel tools, so the panel must show them in the
// strip and render their own views.
func TestSideViewShowsTmuxAndDockerTabs(t *testing.T) {
	for _, locale := range []string{"en", "zh-CN"} {
		t.Run(locale, func(t *testing.T) {
			service := newFakeService()
			snapshot := demoSnapshot()
			snapshot.Tabs[appsysmanager.TabTmux] = appsysmanager.TabState{
				Tab: appsysmanager.TabTmux, Status: appsysmanager.StatusLive,
				Sessions: []appsysmanager.TmuxSession{
					{Name: "deploy", Windows: 2, Attached: true},
				},
			}
			snapshot.Tabs[appsysmanager.TabDocker] = appsysmanager.TabState{
				Tab: appsysmanager.TabDocker, Status: appsysmanager.StatusLive,
				Containers: []appsysmanager.Container{
					{Name: "web", Image: "nginx:1.27", State: "running", Status: "Up 2 hours"},
				},
			}
			service.snapshot = snapshot
			state := newTestState(service, newFakePreferences())
			state.ShowHost("alpha")

			tt := ui.NewTester(SideView(state, 360), 360, 620)
			tt.SetLocale(locale)
			tt.Frame()
			m := locales.In(locale)

			if err := tt.Click(m.SysManagerTabTmux()); err != nil {
				t.Fatalf("choose the tmux System Tab: %v", err)
			}
			tt.Frame()
			for _, want := range []string{"deploy", m.SysManagerNewSession()} {
				if !tt.HasText(want) {
					t.Fatalf("the side tmux tab lacks %q: %v", want, tt.Texts())
				}
			}
			if got := state.Tab(); got != appsysmanager.TabTmux {
				t.Fatalf("the side panel shows tab %s, want tmux", got)
			}

			if err := tt.Click(m.SysManagerTabDocker()); err != nil {
				t.Fatalf("choose the Docker System Tab: %v", err)
			}
			tt.Frame()
			for _, want := range []string{"web", m.SysManagerContainers()} {
				if !tt.HasText(want) {
					t.Fatalf("the side Docker tab lacks %q: %v", want, tt.Texts())
				}
			}
			if got := state.Tab(); got != appsysmanager.TabDocker {
				t.Fatalf("the side panel shows tab %s, want Docker", got)
			}
		})
	}
}
