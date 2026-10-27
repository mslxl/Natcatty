package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/s3/fake"
	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

func startFake(t *testing.T) *fake.Server {
	t.Helper()
	server, err := fake.Start()
	if err != nil {
		t.Fatalf("start fake S3: %v", err)
	}
	t.Cleanup(server.Close)
	return server
}

func settingsFor(server *fake.Server) appsync.S3Settings {
	return appsync.S3Settings{
		Endpoint:        server.URL(),
		Region:          fake.DefaultRegion,
		Bucket:          fake.DefaultBucket,
		Prefix:          "team//vault/",
		AccessKeyID:     fake.DefaultAccessKeyID,
		SecretAccessKey: fake.DefaultSecretAccessKey,
	}
}

func arbitraryBytes() []byte {
	pattern := make([]byte, 256)
	for index := range pattern {
		pattern[index] = byte(index)
	}
	return append(bytes.Repeat(pattern, 512), []byte("\x00\xff\xfe invalid utf8 \x80")...)
}

func TestConnectReadWriteAndConflictAgainstTheFake(t *testing.T) {
	const sessionToken = "session-token-abc123"
	server := startFake(t)
	server.Configure(func(control *fake.Control) {
		control.SessionToken = sessionToken
	})
	client := New(Options{})
	ctx := context.Background()
	settings := settingsFor(server)
	settings.SessionToken = sessionToken

	prepared, insecure, err := client.Prepare(settings)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !insecure {
		t.Fatal("the loopback HTTP fake must be reported as insecure")
	}
	if prepared.Prefix != "team/vault" {
		t.Fatalf("prefix was not normalized: %q", prepared.Prefix)
	}

	token, grant, err := client.Connect(ctx, settings)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if token.Provider != appsync.ProviderS3 {
		t.Fatalf("token provider = %q", token.Provider)
	}
	if token.S3 == nil || token.S3.SessionToken != sessionToken ||
		token.S3.SecretAccessKey != fake.DefaultSecretAccessKey ||
		token.S3.AccessKeyID != fake.DefaultAccessKeyID {
		t.Fatalf("connect did not retain the signing material in the credential blob")
	}
	if token.S3.Endpoint != server.URL() || token.S3.Region != fake.DefaultRegion ||
		token.S3.Bucket != fake.DefaultBucket || token.S3.Prefix != "team/vault" {
		t.Fatalf("connect did not normalize the non-secret settings: %+v", token.S3)
	}
	parsed, err := url.Parse(server.URL())
	if err != nil {
		t.Fatalf("parse fake URL: %v", err)
	}
	if !strings.Contains(grant.Login, parsed.Host) || !strings.Contains(grant.Login, "team/vault") {
		t.Fatalf("grant login = %q, want bucket/prefix@host", grant.Login)
	}
	if !strings.Contains(grant.ID, "/team/vault/"+domainsync.SyncFileName) {
		t.Fatalf("grant identity = %q", grant.ID)
	}
	if strings.Contains(grant.ID, fake.DefaultSecretAccessKey) || strings.Contains(grant.ID, sessionToken) {
		t.Fatal("grant identity leaked signing material")
	}

	empty, exists, err := client.Read(ctx, token)
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if exists {
		t.Fatalf("a fresh bucket reported an existing object: %+v", empty)
	}

	key := "team/vault/" + domainsync.SyncFileName
	firstContent := arbitraryBytes()
	created, err := client.Write(ctx, token, appsync.RemoteFile{}, firstContent)
	if err != nil {
		t.Fatalf("Write create: %v", err)
	}
	if created.ID == "" || created.ETag == "" {
		t.Fatalf("created file has no id/etag: %+v", created)
	}
	stored, ok := server.ObjectContent(key)
	if !ok {
		t.Fatalf("the fake has no object at %q", key)
	}
	if !bytes.Equal(stored, firstContent) {
		t.Fatal("the fake stored different bytes than the client uploaded")
	}

	read, exists, err := client.Read(ctx, token)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !exists || !bytes.Equal(read.Content, firstContent) || read.ETag != created.ETag {
		t.Fatalf("read back = exists %v etag %q, created etag %q", exists, read.ETag, created.ETag)
	}

	secondContent := append([]byte("second revision: "), firstContent...)
	updated, err := client.Write(ctx, token, read, secondContent)
	if err != nil {
		t.Fatalf("Write update: %v", err)
	}
	if updated.ETag == "" || updated.ETag == read.ETag {
		t.Fatalf("update did not produce a new ETag: %q -> %q", read.ETag, updated.ETag)
	}
	if updated.ID != read.ID {
		t.Fatalf("update changed the object id: %q -> %q", read.ID, updated.ID)
	}

	if err := server.BumpETag(key); err != nil {
		t.Fatalf("BumpETag: %v", err)
	}
	_, err = client.Write(ctx, token, updated, []byte("stale write"))
	if !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("stale conditional write error = %v, want ErrConflict", err)
	}
	fresh, exists, err := client.Read(ctx, token)
	if err != nil || !exists {
		t.Fatalf("Read after conflict: exists %v err %v", exists, err)
	}
	if _, err := client.Write(ctx, token, fresh, []byte("fresh write")); err != nil {
		t.Fatalf("Write after re-read: %v", err)
	}

	state := server.State()
	counters := state.Counters
	if counters.Verified == 0 || counters.PathStyleRequests == 0 {
		t.Fatalf("the fake did not verify path-style SigV4 requests: %+v", counters)
	}
	if counters.SignatureFailures != 0 || counters.SessionTokenFailures != 0 ||
		counters.BucketMismatches != 0 || counters.Redirects != 0 ||
		counters.SecretViolations != 0 {
		t.Fatalf("unexpected verification failures: %+v", counters)
	}
	if counters.PreconditionFailures == 0 {
		t.Fatalf("the stale write was not rejected as a precondition failure: %+v", counters)
	}
	if counters.BytesWritten < len(firstContent)+len(secondContent) {
		t.Fatalf("bytes written counter = %d", counters.BytesWritten)
	}

	var sawCreateCondition, sawUpdateCondition, sawToken bool
	for _, trace := range state.Traces {
		if trace.Authorization != Algorithm+" <redacted>" {
			t.Fatalf("trace authorization is not redacted: %q", trace.Authorization)
		}
		if !trace.PathStyle {
			t.Fatalf("trace is not path-style: %+v", trace)
		}
		if trace.SessionTokenPresent {
			sawToken = true
		}
		if trace.IfNoneMatch == "*" {
			sawCreateCondition = true
		}
		if trace.IfMatch != "" {
			sawUpdateCondition = true
		}
	}
	if !sawCreateCondition || !sawUpdateCondition || !sawToken {
		t.Fatalf("conditional/token request evidence missing: create=%v update=%v token=%v",
			sawCreateCondition, sawUpdateCondition, sawToken)
	}

	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	for _, secret := range []string{fake.DefaultSecretAccessKey, sessionToken, fake.DefaultAccessKeyID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the fake state leaked a credential value")
		}
	}
}

