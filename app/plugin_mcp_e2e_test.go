package app

import (
	"archive/zip"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
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
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/testutil"
)

const mcpSampleID = "acme.mcp-tools"

// mcpSampleArchive packages the committed sample module exactly as a user
// would install it: the real validator, install store, manager, container, and
// wazero runtime handle it below.
func mcpSampleArchive(t *testing.T) string {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join(testutil.ModuleRoot(t),
		"sdk", "samples", "mcp-tools", "plugin.toml"))
	if err != nil {
		t.Fatalf("read sample manifest: %v", err)
	}
	return mcpSampleArchiveWithManifest(t, string(manifest))
}

// mcpSampleArchiveWithManifest packages the committed module with a manifest
// override, so a test can flip the permission class without rebuilding WASM.
func mcpSampleArchiveWithManifest(t *testing.T, manifest string) string {
	t.Helper()
	root := filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "mcp-tools")
	module, err := os.ReadFile(filepath.Join(root, "plugin.wasm"))
	if err != nil {
		t.Fatalf("read sample module: %v", err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, "i18n", "en.toml"))
	if err != nil {
		t.Fatalf("read sample catalog: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), mcpSampleID+".natplug")
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
		{"plugin.toml", []byte(manifest)},
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

// receivePluginMCP reads lines until the response with wantID arrives,
// collecting any notifications that were pushed first.
func receivePluginMCP(t *testing.T, client *stdioMCPClient,
	wantID int) (stdioResponse, []map[string]any) {
	t.Helper()
	timeout := time.After(60 * time.Second)
	notifications := make([]map[string]any, 0, 2)
	for {
		select {
		case line := <-client.lines:
			var envelope struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &envelope); err != nil {
				t.Fatalf("decode MCP line %q: %v", line, err)
			}
			if envelope.Method != "" {
				notifications = append(notifications, map[string]any{
					"method": envelope.Method,
					"params": envelope.Params,
				})
				continue
			}
			if envelope.ID != wantID {
				t.Fatalf("MCP response id = %d, want %d", envelope.ID, wantID)
			}
			var response stdioResponse
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				t.Fatalf("decode MCP response %q: %v", line, err)
			}
			if response.Error != nil {
				t.Fatalf("MCP protocol error: %+v", response.Error)
			}
			return response, notifications
		case err := <-client.readError:
			t.Fatalf("MCP stdout closed: %v", err)
		case <-timeout:
			t.Fatal("MCP response did not arrive")
		}
	}
}

// buildMCPProcessBinary builds the real cmd/natcatty-mcp executable the
// end-to-end tests launch as an MCP client would.
func buildMCPProcessBinary(t *testing.T) string {
	t.Helper()
	root := policyModuleRoot(t)
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelBuild()
	binaryName := "natcatty-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	build := exec.CommandContext(buildContext, "go", "build", "-o", binaryPath, "./cmd/natcatty-mcp")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/natcatty-mcp: %v\n%s", err, output)
	}
	return binaryPath
}

// awaitPluginProgress waits for the progress notification carrying token, which
// proves the guest is executing the tool call and the call is in flight.
func awaitPluginProgress(t *testing.T, client *stdioMCPClient, token string) {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case line := <-client.lines:
			var message struct {
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &message); err != nil {
				t.Fatalf("decode MCP line %q: %v", line, err)
			}
			if message.Method != mcprpc.NotificationProgress ||
				message.Params["progressToken"] != token {
				continue
			}
			if message.Params["progress"] != float64(1) {
				t.Fatalf("slow tool progress = %#v", message.Params)
			}
			return
		case err := <-client.readError:
			t.Fatalf("MCP stdout closed before the slow call reported progress: %v", err)
		case <-timeout:
			t.Fatal("the slow tool never reported progress")
		}
	}
}

// drainMCPLines returns the messages the client already received, including
// those buffered by a process that has just exited.
func drainMCPLines(client *stdioMCPClient) []string {
	lines := make([]string, 0, 4)
	// A closed stdout means every line the reader produced is already in the
	// lines buffer before the read error.
	select {
	case <-client.readError:
	case <-time.After(2 * time.Second):
	}
	for {
		select {
		case line := <-client.lines:
			lines = append(lines, line)
		default:
			return lines
		}
	}
}

