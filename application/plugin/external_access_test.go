package plugin_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// externalFixture wires every external-access port with a deterministic
// memory implementation. The authority, audit log, and clock are the real
// production types, so denials and lease expiry are exercised end to end.
type externalFixture struct {
	host      *appplugin.HostServices
	session   *appplugin.ServiceSession
	authority *appplugin.Authority
	audit     *recordingAudit
	ref       appplugin.PluginRef
	fiber     domainplugin.FiberID
	now       *time.Time
	network   *appplugin.MemoryNetworkBroker
	pins      *appplugin.MemoryTrustPinStore
	confirmer *appplugin.MemoryTrustConfirmer
	auth      *appplugin.MemoryAuthBroker
	vault     *appplugin.MemoryVaultFields
	hosts     *appplugin.MemoryHostCatalog
	handles   *appplugin.HandleTable
}

func newExternalFixture(t *testing.T, mutate func(*appplugin.ServiceDeps),
	hosts ...appplugin.HostSummary) *externalFixture {
	t.Helper()
	now := time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	audit := &recordingAudit{}
	authority := appplugin.NewAuthority()
	ref := appplugin.PluginRef{
		ID:      "acme.external",
		Name:    "Acme External",
		Version: "1.0.0",
		Source:  domainplugin.SourceLocalFile,
	}
	fiber := domainplugin.NewFiberID("installed", ref.ID, 1)
	network := &appplugin.MemoryNetworkBroker{Addresses: map[string][]netip.Addr{}}
	pins := &appplugin.MemoryTrustPinStore{}
	confirmer := &appplugin.MemoryTrustConfirmer{}
	auth := &appplugin.MemoryAuthBroker{}
	vaultFields := appplugin.NewMemoryVaultFields()
	hostsCatalog := appplugin.NewMemoryHostCatalog(hosts...)

	limits := appplugin.DefaultServiceLimits()
	limits.NetworkDialTimeout = 2 * time.Second
	limits.NetworkIdleTimeout = time.Minute
	deps := appplugin.ServiceDeps{
		Authority:      authority,
		Audit:          audit,
		Now:            func() time.Time { return now },
		Hosts:          hostsCatalog,
		Network:        network,
		TrustPins:      pins,
		TrustConfirmer: confirmer,
		Auth:           auth,
		Vault:          vaultFields,
		Limits:         limits,
	}
	if mutate != nil {
		mutate(&deps)
	}
	host := appplugin.NewHostServices(deps)
	handles := appplugin.NewHandleTable(appplugin.MaxHandles)
	session := host.Open(ref, fiber, nil)
	session.BindHandles(handles)
	t.Cleanup(func() { host.CloseAll() })
	return &externalFixture{
		host: host, session: session, authority: authority, audit: audit,
		ref: ref, fiber: fiber, now: &now, network: network, pins: pins,
		confirmer: confirmer, auth: auth, vault: vaultFields, hosts: hostsCatalog,
		handles: handles,
	}
}

func (f *externalFixture) grant(permission domainplugin.Permission,
	scope domainplugin.Scope) {
	f.authority.Grant(appplugin.SubjectForRef(f.ref), domainplugin.PermissionGrant{
		Permission: permission, Scope: scope, GrantedAt: *f.now,
	})
}

func (f *externalFixture) grantNetwork(host string, port int, protocol,
	tls string) {
	f.grantNetworkPorts(host, []int{port}, protocol, tls)
}

func (f *externalFixture) grantNetworkPorts(host string, ports []int, protocol,
	tls string) {
	f.grant(domainplugin.PermissionNetworkDial, domainplugin.Scope{
		Kind: domainplugin.ScopeNetwork, Host: host,
		Ports: append([]int(nil), ports...), Protocol: protocol, TLS: tls,
	})
}

func (f *externalFixture) grantVault(permission domainplugin.Permission,
	host, field string) {
	f.grant(permission, domainplugin.Scope{
		Kind: domainplugin.ScopeVault, Host: host, Field: field,
	})
}

func (f *externalFixture) reasons() []string {
	var out []string
	for _, event := range f.audit.snapshot() {
		out = append(out, event.Reason)
	}
	return out
}

func (f *externalFixture) hasReason(reason string) bool {
	for _, got := range f.reasons() {
		if got == reason {
			return true
		}
	}
	return false
}

func (f *externalFixture) auditJSON(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(f.audit.snapshot())
	if err != nil {
		t.Fatalf("marshal audit events: %v", err)
	}
	return string(data)
}

// testSocket is a deterministic ExternalSocket. Reads return the seeded bytes
// once, then EOF; half-close and close are recorded.
type testSocket struct {
	mu          sync.Mutex
	readData    []byte
	readOffset  int
	writes      []byte
	closed      bool
	writeClosed bool
	readClosed  bool
	deadline    time.Time
}

func newTestSocket(readData string) *testSocket {
	return &testSocket{readData: []byte(readData)}
}

func (s *testSocket) Read(buffer []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOffset >= len(s.readData) {
		return 0, io.EOF
	}
	n := copy(buffer, s.readData[s.readOffset:])
	s.readOffset += n
	return n, nil
}

func (s *testSocket) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.writeClosed {
		return 0, errors.New("socket is closed")
	}
	s.writes = append(s.writes, data...)
	return len(data), nil
}

func (s *testSocket) SetDeadline(t time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = t
	return nil
}

func (s *testSocket) CloseWrite() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeClosed = true
	return nil
}

func (s *testSocket) CloseRead() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readClosed = true
	return nil
}

func (s *testSocket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *testSocket) state() (closed, writeClosed, readClosed bool, writes string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed, s.writeClosed, s.readClosed, string(s.writes)
}

func TestNetworkDialRequiresTheExactEndpointScope(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	socket := newTestSocket("")
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return socket, nil
	}
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTLS, "verify")

	// An unqualified TLS dial defaults to host verification and matches the
	// grant that names verify.
	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTLS,
	})
	if err != nil {
		t.Fatalf("dial with exact tls scope: %v", err)
	}
	if result.Handle == 0 || result.TLS != domainplugin.TLSPolicyVerify {
		t.Fatalf("dial result = %+v", result)
	}
	if len(fixture.network.Dials()) != 1 {
		t.Fatalf("dial count = %d, want 1", len(fixture.network.Dials()))
	}

	denied := []appplugin.NetworkDialSpec{
		{Host: "db.internal", Port: 5433, Protocol: domainplugin.NetworkProtocolTLS},
		{Host: "other.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTLS},
		{Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP},
		{Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTLS, TLS: domainplugin.TLSPolicyNone},
		{Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTLS, TLS: domainplugin.TLSPolicySystem},
	}
	for _, spec := range denied {
		_, err := fixture.session.NetworkDial(context.Background(), spec)
		if err == nil {
			t.Fatalf("dial %+v was not denied", spec)
		}
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
			t.Fatalf("dial %+v code = %q, want denied", spec, code)
		}
		if !appplugin.IsDenied(err) {
			t.Fatalf("dial %+v error is not a typed denial: %v", spec, err)
		}
	}
	if len(fixture.network.Dials()) != 1 {
		t.Fatalf("a denied dial reached the transport: %d dials", len(fixture.network.Dials()))
	}
	if !fixture.hasReason("network.dial:denied") {
		t.Fatalf("denials were not audited: %v", fixture.reasons())
	}

	_, err = fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: "udp",
	})
	if err == nil || serviceErrorCode(t, err) != appplugin.ServiceCodeInvalid {
		t.Fatalf("UDP dial error = %v, want invalid", err)
	}
}

