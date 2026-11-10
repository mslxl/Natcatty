package sync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// F2 regression fixtures. The payload carries one surviving acme.good
// namespace on the target record and a sibling record that must survive every
// repair.
func interopF2GoodPluginData() plugindata.Map {
	return plugindata.Map{"acme.good": {
		SchemaVersion: 1,
		Fields:        map[string]json.RawMessage{"name": json.RawMessage(`"ok"`)},
	}}
}

func interopF2GoodContainer(t *testing.T) string {
	t.Helper()
	encoded, err := plugindata.EncodeMap(interopF2GoodPluginData())
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	return string(encoded)
}

func interopF2HostPayload() Payload {
	return payloadWithHosts(
		interopHostWithPluginData("h1", "One", interopF2GoodPluginData()),
		host("h2", "Two", "two.example"),
	)
}

func interopF2ProfilePayload() Payload {
	base := time.UnixMilli(1000)
	return Payload{
		DataProfiles: []domainplugin.DataProfile{
			{
				ID: "p1", Provider: "plugin.acme.data.db", Label: "p1",
				Mode:       domainplugin.DataModeReadOnly,
				PluginData: interopF2GoodPluginData(),
				CreatedAt:  base, UpdatedAt: base,
			},
			{
				ID: "p2", Provider: "plugin.acme.data.db", Label: "p2",
				Mode:      domainplugin.DataModeReadOnly,
				CreatedAt: base, UpdatedAt: base,
			},
		},
		SyncedAt: 1000,
	}
}

// interopRewriteRecordSpellings removes every pluginData spelling from one
// record and writes exactly the given spellings. A map holds case-variant
// spellings side by side and domainsync.MarshalJSON orders them canonically, so
// the last-wins decoder sees a deterministic document order.
func interopRewriteRecordSpellings(t *testing.T, record json.RawMessage,
	spellings map[string]string) json.RawMessage {

	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(record, &fields); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	for key := range fields {
		if plugindata.IsPluginDataProperty(key) {
			delete(fields, key)
		}
	}
	for spelling, container := range spellings {
		value, err := domainsync.CanonicalValue(json.RawMessage(container))
		if err != nil {
			t.Fatalf("canonicalize %s container: %v", spelling, err)
		}
		fields[spelling] = value
	}
	encoded, err := domainsync.MarshalJSON(fields)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return encoded
}

// interopStateWithRecordSpellings rebuilds one state record with the given
// pluginData spellings as ordinary causal registers. Unlike the single-spelling
// helper it can hold the canonical spelling alongside a case-variant one, which
// a foreign writer that never canonicalized its record produces.
func interopStateWithRecordSpellings(t *testing.T, state domainsync.ConvergentState,
	node string, now int64, family, recordID string,
	spellings map[string]string) domainsync.ConvergentState {

	t.Helper()
	snapshot, err := domainsync.MaterializeConvergentSnapshot(state)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	records := snapshot.Families[family]
	found := false
	for index := range records {
		if records[index].ID != recordID {
			continue
		}
		value := interopRewriteRecordSpellings(t, records[index].Value, spellings)
		records[index].Value, err = domainsync.CanonicalValue(value)
		if err != nil {
			t.Fatalf("CanonicalValue: %v", err)
		}
		found = true
	}
	if !found {
		t.Fatalf("state has no %s record %q", family, recordID)
	}
	snapshot.Families[family] = records
	next, _, err := domainsync.ApplyConvergentSnapshot(state, snapshot, node, now, false)
	if err != nil {
		t.Fatalf("ApplyConvergentSnapshot: %v", err)
	}
	return next
}

// interopForeignContentWithSpellings builds v2 ciphertext whose envelope and
// inline payload both carry the same raw, unsanitized spellings. payloadFromState
// sanitizes the inline copy, so the inline record is rewritten afterwards; the
// envelope state is never sanitized.
func interopForeignContentWithSpellings(t *testing.T, state domainsync.ConvergentState,
	family, recordID string, spellings map[string]string) []byte {

	t.Helper()
	return interopForeignContentRewrite(t, state, family, recordID,
		func(record json.RawMessage) json.RawMessage {
			return interopRewriteRecordSpellings(t, record, spellings)
		})
}

