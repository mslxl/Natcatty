//go:build conformance

package atuinconformance

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"

	atuinremote "github.com/mslxl/natcatty/adapters/atuin/remote"
	"github.com/mslxl/natcatty/adapters/atuin/wire"
	appstore "github.com/mslxl/natcatty/adapters/store"
	apphistory "github.com/mslxl/natcatty/application/history"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// The pinned release under test. The suite refuses any other version so a
// local 18.24.2 install can never be reported as pinned-version evidence.
// Keep these values in sync with scripts/atuin-conformance.sh and
// docs/atuin/conformance.md when the reference pin moves.
const (
	pinnedAtuinVersion = "18.24.5"
	pinnedAtuinCommit  = "ad5bf5c35b7788c05b8d936f5da59c9ab1ac320e"

	testUsername = "conformance"
	testPassword = "conformance-local-only"

	commandTimeout = 2 * time.Minute
	syncTimeout    = 2 * time.Minute
	serverReady    = 30 * time.Second
	processStop    = 10 * time.Second
)

// testEnv resolves and verifies the pinned binaries. A missing binary skips
// the suite: a normal `go test ./...` never compiles it, and a tagged run
// without the release artifacts is not evidence. A present but wrong version
// fails loudly instead of silently substituting whatever is installed.
type testEnv struct {
	cliBin     string
	serverBin  string
	cliVersion string
	serverVer  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	cli := strings.TrimSpace(os.Getenv("ATUIN_CLI_BIN"))
	server := strings.TrimSpace(os.Getenv("ATUIN_SERVER_BIN"))
	if cli == "" || server == "" {
		t.Skip("conformance suite needs ATUIN_CLI_BIN and ATUIN_SERVER_BIN pointing at Atuin v18.24.5; see docs/atuin/conformance.md")
	}
	env := &testEnv{cliBin: cli, serverBin: server}
	env.cliVersion = binaryVersion(t, cli)
	env.serverVer = binaryVersion(t, server)
	if !versionLine(env.cliVersion, "atuin", pinnedAtuinVersion) {
		t.Fatalf("ATUIN_CLI_BIN is %q, want atuin %s; refusing to substitute a different release", env.cliVersion, pinnedAtuinVersion)
	}
	if !versionLine(env.serverVer, "atuin-server", pinnedAtuinVersion) {
		t.Fatalf("ATUIN_SERVER_BIN is %q, want atuin-server %s; refusing to substitute a different release", env.serverVer, pinnedAtuinVersion)
	}
	if !strings.Contains(env.cliVersion, pinnedAtuinCommit) {
		t.Fatalf("ATUIN_CLI_BIN is %q; refusing evidence without pinned client commit %s", env.cliVersion, pinnedAtuinCommit)
	}
	t.Logf("pinned binaries: cli=%s; server=%s", env.cliVersion, env.serverVer)
	t.Logf("pinned client commit confirmed: %s", pinnedAtuinCommit)
	return env
}

func binaryVersion(t *testing.T, binary string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("run %s --version: %v\n%s", binary, err, out)
	}
	return strings.TrimSpace(string(out))
}

// versionLine reports whether output is "<name> <version>" followed by a
// space, an opening parenthesis, or end of string. It deliberately rejects a
// longer version that merely starts with the pinned value.
func versionLine(output, name, version string) bool {
	prefix := name + " " + version
	if !strings.HasPrefix(output, prefix) {
		return false
	}
	rest := output[len(prefix):]
	return rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "(")
}

// environment builds a clean child environment. ATUIN_* and the home/XDG
// variables are removed first so an inherited ATUIN_SESSION belonging to the
// developer's real Atuin install can never reach a test process.
func environment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "ATUIN_") || name == "HOME" ||
			strings.HasPrefix(name, "XDG_") {
			continue
		}
		values[name] = value
	}
	for name, value := range overrides {
		values[name] = value
	}
	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

