// Package oauthloopback is the provider-neutral authorization-code flow shared
// by the browser-authorized cloud-sync adapters. It generates a PKCE proof,
// opens the provider's consent URL through the caller's system-browser opener,
// listens on an ephemeral 127.0.0.1 port, validates the redirect's state, and
// returns the code plus the material the adapter needs to exchange it. Endpoint
// URLs, scopes, and account identity stay in each provider adapter.
//
// No client secret is ever generated or sent: the flow is the installed-app
// public-client shape Google and Microsoft both document.
package oauthloopback

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
)

// CallbackPath is the loopback path the provider redirects to.
const CallbackPath = "/oauth/callback"

// PKCE is one authorization attempt's proof key and anti-forgery state.
type PKCE struct {
	Verifier  string
	Challenge string
	State     string
}

// NewPKCE builds a verifier from 32 random bytes (43 base64url characters),
// its S256 challenge, and a 16-byte state. It reads from random; nil uses the
// process CSPRNG.
func NewPKCE(random io.Reader) (PKCE, error) {
	if random == nil {
		random = rand.Reader
	}
	verifier, err := randomToken(random, 32)
	if err != nil {
		return PKCE{}, fmt.Errorf("oauth loopback: generate the PKCE verifier: %w", err)
	}
	state, err := randomToken(random, 16)
	if err != nil {
		return PKCE{}, fmt.Errorf("oauth loopback: generate the authorization state: %w", err)
	}
	return PKCE{Verifier: verifier, Challenge: challengeFor(verifier), State: state}, nil
}

// challengeFor returns the base64url-encoded SHA-256 of verifier, the S256
// code challenge.
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomToken(random io.Reader, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := io.ReadFull(random, buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// Flow describes one provider's authorization-code surface. Everything a
// provider shares lives here; its authorize path, scope, and extra query
// parameters are supplied by the adapter.
type Flow struct {
	// Provider is the human name used in the loopback page ("Google",
	// "Microsoft"). Empty falls back to "The provider".
	Provider string
	// AuthBase is the provider's authorize origin, without a trailing slash.
	AuthBase string
	// AuthorizePath is appended to AuthBase, for example "/authorize".
	AuthorizePath string
	// Scope is the space-delimited OAuth scope.
	Scope string
	// ClientID is the public installed-app client id.
	ClientID string
	// ExtraQuery carries provider-specific parameters such as prompt=consent.
	ExtraQuery url.Values
	// Timeout bounds the wait for the redirect. The zero value is five
	// minutes.
	Timeout time.Duration
	// Random feeds PKCE generation; nil uses the process CSPRNG.
	Random io.Reader
}

// Authorization is what a completed redirect yields.
type Authorization struct {
	Code        string
	Verifier    string
	RedirectURI string
}

const defaultTimeout = 5 * time.Minute

// Authorize opens the consent URL through open, waits for the loopback
// redirect, validates the state, and returns the authorization code. A
// mismatch, denial, missing code, timeout, or unavailable browser is a typed
// error the application maps onto its localized problem vocabulary.
func (f Flow) Authorize(ctx context.Context, open func(string) error) (Authorization, error) {
	if f.ClientID == "" {
		return Authorization{}, fmt.Errorf("%w: no OAuth client id configured", appsync.ErrNotConfigured)
	}
	if open == nil {
		return Authorization{}, appsync.ErrBrowserUnavailable
	}
	proof, err := NewPKCE(f.Random)
	if err != nil {
		return Authorization{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Authorization{}, fmt.Errorf("%w: start the loopback listener: %v", appsync.ErrNetwork, err)
	}
	redirectURI := "http://" + listener.Addr().String() + CallbackPath
	results := make(chan callbackResult, 1)
	server := &http.Server{Handler: callbackHandler(f.provider(), proof.State, results)}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	defer func() {
		_ = server.Close()
	}()

	authURL := f.authorizationURL(redirectURI, proof)
	if err := open(authURL); err != nil {
		return Authorization{}, fmt.Errorf("%w: %v", appsync.ErrBrowserUnavailable, err)
	}

	timeout := f.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Authorization{}, fmt.Errorf("%w: %v", appsync.ErrAuthorizationTimeout, ctx.Err())
		}
		return Authorization{}, ctx.Err()
	case <-timer.C:
		return Authorization{}, fmt.Errorf("%w: no redirect within %s", appsync.ErrAuthorizationTimeout, timeout)
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return Authorization{}, fmt.Errorf("%w: loopback listener: %v", appsync.ErrNetwork, err)
		}
		return Authorization{}, appsync.ErrNetwork
	case result := <-results:
		if result.err != nil {
			return Authorization{}, result.err
		}
		return Authorization{Code: result.code, Verifier: proof.Verifier, RedirectURI: redirectURI}, nil
	}
}

func (f Flow) provider() string {
	if f.Provider == "" {
		return "The provider"
	}
	return f.Provider
}

func (f Flow) authorizationURL(redirectURI string, proof PKCE) string {
	query := url.Values{}
	query.Set("client_id", f.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", f.Scope)
	query.Set("state", proof.State)
	query.Set("code_challenge", proof.Challenge)
	query.Set("code_challenge_method", "S256")
	for key, values := range f.ExtraQuery {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	return f.AuthBase + f.AuthorizePath + "?" + query.Encode()
}

type callbackResult struct {
	code string
	err  error
}

// callbackHandler answers the loopback redirect. The page is written before
// the result is published so the browser is never cut off by authorization
// completing.
//
// The browser page is plain English rather than a catalog string: it is
// served outside the MyGo UI by a provider-neutral adapter, which cannot reach
// ui/locales, and the app-localized authorization state remains in the
// Settings surface. Ticket 50 shipped these same strings in the Google Drive
// adapter before the shared flow moved here.
func callbackHandler(provider, expectedState string, results chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(CallbackPath, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch {
		case query.Get("state") != expectedState:
			writeCallbackPage(w, http.StatusBadRequest, "Authorization failed", "The authorization state did not match this attempt. Return to Netcatty and try again.")
			publishCallback(results, callbackResult{err: appsync.ErrAuthorizationState})
		case query.Get("error") != "":
			writeCallbackPage(w, http.StatusOK, "Authorization cancelled", provider+" did not grant access. You can close this window and try again from Netcatty.")
			publishCallback(results, callbackResult{err: appsync.ErrAuthorizationDenied})
		case query.Get("code") == "":
			writeCallbackPage(w, http.StatusBadRequest, "Authorization failed", "The redirect carried no authorization code. Return to Netcatty and try again.")
			publishCallback(results, callbackResult{err: fmt.Errorf("%w: the redirect carried no code", appsync.ErrAuthorizationDenied)})
		default:
			writeCallbackPage(w, http.StatusOK, "Netcatty is connected", "Authorization finished. You can close this window and return to Netcatty.")
			publishCallback(results, callbackResult{code: query.Get("code")})
		}
	})
	return mux
}

func publishCallback(results chan<- callbackResult, result callbackResult) {
	select {
	case results <- result:
	default:
	}
}

func writeCallbackPage(w http.ResponseWriter, status int, title, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<!doctype html><html><head><meta charset=\"utf-8\"><title>"+
		title+"</title></head><body style=\"font-family: system-ui, sans-serif; margin: 3rem; color: #1f2328;\"><h1 style=\"font-size: 1.35rem;\">"+
		title+"</h1><p style=\"font-size: 0.95rem; max-width: 30rem;\">"+message+"</p></body></html>")
}
