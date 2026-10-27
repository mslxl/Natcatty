//go:build selftest

package app

// Ticket 51 self-test. Press F11, then 5, then 1.
//
// The check runs the real OneDrive sync stack headlessly against an in-process
// fake Microsoft identity and Graph server; no real network, browser, or
// Microsoft credential is involved:
//
//   - the system-browser opener receives a PKCE S256 authorize URL whose
//     redirect is an ephemeral 127.0.0.1 loopback; the fake validates the
//     verifier and exchanges the code without a client secret;
//   - the product file lives only in the approot: upload, conditional update,
//     and download go through the real OneDrive adapter, and the bearer token
//     is never attached to the pre-authenticated download URL;
//   - an expired access token and a 401 both refresh once; Microsoft rotates
//     the refresh token, and the rotated credential is persisted;
//   - invalid_grant starts a fresh browser authorization and resumes the
//     interrupted sync;
//   - authorizing a different Microsoft account clears the merge anchor, while
//     reauthorizing the same account keeps it;
//   - a denied or tampered reauthorization leaves the anchor, the stored
//     credential, and the remote file untouched.
//
// A repeated F11,5,1 starts from a clean scenario. The scenario runs in one
// goroutine so the event loop is never blocked; waits poll an observable
// condition with the scenario deadline as the hard bound.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-51.json (os.TempDir on other platforms).

import (
	"bytes"
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

	adapteronedrive "github.com/mslxl/natcatty/adapters/onedrive"
	"github.com/mslxl/natcatty/adapters/onedrive/fake"
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
	selftest51Ticket   = 51
	selftest51Timeout  = 60 * time.Second
	selftest51AuthTime = 15 * time.Second
	selftest51Password = "ticket 51 master password"
)

type selftest51 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest51Result
	report   selftest51Report
	err      error
}

type selftest51Report struct {
	AuthOpened        int
	AuthS256          bool
	AuthLoopback      bool
	AuthNoSecret      bool
	AuthLogin         string
	AuthAccount       string
	AuthProvider      string
	AuthTokenProvider string
	AuthExchanges     int
	SecretViolations  int

	UploadAction   string
	UploadHost     string
	UploadBytes    int
	UpdateAction   string
	UpdateHost     string
	Updates        int
	DownloadAction string
	DownloadHost   string
	ApprootFiles   int
	ApprootName    string

	ExpiryRefreshes      int
	ExpiryRotated        bool
	ExpiryRotatedRefresh bool
	UnauthorizedRefresh  int
	UnauthorizedRetried  bool

	ReauthAuthorizes int
	ReauthUploaded   bool
	ReauthLogin      string
	ReauthAccount    string
	ReauthRotated    bool
	ReauthProblem    string

	AccountBefore string
	AccountAfter  string
	AccountLogin  string
	AnchorCleared bool
	SecondFiles   int
	FirstFiles    int

	SameAnchorKept bool
	SameAccount    string

	FailedDenied     bool
	FailedState      bool
	FailedAnchorKept bool
	FailedTokenKept  bool
	FailedRemoteKept bool
	FailedProblem    string
	FailedAuthorizes int
}

type selftest51Result struct {
	report selftest51Report
	err    error
}

func (t *selftest51) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest51Ticket,
		Name:   "OneDrive approot sync",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "pkce-system-browser-loopback", Run: t.authStep},
			{Name: "approot-read-write", Run: t.approotStep},
			{Name: "token-rotation-refresh", Run: t.refreshStep},
			{Name: "reauthorization-resumes-sync", Run: t.reauthStep},
			{Name: "different-account-clears-anchor", Run: t.accountStep},
			{Name: "same-account-keeps-anchor", Run: t.sameAccountStep},
			{Name: "failed-reauthorization-preserves-state", Run: t.failedStep},
		},
	}
}

func (t *selftest51) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest51Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest51Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest51Timeout)
		t.cancel = cancel
		t.result = make(chan selftest51Result, 1)
		go func() {
			report, err := runSelftest51(ctx)
			t.result <- selftest51Result{report: report, err: err}
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
			"fake Microsoft: PKCE loopback authorization, approot upload/update/download, expiry and 401 refresh, invalid_grant reauthorization, account anchor rotation, failed reauthorization preserves state",
			"adapters/onedrive + adapters/onedrive/fake + application/sync")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the OneDrive scenario did not finish in %s", selftest51Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless OneDrive scenario")
	}
}

