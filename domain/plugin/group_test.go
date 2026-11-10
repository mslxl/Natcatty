package plugin

import (
	"strings"
	"testing"
)

func serviceTestManifest(id string) Manifest {
	return Manifest{
		ManifestVersion: 1,
		ID:              id,
		Name:            "Services",
		Version:         "1.0.0",
		PluginAPI:       "^0.1",
		ABI:             1,
		DefaultLocale:   "en",
		Wasm: Wasm{
			Path:         "plugin.wasm",
			SHA256:       strings.Repeat("a", 64),
			MemoryMaxMiB: 64,
		},
	}
}

func TestFiberIDCarriesGroupPluginAndInstance(t *testing.T) {
	id := NewFiberID("team.alpha", "acme.hello", 7)
	if id.Group() != "team.alpha" {
		t.Fatalf("Group = %q", id.Group())
	}
	if id.PluginID() != "acme.hello" {
		t.Fatalf("PluginID = %q", id.PluginID())
	}
	if id.Instance() != 7 {
		t.Fatalf("Instance = %d", id.Instance())
	}
	if got := string(id); got != "team.alpha/acme.hello#7" {
		t.Fatalf("identity = %q", got)
	}
	for _, malformed := range []FiberID{"acme.hello", "team/acme.hello", "team/acme.hello#x"} {
		if malformed.Instance() != 0 {
			t.Fatalf("%q has instance %d, want 0", malformed, malformed.Instance())
		}
	}
}

func TestGroupValidationAndDefaults(t *testing.T) {
	group := Group{
		Name:   "team.alpha",
		Parent: "team",
		Defaults: map[string]map[string]any{
			"acme.hello": {"mode": "slow"},
		},
	}
	if err := group.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	defaults := group.DefaultsFor("acme.hello")
	if defaults["mode"] != "slow" {
		t.Fatalf("defaults = %+v", defaults)
	}
	defaults["mode"] = "mutated"
	if group.DefaultsFor("acme.hello")["mode"] != "slow" {
		t.Fatal("DefaultsFor returned the group's own map")
	}

	for _, invalid := range []Group{
		{Name: "bad/name"},
		{Name: "team", Parent: "team"},
		{Name: "team", Parent: "bad parent"},
		{Name: "team", Defaults: map[string]map[string]any{"invalid": {"x": 1}}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("Validate accepted %+v", invalid)
		}
	}
}

func TestManifestValidateChecksInjectAndProvidesNamespaces(t *testing.T) {
	manifest := serviceTestManifest("acme.services")
	manifest.Inject = []Injection{{Service: "plugin.acme.other.search"}}
	manifest.Provides = []Provision{{Service: "plugin.acme.services.search"}}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(manifest.RequiredInjections()) != 1 {
		t.Fatalf("required injections = %v", manifest.RequiredInjections())
	}
	manifest.Inject[0].Optional = true
	if len(manifest.RequiredInjections()) != 0 {
		t.Fatalf("optional injection counted as required: %v", manifest.RequiredInjections())
	}

	foreign := serviceTestManifest("acme.services")
	foreign.Provides = []Provision{{Service: "plugin.acme.other.search"}}
	if err := foreign.Validate(); err == nil {
		t.Fatal("Validate accepted a provision outside the plugin namespace")
	}

	duplicate := serviceTestManifest("acme.services")
	duplicate.Inject = []Injection{
		{Service: "plugin.acme.other.search"},
		{Service: "plugin.acme.other.search", Optional: true},
	}
	if err := duplicate.Validate(); err == nil {
		t.Fatal("Validate accepted a duplicate injection")
	}
}

func TestInjectionReasonRoundTrips(t *testing.T) {
	reason := InjectionReason([]string{"plugin.b.b", "plugin.a.a"})
	if reason != ReasonInjectionMissing+": plugin.a.a,plugin.b.b" {
		t.Fatalf("reason = %q", reason)
	}
	services, ok := ParseInjectionReason(reason)
	if !ok || len(services) != 2 || services[0] != "plugin.a.a" || services[1] != "plugin.b.b" {
		t.Fatalf("ParseInjectionReason = %v, %v", services, ok)
	}
	if _, ok := ParseInjectionReason("config-invalid: x"); ok {
		t.Fatal("ParseInjectionReason accepted a configuration reason")
	}
	if err := ValidateServiceName("plugin.acme.search"); err != nil {
		t.Fatalf("ValidateServiceName: %v", err)
	}
	for _, name := range []string{"", "acme.search", "plugin.acme", "plugin.acme.", "Plugin.acme.x"} {
		if err := ValidateServiceName(name); err == nil {
			t.Fatalf("ValidateServiceName accepted %q", name)
		}
	}
}

func TestParsePermissionReasonDoesNotAcceptInjectionReasons(t *testing.T) {
	_, _, ok := ParsePermissionReason(
		InjectionReason([]string{"plugin.a.a"}))
	if ok {
		t.Fatal("ParsePermissionReason accepted an injection reason")
	}
	code, permission, ok := ParsePermissionReason(
		PermissionReason(ReasonPermissionDenied, PermissionCommands))
	if !ok || code != ReasonPermissionDenied ||
		permission != PermissionCommands {
		t.Fatalf("ParsePermissionReason = %q %q %v", code, permission, ok)
	}
	if !strings.Contains(ConfigReason("mode: bad"), ReasonConfigInvalid) {
		t.Fatal("ConfigReason does not carry the stable code")
	}
}
