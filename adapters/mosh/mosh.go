// Package mosh implements the Mosh Session transport: an SSH bootstrap that
// runs the remote mosh-server, the MOSH CONNECT handshake, and an in-process
// client that speaks mosh's UDP protocol through internal/mosh. No local
// mosh-client process or bundled binary is involved.
//
// The SSH bootstrap itself is supplied by the application through the
// Bootstrap port, so saved credentials, MFA, known-host trust, and jump chains
// use the same Connection Manager as an SSH Session.
package mosh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

// DefaultHandshakeTimeout bounds the wait for mosh-server's announcement. The
// SSH login itself may legitimately take longer with MFA, so the application
// may raise it.
const DefaultHandshakeTimeout = 60 * time.Second

// maxBufferedOutput bounds bytes waiting for the terminal. The reader applies
// backpressure once full, so the client is not read faster than the terminal
// drains it.
const maxBufferedOutput = 4 << 20

// Bootstrap opens the SSH channel that runs mosh-server. The application backs
// it with the Connection Manager; tests use an in-process fixture.
type Bootstrap interface {
	OpenBootstrap(ctx context.Context, spec appsession.StartSpec, cfg moshmodel.Config) (io.ReadWriteCloser, error)
}

// configFromEnvironment merges a Session's locale environment into its Mosh
// configuration. The SSH bootstrap command is built from the result.
func configFromEnvironment(spec appsession.StartSpec, environment []string) moshmodel.Config {
	cfg := spec.Mosh.Normalized()
	locales := make(map[string]string)
	for _, name := range []string{
		"LANG", "LANGUAGE", "LC_CTYPE", "LC_NUMERIC", "LC_TIME", "LC_COLLATE",
		"LC_MONETARY", "LC_MESSAGES", "LC_PAPER", "LC_NAME", "LC_ADDRESS",
		"LC_TELEPHONE", "LC_MEASUREMENT", "LC_IDENTIFICATION", "LC_ALL",
	} {
		if value := envValue(environment, name); value != "" {
			locales[name] = value
		}
	}
	if cfg.Lang == "" || cfg.Lang == moshmodel.DefaultLang {
		if lang := envValue(environment, "LC_ALL"); lang != "" {
			cfg.Lang = lang
		} else if lang := envValue(environment, "LANG"); lang != "" {
			cfg.Lang = lang
		}
	}
	cfg.Locales = locales
	if cfg.Cols == 0 {
		cfg.Cols = spec.Cols
	}
	if cfg.Rows == 0 {
		cfg.Rows = spec.Rows
	}
	return cfg
}

// readAnnouncement consumes bootstrap output until the MOSH CONNECT marker
// appears. Visible bytes are forwarded to queue. invalid reports that a marker
// appeared but its port or key was unusable. The error is the underlying read
// or context failure; io.EOF means the channel closed without an announcement.
func readAnnouncement(ctx context.Context, stream io.ReadWriteCloser, queue *outputQueue) (moshmodel.Connect, bool, error) {
	sniffer := moshmodel.NewSniffer()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := stream.Read(buffer)
		if n > 0 {
			visible, connect := sniffer.Feed(buffer[:n])
			if len(visible) > 0 {
				if err := queue.push(visible); err != nil {
					return moshmodel.Connect{}, sniffer.InvalidAnnouncement(), err
				}
			}
			if connect != nil {
				return *connect, sniffer.InvalidAnnouncement(), nil
			}
		}
		if readErr != nil {
			visible, connect := sniffer.Flush()
			if len(visible) > 0 {
				if err := queue.push(visible); err != nil {
					return moshmodel.Connect{}, sniffer.InvalidAnnouncement(), err
				}
			}
			if connect != nil {
				return *connect, sniffer.InvalidAnnouncement(), nil
			}
			if ctx.Err() != nil {
				return moshmodel.Connect{}, sniffer.InvalidAnnouncement(), ctx.Err()
			}
			return moshmodel.Connect{}, sniffer.InvalidAnnouncement(), readErr
		}
	}
}

