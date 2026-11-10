package plugin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// t23ServiceFunc answers one host-to-guest service request. Tests return a
// provider envelope; returning a TrapError exercises the recycle path.
type t23ServiceFunc func(ctx context.Context, sink appplugin.HostSink,
	request appplugin.ServiceRequest) ([]byte, error)

// t23DispatchFunc models a guest command that drives the services host
// module, for example a dynamic registration or a nested service call.
type t23DispatchFunc func(ctx context.Context, sink appplugin.HostSink, command string) error

type t23Runtime struct {
	mu         sync.Mutex
	services   map[string]t23ServiceFunc
	dispatches map[string]t23DispatchFunc
	instances  map[string][]*t23Instance
}

func newT23Runtime() *t23Runtime {
	return &t23Runtime{
		services:   make(map[string]t23ServiceFunc),
		dispatches: make(map[string]t23DispatchFunc),
		instances:  make(map[string][]*t23Instance),
	}
}

func (r *t23Runtime) provide(id string, fn t23ServiceFunc) {
	r.mu.Lock()
	r.services[id] = fn
	r.mu.Unlock()
}

func (r *t23Runtime) dispatch(id string, fn t23DispatchFunc) {
	r.mu.Lock()
	r.dispatches[id] = fn
	r.mu.Unlock()
}

func (r *t23Runtime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	r.mu.Lock()
	instance := &t23Instance{
		name:         spec.Name,
		sink:         sink,
		service:      r.services[spec.Name],
		dispatchFunc: r.dispatches[spec.Name],
		handles:      appplugin.NewHandleTable(appplugin.MaxHandles),
	}
	r.instances[spec.Name] = append(r.instances[spec.Name], instance)
	r.mu.Unlock()
	return instance, nil
}

func (r *t23Runtime) Close(context.Context) error { return nil }

func (r *t23Runtime) dispatcher(t *testing.T, id string, index int) appplugin.ServiceDispatcher {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	instances := r.instances[id]
	if index >= len(instances) {
		t.Fatalf("no instance %d of %q", index, id)
	}
	if instances[index].sink.Services == nil {
		t.Fatalf("instance %d of %q has no service dispatcher", index, id)
	}
	return instances[index].sink.Services
}

type t23Instance struct {
	name         string
	sink         appplugin.HostSink
	service      t23ServiceFunc
	dispatchFunc t23DispatchFunc

	handles *appplugin.HandleTable
	slot    sync.Mutex
	closed  bool
}

func (i *t23Instance) Init(context.Context) error     { return nil }
func (i *t23Instance) Poll(context.Context) error     { return nil }
func (i *t23Instance) Shutdown(context.Context) error { return nil }
func (i *t23Instance) Close(context.Context) error {
	i.handles.RevokeAll()
	return nil
}
func (i *t23Instance) Handles() *appplugin.HandleTable { return i.handles }

func (i *t23Instance) Dispatch(ctx context.Context, command string) error {
	if i.dispatchFunc == nil {
		return nil
	}
	return i.dispatchFunc(ctx, i.sink, command)
}

func (i *t23Instance) DispatchTimed(ctx context.Context, command string,
	_ time.Duration) error {
	return i.Dispatch(ctx, command)
}

func (i *t23Instance) SupportsService() bool { return i.service != nil }

func (i *t23Instance) ServiceRequest(ctx context.Context,
	request appplugin.ServiceRequest) ([]byte, error) {
	if !i.slot.TryLock() {
		return nil, appplugin.ErrCallInFlight
	}
	defer i.slot.Unlock()
	if i.service == nil {
		return nil, &appplugin.MissingExportError{Name: domainpluginabi.ExportServiceRequest}
	}
	return i.service(ctx, i.sink, request)
}

var _ appplugin.ServiceCapableInstance = (*t23Instance)(nil)

// t23ProviderManifest builds a provider declaration.
func t23ProviderManifest(id, service string, methods ...domainplugin.ServiceMethodDecl) domainplugin.Manifest {
	manifest := t10BareManifest(id)
	manifest.Provides = []domainplugin.Provision{{
		Service: service,
		Version: "1.2.0",
		Access:  domainplugin.ServiceAccessRead,
		Methods: methods,
	}}
	return manifest
}

// t23ConsumerManifest builds an inject declaration.
func t23ConsumerManifest(id, service string, optional bool) domainplugin.Manifest {
	manifest := t10BareManifest(id)
	manifest.Inject = []domainplugin.Injection{{Service: service, Optional: optional}}
	return manifest
}

func t23Load(t *testing.T, container *appplugin.Container, manifest domainplugin.Manifest,
	name string) (*appplugin.Fiber, error) {
	t.Helper()
	dir := pluginDir(t, name, manifest)
	return container.LoadInstance(context.Background(), appplugin.LoadSpec{
		Group: "app", Directory: dir, Instance: 1,
		Development: true, Source: domainplugin.SourceDevelopment,
	})
}

func t23LoadOK(t *testing.T, container *appplugin.Container, manifest domainplugin.Manifest,
	name string) *appplugin.Fiber {
	t.Helper()
	fiber, err := t23Load(t, container, manifest, name)
	if fiber == nil {
		t.Fatalf("load %s: %v", manifest.ID, err)
	}
	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("%s state = %s (%s)", fiber.ID(), fiber.State(), fiber.Reason())
	}
	return fiber
}

// t23Call invokes one services host method on a dispatcher.
func t23Call(t *testing.T, dispatcher appplugin.ServiceDispatcher, method uint32,
	body any) map[string]any {
	t.Helper()
	var payload []byte
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		payload = raw
	}
	response := dispatcher.Call(context.Background(), appplugin.AbiServiceCall(
		appplugin.ServiceServices, method, payload))
	var envelope map[string]any
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatalf("decode services envelope: %v (%s)", err, response)
	}
	return envelope
}

