package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

const convergentTestPassword = "secret"

func installRemoteContent(t *testing.T, remote *fakeRemote, content []byte, updatedAt int64) {
	t.Helper()
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.file = RemoteFile{
		ID:        "gist-1",
		ETag:      "etag-installed",
		Content:   append([]byte(nil), content...),
		UpdatedAt: updatedAt,
	}
	remote.exists = true
}

func encryptedContentForTest(t *testing.T, payload Payload, schema int, updatedAt int64) []byte {
	t.Helper()
	envelope, err := domainsync.EncryptPayload(payload, convergentTestPassword, domainsync.EncryptOptions{
		Version:           1,
		UpdatedAt:         updatedAt,
		DeviceID:          "other-device",
		DeviceName:        "Other Device",
		AppVersion:        "1.0",
		KDFIterations:     testKDFIterations,
		SyncSchemaVersion: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	content, err := domainsync.MarshalFile(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func convergentStateForTest(t *testing.T, payload Payload, node string, now int64) domainsync.ConvergentState {
	t.Helper()
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}
	state, err := domainsync.CreateConvergentStateFromSnapshot(snapshot, node, now)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func convergentContentForState(t *testing.T, state domainsync.ConvergentState, updatedAt int64, deviceID string) []byte {
	t.Helper()
	payload, err := payloadFromState(state, nil, updatedAt, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	return encryptedContentForTest(t, payload, 2, updatedAt)
}

func mutateStateValue(t *testing.T, state domainsync.ConvergentState) domainsync.ConvergentState {
	t.Helper()
	clone, err := domainsync.NormalizeConvergentState(state)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the Host label deterministically. Picking the first string field
	// in map order can hit an RFC 3339 timestamp, which would make the test
	// fail during materialization instead of exercising divergence.
	collection, ok := clone.Collections["hosts"]
	if !ok {
		t.Fatal("the convergent state has no hosts collection")
	}
	entity, ok := collection.Entities["host-1"]
	if !ok {
		t.Fatal("the convergent state has no host-1")
	}
	register, ok := entity.Fields["label"]
	if !ok || len(register.Candidates) == 0 {
		t.Fatal("host-1 has no label candidate")
	}
	mutated := false
	for index := range register.Candidates {
		candidate := &register.Candidates[index]
		if candidate.Tombstone {
			continue
		}
		candidate.Value = json.RawMessage(`"tampered"`)
		mutated = true
		break
	}
	if !mutated {
		t.Fatal("host-1 has no live label candidate")
	}
	entity.Fields["label"] = register
	collection.Entities["host-1"] = entity
	clone.Collections["hosts"] = collection
	if err := domainsync.ValidateConvergentState(clone); err != nil {
		t.Fatalf("tampered state is invalid: %v", err)
	}
	return clone
}

func migrateToConvergent(t *testing.T, service *Service) ConvergentMigrationPreview {
	t.Helper()
	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if err != nil {
		t.Fatalf("enable convergent sync: %v", err)
	}
	if !preview.CanInitialize {
		t.Fatalf("preview = %+v", preview)
	}
	return preview
}

func hostIDs(payload Payload) []string {
	ids := make([]string, 0, len(payload.Hosts))
	for _, item := range payload.Hosts {
		ids = append(ids, string(item.ID))
	}
	return ids
}

func hasHostID(payload Payload, id string) bool {
	for _, item := range payload.Hosts {
		if string(item.ID) == id {
			return true
		}
	}
	return false
}

func TestConvergentEnableMigratesV1AndRepeatedSyncWritesNothing(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)

	preview := migrateToConvergent(t, service)
	if !preview.CanInitialize || preview.EntityCount != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	if backups.createdCount() == 0 {
		t.Fatal("migration did not create a protective backup")
	}
	config := configs.config
	if config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion || !config.Convergent.Enabled {
		t.Fatalf("convergent config = %+v", config.Convergent)
	}
	if config.Convergent.Replica == "" || config.Convergent.ReplicaSignature == "" {
		t.Fatal("migration did not persist a sealed replica")
	}
	if config.Convergent.VerifiedPayload == "" || config.Convergent.VerifiedSignature == "" {
		t.Fatal("migration did not persist a verified payload after the first sync")
	}
	info := service.Snapshot().Convergent
	if info.Phase != "converged" || info.Round != 3 || info.RoundKind != ConvergentRoundState || info.Outcome != "converged" {
		t.Fatalf("convergent info = %+v", info)
	}
	if len(info.Rounds) != ConvergentVerifyRounds {
		t.Fatalf("rounds = %+v", info.Rounds)
	}
	for _, round := range info.Rounds {
		if round.Outcome != "verified" {
			t.Fatalf("round %+v did not verify", round)
		}
	}
	remotePayload := remote.payload(t, convergentTestPassword)
	if !hasHostID(remotePayload, "host-1") {
		t.Fatalf("remote hosts = %v", hostIDs(remotePayload))
	}
	if remotePayload.ConvergentSync == nil {
		t.Fatal("remote payload has no convergent envelope")
	}
	for _, mark := range []struct{ schema int }{{2}} {
		envelope, err := domainsync.UnmarshalFile(remote.file.Content)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Meta.SyncSchemaVersion != mark.schema {
			t.Fatalf("remote schema = %d", envelope.Meta.SyncSchemaVersion)
		}
	}

	remote.mu.Lock()
	before := append([]byte(nil), remote.file.Content...)
	writesBefore := len(remote.writeLog)
	remote.mu.Unlock()
	vault.mu.Lock()
	appliedBefore := len(vault.applied)
	vault.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("repeated sync: %v", err)
	}
	if result.Writes != 0 || result.Uploaded || result.Action != domainsync.ActionNone {
		t.Fatalf("repeated sync result = %+v", result)
	}
	remote.mu.Lock()
	after := append([]byte(nil), remote.file.Content...)
	writesAfter := len(remote.writeLog)
	remote.mu.Unlock()
	if writesAfter != writesBefore {
		t.Fatalf("repeated sync wrote the provider: %d -> %d", writesBefore, writesAfter)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("repeated sync changed the provider bytes")
	}
	vault.mu.Lock()
	appliedAfter := len(vault.applied)
	vault.mu.Unlock()
	if appliedAfter != appliedBefore {
		t.Fatalf("repeated sync rewrote the Vault: %d -> %d", appliedBefore, appliedAfter)
	}
	repeated := service.Snapshot().Convergent
	if repeated.Writes != 0 || repeated.Phase != "converged" || repeated.Round != ConvergentVerifyRounds {
		t.Fatalf("repeated convergent info = %+v", repeated)
	}
}

func TestConvergentEnableAdoptsMatchingV1Payload(t *testing.T) {
	service, remote, _, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	remotePayload := payloadWithHost("local", 9000)
	installRemoteContent(t, remote, encryptedContentForTest(t, remotePayload, 1, 9000), 9000)

	preview := migrateToConvergent(t, service)
	if preview.ProviderSchemaVersion != 1 {
		t.Fatalf("provider schema = %d", preview.ProviderSchemaVersion)
	}
	if configs.config.Convergent.SchemaVersion != 2 {
		t.Fatal("v2 marker was not written")
	}
	upgraded := remote.payload(t, convergentTestPassword)
	if upgraded.ConvergentSync == nil {
		t.Fatal("matching v1 payload was not upgraded to v2")
	}
	if len(upgraded.Hosts) != 1 || upgraded.Hosts[0].Label != "local" {
		t.Fatalf("upgraded hosts = %+v", upgraded.Hosts)
	}
}

func TestConvergentMigrationImportsLegacyTombstones(t *testing.T) {
	service, remote, _, _ := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	remotePayload := payloadWithHost("local", 9000)
	remotePayload.SyncMeta = &SyncMeta{
		SchemaVersion: 1,
		Deletions: []SyncDeletion{
			{EntityType: "hosts", ID: "gone", DeletedAt: 8000, DeviceID: "legacy"},
			// A deletion for an entity the payload still carries is stale;
			// importing it would delete a live record.
			{EntityType: "hosts", ID: "host-1", DeletedAt: 8500, DeviceID: "legacy"},
		},
	}
	installRemoteContent(t, remote, encryptedContentForTest(t, remotePayload, 1, 9000), 9000)

	migrateToConvergent(t, service)
	upgraded := remote.payload(t, convergentTestPassword)
	if !hasHostID(upgraded, "host-1") {
		t.Fatal("legacy migration dropped the live host")
	}
	if upgraded.SyncMeta == nil {
		t.Fatal("upgraded payload has no syncMeta")
	}
	found := map[string]int64{}
	for _, deletion := range upgraded.SyncMeta.Deletions {
		found[deletion.ID] = deletion.DeletedAt
	}
	if _, ok := found["host-1"]; ok {
		t.Fatal("stale deletion for a live host was imported")
	}
	if found["gone"] != 8000 {
		t.Fatalf("legacy tombstone = %+v, want gone@8000", upgraded.SyncMeta.Deletions)
	}
	envelope, err := domainsync.ParseConvergentEnvelope(upgraded.ConvergentSync)
	if err != nil {
		t.Fatalf("parse migrated envelope: %v", err)
	}
	entity, ok := envelope.State.Collections["hosts"].Entities["gone"]
	if !ok {
		t.Fatal("legacy tombstone is absent from the v2 state")
	}
	winner, ok := domainsync.SelectRegisterWinner(entity.Presence)
	if !ok || !winner.Tombstone {
		t.Fatalf("legacy tombstone winner = %+v present=%v", winner, ok)
	}
}

func TestConvergentMigrationMergesTrustedV1Baseline(t *testing.T) {
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("establish v1 baseline: %v", err)
	}
	vault.set(payloadWithHosts(
		host("h1", "Base", "base.example"),
		host("h2", "Local only", "local.example"),
	))
	remotePayload := payloadWithHosts(
		host("h1", "Base", "base.example"),
		host("h3", "Remote only", "remote.example"),
	)
	installRemoteContent(t, remote, encryptedContentForTest(t, remotePayload, 1, 9000), 9000)

	migrateToConvergent(t, service)
	merged := remote.payload(t, convergentTestPassword)
	for _, id := range []string{"h1", "h2", "h3"} {
		if !hasHostID(merged, id) {
			t.Fatalf("merged remote is missing %s: %v", id, hostIDs(merged))
		}
	}
	vault.mu.Lock()
	local := clonePayload(vault.payload)
	vault.mu.Unlock()
	for _, id := range []string{"h1", "h2", "h3"} {
		if !hasHostID(local, id) {
			t.Fatalf("merged local is missing %s: %v", id, hostIDs(local))
		}
	}
	if configs.config.Convergent.SchemaVersion != 2 {
		t.Fatal("v2 marker was not written")
	}
}

func TestConvergentMigrationRefusesWithoutTrustedBaseline(t *testing.T) {
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	vault.set(payloadWithHosts(host("local", "Local", "local.example")))
	remotePayload := payloadWithHosts(host("remote", "Remote", "remote.example"))
	content := encryptedContentForTest(t, remotePayload, 1, 9000)
	installRemoteContent(t, remote, content, 9000)

	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("error = %v", err)
	}
	if preview.CanInitialize || len(preview.BlockedReasons) == 0 || preview.BlockedReasons[0].Code != "no-trusted-baseline" {
		t.Fatalf("preview = %+v", preview)
	}
	if configs.config.Convergent.SchemaVersion != 0 {
		t.Fatal("a refused migration wrote the v2 marker")
	}
	remote.mu.Lock()
	after := append([]byte(nil), remote.file.Content...)
	writes := len(remote.writeLog)
	remote.mu.Unlock()
	if !bytes.Equal(after, content) || writes != 0 {
		t.Fatalf("refused migration touched the provider (writes=%d)", writes)
	}
	vault.mu.Lock()
	applied := len(vault.applied)
	vault.mu.Unlock()
	if applied != 0 {
		t.Fatal("refused migration changed the Vault")
	}
	snapshot := service.Snapshot()
	if snapshot.Problem != ProblemConvergentMigration || snapshot.Convergent.Phase != "blocked" {
		t.Fatalf("snapshot = problem %q, info %+v", snapshot.Problem, snapshot.Convergent)
	}
}

func TestConvergentMigrationRefusesUnresolvedLegacyConflict(t *testing.T) {
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("establish baseline: %v", err)
	}
	vault.set(payloadWithHosts(host("h1", "Local", "base.example")))
	remotePayload := payloadWithHosts(host("h1", "Remote", "base.example"))
	content := encryptedContentForTest(t, remotePayload, 1, 9000)
	installRemoteContent(t, remote, content, 9000)

	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("error = %v", err)
	}
	if preview.CanInitialize || len(preview.BlockedReasons) == 0 || preview.BlockedReasons[0].Code != "legacy-conflict" {
		t.Fatalf("preview = %+v", preview)
	}
	if configs.config.Convergent.SchemaVersion != 0 {
		t.Fatal("conflicting migration wrote the v2 marker")
	}
	remote.mu.Lock()
	after := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	if !bytes.Equal(after, content) {
		t.Fatal("conflicting migration rewrote the provider")
	}
}

