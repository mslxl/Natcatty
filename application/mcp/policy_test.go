package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testForcedScope = "__external_mcp__"
	testSessionA    = "session-a"
	testSessionB    = "session-b"
	testHostA       = "host-a"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type fakeMode struct {
	mu   sync.Mutex
	mode Mode
}

func newFakeMode(mode Mode) *fakeMode { return &fakeMode{mode: mode} }

func (m *fakeMode) Get() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

func (m *fakeMode) Set(mode Mode) {
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
}

type policyFixture struct {
	service *Service
	clock   *fakeClock
	mode    *fakeMode
	signals chan struct{}
}

func newPolicyFixture(t *testing.T, allowlist []string, mode Mode) *policyFixture {
	t.Helper()
	fixture := &policyFixture{
		clock:   newFakeClock(),
		mode:    newFakeMode(mode),
		signals: make(chan struct{}, 64),
	}
	fixture.service = NewService(Config{
		ForcedChatSessionID: testForcedScope,
		Allowlist:           allowlist,
		Mode:                fixture.mode.Get,
		Now:                 fixture.clock.Now,
		Schedule:            func(time.Duration, func()) func() { return func() {} },
		Redraw: func() {
			select {
			case fixture.signals <- struct{}{}:
			default:
			}
		},
	})
	return fixture
}

func testTool(name string) Tool {
	return Tool{Name: name, Write: true}
}

func testRequest(tool Tool, sessionID string) Request {
	return Request{
		Tool:   tool,
		Client: "netcatty-remote-hosts/1.0.0",
		Scope:  RequestedScope{},
		Target: Target{SessionID: sessionID, Display: sessionID},
		Arguments: map[string]any{
			"sessionId": sessionID,
			"command":   "echo hello",
		},
	}
}

type authorizeResult struct {
	decision Decision
	err      error
}

func startAuthorize(service *Service, request Request) <-chan authorizeResult {
	done := make(chan authorizeResult, 1)
	go func() {
		decision, err := service.Authorize(context.Background(), request)
		done <- authorizeResult{decision: decision, err: err}
	}()
	return done
}

func awaitPending(t *testing.T, fixture *policyFixture) ApprovalView {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		if pending := fixture.service.Snapshot().Pending; len(pending) > 0 {
			return pending[0]
		}
		select {
		case <-fixture.signals:
		case <-timeout:
			t.Fatal("approval prompt did not appear")
			return ApprovalView{}
		}
	}
}

func awaitResult(t *testing.T, done <-chan authorizeResult) authorizeResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(10 * time.Second):
		t.Fatal("authorize did not return")
		return authorizeResult{}
	}
}

func requireCode(t *testing.T, err error, code ErrorCode) *Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, got nil", code)
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("expected typed policy error, got %T: %v", err, err)
	}
	if typed.Code != code {
		t.Fatalf("expected code %s, got %s (%v)", code, typed.Code, err)
	}
	return typed
}

