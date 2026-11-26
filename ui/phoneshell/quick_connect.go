package phoneshell

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	launch "github.com/mslxl/natcatty/domain/model/launch"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// quickConnectPath is the Sessions home's Quick Connect form. It is a plain
// page rather than a /session/{id} route: the form is not a Tab, so it must
// never be mistaken for one when the lifecycle rule resolves the visible
// Session.
const quickConnectPath = "/quick-connect"

// QuickConnectOpener is implemented by an opener that can open a target with
// no Vault record. The host carries the parsed endpoint and has no ID; the
// secret is the form's one-shot credential (never stored). It is a separate
// capability from SessionOpener because a Vault-backed OpenHost resolves the
// Host by ID, which an Unsaved Host does not have.
type QuickConnectOpener interface {
	OpenTarget(ctx context.Context, host domainvault.Host, secret string) (SessionHandle, error)
}

// errQuickConnectEmpty is the Quick Connect form's own "nothing typed" failure.
// The shared parser has no target to reject, so the form names the missing
// input itself.
var errQuickConnectEmpty = errors.New("quick connect: empty target")

// errNoQuickConnectOpener reports a Shell whose opener cannot take an Unsaved
// Host directly. A Vault-backed opener that ignores the target would look the
// empty Host ID up and fail with a confusing message, so the Pane states the
// real limitation instead.
var errNoQuickConnectOpener = errors.New("this build cannot open a Quick Connect target")

// quickConnectState is the Sessions home's Quick Connect form. It lives with
// the Tabs because connecting opens one, and keeping the draft here lets a
// corrected target survive leaving and returning to the form.
type quickConnectState struct {
	target        string
	secret        string
	networkDevice bool
	problem       string
}

// parseQuickConnectTarget applies the shared desktop target parser to the one
// value the phone form collects. The parser accepts the same forms a desktop
// launch does: ssh:// and telnet:// URLs and the ssh-style user@host,
// host:port, and [v6]:port spellings. A value with whitespace is rejected:
// the field is one target, and the parser would otherwise read the whole
// sentence as a hostname.
func parseQuickConnectTarget(raw string) (launch.Target, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return launch.Target{}, errQuickConnectEmpty
	}
	if strings.ContainsAny(target, " \t\r\n") {
		return launch.Target{}, fmt.Errorf("%w: %q has spaces", launch.ErrInvalidTarget, target)
	}
	targets, problems := launch.ParseCommandLine([]string{target})
	if len(targets) == 0 {
		if len(problems) > 0 {
			return launch.Target{}, problems[0]
		}
		return launch.Target{}, fmt.Errorf("%w: %q", launch.ErrInvalidTarget, target)
	}
	parsed := targets[0]
	if parsed.Kind != launch.TargetSSH && parsed.Kind != launch.TargetTelnet {
		return launch.Target{}, fmt.Errorf("%w: %q", launch.ErrUnsupportedScheme, parsed.Kind)
	}
	if parsed.Hostname == "" {
		return launch.Target{}, fmt.Errorf("%w: missing host", launch.ErrInvalidTarget)
	}
	return parsed, nil
}

// quickConnectHost builds the Unsaved Host a parsed target names. It is the
// phone's counterpart of the desktop form's Unsaved Host construction: the ID
// stays empty, so the Session is never treated as a saved record. Telnet keeps
// its own default port; SSH uses the parser's default. Network-device mode is
// an SSH-only Host flag, so a Telnet target ignores it.
func quickConnectHost(target launch.Target, networkDevice bool) domainvault.Host {
	protocol := domainvault.ProtocolSSH
	port := target.Port
	switch target.Kind {
	case launch.TargetTelnet:
		protocol = domainvault.ProtocolTelnet
		if port == 0 {
			port = 23
		}
	default:
		if port == 0 {
			port = launch.DefaultSSHPort
		}
	}
	label := target.Hostname
	if target.Username != "" {
		label = target.Username + "@" + target.Hostname
	}
	host := domainvault.Host{
		Label:    label,
		Hostname: target.Hostname,
		Port:     port,
		Username: target.Username,
		Protocol: protocol,
	}
	if networkDevice && protocol == domainvault.ProtocolSSH {
		host.DeviceType = domainvault.DeviceTypeNetwork
	}
	return host
}

