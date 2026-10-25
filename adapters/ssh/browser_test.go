package ssh

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appbrowser "github.com/mslxl/natcatty/application/browser"
	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/domain/model"
)

// browserSSHNetwork starts a fresh fakessh server for every physical SSH
// dial. Two host-routed browser panels on the same Host must therefore cause
// two servers: a shared transport would cause one.
type browserSSHNetwork struct {
	origin string

	mu      sync.Mutex
	servers []*fakessh.Server
	conns   []net.Conn
	dials   int
}

func (n *browserSSHNetwork) dial(context.Context, string, string) (net.Conn, error) {
	client, server := fakessh.Pipe()
	origin := n.origin
	instance, err := fakessh.Start(server, fakessh.Options{
		Password: "pw",
		ForwardDial: func(string, int) (net.Conn, error) {
			return net.Dial("tcp", origin)
		},
	})
	if err != nil {
		_ = client.Close()
		_ = server.Close()
		return nil, err
	}
	n.mu.Lock()
	n.servers = append(n.servers, instance)
	n.conns = append(n.conns, client)
	n.dials++
	n.mu.Unlock()
	return client, nil
}

func (n *browserSSHNetwork) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.dials
}

func (n *browserSSHNetwork) server(index int) *fakessh.Server {
	n.mu.Lock()
	defer n.mu.Unlock()
	if index < 0 || index >= len(n.servers) {
		return nil
	}
	return n.servers[index]
}

func (n *browserSSHNetwork) close() {
	n.mu.Lock()
	servers := n.servers
	conns := n.conns
	n.servers = nil
	n.conns = nil
	n.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
	for _, server := range servers {
		_ = server.Close()
	}
}

// browserTunnel adapts a channel lease to application/browser's Tunnel.
type browserTunnel struct {
	lease *appconnection.Lease
	appconnection.ForwardingClient
}

func (t *browserTunnel) Done() <-chan struct{} { return t.lease.Done() }
func (t *browserTunnel) Discard()              { _ = t.lease.Discard() }

type browserConnector struct {
	connections *appconnection.Manager
}

func (c browserConnector) Connect(ctx context.Context, hostID model.HostID) (appbrowser.Tunnel, error) {
	lease, err := c.connections.Acquire(ctx, appconnection.Request{
		HostID:   hostID,
		Hostname: "fixture",
		Port:     22,
		Username: "root",
		// The panel's tunnel is fresh and is never lent to another lease.
		Dedicated: true,
		// A negative interval disables keepalive for the short-lived fixture.
		Keepalive: &appconnection.KeepalivePolicy{Interval: -1},
		LeaseKind: appconnection.LeaseChannel,
	})
	if err != nil {
		return nil, err
	}
	forwarding, err := lease.Forwarding()
	if err != nil {
		_ = lease.Discard()
		return nil, err
	}
	return &browserTunnel{lease: lease, ForwardingClient: forwarding}, nil
}

func TestBrowserPanelRoutesPagesThroughADedicatedRealSSHTransport(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<html><head><title>Remote Console</title></head><body>page from %s</body></html>", r.Host)
	}))
	defer origin.Close()
	originAddress := origin.Listener.Addr().String()

	network := &browserSSHNetwork{origin: originAddress}
	connections := appconnection.NewManager(
		NewDialer(
			WithDialFunc(network.dial),
			WithTimeouts(2*time.Second, 2*time.Second),
		),
		forwardPasswords{},
		forwardKnownHosts{},
		appconnection.WithPrompter(forwardPrompter{}),
		appconnection.WithIdleTTL(10*time.Second),
	)
	t.Cleanup(func() {
		connections.CloseAll()
		network.close()
	})
	connector := browserConnector{connections: connections}

	first := appbrowser.New(appbrowser.Config{HostID: "host-1", Label: "web-1", Connector: connector})
	second := appbrowser.New(appbrowser.Config{HostID: "host-1", Label: "web-1", Connector: connector})
	defer first.Close()
	defer second.Close()
	firstUpdates := browserUpdates(t, first)
	secondUpdates := browserUpdates(t, second)
	first.Start()
	firstState := browserWait(t, firstUpdates, "the first browser tunnel", func(state appbrowser.State) bool {
		return state.Route == appbrowser.RouteConnected
	})
	second.Start()
	secondState := browserWait(t, secondUpdates, "the second browser tunnel", func(state appbrowser.State) bool {
		return state.Route == appbrowser.RouteConnected
	})
	if network.count() != 2 {
		t.Fatalf("physical SSH dials = %d, want one dedicated transport per panel", network.count())
	}

	fetch := func(panel *appbrowser.Panel, tabID string) string {
		t.Helper()
		gatewayURL, err := panel.Navigate(tabID, "http://app.internal:8080/")
		if err != nil {
			t.Fatalf("Navigate: %v", err)
		}
		response, err := http.Get(gatewayURL)
		if err != nil {
			t.Fatalf("fetch through the gateway: %v", err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return string(body)
	}
	if body := fetch(first, firstState.ActiveID); !strings.Contains(body, "page from app.internal:8080") {
		t.Fatalf("first page body = %q", body)
	}
	if body := fetch(second, secondState.ActiveID); !strings.Contains(body, "page from app.internal:8080") {
		t.Fatalf("second page body = %q", body)
	}
	for index := 0; index < 2; index++ {
		server := network.server(index)
		forwards := server.Forwards()
		if len(forwards) != 1 || forwards[0].Host != "app.internal" || forwards[0].Port != 8080 {
			t.Fatalf("server %d direct-tcpip requests = %+v", index, forwards)
		}
	}

	// Closing one panel must release only its own transport; the other keeps
	// serving. The first server's connection ends, proving the dedicated
	// transport did not outlive its panel.
	firstServer := network.server(0)
	closed := make(chan struct{})
	go func() {
		firstServer.Wait()
		close(closed)
	}()
	first.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the first browser did not close its dedicated SSH transport")
	}
	if state := second.State(); state.Route != appbrowser.RouteConnected {
		t.Fatalf("the second browser lost its route: %+v", state)
	}
	if body := fetch(second, secondState.ActiveID); !strings.Contains(body, "page from app.internal:8080") {
		t.Fatalf("the second page stopped working: %q", body)
	}
}

func browserUpdates(t *testing.T, panel *appbrowser.Panel) <-chan appbrowser.State {
	t.Helper()
	updates := make(chan appbrowser.State, 32)
	panel.Subscribe(func(state appbrowser.State) {
		select {
		case updates <- state:
		default:
		}
	})
	return updates
}

func browserWait(t *testing.T, updates <-chan appbrowser.State, what string,
	want func(appbrowser.State) bool) appbrowser.State {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case state := <-updates:
			if want(state) {
				return state
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}
