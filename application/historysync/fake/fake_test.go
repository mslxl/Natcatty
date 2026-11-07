package fake_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	historysync "github.com/mslxl/natcatty/application/historysync"
	"github.com/mslxl/natcatty/application/historysync/fake"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
)

const (
	fakeServer     = "https://atuin.self-hosted.test"
	fakeUsername   = "alice"
	fakePassword   = "hunter2"
	fakeToken      = "minted-account-token"
	fakeWriterUUID = "0198c7e2-6a1b-7c3d-8e4f-5a6b7c8d9e0f"
)

var fakeMasterKey = base64.StdEncoding.EncodeToString(
	[]byte("0123456789abcdef0123456789abcdef"))

type testSettings struct {
	history domainpreferences.History
}

func newTestSettings() *testSettings {
	history := domainpreferences.Defaults().History
	history.SyncEnabled = true
	history.SyncAddress = fakeServer
	return &testSettings{history: history}
}

func (s *testSettings) HistorySettings() domainpreferences.History { return s.history }

func (s *testSettings) UpdateHistorySettings(change func(*domainpreferences.History)) error {
	change(&s.history)
	return nil
}

type testSecrets struct {
	values map[historysync.SecretRef]string
}

func newTestSecrets() *testSecrets {
	return &testSecrets{values: map[historysync.SecretRef]string{}}
}

func (s *testSecrets) Mode() historysync.SecretMode { return historysync.SecretModeOS }

func (s *testSecrets) Has(_ context.Context, ref historysync.SecretRef) (bool, error) {
	_, ok := s.values[ref]
	return ok, nil
}

func (s *testSecrets) Get(_ context.Context, ref historysync.SecretRef) (string, error) {
	value, ok := s.values[ref]
	if !ok {
		return "", historysync.ErrSecretNotFound
	}
	return value, nil
}

func (s *testSecrets) Set(_ context.Context, ref historysync.SecretRef, value string) error {
	s.values[ref] = value
	return nil
}

func (s *testSecrets) Delete(_ context.Context, ref historysync.SecretRef) error {
	delete(s.values, ref)
	return nil
}

type testMetadata struct {
	metadata domainhistory.Metadata
}

func newTestMetadata() *testMetadata {
	return &testMetadata{metadata: domainhistory.Metadata{WriterUUID: fakeWriterUUID}}
}

func (m *testMetadata) HistoryMetadata(context.Context) (domainhistory.Metadata, error) {
	return m.metadata, nil
}

func (m *testMetadata) SaveHistoryMetadata(_ context.Context, metadata domainhistory.Metadata) error {
	m.metadata = metadata
	return nil
}

