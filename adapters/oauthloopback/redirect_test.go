package oauthloopback_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/oauthloopback"
	appsync "github.com/mslxl/natcatty/application/sync"
)

const testRedirectURI = "netcatty://oauth/callback"

// reversedClientRedirectURI is the host-less redirect Google's mobile OAuth
// clients use: the reversed client id followed by scheme:/path. The OS
// delivers the callback without an authority (no //), so the registry must
// match it on the scheme and path.
const reversedClientScheme = "com.googleusercontent.apps.123-abc.apps.googleusercontent.com"
const reversedClientRedirectURI = reversedClientScheme + ":/oauth2redirect"

func newTestRegistry(t *testing.T, timeout time.Duration) *oauthloopback.Registry {
	t.Helper()
	registry, err := oauthloopback.NewRegistry(testRedirectURI,
		oauthloopback.WithRegistryTimeout(timeout))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func newRegistryFor(t *testing.T, redirectURI string, timeout time.Duration) *oauthloopback.Registry {
	t.Helper()
	registry, err := oauthloopback.NewRegistry(redirectURI,
		oauthloopback.WithRegistryTimeout(timeout))
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func testFlow(redirect oauthloopback.Redirect) oauthloopback.Flow {
	return oauthloopback.Flow{
		Provider:      "Google",
		AuthBase:      "https://accounts.example",
		AuthorizePath: "/authorize",
		Scope:         "drive.appdata",
		ClientID:      "mobile-client",
		Redirect:      redirect,
	}
}

// openAndDeliver records the consent URL and returns the callback the OS would
// hand back, using the state the flow generated.
func openAndDeliver(t *testing.T, redirect oauthloopback.Redirect, callback func(state string) string) func(string) error {
	t.Helper()
	return func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		registry, ok := redirect.(*oauthloopback.Registry)
		if !ok {
			t.Fatalf("redirect is %T, want *oauthloopback.Registry", redirect)
		}
		go registry.Deliver(callback(parsed.Query().Get("state")))
		return nil
	}
}

// TestFlowUsesTheCustomSchemeRedirect proves the shared PKCE flow works
// without a loopback listener: the provider receives the registered redirect
// URI, the returned code is paired with this attempt's verifier, and the
// callback is accepted only after the state matches.
func TestFlowUsesTheCustomSchemeRedirect(t *testing.T) {
	registry := newTestRegistry(t, time.Second)

	var authURL string
	open := openAndDeliver(t, registry, func(state string) string {
		return testRedirectURI + "?code=auth-code&state=" + url.QueryEscape(state)
	})
	wrapped := func(raw string) error {
		authURL = raw
		return open(raw)
	}
	authorization, err := testFlow(registry).Authorize(context.Background(), wrapped)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if authorization.Code != "auth-code" {
		t.Fatalf("code = %q, want auth-code", authorization.Code)
	}
	if authorization.RedirectURI != testRedirectURI {
		t.Fatalf("redirect = %q, want the registered custom scheme", authorization.RedirectURI)
	}
	if len(authorization.Verifier) != 43 {
		t.Fatalf("verifier length = %d, want the PKCE verifier", len(authorization.Verifier))
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != testRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, testRedirectURI)
	}
	if got := parsed.Query().Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q, want S256", got)
	}
	if parsed.Query().Get("state") == "" || parsed.Query().Get("code_challenge") == "" {
		t.Fatal("the consent URL is missing state or the code challenge")
	}
}

func TestFlowRejectsACallbackWithTheWrongState(t *testing.T) {
	registry := newTestRegistry(t, time.Second)
	open := openAndDeliver(t, registry, func(string) string {
		return testRedirectURI + "?code=stolen&state=another-attempt"
	})
	_, err := testFlow(registry).Authorize(context.Background(), open)
	if !errors.Is(err, appsync.ErrAuthorizationState) {
		t.Fatalf("error = %v, want ErrAuthorizationState", err)
	}
}

