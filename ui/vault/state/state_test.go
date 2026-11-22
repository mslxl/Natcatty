package state

import (
	"context"
	"errors"
	"strconv"
	"testing"

	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// fakeHosts is the application seam the Vault state is tested against.
type fakeHosts struct {
	items   []domainvault.Host
	created []domainvault.Draft
	updated []domainvault.Draft
	deleted []model.HostID
	err     error
	nextID  int
}

func (f *fakeHosts) List(context.Context) ([]domainvault.Host, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]domainvault.Host(nil), f.items...), nil
}

func (f *fakeHosts) Create(_ context.Context, draft domainvault.Draft) (domainvault.Host, error) {
	if f.err != nil {
		return domainvault.Host{}, f.err
	}
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	f.nextID++
	host := domainvault.Host{
		ID:                     model.HostID("host-" + strconv.Itoa(f.nextID)),
		Label:                  normalized.Label,
		Hostname:               normalized.Hostname,
		Port:                   normalized.Port,
		Username:               normalized.Username,
		Protocol:               normalized.Protocol,
		Transport:              normalized.Transport,
		Group:                  normalized.Group,
		Tags:                   normalized.Tags,
		Notes:                  normalized.Notes,
		Pinned:                 normalized.Pinned,
		StartupCommand:         normalized.StartupCommand,
		IdentityID:             normalized.IdentityID,
		KeyID:                  normalized.KeyID,
		AuthMethod:             normalized.AuthMethod,
		UseSSHAgent:            normalized.UseSSHAgent,
		RequiresMFA:            normalized.RequiresMFA,
		X11Forwarding:          normalized.X11Forwarding,
		X11WarningAcknowledged: normalized.X11WarningAcknowledged,
		HostChain:              domainvault.CloneHostChain(normalized.HostChain),
		Proxy:                  domainvault.CloneProxy(normalized.Proxy),
		ProxyProfileID:         normalized.ProxyProfileID,
		DirectProxy:            normalized.DirectProxy,
		NetworkProfileID:       normalized.NetworkProfileID,
		LegacyAlgorithms:       normalized.LegacyAlgorithms,
		SkipEcdsaHostKey:       normalized.SkipEcdsaHostKey,
		Algorithms:             domainvault.CloneAlgorithms(normalized.Algorithms),
		DeviceType:             normalized.DeviceType,
		AutoAttachTmux:         normalized.AutoAttachTmux,
		NewlineMode:            normalized.NewlineMode,
		BackspaceMode:          normalized.BackspaceMode,
		LineEditingMode:        normalized.LineEditingMode,
		MoshServerPath:         normalized.MoshServerPath,
		ETPort:                 normalized.ETPort,
		VNC:                    domainvault.CloneVNCConfig(normalized.VNC),
		OrderKey:               int64(f.nextID) * 1000,
	}
	f.created = append(f.created, draft)
	f.items = append(f.items, host)
	return host, nil
}

func (f *fakeHosts) Update(_ context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error) {
	if f.err != nil {
		return domainvault.Host{}, f.err
	}
	normalized, err := domainvault.NormalizeDraft(draft)
	if err != nil {
		return domainvault.Host{}, &appvault.Error{Kind: appvault.KindValidation, Cause: err}
	}
	for i, host := range f.items {
		if host.ID != id {
			continue
		}
		host.Label = normalized.Label
		host.Hostname = normalized.Hostname
		host.Port = normalized.Port
		host.Username = normalized.Username
		host.Protocol = normalized.Protocol
		host.Transport = normalized.Transport
		host.Group = normalized.Group
		host.Tags = normalized.Tags
		host.Notes = normalized.Notes
		host.Pinned = normalized.Pinned
		host.StartupCommand = normalized.StartupCommand
		host.IdentityID = normalized.IdentityID
		host.KeyID = normalized.KeyID
		host.AuthMethod = normalized.AuthMethod
		host.UseSSHAgent = normalized.UseSSHAgent
		host.RequiresMFA = normalized.RequiresMFA
		host.X11Forwarding = normalized.X11Forwarding
		host.X11WarningAcknowledged = normalized.X11WarningAcknowledged
		host.X11ForwardingSet = normalized.X11ForwardingSet
		host.HostChain = domainvault.CloneHostChain(normalized.HostChain)
		host.Proxy = domainvault.CloneProxy(normalized.Proxy)
		host.ProxyProfileID = normalized.ProxyProfileID
		host.DirectProxy = normalized.DirectProxy
		host.NetworkProfileID = normalized.NetworkProfileID
		host.LegacyAlgorithms = normalized.LegacyAlgorithms
		host.SkipEcdsaHostKey = normalized.SkipEcdsaHostKey
		host.Algorithms = domainvault.CloneAlgorithms(normalized.Algorithms)
		host.DeviceType = normalized.DeviceType
		host.AutoAttachTmux = normalized.AutoAttachTmux
		host.NewlineMode = normalized.NewlineMode
		host.BackspaceMode = normalized.BackspaceMode
		host.LineEditingMode = normalized.LineEditingMode
		host.MoshServerPath = normalized.MoshServerPath
		host.ETPort = normalized.ETPort
		host.VNC = domainvault.CloneVNCConfig(normalized.VNC)
		f.items[i] = host
		f.updated = append(f.updated, draft)
		return host, nil
	}
	return domainvault.Host{}, appvault.ErrNotFound
}

func (f *fakeHosts) Delete(_ context.Context, id model.HostID) error {
	if f.err != nil {
		return f.err
	}
	for i, host := range f.items {
		if host.ID == id {
			f.items = append(f.items[:i], f.items[i+1:]...)
			f.deleted = append(f.deleted, id)
			return nil
		}
	}
	return appvault.ErrNotFound
}

func newTestState(t *testing.T, hosts *fakeHosts) (*State, *int) {
	t.Helper()
	redraws := new(int)
	state := NewState(hosts, WithRedrawRequest(func() { *redraws++ }))
	return state, redraws
}

func TestReloadListsHosts(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "a", Label: "alpha"}, {ID: "b", Label: "beta"}}}
	state, redraws := newTestState(t, repo)

	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if state.HostCount() != 2 || state.Items()[0].ID != "a" {
		t.Fatalf("items = %+v, want two Hosts", state.Items())
	}
	if *redraws != 1 {
		t.Fatalf("redraws = %d, want 1", *redraws)
	}
}

