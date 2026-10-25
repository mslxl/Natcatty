// Package telnet holds the Telnet wire vocabulary shared by the Session, the
// transport adapter, and the connection form: RFC 854 command framing, the
// option negotiation the original client drove (RFC 857 ECHO, RFC 858
// SUPPRESS-GO-AHEAD, RFC 1073 NAWS, RFC 1091 TERMINAL-TYPE), NVT newline
// normalization, and the IAC escaping rule for binary data.
//
// The package is pure data and state machines. It imports only the standard
// library and never touches a socket, so the framing can be exercised one
// byte at a time.
package telnet

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Telnet commands (RFC 854 / RFC 855).
const (
	// IAC introduces every Telnet command. A literal 0xFF in the data stream
	// is sent doubled.
	IAC byte = 255
	// SE ends a subnegotiation.
	SE byte = 240
	// NOP is a no-operation command.
	NOP byte = 241
	// DM is the data-mark command.
	DM byte = 242
	// BRK is the break command.
	BRK byte = 243
	// IP is the interrupt-process command.
	IP byte = 244
	// AO is the abort-output command.
	AO byte = 245
	// AYT is the are-you-there command.
	AYT byte = 246
	// EC is the erase-character command.
	EC byte = 247
	// EL is the erase-line command.
	EL byte = 248
	// GA signals go-ahead.
	GA byte = 249
	// SB starts a subnegotiation.
	SB byte = 250
	// WILL offers to enable an option on the sender's side.
	WILL byte = 251
	// WONT refuses to enable an option on the sender's side.
	WONT byte = 252
	// DO asks the peer to enable an option on its side.
	DO byte = 253
	// DONT asks the peer to disable an option on its side.
	DONT byte = 254
)

// Options the client negotiates. Other options are refused so the
// conversation terminates instead of looping.
const (
	// OptionEcho is the echo option (RFC 857).
	OptionEcho byte = 1
	// OptionSuppressGoAhead is the suppress-go-ahead option (RFC 858).
	OptionSuppressGoAhead byte = 3
	// OptionStatus is the status option (RFC 859).
	OptionStatus byte = 5
	// OptionTerminalType is the terminal-type option (RFC 1091).
	OptionTerminalType byte = 24
	// OptionNAWS is the negotiate-about-window-size option (RFC 1073).
	OptionNAWS byte = 31
	// OptionTerminalSpeed is the terminal-speed option (RFC 1079).
	OptionTerminalSpeed byte = 32
	// OptionLinemode is the line-mode option (RFC 1184).
	OptionLinemode byte = 34
	// OptionNewEnviron is the new-environment option (RFC 1572).
	OptionNewEnviron byte = 39
)

// Subnegotiation qualifiers used by TERMINAL-TYPE.
const (
	// SuboptionIS reports the current value.
	SuboptionIS byte = 0
	// SuboptionSEND asks the peer for its value.
	SuboptionSEND byte = 1
)

// IsOptionCommand reports whether cmd is WILL, WONT, DO, or DONT. Every other
// command carries no option byte.
func IsOptionCommand(cmd byte) bool {
	return cmd == WILL || cmd == WONT || cmd == DO || cmd == DONT
}

// DefaultTermType is the terminal type the original advertised.
const DefaultTermType = "XTERM-256COLOR"

// DefaultPort is the Telnet service port.
const DefaultPort = 23

// DefaultConnectTimeout is the original client's dial deadline.
const DefaultConnectTimeout = 10 * time.Second

// ErrInvalidConfig reports a Telnet configuration that cannot be dialed.
var ErrInvalidConfig = errors.New("telnet: invalid configuration")

// ValidationError names the field and reason a configuration was rejected.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("telnet: %s %s", e.Field, e.Reason)
}

// Is reports errors.Is(err, ErrInvalidConfig).
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidConfig }

// Config is one Telnet endpoint. The Session start spec, the Host connect
// path, and the workspace descriptor share it.
type Config struct {
	// Hostname is the peer's host name or address.
	Hostname string `json:"hostname"`
	// Port is the peer's TCP port.
	Port int `json:"port,omitempty"`
	// ConnectTimeout bounds the TCP dial. Zero uses DefaultConnectTimeout.
	ConnectTimeout time.Duration `json:"connectTimeout,omitempty"`
	// TermType is advertised in TERMINAL-TYPE. Empty uses DefaultTermType.
	TermType string `json:"termType,omitempty"`
}

