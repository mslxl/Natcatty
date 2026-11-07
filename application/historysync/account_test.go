package historysync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

const (
	testWriterUUID = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f"
	testServer     = "https://atuin.example.test"
	testToken      = "session-token-secret-value"
)

var (
	testAccount = Account{ServerURL: testServer, UserID: "user-1", Username: "alice"}
	testKey     = []byte("0123456789abcdef0123456789abcdef")
	testKeyB64  = base64.StdEncoding.EncodeToString(testKey)
)

// fakeSettings is the account settings port.
type fakeSettings struct {
	history domainpreferences.History
	err     error
}

func newFakeSettings() *fakeSettings {
	history := domainpreferences.Defaults().History
	history.SyncEnabled = true
	history.SyncAddress = testServer
	return &fakeSettings{history: history}
}

func (s *fakeSettings) HistorySettings() domainpreferences.History { return s.history }

func (s *fakeSettings) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	if s.err != nil {
		return s.err
	}
	change(&s.history)
	return nil
}

// fakeSecrets counts every access so tests can prove Status never decrypts
// the key and credentials never leak into another store.
type fakeSecrets struct {
	mode   SecretMode
	values map[SecretRef]string
	has    map[SecretRef]int
	get    map[SecretRef]int
	set    map[SecretRef]int
	delete map[SecretRef]int

	hasErr    error
	getErr    error
	setErr    error
	deleteErr error
}

func newFakeSecrets() *fakeSecrets {
	return &fakeSecrets{
		mode:   SecretModeOS,
		values: map[SecretRef]string{},
		has:    map[SecretRef]int{},
		get:    map[SecretRef]int{},
		set:    map[SecretRef]int{},
		delete: map[SecretRef]int{},
	}
}

func (s *fakeSecrets) Mode() SecretMode { return s.mode }

func (s *fakeSecrets) Has(_ context.Context, ref SecretRef) (bool, error) {
	s.has[ref]++
	if s.hasErr != nil {
		return false, s.hasErr
	}
	switch s.mode {
	case SecretModePassphraseLocked:
		return false, ErrSecretsLocked
	case SecretModeUnavailable:
		return false, ErrSecretsUnavailable
	}
	_, ok := s.values[ref]
	return ok, nil
}

