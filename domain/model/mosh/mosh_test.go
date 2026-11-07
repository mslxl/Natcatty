package mosh

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const testKey = "abcdefghijklmnopqrstuv"

func TestParseConnectLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		key  string
		port int
		ok   bool
	}{
		{name: "plain", line: "MOSH CONNECT 60001 " + testKey, key: testKey, port: 60001, ok: true},
		{name: "padded", line: "MOSH CONNECT 60001 " + testKey + "==", key: testKey + "==", port: 60001, ok: true},
		{name: "sgr wrapped", line: "\x1b[32mMOSH CONNECT 60002 " + testKey + "\x1b[0m", key: testKey, port: 60002, ok: true},
		{name: "csi inside key", line: "MOSH CONNECT 60003 abcdefghij\x1b[?25hklmnopqrstuv", key: testKey, port: 60003, ok: true},
		{name: "osc before", line: "\x1b]0;title\x07MOSH CONNECT 60004 " + testKey + " tail", key: testKey, port: 60004, ok: true},
		{name: "trailing spaces", line: "MOSH CONNECT 60005 " + testKey + "   \x1b[1m", key: testKey, port: 60005, ok: true},
		{name: "short key", line: "MOSH CONNECT 60001 abc", ok: false},
		{name: "long key", line: "MOSH CONNECT 60001 " + testKey + "X", ok: false},
		{name: "bad padding", line: "MOSH CONNECT 60001 " + testKey + "=", ok: false},
		{name: "port zero", line: "MOSH CONNECT 0 " + testKey, ok: false},
		{name: "port huge", line: "MOSH CONNECT 70000 " + testKey, ok: false},
		{name: "no marker", line: "mosh connect 60001 " + testKey, ok: false},
		{name: "tab separated", line: "MOSH CONNECT\t60006\t" + testKey, key: testKey, port: 60006, ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, ok := ParseConnectLine(test.line)
			if ok != test.ok {
				t.Fatalf("ParseConnectLine() ok = %v, want %v", ok, test.ok)
			}
			if !ok {
				return
			}
			if parsed.Port != test.port || parsed.Key != test.key {
				t.Fatalf("parsed = %+v, want port %d key %q", parsed, test.port, test.key)
			}
			if parsed.MatchStart < 0 || parsed.MatchEnd <= parsed.MatchStart {
				t.Fatalf("invalid match range %d..%d", parsed.MatchStart, parsed.MatchEnd)
			}
		})
	}
}

func TestParseIPLine(t *testing.T) {
	for _, line := range []string{
		"MOSH IP 203.0.113.7",
		"\x1b[36mMOSH IP 2001:db8::1\x1b[0m",
		"MOSH IP\t198.51.100.4\r",
	} {
		if _, ok := ParseIPLine(line); !ok {
			t.Fatalf("ParseIPLine(%q) did not parse", line)
		}
	}
	if _, ok := ParseIPLine("MOSH IP not-an-address"); ok {
		t.Fatal("ParseIPLine accepted a hostname")
	}
}

// The announcement may arrive in pieces: the marker, a key without padding,
// and finally the padding. It must not surface in the terminal stream.
func TestSnifferChunkBoundaries(t *testing.T) {
	sniffer := NewSniffer()
	chunks := []string{
		"Welcome\r\nMOSH IP 203.0.113.9\r\nMOSH CO",
		"NNECT 6000",
		"1 " + testKey[:10],
		testKey[10:],
		"==",
		"\x1b[?25h\r\n",
	}
	var visible strings.Builder
	var connect *Connect
	for index, chunk := range chunks {
		out, parsed := sniffer.Feed([]byte(chunk))
		visible.Write(out)
		if parsed != nil {
			if connect != nil {
				t.Fatalf("chunk %d announced twice", index)
			}
			connect = parsed
		}
	}
	if connect == nil {
		t.Fatalf("no announcement; visible = %q", visible.String())
	}
	if connect.Port != 60001 || connect.Key != testKey+"==" || connect.Host != "203.0.113.9" {
		t.Fatalf("connect = %+v", connect)
	}
	got := visible.String()
	if got != "Welcome\r\n" {
		t.Fatalf("visible = %q, want only the welcome line", got)
	}
	if strings.Contains(got, "MOSH") || strings.Contains(got, testKey) {
		t.Fatalf("visible stream leaked protocol data: %q", got)
	}
	if !sniffer.Connected() {
		t.Fatal("Connected() = false after parse")
	}
}

// A 22-character key prefix must wait for possible "==" in a later chunk.
func TestSnifferWaitsForPadding(t *testing.T) {
	sniffer := NewSniffer()
	out, parsed := sniffer.Feed([]byte("MOSH CONNECT 60001 " + testKey))
	if parsed != nil {
		t.Fatalf("parsed an unpadded key too early: %+v", parsed)
	}
	if strings.Contains(string(out), testKey) {
		t.Fatalf("visible leaked the partial key: %q", out)
	}
	out, parsed = sniffer.Feed([]byte("==\n"))
	if parsed == nil || parsed.Key != testKey+"==" {
		t.Fatalf("padded parse = %+v", parsed)
	}
	if len(out) != 0 {
		t.Fatalf("visible = %q, want empty", out)
	}
}

