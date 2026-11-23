package ssh

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// TestProxyCommandUnavailableStartsNoProcessAndNoDial proves the platform gate
// closes ProxyCommand before any I/O: Dial and DialStream both return
// KindUnsupported with a clear reason, no process starts, and neither a proxy
// nor a direct socket dial happens.
func TestProxyCommandUnavailableStartsNoProcessAndNoDial(t *testing.T) {
	for _, entry := range []string{"Dial", "DialStream"} {
		t.Run(entry, func(t *testing.T) {
			var processStarts, socketDials int
			dialer := NewDialer(
				WithProxyCommandUnavailable(),
				WithProcessStart(func(string) (net.Conn, error) {
					processStarts++
					return nil, errors.New("the ProxyCommand process must not start")
				}),
				WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
					socketDials++
					return nil, errors.New("no socket dial is allowed")
				}),
			)
			req := appconnection.DialRequest{
				Hostname: "target.invalid", Port: 2222, Username: "probe",
				Proxy: &appconnection.Proxy{
					Type: appconnection.ProxyCommand, Command: "probe %h %p",
				},
			}

			var err error
			if entry == "Dial" {
				_, err = dialer.Dial(context.Background(), req)
			} else {
				_, err = dialer.DialStream(context.Background(), req)
			}
			if err == nil {
				t.Fatal("the transport accepted ProxyCommand on a gated platform")
			}
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
				t.Fatalf("error = %v, want KindUnsupported", err)
			}
			if !errors.Is(err, appconnection.ErrProxyCommandUnavailable) {
				t.Fatalf("error = %v, want ErrProxyCommandUnavailable", err)
			}
			if !strings.Contains(err.Error(), "ProxyCommand is unavailable on this platform") {
				t.Fatalf("error = %q, want the platform reason", err)
			}
			if processStarts != 0 || socketDials != 0 {
				t.Fatalf("process starts = %d, socket dials = %d, want zero of each",
					processStarts, socketDials)
			}
		})
	}
}

// TestProxyCommandNetworkConflictWinsOverPlatformGate keeps the existing
// exclusion order: a route selecting both a ProxyCommand and a Network Profile
// reports the conflict rather than the platform gate, and still performs no
// process start and no dial.
func TestProxyCommandNetworkConflictWinsOverPlatformGate(t *testing.T) {
	for _, entry := range []string{"Dial", "DialStream"} {
		t.Run(entry, func(t *testing.T) {
			var processStarts, socketDials int
			dialer := NewDialer(
				WithProxyCommandUnavailable(),
				WithProcessStart(func(string) (net.Conn, error) {
					processStarts++
					return nil, errors.New("the ProxyCommand process must not start")
				}),
				WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
					socketDials++
					return nil, errors.New("no socket dial is allowed")
				}),
			)
			req := appconnection.DialRequest{
				Hostname: "target.invalid", Port: 2222, Username: "probe",
				Proxy: &appconnection.Proxy{
					Type: appconnection.ProxyCommand, Command: "probe",
				},
				Network: &appconnection.NetworkRoute{ProfileID: "synthetic-network"},
			}

			var err error
			if entry == "Dial" {
				_, err = dialer.Dial(context.Background(), req)
			} else {
				_, err = dialer.DialStream(context.Background(), req)
			}
			if !errors.Is(err, appconnection.ErrProxyCommandNetworkConflict) {
				t.Fatalf("error = %v, want the ProxyCommand/Network Profile conflict", err)
			}
			if errors.Is(err, appconnection.ErrProxyCommandUnavailable) {
				t.Fatalf("error = %v, want the conflict to win over the platform gate", err)
			}
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
				t.Fatalf("error = %v, want KindUnsupported", err)
			}
			if processStarts != 0 || socketDials != 0 {
				t.Fatalf("process starts = %d, socket dials = %d, want zero of each",
					processStarts, socketDials)
			}
		})
	}
}
