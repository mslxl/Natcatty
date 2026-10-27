//go:build selftest

package app

// Ticket 49 self-test. Press F11, then 4, then 9.
//
// The check runs the real sync stack headlessly against an in-process fake
// GitHub server; no real network and no window are involved:
//
//   - the original Electron writer's fixed envelope vector decrypts and
//     re-encrypts byte-for-byte, and its master-key verifier matches;
//   - GitHub's device flow authorizes a token and resolves the login;
//   - a manual upload publishes the Vault and its encrypted secret, a newer
//     cloud copy downloads into the real Vault state, the running list picks
//     it up on the next frame, and the following sync is a no-op;
//   - a Vault edit schedules the debounced automatic sync (twice);
//   - a lost conditional write (412) surfaces a durable conflict without
//     writing, and choosing the cloud copy downloads it;
//   - an emptied local Vault is blocked as a tombstone sync for both manual
//     and automatic triggers without changing the cloud bytes.
//
// The scenario runs in one goroutine so the event loop is never blocked. The
// first step polls its result channel with a hard 30 s deadline; later steps
// assert one area each. A repeated F11,4,9 starts from a clean scenario.
//
// Result: %TEMP%\natcatty-selftest-49.json.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adaptergithub "github.com/mslxl/natcatty/adapters/github"
	adapterstore "github.com/mslxl/natcatty/adapters/store"
	adaptersyncbackup "github.com/mslxl/natcatty/adapters/syncbackup"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	adapterbackup "github.com/mslxl/natcatty/adapters/vaultbackup"
	appsync "github.com/mslxl/natcatty/application/sync"
	appvault "github.com/mslxl/natcatty/application/vault"
	appbackup "github.com/mslxl/natcatty/application/vaultbackup"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/selftest"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

const (
	selftest49Ticket = 49
	// selftest49Timeout bounds the whole headless scenario. Every wait inside
	// it also observes the same deadline.
	selftest49Timeout = 30 * time.Second
	// selftest49Debounce is short enough for acceptance but still a real
	// timer, so the check exercises the debounce path rather than calling
	// Sync directly.
	selftest49Debounce = 25 * time.Millisecond
	selftest49Password = "ticket-49 master password"
)

type selftest49 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest49Result
	report   selftest49Report
	err      error
}

type selftest49Report struct {
	VectorBytes        int
	VectorPlaintext    bool
	VectorCipher       bool
	VectorVerifier     bool
	DeviceUserCode     string
	DeviceLogin        string
	TokenStored        bool
	UploadAction       string
	UploadHost         string
	UploadSecret       string
	DownloadAction     string
	DownloadHost       string
	ViewStaleHost      string
	ViewFreshHost      string
	ViewNotifications  int
	NoOpAction         string
	AutoFirstHost      string
	AutoSecondHost     string
	AutoWrites         int
	ConflictSurfaced   bool
	ConflictAction     string
	ConflictLocalHost  string
	ConflictRemoteHost string
	ConflictAttempts   int
	ConflictWrites     int
	EmptyManualError   string
	EmptyAutoError     string
	EmptyProblem       string
	EmptyCloudBytes    int
	EmptyCloudHost     string
	EmptyWrites        int
}

type selftest49Result struct {
	report selftest49Report
	err    error
}

func (t *selftest49) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest49Ticket,
		Name:   "Sync core and GitHub Gist",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "original-envelope-format", Run: t.vectorStep},
			{Name: "device-flow-authorization", Run: t.deviceStep},
			{Name: "manual-sync-up-and-down", Run: t.manualStep},
			{Name: "debounced-automatic-sync", Run: t.autoStep},
			{Name: "conflict-last-write-wins", Run: t.conflictStep},
			{Name: "empty-vault-overwrite-guard", Run: t.emptyStep},
		},
	}
}

