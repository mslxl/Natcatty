package app

// Ticket 58 rework evidence: every executable catalog write through the
// compiled natcatty-mcp stdio client, against the shipped-app dynamic Session
// scope and real transports. The SSH peer is the in-process fakessh fixture
// running a real POSIX shell and a real pkg/sftp subsystem rooted at a temp
// directory; the local shell, Vault, secret store, connection Manager, SFTP
// adapter, and forwarding runtime are the production ones.

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/localpty"
	adaptersftp "github.com/mslxl/natcatty/adapters/sftp"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	"github.com/mslxl/natcatty/adapters/store"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainstransfer "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
)

// realWriteNetwork starts a fresh fakessh peer for every physical SSH dial.
// The peer serves an interactive POSIX shell and an SFTP subsystem rooted at
// the fixture directory, so the real Manager, SFTP adapter, and transfer
// engine run unchanged; only the TCP handshake is modelled.
type realWriteNetwork struct {
	root     string
	password string
	shell    string

	mu      sync.Mutex
	dials   int
	servers []*fakessh.Server
	conns   []net.Conn
}

func (n *realWriteNetwork) dial(context.Context, string, string) (net.Conn, error) {
	client, server := fakessh.Pipe()
	options := fakessh.Options{
		Password: n.password,
		Subsystems: map[string]func(xssh.Channel){
			"sftp": func(channel xssh.Channel) {
				root := n.root
				instance, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
				if err != nil {
					_ = channel.Close()
					return
				}
				_ = instance.Serve()
				_ = instance.Close()
			},
		},
	}
	if shell := n.shell; shell != "" {
		options.Shell = func(channel xssh.Channel) { serveRealShell(shell, channel) }
	}
	instance, err := fakessh.Start(server, options)
	if err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	n.mu.Lock()
	n.dials++
	n.servers = append(n.servers, instance)
	n.conns = append(n.conns, client, server)
	n.mu.Unlock()
	return client, nil
}

// serveRealShell runs a real POSIX shell over the channel so the marker
// wrapper, blocklist, timeout interrupt, and background-job paths all execute.
func serveRealShell(path string, channel xssh.Channel) {
	command := exec.Command(path, "--noprofile", "--norc")
	stdin, stdinErr := command.StdinPipe()
	stdout, stdoutErr := command.StdoutPipe()
	if stdinErr != nil || stdoutErr != nil {
		_ = channel.Close()
		return
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		_ = channel.Close()
		return
	}
	go func() { _, _ = io.Copy(stdin, channel) }()
	_, _ = io.Copy(channel, stdout)
	_ = command.Process.Kill()
	_, _ = command.Process.Wait()
}

func (n *realWriteNetwork) close() {
	n.mu.Lock()
	servers, conns := n.servers, n.conns
	n.servers, n.conns = nil, nil
	n.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	for _, server := range servers {
		_ = server.Close()
	}
}

func (n *realWriteNetwork) dialCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dials
}

type realWritePrompter struct{}

func (realWritePrompter) Confirm(context.Context, domainconn.Key) (appconnection.PromptDecision, error) {
	return appconnection.PromptDecision{Trust: true}, nil
}

// mcpRealWriteFixture is the real write graph behind the external MCP
// listener. The policy scope is the shipped-app live-Session source, not a
// launch-time allowlist.
type mcpRealWriteFixture struct {
	ctx        context.Context
	database   *store.Store
	secrets    *store.SecretStore
	vault      *appvault.Service
	sessions   *appsession.Service
	manager    *appconnection.Manager
	forwarding *appforwarding.Manager
	transfer   *apptransfer.Service
	policy     *appmcp.Service
	handler    *mcpPolicyHandler
	executor   *mcpWriteExecutor
	bridge     *mcpReadBridge
	live       *mcpLiveSessions
	network    *realWriteNetwork

	localSession model.SessionID
	sshSession   model.SessionID
	host         domainvault.Host
	remoteRoot   string
	localRoot    string

	modeMu sync.Mutex
	mode   appmcp.Mode
}

func (f *mcpRealWriteFixture) setMode(mode appmcp.Mode) {
	f.modeMu.Lock()
	f.mode = mode
	f.modeMu.Unlock()
}

func (f *mcpRealWriteFixture) openHost(ctx context.Context, host domainvault.Host) (model.SessionID, error) {
	session, err := f.sessions.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindSSH, Label: host.Label, HostID: host.ID,
		Hostname: host.Hostname, Port: host.Port, Username: host.Username,
		Cols: 80, Rows: 24,
	})
	if err != nil {
		return "", err
	}
	return session.ID(), nil
}

