// Package mosh holds the Mosh transport vocabulary the app shares with its
// adapters and UI: the SSH bootstrap command, the MOSH CONNECT parser, and the
// mosh-client invocation.
//
// It mirrors the original Netcatty Mosh wrapper: the bootstrap runs
// `mosh-server new` over an SSH channel, the server announces itself with
// `MOSH CONNECT <port> <key>`, and the local Session swaps to the bundled
// mosh-client with that key. The parser is deliberately tolerant of CSI/OSC
// escapes and chunk boundaries because a Windows pseudo-terminal can inject
// them around the marker.
package mosh

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// DefaultServerCommand is the remote mosh-server invocation. `-s` keeps the
// server alive while the launcher exits; `-c 256` makes the server advertise
// xterm-256color, matching the stock mosh wrapper.
const DefaultServerCommand = "mosh-server new -s -c 256"

// DefaultLang is the locale offered when the client has none.
const DefaultLang = "en_US.UTF-8"

// DefaultTerm is the terminfo entry mosh-client uses locally.
const DefaultTerm = "xterm-256color"

// Protocol markers are internal to the bootstrap and hidden from the terminal.
const (
	connectMarker = "MOSH CONNECT"
	ipMarker      = "MOSH IP"
)

// Errors the transport and the UI classify without parsing text.
var (
	// ErrInvalidConfig reports an unusable Mosh configuration.
	ErrInvalidConfig = errors.New("mosh: invalid configuration")
	// ErrJumpUnsupported reports a Host route that uses a jump chain. Mosh
	// bootstraps its own SSH session and the original refused chains.
	ErrJumpUnsupported = errors.New("mosh: jump host chains are not supported")
	// ErrProxyUnsupported reports a Host route that uses a proxy. Mosh cannot
	// carry the proxy through to its UDP channel.
	ErrProxyUnsupported = errors.New("mosh: proxy connections are not supported")
	// ErrHandshake reports that the server never announced MOSH CONNECT.
	ErrHandshake = errors.New("mosh: no MOSH CONNECT line from mosh-server")
	// ErrBundleUnavailable reports a missing bundled mosh-client.
	ErrBundleUnavailable = errors.New("mosh: bundled mosh-client is not available")
	// ErrChecksumMismatch reports a bundled mosh-client that failed SHA-256
	// verification.
	ErrChecksumMismatch = errors.New("mosh: bundled mosh-client failed checksum verification")
	// ErrPlatformUnsupported reports a platform with no MoshCatty build.
	ErrPlatformUnsupported = errors.New("mosh: this platform has no bundled mosh-client")
)

// ValidationError reports the configuration field that is unusable.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("mosh: %s %s", e.Field, e.Reason)
}

// Is makes every validation failure match ErrInvalidConfig.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidConfig }

// Config is one Mosh Session's bootstrap and client settings.
type Config struct {
	// Hostname, Port, and Username address the SSH bootstrap. Port defaults to
	// 22.
	Hostname string
	Port     int
	Username string
	// ServerPath overrides the remote mosh-server binary. Empty uses the
	// remote PATH.
	ServerPath string
	// Lang is the UTF-8 locale offered to mosh-server when no locale variable
	// is inherited.
	Lang string
	// Locales carries the client's locale environment in stock mosh order. It
	// may be nil.
	Locales map[string]string
	// Cols and Rows size the local mosh-client.
	Cols, Rows int
}

// Normalized fills the defaults a blank form implies.
func (c Config) Normalized() Config {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.Username = strings.TrimSpace(c.Username)
	c.ServerPath = strings.TrimSpace(c.ServerPath)
	c.Lang = strings.TrimSpace(c.Lang)
	if c.Port == 0 {
		c.Port = 22
	}
	if c.Lang == "" {
		c.Lang = DefaultLang
	}
	if c.Locales != nil {
		trimmed := make(map[string]string, len(c.Locales))
		for name, value := range c.Locales {
			if value = strings.TrimSpace(value); value != "" {
				trimmed[name] = value
			}
		}
		c.Locales = trimmed
	}
	return c
}