func TestCreateFlowSavesAndReloads(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	if !state.Editing() {
		t.Fatal("BeginCreate did not open the editor")
	}
	form := state.Form()
	form.Label = "web"
	form.Hostname = "example.com"
	form.Username = "root"
	form.Group = "prod/web"
	form.Tags = "prod, web"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Editing() {
		t.Fatal("Save left the editor open")
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("Problem = %q, want none", state.Problem())
	}
	if len(repo.created) != 1 {
		t.Fatalf("created drafts = %d, want 1", len(repo.created))
	}
	if state.HostCount() != 1 {
		t.Fatalf("Hosts after Save = %d, want 1", state.HostCount())
	}
}

func TestHostDetailsRoundTripsInputCompatibility(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	form := state.Form()
	form.Label = "legacy-shell"
	form.Hostname = "example.com"
	form.Username = "root"
	form.NewlineMode = string(modelterminal.NewlineCRLF)
	form.BackspaceMode = string(modelterminal.BackspaceCtrlH)
	form.LineEditingMode = string(modelterminal.LineEditingByte)

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("created drafts = %d, want 1", len(repo.created))
	}
	created := repo.created[0]
	if created.NewlineMode != modelterminal.NewlineCRLF ||
		created.BackspaceMode != modelterminal.BackspaceCtrlH ||
		created.LineEditingMode != modelterminal.LineEditingByte {
		t.Fatalf("created draft input modes = %q/%q/%q, want crlf/ctrl-h/byte",
			created.NewlineMode, created.BackspaceMode, created.LineEditingMode)
	}
	saved := state.Items()[0]
	if saved.NewlineMode != modelterminal.NewlineCRLF ||
		saved.BackspaceMode != modelterminal.BackspaceCtrlH ||
		saved.LineEditingMode != modelterminal.LineEditingByte {
		t.Fatalf("saved Host input modes = %q/%q/%q, want crlf/ctrl-h/byte",
			saved.NewlineMode, saved.BackspaceMode, saved.LineEditingMode)
	}

	if !state.BeginEdit(saved.ID) {
		t.Fatal("BeginEdit failed")
	}
	restored := state.Form()
	if restored.NewlineMode != string(modelterminal.NewlineCRLF) ||
		restored.BackspaceMode != string(modelterminal.BackspaceCtrlH) ||
		restored.LineEditingMode != string(modelterminal.LineEditingByte) {
		t.Fatalf("editor input modes = %q/%q/%q, want crlf/ctrl-h/byte",
			restored.NewlineMode, restored.BackspaceMode, restored.LineEditingMode)
	}
}

func TestHostDetailsClearsInputCompatibilityWhenLeavingSSH(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	form := state.Form()
	form.Label = "console"
	form.Hostname = "console.example.com"
	form.Protocol = string(domainvault.ProtocolTelnet)
	form.NewlineMode = string(modelterminal.NewlineLF)
	form.BackspaceMode = string(modelterminal.BackspaceCtrlH)
	form.LineEditingMode = string(modelterminal.LineEditingByte)

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("created drafts = %d, want 1", len(repo.created))
	}
	saved := state.Items()[0]
	if saved.NewlineMode != modelterminal.NewlineCR ||
		saved.BackspaceMode != modelterminal.BackspaceDEL ||
		saved.LineEditingMode != modelterminal.LineEditingCharacter {
		t.Fatalf("non-SSH Host input modes = %q/%q/%q, want defaults",
			saved.NewlineMode, saved.BackspaceMode, saved.LineEditingMode)
	}
}

func TestX11ForwardingIsOffByDefaultAndNeedsTheWarningAcknowledgement(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()

	form := state.Form()
	if form.X11Forwarding || form.X11WarningAcknowledged {
		t.Fatalf("new Host form X11 fields = %+v, want both off", form)
	}
	form.Label = "gui"
	form.Hostname = "example.com"
	form.X11Forwarding = true

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemX11Acknowledged {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemX11Acknowledged)
	}
	if len(repo.created) != 0 {
		t.Fatalf("created drafts = %d, want 0 before acknowledgement", len(repo.created))
	}

	state.Form().X11WarningAcknowledged = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save after acknowledgement: %v", err)
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("Problem = %q, want none", state.Problem())
	}
	if len(repo.created) != 1 {
		t.Fatalf("created drafts = %d, want 1", len(repo.created))
	}
	if !repo.created[0].X11Forwarding || !repo.created[0].X11WarningAcknowledged {
		t.Fatalf("created draft X11 fields = %+v, want both set", repo.created[0])
	}
}

func TestDisablingX11ClearsTheAcknowledgement(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "gui", Hostname: "example.com",
		X11Forwarding: true, X11WarningAcknowledged: true,
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit failed")
	}
	if !state.Form().X11Forwarding || !state.Form().X11WarningAcknowledged {
		t.Fatalf("form X11 fields = %+v, want the saved values", state.Form())
	}

	state.SetX11Forwarding(false)
	if state.Form().X11WarningAcknowledged {
		t.Fatal("disabling X11 kept the acknowledgement")
	}

	// Re-enabling in the same edit must not resurrect the old acknowledgement.
	state.SetX11Forwarding(true)
	if state.Form().X11WarningAcknowledged {
		t.Fatal("re-enabling X11 kept the acknowledgement")
	}
	state.Form().Label = "gui"
	state.Form().Hostname = "example.com"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemX11Acknowledged {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemX11Acknowledged)
	}
}

