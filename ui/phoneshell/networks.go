package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/domain/wireguardconf"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	"github.com/mslxl/natcatty/ui/locales"
	vaultstate "github.com/mslxl/natcatty/ui/vault/state"
)

// NetworkStatusSource is the latest non-secret Provider status per Network
// Profile. The desktop State implements it; a phone build can supply the same
// registry-backed status store, and a test supplies a fake. The statuses carry
// no credential material: only phases, addresses, an Auth URL, and an error.
type NetworkStatusSource interface {
	NetworkStatus(profileID string) (vaultstate.NetworkProviderStatus, bool)
}

// networkKindTailscaleIndex and networkKindWireGuardIndex are the editor's
// segment order.
const (
	networkKindTailscaleIndex = iota
	networkKindWireGuardIndex
)

// networkUnavailable is the page's state when the Vault service was built
// without Network Profile persistence. No production root leaves it unset.
const networkUnavailable = "Network Profiles are not available on this device."

// networkPeerForm is one editable WireGuard peer row. Numeric fields stay text
// so a malformed value is rejected instead of silently becoming zero. The ID
// addresses a stored Preshared Key and is preserved across an edit.
type networkPeerForm struct {
	id              string
	publicKey       string
	presharedKey    string
	hasPresharedKey bool
	endpoint        string
	allowedIPs      string
	keepalive       string
}

// networkDraftForm is the plaintext editor of one Network Profile. Credential
// fields are write-only: the saved record contributes only the Has* markers,
// and a blank field on an edit keeps the stored credential.
type networkDraftForm struct {
	id        string
	label     string
	kindIndex int
	editing   bool

	// Tailscale Net
	hostname   string
	authKey    string
	hasAuthKey bool
	controlURL string
	ephemeral  bool
	tags       string

	// WireGuard Net
	privateKey    string
	hasPrivateKey bool
	addresses     string
	dns           string
	mtu           string
	listenPort    string
	peers         []networkPeerForm
}

// networkPages owns the Networks section's list and editor. The list reads the
// shared Vault service over the same SQLite store the desktop uses, so a
// profile created or edited on either device appears on the other after a
// reload. The Provider controls drive the same registry the desktop's Vault
// surface uses; this page never runs protocol logic of its own.
type networkPages struct {
	store        vaultstate.NetworkProfiles
	actions      vaultstate.NetworkProviderActions
	statusSource NetworkStatusSource
	picker       vaultstate.WireGuardConfPicker
	openURL      func(string) error
	copyText     func(string)

	listStatus vaultStatus
	problem    string
	profiles   []domainvault.NetworkProfile

	pendingDelete string
	draft         *networkDraftForm
	draftProblem  string
	// draftNotice reports a successful WireGuard import. It is advisory and
	// cleared on the next edit or cancel.
	draftNotice string

	// actionMu guards actionProblem, which an asynchronous Provider action
	// writes after its network call returns.
	actionMu      sync.Mutex
	actionProblem string
}

func newNetworkPages() *networkPages {
	return &networkPages{
		listStatus: vaultLoading,
		openURL:    mygo.Shell.OpenExternal,
		copyText:   mygo.Clipboard.WriteText,
	}
}

// WithNetworkProviderControl gives the Networks page the shared interactive
// login lifecycle and status. The actions are the same application controller
// the desktop wires into its Vault surface; the status source is read on every
// frame. Without it the page explains that signing in is unavailable, while
// listing, editing, and deleting keep working.
func WithNetworkProviderControl(actions vaultstate.NetworkProviderActions,
	statuses NetworkStatusSource) Option {

	return func(s *Shell) {
		if s.vault.networks == nil {
			s.vault.networks = newNetworkPages()
		}
		s.vault.networks.actions = actions
		s.vault.networks.statusSource = statuses
	}
}

// WithNetworkConfPicker gives the WireGuard editor the system file picker. The
// composition root supplies the same adapter the desktop uses; without it the
// editor says importing is unavailable instead of substituting a text box.
func WithNetworkConfPicker(picker vaultstate.WireGuardConfPicker) Option {
	return func(s *Shell) {
		if s.vault.networks == nil {
			s.vault.networks = newNetworkPages()
		}
		s.vault.networks.picker = picker
	}
}

