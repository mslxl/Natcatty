//go:build !windows

package ssh

import (
	"context"
	"errors"
	"net"
	"os"
)

// ErrAgentUnavailable reports that no local SSH agent is reachable.
var ErrAgentUnavailable = errors.New("ssh: agent unavailable")

// AgentDialFunc opens the local SSH agent's transport. Tests replace it with
// an in-memory pipe running agent.ServeAgent.
type AgentDialFunc func(ctx context.Context) (net.Conn, error)

// defaultAgentDial connects to SSH_AUTH_SOCK on Unix.
func defaultAgentDial(ctx context.Context) (net.Conn, error) {
	path := os.Getenv("SSH_AUTH_SOCK")
	if path == "" {
		return nil, ErrAgentUnavailable
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, errors.Join(ErrAgentUnavailable, err)
	}
	return conn, nil
}
