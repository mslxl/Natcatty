package phoneshell

// The Port Forwarding pages. The Vault section owns the rule list, editor, and
// delete confirmation; it writes through the same application forwarding
// manager the desktop uses, so a rule changed on the phone is the rule a
// desktop reload reads. A Host's detail page and the Session Tools surface draw
// the same runtime restricted to that Host, where the actions are start, stop,
// and retry.
//
// Runtime status is never persisted. Every page reads the manager's snapshot
// while it draws, so a rebuild that completes between frames shows its result
// without the page keeping a second copy that could disagree.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
)

// ForwardingService is the Port Forwarding use-case seam the phone pages need.
// The shared application forwarding manager implements it; tests provide a
// fake. It is the same runtime a Session's forwarding channel uses, so a rule
// started here shares the connection pool, SSH routes, and bind errors.
type ForwardingService interface {
	List(ctx context.Context) ([]appforwarding.View, error)
	Create(ctx context.Context, draft domainforward.Draft) (domainforward.Rule, error)
	Update(ctx context.Context, id model.ForwardingRuleID, draft domainforward.Draft) (domainforward.Rule, error)
	Delete(ctx context.Context, id model.ForwardingRuleID) error
	Start(ctx context.Context, id model.ForwardingRuleID) error
	Stop(ctx context.Context, id model.ForwardingRuleID) error
	Snapshot() appforwarding.Snapshot
	Subscribe(fn func(appforwarding.Event)) func()
}

// ForwardingProvider is the phone Session runtime's forwarding seam. The Shell
// takes the service from the Session opener when the composition root did not
// pass one explicitly, which keeps the phone root's wiring to one option.
type ForwardingProvider interface {
	ForwardingService() ForwardingService
}

// WithForwarding gives the Vault and Host pages the shared Port Forwarding
// runtime. Without it the section and Host entries say forwarding is not
// available on this device; no other page is affected.
func WithForwarding(service ForwardingService) Option {
	return func(s *Shell) { s.vault.forwarding.service = service }
}

// forwardingForm is one open Port Forwarding Rule editor. Ports stay strings
// so a malformed value produces a message instead of silently becoming zero.
type forwardingForm struct {
	id         model.ForwardingRuleID
	kind       string
	label      string
	hostID     string
	bind       string
	localPort  string
	remoteHost string
	remotePort string
	autoStart  bool
	// reconnect is the explicit auto-reconnect choice. Saving a value equal to
	// autoStart writes the original tri-state as nil, so "reconnect follows
	// auto-start" stays the default.
	reconnect bool
	enabled   bool
}

// forwardingPages owns the Port Forwarding Rule list and the Vault section's
// editor state. A Tools surface keeps its own instance in its per-Pane state,
// so opening a rule's editor in one place never changes another surface.
type forwardingPages struct {
	service ForwardingService
	// hosts resolves a rule's Host label and offers the eligible Hosts to the
	// editor. It is nil for a Tools surface, which always acts on the Session's
	// own Host.
	hosts *hostPages

	status  vaultStatus
	problem string
	loaded  bool
	views   []appforwarding.View

	editing     bool
	form        forwardingForm
	formProblem string

	pendingDelete model.ForwardingRuleID
	actionProblem string
}

func newForwardingPages() *forwardingPages {
	return &forwardingPages{status: vaultLoading}
}

// load reads the shared rule list. A nil service is this page's own
// unavailable state, not a reason the rest of the Vault fails to open.
func (f *forwardingPages) load(ctx context.Context) error {
	f.loaded = true
	if f.service == nil {
		f.status = vaultFailed
		f.problem = "Port forwarding rules are not available on this device."
		return nil
	}
	f.status = vaultLoading
	views, err := f.service.List(ctx)
	if err != nil {
		f.status = vaultFailed
		f.problem = "Could not load Port Forwarding Rules."
		return err
	}
	f.views = views
	f.status = vaultReady
	f.problem = ""
	return nil
}