// Validate checks that a normalized configuration can be launched.
func (c Config) Validate() error {
	if c.Hostname == "" {
		return &ValidationError{Field: "hostname", Reason: "is required"}
	}
	if c.Port < 1 || c.Port > 65535 {
		return &ValidationError{Field: "port", Reason: "is out of range"}
	}
	if c.Cols < 0 || c.Rows < 0 {
		return &ValidationError{Field: "size", Reason: "is out of range"}
	}
	return nil
}

// Target returns the SSH destination for the bootstrap.
func (c Config) Target() string {
	if c.Username != "" {
		return c.Username + "@" + c.Hostname
	}
	return c.Hostname
}

// ShellQuote quotes a value for a POSIX shell.
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// ServerCommand returns the remote mosh-server invocation, with the configured
// path quoted when one is set.
func ServerCommand(serverPath string) string {
	trimmed := strings.TrimSpace(serverPath)
	if trimmed == "" {
		return DefaultServerCommand
	}
	return ShellQuote(trimmed) + " new -s -c 256"
}

// localeNames is the stock mosh order for locale handoff.
var localeNames = []string{
	"LANG",
	"LANGUAGE",
	"LC_CTYPE",
	"LC_NUMERIC",
	"LC_TIME",
	"LC_COLLATE",
	"LC_MONETARY",
	"LC_MESSAGES",
	"LC_PAPER",
	"LC_NAME",
	"LC_ADDRESS",
	"LC_TELEPHONE",
	"LC_MEASUREMENT",
	"LC_IDENTIFICATION",
	"LC_ALL",
}

// RemoteScript builds the POSIX sh script the SSH channel runs. It reports the
// address SSH actually reached so the UDP channel targets the same endpoint,
// then replaces itself with mosh-server.
func (c Config) RemoteScript() string {
	localeAssignments := make([]string, 0, len(localeNames))
	for _, name := range localeNames {
		if value, ok := c.Locales[name]; ok {
			localeAssignments = append(localeAssignments, name+"="+value)
		}
	}
	if len(localeAssignments) == 0 {
		localeAssignments = append(localeAssignments, "LANG="+c.Lang)
	}
	var localeArgs strings.Builder
	for _, assignment := range localeAssignments {
		localeArgs.WriteString(" -l ")
		localeArgs.WriteString(ShellQuote(assignment))
	}
	return `if [ -n "$SSH_CONNECTION" ]; then set -- $SSH_CONNECTION; printf '\nMOSH IP %s\n' "$3"; fi; exec ` +
		ServerCommand(c.ServerPath) + localeArgs.String()
}

// RemoteCommand wraps RemoteScript in the explicit POSIX shell invocation. A
// login shell that is not sh-compatible must not interpret the script.
func (c Config) RemoteCommand() string {
	return "sh -c " + ShellQuote(c.RemoteScript())
}

// ClientArgs is the local mosh-client argv after the handshake.
func ClientArgs(host string, port int) ([]string, error) {
	if strings.TrimSpace(host) == "" {
		return nil, &ValidationError{Field: "client host", Reason: "is required"}
	}
	if port < 1 || port > 65535 {
		return nil, &ValidationError{Field: "client port", Reason: "is out of range"}
	}
	return []string{host, strconv.Itoa(port)}, nil
}

// ClientEnv merges the mosh-client environment. MOSH_KEY carries the shared
// secret. TERM and LANG are preserved so the local terminfo lookup matches the
// terminal; MOSH_NO_TERM_INIT is set by the adapter because the existing
// terminal already owns screen initialization.
func ClientEnv(base []string, key, lang, fallbackHost string) []string {
	if lang == "" {
		lang = DefaultLang
	}
	haveLang, haveTerm := false, false
	env := make([]string, 0, len(base)+3)
	for _, entry := range base {
		name, _, ok := strings.Cut(entry, "=")
		if !ok {
			env = append(env, entry)
			continue
		}
		if name == "MOSH_FALLBACK_HOST" || name == "MOSH_KEY" {
			continue
		}
		switch name {
		case "LANG":
			haveLang = true
		case "TERM":
			haveTerm = true
		}
		env = append(env, entry)
	}
	if !haveLang {
		env = append(env, "LANG="+lang)
	}
	if !haveTerm {
		env = append(env, "TERM="+DefaultTerm)
	}
	env = append(env, "MOSH_KEY="+key)
	if fallbackHost != "" {
		env = append(env, "MOSH_FALLBACK_HOST="+fallbackHost)
	}
	return env
}

