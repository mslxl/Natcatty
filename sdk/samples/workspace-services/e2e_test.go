package workspaceservices_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strconv"
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
	sampleID                 = "acme.workspace-services"
	sampleFilesPlaceholder   = "/__NATCATTY_WORKSPACE__"
	sampleFileContents       = "workspace sample"
	sampleTerminalOutput     = "workspace terminal\n"
	sampleGrantedPermission  = domainplugin.PermissionCommands
	sampleWorkspaceScopeKind = domainplugin.ScopeFiles
)

// sampleArchive packages the committed sample with its directory scopes
// rewritten to a real temporary directory. The real archive validator, install
// store, consent review, manager, container, wazero runtime, and host-service
// stack all run unchanged.
func sampleArchive(t *testing.T, filesRoot string) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "workspace-services")
	manifest, err := os.ReadFile(filepath.Join(root, "plugin.toml"))
	if err != nil {
		t.Fatalf("read sample manifest: %v", err)
	}
	quotedPath := strconv.Quote(filesRoot)
	manifest = []byte(strings.ReplaceAll(string(manifest),
		strconv.Quote(sampleFilesPlaceholder), quotedPath))
	module, err := os.ReadFile(filepath.Join(root, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read sample module: %v", err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "i18n", "en.toml"))
	if err != nil {
		t.Fatalf("read sample catalog: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), sampleID+".natplug")
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

type commandRecorder struct {
	mu       sync.Mutex
	commands map[string]appplugin.Handler
}

func newCommandRecorder() *commandRecorder {
	return &commandRecorder{commands: make(map[string]appplugin.Handler)}
}

func (r *commandRecorder) RegisterCommand(ref appplugin.PluginRef,
	command domainplugin.Command, run appplugin.Handler) (appplugin.Disposer, error) {
	qualified := domainplugin.QualifiedCommandID(ref.ID, command.ID)
	r.mu.Lock()
	r.commands[qualified] = run
	r.mu.Unlock()
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			delete(r.commands, qualified)
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

func (r *commandRecorder) handler(t *testing.T, commandID string) appplugin.Handler {
	t.Helper()
	qualified := domainplugin.QualifiedCommandID(sampleID, commandID)
	r.mu.Lock()
	handler := r.commands[qualified]
	r.mu.Unlock()
	if handler == nil {
		t.Fatalf("plugin did not register command %q", qualified)
	}
	return handler
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
	recorder *commandRecorder
	feedback *feedbackSink
	hosts    *appplugin.MemoryHostCatalog
	sessions *appplugin.MemorySessionCatalog
	terminal *appplugin.MemoryTerminalCatalog
	remote   *appplugin.MemoryRemoteFiles
	files    string
	record   domainplugin.InstallRecord
}

func startSample(t *testing.T, grantOptional bool) *sampleRun {
	t.Helper()
	ctx := context.Background()
	filesRoot := t.TempDir()
	store := adapterplugininstall.Open(t.TempDir())
	recorder := newCommandRecorder()
	feedback := &feedbackSink{}
	hosts := appplugin.NewMemoryHostCatalog(
		appplugin.HostSummary{ID: "host-a", Label: "Alpha", Hostname: "a.example", Port: 22, Protocol: "ssh"},
		appplugin.HostSummary{ID: "host-b", Label: "Beta", Hostname: "b.example", Port: 22, Protocol: "ssh"},
	)
	sessions := appplugin.NewMemorySessionCatalog(
		appplugin.SessionSummary{ID: "session-1", Kind: "ssh", Title: "one", HostID: "host-a", State: "running"},
		appplugin.SessionSummary{ID: "session-2", Kind: "ssh", Title: "two", HostID: "host-b", State: "running"},
	)
	terminal := appplugin.NewMemoryTerminalCatalog(
		[]appplugin.TerminalInfo{{SessionID: "session-1", Title: "one", Cols: 80, Rows: 24}},
		map[string][][]byte{"session-1": {[]byte(sampleTerminalOutput)}},
	)
	remote := appplugin.NewMemoryRemoteFiles()
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		recorder,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithHostCatalog(hosts),
		appplugin.WithSessionCatalog(sessions),
		appplugin.WithTerminal(terminal),
		appplugin.WithRemoteFiles(remote),
		appplugin.WithServiceDataDir(store.DataDir),
		appplugin.WithManagerFeedback(func(ref appplugin.PluginRef, output string) {
			feedback.add(ref, output)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, sampleArchive(t, filesRoot))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if record.ID != sampleID || record.Source != domainplugin.SourceLocalFile {
		t.Fatalf("install record = %+v", record)
	}

	review, err := manager.Review(ctx, sampleID)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(review.Required) != 1 || review.Required[0].Permission != sampleGrantedPermission {
		t.Fatalf("required consent rows = %+v", review.Required)
	}
	scopes := make(map[domainplugin.Permission]domainplugin.Scope)
	for _, row := range review.Optional {
		scopes[row.Permission] = row.Scope
	}
	if len(scopes) != 5 {
		t.Fatalf("optional consent rows = %+v", review.Optional)
	}
	if scope := scopes[domainplugin.PermissionHostsRead]; scope.Kind != domainplugin.ScopeHost ||
		scope.Host != "host-a" {
		t.Fatalf("hosts.read consent scope = %+v", scope)
	}
	if scope := scopes[domainplugin.PermissionSessionsRead]; scope.Kind != domainplugin.ScopeSession ||
		scope.Session != "session-1" {
		t.Fatalf("sessions.read consent scope = %+v", scope)
	}
	if scope := scopes[domainplugin.PermissionTerminalObserve]; scope.Kind != domainplugin.ScopeSession ||
		scope.Session != "session-1" {
		t.Fatalf("terminal.observe consent scope = %+v", scope)
	}
	for _, permission := range []domainplugin.Permission{
		domainplugin.PermissionFilesRead, domainplugin.PermissionFilesWrite,
	} {
		scope := scopes[permission]
		if scope.Kind != sampleWorkspaceScopeKind || scope.Path != filesRoot {
			t.Fatalf("%s consent scope = %+v, want %s", permission, scope, filesRoot)
		}
	}

	decisions := []domainplugin.PermissionDecision{{
		Permission: sampleGrantedPermission, Allow: true,
	}}
	if grantOptional {
		for _, permission := range []domainplugin.Permission{
			domainplugin.PermissionHostsRead,
			domainplugin.PermissionSessionsRead,
			domainplugin.PermissionTerminalObserve,
			domainplugin.PermissionFilesRead,
			domainplugin.PermissionFilesWrite,
		} {
			decisions = append(decisions, domainplugin.PermissionDecision{
				Permission: permission, Allow: true,
			})
		}
	}
	if err := manager.Enable(ctx, sampleID, decisions); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	list, err := manager.List(ctx)
	if err != nil || len(list) != 1 || list[0].Status != appplugin.StatusActive {
		t.Fatalf("List after Enable = %+v, %v", list, err)
	}
	return &sampleRun{
		manager: manager, recorder: recorder, feedback: feedback,
		hosts: hosts, sessions: sessions, terminal: terminal, remote: remote,
		files: filesRoot, record: record,
	}
}

func runCommand(t *testing.T, run *sampleRun, commandID string) string {
	t.Helper()
	before := len(run.feedback.snapshot())
	handler := run.recorder.handler(t, commandID)
	if err := handler(context.Background()); err != nil {
		t.Fatalf("command %q: %v", commandID, err)
	}
	outputs := run.feedback.snapshot()
	if len(outputs) != before+1 {
		t.Fatalf("outputs after %q = %v", commandID, outputs)
	}
	return outputs[len(outputs)-1]
}

func requireReportLines(t *testing.T, report string, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if !strings.Contains(report, line) {
			t.Fatalf("report is missing %q:\n%s", line, report)
		}
	}
}

func TestWorkspaceServicesSampleUsesGrantedServices(t *testing.T) {
	run := startSample(t, true)
	report := runCommand(t, run, "selftest")
	requireReportLines(t, report,
		"files.preopens code=ok\n",
		"files.open code=ok handle=",
		"files.write code=ok written=16\n",
		"files.read code=ok data=d29ya3NwYWNlIHNhbXBsZQ==\n",
		"files.read.match=ok\n",
		"files.stat code=ok\n",
		"files.close code=ok\n",
		"files.open.outside code=denied permission=files.read\n",
		"files.open.glob code=invalid permission=files.read\n",
		"hosts.list code=ok\n",
		"hosts.get.granted code=ok\n",
		"hosts.get.denied code=denied permission=hosts.read\n",
		"hosts.create code=denied permission=hosts.write\n",
		"sessions.list code=ok\n",
		"sessions.get.granted code=ok\n",
		"sessions.get.denied code=denied permission=sessions.read\n",
		"terminal.observe code=ok handle=",
		"terminal.read code=ok data=d29ya3NwYWNlIHRlcm1pbmFsCg==\n",
		"terminal.read.match=ok\n",
		"terminal.close code=ok\n",
		"terminal.write code=denied permission=terminal.write\n",
		"terminal.info code=denied permission=terminal.info\n",
		"files.remote_list code=denied permission=files.remote.read\n",
		"files.remote_write code=denied permission=files.remote.write\n",
		"selftest.done\n",
	)

	// The real local filesystem received the guest's range write, and the
	// terminal tap was read-only: no input reached the Session.
	content, err := os.ReadFile(filepath.Join(run.files, "sample.txt"))
	if err != nil {
		t.Fatalf("read sample file: %v", err)
	}
	if string(content) != sampleFileContents {
		t.Fatalf("sample file = %q, want %q", content, sampleFileContents)
	}
	if writes := run.terminal.Writes("session-1"); len(writes) != 0 {
		t.Fatalf("terminal writes = %q, want none", writes)
	}
	if count := run.terminal.ObservationCount(); count != 0 {
		t.Fatalf("live terminal observations after close = %d", count)
	}
	if hosts, err := run.hosts.List(context.Background()); err != nil || len(hosts) != 2 {
		t.Fatalf("host catalog changed: %+v, %v", hosts, err)
	}
	if stored, ok := run.remote.File("host-a", "/sample.txt"); ok {
		t.Fatalf("denied remote write reached the Host: %q", stored)
	}
}

func TestWorkspaceServicesSampleWithoutGrantsGetsTypedDenials(t *testing.T) {
	run := startSample(t, false)
	report := runCommand(t, run, "selftest")
	requireReportLines(t, report,
		"files.preopens code=ok\n",
		"files.open code=denied permission=files.write\n",
		"files.write skipped=no-open\n",
		"hosts.list code=denied permission=hosts.read\n",
		"hosts.get.granted code=denied permission=hosts.read\n",
		"hosts.create code=denied permission=hosts.write\n",
		"sessions.list code=denied permission=sessions.read\n",
		"sessions.get.granted code=denied permission=sessions.read\n",
		"terminal.observe code=denied permission=terminal.observe\n",
		"terminal.read skipped=no-observe\n",
		"terminal.write code=denied permission=terminal.write\n",
		"terminal.info code=denied permission=terminal.info\n",
		"files.remote_list code=denied permission=files.remote.read\n",
		"files.remote_write code=denied permission=files.remote.write\n",
		"selftest.done\n",
	)
	if _, err := os.Stat(filepath.Join(run.files, "sample.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied sample wrote a file: %v", err)
	}
	if writes := run.terminal.Writes("session-1"); len(writes) != 0 {
		t.Fatalf("denied sample wrote terminal input: %q", writes)
	}
}