func TestConvergentMigrationFailClosedMatrix(t *testing.T) {
	validState := convergentStateForTest(t, payloadWithHost("local", 1000), "device-1", 5000)
	validContent := convergentContentForState(t, validState, 9000, "device-1")
	validEnvelope, err := domainsync.UnmarshalFile(validContent)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := domainsync.DecryptPayload(validEnvelope, convergentTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	validPayload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		content []byte
		code    string
	}{
		{
			name:    "unknown future schema",
			content: encryptedContentForTest(t, payloadWithHost("local", 1000), 3, 9000),
			code:    "unsupported-newer-schema",
		},
		{
			name:    "invalid schema",
			content: encryptedContentForTest(t, payloadWithHost("local", 1000), -1, 9000),
			code:    "invalid-schema",
		},
		{
			name:    "v2 missing envelope",
			content: encryptedContentForTest(t, payloadWithHost("local", 1000), 2, 9000),
			code:    "missing-envelope",
		},
		{
			name:    "v1 metadata with envelope",
			content: encryptedContentForTest(t, validPayload, 1, 9000),
			code:    "envelope-metadata-mismatch",
		},
		{
			name: "v2 damaged envelope",
			content: func() []byte {
				payload := payloadWithHost("local", 1000)
				payload.ConvergentSync = json.RawMessage(`{}`)
				return encryptedContentForTest(t, payload, 2, 9000)
			}(),
			code: "damaged-envelope",
		},
		{
			name: "v2 envelope does not match materialized payload",
			content: func() []byte {
				payload := validPayload
				payload.Hosts = payloadWithHost("different", 1000).Hosts
				return encryptedContentForTest(t, payload, 2, 9000)
			}(),
			code: "envelope-materialized-mismatch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
			installRemoteContent(t, remote, test.content, 9000)
			before := append([]byte(nil), test.content...)
			preview, err := service.SetConvergentEnabled(context.Background(), true)
			if !errors.Is(err, ErrConvergentMigration) {
				t.Fatalf("error = %v", err)
			}
			if preview.CanInitialize {
				t.Fatalf("preview = %+v", preview)
			}
			found := false
			for _, reason := range preview.BlockedReasons {
				found = found || reason.Code == test.code
			}
			if !found {
				t.Fatalf("blocked reasons = %+v, want %s", preview.BlockedReasons, test.code)
			}
			if configs.config.Convergent.SchemaVersion != 0 {
				t.Fatal("blocked migration wrote the v2 marker")
			}
			remote.mu.Lock()
			after := append([]byte(nil), remote.file.Content...)
			writes := len(remote.writeLog)
			remote.mu.Unlock()
			if !bytes.Equal(after, before) || writes != 0 {
				t.Fatalf("blocked migration touched the provider (writes=%d)", writes)
			}
			vault.mu.Lock()
			applied := len(vault.applied)
			vault.mu.Unlock()
			if applied != 0 {
				t.Fatal("blocked migration changed the Vault")
			}
			if service.Snapshot().Problem != ProblemConvergentMigration {
				t.Fatalf("problem = %q", service.Snapshot().Problem)
			}
		})
	}
}

func TestConvergentPartialMigrationFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "marker without replica",
			mutate: func(config *Config) {
				config.Convergent = ConvergentConfig{
					SchemaVersion: domainsync.ConvergentSchemaVersion,
					Enabled:       true,
				}
			},
		},
		{
			name: "unknown local marker",
			mutate: func(config *Config) {
				config.Convergent = ConvergentConfig{SchemaVersion: 7, Enabled: true}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
			configs.mu.Lock()
			test.mutate(&configs.config)
			configs.mu.Unlock()
			if err := service.Load(context.Background()); err != nil {
				t.Fatal(err)
			}
			vault.mu.Lock()
			applied := len(vault.applied)
			vault.mu.Unlock()
			result, err := service.Sync(context.Background(), TriggerManual)
			if !errors.Is(err, ErrConvergentCorrupt) {
				t.Fatalf("error = %v", err)
			}
			if result.Writes != 0 || remote.writeCount() != 0 {
				t.Fatal("partial migration reached the provider")
			}
			vault.mu.Lock()
			if len(vault.applied) != applied {
				t.Fatal("partial migration changed the Vault")
			}
			vault.mu.Unlock()
		})
	}

	t.Run("corrupt replica", func(t *testing.T) {
		service, remote, _, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
		state := convergentStateForTest(t, payloadWithHost("local", 1000), "device-1", 5000)
		blob, signature, err := service.sealConvergentState(state)
		if err != nil {
			t.Fatal(err)
		}
		damaged := blob[:len(blob)/2] + "not-json"
		configs.mu.Lock()
		configs.config.Convergent = ConvergentConfig{
			SchemaVersion:    domainsync.ConvergentSchemaVersion,
			Enabled:          true,
			Replica:          damaged,
			ReplicaSignature: signature,
		}
		configs.mu.Unlock()
		if err := service.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, err = service.Sync(context.Background(), TriggerManual)
		if !errors.Is(err, ErrConvergentCorrupt) {
			t.Fatalf("error = %v", err)
		}
		if remote.writeCount() != 0 {
			t.Fatal("corrupt replica reached the provider")
		}
	})
}

