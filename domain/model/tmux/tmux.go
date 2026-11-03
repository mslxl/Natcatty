// Package tmux builds the remote commands and decisions for the per-Host SSH
// auto-attach capability. It performs no I/O, knows no transport, and produces
// no user-visible text. The SSH transport factory runs the probe on an exec
// channel of the same Connection, then starts the wrapper (or a plain login
// shell) on the terminal channel.
package tmux

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/mslxl/natcatty/domain/model"
)

// SessionPrefix starts every deterministic session name.
const SessionPrefix = "natcatty-"

// DetachedExitStatus is the shell status the wrapper reports when the tmux
// client detached cleanly and the named session is still alive. The app maps
// it to a detach instead of an ordinary shell exit, so exiting the shell
// inside tmux (which destroys the session) keeps its ended Tab.
const DetachedExitStatus = 97

// Decision is what a remote probe reports about the tmux environment.
type Decision string

const (
	// DecisionCreate means tmux is usable and no session exists yet.
	DecisionCreate Decision = "create"
	// DecisionAttach means tmux is usable and the named session exists.
	DecisionAttach Decision = "attach"
	// DecisionNested means the remote process already runs inside tmux.
	DecisionNested Decision = "nested"
	// DecisionUnavailable means tmux is missing, not executable, or the shell
	// could not run the probe.
	DecisionUnavailable Decision = "unavailable"
)

// Probe output tokens. They are deliberately outside the session-name
// alphabet so a shell startup banner cannot be mistaken for a decision.
const (
	probeCreate      = "natcatty-tmux-create"
	probeAttach      = "natcatty-tmux-attach"
	probeNested      = "natcatty-tmux-nested"
	probeUnavailable = "natcatty-tmux-unavailable"
)

// ProbeAnswer returns the stdout line a probe prints for decision. An unknown
// decision has no answer.
func ProbeAnswer(decision Decision) string {
	switch decision {
	case DecisionCreate:
		return probeCreate
	case DecisionAttach:
		return probeAttach
	case DecisionNested:
		return probeNested
	case DecisionUnavailable:
		return probeUnavailable
	default:
		return ""
	}
}

// SessionName returns the deterministic tmux session name for a Host. A Host
// always maps to the same name, so a later connection can attach to the
// session an earlier one left running.
func SessionName(hostID model.HostID) string {
	return SessionPrefix + SanitizeID(string(hostID))
}

// SanitizeID maps a Host ID onto tmux's safe session-name alphabet: ASCII
// letters, digits, '-' and '_'. Every other byte, including a literal '_', is
// replaced by '_' followed by two lowercase hexadecimal digits. The encoding
// is deterministic and injective, so two different Host IDs never share a
// session name through sanitization. The empty ID maps to '_', which no
// non-empty ID can produce because every literal '_' is escaped.
func SanitizeID(id string) string {
	if id == "" {
		return "_"
	}
	var builder strings.Builder
	builder.Grow(len(id))
	for index := 0; index < len(id); index++ {
		value := id[index]
		switch {
		case value >= 'a' && value <= 'z',
			value >= 'A' && value <= 'Z',
			value >= '0' && value <= '9',
			value == '-':
			builder.WriteByte(value)
		default:
			fmt.Fprintf(&builder, "_%02x", value)
		}
	}
	return builder.String()
}

// Quote turns a value into one POSIX shell word. Single quotes stop every
// expansion and command substitution; an embedded single quote is closed,
// escaped, and reopened. The session name is already sanitized, but quoting it
// here keeps the command construction safe on its own terms.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ProbeCommand returns the POSIX shell command the app runs over a
// non-interactive exec channel. Its stdout carries exactly one ProbeAnswer
// token for the factory to parse. The probe never starts tmux: has-session
// only inspects the existing server, and a missing server is "create".
func ProbeCommand(session string) string {
	target := Quote("=" + session)
	return strings.Join([]string{
		`if [ -n "${TMUX:-}" ]; then printf '%s\n' ` + probeNested,
		`elif ! command -v tmux >/dev/null 2>&1; then printf '%s\n' ` + probeUnavailable,
		`elif ! tmux -V >/dev/null 2>&1; then printf '%s\n' ` + probeUnavailable,
		`elif tmux has-session -t ` + target + ` >/dev/null 2>&1; then printf '%s\n' ` + probeAttach,
		`else printf '%s\n' ` + probeCreate,
	}, "; ") + "; fi"
}

