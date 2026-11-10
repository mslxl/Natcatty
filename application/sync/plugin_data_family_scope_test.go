package sync

// Round-7 regression fixtures. snapshotsEqualIgnoringPluginData may only strip
// the pluginData property from the two families whose records define it
// (hosts and dataProfiles). The settings family is an object-shaped family
// whose record value is the settings document itself, so a document field
// named pluginData is real data, not an opaque container. Every typed family
// (for example portForwardingRules) cannot represent the property at all, so an
// envelope state that carries it is unrepairable. Both shapes used to be
// accepted as spelling-only mismatches, which silently rewrote the provider
// forever.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// TestSnapshotsEqualIgnoringPluginDataIsFamilyScoped pins the predicate's
// family scoping. Spelling-only differences are excused only for Host and
// DataProfile records; a settings document field or a typed record carrying a
// top-level pluginData property is a real mismatch.
func TestSnapshotsEqualIgnoringPluginDataIsFamilyScoped(t *testing.T) {
	snapshot := func(family, id string, value json.RawMessage) domainsync.ConvergentSnapshot {
		return domainsync.ConvergentSnapshot{
			Families: map[string][]domainsync.ConvergentRecord{
				family: {{ID: id, Value: value}},
			},
		}
	}

	hostGood := json.RawMessage(`{"id":"h1","label":"One","pluginData":{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}}`)
	hostVariant := json.RawMessage(`{"id":"h1","label":"One","PluginData":null}`)
	profileGood := json.RawMessage(`{"id":"p1","label":"P","pluginData":{"acme.good":{"schemaVersion":1,"fields":{"name":"ok"}}}}`)
	profileVariant := json.RawMessage(`{"id":"p1","label":"P","PLUGINDATA":{}}`)
	settingsOne := json.RawMessage(`{"pluginData":{"x":1},"theme":"dark"}`)
	settingsTwo := json.RawMessage(`{"pluginData":{"x":2},"theme":"dark"}`)
	ruleWithProperty := json.RawMessage(`{"id":"r1","label":"R","pluginData":{"x":1}}`)
	rulePlain := json.RawMessage(`{"id":"r1","label":"R"}`)
	ruleRelabelled := json.RawMessage(`{"id":"r1","label":"R2"}`)

	cases := []struct {
		name  string
		left  domainsync.ConvergentSnapshot
		right domainsync.ConvergentSnapshot
		want  bool
	}{
		{
			name:  "hosts-spelling-only-remains-excused",
			left:  snapshot("hosts", "h1", hostGood),
			right: snapshot("hosts", "h1", hostVariant),
			want:  true,
		},
		{
			name:  "dataProfiles-spelling-only-remains-excused",
			left:  snapshot("dataProfiles", "p1", profileGood),
			right: snapshot("dataProfiles", "p1", profileVariant),
			want:  true,
		},
		{
			name:  "settings-document-pluginData-field-differs",
			left:  snapshot("settings", settingsKeyPreferences, settingsOne),
			right: snapshot("settings", settingsKeyPreferences, settingsTwo),
			want:  false,
		},
		{
			name:  "settings-document-identical",
			left:  snapshot("settings", settingsKeyPreferences, settingsOne),
			right: snapshot("settings", settingsKeyPreferences, settingsOne),
			want:  true,
		},
		{
			name:  "typed-family-pluginData-property-only-differs",
			left:  snapshot("portForwardingRules", "r1", ruleWithProperty),
			right: snapshot("portForwardingRules", "r1", rulePlain),
			want:  false,
		},
		{
			name:  "typed-family-identical",
			left:  snapshot("portForwardingRules", "r1", rulePlain),
			right: snapshot("portForwardingRules", "r1", rulePlain),
			want:  true,
		},
		{
			name:  "typed-family-real-field-differs",
			left:  snapshot("portForwardingRules", "r1", rulePlain),
			right: snapshot("portForwardingRules", "r1", ruleRelabelled),
			want:  false,
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

// familyScopeObjectContent builds v2 ciphertext from a state, rewriting one
// record of an object-shaped family in the inline payload while leaving the
// envelope state untouched. interopF2bContent only handles array families.
func familyScopeObjectContent(t *testing.T, state domainsync.ConvergentState,
	family, recordID string, rewrite func(json.RawMessage) json.RawMessage) []byte {

	t.Helper()
	payload, err := payloadFromState(state, nil, 9000, "other-device")
	if err != nil {
		t.Fatalf("payloadFromState: %v", err)
	}
	raw, err := domainsync.MarshalJSON(payload)
	if err != nil {
		t.Fatalf("MarshalJSON payload: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(top[family], &object); err != nil {
		t.Fatalf("decode %s object: %v", family, err)
	}
	value, exists := object[recordID]
	if !exists {
		t.Fatalf("inline payload has no %s record %q", family, recordID)
	}
	object[recordID] = rewrite(value)
	top[family], err = domainsync.MarshalJSON(object)
	if err != nil {
		t.Fatalf("encode %s object: %v", family, err)
	}
	raw, err = domainsync.MarshalJSON(top)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	envelope, err := domainsync.EncryptPayload(json.RawMessage(raw), convergentTestPassword,
		domainsync.EncryptOptions{
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
	// The fixture must be readable before it is installed; otherwise the
	// assertion below would only observe a broken builder.
	file, err := domainsync.UnmarshalFile(content)
	if err != nil {
		t.Fatalf("built content does not unmarshal: %v", err)
	}
	plaintext, err := domainsync.DecryptPayload(file, convergentTestPassword)
	if err != nil {
		t.Fatalf("built content does not decrypt: %v", err)
	}
	if _, err := decodePayloadBytes(plaintext); err != nil {
		t.Fatalf("built plaintext does not decode: %v: %s", err, plaintext)
	}
	return content
}

// familyScopeRemoteBytes returns the provider bytes and total write count.
func familyScopeRemoteBytes(t *testing.T, remote *fakeRemote) ([]byte, int) {
	t.Helper()
	remote.mu.Lock()
	defer remote.mu.Unlock()
	return append([]byte(nil), remote.file.Content...), len(remote.writeLog)
}

// familyScopeInlineSettings returns one inline settings document straight from
// the provider ciphertext.
func familyScopeInlineSettings(t *testing.T, remote *fakeRemote, recordID string) json.RawMessage {
	t.Helper()
	content, _ := familyScopeRemoteBytes(t, remote)
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
	value, exists := payload.Settings[recordID]
	if !exists {
		t.Fatalf("provider inline settings have no %q document", recordID)
	}
	return append(json.RawMessage(nil), value...)
}

func familyScopeDecodeX(t *testing.T, document json.RawMessage) int {
	t.Helper()
	var fields struct {
		PluginData struct {
			X int `json:"x"`
		} `json:"pluginData"`
	}
	if err := json.Unmarshal(document, &fields); err != nil {
		t.Fatalf("decode settings document %s: %v", document, err)
	}
	return fields.PluginData.X
}

// TestConvergentRejectsSettingsDocumentPluginDataMismatch is finding 1: a
// settings document field literally named pluginData is document data. The
// state must not be accepted over a differing inline copy just because the
// field happens to share the opaque property's name.
func TestConvergentRejectsSettingsDocumentPluginDataMismatch(t *testing.T) {
	const recordID = settingsKeyPreferences
	inlineDocument := json.RawMessage(`{"pluginData":{"x":2},"theme":"dark"}`)
	stateDocument := json.RawMessage(`{"pluginData":{"x":1},"theme":"dark"}`)

	remote := newFakeRemote()
	local := Payload{Settings: map[string]json.RawMessage{
		recordID: append(json.RawMessage(nil), inlineDocument...),
	}}
	device, _ := newInteropRecordingDevice(t, remote, "device-a", local)
	if _, err := device.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}

	state := device.state(t)
	foreign := interopStateWithRecordSpellings(t, state, "other-device",
		state.HLC.WallTime+1000, "settings", recordID,
		map[string]string{"pluginData": `{"x":1}`})
	content := familyScopeObjectContent(t, foreign, "settings", recordID,
		func(json.RawMessage) json.RawMessage { return inlineDocument })
	installRemoteContent(t, remote, content, 9000)
	before, writesBefore := familyScopeRemoteBytes(t, remote)
	if familyScopeDecodeX(t, stateDocument) != 1 {
		t.Fatalf("fixture state document = %s, want x=1", stateDocument)
	}

	read, _, err := device.service.readConvergentRemote(context.Background(),
		domainsync.Token{AccessToken: "token"})
	if err != nil {
		t.Fatalf("readConvergentRemote: %v", err)
	}
	if read.Kind != convergentRemoteInvalid {
		t.Fatalf("remote kind = %q, want invalid (detail %q)", read.Kind, read.Detail)
	}
	if read.Code != "envelope-materialized-mismatch" {
		t.Fatalf("remote code = %q, want envelope-materialized-mismatch (detail %q)",
			read.Code, read.Detail)
	}
	if read.PayloadMismatch {
		t.Fatal("a differing settings document was accepted as a spelling mismatch")
	}

	assertBlocked := func(attempt int) {
		t.Helper()
		result, syncErr := device.service.Sync(context.Background(), TriggerManual)
		if !errors.Is(syncErr, ErrConvergentCorrupt) {
			t.Fatalf("sync %d error = %v, want ErrConvergentCorrupt", attempt, syncErr)
		}
		if result.Writes != 0 {
			t.Fatalf("sync %d wrote %d times", attempt, result.Writes)
		}
		after, writes := familyScopeRemoteBytes(t, remote)
		if !bytes.Equal(after, before) || writes != writesBefore {
			t.Fatalf("sync %d touched the provider (writes %d -> %d)",
				attempt, writesBefore, writes)
		}
		inline := familyScopeInlineSettings(t, remote, recordID)
		if got := familyScopeDecodeX(t, inline); got != 2 {
			t.Fatalf("sync %d published inline x=%d, want the untouched x=2",
				attempt, got)
		}
	}

	assertBlocked(1)
	assertBlocked(2)
	assertBlocked(3)

	// The same reasoning applies to the local Vault: the state's x=1 must never
	// be applied as if the two views had been reconciled.
	applied := interopVaultPayload(t, device.vault)
	if got := familyScopeDecodeX(t, applied.Settings[recordID]); got != 2 {
		t.Fatalf("Vault settings x=%d, want the untouched x=2", got)
	}
}

// TestConvergentRejectsTypedFamilyPluginDataProperty is finding 2: a typed
// family cannot represent a top-level pluginData property, and the repair only
// canonicalizes Host and DataProfile records. Accepting the shape forced a
// provider rewrite on every verification round. It must fail closed instead.
func TestConvergentRejectsTypedFamilyPluginDataProperty(t *testing.T) {
	payload := Payload{
		PortForwardingRules: []domainforward.Rule{{
			ID: "r1", Label: "rule one", Type: domainforward.TypeLocal,
			BindAddress: "127.0.0.1", LocalPort: 8080,
			RemoteHost: "db.internal", RemotePort: 5432, HostID: "h1",
			OrderKey: 1000,
		}},
	}
	strategies := []domainsync.Strategy{
		domainsync.StrategySmartMerge,
		domainsync.StrategyPreferCloud,
		domainsync.StrategySmartMergePreserve,
	}
	for _, strategy := range strategies {
		t.Run(string(strategy), func(t *testing.T) {
			remote := newFakeRemote()
			device, _ := newInteropRecordingDevice(t, remote, "device-a", payload)
			if _, err := device.service.SetConvergentEnabled(context.Background(), true); err != nil {
				t.Fatalf("enable convergent sync: %v", err)
			}
			if err := device.service.SetStrategy(context.Background(), strategy); err != nil {
				t.Fatalf("set strategy: %v", err)
			}

			state := device.state(t)
			foreign := interopStateWithRecordSpellings(t, state, "other-device",
				state.HLC.WallTime+1000, "portForwardingRules", "r1",
				map[string]string{"pluginData": `{"x":1}`})
			installRemoteContent(t, remote,
				interopF2bContent(t, foreign, "portForwardingRules",
					func(records []json.RawMessage) []json.RawMessage { return records }),
				9000)
			before, writesBefore := familyScopeRemoteBytes(t, remote)

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
			if read.PayloadMismatch {
				t.Fatal("an unrepairable typed-family property was accepted")
			}

			for attempt := 1; attempt <= 3; attempt++ {
				result, syncErr := device.service.Sync(context.Background(), TriggerManual)
				if !errors.Is(syncErr, ErrConvergentCorrupt) {
					t.Fatalf("sync %d error = %v, want ErrConvergentCorrupt",
						attempt, syncErr)
				}
				if result.Writes != 0 {
					t.Fatalf("sync %d wrote %d times", attempt, result.Writes)
				}
				after, writes := familyScopeRemoteBytes(t, remote)
				if !bytes.Equal(after, before) || writes != writesBefore {
					t.Fatalf("sync %d touched the provider (writes %d -> %d)",
						attempt, writesBefore, writes)
				}
			}
		})
	}
}