// quickConnectProblemText turns a parser failure into a correctable sentence.
// The raw parser error is never shown: the user gets what to change instead.
func quickConnectProblemText(err error) string {
	switch {
	case errors.Is(err, errQuickConnectEmpty):
		return "Enter a target to connect, such as user@host:2222 or ssh://user@host."
	case errors.Is(err, launch.ErrUnsupportedScheme):
		return "Only ssh:// and telnet:// targets connect. Remove the other scheme, then try again."
	default:
		return "Check the target's host and port (1-65535), then try again."
	}
}

// openQuickConnectForm pushes the form on the Sessions router. The entry is
// offered from the Sessions home, so that section is already showing.
func (s *Shell) openQuickConnectForm() {
	router := s.sections[Sessions].router
	if router.Path() == quickConnectPath {
		return
	}
	// The target is a convenience draft, but the credential is not: a form
	// abandoned by Cancel or the system Back gesture must not leave the typed
	// secret in memory for the next visit. The desktop form clears it on
	// reset for the same reason.
	s.sessions.quickConnect.secret = ""
	router.Push(quickConnectPath)
}

// drawQuickConnect renders the form. A rejected target stays here with a
// correctable problem; a valid one replaces the form with the new Tab's page,
// so Back from that Session returns to the Sessions home rather than an old
// form.
func (s *Shell) drawQuickConnect(c *ui.Context) {
	form := &s.sessions.quickConnect
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			kit.SectionBar(c, "Quick Connect", nil)
			kit.EmptyHint(c, "Open a temporary Session without saving a Host.")
			ui.TextInput(c.Key("quick-connect-target"), &form.target).
				Label("Target").
				Placeholder("user@host:port or ssh://user@host").
				FillWidth().AutoFocus()
			ui.TextInput(c.Key("quick-connect-secret"), &form.secret).
				Label("Password (optional)").
				Password().FillWidth()
			ui.Checkbox(c.Key("quick-connect-network-device"), &form.networkDevice,
				"Network device (SSH)")
			if form.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, form.problem, nil)
			}
			ui.Row(c).Gap(8).Children(func() {
				if kit.PrimaryButton(c, icons.Zap, "Connect").Clicked() {
					s.connectQuickConnect()
				}
				if kit.GhostButton(c, icons.X, "Cancel").Clicked() {
					form.secret = ""
					s.sections[Sessions].router.Back()
				}
			})
		})
	})
}

// connectQuickConnect validates the form and opens the target. A failure keeps
// the form and its draft, records the correctable problem, and creates no Tab;
// the next attempt re-validates the corrected value.
func (s *Shell) connectQuickConnect() {
	form := &s.sessions.quickConnect
	target, err := parseQuickConnectTarget(form.target)
	if err != nil {
		form.problem = quickConnectProblemText(err)
		return
	}
	secret := strings.TrimSpace(form.secret)
	if secret == "" {
		secret = target.Password
	}
	host := quickConnectHost(target, form.networkDevice)
	// The draft, including the one-shot credential, is discarded as soon as
	// the open starts; only the Pane's unpersisted field carries the secret.
	s.sessions.quickConnect = quickConnectState{}
	s.openQuickConnectTarget(host, secret)
}

// openQuickConnectTarget opens an Unsaved Host as a new Tab and selects it,
// using the same begin/select/push/launch flow a saved Host uses.
func (s *Shell) openQuickConnectTarget(host domainvault.Host, secret string) {
	tab, pane := s.sessions.beginQuickConnect(host, secret)
	s.Select(Sessions)
	router := s.sections[Sessions].router
	route := "/session/" + tab.id
	if router.Path() == quickConnectPath {
		router.Replace(route)
	} else {
		router.Push(route)
	}
	s.launchPane(tab.id, string(pane.id))
}
