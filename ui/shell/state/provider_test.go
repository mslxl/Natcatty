package state

import (
	"context"
	"errors"
	"sync"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	appworkspace "github.com/mslxl/natcatty/application/workspace"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	domainworkspace "github.com/mslxl/natcatty/domain/workspace"
)

// providerMetadataTransport adds the provider-owned open result to the fake
// transport, so a Workspace save sees the same metadata the Session carries.
type providerMetadataTransport struct {
	*fakeTransport
	provider string
	protocol string
	restore  []byte
}

func (t *providerMetadataTransport) InitialSize() (int, int) { return 100, 30 }

func (t *providerMetadataTransport) ProviderID() string { return t.provider }

func (t *providerMetadataTransport) Protocol() string { return t.protocol }

func (t *providerMetadataTransport) RestoreDescriptor() []byte {
	return append([]byte(nil), t.restore...)
}

type providerGraphFactory struct {
	mu         sync.Mutex
	specs      []appsession.StartSpec
	transports []*providerMetadataTransport
}

func (f *providerGraphFactory) Open(_ context.Context,
	spec appsession.StartSpec) (appsession.Transport, error) {
	transport := &providerMetadataTransport{
		fakeTransport: newFakeTransport(),
		provider:      "acme.loop",
		protocol:      "x-acme-loop",
		restore:       []byte("restore-state"),
	}
	f.mu.Lock()
	f.specs = append(f.specs, spec)
	f.transports = append(f.transports, transport)
	f.mu.Unlock()
	return transport, nil
}

func (f *providerGraphFactory) snapshot() ([]appsession.StartSpec, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]appsession.StartSpec(nil), f.specs...), len(f.specs)
}

type providerGraph struct {
	state   *State
	factory *providerGraphFactory
	repo    *memoryWorkspaceRepository
}

func newProviderGraph(t *testing.T, claimed bool) *providerGraph {
	t.Helper()
	graph := &providerGraph{
		factory: &providerGraphFactory{},
		repo:    &memoryWorkspaceRepository{payload: domainworkspace.EmptyPayload()},
	}
	service := appsession.NewService(graph.factory)
	t.Cleanup(service.CloseAll)
	options := []Option{
		WithSessions(service, func(*appsession.Session) (Surface, error) {
			return &fakeSurface{}, nil
		}),
		WithWorkspace(appworkspace.NewService(graph.repo),
			func(context.Context, model.HostID) (domainvault.Host, bool) {
				return domainvault.Host{}, false
			}),
	}
	if claimed {
		options = append(options, WithProviderClaims(func(protocol string) bool {
			return protocol == "x-acme-loop"
		}))
	}
	graph.state = NewState(options...)
	return graph
}

func TestOpenHostUsesThePluginKindForAClaimedProvider(t *testing.T) {
	graph := newTestGraph(t)
	graph.state.SetProviderClaims(func(protocol string) bool {
		return protocol == "x-acme-loop"
	})
	err := graph.state.OpenHost(domainvault.Host{
		ID: "host-plugin", Label: "plugin", Hostname: "plugin.example.com",
		Port: 2222, Username: "demo", Protocol: "x-acme-loop",
	})
	if err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	// The dial goroutine queues its outcome; Flush applies it under the state
	// lock, which also publishes the factory's recorded spec to this goroutine.
	waitFor(t, "the provider Tab", func() bool {
		graph.state.Flush()
		return graph.state.TabCount() == 1
	})
	spec := graph.factory.spec
	if spec.Kind != appsession.KindPlugin || spec.Protocol != "x-acme-loop" {
		t.Fatalf("StartSpec = %+v, want a plugin Session for x-acme-loop", spec)
	}
	if spec.HostID != "host-plugin" || spec.Hostname != "plugin.example.com" {
		t.Fatalf("StartSpec endpoint = %+v", spec)
	}
}