func (t *selftest51) authStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.AuthOpened == 0 || !report.AuthS256 || !report.AuthLoopback || !report.AuthNoSecret {
		return selftest.Failed(fmt.Errorf(
			"authorize opener = %d S256 = %v loopback = %v noSecret = %v",
			report.AuthOpened, report.AuthS256, report.AuthLoopback, report.AuthNoSecret))
	}
	if report.AuthLogin != "one@example.test" || report.AuthAccount != "onedrive-account-1" ||
		report.AuthProvider != appsync.ProviderOneDrive ||
		report.AuthTokenProvider != appsync.ProviderOneDrive {
		return selftest.Failed(fmt.Errorf("login = %q account = %q provider = %q tokenProvider = %q",
			report.AuthLogin, report.AuthAccount, report.AuthProvider, report.AuthTokenProvider))
	}
	if report.AuthExchanges != 1 || report.SecretViolations != 0 {
		return selftest.Failed(fmt.Errorf("exchanges = %d secret violations = %d",
			report.AuthExchanges, report.SecretViolations))
	}
	return selftest.Passed(
		"system browser opened an S256 authorize URL with an ephemeral 127.0.0.1 callback; the fake exchanged the code with the verifier and no client secret, and the token was stored under the onedrive provider",
		"adapters/onedrive.Authorize + adapters/oauthloopback S256 + loopback http.Server")
}

func (t *selftest51) approotStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.UploadAction != string(domainsync.ActionUpload) || report.UploadHost != "onedrive-alpha" ||
		report.UploadBytes == 0 {
		return selftest.Failed(fmt.Errorf("upload action = %q host = %q bytes = %d",
			report.UploadAction, report.UploadHost, report.UploadBytes))
	}
	if report.UpdateAction != string(domainsync.ActionUpload) || report.UpdateHost != "onedrive-beta" ||
		report.Updates != 1 {
		return selftest.Failed(fmt.Errorf("update action = %q host = %q updates = %d",
			report.UpdateAction, report.UpdateHost, report.Updates))
	}
	if report.DownloadAction != string(domainsync.ActionDownload) || report.DownloadHost != "onedrive-cloud" {
		return selftest.Failed(fmt.Errorf("download action = %q host = %q",
			report.DownloadAction, report.DownloadHost))
	}
	if report.ApprootFiles != 1 || report.ApprootName != domainsync.SyncFileName {
		return selftest.Failed(fmt.Errorf("approot files = %d name = %q",
			report.ApprootFiles, report.ApprootName))
	}
	return selftest.Passed(
		"the product file was created and conditionally updated in the approot, a peer copy downloaded into the local Vault, and the cloud name stayed netcatty-vault.json",
		"adapters/onedrive approot simple PUT create/update + downloadUrl")
}

func (t *selftest51) refreshStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.ExpiryRefreshes != 1 || !report.ExpiryRotated || !report.ExpiryRotatedRefresh {
		return selftest.Failed(fmt.Errorf("expiry refreshes = %d rotatedAccess = %v rotatedRefresh = %v",
			report.ExpiryRefreshes, report.ExpiryRotated, report.ExpiryRotatedRefresh))
	}
	if report.UnauthorizedRefresh != 1 || !report.UnauthorizedRetried {
		return selftest.Failed(fmt.Errorf("401 refreshes = %d retried = %v",
			report.UnauthorizedRefresh, report.UnauthorizedRetried))
	}
	return selftest.Passed(
		"an expired access token refreshed once before the sync, and a Graph 401 refreshed once and retried the read; Microsoft's rotated access and refresh tokens were both persisted",
		"application/sync ensureFreshToken/refreshToken + adapters/onedrive.Refresh rotation")
}

