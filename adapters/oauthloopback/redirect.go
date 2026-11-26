// Custom-scheme OAuth callbacks for phones.
package oauthloopback

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	appsync "github.com/mslxl/natcatty/application/sync"
)

// DefaultRegistryTimeout is the foreground budget one mobile authorization
// may spend waiting for its callback. Time while the app is suspended does not
// count: iOS and Android freeze an app while the system browser is open, so a
// wall-clock budget would expire before the user could return.
const DefaultRegistryTimeout = 5 * time.Minute

// ErrCallbackInUse reports a second authorization trying to wait on one
// registry. The phone starts at most one browser sign-in at a time.
var ErrCallbackInUse = errors.New("oauth callback: another authorization is waiting")

// Registry is the phone's Redirect: the OS hands every incoming URL to
// Deliver, and Wait blocks the authorization attempt on the callback that
// matches the registry's fixed redirect URI. It is safe from any goroutine,
// and Delivery may happen before Wait starts (the callback is buffered).
type Registry struct {
	redirectURI string
	scheme      string
	host        string
	path        string
	timeout     time.Duration

	mu sync.Mutex
	// waiting is the attempt currently blocked in Wait, if any.
	waiting chan url.Values
	// pending holds a callback that arrived between open and Wait. The state
	// parameter still has to match, so a stale value only causes one
	// retryable mismatch rather than exchanging with the wrong verifier.
	pending   url.Values
	pendingAt time.Time
	// activeElapsed accumulates the time the app spent in the foreground.
	// Suspension freezes activeStart; Resume resumes it. A wait's budget is
	// measured against the difference from its own baseline.
	activeElapsed time.Duration
	activeStart   time.Time
	suspended     bool
	wake          chan struct{}
}

// RegistryOption customizes a Registry.
type RegistryOption func(*Registry)

// WithRegistryTimeout sets the foreground budget for one callback. A zero or
// negative value uses DefaultRegistryTimeout.
func WithRegistryTimeout(timeout time.Duration) RegistryOption {
	return func(r *Registry) {
		if timeout > 0 {
			r.timeout = timeout
		}
	}
}

// NewRegistry builds a Redirect for redirectURI, such as
// netcatty://oauth/callback. The scheme, host, and path must match the iOS URL
// type and Android intent-filter declared in mygo.json; delivering another URL
// of the same scheme returns true but does not satisfy the wait.
//
// A host-less custom scheme of the form scheme:/path is accepted too. Google's
// mobile OAuth clients use exactly that shape for their reversed-client-id
// redirect, for example
// com.googleusercontent.apps.123-abc.apps.googleusercontent.com:/oauth2redirect,
// and the OS delivers the callback without an authority. For such a
// registration the host is not part of the match: the scheme and path must.
func NewRegistry(redirectURI string, opts ...RegistryOption) (*Registry, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return nil, fmt.Errorf("oauth callback: parse %q: %w", redirectURI, err)
	}
	if parsed.Scheme == "" {
		return nil, fmt.Errorf("oauth callback: %q is not a custom scheme URI", redirectURI)
	}
	path := parsed.EscapedPath()
	if parsed.Host == "" {
		// Accept only the opaque-free scheme:/path form. An opaque URI
		// (scheme:opaque) or a scheme with no path cannot be matched against a
		// delivered redirect.
		if parsed.Opaque != "" || path == "" {
			return nil, fmt.Errorf("oauth callback: %q is not a custom scheme URI with a path", redirectURI)
		}
	}
	if path == "" {
		path = "/"
	}
	r := &Registry{
		redirectURI: redirectURI,
		scheme:      strings.ToLower(parsed.Scheme),
		host:        strings.ToLower(parsed.Host),
		path:        path,
		timeout:     DefaultRegistryTimeout,
		activeStart: time.Now(),
		wake:        make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r, nil
}

// RedirectURI is the fixed redirect_uri sent to the provider.
func (r *Registry) RedirectURI() string { return r.redirectURI }