func TestNetworkDialPinsTheResolvedAddressAgainstRebinding(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{
		netip.MustParseAddr("10.0.0.7"),
		netip.MustParseAddr("10.0.0.8"),
	}
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTCP, "")

	dialTargets := 0
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		dialTargets++
		if dialTargets == 1 {
			// A second DNS answer would now point elsewhere. The service must
			// not consult it: the pinned address is already chosen.
			fixture.network.Addresses["db.internal"] = []netip.Addr{
				netip.MustParseAddr("192.0.2.66"),
			}
			return nil, errors.New("first address refused")
		}
		return newTestSocket(""), nil
	}

	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if fixture.network.ResolveCalls() != 1 {
		t.Fatalf("resolve calls = %d, want exactly 1", fixture.network.ResolveCalls())
	}
	dials := fixture.network.Dials()
	if len(dials) != 2 {
		t.Fatalf("dial attempts = %d, want 2", len(dials))
	}
	if dials[0].Address != netip.MustParseAddrPort("10.0.0.7:5432") ||
		dials[1].Address != netip.MustParseAddrPort("10.0.0.8:5432") {
		t.Fatalf("dial addresses = %v, %v; the resolved set was not pinned", dials[0].Address, dials[1].Address)
	}
	for _, dial := range dials {
		if dial.Host != "db.internal" {
			t.Fatalf("dial host = %q, want the granted name for SNI", dial.Host)
		}
	}
	if result.Resolved != "10.0.0.8" {
		t.Fatalf("resolved = %q, want the pinned second address", result.Resolved)
	}

	// A literal IP is pinned without a resolver call.
	fixture.grantNetwork("10.9.9.9", 443, domainplugin.NetworkProtocolTCP, "")
	if _, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.9.9.9", Port: 443, Protocol: domainplugin.NetworkProtocolTCP,
	}); err != nil {
		t.Fatalf("literal dial: %v", err)
	}
	if fixture.network.ResolveCalls() != 1 {
		t.Fatalf("literal IP was resolved: %d calls", fixture.network.ResolveCalls())
	}
}

func TestNetworkTLSUpgradeIsHostOwnedAndSingleUse(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return newTestSocket(""), nil
	}
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTCP, "")

	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if fixture.network.Dials()[0].TLS.Mode != "" {
		t.Fatalf("plaintext dial carried TLS policy %q", fixture.network.Dials()[0].TLS.Mode)
	}

	upgraded, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle)
	if err != nil {
		t.Fatalf("tls upgrade: %v", err)
	}
	if upgraded.Mode != domainplugin.TLSPolicyVerify || upgraded.ServerName != "db.internal" {
		t.Fatalf("upgrade result = %+v, want host-owned verify with SNI", upgraded)
	}
	policies := fixture.network.Upgrades()
	if len(policies) != 1 {
		t.Fatalf("upgrade count = %d, want 1", len(policies))
	}
	if policies[0].Mode != domainplugin.TLSPolicyVerify || policies[0].ServerName != "db.internal" {
		t.Fatalf("upgrade policy = %+v", policies[0])
	}

	if _, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle); err == nil {
		t.Fatal("second in-band upgrade was allowed")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeConflict {
		t.Fatalf("second upgrade code = %q, want conflict", code)
	}

	// A connection dialed as TLS is already secured; it cannot upgrade again.
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTLS, "verify")
	secure, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTLS,
	})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	if fixture.network.Dials()[1].TLS.Mode != domainplugin.TLSPolicyVerify {
		t.Fatalf("tls dial policy = %q, want verify", fixture.network.Dials()[1].TLS.Mode)
	}
	if _, err := fixture.session.NetworkTLSUpgrade(context.Background(), secure.Handle); err == nil {
		t.Fatal("upgrade of an already-secured connection was allowed")
	}
}

func TestNetworkViaHostUsesTheHostSSHStack(t *testing.T) {
	jump := appplugin.HostSummary{
		ID: "jump", Label: "Jump", Hostname: "jump.example", Port: 22,
		Protocol: "ssh", Username: "ops",
	}
	web := appplugin.HostSummary{
		ID: "web", Label: "Web", Hostname: "web.example", Port: 23,
		Protocol: "telnet",
	}
	fixture := newExternalFixture(t, nil, jump, web)
	fixture.network.Addresses["10.0.0.5"] = []netip.Addr{netip.MustParseAddr("10.0.0.5")}
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return newTestSocket(""), nil
	}
	fixture.grantNetwork("10.0.0.5", 5432, domainplugin.NetworkProtocolTCP, "")
	fixture.grant(domainplugin.PermissionHostsRead, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "jump",
	})

	if _, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.5", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
		ViaHost: "jump",
	}); err != nil {
		t.Fatalf("dial via host: %v", err)
	}
	if len(fixture.network.ViaDials()) != 1 || len(fixture.network.Dials()) != 0 {
		t.Fatalf("via dials = %d, direct dials = %d",
			len(fixture.network.ViaDials()), len(fixture.network.Dials()))
	}
	via := fixture.network.ViaDials()[0]
	if via.ViaHost != "jump" || via.Address != netip.MustParseAddrPort("10.0.0.5:5432") {
		t.Fatalf("via target = %+v", via)
	}

	// A non-SSH Host can never tunnel.
	if _, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.5", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
		ViaHost: "web",
	}); err == nil || serviceErrorCode(t, err) != appplugin.ServiceCodeInvalid {
		t.Fatalf("non-ssh via_host error = %v, want invalid", err)
	}

	// The Host must still be readable under hosts.read.
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref), domainplugin.PermissionHostsRead)
	if _, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.5", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
		ViaHost: "jump",
	}); err == nil || serviceErrorCode(t, err) != appplugin.ServiceCodeDenied {
		t.Fatalf("via_host without hosts.read error = %v, want denied", err)
	}
}