func TestSnifferFlushUnterminated(t *testing.T) {
	sniffer := NewSniffer()
	out, parsed := sniffer.Feed([]byte("banner\r\nMOSH CONNECT 60009 " + testKey))
	if parsed != nil {
		t.Fatal("parsed before flusher")
	}
	if string(out) != "banner\r\n" {
		t.Fatalf("feed visible = %q", out)
	}
	visible, parsed := sniffer.Flush()
	if parsed == nil {
		t.Fatalf("Flush did not recover the announcement; visible = %q", visible)
	}
	if parsed.Key != testKey || parsed.Port != 60009 {
		t.Fatalf("parsed = %+v", parsed)
	}
	if len(visible) != 0 {
		t.Fatalf("flush visible = %q, want empty", visible)
	}
}

func TestSnifferPassesOrdinaryOutput(t *testing.T) {
	sniffer := NewSniffer()
	out, parsed := sniffer.Feed([]byte("hello\r\nworld"))
	if parsed != nil || string(out) != "hello\r\nworld" {
		t.Fatalf("out = %q parsed = %+v", out, parsed)
	}
	out, _ = sniffer.Feed([]byte(" continues\n"))
	if want := " continues\n"; string(out) != want {
		t.Fatalf("out = %q, want %q", out, want)
	}
}

func TestSnifferPassesThroughAfterConnect(t *testing.T) {
	sniffer := NewSniffer()
	if _, parsed := sniffer.Feed([]byte("MOSH CONNECT 60001 " + testKey + "\n")); parsed == nil {
		t.Fatal("no parse")
	}
	out, parsed := sniffer.Feed([]byte("late"))
	if parsed != nil || string(out) != "late" {
		t.Fatalf("after connect out=%q parsed=%+v", out, parsed)
	}
}

func TestDecodeKey(t *testing.T) {
	want, err := base64.RawStdEncoding.DecodeString(testKey)
	if err != nil || len(want) != 16 {
		t.Fatalf("test key decode = %d bytes, %v", len(want), err)
	}
	for _, key := range []string{testKey, testKey + "=="} {
		decoded, err := DecodeKey(key)
		if err != nil {
			t.Fatalf("DecodeKey(%q): %v", key, err)
		}
		if !bytes.Equal(decoded, want) {
			t.Fatalf("DecodeKey(%q) = %x, want %x", key, decoded, want)
		}
		clear(decoded)
	}

	for _, key := range []string{
		"",
		"short",
		testKey + "X",
		testKey + "=",
		testKey[:21] + "!",
		testKey[:21] + "=",
	} {
		if _, err := DecodeKey(key); !errors.Is(err, ErrAnnouncement) {
			t.Fatalf("DecodeKey(%q) error = %v, want ErrAnnouncement", key, err)
		}
	}
}

func TestSnifferInvalidAnnouncement(t *testing.T) {
	t.Run("invalid port", func(t *testing.T) {
		sniffer := NewSniffer()
		out, parsed := sniffer.Feed([]byte("MOSH CONNECT 70000 " + testKey + "\r\n"))
		if parsed != nil {
			t.Fatalf("parsed an invalid announcement: %+v", parsed)
		}
		if !sniffer.InvalidAnnouncement() {
			t.Fatal("InvalidAnnouncement() = false for an unusable port")
		}
		if !strings.Contains(string(out), "MOSH CONNECT") {
			t.Fatalf("visible = %q, want the unusable line kept", out)
		}
	})

	t.Run("invalid key under Flush", func(t *testing.T) {
		sniffer := NewSniffer()
		if _, parsed := sniffer.Feed([]byte("MOSH CONNECT 60001 abc")); parsed != nil {
			t.Fatalf("parsed an invalid key: %+v", parsed)
		}
		if sniffer.InvalidAnnouncement() {
			t.Fatal("marker was reported invalid before the line ended")
		}
		visible, parsed := sniffer.Flush()
		if parsed != nil {
			t.Fatalf("Flush parsed an invalid key: %+v", parsed)
		}
		if !sniffer.InvalidAnnouncement() {
			t.Fatal("InvalidAnnouncement() = false after an unusable key")
		}
		if !strings.Contains(string(visible), "MOSH CONNECT") {
			t.Fatalf("flush visible = %q, want the unusable line kept", visible)
		}
	})

	t.Run("ordinary output stays valid", func(t *testing.T) {
		sniffer := NewSniffer()
		if _, parsed := sniffer.Feed([]byte("Welcome to the server\r\n")); parsed != nil {
			t.Fatalf("parsed ordinary output: %+v", parsed)
		}
		if sniffer.InvalidAnnouncement() {
			t.Fatal("ordinary output was marked invalid")
		}
	})

	t.Run("valid announcement stays valid", func(t *testing.T) {
		sniffer := NewSniffer()
		if _, parsed := sniffer.Feed([]byte("MOSH CONNECT 60001 " + testKey + "\r\n")); parsed == nil {
			t.Fatal("valid announcement was not parsed")
		}
		if sniffer.InvalidAnnouncement() {
			t.Fatal("valid announcement was marked invalid")
		}
	})
}

