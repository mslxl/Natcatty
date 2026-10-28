// Package forwarding renders and drives the Vault's Port Forwarding Rule
// surface. State holds behavior and never contains user-visible text; the view
// resolves every message through the catalogs.
package forwarding

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	appforwarding "github.com/mslxl/natcatty/application/forwarding"
	domainforward "github.com/mslxl/natcatty/domain/forwarding"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// Service is the forwarding use-case seam. The application manager implements
// it; tests provide a fake.
type Service interface {
	List(ctx context.Context) ([]appforwarding.View, error)
	Create(ctx context.Context, draft domainforward.Draft) (domainforward.Rule, error)
	Update(ctx context.Context, id model.ForwardingRuleID, draft domainforward.Draft) (domainforward.Rule, error)
	Delete(ctx context.Context, id model.ForwardingRuleID) error
	Start(ctx context.Context, id model.ForwardingRuleID) error
	Stop(ctx context.Context, id model.ForwardingRuleID) error
	Snapshot() appforwarding.Snapshot
	Subscribe(fn func(appforwarding.Event)) func()
}

// Hosts is the Vault Host list the rule editor references.
type Hosts interface {
	List(ctx context.Context) ([]domainvault.Host, error)
}

// Problem identifies a failure the view must localize.
type Problem string

const (
	ProblemNone               Problem = ""
	ProblemLabelRequired      Problem = "label_required"
	ProblemLabelInvalid       Problem = "label_invalid"
	ProblemType               Problem = "type"
	ProblemHostRequired       Problem = "host_required"
	ProblemBindInvalid        Problem = "bind_invalid"
	ProblemLocalPortInvalid   Problem = "local_port_invalid"
	ProblemLocalPortRange     Problem = "local_port_range"
	ProblemRemoteHostRequired Problem = "remote_host_required"
	ProblemRemoteHostInvalid  Problem = "remote_host_invalid"
	ProblemRemotePortInvalid  Problem = "remote_port_invalid"
	ProblemRemotePortRange    Problem = "remote_port_range"
	ProblemNotFound           Problem = "not_found"
	ProblemDisabled           Problem = "disabled"
	ProblemInternal           Problem = "internal"
)

// AutoReconnect choices. Follow is the original tri-state default: reconnect
// whenever auto-start is on. On and Off are explicit overrides.
const (
	AutoReconnectFollow = "follow"
	AutoReconnectOn     = "on"
	AutoReconnectOff    = "off"
)

// Form is the editable text of one rule. Ports stay strings so a malformed
// value produces a localized message instead of silently becoming zero.
type Form struct {
	ID            model.ForwardingRuleID
	Type          string
	Label         string
	HostID        string
	BindAddress   string
	LocalPort     string
	RemoteHost    string
	RemotePort    string
	AutoStart     bool
	AutoReconnect string
	Enabled       bool
}

// State is the forwarding surface's window-scoped state. It is mutated only
// on the UI thread; runtime events from other goroutines only request a
// redraw.
type State struct {
	service Service
	hosts   Hosts

	items         []appforwarding.View
	hostItems     []domainvault.Host
	editing       bool
	form          Form
	editorFields  editorFieldHandles
	problem       Problem
	pendingDelete model.ForwardingRuleID
	redraw        func()

	mu           sync.Mutex
	unsubscribe  func()
	lastRevision uint64
}

// Option customizes a State.
type Option func(*State)

// WithRedrawRequest sets the function called when the surface changes.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// NewState returns a forwarding surface over its ports.
func NewState(service Service, hosts Hosts, opts ...Option) *State {
	state := &State{service: service, hosts: hosts}
	for _, opt := range opts {
		opt(state)
	}
	if service != nil {
		// Events only wake the UI. Flush reads the authoritative snapshot, so
		// a dropped event cannot leave a stale status on screen.
		state.unsubscribe = service.Subscribe(func(appforwarding.Event) {
			state.requestRedraw()
		})
	}
	return state
}

