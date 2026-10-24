// Package serial holds the serial-port configuration vocabulary shared by the
// Session, the adapters that open a port, and the connection dialog. It is
// pure data and validation: it imports only the standard library, so the
// domain never names an operating-system serial API.
//
// The values and defaults match the original Electron implementation
// (components/SerialConnectModal.tsx and domain/models/connection.ts): 8 data
// bits, 1 stop bit, no parity, no flow control, and 115200 baud.
package serial

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Parity is a serial parity mode.
type Parity string

const (
	ParityNone  Parity = "none"
	ParityEven  Parity = "even"
	ParityOdd   Parity = "odd"
	ParityMark  Parity = "mark"
	ParitySpace Parity = "space"
)

// Valid reports whether p is one of the original parity choices.
func (p Parity) Valid() bool {
	switch p {
	case ParityNone, ParityEven, ParityOdd, ParityMark, ParitySpace:
		return true
	}
	return false
}

// NormalizeParity maps an empty or unknown value to no parity, the original
// default.
func NormalizeParity(p Parity) Parity {
	if p.Valid() {
		return p
	}
	return ParityNone
}

// ParseParity reads a parity token. It is the form-side counterpart of
// Parity.Valid.
func ParseParity(value string) (Parity, bool) {
	p := Parity(strings.ToLower(strings.TrimSpace(value)))
	return p, p.Valid()
}

// FlowControl is a serial flow-control mode. The identifiers are the
// original's persisted spellings, including the slash.
type FlowControl string

const (
	FlowNone    FlowControl = "none"
	FlowXONXOFF FlowControl = "xon/xoff"
	FlowRTSCTS  FlowControl = "rts/cts"
)

// Valid reports whether f is one of the original flow-control choices.
func (f FlowControl) Valid() bool {
	switch f {
	case FlowNone, FlowXONXOFF, FlowRTSCTS:
		return true
	}
	return false
}

// NormalizeFlowControl maps an empty or unknown value to no flow control, the
// original default.
func NormalizeFlowControl(f FlowControl) FlowControl {
	if f.Valid() {
		return f
	}
	return FlowNone
}

// ParseFlowControl reads a flow-control token. It accepts the original's
// slash spelling and a hyphenated spelling from existing forms.
func ParseFlowControl(value string) (FlowControl, bool) {
	normalized := FlowControl(strings.ToLower(strings.TrimSpace(value)))
	if normalized == "xon-xoff" {
		normalized = FlowXONXOFF
	}
	if normalized == "rts-cts" {
		normalized = FlowRTSCTS
	}
	return normalized, normalized.Valid()
}

// StopBits is the number of stop bits. The original stores 1, 1.5, or 2 as a
// number; the fractional value is exact in binary, so it compares reliably.
type StopBits float64

const (
	StopBitsOne        StopBits = 1
	StopBitsOneAndHalf StopBits = 1.5
	StopBitsTwo        StopBits = 2
)

// Valid reports whether s is one of the original stop-bit choices.
func (s StopBits) Valid() bool {
	switch s {
	case StopBitsOne, StopBitsOneAndHalf, StopBitsTwo:
		return true
	}
	return false
}

// NormalizeStopBits maps an empty or unknown value to one stop bit, the
// original default.
func NormalizeStopBits(s StopBits) StopBits {
	if s.Valid() {
		return s
	}
	return StopBitsOne
}

// ParseStopBits reads a stop-bit count from a form value.
func ParseStopBits(value string) (StopBits, bool) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return StopBitsOne, false
	}
	bits := StopBits(parsed)
	return bits, bits.Valid()
}

// Bounds for the framing settings. The original offers 5-8 data bits and any
// positive integer baud rate; the upper bound keeps a pasted number from
// overflowing a platform API.
const (
	MinDataBits = 5
	MaxDataBits = 8
	MinBaudRate = 1
	MaxBaudRate = 4_000_000
)

// DefaultBaudRate is the original modal's initial baud rate.
const DefaultBaudRate = 115200

// BaudRates are the original modal's baud-rate choices. A user may still type
// a custom value where the platform supports it.
var BaudRates = []int{300, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400, 460800, 921600}

// Config is one serial connection's framing. It is the shared model for the
// Session start spec, the connection dialog, and the platform backends.
type Config struct {
	// Path is the port path, for example /dev/ttyUSB0 or COM3.
	Path string `json:"path"`
	// BaudRate is the signalling rate.
	BaudRate int `json:"baudRate"`
	// DataBits is 5, 6, 7, or 8.
	DataBits int `json:"dataBits,omitempty"`
	// StopBits is 1, 1.5, or 2.
	StopBits StopBits `json:"stopBits,omitempty"`
	// Parity is none, even, odd, mark, or space.
	Parity Parity `json:"parity,omitempty"`
	// FlowControl is none, xon/xoff, or rts/cts.
	FlowControl FlowControl `json:"flowControl,omitempty"`
}

