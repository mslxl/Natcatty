package telnet

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestNormalizeNewlinesUsesNVTForm(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare carriage return", "ps\r", "ps\r\n"},
		{"bare newline", "ps\n", "ps\r\n"},
		{"CR LF is preserved", "ps\r\n", "ps\r\n"},
		{"CR NUL is preserved", "red\r\x00raw", "red\r\x00raw"},
		{"multiple lines", "one\ntwo\rthree\r\n", "one\r\ntwo\r\nthree\r\n"},
		{"empty", "", ""},
		{"binary bytes are untouched", "\x01\x02\xff", "\x01\x02\xff"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := NormalizeNewlines(test.in); got != test.want {
				t.Fatalf("NormalizeNewlines(%q) = %q, want %q", test.in, got, test.want)
			}
		})
	}
}

func TestEscapeIACDoublesLiteralsAndKeepsCleanData(t *testing.T) {
	clean := []byte("abc")
	if got := EscapeIAC(clean); &got[0] != &clean[0] {
		t.Fatal("clean data was copied instead of passed through")
	}
	in := []byte{0xff, 'a', 0xff, 0xff, 'b'}
	want := []byte{0xff, 0xff, 'a', 0xff, 0xff, 0xff, 0xff, 'b'}
	if got := EscapeIAC(in); !bytes.Equal(got, want) {
		t.Fatalf("EscapeIAC = %v, want %v", got, want)
	}
}

// collect builds a parser and records every event.
func collect() (*Parser, *[][]byte, *[]command, *[]subnegotiation) {
	var data [][]byte
	var commands []command
	var subnegs []subnegotiation
	parser := NewParser(Callbacks{
		Data: func(chunk []byte) {
			data = append(data, append([]byte(nil), chunk...))
		},
		Command: func(cmd, opt byte) {
			commands = append(commands, command{cmd: cmd, opt: opt})
		},
		Subnegotiation: func(opt byte, payload []byte) {
			subnegs = append(subnegs, subnegotiation{opt: opt, payload: append([]byte(nil), payload...)})
		},
	})
	return parser, &data, &commands, &subnegs
}

type command struct{ cmd, opt byte }
type subnegotiation struct {
	opt     byte
	payload []byte
}

func dataText(data *[][]byte) string {
	var out []byte
	for _, chunk := range *data {
		out = append(out, chunk...)
	}
	return string(out)
}

func TestParserEmitsCleanData(t *testing.T) {
	parser, data, commands, subnegs := collect()
	parser.Feed([]byte("hello world"))
	if got := dataText(data); got != "hello world" {
		t.Fatalf("data = %q", got)
	}
	if len(*commands) != 0 || len(*subnegs) != 0 {
		t.Fatalf("commands = %v, subnegs = %v", *commands, *subnegs)
	}
}

func TestParserSplitsDataAroundAnOptionCommand(t *testing.T) {
	parser, data, commands, _ := collect()
	parser.Feed([]byte("login: "))
	parser.Feed([]byte{IAC, WILL, OptionEcho})
	parser.Feed([]byte("admin"))
	if got := dataText(data); got != "login: admin" {
		t.Fatalf("data = %q", got)
	}
	want := []command{{cmd: WILL, opt: OptionEcho}}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v, want %v", *commands, want)
	}
}

func TestParserUnescapesIACAndIgnoresStandaloneVerbs(t *testing.T) {
	parser, data, commands, _ := collect()
	parser.Feed([]byte{'a', IAC, IAC, 'b', IAC, NOP, 'c'})
	if got := dataText(data); got != string([]byte{'a', 0xff, 'b', 'c'}) {
		t.Fatalf("data = %v", []byte(dataText(data)))
	}
	if len(*commands) != 0 {
		t.Fatalf("commands = %v", *commands)
	}
}

