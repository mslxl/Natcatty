// Package serial owns the serial connection dialog: the form state, port
// enumeration, validation, and the native modal. The transport is opened by
// the caller's opener, so the dialog never imports an adapter.
package serial

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	"github.com/mslxl/natcatty/ui/locales"
)

// PortLister enumerates the platform's serial ports. The app wires the serial
// adapter; tests wire a fake.
type PortLister func(context.Context) ([]domainserial.PortInfo, error)

// Opener opens a serial Session for a validated configuration.
type Opener func(domainserial.Config) error

// SecretStore keeps a serial profile's auto-login password in the app's
// encrypted field-secret store. The profile key is the normalized port path
// from Config.SecretProfileID. The dialog never writes a password into the
// configuration, a workspace payload, or a log.
type SecretStore interface {
	// GetPassword returns the stored password and whether one exists.
	GetPassword(ctx context.Context, profile string) (string, bool, error)
	// SetPassword stores or replaces the profile's password.
	SetPassword(ctx context.Context, profile, password string) error
	// DeletePassword removes the profile's password.
	DeletePassword(ctx context.Context, profile string) error
}

// ErrNoSecretStore reports a dialog asked to keep auto-login credentials
// without a secret store. The session can still open, but the password is not
// persisted.
var ErrNoSecretStore = errors.New("serial dialog: no secret store")

// ErrNoOpener reports a dialog created without a way to open a Session.
var ErrNoOpener = errors.New("serial dialog: no Session opener")

// Option customizes a State.
type Option func(*State)

// WithRedraw sets the function called when the dialog changes something the
// window must repaint.
func WithRedraw(fn func()) Option {
	return func(s *State) { s.redraw = fn }
}

// WithCapabilities overrides the platform support table. The app keeps the
// platform table; tests inject one so every target's rules are exercised.
func WithCapabilities(caps domainserial.Capabilities) Option {
	return func(s *State) { s.capabilities = caps }
}

// WithSecretStore lets the dialog keep an auto-login password in the app's
// encrypted field-secret store. Without it, a password typed into the dialog is
// refused rather than silently dropped.
func WithSecretStore(store SecretStore) Option {
	return func(s *State) { s.secrets = store }
}

// State is the serial dialog's form and enumeration state. It is mutated on
// the UI thread; the port list is filled synchronously because both platform
// enumerators are local metadata reads, and an asynchronous list would need a
// frame to settle before the dialog could be used.
type State struct {
	mu           sync.Mutex
	lister       PortLister
	opener       Opener
	secrets      SecretStore
	redraw       func()
	visible      bool
	capabilities domainserial.Capabilities

	ports    []domainserial.PortInfo
	portsErr error
	problem  error

	path        string
	baud        string
	dataBits    string
	stopBits    string
	parity      string
	flowControl string

	autoLoginEnabled  bool
	autoLoginUsername string
	autoLoginPassword string
	// autoLoginPasswordPort records the port the typed password was entered
	// for. A password is never stored or replayed for another port, even if a
	// caller changes the path without clearing the form.
	autoLoginPasswordPort string
	autoLoginForget       bool
	autoLoginAttempts     string
	autoLoginReplies      string
}

// defaultAttempts is the dialog's initial attempt limit, matching the expect
// engine's default so an unedited form shows what will happen.
const defaultAttempts = 3

// NewState returns a hidden dialog with the original defaults: 115200 baud,
// 8 data bits, 1 stop bit, no parity, and no flow control.
func NewState(lister PortLister, opener Opener, opts ...Option) *State {
	state := &State{
		lister:            lister,
		opener:            opener,
		capabilities:      domainserial.PlatformCapabilities(),
		baud:              strconv.Itoa(domainserial.DefaultBaudRate),
		dataBits:          "8",
		stopBits:          "1",
		parity:            string(domainserial.ParityNone),
		flowControl:       string(domainserial.FlowNone),
		autoLoginAttempts: strconv.Itoa(defaultAttempts),
	}
	for _, opt := range opts {
		opt(state)
	}
	return state
}

// Show opens the dialog and refreshes the port list.
func (s *State) Show() {
	s.mu.Lock()
	s.visible = true
	s.problem = nil
	s.mu.Unlock()
	s.RefreshPorts()
	s.requestRedraw()
}

// Hide closes the dialog.
func (s *State) Hide() {
	s.mu.Lock()
	s.visible = false
	s.mu.Unlock()
	s.requestRedraw()
}

// Visible reports whether the modal should render.
func (s *State) Visible() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.visible
}