func TestNetworkCreditsHalfCloseAndClose(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["echo.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.3")}
	socket := newTestSocket("pong")
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return socket, nil
	}
	fixture.grantNetwork("echo.internal", 7, domainplugin.NetworkProtocolTCP, "")

	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "echo.internal", Port: 7, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	read, err := fixture.session.NetworkRead(context.Background(), result.Handle, 4)
	if err != nil || string(read.Data) != "pong" || read.EOF {
		t.Fatalf("read = %+v, %v", read, err)
	}

	if _, err := fixture.session.NetworkRead(context.Background(), result.Handle,
		fixture.host.Limits().NetworkChunkBytes+1); err == nil ||
		serviceErrorCode(t, err) != appplugin.ServiceCodeLimit {
		t.Fatalf("oversized read error = %v, want limit", err)
	}
	if _, err := fixture.session.NetworkWrite(context.Background(), result.Handle,
		[]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, _, writes := socket.state(); writes != "ping" {
		t.Fatalf("socket writes = %q", writes)
	}

	if err := fixture.session.NetworkShutdown(result.Handle, "write"); err != nil {
		t.Fatalf("shutdown write: %v", err)
	}
	if err := fixture.session.NetworkShutdown(result.Handle, "read"); err != nil {
		t.Fatalf("shutdown read: %v", err)
	}
	closed, writeClosed, readClosed, _ := socket.state()
	if closed || !writeClosed || !readClosed {
		t.Fatalf("half-close state closed=%v write=%v read=%v", closed, writeClosed, readClosed)
	}
	if err := fixture.session.NetworkShutdown(result.Handle, "sideways"); err == nil {
		t.Fatal("invalid shutdown direction was accepted")
	}
	// A half-closed read reports EOF without another socket read.
	if after, err := fixture.session.NetworkRead(context.Background(), result.Handle, 4); err != nil || !after.EOF {
		t.Fatalf("read after read half-close = %+v, %v; want EOF", after, err)
	}

	if err := fixture.session.NetworkClose(result.Handle); err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed, _, _, _ := socket.state(); !closed {
		t.Fatal("socket was not closed")
	}
	if err := fixture.session.NetworkClose(result.Handle); err != nil {
		t.Fatalf("close is not idempotent: %v", err)
	}
	if _, err := fixture.session.NetworkRead(context.Background(), result.Handle, 1); err == nil {
		t.Fatal("read after close succeeded")
	}
}