// Ticket 48: opening a Host that leaves X11 unset shows the Host Group's
// default, and saving without touching it leaves the Host unowned so it keeps
// following the group. A toggle becomes the Host's own value, including off.
func TestInheritedX11IsShownButNotSavedAsAnExplicitChoice(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com",
		Protocol: domainvault.ProtocolSSH, Group: "prod",
	}}}
	groups := &effectiveGroups{items: []domainvault.Group{{
		Path: "prod", Protocol: domainvault.ProtocolSSH,
		X11Forwarding: true, X11WarningAcknowledged: true,
	}}}
	state := NewState(repo, WithGroups(groups), WithRedrawRequest(func() {}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit failed")
	}
	form := state.Form()
	if !form.X11Forwarding || !form.X11WarningAcknowledged || !form.X11Inherited {
		t.Fatalf("form = %+v, want the inherited X11 on", form)
	}
	if form.X11ForwardingSet {
		t.Fatal("the inherited value was marked as the Host's own")
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("updates = %d, want 1", len(repo.updated))
	}
	saved := repo.updated[0]
	if saved.X11Forwarding || saved.X11WarningAcknowledged || saved.X11ForwardingSet {
		t.Fatalf("saving the displayed inheritance wrote an explicit choice: %+v", saved)
	}
	if effective, _ := state.EffectiveHost("host-1"); !effective.X11Enabled() {
		t.Fatalf("effective Host = %+v, want it to keep inheriting X11", effective)
	}

	// An explicit toggle is the Host's own value, even when it turns the
	// group's default off.
	state.BeginEdit("host-1")
	state.SetX11Forwarding(false)
	if err := state.Save(); err != nil {
		t.Fatalf("Save explicit off: %v", err)
	}
	if len(repo.updated) != 2 {
		t.Fatalf("updates = %d, want 2", len(repo.updated))
	}
	off := repo.updated[1]
	if off.X11Forwarding || !off.X11ForwardingSet {
		t.Fatalf("explicit off draft = %+v, want an owned off", off)
	}
	if effective, _ := state.EffectiveHost("host-1"); effective.X11Enabled() {
		t.Fatalf("effective Host = %+v, want the explicit off to win", effective)
	}
}

// TestSwitchingProtocolToSSHReappliesInheritedX11 proves the X11 control and
// its source label agree after a protocol change. A non-SSH Host hides the
// control and clears the displayed value, so switching back to SSH must
// re-resolve the group default rather than leave an unchecked box labelled
// inherited.
func TestSwitchingProtocolToSSHReappliesInheritedX11(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com",
		Protocol: domainvault.ProtocolTelnet, Group: "prod",
	}}}
	groups := &effectiveGroups{items: []domainvault.Group{{
		Path: "prod", Protocol: domainvault.ProtocolSSH,
		X11Forwarding: true, X11WarningAcknowledged: true,
	}}}
	state := NewState(repo, WithGroups(groups), WithRedrawRequest(func() {}))
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit failed")
	}
	if form := state.Form(); form.X11Forwarding || form.X11Inherited {
		t.Fatalf("a Telnet Host shows X11: %+v", form)
	}

	state.SetHostProtocol(string(domainvault.ProtocolSSH))
	form := state.Form()
	if !form.X11Forwarding || !form.X11WarningAcknowledged || !form.X11Inherited {
		t.Fatalf("switching to SSH did not show the inherited X11: %+v", form)
	}
	if form.X11ForwardingSet {
		t.Fatal("the inherited value was marked as the Host's own")
	}
	if source, _, _ := state.HostX11Source(); source != InheritedValueSourceInherited {
		t.Fatalf("HostX11Source = %q, want inherited", source)
	}

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("updates = %d, want 1", len(repo.updated))
	}
	saved := repo.updated[0]
	if saved.X11Forwarding || saved.X11WarningAcknowledged || saved.X11ForwardingSet {
		t.Fatalf("saving the displayed inheritance wrote an explicit choice: %+v", saved)
	}
	if effective, _ := state.EffectiveHost("host-1"); !effective.X11Enabled() {
		t.Fatalf("effective Host = %+v, want it to inherit X11", effective)
	}
}

func TestX11IsClearedOutsideSSH(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	form := state.Form()
	form.Label = "telnet"
	form.Hostname = "example.com"
	form.Protocol = string(domainvault.ProtocolTelnet)
	// A hidden control must not keep a value Save would persist.
	form.X11Forwarding = true
	form.X11WarningAcknowledged = true

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("Problem = %q, want none", state.Problem())
	}
	if len(repo.items) != 1 || repo.items[0].X11Forwarding ||
		repo.items[0].X11WarningAcknowledged {
		t.Fatalf("saved Host = %+v, want X11 cleared", repo.items)
	}
}

func TestEditingAnotherFieldPreservesEnabledX11(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "gui", Hostname: "example.com",
		X11Forwarding: true, X11WarningAcknowledged: true,
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit failed")
	}
	state.Form().Notes = "updated"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("Problem = %q, want none", state.Problem())
	}
	if len(repo.updated) != 1 || !repo.updated[0].X11Forwarding || !repo.updated[0].X11WarningAcknowledged {
		t.Fatalf("updated draft = %+v, want X11 preserved", repo.updated)
	}
}

func TestInvalidHostProducesAProblemKey(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Label = "web"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemHostnameRequired {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemHostnameRequired)
	}
	if !state.Editing() {
		t.Fatal("Save closed the editor after a validation failure")
	}
	if len(repo.created) != 0 {
		t.Fatalf("created drafts = %d, want 0", len(repo.created))
	}
}

func TestNonNumericPortIsRejectedBeforeTheService(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Hostname = "example.com"
	state.Form().Port = "twenty-two"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemPortInvalid {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemPortInvalid)
	}
	if len(repo.created) != 0 {
		t.Fatalf("created drafts = %d, want 0", len(repo.created))
	}
}

func TestEditPopulatesTheFormAndUpdatesTheSameHost(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, Group: "prod",
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit returned false for a listed Host")
	}
	if state.Form().ID != "host-1" || state.Form().Hostname != "example.com" || state.Form().Port != "22" {
		t.Fatalf("form = %+v, want the Host's values", state.Form())
	}
	state.Form().Label = "web-2"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.updated) != 1 || len(repo.created) != 0 {
		t.Fatalf("updated = %d, created = %d, want 1 and 0", len(repo.updated), len(repo.created))
	}
	if state.Items()[0].Label != "web-2" {
		t.Fatalf("Label = %q, want web-2", state.Items()[0].Label)
	}
}

func TestHostDetailsRoundTripsTheStartupCommand(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
		StartupCommand: "echo ready",
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	state.BeginEdit("host-1")
	if got := state.Form().StartupCommand; got != "echo ready" {
		t.Fatalf("Form().StartupCommand = %q, want the Host's command", got)
	}
	state.Form().StartupCommand = "  uptime  "
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.updated) != 1 {
		t.Fatalf("updated drafts = %d, want 1", len(repo.updated))
	}
	normalized, err := domainvault.NormalizeDraft(repo.updated[0])
	if err != nil {
		t.Fatalf("NormalizeDraft: %v", err)
	}
	if normalized.StartupCommand != "uptime" {
		t.Fatalf("StartupCommand = %q, want uptime", normalized.StartupCommand)
	}
	if got := state.Items()[0].StartupCommand; got != "uptime" {
		t.Fatalf("StartupCommand = %q, want uptime", got)
	}
}

