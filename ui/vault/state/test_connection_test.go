package state

import (
	"context"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func testConnectionHost() domainvault.Host {
	return domainvault.Host{
		ID: "host-1", Label: "prod", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}
}

func waitForTestOutcome(t *testing.T, state *State, id model.HostID) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.FlushTests()
		if !state.Testing(id) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for the Test connection outcome for %s", id)
}

func TestTestConnectionReportsSuccess(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{testConnectionHost()}}
	started := make(chan struct{})
	release := make(chan struct{})
	var tested domainvault.Host
	state := NewState(hosts, WithConnectionTest(func(_ context.Context, host domainvault.Host) error {
		tested = host
		close(started)
		<-release
		return nil
	}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if !state.Test("host-1") {
		t.Fatal("Test returned false for a listed Host")
	}
	<-started
	if !state.Testing("host-1") {
		t.Fatal("the Host is not reported as testing while the dial runs")
	}
	state.FlushTests()
	if !state.Testing("host-1") {
		t.Fatal("FlushTests cleared an attempt that has not finished")
	}
	close(release)
	waitForTestOutcome(t, state, "host-1")

	if tested.ID != "host-1" {
		t.Fatalf("tester received Host %q, want host-1", tested.ID)
	}
	if !state.TestSucceeded("host-1") || state.TestProblem("host-1") != "" {
		t.Fatalf("result = success:%t problem:%q, want success", state.TestSucceeded("host-1"), state.TestProblem("host-1"))
	}
}

func TestTestConnectionReportsATypedFailure(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{testConnectionHost()}}
	state := NewState(hosts, WithConnectionTest(func(context.Context, domainvault.Host) error {
		return &appconnection.Error{Kind: appconnection.KindAuth, Cause: appconnection.ErrAuthentication}
	}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if !state.Test("host-1") {
		t.Fatal("Test returned false for a listed Host")
	}
	waitForTestOutcome(t, state, "host-1")
	if got := state.TestProblem("host-1"); got != appconnection.KindAuth {
		t.Fatalf("problem = %q, want auth", got)
	}
	if state.TestSucceeded("host-1") {
		t.Fatal("a failed attempt is reported as successful")
	}
}

func TestTestConnectionWithoutASeamOrHostIsRejected(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{testConnectionHost()}}
	state := NewState(hosts)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if state.Test("host-1") {
		t.Fatal("Test succeeded without a connection seam")
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("problem = %q, want internal", state.Problem())
	}

	withSeam := NewState(hosts, WithConnectionTest(func(context.Context, domainvault.Host) error { return nil }))
	_ = withSeam.Reload()
	if withSeam.Test("missing") {
		t.Fatal("Test accepted an unknown Host")
	}
	if withSeam.Problem() != ProblemNotFound {
		t.Fatalf("problem = %q, want not_found", withSeam.Problem())
	}
}