// WithNetworkLoginActions replaces how the Tailscale login URL is opened and
// copied. The defaults hand the URL to the system browser and clipboard; tests
// inject recorders so they never touch either.
func WithNetworkLoginActions(open func(string) error, copyText func(string)) Option {
	return func(s *Shell) {
		if s.vault.networks == nil {
			s.vault.networks = newNetworkPages()
		}
		if open != nil {
			s.vault.networks.openURL = open
		}
		if copyText != nil {
			s.vault.networks.copyText = copyText
		}
	}
}

// loadNetworks binds the shared Vault service and replaces the list. The
// binding is lazy because the Shell may learn its Vault through the Hosts seam
// rather than WithVault. A service built without Network Profiles keeps the
// page unavailable, and no other Vault section is affected: the failure is
// this page's own state, never the Shell's Load error.
func (v *vaultPages) loadNetworks(ctx context.Context) {
	n := v.networks
	if n == nil {
		return
	}
	n.pendingDelete = ""
	if store, ok := v.store.(vaultstate.NetworkProfiles); ok {
		n.store = store
	}
	if n.store == nil {
		n.listStatus, n.problem = vaultFailed, networkUnavailable
		return
	}
	n.listStatus = vaultLoading
	profiles, err := n.store.ListNetworkProfiles(ctx)
	if err != nil {
		// A Vault service wired without Network Profile persistence still
		// serves the other sections. This page reports its own unavailable
		// state instead of failing the Shell's whole Load.
		n.listStatus, n.problem = vaultFailed, "Could not load Network Profiles."
		return
	}
	n.profiles, n.listStatus, n.problem = profiles, vaultReady, ""
}

// networksSummary is the Vault landing row's second line.
func (v *vaultPages) networksSummary() string {
	if v.networks == nil {
		return "Unavailable"
	}
	switch v.networks.listStatus {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(v.networks.profiles) {
	case 0:
		return "No Network Profiles yet"
	case 1:
		return "1 Network Profile"
	default:
		return fmt.Sprintf("%d Network Profiles", len(v.networks.profiles))
	}
}

// drawNetworks renders the Networks section: the list, its editor, and the
// delete confirmation. The editor replaces the list so a half-filled form
// cannot be mistaken for a saved profile.
func (v *vaultPages) drawNetworks(c *ui.Context) {
	n := v.networks
	if n == nil || n.store == nil {
		width := pageContentWidth(c)
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, networkUnavailable, nil)
			})
		})
		return
	}
	if n.draft != nil {
		v.drawNetworkDraft(c)
		return
	}
	v.drawNetworkList(c)
}

func (v *vaultPages) drawNetworkList(c *ui.Context) {
	n := v.networks
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			v.refreshBar(c, "Network Profiles")
			if kit.GhostButton(c, icons.Plus, "Add Tailscale profile").Clicked() {
				n.beginCreate(domainvault.NetworkKindTailscale)
			}
			if kit.GhostButton(c, icons.Plus, "Add WireGuard profile").Clicked() {
				n.beginCreate(domainvault.NetworkKindWireGuard)
			}
			if problem := n.lastActionProblem(); problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, problem, nil)
			}
			switch n.listStatus {
			case vaultLoading:
				kit.EmptyHint(c, "Loading Network Profiles…")
				return
			case vaultFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, n.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						v.loadNetworks(context.Background())
					}
				})
				if len(n.profiles) == 0 {
					return
				}
			default:
				if n.problem != "" {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, n.problem, nil)
				}
			}
			if len(n.profiles) == 0 {
				kit.EmptyHint(c, "No Network Profiles yet. Profiles added on the desktop appear here.")
				return
			}
			for _, profile := range n.profiles {
				profile := profile
				v.drawNetworkRow(c, profile)
			}
			if n.pendingDelete != "" {
				v.drawNetworkDelete(c)
			}
		})
	})
}

func (v *vaultPages) drawNetworkRow(c *ui.Context, profile domainvault.NetworkProfile) {
	n := v.networks
	name := networkProfileName(profile)
	row := kit.ListRow(c).Label("Edit profile " + name)
	handled := false
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, name).FontWeight(600).SingleLine()
			ui.Text(c, networkProfileSummary(profile)).FontSize(12).
				TextColor(c.Theme().TextMuted).SingleLine()
			if profile.Kind == domainvault.NetworkKindTailscale {
				if v.drawNetworkProviderControls(c, profile) {
					handled = true
				}
			}
		})
		if kit.IconButton(c, icons.Trash, "Delete profile "+name).Clicked() {
			n.pendingDelete = profile.ID
			handled = true
		}
		ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
	})
	if !handled && row.Clicked() {
		n.beginEdit(profile)
	}
}