// RefreshPorts enumerates the platform's ports. An enumeration failure is kept
// so the dialog can explain it instead of pretending no device is connected.
func (s *State) RefreshPorts() {
	s.mu.Lock()
	lister := s.lister
	s.mu.Unlock()
	if lister == nil {
		s.mu.Lock()
		s.ports = nil
		s.portsErr = nil
		s.mu.Unlock()
		return
	}
	ports, err := lister(context.Background())
	s.mu.Lock()
	s.ports = append([]domainserial.PortInfo(nil), ports...)
	s.portsErr = err
	if err == nil && s.path == "" && len(ports) > 0 {
		s.path = ports[0].Path
	}
	s.mu.Unlock()
	s.requestRedraw()
}

// Ports returns the detected ports.
func (s *State) Ports() []domainserial.PortInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domainserial.PortInfo(nil), s.ports...)
}

// PortsError returns the last enumeration failure.
func (s *State) PortsError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.portsErr
}

// SetPort selects a port path and clears the last validation or connection
// failure, since the user changed the input that caused it. A different port
// also discards the typed auto-login password and forget request: they belong
// to the port they were entered for, and carrying them over would send one
// device's credential to another.
func (s *State) SetPort(path string) {
	s.mu.Lock()
	trimmed := strings.TrimSpace(path)
	if trimmed != s.path {
		s.autoLoginPassword = ""
		s.autoLoginPasswordPort = ""
		s.autoLoginForget = false
	}
	s.path = trimmed
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// Port returns the current path.
func (s *State) Port() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// SetBaudRate sets the baud-rate text. It is parsed by Config.
func (s *State) SetBaudRate(value string) {
	s.mu.Lock()
	s.baud = strings.TrimSpace(value)
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// BaudRate returns the baud-rate text.
func (s *State) BaudRate() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baud
}

// SetDataBits selects a data-bit count.
func (s *State) SetDataBits(value string) {
	s.mu.Lock()
	s.dataBits = strings.TrimSpace(value)
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// DataBits returns the selected data-bit count.
func (s *State) DataBits() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dataBits
}

// SetStopBits selects a stop-bit count.
func (s *State) SetStopBits(value string) {
	s.mu.Lock()
	s.stopBits = strings.TrimSpace(value)
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// StopBits returns the selected stop-bit count.
func (s *State) StopBits() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopBits
}

// StopBitsNeedWarning reports the original's caution about 1.5 stop bits,
// which Windows drivers may not accept.
func (s *State) StopBitsNeedWarning() bool {
	bits, ok := domainserial.ParseStopBits(s.StopBits())
	return ok && bits == domainserial.StopBitsOneAndHalf
}

// SetParity selects a parity mode by its persisted token.
func (s *State) SetParity(value string) {
	s.mu.Lock()
	if parity, ok := domainserial.ParseParity(value); ok {
		s.parity = string(parity)
	}
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// Parity returns the selected parity token.
func (s *State) Parity() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parity
}

// SetFlowControl selects a flow-control mode by its persisted token.
func (s *State) SetFlowControl(value string) {
	s.mu.Lock()
	if flow, ok := domainserial.ParseFlowControl(value); ok {
		s.flowControl = string(flow)
	}
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// FlowControl returns the selected flow-control token.
func (s *State) FlowControl() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flowControl
}

// SetAutoLoginEnabled turns console login automation on or off. Turning it off
// leaves the other fields intact so the user can switch it back on without
// retyping; Config drops them from the opened profile while it is off.
func (s *State) SetAutoLoginEnabled(enabled bool) {
	s.mu.Lock()
	s.autoLoginEnabled = enabled
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// AutoLoginEnabled reports whether login automation is on.
func (s *State) AutoLoginEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoLoginEnabled
}

// SetAutoLoginUsername sets the username the detector answers with.
func (s *State) SetAutoLoginUsername(value string) {
	s.mu.Lock()
	s.autoLoginUsername = value
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// AutoLoginUsername returns the username text.
func (s *State) AutoLoginUsername() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoLoginUsername
}

// SetAutoLoginPassword sets the password text. It is written to the field
// secret store on Connect and never enters Config. The password is scoped to
// the port selected now; changing the port discards it.
func (s *State) SetAutoLoginPassword(value string) {
	s.mu.Lock()
	s.autoLoginPassword = value
	s.autoLoginPasswordPort = strings.TrimSpace(s.path)
	s.autoLoginForget = false
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// AutoLoginPassword returns the password text for the form only. Callers must
// not log or persist it.
func (s *State) AutoLoginPassword() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoLoginPassword
}

// SetAutoLoginForgetPassword asks Connect to delete the profile's stored
// password. Setting it also clears the form's password text.
func (s *State) SetAutoLoginForgetPassword(forget bool) {
	s.mu.Lock()
	s.autoLoginForget = forget
	if forget {
		s.autoLoginPassword = ""
	}
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// AutoLoginForgetPassword reports whether the stored password will be deleted.
func (s *State) AutoLoginForgetPassword() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoLoginForget
}

// SetAutoLoginAttempts sets the attempt-limit text. It is parsed by Config.
func (s *State) SetAutoLoginAttempts(value string) {
	s.mu.Lock()
	s.autoLoginAttempts = strings.TrimSpace(value)
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// AutoLoginAttempts returns the attempt-limit text.
func (s *State) AutoLoginAttempts() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoLoginAttempts
}

// SetAutoLoginReplies sets the expect/reply rules as one "expect => reply" rule
// per line, the form's editable shape.
func (s *State) SetAutoLoginReplies(value string) {
	s.mu.Lock()
	s.autoLoginReplies = value
	s.problem = nil
	s.mu.Unlock()
	s.requestRedraw()
}

// AutoLoginReplies returns the expect/reply rules as edited text.
func (s *State) AutoLoginReplies() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.autoLoginReplies
}