func TestConvergentSyncRefusesUnreadableRemoteWithoutOverwrite(t *testing.T) {
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	migrateToConvergent(t, service)
	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	remote.mu.Unlock()

	future := encryptedContentForTest(t, payloadWithHost("local", 1000), 3, 12000)
	installRemoteContent(t, remote, future, 12000)
	before := append([]byte(nil), future...)
	_, err := service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("error = %v", err)
	}
	remote.mu.Lock()
	after := append([]byte(nil), remote.file.Content...)
	writesAfter := len(remote.writeLog)
	remote.mu.Unlock()
	if writesAfter != writesBefore || !bytes.Equal(after, before) {
		t.Fatal("a future provider payload was overwritten")
	}
	vault.mu.Lock()
	appliedBefore := len(vault.applied)
	vault.mu.Unlock()

	missing := encryptedContentForTest(t, payloadWithHost("local", 1000), 2, 13000)
	installRemoteContent(t, remote, missing, 13000)
	_, err = service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrConvergentCorrupt) {
		t.Fatalf("missing envelope error = %v", err)
	}
	remote.mu.Lock()
	after = append([]byte(nil), remote.file.Content...)
	writesAfter = len(remote.writeLog)
	remote.mu.Unlock()
	if writesAfter != writesBefore || !bytes.Equal(after, missing) {
		t.Fatal("a damaged v2 payload was overwritten")
	}
	vault.mu.Lock()
	if len(vault.applied) != appliedBefore {
		t.Fatal("a refused remote payload changed the Vault")
	}
	vault.mu.Unlock()
}

type tamperAfterWriteRemote struct {
	*fakeRemote
	tamper func([]byte) []byte
	done   bool
}

func (r *tamperAfterWriteRemote) Write(ctx context.Context, token domainsync.Token, current RemoteFile, content []byte) (RemoteFile, error) {
	written, err := r.fakeRemote.Write(ctx, token, current, content)
	if err != nil || r.done {
		return written, err
	}
	r.done = true
	tampered := r.tamper(content)
	envelope, parseErr := domainsync.UnmarshalFile(tampered)
	if parseErr != nil {
		return RemoteFile{}, parseErr
	}
	r.fakeRemote.mu.Lock()
	r.fakeRemote.file = RemoteFile{
		ID:        written.ID,
		ETag:      written.ETag + "-tampered",
		Content:   append([]byte(nil), tampered...),
		UpdatedAt: envelope.Meta.UpdatedAt,
	}
	r.fakeRemote.mu.Unlock()
	return written, nil
}