// Normalized fills the original defaults and trims the host and terminal type.
func (c Config) Normalized() Config {
	c.Hostname = strings.TrimSpace(c.Hostname)
	c.TermType = strings.TrimSpace(c.TermType)
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = DefaultConnectTimeout
	}
	if c.TermType == "" {
		c.TermType = DefaultTermType
	}
	return c
}

// Validate checks a normalized configuration.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Hostname) == "" {
		return &ValidationError{Field: "hostname", Reason: "is required"}
	}
	if c.Port < 1 || c.Port > 65535 {
		return &ValidationError{Field: "port", Reason: "is out of range"}
	}
	if c.ConnectTimeout < 0 {
		return &ValidationError{Field: "connectTimeout", Reason: "must not be negative"}
	}
	return nil
}

// NormalizeNewlines converts terminal input to the NVT form the original
// terminal produced: Enter (a lone CR) and a lone LF both become CR LF, so a
// remote line discipline receives the telnet newline. An existing CR LF or CR
// NUL pair is already valid and is preserved rather than expanded twice.
func NormalizeNewlines(text string) string {
	if text == "" {
		return text
	}
	var out strings.Builder
	out.Grow(len(text) + 8)
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\r':
			out.WriteByte('\r')
			if i+1 >= len(text) || (text[i+1] != '\n' && text[i+1] != 0) {
				out.WriteByte('\n')
			}
		case '\n':
			if i == 0 || text[i-1] != '\r' {
				out.WriteByte('\r')
			}
			out.WriteByte('\n')
		default:
			out.WriteByte(text[i])
		}
	}
	return out.String()
}

// EscapeIAC doubles every literal 0xFF in data so the peer does not read it as
// a command introducer. Data without 0xFF is returned unchanged and costs no
// allocation.
func EscapeIAC(data []byte) []byte {
	index := -1
	for i, value := range data {
		if value == IAC {
			index = i
			break
		}
	}
	if index < 0 {
		return data
	}
	out := make([]byte, 0, len(data)+1)
	out = append(out, data[:index]...)
	for _, value := range data[index:] {
		out = append(out, value)
		if value == IAC {
			out = append(out, IAC)
		}
	}
	return out
}

func unescapeIAC(payload []byte) []byte {
	index := -1
	for i, value := range payload {
		if value == IAC {
			index = i
			break
		}
	}
	if index < 0 {
		return payload
	}
	out := make([]byte, 0, len(payload))
	out = append(out, payload[:index]...)
	for i := index; i < len(payload); i++ {
		if payload[i] == IAC && i+1 < len(payload) && payload[i+1] == IAC {
			out = append(out, IAC)
			i++
			continue
		}
		out = append(out, payload[i])
	}
	return out
}

// Callbacks receive the parser's events. Any callback may be nil.
type Callbacks struct {
	// Data receives bytes that belong to the data stream, with IAC IAC
	// already unescaped. It is never called with an empty slice.
	Data func([]byte)
	// Command receives WILL, WONT, DO, or DONT and its option byte.
	Command func(cmd, opt byte)
	// Subnegotiation receives a complete IAC SB option payload IAC SE. The
	// payload has any IAC IAC pair unescaped.
	Subnegotiation func(opt byte, payload []byte)
}

// Parser turns a Telnet byte stream into data, commands, and
// subnegotiations. It preserves a partial command or an unterminated
// subnegotiation between Feed calls, so a sequence split across TCP reads is
// reassembled before it is acted on.
type Parser struct {
	pending   []byte
	callbacks Callbacks
}

// NewParser returns a parser that reports to callbacks.
func NewParser(callbacks Callbacks) *Parser {
	return &Parser{callbacks: callbacks}
}

// PendingByteCount reports bytes held for the next Feed.
func (p *Parser) PendingByteCount() int { return len(p.pending) }

// Reset drops any partial command. It is used when a transport is torn down.
func (p *Parser) Reset() { p.pending = nil }

