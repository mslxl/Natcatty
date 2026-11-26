package session

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	domainhistory "github.com/mslxl/natcatty/domain/history"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

// CommandState is the command tracking the Session derives from typed input
// and OSC 133 marks. Text is the submitted command, never expanded arguments.
type CommandState struct {
	Text        string
	Running     bool
	ExitCode    int
	HasExitCode bool
	StartedAt   time.Time
	FinishedAt  time.Time
}

// capturedCommand is one command execution the tap observed. The Session adds
// its identity and scope and turns it into a CommandEvent. Unknown values are
// zero: an empty cwd or shell, a zero finishedAt when the Session ended before
// the shell reported a finish (duration unknown), and hasExitCode false when
// the shell never reported an exit code.
type capturedCommand struct {
	command     string
	cwd         string
	shell       string
	startedAt   time.Time
	finishedAt  time.Time
	exitCode    int
	hasExitCode bool
	captureTier int
}

// pendingCommand is the one execution the tap is still capturing. It starts on
// Enter with typed text, is finalized once, and is then nil until the next
// Enter. A finalized execution's late OSC 133 marks are ignored.
type pendingCommand struct {
	command     string
	cwd         string
	shell       string
	startedAt   time.Time
	captureTier int
	interrupted bool
	// awaitingHook marks a command typed while a hook was planned but its
	// first private sequence or handshake has not arrived yet. The shell's own
	// prompt marks must not finalize it: the private sequence replaces it and
	// its D publishes exactly one Tier 1 row. A skip marker releases it to the
	// ordinary prompt path; if the hook never speaks at all, Session close
	// publishes the tentative once with an unknown finish.
	awaitingHook bool
}

// TransferKind names a detected file transfer protocol.
type TransferKind string

const (
	TransferZMODEM TransferKind = "zmodem"
	TransferYMODEM TransferKind = "ymodem"
)

// TransferEnd classifies how a gated transfer ended.
type TransferEnd string

const (
	TransferEndCompleted TransferEnd = "completed"
	TransferEndCancelled TransferEnd = "cancelled"
)

// TransferState is the current or most recent transfer gate.
type TransferState struct {
	Kind       TransferKind
	Active     bool
	StartedAt  time.Time
	FinishedAt time.Time
	Suppressed int
	End        TransferEnd
}

// ClipboardOperation is the direction of an OSC 52 request.
type ClipboardOperation string

const (
	ClipboardRead  ClipboardOperation = "read"
	ClipboardWrite ClipboardOperation = "write"
)

// ClipboardEvent records an OSC 52 request without retaining its payload:
// clipboard data can be a password, so only the decision and size are kept.
type ClipboardEvent struct {
	Operation ClipboardOperation
	Allowed   bool
	Bytes     int
	At        time.Time
}

// TerminalNotification is one OSC 9, 777, or 99 desktop notification request.
type TerminalNotification struct {
	Title    string
	Body     string
	Protocol string
	At       time.Time
}

// Stats are the per-Session counters the status bar shows. OutputBytes counts
// the bytes the terminal received after filtering; InputBytes counts the bytes
// queued for the transport, including broadcast input.
type Stats struct {
	OutputBytes    int64
	InputBytes     int64
	Commands       int64
	FailedCommands int64
	Bells          int64
	Notifications  int64
	Transfers      int64
}

// tapEffects reports state changes that need publication after a chunk.
type tapEffects struct {
	Changed      bool
	Bell         bool
	Notification *TerminalNotification
	Transfer     *TransferState
	// Completions are commands that finished during this chunk. The Session
	// publishes them only after observe returns and releases the tap lock.
	Completions []capturedCommand
	// PromptRecheck asks the Session for one deferred prompt check after the
	// guard window: a command is still running with output after it started,
	// but its chunk did not settle it, and no further output may arrive to
	// trigger another check. The check only finishes a prompt-like tail.
	PromptRecheck bool
}

// tapState is the immutable per-chunk view the Session copies into Snapshot.
type tapState struct {
	CWD     string
	Title   string
	Command CommandState
	Input   string
	// InputCursor is the cursor's rune offset within Input. It is where the
	// next typed character goes and what an accepted suggestion may replace.
	InputCursor int
	// HookActive reports that the session-scoped Netcatty hook has emitted an
	// authoritative event for this Session, so capture can reach Tier 1.
	HookActive bool
	// BracketedPaste reports that the peer enabled DECSET 2004.
	BracketedPaste bool
	Busy           bool
	LastActivity   time.Time
	Notification   *TerminalNotification
	Clipboard      *ClipboardEvent
	Transfer       TransferState
	Stats          Stats
}

// activityWindow is how long output keeps the activity indicator lit after
// the latest byte. A running command or active transfer keeps it lit longer.
const activityWindow = 2 * time.Second

// promptWindow is how long the prompt fallback holds back after a command
// starts. A prompt already in the tail belongs to the previous command, and
// that command's output can still be in flight when Enter is pressed, so the
// new command must not be ended the instant it starts. A fast command's own
// prompt can also land inside the window, so the hold is bounded: the tap
// reports promptDeferred and resolvePrompt settles the command once the window
// expires, even when no further output arrives.
const promptWindow = 50 * time.Millisecond

// sequenceCarryMax bounds an unterminated OSC or other escape sequence held
// for the rest of it. Past it the bytes are forwarded as they are rather than
// buffered without limit.
const sequenceCarryMax = 1 << 20

// The session hook prefixes its app-owned sequences with these markers:
//
//	OSC 1337;NetcattyHook;<shell>
//	OSC 1337;NetcattyHookSkip[;<shell>]
//	OSC 1337;NetcattyExec;<shell>;<base64url(cwd)>;<base64url(command)>
//
// The prompt handshake marks the hook active, so the tap trusts the private
// preexec sequence instead of finalizing typed input at a native prompt mark.
// The skip marker says the startup script deliberately installed nothing (an
// existing integration, a missing encoder, private fish, or an unsupported
// remote shell), so the tap must stop reserving the first typed command for a
// hook that will never speak. The tap consumes all three sequences before the
// renderer sees them. The two exec payload fields are base64url with or
// without padding.
const (
	netcattyHookPrefix = "NetcattyHook;"
	// netcattyHookSkipPrefix has no trailing semicolon: the shell field is
	// optional, so the remote wrapper can mark an unsupported shell whose name
	// is not a safe token.
	netcattyHookSkipPrefix = "NetcattyHookSkip"
	netcattyExecPrefix     = "NetcattyExec;"
	// hookShellMax and hookValueMax bound the decoded fields. A command longer
	// than the history limit is truncated, matching the domain's write path; a
	// hostile sequence cannot make the tap retain an unbounded string.
	hookShellMax = 32
	hookValueMax = 4096
)

// Notification policy, matching the original.
const (
	oscNotificationFallbackTitle = "Netcatty"
	oscNotificationTitleMax      = 120
	oscNotificationBodyMax       = 500
	osc99PendingTTL              = 10 * time.Second
	osc99PendingMax              = 16
	oscNotificationWindow        = 10 * time.Second
	oscNotificationMax           = 4
	oscNotificationMinGap        = 400 * time.Millisecond
)

// ZMODEM starts with the hex header prefix ** CAN B. YMODEM starts with a
// 128- or 1024-byte block header whose block 0 complement is FF.
var (
	zmodemStart  = []byte{'*', '*', 0x18, 'B'}
	ymodemStarts = [][]byte{
		{0x01, 0x00, 0xff},
		{0x02, 0x00, 0xff},
	}
)

// terminalTap is the streaming parser between a transport and the terminal.
// It strips protocol payloads, enforces OSC 52, and derives the command,
// activity, notification, clipboard, and transfer state the UI shows.
type terminalTap struct {
	mu     sync.Mutex
	policy domainterminal.ClipboardAccess

	buffer []byte
	tail   []byte

	cwd     string
	title   string
	command CommandState
	pending *pendingCommand
	// nativeSeen reports that the shell emitted OSC 133 or OSC 7. Without it,
	// typed input plus prompt detection is Tier 3; with it, capture is Tier 2.
	nativeSeen bool
	// hookActive reports that the session-scoped Netcatty hook emitted its
	// private sequence. It is not a tier by itself: a command is Tier 1 only
	// when the private sequence replaced or started it.
	hookActive bool
	// hookExpected reports that the Session planned and injected a hook, so
	// its first handshake or private sequence is still to come. Until one
	// arrives, a typed Enter is tentative: the shell's native prompt marks
	// belong to the pre-hook prompt and must not finalize the command. A
	// NetcattyHookSkip marker also ends the wait, releasing the command to the
	// ordinary Tier 2/3 fallback.
	hookExpected bool
	// hookSeen reports that the planned hook has spoken at least once. A
	// nested shell detected later clears hookActive but not hookSeen, so its
	// inner commands return to the ordinary Tier 2/3 capture.
	hookSeen bool
	// nestedShell names an opaque nested shell (nu or xonsh) that took over
	// the terminal from a hook-owned command. Its native OSC marks belong to
	// the nested shell, not the Session shell, so they are not used to raise
	// the inner commands above the typed fallback. The outer hook's next
	// handshake clears it and restores Tier 1.
	nestedShell string
	inputBuffer string
	// inputCursor is the cursor's position in inputBuffer, counted in runes.
	// The tap tracks the line editor's cursor keys, home/end, kill-line, and
	// insertions at the cursor so an accepted suggestion can replace only the
	// text left of the cursor and leave the rest of the line untouched.
	// Without this the tap would only know the line's bytes, not where the
	// user is editing.
	inputCursor  int
	inputPending []byte
	inputEscape  inputState
	// inputLost reports that the line editor changed the buffer in a way the
	// tap cannot mirror: history recall (Up/Down), reverse search (Ctrl+R),
	// word kills and yanks (Ctrl+W/Y), or a transpose (Ctrl+T) replace or
	// reorder text the tap never sees. The tracked line is then a hybrid of
	// what the user typed and what the editor recalled, and recording it at
	// Enter writes a command that never ran — the garbled rows autocomplete
	// later offers. The capture is discarded instead: no row is better than a
	// wrong row, because the hook and the shell's own native marks still
	// capture what they can prove. The next Enter or Ctrl+C re-arms the tap.
	inputLost bool
	// submittedInput reads the logical input line the shell actually shows on
	// the cursor's prompt rows, prompt chrome included — the screen's truth at
	// the moment Enter is pressed. It is nil when no live view is attached, and
	// it answers ok=false when there is no trustworthy line (no shell
	// integration, a program running, the alternate screen). The tap consults
	// it to reconcile the keystroke mirror against the rendered line: after a
	// history recall or reverse search the mirror is empty or lost but the
	// rendered line is the command about to run. It is invoked synchronously
	// from the input path, so it must only read the screen, never write input.
	// Guarded by mu.
	submittedInput func() (string, int, bool)
	// promptPrefix is the prompt chrome the tap captured from the screen: the
	// text up to the cursor at the first keystroke of a fresh line, the one
	// moment echo is provably absent because the keystroke has not even reached
	// the shell. It is the natcatty answer to the original's cached
	// lastPromptText — read from the screen, never guessed from prompt shapes.
	// The recall fallback subtracts it from the rendered line to split the
	// input from the chrome. Empty when never captured, in which case a recall
	// capture degrades to the safe discard. Guarded by mu.
	promptPrefix string
	// inputPristine reports that the tracked line has held nothing since the
	// last fresh-line reset (Enter or Ctrl+C): no printable byte has joined
	// the mirror yet. It is the gate for the prompt capture — only then is the
	// screen provably bare of echo. A line the user typed into and erased back
	// to empty is not pristine: the echo of the erasure can still be in flight
	// on a slow link, so the screen may show a half-erased line, and capturing
	// that residue as prompt chrome would strip real command text later.
	// Guarded by mu.
	inputPristine bool
	// completionArmed reports that a Tab keystroke on this line may have grown
	// it invisibly: the completion key is neither mirrored nor invalidating,
	// so the mirror can be a strict fragment of the rendered line. The
	// fragment rule at Enter applies only while armed — a rendered line that
	// outgrows the mirror without a completion keystroke is the erase echo of
	// a longer line still in flight, and the mirror must keep it. Cleared at
	// every fresh-line reset. Guarded by mu.
	completionArmed bool
	// inputCSIParams holds the parameter bytes of the CSI sequence being
	// parsed, so the tap can recognize the bracketed-paste markers.
	inputCSIParams []byte
	// inputPaste reports that the bytes being typed are inside a bracketed
	// paste. The shell inserts them into its line editor literally, so the
	// tap must not treat their newlines as Enter.
	inputPaste bool
	// bracketedPaste reports that the peer last enabled DECSET 2004 and has
	// not disabled it since. A compose submission wraps multiline text in the
	// paste markers only while this is true.
	bracketedPaste bool
	lastOutput     time.Time
	// outputSinceStart marks that text output arrived after the current
	// command started. The prompt fallback must ignore the previous command's
	// prompt still sitting in the tail.
	outputSinceStart bool
	notification     *TerminalNotification
	clipboard        *ClipboardEvent
	transfer         TransferState
	stats            Stats
	bellPending      bool

	osc99              osc99Assembler
	notificationStamps []time.Time
}

