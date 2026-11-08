package remote

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// loginBodyTransport keeps the login request so the test can replay the exact
// buffer the adapter marshaled. bytes.Reader's GetBody closure shares that
// buffer, so a replay observes a wipe the adapter performed.
type loginBodyTransport struct {
	request *http.Request
}

func (transport *loginBodyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			headerAtuinVersion: []string{historysync.PinnedAtuinVersion},
			"Content-Type":     []string{"application/json"},
		},
		Body:    io.NopCloser(strings.NewReader(`{"session":"minted","auth":"cli","user_id":"u-1"}`)),
		Request: request,
	}, nil
}

func TestAuthenticateWipesTheLoginRequestBody(t *testing.T) {
	transport := &loginBodyTransport{}
	factory := NewFactory(Options{HTTPClient: &http.Client{Transport: transport}})
	remote, err := factory.Open(context.Background(), historysync.Config{
		BaseURL: "https://atuin.example.test", AuthMode: atuinmodel.AuthModeSession,
		Username: "alice", Password: "hunter2",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer remote.Close()

	result, err := remote.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if result.Token != "minted" {
		t.Fatalf("minted token = %q, want minted", result.Token)
	}
	if transport.request == nil || transport.request.GetBody == nil {
		t.Fatal("the login request had no replayable body")
	}

	replay, err := transport.request.GetBody()
	if err != nil {
		t.Fatalf("replay the login body: %v", err)
	}
	defer replay.Close()
	body, err := io.ReadAll(replay)
	if err != nil {
		t.Fatalf("read the login body: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("the login request body was empty")
	}
	if bytes.Contains(body, []byte("hunter2")) {
		// Do not print the body: a regression would publish the password to CI
		// logs, which is the disclosure this test exists to prevent.
		t.Fatal("the login request body still contains the password")
	}
	for index, value := range body {
		if value != 0 {
			t.Fatalf("login body byte %d = %d after Authenticate, want zero", index, value)
		}
	}
}
