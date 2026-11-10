package plugin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"

	appplugin "github.com/mslxl/natcatty/application/plugin"
)

// auditRecorder captures provider audit events for assertions.
type auditRecorder struct {
	mu     sync.Mutex
	events []domainplugin.AuditEvent
}

func (r *auditRecorder) Record(event domainplugin.AuditEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *auditRecorder) Close() error { return nil }

func (r *auditRecorder) snapshot() []domainplugin.AuditEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domainplugin.AuditEvent(nil), r.events...)
}

// fakeProviderGuest is the fiber-side provider port. It records requests and
// lets a test answer as the guest would through the stream callbacks.
type fakeProviderGuest struct {
	supports bool
	handler  func(ctx context.Context, host *appplugin.ProviderHost,
		fiber domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error)

	mu    sync.Mutex
	calls []appplugin.ProviderRequest
}

func (g *fakeProviderGuest) SupportsProvider() bool { return g.supports }

func (g *fakeProviderGuest) InvokeProvider(ctx context.Context,
	host *appplugin.ProviderHost, fiber domainplugin.FiberID,
	request appplugin.ProviderRequest) ([]byte, error) {
	g.mu.Lock()
	g.calls = append(g.calls, request)
	g.mu.Unlock()
	if g.handler != nil {
		return g.handler(ctx, host, fiber, request)
	}
	return []byte(`{"status":"ok"}`), nil
}

// fiberProviderGuest adapts fakeProviderGuest to ProviderInvoker with the
// fiber identity bound.
type fiberProviderGuest struct {
	guest *fakeProviderGuest
	host  *appplugin.ProviderHost
	fiber domainplugin.FiberID
}

func (g fiberProviderGuest) SupportsProvider() bool { return g.guest.SupportsProvider() }

func (g fiberProviderGuest) InvokeProvider(ctx context.Context,
	request appplugin.ProviderRequest) ([]byte, error) {
	return g.guest.InvokeProvider(ctx, g.host, g.fiber, request)
}

func sampleConnection(protocol string) domainplugin.ConnectionProvider {
	return domainplugin.ConnectionProvider{
		Protocol: protocol,
		Name:     "Acme Echo",
		HostFields: []domainplugin.ConnectionField{
			{Key: "target", Label: "Target", Kind: "text", Required: true},
		},
		Capabilities: domainplugin.ConnectionCapabilities{
			Resize:          true,
			Probe:           true,
			Reconnect:       true,
			Signals:         []string{"interrupt", "terminate"},
			MaxSessions:     4,
			RestoreStateMax: 64,
		},
		AuthMethods:   []string{"none"},
		RestoreSchema: []string{"token"},
	}
}

func newProviderHost(t *testing.T, audit appplugin.AuditSink,
	limits appplugin.ProviderLimits) *appplugin.ProviderHost {
	t.Helper()
	host := appplugin.NewProviderHost(appplugin.ProviderHostOptions{
		Audit: audit, Limits: limits,
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	})
	t.Cleanup(host.Close)
	return host
}

func registerProvider(t *testing.T, host *appplugin.ProviderHost,
	fiber domainplugin.FiberID, id, protocol string,
	guest *fakeProviderGuest) *appplugin.Provider {
	t.Helper()
	provider, err := host.Register(fiber, appplugin.PluginRef{ID: id},
		sampleConnection(protocol), fiberProviderGuest{guest: guest, host: host, fiber: fiber})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return provider
}

func openPayload(t *testing.T, request appplugin.ProviderRequest) (uint32, int, int) {
	t.Helper()
	var payload struct {
		Stream uint32 `json:"stream"`
		Cols   int    `json:"cols"`
		Rows   int    `json:"rows"`
	}
	if err := json.Unmarshal(request.Payload, &payload); err != nil {
		t.Fatalf("decode open payload: %v", err)
	}
	return payload.Stream, payload.Cols, payload.Rows
}

