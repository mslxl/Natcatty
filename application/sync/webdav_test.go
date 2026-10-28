package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	domainsync "github.com/mslxl/natcatty/domain/sync"
)

// fakeWebDAV is a deterministic WebDAVConnector and Remote. The adapter tests
// cover the real HTTP surface; these tests cover the service's gate, storage,
// and problem mapping.
type fakeWebDAV struct {
	mu sync.Mutex

	prepareErr error
	connectErr error
	readErr    error
	writeErr   error

	prepared      WebDAVSettings
	preparedCount int
	connectCalls  int
	connects      int
	lastSettings  WebDAVSettings
	grant         Grant
	file          RemoteFile
	exists        bool
	readCount     int
	writes        [][]byte
	writeCount    int
	etag          string
	connectToken  domainsync.Token
	returnNoToken bool
}

func (f *fakeWebDAV) Prepare(settings WebDAVSettings) (WebDAVSettings, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preparedCount++
	if f.prepareErr != nil {
		return settings, false, f.prepareErr
	}
	if strings.TrimSpace(settings.Endpoint) == "" {
		return settings, false, ErrInvalidEndpoint
	}
	normalized := WebDAVSettings{
		Endpoint: strings.TrimRight(strings.TrimSpace(settings.Endpoint), "/"),
		AuthType: domainsync.NormalizeWebDAVAuthType(settings.AuthType),
		Username: strings.TrimSpace(settings.Username),
		Password: settings.Password,
		Token:    strings.TrimSpace(settings.Token),
	}
	f.prepared = normalized
	return normalized, strings.HasPrefix(normalized.Endpoint, "http://"), nil
}

func (f *fakeWebDAV) Connect(_ context.Context, settings WebDAVSettings) (domainsync.Token, Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	if f.connectErr != nil {
		return domainsync.Token{}, Grant{}, f.connectErr
	}
	f.lastSettings = settings
	grant := f.grant
	if grant.ID == "" {
		identity := settings.Endpoint
		login := strings.TrimPrefix(strings.TrimPrefix(settings.Endpoint, "https://"), "http://")
		if settings.AuthType == domainsync.WebDAVAuthBasic || settings.AuthType == domainsync.WebDAVAuthDigest {
			identity += "#" + settings.Username
			login = settings.Username + "@" + strings.TrimPrefix(strings.TrimPrefix(settings.Endpoint, "https://"), "http://")
		}
		grant = Grant{ID: identity, Login: login}
	}
	token := domainsync.Token{
		Provider:  ProviderWebDAV,
		TokenType: "WebDAV",
		WebDAV: &domainsync.WebDAVCredential{
			Endpoint: settings.Endpoint,
			AuthType: settings.AuthType,
			Username: settings.Username,
			Password: settings.Password,
			Token:    settings.Token,
		},
	}
	return token, grant, nil
}

func (f *fakeWebDAV) User(context.Context, domainsync.Token) (Grant, error) {
	return f.grant, nil
}

func (f *fakeWebDAV) Read(context.Context, domainsync.Token) (RemoteFile, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCount++
	if f.readErr != nil {
		return RemoteFile{}, false, f.readErr
	}
	return f.file, f.exists, nil
}

func (f *fakeWebDAV) Write(_ context.Context, _ domainsync.Token, current RemoteFile, content []byte) (RemoteFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCount++
	if f.writeErr != nil {
		return RemoteFile{}, f.writeErr
	}
	f.writes = append(f.writes, append([]byte(nil), content...))
	f.etag = "etag-" + string(rune('0'+f.writeCount))
	f.file = RemoteFile{ID: "https://dav.test/dav/netcatty-vault.json", ETag: f.etag, Content: append([]byte(nil), content...)}
	f.exists = true
	return f.file, nil
}

func newWebDAVTestService(t *testing.T, connector *fakeWebDAV, config Config, tokens *memoryTokens) (*Service, *fakeVault, *memoryConfigs) {
	t.Helper()
	if tokens == nil {
		tokens = &memoryTokens{}
	}
	vault := &fakeVault{payload: payloadWithHost("local", 1000)}
	configs := &memoryConfigs{config: config}
	service := NewService(connector, vault, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithRemote(ProviderWebDAV, connector),
		WithClock(func() time.Time { return time.UnixMilli(5000) }),
		WithDebounce(10*time.Millisecond),
	)
	if err := service.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := service.SetPassword("master-password"); err != nil {
		t.Fatalf("set password: %v", err)
	}
	return service, vault, configs
}

