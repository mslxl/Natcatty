package ssh

import (
	"context"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

// TestRusshBannerReachesSingleChannelPlanningThroughHandshake proves the
// classification survives the real x/crypto/ssh handshake and the shared
// Connection Manager. The fixture advertises itself as russh; the lease handed
// to the session layer must carry that banner and report the one-channel
// constraint, so SCP fallback avoids opening a second session channel.
func TestRusshBannerReachesSingleChannelPlanningThroughHandshake(t *testing.T) {
	const banner = "SSH-2.0-russh_0.40.0"
	_, dialFunc := startFixture(t, fakessh.Options{
		Password:      "pw",
		ServerVersion: banner,
	})
	dialer := NewDialer(WithDialFunc(dialFunc), WithTimeouts(2*time.Second, 2*time.Second))
	manager := appconnection.NewManager(dialer, forwardPasswords{}, forwardKnownHosts{},
		appconnection.WithPrompter(forwardPrompter{}),
		appconnection.WithIdleTTL(10*time.Second),
	)
	t.Cleanup(manager.CloseAll)

	lease, err := manager.Acquire(context.Background(), appconnection.Request{
		HostID:   "russh-host",
		Hostname: "russh.example.com",
		Port:     22,
		Username: "tester",
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	if got := lease.RemoteVersion(); got != banner {
		t.Fatalf("RemoteVersion = %q, want %q", got, banner)
	}
	if !lease.SingleChannel() {
		t.Fatal("a russh connection was not reported as single-channel")
	}
}
