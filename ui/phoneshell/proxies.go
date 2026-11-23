package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// ProxyProfileStore is the shared Vault service's Proxy Profile half. The
// application Vault service implements it over the same SQLite store the
// desktop reads, so a Profile created or edited on the phone appears on the
// desktop after a sync and vice versa.
type ProxyProfileStore interface {
	ListProxyProfiles(ctx context.Context) ([]domainvault.ProxyProfile, error)
	CreateProxyProfile(ctx context.Context, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error)
	UpdateProxyProfile(ctx context.Context, id string, draft domainvault.ProxyProfileDraft) (domainvault.ProxyProfile, error)
	DeleteProxyProfile(ctx context.Context, id string) error
}

// directProxyChoice is the radio sentinel for an explicit direct connection.
// It never reaches the Vault: the editor translates it into DirectProxy and an
// empty Profile reference, exactly as the desktop does.
const directProxyChoice = "\x00direct"

// proxyDraftForm is the plaintext editor of one Proxy Profile. The password
// is not exposed on the phone: a Profile that carries one keeps it, and a new
// password can only be set on the desktop.
type proxyDraftForm struct {
	id        string
	label     string
	typeIndex int
	host      string
	port      string
	command   string
	username  string
	editing   bool
}

// proxyTypeOptions is the order of the editor's type choice.
var proxyTypeOptions = []domainvault.ProxyType{
	domainvault.ProxyCommand,
	domainvault.ProxyHTTP,
	domainvault.ProxySOCKS5,
}

func proxyTypeIndex(t domainvault.ProxyType) int {
	for i, candidate := range proxyTypeOptions {
		if candidate == t {
			return i
		}
	}
	return len(proxyTypeOptions) - 1
}

// proxyTypeAt reads the editor's choice defensively: a corrupt index falls
// back to SOCKS5 rather than panicking the page.
func proxyTypeAt(index int) domainvault.ProxyType {
	if index < 0 || index >= len(proxyTypeOptions) {
		return domainvault.ProxySOCKS5
	}
	return proxyTypeOptions[index]
}

// drawProxies renders the Proxies section: the Proxy Profile list, its editor,
// and the delete confirmation. The list is the shared Vault's; the page keeps
// no local copy that could disagree with the desktop.
func (v *vaultPages) drawProxies(c *ui.Context) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			if v.proxyDraft != nil {
				v.drawProxyDraft(c)
				return
			}
			v.drawProxyList(c)
		})
	})
}

func (v *vaultPages) drawProxyList(c *ui.Context) {
	if v.store == nil || v.proxies == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, vaultUnavailable, nil)
		return
	}
	v.refreshBar(c, "Proxy Profiles")
	v.drawProxyCommandNotice(c)
	if kit.GhostButton(c, icons.Plus, "Add profile").Clicked() {
		v.beginCreateProxy()
	}
	v.drawProxyStatus(c)
}

// drawProxyStatus renders the list's loading, failure, and rows. A failed
// refresh keeps the rows already on screen under the message.
func (v *vaultPages) drawProxyStatus(c *ui.Context) {
	switch v.proxyStatus {
	case vaultLoading:
		kit.EmptyHint(c, "Loading Proxy Profiles…")
		return
	case vaultFailed:
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.proxyProblem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				_ = v.load(context.Background())
			}
		})
		if len(v.proxyProfiles) == 0 {
			return
		}
	default:
		if v.proxyProblem != "" {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.proxyProblem, nil)
		}
	}
	if len(v.proxyProfiles) == 0 {
		kit.EmptyHint(c, "No Proxy Profiles yet. Profiles added on the desktop appear here.")
		return
	}
	for _, profile := range v.proxyProfiles {
		profile := profile
		row := kit.ListRow(c).Label("Edit profile " + proxyProfileName(profile))
		handled := false
		row.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, proxyProfileName(profile)).FontWeight(600).SingleLine()
				ui.Text(c, proxyProfileSummary(profile)).FontSize(12).
					TextColor(c.Theme().TextMuted).SingleLine()
			})
			if kit.IconButton(c, icons.Trash, "Delete profile "+proxyProfileName(profile)).Clicked() {
				v.pendingProxy = profile.ID
				handled = true
			}
			ui.Text(c, "›").FontSize(20).TextColor(c.Theme().TextMuted)
		})
		if !handled && row.Clicked() {
			v.beginEditProxy(profile)
		}
	}
	if v.pendingProxy != "" {
		name := v.pendingProxyName()
		cancelled, confirmed := kit.ConfirmBar(c,
			"Delete the Proxy Profile "+name+"?",
			func() {
				kit.EmptyHint(c, "Hosts and Host Groups that use it keep their other settings; the reference is cleared.")
			},
			"Cancel", "Delete")
		switch {
		case cancelled:
			v.pendingProxy = ""
		case confirmed:
			v.deleteProxy(context.Background(), v.pendingProxy)
		}
	}
}