func TestScopeIntersection(t *testing.T) {
	allowlist := []string{testSessionA, testSessionB}
	cases := []struct {
		name      string
		requested RequestedScope
		want      []string
		code      ErrorCode
	}{
		{
			name:      "omitted chat scope and session list inherit the allowlist",
			requested: RequestedScope{},
			want:      []string{testSessionA, testSessionB},
		},
		{
			name:      "matching forced chat scope is allowed",
			requested: RequestedScope{ChatSessionID: testForcedScope},
			want:      []string{testSessionA, testSessionB},
		},
		{
			name:      "foreign chat scope has an empty intersection",
			requested: RequestedScope{ChatSessionID: "chat-other"},
			code:      CodeScopeMismatch,
		},
		{
			name: "requested session list narrows the allowlist",
			requested: RequestedScope{
				SessionIDs:        []string{testSessionB},
				SessionIDsPresent: true,
			},
			want: []string{testSessionB},
		},
		{
			name: "duplicate requested sessions are collapsed",
			requested: RequestedScope{
				SessionIDs:        []string{testSessionB, testSessionA, testSessionB},
				SessionIDsPresent: true,
			},
			want: []string{testSessionB, testSessionA},
		},
		{
			name: "explicit empty list means no session access",
			requested: RequestedScope{
				SessionIDs:        []string{},
				SessionIDsPresent: true,
			},
			want: []string{},
		},
		{
			name: "any session outside the intersection rejects the whole request",
			requested: RequestedScope{
				SessionIDs:        []string{testSessionA, "session-outside"},
				SessionIDsPresent: true,
			},
			code: CodeSessionOutOfScope,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newPolicyFixture(t, allowlist, ModeAuto)
			got, err := fixture.service.EffectiveSessions(testCase.requested)
			if testCase.code != "" {
				requireCode(t, err, testCase.code)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(testCase.want, ",") {
				t.Fatalf("effective sessions = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestEmptyAllowlistIsNoAccess(t *testing.T) {
	fixture := newPolicyFixture(t, nil, ModeAuto)
	if got, err := fixture.service.EffectiveSessions(RequestedScope{}); err != nil || len(got) != 0 {
		t.Fatalf("empty allowlist must mean no access, got %v (err %v)", got, err)
	}
	_, err := fixture.service.Authorize(context.Background(), testRequest(testTool("terminal_execute"), testSessionA))
	requireCode(t, err, CodeSessionOutOfScope)
}

func TestParseSessionAllowlist(t *testing.T) {
	got := ParseSessionAllowlist(" a, b ,, a ,c ")
	want := []string{"a", "b", "c"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("allowlist = %v, want %v", got, want)
	}
	if ids := ParseSessionAllowlist(""); len(ids) != 0 {
		t.Fatalf("empty value must parse to no sessions, got %v", ids)
	}
}

func TestRequestedScopeFromRejectsMalformedValues(t *testing.T) {
	if _, err := RequestedScopeFrom(map[string]any{"chatSessionId": 7}); err == nil {
		t.Fatal("non-string chatSessionId must be rejected")
	} else {
		requireCode(t, err, CodeScopeMismatch)
	}
	for _, value := range []any{"session-a", []any{1}, []any{nil}} {
		_, err := RequestedScopeFrom(map[string]any{"scopedSessionIds": value})
		if err == nil {
			t.Fatalf("scopedSessionIds=%#v must be rejected", value)
		}
		requireCode(t, err, CodeSessionOutOfScope)
	}
	requested, err := RequestedScopeFrom(map[string]any{
		"chatSessionId":    testForcedScope,
		"scopedSessionIds": []any{testSessionA},
	})
	if err != nil {
		t.Fatalf("valid scope rejected: %v", err)
	}
	if requested.ChatSessionID != testForcedScope || !requested.SessionIDsPresent || len(requested.SessionIDs) != 1 {
		t.Fatalf("unexpected parsed scope: %#v", requested)
	}
}

func TestScopeIsCheckedBeforeAnyApproval(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	request := testRequest(testTool("terminal_execute"), testSessionA)
	request.Scope.ChatSessionID = "chat-other"
	_, err := fixture.service.Authorize(context.Background(), request)
	typed := requireCode(t, err, CodeScopeMismatch)
	if typed.Tool != "terminal_execute" {
		t.Fatalf("scope error must name the tool, got %q", typed.Tool)
	}
	if pending := fixture.service.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("scope rejection must not create an approval, got %d", len(pending))
	}
}

func TestTargetSessionMustBeInsideEffectiveScope(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA, testSessionB}, ModeAuto)
	request := testRequest(testTool("terminal_execute"), testSessionB)
	request.Scope = RequestedScope{SessionIDs: []string{testSessionA}, SessionIDsPresent: true}
	_, err := fixture.service.Authorize(context.Background(), request)
	typed := requireCode(t, err, CodeSessionOutOfScope)
	if typed.Tool != "terminal_execute" {
		t.Fatalf("out-of-scope error must name the tool, got %q", typed.Tool)
	}

	read := testRequest(Tool{Name: "vault_hosts_list"}, testSessionB)
	read.Scope = request.Scope
	_, err = fixture.service.Authorize(context.Background(), read)
	requireCode(t, err, CodeSessionOutOfScope)
}

func TestObserverModeDeniesEveryWriteAndAllowsReads(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeObserver)
	for _, name := range []string{"terminal_execute", "sftp_write", "vault_hosts_delete", "port_forward_start"} {
		_, err := fixture.service.Authorize(context.Background(), testRequest(testTool(name), testSessionA))
		typed := requireCode(t, err, CodePermissionDenied)
		if typed.Tool != name {
			t.Fatalf("observer denial must name %q, got %q", name, typed.Tool)
		}
	}
	decision, err := fixture.service.Authorize(context.Background(), testRequest(Tool{Name: "vault_hosts_list"}, testSessionA))
	if err != nil {
		t.Fatalf("read denied in observer mode: %v", err)
	}
	if decision.Mode != ModeObserver || decision.Approved {
		t.Fatalf("unexpected read decision: %#v", decision)
	}
}

func TestAutoModeAllowsWritesWithoutApproval(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeAuto)
	decision, err := fixture.service.Authorize(context.Background(), testRequest(testTool("terminal_execute"), testSessionA))
	if err != nil {
		t.Fatalf("auto mode denied a write: %v", err)
	}
	if decision.Approved || decision.ApprovalID != "" {
		t.Fatalf("auto mode must not create an approval: %#v", decision)
	}
	if pending := fixture.service.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("auto mode must not prompt, got %d pending", len(pending))
	}
}

