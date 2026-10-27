//go:build selftest

package app

// Ticket 57 self-test. Press F11, then 5, then 7.
//
// The check runs the real application policy service with an injected clock and
// a discovery-backed mode source:
//
//   - the forced scope intersects with the launch allowlist, an explicit empty
//     scope exposes nothing, and an out-of-scope request is rejected before a
//     prompt or an action;
//   - observer denies all 41 non-bypass writes, confirm prompts for all 41,
//     auto allows all 43 writes without a prompt, and the bypass set is exactly
//     session_close and terminal_stop;
//   - approvals are single-use, expire at 110.0 s, and still succeed at
//     109.9 s; an expired prompt cannot be replayed;
//   - an ownership replacement advances the generation, resolves a pending
//     approval as stale, rejects the old grant and both bypass actions, then a
//     fresh grant in the new generation is required;
//   - the Settings controller approves, denies, expires, and dismisses real
//     prompts, and its mode selector changes the next call immediately;
//   - a real cmd/natcatty-mcp process connects over stdio to a dedicated
//     loopback listener, is identified in the prompt, approves the first write,
//     closes an adopted Session without approval, is denied in observer and
//     allowed in auto, observes the expired prompt, and must re-open after a
//     stale generation.
//
// Every wait is a real condition (prompt snapshot, response line, process exit)
// or the scenario context's hard deadline. Nothing sleeps, and a repeated
// F11,5,7 resets all state and reruns the whole scenario.
//
// Result: %TEMP%\natcatty-selftest-57.json.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/internal/adapters/mcprpc"
	"github.com/mslxl/natcatty/internal/mcp"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
	"github.com/mslxl/natcatty/ui/selftest"
	"github.com/mslxl/natcatty/ui/settings"
)

const (
	selftest57Ticket = 57
	// selftest57Timeout bounds the policy matrix, the build, and the stdio
	// exchange. The per-wait condition deadlines are the scenario context.
	selftest57Timeout = 180 * time.Second
)

type selftest57 struct {
	started  bool
	done     bool
	deadline time.Time
	cancel   context.CancelFunc
	result   chan selftest57Result
	report   selftest57Report
	err      error
}

type selftest57Report struct {
	ScopeOK           bool
	ScopeCodes        string
	ScopeBeforePrompt bool

	ObserverWrites int
	ObserverDenied int
	ObserverBypass int
	ConfirmWrites  int
	ConfirmPrompts int
	ConfirmBypass  int
	AutoWrites     int
	AutoAllowed    int
	AutoPrompts    int
	BypassNames    string
	BypassMismatch int
	ModeSwitch     bool

	Boundary1099    bool
	Boundary1100    bool
	ExpiryReplay    bool
	ApproveSingle   bool
	DenySingle      bool
	ReadsNoPrompt   bool
	GenerationStale bool
	GenerationFresh bool
	Generation      uint64
	BypassScope     bool

	GUIApprove bool
	GUIDeny    bool
	GUIExpired bool
	GUIDismiss bool
	GUIMode    bool

	StdioClient  string
	StdioAdopt   bool
	StdioScope   bool
	StdioObserve bool
	StdioAuto    bool
	StdioExpired bool
	StdioStale   bool
	StdioFresh   bool
}

type selftest57Result struct {
	report selftest57Report
	err    error
}

func (t *selftest57) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest57Ticket,
		Name:   "External MCP scope, permission modes, generations, and approvals",
		Steps: []selftest.Step{
			{Name: "run-policy-and-stdio-matrix", Run: t.scenarioStep},
			{Name: "verify-scope-and-permission-modes", Run: t.scopeModeStep},
			{Name: "verify-approvals-and-generations", Run: t.approvalStep},
			{Name: "verify-gui-and-real-client", Run: t.stdioStep},
		},
	}
}

// scenarioStep resets at entry so a repeated F11,5,7 starts from a clean
// report, then polls the background scenario's result channel. The UI thread
// never blocks on the exchange.
func (t *selftest57) scenarioStep(*ui.Context) selftest.Outcome {
	if t.done {
		t.started = false
		t.done = false
		t.report = selftest57Report{}
		t.err = nil
		t.result = nil
	}
	if !t.started {
		t.started = true
		t.deadline = time.Now().Add(selftest57Timeout)
		ctx, cancel := context.WithTimeout(context.Background(), selftest57Timeout)
		t.cancel = cancel
		t.result = make(chan selftest57Result, 1)
		go func() {
			report, err := runSelftest57(ctx)
			t.result <- selftest57Result{report: report, err: err}
		}()
	}
	select {
	case result := <-t.result:
		t.done = true
		t.report = result.report
		t.err = result.err
		if t.cancel != nil {
			t.cancel()
		}
		if t.err != nil {
			return selftest.Failed(t.err)
		}
		return selftest.Passed(
			"scope intersection, allowlist, three permission modes, exact bypass set, 110 s approvals, stale and fresh generations, GUI decisions, and a real stdio client",
			"application/mcp + app/mcp_policy.go + internal/mcp/stdio.go + ui/settings/mcp.go")
	default:
		if !time.Now().Before(t.deadline) {
			t.done = true
			t.err = fmt.Errorf("the ticket 57 policy and stdio scenario did not finish in %s", selftest57Timeout)
			if t.cancel != nil {
				t.cancel()
			}
			return selftest.Failed(t.err)
		}
		return pending(t.deadline, "the policy, GUI, and stdio scenario")
	}
}

func (t *selftest57) scopeModeStep(*ui.Context) selftest.Outcome {
	if t.err != nil {
		return selftest.Failed(t.err)
	}
	report := t.report
	if !report.ScopeOK || !report.ScopeBeforePrompt ||
		!strings.Contains(report.ScopeCodes, "mcp_scope_mismatch") ||
		!strings.Contains(report.ScopeCodes, "mcp_session_out_of_scope") {
		return selftest.Failed(fmt.Errorf("scope intersection=%v before-prompt=%v codes=%s",
			report.ScopeOK, report.ScopeBeforePrompt, report.ScopeCodes))
	}
	if report.ObserverWrites != 43 || report.ObserverDenied != 41 || report.ObserverBypass != 2 {
		return selftest.Failed(fmt.Errorf("observer writes=%d denied=%d bypass=%d",
			report.ObserverWrites, report.ObserverDenied, report.ObserverBypass))
	}
	if report.ConfirmWrites != 43 || report.ConfirmPrompts != 41 || report.ConfirmBypass != 2 {
		return selftest.Failed(fmt.Errorf("confirm writes=%d prompts=%d bypass=%d",
			report.ConfirmWrites, report.ConfirmPrompts, report.ConfirmBypass))
	}
	if report.AutoWrites != 43 || report.AutoAllowed != 43 || report.AutoPrompts != 0 {
		return selftest.Failed(fmt.Errorf("auto writes=%d allowed=%d prompts=%d",
			report.AutoWrites, report.AutoAllowed, report.AutoPrompts))
	}
	if report.BypassNames != "session_close,terminal_stop" || report.BypassMismatch != 0 {
		return selftest.Failed(fmt.Errorf("bypass set=%q mismatched=%d",
			report.BypassNames, report.BypassMismatch))
	}
	if !report.ModeSwitch {
		return selftest.Failed(errors.New("a mode change did not apply to the next call"))
	}
	if !report.BypassScope {
		return selftest.Failed(errors.New("a bypass action escaped scope, allowlist, or generation rules"))
	}
	return selftest.Passed(
		fmt.Sprintf("observer %d/%d denied, confirm %d/%d prompted, auto %d/%d allowed, bypass=%s",
			report.ObserverDenied, report.ObserverWrites,
			report.ConfirmPrompts, report.ConfirmWrites,
			report.AutoAllowed, report.AutoWrites, report.BypassNames),
		"application/mcp Authorize + catalog policy flags + app/mcp_policy.go")
}

