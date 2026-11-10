package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// interopRecordingVault exposes the quarantine diagnostics the last Apply
// received, matching the adapters/syncvault PluginDataReporter contract the
// sync result reads.
type interopRecordingVault struct {
	*fakeVault
	diagnosticMu sync.Mutex
	diagnostics  []plugindata.Diagnostic
}

func (v *interopRecordingVault) Apply(ctx context.Context, payload Payload) error {
	if err := v.fakeVault.Apply(ctx, payload); err != nil {
		return err
	}
	diagnostics := make([]plugindata.Diagnostic, 0, len(payload.PluginDataQuarantines))
	for _, rejected := range payload.PluginDataQuarantines {
		quarantine := rejected.Quarantine
		diagnostics = append(diagnostics, plugindata.Diagnostic{
			RecordKind:    rejected.Kind,
			RecordID:      rejected.RecordID,
			PluginID:      quarantine.PluginID,
			SchemaVersion: quarantine.SchemaVersion,
			Fields:        quarantine.Fields,
			State:         plugindata.StateQuarantined,
			Reason:        quarantine.Reason,
			Detail:        quarantine.Detail,
		})
	}
	v.diagnosticMu.Lock()
	v.diagnostics = diagnostics
	v.diagnosticMu.Unlock()
	return nil
}

func (v *interopRecordingVault) PluginDataDiagnostics() []plugindata.Diagnostic {
	v.diagnosticMu.Lock()
	defer v.diagnosticMu.Unlock()
	return append([]plugindata.Diagnostic(nil), v.diagnostics...)
}

func (v *interopRecordingVault) diagnosticsFor(kind plugindata.RecordKind,
	recordID, pluginID string) (plugindata.Diagnostic, bool) {

	v.diagnosticMu.Lock()
	defer v.diagnosticMu.Unlock()
	for _, diagnostic := range v.diagnostics {
		if diagnostic.RecordKind == kind && diagnostic.RecordID == recordID &&
			diagnostic.PluginID == pluginID {
			return diagnostic, true
		}
	}
	return plugindata.Diagnostic{}, false
}

// newInteropRecordingDevice is newConvergentDevice with a Vault that reports
// the quarantine diagnostics the sync result exposes.
func newInteropRecordingDevice(t *testing.T, remote *fakeRemote, id string,
	payload Payload) (*convergentDevice, *interopRecordingVault) {

	t.Helper()
	vault := &interopRecordingVault{fakeVault: &fakeVault{payload: payload}}
	configs := &memoryConfigs{config: Config{
		Connected: true,
		Login:     "octocat",
		Strategy:  domainsync.StrategySmartMerge,
	}}
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
		t.Fatal(err)
	}
	if err := service.SetPassword(convergentTestPassword); err != nil {
		t.Fatal(err)
	}
	return &convergentDevice{
		service: service, remote: remote, vault: vault.fakeVault, configs: configs,
	}, vault
}

func interopF1HostPayload() Payload {
	return payloadWithHosts(
		interopHostWithPluginData("h1", "One",
			interopFieldPluginData(json.RawMessage(`"ok"`))),
		host("h2", "Two", "two.example"),
	)
}