func TestConfirmModeRequiresOneApprovalPerWrite(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	if pending.Client != "netcatty-remote-hosts/1.0.0" || pending.Tool != "terminal_execute" {
		t.Fatalf("prompt must show client and tool: %#v", pending)
	}
	if pending.Target != testSessionA || pending.Scope != testForcedScope || pending.SessionID != testSessionA {
		t.Fatalf("prompt must show target and scope: %#v", pending)
	}
	if pending.Status != ApprovalPending || pending.Expired {
		t.Fatalf("unexpected initial prompt state: %#v", pending)
	}
	if pending.Remaining != DefaultApprovalTimeout {
		t.Fatalf("initial remaining = %v, want %v", pending.Remaining, DefaultApprovalTimeout)
	}
	if err := fixture.service.Approve(pending.ID); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	result := awaitResult(t, done)
	if result.err != nil {
		t.Fatalf("approved write failed: %v", result.err)
	}
	if !result.decision.Approved || result.decision.ApprovalID != pending.ID {
		t.Fatalf("decision must record the consumed approval: %#v", result.decision)
	}

	// The approval is single-use: another write needs a fresh prompt.
	second := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	awaitPending(t, fixture)
	select {
	case result := <-second:
		t.Fatalf("second write must wait for a new approval, returned %#v", result)
	default:
	}
}

func TestConfirmModeDenyResolvesTheCall(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	if err := fixture.service.Deny(pending.ID); err != nil {
		t.Fatalf("deny failed: %v", err)
	}
	result := awaitResult(t, done)
	typed := requireCode(t, result.err, CodeApprovalDenied)
	if !strings.Contains(typed.Message, "denied by user") {
		t.Fatalf("deny message = %q", typed.Message)
	}
	if typed.Tool != "terminal_execute" {
		t.Fatalf("deny error must name the tool, got %q", typed.Tool)
	}
}

