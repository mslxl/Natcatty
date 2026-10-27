package app

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

// policyModuleRoot walks up from the test working directory to the module root,
// so go build can address ./cmd/natcatty-mcp.
func policyModuleRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod not found above the test working directory")
		}
		directory = parent
	}
}

// stdioMCPClient speaks newline-delimited MCP JSON-RPC to a real
// cmd/natcatty-mcp process. It separates send from receive because a
// confirm-mode tools/call blocks in the app until the approval is resolved.
type stdioMCPClient struct {
	stdin     io.WriteCloser
	lines     chan string
	readError chan error
	nextID    int
}

type stdioResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Result  map[string]any   `json:"result"`
	Error   *mcprpc.RPCError `json:"error"`
}

func newStdioMCPClient(stdin io.WriteCloser, stdout io.Reader) *stdioMCPClient {
	client := &stdioMCPClient{
		stdin:     stdin,
		lines:     make(chan string, 16),
		readError: make(chan error, 1),
	}
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				client.lines <- trimmed
			}
			if err != nil {
				client.readError <- err
				return
			}
		}
	}()
	return client
}

func (c *stdioMCPClient) send(method string, params map[string]any) int {
	c.nextID++
	body := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		body["params"] = params
	}
	payload, _ := json.Marshal(body)
	_, _ = c.stdin.Write(append(payload, '\n'))
	return c.nextID
}

func (c *stdioMCPClient) receive(t *testing.T) stdioResponse {
	t.Helper()
	timeout := time.After(30 * time.Second)
	select {
	case line := <-c.lines:
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
		t.Fatal("MCP response did not arrive")
	}
	return stdioResponse{}
}

func (c *stdioMCPClient) request(t *testing.T, method string, params map[string]any) stdioResponse {
	t.Helper()
	c.send(method, params)
	return c.receive(t)
}

// toolCallResult extracts the first text block and the isError flag.
func toolCallResult(t *testing.T, response stdioResponse) (string, bool) {
	t.Helper()
	content, ok := response.Result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tool result has no content: %#v", response.Result)
	}
	block, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("tool content block = %#v", content[0])
	}
	text, _ := block["text"].(string)
	isError, _ := response.Result["isError"].(bool)
	return text, isError
}

func awaitE2EPrompt(t *testing.T, policy *appmcp.Service, signals <-chan struct{}) appmcp.ApprovalView {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		if pending := policy.Snapshot().Pending; len(pending) > 0 {
			return pending[0]
		}
		select {
		case <-signals:
		case <-timeout:
			t.Fatal("the approval prompt did not appear")
			return appmcp.ApprovalView{}
		}
	}
}

