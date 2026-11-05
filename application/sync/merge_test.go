package sync

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func mergeTestHost(id, label, hostname string) domainvault.Host {
	return domainvault.Host{
		ID:        model.HostID(id),
		Label:     label,
		Hostname:  hostname,
		Protocol:  domainvault.ProtocolSSH,
		Port:      22,
		UpdatedAt: time.UnixMilli(1000),
	}
}

func mergeTestGroup(path, notes string) domainvault.Group {
	return domainvault.Group{Path: path, Notes: notes, UpdatedAt: time.UnixMilli(1000)}
}

func TestMergeAddAddUpdateUpdateDeleteUpdateDeleteDeleteAndUnchanged(t *testing.T) {
	base := &Payload{
		Hosts: []domainvault.Host{
			mergeTestHost("keep", "Keep", "keep.example"),
			mergeTestHost("local-change", "Base", "local.example"),
			mergeTestHost("remote-change", "Base", "remote.example"),
			mergeTestHost("local-delete", "Base", "gone.example"),
			mergeTestHost("remote-delete", "Base", "gone.example"),
			mergeTestHost("local-delete-remote-edit", "Base", "gone.example"),
			mergeTestHost("remote-delete-local-edit", "Base", "gone.example"),
			mergeTestHost("both-delete", "Base", "gone.example"),
			mergeTestHost("both-change", "Base", "both.example"),
		},
	}
	local := &Payload{
		Hosts: []domainvault.Host{
			mergeTestHost("keep", "Keep", "keep.example"),
			mergeTestHost("local-change", "Local", "local.example"),
			mergeTestHost("remote-change", "Base", "remote.example"),
			mergeTestHost("remote-delete", "Base", "gone.example"),
			mergeTestHost("remote-delete-local-edit", "Local edit", "gone.example"),
			mergeTestHost("both-change", "Local", "both.example"),
			mergeTestHost("local-added", "Added locally", "new.example"),
			mergeTestHost("both-added", "Local added", "same.example"),
		},
	}
	remote := &Payload{
		Hosts: []domainvault.Host{
			mergeTestHost("keep", "Keep", "keep.example"),
			mergeTestHost("local-change", "Base", "local.example"),
			mergeTestHost("remote-change", "Remote", "remote.example"),
			mergeTestHost("local-delete", "Base", "gone.example"),
			mergeTestHost("local-delete-remote-edit", "Remote edit", "gone.example"),
			mergeTestHost("both-change", "Base", "remote.example"),
			mergeTestHost("remote-added", "Added remotely", "new.example"),
			mergeTestHost("both-added", "Remote added", "same.example"),
		},
	}

	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]domainvault.Host{}
	for _, host := range result.Payload.Hosts {
		byID[string(host.ID)] = host
	}
	if _, ok := byID["local-delete"]; ok {
		t.Error("local deletion with an unchanged remote should delete")
	}
	if _, ok := byID["remote-delete"]; ok {
		t.Error("a remote deletion with an unchanged local copy should delete")
	}
	if _, ok := byID["remote-delete-local-edit"]; !ok || byID["remote-delete-local-edit"].Label != "Local edit" {
		t.Error("a remote deletion against a local edit must keep the local edit")
	}
	if _, ok := byID["local-delete-remote-edit"]; !ok || byID["local-delete-remote-edit"].Label != "Remote edit" {
		t.Error("a local deletion against a remote edit must keep the remote edit")
	}
	if _, ok := byID["both-delete"]; ok {
		t.Error("both sides deleting an entity must delete it")
	}
	if byID["local-change"].Label != "Local" {
		t.Error("a local-only modification must win")
	}
	if byID["remote-change"].Label != "Remote" {
		t.Error("a remote-only modification must win")
	}
	if byID["keep"].Label != "Keep" {
		t.Error("an unchanged entity must stay")
	}
	if byID["local-added"].Label != "Added locally" || byID["remote-added"].Label != "Added remotely" {
		t.Error("one-sided additions must both survive")
	}
	if byID["both-added"].Label != "Local added" {
		t.Error("a both-added ID prefers local")
	}
	// Both changed different fields: the field merge keeps both edits.
	if byID["both-change"].Label != "Local" || byID["both-change"].Hostname != "remote.example" {
		t.Errorf("field merge lost an edit: %+v", byID["both-change"])
	}
	if result.Summary.AddedLocal != 1 || result.Summary.AddedRemote != 1 {
		t.Errorf("added counts = %+v", result.Summary)
	}
	if result.Summary.DeletedLocal != 1 || result.Summary.DeletedRemote != 1 {
		t.Errorf("deleted counts = %+v", result.Summary)
	}
	if result.Summary.ModifiedLocal != 2 || result.Summary.ModifiedRemote != 2 {
		t.Errorf("modified counts = %+v", result.Summary)
	}
	if len(result.Conflicts) != 3 {
		t.Fatalf("conflicts = %+v", result.Conflicts)
	}
	// The two delete/update clashes and both-added are the expected
	// conflicts; both-change field-merges cleanly.
	kinds := map[ConflictKind]bool{}
	for _, item := range result.Conflicts {
		kinds[item.Kind] = true
	}
	if !kinds[ConflictRemoteDeletedLocalModified] || !kinds[ConflictLocalDeletedRemoteModified] ||
		!kinds[ConflictBothAdded] {
		t.Errorf("conflict kinds = %+v", result.Conflicts)
	}
}

