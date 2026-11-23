package phoneshell

import (
	"context"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// HostGroupStore is the Host Group half of the shared Vault. The Vault's Hosts
// family reads the groups to show drop targets and moves Hosts into one group
// through the same all-or-nothing operation the desktop uses. The application
// Vault service implements it over the same SQLite store.
type HostGroupStore interface {
	ListGroups(ctx context.Context) ([]domainvault.Group, error)
	AssignHostsGroup(ctx context.Context, hostIDs []model.HostID, groupID string) error
}

// VaultStore is the Vault use-case seam the phone pages need. The application
// Vault service implements it over the same SQLite store the desktop reads,
// so a Key or Known Host created or deleted on either device appears on the
// other after a reload.
//
// The domain records carry no private material: an SSH Key lists its public
// key, and a Known Host its public key and fingerprint. Nothing this page
// reads can render a private key or passphrase.
type VaultStore interface {
	HostGroupStore
	ListKeys(ctx context.Context) ([]domainvault.Key, error)
	DeleteKey(ctx context.Context, id model.KeyID) error
	ListKnownHosts(ctx context.Context) ([]domainconn.KnownHost, error)
	DeleteKnownHost(ctx context.Context, id string) error
}

// vaultStatus is the state of the Vault lists.
type vaultStatus int

const (
	vaultLoading vaultStatus = iota
	vaultReady
	vaultFailed
)

// vaultPages owns the Vault section's lists and their confirmations. The two
// lists load and fail independently: a Key read that fails leaves the Known
// Hosts page usable, and the Vault's other sections do not share its state.
type vaultPages struct {
	store VaultStore
	// hosts is the Vault's Hosts family. It reuses the Hosts tab's list and
	// detail pages instead of keeping a second copy of the same records.
	hosts *hostPages
	// hostDetails pushes one Host's detail page on the Vault section's router.
	// The Shell sets it, because the section owns the router.
	hostDetails func(domainvault.Host)

	keysStatus  vaultStatus
	keysProblem string
	keys        []domainvault.Key

	knownStatus  vaultStatus
	knownProblem string
	knownHosts   []domainconn.KnownHost

	pendingKey       model.KeyID
	pendingKnownHost string
}

func newVaultPages() *vaultPages {
	return &vaultPages{keysStatus: vaultLoading, knownStatus: vaultLoading}
}

// load replaces both lists from the shared store. The first call from the
// composition root observes vaultLoading; a test can render that state before
// calling Load. A failure is kept as that page's error state and returned for
// the caller to report, but the other list still loads.
func (v *vaultPages) load(ctx context.Context) error {
	v.pendingKey = ""
	v.pendingKnownHost = ""
	if v.store == nil {
		const unavailable = "The Vault is not available on this device."
		v.keysStatus, v.keysProblem = vaultFailed, unavailable
		v.knownStatus, v.knownProblem = vaultFailed, unavailable
		return nil
	}
	var firstErr error

	v.keysStatus = vaultLoading
	keys, err := v.store.ListKeys(ctx)
	if err != nil {
		v.keysStatus, v.keysProblem = vaultFailed, "Could not load SSH keys."
		firstErr = err
	} else {
		v.keys, v.keysStatus, v.keysProblem = keys, vaultReady, ""
	}

	v.knownStatus = vaultLoading
	known, err := v.store.ListKnownHosts(ctx)
	if err != nil {
		v.knownStatus, v.knownProblem = vaultFailed, "Could not load known hosts."
		if firstErr == nil {
			firstErr = err
		}
	} else {
		v.knownHosts, v.knownStatus, v.knownProblem = known, vaultReady, ""
	}
	return firstErr
}

func (v *vaultPages) keysSummary() string {
	switch v.keysStatus {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(v.keys) {
	case 0:
		return "No keys yet"
	case 1:
		return "1 key"
	default:
		return fmt.Sprintf("%d keys", len(v.keys))
	}
}

func (v *vaultPages) knownHostsSummary() string {
	switch v.knownStatus {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(v.knownHosts) {
	case 0:
		return "No known hosts yet"
	case 1:
		return "1 known host"
	default:
		return fmt.Sprintf("%d known hosts", len(v.knownHosts))
	}
}

// draw renders one Vault page. The landing page itself is the section list;
// every other real page owns its Scroll so one page's load state cannot blank
// another.
func (v *vaultPages) draw(c *ui.Context, p page) {
	switch p.path {
	case "/hosts":
		if v.hosts == nil {
			drawVaultUnavailable(c)
			return
		}
		v.hosts.drawVaultRoot(c, v.hostDetails)
	case "/keychain":
		v.drawKeys(c)
	case "/known-hosts":
		v.drawKnownHosts(c)
	}
}

// drawVaultUnavailable is the Hosts family's state when the Shell was built
// without it. No production root leaves it nil.
func drawVaultUnavailable(c *ui.Context) {
	width, _ := c.Size()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			kit.EmptyHint(c, "The Vault is not available on this device.")
		})
	})
}

