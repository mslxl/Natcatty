package hostservices_test

import (
	"archive/zip"
	"context"
	"fmt"
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

const sampleID = "acme.host-services"

func sampleArchive(t *testing.T) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "host-services")
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
	archivePath := filepath.Join(t.TempDir(), sampleID+".natplug")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(file)
	// One fixed timestamp and sorted entry names satisfy the deterministic
	// archive rules the real validator enforces.
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

// commandRecorder is the host-side contribution registrar. It keeps the real
// command handlers the container registers so the test can invoke them the way
// the command palette would.
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

// feedbackSink collects the guest's visible output.
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

// logSink counts the log lines the host accepted after its rate limiter.
type logSink struct {
	mu    sync.Mutex
	lines int
}

func (l *logSink) add(appplugin.PluginRef, appplugin.LogLevel, string) {
	l.mu.Lock()
	l.lines++
	l.mu.Unlock()
}

func (l *logSink) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lines
}

type sampleRun struct {
	manager       *appplugin.Manager
	recorder      *commandRecorder
	feedback      *feedbackSink
	logs          *logSink
	clipboard     *appplugin.MemoryClipboard
	notifications *appplugin.MemoryNotificationSink
	store         *adapterplugininstall.Store
	record        domainplugin.InstallRecord
}

// startSample packages the committed sample as a .natplug and drives it
// through the real archive validator, install store, consent review, manager,
// container, wazero runtime, and host-service stack. grantOptional decides
// whether the optional permissions are consented to at enable time.
func startSample(t *testing.T, grantOptional bool) *sampleRun {
	t.Helper()
	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	recorder := newCommandRecorder()
	feedback := &feedbackSink{}
	logs := &logSink{}
	clipboard := appplugin.NewMemoryClipboard("initial clipboard")
	notifications := &appplugin.MemoryNotificationSink{}
	manager := appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		recorder,
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithClipboard(clipboard),
		appplugin.WithNotificationSink(notifications),
		appplugin.WithHostSettings(appplugin.MemoryHostSettings{Values: map[string]string{
			"app.locale":   "en",
			"app.version":  "0.1.0",
			"app.platform": "darwin/arm64",
		}}),
		appplugin.WithServiceDataDir(store.DataDir),
		appplugin.WithServiceLimits(appplugin.ServiceLimits{
			EventQueueDepth:  2,
			NotificationRate: 10,
		}),
		appplugin.WithManagerFeedback(func(ref appplugin.PluginRef, output string) {
			feedback.add(ref, output)
		}),
		appplugin.WithManagerLogf(func(ref appplugin.PluginRef, level appplugin.LogLevel,
			message string) {
			logs.add(ref, level, message)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, sampleArchive(t))
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
	if len(review.Required) != 1 || review.Required[0].Permission != domainplugin.PermissionCommands {
		t.Fatalf("required consent rows = %+v", review.Required)
	}
	scopes := make(map[domainplugin.Permission]domainplugin.Scope)
	for _, row := range review.Optional {
		scopes[row.Permission] = row.Scope
	}
	if len(scopes) != 6 {
		t.Fatalf("optional consent rows = %+v", review.Optional)
	}
	if scope := scopes[domainplugin.PermissionEventsSubscribe]; scope.Kind != domainplugin.ScopeEvents ||
		scope.Event != "notification.action" {
		t.Fatalf("events.subscribe consent scope = %+v", scope)
	}
	if scope := scopes[domainplugin.PermissionSettingsRead]; scope.Kind != domainplugin.ScopeSettings ||
		scope.Key != "app.locale" {
		t.Fatalf("settings.read consent scope = %+v", scope)
	}

	decisions := []domainplugin.PermissionDecision{{
		Permission: domainplugin.PermissionCommands, Allow: true,
	}}
	if grantOptional {
		for _, permission := range []domainplugin.Permission{
			domainplugin.PermissionStorage,
			domainplugin.PermissionClipboardRead,
			domainplugin.PermissionClipboardWrite,
			domainplugin.PermissionNotifications,
			domainplugin.PermissionEventsSubscribe,
			domainplugin.PermissionSettingsRead,
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
		manager:       manager,
		recorder:      recorder,
		feedback:      feedback,
		logs:          logs,
		clipboard:     clipboard,
		notifications: notifications,
		store:         store,
		record:        record,
	}
}

// runCommand invokes one command through the container and returns the single
// output the guest wrote for it.
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

func TestHostServicesSampleUsesGrantedServices(t *testing.T) {
	run := startSample(t, true)
	report := runCommand(t, run, "selftest")
	requireReportLines(t, report,
		"storage.set code=ok\n",
		"storage.get code=ok value=aGVsbG8=\n",
		"storage.get.match=ok\n",
		"storage.list code=ok\n",
		"storage.list.match=ok\n",
		"storage.delete code=ok\n",
		"clipboard.write code=ok\n",
		"clipboard.read code=ok text=host services sample\n",
		"clipboard.read.match=ok\n",
		"events.subscribe.actions code=ok handle=",
		"notifications.post.01 code=ok id=",
		"notifications.post.02 code=ok id=",
		"notifications.post.03 code=ok id=",
		"notifications.post.04 code=ok id=",
		"notifications.post.dismiss code=ok id=",
		"notifications.dismiss code=ok\n",
		"settings.read.locale code=ok value=en\n",
		// Settings read is key scoped and events subscribe is type scoped:
		// the granted permission alone does not authorize other keys/types.
		"settings.read.version code=denied permission=settings.read\n",
		"events.subscribe.terminal code=denied permission=events.subscribe\n",
		"logging calls=500\n",
		"selftest.done\n",
	)

	if text, err := run.clipboard.ReadText(context.Background()); err != nil ||
		text != "host services sample" {
		t.Fatalf("real clipboard = %q, %v", text, err)
	}
	posted := run.notifications.Notifications()
	if len(posted) != 5 {
		t.Fatalf("posted notifications = %+v, want five", posted)
	}
	for i := 0; i < 4; i++ {
		if posted[i].Title != fmt.Sprintf("Host services %d", i+1) ||
			len(posted[i].Actions) != 1 || posted[i].Actions[0].ID != "open" {
			t.Fatalf("actionable notification %d = %+v", i+1, posted[i])
		}
	}

	// A plugin's own settings schema is surfaced from the same installed
	// manifest, including defaults and the volatile field.
	view, err := run.manager.Settings(context.Background(), sampleID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	fields := make(map[string]appplugin.SettingsFieldView)
	for _, field := range view.Fields {
		fields[field.Key] = field
	}
	if !view.Valid || len(fields) != 3 {
		t.Fatalf("settings view = %+v", view)
	}
	if fields["greeting"].Value != "hello from plugin" || fields["verbose"].Value != false {
		t.Fatalf("settings defaults = %+v", fields)
	}
	if !fields["session_note"].Volatile || fields["session_note"].Value != "temporary" {
		t.Fatalf("volatile field = %+v", fields["session_note"])
	}

	// The guest logged a 500-line burst. The host rate limiter accepts a
	// bounded prefix and drops the rest instead of buffering without limit.
	fiber, ok := run.manager.Container().FiberByRef(appplugin.PluginRef{
		ID: sampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the enabled sample has no live fiber")
	}
	accepted := run.logs.count()
	if accepted == 0 || accepted > appplugin.MaxLogsPerSecond {
		t.Fatalf("accepted log lines = %d, want 1..%d", accepted, appplugin.MaxLogsPerSecond)
	}
	if fiber.DroppedLogs() == 0 {
		t.Fatal("the guest log burst was not bounded")
	}

	// Host notification actions flow back as targeted notification.action
	// events. Four actions into a depth-two queue force one gap event.
	for i := 0; i < 4; i++ {
		if err := run.manager.NotificationAction(posted[i].ID, "open"); err != nil {
			t.Fatalf("NotificationAction(%q): %v", posted[i].ID, err)
		}
	}
	polled := runCommand(t, run, "poll")
	requireReportLines(t, polled,
		"poll code=ok\n",
		`"type":"events.gap"`,
		`"dropped":2`,
	)
	if got := strings.Count(polled, `"action":"open"`); got != 2 {
		t.Fatalf("surviving action events = %d, want 2:\n%s", got, polled)
	}
}

func TestHostServicesSampleReportsTypedDenials(t *testing.T) {
	run := startSample(t, false)
	report := runCommand(t, run, "selftest")
	requireReportLines(t, report,
		"storage.set code=denied permission=storage\n",
		"storage.get code=denied permission=storage\n",
		"storage.get.match=no\n",
		"storage.list code=denied permission=storage\n",
		"storage.list.match=no\n",
		"storage.delete code=denied permission=storage\n",
		"clipboard.write code=denied permission=clipboard.write\n",
		"clipboard.read code=denied permission=clipboard.read\n",
		"clipboard.read.match=no\n",
		"events.subscribe.actions code=denied permission=events.subscribe\n",
		"notifications.post.01 code=denied permission=notifications\n",
		"notifications.post.02 code=denied permission=notifications\n",
		"notifications.post.03 code=denied permission=notifications\n",
		"notifications.post.04 code=denied permission=notifications\n",
		"notifications.post.dismiss code=denied permission=notifications\n",
		"notifications.dismiss skipped\n",
		"settings.read.locale code=denied permission=settings.read\n",
		"settings.read.version code=denied permission=settings.read\n",
		"events.subscribe.terminal code=denied permission=events.subscribe\n",
		"logging calls=500\n",
		"selftest.done\n",
	)

	// Nothing reached a host effect: no notification, no clipboard write, no
	// live event subscription, and the poll command has no handle to reuse.
	if got := run.notifications.Len(); got != 0 {
		t.Fatalf("posted notifications = %d, want 0", got)
	}
	if text, err := run.clipboard.ReadText(context.Background()); err != nil ||
		text != "initial clipboard" {
		t.Fatalf("clipboard after denials = %q, %v", text, err)
	}
	if got := run.manager.Services().SubscribeCount(); got != 0 {
		t.Fatalf("live subscriptions after denials = %d, want 0", got)
	}
	fiber, ok := run.manager.Container().FiberByRef(appplugin.PluginRef{
		ID: sampleID, Source: run.record.Source,
	})
	if !ok {
		t.Fatal("the enabled sample has no live fiber")
	}
	if fiber.DroppedLogs() == 0 {
		t.Fatal("the denied-mode guest log burst was not bounded")
	}
	polled := runCommand(t, run, "poll")
	requireReportLines(t, polled, "poll code=unsubscribed\n")
	if strings.Contains(polled, "poll.response=") {
		t.Fatalf("denied poll returned a response:\n%s", polled)
	}
}
