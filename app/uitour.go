//go:build uitour

package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"

	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/shell/state"
	"github.com/mslxl/natcatty/ui/vault/section"
)

// RunUITour starts the app on a fresh NATCATTY_TOUR_PROFILE with in-memory
// secrets, seeds sample Hosts, then walks every page and captures the visible windows into
// NATCATTY_TOUR_OUT with the helper NATCATTY_TOUR_WIDS.
func RunUITour() error {
	profile := os.Getenv("NATCATTY_TOUR_PROFILE")
	out := os.Getenv("NATCATTY_TOUR_OUT")
	wids := os.Getenv("NATCATTY_TOUR_WIDS")
	// Secrets live only in memory, so every tour seeds a fresh profile.
	if err := os.RemoveAll(profile); err != nil {
		return err
	}
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	mygo.App.SetPath(mygo.PathUserData, profile)
	return run(func(win hostWindow, deps packagedSmokeDeps) {
		go func() {
			// The tour opens a local shell; without this the quit waits on the
			// close-sessions confirmation dialog.
			_ = deps.preferences.Update(func(s *domainpreferences.Settings) { s.Security.ConfirmCloseSessions = false })
			defer mygo.RunOnMain(func() { mygo.App.Quit() })
			ctx := context.Background()
			tourSeed(ctx, deps)
			shell := deps.shell.(*state.State)
			n := 0
			step := func(name string, fn func()) {
				mygo.RunOnMain(func() { fn(); win.Invalidate() })
				time.Sleep(1500 * time.Millisecond)
				mygo.RunOnMain(func() { win.Invalidate() })
				time.Sleep(300 * time.Millisecond)
				n++
				tourCapture(wids, out, fmt.Sprintf("%02d-%s", n, name))
			}
			pages := func(prefix string) {
				for _, p := range []section.Section{section.Hosts, section.Keychain, section.Proxies,
					section.Networks, section.Forwarding, section.Snippets, section.Notes, section.KnownHosts} {
					p := p
					step(prefix+"-vault-"+string(p), func() { shell.ShowVaultSection(p) })
				}
				step(prefix+"-logs", shell.ShowLogs)
				step(prefix+"-monitor", shell.ShowMonitor)
				step(prefix+"-system", shell.ShowSystemManager)
				step(prefix+"-sftp", shell.ShowSFTP)
			}
			theme := func(mode domainpreferences.ThemeMode) {
				_ = deps.preferences.Update(func(s *domainpreferences.Settings) { s.Appearance.ThemeMode = mode })
			}
			theme(domainpreferences.ThemeLight)
			pages("light")
			step("light-terminal", func() {
				_ = shell.OpenHost(domainvault.Host{ID: "tour-local", Label: "Local shell", Protocol: domainvault.ProtocolLocal})
			})
			time.Sleep(2 * time.Second)
			step("light-terminal-ready", func() {})
			step("light-side-open", func() {
				if id, ok := shell.TabID(shell.ActiveTab()); ok {
					shell.ToggleFiles(id, "", "")
				}
			})
			for _, tool := range []state.SideToolID{state.SideSFTP, state.SideScripts, state.SideHistory, state.SideTheme, state.SideSystem, state.SideNotes} {
				tool := tool
				step("light-side-"+string(tool), func() {
					if id, ok := shell.TabID(shell.ActiveTab()); ok {
						shell.SetSideTool(id, tool)
					}
				})
			}
			step("light-settings", shell.RequestSettings)
			theme(domainpreferences.ThemeDark)
			step("dark-terminal", func() {})
			pages("dark")
			shell.RequestLocale("zh-CN")
			pages("zh")
		}()
	}, runOptions{packagedSmoke: true, secrets: &tourSecrets{}})
}

// tourSecrets keeps secrets in memory so the tour never reaches the OS
// keychain or blocks on its permission prompt.
type tourSecrets struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *tourSecrets) Get(key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *tourSecrets) Set(key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *tourSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

func tourCapture(wids, out, name string) {
	raw, err := exec.Command(wids, fmt.Sprint(os.Getpid())).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tour: wids:", err)
		return
	}
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		path := filepath.Join(out, fmt.Sprintf("%s-w%d.png", name, i))
		if err := exec.Command("screencapture", "-x", "-o", "-l", fields[0], path).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "tour: capture:", err)
		}
		fmt.Println("tour:", path)
	}
}

func tourSeed(ctx context.Context, deps packagedSmokeDeps) {
	_ = deps.vault.UseSessionSecrets(ctx)
	drafts := []domainvault.Draft{
		{Label: "prod-web-01", Hostname: "10.0.1.11", Port: 22, Username: "deploy", Protocol: domainvault.ProtocolSSH, Group: "Production", Tags: []string{"web", "nginx"}, Pinned: true},
		{Label: "prod-web-02", Hostname: "10.0.1.12", Port: 22, Username: "deploy", Protocol: domainvault.ProtocolSSH, Group: "Production", Tags: []string{"web"}},
		{Label: "prod-db-primary", Hostname: "db.internal.example.com", Port: 2222, Username: "postgres", Protocol: domainvault.ProtocolSSH, Group: "Production/Database", Tags: []string{"db"}},
		{Label: "staging-api", Hostname: "staging.example.com", Port: 22, Username: "ubuntu", Protocol: domainvault.ProtocolMosh, Group: "Staging"},
		{Label: "build-box-with-a-really-long-name-for-truncation", Hostname: "build-01.ci.very-long-subdomain.example.org", Port: 22, Username: "ci", Protocol: domainvault.ProtocolET, Group: "CI", Tags: []string{"ci", "linux", "arm64", "large"}},
		{Label: "core-switch", Hostname: "192.168.0.1", Port: 23, Protocol: domainvault.ProtocolTelnet, Group: "Network"},
		{Label: "开发机", Hostname: "dev.local", Port: 22, Username: "me", Protocol: domainvault.ProtocolSSH},
		{Label: "Local zsh", Protocol: domainvault.ProtocolLocal},
	}
	for _, d := range drafts {
		if _, err := deps.vault.Create(ctx, d); err != nil {
			fmt.Fprintln(os.Stderr, "tour: seed:", d.Label, err)
		}
	}
}