// Config builds the selected configuration. A form value that cannot be read
// is reported as the same typed validation error the transport would return.
func (s *State) Config() (domainserial.Config, error) {
	s.mu.Lock()
	path, baudText := s.path, s.baud
	dataText, stopText := s.dataBits, s.stopBits
	parityText, flowText := s.parity, s.flowControl
	autoEnabled, autoUsername := s.autoLoginEnabled, s.autoLoginUsername
	autoAttempts, autoReplies := s.autoLoginAttempts, s.autoLoginReplies
	s.mu.Unlock()

	baud, err := strconv.Atoi(strings.TrimSpace(baudText))
	if err != nil {
		return domainserial.Config{}, &domainserial.ValidationError{
			Field: "baudRate", Value: baudText, Reason: "must be a whole number",
		}
	}
	dataBits, err := strconv.Atoi(strings.TrimSpace(dataText))
	if err != nil {
		return domainserial.Config{}, &domainserial.ValidationError{
			Field: "dataBits", Value: dataText, Reason: "must be 5, 6, 7, or 8",
		}
	}
	stopBits, ok := domainserial.ParseStopBits(stopText)
	if !ok {
		return domainserial.Config{}, &domainserial.ValidationError{
			Field: "stopBits", Value: stopText, Reason: "must be 1, 1.5, or 2",
		}
	}
	parity, ok := domainserial.ParseParity(parityText)
	if !ok {
		return domainserial.Config{}, &domainserial.ValidationError{
			Field: "parity", Value: parityText, Reason: "must be none, even, odd, mark, or space",
		}
	}
	flow, ok := domainserial.ParseFlowControl(flowText)
	if !ok {
		return domainserial.Config{}, &domainserial.ValidationError{
			Field: "flowControl", Value: flowText, Reason: "must be none, xon/xoff, or rts/cts",
		}
	}
	cfg := domainserial.Config{
		Path: path, BaudRate: baud, DataBits: dataBits,
		StopBits: stopBits, Parity: parity, FlowControl: flow,
	}.Normalized()
	if autoEnabled {
		attempts := 0
		if strings.TrimSpace(autoAttempts) != "" {
			parsed, err := strconv.Atoi(strings.TrimSpace(autoAttempts))
			if err != nil || parsed < 0 {
				return domainserial.Config{}, &domainserial.ValidationError{
					Field: "autoLoginAttempts", Value: autoAttempts,
					Reason: "must be a whole number of attempts",
				}
			}
			attempts = parsed
		}
		profile := domainserial.AutoLogin{
			Enabled:     true,
			Username:    autoUsername,
			MaxAttempts: attempts,
			Replies:     parseReplies(autoReplies),
		}.Normalized()
		cfg.AutoLogin = &profile
	}
	if err := cfg.Validate(); err != nil {
		return domainserial.Config{}, err
	}
	return cfg, nil
}