func TestRemoteCommand(t *testing.T) {
	cfg := Config{
		Hostname:   "example.test",
		ServerPath: "/opt/mosh bin/mosh-server",
		Lang:       "ja_JP.UTF-8",
		Locales: map[string]string{
			"LC_ALL":       "en_GB.UTF-8",
			"LANG":         "en_GB.UTF-8",
			"TERM_PROGRAM": "ignored",
		},
	}.Normalized()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	command := cfg.RemoteCommand()
	if !strings.HasPrefix(command, "sh -c '") {
		t.Fatalf("command = %q, want explicit sh -c", command)
	}
	for _, want := range []string{
		"$SSH_CONNECTION",
		`printf '\''\nMOSH IP %s\n'\'' "$3"`,
		`exec '\''/opt/mosh bin/mosh-server'\'' new -s -c 256`,
		`-l '\''LANG=en_GB.UTF-8'\''`,
		`-l '\''LC_ALL=en_GB.UTF-8'\''`,
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("command missing %q:\n%s", want, command)
		}
	}
	if strings.Contains(command, "TERM_PROGRAM") {
		t.Fatal("unknown locale variable was offered")
	}
	if strings.Index(command, "LANG=") > strings.Index(command, "LC_ALL=") {
		t.Fatal("locale order does not follow the stock list")
	}
}

func TestRemoteCommandDefaultsAndQuoting(t *testing.T) {
	cfg := Config{Hostname: "host"}.Normalized()
	script := cfg.RemoteScript()
	if !strings.Contains(script, `exec mosh-server new -s -c 256 -l 'LANG=en_US.UTF-8'`) {
		t.Fatalf("script = %q", script)
	}
	quoted := ShellQuote("a'b")
	if quoted != `'a'\''b'` {
		t.Fatalf("ShellQuote = %q", quoted)
	}
	cfg.ServerPath = "mosh-server'; touch /tmp/pwn; '"
	script = cfg.RemoteScript()
	if !strings.Contains(script, `exec 'mosh-server'\''; touch /tmp/pwn; '\'''`) {
		t.Fatalf("server path was not shell-quoted: %q", script)
	}
}

func TestConfigValidation(t *testing.T) {
	if err := (Config{}).Normalized().Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty host error = %v, want ErrInvalidConfig", err)
	}
	cfg := Config{Hostname: " host ", Username: " user ", Port: 2222}.Normalized()
	if cfg.Hostname != "host" || cfg.Port != 2222 || cfg.Lang != DefaultLang {
		t.Fatalf("normalized = %+v", cfg)
	}
	if cfg.Target() != "user@host" {
		t.Fatalf("target = %q", cfg.Target())
	}
}

func TestPredictionDisplayNormalizesToStockModes(t *testing.T) {
	cases := []struct {
		value PredictionDisplay
		want  PredictionDisplay
		env   string
	}{
		// Empty is stock's adaptive default, as is an unknown value from a
		// newer build: prediction is display-only and never blocks a Session.
		{"", PredictionAdaptive, "adaptive"},
		{"adaptive", PredictionAdaptive, "adaptive"},
		{"always", PredictionAlways, "always"},
		{"never", PredictionNever, "never"},
		{"experimental", PredictionAdaptive, "adaptive"},
	}
	for _, c := range cases {
		if got := c.value.Normalized(); got != c.want {
			t.Errorf("%q.Normalized() = %q, want %q", c.value, got, c.want)
		}
		if got := c.value.EnvValue(); got != c.env {
			t.Errorf("%q.EnvValue() = %q, want %q", c.value, got, c.env)
		}
	}
}

func TestConfigNormalizedCarriesThePredictionMode(t *testing.T) {
	for _, mode := range []PredictionDisplay{PredictionAdaptive, PredictionAlways, PredictionNever} {
		cfg := Config{Hostname: " host ", PredictionDisplay: mode}.Normalized()
		if cfg.PredictionDisplay != mode {
			t.Errorf("Normalized().PredictionDisplay = %q, want %q", cfg.PredictionDisplay, mode)
		}
	}
	cfg := Config{Hostname: "host"}.Normalized()
	if cfg.PredictionDisplay != PredictionAdaptive {
		t.Fatalf("empty prediction mode = %q, want adaptive", cfg.PredictionDisplay)
	}
}
