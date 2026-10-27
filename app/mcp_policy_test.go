package app

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

type policyClock struct {
	mu  sync.Mutex
	now time.Time
}

func newPolicyClock() *policyClock {
	return &policyClock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *policyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *policyClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

type policyMode struct {
	mu   sync.Mutex
	mode appmcp.Mode
}

func newPolicyMode(mode appmcp.Mode) *policyMode { return &policyMode{mode: mode} }

func (m *policyMode) Get() appmcp.Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

func (m *policyMode) Set(mode appmcp.Mode) {
	m.mu.Lock()
	m.mode = mode
	m.mu.Unlock()
}

type policyFixture struct {
	handler *mcpPolicyHandler
	policy  *appmcp.Service
	clock   *policyClock
	mode    *policyMode
	signals chan struct{}
}

// closingSessions implements the handler's session closer and records calls.
type closingSessions struct {
	mu     sync.Mutex
	closed []string
}

func (c *closingSessions) Close(id model.SessionID) (bool, error) {
	c.mu.Lock()
	c.closed = append(c.closed, string(id))
	c.mu.Unlock()
	return true, nil
}

func newAppPolicyFixture(t *testing.T, allowlist []string, mode appmcp.Mode,
	sessions fakeSessions, vault fakeVault, closer mcpSessionCloser) *policyFixture {
	t.Helper()
	return newAppPolicyFixtureWithForwarding(t, allowlist, mode, sessions, vault,
		fakeForwarding{}, closer)
}

func newAppPolicyFixtureWithForwarding(t *testing.T, allowlist []string, mode appmcp.Mode,
	sessions fakeSessions, vault fakeVault, forwarding mcpForwardingReader,
	closer mcpSessionCloser) *policyFixture {
	t.Helper()
	clock := newPolicyClock()
	modeState := newPolicyMode(mode)
	signals := make(chan struct{}, 64)
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: "__external_mcp__",
		Allowlist:           allowlist,
		Mode:                modeState.Get,
		Now:                 clock.Now,
		Schedule:            func(time.Duration, func()) func() { return func() {} },
		Redraw: func() {
			select {
			case signals <- struct{}{}:
			default:
			}
		},
	})
	bridge := newMCPReadBridge(vault, forwarding, sessions, fakeTransfer{})
	return &policyFixture{
		handler: newMCPPolicyHandler(bridge, policy, sessions, closer, nil),
		policy:  policy,
		clock:   clock,
		mode:    modeState,
		signals: signals,
	}
}

type handlerResult struct {
	value any
	err   error
}

func callPolicy(t *testing.T, fixture *policyFixture, method string, params map[string]any) map[string]any {
	t.Helper()
	value, err := fixture.handler.Handle(context.Background(), method, mustJSON(t, params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s returned %#v", method, value)
	}
	return result
}

func startPolicyCall(fixture *policyFixture, method string, params map[string]any) <-chan handlerResult {
	done := make(chan handlerResult, 1)
	payload, _ := json.Marshal(params)
	go func() {
		value, err := fixture.handler.Handle(context.Background(), method, payload)
		done <- handlerResult{value: value, err: err}
	}()
	return done
}

func awaitHandler(t *testing.T, done <-chan handlerResult) map[string]any {
	t.Helper()
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("handler: %v", result.err)
		}
		mapped, ok := result.value.(map[string]any)
		if !ok {
			t.Fatalf("handler returned %#v", result.value)
		}
		return mapped
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not return")
		return nil
	}
}

func awaitPolicyPrompt(t *testing.T, fixture *policyFixture) appmcp.ApprovalView {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		if pending := fixture.policy.Snapshot().Pending; len(pending) > 0 {
			return pending[0]
		}
		select {
		case <-fixture.signals:
		case <-timeout:
			t.Fatal("approval prompt did not appear")
			return appmcp.ApprovalView{}
		}
	}
}

func requireHandlerCode(t *testing.T, result map[string]any, code appmcp.ErrorCode) {
	t.Helper()
	if result["ok"] != false {
		t.Fatalf("expected failure, got %v", result)
	}
	if result["code"] != string(code) {
		t.Fatalf("expected code %s, got %v (%v)", code, result["code"], result["error"])
	}
}

