// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	appconnection "github.com/mslxl/natcatty/application/connection"
	"google.golang.org/protobuf/proto"
)

// countedLease is a fakeLease that records how many times the factory ran the
// bootstrap on it and whether it is still considered alive.
type countedLease struct {
	*fakeLease
	mu    sync.Mutex
	execs int
}

func (l *countedLease) OpenExec(ctx context.Context, command string) (appconnection.ExecStream, error) {
	l.mu.Lock()
	l.execs++
	l.mu.Unlock()
	return l.fakeLease.OpenExec(ctx, command)
}

func (l *countedLease) execCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.execs
}

func (l *countedLease) die() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

// requireEventually waits on an observable condition with a hard deadline.
func requireEventually(t *testing.T, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// recvTerminalInput waits for the server to receive want in one or more
// TERMINAL_BUFFER packets, skipping liveness probes and other traffic. It
// fails when the deadline passes with only part of the input.
func recvTerminalInput(t *testing.T, server *etservertest.Server, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var input strings.Builder
	for {
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		packet, err := server.Recv(ctx)
		cancel()
		if err != nil {
			t.Fatalf("server did not receive %q: %v; received %q", want, err, input.String())
		}
		if packet.Header != protocol.HeaderTerminalBuffer {
			continue
		}
		buffer := &protocol.TerminalBuffer{}
		if err := proto.Unmarshal(packet.Payload, buffer); err != nil {
			t.Fatalf("decode replayed TerminalBuffer: %v", err)
		}
		input.Write(buffer.GetBuffer())
		if strings.Contains(input.String(), want) {
			return
		}
	}
}

// TestNetworkChangeReconnectsWithANewLease proves a lost SSH lease is
// replaced without opening a new bootstrap session: the ET client reacquires a
// lease, redials etserver through it, and replays the same sequence state, so
// the shell and its output survive the network change.
func TestNetworkChangeReconnectsWithANewLease(t *testing.T) {
	server := etservertest.NewServer(testID, testPasskey)
	network := etservertest.NewNetwork(server)
	t.Cleanup(network.Close)

	newLease := func() *countedLease {
		return &countedLease{fakeLease: newFakeLease(credentialsExec(), &recordingForwarder{
			dial: func(context.Context) (net.Conn, error) {
				return network.DialContext(context.Background(), "tcp", "127.0.0.1:2022")
			},
		})}
	}
	first := newLease()
	var mu sync.Mutex
	leases := []*countedLease{first}
	factory := InProcessFactory{KeepAlive: 100 * time.Millisecond}
	acquired := 0
	factory.Acquire = func(context.Context, appconnection.Request) (ConnectionLease, error) {
		mu.Lock()
		defer mu.Unlock()
		acquired++
		if acquired == 1 {
			return first, nil
		}
		next := newLease()
		leases = append(leases, next)
		return next, nil
	}

	// The fake server does not answer INITIAL_PAYLOAD itself.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := server.Recv(ctx); err != nil {
			return
		}
		response := &protocol.InitialResponse{}
		encoded, err := proto.Marshal(response)
		if err != nil {
			return
		}
		_ = server.Send(context.Background(), protocol.Packet{
			Header: protocol.HeaderInitialResponse, Payload: encoded,
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport, err := factory.Open(ctx, inProcessStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	_ = recvPacket(t, server) // initial TERMINAL_INFO

	if err := server.Send(context.Background(), protocol.Packet{
		Header: protocol.HeaderTerminalBuffer,
		Payload: mustMarshal(t, &protocol.TerminalBuffer{}, func(m *protocol.TerminalBuffer) {
			m.SetBuffer([]byte("before the network change"))
		}),
	}); err != nil {
		t.Fatalf("Send before: %v", err)
	}
	readTransport(t, transport, "before the network change")

	// A network change: the SSH transport dies and its forwarding channel can
	// no longer dial. The ET session at etserver stays alive.
	first.die()
	network.SetRefuse(true)
	network.CutAll()

	// Input typed while the link is down is sealed once and retained in the
	// replay window; the replacement link must deliver it exactly once.
	const queuedInput = "typed during the network change"
	if _, err := transport.Write([]byte(queuedInput)); err != nil {
		t.Fatalf("Write during the network change: %v", err)
	}

	requireEventually(t, "a replacement lease", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(leases) >= 2
	})
	network.SetRefuse(false)

	if err := server.Send(context.Background(), protocol.Packet{
		Header: protocol.HeaderTerminalBuffer,
		Payload: mustMarshal(t, &protocol.TerminalBuffer{}, func(m *protocol.TerminalBuffer) {
			m.SetBuffer([]byte("after the network change"))
		}),
	}); err != nil {
		t.Fatalf("Send after: %v", err)
	}
	text := readTransport(t, transport, "after the network change")
	if strings.Contains(text, "before the network change") {
		t.Fatalf("reconnect replayed output into the live terminal: %q", text)
	}
	recvTerminalInput(t, server, queuedInput)

	mu.Lock()
	defer mu.Unlock()
	if got := first.execCount(); got != 1 {
		t.Fatalf("bootstrap ran %d times on the first lease, want 1", got)
	}
	for i, lease := range leases[1:] {
		if got := lease.execCount(); got != 0 {
			t.Fatalf("replacement lease %d ran the bootstrap %d times, want 0", i+1, got)
		}
	}
}
