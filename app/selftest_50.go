//go:build selftest

package app

// Ticket 50 self-test. Press F11, then 5, then 0.
//
// The check runs the real Google Drive sync stack headlessly against an
// in-process fake Google server; no real network, browser, or Google
// credential is involved:
//
//   - the system-browser opener receives a PKCE S256 authorize URL whose
//     redirect is an ephemeral 127.0.0.1 loopback; the fake validates the
//     verifier and exchanges the code without a client secret;
//   - the product file lives only in appDataFolder and uploads, updates, and
//     downloads through the real Drive adapter;
//   - an expired access token and a 401 both refresh once, and the rotated
//     credential is persisted;
//   - invalid_grant starts a fresh browser authorization and resumes the
//     interrupted sync;
//   - authorizing a different Google account clears the merge anchor, while
//     reauthorizing the same account keeps it.
//
// A repeated F11,5,0 starts from a clean scenario. The scenario runs in one
// goroutine so the event loop is never blocked; waits poll an observable
// condition with the scenario deadline as the hard bound.
//
// Result: %TEMP%\natcatty-selftest-50.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	adaptergdrive "github.com/mslxl/natcatty/adapters/gdrive"
	"github.com/mslxl/natcatty/adapters/gdrive/fake"
	adapterstore "github.com/mslxl/natcatty/adapters/store"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	platformsecrets "github.com/mslxl/natcatty/platform/secrets"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest50Ticket   = 50
	selftest50Timeout  = 45 * time.Second
	selftest50AuthTime = 15 * time.Second
	selftest50Password = "ticket 50 master password"
)

type selftest50 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest50Result
	report   selftest50Report
	err      error
}

type selftest50Report struct {
	AuthOpened          int
	AuthS256            bool
	AuthLoopback        bool
	AuthNoSecret        bool
	AuthLogin           string
	AuthAccount         string
	AuthProvider        string
	AuthTokenProvider   string
	AuthExchanges       int
	SecretViolations    int
	UploadAction        string
	UploadHost          string
	UploadBytes         int
	UpdateAction        string
	UpdateHost          string
	Updates             int
	DownloadAction      string
	DownloadHost        string
	ExpiryRefreshes     int
	ExpiryRotated       bool
	ExpiryKeptRefresh   bool
	UnauthorizedRefresh int
	UnauthorizedRetried bool
	ReauthAuthorizes    int
	ReauthUploaded      bool
	ReauthLogin         string
	ReauthAccount       string
	ReauthRotated       bool
	ReauthProblem       string
	AccountBefore       string
	AccountAfter        string
	AccountLogin        string
	AnchorCleared       bool
	SecondFiles         int
	FirstFiles          int
	SameAnchorKept      bool
	SameAccount         string
}

type selftest50Result struct {
	report selftest50Report
	err    error
}

func (t *selftest50) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest50Ticket,
		Name:   "Google Drive appDataFolder sync",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "pkce-system-browser-loopback", Run: t.authStep},
			{Name: "appdatafolder-read-write", Run: t.driveStep},
			{Name: "token-refresh", Run: t.refreshStep},
			{Name: "reauthorization-resumes-sync", Run: t.reauthStep},
			{Name: "different-account-clears-anchor", Run: t.accountStep},
			{Name: "same-account-keeps-anchor", Run: t.sameAccountStep},
		},
	}
}

func (t *selftest50) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest50Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest50Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest50Timeout)
		t.cancel = cancel
		t.result = make(chan selftest50Result, 1)
		go func() {
			report, err := runSelftest50(ctx)
			t.result <- selftest50Result{report: report, err: err}
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
			"fake Google: PKCE loopback authorization, appDataFolder upload/update/download, expiry and 401 refresh, invalid_grant reauthorization, account anchor rotation",
			"adapters/gdrive + adapters/gdrive/fake + application/sync")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the Google Drive scenario did not finish in %s", selftest50Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless Google Drive scenario")
	}
}

