// SPDX-License-Identifier: Apache-2.0
package etcp

import (
	"net"
	"testing"
	"testing/synctest"
	"time"
)

// watchdogTestConn records Close so the test can observe whether the watchdog
// killed the link. Reads and writes otherwise succeed immediately.
type watchdogTestConn struct {
	closed chan struct{}
}

func (c *watchdogTestConn) Read([]byte) (int, error) {
	if c.isClosed() {
		return 0, net.ErrClosed
	}
	return 0, nil
}

func (c *watchdogTestConn) Write(p []byte) (int, error) {
	if c.isClosed() {
		return 0, net.ErrClosed
	}
	return len(p), nil
}

func (c *watchdogTestConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *watchdogTestConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *watchdogTestConn) LocalAddr() net.Addr              { return nil }
func (c *watchdogTestConn) RemoteAddr() net.Addr             { return nil }
func (c *watchdogTestConn) SetDeadline(time.Time) error      { return nil }
func (c *watchdogTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *watchdogTestConn) SetWriteDeadline(time.Time) error { return nil }

// TestHandshakeWatchdogTreatsProgressAsLiveness drives the stale-callback race
// directly: a timer callback that already started cannot be stopped by
// Timer.Reset, so expire must re-check progress and reschedule instead of
// closing a link that just moved data. Without the re-check this test's link
// dies at the simulated callback.
func TestHandshakeWatchdogTreatsProgressAsLiveness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		conn := &watchdogTestConn{closed: make(chan struct{})}
		const timeout = 10 * time.Second
		w := newHandshakeWatchdog(conn, timeout)
		defer w.stop()

		time.Sleep(9 * time.Second)
		if _, err := w.Write([]byte("progress")); err != nil {
			t.Fatalf("write progress: %v", err)
		}

		// The old timer was due one second later; this is the callback it
		// would have run, racing with the progress above.
		w.expire()
		if conn.isClosed() {
			t.Fatal("the watchdog closed a link that had just made progress")
		}

		time.Sleep(timeout)
		synctest.Wait()
		if !conn.isClosed() {
			t.Fatal("the watchdog did not close a link that stayed idle past the timeout")
		}
	})
}
