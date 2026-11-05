// SPDX-License-Identifier: GPL-3.0-or-later

package et

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/et/internal/etgo/etservertest"
	"github.com/mslxl/natcatty/adapters/et/internal/etgo/protocol"
	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	etmodel "github.com/mslxl/natcatty/domain/model/et"
	xssh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"google.golang.org/protobuf/proto"
)

// routeSourceFunc adapts a function to the factory's route port.
type routeSourceFunc func(context.Context, model.HostID) (appconnection.Route, error)

func (f routeSourceFunc) Route(ctx context.Context, id model.HostID) (appconnection.Route, error) {
	return f(ctx, id)
}

// passwordMap is a per-Host saved password source, the shape the lab jump test
// needs because two endpoints use different passwords.
type passwordMap map[model.HostID]string

func (p passwordMap) Password(_ context.Context, id model.HostID) (string, error) {
	value, ok := p[id]
	if !ok {
		return "", appconnection.AuthFailure(appconnection.ErrNoCredentials)
	}
	return value, nil
}

// authPlanMap returns the shared Manager's per-Host Authentication plan. It
// stands in for the Vault-backed source so a test can select password, key,
// agent, certificate, and identity explicitly.
type authPlanMap map[model.HostID]appconnection.AuthPlan

func (p authPlanMap) AuthPlan(_ context.Context, req appconnection.Request) (appconnection.AuthPlan, error) {
	plan, ok := p[req.HostID]
	if !ok {
		return appconnection.AuthPlan{}, appconnection.AuthFailure(appconnection.ErrNoCredentials)
	}
	if plan.Username == "" {
		plan.Username = req.Username
	}
	return plan, nil
}

// knownHostsFixture is the Manager's trust store in tests.
type knownHostsFixture struct {
	mu      sync.Mutex
	records []domainconn.KnownHost
}

func (k *knownHostsFixture) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]domainconn.KnownHost(nil), k.records...), nil
}

func (k *knownHostsFixture) UpsertKnownHost(_ context.Context, _ domainconn.KnownHost) error {
	return nil
}

// trust records one fake server's host key for the logical endpoint the SSH
// client will verify.
func (k *knownHostsFixture) trust(server *fakessh.Server, hostname string, port int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.records = append(k.records, domainconn.KnownHost{
		ID:          hostname,
		Hostname:    hostname,
		Port:        port,
		KeyType:     server.KeyType(),
		PublicKey:   server.PublicKey(),
		Fingerprint: server.Fingerprint(),
	})
}

// sshFixture is one in-process SSH server and the client end that reaches it.
type sshFixture struct {
	server *fakessh.Server
	client net.Conn
}

func startSSHFixture(t *testing.T, options fakessh.Options) *sshFixture {
	t.Helper()
	clientConn, serverConn := fakessh.Pipe()
	server, err := fakessh.Start(serverConn, options)
	if err != nil {
		t.Fatalf("fakessh.Start: %v", err)
	}
	fixture := &sshFixture{server: server, client: clientConn}
	t.Cleanup(func() {
		_ = server.Close()
		_ = clientConn.Close()
	})
	return fixture
}

// etFixture is an SSH endpoint that also serves the etterminal bootstrap and
// forwards the ET socket to an in-process etserver.
type etFixture struct {
	*sshFixture
	etServer *etservertest.Server
	network  *etservertest.Network

	mu       sync.Mutex
	commands []string
	forwards []fakessh.Forward
}

func startETFixture(t *testing.T, options fakessh.Options, id, passkey string) *etFixture {
	t.Helper()
	etServer := etservertest.NewServer(id, passkey)
	network := etservertest.NewNetwork(etServer)
	fixture := &etFixture{etServer: etServer, network: network}
	options.Exec = func(command string, channel xssh.Channel) int {
		fixture.mu.Lock()
		fixture.commands = append(fixture.commands, command)
		fixture.mu.Unlock()
		_, _ = io.WriteString(channel, "IDPASSKEY:"+id+"/"+passkey+"\r\n")
		return 0
	}
	options.ForwardDial = func(host string, port int) (net.Conn, error) {
		fixture.mu.Lock()
		fixture.forwards = append(fixture.forwards, fakessh.Forward{Host: host, Port: port})
		fixture.mu.Unlock()
		return network.DialContext(context.Background(), "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	}
	fixture.sshFixture = startSSHFixture(t, options)
	t.Cleanup(network.Close)
	return fixture
}

func (f *etFixture) execCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commands)
}