// drawNetworkProviderControls renders one Tailscale profile's current Provider
// status and the actions that apply to it. It reports whether one of those
// actions was tapped, so the surrounding row does not also open the editor.
func (v *vaultPages) drawNetworkProviderControls(c *ui.Context,
	profile domainvault.NetworkProfile) bool {

	n := v.networks
	status, ok := n.status(profile.ID)
	if !ok {
		if kit.GhostButton(c, icons.LogIn, "Sign in").Clicked() {
			n.startProvider(c, profile.ID)
			return true
		}
		return false
	}
	phase := uiconnection.ProviderPhaseText(locales.In("en"), status.Phase)
	if phase == "" {
		phase = string(status.Phase)
	}
	if address := status.Address(); address != "" {
		phase += " · " + address
	}
	if phase != "" {
		ui.Text(c, phase).FontSize(12).TextColor(c.Theme().TextMuted)
	}
	acted := false
	if status.AuthURL != "" {
		ui.Text(c, "Sign in at "+status.AuthURL).FontSize(12).
			TextColor(c.Theme().TextMuted)
		ui.Row(c).Gap(6).Wrap().Children(func() {
			if kit.GhostButton(c, icons.Copy, "Copy login URL").Clicked() {
				if n.copyText != nil {
					n.copyText(status.AuthURL)
				}
				acted = true
			}
			if kit.GhostButton(c, icons.ExternalLink, "Open login URL").Clicked() {
				if err := n.openLoginURL(status.AuthURL); err != nil {
					n.setActionProblem("Could not open the login URL.")
					c.Invalidate()
				}
				acted = true
			}
		})
	}
	if status.LastError != "" {
		ui.Text(c, status.LastError).FontSize(12).
			TextColor(c.Theme().Danger)
	}
	ui.Row(c).Gap(6).Wrap().Children(func() {
		switch status.Phase {
		case appconnection.ProviderPhaseStarting, appconnection.ProviderPhaseNeedsLogin:
			if kit.GhostButton(c, icons.X, "Cancel").Clicked() {
				n.cancelProvider(c, profile.ID)
				acted = true
			}
		case appconnection.ProviderPhaseError, appconnection.ProviderPhaseStopped:
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				n.retryProvider(c, profile.ID)
				acted = true
			}
		}
	})
	return acted
}

// drawNetworkDelete renders the confirmation for the profile awaiting delete.
// The shared service clears every Host and Host Group reference, so the
// explanation says those records survive.
func (v *vaultPages) drawNetworkDelete(c *ui.Context) {
	n := v.networks
	name := n.pendingDeleteName()
	cancelled, confirmed := kit.ConfirmBar(c,
		"Delete the Network Profile "+name+"?",
		func() {
			kit.EmptyHint(c, "Hosts and Host Groups that use it keep their other settings; the reference is cleared.")
		},
		"Cancel", "Delete")
	switch {
	case cancelled:
		n.pendingDelete = ""
	case confirmed:
		v.deleteNetwork(context.Background(), n.pendingDelete)
	}
}

// drawNetworkDraft renders the editor. The fields scroll while Save and Cancel
// stay below them, so a long peer list cannot hide the primary action.
func (v *vaultPages) drawNetworkDraft(c *ui.Context) {
	n := v.networks
	f := n.draft
	title := "New Network Profile"
	if f.editing {
		title = "Edit Network Profile"
	}
	ui.Column(c).Fill().Gap(10).Children(func() {
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Column(c).FillWidth().Padding(16).Gap(12).Children(func() {
				kit.SectionBar(c, title, nil)
				if n.draftProblem != "" {
					kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, n.draftProblem, nil)
				}
				if n.draftNotice != "" {
					kit.Notice(c, icons.CircleCheck, c.Theme().Success, n.draftNotice, nil)
				}
				kit.FormRow(c, "Label", func() {
					ui.TextInput(c, &f.label).Placeholder("A name you recognize").FillWidth()
				})
				if f.editing {
					kit.FormRow(c, "Kind", func() {
						ui.Text(c, networkKindLabel(domainvault.NetworkProfileKind(networkKindAt(f.kindIndex))))
					})
				} else {
					kit.FormRow(c, "Kind", func() {
						ui.Segmented(c, &f.kindIndex, "Tailscale", "WireGuard")
					}).Description("The kind decides which fields and secrets the profile stores.")
				}
				switch networkKindAt(f.kindIndex) {
				case domainvault.NetworkKindTailscale:
					v.drawTailscaleFields(c)
				case domainvault.NetworkKindWireGuard:
					v.drawWireGuardFields(c)
				}
			})
		})
		ui.Column(c).FillWidth().Padding(12, 16).Gap(8).Children(func() {
			if kit.PrimaryButton(c, "", "Save profile").Clicked() {
				v.submitNetwork()
			}
			if kit.SecondaryButton(c, "", "Cancel").Clicked() {
				n.cancelDraft()
			}
		})
	})
}