func (s *fakeSecrets) Get(_ context.Context, ref SecretRef) (string, error) {
	s.get[ref]++
	if s.getErr != nil {
		return "", s.getErr
	}
	switch s.mode {
	case SecretModePassphraseLocked:
		return "", ErrSecretsLocked
	case SecretModeUnavailable:
		return "", ErrSecretsUnavailable
	}
	value, ok := s.values[ref]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func (s *fakeSecrets) Set(_ context.Context, ref SecretRef, value string) error {
	s.set[ref]++
	if s.setErr != nil {
		return s.setErr
	}
	switch s.mode {
	case SecretModePassphraseLocked:
		return ErrSecretsLocked
	case SecretModeUnavailable:
		return ErrSecretsUnavailable
	}
	s.values[ref] = value
	return nil
}

func (s *fakeSecrets) Delete(_ context.Context, ref SecretRef) error {
	s.delete[ref]++
	if s.deleteErr != nil {
		return s.deleteErr
	}
	switch s.mode {
	case SecretModePassphraseLocked:
		return ErrSecretsLocked
	case SecretModeUnavailable:
		return ErrSecretsUnavailable
	}
	delete(s.values, ref)
	return nil
}

// fakeMetadata is the device-local metadata port.
type fakeMetadata struct {
	metadata  domainhistory.Metadata
	loadErr   error
	saveErr   error
	saveCalls int
}

func newFakeMetadata() *fakeMetadata {
	return &fakeMetadata{metadata: domainhistory.Metadata{
		WriterUUID: testWriterUUID,
		HostImports: map[string]domainhistory.ImportCheckpoint{
			"host-1": {HistoryID: "imported"},
		},
	}}
}

func (m *fakeMetadata) HistoryMetadata(context.Context) (domainhistory.Metadata, error) {
	if m.loadErr != nil {
		return domainhistory.Metadata{}, m.loadErr
	}
	return m.metadata, nil
}

func (m *fakeMetadata) SaveHistoryMetadata(_ context.Context, metadata domainhistory.Metadata) error {
	m.saveCalls++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.metadata = metadata
	return nil
}

// fakeRemote implements the one external historysync seam. Ticket 13 never
// calls Download or Upload; the counters make that an assertion.
type fakeRemote struct {
	config Config

	session       AuthResult
	authErr       error
	capabilities  Capabilities
	capsErr       error
	statuses      []SeriesStatus
	statusErr     error
	validation    KeyValidation
	validationErr error

	authCalls       int
	capabilityCalls int
	statusCalls     int
	validationCalls int
	downloadCalls   int
	uploadCalls     int
	closeCalls      int
}

func newFakeRemote() *fakeRemote {
	return &fakeRemote{
		session:      AuthResult{Token: testToken, Account: testAccount},
		capabilities: validCapabilities(),
		validation:   KeyValidationUnverified,
	}
}

func (r *fakeRemote) Authenticate(context.Context) (AuthResult, error) {
	r.authCalls++
	if r.authErr != nil {
		return AuthResult{}, r.authErr
	}
	return r.session, nil
}

func (r *fakeRemote) Capabilities(context.Context) (Capabilities, error) {
	r.capabilityCalls++
	if r.capsErr != nil {
		return Capabilities{}, r.capsErr
	}
	return r.capabilities, nil
}

func (r *fakeRemote) RecordStatus(context.Context) ([]SeriesStatus, error) {
	r.statusCalls++
	if r.statusErr != nil {
		return nil, r.statusErr
	}
	return r.statuses, nil
}

func (r *fakeRemote) Download(context.Context, Series, int64, int) (Page, error) {
	r.downloadCalls++
	return Page{}, errors.New("ticket 13 must not download records")
}

func (r *fakeRemote) Upload(context.Context, []SyncRecord) error {
	r.uploadCalls++
	return errors.New("ticket 13 must not upload records")
}

func (r *fakeRemote) ValidateMasterKey(context.Context) (KeyValidation, error) {
	r.validationCalls++
	if r.validationErr != nil {
		return "", r.validationErr
	}
	return r.validation, nil
}

func (r *fakeRemote) Close() error {
	r.closeCalls++
	domainhistory.WipeMasterKey(r.config.MasterKey)
	r.config.Token = ""
	r.config.Password = ""
	return nil
}

type fakeFactory struct {
	remote     *fakeRemote
	openErr    error
	opens      int
	lastConfig Config
}

func (f *fakeFactory) Open(_ context.Context, config Config) (Remote, error) {
	f.opens++
	f.lastConfig = config
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.remote.config = config
	return f.remote, nil
}

type accountHarness struct {
	service  *AccountService
	settings *fakeSettings
	secrets  *fakeSecrets
	metadata *fakeMetadata
	remote   *fakeRemote
	factory  *fakeFactory
}

func newAccountHarness(t *testing.T) *accountHarness {
	t.Helper()
	settings := newFakeSettings()
	secrets := newFakeSecrets()
	metadata := newFakeMetadata()
	remote := newFakeRemote()
	factory := &fakeFactory{remote: remote}
	return &accountHarness{
		service:  NewAccountService(settings, secrets, metadata, factory),
		settings: settings,
		secrets:  secrets,
		metadata: metadata,
		remote:   remote,
		factory:  factory,
	}
}

func (h *accountHarness) loginPassword(t *testing.T) Status {
	t.Helper()
	status, err := h.service.Login(context.Background(), LoginRequest{
		ServerURL: testServer, Username: "alice", Password: "hunter2",
		AuthMode: atuinmodel.AuthModeSession,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return status
}

func (h *accountHarness) loginToken(t *testing.T, token string, mode atuinmodel.AuthMode) Status {
	t.Helper()
	status, err := h.service.Login(context.Background(), LoginRequest{
		ServerURL: testServer, Token: token, AuthMode: mode,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	return status
}

func (h *accountHarness) configureEmptyAccount(t *testing.T) {
	t.Helper()
	h.loginPassword(t)
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatalf("SetMasterKey: %v", err)
	}
}

func TestLoginWithUsernamePasswordStoresOnlyTheToken(t *testing.T) {
	h := newAccountHarness(t)
	status := h.loginPassword(t)

	if got := h.secrets.values[SessionTokenRef()]; got != testToken {
		t.Fatalf("stored token = %q, want the minted token", got)
	}
	if h.secrets.values[MasterKeyRef()] != "" {
		t.Fatal("login stored a master key")
	}
	if h.factory.lastConfig.AuthMode != atuinmodel.AuthModeSession ||
		h.factory.lastConfig.Username != "alice" || h.factory.lastConfig.Password != "hunter2" {
		t.Fatalf("login config = %+v", h.factory.lastConfig)
	}
	if h.settings.history.Username != "alice" ||
		h.settings.history.SyncAddress != testServer ||
		h.settings.history.AuthMode != atuinmodel.AuthModeSession {
		t.Fatalf("settings = %+v", h.settings.history)
	}
	if h.metadata.metadata.Account != (domainhistory.AccountIdentity{
		ServerURL: testServer, UserID: "user-1"}) {
		t.Fatalf("account identity = %+v", h.metadata.metadata.Account)
	}
	if h.remote.downloadCalls != 0 || h.remote.uploadCalls != 0 {
		t.Fatal("login synced records")
	}
	assertCredentialFree(t, "login status", status, testToken, "hunter2")
	assertCredentialFree(t, "settings", h.settings.history, testToken, "hunter2")
	assertCredentialFree(t, "metadata", h.metadata.metadata, testToken, "hunter2")
}

func TestLoginWithPastedBearerToken(t *testing.T) {
	h := newAccountHarness(t)
	h.remote.session = AuthResult{Account: testAccount}
	status := h.loginToken(t, "pasted-bearer-token", atuinmodel.AuthModeBearer)

	if got := h.secrets.values[SessionTokenRef()]; got != "pasted-bearer-token" {
		t.Fatalf("stored token = %q", got)
	}
	if h.factory.lastConfig.AuthMode != atuinmodel.AuthModeBearer ||
		h.factory.lastConfig.Token != "pasted-bearer-token" ||
		h.factory.lastConfig.Password != "" {
		t.Fatalf("login config = %+v", h.factory.lastConfig)
	}
	if h.settings.history.AuthMode != atuinmodel.AuthModeBearer {
		t.Fatalf("auth mode = %q", h.settings.history.AuthMode)
	}
	assertCredentialFree(t, "status", status, "pasted-bearer-token")
}

func TestLoginRejectsInvalidRequests(t *testing.T) {
	tests := []struct {
		name    string
		request LoginRequest
	}{
		{"token and password", LoginRequest{Token: "t", Password: "p"}},
		{"no credential", LoginRequest{Username: "alice"}},
		{"password without username", LoginRequest{Password: "p"}},
		{"password with bearer mode", LoginRequest{
			Username: "alice", Password: "p", AuthMode: atuinmodel.AuthModeBearer}},
	}
	for _, test := range tests {
		h := newAccountHarness(t)
		test.request.ServerURL = testServer
		if _, err := h.service.Login(context.Background(), test.request); !errors.Is(err, ErrInvalidLogin) {
			t.Errorf("%s: Login = %v, want ErrInvalidLogin", test.name, err)
		}
		if _, ok := h.secrets.values[SessionTokenRef()]; ok {
			t.Errorf("%s: invalid login stored a token", test.name)
		}
	}
}

func TestLoginFailsClosedOnAnIncompatibleServer(t *testing.T) {
	h := newAccountHarness(t)
	h.remote.capabilities.Major = 19
	if _, err := h.service.Login(context.Background(), LoginRequest{
		ServerURL: testServer, Username: "alice", Password: "hunter2",
	}); !errors.Is(err, ErrIncompatibleServer) {
		t.Fatalf("Login = %v, want ErrIncompatibleServer", err)
	}
	if _, ok := h.secrets.values[SessionTokenRef()]; ok {
		t.Fatal("an incompatible server's token was stored")
	}
	if h.settings.history.Username != "" {
		t.Fatalf("an incompatible login changed settings: %+v", h.settings.history)
	}
}

func TestLoginRejectsAnAccountChangeWithoutLogout(t *testing.T) {
	h := newAccountHarness(t)
	h.metadata.metadata.Account = domainhistory.AccountIdentity{
		ServerURL: "https://old.example.test", UserID: "someone-else"}
	h.secrets.values[SessionTokenRef()] = "connected-token"
	if _, err := h.service.Login(context.Background(), LoginRequest{
		ServerURL: testServer, Username: "alice", Password: "hunter2",
	}); !errors.Is(err, ErrAccountChangeRequiresLogout) {
		t.Fatalf("Login = %v, want ErrAccountChangeRequiresLogout", err)
	}
	if got := h.secrets.values[SessionTokenRef()]; got != "connected-token" {
		t.Fatalf("the connected token was replaced: %q", got)
	}
	if h.metadata.metadata.Account.UserID != "someone-else" {
		t.Fatalf("the connected account identity was replaced: %+v", h.metadata.metadata.Account)
	}
}

func TestLoginRequiresAnUnlockedSecretStore(t *testing.T) {
	for _, test := range []struct {
		mode SecretMode
		err  error
	}{
		{SecretModePassphraseLocked, ErrSecretsLocked},
		{SecretModeUnavailable, ErrSecretsUnavailable},
	} {
		h := newAccountHarness(t)
		h.secrets.mode = test.mode
		status, err := h.service.Login(context.Background(), LoginRequest{
			ServerURL: testServer, Username: "alice", Password: "hunter2",
		})
		if !errors.Is(err, test.err) {
			t.Errorf("%s: Login = %v, want %v", test.mode, err, test.err)
		}
		if test.mode == SecretModePassphraseLocked && status.State != atuinmodel.SyncLocked {
			t.Errorf("%s: state = %q, want locked", test.mode, status.State)
		}
		if h.factory.opens != 0 {
			t.Errorf("%s: opened a remote with a locked secret store", test.mode)
		}
	}
}

func TestSetMasterKeyValidatesAgainstARealRecord(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	h.remote.statuses = []SeriesStatus{{
		Series:   Series{SeriesHost: "remote-writer", Tag: domainhistory.RecordTagHistory},
		MaxIndex: 3,
	}}
	h.remote.validation = KeyValidationVerified

	status, err := h.service.SetMasterKey(context.Background(), testKeyB64, false)
	if err != nil {
		t.Fatalf("SetMasterKey: %v", err)
	}
	if got := h.secrets.values[MasterKeyRef()]; got != testKeyB64 {
		t.Fatalf("stored key = %q, want canonical base64", got)
	}
	wantFingerprint := domainhistory.KeyFingerprint(testKey)
	if h.metadata.metadata.KeyFingerprint != wantFingerprint {
		t.Fatalf("metadata fingerprint = %q, want %q", h.metadata.metadata.KeyFingerprint, wantFingerprint)
	}
	if status.KeyState != atuinmodel.KeyStateVerified || status.State != atuinmodel.SyncIdle {
		t.Fatalf("status = %+v, want verified idle", status)
	}
	if h.remote.validationCalls != 1 {
		t.Fatalf("validation calls = %d, want 1", h.remote.validationCalls)
	}
	assertCredentialFree(t, "status", status, testKeyB64, string(testKey))
	assertCredentialFree(t, "settings", h.settings.history, testKeyB64, string(testKey))
	assertCredentialFree(t, "metadata", h.metadata.metadata, testKeyB64, string(testKey))
}

func TestSetMasterKeyMarksAnEmptyAccountUnverified(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	h.remote.validation = KeyValidationUnverified

	status, err := h.service.SetMasterKey(context.Background(), testKeyB64, false)
	if err != nil {
		t.Fatalf("SetMasterKey: %v", err)
	}
	if status.KeyState != atuinmodel.KeyStateUnverified || status.Problem != atuinmodel.ProblemKeyUnverified {
		t.Fatalf("status = %+v, want an unverified key warning", status)
	}
	if h.remote.validationCalls != 0 {
		t.Fatalf("validated against an empty account (%d calls)", h.remote.validationCalls)
	}
	if h.secrets.values[MasterKeyRef()] == "" {
		t.Fatal("the unverified key was not stored")
	}
}

func TestSetMasterKeyRejectsAMismatchAndKeepsTheStoredKey(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatal(err)
	}
	h.remote.statuses = []SeriesStatus{{
		Series:   Series{SeriesHost: "remote-writer", Tag: domainhistory.RecordTagHistory},
		MaxIndex: 0,
	}}
	h.remote.validation = KeyValidationMismatch

	status, err := h.service.SetMasterKey(context.Background(), testKeyB64, false)
	if !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("SetMasterKey = %v, want ErrKeyMismatch", err)
	}
	if status.State != atuinmodel.SyncKeyMismatch || status.KeyState != atuinmodel.KeyStateMismatch {
		t.Fatalf("status = %+v, want key mismatch", status)
	}
	if got := h.secrets.values[MasterKeyRef()]; got != testKeyB64 {
		t.Fatalf("the rejected key replaced the stored key: %q", got)
	}
}

func TestSetMasterKeyRequiresConfirmationForAKeyChange(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	h.metadata.metadata.Series = map[string]domainhistory.SeriesCheckpoint{
		"remote-writer:history": {SeriesHost: "remote-writer", Tag: "history", AppliedIndex: 2, UploadedIndex: 1},
	}
	newKey := []byte("fedcba9876543210fedcba9876543210")
	newKeyB64 := base64.StdEncoding.EncodeToString(newKey)
	h.remote.statuses = []SeriesStatus{{
		Series:   Series{SeriesHost: "remote-writer", Tag: domainhistory.RecordTagHistory},
		MaxIndex: 2,
	}}
	h.remote.validation = KeyValidationMismatch

	if _, err := h.service.SetMasterKey(context.Background(), newKeyB64, false); !errors.Is(err, ErrKeyChangeConfirmation) {
		t.Fatalf("SetMasterKey without confirmation = %v, want ErrKeyChangeConfirmation", err)
	}
	if h.metadata.metadata.KeyFingerprint != domainhistory.KeyFingerprint(testKey) {
		t.Fatal("a rejected key change altered the fingerprint")
	}
	if h.metadata.metadata.Series == nil {
		t.Fatal("a rejected key change cleared the sync checkpoints")
	}

	status, err := h.service.SetMasterKey(context.Background(), newKeyB64, true)
	if err != nil {
		t.Fatalf("confirmed SetMasterKey: %v", err)
	}
	if got := h.secrets.values[MasterKeyRef()]; got != newKeyB64 {
		t.Fatalf("stored key = %q, want the new key", got)
	}
	if h.metadata.metadata.KeyFingerprint != domainhistory.KeyFingerprint(newKey) {
		t.Fatal("the fingerprint was not updated")
	}
	if h.metadata.metadata.Series != nil {
		t.Fatal("the confirmed key change did not reset sync checkpoints")
	}
	if h.metadata.metadata.WriterUUID != testWriterUUID {
		t.Fatal("the writer UUID was lost on key change")
	}
	if _, ok := h.metadata.metadata.HostImports["host-1"]; !ok {
		t.Fatal("import checkpoints were lost on key change")
	}
	if status.KeyState != atuinmodel.KeyStateMismatch || status.State != atuinmodel.SyncKeyMismatch {
		t.Fatalf("status = %+v, want an unreadable-remote-history warning", status)
	}
}

func TestLogoutKeepsLocalIdentityAndFingerprint(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	fingerprint := domainhistory.KeyFingerprint(testKey)
	h.metadata.metadata.KeyFingerprint = fingerprint
	h.metadata.metadata.ShellHistoryImported = true
	h.metadata.metadata.Series = map[string]domainhistory.SeriesCheckpoint{
		"remote-writer:history": {SeriesHost: "remote-writer", Tag: "history", AppliedIndex: 2},
	}

	status, err := h.service.Logout(context.Background())
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, ok := h.secrets.values[SessionTokenRef()]; ok {
		t.Fatal("logout kept the session token")
	}
	if _, ok := h.secrets.values[MasterKeyRef()]; ok {
		t.Fatal("logout kept the master key")
	}
	wantAccount := domainhistory.AccountIdentity{ServerURL: testServer, UserID: "user-1"}
	if h.metadata.metadata.Account != wantAccount {
		t.Fatalf("logout lost the account identity: %+v", h.metadata.metadata.Account)
	}
	if h.metadata.metadata.Series != nil {
		t.Fatal("logout kept sync checkpoints")
	}
	if h.metadata.metadata.WriterUUID != testWriterUUID {
		t.Fatal("logout lost the writer UUID")
	}
	if h.metadata.metadata.KeyFingerprint != fingerprint {
		t.Fatal("logout lost the key fingerprint")
	}
	if !h.metadata.metadata.ShellHistoryImported {
		t.Fatal("logout lost the shell-history import marker")
	}
	if _, ok := h.metadata.metadata.HostImports["host-1"]; !ok {
		t.Fatal("logout lost the Host import checkpoints")
	}
	if status.KeyFingerprint != fingerprint || status.KeyState != atuinmodel.KeyStateMissing {
		t.Fatalf("status = %+v, want the surviving fingerprint and a missing key", status)
	}

	// Logout disconnects the account. A later login may choose another
	// account; the stored identity is updated for the next sync to compare.
	h.remote.session = AuthResult{Account: Account{
		ServerURL: testServer, UserID: "user-2", Username: "bob"}}
	if _, err := h.service.Login(context.Background(), LoginRequest{
		ServerURL: testServer, Token: "new-account-token",
		AuthMode: atuinmodel.AuthModeSession,
	}); err != nil {
		t.Fatalf("login after logout: %v", err)
	}
	if h.metadata.metadata.Account.UserID != "user-2" {
		t.Fatalf("account identity = %+v, want the new account", h.metadata.metadata.Account)
	}
	if h.secrets.values[SessionTokenRef()] != "new-account-token" {
		t.Fatal("the new account's token was not stored")
	}
	if h.metadata.metadata.WriterUUID != testWriterUUID ||
		h.metadata.metadata.KeyFingerprint != fingerprint {
		t.Fatal("switching accounts after logout lost the writer UUID or fingerprint")
	}
}

func TestForgetMasterKeyKeepsTheAccount(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	fingerprint := domainhistory.KeyFingerprint(testKey)
	h.metadata.metadata.KeyFingerprint = fingerprint
	h.metadata.metadata.Series = map[string]domainhistory.SeriesCheckpoint{
		"remote-writer:history": {SeriesHost: "remote-writer", Tag: "history"},
	}

	status, err := h.service.ForgetMasterKey(context.Background())
	if err != nil {
		t.Fatalf("ForgetMasterKey: %v", err)
	}
	if _, ok := h.secrets.values[MasterKeyRef()]; ok {
		t.Fatal("the key was not deleted")
	}
	if h.secrets.values[SessionTokenRef()] != testToken {
		t.Fatal("forgetting the key also deleted the session token")
	}
	if h.metadata.metadata.Account.UserID != "user-1" {
		t.Fatal("forgetting the key lost the account identity")
	}
	if h.metadata.metadata.Series != nil {
		t.Fatal("forgetting the key kept sync checkpoints")
	}
	if h.metadata.metadata.KeyFingerprint != fingerprint || h.metadata.metadata.WriterUUID != testWriterUUID {
		t.Fatal("forgetting the key lost the fingerprint or writer UUID")
	}
	if status.KeyState != atuinmodel.KeyStateMissing || status.Problem != atuinmodel.ProblemKeyMissing {
		t.Fatalf("status = %+v, want a missing-key warning", status)
	}
}

func TestCheckValidatesTheAccountWithoutSyncingRecords(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	h.remote.statuses = []SeriesStatus{{
		Series:   Series{SeriesHost: "remote-writer", Tag: domainhistory.RecordTagHistory},
		MaxIndex: 1,
	}}
	h.remote.validation = KeyValidationVerified
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatal(err)
	}
	h.remote.authCalls = 0
	h.remote.capabilityCalls = 0
	h.remote.statusCalls = 0
	h.remote.validationCalls = 0
	h.remote.downloadCalls = 0
	h.remote.uploadCalls = 0

	status, err := h.service.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if status.State != atuinmodel.SyncIdle || status.KeyState != atuinmodel.KeyStateVerified {
		t.Fatalf("status = %+v, want verified idle", status)
	}
	if h.remote.authCalls == 0 || h.remote.capabilityCalls == 0 ||
		h.remote.statusCalls == 0 || h.remote.validationCalls == 0 {
		t.Fatalf("Check did not validate the account: %+v", h.remote)
	}
	if h.remote.downloadCalls != 0 || h.remote.uploadCalls != 0 {
		t.Fatal("Check synced records")
	}
}

func TestCheckMapsRemoteFailuresToSanitizedStatuses(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fakeRemote)
		state     atuinmodel.SyncState
		problem   atuinmodel.Problem
	}{
		{
			name:      "auth expired",
			configure: func(remote *fakeRemote) { remote.authErr = ErrAuthExpired },
			state:     atuinmodel.SyncAuthExpired,
			problem:   atuinmodel.ProblemAuthExpired,
		},
		{
			name:      "incompatible server",
			configure: func(remote *fakeRemote) { remote.capsErr = ErrIncompatibleServer },
			state:     atuinmodel.SyncIncompatibleServer,
			problem:   atuinmodel.ProblemIncompatibleServer,
		},
		{
			name:      "offline",
			configure: func(remote *fakeRemote) { remote.statusErr = ErrOffline },
			state:     atuinmodel.SyncOffline,
			problem:   atuinmodel.ProblemOffline,
		},
		{
			name:      "key mismatch",
			configure: func(remote *fakeRemote) { remote.validationErr = ErrKeyMismatch },
			state:     atuinmodel.SyncKeyMismatch,
			problem:   atuinmodel.ProblemKeyMismatch,
		},
		{
			name: "unknown error with a sensitive message",
			configure: func(remote *fakeRemote) {
				remote.statusErr = errors.New("token=" + testToken + " key=" + testKeyB64)
			},
			state:   atuinmodel.SyncError,
			problem: atuinmodel.ProblemError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newAccountHarness(t)
			h.loginPassword(t)
			h.remote.statuses = []SeriesStatus{{
				Series:   Series{SeriesHost: "remote-writer", Tag: domainhistory.RecordTagHistory},
				MaxIndex: 1,
			}}
			h.remote.validation = KeyValidationVerified
			if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
				t.Fatal(err)
			}
			test.configure(h.remote)
			status, err := h.service.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if status.State != test.state || status.Problem != test.problem {
				t.Fatalf("status = %+v, want state %q problem %q", status, test.state, test.problem)
			}
			assertCredentialFree(t, "status", status, testToken, testKeyB64)
		})
	}
}