func (f *etFixture) recordedForwards() []fakessh.Forward {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakessh.Forward(nil), f.forwards...)
}

// fixtureDialer is the real SSH adapter with its raw TCP dial replaced by the
// first fixture's client end. Every hop after the first is reached through the
// real adapter's direct-tcpip chain.
func fixtureDialer(first *sshFixture) *sshadapter.Dialer {
	return sshadapter.NewDialer(
		sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			return first.client, nil
		}),
		sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
	)
}

func newRouteManager(t *testing.T, dialer appconnection.Dialer, passwords appconnection.PasswordSource,
	known appconnection.KnownHostRepository, opts ...appconnection.Option) *appconnection.Manager {

	t.Helper()
	manager := appconnection.NewManager(dialer, passwords, known, opts...)
	t.Cleanup(manager.CloseAll)
	return manager
}

// respondToInitialPayload answers the protocol-6 start handshake. The fake
// server deliberately does not answer on its own, exactly like openFake.
func respondToInitialPayload(t *testing.T, server *etservertest.Server) {
	t.Helper()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		packet, err := server.Recv(ctx)
		if err != nil {
			return
		}
		if packet.Header != protocol.HeaderInitialPayload {
			t.Errorf("first ET packet = %v, want INITIAL_PAYLOAD", packet.Header)
		}
		encoded, err := proto.Marshal(&protocol.InitialResponse{})
		if err != nil {
			t.Errorf("marshal INITIAL_RESPONSE: %v", err)
			return
		}
		_ = server.Send(context.Background(), protocol.Packet{
			Header: protocol.HeaderInitialResponse, Payload: encoded,
		})
	}()
}

func routeSpec(hostname string, port int, username string) appsession.StartSpec {
	cfg := etmodel.Config{
		Hostname: hostname,
		Port:     port,
		Username: username,
		ETPort:   etmodel.DefaultETPort,
		Cols:     100,
		Rows:     30,
	}.Normalized()
	return appsession.StartSpec{
		Kind:     appsession.KindET,
		HostID:   "target",
		Hostname: hostname,
		Port:     port,
		Username: username,
		Cols:     100,
		Rows:     30,
		ET:       &cfg,
	}
}

func openETThroughManager(t *testing.T, manager *appconnection.Manager, route appconnection.Route,
	spec appsession.StartSpec) appsession.Transport {

	t.Helper()
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return route, nil
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	transport, err := factory.Open(ctx, spec)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	return transport
}

// expectETOutput sends one terminal buffer and reads it back through the
// Session transport, proving the ET session runs over the resolved route.
func expectETOutput(t *testing.T, transport appsession.Transport, server *etservertest.Server, want string) {
	t.Helper()
	err := server.Send(context.Background(), protocol.Packet{
		Header: protocol.HeaderTerminalBuffer,
		Payload: mustMarshal(t, &protocol.TerminalBuffer{}, func(m *protocol.TerminalBuffer) {
			m.SetBuffer([]byte(want))
		}),
	})
	if err != nil {
		t.Fatalf("send terminal output: %v", err)
	}
	readTransport(t, transport, want)
}

// startHTTPConnectProxy runs a minimal HTTP CONNECT proxy. target opens the
// upstream connection for one accepted client, so the same helper serves an
// in-process fake or a real container.
func startHTTPConnectProxy(t *testing.T,
	target func() (net.Conn, error)) (host string, port int, requests <-chan string) {

	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	received := make(chan string, 1)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				reader := bufio.NewReader(conn)
				var builder strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					builder.WriteString(line)
					if line == "\r\n" {
						break
					}
					if builder.Len() > 64<<10 {
						return
					}
				}
				select {
				case received <- builder.String():
				default:
				}
				upstream, err := target()
				if err != nil {
					return
				}
				defer upstream.Close()
				if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
					return
				}
				done := make(chan struct{}, 2)
				go func() {
					_, _ = io.Copy(upstream, reader)
					done <- struct{}{}
				}()
				go func() {
					_, _ = io.Copy(conn, upstream)
					done <- struct{}{}
				}()
				<-done
			}(conn)
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return address.IP.String(), address.Port, received
}

func waitProxyRequest(t *testing.T, requests <-chan string) string {
	t.Helper()
	select {
	case request := <-requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("the proxy received no CONNECT request")
		return ""
	}
}

