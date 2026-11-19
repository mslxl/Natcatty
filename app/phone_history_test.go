package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	atuinwire "github.com/mslxl/natcatty/adapters/atuin/wire"
	"github.com/mslxl/natcatty/adapters/store"
	historysync "github.com/mslxl/natcatty/application/historysync"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsession "github.com/mslxl/natcatty/application/session"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

// phoneAtuinServer is a loopback Atuin v18 endpoint that records uploads. It
// implements only what the pinned adapter uses, so the phone test exercises
// the production factory and the production wire format. No live account or
// credentials are involved.
type phoneAtuinServer struct {
	mu      sync.Mutex
	records map[string]atuinwire.Record
	failing bool
	posts   int
}

func newPhoneAtuinServer(t *testing.T) (*phoneAtuinServer, *httptest.Server) {
	t.Helper()
	fake := &phoneAtuinServer{records: make(map[string]atuinwire.Record)}
	server := httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(server.Close)
	return fake, server
}

func (s *phoneAtuinServer) handle(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Atuin-Version", historysync.PinnedAtuinVersion)
	writer.Header().Set("Content-Type", "application/json")
	if request.URL.Path == "/api/v0/record" && request.Method == http.MethodPost {
		s.upload(writer, request)
		return
	}
	switch request.URL.Path {
	case "/login":
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"session": "phone-loopback-session", "auth": "cli", "user_id": "user-1",
		})
	case "/api/v0/me":
		_ = json.NewEncoder(writer).Encode(map[string]string{
			"username": "alice", "id": "user-1",
		})
	case "/api/v0/capabilities":
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"version": "1",
			"capabilities": map[string]any{
				historysync.CapabilityServer:   map[string]int{"version": 1},
				historysync.CapabilityPageSize: map[string]int{"version": 1, "page_size": 100},
			},
		})
	case "/api/v0/record":
		// Record status: an empty server has no history, so the first key is
		// accepted as unverified and the user confirms it.
		_ = json.NewEncoder(writer).Encode(map[string]any{"hosts": map[string]any{}})
	case "/api/v0/record/next":
		s.readBack(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (s *phoneAtuinServer) upload(writer http.ResponseWriter, request *http.Request) {
	var records []atuinwire.Record
	if err := json.NewDecoder(request.Body).Decode(&records); err != nil {
		http.Error(writer, "bad body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts++
	if s.failing {
		http.Error(writer, "server unavailable", http.StatusInternalServerError)
		return
	}
	for _, record := range records {
		key := record.SeriesHost + "\x00" + record.Tag + "\x00" + strconv.FormatUint(record.Index, 10)
		if _, exists := s.records[key]; !exists {
			s.records[key] = record
		}
	}
	writer.WriteHeader(http.StatusOK)
}

func (s *phoneAtuinServer) readBack(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	start, _ := strconv.ParseUint(query.Get("start"), 10, 64)
	count, err := strconv.Atoi(query.Get("count"))
	if err != nil || count < 0 {
		http.Error(writer, "bad count", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	records := make([]atuinwire.Record, 0, len(s.records))
	for _, record := range s.records {
		if record.SeriesHost == query.Get("host") && record.Tag == query.Get("tag") &&
			record.Index >= start {
			records = append(records, record)
		}
	}
	s.mu.Unlock()
	sort.Slice(records, func(left, right int) bool {
		return records[left].Index < records[right].Index
	})
	if len(records) > count {
		records = records[:count]
	}
	_ = json.NewEncoder(writer).Encode(records)
}

func (s *phoneAtuinServer) setFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = failing
}

func (s *phoneAtuinServer) uploaded() []atuinwire.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]atuinwire.Record, 0, len(s.records))
	for _, record := range s.records {
		records = append(records, record)
	}
	return records
}

// phoneSessionTransport is a remote terminal whose reads block until the test
// sends output. It stands in for the SSH channel a phone opens.
type phoneSessionTransport struct {
	mu     sync.Mutex
	input  bytes.Buffer
	out    chan []byte
	closed chan struct{}
	once   sync.Once
}

func newPhoneSessionTransport() *phoneSessionTransport {
	return &phoneSessionTransport{out: make(chan []byte, 16), closed: make(chan struct{})}
}

func (t *phoneSessionTransport) Read(p []byte) (int, error) {
	select {
	case data := <-t.out:
		return copy(p, data), nil
	case <-t.closed:
		return 0, io.EOF
	}
}

func (t *phoneSessionTransport) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.Write(p)
}

func (t *phoneSessionTransport) Resize(int, int) error { return nil }

func (t *phoneSessionTransport) Close() error {
	t.once.Do(func() { close(t.closed) })
	return nil
}

func (t *phoneSessionTransport) send(output string) { t.out <- []byte(output) }

func (t *phoneSessionTransport) typed() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.String()
}

