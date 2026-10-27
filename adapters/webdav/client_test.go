package webdav

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/webdav/fake"
	appsync "github.com/mslxl/natcatty/application/sync"
	domainsync "github.com/mslxl/natcatty/domain/sync"
)

func testContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func startFake(t *testing.T) *fake.Server {
	t.Helper()
	server, err := fake.Start()
	if err != nil {
		t.Fatalf("start fake: %v", err)
	}
	t.Cleanup(server.Close)
	return server
}

func settingsFor(server *fake.Server, authType, username, password, token string) appsync.WebDAVSettings {
	return appsync.WebDAVSettings{
		Endpoint: server.CollectionURL(),
		AuthType: authType,
		Username: username,
		Password: password,
		Token:    token,
	}
}

func connect(t *testing.T, client *Client, settings appsync.WebDAVSettings) (domainsync.Token, appsync.Grant) {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	token, grant, err := client.Connect(ctx, settings)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return token, grant
}

func TestNormalizeEndpoint(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"adds https", "dav.example.test/dav", "https://dav.example.test/dav"},
		{"trims trailing slash", " https://dav.example.test/dav/ ", "https://dav.example.test/dav"},
		{"root", "https://dav.example.test/", "https://dav.example.test"},
		{"cleans path", "https://dav.example.test//dav/./sub/../", "https://dav.example.test/dav"},
		{"lowercases host", "HTTPS://DAV.EXAMPLE.TEST/Dav", "https://dav.example.test/Dav"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeEndpoint(test.raw)
			if err != nil {
				t.Fatalf("NormalizeEndpoint(%q): %v", test.raw, err)
			}
			if got != test.want {
				t.Fatalf("NormalizeEndpoint(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}

	for _, raw := range []string{
		"",
		"ftp://dav.example.test/dav",
		"https://user:pass@dav.example.test/dav",
		"https://dav.example.test/dav?token=x",
		"https://dav.example.test/dav#fragment",
		"https:///dav",
	} {
		if _, err := NormalizeEndpoint(raw); !errors.Is(err, appsync.ErrInvalidEndpoint) {
			t.Errorf("NormalizeEndpoint(%q) error = %v, want ErrInvalidEndpoint", raw, err)
		}
	}
}

func TestPrepareValidatesAndFlagsInsecure(t *testing.T) {
	client := New(Options{})
	settings, insecure, err := client.Prepare(appsync.WebDAVSettings{
		Endpoint: "http://dav.example.test/dav/",
		AuthType: "token",
		Token:    " bearer-token ",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !insecure {
		t.Fatal("plain HTTP must be reported insecure")
	}
	if settings.Endpoint != "http://dav.example.test/dav" {
		t.Fatalf("endpoint = %q", settings.Endpoint)
	}
	if settings.AuthType != domainsync.WebDAVAuthBearer || settings.Token != "bearer-token" {
		t.Fatalf("settings = %+v", settings)
	}

	if _, secure, err := client.Prepare(appsync.WebDAVSettings{
		Endpoint: "https://dav.example.test/dav", AuthType: "basic", Username: "u", Password: "p",
	}); err != nil || secure {
		t.Fatalf("https Prepare = secure %v, err %v", secure, err)
	}

	for _, invalid := range []appsync.WebDAVSettings{
		{Endpoint: "https://dav.example.test/dav", AuthType: "basic", Username: "u"},
		{Endpoint: "https://dav.example.test/dav", AuthType: "digest", Password: "p"},
		{Endpoint: "https://dav.example.test/dav", AuthType: "bearer"},
	} {
		if _, _, err := client.Prepare(invalid); !errors.Is(err, appsync.ErrInvalidEndpoint) {
			t.Errorf("Prepare(%+v) error = %v, want ErrInvalidEndpoint", invalid, err)
		}
	}
}

func TestBasicAuthUsesUTF8AndRoundTripsBytes(t *testing.T) {
	server := startFake(t)
	const username = "üser"
	const password = "pässwörd-秘密"
	server.Configure(func(control *fake.Control) {
		control.AuthMode = domainsync.WebDAVAuthBasic
		control.Username = username
		control.Password = password
	})
	client := New(Options{})
	token, grant := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, username, password, ""))
	if !strings.HasPrefix(grant.Login, username+"@") {
		t.Fatalf("login = %q", grant.Login)
	}

	ctx, cancel := testContext(t)
	defer cancel()
	file, exists, err := client.Read(ctx, token)
	if err != nil || exists {
		t.Fatalf("empty read = %+v, exists %v, err %v", file, exists, err)
	}
	payload := []byte{0x00, 0x01, 0xff, '\n', '{', '"', 0xc3, 0xbc, '}'}
	written, err := client.Write(ctx, token, file, payload)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written.ETag == "" {
		t.Fatal("expected an ETag from the fake")
	}
	got, exists, err := client.Read(ctx, token)
	if err != nil || !exists {
		t.Fatalf("read = exists %v, err %v", exists, err)
	}
	if !bytes.Equal(got.Content, payload) {
		t.Fatalf("round trip changed bytes: %x != %x", got.Content, payload)
	}

	state := server.State()
	if state.Counters.BasicSuccesses == 0 {
		t.Fatal("fake saw no successful Basic authentication")
	}
	assertNoSecretsInState(t, state, password)
}

func TestDigestAuthChallengeResponseAcrossAlgorithms(t *testing.T) {
	for _, algorithm := range []string{"MD5", "MD5-sess", "SHA-256", "SHA-256-sess"} {
		t.Run(algorithm, func(t *testing.T) {
			server := startFake(t)
			const username = "digest-user"
			const password = "digest-pässwörd"
			server.Configure(func(control *fake.Control) {
				control.AuthMode = domainsync.WebDAVAuthDigest
				control.Username = username
				control.Password = password
				control.DigestAlgorithm = algorithm
			})
			client := New(Options{})
			token, _ := connect(t, client, settingsFor(server, domainsync.WebDAVAuthDigest, username, password, ""))

			ctx, cancel := testContext(t)
			defer cancel()
			payload := []byte(`{"meta":{"version":1},"payload":"Y2lwaGVy"}`)
			file, _, err := client.Read(ctx, token)
			if err != nil {
				t.Fatalf("read before write: %v", err)
			}
			if _, err := client.Write(ctx, token, file, payload); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, exists, err := client.Read(ctx, token)
			if err != nil || !exists || !bytes.Equal(got.Content, payload) {
				t.Fatalf("read back exists %v err %v content %q", exists, err, got.Content)
			}

			state := server.State()
			if state.Counters.DigestChallenges == 0 {
				t.Fatal("fake issued no Digest challenge")
			}
			if state.Counters.DigestSuccesses == 0 {
				t.Fatal("fake saw no successful Digest response")
			}
			if state.Counters.Unauthorized == 0 {
				t.Fatal("expected at least the initial unauthenticated challenge")
			}
			assertNoSecretsInState(t, state, password)
		})
	}
}

func TestDigestStaleNonceIsRetriedOnce(t *testing.T) {
	server := startFake(t)
	const username = "digest-user"
	const password = "digest-password"
	server.Configure(func(control *fake.Control) {
		control.AuthMode = domainsync.WebDAVAuthDigest
		control.Username = username
		control.Password = password
		control.DigestStale = true
	})
	client := New(Options{})
	connect(t, client, settingsFor(server, domainsync.WebDAVAuthDigest, username, password, ""))
	state := server.State()
	if state.Counters.DigestChallenges < 2 {
		t.Fatalf("Digest challenges = %d, want the initial and stale challenges", state.Counters.DigestChallenges)
	}
	if state.Counters.DigestSuccesses == 0 {
		t.Fatal("stale retry did not succeed")
	}
	if state.Control.StalePending {
		t.Fatal("stale flag was not consumed exactly once")
	}
}

func TestDigestWrongPasswordIsACredentialRejectionWithoutALoop(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) {
		control.AuthMode = domainsync.WebDAVAuthDigest
		control.Username = "digest-user"
		control.Password = "right-password"
	})
	client := New(Options{})
	ctx, cancel := testContext(t)
	defer cancel()
	_, _, err := client.Connect(ctx, settingsFor(server, domainsync.WebDAVAuthDigest, "digest-user", "wrong-password", ""))
	if !errors.Is(err, appsync.ErrCredentialsRejected) {
		t.Fatalf("error = %v, want ErrCredentialsRejected", err)
	}
	if requests := server.State().Counters.Requests; requests > 3 {
		t.Fatalf("wrong Digest password caused %d requests; must not loop", requests)
	}
}