func (t *selftest57) approvalStep(*ui.Context) selftest.Outcome {
	if t.err != nil {
		return selftest.Failed(t.err)
	}
	report := t.report
	if !report.Boundary1099 || !report.Boundary1100 || !report.ExpiryReplay ||
		!report.ApproveSingle || !report.DenySingle || !report.ReadsNoPrompt {
		return selftest.Failed(fmt.Errorf(
			"approvals 109.9=%v 110.0=%v expiry-replay=%v approve-single=%v deny-single=%v reads-no-prompt=%v",
			report.Boundary1099, report.Boundary1100, report.ExpiryReplay,
			report.ApproveSingle, report.DenySingle, report.ReadsNoPrompt))
	}
	if !report.GenerationStale || !report.GenerationFresh || report.Generation < 3 {
		return selftest.Failed(fmt.Errorf("ownership stale=%v fresh=%v generation=%d",
			report.GenerationStale, report.GenerationFresh, report.Generation))
	}
	if !report.GUIApprove || !report.GUIDeny || !report.GUIExpired || !report.GUIDismiss || !report.GUIMode {
		return selftest.Failed(fmt.Errorf(
			"GUI approve=%v deny=%v expired=%v dismiss=%v mode=%v",
			report.GUIApprove, report.GUIDeny, report.GUIExpired, report.GUIDismiss, report.GUIMode))
	}
	return selftest.Passed(
		fmt.Sprintf("109.9 s approved, 110.0 s expired and unreplayable, single-use deny; generation=%d stale then fresh; GUI approve/deny/expire/dismiss",
			report.Generation),
		"application/mcp approvals + ownership + ui/settings MCPState")
}

func (t *selftest57) stdioStep(*ui.Context) selftest.Outcome {
	if t.err != nil {
		return selftest.Failed(t.err)
	}
	report := t.report
	if report.StdioClient != "selftest-57/1.0" ||
		!report.StdioAdopt || !report.StdioScope || !report.StdioObserve ||
		!report.StdioAuto || !report.StdioExpired || !report.StdioStale || !report.StdioFresh {
		return selftest.Failed(fmt.Errorf(
			"stdio client=%q adopt=%v scope=%v observer=%v auto=%v expired=%v stale=%v fresh=%v",
			report.StdioClient, report.StdioAdopt, report.StdioScope, report.StdioObserve,
			report.StdioAuto, report.StdioExpired, report.StdioStale, report.StdioFresh))
	}
	return selftest.Passed(
		fmt.Sprintf("real stdio client %s: approval, bypass close, observer denial, auto allow, expiry, stale then fresh generation",
			report.StdioClient),
		"cmd/natcatty-mcp + internal/adapters/mcprpc + app/mcp_policy.go")
}

func runSelftest57(ctx context.Context) (selftest57Report, error) {
	var report selftest57Report
	if err := selftest57CheckScopes(ctx, &report); err != nil {
		return report, err
	}
	if err := selftest57CheckModes(ctx, &report); err != nil {
		return report, err
	}
	if err := selftest57CheckApprovals(ctx, &report); err != nil {
		return report, err
	}
	if err := selftest57CheckGenerations(ctx, &report); err != nil {
		return report, err
	}
	if err := selftest57CheckStdio(ctx, &report); err != nil {
		return report, err
	}
	return report, nil
}

// selftest57Clock is the injected approval clock. It is safe because the
// policy may read it from a blocked call's goroutine.
type selftest57Clock struct {
	mu  sync.Mutex
	now time.Time
}

func newSelftest57Clock() *selftest57Clock {
	return &selftest57Clock{now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
}

func (c *selftest57Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *selftest57Clock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	c.mu.Unlock()
}

// selftest57Closer records session_close calls without touching a terminal.
type selftest57Closer struct {
	mu     sync.Mutex
	closed []string
}

func (c *selftest57Closer) Close(id model.SessionID) (bool, error) {
	c.mu.Lock()
	c.closed = append(c.closed, string(id))
	c.mu.Unlock()
	return true, nil
}

func (c *selftest57Closer) Closed() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.closed...)
}

// selftest57Sessions is the live-session projection the read bridge uses. The
// two Sessions are enough to prove the allowlist and target labels.
type selftest57Sessions struct {
	selftest56EmptySessions
}

func (selftest57Sessions) DescribeAll() []appsession.SessionDescription {
	return []appsession.SessionDescription{
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
	}
}

func (selftest57Sessions) Describe(id model.SessionID) (appsession.SessionDescription, bool) {
	for _, description := range (selftest57Sessions{}).DescribeAll() {
		if description.ID == id {
			return description, true
		}
	}
	return appsession.SessionDescription{}, false
}

func (selftest57Sessions) ScreenText(id model.SessionID) (appsession.ScreenTextSnapshot, bool) {
	if id != "s1" && id != "s2" {
		return appsession.ScreenTextSnapshot{}, false
	}
	return appsession.ScreenTextSnapshot{SessionID: id, Text: "selftest output", Cols: 80, Rows: 24}, true
}

// selftest57Jobs is the ticket-58 job→Session seam. Background job execution
// is not implemented yet, but terminal_stop's policy must still be provable
// with the catalog's real jobId input.
type selftest57Jobs struct{}

func (selftest57Jobs) SessionForJob(jobID string) (model.SessionID, bool) {
	switch jobID {
	case "j1":
		return "s1", true
	case "j2":
		return "s2", true
	default:
		return "", false
	}
}

// selftest57Vault provides the two Hosts the target labels and host_open use.
type selftest57Vault struct {
	selftest56EmptyVault
}

func (selftest57Vault) List(context.Context) ([]domainvault.Host, error) {
	return []domainvault.Host{
		{ID: "h1", Label: "web", Hostname: "web.example", Protocol: domainvault.ProtocolSSH},
		{ID: "h2", Label: "db", Hostname: "db.example", Protocol: domainvault.ProtocolSSH},
	}, nil
}

func (selftest57Vault) Get(_ context.Context, id model.HostID) (domainvault.Host, error) {
	hosts, _ := (selftest57Vault{}).List(context.Background())
	for _, host := range hosts {
		if host.ID == id {
			return host, nil
		}
	}
	return domainvault.Host{}, fmt.Errorf("host %q was not found", id)
}

// selftest57Fixture composes the real policy, handler, bridge, clock, mode, and
// closer used by the production app. The schedule is a no-op because the
// scenario owns the injected clock and calls Sweep itself.
type selftest57Fixture struct {
	policy  *appmcp.Service
	mode    *mcpModeState
	clock   *selftest57Clock
	handler *mcpPolicyHandler
	bridge  *mcpReadBridge
	closer  *selftest57Closer
	signals chan struct{}
}

func newSelftest57Fixture(mode appmcp.Mode, allowlist []string) *selftest57Fixture {
	return newSelftest57FixtureAt(mode, allowlist, "")
}

func newSelftest57FixtureAt(mode appmcp.Mode, allowlist []string, discoveryPath string) *selftest57Fixture {
	clock := newSelftest57Clock()
	modeState := newMCPModeState(discoveryPath)
	_ = modeState.Set(mode)
	signals := make(chan struct{}, 256)
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: mcprpc.ExternalScope,
		Allowlist:           append([]string(nil), allowlist...),
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
	sessions := selftest57Sessions{}
	bridge := newMCPReadBridge(selftest57Vault{}, selftest56EmptyForwarding{}, sessions, nil)
	closer := &selftest57Closer{}
	return &selftest57Fixture{
		policy: policy, mode: modeState, clock: clock,
		handler: newMCPPolicyHandler(bridge, policy, sessions, closer, selftest57Jobs{}),
		bridge:  bridge, closer: closer, signals: signals,
	}
}

