//go:build uitour

package vault

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// TestRenderHostsUITour renders the Hosts page in each view mode, at a wide
// and a narrow content width, in both themes, from fakes.
func TestRenderHostsUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	now := time.Now().UnixMilli()
	host := func(id, label, group string) domainvault.Host {
		h := layoutHost(id, label)
		h.Group = group
		return h
	}
	pinned := host("p1", "bastion", "")
	pinned.Pinned = true
	recent := host("r1", "prod-web-01", "Production")
	recent.LastConnectedAt = now - 60_000
	recent.Tags = []string{"web", "nginx"}
	mosh := host("m1", "staging-api", "Staging")
	mosh.Protocol = domainvault.ProtocolMosh
	local := domainvault.Host{ID: "l1", Label: "Local zsh", Protocol: domainvault.ProtocolLocal}
	items := []domainvault.Host{pinned, recent, mosh, local,
		host("d1", "prod-db-primary-with-a-very-long-name", "Production/Databases"),
		host("d2", "prod-db-replica", "Production/Databases"),
		host("w2", "prod-web-02", "Production"),
		host("x1", "home-nas", "")}
	hosts := &entityHosts{items: items}
	groups := &entityGroups{hosts: hosts, items: []domainvault.Group{
		{Path: "Production"}, {Path: "Production/Databases"}, {Path: "Staging"}, {Path: "Empty group"}}}
	hosts.groups = groups
	state := vaultstate.NewState(hosts, vaultstate.WithGroups(groups))
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	m := locales.In("en")
	for _, dark := range []bool{false, true} {
		preset := domaintheme.ResolvePreset(dark, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
		theme := uitheme.UI(dark, preset, uitheme.Options{})
		mode := map[bool]string{false: "light", true: "dark"}[dark]
		for _, width := range []int{900, 540} {
			for _, view := range []domainpreferences.HostViewMode{domainpreferences.HostViewGrid,
				domainpreferences.HostViewList, domainpreferences.HostViewTree} {
				layout := domainpreferences.Defaults().Layout
				layout.HostViewMode = view
				v := View(state, WithLayout(func() domainpreferences.Layout { return layout },
					func(change func(*domainpreferences.Layout)) { change(&layout) }),
					WithOpenSFTP(func(domainvault.Host) {}, func() bool { return true }),
					WithOpenBrowser(func(domainvault.Host) {}), WithClipboard(func(string) {}))
				tt := ui.NewTester(func(c *ui.Context) {
					c.SetTheme(theme)
					ui.Column(c).Fill().Background(c.Theme().Background).Padding(12).Children(func() { v(c) })
				}, width, 820)
				for _, locale := range []string{"en", "zh-CN"} {
					if locale == "zh-CN" && (width != 900 || view != domainpreferences.HostViewGrid) {
						continue
					}
					tt.SetLocale(locale)
					tt.Frame()
					tt.Frame()
					shot(t, tt, out, "hosts-"+mode+"-"+locale+"-"+string(view)+"-"+itoa(width))
				}
				tt.SetLocale("en")
				if view == domainpreferences.HostViewGrid && width == 900 {
					if err := tt.Click(m.VaultItemActions("prod-web-01")); err == nil {
						tt.Frame()
						shot(t, tt, out, "hosts-"+mode+"-menu")
						tt.KeyDown(ui.KeyEscape)
						tt.Frame()
					} else {
						t.Log(err)
					}
					if err := tt.Click("Production"); err == nil {
						tt.Frame()
						tt.Frame()
						shot(t, tt, out, "hosts-"+mode+"-group")
					} else {
						t.Log(err)
					}
				}
			}
		}
	}
}

// TestRenderHostGroupsUITour renders Host Groups in the states the main
// tour skips: browsed in the list view, a nested group, a collapsed tree, and
// a root that shows only ungrouped Hosts.
func TestRenderHostGroupsUITour(t *testing.T) {
	out := os.Getenv("NATCATTY_SCREENSHOT_DIR")
	if out == "" {
		t.Skip("set NATCATTY_SCREENSHOT_DIR")
	}
	host := func(id, label, group string) domainvault.Host {
		h := layoutHost(id, label)
		h.Group = group
		return h
	}
	hosts := &entityHosts{items: []domainvault.Host{host("w1", "prod-web-01", "Production"),
		host("d1", "prod-db", "Production/Databases"), host("s1", "staging-api", "Staging"),
		host("x1", "home-nas", "")}}
	groups := &entityGroups{hosts: hosts, items: []domainvault.Group{
		{Path: "Production"}, {Path: "Production/Databases"}, {Path: "Staging"}}}
	hosts.groups = groups
	state := vaultstate.NewState(hosts, vaultstate.WithGroups(groups))
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	m := locales.In("en")
	preset := domaintheme.ResolvePreset(false, domaintheme.DefaultLightTheme, domaintheme.DefaultDarkTheme)
	theme := uitheme.UI(false, preset, uitheme.Options{})
	render := func(layout *domainpreferences.Layout) *ui.Tester {
		v := View(state, WithLayout(func() domainpreferences.Layout { return *layout },
			func(change func(*domainpreferences.Layout)) { change(layout) }))
		tt := ui.NewTester(func(c *ui.Context) {
			c.SetTheme(theme)
			ui.Column(c).Fill().Background(c.Theme().Background).Padding(12).Children(func() { v(c) })
		}, 900, 520)
		tt.SetLocale("en")
		tt.Frame()
		tt.Frame()
		return tt
	}
	click := func(tt *ui.Tester, text string) {
		t.Helper()
		if err := tt.Click(text); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		tt.Frame()
	}

	list := domainpreferences.Defaults().Layout
	list.HostViewMode = domainpreferences.HostViewList
	tt := render(&list)
	click(tt, "Production")
	shot(t, tt, out, "groups-list-browsed")
	click(tt, "Databases")
	shot(t, tt, out, "groups-list-nested")
	// Switching views inside a group keeps the place in the tree.
	click(tt, m.VaultViewGrid())
	shot(t, tt, out, "groups-grid-nested")

	tree := domainpreferences.Defaults().Layout
	tree.HostViewMode = domainpreferences.HostViewTree
	tt = render(&tree)
	click(tt, "Production")
	shot(t, tt, out, "groups-tree-collapsed")

	ungrouped := domainpreferences.Defaults().Layout
	ungrouped.ShowOnlyUngroupedHostsInRoot = true
	tt = render(&ungrouped)
	shot(t, tt, out, "groups-root-ungrouped-only")
}

func itoa(n int) string {
	return string(rune('0'+n/100%10)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}

func shot(t *testing.T, tt *ui.Tester, out, name string) {
	file, err := os.Create(filepath.Join(out, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
