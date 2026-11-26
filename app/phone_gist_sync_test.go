package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/store"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appsync "github.com/mslxl/natcatty/application/sync"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// gistTestAccessToken is the credential the fake provider hands out. The tests
// prove it reaches the token store and nowhere a user or a sync document can
// read.
const gistTestAccessToken = "gho_gist_phone_test_token"

// gistTestRemote is an in-memory GitHub Gist. It implements the provider port
// and the shared device-authorization port, so the phone runs the real
// appsync.Service polling loop and the real upload/download state machine;
// only the network and the browser are replaced.
type gistTestRemote struct {
	mu       sync.Mutex
	file     appsync.RemoteFile
	writes   int
	polls    int
	pollErrs []error
}

var _ appsync.Remote = (*gistTestRemote)(nil)
var _ appsync.DeviceAuthorizer = (*gistTestRemote)(nil)

func newGistTestRemote() *gistTestRemote { return &gistTestRemote{} }

// RequestDeviceCode returns a code with a long interval so a test can observe
// the authorizing state and cancel it before the next poll.
func (r *gistTestRemote) RequestDeviceCode(context.Context) (appsync.DeviceCode, error) {
	return appsync.DeviceCode{
		DeviceCode:      "gist-device-code",
		UserCode:        "WXYZ-9876",
		VerificationURI: "https://github.com/login/device",
		ExpiresIn:       900,
		Interval:        30,
	}, nil
}

// PollDeviceToken returns the queued error, or the access token once the queue
// is empty. A success test therefore needs no real wait: the shared loop polls
// before its first sleep.
func (r *gistTestRemote) PollDeviceToken(context.Context, appsync.DeviceCode) (domainsync.Token, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.polls++
	if len(r.pollErrs) > 0 {
		err := r.pollErrs[0]
		r.pollErrs = r.pollErrs[1:]
		if err != nil {
			return domainsync.Token{}, err
		}
	}
	return domainsync.Token{
		AccessToken: gistTestAccessToken,
		TokenType:   "bearer",
		Scope:       "gist read:user",
	}, nil
}

func (r *gistTestRemote) User(context.Context, domainsync.Token) (appsync.Grant, error) {
	return appsync.Grant{ID: "42", Login: "octocat"}, nil
}

func (r *gistTestRemote) Read(context.Context, domainsync.Token) (appsync.RemoteFile, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file.ID == "" {
		return appsync.RemoteFile{}, false, nil
	}
	file := r.file
	file.Content = append([]byte(nil), r.file.Content...)
	return file, true, nil
}

func (r *gistTestRemote) Write(_ context.Context, _ domainsync.Token,
	current appsync.RemoteFile, content []byte) (appsync.RemoteFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current.ID != "" && current.ETag != r.file.ETag {
		return appsync.RemoteFile{}, appsync.ErrConflict
	}
	r.writes++
	r.file = appsync.RemoteFile{
		ID:        "gist-1",
		ETag:      "gist-etag",
		Content:   append([]byte(nil), content...),
		UpdatedAt: time.Now().UnixMilli(),
	}
	return r.file, nil
}

func (r *gistTestRemote) content() ([]byte, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file.ID == "" {
		return nil, false
	}
	return append([]byte(nil), r.file.Content...), true
}

// gistDevice is one phone built by the real newPhoneSync composition root.
type gistDevice struct {
	sync     *phoneSync
	database *store.Store
	tokens   *phoneSyncTokens
	dir      string
}

