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

// State is the serial dialog's form and enumeration state. It is mutated on
// the UI thread; the port list is filled synchronously because both platform
// enumerators are local metadata reads, and an asynchronous list would need a
// frame to settle before the dialog could be used.
type State struct {
	mu           sync.Mutex
	lister       PortLister
	opener       Opener
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
}

// NewState returns a hidden dialog with the original defaults: 115200 baud,
// 8 data bits, 1 stop bit, no parity, and no flow control.
func NewState(lister PortLister, opener Opener, opts ...Option) *State {
	state := &State{
		lister:       lister,
		opener:       opener,
		capabilities: domainserial.PlatformCapabilities(),
		baud:         strconv.Itoa(domainserial.DefaultBaudRate),
		dataBits:     "8",
		stopBits:     "1",
		parity:       string(domainserial.ParityNone),
		flowControl:  string(domainserial.FlowNone),
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
// failure, since the user changed the input that caused it.
func (s *State) SetPort(path string) {
	s.mu.Lock()
	s.path = strings.TrimSpace(path)
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

// Config builds the selected configuration. A form value that cannot be read
// is reported as the same typed validation error the transport would return.
func (s *State) Config() (domainserial.Config, error) {
	s.mu.Lock()
	path, baudText := s.path, s.baud
	dataText, stopText := s.dataBits, s.stopBits
	parityText, flowText := s.parity, s.flowControl
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
	if err := cfg.Validate(); err != nil {
		return domainserial.Config{}, err
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
	if err := s.opener(cfg); err != nil {
		s.setProblem(err)
		return err
	}
	s.mu.Lock()
	s.visible = false
	s.problem = nil
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
	case errors.Is(err, ErrNoOpener):
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
