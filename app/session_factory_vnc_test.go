package app

import (
	"context"
	"strings"
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainprotocol "github.com/mslxl/natcatty/domain/model/protocol"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	shellstate "github.com/mslxl/natcatty/ui/shell/state"
)

type factoryVNC struct {
	spec  appsession.StartSpec
	calls int
}

func (f *factoryVNC) Open(_ context.Context, spec appsession.StartSpec) (appsession.Transport, error) {
	f.calls++
	f.spec = spec
	return &factoryTerminal{}, nil
}

func vncHost() domainvault.Host {
	return domainvault.Host{
		ID:       model.HostID("vnc-desk"),
		Label:    "Lab desktop",
		Protocol: domainvault.ProtocolVNC,
		Hostname: "desk.local",
		Port:     5901,
		Username: "viewer",
	}
}

// TestVNCHostResolvesThroughTheDescriptorAndFactory proves the saved Host's
// protocol descriptor selects the VNC session kind and that the app's factory
// dispatch reaches the registered VNC transport, with no protocol-name branch
// and no fallback to a local shell.
func TestVNCHostResolvesThroughTheDescriptorAndFactory(t *testing.T) {
	host := vncHost()
	if transport := host.Protocol.SessionTransport(); transport != domainprotocol.TransportVNC {
		t.Fatalf("SessionTransport = %q, want %q", transport, domainprotocol.TransportVNC)
	}
	spec := shellstate.HostSessionSpec(host)
	if spec.Kind != appsession.KindVNC {
		t.Fatalf("HostSessionSpec kind = %q, want vnc", spec.Kind)
	}
	if spec.VNC == nil || spec.VNC.Hostname != "desk.local" || spec.VNC.Port != 5901 {
		t.Fatalf("VNC start section = %+v, want the Host endpoint", spec.VNC)
	}

	local := &factoryLocal{}
	vncFactory := &factoryVNC{}
	factory := newTransportFactory(local, nil, nil, nil, nil).withVNC(vncFactory)
	transport, err := factory.Open(context.Background(), spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = transport.Close()
	if vncFactory.calls != 1 || vncFactory.spec.Kind != appsession.KindVNC {
		t.Fatalf("VNC factory calls=%d spec=%+v, want one VNC open", vncFactory.calls, vncFactory.spec)
	}
	if vncFactory.spec.VNC == nil || vncFactory.spec.VNC.Hostname != "desk.local" {
		t.Fatalf("VNC factory spec = %+v, want the endpoint", vncFactory.spec)
	}
	if local.spec.Kind != "" {
		t.Fatalf("the local factory opened a VNC Session: %+v", local.spec)
	}
}

// TestVNCWithoutAFactoryFailsClosed keeps a VNC Host from silently opening a
// local shell.
func TestVNCWithoutAFactoryFailsClosed(t *testing.T) {
	local := &factoryLocal{}
	factory := newTransportFactory(local, nil, nil, nil, nil)
	_, err := factory.Open(context.Background(), shellstate.HostSessionSpec(vncHost()))
	if err == nil || !strings.Contains(err.Error(), "no VNC transport") {
		t.Fatalf("Open = %v, want a missing VNC transport error", err)
	}
	if local.spec.Kind != "" {
		t.Fatalf("the local factory opened a VNC Session: %+v", local.spec)
	}
}
