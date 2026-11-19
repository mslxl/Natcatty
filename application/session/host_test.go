package session

import (
	"strings"
	"testing"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func TestHostSpecMapsEveryProtocol(t *testing.T) {
	base := domainvault.Host{
		ID:       "host-1",
		Label:    "prod box",
		Hostname: "prod.example.com",
		Port:     2222,
		Username: "deploy",
	}

	ssh := base
	ssh.Protocol = domainvault.ProtocolSSH
	spec := HostSpec(ssh)
	if spec.Kind != KindSSH || spec.Label != "prod box" || spec.Hostname != "prod.example.com" || spec.Port != 2222 || spec.Username != "deploy" {
		t.Fatalf("SSH spec = %+v", spec)
	}
	if spec.Reconnect == nil {
		t.Error("SSH spec has no reconnect policy")
	}
	if spec.Telnet != nil || spec.Mosh != nil || spec.ET != nil || spec.VNC != nil || spec.Serial != nil {
		t.Error("SSH spec carries another transport's config")
	}

	// An empty protocol is a legacy record that NormalizeDraft made SSH.
	legacy := base
	legacy.Protocol = ""
	if got := HostSpec(legacy).Kind; got != KindSSH {
		t.Errorf("legacy protocol kind = %q, want ssh", got)
	}

	// A Host without a label falls back to its hostname, as the desktop Tab does.
	unlabelled := base
	unlabelled.Label = ""
	unlabelled.Protocol = domainvault.ProtocolSSH
	if got := HostSpec(unlabelled).Label; got != base.Hostname {
		t.Errorf("label = %q, want %q", got, base.Hostname)
	}

	// A protocol this build no longer carries fails closed: no Kind, no
	// reconnect policy, never an SSH dial.
	removed := base
	removed.Protocol = domainvault.Protocol("local")
	removedSpec := HostSpec(removed)
	if removedSpec.Kind != "" {
		t.Errorf("removed protocol kind = %q, want the zero kind", removedSpec.Kind)
	}
	if removedSpec.Reconnect != nil {
		t.Error("a removed protocol got a reconnect policy")
	}

	telnet := base
	telnet.Protocol = domainvault.ProtocolTelnet
	telnetSpec := HostSpec(telnet)
	if telnetSpec.Kind != KindTelnet || telnetSpec.Telnet == nil || telnetSpec.Telnet.Hostname != telnet.Hostname {
		t.Fatalf("telnet spec = %+v", telnetSpec)
	}
	if telnetSpec.Reconnect == nil || telnetSpec.Reconnect.ShouldRetry(End{}) {
		t.Error("telnet auto-retries a dropped socket")
	}

	mosh := base
	mosh.Protocol = domainvault.ProtocolSSH
	mosh.Transport = domainvault.TransportMosh
	mosh.MoshServerPath = "/opt/mosh/bin/mosh-server"
	moshSpec := HostSpec(mosh)
	if moshSpec.Kind != KindMosh || moshSpec.Mosh == nil || moshSpec.Mosh.ServerPath != mosh.MoshServerPath {
		t.Fatalf("mosh spec = %+v", moshSpec)
	}

	et := base
	et.Protocol = domainvault.ProtocolSSH
	et.Transport = domainvault.TransportET
	et.ETPort = 2022
	etSpec := HostSpec(et)
	if etSpec.Kind != KindET || etSpec.ET == nil || etSpec.ET.ETPort != 2022 {
		t.Fatalf("et spec = %+v", etSpec)
	}
	if !strings.HasPrefix(etSpec.ResumeKey, "et-resume-") {
		t.Errorf("et resume key = %q", etSpec.ResumeKey)
	}
	if etSpec.ResumeKey == HostSpec(et).ResumeKey {
		t.Error("two ET specs share a resume key")
	}

	vnc := base
	vnc.Protocol = domainvault.ProtocolVNC
	vncSpec := HostSpec(vnc)
	if vncSpec.Kind != KindVNC || vncSpec.VNC == nil || vncSpec.VNC.Port != vnc.Port {
		t.Fatalf("vnc spec = %+v", vncSpec)
	}
}

func TestHostSpecCarriesSSHOptionsOnly(t *testing.T) {
	host := domainvault.Host{
		ID:                     "host-options",
		Protocol:               domainvault.ProtocolSSH,
		Hostname:               "example.com",
		X11Forwarding:          true,
		X11WarningAcknowledged: true,
		DeviceType:             domainvault.DeviceTypeNetwork,
		AutoAttachTmux:         true,
	}
	spec := HostSpec(host)
	if !spec.X11Forwarding || !spec.NetworkDevice {
		t.Fatalf("SSH options missing: %+v", spec)
	}
	// Network-device mode owns the PTY and never attaches tmux.
	if spec.AutoAttachTmux {
		t.Error("network device kept tmux auto-attach")
	}

	host.DeviceType = domainvault.DeviceTypeDefault
	if !HostSpec(host).AutoAttachTmux {
		t.Error("plain SSH Host lost tmux auto-attach")
	}

	// A Mosh Host must not inherit SSH-only behavior.
	host.Transport = domainvault.TransportMosh
	mosh := HostSpec(host)
	if mosh.X11Forwarding || mosh.NetworkDevice || mosh.AutoAttachTmux {
		t.Errorf("Mosh inherited SSH options: %+v", mosh)
	}
}
