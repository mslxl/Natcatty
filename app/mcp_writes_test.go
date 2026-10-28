package app

// Ticket 58 integration tests. They run the real application services behind
// the external MCP handler: a real SQLite Vault with encrypted field secrets, a
// real Session service (one local shell plus a fake SSH PTY), the real
// forwarding manager over a fake connection, and a fake two-pane filesystem for
// SFTP. Policy mode is auto for mapping tests; the policy matrix itself lives
// in mcp_policy_test.go and stays untouched.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/localpty"
	"github.com/mslxl/natcatty/adapters/store"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	apptransfer "github.com/mslxl/natcatty/application/transfer"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
)

// writeTestKeyRing is an in-memory platform secret store.
type writeTestKeyRing struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (k *writeTestKeyRing) Get(key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	value, ok := k.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (k *writeTestKeyRing) Set(key string, value []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[key] = append([]byte(nil), value...)
	return nil
}

func (k *writeTestKeyRing) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.values, key)
	return nil
}

// writeTestTransport is a PTY whose reads block until Close. It stands in for
// an SSH shell when a test only needs a Session with a saved Host.
type writeTestTransport struct {
	mu      sync.Mutex
	written bytes.Buffer
	done    chan struct{}
	once    sync.Once
}

func newWriteTestTransport() *writeTestTransport {
	return &writeTestTransport{done: make(chan struct{})}
}

func (t *writeTestTransport) Read([]byte) (int, error) {
	<-t.done
	return 0, io.EOF
}

func (t *writeTestTransport) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written.Write(data)
}

func (t *writeTestTransport) Resize(int, int) error { return nil }

func (t *writeTestTransport) Close() error {
	t.once.Do(func() { close(t.done) })
	return nil
}

// writeTestFactory opens the real local shell and a fresh blocking PTY for
// each SSH Session. Sharing one PTY between SSH Sessions coupled their
// lifetimes: closing one Session ended the other one's reader too.
type writeTestFactory struct {
	local *localpty.Factory
	// newSSH makes the SSH transport; nil makes a fresh blocking one. Each
	// call gets its own, so Sessions keep independent lifetimes.
	newSSH func() *writeTestTransport
}

func (f writeTestFactory) Open(ctx context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	if spec.Kind == appsession.KindSSH {
		if f.newSSH != nil {
			return f.newSSH(), nil
		}
		return newWriteTestTransport(), nil
	}
	factory := localpty.Factory{}
	return factory.Open(ctx, spec)
}

// writeFakeFS is a POSIX-notation in-memory pane.
type writeFakeFS struct {
	mu      sync.Mutex
	backing string
	files   map[string][]byte
	dirs    map[string]bool
	modes   map[string]uint32
}

func newWriteFakeFS(backing string) *writeFakeFS {
	return &writeFakeFS{
		backing: backing,
		files:   map[string][]byte{}, dirs: map[string]bool{"/": true},
		modes: map[string]uint32{},
	}
}

func (f *writeFakeFS) Root(context.Context) (string, error) { return "/", nil }

func (f *writeFakeFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir = f.clean(dir)
	if !f.dirs[dir] {
		return nil, os.ErrNotExist
	}
	entries := make([]domainsftp.Entry, 0)
	for file := range f.files {
		if f.Parent(file) == dir {
			entries = append(entries, f.entry(file))
		}
	}
	for child := range f.dirs {
		if child != dir && child != "/" && f.Parent(child) == dir {
			entries = append(entries, f.entry(child))
		}
	}
	return entries, nil
}

func (f *writeFakeFS) Stat(_ context.Context, target string) (domainsftp.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target = f.clean(target)
	if f.dirs[target] {
		return f.entry(target), nil
	}
	if _, ok := f.files[target]; ok {
		return f.entry(target), nil
	}
	return domainsftp.Entry{}, os.ErrNotExist
}

func (f *writeFakeFS) entry(target string) domainsftp.Entry {
	mode := uint32(0o644)
	kind := domainsftp.KindFile
	size := int64(0)
	if f.dirs[target] {
		mode, kind = 0o755, domainsftp.KindDir
	} else {
		size = int64(len(f.files[target]))
	}
	if stored, ok := f.modes[target]; ok {
		mode = stored
	}
	return domainsftp.Entry{
		Name: f.Base(target), Path: target, Kind: kind, Size: size, Mode: mode,
		ModTime: time.UnixMilli(1),
	}
}

func (f *writeFakeFS) Mkdir(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	target = f.clean(target)
	if f.dirs[target] {
		return os.ErrExist
	}
	f.dirs[target] = true
	return nil
}

