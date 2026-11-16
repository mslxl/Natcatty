package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// migrationFieldConflictFixture builds a provider v2 state whose Host has two
// genuine two-sided field edits: one device changed the label, the other the
// hostname, both from the same base. The local Vault and the merge anchor are
// still the base payload, so the migration sees the conflict before it can
// adopt either side.
func migrationFieldConflictFixture(t *testing.T) (*Service, *fakeRemote, *fakeVault, *memoryConfigs, []byte) {
	t.Helper()
	backups := &fakeBackups{}
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)
	merged := migrationConflictedState(t, "From B")
	content := convergentContentForState(t, merged, 9000, "device-a")
	installRemoteContent(t, remote, content, 9000)
	return service, remote, vault, configs, content
}

// migrationConflictedState builds the provider v2 state the migration fixture
// installs: one device changed the label, another the hostname, both from the
// same base. remoteLabel is the label the remote side keeps.
func migrationConflictedState(t *testing.T, remoteLabel string) domainsync.ConvergentState {
	t.Helper()
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	baseSnapshot, err := payloadSnapshot(base)
	if err != nil {
		t.Fatal(err)
	}
	baseState, err := domainsync.CreateConvergentStateFromSnapshot(baseSnapshot, "device-base", 1000)
	if err != nil {
		t.Fatal(err)
	}
	sideA, err := payloadSnapshot(payloadWithHosts(host("h1", "From A", "a.example")))
	if err != nil {
		t.Fatal(err)
	}
	sideB, err := payloadSnapshot(payloadWithHosts(host("h1", remoteLabel, "b.example")))
	if err != nil {
		t.Fatal(err)
	}
	stateA, _, err := domainsync.ApplyConvergentSnapshot(baseState, sideA, "device-a", 2000, false)
	if err != nil {
		t.Fatal(err)
	}
	stateB, _, err := domainsync.ApplyConvergentSnapshot(baseState, sideB, "device-b", 3000, false)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := domainsync.MergeConvergentStates(stateA, stateB)
	if err != nil {
		t.Fatal(err)
	}
	return merged
}

func conflictForField(t *testing.T, preview ConvergentMigrationPreview, field string) ConvergentMigrationConflict {
	t.Helper()
	for _, conflict := range preview.Conflicts {
		if conflict.Field == field {
			return conflict
		}
	}
	t.Fatalf("no conflict for %q in %+v", field, preview.Conflicts)
	return ConvergentMigrationConflict{}
}

func TestConvergentMigrationListsAndResolvesFieldConflicts(t *testing.T) {
	service, remote, vault, configs, content := migrationFieldConflictFixture(t)

	// The initial attempt is blocked and lists both contested fields with the
	// value each side would keep. Nothing is written before a choice exists.
	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	remote.mu.Unlock()
	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("error = %v, want a blocked migration", err)
	}
	if preview.CanInitialize || preview.ConflictCount != 2 || len(preview.Conflicts) != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	label := conflictForField(t, preview, "label")
	if string(label.LocalValue) != `"Base"` || string(label.RemoteValue) != `"From B"` {
		t.Fatalf("label values = local %s, remote %s", label.LocalValue, label.RemoteValue)
	}
	if !label.LocalPresent || !label.RemotePresent || label.Secret {
		t.Fatalf("label presence = %+v", label)
	}
	hostname := conflictForField(t, preview, "hostname")
	if string(hostname.LocalValue) != `"base.example"` || string(hostname.RemoteValue) != `"b.example"` {
		t.Fatalf("hostname values = local %s, remote %s", hostname.LocalValue, hostname.RemoteValue)
	}
	if configs.config.Convergent.SchemaVersion != 0 {
		t.Fatal("blocked migration wrote the v2 marker")
	}
	remote.mu.Lock()
	after := append([]byte(nil), remote.file.Content...)
	writes := len(remote.writeLog)
	remote.mu.Unlock()
	if !bytes.Equal(after, content) || writes != writesBefore {
		t.Fatalf("blocked migration touched the provider (writes=%d, before=%d)", writes, writesBefore)
	}

	// One choice is not enough: the migration revalidates and stays blocked on
	// the remaining field.
	preview, err = service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register: label.Register,
		Side:     ConvergentConflictKeepLocal,
	})
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("partial resolution error = %v", err)
	}
	if preview.CanInitialize || preview.ConflictCount != 1 || len(preview.Conflicts) != 1 {
		t.Fatalf("partial preview = %+v", preview)
	}
	if preview.Conflicts[0].Field != "hostname" {
		t.Fatalf("remaining conflict = %+v", preview.Conflicts[0])
	}

	// The second choice completes the migration with the chosen values: the
	// local label and the provider's hostname.
	preview, err = service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register: hostname.Register,
		Side:     ConvergentConflictKeepRemote,
	})
	if err != nil {
		t.Fatalf("complete resolution: %v", err)
	}
	if !preview.CanInitialize || preview.ConflictCount != 0 {
		t.Fatalf("resolved preview = %+v", preview)
	}
	if configs.config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		t.Fatal("resolved migration did not write the v2 marker")
	}
	cloud := remote.payload(t, convergentTestPassword)
	if len(cloud.Hosts) != 1 || cloud.Hosts[0].Label != "Base" || cloud.Hosts[0].Hostname != "b.example" {
		t.Fatalf("cloud host = %+v", cloud.Hosts)
	}
	vault.mu.Lock()
	local := clonePayload(vault.payload)
	vault.mu.Unlock()
	if len(local.Hosts) != 1 || local.Hosts[0].Label != "Base" || local.Hosts[0].Hostname != "b.example" {
		t.Fatalf("local host = %+v", local.Hosts)
	}
}