func newMCPRealWriteFixture(t *testing.T) *mcpRealWriteFixture {
	t.Helper()
	ctx := context.Background()
	shell, _ := exec.LookPath("bash")
	if shell == "" {
		shell, _ = exec.LookPath("sh")
	}
	if shell == "" {
		t.Skip("no POSIX shell is available for the real SSH fixture")
	}

	dir := t.TempDir()
	remoteDirectory := filepath.Join(dir, "remote")
	localDirectory := filepath.Join(dir, "local")
	for _, path := range []string{remoteDirectory, localDirectory} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create the fixture directory: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(remoteDirectory, "read.txt"), []byte("real download"), 0o640); err != nil {
		t.Fatalf("seed the remote file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDirectory, "upload.txt"), []byte("real upload"), 0o600); err != nil {
		t.Fatalf("seed the local file: %v", err)
	}

	database, err := store.Open(ctx, filepath.Join(dir, "vault.db"))
	if err != nil {
		t.Fatalf("open the Vault database: %v", err)
	}
	keyRing := &writeTestKeyRing{values: map[string][]byte{}}
	secrets, err := store.OpenSecretStore(ctx, database, keyRing,
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open the secret store: %v", err)
	}
	vault := appvault.NewService(database, appvault.WithSecrets(secrets),
		appvault.WithKeys(database), appvault.WithGroups(database),
		appvault.WithNotes(database), appvault.WithSnippets(database),
		appvault.WithIdentities(database), appvault.WithProxyProfiles(database),
		appvault.WithKnownHosts(database), appvault.WithImportStore(database))

	network := &realWriteNetwork{root: remoteDirectory, password: "s3cret", shell: shell}
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(network.dial),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	manager := appconnection.NewManager(dialer, vaultPasswords{vault: vault},
		&factoryKnownHosts{}, appconnection.WithPrompter(realWritePrompter{}))
	sessions := appsession.NewService(newTransportFactory(
		localpty.Factory{}, nil, nil, manager, nil))

	host, err := vault.Create(ctx, domainvault.Draft{
		Label: "real fixture", Hostname: "real.example", Port: 22,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
		SFTPFileProtocol: domainvault.FileProtocolSFTP,
	})
	if err != nil {
		network.close()
		manager.CloseAll()
		sessions.CloseAll()
		secrets.Close()
		_ = database.Close()
		t.Fatalf("create the fixture Host: %v", err)
	}
	if err := vault.SetSecret(ctx, host.ID, appvault.FieldPassword, "s3cret"); err != nil {
		t.Fatalf("store the fixture password: %v", err)
	}

	localSession, err := sessions.OpenLocal(ctx)
	if err != nil {
		t.Fatalf("open the real local shell: %v", err)
	}
	sshSession, err := sessions.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "real ssh", HostID: host.ID,
		Hostname: host.Hostname, Port: host.Port, Username: host.Username,
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the real SSH Session: %v", err)
	}

	forwarding := appforwarding.NewManager(database, forwardingConnector{
		vault: vault, connections: manager,
	})
	opener := &sftpRemoteOpener{manager: manager, vault: vault}
	transfer := apptransfer.NewService(adaptersftp.NewLocal(), opener)
	remoteFiles, err := transfer.OpenRemote(ctx, host.ID, "real fixture")
	if err != nil {
		t.Fatalf("open the real remote pane: %v", err)
	}
	remoteRoot, err := remoteFiles.Root(ctx)
	_ = remoteFiles.Close()
	if err != nil {
		t.Fatalf("read the remote root: %v", err)
	}

	fixture := &mcpRealWriteFixture{
		database: database, secrets: secrets, vault: vault, sessions: sessions,
		manager: manager, forwarding: forwarding, transfer: transfer,
		network: network, remoteRoot: remoteRoot, localRoot: localDirectory,
		localSession: localSession.ID(), sshSession: sshSession.ID(), host: host,
		mode: appmcp.ModeAuto,
	}
	mode := func() appmcp.Mode {
		fixture.modeMu.Lock()
		defer fixture.modeMu.Unlock()
		return fixture.mode
	}
	fixture.live = newMCPLiveSessions(sessions, dir, nil)
	fixture.policy = appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: "__external_mcp__",
		Sessions:            fixture.live,
		Mode:                mode,
	})
	fixture.bridge = newMCPReadBridge(vault, forwarding, sessions, transfer)
	fixture.handler = newMCPPolicyHandler(fixture.bridge, fixture.policy, sessions,
		sessions, nil)
	fixture.executor = newMCPWriteExecutor(fixture.policy, sessions, vault,
		forwarding, transfer, fixture.openHost)
	fixture.handler.attachWrites(fixture.executor)
	fixture.ctx = ctx

	t.Cleanup(func() {
		fixture.sessions.CloseAll()
		fixture.manager.CloseAll()
		fixture.forwarding.Close()
		fixture.network.close()
		fixture.secrets.Close()
		_ = fixture.database.Close()
	})
	return fixture
}