func TestParserParsesCompleteSubnegotiation(t *testing.T) {
	parser, data, _, subnegs := collect()
	parser.Feed([]byte{IAC, SB, OptionTerminalType, 0})
	parser.Feed([]byte("XTERM"))
	parser.Feed([]byte{IAC, SE})
	if len(*data) != 0 || len(*subnegs) != 1 {
		t.Fatalf("data = %v, subnegs = %v", *data, *subnegs)
	}
	if (*subnegs)[0].opt != OptionTerminalType {
		t.Fatalf("option = %d", (*subnegs)[0].opt)
	}
	if got := string((*subnegs)[0].payload); got != "\x00XTERM" {
		t.Fatalf("payload = %q", got)
	}
}

func TestParserToleratesEscapedIACInsideSubnegotiation(t *testing.T) {
	parser, _, _, subnegs := collect()
	parser.Feed([]byte{IAC, SB, OptionStatus, IAC, IAC, 0x01, IAC, SE})
	if len(*subnegs) != 1 {
		t.Fatalf("subnegs = %v", *subnegs)
	}
	want := []byte{0xff, 0x01}
	if !bytes.Equal((*subnegs)[0].payload, want) {
		t.Fatalf("payload = %v, want %v", (*subnegs)[0].payload, want)
	}
}

func TestParserPreservesPartialCommandsAcrossFeeds(t *testing.T) {
	parser, data, commands, _ := collect()
	parser.Feed(append([]byte("hi"), IAC))
	if got := dataText(data); got != "hi" {
		t.Fatalf("data = %q", got)
	}
	if parser.PendingByteCount() != 1 || len(*commands) != 0 {
		t.Fatalf("pending = %d, commands = %v", parser.PendingByteCount(), *commands)
	}
	parser.Feed([]byte{DO, OptionNAWS, 'a'})
	if parser.PendingByteCount() != 0 {
		t.Fatalf("pending = %d, want 0", parser.PendingByteCount())
	}
	want := []command{{cmd: DO, opt: OptionNAWS}}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v, want %v", *commands, want)
	}
	if got := dataText(data); got != "hia" {
		t.Fatalf("data = %q", got)
	}
}

func TestParserPreservesHalfAnOptionCommand(t *testing.T) {
	parser, data, commands, _ := collect()
	parser.Feed([]byte{'a', IAC, DO})
	if got := dataText(data); got != "a" || parser.PendingByteCount() != 2 {
		t.Fatalf("data = %q, pending = %d", got, parser.PendingByteCount())
	}
	parser.Feed([]byte{OptionTerminalType, 'b'})
	want := []command{{cmd: DO, opt: OptionTerminalType}}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v, want %v", *commands, want)
	}
	if got := dataText(data); got != "ab" {
		t.Fatalf("data = %q", got)
	}
}

func TestParserNeverLeaksAnUnterminatedSubnegotiation(t *testing.T) {
	parser, data, _, subnegs := collect()
	parser.Feed(append([]byte("prefix"), IAC, SB, OptionTerminalType, 0))
	parser.Feed([]byte("XTERM-PARTIAL"))
	if got := dataText(data); got != "prefix" {
		t.Fatalf("data = %q", got)
	}
	if len(*subnegs) != 0 || parser.PendingByteCount() == 0 {
		t.Fatalf("subnegs = %v, pending = %d", *subnegs, parser.PendingByteCount())
	}
	parser.Feed(append([]byte("REST"), IAC, SE))
	parser.Feed([]byte(" tail"))
	if got := dataText(data); got != "prefix tail" {
		t.Fatalf("data = %q", got)
	}
	if len(*subnegs) != 1 || string((*subnegs)[0].payload) != "\x00XTERM-PARTIALREST" {
		t.Fatalf("subnegs = %v", *subnegs)
	}
}

func TestParserHandlesConsecutiveCommands(t *testing.T) {
	parser, _, commands, _ := collect()
	parser.Feed([]byte{IAC, WILL, OptionEcho, IAC, DO, OptionSuppressGoAhead})
	want := []command{{cmd: WILL, opt: OptionEcho}, {cmd: DO, opt: OptionSuppressGoAhead}}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v, want %v", *commands, want)
	}
}