func pluginToolNames(t *testing.T, response stdioResponse) map[string]bool {
	t.Helper()
	tools, ok := response.Result["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result = %#v", response.Result)
	}
	names := make(map[string]bool, len(tools))
	for _, raw := range tools {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		names[name] = true
	}
	return names
}

// TestPluginMCPToolsEndToEndThroughStdio composes the real archive validator,
// install store, consent-granted Manager, container, wazero runtime, the real
// app policy handler, the real loopback listener, the real natcatty-mcp stdio
// binary, and a real MCP JSON-RPC client. It proves tools/list and tools/call
// work for a granted read tool, progress is forwarded, a write tool fails the
// MCP mode gate in observer mode, a session target fails the scope gate, and
// revoking mcp.tools removes the tools.
func TestPluginMCPToolsEndToEndThroughStdio(t *testing.T) {
	root := policyModuleRoot(t)
	binaryPath := buildMCPProcessBinary(t)

	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	audit := &consentAudit{}
	var toolsChanged atomic.Int32
	var manager *appplugin.Manager
	var mcpListener *externalMCP
	tools := newMCPPluginTools(func() *appplugin.Manager { return manager })
	manager = appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		newPluginSurface(command.NewRegistry()),
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithAudit(audit),
		appplugin.WithMCPToolRegistrar(tools),
		appplugin.WithMCPToolsChanged(func() {
			toolsChanged.Add(1)
			mcpListener.NotifyAll(mcprpc.NotificationToolsListChanged, nil)
		}),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, mcpSampleArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if record.ID != mcpSampleID {
		t.Fatalf("installed id = %q", record.ID)
	}
	if err := manager.Enable(ctx, record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionMCPTools, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	detail, ok, err := manager.Detail(ctx, record.ID)
	if err != nil || !ok || !detail.Enabled || detail.State != domainplugin.StateActive {
		t.Fatalf("detail after enable = %+v, ok=%v err=%v", detail, ok, err)
	}
	if toolsChanged.Load() == 0 {
		t.Fatal("the tools-changed hook was not called on activation")
	}

	sessions := policyTestSessions()
	vault := policyTestVault()
	bridge := newMCPReadBridge(vault, fakeForwarding{}, sessions, fakeTransfer{})
	mode := newPolicyMode(appmcp.ModeAuto)
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: mcprpc.ExternalScope,
		Allowlist:           []string{"s1"},
		Mode:                mode.Get,
	})
	handler := newMCPPolicyHandler(bridge, policy, sessions, &closingSessions{}, nil)
	handler.attachPluginTools(tools)
	discoveryPath := filepath.Join(t.TempDir(), "discovery.json")
	listener, err := startExternalMCP(ctx, bridge, discoveryPath, nil, withMCPHandler(handler))
	if err != nil {
		t.Fatalf("start the policy listener: %v", err)
	}
	mcpListener = listener
	defer listener.Close()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelCommand()
	process := exec.CommandContext(commandContext, binaryPath)
	process.Dir = root
	process.Env = append(os.Environ(),
		mcprpc.EnvDiscoveryFile+"="+discoveryPath,
		mcprpc.EnvPort+"=",
		mcprpc.EnvToken+"=",
	)
	stdin, err := process.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var stderr strings.Builder
	process.Stderr = &stderr
	if err := process.Start(); err != nil {
		t.Fatalf("start natcatty-mcp: %v", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	}()
	client := newStdioMCPClient(stdin, stdout)

	initializeID := client.send("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "plugin-e2e", "version": "1.0"},
	})
	if _, notifications := receivePluginMCP(t, client, initializeID); len(notifications) != 0 {
		t.Fatalf("initialize produced notifications: %#v", notifications)
	}

	listID := client.send("tools/list", map[string]any{})
	listResponse, _ := receivePluginMCP(t, client, listID)
	names := pluginToolNames(t, listResponse)
	for _, want := range []string{
		"plugin." + mcpSampleID + ".echo",
		"plugin." + mcpSampleID + ".write_note",
		"plugin." + mcpSampleID + ".echo_session",
		"plugin." + mcpSampleID + ".fail",
		"get_environment",
	} {
		if !names[want] {
			t.Fatalf("tools/list is missing %q: %v", want, names)
		}
	}

	// Granted read tool through the real guest, with a progress token. The
	// guest reports progress, the app forwards it, and the stdio process emits
	// notifications/progress before the response.
	echoID := client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".echo",
		"arguments": map[string]any{"text": "hello"},
		"_meta":     map[string]any{"progressToken": "progress-1"},
	})
	echoResponse, notifications := receivePluginMCP(t, client, echoID)
	echoText, echoError := toolCallResult(t, echoResponse)
	if echoError || echoText != "echo: hello" {
		t.Fatalf("echo result: error=%v text=%q", echoError, echoText)
	}
	structured, _ := echoResponse.Result["structuredContent"].(map[string]any)
	if structured["tool"] != "echo" || structured["text"] != "hello" {
		t.Fatalf("echo structuredContent = %#v", structured)
	}
	progressSeen := false
	for _, notification := range notifications {
		if notification["method"] != mcprpc.NotificationProgress {
			continue
		}
		params, _ := notification["params"].(map[string]any)
		if params["progressToken"] != "progress-1" {
			t.Fatalf("progress token = %#v", params["progressToken"])
		}
		if params["progress"] != float64(50) {
			t.Fatalf("progress = %#v", params["progress"])
		}
		progressSeen = true
	}
	if !progressSeen {
		t.Fatalf("no progress notification was forwarded: %#v", notifications)
	}
	// The same activity snapshot the Settings surface uses for built-in
	// writes also carries the plugin tool outcome.
	activity := policy.Snapshot().LastActivity
	if activity == nil || activity.Tool != "plugin."+mcpSampleID+".echo" ||
		activity.Status != appmcp.ActivityOK {
		t.Fatalf("last activity = %+v", activity)
	}

	// The second gate: observer mode denies a write tool.
	mode.Set(appmcp.ModeObserver)
	writeID := client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".write_note",
		"arguments": map[string]any{},
	})
	writeResponse, _ := receivePluginMCP(t, client, writeID)
	writeText, writeError := toolCallResult(t, writeResponse)
	if !writeError || !strings.Contains(writeText, string(appmcp.CodePermissionDenied)) {
		t.Fatalf("observer write result: error=%v text=%q", writeError, writeText)
	}

	// The second gate: auto mode allows the same write tool without an
	// approval prompt.
	mode.Set(appmcp.ModeAuto)
	writeID = client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".write_note",
		"arguments": map[string]any{},
	})
	writeResponse, _ = receivePluginMCP(t, client, writeID)
	if text, isError := toolCallResult(t, writeResponse); isError || text != "wrote" {
		t.Fatalf("auto write result: error=%v text=%q", isError, text)
	}

	// The second gate: a session target outside the allowlist is rejected
	// before the guest runs.
	outOfScopeID := client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".echo_session",
		"arguments": map[string]any{"sessionId": "s2"},
	})
	outOfScope, _ := receivePluginMCP(t, client, outOfScopeID)
	outOfScopeText, outOfScopeError := toolCallResult(t, outOfScope)
	if !outOfScopeError || !strings.Contains(outOfScopeText, string(appmcp.CodeSessionOutOfScope)) {
		t.Fatalf("out-of-scope result: error=%v text=%q", outOfScopeError, outOfScopeText)
	}
	inScopeID := client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".echo_session",
		"arguments": map[string]any{"sessionId": "s1"},
	})
	inScope, _ := receivePluginMCP(t, client, inScopeID)
	if text, isError := toolCallResult(t, inScope); isError || text != "session ok" {
		t.Fatalf("in-scope session result: error=%v text=%q", isError, text)
	}

	// Guest-reported typed error.
	failID := client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".fail",
		"arguments": map[string]any{},
	})
	failResponse, _ := receivePluginMCP(t, client, failID)
	failText, failError := toolCallResult(t, failResponse)
	if !failError || !strings.Contains(failText, "guest tool failed") {
		t.Fatalf("guest failure result: error=%v text=%q", failError, failText)
	}
	// A guest tool error keeps its content and structured content.
	failStructured, _ := failResponse.Result["structuredContent"].(map[string]any)
	if failStructured["failed"] != true {
		t.Fatalf("guest failure structuredContent = %#v", failResponse.Result["structuredContent"])
	}

	// Audit every outcome without recording arguments or result text.
	audit.mu.Lock()
	mcpEvents := make([]domainplugin.AuditEvent, 0)
	for _, event := range audit.events {
		if event.Event == domainplugin.AuditMCPTool {
			mcpEvents = append(mcpEvents, event)
		}
	}
	audit.mu.Unlock()
	if len(mcpEvents) < 4 {
		t.Fatalf("MCP tool audit events = %d, want at least 4", len(mcpEvents))
	}
	sawGuestFailure := false
	for _, event := range mcpEvents {
		if event.Tool == "" || event.PluginID != mcpSampleID {
			t.Fatalf("audit event = %+v", event)
		}
		if strings.Contains(event.Tool, "hello") || strings.Contains(event.Reason, "hello") {
			t.Fatalf("audit event leaked an argument: %+v", event)
		}
		// The audit contract stores identifiers, never user-visible labels.
		if strings.Contains(event.Target, "web shell") ||
			strings.Contains(event.Target, "web (") {
			t.Fatalf("audit event leaked a target label: %+v", event)
		}
		if strings.HasSuffix(event.Tool, ".echo_session") &&
			event.Target != "s1" && event.Target != "s2" {
			t.Fatalf("session audit target = %q, want a session identifier", event.Target)
		}
		if strings.HasSuffix(event.Tool, ".fail") && event.Status == "error" {
			sawGuestFailure = true
		}
	}
	if !sawGuestFailure {
		t.Fatalf("the guest failure was not audited: %+v", mcpEvents)
	}

	// First gate: revoking the required mcp.tools permission unloads the
	// plugin; the tools disappear and a call fails typed.
	if err := manager.Revoke(ctx, record.ID, domainplugin.PermissionMCPTools); err != nil {
		t.Fatalf("Revoke mcp.tools: %v", err)
	}
	listID = client.send("tools/list", map[string]any{})
	listResponse, changeNotifications := receivePluginMCP(t, client, listID)
	names = pluginToolNames(t, listResponse)
	if names["plugin."+mcpSampleID+".echo"] {
		t.Fatal("a revoked plugin still lists its MCP tool")
	}
	changeSeen := false
	for _, notification := range changeNotifications {
		if notification["method"] == mcprpc.NotificationToolsListChanged {
			changeSeen = true
		}
	}
	if !changeSeen {
		t.Fatalf("revoking mcp.tools did not push tools/list_changed: %#v", changeNotifications)
	}
	revokedID := client.send("tools/call", map[string]any{
		"name":      "plugin." + mcpSampleID + ".echo",
		"arguments": map[string]any{"text": "after revoke"},
	})
	revoked, _ := receivePluginMCP(t, client, revokedID)
	revokedText, revokedError := toolCallResult(t, revoked)
	if !revokedError ||
		(!strings.Contains(revokedText, string(appmcp.CodePluginToolUnavailable)) &&
			!strings.Contains(revokedText, string(appmcp.CodePluginToolReloading))) {
		t.Fatalf("revoked call result: error=%v text=%q", revokedError, revokedText)
	}

	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- process.Wait() }()
	select {
	case <-waitDone:
		waited = true
	case <-time.After(30 * time.Second):
		t.Fatalf("natcatty-mcp did not exit after stdin closed: %s", stderr.String())
	}
}

