package app

// Ticket 58's real-client evidence. The test builds cmd/natcatty-mcp, starts
// the production authenticated loopback listener on the real write graph, and
// drives every executable catalog write through the stdio MCP process. Script
// tools return the typed unavailable result, a blocked command never reaches
// the shell, and a confirm-mode write is approved with its nested credential
// redacted from the prompt.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

// receiveID reads until the response with the matching JSON-RPC id, skipping
// the loader's tools/list_changed notification (which has no id).
func (c *stdioMCPClient) receiveID(t *testing.T, id int) stdioResponse {
	t.Helper()
	// The real-PTY tests deliberately set short per-call tool deadlines to
	// prove the typed timeout, but a loaded parallel suite can also make a
	// legitimate megabyte of terminal output take far longer than the
	// production 60 s call bound. The client budget is a test deadline, not
	// the tool deadline, so it stays well above the slowest real-transport
	// fixture.
	timeout := time.After(210 * time.Second)
	for {
		select {
		case line := <-c.lines:
			var envelope struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal([]byte(line), &envelope); err != nil {
				t.Fatalf("decode MCP line %q: %v", line, err)
			}
			if envelope.Method != "" || envelope.ID == nil {
				continue
			}
			if *envelope.ID != id {
				t.Fatalf("MCP response id = %d, want %d", *envelope.ID, id)
			}
			var response stdioResponse
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				t.Fatalf("decode MCP response %q: %v", line, err)
			}
			if response.Error != nil {
				t.Fatalf("MCP protocol error: %+v", response.Error)
			}
			return response
		case err := <-c.readError:
			t.Fatalf("MCP stdout closed: %v", err)
		case <-timeout:
			t.Fatalf("MCP response %d did not arrive", id)
		}
	}
}

type e2eToolResult struct {
	result  map[string]any
	text    string
	isError bool
}

func e2eToolCall(t *testing.T, client *stdioMCPClient, name string,
	arguments map[string]any) e2eToolResult {
	t.Helper()
	id := client.send("tools/call", map[string]any{"name": name, "arguments": arguments})
	response := client.receiveID(t, id)
	text, isError := toolCallResult(t, response)
	echo := e2eToolResult{text: text, isError: isError}
	if !isError {
		if err := json.Unmarshal([]byte(text), &echo.result); err != nil {
			t.Fatalf("%s result is not JSON: %v\n%s", name, err, text)
		}
	}
	return echo
}

func e2eToolOK(t *testing.T, client *stdioMCPClient, name string,
	arguments map[string]any) map[string]any {
	t.Helper()
	call := e2eToolCall(t, client, name, arguments)
	if call.isError {
		t.Fatalf("%s failed: %s", name, call.text)
	}
	if call.result["ok"] != true {
		t.Fatalf("%s result = %#v", name, call.result)
	}
	return call.result
}

func e2eToolErrorContains(t *testing.T, client *stdioMCPClient, name string,
	arguments map[string]any, fragments ...string) string {
	t.Helper()
	call := e2eToolCall(t, client, name, arguments)
	if !call.isError {
		t.Fatalf("%s unexpectedly succeeded: %s", name, call.text)
	}
	for _, fragment := range fragments {
		if !strings.Contains(call.text, fragment) {
			t.Fatalf("%s error = %q, want %q", name, call.text, fragment)
		}
	}
	return call.text
}

func e2eField(t *testing.T, result map[string]any, key string) string {
	t.Helper()
	value := strings.TrimSpace(fmt.Sprint(result[key]))
	if value == "" || value == "<nil>" {
		t.Fatalf("result has no %s: %#v", key, result)
	}
	return value
}

func e2eNestedID(t *testing.T, result map[string]any, listKey, idKey string) string {
	t.Helper()
	entries, _ := result[listKey].([]any)
	if len(entries) == 0 {
		t.Fatalf("result has no %s: %#v", listKey, result)
	}
	entry, _ := entries[0].(map[string]any)
	return e2eField(t, entry, idKey)
}