type inputState int

const (
	inputPlain inputState = iota
	inputEscape
	inputCSI
	inputOSC
	inputOSCExit
)

type osc99Pending struct {
	title     string
	body      string
	updatedAt time.Time
}

func newTerminalTap(policy domainterminal.ClipboardAccess) *terminalTap {
	return &terminalTap{
		policy: policy,
		osc99:  osc99Assembler{pending: make(map[string]*osc99Pending)},
		stats:  Stats{},
		// The first line of a session is as fresh as one after Enter: nothing
		// has been typed into it, so its first keystroke is echo-free.
		inputPristine: true,
	}
}

func (t *terminalTap) setClipboardPolicy(policy domainterminal.ClipboardAccess) {
	t.mu.Lock()
	t.policy = policy
	t.mu.Unlock()
}

// setSubmittedInputReader registers the reader the tap consults at Enter, or
// clears it with nil. The reader is the live view's window onto the screen; it
// is replaced when a view attaches and cleared when it detaches, so a
// hibernated view never answers for its replacement.
func (t *terminalTap) setSubmittedInputReader(reader func() (string, int, bool)) {
	t.mu.Lock()
	t.submittedInput = reader
	t.mu.Unlock()
}

// withdrawCaptureHook releases a planned hook that the transport did not
// install. The shell never receives the startup files, so its own prompt
// marks may finalize a typed capture again. Clearing hookSeen also means an
// unrelated late skip cannot change state later.
func (t *terminalTap) withdrawCaptureHook() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.hookExpected = false
	t.hookActive = false
	t.hookSeen = false
	t.nestedShell = ""
	if t.pending != nil {
		t.pending.awaitingHook = false
	}
}

// observe consumes transport output and returns the bytes the terminal may
// render. OSC 52 requests that policy rejects, program notifications, and
// gated file-transfer payloads are removed here, before any renderer or log
// sees them.
func (t *terminalTap) observe(data []byte, now time.Time) ([]byte, tapEffects) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buffer = append(t.buffer, data...)
	output := make([]byte, 0, len(data))
	var effects tapEffects

	for len(t.buffer) > 0 {
		if t.transfer.Active {
			marker := transferEndMarker(t.transfer.Kind)
			index := bytes.Index(t.buffer, marker)
			if index < 0 {
				keep := len(marker) - 1
				if len(t.buffer) > keep {
					suppressed := len(t.buffer) - keep
					t.transfer.Suppressed += suppressed
					copy(t.buffer, t.buffer[suppressed:])
					t.buffer = t.buffer[:keep]
					effects.Changed = true
				}
				break
			}
			t.transfer.Suppressed += index
			t.buffer = t.buffer[index+len(marker):]
			t.transfer.Active = false
			t.transfer.FinishedAt = now
			t.transfer.End = TransferEndCompleted
			copyState := t.transfer
			effects.Transfer = &copyState
			effects.Changed = true
			continue
		}

		index := nextTapSpecial(t.buffer)
		if index < 0 {
			output = append(output, t.buffer...)
			t.noteOutput(t.buffer, now)
			t.buffer = nil
			break
		}
		if index > 0 {
			output = append(output, t.buffer[:index]...)
			t.noteOutput(t.buffer[:index], now)
			t.buffer = t.buffer[index:]
			continue
		}

		switch t.buffer[0] {
		case 0x1b:
			if len(t.buffer) < 2 {
				goto done
			}
			if t.buffer[1] == ']' {
				contentEnd, totalEnd, complete := findOSC(t.buffer)
				if !complete {
					t.carryIncomplete(now, &output)
					goto done
				}
				forward, effect := t.handleOSC(t.buffer[2:contentEnd], now)
				if forward {
					output = append(output, t.buffer[:totalEnd]...)
				}
				if effect.Changed {
					effects.Changed = true
				}
				if effect.Notification != nil {
					effects.Notification = effect.Notification
					effects.Changed = true
				}
				if len(effect.Completions) > 0 {
					effects.Completions = append(effects.Completions, effect.Completions...)
				}
				t.buffer = t.buffer[totalEnd:]
				continue
			}
			// Any other escape sequence is one unit of output. Forwarding its
			// ESC alone and treating the rest as text left CSI parameter bytes
			// such as "?25h" in the tail as visible output, where they hid a
			// prompt that followed a command's output.
			end, complete := escapeEnd(t.buffer)
			if !complete {
				t.carryIncomplete(now, &output)
				goto done
			}
			if enabled, ok := bracketedPasteMode(t.buffer[:end]); ok && enabled != t.bracketedPaste {
				t.bracketedPaste = enabled
				effects.Changed = true
			}
			output = append(output, t.buffer[:end]...)
			t.buffer = t.buffer[end:]
		case '*':
			if bytes.HasPrefix(t.buffer, zmodemStart) {
				t.startTransfer(TransferZMODEM, len(zmodemStart), now)
				t.buffer = t.buffer[len(zmodemStart):]
				copyState := t.transfer
				effects.Transfer = &copyState
				effects.Changed = true
				continue
			}
			if isPartialPrefix(t.buffer, zmodemStart) {
				goto done
			}
			output = append(output, t.buffer[0])
			t.buffer = t.buffer[1:]
		default:
			started := false
			for _, marker := range ymodemStarts {
				if bytes.HasPrefix(t.buffer, marker) {
					t.startTransfer(TransferYMODEM, len(marker), now)
					t.buffer = t.buffer[len(marker):]
					copyState := t.transfer
					effects.Transfer = &copyState
					effects.Changed = true
					started = true
					break
				}
				if isPartialPrefix(t.buffer, marker) {
					goto done
				}
			}
			if started {
				continue
			}
			output = append(output, t.buffer[0])
			t.buffer = t.buffer[1:]
		}
	}

done:
	if result, completion := t.checkPrompt(now); result == promptFinished {
		effects.Changed = true
		if completion != nil {
			effects.Completions = append(effects.Completions, *completion)
		}
	}
	if t.pending != nil && t.outputSinceStart {
		// The fallback did not settle the command in this chunk. A prompt can
		// arrive in a chunk whose visible tail is not prompt-like, and a quiet
		// session gets no next chunk to look again. Ask for one bounded recheck
		// after the guard window; it only finishes a prompt-like tail.
		effects.PromptRecheck = true
	}
	if t.bellPending {
		t.bellPending = false
		effects.Bell = true
		effects.Changed = true
	}
	if !effects.Changed && len(output) > 0 {
		// Output itself is a visible state change (activity, command buffer).
		effects.Changed = true
	}
	return output, effects
}

// carryIncomplete forwards an unterminated sequence once it outgrows the
// carry bound, rather than buffering it without limit. The tap then starts
// over with the next read.
func (t *terminalTap) carryIncomplete(now time.Time, output *[]byte) {
	if len(t.buffer) <= sequenceCarryMax {
		return
	}
	*output = append(*output, t.buffer...)
	t.noteOutput(t.buffer, now)
	t.buffer = nil
}

func (t *terminalTap) noteOutput(data []byte, now time.Time) {
	if len(data) == 0 {
		return
	}
	t.lastOutput = now
	if t.pending != nil {
		// An interrupted command still has a pending capture, and its output
		// can carry the prompt that finalizes it without a D mark.
		t.outputSinceStart = true
	}
	t.stats.OutputBytes += int64(len(data))
	if bytes.IndexByte(data, 0x07) >= 0 {
		t.stats.Bells += int64(bytes.Count(data, []byte{0x07}))
		t.bellPending = true
	}
	t.tail = append(t.tail, data...)
	if len(t.tail) > 512 {
		t.tail = append([]byte(nil), t.tail[len(t.tail)-512:]...)
	}
}

func (t *terminalTap) startTransfer(kind TransferKind, prefix int, now time.Time) {
	t.transfer = TransferState{
		Kind:       kind,
		Active:     true,
		StartedAt:  now,
		Suppressed: prefix,
	}
	t.stats.Transfers++
}

