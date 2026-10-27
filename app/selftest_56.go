//go:build selftest

package app

// Ticket 56 self-test. Press F11, then 5, then 6.
//
// The check builds the real cmd/natcatty-mcp binary, launches it as an external
// client, and drives the MCP protocol over stdio:
//
//   - initialize identifies netcatty-remote-hosts 1.0.0;
//   - tools/list starts with 14 core tools plus the loader;
//   - load_netcatty_tools("sftp_advanced") changes visibility to 21 and
//     emits notifications/tools/list_changed;
//   - load_netcatty_tools("all") exposes all 69 catalog tools plus the loader;
//   - get_environment, vault_hosts_list, and vault_notes_list return successful
//     tool results, and a write tool returns the read-only error.
//
// The listener is the app's production external MCP surface when run() started
// it. Unit tests build the window graph directly, so the check falls back to a
// dedicated listener with the same bridge and empty readers.
//
// No step sleeps: readiness is a JSON-RPC response, and every wait has a hard
// deadline. Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-56.json.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest56Ticket = 56
	// selftest56Timeout bounds the build, launch, and full protocol exchange.
	selftest56Timeout = 180 * time.Second
)

type selftest56 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest56Result
	report   selftest56Report
	err      error
}

type selftest56Report struct {
	InitialTools  int
	AfterToolset  int
	AfterAll      int
	LoaderNewly   int
	EnvironmentOK bool
	HostsOK       bool
	NotesOK       bool
	WriteRejected bool
	Bounded       bool
}

type selftest56Result struct {
	report selftest56Report
	err    error
}

func (t *selftest56) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest56Ticket,
		Name:   "External MCP stdio catalog and read tools",
		Steps: []selftest.Step{
			{Name: "stdio-authenticated-catalog", Run: t.exchangeStep},
			{Name: "verify-read-tools", Run: t.verifyStep},
		},
	}
}

func (t *selftest56) exchangeStep(*ui.Context) selftest.Outcome {
	if t.done {
		if t.err != nil {
			return selftest.Failed(t.err)
		}
		return t.passed()
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest56Timeout)
		t.result = make(chan selftest56Result, 1)
		ctx, cancel := context.WithTimeout(context.Background(), selftest56Timeout)
		t.cancel = cancel
		go func() {
			report, err := runSelftest56Exchange(ctx)
			t.result <- selftest56Result{report: report, err: err}
		}()
	}
	select {
	case result := <-t.result:
		t.done = true
		t.report = result.report
		t.err = result.err
		if t.cancel != nil {
			t.cancel()
		}
		if t.err != nil {
			return selftest.Failed(t.err)
		}
		return t.passed()
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the stdio MCP exchange did not finish in %s", selftest56Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the stdio MCP exchange")
	}
}

func (t *selftest56) passed() selftest.Outcome {
	return selftest.Passed(fmt.Sprintf(
		"stdio authenticated and the catalog grew %d -> %d -> %d visible tools",
		t.report.InitialTools, t.report.AfterToolset, t.report.AfterAll),
		"cmd/natcatty-mcp + internal/adapters/mcprpc")
}

func (t *selftest56) verifyStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.InitialTools != len(catalog.CoreNames)+1 {
		return selftest.Failed(fmt.Errorf("initial visible tools = %d, want %d",
			report.InitialTools, len(catalog.CoreNames)+1))
	}
	if report.AfterToolset != len(catalog.CoreNames)+1+len(catalog.Toolsets["sftp_advanced"]) {
		return selftest.Failed(fmt.Errorf("visible tools after sftp_advanced = %d", report.AfterToolset))
	}
	if report.AfterAll != len(catalog.All())+1 {
		return selftest.Failed(fmt.Errorf("visible tools after all = %d, want %d",
			report.AfterAll, len(catalog.All())+1))
	}
	if report.LoaderNewly != len(catalog.Toolsets["sftp_advanced"]) {
		return selftest.Failed(fmt.Errorf("loader loaded %d tools, want %d",
			report.LoaderNewly, len(catalog.Toolsets["sftp_advanced"])))
	}
	if !report.EnvironmentOK || !report.HostsOK || !report.NotesOK {
		return selftest.Failed(fmt.Errorf(
			"read tools: environment=%v hosts=%v notes=%v",
			report.EnvironmentOK, report.HostsOK, report.NotesOK))
	}
	if !report.WriteRejected {
		return selftest.Failed(errors.New("a write tool was not rejected as read-only"))
	}
	if !report.Bounded {
		return selftest.Failed(errors.New("a tool result exceeded the 256 KiB bound"))
	}
	return selftest.Passed(
		"69 catalog tools + loader; 14 core initially; environment, hosts, and notes read; writes rejected",
		"internal/mcp/catalog + app/mcp.go read bridge")
}

