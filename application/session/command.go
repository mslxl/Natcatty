package session

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// Programmatic shell execution. Commands are wrapped with a random marker,
// typed into the Session's existing PTY, and completed when the marker's end
// line arrives with an exit status. This mirrors the original Electron MCP
// PTY bridge: the wrapper keeps shell syntax errors and shell-terminating
// constructs inside a subshell, so the user's login shell survives, and every
// invocation carries an overall deadline and a bounded output window.

const (
	// DefaultCommandTimeout is the per-call shell command budget, matching
	// the original MCP commandTimeoutMs.
	DefaultCommandTimeout = 60 * time.Second
	// DefaultCommandOutputLimit bounds one foreground command's captured
	// output, matching the original 1 MiB foreground PTY capture.
	DefaultCommandOutputLimit = 1 << 20
	// commandPreMarkerLimit bounds output retained before the start marker is
	// found. The echoed wrapper is normally far smaller; the cap keeps a
	// chatty PTY that never answers from growing memory without bound.
	commandPreMarkerLimit = 4 << 20
	// commandInterruptQuiet is how long the terminal must stay quiet after a
	// timeout interrupt before the next command may be typed. PowerShell and
	// other line editors briefly ignore or reflow input while aborting the
	// interrupted pipeline; typing then loses the next wrapper.
	commandInterruptQuiet = 50 * time.Millisecond
	// commandInterruptSettle bounds the wait even if the interrupted process
	// keeps producing output.
	commandInterruptSettle = time.Second
)

var (
	// ErrCommandBusy reports a command already running on this Session.
	ErrCommandBusy = errors.New("session: a command is already running")
	// ErrCommandClosed reports a Session that ended before the command did.
	ErrCommandClosed = errors.New("session: closed")
	// ErrCommandTimeout reports a command that exceeded its overall budget.
	ErrCommandTimeout = errors.New("session: command timed out")
	// ErrCommandCancelled reports a command the caller cancelled.
	ErrCommandCancelled = errors.New("session: command cancelled")
	// ErrCommandEmpty reports an empty command.
	ErrCommandEmpty = errors.New("session: command is empty")
)

// CommandOptions configure one programmatic shell command. Zero values use the
// original MCP defaults.
type CommandOptions struct {
	// Timeout bounds the command overall. Zero uses DefaultCommandTimeout.
	Timeout time.Duration
	// MaxOutputBytes bounds the captured output. Zero uses
	// DefaultCommandOutputLimit. The newest bytes are kept.
	MaxOutputBytes int
	// RawIdle is the idle completion window for serial and network-device
	// Sessions. Zero uses DefaultRawExecIdleTimeout.
	RawIdle time.Duration
	// OnStart runs once when the shell wrapper's start marker arrives. A
	// background job uses it to distinguish a failed startup from a command
	// that is actually running.
	OnStart func()
	// OnProgress receives the current bounded output after each chunk while
	// the command runs. A background job uses it so terminal_poll can return
	// partial output instead of waiting for completion.
	OnProgress func(CommandProgress)
}

// CommandProgress is a running command's bounded output snapshot. Offsets use
// the same monotonic byte accounting as CommandResult.
type CommandProgress struct {
	Stdout           string
	OutputBaseOffset int
	TotalOutputChars int
	Truncated        bool
}

// CommandResult is one finished command.
type CommandResult struct {
	Stdout string
	// ExitCode and HasExitCode mirror the original exec result. Raw network
	// and serial devices expose no exit status, so HasExitCode is false.
	ExitCode    int
	HasExitCode bool
	// Truncated reports that output exceeded MaxOutputBytes. OutputBaseOffset
	// is how many leading bytes were dropped and TotalOutputChars is how many
	// bytes were produced after the start marker.
	Truncated        bool
	OutputBaseOffset int
	TotalOutputChars int
	// NoResponse is set for raw execution that produced no output.
	NoResponse bool
	// OutputMode is "shell" for marker-wrapped execution and "raw" for serial
	// and network-device execution.
	OutputMode string
}

// ShellKind reports the shell family used to wrap commands and select the
// blocklist. "raw" means the Session cannot run a shell.
func (s *Session) ShellKind() string {
	return shellKindForSpec(s.spec)
}