func TestParserEmptyFeedAndReset(t *testing.T) {
	parser, data, commands, _ := collect()
	parser.Feed(nil)
	parser.Feed([]byte{})
	parser.Feed([]byte{IAC})
	if len(*data) != 0 || len(*commands) != 0 || parser.PendingByteCount() != 1 {
		t.Fatalf("data = %v, commands = %v, pending = %d", *data, *commands, parser.PendingByteCount())
	}
	parser.Reset()
	if parser.PendingByteCount() != 0 {
		t.Fatalf("pending = %d after Reset", parser.PendingByteCount())
	}
}

func recordNegotiator(options NegotiatorOptions) (*Negotiator, *[]command, *[]subnegotiation) {
	var commands []command
	var subnegs []subnegotiation
	if options.WriteCommand == nil {
		options.WriteCommand = func(cmd, opt byte) {
			commands = append(commands, command{cmd: cmd, opt: opt})
		}
	}
	if options.WriteSubnegotiation == nil {
		options.WriteSubnegotiation = func(opt byte, payload []byte) {
			subnegs = append(subnegs, subnegotiation{opt: opt, payload: append([]byte(nil), payload...)})
		}
	}
	if options.WindowSize == nil {
		options.WindowSize = func() (int, int) { return 120, 40 }
	}
	return NewNegotiator(options), &commands, &subnegs
}

func TestNegotiatorStartDrivesTheCanonicalHandshake(t *testing.T) {
	negotiator, commands, _ := recordNegotiator(NegotiatorOptions{})
	negotiator.Start()
	want := []command{
		{cmd: DO, opt: OptionSuppressGoAhead},
		{cmd: WILL, opt: OptionTerminalType},
		{cmd: WILL, opt: OptionNAWS},
	}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v, want %v", *commands, want)
	}
	if negotiator.PendingDoCount() != 1 || negotiator.PendingWillCount() != 2 {
		t.Fatalf("pending do=%d will=%d", negotiator.PendingDoCount(), negotiator.PendingWillCount())
	}
}

func TestNegotiatorSwallowsAcknowledgementsAndAnswersIndependentRequests(t *testing.T) {
	negotiator, commands, _ := recordNegotiator(NegotiatorOptions{})
	negotiator.Start()
	*commands = nil

	negotiator.HandleCommand(WILL, OptionSuppressGoAhead)
	if len(*commands) != 0 || negotiator.PendingDoCount() != 0 {
		t.Fatalf("acknowledgement produced %v, pending do=%d", *commands, negotiator.PendingDoCount())
	}

	// The peer asks us to enable SGA on our side while our request to it is
	// still in flight. RFC 858 treats the directions independently.
	negotiator.HandleCommand(DO, OptionSuppressGoAhead)
	want := []command{{cmd: WILL, opt: OptionSuppressGoAhead}}
	if !reflect.DeepEqual(*commands, want) || negotiator.PendingDoCount() != 0 {
		t.Fatalf("commands = %v, pending do=%d", *commands, negotiator.PendingDoCount())
	}
}