func (f *writeFakeFS) MkdirAll(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mkdirAllLocked(f.clean(target))
	return nil
}

func (f *writeFakeFS) mkdirAllLocked(target string) {
	if f.dirs[target] {
		return
	}
	parent := f.Parent(target)
	if parent != target {
		f.mkdirAllLocked(parent)
	}
	f.dirs[target] = true
}

func (f *writeFakeFS) Open(_ context.Context, target string) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[f.clean(target)]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), data...))), nil
}

func (f *writeFakeFS) Create(_ context.Context, target string) (io.WriteCloser, error) {
	target = f.clean(target)
	f.mu.Lock()
	f.mkdirAllLocked(f.Parent(target))
	f.mu.Unlock()
	return &writeFakeWriter{fs: f, target: target}, nil
}

type writeFakeWriter struct {
	fs     *writeFakeFS
	target string
	buffer bytes.Buffer
}

func (w *writeFakeWriter) Write(data []byte) (int, error) { return w.buffer.Write(data) }

func (w *writeFakeWriter) Close() error {
	w.fs.mu.Lock()
	defer w.fs.mu.Unlock()
	w.fs.files[w.target] = append([]byte(nil), w.buffer.Bytes()...)
	return nil
}

func (f *writeFakeFS) Remove(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	target = f.clean(target)
	if f.dirs[target] {
		for child := range f.dirs {
			if child != target && strings.HasPrefix(child, target+"/") {
				return errors.New("directory not empty")
			}
		}
		if target == "/" {
			return errors.New("cannot remove root")
		}
		delete(f.dirs, target)
		return nil
	}
	if _, ok := f.files[target]; !ok {
		return os.ErrNotExist
	}
	delete(f.files, target)
	return nil
}

func (f *writeFakeFS) RemoveAll(_ context.Context, target string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	target = f.clean(target)
	for file := range f.files {
		if file == target || strings.HasPrefix(file, target+"/") {
			delete(f.files, file)
		}
	}
	for dir := range f.dirs {
		if dir == target || strings.HasPrefix(dir, target+"/") {
			delete(f.dirs, dir)
		}
	}
	return nil
}

func (f *writeFakeFS) Rename(_ context.Context, oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	oldPath, newPath = f.clean(oldPath), f.clean(newPath)
	if data, ok := f.files[oldPath]; ok {
		delete(f.files, oldPath)
		f.files[newPath] = data
		return nil
	}
	if f.dirs[oldPath] {
		delete(f.dirs, oldPath)
		f.dirs[newPath] = true
		return nil
	}
	return os.ErrNotExist
}

func (f *writeFakeFS) Chmod(_ context.Context, target string, mode uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	target = f.clean(target)
	if !f.dirs[target] {
		if _, ok := f.files[target]; !ok {
			return os.ErrNotExist
		}
	}
	f.modes[target] = mode
	return nil
}

func (f *writeFakeFS) Join(parts ...string) string { return path.Join(parts...) }

func (f *writeFakeFS) Parent(target string) string {
	target = f.clean(target)
	if target == "/" {
		return "/"
	}
	return path.Dir(target)
}

func (f *writeFakeFS) Base(target string) string { return path.Base(f.clean(target)) }

func (f *writeFakeFS) Style() apptransfer.PathStyle { return apptransfer.StylePOSIX }

func (f *writeFakeFS) Backing() string { return f.backing }

func (f *writeFakeFS) Canonical(target string) string { return f.clean(target) }

func (f *writeFakeFS) Close() error { return nil }

