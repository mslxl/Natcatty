// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
)

// faultProxy relays UDP datagrams between a mosh client and a fake or real
// mosh-server. Loss, duplication, reordering, and source address changes are
// driven by counters and explicit test calls rather than wall-clock races.
// A proxy-level rebind changes the source endpoint the server sees, which is
// what a NAT rebinding or a network switch produces.
type faultProxy struct {
	t      *testing.T
	front  *net.UDPConn
	server *net.UDPAddr

	mu         sync.Mutex
	clientAddr *net.UDPAddr
	upstream   *net.UDPConn
	// replyFront sends server-to-client datagrams. It normally aliases front;
	// SwitchServerSource replaces it with a socket on a different port so the
	// client sees replies from a new source address.
	replyFront *net.UDPConn
	streams    map[*net.UDPConn]bool
	pending    [2][]byte
	timers     [2]*time.Timer
	closed     bool
	rebinds    int
	switches   int

	// Loss policy, set before the first client datagram.
	dropClientThrough atomic.Int32
	dropClientEvery   atomic.Int32
	dropServerEvery   atomic.Int32

	// Dynamic policy toggled by tests.
	blackhole      atomic.Bool
	duplicate      atomic.Bool
	reorder        atomic.Bool
	dropNextClient atomic.Int32
	dropNextServer atomic.Int32

	clientSeq int
	serverSeq int
	drops     atomic.Int32
	stop      chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
}

func startFaultProxy(t *testing.T, server *net.UDPAddr) *faultProxy {
	t.Helper()
	front, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	upstream, err := net.ListenUDP("udp4", nil)
	if err != nil {
		_ = front.Close()
		t.Fatalf("proxy upstream: %v", err)
	}
	proxy := &faultProxy{
		t:        t,
		front:    front,
		server:   server,
		streams:  map[*net.UDPConn]bool{upstream: true},
		upstream: upstream,
		stop:     make(chan struct{}),
	}
	proxy.wg.Add(2)
	go proxy.readFront()
	go proxy.readUpstream(upstream)
	t.Cleanup(proxy.Close)
	return proxy
}

func (p *faultProxy) Port() int { return p.front.LocalAddr().(*net.UDPAddr).Port }

func (p *faultProxy) Close() {
	p.closeOnce.Do(func() {
		close(p.stop)
		p.mu.Lock()
		p.closed = true
		streams := make([]*net.UDPConn, 0, len(p.streams))
		for stream := range p.streams {
			streams = append(streams, stream)
			delete(p.streams, stream)
		}
		p.upstream = nil
		p.replyFront = nil
		for direction := range p.timers {
			if p.timers[direction] != nil {
				p.timers[direction].Stop()
				p.timers[direction] = nil
			}
		}
		p.mu.Unlock()
		_ = p.front.Close()
		for _, stream := range streams {
			_ = stream.Close()
		}
		p.wg.Wait()
	})
}

// Rebind opens a new upstream socket. The next client datagram reaches the
// server from a different source address, like a NAT rebinding or a network
// switch. Datagrams still in flight to the old socket are lost.
func (p *faultProxy) Rebind() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	old := p.upstream
	upstream, err := net.ListenUDP("udp4", nil)
	if err != nil {
		p.mu.Unlock()
		p.t.Errorf("proxy rebind: %v", err)
		return
	}
	p.upstream = upstream
	p.streams[upstream] = true
	p.rebinds++
	p.wg.Add(1)
	p.mu.Unlock()

	if old != nil {
		_ = old.Close()
		p.mu.Lock()
		delete(p.streams, old)
		p.mu.Unlock()
	}
	go p.readUpstream(upstream)
}

// Rebinds reports how many times the proxy changed its upstream source.
func (p *faultProxy) Rebinds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rebinds
}

// UpstreamAddr reports the source address the server currently sees.
func (p *faultProxy) UpstreamAddr() *net.UDPAddr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.upstream == nil {
		return nil
	}
	return p.upstream.LocalAddr().(*net.UDPAddr)
}