func TestProviderHostClaimsOneProtocol(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	first := &fakeProviderGuest{supports: true}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", first)
	if !host.Claimed("x-acme-echo") {
		t.Fatal("registered protocol is not claimed")
	}

	second := &fakeProviderGuest{supports: true}
	_, err := host.Register("installed/other/1", appplugin.PluginRef{ID: "other.echo"},
		sampleConnection("x-acme-echo"), fiberProviderGuest{guest: second, host: host,
			fiber: "installed/other/1"})
	if !errors.Is(err, protocolmodel.ErrDuplicateProtocol) {
		t.Fatalf("duplicate claim error = %v, want ErrDuplicateProtocol", err)
	}

	if _, err := host.Register("installed/other/1", appplugin.PluginRef{ID: "other.echo"},
		sampleConnection("ssh"), fiberProviderGuest{guest: second, host: host,
			fiber: "installed/other/1"}); err == nil {
		t.Fatal("Register accepted a built-in protocol")
	}

	withoutExport := &fakeProviderGuest{}
	if _, err := host.Register("installed/other/1", appplugin.PluginRef{ID: "other.echo"},
		sampleConnection("x-other-echo"), fiberProviderGuest{guest: withoutExport,
			host: host, fiber: "installed/other/1"}); err == nil ||
		!strings.Contains(err.Error(), domainpluginabi.ExportProviderRequest) {
		t.Fatalf("missing-export error = %v", err)
	}

	if err := host.Unregister(provider); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if host.Claimed("x-acme-echo") {
		t.Fatal("Unregister left the protocol claimed")
	}
	registerProvider(t, host, "installed/other/1", "other.echo", "x-acme-echo", second)
}

func TestProviderOpenReturnsDimensionsAndRestore(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	guest := &fakeProviderGuest{supports: true}
	guest.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		if request.Method != domainpluginabi.ProviderMethodOpen {
			return []byte(`{"status":"ok"}`), nil
		}
		return []byte(`{"status":"ok","cols":132,"rows":43,"restore":"` +
			base64.StdEncoding.EncodeToString([]byte("opaque")) + `"}`), nil
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)

	stream, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Port: 2222, Username: "user",
		Label: "Example", Cols: 80, Rows: 24,
		Config:  []byte(`{"target":"loopback"}`),
		Restore: []byte("prior"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	if cols, rows := stream.InitialSize(); cols != 132 || rows != 43 {
		t.Fatalf("InitialSize = %d,%d; want 132,43", cols, rows)
	}
	if got := string(stream.RestoreDescriptor()); got != "opaque" {
		t.Fatalf("RestoreDescriptor = %q", got)
	}
	if stream.ProviderID() != "acme.echo" || stream.Protocol() != "x-acme-echo" {
		t.Fatalf("metadata = %q %q", stream.ProviderID(), stream.Protocol())
	}
}

func TestProviderOpenIsCancellable(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{
		OpenTimeout: 5 * time.Second,
	})
	started := make(chan uint64, 1)
	sawCancel := make(chan bool, 1)
	var opens atomic.Int32
	guest := &fakeProviderGuest{supports: true}
	guest.handler = func(ctx context.Context, host *appplugin.ProviderHost,
		fiber domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		if request.Method != domainpluginabi.ProviderMethodOpen {
			return []byte(`{"status":"ok"}`), nil
		}
		if opens.Add(1) > 1 {
			return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
		}
		started <- request.ID
		callbacks := host.Callbacks(fiber)
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			if callbacks.Cancelled(request.ID) {
				sawCancel <- true
				// Answer success after the cancellation poll. The host stopped
				// waiting, so this half-open stream must be discarded anyway.
				return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
			}
			select {
			case <-ctx.Done():
				sawCancel <- false
				return nil, ctx.Err()
			case <-ticker.C:
			}
		}
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := provider.Open(ctx, appplugin.ProviderOpenRequest{
			HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
		})
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("open never reached the provider")
	}
	cancel()
	select {
	case err := <-result:
		var typed *domainplugin.ProviderError
		if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorCancelled {
			t.Fatalf("cancelled open error = %v, want cancelled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled open did not return")
	}
	if !<-sawCancel {
		t.Fatal("guest never observed the cooperative cancellation poll")
	}

	// The rejected open left no reserved stream or session slot behind.
	if _, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
	}); err != nil {
		t.Fatalf("open after cancellation: %v", err)
	}
}

