package app

import (
	"context"
	"errors"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

type forwardingTestVault struct{ host domainvault.Host }

func (v forwardingTestVault) Get(context.Context, model.HostID) (domainvault.Host, error) {
	return v.host, nil
}

// TestForwardingRefusesAProtocolWithoutChannelSupport covers the connector
// guard: a Telnet or VNC Host is a real Host, but it has no SSH connection to
// carry a forwarding channel, so the rule must fail with a typed permanent
// error before any dial.
func TestForwardingRefusesAProtocolWithoutChannelSupport(t *testing.T) {
	for _, protocol := range []domainvault.Protocol{domainvault.ProtocolTelnet, domainvault.ProtocolVNC} {
		connector := forwardingConnector{
			vault: forwardingTestVault{host: domainvault.Host{
				ID: "console", Label: "console", Hostname: "console.local",
				Protocol: protocol,
			}},
			connections: appconnection.NewManager(nil, nil, nil),
		}

		_, err := connector.Connect(context.Background(), "console")
		var typed *appforwarding.RuntimeError
		if !errors.As(err, &typed) || typed.Kind != appforwarding.ErrorUnsupported {
			t.Fatalf("%s: Connect = %v, want an unsupported runtime error", protocol, err)
		}
		if !typed.Permanent() {
			t.Fatalf("%s: a protocol without forwarding support must not be retried", protocol)
		}
	}
}

func TestForwardingRuntimeErrorPreservesKindAndFailingHop(t *testing.T) {
	cause := &appconnection.Error{
		Kind:  appconnection.KindAuth,
		Cause: appconnection.ErrAuthentication,
		Hop:   &appconnection.HopInfo{Index: 2, Total: 3, Label: "bastion"},
	}
	var typed *appforwarding.RuntimeError
	if !errors.As(forwardingRuntimeError(cause), &typed) {
		t.Fatalf("mapping did not return a RuntimeError")
	}
	if typed.Kind != appforwarding.ErrorAuth {
		t.Fatalf("kind = %q, want auth", typed.Kind)
	}
	if typed.Hop == nil || typed.Hop.Index != 2 || typed.Hop.Total != 3 || typed.Hop.Label != "bastion" {
		t.Fatalf("hop = %+v, want 2/3 bastion", typed.Hop)
	}
	if !typed.Permanent() {
		t.Fatal("authentication must not be retried")
	}
}

func TestForwardingRuntimeErrorClassifiesBindConflictAsPermanent(t *testing.T) {
	cause := &appconnection.Error{
		Kind:  appconnection.KindNetwork,
		Cause: appconnection.ErrBindConflict,
	}
	var typed *appforwarding.RuntimeError
	if !errors.As(forwardingRuntimeError(cause), &typed) {
		t.Fatalf("mapping did not return a RuntimeError")
	}
	if typed.Kind != appforwarding.ErrorBindConflict {
		t.Fatalf("kind = %q, want bind conflict", typed.Kind)
	}
	if !typed.Permanent() {
		t.Fatal("a bind conflict must not reconnect")
	}
}

func TestForwardingRuntimeErrorReportsAnUnavailableCapability(t *testing.T) {
	err := forwardingRuntimeError(appconnection.ErrForwardingUnavailable)
	var typed *appforwarding.RuntimeError
	if !errors.As(err, &typed) {
		t.Fatalf("mapping did not return a RuntimeError")
	}
	if typed.Kind != appforwarding.ErrorUnsupported {
		t.Fatalf("kind = %q, want unsupported", typed.Kind)
	}
}

func TestForwardingRuntimeErrorKeepsUnknownFailuresInternal(t *testing.T) {
	err := forwardingRuntimeError(errors.New("storage unavailable"))
	var typed *appforwarding.RuntimeError
	if !errors.As(err, &typed) {
		t.Fatalf("mapping did not return a RuntimeError")
	}
	if typed.Kind != appforwarding.ErrorInternal {
		t.Fatalf("kind = %q, want internal", typed.Kind)
	}
}
