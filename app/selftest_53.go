//go:build selftest

package app

// Ticket 53 self-test. Press F11, then 5, then 3.
//
// The check runs the real S3 sync stack headlessly against in-process fake S3
// servers; no real AWS endpoint, SDK, or network is involved:
//
//   - a plain-HTTP endpoint is refused before a credential is stored or a byte
//     is sent, then the explicit confirmation connects and a manual sync
//     uploads the product file through the real adapter;
//   - the fake recomputes SigV4 from every received request and rejects a
//     wrong secret, so a valid round trip proves the signer and verifier agree;
//   - path-style addressing, an endpoint base path, region, bucket, prefix, and
//     an enforced session token are each observed in the fake's state;
//   - read, write, ETag conflict, 403, and 404 NoSuchBucket are exercised
//     directly, including arbitrary non-UTF-8 bytes;
//   - the access key, secret access key, session token, and master password
//     stay out of the profile file and snapshot, live in the credential store,
//     and never appear in a recorded Authorization header.
//
// A repeated F11,5,3 starts from a clean scenario. The scenario runs in one
// goroutine so the event loop is never blocked; waits poll an observable
// condition with the scenario deadline as the hard bound.
//
// Result: %TEMP%\natcatty-selftest-<pid>\natcatty-selftest-53.json (os.TempDir on other platforms).

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

	adapters3 "github.com/mslxl/natcatty/adapters/s3"
	s3fake "github.com/mslxl/natcatty/adapters/s3/fake"
	adapterstore "github.com/mslxl/natcatty/adapters/store"
	adaptersyncstore "github.com/mslxl/natcatty/adapters/syncstore"
	adaptersyncvault "github.com/mslxl/natcatty/adapters/syncvault"
	appsync "github.com/mslxl/natcatty/application/sync"
	"github.com/mslxl/natcatty/domain/model"
	domainsync "github.com/mslxl/natcatty/domain/sync"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/selftest"
)

const (
	selftest53Ticket   = 53
	selftest53Timeout  = 90 * time.Second
	selftest53Password = "ticket 53 master password"
	selftest53Bucket   = "natcatty-selftest-bucket"
	selftest53Region   = "us-east-1"
	selftest53Prefix   = "team/selftest"
	selftest53BasePath = "/gateway"
	selftest53Session  = "selftest-s3-session-token"
	selftest53WrongKey = "wrong-secret-value"
)

type selftest53 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest53Result
	report   selftest53Report
	err      error
}

type selftest53Report struct {
	// Insecure endpoint gate.
	InsecureRejected  bool
	InsecureRequests  int
	InsecureNoToken   bool
	InsecureProblem   string
	ConfirmedConnect  bool
	ConfirmedInsecure bool
	AutoRefused       bool
	AutoRequests      int

	// End-to-end service sync.
	SyncAction string
	SyncHost   string
	SyncBytes  int

	// SigV4 server-side verification.
	SignatureVerified   int
	SignatureFailures   int
	WrongSecretRejected bool
	WrongSecretRequests int

	// Endpoint, path style, bucket, and prefix.
	EndpointHonored   bool
	PathStyleRequests int
	BasePathHonored   bool
	TracePath         string
	TraceBucket       string
	TraceKey          string
	PrefixObjectFound bool
	BucketMismatches  int

	// Region and bucket.
	WrongRegionRejected  bool
	WrongRegionRedirects int
	WrongRegionRequests  int
	CorrectRegionConnect bool
	WrongBucketRejected  bool

	// Session token.
	MissingTokenRejected bool
	WrongTokenRejected   bool
	TokenConnect         bool
	TokenFailures        int
	TraceTokenSigned     bool

	// Direct read/write behavior.
	ReadEmpty          bool
	RoundTrip          bool
	ArbitraryBytes     int
	ETagPresent        bool
	ConflictRejected   bool
	ConflictRecovered  bool
	ForbiddenMapped    bool
	MissingBucketError bool

	// Credential redaction.
	ConfigFileNoSecret bool
	SnapshotNoSecret   bool
	TokenHasSecret     bool
	TraceRedacted      bool
	FakeStateNoSecret  bool
	SecretViolations   int
}

