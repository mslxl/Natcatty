// SPDX-License-Identifier: GPL-3.0-or-later

package wireguard

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestRealPeerTCP is the opt-in end-to-end test against a real WireGuard peer.
// It is skipped unless the environment names a peer and a TCP target, so the
// default test run never touches an external network.
//
// Required environment variables:
//
//	NATCATTY_WIREGUARD_E2E=1
//	NATCATTY_WIREGUARD_E2E_PRIVATE_KEY   local Private Key, base64 or hex
//	NATCATTY_WIREGUARD_E2E_PEER_PUBLIC_KEY
//	NATCATTY_WIREGUARD_E2E_PEER_ENDPOINT host:port reachable on the physical network
//	NATCATTY_WIREGUARD_E2E_ALLOWED_IPS   comma-separated prefixes for that peer
//	NATCATTY_WIREGUARD_E2E_TARGET        host:port to dial through the tunnel
//
// Optional:
//
//	NATCATTY_WIREGUARD_E2E_ADDRESSES comma-separated local addresses (default 10.0.0.2/32)
//	NATCATTY_WIREGUARD_E2E_DNS       comma-separated tunnel DNS servers
func TestRealPeerTCP(t *testing.T) {
	if os.Getenv("NATCATTY_WIREGUARD_E2E") != "1" {
		t.Skip("set NATCATTY_WIREGUARD_E2E=1 to run the real-peer test")
	}
	privateKey := os.Getenv("NATCATTY_WIREGUARD_E2E_PRIVATE_KEY")
	peerKey := os.Getenv("NATCATTY_WIREGUARD_E2E_PEER_PUBLIC_KEY")
	peerEndpoint := os.Getenv("NATCATTY_WIREGUARD_E2E_PEER_ENDPOINT")
	allowedIPs := splitList(os.Getenv("NATCATTY_WIREGUARD_E2E_ALLOWED_IPS"))
	target := os.Getenv("NATCATTY_WIREGUARD_E2E_TARGET")
	if privateKey == "" || peerKey == "" || peerEndpoint == "" ||
		len(allowedIPs) == 0 || target == "" {
		t.Skip("set the NATCATTY_WIREGUARD_E2E_* peer, AllowedIPs, and target variables")
	}
	addresses := splitList(os.Getenv("NATCATTY_WIREGUARD_E2E_ADDRESSES"))
	if len(addresses) == 0 {
		addresses = []string{"10.0.0.2/32"}
	}

	config := Config{
		ProfileID:  "wireguard-e2e",
		Label:      "WireGuard E2E",
		Kind:       domainvault.NetworkKindWireGuard,
		Addresses:  addresses,
		DNS:        splitList(os.Getenv("NATCATTY_WIREGUARD_E2E_DNS")),
		PrivateKey: privateKey,
		Peers: []Peer{{
			ID:         "peer-e2e",
			PublicKey:  peerKey,
			Endpoint:   peerEndpoint,
			AllowedIPs: allowedIPs,
		}},
	}
	updates := make(chan Status, 16)
	provider, err := NewProvider(config,
		WithStatusObserver(func(status Status) {
			select {
			case updates <- status:
			default:
			}
		}),
		// The default poll interval is a display cadence, too slow to wait on in
		// a test.
		withStatusPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer func() { _ = provider.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := provider.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatalf("dial %s through the real peer: %v", target, err)
	}
	_ = conn.Close()

	// A successful dial proves the device completed its handshake, but the
	// published Status is sampled by the poll goroutine. Wait for a real
	// established event with a hard deadline instead of reading the cached
	// status immediately.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		status := provider.Status()
		if status.Phase == appconnection.ProviderPhaseRunning &&
			status.Handshake == HandshakeEstablished {
			break
		}
		select {
		case <-updates:
		case <-deadline.C:
			t.Fatalf("status after a successful dial = %+v, want running with an established handshake",
				provider.Status())
		}
	}

	status := provider.Status()
	if status.Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("phase = %q, want running", status.Phase)
	}
	rendered := status.Endpoint()
	if strings.Contains(rendered, privateKey) {
		t.Fatal("the status leaks the Private Key")
	}
}

func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}
