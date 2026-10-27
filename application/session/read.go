package session

import (
	"sort"
	"strings"

	"github.com/mslxl/natcatty/domain/model"
)

// SessionDescription is the read-only projection the external MCP surface
// exposes for one Session. It carries no output and no credential material.
type SessionDescription struct {
	ID         model.SessionID
	Label      string
	HostID     model.HostID
	Kind       Kind
	Status     Status
	Connection ConnectionStatus
	Hostname   string
	Port       int
	Username   string
	Cols       int
	Rows       int
	Title      string
	CWD        string
	Connected  bool
}

// LiveIdentity is the cheap Session identity projection ownership tracking
// consumes. Unlike SessionDescription it takes no per-Session snapshot.
type LiveIdentity struct {
	ID     model.SessionID
	HostID model.HostID
}

// LiveIdentities returns every live Session's ID and Host, ordered by ID. It
// runs on every Session change, so it deliberately avoids snapshotting.
func (s *Service) LiveIdentities() []LiveIdentity {
	s.mu.Lock()
	identities := make([]LiveIdentity, 0, len(s.sessions))
	for id, session := range s.sessions {
		identities = append(identities, LiveIdentity{ID: id, HostID: session.spec.HostID})
	}
	s.mu.Unlock()
	sort.Slice(identities, func(left, right int) bool {
		return identities[left].ID < identities[right].ID
	})
	return identities
}

// Describe returns one Session's projection. A Session that already ended is
// not described.
func (s *Service) Describe(id model.SessionID) (SessionDescription, bool) {
	s.mu.Lock()
	session, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return SessionDescription{}, false
	}
	return describe(session), true
}

// DescribeAll returns every live Session ordered by ID, so the external MCP
// listing is stable across calls.
func (s *Service) DescribeAll() []SessionDescription {
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()

	descriptions := make([]SessionDescription, 0, len(sessions))
	for _, session := range sessions {
		descriptions = append(descriptions, describe(session))
	}
	sort.Slice(descriptions, func(left, right int) bool {
		return descriptions[left].ID < descriptions[right].ID
	})
	return descriptions
}

func describe(session *Session) SessionDescription {
	snapshot := session.Snapshot()
	return SessionDescription{
		ID:         session.id,
		Label:      session.Label(),
		HostID:     session.spec.HostID,
		Kind:       session.spec.Kind,
		Status:     snapshot.Status,
		Connection: snapshot.Connection,
		Hostname:   session.spec.Hostname,
		Port:       session.spec.Port,
		Username:   session.spec.Username,
		Cols:       snapshot.Cols,
		Rows:       snapshot.Rows,
		Title:      snapshot.Title,
		CWD:        snapshot.CWD,
		// A reconnecting Session keeps its entry and stays visible, matching
		// the original's connected:false/true behavior: the agent should wait
		// on the existing ID instead of opening a duplicate tab.
		Connected: snapshot.Status == StatusRunning && snapshot.Connection != ConnectionStatusError,
	}
}

// ScreenText returns one Session's plain-text screen snapshot. The boolean is
// false once the Session is no longer registered.
func (s *Service) ScreenText(id model.SessionID) (ScreenTextSnapshot, bool) {
	s.mu.Lock()
	session, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return ScreenTextSnapshot{}, false
	}
	return session.ScreenText(), true
}

// ScreenTextSnapshot is a Session's replayable screen rendered as plain text,
// with the window size needed to derive a viewport.
type ScreenTextSnapshot struct {
	SessionID       model.SessionID
	Text            string
	Cols            int
	Rows            int
	AlternateScreen bool
}

// ScreenText captures the Session's bounded screen replay and removes terminal
// control sequences so line-oriented context reads see text, not escape
// codes. It never consumes the live view's output queue.
func (s *Session) ScreenText() ScreenTextSnapshot {
	observation, err := s.Observe()
	if err != nil {
		snapshot := s.Snapshot()
		return ScreenTextSnapshot{SessionID: s.id, Cols: snapshot.Cols, Rows: snapshot.Rows}
	}
	screen := observation.Screen()
	_ = observation.Close()

	raw := string(screen.Data)
	return ScreenTextSnapshot{
		SessionID:       s.id,
		Text:            stripTerminalControl(raw),
		Cols:            screen.Cols,
		Rows:            screen.Rows,
		AlternateScreen: usesAlternateScreen(raw),
	}
}

// stripTerminalControl removes CSI, OSC, DCS, and single-character escape
// sequences, normalizes carriage returns to newlines, and drops other control
// bytes. It is deliberately a text extractor, not a full terminal emulator:
// the replayed stream has already been rendered into a line buffer by the
// Session, and escape codes would otherwise become the agent's content.
func stripTerminalControl(raw string) string {
	var builder strings.Builder
	builder.Grow(len(raw))
	for index := 0; index < len(raw); {
		character := raw[index]
		switch {
		case character == 0x1b:
			index = skipEscape(raw, index)
		case character == '\r':
			builder.WriteByte('\n')
			index++
		case character == '\n' || character == '\t':
			builder.WriteByte(character)
			index++
		case character < 0x20 || character == 0x7f:
			index++
		default:
			builder.WriteByte(character)
			index++
		}
	}
	return builder.String()
}