func TestCheckFailsClosedWhenHistoryExistsButKeyIsUnverified(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	h.remote.statuses = []SeriesStatus{{
		Series:   Series{SeriesHost: "remote-writer", Tag: domainhistory.RecordTagHistory},
		MaxIndex: 1,
	}}
	h.remote.validation = KeyValidationVerified
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatal(err)
	}
	h.remote.validation = KeyValidationUnverified

	status, err := h.service.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if status.State != atuinmodel.SyncError || status.Problem != atuinmodel.ProblemError {
		t.Fatalf("status = %+v, want a fail-closed error", status)
	}
	if status.KeyState != atuinmodel.KeyStateUnverified {
		t.Fatalf("key state = %q, want unverified", status.KeyState)
	}
	assertCredentialFree(t, "status", status, testKeyB64)
}

func TestCheckDoesNothingWhileDisabledLockedOrUnconfigured(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		h := newAccountHarness(t)
		h.settings.history.SyncEnabled = false
		status, err := h.service.Check(context.Background())
		if err != nil || status.State != atuinmodel.SyncDisabled {
			t.Fatalf("Check = %+v, %v", status, err)
		}
		if h.factory.opens != 0 {
			t.Fatal("a disabled sync opened a remote")
		}
	})
	t.Run("locked", func(t *testing.T) {
		h := newAccountHarness(t)
		h.secrets.mode = SecretModePassphraseLocked
		status, err := h.service.Check(context.Background())
		if err != nil || status.State != atuinmodel.SyncLocked {
			t.Fatalf("Check = %+v, %v", status, err)
		}
		if h.factory.opens != 0 {
			t.Fatal("a locked store opened a remote")
		}
	})
	t.Run("not configured", func(t *testing.T) {
		h := newAccountHarness(t)
		status, err := h.service.Check(context.Background())
		if err != nil || status.State != atuinmodel.SyncNotConfigured {
			t.Fatalf("Check = %+v, %v", status, err)
		}
		if h.factory.opens != 0 {
			t.Fatal("an unconfigured account opened a remote")
		}
	})
	t.Run("missing key", func(t *testing.T) {
		h := newAccountHarness(t)
		h.loginPassword(t)
		h.factory.opens = 0
		status, err := h.service.Check(context.Background())
		if err != nil || status.State != atuinmodel.SyncNotConfigured ||
			status.Problem != atuinmodel.ProblemKeyMissing {
			t.Fatalf("Check = %+v, %v", status, err)
		}
		if h.factory.opens != 0 {
			t.Fatal("a missing key opened a remote")
		}
	})
}

