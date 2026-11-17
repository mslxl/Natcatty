package serial

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
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
	for _, path := range []string{"/dev/ttyS0", "/dev/pts/4", "COM1", "com12", `\\.\COM7`,
		`\\.\pipe\vm-console`, `\\.\pipe\console with spaces`, `\\?\pipe\vm-console`} {
		if !ValidPortPath(path) {
			t.Fatalf("ValidPortPath(%q) = false", path)
		}
	}
	for _, path := range []string{"", "COM", "COMx", "/tmp/port", "ttyS0",
		`\\server\pipe\vm-console`, `\\.\pipe\`, `\\.\pipe`} {
		if ValidPortPath(path) {
			t.Fatalf("ValidPortPath(%q) = true", path)
		}
	}
}

// TestNamedPipeEndpointsAreWindowsOnly keeps a pipe path a valid serial form
// for the saved-port list while rejecting it on a platform that cannot open
// one, and accepts framing the pipe ignores.
func TestNamedPipeEndpointsAreWindowsOnly(t *testing.T) {
	windowsCaps := CapabilitiesFor("windows")
	linuxCaps := CapabilitiesFor("linux")
	for _, path := range []string{`\\.\pipe\vm-console`, `\\.\pipe\console with spaces`, `\\?\pipe\vm-console`} {
		if !IsNamedPipePath(path) {
			t.Fatalf("IsNamedPipePath(%q) = false", path)
		}
		cfg := (Config{Path: path, BaudRate: 500000, DataBits: 8,
			StopBits: StopBitsOneAndHalf, Parity: ParityMark, FlowControl: FlowRTSCTS}).Normalized()
		if err := CheckSupport(cfg, windowsCaps); err != nil {
			t.Fatalf("CheckSupport(%q, windows) = %v, want supported", path, err)
		}
		if err := CheckSupport(cfg, linuxCaps); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("CheckSupport(%q, linux) = %v, want ErrUnsupported", path, err)
		}
	}

	for _, path := range []string{`\\server\pipe\vm-console`, `\\.\pipe\`, "pipe\\vm-console"} {
		if IsNamedPipePath(path) {
			t.Fatalf("IsNamedPipePath(%q) = true, want a local pipe only", path)
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

// TestLegacyConfigDecodesWithoutAutoLogin keeps an older workspace payload,
// written before auto-login existed, decodable and openable unchanged.
func TestLegacyConfigDecodesWithoutAutoLogin(t *testing.T) {
	const legacy = `{"path":"COM3","baudRate":115200,"dataBits":8,"stopBits":1,"parity":"none","flowControl":"none"}`
	var cfg Config
	if err := json.Unmarshal([]byte(legacy), &cfg); err != nil {
		t.Fatalf("decode legacy config: %v", err)
	}
	if cfg.AutoLogin != nil {
		t.Fatalf("legacy config invented auto-login: %+v", cfg.AutoLogin)
	}
	if err := cfg.Normalized().Validate(); err != nil {
		t.Fatalf("legacy config is not openable: %v", err)
	}
}

// TestAutoLoginRoundTripsWithoutAPassword proves the persisted profile keeps
// its username, rules, attempt limit, and secret-presence flag while carrying
// no password field at all.
func TestAutoLoginRoundTripsWithoutAPassword(t *testing.T) {
	cfg := Config{
		Path: "  /dev/ttyUSB0  ", BaudRate: 115200,
		AutoLogin: &AutoLogin{
			Enabled:     true,
			Username:    " admin ",
			PasswordSet: true,
			MaxAttempts: 2,
			Replies: []AutoLoginReply{
				{Expect: " Are you sure ", Respond: " yes "},
				{Expect: "  ", Respond: "dropped"},
			},
		},
	}.Normalized()

	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(encoded), `"password"`) {
		t.Fatalf("encoded profile names a password field: %s", encoded)
	}
	var decoded Config
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	profile := decoded.AutoLogin
	if profile == nil || !profile.Enabled || profile.Username != "admin" ||
		!profile.PasswordSet || profile.MaxAttempts != 2 {
		t.Fatalf("decoded profile = %+v", profile)
	}
	wantReplies := []AutoLoginReply{{Expect: "Are you sure", Respond: "yes"}}
	if !slices.Equal(profile.Replies, wantReplies) {
		t.Fatalf("decoded replies = %+v, want %+v", profile.Replies, wantReplies)
	}
	if got, want := cfg.SecretProfileID(), "/dev/ttyUSB0"; got != want {
		t.Fatalf("SecretProfileID = %q, want %q", got, want)
	}
	if SecretEntityType != "serial_profile" || SecretFieldPassword != "password" {
		t.Fatalf("secret names changed: %q %q", SecretEntityType, SecretFieldPassword)
	}
}

// TestSecretProfileIDCanonicalizesEquivalentSpellings pins the identity of a
// stored serial credential. A user who typed "com3" in one dialog and
// "\\.\COM3" in another must reach the same secret, while two different
// devices and two different POSIX paths stay distinct.
func TestSecretProfileIDCanonicalizesEquivalentSpellings(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "upper COM", path: "COM3", want: "COM3"},
		{name: "lower COM", path: "com3", want: "COM3"},
		{name: "padded mixed COM", path: " Com3 ", want: "COM3"},
		{name: "windows device prefix", path: `\\.\COM3`, want: "COM3"},
		{name: "padded device prefix", path: `  \\.\com3  `, want: "COM3"},
		{name: "posix path", path: "/dev/ttyUSB0", want: "/dev/ttyUSB0"},
		{name: "posix case", path: "/dev/TTYUSB0", want: "/dev/TTYUSB0"},
		{name: "posix padded", path: " /dev/ttyUSB0 ", want: "/dev/ttyUSB0"},
		{name: "named pipe", path: `\\.\pipe\Console`, want: `\\.\pipe\console`},
		{name: "question prefix", path: `\\?\pipe\CONSOLE`, want: `\\.\pipe\console`},
		{name: "upper pipe prefix", path: `\\.\PIPE\Console`, want: `\\.\pipe\console`},
		{name: "named pipe with space", path: `\\.\pipe\Console 1`, want: `\\.\pipe\console 1`},
		{name: "empty", path: "  ", want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := (Config{Path: testCase.path}).SecretProfileID(); got != testCase.want {
				t.Fatalf("SecretProfileID(%q) = %q, want %q", testCase.path, got, testCase.want)
			}
		})
	}

	if first, second := (Config{Path: `\\.\pipe\one`}).SecretProfileID(),
		(Config{Path: `\\.\pipe\two`}).SecretProfileID(); first == second {
		t.Fatalf("two named pipes share the key %q", first)
	}
	if first, second := (Config{Path: "/dev/ttyUSB0"}).SecretProfileID(),
		(Config{Path: "/dev/ttyUSB1"}).SecretProfileID(); first == second {
		t.Fatalf("two POSIX devices share the key %q", first)
	}
	// Only the secret key folds: the path the transport opens keeps the
	// spelling the user chose.
	if got := (Config{Path: `\\.\COM3`}).Normalized().Path; got != `\\.\COM3` {
		t.Fatalf("Normalized path = %q, want the original device spelling", got)
	}
}