func mustJSON(t *testing.T, params map[string]any) json.RawMessage {
	t.Helper()
	if params == nil {
		return json.RawMessage("{}")
	}
	payload, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("encode params: %v", err)
	}
	return payload
}

func policyTestSessions() fakeSessions {
	return fakeSessions{
		descriptions: []appsession.SessionDescription{
			{
				ID: "s1", Label: "web shell", HostID: "h1", Kind: appsession.KindSSH,
				Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive,
				Hostname: "web.example", Username: "deploy", Connected: true,
			},
			{
				ID: "s2", Label: "db shell", HostID: "h2", Kind: appsession.KindSSH,
				Status: appsession.StatusRunning, Connection: appsession.ConnectionStatusActive,
				Hostname: "db.example", Username: "deploy", Connected: true,
			},
		},
		screens: map[model.SessionID]appsession.ScreenTextSnapshot{
			"s1": {SessionID: "s1", Text: "web output", Cols: 80, Rows: 24},
			"s2": {SessionID: "s2", Text: "db output", Cols: 80, Rows: 24},
		},
	}
}

func policyTestVault() fakeVault {
	return fakeVault{hosts: []domainvault.Host{
		{ID: "h1", Label: "web", Hostname: "web.example", Protocol: domainvault.ProtocolSSH},
		{ID: "h2", Label: "db", Hostname: "db.example", Protocol: domainvault.ProtocolSSH},
	}}
}

// policyTestJobs is the ticket-58 job→Session seam. The real build has no
// background jobs yet, so tests supply the mapping to prove terminal_stop
// rejects an out-of-scope or stale Session for the catalog's actual jobId.
type policyTestJobs struct{}

func (policyTestJobs) SessionForJob(jobID string) (model.SessionID, bool) {
	switch jobID {
	case "j1":
		return "s1", true
	case "j2":
		return "s2", true
	default:
		return "", false
	}
}

func TestMCPPolicyScopeFiltersEnvironmentAndRejectsOutsideScope(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), nil)

	environment := callPolicy(t, fixture, "netcatty/getContext", nil)
	hosts, _ := environment["hosts"].([]any)
	if len(hosts) != 1 || hosts[0].(map[string]any)["sessionId"] != "s1" {
		t.Fatalf("getContext must list only scoped sessions: %#v", environment["hosts"])
	}

	result := callPolicy(t, fixture, "public/vault/hosts/list", map[string]any{
		"chatSessionId": "chat-other",
	})
	requireHandlerCode(t, result, appmcp.CodeScopeMismatch)
	if result["tool"] != "vault_hosts_list" {
		t.Fatalf("scope denial must name the tool: %v", result)
	}

	result = callPolicy(t, fixture, "public/vault/hosts/list", map[string]any{
		"scopedSessionIds": []any{"s2"},
	})
	requireHandlerCode(t, result, appmcp.CodeSessionOutOfScope)
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("scope rejection created a prompt: %#v", pending)
	}

	result = callPolicy(t, fixture, "netcatty/getContext", map[string]any{
		"scopedSessionIds": []any{},
	})
	if hosts, _ := result["hosts"].([]any); len(hosts) != 0 {
		t.Fatalf("empty explicit scope must expose no hosts: %#v", hosts)
	}
}

func TestMCPPolicyEnvironmentFiltersTunnelsToScopedHosts(t *testing.T) {
	forwarding := fakeForwarding{views: []appforwarding.View{
		{Rule: domainforward.Rule{ID: "r1", HostID: "h1", Type: domainforward.TypeLocal},
			Status: domainforward.StatusActive},
		{Rule: domainforward.Rule{ID: "r2", HostID: "h2", Type: domainforward.TypeLocal},
			Status: domainforward.StatusActive},
	}}
	fixture := newAppPolicyFixtureWithForwarding(t, []string{"s1"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), forwarding, nil)

	environment := callPolicy(t, fixture, "netcatty/getContext", nil)
	tunnels, _ := environment["activePortForwardTunnels"].([]any)
	if len(tunnels) != 1 {
		t.Fatalf("tunnels = %#v, want only the scoped Host's tunnel", tunnels)
	}
	tunnel, _ := tunnels[0].(map[string]any)
	if tunnel["id"] != "r1" || tunnel["hostId"] != "h1" {
		t.Fatalf("tunnel = %#v, want r1 on h1", tunnel)
	}
}

