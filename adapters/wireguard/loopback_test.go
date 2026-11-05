// SPDX-License-Identifier: GPL-3.0-or-later

package wireguard

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/tun/netstack"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// keyPair derives a WireGuard key pair the same way the device does: X25519
// clamps the scalar, and the public key is the base point multiplied by it.
func keyPair(t *testing.T, seed byte) (private, public string) {
	t.Helper()
	privateBytes := make([]byte, 32)
	for i := range privateBytes {
		privateBytes[i] = seed + byte(i)
	}
	publicBytes, err := curve25519.X25519(privateBytes, curve25519.Basepoint)
	if err != nil {
		t.Fatalf("derive public key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(privateBytes),
		base64.StdEncoding.EncodeToString(publicBytes)
}

// loopbackProvider starts one real Provider over UDP loopback and publishes its
// statuses to a buffered channel.
func loopbackProvider(t *testing.T, config Config) (*Provider, <-chan Status) {
	t.Helper()
	events := make(chan Status, 1024)
	provider, err := NewProvider(config,
		WithStatusObserver(func(status Status) {
			select {
			case events <- status:
			default:
			}
		}),
		withStatusPollInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewProvider(%s): %v", config.ProfileID, err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if err := provider.Start(context.Background()); err != nil {
		t.Fatalf("Start(%s): %v", config.ProfileID, err)
	}
	return provider, events
}

// loopbackNet returns the live netstack of a running Provider.
func loopbackNet(t *testing.T, provider *Provider) *netstack.Net {
	t.Helper()
	provider.mu.Lock()
	backend := provider.backend
	provider.mu.Unlock()
	real, ok := backend.(*netstackBackend)
	if !ok {
		t.Fatal("the Provider is not backed by the real netstack")
	}
	real.mu.Lock()
	defer real.mu.Unlock()
	if real.net == nil {
		t.Fatal("the netstack is not running")
	}
	return real.net
}

func TestLoopbackPeersHandshakeAndCarryTCPThroughTheTunnel(t *testing.T) {
	privateA, publicA := keyPair(t, 0x11)
	privateB, publicB := keyPair(t, 0x44)

	serverConfig := Config{
		ProfileID:  "loop-a",
		Label:      "Loop A",
		Kind:       domainvault.NetworkKindWireGuard,
		Addresses:  []string{"10.66.0.1/32"},
		PrivateKey: privateA,
		Peers: []Peer{{
			ID:                  "peer-b",
			PublicKey:           publicB,
			AllowedIPs:          []string{"10.66.0.2/32"},
			PersistentKeepalive: 5,
		}},
	}
	server, serverEvents := loopbackProvider(t, serverConfig)
	listenPort := server.Status().ListenPort
	if listenPort == 0 || listenPort > 65535 {
		t.Fatalf("server listen port = %d, want the socket's actual port", listenPort)
	}

	clientConfig := Config{
		ProfileID:  "loop-b",
		Label:      "Loop B",
		Kind:       domainvault.NetworkKindWireGuard,
		Addresses:  []string{"10.66.0.2/32"},
		PrivateKey: privateB,
		Peers: []Peer{{
			ID:                  "peer-a",
			PublicKey:           publicA,
			Endpoint:            fmt.Sprintf("127.0.0.1:%d", listenPort),
			AllowedIPs:          []string{"10.66.0.1/32"},
			PersistentKeepalive: 5,
		}},
	}
	client, clientEvents := loopbackProvider(t, clientConfig)
	clientPort := client.Status().ListenPort

	listener, err := loopbackNet(t, server).ListenTCPAddrPort(
		netip.MustParseAddrPort("10.66.0.1:9000"))
	if err != nil {
		t.Fatalf("listen on the server netstack: %v", err)
	}
	defer listener.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		request := make([]byte, len("hello"))
		if _, err := io.ReadFull(conn, request); err != nil {
			accepted <- err
			return
		}
		if string(request) != "hello" {
			accepted <- fmt.Errorf("server read %q, want hello", request)
			return
		}
		_, err = conn.Write([]byte("world"))
		accepted <- err
	}()

	dialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := client.DialContext(dialCtx, "tcp", "10.66.0.1:9000")
	if err != nil {
		t.Fatalf("dial through the tunnel: %v", err)
	}
	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write through the tunnel: %v", err)
	}
	response := make([]byte, len("world"))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("read through the tunnel: %v", err)
	}
	if string(response) != "world" {
		t.Fatalf("client read %q, want world", response)
	}
	_ = conn.Close()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("server side: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server side never finished")
	}

	// The client learns the handshake it initiated, and the server learns the
	// client's roamed Endpoint from the authenticated packets.
	clientStatus := waitForStatus(t, clientEvents, 20*time.Second, func(status Status) bool {
		return status.Handshake == HandshakeEstablished &&
			status.RxBytes > 0 && status.TxBytes > 0
	})
	serverStatus := waitForStatus(t, serverEvents, 20*time.Second, func(status Status) bool {
		if len(status.Peers) != 1 {
			return false
		}
		return status.Handshake == HandshakeEstablished &&
			status.Peers[0].Endpoint != "" &&
			status.RxBytes > 0 && status.TxBytes > 0
	})
	wantEndpoint := fmt.Sprintf("127.0.0.1:%d", clientPort)
	if serverStatus.Peers[0].Endpoint != wantEndpoint {
		t.Fatalf("server learned endpoint %q, want %q",
			serverStatus.Peers[0].Endpoint, wantEndpoint)
	}
	if !clientStatus.LastHandshake.IsZero() {
		if age := time.Since(clientStatus.LastHandshake); age < 0 || age > time.Minute {
			t.Fatalf("client last handshake is implausible: %v", clientStatus.LastHandshake)
		}
	}
	if server.Status().Phase != appconnection.ProviderPhaseRunning ||
		client.Status().Phase != appconnection.ProviderPhaseRunning {
		t.Fatalf("phases = %q/%q, want running",
			server.Status().Phase, client.Status().Phase)
	}
}

func TestLoopbackProviderCloseStopsTheTunnel(t *testing.T) {
	private, public := keyPair(t, 0x77)
	config := Config{
		ProfileID:  "loop-close",
		Label:      "Loop Close",
		Kind:       domainvault.NetworkKindWireGuard,
		Addresses:  []string{"10.67.0.1/32"},
		PrivateKey: private,
		Peers: []Peer{{
			ID:         "peer",
			PublicKey:  public,
			Endpoint:   "127.0.0.1:51820",
			AllowedIPs: []string{"10.67.0.2/32"},
		}},
	}
	provider, _ := loopbackProvider(t, config)
	listenPort := provider.Status().ListenPort
	if listenPort == 0 {
		t.Fatal("the device did not report its listen port")
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if provider.Phase() != appconnection.ProviderPhaseStopped {
		t.Fatalf("phase = %q, want stopped", provider.Phase())
	}
	if _, err := provider.DialContext(context.Background(), "tcp", "10.67.0.2:22"); err == nil {
		t.Fatal("a closed Provider still dialed")
	}
}