// scenarioStep starts the scenario once and polls its result. A completed
// previous run is reset first, because the harness reuses the registered
// check for a repeated F11,4,9.
func (t *selftest49) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest49Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest49Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest49Timeout)
		t.cancel = cancel
		t.result = make(chan selftest49Result, 1)
		go func() {
			report, err := runSelftest49(ctx)
			t.result <- selftest49Result{report: report, err: err}
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
		return selftest.Passed(
			"fake GitHub: envelope vector, device flow, manual up/down with a live Vault list refresh, two debounced writes, conflict download, empty guard",
			"adapters/github + application/sync + adapters/syncvault + ui/vault/state")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the sync scenario did not finish in %s", selftest49Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless sync scenario")
	}
}

func (t *selftest49) vectorStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.VectorPlaintext {
		return selftest.Failed(errors.New("the original vector plaintext did not decrypt"))
	}
	if !report.VectorCipher || report.VectorBytes == 0 {
		return selftest.Failed(errors.New("the re-encrypted envelope is not byte-identical to the original vector"))
	}
	if !report.VectorVerifier {
		return selftest.Failed(errors.New("the original master-key verification hash did not match"))
	}
	return selftest.Passed(fmt.Sprintf(
		"decrypted and re-encrypted %d bytes identically; PBKDF2-SHA-256 verifier matched",
		report.VectorBytes),
		"domain/sync.OriginalFormatVector + EncryptBytes/MarshalFile")
}

func (t *selftest49) deviceStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.DeviceUserCode != "ABCD-1234" {
		return selftest.Failed(fmt.Errorf("user code = %q", report.DeviceUserCode))
	}
	if report.DeviceLogin != "selftest-user" || !report.TokenStored {
		return selftest.Failed(fmt.Errorf("login = %q tokenStored = %v",
			report.DeviceLogin, report.TokenStored))
	}
	return selftest.Passed(
		"device code issued, token exchanged, /user resolved selftest-user, token persisted",
		"adapters/github.Client device flow + httptest fake GitHub")
}

func (t *selftest49) manualStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.UploadAction != string(domainsync.ActionUpload) {
		return selftest.Failed(fmt.Errorf("first sync action = %q", report.UploadAction))
	}
	if report.UploadHost != "initial-host" || report.UploadSecret != "initial-secret" {
		return selftest.Failed(fmt.Errorf("uploaded host = %q secret = %q",
			report.UploadHost, report.UploadSecret))
	}
	if report.DownloadAction != string(domainsync.ActionDownload) || report.DownloadHost != "cloud-host" {
		return selftest.Failed(fmt.Errorf("download action = %q host = %q",
			report.DownloadAction, report.DownloadHost))
	}
	if report.NoOpAction != string(domainsync.ActionNone) {
		return selftest.Failed(fmt.Errorf("sync after download = %q, want none", report.NoOpAction))
	}
	if report.ViewNotifications != 1 {
		return selftest.Failed(fmt.Errorf("applied-snapshot notifications = %d, want 1", report.ViewNotifications))
	}
	if report.ViewStaleHost != "initial-host" || report.ViewFreshHost != "cloud-host" {
		return selftest.Failed(fmt.Errorf("live Vault list = %q before the frame, %q after, want initial-host then cloud-host",
			report.ViewStaleHost, report.ViewFreshHost))
	}
	return selftest.Passed(
		"upload carried the Host and its encrypted password; newer cloud copy applied and the live Vault list refreshed in the same process; follow-up no-op",
		"application/sync.WithAppliedSnapshot + ui/vault/state.InvalidateLists/FlushLists")
}

func (t *selftest49) autoStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.AutoFirstHost != "auto-one" || report.AutoSecondHost != "auto-two" {
		return selftest.Failed(fmt.Errorf("debounced writes carried %q then %q",
			report.AutoFirstHost, report.AutoSecondHost))
	}
	if report.AutoWrites < 2 {
		return selftest.Failed(fmt.Errorf("automatic writes = %d, want at least 2", report.AutoWrites))
	}
	return selftest.Passed(
		"two Vault edits became two automatic uploads after the debounce interval",
		"application/sync.NotifyLocalChange debounce + adapters/store change hook")
}