func TestApprovalBoundary109Point9SecondsAllowed(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)

	fixture.clock.Advance(109*time.Second + 900*time.Millisecond)
	fixture.service.Sweep()
	views := fixture.service.Snapshot().Pending
	if len(views) != 1 {
		t.Fatalf("approval at 109.9 s must still be pending, got %#v", views)
	}
	if views[0].Expired || views[0].Remaining != 100*time.Millisecond {
		t.Fatalf("remaining at 109.9 s = %v (expired=%v), want 100ms", views[0].Remaining, views[0].Expired)
	}
	if err := fixture.service.Approve(pending.ID); err != nil {
		t.Fatalf("approval at 109.9 s must be accepted: %v", err)
	}
	result := awaitResult(t, done)
	if result.err != nil || !result.decision.Approved {
		t.Fatalf("approved call at 109.9 s failed: %#v", result)
	}
}

func TestApprovalBoundary110SecondsExpired(t *testing.T) {
	t.Run("approve at exactly the deadline expires", func(t *testing.T) {
		fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
		done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
		pending := awaitPending(t, fixture)
		fixture.clock.Advance(DefaultApprovalTimeout)
		err := fixture.service.Approve(pending.ID)
		requireCode(t, err, CodeApprovalExpired)
		result := awaitResult(t, done)
		requireCode(t, result.err, CodeApprovalExpired)
	})

	t.Run("sweep at exactly the deadline expires", func(t *testing.T) {
		fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
		done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
		pending := awaitPending(t, fixture)
		fixture.clock.Advance(DefaultApprovalTimeout)
		fixture.service.Sweep()
		if pendingNow := fixture.service.Snapshot().Pending; len(pendingNow) != 0 {
			t.Fatalf("approval must be expired at 110.0 s, got %#v", pendingNow)
		}
		err := fixture.service.Approve(pending.ID)
		requireCode(t, err, CodeApprovalExpired)
		result := awaitResult(t, done)
		requireCode(t, result.err, CodeApprovalExpired)
	})
}

func TestSnapshotExpiresAtTheAbsoluteDeadline(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)

	// No Sweep call here: the UI's read model must reconcile the deadline itself
	// so it can never offer Approve/Deny or a live countdown after 110.0 s.
	fixture.clock.Advance(DefaultApprovalTimeout)
	snapshot := fixture.service.Snapshot()
	if len(snapshot.Pending) != 0 {
		t.Fatalf("snapshot at 110.0 s still reports pending prompts: %#v", snapshot.Pending)
	}
	if len(snapshot.Recent) != 1 || snapshot.Recent[0].ID != pending.ID ||
		snapshot.Recent[0].Status != ApprovalExpired || !snapshot.Recent[0].Expired ||
		snapshot.Recent[0].Remaining != 0 {
		t.Fatalf("snapshot expired prompt = %#v", snapshot.Recent)
	}
	result := awaitResult(t, done)
	requireCode(t, result.err, CodeApprovalExpired)
}

func TestExpiredApprovalCannotBeReplayed(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	fixture.clock.Advance(DefaultApprovalTimeout)
	fixture.service.Sweep()
	awaitResult(t, done)

	requireCode(t, fixture.service.Approve(pending.ID), CodeApprovalExpired)
	requireCode(t, fixture.service.Deny(pending.ID), CodeApprovalInvalid)

	recent := fixture.service.Snapshot().Recent
	if len(recent) != 1 || recent[0].ID != pending.ID || recent[0].Status != ApprovalExpired || !recent[0].Expired {
		t.Fatalf("expired prompt must stay visible: %#v", recent)
	}
	fixture.service.Dismiss(pending.ID)
	if recent := fixture.service.Snapshot().Recent; len(recent) != 0 {
		t.Fatalf("dismiss must remove the expired prompt, got %#v", recent)
	}
}

func TestApprovedApprovalCannotBeReplayed(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	if err := fixture.service.Approve(pending.ID); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	awaitResult(t, done)
	requireCode(t, fixture.service.Approve(pending.ID), CodeApprovalInvalid)
	requireCode(t, fixture.service.Deny(pending.ID), CodeApprovalInvalid)
}