// TestPluginMCPToolsCancelOnMidCallDisconnect is the disconnect regression: the
// real natcatty-mcp process calls the sample's deliberately blocking tool, the
// client closes stdin while the call is still running (the guest already
// reported progress), and the process must cancel the call through the app and
// wasm stack and exit promptly instead of waiting for the tool deadline.
func TestPluginMCPToolsCancelOnMidCallDisconnect(t *testing.T) {
	root := policyModuleRoot(t)
	binaryPath := buildMCPProcessBinary(t)

	ctx := context.Background()
	store := adapterplugininstall.Open(t.TempDir())
	audit := &consentAudit{}
	var manager *appplugin.Manager
	tools := newMCPPluginTools(func() *appplugin.Manager { return manager })
	manager = appplugin.NewManager(
		adapterwasm.NewRuntime(),
		adapterpluginmanifest.Source{},
		newPluginSurface(command.NewRegistry()),
		adapterpluginpackage.New(adapterwasm.Validator{}),
		store,
		appplugin.WithAudit(audit),
		appplugin.WithMCPToolRegistrar(tools),
	)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })

	record, err := manager.InstallFile(ctx, mcpSampleArchive(t))
	if err != nil {
		t.Fatalf("InstallFile: %v", err)
	}
	if err := manager.Enable(ctx, record.ID, []domainplugin.PermissionDecision{
		{Permission: domainplugin.PermissionMCPTools, Allow: true},
	}); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	sessions := policyTestSessions()
	vault := policyTestVault()
	bridge := newMCPReadBridge(vault, fakeForwarding{}, sessions, fakeTransfer{})
	mode := newPolicyMode(appmcp.ModeAuto)
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: mcprpc.ExternalScope,
		Allowlist:           []string{"s1"},
		Mode:                mode.Get,
	})
	handler := newMCPPolicyHandler(bridge, policy, sessions, &closingSessions{}, nil)
	handler.attachPluginTools(tools)
	discoveryPath := filepath.Join(t.TempDir(), "discovery.json")
	listener, err := startExternalMCP(ctx, bridge, discoveryPath, nil, withMCPHandler(handler))
	if err != nil {
		t.Fatalf("start the policy listener: %v", err)
	}
	defer listener.Close()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelCommand()
	process := exec.CommandContext(commandContext, binaryPath)
	process.Dir = root
	process.Env = append(os.Environ(),
		mcprpc.EnvDiscoveryFile+"="+discoveryPath,
		mcprpc.EnvPort+"=",
		mcprpc.EnvToken+"=",
	)
	stdin, err := process.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := process.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var stderr strings.Builder
	process.Stderr = &stderr
	if err := process.Start(); err != nil {
		t.Fatalf("start natcatty-mcp: %v", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	}()
	client := newStdioMCPClient(stdin, stdout)

	initializeID := client.send("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "plugin-e2e", "version": "1.0"},
	})
	if _, notifications := receivePluginMCP(t, client, initializeID); len(notifications) != 0 {
		t.Fatalf("initialize produced notifications: %#v", notifications)
	}

	slowName := "plugin." + mcpSampleID + ".slow"
	slowID := client.send("tools/call", map[string]any{
		"name":      slowName,
		"arguments": map[string]any{},
		"_meta":     map[string]any{"progressToken": "slow-progress"},
	})
	// The guest reports progress from inside the call before it blocks, so the
	// notification proves the call has started and is still in flight.
	awaitPluginProgress(t, client, "slow-progress")

	// Queue one more request behind the in-flight call and then disconnect.
	// The stdin pump must keep reading past the queued request and observe
	// EOF; if it blocks on the handoff, the process waits for the tool
	// deadline instead of exiting.
	client.send("ping", map[string]any{})

	// Close the client end of stdin (the standard graceful disconnect) while
	// the tool call is outstanding.
	if err := stdin.Close(); err != nil {
		t.Fatalf("close stdin mid-call: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- process.Wait() }()
	select {
	case <-waitDone:
		waited = true
	case <-time.After(10 * time.Second):
		t.Fatalf("natcatty-mcp did not exit within 10s of a mid-call stdin close: %s",
			stderr.String())
	}

	// The app-side disconnect path cancels the wasm call; the audit row is the
	// durable, payload-free record of that cancellation.
	cancelled := false
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		audit.mu.Lock()
		for _, event := range audit.events {
			if event.Event != domainplugin.AuditMCPTool || event.Tool != slowName {
				continue
			}
			if event.Status == "ok" {
				audit.mu.Unlock()
				t.Fatalf("the disconnected call was audited as a success: %+v", event)
			}
			if event.Status == string(appmcp.CodePluginToolCancelled) {
				cancelled = true
			}
		}
		audit.mu.Unlock()
		if cancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled {
		t.Fatalf("the in-flight call was not audited as %q", appmcp.CodePluginToolCancelled)
	}

	activity := policy.Snapshot().LastActivity
	if activity == nil || activity.Tool != slowName ||
		activity.Status != appmcp.ActivityTimeout ||
		activity.Rule != string(appmcp.CodePluginToolCancelled) {
		t.Fatalf("last activity after the disconnect = %+v", activity)
	}

	// Nothing successful may reach the MCP client for the cancelled call.
	for _, line := range drainMCPLines(client) {
		var message struct {
			ID     int            `json:"id"`
			Result map[string]any `json:"result"`
			Error  map[string]any `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &message); err != nil || message.ID != slowID {
			continue
		}
		if message.Error != nil {
			continue
		}
		if isError, _ := message.Result["isError"].(bool); !isError {
			t.Fatalf("a successful result was delivered for the cancelled call: %s", line)
		}
	}
}