func (v *vaultPages) drawTailscaleFields(c *ui.Context) {
	n := v.networks
	f := n.draft
	kit.FormRow(c, "Hostname", func() {
		ui.TextInput(c, &f.hostname).Placeholder("netcatty-node").FillWidth()
	}).Description("The tailnet name for this device. Leave blank to let Tailscale choose.")
	authHint := "Stored encrypted in the Vault. Leave blank to keep it."
	if !f.hasAuthKey {
		authHint = "Optional: an Auth Key to join without an interactive login."
	}
	kit.FormRow(c, "Auth key", func() {
		ui.TextInput(c, &f.authKey).Password().FillWidth()
	}).Description(authHint)
	kit.FormRow(c, "Control URL", func() {
		ui.TextInput(c, &f.controlURL).Placeholder("https://controlplane.tailscale.com").FillWidth()
	}).Description("A custom Headscale control plane is experimental.")
	ui.Checkbox(c, &f.ephemeral, "Ephemeral node")
	kit.FormRow(c, "Tags", func() {
		ui.TextInput(c, &f.tags).Placeholder("tag:prod, tag:ops").FillWidth()
	}).Description("Comma-separated tags this node advertises.")
	ui.Text(c, "The embedded node uploads its logs to Tailscale's log service; this build cannot disable that.").
		FontSize(12).TextColor(c.Theme().TextMuted)
}

func (v *vaultPages) drawWireGuardFields(c *ui.Context) {
	n := v.networks
	f := n.draft
	if kit.SecondaryButton(c, icons.FilePlus, "Import .conf").Clicked() {
		n.importConf()
	}
	ui.Text(c, "Choose a wg-quick .conf with the system file picker. Nothing changes if you cancel.").
		FontSize(12).TextColor(c.Theme().TextMuted)
	privateHint := "Stored encrypted in the Vault. Leave blank to keep it."
	if !f.hasPrivateKey {
		privateHint = "The interface PrivateKey from the configuration."
	}
	kit.FormRow(c, "Private key", func() {
		ui.TextInput(c, &f.privateKey).Password().FillWidth()
	}).Description(privateHint)
	kit.FormRow(c, "Addresses", func() {
		ui.TextInput(c, &f.addresses).Placeholder("10.0.0.2/32, fd00::2/128").FillWidth()
	}).Description("Comma-separated interface addresses.")
	kit.FormRow(c, "DNS", func() {
		ui.TextInput(c, &f.dns).Placeholder("10.0.0.1").FillWidth()
	}).Description("A profile with no DNS reaches IP-literal Hosts only.")
	kit.FormRow(c, "MTU", func() {
		ui.TextInput(c, &f.mtu).Placeholder(strconv.Itoa(domainvault.DefaultWireGuardMTU)).Width(120)
	})
	kit.FormRow(c, "Listen port", func() {
		ui.TextInput(c, &f.listenPort).Placeholder("0").Width(120)
	})
	ui.Text(c, "Peers").FontSize(14).FontWeight(600)
	for index := range f.peers {
		v.drawNetworkPeer(c, index)
	}
	if kit.GhostButton(c, icons.Plus, "Add peer").Clicked() {
		f.peers = append(f.peers, networkPeerForm{})
	}
}