// Close releases the runtime event subscription.
func (s *State) Close() {
	s.mu.Lock()
	unsubscribe := s.unsubscribe
	s.unsubscribe = nil
	s.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

// Reload reads the rule list and the Hosts the editor can reference.
func (s *State) Reload() error {
	if s.service == nil {
		s.problem = ProblemInternal
		s.requestRedraw()
		return errors.New("forwarding: no service")
	}
	items, err := s.service.List(context.Background())
	if err != nil {
		s.problem = ProblemInternal
		s.requestRedraw()
		return err
	}
	s.items = items
	if s.hosts != nil {
		hosts, err := s.hosts.List(context.Background())
		if err != nil {
			s.problem = ProblemInternal
			s.requestRedraw()
			return err
		}
		s.hostItems = hosts
	}
	s.requestRedraw()
	return nil
}

// Flush reads the latest runtime snapshot. The view calls it on the UI thread
// before rendering; reading every frame means a coalesced or dropped event
// cannot strand the surface on an old status.
func (s *State) Flush() {
	if s.service == nil {
		return
	}
	snapshot := s.service.Snapshot()
	if snapshot.Revision == s.lastRevision {
		return
	}
	s.lastRevision = snapshot.Revision
	s.items = snapshot.Rules
}

// Items returns the listed rules in display order.
func (s *State) Items() []appforwarding.View { return s.items }

// RuleCount returns the number of listed rules.
func (s *State) RuleCount() int { return len(s.items) }

// Hosts returns the Hosts an editor can reference.
func (s *State) Hosts() []domainvault.Host { return s.hostItems }

// Editing reports whether the rule editor is open.
func (s *State) Editing() bool { return s.editing }

// Form returns the editor's form for binding. It is valid while Editing.
func (s *State) Form() *Form { return &s.form }

// Problem returns the current failure, if any.
func (s *State) Problem() Problem { return s.problem }

// PendingDelete returns the rule awaiting delete confirmation, or "".
func (s *State) PendingDelete() model.ForwardingRuleID { return s.pendingDelete }

// BeginCreate opens the editor with a new rule.
func (s *State) BeginCreate() {
	s.editing = true
	s.form = Form{
		Type:          string(domainforward.TypeLocal),
		BindAddress:   domainforward.DefaultBindAddress,
		AutoReconnect: AutoReconnectFollow,
		Enabled:       true,
	}
	s.problem = ProblemNone
	s.pendingDelete = ""
	s.requestRedraw()
}

// BeginEdit opens the editor with a listed rule's values. It reports false
// when the rule is not listed.
func (s *State) BeginEdit(id model.ForwardingRuleID) bool {
	view, ok := s.find(id)
	if !ok {
		return false
	}
	rule := view.Rule
	s.editing = true
	s.problem = ProblemNone
	s.pendingDelete = ""
	reconnect := AutoReconnectFollow
	if rule.AutoReconnect != nil {
		reconnect = AutoReconnectOff
		if *rule.AutoReconnect {
			reconnect = AutoReconnectOn
		}
	}
	s.form = Form{
		ID:            rule.ID,
		Type:          string(rule.Type),
		Label:         rule.Label,
		HostID:        string(rule.HostID),
		BindAddress:   rule.BindAddress,
		LocalPort:     strconv.Itoa(rule.LocalPort),
		RemoteHost:    rule.RemoteHost,
		RemotePort:    portText(rule.RemotePort),
		AutoStart:     rule.AutoStart,
		AutoReconnect: reconnect,
		Enabled:       rule.IsEnabled(),
	}
	s.requestRedraw()
	return true
}

// CancelEdit closes the editor without saving.
func (s *State) CancelEdit() {
	s.editing = false
	s.form = Form{}
	s.problem = ProblemNone
	s.requestRedraw()
}

// Save validates the form through the application service and persists it.
func (s *State) Save() error {
	draft, problem := s.draft()
	if problem != ProblemNone {
		s.problem = problem
		s.requestRedraw()
		return nil
	}
	var err error
	if s.form.ID == "" {
		_, err = s.service.Create(context.Background(), draft)
	} else {
		_, err = s.service.Update(context.Background(), s.form.ID, draft)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.requestRedraw()
		return nil
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.editing = false
	s.form = Form{}
	s.problem = ProblemNone
	s.requestRedraw()
	return nil
}

// Start starts one listed rule.
func (s *State) Start(id model.ForwardingRuleID) bool {
	if _, ok := s.find(id); !ok {
		s.problem = ProblemNotFound
		s.requestRedraw()
		return false
	}
	s.problem = ProblemNone
	if err := s.service.Start(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.requestRedraw()
		return false
	}
	s.requestRedraw()
	return true
}

// Stop stops one listed rule.
func (s *State) Stop(id model.ForwardingRuleID) bool {
	if _, ok := s.find(id); !ok {
		s.problem = ProblemNotFound
		s.requestRedraw()
		return false
	}
	s.problem = ProblemNone
	if err := s.service.Stop(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.requestRedraw()
		return false
	}
	s.requestRedraw()
	return true
}

// ToggleEnabled flips a rule's durable enabled flag through an update.
func (s *State) ToggleEnabled(id model.ForwardingRuleID) bool {
	view, ok := s.find(id)
	if !ok {
		s.problem = ProblemNotFound
		s.requestRedraw()
		return false
	}
	draft := domainforward.DraftOf(view.Rule)
	enabled := !view.Rule.IsEnabled()
	draft.Enabled = &enabled
	if _, err := s.service.Update(context.Background(), id, draft); err != nil {
		s.problem = problemFor(err)
		s.requestRedraw()
		return false
	}
	s.problem = ProblemNone
	_ = s.Reload()
	return true
}

// RequestDelete opens the confirmation for one rule.
func (s *State) RequestDelete(id model.ForwardingRuleID) {
	s.pendingDelete = id
	s.problem = ProblemNone
	s.requestRedraw()
}

// CancelDelete closes the delete confirmation.
func (s *State) CancelDelete() {
	s.pendingDelete = ""
	s.requestRedraw()
}

// ConfirmDelete deletes the rule awaiting confirmation.
func (s *State) ConfirmDelete() error {
	if s.pendingDelete == "" {
		return nil
	}
	id := s.pendingDelete
	s.pendingDelete = ""
	if err := s.service.Delete(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.requestRedraw()
		return nil
	}
	if err := s.Reload(); err != nil {
		return err
	}
	s.problem = ProblemNone
	s.requestRedraw()
	return nil
}

// Status returns a rule's runtime status, or StatusUnknown when it is not
// listed.
func (s *State) Status(id model.ForwardingRuleID) domainforward.Status {
	if view, ok := s.find(id); ok {
		return view.Status
	}
	return domainforward.StatusUnknown
}

// RuntimeError returns a rule's runtime failure identifier.
func (s *State) RuntimeError(id model.ForwardingRuleID) appforwarding.ErrorKind {
	if view, ok := s.find(id); ok {
		return view.Error
	}
	return appforwarding.ErrorNone
}

// RuntimeHop returns the chain endpoint a runtime failure came from.
func (s *State) RuntimeHop(id model.ForwardingRuleID) *appforwarding.Hop {
	if view, ok := s.find(id); ok {
		return view.Hop
	}
	return nil
}

// HostLabel returns a Host's display label, or its id when the Host is gone.
func (s *State) HostLabel(id model.HostID) string {
	for _, host := range s.hostItems {
		if host.ID == id {
			return host.Label
		}
	}
	return string(id)
}

func (s *State) find(id model.ForwardingRuleID) (appforwarding.View, bool) {
	for _, view := range s.items {
		if view.Rule.ID == id {
			return view, true
		}
	}
	return appforwarding.View{}, false
}

// draft turns the form into a domain draft, decoding the text ports.
func (s *State) draft() (domainforward.Draft, Problem) {
	kind := domainforward.Type(strings.TrimSpace(s.form.Type))
	if kind != domainforward.TypeLocal && kind != domainforward.TypeRemote &&
		kind != domainforward.TypeDynamic {
		return domainforward.Draft{}, ProblemType
	}
	localPort, problem := parsePort(s.form.LocalPort, ProblemLocalPortInvalid, ProblemLocalPortRange)
	if problem != ProblemNone {
		return domainforward.Draft{}, problem
	}
	remotePort := 0
	if kind != domainforward.TypeDynamic {
		var remoteProblem Problem
		remotePort, remoteProblem = parsePort(s.form.RemotePort, ProblemRemotePortInvalid, ProblemRemotePortRange)
		if remoteProblem != ProblemNone {
			return domainforward.Draft{}, remoteProblem
		}
	}
	// nil keeps the original "autoReconnect ?? autoStart" tri-state.
	var autoReconnect *bool
	switch s.form.AutoReconnect {
	case AutoReconnectOn:
		value := true
		autoReconnect = &value
	case AutoReconnectOff:
		value := false
		autoReconnect = &value
	case AutoReconnectFollow, "":
	default:
		return domainforward.Draft{}, ProblemType
	}
	enabled := s.form.Enabled
	return domainforward.Draft{
		Label:         s.form.Label,
		Type:          kind,
		BindAddress:   s.form.BindAddress,
		LocalPort:     localPort,
		RemoteHost:    s.form.RemoteHost,
		RemotePort:    remotePort,
		HostID:        model.HostID(s.form.HostID),
		AutoStart:     s.form.AutoStart,
		AutoReconnect: autoReconnect,
		Enabled:       &enabled,
	}, ProblemNone
}

func parsePort(value string, invalid, outOfRange Problem) (int, Problem) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, invalid
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, invalid
	}
	if port < 1 || port > 65535 {
		return 0, outOfRange
	}
	return port, ProblemNone
}