// ---------------------------------------------------------------------------
// Local self-hosted Atuin server
// ---------------------------------------------------------------------------

type conformanceServer struct {
	t       *testing.T
	baseURL string
	logPath string
	cmd     *exec.Cmd
	done    chan struct{}

	exitMu  sync.Mutex
	exitErr error
}

func startConformanceServer(t *testing.T, env *testEnv) *conformanceServer {
	t.Helper()
	dir := t.TempDir()
	port := freePort(t)
	dbPath := filepath.Join(dir, "atuin-server.db")
	config := fmt.Sprintf("host = \"127.0.0.1\"\nport = %d\nopen_registration = true\ndb_uri = %q\n",
		port, "sqlite://"+dbPath)
	configPath := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write server config: %v", err)
	}
	logPath := filepath.Join(dir, "server.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create server log: %v", err)
	}
	cmd := exec.Command(env.serverBin, "start")
	cmd.Env = environment(map[string]string{"ATUIN_CONFIG_DIR": dir})
	cmd.Dir = dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	server := &conformanceServer{
		t: t, baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		logPath: logPath, cmd: cmd, done: make(chan struct{}),
	}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start pinned Atuin server: %v", err)
	}
	go func() {
		err := cmd.Wait()
		server.exitMu.Lock()
		server.exitErr = err
		server.exitMu.Unlock()
		_ = logFile.Close()
		close(server.done)
	}()
	t.Cleanup(func() { server.stop() })

	server.waitReady()
	t.Logf("self-hosted Atuin server ready at %s (sqlite database in %s)", server.baseURL, dbPath)
	return server
}

func (s *conformanceServer) waitReady() {
	s.t.Helper()
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(serverReady)
	defer deadline.Stop()
	endpoint := s.baseURL + "/api/v0/capabilities"
	for {
		select {
		case <-s.done:
			s.t.Fatalf("pinned Atuin server exited before becoming ready: %v\nserver log:\n%s",
				s.exitError(), tailFile(s.logPath, 4000))
		case <-deadline.C:
			s.t.Fatalf("pinned Atuin server did not answer %s within %s\nserver log:\n%s",
				endpoint, serverReady, tailFile(s.logPath, 4000))
		case <-ticker.C:
			response, err := client.Get(endpoint)
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
	}
}

func (s *conformanceServer) stop() {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-s.done:
		return
	case <-time.After(processStop):
	}
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	<-s.done
}

func (s *conformanceServer) exitError() error {
	s.exitMu.Lock()
	defer s.exitMu.Unlock()
	return s.exitErr
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve an ephemeral port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release the ephemeral port: %v", err)
	}
	return port
}

func tailFile(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("(could not read %s: %v)", path, err)
	}
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return string(data)
}

// status reads the server's record status through the public API.
func (s *conformanceServer) status(t *testing.T, token string) map[string]map[string]int64 {
	t.Helper()
	var document struct {
		Hosts map[string]map[string]int64 `json:"hosts"`
	}
	s.getJSON(t, token, "/api/v0/record", nil, &document)
	return document.Hosts
}

// records reads every record of one series through the public API.
func (s *conformanceServer) records(t *testing.T, token, host, tag string) []wire.Record {
	t.Helper()
	query := url.Values{
		"host":  {host},
		"tag":   {tag},
		"start": {"0"},
		"count": {"1000"},
	}
	var records []wire.Record
	s.getJSON(t, token, "/api/v0/record/next", query, &records)
	return records
}

func (s *conformanceServer) getJSON(t *testing.T, token, path string, query url.Values, target any) {
	t.Helper()
	endpoint := s.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("build GET %s: %v", path, err)
	}
	request.Header.Set("Authorization", "Token "+token)
	request.Header.Set("Atuin-Version", pinnedAtuinVersion)
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read GET %s: %v", path, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET %s returned HTTP %d: %s", path, response.StatusCode, body)
	}
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode GET %s: %v\n%s", path, err, body)
	}
}