func TestMergeDeleteRemoteAndLocalUpdateKeepsTheModification(t *testing.T) {
	base := &Payload{Hosts: []domainvault.Host{mergeTestHost("h", "Base", "old.example")}}
	local := &Payload{Hosts: []domainvault.Host{mergeTestHost("h", "Local edit", "new.example")}}
	remote := &Payload{}

	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Hosts) != 1 || result.Payload.Hosts[0].Label != "Local edit" {
		t.Fatalf("a remote deletion must not discard a local edit: %+v", result.Payload.Hosts)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Kind != ConflictRemoteDeletedLocalModified {
		t.Fatalf("conflicts = %+v", result.Conflicts)
	}
}

func TestMergePreserveDeletionsNeverPropagatesOneSidedDeletion(t *testing.T) {
	base := &Payload{Hosts: []domainvault.Host{
		mergeTestHost("a", "A", "a.example"),
		mergeTestHost("b", "B", "b.example"),
		mergeTestHost("c", "C", "c.example"),
	}}
	local := &Payload{Hosts: []domainvault.Host{
		mergeTestHost("a", "A", "a.example"),
	}}
	remote := &Payload{Hosts: []domainvault.Host{
		mergeTestHost("a", "A", "a.example"),
		mergeTestHost("b", "B", "b.example"),
	}}

	result, err := MergePayloads(base, *local, *remote, MergeOptions{PreserveDeletions: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Hosts) != 2 {
		t.Fatalf("preserve merge kept %d hosts, want 2", len(result.Payload.Hosts))
	}
	if result.Summary.DeletedLocal != 1 || result.Summary.DeletedRemote != 0 {
		t.Fatalf("preserve merge should record deletions without applying them: %+v", result.Summary)
	}

	// The default strategy propagates the same local deletion.
	result, err = MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, host := range result.Payload.Hosts {
		ids[string(host.ID)] = true
	}
	if ids["b"] || ids["c"] {
		t.Fatalf("smartMerge should delete one-sided deletions: %+v", ids)
	}
}

func TestMergeEmptyBaseIsASetUnionPreferringLocal(t *testing.T) {
	local := &Payload{
		Hosts:  []domainvault.Host{mergeTestHost("shared", "Local", "shared.example")},
		Groups: []domainvault.Group{mergeTestGroup("team", "local notes")},
		Notes:  []domainvault.Note{{ID: "note-local", Title: "Local"}},
	}
	remote := &Payload{
		Hosts:  []domainvault.Host{mergeTestHost("shared", "Remote", "shared.example")},
		Groups: []domainvault.Group{mergeTestGroup("team", "remote notes"), mergeTestGroup("remote", "")},
		Notes:  []domainvault.Note{{ID: "note-remote", Title: "Remote"}},
	}
	result, err := MergePayloads(nil, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Hosts) != 1 || result.Payload.Hosts[0].Label != "Local" {
		t.Fatalf("empty base must prefer local on duplicate IDs: %+v", result.Payload.Hosts)
	}
	if len(result.Payload.Groups) != 2 || result.Payload.Groups[0].Path != "team" {
		t.Fatalf("groups = %+v", result.Payload.Groups)
	}
	if len(result.Payload.Notes) != 2 {
		t.Fatalf("notes = %+v", result.Payload.Notes)
	}
	if result.Summary.AddedLocal != 1 || result.Summary.AddedRemote != 2 {
		t.Fatalf("summary = %+v", result.Summary)
	}
}

func TestMergeBothModifiedSameFieldPrefersLocalAndReportsConflict(t *testing.T) {
	base := &Payload{Hosts: []domainvault.Host{mergeTestHost("h", "Base", "base.example")}}
	local := &Payload{Hosts: []domainvault.Host{mergeTestHost("h", "Local", "base.example")}}
	remote := &Payload{Hosts: []domainvault.Host{mergeTestHost("h", "Remote", "base.example")}}

	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.Hosts[0].Label != "Local" {
		t.Fatalf("same-field clash must prefer local, got %q", result.Payload.Hosts[0].Label)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("conflicts = %+v", result.Conflicts)
	}
	item := result.Conflicts[0]
	if item.EntityType != "hosts" || item.EntityID != "h" || item.Kind != ConflictBothModified {
		t.Fatalf("conflict = %+v", item)
	}
}

// TestMergeFieldDeletedLocallyStaysFromRemote documents a deliberate parity
// choice: like the original mergeRecordFields, the field merge starts from the
// remote record and walks only the local object's keys, so deleting one field
// on this device while the other side edits a different field resurrects the
// remote field. Entity deletion is handled separately and is not affected.
func TestMergeFieldDeletedLocallyStaysFromRemote(t *testing.T) {
	baseHost := mergeTestHost("h", "Base", "base.example")
	baseHost.Notes = "note"
	localHost := mergeTestHost("h", "Base", "base.example") // Notes deliberately absent
	remoteHost := mergeTestHost("h", "Base", "remote.example")
	remoteHost.Notes = "note"
	base := &Payload{Hosts: []domainvault.Host{baseHost}}
	local := &Payload{Hosts: []domainvault.Host{localHost}}
	remote := &Payload{Hosts: []domainvault.Host{remoteHost}}

	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Hosts) != 1 {
		t.Fatalf("hosts = %+v", result.Payload.Hosts)
	}
	merged := result.Payload.Hosts[0]
	if merged.Hostname != "remote.example" || merged.Notes != "note" {
		t.Fatalf("field merge = %+v", merged)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("a missing local key is not a conflict: %+v", result.Conflicts)
	}
}

