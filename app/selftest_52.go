//go:build selftest

package app

// Ticket 52 self-test. Press F11, then 5, then 2.
//
// The check runs the real WebDAV sync stack headlessly against an in-process
// fake WebDAV server; no real network or WebDAV account is involved:
//
//   - a plain-HTTP connection is refused before a credential is stored or a
//     byte is sent, then the explicit confirmation connects and a manual sync
//     uploads the product file through the real adapter;
//   - Basic (UTF-8), Digest (SHA-256 with a stale nonce), and Bearer
//     authentication each read and write the sync file, a 404 is the empty
//     state, and an ETag race is reported as a conflict;
//   - a self-signed TLS certificate is rejected with a typed error and the
//     fake never receives a request, and a cross-host redirect is refused;
//   - the password lives only in the credential store: the profile file, the
//     snapshot, and every recorded Authorization header are free of it.
//
// A repeated F11,5,2 starts from a clean scenario. The scenario runs in one
// goroutine so the event loop is never blocked; waits poll an observable
// condition with the scenario deadline as the hard bound.
//
// Result: %TEMP%\natcatty-selftest-52.json (os.TempDir on other platforms).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	adapterstore "github.com/mslxl/natcatty/adapters/store"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	adapterwebdav "github.com/mslxl/natcatty/adapters/webdav"
	webdavfake "github.com/mslxl/natcatty/adapters/webdav/fake"
	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest52Ticket           = 52
	selftest52Timeout          = 60 * time.Second
	selftest52Password         = "ticket 52 master password"
	selftest52User             = "dav-user"
	selftest52Secret           = "dav-secret"
	selftest52BearerCredential = "selftest-bearer-value"
)

type selftest52 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest52Result
	report   selftest52Report
	err      error
}

type selftest52Report struct {
	// Insecure endpoint gate.
	InsecureRejected  bool
	InsecureRequests  int
	InsecureNoToken   bool
	InsecureProblem   string
	ConfirmedConnect  bool
	ConfirmedInsecure bool
	AutoRefused       bool
	AutoRequests      int

	// Basic.
	BasicConnect   bool
	BasicLogin     string
	BasicNotFound  bool
	BasicRoundTrip bool
	BasicBytes     int
	BasicETag      bool
	BasicConflict  bool
	BasicAuths     int
	SyncAction     string
	SyncHost       string
	SyncBytes      int

	// Digest.
	DigestConnect    bool
	DigestAlgorithm  string
	DigestChallenges int
	DigestSuccesses  int
	DigestStale      bool
	DigestRoundTrip  bool

	// Bearer.
	BearerConnect   bool
	BearerSuccesses int
	BearerRoundTrip bool

	// Transport policy.
	TLSRejected     bool
	TLSRequests     int
	RedirectRefused bool

	// Credential redaction.
	ConfigFileNoSecret bool
	SnapshotNoSecret   bool
	TokenHasSecret     bool
	TraceRedacted      bool
	SecretViolations   int
}

type selftest52Result struct {
	report selftest52Report
	err    error
}

func (t *selftest52) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest52Ticket,
		Name:   "WebDAV sync provider",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "insecure-endpoint-gate", Run: t.insecureStep},
			{Name: "basic-read-write-conflict-404", Run: t.basicStep},
			{Name: "digest-challenge-stale-nonce", Run: t.digestStep},
			{Name: "bearer-authentication", Run: t.bearerStep},
			{Name: "tls-certificate-rejected", Run: t.tlsStep},
			{Name: "cross-host-redirect-refused", Run: t.redirectStep},
			{Name: "credential-redaction", Run: t.redactionStep},
		},
	}
}

