package remote

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	historysync "github.com/mslxl/natcatty/application/historysync"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

// retainedResponseBody records the destination slice io.ReadAll hands to Read.
// That slice shares its backing array with the buffer do returns, so after
// Authenticate the test can observe whether the adapter zeroed the response
// that carried the minted session token.
type retainedResponseBody struct {
	payload   []byte
	readBuf   []byte
	readError bool
	failed    bool
}

func (body *retainedResponseBody) Read(destination []byte) (int, error) {
	if len(body.payload) == 0 {
		if body.readError && !body.failed {
			body.failed = true
			return 0, errors.New("injected response read failure")
		}
		return 0, io.EOF
	}
	written := copy(destination, body.payload)
	body.readBuf = destination[:written]
	body.payload = body.payload[written:]
	return written, nil
}

func (body *retainedResponseBody) Close() error { return nil }

type loginResponseWipeTransport struct {
	payload   []byte
	readError bool
	body      *retainedResponseBody
}

func (transport *loginResponseWipeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.body = &retainedResponseBody{
		payload: transport.payload, readError: transport.readError,
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			headerAtuinVersion: []string{historysync.PinnedAtuinVersion},
			"Content-Type":     []string{"application/json"},
		},
		Body:    transport.body,
		Request: request,
	}, nil
}

func authenticateWithResponse(t *testing.T, payload string) (*loginResponseWipeTransport, error) {
	t.Helper()
	return authenticateWithTransport(t, &loginResponseWipeTransport{payload: []byte(payload)})
}

func authenticateWithTransport(t *testing.T,
	transport *loginResponseWipeTransport) (*loginResponseWipeTransport, error) {
	t.Helper()
	factory := NewFactory(Options{HTTPClient: &http.Client{Transport: transport}})
	remote, err := factory.Open(context.Background(), historysync.Config{
		BaseURL: "https://atuin.example.test", AuthMode: atuinmodel.AuthModeSession,
		Username: "alice", Password: "hunter2",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = remote.Authenticate(context.Background())
	_ = remote.Close()
	return transport, err
}

// TestAuthenticateWipesTheLoginResponseBody proves the buffer that decoded the
// minted session token is zeroed after a successful login, including the token
// bytes. The decoded token string remains available by design.
func TestAuthenticateWipesTheLoginResponseBody(t *testing.T) {
	const token = "minted-session-token-that-must-not-linger"
	transport, err := authenticateWithResponse(t,
		`{"session":"`+token+`","auth":"cli","user_id":"u-1"}`)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	assertRetainedResponseWiped(t, transport)
}

// TestAuthenticateWipesTheLoginResponseOnDecodeFailure covers the decode error
// path: a malformed login response still zeroes the buffer.
func TestAuthenticateWipesTheLoginResponseOnDecodeFailure(t *testing.T) {
	transport, err := authenticateWithResponse(t, `{"session":"minted-token"`)
	if err == nil {
		t.Fatal("a malformed login response was accepted")
	}
	assertRetainedResponseWiped(t, transport)
}

// TestAuthenticateWipesTheLoginResponseOnReadFailure covers the partial-read
// path: io.ReadAll returns the bytes it already read together with the error,
// and do must zero that buffer before discarding it.
func TestAuthenticateWipesTheLoginResponseOnReadFailure(t *testing.T) {
	const token = "partial-read-minted-session-token"
	transport, err := authenticateWithTransport(t, &loginResponseWipeTransport{
		payload:   []byte(`{"session":"` + token + `","auth":"cli","user_id":"u-1"}`),
		readError: true,
	})
	if err == nil {
		t.Fatal("a failing login response read was accepted")
	}
	assertRetainedResponseWiped(t, transport)
}

func assertRetainedResponseWiped(t *testing.T, transport *loginResponseWipeTransport) {
	t.Helper()
	if transport.body == nil || len(transport.body.readBuf) == 0 {
		t.Fatal("the test did not observe the response buffer")
	}
	for index, value := range transport.body.readBuf {
		if value != 0 {
			// Never print the buffer: a regression would publish the session
			// token to CI logs.
			t.Fatalf("login response byte %d = %d after Authenticate, want zero", index, value)
		}
	}
}
