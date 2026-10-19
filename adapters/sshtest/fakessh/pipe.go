package fakessh

import (
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Pipe returns a connected, buffered client/server pair. It is deliberately
// not net.Pipe: SSH peers both write their identification string before
// reading it, which deadlocks an unbuffered pipe. This pair behaves like a
// loopback socket without opening a listener.
func Pipe() (client net.Conn, server net.Conn) {
	clientToServer := newPipeBuffer()
	serverToClient := newPipeBuffer()
	return &pipeEnd{reader: serverToClient, writer: clientToServer},
		&pipeEnd{reader: clientToServer, writer: serverToClient}
}

// pipeBuffer is one unidirectional byte queue with a closed flag.
type pipeBuffer struct {
	mu            sync.Mutex
	cond          *sync.Cond
	buffer        []byte
	closed        bool
	readDeadline  time.Time
	writeDeadline time.Time
}

func newPipeBuffer() *pipeBuffer {
	buffer := &pipeBuffer{}
	buffer.cond = sync.NewCond(&buffer.mu)
	return buffer
}

func (b *pipeBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for len(b.buffer) == 0 && !b.closed {
		if expired(b.readDeadline) {
			return 0, os.ErrDeadlineExceeded
		}
		b.cond.Wait()
	}
	if len(b.buffer) == 0 {
		return 0, io.EOF
	}
	n := copy(p, b.buffer)
	b.buffer = b.buffer[n:]
	return n, nil
}

func (b *pipeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, os.ErrClosed
	}
	if expired(b.writeDeadline) {
		return 0, os.ErrDeadlineExceeded
	}
	b.buffer = append(b.buffer, p...)
	b.cond.Broadcast()
	return len(p), nil
}

func (b *pipeBuffer) SetReadDeadline(deadline time.Time) error {
	b.mu.Lock()
	b.readDeadline = deadline
	b.mu.Unlock()
	b.cond.Broadcast()
	if !deadline.IsZero() {
		time.AfterFunc(time.Until(deadline), b.cond.Broadcast)
	}
	return nil
}

func (b *pipeBuffer) SetWriteDeadline(deadline time.Time) error {
	b.mu.Lock()
	b.writeDeadline = deadline
	b.mu.Unlock()
	b.cond.Broadcast()
	return nil
}

func (b *pipeBuffer) Close() error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cond.Broadcast()
	return nil
}

func expired(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

// pipeEnd is one side of the pair.
type pipeEnd struct {
	reader *pipeBuffer
	writer *pipeBuffer
}

func (e *pipeEnd) Read(p []byte) (int, error)  { return e.reader.Read(p) }
func (e *pipeEnd) Write(p []byte) (int, error) { return e.writer.Write(p) }

// Close closes both directions, so the peer sees EOF and writes fail.
func (e *pipeEnd) Close() error {
	_ = e.reader.Close()
	_ = e.writer.Close()
	return nil
}

func (e *pipeEnd) LocalAddr() net.Addr  { return pipeAddr("pipe") }
func (e *pipeEnd) RemoteAddr() net.Addr { return pipeAddr("pipe") }

func (e *pipeEnd) SetDeadline(deadline time.Time) error {
	_ = e.reader.SetReadDeadline(deadline)
	return e.writer.SetWriteDeadline(deadline)
}

func (e *pipeEnd) SetReadDeadline(deadline time.Time) error {
	return e.reader.SetReadDeadline(deadline)
}

func (e *pipeEnd) SetWriteDeadline(deadline time.Time) error {
	return e.writer.SetWriteDeadline(deadline)
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }
