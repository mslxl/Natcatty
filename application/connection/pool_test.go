package connection

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// countingClient is a Client whose opens, keepalives, and closure the pool
// tests observe.
type countingClient struct {
	mu           sync.Mutex
	closed       bool
	opens        int
	keepalives   int
	keepaliveErr error
	version      string
}

func (c *countingClient) OpenTerminal(context.Context, TerminalSpec) (Terminal, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens++
	return &fakeTerminal{}, nil
}

func (c *countingClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *countingClient) Keepalive() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keepalives++
	return c.keepaliveErr
}

func (c *countingClient) RemoteVersion() string { return c.version }

func (c *countingClient) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *countingClient) keepaliveCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keepalives
}

func (c *countingClient) setKeepaliveErr(err error) {
	c.mu.Lock()
	c.keepaliveErr = err
	c.mu.Unlock()
}

// sequenceDialer returns one fresh client per physical dial.
type sequenceDialer struct {
	mu      sync.Mutex
	clients []*countingClient
	errs    []error
	calls   int
}

func (d *sequenceDialer) Dial(context.Context, DialRequest) (Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	index := d.calls
	d.calls++
	if index < len(d.errs) && d.errs[index] != nil {
		return nil, d.errs[index]
	}
	if index < len(d.clients) {
		return d.clients[index], nil
	}
	client := &countingClient{}
	d.clients = append(d.clients, client)
	return client, nil
}

func (d *sequenceDialer) dialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func poolTestManager(t *testing.T, dialer Dialer, opts ...Option) *Manager {
	t.Helper()
	all := append([]Option{
		WithIDGenerator(func() string { return "kh" }),
	}, opts...)
	manager := NewManager(dialer, &fakePasswordSource{password: "s3cret"}, &fakeKnownHosts{}, all...)
	t.Cleanup(manager.CloseAll)
	return manager
}

func TestPoolBeginJoinsAnInFlightCompatibleDial(t *testing.T) {
	pool := newPool(DefaultIdleTTL, MaxIdleConnections, time.Now, func(time.Duration, func()) func() { return func() {} })
	endpoint := endpointFor(sshRequest())

	if reuse, wait, lead := pool.begin(endpoint, LeaseShell); reuse != nil || wait != nil || lead == nil {
		t.Fatalf("first begin = (%v, %v, %v), want a leader", reuse, wait, lead)
	}
	_, wait, secondLead := pool.begin(endpoint, LeaseShell)
	if secondLead != nil || wait == nil {
		t.Fatal("a concurrent compatible request opened a second physical dial")
	}

	// A shell needing agent forwarding cannot join a non-forwarding leader.
	forwarding := sshRequest()
	forwarding.AgentForwarding = true
	forwarding.ForwardingAgentFingerprint = "agent-a"
	_, waitAF, leadAF := pool.begin(endpointFor(forwarding), LeaseShell)
	if leadAF == nil || waitAF != nil {
		t.Fatal("an agent-forwarding shell joined an incompatible in-flight dial")
	}
}

func TestLeaseReleaseKeepsTheTransportForOtherLeases(t *testing.T) {
	dialer := &sequenceDialer{clients: []*countingClient{{}}}
	manager := poolTestManager(t, dialer)

	first, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("dial count = %d, want one shared Connection", dialer.dialCount())
	}

	if err := first.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if dialer.clients[0].isClosed() {
		t.Fatal("releasing one lease closed a transport another lease still uses")
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if dialer.clients[0].isClosed() {
		t.Fatal("the last release closed the transport instead of parking it")
	}
	third, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("third Acquire: %v", err)
	}
	if dialer.dialCount() != 1 {
		t.Fatalf("parked transport was not reused: dial count = %d", dialer.dialCount())
	}
	_ = third.Release()
}