// realStdioClient starts the compiled natcatty-mcp process against a fresh
// authenticated listener and initializes the MCP session.
func realStdioClient(t *testing.T, fixture *mcpRealWriteFixture) (*stdioMCPClient, func()) {
	t.Helper()
	root := policyModuleRoot(t)
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 180*time.Second)
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
	listener, err := startExternalMCP(listenerContext, fixture.bridge, discoveryPath, nil,
		withMCPHandler(fixture.handler))
	if err != nil {
		cancelListener()
		t.Fatalf("start the external MCP listener: %v", err)
	}

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 240*time.Second)
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
	client := newStdioMCPClient(stdin, stdout)
	response := client.request(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ticket58-real", "version": "1.0"},
	})
	if response.Error != nil {
		t.Fatalf("initialize: %+v", response.Error)
	}
	// The progressive surface starts with the core tools; loading all is the
	// same call a real client makes before using a Vault write.
	loaderID := client.send("tools/call", map[string]any{
		"name": "load_netcatty_tools", "arguments": map[string]any{"toolset": "all"},
	})
	if _, isError := toolCallResult(t, client.receiveID(t, loaderID)); isError {
		t.Fatalf("load all tools failed")
	}
	cleanup := func() {
		_ = stdin.Close()
		waitDone := make(chan error, 1)
		go func() { waitDone <- command.Wait() }()
		select {
		case <-waitDone:
		case <-time.After(30 * time.Second):
			t.Errorf("natcatty-mcp did not exit after stdin closed: %s", stderr.String())
			_ = command.Process.Kill()
		}
		cancelCommand()
		listener.Close()
		cancelListener()
	}
	return client, cleanup
}

func realFreePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a local port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// TestMCPWriteEveryExecutableToolWithRealTransportsAndDynamicScope is the
// rework's end-to-end reachability evidence. The policy has no launch-time
// allowlist: the live Sessions are the scope, exactly as the shipped app now
// behaves. All 34 executable writes cross the compiled stdio client, and the
// session-scoped ones act on a real shell and a real SFTP subsystem.
func TestMCPWriteEveryExecutableToolWithRealTransportsAndDynamicScope(t *testing.T) {
	fixture := newMCPRealWriteFixture(t)
	fixture.setMode(appmcp.ModeAuto)
	client, cleanup := realStdioClient(t, fixture)
	defer cleanup()

	sessionID := string(fixture.localSession)
	sshSessionID := string(fixture.sshSession)

	// The dynamic scope lists both live Sessions through the real client.
	environment := e2eToolPayload(t, client, "get_environment", map[string]any{})
	if environment["hostCount"] != float64(2) {
		t.Fatalf("get_environment hostCount = %v, want 2 (%#v)", environment["hostCount"], environment["hosts"])
	}
	listed := make(map[string]bool)
	for _, raw := range environment["hosts"].([]any) {
		entry, _ := raw.(map[string]any)
		listed[fmt.Sprint(entry["sessionId"])] = true
	}
	if !listed[sessionID] || !listed[sshSessionID] {
		t.Fatalf("get_environment listed %v, want both live Sessions", listed)
	}

	// The real local shell and the real SSH shell both execute a command.
	executed := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "echo ncmcp-real-local",
	})
	if stdout, _ := executed["stdout"].(string); !strings.Contains(stdout, "ncmcp-real-local") {
		t.Fatalf("local terminal_execute stdout = %q", stdout)
	}
	remoteExecuted := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sshSessionID, "command": "echo ncmcp-real-ssh",
	})
	if stdout, _ := remoteExecuted["stdout"].(string); !strings.Contains(stdout, "ncmcp-real-ssh") {
		t.Fatalf("SSH terminal_execute stdout = %q", stdout)
	}

	// Background start / poll / stop on the real local shell.
	started := e2eToolOK(t, client, "terminal_start", map[string]any{
		"sessionId": sessionID, "command": "echo ncmcp-real-job",
	})
	jobID := e2eField(t, started, "jobId")
	deadline := time.Now().Add(30 * time.Second)
	var polled map[string]any
	for time.Now().Before(deadline) {
		polled = e2eToolOK(t, client, "terminal_poll", map[string]any{
			"jobId": jobID, "offset": float64(0),
		})
		if polled["completed"] == true {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
	if polled["completed"] != true {
		t.Fatalf("background job did not complete: %#v", polled)
	}
	if output, _ := polled["output"].(string); !strings.Contains(output, "ncmcp-real-job") {
		t.Fatalf("background job output = %q", output)
	}
	e2eToolOK(t, client, "terminal_stop", map[string]any{"jobId": jobID})

	// Canonicalized bypasses never reach either shell. The marker lives in a
	// fresh disposable directory; a missed bypass would delete it.
	disposable, err := os.MkdirTemp("", "natcatty-real-bypass-")
	if err != nil {
		t.Fatalf("create the disposable directory: %v", err)
	}
	defer os.RemoveAll(disposable)
	marker := filepath.Join(disposable, "marker.txt")
	if err := os.WriteFile(marker, []byte("real-bypass-marker"), 0o600); err != nil {
		t.Fatalf("write the disposable marker: %v", err)
	}
	posixMarker := filepath.ToSlash(marker)
	posixForms := []struct {
		name    string
		command string
	}{
		{"phrase-quotes", fmt.Sprintf(`r''m -rf %s`, posixMarker)},
		{"double-quotes", fmt.Sprintf(`"rm" -rf %s`, posixMarker)},
		{"single-quotes", fmt.Sprintf(`'rm' -rf %s`, posixMarker)},
		{"partial-quotes", fmt.Sprintf(`"r"m -rf %s`, posixMarker)},
		{"ansi-c", fmt.Sprintf(`$'r'm -rf %s`, posixMarker)},
		{"ifs", fmt.Sprintf(`rm$IFS-rf %s`, posixMarker)},
		{"braced-ifs", fmt.Sprintf(`rm${IFS}-rf %s`, posixMarker)},
		{"assignment", fmt.Sprintf(`x=rm; $x -rf %s`, posixMarker)},
		{"command-prefix", fmt.Sprintf(`command rm -rf %s`, posixMarker)},
		{"env-prefix", fmt.Sprintf(`env rm -rf %s`, posixMarker)},
		{"backslash", fmt.Sprintf(`\rm -rf %s`, posixMarker)},
		{"shell-payload", fmt.Sprintf(`bash -c '"rm" -rf %s'`, posixMarker)},
		// Second rework: brace expansion, any-command-into-shell, and find's
		// own delete action, through the real client and real SSH shell.
		{"brace-words", fmt.Sprintf(`{rm,-rf,%s}`, posixMarker)},
		{"second-brace-group", fmt.Sprintf(`echo {a,b}; {rm,-rf,%s}`, posixMarker)},
		{"brace-variable-path", fmt.Sprintf(`D=%s; {rm,-rf,$D}`, posixMarker)},
		{"printf-pipe-sh", fmt.Sprintf("printf 'r\\155 -rf %%s' %s | sh", posixMarker)},
		{"printf-pipe-env-sh", fmt.Sprintf("printf 'r\\155 -rf %%s' %s | env sh", posixMarker)},
		{"find-delete", fmt.Sprintf(`find %s -delete`, posixMarker)},
	}
	for _, form := range posixForms {
		blocked := e2eToolErrorContains(t, client, "terminal_execute", map[string]any{
			"sessionId": sshSessionID, "command": form.command,
		}, "mcp_command_blocked")
		expectedRule := "common."
		switch form.name {
		case "printf-pipe-sh", "printf-pipe-env-sh":
			expectedRule = "posixNative.pipe-to-shell"
		case "find-delete":
			expectedRule = "posix.find-delete"
		}
		if !strings.Contains(blocked, expectedRule) {
			t.Fatalf("%s block did not name %s: %s", form.name, expectedRule, blocked)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("bypass %q was blocked but the marker is gone: %v", form.name, err)
		}
	}
	if shell, _ := fixture.sessions.ShellKind(fixture.localSession); shell == "powershell" {
		windowsMarker := strings.ReplaceAll(marker, `\`, `\\`)
		powerShellForms := []struct {
			name    string
			command string
		}{
			{"backtick-escaped-parameter", fmt.Sprintf("Remove-Item -`Re`curse -Force %s", windowsMarker)},
			{"backtick-escaped-alias", fmt.Sprintf("ri -`r -`fo %s", windowsMarker)},
			{"backtick-newline", fmt.Sprintf("Remove-Item -Rec`\nurse -Force %s", windowsMarker)},
			{"call-operator-quoted", fmt.Sprintf("& 'Remove-Item' -Recurse -Force %s", windowsMarker)},
			{"quoted-command-and-parameters", fmt.Sprintf("\"Remove-Item\" \"-Recurse\" \"-Force\" %s", windowsMarker)},
			// Second rework: constant folding, alias resolution, and the cmd
			// interpreter carrying a force delete.
			{"folded-call-operator", fmt.Sprintf("& (''+'Remove'+'-Item') -Recurse -Force %s", windowsMarker)},
			{"alias-then-invoke", fmt.Sprintf("Set-Alias zz Remove-Item; zz -Recurse -Force %s", windowsMarker)},
			{"cmd-keep-open-delete", fmt.Sprintf(`cmd /k del /f /q "%s" & exit`, windowsMarker)},
			{"cmd-glued-delete", fmt.Sprintf(`cmd /c"del /f /q "%s" & exit"`, windowsMarker)},
		}
		for _, form := range powerShellForms {
			e2eToolErrorContains(t, client, "terminal_execute", map[string]any{
				"sessionId": sessionID, "command": form.command,
			}, "mcp_command_blocked")
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("PowerShell bypass %q removed the marker: %v", form.name, err)
			}
		}
	}

	// Vault Host create, update, notes, import, then delete.
	created := e2eToolOK(t, client, "vault_hosts_create", map[string]any{
		"hosts": `[{"hostname":"real-created.example","label":"real created","username":"ops"}]`,
	})
	createdHostID := e2eNestedID(t, created, "created", "id")
	e2eToolOK(t, client, "vault_hosts_update", map[string]any{
		"hostId": createdHostID, "label": "real renamed", "notes": "real details",
	})
	e2eToolOK(t, client, "host_notes_set", map[string]any{
		"hostId": createdHostID, "notes": "real notes",
	})
	imported := e2eToolOK(t, client, "vault_hosts_import", map[string]any{
		"format": "csv", "text": "hostname,label\nreal-import.example,real import\n",
	})
	if imported["imported"] == nil {
		t.Fatalf("vault_hosts_import result = %#v", imported)
	}

	// Vault Note create, update, import, delete.
	note := e2eToolOK(t, client, "vault_notes_create", map[string]any{
		"title": "real note", "content": "# real\nbody",
	})
	noteID := e2eField(t, note, "id")
	e2eToolOK(t, client, "vault_notes_update", map[string]any{
		"noteId": noteID, "content": "changed through the real client",
	})
	e2eToolOK(t, client, "vault_notes_import", map[string]any{
		"fileName": "real-import.md", "content": "imported through the real client",
	})
	e2eToolOK(t, client, "vault_notes_delete", map[string]any{"noteId": noteID})

	// Vault Group create, update, delete.
	e2eToolOK(t, client, "vault_groups_create", map[string]any{
		"path": "real/infra", "defaults": `{"username":"real-group"}`,
	})
	e2eToolOK(t, client, "vault_groups_update", map[string]any{
		"path": "real/infra", "newPath": "real/prod", "defaults": `{"username":"real-prod"}`,
	})
	e2eToolOK(t, client, "vault_groups_delete", map[string]any{
		"path": "real/prod", "deleteHosts": "false",
	})

	// Snippets create, update, run through the real local shell, delete.
	snippet := e2eToolOK(t, client, "snippets_create", map[string]any{
		"label": "real snippet", "content": "echo ncmcp-real-snippet",
	})
	snippetID := e2eField(t, snippet, "id")
	e2eToolOK(t, client, "snippets_update", map[string]any{
		"snippetId": snippetID, "label": "real snippet renamed", "noAutoRun": "true",
	})
	e2eToolOK(t, client, "snippets_run", map[string]any{
		"snippetId": snippetID, "sessionId": sessionID,
	})
	e2eToolOK(t, client, "snippets_delete", map[string]any{"snippetId": snippetID})

	// Port-forwarding rules over the real connection Manager and a real local
	// listener: create, update, duplicate, start, stop, delete.
	rule := e2eToolOK(t, client, "portforward_rules_create", map[string]any{
		"label": "real rule", "type": "local", "localPort": float64(realFreePort(t)),
		"bindAddress": "127.0.0.1", "remoteHost": "db.internal",
		"remotePort": float64(5432), "hostId": string(fixture.host.ID),
	})
	ruleID := e2eField(t, rule, "ruleId")
	e2eToolOK(t, client, "portforward_rules_update", map[string]any{
		"ruleId": ruleID, "label": "real rule updated", "localPort": float64(realFreePort(t)),
	})
	duplicated := e2eToolOK(t, client, "portforward_rules_duplicate", map[string]any{
		"ruleId": ruleID,
	})
	duplicateID := e2eField(t, duplicated, "ruleId")
	e2eToolOK(t, client, "portforward_start", map[string]any{"ruleId": duplicateID})
	waitRealForwardStatus(t, fixture, duplicateID, domainforward.StatusActive)
	e2eToolOK(t, client, "portforward_stop", map[string]any{"ruleId": duplicateID})
	waitRealForwardStatus(t, fixture, duplicateID, domainforward.StatusInactive)
	e2eToolOK(t, client, "portforward_rules_delete", map[string]any{"ruleId": duplicateID})
	e2eToolOK(t, client, "portforward_rules_delete", map[string]any{"ruleId": ruleID})

	// The seven SFTP writes against the real pkg/sftp subsystem.
	remotePath := func(name string) string { return fixture.remoteRoot + "/" + name }
	e2eToolOK(t, client, "sftp_write_file", map[string]any{
		"sessionId": sshSessionID, "path": remotePath("written.txt"), "content": "real written",
	})
	requireRealFile(t, filepath.Join(fixture.network.root, "written.txt"), "real written")
	e2eToolOK(t, client, "sftp_mkdir", map[string]any{
		"sessionId": sshSessionID, "path": remotePath("newdir"),
	})
	if info, err := os.Stat(filepath.Join(fixture.network.root, "newdir")); err != nil || !info.IsDir() {
		t.Fatalf("sftp_mkdir did not create the real directory: %v", err)
	}
	e2eToolOK(t, client, "sftp_rename", map[string]any{
		"sessionId": sshSessionID, "oldPath": remotePath("written.txt"),
		"newPath": remotePath("renamed.txt"),
	})
	requireRealFile(t, filepath.Join(fixture.network.root, "renamed.txt"), "real written")
	if _, err := os.Stat(filepath.Join(fixture.network.root, "written.txt")); !os.IsNotExist(err) {
		t.Fatalf("sftp_rename left the original: %v", err)
	}
	e2eToolOK(t, client, "sftp_chmod", map[string]any{
		"sessionId": sshSessionID, "path": remotePath("renamed.txt"), "mode": "755",
	})
	e2eToolOK(t, client, "sftp_delete", map[string]any{
		"sessionId": sshSessionID, "path": remotePath("renamed.txt"),
	})
	if _, err := os.Stat(filepath.Join(fixture.network.root, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("sftp_delete left the file: %v", err)
	}
	downloadPath := filepath.Join(fixture.localRoot, "downloaded.txt")
	e2eToolOK(t, client, "sftp_download", map[string]any{
		"sessionId": sshSessionID, "remotePath": remotePath("read.txt"),
		"localPath": downloadPath,
	})
	requireRealFile(t, downloadPath, "real download")
	e2eToolOK(t, client, "sftp_upload", map[string]any{
		"sessionId": sshSessionID, "localPath": filepath.Join(fixture.localRoot, "upload.txt"),
		"remotePath": remotePath("uploaded.txt"),
	})
	requireRealFile(t, filepath.Join(fixture.network.root, "uploaded.txt"), "real upload")

	// Host reuse is visibility-only; only newTab grants close ownership.
	reused := e2eToolOK(t, client, "host_open", map[string]any{
		"hostId": string(fixture.host.ID), "newTab": "false",
	})
	if reused["status"] != "reused" || reused["sessionId"] != sshSessionID {
		t.Fatalf("host_open reuse = %#v", reused)
	}
	e2eToolErrorContains(t, client, "session_close", map[string]any{
		"sessionId": sshSessionID,
	}, "mcp_session_not_owned")
	opened := e2eToolOK(t, client, "host_open", map[string]any{
		"hostId": string(fixture.host.ID), "newTab": "true",
	})
	if opened["status"] != "opened" {
		t.Fatalf("host_open newTab = %#v", opened)
	}
	openedSessionID := e2eField(t, opened, "sessionId")
	if openedSessionID == sshSessionID {
		t.Fatalf("host_open newTab reused an existing Session: %#v", opened)
	}
	// The new Session really runs on the SSH fixture.
	openedExecuted := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": openedSessionID, "command": "echo ncmcp-real-opened",
	})
	if stdout, _ := openedExecuted["stdout"].(string); !strings.Contains(stdout, "ncmcp-real-opened") {
		t.Fatalf("opened Session stdout = %q", stdout)
	}
	closed := e2eToolOK(t, client, "session_close", map[string]any{
		"sessionId": openedSessionID,
	})
	if closed["status"] != "closed" {
		t.Fatalf("session_close = %#v", closed)
	}
	// session_close is asynchronous: the Session is unregistered when its
	// lifecycle loop finishes, which can be after the tool has already
	// returned. Wait for the observable condition instead of sampling once.
	waitFor(t, "session_close to unregister the opened Session", func() bool {
		_, ok := fixture.sessions.Describe(model.SessionID(openedSessionID))
		return !ok
	})

	// Vault Host delete completes the vault family.
	e2eToolOK(t, client, "vault_hosts_delete", map[string]any{"hostId": createdHostID})

	// The nine script tools are typed-unavailable with no side effects.
	beforeSnippets, _ := fixture.vault.ListSnippets(fixture.ctx)
	unavailable := map[string]map[string]any{
		"scripts_create":           {"label": "x", "content": "x"},
		"scripts_update":           {"scriptId": "x", "content": "x"},
		"scripts_delete":           {"scriptId": "x"},
		"scripts_run":              {"scriptId": "x"},
		"scripts_run_stop":         {"runId": "x"},
		"scripts_run_pause":        {"runId": "x"},
		"scripts_run_resume":       {"runId": "x"},
		"scripts_targets_set":      {"scriptId": "x", "targets": "[]"},
		"host_connect_scripts_set": {"hostId": string(fixture.host.ID), "scriptIds": "[]"},
	}
	for name, arguments := range unavailable {
		e2eToolErrorContains(t, client, name, arguments,
			"mcp_tool_unavailable", "not available in this build")
	}
	afterSnippets, _ := fixture.vault.ListSnippets(fixture.ctx)
	if len(afterSnippets) != len(beforeSnippets) {
		t.Fatalf("an unavailable tool changed Snippets: %d -> %d", len(beforeSnippets), len(afterSnippets))
	}
	// The unavailability is side-effect free: reading the Host's connect
	// script queue back through the same real client shows it still empty.
	bindings := e2eToolPayload(t, client, "host_connect_scripts_list", map[string]any{
		"hostId": string(fixture.host.ID),
	})
	if ids, _ := bindings["connectScriptIds"].([]any); len(ids) != 0 {
		t.Fatalf("host connection scripts were written: %#v", bindings)
	}
	// get_environment still lists the live Sessions after the close: the
	// dynamic scope has no stale id from the opened Session.
	after := e2eToolPayload(t, client, "get_environment", map[string]any{})
	if after["hostCount"] != float64(2) {
		t.Fatalf("get_environment after close hostCount = %v, want 2", after["hostCount"])
	}
}

