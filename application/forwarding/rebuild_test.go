package forwarding

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
)

// rulesForRebuild is one running rule of each direction. The local and
// dynamic rules bind real loopback listeners, so their rebuild proves the
// listen address is released before the next attempt; the remote rule's
// listener comes from the fake transport, which is where a remote rule binds.
func rulesForRebuild(t *testing.T) []domainforward.Rule {
	t.Helper()
	return []domainforward.Rule{
		{
			ID: "local", Label: "local", Type: domainforward.TypeLocal,
			BindAddress: "127.0.0.1", LocalPort: freePort(t),
			RemoteHost: "127.0.0.1", RemotePort: 5432,
			HostID: "host-local", OrderKey: 1000,
		},
		{
			ID: "remote", Label: "remote", Type: domainforward.TypeRemote,
			BindAddress: "127.0.0.1", LocalPort: freePort(t),
			RemoteHost: "127.0.0.1", RemotePort: 80,
			HostID: "host-remote", OrderKey: 2000,
		},
		{
			ID: "dynamic", Label: "dynamic", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: freePort(t),
			HostID: "host-dynamic", OrderKey: 3000,
		},
	}
}

func remoteListener(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return listener
}

// startAll starts every rule and waits for it to become active.
func startAll(t *testing.T, manager *Manager, rules []domainforward.Rule) {
	t.Helper()
	for _, rule := range rules {
		if err := manager.Start(context.Background(), rule.ID); err != nil {
			t.Fatalf("Start(%s): %v", rule.ID, err)
		}
	}
	for _, rule := range rules {
		waitStatus(t, manager, rule.ID, domainforward.StatusActive)
	}
}

func TestRebuildRunningRebuildsLocalRemoteAndDynamicRules(t *testing.T) {
	rules := rulesForRebuild(t)
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		return &fakeConnection{
			listen: func(string) (net.Listener, error) { return remoteListener(t), nil },
		}, nil
	}}
	manager := NewManager(newFakeRepository(rules...), connector)
	t.Cleanup(manager.Close)

	startAll(t, manager, rules)
	if got := connector.count(); got != len(rules) {
		t.Fatalf("connects after start = %d, want %d", got, len(rules))
	}

	manager.RebuildRunning(context.Background())

	for _, rule := range rules {
		view := waitStatus(t, manager, rule.ID, domainforward.StatusActive)
		if view.Error != ErrorNone {
			t.Fatalf("%s error after a successful rebuild = %q, want none", rule.ID, view.Error)
		}
	}
	// Every direction attempted a fresh connection: two local/dynamic listens
	// and one remote listen all acquire a transport.
	if got := connector.count(); got != len(rules)*2 {
		t.Fatalf("connects after rebuild = %d, want %d", got, len(rules)*2)
	}
}

func TestRebuildRunningClearsAPriorError(t *testing.T) {
	rule := localRule("local", freePort(t))
	fail := true
	connector := &fakeConnector{connect: func(context.Context, model.HostID) (Connection, error) {
		if fail {
			return nil, errors.New("network is down")
		}
		return &fakeConnection{}, nil
	}}
	manager := NewManager(newFakeRepository(rule), connector)
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	failed := waitStatus(t, manager, rule.ID, domainforward.StatusError)
	if failed.Error == ErrorNone {
		t.Fatal("the failed start recorded no error kind")
	}

	fail = false
	manager.RebuildRunning(context.Background())

	rebuilt := waitStatus(t, manager, rule.ID, domainforward.StatusActive)
	if rebuilt.Error != ErrorNone || rebuilt.Detail != "" {
		t.Fatalf("rebuilt view = %+v, want the error cleared", rebuilt)
	}
}

