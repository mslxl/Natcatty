package app

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// vncDialForConfig returns a dial seam that starts a fresh in-process RFB
// server for every connection, so each Open in a test gets its own stream.
func vncDialForConfig(t *testing.T, cfg rfbtest.Config) func(context.Context, string, string) (net.Conn, error) {
	t.Helper()
	return func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		serve := rfbtest.New(cfg)
		go func() { _ = serve.Serve(server) }()
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
		})
		return client, nil
	}
}

// vncTestSecrets records the Vault reads the app performs for a VNC Host.
type vncTestSecrets struct {
	mu    sync.Mutex
	value string
	err   error
	id    model.HostID
	field string
	reads int
}

func (s *vncTestSecrets) Secret(_ context.Context, id model.HostID, field string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	s.id = id
	s.field = field
	if s.err != nil {
		return "", s.err
	}
	return s.value, nil
}

// vncTestWarnings is the per-Host acknowledgement store.
type vncTestWarnings struct {
	mu     sync.Mutex
	acked  bool
	checks int
	acks   int
	errors error
}

func (w *vncTestWarnings) PlaintextWarningAcknowledged(context.Context, model.HostID) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.checks++
	return w.acked, w.errors
}

func (w *vncTestWarnings) AcknowledgePlaintextWarning(context.Context, model.HostID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.acks++
	if w.errors != nil {
		return w.errors
	}
	w.acked = true
	return nil
}

func (w *vncTestWarnings) state() (acked bool, checks, acks int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.acked, w.checks, w.acks
}

// vncPlaintextTestPrompter is a scripted pre-connect confirmation. It reports each
// request and waits for the test to answer, so a test can inspect the store
// between the request and the user's explicit action.
type vncPlaintextTestPrompter struct {
	requests chan string
	answers  chan bool
	mu       sync.Mutex
	calls    int
	ids      []model.HostID
}

func newVNCPlaintextTestPrompter() *vncPlaintextTestPrompter {
	return &vncPlaintextTestPrompter{
		requests: make(chan string, 1),
		answers:  make(chan bool, 1),
	}
}

func (p *vncPlaintextTestPrompter) ConfirmPlaintext(_ context.Context, id model.HostID, name string) (bool, error) {
	p.mu.Lock()
	p.calls++
	p.ids = append(p.ids, id)
	p.mu.Unlock()
	p.requests <- name
	if !<-p.answers {
		return false, sessionui.ErrPlaintextDeclined
	}
	return true, nil
}

func (p *vncPlaintextTestPrompter) state() (calls int, ids []model.HostID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, append([]model.HostID(nil), p.ids...)
}

// TestVNCResolvesTheSavedPasswordAndUsername proves the app reads the
// dedicated VNC secret slot and carries the Host username into the start
// section.
func TestVNCResolvesTheSavedPasswordAndUsername(t *testing.T) {
	secrets := &vncTestSecrets{value: "vnc-pass-15"}
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: vncDialForConfig(t, rfbtest.Config{Width: 2, Height: 2, Password: "vnc-pass-15"}),
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withVNCPasswords(secrets)

	transport, err := factory.Open(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })

	secrets.mu.Lock()
	id, field, reads := secrets.id, secrets.field, secrets.reads
	secrets.mu.Unlock()
	if reads != 1 || id != "vnc-desk" || field != appvault.FieldVNCPassword {
		t.Fatalf("secret lookup = %d for %q/%q, want the VNC password slot", reads, id, field)
	}

	spec, err := factory.withCredentials(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("withCredentials: %v", err)
	}
	if spec.VNC == nil || spec.VNC.Username != "viewer" {
		t.Fatalf("VNC section = %+v, want the Host username", spec.VNC)
	}
}

