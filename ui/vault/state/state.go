// Package state owns the Vault surface's window-scoped state: the listed
// Hosts, the editor form, the delete confirmation, and the problem the view
// must localize. It is mutated only on the UI thread.
package state

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	uiconnection "github.com/mslxl/natcatty/ui/connection"
)

// Hosts is the Vault use-case seam. The application Service implements it;
// tests provide a fake.
type Hosts interface {
	List(ctx context.Context) ([]domainvault.Host, error)
	Create(ctx context.Context, draft domainvault.Draft) (domainvault.Host, error)
	Update(ctx context.Context, id model.HostID, draft domainvault.Draft) (domainvault.Host, error)
	Delete(ctx context.Context, id model.HostID) error
}

// Keys is the SSH Key use-case seam. The application Service implements it;
// tests provide a fake.
type Keys interface {
	ListKeys(ctx context.Context) ([]domainvault.Key, error)
	CreateKey(ctx context.Context, draft domainvault.KeyDraft) (domainvault.Key, error)
	UpdateKey(ctx context.Context, id model.KeyID, draft domainvault.KeyDraft) (domainvault.Key, error)
	DeleteKey(ctx context.Context, id model.KeyID) error
}

// Identities is the Identity use-case seam. The application Service implements
// it; tests provide a fake.
type Identities interface {
	ListIdentities(ctx context.Context) ([]domainvault.Identity, error)
	CreateIdentity(ctx context.Context, draft domainvault.IdentityDraft) (domainvault.Identity, error)
	UpdateIdentity(ctx context.Context, id model.IdentityID, draft domainvault.IdentityDraft) (domainvault.Identity, error)
	DeleteIdentity(ctx context.Context, id model.IdentityID) error
}

// Secrets is the field-secret mode seam. The application Service implements
// it; tests provide a fake.
type Secrets interface {
	SecretMode() appvault.SecretMode
	SetSecretsPassphrase(ctx context.Context, passphrase string) error
	UnlockSecrets(ctx context.Context, passphrase string) error
	UseSessionSecrets(ctx context.Context) error
}

// ConnectionTester opens, authenticates, and verifies a Host without opening
// a Session. The app wires the connection manager's one-shot test; tests wire
// a fake.
type ConnectionTester func(ctx context.Context, host domainvault.Host) error

// Problem identifies a failure the view must show in the user's language.
// State stores the identifier, never user-visible text.
type Problem string

const (
	ProblemNone                     Problem = ""
	ProblemHostnameRequired         Problem = "hostname_required"
	ProblemHostnameInvalid          Problem = "hostname_invalid"
	ProblemLabelRequired            Problem = "label_required"
	ProblemLabelInvalid             Problem = "label_invalid"
	ProblemPortInvalid              Problem = "port_invalid"
	ProblemPortRange                Problem = "port_range"
	ProblemProtocol                 Problem = "protocol"
	ProblemUsername                 Problem = "username"
	ProblemGroup                    Problem = "group"
	ProblemTags                     Problem = "tags"
	ProblemNotes                    Problem = "notes"
	ProblemDuplicate                Problem = "duplicate"
	ProblemNotFound                 Problem = "not_found"
	ProblemInternal                 Problem = "internal"
	ProblemSecretField              Problem = "secret_field"
	ProblemSecretsUnavailable       Problem = "secrets_unavailable"
	ProblemSecretsLocked            Problem = "secrets_locked"
	ProblemSecretWrongPassphrase    Problem = "secret_wrong_passphrase"
	ProblemSecretPassphraseRequired Problem = "secret_passphrase_required"
	ProblemSecretPassphraseMismatch Problem = "secret_passphrase_mismatch"
	ProblemSecretUnreadable         Problem = "secret_unreadable"
	ProblemSecretSetupFailed        Problem = "secret_setup_failed"
	ProblemKeyRequired              Problem = "key_required"
	ProblemKeyMaterial              Problem = "key_material"
	ProblemKeyPassphrase            Problem = "key_passphrase"
	ProblemKeyCertificate           Problem = "key_certificate"
	ProblemKeyUnsupported           Problem = "key_unsupported"
	ProblemIdentityKeyRequired      Problem = "identity_key_required"
	ProblemIdentityUsernameRequired Problem = "identity_username_required"
	ProblemAuthMethod               Problem = "auth_method"
	ProblemProxy                    Problem = "proxy"
	ProblemAlgorithms               Problem = "algorithms"
)