func testPrivateKey(t *testing.T) (xssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := xssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatalf("key signer: %v", err)
	}
	block, err := xssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return signer, string(pem.EncodeToMemory(block))
}

// testAgentDial serves an in-memory OpenSSH agent over net.Pipe. The agent
// protocol is the real one; only the key storage is software.
func testAgentDial(t *testing.T, private ed25519.PrivateKey) func(context.Context) (net.Conn, error) {
	t.Helper()
	keyring := agent.NewKeyring()
	if err := keyring.Add(agent.AddedKey{PrivateKey: private, Comment: "ticket-26"}); err != nil {
		t.Fatalf("add agent key: %v", err)
	}
	return func(ctx context.Context) (net.Conn, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			_ = agent.ServeAgent(keyring, server)
		}()
		return client, nil
	}
}

func TestInProcessETConnectsThroughAProxy(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	proxyHost, proxyPort, requests := startHTTPConnectProxy(t, func() (net.Conn, error) {
		return target.client, nil
	})
	known := &knownHostsFixture{}
	known.trust(target.server, "target.internal", 2222)
	manager := newRouteManager(t, sshadapter.NewDialer(sshadapter.WithTimeouts(5*time.Second, 5*time.Second)),
		passwordMap{"target": "target-password"}, known)
	respondToInitialPayload(t, target.etServer)

	route := appconnection.Route{Proxy: &appconnection.Proxy{
		Type: appconnection.ProxyHTTP, Host: proxyHost, Port: proxyPort,
		Username: "proxy-user", Password: "proxy-password",
	}}
	transport := openETThroughManager(t, manager, route, routeSpec("target.internal", 2222, "target-user"))

	request := waitProxyRequest(t, requests)
	if first := strings.SplitN(request, "\r\n", 2)[0]; first != "CONNECT target.internal:2222 HTTP/1.1" {
		t.Fatalf("proxy request line = %q", first)
	}
	credentials := base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
	if !strings.Contains(request, "Proxy-Authorization: Basic "+credentials+"\r\n") {
		t.Fatalf("proxy credentials missing from %q", request)
	}
	if username, password := target.server.LastCredentials(); username != "target-user" || password != "target-password" {
		t.Fatalf("target credentials = %q/%q, want the shared saved password", username, password)
	}
	if target.execCount() != 1 {
		t.Fatalf("bootstrap exec calls = %d, want 1", target.execCount())
	}
	forwards := target.recordedForwards()
	if len(forwards) != 1 || forwards[0].Host != etServerHost || forwards[0].Port != etmodel.DefaultETPort {
		t.Fatalf("ET forwards = %+v, want the direct-tcpip route to etserver", forwards)
	}
	expectETOutput(t, transport, target.etServer, "et-through-a-proxy")
}

