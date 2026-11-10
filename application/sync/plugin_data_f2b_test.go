package sync

// F2b regression fixtures. The F2 fixtures cannot express this defect because
// they serialize the inline record through a Go map and domainsync.MarshalJSON,
// which sorts the keys: the inline view and the CRDT envelope then disagree
// about a duplicate pluginData spelling only by accident, if at all. Here the
// inline record is rebuilt by byte concatenation so encoding/json's
// document-order last-wins decode sees exactly the ordering a foreign replica
// can produce, while the envelope materialization keeps its sorted order.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// interopF2bRecordWithPluginData removes every pluginData-spelled property
// from record and appends the supplied raw property fragments in the order
// given. Non-pluginData properties keep their original bytes and document
// order; no Go map is involved in the record's reconstruction.
func interopF2bRecordWithPluginData(t *testing.T, record json.RawMessage,
	properties, extras []string) json.RawMessage {

	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(record))
	token, err := decoder.Token()
	if err != nil {
		t.Fatalf("decode record token: %v", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		t.Fatalf("record is not an object: %s", record)
	}
	parts := make([]string, 0, 8)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode record key: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			t.Fatalf("record property name is not a string: %s", record)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode record property %q: %v", key, err)
		}
		if plugindata.IsPluginDataProperty(key) {
			continue
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			t.Fatalf("encode record key %q: %v", key, err)
		}
		parts = append(parts, string(encodedKey)+":"+string(value))
	}
	if _, err := decoder.Token(); err != nil {
		t.Fatalf("decode record end: %v", err)
	}
	parts = append(parts, properties...)
	parts = append(parts, extras...)
	return json.RawMessage("{" + strings.Join(parts, ",") + "}")
}

// interopF2bReplaceRecord rewrites exactly one record of a family with the
// given pluginData property fragments, leaving every sibling record's bytes
// untouched.
func interopF2bReplaceRecord(t *testing.T, records []json.RawMessage, recordID string,
	properties, extras []string) []json.RawMessage {

	t.Helper()
	for index := range records {
		var identity struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(records[index], &identity); err != nil {
			t.Fatalf("decode record identity: %v", err)
		}
		if identity.ID != recordID {
			continue
		}
		records[index] = interopF2bRecordWithPluginData(t, records[index],
			properties, extras)
		return records
	}
	t.Fatalf("inline payload has no host/dataProfile record %q", recordID)
	return records
}