func TestConflictMapsToTheDuplicateProblem(t *testing.T) {
	repo := &fakeHosts{err: &appvault.Error{Kind: appvault.KindConflict}}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Hostname = "example.com"

	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemDuplicate {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemDuplicate)
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "host-1", Label: "web"}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	state.RequestDelete("host-1")
	if state.PendingDelete() != "host-1" {
		t.Fatalf("pending delete = %q, want host-1", state.PendingDelete())
	}
	state.CancelDelete()
	if state.PendingDelete() != "" || len(repo.deleted) != 0 {
		t.Fatal("CancelDelete deleted the Host or left the confirmation open")
	}

	state.RequestDelete("host-1")
	if err := state.ConfirmDelete(); err != nil {
		t.Fatalf("ConfirmDelete: %v", err)
	}
	if len(repo.deleted) != 1 || repo.deleted[0] != "host-1" {
		t.Fatalf("deleted = %v, want [host-1]", repo.deleted)
	}
	if state.HostCount() != 0 {
		t.Fatalf("Hosts after Delete = %d, want 0", state.HostCount())
	}
}

func TestTogglePinnedPersistsThePreferenceThroughUpdate(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	if err := state.TogglePinned("host-1"); err != nil {
		t.Fatalf("TogglePinned: %v", err)
	}
	if len(repo.updated) != 1 || !repo.updated[0].Pinned {
		t.Fatalf("updated drafts = %+v, want Pinned true", repo.updated)
	}
	if !state.Items()[0].Pinned {
		t.Fatal("the pinned Host was not refreshed")
	}
}

func TestConnectCallsTheConnectActionForAListedHost(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "a", Label: "alpha", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	}}}
	var connected domainvault.Host
	state := NewState(repo, WithConnect(func(host domainvault.Host) { connected = host }))
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}

	if !state.Connect("a") {
		t.Fatal("Connect reported failure for a listed Host")
	}
	if connected.ID != "a" || connected.Hostname != "example.com" {
		t.Fatalf("connected Host = %+v, want the listed Host", connected)
	}

	if state.Connect("missing") {
		t.Fatal("Connect reported success for an unknown Host")
	}
	if state.Problem() != ProblemNotFound {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemNotFound)
	}
}

func TestReloadDropsASelectionThatNoLongerExists(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{ID: "host-1", Label: "web"}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	state.Select("host-1")
	repo.items = nil
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	if state.Selected() != "" {
		t.Fatalf("selection = %q after the Host disappeared, want empty", state.Selected())
	}
}

func TestStorageFailureProducesTheInternalProblem(t *testing.T) {
	repo := &fakeHosts{err: errors.New("disk on fire")}
	state, _ := newTestState(t, repo)

	if err := state.Reload(); err == nil {
		t.Fatal("Reload returned nil, want the storage error")
	}
	if state.Problem() != ProblemInternal {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemInternal)
	}
}

type fakeSecrets struct {
	mode       appvault.SecretMode
	setErr     error
	unlockErr  error
	sessionErr error
	passphrase string
}

func (f *fakeSecrets) SecretMode() appvault.SecretMode { return f.mode }

func (f *fakeSecrets) SetSecretsPassphrase(_ context.Context, passphrase string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.passphrase = passphrase
	f.mode = appvault.SecretModePassphrase
	return nil
}

func (f *fakeSecrets) UnlockSecrets(_ context.Context, passphrase string) error {
	if f.unlockErr != nil {
		return f.unlockErr
	}
	f.passphrase = passphrase
	f.mode = appvault.SecretModePassphrase
	return nil
}

func (f *fakeSecrets) UseSessionSecrets(context.Context) error {
	if f.sessionErr != nil {
		return f.sessionErr
	}
	f.mode = appvault.SecretModeSession
	return nil
}

func newStateWithSecrets(t *testing.T, repo *fakeHosts, secrets Secrets) *State {
	t.Helper()
	return NewState(repo, WithSecrets(secrets), WithRedrawRequest(func() {}))
}

func TestSaveSubmitsThePasswordAndClearsTheForm(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Hostname = "example.com"
	state.Form().Password = "hunter2"

	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if len(repo.created) != 1 || repo.created[0].Password != "hunter2" {
		t.Fatalf("created drafts = %+v, want the password", repo.created)
	}
	if state.Editing() {
		t.Fatal("the editor stayed open")
	}
}

func TestEditNeverLoadsTheStoredPasswordIntoTheForm(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, HasPassword: true,
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatal(err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit returned false")
	}
	if state.Form().Password != "" {
		t.Fatalf("form password = %q, want empty", state.Form().Password)
	}
	if !state.Form().HasPassword {
		t.Error("the form does not know that a password is stored")
	}
}

func TestPassphraseSetupValidatesAndChangesTheMode(t *testing.T) {
	secrets := &fakeSecrets{mode: appvault.SecretModeUnavailable}
	state := newStateWithSecrets(t, &fakeHosts{}, secrets)

	*state.SecretPassphrase() = ""
	if state.SetSecretPassphrase() {
		t.Fatal("an empty passphrase was accepted")
	}
	if state.SecretProblem() != ProblemSecretPassphraseRequired {
		t.Fatalf("problem = %q, want %q", state.SecretProblem(), ProblemSecretPassphraseRequired)
	}

	*state.SecretPassphrase() = "one"
	*state.SecretConfirmation() = "two"
	if state.SetSecretPassphrase() {
		t.Fatal("mismatched passphrases were accepted")
	}
	if state.SecretProblem() != ProblemSecretPassphraseMismatch {
		t.Fatalf("problem = %q, want %q", state.SecretProblem(), ProblemSecretPassphraseMismatch)
	}

	*state.SecretPassphrase() = "pass"
	*state.SecretConfirmation() = "pass"
	if !state.SetSecretPassphrase() {
		t.Fatalf("SetSecretPassphrase failed: %q", state.SecretProblem())
	}
	if secrets.passphrase != "pass" || secrets.mode != appvault.SecretModePassphrase {
		t.Fatalf("secrets = %+v, want passphrase mode", secrets)
	}
	if *state.SecretPassphrase() != "" || state.SecretProblem() != ProblemNone {
		t.Fatal("a successful setup left input or a problem behind")
	}
}