func TestInProcessETConnectsThroughAMultiHopJumpChain(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	middle := startSSHFixture(t, fakessh.Options{
		Password: "middle-password",
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	outer := startSSHFixture(t, fakessh.Options{
		Password: "outer-password",
		ForwardDial: func(string, int) (net.Conn, error) {
			return middle.client, nil
		},
	})
	known := &knownHostsFixture{}
	known.trust(outer.server, "outer.internal", 22)
	known.trust(middle.server, "middle.internal", 22)
	known.trust(target.server, "target.internal", 2222)
	manager := newRouteManager(t, fixtureDialer(outer), passwordMap{
		"outer":  "outer-password",
		"middle": "middle-password",
		"target": "target-password",
	}, known)
	respondToInitialPayload(t, target.etServer)

	route := appconnection.Route{Jumps: []appconnection.Hop{
		{HostID: "outer", Label: "outer", Hostname: "outer.internal", Port: 22, Username: "outer-user"},
		{HostID: "middle", Label: "middle", Hostname: "middle.internal", Port: 22, Username: "middle-user"},
	}}
	transport := openETThroughManager(t, manager, route, routeSpec("target.internal", 2222, "target-user"))

	if username, password := outer.server.LastCredentials(); username != "outer-user" || password != "outer-password" {
		t.Fatalf("outer credentials = %q/%q", username, password)
	}
	if username, password := middle.server.LastCredentials(); username != "middle-user" || password != "middle-password" {
		t.Fatalf("middle credentials = %q/%q", username, password)
	}
	if username, password := target.server.LastCredentials(); username != "target-user" || password != "target-password" {
		t.Fatalf("target credentials = %q/%q", username, password)
	}
	outerForwards := outer.server.Forwards()
	if len(outerForwards) != 1 || outerForwards[0].Host != "middle.internal" || outerForwards[0].Port != 22 {
		t.Fatalf("outer forwards = %+v", outerForwards)
	}
	middleForwards := middle.server.Forwards()
	if len(middleForwards) != 1 || middleForwards[0].Host != "target.internal" || middleForwards[0].Port != 2222 {
		t.Fatalf("middle forwards = %+v", middleForwards)
	}
	forwards := target.recordedForwards()
	if len(forwards) != 1 || forwards[0].Host != etServerHost || forwards[0].Port != etmodel.DefaultETPort {
		t.Fatalf("ET forwards = %+v", forwards)
	}
	expectETOutput(t, transport, target.etServer, "et-through-a-chain")
}

func TestInProcessETAuthenticatesWithEachSharedMethod(t *testing.T) {
	t.Run("password", func(t *testing.T) {
		target := startETFixture(t, fakessh.Options{Password: "saved-password"}, testID, testPasskey)
		known := &knownHostsFixture{}
		known.trust(target.server, "target.internal", 2222)
		manager := newRouteManager(t, fixtureDialer(target.sshFixture),
			passwordMap{"target": "saved-password"}, known)
		respondToInitialPayload(t, target.etServer)

		transport := openETThroughManager(t, manager, appconnection.Route{},
			routeSpec("target.internal", 2222, "password-user"))
		if username, password := target.server.LastCredentials(); username != "password-user" || password != "saved-password" {
			t.Fatalf("credentials = %q/%q", username, password)
		}
		expectETOutput(t, transport, target.etServer, "password-auth")
	})

	t.Run("selected key and identity", func(t *testing.T) {
		signer, keyPEM := testPrivateKey(t)
		target := startETFixture(t, fakessh.Options{PublicKeys: []xssh.PublicKey{signer.PublicKey()}}, testID, testPasskey)
		known := &knownHostsFixture{}
		known.trust(target.server, "target.internal", 2222)
		plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
			Username: "key-user",
			Mode:     appconnection.AuthModeKey,
			SelectedKey: &appconnection.KeyMaterial{
				ID: "selected", Label: "work", PrivateKey: keyPEM,
			},
		})
		manager := newRouteManager(t, fixtureDialer(target.sshFixture), nil, known,
			appconnection.WithAuthSource(authPlanMap{"target": plan}))
		respondToInitialPayload(t, target.etServer)

		transport := openETThroughManager(t, manager, appconnection.Route{},
			routeSpec("target.internal", 2222, "fallback-user"))
		if attempts := target.server.PublicKeyAttempts(); attempts == 0 {
			t.Fatal("the target saw no public-key attempt")
		}
		if got := target.server.LastPublicKeyUsername(); got != "key-user" {
			t.Fatalf("public-key username = %q, want the identity selected in the Vault plan", got)
		}
		if attempts := target.server.PasswordAttempts(); attempts != 0 {
			t.Fatalf("password attempts = %d, want key-only authentication", attempts)
		}
		expectETOutput(t, transport, target.etServer, "key-auth")
	})

	t.Run("agent", func(t *testing.T) {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate agent key: %v", err)
		}
		agentKey, err := xssh.NewPublicKey(public)
		if err != nil {
			t.Fatalf("agent public key: %v", err)
		}
		target := startETFixture(t, fakessh.Options{PublicKeys: []xssh.PublicKey{agentKey}}, testID, testPasskey)
		known := &knownHostsFixture{}
		known.trust(target.server, "target.internal", 2222)
		dialer := sshadapter.NewDialer(
			sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
				return target.client, nil
			}),
			sshadapter.WithAgentDial(testAgentDial(t, private)),
			sshadapter.WithTimeouts(5*time.Second, 5*time.Second),
		)
		plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
			Username: "agent-user", Mode: appconnection.AuthModeAuto, UseAgent: true,
		})
		manager := newRouteManager(t, dialer, nil, known,
			appconnection.WithAuthSource(authPlanMap{"target": plan}))
		respondToInitialPayload(t, target.etServer)

		transport := openETThroughManager(t, manager, appconnection.Route{},
			routeSpec("target.internal", 2222, "fallback-user"))
		if attempts := target.server.PublicKeyAttempts(); attempts == 0 {
			t.Fatal("the target saw no agent public-key attempt")
		}
		if got := target.server.LastPublicKeyUsername(); got != "agent-user" {
			t.Fatalf("agent username = %q, want agent-user", got)
		}
		expectETOutput(t, transport, target.etServer, "agent-auth")
	})

	t.Run("certificate", func(t *testing.T) {
		_, caPrivate, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate CA: %v", err)
		}
		caSigner, err := xssh.NewSignerFromKey(caPrivate)
		if err != nil {
			t.Fatalf("CA signer: %v", err)
		}
		_, userPrivate, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate user key: %v", err)
		}
		userSigner, err := xssh.NewSignerFromKey(userPrivate)
		if err != nil {
			t.Fatalf("user signer: %v", err)
		}
		certificate := &xssh.Certificate{
			Key:             userSigner.PublicKey(),
			CertType:        xssh.UserCert,
			KeyId:           "ticket-26",
			ValidPrincipals: []string{"cert-user"},
			ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
			ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
		}
		if err := certificate.SignCert(rand.Reader, caSigner); err != nil {
			t.Fatalf("sign certificate: %v", err)
		}
		block, err := xssh.MarshalPrivateKey(userPrivate, "")
		if err != nil {
			t.Fatalf("marshal user key: %v", err)
		}
		target := startETFixture(t, fakessh.Options{
			CertificateAuthority: caSigner.PublicKey(),
		}, testID, testPasskey)
		known := &knownHostsFixture{}
		known.trust(target.server, "target.internal", 2222)
		plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
			Username: "cert-user",
			Mode:     appconnection.AuthModeCertificate,
			SelectedKey: &appconnection.KeyMaterial{
				ID: "selected", Label: "cert",
				PrivateKey:  string(pem.EncodeToMemory(block)),
				Certificate: string(xssh.MarshalAuthorizedKey(certificate)),
			},
		})
		manager := newRouteManager(t, fixtureDialer(target.sshFixture), nil, known,
			appconnection.WithAuthSource(authPlanMap{"target": plan}))
		respondToInitialPayload(t, target.etServer)

		transport := openETThroughManager(t, manager, appconnection.Route{},
			routeSpec("target.internal", 2222, "fallback-user"))
		keyType, keyID := target.server.LastPublicKey()
		if !strings.Contains(keyType, "-cert-") {
			t.Fatalf("offered key type = %q, want a certificate", keyType)
		}
		if keyID != "ticket-26" {
			t.Fatalf("certificate KeyId = %q", keyID)
		}
		if got := target.server.LastPublicKeyUsername(); got != "cert-user" {
			t.Fatalf("certificate username = %q, want cert-user", got)
		}
		expectETOutput(t, transport, target.etServer, "certificate-auth")
	})
}

