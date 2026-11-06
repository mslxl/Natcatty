// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

// FuzzAnnouncementParser feeds arbitrary mosh-server bootstrap output to the
// announcement sniffer and key decoder. The sniffer buffers only up to the
// connect line (plus its bounded pre-connect passthrough), and DecodeKey
// rejects every length but the 22-character base64 form. The target fails on
// a panic or an unbounded buffered announcement.
//
//	go test ./domain/model/mosh -run '^$' -fuzz '^FuzzAnnouncementParser$' -fuzztime 20s

import "testing"

const fuzzInputLimit = 64 << 10

func FuzzAnnouncementParser(f *testing.F) {
	f.Add([]byte("MOSH CONNECT 60001 0123456789abcdefghijkl\r\n"))
	f.Add([]byte("MOSH IP 192.0.2.1\r\nMOSH CONNECT 60001 0123456789abcdefghijkl\r\n"))
	f.Add([]byte("Welcome\r\n"))
	f.Add([]byte("MOSH CONNECT"))
	f.Add([]byte("MOSH CONNECT 99999 !!!!!!!!!!!!!!!!!!!!!!\r\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > fuzzInputLimit {
			t.Skip()
		}
		sniffer := NewSniffer()
		for offset := 0; offset < len(data); offset += 7 {
			end := offset + 7
			if end > len(data) {
				end = len(data)
			}
			_, _ = sniffer.Feed(data[offset:end])
		}
		_, _ = sniffer.Flush()
		if _, ok := ParseConnectLine(string(data)); ok {
			_ = ValidKey("0123456789abcdefghijkl")
		}
		_, _ = DecodeKey("0123456789abcdefghijkl")
	})
}