// TestMCPWriteEveryExecutableToolThroughARealStdioClient is the end-to-end
// acceptance path for ticket 58. It uses fake SSH/SFTP transports and the real
// local shell, but every call crosses the compiled stdio process and the
// authenticated loopback listener.
func TestMCPWriteEveryExecutableToolThroughARealStdioClient(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	fixture.setMode(appmcp.ModeAuto)

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

	discoveryPath := filepath.Join(t.TempDir(), "discovery.json")
	listenerContext, cancelListener := context.WithCancel(context.Background())
	defer cancelListener()
	listener, err := startExternalMCP(listenerContext, fixture.bridge, discoveryPath, nil,
		withMCPHandler(fixture.handler))
	if err != nil {
		t.Fatalf("start the external MCP listener: %v", err)
	}
	defer listener.Close()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, binaryPath)
	command.Dir = root
	command.Env = append(os.Environ(),
		mcprpc.EnvDiscoveryFile+"="+discoveryPath,
		mcprpc.EnvPort+"=",
		mcprpc.EnvToken+"=",
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start natcatty-mcp: %v", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	}()
	client := newStdioMCPClient(stdin, stdout)
	defer func() {
		_ = stdin.Close()
		waitDone := make(chan error, 1)
		go func() { waitDone <- command.Wait() }()
		select {
		case <-waitDone:
			waited = true
		case <-time.After(30 * time.Second):
			t.Errorf("natcatty-mcp did not exit after stdin closed: %s", stderr.String())
		}
	}()

	initialize := client.request(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ticket58-writes", "version": "1.0"},
	})
	serverInfo, _ := initialize.Result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "netcatty-remote-hosts" {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}

	// The initial progressive surface is the original core plus the loader;
	// loading all exposes the original 69 catalog tools and adds no
	// secret-store or AI capability.
	initial := client.request(t, "tools/list", nil)
	initialTools, _ := initial.Result["tools"].([]any)
	if len(initialTools) != len(catalog.CoreNames)+1 {
		t.Fatalf("initial tools = %d, want %d", len(initialTools), len(catalog.CoreNames)+1)
	}
	loaderID := client.send("tools/call", map[string]any{
		"name": "load_netcatty_tools", "arguments": map[string]any{"toolset": "all"},
	})
	loaderResponse := client.receiveID(t, loaderID)
	if _, isError := toolCallResult(t, loaderResponse); isError {
		t.Fatalf("load all tools failed: %#v", loaderResponse.Result)
	}
	loaded := client.request(t, "tools/list", nil)
	allTools, _ := loaded.Result["tools"].([]any)
	if len(allTools) != 70 {
		t.Fatalf("loaded tools = %d, want 69 catalog tools plus the loader", len(allTools))
	}
	// "credentials" appears in the original Vault descriptions as ordinary
	// prose, so names carry the secret-capability rule while descriptions only
	// have to be free of AI and explicit secret-manager capability names.
	nameForbidden := []string{"secret", "credential", "keychain", "password-manager",
		"ai_", "assistant", "llm", "chat.completion", "conversation"}
	descriptionForbidden := []string{"secret-manager", "password-manager", "keychain",
		"ai_", "assistant", "llm", "chat.completion", "conversation"}
	for _, raw := range allTools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		description := strings.ToLower(fmt.Sprint(tool["description"]))
		lowerName := strings.ToLower(name)
		for _, term := range nameForbidden {
			if strings.Contains(lowerName, term) {
				t.Fatalf("stdio tools/list exposed a forbidden secret/AI tool: %s", name)
			}
		}
		for _, term := range descriptionForbidden {
			if strings.Contains(description, term) {
				t.Fatalf("stdio tools/list exposed a forbidden secret/AI capability: %s", name)
			}
		}
	}

	sessionID := string(fixture.localSession)
	sshSessionID := string(fixture.sshSession)
	remote := fixture.remote

	// Vault Host create, update, notes, import, and later delete.
	created := e2eToolOK(t, client, "vault_hosts_create", map[string]any{
		"hosts": `[{"hostname":"stdio.example","label":"stdio","username":"ops"}]`,
	})
	hostID := e2eNestedID(t, created, "created", "id")
	e2eToolOK(t, client, "vault_hosts_update", map[string]any{
		"hostId": hostID, "label": "stdio-renamed", "notes": "stdio details",
	})
	e2eToolOK(t, client, "host_notes_set", map[string]any{
		"hostId": hostID, "notes": "stdio notes",
	})
	imported := e2eToolOK(t, client, "vault_hosts_import", map[string]any{
		"format": "csv", "text": "hostname,label\nstdio-import.example,stdio-import\n",
	})
	if imported["imported"] == nil {
		t.Fatalf("vault_hosts_import result = %#v", imported)
	}

	// Vault Note create, update, import, delete.
	note := e2eToolOK(t, client, "vault_notes_create", map[string]any{
		"title": "stdio note", "content": "# stdio\nbody",
	})
	noteID := e2eField(t, note, "id")
	e2eToolOK(t, client, "vault_notes_update", map[string]any{
		"noteId": noteID, "content": "changed through stdio",
	})
	e2eToolOK(t, client, "vault_notes_import", map[string]any{
		"fileName": "stdio-import.md", "content": "imported through stdio",
	})
	e2eToolOK(t, client, "vault_notes_delete", map[string]any{"noteId": noteID})

	// Vault Group create, update, delete.
	e2eToolOK(t, client, "vault_groups_create", map[string]any{
		"path": "stdio/infra", "defaults": `{"username":"stdio-group"}`,
	})
	e2eToolOK(t, client, "vault_groups_update", map[string]any{
		"path": "stdio/infra", "newPath": "stdio/prod", "defaults": `{"username":"stdio-prod"}`,
	})
	e2eToolOK(t, client, "vault_groups_delete", map[string]any{
		"path": "stdio/prod", "deleteHosts": "false",
	})

	// Snippets create, update, run, delete.
	snippet := e2eToolOK(t, client, "snippets_create", map[string]any{
		"label": "stdio snippet", "content": "echo ncmcp-stdio-snippet",
	})
	snippetID := e2eField(t, snippet, "id")
	e2eToolOK(t, client, "snippets_update", map[string]any{
		"snippetId": snippetID, "label": "stdio snippet renamed", "noAutoRun": "true",
	})
	e2eToolOK(t, client, "snippets_run", map[string]any{
		"snippetId": snippetID, "sessionId": sessionID,
	})
	e2eToolOK(t, client, "snippets_delete", map[string]any{"snippetId": snippetID})

	// Port-forwarding rules: create, update, duplicate, start, stop, delete.
	rule := e2eToolOK(t, client, "portforward_rules_create", map[string]any{
		"label": "stdio rule", "type": "local", "localPort": float64(28090),
		"bindAddress": "127.0.0.1", "remoteHost": "db.internal",
		"remotePort": float64(5432), "hostId": string(fixture.host.ID),
	})
	ruleID := e2eField(t, rule, "ruleId")
	e2eToolOK(t, client, "portforward_rules_update", map[string]any{
		"ruleId": ruleID, "label": "stdio rule updated", "localPort": float64(28091),
	})
	duplicated := e2eToolOK(t, client, "portforward_rules_duplicate", map[string]any{
		"ruleId": ruleID,
	})
	duplicateID := e2eField(t, duplicated, "ruleId")
	e2eToolOK(t, client, "portforward_start", map[string]any{"ruleId": duplicateID})
	e2eToolOK(t, client, "portforward_stop", map[string]any{"ruleId": duplicateID})
	e2eToolOK(t, client, "portforward_rules_delete", map[string]any{"ruleId": duplicateID})
	e2eToolOK(t, client, "portforward_rules_delete", map[string]any{"ruleId": ruleID})

	// Core SFTP write tools and the advanced toolset.
	remote.files["/remote/stdio-read.txt"] = []byte("stdio download")
	fixture.local.files["/local/stdio-upload.txt"] = []byte("stdio upload")
	e2eToolOK(t, client, "sftp_write_file", map[string]any{
		"sessionId": sshSessionID, "path": "/remote/stdio-written.txt", "content": "stdio written",
	})
	if got := string(remote.files["/remote/stdio-written.txt"]); got != "stdio written" {
		t.Fatalf("sftp_write_file content = %q", got)
	}
	e2eToolOK(t, client, "sftp_mkdir", map[string]any{
		"sessionId": sshSessionID, "path": "/remote/stdio-dir",
	})
	if !remote.dirs["/remote/stdio-dir"] {
		t.Fatal("sftp_mkdir did not create the directory")
	}
	e2eToolOK(t, client, "sftp_rename", map[string]any{
		"sessionId": sshSessionID, "oldPath": "/remote/stdio-written.txt",
		"newPath": "/remote/stdio-renamed.txt",
	})
	e2eToolOK(t, client, "sftp_chmod", map[string]any{
		"sessionId": sshSessionID, "path": "/remote/stdio-renamed.txt", "mode": "755",
	})
	if remote.modes["/remote/stdio-renamed.txt"] != 0o755 {
		t.Fatalf("sftp_chmod mode = %o", remote.modes["/remote/stdio-renamed.txt"])
	}
	e2eToolOK(t, client, "sftp_delete", map[string]any{
		"sessionId": sshSessionID, "path": "/remote/stdio-renamed.txt",
	})
	e2eToolOK(t, client, "sftp_download", map[string]any{
		"sessionId": sshSessionID, "remotePath": "/remote/stdio-read.txt",
		"localPath": "/local/stdio-downloaded.txt",
	})
	if got := string(fixture.local.files["/local/stdio-downloaded.txt"]); got != "stdio download" {
		t.Fatalf("sftp_download content = %q", got)
	}
	e2eToolOK(t, client, "sftp_upload", map[string]any{
		"sessionId": sshSessionID, "localPath": "/local/stdio-upload.txt",
		"remotePath": "/remote/stdio-uploaded.txt",
	})
	if got := string(remote.files["/remote/stdio-uploaded.txt"]); got != "stdio upload" {
		t.Fatalf("sftp_upload content = %q", got)
	}

	// Foreground command, background start/poll/stop, and a blocked command
	// that never reaches the shell.
	executed := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "echo ncmcp-stdio-execute",
	})
	if stdout, _ := executed["stdout"].(string); !strings.Contains(stdout, "ncmcp-stdio-execute") {
		t.Fatalf("terminal_execute stdout = %q", stdout)
	}
	blocked := e2eToolErrorContains(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID,
		"command":   "echo ok; rm -rf /tmp/natcatty-ticket58-stdio",
	}, "mcp_command_blocked", "common.rm-recursive-force")
	if !strings.Contains(blocked, "common.rm-recursive-force") {
		t.Fatalf("blocked error did not name the rule: %s", blocked)
	}
	afterBlock := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "echo after-stdio-block",
	})
	if stdout, _ := afterBlock["stdout"].(string); !strings.Contains(stdout, "after-stdio-block") {
		t.Fatalf("shell state after a block = %q", stdout)
	}

	started := e2eToolOK(t, client, "terminal_start", map[string]any{
		"sessionId": sessionID, "command": "echo ncmcp-stdio-job",
	})
	jobID := e2eField(t, started, "jobId")
	var polled map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		polled = e2eToolOK(t, client, "terminal_poll", map[string]any{
			"jobId": jobID, "offset": float64(0),
		})
		if polled["completed"] == true {
			break
		}
		select {
		case <-time.After(20 * time.Millisecond):
		}
	}
	if polled["completed"] != true {
		t.Fatalf("background job did not complete: %#v", polled)
	}
	e2eToolOK(t, client, "terminal_stop", map[string]any{"jobId": jobID})

	// Host open, then Host delete. newTab grants this client ownership of the
	// returned Session; a reuse below would be visibility-only.
	opened := e2eToolOK(t, client, "host_open", map[string]any{
		"hostId": hostID, "newTab": "true",
	})
	if opened["status"] != "opened" || opened["sessionId"] != sshSessionID {
		t.Fatalf("host_open result = %#v", opened)
	}
	e2eToolOK(t, client, "vault_hosts_delete", map[string]any{"hostId": hostID})

	// The nine script tools are explicitly unavailable, never the old generic
	// fallback, and are never advertised as executable by the app.
	unavailable := map[string]map[string]any{
		"scripts_create":           {"label": "x", "content": "x"},
		"scripts_update":           {"scriptId": "x", "content": "x"},
		"scripts_delete":           {"scriptId": "x"},
		"scripts_run":              {"scriptId": "x"},
		"scripts_run_stop":         {"runId": "x"},
		"scripts_run_pause":        {"runId": "x"},
		"scripts_run_resume":       {"runId": "x"},
		"scripts_targets_set":      {"scriptId": "x", "targets": "[]"},
		"host_connect_scripts_set": {"hostId": hostID, "scriptIds": "[]"},
	}
	for name, arguments := range unavailable {
		e2eToolErrorContains(t, client, name, arguments,
			"mcp_tool_unavailable", "not available in this build")
	}

	// Confirm mode through the real stdio client: the prompt must show the
	// nested credential as redacted, and approval executes the write.
	fixture.setMode(appmcp.ModeConfirm)
	const password = "{{SECRET_stdio_ticket58}}"
	callID := client.send("tools/call", map[string]any{
		"name": "vault_hosts_create",
		"arguments": map[string]any{
			"hosts": fmt.Sprintf(`[{"hostname":"stdio-secret.example","password":%q}]`, password),
		},
	})
	prompt := awaitMCPApproval(t, fixture)
	if prompt.Tool != "vault_hosts_create" || prompt.Client != "ticket58-writes/1.0" {
		t.Fatalf("stdio prompt = %#v", prompt)
	}
	if strings.Contains(prompt.Summary, password) || !strings.Contains(prompt.Summary, "[redacted]") {
		t.Fatalf("stdio approval summary leaked the nested credential: %s", prompt.Summary)
	}
	if err := fixture.policy.Approve(prompt.ID); err != nil {
		t.Fatalf("approve the stdio write: %v", err)
	}
	approvedResponse := client.receiveID(t, callID)
	approvedText, approvedError := toolCallResult(t, approvedResponse)
	if approvedError {
		t.Fatalf("approved write failed: %s", approvedText)
	}
	if strings.Contains(approvedText, password) {
		t.Fatalf("approved write result leaked the credential: %s", approvedText)
	}

	// session_close is the second exact approval bypass. The earlier newTab
	// host_open granted this Session in the current generation, so the close
	// must reach the real Session service and release the grant.
	closed := e2eToolOK(t, client, "session_close", map[string]any{
		"sessionId": sshSessionID,
	})
	if closed["status"] != "closed" || closed["sessionId"] != sshSessionID {
		t.Fatalf("session_close result = %#v", closed)
	}
	fixture.setMode(appmcp.ModeAuto)
}