func TestRebuildRunningIsolatesOneRulesFailure(t *testing.T) {
	rules := rulesForRebuild(t)
	failing := false
	connector := &fakeConnector{connect: func(_ context.Context, hostID model.HostID) (Connection, error) {
		if failing && hostID == "host-remote" {
			return nil, errors.New("network is down")
		}
		return &fakeConnection{
			listen: func(string) (net.Listener, error) { return remoteListener(t), nil },
		}, nil
	}}
	manager := NewManager(newFakeRepository(rules...), connector)
	t.Cleanup(manager.Close)

	startAll(t, manager, rules)
	failing = true
	manager.RebuildRunning(context.Background())

	failed := waitStatus(t, manager, "remote", domainforward.StatusError)
	if failed.Error != ErrorNetwork {
		t.Fatalf("remote error = %q, want %q", failed.Error, ErrorNetwork)
	}
	for _, id := range []model.ForwardingRuleID{"local", "dynamic"} {
		if view := waitStatus(t, manager, id, domainforward.StatusActive); view.Error != ErrorNone {
			t.Fatalf("%s error = %q, want none; one rule's failure leaked", id, view.Error)
		}
	}
}

func TestRebuildRunningDoesNotRestartARuleStoppedDuringCleanup(t *testing.T) {
	listener := &stopAwareListener{
		closeStarted: make(chan struct{}),
		unblock:      make(chan struct{}),
		closed:       make(chan struct{}),
	}
	rule := localRule("local", freePort(t))
	connector := &fakeConnector{}
	manager := NewManager(newFakeRepository(rule), connector,
		WithListen(func(string, string) (net.Listener, error) { return listener, nil }))
	t.Cleanup(manager.Close)

	if err := manager.Start(context.Background(), rule.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, manager, rule.ID, domainforward.StatusActive)

	done := make(chan struct{})
	go func() {
		manager.RebuildRunning(context.Background())
		close(done)
	}()
	// The rebuild has stopped the entry and is releasing its listener. The
	// user stops the rule now; the release is still in flight.
	<-listener.closeStarted
	if err := manager.Stop(context.Background(), rule.ID); err != nil {
		t.Fatalf("Stop during rebuild: %v", err)
	}
	close(listener.unblock)
	<-done

	// Give a wrongly restarted generation time to reach the connector.
	time.Sleep(50 * time.Millisecond)
	if got := connector.count(); got != 1 {
		t.Fatalf("rebuild restarted a stopped rule: connects = %d, want 1", got)
	}
	if view := manager.Snapshot().Rules[0]; view.Status != domainforward.StatusInactive {
		t.Fatalf("status after stop during rebuild = %q, want inactive", view.Status)
	}
}

// stopAwareListener blocks Close until the test releases it, which keeps a
// rebuild inside its cleanup window while the test stops the rule.
type stopAwareListener struct {
	closeStarted chan struct{}
	unblock      chan struct{}
	closed       chan struct{}
	once         sync.Once
}

func (l *stopAwareListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}

func (l *stopAwareListener) Close() error {
	l.once.Do(func() {
		close(l.closeStarted)
		<-l.unblock
		close(l.closed)
	})
	return nil
}

func (l *stopAwareListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func TestAutoStartStartsOnlyEnabledAutoStartRules(t *testing.T) {
	disabled := false
	rules := []domainforward.Rule{
		{ID: "auto", Label: "auto", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: freePort(t),
			HostID: "host-auto", AutoStart: true, OrderKey: 1000},
		{ID: "manual", Label: "manual", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: freePort(t),
			HostID: "host-manual", OrderKey: 2000},
		{ID: "disabled", Label: "disabled", Type: domainforward.TypeDynamic,
			BindAddress: "127.0.0.1", LocalPort: freePort(t),
			HostID: "host-disabled", AutoStart: true, Enabled: &disabled, OrderKey: 3000},
	}
	connector := &fakeConnector{}
	manager := NewManager(newFakeRepository(rules...), connector)
	t.Cleanup(manager.Close)

	manager.AutoStart(context.Background())
	waitStatus(t, manager, "auto", domainforward.StatusActive)

	if got := connector.count(); got != 1 {
		t.Fatalf("auto-start connects = %d, want only the one auto-start enabled rule", got)
	}
	for _, id := range []model.ForwardingRuleID{"manual", "disabled"} {
		found := false
		for _, view := range manager.Snapshot().Rules {
			if view.Rule.ID != id {
				continue
			}
			found = true
			if view.Status != domainforward.StatusInactive {
				t.Fatalf("%s status = %q, want inactive", id, view.Status)
			}
		}
		if !found {
			t.Fatalf("%s is missing from the runtime snapshot", id)
		}
	}
}