// SwitchServerSource makes subsequent server-to-client datagrams originate
// from a new UDP socket. The client must accept them even though it sends to
// the original announced address; an unconnected socket does, a connected one
// would have the reply dropped by the kernel.
func (p *faultProxy) SwitchServerSource() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		p.mu.Unlock()
		p.t.Errorf("switch server source: %v", err)
		return
	}
	old := p.replyFront
	p.replyFront = socket
	p.streams[socket] = true
	p.switches++
	p.mu.Unlock()

	if old != nil {
		_ = old.Close()
		p.mu.Lock()
		delete(p.streams, old)
		p.mu.Unlock()
	}
}

// ReplySource reports the source address of server-to-client datagrams.
func (p *faultProxy) ReplySource() *net.UDPAddr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.replyFront != nil {
		return p.replyFront.LocalAddr().(*net.UDPAddr)
	}
	return p.front.LocalAddr().(*net.UDPAddr)
}

// SourceSwitches reports how many times the server-facing reply source moved.
func (p *faultProxy) SourceSwitches() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.switches
}

// Drops reports how many datagrams the proxy discarded.
func (p *faultProxy) Drops() int { return int(p.drops.Load()) }

func (p *faultProxy) shouldDrop(direction int, seq int) bool {
	if p.blackhole.Load() {
		return true
	}
	if direction == 0 {
		if through := int(p.dropClientThrough.Load()); seq <= through {
			return true
		}
		if every := int(p.dropClientEvery.Load()); every > 0 && seq%every == 0 {
			return true
		}
		return consumeDrop(&p.dropNextClient)
	}
	if every := int(p.dropServerEvery.Load()); every > 0 && seq%every == 0 {
		return true
	}
	return consumeDrop(&p.dropNextServer)
}

func consumeDrop(counter *atomic.Int32) bool {
	for {
		value := counter.Load()
		if value <= 0 {
			return false
		}
		if counter.CompareAndSwap(value, value-1) {
			return true
		}
	}
}

func (p *faultProxy) readFront() {
	defer p.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, addr, err := p.front.ReadFromUDP(buffer)
		if err != nil {
			select {
			case <-p.stop:
				return
			default:
				continue
			}
		}
		p.mu.Lock()
		p.clientAddr = addr
		p.mu.Unlock()
		p.clientSeq++
		payload := append([]byte(nil), buffer[:n]...)
		if p.shouldDrop(0, p.clientSeq) {
			p.drops.Add(1)
			continue
		}
		p.deliver(0, payload)
	}
}

func (p *faultProxy) readUpstream(stream *net.UDPConn) {
	defer p.wg.Done()
	buffer := make([]byte, 64<<10)
	for {
		n, _, err := stream.ReadFromUDP(buffer)
		if err != nil {
			p.mu.Lock()
			current := p.upstream == stream && !p.closed
			p.mu.Unlock()
			if !current {
				return
			}
			select {
			case <-p.stop:
				return
			default:
				continue
			}
		}
		p.serverSeq++
		payload := append([]byte(nil), buffer[:n]...)
		if p.shouldDrop(1, p.serverSeq) {
			p.drops.Add(1)
			continue
		}
		p.deliver(1, payload)
	}
}

// deliver applies duplication and reordering, then forwards datagrams.
func (p *faultProxy) deliver(direction int, payload []byte) {
	if !p.reorder.Load() {
		p.forward(direction, payload, p.duplicate.Load())
		return
	}
	p.mu.Lock()
	held := p.pending[direction]
	p.pending[direction] = payload
	if held != nil {
		p.pending[direction] = nil
		if p.timers[direction] != nil {
			p.timers[direction].Stop()
			p.timers[direction] = nil
		}
		p.mu.Unlock()
		p.forward(direction, payload, p.duplicate.Load())
		p.forward(direction, held, p.duplicate.Load())
		return
	}
	if p.timers[direction] == nil {
		direction := direction
		p.timers[direction] = time.AfterFunc(5*time.Millisecond, func() {
			p.mu.Lock()
			pending := p.pending[direction]
			p.pending[direction] = nil
			p.timers[direction] = nil
			p.mu.Unlock()
			if pending != nil {
				p.forward(direction, pending, p.duplicate.Load())
			}
		})
	}
	p.mu.Unlock()
}