// Normalized returns cfg with the original defaults filled in and the path's
// surrounding whitespace removed. An unknown enum stays as written so
// Validate can reject it rather than silently changing the user's choice.
func (c Config) Normalized() Config {
	c.Path = strings.TrimSpace(c.Path)
	if c.BaudRate == 0 {
		c.BaudRate = DefaultBaudRate
	}
	if c.DataBits == 0 {
		c.DataBits = 8
	}
	if c.StopBits == 0 {
		c.StopBits = StopBitsOne
	}
	if c.Parity == "" {
		c.Parity = ParityNone
	}
	if c.FlowControl == "" {
		c.FlowControl = FlowNone
	}
	return c
}

// ErrInvalidConfig reports a serial configuration that cannot be opened. It is
// a typed error so a form and the Session can tell a user input mistake from
// a device or operating-system failure.
var ErrInvalidConfig = errors.New("serial: invalid configuration")

// ValidationError names the field a configuration mistake came from.
type ValidationError struct {
	Field  string
	Value  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("serial: %s %q %s", e.Field, e.Value, e.Reason)
}

// Is makes errors.Is(err, ErrInvalidConfig) true for every validation error.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalidConfig }

// Validate checks an already-normalized configuration.
func (c Config) Validate() error {
	if c.Path == "" {
		return &ValidationError{Field: "port", Reason: "is required"}
	}
	if !ValidPortPath(c.Path) {
		return &ValidationError{Field: "port", Value: c.Path,
			Reason: "must be a /dev/ path or a COM port"}
	}
	if c.BaudRate < MinBaudRate || c.BaudRate > MaxBaudRate {
		return &ValidationError{Field: "baudRate", Value: strconv.Itoa(c.BaudRate),
			Reason: fmt.Sprintf("must be between %d and %d", MinBaudRate, MaxBaudRate)}
	}
	if c.DataBits < MinDataBits || c.DataBits > MaxDataBits {
		return &ValidationError{Field: "dataBits", Value: strconv.Itoa(c.DataBits),
			Reason: "must be 5, 6, 7, or 8"}
	}
	if !c.StopBits.Valid() {
		return &ValidationError{Field: "stopBits", Value: formatFloat(float64(c.StopBits)),
			Reason: "must be 1, 1.5, or 2"}
	}
	if !c.Parity.Valid() {
		return &ValidationError{Field: "parity", Value: string(c.Parity),
			Reason: "must be none, even, odd, mark, or space"}
	}
	if !c.FlowControl.Valid() {
		return &ValidationError{Field: "flowControl", Value: string(c.FlowControl),
			Reason: "must be none, xon/xoff, or rts/cts"}
	}
	return nil
}

var (
	comPortPattern       = regexp.MustCompile(`^COM[0-9]+$`)
	windowsDevicePattern = regexp.MustCompile(`^\\\\\.\\COM[0-9]+$`)
)

// ValidPortPath reports whether path is a form the original accepted: a Unix
// device below /dev/, a COM port, or a Windows raw-device path. Validation is
// deliberately platform-independent; whether the device exists is decided by
// the open call, and a pseudo-terminal path is a valid /dev/ path.
func ValidPortPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasPrefix(path, "/dev/") ||
		comPortPattern.MatchString(strings.ToUpper(path)) ||
		windowsDevicePattern.MatchString(strings.ToUpper(path))
}

// PortName returns the short display name of a port, as the original derived
// "Serial: <name>" labels. An empty path returns "serial".
func PortName(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "serial"
	}
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 && index+1 < len(trimmed) {
		return trimmed[index+1:]
	}
	return trimmed
}

// PortInfo describes one enumerated port. The fields match the original
// serialport list result; empty metadata is expected on platforms that cannot
// report it.
type PortInfo struct {
	Path         string `json:"path"`
	Manufacturer string `json:"manufacturer,omitempty"`
	SerialNumber string `json:"serialNumber,omitempty"`
	VendorID     string `json:"vendorId,omitempty"`
	ProductID    string `json:"productId,omitempty"`
	PNPID        string `json:"pnpId,omitempty"`
	Type         string `json:"type,omitempty"`
}

// ErrUnsupported reports a setting or platform the serial transport cannot
// serve. It is distinct from ErrInvalidConfig: the input is well formed, but
// the target does not provide it.
var ErrUnsupported = errors.New("serial: unsupported")

// UnsupportedError names the platform and setting that cannot be used.
type UnsupportedError struct {
	Platform string
	Setting  string
	Value    string
}

func (e *UnsupportedError) Error() string {
	setting := e.Setting
	if setting == "" {
		setting = "serial"
	}
	value := ""
	if e.Value != "" {
		value = " " + e.Value
	}
	return fmt.Sprintf("serial: %s%s is not supported on %s", setting, value, e.Platform)
}

// Is makes errors.Is(err, ErrUnsupported) true for every unsupported error.
func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
