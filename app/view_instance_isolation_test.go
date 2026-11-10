package app

import (
	"testing"

	appview "github.com/mslxl/natcatty/application/view"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// TestOtherPluginInstanceLiveKeepsOneUISessionPerFiber proves the per-instance
// isolation hook: a plugin's UISession may reset only after its last live View
// Instance closes, while another plugin's instance never keeps it alive.
func TestOtherPluginInstanceLiveKeepsOneUISessionPerFiber(t *testing.T) {
	registry := appview.NewRegistry()
	t.Cleanup(registry.Close)
	register := func(pluginID string, policy domainplugin.InstancePolicy) {
		t.Helper()
		if _, err := registry.Register(appview.Owner{PluginID: pluginID}, domainplugin.ViewContribution{
			ID:         "view",
			Placements: []domainplugin.Placement{domainplugin.PlacementPane},
			Instances:  policy,
		}); err != nil {
			t.Fatalf("Register %s: %v", pluginID, err)
		}
	}
	open := func(pluginID string) *appview.Instance {
		t.Helper()
		instance, _, err := registry.Open(appview.OpenRequest{
			ViewID: domainplugin.QualifiedViewID(pluginID, "view"),
		})
		if err != nil {
			t.Fatalf("Open %s: %v", pluginID, err)
		}
		return instance
	}

	register("acme.one", domainplugin.InstanceMulti)
	register("acme.two", domainplugin.InstanceSingleton)
	first := open("acme.one")
	second := open("acme.one")
	otherPlugin := open("acme.two")

	if !anotherInstanceOfSamePluginLive(registry, first) {
		t.Fatal("closing one multi-instance View would reset a session another instance needs")
	}
	if anotherInstanceOfSamePluginLive(registry, otherPlugin) {
		t.Fatal("another plugin's instance kept this plugin's session alive")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if anotherInstanceOfSamePluginLive(registry, first) {
		t.Fatal("the last live instance reported another instance remaining")
	}
}