func TestMCPPolicyReadContextUsesTheEffectiveScope(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1", "s2"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), nil)
	result := callPolicy(t, fixture, "netcatty/readContext", nil)
	if result["ok"] != false || !strings.Contains(result["error"].(string), "sessionId is required") {
		t.Fatalf("two scoped sessions cannot be implicit: %v", result)
	}

	fixture = newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), nil)
	result = callPolicy(t, fixture, "netcatty/readContext", nil)
	if result["ok"] != true || result["sessionId"] != "s1" ||
		!strings.Contains(result["content"].(string), "web output") {
		t.Fatalf("implicit readContext must use the one scoped session: %v", result)
	}

	result = callPolicy(t, fixture, "netcatty/readContext", map[string]any{"sessionId": "s2"})
	requireHandlerCode(t, result, appmcp.CodeSessionOutOfScope)
}

func writeTools(t *testing.T) []catalog.Tool {
	t.Helper()
	writes := make([]catalog.Tool, 0, 43)
	for _, tool := range catalog.All() {
		if tool.Policy.Write {
			writes = append(writes, tool)
		}
	}
	if len(writes) != 43 {
		t.Fatalf("catalog writes = %d, want 43", len(writes))
	}
	return writes
}

func policyArgsFor(t *testing.T, tool catalog.Tool) map[string]any {
	switch tool.Name {
	case "host_open":
		return map[string]any{"hostId": "h1"}
	case "terminal_stop":
		// The catalog's real input is jobId; a sessionId here would be a
		// test-only shape the stdio server can never send.
		return map[string]any{"jobId": "j1"}
	case "session_close", "terminal_execute", "terminal_start",
		"terminal_poll", "terminal_read_context":
		return map[string]any{"sessionId": "s1"}
	default:
		return nil
	}
}

func TestMCPPolicyObserverDeniesEveryWriteExceptTheBypassSet(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeObserver,
		policyTestSessions(), policyTestVault(), nil)
	for _, tool := range writeTools(t) {
		result := callPolicy(t, fixture, tool.RPCMethod, policyArgsFor(t, tool))
		if tool.Policy.BypassesObserverBlock {
			if result["code"] == string(appmcp.CodePermissionDenied) {
				t.Fatalf("%s must bypass the observer block: %v", tool.Name, result)
			}
			continue
		}
		requireHandlerCode(t, result, appmcp.CodePermissionDenied)
		if result["tool"] != tool.Name {
			t.Fatalf("%s denial names %v", tool.Name, result["tool"])
		}
	}
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("observer mode created prompts: %#v", pending)
	}
}

func TestMCPPolicyConfirmRequiresApprovalForEveryNonBypassWrite(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	required := 0
	for _, tool := range writeTools(t) {
		done := startPolicyCall(fixture, tool.RPCMethod, policyArgsFor(t, tool))
		if tool.Policy.BypassesApproval {
			result := awaitHandler(t, done)
			if result["code"] == string(appmcp.CodeApprovalDenied) ||
				result["code"] == string(appmcp.CodeApprovalExpired) {
				t.Fatalf("%s must bypass approval: %v", tool.Name, result)
			}
			if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
				t.Fatalf("%s created a prompt despite bypassing approval", tool.Name)
			}
			continue
		}
		required++
		prompt := awaitPolicyPrompt(t, fixture)
		if prompt.Tool != tool.Name {
			t.Fatalf("prompt tool = %q, want %q", prompt.Tool, tool.Name)
		}
		if prompt.Client != "" {
			// No identity was forwarded; the Settings surface localizes the
			// fallback rather than receiving an English application string.
			t.Fatalf("prompt client = %q, want empty", prompt.Client)
		}
		if err := fixture.policy.Deny(prompt.ID); err != nil {
			t.Fatalf("deny %s: %v", tool.Name, err)
		}
		result := awaitHandler(t, done)
		requireHandlerCode(t, result, appmcp.CodeApprovalDenied)
		if result["tool"] != tool.Name {
			t.Fatalf("%s denial names %v", tool.Name, result["tool"])
		}
	}
	if required != 41 {
		t.Fatalf("approval-required writes = %d, want 41", required)
	}
}

