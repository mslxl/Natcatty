package app

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adapterplugininstall "github.com/mslxl/natcatty/adapters/plugininstall"
	adapterpluginmanifest "github.com/mslxl/natcatty/adapters/pluginmanifest"
	adapterpluginpackage "github.com/mslxl/natcatty/adapters/pluginpackage"
	adapterwasm "github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
)

const (
	dataSampleID         = "acme.data"
	dataSampleProviderID = "plugin.acme.data.warehouse"

	dataSampleSecret         = "sample-password-value"
	dataSampleReadOnlySecret = "sample-readonly-value"
)

// dataSampleAudit records the content-free audit rows the manager and the
// data-provider host emit.
type dataSampleAudit struct {
	mu     sync.Mutex
	events []domainplugin.AuditEvent
}

func (a *dataSampleAudit) Record(event domainplugin.AuditEvent) {
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
}

func (a *dataSampleAudit) Close() error { return nil }

func (a *dataSampleAudit) dataEvents() []domainplugin.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var events []domainplugin.AuditEvent
	for _, event := range a.events {
		if event.Event == domainplugin.AuditData {
			events = append(events, event)
		}
	}
	return events
}

// dataSampleSchedule is a deterministic stand-in for the idle-TTL timer. The
// host schedules the 5-minute expiry when a connection is released; a test
// fires it without waiting.
type dataSampleSchedule struct {
	mu      sync.Mutex
	delays  []time.Duration
	pending []*dataSampleTimer
}

type dataSampleTimer struct {
	stopped bool
	fired   bool
	fn      func()
}

func (s *dataSampleSchedule) Schedule(delay time.Duration, fn func()) func() {
	timer := &dataSampleTimer{fn: fn}
	s.mu.Lock()
	s.delays = append(s.delays, delay)
	s.pending = append(s.pending, timer)
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		timer.stopped = true
		s.mu.Unlock()
	}
}

func (s *dataSampleSchedule) FireAll() int {
	s.mu.Lock()
	timers := append([]*dataSampleTimer(nil), s.pending...)
	s.mu.Unlock()
	fired := 0
	for _, timer := range timers {
		s.mu.Lock()
		run := !timer.stopped && !timer.fired
		if run {
			timer.fired = true
		}
		s.mu.Unlock()
		if run {
			timer.fn()
			fired++
		}
	}
	return fired
}

func (s *dataSampleSchedule) Delays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

// dataProviderSampleArchive packs the committed C sample the way the package
// validator and installer expect.
func dataProviderSampleArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "data-provider")
	var files []struct {
		name string
		data []byte
	}
	for _, entry := range []string{
		filepath.Join("i18n", "en.toml"),
		filepath.Join("plugin.toml"),
		filepath.Join("plugin.wasm"),
	} {
		data, err := os.ReadFile(filepath.Join(root, entry))
		if err != nil {
			t.Fatalf("read sample %s: %v", entry, err)
		}
		files = append(files, struct {
			name string
			data []byte
		}{filepath.ToSlash(entry), data})
	}
	archivePath := filepath.Join(t.TempDir(), dataSampleID+".natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, entry := range files {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetModTime(fixed)
		header.SetMode(0o644)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create %s: %v", entry.name, err)
		}
		if _, err := part.Write(entry.data); err != nil {
			t.Fatalf("write %s: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close archive file: %v", err)
	}
	return archivePath
}

