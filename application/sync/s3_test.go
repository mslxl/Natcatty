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

// fakeS3 is a deterministic S3Connector and Remote. The adapter tests cover the
// real HTTP and SigV4 surface; these tests cover the service's insecure gate,
// credential storage, problem mapping, and anchor rotation.
type fakeS3 struct {
	mu sync.Mutex

	prepareErr error
	connectErr error
	readErr    error
	writeErr   error

	prepared      S3Settings
	preparedCount int
	connectCalls  int
	lastSettings  S3Settings
	grant         Grant
	file          RemoteFile
	exists        bool
	readCount     int
	writes        [][]byte
	writeCount    int
}

func normalizeFakeS3Endpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

func normalizeFakeS3Prefix(prefix string) string {
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	for strings.Contains(prefix, "//") {
		prefix = strings.ReplaceAll(prefix, "//", "/")
	}
	return prefix
}

func (f *fakeS3) Prepare(settings S3Settings) (S3Settings, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preparedCount++
	if f.prepareErr != nil {
		return settings, false, f.prepareErr
	}
	normalized := S3Settings{
		Endpoint:        normalizeFakeS3Endpoint(settings.Endpoint),
		Region:          strings.TrimSpace(settings.Region),
		Bucket:          strings.TrimSpace(settings.Bucket),
		Prefix:          normalizeFakeS3Prefix(settings.Prefix),
		AccessKeyID:     strings.TrimSpace(settings.AccessKeyID),
		SecretAccessKey: settings.SecretAccessKey,
		SessionToken:    strings.TrimSpace(settings.SessionToken),
	}
	if normalized.Endpoint == "" || normalized.Region == "" || normalized.Bucket == "" ||
		normalized.AccessKeyID == "" || normalized.SecretAccessKey == "" {
		return normalized, false, ErrInvalidEndpoint
	}
	f.prepared = normalized
	return normalized, strings.HasPrefix(normalized.Endpoint, "http://"), nil
}

func fakeS3Grant(settings S3Settings) Grant {
	host := strings.TrimPrefix(strings.TrimPrefix(settings.Endpoint, "https://"), "http://")
	login := settings.Bucket
	if settings.Prefix != "" {
		login += "/" + settings.Prefix
	}
	login += "@" + host
	return Grant{
		ID:    settings.Endpoint + "/" + settings.Bucket + "/" + settings.Prefix,
		Login: login,
	}
}

func (f *fakeS3) Connect(_ context.Context, settings S3Settings) (domainsync.Token, Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls++
	if f.connectErr != nil {
		return domainsync.Token{}, Grant{}, f.connectErr
	}
	f.lastSettings = settings
	grant := f.grant
	if grant.ID == "" {
		grant = fakeS3Grant(settings)
	}
	token := domainsync.Token{
		Provider:  ProviderS3,
		TokenType: "S3",
		S3: &domainsync.S3Credential{
			Endpoint:        settings.Endpoint,
			Region:          settings.Region,
			Bucket:          settings.Bucket,
			Prefix:          settings.Prefix,
			AccessKeyID:     settings.AccessKeyID,
			SecretAccessKey: settings.SecretAccessKey,
			SessionToken:    settings.SessionToken,
		},
	}
	return token, grant, nil
}

func (f *fakeS3) User(context.Context, domainsync.Token) (Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.grant, nil
}

func (f *fakeS3) Read(context.Context, domainsync.Token) (RemoteFile, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCount++
	if f.readErr != nil {
		return RemoteFile{}, false, f.readErr
	}
	return f.file, f.exists, nil
}

func (f *fakeS3) Write(_ context.Context, _ domainsync.Token, _ RemoteFile, content []byte) (RemoteFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCount++
	if f.writeErr != nil {
		return RemoteFile{}, f.writeErr
	}
	f.writes = append(f.writes, append([]byte(nil), content...))
	f.file = RemoteFile{
		ID:      "s3://bucket/netcatty-vault.json",
		ETag:    "etag-" + string(rune('0'+f.writeCount)),
		Content: append([]byte(nil), content...),
	}
	f.exists = true
	return f.file, nil
}