func (t *selftest51) reauthStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.ReauthAuthorizes != 1 || !report.ReauthUploaded {
		return selftest.Failed(fmt.Errorf("reauthorize flows = %d resumed upload = %v",
			report.ReauthAuthorizes, report.ReauthUploaded))
	}
	if report.ReauthLogin != "one@example.test" || report.ReauthAccount != "onedrive-account-1" ||
		!report.ReauthRotated {
		return selftest.Failed(fmt.Errorf("reauth login = %q account = %q tokenRotated = %v",
			report.ReauthLogin, report.ReauthAccount, report.ReauthRotated))
	}
	if report.ReauthProblem != "" {
		return selftest.Failed(fmt.Errorf("problem after reauthorization = %q", report.ReauthProblem))
	}
	return selftest.Passed(
		"invalid_grant started a fresh browser authorization and the interrupted sync resumed and uploaded; no generic failure remained",
		"application/sync reauthorize + BrowserAuthorizer + adapters/onedrive invalid_grant mapping")
}

func (t *selftest51) accountStep(*ui.Context) selftest.Outcome {
	report := t.report
	if report.AccountBefore != "onedrive-account-1" || report.AccountAfter != "onedrive-account-2" ||
		report.AccountLogin != "two@example.test" {
		return selftest.Failed(fmt.Errorf("account before = %q after = %q login = %q",
			report.AccountBefore, report.AccountAfter, report.AccountLogin))
	}
	if !report.AnchorCleared {
		return selftest.Failed(errors.New("authorizing a different Microsoft account kept the merge anchor"))
	}
	if report.SecondFiles != 1 || report.FirstFiles != 1 {
		return selftest.Failed(fmt.Errorf("approot files: second = %d first = %d",
			report.SecondFiles, report.FirstFiles))
	}
	return selftest.Passed(
		"switching from one@example.test to two@example.test cleared the anchor and uploaded into the second account's approot while the first account's copy stayed isolated",
		"application/sync applyGrant + Config.AccountID + adapters/onedrive/fake per-account approot")
}

func (t *selftest51) sameAccountStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.SameAnchorKept || report.SameAccount != "onedrive-account-2" {
		return selftest.Failed(fmt.Errorf("same-account reconnect kept anchor = %v account = %q",
			report.SameAnchorKept, report.SameAccount))
	}
	return selftest.Passed(
		"reauthorizing the same Microsoft account kept the merge anchor",
		"application/sync applyGrant same-identity branch")
}

func (t *selftest51) failedStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.FailedDenied || !report.FailedState {
		return selftest.Failed(fmt.Errorf("dialogs: denied = %v stateMismatch = %v",
			report.FailedDenied, report.FailedState))
	}
	if report.FailedAuthorizes != 2 {
		return selftest.Failed(fmt.Errorf("failed authorizations = %d, want 2", report.FailedAuthorizes))
	}
	if !report.FailedAnchorKept || !report.FailedTokenKept || !report.FailedRemoteKept {
		return selftest.Failed(fmt.Errorf("after a failed reauthorization anchorKept = %v tokenKept = %v remoteKept = %v",
			report.FailedAnchorKept, report.FailedTokenKept, report.FailedRemoteKept))
	}
	if report.FailedProblem != string(appsync.ProblemReauthorization) {
		return selftest.Failed(fmt.Errorf("problem after a failed reauthorization = %q, want %q",
			report.FailedProblem, appsync.ProblemReauthorization))
	}
	return selftest.Passed(
		"a denied and a state-mismatched reauthorization both left the merge anchor, the stored credential, and the existing remote file untouched, and reported the actionable reconnect state",
		"application/sync reauthorize error chain + ProblemReauthorization + adapters/onedrive/fake pre/post content")
}

// selftest51KeyStore is the in-memory credential store. The field-secret
// adapter gets an unsupported store so it stays session-only and never touches
// the developer's keychain; the token store gets a working map.
type selftest51KeyStore struct {
	mu          sync.Mutex
	values      map[string][]byte
	unsupported bool
}