// ResolveLang maps a terminal charset onto the UTF-8 locale Mosh offers. Mosh
// requires a UTF-8 locale on both ends; a non-UTF-8 charset still offers the
// default because the remote server prefers its own working locale.
func ResolveLang(charset string) string {
	normalized := strings.ToLower(strings.TrimSpace(charset))
	if normalized == "utf-8" || normalized == "utf8" {
		return DefaultLang
	}
	return DefaultLang
}

// Connect is the parsed server announcement.
type Connect struct {
	Port int
	Key  string
	// Host is the MOSH IP reported before the announcement, when present.
	Host string
}

// Sniffer extracts the server announcement from the bootstrap stream and
// redacts it from what the terminal shows. Feed chunks in order; Flush when the
// SSH channel ends because mosh-server often prints the marker without a
// trailing newline.
type Sniffer struct {
	pending  []byte
	connect  *Connect
	moshHost string
}

// NewSniffer returns a sniffer with no buffered bytes.
func NewSniffer() *Sniffer { return &Sniffer{} }

// Connected reports whether the announcement has been found.
func (s *Sniffer) Connected() bool { return s.connect != nil }

// Feed consumes one bootstrap chunk and returns the bytes the terminal may
// show. It returns the announcement exactly once.
func (s *Sniffer) Feed(chunk []byte) ([]byte, *Connect) {
	if s.connect != nil {
		return chunk, nil
	}
	s.pending = append(s.pending, chunk...)
	text := string(s.pending)
	var visible strings.Builder
	consumed := 0

	for _, line := range completeLines(text) {
		if line.start > consumed {
			visible.WriteString(text[consumed:line.start])
		}
		if host, ok := ParseIPLine(line.text); ok {
			s.moshHost = host
			consumed = line.end
			continue
		}
		if parsed, ok := ParseConnectLine(line.text); ok {
			s.connect = &Connect{Port: parsed.Port, Key: parsed.Key, Host: s.moshHost}
			visible.WriteString(line.text[:parsed.MatchStart])
			if suffix := line.text[parsed.MatchEnd:]; suffix != "" {
				visible.WriteString(suffix)
				visible.WriteString(line.newline)
			}
			consumed = line.end
			break
		}
		visible.WriteString(line.text)
		visible.WriteString(line.newline)
		consumed = line.end
	}

	if s.connect != nil {
		visible.WriteString(text[consumed:])
		s.pending = nil
		return []byte(visible.String()), s.connect
	}

	s.pending = s.pending[consumed:]
	hold := potentialProtocolStart(string(s.pending))
	if hold == -1 {
		visible.WriteString(string(s.pending))
		s.pending = nil
	} else {
		visible.WriteString(string(s.pending[:hold]))
		s.pending = s.pending[hold:]
		if len(s.pending) > maxProtocolLine {
			visible.WriteString(string(s.pending))
			s.pending = nil
		}
	}
	if len(s.pending) > ringSize {
		overflow := len(s.pending) - ringSize
		visible.WriteString(string(s.pending[:overflow]))
		s.pending = s.pending[overflow:]
	}
	return []byte(visible.String()), nil
}

// Flush drains an unterminated trailing protocol line after the bootstrap
// ends. It returns the announcement when the final line held one.
func (s *Sniffer) Flush() ([]byte, *Connect) {
	if s.connect != nil || len(s.pending) == 0 {
		return nil, nil
	}
	text := string(s.pending)
	s.pending = nil
	if host, ok := ParseIPLine(text); ok {
		s.moshHost = host
	}
	parsed, ok := ParseConnectLine(text)
	if !ok {
		return []byte(text), nil
	}
	s.connect = &Connect{Port: parsed.Port, Key: parsed.Key, Host: s.moshHost}
	visible := text[:parsed.MatchStart] + text[parsed.MatchEnd:]
	return []byte(visible), s.connect
}