// skipEscape returns the index just past one escape sequence starting at
// start, where raw[start] is ESC.
func skipEscape(raw string, start int) int {
	if start+1 >= len(raw) {
		return len(raw)
	}
	switch raw[start+1] {
	case '[':
		// CSI: parameter and intermediate bytes, then one final byte 0x40-0x7E.
		for index := start + 2; index < len(raw); index++ {
			if raw[index] >= 0x40 && raw[index] <= 0x7e {
				return index + 1
			}
		}
		return len(raw)
	case ']':
		// OSC: terminated by BEL or by ST (ESC \).
		for index := start + 2; index < len(raw); index++ {
			if raw[index] == 0x07 {
				return index + 1
			}
			if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
				return index + 2
			}
		}
		return len(raw)
	case 'P', '^', '_':
		// DCS, PM, and APC also end at ST.
		for index := start + 2; index < len(raw); index++ {
			if raw[index] == 0x1b && index+1 < len(raw) && raw[index+1] == '\\' {
				return index + 2
			}
		}
		return len(raw)
	default:
		return start + 2
	}
}

// usesAlternateScreen reports whether the replay entered an alternate screen.
func usesAlternateScreen(raw string) bool {
	return strings.Contains(raw, "\x1b[?1049h") ||
		strings.Contains(raw, "\x1b[?1047h") ||
		strings.Contains(raw, "\x1b[?47h")
}

// TerminalLines splits context text exactly as the original did: CRLF and bare
// CR are line breaks, and an empty text has no lines.
func TerminalLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// TerminalContextRange is the original's terminal slice selector.
type TerminalContextRange string

const (
	TerminalRangeViewport TerminalContextRange = "viewport"
	TerminalRangeTail     TerminalContextRange = "tail"
	TerminalRangeHead     TerminalContextRange = "head"
	TerminalRangeLines    TerminalContextRange = "lines"
)

// Bounds match the original terminal context read.
const (
	TerminalContextDefaultMaxLines = 80
	TerminalContextMaxLines        = 300
)

// TerminalLineWindow is the inclusive zero-based line window a context read
// returns.
type TerminalLineWindow struct {
	StartLine int
	EndLine   int
	MaxLines  int
}

// ResolveTerminalLineWindow chooses the window for a range. The arithmetic is
// the original's, including the inclusive end line and the -1 end of an empty
// buffer.
func ResolveTerminalLineWindow(rangeName TerminalContextRange, totalLines int,
	viewportStart, viewportEnd, startLine, maxLines int) TerminalLineWindow {
	normalizedRange := NormalizeTerminalRange(string(rangeName))
	lines := NormalizeTerminalMaxLines(maxLines)
	if totalLines < 0 {
		totalLines = 0
	}
	if totalLines == 0 {
		return TerminalLineWindow{StartLine: 0, EndLine: -1, MaxLines: lines}
	}
	switch normalizedRange {
	case TerminalRangeHead:
		return TerminalLineWindow{StartLine: 0, EndLine: min(totalLines-1, lines-1), MaxLines: lines}
	case TerminalRangeTail:
		return TerminalLineWindow{StartLine: max(0, totalLines-lines), EndLine: totalLines - 1, MaxLines: lines}
	case TerminalRangeLines:
		start := clampTerminalLine(startLine, totalLines)
		return TerminalLineWindow{StartLine: start, EndLine: min(totalLines-1, start+lines-1), MaxLines: lines}
	default:
		viewportFirst := clampTerminalLine(viewportStart, totalLines)
		viewportLast := clampTerminalLine(viewportEnd, totalLines)
		rawStart := min(viewportFirst, viewportLast)
		end := max(viewportFirst, viewportLast)
		start := max(rawStart, end-lines+1)
		return TerminalLineWindow{StartLine: start, EndLine: end, MaxLines: lines}
	}
}

// NormalizeTerminalRange accepts the four known selectors and otherwise
// defaults to viewport.
func NormalizeTerminalRange(value string) TerminalContextRange {
	switch TerminalContextRange(value) {
	case TerminalRangeViewport, TerminalRangeTail, TerminalRangeHead, TerminalRangeLines:
		return TerminalContextRange(value)
	default:
		return TerminalRangeViewport
	}
}

// NormalizeTerminalMaxLines rounds and clamps a requested line count.
func NormalizeTerminalMaxLines(value int) int {
	if value <= 0 {
		return TerminalContextDefaultMaxLines
	}
	return min(value, TerminalContextMaxLines)
}

func clampTerminalLine(value, totalLines int) int {
	if totalLines <= 0 {
		return 0
	}
	if value < 0 {
		return 0
	}
	return min(totalLines-1, value)
}
