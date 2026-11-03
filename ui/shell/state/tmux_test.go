package state

import (
	"context"
	"sync"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// endingFactory returns transports whose End classification the test chooses,
// so the Tab lifecycle can be driven without a real SSH connection.
type endingFactory struct {
	mu         sync.Mutex
	end        appsession.End
	transports []appsession.Transport
	spec       appsession.StartSpec
}

func (f *endingFactory) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spec = spec
	transport := &endingTransport{fakeTransport: newFakeTransport(), end: f.end}
	f.transports = append(f.transports, transport)
	return transport, nil
}

func (f *endingFactory) last() *endingTransport {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transports[len(f.transports)-1].(*endingTransport)
}

type endingTransport struct {
	*fakeTransport
	end appsession.End
}

func (t *endingTransport) End() appsession.End { return t.end }

func newEndingState(t *testing.T, end appsession.End) (*State, *endingFactory) {
	t.Helper()
	factory := &endingFactory{end: end}
	service := appsession.NewService(factory)
	t.Cleanup(service.CloseAll)
	state := NewState(
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
	)
	return state, factory
}

func TestOpenHostCarriesAutoAttachTmuxOnlyForPlainSSH(t *testing.T) {
	tests := []struct {
		name     string
		host     domainvault.Host
		expected bool
	}{
		{
			name: "ssh opt-in",
			host: domainvault.Host{
				ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
				Username: "root", Protocol: domainvault.ProtocolSSH, AutoAttachTmux: true,
			},
			expected: true,
		},
		{
			name: "ssh default off",
			host: domainvault.Host{
				ID: "host-2", Label: "web", Hostname: "example.com", Port: 22,
				Username: "root", Protocol: domainvault.ProtocolSSH,
			},
			expected: false,
		},
		{
			name: "ssh network device",
			host: domainvault.Host{
				ID: "host-3", Label: "switch", Hostname: "10.0.0.1", Port: 22,
				Username: "admin", Protocol: domainvault.ProtocolSSH,
				DeviceType: domainvault.DeviceTypeNetwork, AutoAttachTmux: true,
			},
			expected: false,
		},
		{
			name: "mosh",
			host: domainvault.Host{
				ID: "host-4", Label: "mosh", Hostname: "example.com", Port: 22,
				Username: "root", Protocol: domainvault.ProtocolMosh, AutoAttachTmux: true,
			},
			expected: false,
		},
		{
			name: "et",
			host: domainvault.Host{
				ID: "host-5", Label: "et", Hostname: "example.com", Port: 22,
				Username: "root", Protocol: domainvault.ProtocolET, AutoAttachTmux: true,
			},
			expected: false,
		},
		{
			name: "telnet",
			host: domainvault.Host{
				ID: "host-6", Label: "console", Hostname: "console.local", Port: 23,
				Protocol: domainvault.ProtocolTelnet, AutoAttachTmux: true,
			},
			expected: false,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			graph := newTestGraph(t)
			if err := graph.state.OpenHost(testCase.host); err != nil {
				t.Fatalf("OpenHost: %v", err)
			}
			waitFor(t, "the Tab", func() bool {
				graph.state.Flush()
				return graph.state.TabCount() == 1
			})
			if got := graph.factory.spec.AutoAttachTmux; got != testCase.expected {
				t.Fatalf("StartSpec AutoAttachTmux = %v, want %v", got, testCase.expected)
			}
		})
	}
}

func TestATmuxDetachClosesTheTabWithoutAnError(t *testing.T) {
	state, factory := newEndingState(t, appsession.End{
		Reason: appsession.EndReasonDetached, ExitCode: 0, HasExitCode: true,
	})
	if err := state.OpenHost(domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH, AutoAttachTmux: true,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Tab", func() bool {
		state.Flush()
		return state.TabCount() == 1
	})

	if err := factory.last().Close(); err != nil {
		t.Fatalf("Close transport: %v", err)
	}
	waitFor(t, "the detached Tab to close", func() bool {
		state.Flush()
		return state.TabCount() == 0
	})
	if state.Problem() != "" {
		t.Fatalf("problem = %q after a detach, want none", state.Problem())
	}
}

func TestAnOrdinaryShellExitKeepsTheTab(t *testing.T) {
	state, factory := newEndingState(t, appsession.End{
		Reason: appsession.EndReasonExited, ExitCode: 0, HasExitCode: true,
	})
	if err := state.OpenHost(domainvault.Host{
		ID: "host-1", Label: "web", Hostname: "example.com", Port: 22,
		Username: "root", Protocol: domainvault.ProtocolSSH,
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the Tab", func() bool {
		state.Flush()
		return state.TabCount() == 1
	})
	if err := factory.last().Close(); err != nil {
		t.Fatalf("Close transport: %v", err)
	}
	waitFor(t, "the Session to exit", func() bool {
		state.Flush()
		snapshot, ok := state.ActiveSnapshot()
		return ok && snapshot.Status == appsession.StatusExited
	})
	if state.TabCount() != 1 {
		t.Fatalf("tabs = %d after an ordinary exit, want the ended Tab", state.TabCount())
	}
}