// drawProxyCommandNotice explains that a command Profile can be saved and
// synced even when this platform cannot execute it. It is informational: Save
// and sync stay enabled.
func (v *vaultPages) drawProxyCommandNotice(c *ui.Context) {
	reason := proxyCommandReason(v.proxyCommand)
	if reason == "" {
		return
	}
	kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
		reason+". You can still save and sync ProxyCommand profiles.", nil)
}

func (v *vaultPages) beginCreateProxy() {
	v.proxyDraft = &proxyDraftForm{
		typeIndex: proxyTypeIndex(domainvault.ProxySOCKS5),
		port:      "1080",
	}
	v.proxyDraftProblem = ""
	v.pendingProxy = ""
}

func (v *vaultPages) beginEditProxy(profile domainvault.ProxyProfile) {
	v.proxyDraft = &proxyDraftForm{
		id:        profile.ID,
		label:     profile.Label,
		typeIndex: proxyTypeIndex(profile.Config.Type),
		host:      profile.Config.Host,
		port:      proxyPortText(profile.Config.Port),
		command:   profile.Config.Command,
		username:  profile.Config.Username,
		editing:   true,
	}
	v.proxyDraftProblem = ""
	v.pendingProxy = ""
}

func (v *vaultPages) cancelProxyDraft() {
	v.proxyDraft = nil
	v.proxyDraftProblem = ""
}

func (v *vaultPages) drawProxyDraft(c *ui.Context) {
	f := v.proxyDraft
	title := "New Proxy Profile"
	if f.editing {
		title = "Edit Proxy Profile"
	}
	kit.SectionBar(c, title, nil)
	if v.proxyDraftProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, v.proxyDraftProblem, nil)
	}
	kit.FormRow(c, "Label", func() {
		ui.TextInput(c, &f.label).Placeholder("A name you recognize").FillWidth()
	})
	kit.FormRow(c, "Type", func() {
		ui.Segmented(c, &f.typeIndex, "ProxyCommand", "HTTP CONNECT", "SOCKS5")
	})
	switch proxyTypeAt(f.typeIndex) {
	case domainvault.ProxyCommand:
		kit.FormRow(c, "Command", func() {
			ui.TextArea(c, &f.command).Height(90).
				Placeholder("ssh -W %h:%p jump.example.com")
		}).Description("%h and %p are replaced with the target host and port.")
		if reason := proxyCommandReason(v.proxyCommand); reason != "" {
			kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
				reason+". You can still save and sync this profile.", nil)
		}
	default:
		kit.FormRow(c, "Host", func() {
			ui.TextInput(c, &f.host).Placeholder("proxy.example.com").FillWidth()
		})
		kit.FormRow(c, "Port", func() {
			ui.TextInput(c, &f.port).Placeholder("1080").Width(120)
		})
		kit.FormRow(c, "Username", func() {
			ui.TextInput(c, &f.username).FillWidth()
		})
	}
	if kit.PrimaryButton(c, "", "Save profile").Clicked() {
		v.submitProxy()
	}
	if kit.SecondaryButton(c, "", "Cancel").Clicked() {
		v.cancelProxyDraft()
	}
}

// submitProxy writes the draft through the shared Vault service. Validation is
// the domain's, so the phone accepts exactly what the desktop accepts.
func (v *vaultPages) submitProxy() {
	f := v.proxyDraft
	if f == nil {
		return
	}
	if v.proxies == nil {
		v.proxyDraftProblem = vaultUnavailable
		return
	}
	draft, problem := f.domainDraft()
	if problem != "" {
		v.proxyDraftProblem = problem
		return
	}
	var err error
	if f.id == "" {
		_, err = v.proxies.CreateProxyProfile(context.Background(), draft)
	} else {
		_, err = v.proxies.UpdateProxyProfile(context.Background(), f.id, draft)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			v.proxyDraftProblem = "Could not save the Proxy Profile. Try again."
			return
		}
		v.proxyDraftProblem = "Could not save the Proxy Profile. Check the fields and try again."
		return
	}
	v.cancelProxyDraft()
	_ = v.load(context.Background())
}

