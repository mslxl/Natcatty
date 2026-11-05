// SPDX-License-Identifier: GPL-3.0-or-later

package wireguard

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestParseDeviceStatusReadsLiveFieldsAndIgnoresSecrets(t *testing.T) {
	privateKey := strings.Repeat("ab", 32)
	presharedKey := strings.Repeat("cd", 32)
	handshake := time.Unix(1_700_000_000, 123_456_789)
	output := strings.Join([]string{
		"private_key=" + privateKey,
		"listen_port=51820",
		"fwmark=0",
		"public_key=" + testPeerKey1,
		"preshared_key=" + presharedKey,
		"protocol_version=1",
		"endpoint=203.0.113.7:51820",
		"last_handshake_time_sec=1700000000",
		"last_handshake_time_nsec=123456789",
		"tx_bytes=512",
		"rx_bytes=1536",
		"persistent_keepalive_interval=25",
		"allowed_ip=10.9.0.0/24",
		"public_key=" + testPeerKey2,
		"preshared_key=" + strings.Repeat("00", 32),
		"last_handshake_time_sec=0",
		"last_handshake_time_nsec=0",
		"tx_bytes=0",
		"rx_bytes=0",
		"a_future_field=ignored",
		"",
	}, "\n")

	status, err := parseDeviceStatus(output)
	if err != nil {
		t.Fatalf("parseDeviceStatus: %v", err)
	}
	if status.ListenPort != 51820 {
		t.Fatalf("listen port = %d, want 51820", status.ListenPort)
	}
	if len(status.Peers) != 2 {
		t.Fatalf("peers = %d, want 2", len(status.Peers))
	}
	first := status.Peers[0]
	if first.PublicKeyHex != testPeerKey1 || first.Endpoint != "203.0.113.7:51820" ||
		first.RxBytes != 1536 || first.TxBytes != 512 {
		t.Fatalf("peer-1 = %+v", first)
	}
	if !first.LastHandshake.Equal(handshake) {
		t.Fatalf("peer-1 handshake = %v, want %v", first.LastHandshake, handshake)
	}
	second := status.Peers[1]
	if !second.LastHandshake.IsZero() {
		t.Fatalf("peer-2 handshake = %v, want zero for a peer that never handshook",
			second.LastHandshake)
	}
	if second.RxBytes != 0 || second.TxBytes != 0 {
		t.Fatalf("peer-2 counters = %+v", second)
	}

	rendered := fmt.Sprintf("%+v", status)
	if strings.Contains(rendered, privateKey) {
		t.Fatal("the parsed status carries the Private Key")
	}
	if strings.Contains(rendered, presharedKey) {
		t.Fatal("the parsed status carries a Preshared Key")
	}
}

func TestParseDeviceStatusRejectsMalformedNumbers(t *testing.T) {
	for _, line := range []string{
		"listen_port=nope",
		"last_handshake_time_sec=nope",
		"rx_bytes=nope",
		"tx_bytes=nope",
	} {
		output := "public_key=" + testPeerKey1 + "\n" + line + "\n"
		if _, err := parseDeviceStatus(output); err == nil {
			t.Fatalf("a malformed %q line was accepted", line)
		}
	}
	// A nanosecond line is only meaningful after a nonzero second line, so a
	// malformed one is only rejected in that context.
	output := "public_key=" + testPeerKey1 +
		"\nlast_handshake_time_sec=1700000000\nlast_handshake_time_nsec=nope\n"
	if _, err := parseDeviceStatus(output); err == nil {
		t.Fatal("a malformed nanosecond line was accepted after a handshake")
	}
}

func TestParseDeviceStatusAcceptsAResponseWithoutATrailingNewline(t *testing.T) {
	status, err := parseDeviceStatus("listen_port=1234")
	if err != nil {
		t.Fatalf("parseDeviceStatus: %v", err)
	}
	if status.ListenPort != 1234 {
		t.Fatalf("listen port = %d, want 1234", status.ListenPort)
	}
}

func TestNewNetstackBackendValidatesRequiredConfiguration(t *testing.T) {
	if _, err := newNetstackBackend(backendConfig{}); err == nil {
		t.Fatal("a config without a private key was accepted")
	}
	if _, err := newNetstackBackend(backendConfig{
		PrivateKeyHex: testPrivateKey,
	}); err == nil {
		t.Fatal("a config without a local address was accepted")
	}
	if _, err := newNetstackBackend(backendConfig{
		PrivateKeyHex:  testPrivateKey,
		LocalAddresses: []netip.Addr{netip.MustParseAddr("10.0.0.2")},
		Peers:          []backendPeer{{}},
	}); err == nil {
		t.Fatal("a peer without a public key was accepted")
	}
}
