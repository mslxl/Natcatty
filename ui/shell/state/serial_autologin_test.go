package state

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	adapterserial "github.com/mslxl/natcatty/adapters/serial"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/application/session/autologin"
	domainserial "github.com/mslxl/natcatty/domain/model/serial"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// serialTestConfig is a valid profile with a saved automation.
func serialTestConfig() domainserial.Config {
	return domainserial.Config{
		Path: "COM9", BaudRate: 115200, DataBits: 8,
		StopBits: domainserial.StopBitsOne, Parity: domainserial.ParityNone,
		FlowControl: domainserial.FlowNone,
		AutoLogin: &domainserial.AutoLogin{
			Enabled: true, Username: "console", PasswordSet: true, MaxAttempts: 2,
			Replies: []domainserial.AutoLoginReply{{Expect: "Site code:", Respond: "lab-1"}},
		},
	}
}

func TestOpenSerialAttachesTheProfileAndPersistsNoSecret(t *testing.T) {
	graph := newWorkspaceGraphWithOptions(t,
		WithSerialAutoLogin(func(_ context.Context, cfg domainserial.Config) *autologin.Options {
			// The app resolver reads the password from the store; here the
			// store is the test's fixed value.
			return appsession.SerialAutoLoginOptions(cfg.AutoLogin, "pa55-secret", true)
		}),
	)
	cfg := serialTestConfig()
	if err := graph.state.OpenSerial(cfg); err != nil {
		t.Fatalf("OpenSerial: %v", err)
	}
	graph.flushUntil(t, "the serial tab", func() bool {
		return graph.state.TabCount() == 1
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindSerial || spec.Serial == nil || spec.Serial.Path != "COM9" {
		t.Fatalf("start spec = %+v, want the serial profile", spec)
	}
	if spec.AutoLogin == nil || spec.AutoLogin.Username != "console" ||
		spec.AutoLogin.Password != "pa55-secret" || !spec.AutoLogin.PasswordSet {
		t.Fatalf("auto-login = %+v, want the profile and stored password", spec.AutoLogin)
	}
	if len(spec.AutoLogin.Replies) != 1 || spec.AutoLogin.Replies[0].Expect != "Site code:" {
		t.Fatalf("auto-login replies = %+v, want the saved rule", spec.AutoLogin.Replies)
	}

	payload := graph.state.WorkspacePayload()
	if len(payload.Panes) != 1 || payload.Panes[0].Session.Serial == nil {
		t.Fatalf("payload panes = %+v, want one serial descriptor", payload.Panes)
	}
	saved := payload.Panes[0].Session.Serial
	if saved.AutoLogin == nil || saved.AutoLogin.Username != "console" ||
		!saved.AutoLogin.PasswordSet || len(saved.AutoLogin.Replies) != 1 {
		t.Fatalf("persisted profile = %+v, want the non-secret automation", saved.AutoLogin)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if strings.Contains(string(encoded), "pa55-secret") {
		t.Fatalf("workspace JSON contains the password: %s", encoded)
	}
}

func TestRestoredSerialPaneAttachesItsSavedAutoLogin(t *testing.T) {
	first := newWorkspaceGraphWithOptions(t,
		WithSerialAutoLogin(func(_ context.Context, cfg domainserial.Config) *autologin.Options {
			return appsession.SerialAutoLoginOptions(cfg.AutoLogin, "pa55-secret", true)
		}),
	)
	if err := first.state.OpenSerial(serialTestConfig()); err != nil {
		t.Fatalf("OpenSerial: %v", err)
	}
	first.flushUntil(t, "the serial tab", func() bool {
		panes := first.state.WorkspacePayload().Panes
		return len(panes) == 1 && panes[0].Session.Serial != nil
	})
	payload := first.state.WorkspacePayload()

	resolved := make(chan domainserial.Config, 1)
	second := newWorkspaceGraphWithOptions(t,
		WithSerialAutoLogin(func(_ context.Context, cfg domainserial.Config) *autologin.Options {
			resolved <- cfg
			return appsession.SerialAutoLoginOptions(cfg.AutoLogin, "pa55-secret", true)
		}),
	)
	second.repo.payload = payload
	second.state.Restore(context.Background())
	waitFor(t, "the restored serial Session to resolve auto-login", func() bool {
		second.state.Flush()
		return len(resolved) == 1
	})

	cfg := <-resolved
	if cfg.Path != "COM9" || cfg.AutoLogin == nil || cfg.AutoLogin.Username != "console" {
		t.Fatalf("restore resolver saw %+v, want the saved profile", cfg)
	}
	spec := second.factory.lastSpec()
	if spec.Kind != appsession.KindSerial || spec.AutoLogin == nil ||
		spec.AutoLogin.Username != "console" || spec.AutoLogin.Password != "pa55-secret" {
		t.Fatalf("restored start spec = %+v, want the saved auto-login", spec)
	}
}

func TestOpenTelnetHostAttachesTheVaultLogin(t *testing.T) {
	graph := newWorkspaceGraphWithOptions(t,
		WithTelnetAutoLogin(func(_ context.Context, host domainvault.Host, password string) *autologin.Options {
			if password != "" {
				t.Errorf("a saved Host supplied a one-shot password %q", password)
			}
			if host.Username == "" {
				return nil
			}
			return &autologin.Options{Username: host.Username, Password: "vault-secret", PasswordSet: true}
		}),
	)
	host := domainvault.Host{
		ID: "host-telnet", Label: "console", Hostname: "console.local",
		Port: 23, Username: "admin", Protocol: domainvault.ProtocolTelnet,
	}
	if err := graph.state.OpenHost(host); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	graph.flushUntil(t, "the Telnet tab", func() bool {
		return graph.state.TabCount() == 1
	})

	spec := graph.factory.lastSpec()
	if spec.Kind != appsession.KindTelnet || spec.AutoLogin == nil ||
		spec.AutoLogin.Username != "admin" || spec.AutoLogin.Password != "vault-secret" {
		t.Fatalf("Telnet start spec = %+v, want the Vault login as auto-login", spec)
	}
}

// serialIntegrationPort is an in-memory serial device for the production open
// path: the real Session service and the real serial adapter run above it.
type serialIntegrationPort struct {
	incoming chan []byte
	closed   chan struct{}
	once     sync.Once

	mu      sync.Mutex
	written []byte
}

func newSerialIntegrationPort() *serialIntegrationPort {
	return &serialIntegrationPort{
		incoming: make(chan []byte, 8),
		closed:   make(chan struct{}),
	}
}

func (p *serialIntegrationPort) Read(buf []byte) (int, error) {
	select {
	case data := <-p.incoming:
		return copy(buf, data), nil
	case <-p.closed:
		return 0, io.EOF
	}
}

func (p *serialIntegrationPort) Write(buf []byte) (int, error) {
	p.mu.Lock()
	p.written = append(p.written, buf...)
	p.mu.Unlock()
	return len(buf), nil
}

func (p *serialIntegrationPort) Close() error {
	p.once.Do(func() { close(p.closed) })
	return nil
}

func (p *serialIntegrationPort) input() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.written)
}

// TestSerialAutoLoginRunsThroughTheProductionOpenPath drives State.OpenSerial
// with the real serial adapter and Session service over a fake device. The
// saved login must answer both prompts without any test-only shortcut.
func TestSerialAutoLoginRunsThroughTheProductionOpenPath(t *testing.T) {
	port := newSerialIntegrationPort()
	capabilities := domainserial.Capabilities{
		Platform: "test", Supported: true,
		DataBits:      []int{5, 6, 7, 8},
		StopBits:      []domainserial.StopBits{domainserial.StopBitsOne, domainserial.StopBitsOneAndHalf, domainserial.StopBitsTwo},
		Parities:      []domainserial.Parity{domainserial.ParityNone, domainserial.ParityEven, domainserial.ParityOdd, domainserial.ParityMark, domainserial.ParitySpace},
		FlowControls:  []domainserial.FlowControl{domainserial.FlowNone, domainserial.FlowXONXOFF, domainserial.FlowRTSCTS},
		ArbitraryBaud: true,
	}
	factory := adapterserial.Factory{
		OpenPort: func(context.Context, domainserial.Config) (io.ReadWriteCloser, error) {
			return port, nil
		},
		Capabilities: &capabilities,
	}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	shell := NewState(
		WithRedrawRequest(func() {}),
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
		WithSerialAutoLogin(func(_ context.Context, cfg domainserial.Config) *autologin.Options {
			return appsession.SerialAutoLoginOptions(cfg.AutoLogin, "pa55-secret", true)
		}),
	)
	t.Cleanup(shell.Shutdown)

	if err := shell.OpenSerial(serialTestConfig()); err != nil {
		t.Fatalf("OpenSerial: %v", err)
	}
	waitFor(t, "the serial tab", func() bool {
		shell.Flush()
		return shell.TabCount() == 1
	})

	port.incoming <- []byte("login: ")
	waitFor(t, "the username", func() bool {
		return port.input() == "console\r"
	})
	port.incoming <- []byte("\r\nPassword: ")
	waitFor(t, "the password", func() bool {
		return port.input() == "console\rpa55-secret\r"
	})
}