func TestBearerAuth(t *testing.T) {
	server := startFake(t)
	const tokenValue = "fake-bearer-token-abc123"
	server.Configure(func(control *fake.Control) {
		control.AuthMode = domainsync.WebDAVAuthBearer
		control.Token = tokenValue
	})
	client := New(Options{})
	token, grant := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBearer, "", "", tokenValue))
	if grant.Login == "" || !strings.Contains(grant.ID, "/dav") {
		t.Fatalf("grant = %+v", grant)
	}

	ctx, cancel := testContext(t)
	defer cancel()
	payload := []byte(`{"payload":"bearer"}`)
	file, _, err := client.Read(ctx, token)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := client.Write(ctx, token, file, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, exists, err := client.Read(ctx, token)
	if err != nil || !exists || !bytes.Equal(got.Content, payload) {
		t.Fatalf("read back exists %v err %v", exists, err)
	}
	state := server.State()
	if state.Counters.BearerSuccesses == 0 {
		t.Fatal("fake saw no successful bearer authentication")
	}
	assertNoSecretsInState(t, state, tokenValue)
}

func TestCreateAndUpdateUseConditionalHeaders(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	token, _ := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	ctx, cancel := testContext(t)
	defer cancel()

	first, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.ETag == "" {
		t.Fatal("create returned no ETag")
	}
	if _, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte(`{"n":2}`)); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("second unconditional create = %v, want ErrConflict", err)
	}
	if err := server.BumpETag("/dav", domainsync.SyncFileName); err != nil {
		t.Fatalf("bump etag: %v", err)
	}
	if _, err := client.Write(ctx, token, first, []byte(`{"n":3}`)); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	latest, exists, err := client.Read(ctx, token)
	if err != nil || !exists {
		t.Fatalf("reread exists %v err %v", exists, err)
	}
	if _, err := client.Write(ctx, token, latest, []byte(`{"n":4}`)); err != nil {
		t.Fatalf("fresh update: %v", err)
	}
	got, _, err := client.Read(ctx, token)
	if err != nil || string(got.Content) != `{"n":4}` {
		t.Fatalf("final content = %q, err %v", got.Content, err)
	}

	state := server.State()
	if state.Counters.PreconditionFailures < 2 {
		t.Fatalf("precondition failures = %d, want >= 2", state.Counters.PreconditionFailures)
	}
	var sawIfNoneMatch, sawIfMatch bool
	for _, trace := range state.Traces {
		if trace.IfNoneMatch == "*" {
			sawIfNoneMatch = true
		}
		if trace.IfMatch != "" {
			sawIfMatch = true
		}
	}
	if !sawIfNoneMatch || !sawIfMatch {
		t.Fatalf("traces missing conditional writes: if-none-match=%v if-match=%v", sawIfNoneMatch, sawIfMatch)
	}
}