func (t *selftest52) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest52Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest52Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest52Timeout)
		t.cancel = cancel
		t.result = make(chan selftest52Result, 1)
		go func() {
			report, err := runSelftest52(ctx)
			t.result <- selftest52Result{report: report, err: err}
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
			"fake WebDAV: explicit plain-HTTP confirmation, Basic/Digest/Bearer read and write, ETag conflict and 404, self-signed TLS and cross-host redirect refused, secrets only in the credential store",
			"adapters/webdav + adapters/webdav/fake + application/sync")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the WebDAV scenario did not finish in %s", selftest52Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless WebDAV scenario")
	}
}

func (t *selftest52) insecureStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.InsecureRejected {
		return selftest.Failed(errors.New("an unconfirmed plain-HTTP endpoint was not refused"))
	}
	if report.InsecureRequests != 0 {
		return selftest.Failed(fmt.Errorf("the refused endpoint sent %d requests", report.InsecureRequests))
	}
	if !report.InsecureNoToken {
		return selftest.Failed(errors.New("the refused endpoint stored a credential"))
	}
	if report.InsecureProblem != string(appsync.ProblemInsecureEndpoint) {
		return selftest.Failed(fmt.Errorf("problem = %q, want %q",
			report.InsecureProblem, appsync.ProblemInsecureEndpoint))
	}
	if !report.ConfirmedConnect || !report.ConfirmedInsecure {
		return selftest.Failed(fmt.Errorf("confirmed connect = %v insecure = %v",
			report.ConfirmedConnect, report.ConfirmedInsecure))
	}
	if !report.AutoRefused || report.AutoRequests != 0 {
		return selftest.Failed(fmt.Errorf("automatic sync refused = %v requests = %d",
			report.AutoRefused, report.AutoRequests))
	}
	return selftest.Passed(
		"plain HTTP was refused before storing or sending the credential; the explicit confirmation connected, and automatic sync stayed refused while manual sync proceeded",
		"application/sync ConnectWebDAV gate + syncLocked insecure branch")
}

func (t *selftest52) basicStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.BasicConnect || report.BasicLogin == "" {
		return selftest.Failed(fmt.Errorf("connect = %v login = %q", report.BasicConnect, report.BasicLogin))
	}
	if !report.BasicNotFound {
		return selftest.Failed(errors.New("a missing sync file was not reported as the empty state"))
	}
	if !report.BasicRoundTrip || report.BasicBytes == 0 {
		return selftest.Failed(fmt.Errorf("round trip = %v bytes = %d",
			report.BasicRoundTrip, report.BasicBytes))
	}
	if !report.BasicETag || !report.BasicConflict {
		return selftest.Failed(fmt.Errorf("ETag = %v conflict = %v", report.BasicETag, report.BasicConflict))
	}
	if report.BasicAuths == 0 {
		return selftest.Failed(errors.New("the fake accepted no Basic credential"))
	}
	if report.SyncAction != string(domainsync.ActionUpload) || report.SyncHost != "webdav-local" ||
		report.SyncBytes == 0 {
		return selftest.Failed(fmt.Errorf("sync action = %q host = %q bytes = %d",
			report.SyncAction, report.SyncHost, report.SyncBytes))
	}
	return selftest.Passed(fmt.Sprintf(
		"Basic connected as %s; 404 was empty, %d arbitrary bytes round-tripped, the ETag was present, a bumped ETag produced ErrConflict, and the service uploaded webdav-local through the fake",
		report.BasicLogin, report.BasicBytes),
		"adapters/webdav conditional PUT If-None-Match/If-Match + application/sync upload")
}

func (t *selftest52) digestStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.DigestConnect {
		return selftest.Failed(errors.New("Digest authentication did not connect"))
	}
	if !report.DigestStale || report.DigestChallenges < 2 || report.DigestSuccesses == 0 {
		return selftest.Failed(fmt.Errorf("challenges = %d successes = %d stale = %v",
			report.DigestChallenges, report.DigestSuccesses, report.DigestStale))
	}
	if !report.DigestRoundTrip {
		return selftest.Failed(errors.New("Digest read/write did not round-trip the bytes"))
	}
	return selftest.Passed(fmt.Sprintf(
		"%s Digest answered %d challenges, retried the stale nonce, and round-tripped the sync file",
		report.DigestAlgorithm, report.DigestChallenges),
		"adapters/webdav doDigest + adapters/webdav/fake challenge/verify")
}