func TestAuthFlowIsHostOwnedAndNeverExposesTheAnswer(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.grant(domainplugin.PermissionAuthInvoke, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})

	challenge, err := fixture.session.AuthBegin(context.Background(), "db",
		appplugin.AuthMethodPassword)
	if err != nil {
		t.Fatalf("auth begin: %v", err)
	}
	if challenge.ID == "" || challenge.HostID != "db" ||
		challenge.Method != appplugin.AuthMethodPassword {
		t.Fatalf("challenge = %+v", challenge)
	}
	begins := fixture.auth.Begins()
	if len(begins) != 1 || begins[0].HostID != "db" ||
		begins[0].Method != appplugin.AuthMethodPassword {
		t.Fatalf("broker begins = %+v", begins)
	}

	const answer = "opaque-response-42"
	result, err := fixture.session.AuthRespond(context.Background(), challenge.ID, answer)
	if err != nil {
		t.Fatalf("auth respond: %v", err)
	}
	if result.Status != appplugin.AuthStatusSucceeded {
		t.Fatalf("status = %q, want succeeded", result.Status)
	}
	answers := fixture.auth.Answers()
	if len(answers) != 1 || answers[0] != answer {
		t.Fatalf("broker answers = %q", answers)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(encoded), answer) {
		t.Fatalf("auth result leaked the guest answer: %s", encoded)
	}
	if _, err := fixture.session.AuthRespond(context.Background(), challenge.ID, "again"); err == nil {
		t.Fatal("respond succeeded after the flow completed")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeNotFound {
		t.Fatalf("respond after completion code = %q, want not-found", code)
	}

	// Audit names the method and status only.
	if !fixture.hasReason("auth.begin:ok") || !fixture.hasReason("auth.respond:succeeded") {
		t.Fatalf("auth audit reasons = %v", fixture.reasons())
	}
	if strings.Contains(fixture.auditJSON(t), answer) {
		t.Fatal("the authentication answer was written to audit")
	}

	// Cancel reaches the broker and removes the flow.
	second, err := fixture.session.AuthBegin(context.Background(), "db",
		appplugin.AuthMethodPassword)
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
	if err := fixture.session.AuthCancel(context.Background(), second.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	cancels := fixture.auth.Cancels()
	if len(cancels) != 1 || cancels[0] != second.ID {
		t.Fatalf("broker cancels = %v", cancels)
	}
	if !fixture.hasReason("auth.cancel:ok") {
		t.Fatalf("cancel was not audited: %v", fixture.reasons())
	}

	// Revocation cancels a live flow in the host broker.
	third, err := fixture.session.AuthBegin(context.Background(), "db",
		appplugin.AuthMethodPassword)
	if err != nil {
		t.Fatalf("third begin: %v", err)
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionAuthInvoke)
	if len(fixture.auth.Cancels()) != 2 || fixture.auth.Cancels()[1] != third.ID {
		t.Fatalf("revocation did not cancel the challenge: %v", fixture.auth.Cancels())
	}
}

func sshKeyMaterial(fill byte) ([]byte, string) {
	const keyType = "ssh-ed25519"
	blob := make([]byte, 4+len(keyType)+16)
	binary.BigEndian.PutUint32(blob, uint32(len(keyType)))
	copy(blob[4:], keyType)
	for i := 4 + len(keyType); i < len(blob); i++ {
		blob[i] = fill
	}
	return []byte(base64.StdEncoding.EncodeToString(blob)), domainconn.Fingerprint(blob)
}

func selfSignedCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "db.internal"},
		NotBefore:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:     time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template,
		&key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestTrustVerificationConfirmsUnknownAndBlocksChanged(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.grant(domainplugin.PermissionTrustVerify, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	materialA, fingerprintA := sshKeyMaterial(0x11)
	materialB, fingerprintB := sshKeyMaterial(0x22)

	// Unknown material asks the host user, and a confirmation pins it.
	fixture.confirmer.Decision = true
	first, err := fixture.session.TrustVerify(context.Background(), "db",
		appplugin.TrustKindSSHHostKey, materialA)
	if err != nil {
		t.Fatalf("verify unknown: %v", err)
	}
	if first.Verdict != appplugin.TrustVerdictTrusted ||
		first.Fingerprint != fingerprintA || first.KeyType != "ssh-ed25519" {
		t.Fatalf("unknown result = %+v", first)
	}
	if len(fixture.confirmer.Confirmed()) != 1 {
		t.Fatalf("host confirmation count = %d", len(fixture.confirmer.Confirmed()))
	}
	if len(fixture.pins.Records()) != 1 {
		t.Fatalf("confirmed pin was not saved: %+v", fixture.pins.Records())
	}

	// A known-good fingerprint is trusted without asking again.
	again, err := fixture.session.TrustVerify(context.Background(), "db",
		appplugin.TrustKindSSHHostKey, materialA)
	if err != nil || again.Verdict != appplugin.TrustVerdictTrusted {
		t.Fatalf("repeat verify = %+v, %v", again, err)
	}
	if len(fixture.confirmer.Confirmed()) != 1 {
		t.Fatal("a pinned key asked the user again")
	}

	// Changed material blocks even when the confirmer would accept it. The
	// plugin cannot override the verdict.
	changed, err := fixture.session.TrustVerify(context.Background(), "db",
		appplugin.TrustKindSSHHostKey, materialB)
	if err != nil {
		t.Fatalf("verify changed: %v", err)
	}
	if changed.Verdict != appplugin.TrustVerdictChanged {
		t.Fatalf("changed verdict = %q, want changed", changed.Verdict)
	}
	if len(fixture.confirmer.Confirmed()) != 1 {
		t.Fatal("changed material was sent to the confirmer")
	}

	// A host revocation is final.
	if err := fixture.pins.Save(context.Background(), appplugin.TrustRecord{
		HostID: "db", Kind: appplugin.TrustKindSSHHostKey,
		KeyType: "ssh-ed25519", Fingerprint: fingerprintB,
	}); err != nil {
		t.Fatalf("save revocable pin: %v", err)
	}
	fixture.pins.Revoke("db", appplugin.TrustKindSSHHostKey, fingerprintB)
	revoked, err := fixture.session.TrustVerify(context.Background(), "db",
		appplugin.TrustKindSSHHostKey, materialB)
	if err != nil || revoked.Verdict != appplugin.TrustVerdictRevoked {
		t.Fatalf("revoked verify = %+v, %v", revoked, err)
	}
	if len(fixture.confirmer.Confirmed()) != 1 {
		t.Fatal("revoked material was sent to the confirmer")
	}

	// Unknown material with a rejecting host stays unknown.
	fixture.confirmer.Decision = false
	rejected, err := fixture.session.TrustVerify(context.Background(), "db",
		appplugin.TrustKindCustomFingerprint, []byte("ABC-123"))
	if err != nil {
		t.Fatalf("verify custom unknown: %v", err)
	}
	if rejected.Verdict != appplugin.TrustVerdictUnknown ||
		rejected.Fingerprint != "abc-123" {
		t.Fatalf("rejected custom verdict = %+v", rejected)
	}

	// TLS certificates use the same host-owned decision path.
	certificate := selfSignedCertificatePEM(t)
	tlsResult, err := fixture.session.TrustVerify(context.Background(), "db",
		appplugin.TrustKindTLSCert, certificate)
	if err != nil {
		t.Fatalf("verify tls cert: %v", err)
	}
	if tlsResult.Verdict != appplugin.TrustVerdictUnknown ||
		!strings.HasPrefix(tlsResult.Fingerprint, "sha256:") {
		t.Fatalf("tls result = %+v", tlsResult)
	}

	// The audit names verdicts, never material or fingerprints.
	encoded := fixture.auditJSON(t)
	for _, fragment := range []string{fingerprintA, fingerprintB,
		string(materialA), string(materialB)} {
		if strings.Contains(encoded, fragment) {
			t.Fatal("trust material or fingerprint reached the audit log")
		}
	}
	for _, verdict := range []string{"trust.verify:trusted", "trust.verify:changed",
		"trust.verify:revoked", "trust.verify:unknown"} {
		if !fixture.hasReason(verdict) {
			t.Fatalf("missing audit reason %q: %v", verdict, fixture.reasons())
		}
	}
}

func TestVaultMetadataReturnsOnlyGrantedFields(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.vault.DescriptorsByHost["db"] = []appplugin.VaultFieldDescriptor{
		{HostID: "db", Field: "password", Label: "Password", Kind: "password", GuestDeliverable: true},
		{HostID: "db", Field: "passphrase", Label: "Passphrase", Kind: "passphrase", HostExecuted: true},
		{HostID: "other", Field: "password", Label: "Other", Kind: "password", GuestDeliverable: true},
	}
	fixture.grantVault(domainplugin.PermissionVaultMetadata, "db", "password")

	descriptors, err := fixture.session.VaultMetadata(context.Background(), "db")
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if len(descriptors) != 1 || descriptors[0].Field != "password" {
		t.Fatalf("metadata = %+v, want only the granted field", descriptors)
	}
	if _, err := fixture.session.VaultFields(context.Background(), "db", "password"); err != nil {
		t.Fatalf("fields password: %v", err)
	}
	if _, err := fixture.session.VaultFields(context.Background(), "db", "passphrase"); err == nil {
		t.Fatal("fields returned an ungranted descriptor")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("ungranted field code = %q, want denied", code)
	}
	if _, err := fixture.session.VaultMetadata(context.Background(), "other"); err == nil {
		t.Fatal("metadata for an ungranted Host was allowed")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("ungranted metadata code = %q, want denied", code)
	}
	if !fixture.hasReason("vault.metadata:ok") ||
		!fixture.hasReason("vault.metadata:denied") {
		t.Fatalf("metadata audit reasons = %v", fixture.reasons())
	}
}

func TestVaultLeaseTTLConnectionCloseAndRevocation(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.vault.DescriptorsByHost["db"] = []appplugin.VaultFieldDescriptor{
		{HostID: "db", Field: "password", Kind: "password", GuestDeliverable: true},
		{HostID: "db", Field: "private_key", Kind: "key", HostExecuted: true},
	}
	fixture.vault.Values["db/password"] = "s3cret-value"

	// A host-executed field is denied and its value is never fetched.
	fixture.grantVault(domainplugin.PermissionVaultSecrets, "db", "private_key")
	if _, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"private_key", 0, 0); err == nil {
		t.Fatal("host-executed field was leased")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("host-executed code = %q, want denied", code)
	}
	for _, call := range fixture.vault.ValueCalls() {
		if strings.Contains(call, "private_key") {
			t.Fatalf("host-executed value was fetched: %v", fixture.vault.ValueCalls())
		}
	}

	fixture.grantVault(domainplugin.PermissionVaultSecrets, "db", "password")
	lease, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, 0)
	if err != nil {
		t.Fatalf("lease issue: %v", err)
	}
	expires := lease.ExpiresAt.Sub(*fixture.now)
	if expires != appplugin.DefaultSecretLeaseTTL {
		t.Fatalf("default ttl = %v, want %v", expires, appplugin.DefaultSecretLeaseTTL)
	}
	value, err := fixture.session.VaultLeaseRead(context.Background(), lease.Handle)
	if err != nil || string(value.Value) != "s3cret-value" {
		t.Fatalf("lease read = %q, %v", value.Value, err)
	}
	if strings.Contains(fixture.auditJSON(t), "s3cret-value") {
		t.Fatal("the leased secret was written to audit")
	}
	if !fixture.hasReason("vault.lease_issue:ok") ||
		!fixture.hasReason("vault.lease_read:ok") {
		t.Fatalf("lease audit reasons = %v", fixture.reasons())
	}

	// A TTL above the cap is clamped, and expiry invalidates the handle.
	capped, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 10*time.Minute, 0)
	if err != nil {
		t.Fatalf("capped lease: %v", err)
	}
	if capped.ExpiresAt.Sub(*fixture.now) != appplugin.DefaultSecretLeaseTTL {
		t.Fatalf("ttl was not capped: %v", capped.ExpiresAt.Sub(*fixture.now))
	}
	*fixture.now = fixture.now.Add(appplugin.DefaultSecretLeaseTTL + time.Second)
	if _, err := fixture.session.VaultLeaseRead(context.Background(), capped.Handle); err == nil {
		t.Fatal("expired lease was readable")
	}
	if _, err := fixture.session.VaultLeaseRead(context.Background(), lease.Handle); err == nil {
		t.Fatal("expired lease was readable")
	}

	// Connection close drops a bound lease even before its TTL.
	fixture.network.Addresses["10.0.0.9"] = []netip.Addr{netip.MustParseAddr("10.0.0.9")}
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return newTestSocket(""), nil
	}
	fixture.grantNetwork("10.0.0.9", 5432, domainplugin.NetworkProtocolTCP, "")
	connection, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.9", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial for lease binding: %v", err)
	}
	bound, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, connection.Handle)
	if err != nil {
		t.Fatalf("bound lease: %v", err)
	}
	if err := fixture.session.NetworkClose(connection.Handle); err != nil {
		t.Fatalf("close connection: %v", err)
	}
	if _, err := fixture.session.VaultLeaseRead(context.Background(), bound.Handle); err == nil {
		t.Fatal("connection-bound lease survived close")
	}

	// A stale connection handle cannot bind a lease.
	if _, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, connection.Handle); err == nil {
		t.Fatal("lease bound to a closed connection")
	}

	// Revocation invalidates every live lease.
	live, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, 0)
	if err != nil {
		t.Fatalf("lease before revocation: %v", err)
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionVaultSecrets)
	if _, err := fixture.session.VaultLeaseRead(context.Background(), live.Handle); err == nil {
		t.Fatal("lease survived revocation")
	}
}