func (v *vaultPages) drawNetworkPeer(c *ui.Context, index int) {
	n := v.networks
	f := n.draft
	peer := &f.peers[index]
	ui.Column(c).FillWidth().Gap(6).Padding(8).Background(c.Theme().Surface).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("Peer %d", index+1)).FontWeight(600)
			ui.Box(c).Grow(1)
			if kit.GhostButton(c, icons.Trash, fmt.Sprintf("Remove peer %d", index+1)).Clicked() {
				n.removePeer(index)
			}
		})
		kit.FormRow(c, "Public key", func() {
			ui.TextInput(c, &peer.publicKey).FillWidth()
		})
		kit.FormRow(c, "Endpoint", func() {
			ui.TextInput(c, &peer.endpoint).Placeholder("vpn.example.com:51820").FillWidth()
		})
		kit.FormRow(c, "Allowed IPs", func() {
			ui.TextInput(c, &peer.allowedIPs).Placeholder("10.0.0.0/24, 0.0.0.0/0").FillWidth()
		})
		presharedHint := "Optional. Leave blank to keep a stored Preshared Key."
		if !peer.hasPresharedKey {
			presharedHint = "Optional: the peer's Preshared Key."
		}
		kit.FormRow(c, "Preshared key", func() {
			ui.TextInput(c, &peer.presharedKey).Password().FillWidth()
		}).Description(presharedHint)
		kit.FormRow(c, "Persistent keepalive", func() {
			ui.TextInput(c, &peer.keepalive).Placeholder("25").Width(120)
		})
	})
}

// beginCreate opens the editor for one Kind. Tailscale and WireGuard have
// separate entry buttons, so the Kind never has to be guessed.
func (n *networkPages) beginCreate(kind domainvault.NetworkProfileKind) {
	n.draft = &networkDraftForm{
		kindIndex: networkKindIndex(kind),
	}
	if kind == domainvault.NetworkKindWireGuard {
		n.draft.peers = []networkPeerForm{{}}
	}
	n.draftProblem = ""
	n.draftNotice = ""
	n.pendingDelete = ""
}

// beginEdit opens a saved profile without loading any credential plaintext:
// the Has* markers tell the editor what is stored, and a blank credential
// field keeps it.
func (n *networkPages) beginEdit(profile domainvault.NetworkProfile) {
	form := &networkDraftForm{
		id:        profile.ID,
		label:     profile.Label,
		kindIndex: networkKindIndex(profile.Kind),
		editing:   true,
	}
	switch profile.Kind {
	case domainvault.NetworkKindTailscale:
		if profile.Tailscale != nil {
			form.hostname = profile.Tailscale.Hostname
			form.hasAuthKey = profile.Tailscale.HasAuthKey
			form.controlURL = profile.Tailscale.ControlURL
			form.ephemeral = profile.Tailscale.Ephemeral
			form.tags = strings.Join(profile.Tailscale.Tags, ", ")
		}
	case domainvault.NetworkKindWireGuard:
		if profile.WireGuard != nil {
			form.hasPrivateKey = profile.WireGuard.HasPrivateKey
			form.addresses = strings.Join(profile.WireGuard.Addresses, ", ")
			form.dns = strings.Join(profile.WireGuard.DNS, ", ")
			if profile.WireGuard.MTU != 0 {
				form.mtu = strconv.Itoa(profile.WireGuard.MTU)
			}
			if profile.WireGuard.ListenPort != 0 {
				form.listenPort = strconv.Itoa(profile.WireGuard.ListenPort)
			}
			for _, peer := range profile.WireGuard.Peers {
				row := networkPeerForm{
					id:              peer.ID,
					publicKey:       peer.PublicKey,
					hasPresharedKey: peer.HasPresharedKey,
					endpoint:        peer.Endpoint,
					allowedIPs:      strings.Join(peer.AllowedIPs, ", "),
				}
				if peer.PersistentKeepalive != 0 {
					row.keepalive = strconv.Itoa(peer.PersistentKeepalive)
				}
				form.peers = append(form.peers, row)
			}
		}
	}
	n.draft = form
	n.draftProblem = ""
	n.draftNotice = ""
	n.pendingDelete = ""
}

func (n *networkPages) cancelDraft() {
	n.draft = nil
	n.draftProblem = ""
	n.draftNotice = ""
}

func (n *networkPages) removePeer(index int) {
	if n.draft == nil || index < 0 || index >= len(n.draft.peers) {
		return
	}
	n.draft.peers = append(n.draft.peers[:index], n.draft.peers[index+1:]...)
}

