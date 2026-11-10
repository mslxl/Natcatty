package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	"github.com/mslxl/natcatty/application/plugin/fake"
	"github.com/mslxl/natcatty/domain/model/plugindata"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

type fakePluginDataRepo struct {
	records    []appplugin.PluginDataRecord
	quarantine []plugindata.Diagnostic
	raw        map[string][]byte
	cleared    []string
	sets       int
	purged     []string
}

func (r *fakePluginDataRepo) PluginDataRecords(context.Context) ([]appplugin.PluginDataRecord, error) {
	return r.records, nil
}

func (r *fakePluginDataRepo) set(kind plugindata.RecordKind, id string, data plugindata.Map) error {
	r.sets++
	for index := range r.records {
		if r.records[index].Kind == kind && r.records[index].ID == id {
			r.records[index].Data = data
			return nil
		}
	}
	return appplugin.ErrPluginDataNotFound
}

func (r *fakePluginDataRepo) SetHostPluginData(_ context.Context, id string, data plugindata.Map) error {
	return r.set(plugindata.RecordHost, id, data)
}

func (r *fakePluginDataRepo) SetDataProfilePluginData(_ context.Context, id string, data plugindata.Map) error {
	return r.set(plugindata.RecordDataProfile, id, data)
}

func (r *fakePluginDataRepo) PurgePluginData(_ context.Context, pluginID string) error {
	r.purged = append(r.purged, pluginID)
	return nil
}

func (r *fakePluginDataRepo) PluginDataDiagnostics(context.Context) ([]plugindata.Diagnostic, error) {
	return r.quarantine, nil
}

func (r *fakePluginDataRepo) quarantineKey(kind plugindata.RecordKind,
	recordID, pluginID string) string {
	return string(kind) + "\x00" + recordID + "\x00" + pluginID
}

func (r *fakePluginDataRepo) QuarantinedNamespace(_ context.Context,
	kind plugindata.RecordKind, recordID, pluginID string) ([]byte, bool, error) {
	raw, ok := r.raw[r.quarantineKey(kind, recordID, pluginID)]
	return raw, ok, nil
}

func (r *fakePluginDataRepo) ClearPluginDataQuarantine(_ context.Context,
	kind plugindata.RecordKind, recordID, pluginID string) error {
	key := r.quarantineKey(kind, recordID, pluginID)
	delete(r.raw, key)
	r.cleared = append(r.cleared, pluginID)
	return nil
}

type fakePluginDataStates map[string]appplugin.PluginDataProviderState

func (s fakePluginDataStates) PluginDataProviderStates(context.Context) (map[string]appplugin.PluginDataProviderState, error) {
	return s, nil
}

type fakePluginDataSecrets map[string]string

func (s fakePluginDataSecrets) Put(context.Context, string, string, string) (string, error) {
	return "", nil
}
func (s fakePluginDataSecrets) Get(_ context.Context, ref string) (string, error) {
	value, ok := s[ref]
	if !ok {
		return "", appplugin.ErrDataSecretNotFound
	}
	return value, nil
}
func (s fakePluginDataSecrets) Delete(context.Context, string) error { return nil }

func testPluginNamespace(version int, fields map[string]string) plugindata.Namespace {
	values := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		values[key] = json.RawMessage(value)
	}
	return plugindata.Namespace{SchemaVersion: version, Fields: values}
}

func pluginDataFixture() (*fakePluginDataRepo, fakePluginDataStates, fakePluginDataSecrets) {
	hostNamespace := testPluginNamespace(1, map[string]string{"name": `"old"`})
	hostNamespace.Extra = map[string]json.RawMessage{"future": json.RawMessage(`true`)}
	repo := &fakePluginDataRepo{records: []appplugin.PluginDataRecord{
		{Kind: plugindata.RecordHost, ID: "h1", Data: plugindata.Map{"acme.one": hostNamespace}},
		{Kind: plugindata.RecordDataProfile, ID: "p1",
			Provider:   "plugin.acme.data.db",
			SecretRefs: map[string]string{"token": "vault:data-profile:p1:token"},
			Data: plugindata.Map{"acme.one": testPluginNamespace(1,
				map[string]string{"name": `"profile"`})}},
	}}
	states := fakePluginDataStates{
		"acme.one": {Active: true, SchemaVersion: 2},
	}
	return repo, states, fakePluginDataSecrets{}
}