// ParseProbe reads the probe's stdout. A shell startup banner may add lines,
// so unrelated lines are ignored; an empty answer, or two conflicting
// decisions, is not a usable answer. The returned bool reports whether a
// decision was found.
func ParseProbe(output string) (Decision, bool) {
	found := Decision("")
	for _, line := range strings.Split(output, "\n") {
		var decision Decision
		switch strings.TrimSpace(line) {
		case probeCreate:
			decision = DecisionCreate
		case probeAttach:
			decision = DecisionAttach
		case probeNested:
			decision = DecisionNested
		case probeUnavailable:
			decision = DecisionUnavailable
		default:
			continue
		}
		if found != "" && found != decision {
			return "", false
		}
		found = decision
	}
	return found, found != ""
}

// WrapperCommand returns the POSIX shell command started under the terminal
// PTY. It replaces itself with a plain login shell when the remote environment
// already runs inside tmux or tmux is missing, and it falls back to a plain
// login shell when tmux cannot start. The fallback means the connection never
// fails because of tmux. new-session -A creates the named session on the first
// connect and attaches to it afterwards. A clean client exit is reported as
// DetachedExitStatus only when the named session survived; otherwise the
// wrapper exits with tmux's own status.
func WrapperCommand(session string) string {
	plainShell := `exec "${SHELL:-/bin/sh}" -l`
	target := Quote("=" + session)
	return strings.Join([]string{
		`if [ -n "${TMUX:-}" ]; then ` + plainShell + `; fi`,
		`if ! command -v tmux >/dev/null 2>&1; then ` + plainShell + `; fi`,
		`tmux new-session -A -s ` + Quote(session),
		`status=$?`,
		`if [ "$status" -eq 0 ] && tmux has-session -t ` + target + ` >/dev/null 2>&1; then exit ` + strconv.Itoa(DetachedExitStatus) + `; fi`,
		`if [ "$status" -ne 0 ]; then ` + plainShell + `; fi`,
		`exit "$status"`,
	}, "; ")
}

// NoticeKind identifies a non-fatal capability notice the app may show.
type NoticeKind string

const (
	// NoticeRestored reports that the connection attached to an existing
	// tmux session.
	NoticeRestored NoticeKind = "restored"
	// NoticeNested reports that the remote was already inside tmux.
	NoticeNested NoticeKind = "nested"
	// NoticeUnavailable reports that tmux is missing or cannot run.
	NoticeUnavailable NoticeKind = "unavailable"
)

type noticeKey struct {
	hostID model.HostID
	kind   NoticeKind
}

// NoticeState remembers which fallback notices a Host has already shown, so a
// missing tmux is not announced on every reconnect. Restored notices are not
// deduplicated: every reattach reports that the session came back. A nil
// state announces everything.
type NoticeState struct {
	mu        sync.Mutex
	announced map[noticeKey]bool
}

// NewNoticeState returns an empty notice state.
func NewNoticeState() *NoticeState {
	return &NoticeState{announced: make(map[noticeKey]bool)}
}

// ShouldAnnounce reports whether a notice should be announced. Restored
// notices always are. Nested and unavailable notices are announced at most
// once per Host for the lifetime of the state.
func (s *NoticeState) ShouldAnnounce(hostID model.HostID, kind NoticeKind) bool {
	if kind == NoticeRestored {
		return true
	}
	if s == nil {
		return true
	}
	key := noticeKey{hostID: hostID, kind: kind}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.announced == nil {
		s.announced = make(map[noticeKey]bool)
	}
	if s.announced[key] {
		return false
	}
	s.announced[key] = true
	return true
}