func TestConvergentVerificationRoundDetectsInjectedDivergence(t *testing.T) {
	fake := newFakeRemote()
	remote := &tamperAfterWriteRemote{
		fakeRemote: fake,
		tamper: func(content []byte) []byte {
			envelope, err := domainsync.UnmarshalFile(content)
			if err != nil {
				t.Fatal(err)
			}
			plaintext, err := domainsync.DecryptPayload(envelope, convergentTestPassword)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := decodePayloadBytes(plaintext)
			if err != nil {
				t.Fatal(err)
			}
			stateEnvelope, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
			if err != nil {
				t.Fatal(err)
			}
			tampered := mutateStateValue(t, stateEnvelope.State)
			return convergentContentForState(t, tampered, envelope.Meta.UpdatedAt, "device-1")
		},
	}
	vault := &fakeVault{payload: payloadWithHost("local", 1000)}
	configs := &memoryConfigs{config: Config{Connected: true, Login: "octocat", Strategy: domainsync.StrategySmartMerge}}
	tokens := &memoryTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	service := NewService(remote, vault, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithWaiter(&fakeWaiter{}),
		WithBackups(&fakeBackups{}),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDevice("device-1", "Test Device", "1.0"),
		WithDebounce(10*time.Millisecond),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword(convergentTestPassword); err != nil {
		t.Fatal(err)
	}

	_, err := service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentDivergence) {
		t.Fatalf("error = %v", err)
	}
	if fake.writeCount() != 1 {
		t.Fatalf("provider writes = %d, want one non-corrected write", fake.writeCount())
	}
	snapshot := service.Snapshot()
	if snapshot.Problem != ProblemConvergentDivergence || snapshot.Convergent.Phase != "diverged" {
		t.Fatalf("snapshot = problem %q, info %+v", snapshot.Problem, snapshot.Convergent)
	}
	divergence := snapshot.Convergent.Divergence
	if divergence == nil || divergence.Kind == "" || len(divergence.Actions) != 3 {
		t.Fatalf("divergence = %+v", divergence)
	}
	if divergence.Kind != "entity-hash-mismatch" && divergence.Kind != "dot-payload-mismatch" {
		t.Fatalf("divergence kind = %q", divergence.Kind)
	}
	if len(snapshot.Convergent.Rounds) != ConvergentVerifyRounds {
		t.Fatalf("rounds = %+v", snapshot.Convergent.Rounds)
	}
	if snapshot.Convergent.Rounds[0].Outcome != "retry" {
		t.Fatalf("round 1 outcome = %q, want retry", snapshot.Convergent.Rounds[0].Outcome)
	}
}

// TestVerificationRoundComparisonsAreProgressive proves the three specified
// comparisons are distinct: a payload-only change fails rounds 2 and 3, while
// a state-only change with identical manifests and entity hashes fails only
// round 3.
func TestVerificationRoundComparisonsAreProgressive(t *testing.T) {
	expected := convergentStateForTest(t, payloadWithHost("local", 1000), "device-1", 5000)
	for round := 1; round <= ConvergentVerifyRounds; round++ {
		if !roundSatisfied(round, expected, expected) {
			t.Fatalf("round %d rejected an identical state", round)
		}
	}

	payloadChanged := mutateStateValue(t, expected)
	if !roundSatisfied(1, expected, payloadChanged) {
		t.Fatal("round 1 should compare only manifests")
	}
	if roundSatisfied(2, expected, payloadChanged) {
		t.Fatal("round 2 accepted a different entity hash")
	}
	if roundSatisfied(3, expected, payloadChanged) {
		t.Fatal("round 3 accepted a different full-state hash")
	}

	stateOnly, err := domainsync.NormalizeConvergentState(expected)
	if err != nil {
		t.Fatal(err)
	}
	stateOnly.Collections["empty"] = domainsync.ConvergentCollection{Entities: map[string]domainsync.ConvergentEntity{}}
	if err := domainsync.ValidateConvergentState(stateOnly); err != nil {
		t.Fatalf("state-only mutation is invalid: %v", err)
	}
	if !domainsync.ManifestsEqual(
		domainsync.BuildConvergentManifest(expected),
		domainsync.BuildConvergentManifest(stateOnly),
	) {
		t.Fatal("the state-only mutation changed the manifest")
	}
	if !roundSatisfied(1, expected, stateOnly) {
		t.Fatal("round 1 rejected an identical manifest")
	}
	if !roundSatisfied(2, expected, stateOnly) {
		t.Fatal("round 2 rejected identical entity hashes")
	}
	if roundSatisfied(3, expected, stateOnly) {
		t.Fatal("round 3 accepted a different full-state hash")
	}
}