// e2eToolPayload is e2eToolOK's sibling for the read-shaped results that carry
// no "ok" field, such as get_environment.
func e2eToolPayload(t *testing.T, client *stdioMCPClient, name string,
	arguments map[string]any) map[string]any {
	t.Helper()
	call := e2eToolCall(t, client, name, arguments)
	if call.isError {
		t.Fatalf("%s failed: %s", name, call.text)
	}
	return call.result
}

func requireRealFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func waitRealForwardStatus(t *testing.T, fixture *mcpRealWriteFixture,
	id string, want domainforward.Status) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last domainforward.Status
	for time.Now().Before(deadline) {
		views, err := fixture.forwarding.List(fixture.ctx)
		if err != nil {
			t.Fatalf("list forwarding rules: %v", err)
		}
		last = ""
		for _, view := range views {
			if string(view.Rule.ID) == id {
				last = view.Status
			}
		}
		if last == want {
			return
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
	t.Fatalf("forwarding rule %s status = %q, want %q", id, last, want)
}

// TestMCPWriteDynamicScopeDrivesRealClientReachability pins the shipped-app
// property ticket 58 shipped without: a Session created after launch is
// writable through the real client, session_close is reachable, and turning
// sharing off removes it immediately.
func TestMCPWriteDynamicScopeDrivesRealClientReachability(t *testing.T) {
	fixture := newMCPRealWriteFixture(t)
	fixture.setMode(appmcp.ModeAuto)
	client, cleanup := realStdioClient(t, fixture)
	defer cleanup()

	snapshot := fixture.policy.Snapshot()
	if !snapshot.LiveScope || !snapshot.SharingEnabled || len(snapshot.Allowlist) != 2 {
		t.Fatalf("dynamic snapshot = %#v", snapshot)
	}
	// newTab grants ownership, so session_close through the real client is
	// reachable without any launch-time allowlist.
	opened := e2eToolOK(t, client, "host_open", map[string]any{
		"hostId": string(fixture.host.ID), "newTab": "true",
	})
	openedSessionID := e2eField(t, opened, "sessionId")
	closed := e2eToolOK(t, client, "session_close", map[string]any{"sessionId": openedSessionID})
	if closed["status"] != "closed" {
		t.Fatalf("session_close = %#v", closed)
	}

	if err := fixture.live.SetEnabled(false); err != nil {
		t.Fatalf("turn sharing off: %v", err)
	}
	e2eToolErrorContains(t, client, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession), "command": "echo never",
	}, "mcp_session_out_of_scope")
	if err := fixture.live.SetEnabled(true); err != nil {
		t.Fatalf("turn sharing back on: %v", err)
	}
	executed := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession), "command": "echo ncmcp-real-again",
	})
	if stdout, _ := executed["stdout"].(string); !strings.Contains(stdout, "ncmcp-real-again") {
		t.Fatalf("stdout after re-enabling sharing = %q", stdout)
	}
	// The persisted switch is readable back through the Settings snapshot.
	settings := newMCPLiveSessions(fixture.sessions, filepath.Dir(fixture.live.path), nil)
	if !settings.SharingEnabled() {
		t.Fatal("the persisted sharing switch did not survive a reload")
	}
}