// domainDraft turns the editor's text into the shared draft and pre-validates
// it, so a malformed port is explained before it reaches the service.
func (f *proxyDraftForm) domainDraft() (domainvault.ProxyProfileDraft, string) {
	draft := domainvault.ProxyProfileDraft{Label: f.label}
	switch proxyTypeAt(f.typeIndex) {
	case domainvault.ProxyCommand:
		draft.Config = domainvault.ProxyConfig{
			Type:    domainvault.ProxyCommand,
			Command: f.command,
		}
	default:
		port := 0
		if trimmed := strings.TrimSpace(f.port); trimmed != "" {
			parsed, err := strconv.Atoi(trimmed)
			if err != nil {
				return domainvault.ProxyProfileDraft{}, "Enter a port between 1 and 65535."
			}
			port = parsed
		}
		draft.Config = domainvault.ProxyConfig{
			Type:     proxyTypeAt(f.typeIndex),
			Host:     f.host,
			Port:     port,
			Username: f.username,
		}
	}
	normalized, err := domainvault.NormalizeProxyProfileDraft(draft)
	if err != nil {
		return domainvault.ProxyProfileDraft{}, proxyDraftProblemText(err)
	}
	return normalized, ""
}

func proxyDraftProblemText(err error) string {
	var invalid *domainvault.ValidationError
	if !errors.As(err, &invalid) {
		return "The Vault could not save the Proxy Profile. Check the fields and try again."
	}
	switch invalid.Field {
	case domainvault.FieldLabel:
		return "Enter a name for the Proxy Profile."
	case domainvault.FieldProxy:
		switch invalid.Rule {
		case domainvault.RuleRequired:
			return "Fill in the command, or the proxy host and port."
		case domainvault.RuleOutOfRange:
			return "Enter a port between 1 and 65535."
		default:
			return "Check the Proxy Profile's type and fields."
		}
	default:
		return "The Vault could not save the Proxy Profile. Check the fields and try again."
	}
}

// deleteProxy removes the Profile from the shared store; the service clears
// every Host and Host Group reference in the same operation. The Host lists
// are reloaded so the cleared references show immediately.
func (v *vaultPages) deleteProxy(ctx context.Context, id string) {
	v.pendingProxy = ""
	if v.proxies == nil {
		v.proxyProblem = vaultUnavailable
		return
	}
	if err := v.proxies.DeleteProxyProfile(ctx, id); err != nil {
		v.proxyProblem = "Could not delete the Proxy Profile."
		return
	}
	_ = v.load(ctx)
	if v.hosts != nil {
		_ = v.hosts.load(ctx)
	}
}

func (v *vaultPages) pendingProxyName() string {
	for _, profile := range v.proxyProfiles {
		if profile.ID == v.pendingProxy {
			return proxyProfileName(profile)
		}
	}
	return v.pendingProxy
}

// proxiesSummary is the Vault landing row's second line.
func (v *vaultPages) proxiesSummary() string {
	switch v.proxyStatus {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(v.proxyProfiles) {
	case 0:
		return "No Proxy Profiles yet"
	case 1:
		return "1 Proxy Profile"
	default:
		return fmt.Sprintf("%d Proxy Profiles", len(v.proxyProfiles))
	}
}

// proxyProfileName is the label a user recognizes, or the ID for an
// unlabelled Profile.
func proxyProfileName(profile domainvault.ProxyProfile) string {
	if label := strings.TrimSpace(profile.Label); label != "" {
		return label
	}
	return profile.ID
}

// proxyProfileSummary names the type and, for a network proxy, its endpoint.
func proxyProfileSummary(profile domainvault.ProxyProfile) string {
	switch profile.Config.Type {
	case domainvault.ProxyCommand:
		if command := strings.TrimSpace(profile.Config.Command); command != "" {
			return "ProxyCommand · " + command
		}
		return "ProxyCommand"
	case domainvault.ProxyHTTP, domainvault.ProxySOCKS5:
		endpoint := profile.Config.Host
		if profile.Config.Port != 0 {
			endpoint = fmt.Sprintf("%s:%d", endpoint, profile.Config.Port)
		}
		if profile.Config.Type == domainvault.ProxyHTTP {
			return "HTTP CONNECT · " + endpoint
		}
		return "SOCKS5 · " + endpoint
	default:
		return string(profile.Config.Type)
	}
}

// proxyPortText renders a stored port for the editor, leaving an unset port
// blank instead of showing zero.
func proxyPortText(port int) string {
	if port == 0 {
		return ""
	}
	return strconv.Itoa(port)
}