func TestOpenHostPersistsAndRestoresTheProviderDescriptor(t *testing.T) {
	graph := newProviderGraph(t, true)
	if err := graph.state.OpenHost(domainvault.Host{
		ID: "host-plugin", Label: "plugin", Hostname: "plugin.example.com",
		Port: 2222, Username: "demo", Protocol: "x-acme-loop",
	}); err != nil {
		t.Fatalf("OpenHost: %v", err)
	}
	waitFor(t, "the plugin Pane", func() bool {
		graph.state.Flush()
		_, count := graph.factory.snapshot()
		return graph.state.TabCount() == 1 && count == 1
	})

	payload := graph.state.WorkspacePayload()
	if payload.Version != domainworkspace.PayloadVersion {
		t.Fatalf("payload version = %d, want %d", payload.Version, domainworkspace.PayloadVersion)
	}
	if len(payload.Panes) != 1 || payload.Panes[0].Session == nil {
		t.Fatalf("payload panes = %+v", payload.Panes)
	}
	descriptor := *payload.Panes[0].Session
	if descriptor.Kind != domainworkspace.SessionPlugin ||
		descriptor.Provider != "acme.loop" ||
		descriptor.Protocol != "x-acme-loop" ||
		string(descriptor.Restore) != "restore-state" {
		t.Fatalf("persisted descriptor = %+v", descriptor)
	}

	restored := newProviderGraph(t, true)
	restored.repo.payload = payload
	restored.state.Restore(context.Background())
	waitFor(t, "the restored provider Session", func() bool {
		restored.state.Flush()
		_, count := restored.factory.snapshot()
		return count == 1
	})
	specs, _ := restored.factory.snapshot()
	spec := specs[0]
	if spec.Kind != appsession.KindPlugin ||
		spec.Protocol != "x-acme-loop" ||
		spec.Provider != "acme.loop" ||
		string(spec.Restore) != "restore-state" {
		t.Fatalf("restored StartSpec = %+v", spec)
	}
	paneID := payload.Panes[0].ID
	waitFor(t, "the restored Pane surface", func() bool {
		restored.state.Flush()
		info, ok := restored.state.PaneInfoAt(0, paneID)
		return ok && !info.Pending
	})
	if info, _ := restored.state.PaneInfoAt(0, paneID); info.Err != nil || info.Surface == nil {
		t.Fatalf("restored Pane = %+v, want a live surface", info)
	}
}

func TestRestoreKeepsAnUnavailablePlaceholderForAMissingProvider(t *testing.T) {
	root := domainworkspace.NewPaneNode("pane-plugin")
	payload := domainworkspace.Payload{
		Version:   domainworkspace.PayloadVersion,
		ActiveTab: 0,
		Tabs: []domainworkspace.RestoredTab{{
			ID: "tab-plugin", Number: 1, Root: root, FocusPath: []int{0},
		}},
		Panes: []domainworkspace.RestoredPane{restoredSessionPane("pane-plugin",
			domainworkspace.SessionDescriptor{
				Kind:     domainworkspace.SessionPlugin,
				Label:    "plugin sample",
				HostID:   "host-plugin",
				Hostname: "plugin.example.com",
				Port:     2222,
				Provider: "acme.loop",
				Protocol: "x-acme-loop",
				Restore:  []byte("restore-state"),
			})},
	}

	graph := newProviderGraph(t, false)
	graph.repo.payload = payload
	graph.state.Restore(context.Background())

	info, ok := graph.state.PaneInfoAt(0, "pane-plugin")
	if !ok || info.Surface != nil || !errors.Is(info.Err, ErrUnavailablePane) {
		t.Fatalf("missing-provider Pane = %+v, want an unavailable placeholder", info)
	}
	if info.Kind != domainworkspace.PaneKindSession {
		t.Fatalf("placeholder kind = %q", info.Kind)
	}
	if _, count := graph.factory.snapshot(); count != 0 {
		t.Fatalf("missing provider opened %d Sessions, want 0", count)
	}
	if len(panesOf(t, graph.state, 0)) != 1 {
		t.Fatal("the missing provider removed its Pane from the layout")
	}
}