func shellKindForSpec(spec StartSpec) string {
	if spec.NetworkDevice || spec.Kind == KindSerial {
		return "raw"
	}
	if len(spec.Command) > 0 {
		base := strings.ToLower(filepath.Base(spec.Command[0]))
		base = strings.TrimSuffix(base, ".exe")
		switch {
		case base == "pwsh" || base == "powershell":
			return "powershell"
		case base == "cmd":
			return "cmd"
		case base == "fish":
			return "fish"
		default:
			return "posix"
		}
	}
	if spec.Kind == KindLocal {
		if runtime.GOOS == "windows" {
			// localpty prefers pwsh.exe or powershell.exe before cmd.exe.
			return "powershell"
		}
		return "posix"
	}
	// SSH and the other remote shell protocols have no shell hint in this
	// build. Return the unknown kind so the MCP blocklist uses the strict union
	// of every shell group; claiming POSIX here would let a PowerShell remote
	// accept Remove-Item -Recurse -Force.
	return ""
}

// Command runs one command on the Session's existing interactive PTY. Serial
// and network-device Sessions use raw execution without a wrapper; every other
// Session uses the marker wrapper for its shell.
func (s *Session) Command(ctx context.Context, command string, opts CommandOptions) (CommandResult, error) {
	if strings.TrimSpace(command) == "" {
		return CommandResult{}, ErrCommandEmpty
	}
	if s.ShellKind() == "raw" {
		result, err := s.RawExec(ctx, command, RawExecOptions{
			Timeout:        opts.Timeout,
			Idle:           opts.RawIdle,
			MaxOutputBytes: opts.MaxOutputBytes,
		})
		converted := CommandResult{
			Stdout:      result.Stdout,
			ExitCode:    result.ExitCode,
			HasExitCode: result.HasExitCode,
			Truncated:   result.Truncated,
			NoResponse:  result.NoResponse,
			OutputMode:  "raw",
		}
		switch {
		case errors.Is(err, ErrRawExecTimeout):
			err = ErrCommandTimeout
		case errors.Is(err, ErrRawExecCancelled):
			err = ErrCommandCancelled
		case errors.Is(err, ErrRawExecBusy):
			err = ErrCommandBusy
		case errors.Is(err, ErrRawExecClosed):
			err = ErrCommandClosed
		}
		return converted, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if !s.rawExecMu.TryLock() {
		return CommandResult{}, ErrCommandBusy
	}
	defer s.rawExecMu.Unlock()
	if s.Snapshot().Status != StatusRunning {
		return CommandResult{}, ErrCommandClosed
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	limit := opts.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultCommandOutputLimit
	}

	feed := s.subscribeRawOutput()
	defer s.unsubscribeRawOutput(feed)

	marker := newCommandMarker()
	wrapper := buildCommandWrapper(command, s.ShellKind(), marker)
	if err := s.writeRaw([]byte(wrapper)); err != nil {
		return CommandResult{}, ErrCommandClosed
	}

	collector := &commandCollector{
		marker: marker, limit: limit, onStart: opts.OnStart, onProgress: opts.OnProgress,
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.interruptCommand()
			s.settleAfterInterrupt(feed)
			return collector.result(), ErrCommandCancelled
		case <-s.done:
			return collector.result(), ErrCommandClosed
		case <-feed.done:
			return collector.result(), ErrCommandClosed
		case <-timer.C:
			s.interruptCommand()
			s.settleAfterInterrupt(feed)
			return collector.result(), ErrCommandTimeout
		case chunk := <-feed.chunks:
			exitCode, finished := collector.feed(chunk)
			if finished {
				result := collector.result()
				result.ExitCode = exitCode
				result.HasExitCode = true
				result.OutputMode = "shell"
				return result, nil
			}
		}
	}
}

// Command runs one command on a registered Session. It is the application
// service seam the external MCP write executor uses.
func (s *Service) Command(ctx context.Context, id model.SessionID, command string, opts CommandOptions) (CommandResult, error) {
	s.mu.Lock()
	session := s.sessions[id]
	s.mu.Unlock()
	if session == nil {
		return CommandResult{}, ErrCommandClosed
	}
	return session.Command(ctx, command, opts)
}

// ShellKind reports the shell family used to wrap commands for a registered
// Session. ok is false when the Session is not live. "raw" means serial or
// network-device execution, where the blocklist does not apply.
func (s *Service) ShellKind(id model.SessionID) (string, bool) {
	s.mu.Lock()
	session := s.sessions[id]
	s.mu.Unlock()
	if session == nil {
		return "", false
	}
	return session.ShellKind(), true
}