func TestCheckReportsAMissingRemoteAdapterAsNotConfigured(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	h.service.factory = nil

	status, err := h.service.Check(context.Background())
	if !errors.Is(err, ErrRemoteUnavailable) {
		t.Fatalf("Check = %v, want ErrRemoteUnavailable", err)
	}
	if status.State != atuinmodel.SyncNotConfigured ||
		status.Problem != atuinmodel.ProblemNotConfigured {
		t.Fatalf("status = %+v, want not configured", status)
	}
}

func TestStoredCredentialsFailClosedAfterTheSyncAddressChanges(t *testing.T) {
	t.Run("Check", func(t *testing.T) {
		h := newAccountHarness(t)
		h.configureEmptyAccount(t)
		opens := h.factory.opens
		gets := h.secrets.get[SessionTokenRef()]
		h.settings.history.SyncAddress = "https://evil.example.test"
		status, err := h.service.Check(context.Background())
		if !errors.Is(err, ErrAccountChangeRequiresLogout) {
			t.Fatalf("Check = %v, want ErrAccountChangeRequiresLogout", err)
		}
		if h.factory.opens != opens {
			t.Fatalf("Check opened %d remote(s) with stored credentials after the address changed", h.factory.opens-opens)
		}
		if h.secrets.get[SessionTokenRef()] != gets {
			t.Fatal("Check decrypted the token before the address ownership check")
		}
		if status.State != atuinmodel.SyncError {
			t.Fatalf("status = %+v, want error", status)
		}
		assertCredentialFree(t, "status", status, testToken, testKeyB64)
	})

	t.Run("SetMasterKey", func(t *testing.T) {
		h := newAccountHarness(t)
		h.loginPassword(t)
		opens := h.factory.opens
		h.settings.history.SyncAddress = "https://evil.example.test"
		status, err := h.service.SetMasterKey(context.Background(), testKeyB64, false)
		if !errors.Is(err, ErrAccountChangeRequiresLogout) {
			t.Fatalf("SetMasterKey = %v, want ErrAccountChangeRequiresLogout", err)
		}
		if h.factory.opens != opens {
			t.Fatalf("SetMasterKey opened %d remote(s) with stored credentials after the address changed", h.factory.opens-opens)
		}
		if _, ok := h.secrets.values[MasterKeyRef()]; ok {
			t.Fatal("SetMasterKey stored a key after the address changed")
		}
		if status.State != atuinmodel.SyncError {
			t.Fatalf("status = %+v, want error", status)
		}
		assertCredentialFree(t, "status", status, testToken, testKeyB64)
	})

	t.Run("Login", func(t *testing.T) {
		h := newAccountHarness(t)
		h.loginPassword(t)
		opens := h.factory.opens
		status, err := h.service.Login(context.Background(), LoginRequest{
			ServerURL: "https://evil.example.test", Username: "alice", Password: "hunter2",
		})
		if !errors.Is(err, ErrAccountChangeRequiresLogout) {
			t.Fatalf("Login = %v, want ErrAccountChangeRequiresLogout", err)
		}
		if h.factory.opens != opens {
			t.Fatalf("Login opened %d remote(s) before the account ownership check", h.factory.opens-opens)
		}
		if status.State != atuinmodel.SyncError {
			t.Fatalf("status = %+v, want error", status)
		}
	})
}

