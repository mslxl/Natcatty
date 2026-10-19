package connection

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
)

type fakePasswordSource struct {
	hostID   model.HostID
	password string
	err      error
	calls    int
}

func (f *fakePasswordSource) Password(_ context.Context, hostID model.HostID) (string, error) {
	f.calls++
	f.hostID = hostID
	return f.password, f.err
}

type fakeClient struct {
	mu       sync.Mutex
	closed   bool
	term     TerminalSpec
	openErr  error
	terminal Terminal
}

func (f *fakeClient) OpenTerminal(_ context.Context, spec TerminalSpec) (Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.term = spec
	if f.openErr != nil {
		return nil, f.openErr
	}
	if f.terminal == nil {
		f.terminal = &fakeTerminal{}
	}
	return f.terminal, nil
}

func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeClient) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

type fakeTerminal struct {
	closed bool
}

func (f *fakeTerminal) Read([]byte) (int, error)    { return 0, io.EOF }
func (f *fakeTerminal) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeTerminal) Resize(int, int) error       { return nil }
func (f *fakeTerminal) Close() error                { f.closed = true; return nil }

type fakeDialer struct {
	mu            sync.Mutex
	req           DialRequest
	key           domainconn.Key
	verify        bool
	authAttempted bool
	authMethod    string
	client        *fakeClient
	err           error
	dials         int
}

func (f *fakeDialer) Dial(ctx context.Context, req DialRequest) (Client, error) {
	f.mu.Lock()
	f.req = req
	f.dials++
	f.mu.Unlock()
	if f.verify && req.VerifyHostKey != nil {
		if err := req.VerifyHostKey(ctx, f.key); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	f.authAttempted = true
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.client == nil {
		f.client = &fakeClient{}
	}
	if req.OnAuth != nil && f.authMethod != "" {
		req.OnAuth(f.authMethod)
	}
	return f.client, nil
}

func (f *fakeDialer) request() DialRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.req
}

func (f *fakeDialer) authenticated() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authAttempted
}

func (f *fakeDialer) dialCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials
}

type fakeKnownHosts struct {
	records []domainconn.KnownHost
	upserts []domainconn.KnownHost
	listErr error
	saveErr error
}

func (f *fakeKnownHosts) ListKnownHosts(context.Context) ([]domainconn.KnownHost, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]domainconn.KnownHost(nil), f.records...), nil
}

func (f *fakeKnownHosts) UpsertKnownHost(_ context.Context, known domainconn.KnownHost) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.upserts = append(f.upserts, known)
	return nil
}

type fakePrompter struct {
	mu       sync.Mutex
	keys     []domainconn.Key
	decision PromptDecision
	err      error
}

func (f *fakePrompter) Confirm(_ context.Context, key domainconn.Key) (PromptDecision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	return f.decision, f.err
}

func (f *fakePrompter) prompted() []domainconn.Key {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domainconn.Key(nil), f.keys...)
}

func newTestManager(t *testing.T, opts ...Option) (*Manager, *fakePasswordSource, *fakeDialer, *fakeKnownHosts, *fakePrompter) {
	t.Helper()
	passwords := &fakePasswordSource{password: "s3cret"}
	dialer := &fakeDialer{}
	known := &fakeKnownHosts{}
	prompter := &fakePrompter{decision: PromptDecision{Trust: true, Persist: true}}
	all := append([]Option{
		WithPrompter(prompter),
		WithClock(func() time.Time { return time.Unix(1700000000, 0).UTC() }),
		WithIDGenerator(func() string { return "kh-new" }),
	}, opts...)
	manager := NewManager(dialer, passwords, known, all...)
	return manager, passwords, dialer, known, prompter
}

func sshRequest() Request {
	return Request{HostID: "host-1", Hostname: "example.com", Port: 22, Username: "root"}
}

func TestAcquireNeverPoolsX11Connections(t *testing.T) {
	dialer := &sequenceDialer{}
	manager := poolTestManager(t, dialer)

	x11 := sshRequest()
	x11.X11Forwarding = true
	first, err := manager.Acquire(context.Background(), x11)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	second, err := manager.Acquire(context.Background(), x11)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if dialer.dialCount() != 2 {
		t.Fatalf("dials = %d, want 2: an X11 Connection was shared", dialer.dialCount())
	}
	if first.conn == second.conn {
		t.Fatal("two X11 Sessions shared one Connection")
	}

	// A plain request for the same endpoint must not join the X11 transport
	// either, because X11 forwarding is negotiated for the whole connection.
	plain, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("plain Acquire: %v", err)
	}
	if dialer.dialCount() != 3 {
		t.Fatalf("dials = %d, want 3: a plain Session reused an X11 Connection", dialer.dialCount())
	}

	firstClient := dialer.clients[0]
	_ = second.Release()
	_ = plain.Release()
	_ = first.Release()
	if !firstClient.isClosed() {
		t.Fatal("an X11 Connection was parked instead of closed")
	}
}

