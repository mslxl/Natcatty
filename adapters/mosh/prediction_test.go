// SPDX-License-Identifier: GPL-3.0-or-later

package mosh

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	appsession "github.com/mslxl/natcatty/application/session"
	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	moshcore "github.com/mslxl/natcatty/internal/mosh"
	"github.com/mslxl/natcatty/internal/mosh/terminal"
)

// predictionScript drives a fake mosh server for the local-echo tests. It
// renders a two-character prompt, confirms every first 'Q' with a matching
// host state, and deliberately withholds the echo for later keystrokes so the
// test can observe what the client displays while the server is silent.
type predictionScript struct {
	server *fakeServer

	mu        sync.Mutex
	introSent bool
	qSent     bool
	firstSeen []byte
	lastFrame uint64
	changed   chan struct{}
}

func (p *predictionScript) handle(instruction moshcore.TransportInstruction) {
	if instruction.NewNum == moshcore.ShutdownNum {
		p.server.sendMu.Lock()
		p.server.clientAckNum = moshcore.ShutdownNum
		p.server.sendMu.Unlock()
		p.server.sendHostInstructions(nil, 0)
		return
	}
	// A scripted server skips the fixture's normal acknowledgement bookkeeping,
	// but the responding host state must still echo the newest client state the
	// way mosh-server does: that EchoAck is what confirms a prediction.
	p.server.sendMu.Lock()
	if instruction.NewNum > p.server.clientAckNum {
		p.server.clientAckNum = instruction.NewNum
	}
	p.server.sendMu.Unlock()
	messages, err := moshcore.UnmarshalUserMessage(instruction.Diff)
	if err != nil {
		return
	}
	var keys []byte
	for _, message := range messages {
		keys = append(keys, message.Keys...)
	}
	if len(keys) == 0 {
		p.mu.Lock()
		introduce := !p.introSent
		p.introSent = true
		p.mu.Unlock()
		if introduce {
			p.server.sendHostState([]byte("ab"), 0)
		}
		return
	}

	p.mu.Lock()
	for _, key := range keys {
		seen := false
		for _, prior := range p.firstSeen {
			if prior == key {
				seen = true
				break
			}
		}
		if !seen {
			p.firstSeen = append(p.firstSeen, key)
		}
	}
	if instruction.NewNum > p.lastFrame {
		p.lastFrame = instruction.NewNum
	}
	echoQ := string(keys) == "Q" && !p.qSent
	if echoQ {
		p.qSent = true
	}
	p.mu.Unlock()
	select {
	case p.changed <- struct{}{}:
	default:
	}

	if echoQ {
		// Append the echoed character at the server's authoritative cursor and
		// echo the client frame that carried it, as mosh-server does after
		// registering input for its EchoAck.
		p.server.sendHostInstructions([]moshcore.HostInstruction{
			{HostString: []byte("Q")},
			{EchoAckPresent: true, EchoAckNum: p.lastClientFrame()},
		}, 0)
	}
}

func (p *predictionScript) received() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.firstSeen...)
}

func (p *predictionScript) lastClientFrame() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastFrame
}

// confirm appends host content and marks the newest client state as echoed,
// which is what mosh-server sends once it has processed those keystrokes.
func (p *predictionScript) confirm(hostString string) {
	p.server.sendHostInstructions([]moshcore.HostInstruction{
		{HostString: []byte(hostString)},
		{EchoAckPresent: true, EchoAckNum: p.lastClientFrame()},
	}, 0)
}

func startPredictionServer(t *testing.T) (*fakeServer, *predictionScript) {
	t.Helper()
	server := newFakeServerAt(t, net.IPv4(127, 0, 0, 1), 2)
	script := &predictionScript{server: server, changed: make(chan struct{}, 1)}
	server.scripted = true
	server.onDecoded = script.handle
	server.start()
	return server, script
}

func openPredictionTransport(t *testing.T, server *fakeServer,
	mode moshmodel.PredictionDisplay) appsession.Transport {
	t.Helper()
	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcement()},
		ProbeTimeout: 500 * time.Millisecond,
	}
	spec := inProcessTestSpec()
	spec.Mosh.PredictionDisplay = mode
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

func inProcessOf(t *testing.T, transport appsession.Transport) *inProcessTransport {
	t.Helper()
	concrete, ok := transport.(*inProcessTransport)
	if !ok {
		t.Fatalf("transport type = %T, want *inProcessTransport", transport)
	}
	return concrete
}