func TestTheConnectedAccountAcceptsAHostCaseVariation(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	h.settings.history.SyncAddress = "HTTPS://ATUIN.EXAMPLE.TEST/"
	opens := h.factory.opens
	status, err := h.service.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if h.factory.opens != opens+1 {
		t.Fatal("a case-only address change did not reach the server")
	}
	if got := h.factory.lastConfig.BaseURL; got != testServer {
		t.Fatalf("effective address = %q, want %q", got, testServer)
	}
	if status.State != atuinmodel.SyncIdle {
		t.Fatalf("status = %+v", status)
	}
}

func TestStatusPreservesAnUnverifiedKeyWarningAfterSetMasterKey(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatal(err)
	}
	status := h.service.Status()
	if status.Problem != atuinmodel.ProblemKeyUnverified ||
		status.KeyState != atuinmodel.KeyStateUnverified {
		t.Fatalf("Status = %+v, want the unverified-key warning", status)
	}
	if status.KeyConfirmed {
		t.Fatal("an unconfirmed key reported as confirmed")
	}
}

func TestConfirmUnverifiedKeyPersistsForTheSameKey(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatal(err)
	}
	status, err := h.service.ConfirmUnverifiedKey(context.Background())
	if err != nil {
		t.Fatalf("ConfirmUnverifiedKey: %v", err)
	}
	if !status.KeyConfirmed || status.KeyState != atuinmodel.KeyStateUnverified ||
		status.Problem != atuinmodel.ProblemKeyUnverified {
		t.Fatalf("status = %+v, want a confirmed unverified key", status)
	}
	if !h.metadata.metadata.UnverifiedKeyConfirmed {
		t.Fatal("the confirmation was not persisted")
	}
	if got := h.service.Status(); !got.KeyConfirmed {
		t.Fatalf("Status lost the confirmation: %+v", got)
	}

	// Re-entering the same key keeps the confirmation.
	if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
		t.Fatal(err)
	}
	if !h.metadata.metadata.UnverifiedKeyConfirmed {
		t.Fatal("re-entering the same key cleared the confirmation")
	}

	// A different key is a new risk and must be confirmed again.
	newKey := base64.StdEncoding.EncodeToString([]byte("00112233445566778899aabbccddeeff"))
	if _, err := h.service.SetMasterKey(context.Background(), newKey, true); err != nil {
		t.Fatal(err)
	}
	if h.metadata.metadata.UnverifiedKeyConfirmed {
		t.Fatal("a key change kept the previous key's confirmation")
	}
	if got := h.service.Status(); got.KeyConfirmed {
		t.Fatalf("Status kept the previous key's confirmation: %+v", got)
	}
}