// newGistDevice builds the phone's Vault sync engine for the GitHub provider.
// It mirrors newSyncDevice, but registers the Gist remote and the GitHub
// profile instead of WebDAV.
func newGistDevice(t *testing.T, ctx context.Context, dir, name string,
	remote *gistTestRemote, tokens *phoneSyncTokens) *gistDevice {
	t.Helper()
	database, err := store.Open(ctx, filepath.Join(dir, "natcatty.db"))
	if err != nil {
		t.Fatalf("%s: open store: %v", name, err)
	}
	t.Cleanup(func() { _ = database.Close() })
	secrets, err := store.OpenSecretStore(ctx, database, newPhoneTestKeys())
	if err != nil {
		t.Fatalf("%s: open secret store: %v", name, err)
	}
	preferences := apppreferences.NewService(
		store.OpenPreferences(filepath.Join(dir, "preferences.json")))
	if err := preferences.Load(ctx); err != nil {
		t.Fatalf("%s: load preferences: %v", name, err)
	}
	terminal := appterminalsettings.NewService(
		store.OpenTerminalSettings(filepath.Join(dir, "terminal-settings.json")))
	if err := terminal.Load(ctx); err != nil {
		t.Fatalf("%s: load terminal settings: %v", name, err)
	}
	if err := database.Save(ctx, domainvault.Host{
		ID:        model.HostID(name + "-host"),
		Label:     name + " host",
		Hostname:  name + ".example",
		Port:      22,
		Protocol:  domainvault.ProtocolSSH,
		UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("%s: save host: %v", name, err)
	}
	synced, err := newPhoneSync(ctx, database, secrets, preferences, terminal, dir,
		withPhoneSyncConfigs(adaptersyncstore.OpenConfig(filepath.Join(dir, "cloud-sync.json"))),
		withPhoneSyncTokens(tokens),
		withPhoneSyncRemote(appsync.ProviderGitHub, remote),
		withPhoneSyncProvider(appsync.ProviderGitHub),
		withPhoneSyncDevice(name+"-device", name, "test"),
	)
	if err != nil {
		t.Fatalf("%s: build phone sync: %v", name, err)
	}
	t.Cleanup(synced.Stop)
	return &gistDevice{sync: synced, database: database, tokens: tokens, dir: dir}
}

// waitForGistSync polls the observable snapshot with a hard deadline. The poll
// interval is a cadence; the condition and deadline are the synchronization.
func waitForGistSync(t *testing.T, phone *phoneSync, want string, cond func(appsync.Snapshot) bool) appsync.Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot := phone.Snapshot()
		if cond(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last snapshot = %+v", want, snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func startGistConnect(t *testing.T, phone *phoneSync) {
	t.Helper()
	if err := phone.StartGitHubConnect(syncMasterPassword); err != nil {
		t.Fatalf("start GitHub connect: %v", err)
	}
}

// TestPhoneGitHubGistSyncUploadsAndDownloads drives ticket 43's acceptance
// through the shared engine: the device-code login connects the phone, a
// portable change uploads in the same encrypted envelope the other providers
// use, and a second device configured for the same Gist downloads it. The
// access token reaches only the credential store.
func TestPhoneGitHubGistSyncUploadsAndDownloads(t *testing.T) {
	ctx := context.Background()
	remote := newGistTestRemote()

	phone := newGistDevice(t, ctx, t.TempDir(), "phone", remote, &phoneSyncTokens{})
	startGistConnect(t, phone.sync)
	snapshot := waitForGistSync(t, phone.sync, "a connected profile",
		func(s appsync.Snapshot) bool { return s.Connected && s.DeviceCode.UserCode == "" })
	if snapshot.Provider != appsync.ProviderGitHub {
		t.Fatalf("provider = %q, want github", snapshot.Provider)
	}
	if snapshot.Login != "octocat" {
		t.Fatalf("login = %q, want octocat", snapshot.Login)
	}
	if snapshot.DeviceCode.UserCode != "" {
		t.Fatal("the connected snapshot still carries the one-time code")
	}

	// The credential is in the token store and tagged for the provider.
	token, err := phone.tokens.Load(ctx)
	if err != nil {
		t.Fatalf("load stored token: %v", err)
	}
	if token.AccessToken != gistTestAccessToken || token.Provider != appsync.ProviderGitHub {
		t.Fatalf("stored token provider/credential = %q/%q, want github/the fake credential",
			token.Provider, token.AccessToken)
	}

	// Nothing in the non-secret profile may contain the credential.
	profile, err := os.ReadFile(filepath.Join(phone.dir, "cloud-sync.json"))
	if err != nil {
		t.Fatalf("read sync profile: %v", err)
	}
	if strings.Contains(string(profile), gistTestAccessToken) {
		t.Fatal("the access token was written into the sync profile document")
	}

	if err := phone.sync.service.SetPassword(syncMasterPassword); err != nil {
		t.Fatalf("set password: %v", err)
	}
	result, err := phone.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("phone upload: %v", err)
	}
	if !result.Uploaded {
		t.Fatalf("phone sync result = %+v, want an upload", result)
	}
	content, ok := remote.content()
	if !ok {
		t.Fatal("the phone did not publish a Gist")
	}
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		t.Fatalf("the Gist copy is not the shared sync envelope: %v", err)
	}
	if envelope.Meta.DeviceID != "phone-device" {
		t.Fatalf("envelope device = %q, want the phone's device id", envelope.Meta.DeviceID)
	}

	desktop := newGistDevice(t, ctx, t.TempDir(), "desktop", remote, &phoneSyncTokens{})
	startGistConnect(t, desktop.sync)
	waitForGistSync(t, desktop.sync, "the second connected profile",
		func(s appsync.Snapshot) bool { return s.Connected })
	if err := desktop.sync.service.SetStrategy(ctx, domainsync.StrategyPreferCloud); err != nil {
		t.Fatalf("set desktop strategy: %v", err)
	}
	result, err = desktop.sync.service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		t.Fatalf("desktop download: %v", err)
	}
	if !result.Downloaded {
		t.Fatalf("desktop sync result = %+v, want a download", result)
	}
	hosts, err := desktop.database.List(ctx)
	if err != nil {
		t.Fatalf("list desktop hosts: %v", err)
	}
	found := false
	for _, host := range hosts {
		if host.Label == "phone host" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the desktop did not receive the phone's host; hosts = %+v", hosts)
	}
}