func TestMergeIsDeterministicAndIgnoresMapFieldOrder(t *testing.T) {
	base := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("b", "Base", "b.example"), mergeTestHost("a", "A", "a.example")},
		Settings: map[string]json.RawMessage{
			"nested": json.RawMessage(`{"z":1,"a":{"y":2,"x":3}}`),
		},
	}
	local := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("a", "A2", "a.example")},
		Settings: map[string]json.RawMessage{
			"nested": json.RawMessage(`{"z":9,"a":{"x":3,"y":2}}`),
		},
	}
	remote := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("a", "A", "a.example"), mergeTestHost("c", "C", "c.example")},
		Settings: map[string]json.RawMessage{
			"nested": json.RawMessage(`{"a":{"x":3,"y":2},"z":1}`),
		},
	}
	first, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, _ := json.Marshal(first.Payload)
	secondJSON, _ := json.Marshal(second.Payload)
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("merge is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	// The reordered nested object must not count as a local change on key "a".
	if len(first.Conflicts) != 0 {
		t.Fatalf("key order caused a false conflict: %+v", first.Conflicts)
	}
}

func TestMergeSettingsDeepMergesNestedMapsAndPreservesUnknownKeys(t *testing.T) {
	base := settingsFromJSON(t, `{
		"theme": {"mode":"light","accent":"blue","nested":{"a":1,"b":2}},
		"columns": 3,
		"unknownBase": {"kept": true}
	}`)
	local := settingsFromJSON(t, `{
		"theme": {"mode":"dark","accent":"blue","nested":{"a":1,"b":2}},
		"columns": 4,
		"unknownLocal": "local"
	}`)
	remote := settingsFromJSON(t, `{
		"theme": {"mode":"light","accent":"red","nested":{"a":5,"b":2}},
		"columns": 3,
		"unknownRemote": {"r": 1}
	}`)

	payload, conflict, err := mergeSettingsPayload(base, local, remote, false)
	if err != nil {
		t.Fatal(err)
	}
	decoded := settingsToStringMap(t, payload)
	theme := decoded["theme"].(map[string]any)
	if theme["mode"] != "dark" || theme["accent"] != "red" {
		t.Fatalf("deep merge did not keep both sides: %v", theme)
	}
	nested := theme["nested"].(map[string]any)
	if nested["a"] != json.Number("5") || nested["b"] != json.Number("2") {
		t.Fatalf("nested merge = %v", nested)
	}
	if decoded["columns"] != json.Number("4") {
		t.Fatalf("columns = %v", decoded["columns"])
	}
	// Unknown keys that only one side carries survive. A key deleted on both
	// sides is a real deletion, matching the original.
	for _, key := range []string{"unknownLocal", "unknownRemote"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("unknown key %q was dropped", key)
		}
	}
	if _, ok := decoded["unknownBase"]; ok {
		t.Error("a key absent from both sides must be deleted")
	}
	if !conflict {
		t.Error("both sides changed the settings, so a conflict should be reported")
	}
}

func TestMergeSettingsBothSidesDeletingAKeyIsNotAConflict(t *testing.T) {
	base := settingsFromJSON(t, `{"removed":"base","kept":1,"nested":{"gone":true,"stay":2}}`)
	local := settingsFromJSON(t, `{"kept":1,"nested":{"stay":2}}`)
	remote := settingsFromJSON(t, `{"kept":1,"nested":{"stay":2}}`)
	payload, conflict, err := mergeSettingsPayload(base, local, remote, false)
	if err != nil {
		t.Fatal(err)
	}
	if conflict {
		t.Fatal("both sides deleting the same key is agreement, not a conflict")
	}
	decoded := settingsToStringMap(t, payload)
	if _, ok := decoded["removed"]; ok {
		t.Fatalf("a key deleted on both sides survived: %v", decoded)
	}
	nested := decoded["nested"].(map[string]any)
	if _, ok := nested["gone"]; ok || nested["stay"] != json.Number("2") {
		t.Fatalf("nested deletion = %v", nested)
	}
}