func (f *selftest57Fixture) call(ctx context.Context, method string, arguments map[string]any) (map[string]any, error) {
	payload, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	value, err := f.handler.Handle(ctx, method, payload)
	if err != nil {
		return nil, err
	}
	mapped, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned %T", method, value)
	}
	return mapped, nil
}

type selftest57Authorization struct {
	decision appmcp.Decision
	err      error
}

func (f *selftest57Fixture) authorize(ctx context.Context, tool catalog.Tool,
	arguments map[string]any, target appmcp.Target) <-chan selftest57Authorization {
	done := make(chan selftest57Authorization, 1)
	request := appmcp.Request{
		Tool: appmcp.Tool{
			Name:                  tool.Name,
			Write:                 tool.Policy.Write,
			BypassesApproval:      tool.Policy.BypassesApproval,
			BypassesObserverBlock: tool.Policy.BypassesObserverBlock,
		},
		Client:    "selftest-57",
		Target:    target,
		Arguments: arguments,
	}
	go func() {
		decision, err := f.policy.Authorize(ctx, request)
		done <- selftest57Authorization{decision: decision, err: err}
	}()
	return done
}

func (f *selftest57Fixture) awaitPrompt(ctx context.Context) (appmcp.ApprovalView, error) {
	for {
		if pending := f.policy.Snapshot().Pending; len(pending) > 0 {
			return pending[0], nil
		}
		select {
		case <-ctx.Done():
			return appmcp.ApprovalView{}, fmt.Errorf("waiting for an approval prompt: %w", ctx.Err())
		case <-f.signals:
		}
	}
}

func (f *selftest57Fixture) awaitAuthorization(ctx context.Context,
	done <-chan selftest57Authorization) (appmcp.Decision, error) {
	select {
	case result := <-done:
		return result.decision, result.err
	case <-ctx.Done():
		return appmcp.Decision{}, fmt.Errorf("waiting for an authorization: %w", ctx.Err())
	}
}

type selftest57HandlerCall struct {
	value map[string]any
	err   error
}

func (f *selftest57Fixture) callAsync(ctx context.Context, method string,
	arguments map[string]any) <-chan selftest57HandlerCall {
	done := make(chan selftest57HandlerCall, 1)
	go func() {
		value, err := f.call(ctx, method, arguments)
		done <- selftest57HandlerCall{value: value, err: err}
	}()
	return done
}

func (f *selftest57Fixture) awaitCall(ctx context.Context,
	done <-chan selftest57HandlerCall) (map[string]any, error) {
	select {
	case result := <-done:
		return result.value, result.err
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for a handler call: %w", ctx.Err())
	}
}

func policyResultCode(result map[string]any) appmcp.ErrorCode {
	if result == nil {
		return ""
	}
	code, _ := result["code"].(string)
	return appmcp.ErrorCode(code)
}

