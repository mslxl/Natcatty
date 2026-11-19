// Package echoconn is the in-memory echo transport the phone terminal
// diagnostic and the rendering tests share.
//
// It is a test transport, not a Session kind: it has no PTY and no local
// shell. What the terminal writes comes back as the output it reads, so the
// whole chain — Go writes through the terminal, the native library parses
// it, the grid and the paint read it back — runs with no network.
package echoconn

import (
	"io"
	"sync"
)

// Conn is an io.ReadWriteCloser that echoes what is written to it.
type Conn struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

// New returns an open echo connection.
func New() *Conn {
	c := &Conn{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// Write queues p as the output the reader returns.
func (c *Conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	c.buf = append(c.buf, p...)
	c.cond.Broadcast()
	return len(p), nil
}

// Read returns what was written, waiting until there is some or the
// connection is closed.
func (c *Conn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) == 0 && !c.closed {
		c.cond.Wait()
	}
	if len(c.buf) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

// Close ends the connection: a blocked Read returns io.EOF and later
// writes fail.
func (c *Conn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.cond.Broadcast()
	}
	return nil
}