// Form is the editable text of one Host. Port stays a string so a malformed
// value can be rejected with a localized message instead of silently becoming
// zero.
type Form struct {
	ID       model.HostID
	Label    string
	Hostname string
	Port     string
	Username string
	Protocol string
	Group    string
	Tags     string
	Notes    string
	Pinned   bool
	// Password is plaintext form input. It is submitted to the application
	// service, never rendered back and never stored on the Host record.
	Password string
	// HasPassword mirrors the Host's non-secret credential flag for the
	// editor hint.
	HasPassword bool
	// IdentityID, KeyID, AuthMethod, UseSSHAgent, and RequiresMFA select how
	// the Host authenticates. They hold references, never credential material.
	IdentityID  string
	KeyID       string
	AuthMethod  string
	UseSSHAgent bool
	RequiresMFA bool
}

// State is the Vault surface's state.
type State struct {
	hosts         Hosts
	keys          Keys
	identities    Identities
	secrets       Secrets
	items         []domainvault.Host
	keyItems      []domainvault.Key
	identityItems []domainvault.Identity
	selected      model.HostID
	editing       bool
	form          Form
	problem       Problem
	pendingDelete model.HostID
	requestRedraw func()
	connect       func(domainvault.Host)
	tester        ConnectionTester

	// Test results are keyed by Host. The maps are mutated only on the UI
	// thread; completed attempts arrive through the lock-protected queue.
	testing      map[model.HostID]bool
	testProblems map[model.HostID]appconnection.Kind
	testPassed   map[model.HostID]bool
	testMu       sync.Mutex
	testOutcomes []testOutcome

	editingKey            bool
	keyForm               KeyForm
	pendingDeleteKey      model.KeyID
	editingIdentity       bool
	identityForm          IdentityForm
	pendingDeleteIdentity model.IdentityID

	// hostChain, proxy, and algorithms are preserved across an edit. They are
	// not exposed in the Host editor yet, so the editor must round-trip them
	// unchanged instead of silently clearing a saved route or negotiation
	// policy.
	hostChain        *domainvault.HostChain
	proxy            *domainvault.ProxyConfig
	legacyAlgorithms bool
	skipEcdsaHostKey bool
	algorithms       *domainvault.AlgorithmOverrides

	secretPassphrase string
	secretConfirm    string
	secretProblem    Problem
}

// Option customizes a State.
type Option func(*State)

// WithRedrawRequest sets the function called when the Vault surface changes.
func WithRedrawRequest(fn func()) Option {
	return func(s *State) { s.requestRedraw = fn }
}

// WithKeys gives the Vault surface SSH Key management.
func WithKeys(keys Keys) Option {
	return func(s *State) { s.keys = keys }
}

// WithIdentities gives the Vault surface Identity management.
func WithIdentities(identities Identities) Option {
	return func(s *State) { s.identities = identities }
}

// WithSecrets gives the Vault surface the field-secret mode and setup use
// cases. Without it, the surface assumes an OS credential store and hides the
// fallback setup.
func WithSecrets(secrets Secrets) Option {
	return func(s *State) { s.secrets = secrets }
}

// WithConnect gives the Vault surface the action that opens an SSH Session for
// a Host. The app wires the shell's OpenHost; tests wire a fake.
func WithConnect(fn func(domainvault.Host)) Option {
	return func(s *State) { s.connect = fn }
}

// WithConnectionTest gives the Vault surface the one-shot Test connection
// action. The app wires the connection manager; tests wire a fake.
func WithConnectionTest(fn ConnectionTester) Option {
	return func(s *State) { s.tester = fn }
}

// testOutcome is the result of a Test connection attempt started off the UI
// thread. FlushTests applies it.
type testOutcome struct {
	id  model.HostID
	err error
}