// postRecords writes raw wire records directly to the pinned server. The
// conformance suite uses it only to seed a non-history tag, which the
// production adapter intentionally never sends or downloads.
func (s *conformanceServer) postRecords(t *testing.T, token string, records []wire.Record) {
	t.Helper()
	body, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("encode raw records: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, s.baseURL+"/api/v0/record", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build raw record POST: %v", err)
	}
	request.Header.Set("Authorization", "Token "+token)
	request.Header.Set("Atuin-Version", pinnedAtuinVersion)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("raw record POST: %v", err)
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("raw record POST returned HTTP %d: %s", response.StatusCode, responseBody)
	}
}

// ---------------------------------------------------------------------------
// Official CLI
// ---------------------------------------------------------------------------

type cliEntry struct {
	ID        string
	Command   string
	CWD       string
	Origin    string
	Session   string
	Exit      int64
	Duration  int64
	Timestamp int64
	DeletedAt int64
}

type cliClient struct {
	t         *testing.T
	bin       string
	home      string
	workDir   string
	configDir string
	envMap    map[string]string
	username  string

	sessionToken string
	masterKey    string
}

func newCLIClient(t *testing.T, env *testEnv, server *conformanceServer) *cliClient {
	t.Helper()
	root := t.TempDir()
	client := &cliClient{
		t: t, bin: env.cliBin,
		home:      filepath.Join(root, "home"),
		workDir:   filepath.Join(root, "work"),
		configDir: filepath.Join(root, "config", "atuin"),
		username:  testUsername,
	}
	for _, dir := range []string{client.home, client.workDir, client.configDir,
		filepath.Join(root, "data"), filepath.Join(root, "cache"), filepath.Join(root, "state")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create CLI directory %s: %v", dir, err)
		}
	}
	config := fmt.Sprintf("sync_address = %q\nauto_sync = false\nupdate_check = false\nfilter_mode = \"global\"\n[daemon]\nenabled = false\nautostart = false\n",
		server.baseURL)
	if err := os.WriteFile(filepath.Join(client.configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write CLI config: %v", err)
	}
	client.envMap = map[string]string{
		"HOME": client.home,
		// The CLI records the shell's PWD, not the process working directory.
		"PWD":                 client.workDir,
		"XDG_CONFIG_HOME":     filepath.Join(root, "config"),
		"XDG_DATA_HOME":       filepath.Join(root, "data"),
		"XDG_CACHE_HOME":      filepath.Join(root, "cache"),
		"XDG_STATE_HOME":      filepath.Join(root, "state"),
		"ATUIN_CONFIG_DIR":    client.configDir,
		"ATUIN_PASSWORD":      testPassword,
		"NO_COLOR":            "1",
		"TERM":                "dumb",
		"GIT_TERMINAL_PROMPT": "0",
	}
	if _, stderr := client.run("account", "register", "-u", client.username,
		"-p", testPassword, "-e", "conformance@example.test"); strings.TrimSpace(stderr) == "" {
		t.Logf("official CLI registered account %q on the local server", client.username)
	}
	client.sessionToken = client.readMeta(t, "session")
	if client.sessionToken == "" {
		t.Fatal("official CLI stored no session token")
	}
	// `atuin key --base64` exports the key in Atuin's MessagePack array16
	// encoding. Parse it with the same domain decoder the app uses for an
	// imported key, then hold the canonical raw-base64 form in the test secret
	// store.
	client.masterKey = testCanonicalMasterKey(t, strings.TrimSpace(client.output("key", "--base64")))
	t.Logf("official CLI account ready: home=%s", client.home)
	return client
}

func (client *cliClient) environment(extra map[string]string) []string {
	values := make(map[string]string, len(client.envMap)+len(extra))
	for name, value := range client.envMap {
		values[name] = value
	}
	for name, value := range extra {
		values[name] = value
	}
	return environment(values)
}