func TestVaultLeaseQuotaAndFiberBinding(t *testing.T) {
	limits := appplugin.DefaultServiceLimits()
	limits.SecretLeases = 1
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.Limits = limits
	})
	fixture.vault.DescriptorsByHost["db"] = []appplugin.VaultFieldDescriptor{
		{HostID: "db", Field: "password", Kind: "password", GuestDeliverable: true},
	}
	fixture.vault.Values["db/password"] = "one"
	fixture.grantVault(domainplugin.PermissionVaultSecrets, "db", "password")

	lease, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, 0)
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if _, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, 0); err == nil {
		t.Fatal("lease quota was not enforced")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("quota code = %q, want limit", code)
	}
	if err := fixture.session.VaultLeaseRelease(context.Background(), lease.Handle); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := fixture.session.VaultLeaseRead(context.Background(), lease.Handle); err == nil {
		t.Fatal("released lease was readable")
	}
	if _, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, 0); err != nil {
		t.Fatalf("lease after release: %v", err)
	}

	// A lease belongs to the Fiber that issued it. Another Fiber of the same
	// plugin has its own handle table and cannot read it.
	otherFiber := domainplugin.NewFiberID("installed", fixture.ref.ID, 2)
	other := fixture.host.Open(fixture.ref, otherFiber, nil)
	other.BindHandles(appplugin.NewHandleTable(appplugin.MaxHandles))
	t.Cleanup(func() { fixture.host.Close(otherFiber) })
	if _, err := other.VaultLeaseRead(context.Background(), lease.Handle); err == nil {
		t.Fatal("a lease was readable from another Fiber")
	}
}

func TestExternalConnectionQuotaIsReservedBeforeDialing(t *testing.T) {
	limits := appplugin.DefaultServiceLimits()
	limits.NetworkConnections = 2
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.Limits = limits
	})
	fixture.network.Addresses["svc.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.2")}
	fixture.grantNetwork("svc.internal", 9000, domainplugin.NetworkProtocolTCP, "")
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		entered <- struct{}{}
		<-release
		return newTestSocket(""), nil
	}

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
				Host: "svc.internal", Port: 9000, Protocol: domainplugin.NetworkProtocolTCP,
			})
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("dials did not reach the transport")
		}
	}
	if _, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "svc.internal", Port: 9000, Protocol: domainplugin.NetworkProtocolTCP,
	}); err == nil {
		t.Fatal("dial beyond the connection quota succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("quota code = %q, want limit", code)
	}
	close(release)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent dial: %v", err)
		}
	}
}

func TestNetworkDialAcceptsAnyGrantedPort(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return newTestSocket(""), nil
	}
	// One grant may name several ports on the same endpoint. Every dialed port
	// must still be one of them; there are no ranges or wildcards.
	fixture.grant(domainplugin.PermissionNetworkDial, domainplugin.Scope{
		Kind: domainplugin.ScopeNetwork, Host: "db.internal", Ports: []int{5432, 5433},
		Protocol: domainplugin.NetworkProtocolTCP,
	})
	for _, port := range []int{5432, 5433} {
		result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
			Host: "db.internal", Port: port, Protocol: domainplugin.NetworkProtocolTCP,
		})
		if err != nil {
			t.Fatalf("dial %d: %v", port, err)
		}
		if result.Handle == 0 {
			t.Fatalf("dial %d returned no handle", port)
		}
	}
	if len(fixture.network.Dials()) != 2 {
		t.Fatalf("dial count = %d, want 2", len(fixture.network.Dials()))
	}
	if _, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5434, Protocol: domainplugin.NetworkProtocolTCP,
	}); err == nil || serviceErrorCode(t, err) != appplugin.ServiceCodeDenied {
		t.Fatalf("dial outside the granted ports error = %v, want denied", err)
	}
}

func TestFailedTLSUpgradeFailsTheConnectionClosed(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	socket := newTestSocket("")
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return socket, nil
	}
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTCP, "")
	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	fixture.network.UpgradeErr = appplugin.ErrExternalTrustRejected
	if _, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle); err == nil {
		t.Fatal("failed upgrade was reported as success")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("failed upgrade code = %q, want denied", code)
	}
	if closed, _, _, _ := socket.state(); !closed {
		t.Fatal("a failed in-band upgrade left the socket open")
	}

	// The stream state after a failed handshake is unknown, so the connection
	// fails closed: no plaintext read and no second attempt.
	if _, err := fixture.session.NetworkRead(context.Background(), result.Handle, 1); err == nil {
		t.Fatal("read succeeded after a failed TLS upgrade")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("read after failed upgrade code = %q, want invalid", code)
	}
	if _, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle); err == nil {
		t.Fatal("upgrade retry succeeded after a failed handshake")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("retry code = %q, want invalid", code)
	}
	if !fixture.hasReason("network.tls_upgrade:denied") {
		t.Fatalf("failed upgrade was not audited: %v", fixture.reasons())
	}
}

func TestInvalidNetworkHandlesAreAudited(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	ctx := context.Background()
	const bad = appplugin.Handle(0xdead)
	calls := []struct {
		reason string
		call   func() error
	}{
		{"network.read:invalid", func() error {
			_, err := fixture.session.NetworkRead(ctx, bad, 1)
			return err
		}},
		{"network.write:invalid", func() error {
			_, err := fixture.session.NetworkWrite(ctx, bad, []byte("x"))
			return err
		}},
		{"network.shutdown:invalid", func() error {
			return fixture.session.NetworkShutdown(bad, "write")
		}},
		{"network.tls_upgrade:invalid", func() error {
			_, err := fixture.session.NetworkTLSUpgrade(ctx, bad)
			return err
		}},
	}
	for _, entry := range calls {
		err := entry.call()
		if err == nil {
			t.Fatalf("%s was accepted", entry.reason)
		}
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
			t.Fatalf("%s code = %q, want invalid", entry.reason, code)
		}
		if !fixture.hasReason(entry.reason) {
			t.Fatalf("%s was not audited: %v", entry.reason, fixture.reasons())
		}
	}
}

