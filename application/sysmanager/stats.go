package sysmanager

import (
	"context"
	"errors"

	"github.com/mslxl/natcatty/application/hoststats"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// sysmanagerStatsConnector adapts the System Tool's port to the shared Host
// Stats collector. The collector owns the Stats Sample command, parser,
// cache, counters, and failure policy; this adapter only translates the
// transport vocabulary.
type sysmanagerStatsConnector struct {
	connector Connector
}

func (a sysmanagerStatsConnector) Open(ctx context.Context, host domainvault.Host) (hoststats.Connection, error) {
	if a.connector == nil {
		return nil, &hoststats.Error{Reason: hoststats.ReasonInternal,
			Cause: errors.New("sysmanager: no connector")}
	}
	connection, err := a.connector.Open(ctx, host)
	if err != nil {
		return nil, sysmanagerStatsError(err)
	}
	if connection == nil {
		return nil, &hoststats.Error{Reason: hoststats.ReasonInternal,
			Cause: errors.New("sysmanager: connector returned no connection")}
	}
	return sysmanagerStatsConnection{connection: connection}, nil
}

type sysmanagerStatsConnection struct {
	connection Connection
}

func (c sysmanagerStatsConnection) Exec(ctx context.Context, command string) (hoststats.ExecResult, error) {
	result, err := c.connection.Exec(ctx, command)
	if err != nil {
		return hoststats.ExecResult{}, sysmanagerStatsError(err)
	}
	return hoststats.ExecResult{
		Stdout:      result.Stdout,
		Stderr:      result.Stderr,
		ExitCode:    result.ExitCode,
		HasExitCode: result.HasExitCode,
	}, nil
}

func (c sysmanagerStatsConnection) Close() error { return c.connection.Close() }

// SingleChannel forwards the transport's one-channel property, so the
// collector refuses to open an exec channel on a peer that has none to spare.
func (c sysmanagerStatsConnection) SingleChannel() bool {
	peer, ok := c.connection.(interface{ SingleChannel() bool })
	return ok && peer.SingleChannel()
}

func sysmanagerStatsError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return &hoststats.Error{Reason: sysmanagerStatsReason(typed.Reason), Cause: typed.Cause}
	}
	if errors.Is(err, context.Canceled) {
		return &hoststats.Error{Reason: hoststats.ReasonCancelled, Cause: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &hoststats.Error{Reason: hoststats.ReasonTimeout, Cause: err}
	}
	return &hoststats.Error{Reason: hoststats.ReasonInternal, Cause: err}
}

func sysmanagerStatsReason(reason Reason) hoststats.Reason {
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
	case ReasonPermission:
		return hoststats.ReasonPermission
	case ReasonUnsupported:
		return hoststats.ReasonUnsupported
	case ReasonCancelled:
		return hoststats.ReasonCancelled
	case ReasonNoExec:
		return hoststats.ReasonNoExec
	case ReasonNoData:
		return hoststats.ReasonEmpty
	case ReasonMissingCommand:
		return hoststats.ReasonMissingCommand
	default:
		return hoststats.ReasonInternal
	}
}