func TestProviderErrorsUseTypedStatuses(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	guest := &fakeProviderGuest{supports: true}
	guest.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		switch request.Method {
		case domainpluginabi.ProviderMethodOpen:
			return []byte(`{"status":"auth-failed","message":"host rejected the credential"}`), nil
		case domainpluginabi.ProviderMethodValidate:
			return []byte(`{"status":"made-up"}`), nil
		default:
			return []byte(`{"status":"unreachable"}`), nil
		}
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)

	_, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
	})
	var typed *domainplugin.ProviderError
	if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorAuthFailed {
		t.Fatalf("open error = %v, want auth-failed", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("typed error carried payload: %v", err)
	}
	err = provider.Validate(context.Background(), nil)
	if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorProtocol {
		t.Fatalf("validate error = %v, want protocol-error", err)
	}
	err = provider.Probe(context.Background(), nil)
	if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorUnreachable {
		t.Fatalf("probe error = %v, want unreachable", err)
	}
}

func TestProviderRestoreDescriptorBounds(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	guest := &fakeProviderGuest{supports: true}
	var restoreResponse string
	guest.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		if request.Method == domainpluginabi.ProviderMethodOpen && restoreResponse != "" {
			return []byte(`{"status":"ok","cols":80,"rows":24,"restore":"` +
				restoreResponse + `"}`), nil
		}
		return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)

	open := func(restore []byte) (*appplugin.ProviderStream, error) {
		return provider.Open(context.Background(), appplugin.ProviderOpenRequest{
			HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
			Restore: restore,
		})
	}
	_, err := open(make([]byte, 65))
	var typed *domainplugin.ProviderError
	if err == nil || !errors.As(err, &typed) ||
		typed.Kind != domainplugin.ProviderErrorProtocol {
		t.Fatalf("oversized restore error = %v, want protocol-error", err)
	}
	stream, err := open(make([]byte, 64))
	if err != nil {
		t.Fatalf("64-byte restore: %v", err)
	}
	if len(stream.RestoreDescriptor()) != 0 {
		t.Fatalf("empty restore response = %q", stream.RestoreDescriptor())
	}
	_ = stream.Close()

	// A guest-supplied descriptor is bounded by the same declaration.
	restoreResponse = base64.StdEncoding.EncodeToString(make([]byte, 65))
	if _, err := open(nil); err == nil || !errors.As(err, &typed) ||
		typed.Kind != domainplugin.ProviderErrorProtocol {
		t.Fatalf("oversized guest restore error = %v, want protocol-error", err)
	}
	restoreResponse = base64.StdEncoding.EncodeToString([]byte("bounded"))
	stream, err = open(nil)
	if err != nil {
		t.Fatalf("bounded guest restore: %v", err)
	}
	if got := string(stream.RestoreDescriptor()); got != "bounded" {
		t.Fatalf("guest restore = %q", got)
	}
	_ = stream.Close()
}

func TestProviderFailedStreamReleasesItsSlot(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	guest := &fakeProviderGuest{supports: true}
	guest.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		switch request.Method {
		case domainpluginabi.ProviderMethodPump:
			return []byte(`{"status":"unreachable"}`), nil
		default:
			return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
		}
	}
	connection := sampleConnection("x-acme-echo")
	connection.Capabilities.MaxSessions = 1
	provider, err := host.Register("installed/acme/1", appplugin.PluginRef{ID: "acme.echo"},
		connection, fiberProviderGuest{guest: guest, host: host, fiber: "installed/acme/1"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	failed := openEchoStream(t, provider)
	if _, err := failed.Write([]byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for failed.Failure() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the pump failure did not reach the stream")
		}
		time.Sleep(time.Millisecond)
	}
	var typed *domainplugin.ProviderError
	if !errors.As(failed.Failure(), &typed) ||
		typed.Kind != domainplugin.ProviderErrorUnreachable {
		t.Fatalf("stream failure = %v, want unreachable", failed.Failure())
	}

	// The failed stream released its only session slot instead of leaking it.
	replacement, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-2", Hostname: "example.test", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open after a stream failure: %v", err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatalf("close replacement: %v", err)
	}
	if err := failed.Close(); err != nil {
		t.Fatalf("close failed stream: %v", err)
	}
}

