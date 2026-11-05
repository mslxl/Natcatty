// SPDX-License-Identifier: GPL-3.0-or-later

package wireguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// errBackendClosed reports an operation on a device that was already released.
var errBackendClosed = errors.New("wireguard: device is closed")

// backend is the userspace WireGuard device the Provider drives. It exists as a
// seam so the Provider's state machine, resolution, redaction, and status
// mapping can be tested without creating a gVisor network stack.
type backend interface {
	// Start creates the netstack and device and brings WireGuard up. The
	// returned status includes the actual UDP listen port.
	Start(ctx context.Context) (backendStatus, error)
	// Dial dials a TCP target through the netstack.
	Dial(ctx context.Context, network, address string) (net.Conn, error)
	// Status samples the live device. It never blocks on the network.
	Status(ctx context.Context) (backendStatus, error)
	// SetEndpoints applies new Endpoint addresses for configured peers.
	SetEndpoints(ctx context.Context, endpoints map[string]string) error
	// Close releases the device. It is idempotent.
	Close() error
}

// backendConfig is the resolved device configuration. Keys remain in memory
// only and are never logged or exposed by a Status.
type backendConfig struct {
	PrivateKeyHex  string
	ListenPort     uint16
	MTU            int
	LocalAddresses []netip.Addr
	DNSServers     []netip.Addr
	Peers          []backendPeer
	Logf           func(format string, args ...any)
}

// backendPeer is one peer handed to the UAPI. Endpoint is already an IP:port.
type backendPeer struct {
	PublicKeyHex        string
	PresharedKeyHex     string
	Endpoint            string
	AllowedIPs          []netip.Prefix
	PersistentKeepalive uint16
}

// backendPeerStatus is one peer's live, non-secret state read from the UAPI.
// PresharedKey is deliberately not carried out of the parser.
type backendPeerStatus struct {
	PublicKeyHex  string
	Endpoint      string
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
}

// backendStatus is the live device state read from the UAPI.
type backendStatus struct {
	Phase      appconnection.ProviderPhase
	ListenPort int
	Peers      []backendPeerStatus
}

// netstackBackend is the real backend. It owns exactly one wireguard-go Device
// and one gVisor netstack. It never creates an operating-system interface, so
// no TUN driver, route change, administrator right, or sidecar is involved.
type netstackBackend struct {
	config backendConfig

	mu      sync.Mutex
	started bool
	closed  bool
	dev     *device.Device
	net     *netstack.Net

	// publicKeys is the set of configured peer public keys in hexadecimal.
	publicKeys map[string]bool
}

// newNetstackBackend constructs an unstarted backend. It performs no I/O.
func newNetstackBackend(config backendConfig) (backend, error) {
	if config.PrivateKeyHex == "" {
		return nil, errors.New("wireguard: device configuration has no private key")
	}
	if len(config.LocalAddresses) == 0 {
		return nil, errors.New("wireguard: device configuration has no local address")
	}
	publicKeys := make(map[string]bool, len(config.Peers))
	for _, peer := range config.Peers {
		if peer.PublicKeyHex == "" {
			return nil, errors.New("wireguard: device configuration has a peer without a public key")
		}
		publicKeys[peer.PublicKeyHex] = true
	}
	return &netstackBackend{config: config, publicKeys: publicKeys}, nil
}

// Start creates the netstack and device, applies the profile, and brings the
// device up. Every failure path releases everything it created.
func (b *netstackBackend) Start(ctx context.Context) (backendStatus, error) {
	if err := ctx.Err(); err != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseStopped}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return backendStatus{Phase: appconnection.ProviderPhaseStopped}, errBackendClosed
	}
	if b.started {
		return b.statusLocked()
	}

	tunDevice, netStack, err := netstack.CreateNetTUN(
		b.config.LocalAddresses, b.config.DNSServers, b.config.MTU)
	if err != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseError},
			fmt.Errorf("wireguard: create netstack: %w", err)
	}

	logger := &device.Logger{Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf}
	if b.config.Logf != nil {
		logger.Errorf = b.config.Logf
	}
	dev := device.NewDevice(tunDevice, conn.NewDefaultBind(), logger)
	if err := dev.IpcSet(b.uapiConfig()); err != nil {
		dev.Close()
		return backendStatus{Phase: appconnection.ProviderPhaseError},
			fmt.Errorf("wireguard: apply configuration: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return backendStatus{Phase: appconnection.ProviderPhaseError},
			fmt.Errorf("wireguard: bring device up: %w", err)
	}

	b.dev = dev
	b.net = netStack
	b.started = true
	status, err := b.statusLocked()
	if err != nil {
		dev.Close()
		b.dev = nil
		b.net = nil
		b.started = false
		return backendStatus{Phase: appconnection.ProviderPhaseError}, err
	}
	return status, nil
}

// uapiConfig renders the one-shot configuration. Values are keys in hex,
// numeric ports, and prefixes, so no line needs escaping. The caller holds the
// backend lock.
func (b *netstackBackend) uapiConfig() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "private_key=%s\n", b.config.PrivateKeyHex)
	if b.config.ListenPort != 0 {
		fmt.Fprintf(&builder, "listen_port=%d\n", b.config.ListenPort)
	}
	builder.WriteString("replace_peers=true\n")
	for _, peer := range b.config.Peers {
		fmt.Fprintf(&builder, "public_key=%s\n", peer.PublicKeyHex)
		if peer.PresharedKeyHex != "" {
			fmt.Fprintf(&builder, "preshared_key=%s\n", peer.PresharedKeyHex)
		}
		if peer.Endpoint != "" {
			fmt.Fprintf(&builder, "endpoint=%s\n", peer.Endpoint)
		}
		if peer.PersistentKeepalive != 0 {
			fmt.Fprintf(&builder, "persistent_keepalive_interval=%d\n", peer.PersistentKeepalive)
		}
		for _, prefix := range peer.AllowedIPs {
			fmt.Fprintf(&builder, "allowed_ip=%s\n", prefix.String())
		}
	}
	return builder.String()
}

