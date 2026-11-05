// SPDX-License-Identifier: GPL-3.0-or-later

package connection

import (
	"fmt"
	"time"

	"github.com/mslxl/natcatty/ui/locales"
)

// ByteCount formats a transfer counter with binary units. The digits and unit
// suffix are locale-neutral data, like a timestamp: the UI localizes the label
// around them, not the measurement itself.
func ByteCount(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes) / unit
	for _, suffix := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
		value /= unit
	}
	return fmt.Sprintf("%.1f EiB", value)
}

// Timestamp formats a record time as locale-neutral data. An absent time
// renders empty so a caller can choose its own fallback text.
func Timestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

// HandshakeTime formats a WireGuard handshake time. A zero time means the peer
// has never completed a handshake, which is a distinct state, not a timestamp.
func HandshakeTime(m locales.Messages, value time.Time) string {
	if value.IsZero() {
		return m.NetworkHandshakeNever()
	}
	return Timestamp(value)
}

// WireGuardDetail localizes the live WireGuard details a Profile row and a
// Connection Log row share: handshake state, last handshake time, and the rx/tx
// counters. It never includes key material.
func WireGuardDetail(m locales.Messages, established bool, lastHandshake time.Time,
	rxBytes, txBytes uint64) string {
	handshake := m.NetworkHandshakePending()
	if established {
		handshake = m.NetworkHandshakeEstablished()
	}
	return m.NetworkWireGuardDetail(handshake, HandshakeTime(m, lastHandshake),
		ByteCount(rxBytes), ByteCount(txBytes))
}
