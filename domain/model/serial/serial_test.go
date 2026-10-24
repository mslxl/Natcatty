package serial

import (
	"errors"
	"slices"
	"testing"
)

func containsStopBits(values []StopBits, want StopBits) bool {
	return slices.Contains(values, want)
}

func containsParity(values []Parity, want Parity) bool {
	return slices.Contains(values, want)
}

func TestNormalizeAppliesTheOriginalDefaults(t *testing.T) {
	got := (Config{Path: "  COM3 "}).Normalized()
	want := Config{
		Path:        "COM3",
		BaudRate:    115200,
		DataBits:    8,
		StopBits:    StopBitsOne,
		Parity:      ParityNone,
		FlowControl: FlowNone,
	}
	if got != want {
		t.Fatalf("Normalized() = %+v, want %+v", got, want)
	}
}

func TestValidateAcceptsTheFullOriginalParameterSet(t *testing.T) {
	paths := []string{"/dev/ttyUSB0", "/dev/cu.usbserial-1", "COM1", `\\.\COM12`}
	for _, path := range paths {
		for dataBits := MinDataBits; dataBits <= MaxDataBits; dataBits++ {
			for _, stopBits := range []StopBits{StopBitsOne, StopBitsOneAndHalf, StopBitsTwo} {
				for _, parity := range []Parity{ParityNone, ParityEven, ParityOdd, ParityMark, ParitySpace} {
					for _, flow := range []FlowControl{FlowNone, FlowXONXOFF, FlowRTSCTS} {
						cfg := Config{
							Path: path, BaudRate: 115200, DataBits: dataBits,
							StopBits: stopBits, Parity: parity, FlowControl: flow,
						}
						if err := cfg.Validate(); err != nil {
							t.Fatalf("Validate(%+v) = %v, want nil", cfg, err)
						}
					}
				}
			}
		}
	}
}

func TestValidateRejectsBadInputAsATypedError(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"empty path", Config{BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: ParityNone, FlowControl: FlowNone}},
		{"bad path", Config{Path: "ttyUSB0", BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: ParityNone, FlowControl: FlowNone}},
		{"zero baud", Config{Path: "COM1", BaudRate: 0, DataBits: 8, StopBits: 1, Parity: ParityNone, FlowControl: FlowNone}},
		{"data bits", Config{Path: "COM1", BaudRate: 115200, DataBits: 9, StopBits: 1, Parity: ParityNone, FlowControl: FlowNone}},
		{"stop bits", Config{Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 3, Parity: ParityNone, FlowControl: FlowNone}},
		{"parity", Config{Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: "weird", FlowControl: FlowNone}},
		{"flow control", Config{Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: ParityNone, FlowControl: "hardware"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.cfg.Validate()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Validate() = %v, want ErrInvalidConfig", err)
			}
			var typed *ValidationError
			if !errors.As(err, &typed) || typed.Field == "" {
				t.Fatalf("error = %v, want a ValidationError naming its field", err)
			}
		})
	}
}

func TestParsersKeepTheOriginalTokens(t *testing.T) {
	for _, value := range []string{"none", "even", "odd", "mark", "space"} {
		parity, ok := ParseParity(value)
		if !ok || string(parity) != value {
			t.Fatalf("ParseParity(%q) = %q, %v", value, parity, ok)
		}
	}
	for _, value := range []string{"none", "xon/xoff", "rts/cts", "XON-XOFF", "RTS-CTS"} {
		if _, ok := ParseFlowControl(value); !ok {
			t.Fatalf("ParseFlowControl(%q) rejected a valid mode", value)
		}
	}
	for _, value := range []string{"1", "1.5", "2"} {
		if _, ok := ParseStopBits(value); !ok {
			t.Fatalf("ParseStopBits(%q) rejected a valid choice", value)
		}
	}
	for _, value := range []string{"", "0", "3", "one"} {
		if _, ok := ParseStopBits(value); ok {
			t.Fatalf("ParseStopBits(%q) accepted an invalid choice", value)
		}
	}
}

