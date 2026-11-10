package app

import (
	"archive/zip"
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
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	"github.com/mslxl/natcatty/internal/testutil"
)

const (
	serviceProviderID = "acme.service-provider"
	serviceConsumerID = "acme.service-consumer"
	serviceEcho       = "plugin.acme.service-provider.echo"
	serviceCallback   = "plugin.acme.service-consumer.callback"
)

// serviceSampleArchive packages one committed sample using the same
// deterministic archive rules the production validator enforces.
func serviceSampleArchive(t *testing.T, directory string) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", directory)
	manifest, err := os.ReadFile(filepath.Join(root, "plugin.toml"))
	if err != nil {
		t.Fatalf("read %s manifest: %v", directory, err)
	}
	module, err := os.ReadFile(filepath.Join(root, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read %s module: %v", directory, err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "i18n", "en.toml"))
	if err != nil {
		t.Fatalf("read %s catalog: %v", directory, err)
	}
	archivePath := filepath.Join(t.TempDir(), directory+".natplug")
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

type serviceCommandRecorder struct {
	mu       sync.Mutex
	next     uint64
	commands map[string][]serviceRecordedCommand
}

type serviceRecordedCommand struct {
	id      uint64
	handler appplugin.Handler
}

func newServiceCommandRecorder() *serviceCommandRecorder {
	return &serviceCommandRecorder{commands: make(map[string][]serviceRecordedCommand)}
}

func (r *serviceCommandRecorder) RegisterCommand(ref appplugin.PluginRef,
	command domainplugin.Command, run appplugin.Handler) (appplugin.Disposer, error) {
	qualified := domainplugin.QualifiedCommandID(ref.ID, command.ID)
	r.mu.Lock()
	r.next++
	id := r.next
	r.commands[qualified] = append(r.commands[qualified], serviceRecordedCommand{id: id, handler: run})
	r.mu.Unlock()
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			entries := r.commands[qualified]
			for i := range entries {
				if entries[i].id == id {
					r.commands[qualified] = append(entries[:i], entries[i+1:]...)
					break
				}
			}
			r.mu.Unlock()
		})
		return nil
	}, nil
}

func (r *serviceCommandRecorder) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (r *serviceCommandRecorder) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (r *serviceCommandRecorder) count(pluginID, commandID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.commands[domainplugin.QualifiedCommandID(pluginID, commandID)])
}

func (r *serviceCommandRecorder) call(t *testing.T, pluginID, commandID string) {
	t.Helper()
	qualified := domainplugin.QualifiedCommandID(pluginID, commandID)
	r.mu.Lock()
	entries := append([]serviceRecordedCommand(nil), r.commands[qualified]...)
	r.mu.Unlock()
	if len(entries) == 0 {
		t.Fatalf("plugin %s has no handler for %q", pluginID, qualified)
	}
	if err := entries[0].handler(context.Background()); err != nil {
		t.Fatalf("command %s: %v", qualified, err)
	}
}

type serviceFeedback struct {
	mu      sync.Mutex
	outputs []string
}

func (f *serviceFeedback) add(_ appplugin.PluginRef, output string) {
	f.mu.Lock()
	f.outputs = append(f.outputs, output)
	f.mu.Unlock()
}

func (f *serviceFeedback) reset() {
	f.mu.Lock()
	f.outputs = nil
	f.mu.Unlock()
}

func (f *serviceFeedback) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.outputs, "")
}

type serviceSampleRun struct {
	manager  *appplugin.Manager
	recorder *serviceCommandRecorder
	feedback *serviceFeedback
}

func startServiceSampleRun(t *testing.T) *serviceSampleRun {
	t.Helper()
	recorder := newServiceCommandRecorder()
	feedback := &serviceFeedback{}
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		recorder,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		adapterplugininstall.Open(t.TempDir()),
		appplugin.WithManagerFeedback(feedback.add),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return &serviceSampleRun{manager: manager, recorder: recorder, feedback: feedback}
}

func (r *serviceSampleRun) install(t *testing.T, directory string) {
	t.Helper()
	if _, err := r.manager.InstallFile(context.Background(), serviceSampleArchive(t, directory)); err != nil {
		t.Fatalf("InstallFile(%s): %v", directory, err)
	}
}

func (r *serviceSampleRun) enable(t *testing.T, id string, grantOptional bool) {
	t.Helper()
	view, err := r.manager.Review(context.Background(), id)
	if err != nil {
		t.Fatalf("Review(%s): %v", id, err)
	}
	decisions := make([]domainplugin.PermissionDecision, 0, len(view.Required)+len(view.Optional))
	for _, row := range view.Required {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Scope: row.Scope, Allow: true,
		})
	}
	if grantOptional {
		for _, row := range view.Optional {
			decisions = append(decisions, domainplugin.PermissionDecision{
				Permission: row.Permission, Scope: row.Scope, Allow: true,
			})
		}
	}
	if err := r.manager.Enable(context.Background(), id, decisions); err != nil {
		t.Fatalf("Enable(%s): %v", id, err)
	}
}