// refresh replaces the listed rules with the manager's runtime snapshot. It
// runs while a page draws, so a status change that happened between frames is
// shown without a reload.
func (f *forwardingPages) refresh() {
	if f.service == nil || f.status != vaultReady {
		return
	}
	f.views = f.service.Snapshot().Rules
}

// summary is the Vault landing row's second line.
func (f *forwardingPages) summary() string {
	if f == nil {
		return "Not on the phone yet"
	}
	switch f.status {
	case vaultLoading:
		return "Loading…"
	case vaultFailed:
		return "Unavailable"
	}
	switch len(f.views) {
	case 0:
		return "No rules yet"
	case 1:
		return "1 rule"
	default:
		return fmt.Sprintf("%d rules", len(f.views))
	}
}

// available reports whether a Host detail page should offer the section.
func (f *forwardingPages) available() bool {
	return f != nil && f.service != nil
}

// draw renders the Vault's Forwarding page.
func (f *forwardingPages) draw(c *ui.Context) {
	width := pageContentWidth(c)
	ui.Scroll(c).Fill().Children(func() {
		ui.Column(c).Width(width).Padding(16).Gap(12).Children(func() {
			if f.editing {
				f.drawEditor(c)
				return
			}
			switch f.status {
			case vaultLoading:
				kit.EmptyHint(c, "Loading Port Forwarding Rules…")
				return
			case vaultFailed:
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, f.problem, func() {
					if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
						_ = f.load(context.Background())
					}
				})
				return
			}
			f.refresh()
			kit.SectionBar(c, "Port forwarding", func() {
				if kit.GhostButton(c, icons.Plus, "New rule").Clicked() {
					f.beginCreate("")
				}
				if kit.GhostButton(c, icons.RefreshCw, "Refresh").Clicked() {
					_ = f.load(context.Background())
				}
			})
			if f.problem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, f.problem, nil)
			}
			if f.actionProblem != "" {
				kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, f.actionProblem, nil)
			}
			f.drawDeleteConfirm(c)
			if len(f.views) == 0 {
				kit.EmptyHint(c, "No Port Forwarding Rules yet. Add one to reach a service through an SSH Host.")
				return
			}
			for _, view := range f.views {
				view := view
				f.drawRuleRow(c, view, nil, true)
			}
		})
	})
}

// drawHostRules renders the rules of one Host. It is drawn inside the Host
// detail page or the Tools surface, both of which already provide the Scroll
// and padded Column, so the section only adds its heading and rows.
func (f *forwardingPages) drawHostRules(c *ui.Context, host domainvault.Host) {
	if f.service == nil {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Warning,
			"Port forwarding is not available on this device.", nil)
		return
	}
	if !host.Protocol.SupportsForwarding() {
		kit.EmptyHint(c, "This Host cannot carry a forwarding channel.")
		return
	}
	if !f.loaded {
		_ = f.load(context.Background())
	}
	f.refresh()
	kit.SectionBar(c, "Port forwarding · "+hostName(host), func() {
		if kit.GhostButton(c, icons.RefreshCw, "Refresh").Clicked() {
			_ = f.load(context.Background())
		}
	})
	if f.status == vaultLoading {
		kit.EmptyHint(c, "Loading this Host's rules…")
		return
	}
	if f.status == vaultFailed {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, f.problem, func() {
			if kit.GhostButton(c, icons.RefreshCw, "Retry").Clicked() {
				_ = f.load(context.Background())
			}
		})
		return
	}
	if f.actionProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, f.actionProblem, nil)
	}
	rules := f.rulesFor(host.ID)
	if len(rules) == 0 {
		kit.EmptyHint(c, "No Port Forwarding Rules for this Host.")
		return
	}
	for _, view := range rules {
		view := view
		f.drawRuleRow(c, view, &host, false)
	}
}

