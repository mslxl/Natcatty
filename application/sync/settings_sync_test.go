package sync

import (
	"encoding/json"
	"testing"

	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// These mirror adapters/syncedsettings's namespaced keys. The application
// layer owns the wire shape, and the adapter test pins the same values.
const (
	settingsKeyPreferences = "natcatty.preferences"
	settingsKeyTerminal    = "natcatty.terminal"
	settingsKeyThemes      = "natcatty.themes"
)

func TestSettingsFamilyKeepsTheFlatObjectShape(t *testing.T) {
	preferences := json.RawMessage(`{"appearance":{"terminalFontSize":15},"layout":{"showSftpTab":false}}`)
	themes := json.RawMessage(`[{"id":"custom-x","name":"X"}]`)
	payload := Payload{Settings: map[string]json.RawMessage{
		settingsKeyPreferences: preferences,
		settingsKeyThemes:      themes,
	}}

	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	records := snapshot.Families["settings"]
	if len(records) != 2 {
		t.Fatalf("settings records = %+v, want one per document", records)
	}

	raw, err := domainsync.RawSnapshotFor(snapshot, convergentFamilies())
	if err != nil {
		t.Fatalf("RawSnapshotFor: %v", err)
	}
	var restored map[string]json.RawMessage
	if err := json.Unmarshal(raw["settings"], &restored); err != nil {
		t.Fatalf("settings payload is not an object: %v", err)
	}
	assertJSONEqual(t, restored[settingsKeyPreferences], preferences)
	assertJSONEqual(t, restored[settingsKeyThemes], themes)
}

func TestSettingsFamilyMergesDifferentDocumentsAndFields(t *testing.T) {
	localPayload := Payload{Settings: map[string]json.RawMessage{
		settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":15},"layout":{"showSftpTab":true}}`),
		settingsKeyTerminal:    json.RawMessage(`{"OSC52":"write"}`),
	}}
	remotePayload := Payload{Settings: map[string]json.RawMessage{
		settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":15},"layout":{"showSftpTab":false}}`),
	}}

	localState := convergentStateForTest(t, localPayload, "device-a", 1000)
	remoteState := convergentStateForTest(t, remotePayload, "device-b", 2000)
	joined, err := domainsync.MergeConvergentStates(localState, remoteState)
	if err != nil {
		t.Fatalf("MergeConvergentStates: %v", err)
	}
	payload, err := payloadFromState(joined, nil, 3000, "device-a")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	if _, ok := payload.Settings[settingsKeyTerminal]; !ok {
		t.Fatalf("terminal settings were dropped: %+v", payload.Settings)
	}
	// The two devices changed different top-level groups of the same
	// document, so both changes survive the per-field registers.
	var preferences map[string]map[string]json.RawMessage
	if err := json.Unmarshal(payload.Settings[settingsKeyPreferences], &preferences); err != nil {
		t.Fatalf("decode merged preferences: %v", err)
	}
	assertJSONEqual(t, preferences["appearance"]["terminalFontSize"], json.RawMessage(`15`))
	assertJSONEqual(t, preferences["layout"]["showSftpTab"], json.RawMessage(`false`))
}

func TestMigrateGroupFamilyReKeysLegacyPathRecords(t *testing.T) {
	legacy := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"groups": {
			{ID: "Prod", Value: json.RawMessage(`{"path":"Prod","notes":"parent"}`)},
			{ID: "Prod/Web", Value: json.RawMessage(`{"path":"Prod/Web","notes":"child","orderKey":10}`)},
		},
		"hosts": {
			{ID: "host-1", Value: json.RawMessage(`{"id":"host-1","group":"Prod/Web"}`)},
		},
	}}
	state, err := domainsync.CreateConvergentStateFromSnapshot(legacy, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}

	migrated, changed, err := migrateGroupFamily(state)
	if err != nil {
		t.Fatalf("migrateGroupFamily: %v", err)
	}
	if !changed {
		t.Fatal("migrateGroupFamily reported no change for path-keyed records")
	}
	snapshot, err := domainsync.MaterializeConvergentSnapshot(migrated)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	groups := recordsByValueID(t, snapshot.Families["groups"])
	prodID := domainvault.GroupIDFromPath("Prod")
	webID := domainvault.GroupIDFromPath("Prod/Web")
	prod, ok := groups[prodID]
	if !ok {
		t.Fatalf("migrated groups = %v, want %q", groups, prodID)
	}
	web, ok := groups[webID]
	if !ok {
		t.Fatalf("migrated groups = %v, want %q", groups, webID)
	}
	if _, exists := prod["path"]; exists {
		t.Fatalf("migrated group kept the derived path: %v", prod)
	}
	assertJSONEqual(t, prod["name"], json.RawMessage(`"Prod"`))
	assertJSONEqual(t, prod["notes"], json.RawMessage(`"parent"`))
	assertJSONEqual(t, web["name"], json.RawMessage(`"Web"`))
	assertJSONEqual(t, web["parentId"], json.RawMessage(`"`+prodID+`"`))
	assertJSONEqual(t, web["orderKey"], json.RawMessage(`10`))

	_, changedAgain, err := migrateGroupFamily(migrated)
	if err != nil {
		t.Fatalf("second migrateGroupFamily: %v", err)
	}
	if changedAgain {
		t.Fatal("migration is not idempotent")
	}
}

