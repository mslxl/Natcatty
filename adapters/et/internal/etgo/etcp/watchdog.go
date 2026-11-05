// SPDX-License-Identifier: Apache-2.0
package etcp

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// handshakeWatchdog bounds one connect exchange by progress rather than by
// total time: every successful read or write resets the timer, and if neither
// direction moves for the timeout it closes the link. It exists because the
// SSH forwarding channel used for ET cannot carry net.Conn deadlines (see the
// adapter's noDeadlineConn), so idleConn's deadlines do not bound a reconnect
// handshake. A server that accepts the channel and never answers would
// otherwise block the supervisor forever, because liveness probing starts only
// after the handshake.
type handshakeWatchdog struct {
	net.Conn

	timeout time.Duration

	// lastProgress is a UnixNano stamp updated by every successful read or
	// write. expire re-checks it under mu because a timer callback that has
	// already started cannot be cancelled by Timer.Reset: a callback racing
	// with progress must reschedule instead of closing a live link.
	lastProgress atomic.Int64

	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
	expired bool
}

func newHandshakeWatchdog(conn net.Conn, timeout time.Duration) *handshakeWatchdog {
	w := &handshakeWatchdog{Conn: conn, timeout: timeout}
	w.lastProgress.Store(time.Now().UnixNano())
	w.timer = time.AfterFunc(timeout, w.expire)
	return w
}

func (w *handshakeWatchdog) expire() {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return
	}
	idle := time.Duration(time.Now().UnixNano() - w.lastProgress.Load())
	if idle < w.timeout {
		w.timer.Reset(w.timeout - idle)
		w.mu.Unlock()
		return
	}
	w.stopped = true
	w.expired = true
	w.mu.Unlock()
	// Closing the link unblocks whichever read or write is waiting on it; the
	// caller then sees a failed handshake and redials instead of hanging.
	_ = w.Conn.Close()
}

// touch restarts the idle timer after I/O progress.
func (w *handshakeWatchdog) touch() {
	w.lastProgress.Store(time.Now().UnixNano())
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	w.timer.Reset(w.timeout)
}

func (w *handshakeWatchdog) Read(p []byte) (int, error) {
	n, err := w.Conn.Read(p)
	if n > 0 {
		w.touch()
	}
	return n, err
}

func (w *handshakeWatchdog) Write(p []byte) (int, error) {
	n, err := w.Conn.Write(p)
	if n > 0 {
		w.touch()
	}
	return n, err
}

// stop ends the watchdog after the handshake and reports whether it stayed
// quiet. False means the link was closed as timed out and must not be used.
func (w *handshakeWatchdog) stop() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	w.timer.Stop()
	return !w.expired
}