func (t *selftest49) conflictStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.ConflictAttempts < 1 {
		return selftest.Failed(errors.New("the conditional write never conflicted"))
	}
	if !report.ConflictSurfaced {
		return selftest.Failed(errors.New("the lost conditional write did not surface a durable conflict"))
	}
	if report.ConflictAction != string(domainsync.ActionDownload) {
		return selftest.Failed(fmt.Errorf("conflict resolution action = %q, want download", report.ConflictAction))
	}
	if report.ConflictLocalHost != "conflict-cloud" || report.ConflictRemoteHost != "conflict-cloud" {
		return selftest.Failed(fmt.Errorf("local host = %q cloud host = %q",
			report.ConflictLocalHost, report.ConflictRemoteHost))
	}
	if report.ConflictWrites != 0 {
		return selftest.Failed(fmt.Errorf("successful writes during conflict = %d, want 0", report.ConflictWrites))
	}
	return selftest.Passed(
		"If-Match 412 surfaced a durable conflict without a write; choosing the cloud copy downloaded it, and the next sync was a no-op",
		"adapters/github conditional PATCH + application/sync.ResolveConflict")
}

func (t *selftest49) emptyStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.EmptyManualError != "tombstone guard" || report.EmptyAutoError != "tombstone guard" {
		return selftest.Failed(fmt.Errorf("manual error = %q automatic error = %q",
			report.EmptyManualError, report.EmptyAutoError))
	}
	if report.EmptyProblem != string(appsync.ProblemTombstoneGuard) {
		return selftest.Failed(fmt.Errorf("problem = %q, want %q",
			report.EmptyProblem, appsync.ProblemTombstoneGuard))
	}
	if report.EmptyCloudBytes == 0 || report.EmptyCloudHost != "conflict-cloud" {
		return selftest.Failed(fmt.Errorf("cloud bytes = %d host = %q",
			report.EmptyCloudBytes, report.EmptyCloudHost))
	}
	if report.EmptyWrites != 0 {
		return selftest.Failed(fmt.Errorf("writes while empty = %d, want 0", report.EmptyWrites))
	}
	return selftest.Passed(fmt.Sprintf(
		"the tombstone guard refused manual and automatic sync; %d cloud bytes unchanged", report.EmptyCloudBytes),
		"application/sync detectTombstone before any remote write")
}

// selftest49KeyStore is the in-memory credential store. The field-secret
// adapter gets an unsupported store so it can switch to session-only mode and
// never touches the developer's keychain; the token store gets a working map.
type selftest49KeyStore struct {
	mu          sync.Mutex
	values      map[string][]byte
	unsupported bool
}

func (s *selftest49KeyStore) Get(key string) ([]byte, error) {
	if s.unsupported {
		return nil, platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return nil, platformsecrets.ErrNotFound
	}
	return append([]byte(nil), value...), nil
}

