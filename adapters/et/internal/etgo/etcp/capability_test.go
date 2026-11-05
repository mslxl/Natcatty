// SPDX-License-Identifier: Apache-2.0
package etcp_test

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etcp"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/wire"
)

func TestDialReportsServerChallengeCapability(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newHarness(t, etcp.Dialer{})
		if !h.conn.ChallengeCapable() {
			t.Fatal("a v7 server did not report challenge capability")
		}
		h.close()
	})

	synctest.Test(t, func(t *testing.T) {
		srv := etservertest.NewServer(testID, testKey)
		srv.LegacyHandshake(true)
		nw := etservertest.NewNetwork(srv)
		conn, err := (&etcp.Dialer{NetDialer: nw}).Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			nw.Close()
			t.Fatalf("Dial against a legacy server: %v", err)
		}
		if conn.ChallengeCapable() {
			_ = conn.Close()
			nw.Close()
			t.Fatal("a pre-v7 server reported challenge capability")
		}
		if err := conn.Close(); err != nil {
			nw.Close()
			t.Fatalf("Close: %v", err)
		}
		nw.Close()
	})
}

// silentTestConn ignores deadline setters, like the SSH forwarding channel the
// ET adapter uses. Without the connect watchdog, idleConn's deadlines would be
// no-ops and a reconnect handshake could wait forever.
type silentTestConn struct{ net.Conn }

func (silentTestConn) SetDeadline(time.Time) error      { return nil }
func (silentTestConn) SetReadDeadline(time.Time) error  { return nil }
func (silentTestConn) SetWriteDeadline(time.Time) error { return nil }

type silentTestDialer struct{ inner *scripted }

func (d silentTestDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := d.inner.DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return silentTestConn{Conn: conn}, nil
}

// TestReconnectHandshakeIsBounded reproduces the production failure the
// watchdog exists for: the first link works, then the server accepts every
// redial but never answers the connect exchange, and the channel ignores
// deadlines. Each attempt must give up after HandshakeTimeout and redial;
// before the watchdog, the supervisor blocked on the first silent handshake
// forever.
func TestReconnectHandshakeIsBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		silent := make(chan struct{}, 8)
		s := &scripted{handle: func(i int, c *rawServer) {
			if i == 0 {
				// A normal first session that then loses its link.
				c.acceptFrames(0)
				return
			}
			select {
			case silent <- struct{}{}:
			default:
			}
			// Read the connect request, then never answer. The read below
			// returns when the client's watchdog closes the link.
			var req protocol.ConnectRequest
			_ = wire.ReadMessage(c.br, &req)
			var one [1]byte
			_, _ = c.br.Read(one[:])
		}}
		const timeout = 20 * time.Second
		conn, err := (&etcp.Dialer{
			NetDialer:        silentTestDialer{inner: s},
			HandshakeTimeout: timeout,
		}).Dial(t.Context(), testAddr, testID, testKey)
		if err != nil {
			t.Fatalf("Dial: %v", err)
		}
		defer func() {
			_ = conn.Close()
			s.wg.Wait()
		}()

		// Two started silent attempts prove the first one timed out and the
		// supervisor moved on, rather than blocking on it forever.
		for started := 0; started < 2; started++ {
			select {
			case <-silent:
			case <-time.After(time.Hour):
				t.Fatalf("only %d silent reconnect attempts started; the handshake did not time out", started)
			}
		}
	})
}
