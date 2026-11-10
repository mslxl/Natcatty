package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Runtime defaults from the plugin runtime contract. The surfaces a plugin
// sees are bounded by these values; adapters may inject shorter deadlines in
// tests, but a host never raises them.
const (
	// DefaultInitTimeout bounds one natcatty_init call.
	DefaultInitTimeout = 5 * time.Second
	// DefaultCallTimeout bounds one dispatch or poll call.
	DefaultCallTimeout = 2 * time.Second
	// DefaultUnloadTimeout bounds one natcatty_shutdown call.
	DefaultUnloadTimeout = 2 * time.Second
	// DefaultWatchdogGrace is how long the watchdog waits past the call
	// deadline before force-closing the module. It is a backstop for host
	// imports that ignore context cancellation.
	DefaultWatchdogGrace = 500 * time.Millisecond
	// DefaultGlobalUnloadBudget bounds the whole app-quit teardown. A sibling
	// plugin that hangs cannot keep the process alive past this budget.
	DefaultGlobalUnloadBudget = 5 * time.Second
	// MinRestartBackoff is the shortest delay before an automatic restart. A
	// crashing plugin therefore restarts at most once per second, never in a
	// tight loop.
	MinRestartBackoff = time.Second
	// DefaultRestartBackoff is the automatic-restart delay when the host does
	// not configure one.
	DefaultRestartBackoff = time.Second
	// MaxAutomaticRestarts is how many automatic restarts a runtime fault
	// earns before a manual restart is required.
	MaxAutomaticRestarts = 1
	// MaxFaultRecords is the bounded per-fiber fault history.
	MaxFaultRecords = 32

	// MaxLogsPerSecond is the per-fiber log rate limit.
	MaxLogsPerSecond = 200
	// MaxLogLines is the per-fiber log ring size.
	MaxLogLines = 200
	// MaxEffectRegistrations is the per-fiber host-side effect quota.
	MaxEffectRegistrations = 4096
	// MaxHandles is the per-instance host-owned handle quota.
	MaxHandles = 4096
	// MaxQueueDepth is the default host request queue depth.
	MaxQueueDepth = 64
	// MaxStreamsPerPlugin is the concurrent stream quota per plugin.
	MaxStreamsPerPlugin = 16

	// DefaultNetworkConnections is the per-fiber external connection quota.
	DefaultNetworkConnections = 8
	// DefaultNetworkDialTimeout caps one external dial unless the host
	// configures a shorter value.
	DefaultNetworkDialTimeout = 30 * time.Second
	// DefaultNetworkIdleTimeout closes an external connection that has had no
	// traffic for this long.
	DefaultNetworkIdleTimeout = 5 * time.Minute
	// DefaultAuthChallenges is the per-fiber live authentication-flow quota.
	DefaultAuthChallenges = 4
	// DefaultSecretLeases is the per-fiber live vault-lease quota.
	DefaultSecretLeases = 16
	// DefaultSecretLeaseTTL is the default and maximum lifetime of one vault
	// lease.
	DefaultSecretLeaseTTL = 60 * time.Second
	// DefaultSecretValueBytes is the largest guest-deliverable secret one
	// lease may return.
	DefaultSecretValueBytes = 64 << 10
	// DefaultTrustConfirmTimeout bounds one host trust-confirmation prompt.
	// The host user reviews the material, so this is deliberately longer than
	// the guest compute deadline.
	DefaultTrustConfirmTimeout = 2 * time.Minute

	// StreamChunkSize is the largest chunk a stream accepts at once.
	StreamChunkSize = 64 << 10
	// StreamInitialWindow is the credit a stream starts with.
	StreamInitialWindow = 256 << 10
	// StreamMaxBuffered is the per-stream buffered-byte ceiling and the
	// maximum credit a stream may accumulate.
	StreamMaxBuffered = 1 << 20

	// DefaultServiceCallTimeout bounds one cross-plugin call when the method
	// declares the default timeout class and the caller has no shorter
	// deadline.
	DefaultServiceCallTimeout = 5 * time.Second
	// MaxServiceCallTimeout is the hard cross-plugin deadline ceiling. The
	// outer guest-call deadline always wins when it is shorter.
	MaxServiceCallTimeout = 30 * time.Second
	// ServiceStreamIdleClose is the idle period after which a cross-plugin
	// stream closes as unavailable.
	ServiceStreamIdleClose = 30 * time.Second
)