// Maximum buffered protocol line and trailing window, matching the original.
const (
	ringSize        = 4096
	maxProtocolLine = 512
)

// ParsedConnectLine is one MOSH CONNECT match inside a line.
type ParsedConnectLine struct {
	Port       int
	Key        string
	MatchStart int
	MatchEnd   int
}

var (
	connectPrefixRE = regexp.MustCompile(`MOSH CONNECT[ \t]+(\d{1,5})[ \t]+`)
	ipLineRE        = regexp.MustCompile(`MOSH IP[ \t]+(\S+)`)
	keyByte         = func(b byte) bool {
		return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '+' || b == '/'
	}
	keyByteOrPadding = func(b byte) bool { return keyByte(b) || b == '=' }
)

// ParseConnectLine parses one complete line. ANSI escapes may surround or
// split the marker and key.
func ParseConnectLine(line string) (ParsedConnectLine, bool) {
	cleaned, cleanToOriginal := stripANSIWithMap(line)
	match := connectPrefixRE.FindStringSubmatchIndex(cleaned)
	if match == nil {
		return ParsedConnectLine{}, false
	}
	port, err := strconv.Atoi(cleaned[match[2]:match[3]])
	if err != nil || port < 1 || port > 65535 {
		return ParsedConnectLine{}, false
	}
	connectIndex, ok := cleanOffset(cleanToOriginal, match[0])
	if !ok {
		return ParsedConnectLine{}, false
	}
	keyStart, ok := cleanOffset(cleanToOriginal, match[1])
	if !ok {
		return ParsedConnectLine{}, false
	}

	var key strings.Builder
	pos := keyStart
	for pos < len(line) && key.Len() < 22 {
		if line[pos] == 0x1b {
			if end, ok := ansiEnd(line, pos); ok {
				pos = end
				continue
			}
		}
		if !keyByte(line[pos]) {
			return ParsedConnectLine{}, false
		}
		key.WriteByte(line[pos])
		pos++
	}
	if key.Len() != 22 {
		return ParsedConnectLine{}, false
	}

	paddingLookahead := skipANSI(line, pos)
	switch {
	case strings.HasPrefix(line[paddingLookahead:], "=="):
		key.WriteString("==")
		pos = paddingLookahead + 2
	case paddingLookahead < len(line) && line[paddingLookahead] == '=':
		second := skipANSI(line, paddingLookahead+1)
		if second < len(line) && line[second] == '=' {
			key.WriteString("==")
			pos = second + 1
		} else if paddingLookahead == pos {
			return ParsedConnectLine{}, false
		}
	case paddingLookahead == pos && pos < len(line) && keyByteOrPadding(line[pos]):
		return ParsedConnectLine{}, false
	}
	if pos < len(line) && keyByteOrPadding(line[pos]) {
		return ParsedConnectLine{}, false
	}
	if !ValidKey(key.String()) {
		return ParsedConnectLine{}, false
	}

	matchEnd := pos
	for matchEnd < len(line) {
		if line[matchEnd] == 0x1b {
			if end, ok := ansiEnd(line, matchEnd); ok {
				matchEnd = end
				continue
			}
		}
		if line[matchEnd] == ' ' || line[matchEnd] == '\t' {
			matchEnd++
			continue
		}
		break
	}
	return ParsedConnectLine{
		Port:       port,
		Key:        key.String(),
		MatchStart: connectIndex,
		MatchEnd:   matchEnd,
	}, true
}

// ValidKey reports the two MOSH_KEY shapes mosh-server emits: a 22-character
// token, or the same token padded to 24 with "==".
func ValidKey(key string) bool {
	return len(key) == 22 || len(key) == 24 && strings.HasSuffix(key, "==")
}