func (s *selftest51KeyStore) Get(key string) ([]byte, error) {
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

func (s *selftest51KeyStore) Set(key string, value []byte) error {
	if s.unsupported {
		return platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *selftest51KeyStore) Delete(key string) error {
	if s.unsupported {
		return platformsecrets.ErrUnsupported
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.values, key)
	return nil
}

// runSelftest51 executes the whole headless scenario and releases every
// temporary resource before returning.
func runSelftest51(ctx context.Context) (report selftest51Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-51-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	microsoft, err := fake.Start()
	if err != nil {
		return report, fmt.Errorf("start the fake Microsoft server: %w", err)
	}
	defer microsoft.Close()
	microsoft.Reset()

	database, err := adapterstore.Open(ctx, filepath.Join(directory, "vault.db"))
	if err != nil {
		return report, fmt.Errorf("open the SQLite Vault: %w", err)
	}
	defer database.Close()
	secrets, err := adapterstore.OpenSecretStore(ctx, database, &selftest51KeyStore{unsupported: true})
	if err != nil {
		return report, fmt.Errorf("open the field-secret store: %w", err)
	}
	defer secrets.Close()
	if err := secrets.UseSessionOnly(ctx); err != nil {
		return report, fmt.Errorf("switch the field-secret store to session-only: %w", err)
	}

	tokens := adaptersyncstore.NewTokenStore(&selftest51KeyStore{values: map[string][]byte{}})
	configs := adaptersyncstore.OpenConfig(filepath.Join(directory, "cloud-sync.json"))
	remote := adapteronedrive.New(adapteronedrive.Options{
		AuthBase:    microsoft.URL(),
		TokenBase:   microsoft.URL(),
		GraphBase:   microsoft.URL() + "/v1.0",
		ClientID:    microsoft.ClientID(),
		AuthTimeout: selftest51AuthTime,
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
		appsync.WithProvider(appsync.ProviderOneDrive),
		appsync.WithRemote(appsync.ProviderOneDrive, remote),
		appsync.WithBrowserOpener(opener),
		appsync.WithDevice("selftest-onedrive-device", "Self-test Device", "selftest"),
	)
	defer service.Stop()
	defer service.ClearPassword()
	if err := service.Load(ctx); err != nil {
		return report, fmt.Errorf("load the sync profile: %w", err)
	}
	if err := service.SetPassword(selftest51Password); err != nil {
		return report, fmt.Errorf("set the master password: %w", err)
	}

	if err := database.Save(ctx, selftest51Host("onedrive-alpha", time.Now())); err != nil {
		return report, fmt.Errorf("save the initial Host: %w", err)
	}
	if err := service.Connect(ctx, selftest51Password, nil); err != nil {
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
	state := microsoft.State()
	report.AuthExchanges = state.TokenExchanges
	report.SecretViolations = state.SecretViolations

	// 1. Upload into the approot, then update the same item.
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
	uploadedPayload, err := selftest51Payload(uploadedFile.Content)
	if err != nil {
		return report, fmt.Errorf("decode the uploaded cloud copy: %w", err)
	}
	report.UploadHost = selftest51FirstLabel(uploadedPayload.Hosts)

	if err := database.Save(ctx, selftest51Host("onedrive-beta", time.Now())); err != nil {
		return report, fmt.Errorf("save the updated Host: %w", err)
	}
	updated, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual update: %w", err)
	}
	report.UpdateAction = string(updated.Action)
	state = microsoft.State()
	report.Updates = state.Updates
	updatedFile, exists, err := remote.Read(ctx, token)
	if err != nil || !exists {
		return report, fmt.Errorf("read the updated file = %v, %v", exists, err)
	}
	updatedPayload, err := selftest51Payload(updatedFile.Content)
	if err != nil {
		return report, fmt.Errorf("decode the updated cloud copy: %w", err)
	}
	report.UpdateHost = selftest51MatchingLabel(updatedPayload.Hosts, "onedrive-beta")
	if len(state.Accounts) > 0 {
		report.FirstFiles = len(state.Accounts[0].Files)
		if len(state.Accounts[0].Files) > 0 {
			report.ApprootName = state.Accounts[0].Files[0].Name
			report.ApprootFiles = len(state.Accounts[0].Files)
		}
	}

	// 2. A peer device writes a newer copy; the next sync downloads it.
	cloudTime := time.Now().Add(time.Hour)
	cloudContent, err := selftest51Envelope(selftest51Host("onedrive-cloud", cloudTime), cloudTime)
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
	report.DownloadHost = selftest51FirstLabel(hosts)

	// 3. An expired access token refreshes exactly once. Microsoft rotates the
	// refresh token, so both halves of the pair must be persisted.
	token, err = tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the token before the expiry check: %w", err)
	}
	token.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli()
	if err := tokens.Save(ctx, token); err != nil {
		return report, fmt.Errorf("expire the stored token: %w", err)
	}
	refreshesBefore := microsoft.State().Refreshes
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync with an expired token: %w", err)
	}
	report.ExpiryRefreshes = microsoft.State().Refreshes - refreshesBefore
	rotated, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the refreshed token: %w", err)
	}
	report.ExpiryRotated = rotated.AccessToken != token.AccessToken && rotated.AccessToken != ""
	report.ExpiryRotatedRefresh = rotated.RefreshToken != "" && rotated.RefreshToken != token.RefreshToken

	// 4. A Graph 401 on the read refreshes and retries the same operation.
	microsoft.ExpireAccessTokens()
	refreshesBefore = microsoft.State().Refreshes
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync after a 401: %w", err)
	}
	report.UnauthorizedRefresh = microsoft.State().Refreshes - refreshesBefore
	retried, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the token after the 401 retry: %w", err)
	}
	report.UnauthorizedRetried = report.UnauthorizedRefresh == 1 && retried.AccessToken != rotated.AccessToken

	// 5. invalid_grant starts a fresh browser flow and resumes the interrupted
	// upload. Expire the access token and revoke every refresh token, then make
	// the local Vault newer so a no-op cannot stand in for a resumed sync.
	if err := database.Save(ctx, selftest51Host("onedrive-reauth", time.Now())); err != nil {
		return report, fmt.Errorf("save the reauthorization Host: %w", err)
	}
	microsoft.ExpireAccessTokens()
	microsoft.Configure(func(control *fake.Control) { control.InvalidateRefresh = true })
	authorizationsBefore := microsoft.State().Authorizations
	tokenBefore, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("reload the token before reauthorization: %w", err)
	}
	resumed, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("sync through invalid_grant: %w", err)
	}
	report.ReauthAuthorizes = microsoft.State().Authorizations - authorizationsBefore
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
	microsoft.Configure(func(control *fake.Control) { control.InvalidateRefresh = false })

	// 6. A different Microsoft account clears the merge anchor.
	anchorConfig, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the anchor before switching accounts: %w", err)
	}
	report.AccountBefore = anchorConfig.AccountID
	microsoft.Configure(func(control *fake.Control) { control.Account = "second" })
	if err := service.Connect(ctx, "", nil); err != nil {
		return report, fmt.Errorf("authorize the second account: %w", err)
	}
	switched, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the profile after switching accounts: %w", err)
	}
	report.AccountAfter = switched.AccountID
	report.AccountLogin = switched.Login
	report.AnchorCleared = selftest51AnchorEmpty(switched)
	if _, err := service.Sync(ctx, appsync.TriggerManual); err != nil {
		return report, fmt.Errorf("sync into the second account: %w", err)
	}
	state = microsoft.State()
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
	report.SameAnchorKept = selftest51AnchorEqual(beforeSame, afterSame)

	// 8. A denied and a tampered reauthorization must leave the existing anchor,
	// credential, and remote copy intact.
	anchorBeforeFailure, err := configs.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the anchor before the failed reauthorization: %w", err)
	}
	tokenBeforeFailure, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the token before the failed reauthorization: %w", err)
	}
	contentBeforeFailure, ok := microsoft.FileContent("second", domainsync.SyncFileName)
	if !ok {
		return report, errors.New("the second account has no file before the failed reauthorization")
	}

	failedAuthorizations := 0
	deniedAnchor, deniedToken, deniedRemote := false, false, false
	stateAnchor, stateToken, stateRemote := false, false, false

	microsoft.Configure(func(control *fake.Control) {
		control.Deny = true
		control.InvalidateRefresh = true
	})
	microsoft.ExpireAccessTokens()
	beforeFailure := microsoft.State().Authorizations
	_, deniedErr := service.Sync(ctx, appsync.TriggerManual)
	report.FailedDenied = errors.Is(deniedErr, appsync.ErrReauthorizationRequired) &&
		errors.Is(deniedErr, appsync.ErrAuthorizationDenied)
	failedAuthorizations += microsoft.State().Authorizations - beforeFailure
	deniedAnchor, deniedToken, deniedRemote = selftest51FailureKept(configs, tokens, microsoft, anchorBeforeFailure, tokenBeforeFailure, contentBeforeFailure)

	microsoft.Configure(func(control *fake.Control) {
		control.Deny = false
		control.TamperState = true
		control.InvalidateRefresh = true
	})
	microsoft.ExpireAccessTokens()
	beforeFailure = microsoft.State().Authorizations
	_, stateErr := service.Sync(ctx, appsync.TriggerManual)
	report.FailedState = errors.Is(stateErr, appsync.ErrReauthorizationRequired) &&
		errors.Is(stateErr, appsync.ErrAuthorizationState)
	failedAuthorizations += microsoft.State().Authorizations - beforeFailure
	stateAnchor, stateToken, stateRemote = selftest51FailureKept(configs, tokens, microsoft, anchorBeforeFailure, tokenBeforeFailure, contentBeforeFailure)

	report.FailedAuthorizes = failedAuthorizations
	report.FailedAnchorKept = deniedAnchor && stateAnchor
	report.FailedTokenKept = deniedToken && stateToken
	report.FailedRemoteKept = deniedRemote && stateRemote
	report.FailedProblem = string(service.Snapshot().Problem)
	microsoft.Configure(func(control *fake.Control) {
		control.Deny = false
		control.TamperState = false
		control.InvalidateRefresh = false
	})

	return report, nil
}