func TestPayloadFromStateDecoratesGroupReferencesWithoutBreakingTheEnvelope(t *testing.T) {
	state := convergentStateForTest(t, Payload{
		Groups: []domainvault.Group{
			{ID: "g-prod", Name: "Prod", OrderKey: 1},
			{ID: "g-web", ParentID: "g-prod", Name: "Web", OrderKey: 2},
		},
		Hosts: []domainvault.Host{{
			ID: "host-1", Label: "web", GroupID: "g-web",
		}},
		Snippets: []domainvault.Snippet{{
			ID: "snippet-1", Label: "deploy", TargetGroupIDs: []string{"g-web"},
		}},
	}, "device-a", 1000)

	outgoing, err := payloadFromState(state, nil, 2000, "device-a")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	paths := make(map[string]string, len(outgoing.Groups))
	for _, group := range outgoing.Groups {
		paths[group.ID] = group.Path
	}
	if paths["g-prod"] != "Prod" || paths["g-web"] != "Prod/Web" {
		t.Fatalf("group paths = %v", paths)
	}
	if len(outgoing.Hosts) != 1 || outgoing.Hosts[0].Group != "Prod/Web" {
		t.Fatalf("host group = %+v", outgoing.Hosts)
	}
	if len(outgoing.Snippets) != 1 ||
		len(outgoing.Snippets[0].TargetGroups) != 1 ||
		outgoing.Snippets[0].TargetGroups[0] != "Prod/Web" {
		t.Fatalf("snippet groups = %+v", outgoing.Snippets)
	}

	snapshot, err := payloadSnapshot(outgoing)
	if err != nil {
		t.Fatalf("payloadSnapshot(outgoing): %v", err)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(state, snapshot); err != nil {
		t.Fatalf("decorated payload no longer matches the envelope: %v", err)
	}
}

func recordsByValueID(t *testing.T, records []domainsync.ConvergentRecord) map[string]map[string]json.RawMessage {
	t.Helper()
	out := make(map[string]map[string]json.RawMessage, len(records))
	for _, record := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(record.Value, &fields); err != nil {
			t.Fatalf("decode record %q: %v", record.ID, err)
		}
		out[record.ID] = fields
	}
	return out
}

func assertJSONEqual(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	canonicalGot, err := domainsync.CanonicalValue(got)
	if err != nil {
		t.Fatalf("canonicalize got %s: %v", got, err)
	}
	canonicalWant, err := domainsync.CanonicalValue(want)
	if err != nil {
		t.Fatalf("canonicalize want %s: %v", want, err)
	}
	if string(canonicalGot) != string(canonicalWant) {
		t.Fatalf("got %s, want %s", canonicalGot, canonicalWant)
	}
}

func TestGroupMoveOnOneDeviceCarriesMembersOnAnother(t *testing.T) {
	base := Payload{
		Groups: []domainvault.Group{
			{ID: "g-prod", Name: "Prod", OrderKey: 1},
			{ID: "g-web", ParentID: "g-prod", Name: "Web", OrderKey: 2},
		},
		Hosts: []domainvault.Host{{
			ID: "host-1", Label: "web", GroupID: "g-web",
		}},
	}
	baseState := convergentStateForTest(t, base, "device-a", 1000)
	otherState, err := domainsync.NormalizeConvergentState(baseState)
	if err != nil {
		t.Fatalf("NormalizeConvergentState: %v", err)
	}

	moved := base
	moved.Groups = append([]domainvault.Group(nil), base.Groups...)
	moved.Groups[1].ParentID = ""
	movedSnapshot, err := payloadSnapshot(moved)
	if err != nil {
		t.Fatalf("payloadSnapshot(moved): %v", err)
	}
	otherState, _, err = domainsync.ApplyConvergentSnapshot(otherState, movedSnapshot, "device-b", 2000, false)
	if err != nil {
		t.Fatalf("ApplyConvergentSnapshot: %v", err)
	}

	joined, err := domainsync.MergeConvergentStates(baseState, otherState)
	if err != nil {
		t.Fatalf("MergeConvergentStates: %v", err)
	}
	payload, err := payloadFromState(joined, nil, 3000, "device-a")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	paths := make(map[string]string, len(payload.Groups))
	for _, group := range payload.Groups {
		paths[group.ID] = group.Path
	}
	if paths["g-web"] != "Web" {
		t.Fatalf("group path after the move = %q, want Web", paths["g-web"])
	}
	if len(payload.Hosts) != 1 || payload.Hosts[0].Group != "Web" {
		t.Fatalf("host group after the move = %+v, want Web", payload.Hosts)
	}
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(joined, snapshot); err != nil {
		t.Fatalf("materialized payload does not match the joined envelope: %v", err)
	}
}