func TestVaultLeaseBoundToAnIdleConnectionExpires(t *testing.T) {
	limits := appplugin.DefaultServiceLimits()
	limits.NetworkIdleTimeout = 30 * time.Second
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.Limits = limits
	})
	fixture.vault.DescriptorsByHost["db"] = []appplugin.VaultFieldDescriptor{
		{HostID: "db", Field: "password", Kind: "password", GuestDeliverable: true},
	}
	fixture.vault.Values["db/password"] = "idle-secret"
	fixture.grantVault(domainplugin.PermissionVaultSecrets, "db", "password")
	fixture.network.Addresses["10.0.0.9"] = []netip.Addr{netip.MustParseAddr("10.0.0.9")}
	socket := newTestSocket("")
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return socket, nil
	}
	fixture.grantNetwork("10.0.0.9", 5432, domainplugin.NetworkProtocolTCP, "")

	connection, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.9", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	lease, err := fixture.session.VaultLeaseIssue(context.Background(), "db",
		"password", 0, connection.Handle)
	if err != nil {
		t.Fatalf("bound lease: %v", err)
	}
	if _, err := fixture.session.VaultLeaseRead(context.Background(), lease.Handle); err != nil {
		t.Fatalf("lease before idle expiry: %v", err)
	}

	*fixture.now = fixture.now.Add(limits.NetworkIdleTimeout + time.Second)
	if _, err := fixture.session.VaultLeaseRead(context.Background(), lease.Handle); err == nil {
		t.Fatal("lease bound to an idle connection was readable")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("idle lease code = %q, want invalid", code)
	}
	if closed, _, _, _ := socket.state(); !closed {
		t.Fatal("the idle connection was not closed")
	}
	// The expiry path destroys the lease, so the handle stays dead even after
	// the clock is moved back.
	*fixture.now = fixture.now.Add(-2 * time.Second)
	if _, err := fixture.session.VaultLeaseRead(context.Background(), lease.Handle); err == nil {
		t.Fatal("expired lease handle came back to life")
	}
}

func TestAuthChallengeQuotaReservesConcurrentBegins(t *testing.T) {
	limits := appplugin.DefaultServiceLimits()
	limits.AuthChallenges = 2
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.Limits = limits
	})
	fixture.grant(domainplugin.PermissionAuthInvoke, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})

	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var sequence atomic.Int64
	fixture.auth.BeginFunc = func(request appplugin.AuthBeginRequest) (appplugin.AuthChallenge, error) {
		id := sequence.Add(1)
		entered <- struct{}{}
		<-release
		return appplugin.AuthChallenge{
			ID:     fmt.Sprintf("challenge-%d", id),
			HostID: request.HostID, Method: request.Method, CanRespond: true,
		}, nil
	}

	results := make(chan appplugin.AuthChallenge, 2)
	var wait sync.WaitGroup
	for i := 0; i < 2; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			challenge, err := fixture.session.AuthBegin(context.Background(), "db",
				appplugin.AuthMethodPassword)
			if err != nil {
				t.Errorf("concurrent auth begin: %v", err)
			}
			results <- challenge
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("auth begins did not reach the broker")
		}
	}

	// Both broker calls are still in flight and already hold their slots, so a
	// third begin is denied without reaching the host.
	if _, err := fixture.session.AuthBegin(context.Background(), "db",
		appplugin.AuthMethodPassword); err == nil {
		t.Fatal("begin beyond the challenge quota succeeded")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeLimit {
		t.Fatalf("quota code = %q, want limit", code)
	}
	if begins := fixture.auth.Begins(); len(begins) != 2 {
		t.Fatalf("denied begin reached the host broker: %d begins", len(begins))
	}

	close(release)
	wait.Wait()
	close(results)
	var live []appplugin.AuthChallenge
	for challenge := range results {
		live = append(live, challenge)
	}
	if len(live) != 2 {
		t.Fatalf("live challenges = %d, want 2", len(live))
	}

	// Cancelling one flow frees its slot even though the other is still live.
	if err := fixture.session.AuthCancel(context.Background(), live[0].ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := fixture.session.AuthBegin(context.Background(), "db",
		appplugin.AuthMethodPassword); err != nil {
		t.Fatalf("begin after a cancel: %v", err)
	}
	if !fixture.hasReason("auth.begin:limit") {
		t.Fatalf("quota denial was not audited: %v", fixture.reasons())
	}
}

func TestTrustRevocationDuringConfirmationPersistsNothing(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.grant(domainplugin.PermissionTrustVerify, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.confirmer.ConfirmFn = func(appplugin.TrustRequest) (bool, error) {
		close(entered)
		<-release
		return true, nil
	}
	material, _ := sshKeyMaterial(0x55)

	type outcome struct {
		result appplugin.TrustVerifyResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fixture.session.TrustVerify(context.Background(), "db",
			appplugin.TrustKindSSHHostKey, material)
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirmation did not reach the host")
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionTrustVerify)
	close(release)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("TrustVerify did not return after the confirmation")
	}
	if got.err == nil {
		t.Fatalf("trust verification succeeded after revocation: %+v", got.result)
	}
	if code := serviceErrorCode(t, got.err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked verification code = %q, want invalid", code)
	}
	var revoked *appplugin.CapabilityRevokedError
	if !errors.As(got.err, &revoked) {
		t.Fatalf("revoked verification error = %v, want CapabilityRevokedError", got.err)
	}
	if got.result.Verdict == appplugin.TrustVerdictTrusted {
		t.Fatalf("revoked verification reported a trusted verdict: %+v", got.result)
	}
	if records := fixture.pins.Records(); len(records) != 0 {
		t.Fatalf("a pin was persisted after revocation: %+v", records)
	}
	if !fixture.hasReason("trust.verify:invalid") {
		t.Fatalf("revoked verification was not audited: %v", fixture.reasons())
	}
	if fixture.hasReason("trust.verify:trusted") || fixture.hasReason("trust.verify:unknown") {
		t.Fatalf("revoked verification reported a normal verdict: %v", fixture.reasons())
	}
}

// cancelAwareConfirmer blocks until its context is canceled, like the real
// host prompt, and records that cancellation reached it.
type cancelAwareConfirmer struct {
	entered chan struct{}
	done    chan error
}

func (c *cancelAwareConfirmer) Confirm(ctx context.Context,
	_ appplugin.TrustRequest) (bool, error) {
	close(c.entered)
	<-ctx.Done()
	c.done <- ctx.Err()
	return false, ctx.Err()
}

func TestTrustRevocationCancelsPendingConfirmation(t *testing.T) {
	confirmer := &cancelAwareConfirmer{
		entered: make(chan struct{}),
		done:    make(chan error, 1),
	}
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.TrustConfirmer = confirmer
	})
	fixture.grant(domainplugin.PermissionTrustVerify, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	material, _ := sshKeyMaterial(0x66)

	done := make(chan error, 1)
	go func() {
		_, err := fixture.session.TrustVerify(context.Background(), "db",
			appplugin.TrustKindSSHHostKey, material)
		done <- err
	}()
	select {
	case <-confirmer.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the confirmation did not reach the host")
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionTrustVerify)

	select {
	case err := <-confirmer.done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("confirmation context error = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("revocation did not cancel the pending confirmation")
	}
	select {
	case err := <-done:
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
			t.Fatalf("verify after canceled confirmation code = %q, want invalid", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TrustVerify did not return after cancellation")
	}
}

func TestAuthRevocationDuringRespondDoesNotReportSuccess(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.grant(domainplugin.PermissionAuthInvoke, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	challenge, err := fixture.session.AuthBegin(context.Background(), "db",
		appplugin.AuthMethodPassword)
	if err != nil {
		t.Fatalf("auth begin: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.auth.RespondFunc = func(challengeID, answer string) (appplugin.AuthResult, error) {
		close(entered)
		<-release
		return appplugin.AuthResult{ChallengeID: challengeID, Status: appplugin.AuthStatusSucceeded}, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := fixture.session.AuthRespond(context.Background(), challenge.ID, "answer")
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the broker did not receive the response")
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionAuthInvoke)
	close(release)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("auth respond succeeded after revocation")
		}
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
			t.Fatalf("revoked respond code = %q, want invalid", code)
		}
		var revoked *appplugin.CapabilityRevokedError
		if !errors.As(err, &revoked) {
			t.Fatalf("revoked respond error = %v, want CapabilityRevokedError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AuthRespond did not return after the broker")
	}
	if fixture.hasReason("auth.respond:succeeded") {
		t.Fatalf("revoked respond was audited as success: %v", fixture.reasons())
	}
	if _, err := fixture.session.AuthRespond(context.Background(), challenge.ID, "again"); err == nil {
		t.Fatal("the challenge survived revocation")
	}
}

// deadlineNetworkBroker blocks in the dial until its method context ends. It
// proves the dial deadline is the method's own bound, not the guest compute
// slice.
type deadlineNetworkBroker struct {
	entered chan struct{}
}

func (b *deadlineNetworkBroker) Resolve(_ context.Context, _ string,
	_ appplugin.DNSPolicy) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
}

func (b *deadlineNetworkBroker) DialPinned(ctx context.Context,
	_ appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *deadlineNetworkBroker) DialViaHost(ctx context.Context,
	target appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
	return b.DialPinned(ctx, target)
}

func (b *deadlineNetworkBroker) UpgradeTLS(_ context.Context,
	socket appplugin.ExternalSocket, _ appplugin.TLSClientPolicy) (appplugin.ExternalSocket, error) {
	return socket, nil
}

func TestNetworkDialOverItsOwnDeadlineFailsTyped(t *testing.T) {
	broker := &deadlineNetworkBroker{entered: make(chan struct{})}
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.Network = broker
	})
	fixture.grantNetwork("10.0.0.7", 5432, domainplugin.NetworkProtocolTCP, "")

	start := time.Now()
	_, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.7", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
		Timeout: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("a dial past its own deadline succeeded")
	}
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeFault {
		t.Fatalf("dial timeout code = %q, want fault", code)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("dial timeout error = %v, want a typed timeout message", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("dial timeout did not honor the method bound: %v", elapsed)
	}
}