type selftest53Result struct {
	report selftest53Report
	err    error
}

func (t *selftest53) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest53Ticket,
		Name:   "S3 sync provider",
		Steps: []selftest.Step{
			{Name: "run-headless-scenario", Run: t.scenarioStep},
			{Name: "insecure-endpoint-gate", Run: t.insecureStep},
			{Name: "sigv4-server-verification", Run: t.sigv4Step},
			{Name: "path-style-endpoint-bucket-prefix", Run: t.pathStyleStep},
			{Name: "region-and-bucket-honored", Run: t.regionStep},
			{Name: "session-token-enforced", Run: t.tokenStep},
			{Name: "read-write-conflict-403-404", Run: t.behaviorStep},
			{Name: "credential-redaction", Run: t.redactionStep},
		},
	}
}

func (t *selftest53) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest53Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest53Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest53Timeout)
		t.cancel = cancel
		t.result = make(chan selftest53Result, 1)
		go func() {
			report, err := runSelftest53(ctx)
			t.result <- selftest53Result{report: report, err: err}
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
			"fake S3: explicit plain-HTTP confirmation, server-verified SigV4 with path-style base-path addressing, enforced region/bucket/prefix/session token, read/write/conflict/403/404, and signing material confined to the credential store",
			"adapters/s3 + adapters/s3/fake + application/sync")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the S3 scenario did not finish in %s", selftest53Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the headless S3 scenario")
	}
}

func (t *selftest53) insecureStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.InsecureRejected || report.InsecureRequests != 0 || !report.InsecureNoToken {
		return selftest.Failed(fmt.Errorf("rejected = %v requests = %d no token = %v",
			report.InsecureRejected, report.InsecureRequests, report.InsecureNoToken))
	}
	if report.InsecureProblem != string(appsync.ProblemInsecureEndpoint) {
		return selftest.Failed(fmt.Errorf("problem = %q", report.InsecureProblem))
	}
	if !report.ConfirmedConnect || !report.ConfirmedInsecure {
		return selftest.Failed(fmt.Errorf("confirmed = %v insecure = %v",
			report.ConfirmedConnect, report.ConfirmedInsecure))
	}
	if !report.AutoRefused || report.AutoRequests != 0 {
		return selftest.Failed(fmt.Errorf("auto refused = %v requests = %d",
			report.AutoRefused, report.AutoRequests))
	}
	return selftest.Passed(
		"plain HTTP was refused before storing or sending the signing material; the explicit confirmation connected, automatic sync stayed refused, and manual sync uploaded through the real adapter",
		"application/sync ConnectS3 gate + syncLocked insecure branch")
}

func (t *selftest53) sigv4Step(*ui.Context) selftest.Outcome {
	report := t.report
	if report.SignatureVerified == 0 {
		return selftest.Failed(errors.New("the fake verified no SigV4 request"))
	}
	if !report.WrongSecretRejected || report.WrongSecretRequests != 1 || report.SignatureFailures == 0 {
		return selftest.Failed(fmt.Errorf("wrong secret rejected = %v requests = %d failures = %d",
			report.WrongSecretRejected, report.WrongSecretRequests, report.SignatureFailures))
	}
	return selftest.Passed(fmt.Sprintf(
		"the fake recomputed %d valid SigV4 signatures; a wrong secret failed with ErrCredentialsRejected after exactly one request and was counted as %d signature failure(s)",
		report.SignatureVerified, report.SignatureFailures),
		"adapters/s3 Sign + adapters/s3/fake independent canonical request/signature recomputation")
}