type phoneSessionFactory struct{ transport *phoneSessionTransport }

func (f phoneSessionFactory) Open(context.Context, appsession.StartSpec) (appsession.Transport, error) {
	return f.transport, nil
}

// newTestPhoneHistory builds the phone's real history graph over a temporary
// SQLite database and the same preferences and field-secret services the
// device uses.
func newTestPhoneHistory(t *testing.T, serverURL string) *phoneHistory {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	preferences := apppreferences.NewService(store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("load preferences: %v", err)
	}
	if err := preferences.UpdateHistorySettings(func(settings *domainpreferences.History) {
		settings.SyncEnabled = true
		settings.SyncAddress = serverURL
		settings.Username = "alice"
		settings.AuthMode = atuinmodel.AuthModeSession
	}); err != nil {
		t.Fatalf("save history settings: %v", err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("load terminal settings: %v", err)
	}
	secretStore, err := store.OpenSecretStore(ctx, database,
		&writeTestKeyRing{values: make(map[string][]byte)},
		store.WithArgon2Params(1, 1024, 1))
	if err != nil {
		t.Fatalf("open secret store: %v", err)
	}
	history := newPhoneHistory(database, preferences, terminal, secretStore)
	t.Cleanup(history.Stop)
	return history
}

// signIn configures the account exactly as the phone Settings page does:
// login through the account service, then save and confirm the master key.
func signInPhoneHistory(t *testing.T, history *phoneHistory, serverURL string, key []byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := history.Login(ctx, historysync.LoginRequest{
		ServerURL: serverURL, Username: "alice", Password: "hunter2",
		AuthMode: atuinmodel.AuthModeSession,
	}); err != nil {
		t.Fatalf("phone Atuin login failed: %T", err)
	}
	if _, err := history.SetMasterKey(ctx, base64.StdEncoding.EncodeToString(key), false); err != nil {
		t.Fatalf("phone master key rejected: %T", err)
	}
	if _, err := history.ConfirmUnverifiedKey(ctx); err != nil {
		t.Fatalf("phone master key confirmation failed: %T", err)
	}
	if status := history.Status(); !status.Configured {
		t.Fatalf("phone account is not configured after sign-in: %+v", status)
	}
}

// openPhoneTestSession opens a remote Session through the phone's completion
// hook and drains its view, which is what delivers OSC capture to the tap.
func openPhoneTestSession(t *testing.T, history *phoneHistory) (*appsession.Session, *phoneSessionTransport) {
	t.Helper()
	transport := newPhoneSessionTransport()
	t.Cleanup(func() { _ = transport.Close() })
	service := appsession.NewService(phoneSessionFactory{transport: transport},
		appsession.WithCommandCompletionHook(history.commandHook()))
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, HostID: "host-1", Label: "web-1",
		Hostname: "web-1.example", Username: "deploy", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open phone Session: %v", err)
	}
	view, err := session.AttachView()
	if err != nil {
		t.Fatalf("attach phone Session view: %v", err)
	}
	go func() { _, _ = io.Copy(io.Discard, view) }()
	t.Cleanup(func() { _ = view.Close(); _ = session.Close() })
	return session, transport
}

func waitForCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func recordedCommand(t *testing.T, history *phoneHistory) string {
	t.Helper()
	entries, err := history.history.Entries(context.Background())
	if err != nil {
		t.Fatalf("read phone history: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the phone did not journal the command")
	}
	return entries[len(entries)-1].Command
}

// TestPhoneCommandReachesSyncedAtuinHistory is the acceptance proof for the
// first checkbox: a command typed into a phone Session is journaled and then
// uploaded by the production sync engine to a loopback Atuin server, where the
// encrypted payload decrypts to the same command. The account configuration is
// the shared preferences document and the shared field-secret store.
func TestPhoneCommandReachesSyncedAtuinHistory(t *testing.T) {
	fake, server := newPhoneAtuinServer(t)
	key := make([]byte, domainhistory.MasterKeyBytes)
	for index := range key {
		key[index] = byte(index + 1)
	}
	history := newTestPhoneHistory(t, server.URL)
	signInPhoneHistory(t, history, server.URL, key)

	session, transport := openPhoneTestSession(t, history)
	if _, err := session.TerminalConn().Write([]byte("echo synced-from-phone\r")); err != nil {
		t.Fatalf("type phone command: %v", err)
	}
	waitForCondition(t, "the phone transport to receive the command", func() bool {
		return transport.typed() == "echo synced-from-phone\r"
	})
	transport.send("\x1b]133;D;0\x07")
	waitForCondition(t, "the command journal row", func() bool {
		entries, err := history.history.Entries(context.Background())
		return err == nil && len(entries) > 0
	})
	if got := recordedCommand(t, history); got != "echo synced-from-phone" {
		t.Fatalf("journaled command = %q, want echo synced-from-phone", got)
	}

	if _, err := history.sync.SyncNow(context.Background()); err != nil {
		t.Fatalf("phone history sync failed: %T", err)
	}

	records := fake.uploaded()
	if len(records) == 0 {
		t.Fatal("the loopback Atuin server received no records")
	}
	wireKey, err := atuinwire.KeyFromBytes(key)
	if err != nil {
		t.Fatalf("build wire key: %v", err)
	}
	found := false
	for _, record := range records {
		if record.Tag != domainhistory.RecordTagHistory {
			continue
		}
		plaintext, err := record.Decrypt(wireKey)
		if err != nil {
			t.Fatalf("decrypt uploaded phone record: %v", err)
		}
		wrapper, err := atuinwire.UnmarshalHistoryRecord(record.Version, plaintext)
		if err != nil {
			t.Fatalf("decode uploaded phone record: %v", err)
		}
		if wrapper.History != nil && wrapper.History.Command == "echo synced-from-phone" {
			found = true
		}
		if bytes.Contains(plaintext, []byte("phone-loopback-session")) {
			t.Fatal("the uploaded record carried the session token")
		}
	}
	if !found {
		t.Fatal("the command typed on the phone is absent from synced Atuin history")
	}
}