func (r *serviceSampleRun) run(t *testing.T, pluginID, commandID string) string {
	t.Helper()
	r.feedback.reset()
	r.recorder.call(t, pluginID, commandID)
	return r.feedback.joined()
}

func requireContains(t *testing.T, body, needle string) {
	t.Helper()
	if !strings.Contains(body, needle) {
		t.Fatalf("output is missing %q:\n%s", needle, body)
	}
}

func requireServicePublished(t *testing.T, manager *appplugin.Manager, service string, want bool) {
	t.Helper()
	for _, row := range manager.Container().Services() {
		if row.Service == service {
			if !want {
				t.Fatalf("service %s is still published: %+v", service, row)
			}
			return
		}
	}
	if want {
		t.Fatalf("service %s is not published", service)
	}
}

// TestServiceSamplesRunThroughTheAppWiring drives the committed provider and
// consumer samples through the real install store, consent review, manager,
// container, wazero runtime, and cross-plugin broker. Disabling and revoking
// the provider must unbind the consumer and remove every published service,
// and re-enabling it must rebuild the same bindings.
func TestServiceSamplesRunThroughTheAppWiring(t *testing.T) {
	ctx := context.Background()
	run := startServiceSampleRun(t)
	run.install(t, "service-consumer")
	run.install(t, "service-provider")

	run.enable(t, serviceConsumerID, true)
	requireContains(t, run.run(t, serviceConsumerID, "status"),
		"status code=ok bound=false reason=service-missing\n")

	run.enable(t, serviceProviderID, true)
	requireContains(t, run.run(t, serviceConsumerID, "status"),
		"status code=ok bound=true version=1.2.0\n")

	selftest := run.run(t, serviceConsumerID, "selftest")
	for _, line := range []string{
		"status code=ok bound=true version=1.2.0\n",
		"echo code=ok text=echo:hello\n",
		"download code=ok chunks=3 bytes=21\n",
		"subscribe code=ok handle=",
		"poll code=ok events=3\n",
		"selftest.done\n",
	} {
		requireContains(t, selftest, line)
	}
	requireContains(t, run.run(t, serviceProviderID, "callback"),
		"callback code=ok token=cb-1 consumer=acme.service-consumer\n")

	container := run.manager.Container()
	consumerFiber := domainplugin.NewFiberID("installed", serviceConsumerID, 1)
	providerFiber := domainplugin.NewFiberID("installed", serviceProviderID, 1)
	binding, ok := container.Broker().Binding(consumerFiber, serviceEcho)
	if !ok || !binding.Bound || binding.Provider != serviceProviderID {
		t.Fatalf("consumer echo binding = %+v, %v", binding, ok)
	}
	if _, ok := container.Broker().Binding(providerFiber, serviceCallback); !ok {
		t.Fatal("provider callback binding is missing")
	}
	requireServicePublished(t, run.manager, serviceEcho, true)
	requireServicePublished(t, run.manager, serviceCallback, true)

	if err := run.manager.Disable(ctx, serviceProviderID); err != nil {
		t.Fatalf("Disable(provider): %v", err)
	}
	if run.recorder.count(serviceProviderID, "callback") != 0 {
		t.Fatal("disabling the provider left its command handler registered")
	}
	requireServicePublished(t, run.manager, serviceEcho, false)
	// Callback belongs to the consumer, but the provider command that invokes
	// it is gone; only the consumer's own service remains.
	requireServicePublished(t, run.manager, serviceCallback, true)
	requireContains(t, run.run(t, serviceConsumerID, "status"),
		"status code=ok bound=false reason=service-missing\n")

	run.enable(t, serviceProviderID, true)
	if run.recorder.count(serviceProviderID, "callback") != 1 {
		t.Fatal("re-enabling the provider did not restore its command handler")
	}
	requireServicePublished(t, run.manager, serviceEcho, true)
	requireContains(t, run.run(t, serviceConsumerID, "status"),
		"status code=ok bound=true version=1.2.0\n")

	// A required revoke unloads the provider, so its service, command, and
	// the consumer binding all disappear together.
	if err := run.manager.Revoke(ctx, serviceProviderID,
		domainplugin.PermissionCommands); err != nil {
		t.Fatalf("Revoke(provider commands): %v", err)
	}
	requireServicePublished(t, run.manager, serviceEcho, false)
	requireContains(t, run.run(t, serviceConsumerID, "status"),
		"status code=ok bound=false reason=service-missing\n")

	for _, id := range []string{serviceProviderID, serviceConsumerID} {
		if err := run.manager.Uninstall(ctx, id, true); err != nil {
			t.Fatalf("Uninstall(%s): %v", id, err)
		}
	}
	if fibers := run.manager.Container().Fibers(); len(fibers) != 0 {
		t.Fatalf("uninstall left fibers: %+v", fibers)
	}
	if services := run.manager.Container().Services(); len(services) != 0 {
		t.Fatalf("uninstall left services: %+v", services)
	}
	if run.recorder.count(serviceConsumerID, "status") != 0 {
		t.Fatal("uninstall left the consumer command handler registered")
	}
}