func (f *writeFakeFS) clean(target string) string {
	if target == "" {
		return "/"
	}
	cleaned := path.Clean(strings.ReplaceAll(target, `\`, "/"))
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

type writeFakeRemoteOpener struct{ fs *writeFakeFS }

func (o writeFakeRemoteOpener) OpenRemote(context.Context, model.HostID, string) (apptransfer.FileSystem, error) {
	return o.fs, nil
}

type writeFakeLocalOpener struct{ fs *writeFakeFS }

func (o writeFakeLocalOpener) OpenLocal(context.Context) (apptransfer.FileSystem, error) {
	return o.fs, nil
}

// writeFakeForwardListener accepts connections until Close.
type writeFakeForwardListener struct {
	closed chan struct{}
	once   sync.Once
}

func newWriteFakeForwardListener() *writeFakeForwardListener {
	return &writeFakeForwardListener{closed: make(chan struct{})}
}

func (l *writeFakeForwardListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *writeFakeForwardListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *writeFakeForwardListener) Addr() net.Addr { return writeFakeAddr("127.0.0.1:1") }

type writeFakeAddr string

func (a writeFakeAddr) Network() string { return "tcp" }
func (a writeFakeAddr) String() string  { return string(a) }

type writeFakeConnection struct{}

func (writeFakeConnection) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("not used")
}
func (writeFakeConnection) ListenTCP(string) (net.Listener, error) {
	return newWriteFakeForwardListener(), nil
}
func (writeFakeConnection) Release() {}
func (writeFakeConnection) Discard() {}

type writeFakeConnector struct{}

func (writeFakeConnector) Connect(context.Context, model.HostID) (appforwarding.Connection, error) {
	return writeFakeConnection{}, nil
}

type mcpWriteFixture struct {
	ctx      context.Context
	vault    *appvault.Service
	sessions *appsession.Service
	policy   *appmcp.Service
	executor *mcpWriteExecutor
	handler  *mcpPolicyHandler
	bridge   *mcpReadBridge

	localSession model.SessionID
	sshSession   model.SessionID
	host         domainvault.Host
	remote       *writeFakeFS
	local        *writeFakeFS
	forwarding   *appforwarding.Manager

	modeMu sync.Mutex
	mode   appmcp.Mode
}

// setMode changes the policy's current mode for tests. The app discovers the
// mode per call; the fixture's closure reads this field.
func (f *mcpWriteFixture) setMode(mode appmcp.Mode) {
	f.modeMu.Lock()
	f.mode = mode
	f.modeMu.Unlock()
}

func newMCPWriteFixture(t *testing.T) *mcpWriteFixture {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "vault.db"))
	if err != nil {
		t.Fatalf("open the Vault database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	keyRing := &writeTestKeyRing{values: map[string][]byte{}}
	secrets, err := store.OpenSecretStore(ctx, database, keyRing,
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open the secret store: %v", err)
	}
	t.Cleanup(secrets.Close)
	vault := appvault.NewService(database, appvault.WithSecrets(secrets),
		appvault.WithKeys(database), appvault.WithGroups(database),
		appvault.WithNotes(database), appvault.WithSnippets(database),
		appvault.WithIdentities(database), appvault.WithProxyProfiles(database),
		appvault.WithKnownHosts(database), appvault.WithImportStore(database))

	host, err := vault.Create(ctx, domainvault.Draft{
		Label: "write host", Hostname: "write.example", Port: 22,
		Username: "deploy", Protocol: domainvault.ProtocolSSH,
	})
	if err != nil {
		t.Fatalf("create the fixture Host: %v", err)
	}

	factory := writeTestFactory{newSSH: newWriteTestTransport}
	sessions := appsession.NewService(factory)
	t.Cleanup(sessions.CloseAll)
	localSession, err := sessions.OpenLocal(ctx)
	if err != nil {
		t.Fatalf("open the local shell: %v", err)
	}
	sshSession, err := sessions.Open(ctx, appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "write ssh", HostID: host.ID,
		Hostname: host.Hostname, Port: host.Port, Username: host.Username,
		Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the fake SSH Session: %v", err)
	}

	forwarding := appforwarding.NewManager(database, writeFakeConnector{},
		appforwarding.WithListen(func(string, string) (net.Listener, error) {
			return newWriteFakeForwardListener(), nil
		}))
	t.Cleanup(forwarding.Close)
	remote := newWriteFakeFS("fake-remote")
	local := newWriteFakeFS("fake-local")
	transfer := apptransfer.NewService(
		writeFakeLocalOpener{fs: local}, writeFakeRemoteOpener{fs: remote})

	bridge := newMCPReadBridge(vault, forwarding, sessions, transfer)
	fixture := &mcpWriteFixture{mode: appmcp.ModeAuto}
	mode := func() appmcp.Mode {
		fixture.modeMu.Lock()
		defer fixture.modeMu.Unlock()
		return fixture.mode
	}
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: "external-chat",
		Allowlist:           []string{string(localSession.ID()), string(sshSession.ID())},
		Mode:                mode,
	})
	handler := newMCPPolicyHandler(bridge, policy, sessions, sessions, nil)
	executor := newMCPWriteExecutor(policy, sessions, vault, forwarding, transfer,
		func(context.Context, domainvault.Host) (model.SessionID, error) {
			return sshSession.ID(), nil
		})
	handler.attachWrites(executor)

	fixture.ctx = ctx
	fixture.vault = vault
	fixture.sessions = sessions
	fixture.policy = policy
	fixture.executor = executor
	fixture.handler = handler
	fixture.bridge = bridge
	fixture.localSession = localSession.ID()
	fixture.sshSession = sshSession.ID()
	fixture.host = host
	fixture.remote = remote
	fixture.local = local
	fixture.forwarding = forwarding
	return fixture
}

// callTool invokes one catalog tool through the real policy handler.
func callMCPTool(t *testing.T, fixture *mcpWriteFixture, toolName string,
	arguments map[string]any) map[string]any {
	t.Helper()
	tool, ok := catalog.ByName(toolName)
	if !ok {
		t.Fatalf("catalog has no tool %q", toolName)
	}
	params, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("encode %s arguments: %v", toolName, err)
	}
	response, err := fixture.handler.Handle(fixture.ctx, tool.RPCMethod, params)
	if err != nil {
		t.Fatalf("%s transport error: %v", toolName, err)
	}
	result, ok := response.(map[string]any)
	if !ok {
		t.Fatalf("%s result = %#v", toolName, response)
	}
	t.Logf("%s -> ok=%v code=%v error=%v", toolName, result["ok"], result["code"], result["error"])
	return result
}

func requireMCPWriteOK(t *testing.T, toolName string, result map[string]any) {
	t.Helper()
	if result["ok"] != true {
		t.Fatalf("%s did not execute: %#v", toolName, result)
	}
	if code, _ := result["code"].(string); code != "" {
		t.Fatalf("%s returned code %q: %#v", toolName, code, result)
	}
}

func requireMCPWriteCode(t *testing.T, toolName string, result map[string]any, code appmcp.ErrorCode) {
	t.Helper()
	if result["ok"] == true {
		t.Fatalf("%s unexpectedly succeeded: %#v", toolName, result)
	}
	if got, _ := result["code"].(string); got != string(code) {
		t.Fatalf("%s code = %q, want %q: %#v", toolName, got, code, result)
	}
}

// ticket58UnavailableWrites is the exact set with no application behavior in
// this build: the JavaScript script subsystem and its host trigger binding.
var ticket58UnavailableWrites = map[string]bool{
	"scripts_create": true, "scripts_update": true, "scripts_delete": true,
	"scripts_run": true, "scripts_run_stop": true, "scripts_run_pause": true,
	"scripts_run_resume": true, "scripts_targets_set": true,
	"host_connect_scripts_set": true,
}

func TestMCPWriteCatalogEveryWriteMapsOrIsExplicitlyUnavailable(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	writeTools := make([]string, 0, 43)
	for _, tool := range catalog.All() {
		if tool.Policy.Write {
			writeTools = append(writeTools, tool.Name)
		}
	}
	if len(writeTools) != 43 {
		t.Fatalf("catalog write tools = %d, want 43", len(writeTools))
	}
	// Every unavailable tool answers with the typed localized code, never the
	// ticket-57 generic fallback.
	for name := range ticket58UnavailableWrites {
		result := callMCPTool(t, fixture, name, map[string]any{
			"label": "x", "content": "x", "scriptId": "x", "runId": "x",
		})
		requireMCPWriteCode(t, name, result, appmcp.CodeToolUnavailable)
		if strings.Contains(fmt.Sprint(result["error"]), "not available in this build yet") {
			t.Fatalf("%s used the ticket-57 fallback: %#v", name, result)
		}
	}
	// No other write may take the generic ticket-57 fallback either. This is
	// asserted by the mapping tests below; here the catalog is pinned so a new
	// write tool cannot be added without a ticket-58 decision.
	for _, name := range writeTools {
		if ticket58UnavailableWrites[name] || name == "session_close" {
			continue
		}
		if strings.HasPrefix(name, "scripts_") || name == "host_connect_scripts_set" {
			t.Fatalf("script tool %q is missing from the unavailable set", name)
		}
	}
	if len(ticket58UnavailableWrites) != 9 {
		t.Fatalf("unavailable writes = %d, want 9", len(ticket58UnavailableWrites))
	}
}

func TestMCPWriteVaultNotesGroupsAndSnippetsExecute(t *testing.T) {
	fixture := newMCPWriteFixture(t)

	// Host create, update, notes, and delete.
	created := callMCPTool(t, fixture, "vault_hosts_create", map[string]any{
		"hosts": `[{"hostname":"created.example","label":"created","username":"ops","port":2222,
			"group":"58/infra","tags":["a","b"],"notes":"first"}]`,
	})
	requireMCPWriteOK(t, "vault_hosts_create", created)
	createdHosts, _ := created["created"].([]map[string]any)
	if len(createdHosts) != 1 {
		t.Fatalf("created = %#v", created["created"])
	}
	hostID := fmt.Sprint(createdHosts[0]["id"])
	if hostID == "" {
		t.Fatalf("created Host has no id: %#v", created)
	}
	updated := callMCPTool(t, fixture, "vault_hosts_update", map[string]any{
		"hostId": hostID, "label": "renamed", "notes": "second", "tags": "x,y",
	})
	requireMCPWriteOK(t, "vault_hosts_update", updated)
	host, err := fixture.vault.Get(fixture.ctx, model.HostID(hostID))
	if err != nil || host.Label != "renamed" || host.Notes != "second" || len(host.Tags) != 2 {
		t.Fatalf("Host update did not reach the Vault: %+v (%v)", host, err)
	}
	notes := callMCPTool(t, fixture, "host_notes_set", map[string]any{
		"hostId": hostID, "notes": "details",
	})
	requireMCPWriteOK(t, "host_notes_set", notes)
	if host, _ = fixture.vault.Get(fixture.ctx, model.HostID(hostID)); host.Notes != "details" {
		t.Fatalf("host_notes_set wrote %q", host.Notes)
	}
	dryRun := callMCPTool(t, fixture, "vault_hosts_create", map[string]any{
		"hosts": `[{"hostname":"dry.example"}]`, "dryRun": "true",
	})
	requireMCPWriteOK(t, "vault_hosts_create", dryRun)
	if dryRun["dryRun"] != true || dryRun["createdCount"] != 1 {
		t.Fatalf("dry run = %#v", dryRun)
	}
	hostsBefore, _ := fixture.vault.List(fixture.ctx)
	imported := callMCPTool(t, fixture, "vault_hosts_import", map[string]any{
		"format": "csv", "text": "hostname,label\nimported.example,imported\n",
	})
	requireMCPWriteOK(t, "vault_hosts_import", imported)
	hostsAfter, _ := fixture.vault.List(fixture.ctx)
	if len(hostsAfter) <= len(hostsBefore) {
		t.Fatalf("vault_hosts_import wrote no Host: before=%d after=%d", len(hostsBefore), len(hostsAfter))
	}

	// Notes create, update, import, delete.
	note := callMCPTool(t, fixture, "vault_notes_create", map[string]any{
		"title": "Runbook", "content": "# Runbook\nbody", "group": "58",
	})
	requireMCPWriteOK(t, "vault_notes_create", note)
	noteID := fmt.Sprint(note["id"])
	noteUpdate := callMCPTool(t, fixture, "vault_notes_update", map[string]any{
		"noteId": noteID, "content": "changed",
	})
	requireMCPWriteOK(t, "vault_notes_update", noteUpdate)
	storedNote, err := fixture.vault.GetNote(fixture.ctx, noteID)
	if err != nil || storedNote.Content != "changed" || storedNote.Title != "Runbook" {
		t.Fatalf("Note update = %+v (%v)", storedNote, err)
	}
	noteImport := callMCPTool(t, fixture, "vault_notes_import", map[string]any{
		"fileName": "imported.md", "content": "no heading",
	})
	requireMCPWriteOK(t, "vault_notes_import", noteImport)
	importedNotes, _ := fixture.vault.ListNotes(fixture.ctx)
	if len(importedNotes) != 2 {
		t.Fatalf("notes after import = %d, want 2", len(importedNotes))
	}
	deletedNote := callMCPTool(t, fixture, "vault_notes_delete", map[string]any{"noteId": noteID})
	requireMCPWriteOK(t, "vault_notes_delete", deletedNote)
	if _, err := fixture.vault.GetNote(fixture.ctx, noteID); err == nil {
		t.Fatal("vault_notes_delete left the Note")
	}

	// Groups create, update (rename), delete with hosts.
	group := callMCPTool(t, fixture, "vault_groups_create", map[string]any{
		"path": "58/infra", "defaults": `{"username":"group-user"}`,
	})
	requireMCPWriteOK(t, "vault_groups_create", group)
	groupUpdate := callMCPTool(t, fixture, "vault_groups_update", map[string]any{
		"path": "58/infra", "newPath": "58/prod", "defaults": `{"username":"prod-user"}`,
	})
	requireMCPWriteOK(t, "vault_groups_update", groupUpdate)
	groups, _ := fixture.vault.ListGroups(fixture.ctx)
	foundGroup := false
	for _, candidate := range groups {
		if candidate.Path == "58/prod" && candidate.Username == "prod-user" {
			foundGroup = true
		}
	}
	if !foundGroup {
		t.Fatalf("group rename did not reach the Vault: %+v", groups)
	}
	groupDelete := callMCPTool(t, fixture, "vault_groups_delete", map[string]any{
		"path": "58/prod", "deleteHosts": "true",
	})
	requireMCPWriteOK(t, "vault_groups_delete", groupDelete)

	// The host in 58/infra was renamed to 58/prod with its group; deleting the
	// group with deleteHosts must remove it.
	if _, err := fixture.vault.Get(fixture.ctx, model.HostID(hostID)); err == nil {
		t.Fatal("vault_groups_delete deleteHosts left a member Host")
	}

	// Snippets create, update, run, delete.
	snippet := callMCPTool(t, fixture, "snippets_create", map[string]any{
		"label": "ticket58", "content": "echo ncmcp-snippet",
	})
	requireMCPWriteOK(t, "snippets_create", snippet)
	snippetID := fmt.Sprint(snippet["id"])
	snippetUpdate := callMCPTool(t, fixture, "snippets_update", map[string]any{
		"snippetId": snippetID, "label": "ticket58-renamed", "noAutoRun": "true",
	})
	requireMCPWriteOK(t, "snippets_update", snippetUpdate)
	run := callMCPTool(t, fixture, "snippets_run", map[string]any{
		"snippetId": snippetID, "sessionId": string(fixture.localSession),
	})
	requireMCPWriteOK(t, "snippets_run", run)
	snippetDelete := callMCPTool(t, fixture, "snippets_delete", map[string]any{
		"snippetId": snippetID,
	})
	requireMCPWriteOK(t, "snippets_delete", snippetDelete)

	hostDelete := callMCPTool(t, fixture, "vault_hosts_delete", map[string]any{"hostId": hostID})
	if hostDelete["ok"] != true {
		// The group delete already removed it; deleting again is a typed
		// not-found rather than a silent success.
		requireMCPWriteCode(t, "vault_hosts_delete", hostDelete, appmcp.CodeInvalidArgument)
	} else {
		requireMCPWriteOK(t, "vault_hosts_delete", hostDelete)
	}
}

func TestMCPWriteForwardingRulesExecute(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	created := callMCPTool(t, fixture, "portforward_rules_create", map[string]any{
		"label": "ticket58", "type": "local", "localPort": float64(28080),
		"bindAddress": "127.0.0.1", "remoteHost": "db.internal",
		"remotePort": float64(5432), "hostId": string(fixture.host.ID),
		"autoStart": "true", "autoReconnect": "true",
	})
	requireMCPWriteOK(t, "portforward_rules_create", created)
	ruleID := fmt.Sprint(created["ruleId"])
	updated := callMCPTool(t, fixture, "portforward_rules_update", map[string]any{
		"ruleId": ruleID, "label": "ticket58-updated", "localPort": float64(28081),
	})
	requireMCPWriteOK(t, "portforward_rules_update", updated)
	duplicated := callMCPTool(t, fixture, "portforward_rules_duplicate", map[string]any{
		"ruleId": ruleID,
	})
	requireMCPWriteOK(t, "portforward_rules_duplicate", duplicated)
	duplicateID := fmt.Sprint(duplicated["ruleId"])
	if duplicateID == "" || duplicateID == ruleID {
		t.Fatalf("duplicate id = %q, source = %q", duplicateID, ruleID)
	}
	started := callMCPTool(t, fixture, "portforward_start", map[string]any{"ruleId": duplicateID})
	requireMCPWriteOK(t, "portforward_start", started)
	stopped := callMCPTool(t, fixture, "portforward_stop", map[string]any{"ruleId": duplicateID})
	requireMCPWriteOK(t, "portforward_stop", stopped)
	if err := fixture.forwarding.Delete(fixture.ctx, model.ForwardingRuleID(ruleID)); err != nil {
		t.Fatalf("cleanup source rule: %v", err)
	}
	deleteResult := callMCPTool(t, fixture, "portforward_rules_delete", map[string]any{
		"ruleId": duplicateID,
	})
	requireMCPWriteOK(t, "portforward_rules_delete", deleteResult)
	views, _ := fixture.forwarding.List(fixture.ctx)
	if len(views) != 0 {
		t.Fatalf("forwarding rules remain: %+v", views)
	}
}

func TestMCPWriteSFTPToolsExecuteAgainstTheSessionHost(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	fixture.remote.files["/remote/read.txt"] = []byte("download")
	fixture.local.files["/local/upload.txt"] = []byte("upload")

	sessionID := string(fixture.sshSession)
	write := callMCPTool(t, fixture, "sftp_write_file", map[string]any{
		"sessionId": sessionID, "path": "/remote/written.txt", "content": "written",
	})
	requireMCPWriteOK(t, "sftp_write_file", write)
	if got := string(fixture.remote.files["/remote/written.txt"]); got != "written" {
		t.Fatalf("written content = %q", got)
	}
	mkdir := callMCPTool(t, fixture, "sftp_mkdir", map[string]any{
		"sessionId": sessionID, "path": "/remote/newdir",
	})
	requireMCPWriteOK(t, "sftp_mkdir", mkdir)
	if !fixture.remote.dirs["/remote/newdir"] {
		t.Fatal("sftp_mkdir did not create the directory")
	}
	rename := callMCPTool(t, fixture, "sftp_rename", map[string]any{
		"sessionId": sessionID, "oldPath": "/remote/written.txt", "newPath": "/remote/renamed.txt",
	})
	requireMCPWriteOK(t, "sftp_rename", rename)
	if _, ok := fixture.remote.files["/remote/renamed.txt"]; !ok {
		t.Fatal("sftp_rename did not move the file")
	}
	chmod := callMCPTool(t, fixture, "sftp_chmod", map[string]any{
		"sessionId": sessionID, "path": "/remote/renamed.txt", "mode": "755",
	})
	requireMCPWriteOK(t, "sftp_chmod", chmod)
	if fixture.remote.modes["/remote/renamed.txt"] != 0o755 {
		t.Fatalf("mode = %o, want 755", fixture.remote.modes["/remote/renamed.txt"])
	}
	deleteFile := callMCPTool(t, fixture, "sftp_delete", map[string]any{
		"sessionId": sessionID, "path": "/remote/renamed.txt",
	})
	requireMCPWriteOK(t, "sftp_delete", deleteFile)
	if _, ok := fixture.remote.files["/remote/renamed.txt"]; ok {
		t.Fatal("sftp_delete left the file")
	}
	download := callMCPTool(t, fixture, "sftp_download", map[string]any{
		"sessionId": sessionID, "remotePath": "/remote/read.txt", "localPath": "/local/downloaded.txt",
	})
	requireMCPWriteOK(t, "sftp_download", download)
	if got := string(fixture.local.files["/local/downloaded.txt"]); got != "download" {
		t.Fatalf("downloaded content = %q", got)
	}
	upload := callMCPTool(t, fixture, "sftp_upload", map[string]any{
		"sessionId": sessionID, "localPath": "/local/upload.txt", "remotePath": "/remote/uploaded.txt",
	})
	requireMCPWriteOK(t, "sftp_upload", upload)
	if got := string(fixture.remote.files["/remote/uploaded.txt"]); got != "upload" {
		t.Fatalf("uploaded content = %q", got)
	}
}

func TestMCPWriteTerminalExecuteStartPollStop(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	sessionID := string(fixture.localSession)
	executed := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": sessionID, "command": "echo ncmcp-58-execute",
	})
	requireMCPWriteOK(t, "terminal_execute", executed)
	if stdout, _ := executed["stdout"].(string); !strings.Contains(stdout, "ncmcp-58-execute") {
		t.Fatalf("terminal_execute stdout = %q", stdout)
	}

	started := callMCPTool(t, fixture, "terminal_start", map[string]any{
		"sessionId": sessionID, "command": "echo ncmcp-58-job",
	})
	requireMCPWriteOK(t, "terminal_start", started)
	jobID := fmt.Sprint(started["jobId"])
	if jobID == "" {
		t.Fatalf("terminal_start returned no jobId: %#v", started)
	}
	deadline := time.Now().Add(30 * time.Second)
	var polled map[string]any
	for time.Now().Before(deadline) {
		polled = callMCPTool(t, fixture, "terminal_poll", map[string]any{
			"jobId": jobID, "offset": float64(0),
		})
		if polled["ok"] == true && polled["completed"] == true {
			break
		}
		timer := time.NewTimer(20 * time.Millisecond)
		<-timer.C
	}
	if polled["completed"] != true {
		t.Fatalf("background job did not complete: %#v", polled)
	}
	if output, _ := polled["output"].(string); !strings.Contains(output, "ncmcp-58-job") {
		t.Fatalf("job output = %q", output)
	}
	if next, ok := polled["nextOffset"].(int); !ok || next <= 0 {
		t.Fatalf("job nextOffset = %v", polled["nextOffset"])
	}
	stopped := callMCPTool(t, fixture, "terminal_stop", map[string]any{"jobId": jobID})
	requireMCPWriteOK(t, "terminal_stop", stopped)
	if stopped["completed"] != true {
		t.Fatalf("terminal_stop snapshot = %#v", stopped)
	}
}

func TestMCPWriteHostOpenUsesTheRealOpener(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	reused := callMCPTool(t, fixture, "host_open", map[string]any{
		"hostId": string(fixture.host.ID), "newTab": "false",
	})
	requireMCPWriteOK(t, "host_open", reused)
	if reused["status"] != "reused" {
		t.Fatalf("host_open reuse = %#v", reused)
	}
	opened := callMCPTool(t, fixture, "host_open", map[string]any{
		"hostId": string(fixture.host.ID), "newTab": "true",
	})
	requireMCPWriteOK(t, "host_open", opened)
	if opened["status"] != "opened" || opened["sessionId"] != string(fixture.sshSession) {
		t.Fatalf("host_open = %#v", opened)
	}
}

// TestMCPWriteHostReuseDoesNotGrantSessionClose pins the original bridge's
// visibility-only reuse: seeing an existing Session is not permission to close
// it. Only a Host opened through newTab is owned by this client.
func TestMCPWriteHostReuseDoesNotGrantSessionClose(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	reused := callMCPTool(t, fixture, "host_open", map[string]any{
		"hostId": string(fixture.host.ID), "newTab": "false",
	})
	requireMCPWriteOK(t, "host_open", reused)
	closed := callMCPTool(t, fixture, "session_close", map[string]any{
		"sessionId": string(fixture.sshSession),
	})
	requireMCPWriteCode(t, "session_close", closed, appmcp.CodeSessionNotOwned)
}

func TestMCPWriteBlocklistAndRawSessions(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	blocked := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession),
		"command":   "echo ok; rm -rf /tmp/natcatty-ticket58-bypass",
	})
	requireMCPWriteCode(t, "terminal_execute", blocked, appmcp.CodeCommandBlocked)
	if blocked["rule"] != "common.rm-recursive-force" {
		t.Fatalf("blocked rule = %v", blocked["rule"])
	}
	// The blocked command never reached the shell: a following command still
	// sees a clean marker sequence.
	after := callMCPTool(t, fixture, "terminal_execute", map[string]any{
		"sessionId": string(fixture.localSession), "command": "echo after-block",
	})
	requireMCPWriteOK(t, "terminal_execute", after)
	if stdout, _ := after["stdout"].(string); !strings.Contains(stdout, "after-block") {
		t.Fatalf("shell state after block = %q", stdout)
	}

	// Serial and network-device Sessions deliberately skip the blocklist.
	rawTransport := newWriteTestTransport()
	rawFactory := writeTestFactory{newSSH: func() *writeTestTransport { return rawTransport }}
	rawSessions := appsession.NewService(rawFactory)
	t.Cleanup(rawSessions.CloseAll)
	serial, err := rawSessions.Open(fixture.ctx, appsession.StartSpec{
		Kind: appsession.KindSerial, Label: "raw serial", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the raw Session: %v", err)
	}
	executor := newMCPWriteExecutor(fixture.policy, rawSessions, fixture.vault,
		fixture.forwarding, nil, nil)
	rawResult, err := executor.execute(fixture.ctx, "terminal_execute", map[string]any{
		"sessionId": string(serial.ID()), "command": "rm -rf /tmp/natcatty-ticket58-raw",
	}, appmcp.Decision{})
	if err != nil {
		t.Fatalf("raw execution returned a blocklist error: %v", err)
	}
	if rawResult["outputMode"] != "raw" {
		t.Fatalf("raw execution result = %#v", rawResult)
	}

	// Network-device SSH Sessions use the same vendor-CLI escape hatch: their
	// peer may treat "shutdown" or "rm" as configuration commands.
	networkSessions := appsession.NewService(writeTestFactory{newSSH: newWriteTestTransport})
	t.Cleanup(networkSessions.CloseAll)
	network, err := networkSessions.Open(fixture.ctx, appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "network device", Hostname: "switch.example",
		NetworkDevice: true, Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the network-device Session: %v", err)
	}
	networkExecutor := newMCPWriteExecutor(fixture.policy, networkSessions, fixture.vault,
		fixture.forwarding, nil, nil)
	networkResult, err := networkExecutor.execute(fixture.ctx, "terminal_execute", map[string]any{
		"sessionId": string(network.ID()), "command": "rm -rf /tmp/natcatty-ticket58-network",
	}, appmcp.Decision{})
	if err != nil {
		t.Fatalf("network-device execution returned a blocklist error: %v", err)
	}
	if networkResult["outputMode"] != "raw" {
		t.Fatalf("network-device execution result = %#v", networkResult)
	}
}