func TestAcquireUsesTheStoredPasswordAndParksTheTransport(t *testing.T) {
	manager, passwords, dialer, _, _ := newTestManager(t)

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if passwords.hostID != "host-1" {
		t.Fatalf("password lookup HostID = %q, want host-1", passwords.hostID)
	}
	request := dialer.request()
	if request.Password != "s3cret" || request.Username != "root" ||
		request.Hostname != "example.com" || request.Port != 22 {
		t.Fatalf("dial request = %+v", request)
	}

	terminal, err := lease.OpenTerminal(context.Background(), TerminalSpec{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatalf("OpenTerminal: %v", err)
	}
	if terminal == nil {
		t.Fatal("OpenTerminal returned a nil terminal")
	}
	if dialer.client.term.Cols != 120 || dialer.client.term.Rows != 40 {
		t.Fatalf("terminal spec = %+v, want 120x40", dialer.client.term)
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Ticket 09 parks the last released transport for reuse instead of
	// closing it. A second consumer reaches the same physical connection.
	if dialer.client.isClosed() {
		t.Fatal("releasing the last lease closed a transport that should be parked")
	}
	second, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if got := dialer.dialCount(); got != 1 {
		t.Fatalf("dial count = %d, want 1 while the transport is parked", got)
	}
	_ = second.Release()
	manager.CloseAll()
	if !dialer.client.isClosed() {
		t.Fatal("CloseAll did not close the parked transport")
	}
}

func TestTrustedHostKeySkipsThePrompt(t *testing.T) {
	fingerprint := domainconn.Fingerprint([]byte("server-key"))
	manager, _, dialer, known, prompter := newTestManager(t)
	known.records = []domainconn.KnownHost{{
		ID: "kh-1", Hostname: "example.com", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: fingerprint,
	}}
	dialer.verify = true
	dialer.key = domainconn.Key{
		Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: fingerprint,
	}

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()

	if len(prompter.prompted()) != 0 {
		t.Fatalf("trusted key prompted: %+v", prompter.prompted())
	}
	if len(known.upserts) != 0 {
		t.Fatalf("trusted key was rewritten: %+v", known.upserts)
	}
	if !dialer.authenticated() {
		t.Fatal("authentication did not proceed for a trusted key")
	}
}

func TestUnknownHostKeyIsPromptedAndTrustPersists(t *testing.T) {
	manager, _, dialer, known, prompter := newTestManager(t)
	dialer.verify = true
	dialer.key = domainconn.Key{
		Hostname: "example.com", Port: 2222, KeyType: "ssh-ed25519",
		Fingerprint: "fingerprint-1", PublicKey: "ssh-ed25519 AAAA",
	}

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()

	prompted := prompter.prompted()
	if len(prompted) != 1 {
		t.Fatalf("prompts = %+v, want one", prompted)
	}
	if prompted[0].Status != domainconn.StatusUnknown {
		t.Fatalf("prompt status = %q, want unknown", prompted[0].Status)
	}
	if prompted[0].Fingerprint != "fingerprint-1" {
		t.Fatalf("prompt fingerprint = %q", prompted[0].Fingerprint)
	}
	if len(known.upserts) != 1 {
		t.Fatalf("upserts = %+v, want one Known Host", known.upserts)
	}
	saved := known.upserts[0]
	if saved.ID != "kh-new" || saved.Hostname != "example.com" || saved.Port != 2222 ||
		saved.KeyType != "ssh-ed25519" || saved.Fingerprint != "fingerprint-1" {
		t.Fatalf("saved Known Host = %+v", saved)
	}
	if saved.DiscoveredAt.IsZero() || !saved.DiscoveredAt.Equal(saved.LastSeen) {
		t.Fatalf("saved timestamps = %v / %v", saved.DiscoveredAt, saved.LastSeen)
	}
	if !dialer.authenticated() {
		t.Fatal("authentication did not proceed after the user trusted the key")
	}
}

func TestChangedHostKeyIsDistinctAndRejectionAbortsBeforeAuth(t *testing.T) {
	manager, _, dialer, known, prompter := newTestManager(t)
	known.records = []domainconn.KnownHost{{
		ID: "kh-old", Hostname: "example.com", Port: 22,
		KeyType: "ssh-ed25519", Fingerprint: "old-fingerprint",
	}}
	prompter.decision = PromptDecision{Trust: false}
	dialer.verify = true
	dialer.key = domainconn.Key{
		Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519",
		Fingerprint: "new-fingerprint",
	}

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if lease != nil {
		t.Fatal("Acquire returned a lease after the user rejected the key")
	}
	if !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("Acquire error = %v, want ErrHostKeyRejected", err)
	}
	assertKind(t, err, KindHostKey)

	prompted := prompter.prompted()
	if len(prompted) != 1 || prompted[0].Status != domainconn.StatusChanged {
		t.Fatalf("prompts = %+v, want one changed prompt", prompted)
	}
	if prompted[0].ExpectedFingerprint != "old-fingerprint" {
		t.Fatalf("expected fingerprint = %q", prompted[0].ExpectedFingerprint)
	}
	if dialer.authenticated() {
		t.Fatal("authentication was attempted after the host key was rejected")
	}
	if len(known.upserts) != 0 {
		t.Fatalf("a rejected key was saved: %+v", known.upserts)
	}
}

func TestRejectionWithoutAPrompterFailsClosed(t *testing.T) {
	manager, _, dialer, _, _ := newTestManager(t, WithPrompter(nil))
	dialer.verify = true
	dialer.key = domainconn.Key{Hostname: "example.com", Port: 22, KeyType: "ssh-ed25519", Fingerprint: "x"}

	if _, err := manager.Acquire(context.Background(), sshRequest()); !errors.Is(err, ErrHostKeyRejected) {
		t.Fatalf("Acquire error = %v, want ErrHostKeyRejected", err)
	}
	if dialer.authenticated() {
		t.Fatal("authentication was attempted without a trust decision")
	}
}

func TestDialFailuresAreTyped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		kind Kind
	}{
		{"auth", &Error{Kind: KindAuth, Cause: ErrAuthentication}, KindAuth},
		{"network", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("refused")}, KindNetwork},
		{"timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, KindTimeout},
		{"unsupported", &Error{Kind: KindUnsupported, Cause: ErrUnsupportedAlgorithm}, KindUnsupported},
		{"cancelled", context.Canceled, KindCancelled},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, _, dialer, _, _ := newTestManager(t)
			dialer.err = test.err
			_, err := manager.Acquire(context.Background(), sshRequest())
			if err == nil {
				t.Fatal("Acquire succeeded, want an error")
			}
			assertKind(t, err, test.kind)
		})
	}
}