// TestMCPWriteTimeoutAndTruncationOnTheRealSSHShell proves the typed timeout
// and bounded-output answers on the real fixture shell, not a modelled one.
func TestMCPWriteTimeoutAndTruncationOnTheRealSSHShell(t *testing.T) {
	fixture := newMCPRealWriteFixture(t)
	fixture.setMode(appmcp.ModeAuto)
	client, cleanup := realStdioClient(t, fixture)
	defer cleanup()
	sessionID := string(fixture.sshSession)

	// A timeout is a typed failure over the protocol and the Session stays
	// usable. The partial output rides on the handler result; the generic
	// stdio renderer keeps only the error line for an ok:false result, which
	// the handler-seam test below asserts. The test-only deadlines below leave
	// room for a loaded runner to echo the command while remaining far shorter
	// than the interrupted sleeps.
	fixture.executor.toolTimeout = 3 * time.Second
	call := e2eToolCall(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID,
		"command":   "printf 'real-before-timeout\n'; sleep 12; printf 'never\n'",
	})
	if !call.isError || !strings.Contains(call.text, "mcp_command_timeout") {
		t.Fatalf("timeout over stdio = %q (isError=%v)", call.text, call.isError)
	}
	fixture.executor.toolTimeout = 30 * time.Second
	after := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "echo real-after-timeout",
	})
	if stdout, _ := after["stdout"].(string); !strings.Contains(stdout, "real-after-timeout") {
		t.Fatalf("the timed-out Session did not recover: %q", stdout)
	}
	// With the shell responsive again, the partial output of a slow command is
	// returned alongside the typed timeout.
	fixture.executor.toolTimeout = 3 * time.Second
	partial := fixture.executor.Execute(fixture.ctx, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "printf 'real-partial\n'; sleep 12",
	}, appmcp.Decision{})
	if partial["code"] != string(appmcp.CodeCommandTimeout) ||
		!strings.Contains(fmt.Sprint(partial["stdout"]), "real-partial") {
		t.Fatalf("timeout handler result = %#v", partial)
	}
	fixture.executor.toolTimeout = 30 * time.Second
	if recovered := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "echo real-after-partial",
	}); !strings.Contains(fmt.Sprint(recovered["stdout"]), "real-after-partial") {
		t.Fatalf("the Session did not recover after the partial timeout: %#v", recovered)
	}

	// Output above the foreground ceiling is truncated and marked, and the
	// retained bytes are the newest ones. Generating a megabyte through a real
	// PTY can take longer under a loaded parallel suite than the short probe
	// deadlines above; the production bound itself is covered by the bounds
	// and GUI tests, so this evidence call gets a wide test-only deadline.
	fixture.executor.toolTimeout = 180 * time.Second
	produced := appmcp.ForegroundOutputLimit + 8192
	truncated := e2eToolOK(t, client, "terminal_execute", map[string]any{
		"sessionId": sessionID,
		"command": fmt.Sprintf(
			`head -c %d /dev/zero | tr '\0' x; echo; echo real-tail`, produced),
	})
	if truncated["outputTruncated"] != true {
		t.Fatalf("truncation flags = %#v", truncated)
	}
	stdout, _ := truncated["stdout"].(string)
	if len(stdout) > appmcp.ForegroundOutputLimit {
		t.Fatalf("foreground stdout = %d bytes, cap %d", len(stdout), appmcp.ForegroundOutputLimit)
	}
	if !strings.Contains(stdout, "real-tail") {
		t.Fatal("the retained window dropped the newest output")
	}
	base, _ := truncated["outputBaseOffset"].(float64)
	total, _ := truncated["totalOutputChars"].(float64)
	if base <= 0 || total < float64(produced) || base+float64(len(stdout)) > total+1 {
		t.Fatalf("offsets base=%v total=%v stdout=%d", base, total, len(stdout))
	}
}

