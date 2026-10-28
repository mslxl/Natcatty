package app

// Ticket 58 rework: the app layer's live-Session scope. These tests pin the
// shipped-app defaults and the launch-time intersection without a window, so a
// regression that reintroduces a launch-time-only allowlist fails here rather
// than only in a GUI run.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

func TestMCPLiveSessionsSharesLiveSessionsByDefault(t *testing.T) {
	userData := t.TempDir()
	sessions := appsession.NewService(writeTestFactory{ssh: newWriteTestTransport()})
	t.Cleanup(sessions.CloseAll)
	session, err := sessions.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "shared", HostID: "host-1",
		Hostname: "shared.example", Port: 22, Username: "ops", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the Session: %v", err)
	}

	provider := newMCPLiveSessions(sessions, userData, nil)
	if !provider.SharingEnabled() {
		t.Fatal("a fresh install must share live Sessions")
	}
	live := provider.LiveSessions()
	if len(live) != 1 || live[0].ID != string(session.ID()) {
		t.Fatalf("live Sessions = %+v, want the open Session", live)
	}
	if live[0].HostID != "host-1" || live[0].Label != "shared" {
		t.Fatalf("live Session identity = %+v", live[0])
	}
	// The policy reads the provider on every call: a new Session is in scope
	// without restarting anything.
	second, err := sessions.Open(context.Background(), appsession.StartSpec{
		Kind: appsession.KindSSH, Label: "second", HostID: "host-2",
		Hostname: "second.example", Port: 22, Username: "ops", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("open the second Session: %v", err)
	}
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: "__external_mcp__",
		Sessions:            provider,
		Mode:                func() appmcp.Mode { return appmcp.ModeAuto },
	})
	ids, err := policy.EffectiveSessions(appmcp.RequestedScope{})
	if err != nil {
		t.Fatalf("EffectiveSessions: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("effective Sessions = %v, want both live Sessions", ids)
	}
	if _, err := sessions.Close(second.ID()); err != nil {
		t.Fatalf("close the second Session: %v", err)
	}
	// A closed Session unregisters when its transport finishes; the scope must
	// follow the real lifetime, not the Close request.
	select {
	case <-second.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the closed Session never finished")
	}
	ids, _ = policy.EffectiveSessions(appmcp.RequestedScope{})
	if len(ids) != 1 || ids[0] != string(session.ID()) {
		t.Fatalf("effective Sessions after close = %v, want only the first", ids)
	}
}

func TestMCPLiveSessionsSwitchPersistsAndSurvivesAReload(t *testing.T) {
	userData := t.TempDir()
	sessions := appsession.NewService(writeTestFactory{ssh: newWriteTestTransport()})
	t.Cleanup(sessions.CloseAll)
	if _, err := sessions.OpenLocal(context.Background()); err != nil {
		t.Fatalf("open the local shell: %v", err)
	}
	redraws := 0
	provider := newMCPLiveSessions(sessions, userData, func() { redraws++ })
	if err := provider.SetEnabled(false); err != nil {
		t.Fatalf("turn sharing off: %v", err)
	}
	if redraws != 1 {
		t.Fatalf("redraws = %d, want 1", redraws)
	}
	path := filepath.Join(userData, "external-mcp", mcpSharingFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the sharing file: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode the sharing file: %v", err)
	}
	if record["enabled"] != false {
		t.Fatalf("sharing file = %v, want enabled=false", record)
	}
	reloaded := newMCPLiveSessions(sessions, userData, nil)
	if reloaded.SharingEnabled() {
		t.Fatal("the off switch did not survive a reload")
	}
	if err := reloaded.SetEnabled(true); err != nil {
		t.Fatalf("turn sharing back on: %v", err)
	}
	if !newMCPLiveSessions(sessions, userData, nil).SharingEnabled() {
		t.Fatal("the on switch did not survive a reload")
	}
}

func TestMCPLiveSessionsDefaultsOnForMissingOrMalformedFiles(t *testing.T) {
	for name, contents := range map[string]string{
		"malformed":     "{not json",
		"missing-field": `{"other":true}`,
		"empty-object":  `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			userData := t.TempDir()
			path := filepath.Join(userData, "external-mcp", mcpSharingFile)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("create the sharing directory: %v", err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("write the sharing file: %v", err)
			}
			if !newMCPLiveSessions(nil, userData, nil).SharingEnabled() {
				t.Fatalf("%s disabled sharing; the fail-safe default is on", name)
			}
		})
	}
}

// TestMCPLiveSessionSwitchDeniesScopeThroughTheHandler proves the switch is
// enforced by the policy path, not only rendered in Settings.
func TestMCPLiveSessionSwitchDeniesScopeThroughTheHandler(t *testing.T) {
	userData := t.TempDir()
	sessions := appsession.NewService(writeTestFactory{ssh: newWriteTestTransport()})
	t.Cleanup(sessions.CloseAll)
	session, err := sessions.OpenLocal(context.Background())
	if err != nil {
		t.Fatalf("open the local shell: %v", err)
	}
	provider := newMCPLiveSessions(sessions, userData, nil)
	policy := appmcp.NewService(appmcp.Config{
		ForcedChatSessionID: "__external_mcp__",
		Sessions:            provider,
		Mode:                func() appmcp.Mode { return appmcp.ModeAuto },
	})
	handler := newMCPPolicyHandler(newMCPReadBridge(nil, nil, sessions, nil),
		policy, sessions, sessions, nil)
	call := func() map[string]any {
		tool, ok := catalog.ByName("terminal_execute")
		if !ok {
			t.Fatal("the catalog has no terminal_execute")
		}
		result, err := handler.Handle(context.Background(), tool.RPCMethod,
			json.RawMessage(`{"sessionId":"`+string(session.ID())+`","command":"echo scope"}`))
		if err != nil {
			t.Fatalf("handle: %v", err)
		}
		return result.(map[string]any)
	}
	if code := mcpLiveScopeResultCode(t, call()); code != "mcp_tool_unavailable" &&
		code != "" {
		// The fixture has no write executor, so an in-scope call falls through
		// to the typed "not available" answer; an out-of-scope call must not.
		t.Fatalf("in-scope code = %q", code)
	}
	if err := provider.SetEnabled(false); err != nil {
		t.Fatalf("turn sharing off: %v", err)
	}
	if code := mcpLiveScopeResultCode(t, call()); code != "mcp_session_out_of_scope" {
		t.Fatalf("out-of-scope code = %q, want mcp_session_out_of_scope", code)
	}
}

func mcpLiveScopeResultCode(t *testing.T, result map[string]any) string {
	t.Helper()
	code, _ := result["code"].(string)
	return code
}

// TestMCPLiveSessionFailedSaveKeepsThePreviousSwitch pins the ordering: the
// switch is published only after the preference is durable, so a failed save
// cannot leave the UI and the enforcing policy disagreeing.
func TestMCPLiveSessionFailedSaveKeepsThePreviousSwitch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := newMCPLiveSessions(nil, file, nil)
	if !provider.SharingEnabled() {
		t.Fatal("the default switch must be on")
	}
	if err := provider.SetEnabled(false); err == nil {
		t.Fatal("saving under a regular file must fail")
	}
	if !provider.SharingEnabled() {
		t.Fatal("a failed save must keep the previous switch")
	}
}
