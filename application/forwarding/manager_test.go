package forwarding

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

// fakeRepository is the persistence port with an external-update hook for
// reconnect-gate tests.
type fakeRepository struct {
	mu    sync.Mutex
	rules map[model.ForwardingRuleID]domainforward.Rule
	order []model.ForwardingRuleID
}

func newFakeRepository(rules ...domainforward.Rule) *fakeRepository {
	repo := &fakeRepository{rules: map[model.ForwardingRuleID]domainforward.Rule{}}
	for _, rule := range rules {
		repo.put(rule)
	}
	return repo
}

func (r *fakeRepository) put(rule domainforward.Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rules[rule.ID]; !ok {
		r.order = append(r.order, rule.ID)
	}
	r.rules[rule.ID] = rule
}

func (r *fakeRepository) ListForwardingRules(context.Context) ([]domainforward.Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domainforward.Rule, 0, len(r.order))
	for _, id := range r.order {
		if rule, ok := r.rules[id]; ok {
			out = append(out, rule)
		}
	}
	return out, nil
}

func (r *fakeRepository) GetForwardingRule(_ context.Context, id model.ForwardingRuleID) (domainforward.Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rule, ok := r.rules[id]
	if !ok {
		return domainforward.Rule{}, ErrNotFound
	}
	return rule, nil
}

func (r *fakeRepository) SaveForwardingRule(_ context.Context, rule domainforward.Rule) error {
	r.put(rule)
	return nil
}

func (r *fakeRepository) DeleteForwardingRule(_ context.Context, id model.ForwardingRuleID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rules[id]; !ok {
		return ErrNotFound
	}
	delete(r.rules, id)
	return nil
}

type fakeConnector struct {
	mu       sync.Mutex
	connects int
	hosts    []model.HostID
	connect  func(context.Context, model.HostID) (Connection, error)
}

func (c *fakeConnector) Connect(ctx context.Context, hostID model.HostID) (Connection, error) {
	c.mu.Lock()
	c.connects++
	c.hosts = append(c.hosts, hostID)
	fn := c.connect
	c.mu.Unlock()
	if fn != nil {
		return fn(ctx, hostID)
	}
	return &fakeConnection{}, nil
}

func (c *fakeConnector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connects
}

type fakeConnection struct {
	mu       sync.Mutex
	dial     func(context.Context, string, string) (net.Conn, error)
	listen   func(string) (net.Listener, error)
	done     chan struct{}
	releases int
	discards int
	lastAddr string
	lastDest string
}

func (c *fakeConnection) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	c.mu.Lock()
	c.lastDest = address
	fn := c.dial
	c.mu.Unlock()
	if fn != nil {
		return fn(ctx, network, address)
	}
	return nil, errors.New("fake connection: no dial")
}

func (c *fakeConnection) ListenTCP(address string) (net.Listener, error) {
	c.mu.Lock()
	c.lastAddr = address
	fn := c.listen
	c.mu.Unlock()
	if fn != nil {
		return fn(address)
	}
	return nil, errors.New("fake connection: no listener")
}

func (c *fakeConnection) Release() {
	c.mu.Lock()
	c.releases++
	c.mu.Unlock()
}

func (c *fakeConnection) Discard() {
	c.mu.Lock()
	c.discards++
	c.mu.Unlock()
}

// Done models an optional transport-liveness signal.
func (c *fakeConnection) Done() <-chan struct{} { return c.done }

func (c *fakeConnection) counts() (releases, discards int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.releases, c.discards
}

type fakeScheduler struct {
	mu        sync.Mutex
	delays    []time.Duration
	callbacks chan func()
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{callbacks: make(chan func(), 32)}
}

func (s *fakeScheduler) schedule(delay time.Duration, fn func()) func() {
	s.mu.Lock()
	s.delays = append(s.delays, delay)
	s.mu.Unlock()
	s.callbacks <- fn
	return func() {}
}