func TestMigrationAdoptsTheCloudCopyWithOnlyLocalDefaultSettings(t *testing.T) {
	remotePayload := Payload{
		Hosts: []domainvault.Host{{ID: "host-1", Label: "web"}},
		Settings: map[string]json.RawMessage{
			settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":22}}`),
		},
	}
	remoteState := convergentStateForTest(t, remotePayload, "device-b", 900)
	remoteSnapshot, err := domainsync.MaterializeConvergentSnapshot(remoteState)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	localPayload := Payload{Settings: map[string]json.RawMessage{
		settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":13}}`),
	}}
	localSnapshot, err := payloadSnapshot(localPayload)
	if err != nil {
		t.Fatalf("payloadSnapshot(local): %v", err)
	}

	plan := planConvergentMigration(convergentMigrationInput{
		DeviceID:      "device-a",
		Now:           2000,
		Strategy:      domainsync.StrategySmartMerge,
		Local:         localPayload,
		LocalSnapshot: localSnapshot,
		Remote: convergentRemote{
			Kind:          convergentRemoteV2,
			Payload:       remotePayload,
			State:         &remoteState,
			Materialized:  remoteSnapshot,
			SchemaVersion: 2,
		},
	})
	if !plan.Preview.CanInitialize || plan.Materialized == nil {
		t.Fatalf("fresh install was blocked: %+v", plan.Preview.BlockedReasons)
	}
	if len(plan.Materialized.Hosts) != 1 {
		t.Fatalf("adopted hosts = %+v, want the cloud Host", plan.Materialized.Hosts)
	}
	assertJSONEqual(t, plan.Materialized.Settings[settingsKeyPreferences],
		remotePayload.Settings[settingsKeyPreferences])
}

func TestMigrationIntoAV1CloudCarriesTheLocalPortableSettings(t *testing.T) {
	remotePayload := Payload{
		Hosts: []domainvault.Host{{ID: "host-1", Label: "web"}},
	}
	localPayload := Payload{
		Hosts: []domainvault.Host{{ID: "host-1", Label: "web"}},
		Settings: map[string]json.RawMessage{
			settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":17}}`),
		},
	}
	localSnapshot, err := payloadSnapshot(localPayload)
	if err != nil {
		t.Fatalf("payloadSnapshot(local): %v", err)
	}
	plan := planConvergentMigration(convergentMigrationInput{
		DeviceID:      "device-a",
		Now:           2000,
		Strategy:      domainsync.StrategySmartMerge,
		Local:         localPayload,
		LocalSnapshot: localSnapshot,
		Remote: convergentRemote{
			Kind:          convergentRemoteV1,
			Payload:       remotePayload,
			SchemaVersion: 1,
		},
	})
	if !plan.Preview.CanInitialize || plan.Materialized == nil {
		t.Fatalf("v1 adoption was blocked: %+v", plan.Preview.BlockedReasons)
	}
	assertJSONEqual(t, plan.Materialized.Settings[settingsKeyPreferences],
		localPayload.Settings[settingsKeyPreferences])
}