// mochHandshakeError classifies a bootstrap read that ended before
// mosh-server announced itself.
func mochHandshakeError(cause error) error {
	if cause == nil || errors.Is(cause, io.EOF) {
		return fmt.Errorf("%w: the SSH channel closed before mosh-server announced itself", moshmodel.ErrHandshake)
	}
	return fmt.Errorf("%w: %v", moshmodel.ErrHandshake, cause)
}

// CheckRoute enforces the original restrictions: Mosh bootstraps its own SSH
// session, so a jump chain or proxy cannot be carried into the UDP channel.
func CheckRoute(jumpCount int, proxied bool) error {
	if proxied {
		return moshmodel.ErrProxyUnsupported
	}
	if jumpCount > 0 {
		return moshmodel.ErrJumpUnsupported
	}
	return nil
}

// envValue returns a variable from a KEY=value list.
func envValue(environment []string, name string) string {
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, name+"="); ok {
			return value
		}
	}
	return ""
}

// setEnv replaces a variable, preserving the order and leaving other entries
// untouched. An empty value from the caller is ignored.
func setEnv(environment []string, name, value string) []string {
	if value == "" {
		return environment
	}
	prefix := name + "="
	for index, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			environment[index] = prefix + value
			return environment
		}
	}
	return append(environment, prefix+value)
}

// mergeEnv overlays extra KEY=value entries on a base environment.
func mergeEnv(base, extra []string) []string {
	result := append([]string(nil), base...)
	for _, entry := range extra {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			continue
		}
		result = setEnv(result, name, value)
	}
	return result
}

// outputQueue is the bounded handoff between the bootstrap reader and the
// terminal. Push blocks when full, which backpressures the reader; Read blocks
// when empty.
type outputQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	bytes  int
	max    int
	// total counts every output byte pushed, including final bytes. The
	// terminal renderer uses it to place a remote resize at its exact stream
	// position.
	total uint64
	// sealed reports that final terminal-restoration bytes were pushed. A
	// normal push that was in flight during close must not land after them.
	sealed bool
	closed bool
	err    error
}

func newOutputQueue(max int) *outputQueue {
	queue := &outputQueue{max: max}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *outputQueue) push(chunk []byte) error {
	if len(chunk) == 0 {
		return nil
	}
	copied := bytes.Clone(chunk)
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.bytes >= q.max && !q.closed && !q.sealed {
		q.cond.Wait()
	}
	if q.closed || q.sealed {
		return io.ErrClosedPipe
	}
	q.chunks = append(q.chunks, copied)
	q.bytes += len(copied)
	q.total += uint64(len(copied))
	q.cond.Broadcast()
	return nil
}

// pushedBytes reports how many output bytes the renderer has produced. It is
// the stream offset of the next frame.
func (q *outputQueue) pushedBytes() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.total
}

func (q *outputQueue) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.chunks) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.chunks) == 0 {
		if q.err != nil {
			return 0, q.err
		}
		return 0, io.EOF
	}
	n := copy(p, q.chunks[0])
	if n == len(q.chunks[0]) {
		q.chunks[0] = nil
		q.chunks = q.chunks[1:]
	} else {
		q.chunks[0] = q.chunks[0][n:]
	}
	q.bytes -= n
	q.cond.Broadcast()
	return n, nil
}

func (q *outputQueue) close(err error) {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.err = err
	}
	q.mu.Unlock()
	q.cond.Broadcast()
}

// pushFinal appends terminal-restoration bytes without applying backpressure.
// At shutdown there may be no reader left to drain a full queue, so blocking
// here would deadlock the Session's close.
func (q *outputQueue) pushFinal(chunk []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	// Seal before appending, even for an empty shutdown frame, so an
	// in-flight normal push cannot land after the restoration bytes.
	q.sealed = true
	if len(chunk) == 0 {
		return
	}
	copied := bytes.Clone(chunk)
	q.chunks = append(q.chunks, copied)
	q.bytes += len(copied)
	q.total += uint64(len(copied))
	q.cond.Broadcast()
}
