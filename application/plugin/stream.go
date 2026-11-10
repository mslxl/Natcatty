package plugin

import (
	"fmt"
	"sync"
)

// StreamBuffer is the host-side half of one credit-based stream. Writers add
// bounded chunks; readers consume them and return credit. It enforces the
// runtime's 64 KiB chunk, initial 256 KiB credit, and 1 MiB buffer ceilings.
//
// A producer that runs out of credit gets ErrWouldBlock and retries after the
// consumer reads and grants more. A chunk larger than the chunk ceiling or a
// write that would exceed the buffer ceiling gets a LimitError.
type StreamBuffer struct {
	mu            sync.Mutex
	chunks        [][]byte
	buffered      int
	credit        int
	initialWindow int
	chunkLimit    int
	bufferLimit   int
	closed        bool
}

// NewStreamBuffer returns a stream with the contract defaults.
func NewStreamBuffer() *StreamBuffer {
	return NewStreamBufferWithLimits(StreamChunkSize, StreamMaxBuffered, StreamInitialWindow)
}

// NewStreamBufferWithLimits returns a stream with explicit ceilings. All
// values must be positive; non-positive values fall back to the contract
// defaults.
func NewStreamBufferWithLimits(chunkLimit, bufferLimit, initialWindow int) *StreamBuffer {
	if chunkLimit <= 0 {
		chunkLimit = StreamChunkSize
	}
	if bufferLimit <= 0 {
		bufferLimit = StreamMaxBuffered
	}
	if initialWindow <= 0 || initialWindow > bufferLimit {
		initialWindow = StreamInitialWindow
		if initialWindow > bufferLimit {
			initialWindow = bufferLimit
		}
	}
	return &StreamBuffer{
		credit:        initialWindow,
		initialWindow: initialWindow,
		chunkLimit:    chunkLimit,
		bufferLimit:   bufferLimit,
	}
}

// Write copies chunk into the stream. Empty chunks are ignored.
func (s *StreamBuffer) Write(chunk []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStreamClosed
	}
	if len(chunk) == 0 {
		return nil
	}
	if len(chunk) > s.chunkLimit {
		return &LimitError{Resource: "stream chunk bytes", Limit: s.chunkLimit}
	}
	if s.buffered+len(chunk) > s.bufferLimit {
		return &LimitError{Resource: "stream buffered bytes", Limit: s.bufferLimit}
	}
	if s.credit < len(chunk) {
		return ErrWouldBlock
	}
	data := append([]byte(nil), chunk...)
	s.chunks = append(s.chunks, data)
	s.buffered += len(data)
	s.credit -= len(data)
	return nil
}

// Read pops the oldest chunk and restores its bytes as credit. It returns
// ErrWouldBlock when the stream is open and empty, and ErrStreamClosed after
// the stream is closed and drained.
func (s *StreamBuffer) Read() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.chunks) == 0 {
		if s.closed {
			return nil, ErrStreamClosed
		}
		return nil, ErrWouldBlock
	}
	chunk := s.chunks[0]
	s.chunks = s.chunks[1:]
	s.buffered -= len(chunk)
	s.credit += len(chunk)
	if s.credit > s.bufferLimit {
		s.credit = s.bufferLimit
	}
	return chunk, nil
}

// Grant adds credit up to the buffer ceiling. It rejects a grant that would
// exceed the ceiling with a LimitError rather than silently growing the
// window.
func (s *StreamBuffer) Grant(n int) error {
	if n < 0 {
		return fmt.Errorf("plugin: cannot grant negative stream credit (%d)", n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.credit+n > s.bufferLimit {
		return &LimitError{Resource: "stream credit bytes", Limit: s.bufferLimit}
	}
	s.credit += n
	return nil
}

// Close marks the stream closed. Buffered chunks remain readable.
func (s *StreamBuffer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Buffered returns the number of buffered bytes.
func (s *StreamBuffer) Buffered() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buffered
}

// Credit returns the remaining send credit in bytes.
func (s *StreamBuffer) Credit() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.credit
}

// Closed reports whether Close was called.
func (s *StreamBuffer) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