func TestMigrationDoesNotBlockWhenOnlyPortableSettingsDiffer(t *testing.T) {
	remotePayload := Payload{
		Hosts: []domainvault.Host{{ID: "host-1", Label: "web"}},
		Settings: map[string]json.RawMessage{
			settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":22}}`),
		},
	}
	remoteState := convergentStateForTest(t, remotePayload, "device-b", 900)
	remoteSnapshot, err := domainsync.MaterializeConvergentSnapshot(remoteState)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	localPayload := Payload{
		Hosts: []domainvault.Host{{ID: "host-1", Label: "web"}},
		Settings: map[string]json.RawMessage{
			settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":13}}`),
		},
	}
	localSnapshot, err := payloadSnapshot(localPayload)
	if err != nil {
		t.Fatalf("payloadSnapshot(local): %v", err)
	}
	plan := planConvergentMigration(convergentMigrationInput{
		DeviceID:      "device-a",
		Now:           2000,
		Strategy:      domainsync.StrategySmartMerge,
		Local:         localPayload,
		LocalSnapshot: localSnapshot,
		Remote: convergentRemote{
			Kind:          convergentRemoteV2,
			Payload:       remotePayload,
			State:         &remoteState,
			Materialized:  remoteSnapshot,
			SchemaVersion: 2,
		},
	})
	if !plan.Preview.CanInitialize || plan.Materialized == nil {
		t.Fatalf("settings-only difference blocked the join: %+v", plan.Preview.BlockedReasons)
	}
	// The cloud copy is the source of truth for the joining device; its
	// settings are what the state and the outgoing payload carry.
	assertJSONEqual(t, plan.Materialized.Settings[settingsKeyPreferences],
		remotePayload.Settings[settingsKeyPreferences])
}

func TestMigrationPublishesSettingsFromAnEmptyVaultWhenNoCloudCopyExists(t *testing.T) {
	localPayload := Payload{Settings: map[string]json.RawMessage{
		settingsKeyPreferences: json.RawMessage(`{"appearance":{"terminalFontSize":16}}`),
	}}
	localSnapshot, err := payloadSnapshot(localPayload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	plan := planConvergentMigration(convergentMigrationInput{
		DeviceID:      "device-a",
		Now:           2000,
		Strategy:      domainsync.StrategySmartMerge,
		Local:         localPayload,
		LocalSnapshot: localSnapshot,
		Remote: convergentRemote{
			Kind: convergentRemoteMissing,
		},
	})
	if !plan.Preview.CanInitialize || plan.Materialized == nil {
		t.Fatalf("settings-only Vault was blocked: %+v", plan.Preview.BlockedReasons)
	}
	assertJSONEqual(t, plan.Materialized.Settings[settingsKeyPreferences],
		localPayload.Settings[settingsKeyPreferences])
}

func TestSettingsFamilyRejectsANullDocument(t *testing.T) {
	_, err := payloadSnapshot(Payload{Settings: map[string]json.RawMessage{
		settingsKeyPreferences: json.RawMessage(`null`),
	}})
	if err == nil {
		t.Fatal("a null settings document was accepted")
	}
}

func TestMigratedGroupStateBuriesAPathKeyedReplica(t *testing.T) {
	legacy := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"groups": {
			{ID: "Prod", Value: json.RawMessage(`{"path":"Prod","notes":"parent"}`)},
			{ID: "Prod/Web", Value: json.RawMessage(`{"path":"Prod/Web","notes":"child"}`)},
		},
	}}
	legacyState, err := domainsync.CreateConvergentStateFromSnapshot(legacy, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}
	migrated, _, err := migrateGroupFamily(legacyState)
	if err != nil {
		t.Fatalf("migrateGroupFamily: %v", err)
	}

	// A peer that still holds the old path-keyed state must not resurrect the
	// tombstoned entities when the two states merge.
	merged, err := domainsync.MergeConvergentStates(migrated, legacyState)
	if err != nil {
		t.Fatalf("MergeConvergentStates: %v", err)
	}
	snapshot, err := domainsync.MaterializeConvergentSnapshot(merged)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	groups := snapshot.Families["groups"]
	if len(groups) != 2 {
		t.Fatalf("visible groups after merge = %d, want the two stable-ID records", len(groups))
	}
	for _, record := range groups {
		if record.ID == "Prod" || record.ID == "Prod/Web" {
			t.Fatalf("legacy path-keyed group %q was resurrected", record.ID)
		}
	}
}