func TestUnlockSecretsReportsAWrongPassphrase(t *testing.T) {
	secrets := &fakeSecrets{mode: appvault.SecretModePassphraseLocked, unlockErr: &appvault.Error{Kind: appvault.KindPermission}}
	state := newStateWithSecrets(t, &fakeHosts{}, secrets)
	*state.SecretPassphrase() = "wrong"

	if state.UnlockSecrets() {
		t.Fatal("a wrong passphrase unlocked the store")
	}
	if state.SecretProblem() != ProblemSecretWrongPassphrase {
		t.Fatalf("problem = %q, want %q", state.SecretProblem(), ProblemSecretWrongPassphrase)
	}
}

func TestUseSessionSecretsChangesTheMode(t *testing.T) {
	secrets := &fakeSecrets{mode: appvault.SecretModeUnavailable}
	state := newStateWithSecrets(t, &fakeHosts{}, secrets)
	if !state.UseSessionSecrets() {
		t.Fatalf("UseSessionSecrets failed: %q", state.SecretProblem())
	}
	if secrets.mode != appvault.SecretModeSession {
		t.Fatalf("mode = %q, want session", secrets.mode)
	}
}

func TestSecretServiceErrorsMapToProblems(t *testing.T) {
	tests := []struct {
		kind appvault.Kind
		want Problem
	}{
		{appvault.KindPermission, ProblemSecretsLocked},
		{appvault.KindUnsupported, ProblemSecretsUnavailable},
	}
	for _, test := range tests {
		repo := &fakeHosts{err: &appvault.Error{Kind: test.kind}}
		state, _ := newTestState(t, repo)
		state.BeginCreate()
		state.Form().Hostname = "example.com"
		state.Form().Password = "hunter2"
		if err := state.Save(); err != nil {
			t.Fatal(err)
		}
		if state.Problem() != test.want {
			t.Errorf("kind %q mapped to %q, want %q", test.kind, state.Problem(), test.want)
		}
	}
}

func TestUnreadableSecretMapsToItsOwnProblem(t *testing.T) {
	repo := &fakeHosts{err: &appvault.Error{Kind: appvault.KindInternal, Cause: appvault.ErrSecretUnreadable}}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	state.Form().Hostname = "example.com"
	state.Form().Password = "hunter2"
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	if state.Problem() != ProblemSecretUnreadable {
		t.Fatalf("problem = %q, want %q", state.Problem(), ProblemSecretUnreadable)
	}
}

func TestSecretModeWithoutASeamIsOS(t *testing.T) {
	state, _ := newTestState(t, &fakeHosts{})
	if state.SecretMode() != appvault.SecretModeOS {
		t.Fatalf("SecretMode = %q, want os", state.SecretMode())
	}
}

func TestValidationProblemsMapRoutingAndAlgorithms(t *testing.T) {
	tests := []struct {
		field domainvault.Field
		want  Problem
	}{
		{domainvault.FieldChain, ProblemProxy},
		{domainvault.FieldProxy, ProblemProxy},
		{domainvault.FieldAlgorithms, ProblemAlgorithms},
	}
	for _, test := range tests {
		problem := problemForValidation(&domainvault.ValidationError{Field: test.field})
		if problem != test.want {
			t.Errorf("field %s maps to %s, want %s", test.field, problem, test.want)
		}
	}
}

func TestEditingPreservesTheSavedChainAndProxy(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "gateway", Hostname: "target.internal", Port: 22,
		Username: "app", Protocol: domainvault.ProtocolSSH,
		HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"jump-1", "jump-2"}},
		Proxy: &domainvault.ProxyConfig{
			Type: domainvault.ProxySOCKS5, Host: "proxy.internal", Port: 1080,
			Username: "user", Password: "secret", HasPassword: true,
		},
		LegacyAlgorithms: true,
		SkipEcdsaHostKey: true,
		Algorithms: &domainvault.AlgorithmOverrides{
			KeyExchanges: []string{"diffie-hellman-group14-sha1"},
		},
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit returned false")
	}
	state.Form().Label = "renamed"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if len(repo.updated) != 1 {
		t.Fatalf("updates = %d, want 1", len(repo.updated))
	}
	draft := repo.updated[0]
	if draft.HostChain == nil || len(draft.HostChain.HostIDs) != 2 ||
		draft.HostChain.HostIDs[0] != "jump-1" || draft.HostChain.HostIDs[1] != "jump-2" {
		t.Fatalf("chain = %+v", draft.HostChain)
	}
	if draft.Proxy == nil || draft.Proxy.Type != domainvault.ProxySOCKS5 ||
		draft.Proxy.Host != "proxy.internal" || draft.Proxy.Port != 1080 ||
		draft.Proxy.Username != "user" {
		t.Fatalf("proxy = %+v", draft.Proxy)
	}
	if draft.Proxy.Password != "" {
		t.Fatal("the saved proxy password was copied into the draft")
	}
	if !draft.LegacyAlgorithms || !draft.SkipEcdsaHostKey || draft.Algorithms == nil ||
		len(draft.Algorithms.KeyExchanges) != 1 ||
		draft.Algorithms.KeyExchanges[0] != "diffie-hellman-group14-sha1" {
		t.Fatalf("algorithms = %+v, legacy = %v, skip = %v", draft.Algorithms, draft.LegacyAlgorithms, draft.SkipEcdsaHostKey)
	}

	// The reloaded Host must supply the same route and policy to a second
	// edit; otherwise a save would silently clear them.
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit after save returned false")
	}
	state.Form().Notes = "second edit"
	if err := state.Save(); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if len(repo.updated) != 2 {
		t.Fatalf("updates = %d, want 2", len(repo.updated))
	}
	second := repo.updated[1]
	if second.HostChain == nil || len(second.HostChain.HostIDs) != 2 {
		t.Fatalf("second edit chain = %+v", second.HostChain)
	}
	if second.Proxy == nil || second.Proxy.Host != "proxy.internal" {
		t.Fatalf("second edit proxy = %+v", second.Proxy)
	}
	if !second.LegacyAlgorithms || !second.SkipEcdsaHostKey || second.Algorithms == nil ||
		len(second.Algorithms.KeyExchanges) != 1 {
		t.Fatalf("second edit algorithms = %+v, legacy = %v, skip = %v",
			second.Algorithms, second.LegacyAlgorithms, second.SkipEcdsaHostKey)
	}
}