func TestConvergentEqualVectorDifferentStateIsDivergenceAndRestoreIsSafe(t *testing.T) {
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	migrateToConvergent(t, service)
	stateA, err := service.loadConvergentStateStrict(configs.config)
	if err != nil {
		t.Fatal(err)
	}
	stateB := convergentStateForTest(t, payloadWithHost("forged", 1000), "device-1", 5000)
	hashA, err := domainsync.ConvergentStateHash(stateA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := domainsync.ConvergentStateHash(stateB)
	if err != nil {
		t.Fatal(err)
	}
	if hashA == hashB {
		t.Fatal("test states are identical")
	}
	content := convergentContentForState(t, stateB, 12000, "device-1")
	installRemoteContent(t, remote, content, 12000)
	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	remote.mu.Unlock()

	_, err = service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrConvergentDivergence) {
		t.Fatalf("error = %v", err)
	}
	remote.mu.Lock()
	after := append([]byte(nil), remote.file.Content...)
	writesAfter := len(remote.writeLog)
	remote.mu.Unlock()
	if writesAfter != writesBefore || !bytes.Equal(after, content) {
		t.Fatal("divergence triggered an automatic overwrite")
	}
	info := service.Snapshot().Convergent
	if info.Divergence == nil || (info.Divergence.Kind != "equal-vector-different-state" && info.Divergence.Kind != "dot-payload-mismatch") {
		t.Fatalf("divergence info = %+v", info.Divergence)
	}
	actions := map[string]bool{}
	for _, action := range info.Divergence.Actions {
		actions[action] = true
	}
	if !actions["inspect"] || !actions["restore"] || !actions["resync"] {
		t.Fatalf("unsafe actions = %+v", info.Divergence.Actions)
	}

	vault.set(payloadWithHost("damaged-local", 2000))
	result, err := service.RestoreLastVerified(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Writes != 0 || result.Action != domainsync.ActionDownload {
		t.Fatalf("restore result = %+v", result)
	}
	vault.mu.Lock()
	restored := clonePayload(vault.payload)
	vault.mu.Unlock()
	if len(restored.Hosts) != 1 || restored.Hosts[0].Label != "local" {
		t.Fatalf("restored hosts = %+v", restored.Hosts)
	}
	if remote.writeCount() != writesAfter {
		t.Fatal("restore wrote the provider")
	}
}

func TestConvergentPauseNeverFallsBackToLegacy(t *testing.T) {
	service, remote, _, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, &fakeBackups{})
	migrateToConvergent(t, service)
	if _, err := service.SetConvergentEnabled(context.Background(), false); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if configs.config.Convergent.SchemaVersion != 2 || configs.config.Convergent.Enabled {
		t.Fatalf("paused config = %+v", configs.config.Convergent)
	}
	if service.Snapshot().Convergent.Phase != "paused" {
		t.Fatalf("paused info = %+v", service.Snapshot().Convergent)
	}
	writesBefore := remote.writeCount()
	_, err := service.Sync(context.Background(), TriggerManual)
	if !errors.Is(err, ErrConvergentPaused) {
		t.Fatalf("paused sync error = %v", err)
	}
	if remote.writeCount() != writesBefore {
		t.Fatal("paused sync fell through to the legacy writer")
	}
	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !preview.AlreadyInitialized {
		t.Fatalf("resume preview = %+v", preview)
	}
	if !configs.config.Convergent.Enabled || configs.config.Convergent.Replica == "" {
		t.Fatal("resume discarded the replica")
	}
	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("resumed sync: %v", err)
	}
	if result.Writes != 0 {
		t.Fatalf("resumed sync writes = %d", result.Writes)
	}
}

type convergentDevice struct {
	service *Service
	remote  *fakeRemote
	vault   *fakeVault
	configs *memoryConfigs
}

func newConvergentDevice(t *testing.T, remote *fakeRemote, id string, payload Payload) *convergentDevice {
	t.Helper()
	vault := &fakeVault{payload: payload}
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
	return &convergentDevice{service: service, remote: remote, vault: vault, configs: configs}
}

func (d *convergentDevice) state(t *testing.T) domainsync.ConvergentState {
	t.Helper()
	state, err := d.service.loadConvergentStateStrict(d.configs.config)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func (d *convergentDevice) snapshot(t *testing.T) domainsync.ConvergentSnapshot {
	t.Helper()
	d.vault.mu.Lock()
	payload := clonePayload(d.vault.payload)
	d.vault.mu.Unlock()
	snapshot, err := payloadSnapshot(payload)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestConvergentTwoDeviceProfilesConvergeIncludingConcurrentWrites(t *testing.T) {
	remote := newFakeRemote()
	deviceA := newConvergentDevice(t, remote, "device-a", payloadWithHosts(host("h1", "A", "a.example")))
	deviceB := newConvergentDevice(t, remote, "device-b", Payload{})

	if _, err := deviceA.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable A: %v", err)
	}
	if _, err := deviceB.service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable B: %v", err)
	}
	if _, err := deviceB.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("B adopts A: %v", err)
	}
	// B adds a record; A must pick it up without treating its absence as a
	// local deletion.
	deviceB.vault.set(payloadWithHosts(
		host("h1", "A", "a.example"),
		host("h2", "B", "b.example"),
	))
	if _, err := deviceB.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("B uploads h2: %v", err)
	}
	if _, err := deviceA.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("A downloads h2: %v", err)
	}
	deviceA.vault.mu.Lock()
	haveH2 := hasHostID(deviceA.vault.payload, "h2")
	deviceA.vault.mu.Unlock()
	if !haveH2 {
		t.Fatalf("A did not download h2: %v", hostIDs(deviceA.vault.payload))
	}

	// Both devices edit the same field while offline. The register retains
	// both candidates; the deterministic winner is the same on both replicas.
	deviceA.vault.set(payloadWithHosts(
		host("h1", "A2", "a.example"),
		host("h2", "B", "b.example"),
	))
	deviceB.vault.set(payloadWithHosts(
		host("h1", "B2", "a.example"),
		host("h2", "B", "b.example"),
	))
	if _, err := deviceA.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("A concurrent sync: %v", err)
	}
	if _, err := deviceB.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("B concurrent sync: %v", err)
	}
	if _, err := deviceA.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("A reconverges: %v", err)
	}

	stateA := deviceA.state(t)
	stateB := deviceB.state(t)
	hashA, err := domainsync.ConvergentStateHash(stateA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := domainsync.ConvergentStateHash(stateB)
	if err != nil {
		t.Fatal(err)
	}
	if hashA != hashB {
		t.Fatal("device replicas did not converge to identical canonical bytes")
	}
	snapshotA := deviceA.snapshot(t)
	snapshotB := deviceB.snapshot(t)
	if !snapshotsEqual(snapshotA, snapshotB) {
		t.Fatal("device Vault snapshots differ after convergence")
	}
	envelope, err := domainsync.UnmarshalFile(remote.file.Content)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := domainsync.DecryptPayload(envelope, convergentTestPassword)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := decodePayloadBytes(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	cloudEnvelope, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
	if err != nil {
		t.Fatal(err)
	}
	cloudHash, err := domainsync.ConvergentStateHash(cloudEnvelope.State)
	if err != nil {
		t.Fatal(err)
	}
	if cloudHash != hashA {
		t.Fatal("provider state differs from both converged replicas")
	}
	if domainsync.ConvergentConflictCount(stateA) == 0 {
		t.Fatal("concurrent writes did not retain a multi-value conflict")
	}

	writesA := remote.writeCount()
	if _, err := deviceA.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("A repeated sync: %v", err)
	}
	if _, err := deviceB.service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("B repeated sync: %v", err)
	}
	if remote.writeCount() != writesA {
		t.Fatalf("repeated converged syncs wrote the provider: %d -> %d", writesA, remote.writeCount())
	}
	if deviceA.service.Snapshot().Convergent.Writes != 0 || deviceB.service.Snapshot().Convergent.Writes != 0 {
		t.Fatal("repeated converged sync did not report zero writes")
	}
}

