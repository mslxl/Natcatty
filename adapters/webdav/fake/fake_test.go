package fake

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func basicHeader(username, password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
}

func mustRequest(t *testing.T, method, target string, body []byte, headers map[string]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, target, err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	return response
}

func readBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

func TestFakeSupportsOptionsMKCOLAndConditionalWrites(t *testing.T) {
	server, err := Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Close()
	auth := map[string]string{"Authorization": basicHeader(defaultUsername, defaultPassword)}
	fileURL := server.CollectionURL() + "/" + "netcatty-vault.json"

	options := mustRequest(t, http.MethodOptions, server.CollectionURL(), nil, nil)
	readBody(t, options)
	if options.StatusCode != http.StatusOK || !strings.Contains(options.Header.Get("Allow"), "MKCOL") {
		t.Fatalf("OPTIONS = %d allow %q", options.StatusCode, options.Header.Get("Allow"))
	}

	missing := mustRequest(t, http.MethodGet, fileURL, nil, auth)
	readBody(t, missing)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("GET missing = %d", missing.StatusCode)
	}

	payload := []byte{0x00, 0xff, '\n', 'a'}
	created := mustRequest(t, http.MethodPut, fileURL, payload, auth)
	readBody(t, created)
	if created.StatusCode != http.StatusCreated || created.Header.Get("ETag") == "" {
		t.Fatalf("PUT create = %d etag %q", created.StatusCode, created.Header.Get("ETag"))
	}
	etag := created.Header.Get("ETag")

	head := mustRequest(t, http.MethodHead, fileURL, nil, auth)
	readBody(t, head)
	if head.StatusCode != http.StatusOK || head.Header.Get("ETag") != etag {
		t.Fatalf("HEAD = %d etag %q", head.StatusCode, head.Header.Get("ETag"))
	}
	got := mustRequest(t, http.MethodGet, fileURL, nil, auth)
	if body := readBody(t, got); !bytes.Equal(body, payload) {
		t.Fatalf("GET = %x, want %x", body, payload)
	}

	stale := mustRequest(t, http.MethodPut, fileURL, []byte("stale"), map[string]string{
		"Authorization": auth["Authorization"], "If-Match": `"wrong"`,
	})
	readBody(t, stale)
	if stale.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match = %d", stale.StatusCode)
	}
	unconditional := mustRequest(t, http.MethodPut, fileURL, []byte("again"), map[string]string{
		"Authorization": auth["Authorization"], "If-None-Match": "*",
	})
	readBody(t, unconditional)
	if unconditional.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("If-None-Match create over existing = %d", unconditional.StatusCode)
	}

	collection := mustRequest(t, methodMKCOL, server.URL()+"/dav/sub", nil, auth)
	readBody(t, collection)
	if collection.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL = %d", collection.StatusCode)
	}
	again := mustRequest(t, methodMKCOL, server.URL()+"/dav/sub", nil, auth)
	readBody(t, again)
	if again.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("second MKCOL = %d", again.StatusCode)
	}
	orphan := mustRequest(t, http.MethodPut, server.URL()+"/missing/file.json", []byte("x"), auth)
	readBody(t, orphan)
	if orphan.StatusCode != http.StatusConflict {
		t.Fatalf("PUT into a missing collection = %d", orphan.StatusCode)
	}

	state := server.State()
	if state.Counters.PreconditionFailures != 2 {
		t.Fatalf("precondition failures = %d", state.Counters.PreconditionFailures)
	}
	if state.Counters.Mkcols != 2 || state.Counters.Heads == 0 || state.Counters.Gets == 0 {
		t.Fatalf("counters = %+v", state.Counters)
	}
}

func TestFakeConflictWritesRejectsEveryConditionalPut(t *testing.T) {
	server, err := Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Close()
	auth := map[string]string{"Authorization": basicHeader(defaultUsername, defaultPassword)}
	fileURL := server.CollectionURL() + "/netcatty-vault.json"
	created := mustRequest(t, http.MethodPut, fileURL, []byte("first"), auth)
	readBody(t, created)
	etag := created.Header.Get("ETag")

	server.Configure(func(control *Control) { control.ConflictWrites = true })
	for name, headers := range map[string]map[string]string{
		"if-match":      {"Authorization": auth["Authorization"], "If-Match": etag},
		"if-none-match": {"Authorization": auth["Authorization"], "If-None-Match": "*"},
	} {
		response := mustRequest(t, http.MethodPut, fileURL, []byte("second"), headers)
		readBody(t, response)
		if response.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("%s PUT = %d, want 412", name, response.StatusCode)
		}
	}
	if content, ok := server.FileContent(server.CollectionPath(), "netcatty-vault.json"); !ok || string(content) != "first" {
		t.Fatalf("conflicting writes changed the file: %q %v", content, ok)
	}

	server.Configure(func(control *Control) { control.ConflictWrites = false })
	recovered := mustRequest(t, http.MethodPut, fileURL, []byte("second"), map[string]string{
		"Authorization": auth["Authorization"], "If-Match": etag,
	})
	readBody(t, recovered)
	if recovered.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT after clearing the conflict = %d", recovered.StatusCode)
	}
	if state := server.State(); state.Counters.PreconditionFailures < 2 {
		t.Fatalf("precondition failures = %d", state.Counters.PreconditionFailures)
	}
}

func TestFakeRedactsAuthorizationAndCountsEscapedSecrets(t *testing.T) {
	server, err := Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Close()
	auth := map[string]string{"Authorization": basicHeader(defaultUsername, defaultPassword)}
	response := mustRequest(t, http.MethodGet, server.CollectionURL()+"/x", nil, auth)
	readBody(t, response)
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("GET = %d", response.StatusCode)
	}
	state := server.State()
	encoded, _ := json.Marshal(state)
	if strings.Contains(string(encoded), defaultPassword) || strings.Contains(string(encoded), defaultToken) {
		t.Fatalf("state leaked a secret: %s", encoded)
	}
	for _, trace := range state.Traces {
		if trace.Authorization != "" && trace.Authorization != "Basic <redacted>" {
			t.Fatalf("trace authorization = %q", trace.Authorization)
		}
	}

	escaped := mustRequest(t, http.MethodGet, server.CollectionURL()+"/x?access_token="+defaultToken, nil, auth)
	readBody(t, escaped)
	if violations := server.State().Counters.SecretViolations; violations != 1 {
		t.Fatalf("secret violations = %d, want 1", violations)
	}
}

func TestFakeDigestChallengeUsesARealNonce(t *testing.T) {
	server, err := Start()
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer server.Close()
	server.Configure(func(control *Control) {
		control.AuthMode = "digest"
		control.DigestAlgorithm = "SHA-256"
	})
	response := mustRequest(t, http.MethodGet, server.CollectionURL()+"/x", nil, nil)
	readBody(t, response)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET = %d", response.StatusCode)
	}
	challenge := response.Header.Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Digest ") || !strings.Contains(challenge, "nonce=") ||
		!strings.Contains(challenge, "qop=\"auth\"") || !strings.Contains(challenge, "algorithm=SHA-256") {
		t.Fatalf("challenge = %q", challenge)
	}
	if counters := server.State().Counters; counters.DigestChallenges != 1 || counters.Unauthorized != 1 {
		t.Fatalf("counters = %+v", counters)
	}
}