// interopForeignContentWithRawInline splices raw pluginData properties into the
// inline record. The state representation cannot hold a duplicate top-level
// property or insignificant whitespace (the CRDT validates canonical values),
// so those shapes only exist on the payload side.
func interopForeignContentWithRawInline(t *testing.T, state domainsync.ConvergentState,
	family, recordID, rawPluginData string) []byte {

	t.Helper()
	return interopForeignContentRewrite(t, state, family, recordID,
		func(record json.RawMessage) json.RawMessage {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(record, &fields); err != nil {
				t.Fatalf("decode record: %v", err)
			}
			for key := range fields {
				if plugindata.IsPluginDataProperty(key) {
					delete(fields, key)
				}
			}
			encoded, err := domainsync.MarshalJSON(fields)
			if err != nil {
				t.Fatalf("encode record: %v", err)
			}
			if len(encoded) == 0 || encoded[len(encoded)-1] != '}' {
				t.Fatalf("record is not an object: %s", encoded)
			}
			body := encoded[:len(encoded)-1]
			if len(fields) > 0 {
				body = append(body, ',')
			}
			body = append(body, []byte(rawPluginData)...)
			return append(body, '}')
		})
}

func interopForeignContentRewrite(t *testing.T, state domainsync.ConvergentState,
	family, recordID string, rewrite func(json.RawMessage) json.RawMessage) []byte {

	t.Helper()
	payload, err := payloadFromState(state, nil, 9000, "other-device")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	data, err := domainsync.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("MarshalJSON payload: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var records []json.RawMessage
	if err := json.Unmarshal(top[family], &records); err != nil {
		t.Fatalf("decode %s: %v", family, err)
	}
	replaced := false
	for index := range records {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(records[index], &fields); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		var id string
		_ = json.Unmarshal(fields["id"], &id)
		if id != recordID {
			continue
		}
		records[index] = rewrite(records[index])
		replaced = true
	}
	if !replaced {
		t.Fatalf("inline payload has no %s record %q", family, recordID)
	}
	top[family], err = domainsync.MarshalJSON(records)
	if err != nil {
		t.Fatalf("encode records: %v", err)
	}
	raw, err := domainsync.MarshalJSON(top)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	envelope, err := domainsync.EncryptPayload(json.RawMessage(raw),
		convergentTestPassword, domainsync.EncryptOptions{
			Version: 1, UpdatedAt: 9000, DeviceID: "other-device",
			DeviceName: "Other Device", AppVersion: "1.0",
			KDFIterations: testKDFIterations, SyncSchemaVersion: 2,
		})
	if err != nil {
		t.Fatalf("EncryptPayload: %v", err)
	}
	content, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatalf("MarshalFile: %v", err)
	}
	return content
}

// interopAssertRemoteMaterializedSpellingsCanonical proves the canonical repair
// reached the published state: every materialized record exposes only the
// canonical pluginData property. A named tombstone register may remain in the
// envelope metadata (that is how the CRDT records a removal), but it must not
// materialize as a record property.
func interopAssertRemoteMaterializedSpellingsCanonical(t *testing.T, remote *fakeRemote,
	spellings map[string]string) {

	t.Helper()
	remote.mu.Lock()
	content := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	file, err := domainsync.UnmarshalFile(content)
	if err != nil {
		t.Fatalf("UnmarshalFile: %v", err)
	}
	plaintext, err := domainsync.DecryptPayload(file, convergentTestPassword)
	if err != nil {
		t.Fatalf("DecryptPayload: %v", err)
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatalf("decodePayloadBytes: %v", err)
	}
	envelope, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
	if err != nil {
		t.Fatalf("ParseConvergentEnvelope: %v", err)
	}
	snapshot, err := domainsync.MaterializeConvergentSnapshot(envelope.State)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	variant := false
	for spelling := range spellings {
		if spelling != plugindata.JSONField {
			variant = true
		}
	}
	if !variant {
		return
	}
	for family, records := range snapshot.Families {
		if _, hasPluginData := convergentPluginDataKind(family); !hasPluginData {
			continue
		}
		for _, record := range records {
			properties, scanErr := plugindata.ScanPluginDataProperties(record.Value)
			if scanErr != nil {
				t.Fatalf("%s/%s: %v", family, record.ID, scanErr)
			}
			if properties.DuplicateRecordProperty {
				t.Fatalf("%s/%s repeats a pluginData property: %s",
					family, record.ID, record.Value)
			}
			for _, key := range properties.Keys {
				if key != plugindata.JSONField {
					t.Fatalf("%s/%s materializes the %q spelling: %s",
						family, record.ID, key, record.Value)
				}
			}
		}
	}
}