func portText(port int) string {
	if port == 0 {
		return ""
	}
	return strconv.Itoa(port)
}

// problemFor maps a typed application error to the view's problem id.
func problemFor(err error) Problem {
	var typed *appforwarding.Error
	if !errors.As(err, &typed) {
		return ProblemInternal
	}
	switch typed.Kind {
	case appforwarding.KindNotFound:
		return ProblemNotFound
	case appforwarding.KindDisabled:
		return ProblemDisabled
	case appforwarding.KindValidation:
		var validation *domainforward.ValidationError
		if !errors.As(err, &validation) {
			return ProblemInternal
		}
		return problemForValidation(validation)
	default:
		return ProblemInternal
	}
}

func problemForValidation(invalid *domainforward.ValidationError) Problem {
	switch invalid.Field {
	case domainforward.FieldLabel:
		if invalid.Rule == domainforward.RuleRequired {
			return ProblemLabelRequired
		}
		return ProblemLabelInvalid
	case domainforward.FieldType:
		return ProblemType
	case domainforward.FieldHost:
		return ProblemHostRequired
	case domainforward.FieldBindAddress:
		return ProblemBindInvalid
	case domainforward.FieldLocalPort:
		if invalid.Rule == domainforward.RuleOutOfRange {
			return ProblemLocalPortRange
		}
		return ProblemLocalPortInvalid
	case domainforward.FieldRemoteHost:
		if invalid.Rule == domainforward.RuleRequired {
			return ProblemRemoteHostRequired
		}
		return ProblemRemoteHostInvalid
	case domainforward.FieldRemotePort:
		if invalid.Rule == domainforward.RuleOutOfRange {
			return ProblemRemotePortRange
		}
		return ProblemRemotePortInvalid
	default:
		return ProblemInternal
	}
}

func (s *State) requestRedraw() {
	if s.redraw != nil {
		s.redraw()
	}
}