// Begin discards a callback buffered before this attempt, such as one that
// cold-launched the app after a previous sign-in, so a stale state cannot make
// the first retry fail. Call it before opening the browser.
func (r *Registry) Begin() {
	r.mu.Lock()
	r.pending = nil
	r.mu.Unlock()
}

// Deliver routes one URL from the operating system. It reports whether the URL
// belongs to this app's scheme: true means the caller must not treat it as a
// deep link. Only a URL matching the full redirect URI is handed to a waiting
// authorization. A host-less registration (Google's reversed-client-id shape)
// matches on the scheme and path; a host-bearing registration also requires
// the host.
func (r *Registry) Deliver(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, r.scheme) {
		return false
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	if r.host != "" && !strings.EqualFold(parsed.Host, r.host) {
		return true
	}
	if path != r.path {
		return true
	}
	query := parsed.Query()
	r.mu.Lock()
	if r.waiting != nil {
		select {
		case r.waiting <- query:
		default:
		}
	} else {
		r.pending = query
		r.pendingAt = time.Now()
	}
	r.mu.Unlock()
	return true
}

// Wait blocks until the callback arrives or the foreground budget runs out.
// A context cancellation (the page's Cancel) returns the context error; the
// application turns that into its retryable authorization state.
func (r *Registry) Wait(ctx context.Context) (url.Values, error) {
	r.mu.Lock()
	if r.waiting != nil {
		r.mu.Unlock()
		return nil, ErrCallbackInUse
	}
	if r.pending != nil && time.Since(r.pendingAt) <= r.timeout {
		pending := r.pending
		r.pending = nil
		r.mu.Unlock()
		return pending, nil
	}
	results := make(chan url.Values, 1)
	r.waiting = results
	base := r.activeDurationLocked()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.waiting == results {
			r.waiting = nil
		}
		r.mu.Unlock()
	}()

	for {
		r.mu.Lock()
		spent := r.activeDurationLocked() - base
		suspended := r.suspended
		r.mu.Unlock()
		remaining := r.timeout - spent
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: no callback within %s of foreground time", appsync.ErrAuthorizationTimeout, r.timeout)
		}
		var timeout <-chan time.Time
		var timer *time.Timer
		if !suspended {
			timer = time.NewTimer(remaining)
			timeout = timer.C
		}
		select {
		case query := <-results:
			stopTimer(timer)
			return query, nil
		case <-ctx.Done():
			stopTimer(timer)
			return nil, ctx.Err()
		case <-r.wake:
			stopTimer(timer)
		case <-timeout:
			// The timer may have expired only because the app was frozen
			// after Suspend recorded the foreground span but before this
			// goroutine ran. Re-read the state: if the app is suspended now,
			// or foreground time remains, loop instead of failing.
			r.mu.Lock()
			spent := r.activeDurationLocked() - base
			suspended := r.suspended
			r.mu.Unlock()
			if suspended || r.timeout-spent > 0 {
				continue
			}
			return nil, fmt.Errorf("%w: no callback within %s of foreground time", appsync.ErrAuthorizationTimeout, r.timeout)
		}
	}
}

// Suspend pauses the callback budget while the app is in the background. The
// phone composition root calls it from the lifecycle hook that fires before
// the system browser takes over.
func (r *Registry) Suspend() {
	r.mu.Lock()
	if !r.suspended {
		r.activeElapsed += time.Since(r.activeStart)
		r.suspended = true
		r.signalLocked()
	}
	r.mu.Unlock()
}

// Resume restarts the callback budget when the app returns to the foreground.
func (r *Registry) Resume() {
	r.mu.Lock()
	if r.suspended {
		r.suspended = false
		r.activeStart = time.Now()
		r.signalLocked()
	}
	r.mu.Unlock()
}

func (r *Registry) activeDurationLocked() time.Duration {
	if r.suspended {
		return r.activeElapsed
	}
	return r.activeElapsed + time.Since(r.activeStart)
}

func (r *Registry) signalLocked() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}