// interopVaultPayload reads the current Vault view. Unlike the last applied
// payload it is valid when a sync converges without a local Apply.
func interopVaultPayload(t *testing.T, vault *fakeVault) Payload {
	t.Helper()
	payload, err := vault.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Vault.Snapshot: %v", err)
	}
	return payload
}

// TestConvergentSyncRepairsRawForeignCaseVariantPluginData is the F2 blocker
// regression: a foreign v2 state that keeps a case-variant empty/null spelling
// alongside (or instead of) a canonical surviving container must be
// canonicalized in the CRDT state by the first sync. The published envelope must
// then match its own inline payload strictly, the sibling must survive, no
// spurious quarantine may appear for an empty/null variant, and the second and
// third syncs must write nothing.
func TestConvergentSyncRepairsRawForeignCaseVariantPluginData(t *testing.T) {
	good := interopF2GoodContainer(t)
	cases := []struct {
		name          string
		spellings     map[string]string
		wantNamespace bool
	}{
		{
			name:          "canonical-good-plus-variant-null",
			spellings:     map[string]string{"PluginData": "null", "pluginData": good},
			wantNamespace: true,
		},
		{
			name:          "canonical-good-plus-variant-empty",
			spellings:     map[string]string{"PLUGINDATA": "{}", "pluginData": good},
			wantNamespace: true,
		},
		{
			name:          "variant-good-only",
			spellings:     map[string]string{"PluginData": good},
			wantNamespace: true,
		},
		{
			name:      "variant-good-plus-canonical-null",
			spellings: map[string]string{"PluginData": good, "pluginData": "null"},
		},
		{
			name:      "variant-good-plus-canonical-empty",
			spellings: map[string]string{"PluginData": good, "pluginData": "{}"},
		},
	}
	for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
		for _, testCase := range cases {
			t.Run(family.family+"/"+testCase.name, func(t *testing.T) {
				remote := newFakeRemote()
				device, recording := newInteropRecordingDevice(t, remote, "device-a",
					family.payload())
				if _, err := device.service.SetConvergentEnabled(
					context.Background(), true); err != nil {
					t.Fatalf("enable convergent sync: %v", err)
				}
				state := device.state(t)
				foreign := interopStateWithRecordSpellings(t, state, "other-device",
					state.HLC.WallTime+1000, family.family, family.recordID,
					testCase.spellings)
				installRemoteContent(t, remote,
					interopForeignContentWithSpellings(t, foreign, family.family,
						family.recordID, testCase.spellings), 9000)

				result, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("a foreign case-variant state failed the sync: %v", err)
				}
				if result.Writes == 0 {
					t.Fatal("the canonical repair was not published")
				}
				applied := interopVaultPayload(t, device.vault)
				if !family.hasRecord(applied, family.siblingID) {
					t.Fatalf("valid sibling record was lost: %+v", applied)
				}
				pluginData := family.pluginDataOfID(t, applied, family.recordID)
				if testCase.wantNamespace {
					if len(pluginData) != 1 ||
						string(pluginData["acme.good"].Fields["name"]) != `"ok"` {
						t.Fatalf("surviving namespace = %+v", pluginData)
					}
				} else if len(pluginData) != 0 {
					t.Fatalf("no namespace may survive: %+v", pluginData)
				}
				if len(applied.PluginDataQuarantines) != 0 {
					t.Fatalf("an empty/null variant produced quarantines: %+v",
						applied.PluginDataQuarantines)
				}
				if surfaced := recording.PluginDataDiagnostics(); len(surfaced) != 0 {
					t.Fatalf("an empty/null variant produced diagnostics: %+v", surfaced)
				}
				interopAssertRemoteConvergentConsistent(t, remote)
				interopAssertRemoteMaterializedSpellingsCanonical(t, remote,
					testCase.spellings)

				for repeat := 2; repeat <= 3; repeat++ {
					repeated, repeatErr := device.service.Sync(
						context.Background(), TriggerManual)
					if repeatErr != nil {
						t.Fatalf("sync %d failed: %v", repeat, repeatErr)
					}
					if repeated.Writes != 0 {
						t.Fatalf("sync %d wrote %d times", repeat, repeated.Writes)
					}
				}
			})
		}
	}
}

