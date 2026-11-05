// SPDX-License-Identifier: GPL-3.0-or-later

package tailscale

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// The real test is opt-in. It skips explicitly, and therefore never fakes a
// pass, unless every required value is present. Default CI runs with none of
// these variables and never touches the network.
const (
	envRealAuthKey  = "NETCATTY_TAILSCALE_AUTH_KEY"
	envRealHost     = "NETCATTY_TAILSCALE_HOST"
	envRealUser     = "NETCATTY_TAILSCALE_SSH_USER"
	envRealPassword = "NETCATTY_TAILSCALE_SSH_PASSWORD"
	envRealKeyFile  = "NETCATTY_TAILSCALE_SSH_KEY_FILE"
	envRealPort     = "NETCATTY_TAILSCALE_SSH_PORT"
	envRealControl  = "NETCATTY_TAILSCALE_CONTROL_URL"
	envRealHostname = "NETCATTY_TAILSCALE_HOSTNAME"
)

// TestRealTailnetSSH joins a real tailnet with an Auth Key, dials a peer
// through the embedded tsnet node, and authenticates an SSH session. It is
// gated on real credentials and a reachable peer; without them it skips.
func TestRealTailnetSSH(t *testing.T) {
	authKey := strings.TrimSpace(os.Getenv(envRealAuthKey))
	host := strings.TrimSpace(os.Getenv(envRealHost))
	user := strings.TrimSpace(os.Getenv(envRealUser))
	password := os.Getenv(envRealPassword)
	keyFile := strings.TrimSpace(os.Getenv(envRealKeyFile))

	for _, required := range []struct {
		name  string
		value string
	}{
		{envRealAuthKey, authKey},
		{envRealHost, host},
		{envRealUser, user},
	} {
		if required.value == "" {
			t.Skipf("%s is not set; skipping the real tailnet SSH test", required.name)
		}
	}
	if password == "" && keyFile == "" {
		t.Skipf("neither %s nor %s is set; skipping the real tailnet SSH test",
			envRealPassword, envRealKeyFile)
	}

	port := 22
	if value := strings.TrimSpace(os.Getenv(envRealPort)); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 65535 {
			t.Fatalf("%s = %q, want a TCP port", envRealPort, value)
		}
		port = parsed
	}

	var auth []ssh.AuthMethod
	if password != "" {
		auth = append(auth, ssh.Password(password))
	}
	if keyFile != "" {
		pem, err := os.ReadFile(keyFile)
		if err != nil {
			t.Fatalf("read %s: %v", envRealKeyFile, err)
		}
		signer, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			t.Fatalf("parse %s: %v", envRealKeyFile, err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}

	cfg := Config{
		ProfileID:    "real-tailnet-test",
		Label:        "Real tailnet test",
		Kind:         domainvault.NetworkKindTailscale,
		StateBaseDir: t.TempDir(),
		Hostname:     strings.TrimSpace(os.Getenv(envRealHostname)),
		ControlURL:   strings.TrimSpace(os.Getenv(envRealControl)),
		AuthKey:      authKey,
	}
	provider, err := NewProvider(cfg)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	defer func() { _ = provider.Close() }()

	startCtx, cancelStart := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelStart()
	if err := provider.Start(startCtx); err != nil {
		t.Fatalf("join the tailnet: %v", err)
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelDial()
	address := strings.TrimSpace(host) + ":" + strconv.Itoa(port)
	conn, err := provider.DialContext(dialCtx, "tcp", address)
	if err != nil {
		t.Fatalf("dial %s through the embedded node: %v", address, err)
	}
	defer func() { _ = conn.Close() }()

	// This is an opt-in integration test against a user-supplied peer. Host key
	// trust is the operator's responsibility here; production connections use
	// the Vault's Known Host verification instead.
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	})
	if err != nil {
		t.Fatalf("SSH handshake to %s: %v", address, err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("open an SSH session: %v", err)
	}
	defer func() { _ = session.Close() }()
	output, err := session.Output("echo natcatty-tailnet-ok")
	if err != nil {
		t.Fatalf("run the remote command: %v", err)
	}
	if strings.TrimSpace(string(output)) != "natcatty-tailnet-ok" {
		t.Fatalf("remote output = %q, want natcatty-tailnet-ok", output)
	}
}