func TestMCPPolicyAutoAllowsEveryWriteWithoutPrompt(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	for _, tool := range writeTools(t) {
		result := callPolicy(t, fixture, tool.RPCMethod, policyArgsFor(t, tool))
		switch result["code"] {
		case string(appmcp.CodePermissionDenied), string(appmcp.CodeApprovalDenied),
			string(appmcp.CodeApprovalExpired), string(appmcp.CodeStaleGeneration):
			t.Fatalf("auto mode blocked %s: %v", tool.Name, result)
		}
		if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
			t.Fatalf("auto mode prompted for %s: %#v", tool.Name, pending)
		}
	}
}

func TestMCPBypassSetIsExactlySessionCloseAndTerminalStop(t *testing.T) {
	var bypass []string
	for _, tool := range catalog.All() {
		if tool.Policy.Write && tool.Policy.BypassesApproval && tool.Policy.BypassesObserverBlock {
			bypass = append(bypass, tool.Name)
		}
	}
	sort.Strings(bypass)
	if strings.Join(bypass, ",") != "session_close,terminal_stop" {
		t.Fatalf("bypass set = %v, want exactly session_close and terminal_stop", bypass)
	}
	for _, tool := range catalog.All() {
		if !tool.Policy.Write || !tool.Policy.BypassesApproval {
			continue
		}
		if !tool.Policy.BypassesObserverBlock {
			t.Fatalf("%s bypasses approval but not the observer block", tool.Name)
		}
	}
}

func TestMCPPolicyHostOpenAdoptsAndSessionCloseBypassesApproval(t *testing.T) {
	closer := &closingSessions{}
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
		policyTestSessions(), policyTestVault(), closer)

	done := startPolicyCall(fixture, "public/vault/hosts/open", map[string]any{"hostId": "h1"})
	prompt := awaitPolicyPrompt(t, fixture)
	if prompt.Tool != "host_open" || !strings.Contains(prompt.Target, "web") {
		t.Fatalf("host_open prompt = %#v", prompt)
	}
	if err := fixture.policy.Approve(prompt.ID); err != nil {
		t.Fatalf("approve host_open: %v", err)
	}
	opened := awaitHandler(t, done)
	// Write execution is not part of this ticket; the approval is still proven
	// by the prompt and the adopted ownership below.
	if opened["code"] == string(appmcp.CodeApprovalDenied) {
		t.Fatalf("approved host_open was denied: %v", opened)
	}

	// For the ownership path, grant directly at the current generation.
	generation := fixture.policy.GrantOwnedSession("s1", "h1")
	if generation != 1 {
		t.Fatalf("grant generation = %d", generation)
	}
	closed := callPolicy(t, fixture, "public/session/close", map[string]any{"sessionId": "s1"})
	if closed["ok"] != true || closed["status"] != "closed" {
		t.Fatalf("session_close result = %v", closed)
	}
	if closed["sessionId"] != "s1" {
		t.Fatalf("session_close sessionId = %v", closed["sessionId"])
	}
	closer.mu.Lock()
	recorded := append([]string(nil), closer.closed...)
	closer.mu.Unlock()
	if len(recorded) != 1 || recorded[0] != "s1" {
		t.Fatalf("closer calls = %v", recorded)
	}
	requireHandlerCode(t, callPolicy(t, fixture, "public/session/close",
		map[string]any{"sessionId": "s1"}), appmcp.CodeSessionNotOwned)
}

