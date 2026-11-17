package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
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

// firstConflictPreview renders the blocked migration preview the way the
// settings surface does: one planning call, then the rows carry the
// fingerprints every later choice must echo.
func firstConflictPreview(t *testing.T, service *Service) ConvergentMigrationPreview {
	t.Helper()
	preview, err := service.SetConvergentEnabled(context.Background(), true)
	if !errors.Is(err, ErrConvergentMigration) {
		t.Fatalf("initial preview error = %v, want a blocked migration", err)
	}
	if len(preview.Conflicts) == 0 {
		t.Fatalf("initial preview has no conflicts: %+v", preview)
	}
	return preview
}

// conflictExpectations models the rows a bulk button was rendered from.
func conflictExpectations(conflicts []ConvergentMigrationConflict) []ConvergentConflictExpectation {
	expected := make([]ConvergentConflictExpectation, 0, len(conflicts))
	for _, conflict := range conflicts {
		expected = append(expected, ConvergentConflictExpectation{
			Register:    conflict.Register,
			Fingerprint: conflict.Fingerprint,
		})
	}
	return expected
}

// migrationSingleLabelConflictFixture builds a provider v2 state with exactly
// one contested field: the label. The local Vault and the merge anchor stay at
// the base value, so resolving that row is the only thing between the blocked
// preview and a completed migration.
func migrationSingleLabelConflictFixture(t *testing.T) (*Service, *fakeRemote, *fakeVault, *memoryConfigs, []byte) {
	t.Helper()
	backups := &fakeBackups{}
	service, remote, vault, configs := serviceForStrategy(t, domainsync.StrategySmartMerge, backups)
	base := payloadWithHosts(host("h1", "Base", "base.example"))
	vault.set(base)
	establishAnchor(t, service, remote, base)
	merged := migrationSingleLabelConflictState(t, "From B")
	content := convergentContentForState(t, merged, 9000, "device-a")
	installRemoteContent(t, remote, content, 9000)
	return service, remote, vault, configs, content
}