// selftest51FailureKept reports whether the profile anchor, stored token, and
// remote product file are still the ones captured before a failed
// reauthorization. The three are read independently so a failure names the
// exact invariant that broke.
func selftest51FailureKept(
	configs *adaptersyncstore.ConfigStore,
	tokens *adaptersyncstore.TokenStore,
	microsoft *fake.Server,
	anchor appsync.Config,
	token domainsync.Token,
	content []byte,
) (anchorKept, tokenKept, remoteKept bool) {
	afterConfig, configErr := configs.Load(context.Background())
	afterToken, tokenErr := tokens.Load(context.Background())
	afterContent, ok := microsoft.FileContent("second", domainsync.SyncFileName)
	anchorKept = configErr == nil && selftest51AnchorEqual(anchor, afterConfig)
	tokenKept = tokenErr == nil && afterToken.AccessToken == token.AccessToken &&
		afterToken.RefreshToken == token.RefreshToken
	remoteKept = ok && bytes.Equal(content, afterContent)
	return anchorKept, tokenKept, remoteKept
}

func selftest51AnchorEmpty(config appsync.Config) bool {
	return config.LocalSignature == "" && config.RemoteID == "" && config.RemoteETag == "" &&
		config.RemoteUpdatedAt == 0 && config.LastSyncedAt == 0
}

