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
	// PromptRecheck asks the Session for one deferred prompt check after the
	// guard window: a command is still running with output after it started,
	// but its chunk did not settle it, and no further output may arrive to
	// trigger another check. The check only finishes a prompt-like tail.
	PromptRecheck bool
}

// tapState is the immutable per-chunk view the Session copies into Snapshot.
type tapState struct {
	CWD          string
	Title        string
	Command      CommandState
	Input        string
	Busy         bool
	LastActivity time.Time
	Notification *TerminalNotification
	Clipboard    *ClipboardEvent
	Transfer     TransferState
	Stats        Stats
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

	cwd          string
	title        string
	command      CommandState
	inputBuffer  string
	inputPending []byte
	inputEscape  inputState
	lastOutput   time.Time
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
	}
}

func (t *terminalTap) setClipboardPolicy(policy domainterminal.ClipboardAccess) {
	t.mu.Lock()
	t.policy = policy
	t.mu.Unlock()
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
	if t.checkPrompt(now) == promptFinished {
		effects.Changed = true
	}
	if t.command.Running && t.outputSinceStart {
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
	if t.command.Running {
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
		t.cwd = parseOSC7(string(rest))
		return true, tapEffects{Changed: true}
	case 52:
		return t.handleOSC52(string(rest), now), tapEffects{Changed: true}
	case 133:
		changed := t.handleOSC133(string(rest), now)
		return true, tapEffects{Changed: changed}
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

func (t *terminalTap) handleOSC133(rest string, now time.Time) bool {
	if rest == "" {
		return false
	}
	kind := rest[0]
	switch kind {
	case 'A':
		if t.command.Running {
			t.command.Running = false
			t.command.FinishedAt = now
		}
		return true
	case 'B':
		t.command.Running = false
		return true
	case 'C':
		t.command.Running = true
		if t.command.StartedAt.IsZero() {
			t.command.StartedAt = now
		}
		t.command.FinishedAt = time.Time{}
		// Only output after the command starts can carry its prompt.
		t.outputSinceStart = false
		return true
	case 'D':
		t.command.Running = false
		t.command.FinishedAt = now
		if len(rest) > 2 && rest[1] == ';' {
			if code, err := parseInt(rest[2:]); err == nil {
				t.command.ExitCode = code
				t.command.HasExitCode = true
				if code != 0 {
					t.stats.FailedCommands++
				}
			}
		}
		return true
	default:
		return false
	}
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
		number = number*10 + int(value[index]-'0')
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
// already in the tail from before Enter belongs to the previous command.
func (t *terminalTap) checkPrompt(now time.Time) promptResult {
	if !t.command.Running || !t.outputSinceStart || len(t.tail) == 0 {
		return promptNoChange
	}
	line := lastVisibleLine(t.tail)
	if line == "" || !promptLike(line) {
		return promptNoChange
	}
	if !t.command.StartedAt.IsZero() && now.Sub(t.command.StartedAt) < promptWindow {
		return promptDeferred
	}
	t.command.Running = false
	t.command.FinishedAt = now
	return promptFinished
}

// resolvePrompt re-runs the prompt fallback once the guard window has expired.
// A fast command's prompt can arrive while checkPrompt is still holding back,
// and with no further output nothing else would revisit it, leaving Running
// set and autocomplete suppressed until Ctrl+C.
func (t *terminalTap) resolvePrompt(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.checkPrompt(now) == promptFinished
}

func lastVisibleLine(data []byte) string {
	clean := stripTerminalControls(data)
	clean = strings.ReplaceAll(clean, "\r", "\n")
	lines := strings.Split(clean, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.TrimSpace(lines[index]) != "" {
			return strings.TrimRight(lines[index], " \t")
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
// the submitted command if Enter completed one, and the transfer state change.
// During a transfer all normal input is dropped; Ctrl+C aborts with the
// original five CAN bytes.
func (t *terminalTap) handleInput(data []byte, now time.Time) ([]byte, bool, string, *TransferState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.transfer.Active {
		if bytes.IndexByte(data, 0x03) >= 0 {
			t.transfer.Active = false
			t.transfer.FinishedAt = now
			t.transfer.End = TransferEndCancelled
			t.buffer = nil
			copyState := t.transfer
			return []byte{0x18, 0x18, 0x18, 0x18, 0x18}, true, "", &copyState
		}
		return nil, false, "", nil
	}
	changed, submitted := t.recordInput(data, now)
	return data, changed, submitted, nil
}

func (t *terminalTap) recordInput(data []byte, now time.Time) (bool, string) {
	changed := false
	submitted := ""
	for _, value := range data {
		switch t.inputEscape {
		case inputEscape:
			t.inputEscape = inputPlain
			if value == '[' {
				t.inputEscape = inputCSI
			} else if value == ']' {
				t.inputEscape = inputOSC
			}
			continue
		case inputCSI:
			if value >= 0x40 && value <= 0x7e {
				t.inputEscape = inputPlain
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
			t.inputPending = nil
			if t.command.Running {
				t.command.Running = false
				t.command.FinishedAt = now
			}
			changed = true
		case 0x15:
			t.inputBuffer = ""
			t.inputPending = nil
			changed = true
		case 0x7f, 0x08:
			t.inputPending = nil
			if t.inputBuffer != "" {
				_, size := utf8.DecodeLastRuneInString(t.inputBuffer)
				t.inputBuffer = t.inputBuffer[:len(t.inputBuffer)-size]
				changed = true
			}
		case '\r', '\n':
			command := strings.TrimSpace(t.inputBuffer)
			t.inputBuffer = ""
			t.inputPending = nil
			if command != "" {
				t.command = CommandState{
					Text:      command,
					Running:   true,
					StartedAt: now,
				}
				// Only output after the command starts can carry its prompt;
				// the previous prompt may still be in the tail.
				t.outputSinceStart = false
				t.stats.Commands++
				submitted = command
			}
			changed = true
		default:
			if value >= 0x20 {
				changed = t.appendInputByte(value) || changed
			}
		}
	}
	return changed, submitted
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
	t.inputBuffer += string(text)
	return true
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
		CWD:          t.cwd,
		Title:        t.title,
		Command:      t.command,
		Input:        t.inputBuffer,
		Busy:         t.busyLocked(now),
		LastActivity: t.lastOutput,
		Transfer:     t.transfer,
		Stats:        t.stats,
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
