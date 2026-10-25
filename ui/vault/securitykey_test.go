package vault

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

func waitForVaultText(t *testing.T, tt *ui.Tester, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tt.HasText(want) {
			return
		}
		tt.Frame()
	}
	t.Fatalf("the Vault never showed %q; texts %q", want, tt.Texts())
}

func securityKeyEditor(t *testing.T, summary appconnection.SecurityKeySummary) (*vaultstate.State, *ui.Tester) {
	t.Helper()
	state := vaultstate.NewState(&fakeHosts{},
		vaultstate.WithSecurityKeyProbe(func(context.Context) appconnection.SecurityKeySummary {
			return summary
		}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	tt := ui.NewTester(View(state), 900, 1800)
	state.BeginCreate()
	state.Form().UseSSHAgent = true
	tt.Frame()
	return state, tt
}

func TestVaultViewShowsTheSecurityKeyAgentReadyState(t *testing.T) {
	m := locales.In("en")
	state, tt := securityKeyEditor(t, appconnection.SecurityKeySummary{Available: true, Count: 2})
	want := m.VaultHostSecurityKeyReady(2)
	waitForVaultText(t, tt, want)

	// The hint belongs to the agent option; turning it off hides it again.
	state.Form().UseSSHAgent = false
	tt.Frame()
	if tt.HasText(want) {
		t.Fatalf("the security key hint survived disabling the agent: %q", tt.Texts())
	}
}

func TestVaultViewShowsTheEmptyAgentState(t *testing.T) {
	m := locales.In("en")
	_, tt := securityKeyEditor(t, appconnection.SecurityKeySummary{Available: true})
	waitForVaultText(t, tt, m.VaultHostSecurityKeyEmpty())
}

func TestVaultViewShowsTheMissingAgentState(t *testing.T) {
	m := locales.In("en")
	_, tt := securityKeyEditor(t, appconnection.SecurityKeySummary{})
	waitForVaultText(t, tt, m.VaultHostSecurityKeyUnavailable())
}