func selftest51AnchorEqual(first, second appsync.Config) bool {
	return first.AccountID == second.AccountID &&
		first.LocalSignature == second.LocalSignature &&
		first.RemoteID == second.RemoteID &&
		first.RemoteETag == second.RemoteETag &&
		first.RemoteUpdatedAt == second.RemoteUpdatedAt &&
		first.LastSyncedAt == second.LastSyncedAt
}

func selftest51Host(id string, when time.Time) domainvault.Host {
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

func selftest51Envelope(host domainvault.Host, when time.Time) ([]byte, error) {
	payload := appsync.Payload{Hosts: []domainvault.Host{host}, SyncedAt: when.UnixMilli()}
	envelope, err := domainsync.EncryptPayload(payload, selftest51Password, domainsync.EncryptOptions{
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

func selftest51Payload(content []byte) (appsync.Payload, error) {
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.Payload{}, err
	}
	plaintext, err := domainsync.DecryptPayload(envelope, selftest51Password)
	if err != nil {
		return appsync.Payload{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return appsync.Payload{}, err
	}
	return payload, nil
}

func selftest51FirstLabel(hosts []domainvault.Host) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0].Label
}

func selftest51MatchingLabel(hosts []domainvault.Host, label string) string {
	for _, host := range hosts {
		if host.Label == label {
			return label
		}
	}
	return ""
}