func TestNoETagFallsBackToAFreshReadBeforeWriting(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.NoETag = true })
	client := New(Options{})
	token, _ := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	ctx, cancel := testContext(t)
	defer cancel()

	created, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ETag != "" {
		t.Fatalf("ETag = %q despite NoETag", created.ETag)
	}
	current, exists, err := client.Read(ctx, token)
	if err != nil || !exists {
		t.Fatalf("read exists %v err %v", exists, err)
	}
	if err := server.SeedFile("/dav", domainsync.SyncFileName, []byte(`{"n":2}`)); err != nil {
		t.Fatalf("seed peer change: %v", err)
	}
	if _, err := client.Write(ctx, token, current, []byte(`{"n":3}`)); !errors.Is(err, appsync.ErrConflict) {
		t.Fatalf("write over a changed file without ETag = %v, want ErrConflict", err)
	}
	latest, _, err := client.Read(ctx, token)
	if err != nil {
		t.Fatalf("reread: %v", err)
	}
	if _, err := client.Write(ctx, token, latest, []byte(`{"n":4}`)); err != nil {
		t.Fatalf("write after fresh read: %v", err)
	}
}

func TestForbiddenIsDistinctFromCredentialRejection(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	ctx, cancel := testContext(t)
	defer cancel()
	token, _ := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))

	server.Configure(func(control *fake.Control) { control.Forbidden = true })
	if _, _, err := client.Read(ctx, token); !errors.Is(err, appsync.ErrForbidden) {
		t.Fatalf("read = %v, want ErrForbidden", err)
	}
	server.Configure(func(control *fake.Control) { control.Forbidden = false; control.ReadOnly = true })
	if _, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte(`{}`)); !errors.Is(err, appsync.ErrForbidden) {
		t.Fatalf("read-only write = %v, want ErrForbidden", err)
	}

	_, _, err := client.Connect(ctx, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "wrong", ""))
	if !errors.Is(err, appsync.ErrCredentialsRejected) || errors.Is(err, appsync.ErrForbidden) {
		t.Fatalf("wrong password = %v, want only ErrCredentialsRejected", err)
	}
}