// TestConvergentSyncHandlesRawForeignDuplicateAndWhitespaceSpellings closes the
// inline-payload half of the coverage gap: a foreign record may repeat the
// canonical property and pad the container with whitespace. With a canonical
// state the strict envelope comparison and a repeated sync must still agree;
// with a variant register in the state the same inline shape must repair once
// and then stay quiet.
func TestConvergentSyncHandlesRawForeignDuplicateAndWhitespaceSpellings(t *testing.T) {
	good := interopF2GoodContainer(t)
	cases := []struct {
		name           string
		stateSpellings map[string]string
		rawInline      string
		wantNamespace  bool
	}{
		{
			name:           "canonical-state-duplicate-and-whitespace",
			stateSpellings: map[string]string{"pluginData": good},
			rawInline: `"pluginData":{"acme.good":{"schemaVersion":1,"fields":{"name":"shadowed"}}},` +
				`"pluginData" : { "acme.good" : ` + interopGoodNamespace("ok") + ` }`,
			wantNamespace: true,
		},
		{
			name:           "variant-state-duplicate-and-whitespace",
			stateSpellings: map[string]string{"PluginData": "null", "pluginData": good},
			rawInline: `"pluginData":{"acme.good":{"schemaVersion":1,"fields":{"name":"shadowed"}}},` +
				`"PluginData" : null ,` +
				`"pluginData" : { "acme.good" : ` + interopGoodNamespace("ok") + ` }`,
			wantNamespace: true,
		},
	}
	for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
		for _, testCase := range cases {
			t.Run(family.family+"/"+testCase.name, func(t *testing.T) {
				remote := newFakeRemote()
				device, _ := newInteropRecordingDevice(t, remote, "device-a",
					family.payload())
				if _, err := device.service.SetConvergentEnabled(
					context.Background(), true); err != nil {
					t.Fatalf("enable convergent sync: %v", err)
				}
				state := device.state(t)
				foreign := interopStateWithRecordSpellings(t, state, "other-device",
					state.HLC.WallTime+1000, family.family, family.recordID,
					testCase.stateSpellings)
				installRemoteContent(t, remote,
					interopForeignContentWithRawInline(t, foreign, family.family,
						family.recordID, testCase.rawInline), 9000)

				result, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("a duplicate/whitespace inline record failed the sync: %v", err)
				}
				applied := interopVaultPayload(t, device.vault)
				pluginData := family.pluginDataOfID(t, applied, family.recordID)
				if testCase.wantNamespace {
					if len(pluginData) != 1 ||
						string(pluginData["acme.good"].Fields["name"]) != `"ok"` {
						t.Fatalf("surviving namespace = %+v", pluginData)
					}
				}
				interopAssertRemoteConvergentConsistent(t, remote)
				interopAssertRemoteMaterializedSpellingsCanonical(t, remote,
					testCase.stateSpellings)
				if len(applied.PluginDataQuarantines) != 0 {
					t.Fatalf("duplicate/whitespace shapes produced quarantines: %+v",
						applied.PluginDataQuarantines)
				}
				// A state that was already canonical must not be rewritten just
				// because its inline payload spelling was noisy.
				if len(testCase.stateSpellings) == 1 && result.Writes != 0 {
					t.Fatalf("a canonical state wrote %d times", result.Writes)
				}
				for repeat := 2; repeat <= 3; repeat++ {
					repeated, repeatErr := device.service.Sync(
						context.Background(), TriggerManual)
					if repeatErr != nil {
						t.Fatalf("sync %d failed: %v", repeat, repeatErr)
					}
					if repeated.Writes != 0 {
						t.Fatalf("sync %d wrote %d times", repeat, repeated.Writes)
					}
				}
			})
		}
	}
}