func TestCreatingAHostDoesNotInheritAPreviousEditsRoute(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "gateway", Hostname: "target.internal", Port: 22,
		Protocol:  domainvault.ProtocolSSH,
		HostChain: &domainvault.HostChain{HostIDs: []model.HostID{"jump-1"}},
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit returned false")
	}
	state.CancelEdit()
	state.BeginCreate()
	state.Form().Label = "new"
	state.Form().Hostname = "new.internal"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if repo.created[0].HostChain != nil || repo.created[0].Proxy != nil {
		t.Fatalf("new Host inherited a route: %+v", repo.created[0])
	}
	if repo.created[0].LegacyAlgorithms || repo.created[0].SkipEcdsaHostKey || repo.created[0].Algorithms != nil {
		t.Fatalf("new Host inherited an algorithm policy: %+v", repo.created[0])
	}
}

func TestHostFormSubmitsAndRestoresTheFileProtocol(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	if form := state.Form(); form.FileProtocol != string(domainvault.FileProtocolAuto) {
		t.Fatalf("new Host protocol = %q, want auto", form.FileProtocol)
	}
	form := state.Form()
	form.Label = "web"
	form.Hostname = "example.com"
	form.FileProtocol = string(domainvault.FileProtocolSCP)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 || repo.created[0].SFTPFileProtocol != domainvault.FileProtocolSCP {
		t.Fatalf("created = %+v", repo.created)
	}

	repo.items = []domainvault.Host{{
		ID: "host-9", Label: "root-box", Hostname: "example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, SFTPFileProtocol: domainvault.FileProtocolSFTP,
		SFTPSudo: true,
	}}
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-9") {
		t.Fatal("BeginEdit did not open the editor")
	}
	restored := state.Form()
	if restored.FileProtocol != string(domainvault.FileProtocolSFTP) || !restored.SFTPSudo {
		t.Fatalf("restored form = %+v", restored)
	}

	// A Host saved before the preference existed has no value; the editor must
	// show automatic rather than a blank select.
	repo.items = append(repo.items, domainvault.Host{
		ID: "host-legacy", Label: "legacy", Hostname: "legacy.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH,
	})
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload legacy: %v", err)
	}
	if !state.BeginEdit("host-legacy") {
		t.Fatal("BeginEdit did not open the legacy editor")
	}
	if got := state.Form().FileProtocol; got != string(domainvault.FileProtocolAuto) {
		t.Fatalf("legacy protocol = %q, want auto", got)
	}
}

func TestNetworkDeviceModeIsSavedOnlyForSSH(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	form := state.Form()
	form.Label = "switch"
	form.Hostname = "switch.example.com"
	form.Protocol = string(domainvault.ProtocolSSH)
	form.NetworkDevice = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 || repo.created[0].DeviceType != domainvault.DeviceTypeNetwork {
		t.Fatalf("created = %+v, want the network device mode", repo.created)
	}
	if !repo.items[0].IsNetworkDevice() {
		t.Fatalf("saved Host is not a network device: %+v", repo.items[0])
	}

	// The editor offers the mode only for SSH, so a hidden checkbox must never
	// save raw execution onto a Host whose transport cannot run it.
	state.BeginCreate()
	form = state.Form()
	form.Label = "roaming"
	form.Hostname = "roaming.example.com"
	form.Protocol = string(domainvault.ProtocolSSH)
	form.Transport = string(domainvault.TransportMosh)
	form.NetworkDevice = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save Mosh Host: %v", err)
	}
	if last := repo.created[len(repo.created)-1]; last.DeviceType != domainvault.DeviceTypeDefault {
		t.Fatalf("Mosh Host device type = %q, want the default", last.DeviceType)
	}
}

func TestBeginEditRestoresTheNetworkDeviceMode(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-switch", Label: "switch", Hostname: "switch.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, DeviceType: domainvault.DeviceTypeNetwork,
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-switch") {
		t.Fatal("BeginEdit did not open the editor")
	}
	if !state.Form().NetworkDevice {
		t.Fatal("the editor did not restore the network device mode")
	}

	// Switching away from SSH drops the mode, which NormalizeDraft also
	// enforces for records written by hand.
	form := state.Form()
	form.Protocol = string(domainvault.ProtocolTelnet)
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	last := repo.updated[len(repo.updated)-1]
	if last.DeviceType != domainvault.DeviceTypeDefault {
		t.Fatalf("switched Host device type = %q, want the default", last.DeviceType)
	}
}

// TestEditorPreservesAnUnsupportedTransportTillSaveRejectsIt proves opening a
// record with an unknown transport does not rewrite it as plain SSH: the form
// keeps the raw value, and Save reports it instead of persisting a change.
func TestEditorPreservesAnUnsupportedTransportTillSaveRejectsIt(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "bad", Label: "bad", Hostname: "bad.example.com", Port: 22,
		Protocol: domainvault.ProtocolSSH, Transport: "ssh2",
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("bad") {
		t.Fatal("BeginEdit did not open the Host")
	}
	if got := state.Form().Transport; got != "ssh2" {
		t.Fatalf("form transport = %q, want the raw ssh2", got)
	}
	state.Form().Label = "renamed"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := state.Problem(); got != ProblemProtocol {
		t.Fatalf("problem = %q, want %q", got, ProblemProtocol)
	}
	if len(repo.updated) != 0 {
		t.Fatalf("an unsupported transport was persisted: %+v", repo.updated)
	}
}