// ParseIPLine parses the optional `MOSH IP <address>` announcement.
func ParseIPLine(line string) (string, bool) {
	match := ipLineRE.FindStringSubmatch(stripANSI(line))
	if match == nil {
		return "", false
	}
	host := match[1]
	if net.ParseIP(host) == nil {
		return "", false
	}
	return host, true
}

// lineRange is one complete line and its newline inside a chunk.
type lineRange struct {
	text    string
	newline string
	start   int
	end     int
}

// completeLines iterates CRLF, CR, and LF-terminated lines.
func completeLines(text string) []lineRange {
	var lines []lineRange
	for index := 0; index < len(text); {
		newline := strings.IndexAny(text[index:], "\r\n")
		if newline < 0 {
			break
		}
		start := index
		index += newline
		terminator := text[index : index+1]
		index++
		if terminator == "\r" && index < len(text) && text[index] == '\n' {
			terminator = "\r\n"
			index++
		}
		lines = append(lines, lineRange{
			text:    text[start : start+newline],
			newline: terminator,
			start:   start,
			end:     index,
		})
	}
	return lines
}

// potentialProtocolStart returns the earliest position where a buffered chunk
// might begin a protocol marker, or -1.
func potentialProtocolStart(text string) int {
	if text == "" {
		return -1
	}
	best := -1
	consider := func(position int) {
		if best == -1 || position < best {
			best = position
		}
	}
	for _, marker := range []string{connectMarker, ipMarker} {
		if full := strings.Index(text, marker); full != -1 {
			consider(full)
		}
		limit := len(marker) - 1
		if len(text) < limit {
			limit = len(text)
		}
		for length := limit; length > 0; length-- {
			if strings.HasPrefix(marker, text[len(text)-length:]) {
				consider(len(text) - length)
				break
			}
		}
	}
	return best
}

// cleanOffset maps a cleaned byte offset back onto the original line.
func cleanOffset(positions []int, offset int) (int, bool) {
	if offset < 0 || offset >= len(positions) {
		return 0, false
	}
	return positions[offset], true
}

// stripANSI removes escape sequences.
func stripANSI(text string) string {
	cleaned, _ := stripANSIWithMap(text)
	return cleaned
}

// stripANSIWithMap removes escape sequences and records the original offset of
// every kept byte, plus the end of the line.
func stripANSIWithMap(text string) (string, []int) {
	var cleaned strings.Builder
	positions := make([]int, 0, len(text)+1)
	for index := 0; index < len(text); {
		if text[index] == 0x1b {
			if end, ok := ansiEnd(text, index); ok {
				index = end
				continue
			}
		}
		positions = append(positions, index)
		cleaned.WriteByte(text[index])
		index++
	}
	positions = append(positions, len(text))
	return cleaned.String(), positions
}

// skipANSI advances past consecutive escape sequences.
func skipANSI(text string, offset int) int {
	position := offset
	for position < len(text) && text[position] == 0x1b {
		end, ok := ansiEnd(text, position)
		if !ok {
			break
		}
		position = end
	}
	return position
}

// ansiEnd returns the offset after the escape sequence starting at start.
// Unterminated strings consume only ESC and their introducer, matching the
// original parser's single-character fallback.
func ansiEnd(text string, start int) (int, bool) {
	if start+1 >= len(text) || text[start] != 0x1b {
		return 0, false
	}
	switch text[start+1] {
	case '[':
		for index := start + 2; index < len(text); index++ {
			if text[index] >= 0x40 && text[index] <= 0x7e {
				return index + 1, true
			}
		}
		return start + 2, true
	case ']':
		for index := start + 2; index < len(text); index++ {
			if text[index] == 0x07 {
				return index + 1, true
			}
			if text[index] == 0x1b && index+1 < len(text) && text[index+1] == '\\' {
				return index + 2, true
			}
		}
		return start + 2, true
	case 'P', 'X', '^', '_':
		for index := start + 2; index+1 < len(text); index++ {
			if text[index] == 0x1b && text[index+1] == '\\' {
				return index + 2, true
			}
		}
		return start + 2, true
	default:
		return start + 2, true
	}
}