func (s *Session) interruptCommand() {
	select {
	case s.writeCh <- []byte{0x03}:
	default:
	}
}

// settleAfterInterrupt waits for the line editor to finish aborting before the
// caller can start the next command. A shell prompt usually arrives within a
// few milliseconds; the quiet window absorbs the editor's redraw, and the hard
// deadline prevents a process that ignores Ctrl+C from blocking the Session.
// Output consumed here is prompt residue, not command output, so it never
// enters the timed-out result.
func (s *Session) settleAfterInterrupt(feed *rawOutputFeed) {
	quiet := time.NewTimer(commandInterruptQuiet)
	defer quiet.Stop()
	deadline := time.NewTimer(commandInterruptSettle)
	defer deadline.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-feed.done:
			return
		case <-deadline.C:
			return
		case <-feed.chunks:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(commandInterruptQuiet)
		case <-quiet.C:
			return
		}
	}
}

var commandMarkerCounter uint64

func newCommandMarker() string {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		// crypto/rand is always available on supported platforms; the
		// counter still keeps markers unique if it ever fails.
		random = []byte(strconv.FormatUint(atomic.AddUint64(&commandMarkerCounter, 1), 16))
	}
	return "__NCMCP_" + strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + hex.EncodeToString(random) + "__"
}