func (t *selftest52) bearerStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.BearerConnect || report.BearerSuccesses == 0 || !report.BearerRoundTrip {
		return selftest.Failed(fmt.Errorf("connect = %v successes = %d roundTrip = %v",
			report.BearerConnect, report.BearerSuccesses, report.BearerRoundTrip))
	}
	return selftest.Passed(
		"a bearer token authenticated the read and write of the sync file",
		"adapters/webdav bearer Authorization + adapters/webdav/fake bearer mode")
}

func (t *selftest52) tlsStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.TLSRejected {
		return selftest.Failed(errors.New("a self-signed TLS certificate was accepted"))
	}
	if report.TLSRequests != 0 {
		return selftest.Failed(fmt.Errorf("the rejected TLS endpoint served %d requests", report.TLSRequests))
	}
	return selftest.Passed(
		"a self-signed certificate failed with ErrTLS and no HTTP request left the client; certificate verification is never skipped",
		"adapters/webdav mapTransportError + crypto/tls default verification")
}

func (t *selftest52) redirectStep(*ui.Context) selftest.Outcome {
	if !t.report.RedirectRefused {
		return selftest.Failed(errors.New("a cross-host redirect was followed"))
	}
	return selftest.Passed(
		"a redirect to another host was refused with ErrRedirectRefused",
		"adapters/webdav checkRedirect")
}

func (t *selftest52) redactionStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.ConfigFileNoSecret || !report.SnapshotNoSecret {
		return selftest.Failed(fmt.Errorf("config file clean = %v snapshot clean = %v",
			report.ConfigFileNoSecret, report.SnapshotNoSecret))
	}
	if !report.TokenHasSecret {
		return selftest.Failed(errors.New("the secure token store did not hold the WebDAV password"))
	}
	if !report.TraceRedacted || report.SecretViolations != 0 {
		return selftest.Failed(fmt.Errorf("traces redacted = %v secret violations = %d",
			report.TraceRedacted, report.SecretViolations))
	}
	return selftest.Passed(
		"the profile file and snapshot contained no secret, the credential store held the password, and every Authorization header in the fake's traces was redacted with zero secret violations",
		"adapters/syncstore.ConfigStore + domain/sync.Token + adapters/webdav/fake redacted traces")
}