func TestInProcessETRejectsAnUntrustedHostKey(t *testing.T) {
	cases := []struct {
		name    string
		records func(*fakessh.Server) []domainconn.KnownHost
	}{
		{"unknown key", func(*fakessh.Server) []domainconn.KnownHost { return nil }},
		{"changed key", func(server *fakessh.Server) []domainconn.KnownHost {
			return []domainconn.KnownHost{{
				Hostname: "target.internal", Port: 2222, KeyType: server.KeyType(),
				Fingerprint: domainconn.Fingerprint([]byte("an older key")),
			}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
			known := &knownHostsFixture{records: tc.records(target.server)}
			manager := newRouteManager(t, fixtureDialer(target.sshFixture),
				passwordMap{"target": "target-password"}, known)
			factory := InProcessFactory{
				Manager: manager,
				Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
					return appconnection.Route{}, nil
				}),
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := factory.Open(ctx, routeSpec("target.internal", 2222, "target-user"))
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != appconnection.KindHostKey {
				t.Fatalf("Open error = %v, want a typed host-key rejection", err)
			}
			if !errors.Is(err, appconnection.ErrHostKeyRejected) {
				t.Fatalf("Open error = %v, want ErrHostKeyRejected", err)
			}
			if target.execCount() != 0 || len(target.recordedForwards()) != 0 {
				t.Fatal("a rejected host key still started the ET bootstrap or forwarding")
			}
		})
	}
}

