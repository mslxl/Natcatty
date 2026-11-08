//go:build conformance

package atuinconformance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// conformanceProxy is a loopback reverse proxy in front of the local pinned
// server. It is the only way the suite creates a version/capability mismatch
// or an interrupted upload: the pinned server cannot be configured to speak an
// unknown major version, and a real network drop must happen after the server
// commits a record. Every request stays on 127.0.0.1.
type conformanceProxy struct {
	t       *testing.T
	backend *url.URL
	server  *httptest.Server
	client  *http.Client

	mu              sync.Mutex
	rewriteVersion  string
	stripPageSize   bool
	unknownEnforced bool
	dropNextUpload  bool
	tags            []string
}

func newConformanceProxy(t *testing.T, backendURL string) *conformanceProxy {
	t.Helper()
	backend, err := url.Parse(backendURL)
	if err != nil {
		t.Fatalf("parse proxy backend %q: %v", backendURL, err)
	}
	proxy := &conformanceProxy{
		t: t, backend: backend,
		client: &http.Client{
			Timeout: 60 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	proxy.server = httptest.NewServer(http.HandlerFunc(proxy.serve))
	t.Cleanup(proxy.server.Close)
	return proxy
}

// URL is the base address a Netcatty writer points at.
func (proxy *conformanceProxy) URL() string { return proxy.server.URL }

func (proxy *conformanceProxy) rewriteAtuinVersion(version string) {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.rewriteVersion = version
}

func (proxy *conformanceProxy) stripPageSizeCapability() {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.stripPageSize = true
}

func (proxy *conformanceProxy) enforceUnknownCapability() {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.unknownEnforced = true
}

// dropNextUploadResponse forwards the next upload to the server and then
// aborts the client connection without a response. The server commits the
// records; the client cannot know it succeeded.
func (proxy *conformanceProxy) dropNextUploadResponse() {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	proxy.dropNextUpload = true
}

func (proxy *conformanceProxy) requestedTags() []string {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return append([]string(nil), proxy.tags...)
}

func (proxy *conformanceProxy) serve(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	_ = request.Body.Close()
	if err != nil {
		http.Error(writer, "proxy could not read the request", http.StatusBadGateway)
		return
	}

	proxy.mu.Lock()
	if request.URL.Path == "/api/v0/record/next" {
		proxy.tags = append(proxy.tags, request.URL.Query().Get("tag"))
	}
	drop := proxy.dropNextUpload && request.Method == http.MethodPost && request.URL.Path == "/api/v0/record"
	if drop {
		proxy.dropNextUpload = false
	}
	proxy.mu.Unlock()

	target := *proxy.backend
	target.Path = request.URL.Path
	target.RawQuery = request.URL.RawQuery
	backendRequest, err := http.NewRequestWithContext(request.Context(), request.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(writer, "proxy could not build the backend request", http.StatusBadGateway)
		return
	}
	for name, values := range request.Header {
		for _, value := range values {
			backendRequest.Header.Add(name, value)
		}
	}
	backendRequest.Header.Set("Accept-Encoding", "identity")
	response, err := proxy.client.Do(backendRequest)
	if err != nil {
		if drop {
			panic(http.ErrAbortHandler)
		}
		http.Error(writer, "proxy backend request failed", http.StatusBadGateway)
		return
	}
	responseBody, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		if drop {
			panic(http.ErrAbortHandler)
		}
		http.Error(writer, "proxy backend response failed", http.StatusBadGateway)
		return
	}

	proxy.mu.Lock()
	stripPageSize := proxy.stripPageSize && request.URL.Path == "/api/v0/capabilities"
	unknownEnforced := proxy.unknownEnforced && request.URL.Path == "/api/v0/capabilities"
	version := proxy.rewriteVersion
	proxy.mu.Unlock()

	mutated := false
	if stripPageSize || unknownEnforced {
		var document map[string]any
		if err := json.Unmarshal(responseBody, &document); err != nil {
			http.Error(writer, "proxy could not decode the capabilities response", http.StatusBadGateway)
			return
		}
		if stripPageSize {
			if capabilities, ok := document["capabilities"].(map[string]any); ok {
				delete(capabilities, "sh.atuin.server/records.page_size")
			}
		}
		if unknownEnforced {
			document["enforced"] = []any{"sh.atuin.server/conformance-unknown"}
		}
		responseBody, err = json.Marshal(document)
		if err != nil {
			http.Error(writer, "proxy could not encode the capabilities response", http.StatusBadGateway)
			return
		}
		mutated = true
	}

	if drop {
		// Every byte the server wrote has been read: the records are committed
		// before the client connection is aborted with no response.
		panic(http.ErrAbortHandler)
	}

	for name, values := range response.Header {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	if mutated {
		writer.Header().Del("Content-Length")
		writer.Header().Set("Content-Type", "application/json")
	}
	if version != "" {
		writer.Header().Set("Atuin-Version", version)
	}
	writer.WriteHeader(response.StatusCode)
	_, _ = writer.Write(responseBody)
}
