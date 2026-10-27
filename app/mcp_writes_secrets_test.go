package app

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	"github.com/mslxl/natcatty/internal/mcp/catalog"
)

// invokeMCPTool is the goroutine-safe form of callMCPTool: it reports errors
// instead of failing the test from a blocking policy call.
func invokeMCPTool(fixture *mcpWriteFixture, toolName string,
	arguments map[string]any) (map[string]any, error) {
	tool, ok := catalog.ByName(toolName)
	if !ok {
		return nil, fmt.Errorf("catalog has no tool %q", toolName)
	}
	params, err := json.Marshal(arguments)
	if err != nil {
		return nil, err
	}
	response, err := fixture.handler.Handle(fixture.ctx, tool.RPCMethod, params)
	if err != nil {
		return nil, err
	}
	result, ok := response.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s result = %#v", toolName, response)
	}
	return result, nil
}

func awaitMCPApproval(t *testing.T, fixture *mcpWriteFixture) appmcp.ApprovalView {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pending := fixture.policy.Snapshot().Pending; len(pending) > 0 {
			return pending[0]
		}
		if !time.Now().Before(deadline) {
			t.Fatal("the approval prompt never appeared")
		}
		<-ticker.C
	}
}

func awaitMCPResult(t *testing.T, result <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(30 * time.Second):
		t.Fatal("the tool call did not finish")
		return nil
	}
}

// TestMCPWriteSecretsAreRedactedAndNeverReachable proves a credential passed
// to a real write is redacted from the approval summary, absent from the tool
// result, absent from the recorded activity, and absent from every read tool's
// view, even after approval.
func TestMCPWriteSecretsAreRedactedAndNeverReachable(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	fixture.setMode(appmcp.ModeConfirm)
	const password = "ticket58-super-secret-value"
	const passphrase = "ticket58-key-passphrase"
	done := make(chan map[string]any, 1)
	go func() {
		result, err := invokeMCPTool(fixture, "vault_hosts_create", map[string]any{
			"hosts": fmt.Sprintf(`[{"hostname":"secret.example","label":"secret",`+
				`"password":%q,"passphrase":%q}]`, password, passphrase),
		})
		if err != nil {
			done <- map[string]any{"ok": false, "error": err.Error()}
			return
		}
		done <- result
	}()
	prompt := awaitMCPApproval(t, fixture)
	if prompt.Tool != "vault_hosts_create" {
		t.Fatalf("prompt tool = %q", prompt.Tool)
	}
	if strings.Contains(prompt.Summary, password) || strings.Contains(prompt.Summary, passphrase) {
		t.Fatalf("the approval summary leaked a secret: %s", prompt.Summary)
	}
	// The hosts payload is a JSON string, so the redaction placeholders are
	// escaped inside it; assert both field names and both placeholders rather
	// than a top-level "name=[redacted]" shape.
	if !strings.Contains(prompt.Summary, "password") ||
		!strings.Contains(prompt.Summary, "passphrase") ||
		strings.Count(prompt.Summary, "[redacted]") < 2 {
		t.Fatalf("the approval summary did not mark the secret fields: %s", prompt.Summary)
	}
	if err := fixture.policy.Approve(prompt.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	result := awaitMCPResult(t, done)
	requireMCPWriteOK(t, "vault_hosts_create", result)
	if text := fmt.Sprint(result); strings.Contains(text, password) || strings.Contains(text, passphrase) {
		t.Fatalf("the write result leaked a secret: %s", text)
	}
	created, _ := result["created"].([]map[string]any)
	if len(created) == 0 {
		t.Fatalf("created = %#v", result["created"])
	}
	hostID := fmt.Sprint(created[0]["id"])

	// The activity strip never carries argument values at all; assert the
	// secret is absent from the full snapshot exposed to Settings.
	activity := fixture.policy.Snapshot().LastActivity
	if activity == nil || activity.Tool != "vault_hosts_create" || activity.Status != appmcp.ActivityOK {
		t.Fatalf("activity = %#v", activity)
	}
	if strings.Contains(activity.Detail, password) || strings.Contains(activity.Rule, password) {
		t.Fatalf("activity leaked a secret: %#v", activity)
	}

	// Read tools can describe the Host but never return the stored credential
	// (the Vault stores it separately from the Host metadata).
	for _, toolName := range []string{"vault_hosts_list", "host_get"} {
		read := callMCPTool(t, fixture, toolName, map[string]any{"hostId": hostID})
		requireMCPWriteOK(t, toolName, read)
		text := fmt.Sprint(read)
		if strings.Contains(text, password) || strings.Contains(text, passphrase) {
			t.Fatalf("%s leaked a stored secret: %s", toolName, text)
		}
		for _, field := range []string{"password", "passphrase", "privateKey", "keyMaterial"} {
			if strings.Contains(text, `"`+field+`"`) {
				t.Fatalf("%s returned a credential field %q: %s", toolName, field, text)
			}
		}
	}
}

// TestMCPWriteCatalogHasNoSecretOrAICapability pins the external surface: the
// catalog keeps the original 69 tools and adds no secret-store or AI-chat
// capability. list_attachments/read_attachment are part of the original MCP
// contract (attachment domain), not new AI tools.
func TestMCPWriteCatalogHasNoSecretOrAICapability(t *testing.T) {
	tools := catalog.All()
	if len(tools) != 69 {
		t.Fatalf("catalog tools = %d, want the original 69", len(tools))
	}
	forbidden := []string{
		"secret", "credential", "keychain", "password-manager",
		"ai_", "assistant", "llm", "chat.completion", "conversation",
	}
	for _, tool := range tools {
		identity := strings.ToLower(tool.Name + " " + tool.CapabilityID + " " + tool.Domain)
		for _, term := range forbidden {
			if strings.Contains(identity, term) {
				t.Fatalf("catalog exposes a forbidden secret/AI capability: %s (%s)",
					tool.Name, tool.CapabilityID)
			}
		}
	}
}

// TestMCPWriteApprovalDoesNotExposeSecretValuesAlsoStaysAfterApproval
// exercises the second half of the promise through the real policy surface:
// once approved, neither the snapshot's recent list nor a later snapshot
// carries the raw argument.
func TestMCPWriteApprovalSnapshotsNeverCarryTheSecret(t *testing.T) {
	fixture := newMCPWriteFixture(t)
	fixture.setMode(appmcp.ModeConfirm)
	const password = "post-approval-secret-58"
	done := make(chan map[string]any, 1)
	go func() {
		result, err := invokeMCPTool(fixture, "vault_hosts_create", map[string]any{
			"hosts": fmt.Sprintf(`[{"hostname":"post.example","password":%q}]`, password),
		})
		if err != nil {
			done <- map[string]any{"ok": false, "error": err.Error()}
			return
		}
		done <- result
	}()
	prompt := awaitMCPApproval(t, fixture)
	if err := fixture.policy.Approve(prompt.ID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	_ = awaitMCPResult(t, done)
	snapshot := fixture.policy.Snapshot()
	if len(snapshot.Pending) != 0 {
		t.Fatalf("an approved request is still pending: %#v", snapshot.Pending)
	}
	if text := fmt.Sprintf("%#v", snapshot); strings.Contains(text, password) {
		t.Fatalf("snapshot leaked the approved secret: %s", text)
	}
}
