package serial

import (
	"fmt"
	"runtime"
	"slices"
	"strconv"
)

// Capabilities describes what one platform's serial backend can program. The
// backends consult it before opening a port, so an unsupported framing choice
// is a typed error rather than a silently different connection. The lists are
// intentionally explicit: the original UI offered every combination, while
// the underlying operating systems differ (1.5 stop bits are Windows-only, and
// macOS has no mark/space parity).
type Capabilities struct {
	Platform      string
	Supported     bool
	DataBits      []int
	StopBits      []StopBits
	Parities      []Parity
	FlowControls  []FlowControl
	BaudRates     []int
	ArbitraryBaud bool
}

// PlatformCapabilities returns the capabilities of the running platform.
func PlatformCapabilities() Capabilities {
	return CapabilitiesFor(runtime.GOOS)
}

// CapabilitiesFor returns the backend capabilities for a GOOS value. It is
// pure so the support rules can be tested for every target on one machine.
func CapabilitiesFor(goos string) Capabilities {
	switch goos {
	case "windows":
		return Capabilities{
			Platform:     goos,
			Supported:    true,
			DataBits:     []int{5, 6, 7, 8},
			StopBits:     []StopBits{StopBitsOne, StopBitsOneAndHalf, StopBitsTwo},
			Parities:     []Parity{ParityNone, ParityEven, ParityOdd, ParityMark, ParitySpace},
			FlowControls: []FlowControl{FlowNone, FlowXONXOFF, FlowRTSCTS},
			BaudRates:    append([]int(nil), BaudRates...),
			// The DCB stores the rate as a 32-bit integer, so a custom rate is
			// a driver question rather than a representation limit.
			ArbitraryBaud: true,
		}
	case "linux":
		return Capabilities{
			Platform:     goos,
			Supported:    true,
			DataBits:     []int{5, 6, 7, 8},
			StopBits:     []StopBits{StopBitsOne, StopBitsTwo},
			Parities:     []Parity{ParityNone, ParityEven, ParityOdd, ParityMark, ParitySpace},
			FlowControls: []FlowControl{FlowNone, FlowXONXOFF, FlowRTSCTS},
			BaudRates:    append([]int(nil), BaudRates...),
		}
	case "darwin":
		return Capabilities{
			Platform:  goos,
			Supported: true,
			DataBits:  []int{5, 6, 7, 8},
			StopBits:  []StopBits{StopBitsOne, StopBitsTwo},
			// POSIX termios on macOS has no CMSPAR equivalent.
			Parities:     []Parity{ParityNone, ParityEven, ParityOdd},
			FlowControls: []FlowControl{FlowNone, FlowXONXOFF, FlowRTSCTS},
			// x/sys/unix exposes the standard B* constants through B230400 on
			// Darwin; the original serialport binding used IOSSIOSPEED for a
			// custom rate, which this pure-Go backend does not need for the
			// acceptance set.
			BaudRates: []int{300, 1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200, 230400},
		}
	default:
		return Capabilities{Platform: goos, Supported: false}
	}
}

// CheckSupport reports the first framing choice cfg asks for that caps cannot
// program. cfg must already be normalized.
func CheckSupport(cfg Config, caps Capabilities) error {
	if !caps.Supported {
		return &UnsupportedError{Platform: caps.Platform, Setting: "serial ports"}
	}
	if !slices.Contains(caps.DataBits, cfg.DataBits) {
		return &UnsupportedError{Platform: caps.Platform, Setting: "dataBits",
			Value: strconv.Itoa(cfg.DataBits)}
	}
	if !slices.Contains(caps.StopBits, cfg.StopBits) {
		return &UnsupportedError{Platform: caps.Platform, Setting: "stopBits",
			Value: formatFloat(float64(cfg.StopBits))}
	}
	// Win32 DCB requires 5 data bits with 1.5 stop bits. Only the Windows
	// table advertises 1.5, so a typed error here keeps SetCommState from
	// rejecting the combination with a raw driver error.
	if cfg.StopBits == StopBitsOneAndHalf && cfg.DataBits != 5 {
		return &UnsupportedError{Platform: caps.Platform, Setting: "stopBits",
			Value: "1.5 requires 5 data bits"}
	}
	if !slices.Contains(caps.Parities, cfg.Parity) {
		return &UnsupportedError{Platform: caps.Platform, Setting: "parity",
			Value: string(cfg.Parity)}
	}
	if !slices.Contains(caps.FlowControls, cfg.FlowControl) {
		return &UnsupportedError{Platform: caps.Platform, Setting: "flowControl",
			Value: string(cfg.FlowControl)}
	}
	if !caps.ArbitraryBaud && !slices.Contains(caps.BaudRates, cfg.BaudRate) {
		return &UnsupportedError{Platform: caps.Platform, Setting: "baudRate",
			Value: fmt.Sprintf("%d", cfg.BaudRate)}
	}
	return nil
}