// importConf reads one wg-quick file through the injected system picker and
// fills the editor from the shared parser. A cancelled picker returns before
// any field is touched; a parse or read failure reports a message and also
// leaves the form as it was.
func (n *networkPages) importConf() {
	f := n.draft
	if f == nil {
		return
	}
	if n.picker == nil {
		n.draftProblem = "Importing a .conf is not available on this device."
		return
	}
	name, content, err := n.picker.PickWireGuardConf()
	if err != nil {
		if errors.Is(err, vaultstate.ErrNetworkConfTooLarge) {
			n.draftProblem = "The selected .conf is too large to import."
		} else {
			n.draftProblem = "Could not read the selected .conf."
		}
		return
	}
	if name == "" {
		// The system picker was cancelled. Leave every field exactly as it was.
		return
	}
	if len(content) > wireguardconf.MaxFileBytes {
		n.draftProblem = "The selected .conf is too large to import."
		return
	}
	config, _, parseErr := wireguardconf.Parse(content)
	if parseErr != nil {
		n.draftProblem = "The selected .conf is not a valid wg-quick configuration."
		return
	}
	applyWireGuardConf(f, config)
	n.draftProblem = ""
	n.draftNotice = "Imported " + networkConfBaseName(name) + ". Review the profile and save it."
}

func applyWireGuardConf(f *networkDraftForm, config *wireguardconf.Config) {
	f.kindIndex = networkKindWireGuardIndex
	f.privateKey = wireguardconf.EncodeKey(config.Interface.PrivateKey)
	f.addresses = joinNetworkValues(config.Interface.Addresses)
	f.dns = joinNetworkValues(config.Interface.DNS)
	mtu := config.Interface.MTU
	if mtu == 0 {
		mtu = domainvault.DefaultWireGuardMTU
	}
	f.mtu = strconv.Itoa(int(mtu))
	f.listenPort = ""
	if config.Interface.ListenPort != 0 {
		f.listenPort = strconv.Itoa(int(config.Interface.ListenPort))
	}
	f.peers = make([]networkPeerForm, 0, len(config.Peers))
	for _, peer := range config.Peers {
		row := networkPeerForm{
			publicKey:  wireguardconf.EncodeKey(peer.PublicKey),
			allowedIPs: joinNetworkValues(peer.AllowedIPs),
		}
		// A file may omit the Endpoint. The zero value must stay an empty
		// field: ":0" is not a valid endpoint.
		if peer.Endpoint.Host != "" && peer.Endpoint.Port != 0 {
			row.endpoint = peer.Endpoint.String()
		}
		if peer.PresharedKey != nil {
			row.presharedKey = wireguardconf.EncodeKey(*peer.PresharedKey)
		}
		if peer.PersistentKeepalive != 0 {
			row.keepalive = strconv.Itoa(int(peer.PersistentKeepalive))
		}
		f.peers = append(f.peers, row)
	}
}

// submitNetwork writes the draft through the shared Vault service. Validation
// is the domain's, so the phone accepts exactly what the desktop accepts.
func (v *vaultPages) submitNetwork() {
	n := v.networks
	f := n.draft
	if f == nil {
		return
	}
	if n.store == nil {
		n.draftProblem = networkUnavailable
		return
	}
	draft, problem := f.domainDraft()
	if problem != "" {
		n.draftProblem = problem
		return
	}
	var err error
	if f.id == "" {
		_, err = n.store.CreateNetworkProfile(context.Background(), draft)
	} else {
		_, err = n.store.UpdateNetworkProfile(context.Background(), f.id, draft)
	}
	if err != nil {
		n.draftProblem = networkSaveProblemText(err)
		return
	}
	n.cancelDraft()
	_ = v.load(context.Background())
}