func TestConfirmUnverifiedKeyRequiresAStoredKey(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	status, err := h.service.ConfirmUnverifiedKey(context.Background())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("ConfirmUnverifiedKey = %v, want ErrNotConfigured", err)
	}
	if status.Problem != atuinmodel.ProblemKeyMissing || status.KeyConfirmed {
		t.Fatalf("status = %+v, want a missing-key error", status)
	}
}

func TestKeyLossAndLogoutClearTheUnverifiedConfirmation(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*AccountService) (Status, error)
	}{
		{"forget", func(service *AccountService) (Status, error) {
			return service.ForgetMasterKey(context.Background())
		}},
		{"logout", func(service *AccountService) (Status, error) {
			return service.Logout(context.Background())
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAccountHarness(t)
			h.loginPassword(t)
			if _, err := h.service.SetMasterKey(context.Background(), testKeyB64, false); err != nil {
				t.Fatal(err)
			}
			if _, err := h.service.ConfirmUnverifiedKey(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := test.run(h.service); err != nil {
				t.Fatal(err)
			}
			if h.metadata.metadata.UnverifiedKeyConfirmed {
				t.Fatal("the confirmation survived losing the key")
			}
			if got := h.service.Status(); got.KeyConfirmed {
				t.Fatalf("Status kept a confirmation without a key: %+v", got)
			}
		})
	}
}