func insecureSettings() WebDAVSettings {
	return WebDAVSettings{
		Endpoint: "http://dav.test/dav",
		AuthType: domainsync.WebDAVAuthBasic,
		Username: "alice",
		Password: "http-password-秘密",
	}
}

func secureSettings() WebDAVSettings {
	return WebDAVSettings{
		Endpoint: "https://dav.test/dav/",
		AuthType: domainsync.WebDAVAuthBasic,
		Username: "alice",
		Password: "https-password-秘密",
	}
}

func TestConnectWebDAVRefusesInsecureWithoutConfirmation(t *testing.T) {
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{}
	service, _, configs := newWebDAVTestService(t, connector, Config{}, tokens)

	err := service.ConnectWebDAV(context.Background(), "master-password", insecureSettings(), false)
	if !errors.Is(err, ErrInsecureEndpoint) {
		t.Fatalf("connect = %v, want ErrInsecureEndpoint", err)
	}
	connector.mu.Lock()
	connectCalls, preparedCount := connector.connectCalls, connector.preparedCount
	connector.mu.Unlock()
	if connectCalls != 0 {
		t.Fatalf("Connect was called %d times before confirmation", connectCalls)
	}
	if preparedCount != 1 {
		t.Fatalf("Prepare calls = %d, want 1", preparedCount)
	}
	if tokens.set {
		t.Fatal("the credential was stored before confirmation")
	}
	if configs.config.Connected {
		t.Fatal("the profile was marked connected before confirmation")
	}
	snapshot := service.Snapshot()
	if snapshot.Problem != ProblemInsecureEndpoint {
		t.Fatalf("problem = %q", snapshot.Problem)
	}
	if snapshot.WebDAVEndpoint != "" {
		t.Fatalf("unconfirmed endpoint was cached: %q", snapshot.WebDAVEndpoint)
	}
}

func TestConnectWebDAVStoresSecretsOnlyInTheCredentialStore(t *testing.T) {
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{}
	service, _, configs := newWebDAVTestService(t, connector, Config{}, tokens)

	if err := service.ConnectWebDAV(context.Background(), "master-password", secureSettings(), false); err != nil {
		t.Fatalf("connect: %v", err)
	}
	connector.mu.Lock()
	last := connector.lastSettings
	connector.mu.Unlock()
	if last.Endpoint != "https://dav.test/dav" {
		t.Fatalf("connector got endpoint %q", last.Endpoint)
	}
	tokens.mu.Lock()
	stored := tokens.token
	set := tokens.set
	tokens.mu.Unlock()
	if !set || stored.Provider != ProviderWebDAV || stored.WebDAV == nil {
		t.Fatalf("stored token = %+v, set %v", stored, set)
	}
	if stored.WebDAV.Password != secureSettings().Password || stored.WebDAV.Username != "alice" {
		t.Fatalf("stored credential = %+v", stored.WebDAV)
	}
	encoded, err := json.Marshal(configs.config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, secret := range []string{secureSettings().Password, "https-password"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("config leaked a WebDAV secret: %s", encoded)
		}
	}
	if configs.config.Provider != ProviderWebDAV || !configs.config.Connected {
		t.Fatalf("config = %+v", configs.config)
	}
	snapshot := service.Snapshot()
	if snapshot.Provider != ProviderWebDAV || !snapshot.Connected || snapshot.WebDAVInsecure {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.WebDAVEndpoint != "https://dav.test/dav" || snapshot.WebDAVAuthType != domainsync.WebDAVAuthBasic ||
		snapshot.WebDAVUsername != "alice" {
		t.Fatalf("snapshot form fields = %+v", snapshot)
	}
	form := service.WebDAVSettings()
	if form.Password != "" || form.Token != "" {
		t.Fatalf("WebDAVSettings leaked a secret: %+v", form)
	}
	if form.Endpoint != "https://dav.test/dav" || form.Username != "alice" {
		t.Fatalf("WebDAVSettings = %+v", form)
	}
}

func TestConnectWebDAVConfirmsInsecureButNeverAutoSyncs(t *testing.T) {
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{}
	service, _, configs := newWebDAVTestService(t, connector, Config{}, tokens)

	if err := service.ConnectWebDAV(context.Background(), "master-password", insecureSettings(), true); err != nil {
		t.Fatalf("confirmed connect: %v", err)
	}
	if !service.Snapshot().WebDAVInsecure {
		t.Fatal("snapshot did not report the insecure endpoint")
	}
	if err := service.SetAutoSync(context.Background(), true); err != nil {
		t.Fatalf("enable auto sync: %v", err)
	}
	service.mu.Lock()
	scheduled, timer := service.scheduled, service.timer
	service.mu.Unlock()
	if scheduled || timer != nil {
		t.Fatal("automatic sync was scheduled for a plain-HTTP endpoint")
	}
	result, err := service.Sync(context.Background(), TriggerAuto)
	if !errors.Is(err, ErrInsecureEndpoint) {
		t.Fatalf("automatic sync = %v, want ErrInsecureEndpoint", err)
	}
	if result.Trigger != TriggerAuto {
		t.Fatalf("result = %+v", result)
	}
	connector.mu.Lock()
	reads := connector.readCount
	connector.mu.Unlock()
	if reads != 0 {
		t.Fatalf("automatic sync read the remote %d times", reads)
	}
	if configs.config.Connected != true {
		t.Fatalf("config = %+v", configs.config)
	}
}

func TestManualSyncOfARestoredInsecureProfileRequiresAFreshConfirmation(t *testing.T) {
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{token: domainsync.Token{
		Provider:  ProviderWebDAV,
		TokenType: "WebDAV",
		WebDAV: &domainsync.WebDAVCredential{
			Endpoint: "http://dav.test/dav",
			AuthType: domainsync.WebDAVAuthBasic,
			Username: "alice",
			Password: "http-password-秘密",
		},
	}, set: true}
	config := Config{Provider: ProviderWebDAV, Connected: true, Login: "alice@dav.test", AccountID: "http://dav.test/dav#alice"}
	service, _, _ := newWebDAVTestService(t, connector, config, tokens)

	if !service.Snapshot().WebDAVInsecure {
		t.Fatal("restored insecure profile was not reported")
	}
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrInsecureEndpoint) {
		t.Fatalf("manual sync without confirmation = %v", err)
	}
	connector.mu.Lock()
	reads := connector.readCount
	connector.mu.Unlock()
	if reads != 0 {
		t.Fatal("credential was sent before confirmation")
	}
	if !service.ConfirmInsecureEndpoint(context.Background()) {
		t.Fatal("insecure endpoint was not confirmable")
	}
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("confirmed manual sync: %v", err)
	}
	connector.mu.Lock()
	writes := connector.writeCount
	connector.mu.Unlock()
	if writes != 1 {
		t.Fatalf("writes = %d, want 1", writes)
	}
}

