package fake_test

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/s3"
	"github.com/mslxl/natcatty/adapters/s3/fake"
)

// signedRequest builds a request exactly as the client would, but with the
// signing steps exposed so the test can tamper after signing.
func signedRequest(t *testing.T, method, target string, payload []byte, token string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(string(payload)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	hash := s3.PayloadSHA256(payload)
	request.Header.Set("x-amz-content-sha256", hash)
	request.Header.Set("x-amz-date", at.Format("20060102T150405Z"))
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("x-amz-security-token", token)
	}
	parsed, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}
	signed, err := s3.Sign(s3.SignInput{
		Method:      method,
		Host:        parsed.Host,
		URI:         parsed.Path,
		Headers:     request.Header,
		PayloadHash: hash,
		Region:      fake.DefaultRegion,
		Service:     s3.Service,
		Time:        at,
	}, s3.Credentials{
		AccessKeyID:     fake.DefaultAccessKeyID,
		SecretAccessKey: fake.DefaultSecretAccessKey,
		SessionToken:    token,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	request.Header.Set("Authorization", signed.Authorization)
	return request
}

func send(t *testing.T, request *http.Request) (*http.Response, string) {
	t.Helper()
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response, string(body)
}

func TestFakeVerifiesSigV4ServerSide(t *testing.T) {
	const token = "session-token-value"
	server, err := fake.Start()
	if err != nil {
		t.Fatalf("start fake: %v", err)
	}
	t.Cleanup(server.Close)
	server.Configure(func(control *fake.Control) { control.SessionToken = token })

	target := server.URL() + "/" + fake.DefaultBucket + "/netcatty-vault.json"
	response, body := send(t, signedRequest(t, http.MethodGet, target, nil, token))
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET status = %d body = %s", response.StatusCode, body)
	}

	put := signedRequest(t, http.MethodPut, target, []byte("arbitrary\x00bytes\xff"), token)
	response, body = send(t, put)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("PUT status = %d body = %s", response.StatusCode, body)
	}
	if response.Header.Get("ETag") == "" {
		t.Fatal("PUT returned no ETag")
	}
	if stored, ok := server.ObjectContent("netcatty-vault.json"); !ok ||
		string(stored) != "arbitrary\x00bytes\xff" {
		t.Fatalf("stored bytes = %q exists=%v", stored, ok)
	}
	state := server.State()
	if state.Counters.Verified < 2 || state.Counters.PathStyleRequests < 2 {
		t.Fatalf("verification counters = %+v", state.Counters)
	}
	if state.Counters.SignatureFailures != 0 || state.Counters.SessionTokenFailures != 0 {
		t.Fatalf("valid requests failed verification: %+v", state.Counters)
	}
	for _, trace := range state.Traces {
		if !strings.Contains(trace.SignedHeaders, "x-amz-security-token") {
			t.Fatalf("the token was not signed: %+v", trace)
		}
		if trace.Authorization != s3.Algorithm+" <redacted>" {
			t.Fatalf("trace authorization = %q", trace.Authorization)
		}
	}
}

func TestFakeRejectsTampering(t *testing.T) {
	const token = "session-token-value"
	server, err := fake.Start()
	if err != nil {
		t.Fatalf("start fake: %v", err)
	}
	t.Cleanup(server.Close)
	server.Configure(func(control *fake.Control) { control.SessionToken = token })
	target := server.URL() + "/" + fake.DefaultBucket + "/netcatty-vault.json"

	t.Run("session token changed after signing", func(t *testing.T) {
		request := signedRequest(t, http.MethodGet, target, nil, token)
		request.Header.Set("x-amz-security-token", "tampered-token")
		response, body := send(t, request)
		if response.StatusCode != http.StatusForbidden || !strings.Contains(body, "InvalidToken") {
			t.Fatalf("status = %d body = %s", response.StatusCode, body)
		}
	})

	t.Run("body changed after signing", func(t *testing.T) {
		request := signedRequest(t, http.MethodPut, target, []byte("original"), token)
		request.Body = io.NopCloser(strings.NewReader("tampered"))
		request.ContentLength = int64(len("tampered"))
		response, body := send(t, request)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, "XAmzContentSHA256Mismatch") {
			t.Fatalf("status = %d body = %s", response.StatusCode, body)
		}
	})

	t.Run("signed header changed after signing", func(t *testing.T) {
		request := signedRequest(t, http.MethodPut, target, []byte("original"), token)
		request.Header.Set("Content-Type", "text/plain")
		response, body := send(t, request)
		if response.StatusCode != http.StatusForbidden || !strings.Contains(body, "SignatureDoesNotMatch") {
			t.Fatalf("status = %d body = %s", response.StatusCode, body)
		}
	})

	t.Run("unsigned x-amz header added", func(t *testing.T) {
		request := signedRequest(t, http.MethodGet, target, nil, token)
		request.Header.Set("x-amz-meta-extra", "value")
		response, body := send(t, request)
		if response.StatusCode != http.StatusForbidden || !strings.Contains(body, "SignatureDoesNotMatch") {
			t.Fatalf("status = %d body = %s", response.StatusCode, body)
		}
	})

	t.Run("wrong signature", func(t *testing.T) {
		request := signedRequest(t, http.MethodGet, target, nil, token)
		authorization := request.Header.Get("Authorization")
		signatureAt := strings.LastIndex(authorization, "Signature=")
		if signatureAt < 0 {
			t.Fatal("signed request has no signature")
		}
		request.Header.Set("Authorization", authorization[:signatureAt]+"Signature="+strings.Repeat("0", 64))
		response, body := send(t, request)
		if response.StatusCode != http.StatusForbidden || !strings.Contains(body, "SignatureDoesNotMatch") {
			t.Fatalf("status = %d body = %s", response.StatusCode, body)
		}
	})

	state := server.State()
	if state.Counters.SignatureFailures < 3 {
		t.Fatalf("signature failures = %+v", state.Counters)
	}
	if state.Counters.SessionTokenFailures < 1 {
		t.Fatalf("session token failures = %+v", state.Counters)
	}
	encoded := state.Traces
	for _, trace := range encoded {
		if strings.Contains(trace.Authorization, "tampered") {
			t.Fatalf("trace stored a tampered Authorization: %+v", trace)
		}
	}
}
