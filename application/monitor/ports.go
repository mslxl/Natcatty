// Package monitor owns the monitoring use case: it opens one Connection per
// demanded Host through a consumer-owned port, runs the metric commands off
// the UI thread, applies the Host-key trust path the connection layer owns,
// and publishes bounded snapshots. It never imports the UI or an adapter.
package monitor

import (
	"context"
	"errors"

	domainmonitor "github.com/mslxl/natcatty/domain/monitor"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// ErrNoConnector reports a Service constructed without a transport port.
var ErrNoConnector = errors.New("monitor: no connector")

// ErrTooManyHosts reports a demand above the 256-Host cap.
var ErrTooManyHosts = errors.New("monitor: more than 256 Hosts")

// Reason is the typed failure code carried across the monitor port. The
// values are the domain's, so a portrait of a failure needs no translation.
type Reason = domainmonitor.Reason

const (
	ReasonNone        = domainmonitor.ReasonNone
	ReasonHostKey     = domainmonitor.ReasonHostKey
	ReasonAuth        = domainmonitor.ReasonAuth
	ReasonNetwork     = domainmonitor.ReasonNetwork
	ReasonTimeout     = domainmonitor.ReasonTimeout
	ReasonUnsupported = domainmonitor.ReasonUnsupported
	ReasonCancelled   = domainmonitor.ReasonCancelled
	ReasonInternal    = domainmonitor.ReasonInternal
	ReasonOS          = domainmonitor.ReasonOS
)

// ExecResult is one finished metric command. HasExitCode reports whether the
// peer sent a status.
type ExecResult struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	HasExitCode bool
}

// Connection is one authenticated Host transport. The application adapter wraps
// a connection Lease. Exec must honor context cancellation.
type Connection interface {
	Exec(ctx context.Context, command string) (ExecResult, error)
	Close() error
}

// Connector opens the transport for a Host. The real adapter reuses the
// shared Connection Manager, so Host-key verification, the trust prompt,
// Saved passwords, jump chains, proxies, and algorithm policy are the same
// ones a Session uses. A Connector must not run a metric command before the
// transport is authenticated and its Host key is trusted.
type Connector interface {
	Open(ctx context.Context, host domainvault.Host) (Connection, error)
}
