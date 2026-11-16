package app

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// legacyOnlyKeyExchange is a KEX algorithm x/crypto/ssh implements but does not
// offer by default. A server limited to it negotiates only when the client's
// legacy opt-in added it, which is exactly the policy decision under test.
const legacyOnlyKeyExchange = "diffie-hellman-group14-sha1"

// legacyAlgorithmFixture wires the production Session factory to a real SSH
// handshake against a server that offers only legacyOnlyKeyExchange. Calling
// it again per subtest gives every case its own server and SSH manager.
func legacyAlgorithmFixture(t *testing.T, routes vaultRoutes, globalLegacy bool) *transportFactory {
	t.Helper()
	options := fakessh.Options{
		Password:     "s3cret",
		KeyExchanges: []string{legacyOnlyKeyExchange},
	}

	var mu sync.Mutex
	var servers []*fakessh.Server
	var conns []net.Conn
	dial := func(context.Context, string, string) (net.Conn, error) {
		clientConn, serverConn := fakessh.Pipe()
		server, err := fakessh.Start(serverConn, options)
		if err != nil {
			_ = clientConn.Close()
			_ = serverConn.Close()
			return nil, err
		}
		mu.Lock()
		servers = append(servers, server)
		conns = append(conns, clientConn)
		mu.Unlock()
		return clientConn, nil
	}
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(dial),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
	manager := appconnection.NewManager(
		dialer,
		vaultPasswords{vault: factoryPasswords{value: "s3cret"}},
		&factoryKnownHosts{},
		appconnection.WithPrompter(remoteCommandPrompter{}),
	)
	t.Cleanup(func() {
		manager.CloseAll()
		mu.Lock()
		started := append([]*fakessh.Server(nil), servers...)
		opened := append([]net.Conn(nil), conns...)
		mu.Unlock()
		for _, server := range started {
			_ = server.Close()
		}
		for _, conn := range opened {
			_ = conn.Close()
		}
	})
	return newTransportFactory(&factoryLocal{}, nil, nil, manager, routes).
		withTerminalSettings(factoryTerminalSettings{legacy: globalLegacy})
}

// TestLegacyAlgorithmOptInDecidesTheHandshake drives a real negotiation with a
// server that requires a legacy KEX. The terminal-wide opt-in must decide an
// Unsaved Host, a saved Host must use its own setting in both directions, and
// the failure must be the refused handshake rather than a successful dial.
func TestLegacyAlgorithmOptInDecidesTheHandshake(t *testing.T) {
	savedRoutes := func(hostLegacy bool) vaultRoutes {
		return vaultRoutes{vault: &fakeRouteVault{
			hosts: map[model.HostID]domainvault.Host{
				"saved": {
					ID: "saved", Hostname: "saved.example.com", Port: 22, Username: "root",
					LegacyAlgorithms: hostLegacy,
				},
			},
			secrets: map[string]string{},
		}}
	}

	tests := []struct {
		name         string
		globalLegacy bool
		hostLegacy   bool
		saved        bool
		wantError    bool
	}{
		{"global off refuses an Unsaved Host", false, false, false, true},
		{"global on connects an Unsaved Host", true, false, false, false},
		{"a saved Host off wins over the global on", true, false, true, true},
		{"a saved Host on wins over the global off", false, true, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			routes := vaultRoutes{vault: &fakeRouteVault{
				hosts:   map[model.HostID]domainvault.Host{},
				secrets: map[string]string{},
			}}
			spec := appsession.StartSpec{
				Kind: appsession.KindSSH, Hostname: "typed.example.com", Port: 22,
				Username: "root", Cols: 100, Rows: 30,
			}
			if test.saved {
				routes = savedRoutes(test.hostLegacy)
				spec.HostID = "saved"
				spec.Hostname = "saved.example.com"
			}
			factory := legacyAlgorithmFixture(t, routes, test.globalLegacy)

			type openResult struct {
				transport appsession.Transport
				err       error
			}
			got := awaitResult(t, "Open", func() (openResult, error) {
				transport, err := factory.Open(context.Background(), spec)
				return openResult{transport: transport, err: err}, nil
			})
			if test.wantError {
				if got.err == nil {
					if got.transport != nil {
						_ = got.transport.Close()
					}
					t.Fatal("Open succeeded, want the handshake refused without the legacy opt-in")
				}
				return
			}
			if got.err != nil {
				t.Fatalf("Open: %v", got.err)
			}
			if got.transport == nil {
				t.Fatal("Open returned neither a transport nor an error")
			}
			if err := got.transport.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}
