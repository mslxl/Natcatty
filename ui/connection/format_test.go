// SPDX-License-Identifier: GPL-3.0-or-later

package connection

import (
	"testing"
	"time"

	"github.com/mslxl/natcatty/ui/locales"
)

func TestByteCountUsesBinaryUnits(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1536 * 1024, "1.5 MiB"},
		{3 * 1024 * 1024 * 1024, "3.0 GiB"},
	}
	for _, test := range tests {
		if got := ByteCount(test.bytes); got != test.want {
			t.Errorf("ByteCount(%d) = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func TestWireGuardDetailLocalizesHandshakeAndTraffic(t *testing.T) {
	m := locales.In("en")
	handshake := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	want := "Handshake established · last " + Timestamp(handshake) + " · rx 1.5 KiB · tx 512 B"
	if got := WireGuardDetail(m, true, handshake, 1536, 512); got != want {
		t.Fatalf("WireGuardDetail = %q, want %q", got, want)
	}

	want = "No handshake yet · last never · rx 0 B · tx 0 B"
	if got := WireGuardDetail(m, false, time.Time{}, 0, 0); got != want {
		t.Fatalf("WireGuardDetail without a handshake = %q, want %q", got, want)
	}

	if got := HandshakeTime(m, time.Time{}); got != m.NetworkHandshakeNever() {
		t.Fatalf("HandshakeTime(zero) = %q, want the localized never text", got)
	}
	if got := HandshakeTime(m, handshake); got != Timestamp(handshake) {
		t.Fatalf("HandshakeTime = %q, want %q", got, Timestamp(handshake))
	}
}