func TestMergeSettingsFirstMergePrefersCloudOnSameField(t *testing.T) {
	local := settingsFromJSON(t, `{"theme":"local","onlyLocal":true,"nested":{"a":1}}`)
	remote := settingsFromJSON(t, `{"theme":"cloud","onlyRemote":true,"nested":{"a":2}}`)
	payload, _, err := mergeSettingsPayload(nil, local, remote, true)
	if err != nil {
		t.Fatal(err)
	}
	decoded := settingsToStringMap(t, payload)
	if decoded["theme"] != "cloud" {
		t.Fatalf("first merge should prefer the cloud on a same-field clash: %v", decoded["theme"])
	}
	if decoded["onlyLocal"] != true || decoded["onlyRemote"] != true {
		t.Fatalf("one-sided keys must be preserved: %v", decoded)
	}
}

func TestMergeSettingsHonorsExplicitEmptyObjectReset(t *testing.T) {
	local := settingsFromJSON(t, `{"theme":{"mode":"light"}}`)
	remote := settingsFromJSON(t, `{"theme":{}}`)
	payload, _, err := mergeSettingsPayload(nil, local, remote, true)
	if err != nil {
		t.Fatal(err)
	}
	decoded := settingsToStringMap(t, payload)
	theme, ok := decoded["theme"].(map[string]any)
	if !ok || len(theme) != 0 {
		t.Fatalf("an explicit empty remote object is a reset marker: %v", decoded["theme"])
	}
}

func TestMergeSettingsMergesIDArrays(t *testing.T) {
	base := settingsFromJSON(t, `{"themes":[{"id":"a","color":"red"},{"id":"b","color":"blue"}]}`)
	local := settingsFromJSON(t, `{"themes":[{"id":"a","color":"green"},{"id":"b","color":"blue"}]}`)
	remote := settingsFromJSON(t, `{"themes":[{"id":"a","color":"red"},{"id":"b","color":"orange"},{"id":"c","color":"black"}]}`)
	payload, _, err := mergeSettingsPayload(base, local, remote, false)
	if err != nil {
		t.Fatal(err)
	}
	themes := settingsToStringMap(t, payload)["themes"].([]any)
	byID := map[string]map[string]any{}
	for _, value := range themes {
		object := value.(map[string]any)
		byID[object["id"].(string)] = object
	}
	if len(byID) != 3 {
		t.Fatalf("themes = %v", byID)
	}
	if byID["a"]["color"] != "green" || byID["b"]["color"] != "orange" || byID["c"]["color"] != "black" {
		t.Fatalf("id array merge = %v", byID)
	}
}

func TestMergeSettingsFixesLegacyAutoClose(t *testing.T) {
	base := settingsFromJSON(t, `{}`)
	local := settingsFromJSON(t, `{"terminalSettings":{"autoCloseOnExit":true}}`)
	remote := settingsFromJSON(t, `{"terminalSettings":{"autoCloseOnExit":true}}`)
	payload, _, err := mergeSettingsPayload(base, local, remote, false)
	if err != nil {
		t.Fatal(err)
	}
	terminal := settingsToStringMap(t, payload)["terminalSettings"].(map[string]any)
	if terminal["autoCloseOnExit"] != false {
		t.Fatalf("legacy autoCloseOnExit must merge to false: %v", terminal)
	}
}

func TestMergeSecretsFollowsTheEntityAndIsScrubbedAfterDeletion(t *testing.T) {
	base := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("h", "H", "h.example")},
		Secrets: []SecretValue{
			{EntityType: "host", EntityID: "h", Field: "password", Value: "base"},
			{EntityType: "host", EntityID: "gone", Field: "password", Value: "base"},
		},
	}
	local := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("h", "H", "h.example")},
		Secrets: []SecretValue{
			{EntityType: "host", EntityID: "h", Field: "password", Value: "local"},
			{EntityType: "host", EntityID: "gone", Field: "password", Value: "base"},
		},
	}
	remote := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("h", "H", "h.example")},
		Secrets: []SecretValue{
			{EntityType: "host", EntityID: "h", Field: "password", Value: "base"},
		},
	}
	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Secrets) != 1 {
		t.Fatalf("secrets = %+v", result.Payload.Secrets)
	}
	if result.Payload.Secrets[0].Value != "local" {
		t.Fatalf("secret merge = %+v", result.Payload.Secrets[0])
	}
}