func policyErrorCode(err error) appmcp.ErrorCode {
	var typed *appmcp.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

func policyArgsForTool(tool catalog.Tool) map[string]any {
	switch tool.Name {
	case "host_open":
		return map[string]any{"hostId": "h1"}
	case "terminal_stop":
		return map[string]any{"jobId": "j1"}
	case "session_close", "terminal_execute", "terminal_start",
		"terminal_poll", "terminal_read_context":
		return map[string]any{"sessionId": "s1"}
	default:
		return nil
	}
}

// selftest57CheckScopes proves the forced scope intersection, the explicit
// empty allowlist, the pre-action rejection order, and the handler's filtered
// environment projection.
func selftest57CheckScopes(ctx context.Context, report *selftest57Report) error {
	fixture := newSelftest57Fixture(appmcp.ModeAuto, []string{"s1", "s2"})
	forced := mcprpc.ExternalScope
	effective, err := fixture.policy.EffectiveSessions(appmcp.RequestedScope{})
	if err != nil || strings.Join(effective, ",") != "s1,s2" {
		return fmt.Errorf("omitted scope intersection = %v (%v)", effective, err)
	}
	requested, err := appmcp.RequestedScopeFrom(map[string]any{"chatSessionId": forced})
	if err != nil {
		return fmt.Errorf("parse the forced chat scope: %w", err)
	}
	effective, err = fixture.policy.EffectiveSessions(requested)
	if err != nil || strings.Join(effective, ",") != "s1,s2" {
		return fmt.Errorf("forced chat scope intersection = %v (%v)", effective, err)
	}
	requested, _ = appmcp.RequestedScopeFrom(map[string]any{"chatSessionId": "chat-other"})
	_, err = fixture.policy.EffectiveSessions(requested)
	if policyErrorCode(err) != appmcp.CodeScopeMismatch {
		return fmt.Errorf("foreign chat scope error = %v", err)
	}
	codes := []appmcp.ErrorCode{appmcp.CodeScopeMismatch}

	requested, _ = appmcp.RequestedScopeFrom(map[string]any{"scopedSessionIds": []any{"s1"}})
	effective, err = fixture.policy.EffectiveSessions(requested)
	if err != nil || strings.Join(effective, ",") != "s1" {
		return fmt.Errorf("narrowed scope = %v (%v)", effective, err)
	}
	requested, _ = appmcp.RequestedScopeFrom(map[string]any{"scopedSessionIds": []any{"s1", "s2", "s1"}})
	effective, err = fixture.policy.EffectiveSessions(requested)
	if err != nil || strings.Join(effective, ",") != "s1,s2" {
		return fmt.Errorf("deduplicated scope = %v (%v)", effective, err)
	}
	requested, _ = appmcp.RequestedScopeFrom(map[string]any{"scopedSessionIds": []any{"s3"}})
	_, err = fixture.policy.EffectiveSessions(requested)
	if policyErrorCode(err) != appmcp.CodeSessionOutOfScope {
		return fmt.Errorf("outside allowlist error = %v", err)
	}
	codes = append(codes, appmcp.CodeSessionOutOfScope)
	requested, _ = appmcp.RequestedScopeFrom(map[string]any{"scopedSessionIds": []any{}})
	effective, err = fixture.policy.EffectiveSessions(requested)
	if err != nil || len(effective) != 0 {
		return fmt.Errorf("explicit empty scope = %v (%v)", effective, err)
	}

	if _, err := appmcp.RequestedScopeFrom(map[string]any{"chatSessionId": float64(1)}); policyErrorCode(err) != appmcp.CodeScopeMismatch {
		return fmt.Errorf("malformed chatSessionId error = %v", err)
	}
	if _, err := appmcp.RequestedScopeFrom(map[string]any{"scopedSessionIds": []any{float64(1)}}); policyErrorCode(err) != appmcp.CodeSessionOutOfScope {
		return fmt.Errorf("malformed scopedSessionIds error = %v", err)
	}
	if _, err := appmcp.RequestedScopeFrom(map[string]any{"scopedSessionIds": "s1"}); policyErrorCode(err) != appmcp.CodeSessionOutOfScope {
		return fmt.Errorf("non-array scopedSessionIds error = %v", err)
	}

	// The handler must filter reads to the effective scope and reject a
	// request before any prompt is created.
	environment, err := fixture.call(ctx, "netcatty/getContext", nil)
	if err != nil {
		return err
	}
	hosts, _ := environment["hosts"].([]any)
	if len(hosts) != 2 {
		return fmt.Errorf("getContext hosts = %d, want 2", len(hosts))
	}
	narrowed, err := fixture.call(ctx, "netcatty/getContext", map[string]any{
		"scopedSessionIds": []any{"s1"},
	})
	if err != nil {
		return err
	}
	narrowedHosts, _ := narrowed["hosts"].([]any)
	if len(narrowedHosts) != 1 || narrowedHosts[0].(map[string]any)["sessionId"] != "s1" {
		return fmt.Errorf("narrowed getContext hosts = %#v", narrowedHosts)
	}
	empty, err := fixture.call(ctx, "netcatty/getContext", map[string]any{
		"scopedSessionIds": []any{},
	})
	if err != nil {
		return err
	}
	emptyHosts, _ := empty["hosts"].([]any)
	if len(emptyHosts) != 0 {
		return fmt.Errorf("explicit empty getContext hosts = %#v", emptyHosts)
	}

	confirm := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	denied, err := confirm.call(ctx, "netcatty/getContext", map[string]any{
		"chatSessionId": "chat-other",
	})
	if err != nil {
		return err
	}
	if policyResultCode(denied) != appmcp.CodeScopeMismatch || len(confirm.policy.Snapshot().Pending) != 0 {
		return fmt.Errorf("scope rejection = %v pending=%d",
			denied, len(confirm.policy.Snapshot().Pending))
	}
	denied, err = confirm.call(ctx, "netcatty/exec", map[string]any{
		"sessionId": "s2", "command": "true",
	})
	if err != nil {
		return err
	}
	if policyResultCode(denied) != appmcp.CodeSessionOutOfScope || len(confirm.policy.Snapshot().Pending) != 0 {
		return fmt.Errorf("out-of-scope write = %v pending=%d",
			denied, len(confirm.policy.Snapshot().Pending))
	}
	if denied["tool"] != "terminal_execute" {
		return fmt.Errorf("out-of-scope denial names %v", denied["tool"])
	}

	report.ScopeOK = true
	report.ScopeBeforePrompt = true
	report.ScopeCodes = strings.Join([]string{string(codes[0]), string(codes[1])}, ",")
	return nil
}

// selftest57CheckModes proves the three permission modes against every catalog
// write, the exact bypass set, and an immediate mode change through the handler.
func selftest57CheckModes(ctx context.Context, report *selftest57Report) error {
	writes := make([]catalog.Tool, 0, 43)
	bypass := make([]string, 0, 2)
	for _, tool := range catalog.All() {
		if !tool.Policy.Write {
			continue
		}
		writes = append(writes, tool)
		if tool.Policy.BypassesApproval {
			bypass = append(bypass, tool.Name)
		}
		if tool.Policy.BypassesApproval != tool.Policy.BypassesObserverBlock {
			report.BypassMismatch++
		}
	}
	sort.Strings(bypass)
	report.BypassNames = strings.Join(bypass, ",")
	report.ObserverWrites = len(writes)
	report.ConfirmWrites = len(writes)
	report.AutoWrites = len(writes)

	observer := newSelftest57Fixture(appmcp.ModeObserver, []string{"s1"})
	for _, tool := range writes {
		_, err := observer.policy.Authorize(ctx, appmcp.Request{
			Tool: appmcp.Tool{
				Name: tool.Name, Write: true,
				BypassesApproval:      tool.Policy.BypassesApproval,
				BypassesObserverBlock: tool.Policy.BypassesObserverBlock,
			},
			Client: "selftest-57",
			Target: appmcp.Target{SessionID: "s1", Display: "s1"},
		})
		if tool.Policy.BypassesObserverBlock {
			if err != nil {
				return fmt.Errorf("observer blocked the bypass tool %s: %w", tool.Name, err)
			}
			report.ObserverBypass++
			continue
		}
		typed := policyErrorCode(err)
		if typed != appmcp.CodePermissionDenied {
			return fmt.Errorf("observer allowed %s: %v", tool.Name, err)
		}
		var detail *appmcp.Error
		if errors.As(err, &detail) && detail.Tool != tool.Name {
			return fmt.Errorf("observer denial for %s names %q", tool.Name, detail.Tool)
		}
		report.ObserverDenied++
	}
	if pending := observer.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("observer created %d prompts", len(pending))
	}

	confirm := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	for _, tool := range writes {
		arguments := policyArgsForTool(tool)
		if tool.Policy.BypassesApproval {
			_, err := confirm.policy.Authorize(ctx, appmcp.Request{
				Tool: appmcp.Tool{
					Name: tool.Name, Write: true,
					BypassesApproval:      true,
					BypassesObserverBlock: tool.Policy.BypassesObserverBlock,
				},
				Client: "selftest-57",
				Target: appmcp.Target{SessionID: "s1", Display: "s1"},
			})
			if err != nil {
				return fmt.Errorf("confirm blocked the bypass tool %s: %w", tool.Name, err)
			}
			if pending := confirm.policy.Snapshot().Pending; len(pending) != 0 {
				return fmt.Errorf("confirm prompted for the bypass tool %s", tool.Name)
			}
			report.ConfirmBypass++
			continue
		}
		done := confirm.authorize(ctx, tool, arguments,
			appmcp.Target{SessionID: "s1", Display: "s1"})
		prompt, err := confirm.awaitPrompt(ctx)
		if err != nil {
			return fmt.Errorf("%s: %w", tool.Name, err)
		}
		if prompt.Tool != tool.Name || prompt.Client != "selftest-57" ||
			prompt.Scope != mcprpc.ExternalScope || prompt.Remaining <= 0 ||
			prompt.Deadline.Sub(prompt.CreatedAt) != appmcp.DefaultApprovalTimeout {
			return fmt.Errorf("%s prompt = %+v", tool.Name, prompt)
		}
		if err := confirm.policy.Deny(prompt.ID); err != nil {
			return fmt.Errorf("deny %s: %w", tool.Name, err)
		}
		_, err = confirm.awaitAuthorization(ctx, done)
		if policyErrorCode(err) != appmcp.CodeApprovalDenied {
			return fmt.Errorf("%s denied approval result = %v", tool.Name, err)
		}
		report.ConfirmPrompts++
	}
	if pending := confirm.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("confirm left %d prompts pending", len(pending))
	}

	auto := newSelftest57Fixture(appmcp.ModeAuto, []string{"s1"})
	for _, tool := range writes {
		_, err := auto.policy.Authorize(ctx, appmcp.Request{
			Tool: appmcp.Tool{
				Name: tool.Name, Write: true,
				BypassesApproval:      tool.Policy.BypassesApproval,
				BypassesObserverBlock: tool.Policy.BypassesObserverBlock,
			},
			Client: "selftest-57",
			Target: appmcp.Target{SessionID: "s1", Display: "s1"},
		})
		if err != nil {
			return fmt.Errorf("auto blocked %s: %w", tool.Name, err)
		}
		report.AutoAllowed++
	}
	report.AutoPrompts = len(auto.policy.Snapshot().Pending)

	// A mode change is re-read through the handler for the very next call and
	// for a retry and another write tool.
	switching := newSelftest57Fixture(appmcp.ModeAuto, []string{"s1"})
	autoCall, err := switching.call(ctx, "netcatty/exec", map[string]any{
		"sessionId": "s1", "command": "true",
	})
	if err != nil {
		return err
	}
	if policyResultCode(autoCall) == appmcp.CodePermissionDenied ||
		len(switching.policy.Snapshot().Pending) != 0 {
		return fmt.Errorf("auto handler call = %v pending=%d",
			autoCall, len(switching.policy.Snapshot().Pending))
	}
	if err := switching.mode.Set(appmcp.ModeObserver); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		denied, err := switching.call(ctx, "netcatty/exec", map[string]any{
			"sessionId": "s1", "command": "true",
		})
		if err != nil {
			return err
		}
		if policyResultCode(denied) != appmcp.CodePermissionDenied ||
			denied["tool"] != "terminal_execute" {
			return fmt.Errorf("observer retry %d = %v", attempt, denied)
		}
	}
	otherPath, err := switching.call(ctx, "netcatty/sftp/write", map[string]any{
		"sessionId": "s1", "path": "/tmp/x", "content": "data",
	})
	if err != nil {
		return err
	}
	if policyResultCode(otherPath) != appmcp.CodePermissionDenied ||
		otherPath["tool"] != "sftp_write_file" {
		return fmt.Errorf("observer other path = %v", otherPath)
	}
	if err := switching.mode.Set(appmcp.ModeConfirm); err != nil {
		return err
	}
	pendingCall := switching.callAsync(ctx, "netcatty/exec", map[string]any{
		"sessionId": "s1", "command": "true",
	})
	prompt, err := switching.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	if err := switching.policy.Deny(prompt.ID); err != nil {
		return err
	}
	denied, err := switching.awaitCall(ctx, pendingCall)
	if err != nil {
		return err
	}
	if policyResultCode(denied) != appmcp.CodeApprovalDenied {
		return fmt.Errorf("confirm handler result = %v", denied)
	}
	report.ModeSwitch = true
	return nil
}

