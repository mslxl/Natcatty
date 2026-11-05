package state

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/wireguardconf"
)

var errTestRead = errors.New("read failed")

func confKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

func importConf() string {
	return "[Interface]\n" +
		"PrivateKey = " + confKey(1) + "\n" +
		"Address = 10.0.0.2/32, fd00::2\n" +
		"DNS = 10.0.0.1, corp.example\n" +
		"MTU = 1280\n" +
		"ListenPort = 51820\n" +
		"FwMark = 0x1f\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + confKey(2) + "\n" +
		"PresharedKey = " + confKey(3) + "\n" +
		"AllowedIPs = 10.0.0.0/24, 10.0.0.1/24\n" +
		"Endpoint = vpn.example.com:51820\n" +
		"PersistentKeepalive = 25\n"
}

type fakeWireGuardConfPicker struct {
	name    string
	content []byte
	err     error
	calls   int
}

func (f *fakeWireGuardConfPicker) PickWireGuardConf() (string, []byte, error) {
	f.calls++
	return f.name, f.content, f.err
}

func newNetworkImportState(profiles *fakeNetworkProfileService,
	picker *fakeWireGuardConfPicker) *State {

	options := []Option{WithNetworkProfiles(profiles)}
	if picker != nil {
		options = append(options, WithWireGuardConfPicker(picker))
	}
	state := NewState(&fakeHosts{}, options...)
	if err := state.Reload(); err != nil {
		panic(err)
	}
	return state
}