func TestApprovalTimeoutIsAbsoluteNotSliding(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	fixture.clock.Advance(60 * time.Second)
	if views := fixture.service.Snapshot().Pending; views[0].Remaining != 50*time.Second {
		t.Fatalf("remaining after 60 s = %v, want 50s", views[0].Remaining)
	}
	if err := fixture.service.Approve(pending.ID); err != nil {
		t.Fatalf("approve at 60 s failed: %v", err)
	}
	awaitResult(t, done)
}

func TestModeChangeAppliesToTheNextCallAndCannotBeBypassed(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeAuto)
	request := testRequest(testTool("terminal_execute"), testSessionA)
	if _, err := fixture.service.Authorize(context.Background(), request); err != nil {
		t.Fatalf("auto mode denied the write: %v", err)
	}

	fixture.mode.Set(ModeObserver)
	for _, tool := range []string{"terminal_execute", "sftp_write"} {
		for attempt := 0; attempt < 2; attempt++ {
			_, err := fixture.service.Authorize(context.Background(), testRequest(testTool(tool), testSessionA))
			requireCode(t, err, CodePermissionDenied)
		}
	}

	fixture.mode.Set(ModeConfirm)
	done := startAuthorize(fixture.service, request)
	pending := awaitPending(t, fixture)
	if err := fixture.service.Approve(pending.ID); err != nil {
		t.Fatalf("approve after mode switch failed: %v", err)
	}
	if result := awaitResult(t, done); result.err != nil {
		t.Fatalf("confirm-mode write failed: %v", result.err)
	}
}

func TestBypassToolsSkipObserverBlockAndApprovalButStillNeedScope(t *testing.T) {
	bypass := Tool{Name: "session_close", Write: true, BypassesApproval: true, BypassesObserverBlock: true}
	for _, mode := range []Mode{ModeObserver, ModeConfirm, ModeAuto} {
		fixture := newPolicyFixture(t, []string{testSessionA}, mode)
		decision, err := fixture.service.Authorize(context.Background(), testRequest(bypass, testSessionA))
		if err != nil {
			t.Fatalf("%s: bypass tool denied: %v", mode, err)
		}
		if pending := fixture.service.Snapshot().Pending; len(pending) != 0 {
			t.Fatalf("%s: bypass tool must not prompt", mode)
		}
		if decision.Mode != mode || decision.Approved {
			t.Fatalf("%s: unexpected decision %#v", mode, decision)
		}

		outOfScope := testRequest(bypass, testSessionB)
		_, err = fixture.service.Authorize(context.Background(), outOfScope)
		requireCode(t, err, CodeSessionOutOfScope)
	}
}

func TestApprovalPromptRedactsSecrets(t *testing.T) {
	summary := SummarizeArguments(map[string]any{
		"password":         "hunter2",
		"privateKey":       "-----BEGIN KEY-----",
		"fileContent":      "hello",
		"command":          "rm -rf /",
		"chatSessionId":    "scope",
		"_mcpClient":       "client",
		"scopedSessionIds": []any{"session-a"},
		"count":            float64(3),
	})
	for _, leaked := range []string{"hunter2", "BEGIN KEY", "hello", "scope", "client", "session-a"} {
		if strings.Contains(summary, leaked) {
			t.Fatalf("summary leaked %q: %s", leaked, summary)
		}
	}
	for _, expected := range []string{"password=[redacted", "privateKey=[redacted", "fileContent=[redacted", "command=\"rm -rf /\"", "count=\"3\""} {
		if !strings.Contains(summary, expected) {
			t.Fatalf("summary missing %q: %s", expected, summary)
		}
	}
	long := SummarizeArguments(map[string]any{"command": strings.Repeat("x", 1000)})
	if len([]rune(long)) > approvalSummaryLimit+1 {
		t.Fatalf("summary not bounded: %d runes", len([]rune(long)))
	}
	if got := SummarizeArguments(nil); got != "(no arguments)" {
		t.Fatalf("empty summary = %q", got)
	}
}