// domainDraft turns the editor's text into the shared draft and pre-validates
// it, so a malformed number is explained before it reaches the service.
func (f *networkDraftForm) domainDraft() (domainvault.NetworkProfileDraft, string) {
	kind := networkKindAt(f.kindIndex)
	draft := domainvault.NetworkProfileDraft{Label: f.label, Kind: kind}
	switch kind {
	case domainvault.NetworkKindTailscale:
		draft.Tailscale = &domainvault.TailscaleNetworkDraft{
			Hostname:   f.hostname,
			AuthKey:    f.authKey,
			ControlURL: f.controlURL,
			Ephemeral:  f.ephemeral,
			Tags:       splitNetworkList(f.tags),
		}
	case domainvault.NetworkKindWireGuard:
		mtu := 0
		if trimmed := strings.TrimSpace(f.mtu); trimmed != "" {
			parsed, err := strconv.Atoi(trimmed)
			if err != nil {
				return domainvault.NetworkProfileDraft{}, "Enter a valid MTU."
			}
			mtu = parsed
		}
		listenPort := 0
		if trimmed := strings.TrimSpace(f.listenPort); trimmed != "" {
			parsed, err := strconv.Atoi(trimmed)
			if err != nil || parsed < 0 || parsed > 65535 {
				return domainvault.NetworkProfileDraft{}, "Enter a listen port between 0 and 65535."
			}
			listenPort = parsed
		}
		config := &domainvault.WireGuardNetworkDraft{
			PrivateKey: f.privateKey,
			Addresses:  splitNetworkList(f.addresses),
			DNS:        splitNetworkList(f.dns),
			MTU:        mtu,
			ListenPort: listenPort,
		}
		for _, row := range f.peers {
			keepalive := 0
			if trimmed := strings.TrimSpace(row.keepalive); trimmed != "" {
				parsed, err := strconv.Atoi(trimmed)
				if err != nil || parsed < 0 || parsed > 65535 {
					return domainvault.NetworkProfileDraft{}, "Enter a persistent keepalive between 0 and 65535."
				}
				keepalive = parsed
			}
			config.Peers = append(config.Peers, domainvault.WireGuardPeerDraft{
				ID:                  row.id,
				PublicKey:           row.publicKey,
				PresharedKey:        row.presharedKey,
				Endpoint:            row.endpoint,
				AllowedIPs:          splitNetworkList(row.allowedIPs),
				PersistentKeepalive: keepalive,
			})
		}
		draft.WireGuard = config
	}
	normalized, err := domainvault.NormalizeNetworkProfileDraft(draft)
	if err != nil {
		return domainvault.NetworkProfileDraft{}, networkDraftProblemText(kind, err)
	}
	return normalized, ""
}

func networkDraftProblemText(kind domainvault.NetworkProfileKind, err error) string {
	var invalid *domainvault.ValidationError
	if !errors.As(err, &invalid) {
		return "The Vault could not save the Network Profile. Check the fields and try again."
	}
	if invalid.Field == domainvault.FieldLabel {
		return "Enter a name for the Network Profile."
	}
	switch kind {
	case domainvault.NetworkKindTailscale:
		return "Check the Tailscale hostname, Auth Key, control URL, and tags."
	case domainvault.NetworkKindWireGuard:
		return "Check the WireGuard keys, addresses, DNS, MTU, and peers. At least one peer is required."
	}
	return "Check the Network Profile's fields and try again."
}

func networkSaveProblemText(err error) string {
	var typed *domainvault.ValidationError
	if errors.As(err, &typed) {
		return networkDraftProblemText("", err)
	}
	return "Could not save the Network Profile. Check the fields and try again."
}

// deleteNetwork removes the profile from the shared store. The service clears
// every Host and Host Group reference in the same operation, so the Host lists
// are reloaded to show that immediately.
func (v *vaultPages) deleteNetwork(ctx context.Context, id string) {
	n := v.networks
	n.pendingDelete = ""
	if n.store == nil {
		n.problem = networkUnavailable
		return
	}
	if err := n.store.DeleteNetworkProfile(ctx, id); err != nil {
		n.problem = "Could not delete the Network Profile."
		return
	}
	_ = v.load(ctx)
	if v.hosts != nil {
		_ = v.hosts.load(ctx)
	}
}

func (n *networkPages) pendingDeleteName() string {
	for _, profile := range n.profiles {
		if profile.ID == n.pendingDelete {
			return networkProfileName(profile)
		}
	}
	return n.pendingDelete
}

// status reads the profile's latest non-secret Provider status. A build with
// no status source simply has no status to show, which is different from a
// profile whose Provider has not started.
func (n *networkPages) status(profileID string) (vaultstate.NetworkProviderStatus, bool) {
	if n.statusSource == nil {
		return vaultstate.NetworkProviderStatus{}, false
	}
	return n.statusSource.NetworkStatus(profileID)
}

