package app

import (
	"archive/zip"
	"context"
	"encoding/base64"
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
	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
)

const (
	sampleProviderID       = "acme.loopback"
	sampleProviderProtocol = "x-acme-loopback"
	sampleProviderHostID   = "host-1"

	// sampleProviderHostKey mirrors SAMPLE_HOST_KEY in
	// sdk/samples/loopback-provider/src/main.c. Keeping the literal here makes
	// the trusted-pin case fail if the sample's key changes, instead of
	// silently testing a stale pin.
	sampleProviderHostKey     = "AAAAC3NzaC1lZDI1NTE5AAAAIGpfps5bf8fgoJblrbNJe/oeii0cdpckYWK7Ixa3wQrR"
	sampleProviderHostKeyType = "ssh-ed25519"
)

// providerSampleRegistrar satisfies the contribution port for a manifest with
// no commands, menus, or keybindings.
type providerSampleRegistrar struct{}

func (providerSampleRegistrar) RegisterCommand(appplugin.PluginRef,
	domainplugin.Command, appplugin.Handler) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (providerSampleRegistrar) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (providerSampleRegistrar) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

// providerSampleTrustPins records the host-side trust lookups and serves the
// seeded records a test wants TrustVerify to classify.
type providerSampleTrustPins struct {
	mu      sync.Mutex
	records []appplugin.TrustRecord
	lookups []struct {
		HostID string
		Kind   appplugin.TrustKind
	}
	saved []appplugin.TrustRecord
}

func (p *providerSampleTrustPins) Pins(_ context.Context, hostID string,
	kind appplugin.TrustKind) ([]appplugin.TrustRecord, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lookups = append(p.lookups, struct {
		HostID string
		Kind   appplugin.TrustKind
	}{hostID, kind})
	var out []appplugin.TrustRecord
	for _, record := range p.records {
		if record.HostID == hostID && record.Kind == kind {
			out = append(out, record)
		}
	}
	return out, nil
}

func (p *providerSampleTrustPins) Save(_ context.Context,
	record appplugin.TrustRecord) error {
	p.mu.Lock()
	p.saved = append(p.saved, record)
	p.records = append(p.records, record)
	p.mu.Unlock()
	return nil
}

func (p *providerSampleTrustPins) Delete(context.Context, appplugin.TrustRecord) error {
	return nil
}

// providerSampleTrustConfirmer answers the host confirmation the sample's
// unknown-material verify call triggers. allow=false models the user declining.
type providerSampleTrustConfirmer struct {
	mu       sync.Mutex
	allow    bool
	requests []appplugin.TrustRequest
}

func (c *providerSampleTrustConfirmer) Confirm(_ context.Context,
	request appplugin.TrustRequest) (bool, error) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	allow := c.allow
	c.mu.Unlock()
	return allow, nil
}

type providerSampleAudit struct {
	mu     sync.Mutex
	events []domainplugin.AuditEvent
}

func (a *providerSampleAudit) Record(event domainplugin.AuditEvent) {
	a.mu.Lock()
	a.events = append(a.events, event)
	a.mu.Unlock()
}

func (a *providerSampleAudit) Close() error { return nil }

func (a *providerSampleAudit) providerEvents() []domainplugin.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	var events []domainplugin.AuditEvent
	for _, event := range a.events {
		if event.Event == domainplugin.AuditProvider {
			events = append(events, event)
		}
	}
	return events
}

func providerSampleArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples",
		"loopback-provider")
	manifest, err := os.ReadFile(filepath.Join(root, "plugin.toml"))
	if err != nil {
		t.Fatalf("read sample manifest: %v", err)
	}
	module, err := os.ReadFile(filepath.Join(root, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read sample module: %v", err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "i18n", "en.toml"))
	if err != nil {
		t.Fatalf("read sample catalog: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), sampleProviderID+".natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	fixed := time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"i18n/en.toml", catalog},
		{"plugin.toml", manifest},
		{"plugin.wasm", module},
	} {
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

// enableProviderSample installs, consents to, and enables the committed sample.
// The returned manager is closed by t.Cleanup.
func enableProviderSample(t *testing.T, pins appplugin.TrustPinStore,
	confirmer appplugin.TrustConfirmer) (*appplugin.Manager, *appplugin.ProviderHost,
	*appsession.ProviderRegistry, *providerSampleAudit,
	*appplugin.MemoryAuthBroker) {

	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	registry := appsession.NewProviderRegistry()
	authBroker := &appplugin.MemoryAuthBroker{}
	audit := &providerSampleAudit{}
	providerHost := appplugin.NewProviderHost(appplugin.ProviderHostOptions{Audit: audit})

	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		providerSampleRegistrar{},
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithAudit(audit),
		appplugin.WithAuthBroker(authBroker),
		appplugin.WithExternalTrust(pins, confirmer),
		appplugin.WithManagerConnectionProviders(providerHost,
			&connectionProviderRegistrar{registry: registry}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	if _, err := manager.InstallFile(ctx, providerSampleArchive(t)); err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	review, err := manager.Review(ctx, sampleProviderID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(review.Required) != 1 ||
		review.Required[0].Permission != domainplugin.PermissionProviderConnection ||
		review.Required[0].Scope.Provider != sampleProviderID {
		t.Fatalf("required consent rows = %+v", review.Required)
	}
	if len(review.Optional) != 2 {
		t.Fatalf("optional consent rows = %+v", review.Optional)
	}
	decisions := []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionProviderConnection, Allow: true},
		{Permission: domainplugin.PermissionAuthInvoke, Allow: true},
		{Permission: domainplugin.PermissionTrustVerify, Allow: true},
	}
	if err := manager.Enable(ctx, sampleProviderID, decisions); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !providerHost.Claimed(sampleProviderProtocol) {
		t.Fatal("the enabled sample did not claim its protocol")
	}
	if _, ok := registry.Resolve(sampleProviderProtocol); !ok {
		t.Fatal("the enabled sample did not publish a transport factory")
	}
	return manager, providerHost, registry, audit, authBroker
}

// sampleProviderKeyIdentity derives the SSH key type and fingerprint the host
// computes for the key constant the sample presents.
func sampleProviderKeyIdentity(t *testing.T) (string, string) {
	t.Helper()
	blob, err := base64.StdEncoding.DecodeString(sampleProviderHostKey)
	if err != nil {
		t.Fatalf("decode sample host key: %v", err)
	}
	return sampleProviderHostKeyType, domainconn.Fingerprint(blob)
}

// TestConnectionProviderSampleEchoesThroughAHostSession drives the committed C
// sample end to end: archive validation, install, consent, enable, container
// activation, wazero provider calls, host-owned auth and trust, the app's
// provider transport factory, and a real Session whose bytes echo back.
func TestConnectionProviderSampleEchoesThroughAHostSession(t *testing.T) {
	ctx := context.Background()
	pins := &providerSampleTrustPins{}
	confirmer := &providerSampleTrustConfirmer{allow: true}
	manager, providerHost, registry, audit, authBroker := enableProviderSample(t, pins, confirmer)

	factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
		withProviders(registry)
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	session, err := service.Open(ctx, appsession.StartSpec{
		Kind:           appsession.KindPlugin,
		Protocol:       sampleProviderProtocol,
		Provider:       sampleProviderID,
		HostID:         sampleProviderHostID,
		Hostname:       "echo.example.test",
		Port:           2222,
		Username:       "demo",
		Label:          "provider sample",
		Cols:           80,
		Rows:           24,
		ProviderConfig: []byte(`{"target":"echo.example.test"}`),
		Restore:        []byte("prior-state"),
	})
	if err != nil {
		t.Fatalf("Open provider Session: %v", err)
	}

	snapshot := session.Snapshot()
	if snapshot.Kind != appsession.KindPlugin || snapshot.Status != appsession.StatusRunning {
		t.Fatalf("initial snapshot = %+v", snapshot)
	}
	if snapshot.Cols != 100 || snapshot.Rows != 30 {
		t.Fatalf("initial size = %dx%d, want 100x30 from the provider", snapshot.Cols, snapshot.Rows)
	}
	if session.ProviderID() != sampleProviderID ||
		session.ProviderProtocol() != sampleProviderProtocol {
		t.Fatalf("provider metadata = %q %q", session.ProviderID(), session.ProviderProtocol())
	}
	if got := string(session.ProviderRestoreDescriptor()); got != "echo-state" {
		t.Fatalf("restore descriptor = %q, want echo-state", got)
	}

	begins := authBroker.Begins()
	if len(begins) != 1 || begins[0].HostID != sampleProviderHostID ||
		begins[0].Method != appplugin.AuthMethodNone {
		t.Fatalf("host auth begins = %+v", begins)
	}
	pins.mu.Lock()
	lookups := append(pins.lookups[:0:0], pins.lookups...)
	saved := len(pins.saved)
	pins.mu.Unlock()
	if len(lookups) != 1 || lookups[0].HostID != sampleProviderHostID ||
		lookups[0].Kind != appplugin.TrustKindSSHHostKey {
		t.Fatalf("host trust lookups = %+v", lookups)
	}
	confirmer.mu.Lock()
	confirmations := len(confirmer.requests)
	confirmer.mu.Unlock()
	if confirmations != 1 || saved != 1 {
		t.Fatalf("host trust confirmations = %d, saved pins = %d", confirmations, saved)
	}

	if result := service.SendInput([]model.SessionID{session.ID()}, []byte("hello provider\n")); result.DeliveredCount() != 1 {
		t.Fatalf("SendInput = %+v", result)
	}
	waitForApp(t, "the provider echo", func() bool {
		return strings.Contains(session.ScreenText().Text, "hello provider")
	})
	if err := session.Resize(120, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := session.Signal("interrupt"); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	if err := session.Signal("not-declared"); err == nil {
		t.Fatal("undeclared signal was accepted")
	}

	if err := manager.Close(ctx); err != nil {
		t.Fatalf("manager.Close: %v", err)
	}
	if providerHost.Claimed(sampleProviderProtocol) {
		t.Fatal("unloading the sample left its protocol claimed")
	}
	if _, ok := registry.Resolve(sampleProviderProtocol); ok {
		t.Fatal("unloading the sample left its transport factory registered")
	}
	waitForApp(t, "the Session to end after unload", func() bool {
		select {
		case <-session.Done():
			return true
		default:
			return false
		}
	})
	snapshot = session.Snapshot()
	if snapshot.ProviderFailure == nil ||
		snapshot.ProviderFailure.Status != appsession.ProviderStatusFaulted ||
		snapshot.ProviderFailure.Provider != sampleProviderID ||
		snapshot.ProviderFailure.Protocol != sampleProviderProtocol {
		t.Fatalf("unload ProviderFailure = %+v, want provider-faulted for %s/%s",
			snapshot.ProviderFailure, sampleProviderID, sampleProviderProtocol)
	}

	events := audit.providerEvents()
	if len(events) == 0 {
		t.Fatal("the provider host recorded no audit events")
	}
	opened := false
	for _, event := range events {
		// An instance-level failure has no single protocol; every other event
		// names the protocol the sample owns.
		if event.Protocol != "" && event.Protocol != sampleProviderProtocol {
			t.Fatalf("provider audit event = %+v", event)
		}
		if event.Protocol == sampleProviderProtocol &&
			strings.HasPrefix(event.Reason, "provider.open:") {
			opened = true
		}
		if strings.Contains(event.Reason, "hello provider") ||
			strings.Contains(event.Status, "hello provider") {
			t.Fatalf("provider audit carried stream bytes: %+v", event)
		}
	}
	if !opened {
		t.Fatalf("provider audit has no open event: %+v", events)
	}
}

// TestConnectionProviderSampleTrustVerdicts runs the committed wasm sample
// against each host-owned trust verdict. The guest may proceed only on
// "trusted"; unknown (including a declined host confirmation), changed, and
// revoked material must fail the open with the typed host-key status and leave
// no pin behind.
func TestConnectionProviderSampleTrustVerdicts(t *testing.T) {
	keyType, fingerprint := sampleProviderKeyIdentity(t)
	pin := func(fingerprint string, revoked bool) []appplugin.TrustRecord {
		return []appplugin.TrustRecord{{
			HostID: sampleProviderHostID, Kind: appplugin.TrustKindSSHHostKey,
			KeyType: keyType, Fingerprint: fingerprint, Revoked: revoked,
		}}
	}
	tests := []struct {
		name              string
		records           []appplugin.TrustRecord
		allow             bool
		wantConfirmations int
		wantOpen          bool
	}{
		{
			name:     "trusted pin opens without confirmation",
			records:  pin(fingerprint, false),
			wantOpen: true,
		},
		{
			name:              "declined confirmation fails closed",
			allow:             false,
			wantConfirmations: 1,
		},
		{
			name:    "changed material fails closed",
			records: pin("sha256:some-other-fingerprint", false),
			allow:   true,
		},
		{
			name:    "revoked material fails closed",
			records: pin(fingerprint, true),
			allow:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pins := &providerSampleTrustPins{
				records: append([]appplugin.TrustRecord(nil), tt.records...),
			}
			confirmer := &providerSampleTrustConfirmer{allow: tt.allow}
			_, _, registry, _, _ := enableProviderSample(t, pins, confirmer)
			factory := newTransportFactory(&factoryLocal{}, nil, nil, nil, nil).
				withProviders(registry)
			service := appsession.NewService(factory)
			t.Cleanup(service.CloseAll)

			session, err := service.Open(ctx, appsession.StartSpec{
				Kind:           appsession.KindPlugin,
				Protocol:       sampleProviderProtocol,
				Provider:       sampleProviderID,
				HostID:         sampleProviderHostID,
				Hostname:       "echo.example.test",
				Port:           2222,
				Username:       "demo",
				Cols:           80,
				Rows:           24,
				ProviderConfig: []byte(`{"target":"echo.example.test"}`),
			})

			pins.mu.Lock()
			lookups := append(pins.lookups[:0:0], pins.lookups...)
			saved := len(pins.saved)
			pins.mu.Unlock()
			if len(lookups) != 1 || lookups[0].HostID != sampleProviderHostID ||
				lookups[0].Kind != appplugin.TrustKindSSHHostKey {
				t.Fatalf("host trust lookups = %+v", lookups)
			}
			confirmer.mu.Lock()
			confirmations := len(confirmer.requests)
			confirmer.mu.Unlock()
			if confirmations != tt.wantConfirmations {
				t.Fatalf("host trust confirmations = %d, want %d",
					confirmations, tt.wantConfirmations)
			}
			if saved != 0 {
				t.Fatalf("trust verification saved %d pins, want none", saved)
			}

			if tt.wantOpen {
				if err != nil {
					t.Fatalf("Open with a trusted pin: %v", err)
				}
				if result := service.SendInput([]model.SessionID{session.ID()},
					[]byte("trusted echo\n")); result.DeliveredCount() != 1 {
					t.Fatalf("SendInput = %+v", result)
				}
				waitForApp(t, "the trusted provider echo", func() bool {
					return strings.Contains(session.ScreenText().Text, "trusted echo")
				})
				return
			}
			if err == nil {
				t.Fatal("the sample opened with an untrusted verdict")
			}
			var typed *domainplugin.ProviderError
			if !errors.As(err, &typed) ||
				typed.Kind != domainplugin.ProviderErrorHostKeyChanged ||
				typed.Provider != sampleProviderID ||
				typed.Protocol != sampleProviderProtocol {
				t.Fatalf("Open error = %v, want host-key-changed for %s/%s",
					err, sampleProviderID, sampleProviderProtocol)
			}
		})
	}
}

func waitForApp(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