func TestProviderStreamCreditAndChunkBounds(t *testing.T) {
	guest := &fakeProviderGuest{supports: true}
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{
		QueueBytes: 1024, ChunkBytes: 256,
	})
	var streamID uint32
	guest.handler = func(_ context.Context, host *appplugin.ProviderHost,
		fiber domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		if request.Method == domainpluginabi.ProviderMethodOpen {
			streamID, _, _ = openPayload(t, request)
			callbacks := host.Callbacks(fiber)
			if status := callbacks.OutputWrite(streamID, make([]byte, 257)); status != -1 {
				t.Errorf("oversized output chunk status = %d, want -1", status)
			}
			for i := 0; i < 4; i++ {
				if status := callbacks.OutputWrite(streamID,
					bytesOf('a'+byte(i), 256)); status != 0 {
					t.Errorf("chunk %d status = %d, want 0", i, status)
				}
			}
			if status := callbacks.OutputWrite(streamID, make([]byte, 1)); status != -2 {
				t.Errorf("full queue status = %d, want -2", status)
			}
			return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
		}
		return []byte(`{"status":"ok"}`), nil
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)
	stream, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	buffer := make([]byte, 256)
	if n, err := stream.Read(buffer); err != nil || n != 256 {
		t.Fatalf("Read = %d, %v; want 256 bytes", n, err)
	}
	callbacks := host.Callbacks("installed/acme/1")
	if credit := callbacks.OutputCredit(streamID); credit != 256 {
		t.Fatalf("output credit after read = %d, want 256", credit)
	}

	if n, err := stream.Write(bytesOf('z', 700)); err != nil || n != 700 {
		t.Fatalf("Write = %d, %v", n, err)
	}
	input := callbacks.InputRead(streamID, 1024)
	if len(input) != 256 {
		t.Fatalf("input read returned %d bytes, want one 256 byte chunk", len(input))
	}
	if credit := callbacks.InputCredit(streamID); credit != 1024-700+256 {
		t.Fatalf("input credit = %d, want %d", credit, 1024-700+256)
	}
}