func TestEndpointBasePathIsHonored(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) {
		control.BasePath = "/s3/gateway"
	})
	client := New(Options{})
	ctx := context.Background()
	settings := settingsFor(server)
	settings.Endpoint = server.URL() + "/s3/gateway/"
	settings.Prefix = "base/path"

	if _, _, err := client.Connect(ctx, settings); err != nil {
		t.Fatalf("Connect through a base path: %v", err)
	}
	token, _, err := client.Connect(ctx, settings)
	if err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	if token.S3.Endpoint != server.URL()+"/s3/gateway" {
		t.Fatalf("endpoint = %q", token.S3.Endpoint)
	}
	written, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte("through the base path"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	read, exists, err := client.Read(ctx, token)
	if err != nil || !exists {
		t.Fatalf("Read: exists %v err %v", exists, err)
	}
	if !bytes.Equal(read.Content, []byte("through the base path")) {
		t.Fatalf("read back %q", read.Content)
	}
	if _, ok := server.ObjectContent("base/path/" + domainsync.SyncFileName); !ok {
		t.Fatal("the object key did not include the prefix")
	}
	_ = written

	found := false
	for _, trace := range server.State().Traces {
		if strings.HasPrefix(trace.Path, "/s3/gateway/") {
			found = true
		}
	}
	if !found {
		t.Fatal("no request used the endpoint base path")
	}
}