// selftest57CheckApprovals proves the 109.9/110.0 boundary, single use, expiry
// replay, reads, the GUI seam, and the ownership rules for both bypass tools.
func selftest57CheckApprovals(ctx context.Context, report *selftest57Report) error {
	tool, ok := catalog.ByName("terminal_execute")
	if !ok {
		return errors.New("terminal_execute is missing from the catalog")
	}
	arguments := map[string]any{"sessionId": "s1", "command": "true"}
	target := appmcp.Target{SessionID: "s1", Display: "web shell (s1)"}

	// 109.9 s: still inside the absolute deadline and consumed once.
	before := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	done := before.authorize(ctx, tool, arguments, target)
	prompt, err := before.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	if prompt.Remaining != appmcp.DefaultApprovalTimeout ||
		prompt.Deadline.Sub(prompt.CreatedAt) != 110*time.Second {
		return fmt.Errorf("fresh prompt remaining=%s deadline-created=%s",
			prompt.Remaining, prompt.Deadline.Sub(prompt.CreatedAt))
	}
	before.clock.Advance(109*time.Second + 900*time.Millisecond)
	if err := before.policy.Approve(prompt.ID); err != nil {
		return fmt.Errorf("approve at 109.9 s: %w", err)
	}
	decision, err := before.awaitAuthorization(ctx, done)
	if err != nil || !decision.Approved || decision.ApprovalID != prompt.ID {
		return fmt.Errorf("109.9 s decision = %+v (%v)", decision, err)
	}
	report.Boundary1099 = true
	if err := before.policy.Approve(prompt.ID); policyErrorCode(err) != appmcp.CodeApprovalInvalid {
		return fmt.Errorf("approved prompt replay = %v", err)
	}
	report.ApproveSingle = true

	// 110.0 s exactly: expired, the waiting call is denied, and Approve cannot
	// replay the expired id.
	at := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	done = at.authorize(ctx, tool, arguments, target)
	prompt, err = at.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	at.clock.Advance(110 * time.Second)
	at.policy.Sweep()
	_, err = at.awaitAuthorization(ctx, done)
	if policyErrorCode(err) != appmcp.CodeApprovalExpired {
		return fmt.Errorf("110.0 s authorization = %v", err)
	}
	if err := at.policy.Approve(prompt.ID); policyErrorCode(err) != appmcp.CodeApprovalExpired {
		return fmt.Errorf("expired approval replay = %v", err)
	}
	recent := at.policy.Snapshot().Recent
	if len(recent) != 1 || recent[0].Status != appmcp.ApprovalExpired ||
		!recent[0].Expired || recent[0].Remaining != 0 {
		return fmt.Errorf("expired recent = %+v", recent)
	}
	if pending := at.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("expired prompt remains pending: %+v", pending)
	}
	report.Boundary1100 = true
	report.ExpiryReplay = true

	// Explicit denial is also single-use.
	denied := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	done = denied.authorize(ctx, tool, arguments, target)
	prompt, err = denied.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	if err := denied.policy.Deny(prompt.ID); err != nil {
		return err
	}
	_, err = denied.awaitAuthorization(ctx, done)
	if policyErrorCode(err) != appmcp.CodeApprovalDenied {
		return fmt.Errorf("denied authorization = %v", err)
	}
	if err := denied.policy.Approve(prompt.ID); policyErrorCode(err) != appmcp.CodeApprovalInvalid {
		return fmt.Errorf("denied prompt replay = %v", err)
	}
	report.DenySingle = true

	// Reads need no approval and use the effective scope.
	reads := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	environment, err := reads.call(ctx, "netcatty/getContext", nil)
	if err != nil || environment["ok"] == false {
		return fmt.Errorf("confirm getContext = %v (%v)", environment, err)
	}
	readContext, err := reads.call(ctx, "netcatty/readContext", map[string]any{"sessionId": "s1"})
	if err != nil || readContext["ok"] != true {
		return fmt.Errorf("confirm readContext = %v (%v)", readContext, err)
	}
	if pending := reads.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("reads created %d prompts", len(pending))
	}
	report.ReadsNoPrompt = true

	// The Settings controller is the GUI's seam for the same policy: approve,
	// deny, expired, dismiss, and mode selection.
	gui := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	state := settings.NewMCPState(&mcpSettingsController{policy: gui.policy, mode: gui.mode})
	done = gui.authorize(ctx, tool, arguments, target)
	prompt, err = gui.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	snapshot := state.Snapshot()
	if len(snapshot.Pending) != 1 || snapshot.Pending[0].Remaining != 110*time.Second ||
		snapshot.ForcedScope != mcprpc.ExternalScope {
		return fmt.Errorf("GUI snapshot = %+v", snapshot)
	}
	state.Approve(prompt.ID)
	if state.Problem() != settings.MCPProblemNone {
		return fmt.Errorf("GUI approve problem = %v", state.Problem())
	}
	decision, err = gui.awaitAuthorization(ctx, done)
	if err != nil || !decision.Approved {
		return fmt.Errorf("GUI approved authorization = %+v (%v)", decision, err)
	}
	state.Approve(prompt.ID)
	if state.Problem() != settings.MCPProblemInvalid {
		return fmt.Errorf("GUI approve replay problem = %v", state.Problem())
	}
	report.GUIApprove = true

	done = gui.authorize(ctx, tool, arguments, target)
	prompt, err = gui.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	state.Deny(prompt.ID)
	if state.Problem() != settings.MCPProblemNone {
		return fmt.Errorf("GUI deny problem = %v", state.Problem())
	}
	_, err = gui.awaitAuthorization(ctx, done)
	if policyErrorCode(err) != appmcp.CodeApprovalDenied {
		return fmt.Errorf("GUI denied authorization = %v", err)
	}
	report.GUIDeny = true

	done = gui.authorize(ctx, tool, arguments, target)
	prompt, err = gui.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	gui.clock.Advance(110 * time.Second)
	gui.policy.Sweep()
	state.Approve(prompt.ID)
	if state.Problem() != settings.MCPProblemExpired {
		return fmt.Errorf("GUI expired problem = %v", state.Problem())
	}
	state.Dismiss(prompt.ID)
	if recent := state.Snapshot().Recent; len(recent) != 0 {
		return fmt.Errorf("GUI dismiss left %+v", recent)
	}
	report.GUIExpired = true
	report.GUIDismiss = true

	state.SetMode(appmcp.ModeObserver)
	if state.Problem() != settings.MCPProblemNone ||
		gui.policy.CurrentMode() != appmcp.ModeObserver ||
		state.Snapshot().Mode != appmcp.ModeObserver {
		return fmt.Errorf("GUI mode = %v policy=%v snapshot=%v",
			state.Problem(), gui.policy.CurrentMode(), state.Snapshot().Mode)
	}
	report.GUIMode = true

	// Both bypass tools still obey scope, allowlist, and generation.
	bypass := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	if result, err := bypass.call(ctx, "netcatty/jobStop", map[string]any{
		"jobId": "j1",
	}); err != nil || policyResultCode(result) != "" {
		return fmt.Errorf("in-scope terminal_stop = %v (%v)", result, err)
	}
	if pending := bypass.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("terminal_stop prompted: %+v", pending)
	}
	if result, err := bypass.call(ctx, "netcatty/jobStop", map[string]any{
		"jobId": "j2",
	}); err != nil || policyResultCode(result) != appmcp.CodeSessionOutOfScope {
		return fmt.Errorf("out-of-scope terminal_stop = %v (%v)", result, err)
	}
	if result, err := bypass.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s2",
	}); err != nil || policyResultCode(result) != appmcp.CodeSessionOutOfScope {
		return fmt.Errorf("out-of-scope session_close = %v (%v)", result, err)
	}
	if result, err := bypass.call(ctx, "netcatty/jobStop", map[string]any{
		"jobId": "j1", "chatSessionId": "chat-other",
	}); err != nil || policyResultCode(result) != appmcp.CodeScopeMismatch {
		return fmt.Errorf("foreign-scope terminal_stop = %v (%v)", result, err)
	}
	if result, err := bypass.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s1",
	}); err != nil || policyResultCode(result) != appmcp.CodeSessionNotOwned {
		return fmt.Errorf("unowned session_close = %v (%v)", result, err)
	}
	if pending := bypass.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("bypass checks prompted: %+v", pending)
	}
	report.BypassScope = true
	return nil
}