// runSelftest56Exchange performs the external-client half of the check. It is
// one goroutine so the UI thread only polls its result channel.
func runSelftest56Exchange(ctx context.Context) (selftest56Report, error) {
	var report selftest56Report

	discoveryPath := ""
	var dedicated *externalMCP
	if runningExternalMCP != nil {
		discoveryPath = runningExternalMCP.discoveryPath
	} else {
		// Unit tests construct the window graph without run(); use the same
		// production listener and read bridge with deterministic empty readers.
		directory, err := os.MkdirTemp("", "natcatty-selftest-56-state-")
		if err != nil {
			return report, fmt.Errorf("create a temporary MCP state directory: %w", err)
		}
		defer os.RemoveAll(directory)
		dedicated, err = startExternalMCP(ctx, newMCPReadBridge(
			selftest56EmptyVault{}, selftest56EmptyForwarding{},
			selftest56EmptySessions{}, nil,
		), filepath.Join(directory, "discovery.json"), nil)
		if err != nil {
			return report, fmt.Errorf("start the selftest MCP listener: %w", err)
		}
		defer dedicated.Close()
		discoveryPath = dedicated.discoveryPath
	}

	root, err := selftest56ModuleRoot()
	if err != nil {
		return report, err
	}
	binaryDirectory, err := os.MkdirTemp("", "natcatty-mcp-bin-")
	if err != nil {
		return report, fmt.Errorf("create a temporary binary directory: %w", err)
	}
	defer os.RemoveAll(binaryDirectory)
	binaryName := "natcatty-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(binaryDirectory, binaryName)

	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/natcatty-mcp")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return report, fmt.Errorf("build cmd/natcatty-mcp: %w\n%s", err, output)
	}

	command := exec.CommandContext(ctx, binaryPath)
	command.Dir = root
	command.Env = append(os.Environ(),
		mcprpc.EnvDiscoveryFile+"="+discoveryPath,
		mcprpc.EnvPort+"=",
		mcprpc.EnvToken+"=",
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		return report, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return report, err
	}
	var stderr selftest56Stderr
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return report, fmt.Errorf("start natcatty-mcp: %w", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	}()

	lines := make(chan string, 64)
	readErrors := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				lines <- trimmed
			}
			if err != nil {
				readErrors <- err
				return
			}
		}
	}()

	client := &selftest56Client{stdin: stdin, lines: lines, readErrors: readErrors}

	initialize, err := client.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "natcatty-selftest", "version": "1"},
	})
	if err != nil {
		return report, fmt.Errorf("initialize: %w (%s)", err, stderr.String())
	}
	initializeResult, err := mcpResult(initialize)
	if err != nil {
		return report, err
	}
	info, _ := initializeResult["serverInfo"].(map[string]any)
	if info["name"] != mcp.ServerName || info["version"] != mcp.ServerVersion {
		return report, fmt.Errorf("serverInfo = %v", info)
	}
	if instructions, _ := initializeResult["instructions"].(string); !strings.Contains(instructions, "15 core tools start loaded") {
		return report, fmt.Errorf("instructions = %q", instructions)
	}

	tools, err := client.request(ctx, "tools/list", nil)
	if err != nil {
		return report, err
	}
	names, err := mcpToolNames(tools)
	if err != nil {
		return report, err
	}
	report.InitialTools = len(names)
	if len(names) == 0 || names[0] != catalog.LoaderName {
		return report, fmt.Errorf("the loader is not first: %v", names)
	}

	loader, err := client.request(ctx, "tools/call", map[string]any{
		"name": catalog.LoaderName, "arguments": map[string]any{"toolset": "sftp_advanced"},
	})
	if err != nil {
		return report, err
	}
	loaderText, _, err := mcpToolText(loader)
	if err != nil {
		return report, err
	}
	var loaderPayload map[string]any
	if err := json.Unmarshal([]byte(loaderText), &loaderPayload); err != nil {
		return report, fmt.Errorf("loader result is not JSON: %w", err)
	}
	report.LoaderNewly = len(asAnySlice(loaderPayload["newlyLoaded"]))
	if visible := intNumber(loaderPayload["visibleToolCount"]); visible != len(catalog.CoreNames)+1+report.LoaderNewly {
		return report, fmt.Errorf("loader reported %d visible tools after loading %d",
			visible, report.LoaderNewly)
	}

	tools, err = client.request(ctx, "tools/list", nil)
	if err != nil {
		return report, err
	}
	if names, err = mcpToolNames(tools); err != nil {
		return report, err
	}
	report.AfterToolset = len(names)

	loader, err = client.request(ctx, "tools/call", map[string]any{
		"name": catalog.LoaderName, "arguments": map[string]any{"toolset": "all"},
	})
	if err != nil {
		return report, err
	}
	if _, _, err := mcpToolText(loader); err != nil {
		return report, err
	}
	tools, err = client.request(ctx, "tools/list", nil)
	if err != nil {
		return report, err
	}
	if names, err = mcpToolNames(tools); err != nil {
		return report, err
	}
	report.AfterAll = len(names)
	if !containsAll(names, "get_environment", "sftp_stat", "vault_notes_get", "portforward_start") {
		return report, fmt.Errorf("load all is missing expected tools: %v", names)
	}

	environment, err := client.request(ctx, "tools/call", map[string]any{
		"name": "get_environment", "arguments": map[string]any{},
	})
	if err != nil {
		return report, err
	}
	text, isError, err := mcpToolText(environment)
	if err != nil {
		return report, err
	}
	report.EnvironmentOK = !isError && strings.Contains(text, "netcatty-terminal")
	report.Bounded = len(text) <= mcp.MaxToolResultBytes+128

	hosts, err := client.request(ctx, "tools/call", map[string]any{
		"name": "vault_hosts_list", "arguments": map[string]any{},
	})
	if err != nil {
		return report, err
	}
	hostsText, hostsError, err := mcpToolText(hosts)
	if err != nil {
		return report, err
	}
	report.HostsOK = !hostsError && hostsText != ""
	report.Bounded = report.Bounded && len(hostsText) <= mcp.MaxToolResultBytes+128

	notes, err := client.request(ctx, "tools/call", map[string]any{
		"name": "vault_notes_list", "arguments": map[string]any{},
	})
	if err != nil {
		return report, err
	}
	notesText, notesError, err := mcpToolText(notes)
	if err != nil {
		return report, err
	}
	report.NotesOK = !notesError && notesText != ""

	write, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "selftest", "command": "true",
		},
	})
	if err != nil {
		return report, err
	}
	writeText, writeError, err := mcpToolText(write)
	if err != nil {
		return report, err
	}
	report.WriteRejected = writeError && strings.Contains(writeText, "read-only")

	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case <-waitDone:
		waited = true
	case <-ctx.Done():
		return report, fmt.Errorf("natcatty-mcp did not exit: %w (%s)", ctx.Err(), stderr.String())
	}
	if !report.WriteRejected {
		return report, fmt.Errorf("write rejection text = %q (stderr: %s)", writeText, stderr.String())
	}
	return report, nil
}