// Limits carries the per-runtime deadlines. The zero value means
// DefaultLimits.
type Limits struct {
	InitTimeout   time.Duration
	CallTimeout   time.Duration
	UnloadTimeout time.Duration
	WatchdogGrace time.Duration
}

// DefaultLimits returns the contract defaults.
func DefaultLimits() Limits {
	return Limits{
		InitTimeout:   DefaultInitTimeout,
		CallTimeout:   DefaultCallTimeout,
		UnloadTimeout: DefaultUnloadTimeout,
		WatchdogGrace: DefaultWatchdogGrace,
	}
}

// Normalize replaces non-positive fields with the contract defaults, so a
// partial Limits value never disables a limit.
func (l Limits) Normalize() Limits {
	defaults := DefaultLimits()
	if l.InitTimeout <= 0 {
		l.InitTimeout = defaults.InitTimeout
	}
	if l.CallTimeout <= 0 {
		l.CallTimeout = defaults.CallTimeout
	}
	if l.UnloadTimeout <= 0 {
		l.UnloadTimeout = defaults.UnloadTimeout
	}
	if l.WatchdogGrace <= 0 {
		l.WatchdogGrace = defaults.WatchdogGrace
	}
	return l
}

// LimitError reports that a host quota was exhausted. It is returned instead
// of growing a log ring, effect stack, handle table, queue, or stream without
// bound.
type LimitError struct {
	// Resource names the bounded resource, for example "effect
	// registrations".
	Resource string
	// Limit is the maximum the host allows.
	Limit int
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("plugin: %s limit of %d exceeded", e.Resource, e.Limit)
}

// DeadlineError reports that a guest call was stopped at the host's
// per-operation deadline. It matches context.DeadlineExceeded.
type DeadlineError struct {
	Operation string
	Limit     time.Duration
	// Hint is an optional operator-facing note. The wasm adapter sets it in
	// interpreter compatibility mode so a deadline can be told apart from a
	// slower execution engine; it never relaxes the limit.
	Hint string
}

func (e *DeadlineError) Error() string {
	message := fmt.Sprintf("plugin %s exceeded its %s deadline", e.Operation, e.Limit)
	if e.Hint != "" {
		message += " (" + e.Hint + ")"
	}
	return message
}

// Unwrap lets callers test a DeadlineError with errors.Is(err,
// context.DeadlineExceeded).
func (e *DeadlineError) Unwrap() error { return context.DeadlineExceeded }

// ErrCallInFlight reports a second guest call attempted while one is already
// running on the same Plugin Instance. There is one dispatcher and one
// in-flight call per instance.
var ErrCallInFlight = errors.New("plugin: another call is already in flight")

// ErrStreamClosed reports a read or write on a closed stream.
var ErrStreamClosed = errors.New("plugin: stream is closed")

// ErrWouldBlock reports that a credit-based stream has no credit or no
// buffered data. The caller retries after the peer grants credit or writes.
var ErrWouldBlock = errors.New("plugin: stream would block")

// IsUnusable reports whether err means the instance can no longer be called:
// a guest trap, a host-import panic, a deadline, or cancellation. The
// container recycles such an instance and marks its fiber FAILED.
func IsUnusable(err error) bool {
	if err == nil {
		return false
	}
	var trap *TrapError
	var deadline *DeadlineError
	if errors.As(err, &trap) || errors.As(err, &deadline) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