func TestMoshAndETSettingsAreSavedOnlyForTheirTransport(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	form := state.Form()
	form.Label = "build-box"
	form.Hostname = "build.example.com"
	form.Protocol = string(domainvault.ProtocolSSH)
	form.Transport = string(domainvault.TransportMosh)
	form.MoshServerPath = "/usr/local/bin/mosh-server"
	// A hidden ET field must not leak into a Mosh Host.
	form.ETPort = "8737"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 {
		t.Fatalf("created = %d, want 1", len(repo.created))
	}
	if repo.created[0].MoshServerPath != "/usr/local/bin/mosh-server" {
		t.Fatalf("Mosh draft = %+v", repo.created[0])
	}
	if saved := repo.items[0]; saved.MoshServerPath != "/usr/local/bin/mosh-server" || saved.ETPort != 0 {
		t.Fatalf("Mosh Host = %+v", saved)
	}

	state.BeginCreate()
	form = state.Form()
	form.Label = "et-box"
	form.Hostname = "et.example.com"
	form.Protocol = string(domainvault.ProtocolSSH)
	form.Transport = string(domainvault.TransportET)
	form.ETPort = "8737"
	form.MoshServerPath = "/opt/mosh-server"
	if err := state.Save(); err != nil {
		t.Fatalf("Save ET: %v", err)
	}
	etHost := repo.items[len(repo.items)-1]
	if etHost.ETPort != 8737 || etHost.MoshServerPath != "" {
		t.Fatalf("ET Host = %+v", etHost)
	}
	if !state.BeginEdit(etHost.ID) {
		t.Fatal("BeginEdit did not open the ET editor")
	}
	restored := state.Form()
	if restored.ETPort != "8737" || restored.MoshServerPath != "" {
		t.Fatalf("restored ET form = %+v", restored)
	}
	// A saved Host with the default port keeps the field blank; the editor's
	// placeholder shows 2022 and the domain default fills it in on save.
	repo.items[len(repo.items)-1].ETPort = 0
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit(etHost.ID) || state.Form().ETPort != "" {
		t.Fatalf("default ET port = %q, want a blank field", state.Form().ETPort)
	}

	state.BeginCreate()
	form = state.Form()
	form.Label = "bad-port"
	form.Hostname = "bad.example.com"
	form.Protocol = string(domainvault.ProtocolSSH)
	form.Transport = string(domainvault.TransportET)
	form.ETPort = "not-a-port"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemETPort {
		t.Fatalf("problem = %v, want %v", state.Problem(), ProblemETPort)
	}
}

func TestHostFormSubmitsAndRestoresAutoAttachTmux(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	if state.Form().AutoAttachTmux {
		t.Fatal("a new Host enabled autoAttachTmux by default")
	}
	form := state.Form()
	form.Label = "web"
	form.Hostname = "example.com"
	form.AutoAttachTmux = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 || !repo.created[0].AutoAttachTmux {
		t.Fatalf("created draft = %+v, want autoAttachTmux", repo.created)
	}
	if got := repo.items[0]; !got.AutoAttachTmux {
		t.Fatalf("saved Host = %+v, want autoAttachTmux", got)
	}

	if !state.BeginEdit(repo.items[0].ID) {
		t.Fatal("BeginEdit did not open the editor")
	}
	if !state.Form().AutoAttachTmux {
		t.Fatal("the editor did not restore autoAttachTmux")
	}
	state.SetHostProtocol(string(domainvault.ProtocolTelnet))
	if state.Form().AutoAttachTmux {
		t.Fatal("switching protocol kept autoAttachTmux")
	}
	state.Form().AutoAttachTmux = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save Telnet: %v", err)
	}
	if repo.updated[len(repo.updated)-1].AutoAttachTmux {
		t.Fatal("a Telnet draft carried autoAttachTmux")
	}
}

func TestNetworkDeviceModeClearsAutoAttachTmux(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	form := state.Form()
	form.Label = "switch"
	form.Hostname = "10.0.0.1"
	form.AutoAttachTmux = true
	form.NetworkDevice = true
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(repo.created) != 1 || repo.created[0].AutoAttachTmux {
		t.Fatalf("network-device draft = %+v, want autoAttachTmux cleared", repo.created)
	}
}

// TestVNCFormStoresTheDisplayAsTheRealPort proves the editor's display-number
// input resolves to a TCP port, and switching input modes carries the same
// endpoint without changing it.
func TestVNCFormStoresTheDisplayAsTheRealPort(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)

	state.BeginCreate()
	form := state.Form()
	form.Label = "desk"
	form.Hostname = "desk.local"
	state.SetHostProtocol(string(domainvault.ProtocolVNC))
	form = state.Form()
	if form.VNCInputMode != string(domainvault.VNCDisplayNumber) ||
		form.VNCFitMode != string(domainvault.VNCFit) ||
		!form.VNCShared ||
		form.VNCCursorMode != string(domainvault.VNCCursorLocal) {
		t.Fatalf("VNC defaults = %+v", *form)
	}
	form.VNCDisplay = "1"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemNone {
		t.Fatalf("Problem = %q", state.Problem())
	}
	saved := repo.items[0]
	if saved.Port != 5901 || saved.Protocol != domainvault.ProtocolVNC {
		t.Fatalf("saved Host = %+v, want VNC on 5901", saved)
	}
	if saved.VNC == nil || saved.VNC.DisplayMode != domainvault.VNCDisplayNumber {
		t.Fatalf("saved VNC config = %+v", saved.VNC)
	}

	if !state.BeginEdit(saved.ID) {
		t.Fatal("BeginEdit failed")
	}
	form = state.Form()
	if form.Port != "5901" || form.VNCDisplay != "1" {
		t.Fatalf("reloaded endpoint = port %q display %q, want 5901 / 1", form.Port, form.VNCDisplay)
	}

	state.SetVNCInputMode(string(domainvault.VNCPort))
	form = state.Form()
	if form.Port != "5901" || form.VNCInputMode != string(domainvault.VNCPort) {
		t.Fatalf("port mode = %+v, want port 5901", *form)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save port mode: %v", err)
	}
	if got := repo.items[0].Port; got != 5901 {
		t.Fatalf("switching modes changed the stored port to %d", got)
	}
	if got := repo.items[0].VNC.DisplayMode; got != domainvault.VNCPort {
		t.Fatalf("saved input mode = %q, want port", got)
	}

	// The raw port 6000 has no display, so keeping port input is the only
	// way to save it; the display field is not the source of truth there.
	if !state.BeginEdit(saved.ID) {
		t.Fatal("BeginEdit for the display-range check failed")
	}
	form = state.Form()
	form.Port = "6000"
	state.SetVNCInputMode(string(domainvault.VNCDisplayNumber))
	if form.VNCDisplay != "100" {
		t.Fatalf("display derivation = %q, want 100 so the range error is visible", form.VNCDisplay)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if state.Problem() != ProblemVNCDisplayRange {
		t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemVNCDisplayRange)
	}
}