// revocationUpgradeBroker dials normally but blocks the TLS handshake until
// its context ends, like a host transport that honors cancellation.
type revocationUpgradeBroker struct {
	entered chan struct{}
}

func (b *revocationUpgradeBroker) Resolve(context.Context, string,
	appplugin.DNSPolicy) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
}

func (b *revocationUpgradeBroker) DialPinned(context.Context,
	appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
	return newTestSocket(""), nil
}

func (b *revocationUpgradeBroker) DialViaHost(ctx context.Context,
	target appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
	return b.DialPinned(ctx, target)
}

func (b *revocationUpgradeBroker) UpgradeTLS(ctx context.Context,
	_ appplugin.ExternalSocket, _ appplugin.TLSClientPolicy) (appplugin.ExternalSocket, error) {
	close(b.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestNetworkTLSUpgradeRevocationDuringHandshakeIsRejected(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	dialed := newTestSocket("")
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return dialed, nil
	}
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTCP, "")
	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	upgradedSocket := newTestSocket("")
	fixture.network.UpgradeFunc = func(appplugin.ExternalSocket,
		appplugin.TLSClientPolicy) (appplugin.ExternalSocket, error) {
		close(entered)
		<-release
		return upgradedSocket, nil
	}

	type outcome struct {
		result appplugin.NetworkTLSUpgradeResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		upgraded, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle)
		done <- outcome{result: upgraded, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the TLS handshake did not reach the host transport")
	}

	// Revocation must not wait behind the in-flight handshake.
	revoked := make(chan struct{})
	go func() {
		fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
			domainplugin.PermissionNetworkDial)
		close(revoked)
	}()
	select {
	case <-revoked:
	case <-time.After(2 * time.Second):
		// Release the handshake so a regression cannot deadlock cleanup.
		close(release)
		t.Fatal("Revoke blocked behind the in-flight TLS upgrade")
	}
	close(release)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("NetworkTLSUpgrade did not return after the handshake")
	}
	if got.err == nil {
		t.Fatalf("TLS upgrade succeeded after revocation: %+v", got.result)
	}
	if got.result.Mode != "" || got.result.ServerName != "" {
		t.Fatalf("revoked upgrade returned a successful result: %+v", got.result)
	}
	if code := serviceErrorCode(t, got.err); code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked upgrade code = %q, want invalid", code)
	}
	var capability *appplugin.CapabilityRevokedError
	if !errors.As(got.err, &capability) {
		t.Fatalf("revoked upgrade error = %v, want CapabilityRevokedError", got.err)
	}
	if !fixture.hasReason("network.tls_upgrade:invalid") {
		t.Fatalf("revoked upgrade was not audited: %v", fixture.reasons())
	}
	if fixture.hasReason("network.tls_upgrade:ok") {
		t.Fatalf("revoked upgrade was audited as success: %v", fixture.reasons())
	}
	if closed, _, _, _ := dialed.state(); !closed {
		t.Fatal("a revoked in-band upgrade left the connection open")
	}
	if closed, _, _, _ := upgradedSocket.state(); !closed {
		t.Fatal("a revoked in-band upgrade leaked the broker socket")
	}
	if _, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle); err == nil {
		t.Fatal("the revoked connection still accepted a TLS upgrade")
	}
}

func TestNetworkTLSUpgradeRevocationCancelsCtxAwareHandshake(t *testing.T) {
	broker := &revocationUpgradeBroker{entered: make(chan struct{})}
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.Network = broker
	})
	fixture.grantNetwork("10.0.0.7", 5432, domainplugin.NetworkProtocolTCP, "")
	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "10.0.0.7", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle)
		done <- err
	}()
	select {
	case <-broker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the TLS handshake did not reach the host transport")
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionNetworkDial)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("TLS upgrade succeeded after revocation")
		}
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
			t.Fatalf("revoked upgrade code = %q, want invalid (not fault)", code)
		}
		var capability *appplugin.CapabilityRevokedError
		if !errors.As(err, &capability) {
			t.Fatalf("revoked upgrade error = %v, want CapabilityRevokedError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("NetworkTLSUpgrade did not return after revocation")
	}
	if fixture.hasReason("network.tls_upgrade:ok") {
		t.Fatalf("revoked upgrade was audited as success: %v", fixture.reasons())
	}
}

// blockingSavePinStore persists the pin first and then blocks inside Save, so
// a test can revoke between the write and the post-save check.
type blockingSavePinStore struct {
	*appplugin.MemoryTrustPinStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	deletes atomic.Int32
}

func (s *blockingSavePinStore) Save(ctx context.Context, record appplugin.TrustRecord) error {
	if err := s.MemoryTrustPinStore.Save(ctx, record); err != nil {
		return err
	}
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return nil
}

