package monitor

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/application/hoststats"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// monitorStatsConnector adapts the monitoring port to the shared Host Stats
// collector. The collector owns the command shape, cache, counters, and
// failure policy; this adapter only translates the transport vocabulary.
type monitorStatsConnector struct {
	connector Connector
}

func (a monitorStatsConnector) Open(ctx context.Context, host domainvault.Host) (hoststats.Connection, error) {
	if a.connector == nil {
		return nil, &hoststats.Error{Reason: hoststats.ReasonInternal,
			Cause: errors.New("monitor: no connector")}
	}
	connection, err := a.connector.Open(ctx, host)
	if err != nil {
		return nil, monitorStatsError(err)
	}
	if connection == nil {
		return nil, &hoststats.Error{Reason: hoststats.ReasonInternal,
			Cause: errors.New("monitor: connector returned no connection")}
	}
	return monitorStatsConnection{connection: connection}, nil
}

type monitorStatsConnection struct {
	connection Connection
}

func (c monitorStatsConnection) Exec(ctx context.Context, command string) (hoststats.ExecResult, error) {
	result, err := c.connection.Exec(ctx, command)
	if err != nil {
		return hoststats.ExecResult{}, monitorStatsError(err)
	}
	return hoststats.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (c monitorStatsConnection) Close() error { return c.connection.Close() }

// SingleChannel forwards the transport's one-channel property, so the
// collector refuses to open an exec channel on a peer that has none to spare.
func (c monitorStatsConnection) SingleChannel() bool {
	peer, ok := c.connection.(interface{ SingleChannel() bool })
	return ok && peer.SingleChannel()
}

func monitorStatsError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return &hoststats.Error{Reason: monitorStatsReason(typed.Reason), Cause: typed.Cause}
	}
	if errors.Is(err, context.Canceled) {
		return &hoststats.Error{Reason: hoststats.ReasonCancelled, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &hoststats.Error{Reason: hoststats.ReasonTimeout, Cause: err}
	}
	return &hoststats.Error{Reason: hoststats.ReasonInternal, Cause: err}
}

func monitorStatsReason(reason Reason) hoststats.Reason {
	switch reason {
	case ReasonHostKey:
		return hoststats.ReasonHostKey
	case ReasonAuth:
		return hoststats.ReasonAuth
	case ReasonNetwork:
		return hoststats.ReasonNetwork
	case ReasonTimeout:
		return hoststats.ReasonTimeout
	case ReasonOS:
		return hoststats.ReasonOS
	case ReasonUnsupported:
		return hoststats.ReasonUnsupported
	case ReasonCancelled:
		return hoststats.ReasonCancelled
	default:
		return hoststats.ReasonInternal
	}
}
