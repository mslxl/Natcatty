package sync

import (
	"encoding/json"
	"testing"
)

func dataProfileSnapshot(t *testing.T, name string) ConvergentSnapshot {
	t.Helper()
	raw := mustJSONValue(t, []any{map[string]any{
		"id":       "p1",
		"provider": "plugin.acme.data.db",
		"label":    "Profile",
		"mode":     "read_only",
		"pluginData": map[string]any{
			"acme.data": map[string]any{
				"schemaVersion": 1,
				"fields":        map[string]any{"name": name},
				"future":        map[string]any{"keep": true},
			},
		},
	}})
	snapshot, err := ParseConvergentSnapshot(
		map[string]json.RawMessage{"dataProfiles": raw}, DefaultConvergentFamilies())
	if err != nil {
		t.Fatalf("ParseConvergentSnapshot: %v", err)
	}
	return snapshot
}

func dataProfilePluginData(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	pluginData, ok := value["pluginData"].(map[string]any)
	if !ok {
		t.Fatalf("pluginData = %+v", value["pluginData"])
	}
	namespace, ok := pluginData["acme.data"].(map[string]any)
	if !ok {
		t.Fatalf("namespace = %+v", pluginData)
	}
	return namespace
}

func TestDefaultConvergentFamiliesCarryDataProfilePluginData(t *testing.T) {
	hasFamily := false
	for _, family := range DefaultConvergentFamilies() {
		if family.Name == "dataProfiles" {
			hasFamily = len(family.KeyFields) == 1 && family.KeyFields[0] == "id"
		}
	}
	if !hasFamily {
		t.Fatal("dataProfiles is not a keyed convergent family")
	}

	state := mustCreateState(t, dataProfileSnapshot(t, "a"), "device-a", 1000)
	value := materializedValue(t, mustMaterialize(t, state), "dataProfiles", "p1")
	namespace := dataProfilePluginData(t, value)
	fields, _ := namespace["fields"].(map[string]any)
	if fields["name"] != "a" {
		t.Fatalf("fields = %+v", namespace["fields"])
	}
	future, _ := namespace["future"].(map[string]any)
	if future["keep"] != true {
		t.Fatalf("unknown namespace property was lost: %+v", namespace)
	}

	// Two devices editing the same profile concurrently keep the pluginData
	// register whole; the deterministic winner is one complete value.
	local, _ := mustApply(t, state, dataProfileSnapshot(t, "local"), "device-b", 2000, false)
	remote, _ := mustApply(t, state, dataProfileSnapshot(t, "remote"), "device-c", 2001, false)
	merged := mustMerge(t, local, remote)
	namespace = dataProfilePluginData(t, materializedValue(t,
		mustMaterialize(t, merged), "dataProfiles", "p1"))
	fields, _ = namespace["fields"].(map[string]any)
	if fields["name"] != "local" && fields["name"] != "remote" {
		t.Fatalf("merged fields = %+v, want one complete concurrent value", fields)
	}
	future, _ = namespace["future"].(map[string]any)
	if future["keep"] != true {
		t.Fatalf("merged pluginData lost an unknown property: %+v", namespace)
	}
	// The merged state materializes to the same bytes in either delivery order.
	materializedLeft, err := MaterializeConvergentSnapshot(merged)
	if err != nil {
		t.Fatalf("materialize merged: %v", err)
	}
	materializedRight, err := MaterializeConvergentSnapshot(mustMerge(t, remote, local))
	if err != nil {
		t.Fatalf("materialize swapped: %v", err)
	}
	leftBytes, _ := json.Marshal(materializedLeft.Families["dataProfiles"])
	rightBytes, _ := json.Marshal(materializedRight.Families["dataProfiles"])
	if string(leftBytes) != string(rightBytes) {
		t.Fatalf("materialization depends on delivery order:\n%s\n%s", leftBytes, rightBytes)
	}
}