// rulesFor filters the runtime view to one Host, keeping the manager's order.
func (f *forwardingPages) rulesFor(hostID model.HostID) []appforwarding.View {
	out := make([]appforwarding.View, 0, len(f.views))
	for _, view := range f.views {
		if view.Rule.HostID == hostID {
			out = append(out, view)
		}
	}
	return out
}

// drawRuleRow renders one rule and its actions. The Vault page also offers
// Edit and Delete there; a Host or Tools row offers only the runtime actions,
// because rule CRUD is centralized in the Vault.
func (f *forwardingPages) drawRuleRow(c *ui.Context, view appforwarding.View, host *domainvault.Host, editable bool) {
	rule := view.Rule
	t := c.Theme()
	kit.ListRow(c).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, rule.Label).FontWeight(600).SingleLine()
			ui.Text(c, f.ruleSummary(rule, host)).FontSize(12).
				TextColor(t.TextMuted).SingleLine()
			if text, danger := forwardingStatusText(view); text != "" {
				color := t.TextMuted
				if danger {
					color = t.Danger
				}
				ui.Text(c, text).FontSize(12).TextColor(color)
			}
		})
		switch view.Status {
		case domainforward.StatusActive, domainforward.StatusConnecting:
			if kit.IconButton(c, icons.Square, "Stop "+rule.Label).Clicked() {
				f.stopRule(rule.ID)
			}
		case domainforward.StatusError:
			if kit.IconButton(c, icons.RefreshCw, "Retry "+rule.Label).Clicked() {
				f.startRule(rule.ID)
			}
		default:
			if kit.IconButton(c, icons.Play, "Start "+rule.Label).Clicked() {
				f.startRule(rule.ID)
			}
		}
		if !editable {
			return
		}
		if kit.IconButton(c, icons.Pencil, "Edit rule "+rule.Label).Clicked() {
			f.beginEdit(view)
		}
		if kit.IconButton(c, icons.Trash, "Delete rule "+rule.Label).Clicked() {
			f.pendingDelete = rule.ID
			f.actionProblem = ""
		}
	})
}

// ruleSummary is the rule's direction, Host, listen address, and destination.
func (f *forwardingPages) ruleSummary(rule domainforward.Rule, host *domainvault.Host) string {
	parts := []string{forwardingTypeLabel(rule.Type), f.hostLabel(rule.HostID, host), rule.ListenAddress()}
	if rule.Type != domainforward.TypeDynamic {
		parts = append(parts, "→ "+rule.Destination())
	}
	if rule.AutoStart {
		parts = append(parts, "auto-start")
	}
	if !rule.IsEnabled() {
		parts = append(parts, "disabled")
	}
	return strings.Join(parts, " · ")
}

// hostLabel prefers the loaded Vault record, then the Host the section was
// opened for, and finally the stored identifier so a dangling rule is still
// identifiable.
func (f *forwardingPages) hostLabel(id model.HostID, host *domainvault.Host) string {
	if f.hosts != nil {
		if record, ok := f.hosts.host(string(id)); ok {
			return hostName(record)
		}
	}
	if host != nil && host.ID == id {
		return hostName(*host)
	}
	return string(id)
}

// forwardingStatusText returns the runtime status and whether it is a failure.
func forwardingStatusText(view appforwarding.View) (string, bool) {
	switch view.Status {
	case domainforward.StatusActive:
		return "Running", false
	case domainforward.StatusConnecting:
		return "Connecting…", false
	case domainforward.StatusInactive:
		return "Stopped", false
	case domainforward.StatusError:
		parts := []string{"Error"}
		if text := forwardingRuntimeErrorText(view.Error); text != "" {
			parts = append(parts, text)
		}
		if view.Hop != nil {
			parts = append(parts, fmt.Sprintf("hop %d of %d (%s)",
				view.Hop.Index, view.Hop.Total, view.Hop.Label))
		}
		return strings.Join(parts, " · "), true
	case domainforward.StatusUnknown:
		return "Unknown", false
	}
	return "", false
}