func TestPluginDataReportMarksUnresolvedSecretsDormant(t *testing.T) {
	repo, states, secrets := pluginDataFixture()
	host := appplugin.NewPluginDataHost(repo, states, appplugin.WithPluginDataSecrets(secrets))
	report, err := host.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(report.Namespaces) != 2 {
		t.Fatalf("namespaces = %+v", report.Namespaces)
	}
	// Sorted by record kind then ID: data_profile/p1 before host/h1.
	if report.Namespaces[0].RecordKind != plugindata.RecordDataProfile ||
		report.Namespaces[0].State != plugindata.StateDormant ||
		report.Namespaces[0].Reason != plugindata.ReasonUnresolvedSecret ||
		report.Namespaces[0].Detail != "token" {
		t.Fatalf("profile namespace = %+v", report.Namespaces[0])
	}
	if report.Namespaces[1].RecordKind != plugindata.RecordHost ||
		report.Namespaces[1].State != plugindata.StateActive {
		t.Fatalf("host namespace = %+v", report.Namespaces[1])
	}
	diagnostics, err := host.Diagnostics(context.Background())
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].RecordID != "p1" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	// The unresolved reference stays in the record; nothing was emptied.
	if repo.records[1].SecretRefs["token"] != "vault:data-profile:p1:token" ||
		len(repo.records[1].Data) != 1 {
		t.Fatalf("unresolved profile was rewritten: %+v", repo.records[1])
	}

	// Resolving the reference restores the active state without a migration.
	secrets["vault:data-profile:p1:token"] = "secret"
	report, _ = host.Report(context.Background())
	if report.Namespaces[0].State != plugindata.StateActive {
		t.Fatalf("resolved profile state = %+v", report.Namespaces[0])
	}
}

func TestPluginDataSaveRequiresAnActiveProviderAndPreservesUnknownProperties(t *testing.T) {
	repo, states, _ := pluginDataFixture()
	host := appplugin.NewPluginDataHost(repo, states)
	saved := testPluginNamespace(1, map[string]string{"name": `"new"`})
	if err := host.SaveNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.one", saved); err != nil {
		t.Fatalf("SaveNamespace: %v", err)
	}
	if repo.sets != 1 {
		t.Fatalf("writes = %d, want 1", repo.sets)
	}
	stored := repo.records[0].Data["acme.one"]
	if stored.SchemaVersion != 2 {
		t.Fatalf("schema version = %d, want the active provider's 2", stored.SchemaVersion)
	}
	if string(stored.Fields["name"]) != `"new"` {
		t.Fatalf("fields = %+v", stored.Fields)
	}
	if string(stored.Extra["future"]) != "true" {
		t.Fatal("unknown namespace property was not preserved")
	}

	deep := strings.Repeat("[", plugindata.MaxDepth+1) + "0" + strings.Repeat("]", plugindata.MaxDepth+1)
	err := host.SaveNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.one", testPluginNamespace(1, map[string]string{"deep": deep}))
	var invalid *plugindata.ValidationError
	if !errors.As(err, &invalid) || invalid.Reason != plugindata.ReasonDepth {
		t.Fatalf("invalid save = %v", err)
	}

	// A missing or disabled provider is read-only: no write reaches storage.
	before := repo.sets
	err = host.SaveNamespace(context.Background(), plugindata.RecordHost,
		"h1", "other.two", testPluginNamespace(1, nil))
	if !errors.Is(err, appplugin.ErrPluginDataReadOnly) || repo.sets != before {
		t.Fatalf("inactive save = %v, writes = %d", err, repo.sets)
	}
}

func TestPluginDataNewerSchemaIsDormantAndReadOnlyUntilReinstalled(t *testing.T) {
	repo, states, _ := pluginDataFixture()
	host := appplugin.NewPluginDataHost(repo, states)
	newer := testPluginNamespace(3, map[string]string{"name": `"future"`})
	repo.records = []appplugin.PluginDataRecord{{
		Kind: plugindata.RecordHost, ID: "h1",
		Data: plugindata.Map{"acme.one": newer},
	}}
	report, err := host.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Namespaces[0].State != plugindata.StateNewer ||
		report.Namespaces[0].Reason != plugindata.ReasonSchemaNewer {
		t.Fatalf("newer namespace = %+v", report.Namespaces[0])
	}
	err = host.SaveNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.one", testPluginNamespace(3, map[string]string{"name": `"edit"`}))
	if !errors.Is(err, appplugin.ErrPluginDataReadOnly) {
		t.Fatalf("edit of a newer namespace = %v", err)
	}

	// A provider update to version 3 makes the same data editable again, with
	// no automatic migration of the stored value.
	states["acme.one"] = appplugin.PluginDataProviderState{Active: true, SchemaVersion: 3}
	if err := host.SaveNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.one", testPluginNamespace(3, map[string]string{"name": `"edit"`})); err != nil {
		t.Fatalf("save after provider update: %v", err)
	}
	if string(repo.records[0].Data["acme.one"].Fields["name"]) != `"edit"` {
		t.Fatal("edited namespace was not stored")
	}
}