func t23Invoke(t *testing.T, dispatcher appplugin.ServiceDispatcher, service, method string,
	payload any) map[string]any {
	t.Helper()
	body := map[string]any{"service": service, "method": method}
	if payload != nil {
		body["payload"] = payload
	}
	return t23Call(t, dispatcher, domainpluginabi.ServicesMethodInvoke, body)
}

func t23OK(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	if code, _ := envelope["code"].(string); code != appplugin.ServiceCallOK {
		t.Fatalf("services response = %v", envelope)
	}
	data, _ := envelope["data"].(map[string]any)
	return data
}

func t23Code(envelope map[string]any) string {
	code, _ := envelope["code"].(string)
	return code
}

func t23JSON(v any) []byte {
	payload, _ := json.Marshal(v)
	return payload
}

func t23Envelope(data any) []byte { return t23JSON(map[string]any{"code": "ok", "data": data}) }

func t23ErrorEnvelope(code, message string) []byte {
	return t23JSON(map[string]any{"code": code, "message": message})
}

// t23ProviderRequest decodes the broker request envelope.
func t23ProviderRequest(t *testing.T, request appplugin.ServiceRequest) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(request.Payload, &decoded); err != nil {
		t.Fatalf("provider request is not JSON: %v (%s)", err, request.Payload)
	}
	return decoded
}

// t23ServicesSink wraps a dispatcher for a provider handler.
func t23Services(t *testing.T, sink appplugin.HostSink, method uint32, body any) map[string]any {
	t.Helper()
	if sink.Services == nil {
		t.Fatal("provider has no services dispatcher")
	}
	return t23Call(t, sink.Services, method, body)
}

func t23NewHost(t *testing.T, authority *appplugin.Authority, audit appplugin.AuditSink,
	now func() time.Time) *appplugin.HostServices {
	t.Helper()
	return appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority: authority,
		Audit:     audit,
		Now:       now,
	})
}

func TestCrossPluginInvokeValidatesArgumentsResultsAndBinary(t *testing.T) {
	method := domainplugin.ServiceMethodDecl{
		Name: "echo",
		ArgsSchema: map[string]any{
			"type": "object", "required": []any{"text"},
			"properties":           map[string]any{"text": map[string]any{"type": "string"}},
			"additionalProperties": false,
		},
		ResultSchema: map[string]any{
			"type": "object", "required": []any{"text"},
			"properties":           map[string]any{"text": map[string]any{"type": "string"}},
			"additionalProperties": false,
		},
	}
	binaryMethod := domainplugin.ServiceMethodDecl{
		Name:        "blob",
		Encoding:    domainplugin.ServiceEncodingBinary,
		ContentType: "application/octet-stream",
	}
	service := "plugin.acme.echo.echo"
	provider := t23ProviderManifest("acme.echo", service, method, binaryMethod)
	consumer := t23ConsumerManifest("acme.echo-client", service, false)

	runtime := newT23Runtime()
	runtime.provide("acme.echo", func(_ context.Context, _ appplugin.HostSink,
		request appplugin.ServiceRequest) ([]byte, error) {
		var decoded struct {
			Method  string          `json:"method"`
			Payload json.RawMessage `json:"payload"`
			DataB64 string          `json:"payload_b64"`
		}
		_ = json.Unmarshal(request.Payload, &decoded)
		switch decoded.Method {
		case "echo":
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(decoded.Payload, &args); err != nil {
				return t23ErrorEnvelope("invalid-argument", "bad args"), nil
			}
			if args.Text == "bad-result" {
				return t23Envelope(map[string]any{"unexpected": true}), nil
			}
			return t23Envelope(map[string]any{"text": "pong:" + args.Text}), nil
		case "blob":
			data, err := base64.StdEncoding.DecodeString(decoded.DataB64)
			if err != nil {
				return t23ErrorEnvelope("invalid-argument", "bad base64"), nil
			}
			return t23JSON(map[string]any{
				"code":     "ok",
				"data_b64": base64.StdEncoding.EncodeToString(append([]byte("echo:"), data...)),
			}), nil
		default:
			return t23ErrorEnvelope("not-found", "unknown method"), nil
		}
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, provider, "echo")
	consumerFiber := t23LoadOK(t, container, consumer, "echo-client")

	dispatcher := runtime.dispatcher(t, "acme.echo-client", 0)
	data := t23OK(t, t23Invoke(t, dispatcher, service, "echo", map[string]any{"text": "hi"}))
	if data["payload"].(map[string]any)["text"] != "pong:hi" {
		t.Fatalf("echo result = %v", data)
	}
	if data["provider"] != "acme.echo" || data["version"] != "1.2.0" {
		t.Fatalf("provider metadata = %v", data)
	}

	if code := t23Code(t23Invoke(t, dispatcher, service, "echo",
		map[string]any{"text": 7})); code != appplugin.ServiceCallInvalidArgument {
		t.Fatalf("invalid args code = %q", code)
	}
	if code := t23Code(t23Invoke(t, dispatcher, service, "echo",
		map[string]any{"text": "bad-result"})); code != appplugin.ServiceCallInternal {
		t.Fatalf("invalid provider result code = %q", code)
	}
	if code := t23Code(t23Invoke(t, dispatcher, service, "missing", nil)); code != appplugin.ServiceCallNotFound {
		t.Fatalf("unknown method code = %q", code)
	}

	binaryData := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodInvoke,
		map[string]any{
			"service": service, "method": "blob",
			"payload_b64": base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 0xff}),
		}))
	decoded, err := base64.StdEncoding.DecodeString(binaryData["payload_b64"].(string))
	if err != nil || string(decoded) != "echo:\x00\x01\x02\xff" {
		t.Fatalf("binary result = %v (%v)", binaryData, err)
	}
	if binaryData["content_type"] != "application/octet-stream" {
		t.Fatalf("binary content type = %v", binaryData)
	}

	binding, ok := container.Broker().Binding(consumerFiber.ID(), service)
	if !ok || !binding.Bound || binding.Provider != "acme.echo" {
		t.Fatalf("binding = %+v (%v)", binding, ok)
	}
	stats := container.Broker().Stats()
	if stats.Calls < 5 || stats.Errors < 3 {
		t.Fatalf("call stats = %+v", stats)
	}
}

