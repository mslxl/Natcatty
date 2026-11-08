package remote

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
)

func TestAuthenticateAccountBadRequestIsAuthFailed(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/me": func(writer http.ResponseWriter, request *http.Request) {
			// The pinned OSS server answers a Bearer header with this 400.
			writeJSON(t, writer, http.StatusBadRequest, map[string]string{
				"reason": "invalid authorization header encoding",
			})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeBearer, Token: "hub-token",
	})
	_, err := remote.Authenticate(context.Background())
	if !errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("Authenticate = %v, want ErrAuthFailed", err)
	}
	if errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("a configuration error was classified retryable: %v", err)
	}
	if strings.Contains(err.Error(), "hub-token") || strings.Contains(err.Error(), "invalid authorization") {
		t.Fatalf("Authenticate leaked a token or server message: %v", err)
	}
}

func TestBadRequestOnAnotherEndpointStaysRetryable(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			http.Error(writer, "bad request", http.StatusBadRequest)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Capabilities = %v, want ErrRemote", err)
	}
	if errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("an unrelated 400 was classified as auth failure: %v", err)
	}
}

func TestAuthenticateRefusesAHubLoginTokenWithoutStoringIt(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/login": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]string{
				"session": "atapi_hub_session", "auth": "hub", "user_id": "user-1",
			})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Username: "alice", Password: "hunter2",
	})
	_, err := remote.Authenticate(context.Background())
	if !errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("Authenticate = %v, want ErrAuthFailed", err)
	}
	if remote.token != "" {
		t.Fatalf("a refused Hub token was stored: %q", remote.token)
	}
	if strings.Contains(err.Error(), "atapi_hub_session") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("Authenticate leaked a credential: %v", err)
	}
}

func TestAuthenticateRejectsAnUnknownLoginAuthKind(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/login": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, map[string]string{
				"session": "minted-session", "auth": "saml",
			})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Username: "alice", Password: "hunter2",
	})
	_, err := remote.Authenticate(context.Background())
	if !errors.Is(err, historysync.ErrAuthFailed) {
		t.Fatalf("Authenticate = %v, want ErrAuthFailed", err)
	}
	if remote.token != "" {
		t.Fatalf("an unsupported auth kind was stored: %q", remote.token)
	}
}

func TestAuthenticateAcceptsALegacyLoginWithoutAnAuthKind(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/login": func(writer http.ResponseWriter, request *http.Request) {
			// An old server omits auth; the pinned client treats that as a
			// legacy CLI session.
			writeJSON(t, writer, http.StatusOK, map[string]string{"session": "minted-session"})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Username: "alice", Password: "hunter2",
	})
	result, err := remote.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if result.Token != "minted-session" || remote.token != "minted-session" {
		t.Fatalf("legacy login token = %q, stored %q", result.Token, remote.token)
	}
}

func TestCapabilitiesNotFoundIsAnIncompatibleServer(t *testing.T) {
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			http.NotFound(writer, request)
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	_, err := remote.Capabilities(context.Background())
	if !errors.Is(err, historysync.ErrIncompatibleServer) {
		t.Fatalf("Capabilities = %v, want ErrIncompatibleServer", err)
	}
	if errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("a missing capabilities endpoint was classified retryable: %v", err)
	}
}

func TestAuthFailureWithATruncatedBodyStillMapsToAuth(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
				"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
					// Claim a large body but send a fragment. The adapter must
					// classify the status before it reads the body.
					writer.Header().Set("Content-Length", "1048576")
					writer.WriteHeader(status)
					_, _ = writer.Write([]byte("{"))
				},
			})
			remote := openTestRemote(t, server.URL, historysync.Config{
				AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
			})
			_, err := remote.Capabilities(context.Background())
			if !errors.Is(err, historysync.ErrAuthExpired) {
				t.Fatalf("Capabilities = %v, want ErrAuthExpired", err)
			}
			if strings.Contains(err.Error(), "session-token") {
				t.Fatalf("error leaked the token: %v", err)
			}
		})
	}
}

func TestSuppliedHTTPClientCannotFollowRedirects(t *testing.T) {
	target, targetRecorder := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/token-capture": func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusOK)
		},
	})
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/capabilities": func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Location", target.URL+"/token-capture")
			writer.WriteHeader(http.StatusTemporaryRedirect)
		},
	})
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return nil },
	}
	factory := NewFactory(Options{HTTPClient: client})
	remote, err := factory.Open(context.Background(), historysync.Config{
		BaseURL: server.URL, AuthMode: atuinmodel.AuthModeSession, Token: "session-token",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = remote.Close() })
	if _, err := remote.Capabilities(context.Background()); !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Capabilities = %v, want ErrRemote", err)
	}
	if len(targetRecorder.all()) != 0 {
		t.Fatal("a caller-supplied client followed a redirect and forwarded the token")
	}
}

func TestDownloadRejectsTheMaximumRecordIndex(t *testing.T) {
	key := testKey(t)
	record := mustWireRecord(t, recordSpec{ID: testRecordID0, Index: math.MaxInt64}, key)
	server, _ := newTestServer(t, historysync.PinnedAtuinVersion, map[string]http.HandlerFunc{
		"/api/v0/record/next": func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, []wire.Record{record})
		},
	})
	remote := openTestRemote(t, server.URL, historysync.Config{
		AuthMode: atuinmodel.AuthModeSession, Token: "session-token", MasterKey: key.Bytes(),
	})
	_, err := remote.Download(context.Background(), historysync.Series{
		SeriesHost: testWriterID, Tag: domainhistory.RecordTagHistory,
	}, 0, 1)
	if !errors.Is(err, historysync.ErrRemote) {
		t.Fatalf("Download = %v, want ErrRemote for an overflowing index", err)
	}
}
