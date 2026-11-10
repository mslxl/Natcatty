package serviceprovider_test

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
	providerID = "acme.service-provider"
	consumerID = "acme.service-consumer"

	echoService     = "plugin.acme.service-provider.echo"
	extraService    = "plugin.acme.service-provider.extra"
	callbackService = "plugin.acme.service-consumer.callback"
)

// sampleArchive packages one committed sample as a .natplug using the same
// deterministic archive rules the real package validator enforces.
func sampleArchive(t *testing.T, directory, id string) string {
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
	archivePath := filepath.Join(t.TempDir(), id+".natplug")
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

// commandRecorder keeps every registered command handler. Two plugin instances
// of one identity may register the same qualified command in different groups,
// so handlers are stored per registration order.
type commandRecorder struct {
	mu       sync.Mutex
	next     uint64
	commands map[string][]recordedCommand
}

type recordedCommand struct {
	key     string
	id      uint64
	handler appplugin.Handler
}

func newCommandRecorder() *commandRecorder {
	return &commandRecorder{commands: make(map[string][]recordedCommand)}
}

func (r *commandRecorder) RegisterCommand(ref appplugin.PluginRef,
	command domainplugin.Command, run appplugin.Handler) (appplugin.Disposer, error) {
	qualified := domainplugin.QualifiedCommandID(ref.ID, command.ID)
	r.mu.Lock()
	r.next++
	id := r.next
	r.commands[qualified] = append(r.commands[qualified], recordedCommand{
		key: qualified, id: id, handler: run,
	})
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

func (r *commandRecorder) RegisterMenu(appplugin.PluginRef,
	domainplugin.Menu) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (r *commandRecorder) RegisterKeybinding(appplugin.PluginRef,
	domainplugin.Keybinding) (appplugin.Disposer, error) {
	return func(context.Context) error { return nil }, nil
}

func (r *commandRecorder) handlerAt(t *testing.T, pluginID, commandID string, index int) appplugin.Handler {
	t.Helper()
	qualified := domainplugin.QualifiedCommandID(pluginID, commandID)
	r.mu.Lock()
	entries := append([]recordedCommand(nil), r.commands[qualified]...)
	r.mu.Unlock()
	if index < 0 || index >= len(entries) {
		t.Fatalf("plugin %s registered %d handlers for %q, want index %d",
			pluginID, len(entries), qualified, index)
	}
	return entries[index].handler
}

type feedbackSink struct {
	mu      sync.Mutex
	outputs []string
}

func (f *feedbackSink) add(_ appplugin.PluginRef, output string) {
	f.mu.Lock()
	f.outputs = append(f.outputs, output)
	f.mu.Unlock()
}

func (f *feedbackSink) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.outputs...)
}

type sampleRun struct {
	manager  *appplugin.Manager
	store    *adapterplugininstall.Store
	recorder *commandRecorder
	feedback *feedbackSink
	records  map[string]domainplugin.InstallRecord
}