func (p *faultProxy) forward(direction int, payload []byte, duplicate bool) {
	p.mu.Lock()
	closed := p.closed
	var destination *net.UDPAddr
	var stream *net.UDPConn
	if direction == 0 {
		destination = p.server
		stream = p.upstream
	} else {
		destination = p.clientAddr
		stream = p.replyFront
		if stream == nil {
			stream = p.front
		}
	}
	p.mu.Unlock()
	if closed || destination == nil || stream == nil {
		return
	}
	copies := 1
	if duplicate {
		copies = 2
	}
	for i := 0; i < copies; i++ {
		if _, err := stream.WriteToUDP(payload, destination); err != nil {
			select {
			case <-p.stop:
				return
			default:
			}
		}
	}
}

func openTestTransport(t *testing.T, bootstrap Bootstrap, probeTimeout time.Duration) appsession.Transport {
	t.Helper()
	factory := InProcessFactory{Bootstrap: bootstrap, ProbeTimeout: probeTimeout}
	transport, err := factory.Open(context.Background(), inProcessTestSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func proxiedBootstrap(server *fakeServer, proxy *faultProxy) bannerBootstrap {
	return bannerBootstrap{banner: server.announcementOn(proxy.Port())}
}

func TestInProcessProbeRetransmitsLostInitialDatagrams(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	// Lose the first two probe datagrams. The probe must retransmit inside
	// the default 1200ms window; before this fix the tracer sent only once
	// and returned ErrTimeout as soon as one initial packet was lost.
	proxy.dropClientThrough.Store(2)

	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 0)
	readUntil(t, transport, "fake$ ", 3*time.Second)
	if proxy.Drops() < 2 {
		t.Fatalf("proxy dropped %d datagrams, want at least 2", proxy.Drops())
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
}

func TestInProcessServerShutdownDuringProbe(t *testing.T) {
	server := startFakeServerShutdownOnFirst(t)
	transport := openTestTransport(t, bannerBootstrap{banner: server.announcement()}, 3*time.Second)
	text := readUntil(t, transport, "bye\r\n", 3*time.Second)
	if !strings.Contains(text, "bye") {
		t.Fatalf("final output = %q", text)
	}
	done := make(chan struct{})
	go func() {
		_, _ = transport.(appsession.ExitWaiter).Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not end when the server sent its sentinel during the probe")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want exited", end)
	}
	select {
	case <-server.shutdownAck:
	case <-time.After(time.Second):
		t.Fatal("the probe's shutdown sentinel was not acknowledged")
	}
}

func TestInProcessAcceptsRepliesFromNewServerAddress(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	before := proxy.ReplySource()
	proxy.SwitchServerSource()
	after := proxy.ReplySource()
	if before == nil || after == nil || before.String() == after.String() {
		t.Fatalf("server source address did not change: %v -> %v", before, after)
	}
	// An unconnected client socket accepts this authenticated datagram even
	// though it sent its own state to the original announced address.
	if _, err := transport.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	readUntil(t, transport, "hi\r\n", 3*time.Second)
	if proxy.SourceSwitches() != 1 {
		t.Fatalf("server source switches = %d, want 1", proxy.SourceSwitches())
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
}

func TestInProcessSustainedLossConverges(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	// Drop every third datagram in each direction for the life of the session,
	// including the shutdown handshake.
	proxy.dropClientEvery.Store(3)
	proxy.dropServerEvery.Store(3)

	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	for i := 0; i < 2; i++ {
		if _, err := transport.Write([]byte("echo hi\r")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		readUntil(t, transport, "hi\r\n", 10*time.Second)
	}
	if proxy.Drops() == 0 {
		t.Fatal("no datagrams were dropped; the test proved nothing")
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-server.shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("mosh-server never saw the shutdown sentinel under loss")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
}

func TestInProcessReorderingConverges(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	proxy.reorder.Store(true)

	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	// A payload larger than one fragment proves reassembly survives reordered
	// datagrams, not just reordered single-datagram states.
	bulk := make([]byte, 3000)
	state := uint64(0x2545f4914f6cdd1d)
	for i := range bulk {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		bulk[i] = byte(state)
	}
	copy(bulk[len(bulk)-16:], "REORDER-MARKER!!")
	if _, err := transport.Write(bulk); err != nil {
		t.Fatalf("bulk Write: %v", err)
	}
	waitInput(t, server, "REORDER-MARKER!!", 10*time.Second)
	if _, err := transport.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	readUntil(t, transport, "hi\r\n", 10*time.Second)
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
}

func TestInProcessDuplicatesAreIdempotent(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	proxy.duplicate.Store(true)

	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)
	if _, err := transport.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	waitInput(t, server, "echo hi", 3*time.Second)
	text := readUntil(t, transport, "hi\r\n", 3*time.Second)

	// Keep observing for a bounded window after the first marker, then close
	// the transport so the blocking Read returns. Any duplicate output the
	// SSP failed to suppress shows up here.
	drained := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		buffer := make([]byte, 4096)
		for {
			n, err := transport.Read(buffer)
			if n > 0 {
				out.Write(buffer[:n])
			}
			if err != nil {
				drained <- out.String()
				return
			}
		}
	}()
	time.Sleep(250 * time.Millisecond)
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case extra := <-drained:
		text += extra
	case <-time.After(3 * time.Second):
		t.Fatal("Read did not finish after Close")
	}
	if got := strings.Count(text, "hi\r\n"); got != 1 {
		t.Fatalf("duplicated output contained hi %d times: %q", got, text)
	}
}

func TestInProcessTimeoutClassification(t *testing.T) {
	t.Run("probe timeout is typed", func(t *testing.T) {
		server := startFakeServer(t)
		proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
		proxy.blackhole.Store(true)
		factory := InProcessFactory{
			Bootstrap:    proxiedBootstrap(server, proxy),
			ProbeTimeout: 200 * time.Millisecond,
		}
		_, err := factory.Open(context.Background(), inProcessTestSpec())
		if !errors.Is(err, moshmodel.ErrTimeout) {
			t.Fatalf("Open error = %v, want ErrTimeout", err)
		}
	})

	t.Run("user close wins over a blackholed network", func(t *testing.T) {
		server := startFakeServer(t)
		proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
		transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
		readUntil(t, transport, "fake$ ", 3*time.Second)
		proxy.blackhole.Store(true)
		started := time.Now()
		if err := transport.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("Close took %s with a dead network", elapsed)
		}
		if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
			t.Fatalf("end = %+v, want closed", end)
		}
	})
}

