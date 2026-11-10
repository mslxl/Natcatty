package wasm_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/mslxl/natcatty/adapters/wasm"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// recordingDispatcher returns one fixed envelope and records every service
// call, so the test proves the real wazero adapter routes each service module
// to the application dispatcher and copies the response back into guest
// memory.
type recordingDispatcher struct {
	mu       sync.Mutex
	calls    []appplugin.ServiceCall
	response []byte
}

var _ appplugin.ServiceDispatcher = (*recordingDispatcher)(nil)

func (d *recordingDispatcher) Call(_ context.Context, call appplugin.ServiceCall) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call)
	return d.response
}

func (d *recordingDispatcher) snapshot() []appplugin.ServiceCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]appplugin.ServiceCall(nil), d.calls...)
}

func TestServiceModulesDispatchToTheHostDispatcher(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })

	dispatcher := &recordingDispatcher{response: []byte(`{"code":"ok","data":{}}`)}
	var outputs []string
	instance, err := rt.Instantiate(context.Background(), smokeFixture(t, "services"),
		appplugin.HostSink{
			Services: dispatcher,
			Output:   func(data []byte) { outputs = append(outputs, string(data)) },
		})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = instance.Close(context.Background()) }()
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := instance.Dispatch(context.Background(), "plugin.acme.services.run"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(outputs) != 1 || outputs[0] != "services ok" {
		t.Fatalf("visible output = %v, want services ok", outputs)
	}

	calls := dispatcher.snapshot()
	want := []struct {
		service string
		method  uint32
		request string
	}{
		{appplugin.ServiceStorage, domainpluginabi.StorageMethodSet, `"key":"k"`},
		{appplugin.ServiceClipboard, domainpluginabi.ClipboardMethodWrite, `"text":"hi"`},
		{appplugin.ServiceNotifications, domainpluginabi.NotificationsMethodPost, `{}`},
		{appplugin.ServiceEvents, domainpluginabi.EventsMethodSubscribe, `{}`},
		{appplugin.ServiceSettings, domainpluginabi.SettingsMethodRead, `{}`},
		// The final call uses a response cap smaller than the envelope, so
		// the adapter returns the limit status instead of a partial write.
		{appplugin.ServiceClipboard, domainpluginabi.ClipboardMethodRead, `{}`},
	}
	if len(calls) != len(want) {
		t.Fatalf("dispatcher calls = %+v, want %d", calls, len(want))
	}
	for i, expectation := range want {
		call := calls[i]
		if call.Service != expectation.service || call.Method != expectation.method {
			t.Fatalf("call %d = %s/%d, want %s/%d", i, call.Service, call.Method,
				expectation.service, expectation.method)
		}
		if !strings.Contains(string(call.Request), expectation.request) {
			t.Fatalf("call %d request = %q, want it to contain %q",
				i, call.Request, expectation.request)
		}
	}
}

func TestServiceModulesRequireAHostDispatcher(t *testing.T) {
	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	instance, err := rt.Instantiate(context.Background(), smokeFixture(t, "services"),
		appplugin.HostSink{})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = instance.Close(context.Background()) }()
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	// The fixture expects status 0; an unavailable service makes it return a
	// guest failure instead of a partial success.
	err = instance.Dispatch(context.Background(), "plugin.acme.services.run")
	if err == nil {
		t.Fatal("Dispatch succeeded without a host service dispatcher")
	}
}

// TestGuestReachesRealHostServices wires the real wazero service modules to a
// real HostServices session (not a fake dispatcher). The fixture writes through
// storage and clipboard, so the host-side effects prove the guest-to-host path
// end to end.
func TestGuestReachesRealHostServices(t *testing.T) {
	authority := appplugin.NewAuthority()
	clipboard := appplugin.NewMemoryClipboard("")
	host := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority: authority,
		Clipboard: clipboard,
	})
	t.Cleanup(host.CloseAll)
	ref := appplugin.PluginRef{
		ID:      "acme.services",
		Name:    "Acme Services",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
	}
	subject := appplugin.SubjectForRef(ref)
	for _, permission := range []domainplugin.Permission{
		domainplugin.PermissionStorage,
		domainplugin.PermissionClipboardRead,
		domainplugin.PermissionClipboardWrite,
		domainplugin.PermissionNotifications,
	} {
		authority.Grant(subject, domainplugin.PermissionGrant{Permission: permission})
	}
	session := host.Open(ref, domainplugin.NewFiberID("installed", ref.ID, 1), nil)
	if session == nil {
		t.Fatal("HostServices.Open returned nil")
	}

	rt := wasm.NewRuntime(wasm.WithLimits(contractLimits))
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	instance, err := rt.Instantiate(context.Background(), smokeFixture(t, "services"),
		appplugin.HostSink{Services: session.Dispatcher()})
	if err != nil {
		t.Fatalf("Instantiate: %v", err)
	}
	defer func() { _ = instance.Close(context.Background()) }()
	// The service session needs the instance's generation-tagged handle table
	// before the guest subscribes.
	session.BindHandles(instance.Handles())
	if err := instance.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := instance.Dispatch(context.Background(), "plugin.acme.services.run"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	value, err := session.StorageGet("k")
	if err != nil || string(value) != "hi" {
		t.Fatalf("real storage value = %q, %v", value, err)
	}
	if text, err := clipboard.ReadText(context.Background()); err != nil || text != "hi" {
		t.Fatalf("real clipboard = %q, %v", text, err)
	}
}