func TestMCPPolicyCloseRejectsStaleGenerationAndRequiresFreshGrant(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	fixture.policy.GrantOwnedSession("s1", "h1")
	fixture.policy.InvalidateOwnership()
	requireHandlerCode(t, callPolicy(t, fixture, "public/session/close",
		map[string]any{"sessionId": "s1"}), appmcp.CodeStaleGeneration)
	requireHandlerCode(t, callPolicy(t, fixture, "public/session/close",
		map[string]any{"sessionId": "s1"}), appmcp.CodeStaleGeneration)

	fixture.policy.GrantOwnedSession("s1", "h1")
	closed := callPolicy(t, fixture, "public/session/close", map[string]any{"sessionId": "s1"})
	if closed["ok"] != true {
		t.Fatalf("fresh generation close failed: %v", closed)
	}
}

func TestMCPPolicyPendingApprovalIsInvalidatedBySessionReplacement(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	fixture.policy.GrantOwnedSession("s1", "h1")
	done := startPolicyCall(fixture, "netcatty/exec", map[string]any{
		"sessionId": "s1", "command": "rm -rf /tmp/x",
	})
	prompt := awaitPolicyPrompt(t, fixture)
	if !strings.Contains(prompt.Summary, "command=") || !strings.Contains(prompt.Target, "web shell") {
		t.Fatalf("prompt summary/target = %#v", prompt)
	}

	fixture.policy.ObserveSessions([]appmcp.SessionRef{
		{ID: "s1-replacement", HostID: "h1"},
	})
	result := awaitHandler(t, done)
	requireHandlerCode(t, result, appmcp.CodeStaleGeneration)
	if err := fixture.policy.Approve(prompt.ID); !isStaleError(err) {
		t.Fatalf("stale approval replay = %v", err)
	}
	if fixture.policy.Generation() != 2 {
		t.Fatalf("generation = %d, want 2", fixture.policy.Generation())
	}
}

func isStaleError(err error) bool {
	var typed *appmcp.Error
	return errors.As(err, &typed) && typed.Code == appmcp.CodeStaleGeneration
}

func TestMCPPolicyTerminalStopBypassesApprovalButObeysScopeAndGeneration(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	fixture.handler.jobs = policyTestJobs{}

	// A real jobId (not the placeholder sessionId the ticket-56 bridge ignores)
	// resolves to an in-scope Session; the bypass reaches the handler without a
	// prompt, and this build honestly has no job to stop.
	result := callPolicy(t, fixture, "netcatty/jobStop", map[string]any{"jobId": "j1"})
	if result["ok"] != false || !strings.Contains(result["error"].(string), "Background job not found") {
		t.Fatalf("terminal_stop result = %v", result)
	}
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("terminal_stop prompted: %#v", pending)
	}

	// The job's Session is outside the allowlist/intersection.
	requireHandlerCode(t, callPolicy(t, fixture, "netcatty/jobStop",
		map[string]any{"jobId": "j2"}), appmcp.CodeSessionOutOfScope)

	// A job belonging to a superseded generation is stale, even though the tool
	// bypasses approval and the observer block.
	fixture.policy.GrantOwnedSession("s1", "h1")
	fixture.policy.InvalidateOwnership()
	requireHandlerCode(t, callPolicy(t, fixture, "netcatty/jobStop",
		map[string]any{"jobId": "j1"}), appmcp.CodeStaleGeneration)

	// The fresh generation allows the bypass tool through again (and still has
	// no live job to stop in this build).
	fixture.policy.GrantOwnedSession("s1", "h1")
	result = callPolicy(t, fixture, "netcatty/jobStop", map[string]any{"jobId": "j1"})
	if result["ok"] != false || result["code"] != nil {
		t.Fatalf("fresh terminal_stop result = %v", result)
	}
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		t.Fatalf("fresh terminal_stop prompted: %#v", pending)
	}
}