func TestIdleParkExpiresOnTheConfiguredTTL(t *testing.T) {
	var now time.Time
	var scheduled time.Duration
	var fire func()
	dialer := &sequenceDialer{clients: []*countingClient{{}}}
	manager := poolTestManager(t, dialer,
		WithClock(func() time.Time { return now }),
		WithIdleTTL(5*time.Minute),
		WithTimerScheduler(func(duration time.Duration, fn func()) func() {
			scheduled = duration
			fire = fn
			return func() {}
		}),
	)

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()
	if scheduled != 5*time.Minute || fire == nil {
		t.Fatalf("park scheduled %v, want the five-minute TTL", scheduled)
	}
	if dialer.clients[0].isClosed() {
		t.Fatal("the transport closed before its idle deadline")
	}

	now = now.Add(5 * time.Minute)
	fire()
	if !dialer.clients[0].isClosed() {
		t.Fatal("the idle deadline did not close the parked transport")
	}
}

func TestZeroIdleTTLKeepsTheTransportUntilManagerClose(t *testing.T) {
	scheduled := 0
	dialer := &sequenceDialer{clients: []*countingClient{{}}}
	manager := poolTestManager(t, dialer,
		WithIdleTTL(0),
		WithTimerScheduler(func(time.Duration, func()) func() {
			scheduled++
			return func() {}
		}),
	)

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()
	if scheduled != 0 {
		t.Fatalf("TTL 0 scheduled %d timers, want none", scheduled)
	}
	if dialer.clients[0].isClosed() {
		t.Fatal("TTL 0 closed the parked transport before quit")
	}
	manager.CloseAll()
	if !dialer.clients[0].isClosed() {
		t.Fatal("CloseAll did not close a TTL-0 parked transport")
	}
}

func TestIdleLimitEvictsTheLeastRecentlyUsedTransport(t *testing.T) {
	clients := []*countingClient{{}, {}, {}}
	dialer := &sequenceDialer{clients: clients}
	manager := poolTestManager(t, dialer, WithMaxIdleConnections(2))

	release := func(fingerprint string) {
		request := sshRequest()
		request.ConfigFingerprint = fingerprint
		lease, err := manager.Acquire(context.Background(), request)
		if err != nil {
			t.Fatalf("Acquire %s: %v", fingerprint, err)
		}
		_ = lease.Release()
	}
	release("a")
	release("b")
	release("c")

	if !clients[0].isClosed() {
		t.Fatal("the oldest parked transport was not evicted")
	}
	if clients[1].isClosed() || clients[2].isClosed() {
		t.Fatal("the idle limit evicted a newer parked transport")
	}
}