func forwardingRuntimeErrorText(kind appforwarding.ErrorKind) string {
	switch kind {
	case appforwarding.ErrorBindConflict:
		return "the listen address is already in use"
	case appforwarding.ErrorAuth:
		return "the Host rejected the credentials"
	case appforwarding.ErrorHostKey:
		return "the Host key was rejected"
	case appforwarding.ErrorNetwork:
		return "the network connection failed"
	case appforwarding.ErrorTimeout:
		return "the connection timed out"
	case appforwarding.ErrorUnsupported:
		return "this Host cannot forward ports"
	case appforwarding.ErrorCancelled:
		return "the attempt was cancelled"
	case appforwarding.ErrorUnforward:
		return "the old forward could not be released"
	case appforwarding.ErrorInternal:
		return "the forwarding runtime failed"
	}
	return ""
}

func forwardingTypeLabel(kind domainforward.Type) string {
	switch kind {
	case domainforward.TypeRemote:
		return "Remote"
	case domainforward.TypeDynamic:
		return "Dynamic"
	default:
		return "Local"
	}
}

// startRule starts one rule. A rule in error is retried through the same
// call; the manager accepts a new start once the previous attempt failed.
func (f *forwardingPages) startRule(id model.ForwardingRuleID) {
	if f.service == nil {
		return
	}
	if err := f.service.Start(context.Background(), id); err != nil {
		f.actionProblem = forwardingProblemText(err)
	} else {
		f.actionProblem = ""
	}
	f.refresh()
}

func (f *forwardingPages) stopRule(id model.ForwardingRuleID) {
	if f.service == nil {
		return
	}
	if err := f.service.Stop(context.Background(), id); err != nil {
		f.actionProblem = forwardingProblemText(err)
	} else {
		f.actionProblem = ""
	}
	f.refresh()
}

// drawDeleteConfirm renders the pending delete at the top of the Vault list.
func (f *forwardingPages) drawDeleteConfirm(c *ui.Context) {
	if f.pendingDelete == "" {
		return
	}
	label := string(f.pendingDelete)
	for _, view := range f.views {
		if view.Rule.ID == f.pendingDelete {
			label = view.Rule.Label
			break
		}
	}
	cancelled, confirmed := kit.ConfirmBar(c, "Delete the rule "+label+"?", func() {
		kit.EmptyHint(c, "The rule is removed from the Vault on every device.")
	}, "Cancel", "Delete")
	switch {
	case cancelled:
		f.pendingDelete = ""
	case confirmed:
		id := f.pendingDelete
		f.pendingDelete = ""
		if err := f.service.Delete(context.Background(), id); err != nil {
			f.actionProblem = forwardingProblemText(err)
			return
		}
		f.actionProblem = ""
		_ = f.load(context.Background())
	}
}

// beginCreate opens the editor for a new rule, optionally already bound to a
// Host the page was opened from.
func (f *forwardingPages) beginCreate(hostID model.HostID) {
	if f.service == nil {
		f.problem = "Port forwarding rules are not available on this device."
		return
	}
	host := string(hostID)
	if host == "" {
		if eligible := f.eligibleHosts(); len(eligible) > 0 {
			host = string(eligible[0].ID)
		}
	}
	f.editing = true
	f.formProblem = ""
	f.pendingDelete = ""
	f.actionProblem = ""
	f.form = forwardingForm{
		kind:      string(domainforward.TypeLocal),
		hostID:    host,
		bind:      domainforward.DefaultBindAddress,
		autoStart: false,
		reconnect: false,
		enabled:   true,
	}
}

// beginEdit opens the editor with a listed rule's values.
func (f *forwardingPages) beginEdit(view appforwarding.View) {
	rule := view.Rule
	f.editing = true
	f.formProblem = ""
	f.pendingDelete = ""
	f.actionProblem = ""
	f.form = forwardingForm{
		id:         rule.ID,
		kind:       string(rule.Type),
		label:      rule.Label,
		hostID:     string(rule.HostID),
		bind:       rule.BindAddress,
		localPort:  strconv.Itoa(rule.LocalPort),
		remoteHost: rule.RemoteHost,
		remotePort: forwardingPortText(rule.RemotePort),
		autoStart:  rule.AutoStart,
		reconnect:  rule.AutoReconnectEnabled(),
		enabled:    rule.IsEnabled(),
	}
}