func TestMergeTombstonesPreventResurrectionWithoutBase(t *testing.T) {
	local := &Payload{
		Hosts: []domainvault.Host{mergeTestHost("deleted-elsewhere", "Stale", "stale.example")},
	}
	remote := &Payload{
		SyncMeta: &SyncMeta{SchemaVersion: 1, Deletions: []SyncDeletion{
			{EntityType: "hosts", ID: "deleted-elsewhere", DeletedAt: 100},
		}},
	}
	result, err := MergePayloads(nil, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Hosts) != 0 {
		t.Fatalf("a remote tombstone must not be resurrected: %+v", result.Payload.Hosts)
	}
	if result.Summary.DeletedRemote != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	// Preserve mode treats absence as ambiguous and keeps the entity.
	result, err = MergePayloads(nil, *local, *remote, MergeOptions{PreserveDeletions: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Payload.Hosts) != 1 {
		t.Fatalf("preserve mode should keep the entity: %+v", result.Payload.Hosts)
	}
}

func TestMergeRecordsNewTombstonesForDeletedEntities(t *testing.T) {
	base := &Payload{Hosts: []domainvault.Host{
		mergeTestHost("keep", "Keep", "keep.example"),
		mergeTestHost("deleted", "Delete", "delete.example"),
	}}
	local := &Payload{Hosts: []domainvault.Host{mergeTestHost("keep", "Keep", "keep.example")}}
	remote := *base
	result, err := MergePayloads(base, *local, remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.SyncMeta == nil || len(result.Payload.SyncMeta.Deletions) != 1 {
		t.Fatalf("tombstones = %+v", result.Payload.SyncMeta)
	}
	deletion := result.Payload.SyncMeta.Deletions[0]
	if deletion.EntityType != "hosts" || deletion.ID != "deleted" {
		t.Fatalf("deletion = %+v", deletion)
	}
	// A deleted entity's tombstone is dropped once the entity is present again.
	restored := *base
	result, err = MergePayloads(&result.Payload, restored, remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.SyncMeta != nil {
		t.Fatalf("a live entity must clear its tombstone: %+v", result.Payload.SyncMeta)
	}
}

func TestMergePathIdentifiedFamilies(t *testing.T) {
	base := &Payload{
		Groups:          []domainvault.Group{mergeTestGroup("team", "base")},
		NoteGroups:      []domainvault.NoteGroup{{Path: "notes", OrderKey: 1}},
		SnippetPackages: []domainvault.SnippetPackage{{Path: "pkg", OrderKey: 1}},
	}
	local := &Payload{
		Groups:          []domainvault.Group{mergeTestGroup("team", "local")},
		NoteGroups:      []domainvault.NoteGroup{{Path: "notes", OrderKey: 2}},
		SnippetPackages: []domainvault.SnippetPackage{{Path: "pkg", OrderKey: 1}},
	}
	remote := &Payload{
		Groups:          []domainvault.Group{mergeTestGroup("team", "base")},
		NoteGroups:      []domainvault.NoteGroup{{Path: "notes", OrderKey: 1}},
		SnippetPackages: []domainvault.SnippetPackage{{Path: "pkg", OrderKey: 3}},
	}
	result, err := MergePayloads(base, *local, *remote, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Payload.Groups[0].Notes != "local" {
		t.Fatalf("groups = %+v", result.Payload.Groups)
	}
	if result.Payload.NoteGroups[0].OrderKey != 2 {
		t.Fatalf("note groups = %+v", result.Payload.NoteGroups)
	}
	if result.Payload.SnippetPackages[0].OrderKey != 3 {
		t.Fatalf("snippet packages = %+v", result.Payload.SnippetPackages)
	}
}

func mergeTestNetworkProfile(id string, kind domainvault.NetworkProfileKind) domainvault.NetworkProfile {
	profile := domainvault.NetworkProfile{ID: id, Label: id, Kind: kind, UpdatedAt: time.UnixMilli(1000)}
	switch kind {
	case domainvault.NetworkKindTailscale:
		profile.Tailscale = &domainvault.TailscaleNetwork{
			Hostname: id + "-node", ControlURL: "https://headscale.example.com", HasAuthKey: true,
		}
	case domainvault.NetworkKindWireGuard:
		profile.WireGuard = &domainvault.WireGuardNetwork{
			Addresses: []string{"10.0.0.2/32"}, DNS: []string{"10.0.0.1"}, MTU: 1280,
			HasPrivateKey: true,
			Peers: []domainvault.WireGuardPeer{
				{ID: id + "-peer-1", PublicKey: "peer-public-1", AllowedIPs: []string{"10.0.0.0/24"}, HasPresharedKey: true},
				{ID: id + "-peer-2", PublicKey: "peer-public-2", AllowedIPs: []string{"10.1.0.0/24"}, HasPresharedKey: true},
			},
		}
	}
	return profile
}

func networkSecret(entityType, entityID, field, value string) SecretValue {
	return SecretValue{EntityType: entityType, EntityID: entityID, Field: field, Value: value}
}

func TestMergeNetworkProfilesDeletesSurvivingRecordsAndOrphanPeerSecrets(t *testing.T) {
	ts := mergeTestNetworkProfile("net-ts", domainvault.NetworkKindTailscale)
	wg := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	base := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{ts, wg},
		Secrets: []SecretValue{
			networkSecret("network_profile", "net-ts", "network_auth_key", "base-auth"),
			networkSecret("network_profile", "net-wg", "network_private_key", "base-private"),
			networkSecret("network_peer", "net-wg-peer-1", "network_preshared_key", "base-psk-1"),
			networkSecret("network_peer", "net-wg-peer-2", "network_preshared_key", "base-psk-2"),
		},
	}
	// Local drops net-ts, removes net-wg-peer-2, edits net-wg, and adds a new
	// profile. A real Snapshot would also omit the dropped secrets.
	localWG := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	localWG.Label = "wg local"
	localWG.WireGuard.Peers = localWG.WireGuard.Peers[:1]
	added := mergeTestNetworkProfile("net-added", domainvault.NetworkKindTailscale)
	local := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{localWG, added},
		Secrets: []SecretValue{
			networkSecret("network_profile", "net-wg", "network_private_key", "local-private"),
			networkSecret("network_peer", "net-wg-peer-1", "network_preshared_key", "local-psk-1"),
			networkSecret("network_profile", "net-added", "network_auth_key", "added-auth"),
		},
	}
	result, err := MergePayloads(&base, local, base, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads: %v", err)
	}
	byID := map[string]domainvault.NetworkProfile{}
	for _, profile := range result.Payload.NetworkProfiles {
		if _, exists := byID[profile.ID]; exists {
			t.Fatalf("duplicate profile %q in merged payload", profile.ID)
		}
		byID[profile.ID] = profile
	}
	if _, ok := byID["net-ts"]; ok {
		t.Error("a deleted profile was resurrected")
	}
	if byID["net-wg"].Label != "wg local" || len(byID["net-wg"].WireGuard.Peers) != 1 ||
		byID["net-wg"].WireGuard.Peers[0].ID != "net-wg-peer-1" {
		t.Fatalf("local profile edit was lost: %+v", byID["net-wg"])
	}
	if _, ok := byID["net-added"]; !ok {
		t.Error("a one-sided profile addition was lost")
	}

	secretValues := map[string]string{}
	for _, secret := range result.Payload.Secrets {
		secretValues[secret.EntityType+"/"+secret.EntityID+"/"+secret.Field] = secret.Value
	}
	want := map[string]string{
		"network_profile/net-wg/network_private_key":       "local-private",
		"network_peer/net-wg-peer-1/network_preshared_key": "local-psk-1",
		"network_profile/net-added/network_auth_key":       "added-auth",
	}
	if len(secretValues) != len(want) {
		t.Fatalf("secrets = %+v, want %+v", secretValues, want)
	}
	for key, value := range want {
		if secretValues[key] != value {
			t.Errorf("secret %s = %q, want %q", key, secretValues[key], value)
		}
	}
	if _, ok := secretValues["network_peer/net-wg-peer-2/network_preshared_key"]; ok {
		t.Error("a removed peer's Preshared Key was retained")
	}
	if _, ok := secretValues["network_profile/net-ts/network_auth_key"]; ok {
		t.Error("a deleted profile's Auth Key was retained")
	}

	// Profile records never carry plaintext, and the Has* markers survive.
	encoded, err := json.Marshal(result.Payload.NetworkProfiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range []string{"local-private", "local-psk-1", "added-auth"} {
		if bytes.Contains(encoded, []byte(plaintext)) {
			t.Fatalf("merged profile records contain plaintext %q", plaintext)
		}
	}
	if !bytes.Contains(encoded, []byte(`"hasAuthKey":true`)) ||
		!bytes.Contains(encoded, []byte(`"hasPresharedKey":true`)) {
		t.Fatalf("Has* markers were lost: %s", encoded)
	}
}

func TestMergeNetworkProfileTombstonePreventsResurrection(t *testing.T) {
	wg := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	ts := mergeTestNetworkProfile("net-ts", domainvault.NetworkKindTailscale)
	local := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{ts, wg},
		Secrets: []SecretValue{
			networkSecret("network_profile", "net-ts", "network_auth_key", "stale-auth"),
			networkSecret("network_peer", "net-wg-peer-1", "network_preshared_key", "live-psk"),
		},
	}
	remote := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{wg},
		SyncMeta: &SyncMeta{SchemaVersion: 1, Deletions: []SyncDeletion{{
			EntityType: "networkProfiles", ID: "net-ts", DeletedAt: 100,
		}}},
	}
	result, err := MergePayloads(nil, local, remote, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads: %v", err)
	}
	if len(result.Payload.NetworkProfiles) != 1 || result.Payload.NetworkProfiles[0].ID != "net-wg" {
		t.Fatalf("tombstoned profile was resurrected: %+v", result.Payload.NetworkProfiles)
	}
	if len(result.Payload.Secrets) != 1 ||
		result.Payload.Secrets[0].EntityID != "net-wg-peer-1" {
		t.Fatalf("secrets after tombstone = %+v", result.Payload.Secrets)
	}
	if result.Payload.SyncMeta == nil || len(result.Payload.SyncMeta.Deletions) != 1 ||
		result.Payload.SyncMeta.Deletions[0].EntityType != "networkProfiles" ||
		result.Payload.SyncMeta.Deletions[0].ID != "net-ts" {
		t.Fatalf("tombstone was dropped: %+v", result.Payload.SyncMeta)
	}
}

func TestFilterSecretsKeepsOnlyNestedPeerKeys(t *testing.T) {
	wg := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	payload := Payload{
		Hosts:           []domainvault.Host{mergeTestHost("h1", "H", "h.example")},
		NetworkProfiles: []domainvault.NetworkProfile{wg},
	}
	secrets := []SecretValue{
		networkSecret("network_profile", "net-wg", "network_private_key", "private"),
		networkSecret("network_profile", "unknown", "network_private_key", "orphan-profile"),
		networkSecret("network_peer", "net-wg-peer-1", "network_preshared_key", "kept-peer"),
		networkSecret("network_peer", "missing-peer", "network_preshared_key", "orphan-peer"),
		networkSecret("host", "h1", "password", "host-secret"),
		networkSecret("host", "missing", "password", "orphan-host"),
	}
	filtered := filterSecretsToPayload(payload, secrets)
	kept := map[string]bool{}
	for _, secret := range filtered {
		kept[secret.EntityType+"/"+secret.EntityID] = true
	}
	for _, want := range []string{"network_profile/net-wg", "network_peer/net-wg-peer-1", "host/h1"} {
		if !kept[want] {
			t.Errorf("secret %s was dropped", want)
		}
	}
	for _, unwanted := range []string{"network_profile/unknown", "network_peer/missing-peer", "host/missing"} {
		if kept[unwanted] {
			t.Errorf("secret %s was retained", unwanted)
		}
	}
}

func TestLegacyPayloadWithoutNetworkProfilesStillParsesAndMerges(t *testing.T) {
	// A payload written before the family existed has no networkProfiles key.
	legacy := []byte(`{"hosts":[{"id":"h1","label":"H","hostname":"h.example","port":22,"username":"root","protocol":"ssh"}],"keys":[],"identities":[],"proxyProfiles":[],"snippets":[],"groups":[],"snippetPackages":[],"notes":[],"noteGroups":[],"portForwardingRules":[],"syncedAt":1}`)
	var payload Payload
	if err := json.Unmarshal(legacy, &payload); err != nil {
		t.Fatalf("legacy payload did not parse: %v", err)
	}
	if len(payload.NetworkProfiles) != 0 {
		t.Fatalf("legacy payload produced profiles: %+v", payload.NetworkProfiles)
	}
	result, err := MergePayloads(nil, payload, Payload{}, MergeOptions{})
	if err != nil {
		t.Fatalf("legacy payload did not merge: %v", err)
	}
	if len(result.Payload.Hosts) != 1 || len(result.Payload.NetworkProfiles) != 0 {
		t.Fatalf("legacy merge = %+v", result.Payload)
	}
}

func TestNetworkProfilesJSONDistinguishesMissingFromExplicitlyEmpty(t *testing.T) {
	const baseFields = `"hosts":[],"keys":[],"identities":[],"proxyProfiles":[],"snippets":[],"groups":[],"snippetPackages":[],"notes":[],"noteGroups":[],"portForwardingRules":[],"syncedAt":1`
	var missing Payload
	if err := json.Unmarshal([]byte("{"+baseFields+"}"), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.NetworkProfiles != nil {
		t.Fatal("a missing networkProfiles key must decode as nil")
	}
	var explicit Payload
	if err := json.Unmarshal([]byte("{"+baseFields+`,"networkProfiles":[]}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.NetworkProfiles == nil {
		t.Fatal("an explicit [] must decode as a non-nil empty slice")
	}
	encoded, err := json.Marshal(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"networkProfiles":[]`)) {
		t.Fatalf("an empty family did not serialize as []: %s", encoded)
	}
}

func TestLegacyPayloadWithoutNetworkProfilesPreservesLocalProfiles(t *testing.T) {
	// A payload written before the family existed cannot express a deletion.
	const legacy = `{"hosts":[],"keys":[],"identities":[],"proxyProfiles":[],"snippets":[],"groups":[],"snippetPackages":[],"notes":[],"noteGroups":[],"portForwardingRules":[],"syncedAt":1}`
	var remote Payload
	if err := json.Unmarshal([]byte(legacy), &remote); err != nil {
		t.Fatalf("legacy payload did not parse: %v", err)
	}
	if remote.NetworkProfiles != nil {
		t.Fatal("the legacy payload unexpectedly carries networkProfiles")
	}

	wg := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	local := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{wg},
		Secrets: []SecretValue{
			networkSecret("network_profile", "net-wg", "network_private_key", "local-private"),
			networkSecret("network_peer", "net-wg-peer-1", "network_preshared_key", "local-psk"),
		},
	}
	base := Payload{NetworkProfiles: []domainvault.NetworkProfile{wg}}
	result, err := MergePayloads(&base, local, remote, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads: %v", err)
	}
	if len(result.Payload.NetworkProfiles) != 1 || result.Payload.NetworkProfiles[0].ID != "net-wg" {
		t.Fatalf("a pre-family payload deleted local profiles: %+v", result.Payload.NetworkProfiles)
	}
	values := map[string]string{}
	for _, secret := range result.Payload.Secrets {
		values[secret.EntityType+"/"+secret.EntityID+"/"+secret.Field] = secret.Value
	}
	if values["network_profile/net-wg/network_private_key"] != "local-private" ||
		values["network_peer/net-wg-peer-1/network_preshared_key"] != "local-psk" {
		t.Fatalf("network credentials were dropped: %+v", values)
	}
	if result.Payload.SyncMeta != nil {
		for _, deletion := range result.Payload.SyncMeta.Deletions {
			if deletion.EntityType == "networkProfiles" {
				t.Fatalf("a pre-family payload produced a tombstone: %+v", deletion)
			}
		}
	}
}

func TestLegacyNilNetworkProfilesHonorsRelayedTombstone(t *testing.T) {
	wg := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	local := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{wg},
		Secrets: []SecretValue{
			networkSecret("network_profile", "net-wg", "network_private_key", "local-private"),
			networkSecret("network_peer", "net-wg-peer-1", "network_preshared_key", "local-psk-1"),
			networkSecret("network_peer", "net-wg-peer-2", "network_preshared_key", "local-psk-2"),
		},
	}
	base := Payload{NetworkProfiles: []domainvault.NetworkProfile{wg}}

	t.Run("relayed tombstone deletes", func(t *testing.T) {
		// A pre-family writer omits the family entirely but still relays the
		// tombstone a newer device published.
		remote := Payload{SyncMeta: &SyncMeta{SchemaVersion: 1, Deletions: []SyncDeletion{{
			EntityType: "networkProfiles", ID: "net-wg", DeletedAt: 100,
		}}}}
		if remote.NetworkProfiles != nil {
			t.Fatal("the test payload must omit the networkProfiles family")
		}
		for name, basePayload := range map[string]*Payload{
			"without base": nil,
			"with base":    &base,
		} {
			t.Run(name, func(t *testing.T) {
				result, err := MergePayloads(basePayload, local, remote, MergeOptions{})
				if err != nil {
					t.Fatalf("MergePayloads: %v", err)
				}
				if len(result.Payload.NetworkProfiles) != 0 {
					t.Fatalf("tombstoned profile survived: %+v", result.Payload.NetworkProfiles)
				}
				if len(result.Payload.Secrets) != 0 {
					t.Fatalf("tombstoned profile's secrets survived: %+v", result.Payload.Secrets)
				}
				found := false
				if result.Payload.SyncMeta != nil {
					for _, deletion := range result.Payload.SyncMeta.Deletions {
						if deletion.EntityType == "networkProfiles" && deletion.ID == "net-wg" {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("relayed tombstone was dropped: %+v", result.Payload.SyncMeta)
				}
			})
		}
	})

	t.Run("absence without tombstone preserves", func(t *testing.T) {
		result, err := MergePayloads(&base, local, Payload{}, MergeOptions{})
		if err != nil {
			t.Fatalf("MergePayloads: %v", err)
		}
		if len(result.Payload.NetworkProfiles) != 1 || result.Payload.NetworkProfiles[0].ID != "net-wg" {
			t.Fatalf("plain absence deleted local profiles: %+v", result.Payload.NetworkProfiles)
		}
		if len(result.Payload.Secrets) != 3 {
			t.Fatalf("plain absence dropped network credentials: %+v", result.Payload.Secrets)
		}
		if result.Payload.SyncMeta != nil {
			for _, deletion := range result.Payload.SyncMeta.Deletions {
				if deletion.EntityType == "networkProfiles" {
					t.Fatalf("plain absence produced a tombstone: %+v", deletion)
				}
			}
		}
	})
}

func TestExplicitEmptyNetworkProfilesPropagatesADeletion(t *testing.T) {
	wg := mergeTestNetworkProfile("net-wg", domainvault.NetworkKindWireGuard)
	local := Payload{
		NetworkProfiles: []domainvault.NetworkProfile{wg},
		Secrets: []SecretValue{
			networkSecret("network_profile", "net-wg", "network_private_key", "local-private"),
		},
	}
	remote := Payload{NetworkProfiles: []domainvault.NetworkProfile{}}
	result, err := MergePayloads(&local, local, remote, MergeOptions{})
	if err != nil {
		t.Fatalf("MergePayloads: %v", err)
	}
	if len(result.Payload.NetworkProfiles) != 0 || len(result.Payload.Secrets) != 0 {
		t.Fatalf("an explicit empty family did not delete: %+v", result.Payload)
	}
	found := false
	if result.Payload.SyncMeta != nil {
		for _, deletion := range result.Payload.SyncMeta.Deletions {
			if deletion.EntityType == "networkProfiles" && deletion.ID == "net-wg" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the deletion was not tombstoned: %+v", result.Payload.SyncMeta)
	}
}

func settingsFromJSON(t *testing.T, raw string) map[string]json.RawMessage {
	t.Helper()
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatalf("settings JSON: %v", err)
	}
	return settings
}
func settingsToStringMap(t *testing.T, settings map[string]json.RawMessage) map[string]any {
	t.Helper()
	value, err := decodeSettingsMap(settings)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