// selftest56Stderr collects the child's diagnostics while it can still be
// writing them, so an early failure can quote stderr without a data race.
type selftest56Stderr struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *selftest56Stderr) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(data)
}

func (s *selftest56Stderr) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// selftest56Client is the small stdio JSON-RPC client the check drives.
type selftest56Client struct {
	stdin      io.Writer
	lines      <-chan string
	readErrors <-chan error
	nextID     int
}

func (c *selftest56Client) request(ctx context.Context, method string, params any) (map[string]any, error) {
	id := c.nextID
	c.nextID++
	message := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-c.readErrors:
			return nil, fmt.Errorf("read stdout: %w", err)
		case line := <-c.lines:
			var response map[string]any
			if json.Unmarshal([]byte(line), &response) != nil {
				continue
			}
			if number, ok := response["id"].(float64); ok && int(number) == id {
				return response, nil
			}
		}
	}
}

func mcpResult(response map[string]any) (map[string]any, error) {
	if rpcErr, present := response["error"]; present {
		return nil, fmt.Errorf("JSON-RPC error: %v", rpcErr)
	}
	result, ok := response["result"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response has no result object: %v", response)
	}
	return result, nil
}

func mcpToolNames(response map[string]any) ([]string, error) {
	result, err := mcpResult(response)
	if err != nil {
		return nil, err
	}
	raw, ok := result["tools"].([]any)
	if !ok {
		return nil, fmt.Errorf("tools/list result = %v", result)
	}
	names := make([]string, 0, len(raw))
	for _, item := range raw {
		tool, _ := item.(map[string]any)
		name, _ := tool["name"].(string)
		names = append(names, name)
	}
	return names, nil
}