func (client *cliClient) run(args ...string) (string, string) {
	client.t.Helper()
	return client.runEnv(nil, args...)
}

func (client *cliClient) runEnv(extra map[string]string, args ...string) (string, string) {
	client.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, client.bin, args...)
	cmd.Env = client.environment(extra)
	cmd.Dir = client.workDir
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		client.t.Fatalf("atuin %v timed out after %s\nstdout:\n%s\nstderr:\n%s",
			args, commandTimeout, stdout.String(), stderr.String())
	}
	if err != nil {
		client.t.Fatalf("atuin %v failed: %v\nstdout:\n%s\nstderr:\n%s",
			args, err, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String()
}

func (client *cliClient) output(args ...string) string {
	client.t.Helper()
	stdout, _ := client.run(args...)
	return stdout
}

// addCommand records and finishes one command through the official CLI. The
// duration is in nanoseconds, which is what `atuin history end` stores.
func (client *cliClient) addCommand(command string, exit int, duration time.Duration) string {
	client.t.Helper()
	id := strings.TrimSpace(client.output("history", "start", "--", command))
	if id == "" {
		client.t.Fatalf("atuin history start %q printed no history id", command)
	}
	client.run("history", "end", "--exit", strconv.Itoa(exit),
		"--duration", strconv.FormatInt(int64(duration), 10), id)
	return id
}

func (client *cliClient) sync() {
	client.t.Helper()
	client.run("sync")
}

func (client *cliClient) entries() []cliEntry {
	client.t.Helper()
	path := filepath.Join(client.home, ".atuin", "data", "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		client.t.Fatalf("open CLI history database: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`select id, command, cwd, hostname, session, exit, duration, timestamp, coalesce(deleted_at, 0) from history`)
	if err != nil {
		client.t.Fatalf("query CLI history database: %v", err)
	}
	defer rows.Close()
	var entries []cliEntry
	for rows.Next() {
		var entry cliEntry
		if err := rows.Scan(&entry.ID, &entry.Command, &entry.CWD, &entry.Origin,
			&entry.Session, &entry.Exit, &entry.Duration, &entry.Timestamp, &entry.DeletedAt); err != nil {
			client.t.Fatalf("scan CLI history row: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		client.t.Fatalf("iterate CLI history rows: %v", err)
	}
	return entries
}

func (client *cliClient) entryByCommand(command string) (cliEntry, bool) {
	client.t.Helper()
	for _, entry := range client.entries() {
		if entry.Command == command {
			return entry, true
		}
	}
	return cliEntry{}, false
}

func (client *cliClient) readMeta(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(client.home, ".atuin", "data", "meta.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open CLI metadata database: %v", err)
	}
	defer db.Close()
	var value string
	if err := db.QueryRow(`select value from meta where key = ?`, key).Scan(&value); err != nil {
		t.Fatalf("read CLI metadata %q: %v", key, err)
	}
	return value
}

// addHistoryFilter configures the CLI's exclusion filter used by `history
// prune`. The command under test was recorded before the filter existed, so
// prune observes it as matched and writes a tombstone.
func (client *cliClient) addHistoryFilter(pattern string) {
	client.t.Helper()
	path := filepath.Join(client.configDir, "config.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		client.t.Fatalf("read CLI config: %v", err)
	}
	line := fmt.Sprintf("history_filter = [%q]\n", pattern)
	if !strings.Contains(string(data), "history_filter") {
		// The filter is a top-level key. The config already contains a
		// [daemon] table, so appending would silently scope it to that table.
		section := strings.Index(string(data), "\n[daemon]")
		if section < 0 {
			data = append(data, []byte(line)...)
		} else {
			data = append(data[:section+1], append([]byte(line), data[section+1:]...)...)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		client.t.Fatalf("write CLI config: %v", err)
	}
}

// prune deletes the CLI entries matching the configured filter and writes the
// delete records. ATUIN_SESSION is required by the CLI database layer.
func (client *cliClient) prune(session string) {
	client.t.Helper()
	client.runEnv(map[string]string{"ATUIN_SESSION": session}, "history", "prune")
}

func (client *cliClient) listCommands() []string {
	client.t.Helper()
	entries := client.entries()
	session := ""
	if len(entries) > 0 {
		session = entries[0].Session
	}
	stdout, _ := client.runEnv(map[string]string{"ATUIN_SESSION": session}, "history", "list", "--cmd-only")
	var commands []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			commands = append(commands, line)
		}
	}
	return commands
}

// ---------------------------------------------------------------------------
// Netcatty writer: real store, history service, sync engine, HTTP adapter
// ---------------------------------------------------------------------------

type testSettings struct {
	mu      sync.Mutex
	history domainpreferences.History
}

func newTestSettings(address string) *testSettings {
	history := domainpreferences.Defaults().History
	history.SyncEnabled = true
	history.SyncAddress = address
	history.AuthMode = atuinmodel.AuthModeSession
	history.SyncIntervalMinutes = 5
	return &testSettings{history: history}
}

func (settings *testSettings) HistorySettings() domainpreferences.History {
	settings.mu.Lock()
	defer settings.mu.Unlock()
	return settings.history
}

func (settings *testSettings) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	settings.mu.Lock()
	defer settings.mu.Unlock()
	change(&settings.history)
	return nil
}