func startRun(t *testing.T) *sampleRun {
	t.Helper()
	store := adapterplugininstall.Open(t.TempDir())
	recorder := newCommandRecorder()
	feedback := &feedbackSink{}
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		recorder,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithManagerFeedback(func(ref appplugin.PluginRef, output string) {
			feedback.add(ref, output)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	return &sampleRun{
		manager: manager, store: store, recorder: recorder,
		feedback: feedback, records: make(map[string]domainplugin.InstallRecord),
	}
}

func (r *sampleRun) install(t *testing.T, directory, id string) {
	t.Helper()
	record, err := r.manager.InstallFile(context.Background(), sampleArchive(t, directory, id))
	if err != nil {
		t.Fatalf("InstallFile(%s): %v", id, err)
	}
	if record.ID != id {
		t.Fatalf("installed id = %q, want %q", record.ID, id)
	}
	r.records[id] = record
}

// review returns the consent rows keyed by permission.
func (r *sampleRun) review(t *testing.T, id string) (required, optional map[domainplugin.Permission]domainplugin.PermissionConsent) {
	t.Helper()
	view, err := r.manager.Review(context.Background(), id)
	if err != nil {
		t.Fatalf("Review(%s): %v", id, err)
	}
	required = make(map[domainplugin.Permission]domainplugin.PermissionConsent)
	optional = make(map[domainplugin.Permission]domainplugin.PermissionConsent)
	for _, row := range view.Required {
		required[row.Permission] = row
	}
	for _, row := range view.Optional {
		optional[row.Permission] = row
	}
	return required, optional
}

// enable grants the required permissions and, optionally, every optional
// permission the manifest requested.
func (r *sampleRun) enable(t *testing.T, id string, grantOptional bool) {
	t.Helper()
	view, err := r.manager.Review(context.Background(), id)
	if err != nil {
		t.Fatalf("Review(%s): %v", id, err)
	}
	decisions := make([]domainplugin.PermissionDecision, 0, len(view.Required)+len(view.Optional))
	for _, row := range view.Required {
		decisions = append(decisions, domainplugin.PermissionDecision{
			Permission: row.Permission, Allow: true,
		})
	}
	if grantOptional {
		for _, row := range view.Optional {
			decisions = append(decisions, domainplugin.PermissionDecision{
				Permission: row.Permission, Allow: true,
			})
		}
	}
	if err := r.manager.Enable(context.Background(), id, decisions); err != nil {
		t.Fatalf("Enable(%s): %v", id, err)
	}
}

func (r *sampleRun) runCommand(t *testing.T, pluginID, commandID string, index int) string {
	t.Helper()
	before := len(r.feedback.snapshot())
	handler := r.recorder.handlerAt(t, pluginID, commandID, index)
	if err := handler(context.Background()); err != nil {
		t.Fatalf("command %s/%s: %v", pluginID, commandID, err)
	}
	outputs := r.feedback.snapshot()
	if len(outputs) != before+1 {
		t.Fatalf("outputs after %s/%s = %v", pluginID, commandID, outputs)
	}
	return outputs[len(outputs)-1]
}

func requireLines(t *testing.T, report string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(report, line) {
			t.Fatalf("report is missing %q:\n%s", line, report)
		}
	}
}

func requireOrdered(t *testing.T, report string, needles ...string) {
	t.Helper()
	offset := 0
	for _, needle := range needles {
		index := strings.Index(report[offset:], needle)
		if index < 0 {
			t.Fatalf("report is missing ordered %q after byte %d:\n%s", needle, offset, report)
		}
		offset += index + len(needle)
	}
}

// TestServiceSamplesUseGrantedServices drives both committed WASM guests
// through the real archive validator, install store, consent review, manager,
// container, wazero runtime, and cross-plugin broker.
func TestServiceSamplesUseGrantedServices(t *testing.T) {
	run := startRun(t)
	run.install(t, "service-consumer", consumerID)
	run.install(t, "service-provider", providerID)

	providerRequired, providerOptional := run.review(t, providerID)
	if len(providerRequired) != 1 || providerRequired[domainplugin.PermissionCommands].Permission != domainplugin.PermissionCommands {
		t.Fatalf("provider required rows = %+v", providerRequired)
	}
	provide := providerOptional[domainplugin.PermissionServicesProvide].Scope
	if provide.Kind != domainplugin.ScopeServices ||
		provide.Service != "plugin.acme.service-provider." ||
		provide.Access != domainplugin.ServiceAccessRead ||
		provide.Version != "^1.0" {
		t.Fatalf("provider services.provide scope = %+v", provide)
	}
	providerInvoke := providerOptional[domainplugin.PermissionServicesInvoke].Scope
	if providerInvoke.Service != callbackService ||
		providerInvoke.Provider != consumerID ||
		providerInvoke.Version != "^1.0" ||
		providerInvoke.Access != domainplugin.ServiceAccessRead {
		t.Fatalf("provider services.invoke scope = %+v", providerInvoke)
	}

	consumerRequired, consumerOptional := run.review(t, consumerID)
	if len(consumerRequired) != 1 {
		t.Fatalf("consumer required rows = %+v", consumerRequired)
	}
	invoke := consumerOptional[domainplugin.PermissionServicesInvoke].Scope
	if invoke.Service != echoService ||
		invoke.Provider != providerID ||
		invoke.Version != "^1.0" ||
		invoke.Access != domainplugin.ServiceAccessWrite {
		t.Fatalf("consumer services.invoke scope = %+v", invoke)
	}
	consumerProvide := consumerOptional[domainplugin.PermissionServicesProvide].Scope
	if consumerProvide.Service != "plugin.acme.service-consumer." ||
		consumerProvide.Access != domainplugin.ServiceAccessRead {
		t.Fatalf("consumer services.provide scope = %+v", consumerProvide)
	}

	// The consumer starts first: its optional injection is unbound until the
	// provider becomes active.
	run.enable(t, consumerID, true)
	status := run.runCommand(t, consumerID, "status", 0)
	requireLines(t, status,
		"status code=ok bound=false reason=service-missing\n",
		"status.done\n",
	)

	run.enable(t, providerID, true)
	status = run.runCommand(t, consumerID, "status", 0)
	requireLines(t, status, "status code=ok bound=true version=1.2.0\n")

	// The provider invokes the consumer-owned callback service while the
	// consumer is idle: callback-by-service without a re-entrant call.
	callback := run.runCommand(t, providerID, "callback", 0)
	requireLines(t, callback,
		"callback code=ok token=cb-1 consumer=acme.service-consumer\n",
		"callback.done\n",
	)

	selftest := run.runCommand(t, consumerID, "selftest", 0)
	requireLines(t, selftest,
		"status code=ok bound=true version=1.2.0\n",
		"echo code=ok text=echo:hello\n",
		"download code=ok chunks=3 bytes=21\n",
		"upload code=ok chunks=2 bytes=9 close=ok\n",
		"duplex code=ok chunks=2 bytes=6 close=ok\n",
		"subscribe code=ok handle=",
		"notify code=ok\n",
		"poll code=ok events=3\n",
		"providers.extra.before code=ok count=0\n",
		"register_extra code=ok\n",
		"providers.extra.after code=ok count=1 dynamic=true\n",
		"unregister_extra code=ok\n",
		"providers.extra.final code=ok count=0\n",
		"selftest.done\n",
	)
	// Event delivery is ordered per provider and event.
	requireOrdered(t, selftest, `"sequence":1`, `"sequence":2`, `"sequence":3`)

	providers := run.runCommand(t, consumerID, "providers", 0)
	requireLines(t, providers, "providers.echo code=ok count=1\n", "providers.done\n")

	// The broker's graph shows both directions of the contract.
	container := run.manager.Container()
	consumerFiber := domainplugin.NewFiberID("installed", consumerID, 1)
	providerFiber := domainplugin.NewFiberID("installed", providerID, 1)
	binding, ok := container.Broker().Binding(consumerFiber, echoService)
	if !ok || !binding.Bound || binding.Provider != providerID ||
		binding.Version != "1.2.0" || binding.Access != domainplugin.ServiceAccessRead {
		t.Fatalf("consumer echo binding = %+v, %v", binding, ok)
	}
	callbackBinding, ok := container.Broker().Binding(providerFiber, callbackService)
	if !ok || !callbackBinding.Bound || callbackBinding.Provider != consumerID {
		t.Fatalf("provider callback binding = %+v, %v", callbackBinding, ok)
	}
	rows := container.Services()
	seen := make(map[string]bool)
	for _, row := range rows {
		seen[row.Service] = true
	}
	if !seen[echoService] || !seen[callbackService] || seen[extraService] {
		t.Fatalf("published services = %+v", rows)
	}
}

// TestServiceSamplesReportTypedDenials enables the provider with its grants
// but the consumer without services.invoke or services.provide, so every call
// reaches the authority boundary and fails closed.
func TestServiceSamplesReportTypedDenials(t *testing.T) {
	run := startRun(t)
	run.install(t, "service-consumer", consumerID)
	run.install(t, "service-provider", providerID)

	run.enable(t, providerID, true)
	run.enable(t, consumerID, false)

	status := run.runCommand(t, consumerID, "status", 0)
	requireLines(t, status,
		"status code=ok bound=false reason=permission-denied\n",
		"status.done\n",
	)

	selftest := run.runCommand(t, consumerID, "selftest", 0)
	requireLines(t, selftest,
		"status code=ok bound=false reason=permission-denied\n",
		"echo code=permission-denied permission=services.invoke\n",
		"download code=permission-denied permission=services.invoke\n",
		"upload code=permission-denied permission=services.invoke\n",
		"duplex code=permission-denied permission=services.invoke\n",
		"subscribe code=permission-denied permission=services.invoke\n",
		"notify code=permission-denied permission=services.invoke\n",
		"poll skipped\n",
		"providers.extra.before code=ok count=0\n",
		"register_extra code=permission-denied permission=services.invoke\n",
		"providers.extra.after code=ok count=0\n",
		"unregister_extra code=permission-denied permission=services.invoke\n",
		"providers.extra.final code=ok count=0\n",
		"selftest.done\n",
	)

	callback := run.runCommand(t, providerID, "callback", 0)
	requireLines(t, callback, "callback code=not-found\n", "callback.done\n")

	// The consumer never published its callback service without the provide
	// grant, and the provider's echo service is still published.
	for _, row := range run.manager.Container().Services() {
		if row.Service == callbackService {
			t.Fatalf("denied consumer published %s", callbackService)
		}
	}
}

// TestServiceSamplesSelectNearestOfMultipleProviders loads a second instance
// of each sample in a child group. The nested consumer sees two providers of
// the same service and binds the nearest one; the outer consumer sees only its
// own group's provider.
func TestServiceSamplesSelectNearestOfMultipleProviders(t *testing.T) {
	ctx := context.Background()
	run := startRun(t)
	run.install(t, "service-consumer", consumerID)
	run.install(t, "service-provider", providerID)
	run.enable(t, consumerID, true)
	run.enable(t, providerID, true)

	container := run.manager.Container()
	if err := container.DefineGroup(domainplugin.Group{Name: "nested", Parent: "installed"}); err != nil {
		t.Fatalf("DefineGroup: %v", err)
	}
	providerDir, err := run.store.PackageDir(ctx, providerID)
	if err != nil {
		t.Fatalf("provider PackageDir: %v", err)
	}
	consumerDir, err := run.store.PackageDir(ctx, consumerID)
	if err != nil {
		t.Fatalf("consumer PackageDir: %v", err)
	}
	nestedProvider, err := container.LoadInstalledInGroup(ctx, "nested", providerDir,
		run.records[providerID].Source)
	if err != nil {
		t.Fatalf("load nested provider: %v", err)
	}
	nestedConsumer, err := container.LoadInstalledInGroup(ctx, "nested", consumerDir,
		run.records[consumerID].Source)
	if err != nil {
		t.Fatalf("load nested consumer: %v", err)
	}
	if nestedProvider.State() != domainplugin.StateActive ||
		nestedConsumer.State() != domainplugin.StateActive {
		t.Fatalf("nested states = %s, %s", nestedProvider.State(), nestedConsumer.State())
	}

	// Both provider rows are visible from the nested consumer; the outer
	// consumer still sees only the provider in its own group.
	nestedProviders := run.runCommand(t, consumerID, "providers", 1)
	requireLines(t, nestedProviders, "providers.echo code=ok count=2\n")
	outerProviders := run.runCommand(t, consumerID, "providers", 0)
	requireLines(t, outerProviders, "providers.echo code=ok count=1\n")

	// Nearest scope wins: the nested binding names the nested provider fiber.
	container = run.manager.Container()
	binding, ok := container.Broker().Binding(nestedConsumer.ID(), echoService)
	if !ok || !binding.Bound || binding.ProviderFiber != nestedProvider.ID() {
		t.Fatalf("nested consumer binding = %+v, %v", binding, ok)
	}
	outerBinding, ok := container.Broker().Binding(
		domainplugin.NewFiberID("installed", consumerID, 1), echoService)
	if !ok || outerBinding.ProviderFiber != domainplugin.NewFiberID("installed", providerID, 1) {
		t.Fatalf("outer consumer binding = %+v, %v", outerBinding, ok)
	}

	// The nested provider's optional injection of the consumer-owned callback
	// also resolves to the nearest instance and answers a real call.
	callback := run.runCommand(t, providerID, "callback", 1)
	requireLines(t, callback, "callback code=ok token=cb-1 consumer=acme.service-consumer\n")
}
