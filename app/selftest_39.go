//go:build selftest

package app

// Ticket 39 self-test. Press F11, then 3, then 9. The check proves the serial
// transport contract without physical hardware:
//
//   - the platform enumerator returns either a usable list or a typed
//     unsupported error, and the injected enumerator's result is preserved;
//   - every framing and flow-control combination validates, and malformed
//     values produce typed validation errors;
//   - all three flow-control modes reach the port, and open/read/write/close
//     run over an in-process fake port;
//   - a clean close reports exit 0 as "closed", a device failure reports exit
//     1 as "error";
//   - settings the platform cannot express, and platforms without a backend,
//     return typed unsupported errors instead of opening or panicking.
//
// Every step is synchronous and checks its observable result; there is no
// sleep and no dependency on a real serial device.
//
// Result: %TEMP%\natcatty-selftest-39.json.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/egoist/mygo/ui"

	adapterserial "github.com/mslxl/natcatty/adapters/serial"
	appsession "github.com/mslxl/natcatty/application/session"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	"github.com/mslxl/natcatty/ui/selftest"
	serialui "github.com/mslxl/natcatty/ui/serial"
)

const selftest39Ticket = 39

func (t *selftest39) check() selftest.Check {
	return selftest.Check{
		Ticket: selftest39Ticket,
		Name:   "Serial port enumeration, framing, and transport",
		Steps: []selftest.Step{
			{Name: "enumerate-ports", Run: t.enumerateStep},
			{Name: "validate-framing-and-flow-control", Run: t.parametersStep},
			{Name: "open-read-write-close", Run: t.transportStep},
			{Name: "typed-unsupported", Run: t.unsupportedStep},
			{Name: "dialog-validates-and-opens", Run: t.dialogStep},
		},
	}
}

type selftest39 struct{}

// enumerateStep checks the real platform enumerator's contract and that a
// supplied enumerator is passed through unchanged.
func (t *selftest39) enumerateStep(*ui.Context) selftest.Outcome {
	ctx := context.Background()
	platform := adapterserial.Factory{}
	ports, err := platform.ListPorts(ctx)
	if err != nil && !errors.Is(err, domainserial.ErrUnsupported) {
		return selftest.Failed(fmt.Errorf("the platform enumerator failed unexpectedly: %w", err))
	}
	if err == nil {
		for _, port := range ports {
			if port.Path == "" || !domainserial.ValidPortPath(port.Path) {
				return selftest.Failed(fmt.Errorf("the platform enumerator returned an unusable path %q", port.Path))
			}
		}
	}

	want := []domainserial.PortInfo{
		{Path: "/dev/ttyUSB0", Manufacturer: "FTDI", Type: "hardware"},
		{Path: "COM3", Manufacturer: "Prolific", Type: "hardware"},
	}
	injected := adapterserial.Factory{
		ListPortsFunc: func(context.Context) ([]domainserial.PortInfo, error) {
			return append([]domainserial.PortInfo(nil), want...), nil
		},
	}
	got, err := injected.ListPorts(ctx)
	if err != nil {
		return selftest.Failed(fmt.Errorf("the injected enumerator failed: %w", err))
	}
	if len(got) != len(want) {
		return selftest.Failed(fmt.Errorf("injected ports = %+v, want %+v", got, want))
	}
	for i := range want {
		if got[i] != want[i] {
			return selftest.Failed(fmt.Errorf("injected port %d = %+v, want %+v", i, got[i], want[i]))
		}
	}

	detail := fmt.Sprintf("platform enumeration returned %d port(s) (%v)", len(ports), err)
	if err == nil {
		sorted := sort.SliceIsSorted(ports, func(i, j int) bool { return ports[i].Path < ports[j].Path })
		if !sorted {
			return selftest.Failed(fmt.Errorf("the platform enumerator returned an unsorted list: %+v", ports))
		}
	}
	return selftest.Passed(detail+"; the injected list was preserved",
		"adapters/serial.Factory.ListPortsFunc")
}

