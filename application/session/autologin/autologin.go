// Package autologin is the expect engine that answers a console's login and
// password prompts. It is pure state: callers feed it decoded output and the
// bytes the user typed, and it returns the lines to write. It never logs, so a
// password or a configured reply cannot leak into a Session log.
//
// The behavior follows the original Electron detector (telnetAutoLogin.cjs):
// prompts are matched on the last output line, ANSI styling is ignored, a
// "Press RETURN" banner is answered once, and a naked Enter used to wake a
// silent serial console does not disarm the detector. This port adds an
// attempt limit, so a device that keeps rejecting the saved credentials is left
// at its prompt instead of being answered forever.
package autologin

import (
	"regexp"
	"strings"
	"sync"
)

// DefaultAttempts bounds how many times the saved credentials are sent when no
// number is configured.
const DefaultAttempts = 3

// tailLimit bounds the retained output. A prompt is a short line at the end,
// so older bytes are dropped without losing the match.
const tailLimit = 2048

var (
	ansiPattern = regexp.MustCompile(`\x1b([@-Z\\_-]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*(\x07|\x1b\\))`)
	// credentialBanner recognizes a MOTD or login banner that happens to
	// contain a credential word, such as "Last login: user". Such a line must
	// not consume an attempt or answer like a prompt: the real "login:" often
	// follows it on the next line. The patterns are deliberately about banner
	// phrasing ("last login", "password expires") so a bare "login:" or
	// "Password:" prompt is still answered.
	credentialBanner = regexp.MustCompile(`(?i)(^|[^a-z0-9])((last|previous|failed|successful)\s+(login|logon)|(last|previous)\s+password|password\s+(expires?|expired|will\s+expire|change\s+required)|change\s+your\s+password)`)
	usernamePrompt   = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(user\s*name|username|login|logon|account|userid|user\s*id|user|用户名|帐号|账号|登录|登入)(\s*[:：>])?\s*$`)
	passwordPrompt   = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9])(password|passwd|passcode|passphrase|pass\s*phrase|pin|密码|口令)(\s*[:：>])?\s*$`)
	continuePrompt   = regexp.MustCompile(`(?i)(press|hit)\s+([<\[(]?\s*)?(return|enter|any\s+key|space)\b(\s*[>\]\)])?.*(continue|get\s+started|start|begin|started)?\.?\s*$`)
	commandPrompt    = regexp.MustCompile(`[$#>]\s*$`)
	wakeOnly         = regexp.MustCompile(`^[\r\n]+$`)
)

// Reply is one user-configured expect/reply pair. Expect is matched against
// the last output line, case-insensitively and as a substring, before the
// built-in login prompts. Respond is written with a carriage return.
type Reply struct {
	Expect  string
	Respond string
}

// Options configures one detector. Username and Password are saved
// credentials; PasswordSet distinguishes a deliberately empty password from no
// password at all. Replies adds custom prompts. A zero MaxAttempts uses
// DefaultAttempts. Write receives each line, already terminated with a
// carriage return.
type Options struct {
	Username    string
	Password    string
	PasswordSet bool
	Replies     []Reply
	MaxAttempts int
	Write       func([]byte)
}

// Engine is one session generation's detector. Its methods are safe for
// concurrent use by the output reader and the input path.
type Engine struct {
	mu          sync.Mutex
	username    string
	password    string
	hasPassword bool
	replies     []Reply
	maxAttempts int
	write       func([]byte)

	tail      string
	scanFrom  int
	sentWake  bool
	sentUser  bool
	sentPass  bool
	attempts  int
	fired     []bool
	active    bool
	completed bool
	pending   [][]byte
}

