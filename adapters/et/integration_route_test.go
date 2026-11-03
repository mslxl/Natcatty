// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
)

// TestInProcessThroughARealProxy runs the in-process ET bootstrap and ET
// handshake through a loopback HTTP CONNECT proxy to a stock etserver. The
// proxy is local test infrastructure; both SSH ends are the real lab.
func TestInProcessThroughARealProxy(t *testing.T) {
	requireLab(t)
	server := labServers()[0]
	proxyHost, proxyPort, requests := startHTTPConnectProxy(t, func() (net.Conn, error) {
		return net.DialTimeout("tcp", server.address, 5*time.Second)
	})
	manager := labManager(t, server.password)
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return appconnection.Route{Proxy: &appconnection.Proxy{
				Type: appconnection.ProxyHTTP, Host: proxyHost, Port: proxyPort,
			}}, nil
		}),
	}
	transport := openLab(t, factory, labSpec(t, server))
	if first := strings.SplitN(waitProxyRequest(t, requests), "\r\n", 2)[0]; first != "CONNECT "+server.address+" HTTP/1.1" {
		t.Fatalf("proxy request line = %q", first)
	}

	output := newOutputCollector(transport)
	writeCommand(t, transport, `printf 'ET_PROXY_%s\n' "$((7*6))"`)
	output.waitFor(t, "ET_PROXY_42", 20*time.Second)
}

// labSpecAt builds an ET session spec for an arbitrary SSH endpoint. The jump
// test needs the target's container-reachable address rather than the
// host-published lab address.
func labSpecAt(t *testing.T, hostID model.HostID, address, username string) appsession.StartSpec {
	t.Helper()
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("%s address: %v", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("%s address: %v", address, err)
	}
	cfg := etmodel.Config{
		Hostname: host, Port: port, Username: username,
		ETPort: etmodel.DefaultETPort, SessionID: "lab-" + string(hostID), Cols: 100, Rows: 30,
	}.Normalized()
	return appsession.StartSpec{
		Kind: appsession.KindET, HostID: hostID, Hostname: host, Port: port,
		Username: username, Cols: 100, Rows: 30, ET: &cfg,
	}
}

// TestInProcessThroughARealJumpHost runs the in-process ET bootstrap across a
// real sshd jump Host. Set NATCATTY_ET_LAB_JUMP=host:port to enable it. When
// the jump is a container that cannot reach the host-published lab port, set
// NATCATTY_ET_LAB_JUMP_TARGET to the target's container-reachable
// host:port. The jump defaults to the lab account and the target's password.
func TestInProcessThroughARealJumpHost(t *testing.T) {
	requireLab(t)
	jumpAddress := strings.TrimSpace(os.Getenv("NATCATTY_ET_LAB_JUMP"))
	if jumpAddress == "" {
		t.Skip("set NATCATTY_ET_LAB_JUMP=host:port with a running sshd jump host")
	}
	jumpHost, jumpPortText, err := net.SplitHostPort(jumpAddress)
	if err != nil {
		t.Fatalf("NATCATTY_ET_LAB_JUMP: %v", err)
	}
	jumpPort, err := strconv.Atoi(jumpPortText)
	if err != nil {
		t.Fatalf("NATCATTY_ET_LAB_JUMP: %v", err)
	}
	server := labServers()[0]
	targetAddress := envOr("NATCATTY_ET_LAB_JUMP_TARGET", server.address)
	jumpUser := envOr("NATCATTY_ET_LAB_JUMP_USER", server.username)
	jumpPassword := envOr("NATCATTY_ET_LAB_JUMP_PASSWORD", server.password)
	targetID := model.HostID("lab-" + server.name)
	passwords := passwordMap{
		model.HostID("jump"): jumpPassword,
		targetID:             server.password,
	}
	manager := appconnection.NewManager(
		sshadapter.NewDialer(), passwords, labKnownHosts{},
		appconnection.WithPrompter(labTrust{}),
	)
	t.Cleanup(manager.CloseAll)
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return appconnection.Route{Jumps: []appconnection.Hop{{
				HostID: "jump", Label: "real jump", Hostname: jumpHost, Port: jumpPort, Username: jumpUser,
			}}}, nil
		}),
	}
	transport := openLab(t, factory, labSpecAt(t, targetID, targetAddress, server.username))

	output := newOutputCollector(transport)
	writeCommand(t, transport, `printf 'ET_JUMP_%s\n' "$((6*7))"`)
	output.waitFor(t, "ET_JUMP_42", 20*time.Second)
}

// TestInProcessThroughRealKeyAuthentication runs the in-process ET path with
// key authentication against a real sshd. Set NATCATTY_ET_LAB_KEY to an
// unencrypted private key file and NATCATTY_ET_LAB_KEY_TARGET=host:port to a
// stock et-test container whose tester account authorizes that key.
func TestInProcessThroughRealKeyAuthentication(t *testing.T) {
	requireLab(t)
	keyPath := strings.TrimSpace(os.Getenv("NATCATTY_ET_LAB_KEY"))
	targetAddress := strings.TrimSpace(os.Getenv("NATCATTY_ET_LAB_KEY_TARGET"))
	if keyPath == "" || targetAddress == "" {
		t.Skip("set NATCATTY_ET_LAB_KEY and NATCATTY_ET_LAB_KEY_TARGET for real key authentication")
	}
	privateKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read NATCATTY_ET_LAB_KEY: %v", err)
	}
	username := envOr("NATCATTY_ET_LAB_KEY_USER", "tester")
	spec := labSpecAt(t, "lab-key", targetAddress, username)
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: username, Mode: appconnection.AuthModeKey,
		SelectedKey: &appconnection.KeyMaterial{
			ID: "lab-key", Label: "lab key", PrivateKey: string(privateKey),
		},
	})
	manager := appconnection.NewManager(
		sshadapter.NewDialer(), nil, labKnownHosts{},
		appconnection.WithPrompter(labTrust{}),
		appconnection.WithAuthSource(authPlanMap{"lab-key": plan}),
	)
	t.Cleanup(manager.CloseAll)
	transport := openLab(t, InProcessFactory{Manager: manager}, spec)

	output := newOutputCollector(transport)
	writeCommand(t, transport, `printf 'ET_KEY_%s\n' "$((6*7))"`)
	output.waitFor(t, "ET_KEY_42", 20*time.Second)
}