func transferEndMarker(kind TransferKind) []byte {
	if kind == TransferZMODEM {
		return []byte("OO")
	}
	return []byte{0x04}
}

// logOutput removes OSC 52 clipboard payloads from output before it reaches a
// Session log. MyGo still receives the sequence so an allowed clipboard write
// works, but clipboard data is not something the screen displays and may be a
// secret, so it must not be persisted. The tap returns only complete OSC
// sequences, so a payload without its terminator cannot be partially logged.
func logOutput(output []byte) []byte {
	index := bytes.Index(output, []byte("\x1b]52;"))
	if index < 0 {
		return output
	}
	var cleaned []byte
	for index >= 0 {
		cleaned = append(cleaned, output[:index]...)
		rest := output[index:]
		end := -1
		for position := 2; position < len(rest); position++ {
			if rest[position] == 0x07 || rest[position] == 0x18 || rest[position] == 0x1a {
				end = position + 1
				break
			}
			if rest[position] == 0x1b {
				if position+1 < len(rest) && rest[position+1] == '\\' {
					end = position + 2
				}
				break
			}
		}
		if end < 0 {
			// An incomplete sequence should not reach here; dropping the
			// remainder is safer than logging a partial payload.
			return cleaned
		}
		output = rest[end:]
		index = bytes.Index(output, []byte("\x1b]52;"))
	}
	return append(cleaned, output...)
}

func nextTapSpecial(data []byte) int {
	for index, value := range data {
		switch value {
		case 0x1b, '*', 0x01, 0x02:
			return index
		}
	}
	return -1
}

func isPartialPrefix(data, marker []byte) bool {
	return len(data) < len(marker) && bytes.Equal(data, marker[:len(data)])
}

// bracketedPasteMode recognizes the DECSET 2004 enable and disable controls
// (ESC [ ? 2 0 0 4 h | l) in one complete output sequence.
func bracketedPasteMode(sequence []byte) (enabled, ok bool) {
	if len(sequence) != 8 || sequence[1] != '[' || sequence[2] != '?' ||
		!bytes.Equal(sequence[3:7], []byte("2004")) {
		return false, false
	}
	switch sequence[7] {
	case 'h':
		return true, true
	case 'l':
		return false, true
	}
	return false, false
}

// escapeEnd returns the length of the escape sequence starting at data[0],
// which is ESC, and whether the buffer holds all of it. It covers control
// sequences, such as the cursor-show ESC [?25h ConPTY writes after a prompt,
// and the two- or three-byte escapes of charsets and keypad modes.
func escapeEnd(data []byte) (int, bool) {
	if len(data) < 2 {
		return 0, false
	}
	if data[1] == '[' {
		for index := 2; index < len(data); index++ {
			if data[index] >= 0x40 && data[index] <= 0x7e {
				return index + 1, true
			}
		}
		return 0, false
	}
	if data[1] == 'O' || data[1] == 'N' {
		// SS3 (ESC O) and SS2 (ESC N) carry one final byte.
		if len(data) < 3 {
			return 0, false
		}
		if data[2] >= 0x40 && data[2] <= 0x7e {
			return 3, true
		}
		return 1, true
	}
	// ESC with optional intermediate bytes (0x20 to 0x2f) and one final byte.
	index := 1
	for index < len(data) && data[index] >= 0x20 && data[index] <= 0x2f {
		index++
	}
	if index >= len(data) {
		return 0, false
	}
	if data[index] >= 0x30 && data[index] <= 0x7e {
		return index + 1, true
	}
	// Not a sequence the grammar knows: forward the ESC alone rather than
	// waiting forever for a terminator that will not come.
	return 1, true
}

// findOSC locates an OSC terminator (BEL or ST). An ESC that does not start ST
// aborts the OSC, matching xterm, and is left for the caller to process.
func findOSC(data []byte) (contentEnd, totalEnd int, complete bool) {
	for index := 2; index < len(data); index++ {
		if data[index] == 0x07 {
			return index, index + 1, true
		}
		if data[index] == 0x18 || data[index] == 0x1a {
			return index, index + 1, true
		}
		if data[index] == 0x1b {
			if index+1 >= len(data) {
				return 0, 0, false
			}
			if data[index+1] == '\\' {
				return index, index + 2, true
			}
			return index, index, true
		}
	}
	return 0, 0, false
}

// handleOSC parses one OSC payload. It reports whether the sequence should
// still reach MyGo and any state effects. Notification sequences are app-owned
// and never reach the terminal.
func (t *terminalTap) handleOSC(payload []byte, now time.Time) (bool, tapEffects) {
	code, rest, ok := splitOSCCode(payload)
	if !ok {
		return true, tapEffects{}
	}
	switch code {
	case 0, 2:
		t.title = string(rest)
		return true, tapEffects{Changed: true}
	case 7:
		if t.nestedShell != "" {
			// The nested shell's cwd is not the Session shell's cwd. The outer
			// shell publishes its own OSC 7 again with the next hook handshake.
			return true, tapEffects{}
		}
		t.cwd = parseOSC7(string(rest))
		t.nativeSeen = true
		return true, tapEffects{Changed: true}
	case 52:
		return t.handleOSC52(string(rest), now), tapEffects{Changed: true}
	case 133:
		changed, completion := t.handleOSC133(string(rest), now)
		effects := tapEffects{Changed: changed}
		if completion != nil {
			effects.Completions = []capturedCommand{*completion}
		}
		return true, effects
	case 1337:
		if changed, owned, completion := t.handleNetcattyHook(string(rest)); owned {
			effects := tapEffects{Changed: changed}
			if completion != nil {
				effects.Completions = []capturedCommand{*completion}
			}
			return false, effects
		}
		if changed, owned := t.handleNetcattyHookSkip(string(rest)); owned {
			return false, tapEffects{Changed: changed}
		}
		changed, owned := t.handleNetcattyExec(string(rest), now)
		if !owned {
			// Another OSC 1337 extension, such as iTerm2's image protocol,
			// belongs to the terminal.
			return true, tapEffects{}
		}
		return false, tapEffects{Changed: changed}
	case 9:
		notification := parseOSC9(string(rest))
		if notification == nil {
			return false, tapEffects{}
		}
		return false, t.publishNotification(*notification, now)
	case 777:
		notification := parseOSC777(string(rest))
		if notification == nil {
			return false, tapEffects{}
		}
		return false, t.publishNotification(*notification, now)
	case 99:
		notification := t.osc99.consume(string(rest), now)
		if notification == nil {
			return false, tapEffects{}
		}
		return false, t.publishNotification(*notification, now)
	default:
		return true, tapEffects{}
	}
}

func splitOSCCode(payload []byte) (int, []byte, bool) {
	index := 0
	code := 0
	for index < len(payload) && payload[index] >= '0' && payload[index] <= '9' {
		code = code*10 + int(payload[index]-'0')
		index++
	}
	if index == 0 {
		return 0, nil, false
	}
	if index == len(payload) {
		return code, nil, true
	}
	if payload[index] != ';' {
		return 0, nil, false
	}
	return code, payload[index+1:], true
}

func (t *terminalTap) handleOSC52(rest string, now time.Time) bool {
	separator := strings.IndexByte(rest, ';')
	if separator < 0 {
		return false
	}
	selection := rest[:separator]
	payload := rest[separator+1:]
	if selection == "" {
		selection = "c"
	}
	if payload == "?" {
		allowed := t.policy.ReadsAllowed() || t.policy.AsksOnRead()
		t.clipboard = &ClipboardEvent{Operation: ClipboardRead, Allowed: allowed, At: now}
		return allowed
	}
	allowed := t.policy.WritesAllowed()
	t.clipboard = &ClipboardEvent{Operation: ClipboardWrite, Allowed: allowed, Bytes: len(payload), At: now}
	return allowed
}

// handleOSC133 applies one standard shell-integration mark. It reports a state
// change and, for D or A, the command completion that the Session must publish
// after the tap lock is released. Marks for an already finalized command are
// ignored until the next Enter starts a new pending command.
func (t *terminalTap) handleOSC133(rest string, now time.Time) (bool, *capturedCommand) {
	if rest == "" {
		return false, nil
	}
	if t.nestedShell != "" {
		// While an opaque nested shell owns the terminal, native marks come
		// from it (nu) or are the outer hook's late marks for a command that
		// was already finalized. Neither may change the Session shell's
		// capture state; the next outer handshake restores it.
		return false, nil
	}
	kind := rest[0]
	switch kind {
	case 'A':
		t.nativeSeen = true
		if t.hookExpected && !t.hookSeen {
			// Every capture hook announces itself at the prompt before this
			// mark. A prompt without that handshake means the shell is running
			// a different startup — the Lens OSC 133 integration replaces the
			// planned hook's ZDOTDIR on a local zsh, for one — so the hook is
			// not installed and will never speak. Stop reserving typed
			// commands for it: otherwise every command of the Session stays
			// pending until it closes, and the history never sees it while the
			// user is typing.
			t.hookExpected = false
			if t.pending != nil && t.pending.awaitingHook {
				// The line was typed before this prompt was drawn, so this
				// mark starts the command rather than finishing it. Native
				// capture owns the line now; its own D publishes it.
				t.pending.awaitingHook = false
				return true, nil
			}
		}
		if t.hookOwnedPending() {
			// The hook owns this capture; only its D may finish it.
			return false, nil
		}
		// A prompt-start mark without a preceding D still finishes the
		// command, with an unknown exit code.
		completion := t.finalizePendingLocked(now, true, 0, false)
		if completion == nil {
			return false, nil
		}
		return true, completion
	case 'B':
		t.nativeSeen = true
		t.command.Running = false
		return true, nil
	case 'C':
		t.nativeSeen = true
		if t.pending == nil {
			return false, nil
		}
		t.command.Running = true
		if t.command.StartedAt.IsZero() {
			t.command.StartedAt = now
		}
		if t.pending.startedAt.IsZero() {
			t.pending.startedAt = now
		}
		t.command.FinishedAt = time.Time{}
		// Only output after the command starts can carry its prompt.
		t.outputSinceStart = false
		return true, nil
	case 'D':
		t.nativeSeen = true
		exitCode, hasExitCode := 0, false
		if len(rest) > 2 && rest[1] == ';' {
			if code, err := parseInt(rest[2:]); err == nil {
				exitCode, hasExitCode = code, true
			}
		}
		// The failure statistic counts every reported non-zero exit, as before,
		// even when no typed command was pending to capture.
		if hasExitCode && exitCode != 0 {
			t.stats.FailedCommands++
		}
		if t.pending != nil && t.pending.awaitingHook {
			// A prompt mark that arrived before the planned hook's first
			// private sequence belongs to an earlier command; the tentative
			// capture must stay until the hook replaces it, a prompt without
			// the handshake releases it, or the Session ends.
			return false, nil
		}
		completion := t.finalizePendingLocked(now, true, exitCode, hasExitCode)
		if completion == nil {
			return false, nil
		}
		return true, completion
	default:
		return false, nil
	}
}

