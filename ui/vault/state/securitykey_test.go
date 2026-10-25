package state

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func waitForSecurityKeyStatus(t *testing.T, state *State) (appconnection.SecurityKeySummary, bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.FlushSecurityKeys()
		if summary, ok := state.SecurityKeyStatus(); ok {
			return summary, true
		}
		time.Sleep(time.Millisecond)
	}
	return appconnection.SecurityKeySummary{}, false
}

func TestSecurityKeyProbeRunsOnceAndPublishesItsSummary(t *testing.T) {
	var calls atomic.Int32
	state := NewState(&fakeHosts{}, WithSecurityKeyProbe(func(context.Context) appconnection.SecurityKeySummary {
		calls.Add(1)
		return appconnection.SecurityKeySummary{Available: true, Count: 2}
	}))
	if _, ok := state.SecurityKeyStatus(); ok {
		t.Fatal("the status is ready before the probe ran")
	}

	state.FlushSecurityKeys()
	state.FlushSecurityKeys()
	summary, ok := waitForSecurityKeyStatus(t, state)
	if !ok {
		t.Fatal("the probe result was never published")
	}
	if !summary.Available || summary.Count != 2 {
		t.Fatalf("summary = %+v, want available with two keys", summary)
	}
	state.FlushSecurityKeys()
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe calls = %d, want 1", got)
	}
}

func TestSecurityKeyProbeReportsAnUnreachableAgent(t *testing.T) {
	state := NewState(&fakeHosts{}, WithSecurityKeyProbe(func(context.Context) appconnection.SecurityKeySummary {
		return appconnection.SecurityKeySummary{}
	}))
	state.FlushSecurityKeys()
	summary, ok := waitForSecurityKeyStatus(t, state)
	if !ok {
		t.Fatal("the probe result was never published")
	}
	if summary.Available || summary.Count != 0 {
		t.Fatalf("summary = %+v, want an unavailable agent", summary)
	}
}

func TestTestConnectionKeepsTheSecurityKeyCause(t *testing.T) {
	hosts := &fakeHosts{items: []domainvault.Host{testConnectionHost()}}
	state := NewState(hosts, WithConnectionTest(func(context.Context, domainvault.Host) error {
		return appconnection.SecurityKeyTimeout(nil)
	}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.Test("host-1") {
		t.Fatal("Test returned false for a listed Host")
	}
	waitForTestOutcome(t, state, "host-1")

	if got := state.TestProblem("host-1"); got != appconnection.KindTimeout {
		t.Fatalf("problem kind = %q, want %q", got, appconnection.KindTimeout)
	}
	err := state.TestProblemError("host-1")
	if !errors.Is(err, appconnection.ErrSecurityKeyDeviceTimeout) {
		t.Fatalf("problem cause = %v, want ErrSecurityKeyDeviceTimeout", err)
	}
}