// parseReplies reads the form's one-rule-per-line text. "expect => reply" and
// "expect = reply" are both accepted; an unreadable line is ignored rather than
// failing the whole connection, because the built-in login prompts still work
// without it.
func parseReplies(text string) []domainserial.AutoLoginReply {
	var replies []domainserial.AutoLoginReply
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		index, separator := strings.Index(line, "=>"), 2
		if index < 0 {
			index, separator = strings.Index(line, "="), 1
		}
		if index < 0 {
			continue
		}
		expect := strings.TrimSpace(line[:index])
		if expect == "" {
			continue
		}
		replies = append(replies, domainserial.AutoLoginReply{
			Expect: expect, Respond: strings.TrimSpace(line[index+separator:]),
		})
	}
	return replies
}

// persistAutoLogin writes, keeps, or deletes the profile's password according
// to the form. It returns the configuration to open: PasswordSet records
// whether a stored secret exists, but the password value never enters cfg. A
// typed password that belongs to a different port is ignored rather than
// stored under this one.
func (s *State) persistAutoLogin(cfg domainserial.Config) (domainserial.Config, error) {
	if cfg.AutoLogin == nil {
		return cfg, nil
	}
	s.mu.Lock()
	password, passwordPort := s.autoLoginPassword, s.autoLoginPasswordPort
	forget, secrets := s.autoLoginForget, s.secrets
	s.mu.Unlock()

	profile := cfg.SecretProfileID()
	if password != "" && domainserial.NormalizePortPath(passwordPort) != profile {
		// The field was filled for another device (or before a port was
		// chosen). Storing it here would replay one port's credential on
		// another, so treat the field as empty for this profile.
		password = ""
	}
	switch {
	case forget:
		if secrets != nil {
			if err := secrets.DeletePassword(context.Background(), profile); err != nil {
				return domainserial.Config{}, err
			}
		}
		cfg.AutoLogin.PasswordSet = false
	case password != "":
		if secrets == nil {
			return domainserial.Config{}, ErrNoSecretStore
		}
		if err := secrets.SetPassword(context.Background(), profile, password); err != nil {
			return domainserial.Config{}, err
		}
		cfg.AutoLogin.PasswordSet = true
	case secrets != nil:
		// An empty field keeps the password already stored for this port, so
		// editing the framing does not silently disable the login. A reconnect
		// to the same port therefore reuses its stored profile password.
		_, found, err := secrets.GetPassword(context.Background(), profile)
		if err != nil {
			return domainserial.Config{}, err
		}
		cfg.AutoLogin.PasswordSet = found
	default:
		cfg.AutoLogin.PasswordSet = false
	}
	return cfg, nil
}

// Connect validates the form and asks the opener to start the Session. A
// failure stays in Problem so the dialog shows it; success closes the dialog.
func (s *State) Connect() error {
	cfg, err := s.Config()
	if err != nil {
		s.setProblem(err)
		return err
	}
	// Reject a setting the platform cannot express before a tab is opened, so
	// the user sees the typed reason beside the field instead of a failed tab.
	if err := domainserial.CheckSupport(cfg, s.capabilities); err != nil {
		s.setProblem(err)
		return err
	}
	if s.opener == nil {
		s.setProblem(ErrNoOpener)
		return ErrNoOpener
	}
	cfg, err = s.persistAutoLogin(cfg)
	if err != nil {
		s.setProblem(err)
		return err
	}
	if err := s.opener(cfg); err != nil {
		s.setProblem(err)
		return err
	}
	s.mu.Lock()
	s.visible = false
	s.problem = nil
	// A completed connect consumes the typed credential. Clearing it here keeps
	// a later Show/SetPort/Connect sequence from replaying this port's
	// password, while an empty field still finds the stored profile password
	// on a reconnect to the same port.
	s.autoLoginPassword = ""
	s.autoLoginPasswordPort = ""
	s.autoLoginForget = false
	s.mu.Unlock()
	s.requestRedraw()
	return nil
}

// Problem returns the last validation or connection failure.
func (s *State) Problem() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.problem
}

// ProblemText localizes a failure for the dialog. The raw error remains
// available through Problem for a technical detail line.
func ProblemText(m locales.Messages, err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domainserial.ErrUnsupported):
		return m.SerialUnsupported()
	case errors.Is(err, domainserial.ErrInvalidConfig):
		return m.SerialInvalidConfiguration()
	case errors.Is(err, ErrNoOpener), errors.Is(err, ErrNoSecretStore):
		return m.ConnectionErrorInternal()
	default:
		return m.SerialConnectionFailed()
	}
}

func (s *State) setProblem(err error) {
	s.mu.Lock()
	s.problem = err
	s.mu.Unlock()
	s.requestRedraw()
}

func (s *State) requestRedraw() {
	s.mu.Lock()
	redraw := s.redraw
	s.mu.Unlock()
	if redraw != nil {
		redraw()
	}
}