// parametersStep validates every framing and flow-control combination, every
// flow-control mode through Open, and the malformed cases.
func (t *selftest39) parametersStep(*ui.Context) selftest.Outcome {
	checked := 0
	for _, dataBits := range []int{5, 6, 7, 8} {
		for _, stopBits := range []domainserial.StopBits{
			domainserial.StopBitsOne,
			domainserial.StopBitsOneAndHalf,
			domainserial.StopBitsTwo,
		} {
			for _, parity := range []domainserial.Parity{
				domainserial.ParityNone,
				domainserial.ParityEven,
				domainserial.ParityOdd,
				domainserial.ParityMark,
				domainserial.ParitySpace,
			} {
				for _, flow := range []domainserial.FlowControl{
					domainserial.FlowNone,
					domainserial.FlowXONXOFF,
					domainserial.FlowRTSCTS,
				} {
					cfg := domainserial.Config{
						Path: "/dev/ttyS0", BaudRate: 115200, DataBits: dataBits,
						StopBits: stopBits, Parity: parity, FlowControl: flow,
					}
					if err := cfg.Validate(); err != nil {
						return selftest.Failed(fmt.Errorf(
							"valid config %d/%v/%s/%s failed: %w", dataBits, stopBits, parity, flow, err))
					}
					checked++
				}
			}
		}
	}
	if checked != 180 {
		return selftest.Failed(fmt.Errorf("checked %d combinations, want 180", checked))
	}

	invalid := []struct {
		name string
		cfg  domainserial.Config
	}{
		{name: "missing path", cfg: domainserial.Config{BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: "none", FlowControl: "none"}},
		{name: "bad baud", cfg: domainserial.Config{Path: "COM1", BaudRate: 0, DataBits: 8, StopBits: 1, Parity: "none", FlowControl: "none"}},
		{name: "bad data bits", cfg: domainserial.Config{Path: "COM1", BaudRate: 115200, DataBits: 9, StopBits: 1, Parity: "none", FlowControl: "none"}},
		{name: "bad stop bits", cfg: domainserial.Config{Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 3, Parity: "none", FlowControl: "none"}},
		{name: "bad parity", cfg: domainserial.Config{Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: "sideways", FlowControl: "none"}},
		{name: "bad flow control", cfg: domainserial.Config{Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 1, Parity: "none", FlowControl: "carrier-pigeon"}},
	}
	for _, tc := range invalid {
		if err := tc.cfg.Validate(); !errors.Is(err, domainserial.ErrInvalidConfig) {
			return selftest.Failed(fmt.Errorf("%s error = %v, want ErrInvalidConfig", tc.name, err))
		}
	}

	// Every flow-control mode must survive normalization and reach the port.
	caps := serialFakePortCapabilities()
	var seen []domainserial.FlowControl
	factory := adapterserial.Factory{
		Capabilities: &caps,
		OpenPort: func(_ context.Context, cfg domainserial.Config) (io.ReadWriteCloser, error) {
			seen = append(seen, cfg.FlowControl)
			return &selftest39Port{}, nil
		},
	}
	for _, flow := range []domainserial.FlowControl{
		domainserial.FlowNone, domainserial.FlowXONXOFF, domainserial.FlowRTSCTS,
	} {
		spec := serialSpec(domainserial.Config{
			Path: "COM1", BaudRate: 9600, DataBits: 8, StopBits: 1,
			Parity: domainserial.ParityNone, FlowControl: flow,
		})
		transport, err := factory.Open(context.Background(), spec)
		if err != nil {
			return selftest.Failed(fmt.Errorf("open with flow %q: %w", flow, err))
		}
		_ = transport.Close()
	}
	if len(seen) != 3 || seen[0] != domainserial.FlowNone ||
		seen[1] != domainserial.FlowXONXOFF || seen[2] != domainserial.FlowRTSCTS {
		return selftest.Failed(fmt.Errorf("flow controls reaching the port = %v", seen))
	}

	return selftest.Passed(
		"180 framing/flow combinations validate; malformed settings return typed errors; all three flow controls reach the port",
		"domain/model/serial.Config + adapters/serial.Factory.OpenPort")
}