// TestPhoneGitHubGistExpiredCodeIsRetryable proves an expired device code
// fails through the shared loop without storing a token and without leaving
// the spent code in the snapshot the page renders.
func TestPhoneGitHubGistExpiredCodeIsRetryable(t *testing.T) {
	ctx := context.Background()
	remote := newGistTestRemote()
	remote.pollErrs = []error{appsync.ErrDeviceCodeExpired}

	phone := newGistDevice(t, ctx, t.TempDir(), "phone", remote, &phoneSyncTokens{})
	startGistConnect(t, phone.sync)
	snapshot := waitForGistSync(t, phone.sync, "the expired state",
		func(s appsync.Snapshot) bool { return s.Status == appsync.StatusFailed })
	if snapshot.Connected {
		t.Fatal("an expired code must not connect the profile")
	}
	if snapshot.Problem != appsync.ProblemAuthorization {
		t.Fatalf("problem = %q, want authorization", snapshot.Problem)
	}
	if snapshot.DeviceCode.UserCode != "" {
		t.Fatal("the expired one-time code is still in the snapshot")
	}
	if _, err := phone.tokens.Load(ctx); err == nil {
		t.Fatal("an expired authorization stored a token")
	}

	// The page's retry is a fresh Start, which succeeds once GitHub answers.
	if err := phone.sync.StartGitHubConnect(syncMasterPassword); err != nil {
		t.Fatalf("retry GitHub connect: %v", err)
	}
	waitForGistSync(t, phone.sync, "the retried connected profile",
		func(s appsync.Snapshot) bool { return s.Connected })
}

// TestPhoneGitHubGistCancelIsRetryable proves the page's Cancel aborts the
// shared polling loop and maps to the same retryable authorization state.
func TestPhoneGitHubGistCancelIsRetryable(t *testing.T) {
	ctx := context.Background()
	remote := newGistTestRemote()
	remote.pollErrs = []error{appsync.ErrAuthorizationPending}

	phone := newGistDevice(t, ctx, t.TempDir(), "phone", remote, &phoneSyncTokens{})
	startGistConnect(t, phone.sync)
	waitForGistSync(t, phone.sync, "the device code",
		func(s appsync.Snapshot) bool { return s.DeviceCode.UserCode != "" })
	phone.sync.CancelGitHubConnect()
	snapshot := waitForGistSync(t, phone.sync, "the cancelled state",
		func(s appsync.Snapshot) bool { return s.Status == appsync.StatusFailed })
	if snapshot.Problem != appsync.ProblemAuthorization {
		t.Fatalf("problem = %q, want authorization", snapshot.Problem)
	}
	if snapshot.DeviceCode.UserCode != "" {
		t.Fatal("the cancelled one-time code is still in the snapshot")
	}
	if _, err := phone.tokens.Load(ctx); err == nil {
		t.Fatal("a cancelled authorization stored a token")
	}
}