func TestPasswordLookupFailureIsAnAuthError(t *testing.T) {
	manager, passwords, _, _, _ := newTestManager(t)
	passwords.err = ErrNoCredentials

	_, err := manager.Acquire(context.Background(), sshRequest())
	assertKind(t, err, KindAuth)
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Acquire error = %v, want ErrNoCredentials", err)
	}
}

func TestLeaseCountsReferencesAndClosesOnce(t *testing.T) {
	client := &fakeClient{}
	conn := newConnection(client)
	first := conn.acquire()
	second := conn.acquire()

	if err := first.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if client.isClosed() {
		t.Fatal("connection closed while a lease was still held")
	}
	if _, err := first.OpenTerminal(context.Background(), TerminalSpec{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("released lease OpenTerminal error = %v, want ErrClosed", err)
	}

	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if !client.isClosed() {
		t.Fatal("connection stayed open after the last lease was released")
	}
	if got := conn.acquire(); got != nil {
		t.Fatal("a closed connection handed out a lease")
	}
}

func TestManagerWithoutPortsFailsSafely(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	if _, err := manager.Acquire(context.Background(), sshRequest()); !errors.Is(err, ErrNoDialer) {
		t.Fatalf("Acquire error = %v, want ErrNoDialer", err)
	}
}

type fakeAuthSource struct {
	plan  AuthPlan
	err   error
	calls int
	req   Request
}

func (f *fakeAuthSource) AuthPlan(_ context.Context, req Request) (AuthPlan, error) {
	f.calls++
	f.req = req
	return f.plan, f.err
}

type fakeAuthPrompter struct{}

func (fakeAuthPrompter) Passphrase(context.Context, PassphraseRequest) (PassphraseDecision, error) {
	return PassphraseDecision{}, nil
}

func (fakeAuthPrompter) KeyboardInteractive(context.Context, KeyboardInteractiveRequest) (KeyboardInteractiveDecision, error) {
	return KeyboardInteractiveDecision{}, nil
}

func TestAcquireResolvesAuthSourceAndForwardsThePrompter(t *testing.T) {
	source := &fakeAuthSource{plan: AuthPlan{
		Mode:     AuthModeKey,
		Username: "alice",
		Candidates: []AuthCandidate{{
			Kind: AuthKindPublicKey, ID: "publickey-user", Primary: true,
			Key: &KeyMaterial{ID: "key-1", Label: "work", PrivateKey: "material"},
		}},
	}}
	prompter := fakeAuthPrompter{}
	manager, passwords, dialer, _, _ := newTestManager(t, WithAuthSource(source), WithAuthPrompter(prompter))

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()

	if passwords.calls != 0 {
		t.Errorf("PasswordSource was consulted %d times, want 0 with an AuthSource", passwords.calls)
	}
	if source.calls != 1 || source.req.HostID != "host-1" {
		t.Errorf("AuthSource calls = %d for %+v", source.calls, source.req)
	}
	request := dialer.request()
	if request.Auth.Mode != AuthModeKey || len(request.Auth.Candidates) != 1 {
		t.Fatalf("forwarded plan = %+v", request.Auth)
	}
	if request.Prompter == nil {
		t.Fatal("the auth prompter was not forwarded")
	}
}

func TestAcquireCachesTheSuccessfulMethodAndClearsItOnFailure(t *testing.T) {
	source := &fakeAuthSource{plan: BuildAuthPlan(AuthSpec{
		Username: "root", Mode: AuthModeAuto, Password: "s3cret", UseAgent: true,
	})}
	manager, _, dialer, _, _ := newTestManager(t, WithAuthSource(source))
	dialer.authMethod = CandidatePassword

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()
	if method, ok := manager.authCache.Get("root@example.com:22"); !ok || method != CandidatePassword {
		t.Fatalf("cached method = %q, %v, want password", method, ok)
	}

	// Force a fresh dial each round: the parked transport is reused only for
	// an identical effective configuration, and this test exercises what a
	// later physical connection asks for.
	second := sshRequest()
	second.ConfigFingerprint = "round-2"
	source.plan = BuildAuthPlan(AuthSpec{
		Username: "root", Mode: AuthModeAuto, Password: "s3cret", UseAgent: true,
	})
	dialer.authMethod = CandidateAgent
	lease, err = manager.Acquire(context.Background(), second)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	_ = lease.Release()
	request := dialer.request()
	if request.Auth.Candidates[0].ID != CandidateAgent {
		t.Fatalf("first candidate = %q, want the agent", request.Auth.Candidates[0].ID)
	}
	if method, ok := manager.authCache.Get("root@example.com:22"); !ok || method != CandidateAgent {
		t.Fatalf("cached method = %q, %v, want agent", method, ok)
	}

	dialer.err = &Error{Kind: KindAuth, Cause: ErrAuthentication}
	third := sshRequest()
	third.ConfigFingerprint = "round-3"
	if _, err := manager.Acquire(context.Background(), third); err == nil {
		t.Fatal("Acquire succeeded, want an auth failure")
	}
	if _, ok := manager.authCache.Get("root@example.com:22"); ok {
		t.Fatal("the auth method cache was not cleared after a failed login")
	}
}

func TestAcquirePromotesACachedAgentOnlyInAutoMode(t *testing.T) {
	keyPlan := BuildAuthPlan(AuthSpec{
		Mode: AuthModeKey, Username: "root",
		SelectedKey: &KeyMaterial{ID: "key-1", Label: "work", PrivateKey: "material"},
	})
	source := &fakeAuthSource{plan: keyPlan}
	manager, _, dialer, _, _ := newTestManager(t, WithAuthSource(source))
	manager.authCache.Put("root@example.com:22", CandidateAgent)

	lease, err := manager.Acquire(context.Background(), sshRequest())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_ = lease.Release()
	request := dialer.request()
	if request.Auth.Candidates[0].ID != "publickey-user" {
		t.Fatalf("first candidate = %q, want the selected key in explicit key mode",
			request.Auth.Candidates[0].ID)
	}
}

func TestAuthSourceFailureKeepsTypedAuthErrors(t *testing.T) {
	source := &fakeAuthSource{err: AuthFailure(ErrNoCredentials)}
	manager, passwords, dialer, _, _ := newTestManager(t, WithAuthSource(source))

	_, err := manager.Acquire(context.Background(), sshRequest())
	assertKind(t, err, KindAuth)
	if !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("error = %v, want ErrNoCredentials", err)
	}
	if passwords.calls != 0 || dialer.authenticated() {
		t.Fatal("authentication proceeded despite an AuthSource failure")
	}
}

func assertKind(t *testing.T, err error, want Kind) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error %v is %T, want *connection.Error", err, err)
	}
	if typed.Kind != want {
		t.Fatalf("error kind = %q, want %q (%v)", typed.Kind, want, err)
	}
}
