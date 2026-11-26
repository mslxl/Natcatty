package app

// App-level Port Forwarding tests. The phone wrapper's lifecycle methods are
// driven directly, while the shared manager runs over fake transports, so the
// tests prove rebuild-on-foreground/network-change, per-rule failure
// isolation, Retry, and the restart auto-start filter without a device or a
// real SSH server.

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

// phoneForwardingRepo is the rule repository with no storage behind it.
type phoneForwardingRepo struct {
	mu    sync.Mutex
	rules []domainforward.Rule
}

func (r *phoneForwardingRepo) ListForwardingRules(context.Context) ([]domainforward.Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domainforward.Rule(nil), r.rules...), nil
}

func (r *phoneForwardingRepo) GetForwardingRule(_ context.Context, id model.ForwardingRuleID) (domainforward.Rule, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rule := range r.rules {
		if rule.ID == id {
			return rule, nil
		}
	}
	return domainforward.Rule{}, appforwarding.ErrNotFound
}

func (r *phoneForwardingRepo) SaveForwardingRule(_ context.Context, rule domainforward.Rule) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rules {
		if r.rules[i].ID == rule.ID {
			r.rules[i] = rule
			return nil
		}
	}
	r.rules = append(r.rules, rule)
	return nil
}

func (r *phoneForwardingRepo) DeleteForwardingRule(_ context.Context, id model.ForwardingRuleID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rules {
		if r.rules[i].ID == id {
			r.rules = append(r.rules[:i], r.rules[i+1:]...)
			return nil
		}
	}
	return appforwarding.ErrNotFound
}

// phoneFakeTransport is the forwarding connector: it counts connects and can
// fail one Host, which is how a rebuild failure is isolated to one rule.
type phoneFakeTransport struct {
	mu       sync.Mutex
	hosts    []model.HostID
	fail     map[model.HostID]bool
	listener func() net.Listener
}

func (c *phoneFakeTransport) Connect(_ context.Context, hostID model.HostID) (appforwarding.Connection, error) {
	c.mu.Lock()
	c.hosts = append(c.hosts, hostID)
	failed := c.fail[hostID]
	c.mu.Unlock()
	if failed {
		return nil, errors.New("network is down")
	}
	return &phoneFakeConnection{listener: c.listener}, nil
}

func (c *phoneFakeTransport) connects() []model.HostID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.HostID(nil), c.hosts...)
}

func (c *phoneFakeTransport) setFail(hosts ...model.HostID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = make(map[model.HostID]bool, len(hosts))
	for _, host := range hosts {
		c.fail[host] = true
	}
}

type phoneFakeConnection struct {
	listener func() net.Listener
}

func (c *phoneFakeConnection) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("fake connection: no dial")
}

func (c *phoneFakeConnection) ListenTCP(string) (net.Listener, error) {
	if c.listener != nil {
		return c.listener(), nil
	}
	return newPhoneFakeListener(), nil
}

func (c *phoneFakeConnection) Release() {}
func (c *phoneFakeConnection) Discard() {}

// phoneFakeListener is a listener that only ever blocks in Accept until it is
// closed, so an established rule stays active.
type phoneFakeListener struct {
	closed chan struct{}
	once   sync.Once
}

func newPhoneFakeListener() *phoneFakeListener {
	return &phoneFakeListener{closed: make(chan struct{})}
}

func (l *phoneFakeListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *phoneFakeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *phoneFakeListener) Addr() net.Addr { return phoneFakeAddr("127.0.0.1:0") }

type phoneFakeAddr string

func (a phoneFakeAddr) Network() string { return "tcp" }
func (a phoneFakeAddr) String() string  { return string(a) }

func phoneForwardingRule(id, hostID string, kind domainforward.Type, order int64) domainforward.Rule {
	return domainforward.Rule{
		ID: model.ForwardingRuleID(id), Label: id, Type: kind,
		BindAddress: "127.0.0.1", LocalPort: 8080,
		RemoteHost: "127.0.0.1", RemotePort: 5432,
		HostID: model.HostID(hostID), OrderKey: order,
	}
}

