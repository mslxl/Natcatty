package plugin

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dispatchSocket is a minimal ExternalSocket for the wire-contract tests.
type dispatchSocket struct {
	mu     sync.Mutex
	data   []byte
	offset int
	writes []byte
	closed bool
}

func (s *dispatchSocket) Read(buffer []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.offset >= len(s.data) {
		return 0, io.EOF
	}
	n := copy(buffer, s.data[s.offset:])
	s.offset += n
	return n, nil
}

func (s *dispatchSocket) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, data...)
	return len(data), nil
}

func (s *dispatchSocket) CloseWrite() error { return nil }
func (s *dispatchSocket) CloseRead() error  { return nil }
func (s *dispatchSocket) SetDeadline(time.Time) error {
	return nil
}

func (s *dispatchSocket) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// dispatchAudit records audit reasons for the wire-contract tests.
type dispatchAudit struct {
	mu      sync.Mutex
	reasons []string
}

func (a *dispatchAudit) Record(event domainplugin.AuditEvent) {
	a.mu.Lock()
	a.reasons = append(a.reasons, event.Reason)
	a.mu.Unlock()
}

func (a *dispatchAudit) Close() error { return nil }

type dispatchFixture struct {
	session   *ServiceSession
	authority *Authority
	network   *MemoryNetworkBroker
	auth      *MemoryAuthBroker
	pins      *MemoryTrustPinStore
	confirmer *MemoryTrustConfirmer
	vault     *MemoryVaultFields
	audit     *dispatchAudit
	subject   AuthoritySubject
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	authority := NewAuthority()
	audit := &dispatchAudit{}
	network := &MemoryNetworkBroker{Addresses: map[string][]netip.Addr{
		"echo.internal": {netip.MustParseAddr("10.0.0.4")},
	}}
	auth := &MemoryAuthBroker{}
	pins := &MemoryTrustPinStore{}
	confirmer := &MemoryTrustConfirmer{Decision: true}
	vault := NewMemoryVaultFields()
	vault.DescriptorsByHost["db"] = []VaultFieldDescriptor{
		{HostID: "db", Field: "password", Kind: "password", GuestDeliverable: true},
	}
	vault.Values["db/password"] = "wire-secret"
	host := NewHostServices(ServiceDeps{
		Authority:      authority,
		Audit:          audit,
		Now:            func() time.Time { return now },
		Network:        network,
		Auth:           auth,
		TrustPins:      pins,
		TrustConfirmer: confirmer,
		Vault:          vault,
	})
	ref := PluginRef{ID: "acme.wire", Version: "1.0.0", Source: domainplugin.SourceLocalFile}
	fiber := domainplugin.NewFiberID("installed", ref.ID, 1)
	session := host.Open(ref, fiber, nil)
	session.BindHandles(NewHandleTable(MaxHandles))
	t.Cleanup(func() { host.CloseAll() })
	return &dispatchFixture{
		session: session, authority: authority, network: network, auth: auth,
		pins: pins, confirmer: confirmer, vault: vault, audit: audit,
		subject: SubjectForRef(ref),
	}
}

func (f *dispatchFixture) hasReason(reason string) bool {
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	for _, got := range f.audit.reasons {
		if got == reason {
			return true
		}
	}
	return false
}

func (f *dispatchFixture) grant(permission domainplugin.Permission,
	scope domainplugin.Scope) {
	f.authority.Grant(f.subject, domainplugin.PermissionGrant{
		Permission: permission, Scope: scope,
	})
}

func callDispatch(t *testing.T, session *ServiceSession, service string,
	method uint32, request string) serviceEnvelope {
	t.Helper()
	payload := serviceDispatcher{session: session}.Call(context.Background(),
		ServiceCall{Service: service, Method: method, Request: []byte(request)})
	var envelope serviceEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("decode %s response %q: %v", service, payload, err)
	}
	return envelope
}

func dispatchData(t *testing.T, envelope serviceEnvelope) map[string]any {
	t.Helper()
	if envelope.Code != ServiceCodeOK {
		t.Fatalf("envelope = %+v", envelope)
	}
	var data map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode data %q: %v", envelope.Data, err)
	}
	return data
}