// TestPhoneSyncFailureLeavesTheSessionRunning covers the second checkbox: with
// a Session live, the server starts failing. The sync run reports the failure
// and the phone status records it, while the Session stays open and still
// accepts input.
func TestPhoneSyncFailureLeavesTheSessionRunning(t *testing.T) {
	fake, server := newPhoneAtuinServer(t)
	key := make([]byte, domainhistory.MasterKeyBytes)
	for index := range key {
		key[index] = byte(index + 1)
	}
	history := newTestPhoneHistory(t, server.URL)
	signInPhoneHistory(t, history, server.URL, key)

	session, transport := openPhoneTestSession(t, history)
	if _, err := session.TerminalConn().Write([]byte("echo stays-live\r")); err != nil {
		t.Fatalf("type phone command: %v", err)
	}
	waitForCondition(t, "the phone transport to receive the command", func() bool {
		return transport.typed() == "echo stays-live\r"
	})
	transport.send("\x1b]133;D;0\x07")
	waitForCondition(t, "the command journal row", func() bool {
		entries, err := history.history.Entries(context.Background())
		return err == nil && len(entries) > 0
	})
	if got := recordedCommand(t, history); got != "echo stays-live" {
		t.Fatalf("journaled command = %q, want echo stays-live", got)
	}

	fake.setFailing(true)
	if _, err := history.sync.SyncNow(context.Background()); err == nil {
		t.Fatal("sync reported success while the server was failing")
	}
	status := history.Status()
	if status.State == atuinmodel.SyncIdle || status.State == "" {
		t.Fatalf("sync failure is not visible in the phone status: %+v", status)
	}
	if status.Problem == atuinmodel.ProblemNone {
		t.Fatalf("sync failure carries no problem code: %+v", status)
	}

	select {
	case <-session.Done():
		t.Fatal("a sync failure ended the live Session")
	default:
	}
	if _, err := session.TerminalConn().Write([]byte("still typing\r")); err != nil {
		t.Fatalf("the live Session stopped accepting input: %v", err)
	}
	waitForCondition(t, "the Session to accept input after the failure", func() bool {
		return bytes.Contains([]byte(transport.typed()), []byte("still typing"))
	})
}

// TestPhoneHistoryUsesTheDesktopAccountConfiguration pins the third checkbox:
// the phone builds its account service from the same preferences document and
// the same secret store entries the desktop uses, and the resulting status
// names the same server and username.
func TestPhoneHistoryUsesTheDesktopAccountConfiguration(t *testing.T) {
	_, server := newPhoneAtuinServer(t)
	key := make([]byte, domainhistory.MasterKeyBytes)
	for index := range key {
		key[index] = byte(index + 1)
	}
	history := newTestPhoneHistory(t, server.URL)
	signInPhoneHistory(t, history, server.URL, key)

	settings := history.HistorySettings()
	if settings.SyncAddress != server.URL || settings.Username != "alice" {
		t.Fatalf("phone history settings = %+v, want the desktop account document", settings)
	}
	status := history.Status()
	if !status.Configured || status.ServerURL != server.URL || status.Username != "alice" {
		t.Fatalf("phone account status = %+v", status)
	}
	// The account record is the shared domainhistory.Metadata document, not a
	// phone-only copy: the identity is persisted with the same fields the
	// desktop reads back.
	metadata, err := history.metadata.HistoryMetadata(context.Background())
	if err != nil {
		t.Fatalf("read account metadata: %v", err)
	}
	if metadata.Account.ServerURL != server.URL || metadata.Account.UserID != "user-1" {
		t.Fatalf("stored account identity = %+v", metadata.Account)
	}
}