func newPhoneForwardingManager(t *testing.T, rules ...domainforward.Rule) (*appforwarding.Manager, *phoneFakeTransport) {
	t.Helper()
	transport := &phoneFakeTransport{}
	manager := appforwarding.NewManager(&phoneForwardingRepo{rules: rules}, transport,
		appforwarding.WithListen(func(string, string) (net.Listener, error) {
			return newPhoneFakeListener(), nil
		}))
	t.Cleanup(manager.Close)
	return manager, transport
}

func waitPhoneForwarding(t *testing.T, manager *appforwarding.Manager,
	id model.ForwardingRuleID, status domainforward.Status) appforwarding.View {
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
	return appforwarding.View{}
}

func startPhoneForwardingRules(t *testing.T, manager *appforwarding.Manager, rules ...domainforward.Rule) {
	t.Helper()
	for _, rule := range rules {
		if err := manager.Start(context.Background(), rule.ID); err != nil {
			t.Fatalf("Start(%s): %v", rule.ID, err)
		}
	}
	for _, rule := range rules {
		waitPhoneForwarding(t, manager, rule.ID, domainforward.StatusActive)
	}
}

// TestPhoneForwardingForegroundRebuildsEveryDirection covers the foreground
// path: Local, Remote, and Dynamic rules all acquire a fresh transport, and a
// successful rebuild leaves every rule active and error-free.
func TestPhoneForwardingForegroundRebuildsEveryDirection(t *testing.T) {
	rules := []domainforward.Rule{
		phoneForwardingRule("local", "host-local", domainforward.TypeLocal, 1000),
		phoneForwardingRule("remote", "host-remote", domainforward.TypeRemote, 2000),
		phoneForwardingRule("dynamic", "host-dynamic", domainforward.TypeDynamic, 3000),
	}
	manager, transport := newPhoneForwardingManager(t, rules...)
	startPhoneForwardingRules(t, manager, rules...)

	runtime := &phoneForwarding{manager: manager}
	runtime.foreground()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(transport.connects()) < len(rules)*2 {
		time.Sleep(5 * time.Millisecond)
	}
	for _, rule := range rules {
		view := waitPhoneForwarding(t, manager, rule.ID, domainforward.StatusActive)
		if view.Error != appforwarding.ErrorNone {
			t.Fatalf("%s error after foreground rebuild = %q, want none", rule.ID, view.Error)
		}
	}
	if got := len(transport.connects()); got != len(rules)*2 {
		t.Fatalf("connects after foreground rebuild = %d, want %d", got, len(rules)*2)
	}
}