func (s *fakeScheduler) next(t *testing.T) func() {
	t.Helper()
	select {
	case fn := <-s.callbacks:
		return fn
	case <-time.After(3 * time.Second):
		t.Fatal("no reconnect was scheduled")
		return nil
	}
}

func (s *fakeScheduler) pending() int {
	return len(s.callbacks)
}

func (s *fakeScheduler) recordedDelays() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Duration(nil), s.delays...)
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func localRule(id string, port int) domainforward.Rule {
	return domainforward.Rule{
		ID: model.ForwardingRuleID(id), Label: id, Type: domainforward.TypeLocal,
		BindAddress: "127.0.0.1", LocalPort: port,
		RemoteHost: "127.0.0.1", RemotePort: 5432,
		HostID: "host-1", OrderKey: 1000,
	}
}

func waitStatus(t *testing.T, manager *Manager, id model.ForwardingRuleID, status domainforward.Status) View {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, view := range manager.Snapshot().Rules {
			if view.Rule.ID == id && view.Status == status {
				return view
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("rule %s never reached %q; snapshot = %+v", id, status, manager.Snapshot())
	return View{}
}

func writeBytes(t *testing.T, conn net.Conn, value string) {
	t.Helper()
	if _, err := conn.Write([]byte(value)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func readBytes(t *testing.T, conn net.Conn, count int) string {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buffer := make([]byte, count)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(buffer)
}

func TestCreateUpdateAndDeletePersistNormalizedRules(t *testing.T) {
	repo := newFakeRepository()
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	manager := NewManager(repo, &fakeConnector{},
		WithClock(func() time.Time { return fixed }),
		WithIDGenerator(func() model.ForwardingRuleID { return "rule-1" }),
	)
	t.Cleanup(manager.Close)

	created, err := manager.Create(context.Background(), domainforward.Draft{
		Label: "  web  ", Type: "LOCAL", LocalPort: 8080,
		RemoteHost: " db.internal ", RemotePort: 5432, HostID: " host-1 ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID != "rule-1" || created.OrderKey != 1000 ||
		created.CreatedAt != fixed || created.BindAddress != "127.0.0.1" {
		t.Fatalf("created = %+v", created)
	}

	updated, err := manager.Update(context.Background(), created.ID, domainforward.Draft{
		Label: "web-2", Type: domainforward.TypeDynamic, LocalPort: 1080, HostID: "host-1",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Label != "web-2" || updated.Type != domainforward.TypeDynamic ||
		updated.RemoteHost != "" || updated.RemotePort != 0 {
		t.Fatalf("updated = %+v", updated)
	}
	if updated.CreatedAt != fixed || updated.OrderKey != 1000 {
		t.Fatalf("Update changed identity fields: %+v", updated)
	}

	if err := manager.Delete(context.Background(), created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := manager.Delete(context.Background(), created.ID); err == nil {
		t.Fatal("deleting a missing rule succeeded")
	} else {
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != KindNotFound {
			t.Fatalf("error = %v, want not-found", err)
		}
	}
}

func TestListDropsMalformedStoredRules(t *testing.T) {
	good := localRule("good", 9000)
	bad := localRule("bad", 9001)
	bad.Type = "tunnel"
	repo := newFakeRepository(good, bad)
	manager := NewManager(repo, &fakeConnector{})
	t.Cleanup(manager.Close)

	views, err := manager.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 1 || views[0].Rule.ID != "good" {
		t.Fatalf("views = %+v, want only the valid rule", views)
	}
	if views[0].Status != domainforward.StatusInactive {
		t.Fatalf("status = %q, want inactive", views[0].Status)
	}
}

func TestStartLocalForwardsThroughTheConnection(t *testing.T) {
	port := freePort(t)
	rule := localRule("local", port)
	repo := newFakeRepository(rule)

	clientSide, managerSide := net.Pipe()
	conn := &fakeConnection{dial: func(context.Context, string, string) (net.Conn, error) {
		return managerSide, nil
	}}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	manager := NewManager(repo, connector)
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)

	local, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial local listener: %v", err)
	}
	defer local.Close()

	writeBytes(t, local, "ping")
	if got := readBytes(t, clientSide, 4); got != "ping" {
		t.Fatalf("forwarded = %q, want ping", got)
	}
	writeBytes(t, clientSide, "pong")
	if got := readBytes(t, local, 4); got != "pong" {
		t.Fatalf("reply = %q, want pong", got)
	}

	if err := manager.Stop(context.Background(), rule.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusInactive)
	deadline := time.Now().Add(2 * time.Second)
	for {
		releases, _ := conn.counts()
		if releases == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("releases = %d, want 1", releases)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond); err == nil {
		t.Fatal("the local listener still accepts after Stop")
	}
}

func TestStopDuringConnectDiscardsTheLateConnection(t *testing.T) {
	port := freePort(t)
	rule := localRule("late", port)
	repo := newFakeRepository(rule)

	connected := make(chan struct{})
	conn := &fakeConnection{dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unused")
	}}
	release := make(chan struct{})
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		close(connected)
		<-release
		return conn, nil
	}}
	manager := NewManager(repo, connector)
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-connected
	if err := manager.Stop(context.Background(), rule.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(release)
	waitStatus(t, manager, rule.ID, domainforward.StatusInactive)
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, discards := conn.counts()
		if discards == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("discards = %d, want the late connection discarded", discards)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStartRemoteForwardsInboundConnectionsLocally(t *testing.T) {
	rule := localRule("remote", 15432)
	rule.Type = domainforward.TypeRemote
	repo := newFakeRepository(rule)

	remoteListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	clientSide, managerSide := net.Pipe()
	conn := &fakeConnection{listen: func(string) (net.Listener, error) { return remoteListener, nil }}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	var dialed string
	manager := NewManager(repo, connector, WithLocalDial(func(_, address string) (net.Conn, error) {
		dialed = address
		return managerSide, nil
	}))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)

	client, err := net.Dial("tcp", remoteListener.Addr().String())
	if err != nil {
		t.Fatalf("dial remote listener: %v", err)
	}
	defer client.Close()
	writeBytes(t, client, "ping")
	if got := readBytes(t, clientSide, 4); got != "ping" {
		t.Fatalf("forwarded = %q, want ping", got)
	}
	conn.mu.Lock()
	requested := conn.lastAddr
	conn.mu.Unlock()
	if requested != "127.0.0.1:15432" {
		t.Fatalf("ListenTCP address = %q, want 127.0.0.1:15432", requested)
	}
	if dialed != "127.0.0.1:5432" {
		t.Fatalf("local destination = %q, want 127.0.0.1:5432", dialed)
	}
}

func TestDynamicRuleSpeaksSOCKS5AndTimesOutIdleConnections(t *testing.T) {
	port := freePort(t)
	rule := localRule("dynamic", port)
	rule.Type = domainforward.TypeDynamic
	rule.RemoteHost, rule.RemotePort = "", 0
	repo := newFakeRepository(rule)

	clientSide, managerSide := net.Pipe()
	conn := &fakeConnection{dial: func(context.Context, string, string) (net.Conn, error) {
		return managerSide, nil
	}}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	manager := NewManager(repo, connector, WithTimeouts(0, 100*time.Millisecond))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial SOCKS listener: %v", err)
	}
	defer client.Close()

	writeBytes(t, client, string([]byte{5, 1, 0}))
	greeting := make([]byte, 2)
	if _, err := io.ReadFull(client, greeting); err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	if greeting[0] != 5 || greeting[1] != 0 {
		t.Fatalf("greeting = %v, want no-auth", greeting)
	}

	host := "db.internal"
	request := []byte{5, 1, 0, 3, byte(len(host))}
	request = append(request, []byte(host)...)
	request = append(request, 0, 80)
	writeBytes(t, client, string(request))
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if reply[1] != 0 {
		t.Fatalf("SOCKS reply = %v, want success", reply)
	}
	conn.mu.Lock()
	destination := conn.lastDest
	conn.mu.Unlock()
	if destination != "db.internal:80" {
		t.Fatalf("dialed = %q, want db.internal:80", destination)
	}

	// The timeout covers the handshake only. An established tunnel stays open
	// through an idle period longer than the handshake deadline.
	time.Sleep(250 * time.Millisecond)
	writeBytes(t, client, "ping")
	if got := readBytes(t, clientSide, 4); got != "ping" {
		t.Fatalf("forwarded = %q, want ping", got)
	}
}

func TestDynamicHandshakeIsBoundedByTheIdleTimeout(t *testing.T) {
	port := freePort(t)
	rule := localRule("dynamic-handshake", port)
	rule.Type = domainforward.TypeDynamic
	rule.RemoteHost, rule.RemotePort = "", 0
	manager := NewManager(newFakeRepository(rule), &fakeConnector{}, WithTimeouts(0, 100*time.Millisecond))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("dial SOCKS listener: %v", err)
	}
	defer client.Close()
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("an idle SOCKS handshake stayed open")
	}
}

func TestBindConflictIsPermanentAndNeverReconnects(t *testing.T) {
	rule := localRule("conflict", 18080)
	rule.Type = domainforward.TypeRemote
	repo := newFakeRepository(rule)
	scheduler := newFakeScheduler()
	conn := &fakeConnection{listen: func(string) (net.Listener, error) {
		return nil, &net.OpError{Op: "listen", Net: "tcp", Err: syscall.EADDRINUSE}
	}}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	manager := NewManager(repo, connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if view.Error != ErrorBindConflict {
		t.Fatalf("error = %q, want bind conflict", view.Error)
	}
	if view.Attempts != 0 {
		t.Fatalf("attempts = %d, want none for a bind conflict", view.Attempts)
	}
	if len(scheduler.recordedDelays()) != 0 {
		t.Fatalf("scheduled %v, want no reconnect for a bind conflict", scheduler.recordedDelays())
	}
	_, discards := conn.counts()
	if discards != 1 {
		t.Fatalf("discards = %d, want 1", discards)
	}
}

func TestAutoReconnectIsBoundedToFiveAttemptsWithThreeSecondDelays(t *testing.T) {
	rule := localRule("retry", freePort(t))
	rule.AutoStart = true
	repo := newFakeRepository(rule)
	scheduler := newFakeScheduler()
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return nil, NewRuntimeError(ErrorNetwork, nil, "refused", errors.New("refused"))
	}}
	manager := NewManager(repo, connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	for attempt := 1; attempt <= ReconnectAttempts; attempt++ {
		view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
		if view.Attempts != attempt {
			t.Fatalf("attempts = %d, want %d", view.Attempts, attempt)
		}
		if attempt < ReconnectAttempts {
			scheduler.next(t)()
			// Wait for the next failure before firing again.
			deadline := time.Now().Add(3 * time.Second)
			for scheduler.pending() == 0 {
				if time.Now().After(deadline) {
					t.Fatalf("attempt %d did not schedule another retry", attempt)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
	}

	// The fifth retry was scheduled when the attempt count reached the bound;
	// firing it must not schedule another one.
	scheduler.next(t)()
	deadline := time.Now().Add(3 * time.Second)
	for connector.count() != ReconnectAttempts+1 {
		if time.Now().After(deadline) {
			t.Fatalf("connects = %d, want %d", connector.count(), ReconnectAttempts+1)
		}
		time.Sleep(5 * time.Millisecond)
	}
	view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if view.Attempts != ReconnectAttempts {
		t.Fatalf("attempts = %d, want the bound %d", view.Attempts, ReconnectAttempts)
	}
	if scheduler.pending() != 0 {
		t.Fatal("auto-reconnect scheduled more than the bound")
	}
	for _, delay := range scheduler.recordedDelays() {
		if delay != ReconnectDelay {
			t.Fatalf("delay = %s, want %s", delay, ReconnectDelay)
		}
	}
	if connector.count() != ReconnectAttempts+1 {
		t.Fatalf("connects = %d, want %d", connector.count(), ReconnectAttempts+1)
	}
}

func TestTransportLossMarksTheRuleFailedAndReconnects(t *testing.T) {
	rule := localRule("transport", freePort(t))
	rule.AutoStart = true
	repo := newFakeRepository(rule)
	scheduler := newFakeScheduler()
	conn := &fakeConnection{done: make(chan struct{})}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	manager := NewManager(repo, connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)

	// The local listener is still healthy; only the lease's transport died.
	close(conn.done)
	view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if view.Error != ErrorNetwork {
		t.Fatalf("error = %q, want network", view.Error)
	}
	scheduler.next(t)()
	deadline := time.Now().Add(3 * time.Second)
	for connector.count() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("connects = %d, want 2", connector.count())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRemoteEstablishNetworkFailureRetries(t *testing.T) {
	rule := localRule("establish-retry", 18081)
	rule.Type = domainforward.TypeRemote
	rule.AutoStart = true
	scheduler := newFakeScheduler()
	conn := &fakeConnection{listen: func(string) (net.Listener, error) {
		return nil, errors.New("ssh: connection reset by peer")
	}}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	manager := NewManager(newFakeRepository(rule), connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if view.Error != ErrorNetwork {
		t.Fatalf("error = %q, want network", view.Error)
	}
	scheduler.next(t)()
}

func TestStopCancelsAPendingReconnect(t *testing.T) {
	rule := localRule("cancel", freePort(t))
	rule.AutoStart = true
	repo := newFakeRepository(rule)
	scheduler := newFakeScheduler()
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return nil, NewRuntimeError(ErrorNetwork, nil, "refused", errors.New("refused"))
	}}
	manager := NewManager(repo, connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusError)
	pending := scheduler.next(t)

	if err := manager.Stop(context.Background(), rule.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	pending()
	time.Sleep(50 * time.Millisecond)
	if connector.count() != 1 {
		t.Fatalf("connects = %d, want the cancelled retry not to run", connector.count())
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusInactive)
}

func TestReconnectReReadsTheAutoReconnectGate(t *testing.T) {
	rule := localRule("gate", freePort(t))
	enabled := true
	rule.AutoReconnect = &enabled
	repo := newFakeRepository(rule)
	scheduler := newFakeScheduler()
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return nil, NewRuntimeError(ErrorNetwork, nil, "refused", errors.New("refused"))
	}}
	manager := NewManager(repo, connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusError)
	pending := scheduler.next(t)

	disabled := false
	rule.AutoReconnect = &disabled
	repo.put(rule)
	pending()
	time.Sleep(50 * time.Millisecond)

	if connector.count() != 1 {
		t.Fatalf("connects = %d, want the retry gated off", connector.count())
	}
	view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if view.Error != ErrorNetwork {
		t.Fatalf("error = %q, want the original network failure", view.Error)
	}
}

func TestAutoReconnectRecoversAndResetsTheAttemptCount(t *testing.T) {
	port := freePort(t)
	rule := localRule("recover", port)
	enabled := true
	rule.AutoReconnect = &enabled
	repo := newFakeRepository(rule)
	scheduler := newFakeScheduler()
	var mu sync.Mutex
	attempt := 0
	conn := &fakeConnection{dial: func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("unused")
	}}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		mu.Lock()
		defer mu.Unlock()
		attempt++
		if attempt == 1 {
			return nil, NewRuntimeError(ErrorNetwork, nil, "refused", errors.New("refused"))
		}
		return conn, nil
	}}
	manager := NewManager(repo, connector, WithScheduler(scheduler.schedule))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusError)
	scheduler.next(t)()
	view := waitStatus(t, manager, rule.ID, domainforward.StatusActive)
	if view.Attempts != 0 {
		t.Fatalf("attempts = %d, want reset after recovery", view.Attempts)
	}
}