type testSecrets struct {
	mu     sync.Mutex
	values map[historysync.SecretRef]string
}

func newTestSecrets(token, key string) *testSecrets {
	return &testSecrets{values: map[historysync.SecretRef]string{
		historysync.SessionTokenRef(): token,
		historysync.MasterKeyRef():    key,
	}}
}

func (secrets *testSecrets) Mode() historysync.SecretMode { return historysync.SecretModeSession }

func (secrets *testSecrets) Has(_ context.Context, ref historysync.SecretRef) (bool, error) {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()
	_, ok := secrets.values[ref]
	return ok, nil
}

func (secrets *testSecrets) Get(_ context.Context, ref historysync.SecretRef) (string, error) {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()
	value, ok := secrets.values[ref]
	if !ok {
		return "", historysync.ErrSecretNotFound
	}
	return value, nil
}

func (secrets *testSecrets) Set(_ context.Context, ref historysync.SecretRef, value string) error {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()
	secrets.values[ref] = value
	return nil
}

func (secrets *testSecrets) Delete(_ context.Context, ref historysync.SecretRef) error {
	secrets.mu.Lock()
	defer secrets.mu.Unlock()
	delete(secrets.values, ref)
	return nil
}

func (secrets *testSecrets) setMasterKey(value string) {
	_ = secrets.Set(context.Background(), historysync.MasterKeyRef(), value)
}

type netcattyWriter struct {
	t        *testing.T
	db       *appstore.Store
	history  *apphistory.Service
	engine   *historysync.Service
	secrets  *testSecrets
	writerID string
}

func newNetcattyWriter(t *testing.T, address, token, masterKey string, confirmUnverified bool) *netcattyWriter {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "natcatty.db")
	db, err := appstore.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open Netcatty store: %v", err)
	}
	writerID, err := db.EnsureWriterUUID(ctx, domainhistory.UUIDv7)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create Netcatty writer UUID: %v", err)
	}
	metadata, err := db.HistoryMetadata(ctx)
	if err != nil {
		_ = db.Close()
		t.Fatalf("read Netcatty metadata: %v", err)
	}
	metadata.Account = domainhistory.AccountIdentity{ServerURL: address}
	metadata.UnverifiedKeyConfirmed = confirmUnverified
	if err := db.SaveHistoryMetadata(ctx, metadata); err != nil {
		_ = db.Close()
		t.Fatalf("save Netcatty account identity: %v", err)
	}
	settings := newTestSettings(address)
	secrets := newTestSecrets(token, masterKey)
	engine := historysync.NewService(settings, secrets, db, atuinremote.NewFactory(atuinremote.Options{}))
	writer := &netcattyWriter{
		t: t, db: db, history: apphistory.NewService(db),
		engine: engine, secrets: secrets, writerID: writerID,
	}
	t.Cleanup(func() {
		engine.Stop()
		_ = db.Close()
	})
	t.Logf("Netcatty writer %s ready at %s (database %s)", writerID, address, dbPath)
	return writer
}