func buildCommandWrapper(command, shell, marker string) string {
	switch shell {
	case "powershell":
		escaped := strings.ReplaceAll(command, "'", "''")
		return "$" + marker + "=0; $" + marker + "_cmd='" + escaped + "'; & { Write-Output '" +
			marker + "_S'; $LASTEXITCODE=$null; try { Invoke-Expression $" + marker +
			"_cmd; $" + marker + "_rc = if ($LASTEXITCODE -ne $null) { $LASTEXITCODE } elseif ($?) { 0 } else { 1 } } catch { $" +
			marker + "_rc = 1 }; Write-Output \"" + marker + "_E:$" + marker + "_rc\" }\r\n"
	case "cmd":
		escaped := strings.ReplaceAll(strings.ReplaceAll(command, `"`, `""`), "%", "%%")
		return `set "` + marker + `=0" & set "` + marker + `_CMD=` + escaped +
			`" & (echo ` + marker + `_S & set "PAGER=cat" & set "SYSTEMD_PAGER=" & set "GIT_PAGER=cat" & set "LESS=" & call cmd /d /s /c "%` +
			marker + `_CMD%" & call echo ` + marker + `_E:^%errorlevel^%)\r\n`
	case "fish":
		escaped := strings.ReplaceAll(strings.ReplaceAll(command, `\`, `\\`), "'", `\'`)
		return " set " + marker + " 0; function __ncmcp_int --on-signal INT; printf '%s\\n' '" +
			marker + "_E:130'; functions -e __ncmcp_int; end; set -l " + marker + "_cmd '" +
			escaped + "'; begin; printf '\\n%s\\n' '" + marker + "_S'; eval $" + marker +
			"_cmd; set " + marker + "_rc $status; functions -e __ncmcp_int; printf '%s\\n' '" +
			marker + "_E:'$" + marker + "_rc; end\n"
	default:
		escaped := strings.ReplaceAll(command, "'", `'\''`)
		return " " + marker + "=0; { printf '\\n%s_S\\n' '" + marker + "'; ( eval '" +
			escaped + "' ); " + marker + "_rc=$?; printf '%s_E:%s\\n' '" + marker + "' \"$" +
			marker + "_rc\"; }\n"
	}
}

// commandCollector finds the wrapper's own-line start and end markers while
// keeping only the newest limit bytes of command output.
type commandCollector struct {
	marker string
	limit  int

	onStart    func()
	onProgress func(CommandProgress)
	started    bool
	pre        []byte
	output     []byte
	finished   bool
	exitCode   int

	dropped   int
	total     int
	truncated bool
	preClosed bool
}

func (c *commandCollector) feed(chunk []byte) (int, bool) {
	if c.finished {
		return c.exitCode, true
	}
	if !c.started {
		if !c.preClosed {
			c.pre = append(c.pre, chunk...)
		}
		if _, end, ok := findOwnLineMarker(c.pre, c.marker+"_S"); ok {
			rest := append([]byte(nil), c.pre[end:]...)
			c.pre = nil
			c.started = true
			if c.onStart != nil {
				c.onStart()
				c.onStart = nil
			}
			rest = trimLineEnding(rest)
			return c.consumed(rest)
		}
		if len(c.pre) > commandPreMarkerLimit {
			cut := len(c.pre) - commandPreMarkerLimit
			c.pre = append(c.pre[:0], c.pre[cut:]...)
			c.preClosed = true
		}
		return 0, false
	}
	return c.consumed(chunk)
}

// consumed feeds bytes through the collector and reports a running snapshot
// after every chunk that does not finish the command. A background job's poll
// reads this rolling output instead of waiting for the end marker.
func (c *commandCollector) consumed(chunk []byte) (int, bool) {
	exitCode, finished := c.consume(chunk)
	if !finished && c.onProgress != nil {
		c.onProgress(c.progress())
	}
	return exitCode, finished
}

func (c *commandCollector) consume(chunk []byte) (int, bool) {
	c.total += len(chunk)
	c.output = append(c.output, chunk...)
	if index, code, ok := findEndMarker(c.output, c.marker); ok {
		c.total -= len(c.output) - index
		c.output = c.output[:index]
		c.trimOutput()
		c.exitCode = code
		c.finished = true
		return code, true
	}
	c.trimOutput()
	return 0, false
}

// trimOutput keeps the newest limit bytes. The first marker-line bytes are
// retained until the end marker arrives; a truncated result reports how many
// leading bytes were dropped so a poller's offsets stay monotonic.
func (c *commandCollector) trimOutput() {
	if len(c.output) <= c.limit {
		return
	}
	cut := len(c.output) - c.limit
	c.output = append(c.output[:0], c.output[cut:]...)
	c.dropped += cut
	c.truncated = true
}

func (c *commandCollector) result() CommandResult {
	return CommandResult{
		Stdout:           c.stdout(),
		Truncated:        c.truncated,
		OutputBaseOffset: c.dropped,
		TotalOutputChars: c.total,
	}
}

func (c *commandCollector) progress() CommandProgress {
	return CommandProgress{
		Stdout:           c.stdout(),
		OutputBaseOffset: c.dropped,
		TotalOutputChars: c.total,
		Truncated:        c.truncated,
	}
}

func (c *commandCollector) stdout() string {
	stdout := strings.ReplaceAll(stripRawANSI(string(c.output)), "\r", "")
	return strings.TrimSpace(stdout)
}

// findOwnLineMarker finds a start marker that ends its line. The preceding
// bytes are deliberately not constrained: a real ConPTY may emit cursor or
// color escapes before the marker (PowerShell does), so the marker is a line
// of its own on screen without a preceding LF in the byte stream. The echoed
// wrapper always has another character after its marker token, never a line
// ending, so it cannot match.
func findOwnLineMarker(data []byte, startMarker string) (int, int, bool) {
	needle := []byte(startMarker)
	for search := 0; search < len(data); {
		offset := bytes.Index(data[search:], needle)
		if offset < 0 {
			return 0, 0, false
		}
		index := search + offset
		end := index + len(needle)
		// Wait for the line ending when the marker is split across reads.
		if end < len(data) && (data[end] == '\r' || data[end] == '\n') {
			return index, end, true
		}
		search = index + 1
	}
	return 0, 0, false
}

func findEndMarker(data []byte, marker string) (int, int, bool) {
	needle := []byte(marker + "_E:")
	for search := 0; search < len(data); {
		offset := bytes.Index(data[search:], needle)
		if offset < 0 {
			return 0, 0, false
		}
		index := search + offset
		position := index + len(needle)
		digits := position
		for digits < len(data) && data[digits] >= '0' && data[digits] <= '9' {
			digits++
		}
		// The end marker must end its line. A wrapper echo has a shell
		// expression after the colon instead of digits, and a marker split
		// across reads waits for more bytes rather than completing early.
		if digits > position && digits < len(data) &&
			(data[digits] == '\r' || data[digits] == '\n') {
			code, err := strconv.Atoi(string(data[position:digits]))
			if err == nil {
				return index, code, true
			}
		}
		search = index + 1
	}
	return 0, 0, false
}

func trimLineEnding(data []byte) []byte {
	if len(data) > 0 && data[0] == '\r' {
		data = data[1:]
	}
	if len(data) > 0 && data[0] == '\n' {
		data = data[1:]
	}
	return data
}
