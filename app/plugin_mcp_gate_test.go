package app

import (
	"context"
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
	"github.com/mslxl/natcatty/application/command"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
)

func newGateTestManager(t *testing.T, runtime appplugin.Runtime,
	tools *mcpPluginTools, changed func()) *appplugin.Manager {
	t.Helper()
	var manager *appplugin.Manager
	manager = appplugin.NewManager(
		runtime,
		adapterpluginmanifest.Source{},
		newPluginSurface(command.NewRegistry()),
		adapterpluginpackage.New(adapterwasm.Validator{}),
		adapterplugininstall.Open(t.TempDir()),
		appplugin.WithMCPToolRegistrar(tools),
		appplugin.WithMCPToolsChanged(changed),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return manager
}

func optionalPermissionSample(t *testing.T) string {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t),
		"sdk", "samples", "mcp-tools", "plugin.toml"))
	if err != nil {
		t.Fatalf("read sample manifest: %v", err)
	}
	variant := strings.Replace(string(manifest),
		`required = ["mcp.tools"]`, `optional = ["mcp.tools"]`, 1)
	if variant == string(manifest) {
		t.Fatal("the sample manifest does not declare mcp.tools as required")
	}
	return mcpSampleArchiveWithManifest(t, variant)
}

// TestMCPPluginToolsRequireThePluginGrant drives the first gate on an ACTIVE
// plugin: the optional mcp.tools grant is absent, present, then revoked. The
// plugin stays ACTIVE throughout, so only the authority check changes.
func TestMCPPluginToolsRequireThePluginGrant(t *testing.T) {
	ctx := context.Background()
	var manager *appplugin.Manager
	tools := newMCPPluginTools(func() *appplugin.Manager { return manager })
	manager = newGateTestManager(t, adapterwasm.NewRuntime(), tools, nil)

	record, err := manager.InstallFile(ctx, optionalPermissionSample(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if err := manager.Enable(ctx, record.ID, nil); err != nil {
		t.Fatalf("Enable without the optional grant: %v", err)
	}
	detail, ok, err := manager.Detail(ctx, record.ID)
	if err != nil || !ok || detail.State != domainplugin.StateActive {
		t.Fatalf("detail = %+v, ok=%v err=%v", detail, ok, err)
	}

	name := domainplugin.QualifiedMCPToolName(record.ID, "echo")
	if listed := tools.List(); len(listed) != 0 {
		t.Fatalf("tools listed without the grant: %#v", listed)
	}
	if _, gateErr := tools.Descriptor(ctx, name); gateErr == nil ||
		gateErr.Code != appmcp.CodePluginToolDenied || gateErr.Retryable {
		t.Fatalf("descriptor without the grant = %+v", gateErr)
	}

	if err := manager.RequestOptional(ctx, record.ID,
		domainplugin.PermissionMCPTools, domainplugin.Scope{}, true, false); err != nil {
		t.Fatalf("RequestOptional: %v", err)
	}
	if listed := tools.List(); len(listed) != 5 {
		t.Fatalf("tools after the grant = %#v", listed)
	}
	entry, gateErr := tools.Descriptor(ctx, name)
	if gateErr != nil {
		t.Fatalf("descriptor after the grant: %+v", gateErr)
	}
	result, callErr := tools.Invoke(ctx, entry, map[string]any{"text": "granted"}, nil)
	if callErr != nil {
		t.Fatalf("Invoke after the grant: %+v", callErr)
	}
	if text, _ := result.Content[0].(map[string]any); text["text"] != "echo: granted" {
		t.Fatalf("invoke content = %#v", result.Content)
	}

	if err := manager.Revoke(ctx, record.ID, domainplugin.PermissionMCPTools); err != nil {
		t.Fatalf("Revoke optional grant: %v", err)
	}
	detail, ok, err = manager.Detail(ctx, record.ID)
	if err != nil || !ok || detail.State != domainplugin.StateActive || !detail.Enabled {
		t.Fatalf("detail after an optional revoke = %+v, ok=%v err=%v", detail, ok, err)
	}
	if listed := tools.List(); len(listed) != 0 {
		t.Fatalf("tools listed after the revoke: %#v", listed)
	}
	if _, gateErr := tools.Descriptor(ctx, name); gateErr == nil ||
		gateErr.Code != appmcp.CodePluginToolDenied {
		t.Fatalf("descriptor after the revoke = %+v", gateErr)
	}
}

// gatingRuntime blocks one module's instantiation so the test can observe the
// reload window in which the record is enabled but the fiber is not ACTIVE.
type gatingRuntime struct {
	inner appplugin.Runtime

	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
}

func newGatingRuntime() *gatingRuntime {
	return &gatingRuntime{
		inner:   adapterwasm.NewRuntime(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
}

func (r *gatingRuntime) arm() {
	r.mu.Lock()
	r.armed = true
	r.mu.Unlock()
}

func (r *gatingRuntime) Instantiate(ctx context.Context, spec appplugin.ModuleSpec,
	sink appplugin.HostSink) (appplugin.Instance, error) {
	r.mu.Lock()
	armed := r.armed
	r.mu.Unlock()
	if armed && spec.Name == mcpSampleID {
		select {
		case r.entered <- struct{}{}:
		default:
		}
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return r.inner.Instantiate(ctx, spec, sink)
}

func (r *gatingRuntime) Close(ctx context.Context) error { return r.inner.Close(ctx) }

// TestMCPPluginToolCallDuringReloadIsRetryable holds the plugin in LOADING and
// proves the tool disappears from the list while a call fails with the typed,
// retryable reload error. Releasing the load restores both.
func TestMCPPluginToolCallDuringReloadIsRetryable(t *testing.T) {
	ctx := context.Background()
	runtime := newGatingRuntime()
	var manager *appplugin.Manager
	tools := newMCPPluginTools(func() *appplugin.Manager { return manager })
	manager = newGateTestManager(t, runtime, tools, nil)

	record, err := manager.InstallFile(ctx, mcpSampleArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	decisions := []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionMCPTools, Allow: true},
	}
	if err := manager.Enable(ctx, record.ID, decisions); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	name := domainplugin.QualifiedMCPToolName(record.ID, "echo")
	if _, gateErr := tools.Descriptor(ctx, name); gateErr != nil {
		t.Fatalf("descriptor before reload: %+v", gateErr)
	}
	if err := manager.Disable(ctx, record.ID); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	runtime.arm()
	enabled := make(chan error, 1)
	go func() { enabled <- manager.Enable(ctx, record.ID, decisions) }()
	select {
	case <-runtime.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the reload never reached module instantiation")
	}

	if listed := tools.List(); len(listed) != 0 {
		t.Fatalf("tools listed during reload: %#v", listed)
	}
	_, gateErr := tools.Descriptor(ctx, name)
	if gateErr == nil || gateErr.Code != appmcp.CodePluginToolReloading || !gateErr.Retryable {
		t.Fatalf("descriptor during reload = %+v", gateErr)
	}

	close(runtime.release)
	select {
	case err := <-enabled:
		if err != nil {
			t.Fatalf("Enable after reload: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the reload did not finish")
	}
	if _, gateErr := tools.Descriptor(ctx, name); gateErr != nil {
		t.Fatalf("descriptor after reload: %+v", gateErr)
	}
	if listed := tools.List(); len(listed) != 5 {
		t.Fatalf("tools after reload = %#v", listed)
	}
}