func TestNegotiatorHandlesNAWS(t *testing.T) {
	negotiator, commands, subnegs := recordNegotiator(NegotiatorOptions{})
	negotiator.Start()
	*commands = nil
	*subnegs = nil

	// DO NAWS acknowledges our WILL: no echoed WILL but the size follows.
	negotiator.HandleCommand(DO, OptionNAWS)
	if len(*commands) != 0 {
		t.Fatalf("acknowledging DO produced %v", *commands)
	}
	wantSize := []byte{0, 120, 0, 40}
	if len(*subnegs) != 1 || (*subnegs)[0].opt != OptionNAWS ||
		!bytes.Equal((*subnegs)[0].payload, wantSize) {
		t.Fatalf("subnegs = %v, want NAWS %v", *subnegs, wantSize)
	}

	*commands = nil
	*subnegs = nil
	// An independent DO NAWS is answered with WILL and a size.
	independent, independentCommands, independentSubnegs := recordNegotiator(NegotiatorOptions{})
	independent.HandleCommand(DO, OptionNAWS)
	want := []command{{cmd: WILL, opt: OptionNAWS}}
	if !reflect.DeepEqual(*independentCommands, want) || len(*independentSubnegs) != 1 {
		t.Fatalf("commands = %v, subnegs = %v", *independentCommands, *independentSubnegs)
	}

	// A repeated DO is ignored.
	independent.HandleCommand(DO, OptionNAWS)
	if len(*independentCommands) != 1 || len(*independentSubnegs) != 1 {
		t.Fatalf("repeated DO produced commands=%v subnegs=%v", *independentCommands, *independentSubnegs)
	}
}

func TestNegotiatorWindowSizeIsSilentUntilEnabled(t *testing.T) {
	negotiator, _, subnegs := recordNegotiator(NegotiatorOptions{})
	negotiator.Start()
	if negotiator.SendWindowSize() {
		t.Fatal("SendWindowSize reported a send before DO NAWS")
	}
	if len(*subnegs) != 0 {
		t.Fatalf("subnegs = %v", *subnegs)
	}
	negotiator.HandleCommand(DO, OptionNAWS)
	*subnegs = nil
	if !negotiator.SendWindowSize() {
		t.Fatal("SendWindowSize did not send after DO NAWS")
	}
	if len(*subnegs) != 1 {
		t.Fatalf("subnegs = %v", *subnegs)
	}
}

func TestNegotiatorAnswersTerminalTypeSend(t *testing.T) {
	negotiator, _, subnegs := recordNegotiator(NegotiatorOptions{TermType: "XTERM-256COLOR"})
	negotiator.HandleSubnegotiation(OptionTerminalType, []byte{SuboptionSEND})
	if len(*subnegs) != 1 || (*subnegs)[0].opt != OptionTerminalType {
		t.Fatalf("subnegs = %v", *subnegs)
	}
	if got := string((*subnegs)[0].payload); got != "\x00XTERM-256COLOR" {
		t.Fatalf("payload = %q", got)
	}
	// Anything else is not answered.
	negotiator.HandleSubnegotiation(OptionTerminalType, []byte{SuboptionIS, 'X'})
	if len(*subnegs) != 1 {
		t.Fatalf("a non-SEND subnegotiation was answered: %v", *subnegs)
	}
}

func TestNegotiatorRefusesUnknownOptions(t *testing.T) {
	negotiator, commands, _ := recordNegotiator(NegotiatorOptions{})
	negotiator.HandleCommand(DO, OptionLinemode)
	negotiator.HandleCommand(WILL, OptionNewEnviron)
	want := []command{
		{cmd: WONT, opt: OptionLinemode},
		{cmd: DONT, opt: OptionNewEnviron},
	}
	if !reflect.DeepEqual(*commands, want) {
		t.Fatalf("commands = %v, want %v", *commands, want)
	}
}

