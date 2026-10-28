package session

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Raw execution is the original network-device mode: the command is typed
// into the Session's existing interactive PTY, exactly as a user would type
// it, and completion is inferred from the device going quiet. Vendor CLIs such
// as Huawei VRP and Cisco IOS have no POSIX shell to wrap the command in, send
// no completion markers, and expose no exit status, so none of those
// mechanisms is available here.

const (
	// DefaultRawExecIdleTimeout is how long the device must stay silent before
	// a raw command is considered finished. It matches the original 3 s idle
	// window used for serial and network-device execution.
	DefaultRawExecIdleTimeout = 3 * time.Second
	// DefaultRawExecTimeout bounds one raw command overall, matching the
	// original MCP command budget.
	DefaultRawExecTimeout = 60 * time.Second
	// RawExecMaxOutputBytes bounds accumulated output. Once it is reached the
	// idle timer is no longer reset, so a device that keeps emitting debug
	// output cannot hold the command open until the overall timeout.
	RawExecMaxOutputBytes = 512 << 10
	// rawExecEchoPhaseChunks is how many leading chunks get a doubled idle
	// window. A device echoes the command and its prompt first; commands such
	// as ping that stay quiet after the echo must not be cut short.
	rawExecEchoPhaseChunks = 2
	// rawExecFeedDepth bounds chunks queued between the Session reader and a
	// running raw command. A full queue backpressures the transport, which is
	// the same rule the terminal view follows.
	rawExecFeedDepth = 16
)

var (
	// ErrRawExecUnsupported reports a Session that cannot run raw commands.
	// Only network-device SSH Sessions and serial Sessions can.
	ErrRawExecUnsupported = errors.New("session: raw execution is not available")
	// ErrRawExecBusy reports a raw command already running on this Session.
	ErrRawExecBusy = errors.New("session: a command is already running")
	// ErrRawExecClosed reports a Session that ended before the command did.
	ErrRawExecClosed = errors.New("session: closed")
	// ErrRawExecTimeout reports a command that exceeded its overall budget.
	ErrRawExecTimeout = errors.New("session: command timed out")
	// ErrRawExecCancelled reports a command the caller cancelled.
	ErrRawExecCancelled = errors.New("session: command cancelled")
	// ErrRawExecEmptyCommand reports an empty command. Vendor CLIs treat a
	// bare Enter as "repeat the previous command", so it is never sent.
	ErrRawExecEmptyCommand = errors.New("session: command is empty")
)

// RawExecOptions configure one raw command. A zero value uses the original
// defaults.
type RawExecOptions struct {
	// Timeout bounds the command overall. Zero uses DefaultRawExecTimeout.
	Timeout time.Duration
	// Idle is how long the device must stay silent to finish the command.
	// Zero uses DefaultRawExecIdleTimeout.
	Idle time.Duration
	// MaxOutputBytes bounds accumulated output. Zero uses
	// RawExecMaxOutputBytes.
	MaxOutputBytes int
}

// RawExecResult is one finished raw command. Vendor CLIs do not expose exit
// codes, so HasExitCode is always false and ExitCode is always zero; callers
// must not treat the command as failed because of the status.
type RawExecResult struct {
	Stdout string
	// ExitCode and HasExitCode mirror ExecResult's shape. HasExitCode is
	// always false for raw execution.
	ExitCode    int
	HasExitCode bool
	// Truncated reports that the device produced more than MaxOutputBytes.
	Truncated bool
	// NoResponse reports that the device produced no output at all before the
	// no-response fallback fired. The command may still be running or may have
	// completed silently, as mode-changing commands commonly do.
	NoResponse bool
}

