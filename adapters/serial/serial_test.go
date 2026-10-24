package serial

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
)

// fakePort is an in-memory serial device. Tests push bytes to the Session and
// inspect what the Session wrote, so the real open path runs without hardware.
type fakePort struct {
	incoming chan []byte
	writes   chan []byte
	closed   chan struct{}
	once     sync.Once
	readErr  error

	mu       sync.Mutex
	closeCnt int
}

func newFakePort() *fakePort {
	return &fakePort{
		incoming: make(chan []byte, 8),
		writes:   make(chan []byte, 8),
		closed:   make(chan struct{}),
	}
}

func (p *fakePort) Read(buf []byte) (int, error) {
	select {
	case data, ok := <-p.incoming:
		if !ok {
			return 0, io.EOF
		}
		return copy(buf, data), nil
	case <-p.closed:
		if p.readErr != nil {
			return 0, p.readErr
		}
		return 0, io.EOF
	}
}

func (p *fakePort) Write(buf []byte) (int, error) {
	data := append([]byte(nil), buf...)
	select {
	case p.writes <- data:
	case <-p.closed:
		return 0, io.EOF
	}
	return len(buf), nil
}

func (p *fakePort) Close() error {
	p.once.Do(func() {
		p.mu.Lock()
		p.closeCnt++
		p.mu.Unlock()
		close(p.closed)
	})
	return nil
}

func (p *fakePort) closeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeCnt
}

func allCapabilities() domainserial.Capabilities {
	return domainserial.Capabilities{
		Platform:      "test",
		Supported:     true,
		DataBits:      []int{5, 6, 7, 8},
		StopBits:      []domainserial.StopBits{domainserial.StopBitsOne, domainserial.StopBitsOneAndHalf, domainserial.StopBitsTwo},
		Parities:      []domainserial.Parity{domainserial.ParityNone, domainserial.ParityEven, domainserial.ParityOdd, domainserial.ParityMark, domainserial.ParitySpace},
		FlowControls:  []domainserial.FlowControl{domainserial.FlowNone, domainserial.FlowXONXOFF, domainserial.FlowRTSCTS},
		ArbitraryBaud: true,
	}
}

func openFactory(t *testing.T, port io.ReadWriteCloser) Factory {
	t.Helper()
	caps := allCapabilities()
	return Factory{
		OpenPort: func(_ context.Context, _ domainserial.Config) (io.ReadWriteCloser, error) {
			return port, nil
		},
		Capabilities: &caps,
	}
}

func serialSpec(cfg domainserial.Config) appsession.StartSpec {
	return appsession.StartSpec{Kind: appsession.KindSerial, Serial: &cfg}
}

func TestFactoryListsThePortsThePlatformEnumerates(t *testing.T) {
	want := []domainserial.PortInfo{
		{Path: "COM3", Manufacturer: "FTDI", Type: "hardware"},
		{Path: "COM7", Type: "hardware"},
	}
	factory := Factory{ListPortsFunc: func(context.Context) ([]domainserial.PortInfo, error) {
		return append([]domainserial.PortInfo(nil), want...), nil
	}}
	got, err := factory.ListPorts(context.Background())
	if err != nil {
		t.Fatalf("ListPorts: %v", err)
	}
	if len(got) != 2 || got[0].Path != "COM3" || got[0].Manufacturer != "FTDI" || got[1].Path != "COM7" {
		t.Fatalf("ports = %+v, want %+v", got, want)
	}
}

func TestFactoryRejectsBadConfigurationAsATypedError(t *testing.T) {
	factory := openFactory(t, newFakePort())
	cfg := domainserial.Config{Path: "", BaudRate: 115200}
	_, err := factory.Open(context.Background(), serialSpec(cfg))
	if !errors.Is(err, domainserial.ErrInvalidConfig) {
		t.Fatalf("Open = %v, want ErrInvalidConfig", err)
	}
}

func TestFactoryRejectsUnsupportedSettingsAsATypedError(t *testing.T) {
	caps := allCapabilities()
	caps.StopBits = []domainserial.StopBits{domainserial.StopBitsOne, domainserial.StopBitsTwo}
	factory := Factory{
		Capabilities: &caps,
		OpenPort: func(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
			t.Fatal("the port was opened for an unsupported setting")
			return nil, nil
		},
	}
	cfg := domainserial.Config{Path: "COM3", BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOneAndHalf, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone}
	_, err := factory.Open(context.Background(), serialSpec(cfg))
	if !errors.Is(err, domainserial.ErrUnsupported) {
		t.Fatalf("Open = %v, want ErrUnsupported", err)
	}
	var typed *domainserial.UnsupportedError
	if !errors.As(err, &typed) || typed.Setting != "stopBits" {
		t.Fatalf("error = %#v, want the stopBits setting", err)
	}
}

func TestFactoryRejectsAnUnsupportedPlatformAndEnumerationAsTypedErrors(t *testing.T) {
	caps := domainserial.CapabilitiesFor("plan9")
	factory := Factory{Capabilities: &caps}
	cfg := domainserial.Config{Path: "COM3", BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOne, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone}
	if _, err := factory.Open(context.Background(), serialSpec(cfg)); !errors.Is(err, domainserial.ErrUnsupported) {
		t.Fatalf("Open on an unsupported platform = %v, want ErrUnsupported", err)
	}
	if _, err := factory.ListPorts(context.Background()); !errors.Is(err, domainserial.ErrUnsupported) {
		t.Fatalf("ListPorts on an unsupported platform = %v, want ErrUnsupported", err)
	}
}