func TestSessionTokenIsEnforced(t *testing.T) {
	const sessionToken = "required-session-token"
	server := startFake(t)
	server.Configure(func(control *fake.Control) {
		control.SessionToken = sessionToken
	})
	client := New(Options{})
	ctx := context.Background()

	without := settingsFor(server)
	if _, _, err := client.Connect(ctx, without); !errors.Is(err, appsync.ErrCredentialsRejected) {
		t.Fatalf("Connect without the session token error = %v", err)
	}
	wrong := settingsFor(server)
	wrong.SessionToken = "wrong-token"
	_, _, err := client.Connect(ctx, wrong)
	if !errors.Is(err, appsync.ErrCredentialsRejected) {
		t.Fatalf("Connect with a wrong session token error = %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("the error leaked the session token: %v", err)
	}
	right := settingsFor(server)
	right.SessionToken = sessionToken
	if _, _, err := client.Connect(ctx, right); err != nil {
		t.Fatalf("Connect with the session token: %v", err)
	}
	state := server.State()
	if state.Counters.SessionTokenFailures != 2 {
		t.Fatalf("session token failures = %d, want 2", state.Counters.SessionTokenFailures)
	}
	if state.Counters.Redirects != 0 || state.Counters.SignatureFailures != 0 {
		t.Fatalf("unexpected counters: %+v", state.Counters)
	}

	server.Reset()
	unexpected := settingsFor(server)
	unexpected.SessionToken = "token-the-fake-does-not-expect"
	if _, _, err := client.Connect(ctx, unexpected); !errors.Is(err, appsync.ErrCredentialsRejected) {
		t.Fatalf("Connect with an unexpected session token error = %v", err)
	}
	if got := server.State().Counters.SessionTokenFailures; got != 1 {
		t.Fatalf("unexpected-token failures = %d", got)
	}
}

func TestWrongRegionIsReportedAndNotFollowed(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) {
		control.Region = "eu-west-1"
	})
	client := New(Options{})
	ctx := context.Background()

	wrong := settingsFor(server)
	wrong.Region = "us-east-1"
	_, _, err := client.Connect(ctx, wrong)
	if !errors.Is(err, appsync.ErrWrongRegion) {
		t.Fatalf("Connect with the wrong region error = %v", err)
	}
	if !strings.Contains(err.Error(), "eu-west-1") {
		t.Fatalf("the wrong-region error does not name the bucket region: %v", err)
	}
	state := server.State()
	if state.Counters.Redirects != 1 {
		t.Fatalf("region redirects = %d, want 1", state.Counters.Redirects)
	}
	if state.Counters.Requests != 1 {
		t.Fatalf("the adapter followed the region redirect: %d requests", state.Counters.Requests)
	}

	right := settingsFor(server)
	right.Region = "eu-west-1"
	if _, _, err := client.Connect(ctx, right); err != nil {
		t.Fatalf("Connect with the correct region: %v", err)
	}
	if state := server.State(); state.Counters.Redirects != 1 {
		t.Fatalf("the correct region still redirected: %+v", state.Counters)
	}
}