func mcpToolText(response map[string]any) (string, bool, error) {
	result, err := mcpResult(response)
	if err != nil {
		return "", false, err
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		return "", false, fmt.Errorf("tool result has no content: %v", result)
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	isError, _ := result["isError"].(bool)
	return text, isError, nil
}

func asAnySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func intNumber(value any) int {
	number, _ := value.(float64)
	return int(number)
}

func containsAll(values []string, wants ...string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		seen[value] = true
	}
	for _, want := range wants {
		if !seen[want] {
			return false
		}
	}
	return true
}

// selftest56ModuleRoot finds the repository root from the compiled source path,
// then from the working directory, so go build can address ./cmd/natcatty-mcp.
func selftest56ModuleRoot() (string, error) {
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		candidate := filepath.Dir(filepath.Dir(file))
		if fileExists(filepath.Join(candidate, "go.mod")) {
			return candidate, nil
		}
	}
	directories := make([]string, 0, 3)
	if working, err := os.Getwd(); err == nil {
		directories = append(directories, working)
	}
	if executable, err := os.Executable(); err == nil {
		directories = append(directories, filepath.Dir(executable))
	}
	for _, directory := range directories {
		for candidate := directory; ; candidate = filepath.Dir(candidate) {
			if fileExists(filepath.Join(candidate, "go.mod")) {
				return candidate, nil
			}
			if filepath.Dir(candidate) == candidate {
				break
			}
		}
	}
	return "", errors.New("selftest 56: could not find the module root (go.mod)")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// Empty readers keep the fallback listener deterministic in unit tests. They
// implement the same interfaces the production services satisfy.
type selftest56EmptyVault struct{}

func (selftest56EmptyVault) List(context.Context) ([]domainvault.Host, error) {
	return nil, nil
}
func (selftest56EmptyVault) Get(context.Context, model.HostID) (domainvault.Host, error) {
	return domainvault.Host{}, appvault.ErrNotFound
}
func (selftest56EmptyVault) ListNotes(context.Context) ([]domainvault.Note, error) {
	return nil, nil
}
func (selftest56EmptyVault) GetNote(context.Context, string) (domainvault.Note, error) {
	return domainvault.Note{}, appvault.ErrNotFound
}
func (selftest56EmptyVault) ListIdentities(context.Context) ([]domainvault.Identity, error) {
	return nil, nil
}
func (selftest56EmptyVault) ListProxyProfiles(context.Context) ([]domainvault.ProxyProfile, error) {
	return nil, nil
}
func (selftest56EmptyVault) ListGroups(context.Context) ([]domainvault.Group, error) {
	return nil, nil
}
func (selftest56EmptyVault) ListSnippets(context.Context) ([]domainvault.Snippet, error) {
	return nil, nil
}

type selftest56EmptySessions struct{}

func (selftest56EmptySessions) Describe(model.SessionID) (appsession.SessionDescription, bool) {
	return appsession.SessionDescription{}, false
}
func (selftest56EmptySessions) DescribeAll() []appsession.SessionDescription { return nil }
func (selftest56EmptySessions) ScreenText(model.SessionID) (appsession.ScreenTextSnapshot, bool) {
	return appsession.ScreenTextSnapshot{}, false
}

type selftest56EmptyForwarding struct{}

func (selftest56EmptyForwarding) List(context.Context) ([]appforwarding.View, error) {
	return nil, nil
}