// Feed consumes one chunk. A partial trailing command is retained.
func (p *Parser) Feed(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	var buf []byte
	if len(p.pending) == 0 {
		buf = chunk
	} else {
		buf = make([]byte, 0, len(p.pending)+len(chunk))
		buf = append(buf, p.pending...)
		buf = append(buf, chunk...)
		p.pending = nil
	}

	out := make([]byte, 0, len(buf))
	flush := func() {
		if len(out) == 0 {
			return
		}
		if p.callbacks.Data != nil {
			p.callbacks.Data(out)
		}
		out = make([]byte, 0, 16)
	}

	for i := 0; i < len(buf); {
		if buf[i] != IAC {
			out = append(out, buf[i])
			i++
			continue
		}
		if i+1 >= len(buf) {
			p.pending = append(p.pending[:0], buf[i:]...)
			break
		}
		cmd := buf[i+1]
		switch {
		case cmd == IAC:
			out = append(out, IAC)
			i += 2
		case IsOptionCommand(cmd):
			if i+2 >= len(buf) {
				p.pending = append(p.pending[:0], buf[i:]...)
				flush()
				return
			}
			opt := buf[i+2]
			flush()
			if p.callbacks.Command != nil {
				p.callbacks.Command(cmd, opt)
			}
			i += 3
		case cmd == SB:
			end, found := findSubnegotiationEnd(buf, i+3)
			if !found {
				p.pending = append(p.pending[:0], buf[i:]...)
				flush()
				return
			}
			opt := buf[i+2]
			payload := unescapeIAC(buf[i+3 : end])
			flush()
			if p.callbacks.Subnegotiation != nil {
				p.callbacks.Subnegotiation(opt, payload)
			}
			i = end + 2
		default:
			// A single-verb command (NOP, AYT, ...). Consume both bytes so
			// they never leak into the data stream.
			flush()
			i += 2
		}
	}
	flush()
}

// findSubnegotiationEnd returns the index of the IAC that starts IAC SE, or
// false when the terminator has not arrived yet. An escaped IAC IAC inside the
// payload and any other IAC verb are skipped by two bytes.
func findSubnegotiationEnd(buf []byte, from int) (int, bool) {
	for j := from; j+1 < len(buf); {
		if buf[j] != IAC {
			j++
			continue
		}
		if buf[j+1] == SE {
			return j, true
		}
		j += 2
	}
	return 0, false
}

// WindowSize reports the current terminal size for NAWS.
type WindowSize func() (cols, rows int)

// Negotiator is the client's Telnet option policy. It owns the direction-aware
// acknowledgement state, decides which options to accept, and emits the wire
// commands through its sinks. It performs no I/O itself.
type Negotiator struct {
	writeCommand        func(cmd, opt byte)
	writeSubnegotiation func(opt byte, payload []byte)
	windowSize          WindowSize
	termType            string

	pendingDo   map[byte]bool
	pendingWill map[byte]bool
	remote      map[byte]bool
	local       map[byte]bool

	remoteEcho bool
	localEcho  bool
}

// NegotiatorOptions configures a Negotiator. The write sinks and window size
// sink are required; the terminal type is optional.
type NegotiatorOptions struct {
	WriteCommand        func(cmd, opt byte)
	WriteSubnegotiation func(opt byte, payload []byte)
	WindowSize          WindowSize
	TermType            string
}

// NewNegotiator returns a negotiator with the original defaults: the peer is
// assumed to echo until WONT ECHO says otherwise, and the client does not echo
// locally until DO ECHO asks it to.
func NewNegotiator(options NegotiatorOptions) *Negotiator {
	n := &Negotiator{
		writeCommand:        options.WriteCommand,
		writeSubnegotiation: options.WriteSubnegotiation,
		windowSize:          options.WindowSize,
		termType:            options.TermType,
		pendingDo:           map[byte]bool{},
		pendingWill:         map[byte]bool{},
		remote:              map[byte]bool{},
		local:               map[byte]bool{},
		remoteEcho:          true,
	}
	if n.writeCommand == nil {
		n.writeCommand = func(byte, byte) {}
	}
	if n.writeSubnegotiation == nil {
		n.writeSubnegotiation = func(byte, []byte) {}
	}
	if n.windowSize == nil {
		n.windowSize = func() (int, int) { return 80, 24 }
	}
	if n.termType == "" {
		n.termType = DefaultTermType
	}
	return n
}

// Start drives the canonical handshake instead of waiting for the peer. Many
// legacy servers do not advance past their banner until the client commits to
// a basic option set.
func (n *Negotiator) Start() {
	n.request(DO, OptionSuppressGoAhead)
	n.request(WILL, OptionTerminalType)
	n.request(WILL, OptionNAWS)
}

func (n *Negotiator) request(cmd, opt byte) {
	if cmd == DO {
		n.pendingDo[opt] = true
	} else if cmd == WILL {
		n.pendingWill[opt] = true
	}
	n.writeCommand(cmd, opt)
}