func newS3TestService(t *testing.T, connector *fakeS3, config Config, tokens *memoryTokens) (*Service, *fakeVault, *memoryConfigs) {
	t.Helper()
	if tokens == nil {
		tokens = &memoryTokens{}
	}
	vault := &fakeVault{payload: payloadWithHost("local", 1000)}
	configs := &memoryConfigs{config: config}
	service := NewService(connector, vault, configs, tokens,
		WithKDFIterations(testKDFIterations),
		WithRemote(ProviderS3, connector),
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

func s3InsecureSettings() S3Settings {
	return S3Settings{
		Endpoint:        "http://s3.test:9000/gateway/",
		Region:          "us-east-1",
		Bucket:          "natcatty-test",
		Prefix:          "/team/vault/",
		AccessKeyID:     "AKIAINSECUREACCESS",
		SecretAccessKey: "http-secret-秘密",
		SessionToken:    "http-session-token",
	}
}

func s3SecureSettings() S3Settings {
	return S3Settings{
		Endpoint:        "https://s3.test/",
		Region:          "eu-west-1",
		Bucket:          "natcatty-test",
		Prefix:          "team//vault/",
		AccessKeyID:     "AKIASECUREACCESS",
		SecretAccessKey: "https-secret-秘密",
		SessionToken:    "secure-session-token",
	}
}

func TestConnectS3RefusesInsecureWithoutConfirmation(t *testing.T) {
	connector := &fakeS3{}
	tokens := &memoryTokens{}
	service, _, configs := newS3TestService(t, connector, Config{}, tokens)

	err := service.ConnectS3(context.Background(), "master-password", s3InsecureSettings(), false)
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
	tokens.mu.Lock()
	stored, set := tokens.token, tokens.set
	tokens.mu.Unlock()
	if set {
		t.Fatalf("the signing material was stored before confirmation: %+v", stored)
	}
	if configs.config.Connected {
		t.Fatal("the profile was marked connected before confirmation")
	}
	if problem := service.Snapshot().Problem; problem != ProblemInsecureEndpoint {
		t.Fatalf("problem = %q, want %q", problem, ProblemInsecureEndpoint)
	}
	if endpoint := service.Snapshot().S3Endpoint; endpoint != "" {
		t.Fatalf("unconfirmed endpoint was cached: %q", endpoint)
	}
}

func TestConnectS3StoresSecretsOnlyInTheCredentialStore(t *testing.T) {
	connector := &fakeS3{}
	tokens := &memoryTokens{}
	service, _, configs := newS3TestService(t, connector, Config{}, tokens)
	settings := s3SecureSettings()

	if err := service.ConnectS3(context.Background(), "master-password", settings, false); err != nil {
		t.Fatalf("connect: %v", err)
	}
	connector.mu.Lock()
	last := connector.lastSettings
	connector.mu.Unlock()
	if last.Endpoint != "https://s3.test" {
		t.Fatalf("connector got endpoint %q", last.Endpoint)
	}
	if last.Prefix != "team/vault" {
		t.Fatalf("connector got prefix %q", last.Prefix)
	}
	if last.AccessKeyID != settings.AccessKeyID || last.SecretAccessKey != settings.SecretAccessKey ||
		last.SessionToken != settings.SessionToken {
		t.Fatalf("connector lost the signing material: %+v", last)
	}

	tokens.mu.Lock()
	stored, set := tokens.token, tokens.set
	tokens.mu.Unlock()
	if !set || stored.Provider != ProviderS3 || stored.S3 == nil {
		t.Fatalf("stored token = %+v, set %v", stored, set)
	}
	if stored.S3.AccessKeyID != settings.AccessKeyID ||
		stored.S3.SecretAccessKey != settings.SecretAccessKey ||
		stored.S3.SessionToken != settings.SessionToken {
		t.Fatalf("stored credential = %+v", stored.S3)
	}
	if stored.S3.Endpoint != "https://s3.test" || stored.S3.Region != "eu-west-1" ||
		stored.S3.Bucket != "natcatty-test" || stored.S3.Prefix != "team/vault" {
		t.Fatalf("stored non-secret fields = %+v", stored.S3)
	}

	encoded, err := json.Marshal(configs.config)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	for _, secret := range []string{settings.AccessKeyID, settings.SecretAccessKey, settings.SessionToken, "https-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("config leaked S3 signing material: %s", encoded)
		}
	}
	if configs.config.Provider != ProviderS3 || !configs.config.Connected {
		t.Fatalf("config = %+v", configs.config)
	}

	snapshot := service.Snapshot()
	if snapshot.Provider != ProviderS3 || !snapshot.Connected || snapshot.S3Insecure {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.S3Endpoint != "https://s3.test" || snapshot.S3Region != "eu-west-1" ||
		snapshot.S3Bucket != "natcatty-test" || snapshot.S3Prefix != "team/vault" {
		t.Fatalf("snapshot form fields = %+v", snapshot)
	}
	form := service.S3Settings()
	if form.AccessKeyID != "" || form.SecretAccessKey != "" || form.SessionToken != "" {
		t.Fatalf("S3Settings leaked signing material: %+v", form)
	}
	if form.Endpoint != "https://s3.test" || form.Region != "eu-west-1" ||
		form.Bucket != "natcatty-test" || form.Prefix != "team/vault" {
		t.Fatalf("S3Settings = %+v", form)
	}
}

func TestConnectS3ConfirmsInsecureButNeverAutoSyncs(t *testing.T) {
	connector := &fakeS3{}
	tokens := &memoryTokens{}
	service, _, configs := newS3TestService(t, connector, Config{}, tokens)

	if err := service.ConnectS3(context.Background(), "master-password", s3InsecureSettings(), true); err != nil {
		t.Fatalf("confirmed connect: %v", err)
	}
	if !service.Snapshot().S3Insecure {
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
	if !configs.config.Connected {
		t.Fatalf("config = %+v", configs.config)
	}
}

func TestManualSyncOfARestoredInsecureS3ProfileRequiresAFreshConfirmation(t *testing.T) {
	connector := &fakeS3{}
	tokens := &memoryTokens{token: domainsync.Token{
		Provider:  ProviderS3,
		TokenType: "S3",
		S3: &domainsync.S3Credential{
			Endpoint:        "http://s3.test:9000",
			Region:          "us-east-1",
			Bucket:          "natcatty-test",
			Prefix:          "team",
			AccessKeyID:     "AKIARESTORED",
			SecretAccessKey: "restored-secret",
			SessionToken:    "restored-token",
		},
	}, set: true}
	config := Config{Provider: ProviderS3, Connected: true, Login: "natcatty-test/team@s3.test"}
	service, _, _ := newS3TestService(t, connector, config, tokens)

	if !service.Snapshot().S3Insecure {
		t.Fatal("restored insecure profile was not reported")
	}
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrInsecureEndpoint) {
		t.Fatalf("manual sync without confirmation = %v", err)
	}
	connector.mu.Lock()
	reads := connector.readCount
	connector.mu.Unlock()
	if reads != 0 {
		t.Fatal("the signing material was sent before confirmation")
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

func TestConfirmInsecureEndpointRefusesSecureAndMissingS3Credentials(t *testing.T) {
	service, _, _ := newS3TestService(t, &fakeS3{}, Config{}, &memoryTokens{})
	if service.ConfirmInsecureEndpoint(context.Background()) {
		t.Fatal("missing credential was confirmable")
	}

	secureTokens := &memoryTokens{token: domainsync.Token{
		Provider: ProviderS3,
		S3: &domainsync.S3Credential{
			Endpoint: "https://s3.test", Region: "eu-west-1", Bucket: "b",
			AccessKeyID: "a", SecretAccessKey: "s",
		},
	}, set: true}
	secureService, _, _ := newS3TestService(t, &fakeS3{}, Config{}, secureTokens)
	if secureService.ConfirmInsecureEndpoint(context.Background()) {
		t.Fatal("HTTPS endpoint was reported insecure")
	}
}

func TestS3ProblemTaxonomy(t *testing.T) {
	tests := []struct {
		err     error
		problem Problem
	}{
		{ErrCredentialsRejected, ProblemCredentials},
		{ErrForbidden, ProblemForbidden},
		{ErrTLS, ProblemTLS},
		{ErrInvalidEndpoint, ProblemInvalidEndpoint},
		{ErrWrongRegion, ProblemWrongRegion},
		{ErrNetwork, ProblemNetwork},
	}
	for _, test := range tests {
		t.Run(string(test.problem), func(t *testing.T) {
			connector := &fakeS3{connectErr: test.err}
			tokens := &memoryTokens{}
			service, _, _ := newS3TestService(t, connector, Config{}, tokens)
			err := service.ConnectS3(context.Background(), "master-password", s3SecureSettings(), false)
			if !errors.Is(err, test.err) {
				t.Fatalf("error = %v, want %v", err, test.err)
			}
			if problem := service.Snapshot().Problem; problem != test.problem {
				t.Fatalf("problem = %q, want %q", problem, test.problem)
			}
			connector.mu.Lock()
			connectCalls := connector.connectCalls
			connector.mu.Unlock()
			if connectCalls != 1 {
				t.Fatalf("Connect calls = %d; a failed connect must not retry", connectCalls)
			}
			tokens.mu.Lock()
			stored, set := tokens.token, tokens.set
			tokens.mu.Unlock()
			if set {
				t.Fatalf("a failed connect stored a credential: %+v", stored)
			}
		})
	}
}

func TestS3WrongRegionIsReportedWithoutFollowingTheRedirect(t *testing.T) {
	connector := &fakeS3{connectErr: ErrWrongRegion}
	service, _, _ := newS3TestService(t, connector, Config{}, &memoryTokens{})
	err := service.ConnectS3(context.Background(), "master-password", s3SecureSettings(), false)
	if !errors.Is(err, ErrWrongRegion) {
		t.Fatalf("connect = %v, want ErrWrongRegion", err)
	}
	snapshot := service.Snapshot()
	if snapshot.Problem != ProblemWrongRegion || snapshot.Connected || snapshot.S3Endpoint != "" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestS3SyncRejectedCredentialDoesNotRefreshOrRetry(t *testing.T) {
	connector := &fakeS3{readErr: ErrCredentialsRejected}
	tokens := &memoryTokens{token: domainsync.Token{
		Provider: ProviderS3,
		S3: &domainsync.S3Credential{
			Endpoint: "https://s3.test", Region: "eu-west-1", Bucket: "b",
			AccessKeyID: "a", SecretAccessKey: "s",
		},
	}, set: true}
	config := Config{Provider: ProviderS3, Connected: true, Login: "b@s3.test"}
	service, _, _ := newS3TestService(t, connector, config, tokens)
	if _, err := service.Sync(context.Background(), TriggerManual); !errors.Is(err, ErrCredentialsRejected) {
		t.Fatalf("sync = %v", err)
	}
	connector.mu.Lock()
	reads, connects := connector.readCount, connector.connectCalls
	connector.mu.Unlock()
	if reads != 1 || connects != 0 {
		t.Fatalf("reads = %d connects = %d; a rejected S3 credential must not retry", reads, connects)
	}
	if problem := service.Snapshot().Problem; problem != ProblemCredentials {
		t.Fatalf("problem = %q", problem)
	}
}

func TestS3ProfileRestoresNonSecretFieldsOnLoad(t *testing.T) {
	tokens := &memoryTokens{token: domainsync.Token{
		Provider: ProviderS3,
		S3: &domainsync.S3Credential{
			Endpoint:        "https://s3.test",
			Region:          "ap-south-1",
			Bucket:          "vault",
			Prefix:          "team",
			AccessKeyID:     "AKIARESTORED",
			SecretAccessKey: "restored-secret",
			SessionToken:    "restored-token",
		},
	}, set: true}
	config := Config{Provider: ProviderS3, Connected: true, Login: "vault/team@s3.test"}
	service, _, _ := newS3TestService(t, &fakeS3{}, config, tokens)

	snapshot := service.Snapshot()
	if snapshot.S3Endpoint != "https://s3.test" || snapshot.S3Region != "ap-south-1" ||
		snapshot.S3Bucket != "vault" || snapshot.S3Prefix != "team" {
		t.Fatalf("snapshot form fields = %+v", snapshot)
	}
	if snapshot.S3Insecure {
		t.Fatal("HTTPS endpoint was reported insecure")
	}
	form := service.S3Settings()
	if form.AccessKeyID != "" || form.SecretAccessKey != "" || form.SessionToken != "" {
		t.Fatalf("restored form leaked signing material: %+v", form)
	}
}

func TestS3AnchorKeepsTheSameAccountAndClearsAChangedOne(t *testing.T) {
	settings := s3SecureSettings()
	connector := &fakeS3{}
	tokens := &memoryTokens{}
	grant := fakeS3Grant(S3Settings{
		Endpoint: "https://s3.test", Region: "eu-west-1", Bucket: "natcatty-test", Prefix: "team/vault",
	})
	config := Config{
		Provider: ProviderS3, Connected: true, Login: grant.Login,
		AccountID: grant.ID, RemoteETag: "anchor-etag", LocalSignature: "signature",
		RemoteID: "s3://natcatty-test/team/vault/netcatty-vault.json",
	}
	service, _, configs := newS3TestService(t, connector, config, tokens)
	if err := service.ConnectS3(context.Background(), "master-password", settings, false); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if configs.config.RemoteETag != "anchor-etag" || configs.config.LocalSignature != "signature" {
		t.Fatalf("same-account reconnect cleared the anchor: %+v", configs.config)
	}

	changed := settings
	changed.Bucket = "other-bucket"
	if err := service.ConnectS3(context.Background(), "master-password", changed, false); err != nil {
		t.Fatalf("connect to a different bucket: %v", err)
	}
	if configs.config.RemoteETag != "" || configs.config.LocalSignature != "" || configs.config.RemoteID != "" {
		t.Fatalf("changed-account reconnect kept the anchor: %+v", configs.config)
	}
}

func TestDisconnectAndSetProviderClearS3FormState(t *testing.T) {
	connector := &fakeS3{}
	tokens := &memoryTokens{}
	service, _, configs := newS3TestService(t, connector, Config{}, tokens)
	if err := service.ConnectS3(context.Background(), "master-password", s3SecureSettings(), false); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := service.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	snapshot := service.Snapshot()
	if snapshot.S3Endpoint != "" || snapshot.S3Region != "" || snapshot.S3Bucket != "" || snapshot.S3Prefix != "" {
		t.Fatalf("disconnect kept S3 form state: %+v", snapshot)
	}
	if snapshot.Connected || snapshot.Provider != ProviderS3 {
		t.Fatalf("snapshot after disconnect = %+v", snapshot)
	}
	tokens.mu.Lock()
	set := tokens.set
	tokens.mu.Unlock()
	if set {
		t.Fatal("disconnect kept the credential")
	}
	if configs.config.Provider != ProviderS3 || configs.config.Connected {
		t.Fatalf("config after disconnect = %+v", configs.config)
	}

	if err := service.ConnectS3(context.Background(), "master-password", s3SecureSettings(), false); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if err := service.SetProvider(context.Background(), ProviderGitHub); err != nil {
		t.Fatalf("switch provider: %v", err)
	}
	snapshot = service.Snapshot()
	if snapshot.S3Endpoint != "" || snapshot.S3Region != "" || snapshot.S3Bucket != "" || snapshot.S3Prefix != "" {
		t.Fatalf("switching provider kept S3 form state: %+v", snapshot)
	}
	if snapshot.Provider != ProviderGitHub || snapshot.Connected {
		t.Fatalf("snapshot after switch = %+v", snapshot)
	}
}

func TestNormalizeProviderRecognizesS3(t *testing.T) {
	if NormalizeProvider("s3") != ProviderS3 {
		t.Fatal("the S3 provider was not normalized")
	}
}