// selftest57CheckGenerations proves a replacement invalidates the old grant and
// a pending approval, and that only a fresh generation can act.
func selftest57CheckGenerations(ctx context.Context, report *selftest57Report) error {
	fixture := newSelftest57Fixture(appmcp.ModeConfirm, []string{"s1"})
	if fixture.policy.Generation() != 1 {
		return fmt.Errorf("initial generation = %d", fixture.policy.Generation())
	}
	fixture.policy.GrantOwnedSession("s1", "h1")
	tool, _ := catalog.ByName("terminal_execute")
	done := fixture.authorize(ctx, tool, map[string]any{
		"sessionId": "s1", "command": "true",
	}, appmcp.Target{SessionID: "s1", Display: "web shell (s1)"})
	prompt, err := fixture.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	fixture.policy.ObserveSessions([]appmcp.SessionRef{{ID: "s1-replacement", HostID: "h1"}})
	if fixture.policy.Generation() != 2 {
		return fmt.Errorf("replacement generation = %d, want 2", fixture.policy.Generation())
	}
	_, err = fixture.awaitAuthorization(ctx, done)
	if policyErrorCode(err) != appmcp.CodeStaleGeneration {
		return fmt.Errorf("stale authorization = %v", err)
	}
	if err := fixture.policy.Approve(prompt.ID); policyErrorCode(err) != appmcp.CodeStaleGeneration {
		return fmt.Errorf("stale approval replay = %v", err)
	}
	if err := fixture.policy.ValidateOwnership("s1"); policyErrorCode(err) != appmcp.CodeStaleGeneration {
		return fmt.Errorf("stale ownership = %v", err)
	}
	if result, err := fixture.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s1",
	}); err != nil || policyResultCode(result) != appmcp.CodeStaleGeneration {
		return fmt.Errorf("stale session_close = %v (%v)", result, err)
	}
	if result, err := fixture.call(ctx, "netcatty/jobStop", map[string]any{
		"jobId": "j1",
	}); err != nil || policyResultCode(result) != appmcp.CodeStaleGeneration {
		return fmt.Errorf("stale terminal_stop = %v (%v)", result, err)
	}
	if closed := fixture.closer.Closed(); len(closed) != 0 {
		return fmt.Errorf("stale close reached the Session service: %v", closed)
	}

	// The same Session in the new generation must be granted again.
	if generation := fixture.policy.GrantOwnedSession("s1", "h1"); generation != 2 {
		return fmt.Errorf("fresh grant generation = %d", generation)
	}
	if result, err := fixture.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s1",
	}); err != nil || result["ok"] != true {
		return fmt.Errorf("fresh session_close = %v (%v)", result, err)
	}
	if closed := fixture.closer.Closed(); len(closed) != 1 || closed[0] != "s1" {
		return fmt.Errorf("fresh close calls = %v", closed)
	}
	if result, err := fixture.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s1",
	}); err != nil || policyResultCode(result) != appmcp.CodeSessionNotOwned {
		return fmt.Errorf("second close = %v (%v)", result, err)
	}
	if result, err := fixture.call(ctx, "netcatty/jobStop", map[string]any{
		"jobId": "j1",
	}); err != nil || policyResultCode(result) != "" {
		return fmt.Errorf("fresh terminal_stop = %v (%v)", result, err)
	}

	// A scope-wide invalidation advances again and still requires a re-grant.
	// Keep a grant in generation 2 so the invalidation has something to mark
	// stale, exactly as a live Session would.
	if generation := fixture.policy.GrantOwnedSession("s1", "h1"); generation != 2 {
		return fmt.Errorf("pre-invalidation grant = %d", generation)
	}
	if generation := fixture.policy.InvalidateOwnership(); generation != 3 {
		return fmt.Errorf("invalidated generation = %d", generation)
	}
	if result, err := fixture.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s1",
	}); err != nil || policyResultCode(result) != appmcp.CodeStaleGeneration {
		return fmt.Errorf("invalidated session_close = %v (%v)", result, err)
	}
	if generation := fixture.policy.GrantOwnedSession("s1", "h1"); generation != 3 {
		return fmt.Errorf("third-generation grant = %d", generation)
	}
	if result, err := fixture.call(ctx, "public/session/close", map[string]any{
		"sessionId": "s1",
	}); err != nil || result["ok"] != true {
		return fmt.Errorf("third-generation session_close = %v (%v)", result, err)
	}
	report.GenerationStale = true
	report.GenerationFresh = true
	report.Generation = fixture.policy.Generation()
	return nil
}

// selftest57StdioClient speaks newline-delimited MCP JSON-RPC to a real
// cmd/natcatty-mcp process. Send is separate from receive because a confirm
// write blocks until the policy approval is resolved.
type selftest57StdioClient struct {
	stdin      io.Writer
	lines      chan string
	readErrors chan error
	nextID     int
}

func newSelftest57StdioClient(stdin io.Writer, stdout io.Reader) *selftest57StdioClient {
	client := &selftest57StdioClient{
		stdin:      stdin,
		lines:      make(chan string, 32),
		readErrors: make(chan error, 1),
	}
	go func() {
		reader := bufio.NewReader(stdout)
		for {
			line, err := reader.ReadString('\n')
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				client.lines <- trimmed
			}
			if err != nil {
				client.readErrors <- err
				return
			}
		}
	}()
	return client
}

func (c *selftest57StdioClient) send(method string, params any) (int, error) {
	c.nextID++
	message := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		message["params"] = params
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		return 0, err
	}
	if _, err := c.stdin.Write(append(encoded, '\n')); err != nil {
		return 0, err
	}
	return c.nextID, nil
}

