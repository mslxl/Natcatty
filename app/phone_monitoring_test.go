package app

import (
	"context"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// monitoringDialRecorder reports the dial request the Connection Manager built
// for a monitoring connection. The sampler opens transports off the UI thread,
// so the request crosses a channel instead of a shared field; the send never
// blocks a retry.
type monitoringDialRecorder struct {
	requests chan appconnection.DialRequest
}

func (r *monitoringDialRecorder) Dial(_ context.Context, req appconnection.DialRequest) (appconnection.Client, error) {
	select {
	case r.requests <- req:
	default:
	}
	return nil, errPhoneProxyRecorded
}

// TestPhoneMonitoringUsesTheSessionRoute guards the defect this test was added
// for: the monitoring connector resolved routes from the Vault alone, so an
// app-wide default Proxy Profile synced from the desktop applied to a Session
// but not to Monitoring. The runtime is built through the phone composition
// helper with a fake preferences source, Monitoring is started on a Host that
// selects no Host or Group proxy, and the dial the shared Connection Manager
// receives must carry that default.
func TestPhoneMonitoringUsesTheSessionRoute(t *testing.T) {
	vault, db, dir := newPhoneProxyTestVault(t)
	const command = "probe-monitor-default %h %p"
	savePhoneProxyProfile(t, db, "corp", command)
	host := createPhoneProxyHost(t, vault, domainvault.Draft{Label: "direct-box"})
	preferences := loadPhoneProxyPreferences(t, dir, "phone-preferences.json", "corp")

	recorder := &monitoringDialRecorder{requests: make(chan appconnection.DialRequest, 1)}
	runtime := newPhoneSessionRuntimeWithDialer(
		recorder, vault, db, dir, nil, nil, nil, nil, preferences)
	t.Cleanup(runtime.Close)

	service := runtime.MonitoringService()
	if service == nil {
		t.Fatal("MonitoringService() = nil, want the shared monitoring runtime")
	}
	if closer, ok := service.(interface{ Close() }); ok {
		t.Cleanup(closer.Close)
	}
	if err := service.Start("monitoring-route", []domainvault.Host{host}, time.Second); err != nil {
		t.Fatalf("Start monitoring: %v", err)
	}

	var request appconnection.DialRequest
	select {
	case request = <-recorder.requests:
	case <-time.After(5 * time.Second):
		t.Fatal("monitoring never opened the Host")
	}
	if request.Proxy == nil {
		t.Fatal("monitoring dialed directly; want the app-wide default Proxy Profile")
	}
	if request.Proxy.Type != appconnection.ProxyCommand || request.Proxy.Command != command {
		t.Fatalf("proxy = %+v, want the app-wide default %q", request.Proxy, command)
	}
	if request.Hostname != host.Hostname {
		t.Fatalf("dialed %q, want the monitored Host %q", request.Hostname, host.Hostname)
	}
}