// handleNetcattyHook consumes the session hook's prompt handshake. The
// handshake is emitted at every prompt by the installed hook; once seen, the
// tap stops finalizing typed input at native prompt marks, because the hook's
// private preexec sequence is the authoritative command start. A sequence with
// the private marker is always consumed, even when malformed; a malformed
// payload changes nothing so a broken handshake leaves the fallback in place.
func (t *terminalTap) handleNetcattyHook(rest string) (changed, owned bool, completion *capturedCommand) {
	if !strings.HasPrefix(rest, netcattyHookPrefix) {
		return false, false, nil
	}
	if !validHookShell(rest[len(netcattyHookPrefix):]) {
		return false, true, nil
	}
	if t.nestedShell != "" {
		// A hook has resumed after the nested shell: the outer command's
		// handshake, or the nested shell's own hook. Publish the last inner
		// fallback capture before Tier 1 takes over again. Its finish and exit
		// stay unknown because the inner shell never reported them to us.
		completion = t.finalizePendingLocked(time.Time{}, false, 0, false)
		t.nestedShell = ""
	}
	if t.hookActive {
		t.hookSeen = true
		return completion != nil, true, completion
	}
	t.hookActive = true
	t.hookSeen = true
	return true, true, completion
}

// handleNetcattyHookSkip consumes the startup script's refusal marker. A hook
// that deliberately installs nothing (an existing integration, a missing
// encoder, private fish, or an unsupported remote shell) emits it instead of
// the prompt handshake. The tap then stops waiting for a hook that will never
// speak: hookExpected is cleared and the current tentative capture becomes an
// ordinary typed capture, so the prompt path finishes it and every later
// Enter starts a normal Tier 2/3 capture. A skip that arrives while the hook
// is active is ignored, because the installed hook stays authoritative. A
// skip from a child shell after a nested reset is not ignored: the parent's
// hook no longer owns the prompt, and the child needs the Tier 2/3 fallback.
func (t *terminalTap) handleNetcattyHookSkip(rest string) (changed, owned bool) {
	if rest != netcattyHookSkipPrefix && !strings.HasPrefix(rest, netcattyHookSkipPrefix+";") {
		return false, false
	}
	shell := strings.TrimPrefix(rest, netcattyHookSkipPrefix)
	shell = strings.TrimPrefix(shell, ";")
	if shell != "" && !validHookShell(shell) {
		return false, true
	}
	if t.nestedShell != "" {
		// A skip from the opaque nested shell says nothing about the outer
		// hook, which is resumed by its own handshake.
		return false, true
	}
	if t.hookSeen {
		if !t.hookActive || t.pending == nil || !nestedShellCommand(t.pending.command) {
			// The installed hook stays authoritative; a late skip is ignored.
			return false, true
		}
		// A shell we launched nested refused its own hook. Its startup script
		// emits the marker before its first prompt, so the outer hook is still
		// authoritative over the outer command; the nested prompt will hand
		// capture to the child. Only the plan's remaining expectation changes.
		changed = t.hookExpected
		t.hookExpected = false
		return changed, true
	}
	changed = t.hookExpected
	t.hookExpected = false
	if t.pending != nil {
		t.pending.awaitingHook = false
	}
	return changed, true
}

// handleNetcattyExec consumes the session hook's private sequence. It reports
// whether the tap owned the sequence and whether Session state changed. A
// sequence with the private marker is always consumed, even when malformed, so
// it can never reach the renderer; a malformed payload changes nothing.
func (t *terminalTap) handleNetcattyExec(rest string, now time.Time) (changed, owned bool) {
	if !strings.HasPrefix(rest, netcattyExecPrefix) {
		return false, false
	}
	fields := strings.Split(rest[len(netcattyExecPrefix):], ";")
	if len(fields) != 3 {
		return false, true
	}
	shell := fields[0]
	cwd, cwdErr := decodeBase64URL(fields[1])
	command, commandErr := decodeBase64URL(fields[2])
	if cwdErr != nil || commandErr != nil || !validHookShell(shell) ||
		command == "" || !utf8.ValidString(cwd) || !utf8.ValidString(command) {
		return false, true
	}
	if len(command) > domainhistory.MaxCommandLength {
		command = command[:domainhistory.MaxCommandLength]
	}
	if len(cwd) > hookValueMax {
		cwd = cwd[:hookValueMax]
	}

	// The hook's values are authoritative: replace any pending fallback
	// capture, or start the pending command when nothing was observed yet.
	t.hookActive = true
	t.hookSeen = true
	if t.pending == nil {
		t.pending = &pendingCommand{}
		t.stats.Commands++
	}
	t.pending.command = command
	t.pending.cwd = cwd
	t.pending.shell = shell
	t.pending.startedAt = now
	t.pending.captureTier = domainhistory.CaptureTierHook
	t.pending.interrupted = false
	// The private sequence is the authoritative start: it replaces the
	// tentative typed capture, so the hook's D may finish it.
	t.pending.awaitingHook = false
	t.command = CommandState{Text: command, Running: true, StartedAt: now}
	// Only output after the command starts can carry its prompt.
	t.outputSinceStart = false
	return true, true
}

// decodeBase64URL accepts the URL-safe alphabet with or without padding.
func decodeBase64URL(value string) (string, error) {
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return string(decoded), nil
	}
	decoded, err := base64.URLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// validHookShell keeps the shell name to a short token so a hostile sequence
// cannot smuggle control bytes into a snapshot or a log.
func validHookShell(shell string) bool {
	if shell == "" || len(shell) > hookShellMax {
		return false
	}
	for index := 0; index < len(shell); index++ {
		value := shell[index]
		switch {
		case value >= 'a' && value <= 'z':
		case value >= 'A' && value <= 'Z':
		case value >= '0' && value <= '9':
		case value == '-', value == '_', value == '.':
		default:
			return false
		}
	}
	return true
}

func parseInt(value string) (int, error) {
	sign := 1
	index := 0
	if strings.HasPrefix(value, "-") {
		sign = -1
		index = 1
	}
	if index >= len(value) {
		return 0, errors.New("session: invalid integer")
	}
	number := 0
	for ; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, errors.New("session: invalid integer")
		}
		digit := int(value[index] - '0')
		if number > (int(^uint(0)>>1)-digit)/10 {
			return 0, errors.New("session: integer out of range")
		}
		number = number*10 + digit
	}
	return sign * number, nil
}

func parseOSC7(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "file://") {
		parsed, err := url.Parse(value)
		if err == nil {
			path := parsed.Path
			if path == "" {
				path = parsed.Opaque
			}
			if decoded, err := url.PathUnescape(path); err == nil {
				path = decoded
			}
			if len(path) > 2 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
			return path
		}
	}
	return value
}

type rawNotification struct {
	title    string
	body     string
	protocol string
}

func parseOSC9(payload string) *rawNotification {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	// ConEmu and Windows Terminal use OSC 9;n for progress and other
	// commands; those are not notifications.
	digits := true
	for index := 0; index < len(payload); index++ {
		if payload[index] == ';' {
			break
		}
		if payload[index] < '0' || payload[index] > '9' {
			digits = false
			break
		}
	}
	if digits {
		return nil
	}
	return &rawNotification{body: payload, protocol: "osc9"}
}

func parseOSC777(payload string) *rawNotification {
	if !strings.HasPrefix(strings.ToLower(payload), "notify;") {
		return nil
	}
	rest := payload[len("notify;"):]
	separator := strings.IndexByte(rest, ';')
	if separator < 0 {
		body := strings.TrimSpace(rest)
		if body == "" {
			return nil
		}
		return &rawNotification{body: body, protocol: "osc777"}
	}
	title := strings.TrimSpace(rest[:separator])
	body := strings.TrimSpace(rest[separator+1:])
	if title == "" && body == "" {
		return nil
	}
	return &rawNotification{title: title, body: body, protocol: "osc777"}
}

type osc99Assembler struct {
	pending map[string]*osc99Pending
}

func (a *osc99Assembler) consume(payload string, now time.Time) *rawNotification {
	a.prune(now)
	separator := strings.IndexByte(payload, ';')
	metadata := ""
	rawPayload := payload
	if separator >= 0 {
		metadata = payload[:separator]
		rawPayload = payload[separator+1:]
	}
	fields := parseOSC99Metadata(metadata)
	if fields.close {
		delete(a.pending, fields.id)
		return nil
	}
	decoded := rawPayload
	if fields.encoded {
		decoded = decodeOSC99Base64(rawPayload)
	}
	current := a.pending[fields.id]
	if current == nil {
		current = &osc99Pending{updatedAt: now}
	}
	if fields.part == "body" {
		current.body += decoded
	} else {
		current.title += decoded
	}
	current.updatedAt = now
	if !fields.done {
		a.pending[fields.id] = current
		a.evict()
		return nil
	}
	delete(a.pending, fields.id)
	if strings.TrimSpace(current.title) == "" && strings.TrimSpace(current.body) == "" {
		return nil
	}
	return &rawNotification{title: current.title, body: current.body, protocol: "osc99"}
}

func (a *osc99Assembler) prune(now time.Time) {
	for id, entry := range a.pending {
		if now.Sub(entry.updatedAt) > osc99PendingTTL {
			delete(a.pending, id)
		}
	}
}

func (a *osc99Assembler) evict() {
	for len(a.pending) > osc99PendingMax {
		oldestID := ""
		var oldest time.Time
		for id, entry := range a.pending {
			if oldestID == "" || entry.updatedAt.Before(oldest) {
				oldestID = id
				oldest = entry.updatedAt
			}
		}
		delete(a.pending, oldestID)
	}
}

type osc99Fields struct {
	id      string
	part    string
	done    bool
	encoded bool
	close   bool
}

func parseOSC99Metadata(metadata string) osc99Fields {
	fields := osc99Fields{part: "title", done: true}
	if strings.TrimSpace(metadata) == "" {
		return fields
	}
	for _, token := range strings.Split(metadata, ":") {
		equal := strings.IndexByte(token, '=')
		if equal <= 0 {
			continue
		}
		key := strings.TrimSpace(token[:equal])
		value := strings.TrimSpace(token[equal+1:])
		switch key {
		case "i":
			fields.id = value
		case "p":
			if value == "title" || value == "body" {
				fields.part = value
			} else if value == "close" {
				fields.close = true
			}
		case "a":
			if value == "close" {
				fields.close = true
			}
		case "d":
			fields.done = value != "0"
		case "e":
			fields.encoded = value == "1"
		}
	}
	return fields
}