// TestVNCWrongPasswordIsPermanent proves a saved-but-wrong credential is a
// typed authentication failure the Session must not retry.
func TestVNCWrongPasswordIsPermanent(t *testing.T) {
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: vncDialForConfig(t, rfbtest.Config{Width: 2, Height: 2, Password: "right-pass"}),
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withVNCPasswords(&vncTestSecrets{value: "wrong-pass"})

	_, err := factory.Open(context.Background(), vncStartSpec())
	if !errors.Is(err, rfb.ErrSecurityRejected) {
		t.Fatalf("Open = %v, want ErrSecurityRejected", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth || !typed.Permanent() {
		t.Fatalf("Open = %v, want a permanent KindAuth failure", err)
	}
}

// TestVNCMissingPasswordNamesTheEditorField proves a server that requires a
// password with no saved secret reports the missing credential and where to
// set it.
func TestVNCMissingPasswordNamesTheEditorField(t *testing.T) {
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: vncDialForConfig(t, rfbtest.Config{Width: 2, Height: 2, Password: "required"}),
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withVNCPasswords(&vncTestSecrets{err: appvault.ErrNotFound})

	_, err := factory.Open(context.Background(), vncStartSpec())
	if !errors.Is(err, rfb.ErrMissingPassword) {
		t.Fatalf("Open = %v, want ErrMissingPassword", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindAuth || !typed.Permanent() {
		t.Fatalf("Open = %v, want a permanent KindAuth failure", err)
	}
	if !strings.Contains(err.Error(), "VNC Password field") {
		t.Fatalf("Open = %v, want the editor field named", err)
	}
}

// TestVNCSecretStoreFailureIsInternal proves a locked or failing store is not
// reported as a wrong password.
func TestVNCSecretStoreFailureIsInternal(t *testing.T) {
	dialed := false
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withVNCPasswords(&vncTestSecrets{err: errors.New("vault locked")})

	_, err := factory.Open(context.Background(), vncStartSpec())
	if dialed {
		t.Fatal("a failed secret read still dialed")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindInternal {
		t.Fatalf("Open = %v, want KindInternal", err)
	}
}

// TestVNCPlaintextConfirmationRecordsAcknowledgmentOnContinue proves the
// unrouted confirmation is asked before dialing, the acknowledgement is NOT
// written while the user has not answered, and an explicit Continue persists
// it so the next connect is silent.
func TestVNCPlaintextConfirmationRecordsAcknowledgmentOnContinue(t *testing.T) {
	warnings := &vncTestWarnings{}
	prompt := newVNCPlaintextTestPrompter()
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: vncDialForConfig(t, rfbtest.Config{Width: 2, Height: 2}),
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withPlaintextWarnings(warnings).
		withPlaintextPrompt(prompt)

	type result struct {
		transport appsession.Transport
		err       error
	}
	done := make(chan result, 1)
	go func() {
		transport, err := factory.Open(context.Background(), vncStartSpec())
		done <- result{transport: transport, err: err}
	}()

	select {
	case name := <-prompt.requests:
		if name != "desk.local" {
			t.Fatalf("prompt named %q, want the Host endpoint", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the plaintext confirmation")
	}
	// The user has not acted yet: no acknowledgement may be recorded.
	if acked, checks, acks := warnings.state(); acked || acks != 0 {
		t.Fatalf("warning store = acked %v, checks %d, acks %d before the user acted", acked, checks, acks)
	}

	prompt.answers <- true
	first := <-done
	if first.err != nil {
		t.Fatalf("Open after Continue: %v", first.err)
	}
	if _, ok := first.transport.(appsession.Notifier); ok {
		t.Fatal("a confirmed plaintext connect still published a warning notice")
	}
	_ = first.transport.Close()
	if acked, _, acks := warnings.state(); !acked || acks != 1 {
		t.Fatalf("warning store = acked %v, acks %d; want the explicit Continue recorded once", acked, acks)
	}
	if calls, ids := prompt.state(); calls != 1 || len(ids) != 1 || ids[0] != "vnc-desk" {
		t.Fatalf("prompt = %d calls for %v, want one for vnc-desk", calls, ids)
	}

	second, err := factory.Open(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if _, ok := second.(appsession.Notifier); ok {
		t.Fatal("the second plaintext connect warned again")
	}
	if calls, _ := prompt.state(); calls != 1 {
		t.Fatalf("prompt calls = %d, want the recorded acknowledgement to skip it", calls)
	}
	if _, checks, _ := warnings.state(); checks != 2 {
		t.Fatalf("warning checks = %d, want the acknowledgement consulted once more", checks)
	}
}

// TestVNCPlaintextConfirmationCancelAbortsWithoutAcknowledging proves Cancel
// neither dials nor records the acknowledgement.
func TestVNCPlaintextConfirmationCancelAbortsWithoutAcknowledging(t *testing.T) {
	warnings := &vncTestWarnings{}
	prompt := newVNCPlaintextTestPrompter()
	dialed := false
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withPlaintextWarnings(warnings).
		withPlaintextPrompt(prompt)

	errs := make(chan error, 1)
	go func() {
		_, err := factory.Open(context.Background(), vncStartSpec())
		errs <- err
	}()

	select {
	case <-prompt.requests:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the plaintext confirmation")
	}
	prompt.answers <- false
	err := <-errs
	if !errors.Is(err, sessionui.ErrPlaintextDeclined) {
		t.Fatalf("Open after Cancel = %v, want ErrPlaintextDeclined", err)
	}
	if dialed {
		t.Fatal("Cancel dialed the server")
	}
	if acked, _, acks := warnings.state(); acked || acks != 0 {
		t.Fatalf("warning store = acked %v, acks %d; Cancel must not write", acked, acks)
	}
}

// TestVNCPlaintextNoticeWithoutPromptNeverAcknowledges proves the fallback
// post-connect notice does not record consent on the user's behalf.
func TestVNCPlaintextNoticeWithoutPromptNeverAcknowledges(t *testing.T) {
	warnings := &vncTestWarnings{}
	factory := newVNCFactory(adaptervnc.Factory{
		Dial: vncDialForConfig(t, rfbtest.Config{Width: 2, Height: 2}),
	}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
		withPlaintextWarnings(warnings)

	for attempt := 1; attempt <= 2; attempt++ {
		transport, err := factory.Open(context.Background(), vncStartSpec())
		if err != nil {
			t.Fatalf("Open %d: %v", attempt, err)
		}
		notifier, ok := transport.(appsession.Notifier)
		if !ok {
			t.Fatalf("Open %d published no fallback notice", attempt)
		}
		select {
		case notice, open := <-notifier.Notices():
			if !open || notice.Code != appsession.NoticeVNCPlaintext {
				t.Fatalf("notice = %+v, want %q", notice, appsession.NoticeVNCPlaintext)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the plaintext notice")
		}
		_ = transport.Close()
	}
	if acked, checks, acks := warnings.state(); acked || acks != 0 {
		t.Fatalf("warning store = acked %v, checks %d, acks %d; the fallback notice must not acknowledge", acked, checks, acks)
	}
}

// TestVNCRoutedHostNeverWarns proves a routed Host suppresses the warning even
// when the inner RFB type is plaintext.
func TestVNCRoutedHostNeverWarns(t *testing.T) {
	warnings := &vncTestWarnings{}
	conn, err := vncDialForConfig(t, rfbtest.Config{Width: 2, Height: 2})(
		context.Background(), "tcp", "desk.local:5901")
	if err != nil {
		t.Fatalf("start test server: %v", err)
	}
	provider := &vncTestProvider{conn: conn}
	registry := appconnection.NewNetworkProviderRegistry(&vncTestNetworkFactory{provider: provider})
	routes := &vncTestRoutes{route: appconnection.Route{Network: &appconnection.NetworkRoute{
		ProfileID: "net-desktop", Kind: "tailscale", Fingerprint: "fp-vnc",
	}}}
	factory := newVNCFactory(adaptervnc.Factory{}, routes, registry, nil).
		withPlaintextWarnings(warnings)

	transport, err := factory.Open(context.Background(), vncStartSpec())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	if !vncRoutedDecision(t, transport) {
		t.Fatal("a routed VNC Host was not marked as routed")
	}
	if _, ok := transport.(appsession.Notifier); ok {
		t.Fatal("a routed Host published the plaintext warning")
	}
	if _, checks, acks := warnings.state(); checks != 0 || acks != 0 {
		t.Fatalf("warning store = checks %d, acks %d; a routed Host must not consult it", checks, acks)
	}
}