func (c *selftest57StdioClient) receive(ctx context.Context, id int) (map[string]any, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for response %d: %w", id, ctx.Err())
		case err := <-c.readErrors:
			return nil, fmt.Errorf("reading stdout: %w", err)
		case line := <-c.lines:
			var response map[string]any
			if err := json.Unmarshal([]byte(line), &response); err != nil {
				continue
			}
			number, ok := response["id"].(float64)
			if !ok || int(number) != id {
				continue
			}
			if rpcErr, present := response["error"]; present {
				return nil, fmt.Errorf("JSON-RPC error: %v", rpcErr)
			}
			result, ok := response["result"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("response %d has no result: %v", id, response)
			}
			return result, nil
		}
	}
}

func (c *selftest57StdioClient) request(ctx context.Context, method string, params any) (map[string]any, error) {
	id, err := c.send(method, params)
	if err != nil {
		return nil, err
	}
	return c.receive(ctx, id)
}

func selftest57ToolResult(response map[string]any) (string, bool, error) {
	content, _ := response["content"].([]any)
	if len(content) == 0 {
		return "", false, fmt.Errorf("tool result has no content: %v", response)
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	isError, _ := response["isError"].(bool)
	return text, isError, nil
}

type selftest57Stderr struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *selftest57Stderr) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(data)
}