func (f *forwardingPages) cancelEdit() {
	f.editing = false
	f.form = forwardingForm{}
	f.formProblem = ""
}

// eligibleHosts lists the Hosts a rule can target. A Telnet or Local Host has
// no SSH connection to carry the forwarding channel.
func (f *forwardingPages) eligibleHosts() []domainvault.Host {
	if f.hosts == nil {
		return nil
	}
	out := make([]domainvault.Host, 0, len(f.hosts.hosts))
	for _, host := range f.hosts.hosts {
		if host.Protocol.SupportsForwarding() {
			out = append(out, host)
		}
	}
	return out
}

// drawEditor renders the create or edit form.
func (f *forwardingPages) drawEditor(c *ui.Context) {
	form := &f.form
	title := "New Port Forwarding Rule"
	if form.id != "" {
		title = "Edit Port Forwarding Rule"
	}
	kit.SectionBar(c, title, nil)
	if f.formProblem != "" {
		kit.Notice(c, icons.TriangleAlert, c.Theme().Danger, f.formProblem, nil)
	}
	ui.Form(c, func() {
		kit.FormRow(c, "Type", func() {
			ui.RadioGroup(c, func() {
				ui.Radio(c, &form.kind, string(domainforward.TypeLocal), "Local")
				ui.Radio(c, &form.kind, string(domainforward.TypeRemote), "Remote")
				ui.Radio(c, &form.kind, string(domainforward.TypeDynamic), "Dynamic")
			})
		})
		kit.FormRow(c, "Label", func() {
			ui.TextInput(c, &form.label).Placeholder("web")
		})
		kit.FormRow(c, "Host", func() {
			hosts := f.eligibleHosts()
			if len(hosts) == 0 {
				ui.Text(c, "No Host in the Vault can forward ports.").FontSize(12).
					TextColor(c.Theme().Danger)
				return
			}
			ui.RadioGroup(c, func() {
				for _, host := range hosts {
					ui.Radio(c, &form.hostID, string(host.ID), hostName(host))
				}
			})
		})
		kit.FormRow(c, "Bind address", func() {
			ui.TextInput(c, &form.bind).Placeholder(domainforward.DefaultBindAddress).Width(200)
		})
		kit.FormRow(c, "Local port", func() {
			ui.TextInput(c, &form.localPort).Placeholder("8080").Width(120)
		})
		if form.kind != string(domainforward.TypeDynamic) {
			kit.FormRow(c, "Remote host", func() {
				ui.TextInput(c, &form.remoteHost).Placeholder("localhost").Width(200)
			})
			kit.FormRow(c, "Remote port", func() {
				ui.TextInput(c, &form.remotePort).Placeholder("80").Width(120)
			})
		}
		ui.Checkbox(c, &form.autoStart, "Start when the app launches")
		ui.Checkbox(c, &form.reconnect, "Reconnect after a failure")
		ui.Checkbox(c, &form.enabled, "Enabled")
	})
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if kit.PrimaryButton(c, icons.Check, "Save rule").Clicked() {
			f.save()
		}
		if kit.GhostButton(c, icons.X, "Cancel rule").Clicked() {
			f.cancelEdit()
		}
	})
}

// save validates the form through the shared manager and persists it. The
// manager owns normalization, so the phone never invents a second rule shape.
func (f *forwardingPages) save() {
	if f.service == nil {
		f.formProblem = "Port forwarding rules are not available on this device."
		return
	}
	draft, problem := f.draft()
	if problem != "" {
		f.formProblem = problem
		return
	}
	ctx := context.Background()
	var err error
	if f.form.id == "" {
		_, err = f.service.Create(ctx, draft)
	} else {
		_, err = f.service.Update(ctx, f.form.id, draft)
	}
	if err != nil {
		f.formProblem = forwardingProblemText(err)
		return
	}
	f.editing = false
	f.form = forwardingForm{}
	f.formProblem = ""
	_ = f.load(ctx)
}