// transportStep opens the transport over a fake port, reads, writes, closes,
// and checks both end classifications.
func (t *selftest39) transportStep(*ui.Context) selftest.Outcome {
	caps := serialFakePortCapabilities()
	port := &selftest39Port{incoming: []byte("OK\r\n")}
	opened := 0
	factory := adapterserial.Factory{
		Capabilities: &caps,
		OpenPort: func(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
			opened++
			return port, nil
		},
	}
	transport, err := factory.Open(context.Background(), serialSpec(domainserial.Config{
		Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 1,
		Parity: domainserial.ParityNone, FlowControl: domainserial.FlowRTSCTS,
	}))
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the fake port: %w", err))
	}
	if opened != 1 {
		return selftest.Failed(fmt.Errorf("the port opener ran %d times, want 1", opened))
	}

	if _, err := transport.Write([]byte("AT\r\n")); err != nil {
		return selftest.Failed(fmt.Errorf("write to the fake port: %w", err))
	}
	if got := port.written(); got != "AT\r\n" {
		return selftest.Failed(fmt.Errorf("the port received %q, want %q", got, "AT\r\n"))
	}

	reply := make([]byte, 4)
	if _, err := io.ReadFull(transport, reply); err != nil {
		return selftest.Failed(fmt.Errorf("read from the fake port: %w", err))
	}
	if string(reply) != "OK\r\n" {
		return selftest.Failed(fmt.Errorf("the transport read %q, want %q", reply, "OK\r\n"))
	}

	if err := transport.Close(); err != nil {
		return selftest.Failed(fmt.Errorf("close the fake port: %w", err))
	}
	reporter, ok := transport.(appsession.EndReporter)
	if !ok {
		return selftest.Failed(errors.New("the serial transport does not report its end"))
	}
	if end := reporter.End(); end.Reason != appsession.EndReasonClosed || !end.HasExitCode || end.ExitCode != 0 {
		return selftest.Failed(fmt.Errorf("clean close end = %+v, want closed/0", end))
	}

	failing := &selftest39Port{readErr: errors.New("device unplugged")}
	failingFactory := adapterserial.Factory{
		Capabilities: &caps,
		OpenPort: func(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
			return failing, nil
		},
	}
	broken, err := failingFactory.Open(context.Background(), serialSpec(domainserial.Config{
		Path: "COM1", BaudRate: 115200, DataBits: 8, StopBits: 1,
		Parity: domainserial.ParityNone, FlowControl: domainserial.FlowNone,
	}))
	if err != nil {
		return selftest.Failed(fmt.Errorf("open the failing fake port: %w", err))
	}
	if _, err := broken.Read(make([]byte, 1)); err == nil {
		return selftest.Failed(errors.New("a failing device read succeeded"))
	}
	if end := broken.(appsession.EndReporter).End(); end.Reason != appsession.EndReasonError ||
		!end.HasExitCode || end.ExitCode != 1 {
		return selftest.Failed(fmt.Errorf("device failure end = %+v, want error/1", end))
	}
	if err := broken.Close(); err != nil {
		return selftest.Failed(fmt.Errorf("close the failing fake port: %w", err))
	}

	return selftest.Passed(
		"opened, wrote AT\\r\\n, read OK\\r\\n, and closed with exit 0; a device failure ended with exit 1",
		"adapters/serial.Factory.OpenPort + in-process fake port")
}

// unsupportedStep proves a setting the platform cannot express and a platform
// without a backend both return typed unsupported errors and never panic.
func (t *selftest39) unsupportedStep(*ui.Context) selftest.Outcome {
	linux := domainserial.CapabilitiesFor("linux")
	opened := 0
	factory := adapterserial.Factory{
		Capabilities: &linux,
		OpenPort: func(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
			opened++
			return &selftest39Port{}, nil
		},
	}
	_, err := factory.Open(context.Background(), serialSpec(domainserial.Config{
		Path: "/dev/ttyS0", BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOneAndHalf, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone,
	}))
	if !errors.Is(err, domainserial.ErrUnsupported) {
		return selftest.Failed(fmt.Errorf("1.5 stop bits on Linux error = %v, want ErrUnsupported", err))
	}
	var unsupported *domainserial.UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Setting != "stopBits" {
		return selftest.Failed(fmt.Errorf("typed unsupported error = %#v", err))
	}
	if opened != 0 {
		return selftest.Failed(fmt.Errorf("an unsupported setting still opened the port (%d times)", opened))
	}

	// Use an explicit unsupported table to exercise the no-backend path
	// deterministically, whatever this machine runs.
	none := domainserial.CapabilitiesFor("plan9")
	noBackend := adapterserial.Factory{Capabilities: &none}
	_, err = noBackend.Open(context.Background(), serialSpec(domainserial.Config{
		Path: "/dev/ttyS0", BaudRate: 115200, DataBits: 8, StopBits: 1,
		Parity: domainserial.ParityNone, FlowControl: domainserial.FlowNone,
	}))
	if !errors.Is(err, domainserial.ErrUnsupported) {
		return selftest.Failed(fmt.Errorf("a platform without a backend error = %v, want ErrUnsupported", err))
	}
	if _, err := noBackend.ListPorts(context.Background()); !errors.Is(err, domainserial.ErrUnsupported) {
		return selftest.Failed(fmt.Errorf("enumeration without a backend error = %v, want ErrUnsupported", err))
	}

	return selftest.Passed(
		"1.5 stop bits on Linux and an unknown platform both returned typed unsupported errors without opening a port",
		"domain/model/serial.CapabilitiesFor + adapters/serial.Factory.Capabilities")
}