func decodeOSC99Base64(value string) string {
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return string(decoded)
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return string(decoded)
	}
	return ""
}

func (t *terminalTap) publishNotification(raw rawNotification, now time.Time) tapEffects {
	title, body := resolveNotification(raw)
	if !t.allowNotification(now) {
		return tapEffects{}
	}
	notification := &TerminalNotification{
		Title:    title,
		Body:     body,
		Protocol: raw.protocol,
		At:       now,
	}
	t.notification = notification
	t.stats.Notifications++
	copyNotification := *notification
	return tapEffects{Changed: true, Notification: &copyNotification}
}

func (t *terminalTap) allowNotification(now time.Time) bool {
	recent := t.notificationStamps[:0]
	for _, stamp := range t.notificationStamps {
		if now.Sub(stamp) < oscNotificationWindow {
			recent = append(recent, stamp)
		}
	}
	t.notificationStamps = recent
	if len(recent) > 0 && now.Sub(recent[len(recent)-1]) < oscNotificationMinGap {
		return false
	}
	if len(recent) >= oscNotificationMax {
		return false
	}
	t.notificationStamps = append(t.notificationStamps, now)
	return true
}

func resolveNotification(raw rawNotification) (string, string) {
	title := sanitizeNotificationText(raw.title, oscNotificationTitleMax)
	body := sanitizeNotificationText(raw.body, oscNotificationBodyMax)
	if title != "" && body != "" {
		return title, body
	}
	if body != "" {
		return oscNotificationFallbackTitle, body
	}
	if title != "" {
		return oscNotificationFallbackTitle, title
	}
	return oscNotificationFallbackTitle, ""
}

func sanitizeNotificationText(value string, max int) string {
	if value == "" || max <= 0 {
		return ""
	}
	var builder strings.Builder
	for _, character := range value {
		if character <= 8 || character == 0x0b || character == 0x0c ||
			(character >= 0x0e && character <= 0x1f) || character == 0x7f {
			continue
		}
		builder.WriteRune(character)
	}
	cleaned := strings.Join(strings.Fields(builder.String()), " ")
	runes := []rune(cleaned)
	if len(runes) <= max {
		return cleaned
	}
	return strings.TrimRight(string(runes[:max-1]), " ") + "…"
}

// promptResult is the prompt fallback's outcome for one check.
type promptResult uint8

const (
	promptNoChange promptResult = iota
	promptFinished
	// promptDeferred means a prompt-like line arrived inside the guard window.
	// It cannot settle the command yet, but nothing may follow it, so the
	// caller must check again once the window expires.
	promptDeferred
)

// checkPrompt is the OSC 133-free fallback: a recognizable shell prompt at the
// end of output finishes a command that was submitted with Enter. Only output
// that arrived after the command started can carry the new prompt; a prompt
// already in the tail from before Enter belongs to the previous command. A
// command marked interrupted by Ctrl+C still finalizes here when no D arrived.
func (t *terminalTap) checkPrompt(now time.Time) (promptResult, *capturedCommand) {
	if t.pending == nil || !t.outputSinceStart || len(t.tail) == 0 {
		return promptNoChange, nil
	}
	if t.pending.awaitingHook {
		// The hook was planned but has not spoken yet. Only its private
		// sequence (or Session close) may decide this capture's fate.
		return promptNoChange, nil
	}
	if t.hookOwnedPending() {
		// A hooked command's output may itself end in a prompt-like line
		// (progress percentages, interactive programs, `printf '12%'`), and
		// only the hook's OSC 133 D may finish it. The one exception is a
		// command that started an interactive shell or attach client: its
		// inner prompt proves the outer command is no longer the foreground
		// capture owner.
		return t.promptNestedShellLocked(now)
	}
	if t.pending != nil && nestedShellCommand(t.pending.command) {
		// A shell launched from a Tier 2 or Tier 3 capture takes the terminal
		// too. The inner prompt finalizes the outer capture and clears the
		// parent's native marks so the child's commands start at Tier 3
		// unless the child emits OSC 133/7 itself.
		if result, completion := t.promptNestedShellLocked(now); result != promptNoChange {
			return result, completion
		}
	}
	return t.promptCandidateLocked(now)
}

// hookOwnedPending reports whether a planned or active hook owns the current
// capture. While it does, typed input only queues and native prompt marks and
// the prompt heuristic must not finalize the command.
func (t *terminalTap) hookOwnedPending() bool {
	if t.pending == nil {
		return false
	}
	return t.pending.awaitingHook ||
		(t.hookActive && t.pending.captureTier == domainhistory.CaptureTierHook)
}

// promptNestedShellLocked detects the point where a hooked command handed the
// terminal to an interactive shell, ssh, or tmux attach. The outer command has
// no D yet (the inner shell is still running), but its primary prompt proves
// the outer command reached the foreground prompt. Finalize it with the
// unknown-finish semantics of a new Enter, stop treating the outer hook as
// authoritative, and let the inner shell use the ordinary typed/prompt
// capture. The outer D that arrives when the inner shell exits is ignored
// because the capture is already finalized; the next handshake restores Tier 1.
func (t *terminalTap) promptNestedShellLocked(now time.Time) (promptResult, *capturedCommand) {
	name := nestedShellName(t.pending.command)
	if name == "" {
		return promptNoChange, nil
	}
	line := lastVisibleLine(t.tail)
	if line == "" || !nestedShellPromptLike(name, line) || continuationPrompt(line) {
		return promptNoChange, nil
	}
	if !t.pending.startedAt.IsZero() && now.Sub(t.pending.startedAt) < promptWindow {
		return promptDeferred, nil
	}
	t.hookActive = false
	// The marks seen so far came from the outer shell. The child has not
	// emitted any OSC 133/7 at this point, so its typed commands must start at
	// Tier 3 instead of inheriting Tier 2 from the parent. A child that emits
	// native marks sets this again for its own commands.
	t.nativeSeen = false
	if name == "nu" || name == "xonsh" {
		// These shells were not started with the Session hook. Their native
		// marks describe the inner shell, so drop the outer cwd state and keep
		// the inner commands on the typed fallback (Tier 3) until the Session
		// shell's next handshake.
		t.cwd = ""
		t.nestedShell = name
	}
	return promptFinished, t.finalizePendingLocked(now, false, 0, false)
}

// nestedShellPromptLike recognizes the primary prompt of a nested shell. The
// ordinary prompt heuristic covers bash, zsh, fish, and nu. Xonsh's default
// prompt ends with the "@" that separates the path from the prompt token, so
// it needs its own suffix.
func nestedShellPromptLike(name, line string) bool {
	if promptLike(line) {
		return true
	}
	return name == "xonsh" && strings.HasSuffix(strings.TrimRight(line, " \t"), "@")
}

// nestedShellName reports the interactive shell or attach client command
// starts, or "" when it does not take over the terminal. The command line is
// split with quote and escape awareness, so a launcher or an option value that
// contains spaces stays one word. A shell is only recognized when every
// argument is an option: an operand is a script path and -c runs a command, so
// neither produces an inner interactive prompt. Nushell and xonsh are included
// because a hooked POSIX shell can hand its terminal to either, and their inner
// commands must not be lost.
func nestedShellName(command string) string {
	words := splitShellWords(command)
	if len(words) == 0 {
		return ""
	}
	// PowerShell's call operator prefixes the launcher: "& pwsh" or
	// "& 'C:\Program Files\...\pwsh.exe'". A leading ampersand is a syntax
	// error in a POSIX shell, so unwrapping it cannot misclassify a POSIX
	// command.
	if words[0] == "&" {
		words = words[1:]
		if len(words) == 0 {
			return ""
		}
	} else if strings.HasPrefix(words[0], "&") {
		words[0] = strings.TrimPrefix(words[0], "&")
		if words[0] == "" {
			return ""
		}
	}
	first := words[0]
	// A Windows launcher may name its executable with backslashes and the
	// optional .exe suffix; the basename is what identifies the shell.
	first = strings.ReplaceAll(first, `\`, "/")
	if slash := strings.LastIndexByte(first, '/'); slash >= 0 {
		first = first[slash+1:]
	}
	first = strings.ToLower(strings.TrimSuffix(first, ".exe"))
	// A login shell may be invoked through an argv0 such as -nu.
	first = strings.TrimPrefix(first, "-")
	switch first {
	case "bash", "zsh", "fish", "sh", "dash", "ksh", "nu", "xonsh":
		if nestedShellArgsInteractive(first, words[1:]) {
			return first
		}
		return ""
	case "pwsh", "powershell":
		// PowerShell's interactive options do not share the POSIX short
		// forms; -Command, -File, -EncodedCommand, and -NonInteractive all
		// end the interactive prompt unless -NoExit comes before them.
		// Known value-taking options may carry their value as the next
		// argument ("-ExecutionPolicy Bypass") and still open the prompt.
		if nestedPowerShellArguments(words[1:]) {
			return first
		}
		return ""
	case "ssh":
		return "ssh"
	case "tmux":
		if len(words) < 2 {
			return ""
		}
		switch words[1] {
		case "attach", "attach-session", "a":
			return "tmux"
		}
	}
	return ""
}

func nestedShellCommand(command string) bool {
	return nestedShellName(command) != ""
}

func nestedShellArgsInteractive(name string, args []string) bool {
	for _, argument := range args {
		if !strings.HasPrefix(argument, "-") {
			return false
		}
		if strings.HasPrefix(argument, "--") {
			switch name {
			case "nu":
				switch {
				case argument == "--commands", strings.HasPrefix(argument, "--commands="),
					argument == "--execute", strings.HasPrefix(argument, "--execute="),
					argument == "--stdin", argument == "--mcp",
					argument == "--help", argument == "--version":
					return false
				}
			case "xonsh":
				switch {
				case argument == "--command", strings.HasPrefix(argument, "--command="),
					argument == "--help", argument == "--version":
					return false
				}
			}
			continue
		}
		letters := argument[1:]
		switch name {
		case "nu":
			if strings.ContainsAny(letters, "chv") {
				return false
			}
		case "xonsh":
			if strings.ContainsAny(letters, "chV") {
				return false
			}
		default:
			if strings.Contains(letters, "c") {
				return false
			}
		}
	}
	return true
}

func splitShellWords(command string) []string {
	words := []string{}
	var word strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	for index := 0; index < len(command); index++ {
		character := command[index]
		switch character {
		case ' ', '\t':
			flush()
		case '\'':
			inWord = true
			for index+1 < len(command) {
				index++
				if command[index] != '\'' {
					word.WriteByte(command[index])
					continue
				}
				if index+1 < len(command) && command[index+1] == '\'' {
					word.WriteByte('\'')
					index++
					continue
				}
				break
			}
		case '"':
			inWord = true
			for index+1 < len(command) {
				index++
				if command[index] == '`' && index+1 < len(command) {
					index++
					word.WriteByte(command[index])
					continue
				}
				if command[index] == '"' {
					break
				}
				word.WriteByte(command[index])
			}
		case '`':
			inWord = true
			if index+1 < len(command) {
				index++
				word.WriteByte(command[index])
			}
		default:
			inWord = true
			word.WriteByte(character)
		}
	}
	flush()
	return words
}