// NewState returns an empty Vault state. Call Reload to read the Vault.
func NewState(hosts Hosts, opts ...Option) *State {
	state := &State{hosts: hosts}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Reload reads every Host from the Vault. It reports storage failures and
// records ProblemInternal for the view.
func (s *State) Reload() error {
	err := s.load()
	s.redraw()
	return err
}

// Items returns the listed Hosts in the repository's order.
func (s *State) Items() []domainvault.Host {
	return s.items
}

// HostCount returns the number of listed Hosts.
func (s *State) HostCount() int { return len(s.items) }

// Selected returns the selected Host's id, or "".
func (s *State) Selected() model.HostID { return s.selected }

// Connect opens the SSH Session for one listed Host. It reports false and
// records a problem when the Host is gone or the shell has no connect action.
func (s *State) Connect(id model.HostID) bool {
	host, ok := s.find(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	if s.connect == nil {
		s.problem = ProblemInternal
		s.redraw()
		return false
	}
	s.problem = ProblemNone
	s.connect(host)
	return true
}

// Test starts a Test connection attempt for one listed Host. The dial runs on
// its own goroutine; FlushTests applies the outcome on the UI thread. It
// reports false and records a problem when the Host is gone, the action is not
// wired, or another attempt is already running.
func (s *State) Test(id model.HostID) bool {
	host, ok := s.find(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return false
	}
	if s.tester == nil {
		s.problem = ProblemInternal
		s.redraw()
		return false
	}
	if s.testing[id] {
		return false
	}
	if s.testing == nil {
		s.testing = make(map[model.HostID]bool)
		s.testProblems = make(map[model.HostID]appconnection.Kind)
		s.testPassed = make(map[model.HostID]bool)
	}
	s.testing[id] = true
	delete(s.testProblems, id)
	delete(s.testPassed, id)
	s.problem = ProblemNone
	s.redraw()

	tester := s.tester
	go func() {
		err := tester(context.Background(), host)
		s.testMu.Lock()
		s.testOutcomes = append(s.testOutcomes, testOutcome{id: id, err: err})
		s.testMu.Unlock()
		s.redraw()
	}()
	return true
}

// FlushTests applies completed Test connection attempts. The view calls it on
// the UI thread before rendering.
func (s *State) FlushTests() {
	s.testMu.Lock()
	outcomes := s.testOutcomes
	s.testOutcomes = nil
	s.testMu.Unlock()

	for _, outcome := range outcomes {
		delete(s.testing, outcome.id)
		if outcome.err != nil {
			s.testProblems[outcome.id] = uiconnection.Kind(outcome.err)
			continue
		}
		s.testPassed[outcome.id] = true
	}
}

// Testing reports whether a Test connection attempt is running for a Host.
func (s *State) Testing(id model.HostID) bool { return s.testing[id] }

// TestSucceeded reports whether the last Test connection attempt reached and
// authenticated the Host.
func (s *State) TestSucceeded(id model.HostID) bool { return s.testPassed[id] }

// TestProblem returns the typed failure of the last Test connection attempt,
// or "" when it succeeded or none has run.
func (s *State) TestProblem(id model.HostID) appconnection.Kind {
	return s.testProblems[id]
}

// Select marks one listed Host as selected. An unknown id clears the
// selection.
func (s *State) Select(id model.HostID) {
	if _, ok := s.find(id); ok {
		s.selected = id
	} else {
		s.selected = ""
	}
	s.redraw()
}

// Editing reports whether the Host editor is open.
func (s *State) Editing() bool { return s.editing }

// Form returns the editor's form for binding. It is valid while Editing.
func (s *State) Form() *Form { return &s.form }

// Problem returns the current failure, if any.
func (s *State) Problem() Problem { return s.problem }

// SecretMode reports how credentials are protected. A surface without a
// secret seam reports OS mode so it does not show fallback setup.
func (s *State) SecretMode() appvault.SecretMode {
	if s.secrets == nil {
		return appvault.SecretModeOS
	}
	return s.secrets.SecretMode()
}

// SecretPassphrase returns the passphrase input for the active setup mode.
func (s *State) SecretPassphrase() *string { return &s.secretPassphrase }

// SecretConfirmation returns the confirmation input for passphrase setup.
func (s *State) SecretConfirmation() *string { return &s.secretConfirm }

// SecretProblem returns the current credential-mode failure.
func (s *State) SecretProblem() Problem { return s.secretProblem }

// SetSecretPassphrase chooses passphrase mode. It reports whether the mode
// was set; the view redraws either way.
func (s *State) SetSecretPassphrase() bool {
	if s.secrets == nil {
		s.secretProblem = ProblemSecretsUnavailable
		s.redraw()
		return false
	}
	if s.secretPassphrase == "" {
		s.secretProblem = ProblemSecretPassphraseRequired
		s.redraw()
		return false
	}
	if s.secretPassphrase != s.secretConfirm {
		s.secretProblem = ProblemSecretPassphraseMismatch
		s.redraw()
		return false
	}
	if err := s.secrets.SetSecretsPassphrase(context.Background(), s.secretPassphrase); err != nil {
		s.secretProblem = secretProblemFor(err)
		s.redraw()
		return false
	}
	s.secretPassphrase = ""
	s.secretConfirm = ""
	s.secretProblem = ProblemNone
	s.redraw()
	return true
}

// UnlockSecrets unwraps a passphrase-mode field key.
func (s *State) UnlockSecrets() bool {
	if s.secrets == nil {
		s.secretProblem = ProblemSecretsUnavailable
		s.redraw()
		return false
	}
	if s.secretPassphrase == "" {
		s.secretProblem = ProblemSecretPassphraseRequired
		s.redraw()
		return false
	}
	if err := s.secrets.UnlockSecrets(context.Background(), s.secretPassphrase); err != nil {
		s.secretProblem = secretProblemFor(err)
		s.redraw()
		return false
	}
	s.secretPassphrase = ""
	s.secretConfirm = ""
	s.secretProblem = ProblemNone
	s.redraw()
	return true
}

// UseSessionSecrets chooses session-only mode when the OS store is
// unavailable.
func (s *State) UseSessionSecrets() bool {
	if s.secrets == nil {
		s.secretProblem = ProblemSecretsUnavailable
		s.redraw()
		return false
	}
	if err := s.secrets.UseSessionSecrets(context.Background()); err != nil {
		s.secretProblem = secretProblemFor(err)
		s.redraw()
		return false
	}
	s.secretPassphrase = ""
	s.secretConfirm = ""
	s.secretProblem = ProblemNone
	s.redraw()
	return true
}

// PendingDelete returns the Host awaiting delete confirmation, or "".
func (s *State) PendingDelete() model.HostID { return s.pendingDelete }

// BeginCreate opens the editor with a new Host.
func (s *State) BeginCreate() {
	s.editing = true
	s.form = Form{
		Protocol:   string(domainvault.ProtocolSSH),
		AuthMethod: string(domainvault.HostAuthAuto),
	}
	s.problem = ProblemNone
	s.pendingDelete = ""
	s.hostChain = nil
	s.proxy = nil
	s.legacyAlgorithms = false
	s.skipEcdsaHostKey = false
	s.algorithms = nil
	s.redraw()
}

// BeginEdit opens the editor with a listed Host's values. It reports false
// when the Host is not listed.
func (s *State) BeginEdit(id model.HostID) bool {
	host, ok := s.find(id)
	if !ok {
		return false
	}
	s.editing = true
	s.selected = host.ID
	s.problem = ProblemNone
	s.pendingDelete = ""
	s.form = Form{
		ID:       host.ID,
		Label:    host.Label,
		Hostname: host.Hostname,
		Port:     strconv.Itoa(host.Port),
		Username: host.Username,
		Protocol: string(host.Protocol),
		Group:    host.Group,
		Tags:     strings.Join(host.Tags, ", "),
		Notes:    host.Notes,
		Pinned:   host.Pinned,
		// The stored password is never loaded into the form. Leaving the
		// field blank keeps the existing encrypted value.
		HasPassword: host.HasPassword,
		IdentityID:  string(host.IdentityID),
		KeyID:       string(host.KeyID),
		AuthMethod:  string(host.AuthMethod),
		UseSSHAgent: host.UseSSHAgent,
		RequiresMFA: host.RequiresMFA,
	}
	s.hostChain = domainvault.CloneHostChain(host.HostChain)
	s.proxy = domainvault.CloneProxy(host.Proxy)
	s.legacyAlgorithms = host.LegacyAlgorithms
	s.skipEcdsaHostKey = host.SkipEcdsaHostKey
	s.algorithms = domainvault.CloneAlgorithms(host.Algorithms)
	if s.form.AuthMethod == "" {
		s.form.AuthMethod = string(domainvault.HostAuthAuto)
	}
	if host.Protocol == domainvault.ProtocolLocal {
		s.form.Port = ""
	}
	s.redraw()
	return true
}

// CancelEdit closes the editor without saving.
func (s *State) CancelEdit() {
	s.editing = false
	s.form = Form{}
	s.hostChain = nil
	s.proxy = nil
	s.legacyAlgorithms = false
	s.skipEcdsaHostKey = false
	s.algorithms = nil
	s.problem = ProblemNone
	s.redraw()
}

// Save validates the form through the application service and persists the
// Host. Validation, conflict, and storage failures are reported through
// Problem; the editor stays open so the user can correct the input.
func (s *State) Save() error {
	draft, problem := s.draft()
	if problem != ProblemNone {
		s.problem = problem
		s.redraw()
		return nil
	}

	var err error
	if s.form.ID == "" {
		_, err = s.hosts.Create(context.Background(), draft)
	} else {
		_, err = s.hosts.Update(context.Background(), s.form.ID, draft)
	}
	if err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}

	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.editing = false
	s.form = Form{}
	s.hostChain = nil
	s.proxy = nil
	s.legacyAlgorithms = false
	s.skipEcdsaHostKey = false
	s.algorithms = nil
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// RequestDelete opens the confirmation for one Host.
func (s *State) RequestDelete(id model.HostID) {
	s.pendingDelete = id
	s.problem = ProblemNone
	s.redraw()
}

// CancelDelete closes the delete confirmation.
func (s *State) CancelDelete() {
	s.pendingDelete = ""
	s.redraw()
}

// ConfirmDelete deletes the Host awaiting confirmation. Deletion never
// cascades to another Vault record.
func (s *State) ConfirmDelete() error {
	if s.pendingDelete == "" {
		return nil
	}
	id := s.pendingDelete
	s.pendingDelete = ""
	if err := s.hosts.Delete(context.Background(), id); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if s.selected == id {
		s.selected = ""
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// TogglePinned flips a Host's Pinned preference through an update, so the
// preference survives restart with the Host.
func (s *State) TogglePinned(id model.HostID) error {
	host, ok := s.find(id)
	if !ok {
		s.problem = ProblemNotFound
		s.redraw()
		return nil
	}
	if _, err := s.hosts.Update(context.Background(), id, draftFromHost(host, !host.Pinned)); err != nil {
		s.problem = problemFor(err)
		s.redraw()
		return nil
	}
	if err := s.load(); err != nil {
		s.redraw()
		return nil
	}
	s.problem = ProblemNone
	s.redraw()
	return nil
}

// load refreshes the Host list without requesting a redraw.
func (s *State) load() error {
	hosts, err := s.hosts.List(context.Background())
	if err != nil {
		s.problem = ProblemInternal
		return err
	}
	s.items = hosts
	if s.selected != "" {
		if _, ok := s.find(s.selected); !ok {
			s.selected = ""
		}
	}
	if s.keys != nil {
		keys, err := s.keys.ListKeys(context.Background())
		if err != nil {
			s.problem = ProblemInternal
			return err
		}
		s.keyItems = keys
	}
	if s.identities != nil {
		identities, err := s.identities.ListIdentities(context.Background())
		if err != nil {
			s.problem = ProblemInternal
			return err
		}
		s.identityItems = identities
	}
	return nil
}

// find returns the listed Host with id.
func (s *State) find(id model.HostID) (domainvault.Host, bool) {
	for _, host := range s.items {
		if host.ID == id {
			return host, true
		}
	}
	return domainvault.Host{}, false
}

// draftFromHost turns a Host into the draft an edit would submit, optionally
// changing its Pinned preference.
func draftFromHost(host domainvault.Host, pinned bool) domainvault.Draft {
	return domainvault.Draft{
		Label:            host.Label,
		Hostname:         host.Hostname,
		Port:             host.Port,
		Username:         host.Username,
		Protocol:         host.Protocol,
		Group:            host.Group,
		Tags:             host.Tags,
		Notes:            host.Notes,
		Pinned:           pinned,
		IdentityID:       host.IdentityID,
		KeyID:            host.KeyID,
		AuthMethod:       host.AuthMethod,
		UseSSHAgent:      host.UseSSHAgent,
		RequiresMFA:      host.RequiresMFA,
		HostChain:        domainvault.CloneHostChain(host.HostChain),
		Proxy:            domainvault.CloneProxy(host.Proxy),
		LegacyAlgorithms: host.LegacyAlgorithms,
		SkipEcdsaHostKey: host.SkipEcdsaHostKey,
		Algorithms:       domainvault.CloneAlgorithms(host.Algorithms),
	}
}

// draft turns the form into a domain draft, decoding the fields that the
// editor keeps as text.
func (s *State) draft() (domainvault.Draft, Problem) {
	port := 0
	if trimmed := strings.TrimSpace(s.form.Port); trimmed != "" {
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return domainvault.Draft{}, ProblemPortInvalid
		}
		port = parsed
	}

	var tags []string
	if strings.TrimSpace(s.form.Tags) != "" {
		for _, tag := range strings.Split(s.form.Tags, ",") {
			tags = append(tags, tag)
		}
	}

	return domainvault.Draft{
		Label:       s.form.Label,
		Hostname:    s.form.Hostname,
		Port:        port,
		Username:    s.form.Username,
		Protocol:    domainvault.Protocol(s.form.Protocol),
		Group:       s.form.Group,
		Tags:        tags,
		Notes:       s.form.Notes,
		Pinned:      s.form.Pinned,
		Password:    s.form.Password,
		IdentityID:  model.IdentityID(s.form.IdentityID),
		KeyID:       model.KeyID(s.form.KeyID),
		AuthMethod:  domainvault.HostAuthMethod(s.form.AuthMethod),
		UseSSHAgent: s.form.UseSSHAgent,
		RequiresMFA: s.form.RequiresMFA,
		// The editor does not expose routing yet. Round-trip the saved chain
		// and proxy so editing another field cannot erase them.
		HostChain:        domainvault.CloneHostChain(s.hostChain),
		Proxy:            domainvault.CloneProxy(s.proxy),
		LegacyAlgorithms: s.legacyAlgorithms,
		SkipEcdsaHostKey: s.skipEcdsaHostKey,
		Algorithms:       domainvault.CloneAlgorithms(s.algorithms),
	}, ProblemNone
}

// problemFor maps a typed application error to the view's problem id.
func problemFor(err error) Problem {
	if errors.Is(err, appvault.ErrSecretUnreadable) {
		return ProblemSecretUnreadable
	}
	var typed *appvault.Error
	if !errors.As(err, &typed) {
		return ProblemInternal
	}
	switch typed.Kind {
	case appvault.KindConflict:
		return ProblemDuplicate
	case appvault.KindNotFound:
		return ProblemNotFound
	case appvault.KindValidation:
		var validation *domainvault.ValidationError
		if !errors.As(err, &validation) {
			return ProblemSecretField
		}
		return problemForValidation(validation)
	case appvault.KindPermission:
		return ProblemSecretsLocked
	case appvault.KindUnsupported:
		return ProblemSecretsUnavailable
	case appvault.KindInternal:
		return ProblemInternal
	default:
		return ProblemInternal
	}
}

// secretProblemFor maps a credential-mode failure to the view's problem id.
func secretProblemFor(err error) Problem {
	var typed *appvault.Error
	if !errors.As(err, &typed) {
		return ProblemSecretSetupFailed
	}
	switch typed.Kind {
	case appvault.KindPermission:
		return ProblemSecretWrongPassphrase
	case appvault.KindUnsupported:
		return ProblemSecretsUnavailable
	default:
		return ProblemSecretSetupFailed
	}
}

func problemForValidation(invalid *domainvault.ValidationError) Problem {
	switch invalid.Field {
	case domainvault.FieldHostname:
		if invalid.Rule == domainvault.RuleRequired {
			return ProblemHostnameRequired
		}
		return ProblemHostnameInvalid
	case domainvault.FieldLabel:
		if invalid.Rule == domainvault.RuleRequired {
			return ProblemLabelRequired
		}
		return ProblemLabelInvalid
	case domainvault.FieldPort:
		if invalid.Rule == domainvault.RuleOutOfRange {
			return ProblemPortRange
		}
		return ProblemPortInvalid
	case domainvault.FieldProtocol:
		return ProblemProtocol
	case domainvault.FieldUsername:
		return ProblemUsername
	case domainvault.FieldGroup:
		return ProblemGroup
	case domainvault.FieldTags:
		return ProblemTags
	case domainvault.FieldNotes:
		return ProblemNotes
	case domainvault.FieldChain, domainvault.FieldProxy:
		return ProblemProxy
	case domainvault.FieldAlgorithms:
		return ProblemAlgorithms
	default:
		return ProblemInternal
	}
}

func (s *State) redraw() {
	if s.requestRedraw != nil {
		s.requestRedraw()
	}
}