// New returns a detector armed when any credential or expect/reply rule is
// configured. A replies-only profile is a valid automation on its own: a
// console that asks a custom question and has no login prompt still needs its
// answer.
func New(options Options) *Engine {
	maxAttempts := options.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultAttempts
	}
	engine := &Engine{
		username:    strings.TrimSpace(options.Username),
		password:    options.Password,
		hasPassword: options.PasswordSet || options.Password != "",
		replies:     append([]Reply(nil), options.Replies...),
		maxAttempts: maxAttempts,
		write:       options.Write,
		fired:       make([]bool, len(options.Replies)),
	}
	engine.active = engine.username != "" || engine.hasPassword || hasReply(engine.replies)
	return engine
}

// hasReply reports whether at least one rule can match. An empty Expect is
// inert and is ignored by matchReply.
func hasReply(replies []Reply) bool {
	for _, reply := range replies {
		if strings.TrimSpace(reply.Expect) != "" {
			return true
		}
	}
	return false
}

// Active reports whether the detector can still answer a prompt.
func (e *Engine) Active() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

// Completed reports that credentials were sent and the device reached a
// command prompt. It is false when the detector stopped at its attempt limit or
// a user took over.
func (e *Engine) Completed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.completed
}

// Feed consumes one decoded output chunk.
func (e *Engine) Feed(text string) {
	if text == "" {
		return
	}
	e.mu.Lock()
	if !e.active {
		e.mu.Unlock()
		return
	}
	e.append(text)
	e.advance()
	pending := e.drain()
	e.mu.Unlock()
	e.emit(pending)
}

// UserInput reports bytes the user typed in this Session. A naked Enter or
// newline that merely wakes a silent console keeps the detector armed before
// any credentials were sent; any other input is a takeover and disarms it.
func (e *Engine) UserInput(data []byte) {
	if len(data) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.active {
		return
	}
	if wakeOnly.Match(data) && !e.sentUser && !e.sentPass &&
		!isUsernamePrompt(e.tail) && !isPasswordPrompt(e.tail) {
		return
	}
	e.active = false
}

// Takeover disarms the detector for input that did not come from this
// Session's own keyboard, such as a broadcast or an inserted snippet. Unlike
// UserInput it never treats a naked Enter as a harmless wake: bytes delivered
// by those routes are deliberate, so they always transfer control to the user.
func (e *Engine) Takeover() {
	e.mu.Lock()
	e.active = false
	e.pending = nil
	e.mu.Unlock()
}

// Cancel disarms the detector without writing anything, as when its Session is
// closed or replaced by a reconnect.
func (e *Engine) Cancel() {
	e.mu.Lock()
	e.active = false
	e.pending = nil
	e.mu.Unlock()
}

func (e *Engine) append(text string) {
	e.tail += text
	if len(e.tail) <= tailLimit {
		return
	}
	cut := len(e.tail) - tailLimit
	e.tail = e.tail[cut:]
	e.scanFrom -= cut
	if e.scanFrom < 0 {
		e.scanFrom = 0
	}
}

// advance answers every prompt visible in the newly appended output. Each
// write moves scanFrom past the prompt, so one Feed can answer a login and a
// password prompt that arrived together without matching the same text twice.
func (e *Engine) advance() {
	for e.active {
		window := e.tail[e.scanFrom:]
		if window == "" {
			return
		}
		if (e.sentUser || e.sentPass || e.firedAny()) && isCommandPrompt(e.tail) {
			e.active = false
			e.completed = true
			return
		}
		if !e.sentWake && isContinuePrompt(window) {
			e.sentWake = true
			e.send("")
			e.scanFrom = len(e.tail)
			continue
		}
		if index := e.matchReply(window); index >= 0 {
			e.fired[index] = true
			e.send(e.replies[index].Respond)
			e.scanFrom = len(e.tail)
			continue
		}
		if e.username != "" || e.hasPassword {
			lastLine := isUsernamePrompt(window)
			if lastLine || hasUsernamePromptBeforePassword(window) {
				if e.sentUser {
					if !e.beginAttempt() {
						return
					}
					e.sentUser, e.sentPass = false, false
					e.fired = make([]bool, len(e.replies))
				} else if e.attempts == 0 {
					if !e.beginAttempt() {
						return
					}
				}
				e.sentUser = true
				e.send(e.username)
				if !lastLine && e.hasPassword && !e.sentPass {
					// The chunk already contains the password prompt after the
					// username prompt, so answer both before moving past it.
					e.sentPass = true
					e.send(e.password)
				}
				e.scanFrom = len(e.tail)
				continue
			}
		}
		if e.hasPassword && isPasswordPrompt(window) {
			if e.sentPass {
				// The device asked for the password again instead of returning to
				// its login prompt. That is a rejected attempt like any other.
				if !e.beginAttempt() {
					return
				}
				e.fired = make([]bool, len(e.replies))
			}
			e.sentPass = true
			e.send(e.password)
			e.scanFrom = len(e.tail)
			continue
		}
		return
	}
}