// TestConvergentEnableAcceptsRawForeignCaseVariantPluginData proves migration
// is not blocked by a case-variant spelling, initializes, and surfaces a
// quarantine only when a namespace is really malformed.
func TestConvergentEnableAcceptsRawForeignCaseVariantPluginData(t *testing.T) {
	good := interopF2GoodContainer(t)
	goodAndBad := `{"acme.bad":null,"acme.good":` + interopGoodNamespace("ok") + `}`
	cases := []struct {
		name         string
		spellings    map[string]string
		wantPluginID string
	}{
		{
			name:      "canonical-good-plus-variant-null",
			spellings: map[string]string{"PluginData": "null", "pluginData": good},
		},
		{
			name:      "canonical-good-plus-variant-empty",
			spellings: map[string]string{"PLUGINDATA": "{}", "pluginData": good},
		},
		{
			name:      "variant-good-only",
			spellings: map[string]string{"PluginData": good},
		},
		{
			name:      "variant-good-plus-canonical-null",
			spellings: map[string]string{"PluginData": good, "pluginData": "null"},
		},
		{
			name:         "canonical-malformed-plus-variant-null",
			spellings:    map[string]string{"PluginData": "null", "pluginData": goodAndBad},
			wantPluginID: "acme.bad",
		},
	}
	for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
		for _, testCase := range cases {
			t.Run(family.family+"/"+testCase.name, func(t *testing.T) {
				payload := family.payload()
				state := convergentStateForTest(t, payload, "other-device", 1000)
				foreign := interopStateWithRecordSpellings(t, state, "other-device",
					state.HLC.WallTime+1000, family.family, family.recordID,
					testCase.spellings)
				remote := newFakeRemote()
				installRemoteContent(t, remote,
					interopForeignContentWithSpellings(t, foreign, family.family,
						family.recordID, testCase.spellings), 9000)
				// The local Vault is the sanitized view of the same replica, so
				// migration exercises the spelling canonicalization rather than
				// the unrelated no-trusted-baseline guard (last-wins null/empty
				// legitimately hides the namespace from the materialized view).
				local, err := payloadFromState(foreign, nil, 1000, "other-device")
				if err != nil {
					t.Fatalf("payloadFromState: %v", err)
				}
				device, recording := newInteropRecordingDevice(t, remote, "device-a",
					local)

				preview, err := device.service.SetConvergentEnabled(
					context.Background(), true)
				if err != nil {
					t.Fatalf("migration against a case-variant remote failed: %v", err)
				}
				if !preview.CanInitialize {
					t.Fatalf("migration preview = %+v", preview)
				}
				for _, blocked := range preview.BlockedReasons {
					if blocked.Code == "envelope-materialized-mismatch" {
						t.Fatalf("migration reported %q: %+v",
							blocked.Code, preview.BlockedReasons)
					}
				}
				if testCase.wantPluginID != "" {
					if _, found := recording.diagnosticsFor(family.kind,
						family.recordID, testCase.wantPluginID); !found {
						t.Fatalf("migration diagnostics = %+v",
							recording.PluginDataDiagnostics())
					}
				} else if surfaced := recording.PluginDataDiagnostics(); len(surfaced) != 0 {
					t.Fatalf("empty/null variants produced diagnostics: %+v", surfaced)
				}
				interopAssertRemoteConvergentConsistent(t, remote)

				repeated, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("a repeated sync after migration failed: %v", err)
				}
				if repeated.Writes != 0 {
					t.Fatalf("a repeated sync after migration wrote %d times",
						repeated.Writes)
				}
			})
		}
	}
}