func TestLegacyPayloadWithOnlyALeafGroupMatchesTheMigratedEnvelope(t *testing.T) {
	// An old v2 record is the canonical form of a marshaled payload record, so
	// build the two shapes from the same struct values.
	groupValue, err := domainsync.CanonicalValue(mustMarshal(t, domainvault.Group{
		Path: "Prod/Web", Notes: "child",
	}))
	if err != nil {
		t.Fatal(err)
	}
	hostValue, err := domainsync.CanonicalValue(mustMarshal(t, domainvault.Host{
		ID: "host-1", Label: "web", Group: "Prod/Web",
	}))
	if err != nil {
		t.Fatal(err)
	}
	legacy := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"groups": {
			{ID: "Prod/Web", Value: groupValue},
		},
		"hosts": {
			{ID: "host-1", Value: hostValue},
		},
	}}
	state, err := domainsync.CreateConvergentStateFromSnapshot(legacy, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}
	migrated, _, err := migrateGroupFamily(state)
	if err != nil {
		t.Fatalf("migrateGroupFamily: %v", err)
	}
	payload := Payload{
		Groups: []domainvault.Group{{Path: "Prod/Web", Notes: "child"}},
		Hosts:  []domainvault.Host{{ID: "host-1", Label: "web", Group: "Prod/Web"}},
	}
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	if records := snapshot.Families["groups"]; len(records) != 2 {
		t.Fatalf("materialized groups = %+v, want the leaf and its synthesized parent", records)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(migrated, snapshot); err != nil {
		t.Fatalf("legacy payload does not match the migrated envelope: %v", err)
	}
	migratedSnapshot, err := domainsync.MaterializeConvergentSnapshot(migrated)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	hosts := recordsByValueID(t, migratedSnapshot.Families["hosts"])
	host, ok := hosts["host-1"]
	if !ok {
		t.Fatalf("migrated hosts = %v, want host-1", hosts)
	}
	if _, exists := host["group"]; exists {
		t.Fatalf("migrated host kept the derived path: %v", host)
	}
	assertJSONEqual(t, host["groupId"], json.RawMessage(`"`+domainvault.GroupIDFromPath("Prod/Web")+`"`))
}

func TestLegacyHostInAnImplicitGroupMigratesToStableReferences(t *testing.T) {
	// An old replica could hold a Host in a group path without any group record
	// at all. The migration must synthesize the ancestor nodes and keep the
	// Host's membership under the stable ID.
	hostValue, err := domainsync.CanonicalValue(mustMarshal(t, domainvault.Host{
		ID: "host-1", Label: "web", Group: "Prod/Web",
	}))
	if err != nil {
		t.Fatal(err)
	}
	legacy := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"hosts": {{ID: "host-1", Value: hostValue}},
	}}
	state, err := domainsync.CreateConvergentStateFromSnapshot(legacy, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}
	migrated, changed, err := migrateGroupFamily(state)
	if err != nil {
		t.Fatalf("migrateGroupFamily: %v", err)
	}
	if !changed {
		t.Fatal("migrateGroupFamily reported no change")
	}

	payload := Payload{Hosts: []domainvault.Host{{
		ID: "host-1", Label: "web", Group: "Prod/Web",
	}}}
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	if records := snapshot.Families["groups"]; len(records) != 2 {
		t.Fatalf("synthesized groups = %+v, want Prod and Prod/Web", records)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(migrated, snapshot); err != nil {
		t.Fatalf("legacy host payload does not match the migrated envelope: %v", err)
	}
}

func TestLegacySnippetTargetsMigrateToStableReferences(t *testing.T) {
	snippetValue, err := domainsync.CanonicalValue(mustMarshal(t, domainvault.Snippet{
		ID: "snippet-1", Label: "deploy",
		TargetGroups: []string{"Prod/Web", "Prod/Web"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	legacy := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"snippets": {{ID: "snippet-1", Value: snippetValue}},
	}}
	state, err := domainsync.CreateConvergentStateFromSnapshot(legacy, "device-a", 1000)
	if err != nil {
		t.Fatalf("CreateConvergentStateFromSnapshot: %v", err)
	}
	migrated, _, err := migrateGroupFamily(state)
	if err != nil {
		t.Fatalf("migrateGroupFamily: %v", err)
	}
	payload := Payload{Snippets: []domainvault.Snippet{{
		ID: "snippet-1", Label: "deploy",
		TargetGroups: []string{"Prod/Web", "Prod/Web"},
	}}}
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(migrated, snapshot); err != nil {
		t.Fatalf("legacy snippet payload does not match the migrated envelope: %v", err)
	}
	migratedSnapshot, err := domainsync.MaterializeConvergentSnapshot(migrated)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	snippets := recordsByValueID(t, migratedSnapshot.Families["snippets"])
	snippet, ok := snippets["snippet-1"]
	if !ok {
		t.Fatalf("migrated snippets = %v, want snippet-1", snippets)
	}
	if _, exists := snippet["targetGroups"]; exists {
		t.Fatalf("migrated snippet kept the derived paths: %v", snippet)
	}
	assertJSONEqual(t, snippet["targetGroupIds"],
		json.RawMessage(`["`+domainvault.GroupIDFromPath("Prod/Web")+`"]`))
}

func mustMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