// framebufferRow renders one row the way the display encoder sees it.
func framebufferRow(fb *terminal.Framebuffer, row int) string {
	if fb == nil || row < 0 || row >= fb.DS.Height() {
		return ""
	}
	var out strings.Builder
	for col := 0; col < fb.DS.Width(); col++ {
		fb.Cell(row, col).PrintGrapheme(&out)
	}
	return out.String()
}

// rendererRow reads one row from the framebuffer the renderer last drew
// (including predictions) or from the latest server state (before overlays).
func rendererRow(transport appsession.Transport, row int, authoritative bool) string {
	concrete := transport.(*inProcessTransport)
	concrete.renderer.mu.Lock()
	defer concrete.renderer.mu.Unlock()
	fb := concrete.renderer.lastDrawn
	if authoritative {
		fb = concrete.renderer.latest.Framebuffer()
	}
	return framebufferRow(fb, row)
}

// displayedRow is what the client has drawn, including predictions.
func displayedRow(transport appsession.Transport, row int) string {
	return rendererRow(transport, row, false)
}

// authoritativeRow is the latest server state, before overlays. It must never
// contain speculative bytes.
func authoritativeRow(transport appsession.Transport, row int) string {
	return rendererRow(transport, row, true)
}

func waitForServerKeys(t *testing.T, script *predictionScript, want string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(string(script.received()), want) {
			return
		}
		select {
		case <-script.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for server keys %q, got %q", want, script.received())
		}
	}
}

// TestInProcessPredictionDisplaysBeforeTheServerEcho is the latency-sensitive
// end-to-end path: while the fake server withholds its reply, the second and
// third keystrokes are visible only because the overlay drew them. The
// server-authoritative framebuffer and the bytes on the wire never contain
// them until the server actually sends them.
func TestInProcessPredictionDisplaysBeforeTheServerEcho(t *testing.T) {
	server, script := startPredictionServer(t)
	transport := openPredictionTransport(t, server, moshmodel.PredictionAlways)

	waitFor(t, "the initial prompt", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "ab") &&
			!strings.HasPrefix(displayedRow(transport, 0), "abQ")
	})

	if _, err := transport.Write([]byte("Q")); err != nil {
		t.Fatalf("Write Q: %v", err)
	}
	waitForServerKeys(t, script, "Q")
	// The server echoes Q immediately, which moves the prediction epoch from
	// tentative to confirmed.
	waitFor(t, "the confirmed Q", func() bool {
		concrete := inProcessOf(t, transport)
		concrete.renderer.mu.Lock()
		active := concrete.renderer.overlays.Predictions().Active()
		concrete.renderer.mu.Unlock()
		return !active && strings.HasPrefix(displayedRow(transport, 0), "abQ")
	})

	// Z is written while the server is deliberately silent. The displayed
	// framebuffer gains it immediately; the authoritative state does not.
	if _, err := transport.Write([]byte("Z")); err != nil {
		t.Fatalf("Write Z: %v", err)
	}
	waitForServerKeys(t, script, "Z")
	waitFor(t, "the displayed Z prediction", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQZ")
	})
	if got := authoritativeRow(transport, 0); strings.Contains(got, "Z") {
		t.Fatalf("server-authoritative row %q contains the unpredicted Z", got)
	}

	// The server now confirms Z, so the next prediction is in a confirmed
	// epoch and visible at once.
	script.confirm("Z")
	waitFor(t, "the confirmed Z", func() bool {
		concrete := inProcessOf(t, transport)
		concrete.renderer.mu.Lock()
		active := concrete.renderer.overlays.Predictions().Active()
		concrete.renderer.mu.Unlock()
		return !active && strings.HasPrefix(displayedRow(transport, 0), "abQZ")
	})

	if _, err := transport.Write([]byte("Y")); err != nil {
		t.Fatalf("Write Y: %v", err)
	}
	waitForServerKeys(t, script, "Y")
	waitFor(t, "the displayed Y prediction", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQZY")
	})
	if got := authoritativeRow(transport, 0); strings.Contains(got, "Y") {
		t.Fatalf("server-authoritative row %q contains the unpredicted Y", got)
	}

	// The server's real shell turns Y into X. Once its state and EchoAck
	// arrive, the prediction is corrected: the client shows the authoritative
	// row, not the speculative one.
	script.confirm("\rabQX")
	waitFor(t, "the corrected row", func() bool {
		return strings.TrimRight(displayedRow(transport, 0), " ") == "abQX"
	})
	if got := strings.TrimRight(authoritativeRow(transport, 0), " "); got != "abQX" {
		t.Fatalf("server-authoritative row = %q, want abQX", got)
	}

	// Non-vacuity: every byte the server received over SSP was one the user
	// typed, in typing order. Prediction bytes never became network output.
	if got := string(script.received()); got != "QZY" {
		t.Fatalf("server received user bytes %q, want exactly QZY", got)
	}
}