// TestVNCInputModeSwitchRejectsOutOfRangeDisplay proves an out-of-range
// display is not laundered into a fabricated raw port when the endpoint input
// mode is switched. The stored endpoint stays as it was, the invalid text is
// kept so Save still rejects it, and the range problem is reported.
func TestVNCInputModeSwitchRejectsOutOfRangeDisplay(t *testing.T) {
	for _, display := range []string{"150", "-1"} {
		t.Run(display, func(t *testing.T) {
			repo := &fakeHosts{items: []domainvault.Host{{
				ID: "host-1", Label: "desk", Hostname: "desk.local", Port: 5901,
				Protocol: domainvault.ProtocolVNC,
				VNC:      &domainvault.VNCConfig{DisplayMode: domainvault.VNCDisplayNumber},
			}}}
			state, _ := newTestState(t, repo)
			if err := state.Reload(); err != nil {
				t.Fatalf("Reload: %v", err)
			}
			if !state.BeginEdit("host-1") {
				t.Fatal("BeginEdit failed")
			}
			form := state.Form()
			form.VNCDisplay = display

			state.SetVNCInputMode(string(domainvault.VNCPort))

			form = state.Form()
			if form.Port != "5901" {
				t.Fatalf("switch fabricated port %q, want the stored endpoint 5901", form.Port)
			}
			if form.VNCInputMode != string(domainvault.VNCDisplayNumber) {
				t.Fatalf("input mode = %q, want display mode kept so Save still validates", form.VNCInputMode)
			}
			if state.Problem() != ProblemVNCDisplayRange {
				t.Fatalf("Problem = %q, want %q", state.Problem(), ProblemVNCDisplayRange)
			}

			if err := state.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if got := repo.items[0].Port; got != 5901 {
				t.Fatalf("stored endpoint changed to %d", got)
			}
			if repo.items[0].VNC == nil || repo.items[0].VNC.DisplayMode != domainvault.VNCDisplayNumber {
				t.Fatalf("stored VNC config = %+v, want display mode kept", repo.items[0].VNC)
			}
		})
	}
}

// TestVNCFormRejectsInvalidDisplayAndPort proves malformed endpoint input
// never reaches the Vault.
func TestVNCFormRejectsInvalidDisplayAndPort(t *testing.T) {
	tests := []struct {
		name    string
		mode    domainvault.VNCDisplayMode
		display string
		port    string
		problem Problem
	}{
		{"non-numeric display", domainvault.VNCDisplayNumber, "one", "", ProblemVNCDisplay},
		{"negative display", domainvault.VNCDisplayNumber, "-1", "", ProblemVNCDisplay},
		{"display too high", domainvault.VNCDisplayNumber, "100", "", ProblemVNCDisplayRange},
		{"non-numeric port", domainvault.VNCPort, "", "not-a-port", ProblemPortInvalid},
		{"port too high", domainvault.VNCPort, "", "70000", ProblemPortRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &fakeHosts{}
			state, _ := newTestState(t, repo)
			state.BeginCreate()
			form := state.Form()
			form.Label = "desk"
			form.Hostname = "desk.local"
			state.SetHostProtocol(string(domainvault.ProtocolVNC))
			form = state.Form()
			form.VNCInputMode = string(test.mode)
			form.VNCDisplay = test.display
			form.Port = test.port
			if err := state.Save(); err != nil {
				t.Fatalf("Save: %v", err)
			}
			if state.Problem() != test.problem {
				t.Fatalf("Problem = %q, want %q", state.Problem(), test.problem)
			}
			if len(repo.created) != 0 {
				t.Fatalf("an invalid VNC endpoint was saved: %+v", repo.created)
			}
		})
	}
}

// TestSwitchingAwayFromVNCClearsTheConfigurationWithoutTouchingSSHCredentials
// proves the hidden VNC section leaves no state behind, and returning to VNC
// starts from the defaults.
func TestSwitchingAwayFromVNCClearsTheConfigurationWithoutTouchingSSHCredentials(t *testing.T) {
	repo := &fakeHosts{items: []domainvault.Host{{
		ID: "host-1", Label: "desk", Hostname: "desk.local", Port: 5901,
		Protocol: domainvault.ProtocolVNC, HasPassword: true,
		VNC: &domainvault.VNCConfig{
			DisplayMode: domainvault.VNCPort, FitMode: domainvault.VNCClip,
			ViewOnly: true, Shared: false, CursorMode: domainvault.VNCCursorServer,
		},
	}}}
	state, _ := newTestState(t, repo)
	if err := state.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit failed")
	}
	state.SetHostProtocol(string(domainvault.ProtocolSSH))
	form := state.Form()
	if form.VNCFitMode != string(domainvault.VNCFit) || form.VNCViewOnly ||
		!form.VNCShared || form.VNCCursorMode != string(domainvault.VNCCursorLocal) {
		t.Fatalf("leaving VNC did not clear the configuration: %+v", *form)
	}
	if form.Password != "" || !form.HasPassword {
		t.Fatalf("leaving VNC touched SSH credentials: %+v", *form)
	}
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := repo.updated[len(repo.updated)-1]; got.VNC != nil || got.VNCPassword != "" {
		t.Fatalf("saved draft = %+v, want VNC cleared", got)
	}
	if repo.items[0].VNC != nil {
		t.Fatalf("saved Host = %+v, want VNC cleared", repo.items[0])
	}

	if !state.BeginEdit("host-1") {
		t.Fatal("BeginEdit after switch failed")
	}
	state.SetHostProtocol(string(domainvault.ProtocolVNC))
	form = state.Form()
	if form.VNCInputMode != string(domainvault.VNCDisplayNumber) ||
		form.VNCFitMode != string(domainvault.VNCFit) || form.VNCViewOnly ||
		!form.VNCShared || form.VNCCursorMode != string(domainvault.VNCCursorLocal) {
		t.Fatalf("returning to VNC did not start from defaults: %+v", *form)
	}
}

// TestVNCPasswordUsesOnlyTheDedicatedFormField proves a stale SSH password in
// the form is never submitted for a VNC Host.
func TestVNCPasswordUsesOnlyTheDedicatedFormField(t *testing.T) {
	repo := &fakeHosts{}
	state, _ := newTestState(t, repo)
	state.BeginCreate()
	form := state.Form()
	form.Label = "desk"
	form.Hostname = "desk.local"
	state.SetHostProtocol(string(domainvault.ProtocolVNC))
	form = state.Form()
	form.Password = "ssh-value"
	form.VNCPassword = "vnc-value"
	if err := state.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	draft := repo.created[0]
	if draft.Password != "" {
		t.Fatalf("VNC draft submitted the SSH password %q", draft.Password)
	}
	if draft.VNCPassword != "vnc-value" {
		t.Fatalf("VNC draft password = %q", draft.VNCPassword)
	}
}