func TestFlowReportsADeniedConsent(t *testing.T) {
	registry := newTestRegistry(t, time.Second)
	open := openAndDeliver(t, registry, func(state string) string {
		return testRedirectURI + "?error=access_denied&state=" + url.QueryEscape(state)
	})
	_, err := testFlow(registry).Authorize(context.Background(), open)
	if !errors.Is(err, appsync.ErrAuthorizationDenied) {
		t.Fatalf("error = %v, want ErrAuthorizationDenied", err)
	}
}

func TestFlowReportsACallbackWithoutACode(t *testing.T) {
	registry := newTestRegistry(t, time.Second)
	open := openAndDeliver(t, registry, func(state string) string {
		return testRedirectURI + "?state=" + url.QueryEscape(state)
	})
	_, err := testFlow(registry).Authorize(context.Background(), open)
	if !errors.Is(err, appsync.ErrAuthorizationDenied) {
		t.Fatalf("error = %v, want ErrAuthorizationDenied", err)
	}
}

// TestRegistryAcceptsBothCustomSchemeShapes pins the two registrations the
// phone supports: the app's host-bearing scheme and Google's host-less
// reversed-client-id scheme:/path redirect.
func TestRegistryAcceptsBothCustomSchemeShapes(t *testing.T) {
	for _, uri := range []string{testRedirectURI, reversedClientRedirectURI} {
		registry := newRegistryFor(t, uri, time.Second)
		if got := registry.RedirectURI(); got != uri {
			t.Fatalf("RedirectURI() = %q, want %q", got, uri)
		}
	}
}

// TestFlowCompletesWithAHostLessReversedClientRedirect proves a Google mobile
// callback without an authority reaches the shared PKCE flow: the consent URL
// carries the reversed-client-id redirect, and the delivered code is paired
// with this attempt's verifier after the state matches.
func TestFlowCompletesWithAHostLessReversedClientRedirect(t *testing.T) {
	registry := newRegistryFor(t, reversedClientRedirectURI, time.Second)

	var authURL string
	open := openAndDeliver(t, registry, func(state string) string {
		return reversedClientRedirectURI + "?code=reverse-code&state=" + url.QueryEscape(state)
	})
	wrapped := func(raw string) error {
		authURL = raw
		return open(raw)
	}
	authorization, err := testFlow(registry).Authorize(context.Background(), wrapped)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if authorization.Code != "reverse-code" {
		t.Fatalf("code = %q, want reverse-code", authorization.Code)
	}
	if authorization.RedirectURI != reversedClientRedirectURI {
		t.Fatalf("redirect = %q, want the reversed-client-id URI", authorization.RedirectURI)
	}
	if len(authorization.Verifier) != 43 {
		t.Fatalf("verifier length = %d, want the PKCE verifier", len(authorization.Verifier))
	}
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != reversedClientRedirectURI {
		t.Fatalf("redirect_uri = %q, want %q", got, reversedClientRedirectURI)
	}
}

// TestRegistryDoesNotDeliverAHostLessCallbackWithAnotherPath keeps the path
// comparison strict: a URL of the app's scheme with a different path is
// claimed (so it is not treated as another app's deep link) but never handed
// to a waiting authorization.
func TestRegistryDoesNotDeliverAHostLessCallbackWithAnotherPath(t *testing.T) {
	registry := newRegistryFor(t, reversedClientRedirectURI, 30*time.Millisecond)
	other := reversedClientScheme + ":/not-the-redirect"
	if !registry.Deliver(other + "?code=x&state=y") {
		t.Fatal("a URL of the app's scheme must be claimed even for another path")
	}
	if _, err := registry.Wait(context.Background()); !errors.Is(err, appsync.ErrAuthorizationTimeout) {
		t.Fatalf("error = %v, want the other-path callback to be rejected", err)
	}
}

