package serial

import (
	"context"
	"errors"
	"sync"
	"testing"

	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	"github.com/mslxl/natcatty/ui/locales"
)

type recordedOpen struct {
	mu    sync.Mutex
	calls []domainserial.Config
	err   error
}

func (r *recordedOpen) open(cfg domainserial.Config) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, cfg)
	return r.err
}

func (r *recordedOpen) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *recordedOpen) last(t *testing.T) domainserial.Config {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("opener was not called")
	}
	return r.calls[len(r.calls)-1]
}

func fakeLister(ports []domainserial.PortInfo, err error) PortLister {
	return func(context.Context) ([]domainserial.PortInfo, error) {
		return ports, err
	}
}

func TestNewStateUsesTheOriginalDefaults(t *testing.T) {
	state := NewState(fakeLister(nil, nil), nil)
	if state.Visible() {
		t.Fatal("new state is visible")
	}
	if got, want := state.BaudRate(), "115200"; got != want {
		t.Fatalf("baud = %q, want %q", got, want)
	}
	if got, want := state.DataBits(), "8"; got != want {
		t.Fatalf("data bits = %q, want %q", got, want)
	}
	if got, want := state.StopBits(), "1"; got != want {
		t.Fatalf("stop bits = %q, want %q", got, want)
	}
	if got, want := state.Parity(), "none"; got != want {
		t.Fatalf("parity = %q, want %q", got, want)
	}
	if got, want := state.FlowControl(), "none"; got != want {
		t.Fatalf("flow control = %q, want %q", got, want)
	}
}

func TestConfigCoversEveryFramingAndFlowCombination(t *testing.T) {
	state := NewState(fakeLister(nil, nil), nil)
	state.SetPort("/dev/ttyUSB0")

	dataBits := []string{"5", "6", "7", "8"}
	stopBits := []string{"1", "1.5", "2"}
	parities := []string{"none", "even", "odd", "mark", "space"}
	flows := []string{"none", "xon/xoff", "rts/cts"}

	checked := 0
	for _, data := range dataBits {
		for _, stop := range stopBits {
			for _, parity := range parities {
				for _, flow := range flows {
					state.SetDataBits(data)
					state.SetStopBits(stop)
					state.SetParity(parity)
					state.SetFlowControl(flow)

					cfg, err := state.Config()
					if err != nil {
						t.Fatalf("Config(%s,%s,%s,%s): %v", data, stop, parity, flow, err)
					}
					if cfg.Path != "/dev/ttyUSB0" || cfg.BaudRate != 115200 ||
						cfg.DataBits != atoi(data) ||
						cfg.StopBits != parseStop(t, stop) ||
						string(cfg.Parity) != parity ||
						string(cfg.FlowControl) != flow {
						t.Fatalf("Config(%s,%s,%s,%s) = %+v", data, stop, parity, flow, cfg)
					}
					checked++
				}
			}
		}
	}
	if checked != 180 {
		t.Fatalf("checked %d combinations, want 180", checked)
	}
}

func TestConfigRejectsUnreadableValuesWithTypedErrors(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*State)
		field string
	}{
		{name: "baud not a number", setup: func(s *State) { s.SetBaudRate("fast") }, field: "baudRate"},
		{name: "data bits not a number", setup: func(s *State) { s.SetDataBits("eight") }, field: "dataBits"},
		{name: "data bits out of range", setup: func(s *State) { s.SetDataBits("9") }, field: "dataBits"},
		{name: "stop bits", setup: func(s *State) { s.SetStopBits("3") }, field: "stopBits"},
		{name: "missing path", setup: func(s *State) { s.SetPort("  ") }, field: "port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := NewState(fakeLister(nil, nil), nil)
			state.SetPort("/dev/ttyUSB0")
			tc.setup(state)
			_, err := state.Config()
			if !errors.Is(err, domainserial.ErrInvalidConfig) {
				t.Fatalf("Config error = %v, want ErrInvalidConfig", err)
			}
			var invalid *domainserial.ValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("Config error = %T, want ValidationError", err)
			}
			if invalid.Field != tc.field {
				t.Fatalf("field = %q, want %q", invalid.Field, tc.field)
			}
		})
	}
}

func TestShowEnumeratesPortsAndSelectsTheFirst(t *testing.T) {
	ports := []domainserial.PortInfo{
		{Path: "/dev/ttyUSB0", Manufacturer: "FTDI"},
		{Path: "/dev/ttyACM0"},
	}
	redraws := 0
	state := NewState(fakeLister(ports, nil), nil, WithRedraw(func() { redraws++ }))

	state.Show()
	if !state.Visible() {
		t.Fatal("Show did not make the dialog visible")
	}
	if got := state.Ports(); len(got) != 2 || got[0].Path != "/dev/ttyUSB0" {
		t.Fatalf("ports = %+v", got)
	}
	if state.Port() != "/dev/ttyUSB0" {
		t.Fatalf("port = %q, want the first detected port", state.Port())
	}
	if redraws == 0 {
		t.Fatal("Show did not request a redraw")
	}

	state.RefreshPorts()
	if state.Port() != "/dev/ttyUSB0" {
		t.Fatalf("refresh replaced the user's port with %q", state.Port())
	}
}