func TestUnforwardTimeoutDiscardsTheTransport(t *testing.T) {
	rule := localRule("unforward", 19090)
	rule.Type = domainforward.TypeRemote
	repo := newFakeRepository(rule)
	listener := newBlockingListener()
	conn := &fakeConnection{listen: func(string) (net.Listener, error) { return listener, nil }}
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return conn, nil
	}}
	manager := NewManager(repo, connector, WithTimeouts(50*time.Millisecond, 0))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)
	if err := manager.Stop(context.Background(), rule.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	view := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if view.Error != ErrorUnforward {
		t.Fatalf("error = %q, want unforward", view.Error)
	}
	_, discards := conn.counts()
	if discards != 1 {
		t.Fatalf("discards = %d, want 1", discards)
	}
	close(listener.release)
}

func TestStartRefusesADisabledRuleAndAutoStartSkipsIt(t *testing.T) {
	disabled := false
	rule := localRule("disabled", freePort(t))
	rule.AutoStart = true
	rule.Enabled = &disabled
	repo := newFakeRepository(rule)
	connector := &fakeConnector{}
	manager := NewManager(repo, connector)
	t.Cleanup(manager.Close)

	err := manager.Start(context.Background(), rule.ID)
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindDisabled {
		t.Fatalf("Start error = %v, want disabled", err)
	}
	manager.AutoStart(context.Background())
	time.Sleep(50 * time.Millisecond)
	if connector.count() != 0 {
		t.Fatalf("connects = %d, want 0", connector.count())
	}
}