// TestProviderDefaultStreamBounds locks the contract defaults rather than the
// tiny test overrides: one Session's queues hold 256 KiB per direction and no
// guest call moves more than 64 KiB.
func TestProviderDefaultStreamBounds(t *testing.T) {
	const defaultQueue = 256 << 10
	const defaultChunk = 64 << 10
	if appplugin.DefaultProviderQueueBytes != defaultQueue {
		t.Fatalf("DefaultProviderQueueBytes = %d, want %d",
			appplugin.DefaultProviderQueueBytes, defaultQueue)
	}
	if appplugin.ProviderChunkBytes != defaultChunk {
		t.Fatalf("ProviderChunkBytes = %d, want %d",
			appplugin.ProviderChunkBytes, defaultChunk)
	}

	guest := &fakeProviderGuest{supports: true}
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	var streamID uint32
	guest.handler = func(_ context.Context, host *appplugin.ProviderHost,
		fiber domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		if request.Method != domainpluginabi.ProviderMethodOpen {
			return []byte(`{"status":"ok"}`), nil
		}
		streamID, _, _ = openPayload(t, request)
		callbacks := host.Callbacks(fiber)
		if status := callbacks.OutputWrite(streamID, make([]byte, defaultChunk+1)); status != -1 {
			t.Errorf("chunk over the default bound status = %d, want -1", status)
		}
		for i := 0; i < defaultQueue/defaultChunk; i++ {
			if status := callbacks.OutputWrite(streamID, bytesOf('a'+byte(i), defaultChunk)); status != 0 {
				t.Errorf("default-bounded chunk %d status = %d, want 0", i, status)
			}
		}
		if status := callbacks.OutputWrite(streamID, []byte("x")); status != -2 {
			t.Errorf("full default queue status = %d, want -2", status)
		}
		return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)
	stream, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	callbacks := host.Callbacks("installed/acme/1")
	if credit := callbacks.OutputCredit(streamID); credit != 0 {
		t.Fatalf("full output credit = %d, want 0", credit)
	}
	if n, err := stream.Write(bytesOf('z', 2*defaultChunk)); err != nil || n != 2*defaultChunk {
		t.Fatalf("Write = %d, %v; want %d bytes", n, err, 2*defaultChunk)
	}
	// A read request larger than one chunk still moves exactly one chunk.
	input := callbacks.InputRead(streamID, defaultQueue)
	if len(input) != defaultChunk {
		t.Fatalf("input read returned %d bytes, want the %d byte default chunk",
			len(input), defaultChunk)
	}
	wantCredit := defaultQueue - 2*defaultChunk + defaultChunk
	if credit := callbacks.InputCredit(streamID); credit != wantCredit {
		t.Fatalf("input credit = %d, want %d", credit, wantCredit)
	}
	if _, err := stream.Read(make([]byte, defaultChunk)); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

func TestProviderConfigMustBeAJSONObject(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	guest := &fakeProviderGuest{supports: true}
	var validates, opens atomic.Int32
	guest.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		switch request.Method {
		case domainpluginabi.ProviderMethodValidate:
			validates.Add(1)
		case domainpluginabi.ProviderMethodOpen:
			opens.Add(1)
		}
		return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)

	for _, config := range [][]byte{
		[]byte(`[]`), []byte(`["a"]`), []byte(`"text"`), []byte(`42`),
		[]byte(`true`), []byte(`null`), []byte(`{`), []byte("  \n\t"),
	} {
		err := provider.Validate(context.Background(), config)
		var typed *domainplugin.ProviderError
		if !errors.As(err, &typed) ||
			typed.Kind != domainplugin.ProviderErrorProtocol ||
			!strings.Contains(err.Error(), "not a JSON object") {
			t.Fatalf("Validate(%q) error = %v, want a protocol-error for a non-object", config, err)
		}
	}
	for _, config := range [][]byte{
		{}, []byte(`{}`), []byte(`{"target":"x"}`),
		[]byte(" \n {\"target\":\"x\"} \t"),
	} {
		if err := provider.Validate(context.Background(), config); err != nil {
			t.Fatalf("Validate(%q) error = %v", config, err)
		}
	}
	if validates.Load() != 4 {
		t.Fatalf("guest validate calls = %d, want 4", validates.Load())
	}

	// Open validates the same way before reserving a stream or reaching the
	// guest, so a non-object never becomes an open request.
	_, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
		Config: []byte(`[]`),
	})
	var typed *domainplugin.ProviderError
	if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorProtocol {
		t.Fatalf("Open with an array config = %v, want protocol-error", err)
	}
	if opens.Load() != 0 {
		t.Fatalf("guest open calls = %d, want 0", opens.Load())
	}
}

func TestProviderUnregisterFailsOnlyItsProtocolsStreams(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	guest := newEchoGuest()
	fiber := domainplugin.FiberID("installed/acme/1")
	invoker := fiberProviderGuest{guest: guest, host: host, fiber: fiber}
	first, err := host.Register(fiber, appplugin.PluginRef{ID: "acme.multi"},
		sampleConnection("x-acme-one"), invoker)
	if err != nil {
		t.Fatalf("Register first: %v", err)
	}
	second, err := host.Register(fiber, appplugin.PluginRef{ID: "acme.multi"},
		sampleConnection("x-acme-two"), invoker)
	if err != nil {
		t.Fatalf("Register second: %v", err)
	}
	firstStream := openEchoStream(t, first)
	secondStream := openEchoStream(t, second)
	assertEcho(t, secondStream, "before-unregister")

	if err := host.Unregister(first); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if host.Claimed("x-acme-one") || !host.Claimed("x-acme-two") {
		t.Fatalf("claims after Unregister: one=%t two=%t",
			host.Claimed("x-acme-one"), host.Claimed("x-acme-two"))
	}
	_, err = firstStream.Read(make([]byte, 8))
	var typed *domainplugin.ProviderError
	if !errors.As(err, &typed) ||
		typed.Kind != domainplugin.ProviderErrorFaulted ||
		typed.Protocol != "x-acme-one" {
		t.Fatalf("removed protocol stream read = %v, want provider-faulted for x-acme-one", err)
	}

	// The sibling protocol keeps its instance, its stream, and its pump loop.
	assertEcho(t, secondStream, "still-alive")
	third, err := second.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-3", Hostname: "example.test", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open after sibling unregister: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close sibling stream: %v", err)
	}
}