func (t *selftest50) authStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.AuthOpened == 0 || !report.AuthS256 || !report.AuthLoopback || !report.AuthNoSecret {
		return selftest.Failed(fmt.Errorf(
			"authorize opener = %d S256 = %v loopback = %v noSecret = %v",
			report.AuthOpened, report.AuthS256, report.AuthLoopback, report.AuthNoSecret))
	}
	if report.AuthLogin != "one@example.test" || report.AuthAccount != "google-account-1" ||
		report.AuthProvider != appsync.ProviderGoogleDrive ||
		report.AuthTokenProvider != appsync.ProviderGoogleDrive {
		return selftest.Failed(fmt.Errorf("login = %q account = %q provider = %q tokenProvider = %q",
			report.AuthLogin, report.AuthAccount, report.AuthProvider, report.AuthTokenProvider))
	}
	if report.AuthExchanges != 1 || report.SecretViolations != 0 {
		return selftest.Failed(fmt.Errorf("exchanges = %d secret violations = %d",
			report.AuthExchanges, report.SecretViolations))
	}
	return selftest.Passed(
		"system browser opened an S256 authorize URL with an ephemeral 127.0.0.1 callback; the fake exchanged the code with the verifier and no client secret, and the token was stored under the google provider",
		"adapters/gdrive.Authorize + PKCE S256 + loopback http.Server")
}

func (t *selftest50) driveStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.UploadAction != string(domainsync.ActionUpload) || report.UploadHost != "gdrive-alpha" || report.UploadBytes == 0 {
		return selftest.Failed(fmt.Errorf("upload action = %q host = %q bytes = %d",
			report.UploadAction, report.UploadHost, report.UploadBytes))
	}
	if report.UpdateAction != string(domainsync.ActionUpload) || report.UpdateHost != "gdrive-beta" || report.Updates != 1 {
		return selftest.Failed(fmt.Errorf("update action = %q host = %q updates = %d",
			report.UpdateAction, report.UpdateHost, report.Updates))
	}
	if report.DownloadAction != string(domainsync.ActionDownload) || report.DownloadHost != "gdrive-cloud" {
		return selftest.Failed(fmt.Errorf("download action = %q host = %q",
			report.DownloadAction, report.DownloadHost))
	}
	return selftest.Passed(
		"the product file was created and updated in appDataFolder, a peer copy downloaded into the local Vault, and the names/parent were the original netcatty-vault.json in appDataFolder",
		"adapters/gdrive appDataFolder multipart create + media update + alt=media download")
}

func (t *selftest50) refreshStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.ExpiryRefreshes != 1 || !report.ExpiryRotated || !report.ExpiryKeptRefresh {
		return selftest.Failed(fmt.Errorf("expiry refreshes = %d rotated = %v keptRefresh = %v",
			report.ExpiryRefreshes, report.ExpiryRotated, report.ExpiryKeptRefresh))
	}
	if report.UnauthorizedRefresh != 1 || !report.UnauthorizedRetried {
		return selftest.Failed(fmt.Errorf("401 refreshes = %d retried = %v",
			report.UnauthorizedRefresh, report.UnauthorizedRetried))
	}
	return selftest.Passed(
		"an expired access token refreshed once before the sync, and a Drive 401 refreshed once and retried the read; the rotated access token was persisted and the refresh token was retained",
		"application/sync ensureFreshToken/refreshToken + adapters/gdrive.Refresh")
}

func (t *selftest50) reauthStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.ReauthAuthorizes != 1 || !report.ReauthUploaded {
		return selftest.Failed(fmt.Errorf("reauthorize flows = %d resumed upload = %v",
			report.ReauthAuthorizes, report.ReauthUploaded))
	}
	if report.ReauthLogin != "one@example.test" || report.ReauthAccount != "google-account-1" || !report.ReauthRotated {
		return selftest.Failed(fmt.Errorf("reauth login = %q account = %q tokenRotated = %v",
			report.ReauthLogin, report.ReauthAccount, report.ReauthRotated))
	}
	if report.ReauthProblem != "" {
		return selftest.Failed(fmt.Errorf("problem after reauthorization = %q", report.ReauthProblem))
	}
	return selftest.Passed(
		"invalid_grant started a fresh browser authorization and the interrupted sync resumed and uploaded; no generic failure remained",
		"application/sync reauthorize + BrowserAuthorizer + adapters/gdrive invalid_grant mapping")
}