func (s *blockingSavePinStore) Delete(ctx context.Context, record appplugin.TrustRecord) error {
	s.deletes.Add(1)
	return s.MemoryTrustPinStore.Delete(ctx, record)
}

func TestTrustRevocationDuringPinSaveRollsBack(t *testing.T) {
	store := &blockingSavePinStore{
		MemoryTrustPinStore: &appplugin.MemoryTrustPinStore{},
		entered:             make(chan struct{}),
		release:             make(chan struct{}),
	}
	fixture := newExternalFixture(t, func(deps *appplugin.ServiceDeps) {
		deps.TrustPins = store
	})
	fixture.confirmer.Decision = true
	fixture.grant(domainplugin.PermissionTrustVerify, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	material, _ := sshKeyMaterial(0x77)

	done := make(chan error, 1)
	go func() {
		_, err := fixture.session.TrustVerify(context.Background(), "db",
			appplugin.TrustKindSSHHostKey, material)
		done <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the pin save did not reach the store")
	}
	// The pin is already written when the revocation lands.
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionTrustVerify)
	close(store.release)

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("trust verification succeeded after revocation during save")
		}
		if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeInvalid {
			t.Fatalf("revoked verification code = %q, want invalid", code)
		}
		var capability *appplugin.CapabilityRevokedError
		if !errors.As(err, &capability) {
			t.Fatalf("revoked verification error = %v, want CapabilityRevokedError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("TrustVerify did not return after the save")
	}
	if records := store.Records(); len(records) != 0 {
		t.Fatalf("a revoked pin survived the rollback: %+v", records)
	}
	if store.deletes.Load() == 0 {
		t.Fatal("revocation did not roll back the saved pin")
	}
	if !fixture.hasReason("trust.verify:invalid") || fixture.hasReason("trust.verify:trusted") {
		t.Fatalf("revoked save reported a normal verdict: %v", fixture.reasons())
	}
}

func TestMultiPortGrantAllowsDialAndTLSUpgrade(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		return newTestSocket(""), nil
	}
	fixture.grantNetworkPorts("db.internal", []int{5432, 5433},
		domainplugin.NetworkProtocolTCP, "")

	result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err != nil {
		t.Fatalf("dial under a multi-port grant: %v", err)
	}
	upgraded, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle)
	if err != nil {
		t.Fatalf("TLS upgrade under a multi-port grant: %v", err)
	}
	if upgraded.Mode != domainplugin.TLSPolicyVerify || upgraded.ServerName != "db.internal" {
		t.Fatalf("upgrade result = %+v, want host-owned verify with SNI", upgraded)
	}
	if _, err := fixture.session.NetworkTLSUpgrade(context.Background(), result.Handle); err == nil {
		t.Fatal("second in-band upgrade was allowed")
	} else if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeConflict {
		t.Fatalf("second upgrade code = %q, want conflict", code)
	}
	if !fixture.hasReason("network.tls_upgrade:ok") {
		t.Fatalf("successful upgrade was not audited: %v", fixture.reasons())
	}
}

func TestMultiPortGrantConnectionRefusedMapsToFault(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	fixture.network.DialErr = errors.New("connection refused")
	fixture.grantNetworkPorts("db.internal", []int{5432, 5433},
		domainplugin.NetworkProtocolTCP, "")

	_, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
	})
	if err == nil {
		t.Fatal("a refused dial succeeded")
	}
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeFault {
		t.Fatalf("refused dial code = %q, want fault", code)
	}
	var revoked *appplugin.CapabilityRevokedError
	if errors.As(err, &revoked) {
		t.Fatalf("refused dial was masked as revoked: %v", err)
	}
	if !fixture.hasReason("network.dial:fault") || fixture.hasReason("network.dial:invalid") {
		t.Fatalf("refused dial audit = %v, want fault", fixture.reasons())
	}
}

func TestMultiPortGrantTrustRejectionIsNotMasked(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	fixture.network.DialErr = appplugin.ErrExternalTrustRejected
	fixture.grantNetworkPorts("db.internal", []int{5432, 5433},
		domainplugin.NetworkProtocolTLS, domainplugin.TLSPolicyVerify)

	_, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
		Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTLS,
	})
	if err == nil {
		t.Fatal("a trust-rejected dial succeeded")
	}
	if code := serviceErrorCode(t, err); code != appplugin.ServiceCodeDenied {
		t.Fatalf("trust rejection code = %q, want denied", code)
	}
	var revoked *appplugin.CapabilityRevokedError
	if errors.As(err, &revoked) {
		t.Fatalf("trust rejection was masked as revoked: %v", err)
	}
	if !fixture.hasReason("network.dial:denied") || fixture.hasReason("network.dial:invalid") {
		t.Fatalf("trust rejection audit = %v, want denied", fixture.reasons())
	}
}

func TestRevokedDialAfterBrokerSuccessReturnsCapabilityRevoked(t *testing.T) {
	fixture := newExternalFixture(t, nil)
	fixture.network.Addresses["db.internal"] = []netip.Addr{netip.MustParseAddr("10.0.0.7")}
	socket := newTestSocket("")
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.network.DialFunc = func(appplugin.NetworkDialTarget) (appplugin.ExternalSocket, error) {
		close(entered)
		<-release
		return socket, nil
	}
	fixture.grantNetwork("db.internal", 5432, domainplugin.NetworkProtocolTCP, "")

	type outcome struct {
		result appplugin.NetworkDialResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := fixture.session.NetworkDial(context.Background(), appplugin.NetworkDialSpec{
			Host: "db.internal", Port: 5432, Protocol: domainplugin.NetworkProtocolTCP,
		})
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial did not reach the host transport")
	}
	fixture.authority.Revoke(appplugin.SubjectForRef(fixture.ref),
		domainplugin.PermissionNetworkDial)
	close(release)

	var got outcome
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("NetworkDial did not return after the broker")
	}
	if got.err == nil {
		t.Fatalf("dial succeeded after revocation: %+v", got.result)
	}
	var typed *appplugin.ServiceError
	if !errors.As(got.err, &typed) {
		t.Fatalf("revoked dial error = %T %v, want *ServiceError", got.err, got.err)
	}
	if typed.Code != appplugin.ServiceCodeInvalid {
		t.Fatalf("revoked dial code = %q, want invalid", typed.Code)
	}
	var capability *appplugin.CapabilityRevokedError
	if !errors.As(got.err, &capability) {
		t.Fatalf("revoked dial error = %v, want CapabilityRevokedError", got.err)
	}
	if got.result.Handle != 0 {
		t.Fatalf("revoked dial issued handle %#x", uint64(got.result.Handle))
	}
	if closed, _, _, _ := socket.state(); !closed {
		t.Fatal("a revoked dial left the broker socket open")
	}
	if !fixture.hasReason("network.dial:invalid") || fixture.hasReason("network.dial:ok") {
		t.Fatalf("revoked dial audit = %v, want invalid only", fixture.reasons())
	}
	if _, err := fixture.session.NetworkRead(context.Background(), got.result.Handle, 1); err == nil {
		t.Fatal("a revoked dial left a usable handle")
	}
}