// interopF2bContent encrypts a v2 payload whose envelope state is foreign and
// whose inline family is rewritten by the caller. The family array is spliced
// into the plaintext by concatenating raw bytes, so duplicate or case-variant
// properties written by the rewrite survive in document order.
func interopF2bContent(t *testing.T, state domainsync.ConvergentState, family string,
	rewrite func([]json.RawMessage) []json.RawMessage) []byte {

	t.Helper()
	payload, err := payloadFromState(state, nil, 9000, "other-device")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	raw, err := domainsync.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("MarshalJSON payload: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		t.Fatalf("decode payload token: %v", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		t.Fatalf("payload is not an object")
	}
	parts := make([]string, 0, 16)
	replaced := false
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode payload key: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			t.Fatalf("payload property name is not a string")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode payload property %q: %v", key, err)
		}
		if key == family {
			var records []json.RawMessage
			if err := json.Unmarshal(value, &records); err != nil {
				t.Fatalf("decode %s records: %v", family, err)
			}
			records = rewrite(records)
			value, err = domainsync.MarshalJSON(records)
			if err != nil {
				t.Fatalf("encode %s records: %v", family, err)
			}
			replaced = true
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			t.Fatalf("encode payload key %q: %v", key, err)
		}
		parts = append(parts, string(encodedKey)+":"+string(value))
	}
	if _, err := decoder.Token(); err != nil {
		t.Fatalf("decode payload end: %v", err)
	}
	if !replaced {
		t.Fatalf("payload has no %s family", family)
	}
	envelope, err := domainsync.EncryptPayload(
		json.RawMessage("{"+strings.Join(parts, ",")+"}"),
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

// interopF2bPublishedSnapshot reads and materializes the published envelope.
func interopF2bPublishedSnapshot(t *testing.T, remote *fakeRemote) (domainsync.ConvergentState, domainsync.ConvergentSnapshot) {
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
	return envelope.State, snapshot
}

// interopF2bAssertRemoteStrict requires the published remote to satisfy the
// domain's strict envelope/snapshot agreement directly, and carries no
// case-variant or duplicate pluginData spelling in its state.
func interopF2bAssertRemoteStrict(t *testing.T, remote *fakeRemote) {
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
	materialized, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatalf("payloadSnapshot: %v", err)
	}
	if err := domainsync.EnvelopeMatchesSnapshot(envelope.State, materialized); err != nil {
		t.Fatalf("published remote is not strictly consistent: %v", err)
	}
	stateSnapshot, err := domainsync.MaterializeConvergentSnapshot(envelope.State)
	if err != nil {
		t.Fatalf("MaterializeConvergentSnapshot: %v", err)
	}
	for family, records := range stateSnapshot.Families {
		for _, record := range records {
			properties, err := plugindata.ScanPluginDataProperties(record.Value)
			if err != nil {
				t.Fatalf("%s/%s: %v", family, record.ID, err)
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

func interopF2bAssertNamespace(t *testing.T, payload Payload,
	family interopPluginDataFamily, want bool) {

	t.Helper()
	pluginData := family.pluginDataOfID(t, payload, family.recordID)
	if want {
		if len(pluginData) != 1 ||
			string(pluginData["acme.good"].Fields["name"]) != `"ok"` {
			t.Fatalf("surviving namespace = %+v", pluginData)
		}
		return
	}
	if len(pluginData) != 0 {
		t.Fatalf("no namespace may survive: %+v", pluginData)
	}
}

// interopF2bCase is one inline/envelope ordering shape.
type interopF2bCase struct {
	name           string
	stateSpellings map[string]string
	inlineProps    []string
	wantNamespace  bool
	wantWrites     bool
	wantPluginID   string
}

func interopF2bGoodLikeCases(good, goodAndBad string) []interopF2bCase {
	return []interopF2bCase{
		{
			name:           "canonical-good-plus-variant-null-variant-last",
			stateSpellings: map[string]string{"pluginData": good, "PluginData": "null"},
			inlineProps:    []string{`"pluginData":` + good, `"PluginData":null`},
			wantNamespace:  true,
			wantWrites:     true,
		},
		{
			name:           "canonical-good-plus-variant-empty-variant-last",
			stateSpellings: map[string]string{"pluginData": good, "PLUGINDATA": "{}"},
			inlineProps:    []string{`"pluginData":` + good, `"PLUGINDATA":{}`},
			wantNamespace:  true,
			wantWrites:     true,
		},
		{
			name:           "variant-good-plus-canonical-null-variant-last",
			stateSpellings: map[string]string{"PluginData": good, "pluginData": "null"},
			inlineProps:    []string{`"pluginData":null`, `"PluginData":` + good},
			wantNamespace:  false,
			wantWrites:     true,
		},
		{
			name:           "variant-good-plus-canonical-empty-variant-last",
			stateSpellings: map[string]string{"PluginData": good, "pluginData": "{}"},
			inlineProps:    []string{`"pluginData":{}`, `"PluginData":` + good},
			wantNamespace:  false,
			wantWrites:     true,
		},
		{
			name:           "canonical-good-plus-malformed-plus-variant-null-reversed",
			stateSpellings: map[string]string{"pluginData": goodAndBad, "PluginData": "null"},
			inlineProps:    []string{`"PluginData":null`, `"pluginData":` + goodAndBad},
			wantNamespace:  true,
			wantWrites:     true,
			wantPluginID:   "acme.bad",
		},
		{
			name:           "duplicate-canonical-key-null-last",
			stateSpellings: map[string]string{"pluginData": good},
			inlineProps:    []string{`"pluginData":` + good, `"pluginData":null`},
			wantNamespace:  true,
			wantWrites:     true,
		},
	}
}

func interopF2bGoodAndBadContainer() string {
	return `{"acme.bad":null,"acme.good":` + interopGoodNamespace("ok") + `}`
}

// TestConvergentSyncAcceptsDocumentOrderedPluginDataSpellings is the F2b
// regression: a foreign envelope and its inline copy can disagree about the
// winner among case-variant pluginData spellings because the inline decoder is
// document-ordered and the CRDT materializer is key-sorted. The sanitized CRDT
// state must win, the repair must publish a strictly consistent replica, and
// every subsequent sync must write nothing.
func TestConvergentSyncAcceptsDocumentOrderedPluginDataSpellings(t *testing.T) {
	good := interopF2GoodContainer(t)
	goodAndBad := interopF2bGoodAndBadContainer()
	cases := interopF2bGoodLikeCases(good, goodAndBad)
	strategies := []domainsync.Strategy{
		domainsync.StrategySmartMerge,
		domainsync.StrategyPreferCloud,
	}
	for _, strategy := range strategies {
		for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
			for _, testCase := range cases {
				t.Run(string(strategy)+"/"+family.family+"/"+testCase.name, func(t *testing.T) {
					remote := newFakeRemote()
					device, recording := newInteropRecordingDevice(t, remote, "device-a",
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
						testCase.stateSpellings)
					installRemoteContent(t, remote, interopF2bContent(t, foreign,
						family.family, func(records []json.RawMessage) []json.RawMessage {
							return interopF2bReplaceRecord(t, records, family.recordID,
								testCase.inlineProps, nil)
						}), 9000)

					result, err := device.service.Sync(context.Background(), TriggerManual)
					if err != nil {
						t.Fatalf("a document-ordered spelling shape failed the sync: %v", err)
					}
					if testCase.wantWrites {
						if result.Writes == 0 {
							t.Fatal("the canonical repair was not published")
						}
					} else if result.Writes != 0 {
						t.Fatalf("a canonical envelope wrote %d times", result.Writes)
					}
					applied := interopVaultPayload(t, device.vault)
					if !family.hasRecord(applied, family.siblingID) {
						t.Fatalf("valid sibling record was lost: %+v", applied)
					}
					interopF2bAssertNamespace(t, applied, family, testCase.wantNamespace)
					if testCase.wantPluginID != "" {
						if _, found := interopQuarantine(t, applied, family.kind,
							family.recordID, testCase.wantPluginID); !found {
							t.Fatalf("applied quarantine diagnostics = %+v",
								applied.PluginDataQuarantines)
						}
						if _, found := recording.diagnosticsFor(family.kind,
							family.recordID, testCase.wantPluginID); !found {
							t.Fatalf("sync diagnostics = %+v",
								recording.PluginDataDiagnostics())
						}
					} else if len(applied.PluginDataQuarantines) != 0 {
						t.Fatalf("spurious quarantines: %+v",
							applied.PluginDataQuarantines)
					} else if surfaced := recording.PluginDataDiagnostics(); len(surfaced) != 0 {
						t.Fatalf("spurious diagnostics: %+v", surfaced)
					}
					interopF2bAssertRemoteStrict(t, remote)

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
}

// TestConvergentEnableAcceptsDocumentOrderedPluginDataSpellings proves
// migration is not blocked by a document-ordered duplicate spelling: the
// preview initializes, the post-migration sync writes nothing, and the
// published replica is strictly consistent.
func TestConvergentEnableAcceptsDocumentOrderedPluginDataSpellings(t *testing.T) {
	good := interopF2GoodContainer(t)
	cases := interopF2bGoodLikeCases(good, interopF2bGoodAndBadContainer())
	// Only the reversed-order shapes are interesting for migration; the
	// duplicate-key shape has a canonical envelope and is covered above.
	cases = []interopF2bCase{cases[0], cases[2], cases[4]}
	for _, family := range interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload) {
		for _, testCase := range cases {
			t.Run(family.family+"/"+testCase.name, func(t *testing.T) {
				state := convergentStateForTest(t, family.payload(), "other-device", 1000)
				foreign := interopStateWithRecordSpellings(t, state, "other-device",
					state.HLC.WallTime+1000, family.family, family.recordID,
					testCase.stateSpellings)
				remote := newFakeRemote()
				installRemoteContent(t, remote, interopF2bContent(t, foreign,
					family.family, func(records []json.RawMessage) []json.RawMessage {
						return interopF2bReplaceRecord(t, records, family.recordID,
							testCase.inlineProps, nil)
					}), 9000)
				local, err := payloadFromState(foreign, nil, 1000, "other-device")
				if err != nil {
					t.Fatalf("payloadFromState: %v", err)
				}
				device, recording := newInteropRecordingDevice(t, remote, "device-a", local)

				preview, err := device.service.SetConvergentEnabled(
					context.Background(), true)
				if err != nil {
					t.Fatalf("migration against a document-ordered remote failed: %v", err)
				}
				if !preview.CanInitialize {
					t.Fatalf("migration preview = %+v", preview)
				}
				if preview.SchemaVersion != domainsync.ConvergentSchemaVersion {
					t.Fatalf("preview schema = %d", preview.SchemaVersion)
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
				}
				interopF2bAssertNamespace(t, interopVaultPayload(t, device.vault),
					family, testCase.wantNamespace)
				interopF2bAssertRemoteStrict(t, remote)

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

// newInteropRecordingDeviceWithConfigs rebuilds a Service over an existing
// config store, which is what Service.Load sees after an application restart.
func newInteropRecordingDeviceWithConfigs(t *testing.T, remote *fakeRemote, id string,
	configs *memoryConfigs, payload Payload) (*convergentDevice, *interopRecordingVault) {

	t.Helper()
	vault := &interopRecordingVault{fakeVault: &fakeVault{payload: payload}}
	tokens := &memoryTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	service := NewService(remote, vault, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithWaiter(&fakeWaiter{}),
		WithBackups(&fakeBackups{}),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDevice(id, "Device "+id, "1.0"),
		WithDebounce(10*time.Millisecond),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("Service.Load: %v", err)
	}
	if err := service.SetPassword(convergentTestPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	return &convergentDevice{
		service: service, remote: remote, vault: vault.fakeVault, configs: configs,
	}, vault
}

// TestConvergentDocumentOrderedPluginDataFreshDeviceAndReload publishes a
// repaired replica and proves a fresh device and a reopened service both agree
// with it immediately, with no repeat writes.
func TestConvergentDocumentOrderedPluginDataFreshDeviceAndReload(t *testing.T) {
	family := interopPluginDataFamilies(interopF2HostPayload, interopF2ProfilePayload)[0]
	good := interopF2GoodContainer(t)
	testCase := interopF2bGoodLikeCases(good, interopF2bGoodAndBadContainer())[0]

	remote := newFakeRemote()
	publisher, _ := newInteropRecordingDevice(t, remote, "device-a", family.payload())
	if _, err := publisher.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}
	state := publisher.state(t)
	foreign := interopStateWithRecordSpellings(t, state, "other-device",
		state.HLC.WallTime+1000, family.family, family.recordID, testCase.stateSpellings)
	installRemoteContent(t, remote, interopF2bContent(t, foreign, family.family,
		func(records []json.RawMessage) []json.RawMessage {
			return interopF2bReplaceRecord(t, records, family.recordID,
				testCase.inlineProps, nil)
		}), 9000)
	if _, err := publisher.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("publisher sync: %v", err)
	}
	interopF2bAssertRemoteStrict(t, remote)
	published := interopVaultPayload(t, publisher.vault)

	t.Run("fresh-device", func(t *testing.T) {
		fresh, _ := newInteropRecordingDevice(t, remote, "device-fresh", Payload{})
		preview, err := fresh.service.SetConvergentEnabled(context.Background(), true)
		if err != nil {
			t.Fatalf("fresh device migration: %v", err)
		}
		if !preview.CanInitialize {
			t.Fatalf("fresh device preview = %+v", preview)
		}
		applied := interopVaultPayload(t, fresh.vault)
		if !family.hasRecord(applied, family.recordID) ||
			!family.hasRecord(applied, family.siblingID) {
			t.Fatalf("fresh device did not adopt the published records: %+v", applied)
		}
		interopF2bAssertNamespace(t, applied, family, true)
		for attempt := 1; attempt <= 2; attempt++ {
			result, err := fresh.service.Sync(context.Background(), TriggerManual)
			if err != nil {
				t.Fatalf("fresh device sync %d: %v", attempt, err)
			}
			if result.Writes != 0 {
				t.Fatalf("fresh device sync %d wrote %d times", attempt, result.Writes)
			}
		}
	})

	t.Run("reopened-service", func(t *testing.T) {
		reopened, _ := newInteropRecordingDeviceWithConfigs(t, remote, "device-a",
			publisher.configs, published)
		agree := interopVaultPayload(t, reopened.vault)
		if !family.hasRecord(agree, family.siblingID) {
			t.Fatalf("reopened Vault lost the sibling: %+v", agree)
		}
		interopF2bAssertNamespace(t, agree, family, true)
		for attempt := 1; attempt <= 2; attempt++ {
			result, err := reopened.service.Sync(context.Background(), TriggerManual)
			if err != nil {
				t.Fatalf("reopened sync %d: %v", attempt, err)
			}
			if result.Writes != 0 {
				t.Fatalf("reopened sync %d wrote %d times", attempt, result.Writes)
			}
		}
	})
}

// TestConvergentInlineOnlyPluginDataAnomalyUsesCanonicalState covers round-5
// residual R2: when only the inline copy carries a malformed namespace or a
// later shadowing null, the canonical CRDT state must win, the sync must not
// fail permanently, and any payload-decode quarantine must be carried through.
func TestConvergentInlineOnlyPluginDataAnomalyUsesCanonicalState(t *testing.T) {
	good := interopF2GoodContainer(t)
	goodAndBad := interopF2bGoodAndBadContainer()
	cases := []struct {
		name           string
		inlineProps    []string
		wantNamespace  bool
		wantPluginID   string
		wantQuarantine bool
	}{
		{
			name:          "later-shadowing-null",
			inlineProps:   []string{`"pluginData":` + good, `"PluginData":null`},
			wantNamespace: true,
		},
		{
			name:           "malformed-namespace",
			inlineProps:    []string{`"pluginData":` + goodAndBad},
			wantNamespace:  true,
			wantPluginID:   "acme.bad",
			wantQuarantine: true,
		},
		{
			name:           "non-object-container",
			inlineProps:    []string{`"pluginData":"not-an-object"`},
			wantNamespace:  true,
			wantQuarantine: true,
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
				installRemoteContent(t, remote, interopF2bContent(t, state,
					family.family, func(records []json.RawMessage) []json.RawMessage {
						return interopF2bReplaceRecord(t, records, family.recordID,
							testCase.inlineProps, nil)
					}), 9000)

				result, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("an inline-only pluginData anomaly failed the sync: %v", err)
				}
				if result.Writes == 0 {
					t.Fatal("the provider was not rewritten with the canonical inline copy")
				}
				applied := interopVaultPayload(t, device.vault)
				interopF2bAssertNamespace(t, applied, family, testCase.wantNamespace)
				if testCase.wantQuarantine {
					if len(applied.PluginDataQuarantines) != 1 {
						t.Fatalf("payload decode quarantines = %+v",
							applied.PluginDataQuarantines)
					}
					rejected := applied.PluginDataQuarantines[0]
					if rejected.Kind != family.kind || rejected.RecordID != family.recordID {
						t.Fatalf("quarantine = %+v", rejected)
					}
					if testCase.wantPluginID != "" &&
						rejected.Quarantine.PluginID != testCase.wantPluginID {
						t.Fatalf("quarantined plugin = %q, want %q",
							rejected.Quarantine.PluginID, testCase.wantPluginID)
					}
					if _, found := recording.diagnosticsFor(family.kind,
						family.recordID, rejected.Quarantine.PluginID); !found {
						t.Fatalf("sync diagnostics = %+v",
							recording.PluginDataDiagnostics())
					}
				} else if len(applied.PluginDataQuarantines) != 0 {
					t.Fatalf("spurious quarantines: %+v", applied.PluginDataQuarantines)
				}
				interopF2bAssertRemoteStrict(t, remote)

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

// TestConvergentRejectsNonPluginDataDocumentMismatch is the negative control:
// the new tier only excuses a differing pluginData spelling. A genuine edit
// carried by the inline record, or a record present on one side only, must
// still fail closed with the original mismatch code.
func TestConvergentRejectsNonPluginDataDocumentMismatch(t *testing.T) {
	good := interopF2GoodContainer(t)
	cases := []struct {
		name    string
		rewrite func(t *testing.T, family interopPluginDataFamily,
			records []json.RawMessage) []json.RawMessage
	}{
		{
			name: "differing-label",
			rewrite: func(t *testing.T, family interopPluginDataFamily,
				records []json.RawMessage) []json.RawMessage {
				return interopF2bReplaceRecord(t, records, family.recordID,
					[]string{`"pluginData":` + good}, []string{`"label":"Tampered"`})
			},
		},
		{
			name: "record-present-in-envelope-only",
			rewrite: func(t *testing.T, family interopPluginDataFamily,
				records []json.RawMessage) []json.RawMessage {
				kept := make([]json.RawMessage, 0, len(records))
				for _, record := range records {
					var identity struct {
						ID string `json:"id"`
					}
					if err := json.Unmarshal(record, &identity); err != nil {
						t.Fatalf("decode record identity: %v", err)
					}
					if identity.ID == family.siblingID {
						continue
					}
					kept = append(kept, record)
				}
				return kept
			},
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
				installRemoteContent(t, remote, interopF2bContent(t, state,
					family.family, func(records []json.RawMessage) []json.RawMessage {
						return testCase.rewrite(t, family, records)
					}), 9000)

				read, _, err := device.service.readConvergentRemote(context.Background(),
					domainsync.Token{AccessToken: "token"})
				if err != nil {
					t.Fatalf("readConvergentRemote: %v", err)
				}
				if read.Kind != convergentRemoteInvalid {
					t.Fatalf("remote kind = %q, want invalid", read.Kind)
				}
				if read.Code != "envelope-materialized-mismatch" {
					t.Fatalf("remote code = %q, want envelope-materialized-mismatch",
						read.Code)
				}
				if read.Detail == "" {
					t.Fatal("remote mismatch carried no detail")
				}
				if testCase.name == "differing-label" &&
					!strings.Contains(read.Detail, "does not match the envelope") {
					t.Fatalf("remote detail = %q", read.Detail)
				}
			})
		}
	}
}

// TestSnapshotsEqualIgnoringPluginData pins both branches of the new
// predicate, including the one-sided record and family cases.
func TestSnapshotsEqualIgnoringPluginData(t *testing.T) {
	good := `{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}`
	canonical := json.RawMessage(`{"id":"h1","label":"One","pluginData":` + good + `}`)
	variant := json.RawMessage(`{"id":"h1","label":"One","PluginData":null}`)
	duplicate := json.RawMessage(`{"id":"h1","label":"One","pluginData":` +
		good + `,"pluginData":null}`)
	relabelled := json.RawMessage(`{"id":"h1","label":"Two","pluginData":` + good + `}`)
	other := json.RawMessage(`{"id":"h2","label":"Two","pluginData":` + good + `}`)
	plain := json.RawMessage(`{"id":"h1","label":"One"}`)
	nonObject := json.RawMessage(`null`)

	snapshot := func(records ...domainsync.ConvergentRecord) domainsync.ConvergentSnapshot {
		return domainsync.ConvergentSnapshot{
			Families: map[string][]domainsync.ConvergentRecord{"hosts": records},
		}
	}
	record := func(id string, value json.RawMessage) domainsync.ConvergentRecord {
		return domainsync.ConvergentRecord{ID: id, Value: value}
	}

	cases := []struct {
		name  string
		left  domainsync.ConvergentSnapshot
		right domainsync.ConvergentSnapshot
		want  bool
	}{
		{
			name: "identical",
			left: snapshot(record("h1", canonical)), right: snapshot(record("h1", canonical)),
			want: true,
		},
		{
			name: "spelling-and-order-only",
			left: snapshot(record("h1", canonical)), right: snapshot(record("h1", variant)),
			want: true,
		},
		{
			name: "duplicate-canonical-key-only",
			left: snapshot(record("h1", duplicate)), right: snapshot(record("h1", plain)),
			want: true,
		},
		{
			name: "non-plugindata-field-differs",
			left: snapshot(record("h1", canonical)), right: snapshot(record("h1", relabelled)),
			want: false,
		},
		{
			name:  "extra-record-on-right",
			left:  snapshot(record("h1", canonical)),
			right: snapshot(record("h1", canonical), record("h2", other)),
			want:  false,
		},
		{
			name:  "extra-record-on-left",
			left:  snapshot(record("h1", canonical), record("h2", other)),
			right: snapshot(record("h1", canonical)),
			want:  false,
		},
		{
			name:  "family-on-one-side-only",
			left:  snapshot(record("h1", canonical)),
			right: domainsync.ConvergentSnapshot{},
			want:  false,
		},
		{
			name: "record-value-not-an-object",
			left: snapshot(record("h1", canonical)), right: snapshot(record("h1", nonObject)),
			want: false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := snapshotsEqualIgnoringPluginData(testCase.left, testCase.right); got != testCase.want {
				t.Fatalf("snapshotsEqualIgnoringPluginData = %v, want %v", got, testCase.want)
			}
			if got := snapshotsEqualIgnoringPluginData(testCase.right, testCase.left); got != testCase.want {
				t.Fatalf("snapshotsEqualIgnoringPluginData(reversed) = %v, want %v", got, testCase.want)
			}
		})
	}
}