func TestInProcessETRejectsAnUntrustedJumpHostKey(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	middle := startSSHFixture(t, fakessh.Options{
		Password: "middle-password",
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	outer := startSSHFixture(t, fakessh.Options{
		Password: "outer-password",
		ForwardDial: func(string, int) (net.Conn, error) {
			return middle.client, nil
		},
	})
	// The jump chain's outer host is deliberately not trusted.
	known := &knownHostsFixture{}
	known.trust(middle.server, "middle.internal", 22)
	known.trust(target.server, "target.internal", 2222)
	manager := newRouteManager(t, fixtureDialer(outer), passwordMap{
		"outer": "outer-password", "middle": "middle-password", "target": "target-password",
	}, known)
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return appconnection.Route{Jumps: []appconnection.Hop{
				{HostID: "outer", Hostname: "outer.internal", Port: 22, Username: "outer-user"},
				{HostID: "middle", Hostname: "middle.internal", Port: 22, Username: "middle-user"},
			}}, nil
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, routeSpec("target.internal", 2222, "target-user"))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindHostKey {
		t.Fatalf("Open error = %v, want a typed host-key rejection", err)
	}
	if typed.Hop == nil || typed.Hop.Index != 1 {
		t.Fatalf("hop = %+v, want the outer jump to be named", typed.Hop)
	}
	if target.execCount() != 0 || len(target.recordedForwards()) != 0 {
		t.Fatal("a rejected jump host key still started the ET bootstrap or forwarding")
	}
}

func TestInProcessETReportsAnAuthenticationFailure(t *testing.T) {
	target := startETFixture(t, fakessh.Options{Password: "right-password"}, testID, testPasskey)
	known := &knownHostsFixture{}
	known.trust(target.server, "target.internal", 2222)
	manager := newRouteManager(t, fixtureDialer(target.sshFixture),
		passwordMap{"target": "wrong-password"}, known)
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return appconnection.Route{}, nil
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := factory.Open(ctx, routeSpec("target.internal", 2222, "target-user"))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth {
		t.Fatalf("Open error = %v, want a typed auth failure", err)
	}
	if target.execCount() != 0 || len(target.recordedForwards()) != 0 {
		t.Fatal("a refused login still started the ET bootstrap or forwarding")
	}
}

func TestInProcessETReportsATimeout(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	dialer := sshadapter.NewDialer(
		sshadapter.WithDialFunc(func(context.Context, string, string) (net.Conn, error) {
			return client, nil
		}),
		sshadapter.WithTimeouts(100*time.Millisecond, 150*time.Millisecond),
	)
	known := &knownHostsFixture{}
	manager := newRouteManager(t, dialer, passwordMap{"target": "target-password"}, known)
	factory := InProcessFactory{
		Manager: manager,
		Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
			return appconnection.Route{}, nil
		}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := factory.Open(ctx, routeSpec("target.internal", 2222, "target-user"))
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindTimeout {
		t.Fatalf("Open error = %v, want a typed timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Open took %v; the SSH auth timeout did not bound it", elapsed)
	}
}

func TestInProcessETPassesTheSharedRouteAndPolicies(t *testing.T) {
	route := appconnection.Route{
		Jumps: []appconnection.Hop{{
			HostID: "jump", Label: "bastion", Hostname: "jump.internal", Port: 22,
			Username: "jumper", ConfigFingerprint: "jump-digest",
			Algorithms: appconnection.AlgorithmPolicy{Legacy: true},
		}},
		Proxy: &appconnection.Proxy{
			Type: appconnection.ProxySOCKS5, Host: "proxy.internal", Port: 1080,
			Username: "proxy-user", Password: "proxy-password",
		},
		Network: &appconnection.NetworkRoute{
			ProfileID: "net-target", Kind: "tailscale", Label: "Office", Fingerprint: "net-fp",
		},
		Algorithms: appconnection.AlgorithmPolicy{
			Legacy: true, SkipECDSAHostKey: true, Ciphers: []string{"aes256-ctr"},
		},
		ConfigFingerprint: "target-digest",
	}
	cases := []struct {
		name     string
		username string
		wantUser string
	}{
		{"spec username", "spec-user", "spec-user"},
		{"config fallback", "", "config-user"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got appconnection.Request
			lease := newFakeLease(&memoryExec{data: []byte("no IDPASSKEY marker")}, nil)
			factory := InProcessFactory{
				Routes: routeSourceFunc(func(context.Context, model.HostID) (appconnection.Route, error) {
					return route, nil
				}),
				Acquire: func(_ context.Context, request appconnection.Request) (ConnectionLease, error) {
					got = request
					return lease, nil
				},
			}
			cfg := etmodel.Config{
				Hostname: "target.internal", Port: 2222, Username: "config-user", ETPort: 2022,
			}.Normalized()
			spec := appsession.StartSpec{
				Kind: appsession.KindET, HostID: "target", Hostname: "target.internal",
				Port: 2222, Username: tc.username, ET: &cfg,
			}
			if _, err := factory.Open(context.Background(), spec); !errors.Is(err, etmodel.ErrBootstrapFailed) {
				t.Fatalf("Open error = %v, want the expected bootstrap failure", err)
			}
			if got.HostID != "target" || got.Hostname != "target.internal" || got.Port != 2222 {
				t.Fatalf("request endpoint = %+v", got)
			}
			if got.Username != tc.wantUser {
				t.Fatalf("request username = %q, want %q", got.Username, tc.wantUser)
			}
			if !reflect.DeepEqual(got.Jumps, route.Jumps) {
				t.Fatalf("request jumps = %+v, want %+v", got.Jumps, route.Jumps)
			}
			if !reflect.DeepEqual(got.Proxy, route.Proxy) {
				t.Fatalf("request proxy = %+v, want %+v", got.Proxy, route.Proxy)
			}
			if !reflect.DeepEqual(got.Network, route.Network) {
				t.Fatalf("request network = %+v, want %+v", got.Network, route.Network)
			}
			if !reflect.DeepEqual(got.Algorithms, route.Algorithms) {
				t.Fatalf("request algorithms = %+v, want %+v", got.Algorithms, route.Algorithms)
			}
			if got.ConfigFingerprint != route.ConfigFingerprint {
				t.Fatalf("request fingerprint = %q", got.ConfigFingerprint)
			}
			if got.LeaseKind != appconnection.LeaseShell {
				t.Fatalf("lease kind = %q, want a shell lease", got.LeaseKind)
			}
			if got.Keepalive == nil || *got.Keepalive != appconnection.DefaultKeepalive {
				t.Fatalf("keepalive = %+v, want the shared terminal policy", got.Keepalive)
			}
		})
	}
}