func TestAutoStartStartsOnlyEligibleRules(t *testing.T) {
	auto := localRule("auto", freePort(t))
	auto.AutoStart = true
	manual := localRule("manual", freePort(t))
	repo := newFakeRepository(auto, manual)
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return &fakeConnection{dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unused")
		}}, nil
	}}
	manager := NewManager(repo, connector)
	t.Cleanup(manager.Close)

	manager.AutoStart(context.Background())
	waitStatus(t, manager, auto.ID, domainforward.StatusActive)
	time.Sleep(50 * time.Millisecond)
	if connector.count() != 1 {
		t.Fatalf("connects = %d, want only the auto-start rule", connector.count())
	}
	for _, view := range manager.Snapshot().Rules {
		if view.Rule.ID == manual.ID && view.Status != domainforward.StatusInactive {
			t.Fatalf("manual rule status = %q, want inactive", view.Status)
		}
	}
}

func TestSnapshotRevisionsIncreaseAndEventsCarryTheView(t *testing.T) {
	rule := localRule("events", freePort(t))
	repo := newFakeRepository(rule)
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return &fakeConnection{dial: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("unused")
		}}, nil
	}}
	manager := NewManager(repo, connector)
	t.Cleanup(manager.Close)

	events := make(chan Event, 32)
	manager.Subscribe(func(event Event) { events <- event })
	if _, err := manager.List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	before := manager.Snapshot().Revision
	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)
	after := manager.Snapshot()
	if after.Revision <= before {
		t.Fatalf("revision = %d, want > %d", after.Revision, before)
	}
	select {
	case event := <-events:
		if event.Epoch != after.Epoch {
			t.Fatalf("event epoch = %d, want %d", event.Epoch, after.Epoch)
		}
		if event.Type != EventUpsert || event.ID != rule.ID {
			t.Fatalf("event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no runtime event was published")
	}
}

// blockingListener accepts forever and blocks Close until the test releases
// it, modelling a cancel-tcpip-forward request that never answers.
type blockingListener struct {
	closed  chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{closed: make(chan struct{}), release: make(chan struct{})}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	<-l.release
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *blockingListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 19090}
}
