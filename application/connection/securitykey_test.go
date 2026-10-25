package connection

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSecurityKeyAgent is the in-memory port implementation for application
// tests. The adapter tests drive the real agent wire protocol instead.
type fakeSecurityKeyAgent struct {
	keys    []SecurityKey
	listErr error
	sign    func(ctx context.Context, key SecurityKey, data []byte) (SecurityKeySignature, error)
	calls   int
}

func (a *fakeSecurityKeyAgent) SecurityKeys(context.Context) ([]SecurityKey, error) {
	if a.listErr != nil {
		return nil, a.listErr
	}
	return append([]SecurityKey(nil), a.keys...), nil
}

func (a *fakeSecurityKeyAgent) SignSecurityKey(ctx context.Context, key SecurityKey, data []byte) (SecurityKeySignature, error) {
	a.calls++
	return a.sign(ctx, key, data)
}

func TestSecurityKeyDeviceDeadlineIsThirtySeconds(t *testing.T) {
	if SecurityKeyDeviceDeadline != 30*time.Second {
		t.Fatalf("SecurityKeyDeviceDeadline = %s, want 30s", SecurityKeyDeviceDeadline)
	}
}

func TestSelectSecurityKeysKeepsOnlySKAlgorithmsInAgentOrder(t *testing.T) {
	keys := []SecurityKey{
		{Algorithm: "ssh-ed25519", Blob: []byte("software")},
		{Algorithm: SecurityKeyAlgorithmECDSA, Blob: []byte("ecdsa-sk"), Comment: "primary"},
		{Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("ed25519-sk")},
		{Algorithm: SecurityKeyAlgorithmEd25519},
	}
	selected := SelectSecurityKeys(keys)
	if len(selected) != 2 {
		t.Fatalf("selected %d keys, want 2: %+v", len(selected), selected)
	}
	if selected[0].Algorithm != SecurityKeyAlgorithmECDSA || string(selected[0].Blob) != "ecdsa-sk" ||
		selected[0].Comment != "primary" {
		t.Fatalf("first key = %+v, want the ECDSA-SK identity in agent order", selected[0])
	}
	if selected[1].Algorithm != SecurityKeyAlgorithmEd25519 || string(selected[1].Blob) != "ed25519-sk" {
		t.Fatalf("second key = %+v, want the Ed25519-SK identity", selected[1])
	}

	// The selection copies the blob so later agent mutations cannot reach it.
	keys[1].Blob[0] = 'X'
	if string(selected[0].Blob) != "ecdsa-sk" {
		t.Fatal("the selection shares the agent's blob buffer")
	}
}

func TestSecurityKeyServiceMissingAgentIsTypedUnsupported(t *testing.T) {
	service := SecurityKeyService{}
	_, err := service.Identities(context.Background())
	assertSecurityKeyError(t, err, KindUnsupported, ErrSecurityKeyUnsupported)

	service = SecurityKeyService{Agent: &fakeSecurityKeyAgent{listErr: ErrSecurityKeyUnsupported}}
	_, err = service.Identities(context.Background())
	assertSecurityKeyError(t, err, KindUnsupported, ErrSecurityKeyUnsupported)

	// A signing attempt with no agent at all keeps the same category.
	service = SecurityKeyService{}
	_, err = service.Sign(context.Background(), SecurityKey{
		Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("blob"),
	}, []byte("challenge"))
	assertSecurityKeyError(t, err, KindUnsupported, ErrSecurityKeyUnsupported)
}