// TestConvergentStrategiesRepairRawForeignCaseVariantPluginData covers the
// smartMerge and preferCloud publication paths: both must canonicalize a
// foreign both-spellings state on the first sync and write nothing afterwards.
func TestConvergentStrategiesRepairRawForeignCaseVariantPluginData(t *testing.T) {
	good := interopF2GoodContainer(t)
	spellings := map[string]string{"PluginData": "null", "pluginData": good}
	for _, strategy := range []domainsync.Strategy{
		domainsync.StrategySmartMerge, domainsync.StrategyPreferCloud,
	} {
		for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
			t.Run(string(strategy)+"/"+family.family, func(t *testing.T) {
				remote := newFakeRemote()
				device, _ := newInteropRecordingDevice(t, remote, "device-a",
					family.payload())
				if _, err := device.service.SetConvergentEnabled(
					context.Background(), true); err != nil {
					t.Fatalf("enable convergent sync: %v", err)
				}
				if err := device.service.SetStrategy(context.Background(),
					strategy); err != nil {
					t.Fatalf("set strategy: %v", err)
				}
				state := device.state(t)
				foreign := interopStateWithRecordSpellings(t, state, "other-device",
					state.HLC.WallTime+1000, family.family, family.recordID,
					spellings)
				installRemoteContent(t, remote,
					interopForeignContentWithSpellings(t, foreign, family.family,
						family.recordID, spellings), 9000)

				result, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("%s sync failed: %v", strategy, err)
				}
				if result.Writes == 0 {
					t.Fatalf("%s did not publish the canonical repair", strategy)
				}
				applied := interopVaultPayload(t, device.vault)
				if !family.hasRecord(applied, family.siblingID) {
					t.Fatalf("valid sibling record was lost: %+v", applied)
				}
				pluginData := family.pluginDataOfID(t, applied, family.recordID)
				if len(pluginData) != 1 ||
					string(pluginData["acme.good"].Fields["name"]) != `"ok"` {
					t.Fatalf("surviving namespace = %+v", pluginData)
				}
				interopAssertRemoteConvergentConsistent(t, remote)
				interopAssertRemoteMaterializedSpellingsCanonical(t, remote, spellings)

				repeated, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("%s repeat failed: %v", strategy, err)
				}
				if repeated.Writes != 0 {
					t.Fatalf("%s repeat wrote %d times", strategy, repeated.Writes)
				}
			})
		}
	}
}

// TestConvergentCanonicalPluginDataDoesNotAllocateRepairWrite is the control:
// a genuinely canonical state must be recognized as already canonical, and
// repeated syncs must never allocate a repair write.
func TestConvergentCanonicalPluginDataDoesNotAllocateRepairWrite(t *testing.T) {
	for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
		t.Run(family.family, func(t *testing.T) {
			device, _ := newInteropRecordingDevice(t, newFakeRemote(), "device-a",
				family.payload())
			if _, err := device.service.SetConvergentEnabled(
				context.Background(), true); err != nil {
				t.Fatalf("enable convergent sync: %v", err)
			}
			state := device.state(t)
			next, changed, rejected, err := device.service.repairConvergentStatePluginData(
				state, "device-a")
			if err != nil {
				t.Fatalf("repair canonical state: %v", err)
			}
			if changed {
				t.Fatal("a canonical state allocated a repair write")
			}
			if len(rejected) != 0 {
				t.Fatalf("a canonical state produced diagnostics: %+v", rejected)
			}
			if !statesEqual(next, state) {
				t.Fatal("a canonical state was rewritten")
			}
			for attempt := 1; attempt <= 2; attempt++ {
				result, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("sync %d failed: %v", attempt, err)
				}
				if result.Writes != 0 {
					t.Fatalf("sync %d wrote %d times", attempt, result.Writes)
				}
			}
		})
	}
}