func TestRead404IsTheEmptyState(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	token, _ := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	ctx, cancel := testContext(t)
	defer cancel()
	file, exists, err := client.Read(ctx, token)
	if err != nil || exists || file.ID != "" || len(file.Content) != 0 {
		t.Fatalf("read = %+v exists %v err %v", file, exists, err)
	}
}

func TestConnectingToAMissingCollectionFailsOnTheFirstCreate(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	ctx, cancel := testContext(t)
	defer cancel()
	settings := settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", "")
	settings.Endpoint = server.URL() + "/missing"
	token, _, err := client.Connect(ctx, settings)
	if err != nil {
		t.Fatalf("connect to a missing collection: %v", err)
	}
	if _, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte(`{}`)); !errors.Is(err, appsync.ErrInvalidEndpoint) {
		t.Fatalf("create in a missing collection = %v, want ErrInvalidEndpoint", err)
	}
}

func TestProbeFallsBackWhenHEADIsDisabled(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.DisableHead = true })
	client := New(Options{})
	connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	state := server.State()
	if state.Counters.Heads == 0 || state.Counters.Gets == 0 {
		t.Fatalf("heads %d gets %d; want a HEAD attempt and a GET fallback", state.Counters.Heads, state.Counters.Gets)
	}
}

func TestTLSRejectsASelfSignedCertificate(t *testing.T) {
	server, err := fake.StartTLS()
	if err != nil {
		t.Fatalf("start TLS fake: %v", err)
	}
	t.Cleanup(server.Close)
	client := New(Options{})
	ctx, cancel := testContext(t)
	defer cancel()
	_, _, err = client.Connect(ctx, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	if !errors.Is(err, appsync.ErrTLS) {
		t.Fatalf("self-signed TLS = %v, want ErrTLS", err)
	}
	if requests := server.State().Counters.Requests; requests != 0 {
		t.Fatalf("the fake served %d requests over an unverified TLS connection", requests)
	}
}

func TestSameHostRedirectIsFollowed(t *testing.T) {
	server := startFake(t)
	server.Configure(func(control *fake.Control) { control.Redirect = "same-host" })
	client := New(Options{})
	connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	state := server.State()
	if state.Counters.Redirects == 0 {
		t.Fatal("same-host redirect was not followed")
	}
	if state.Counters.BasicSuccesses == 0 {
		t.Fatal("redirected request was not authenticated")
	}
}

func TestWriteRefusesARedirectThatDropsTheUpload(t *testing.T) {
	server := startFake(t)
	client := New(Options{})
	token, _ := connect(t, client, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
	server.Configure(func(control *fake.Control) { control.Redirect = "same-host" })
	ctx, cancel := testContext(t)
	defer cancel()
	_, err := client.Write(ctx, token, appsync.RemoteFile{}, []byte(`{"upload":true}`))
	if !errors.Is(err, appsync.ErrRedirectRefused) {
		t.Fatalf("PUT redirect = %v, want ErrRedirectRefused", err)
	}
	if state := server.State(); state.Counters.Redirects == 0 {
		t.Fatal("fake saw no redirect")
	}
}

func TestCrossHostAndLoopingRedirectsAreRefused(t *testing.T) {
	for _, mode := range []string{"other-host", "loop"} {
		t.Run(mode, func(t *testing.T) {
			server := startFake(t)
			server.Configure(func(control *fake.Control) { control.Redirect = mode })
			client := New(Options{})
			ctx, cancel := testContext(t)
			defer cancel()
			_, _, err := client.Connect(ctx, settingsFor(server, domainsync.WebDAVAuthBasic, "dav-user", "dav-secret", ""))
			if !errors.Is(err, appsync.ErrRedirectRefused) {
				t.Fatalf("redirect %q = %v, want ErrRedirectRefused", mode, err)
			}
			if state := server.State(); state.Counters.Redirects == 0 {
				t.Fatal("fake saw no redirect")
			}
		})
	}
}

func TestRedirectPolicyRefusesDowngradeAndForeignHost(t *testing.T) {
	via := []*http.Request{{URL: mustParseURL(t, "https://dav.example.test/dav")}}
	for _, target := range []string{
		"http://dav.example.test/dav",
		"https://other.example.test/dav",
		"ftp://dav.example.test/dav",
	} {
		request := &http.Request{URL: mustParseURL(t, target)}
		if err := checkRedirect(request, via); !errors.Is(err, appsync.ErrRedirectRefused) {
			t.Errorf("redirect to %s = %v, want ErrRedirectRefused", target, err)
		}
	}
	upgrade := &http.Request{URL: mustParseURL(t, "https://dav.example.test/dav")}
	viaHTTP := []*http.Request{{URL: mustParseURL(t, "http://dav.example.test/dav")}}
	if err := checkRedirect(upgrade, viaHTTP); err != nil {
		t.Errorf("same-host HTTP to HTTPS upgrade = %v, want nil", err)
	}
}

func TestDigestAuthorizationMatchesTheRFCFormula(t *testing.T) {
	tests := []struct {
		algorithm string
		newHash   func() hash.Hash
		session   bool
	}{
		{"MD5", md5.New, false},
		{"MD5-sess", md5.New, true},
		{"SHA-256", sha256.New, false},
		{"SHA-256-sess", sha256.New, true},
	}
	for _, test := range tests {
		t.Run(test.algorithm, func(t *testing.T) {
			challenge := &digestChallenge{
				Realm: "realm", Nonce: "nonce-value", Opaque: "opaque-value",
				Algorithm: test.algorithm, QopAuth: true,
			}
			header, err := buildDigestAuthorization(challenge, "user", "pässwörd", "PUT", "/dav/netcatty-vault.json")
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if !strings.HasPrefix(header, "Digest ") {
				t.Fatalf("header = %q", header)
			}
			params := testAuthParams(t, header)
			if params["username"] != "user" || params["realm"] != "realm" || params["nonce"] != "nonce-value" ||
				params["uri"] != "/dav/netcatty-vault.json" || params["qop"] != "auth" || params["nc"] != "00000001" {
				t.Fatalf("params = %+v", params)
			}
			cnonce := params["cnonce"]
			if cnonce == "" {
				t.Fatal("missing cnonce")
			}
			digest := func(value string) string {
				sum := test.newHash()
				sum.Write([]byte(value))
				return fmt.Sprintf("%x", sum.Sum(nil))
			}
			ha1 := digest("user:realm:pässwörd")
			if test.session {
				ha1 = digest(ha1 + ":nonce-value:" + cnonce)
			}
			ha2 := digest("PUT:/dav/netcatty-vault.json")
			want := digest(ha1 + ":nonce-value:00000001:" + cnonce + ":auth:" + ha2)
			if params["response"] != want {
				t.Fatalf("response = %q, want %q", params["response"], want)
			}
			if params["opaque"] != "opaque-value" {
				t.Fatalf("opaque = %q", params["opaque"])
			}
		})
	}
}

func TestDigestChallengeParsingHandlesMultipleChallenges(t *testing.T) {
	header := `Basic realm="basic", Digest realm="r", nonce="n", algorithm=SHA-256, qop="auth,auth-int", stale=true`
	params, ok := digestParams(header)
	if !ok {
		t.Fatal("Digest challenge not found")
	}
	// A challenge that offers auth and auth-int must still use auth.
	challenge, err := parseDigestChallenge(params)
	if err != nil {
		t.Fatalf("qop=auth,auth-int offer: %v", err)
	}
	if !challenge.QopAuth {
		t.Fatal("qop=auth,auth-int did not select auth")
	}
	// auth-int alone has no usable option because this client does not send
	// body digests.
	if _, err := parseDigestChallenge(mustDigestParams(t, `Digest realm="r", nonce="n", qop="auth-int"`)); err == nil {
		t.Fatal("qop=auth-int alone was accepted")
	}
	challenge, err = parseDigestChallenge(mustDigestParams(t, `Digest realm="r", nonce="n", algorithm=MD5, qop="auth"`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !challenge.QopAuth || challenge.Algorithm != "MD5" {
		t.Fatalf("challenge = %+v", challenge)
	}
}

func TestUserIdentitySeparatesCollectionsAndCredentials(t *testing.T) {
	base := domainsync.WebDAVCredential{Endpoint: "https://dav.example.test/dav", AuthType: domainsync.WebDAVAuthBasic, Username: "alice"}
	other := base
	other.Endpoint = "https://dav.example.test/other"
	otherUser := base
	otherUser.Username = "bob"
	first, err := grantFor(&base)
	if err != nil {
		t.Fatalf("grantFor: %v", err)
	}
	if first.ID != "https://dav.example.test/dav#alice" || first.Login != "alice@dav.example.test" {
		t.Fatalf("grant = %+v", first)
	}
	otherGrant, _ := grantFor(&other)
	otherUserGrant, _ := grantFor(&otherUser)
	if first.ID == otherGrant.ID || first.ID == otherUserGrant.ID {
		t.Fatal("identity must include the collection and username")
	}
	bearer := domainsync.WebDAVCredential{Endpoint: "https://dav.example.test/dav", AuthType: domainsync.WebDAVAuthBearer}
	bearerGrant, _ := grantFor(&bearer)
	if bearerGrant.Login != "dav.example.test" || strings.Contains(bearerGrant.ID, "#") {
		t.Fatalf("bearer grant = %+v", bearerGrant)
	}
}

func assertNoSecretsInState(t *testing.T, state fake.State, secret string) {
	t.Helper()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("state leaked a credential: %s", encoded)
	}
	for _, trace := range state.Traces {
		if trace.Authorization == "" {
			continue
		}
		if !strings.HasSuffix(trace.Authorization, " <redacted>") {
			t.Fatalf("trace authorization is not redacted: %q", trace.Authorization)
		}
	}
	if state.Counters.SecretViolations != 0 {
		t.Fatalf("fake counted %d secret violations", state.Counters.SecretViolations)
	}
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func mustDigestParams(t *testing.T, header string) string {
	t.Helper()
	params, ok := digestParams(header)
	if !ok {
		t.Fatalf("Digest challenge not found in %q", header)
	}
	return params
}

func testAuthParams(t *testing.T, header string) map[string]string {
	t.Helper()
	result := map[string]string{}
	rest := strings.TrimPrefix(header, "Digest ")
	for _, part := range strings.Split(rest, ", ") {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		result[key] = strings.Trim(value, `"`)
	}
	return result
}