func TestOwnershipGenerationInvalidatesGrantsAndApprovals(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	if generation := fixture.service.Generation(); generation != 1 {
		t.Fatalf("initial generation = %d, want 1", generation)
	}
	if generation := fixture.service.GrantOwnedSession(testSessionA, testHostA); generation != 1 {
		t.Fatalf("grant generation = %d, want 1", generation)
	}
	if err := fixture.service.ValidateOwnership(testSessionA); err != nil {
		t.Fatalf("fresh grant rejected: %v", err)
	}

	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	if generation := fixture.service.InvalidateOwnership(); generation != 2 {
		t.Fatalf("new generation = %d, want 2", generation)
	}
	result := awaitResult(t, done)
	requireCode(t, result.err, CodeStaleGeneration)
	requireCode(t, fixture.service.ValidateOwnership(testSessionA), CodeStaleGeneration)
	requireCode(t, fixture.service.Approve(pending.ID), CodeStaleGeneration)

	console := testRequest(Tool{Name: "session_close", Write: true, BypassesApproval: true, BypassesObserverBlock: true}, testSessionA)
	if _, err := fixture.service.Authorize(context.Background(), console); err != nil {
		t.Fatalf("bypass tool still obeys scope, not ownership in Authorize: %v", err)
	}

	// A fresh generation requires a fresh ownership grant.
	if generation := fixture.service.GrantOwnedSession(testSessionA, testHostA); generation != 2 {
		t.Fatalf("fresh grant generation = %d, want 2", generation)
	}
	if err := fixture.service.ValidateOwnership(testSessionA); err != nil {
		t.Fatalf("fresh grant after generation bump rejected: %v", err)
	}
	fixture.service.ReleaseOwnedSession(testSessionA)
	requireCode(t, fixture.service.ValidateOwnership(testSessionA), CodeSessionNotOwned)
}

func TestObserveSessionsBumpsGenerationOnReplacement(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA, "session-new"}, ModeConfirm)
	fixture.service.GrantOwnedSession(testSessionA, testHostA)
	fixture.service.GrantOwnedSession("session-other", "host-other")

	fixture.service.ObserveSessions([]SessionRef{
		{ID: testSessionA, HostID: testHostA},
		{ID: "session-other", HostID: "host-other"},
	})
	if generation := fixture.service.Generation(); generation != 1 {
		t.Fatalf("unchanged sessions bumped generation to %d", generation)
	}

	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	fixture.service.ObserveSessions([]SessionRef{
		{ID: "session-new", HostID: testHostA},
		{ID: "session-other", HostID: "host-other"},
	})
	if generation := fixture.service.Generation(); generation != 2 {
		t.Fatalf("replacement generation = %d, want 2", generation)
	}
	requireCode(t, awaitResult(t, done).err, CodeStaleGeneration)
	requireCode(t, fixture.service.Approve(pending.ID), CodeStaleGeneration)
	requireCode(t, fixture.service.ValidateOwnership(testSessionA), CodeStaleGeneration)
	// A replacement advances the scope-wide generation: every prior grant,
	// including one for another still-live Host, must be re-adopted.
	requireCode(t, fixture.service.ValidateOwnership("session-other"), CodeStaleGeneration)
	if generation := fixture.service.GrantOwnedSession("session-other", "host-other"); generation != 2 {
		t.Fatalf("re-adopted grant generation = %d, want 2", generation)
	}
	if err := fixture.service.ValidateOwnership("session-other"); err != nil {
		t.Fatalf("re-adopted session rejected: %v", err)
	}
}

func TestObserveSessionsDropsClosedGrantWithoutBumpingGeneration(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeAuto)
	fixture.service.GrantOwnedSession(testSessionA, testHostA)
	fixture.service.ObserveSessions(nil)
	if generation := fixture.service.Generation(); generation != 1 {
		t.Fatalf("a plain close bumped generation to %d", generation)
	}
	requireCode(t, fixture.service.ValidateOwnership(testSessionA), CodeSessionNotOwned)
}