func (s *selftest49KeyStore) Set(key string, value []byte) error {
	if s.unsupported {
		return platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *selftest49KeyStore) Delete(key string) error {
	if s.unsupported {
		return platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// selftest49GitHub is the fake GitHub server state. It implements the device
// endpoints and one Gist with conditional reads and writes.
type selftest49GitHub struct {
	mu           sync.Mutex
	content      []byte
	etag         string
	sequence     int
	writes       int
	conflicts    int
	conflictNext bool
	conflictWith []byte
}

type selftest49GistFile struct {
	Filename  string `json:"filename"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated,omitempty"`
	Size      int    `json:"size,omitempty"`
}

type selftest49Gist struct {
	ID          string                        `json:"id,omitempty"`
	Description string                        `json:"description,omitempty"`
	UpdatedAt   string                        `json:"updated_at,omitempty"`
	Files       map[string]selftest49GistFile `json:"files,omitempty"`
}

func (g *selftest49GitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/login/device/code":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "device-selftest",
			"user_code":        "ABCD-1234",
			"verification_uri": "https://github.test/login/device",
			"expires_in":       900,
			"interval":         1,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/login/oauth/access_token":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-selftest",
			"token_type":   "bearer",
			"scope":        "gist read:user",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"login": "selftest-user"})
	case r.Method == http.MethodGet && r.URL.Path == "/gists":
		g.mu.Lock()
		gists := []selftest49Gist{}
		if g.content != nil {
			gists = append(gists, g.listLocked())
		}
		g.mu.Unlock()
		_ = json.NewEncoder(w).Encode(gists)
	case r.Method == http.MethodPost && r.URL.Path == "/gists":
		var body struct {
			Files map[string]struct {
				Content string `json:"content"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.content = []byte(body.Files[domainsync.SyncFileName].Content)
		g.sequence++
		g.etag = fmt.Sprintf("etag-%d", g.sequence)
		g.writes++
		etag := g.etag
		g.mu.Unlock()
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(selftest49Gist{ID: "gist-selftest"})
	case r.Method == http.MethodGet && r.URL.Path == "/gists/gist-selftest":
		g.mu.Lock()
		if g.content == nil {
			g.mu.Unlock()
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("If-None-Match") == g.etag {
			g.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
		gist := g.listLocked()
		gist.ID = "gist-selftest"
		gist.Description = domainsync.GistDescription
		gist.UpdatedAt = "2026-01-01T00:00:00Z"
		gist.Files = map[string]selftest49GistFile{domainsync.SyncFileName: {
			Filename: domainsync.SyncFileName,
			Content:  string(g.content),
			Size:     len(g.content),
		}}
		etag := g.etag
		g.mu.Unlock()
		w.Header().Set("ETag", etag)
		_ = json.NewEncoder(w).Encode(gist)
	case r.Method == http.MethodPatch && r.URL.Path == "/gists/gist-selftest":
		var body struct {
			Files map[string]struct {
				Content string `json:"content"`
			} `json:"files"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		if g.conflictNext {
			g.conflictNext = false
			g.conflicts++
			g.content = append([]byte(nil), g.conflictWith...)
			g.sequence++
			g.etag = fmt.Sprintf("etag-%d", g.sequence)
			g.mu.Unlock()
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if match := r.Header.Get("If-Match"); match != "" && match != g.etag {
			g.mu.Unlock()
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		g.content = []byte(body.Files[domainsync.SyncFileName].Content)
		g.sequence++
		g.etag = fmt.Sprintf("etag-%d", g.sequence)
		g.writes++
		etag := g.etag
		g.mu.Unlock()
		w.Header().Set("ETag", etag)
		_ = json.NewEncoder(w).Encode(selftest49Gist{ID: "gist-selftest"})
	default:
		http.NotFound(w, r)
	}
}

func (g *selftest49GitHub) listLocked() selftest49Gist {
	return selftest49Gist{
		ID:          "gist-selftest",
		Description: domainsync.GistDescription,
		Files: map[string]selftest49GistFile{
			domainsync.SyncFileName: {Filename: domainsync.SyncFileName},
		},
	}
}

func (g *selftest49GitHub) snapshot() ([]byte, int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]byte(nil), g.content...), g.writes, g.conflicts
}

func (g *selftest49GitHub) setRemote(content []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.content = append([]byte(nil), content...)
	g.sequence++
	g.etag = fmt.Sprintf("etag-%d", g.sequence)
}

func (g *selftest49GitHub) armConflict(content []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.conflictNext = true
	g.conflictWith = append([]byte(nil), content...)
}

// runSelftest49 executes the whole headless scenario. It owns every temporary
// resource and releases them before returning.
func runSelftest49(ctx context.Context) (report selftest49Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-49-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	github := &selftest49GitHub{}
	server := httptest.NewServer(github)
	defer server.Close()

	database, err := adapterstore.Open(ctx, filepath.Join(directory, "vault.db"))
	if err != nil {
		return report, fmt.Errorf("open the SQLite Vault: %w", err)
	}
	defer database.Close()

	secrets, err := adapterstore.OpenSecretStore(ctx, database, &selftest49KeyStore{unsupported: true})
	if err != nil {
		return report, fmt.Errorf("open the field-secret store: %w", err)
	}
	defer secrets.Close()
	if err := secrets.UseSessionOnly(ctx); err != nil {
		return report, fmt.Errorf("switch the field-secret store to session-only: %w", err)
	}

	tokens := adaptersyncstore.NewTokenStore(&selftest49KeyStore{values: map[string][]byte{}})
	configs := adaptersyncstore.OpenConfig(filepath.Join(directory, "cloud-sync.json"))
	remote := adaptergithub.New(adaptergithub.Options{
		APIBase:  server.URL,
		AuthBase: server.URL,
		ClientID: "selftest-client",
	})
	vault := adaptersyncvault.New(database, secrets)
	// A live Vault state mirrors the running UI. The download step proves the
	// applied-snapshot notification refreshes its lists in this same process,
	// without a restart, and that the UI thread applies it on the next frame.
	vaultService := appvault.NewService(database, appvault.WithSecrets(secrets))
	vaultView := vaultstate.NewState(vaultService)
	viewNotifications := 0
	// Conflict resolution requires a backup port; use the real local backup
	// service so this self-test covers the protective restore point too.
	backupStore := adapterbackup.Open(filepath.Join(directory, "backups"), &selftest49KeyStore{values: map[string][]byte{}})
	backupService := appbackup.NewService(database, backupStore, appbackup.WithRetentionStore(backupStore))
	backupService.Start()
	defer backupService.Stop()
	service := appsync.NewService(remote, vault, configs, tokens,
		appsync.WithDebounce(selftest49Debounce),
		appsync.WithDevice("selftest-device", "Self-test Device", "selftest"),
		appsync.WithBackups(adaptersyncbackup.New(backupService)),
		appsync.WithAppliedSnapshot(func() {
			viewNotifications++
			vaultView.InvalidateLists()
		}),
	)
	defer service.Stop()
	defer service.ClearPassword()
	if err := service.Load(ctx); err != nil {
		return report, fmt.Errorf("load the sync profile: %w", err)
	}

	// The production composition root routes committed Vault writes into the
	// debounced automatic sync; mirror that seam here.
	database.SetChangeHook(service.NotifyLocalChange)
	defer database.SetChangeHook(nil)

	// 1. The original writer's ciphertext must still open and re-seal
	// identically, and its verifier must accept the same password.
	vector := domainsync.OriginalFormatVector()
	envelope, err := domainsync.UnmarshalFile([]byte(vector.Envelope))
	if err != nil {
		return report, fmt.Errorf("parse the original envelope: %w", err)
	}
	plaintext, err := domainsync.DecryptPayload(envelope, vector.Password)
	if err != nil {
		return report, fmt.Errorf("decrypt the original envelope: %w", err)
	}
	report.VectorPlaintext = string(plaintext) == vector.Plaintext
	if !report.VectorPlaintext {
		return report, errors.New("the original envelope decrypted to different plaintext")
	}
	sealed, err := domainsync.EncryptBytes(plaintext, vector.Password, domainsync.EncryptOptions{
		Version:    vector.Meta.Version,
		UpdatedAt:  vector.Meta.UpdatedAt,
		DeviceID:   vector.Meta.DeviceID,
		DeviceName: vector.Meta.DeviceName,
		AppVersion: vector.Meta.AppVersion,
		Salt:       vector.Salt,
		IV:         vector.IV,
	})
	if err != nil {
		return report, fmt.Errorf("re-encrypt the original plaintext: %w", err)
	}
	resealed, err := domainsync.MarshalFile(sealed)
	if err != nil {
		return report, fmt.Errorf("serialize the re-encrypted envelope: %w", err)
	}
	report.VectorBytes = len(resealed)
	report.VectorCipher = bytes.Equal(resealed, []byte(vector.Envelope))
	if !report.VectorCipher {
		return report, errors.New("the re-encrypted envelope differs from the original vector")
	}
	key, err := domainsync.DeriveKey(vector.Password, vector.Salt, vector.Meta.KDFIterations)
	if err != nil {
		return report, fmt.Errorf("derive the original master key: %w", err)
	}
	sum := sha256.Sum256(key)
	report.VectorVerifier = base64.StdEncoding.EncodeToString(sum[:]) == vector.VerificationHash
	if !report.VectorVerifier {
		return report, errors.New("the original master-key verifier did not match")
	}

	// 2. Device flow against the fake GitHub server.
	var shown appsync.DeviceCode
	if err := service.Connect(ctx, selftest49Password, func(code appsync.DeviceCode) {
		shown = code
	}); err != nil {
		return report, fmt.Errorf("device-flow authorization: %w", err)
	}
	report.DeviceUserCode = shown.UserCode
	report.DeviceLogin = service.Snapshot().Login
	token, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the authorized token: %w", err)
	}
	report.TokenStored = token.AccessToken == "token-selftest"

	// 3. Manual upload: a Host plus its encrypted field secret.
	initial := selftest49Host("initial-host", time.Now())
	initial.HasPassword = true
	if err := database.Save(ctx, initial); err != nil {
		return report, fmt.Errorf("save the initial Host: %w", err)
	}
	if err := secrets.Set(ctx, appvault.HostSecretRef(initial.ID, appvault.FieldPassword), "initial-secret"); err != nil {
		return report, fmt.Errorf("store the initial password: %w", err)
	}
	uploaded, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual upload: %w", err)
	}
	report.UploadAction = string(uploaded.Action)
	remoteContent, _, _ := github.snapshot()
	remotePayload, err := selftest49Payload(remoteContent)
	if err != nil {
		return report, fmt.Errorf("decode the uploaded cloud copy: %w", err)
	}
	report.UploadHost = selftest49Label(remotePayload)
	if len(remotePayload.Secrets) > 0 {
		report.UploadSecret = remotePayload.Secrets[0].Value
	}
	// The running Vault surface loaded the local state before the sync.
	if err := vaultView.Reload(); err != nil {
		return report, fmt.Errorf("reload the live Vault state: %w", err)
	}
	if got := selftest49FirstLabel(vaultView.Items()); got != "initial-host" {
		return report, fmt.Errorf("live Vault list before download = %q, want initial-host", got)
	}

	// 4. Manual download: a newer cloud copy replaces the local families.
	cloudTime := time.Now().Add(time.Hour)
	cloudContent, err := selftest49Envelope(selftest49Host("cloud-host", cloudTime), cloudTime)
	if err != nil {
		return report, err
	}
	github.setRemote(cloudContent)
	downloaded, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual download: %w", err)
	}
	report.DownloadAction = string(downloaded.Action)
	hosts, err := database.List(ctx)
	if err != nil {
		return report, fmt.Errorf("list the downloaded Hosts: %w", err)
	}
	report.DownloadHost = selftest49FirstLabel(hosts)
	// The applied-snapshot notification must have marked the live state stale
	// without touching its list off the UI thread. The scenario thread now
	// stands in for the next UI frame and applies the reload.
	report.ViewNotifications = viewNotifications
	report.ViewStaleHost = selftest49FirstLabel(vaultView.Items())
	vaultView.FlushLists()
	report.ViewFreshHost = selftest49FirstLabel(vaultView.Items())
	noop, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("sync after download: %w", err)
	}
	report.NoOpAction = string(noop.Action)

	// 5. Automatic sync, scheduled by the Vault change hook and the real
	// debounce timer.
	if err := service.SetAutoSync(ctx, true); err != nil {
		return report, fmt.Errorf("enable automatic sync: %w", err)
	}
	_, before, _ := github.snapshot()
	if err := database.Save(ctx, selftest49Host("auto-one", time.Now())); err != nil {
		return report, fmt.Errorf("save the first automatic Host: %w", err)
	}
	if err := selftest49Wait(ctx, func() bool {
		_, writes, _ := github.snapshot()
		return writes > before
	}); err != nil {
		return report, fmt.Errorf("wait for the first automatic sync: %w", err)
	}
	autoOne, writesOne, _ := github.snapshot()
	firstPayload, err := selftest49Payload(autoOne)
	if err != nil {
		return report, fmt.Errorf("decode the first automatic upload: %w", err)
	}
	report.AutoFirstHost = selftest49Contains(firstPayload, "auto-one")
	if err := database.Save(ctx, selftest49Host("auto-two", time.Now())); err != nil {
		return report, fmt.Errorf("save the second automatic Host: %w", err)
	}
	if err := selftest49Wait(ctx, func() bool {
		_, writes, _ := github.snapshot()
		return writes > writesOne
	}); err != nil {
		return report, fmt.Errorf("wait for the second automatic sync: %w", err)
	}
	autoTwo, writesTwo, _ := github.snapshot()
	secondPayload, err := selftest49Payload(autoTwo)
	if err != nil {
		return report, fmt.Errorf("decode the second automatic upload: %w", err)
	}
	report.AutoSecondHost = selftest49Contains(secondPayload, "auto-two")
	report.AutoWrites = writesTwo - before

	// 6. A conditional write that loses must surface a durable conflict and
	// leave the cloud bytes alone. Choosing the cloud copy then downloads the
	// newer copy instead of overwriting it.
	if err := service.SetAutoSync(ctx, false); err != nil {
		return report, fmt.Errorf("disable automatic sync: %w", err)
	}
	service.CancelDebounce()
	if err := database.Save(ctx, selftest49Host("conflict-local", time.Now())); err != nil {
		return report, fmt.Errorf("save the conflicting Host: %w", err)
	}
	conflictTime := time.Now().Add(2 * time.Hour)
	conflictContent, err := selftest49Envelope(selftest49Host("conflict-cloud", conflictTime), conflictTime)
	if err != nil {
		return report, err
	}
	github.armConflict(conflictContent)
	_, conflictWritesBefore, _ := github.snapshot()
	if _, err := service.Sync(ctx, appsync.TriggerManual); !errors.Is(err, appsync.ErrConflict) {
		return report, fmt.Errorf("sync under a lost conditional write = %v, want conflict", err)
	}
	conflictSnapshot := service.Snapshot()
	report.ConflictSurfaced = conflictSnapshot.Status == appsync.StatusConflict && conflictSnapshot.Conflict != nil
	resolved, err := service.ResolveConflict(ctx, appsync.ResolutionRemote)
	if err != nil {
		return report, fmt.Errorf("resolve the conflict with the cloud copy: %w", err)
	}
	report.ConflictAction = string(resolved.Action)
	hosts, err = database.List(ctx)
	if err != nil {
		return report, fmt.Errorf("list the conflict result: %w", err)
	}
	report.ConflictLocalHost = selftest49FirstLabel(hosts)
	conflictRemoteContent, conflictWritesAfter, attempts := github.snapshot()
	report.ConflictAttempts = attempts
	report.ConflictWrites = conflictWritesAfter - conflictWritesBefore
	conflictRemotePayload, err := selftest49Payload(conflictRemoteContent)
	if err != nil {
		return report, fmt.Errorf("decode the conflict cloud copy: %w", err)
	}
	report.ConflictRemoteHost = selftest49FirstLabel(conflictRemotePayload.Hosts)
	if converged, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync after conflict download: %w", err)
	} else if converged.Action != domainsync.ActionNone {
		return report, fmt.Errorf("sync after conflict download = %q, want none", converged.Action)
	}

	// 7. An emptied local Vault must never replace the cloud copy, for either
	// trigger, and must not even reach a write.
	if err := database.ApplySync(ctx, appsync.Payload{}); err != nil {
		return report, fmt.Errorf("empty the local Vault: %w", err)
	}
	beforeEmpty := conflictRemoteContent
	_, emptyWritesBefore, _ := github.snapshot()
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		report.EmptyManualError = selftest49ErrorName(err)
	}
	if _, err := service.Sync(ctx, appsync.TriggerAuto); err != nil {
		report.EmptyAutoError = selftest49ErrorName(err)
	}
	report.EmptyProblem = string(service.Snapshot().Problem)
	afterEmpty, emptyWritesAfter, _ := github.snapshot()
	report.EmptyCloudBytes = len(afterEmpty)
	report.EmptyWrites = emptyWritesAfter - emptyWritesBefore
	if !bytes.Equal(beforeEmpty, afterEmpty) {
		return report, errors.New("the empty-Vault guard let the cloud copy change")
	}
	emptyPayload, err := selftest49Payload(afterEmpty)
	if err != nil {
		return report, fmt.Errorf("decode the guarded cloud copy: %w", err)
	}
	report.EmptyCloudHost = selftest49FirstLabel(emptyPayload.Hosts)

	return report, nil
}