// stageModeReportingFS models the Windows OpenSSH sftp-server behaviour the
// ticket-58 acceptance hit: it accepts a chmod but creates staging files as
// 0600 and reports that mode back. The underlying filesystem (and every other
// operation) is the real adapter on the real fakessh/pkg-sftp fixture.
type stageModeReportingFS struct {
	apptransfer.FileSystem
}

func (f stageModeReportingFS) Chmod(context.Context, string, uint32) error {
	// Windows OpenSSH accepts the request; the POSIX bits do not stick.
	return nil
}

func (f stageModeReportingFS) Stat(ctx context.Context, p string) (domainstransfer.Entry, error) {
	entry, err := f.FileSystem.Stat(ctx, p)
	if err == nil && apptransfer.IsStagingName(f.FileSystem.Base(p)) {
		entry.Mode = 0o600
	}
	return entry, err
}

type stageModeReportingOpener struct {
	inner apptransfer.RemoteOpener
}

func (o stageModeReportingOpener) OpenRemote(ctx context.Context, hostID model.HostID, label string) (apptransfer.FileSystem, error) {
	files, err := o.inner.OpenRemote(ctx, hostID, label)
	if err != nil {
		return nil, err
	}
	return stageModeReportingFS{FileSystem: files}, nil
}

// TestMCPWriteSFTPUploadAcceptsTheRemoteStageMode drives sftp_upload through
// the compiled stdio client and the real fixture while the destination reports
// the Windows OpenSSH staging mode (0600) for a local Windows source (0666).
// Before the ticket-58 rework this returned sftp_tool_unavailable instead of
// creating the file.
func TestMCPWriteSFTPUploadAcceptsTheRemoteStageMode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("models a local Windows source mode against a POSIX-mode destination")
	}
	fixture := newMCPRealWriteFixture(t)
	opener := stageModeReportingOpener{inner: &sftpRemoteOpener{
		manager: fixture.manager, vault: fixture.vault,
	}}
	fixture.transfer = apptransfer.NewService(adaptersftp.NewLocal(), opener)
	fixture.executor = newMCPWriteExecutor(fixture.policy, fixture.sessions,
		fixture.vault, fixture.forwarding, fixture.transfer, fixture.openHost)
	fixture.handler.attachWrites(fixture.executor)

	client, stop := realStdioClient(t, fixture)
	defer stop()

	localPath := filepath.Join(fixture.localRoot, "stage-mode.txt")
	if err := os.WriteFile(localPath, []byte("stage mode upload"), 0o644); err != nil {
		t.Fatalf("seed the local upload: %v", err)
	}
	e2eToolOK(t, client, "sftp_upload", map[string]any{
		"sessionId": string(fixture.sshSession), "localPath": localPath,
		"remotePath": fixture.remoteRoot + "/stage-mode.txt",
	})
	requireRealFile(t, filepath.Join(fixture.network.root, "stage-mode.txt"), "stage mode upload")
}