func TestSnapshotReportsModeScopeAllowlistAndGeneration(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA, testSessionB}, ModeObserver)
	snapshot := fixture.service.Snapshot()
	if snapshot.Mode != ModeObserver || snapshot.ForcedScope != testForcedScope {
		t.Fatalf("unexpected snapshot header: %#v", snapshot)
	}
	if strings.Join(snapshot.Allowlist, ",") != testSessionA+","+testSessionB || snapshot.Generation != 1 {
		t.Fatalf("unexpected snapshot scope: %#v", snapshot)
	}

	fixture.mode.Set(ModeConfirm)
	done := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	pending := awaitPending(t, fixture)
	if snapshot := fixture.service.Snapshot(); snapshot.Mode != ModeConfirm || len(snapshot.Pending) != 1 {
		t.Fatalf("snapshot must re-read the mode and show the prompt: %#v", snapshot)
	}
	if err := fixture.service.Approve(pending.ID); err != nil {
		t.Fatalf("approve failed: %v", err)
	}
	awaitResult(t, done)
	if snapshot := fixture.service.Snapshot(); len(snapshot.Pending) != 0 {
		t.Fatalf("consumed prompt must leave the pending list: %#v", snapshot.Pending)
	}
}

func TestCancelledApprovalReleasesTheCall(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA}, ModeConfirm)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan authorizeResult, 1)
	go func() {
		decision, err := fixture.service.Authorize(ctx, testRequest(testTool("terminal_execute"), testSessionA))
		done <- authorizeResult{decision: decision, err: err}
	}()
	pending := awaitPending(t, fixture)
	cancel()
	result := awaitResult(t, done)
	requireCode(t, result.err, CodeApprovalDenied)
	requireCode(t, fixture.service.Approve(pending.ID), CodeApprovalInvalid)
}

func TestParseMode(t *testing.T) {
	for _, mode := range []Mode{ModeObserver, ModeConfirm, ModeAuto} {
		parsed, ok := ParseMode(string(mode))
		if !ok || parsed != mode {
			t.Fatalf("ParseMode(%q) = %q, %v", mode, parsed, ok)
		}
	}
	if _, ok := ParseMode("nonsense"); ok {
		t.Fatal("unknown mode must not parse")
	}
	if NormalizeMode("nonsense") != DefaultMode {
		t.Fatalf("unknown mode must normalize to %q", DefaultMode)
	}
	if NormalizeMode(" AUTO ") != ModeAuto {
		t.Fatal("mode parsing must ignore case and spaces")
	}
}

func TestConcurrentApprovalsResolveIndependently(t *testing.T) {
	fixture := newPolicyFixture(t, []string{testSessionA, testSessionB}, ModeConfirm)
	first := startAuthorize(fixture.service, testRequest(testTool("terminal_execute"), testSessionA))
	second := startAuthorize(fixture.service, testRequest(testTool("sftp_write"), testSessionB))
	timeout := time.After(10 * time.Second)
	var pending []ApprovalView
	for {
		pending = fixture.service.Snapshot().Pending
		if len(pending) >= 2 {
			break
		}
		select {
		case <-fixture.signals:
		case <-timeout:
			t.Fatalf("expected two prompts, got %#v", pending)
		}
	}
	if err := fixture.service.Deny(pending[0].ID); err != nil {
		t.Fatalf("deny first: %v", err)
	}
	if err := fixture.service.Approve(pending[1].ID); err != nil {
		t.Fatalf("approve second: %v", err)
	}
	results := []authorizeResult{awaitResult(t, first), awaitResult(t, second)}
	approved, denied := 0, 0
	for _, result := range results {
		if result.err == nil && result.decision.Approved {
			approved++
			continue
		}
		var typed *Error
		if errors.As(result.err, &typed) && typed.Code == CodeApprovalDenied {
			denied++
		}
	}
	if approved != 1 || denied != 1 {
		t.Fatalf("concurrent approvals resolved incorrectly: %#v", results)
	}
}
