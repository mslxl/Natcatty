package mcp

import (
	"context"
	"strings"
	"sync"
	"testing"
)

// fakeLiveSessions is the application-level port fixture: the app supplies the
// real Session service; tests move the live set and the sharing switch.
type fakeLiveSessions struct {
	mu      sync.Mutex
	live    []LiveSession
	enabled bool
}

func (f *fakeLiveSessions) LiveSessions() []LiveSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]LiveSession(nil), f.live...)
}

func (f *fakeLiveSessions) SharingEnabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *fakeLiveSessions) set(live []LiveSession) {
	f.mu.Lock()
	f.live = live
	f.mu.Unlock()
}

func (f *fakeLiveSessions) enable(enabled bool) {
	f.mu.Lock()
	f.enabled = enabled
	f.mu.Unlock()
}

func newLiveScopeFixture(allowlist []string, allowlistSet bool, source *fakeLiveSessions) *Service {
	return NewService(Config{
		ForcedChatSessionID: testForcedScope,
		Allowlist:           allowlist,
		AllowlistSet:        allowlistSet,
		Sessions:            source,
		Mode:                func() Mode { return ModeAuto },
	})
}

func sessionIDs(sessions []string) string { return strings.Join(sessions, ",") }

// TestLiveSessionsAreTheDynamicScope proves the shipped-app rule: while
// sharing is on, every live Session is in scope; the launch-time env allowlist,
// when present, narrows that set; when absent it does not empty the scope.
func TestLiveSessionsAreTheDynamicScope(t *testing.T) {
	source := &fakeLiveSessions{enabled: true, live: []LiveSession{
		{ID: testSessionA, HostID: testHostA, Label: "local"},
		{ID: testSessionB, HostID: "host-b", Label: "remote"},
	}}

	unset := newLiveScopeFixture(nil, false, source)
	got, err := unset.EffectiveSessions(RequestedScope{})
	if err != nil || sessionIDs(got) != testSessionA+","+testSessionB {
		t.Fatalf("unset env effective = %v (%v), want both live Sessions", got, err)
	}

	narrowed := newLiveScopeFixture([]string{testSessionB}, true, source)
	got, err = narrowed.EffectiveSessions(RequestedScope{})
	if err != nil || sessionIDs(got) != testSessionB {
		t.Fatalf("static allowlist effective = %v (%v), want only %s", got, err, testSessionB)
	}

	emptyStatic := newLiveScopeFixture([]string{}, true, source)
	if got, err := emptyStatic.EffectiveSessions(RequestedScope{}); err != nil || len(got) != 0 {
		t.Fatalf("explicit empty env allowlist effective = %v (%v), want no access", got, err)
	}

	source.enable(false)
	if got, err := unset.EffectiveSessions(RequestedScope{}); err != nil || len(got) != 0 {
		t.Fatalf("disabled sharing effective = %v (%v), want no access", got, err)
	}
}

// TestLiveSessionScopeHasNoStaleIDs proves closing a Session removes it on the
// next call and opening one adds it, with no cached identity list.
func TestLiveSessionScopeHasNoStaleIDs(t *testing.T) {
	source := &fakeLiveSessions{enabled: true, live: []LiveSession{{ID: testSessionA}}}
	service := newLiveScopeFixture(nil, false, source)
	if got, _ := service.EffectiveSessions(RequestedScope{}); sessionIDs(got) != testSessionA {
		t.Fatalf("initial effective = %v", got)
	}
	source.set([]LiveSession{{ID: testSessionB, HostID: "host-b"}})
	if got, _ := service.EffectiveSessions(RequestedScope{}); sessionIDs(got) != testSessionB {
		t.Fatalf("effective after close/reopen = %v, want only %s", got, testSessionB)
	}
	if _, err := service.Authorize(context.Background(), testRequest(testTool("terminal_execute"), testSessionA)); err == nil {
		t.Fatal("a closed Session must leave the scope")
	}
}

// TestLiveScopeKeepsRequestedSubsetSemantics proves explicit scopedSessionIds
// still narrow and explicit [] still means nothing under a dynamic scope.
func TestLiveScopeKeepsRequestedSubsetSemantics(t *testing.T) {
	source := &fakeLiveSessions{enabled: true, live: []LiveSession{{ID: testSessionA}, {ID: testSessionB}}}
	service := newLiveScopeFixture(nil, false, source)
	got, err := service.EffectiveSessions(RequestedScope{
		SessionIDs: []string{testSessionB}, SessionIDsPresent: true,
	})
	if err != nil || sessionIDs(got) != testSessionB {
		t.Fatalf("subset effective = %v (%v)", got, err)
	}
	got, err = service.EffectiveSessions(RequestedScope{SessionIDsPresent: true})
	if err != nil || len(got) != 0 {
		t.Fatalf("explicit empty effective = %v (%v)", got, err)
	}
	_, err = service.EffectiveSessions(RequestedScope{
		SessionIDs: []string{"outside"}, SessionIDsPresent: true,
	})
	requireCode(t, err, CodeSessionOutOfScope)
}

// TestSnapshotExposesTheSharedSessionList proves Settings can render the
// localized count and list without reaching into the Session service.
func TestSnapshotExposesTheSharedSessionList(t *testing.T) {
	source := &fakeLiveSessions{enabled: true, live: []LiveSession{
		{ID: testSessionA, HostID: testHostA, Label: "local shell"},
		{ID: testSessionB, HostID: "host-b"},
	}}
	service := newLiveScopeFixture(nil, false, source)
	snapshot := service.Snapshot()
	if !snapshot.SharingEnabled || len(snapshot.ExposedSessions) != 2 {
		t.Fatalf("snapshot sharing=%v exposed=%#v", snapshot.SharingEnabled, snapshot.ExposedSessions)
	}
	if snapshot.ExposedSessions[0].ID != testSessionA || snapshot.ExposedSessions[0].Label != "local shell" {
		t.Fatalf("exposed Session = %#v", snapshot.ExposedSessions[0])
	}
	if sessionIDs(snapshot.Allowlist) != testSessionA+","+testSessionB {
		t.Fatalf("snapshot allowlist = %v, want the effective scope", snapshot.Allowlist)
	}

	source.enable(false)
	snapshot = service.Snapshot()
	if snapshot.SharingEnabled || len(snapshot.ExposedSessions) != 0 || len(snapshot.Allowlist) != 0 {
		t.Fatalf("disabled snapshot = %#v", snapshot)
	}

	static := newLiveScopeFixture([]string{testSessionA}, true, source)
	source.enable(true)
	snapshot = static.Snapshot()
	if !snapshot.EnvAllowlistSet || len(snapshot.ExposedSessions) != 1 ||
		snapshot.ExposedSessions[0].ID != testSessionA {
		t.Fatalf("filtered snapshot = %#v", snapshot)
	}
}