func TestHTTPFailuresMapToActionableErrors(t *testing.T) {
	client := New(Options{})
	ctx := context.Background()

	t.Run("forbidden", func(t *testing.T) {
		server := startFake(t)
		server.Configure(func(control *fake.Control) { control.Forbidden = true })
		// A HEAD probe carries no error document, so the adapter cannot tell a
		// bucket-policy denial from a bad signature; it reports the actionable
		// credential problem rather than guessing.
		_, _, err := client.Connect(ctx, settingsFor(server))
		if !errors.Is(err, appsync.ErrCredentialsRejected) {
			t.Fatalf("HEAD 403 error = %v, want ErrCredentialsRejected", err)
		}
		// A GET carries <Code>AccessDenied</Code>, which is a permissions
		// problem, not a signing-material problem.
		token := domainsync.Token{
			Provider: appsync.ProviderS3,
			S3: &domainsync.S3Credential{
				Endpoint: server.URL(), Region: fake.DefaultRegion, Bucket: fake.DefaultBucket,
				AccessKeyID: fake.DefaultAccessKeyID, SecretAccessKey: fake.DefaultSecretAccessKey,
			},
		}
		_, _, err = client.Read(ctx, token)
		if !errors.Is(err, appsync.ErrForbidden) {
			t.Fatalf("GET 403 AccessDenied error = %v, want ErrForbidden", err)
		}
		if errors.Is(err, appsync.ErrCredentialsRejected) {
			t.Fatalf("403 AccessDenied was misreported as a credential problem: %v", err)
		}
		state := server.State()
		if state.Counters.Forbidden != 2 || state.Counters.Requests != 2 {
			t.Fatalf("403 counters = %+v", state.Counters)
		}
	})

	t.Run("server error", func(t *testing.T) {
		server := startFake(t)
		server.Configure(func(control *fake.Control) { control.ServerError = true })
		_, _, err := client.Connect(ctx, settingsFor(server))
		if !errors.Is(err, appsync.ErrNetwork) {
			t.Fatalf("500 error = %v, want ErrNetwork", err)
		}
	})

	t.Run("missing bucket", func(t *testing.T) {
		server := startFake(t)
		settings := settingsFor(server)
		settings.Bucket = "missing-bucket"
		_, _, err := client.Connect(ctx, settings)
		if !errors.Is(err, appsync.ErrInvalidEndpoint) {
			t.Fatalf("missing bucket error = %v, want ErrInvalidEndpoint", err)
		}
		if got := server.State().Counters.BucketMismatches; got == 0 {
			t.Fatal("the fake did not record the bucket mismatch")
		}
	})

	t.Run("forced conflict", func(t *testing.T) {
		server := startFake(t)
		client := New(Options{})
		token, _, err := client.Connect(ctx, settingsFor(server))
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		server.Configure(func(control *fake.Control) { control.ConflictWrites = true })
		_, err = client.Write(ctx, token, appsync.RemoteFile{}, []byte("conflict"))
		if !errors.Is(err, appsync.ErrConflict) {
			t.Fatalf("forced 412 error = %v, want ErrConflict", err)
		}
	})
}

func TestFailuresNeverCarryTheSecret(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	settings := settingsFor(server)
	settings.SecretAccessKey = "{{SECRET_e7a2n4vq}}"
	_, _, err := client.Connect(context.Background(), settings)
	if !errors.Is(err, appsync.ErrCredentialsRejected) {
		t.Fatalf("wrong secret error = %v", err)
	}
	if strings.Contains(err.Error(), settings.SecretAccessKey) {
		t.Fatalf("the error leaked the secret access key: %v", err)
	}
	if strings.Contains(err.Error(), fake.DefaultSecretAccessKey) {
		t.Fatal("the error leaked the fake secret")
	}
	if strings.Contains(err.Error(), "Signature=") {
		t.Fatalf("the error carried an Authorization fragment: %v", err)
	}
	if got := server.State().Counters.SignatureFailures; got == 0 {
		t.Fatal("the fake did not record the signature failure")
	}
}

func TestReadDoesNotTreatAMissingBucketAsAnEmptyAccount(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	token := domainsync.Token{
		Provider: appsync.ProviderS3,
		S3: &domainsync.S3Credential{
			Endpoint: server.URL(), Region: fake.DefaultRegion, Bucket: "another-bucket",
			AccessKeyID: fake.DefaultAccessKeyID, SecretAccessKey: fake.DefaultSecretAccessKey,
		},
	}
	_, _, err := client.Read(context.Background(), token)
	if !errors.Is(err, appsync.ErrInvalidEndpoint) {
		t.Fatalf("Read with a missing bucket error = %v, want ErrInvalidEndpoint", err)
	}
}

func TestClientNeverFollowsARedirect(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.Region = "ap-south-1" })
	client := New(Options{})
	settings := settingsFor(server)
	settings.Region = "us-east-1"
	_, _, _ = client.Connect(context.Background(), settings)
	state := server.State()
	if len(state.Traces) != 1 {
		t.Fatalf("the client followed the redirect: %d traces", len(state.Traces))
	}
	if state.Traces[0].Status != http.StatusMovedPermanently {
		t.Fatalf("trace status = %d", state.Traces[0].Status)
	}
}