// Dial carries one TCP connection through the netstack. A target host name is
// resolved by the netstack with the tunnel's DNS servers only.
func (b *netstackBackend) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	b.mu.Lock()
	closed, started, netStack := b.closed, b.started, b.net
	b.mu.Unlock()
	if closed {
		return nil, errBackendClosed
	}
	if !started || netStack == nil {
		return nil, errors.New("wireguard: device is not running")
	}
	return netStack.DialContext(ctx, network, address)
}

// Status reads the device's live state from its UAPI. It reports a phase the
// Provider maps to a typed failure, and never includes key material.
func (b *netstackBackend) Status(ctx context.Context) (backendStatus, error) {
	if err := ctx.Err(); err != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseStopped}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statusLocked()
}

// statusLocked samples the device. The caller holds the backend lock.
func (b *netstackBackend) statusLocked() (backendStatus, error) {
	if b.closed {
		return backendStatus{Phase: appconnection.ProviderPhaseStopped}, errBackendClosed
	}
	if !b.started || b.dev == nil {
		return backendStatus{Phase: appconnection.ProviderPhaseStopped}, nil
	}
	select {
	case <-b.dev.Wait():
		return backendStatus{Phase: appconnection.ProviderPhaseError},
			errors.New("wireguard: device stopped unexpectedly")
	default:
	}
	output, err := b.dev.IpcGet()
	if err != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseError},
			fmt.Errorf("wireguard: read device status: %w", err)
	}
	status, err := parseDeviceStatus(output)
	if err != nil {
		return backendStatus{Phase: appconnection.ProviderPhaseError}, err
	}
	status.Phase = appconnection.ProviderPhaseRunning
	return status, nil
}

// SetEndpoints applies re-resolved Endpoint addresses. The caller maps a peer's
// public key in hexadecimal to its new IP:port.
func (b *netstackBackend) SetEndpoints(ctx context.Context, endpoints map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(endpoints) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errBackendClosed
	}
	if !b.started || b.dev == nil {
		return errors.New("wireguard: device is not running")
	}
	var builder strings.Builder
	for publicKey, address := range endpoints {
		if !b.publicKeys[publicKey] {
			continue
		}
		fmt.Fprintf(&builder, "public_key=%s\nendpoint=%s\n", publicKey, address)
	}
	if builder.Len() == 0 {
		return nil
	}
	if err := b.dev.IpcSet(builder.String()); err != nil {
		return fmt.Errorf("wireguard: update endpoint: %w", err)
	}
	return nil
}

// Close releases the device and its netstack. It is idempotent and also closes
// a device that was created but never brought up.
func (b *netstackBackend) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	dev := b.dev
	b.dev = nil
	b.net = nil
	b.started = false
	b.mu.Unlock()
	if dev != nil {
		dev.Close()
	}
	return nil
}

// parseDeviceStatus parses the UAPI get output. It deliberately ignores
// private_key and preshared_key: neither ever leaves the device. A peer entry
// starts at public_key, so a public_key that is also a valid key line is
// treated as a peer boundary, never as a secret to copy.
func parseDeviceStatus(output string) (backendStatus, error) {
	var status backendStatus
	var peer *backendPeerStatus
	flush := func() {
		if peer != nil {
			status.Peers = append(status.Peers, *peer)
			peer = nil
		}
	}
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return backendStatus{}, fmt.Errorf("wireguard: malformed device status line %q", key)
		}
		switch key {
		case "listen_port":
			port, err := strconv.ParseUint(value, 10, 16)
			if err != nil {
				return backendStatus{}, fmt.Errorf("wireguard: malformed device listen_port")
			}
			status.ListenPort = int(port)
		case "public_key":
			flush()
			peer = &backendPeerStatus{PublicKeyHex: value}
		case "private_key", "preshared_key", "fwmark", "protocol_version":
			// Secret or irrelevant. The private key and any Preshared Key must
			// never be copied into a Status.
		case "endpoint":
			if peer != nil {
				peer.Endpoint = value
			}
		case "last_handshake_time_sec":
			if peer == nil {
				continue
			}
			seconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return backendStatus{}, fmt.Errorf("wireguard: malformed device handshake time")
			}
			// Zero seconds means no handshake has completed. It must not become
			// time.Unix(0, 0), which is a real instant and would be reported as
			// an established handshake at the Unix epoch.
			if seconds > 0 {
				peer.LastHandshake = time.Unix(seconds, 0)
			}
		case "last_handshake_time_nsec":
			if peer == nil || peer.LastHandshake.IsZero() {
				continue
			}
			nanos, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return backendStatus{}, fmt.Errorf("wireguard: malformed device handshake time")
			}
			peer.LastHandshake = peer.LastHandshake.Add(time.Duration(nanos))
		case "rx_bytes":
			if peer == nil {
				continue
			}
			bytes, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return backendStatus{}, fmt.Errorf("wireguard: malformed device rx_bytes")
			}
			peer.RxBytes = bytes
		case "tx_bytes":
			if peer == nil {
				continue
			}
			bytes, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return backendStatus{}, fmt.Errorf("wireguard: malformed device tx_bytes")
			}
			peer.TxBytes = bytes
		case "persistent_keepalive_interval", "allowed_ip":
			// Configured values the Provider already knows.
		default:
			// A future wireguard-go may add device-level fields. Unknown lines
			// are skipped rather than failing the whole status read.
		}
	}
	flush()
	return status, nil
}