func migrationSingleLabelConflictState(t *testing.T, remoteLabel string) domainsync.ConvergentState {
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
	sideA, err := payloadSnapshot(payloadWithHosts(host("h1", "From A", "base.example")))
	if err != nil {
		t.Fatal(err)
	}
	sideB, err := payloadSnapshot(payloadWithHosts(host("h1", remoteLabel, "base.example")))
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
	if label.Fingerprint == "" {
		t.Fatal("label conflict has no value fingerprint")
	}
	hostname := conflictForField(t, preview, "hostname")
	if string(hostname.LocalValue) != `"base.example"` || string(hostname.RemoteValue) != `"b.example"` {
		t.Fatalf("hostname values = local %s, remote %s", hostname.LocalValue, hostname.RemoteValue)
	}
	if hostname.Fingerprint == "" || hostname.Fingerprint == label.Fingerprint {
		t.Fatalf("hostname fingerprint = %q, label = %q", hostname.Fingerprint, label.Fingerprint)
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
		Register:    label.Register,
		Fingerprint: label.Fingerprint,
		Side:        ConvergentConflictKeepLocal,
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
		Register:    hostname.Register,
		Fingerprint: preview.Conflicts[0].Fingerprint,
		Side:        ConvergentConflictKeepRemote,
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

	preview := firstConflictPreview(t, service)
	preview, err := service.ResolveConvergentMigrationConflicts(context.Background(), ConvergentConflictKeepLocal, conflictExpectations(preview.Conflicts))
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

	// The secrets family keys on {entityType, entityId, field} joined with the
	// unit separator. That composite ID is display-only and must be printable;
	// the raw identity stays in Register, which is the resolution identity.
	compositeID := "host\x1fh1\x1fpassword"
	composite := []domainsync.ConvergentConflict{{
		Family:   "secrets",
		EntityID: compositeID,
		Field:    "value",
		Register: domainsync.RegisterIdentity("secrets", compositeID, "value"),
	}}
	list = convergentMigrationConflictList(composite, domainsync.ConvergentSnapshot{}, domainsync.ConvergentSnapshot{})
	if list[0].EntityID != "host/h1/password" {
		t.Fatalf("composite secret entity id rendered as %q", list[0].EntityID)
	}
	if strings.ContainsAny(list[0].EntityID, "\x1f\x00") {
		t.Fatalf("composite secret entity id kept control bytes: %q", list[0].EntityID)
	}
	if list[0].Register != composite[0].Register {
		t.Fatalf("display sanitizing changed the resolution identity: %q", list[0].Register)
	}

	// Note bodies are classified as secret on purpose: a note can contain any
	// pasted credential, so the migration surface must never render one. This
	// is an intentional over-redaction, not an accident.
	note := []domainsync.ConvergentConflict{{
		Family:   "notes",
		EntityID: "note-1",
		Field:    "content",
		Register: domainsync.RegisterIdentity("notes", "note-1", "content"),
	}}
	noteSnapshot := domainsync.ConvergentSnapshot{Families: map[string][]domainsync.ConvergentRecord{
		"notes": {{ID: "note-1", Value: json.RawMessage(`{"content":"private body"}`)}},
	}}
	list = convergentMigrationConflictList(note, noteSnapshot, noteSnapshot)
	if len(list) != 1 || !list[0].Secret || len(list[0].LocalValue) != 0 || len(list[0].RemoteValue) != 0 {
		t.Fatalf("note content is intentionally hidden: %+v", list)
	}
}

// TestConvergentMigrationExplicitEnableDropsEarlierChoices checks that choices
// from an abandoned attempt cannot silently resolve a conflict the user is
// shown again from scratch.
func TestConvergentMigrationExplicitEnableDropsEarlierChoices(t *testing.T) {
	service, _, _, _, _ := migrationFieldConflictFixture(t)

	initial := firstConflictPreview(t, service)
	label := conflictForField(t, initial, "label")
	preview, err := service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register:    label.Register,
		Fingerprint: label.Fingerprint,
		Side:        ConvergentConflictKeepLocal,
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
	initial := firstConflictPreview(t, service)
	label := conflictForField(t, initial, "label")
	preview, err := service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register:    label.Register,
		Fingerprint: label.Fingerprint,
		Side:        ConvergentConflictKeepLocal,
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
	// The hostname's own values did not change, so its rendered fingerprint
	// still matches the fresh plan.
	preview, err = service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register:    preview.Conflicts[0].Register,
		Fingerprint: preview.Conflicts[0].Fingerprint,
		Side:        ConvergentConflictKeepRemote,
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

// TestConvergentMigrationRefusesStaleConflictResolution reproduces the stale
// row defect end to end through the real Service path: the row showed
// "From B", the provider changed it to "Changed B", and the old row's
// resolution must not complete the migration with a value the user never saw.
func TestConvergentMigrationRefusesStaleConflictResolution(t *testing.T) {
	service, remote, vault, configs, _ := migrationSingleLabelConflictFixture(t)

	preview := firstConflictPreview(t, service)
	label := conflictForField(t, preview, "label")
	if string(label.RemoteValue) != `"From B"` {
		t.Fatalf("initial remote label = %s, want From B", label.RemoteValue)
	}

	// The user is still looking at the row that rendered "From B" when the
	// provider publishes a different label.
	changedContent := convergentContentForState(t, migrationSingleLabelConflictState(t, "Changed B"), 10000, "device-a")
	installRemoteContent(t, remote, changedContent, 10000)
	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	remote.mu.Unlock()

	preview, err := service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register:    label.Register,
		Fingerprint: label.Fingerprint,
		Side:        ConvergentConflictKeepRemote,
	})
	if !errors.Is(err, ErrConvergentConflictChanged) {
		t.Fatalf("stale resolution error = %v, want ErrConvergentConflictChanged; preview %+v", err, preview)
	}
	if preview.CanInitialize || len(preview.Conflicts) != 1 {
		t.Fatalf("stale resolution preview = %+v", preview)
	}
	fresh := preview.Conflicts[0]
	if string(fresh.RemoteValue) != `"Changed B"` {
		t.Fatalf("refreshed remote label = %s, want Changed B", fresh.RemoteValue)
	}
	if fresh.Fingerprint == label.Fingerprint {
		t.Fatal("a changed remote value kept the stale fingerprint")
	}
	if configs.config.Convergent.SchemaVersion != 0 {
		t.Fatal("stale resolution wrote the v2 marker")
	}
	if recorded := service.migrationResolutionChoices(); len(recorded) != 0 {
		t.Fatalf("stale resolution recorded a choice: %+v", recorded)
	}
	remote.mu.Lock()
	writesAfter := len(remote.writeLog)
	installed := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	if writesAfter != writesBefore || !bytes.Equal(installed, changedContent) {
		t.Fatalf("stale resolution touched the provider (writes=%d, before=%d)", writesAfter, writesBefore)
	}
	vault.mu.Lock()
	local := clonePayload(vault.payload)
	vault.mu.Unlock()
	if len(local.Hosts) != 1 || local.Hosts[0].Label != "Base" {
		t.Fatalf("stale resolution changed the local Vault: %+v", local.Hosts)
	}

	// The refreshed row is a deliberate choice: it resolves and completes.
	preview, err = service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register:    fresh.Register,
		Fingerprint: fresh.Fingerprint,
		Side:        ConvergentConflictKeepRemote,
	})
	if err != nil {
		t.Fatalf("refreshed resolution: %v", err)
	}
	if !preview.CanInitialize || preview.ConflictCount != 0 {
		t.Fatalf("refreshed preview = %+v", preview)
	}
	if configs.config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		t.Fatal("refreshed resolution did not write the v2 marker")
	}
	cloud := remote.payload(t, convergentTestPassword)
	if len(cloud.Hosts) != 1 || cloud.Hosts[0].Label != "Changed B" {
		t.Fatalf("cloud host = %+v", cloud.Hosts)
	}
}