func TestNetworkConfImportPreviewsConfirmsAndSavesANormalizedDraft(t *testing.T) {
	service := &fakeNetworkProfileService{}
	picker := &fakeWireGuardConfPicker{name: `C:\configs\wg0.conf`, content: []byte(importConf())}
	state := newNetworkImportState(service, picker)

	if !state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard) {
		t.Fatalf("BeginCreateNetworkProfile: %v", state.Problem())
	}
	form := state.NetworkProfileForm()
	form.Label = "wg"
	form.PrivateKey = "manual"
	manualPrivateKey := form.PrivateKey

	if !state.ChooseNetworkConfFile() {
		t.Fatalf("ChooseNetworkConfFile: %v", state.NetworkImportProblem())
	}
	preview := state.NetworkImportPreview()
	if preview == nil {
		t.Fatal("no preview after a successful parse")
	}
	if preview.FileName != "wg0.conf" {
		t.Fatalf("FileName = %q", preview.FileName)
	}
	if len(preview.Diagnostics) < 2 {
		t.Fatalf("diagnostics = %+v", preview.Diagnostics)
	}
	// Nothing is stored and the form is untouched before confirmation.
	if len(service.created) != 0 {
		t.Fatalf("an unconfirmed import saved a draft: %+v", service.created)
	}
	if state.NetworkProfileForm().PrivateKey != manualPrivateKey {
		t.Fatal("a successful parse modified the form before confirmation")
	}
	if state.NetworkImportProblem() != ProblemNone {
		t.Fatalf("problem = %q", state.NetworkImportProblem())
	}

	if !state.ConfirmNetworkImport() {
		t.Fatal("ConfirmNetworkImport returned false")
	}
	if state.NetworkImportPreview() != nil {
		t.Fatal("the preview survived confirmation")
	}
	form = state.NetworkProfileForm()
	wantPrivateKey := confKey(1)
	if form.PrivateKey != wantPrivateKey {
		t.Fatalf("PrivateKey = %q, want %q", form.PrivateKey, wantPrivateKey)
	}
	if form.Addresses != "10.0.0.2/32, fd00::2/128" {
		t.Fatalf("Addresses = %q", form.Addresses)
	}
	if form.DNS != "10.0.0.1" {
		t.Fatalf("DNS = %q, want only the IP server", form.DNS)
	}
	if form.MTU != "1280" || form.ListenPort != "51820" {
		t.Fatalf("MTU/ListenPort = %q/%q", form.MTU, form.ListenPort)
	}
	if len(form.Peers) != 1 {
		t.Fatalf("peers = %+v", form.Peers)
	}
	peer := form.Peers[0]
	if peer.PublicKey != confKey(2) || peer.PresharedKey != confKey(3) ||
		peer.Endpoint != "vpn.example.com:51820" ||
		peer.AllowedIPs != "10.0.0.0/24, 10.0.0.0/24" || peer.PersistentKeepalive != "25" {
		t.Fatalf("peer = %+v", peer)
	}
	// Confirmation alone still stores nothing.
	if len(service.created) != 0 {
		t.Fatalf("confirmation saved a draft: %+v", service.created)
	}

	// The manual form keeps working after the import: edit, add, and remove.
	form.Peers[0].Endpoint = "vpn2.example.com:51820"
	state.AddNetworkPeer()
	form = state.NetworkProfileForm()
	form.Peers[1].PublicKey = confKey(4)
	form.Peers[1].AllowedIPs = "10.9.0.0/16"
	state.RemoveNetworkPeer(2)
	if len(state.NetworkProfileForm().Peers) != 2 {
		t.Fatalf("peer editing produced %d rows", len(state.NetworkProfileForm().Peers))
	}
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.created) != 1 {
		t.Fatalf("created = %+v", service.created)
	}
	draft := service.created[0]
	if draft.Kind != domainvault.NetworkKindWireGuard || draft.Label != "wg" ||
		draft.WireGuard == nil {
		t.Fatalf("draft = %+v", draft)
	}
	if draft.WireGuard.PrivateKey != wantPrivateKey || draft.WireGuard.MTU != 1280 ||
		draft.WireGuard.ListenPort != 51820 {
		t.Fatalf("WireGuard draft = %+v", draft.WireGuard)
	}
	if len(draft.WireGuard.Peers) != 2 ||
		draft.WireGuard.Peers[0].Endpoint != "vpn2.example.com:51820" ||
		draft.WireGuard.Peers[1].AllowedIPs[0] != "10.9.0.0/16" {
		t.Fatalf("peers = %+v", draft.WireGuard.Peers)
	}

	// Revalidation still rejects a malformed edit before storage.
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	state.NetworkProfileForm().Label = "wg"
	state.NetworkProfileForm().Addresses = "10.0.0.2/32"
	state.NetworkProfileForm().MTU = "not-a-number"
	state.NetworkProfileForm().Peers[0].PublicKey = confKey(2)
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if state.Problem() != ProblemNetworkMTU || len(service.created) != 1 {
		t.Fatalf("problem = %q, created = %d", state.Problem(), len(service.created))
	}
}

func TestNetworkConfImportFailureNeverTouchesTheForm(t *testing.T) {
	service := &fakeNetworkProfileService{}
	picker := &fakeWireGuardConfPicker{
		name:    "broken.conf",
		content: []byte("[Interface]\nPrivateKey = SUPER-SECRET\nAddress = 10.0.0.2\n"),
	}
	state := newNetworkImportState(service, picker)
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	form := state.NetworkProfileForm()
	form.Label = "manual"
	form.PrivateKey = confKey(5)
	form.Addresses = "10.5.0.2/32"
	form.Peers[0].PublicKey = confKey(6)
	form.Peers[0].AllowedIPs = "10.5.0.0/24"
	before := *form
	beforePeers := append([]NetworkPeerForm(nil), form.Peers...)

	if state.ChooseNetworkConfFile() {
		t.Fatal("ChooseNetworkConfFile accepted an invalid file")
	}
	if state.NetworkImportPreview() != nil {
		t.Fatal("a failed parse left a preview")
	}
	if state.NetworkImportProblem() != ProblemNetworkImportParse {
		t.Fatalf("problem = %q", state.NetworkImportProblem())
	}
	parseErr := state.NetworkImportError()
	if parseErr == nil || parseErr.Line != 2 || parseErr.Field != "PrivateKey" ||
		parseErr.Kind != wireguardconf.ErrSyntax {
		t.Fatalf("parse error = %+v", parseErr)
	}
	if state.NetworkProfileForm().Label != before.Label ||
		state.NetworkProfileForm().PrivateKey != before.PrivateKey ||
		state.NetworkProfileForm().Addresses != before.Addresses ||
		len(state.NetworkProfileForm().Peers) != len(beforePeers) ||
		state.NetworkProfileForm().Peers[0].PublicKey != beforePeers[0].PublicKey {
		t.Fatalf("a failed parse modified the form: %+v", state.NetworkProfileForm())
	}
	if state.ConfirmNetworkImport() {
		t.Fatal("ConfirmNetworkImport accepted a failed parse")
	}
	// Saving afterwards writes the untouched manual form, not a partial import.
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.created) != 1 || service.created[0].WireGuard.PrivateKey != confKey(5) {
		t.Fatalf("created = %+v", service.created)
	}
}