// TestRegistryDoesNotDeliverAnotherScheme keeps a foreign scheme out of the
// OAuth wait so the mobile root can hand ssh:// and other links to the shell.
func TestRegistryDoesNotDeliverAnotherScheme(t *testing.T) {
	registry := newRegistryFor(t, reversedClientRedirectURI, 30*time.Millisecond)
	for _, raw := range []string{
		"netcatty://oauth/callback?code=x&state=y",
		"https://example.test/oauth2redirect?code=x&state=y",
		"ssh://host.example",
	} {
		if registry.Deliver(raw) {
			t.Fatalf("Deliver(%q) claimed a foreign scheme", raw)
		}
	}
	if _, err := registry.Wait(context.Background()); !errors.Is(err, appsync.ErrAuthorizationTimeout) {
		t.Fatalf("error = %v, want a foreign scheme to be rejected", err)
	}
}

func TestRegistryDeliversOnlyItsOwnSchemeAndPath(t *testing.T) {
	registry := newTestRegistry(t, time.Second)
	if registry.Deliver("ssh://host.example") {
		t.Fatal("an ssh link was claimed by the OAuth registry")
	}
	if !registry.Deliver("netcatty://elsewhere/callback?code=x") {
		t.Fatal("the app's own scheme must be claimed even for another path")
	}
	if !registry.Deliver(testRedirectURI + "?code=x&state=y") {
		t.Fatal("the registered redirect was not claimed")
	}
}

// TestRegistryBuffersACallbackDeliveredBeforeWait covers the race between the
// system browser returning at once and the flow reaching Wait.
func TestRegistryBuffersACallbackDeliveredBeforeWait(t *testing.T) {
	registry := newTestRegistry(t, time.Second)
	if !registry.Deliver(testRedirectURI + "?code=early&state=state-1") {
		t.Fatal("Deliver rejected the callback")
	}
	query, err := registry.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if query.Get("code") != "early" {
		t.Fatalf("code = %q, want the buffered callback", query.Get("code"))
	}
}

// TestRegistryTimeoutDoesNotRunWhileSuspended proves the waiting window covers
// switching to the browser and coming back: a callback that arrives well after
// the foreground budget, but while the app was suspended, still completes.
func TestRegistryTimeoutDoesNotRunWhileSuspended(t *testing.T) {
	registry := newTestRegistry(t, 40*time.Millisecond)
	registry.Suspend()

	results := make(chan error, 1)
	go func() {
		_, err := registry.Wait(context.Background())
		results <- err
	}()
	// Long enough that a foreground timer would have expired several times.
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-results:
		t.Fatalf("Wait returned while suspended: %v", err)
	default:
	}
	registry.Deliver(testRedirectURI + "?code=late&state=state-1")
	select {
	case err := <-results:
		if err != nil {
			t.Fatalf("Wait after resume: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not accept the callback after it was delivered")
	}
}

// TestRegistryBeginDiscardsAStaleCallback covers a callback that cold-launched
// the app: it must not be paired with the next attempt's verifier.
func TestRegistryBeginDiscardsAStaleCallback(t *testing.T) {
	registry := newTestRegistry(t, 30*time.Millisecond)
	registry.Deliver(testRedirectURI + "?code=stale&state=old")
	registry.Begin()
	_, err := registry.Wait(context.Background())
	if !errors.Is(err, appsync.ErrAuthorizationTimeout) {
		t.Fatalf("error = %v, want the stale callback to be discarded", err)
	}
}

func TestRegistryTimesOutWithoutACallback(t *testing.T) {
	registry := newTestRegistry(t, 30*time.Millisecond)
	_, err := registry.Wait(context.Background())
	if !errors.Is(err, appsync.ErrAuthorizationTimeout) {
		t.Fatalf("error = %v, want ErrAuthorizationTimeout", err)
	}
}

func TestRegistryCancelReturnsTheContextError(t *testing.T) {
	registry := newTestRegistry(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan error, 1)
	go func() {
		_, err := registry.Wait(ctx)
		results <- err
	}()
	cancel()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after cancel")
	}
}