func interopF1ProfilePayload() Payload {
	base := time.UnixMilli(1000)
	return Payload{
		DataProfiles: []domainplugin.DataProfile{
			{
				ID: "p1", Provider: "plugin.acme.data.db", Label: "p1",
				Mode:       domainplugin.DataModeReadOnly,
				PluginData: interopFieldPluginData(json.RawMessage(`"ok"`)),
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

// interopF1RewriteRecord removes every pluginData spelling from one record and
// writes the given spelling/container when spelling is not empty.
func interopF1RewriteRecord(t *testing.T, record json.RawMessage,
	spelling, container string) json.RawMessage {

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
	if spelling != "" {
		value, err := domainsync.CanonicalValue(json.RawMessage(container))
		if err != nil {
			t.Fatalf("canonicalize container: %v", err)
		}
		fields[spelling] = value
	}
	encoded, err := domainsync.MarshalJSON(fields)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	return encoded
}

// interopF1StateWithSpelling replaces one state record's pluginData with a raw
// spelling/container, preserving the normalized record's other fields.
func interopF1StateWithSpelling(t *testing.T, state domainsync.ConvergentState,
	node string, now int64, family, recordID, spelling, container string) domainsync.ConvergentState {

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
		value := interopF1RewriteRecord(t, records[index].Value, spelling, container)
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

func interopF1OverLimitContainer(t *testing.T) (string, string) {
	t.Helper()
	fields := map[string]json.RawMessage{}
	for index := 0; index < 5; index++ {
		fields[fmt.Sprintf("f%d", index)] = interopSizedField("x", 14<<10)
	}
	big := plugindata.Map{"acme.huge": {
		SchemaVersion: 1,
		Fields:        fields,
	}}
	encoded, err := plugindata.EncodeMap(big)
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	if len(encoded) <= plugindata.MaxRecordBytes {
		t.Fatalf("fixture = %d bytes, want above %d",
			len(encoded), plugindata.MaxRecordBytes)
	}
	return string(encoded), "acme.huge"
}

type interopF1Case struct {
	name         string
	spelling     string
	container    string
	wantPluginID string
	wantReason   plugindata.Reason
}

func interopF1Cases(t *testing.T) []interopF1Case {
	t.Helper()
	overLimit, overLimitPluginID := interopF1OverLimitContainer(t)
	cases := []interopF1Case{
		{
			name: "only-invalid-namespace", spelling: "pluginData",
			container:    `{"acme.bad":null}`,
			wantPluginID: "acme.bad", wantReason: plugindata.ReasonNamespace,
		},
		{
			name: "only-over-limit-namespace", spelling: "pluginData",
			container:    overLimit,
			wantPluginID: overLimitPluginID, wantReason: plugindata.ReasonRecordSize,
		},
		{name: "empty-object", spelling: "pluginData", container: `{}`},
		{name: "null", spelling: "pluginData", container: `null`},
		{name: "variant-null", spelling: "PluginData", container: `null`},
		{name: "variant-empty", spelling: "PLUGINDATA", container: `{}`},
	}
	return cases
}

// interopPluginDataFamily describes one Host or DataProfile family and how a
// test reads its records back out of an applied Vault payload.
type interopPluginDataFamily struct {
	family         string
	recordID       string
	siblingID      string
	payload        func() Payload
	kind           plugindata.RecordKind
	hasRecord      func(Payload, string) bool
	pluginDataOfID func(t *testing.T, payload Payload, id string) plugindata.Map
}

// interopPluginDataFamilies returns the Host and DataProfile descriptors with
// the caller's fixtures. The raw-foreign and apply-style regressions share it
// so the same shapes run against both record kinds.
func interopPluginDataFamilies(hostPayload, profilePayload func() Payload) []interopPluginDataFamily {
	return []interopPluginDataFamily{
		{
			family: "hosts", recordID: "h1", siblingID: "h2",
			payload: hostPayload, kind: plugindata.RecordHost,
			hasRecord: func(payload Payload, id string) bool {
				for _, item := range payload.Hosts {
					if string(item.ID) == id {
						return true
					}
				}
				return false
			},
			pluginDataOfID: func(t *testing.T, payload Payload, id string) plugindata.Map {
				t.Helper()
				for _, item := range payload.Hosts {
					if string(item.ID) == id {
						return item.PluginData
					}
				}
				t.Fatalf("hosts have no %q", id)
				return nil
			},
		},
		{
			family: "dataProfiles", recordID: "p1", siblingID: "p2",
			payload: profilePayload, kind: plugindata.RecordDataProfile,
			hasRecord: func(payload Payload, id string) bool {
				for _, item := range payload.DataProfiles {
					if item.ID == id {
						return true
					}
				}
				return false
			},
			pluginDataOfID: func(t *testing.T, payload Payload, id string) plugindata.Map {
				t.Helper()
				for _, item := range payload.DataProfiles {
					if item.ID == id {
						return item.PluginData
					}
				}
				t.Fatalf("profiles have no %q", id)
				return nil
			},
		},
	}
}

// TestConvergentSyncRepairsMalformedOnlyRemotePluginData is the F1 regression:
// one record whose pluginData has no surviving namespace must be quarantined
// per namespace exactly like a good+bad container, the valid sibling record
// must be applied, the sync must succeed, and a repeat must write nothing.
func TestConvergentSyncRepairsMalformedOnlyRemotePluginData(t *testing.T) {
	families := interopPluginDataFamilies(interopF1HostPayload, interopF1ProfilePayload)
	for _, family := range families {
		for _, testCase := range interopF1Cases(t) {
			t.Run(family.family+"/"+testCase.name, func(t *testing.T) {
				remote := newFakeRemote()
				device, recording := newInteropRecordingDevice(t, remote, "device-a",
					family.payload())
				if _, err := device.service.SetConvergentEnabled(
					context.Background(), true); err != nil {
					t.Fatalf("enable convergent sync: %v", err)
				}
				state := device.state(t)
				bad := interopF1StateWithSpelling(t, state, "other-device",
					state.HLC.WallTime+1000, family.family, family.recordID,
					testCase.spelling, testCase.container)
				installRemoteContent(t, remote,
					convergentContentForState(t, bad, 9000, "other-device"), 9000)
				appliedBefore := interopAppliedCount(device.vault)

				result, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("a malformed-only %s record failed the sync: %v",
						family.family, err)
				}
				applied := interopLastApplied(t, device.vault)
				if interopAppliedCount(device.vault) <= appliedBefore {
					t.Fatal("the quarantined materialization was not applied locally")
				}
				if !family.hasRecord(applied, family.siblingID) {
					t.Fatalf("valid sibling record was lost: %+v", applied)
				}
				pluginData := family.pluginDataOfID(t, applied, family.recordID)
				if len(pluginData) != 0 {
					t.Fatalf("no namespace may survive: %+v", pluginData)
				}
				if testCase.wantPluginID != "" {
					if _, survived := pluginData[testCase.wantPluginID]; survived {
						t.Fatalf("quarantined namespace %q survived: %+v",
							testCase.wantPluginID, pluginData)
					}
				}
				if testCase.wantPluginID != "" {
					entry, found := interopQuarantine(t, applied, family.kind,
						family.recordID, testCase.wantPluginID)
					if !found {
						t.Fatalf("applied quarantine diagnostics = %+v",
							applied.PluginDataQuarantines)
					}
					if entry.Reason != testCase.wantReason {
						t.Fatalf("quarantine = %+v, want reason %s",
							entry, testCase.wantReason)
					}
					diagnostic, found := recording.diagnosticsFor(family.kind,
						family.recordID, testCase.wantPluginID)
					if !found || diagnostic.Reason != testCase.wantReason ||
						diagnostic.State != plugindata.StateQuarantined {
						t.Fatalf("sync result diagnostics = %+v, found = %v",
							recording.PluginDataDiagnostics(), found)
					}
					resultFound := false
					for _, surfaced := range result.PluginDataDiagnostics {
						if surfaced.RecordKind == family.kind &&
							surfaced.RecordID == family.recordID &&
							surfaced.PluginID == testCase.wantPluginID &&
							surfaced.Reason == testCase.wantReason {
							resultFound = true
						}
					}
					if !resultFound {
						t.Fatalf("Sync.PluginDataDiagnostics = %+v",
							result.PluginDataDiagnostics)
					}
				} else {
					// An empty or null container holds nothing malformed, so
					// it must not fabricate a quarantine row.
					if len(applied.PluginDataQuarantines) != 0 {
						t.Fatalf("empty container produced quarantines: %+v",
							applied.PluginDataQuarantines)
					}
					if surfaced := recording.PluginDataDiagnostics(); len(surfaced) != 0 {
						t.Fatalf("empty container produced diagnostics: %+v", surfaced)
					}
				}
				interopAssertRemoteConvergentConsistent(t, remote)

				repeated, err := device.service.Sync(context.Background(), TriggerManual)
				if err != nil {
					t.Fatalf("a repeated sync failed: %v", err)
				}
				if repeated.Writes != 0 {
					t.Fatalf("a repeated sync wrote %d times", repeated.Writes)
				}
				if got := interopAppliedCount(device.vault); got != appliedBefore+1 {
					t.Fatalf("a repeated sync applied the Vault again: %d applies", got)
				}
			})
		}
	}
}

// interopF1Container encodes the fixture's surviving namespace so the raw
// foreign table can put the same canonical data on either property spelling.
func interopF1GoodContainer(t *testing.T) string {
	t.Helper()
	encoded, err := plugindata.EncodeMap(
		interopFieldPluginData(json.RawMessage(`"ok"`)))
	if err != nil {
		t.Fatalf("EncodeMap: %v", err)
	}
	return string(encoded)
}

// TestConvergentSyncHandlesRawForeignPluginDataEnvelope covers a replica this
// implementation did not produce: the envelope and the inline payload both
// carry the unsanitized container. The table includes the original
// canonical-spelling malformed-only shape, case-variant malformed-only
// spellings, and every combination of a case-variant empty/null register with
// a canonical surviving or shadowed container, for both record kinds.
func TestConvergentSyncHandlesRawForeignPluginDataEnvelope(t *testing.T) {
	good := interopF1GoodContainer(t)
	bad := `{"acme.bad":null}`
	goodAndBad := `{"acme.bad":null,"acme.data":{"schemaVersion":1,"fields":{"blob":"ok"}}}`
	cases := []struct {
		name          string
		spellings     map[string]string
		wantNamespace bool
		wantPluginID  string
	}{
		{
			name:         "canonical-malformed-only",
			spellings:    map[string]string{"pluginData": bad},
			wantPluginID: "acme.bad",
		},
		{
			name:         "variant-malformed-only",
			spellings:    map[string]string{"PluginData": bad},
			wantPluginID: "acme.bad",
		},
		{
			name:         "canonical-malformed-plus-variant-null",
			spellings:    map[string]string{"PluginData": "null", "pluginData": bad},
			wantPluginID: "acme.bad",
		},
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
			name:          "canonical-good-plus-malformed-and-variant-null",
			spellings:     map[string]string{"PluginData": "null", "pluginData": goodAndBad},
			wantNamespace: true,
			wantPluginID:  "acme.bad",
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
	for _, family := range interopPluginDataFamilies(interopF1HostPayload, interopF1ProfilePayload) {
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
					t.Fatalf("a raw foreign %s failed the sync: %v", testCase.name, err)
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
						string(pluginData["acme.data"].Fields["blob"]) != `"ok"` {
						t.Fatalf("surviving namespace = %+v", pluginData)
					}
				} else if len(pluginData) != 0 {
					t.Fatalf("no namespace may survive: %+v", pluginData)
				}
				if testCase.wantPluginID != "" {
					if _, found := interopQuarantine(t, applied, family.kind,
						family.recordID, testCase.wantPluginID); !found {
						t.Fatalf("quarantine diagnostics = %+v",
							applied.PluginDataQuarantines)
					}
					if _, found := recording.diagnosticsFor(family.kind,
						family.recordID, testCase.wantPluginID); !found {
						t.Fatalf("sync result diagnostics = %+v",
							recording.PluginDataDiagnostics())
					}
					resultFound := false
					for _, surfaced := range result.PluginDataDiagnostics {
						if surfaced.RecordKind == family.kind &&
							surfaced.RecordID == family.recordID &&
							surfaced.PluginID == testCase.wantPluginID {
							resultFound = true
						}
					}
					if !resultFound {
						t.Fatalf("Sync.PluginDataDiagnostics = %+v",
							result.PluginDataDiagnostics)
					}
				} else if len(applied.PluginDataQuarantines) != 0 {
					t.Fatalf("an empty/null variant produced quarantines: %+v",
						applied.PluginDataQuarantines)
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

// TestConvergentEnableAcceptsMalformedOnlyRemote is the SetConvergentEnabled
// half of F1: a quarantinable namespace must not block migration with
// envelope-materialized-mismatch.
func TestConvergentEnableAcceptsMalformedOnlyRemote(t *testing.T) {
	// The malformed namespace is the provider's only namespace, so the
	// quarantined remote materializes to the same records the local Vault
	// already holds. Migration must adopt that view instead of blocking.
	payload := payloadWithHosts(
		host("h1", "One", "one.example"),
		host("h2", "Two", "two.example"),
	)
	state := convergentStateForTest(t, payload, "other-device", 1000)
	bad := stateWithRecordPluginData(t, state, "other-device",
		state.HLC.WallTime+1000, "hosts", "h1", `{"acme.bad":null}`)
	remote := newFakeRemote()
	installRemoteContent(t, remote,
		convergentContentForState(t, bad, 9000, "other-device"), 9000)
	device, recording := newInteropRecordingDevice(t, remote, "device-a", payload)
	appliedBefore := interopAppliedCount(device.vault)

	preview, err := device.service.SetConvergentEnabled(context.Background(), true)
	if err != nil {
		t.Fatalf("migration against a malformed-only remote failed: %v", err)
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
	if interopAppliedCount(device.vault) <= appliedBefore {
		t.Fatal("migration did not apply the quarantined materialization")
	}
	if _, found := recording.diagnosticsFor(plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("migration diagnostics = %+v", recording.PluginDataDiagnostics())
	}
	applied := interopLastApplied(t, device.vault)
	if _, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("migration applied quarantines = %+v",
			applied.PluginDataQuarantines)
	}
	interopAssertRemoteConvergentConsistent(t, remote)

	repeated, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a repeated sync after migration failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated sync after migration wrote %d times", repeated.Writes)
	}
}

// TestConvergentPreferCloudRepairsLocalMalformedOnlyReplica covers the local
// replica half of F1: preferCloud must repair a persisted all-malformed state
// and converge instead of publishing an unusable envelope forever.
func TestConvergentPreferCloudRepairsLocalMalformedOnlyReplica(t *testing.T) {
	remote := newFakeRemote()
	device, recording := newInteropRecordingDevice(t, remote, "device-a",
		interopF1HostPayload())
	if _, err := device.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}
	if err := device.service.SetStrategy(context.Background(),
		domainsync.StrategyPreferCloud); err != nil {
		t.Fatalf("set preferCloud: %v", err)
	}
	state := device.state(t)
	bad := stateWithRecordPluginData(t, state, "device-a",
		state.HLC.WallTime+1000, "hosts", "h1", `{"acme.bad":null}`)
	blob, signature, err := device.service.sealConvergentState(bad)
	if err != nil {
		t.Fatalf("seal malformed local replica: %v", err)
	}
	config := device.service.currentConfig()
	config.Convergent.Replica = blob
	config.Convergent.ReplicaSignature = signature
	if err := device.service.saveConvergentConfig(config); err != nil {
		t.Fatalf("persist malformed local replica: %v", err)
	}
	appliedBefore := interopAppliedCount(device.vault)

	result, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("preferCloud with a local malformed-only replica failed: %v", err)
	}
	if result.Writes == 0 {
		t.Fatal("the repaired replica was not published")
	}
	applied := interopLastApplied(t, device.vault)
	if interopAppliedCount(device.vault) <= appliedBefore {
		t.Fatal("the repaired materialization was not applied locally")
	}
	byID := map[string]domainvault.Host{}
	for _, item := range applied.Hosts {
		byID[string(item.ID)] = item
	}
	if len(byID["h1"].PluginData) != 0 {
		t.Fatalf("h1 kept a namespace: %+v", byID["h1"].PluginData)
	}
	if _, ok := byID["h2"]; !ok {
		t.Fatalf("valid sibling record was lost: %+v", hostIDs(applied))
	}
	if _, found := interopQuarantine(t, applied, plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("local quarantine diagnostics = %+v",
			applied.PluginDataQuarantines)
	}
	if _, found := recording.diagnosticsFor(plugindata.RecordHost,
		"h1", "acme.bad"); !found {
		t.Fatalf("sync result diagnostics = %+v", recording.PluginDataDiagnostics())
	}
	interopAssertRemoteConvergentConsistent(t, remote)

	repeated, err := device.service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("a repeated preferCloud sync failed: %v", err)
	}
	if repeated.Writes != 0 {
		t.Fatalf("a repeated preferCloud sync wrote %d times", repeated.Writes)
	}
	if got := interopAppliedCount(device.vault); got != appliedBefore+1 {
		t.Fatalf("a repeated preferCloud sync applied the Vault again: %d applies", got)
	}
}