// dialogStep proves the dialog's form validates flow control and that a
// successful Connect hands a normalized configuration to the opener.
func (t *selftest39) dialogStep(*ui.Context) selftest.Outcome {
	var opened []domainserial.Config
	dialog := serialui.NewState(
		func(context.Context) ([]domainserial.PortInfo, error) {
			return []domainserial.PortInfo{{Path: "COM7"}}, nil
		},
		func(cfg domainserial.Config) error {
			opened = append(opened, cfg)
			return nil
		},
	)
	dialog.Show()
	if !dialog.Visible() || dialog.Port() != "COM7" {
		return selftest.Failed(fmt.Errorf("the dialog did not enumerate and select a port (visible=%v port=%q)",
			dialog.Visible(), dialog.Port()))
	}

	dialog.SetBaudRate("57600")
	dialog.SetDataBits("7")
	dialog.SetStopBits("2")
	dialog.SetParity("even")
	dialog.SetFlowControl("xon/xoff")
	cfg, err := dialog.Config()
	if err != nil {
		return selftest.Failed(fmt.Errorf("dialog Config: %w", err))
	}
	if cfg.BaudRate != 57600 || cfg.DataBits != 7 || cfg.StopBits != domainserial.StopBitsTwo ||
		cfg.Parity != domainserial.ParityEven || cfg.FlowControl != domainserial.FlowXONXOFF {
		return selftest.Failed(fmt.Errorf("dialog config = %+v", cfg))
	}
	if err := dialog.Connect(); err != nil {
		return selftest.Failed(fmt.Errorf("dialog Connect: %w", err))
	}
	if len(opened) != 1 || opened[0] != cfg {
		return selftest.Failed(fmt.Errorf("the dialog opened %+v, want %+v", opened, cfg))
	}
	if dialog.Visible() {
		return selftest.Failed(errors.New("a successful Connect left the dialog visible"))
	}

	dialog.Show()
	dialog.SetDataBits("9")
	if err := dialog.Connect(); !errors.Is(err, domainserial.ErrInvalidConfig) {
		return selftest.Failed(fmt.Errorf("invalid dialog Connect error = %v, want ErrInvalidConfig", err))
	}
	if !dialog.Visible() {
		return selftest.Failed(errors.New("an invalid Connect closed the dialog"))
	}

	return selftest.Passed(
		"the dialog validated baud, framing, and flow control, opened a normalized config, and stayed open on an invalid one",
		"ui/serial.State")
}

// selftest39Port is an in-process serial port. Reads drain incoming bytes or
// report readErr; writes are recorded.
type selftest39Port struct {
	mu       sync.Mutex
	incoming []byte
	writes   []byte
	readErr  error
	closed   bool
}

func (p *selftest39Port) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.EOF
	}
	if p.readErr != nil {
		return 0, p.readErr
	}
	if len(p.incoming) == 0 {
		return 0, io.EOF
	}
	n := copy(b, p.incoming)
	p.incoming = p.incoming[n:]
	return n, nil
}

func (p *selftest39Port) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.ErrClosedPipe
	}
	p.writes = append(p.writes, b...)
	return len(b), nil
}

func (p *selftest39Port) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *selftest39Port) written() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.writes)
}

// serialFakePortCapabilities is the widest capability table, so the fake-port
// steps reach the serial code on every host instead of testing the host's
// platform limits.
func serialFakePortCapabilities() domainserial.Capabilities {
	return domainserial.CapabilitiesFor("windows")
}

// serialSpec wraps a serial config as a Session start spec.
func serialSpec(cfg domainserial.Config) appsession.StartSpec {
	return appsession.StartSpec{Kind: appsession.KindSerial, Serial: &cfg}
}