func TestNegotiatorTracksEchoBothDirections(t *testing.T) {
	negotiator, commands, _ := recordNegotiator(NegotiatorOptions{})
	if !negotiator.RemoteEcho() || negotiator.LocalEcho() {
		t.Fatalf("initial echo = remote %v local %v", negotiator.RemoteEcho(), negotiator.LocalEcho())
	}

	negotiator.HandleCommand(WILL, OptionEcho)
	if !reflect.DeepEqual(*commands, []command{{cmd: DO, opt: OptionEcho}}) {
		t.Fatalf("commands = %v", *commands)
	}
	*commands = nil
	negotiator.HandleCommand(WILL, OptionEcho)
	if len(*commands) != 0 {
		t.Fatalf("repeated WILL ECHO produced %v", *commands)
	}

	// DO ECHO asks us to echo locally.
	negotiator.HandleCommand(DO, OptionEcho)
	if !negotiator.LocalEcho() {
		t.Fatal("DO ECHO did not enable local echo")
	}
	*commands = nil
	// A later WILL ECHO transfers echo to the server. Remote echo was already
	// enabled by the first WILL, so only WONT ECHO is emitted.
	negotiator.HandleCommand(WILL, OptionEcho)
	if wanted := []command{{cmd: WONT, opt: OptionEcho}}; !reflect.DeepEqual(*commands, wanted) {
		t.Fatalf("commands = %v, want %v", *commands, wanted)
	}
	if negotiator.LocalEcho() {
		t.Fatal("WILL ECHO left local echo enabled")
	}

	// Without a previous WILL, the transfer also offers DO ECHO so the server
	// keeps echoing.
	fresh, freshCommands, _ := recordNegotiator(NegotiatorOptions{})
	fresh.HandleCommand(DO, OptionEcho)
	*freshCommands = nil
	fresh.HandleCommand(WILL, OptionEcho)
	wanted := []command{{cmd: WONT, opt: OptionEcho}, {cmd: DO, opt: OptionEcho}}
	if !reflect.DeepEqual(*freshCommands, wanted) {
		t.Fatalf("fresh commands = %v, want %v", *freshCommands, wanted)
	}

	negotiator.HandleCommand(WONT, OptionEcho)
	if negotiator.RemoteEcho() {
		t.Fatal("WONT ECHO left remote echo enabled")
	}
}

func TestNegotiatorHandlesDontEcho(t *testing.T) {
	negotiator, commands, _ := recordNegotiator(NegotiatorOptions{})
	negotiator.HandleCommand(DO, OptionEcho)
	*commands = nil
	negotiator.HandleCommand(DONT, OptionEcho)
	if negotiator.LocalEcho() {
		t.Fatal("DONT ECHO left local echo enabled")
	}
	if !reflect.DeepEqual(*commands, []command{{cmd: WONT, opt: OptionEcho}}) {
		t.Fatalf("commands = %v", *commands)
	}
	*commands = nil
	negotiator.HandleCommand(DONT, OptionEcho)
	if len(*commands) != 0 {
		t.Fatalf("repeated DONT produced %v", *commands)
	}
}

func TestConfigNormalizesAndValidates(t *testing.T) {
	cfg := Config{Hostname: "  router.local  "}.Normalized()
	if cfg.Hostname != "router.local" || cfg.Port != DefaultPort ||
		cfg.ConnectTimeout != DefaultConnectTimeout || cfg.TermType != DefaultTermType {
		t.Fatalf("normalized = %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	invalid := []Config{
		{Port: 23},
		{Hostname: "host", Port: 70000},
		{Hostname: "host", Port: 23, ConnectTimeout: -time.Second},
	}
	for _, bad := range invalid {
		if err := bad.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("Validate(%+v) = %v, want ErrInvalidConfig", bad, err)
		}
	}
	explicit := Config{
		Hostname: "host", Port: 2323, ConnectTimeout: 2 * time.Second, TermType: "VT100",
	}.Normalized()
	if explicit.Port != 2323 || explicit.ConnectTimeout != 2*time.Second || explicit.TermType != "VT100" {
		t.Fatalf("explicit config was overwritten: %+v", explicit)
	}
}

func TestIsOptionCommand(t *testing.T) {
	for _, cmd := range []byte{WILL, WONT, DO, DONT} {
		if !IsOptionCommand(cmd) {
			t.Fatalf("IsOptionCommand(%d) = false", cmd)
		}
	}
	for _, cmd := range []byte{IAC, SB, SE, NOP} {
		if IsOptionCommand(cmd) {
			t.Fatalf("IsOptionCommand(%d) = true", cmd)
		}
	}
}