func TestKeepaliveFailureDiscardsTheTransport(t *testing.T) {
	client := &countingClient{keepaliveErr: errors.New("unreachable")}
	dialer := &sequenceDialer{clients: []*countingClient{client}}
	manager := poolTestManager(t, dialer, WithIdleTTL(0))

	lease, err := manager.Acquire(context.Background(), Request{
		HostID: "host-1", Hostname: "example.com", Port: 22, Username: "root",
		Keepalive: &KeepalivePolicy{Interval: 5 * time.Millisecond, CountMax: 2},
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	deadline := time.Now().Add(2 * time.Second)
	for !client.isClosed() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !client.isClosed() {
		t.Fatalf("keepalive failures did not discard the transport (%d probes)", client.keepaliveCalls())
	}
}

func TestKeepaliveIsSilentWhenDisabled(t *testing.T) {
	client := &countingClient{}
	dialer := &sequenceDialer{clients: []*countingClient{client}}
	manager := poolTestManager(t, dialer)

	lease, err := manager.Acquire(context.Background(), Request{
		HostID: "host-1", Hostname: "example.com", Port: 22, Username: "root",
		Keepalive: &KeepalivePolicy{Interval: -1},
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	time.Sleep(30 * time.Millisecond)
	if calls := client.keepaliveCalls(); calls != 0 {
		t.Fatalf("disabled keepalive sent %d probes", calls)
	}
}

func TestKeepalivePolicyResolution(t *testing.T) {
	cases := []struct {
		name   string
		policy KeepalivePolicy
		want   KeepalivePolicy
	}{
		{"fallback", KeepalivePolicy{}, KeepalivePolicy{Interval: FallbackKeepaliveInterval, CountMax: FallbackKeepaliveCountMax}},
		{"explicit", KeepalivePolicy{Interval: DefaultKeepaliveInterval, CountMax: DefaultKeepaliveCountMax}, KeepalivePolicy{Interval: DefaultKeepaliveInterval, CountMax: DefaultKeepaliveCountMax}},
		{"disabled", KeepalivePolicy{Interval: -1}, KeepalivePolicy{}},
		{"negative count", KeepalivePolicy{Interval: time.Second, CountMax: -4}, KeepalivePolicy{Interval: time.Second, CountMax: 0}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.policy.resolved(); got != test.want {
				t.Fatalf("resolved = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestReuseRulesFollowExactAndAsymmetricAgentForwarding(t *testing.T) {
	plain := endpointFor(sshRequest())
	forwardingRequest := sshRequest()
	forwardingRequest.AgentForwarding = true
	forwardingRequest.ForwardingAgentFingerprint = "agent-a"
	forwarding := endpointFor(forwardingRequest)

	if !plain.allowsReuse(plain, LeaseShell) {
		t.Fatal("identical endpoints cannot reuse")
	}
	if forwarding.allowsReuse(plain, LeaseShell) || plain.allowsReuse(forwarding, LeaseShell) {
		t.Fatal("a shell reused a transport with a different agent-forwarding state")
	}
	otherAgent := endpointFor(func() Request {
		request := forwardingRequest
		request.ForwardingAgentFingerprint = "agent-b"
		return request
	}())
	if forwarding.allowsReuse(otherAgent, LeaseShell) {
		t.Fatal("a shell reused a transport backed by a different forwarding agent")
	}

	if !plain.allowsReuse(forwarding, LeaseChannel) {
		t.Fatal("a non-forwarding channel consumer could not reuse a forwarding transport")
	}
	if forwarding.allowsReuse(plain, LeaseChannel) {
		t.Fatal("a forwarding channel consumer reused a non-forwarding transport")
	}
}

func TestSingleChannelX11AndDedicatedAreNeverReused(t *testing.T) {
	single := sshRequest()
	single.SingleChannel = true
	x11 := sshRequest()
	x11.X11Forwarding = true
	dedicated := sshRequest()
	dedicated.Dedicated = true
	plain := endpointFor(sshRequest())

	for name, endpoint := range map[string]endpointConfig{
		"single-channel": endpointFor(single),
		"x11":            endpointFor(x11),
		"dedicated":      endpointFor(dedicated),
	} {
		if endpoint.allowsReuse(endpoint, LeaseShell) {
			t.Errorf("%s endpoint reused another transport", name)
		}
		if plain.allowsReuse(endpoint, LeaseChannel) || endpoint.allowsReuse(plain, LeaseChannel) {
			t.Errorf("%s endpoint participated in channel reuse", name)
		}
	}
}

func TestEndpointKeyIncludesEffectiveConfiguration(t *testing.T) {
	base := endpointFor(sshRequest())
	changedConfig := sshRequest()
	changedConfig.ConfigFingerprint = "rotated"
	if endpointFor(changedConfig).key == base.key {
		t.Fatal("a credential configuration change did not change the pool key")
	}

	changedKeepalive := sshRequest()
	changedKeepalive.Keepalive = &KeepalivePolicy{Interval: time.Minute, CountMax: 2}
	if endpointFor(changedKeepalive).key == base.key {
		t.Fatal("a keepalive change did not change the pool key")
	}

	changedAlgorithm := sshRequest()
	changedAlgorithm.Algorithms = AlgorithmPolicy{Legacy: true}
	if endpointFor(changedAlgorithm).key == base.key {
		t.Fatal("an algorithm policy change did not change the pool key")
	}

	proxied := sshRequest()
	proxied.Proxy = &Proxy{Type: ProxySOCKS5, Host: "proxy", Port: 1080, Username: "user", Password: "first"}
	rotatedProxy := *proxied.Proxy
	rotatedProxy.Password = "rotated"
	proxiedRotation := proxied
	proxiedRotation.Proxy = &rotatedProxy
	if endpointFor(proxied).key == endpointFor(proxiedRotation).key {
		t.Fatal("a rotated proxy credential did not change the pool key")
	}

	agentOnly := sshRequest()
	agentOnly.AgentForwarding = true
	agentOnly.ForwardingAgentFingerprint = "agent-a"
	if endpointFor(agentOnly).key != base.key {
		t.Fatal("agent forwarding changed the pool key; it must remain a reuse rule")
	}
}

// channelTerminal is a terminal whose output the test feeds.
type channelTerminal struct {
	ch     chan []byte
	closed chan struct{}
	once   sync.Once
}

func newChannelTerminal() *channelTerminal {
	return &channelTerminal{ch: make(chan []byte, 8), closed: make(chan struct{})}
}

func (c *channelTerminal) Read(p []byte) (int, error) {
	select {
	case data := <-c.ch:
		return copy(p, data), nil
	case <-c.closed:
		return 0, io.EOF
	}
}

func (c *channelTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (c *channelTerminal) Resize(int, int) error       { return nil }
func (c *channelTerminal) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestProbeReusedTerminalAcceptsALiveShell(t *testing.T) {
	terminal := newChannelTerminal()
	terminal.ch <- []byte("banner")
	probed, alive := probeReusedTerminal(terminal, 10*time.Millisecond)
	if !alive || probed == nil {
		t.Fatal("a live reused shell failed the settle probe")
	}
	buffer := make([]byte, 16)
	n, err := probed.Read(buffer)
	if err != nil || string(buffer[:n]) != "banner" {
		t.Fatalf("probed terminal output = %q, %v; want the buffered banner", buffer[:n], err)
	}
	_ = probed.Close()
}

func TestProbeReusedTerminalRejectsAShellThatEnds(t *testing.T) {
	terminal := newChannelTerminal()
	_ = terminal.Close()
	probed, alive := probeReusedTerminal(terminal, 10*time.Millisecond)
	if alive || probed != nil {
		t.Fatal("a shell that ended during the settle window was accepted")
	}
}

func TestRemoteLivenessPolicy(t *testing.T) {
	if RemoteAllowsIdleParkedShellReuse("SSH-2.0-Qizhi TERM-SSHD_1.0") {
		t.Fatal("a TERM-SSHD transport may not be parked")
	}
	if !RemoteAllowsIdleParkedShellReuse("SSH-2.0-OpenSSH_9.6") {
		t.Fatal("an OpenSSH transport may be parked")
	}

	cases := map[string]bool{
		"SSH-2.0-OpenSSH_9.6":      false,
		"SSH-2.0-dropbear_2022.83": false,
		"SSH-2.0-asyncssh_2.14":    true,
		"":                         true,
		"SSH-2.0-Qizhi TERM-SSHD":  true,
	}
	for version, want := range cases {
		if got := RemoteNeedsReusedShellLivenessCheck(version); got != want {
			t.Errorf("liveness check for %q = %t, want %t", version, got, want)
		}
	}
}

func TestTermSshdTransportIsNotParked(t *testing.T) {
	client := &countingClient{version: "SSH-2.0-Qizhi TERM-SSHD_1.0"}
	dialer := &sequenceDialer{clients: []*countingClient{client}}
	manager := poolTestManager(t, dialer, WithIdleTTL(0))

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()
	if !client.isClosed() {
		t.Fatal("a TERM-SSHD transport was parked")
	}
}