func TestDispatcherRoutesExternalServices(t *testing.T) {
	fixture := newDispatchFixture(t)
	socket := &dispatchSocket{data: []byte("hi")}
	fixture.network.DialFunc = func(NetworkDialTarget) (ExternalSocket, error) {
		return socket, nil
	}
	fixture.grant(domainplugin.PermissionNetworkDial, domainplugin.Scope{
		Kind: domainplugin.ScopeNetwork, Host: "echo.internal", Ports: []int{7},
		Protocol: domainplugin.NetworkProtocolTCP,
	})

	dial := dispatchData(t, callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodDial,
		`{"host":"echo.internal","port":7,"protocol":"tcp"}`))
	handle, ok := dial["handle"].(string)
	if !ok || handle == "" || handle == "0" {
		t.Fatalf("dial data = %+v", dial)
	}
	if dial["resolved"] != "10.0.0.4" {
		t.Fatalf("dial resolved = %v", dial["resolved"])
	}
	write := dispatchData(t, callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodWrite,
		`{"handle":"`+handle+`","data":"`+base64.StdEncoding.EncodeToString([]byte("yo"))+`"}`))
	if write["written"].(float64) != 2 {
		t.Fatalf("write data = %+v", write)
	}
	read := dispatchData(t, callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodRead,
		`{"handle":"`+handle+`","max_bytes":2}`))
	if read["data"] != base64.StdEncoding.EncodeToString([]byte("hi")) ||
		read["eof"].(bool) {
		t.Fatalf("read data = %+v", read)
	}
	callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodShutdown,
		`{"handle":"`+handle+`","direction":"write"}`)
	callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodClose, `{"handle":"`+handle+`"}`)
	closed := callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodRead, `{"handle":"`+handle+`","max_bytes":1}`)
	if closed.Code != ServiceCodeInvalid {
		t.Fatalf("read after close code = %q, want invalid", closed.Code)
	}

	// A denied dial carries its typed permission and scope on the wire.
	denied := callDispatch(t, fixture.session, ServiceNetwork,
		domainpluginabi.NetworkMethodDial,
		`{"host":"other.internal","port":7,"protocol":"tcp"}`)
	if denied.Code != ServiceCodeDenied || denied.Permission != domainplugin.PermissionNetworkDial {
		t.Fatalf("denied envelope = %+v", denied)
	}
	if denied.Scope == nil || denied.Scope.Host != "other.internal" {
		t.Fatalf("denied scope = %+v", denied.Scope)
	}

	fixture.grant(domainplugin.PermissionAuthInvoke, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	begin := dispatchData(t, callDispatch(t, fixture.session, ServiceAuth,
		domainpluginabi.AuthMethodBegin, `{"host_id":"db","method":"password"}`))
	challengeID, _ := begin["challenge_id"].(string)
	if challengeID == "" || begin["method"] != "password" {
		t.Fatalf("begin data = %+v", begin)
	}
	respond := dispatchData(t, callDispatch(t, fixture.session, ServiceAuth,
		domainpluginabi.AuthMethodRespond,
		`{"challenge_id":"`+challengeID+`","answer":"123456"}`))
	if respond["status"] != string(AuthStatusSucceeded) {
		t.Fatalf("respond data = %+v", respond)
	}
	dead := callDispatch(t, fixture.session, ServiceAuth,
		domainpluginabi.AuthMethodCancel, `{"challenge_id":"missing"}`)
	if dead.Code != ServiceCodeNotFound {
		t.Fatalf("cancel missing code = %q", dead.Code)
	}

	keyType := "ssh-wire01"
	blob := make([]byte, 4+len(keyType)+8)
	binary.BigEndian.PutUint32(blob, uint32(len(keyType)))
	copy(blob[4:], keyType)
	// The wire field base64-encodes the material bytes; SSH material itself
	// is the textual "keytype base64" form the host fingerprint parser reads.
	textual := base64.StdEncoding.EncodeToString(blob)
	material := base64.StdEncoding.EncodeToString([]byte(textual))
	fixture.grant(domainplugin.PermissionTrustVerify, domainplugin.Scope{
		Kind: domainplugin.ScopeHost, Host: "db",
	})
	verify := dispatchData(t, callDispatch(t, fixture.session, ServiceTrust,
		domainpluginabi.TrustMethodVerify,
		`{"host_id":"db","kind":"ssh-hostkey","material":"`+material+`"}`))
	if verify["verdict"] != string(TrustVerdictTrusted) ||
		verify["key_type"] != keyType ||
		verify["fingerprint"] != domainconn.Fingerprint(blob) {
		t.Fatalf("verify data = %+v", verify)
	}

	fixture.grant(domainplugin.PermissionVaultMetadata, domainplugin.Scope{
		Kind: domainplugin.ScopeVault, Host: "db", Field: "password",
	})
	metadata := dispatchData(t, callDispatch(t, fixture.session, ServiceVault,
		domainpluginabi.VaultMethodMetadata, `{"host_id":"db"}`))
	fields, _ := metadata["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("metadata data = %+v", metadata)
	}
	descriptor := dispatchData(t, callDispatch(t, fixture.session, ServiceVault,
		domainpluginabi.VaultMethodFields, `{"host_id":"db","field":"password"}`))
	field, _ := descriptor["field"].(map[string]any)
	if field["guest_deliverable"] != true || field["host_id"] != "db" {
		t.Fatalf("fields data = %+v", descriptor)
	}

	fixture.grant(domainplugin.PermissionVaultSecrets, domainplugin.Scope{
		Kind: domainplugin.ScopeVault, Host: "db", Field: "password",
	})
	lease := dispatchData(t, callDispatch(t, fixture.session, ServiceVault,
		domainpluginabi.VaultMethodLeaseIssue,
		`{"host_id":"db","field":"password"}`))
	leaseHandle, _ := lease["handle"].(string)
	if leaseHandle == "" || lease["expires_at"] == "" {
		t.Fatalf("lease data = %+v", lease)
	}
	value := dispatchData(t, callDispatch(t, fixture.session, ServiceVault,
		domainpluginabi.VaultMethodLeaseRead, `{"handle":"`+leaseHandle+`"}`))
	if value["value"] != base64.StdEncoding.EncodeToString([]byte("wire-secret")) {
		t.Fatalf("lease value = %+v", value)
	}
	callDispatch(t, fixture.session, ServiceVault,
		domainpluginabi.VaultMethodLeaseRelease, `{"handle":"`+leaseHandle+`"}`)
	released := callDispatch(t, fixture.session, ServiceVault,
		domainpluginabi.VaultMethodLeaseRead, `{"handle":"`+leaseHandle+`"}`)
	if released.Code != ServiceCodeInvalid {
		t.Fatalf("read released lease code = %q", released.Code)
	}

	for _, service := range []string{ServiceNetwork, ServiceAuth, ServiceTrust, ServiceVault} {
		unknown := callDispatch(t, fixture.session, service, 999, `{}`)
		if unknown.Code != ServiceCodeInvalid ||
			!strings.Contains(unknown.Message, "unknown method") {
			t.Fatalf("%s unknown method envelope = %+v", service, unknown)
		}
	}
	unknownService := callDispatch(t, fixture.session, "nope", 1, `{}`)
	if unknownService.Code != ServiceCodeInvalid {
		t.Fatalf("unknown service envelope = %+v", unknownService)
	}
}

