// Package et holds the EternalTerminal transport vocabulary used by the
// in-process client: the SSH endpoint the Connection Manager opens, the ET
// server port reached through its forwarding channel, and the typed errors the
// transport and the UI classify without parsing text.
package et

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// DefaultPort is the SSH bootstrap port.
	DefaultPort = 22
	// DefaultETPort is the EternalTerminal server port.
	DefaultETPort = 2022
)

// Errors the transport and the UI classify without parsing text.
var (
	// ErrInvalidConfig reports an unusable ET configuration.
	ErrInvalidConfig = errors.New("et: invalid configuration")
	// ErrBootstrapFailed reports that the in-process client could not start
	// etterminal over the SSH connection, or could not read the IDPASSKEY it
	// prints. It never carries the printed passkey.
	ErrBootstrapFailed = errors.New("et: SSH bootstrap failed")
	// ErrHandshakeFailed reports that the ET connection or its
	// INITIAL_PAYLOAD exchange failed. A server that rejects the session or
	// sends an unreadable stream is a handshake failure.
	ErrHandshakeFailed = errors.New("et: ET handshake failed")
	// ErrUnsupportedServer reports a server that does not speak the client's
	// protocol version.
	ErrUnsupportedServer = errors.New("et: unsupported ET server")
	// ErrReattachUnsupported reports that an attach or reset requested by the
	// client reached a server without the v7 challenge capability. Stock et
	// fails with "Server does not support session reattach; upgrade etserver".
	ErrReattachUnsupported = errors.New("et: server does not support session reattach")
	// ErrRetryLater reports that the server is recovering and asked the
	// client to retry (RETRY_LATER). It is not a fatal session outcome: a
	// reconnect after a backoff can succeed.
	ErrRetryLater = errors.New("et: server is recovering; retry later")
	// ErrSessionDisplaced reports that another client took over the session,
	// or that the server reset its history behind this client's back. A second
	// client owns ET's single session slot; reconnecting would fight it.
	ErrSessionDisplaced = errors.New("et: session was taken over by another client")
	// ErrSessionUnavailable reports a saved ET session that cannot be
	// reattached: the replay window cannot cover the gap, or its saved
	// credentials could not be read. The error never carries the session id or
	// passkey.
	ErrSessionUnavailable = errors.New("et: saved session is unavailable")
)

// ValidationError reports the configuration field that is unusable.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("et: %s %s", e.Field, e.Reason)
}

// Is makes every validation failure match ErrInvalidConfig.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidConfig }

// Config is the non-secret half of one ET Session. Credentials, jump chains,
// proxies, and host-key trust travel through the Connection Manager request
// that the in-process client acquires.
type Config struct {
	Hostname string
	Port     int
	Username string
	// ETPort is the EternalTerminal server port, 2022 by default.
	ETPort int
}

// Normalized fills the defaults a blank form implies.
func (c Config) Normalized() Config {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.Username = strings.TrimSpace(c.Username)
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.ETPort == 0 {
		c.ETPort = DefaultETPort
	}
	return c
}

// Validate checks that a normalized configuration names a usable endpoint.
func (c Config) Validate() error {
	if c.Hostname == "" {
		return &ValidationError{Field: "hostname", Reason: "is required"}
	}
	if c.Port < 1 || c.Port > 65535 {
		return &ValidationError{Field: "port", Reason: "is out of range"}
	}
	if c.ETPort < 1 || c.ETPort > 65535 {
		return &ValidationError{Field: "et port", Reason: "is out of range"}
	}
	return nil
}