func TestExternalMCPPolicyEndToEndThroughStdio(t *testing.T) {
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

	closer := &closingSessions{}
	sessions := policyTestSessions()
	vault := policyTestVault()
	bridge := newMCPReadBridge(vault, fakeForwarding{}, sessions, fakeTransfer{})
	discoveryPath := filepath.Join(t.TempDir(), "discovery.json")
	mode := newMCPModeState(discoveryPath)
	signals := make(chan struct{}, 16)
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: mcprpc.ExternalScope,
		Allowlist:           []string{"s1"},
		Mode:                mode.Get,
		Redraw: func() {
			select {
			case signals <- struct{}{}:
			default:
			}
		},
	})
	handler := newMCPPolicyHandler(bridge, policy, sessions, closer, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := startExternalMCP(ctx, bridge, discoveryPath, nil,
		withMCPHandler(handler), withMCPModeState(mode))
	if err != nil {
		t.Fatalf("start the policy listener: %v", err)
	}
	defer listener.Close()

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 120*time.Second)
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

	initialize := client.request(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "e2e-client", "version": "2.0"},
	})
	serverInfo, _ := initialize.Result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "netcatty-remote-hosts" || serverInfo["version"] != "1.0.0" {
		t.Fatalf("serverInfo = %#v", serverInfo)
	}

	// Scope enforcement through the real stdio client: get_environment lists
	// only the allowlisted live Session.
	environment := client.request(t, "tools/call", map[string]any{
		"name": "get_environment", "arguments": map[string]any{},
	})
	environmentText, environmentError := toolCallResult(t, environment)
	if environmentError {
		t.Fatalf("get_environment failed: %s", environmentText)
	}
	if !strings.Contains(environmentText, `"sessionId": "s1"`) ||
		strings.Contains(environmentText, `"sessionId": "s2"`) {
		t.Fatalf("get_environment leaked outside the allowlist: %s", environmentText)
	}

	// host_open in confirm mode blocks on a prompt that shows the real client
	// identity, tool, target, and redacted summary.
	hostOpenID := client.send("tools/call", map[string]any{
		"name":      "host_open",
		"arguments": map[string]any{"hostId": "h1", "newTab": "false"},
	})
	prompt := awaitE2EPrompt(t, policy, signals)
	if prompt.Client != "e2e-client/2.0" {
		t.Fatalf("prompt client = %q, want e2e-client/2.0", prompt.Client)
	}
	if prompt.Tool != "host_open" || !strings.Contains(prompt.Target, "web") ||
		prompt.Scope != mcprpc.ExternalScope {
		t.Fatalf("prompt = %#v", prompt)
	}
	if !strings.Contains(prompt.Summary, `hostId="h1"`) {
		t.Fatalf("prompt summary = %s", prompt.Summary)
	}
	if err := policy.Approve(prompt.ID); err != nil {
		t.Fatalf("approve host_open: %v", err)
	}
	hostOpen := client.receive(t)
	if hostOpen.ID != hostOpenID {
		t.Fatalf("host_open response id = %d, want %d", hostOpen.ID, hostOpenID)
	}
	hostOpenText, hostOpenError := toolCallResult(t, hostOpen)
	if hostOpenError || !strings.Contains(hostOpenText, `"status": "adopted"`) ||
		!strings.Contains(hostOpenText, `"sessionId": "s1"`) {
		t.Fatalf("host_open result: error=%v %s", hostOpenError, hostOpenText)
	}

	// session_close bypasses approval but still passes scope and ownership.
	closeResponse := client.request(t, "tools/call", map[string]any{
		"name": "session_close", "arguments": map[string]any{"sessionId": "s1"},
	})
	closeText, closeError := toolCallResult(t, closeResponse)
	if closeError || !strings.Contains(closeText, `"status": "closed"`) {
		t.Fatalf("session_close result: error=%v %s", closeError, closeText)
	}
	closer.mu.Lock()
	closed := append([]string(nil), closer.closed...)
	closer.mu.Unlock()
	if len(closed) != 1 || closed[0] != "s1" {
		t.Fatalf("session_close did not reach the Session service: %v", closed)
	}

	// A mode change is persisted to discovery and applies to the next call.
	if err := mode.Set(appmcp.ModeObserver); err != nil {
		t.Fatalf("set observer mode: %v", err)
	}
	discovery, err := mcprpc.ReadDiscovery(discoveryPath)
	if err != nil || discovery.PermissionMode != "observer" {
		t.Fatalf("discovery mode = %q (%v)", discovery.PermissionMode, err)
	}
	observerResponse := client.request(t, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s1", "command": "true",
		},
	})
	observerText, observerError := toolCallResult(t, observerResponse)
	if !observerError || !strings.Contains(observerText, "mcp_permission_denied") ||
		!strings.Contains(observerText, "terminal_execute") {
		t.Fatalf("observer mode result: error=%v %s", observerError, observerText)
	}

	if err := mode.Set(appmcp.ModeAuto); err != nil {
		t.Fatalf("set auto mode: %v", err)
	}
	autoResponse := client.request(t, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s1", "command": "true",
		},
	})
	autoText, autoError := toolCallResult(t, autoResponse)
	if !autoError || !strings.Contains(autoText, "write execution is not available") ||
		strings.Contains(autoText, "mcp_permission_denied") {
		t.Fatalf("auto mode result: error=%v %s", autoError, autoText)
	}
	if pending := policy.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("auto mode prompted: %#v", pending)
	}

	outOfScope := client.request(t, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s2", "command": "true",
		},
	})
	outOfScopeText, outOfScopeError := toolCallResult(t, outOfScope)
	if !outOfScopeError || !strings.Contains(outOfScopeText, "mcp_session_out_of_scope") ||
		!strings.Contains(outOfScopeText, `Session "s2"`) {
		t.Fatalf("out-of-scope result: error=%v %s", outOfScopeError, outOfScopeText)
	}

	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case <-waitDone:
		waited = true
	case <-time.After(30 * time.Second):
		t.Fatalf("natcatty-mcp did not exit after stdin closed: %s", stderr.String())
	}
}

// TestMCPPolicyDiscoveryModeRoundTrip pins that Get re-reads the discovery file
// rather than caching the mode.
func TestMCPPolicyDiscoveryModeRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discovery.json")
	state := newMCPModeState(path)
	if state.Get() != appmcp.ModeConfirm {
		t.Fatalf("default mode = %q", state.Get())
	}
	if err := state.Set(appmcp.ModeObserver); err != nil {
		t.Fatalf("set mode before a listener exists: %v", err)
	}
	if state.Get() != appmcp.ModeObserver {
		t.Fatalf("in-memory mode = %q", state.Get())
	}
	if _, err := mcprpc.WriteDiscovery(path, mcprpc.Discovery{
		Port: 4242, Token: "token", PermissionMode: "auto",
	}); err != nil {
		t.Fatalf("write discovery: %v", err)
	}
	if state.Get() != appmcp.ModeAuto {
		t.Fatalf("Get did not re-read discovery: %q", state.Get())
	}
	if err := state.Set(appmcp.ModeConfirm); err != nil {
		t.Fatalf("set mode with discovery: %v", err)
	}
	discovery, err := mcprpc.ReadDiscovery(path)
	if err != nil || discovery.PermissionMode != "confirm" || discovery.Port != 4242 || discovery.Token != "token" {
		t.Fatalf("discovery after Set = %#v (%v)", discovery, err)
	}
}