func TestPluginDataDeletionAndPurgeRequireExplicitConfirmation(t *testing.T) {
	repo := &fakePluginDataRepo{records: []appplugin.PluginDataRecord{{
		Kind: plugindata.RecordHost, ID: "h1", Data: plugindata.Map{
			"acme.one":  testPluginNamespace(1, nil),
			"other.two": testPluginNamespace(1, nil),
		},
	}}}
	states := fakePluginDataStates{}
	host := appplugin.NewPluginDataHost(repo, states)

	if err := host.DeleteNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.one", false); !errors.Is(err, appplugin.ErrPluginDataConfirmationRequired) {
		t.Fatalf("unconfirmed delete = %v", err)
	}
	if err := host.DeleteNamespace(context.Background(), plugindata.RecordHost,
		"h1", "acme.one", true); err != nil {
		t.Fatalf("confirmed delete: %v", err)
	}
	if _, ok := repo.records[0].Data["acme.one"]; ok {
		t.Fatal("deleted namespace stayed in the record")
	}
	if _, ok := repo.records[0].Data["other.two"]; !ok {
		t.Fatal("delete removed another plugin's namespace")
	}
	if err := host.Purge(context.Background(), "acme.one", false); !errors.Is(err, appplugin.ErrPluginDataConfirmationRequired) {
		t.Fatalf("unconfirmed purge = %v", err)
	}
	if len(repo.purged) != 0 {
		t.Fatalf("unconfirmed purge reached the repository: %+v", repo.purged)
	}
	if err := host.Purge(context.Background(), "acme.one", true); err != nil {
		t.Fatalf("confirmed purge: %v", err)
	}
	if len(repo.purged) != 1 || repo.purged[0] != "acme.one" {
		t.Fatalf("purged = %+v", repo.purged)
	}
}

func TestPluginDataDiagnosticsIncludesQuarantinedNamespaces(t *testing.T) {
	repo, states, _ := pluginDataFixture()
	repo.quarantine = []plugindata.Diagnostic{{
		RecordKind: plugindata.RecordDataProfile, RecordID: "p2",
		PluginID: "acme.one", State: plugindata.StateQuarantined,
		Reason: plugindata.ReasonDepth, Detail: "deep",
	}}
	host := appplugin.NewPluginDataHost(repo, states)
	diagnostics, err := host.Diagnostics(context.Background())
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	seenQuarantine := false
	for _, diagnostic := range diagnostics {
		if diagnostic.State == plugindata.StateQuarantined &&
			diagnostic.RecordID == "p2" && diagnostic.Detail == "deep" {
			seenQuarantine = true
		}
	}
	if !seenQuarantine {
		t.Fatalf("quarantine was not reported: %+v", diagnostics)
	}
}

func TestManagerProviderStatesFollowLiveFiberLifecycle(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"}, nil, nil)
	manifest.PluginDataSchemaVersion = 4
	audit := &recordingAudit{}
	manager, _, validator, _ := managerFixture(t, manifest, nil, nil, audit)
	record := installForManager(t, manager, validator)

	states, err := manager.PluginDataProviderStates(context.Background())
	if err != nil {
		t.Fatalf("states: %v", err)
	}
	if state, ok := states[record.ID]; ok && state.Active {
		t.Fatalf("disabled plugin is active: %+v", state)
	}
	if err := manager.Enable(context.Background(), record.ID,
		[]domainplugin.PermissionDecision{
			{Permission: domainplugin.PermissionCommands, Allow: true},
		}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	states, err = manager.PluginDataProviderStates(context.Background())
	if err != nil {
		t.Fatalf("states after enable: %v", err)
	}
	state, ok := states[record.ID]
	if !ok || !state.Active || state.SchemaVersion != 4 {
		t.Fatalf("active state = %+v (found %v)", state, ok)
	}
	if err := manager.Disable(context.Background(), record.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	states, _ = manager.PluginDataProviderStates(context.Background())
	if state, ok := states[record.ID]; ok && state.Active {
		t.Fatalf("disabled plugin is still active: %+v", state)
	}
}

type recordingPurger struct {
	mu  sync.Mutex
	ids []string
}

func (p *recordingPurger) PurgePluginData(_ context.Context, pluginID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ids = append(p.ids, pluginID)
	return nil
}

func (p *recordingPurger) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.ids...)
}

type fakePluginDataDiagnostics []plugindata.Diagnostic

