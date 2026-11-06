// SPDX-License-Identifier: GPL-3.0-or-later

package terminal

// FuzzTerminalParser feeds arbitrary host bytes through the same Complete
// pipeline mosh-server output takes: UTF-8 parser, escape-sequence state
// machine, dispatcher, framebuffer, and display encoder. Parser state, OSC
// collection, and parameter lists are bounded by the production structures;
// the fuzz target fails on a panic or on a framebuffer that grows beyond the
// requested size without an explicit resize.
//
//	go test ./internal/mosh/terminal -run '^$' -fuzz '^FuzzTerminalParser$' -fuzztime 20s

import "testing"

const fuzzInputLimit = 64 << 10

func FuzzTerminalParser(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain text\r\n",
		"\x1b[31mred\x1b[0m",
		"\x1b]0;title\x07",
		"\x1b]52;c;ZGF0YQ==\x07",
		"\x1b[?1049h\x1b[2J\x1b[H",
		"\x1bPq#0;2;0;0;0#0~~\x1b\\",
		"\x1b[38;5;200mtrue\x1b[48:2::1:2:3mcolor",
		"\x1b[999;999H",
		"wide 世界 combining é",
		"\x1b[1;1H\x1b[K\x1bM\x1b[3L",
		"\xff\xfe\xfd",
		"\x1b]999;unterminated",
	} {
		f.Add([]byte(seed))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		const width, height = 80, 24
		complete := NewComplete(width, height)
		_ = complete.Act(string(data))
		if complete.Framebuffer() == nil {
			t.Fatal("Complete lost its framebuffer")
		}
		if got := complete.Framebuffer().DS.width; got != width {
			t.Fatalf("framebuffer width grew to %d without a resize", got)
		}
		if got := complete.Framebuffer().DS.height; got != height {
			t.Fatalf("framebuffer height grew to %d without a resize", got)
		}
	})
}