func TestSecurityKeyServiceSignEnforcesTheDeviceDeadline(t *testing.T) {
	agent := &fakeSecurityKeyAgent{sign: func(ctx context.Context, _ SecurityKey, _ []byte) (SecurityKeySignature, error) {
		<-ctx.Done()
		return SecurityKeySignature{}, ctx.Err()
	}}
	service := SecurityKeyService{Agent: agent, Deadline: 40 * time.Millisecond}

	started := time.Now()
	_, err := service.Sign(context.Background(), SecurityKey{
		Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("blob"),
	}, []byte("challenge"))
	elapsed := time.Since(started)

	assertSecurityKeyError(t, err, KindTimeout, ErrSecurityKeyDeviceTimeout)
	if elapsed < 30*time.Millisecond {
		t.Fatalf("sign returned after %s, before the injected deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("sign returned after %s; the deadline did not bound the request", elapsed)
	}
}

func TestSecurityKeyServiceSignPreservesTheAgentSignatureBytes(t *testing.T) {
	raw := []byte{0, 0, 0, 25, 's', 'k', '-', 's', 's', 'h'}
	agent := &fakeSecurityKeyAgent{sign: func(context.Context, SecurityKey, []byte) (SecurityKeySignature, error) {
		return SecurityKeySignature{Blob: raw}, nil
	}}
	service := SecurityKeyService{Agent: agent}

	got, err := service.Sign(context.Background(), SecurityKey{
		Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("blob"),
	}, []byte("challenge"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if string(got.Blob) != string(raw) {
		t.Fatalf("signature blob = %v, want the agent's exact bytes %v", got.Blob, raw)
	}
	raw[0] = 0xff
	if got.Blob[0] == 0xff {
		t.Fatal("the signature result shares the agent's buffer")
	}
}

func TestSecurityKeyServiceRejectsMalformedMaterial(t *testing.T) {
	agent := &fakeSecurityKeyAgent{sign: func(context.Context, SecurityKey, []byte) (SecurityKeySignature, error) {
		return SecurityKeySignature{}, nil
	}}
	service := SecurityKeyService{Agent: agent}

	_, err := service.Sign(context.Background(), SecurityKey{Algorithm: "ssh-ed25519", Blob: []byte("x")}, nil)
	assertSecurityKeyError(t, err, KindInternal, ErrSecurityKeyProtocol)

	_, err = service.Sign(context.Background(), SecurityKey{
		Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("x"),
	}, []byte("challenge"))
	assertSecurityKeyError(t, err, KindInternal, ErrSecurityKeyProtocol)
}

func TestSecurityKeyServiceCancelKeepsTheCancelledKind(t *testing.T) {
	agent := &fakeSecurityKeyAgent{sign: func(ctx context.Context, _ SecurityKey, _ []byte) (SecurityKeySignature, error) {
		<-ctx.Done()
		return SecurityKeySignature{}, ctx.Err()
	}}
	service := SecurityKeyService{Agent: agent, Deadline: time.Minute}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.Sign(ctx, SecurityKey{
		Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("blob"),
	}, []byte("challenge"))
	var typed *Error
	if !errors.As(err, &typed) || typed.Kind != KindCancelled {
		t.Fatalf("Sign error = %v, want KindCancelled", err)
	}
}

func TestSecurityKeySummaryReportsAgentState(t *testing.T) {
	agent := &fakeSecurityKeyAgent{keys: []SecurityKey{
		{Algorithm: "ssh-ed25519", Blob: []byte("software")},
		{Algorithm: SecurityKeyAlgorithmEd25519, Blob: []byte("hardware")},
	}}
	service := SecurityKeyService{Agent: agent}
	summary := service.Summary(context.Background())
	if !summary.Available || summary.Count != 1 {
		t.Fatalf("summary = %+v, want available with one SK identity", summary)
	}

	service = SecurityKeyService{Agent: &fakeSecurityKeyAgent{listErr: ErrSecurityKeyUnsupported}}
	if summary := service.Summary(context.Background()); summary.Available || summary.Count != 0 {
		t.Fatalf("summary of a missing agent = %+v, want unavailable", summary)
	}
}

func assertSecurityKeyError(t *testing.T, err error, kind Kind, sentinel error) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want a typed connection error", err)
	}
	if typed.Kind != kind {
		t.Fatalf("error kind = %q, want %q (%v)", typed.Kind, kind, err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap %v", err, sentinel)
	}
}