func (v *vaultPages) drawKeys(c *ui.Context) {
	width, _ := c.Size()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			switch v.keysStatus {
			case vaultLoading:
				kit.EmptyHint(c, "Loading the Vault…")
				return
			case vaultFailed:
				// A failed read is recoverable: the phone may have been
				// locked, or the store briefly busy. Retry reads the same
				// shared store again instead of leaving the page stuck.
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.keysProblem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = v.load(context.Background())
					}
				})
				return
			}
			v.refreshBar(c, "SSH keys")
			if v.keysProblem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.keysProblem, nil)
			}
			v.drawKeyRows(c)
		})
	})
}

func (v *vaultPages) drawKeyRows(c *ui.Context) {
	if len(v.keys) == 0 {
		kit.EmptyHint(c, "No SSH keys yet. Keys added on the desktop appear here.")
		return
	}
	for _, key := range v.keys {
		key := key
		kit.ListRow(c).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, keyName(key)).FontWeight(600).SingleLine()
				ui.Text(c, keyDetail(key)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.Trash, "Delete key "+keyName(key)).Clicked() {
				v.pendingKey = key.ID
				v.pendingKnownHost = ""
			}
		})
	}
	if v.pendingKey != "" {
		name := v.pendingKeyName()
		cancelled, confirmed := kit.ConfirmBar(c,
			"Delete the key "+name+"?",
			func() {
				kit.EmptyHint(c, "Connections that use this key will fail until another key is chosen.")
			},
			"Cancel", "Delete")
		switch {
		case cancelled:
			v.pendingKey = ""
		case confirmed:
			v.deleteKey(context.Background(), v.pendingKey)
		}
	}
}

func (v *vaultPages) drawKnownHosts(c *ui.Context) {
	width, _ := c.Size()
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			switch v.knownStatus {
			case vaultLoading:
				kit.EmptyHint(c, "Loading the Vault…")
				return
			case vaultFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.knownProblem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = v.load(context.Background())
					}
				})
				return
			}
			v.refreshBar(c, "Known hosts")
			if v.knownProblem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.knownProblem, nil)
			}
			v.drawKnownHostRows(c)
		})
	})
}

func (v *vaultPages) drawKnownHostRows(c *ui.Context) {
	if len(v.knownHosts) == 0 {
		kit.EmptyHint(c, "No known hosts yet. Hosts you trust appear here.")
		return
	}
	for _, record := range v.knownHosts {
		record := record
		kit.ListRow(c).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, knownHostName(record)).FontWeight(600).SingleLine()
				ui.Text(c, knownHostDetail(record)).FontSize(12).TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.Trash, "Forget "+knownHostName(record)).Clicked() {
				v.pendingKnownHost = record.ID
				v.pendingKey = ""
			}
		})
	}
	if v.pendingKnownHost != "" {
		name := v.pendingKnownHostName()
		cancelled, confirmed := kit.ConfirmBar(c,
			"Forget "+name+"?",
			func() {
				kit.EmptyHint(c, "The next connection to this host will ask you to trust it again.")
			},
			"Cancel", "Forget")
		switch {
		case cancelled:
			v.pendingKnownHost = ""
		case confirmed:
			v.deleteKnownHost(context.Background(), v.pendingKnownHost)
		}
	}
}