func TestConfirmInsecureEndpointRefusesSecureAndMissingCredentials(t *testing.T) {
	service, _, _ := newWebDAVTestService(t, &fakeWebDAV{}, Config{}, &memoryTokens{})
	if service.ConfirmInsecureEndpoint(context.Background()) {
		t.Fatal("missing credential was confirmable")
	}

	secureTokens := &memoryTokens{token: domainsync.Token{
		Provider: ProviderWebDAV,
		WebDAV:   &domainsync.WebDAVCredential{Endpoint: "https://dav.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "a", Password: "b"},
	}, set: true}
	secureService, _, _ := newWebDAVTestService(t, &fakeWebDAV{}, Config{}, secureTokens)
	if secureService.ConfirmInsecureEndpoint(context.Background()) {
		t.Fatal("HTTPS endpoint was reported insecure")
	}
}

func TestWebDAVProblemTaxonomy(t *testing.T) {
	tests := []struct {
		err     error
		problem Problem
	}{
		{ErrCredentialsRejected, ProblemCredentials},
		{ErrForbidden, ProblemForbidden},
		{ErrTLS, ProblemTLS},
		{ErrInvalidEndpoint, ProblemInvalidEndpoint},
		{ErrRedirectRefused, ProblemRedirect},
	}
	for _, test := range tests {
		t.Run(string(test.problem), func(t *testing.T) {
			connector := &fakeWebDAV{connectErr: test.err}
			service, _, _ := newWebDAVTestService(t, connector, Config{}, &memoryTokens{})
			err := service.ConnectWebDAV(context.Background(), "master-password", secureSettings(), false)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if problem := service.Snapshot().Problem; problem != test.problem {
				t.Fatalf("problem = %q, want %q", problem, test.problem)
			}
		})
	}
}

func TestWebDAVSyncRejectedCredentialDoesNotRefreshOrRetry(t *testing.T) {
	connector := &fakeWebDAV{readErr: ErrCredentialsRejected}
	tokens := &memoryTokens{token: domainsync.Token{
		Provider: ProviderWebDAV,
		WebDAV:   &domainsync.WebDAVCredential{Endpoint: "https://dav.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "a", Password: "b"},
	}, set: true}
	config := Config{Provider: ProviderWebDAV, Connected: true, Login: "a@dav.test"}
	service, _, _ := newWebDAVTestService(t, connector, config, tokens)
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrCredentialsRejected) {
		t.Fatalf("sync = %v", err)
	}
	connector.mu.Lock()
	reads, connects := connector.readCount, connector.connectCalls
	connector.mu.Unlock()
	if reads != 1 || connects != 0 {
		t.Fatalf("reads = %d connects = %d; a rejected WebDAV credential must not retry", reads, connects)
	}
	if problem := service.Snapshot().Problem; problem != ProblemCredentials {
		t.Fatalf("problem = %q", problem)
	}
}

func TestWebDAVProfileNormalizationAndRemoteSelection(t *testing.T) {
	if NormalizeProvider("webdav") != ProviderWebDAV {
		t.Fatal("webdav provider was not normalized")
	}
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{token: domainsync.Token{
		Provider: ProviderWebDAV,
		WebDAV:   &domainsync.WebDAVCredential{Endpoint: "https://dav.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "a", Password: "b"},
	}, set: true}
	config := Config{Provider: ProviderWebDAV, Connected: true, Login: "a@dav.test"}
	service, _, _ := newWebDAVTestService(t, connector, config, tokens)
	if snapshot := service.Snapshot(); snapshot.Provider != ProviderWebDAV {
		t.Fatalf("provider = %q", snapshot.Provider)
	}
	if _, err := service.Sync(context.Background(), TriggerManual); err != nil {
		t.Fatalf("sync: %v", err)
	}
	connector.mu.Lock()
	reads := connector.readCount
	connector.mu.Unlock()
	if reads != 1 {
		t.Fatalf("the WebDAV remote was not selected; reads = %d", reads)
	}
}

func TestWebDAVAnchorKeepsTheSameAccountAndClearsAChangedOne(t *testing.T) {
	settings := secureSettings()
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{}
	config := Config{
		Provider: ProviderWebDAV, Connected: true, Login: "alice@dav.test",
		AccountID: "https://dav.test/dav#alice", RemoteETag: "anchor-etag", LocalSignature: "signature",
		RemoteID: "https://dav.test/dav/netcatty-vault.json",
	}
	service, _, configs := newWebDAVTestService(t, connector, config, tokens)
	if err := service.ConnectWebDAV(context.Background(), "master-password", settings, false); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if configs.config.RemoteETag != "anchor-etag" || configs.config.LocalSignature != "signature" {
		t.Fatalf("same-account reconnect cleared the anchor: %+v", configs.config)
	}

	changed := settings
	changed.Username = "bob"
	if err := service.ConnectWebDAV(context.Background(), "master-password", changed, false); err != nil {
		t.Fatalf("connect as bob: %v", err)
	}
	if configs.config.RemoteETag != "" || configs.config.LocalSignature != "" || configs.config.RemoteID != "" {
		t.Fatalf("changed-account reconnect kept the anchor: %+v", configs.config)
	}

	configs.config.RemoteETag = "second-anchor"
	changedEndpoint := settings
	changedEndpoint.Endpoint = "https://other.test/dav"
	if err := service.ConnectWebDAV(context.Background(), "master-password", changedEndpoint, false); err != nil {
		t.Fatalf("connect to another collection: %v", err)
	}
	if configs.config.RemoteETag != "" {
		t.Fatalf("another collection kept the old anchor: %+v", configs.config)
	}
}

func TestBrowserBearerWebDAVUsesTheTokenMode(t *testing.T) {
	connector := &fakeWebDAV{}
	tokens := &memoryTokens{}
	service, _, _ := newWebDAVTestService(t, connector, Config{}, tokens)
	settings := WebDAVSettings{Endpoint: "https://dav.test/dav", AuthType: "token", Token: "bearer-token-秘密"}
	if err := service.ConnectWebDAV(context.Background(), "master-password", settings, false); err != nil {
		t.Fatalf("bearer connect: %v", err)
	}
	tokens.mu.Lock()
	stored := tokens.token
	tokens.mu.Unlock()
	if stored.WebDAV.AuthType != domainsync.WebDAVAuthBearer || stored.WebDAV.Token != "bearer-token-秘密" {
		t.Fatalf("stored bearer = %+v", stored.WebDAV)
	}
	if service.Snapshot().WebDAVAuthType != domainsync.WebDAVAuthBearer {
		t.Fatalf("snapshot auth = %q", service.Snapshot().WebDAVAuthType)
	}
}