func TestConvergentMigrationBulkResolutionKeepsOneSideForEveryField(t *testing.T) {
	service, remote, _, configs, _ := migrationFieldConflictFixture(t)

	preview, err := service.ResolveConvergentMigrationConflicts(context.Background(), ConvergentConflictKeepLocal)
	if err != nil {
		t.Fatalf("bulk resolution: %v", err)
	}
	if !preview.CanInitialize || len(preview.Conflicts) != 0 {
		t.Fatalf("preview = %+v", preview)
	}
	if configs.config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		t.Fatal("bulk resolution did not complete the migration")
	}
	cloud := remote.payload(t, convergentTestPassword)
	if len(cloud.Hosts) != 1 || cloud.Hosts[0].Label != "Base" || cloud.Hosts[0].Hostname != "base.example" {
		t.Fatalf("keep-local cloud host = %+v", cloud.Hosts)
	}
}

// TestConvergentMigrationConflictValuesHideSecrets checks that a contested
// credential register is listed by address but never renders its plaintext.
func TestConvergentMigrationConflictValuesHideSecrets(t *testing.T) {
	conflicts := []domainsync.ConvergentConflict{{
		Family:   "secrets",
		EntityID: "host-1/password",
		Field:    "value",
		Register: domainsync.RegisterIdentity("secrets", "host-1/password", "value"),
	}}
	list := convergentMigrationConflictList(conflicts, domainsync.ConvergentSnapshot{}, domainsync.ConvergentSnapshot{})
	if len(list) != 1 || !list[0].Secret {
		t.Fatalf("secret conflict = %+v", list)
	}
	if len(list[0].LocalValue) != 0 || len(list[0].RemoteValue) != 0 {
		t.Fatalf("secret values were exposed: %+v", list[0])
	}
	if list[0].Register != conflicts[0].Register || list[0].Field != "value" {
		t.Fatalf("secret conflict address = %+v", list[0])
	}

	// A non-secret value is carried verbatim so the UI can show both sides.
	plain := []domainsync.ConvergentConflict{{
		Family:   "hosts",
		EntityID: "host-1",
		Field:    "label",
		Register: domainsync.RegisterIdentity("hosts", "host-1", "label"),
	}}
	local := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"hosts": {{ID: "host-1", Value: json.RawMessage(`{"label":"local"}`)}},
	}}
	remote := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"hosts": {{ID: "host-1", Value: json.RawMessage(`{"label":"remote"}`)}},
	}}
	list = convergentMigrationConflictList(plain, local, remote)
	if list[0].Secret || string(list[0].LocalValue) != `"local"` || string(list[0].RemoteValue) != `"remote"` {
		t.Fatalf("plain conflict = %+v", list[0])
	}

	// A credential-like word in a value is still a value, not a key: a Host
	// labeled "tokenizer" must stay readable.
	tokenizer := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"hosts": {{ID: "host-1", Value: json.RawMessage(`{"label":"tokenizer"}`)}},
	}}
	list = convergentMigrationConflictList(plain, tokenizer, tokenizer)
	if list[0].Secret || string(list[0].LocalValue) != `"tokenizer"` {
		t.Fatalf("value-only marker hid a plain value: %+v", list[0])
	}

	// A credential-like key at any depth still hides the values.
	nested := []domainsync.ConvergentConflict{{
		Family:   "hosts",
		EntityID: "host-1",
		Field:    "advanced",
		Register: domainsync.RegisterIdentity("hosts", "host-1", "advanced"),
	}}
	credentialed := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"hosts": {{ID: "host-1", Value: json.RawMessage(`{"advanced":{"password":"hunter2"}}`)}},
	}}
	list = convergentMigrationConflictList(nested, credentialed, credentialed)
	if len(list) != 1 || !list[0].Secret || len(list[0].LocalValue) != 0 {
		t.Fatalf("nested credential key was exposed: %+v", list)
	}
}