// TestInProcessETCreatesNoSSHArtifacts proves the in-process path never builds
// the bundled client's private HOME, generated ssh_config, known-hosts
// snapshot, key file, or askpass helper.
func TestInProcessETCreatesNoSSHArtifacts(t *testing.T) {
	tempRoot := t.TempDir()
	home := t.TempDir()
	t.Setenv("TMPDIR", tempRoot)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SSH_ASKPASS", "")
	t.Setenv("SSH_ASKPASS_REQUIRE", "")
	t.Setenv("NETCATTY_ET_ASKPASS_MAP", "")

	// Run the bootstrap and forwarding through the real Connection Manager and
	// SSH adapter: those are the only components that could touch HOME, an
	// ssh_config, known_hosts, a key file, or askpass.
	target := startETFixture(t, fakessh.Options{Password: "target-password"}, testID, testPasskey)
	known := &knownHostsFixture{}
	known.trust(target.server, "target.internal", 2222)
	manager := newRouteManager(t, fixtureDialer(target.sshFixture),
		passwordMap{"target": "target-password"}, known)
	respondToInitialPayload(t, target.etServer)
	transport := openETThroughManager(t, manager, appconnection.Route{},
		routeSpec("target.internal", 2222, "target-user"))
	if err := transport.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if target.execCount() != 1 {
		t.Fatalf("bootstrap exec calls = %d, want 1", target.execCount())
	}

	if entries, err := os.ReadDir(tempRoot); err != nil {
		t.Fatalf("read temp root: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("the in-process ET path created temporary artifacts: %v", entries)
	}
	if entries, err := os.ReadDir(home); err != nil {
		t.Fatalf("read home: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("the in-process ET path wrote into HOME: %v", entries)
	}
	for _, name := range []string{"SSH_ASKPASS", "SSH_ASKPASS_REQUIRE", "NETCATTY_ET_ASKPASS_MAP"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s = %q, want no askpass machinery", name, value)
		}
	}
	if got := os.Getenv("HOME"); got != home {
		t.Fatalf("HOME = %q, want the user's unchanged home %q", got, home)
	}
}