func TestConvergentPreferLocalRepeatedSyncWritesNothing(t *testing.T) {
	service, remote, vault, _ := serviceForStrategy(t, domainsync.StrategyPreferLocal, &fakeBackups{})
	migrateToConvergent(t, service)

	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	contentBefore := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	vault.mu.Lock()
	appliedBefore := len(vault.applied)
	vault.mu.Unlock()

	result, err := service.Sync(context.Background(), TriggerManual)
	if err != nil {
		t.Fatalf("repeated sync: %v", err)
	}
	if result.Writes != 0 || result.Uploaded || result.Action != domainsync.ActionNone {
		t.Fatalf("repeated preferLocal result = %+v", result)
	}
	remote.mu.Lock()
	writesAfter := len(remote.writeLog)
	contentAfter := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	if writesAfter != writesBefore {
		t.Fatalf("repeated preferLocal sync wrote the provider: %d -> %d", writesBefore, writesAfter)
	}
	if !bytes.Equal(contentBefore, contentAfter) {
		t.Fatal("repeated preferLocal sync changed the provider bytes")
	}
	vault.mu.Lock()
	appliedAfter := len(vault.applied)
	vault.mu.Unlock()
	if appliedAfter != appliedBefore {
		t.Fatalf("repeated preferLocal sync rewrote the Vault: %d -> %d", appliedBefore, appliedAfter)
	}
	info := service.Snapshot().Convergent
	if info.Writes != 0 || info.Phase != "converged" {
		t.Fatalf("repeated preferLocal info = %+v", info)
	}
}

func TestConvergentMigrationRefusesPersistedPendingConflict(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs, _, _ := testServiceWith(t, WithBackups(backups))
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)

	vault.set(payloadWithHosts(host("h1", "Local edit", "base.example")))
	remote.mu.Lock()
	remote.conflictNext = true
	remote.conflictFile = RemoteFile{
		ID: "gist-1", ETag: "etag-cloud", UpdatedAt: 12000,
		Content: encryptForTest(t, payloadWithHosts(host("h1", "Cloud edit", "cloud.example")), "secret", 12000),
	}
	remote.mu.Unlock()
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want a surfaced conflict", err)
	}
	configs.mu.Lock()
	pending := configs.config.PendingConflict
	configs.mu.Unlock()
	if pending == nil {
		t.Fatal("the conflict was not persisted")
	}
	remoteContent := append([]byte(nil), pending.RemoteContent...)
	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	remoteBefore := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	backupsBefore := backups.createdCount()
	vault.mu.Lock()
	appliedBefore := len(vault.applied)
	vault.mu.Unlock()

	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("error = %v, want a fail-closed migration refusal", err)
	}
	if preview.CanInitialize || len(preview.BlockedReasons) == 0 || preview.BlockedReasons[0].Code != "pending-conflict" {
		t.Fatalf("preview = %+v", preview)
	}
	configs.mu.Lock()
	after := configs.config
	configs.mu.Unlock()
	if after.Convergent.SchemaVersion != 0 {
		t.Fatalf("refused migration wrote marker %d", after.Convergent.SchemaVersion)
	}
	if after.PendingConflict == nil {
		t.Fatal("refused migration dropped the pending conflict")
	}
	if !bytes.Equal(after.PendingConflict.RemoteContent, remoteContent) {
		t.Fatal("refused migration changed the pending conflict's cloud bytes")
	}
	remote.mu.Lock()
	writesAfter := len(remote.writeLog)
	remoteAfter := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	if writesAfter != writesBefore || !bytes.Equal(remoteBefore, remoteAfter) {
		t.Fatal("refused migration touched the provider")
	}
	if backups.createdCount() != backupsBefore {
		t.Fatal("refused migration took a misleading protective backup")
	}
	vault.mu.Lock()
	appliedAfter := len(vault.applied)
	vault.mu.Unlock()
	if appliedAfter != appliedBefore {
		t.Fatal("refused migration changed the Vault")
	}
	info := service.Snapshot().Convergent
	if info.Phase != "blocked" || info.ConflictCount == 0 {
		t.Fatalf("refused migration info = %+v", info)
	}
	if info.TotalRounds != 0 || len(info.Rounds) != 0 {
		t.Fatalf("refused migration reported a verification round: %+v", info)
	}
}