// TestConvergentMigrationExplicitEnableDropsEarlierChoices checks that choices
// from an abandoned attempt cannot silently resolve a conflict the user is
// shown again from scratch.
func TestConvergentMigrationExplicitEnableDropsEarlierChoices(t *testing.T) {
	service, _, _, _, _ := migrationFieldConflictFixture(t)

	preview, err := service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register: domainsync.RegisterIdentity("hosts", "h1", "label"),
		Side:     ConvergentConflictKeepLocal,
	})
	if !errors.Is(err, ErrConvergentMigration) || len(preview.Conflicts) != 1 {
		t.Fatalf("partial resolution = %+v, %v", preview, err)
	}

	preview, err = service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("fresh enable error = %v", err)
	}
	if len(preview.Conflicts) != 2 {
		t.Fatalf("fresh enable kept earlier choices: %+v", preview.Conflicts)
	}
}

// TestConvergentMigrationAbandonedAttemptDropsChoiceWhenRemoteChanged covers an
// abandoned attempt without an explicit re-enable: the user chooses one field,
// leaves the migration blocked, and the provider's value for that field
// changes before they come back and resolve the other field. The old choice
// must not silently dominate the new remote value.
func TestConvergentMigrationAbandonedAttemptDropsChoiceWhenRemoteChanged(t *testing.T) {
	service, remote, _, _, _ := migrationFieldConflictFixture(t)

	// First attempt: keep the local label, leaving the hostname contested.
	preview, err := service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register: domainsync.RegisterIdentity("hosts", "h1", "label"),
		Side:     ConvergentConflictKeepLocal,
	})
	if !errors.Is(err, ErrConvergentMigration) || len(preview.Conflicts) != 1 {
		t.Fatalf("partial resolution = %+v, %v", preview, err)
	}
	if preview.Conflicts[0].Field != "hostname" {
		t.Fatalf("remaining conflict = %+v", preview.Conflicts[0])
	}

	// The attempt is abandoned. The provider now carries a different label.
	changed := convergentContentForState(t, migrationConflictedState(t, "Changed B"), 10000, "device-a")
	installRemoteContent(t, remote, changed, 10000)

	// Second attempt: resolve the hostname. The stale label choice belongs to
	// the earlier value set, so it must not ride along and complete the merge.
	preview, err = service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register: domainsync.RegisterIdentity("hosts", "h1", "hostname"),
		Side:     ConvergentConflictKeepRemote,
	})
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("stale label choice completed the migration: %+v, %v", preview, err)
	}
	if preview.CanInitialize || len(preview.Conflicts) != 1 || preview.Conflicts[0].Field != "label" {
		t.Fatalf("remaining conflict = %+v", preview.Conflicts)
	}
	if got := string(preview.Conflicts[0].RemoteValue); got != `"Changed B"` {
		t.Fatalf("remote label = %s, want the changed value", got)
	}
}