func TestOpenReadWriteCloseOverAFakePort(t *testing.T) {
	port := newFakePort()
	opened := make(chan domainserial.Config, 1)
	caps := allCapabilities()
	factory := Factory{
		Capabilities: &caps,
		OpenPort: func(_ context.Context, cfg domainserial.Config) (io.ReadWriteCloser, error) {
			opened <- cfg
			return port, nil
		},
	}
	cfg := domainserial.Config{Path: "COM3", BaudRate: 9600, DataBits: 7,
		StopBits: domainserial.StopBitsTwo, Parity: domainserial.ParityEven,
		FlowControl: domainserial.FlowRTSCTS}
	transport, err := factory.Open(context.Background(), serialSpec(cfg))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got := <-opened
	if got != cfg {
		t.Fatalf("opened config = %+v, want %+v", got, cfg)
	}

	if _, err := transport.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	select {
	case written := <-port.writes:
		if string(written) != "ping" {
			t.Fatalf("port received %q, want ping", written)
		}
	default:
		t.Fatal("the write never reached the port")
	}

	port.incoming <- []byte("device-ready")
	buf := make([]byte, 64)
	n, err := transport.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf[:n]) != "device-ready" {
		t.Fatalf("Read = %q, want device-ready", buf[:n])
	}

	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-port.closed:
	default:
		t.Fatal("Close did not close the port")
	}
	if port.closeCount() != 1 {
		t.Fatalf("port close count = %d, want 1", port.closeCount())
	}
	reporter, ok := transport.(appsession.EndReporter)
	if !ok {
		t.Fatal("the serial transport does not classify its end")
	}
	end := reporter.End()
	if end.Reason != appsession.EndReasonClosed || !end.HasExitCode || end.ExitCode != 0 {
		t.Fatalf("End() = %+v, want a clean exit 0", end)
	}
}

func TestEveryFramingAndFlowCombinationReachesThePort(t *testing.T) {
	caps := allCapabilities()
	var recorded []domainserial.Config
	var mu sync.Mutex
	factory := Factory{
		Capabilities: &caps,
		OpenPort: func(_ context.Context, cfg domainserial.Config) (io.ReadWriteCloser, error) {
			mu.Lock()
			recorded = append(recorded, cfg)
			mu.Unlock()
			return newFakePort(), nil
		},
	}
	for dataBits := 5; dataBits <= 8; dataBits++ {
		for _, stopBits := range []domainserial.StopBits{domainserial.StopBitsOne, domainserial.StopBitsOneAndHalf, domainserial.StopBitsTwo} {
			for _, parity := range []domainserial.Parity{domainserial.ParityNone, domainserial.ParityEven, domainserial.ParityOdd, domainserial.ParityMark, domainserial.ParitySpace} {
				for _, flow := range []domainserial.FlowControl{domainserial.FlowNone, domainserial.FlowXONXOFF, domainserial.FlowRTSCTS} {
					cfg := domainserial.Config{Path: "COM3", BaudRate: 115200, DataBits: dataBits,
						StopBits: stopBits, Parity: parity, FlowControl: flow}
					transport, err := factory.Open(context.Background(), serialSpec(cfg))
					if stopBits == domainserial.StopBitsOneAndHalf && dataBits != 5 {
						if !errors.Is(err, domainserial.ErrUnsupported) {
							t.Fatalf("Open(%+v) = %v, want ErrUnsupported for 1.5 stop bits", cfg, err)
						}
						continue
					}
					if err != nil {
						t.Fatalf("Open(%+v): %v", cfg, err)
					}
					_ = transport.Close()
				}
			}
		}
	}
	// 1/2 stop bits at every width plus 1.5 stop bits at 5 data bits.
	wantCount := 4*2*5*3 + 1*1*5*3
	if len(recorded) != wantCount {
		t.Fatalf("opened %d ports, want %d", len(recorded), wantCount)
	}
	seenFlow := map[domainserial.FlowControl]bool{}
	for _, cfg := range recorded {
		seenFlow[cfg.FlowControl] = true
	}
	if len(seenFlow) != 3 {
		t.Fatalf("flow controls reaching the port = %v, want all three", seenFlow)
	}
}

func TestPortFailureEndsTheSessionAsAnErrorAndClosePreservesIt(t *testing.T) {
	port := newFakePort()
	port.readErr = errors.New("device unplugged")
	factory := openFactory(t, port)
	cfg := domainserial.Config{Path: "COM3", BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOne, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone}
	transport, err := factory.Open(context.Background(), serialSpec(cfg))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = port.Close()
	if _, err := transport.Read(make([]byte, 8)); err == nil {
		t.Fatal("Read after the device failed returned no error")
	}
	_ = transport.Close()
	reporter := transport.(appsession.EndReporter)
	end := reporter.End()
	if end.Reason != appsession.EndReasonError || end.ExitCode != 1 {
		t.Fatalf("End() = %+v, want an error exit 1 after Close", end)
	}
}

func TestCanceledContextDoesNotOpenThePort(t *testing.T) {
	opened := false
	caps := allCapabilities()
	factory := Factory{
		Capabilities: &caps,
		OpenPort: func(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
			opened = true
			return newFakePort(), nil
		},
	}
	cfg := domainserial.Config{Path: "COM3", BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOne, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := factory.Open(ctx, serialSpec(cfg)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open = %v, want context.Canceled", err)
	}
	if opened {
		t.Fatal("the port was opened after its context was canceled")
	}
}