// refreshBar draws a page heading with a Refresh action. It is how a change
// made on the desktop becomes visible while the phone app stays open.
func (v *vaultPages) refreshBar(c *ui.Context, title string) {
	kit.SectionBar(c, title, func() {
		if kit.GhostButton(c, icons.RefreshCw, "Refresh").Clicked() {
			_ = v.load(context.Background())
		}
	})
}

func (v *vaultPages) pendingKeyName() string {
	for _, key := range v.keys {
		if key.ID == v.pendingKey {
			return keyName(key)
		}
	}
	return ""
}

func (v *vaultPages) pendingKnownHostName() string {
	for _, record := range v.knownHosts {
		if record.ID == v.pendingKnownHost {
			return knownHostName(record)
		}
	}
	return ""
}

// deleteKey removes the Key from the shared store, then reloads the list. The
// deletion is what makes future connections fail to use it; the page never
// keeps a local copy that could disagree with the desktop.
func (v *vaultPages) deleteKey(ctx context.Context, id model.KeyID) {
	v.pendingKey = ""
	if v.store == nil {
		v.keysProblem = "The Vault is not available on this device."
		return
	}
	if err := v.store.DeleteKey(ctx, id); err != nil {
		v.keysProblem = "Could not delete the key."
		return
	}
	_ = v.load(ctx)
}

// deleteKnownHost revokes one trust decision in the shared store.
func (v *vaultPages) deleteKnownHost(ctx context.Context, id string) {
	v.pendingKnownHost = ""
	if v.store == nil {
		v.knownProblem = "The Vault is not available on this device."
		return
	}
	if err := v.store.DeleteKnownHost(ctx, id); err != nil {
		v.knownProblem = "Could not forget the known host."
		return
	}
	_ = v.load(ctx)
}

// keyName is the label a user recognizes, or the ID for an unlabelled key.
func keyName(key domainvault.Key) string {
	if label := strings.TrimSpace(key.Label); label != "" {
		return label
	}
	return string(key.ID)
}

// keyDetail is the non-secret identifying detail: the algorithm, the public
// key, and whether an encrypted passphrase accompanies it.
func keyDetail(key domainvault.Key) string {
	parts := make([]string, 0, 3)
	if key.Type != "" {
		parts = append(parts, string(key.Type))
	}
	if public := shortPublicKey(key.PublicKey); public != "" {
		parts = append(parts, public)
	}
	if key.HasPassphrase {
		parts = append(parts, "passphrase protected")
	}
	if len(parts) == 0 {
		return "No public key"
	}
	return strings.Join(parts, " · ")
}

// shortPublicKey shows the base64 blob's beginning. The public key is not a
// secret, so it may identify the entry; a private key is never in the record.
func shortPublicKey(public string) string {
	fields := strings.Fields(public)
	switch {
	case len(fields) >= 2:
		public = fields[1]
	case len(fields) == 1:
		public = fields[0]
	}
	if len(public) > 24 {
		public = public[:24] + "…"
	}
	return public
}

// knownHostName identifies a trust record by the endpoint it covers.
func knownHostName(record domainconn.KnownHost) string {
	if record.Port == 0 {
		return record.Hostname
	}
	return fmt.Sprintf("%s:%d", record.Hostname, record.Port)
}

// knownHostDetail shows the key type and fingerprint, both public.
func knownHostDetail(record domainconn.KnownHost) string {
	parts := make([]string, 0, 2)
	if record.KeyType != "" {
		parts = append(parts, record.KeyType)
	}
	if record.Fingerprint != "" {
		parts = append(parts, shortFingerprint(record.Fingerprint))
	}
	if len(parts) == 0 {
		return "No fingerprint"
	}
	return strings.Join(parts, " · ")
}

func shortFingerprint(fingerprint string) string {
	if len(fingerprint) > 28 {
		return fingerprint[:28] + "…"
	}
	return fingerprint
}