func TestGuestRevokedDialAfterBrokerSuccessEnvelopeIsInvalid(t *testing.T) {
	fixture := newDispatchFixture(t)
	socket := &dispatchSocket{}
	entered := make(chan struct{})
	release := make(chan struct{})
	fixture.network.DialFunc = func(NetworkDialTarget) (ExternalSocket, error) {
		close(entered)
		<-release
		return socket, nil
	}
	fixture.grant(domainplugin.PermissionNetworkDial, domainplugin.Scope{
		Kind: domainplugin.ScopeNetwork, Host: "echo.internal",
		Ports: []int{5432}, Protocol: domainplugin.NetworkProtocolTCP,
	})

	done := make(chan serviceEnvelope, 1)
	go func() {
		done <- callDispatch(t, fixture.session, ServiceNetwork,
			domainpluginabi.NetworkMethodDial,
			`{"host":"echo.internal","port":5432,"protocol":"tcp"}`)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the dial did not reach the host transport")
	}
	fixture.authority.Revoke(fixture.subject, domainplugin.PermissionNetworkDial)
	close(release)

	var envelope serviceEnvelope
	select {
	case envelope = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the guest dispatch did not return after the broker")
	}
	if envelope.Code != ServiceCodeInvalid {
		t.Fatalf("revoked dial envelope = %+v, want invalid (not fault)", envelope)
	}
	if len(envelope.Data) != 0 {
		t.Fatalf("revoked dial returned data %s", envelope.Data)
	}
	if !fixture.hasReason("network.dial:invalid") || fixture.hasReason("network.dial:ok") {
		t.Fatal("revoked guest dial was not audited as invalid only")
	}
	socket.mu.Lock()
	closed := socket.closed
	socket.mu.Unlock()
	if !closed {
		t.Fatal("a revoked guest dial left the socket open")
	}
}