func TestProviderSchedulerIsFairAcrossStreams(t *testing.T) {
	guest := &fakeProviderGuest{supports: true}
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{
		CallTimeout: time.Second,
	})
	var mu sync.Mutex
	pumps := make(map[uint32]int)
	order := make([]uint32, 0, 16)
	guest.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		switch request.Method {
		case domainpluginabi.ProviderMethodOpen:
			return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
		case domainpluginabi.ProviderMethodPump:
			var payload struct {
				Stream uint32 `json:"stream"`
			}
			_ = json.Unmarshal(request.Payload, &payload)
			mu.Lock()
			pumps[payload.Stream]++
			order = append(order, payload.Stream)
			mu.Unlock()
			// Every stream stays ready, so the scheduler must rotate through
			// all three instead of letting one stream run ahead.
			return []byte(`{"status":"ok","more":true}`), nil
		default:
			return []byte(`{"status":"ok"}`), nil
		}
	}
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)
	streams := make([]*appplugin.ProviderStream, 0, 3)
	for index, hostID := range []string{"host-1", "host-2", "host-3"} {
		stream, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
			HostID: hostID, Hostname: "example.test", Cols: 80, Rows: 24,
		})
		if err != nil {
			t.Fatalf("Open stream %d: %v", index, err)
		}
		t.Cleanup(func() { _ = stream.Close() })
		streams = append(streams, stream)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := true
		for _, stream := range streams {
			if pumps[stream.ID()] < 4 {
				done = false
			}
		}
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("pump counts = %v, want all three streams served repeatedly", pumps)
			mu.Unlock()
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	sequence := append([]uint32(nil), order...)
	pumpCounts := make(map[uint32]int, len(pumps))
	for id, count := range pumps {
		pumpCounts[id] = count
	}
	mu.Unlock()

	// Before all three streams exist, one can legitimately run twice while the
	// test is still opening the others. From the moment every stream has been
	// served once and every stream stays ready, the ready set must rotate with
	// no stream getting a second consecutive turn.
	seen := make(map[uint32]bool, 3)
	start := len(sequence)
	for index, id := range sequence {
		seen[id] = true
		if len(seen) == 3 {
			start = index + 1
			break
		}
	}
	if start >= len(sequence) {
		t.Fatalf("pump order %v does not serve all three streams", sequence)
	}
	for index := start; index < len(sequence); index++ {
		if sequence[index] == sequence[index-1] {
			t.Fatalf("pump order %v starves a stream after all were ready", sequence)
		}
	}
	for _, stream := range streams {
		if pumpCounts[stream.ID()] < 4 {
			t.Fatalf("pump counts = %v, want all three streams served repeatedly", pumpCounts)
		}
	}
}