func TestSetMasterKeyReportsDisabledWhenSyncIsOff(t *testing.T) {
	h := newAccountHarness(t)
	h.loginPassword(t)
	h.settings.history.SyncEnabled = false
	status, err := h.service.SetMasterKey(context.Background(), testKeyB64, false)
	if err != nil {
		t.Fatalf("SetMasterKey: %v", err)
	}
	if status.State != atuinmodel.SyncDisabled ||
		status.Problem != atuinmodel.ProblemDisabled {
		t.Fatalf("status = %+v, want disabled", status)
	}
	if got := h.service.Status(); got.State != atuinmodel.SyncDisabled {
		t.Fatalf("Status = %+v, want disabled", got)
	}
}

func TestSanitizeServerURLNormalizesHostCase(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{" HTTPS://API.ATUIN.SH/ ", "https://api.atuin.sh"},
		{"https://Atuin.Example.Test/base/", "https://atuin.example.test/base"},
		{"not a url/", "not a url"},
	} {
		if got := SanitizeServerURL(test.raw); got != test.want {
			t.Errorf("SanitizeServerURL(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
}

func TestStatusNeverDecryptsTheMasterKey(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	masterRef := MasterKeyRef()
	h.secrets.get[masterRef] = 0
	_ = h.service.Status()
	_ = h.service.Status()
	if h.secrets.get[masterRef] != 0 {
		t.Fatalf("Status decrypted the master key %d times", h.secrets.get[masterRef])
	}
}

func TestStatusReportsDisabledAfterConfiguration(t *testing.T) {
	h := newAccountHarness(t)
	h.configureEmptyAccount(t)
	h.settings.history.SyncEnabled = false
	status := h.service.Status()
	if status.State != atuinmodel.SyncDisabled || status.Problem != atuinmodel.ProblemDisabled {
		t.Fatalf("status = %+v, want disabled", status)
	}
}

func TestNormalizeSyncAddress(t *testing.T) {
	valid := []struct {
		raw  string
		want string
	}{
		{"https://api.atuin.sh/", "https://api.atuin.sh"},
		{"HTTPS://API.ATUIN.SH/", "https://api.atuin.sh"},
		{"  https://atuin.example.test/base/  ", "https://atuin.example.test/base"},
		{"https://atuin.example.test/#", "https://atuin.example.test"},
		{"http://localhost:8888", "http://localhost:8888"},
		{"http://127.0.0.1:8888/", "http://127.0.0.1:8888"},
		{"http://[::1]:8888", "http://[::1]:8888"},
	}
	for _, test := range valid {
		got, err := NormalizeSyncAddress(test.raw)
		if err != nil || got != test.want {
			t.Errorf("NormalizeSyncAddress(%q) = %q, %v, want %q", test.raw, got, err, test.want)
		}
	}
	invalid := []string{
		"",
		"atuin.example.test",
		"http://atuin.example.test",
		"ftp://atuin.example.test",
		"https://user:pass@atuin.example.test",
		"https://atuin.example.test?token=x",
		"https://atuin.example.test/#fragment",
		"https://",
	}
	for _, raw := range invalid {
		if got, err := NormalizeSyncAddress(raw); !errors.Is(err, ErrInvalidSyncAddress) {
			t.Errorf("NormalizeSyncAddress(%q) = %q, %v, want ErrInvalidSyncAddress", raw, got, err)
		}
	}
}

func assertCredentialFree(t *testing.T, label string, value any, credentials ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("%s: json: %v", label, err)
	}
	text := string(encoded) + fmt.Sprintf("%+v", value)
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		if strings.Contains(text, credential) {
			t.Errorf("%s leaked credential %q: %s", label, credential, text)
		}
	}
}