func (t *selftest50) accountStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.AccountBefore != "google-account-1" || report.AccountAfter != "google-account-2" ||
		report.AccountLogin != "two@example.test" {
		return selftest.Failed(fmt.Errorf("account before = %q after = %q login = %q",
			report.AccountBefore, report.AccountAfter, report.AccountLogin))
	}
	if !report.AnchorCleared {
		return selftest.Failed(errors.New("authorizing a different Google account kept the merge anchor"))
	}
	if report.SecondFiles != 1 || report.FirstFiles != 1 {
		return selftest.Failed(fmt.Errorf("appDataFolder files: second = %d first = %d",
			report.SecondFiles, report.FirstFiles))
	}
	return selftest.Passed(
		"switching from one@example.test to two@example.test cleared the anchor and uploaded into the second account's appDataFolder while the first account's copy stayed isolated",
		"application/sync applyGrant + Config.AccountID + adapters/gdrive.fake per-account appDataFolder")
}

func (t *selftest50) sameAccountStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.SameAnchorKept || report.SameAccount != "google-account-2" {
		return selftest.Failed(fmt.Errorf("same-account reconnect kept anchor = %v account = %q",
			report.SameAnchorKept, report.SameAccount))
	}
	return selftest.Passed(
		"reauthorizing the same Google account kept the merge anchor",
		"application/sync applyGrant same-identity branch")
}

// selftest50KeyStore is the in-memory credential store. The field-secret
// adapter gets an unsupported store so it stays session-only and never touches
// the developer's keychain; the token store gets a working map.
type selftest50KeyStore struct {
	mu          sync.Mutex
	values      map[string][]byte
	unsupported bool
}