func TestNetworkConfImportCancelDiscardsThePreview(t *testing.T) {
	service := &fakeNetworkProfileService{}
	picker := &fakeWireGuardConfPicker{name: "wg.conf", content: []byte(importConf())}
	state := newNetworkImportState(service, picker)
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	state.NetworkProfileForm().PrivateKey = confKey(7)

	if !state.ChooseNetworkConfFile() {
		t.Fatalf("ChooseNetworkConfFile: %v", state.NetworkImportProblem())
	}
	state.CancelNetworkImport()
	if state.NetworkImportPreview() != nil || state.NetworkImportError() != nil ||
		state.NetworkImportProblem() != ProblemNone {
		t.Fatalf("cancel left import state: %+v", state.NetworkImportPreview())
	}
	if state.NetworkProfileForm().PrivateKey != confKey(7) {
		t.Fatal("cancel modified the form")
	}
	if len(service.created) != 0 {
		t.Fatal("cancel saved a draft")
	}
}

func TestNetworkConfImportLimitsAndPickerFailures(t *testing.T) {
	service := &fakeNetworkProfileService{}
	t.Run("oversized content", func(t *testing.T) {
		picker := &fakeWireGuardConfPicker{
			name:    "big.conf",
			content: bytes.Repeat([]byte("a"), wireguardconf.MaxFileBytes+1),
		}
		state := newNetworkImportState(service, picker)
		state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
		if state.ChooseNetworkConfFile() {
			t.Fatal("oversized file was accepted")
		}
		if state.NetworkImportProblem() != ProblemNetworkImportTooLarge || state.NetworkImportPreview() != nil {
			t.Fatalf("problem = %q", state.NetworkImportProblem())
		}
	})
	t.Run("picker size rejection", func(t *testing.T) {
		picker := &fakeWireGuardConfPicker{err: ErrNetworkConfTooLarge}
		state := newNetworkImportState(service, picker)
		state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
		if state.ChooseNetworkConfFile() {
			t.Fatal("picker error was ignored")
		}
		if state.NetworkImportProblem() != ProblemNetworkImportTooLarge {
			t.Fatalf("problem = %q", state.NetworkImportProblem())
		}
	})
	t.Run("picker read error", func(t *testing.T) {
		picker := &fakeWireGuardConfPicker{err: errTestRead}
		state := newNetworkImportState(service, picker)
		state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
		if state.ChooseNetworkConfFile() {
			t.Fatal("picker error was ignored")
		}
		if state.NetworkImportProblem() != ProblemNetworkImportFile {
			t.Fatalf("problem = %q", state.NetworkImportProblem())
		}
	})
	t.Run("picker cancel keeps the editor", func(t *testing.T) {
		picker := &fakeWireGuardConfPicker{}
		state := newNetworkImportState(service, picker)
		state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
		state.NetworkProfileForm().PrivateKey = "manual"
		if state.ChooseNetworkConfFile() {
			t.Fatal("a cancelled picker reported success")
		}
		if state.NetworkImportProblem() != ProblemNone ||
			state.NetworkProfileForm().PrivateKey != "manual" {
			t.Fatalf("cancel changed state: problem %q", state.NetworkImportProblem())
		}
	})
	t.Run("no picker", func(t *testing.T) {
		state := newNetworkImportState(service, nil)
		state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
		if state.ChooseNetworkConfFile() {
			t.Fatal("import succeeded without a picker")
		}
		if state.NetworkImportProblem() != ProblemNetworkImportUnavailable {
			t.Fatalf("problem = %q", state.NetworkImportProblem())
		}
	})
}