func nestedPowerShellArguments(arguments []string) bool {
	noExit := false
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "-") || argument == "-" || argument == "--" {
			return false
		}
		name := strings.ToLower(strings.TrimLeft(argument, "-"))
		valueAttached := false
		if base, _, found := strings.Cut(name, ":"); found {
			name, valueAttached = base, true
		}
		switch {
		case len(name) >= 3 && strings.HasPrefix("noexit", name):
			noExit = true
		case strings.HasPrefix("command", name),
			strings.HasPrefix("encodedcommand", name),
			strings.HasPrefix("file", name):
			// Everything after the command or script belongs to it, so an
			// earlier -NoExit is the only one that can keep the prompt.
			return noExit
		case len(name) >= 3 && strings.HasPrefix("noninteractive", name),
			strings.HasPrefix("help", name), name == "?",
			strings.HasPrefix("version", name),
			strings.HasPrefix("embedding", name):
			return false
		case !valueAttached && nestedPowerShellValueOption(name):
			if index+1 >= len(arguments) || strings.HasPrefix(arguments[index+1], "-") {
				// The option's value is missing, so PowerShell would report
				// an error instead of opening a prompt.
				return false
			}
			index++
		}
	}
	return true
}

func nestedPowerShellValueOption(name string) bool {
	switch {
	case strings.HasPrefix(name, "executionpolicy"), name == "ex":
		return true
	case strings.HasPrefix(name, "windowstyle"), name == "w", name == "wo", name == "win", name == "wind":
		return true
	case strings.HasPrefix(name, "workingdirectory"), name == "wd", name == "work", name == "worki":
		return true
	case strings.HasPrefix(name, "configurationname"), name == "config", name == "conf", name == "con":
		return true
	case strings.HasPrefix(name, "settingsfile"), strings.HasPrefix(name, "custompipename"), name == "settings":
		return true
	case strings.HasPrefix(name, "outputformat"), name == "of", name == "o":
		return true
	case strings.HasPrefix(name, "inputformat"), name == "if", name == "i":
		return true
	case strings.HasPrefix(name, "psconsolefile"), name == "psc":
		return true
	}
	return false
}

// promptCandidateLocked applies the prompt heuristic without the hook guards.
// It is used by the fallback paths once the hook has been ruled out.
func (t *terminalTap) promptCandidateLocked(now time.Time) (promptResult, *capturedCommand) {
	if t.pending == nil || !t.outputSinceStart || len(t.tail) == 0 {
		return promptNoChange, nil
	}
	line := lastVisibleLine(t.tail)
	if line == "" {
		return promptNoChange, nil
	}
	if !promptLike(line) && (t.nestedShell == "" || !nestedShellPromptLike(t.nestedShell, line)) {
		return promptNoChange, nil
	}
	if !t.pending.startedAt.IsZero() && now.Sub(t.pending.startedAt) < promptWindow {
		return promptDeferred, nil
	}
	return promptFinished, t.finalizePendingLocked(now, true, 0, false)
}

// resolvePrompt re-runs the prompt fallback once the guard window has expired.
// A fast command's prompt can arrive while checkPrompt is still holding back,
// and with no further output nothing else would revisit it, leaving Running
// set and autocomplete suppressed until Ctrl+C. It returns the completion the
// Session must publish, or nil when the command stays pending.
func (t *terminalTap) resolvePrompt(now time.Time) *capturedCommand {
	t.mu.Lock()
	defer t.mu.Unlock()
	result, completion := t.checkPrompt(now)
	if result != promptFinished {
		return nil
	}
	return completion
}

// continuationPrompt recognizes the shell continuation prompts that would
// otherwise look like a finished command. Bash's PS2 is ">"; zsh's PS2 is a
// parser status such as "dquote>" or "cmdand>". A real primary prompt may also
// end in ">", but the common shapes contain the user@host form, "PS ", or a
// different prompt character, and treating an ambiguous line as a continuation
// only delays a fallback capture instead of publishing a partial command.
func continuationPrompt(line string) bool {
	line = strings.TrimRight(line, " \t")
	if line == ">" {
		return true
	}
	if !strings.HasSuffix(line, ">") || strings.Contains(line, "@") ||
		strings.HasPrefix(line, "PS ") {
		return false
	}
	for _, status := range []string{
		"quote", "dquote", "cmdand", "cmdor", "pipe", "brace", "subsh",
		"math", "then", "else", "do", "done", "fi", "for", "while", "if",
		"case", "select", "repeat", "time", "coproc", "here",
	} {
		if strings.HasSuffix(line, status+">") {
			return true
		}
	}
	return false
}

// startPendingLocked begins capturing one command at Enter. The cwd is the
// last OSC 7 value seen before Enter; a later hook may replace it. The tier is
// Tier 2 when the shell has shown native OSC 133/7 integration, otherwise the
// typed-input fallback is Tier 3.
func (t *terminalTap) startPendingLocked(command string, now time.Time) {
	tier := domainhistory.CaptureTierFallback
	if t.nativeSeen {
		tier = domainhistory.CaptureTierNativeOSC
	}
	t.pending = &pendingCommand{
		command:     command,
		cwd:         t.cwd,
		startedAt:   now,
		captureTier: tier,
	}
}

// finalizePendingLocked finalizes the one pending command and returns it once.
// A nil result means there was nothing left to finalize, so late OSC marks and
// a racing Session close can never produce a second event. finished reports
// whether the shell reported a finish; when it did not (a new Enter or the
// Session ending), FinishedAt stays zero so the duration is unknown.
func (t *terminalTap) finalizePendingLocked(now time.Time, finished bool, exitCode int, hasExitCode bool) *capturedCommand {
	pending := t.pending
	if pending == nil {
		return nil
	}
	t.pending = nil
	tier := pending.captureTier
	if t.nativeSeen && tier == domainhistory.CaptureTierFallback {
		// A command that started as the typed fallback is Tier 2 once the
		// shell shows native OSC 133/7 integration before it finishes.
		tier = domainhistory.CaptureTierNativeOSC
	}
	completion := &capturedCommand{
		command:     pending.command,
		cwd:         pending.cwd,
		shell:       pending.shell,
		startedAt:   pending.startedAt,
		captureTier: tier,
	}
	if finished {
		completion.finishedAt = now
		t.command.FinishedAt = now
	}
	t.command.Running = false
	if hasExitCode {
		completion.exitCode = exitCode
		completion.hasExitCode = true
		t.command.ExitCode = exitCode
		t.command.HasExitCode = true
	}
	return completion
}

// finalizePending finalizes a still-pending command when the Session ends. The
// finish time stays zero because the shell never reported one, so the recorded
// duration is unknown rather than invented.
func (t *terminalTap) finalizePending() *capturedCommand {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.finalizePendingLocked(time.Time{}, false, 0, false)
}