func TestMCPPolicyExpiryBoundaryThroughTheHandler(t *testing.T) {
	t.Run("109.9s approval succeeds", func(t *testing.T) {
		fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
			policyTestSessions(), policyTestVault(), &closingSessions{})
		done := startPolicyCall(fixture, "netcatty/exec", map[string]any{
			"sessionId": "s1", "command": "true",
		})
		prompt := awaitPolicyPrompt(t, fixture)
		fixture.clock.Advance(109*time.Second + 900*time.Millisecond)
		fixture.policy.Sweep()
		if err := fixture.policy.Approve(prompt.ID); err != nil {
			t.Fatalf("approval at 109.9 s rejected: %v", err)
		}
		result := awaitHandler(t, done)
		if result["code"] == string(appmcp.CodeApprovalExpired) {
			t.Fatalf("call expired at 109.9 s: %v", result)
		}
	})
	t.Run("110.0s approval expires", func(t *testing.T) {
		fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
			policyTestSessions(), policyTestVault(), &closingSessions{})
		done := startPolicyCall(fixture, "netcatty/exec", map[string]any{
			"sessionId": "s1", "command": "true",
		})
		prompt := awaitPolicyPrompt(t, fixture)
		fixture.clock.Advance(appmcp.DefaultApprovalTimeout)
		fixture.policy.Sweep()
		result := awaitHandler(t, done)
		requireHandlerCode(t, result, appmcp.CodeApprovalExpired)
		if err := fixture.policy.Approve(prompt.ID); !isExpiredError(err) {
			t.Fatalf("expired approval replay = %v", err)
		}
	})
}

func isExpiredError(err error) bool {
	var typed *appmcp.Error
	return errors.As(err, &typed) && typed.Code == appmcp.CodeApprovalExpired
}

func TestMCPPolicyPromptRedactsSecrets(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeConfirm,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	done := startPolicyCall(fixture, "public/vault/hosts/update", map[string]any{
		"hostId": "h1", "password": "hunter2", "label": "renamed",
		"_mcpClient": "claude-desktop/0.9",
	})
	prompt := awaitPolicyPrompt(t, fixture)
	if strings.Contains(prompt.Summary, "hunter2") {
		t.Fatalf("prompt leaked a password: %s", prompt.Summary)
	}
	if !strings.Contains(prompt.Summary, "password=[redacted") ||
		!strings.Contains(prompt.Summary, `label="renamed"`) {
		t.Fatalf("prompt summary = %s", prompt.Summary)
	}
	if prompt.Client != "claude-desktop/0.9" {
		t.Fatalf("prompt client = %q", prompt.Client)
	}
	_ = fixture.policy.Deny(prompt.ID)
	_ = awaitHandler(t, done)
}

func TestMCPPolicyModeChangesApplyToTheNextCall(t *testing.T) {
	fixture := newAppPolicyFixture(t, []string{"s1"}, appmcp.ModeAuto,
		policyTestSessions(), policyTestVault(), &closingSessions{})
	if result := callPolicy(t, fixture, "netcatty/exec", map[string]any{
		"sessionId": "s1", "command": "true",
	}); result["code"] == string(appmcp.CodePermissionDenied) {
		t.Fatalf("auto mode denied a write: %v", result)
	}
	fixture.mode.Set(appmcp.ModeObserver)
	requireHandlerCode(t, callPolicy(t, fixture, "netcatty/exec", map[string]any{
		"sessionId": "s1", "command": "true",
	}), appmcp.CodePermissionDenied)
	requireHandlerCode(t, callPolicy(t, fixture, "netcatty/sftp/write", map[string]any{
		"sessionId": "s1", "path": "/tmp/x", "content": "data",
	}), appmcp.CodePermissionDenied)

	fixture.mode.Set(appmcp.ModeConfirm)
	done := startPolicyCall(fixture, "netcatty/exec", map[string]any{
		"sessionId": "s1", "command": "true",
	})
	prompt := awaitPolicyPrompt(t, fixture)
	if err := fixture.policy.Approve(prompt.ID); err != nil {
		t.Fatalf("approve after mode switch: %v", err)
	}
	if result := awaitHandler(t, done); result["code"] == string(appmcp.CodePermissionDenied) {
		t.Fatalf("confirm mode still denied the write: %v", result)
	}
}