func TestCapabilitiesForEachBackend(t *testing.T) {
	windows := CapabilitiesFor("windows")
	if !windows.Supported || !windows.ArbitraryBaud || !containsStopBits(windows.StopBits, StopBitsOneAndHalf) ||
		!containsParity(windows.Parities, ParityMark) {
		t.Fatalf("windows capabilities = %+v, want 1.5 stop bits, mark parity, and custom baud", windows)
	}
	linux := CapabilitiesFor("linux")
	if !linux.Supported || containsStopBits(linux.StopBits, StopBitsOneAndHalf) ||
		!containsParity(linux.Parities, ParityMark) || linux.ArbitraryBaud {
		t.Fatalf("linux capabilities = %+v, want no 1.5 stop bits and no custom baud", linux)
	}
	darwin := CapabilitiesFor("darwin")
	if !darwin.Supported || containsStopBits(darwin.StopBits, StopBitsOneAndHalf) ||
		containsParity(darwin.Parities, ParityMark) || containsParity(darwin.Parities, ParitySpace) {
		t.Fatalf("darwin capabilities = %+v, want no 1.5 stop bits and no mark/space parity", darwin)
	}
	if CapabilitiesFor("plan9").Supported {
		t.Fatal("plan9 reported a serial backend")
	}
}

func TestCheckSupportReturnsATypedUnsupportedError(t *testing.T) {
	linux := CapabilitiesFor("linux")
	cfg := (Config{Path: "/dev/ttyUSB0", BaudRate: 115200, DataBits: 8,
		StopBits: StopBitsOneAndHalf, Parity: ParityNone, FlowControl: FlowNone}).Normalized()
	err := CheckSupport(cfg, linux)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("CheckSupport() = %v, want ErrUnsupported", err)
	}
	var typed *UnsupportedError
	if !errors.As(err, &typed) || typed.Setting != "stopBits" || typed.Platform != "linux" {
		t.Fatalf("error = %#v, want a stopBits unsupported error for linux", err)
	}

	darwin := CapabilitiesFor("darwin")
	cfg = (Config{Path: "/dev/cu.usbserial", BaudRate: 115200, DataBits: 8,
		StopBits: StopBitsOne, Parity: ParityMark, FlowControl: FlowNone}).Normalized()
	if err := CheckSupport(cfg, darwin); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("mark parity on darwin = %v, want ErrUnsupported", err)
	}

	custom := (Config{Path: "/dev/ttyUSB0", BaudRate: 500000, DataBits: 8,
		StopBits: StopBitsOne, Parity: ParityNone, FlowControl: FlowNone}).Normalized()
	if err := CheckSupport(custom, linux); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("custom baud on linux = %v, want ErrUnsupported", err)
	}

	unsupported := CheckSupport(cfg, CapabilitiesFor("plan9"))
	if !errors.Is(unsupported, ErrUnsupported) {
		t.Fatalf("unsupported platform = %v, want ErrUnsupported", unsupported)
	}

	// Win32 accepts 1.5 stop bits only with 5 data bits; the combination is a
	// typed unsupported setting rather than a raw driver failure.
	windows := CapabilitiesFor("windows")
	badOneAndAHalf := (Config{Path: "COM3", BaudRate: 115200, DataBits: 8,
		StopBits: StopBitsOneAndHalf, Parity: ParityNone, FlowControl: FlowNone}).Normalized()
	if err := CheckSupport(badOneAndAHalf, windows); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("1.5 stop bits with 8 data bits = %v, want ErrUnsupported", err)
	}
	goodOneAndAHalf := badOneAndAHalf
	goodOneAndAHalf.DataBits = 5
	if err := CheckSupport(goodOneAndAHalf, windows); err != nil {
		t.Fatalf("1.5 stop bits with 5 data bits = %v, want supported", err)
	}
}

func TestValidPortPathAcceptsTheOriginalForms(t *testing.T) {
	for _, path := range []string{"/dev/ttyS0", "/dev/pts/4", "COM1", "com12", `\\.\COM7`} {
		if !ValidPortPath(path) {
			t.Fatalf("ValidPortPath(%q) = false", path)
		}
	}
	for _, path := range []string{"", "COM", "COMx", "/tmp/port", "ttyS0"} {
		if ValidPortPath(path) {
			t.Fatalf("ValidPortPath(%q) = true", path)
		}
	}
}

func TestPortNameMatchesTheOriginalSerialLabels(t *testing.T) {
	tests := map[string]string{
		"/dev/ttyUSB0":      "ttyUSB0",
		"/dev/cu.usbserial": "cu.usbserial",
		`\\.\COM7`:          "COM7",
		"COM3":              "COM3",
		"":                  "serial",
	}
	for path, want := range tests {
		if got := PortName(path); got != want {
			t.Fatalf("PortName(%q) = %q, want %q", path, got, want)
		}
	}
}
