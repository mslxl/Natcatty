package vault

import (
	"testing"

	transportmodel "github.com/mslxl/natcatty/domain/model/transport"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/locales"
)

// TestHostEditorOffersTheBetaAndRepairsAnUnknownChoice covers the per-Host
// half of the beta rollout: the in-process client is selectable and labelled
// as beta, and an unknown value written by another build is called out and can
// be repaired from the same control.
func TestHostEditorOffersTheBetaAndRepairsAnUnknownChoice(t *testing.T) {
	m := locales.In("en")
	state, hosts, _, _, _, tt := newEntityView(t)
	hosts.items[0].Protocol = domainvault.ProtocolMosh
	hosts.items[0].TransportImplementation = transportmodel.Implementation("future_client")
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit rejected the Mosh Host")
	}
	tt.Frame()

	if !tt.HasText(m.VaultHostTransportInProcess()) {
		t.Fatalf("the per-Host beta choice is missing: %v", tt.Texts())
	}
	if !tt.HasText(m.VaultHostTransportUnknown()) {
		t.Fatalf("the unknown per-Host value was not called out: %v", tt.Texts())
	}

	if err := tt.Click(m.VaultHostTransportInProcess()); err != nil {
		t.Fatalf("click in-process: %v", err)
	}
	tt.Frame()
	if got := state.Form().TransportImplementation; got != string(transportmodel.InProcess) {
		t.Fatalf("form implementation = %q, want in_process", got)
	}
	if tt.HasText(m.VaultHostTransportUnknown()) {
		t.Fatalf("the unknown warning survived the repair: %v", tt.Texts())
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tt.Frame()
	if got := hosts.items[0].TransportImplementation; got != transportmodel.InProcess {
		t.Fatalf("saved implementation = %q, want in_process", got)
	}
}

// TestHostEditorKeepsBundledSelectableForRollback proves the per-Host rollback
// path survives the beta, including while the global release default is the
// in-process client.
func TestHostEditorKeepsBundledSelectableForRollback(t *testing.T) {
	m := locales.In("en")
	state, hosts, _, _, _, tt := newEntityView(t)
	hosts.items[0].Protocol = domainvault.ProtocolET
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit rejected the ET Host")
	}
	tt.Frame()

	if !tt.HasText(m.VaultHostTransportInherit()) || !tt.HasText(m.VaultHostTransportBundled()) {
		t.Fatalf("the per-Host inheritance and rollback choices are missing: %v", tt.Texts())
	}
	if err := tt.Click(m.VaultHostTransportBundled()); err != nil {
		t.Fatalf("click bundled: %v", err)
	}
	tt.Frame()
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	tt.Frame()
	if got := hosts.items[0].TransportImplementation; got != transportmodel.Bundled {
		t.Fatalf("saved implementation = %q, want the bundled rollback choice", got)
	}
}