// draft turns the editor's text into the shared draft. Ports are parsed here
// so a malformed value names the field the user must fix.
func (f *forwardingPages) draft() (domainforward.Draft, string) {
	kind := domainforward.Type(strings.TrimSpace(f.form.kind))
	if kind != domainforward.TypeLocal && kind != domainforward.TypeRemote &&
		kind != domainforward.TypeDynamic {
		return domainforward.Draft{}, "Choose Local, Remote, or Dynamic."
	}
	localPort, problem := forwardingParsePort(f.form.localPort)
	if problem != "" {
		return domainforward.Draft{}, "Local port " + problem
	}
	remotePort := 0
	if kind != domainforward.TypeDynamic {
		remotePort, problem = forwardingParsePort(f.form.remotePort)
		if problem != "" {
			return domainforward.Draft{}, "Remote port " + problem
		}
	}
	// nil keeps the original "autoReconnect ?? autoStart" rule: a value equal
	// to auto-start means the user did not choose an override.
	var autoReconnect *bool
	if f.form.reconnect != f.form.autoStart {
		value := f.form.reconnect
		autoReconnect = &value
	}
	enabled := f.form.enabled
	return domainforward.Draft{
		Label:         f.form.label,
		Type:          kind,
		BindAddress:   f.form.bind,
		LocalPort:     localPort,
		RemoteHost:    f.form.remoteHost,
		RemotePort:    remotePort,
		HostID:        model.HostID(strings.TrimSpace(f.form.hostID)),
		AutoStart:     f.form.autoStart,
		AutoReconnect: autoReconnect,
		Enabled:       &enabled,
	}, ""
}

func forwardingParsePort(value string) (int, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, "is required."
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, "must be a number."
	}
	if port < 1 || port > 65535 {
		return 0, "must be between 1 and 65535."
	}
	return port, ""
}

func forwardingPortText(port int) string {
	if port == 0 {
		return ""
	}
	return strconv.Itoa(port)
}

// forwardingProblemText maps a shared forwarding error to a phone message.
func forwardingProblemText(err error) string {
	if err == nil {
		return ""
	}
	var typed *appforwarding.Error
	if !errors.As(err, &typed) {
		return "The forwarding runtime failed."
	}
	switch typed.Kind {
	case appforwarding.KindNotFound:
		return "This rule is no longer in the Vault."
	case appforwarding.KindDisabled:
		return "This rule is disabled. Enable it before starting it."
	case appforwarding.KindValidation:
		if typed.Validation != nil {
			return forwardingValidationText(typed.Validation)
		}
		return "The rule is not valid."
	default:
		return "The forwarding runtime failed."
	}
}

func forwardingValidationText(invalid *domainforward.ValidationError) string {
	field := "A field"
	switch invalid.Field {
	case domainforward.FieldLabel:
		field = "The label"
	case domainforward.FieldType:
		field = "The type"
	case domainforward.FieldHost:
		field = "The Host"
	case domainforward.FieldBindAddress:
		field = "The bind address"
	case domainforward.FieldLocalPort:
		field = "The local port"
	case domainforward.FieldRemoteHost:
		field = "The remote host"
	case domainforward.FieldRemotePort:
		field = "The remote port"
	}
	switch invalid.Rule {
	case domainforward.RuleRequired:
		return field + " is required."
	case domainforward.RuleOutOfRange:
		return field + " is out of range."
	case domainforward.RuleTooLong:
		return field + " is too long."
	case domainforward.RuleUnsupported:
		return field + " is not supported."
	default:
		return field + " is not valid."
	}
}