func TestProviderUnloadAndTrapFailOnlyTheirStreams(t *testing.T) {
	host := newProviderHost(t, &auditRecorder{}, appplugin.ProviderLimits{})
	echo := newEchoGuest()
	providerA := registerProvider(t, host, "installed/acme/1", "acme.one",
		"x-acme-one", echo)
	guestB := newEchoGuest()
	providerB := registerProvider(t, host, "installed/acme/2", "acme.two",
		"x-acme-two", guestB)

	streamA := openEchoStream(t, providerA)
	streamB := openEchoStream(t, providerB)

	if err := host.Unregister(providerA); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	_, err := streamA.Read(make([]byte, 8))
	var typed *domainplugin.ProviderError
	if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorFaulted {
		t.Fatalf("unloaded stream read = %v, want provider-faulted", err)
	}
	assertEcho(t, streamB, "unrelated")

	// A trap on the second provider's stream fails that stream and leaves a
	// third provider untouched.
	trapping := &fakeProviderGuest{supports: true}
	trapping.handler = func(_ context.Context, _ *appplugin.ProviderHost,
		_ domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		if request.Method == domainpluginabi.ProviderMethodOpen {
			return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
		}
		return nil, &appplugin.TrapError{Operation: "provider_request",
			Err: errors.New("boom")}
	}
	providerC := registerProvider(t, host, "installed/acme/3", "acme.three",
		"x-acme-three", trapping)
	guestD := newEchoGuest()
	providerD := registerProvider(t, host, "installed/acme/4", "acme.four",
		"x-acme-four", guestD)
	streamC := openEchoStream(t, providerC)
	streamD := openEchoStream(t, providerD)
	if _, err := streamC.Write([]byte("trap")); err != nil {
		// The first scheduled pump may already have trapped; that is the same
		// typed failure the test asserts below.
		if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorFaulted {
			t.Fatalf("trapping stream write: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := streamC.Failure()
		if err != nil {
			if !errors.As(err, &typed) || typed.Kind != domainplugin.ProviderErrorFaulted {
				t.Fatalf("trapped stream failure = %v", err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("trapped stream was not failed")
		}
		time.Sleep(time.Millisecond)
	}
	assertEcho(t, streamD, "still-alive")
}

func TestProviderAuditIsPayloadFree(t *testing.T) {
	audit := &auditRecorder{}
	host := newProviderHost(t, audit, appplugin.ProviderLimits{})
	guest := newEchoGuest()
	provider := registerProvider(t, host, "installed/acme/1", "acme.echo",
		"x-acme-echo", guest)
	stream := openEchoStream(t, provider)
	if err := stream.Signal("not-declared"); !errors.Is(err, domainplugin.ErrProviderUnsupported) {
		t.Fatalf("undeclared signal error = %v", err)
	}
	assertEcho(t, stream, "payload-should-not-appear")

	events := audit.snapshot()
	var claim, open bool
	for _, event := range events {
		if event.Event != domainplugin.AuditProvider {
			t.Fatalf("event type = %q", event.Event)
		}
		if strings.Contains(event.Reason, "payload-should-not-appear") ||
			strings.Contains(event.Protocol, "payload-should-not-appear") ||
			strings.Contains(event.Status, "payload-should-not-appear") {
			t.Fatalf("audit event carried stream content: %+v", event)
		}
		switch event.Status {
		case "ok":
			switch {
			case strings.HasPrefix(event.Reason, "provider.claim"):
				claim = true
			case strings.HasPrefix(event.Reason, "provider.open"):
				open = true
			}
		case "probe", "signal":
			// Capability denials are recorded by action in the reason.
		}
		if strings.Contains(event.Reason, "signal") && event.Status != "signal" {
			t.Fatalf("denial reason = %q", event.Reason)
		}
	}
	if !claim || !open {
		t.Fatalf("audit events = %+v, want claim and open", events)
	}
}

// echoGuest answers open with a fixed size and echoes each pump's input bytes.
func newEchoGuest() *fakeProviderGuest {
	guest := &fakeProviderGuest{supports: true}
	guest.handler = func(_ context.Context, host *appplugin.ProviderHost,
		fiber domainplugin.FiberID, request appplugin.ProviderRequest) ([]byte, error) {
		callbacks := host.Callbacks(fiber)
		switch request.Method {
		case domainpluginabi.ProviderMethodOpen:
			return []byte(`{"status":"ok","cols":80,"rows":24}`), nil
		case domainpluginabi.ProviderMethodPump:
			var payload struct {
				Stream uint32 `json:"stream"`
			}
			_ = json.Unmarshal(request.Payload, &payload)
			for {
				data := callbacks.InputRead(payload.Stream, 4096)
				if len(data) == 0 {
					break
				}
				if status := callbacks.OutputWrite(payload.Stream, data); status != 0 {
					break
				}
			}
			return []byte(`{"status":"ok"}`), nil
		default:
			return []byte(`{"status":"ok"}`), nil
		}
	}
	return guest
}

func openEchoStream(t *testing.T, provider *appplugin.Provider) *appplugin.ProviderStream {
	t.Helper()
	stream, err := provider.Open(context.Background(), appplugin.ProviderOpenRequest{
		HostID: "host-1", Hostname: "example.test", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	return stream
}

func assertEcho(t *testing.T, stream *appplugin.ProviderStream, text string) {
	t.Helper()
	if _, err := stream.Write([]byte(text)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buffer := make([]byte, len(text))
	done := make(chan error, 1)
	go func() {
		_, err := stream.Read(buffer)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("echo stream did not respond")
	}
	if string(buffer) != text {
		t.Fatalf("echo = %q, want %q", buffer, text)
	}
}

func bytesOf(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}