// TestMCPWriteTimeoutThroughARealStdioClient proves the per-call deadline is
// visible to a real client as the typed code and releases the Session for the
// next call. It uses the real local shell, not a fake.
func TestMCPWriteTimeoutThroughARealStdioClient(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	fixture.setMode(appmcp.ModeAuto)
	shell, _ := fixture.sessions.ShellKind(fixture.localSession)
	var slow string
	switch shell {
	case "powershell":
		slow = "Start-Sleep -Seconds 2"
	case "cmd":
		slow = "timeout /t 2 /nobreak >nul"
	default:
		slow = "sleep 2"
	}
	fixture.executor.toolTimeout = 500 * time.Millisecond

	root := policyModuleRoot(t)
	binaryName := "natcatty-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binaryPath, "./cmd/natcatty-mcp")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/natcatty-mcp: %v\n%s", err, output)
	}

	discoveryPath := filepath.Join(t.TempDir(), "discovery.json")
	listenerContext, cancelListener := context.WithCancel(context.Background())
	defer cancelListener()
	listener, err := startExternalMCP(listenerContext, fixture.bridge, discoveryPath, nil,
		withMCPHandler(fixture.handler))
	if err != nil {
		t.Fatalf("start the external MCP listener: %v", err)
	}
	defer listener.Close()
	commandContext, cancelCommand := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, binaryPath)
	command.Dir = root
	command.Env = append(os.Environ(),
		mcprpc.EnvDiscoveryFile+"="+discoveryPath,
		mcprpc.EnvPort+"=", mcprpc.EnvToken+"=")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout: %v", err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start natcatty-mcp: %v", err)
	}
	client := newStdioMCPClient(stdin, stdout)
	_ = client.request(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "ticket58-timeout", "version": "1.0"},
	})
	timedOut := e2eToolErrorContains(t, client, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   shellCommandFor(shell, "before-stdio-timeout", slow),
	}, "mcp_command_timeout", "timed out")
	_ = timedOut
	fixture.executor.toolTimeout = 30 * time.Second
	followUp := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession), "command": "echo after-stdio-timeout",
	})
	if stdout, _ := followUp["stdout"].(string); !strings.Contains(stdout, "after-stdio-timeout") {
		t.Fatalf("follow-up stdout = %q", stdout)
	}
	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(30 * time.Second):
		_ = command.Process.Kill()
		t.Fatalf("natcatty-mcp did not exit: %s", stderr.String())
	}
}