// TestInProcessPredictionNeverModeWithoutEcho shows the persisted never mode
// reaching the in-process engine: keystrokes are not drawn while the server is
// silent, but the server's echo still updates the display normally.
func TestInProcessPredictionNeverModeWithoutEcho(t *testing.T) {
	server, script := startPredictionServer(t)
	transport := openPredictionTransport(t, server, moshmodel.PredictionNever)

	waitFor(t, "the initial prompt", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "ab")
	})
	if _, err := transport.Write([]byte("Q")); err != nil {
		t.Fatalf("Write Q: %v", err)
	}
	waitForServerKeys(t, script, "Q")
	waitFor(t, "the echoed Q", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQ")
	})

	if _, err := transport.Write([]byte("Z")); err != nil {
		t.Fatalf("Write Z: %v", err)
	}
	waitForServerKeys(t, script, "Z")
	if got := displayedRow(transport, 0); strings.Contains(got, "Z") {
		t.Fatalf("never mode predicted Z: row %q", got)
	}
	if got := authoritativeRow(transport, 0); strings.Contains(got, "Z") {
		t.Fatalf("server-authoritative row %q contains Z", got)
	}

	script.confirm("Z")
	waitFor(t, "the echoed Z", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQZ")
	})
}

// TestInProcessPredictionAdaptiveFollowsSendInterval verifies the stock
// adaptive default: a fast measured link does not predict, and a slow link
// does. This is the user-visible difference between adaptive and always.
func TestInProcessPredictionAdaptiveFollowsSendInterval(t *testing.T) {
	server, script := startPredictionServer(t)
	transport := openPredictionTransport(t, server, moshmodel.PredictionAdaptive)

	waitFor(t, "the initial prompt", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "ab")
	})
	if _, err := transport.Write([]byte("Q")); err != nil {
		t.Fatalf("Write Q: %v", err)
	}
	waitForServerKeys(t, script, "Q")
	waitFor(t, "the emitted Q frame", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQ")
	})

	// The renderer fed the transport's measured localhost pacing (the 20ms
	// clamp) to the engine when it rendered the probe state, so the adaptive
	// SRTT trigger is off. Z is therefore stored but not drawn.
	concrete := inProcessOf(t, transport)
	if got := concrete.client.SendInterval(); got > 20 {
		t.Fatalf("measured localhost send interval = %d ms, want the 20ms clamp", got)
	}
	if _, err := transport.Write([]byte("Z")); err != nil {
		t.Fatalf("Write Z: %v", err)
	}
	waitForServerKeys(t, script, "Z")
	if got := displayedRow(transport, 0); strings.Contains(got, "Z") {
		t.Fatalf("adaptive mode predicted on a fast link: row %q", got)
	}

	// A slow link is exactly stock's trigger. The transport normally supplies
	// this hint; injecting it directly isolates the adaptive decision from the
	// localhost RTT while still exercising the real renderer and transport.
	concrete.renderer.mu.Lock()
	concrete.renderer.overlays.Predictions().SetSendInterval(250)
	concrete.renderer.mu.Unlock()
	if _, err := transport.Write([]byte("X")); err != nil {
		t.Fatalf("Write X: %v", err)
	}
	waitForServerKeys(t, script, "X")
	waitFor(t, "the adaptive slow-link X prediction", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQZX")
	})
	if got := authoritativeRow(transport, 0); strings.Contains(got, "X") {
		t.Fatalf("server-authoritative row %q contains X", got)
	}
}

// delayedProxy forwards a mosh client to the fake server and holds every
// server-to-client datagram for a fixed delay. It is a one-way latency
// simulator: the client's SSP timestamp reply therefore samples a high RTT,
// and its echo really is in flight while the client types.
type delayedProxy struct {
	t      *testing.T
	front  *net.UDPConn
	back   *net.UDPConn
	server *net.UDPAddr
	delay  time.Duration

	mu     sync.Mutex
	client *net.UDPAddr
	stop   chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

func startDelayedProxy(t *testing.T, server *net.UDPAddr, delay time.Duration) *delayedProxy {
	t.Helper()
	front, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("proxy front: %v", err)
	}
	back, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		_ = front.Close()
		t.Fatalf("proxy back: %v", err)
	}
	proxy := &delayedProxy{
		t: t, front: front, back: back, server: server,
		delay: delay, stop: make(chan struct{}),
	}
	proxy.wg.Add(2)
	go proxy.readClient()
	go proxy.readServer()
	t.Cleanup(proxy.Close)
	return proxy
}