// countingBootstrap records how many SSH bootstraps a Session performed and
// serves one banner per call.
type countingBootstrap struct {
	mu      sync.Mutex
	banners []string
	calls   int
}

func (b *countingBootstrap) OpenBootstrap(context.Context, appsession.StartSpec, moshmodel.Config) (io.ReadWriteCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.calls > len(b.banners) {
		return nil, errors.New("no more bootstrap banners")
	}
	return &bufferStream{Reader: strings.NewReader(b.banners[b.calls-1])}, nil
}

func (b *countingBootstrap) Calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func TestInProcessAddressChangeContinuesSameSession(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	bootstrap := &countingBootstrap{banners: []string{server.announcementOn(proxy.Port())}}
	transport := openTestTransport(t, bootstrap, 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	before := server.clientAddress()
	proxy.Rebind()
	if _, err := transport.Write([]byte("echo hi\r")); err != nil {
		t.Fatalf("Write after rebind: %v", err)
	}
	waitInput(t, server, "echo hi", 3*time.Second)
	readUntil(t, transport, "hi\r\n", 5*time.Second)
	after := server.clientAddress()
	if before == nil || after == nil {
		t.Fatal("server never saw a client address")
	}
	if before.String() == after.String() {
		t.Fatalf("server client address did not change: %s", before)
	}
	// Roaming must not reopen the SSH bootstrap; it is the same UDP session
	// on a new source address.
	if calls := bootstrap.Calls(); calls != 1 {
		t.Fatalf("bootstrap calls = %d, want 1 (roaming must not re-handshake)", calls)
	}
	if proxy.Rebinds() != 1 {
		t.Fatalf("proxy rebinds = %d, want 1", proxy.Rebinds())
	}
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
}

func TestInProcessServerShutdownSurvivesLoss(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	// Drop the server's final output and shutdown sentinel once. The client
	// retransmits its exit state, and the fixture resends the sentinel.
	proxy.dropNextServer.Store(1)
	if _, err := transport.Write([]byte("exit\r")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	text := readUntil(t, transport, "bye\r\n", 10*time.Second)
	if !strings.Contains(text, "bye") {
		t.Fatalf("final output = %q", text)
	}
	waiter := transport.(appsession.ExitWaiter)
	done := make(chan struct{})
	go func() {
		_, _ = waiter.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("transport did not end after a lost shutdown sentinel")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonExited {
		t.Fatalf("end = %+v, want exited", end)
	}
	if proxy.Drops() == 0 {
		t.Fatal("the sentinel was not actually dropped")
	}
}

func TestInProcessUserShutdownSurvivesLostSentinel(t *testing.T) {
	server := startFakeServer(t)
	proxy := startFaultProxy(t, server.conn.LocalAddr().(*net.UDPAddr))
	transport := openTestTransport(t, proxiedBootstrap(server, proxy), 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	proxy.dropNextClient.Store(1)
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-server.shutdown:
	case <-time.After(3 * time.Second):
		t.Fatal("mosh-server never saw the retransmitted shutdown sentinel")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonClosed {
		t.Fatalf("end = %+v, want closed", end)
	}
	if proxy.Drops() == 0 {
		t.Fatal("the sentinel was not actually dropped")
	}
}

func TestInProcessProtocolErrorIsReconnectable(t *testing.T) {
	server := startFakeServer(t)
	transport := openTestTransport(t, bannerBootstrap{banner: server.announcement()}, 3*time.Second)
	readUntil(t, transport, "fake$ ", 3*time.Second)

	server.sendProtocolError(3)
	done := make(chan struct{})
	go func() {
		_, _ = transport.(appsession.ExitWaiter).Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not end on a protocol error")
	}
	if end := transport.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonError {
		t.Fatalf("end = %+v, want error (the reconnect policy retries this)", end)
	}
}

// TestInProcessSessionReconnectPolicyOpensANewTransport proves the existing
// Session reconnect policy reopens a mosh transport after a retryable end.
func TestInProcessSessionReconnectPolicyOpensANewTransport(t *testing.T) {
	first := startFakeServer(t)
	second := startFakeServer(t)
	bootstrap := &countingBootstrap{banners: []string{
		first.announcement(),
		second.announcement(),
	}}
	factory := InProcessFactory{Bootstrap: bootstrap, ProbeTimeout: 2 * time.Second}

	var mu sync.Mutex
	sawReconnecting, sawActiveAgain := false, false
	service := appsession.NewService(factory, appsession.WithChangeHook(func(snapshot appsession.Snapshot) {
		mu.Lock()
		defer mu.Unlock()
		if snapshot.Connection == appsession.ConnectionStatusReconnecting {
			sawReconnecting = true
		}
		if sawReconnecting && snapshot.Connection == appsession.ConnectionStatusActive {
			sawActiveAgain = true
		}
	}))

	spec := inProcessTestSpec()
	spec.Reconnect = &appsession.ReconnectPolicy{Delay: 20 * time.Millisecond}
	session, err := service.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() {
		_ = session.Close()
		select {
		case <-session.Done():
		case <-time.After(5 * time.Second):
			t.Error("Session did not finish teardown")
		}
	}()

	first.sendProtocolError(3)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		reconnected := sawActiveAgain
		mu.Unlock()
		if reconnected && bootstrap.Calls() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	reconnecting, active := sawReconnecting, sawActiveAgain
	mu.Unlock()
	if !reconnecting {
		t.Fatal("the Session never reported reconnecting after the mosh protocol error")
	}
	if !active {
		t.Fatal("the Session never became active again after reopening the transport")
	}
	if calls := bootstrap.Calls(); calls != 2 {
		t.Fatalf("bootstrap calls = %d, want 2", calls)
	}
}