// enableDataProviderSample installs and enables the committed WASM sample with
// a data-provider host wired to the in-memory profile ports. The manager is
// closed by t.Cleanup.
func enableDataProviderSample(t *testing.T, dataHost *appplugin.DataProviderHost,
	audit *dataSampleAudit) *appplugin.Manager {

	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		providerSampleRegistrar{},
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithAudit(audit),
		appplugin.WithManagerDataProviders(dataHost),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	if _, err := manager.InstallFile(ctx, dataProviderSampleArchive(t)); err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	review, err := manager.Review(ctx, dataSampleID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(review.Required) != 1 ||
		review.Required[0].Permission != domainplugin.PermissionProviderData ||
		review.Required[0].Scope.Provider != dataSampleID {
		t.Fatalf("required consent rows = %+v", review.Required)
	}
	if err := manager.Enable(ctx, dataSampleID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionProviderData, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, ok := dataHost.Lookup(dataSampleProviderID); !ok {
		t.Fatal("the enabled sample did not register its data provider")
	}
	return manager
}

// TestDataProviderSampleServesProfilesQueriesAndPooling drives the committed C
// sample end to end: archive validation, install, consent, enable, container
// activation, wazero data-provider calls, profile persistence in the Vault
// ports, the typed result envelope, the interactive budget, cooperative
// cancellation, access-mode enforcement, and pooled connections that ping
// before reuse and expire on the idle TTL.
func TestDataProviderSampleServesProfilesQueriesAndPooling(t *testing.T) {
	ctx := context.Background()
	audit := &dataSampleAudit{}
	scheduler := &dataSampleSchedule{}
	dataHost := appplugin.NewDataProviderHost(appplugin.DataHostOptions{
		Audit:    audit,
		Schedule: scheduler.Schedule,
		Limits:   appplugin.DataLimits{InteractiveRows: 4},
		Profiles: appplugin.NewMemoryDataProfileStore(),
		Secrets:  appplugin.NewMemoryDataProfileSecrets(),
	})
	t.Cleanup(dataHost.Close)
	manager := enableDataProviderSample(t, dataHost, audit)
	_ = manager

	provider, ok := dataHost.Lookup(dataSampleProviderID)
	if !ok {
		t.Fatalf("provider %q is not registered", dataSampleProviderID)
	}
	if provider.MaxAccessMode() != domainplugin.DataModeReadWrite ||
		provider.Contribution().Capabilities.Family != domainplugin.DataFamilyTabular {
		t.Fatalf("provider declaration = %+v, grant %q",
			provider.Contribution(), provider.MaxAccessMode())
	}

	profile, err := dataHost.SaveProfile(ctx, "", domainplugin.DataProfileDraft{
		Label:    "Sample warehouse",
		Provider: dataSampleProviderID,
		Mode:     domainplugin.DataModeReadWrite,
		Fields: map[string]string{
			"endpoint": "warehouse.example.test:443",
		},
		Secrets: map[string]string{"password": dataSampleSecret},
	})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if profile.ID == "" || profile.SecretRefs["password"] == "" ||
		strings.Contains(profile.SecretRefs["password"], dataSampleSecret) {
		t.Fatalf("saved profile = %+v", profile)
	}
	if got, err := dataHost.GetProfile(ctx, profile.ID); err != nil || got.ID != profile.ID {
		t.Fatalf("GetProfile = %+v, %v", got, err)
	}
	if list, err := dataHost.ListProfiles(ctx); err != nil || len(list) != 1 {
		t.Fatalf("ListProfiles = %+v, %v", list, err)
	}

	// A profile test returns latency, server identity, and an auth verdict and
	// never echoes the credential.
	testResult, err := dataHost.TestProfile(ctx, profile.ID)
	if err != nil {
		t.Fatalf("TestProfile: %v", err)
	}
	if testResult.ServerIdentity != "sample-warehouse" || testResult.AuthResult != "accepted" {
		t.Fatalf("profile test = %+v", testResult)
	}
	if testResult.LatencyMS < 0 {
		t.Fatalf("profile test latency = %d", testResult.LatencyMS)
	}
	if strings.Contains(testResult.ServerIdentity, dataSampleSecret) ||
		strings.Contains(testResult.AuthResult, dataSampleSecret) {
		t.Fatal("profile test echoed the profile secret")
	}

	lease, err := dataHost.Acquire(ctx, profile.ID, domainplugin.DataModeReadWrite)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.ProfileID() != profile.ID ||
		lease.Mode() != domainplugin.DataModeReadWrite ||
		lease.ProviderID() != dataSampleProviderID ||
		lease.ConnectionID() == 0 {
		t.Fatalf("lease = %q %q %q %d", lease.ProfileID(), lease.Mode(),
			lease.ProviderID(), lease.ConnectionID())
	}
	firstConnection := lease.ConnectionID()

	refined, err := lease.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if refined.Family != domainplugin.DataFamilyTabular ||
		!refined.Supports(domainplugin.DataFeatureCancellation) {
		t.Fatalf("refined capabilities = %+v", refined)
	}

	// Typed values survive the ABI: an int beyond float64 precision and a
	// decimal with more than float64 digits stay exact strings.
	query, typed, err := lease.Query(ctx, appplugin.DataQueryRequest{Query: "typed"})
	if err != nil {
		t.Fatalf("typed query: %v", err)
	}
	if typed.Kind != domainplugin.DataResultTable || typed.Table == nil ||
		len(typed.Table.Rows) != 1 || len(typed.Table.Rows[0]) != 3 {
		t.Fatalf("typed result = %+v", typed)
	}
	if got := typed.Table.Rows[0][0]; got.Kind != domainplugin.DataValueInt ||
		got.Int != "9007199254740993" {
		t.Fatalf("int value = %+v", got)
	}
	if got := typed.Table.Rows[0][1]; got.Kind != domainplugin.DataValueDecimal ||
		got.Decimal != "1234567890.000000000000000001" {
		t.Fatalf("decimal value = %+v", got)
	}
	if got := typed.Table.Rows[0][2]; got.Kind != domainplugin.DataValueString ||
		got.String != "ok" {
		t.Fatalf("string value = %+v", got)
	}
	if query.Class() != domainplugin.DataClassRead {
		t.Fatalf("typed query class = %q", query.Class())
	}

	// Optional methods stay reachable through the real ABI.
	metadata, err := lease.Metadata(ctx, "", "")
	if err != nil || len(metadata.Nodes) != 1 || metadata.Nodes[0].Name != "tables" {
		t.Fatalf("Metadata = %+v, %v", metadata, err)
	}
	transaction, err := lease.Begin(ctx)
	if err != nil || transaction != 21 {
		t.Fatalf("Begin = %d, %v", transaction, err)
	}
	if err := lease.Commit(ctx, transaction); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	plan, err := lease.Plan(ctx, "select 1", nil)
	if err != nil || plan.Kind != domainplugin.DataResultPlan ||
		plan.Plan == nil || plan.Plan.Text != "sample scan" {
		t.Fatalf("Plan = %+v, %v", plan, err)
	}

	// The first batch of three rows fits the four-row budget; the host trims
	// the next batch to one row and reports Limit instead of asking for more.
	rowsQuery, firstBatch, err := lease.Query(ctx, appplugin.DataQueryRequest{Query: "rows"})
	if err != nil {
		t.Fatalf("rows query: %v", err)
	}
	if firstBatch.DataRows() != 3 || firstBatch.Limit || !firstBatch.More {
		t.Fatalf("first batch = %+v", firstBatch)
	}
	secondBatch, err := rowsQuery.Fetch(ctx)
	if err != nil {
		t.Fatalf("budget fetch: %v", err)
	}
	if secondBatch.DataRows() != 1 || !secondBatch.Limit || secondBatch.More ||
		secondBatch.Partial {
		t.Fatalf("budget fetch = %+v", secondBatch)
	}

	// The guest polls the host cancellation import while fetching. The host
	// marks the delivered batch partial and clears More.
	cancelQuery, cancelFirst, err := lease.Query(ctx, appplugin.DataQueryRequest{Query: "cancel"})
	if err != nil {
		t.Fatalf("cancel query: %v", err)
	}
	if cancelFirst.DataRows() != 2 || !cancelFirst.More {
		t.Fatalf("cancel first batch = %+v", cancelFirst)
	}
	fetchCtx, stopFetch := context.WithTimeout(ctx, 250*time.Millisecond)
	partial, err := cancelQuery.Fetch(fetchCtx)
	stopFetch()
	if err != nil {
		t.Fatalf("cancelled fetch: %v", err)
	}
	if !partial.Partial || partial.More || partial.Limit || partial.DataRows() != 2 {
		t.Fatalf("cancelled fetch = %+v", partial)
	}
	if partial.Table == nil || partial.Table.Rows[0][0].Int != "10" ||
		partial.Table.Rows[1][0].Int != "11" {
		t.Fatalf("cancelled fetch rows = %+v", partial)
	}

	// Release pools the connection. A second acquire pings it and reuses the
	// same provider token instead of opening another connection.
	lease.Release()
	if got := dataHost.DataPoolSize(profile.ID, domainplugin.DataModeReadWrite); got != 1 {
		t.Fatalf("pool size after release = %d", got)
	}
	reused, err := dataHost.Acquire(ctx, profile.ID, domainplugin.DataModeReadWrite)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if reused.ConnectionID() != firstConnection {
		t.Fatalf("reused connection = %d, want %d",
			reused.ConnectionID(), firstConnection)
	}
	if got := dataHost.DataPoolSize(profile.ID, domainplugin.DataModeReadWrite); got != 0 {
		t.Fatalf("pool size while leased = %d", got)
	}
	reused.Release()

	delays := scheduler.Delays()
	if len(delays) == 0 || delays[len(delays)-1] != appplugin.DefaultDataIdleTTL {
		t.Fatalf("idle schedule delays = %v", delays)
	}
	if fired := scheduler.FireAll(); fired == 0 {
		t.Fatal("the idle TTL timer did not fire")
	}
	if dataHost.DataPoolSize(profile.ID, domainplugin.DataModeReadWrite) != 0 ||
		dataHost.DataConnectionCount(profile.ID) != 0 {
		t.Fatalf("idle expiry left %d pooled / %d open connections",
			dataHost.DataPoolSize(profile.ID, domainplugin.DataModeReadWrite),
			dataHost.DataConnectionCount(profile.ID))
	}
	fresh, err := dataHost.Acquire(ctx, profile.ID, domainplugin.DataModeReadWrite)
	if err != nil {
		t.Fatalf("acquire after idle expiry: %v", err)
	}
	if fresh.ConnectionID() == firstConnection {
		t.Fatalf("idle expiry reused connection %d", firstConnection)
	}
	fresh.Release()

	// Access modes are selected by the user and enforced by the host. A
	// read-only profile cannot run a write-class operation or be escalated to
	// read-write; a read-write grant cannot be escalated to full.
	readOnly, err := dataHost.SaveProfile(ctx, "", domainplugin.DataProfileDraft{
		Label:    "Sample warehouse read only",
		Provider: dataSampleProviderID,
		Mode:     domainplugin.DataModeReadOnly,
		Fields: map[string]string{
			"endpoint": "warehouse.example.test:443",
		},
		Secrets: map[string]string{"password": dataSampleReadOnlySecret},
	})
	if err != nil {
		t.Fatalf("SaveProfile read-only: %v", err)
	}
	readLease, err := dataHost.Acquire(ctx, readOnly.ID, domainplugin.DataModeReadOnly)
	if err != nil {
		t.Fatalf("acquire read-only: %v", err)
	}
	if _, _, err := readLease.Query(ctx, appplugin.DataQueryRequest{Query: "write"}); err == nil {
		t.Fatal("a write-class query was allowed in read-only mode")
	} else {
		var dataErr *appplugin.DataError
		if !errors.As(err, &dataErr) || dataErr.Kind != appplugin.DataErrorDenied {
			t.Fatalf("write denial = %v", err)
		}
	}
	readLease.Release()
	if _, err := dataHost.Acquire(ctx, readOnly.ID,
		domainplugin.DataModeReadWrite); err == nil {
		t.Fatal("a read-only profile was escalated to read-write")
	}
	if _, err := dataHost.Acquire(ctx, profile.ID, domainplugin.DataModeFull); err == nil {
		t.Fatal("the read-write grant was escalated to full")
	}

	// Audit rows carry actions, modes, classes, sizes, and durations only.
	events := audit.dataEvents()
	var sawOpen, sawTyped, sawDenied, sawIdle bool
	for _, event := range events {
		if strings.Contains(event.Reason, dataSampleSecret) ||
			strings.Contains(event.Reason, dataSampleReadOnlySecret) ||
			strings.Contains(event.Status, dataSampleSecret) {
			t.Fatalf("data audit carried secret content: %+v", event)
		}
		switch event.Reason {
		case "data.open:ok":
			sawOpen = true
		case "data.query:ok":
			if event.Class == string(domainplugin.DataClassRead) {
				sawTyped = true
			}
		case "data.query:denied", "data.open:escalation":
			sawDenied = true
		case "data.idle:expired":
			sawIdle = true
		}
	}
	if !sawOpen || !sawTyped || !sawDenied || !sawIdle {
		t.Fatalf("data audit = %+v", events)
	}
}
