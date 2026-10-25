package mosh

import (
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

func TestClientEnv(t *testing.T) {
	env := ClientEnv([]string{
		"PATH=/bin",
		"TERM=screen-256color",
		"LANG=fr_FR.UTF-8",
		"MOSH_KEY=old",
		"MOSH_FALLBACK_HOST=old.example",
	}, "newkey", "en_US.UTF-8", "203.0.113.5")
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"PATH=/bin",
		"TERM=screen-256color",
		"LANG=fr_FR.UTF-8",
		"MOSH_KEY=newkey",
		"MOSH_FALLBACK_HOST=203.0.113.5",
	} {
		if !strings.Contains(joined, want+"\n") && !strings.HasSuffix(joined, want) {
			t.Fatalf("env missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "MOSH_KEY=old") {
		t.Fatal("stale MOSH_KEY survived")
	}
	if strings.Count(joined, "MOSH_FALLBACK_HOST=") != 1 {
		t.Fatal("MOSH_FALLBACK_HOST duplicated")
	}

	empty := ClientEnv(nil, "key", "", "")
	joined = strings.Join(empty, "\n")
	if !strings.Contains(joined, "TERM="+DefaultTerm) || !strings.Contains(joined, "LANG="+DefaultLang) {
		t.Fatalf("defaults missing: %v", empty)
	}
}

func TestClientArgs(t *testing.T) {
	args, err := ClientArgs("203.0.113.5", 60001)
	if err != nil || len(args) != 2 || args[0] != "203.0.113.5" || args[1] != "60001" {
		t.Fatalf("args = %v err = %v", args, err)
	}
	if _, err := ClientArgs("", 60001); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty host error = %v", err)
	}
	if _, err := ClientArgs("host", 0); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("zero port error = %v", err)
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