// startProvider, cancelProvider, and retryProvider run the shared lifecycle
// off the UI thread, because a node join can block until the login URL or a
// failure arrives. The status source reflects the outcome; an immediate
// failure is reported on the page.
func (n *networkPages) startProvider(c *ui.Context, profileID string) {
	n.runProviderAction(c, "Could not start the Tailscale login.",
		func(ctx context.Context) error { return n.actions.StartNetworkProvider(ctx, profileID) })
}

func (n *networkPages) cancelProvider(c *ui.Context, profileID string) {
	n.runProviderAction(c, "Could not cancel the Tailscale login.",
		func(ctx context.Context) error { return n.actions.CancelNetworkProvider(ctx, profileID) })
}

func (n *networkPages) retryProvider(c *ui.Context, profileID string) {
	n.runProviderAction(c, "Could not retry the Tailscale login.",
		func(ctx context.Context) error { return n.actions.RetryNetworkProvider(ctx, profileID) })
}

func (n *networkPages) runProviderAction(c *ui.Context, failure string,
	action func(context.Context) error) {

	if n.actions == nil {
		n.setActionProblem("Tailscale login is not available on this device.")
		c.Invalidate()
		return
	}
	services := c.Services()
	go func() {
		if err := action(context.Background()); err != nil &&
			!errors.Is(err, context.Canceled) {
			n.setActionProblem(failure)
		}
		services.Invalidate()
	}()
}

func (n *networkPages) openLoginURL(url string) error {
	if n.openURL == nil {
		return errors.New("no URL opener")
	}
	return n.openURL(url)
}

func (n *networkPages) setActionProblem(problem string) {
	n.actionMu.Lock()
	n.actionProblem = problem
	n.actionMu.Unlock()
}

func (n *networkPages) lastActionProblem() string {
	n.actionMu.Lock()
	defer n.actionMu.Unlock()
	return n.actionProblem
}

func networkKindIndex(kind domainvault.NetworkProfileKind) int {
	if kind == domainvault.NetworkKindWireGuard {
		return networkKindWireGuardIndex
	}
	return networkKindTailscaleIndex
}

func networkKindAt(index int) domainvault.NetworkProfileKind {
	if index == networkKindWireGuardIndex {
		return domainvault.NetworkKindWireGuard
	}
	return domainvault.NetworkKindTailscale
}

func networkKindLabel(kind domainvault.NetworkProfileKind) string {
	switch kind {
	case domainvault.NetworkKindTailscale:
		return "Tailscale Net"
	case domainvault.NetworkKindWireGuard:
		return "WireGuard Net"
	default:
		return string(kind)
	}
}

// networkProfileName is the label a user recognizes, or the ID for an
// unlabelled profile.
func networkProfileName(profile domainvault.NetworkProfile) string {
	if label := strings.TrimSpace(profile.Label); label != "" {
		return label
	}
	return profile.ID
}

// networkProfileSummary names the Kind and the non-secret endpoint a list row
// can show. It never includes key material: the record carries none.
func networkProfileSummary(profile domainvault.NetworkProfile) string {
	kind := networkKindLabel(profile.Kind)
	var detail string
	switch profile.Kind {
	case domainvault.NetworkKindTailscale:
		if profile.Tailscale != nil {
			detail = profile.Tailscale.Hostname
			if detail == "" {
				detail = profile.Tailscale.ControlURL
			}
		}
	case domainvault.NetworkKindWireGuard:
		if profile.WireGuard != nil {
			detail = strings.Join(profile.WireGuard.Addresses, ", ")
			if detail == "" && len(profile.WireGuard.Peers) > 0 {
				detail = profile.WireGuard.Peers[0].Endpoint
			}
		}
	}
	if detail == "" {
		return kind
	}
	return kind + " · " + detail
}

// splitNetworkList splits a comma- or newline-separated editor field into
// values, trimming whitespace and dropping empties.
func splitNetworkList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinNetworkValues[T interface{ String() string }](values []T) string {
	rendered := make([]string, 0, len(values))
	for _, value := range values {
		rendered = append(rendered, value.String())
	}
	return strings.Join(rendered, ", ")
}

// networkConfBaseName strips directory components for display.
func networkConfBaseName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	if index := strings.LastIndexByte(name, '/'); index >= 0 {
		return name[index+1:]
	}
	return name
}
