//go:build uitour

package vault

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"

	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	shellui "github.com/mslxl/natcatty/ui/shell"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	"github.com/mslxl/natcatty/ui/vault/section"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// TestRenderUITour writes offscreen PNGs of the Vault pages with fake data to
// NATCATTY_SCREENSHOT_DIR for a visual review. It never touches real stores.
func TestRenderUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	hosts := &entityHosts{items: []domainvault.Host{
		{ID: "h1", Label: "prod-web-01", Hostname: "10.0.1.11", Port: 22, Username: "deploy", Protocol: domainvault.ProtocolSSH, Group: "Production", Pinned: true},
		{ID: "h2", Label: "staging-api", Hostname: "staging.example.com", Port: 22, Username: "ubuntu", Protocol: domainvault.ProtocolMosh, Group: "Staging"},
		{ID: "h3", Label: "Local zsh", Protocol: domainvault.ProtocolLocal},
	}}
	groups := &entityGroups{hosts: hosts}
	proxies := &entityProxies{hosts: hosts, groups: groups}
	proxies.items = []domainvault.ProxyProfile{{ID: "p1", Label: "office",
		Config: domainvault.ProxyConfig{Type: domainvault.ProxySOCKS5, Host: "socks.example.com", Port: 1080}}}
	known := &entityKnownHosts{items: []domainconn.KnownHost{{ID: "k1", Hostname: "10.0.1.11", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: "SHA256:Zm9vYmFyYmF6cXV4c2FtcGxlZmFrZQ"}}}
	state := vaultstate.NewState(hosts, vaultstate.WithGroups(groups),
		vaultstate.WithProxyProfiles(proxies), vaultstate.WithKnownHosts(known))
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	service := appsession.NewService(entityFactory{})
	t.Cleanup(service.CloseAll)
	var shell *shellstate.State
	shell = shellstate.NewState(
		shellstate.WithSessions(service, func(*appsession.Session) (shellstate.Surface, error) { return entitySurface{}, nil }),
		shellstate.WithVault(View(state, WithSection(func() section.Section { return shell.VaultSection() }))),
	)
	for _, dark := range []bool{false, true} {
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		view := shellui.View(shell)
		tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(theme); view(c) }, 1100, 720)
		for _, locale := range []string{"en", "zh-CN"} {
			tt.SetLocale(locale)
			for _, page := range []section.Section{section.Hosts, section.Keychain, section.Proxies,
				section.Networks, section.Snippets, section.Notes, section.KnownHosts} {
				shell.ShowVaultSection(page)
				tt.Frame()
				tt.Frame()
				save(t, tt, out, dark, locale, string(page))
			}
		}
		tt.SetLocale("en")
		shell.ShowVaultSection(section.Proxies)
		state.RequestDeleteProxyProfile("p1")
		tt.Frame()
		tt.Frame()
		save(t, tt, out, dark, "en", "proxies-delete")
		state.CancelDeleteProxyProfile()
	}
}

func save(t *testing.T, tt *ui.Tester, out string, dark bool, locale, name string) {
	mode := "light"
	if dark {
		mode = "dark"
	}
	file, err := os.Create(filepath.Join(out, mode+"-"+locale+"-"+name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