func TestConvergentRestartReportsVerifiedCountsWithoutFabricatedRound(t *testing.T) {
	backups := &fakeBackups{}
	service, remote, vault, configs, tokens, waiter := testServiceWith(t, WithBackups(backups))
	migrateToConvergent(t, service)

	restarted := NewService(remote, vault, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithWaiter(waiter),
		WithBackups(backups),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDevice("device-1", "Test Device", "1.0"),
	)
	if err := restarted.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := restarted.SetPassword("secret"); err != nil {
		t.Fatal(err)
	}
	info := restarted.Snapshot().Convergent
	if info.Phase != "idle" {
		t.Fatalf("restart phase = %q", info.Phase)
	}
	if info.TotalRounds != 0 || info.Round != 0 || len(info.Rounds) != 0 {
		t.Fatalf("restart fabricated a verification round: %+v", info)
	}
	if info.EntityCount != 1 || info.ConflictCount != 0 {
		t.Fatalf("restart counts = %d entities, %d conflicts", info.EntityCount, info.ConflictCount)
	}
	if info.LastVerifiedAt == 0 {
		t.Fatal("restart did not retain the verification time")
	}
}

type concurrentFinalRoundRemote struct {
	*fakeRemote
	stale      []byte
	concurrent func([]byte) []byte
	writes     int
}

func (r *concurrentFinalRoundRemote) Write(ctx context.Context, token domainsync.Token, current RemoteFile, content []byte) (RemoteFile, error) {
	written, err := r.fakeRemote.Write(ctx, token, current, content)
	if err != nil {
		return written, err
	}
	r.writes++
	stored := append([]byte(nil), r.stale...)
	if r.writes >= ConvergentVerifyRounds {
		stored = r.concurrent(content)
	}
	envelope, parseErr := domainsync.UnmarshalFile(stored)
	if parseErr != nil {
		return RemoteFile{}, parseErr
	}
	r.fakeRemote.mu.Lock()
	r.fakeRemote.file = RemoteFile{
		ID:        written.ID,
		ETag:      written.ETag + "-race",
		Content:   stored,
		UpdatedAt: envelope.Meta.UpdatedAt,
	}
	r.fakeRemote.mu.Unlock()
	return written, nil
}

// TestConvergentFinalRoundRetriesOrdinaryConcurrency proves a concurrent
// writer landing between the final upload and its read-back is retried once,
// not reported as divergence. Genuine divergence still returns immediately.
func TestConvergentFinalRoundRetriesOrdinaryConcurrency(t *testing.T) {
	fake := newFakeRemote()
	stale := convergentContentForState(t, convergentStateForTest(t,
		payloadWithHosts(host("stale", "Stale", "stale.example")), "stale", 1000), 1000, "stale")
	remote := &concurrentFinalRoundRemote{fakeRemote: fake, stale: stale}
	remote.concurrent = func(content []byte) []byte {
		envelope, err := domainsync.UnmarshalFile(content)
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := domainsync.DecryptPayload(envelope, convergentTestPassword)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := decodePayloadBytes(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		stateEnvelope, err := domainsync.ParseConvergentEnvelope(payload.ConvergentSync)
		if err != nil {
			t.Fatal(err)
		}
		payload.Hosts = append(payload.Hosts, host("h2", "Concurrent", "b.example"))
		snapshot, err := payloadSnapshot(payload)
		if err != nil {
			t.Fatal(err)
		}
		merged, _, err := domainsync.ApplyConvergentSnapshot(stateEnvelope.State, snapshot, "concurrent", 7000, false)
		if err != nil {
			t.Fatal(err)
		}
		return convergentContentForState(t, merged, envelope.Meta.UpdatedAt, "concurrent")
	}
	vault := &fakeVault{payload: payloadWithHost("local", 1000)}
	configs := &memoryConfigs{config: Config{Connected: true, Login: "octocat", Strategy: domainsync.StrategySmartMerge}}
	tokens := &memoryTokens{token: domainsync.Token{AccessToken: "token"}, set: true}
	service := NewService(remote, vault, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithWaiter(&fakeWaiter{}),
		WithBackups(&fakeBackups{}),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDevice("device-1", "Test Device", "1.0"),
		WithDebounce(10*time.Millisecond),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPassword(convergentTestPassword); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SetConvergentEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable with ordinary concurrency: %v", err)
	}
	if remote.writes != ConvergentVerifyRounds {
		t.Fatalf("provider writes = %d, want %d", remote.writes, ConvergentVerifyRounds)
	}
	info := service.Snapshot().Convergent
	if info.Phase != "converged" || info.Outcome != "converged" || info.Divergence != nil {
		t.Fatalf("convergent info = %+v", info)
	}
	if len(info.Rounds) != ConvergentVerifyRounds || info.Rounds[ConvergentVerifyRounds-1].Outcome != "verified" {
		t.Fatalf("rounds = %+v", info.Rounds)
	}
	vault.mu.Lock()
	haveConcurrent := hasHostID(vault.payload, "h2")
	vault.mu.Unlock()
	if !haveConcurrent {
		t.Fatal("the retried final round did not materialize the concurrent record")
	}
}