func selftest49Host(id string, when time.Time) domainvault.Host {
	return domainvault.Host{
		ID:        model.HostID(id),
		Label:     id,
		Hostname:  id + ".example.test",
		Port:      22,
		Username:  "demo",
		Protocol:  domainvault.ProtocolSSH,
		Tags:      []string{},
		OrderKey:  1000,
		CreatedAt: when.Add(-time.Minute),
		UpdatedAt: when,
	}
}

func selftest49Envelope(host domainvault.Host, when time.Time) ([]byte, error) {
	payload := appsync.Payload{Hosts: []domainvault.Host{host}, SyncedAt: when.UnixMilli()}
	envelope, err := domainsync.EncryptPayload(payload, selftest49Password, domainsync.EncryptOptions{
		Version:    2,
		UpdatedAt:  when.UnixMilli(),
		DeviceID:   "peer-device",
		DeviceName: "Peer Device",
		AppVersion: "selftest",
	})
	if err != nil {
		return nil, fmt.Errorf("encrypt the peer cloud copy: %w", err)
	}
	data, err := domainsync.MarshalFile(envelope)
	if err != nil {
		return nil, fmt.Errorf("serialize the peer cloud copy: %w", err)
	}
	return data, nil
}

func selftest49Payload(content []byte) (appsync.Payload, error) {
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.Payload{}, err
	}
	plaintext, err := domainsync.DecryptPayload(envelope, selftest49Password)
	if err != nil {
		return appsync.Payload{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return appsync.Payload{}, err
	}
	return payload, nil
}

func selftest49FirstLabel(hosts []domainvault.Host) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0].Label
}

func selftest49Label(payload appsync.Payload) string {
	return selftest49FirstLabel(payload.Hosts)
}

func selftest49Contains(payload appsync.Payload, label string) string {
	for _, host := range payload.Hosts {
		if host.Label == label {
			return label
		}
	}
	return ""
}

func selftest49ErrorName(err error) string {
	switch {
	case errors.Is(err, appsync.ErrTombstoneGuard):
		return "tombstone guard"
	case errors.Is(err, appsync.ErrEmptyVault):
		return "empty Vault"
	case err == nil:
		return ""
	default:
		return err.Error()
	}
}

// selftest49Wait polls an observable condition on a cadence with the scenario
// context as its hard deadline. The cadence is not a synchronization
// assumption; the condition alone decides success.
func selftest49Wait(ctx context.Context, condition func() bool) error {
	if condition() {
		return nil
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if condition() {
				return nil
			}
		}
	}
}