func (s *selftest57Stderr) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// selftest57CheckStdio builds the real stdio client, connects it to a dedicated
// authenticated loopback listener, and drives approvals, bypass close, mode
// changes, expiry, and generation invalidation end to end.
func selftest57CheckStdio(ctx context.Context, report *selftest57Report) error {
	root, err := selftest57ModuleRoot()
	if err != nil {
		return err
	}
	binaryDirectory, err := os.MkdirTemp("", "natcatty-selftest-57-bin-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(binaryDirectory)
	binaryName := "natcatty-mcp"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(binaryDirectory, binaryName)
	build := exec.CommandContext(ctx, "go", "build", "-o", binaryPath, "./cmd/natcatty-mcp")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build cmd/natcatty-mcp: %w\n%s", err, output)
	}

	stateDirectory, err := os.MkdirTemp("", "natcatty-selftest-57-state-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stateDirectory)
	discoveryPath := filepath.Join(stateDirectory, "discovery.json")
	fixture := newSelftest57FixtureAt(appmcp.ModeConfirm, []string{"s1"}, discoveryPath)
	listener, err := startExternalMCP(ctx, fixture.bridge, discoveryPath, nil,
		withMCPHandler(fixture.handler), withMCPModeState(fixture.mode))
	if err != nil {
		return fmt.Errorf("start the selftest listener: %w", err)
	}
	defer listener.Close()

	command := exec.CommandContext(ctx, binaryPath)
	command.Dir = root
	command.Env = append(os.Environ(),
		mcprpc.EnvDiscoveryFile+"="+discoveryPath,
		mcprpc.EnvPort+"=",
		mcprpc.EnvToken+"=",
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr selftest57Stderr
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("start natcatty-mcp: %w", err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			_, _ = command.Process.Wait()
		}
	}()
	client := newSelftest57StdioClient(stdin, stdout)
	state := settings.NewMCPState(&mcpSettingsController{policy: fixture.policy, mode: fixture.mode})

	initialize, err := client.request(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "selftest-57", "version": "1.0"},
	})
	if err != nil {
		return fmt.Errorf("initialize: %w (%s)", err, stderr.String())
	}
	serverInfo, _ := initialize["serverInfo"].(map[string]any)
	if serverInfo["name"] != mcp.ServerName || serverInfo["version"] != mcp.ServerVersion {
		return fmt.Errorf("serverInfo = %v", serverInfo)
	}

	environment, err := client.request(ctx, "tools/call", map[string]any{
		"name": "get_environment", "arguments": map[string]any{},
	})
	if err != nil {
		return err
	}
	environmentText, environmentError, err := selftest57ToolResult(environment)
	if err != nil {
		return err
	}
	if environmentError ||
		!strings.Contains(environmentText, `"sessionId": "s1"`) ||
		strings.Contains(environmentText, `"sessionId": "s2"`) {
		return fmt.Errorf("get_environment scope = %q (error=%v)", environmentText, environmentError)
	}
	report.StdioScope = true

	// host_open in confirm mode blocks on a prompt showing the real client.
	hostOpenID, err := client.send("tools/call", map[string]any{
		"name":      "host_open",
		"arguments": map[string]any{"hostId": "h1", "newTab": "false"},
	})
	if err != nil {
		return err
	}
	prompt, err := fixture.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	report.StdioClient = prompt.Client
	if prompt.Client != "selftest-57/1.0" || prompt.Tool != "host_open" ||
		prompt.Scope != mcprpc.ExternalScope || !strings.Contains(prompt.Target, "web") ||
		!strings.Contains(prompt.Summary, `hostId="h1"`) ||
		prompt.Remaining != 110*time.Second {
		return fmt.Errorf("stdio prompt = %+v", prompt)
	}
	state.Approve(prompt.ID)
	if state.Problem() != settings.MCPProblemNone {
		return fmt.Errorf("stdio GUI approve problem = %v", state.Problem())
	}
	hostOpen, err := client.receive(ctx, hostOpenID)
	if err != nil {
		return err
	}
	hostOpenText, hostOpenError, err := selftest57ToolResult(hostOpen)
	if err != nil {
		return err
	}
	if hostOpenError || !strings.Contains(hostOpenText, `"status": "adopted"`) ||
		!strings.Contains(hostOpenText, `"sessionId": "s1"`) {
		return fmt.Errorf("host_open result: error=%v %s", hostOpenError, hostOpenText)
	}
	report.StdioAdopt = true

	// terminal_stop uses the catalog's jobId input. The job resolves to s2,
	// outside the allowlist, so the bypass tool is rejected; a job inside the
	// scope reaches the handler without an approval prompt.
	stopOutOfScope, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_stop", "arguments": map[string]any{"jobId": "j2"},
	})
	if err != nil {
		return err
	}
	stopOutOfScopeText, stopOutOfScopeError, err := selftest57ToolResult(stopOutOfScope)
	if err != nil {
		return err
	}
	if !stopOutOfScopeError || !strings.Contains(stopOutOfScopeText, "mcp_session_out_of_scope") {
		return fmt.Errorf("out-of-scope terminal_stop: error=%v %s", stopOutOfScopeError, stopOutOfScopeText)
	}
	stopInScope, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_stop", "arguments": map[string]any{"jobId": "j1"},
	})
	if err != nil {
		return err
	}
	stopInScopeText, _, err := selftest57ToolResult(stopInScope)
	if err != nil {
		return err
	}
	if !strings.Contains(stopInScopeText, "Background job not found") {
		return fmt.Errorf("in-scope terminal_stop result = %s", stopInScopeText)
	}
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("terminal_stop prompted: %+v", pending)
	}

	// session_close bypasses approval and still passes scope and ownership.
	closeResponse, err := client.request(ctx, "tools/call", map[string]any{
		"name": "session_close", "arguments": map[string]any{"sessionId": "s1"},
	})
	if err != nil {
		return err
	}
	closeText, closeError, err := selftest57ToolResult(closeResponse)
	if err != nil {
		return err
	}
	if closeError || !strings.Contains(closeText, `"status": "closed"`) {
		return fmt.Errorf("session_close result: error=%v %s", closeError, closeText)
	}
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("session_close prompted: %+v", pending)
	}

	// The GUI mode selector persists to discovery and applies immediately.
	state.SetMode(appmcp.ModeObserver)
	if state.Problem() != settings.MCPProblemNone {
		return fmt.Errorf("stdio GUI observer mode problem = %v", state.Problem())
	}
	discovery, err := mcprpc.ReadDiscovery(discoveryPath)
	if err != nil || discovery.PermissionMode != string(appmcp.ModeObserver) {
		return fmt.Errorf("discovery mode = %q (%v)", discovery.PermissionMode, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := client.request(ctx, "tools/call", map[string]any{
			"name": "terminal_execute", "arguments": map[string]any{
				"sessionId": "s1", "command": "true",
			},
		})
		if err != nil {
			return err
		}
		text, isError, err := selftest57ToolResult(response)
		if err != nil {
			return err
		}
		if !isError || !strings.Contains(text, "mcp_permission_denied") ||
			!strings.Contains(text, "terminal_execute") {
			return fmt.Errorf("observer attempt %d: error=%v %s", attempt, isError, text)
		}
	}
	report.StdioObserve = true

	state.SetMode(appmcp.ModeAuto)
	autoResponse, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s1", "command": "true",
		},
	})
	if err != nil {
		return err
	}
	autoText, autoError, err := selftest57ToolResult(autoResponse)
	if err != nil {
		return err
	}
	if !autoError || !strings.Contains(autoText, "write execution is not available") ||
		strings.Contains(autoText, "mcp_permission_denied") {
		return fmt.Errorf("auto mode result: error=%v %s", autoError, autoText)
	}
	if pending := fixture.policy.Snapshot().Pending; len(pending) != 0 {
		return fmt.Errorf("auto mode prompted: %+v", pending)
	}
	report.StdioAuto = true

	outOfScope, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s2", "command": "true",
		},
	})
	if err != nil {
		return err
	}
	outOfScopeText, outOfScopeError, err := selftest57ToolResult(outOfScope)
	if err != nil {
		return err
	}
	if !outOfScopeError || !strings.Contains(outOfScopeText, "mcp_session_out_of_scope") {
		return fmt.Errorf("out-of-scope result: error=%v %s", outOfScopeError, outOfScopeText)
	}
	foreignScope, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s1", "command": "true", "chatSessionId": "chat-other",
		},
	})
	if err != nil {
		return err
	}
	foreignText, foreignError, err := selftest57ToolResult(foreignScope)
	if err != nil {
		return err
	}
	if !foreignError || !strings.Contains(foreignText, "mcp_scope_mismatch") {
		return fmt.Errorf("foreign scope result: error=%v %s", foreignError, foreignText)
	}

	// Confirm mode: the approval expires at exactly 110 s through the real
	// stdio path; the GUI sees the expired state and dismisses it.
	state.SetMode(appmcp.ModeConfirm)
	expireID, err := client.send("tools/call", map[string]any{
		"name": "terminal_execute", "arguments": map[string]any{
			"sessionId": "s1", "command": "true",
		},
	})
	if err != nil {
		return err
	}
	prompt, err = fixture.awaitPrompt(ctx)
	if err != nil {
		return err
	}
	fixture.clock.Advance(110 * time.Second)
	fixture.policy.Sweep()
	expired, err := client.receive(ctx, expireID)
	if err != nil {
		return err
	}
	expiredText, expiredError, err := selftest57ToolResult(expired)
	if err != nil {
		return err
	}
	if !expiredError || !strings.Contains(expiredText, "mcp_approval_expired") {
		return fmt.Errorf("expired stdio result: error=%v %s", expiredError, expiredText)
	}
	state.Approve(prompt.ID)
	if state.Problem() != settings.MCPProblemExpired {
		return fmt.Errorf("stdio GUI expired problem = %v", state.Problem())
	}
	state.Dismiss(prompt.ID)
	if recent := state.Snapshot().Recent; len(recent) != 0 {
		return fmt.Errorf("stdio GUI dismiss left %+v", recent)
	}
	report.StdioExpired = true

	// A re-open after a generation change is required for session_close.
	state.SetMode(appmcp.ModeAuto)
	reopen, err := client.request(ctx, "tools/call", map[string]any{
		"name": "host_open", "arguments": map[string]any{"hostId": "h1"},
	})
	if err != nil {
		return err
	}
	reopenText, reopenError, err := selftest57ToolResult(reopen)
	if err != nil {
		return err
	}
	if reopenError || !strings.Contains(reopenText, `"status": "adopted"`) {
		return fmt.Errorf("re-open result: error=%v %s", reopenError, reopenText)
	}
	fixture.policy.InvalidateOwnership()
	stale, err := client.request(ctx, "tools/call", map[string]any{
		"name": "session_close", "arguments": map[string]any{"sessionId": "s1"},
	})
	if err != nil {
		return err
	}
	staleText, staleError, err := selftest57ToolResult(stale)
	if err != nil {
		return err
	}
	if !staleError || !strings.Contains(staleText, "mcp_stale_generation") {
		return fmt.Errorf("stale close result: error=%v %s", staleError, staleText)
	}
	staleStop, err := client.request(ctx, "tools/call", map[string]any{
		"name": "terminal_stop", "arguments": map[string]any{"jobId": "j1"},
	})
	if err != nil {
		return err
	}
	staleStopText, staleStopError, err := selftest57ToolResult(staleStop)
	if err != nil {
		return err
	}
	if !staleStopError || !strings.Contains(staleStopText, "mcp_stale_generation") {
		return fmt.Errorf("stale terminal_stop result: error=%v %s", staleStopError, staleStopText)
	}
	report.StdioStale = true

	fresh, err := client.request(ctx, "tools/call", map[string]any{
		"name": "host_open", "arguments": map[string]any{"hostId": "h1"},
	})
	if err != nil {
		return err
	}
	freshText, freshError, err := selftest57ToolResult(fresh)
	if err != nil {
		return err
	}
	if freshError || !strings.Contains(freshText, `"status": "adopted"`) {
		return fmt.Errorf("fresh re-open result: error=%v %s", freshError, freshText)
	}
	freshClose, err := client.request(ctx, "tools/call", map[string]any{
		"name": "session_close", "arguments": map[string]any{"sessionId": "s1"},
	})
	if err != nil {
		return err
	}
	freshCloseText, freshCloseError, err := selftest57ToolResult(freshClose)
	if err != nil {
		return err
	}
	if freshCloseError || !strings.Contains(freshCloseText, `"status": "closed"`) {
		return fmt.Errorf("fresh close result: error=%v %s", freshCloseError, freshCloseText)
	}
	report.StdioFresh = true

	_ = stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	select {
	case <-waitDone:
		waited = true
	case <-ctx.Done():
		return fmt.Errorf("natcatty-mcp did not exit: %w (%s)", ctx.Err(), stderr.String())
	}
	return nil
}

// selftest57ModuleRoot finds the module root from the compiled source path,
// then from the working directory, so go build can address ./cmd/natcatty-mcp.
func selftest57ModuleRoot() (string, error) {
	if _, file, _, ok := runtime.Caller(0); ok && filepath.IsAbs(file) {
		candidate := filepath.Dir(filepath.Dir(file))
		if info, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	directories := make([]string, 0, 3)
	if working, err := os.Getwd(); err == nil {
		directories = append(directories, working)
	}
	if executable, err := os.Executable(); err == nil {
		directories = append(directories, filepath.Dir(executable))
	}
	for _, directory := range directories {
		for candidate := directory; ; candidate = filepath.Dir(candidate) {
			if info, err := os.Stat(filepath.Join(candidate, "go.mod")); err == nil && !info.IsDir() {
				return candidate, nil
			}
			if filepath.Dir(candidate) == candidate {
				break
			}
		}
	}
	return "", errors.New("selftest 57: could not find the module root (go.mod)")
}