func (writer *netcattyWriter) sync() (historysync.SyncResult, error) {
	writer.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	return writer.engine.SyncNow(ctx)
}

func (writer *netcattyWriter) mustSync() historysync.SyncResult {
	writer.t.Helper()
	result, err := writer.sync()
	if err != nil {
		writer.t.Fatalf("Netcatty sync failed: %v (status %+v)", err, writer.engine.Status())
	}
	writer.t.Logf("Netcatty sync: downloaded=%d applied=%d uploaded=%d pending=%d",
		result.Downloaded, result.Applied, result.Uploaded, result.PendingUploads)
	return result
}

// record writes one command through the real history service and SQLite
// store. It returns the UUIDv7 identity the service persisted.
func (writer *netcattyWriter) record(command string, exit int, cwd string) string {
	writer.t.Helper()
	now := time.Now().UTC()
	id := domainhistory.UUIDv7()
	entry := domainhistory.Entry{
		ID:          id,
		Command:     command,
		Scope:       "local",
		CWD:         cwd,
		Shell:       "zsh",
		RecordedAt:  now,
		FinishedAt:  now.Add(40 * time.Millisecond),
		Duration:    40 * time.Millisecond,
		ExitCode:    exit,
		HasExitCode: true,
		Origin:      "conformance-host:conformance-user",
		Author:      "conformance-user",
		AuthorKind:  domainhistory.AuthorKindUser,
		Source:      domainhistory.SourceSession,
	}
	if err := writer.history.Record(context.Background(), entry); err != nil {
		writer.t.Fatalf("record Netcatty command %q: %v", command, err)
	}
	return id
}

func (writer *netcattyWriter) delete(id string) {
	writer.t.Helper()
	if err := writer.history.DeleteEntry(context.Background(), id); err != nil {
		writer.t.Fatalf("delete Netcatty command %s: %v", id, err)
	}
}

func (writer *netcattyWriter) liveEntries() []domainhistory.Entry {
	writer.t.Helper()
	entries, err := writer.db.ListHistoryEntries(context.Background())
	if err != nil {
		writer.t.Fatalf("list Netcatty live entries: %v", err)
	}
	return entries
}

func (writer *netcattyWriter) entry(id string) (domainhistory.Entry, bool) {
	writer.t.Helper()
	entry, found, err := writer.db.HistoryEntry(context.Background(), id)
	if err != nil {
		writer.t.Fatalf("read Netcatty entry %s: %v", id, err)
	}
	return entry, found
}

func (writer *netcattyWriter) records() []domainhistory.Record {
	writer.t.Helper()
	records, err := writer.db.ListHistoryRecords(context.Background())
	if err != nil {
		writer.t.Fatalf("list Netcatty records: %v", err)
	}
	return records
}

func (writer *netcattyWriter) metadata() domainhistory.Metadata {
	writer.t.Helper()
	metadata, err := writer.db.HistoryMetadata(context.Background())
	if err != nil {
		writer.t.Fatalf("read Netcatty metadata: %v", err)
	}
	return metadata
}

func (writer *netcattyWriter) pendingUploads() int {
	writer.t.Helper()
	pending := 0
	for _, record := range writer.records() {
		if record.SeriesHost == writer.writerID &&
			record.Tag == domainhistory.RecordTagHistory &&
			record.Direction == domainhistory.RecordDirectionLocal &&
			record.UploadedAt.IsZero() {
			pending++
		}
	}
	return pending
}