// HandleCommand applies one WILL/WONT/DO/DONT from the peer.
func (n *Negotiator) HandleCommand(cmd, opt byte) {
	acknowledges := false
	if (cmd == WILL || cmd == WONT) && n.pendingDo[opt] {
		delete(n.pendingDo, opt)
		acknowledges = true
	} else if (cmd == DO || cmd == DONT) && n.pendingWill[opt] {
		delete(n.pendingWill, opt)
		acknowledges = true
	}

	switch cmd {
	case WILL:
		supported := opt == OptionSuppressGoAhead || opt == OptionEcho
		wasEnabled := n.remote[opt]
		if supported {
			n.remote[opt] = true
		}
		if opt == OptionEcho {
			n.setRemoteEcho(true)
			if n.local[OptionEcho] {
				delete(n.local, OptionEcho)
				n.setLocalEcho(false)
				n.writeCommand(WONT, OptionEcho)
			}
		}
		if !acknowledges {
			if supported && !wasEnabled {
				n.writeCommand(DO, opt)
			} else if !supported {
				n.writeCommand(DONT, opt)
			}
		}
	case DO:
		wasEnabled := n.local[opt]
		switch opt {
		case OptionNAWS:
			n.local[opt] = true
			if !acknowledges && !wasEnabled {
				n.writeCommand(WILL, opt)
			}
			if !wasEnabled {
				n.SendWindowSize()
			}
		case OptionEcho:
			n.local[opt] = true
			if !acknowledges && !wasEnabled {
				n.writeCommand(WILL, opt)
			}
			if !wasEnabled {
				n.setLocalEcho(true)
			}
		case OptionTerminalType, OptionSuppressGoAhead:
			n.local[opt] = true
			if !acknowledges && !wasEnabled {
				n.writeCommand(WILL, opt)
			}
		default:
			if !acknowledges {
				n.writeCommand(WONT, opt)
			}
		}
	case DONT:
		wasEnabled := n.local[opt]
		delete(n.local, opt)
		if opt == OptionEcho && wasEnabled {
			n.setLocalEcho(false)
		}
		if !acknowledges && (wasEnabled || n.pendingWill[opt]) {
			n.writeCommand(WONT, opt)
		}
	case WONT:
		wasEnabled := n.remote[opt]
		delete(n.remote, opt)
		if opt == OptionEcho {
			n.setRemoteEcho(false)
		}
		if !acknowledges && wasEnabled {
			n.writeCommand(DONT, opt)
		}
	}
}

// HandleSubnegotiation answers a TERMINAL-TYPE SEND. Other subnegotiations
// carry no client response.
func (n *Negotiator) HandleSubnegotiation(opt byte, payload []byte) {
	if opt != OptionTerminalType || len(payload) == 0 || payload[0] != SuboptionSEND {
		return
	}
	reply := make([]byte, 0, len(n.termType)+1)
	reply = append(reply, SuboptionIS)
	reply = append(reply, n.termType...)
	n.writeSubnegotiation(OptionTerminalType, reply)
}

// SendWindowSize emits NAWS when the peer has enabled it. It reports whether a
// subnegotiation was sent; a resize before DO NAWS is silent, matching the
// original client.
func (n *Negotiator) SendWindowSize() bool {
	if !n.local[OptionNAWS] {
		return false
	}
	cols, rows := n.windowSize()
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	payload := []byte{
		byte(cols >> 8), byte(cols),
		byte(rows >> 8), byte(rows),
	}
	n.writeSubnegotiation(OptionNAWS, EscapeIAC(payload))
	return true
}

// RemoteEcho reports whether the server echoes input.
func (n *Negotiator) RemoteEcho() bool { return n.remoteEcho }

// LocalEcho reports whether the client was asked to echo input locally.
func (n *Negotiator) LocalEcho() bool { return n.localEcho }

// PendingDoCount reports option requests still awaiting a WILL/WONT.
func (n *Negotiator) PendingDoCount() int { return len(n.pendingDo) }

// PendingWillCount reports option offers still awaiting a DO/DONT.
func (n *Negotiator) PendingWillCount() int { return len(n.pendingWill) }

func (n *Negotiator) setRemoteEcho(enabled bool) { n.remoteEcho = enabled }

func (n *Negotiator) setLocalEcho(enabled bool) { n.localEcho = enabled }