// RawExec runs one command on the Session's interactive PTY without shell
// wrapping or completion markers. The command is written as-is followed by a
// carriage return, matching vendor CLIs that expect CR rather than LF.
//
// Completion is inferred: the command finishes when the device has been silent
// for the idle window, or immediately when it produced no output and the
// no-response fallback fired. Output is stripped of ANSI sequences, carriage
// returns, and the echoed first line, as the original did.
//
// The maximum output is capped and an overall timeout always applies. On the
// overall timeout and on cancellation, Ctrl+C is written so the device abandons
// the command; the partial output is returned with the error.
func (s *Session) RawExec(ctx context.Context, command string, opts RawExecOptions) (RawExecResult, error) {
	if !s.supportsRawExec() {
		return RawExecResult{}, ErrRawExecUnsupported
	}
	if strings.TrimSpace(command) == "" {
		return RawExecResult{}, ErrRawExecEmptyCommand
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.rawExecMu.TryLock() {
		return RawExecResult{}, ErrRawExecBusy
	}
	defer s.rawExecMu.Unlock()
	if s.Snapshot().Status != StatusRunning {
		return RawExecResult{}, ErrRawExecClosed
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultRawExecTimeout
	}
	idle := opts.Idle
	if idle <= 0 {
		idle = DefaultRawExecIdleTimeout
	}
	limit := opts.MaxOutputBytes
	if limit <= 0 {
		limit = RawExecMaxOutputBytes
	}

	// Subscribe before writing: the device may answer immediately, and a
	// response that arrived before the subscription would be lost.
	feed := s.subscribeRawOutput()
	defer s.unsubscribeRawOutput(feed)

	if err := s.writeRaw([]byte(command + "\r")); err != nil {
		return RawExecResult{}, err
	}

	overall := time.NewTimer(timeout)
	defer overall.Stop()
	// A device may complete a mode change silently (enable, configure
	// terminal) or have echo disabled. Without a fallback the idle timer would
	// never start, so this resolves well before the overall timeout, exactly as
	// the original did. The cap is one quarter of the overall budget.
	noResponseWindow := idle * 4
	if quarter := timeout / 4; quarter < noResponseWindow {
		noResponseWindow = quarter
	}
	if noResponseWindow <= 0 {
		noResponseWindow = time.Nanosecond
	}
	noResponse := time.NewTimer(noResponseWindow)
	defer noResponse.Stop()

	idleTimer := time.NewTimer(idle)
	if !idleTimer.Stop() {
		<-idleTimer.C
	}
	defer idleTimer.Stop()
	var idleC <-chan time.Time

	var output []byte
	truncated := false
	chunkCount := 0

	finish := func(noOutput, wasTruncated bool, err error) (RawExecResult, error) {
		result := RawExecResult{
			Stdout:      cleanRawOutput(string(output), command),
			ExitCode:    0,
			HasExitCode: false,
			Truncated:   wasTruncated,
			NoResponse:  noOutput,
		}
		return result, err
	}

	noResponseC := noResponse.C

	for {
		select {
		case <-ctx.Done():
			s.writeRaw([]byte{0x03})
			return finish(false, truncated, ErrRawExecCancelled)
		case <-s.done:
			return finish(false, truncated, ErrRawExecClosed)
		case <-feed.done:
			return finish(false, truncated, ErrRawExecClosed)
		case <-overall.C:
			s.writeRaw([]byte{0x03})
			return finish(false, truncated, ErrRawExecTimeout)
		case <-noResponseC:
			noResponseC = nil
			return finish(len(output) == 0, truncated, nil)
		case <-idleC:
			return finish(false, truncated, nil)
		case chunk := <-feed.chunks:
			noResponseC = nil
			if !noResponse.Stop() {
				select {
				case <-noResponse.C:
				default:
				}
			}
			chunkCount++
			remaining := limit - len(output)
			switch {
			case remaining <= 0:
				truncated = true
			case len(chunk) > remaining:
				output = append(output, chunk[:remaining]...)
				truncated = true
			default:
				output = append(output, chunk...)
			}
			// Once the cap is reached the idle timer is armed one last time
			// and never reset again, so a noisy device cannot keep the command
			// open to the overall limit.
			if len(output) < limit {
				duration := idle
				if chunkCount <= rawExecEchoPhaseChunks {
					duration = idle * 2
				}
				if !idleTimer.Stop() {
					select {
					case <-idleTimer.C:
					default:
					}
				}
				idleTimer.Reset(duration)
				idleC = idleTimer.C
			} else if idleC == nil {
				idleTimer.Reset(idle)
				idleC = idleTimer.C
			}
		}
	}
}

// supportsRawExec reports whether this Session's peer can run bare commands.
// Network-device SSH Sessions and serial Sessions can; a POSIX shell uses the
// wrapper-and-marker path instead.
func (s *Session) supportsRawExec() bool {
	return s.spec.NetworkDevice || s.spec.Kind == KindSerial
}

// NetworkDevice reports whether this Session runs in raw network-device mode.
// The MCP write surface uses it to choose RawExec over a marker-based command.
func (s *Session) NetworkDevice() bool { return s.spec.NetworkDevice }

// writeRaw queues bytes for the transport without passing through the tap, so
// a programmatic command is not recorded as typed input and cannot be dropped
// by a transfer gate.
func (s *Session) writeRaw(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	copied := append([]byte(nil), p...)
	select {
	case s.writeCh <- copied:
		return nil
	case <-s.done:
		return ErrRawExecClosed
	case <-s.ctx.Done():
		return ErrRawExecClosed
	}
}

func (s *Session) subscribeRawOutput() *rawOutputFeed {
	feed := &rawOutputFeed{
		chunks: make(chan []byte, rawExecFeedDepth),
		done:   make(chan struct{}),
	}
	s.rawMu.Lock()
	s.rawFeed = feed
	s.rawMu.Unlock()
	// A headless Session may already be paused on output no view ever drained.
	// The command feed is now the consumer, so release the terminal backlog
	// (the replay keeps it) or the first big command would never be read.
	if !s.conn.hasView() {
		s.conn.discardPending()
	}
	return feed
}

func (s *Session) unsubscribeRawOutput(feed *rawOutputFeed) {
	s.rawMu.Lock()
	if s.rawFeed == feed {
		s.rawFeed = nil
	}
	s.rawMu.Unlock()
	feed.close()
}

// publishRawOutput offers one decoded transport read to a running raw command.
// It blocks while the command's bounded queue is full, which backpressures the
// transport; closing the feed releases the reader when the command finishes.
func (s *Session) publishRawOutput(p []byte) {
	s.rawMu.Lock()
	feed := s.rawFeed
	s.rawMu.Unlock()
	if feed == nil || len(p) == 0 {
		return
	}
	feed.push(p)
}

// rawOutputFeed is the one running command's view of transport output. The
// chunks channel is never closed (a writer must not send on a closed channel);
// closing done releases a blocked writer instead.
type rawOutputFeed struct {
	chunks chan []byte
	done   chan struct{}
	once   sync.Once
}

func (f *rawOutputFeed) push(p []byte) {
	select {
	case f.chunks <- append([]byte(nil), p...):
	case <-f.done:
	}
}

func (f *rawOutputFeed) close() { f.once.Do(func() { close(f.done) }) }

var (
	rawANSIOsc = regexp.MustCompile(`\x1b\][^\x07]*?(?:\x07|\x1b\\)`)
	rawANSICsi = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
)

// stripRawANSI removes OSC and CSI sequences, matching the original's
// stripAnsi helper.
func stripRawANSI(input string) string {
	return rawANSICsi.ReplaceAllString(rawANSIOsc.ReplaceAllString(input, ""), "")
}

// cleanRawOutput applies the original's post-processing: strip ANSI, turn CR
// line endings into LF, drop an echoed first line, and trim the ends.
func cleanRawOutput(output, command string) string {
	cleaned := strings.ReplaceAll(stripRawANSI(output), "\r", "")
	lines := strings.Split(cleaned, "\n")
	if len(lines) > 1 {
		first := strings.TrimSpace(lines[0])
		trimmedCommand := strings.TrimSpace(command)
		if trimmedCommand != "" && (first == trimmedCommand || strings.HasSuffix(first, trimmedCommand)) {
			lines = lines[1:]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