func TestFakeAdapterValidatesAccountAndReportsStatusWithoutSyncing(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		ExpectedUsername: fakeUsername,
		ExpectedPassword: fakePassword,
		MintedToken:      fakeToken,
		Statuses:         []historysync.SeriesStatus{fake.HistoryStatus(4)},
		Validation:       historysync.KeyValidationVerified,
	})
	settings := newTestSettings()
	secrets := newTestSecrets()
	metadata := newTestMetadata()
	service := historysync.NewAccountService(settings, secrets, metadata, factory)
	ctx := context.Background()

	login, err := service.Login(ctx, historysync.LoginRequest{
		ServerURL: fakeServer,
		Username:  fakeUsername,
		Password:  fakePassword,
		AuthMode:  atuinmodel.AuthModeSession,
	})
	if err != nil {
		t.Fatalf("Login through the fake adapter: %v", err)
	}
	loginRemote := factory.Last()
	if got := secrets.values[historysync.SessionTokenRef()]; got != fakeToken {
		t.Fatalf("stored token = %q, want the fake-minted token", got)
	}
	if login.ServerURL != fakeServer || login.Username != fakeUsername ||
		login.AccountID != "fake-atuin-user" || login.ServerVersion != historysync.PinnedAtuinVersion {
		t.Fatalf("login status = %+v", login)
	}
	if login.KeyState != atuinmodel.KeyStateNone {
		t.Fatalf("key state = %q, want none before a key is entered", login.KeyState)
	}
	if loginRemote == nil || loginRemote.AuthMode() != atuinmodel.AuthModeSession {
		t.Fatalf("fake remote did not see the auth mode: %+v", loginRemote)
	}
	assertNoSync(t, "login", loginRemote)

	configured, err := service.SetMasterKey(ctx, fakeMasterKey, false)
	if err != nil {
		t.Fatalf("SetMasterKey through the fake adapter: %v", err)
	}
	keyRemote := factory.Last()
	if configured.State != atuinmodel.SyncIdle || configured.KeyState != atuinmodel.KeyStateVerified {
		t.Fatalf("configured status = %+v, want verified idle", configured)
	}
	if configured.KeyFingerprint != domainhistory.KeyFingerprint(mustKey(t)) {
		t.Fatalf("fingerprint = %q", configured.KeyFingerprint)
	}
	assertNoSync(t, "key validation", keyRemote)
	if keyRemote.Calls().ValidateMasterKey != 1 {
		t.Fatalf("key validation calls = %d, want 1", keyRemote.Calls().ValidateMasterKey)
	}

	checked, err := service.Check(ctx)
	if err != nil {
		t.Fatalf("Check through the fake adapter: %v", err)
	}
	checkRemote := factory.Last()
	if checked.State != atuinmodel.SyncIdle || checked.KeyState != atuinmodel.KeyStateVerified {
		t.Fatalf("checked status = %+v, want verified idle", checked)
	}
	assertNoSync(t, "status check", checkRemote)
	if checkRemote.Calls().RecordStatus == 0 {
		t.Fatal("Check did not read the fake remote's record status")
	}
	if loginRemote == keyRemote || keyRemote == checkRemote {
		t.Fatal("the factory did not scope a new Remote per operation")
	}
	if factory.Opens() != 3 {
		t.Fatalf("factory opened %d remotes, want 3", factory.Opens())
	}
	if !loginRemote.Closed() || !keyRemote.Closed() || !checkRemote.Closed() {
		t.Fatal("an account operation did not close its scoped Remote")
	}

	if got := secrets.values[historysync.MasterKeyRef()]; got != fakeMasterKey {
		t.Fatalf("stored key = %q, want the canonical base64 key", got)
	}
	assertCredentialFree(t, "login status", login, fakePassword, fakeToken)
	assertCredentialFree(t, "configured status", configured, fakePassword, fakeToken, fakeMasterKey)
	assertCredentialFree(t, "settings", settings.history, fakePassword, fakeToken, fakeMasterKey)
	assertCredentialFree(t, "metadata", metadata.metadata, fakePassword, fakeToken, fakeMasterKey)
}

func TestFakeAdapterRequiresExplicitConfirmationForAnEmptyAccountKey(t *testing.T) {
	factory := fake.NewFactory(fake.Options{})
	settings := newTestSettings()
	metadata := newTestMetadata()
	service := historysync.NewAccountService(settings, newTestSecrets(), metadata, factory)
	ctx := context.Background()
	if _, err := service.Login(ctx, historysync.LoginRequest{
		ServerURL: fakeServer, Username: fakeUsername, Password: fakePassword,
	}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	status, err := service.SetMasterKey(ctx, fakeMasterKey, false)
	if err != nil {
		t.Fatalf("SetMasterKey: %v", err)
	}
	keyRemote := factory.Last()
	if status.KeyState != atuinmodel.KeyStateUnverified ||
		status.Problem != atuinmodel.ProblemKeyUnverified || status.KeyConfirmed {
		t.Fatalf("status = %+v, want an unconfirmed unverified key", status)
	}
	if keyRemote.Calls().ValidateMasterKey != 0 {
		t.Fatal("an empty account called real-record validation")
	}
	if metadata.metadata.UnverifiedKeyConfirmed {
		t.Fatal("an empty-account key was confirmed without the user's action")
	}

	confirmed, err := service.ConfirmUnverifiedKey(ctx)
	if err != nil {
		t.Fatalf("ConfirmUnverifiedKey: %v", err)
	}
	if !confirmed.KeyConfirmed || confirmed.KeyState != atuinmodel.KeyStateUnverified {
		t.Fatalf("confirmed status = %+v", confirmed)
	}
	if !metadata.metadata.UnverifiedKeyConfirmed {
		t.Fatal("the confirmation was not persisted for the upload gate")
	}
	if got := service.Status(); !got.KeyConfirmed {
		t.Fatalf("Status = %+v, want the persisted confirmation", got)
	}
}

func TestFakeAdapterRejectsAWrongPastedToken(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		ExpectedToken: "the-only-valid-token",
		Statuses:      []historysync.SeriesStatus{fake.HistoryStatus(1)},
	})
	secrets := newTestSecrets()
	metadata := newTestMetadata()
	service := historysync.NewAccountService(newTestSettings(), secrets, metadata, factory)

	status, err := service.Login(context.Background(), historysync.LoginRequest{
		ServerURL: fakeServer,
		Token:     "wrong-token",
		AuthMode:  atuinmodel.AuthModeBearer,
	})
	if !errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("Login = %v, want ErrAuthFailed", err)
	}
	if status.State != atuinmodel.SyncAuthExpired || status.Problem != atuinmodel.ProblemAuthExpired {
		t.Fatalf("status = %+v, want auth expired", status)
	}
	if _, ok := secrets.values[historysync.SessionTokenRef()]; ok {
		t.Fatal("a rejected token was stored")
	}
	if metadata.metadata.Account != (domainhistory.AccountIdentity{}) {
		t.Fatal("a rejected login recorded an account")
	}
	assertNoSync(t, "rejected login", factory.Last())
}