func (t *selftest53) pathStyleStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.EndpointHonored || !report.BasePathHonored {
		return selftest.Failed(fmt.Errorf("endpoint = %v base path = %v path = %q",
			report.EndpointHonored, report.BasePathHonored, report.TracePath))
	}
	if report.PathStyleRequests == 0 || report.BucketMismatches != 0 {
		return selftest.Failed(fmt.Errorf("path-style requests = %d bucket mismatches = %d",
			report.PathStyleRequests, report.BucketMismatches))
	}
	if report.TraceBucket != selftest53Bucket || report.TraceKey != selftest53Prefix+"/"+domainsync.SyncFileName {
		return selftest.Failed(fmt.Errorf("trace bucket/key = %q/%q", report.TraceBucket, report.TraceKey))
	}
	if !report.PrefixObjectFound {
		return selftest.Failed(errors.New("the object was not stored under the normalized prefix"))
	}
	return selftest.Passed(fmt.Sprintf(
		"path-style endpoint base path %s, bucket %s, and object key %s were used on the wire and agreed with the SigV4 canonical URI",
		selftest53BasePath, selftest53Bucket, report.TraceKey),
		"adapters/s3 objectAddress + adapters/s3/fake path-style routing")
}

func (t *selftest53) regionStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.WrongRegionRejected || report.WrongRegionRedirects != 1 || report.WrongRegionRequests != 1 {
		return selftest.Failed(fmt.Errorf("wrong region rejected = %v redirects = %d requests = %d",
			report.WrongRegionRejected, report.WrongRegionRedirects, report.WrongRegionRequests))
	}
	if !report.CorrectRegionConnect {
		return selftest.Failed(errors.New("the corrected region did not connect"))
	}
	if !report.WrongBucketRejected {
		return selftest.Failed(errors.New("a missing bucket was not reported as an invalid endpoint"))
	}
	return selftest.Passed(
		"a mismatched region produced one PermanentRedirect and was reported as ErrWrongRegion without being followed; the corrected region connected, and a missing bucket was ErrInvalidEndpoint",
		"adapters/s3 mapStatus + adapters/s3/fake x-amz-bucket-region")
}

func (t *selftest53) tokenStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.MissingTokenRejected || !report.WrongTokenRejected {
		return selftest.Failed(fmt.Errorf("missing = %v wrong = %v",
			report.MissingTokenRejected, report.WrongTokenRejected))
	}
	if !report.TokenConnect || report.TokenFailures < 2 || !report.TraceTokenSigned {
		return selftest.Failed(fmt.Errorf("connect = %v failures = %d signed = %v",
			report.TokenConnect, report.TokenFailures, report.TraceTokenSigned))
	}
	return selftest.Passed(fmt.Sprintf(
		"the fake rejected a missing and a wrong session token (%d failures), accepted the configured token, and the traces show x-amz-security-token in the signed headers",
		report.TokenFailures),
		"adapters/s3 Sign x-amz-security-token + adapters/s3/fake token enforcement")
}

func (t *selftest53) behaviorStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.ReadEmpty {
		return selftest.Failed(errors.New("a missing object was not the empty state"))
	}
	if !report.RoundTrip || report.ArbitraryBytes == 0 || !report.ETagPresent {
		return selftest.Failed(fmt.Errorf("round trip = %v bytes = %d etag = %v",
			report.RoundTrip, report.ArbitraryBytes, report.ETagPresent))
	}
	if !report.ConflictRejected || !report.ConflictRecovered {
		return selftest.Failed(fmt.Errorf("conflict = %v recovered = %v",
			report.ConflictRejected, report.ConflictRecovered))
	}
	if !report.ForbiddenMapped || !report.MissingBucketError {
		return selftest.Failed(fmt.Errorf("403 = %v missing bucket = %v",
			report.ForbiddenMapped, report.MissingBucketError))
	}
	return selftest.Passed(fmt.Sprintf(
		"404 was empty, %d arbitrary bytes round-tripped with an ETag, a bumped ETag produced ErrConflict and recovered after re-read, 403 AccessDenied mapped to an actionable forbidden state, and a missing bucket did not look empty",
		report.ArbitraryBytes),
		"adapters/s3 Read/Write conditional If-Match/If-None-Match + mapStatus")
}