func TestEnumerationFailureIsKeptAndDoesNotSelectAPort(t *testing.T) {
	want := errors.New("no serial subsystem")
	state := NewState(fakeLister(nil, want), nil)
	state.Show()
	if !errors.Is(state.PortsError(), want) {
		t.Fatalf("PortsError = %v, want %v", state.PortsError(), want)
	}
	if state.Port() != "" {
		t.Fatalf("port = %q, want empty", state.Port())
	}
}

func TestConnectPassesTheNormalizedConfigAndCloses(t *testing.T) {
	opener := &recordedOpen{}
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "COM3"}}, nil), opener.open)
	state.Show()
	state.SetBaudRate(" 9600 ")
	state.SetDataBits("7")
	state.SetStopBits("2")
	state.SetParity("even")
	state.SetFlowControl("rts/cts")

	if err := state.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if state.Visible() {
		t.Fatal("Connect left the dialog visible")
	}
	if state.Problem() != nil {
		t.Fatalf("problem = %v, want nil", state.Problem())
	}
	cfg := opener.last(t)
	if cfg.Path != "COM3" || cfg.BaudRate != 9600 || cfg.DataBits != 7 ||
		cfg.StopBits != domainserial.StopBitsTwo || cfg.Parity != domainserial.ParityEven ||
		cfg.FlowControl != domainserial.FlowRTSCTS {
		t.Fatalf("opened config = %+v", cfg)
	}
}

func TestConnectFailureKeepsTheDialogAndTheTypedError(t *testing.T) {
	typed := &domainserial.UnsupportedError{Platform: "linux", Setting: "stopBits", Value: "1.5"}
	opener := &recordedOpen{err: typed}
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyS0"}}, nil), opener.open)
	state.Show()

	err := state.Connect()
	if !errors.Is(err, domainserial.ErrUnsupported) {
		t.Fatalf("Connect error = %v, want ErrUnsupported", err)
	}
	if !state.Visible() {
		t.Fatal("Connect failure closed the dialog")
	}
	if !errors.Is(state.Problem(), domainserial.ErrUnsupported) {
		t.Fatalf("Problem = %v, want the typed unsupported error", state.Problem())
	}
}

func TestConnectRejectsAnUnsupportedSettingBeforeOpening(t *testing.T) {
	opener := &recordedOpen{}
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyS0"}}, nil), opener.open,
		WithCapabilities(domainserial.CapabilitiesFor("linux")))
	state.Show()
	state.SetStopBits("1.5")

	err := state.Connect()
	if !errors.Is(err, domainserial.ErrUnsupported) {
		t.Fatalf("Connect error = %v, want ErrUnsupported", err)
	}
	var unsupported *domainserial.UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Setting != "stopBits" {
		t.Fatalf("Connect error = %#v, want a stopBits unsupported error", err)
	}
	if opener.count() != 0 {
		t.Fatalf("opener calls = %d, want 0", opener.count())
	}
	if !state.Visible() {
		t.Fatal("an unsupported setting closed the dialog")
	}
	if !errors.Is(state.Problem(), domainserial.ErrUnsupported) {
		t.Fatalf("Problem = %v, want the typed unsupported error", state.Problem())
	}
}

func TestConnectWithoutAnOpenerReportsTheInternalError(t *testing.T) {
	state := NewState(fakeLister([]domainserial.PortInfo{{Path: "/dev/ttyS0"}}, nil), nil)
	state.Show()
	if err := state.Connect(); !errors.Is(err, ErrNoOpener) {
		t.Fatalf("Connect error = %v, want ErrNoOpener", err)
	}
	if !state.Visible() {
		t.Fatal("Connect without an opener closed the dialog")
	}
}

func TestInvalidSelectionIsIgnored(t *testing.T) {
	state := NewState(nil, nil)
	state.SetParity("bogus")
	state.SetFlowControl("bogus")
	if state.Parity() != "none" || state.FlowControl() != "none" {
		t.Fatalf("parity/flow = %q/%q, want none/none", state.Parity(), state.FlowControl())
	}
}

func TestProblemTextLocalizesEachErrorKind(t *testing.T) {
	m := locales.In("en")
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},
		{name: "invalid", err: &domainserial.ValidationError{Field: "path"}, want: m.SerialInvalidConfiguration()},
		{name: "unsupported", err: &domainserial.UnsupportedError{Platform: "linux"}, want: m.SerialUnsupported()},
		{name: "no opener", err: ErrNoOpener, want: m.ConnectionErrorInternal()},
		{name: "other", err: errors.New("device gone"), want: m.SerialConnectionFailed()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProblemText(m, tc.err); got != tc.want {
				t.Fatalf("ProblemText = %q, want %q", got, tc.want)
			}
		})
	}
}

func atoi(value string) int {
	digits := map[string]int{"5": 5, "6": 6, "7": 7, "8": 8}
	return digits[value]
}

func parseStop(t *testing.T, value string) domainserial.StopBits {
	t.Helper()
	stop, ok := domainserial.ParseStopBits(value)
	if !ok {
		t.Fatalf("ParseStopBits(%q) failed", value)
	}
	return stop
}