// TestPhoneForwardingLaunchActivationDoesNotRebuild checks that the activation
// reported at launch leaves the auto-start connects alone, while a real return
// from the background rebuilds them.
func TestPhoneForwardingLaunchActivationDoesNotRebuild(t *testing.T) {
	rule := phoneForwardingRule("local", "host-local", domainforward.TypeLocal, 1000)
	manager, transport := newPhoneForwardingManager(t, rule)
	startPhoneForwardingRules(t, manager, rule)
	runtime := &phoneForwarding{manager: manager}

	runtime.becameActive()
	time.Sleep(50 * time.Millisecond)
	if got := len(transport.connects()); got != 1 {
		t.Fatalf("launch activation rebuilt the rule: connects = %d, want 1", got)
	}

	runtime.resignedActive()
	runtime.becameActive()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(transport.connects()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := len(transport.connects()); got != 2 {
		t.Fatalf("foreground return connects = %d, want 2", got)
	}
	waitPhoneForwarding(t, manager, rule.ID, domainforward.StatusActive)
}

// TestPhoneForwardingNetworkChangeClearsAPriorError covers the network-change
// path and the rule-local retry: a failed rebuild shows an error only on that
// rule, and a later successful start clears it.
func TestPhoneForwardingNetworkChangeClearsAPriorError(t *testing.T) {
	rule := phoneForwardingRule("local", "host-local", domainforward.TypeLocal, 1000)
	manager, transport := newPhoneForwardingManager(t, rule)
	runtime := &phoneForwarding{manager: manager}

	transport.setFail("host-local")
	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	failed := waitPhoneForwarding(t, manager, rule.ID, domainforward.StatusError)
	if failed.Error == appforwarding.ErrorNone {
		t.Fatal("the failed start recorded no error")
	}

	// The network changed; the rebuild still fails, and the error stays on the
	// rule with a Retry entry.
	runtime.networkChanged()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && len(transport.connects()) < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	waitPhoneForwarding(t, manager, rule.ID, domainforward.StatusError)

	// Retry: the user taps the rule's retry entry, which calls Start.
	transport.setFail()
	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	recovered := waitPhoneForwarding(t, manager, rule.ID, domainforward.StatusActive)
	if recovered.Error != appforwarding.ErrorNone || recovered.Detail != "" {
		t.Fatalf("recovered view = %+v, want the error cleared", recovered)
	}
}

// TestPhoneForwardingRebuildFailureIsIsolatedToOneRule proves a failed rebuild
// on one direction leaves the other running directions untouched.
func TestPhoneForwardingRebuildFailureIsIsolatedToOneRule(t *testing.T) {
	rules := []domainforward.Rule{
		phoneForwardingRule("local", "host-local", domainforward.TypeLocal, 1000),
		phoneForwardingRule("remote", "host-remote", domainforward.TypeRemote, 2000),
		phoneForwardingRule("dynamic", "host-dynamic", domainforward.TypeDynamic, 3000),
	}
	manager, transport := newPhoneForwardingManager(t, rules...)
	startPhoneForwardingRules(t, manager, rules...)

	transport.setFail("host-remote")
	runtime := &phoneForwarding{manager: manager}
	runtime.foreground()

	failed := waitPhoneForwarding(t, manager, "remote", domainforward.StatusError)
	if failed.Error != appforwarding.ErrorNetwork {
		t.Fatalf("remote error = %q, want %q", failed.Error, appforwarding.ErrorNetwork)
	}
	for _, id := range []model.ForwardingRuleID{"local", "dynamic"} {
		if view := waitPhoneForwarding(t, manager, id, domainforward.StatusActive); view.Error != appforwarding.ErrorNone {
			t.Fatalf("%s error = %q; one rule's failure leaked", id, view.Error)
		}
	}
}

// TestPhoneForwardingRestartStartsOnlyAutoStartRules simulates a killed and
// restarted process: a fresh manager over the same repository only starts the
// enabled rules that carry the auto-start flag.
func TestPhoneForwardingRestartStartsOnlyAutoStartRules(t *testing.T) {
	disabled := false
	repo := &phoneForwardingRepo{rules: []domainforward.Rule{
		{ID: "auto", Label: "auto", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: 1080, HostID: "host-auto",
			AutoStart: true, OrderKey: 1000},
		{ID: "manual", Label: "manual", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: 1081, HostID: "host-manual",
			OrderKey: 2000},
		{ID: "disabled", Label: "disabled", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: 1082, HostID: "host-disabled",
			AutoStart: true, Enabled: &disabled, OrderKey: 3000},
	}}
	transport := &phoneFakeTransport{}
	manager := appforwarding.NewManager(repo, transport,
		appforwarding.WithListen(func(string, string) (net.Listener, error) {
			return newPhoneFakeListener(), nil
		}))
	t.Cleanup(manager.Close)

	runtime := &phoneForwarding{manager: manager}
	runtime.startAutoRules(context.Background())
	waitPhoneForwarding(t, manager, "auto", domainforward.StatusActive)

	connects := transport.connects()
	if len(connects) != 1 || connects[0] != "host-auto" {
		t.Fatalf("restart connects = %v, want only the auto-start enabled rule", connects)
	}
	for _, id := range []model.ForwardingRuleID{"manual", "disabled"} {
		if view := waitPhoneForwardingView(manager, id); view.Status != domainforward.StatusInactive {
			t.Fatalf("%s status = %q, want inactive", id, view.Status)
		}
	}
}

func waitPhoneForwardingView(manager *appforwarding.Manager, id model.ForwardingRuleID) appforwarding.View {
	for _, view := range manager.Snapshot().Rules {
		if view.Rule.ID == id {
			return view
		}
	}
	return appforwarding.View{}
}