// runSelftest52 executes the whole headless scenario and releases every
// temporary resource before returning.
func runSelftest52(ctx context.Context) (report selftest52Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-52-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	basicServer, err := webdavfake.Start()
	if err != nil {
		return report, fmt.Errorf("start the basic fake WebDAV server: %w", err)
	}
	defer basicServer.Close()
	basicServer.Reset()

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
	client := adapterwebdav.New(adapterwebdav.Options{})
	vault := adaptersyncvault.New(database, secrets)
	service := appsync.NewService(client, vault, configs, tokens,
		appsync.WithProvider(appsync.ProviderWebDAV),
		appsync.WithRemote(appsync.ProviderWebDAV, client),
		appsync.WithDevice("selftest-webdav-device", "Self-test Device", "selftest"),
	)
	defer service.Stop()
	defer service.ClearPassword()
	if err := service.Load(ctx); err != nil {
		return report, fmt.Errorf("load the sync profile: %w", err)
	}
	if err := service.SetPassword(selftest52Password); err != nil {
		return report, fmt.Errorf("set the master password: %w", err)
	}
	if err := database.Save(ctx, selftest52Host("webdav-local", time.Now())); err != nil {
		return report, fmt.Errorf("save the initial Host: %w", err)
	}

	settings := appsync.WebDAVSettings{
		Endpoint: basicServer.CollectionURL(),
		AuthType: domainsync.WebDAVAuthBasic,
		Username: selftest52User,
		Password: selftest52Secret,
	}

	// 1. The plain-HTTP gate must run before the credential is stored or a
	// request is sent.
	requestsBefore := basicServer.State().Counters.Requests
	gateErr := service.ConnectWebDAV(ctx, selftest52Password, settings, false)
	report.InsecureRejected = errors.Is(gateErr, appsync.ErrInsecureEndpoint)
	report.InsecureRequests = basicServer.State().Counters.Requests - requestsBefore
	_, tokenErr := tokens.Load(ctx)
	report.InsecureNoToken = errors.Is(tokenErr, appsync.ErrNoToken)
	report.InsecureProblem = string(service.Snapshot().Problem)
	if !report.InsecureRejected || !report.InsecureNoToken {
		return report, fmt.Errorf("the unconfirmed plain-HTTP gate = %v, no token = %v",
			report.InsecureRejected, report.InsecureNoToken)
	}

	// 2. The explicit confirmation connects and stores the credential.
	if err := service.ConnectWebDAV(ctx, selftest52Password, settings, true); err != nil {
		return report, fmt.Errorf("confirmed WebDAV connect: %w", err)
	}
	snapshot := service.Snapshot()
	report.ConfirmedConnect = snapshot.Connected && snapshot.Provider == appsync.ProviderWebDAV
	report.ConfirmedInsecure = snapshot.WebDAVInsecure
	report.BasicConnect = report.ConfirmedConnect
	report.BasicLogin = snapshot.Login
	token, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the stored WebDAV credential: %w", err)
	}
	report.TokenHasSecret = token.WebDAV != nil && token.WebDAV.Password == selftest52Secret

	// 3. Automatic sync must refuse plain HTTP even after the connect
	// confirmation; a manual sync is confirmed for this process.
	requestsBefore = basicServer.State().Counters.Requests
	_, autoErr := service.Sync(ctx, appsync.TriggerAuto)
	report.AutoRefused = errors.Is(autoErr, appsync.ErrInsecureEndpoint)
	report.AutoRequests = basicServer.State().Counters.Requests - requestsBefore

	manual, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual WebDAV sync: %w", err)
	}
	report.SyncAction = string(manual.Action)
	cloud, exists := basicServer.FileContent(basicServer.CollectionPath(), domainsync.SyncFileName)
	if !exists {
		return report, errors.New("the manual sync did not create the product file")
	}
	report.SyncBytes = len(cloud)
	payload, err := selftest52Payload(cloud)
	if err != nil {
		return report, fmt.Errorf("decode the uploaded cloud copy: %w", err)
	}
	report.SyncHost = selftest51FirstLabel(payload.Hosts)

	// 4. The bytes contract, 404, ETag, and conflict on a second collection so
	// arbitrary bytes cannot disturb the service's envelope.
	if err := basicServer.CreateCollection("/dav/bytes"); err != nil {
		return report, fmt.Errorf("create the bytes collection: %w", err)
	}
	bytesSettings := settings
	bytesSettings.Endpoint = basicServer.URL() + "/dav/bytes"
	bytesToken, _, err := client.Connect(ctx, bytesSettings)
	if err != nil {
		return report, fmt.Errorf("connect to the bytes collection: %w", err)
	}
	_, exists, err = client.Read(ctx, bytesToken)
	if err != nil {
		return report, fmt.Errorf("read the missing bytes file: %w", err)
	}
	report.BasicNotFound = !exists

	arbitraryBytes := []byte{0x00, 0x01, 0xfe, 0xff, '\n', 'w', 'e', 'b', 'd', 'a', 'v', 0x00}
	written, err := client.Write(ctx, bytesToken, appsync.RemoteFile{}, arbitraryBytes)
	if err != nil {
		return report, fmt.Errorf("create the bytes file: %w", err)
	}
	report.BasicETag = written.ETag != ""
	readBack, exists, err := client.Read(ctx, bytesToken)
	if err != nil || !exists {
		return report, fmt.Errorf("read the bytes file = %v, %v", exists, err)
	}
	report.BasicBytes = len(readBack.Content)
	report.BasicRoundTrip = bytes.Equal(readBack.Content, arbitraryBytes)

	if err := basicServer.BumpETag("/dav/bytes", domainsync.SyncFileName); err != nil {
		return report, fmt.Errorf("bump the bytes ETag: %w", err)
	}
	_, conflictErr := client.Write(ctx, bytesToken, written, []byte("conflicting writer"))
	report.BasicConflict = errors.Is(conflictErr, appsync.ErrConflict)
	latest, exists, err := client.Read(ctx, bytesToken)
	if err != nil || !exists {
		return report, fmt.Errorf("reread the bytes file after the conflict = %v, %v", exists, err)
	}
	if _, err := client.Write(ctx, bytesToken, latest, arbitraryBytes); err != nil {
		return report, fmt.Errorf("recover the bytes file after the conflict: %w", err)
	}
	report.BasicAuths = basicServer.State().Counters.BasicSuccesses

	// 5. The profile file and snapshot never carry the secret.
	configData, err := os.ReadFile(filepath.Join(directory, "cloud-sync.json"))
	if err != nil {
		return report, fmt.Errorf("read the profile file: %w", err)
	}
	report.ConfigFileNoSecret = !strings.Contains(string(configData), selftest52Secret) &&
		!strings.Contains(string(configData), selftest52Password)
	snapshotData, err := json.Marshal(service.Snapshot())
	if err != nil {
		return report, fmt.Errorf("serialize the snapshot: %w", err)
	}
	report.SnapshotNoSecret = !strings.Contains(string(snapshotData), selftest52Secret) &&
		!strings.Contains(string(snapshotData), selftest52BearerCredential)

	// 6. Digest: SHA-256, a real challenge, and one stale nonce.
	digestServer, err := webdavfake.Start()
	if err != nil {
		return report, fmt.Errorf("start the digest fake WebDAV server: %w", err)
	}
	defer digestServer.Close()
	digestServer.Reset()
	digestServer.Configure(func(control *webdavfake.Control) {
		control.AuthMode = "digest"
		control.Username = "digest-user"
		control.Password = "digest-secret"
		control.DigestAlgorithm = "SHA-256"
		control.DigestStale = true
	})
	digestClient := adapterwebdav.New(adapterwebdav.Options{})
	digestToken, _, err := digestClient.Connect(ctx, appsync.WebDAVSettings{
		Endpoint: digestServer.CollectionURL(),
		AuthType: domainsync.WebDAVAuthDigest,
		Username: "digest-user",
		Password: "digest-secret",
	})
	if err != nil {
		return report, fmt.Errorf("Digest connect: %w", err)
	}
	report.DigestConnect = true
	digestBytes := []byte("digest \x00 payload \xff")
	if _, err := digestClient.Write(ctx, digestToken, appsync.RemoteFile{}, digestBytes); err != nil {
		return report, fmt.Errorf("Digest write: %w", err)
	}
	digestBack, digestExists, err := digestClient.Read(ctx, digestToken)
	if err != nil || !digestExists {
		return report, fmt.Errorf("Digest read = %v, %v", digestExists, err)
	}
	report.DigestRoundTrip = bytes.Equal(digestBack.Content, digestBytes)
	digestState := digestServer.State()
	report.DigestAlgorithm = digestState.Control.DigestAlgorithm
	report.DigestChallenges = digestState.Counters.DigestChallenges
	report.DigestSuccesses = digestState.Counters.DigestSuccesses
	report.DigestStale = !digestState.Control.StalePending && report.DigestChallenges >= 2

	// 7. Bearer.
	bearerServer, err := webdavfake.Start()
	if err != nil {
		return report, fmt.Errorf("start the bearer fake WebDAV server: %w", err)
	}
	defer bearerServer.Close()
	bearerServer.Reset()
	bearerServer.Configure(func(control *webdavfake.Control) {
		control.AuthMode = "bearer"
		control.Token = selftest52BearerCredential
	})
	bearerClient := adapterwebdav.New(adapterwebdav.Options{})
	bearerToken, _, err := bearerClient.Connect(ctx, appsync.WebDAVSettings{
		Endpoint: bearerServer.CollectionURL(),
		AuthType: domainsync.WebDAVAuthBearer,
		Token:    selftest52BearerCredential,
	})
	if err != nil {
		return report, fmt.Errorf("bearer connect: %w", err)
	}
	report.BearerConnect = true
	bearerBytes := []byte("bearer \x00 payload \xfe")
	if _, err := bearerClient.Write(ctx, bearerToken, appsync.RemoteFile{}, bearerBytes); err != nil {
		return report, fmt.Errorf("bearer write: %w", err)
	}
	bearerBack, bearerExists, err := bearerClient.Read(ctx, bearerToken)
	if err != nil || !bearerExists {
		return report, fmt.Errorf("bearer read = %v, %v", bearerExists, err)
	}
	report.BearerRoundTrip = bytes.Equal(bearerBack.Content, bearerBytes)
	report.BearerSuccesses = bearerServer.State().Counters.BearerSuccesses

	// 8. A self-signed certificate is rejected before any request.
	tlsServer, err := webdavfake.StartTLS()
	if err != nil {
		return report, fmt.Errorf("start the TLS fake WebDAV server: %w", err)
	}
	defer tlsServer.Close()
	_, _, tlsErr := client.Connect(ctx, appsync.WebDAVSettings{
		Endpoint: tlsServer.CollectionURL(),
		AuthType: domainsync.WebDAVAuthBasic,
		Username: selftest52User,
		Password: selftest52Secret,
	})
	report.TLSRejected = errors.Is(tlsErr, appsync.ErrTLS)
	report.TLSRequests = tlsServer.State().Counters.Requests

	// 9. A cross-host redirect is refused rather than followed.
	redirectServer, err := webdavfake.Start()
	if err != nil {
		return report, fmt.Errorf("start the redirect fake WebDAV server: %w", err)
	}
	defer redirectServer.Close()
	redirectServer.Reset()
	redirectServer.Configure(func(control *webdavfake.Control) { control.Redirect = "other-host" })
	_, _, redirectErr := client.Connect(ctx, appsync.WebDAVSettings{
		Endpoint: redirectServer.CollectionURL(),
		AuthType: domainsync.WebDAVAuthBasic,
		Username: selftest52User,
		Password: selftest52Secret,
	})
	report.RedirectRefused = errors.Is(redirectErr, appsync.ErrRedirectRefused)

	// 10. Every trace keeps only the auth scheme.
	report.TraceRedacted = true
	for _, trace := range basicServer.State().Traces {
		if trace.Authorization == "" {
			continue
		}
		if !strings.HasSuffix(trace.Authorization, " <redacted>") ||
			strings.Contains(trace.Authorization, selftest52Secret) {
			report.TraceRedacted = false
			break
		}
	}
	report.SecretViolations = basicServer.State().Counters.SecretViolations +
		digestServer.State().Counters.SecretViolations +
		bearerServer.State().Counters.SecretViolations +
		redirectServer.State().Counters.SecretViolations +
		tlsServer.State().Counters.SecretViolations

	return report, nil
}

func selftest52Host(id string, when time.Time) domainvault.Host {
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

func selftest52Payload(content []byte) (appsync.Payload, error) {
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.Payload{}, err
	}
	plaintext, err := domainsync.DecryptPayload(envelope, selftest52Password)
	if err != nil {
		return appsync.Payload{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return appsync.Payload{}, err
	}
	return payload, nil
}