func (e *Engine) beginAttempt() bool {
	if e.attempts >= e.maxAttempts {
		e.active = false
		return false
	}
	e.attempts++
	return true
}

// firedAny reports that at least one custom rule answered. It lets a
// replies-only profile complete when the console reaches its command prompt,
// exactly as a credential login does.
func (e *Engine) firedAny() bool {
	for _, fired := range e.fired {
		if fired {
			return true
		}
	}
	return false
}

func (e *Engine) matchReply(window string) int {
	line := lastPromptLine(window)
	if line == "" {
		return -1
	}
	lower := strings.ToLower(line)
	for index, reply := range e.replies {
		if e.fired[index] || reply.Expect == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(reply.Expect)) {
			return index
		}
	}
	return -1
}

func (e *Engine) send(value string) {
	e.pending = append(e.pending, []byte(value+"\r"))
}

func (e *Engine) drain() [][]byte {
	pending := e.pending
	e.pending = nil
	return pending
}

func (e *Engine) emit(pending [][]byte) {
	if e.write == nil {
		return
	}
	for _, line := range pending {
		e.write(line)
	}
}

// stripANSI removes terminal styling so a prompt split by color codes still
// matches. It mirrors the original detector's pattern.
func stripANSI(text string) string {
	if !strings.Contains(text, "\x1b") {
		return text
	}
	return ansiPattern.ReplaceAllString(text, "")
}

// lastPromptLine returns the final non-empty prompt line of text, bounded so a
// pasted banner cannot grow the match window without limit.
func lastPromptLine(text string) string {
	normalized := strings.ReplaceAll(stripANSI(text), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := lines[index]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > 320 {
			line = line[len(line)-320:]
		}
		return line
	}
	return ""
}

func promptLines(text string) []string {
	normalized := strings.ReplaceAll(stripANSI(text), "\r", "\n")
	return strings.Split(normalized, "\n")
}

func isContinuePrompt(text string) bool {
	return continuePrompt.MatchString(lastPromptLine(text))
}

func isUsernamePrompt(text string) bool {
	line := lastPromptLine(text)
	if credentialBanner.MatchString(line) {
		return false
	}
	return usernamePrompt.MatchString(line)
}

func isPasswordPrompt(text string) bool {
	return passwordPrompt.MatchString(lastPromptLine(text))
}

func isCommandPrompt(text string) bool {
	line := strings.TrimRight(lastPromptLine(text), " \t")
	if line == "" || len(line) > 200 {
		return false
	}
	if credentialBanner.MatchString(line) || usernamePrompt.MatchString(line) ||
		passwordPrompt.MatchString(line) || continuePrompt.MatchString(line) {
		return false
	}
	return commandPrompt.MatchString(line)
}

func hasUsernamePromptBeforePassword(text string) bool {
	lines := promptLines(text)
	lastPassword := -1
	for index, line := range lines {
		if passwordPrompt.MatchString(line) {
			lastPassword = index
		}
	}
	if lastPassword < 0 {
		return false
	}
	for _, line := range lines[:lastPassword] {
		if !credentialBanner.MatchString(line) && usernamePrompt.MatchString(line) {
			return true
		}
	}
	return false
}
