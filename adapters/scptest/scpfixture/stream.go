package scpfixture

import (
	"bufio"
	"context"
	"io"
	"sync"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// streamHandler serves one OpenExec request. r is the client's stdin, out is
// its stdout, and stderr is its stderr. The return value is the exit status.
type streamHandler func(r *bufio.Reader, out, stderr io.Writer) int

// fixtureStream is one in-process exec channel backed by unbuffered pipes.
type fixtureStream struct {
	clientReader *io.PipeReader
	clientWriter *io.PipeWriter
	serverReader *io.PipeReader
	serverWriter *io.PipeWriter
	stderrReader *io.PipeReader
	stderrWriter *io.PipeWriter

	done      chan struct{}
	code      int
	closeOnce sync.Once
}

func newFixtureStream() *fixtureStream {
	clientReader, serverWriter := io.Pipe()
	serverReader, clientWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()
	return &fixtureStream{
		clientReader: clientReader,
		clientWriter: clientWriter,
		serverReader: serverReader,
		serverWriter: serverWriter,
		stderrReader: stderrReader,
		stderrWriter: stderrWriter,
		done:         make(chan struct{}),
	}
}

var _ appconnection.ExecStream = (*fixtureStream)(nil)

// run starts the handler on its own goroutine. The context aborts the channel
// by closing it, which unblocks any pending read or write.
func (s *fixtureStream) run(ctx context.Context, handler streamHandler) {
	go func() {
		defer close(s.done)
		defer func() { _ = s.stderrWriter.Close() }()
		defer func() { _ = s.serverWriter.Close() }()
		s.code = handler(bufio.NewReader(s.serverReader), s.serverWriter, s.stderrWriter)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()
}

func (s *fixtureStream) Read(p []byte) (int, error)  { return s.clientReader.Read(p) }
func (s *fixtureStream) Write(p []byte) (int, error) { return s.clientWriter.Write(p) }
func (s *fixtureStream) Stderr() io.Reader           { return s.stderrReader }

// CloseWrite sends end-of-input to the handler while its output stays open, as
// the SSH exec channel does for a streaming command like tar.
func (s *fixtureStream) CloseWrite() error { return s.clientWriter.Close() }

// ExitCode waits for the handler to finish, like an SSH session's Wait.
func (s *fixtureStream) ExitCode() (int, error) {
	<-s.done
	return s.code, nil
}

// Close tears down both directions and both stderr ends. It is safe to call
// more than once and from a context watcher.
func (s *fixtureStream) Close() error {
	s.closeOnce.Do(func() {
		_ = s.clientWriter.Close()
		_ = s.serverReader.Close()
		_ = s.serverWriter.CloseWithError(io.ErrClosedPipe)
		_ = s.clientReader.Close()
		_ = s.stderrWriter.CloseWithError(io.ErrClosedPipe)
		_ = s.stderrReader.Close()
	})
	return nil
}