func (d fakePluginDataDiagnostics) Diagnostics(context.Context) ([]plugindata.Diagnostic, error) {
	return d, nil
}

func managerWithPurger(t *testing.T, manifest domainplugin.Manifest,
	purger appplugin.PluginDataPurger,
	diagnostics appplugin.PluginDataDiagnosticSource,
) (*appplugin.Manager, *memoryStore, *fixedValidator) {
	t.Helper()
	files := map[string][]byte{
		"plugin.toml": []byte("manifest"),
		"plugin.wasm": fixtureModule,
	}
	validator := &fixedValidator{pkg: appplugin.Package{
		Manifest: manifest, Files: files, SHA256: "package-hash",
	}}
	store := newMemoryStore(t)
	options := []appplugin.ManagerOption{
		appplugin.WithAudit(&recordingAudit{}),
		appplugin.WithPluginDataPurger(purger),
		appplugin.WithManagerClock(func() time.Time {
			return time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
		}),
	}
	if diagnostics != nil {
		options = append(options, appplugin.WithManagerPluginDataDiagnostics(diagnostics))
	}
	manager := appplugin.NewManager(fake.New(), fixedManifests{manifest: manifest},
		&registrar{}, validator, store, options...)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager, store, validator
}

func TestManagerDiagnosticsReportDormantPluginData(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"}, nil, nil)
	diagnostics := fakePluginDataDiagnostics{{
		RecordKind: plugindata.RecordHost, RecordID: "h1", PluginID: "acme.one",
		State: plugindata.StateDormant, Reason: plugindata.ReasonProviderUnavailable,
	}}
	manager, _, _ := managerWithPurger(t, manifest, nil, diagnostics)
	report := manager.DiagnosticsReport()
	if len(report.PluginData) != 1 || report.PluginData[0].PluginID != "acme.one" ||
		report.PluginData[0].State != plugindata.StateDormant {
		t.Fatalf("PluginData diagnostics = %+v", report.PluginData)
	}
}

func TestManagerPurgesProviderDataOnlyOnExplicitPurge(t *testing.T) {
	manifest := managerManifest("acme.manager", []string{"commands"}, nil, nil)
	purger := &recordingPurger{}
	manager, _, validator := managerWithPurger(t, manifest, purger, nil)
	record := installForManager(t, manager, validator)
	if err := manager.PurgeData(context.Background(), record.ID); err != nil {
		t.Fatalf("PurgeData: %v", err)
	}
	if calls := purger.calls(); len(calls) != 1 || calls[0] != record.ID {
		t.Fatalf("purge calls = %+v", calls)
	}

	// A plain uninstall keeps provider data.
	if err := manager.Uninstall(context.Background(), record.ID, false); err != nil {
		t.Fatalf("Uninstall(false): %v", err)
	}
	if calls := purger.calls(); len(calls) != 1 {
		t.Fatalf("plain uninstall purged provider data: %+v", calls)
	}

	// Uninstall with the separate purge confirmation removes it.
	record = installForManager(t, manager, validator)
	if err := manager.Uninstall(context.Background(), record.ID, true); err != nil {
		t.Fatalf("Uninstall(true): %v", err)
	}
	if calls := purger.calls(); len(calls) != 2 || calls[1] != record.ID {
		t.Fatalf("purge calls after uninstall = %+v", calls)
	}
}

func TestPluginDataReportSurfacesAProfileSecretWithoutNamespaces(t *testing.T) {
	repo := &fakePluginDataRepo{records: []appplugin.PluginDataRecord{{
		Kind: plugindata.RecordDataProfile, ID: "p2",
		Provider:   "plugin.acme.data.db",
		SecretRefs: map[string]string{"token": "vault:data-profile:p2:token"},
	}}}
	host := appplugin.NewPluginDataHost(repo, fakePluginDataStates{},
		appplugin.WithPluginDataSecrets(fakePluginDataSecrets{}))
	report, err := host.Report(context.Background())
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if len(report.Namespaces) != 1 ||
		report.Namespaces[0].RecordID != "p2" ||
		report.Namespaces[0].PluginID != "acme.data" ||
		report.Namespaces[0].State != plugindata.StateDormant ||
		report.Namespaces[0].Reason != plugindata.ReasonUnresolvedSecret ||
		report.Namespaces[0].Detail != "token" {
		t.Fatalf("profile status = %+v", report.Namespaces)
	}
	diagnostics, err := host.Diagnostics(context.Background())
	if err != nil {
		t.Fatalf("Diagnostics: %v", err)
	}
	if len(diagnostics) != 1 || diagnostics[0].RecordID != "p2" {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
}