func TestNetworkConfImportRequiresTheWireGuardEditor(t *testing.T) {
	service := &fakeNetworkProfileService{}
	picker := &fakeWireGuardConfPicker{name: "wg.conf", content: []byte(importConf())}
	state := newNetworkImportState(service, picker)

	if state.ChooseNetworkConfFile() {
		t.Fatal("import ran with no editor open")
	}
	state.BeginCreateNetworkProfile(domainvault.NetworkKindTailscale)
	if state.ChooseNetworkConfFile() {
		t.Fatal("import ran for a Tailscale profile")
	}
	if picker.calls != 0 {
		t.Fatalf("picker was called %d times", picker.calls)
	}
}

func TestNetworkConfImportPeerWithoutEndpointStaysSaveable(t *testing.T) {
	service := &fakeNetworkProfileService{}
	picker := &fakeWireGuardConfPicker{
		name: "wg.conf",
		content: []byte("[Interface]\nPrivateKey = " + confKey(1) +
			"\nAddress = 10.0.0.2\n[Peer]\nPublicKey = " + confKey(2) +
			"\nAllowedIPs = 10.0.0.0/24\n"),
	}
	state := newNetworkImportState(service, picker)
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	if !state.ChooseNetworkConfFile() || !state.ConfirmNetworkImport() {
		t.Fatalf("import failed: %v", state.NetworkImportProblem())
	}
	if got := state.NetworkProfileForm().Peers[0].Endpoint; got != "" {
		t.Fatalf("Endpoint = %q, want an empty optional endpoint", got)
	}
	state.NetworkProfileForm().Label = "wg"
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.created) != 1 || service.created[0].WireGuard.Peers[0].Endpoint != "" {
		t.Fatalf("created = %+v", service.created)
	}
}

func TestNetworkConfImportEmptyPeerListStaysEditable(t *testing.T) {
	service := &fakeNetworkProfileService{}
	picker := &fakeWireGuardConfPicker{
		name: "wg.conf",
		content: []byte("[Interface]\nPrivateKey = " + confKey(1) +
			"\nAddress = 10.0.0.2\n"),
	}
	state := newNetworkImportState(service, picker)
	state.BeginCreateNetworkProfile(domainvault.NetworkKindWireGuard)
	if !state.ChooseNetworkConfFile() || !state.ConfirmNetworkImport() {
		t.Fatalf("import failed: %v", state.NetworkImportProblem())
	}
	if len(state.NetworkProfileForm().Peers) != 0 {
		t.Fatalf("peers = %+v", state.NetworkProfileForm().Peers)
	}
	state.AddNetworkPeer()
	form := state.NetworkProfileForm()
	form.Label = "wg"
	form.Peers[0].PublicKey = confKey(2)
	form.Peers[0].AllowedIPs = "0.0.0.0/0"
	if err := state.SaveNetworkProfile(); err != nil {
		t.Fatalf("SaveNetworkProfile: %v", err)
	}
	if len(service.created) != 1 || len(service.created[0].WireGuard.Peers) != 1 {
		t.Fatalf("created = %+v", service.created)
	}
}