// TestConvergentMigrationBulkRefusesWhenAnyConflictChanged checks that a bulk
// action is a single guarded unit: one changed row refuses every row, and no
// choice is recorded.
func TestConvergentMigrationBulkRefusesWhenAnyConflictChanged(t *testing.T) {
	service, remote, _, configs, _ := migrationFieldConflictFixture(t)

	initial := firstConflictPreview(t, service)
	expected := conflictExpectations(initial.Conflicts)
	changedContent := convergentContentForState(t, migrationConflictedState(t, "Changed B"), 10000, "device-a")
	installRemoteContent(t, remote, changedContent, 10000)
	remote.mu.Lock()
	writesBefore := len(remote.writeLog)
	remote.mu.Unlock()

	preview, err := service.ResolveConvergentMigrationConflicts(context.Background(), ConvergentConflictKeepLocal, expected)
	if !errors.Is(err, ErrConvergentConflictChanged) {
		t.Fatalf("stale bulk error = %v, want ErrConvergentConflictChanged; preview %+v", err, preview)
	}
	if configs.config.Convergent.SchemaVersion != 0 {
		t.Fatal("stale bulk wrote the v2 marker")
	}
	if recorded := service.migrationResolutionChoices(); len(recorded) != 0 {
		t.Fatalf("stale bulk recorded choices: %+v", recorded)
	}
	remote.mu.Lock()
	writesAfter := len(remote.writeLog)
	installed := append([]byte(nil), remote.file.Content...)
	remote.mu.Unlock()
	if writesAfter != writesBefore || !bytes.Equal(installed, changedContent) {
		t.Fatalf("stale bulk touched the provider (writes=%d, before=%d)", writesAfter, writesBefore)
	}
	changed := conflictForField(t, preview, "label")
	if string(changed.RemoteValue) != `"Changed B"` {
		t.Fatalf("refreshed label = %s, want Changed B", changed.RemoteValue)
	}

	// A fresh bulk on the rows the user can now see completes normally.
	preview, err = service.ResolveConvergentMigrationConflicts(context.Background(), ConvergentConflictKeepLocal, conflictExpectations(preview.Conflicts))
	if err != nil {
		t.Fatalf("fresh bulk: %v", err)
	}
	if !preview.CanInitialize || len(preview.Conflicts) != 0 {
		t.Fatalf("fresh bulk preview = %+v", preview)
	}
	cloud := remote.payload(t, convergentTestPassword)
	if len(cloud.Hosts) != 1 || cloud.Hosts[0].Label != "Base" || cloud.Hosts[0].Hostname != "base.example" {
		t.Fatalf("fresh keep-local cloud host = %+v", cloud.Hosts)
	}
}

// TestConvergentMigrationBulkAppliesOnlyToRemainingConflicts documents ticket
// 66's bulk semantics: the bulk action is labeled "remaining conflicts" and
// preserves a per-field choice already made, rather than silently overriding
// it under a "for all" label.
func TestConvergentMigrationBulkAppliesOnlyToRemainingConflicts(t *testing.T) {
	service, remote, _, configs, _ := migrationFieldConflictFixture(t)

	initial := firstConflictPreview(t, service)
	label := conflictForField(t, initial, "label")
	partial, err := service.ResolveConvergentMigrationConflict(context.Background(), ConvergentConflictResolution{
		Register:    label.Register,
		Fingerprint: label.Fingerprint,
		Side:        ConvergentConflictKeepLocal,
	})
	if !errors.Is(err, ErrConvergentMigration) || len(partial.Conflicts) != 1 {
		t.Fatalf("partial resolution = %+v, %v", partial, err)
	}

	// The bulk button was rendered from the remaining hostname row only.
	preview, err := service.ResolveConvergentMigrationConflicts(context.Background(), ConvergentConflictKeepRemote, conflictExpectations(partial.Conflicts))
	if err != nil {
		t.Fatalf("bulk remaining conflicts: %v", err)
	}
	if !preview.CanInitialize || configs.config.Convergent.SchemaVersion != domainsync.ConvergentSchemaVersion {
		t.Fatalf("bulk remaining conflicts did not complete: %+v", preview)
	}
	cloud := remote.payload(t, convergentTestPassword)
	if len(cloud.Hosts) != 1 || cloud.Hosts[0].Label != "Base" || cloud.Hosts[0].Hostname != "b.example" {
		t.Fatalf("intentional mixed outcome = %+v, want local label and remote hostname", cloud.Hosts)
	}
}