func (p *delayedProxy) Port() int { return p.front.LocalAddr().(*net.UDPAddr).Port }

func (p *delayedProxy) Close() {
	p.once.Do(func() {
		close(p.stop)
		_ = p.front.Close()
		_ = p.back.Close()
		p.wg.Wait()
	})
}

func (p *delayedProxy) readClient() {
	defer p.wg.Done()
	buffer := make([]byte, 2048)
	for {
		n, addr, err := p.front.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.client = addr
		p.mu.Unlock()
		payload := append([]byte(nil), buffer[:n]...)
		if _, err := p.back.WriteToUDP(payload, p.server); err != nil {
			return
		}
	}
}

func (p *delayedProxy) readServer() {
	defer p.wg.Done()
	buffer := make([]byte, 2048)
	for {
		n, _, err := p.back.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		payload := append([]byte(nil), buffer[:n]...)
		// Receiver order is preserved because this loop cannot read the next
		// datagram until the delayed one has been delivered. Test traffic is
		// small; a queue is not needed.
		select {
		case <-p.stop:
			return
		case <-time.After(p.delay):
		}
		p.mu.Lock()
		client := p.client
		p.mu.Unlock()
		if client == nil {
			continue
		}
		if _, err := p.front.WriteToUDP(payload, client); err != nil {
			return
		}
	}
}

// TestInProcessPredictionAdaptiveOnDelayedLink exercises stock's adaptive
// default over a measured slow link rather than an injected interval. The
// proxy holds the server's replies, so the transport's SRTT is high, the
// adaptive trigger is on, and keystrokes appear while the echo is still in
// flight. The bytes received by the server and the authoritative state remain
// exactly the user's input.
func TestInProcessPredictionAdaptiveOnDelayedLink(t *testing.T) {
	server, script := startPredictionServer(t)
	proxy := startDelayedProxy(t, server.conn.LocalAddr().(*net.UDPAddr), 300*time.Millisecond)

	factory := InProcessFactory{
		Bootstrap:    bannerBootstrap{banner: server.announcementOn(proxy.Port())},
		ProbeTimeout: 2 * time.Second,
	}
	spec := inProcessTestSpec()
	spec.Mosh.PredictionDisplay = moshmodel.PredictionAdaptive
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	concrete := inProcessOf(t, transport)

	waitFor(t, "the delayed initial prompt", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "ab")
	})
	// The delayed first exchange gives the client a measured RTT well above
	// stock's adaptive threshold, and readLoop feeds that pacing to the engine
	// before the first frame.
	waitFor(t, "a measured slow send interval", func() bool {
		return concrete.client.SendInterval() > 30
	})
	if got := concrete.client.SendInterval(); got <= 30 {
		t.Fatalf("measured send interval = %d ms, want the delayed link's slow value", got)
	}

	if _, err := transport.Write([]byte("Q")); err != nil {
		t.Fatalf("Write Q: %v", err)
	}
	// The first prediction of a session is tentative: no epoch has been
	// confirmed yet, so stock stores it but does not draw it. Write's frame is
	// emitted synchronously while the proxy still holds the matching echo, so
	// this is observed deterministically.
	if got := displayedRow(transport, 0); strings.Contains(got, "Q") {
		t.Fatalf("tentative first prediction reached the display: row %q", got)
	}
	if got := authoritativeRow(transport, 0); strings.Contains(got, "Q") {
		t.Fatalf("server-authoritative row %q contains the unpredicted Q", got)
	}
	waitForServerKeys(t, script, "Q")
	waitFor(t, "the delayed confirmed Q", func() bool {
		concrete.renderer.mu.Lock()
		active := concrete.renderer.overlays.Predictions().Active()
		concrete.renderer.mu.Unlock()
		return !active && strings.HasPrefix(displayedRow(transport, 0), "abQ")
	})

	if _, err := transport.Write([]byte("Z")); err != nil {
		t.Fatalf("Write Z: %v", err)
	}
	waitForServerKeys(t, script, "Z")
	waitFor(t, "the adaptive Z prediction before the echo", func() bool {
		return strings.HasPrefix(displayedRow(transport, 0), "abQZ")
	})
	if got := authoritativeRow(transport, 0); strings.Contains(got, "Z") {
		t.Fatalf("server-authoritative row %q contains the unpredicted Z", got)
	}
	if got := string(script.received()); got != "QZ" {
		t.Fatalf("server received user bytes %q, want exactly QZ", got)
	}
}