func (t *selftest53) redactionStep(*ui.Context) selftest.Outcome {
	report := t.report
	if !report.ConfigFileNoSecret || !report.SnapshotNoSecret || !report.FakeStateNoSecret {
		return selftest.Failed(fmt.Errorf("config clean = %v snapshot clean = %v fake state clean = %v",
			report.ConfigFileNoSecret, report.SnapshotNoSecret, report.FakeStateNoSecret))
	}
	if !report.TokenHasSecret {
		return selftest.Failed(errors.New("the credential store did not hold the S3 signing material"))
	}
	if !report.TraceRedacted || report.SecretViolations != 0 {
		return selftest.Failed(fmt.Errorf("traces redacted = %v secret violations = %d",
			report.TraceRedacted, report.SecretViolations))
	}
	return selftest.Passed(
		"the profile file, snapshot, and fake state contained no access key, secret, session token, or master password; the credential store held all three signing fields; every Authorization header was redacted with zero secret violations",
		"adapters/syncstore.ConfigStore + domain/sync.Token.S3 + adapters/s3/fake redacted traces")
}

// runSelftest53 executes the whole headless scenario and releases every
// temporary resource before returning.
func runSelftest53(ctx context.Context) (report selftest53Report, err error) {
	directory, err := os.MkdirTemp("", "natcatty-selftest-53-")
	if err != nil {
		return report, fmt.Errorf("create a temporary Vault directory: %w", err)
	}
	defer os.RemoveAll(directory)

	server, err := s3fake.Start()
	if err != nil {
		return report, fmt.Errorf("start the fake S3 server: %w", err)
	}
	defer server.Close()
	server.Reset()
	server.Configure(func(control *s3fake.Control) {
		control.Bucket = selftest53Bucket
		control.Region = selftest53Region
		control.BasePath = selftest53BasePath
		control.SessionToken = selftest53Session
	})

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
	client := adapters3.New(adapters3.Options{})
	vault := adaptersyncvault.New(database, secrets)
	service := appsync.NewService(client, vault, configs, tokens,
		appsync.WithProvider(appsync.ProviderS3),
		appsync.WithRemote(appsync.ProviderS3, client),
		appsync.WithDevice("selftest-s3-device", "Self-test Device", "selftest"),
	)
	defer service.Stop()
	defer service.ClearPassword()
	if err := service.Load(ctx); err != nil {
		return report, fmt.Errorf("load the sync profile: %w", err)
	}
	if err := service.SetPassword(selftest53Password); err != nil {
		return report, fmt.Errorf("set the master password: %w", err)
	}
	if err := database.Save(ctx, selftest53Host("s3-local", time.Now())); err != nil {
		return report, fmt.Errorf("save the initial Host: %w", err)
	}

	settings := appsync.S3Settings{
		Endpoint:        server.URL() + selftest53BasePath,
		Region:          selftest53Region,
		Bucket:          selftest53Bucket,
		Prefix:          selftest53Prefix,
		AccessKeyID:     s3fake.DefaultAccessKeyID,
		SecretAccessKey: s3fake.DefaultSecretAccessKey,
		SessionToken:    selftest53Session,
	}

	// 1. The plain-HTTP gate must run before the credential is stored or a
	// request is sent.
	requestsBefore := server.State().Counters.Requests
	gateErr := service.ConnectS3(ctx, selftest53Password, settings, false)
	report.InsecureRejected = errors.Is(gateErr, appsync.ErrInsecureEndpoint)
	report.InsecureRequests = server.State().Counters.Requests - requestsBefore
	_, tokenErr := tokens.Load(ctx)
	report.InsecureNoToken = errors.Is(tokenErr, appsync.ErrNoToken)
	report.InsecureProblem = string(service.Snapshot().Problem)
	if !report.InsecureRejected || !report.InsecureNoToken {
		return report, fmt.Errorf("the unconfirmed plain-HTTP gate = %v, no token = %v",
			report.InsecureRejected, report.InsecureNoToken)
	}

	// 2. The explicit confirmation connects and stores the signing material.
	if err := service.ConnectS3(ctx, selftest53Password, settings, true); err != nil {
		return report, fmt.Errorf("confirmed S3 connect: %w", err)
	}
	snapshot := service.Snapshot()
	report.ConfirmedConnect = snapshot.Connected && snapshot.Provider == appsync.ProviderS3
	report.ConfirmedInsecure = snapshot.S3Insecure
	token, err := tokens.Load(ctx)
	if err != nil {
		return report, fmt.Errorf("read the stored S3 credential: %w", err)
	}
	report.TokenHasSecret = token.S3 != nil &&
		token.S3.AccessKeyID == s3fake.DefaultAccessKeyID &&
		token.S3.SecretAccessKey == s3fake.DefaultSecretAccessKey &&
		token.S3.SessionToken == selftest53Session &&
		token.S3.Region == selftest53Region &&
		token.S3.Bucket == selftest53Bucket &&
		token.S3.Prefix == selftest53Prefix

	// 3. Automatic sync refuses plain HTTP; manual sync uploads the product
	// file through the real adapter.
	requestsBefore = server.State().Counters.Requests
	_, autoErr := service.Sync(ctx, appsync.TriggerAuto)
	report.AutoRefused = errors.Is(autoErr, appsync.ErrInsecureEndpoint)
	report.AutoRequests = server.State().Counters.Requests - requestsBefore

	manual, err := service.Sync(ctx, appsync.TriggerManual)
	if err != nil {
		return report, fmt.Errorf("manual S3 sync: %w", err)
	}
	report.SyncAction = string(manual.Action)
	key := selftest53Prefix + "/" + domainsync.SyncFileName
	cloud, exists := server.ObjectContent(key)
	if !exists {
		return report, fmt.Errorf("the manual sync did not create %q", key)
	}
	report.SyncBytes = len(cloud)
	report.PrefixObjectFound = true
	payload, err := selftest53Payload(cloud)
	if err != nil {
		return report, fmt.Errorf("decode the uploaded cloud copy: %w", err)
	}
	report.SyncHost = selftest51FirstLabel(payload.Hosts)
	report.EndpointHonored = strings.Contains(snapshot.Login, strings.TrimPrefix(server.URL(), "http://"))

	// 4. Observe path-style addressing, the base path, bucket, and key.
	state := server.State()
	report.SignatureVerified = state.Counters.Verified
	report.SignatureFailures = state.Counters.SignatureFailures
	report.PathStyleRequests = state.Counters.PathStyleRequests
	report.BucketMismatches = state.Counters.BucketMismatches
	for _, trace := range state.Traces {
		if trace.Method == "PUT" && trace.Status == 200 && strings.HasSuffix(trace.Path, key) {
			report.TracePath = trace.Path
			report.TraceBucket = trace.Bucket
			report.TraceKey = trace.Key
			report.BasePathHonored = strings.HasPrefix(trace.Path, selftest53BasePath+"/")
			break
		}
	}

	// 5. A wrong secret is rejected by the fake's independent verifier.
	requestsBefore = state.Counters.Requests
	wrong := settings
	wrong.SecretAccessKey = selftest53WrongKey
	_, _, wrongErr := client.Connect(ctx, wrong)
	report.WrongSecretRejected = errors.Is(wrongErr, appsync.ErrCredentialsRejected)
	report.WrongSecretRequests = server.State().Counters.Requests - requestsBefore
	report.SignatureFailures = server.State().Counters.SignatureFailures

	// 6. Region and bucket: a wrong region redirects once and is not followed;
	// the right region connects; a missing bucket is an invalid endpoint.
	regionServer, err := s3fake.Start()
	if err != nil {
		return report, fmt.Errorf("start the region fake S3 server: %w", err)
	}
	defer regionServer.Close()
	regionServer.Reset()
	regionServer.Configure(func(control *s3fake.Control) {
		control.Bucket = "region-bucket"
		control.Region = "eu-west-1"
		control.SessionToken = selftest53Session
	})
	regionSettings := appsync.S3Settings{
		Endpoint:        regionServer.URL(),
		Region:          "us-east-1",
		Bucket:          "region-bucket",
		AccessKeyID:     s3fake.DefaultAccessKeyID,
		SecretAccessKey: s3fake.DefaultSecretAccessKey,
		SessionToken:    selftest53Session,
	}
	_, _, regionErr := client.Connect(ctx, regionSettings)
	report.WrongRegionRejected = errors.Is(regionErr, appsync.ErrWrongRegion)
	regionState := regionServer.State()
	report.WrongRegionRedirects = regionState.Counters.Redirects
	report.WrongRegionRequests = regionState.Counters.Requests
	regionSettings.Region = "eu-west-1"
	_, _, err = client.Connect(ctx, regionSettings)
	report.CorrectRegionConnect = err == nil
	missingBucket := regionSettings
	missingBucket.Bucket = "missing-bucket"
	_, _, err = client.Connect(ctx, missingBucket)
	report.WrongBucketRejected = errors.Is(err, appsync.ErrInvalidEndpoint)

	// 7. Session token enforcement.
	tokenServer, err := s3fake.Start()
	if err != nil {
		return report, fmt.Errorf("start the token fake S3 server: %w", err)
	}
	defer tokenServer.Close()
	tokenServer.Reset()
	const requiredToken = "required-selftest-token"
	tokenServer.Configure(func(control *s3fake.Control) {
		control.Bucket = "token-bucket"
		control.Region = selftest53Region
		control.SessionToken = requiredToken
	})
	tokenSettings := appsync.S3Settings{
		Endpoint:        tokenServer.URL(),
		Region:          selftest53Region,
		Bucket:          "token-bucket",
		AccessKeyID:     s3fake.DefaultAccessKeyID,
		SecretAccessKey: s3fake.DefaultSecretAccessKey,
	}
	_, _, err = client.Connect(ctx, tokenSettings)
	report.MissingTokenRejected = errors.Is(err, appsync.ErrCredentialsRejected)
	wrongToken := tokenSettings
	wrongToken.SessionToken = "wrong-token"
	_, _, err = client.Connect(ctx, wrongToken)
	report.WrongTokenRejected = errors.Is(err, appsync.ErrCredentialsRejected)
	rightToken := tokenSettings
	rightToken.SessionToken = requiredToken
	_, _, err = client.Connect(ctx, rightToken)
	report.TokenConnect = err == nil
	tokenState := tokenServer.State()
	report.TokenFailures = tokenState.Counters.SessionTokenFailures
	for _, trace := range tokenState.Traces {
		if trace.SessionTokenPresent && strings.Contains(trace.SignedHeaders, "x-amz-security-token") {
			report.TraceTokenSigned = true
		}
	}

	// 8. Direct read/write, 404, ETag conflict, 403, and missing bucket.
	bytesSettings := settings
	bytesSettings.Prefix = "team/bytes"
	server.Configure(func(control *s3fake.Control) { control.SessionToken = selftest53Session })
	bytesToken, _, err := client.Connect(ctx, bytesSettings)
	if err != nil {
		return report, fmt.Errorf("connect to the bytes prefix: %w", err)
	}
	_, exists, err = client.Read(ctx, bytesToken)
	if err != nil {
		return report, fmt.Errorf("read the missing object: %w", err)
	}
	report.ReadEmpty = !exists

	arbitrary := []byte{0x00, 0x01, 0xfe, 0xff, '\n', 's', '3', 0x00}
	written, err := client.Write(ctx, bytesToken, appsync.RemoteFile{}, arbitrary)
	if err != nil {
		return report, fmt.Errorf("write the bytes object: %w", err)
	}
	report.ETagPresent = written.ETag != ""
	readBack, exists, err := client.Read(ctx, bytesToken)
	if err != nil || !exists {
		return report, fmt.Errorf("read the bytes object = %v, %v", exists, err)
	}
	report.ArbitraryBytes = len(readBack.Content)
	report.RoundTrip = bytes.Equal(readBack.Content, arbitrary)

	bytesKey := "team/bytes/" + domainsync.SyncFileName
	if err := server.BumpETag(bytesKey); err != nil {
		return report, fmt.Errorf("bump the bytes ETag: %w", err)
	}
	_, conflictErr := client.Write(ctx, bytesToken, written, []byte("conflicting writer"))
	report.ConflictRejected = errors.Is(conflictErr, appsync.ErrConflict)
	latest, exists, err := client.Read(ctx, bytesToken)
	if err != nil || !exists {
		return report, fmt.Errorf("re-read after the conflict = %v, %v", exists, err)
	}
	_, err = client.Write(ctx, bytesToken, latest, arbitrary)
	report.ConflictRecovered = err == nil

	server.Configure(func(control *s3fake.Control) { control.Forbidden = true })
	_, _, err = client.Read(ctx, bytesToken)
	report.ForbiddenMapped = errors.Is(err, appsync.ErrForbidden)
	server.Configure(func(control *s3fake.Control) { control.Forbidden = false })

	missingToken := domainsync.Token{
		Provider: appsync.ProviderS3,
		S3: &domainsync.S3Credential{
			Endpoint: server.URL() + selftest53BasePath, Region: selftest53Region,
			Bucket: "no-such-bucket", Prefix: "team/bytes",
			AccessKeyID: s3fake.DefaultAccessKeyID, SecretAccessKey: s3fake.DefaultSecretAccessKey,
			SessionToken: selftest53Session,
		},
	}
	_, _, err = client.Read(ctx, missingToken)
	report.MissingBucketError = errors.Is(err, appsync.ErrInvalidEndpoint)

	// 9. Redaction: profile, snapshot, fake state, credential store, traces.
	configData, err := os.ReadFile(filepath.Join(directory, "cloud-sync.json"))
	if err != nil {
		return report, fmt.Errorf("read the profile file: %w", err)
	}
	report.ConfigFileNoSecret = !containsAny(string(configData),
		s3fake.DefaultAccessKeyID, s3fake.DefaultSecretAccessKey,
		selftest53Session, selftest53Password)
	snapshotData, err := json.Marshal(service.Snapshot())
	if err != nil {
		return report, fmt.Errorf("serialize the snapshot: %w", err)
	}
	report.SnapshotNoSecret = !containsAny(string(snapshotData),
		s3fake.DefaultAccessKeyID, s3fake.DefaultSecretAccessKey,
		selftest53Session, selftest53Password)

	finalState := server.State()
	finalData, err := json.Marshal(finalState)
	if err != nil {
		return report, fmt.Errorf("serialize the fake state: %w", err)
	}
	report.FakeStateNoSecret = !containsAny(string(finalData),
		s3fake.DefaultAccessKeyID, s3fake.DefaultSecretAccessKey,
		selftest53Session, selftest53Password)
	report.SignatureVerified = finalState.Counters.Verified
	report.SignatureFailures = finalState.Counters.SignatureFailures
	report.SecretViolations = finalState.Counters.SecretViolations +
		regionState.Counters.SecretViolations + tokenState.Counters.SecretViolations
	report.TraceRedacted = true
	for _, trace := range finalState.Traces {
		if trace.Authorization == "" {
			continue
		}
		if trace.Authorization != adapters3.Algorithm+" <redacted>" ||
			strings.Contains(trace.Authorization, s3fake.DefaultSecretAccessKey) {
			report.TraceRedacted = false
			break
		}
	}

	return report, nil
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func selftest53Host(id string, when time.Time) domainvault.Host {
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

func selftest53Payload(content []byte) (appsync.Payload, error) {
	envelope, err := domainsync.UnmarshalFile(content)
	if err != nil {
		return appsync.Payload{}, err
	}
	plaintext, err := domainsync.DecryptPayload(envelope, selftest53Password)
	if err != nil {
		return appsync.Payload{}, err
	}
	var payload appsync.Payload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return appsync.Payload{}, err
	}
	return payload, nil
}
