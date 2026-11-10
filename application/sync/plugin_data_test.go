package sync

import (
	"encoding/json"
	"testing"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

func mergeTestProfile(id, label string, data plugindata.Map) domainplugin.DataProfile {
	return domainplugin.DataProfile{
		ID: id, Provider: "plugin.acme.data.db", Label: label,
		Mode: domainplugin.DataModeReadOnly, PluginData: data,
	}
}

func mergeTestNamespace(fields map[string]string) plugindata.Namespace {
	values := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		values[key] = json.RawMessage(value)
	}
	return plugindata.Namespace{SchemaVersion: 1, Fields: values}
}

func mergeTestProfileData(name string) plugindata.Map {
	return plugindata.Map{"acme.data": mergeTestNamespace(map[string]string{"name": name})}
}

func TestMergeDataProfilesFollowTheRecordConflictPolicy(t *testing.T) {
	base := &Payload{DataProfiles: []domainplugin.DataProfile{
		mergeTestProfile("keep", "Keep", mergeTestProfileData(`"keep"`)),
		mergeTestProfile("local-change", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("remote-change", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("local-delete", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("remote-delete-local-edit", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("both-change", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("both-clash", "Base", mergeTestProfileData(`"base"`)),
	}}
	local := &Payload{DataProfiles: []domainplugin.DataProfile{
		mergeTestProfile("keep", "Keep", mergeTestProfileData(`"keep"`)),
		mergeTestProfile("local-change", "Local", mergeTestProfileData(`"base"`)),
		mergeTestProfile("remote-change", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("remote-delete-local-edit", "Local edit", mergeTestProfileData(`"base"`)),
		mergeTestProfile("both-change", "Base", mergeTestProfileData(`"local"`)),
		mergeTestProfile("both-clash", "Local", mergeTestProfileData(`"local"`)),
		mergeTestProfile("local-added", "Added", mergeTestProfileData(`"added"`)),
	}}
	remote := &Payload{DataProfiles: []domainplugin.DataProfile{
		mergeTestProfile("keep", "Keep", mergeTestProfileData(`"keep"`)),
		mergeTestProfile("local-change", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("remote-change", "Remote", mergeTestProfileData(`"base"`)),
		mergeTestProfile("local-delete", "Base", mergeTestProfileData(`"base"`)),
		mergeTestProfile("both-change", "Remote", mergeTestProfileData(`"base"`)),
		mergeTestProfile("both-clash", "Remote", mergeTestProfileData(`"base"`)),
		mergeTestProfile("remote-added", "Added", mergeTestProfileData(`"added"`)),
	}}

	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads: %v", err)
	}
	byID := map[string]domainplugin.DataProfile{}
	for _, profile := range result.Payload.DataProfiles {
		byID[profile.ID] = profile
	}
	if _, ok := byID["local-delete"]; ok {
		t.Fatal("remote deletion of an unchanged profile was not applied")
	}
	if byID["local-change"].Label != "Local" {
		t.Fatalf("local-only edit = %+v", byID["local-change"])
	}
	if byID["remote-change"].Label != "Remote" {
		t.Fatalf("remote-only edit = %+v", byID["remote-change"])
	}
	if byID["remote-delete-local-edit"].Label != "Local edit" {
		t.Fatalf("a remote deletion against a local pluginData edit lost the edit: %+v",
			byID["remote-delete-local-edit"])
	}
	if byID["both-change"].Label != "Remote" ||
		string(byID["both-change"].PluginData["acme.data"].Fields["name"]) != `"local"` {
		t.Fatalf("a pluginData edit did not use the record field merge: %+v",
			byID["both-change"])
	}
	if byID["both-clash"].Label != "Local" {
		t.Fatalf("same-field clash did not keep local: %+v", byID["both-clash"])
	}
	if byID["local-added"].Label != "Added" || byID["remote-added"].Label != "Added" {
		t.Fatalf("one-sided additions were dropped: %+v", byID)
	}
	counts := result.Summary.ByEntity["dataProfiles"]
	if counts.AddedLocal != 1 || counts.AddedRemote != 1 ||
		counts.DeletedLocal != 1 || counts.ModifiedLocal != 3 ||
		counts.ModifiedRemote != 3 || counts.Conflicts != 2 {
		t.Fatalf("byEntity[dataProfiles] = %+v", counts)
	}
	conflictKinds := map[string]ConflictKind{}
	for _, conflict := range result.Conflicts {
		if conflict.EntityType == "dataProfiles" {
			conflictKinds[conflict.EntityID] = conflict.Kind
		}
	}
	if conflictKinds["remote-delete-local-edit"] != ConflictRemoteDeletedLocalModified ||
		conflictKinds["both-clash"] != ConflictBothModified {
		t.Fatalf("conflicts = %+v", result.Conflicts)
	}
}

func TestMergeDataProfileSecretsRideWithTheirProfile(t *testing.T) {
	base := &Payload{
		DataProfiles: []domainplugin.DataProfile{
			mergeTestProfile("keep", "Keep", nil),
			mergeTestProfile("deleted", "Deleted", nil),
		},
		Secrets: []SecretValue{
			{EntityType: "data_profile", EntityID: "keep", Field: "token", Value: "base"},
			{EntityType: "data_profile", EntityID: "deleted", Field: "token", Value: "old"},
		},
	}
	local := &Payload{
		DataProfiles: []domainplugin.DataProfile{
			mergeTestProfile("keep", "Keep", nil),
			mergeTestProfile("local-added", "Added", nil),
		},
		Secrets: []SecretValue{
			{EntityType: "data_profile", EntityID: "keep", Field: "token", Value: "fresh"},
			{EntityType: "data_profile", EntityID: "local-added", Field: "token", Value: "new"},
		},
	}
	remote := &Payload{
		DataProfiles: []domainplugin.DataProfile{
			mergeTestProfile("keep", "Keep", nil),
			mergeTestProfile("deleted", "Deleted", nil),
		},
		Secrets: []SecretValue{
			{EntityType: "data_profile", EntityID: "keep", Field: "token", Value: "base"},
			{EntityType: "data_profile", EntityID: "deleted", Field: "token", Value: "old"},
		},
	}
	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads: %v", err)
	}
	secrets := map[string]string{}
	for _, secret := range result.Payload.Secrets {
		secrets[secret.EntityID+"/"+secret.Field] = secret.Value
	}
	if secrets["keep/token"] != "fresh" {
		t.Fatalf("keep secret = %q, want the local edit", secrets["keep/token"])
	}
	if secrets["local-added/token"] != "new" {
		t.Fatalf("added profile secret = %+v", secrets)
	}
	if _, ok := secrets["deleted/token"]; ok {
		t.Fatalf("secret of a deleted profile survived: %+v", result.Payload.Secrets)
	}
	// The deletion is durable even without a base, so the next merge cannot
	// resurrect the profile or its secret.
	next, err := MergePayloads(nil, result.Payload, *remote, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads without base: %v", err)
	}
	for _, profile := range next.Payload.DataProfiles {
		if profile.ID == "deleted" {
			t.Fatal("a tombstoned profile was resurrected")
		}
	}
	for _, secret := range next.Payload.Secrets {
		if secret.EntityID == "deleted" {
			t.Fatalf("a tombstoned profile's secret was resurrected: %+v", secret)
		}
	}
}

func TestConvergentPayloadRoundTripCarriesDataProfilePluginData(t *testing.T) {
	namespace := mergeTestNamespace(map[string]string{"name": `"a"`})
	namespace.Extra = map[string]json.RawMessage{"future": json.RawMessage(`true`)}
	profile := mergeTestProfile("p1", "Profile", plugindata.Map{"acme.data": namespace})
	profile.SecretRefs = map[string]string{"token": "vault:data-profile:p1:token"}
	payload := Payload{DataProfiles: []domainplugin.DataProfile{profile}}

	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	state, err := domainsync.CreateConvergentStateFromSnapshot(snapshot, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}
	materialized, err := payloadFromState(state, nil, 2000, "device-a")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	if len(materialized.DataProfiles) != 1 {
		t.Fatalf("dataProfiles = %+v", materialized.DataProfiles)
	}
	restored := materialized.DataProfiles[0]
	stored := restored.PluginData["acme.data"]
	if stored.SchemaVersion != 1 ||
		string(stored.Fields["name"]) != `"a"` ||
		string(stored.Extra["future"]) != "true" {
		t.Fatalf("restored pluginData = %+v", stored)
	}
	if restored.SecretRefs["token"] != "vault:data-profile:p1:token" {
		t.Fatalf("restored secretRefs = %+v", restored.SecretRefs)
	}
	envelope, err := domainsync.ParseConvergentEnvelope(materialized.ConvergentSync)
	if err != nil {
		t.Fatalf("ParseConvergentEnvelope: %v", err)
	}
	if _, ok := envelope.State.Collections["dataProfiles"].Entities["p1"]; !ok {
		t.Fatal("the v2 state has no dataProfiles entity")
	}
}