func lastVisibleLine(data []byte) string {
	// Split on the raw CR/LF bytes first. Stripping terminal controls removes
	// them too, and doing that first concatenated every line of the tail into
	// one string, so a continuation prompt such as "dquote> " was never seen
	// as the last line.
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	lines := bytes.Split(normalized, []byte("\n"))
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimRight(stripTerminalControls(lines[index]), " \t")
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

func stripTerminalControls(data []byte) string {
	var builder strings.Builder
	for index := 0; index < len(data); index++ {
		value := data[index]
		if value == 0x1b {
			if index+1 >= len(data) {
				break
			}
			if data[index+1] == ']' {
				index += 2
				for index < len(data) {
					if data[index] == 0x07 {
						break
					}
					if data[index] == 0x1b && index+1 < len(data) && data[index+1] == '\\' {
						index++
						break
					}
					index++
				}
				continue
			}
			index++
			if data[index] == '[' {
				for index+1 < len(data) {
					index++
					if data[index] >= 0x40 && data[index] <= 0x7e {
						break
					}
				}
			}
			continue
		}
		if value < 0x20 || value == 0x7f {
			continue
		}
		builder.WriteByte(value)
	}
	return builder.String()
}

func promptLike(line string) bool {
	trimmed := strings.TrimRight(line, " \t")
	for _, suffix := range []string{"$", "#", "❯", "➜", "λ", "%"} {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	if strings.HasSuffix(trimmed, ">") {
		return strings.HasPrefix(trimmed, "PS ") || strings.Contains(trimmed, "@") || len(trimmed) > 2
	}
	return false
}

// handleInput observes typed bytes and returns what may reach the transport,
// whether the input started a command, the commands that finished during this
// call, and the transfer state change. During a transfer all normal input is
// dropped; Ctrl+C aborts with the original five CAN bytes.
func (t *terminalTap) handleInput(data []byte, now time.Time) ([]byte, bool, bool, []capturedCommand, *TransferState) {
	return t.handleInputProvenance(data, now, true)
}

// handleInputProvenance is handleInput with the chunk's provenance: typed is
// true only for keystrokes typed in this Session's own terminal. A delivered
// copy — a broadcast fan-out, a compose submission, an inserted snippet, the
// startup command — is mirrored exactly the same, but the screen is never
// consulted for it: the reader answers for the user's own keystrokes, and a
// delivery's first byte is no fresh-line keystroke, so capturing the prompt
// or arbitrating against the rendered line for it would read a moment that
// has nothing to do with the copy.
func (t *terminalTap) handleInputProvenance(data []byte, now time.Time, typed bool) ([]byte, bool, bool, []capturedCommand, *TransferState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.transfer.Active {
		if bytes.IndexByte(data, 0x03) >= 0 {
			t.transfer.Active = false
			t.transfer.FinishedAt = now
			t.transfer.End = TransferEndCancelled
			t.buffer = nil
			copyState := t.transfer
			return []byte{0x18, 0x18, 0x18, 0x18, 0x18}, true, false, nil, &copyState
		}
		return nil, false, false, nil, nil
	}
	changed, started, completions := t.recordInput(data, now, typed)
	return data, changed, started, completions, nil
}

func (t *terminalTap) recordInput(data []byte, now time.Time, typed bool) (bool, bool, []capturedCommand) {
	changed := false
	started := false
	var completions []capturedCommand
	for _, value := range data {
		if t.inputPaste && t.inputEscape == inputPlain {
			// A bracketed paste is inserted into the line editor literally.
			// Only the end marker is meaningful; every other byte, including
			// a newline, is pasted text rather than Enter or a control key.
			// The text still joins the tracked line, so the input indicator
			// and autocomplete see the whole command after a paste or an
			// explicit History insertion.
			if value == 0x1b {
				t.inputEscape = inputEscape
				continue
			}
			if value == '\r' {
				value = '\n'
			}
			if (value >= 0x20 && value != 0x7f) || value == '\n' || value == '\t' {
				t.inputPristine = false
				changed = t.appendInputByte(value) || changed
			}
			continue
		}
		switch t.inputEscape {
		case inputEscape:
			t.inputEscape = inputPlain
			t.inputCSIParams = t.inputCSIParams[:0]
			if value == '[' {
				t.inputEscape = inputCSI
			} else if value == ']' {
				t.inputEscape = inputOSC
			}
			continue
		case inputCSI:
			switch {
			case value >= 0x40 && value <= 0x7e:
				if value == '~' {
					switch string(t.inputCSIParams) {
					case "200":
						t.inputPaste = true
					case "201":
						t.inputPaste = false
					}
				}
				// Cursor keys and home/end move the line editor's cursor;
				// tracking them keeps the accepted-suggestion erase boundary
				// and the typed line in agreement. Inside a paste they are not
				// keys at all: the editor inserts the pasted bytes literally,
				// so the sequence must neither move the tracked cursor nor
				// (for the recall keys) discard a paste that is fully
				// traceable. The pasted control bytes themselves never join
				// the tracked line either way, since the input filter only
				// takes printable text.
				if !t.inputPaste {
					t.applyInputCSI(value)
				}
				t.inputEscape = inputPlain
				t.inputCSIParams = t.inputCSIParams[:0]
			case value >= 0x30 && value <= 0x3f:
				t.inputCSIParams = append(t.inputCSIParams, value)
			default:
				t.inputEscape = inputPlain
				t.inputCSIParams = t.inputCSIParams[:0]
			}
			continue
		case inputOSC:
			if value == 0x07 {
				t.inputEscape = inputPlain
			} else if value == 0x1b {
				t.inputEscape = inputOSCExit
			}
			continue
		case inputOSCExit:
			if value == '\\' {
				t.inputEscape = inputPlain
			} else {
				t.inputEscape = inputOSC
			}
			continue
		}
		switch value {
		case 0x1b:
			t.inputEscape = inputEscape
			t.inputPending = nil
		case 0x03:
			t.inputBuffer = ""
			t.inputCursor = 0
			t.inputPending = nil
			t.inputLost = false
			t.inputPristine = true
			t.completionArmed = false
			// Ctrl+C never emits by itself: the shell may still report an exit
			// code such as 130, or a prompt fallback or Session close may
			// finalize the command with an unknown exit.
			if t.pending != nil {
				t.pending.interrupted = true
			}
			if t.command.Running {
				t.command.Running = false
				t.command.FinishedAt = now
			}
			changed = true
		case 0x01: // Ctrl+A: beginning of line
			t.inputPending = nil
			t.inputCursor = 0
			changed = true
		case 0x05: // Ctrl+E: end of line
			t.inputPending = nil
			t.inputCursor = t.inputRuneLength()
			changed = true
		case 0x0b: // Ctrl+K: kill to the end of the line
			t.inputPending = nil
			if end := t.inputByteOffset(t.inputCursor); end < len(t.inputBuffer) {
				t.inputBuffer = t.inputBuffer[:end]
				changed = true
			}
		case 0x15: // Ctrl+U: kill to the beginning of the line
			t.inputPending = nil
			if start := t.inputByteOffset(t.inputCursor); start > 0 {
				t.inputBuffer = t.inputBuffer[start:]
				t.inputCursor = 0
				changed = true
			}
		case 0x12, 0x17, 0x19, 0x14, 0x0e, 0x10:
			// Ctrl+R reverse search, Ctrl+W word kill, Ctrl+Y yank, Ctrl+T
			// transpose, Ctrl+N/P history step: each rewrites the buffer from
			// the shell's own history or kill ring, which the tap never sees.
			// A wrong capture is worse than none — the hybrid line becomes a
			// command that never ran — so the capture is discarded.
			t.inputPending = nil
			t.invalidateInputLocked()
		case 0x09:
			// Tab neither joins the mirror nor invalidates it — in a line
			// editor it is the completion key, not text — but it is the one
			// keystroke that can grow the line invisibly, so it arms the
			// fragment rule at Enter. Without the gate the rule could not
			// tell completion growth from a stale pre-erasure line whose
			// erase echo is still in flight: both are the mirror plus a
			// suffix, and only the keystroke that caused it tells them apart.
			t.inputPending = nil
			t.completionArmed = true
		case 0x7f, 0x08:
			t.inputPending = nil
			if t.deleteInputRuneBeforeCursor() {
				changed = true
			}
		case '\r', '\n':
			mirror := strings.TrimSpace(t.inputBuffer)
			lost := t.inputLost
			armed := t.completionArmed
			t.inputBuffer = ""
			t.inputCursor = 0
			t.inputPending = nil
			t.inputLost = false
			t.inputPristine = true
			t.completionArmed = false
			if t.hookActive {
				// The installed hook's private preexec sequence is the only
				// authority while it is active. Typed Enter must not start,
				// finalize, or publish anything: a line typed while a command
				// runs waits in the terminal input queue, and the hook reports
				// it when the shell actually executes it. A nested shell is
				// detected from its prompt, not from elapsed time.
				changed = true
				continue
			}
			if t.pending != nil && t.pending.awaitingHook {
				// The planned hook has not spoken yet. This line (or a later
				// line of the same pasted command) belongs to one tentative
				// capture; its private sequence replaces it, and its D
				// publishes the single Tier 1 row. Never publish a partial.
				changed = true
				continue
			}
			command := t.arbitrateSubmittedLocked(mirror, lost, typed, armed)
			if command != "" {
				if previous := t.finalizePendingLocked(now, false, 0, false); previous != nil {
					// A new Enter finalizes the previous command with an
					// unknown exit and no finish time before the next one
					// starts.
					completions = append(completions, *previous)
				}
				t.command = CommandState{
					Text:      command,
					Running:   true,
					StartedAt: now,
				}
				t.startPendingLocked(command, now)
				if t.hookExpected && !t.hookSeen {
					// A planned hook has not spoken yet: the shell's own
					// prompt marks (from before the hook's first handshake)
					// must not finalize this line.
					t.pending.awaitingHook = true
				}
				// Only output after the command starts can carry its prompt;
				// the previous prompt may still be in the tail.
				t.outputSinceStart = false
				t.stats.Commands++
				started = true
			}
			changed = true
		default:
			if value >= 0x20 {
				// The first keystroke of a pristine line is the one moment the
				// screen provably shows the bare prompt: the echo of this very
				// byte cannot have rendered yet, because it has not even reached
				// the shell. Capturing the prompt at Enter is unreliable — fast
				// input outruns the echo round-trip, so the screen then lags the
				// mirror — but here the line is still exactly the prompt. This
				// is the natcatty answer to the original's prompt cache. The
				// pristine gate excludes a line erased back to empty, whose
				// erase echo can still be in flight: capturing then would
				// snapshot the half-erased residue as chrome.
				if typed && t.inputPristine && !t.inputLost {
					t.captureEmptyPromptLocked()
				}
				t.inputPristine = false
				changed = t.appendInputByte(value) || changed
			}
		}
	}
	return changed, started, completions
}

// captureEmptyPromptLocked records the bare prompt as the prompt prefix. It
// reads the rendered line at the first keystroke of a pristine line, so
// everything up to the cursor is prompt chrome. The cursor sits at the
// input-start position there, and a prompt's trailing space is before it, so
// the text up to the cursor is exactly the prefix; the renderer's padding
// beyond the cursor never joins it. Reading only at a pristine first
// keystroke keeps the capture free of echo — the byte has not reached the
// shell, and no erasure echo can still be in flight — so it never mistakes
// input for chrome. The caller holds mu.
func (t *terminalTap) captureEmptyPromptLocked() {
	if t.submittedInput == nil {
		return
	}
	line, cursorOffset, ok := t.submittedInput()
	if !ok {
		return
	}
	runes := []rune(line)
	if cursorOffset <= 0 || cursorOffset > len(runes) {
		return
	}
	prefix := string(runes[:cursorOffset])
	if prefix == "" {
		return
	}
	t.promptPrefix = prefix
}

// appendInputByte records typed text one byte at a time, waiting for a
// complete UTF-8 rune before adding it to the command. A rune may be split
// across two Write calls, and appending each byte as its own character (the
// previous behaviour) turned "é" into "Ã©" in the command indicator.
func (t *terminalTap) appendInputByte(value byte) bool {
	t.inputPending = append(t.inputPending, value)
	changed := false
	for len(t.inputPending) > 0 && utf8.FullRune(t.inputPending) {
		r, size := utf8.DecodeRune(t.inputPending)
		if r == utf8.RuneError && size == 1 {
			// Preserve an invalid byte rather than silently dropping what
			// the user typed; the shell will decide what it means.
			changed = t.appendInputText(t.inputPending[:1]) || changed
		} else {
			changed = t.appendInputText([]byte(string(r))) || changed
		}
		t.inputPending = t.inputPending[size:]
	}
	return changed
}

func (t *terminalTap) appendInputText(text []byte) bool {
	if len(t.inputBuffer)+len(text) > 4096 {
		return false
	}
	// Typed text goes in at the cursor: the line editor inserts there, not at
	// the end, after the user has moved the cursor.
	offset := t.inputByteOffset(t.inputCursor)
	t.inputBuffer = t.inputBuffer[:offset] + string(text) + t.inputBuffer[offset:]
	t.inputCursor += utf8.RuneCount(text)
	return true
}

// inputRuneLength is the typed line's length in runes, the unit the cursor
// and the line editor's erase keystroke use.
func (t *terminalTap) inputRuneLength() int {
	return utf8.RuneCountInString(t.inputBuffer)
}

// inputByteOffset converts a rune offset in inputBuffer to a byte offset.
func (t *terminalTap) inputByteOffset(runes int) int {
	if runes <= 0 {
		return 0
	}
	count := 0
	for offset := range t.inputBuffer {
		if count == runes {
			return offset
		}
		count++
	}
	return len(t.inputBuffer)
}

// moveInputCursor moves the tracked line-editor cursor by runes and clamps it
// to the line. The terminal receives the same key, so this only mirrors the
// editor's state.
func (t *terminalTap) moveInputCursor(delta int) {
	t.inputCursor += delta
	if t.inputCursor < 0 {
		t.inputCursor = 0
	}
	if length := t.inputRuneLength(); t.inputCursor > length {
		t.inputCursor = length
	}
}

// deleteInputRuneBeforeCursor removes the rune to the left of the cursor, the
// operation Backspace performs.
func (t *terminalTap) deleteInputRuneBeforeCursor() bool {
	if t.inputCursor <= 0 {
		return false
	}
	start := t.inputByteOffset(t.inputCursor - 1)
	_, size := utf8.DecodeRuneInString(t.inputBuffer[start:])
	t.inputBuffer = t.inputBuffer[:start] + t.inputBuffer[start+size:]
	t.inputCursor--
	return true
}

// deleteInputRuneAtCursor removes the rune under the cursor, the operation
// the Delete key performs.
func (t *terminalTap) deleteInputRuneAtCursor() bool {
	if t.inputCursor >= t.inputRuneLength() {
		return false
	}
	start := t.inputByteOffset(t.inputCursor)
	_, size := utf8.DecodeRuneInString(t.inputBuffer[start:])
	t.inputBuffer = t.inputBuffer[:start] + t.inputBuffer[start+size:]
	return true
}

// applyInputCSI mirrors the cursor-editing control sequences a line editor
// understands. The history keys are the ones it cannot mirror: Up and Down
// replace the whole buffer with a history entry the tap never sees, so the
// capture is discarded rather than left a hybrid. Other sequences, including
// the bracketed-paste markers, leave the tracked cursor alone.
func (t *terminalTap) applyInputCSI(final byte) {
	params := t.inputCSIParams
	switch final {
	case 'D':
		t.moveInputCursor(-1)
	case 'C':
		t.moveInputCursor(1)
	case 'A', 'B':
		// Up/Down recall history in every shell line editor (readline, zle,
		// PSReadLine, and the bare shell's own). A modifier form (CSI 1;5A and
		// friends) is still a history or word motion the tap cannot model.
		t.invalidateInputLocked()
	case 'H':
		t.inputCursor = 0
	case 'F':
		t.inputCursor = t.inputRuneLength()
	case '~':
		switch firstCSIParameter(params) {
		case "1", "7":
			t.inputCursor = 0
		case "4", "8":
			t.inputCursor = t.inputRuneLength()
		case "3":
			t.deleteInputRuneAtCursor()
		}
	}
}

// invalidateInputLocked drops the tracked input after the line editor changed
// it in a way the tap cannot mirror. The buffer and cursor are kept for the
// autocomplete popup — the text still on screen is a better completion target
// than nothing — but Enter will not record the hybrid as a command. Typing
// more characters does not lift the invalidation: the recalled base stays
// unknown, so the line never becomes traceable again in this capture.
func (t *terminalTap) invalidateInputLocked() {
	t.inputLost = true
}

// arbitrateSubmittedLocked reconciles the keystroke mirror with the input the
// shell actually shows on the cursor's line, returning the command Enter is
// about to submit. The mirror is the most current witness when it is reliable
// — echo can lag behind fast typing on a slow link, so the screen then trails
// the buffer — but it goes blind when the line editor rewrites the line in a
// way the tap cannot mirror: a history recall or reverse search swaps in text
// the tap never sees, and a completion (Tab) grows it silently, because the
// Tab byte is neither mirrored nor invalidating. Then the rendered line is
// the truth.
//
// The reconciliation, when both witnesses speak:
//   - agreement: either wins.
//   - the mirror is a strict prefix or suffix fragment of the rendered input:
//     the editor grew the line around text the tap mirrored — completion is
//     the common case — so the rendered line is the truth.
//   - otherwise the mirror wins: under echo lag the screen can only trail the
//     mirror (echo reproduces the keystrokes in order), so a rendered line
//     that is no growth of the mirror is the stale one.
//
// A hybrid the fragment test cannot recognize — completion accepted and then
// typed over, so the mirror diverges from the line mid-way — still records
// the mirror: erase echo in flight can produce the same shape, and the two
// are indistinguishable at Enter. That residue predates the fallback and is
// no worse than it.
//
// The rendered line carries the prompt chrome, which the tap strips with the
// prompt prefix it captured at the first keystroke of a fresh line. mirror is
// the trimmed keystroke buffer; lost reports an untraceable edit; typed is
// false for a delivered copy (broadcast, compose, insert, startup), which
// never consults the screen. The reader is consulted at most once, here, and
// never under a hook-owned or awaiting-hook line (those return before this
// point). The caller holds mu.
func (t *terminalTap) arbitrateSubmittedLocked(mirror string, lost bool, typed bool, armed bool) string {
	if !typed {
		// A delivered copy has no screen moment of its own: the mirror is the
		// only witness that may speak for it, and the recall discard applies
		// to it exactly as if no reader existed.
		if mirror != "" && !lost {
			return mirror
		}
		return ""
	}
	input, rendered := t.renderedInputLocked()
	if mirror != "" && !lost {
		if rendered && input != "" && input != mirror && armed && growthOf(input, mirror) {
			// The line editor grew the text the tap mirrored — the completion
			// the Tab byte never told the mirror about. The gate matters:
			// without a Tab on this line the same shape is the erase echo of
			// a longer line still in flight, where the mirror is the truth.
			return input
		}
		// Otherwise the mirror wins: it is ahead of any lagging echo, and
		// nothing the editor did went unmirrored. Echo lag explains every
		// other shape the screen can show — trailing behind the mirror, no
		// echo at all yet, a redrawn chrome the prefix no longer matches — so
		// the more current witness speaks. This is the common case.
		return mirror
	}
	// From here the mirror is either empty or a lost hybrid. Neither may be
	// recorded as-is: the hybrid is a command that never ran, and empty is
	// nothing. The only thing that can rescue the capture is the rendered line
	// minus the prompt; without it the recall discard stands, because no row is
	// better than a wrong row.
	if !rendered || input == "" {
		// An empty input is also what a no-echo password prompt looks like, and
		// a line that does not start with the captured prompt is a redraw the
		// capture cannot trust. Never record either, and never surface the
		// secret the mirror may still hold — the discard is the safe answer.
		return ""
	}
	// The mirror is empty or a lost hybrid, and the rendered line minus the
	// prompt is the command about to run: a recall repainted it whole.
	return input
}

// growthOf reports that mirror is a strict prefix or suffix fragment of the
// rendered input: everything the tap mirrored is still there, but the line
// editor added text the tap never saw.
func growthOf(input, mirror string) bool {
	if len(input) <= len(mirror) {
		return false
	}
	return strings.HasPrefix(input, mirror) || strings.HasSuffix(input, mirror)
}

// renderedInputLocked reads the rendered line once and strips the captured
// prompt chrome, returning the input the line editor is showing. rendered is
// false when there is no trustworthy line to read: no reader, no captured
// prompt prefix, the cursor off the prompt rows, or a line that does not
// start with the captured chrome. The caller holds mu.
func (t *terminalTap) renderedInputLocked() (input string, rendered bool) {
	if t.submittedInput == nil || t.promptPrefix == "" {
		return "", false
	}
	line, _, ok := t.submittedInput()
	if !ok {
		return "", false
	}
	return t.stripPromptPrefixLocked(strings.TrimRight(line, " \t")), true
}

// stripPromptPrefixLocked removes the captured prompt chrome from the rendered
// line, returning the input the line editor is showing. It returns "" when the
// line does not start with the captured prefix (a prompt that changed shape, or
// input echoing a different program's prompt) or when nothing but the prompt is
// on the line. The caller holds mu.
func (t *terminalTap) stripPromptPrefixLocked(line string) string {
	if !strings.HasPrefix(line, t.promptPrefix) {
		return ""
	}
	return strings.TrimSpace(line[len(t.promptPrefix):])
}

// firstCSIParameter is the first numeric parameter of a CSI sequence, for the
// "~"-terminated keys where the number selects the key.
func firstCSIParameter(params []byte) string {
	end := 0
	for end < len(params) && params[end] >= '0' && params[end] <= '9' {
		end++
	}
	return string(params[:end])
}

// cancelTransfer aborts the gate from the UI. Protocol bytes after it are no
// longer suppressed.
func (t *terminalTap) cancelTransfer(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.transfer.Active {
		return false
	}
	t.transfer.Active = false
	t.transfer.FinishedAt = now
	t.transfer.End = TransferEndCancelled
	t.buffer = nil
	return true
}

func (t *terminalTap) completeTransfer(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.transfer.Active {
		return false
	}
	t.transfer.Active = false
	t.transfer.FinishedAt = now
	t.transfer.End = TransferEndCompleted
	return true
}

func (t *terminalTap) state(now time.Time) tapState {
	t.mu.Lock()
	defer t.mu.Unlock()
	state := tapState{
		CWD:            t.cwd,
		Title:          t.title,
		Command:        t.command,
		Input:          t.inputBuffer,
		InputCursor:    t.inputCursor,
		HookActive:     t.hookActive,
		BracketedPaste: t.bracketedPaste,
		Busy:           t.busyLocked(now),
		LastActivity:   t.lastOutput,
		Transfer:       t.transfer,
		Stats:          t.stats,
	}
	if t.notification != nil {
		copyNotification := *t.notification
		state.Notification = &copyNotification
	}
	if t.clipboard != nil {
		copyClipboard := *t.clipboard
		state.Clipboard = &copyClipboard
	}
	return state
}

func (t *terminalTap) busyLocked(now time.Time) bool {
	if t.command.Running || t.transfer.Active {
		return true
	}
	return !t.lastOutput.IsZero() && now.Sub(t.lastOutput) < activityWindow
}

func (t *terminalTap) acknowledgeActivity() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastOutput.IsZero() {
		return false
	}
	t.lastOutput = time.Time{}
	t.tail = nil
	return true
}

// addInputBytes counts input queued for the transport. The Session calls it
// after a write is accepted, so a rejected write is not counted.
func (t *terminalTap) addInputBytes(count int) {
	if count <= 0 {
		return
	}
	t.mu.Lock()
	t.stats.InputBytes += int64(count)
	t.mu.Unlock()
}
