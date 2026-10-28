package session

import (
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
	// commandReadinessProbeTimeout bounds one no-op readiness wrapper. Probes
	// and the real command share the caller's overall deadline, so an
	// unresponsive shell cannot add this window on top of CommandOptions.Timeout.
	commandReadinessProbeTimeout = 750 * time.Millisecond
	// commandReadinessProbeAttempts bounds readiness retries so one dropped
	// line does not consume the caller's whole deadline before the command runs.
	commandReadinessProbeAttempts = 3
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
	deadline := time.Now().Add(timeout)

	// A remote shell that is still starting, and any shell just aborted by
	// Ctrl+C, may discard the wrapper line. Probe with a harmless no-op until
	// the shell answers before typing the command the caller asked for. The
	// probes run against the same overall deadline as the command.
	s.ensureShellReady(ctx, feed, deadline)

	remaining := time.Until(deadline)
	if remaining <= 0 {
		s.commandReady.Store(false)
		s.interruptCommand()
		s.settleAfterInterrupt(feed)
		return CommandResult{}, ErrCommandTimeout
	}

	marker := newCommandMarker()
	wrapper := buildCommandWrapper(command, s.ShellKind(), marker)
	if err := s.writeRaw([]byte(wrapper)); err != nil {
		return CommandResult{}, ErrCommandClosed
	}

	collector := &commandCollector{
		marker: marker, limit: limit, onStart: opts.OnStart, onProgress: opts.OnProgress,
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.commandReady.Store(false)
			s.interruptCommand()
			s.settleAfterInterrupt(feed)
			return collector.result(), ErrCommandCancelled
		case <-s.done:
			s.commandReady.Store(false)
			return collector.result(), ErrCommandClosed
		case <-feed.done:
			s.commandReady.Store(false)
			return collector.result(), ErrCommandClosed
		case <-timer.C:
			s.commandReady.Store(false)
			s.interruptCommand()
			s.settleAfterInterrupt(feed)
			return collector.result(), ErrCommandTimeout
		case chunk := <-feed.chunks:
			exitCode, finished := collector.feed(chunk)
			if finished {
				s.commandReady.Store(true)
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

// ensureShellReady types a harmless no-op wrapper until the shell answers it.
// A remote shell that is still starting (an interactive ForceCommand bash -i,
// for example) and a line editor that just processed Ctrl+C can both discard
// the first wrapper; retrying with a no-op makes the caller's real command
// deterministic instead of letting its whole deadline be consumed by a lost
// line. Probes share the command's overall deadline; a shell that never
// answers leaves the caller with ErrCommandTimeout rather than adding the
// probe window to the documented budget.
func (s *Session) ensureShellReady(ctx context.Context, feed *rawOutputFeed, deadline time.Time) {
	if s.commandReady.Load() {
		return
	}
	for attempt := 0; attempt < commandReadinessProbeAttempts; attempt++ {
		if !time.Now().Before(deadline) {
			return
		}
		if s.probeShell(ctx, feed, deadline) {
			s.commandReady.Store(true)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-feed.done:
			return
		default:
		}
		// Do not interrupt between probes: the timeout already sent Ctrl+C,
		// and an extra one would flush the retried line before the shell is
		// stable. A probe that was discarded is simply retried.
	}
}

// probeShell writes one echo wrapper and waits for its end marker. The probe
// output is consumed here; it never enters the command result. Its timer is
// capped by the overall command deadline.
func (s *Session) probeShell(ctx context.Context, feed *rawOutputFeed, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	timerWindow := commandReadinessProbeTimeout
	if remaining < timerWindow {
		timerWindow = remaining
	}
	marker := newCommandMarker()
	// ":" is a no-op builtin in POSIX shells, a label in cmd, and a caught
	// command error in PowerShell; unlike "echo" it never prompts for mandatory
	// input in an interactive PowerShell host.
	wrapper := buildCommandWrapper(":", s.ShellKind(), marker)
	if err := s.writeRaw([]byte(wrapper)); err != nil {
		return false
	}
	collector := &commandCollector{marker: marker, limit: 4096}
	timer := time.NewTimer(timerWindow)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-s.done:
			return false
		case <-feed.done:
			return false
		case <-timer.C:
			return false
		case chunk := <-feed.chunks:
			if _, finished := collector.feed(chunk); finished {
				return true
			}
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
			marker + "_S'; $global:LASTEXITCODE=$null; try { Invoke-Expression $" + marker +
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
	// scanFrom is where the next end-marker search resumes. A terminal can
	// deliver a megabyte of output in tiny chunks; rescanning the whole
	// rolling window on every chunk is quadratic and can outlast the call
	// deadline, so only new bytes (plus an unfinished candidate) are scanned.
	scanFrom int
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
	if index, code, ok, resume := findEndMarkerFrom(c.output, c.marker, c.scanFrom); ok {
		c.total -= len(c.output) - index
		c.output = c.output[:index]
		c.trimOutput()
		c.exitCode = code
		c.finished = true
		return code, true
	} else {
		c.scanFrom = resume
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
	if c.scanFrom > cut {
		c.scanFrom -= cut
	} else {
		c.scanFrom = 0
	}
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

// skipMarkerNoise steps over bytes a terminal may insert inside a printed
// line: ConPTY hard-wraps a line that reaches the margin by emitting CRLF, a
// cursor-position escape, and a re-rendered copy of the cell at the margin, so
// a long marker arrives split and apparently duplicated. It returns whether
// the buffer holds a complete unit at that position; incomplete means more
// output must arrive before the scan can decide.
func skipMarkerNoise(data []byte, index int) (int, bool) {
	if next, gap, incomplete := skipWrapGap(data, index); gap {
		return next, true
	} else if incomplete {
		return index, false
	}
	for index < len(data) {
		switch data[index] {
		case '\r', '\n', '\b', 0x07:
			index++
		case 0x1b:
			end, complete := skipEscapeSequence(data, index)
			if !complete {
				return index, false
			}
			index = end
		default:
			return index, true
		}
	}
	return index, true
}

// skipWrapGap is skipConPTYWrapGap plus the cell ConPTY re-renders at the
// margin. The re-rendered byte repeats the last character before the gap and
// is not new output. incomplete reports that the buffer ends before the
// re-rendered cell is known.
func skipWrapGap(data []byte, index int) (next int, gap, incomplete bool) {
	after, gap, incomplete := skipConPTYWrapGap(data, index)
	if !gap {
		return index, false, incomplete
	}
	if after >= len(data) {
		return index, false, true
	}
	return after + 1, true, false
}

// skipEscapeSequence returns the first index after the escape sequence that
// starts at index. It reports false when the sequence is cut off by the end of
// the buffer.
func skipEscapeSequence(data []byte, index int) (int, bool) {
	if index+1 >= len(data) {
		return index, false
	}
	switch data[index+1] {
	case '[': // CSI: parameters then a final byte in 0x40..0x7e.
		for scan := index + 2; scan < len(data); scan++ {
			if data[scan] >= 0x40 && data[scan] <= 0x7e {
				return scan + 1, true
			}
		}
		return index, false
	case ']': // OSC: terminated by BEL or ST.
		for scan := index + 2; scan < len(data); scan++ {
			switch data[scan] {
			case 0x07:
				return scan + 1, true
			case 0x1b:
				if scan+1 >= len(data) {
					return index, false
				}
				if data[scan+1] == '\\' {
					return scan + 2, true
				}
			}
		}
		return index, false
	default:
		return index + 2, true
	}
}

// matchMarkerCharacters matches token at start, ignoring wrap noise between
// characters. matched reports how many token characters matched; incomplete
// reports that the buffer ended inside a possible match.
func matchMarkerCharacters(data []byte, start int, token string) (end, matched int, incomplete bool) {
	position := start
	for index := 0; index < len(token); index++ {
		if index > 0 {
			next, complete := skipMarkerNoise(data, position)
			if !complete {
				return 0, 0, true
			}
			position = next
		}
		if position >= len(data) {
			return 0, 0, true
		}
		if data[position] != token[index] {
			return position, index, false
		}
		position++
	}
	return position, len(token), false
}

// findOwnLineMarker finds a start marker that ends its line, tolerating the
// CRLF and cursor escapes ConPTY inserts when a narrow terminal wraps a long
// line. The preceding bytes are deliberately not constrained: a real ConPTY
// may emit cursor or color escapes before the marker (PowerShell does), so the
// marker is a line of its own on screen without a preceding LF in the byte
// stream. The echoed wrapper contains the same token inside quotes, so a
// preceding quote and a following non-line-ending character both keep the echo
// from starting the command.
func findOwnLineMarker(data []byte, startMarker string) (int, int, bool) {
	if startMarker == "" {
		return 0, 0, false
	}
	for start := 0; start < len(data); start++ {
		if data[start] != startMarker[0] {
			continue
		}
		if start > 0 && data[start-1] == '\'' {
			continue
		}
		end, matched, incomplete := matchMarkerCharacters(data, start, startMarker)
		if incomplete {
			return 0, 0, false
		}
		if matched < len(startMarker) {
			continue
		}
		// Wait for the line ending when the marker is split across reads; a
		// wrapper echo has another character after the token, never a line
		// ending, so it cannot match.
		if end >= len(data) {
			return 0, 0, false
		}
		if data[end] != '\r' && data[end] != '\n' {
			continue
		}
		return start, end, true
	}
	return 0, 0, false
}

// skipConPTYWrapGap recognizes the bytes ConPTY inserts where a long line
// reaches the right margin: a line ending followed by an optional SGR reset
// and a cursor-position escape. It reports the index after the gap. incomplete
// reports that the buffer ends inside a possible gap.
func skipConPTYWrapGap(data []byte, index int) (next int, gap, incomplete bool) {
	if index >= len(data) {
		return index, false, true
	}
	if data[index] != '\r' && data[index] != '\n' {
		return index, false, false
	}
	scan := index
	for scan < len(data) && (data[scan] == '\r' || data[scan] == '\n' || data[scan] == '\b') {
		scan++
	}
	for {
		if scan >= len(data) {
			return index, false, true
		}
		if data[scan] != 0x1b {
			return index, false, false
		}
		if scan+1 >= len(data) {
			return index, false, true
		}
		if data[scan+1] != '[' {
			return index, false, false
		}
		end := scan + 2
		for end < len(data) && (data[end] < 0x40 || data[end] > 0x7e) {
			end++
		}
		if end >= len(data) {
			return index, false, true
		}
		switch data[end] {
		case 'H': // Cursor position: the wrapped line continues here.
			return end + 1, true, false
		case 'm': // SGR reset or color: keep looking for the cursor position.
			scan = end + 1
		default:
			return index, false, false
		}
	}
}

// findEndMarker finds the exit line. ConPTY may wrap the marker and the digits
// across several visual lines, so both the token and the digits are matched
// with wrap noise between characters. The line must still end after the last
// digit — with a line ending or a terminal escape sequence — which a wrapper
// echo (whose colon is followed by a shell expression rather than digits)
// never does.
// findEndMarkerFrom searches data for the exit line, starting at from. resume
// is where the next call must start when the marker is absent: it is the
// earliest candidate that the current end of the buffer left undecided, or the
// end of data when every candidate was decided. Scanning only from resume is
// what keeps a megabyte of tiny PTY chunks linear instead of quadratic.
func findEndMarkerFrom(data []byte, marker string, from int) (start, code int, ok bool, resume int) {
	token := marker + "_E:"
	if token == "_E:" {
		return 0, 0, false, len(data)
	}
	if from < 0 {
		from = 0
	}
	if from > len(data) {
		from = len(data)
	}
	incompleteAt := -1
	markIncomplete := func(at int) {
		if incompleteAt < 0 {
			incompleteAt = at
		}
	}
	for start := from; start < len(data); start++ {
		if data[start] != token[0] {
			continue
		}
		end, matched, incomplete := matchMarkerCharacters(data, start, token)
		if incomplete {
			markIncomplete(start)
			continue
		}
		if matched < len(token) {
			continue
		}
		position := end
		digits := make([]byte, 0, 4)
		digitsIncomplete := false
		for {
			if position < len(data) && data[position] >= '0' && data[position] <= '9' {
				digits = append(digits, data[position])
				position++
				continue
			}
			if len(digits) > 0 && position < len(data) &&
				(data[position] == '\r' || data[position] == '\n') {
				// A wrapped continuation of the exit line is a gap that leads
				// to another digit. Anything else ends the line here.
				next, gap, _ := skipWrapGap(data, position)
				if gap && next < len(data) && data[next] >= '0' && data[next] <= '9' {
					position = next
					continue
				}
				break
			}
			if len(digits) == 0 {
				// The marker line may wrap between the colon and the code.
				next, gap, gapIncomplete := skipWrapGap(data, position)
				if next == position && gapIncomplete {
					digitsIncomplete = true
					break
				}
				if gap {
					if next < len(data) && data[next] >= '0' && data[next] <= '9' {
						position = next
						continue
					}
					break
				}
			}
			break
		}
		if digitsIncomplete {
			markIncomplete(start)
			continue
		}
		if len(digits) == 0 {
			continue
		}
		if position >= len(data) {
			markIncomplete(start)
			continue
		}
		// A line ending terminates the exit line. So does a terminal control
		// sequence: a shell that re-enables bracketed paste or repositions the
		// prompt may emit escape bytes immediately after the digits and deliver
		// the newline as part of the prompt redraw, so requiring CR/LF here
		// would leave a completed command running until its deadline.
		if data[position] != '\r' && data[position] != '\n' && data[position] != 0x1b {
			continue
		}
		code, err := strconv.Atoi(string(digits))
		if err != nil {
			continue
		}
		return start, code, true, 0
	}
	if incompleteAt >= 0 {
		return 0, 0, false, incompleteAt
	}
	return 0, 0, false, len(data)
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