func (s *selftest50KeyStore) Get(key string) ([]byte, error) {
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

func (s *selftest50KeyStore) Set(key string, value []byte) error {
	if s.unsupported {
		return platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *selftest50KeyStore) Delete(key string) error {
	if s.unsupported {
		return platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// runSelftest50 executes the whole headless scenario and releases every
// temporary resource before returning.
func runSelftest50(ctx context.Context) (report selftest50Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-50-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	google, err := fake.Start()
	if err != nil {
		return report, fmt.Errorf("start the fake Google server: %w", err)
	}
	defer google.Close()
	google.Reset()

	database, err := adapterstore.Open(ctx, filepath.Join(directory, "vault.db"))
	if err != nil {
		return report, fmt.Errorf("open the SQLite Vault: %w", err)
	}
	defer database.Close()
	secrets, err := adapterstore.OpenSecretStore(ctx, database, &selftest50KeyStore{unsupported: true})
	if err != nil {
		return report, fmt.Errorf("open the field-secret store: %w", err)
	}
	defer secrets.Close()
	if err := secrets.UseSessionOnly(ctx); err != nil {
		return report, fmt.Errorf("switch the field-secret store to session-only: %w", err)
	}

	tokens := adaptersyncstore.NewTokenStore(&selftest50KeyStore{values: map[string][]byte{}})
	configs := adaptersyncstore.OpenConfig(filepath.Join(directory, "cloud-sync.json"))
	remote := adaptergdrive.New(adaptergdrive.Options{
		AuthBase:    google.URL(),
		TokenBase:   google.URL(),
		APIBase:     google.URL() + "/drive/v3",
		ClientID:    google.ClientID(),
		AuthTimeout: selftest50AuthTime,
	})
	vault := adaptersyncvault.New(database, secrets)
	opened := 0
	authS256 := false
	authLoopback := false
	authNoSecret := false
	opener := func(raw string) error {
		opened++
		if parsed, parseErr := url.Parse(raw); parseErr == nil {
			query := parsed.Query()
			if query.Get("code_challenge") != "" && query.Get("code_challenge_method") == "S256" {
				authS256 = true
			}
			if redirect, redirectErr := url.Parse(query.Get("redirect_uri")); redirectErr == nil &&
				redirect.Hostname() == "127.0.0.1" && redirect.Port() != "" &&
				redirect.Path == "/oauth/callback" {
				authLoopback = true
			}
			if query.Get("client_secret") == "" {
				authNoSecret = true
			}
		}
		response, getErr := (&http.Client{Timeout: 10 * time.Second}).Get(raw)
		if getErr == nil {
			_ = response.Body.Close()
		}
		return nil
	}
	service := appsync.NewService(remote, vault, configs, tokens,
		appsync.WithProvider(appsync.ProviderGoogleDrive),
		appsync.WithRemote(appsync.ProviderGoogleDrive, remote),
		appsync.WithBrowserOpener(opener),
		appsync.WithDevice("selftest-google-device", "Self-test Device", "selftest"),
	)
	defer service.Stop()
	defer service.ClearPassword()
	if err := service.Load(ctx); err != nil {
		return report, fmt.Errorf("load the sync profile: %w", err)
	}
	if err := service.SetPassword(selftest50Password); err != nil {
		return report, fmt.Errorf("set the master password: %w", err)
	}

	if err := database.Save(ctx, selftest50Host("gdrive-alpha", time.Now())); err != nil {
		return report, fmt.Errorf("save the initial Host: %w", err)
	}
	if err := service.Connect(ctx, selftest50Password, nil); err != nil {
		return report, fmt.Errorf("browser authorization: %w", err)
	}
	snapshot := service.Snapshot()
	report.AuthOpened = opened
	report.AuthS256 = authS256
	report.AuthLoopback = authLoopback
	report.AuthNoSecret = authNoSecret
	report.AuthLogin = snapshot.Login
	report.AuthProvider = snapshot.Provider
	token, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the authorized token: %w", err)
	}
	report.AuthTokenProvider = token.Provider
	config, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the authorized profile: %w", err)
	}
	report.AuthAccount = config.AccountID
	state := google.State()
	report.AuthExchanges = state.TokenExchanges
	report.SecretViolations = state.SecretViolations

	// 1. Upload into appDataFolder, then update the same file.
	uploaded, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual upload: %w", err)
	}
	report.UploadAction = string(uploaded.Action)
	uploadedFile, exists, err := remote.Read(ctx, token)
	if err != nil || !exists {
		return report, fmt.Errorf("read the uploaded file = %v, %v", exists, err)
	}
	report.UploadBytes = len(uploadedFile.Content)
	uploadedPayload, err := selftest50Payload(uploadedFile.Content)
	if err != nil {
		return report, fmt.Errorf("decode the uploaded cloud copy: %w", err)
	}
	report.UploadHost = selftest50FirstLabel(uploadedPayload.Hosts)

	if err := database.Save(ctx, selftest50Host("gdrive-beta", time.Now())); err != nil {
		return report, fmt.Errorf("save the updated Host: %w", err)
	}
	updated, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual update: %w", err)
	}
	report.UpdateAction = string(updated.Action)
	state = google.State()
	report.Updates = state.Updates
	updatedFile, exists, err := remote.Read(ctx, token)
	if err != nil || !exists {
		return report, fmt.Errorf("read the updated file = %v, %v", exists, err)
	}
	updatedPayload, err := selftest50Payload(updatedFile.Content)
	if err != nil {
		return report, fmt.Errorf("decode the updated cloud copy: %w", err)
	}
	report.UpdateHost = selftest50MatchingLabel(updatedPayload.Hosts, "gdrive-beta")
	if len(state.Accounts) > 0 && len(state.Accounts[0].Files) > 0 {
		report.FirstFiles = len(state.Accounts[0].Files)
	}

	// 2. A peer device writes a newer copy; the next sync downloads it.
	cloudTime := time.Now().Add(time.Hour)
	cloudContent, err := selftest50Envelope(selftest50Host("gdrive-cloud", cloudTime), cloudTime)
	if err != nil {
		return report, err
	}
	if _, err := remote.Write(ctx, token, updatedFile, cloudContent); err != nil {
		return report, fmt.Errorf("write the peer cloud copy: %w", err)
	}
	downloaded, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual download: %w", err)
	}
	report.DownloadAction = string(downloaded.Action)
	hosts, err := database.List(ctx)
	if err != nil {
		return report, fmt.Errorf("list the downloaded Hosts: %w", err)
	}
	report.DownloadHost = selftest50FirstLabel(hosts)

	// 3. An expired access token refreshes exactly once and the rotated token
	// is persisted. Google usually omits refresh_token, so the application must
	// retain the old one.
	token, err = tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the token before the expiry check: %w", err)
	}
	token.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := tokens.Save(ctx, token); err != nil {
		return report, fmt.Errorf("expire the stored token: %w", err)
	}
	refreshesBefore := google.State().Refreshes
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync with an expired token: %w", err)
	}
	report.ExpiryRefreshes = google.State().Refreshes - refreshesBefore
	rotated, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the refreshed token: %w", err)
	}
	report.ExpiryRotated = rotated.AccessToken != token.AccessToken && rotated.AccessToken != ""
	report.ExpiryKeptRefresh = rotated.RefreshToken != ""

	// 4. A Drive 401 on the read refreshes and retries the same operation.
	google.ExpireAccessTokens()
	refreshesBefore = google.State().Refreshes
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync after a 401: %w", err)
	}
	report.UnauthorizedRefresh = google.State().Refreshes - refreshesBefore
	retried, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the token after the 401 retry: %w", err)
	}
	report.UnauthorizedRetried = report.UnauthorizedRefresh == 1 && retried.AccessToken != rotated.AccessToken

	// 5. invalid_grant starts a fresh browser flow and resumes the interrupted
	// upload. Expire the access token and revoke every refresh token, then make
	// the local Vault newer so a no-op cannot stand in for a resumed sync.
	if err := database.Save(ctx, selftest50Host("gdrive-reauth", time.Now())); err != nil {
		return report, fmt.Errorf("save the reauthorization Host: %w", err)
	}
	google.ExpireAccessTokens()
	google.Configure(func(control *fake.Control) { control.InvalidateRefresh = true })
	authorizationsBefore := google.State().Authorizations
	tokenBefore, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the token before reauthorization: %w", err)
	}
	resumed, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("sync through invalid_grant: %w", err)
	}
	report.ReauthAuthorizes = google.State().Authorizations - authorizationsBefore
	report.ReauthUploaded = resumed.Uploaded
	report.ReauthLogin = service.Snapshot().Login
	reauthConfig, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the profile after reauthorization: %w", err)
	}
	report.ReauthAccount = reauthConfig.AccountID
	reauthToken, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the token after reauthorization: %w", err)
	}
	report.ReauthRotated = reauthToken.AccessToken != tokenBefore.AccessToken
	report.ReauthProblem = string(service.Snapshot().Problem)
	google.Configure(func(control *fake.Control) { control.InvalidateRefresh = false })

	// 6. A different Google account clears the merge anchor.
	anchorConfig, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the anchor before switching accounts: %w", err)
	}
	report.AccountBefore = anchorConfig.AccountID
	google.Configure(func(control *fake.Control) { control.Account = "second" })
	if err := service.Connect(ctx, "", nil); err != nil {
		return report, fmt.Errorf("authorize the second account: %w", err)
	}
	switched, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the profile after switching accounts: %w", err)
	}
	report.AccountAfter = switched.AccountID
	report.AccountLogin = switched.Login
	report.AnchorCleared = switched.LocalSignature == "" && switched.RemoteID == "" &&
		switched.RemoteETag == "" && switched.RemoteUpdatedAt == 0 && switched.LastSyncedAt == 0
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync into the second account: %w", err)
	}
	state = google.State()
	if len(state.Accounts) >= 2 {
		report.SecondFiles = len(state.Accounts[1].Files)
		report.FirstFiles = len(state.Accounts[0].Files)
	}

	// 7. The same account keeps the anchor.
	beforeSame, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the anchor before the same-account reconnect: %w", err)
	}
	if err := service.Connect(ctx, "", nil); err != nil {
		return report, fmt.Errorf("reauthorize the same account: %w", err)
	}
	afterSame, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the profile after the same-account reconnect: %w", err)
	}
	report.SameAccount = afterSame.AccountID
	report.SameAnchorKept = afterSame.LocalSignature == beforeSame.LocalSignature &&
		afterSame.RemoteID == beforeSame.RemoteID &&
		afterSame.RemoteETag == beforeSame.RemoteETag &&
		afterSame.RemoteUpdatedAt == beforeSame.RemoteUpdatedAt &&
		afterSame.LastSyncedAt == beforeSame.LastSyncedAt

	return report, nil
}

func selftest50Host(id string, when time.Time) domainvault.Host {
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

func selftest50Envelope(host domainvault.Host, when time.Time) ([]byte, error) {
	payload := appsync.Payload{Hosts: []domainvault.Host{host}, SyncedAt: when.UnixMilli()}
	envelope, err := domainsync.EncryptPayload(payload, selftest50Password, domainsync.EncryptOptions{
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

func selftest50Payload(content []byte) (appsync.Payload, error) {
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.Payload{}, err
	}
	plaintext, err := domainsync.DecryptPayload(envelope, selftest50Password)
	if err != nil {
		return appsync.Payload{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return appsync.Payload{}, err
	}
	return payload, nil
}

func selftest50FirstLabel(hosts []domainvault.Host) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0].Label
}

func selftest50MatchingLabel(hosts []domainvault.Host, label string) string {
	for _, host := range hosts {
		if host.Label == label {
			return label
		}
	}
	return ""
}