// ---------------------------------------------------------------------------
// Shared assertions and helpers
// ---------------------------------------------------------------------------

func uniqueCommand(prefix string) string {
	suffix := strings.ReplaceAll(domainhistory.UUIDv7(), "-", "")
	return prefix + "-" + suffix[:10]
}

func commandsOf(entries []domainhistory.Entry) map[string]domainhistory.Entry {
	result := make(map[string]domainhistory.Entry, len(entries))
	for _, entry := range entries {
		result[entry.Command] = entry
	}
	return result
}

func commandsOfCLI(entries []cliEntry) map[string]cliEntry {
	result := make(map[string]cliEntry, len(entries))
	for _, entry := range entries {
		result[entry.Command] = entry
	}
	return result
}

func requireCommand(t *testing.T, entries map[string]domainhistory.Entry, command string) domainhistory.Entry {
	t.Helper()
	entry, ok := entries[command]
	if !ok {
		t.Fatalf("command %q is missing; present: %v", command, mapKeys(entries))
	}
	return entry
}

func mapKeys(entries map[string]domainhistory.Entry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	return keys
}

func requireNoDuplicateIDs(t *testing.T, entries []domainhistory.Entry) {
	t.Helper()
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if seen[entry.ID] {
			t.Fatalf("duplicate history id %s in %d entries", entry.ID, len(entries))
		}
		seen[entry.ID] = true
	}
}

func requireSeries(t *testing.T, metadata domainhistory.Metadata, writer string) domainhistory.SeriesCheckpoint {
	t.Helper()
	checkpoint, ok := metadata.Series[domainhistory.SeriesKey(writer, domainhistory.RecordTagHistory)]
	if !ok {
		t.Fatalf("no history checkpoint for writer series %s; checkpoints: %v", writer, metadata.Series)
	}
	return checkpoint
}

func seriesCount(status map[string]map[string]int64) int {
	count := 0
	for _, tags := range status {
		count += len(tags)
	}
	return count
}

func decodeServerHistories(t *testing.T, encodedKey string, records []wire.Record) []wire.HistoryRecord {
	t.Helper()
	masterKey := testWireKey(t, encodedKey)
	var histories []wire.HistoryRecord
	for _, record := range records {
		plaintext, err := record.Decrypt(masterKey)
		if err != nil {
			t.Fatalf("decrypt server record %s/%d: %v", record.SeriesHost, record.Index, err)
		}
		history, err := wire.UnmarshalHistoryRecord(record.Version, plaintext)
		if err != nil {
			t.Fatalf("decode server record %s/%d: %v", record.SeriesHost, record.Index, err)
		}
		histories = append(histories, history)
	}
	return histories
}

func escapedCommandPattern(command string) string {
	return "^" + regexp.QuoteMeta(command) + "$"
}

// testCanonicalMasterKey accepts either key export form the official CLI may
// print and returns the canonical raw-base64 form. It also proves the export
// decodes to exactly one 32-byte Atuin key.
func testCanonicalMasterKey(t *testing.T, exported string) string {
	t.Helper()
	raw, err := domainhistory.ParseMasterKey(exported)
	if err != nil || len(raw) != 32 {
		t.Fatalf("parse exported master key: %v (bytes=%d)", err, len(raw))
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	domainhistory.WipeMasterKey(raw)
	return encoded
}

// testWireKey maps a stored master key to the wire key type used by records.
func testWireKey(t *testing.T, encoded string) wire.Key {
	t.Helper()
	raw, err := domainhistory.ParseMasterKey(encoded)
	if err != nil {
		t.Fatalf("decode master key: %v", err)
	}
	key, err := wire.KeyFromBytes(raw)
	if err != nil {
		t.Fatalf("parse master key: %v", err)
	}
	domainhistory.WipeMasterKey(raw)
	return key
}