func TestFakeAdapterFailsClosedOnAnUnknownServerVersion(t *testing.T) {
	capabilities := fake.CompatibleCapabilities()
	capabilities.Major = 19
	factory := fake.NewFactory(fake.Options{Capabilities: capabilities})
	secrets := newTestSecrets()
	service := historysync.NewAccountService(newTestSettings(), secrets, newTestMetadata(), factory)

	status, err := service.Login(context.Background(), historysync.LoginRequest{
		ServerURL: fakeServer,
		Username:  fakeUsername,
		Password:  fakePassword,
	})
	if !errors.Is(err, historysync.ErrIncompatibleServer) {
		t.Fatalf("Login = %v, want ErrIncompatibleServer", err)
	}
	if status.State != atuinmodel.SyncIncompatibleServer {
		t.Fatalf("state = %q, want incompatible server", status.State)
	}
	if _, ok := secrets.values[historysync.SessionTokenRef()]; ok {
		t.Fatal("an incompatible server's token was stored")
	}
	assertNoSync(t, "incompatible login", factory.Last())
}

func TestFakeAdapterInjectsRemoteFailures(t *testing.T) {
	factory := fake.NewFactory(fake.Options{
		ExpectedToken:   "token",
		RecordStatusErr: historysync.ErrOffline,
		Statuses:        []historysync.SeriesStatus{fake.HistoryStatus(1)},
	})
	service := historysync.NewAccountService(newTestSettings(), newTestSecrets(), newTestMetadata(), factory)
	if _, err := service.Login(context.Background(), historysync.LoginRequest{
		ServerURL: fakeServer, Token: "token",
	}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	status, err := service.SetMasterKey(context.Background(), fakeMasterKey, false)
	if !errors.Is(err, historysync.ErrOffline) {
		t.Fatalf("SetMasterKey = %v, want ErrOffline", err)
	}
	if status.State != atuinmodel.SyncOffline {
		t.Fatalf("state = %q, want offline", status.State)
	}
	assertNoSync(t, "offline validation", factory.Last())
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	key, err := domainhistory.ParseMasterKey(fakeMasterKey)
	if err != nil {
		t.Fatalf("ParseMasterKey: %v", err)
	}
	return key
}

func assertNoSync(t *testing.T, label string, remote *fake.Remote) {
	t.Helper()
	if remote == nil {
		t.Fatalf("%s: no fake remote", label)
	}
	calls := remote.Calls()
	if calls.Download != 0 || calls.Upload != 0 {
		t.Fatalf("%s: fake adapter synced records: %+v", label, calls)
	}
}

func assertCredentialFree(t *testing.T, label string, value any, credentials ...string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("%s: json: %v", label, err)
	}
	text := fmt.Sprintf("%+v %s", value, encoded)
	for _, credential := range credentials {
		if credential == "" {
			continue
		}
		if strings.Contains(text, credential) {
			t.Errorf("%s leaked credential %q: %s", label, credential, text)
		}
	}
}
