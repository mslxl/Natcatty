package state

import (
	"testing"

	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// TestHostSessionSpecCarriesVNCOnlyForVNC proves the start specification has a
// kind-selected VNC section with the endpoint, credentials, configuration and
// route, and that no other Kind populates it.
func TestHostSessionSpecCarriesVNCOnlyForVNC(t *testing.T) {
	host := domainvault.Host{
		ID:               "vnc-desk",
		Label:            "desk",
		Hostname:         "desk.local",
		Port:             5901,
		Username:         "viewer",
		Protocol:         domainvault.ProtocolVNC,
		HostChain:        &domainvault.HostChain{HostIDs: []model.HostID{"jump-1"}},
		ProxyProfileID:   "proxy-1",
		NetworkProfileID: "net-1",
		VNC: &domainvault.VNCConfig{
			DisplayMode: domainvault.VNCPort,
			FitMode:     domainvault.VNCClip,
			Shared:      true,
			CursorMode:  domainvault.VNCCursorServer,
		},
	}

	spec := HostSessionSpec(host)
	if spec.Kind != appsession.KindVNC {
		t.Fatalf("spec Kind = %q, want %q", spec.Kind, appsession.KindVNC)
	}
	if spec.VNC == nil {
		t.Fatal("VNC Host produced no VNC start section")
	}
	if spec.VNC.Hostname != "desk.local" || spec.VNC.Port != 5901 ||
		spec.VNC.Username != "viewer" {
		t.Fatalf("VNC endpoint = %+v, want the Host endpoint and username", spec.VNC)
	}
	if spec.VNC.Configuration.FitMode != domainvault.VNCClip ||
		spec.VNC.Configuration.CursorMode != domainvault.VNCCursorServer ||
		spec.VNC.Configuration.DisplayMode != domainvault.VNCPort {
		t.Fatalf("VNC configuration = %+v, want the Host's saved viewer settings", spec.VNC.Configuration)
	}
	if len(spec.VNC.Route.Jumps) != 1 || spec.VNC.Route.Jumps[0] != "jump-1" ||
		spec.VNC.Route.ProxyProfileID != "proxy-1" ||
		spec.VNC.Route.NetworkProfileID != "net-1" {
		t.Fatalf("VNC route = %+v, want the Host route references", spec.VNC.Route)
	}
	if spec.Reconnect == nil {
		t.Fatal("VNC Host has no reconnect policy")
	}

	others := []struct {
		protocol  domainvault.Protocol
		transport domainvault.Transport
	}{
		{domainvault.ProtocolSSH, domainvault.TransportPlain},
		{domainvault.ProtocolSSH, domainvault.TransportMosh},
		{domainvault.ProtocolSSH, domainvault.TransportET},
		{domainvault.ProtocolTelnet, domainvault.TransportPlain},
	}
	for _, other := range others {
		host := host
		host.Protocol = other.protocol
		host.Transport = other.transport
		host.VNC = nil
		if otherSpec := HostSessionSpec(host); otherSpec.VNC != nil {
			t.Fatalf("%s/%s spec populated the VNC section: %+v",
				other.protocol, other.transport, otherSpec.VNC)
		}
	}
}

// TestHostSessionSpecCarriesTheVNCWebSocketTransport proves a saved Host's
// WebSocket transport, upgrade path, and TLS choice reach the Session start
// section unchanged, with the endpoint staying the HTTP port.
func TestHostSessionSpecCarriesTheVNCWebSocketTransport(t *testing.T) {
	host := domainvault.Host{
		ID:       "vnc-desktop",
		Label:    "desktop",
		Hostname: "desk.local",
		Port:     domainvault.VNCDefaultWebSocketPort,
		Protocol: domainvault.ProtocolVNC,
		VNC: &domainvault.VNCConfig{
			DisplayMode:     domainvault.VNCPort,
			FitMode:         domainvault.VNCFit,
			Shared:          true,
			CursorMode:      domainvault.VNCCursorLocal,
			WebSocket:       true,
			WebSocketPath:   "/vnc?token=s3cret",
			WebSocketSecure: true,
		},
	}
	spec := HostSessionSpec(host)
	if spec.Kind != appsession.KindVNC || spec.VNC == nil {
		t.Fatalf("spec = %+v, want a VNC session", spec)
	}
	if spec.VNC.Port != domainvault.VNCDefaultWebSocketPort {
		t.Fatalf("VNC port = %d, want the HTTP port", spec.VNC.Port)
	}
	configuration := spec.VNC.Configuration
	if !configuration.WebSocket || !configuration.WebSocketSecure ||
		configuration.WebSocketPath != "/vnc?token=s3cret" ||
		configuration.DisplayMode != domainvault.VNCPort {
		t.Fatalf("VNC configuration = %+v, want the WebSocket settings", configuration)
	}
}