func TestCrossPluginPermissionScopeDeniesWriteMethod(t *testing.T) {
	service := "plugin.acme.priv.data"
	provider := t23ProviderManifest("acme.priv", service,
		domainplugin.ServiceMethodDecl{Name: "read"},
		domainplugin.ServiceMethodDecl{Name: "write", Access: domainplugin.ServiceAccessWrite})
	consumer := t23ConsumerManifest("acme.priv-client", service, false)
	runtime := newT23Runtime()
	runtime.provide("acme.priv", func(_ context.Context, _ appplugin.HostSink,
		request appplugin.ServiceRequest) ([]byte, error) {
		return t23Envelope(map[string]any{"ok": true}), nil
	})
	authority := appplugin.NewAuthority()
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, authority, nil, nil)),
		appplugin.WithServiceAuthority(authority))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	providerGrant := domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionServicesProvide,
		Scope: domainplugin.Scope{
			Kind: domainplugin.ScopeServices, Service: "plugin.acme.priv.", Access: domainplugin.ServiceAccessRead,
		},
	}
	authority.Grant(appplugin.SubjectForDevelopment("acme.priv"), providerGrant)
	consumerGrant := domainplugin.PermissionGrant{
		Permission: domainplugin.PermissionServicesInvoke,
		Scope: domainplugin.Scope{
			Kind: domainplugin.ScopeServices, Service: service, Provider: "acme.priv",
			Version: "^1.0", Access: domainplugin.ServiceAccessRead,
		},
	}
	authority.Grant(appplugin.SubjectForDevelopment("acme.priv-client"), consumerGrant)
	t23LoadOK(t, container, provider, "priv")
	t23LoadOK(t, container, consumer, "priv-client")

	dispatcher := runtime.dispatcher(t, "acme.priv-client", 0)
	if data := t23OK(t, t23Invoke(t, dispatcher, service, "read", nil)); data["payload"] == nil {
		t.Fatal("read method returned no payload")
	}
	if code := t23Code(t23Invoke(t, dispatcher, service, "write", nil)); code != appplugin.ServiceCallPermissionDenied {
		t.Fatalf("write method code = %q", code)
	}
}

func TestCrossPluginMissingProviderGrantLeavesConsumerPending(t *testing.T) {
	service := "plugin.acme.nogrant.data"
	provider := t23ProviderManifest("acme.nogrant", service,
		domainplugin.ServiceMethodDecl{Name: "read"})
	consumer := t23ConsumerManifest("acme.nogrant-client", service, false)
	runtime := newT23Runtime()
	runtime.provide("acme.nogrant", func(context.Context, appplugin.HostSink,
		appplugin.ServiceRequest) ([]byte, error) {
		return t23Envelope(map[string]any{}), nil
	})
	authority := appplugin.NewAuthority()
	authority.Grant(appplugin.SubjectForDevelopment("acme.nogrant-client"),
		domainplugin.PermissionGrant{
			Permission: domainplugin.PermissionServicesInvoke,
			Scope: domainplugin.Scope{
				Kind: domainplugin.ScopeServices, Service: service, Provider: "acme.nogrant",
				Access: domainplugin.ServiceAccessRead,
			},
		})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, authority, nil, nil)),
		appplugin.WithServiceAuthority(authority))
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	// Without services.provide the provider publishes nothing.
	t23LoadOK(t, container, provider, "nogrant")
	if len(container.Services()) != 0 {
		t.Fatalf("ungranted provider published %+v", container.Services())
	}
	consumerFiber, err := t23Load(t, container, consumer, "nogrant-client")
	if consumerFiber == nil || consumerFiber.State() != domainplugin.StatePending {
		t.Fatalf("consumer = %v, %v", consumerFiber, err)
	}
	if !strings.Contains(consumerFiber.Reason(), service) {
		t.Fatalf("consumer reason = %q", consumerFiber.Reason())
	}
}

func TestCrossPluginProviderTrapIsContainedAndRecycles(t *testing.T) {
	service := "plugin.acme.trap.data"
	provider := t23ProviderManifest("acme.trap", service,
		domainplugin.ServiceMethodDecl{Name: "read"})
	consumer := t23ConsumerManifest("acme.trap-client", service, false)
	runtime := newT23Runtime()
	runtime.provide("acme.trap", func(context.Context, appplugin.HostSink,
		appplugin.ServiceRequest) ([]byte, error) {
		return nil, &appplugin.TrapError{Operation: "service_request", Err: errors.New("boom")}
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, provider, "trap")
	t23LoadOK(t, container, consumer, "trap-client")

	dispatcher := runtime.dispatcher(t, "acme.trap-client", 0)
	if code := t23Code(t23Invoke(t, dispatcher, service, "read", nil)); code != appplugin.ServiceCallInternal {
		t.Fatalf("trap code = %q", code)
	}
	providerFiber, _ := container.FiberByRef(appplugin.PluginRef{
		ID: "acme.trap", Source: domainplugin.SourceDevelopment, Development: true,
	})
	if providerFiber == nil || providerFiber.State() != domainplugin.StateFailed {
		t.Fatalf("provider state after trap = %v", providerFiber)
	}
	if code := t23Code(t23Invoke(t, dispatcher, service, "read", nil)); code != appplugin.ServiceCallNotFound &&
		code != appplugin.ServiceCallUnavailable {
		t.Fatalf("call after trap code = %q", code)
	}
}

func TestCrossPluginDeadlockIsDetectedAcrossRewrite(t *testing.T) {
	serviceA := "plugin.acme.rewrite.a"
	serviceB := "plugin.acme.rewrite2.b"
	providerA := t23ProviderManifest("acme.rewrite", serviceA,
		domainplugin.ServiceMethodDecl{Name: "read"})
	providerA.Provides[0].Service = serviceB
	providerB := t23ProviderManifest("acme.rewrite2", serviceB,
		domainplugin.ServiceMethodDecl{Name: "read"})
	providerB.Provides[0].Service = serviceA
	// Two plugin ids cannot both own one prefix, so each provider owns its
	// own service and calls the other.
	providerA.Provides[0].Service = serviceA
	providerB.Provides[0].Service = serviceB
	consumer := t23ConsumerManifest("acme.rewrite-client", serviceA, false)

	runtime := newT23Runtime()
	runtime.provide("acme.rewrite", func(ctx context.Context, sink appplugin.HostSink,
		request appplugin.ServiceRequest) ([]byte, error) {
		envelope := t23Services(t, sink, domainpluginabi.ServicesMethodInvoke, map[string]any{
			"service": serviceB, "method": "read",
		})
		if code := t23Code(envelope); code != appplugin.ServiceCallOK {
			return t23ErrorEnvelope(code, "downstream"), nil
		}
		return t23Envelope(map[string]any{"a": true}), nil
	})
	runtime.provide("acme.rewrite2", func(ctx context.Context, sink appplugin.HostSink,
		request appplugin.ServiceRequest) ([]byte, error) {
		envelope := t23Services(t, sink, domainpluginabi.ServicesMethodInvoke, map[string]any{
			"service": serviceA, "method": "read",
		})
		if code := t23Code(envelope); code != appplugin.ServiceCallOK {
			return t23ErrorEnvelope(code, "cycle"), nil
		}
		return t23Envelope(map[string]any{"b": true}), nil
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(providerA, providerB, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, providerA, "rewrite")
	t23LoadOK(t, container, providerB, "rewrite2")
	t23LoadOK(t, container, consumer, "rewrite-client")

	dispatcher := runtime.dispatcher(t, "acme.rewrite-client", 0)
	if code := t23Code(t23Invoke(t, dispatcher, serviceA, "read", nil)); code != appplugin.ServiceCallDeadlock {
		t.Fatalf("cycle code = %q", code)
	}
	if stats := container.Broker().Stats(); stats.Deadlocks == 0 {
		t.Fatalf("deadlock was not counted: %+v", stats)
	}
}

func TestCrossPluginBusyProviderIsRetryable(t *testing.T) {
	service := "plugin.acme.busy.data"
	provider := t23ProviderManifest("acme.busy", service,
		domainplugin.ServiceMethodDecl{Name: "read"})
	consumer := t23ConsumerManifest("acme.busy-client", service, false)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	runtime := newT23Runtime()
	runtime.provide("acme.busy", func(context.Context, appplugin.HostSink,
		appplugin.ServiceRequest) ([]byte, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return t23Envelope(map[string]any{"ok": true}), nil
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, provider, "busy")
	t23LoadOK(t, container, consumer, "busy-client")
	dispatcher := runtime.dispatcher(t, "acme.busy-client", 0)

	const callers = 12
	codes := make(chan string, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			envelope := t23Invoke(t, dispatcher, service, "read", nil)
			codes <- t23Code(envelope)
		}()
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider never received the first call")
	}
	// Give the remaining callers time to queue behind the in-flight call.
	deadline := time.Now().Add(2 * time.Second)
	for container.Broker().Stats().Busy == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(codes)
	busy, ok := 0, 0
	for code := range codes {
		switch code {
		case appplugin.ServiceCallBusy:
			busy++
		case appplugin.ServiceCallOK:
			ok++
		}
	}
	if busy == 0 {
		t.Fatalf("no call was rejected as busy: ok=%d", ok)
	}
	if ok == 0 {
		t.Fatalf("every call was rejected: busy=%d", busy)
	}
}

// TestCrossPluginDeadlinePropagatesToProvider proves the caller's deadline is
// carried into the provider call, even for a long-timeout method, and that the
// expiry is a retryable typed timeout rather than a trap.
func TestCrossPluginDeadlinePropagatesToProvider(t *testing.T) {
	service := "plugin.acme.slow.data"
	provider := t23ProviderManifest("acme.slow", service,
		domainplugin.ServiceMethodDecl{Name: "wait", Timeout: domainplugin.ServiceTimeoutLong})
	consumer := t23ConsumerManifest("acme.slow-client", service, false)
	runtime := newT23Runtime()
	runtime.provide("acme.slow", func(ctx context.Context, _ appplugin.HostSink,
		_ appplugin.ServiceRequest) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, provider, "slow")
	t23LoadOK(t, container, consumer, "slow-client")
	dispatcher := runtime.dispatcher(t, "acme.slow-client", 0)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	response := dispatcher.Call(ctx, appplugin.AbiServiceCall(
		appplugin.ServiceServices, domainpluginabi.ServicesMethodInvoke,
		t23JSON(map[string]any{"service": service, "method": "wait"})))
	var envelope map[string]any
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, response)
	}
	if code := t23Code(envelope); code != appplugin.ServiceCallTimeout {
		t.Fatalf("deadline code = %q (%v)", code, envelope)
	}
	if stats := container.Broker().Stats(); stats.Timeouts == 0 {
		t.Fatalf("timeout counter = %+v", stats)
	}
}

// t23StreamProvider answers the shared streaming fixture methods.
func t23StreamProvider(t *testing.T, sink appplugin.HostSink,
	request appplugin.ServiceRequest) ([]byte, error) {
	var decoded struct {
		Method string `json:"method"`
		Stream *struct {
			Handle    string `json:"handle"`
			Direction string `json:"direction"`
			Open      bool   `json:"open"`
		} `json:"stream"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(request.Payload, &decoded); err != nil {
		return t23ErrorEnvelope("invalid-argument", "bad request"), nil
	}
	if decoded.Stream != nil {
		switch decoded.Method {
		case "download":
			if !decoded.Stream.Open {
				return t23Envelope(map[string]any{}), nil
			}
			for i := 0; i < 3; i++ {
				chunk := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("chunk-%d", i)))
				envelope := t23Services(t, sink, domainpluginabi.ServicesMethodStreamWrite,
					map[string]any{"handle": decoded.Stream.Handle, "chunk_b64": chunk})
				if code := t23Code(envelope); code != appplugin.ServiceCallOK {
					return t23ErrorEnvelope(code, "write failed"), nil
				}
			}
			t23Services(t, sink, domainpluginabi.ServicesMethodStreamClose,
				map[string]any{"handle": decoded.Stream.Handle})
			return t23Envelope(map[string]any{}), nil
		case "collect", "mirror":
			if decoded.Stream.Open {
				return t23Envelope(map[string]any{}), nil
			}
			total := 0
			mirrored := make([]byte, 0)
			for {
				envelope := t23Services(t, sink, domainpluginabi.ServicesMethodStreamRead,
					map[string]any{"handle": decoded.Stream.Handle})
				if code := t23Code(envelope); code != appplugin.ServiceCallOK {
					return t23ErrorEnvelope(code, "read failed"), nil
				}
				data := envelope["data"].(map[string]any)
				if again, _ := data["again"].(bool); again {
					break
				}
				if end, _ := data["end"].(bool); end {
					break
				}
				chunk, err := base64.StdEncoding.DecodeString(data["chunk_b64"].(string))
				if err != nil {
					return t23ErrorEnvelope("invalid-argument", "bad chunk"), nil
				}
				total += len(chunk)
				mirrored = append(mirrored, chunk...)
			}
			if decoded.Method == "mirror" {
				chunk := base64.StdEncoding.EncodeToString(mirrored)
				t23Services(t, sink, domainpluginabi.ServicesMethodStreamWrite,
					map[string]any{"handle": decoded.Stream.Handle, "chunk_b64": chunk})
				t23Services(t, sink, domainpluginabi.ServicesMethodStreamClose,
					map[string]any{"handle": decoded.Stream.Handle})
			}
			return t23Envelope(map[string]any{"bytes": total}), nil
		}
	}
	return t23Envelope(map[string]any{}), nil
}

func TestCrossPluginStreamsDownloadUploadDuplexAndReload(t *testing.T) {
	service := "plugin.acme.stream.feed"
	provider := t23ProviderManifest("acme.stream", service,
		domainplugin.ServiceMethodDecl{Name: "download"},
		domainplugin.ServiceMethodDecl{Name: "collect"},
		domainplugin.ServiceMethodDecl{Name: "mirror"},
		domainplugin.ServiceMethodDecl{Name: "hold"})
	consumer := t23ConsumerManifest("acme.stream-client", service, false)
	runtime := newT23Runtime()
	runtime.provide("acme.stream", func(_ context.Context, sink appplugin.HostSink,
		request appplugin.ServiceRequest) ([]byte, error) {
		return t23StreamProvider(t, sink, request)
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	providerFiber := t23LoadOK(t, container, provider, "stream")
	t23LoadOK(t, container, consumer, "stream-client")
	dispatcher := runtime.dispatcher(t, "acme.stream-client", 0)

	readChunks := func(handle string) []string {
		t.Helper()
		chunks := make([]string, 0)
		for {
			envelope := t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamRead,
				map[string]any{"handle": handle})
			if code := t23Code(envelope); code != appplugin.ServiceCallOK {
				t.Fatalf("stream_read code = %q (%v)", code, envelope)
			}
			data := envelope["data"].(map[string]any)
			if again, _ := data["again"].(bool); again {
				return chunks
			}
			if end, _ := data["end"].(bool); end {
				return chunks
			}
			chunk, err := base64.StdEncoding.DecodeString(data["chunk_b64"].(string))
			if err != nil {
				t.Fatalf("chunk: %v", err)
			}
			chunks = append(chunks, string(chunk))
		}
	}

	// Download: the provider writes three chunks and closes before replying.
	download := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamOpen,
		map[string]any{"service": service, "method": "download", "direction": "download"}))
	handle := download["handle"].(string)
	if got := strings.Join(readChunks(handle), ""); got != "chunk-0chunk-1chunk-2" {
		t.Fatalf("download bytes = %q", got)
	}

	// Upload: the consumer writes, then continues the stream in a call so the
	// provider can drain it without a reverse channel.
	upload := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamOpen,
		map[string]any{"service": service, "method": "collect", "direction": "upload"}))
	uploadHandle := upload["handle"].(string)
	if data := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamWrite,
		map[string]any{
			"handle":    uploadHandle,
			"chunk_b64": base64.StdEncoding.EncodeToString([]byte("hello upload")),
		})); data["bytes"] != float64(12) {
		t.Fatalf("upload write = %v", data)
	}
	collected := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodInvoke,
		map[string]any{
			"service": service, "method": "collect", "stream": uploadHandle,
			"payload": map[string]any{},
		}))
	if collected["payload"].(map[string]any)["bytes"] != float64(12) {
		t.Fatalf("upload collected = %v", collected)
	}
	t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamClose,
		map[string]any{"handle": uploadHandle})

	// Duplex: one round trip over the same stream.
	duplex := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamOpen,
		map[string]any{"service": service, "method": "mirror", "direction": "duplex"}))
	duplexHandle := duplex["handle"].(string)
	t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamWrite,
		map[string]any{
			"handle":    duplexHandle,
			"chunk_b64": base64.StdEncoding.EncodeToString([]byte("ping")),
		}))
	t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodInvoke,
		map[string]any{
			"service": service, "method": "mirror", "stream": duplexHandle,
			"payload": map[string]any{},
		}))
	if got := strings.Join(readChunks(duplexHandle), ""); got != "ping" {
		t.Fatalf("duplex bytes = %q", got)
	}

	// A provider reload closes both ends as unavailable.
	pending := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamOpen,
		map[string]any{"service": service, "method": "hold", "direction": "download"}))
	pendingHandle := pending["handle"].(string)
	if err := container.Unload(context.Background(), providerFiber.ID()); err != nil {
		t.Fatalf("unload provider: %v", err)
	}
	envelope := t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamRead,
		map[string]any{"handle": pendingHandle})
	if code := t23Code(envelope); code != appplugin.ServiceCallUnavailable {
		t.Fatalf("stream read after relaod = %q (%v)", code, envelope)
	}
}

func TestCrossPluginEventsOrderingFilterGapAndRateLimit(t *testing.T) {
	service := "plugin.acme.events.feed"
	provider := t23ProviderManifest("acme.events", service,
		domainplugin.ServiceMethodDecl{Name: "read"})
	provider.Provides[0].Events = []domainplugin.ServiceEventDecl{{
		Name:   "pulse",
		Schema: map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}}},
	}}
	consumer := t23ConsumerManifest("acme.events-client", service, false)
	clock := &t23Clock{at: time.Unix(1000, 0)}
	runtime := newT23Runtime()
	runtime.provide("acme.events", func(context.Context, appplugin.HostSink,
		appplugin.ServiceRequest) ([]byte, error) {
		return t23Envelope(map[string]any{}), nil
	})
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, clock.Now)),
		appplugin.WithClock(clock.Now))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, provider, "events")
	t23LoadOK(t, container, consumer, "events-client")
	consumerDispatcher := runtime.dispatcher(t, "acme.events-client", 0)
	providerDispatcher := runtime.dispatcher(t, "acme.events", 0)

	sub := t23OK(t, t23Call(t, consumerDispatcher, domainpluginabi.ServicesMethodSubscribe,
		map[string]any{"service": service, "event": "pulse", "filter": map[string]string{"kind": "tick"}}))
	handle := sub["handle"].(string)

	emit := func(kind string) string {
		return t23Code(t23Call(t, providerDispatcher, domainpluginabi.ServicesMethodEmit,
			map[string]any{"service": service, "event": "pulse", "payload": map[string]any{"kind": kind}}))
	}
	for _, kind := range []string{"tick", "skip", "tick", "tick"} {
		if code := emit(kind); code != appplugin.ServiceCallOK {
			t.Fatalf("emit code = %q", code)
		}
	}
	polled := t23OK(t, t23Call(t, consumerDispatcher, domainpluginabi.ServicesMethodPoll,
		map[string]any{"handle": handle}))
	events := polled["events"].([]any)
	if len(events) != 3 {
		t.Fatalf("polled events = %v", polled)
	}
	last := uint64(0)
	for _, raw := range events {
		event := raw.(map[string]any)
		sequence := uint64(event["sequence"].(float64))
		if sequence <= last {
			t.Fatalf("event sequence is not ordered: %v", events)
		}
		last = sequence
		if event["event"] != "pulse" {
			t.Fatalf("unexpected event %v", event)
		}
	}

	// A second subscription without a filter sees every event. Flood the
	// queue across rate-limit windows so the bounded queue drops the oldest
	// and reports a gap.
	all := t23OK(t, t23Call(t, consumerDispatcher, domainpluginabi.ServicesMethodSubscribe,
		map[string]any{"service": service, "event": "pulse"}))
	allHandle := all["handle"].(string)
	for i := 0; i < 300; i++ {
		if i%90 == 0 {
			clock.Advance(time.Second)
		}
		emit("flood")
	}
	gap := 0
	for i := 0; i < 20 && gap == 0; i++ {
		polled := t23OK(t, t23Call(t, consumerDispatcher, domainpluginabi.ServicesMethodPoll,
			map[string]any{"handle": allHandle, "limit": 64}))
		if value, ok := polled["gap"].(float64); ok {
			gap = int(value)
		}
	}
	if gap == 0 {
		t.Fatal("event overflow did not report a gap")
	}

	// The per-provider rate limit rejects the 101st event in one window.
	clock.Advance(time.Second)
	limited := false
	for i := 0; i < 101; i++ {
		if code := emit("rate"); code == appplugin.ServiceCallLimit {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("event rate limit was not enforced")
	}
}

// t23Clock is a deterministic clock shared by the container and host services.
type t23Clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *t23Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *t23Clock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(delta)
	c.mu.Unlock()
}

func TestCrossPluginDynamicRegistrationAndUnregistration(t *testing.T) {
	service := "plugin.acme.dynamic.feed"
	provider := t10BareManifest("acme.dynamic")
	provider.Provides = []domainplugin.Provision{{
		Service: service, Version: "1.0.0", Dynamic: true,
	}}
	consumer := t23ConsumerManifest("acme.dynamic-client", service, false)
	runtime := newT23Runtime()
	runtime.provide("acme.dynamic", func(context.Context, appplugin.HostSink,
		appplugin.ServiceRequest) ([]byte, error) {
		return t23Envelope(map[string]any{"ok": true}), nil
	})
	var changed []appplugin.ServiceChangedEvent
	var changedMu sync.Mutex
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)),
		appplugin.WithServiceChanged(func(ref appplugin.PluginRef, event appplugin.ServiceChangedEvent) {
			changedMu.Lock()
			changed = append(changed, event)
			changedMu.Unlock()
		}))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	t23LoadOK(t, container, provider, "dynamic")
	consumerFiber, err := t23Load(t, container, consumer, "dynamic-client")
	if consumerFiber == nil || consumerFiber.State() != domainplugin.StatePending {
		t.Fatalf("consumer before registration = %v, %v", consumerFiber, err)
	}
	if len(container.Services()) != 0 {
		t.Fatalf("dynamic-only declaration published %+v", container.Services())
	}
	providerDispatcher := runtime.dispatcher(t, "acme.dynamic", 0)
	registered := t23OK(t, t23Call(t, providerDispatcher, domainpluginabi.ServicesMethodRegister,
		map[string]any{
			"service": service, "version": "1.1.0",
			"methods": []map[string]any{{"name": "read"}},
		}))
	if registered["table_hash"] == "" {
		t.Fatalf("registration = %v", registered)
	}
	if consumerFiber.State() != domainplugin.StateActive {
		t.Fatalf("consumer after registration = %s (%s)",
			consumerFiber.State(), consumerFiber.Reason())
	}
	if code := t23Code(t23Invoke(t, runtime.dispatcher(t, "acme.dynamic-client", 0),
		service, "read", nil)); code != appplugin.ServiceCallOK {
		t.Fatalf("dynamic invoke code = %q", code)
	}
	changedMu.Lock()
	bound := false
	for _, event := range changed {
		if event.Service == service && event.Bound {
			bound = true
		}
	}
	changedMu.Unlock()
	if !bound {
		t.Fatalf("no service_changed bind event: %+v", changed)
	}

	// Versions never move backwards.
	if code := t23Code(t23Call(t, providerDispatcher, domainpluginabi.ServicesMethodRegister,
		map[string]any{"service": service, "version": "0.9.0",
			"methods": []map[string]any{{"name": "read"}}})); code != appplugin.ServiceCallInvalidArgument {
		t.Fatalf("backwards version code = %q", code)
	}
	t23OK(t, t23Call(t, providerDispatcher, domainpluginabi.ServicesMethodUnregister,
		map[string]any{"service": service}))
	if consumerFiber.State() != domainplugin.StatePending {
		t.Fatalf("consumer after unregistration = %s", consumerFiber.State())
	}
	changedMu.Lock()
	unbound := false
	for _, event := range changed {
		if event.Service == service && !event.Bound {
			unbound = true
		}
	}
	changedMu.Unlock()
	if !unbound {
		t.Fatalf("no service_changed unbind event: %+v", changed)
	}
}

func TestCrossPluginOptionalInjectionBindsAndUnbinds(t *testing.T) {
	service := "plugin.acme.optional.feed"
	provider := t23ProviderManifest("acme.optional", service,
		domainplugin.ServiceMethodDecl{Name: "read"})
	consumer := t23ConsumerManifest("acme.optional-client", service, true)
	runtime := newT23Runtime()
	runtime.provide("acme.optional", func(context.Context, appplugin.HostSink,
		appplugin.ServiceRequest) ([]byte, error) {
		return t23Envelope(map[string]any{"ok": true}), nil
	})
	var changed []appplugin.ServiceChangedEvent
	var changedMu sync.Mutex
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)),
		appplugin.WithServiceChanged(func(ref appplugin.PluginRef, event appplugin.ServiceChangedEvent) {
			changedMu.Lock()
			changed = append(changed, event)
			changedMu.Unlock()
		}))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	consumerFiber := t23LoadOK(t, container, consumer, "optional-client")
	dispatcher := runtime.dispatcher(t, "acme.optional-client", 0)
	if code := t23Code(t23Invoke(t, dispatcher, service, "read", nil)); code != appplugin.ServiceCallNotFound {
		t.Fatalf("unbound call code = %q", code)
	}
	status := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStatus,
		map[string]any{"service": service}))
	if status["bound"] != false || status["optional"] != true || status["declared"] != true {
		t.Fatalf("unbound status = %v", status)
	}

	providerFiber := t23LoadOK(t, container, provider, "optional")
	status = t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStatus,
		map[string]any{"service": service}))
	if status["bound"] != true || status["provider"] != "acme.optional" {
		t.Fatalf("bound status = %v", status)
	}
	if code := t23Code(t23Invoke(t, dispatcher, service, "read", nil)); code != appplugin.ServiceCallOK {
		t.Fatalf("bound call code = %q", code)
	}
	if err := container.Unload(context.Background(), providerFiber.ID()); err != nil {
		t.Fatalf("unload provider: %v", err)
	}
	if consumerFiber.State() != domainplugin.StateActive {
		t.Fatalf("optional consumer state = %s", consumerFiber.State())
	}
	if code := t23Code(t23Invoke(t, dispatcher, service, "read", nil)); code != appplugin.ServiceCallNotFound {
		t.Fatalf("call after unbind code = %q", code)
	}
	changedMu.Lock()
	binds, unbinds := 0, 0
	for _, event := range changed {
		if event.Service != service {
			continue
		}
		if event.Bound {
			binds++
		} else {
			unbinds++
		}
	}
	changedMu.Unlock()
	if binds == 0 || unbinds == 0 {
		t.Fatalf("service_changed binds=%d unbinds=%d: %+v", binds, unbinds, changed)
	}
}

func TestCrossPluginCallDepthIsBounded(t *testing.T) {
	const providers = 5
	manifests := make([]domainplugin.Manifest, 0, providers)
	services := make([]string, providers)
	for i := 0; i < providers; i++ {
		id := fmt.Sprintf("acme.depth%d", i+1)
		services[i] = fmt.Sprintf("plugin.%s.n%d", id, i+1)
	}
	for i := 0; i < providers; i++ {
		id := fmt.Sprintf("acme.depth%d", i+1)
		manifest := t23ProviderManifest(id, services[i],
			domainplugin.ServiceMethodDecl{Name: "read"})
		if i+1 < providers {
			manifest.Inject = []domainplugin.Injection{{Service: services[i+1]}}
		}
		manifests = append(manifests, manifest)
	}
	consumer := t23ConsumerManifest("acme.depth-client", services[0], false)
	runtime := newT23Runtime()
	for i := 0; i < providers; i++ {
		next := ""
		if i+1 < providers {
			next = services[i+1]
		}
		runtime.provide(fmt.Sprintf("acme.depth%d", i+1),
			func(ctx context.Context, sink appplugin.HostSink,
				request appplugin.ServiceRequest) ([]byte, error) {
				if next == "" {
					return t23Envelope(map[string]any{"tail": true}), nil
				}
				envelope := t23Services(t, sink, domainpluginabi.ServicesMethodInvoke,
					map[string]any{"service": next, "method": "read"})
				if code := t23Code(envelope); code != appplugin.ServiceCallOK {
					return t23ErrorEnvelope(code, "downstream"), nil
				}
				return t23Envelope(map[string]any{"relayed": true}), nil
			})
	}
	all := append(manifests, consumer)
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(all...), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, nil, nil)))
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	// Load the tail first so every required injection resolves as it appears.
	for i := providers - 1; i >= 0; i-- {
		dirName := fmt.Sprintf("depth%d", i+1)
		t23LoadOK(t, container, manifests[i], dirName)
	}
	t23LoadOK(t, container, consumer, "depth-client")
	dispatcher := runtime.dispatcher(t, "acme.depth-client", 0)
	if code := t23Code(t23Invoke(t, dispatcher, services[0], "read", nil)); code != appplugin.ServiceCallDeadlock {
		t.Fatalf("depth code = %q", code)
	}
}

// TestCrossPluginDiagnosticsAndPayloadFreeAudit covers the operator view: the
// service graph, pending consumers, counters, and audit rows that carry names
// and sizes but never payload bytes.
func TestCrossPluginDiagnosticsAndPayloadFreeAudit(t *testing.T) {
	service := "plugin.acme.diag.data"
	method := domainplugin.ServiceMethodDecl{Name: "read"}
	provider := t23ProviderManifest("acme.diag", service, method)
	provider.Provides[0].Events = []domainplugin.ServiceEventDecl{{Name: "tick"}}
	consumer := t23ConsumerManifest("acme.diag-client", service, false)
	missing := t23ConsumerManifest("acme.diag-other", "plugin.acme.diag.absent", false)

	runtime := newT23Runtime()
	runtime.provide("acme.diag", func(_ context.Context, _ appplugin.HostSink,
		request appplugin.ServiceRequest) ([]byte, error) {
		return t23Envelope(map[string]any{"secret": "provider-result-secret"}), nil
	})
	audit := &recordingAudit{}
	container := appplugin.NewContainer(runtime,
		newT10ManifestSource(provider, consumer, missing), &registrar{},
		appplugin.WithHostServices(t23NewHost(t, nil, audit, nil)),
		appplugin.WithServiceAudit(audit),
	)
	t.Cleanup(func() { _ = container.Close(context.Background()) })

	t23LoadOK(t, container, provider, "diag")
	consumerFiber := t23LoadOK(t, container, consumer, "diag-client")
	pendingFiber, err := t23Load(t, container, missing, "diag-other")
	if err == nil || pendingFiber == nil || pendingFiber.State() != domainplugin.StatePending {
		t.Fatalf("pending consumer = %+v, %v", pendingFiber, err)
	}

	dispatcher := runtime.dispatcher(t, "acme.diag-client", 0)
	if data := t23OK(t, t23Invoke(t, dispatcher, service, "read",
		map[string]any{"token": "caller-request-secret"})); data["payload"] == nil {
		t.Fatal("invoke returned no payload")
	}
	stream := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodStreamOpen,
		map[string]any{"service": service, "method": "read", "direction": "download"}))
	if stream["handle"] == nil {
		t.Fatalf("stream open = %v", stream)
	}
	subscription := t23OK(t, t23Call(t, dispatcher, domainpluginabi.ServicesMethodSubscribe,
		map[string]any{"service": service, "event": "tick"}))
	if subscription["handle"] == nil {
		t.Fatalf("subscribe = %v", subscription)
	}

	report := container.DiagnosticsReport()
	if report.ServiceCalls.Calls < 1 || report.ServiceCalls.Requests == 0 {
		t.Fatalf("service call counters = %+v", report.ServiceCalls)
	}
	if report.Streams != 1 {
		t.Fatalf("streams = %d, want one open stream", report.Streams)
	}
	if report.Subscriptions != 1 {
		t.Fatalf("subscriptions = %d, want one", report.Subscriptions)
	}
	foundBinding := false
	for _, binding := range report.ServiceBindings {
		if binding.Consumer == consumerFiber.ID() && binding.Service == service &&
			binding.Bound && binding.Provider == "acme.diag" {
			foundBinding = true
		}
	}
	if !foundBinding {
		t.Fatalf("service bindings = %+v", report.ServiceBindings)
	}
	if len(report.PendingConsumers) != 1 ||
		report.PendingConsumers[0].Consumer != pendingFiber.ID() ||
		report.PendingConsumers[0].Service != "plugin.acme.diag.absent" ||
		report.PendingConsumers[0].Reason == "" {
		t.Fatalf("pending consumers = %+v", report.PendingConsumers)
	}

	audit.mu.Lock()
	events := append([]domainplugin.AuditEvent(nil), audit.events...)
	audit.mu.Unlock()
	callRows := 0
	for _, event := range events {
		if event.Event != domainplugin.AuditService {
			continue
		}
		callRows++
		joined := event.Reason + "|" + event.Method + "|" + event.Provider
		if strings.Contains(joined, "secret") {
			t.Fatalf("audit row carries payload content: %+v", event)
		}
	}
	if callRows == 0 {
		t.Fatalf("no service audit rows: %+v", events)
	}
}
