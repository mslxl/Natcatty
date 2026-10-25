package ssh

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	xssh "golang.org/x/crypto/ssh"

	"github.com/mslxl/natcatty/adapters/sshtest/fakeagent"
	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

func securityKeyServiceFor(agent *fakeagent.Agent) appconnection.SecurityKeyService {
	return appconnection.SecurityKeyService{
		Agent: NewSecurityKeyAgent(WithSecurityKeyAgentDial(agent.Dial)),
	}
}

func TestSecurityKeyAgentListsIdentityBlobsUnchanged(t *testing.T) {
	hardware := fakeagent.Ed25519SK("yubikey")
	software := fakeagent.SoftwareEd25519("work")
	agent := fakeagent.New(hardware, software)
	provider := NewSecurityKeyAgent(WithSecurityKeyAgentDial(agent.Dial))

	keys, err := provider.SecurityKeys(context.Background())
	if err != nil {
		t.Fatalf("SecurityKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("identities = %d, want 2 (the adapter reports every identity)", len(keys))
	}
	if keys[0].Algorithm != appconnection.SecurityKeyAlgorithmEd25519 ||
		keys[0].Comment != "yubikey" ||
		!bytes.Equal(keys[0].Blob, hardware.Blob) {
		t.Fatalf("SK identity = %+v, want the agent's exact blob", keys[0])
	}
	if keys[1].Algorithm != "ssh-ed25519" || !bytes.Equal(keys[1].Blob, software.Blob) {
		t.Fatalf("software identity = %+v, want the agent's exact blob", keys[1])
	}

	selected, err := securityKeyServiceFor(agent).Identities(context.Background())
	if err != nil {
		t.Fatalf("Identities: %v", err)
	}
	if len(selected) != 1 || !bytes.Equal(selected[0].Blob, hardware.Blob) {
		t.Fatalf("selected = %+v, want only the hardware identity", selected)
	}
}

// TestSecurityKeyAgentPreservesFramingVectors signs with both SK algorithms and
// checks the exact signature framing: algorithm string, raw signature, the
// user-presence flag, and a non-trivial counter. It also verifies the signature
// with x/crypto's own SK verifier, which recomputes SHA256(application) from
// the public blob, so a lost application string or flag fails the test.
func TestSecurityKeyAgentPreservesFramingVectors(t *testing.T) {
	cases := []struct {
		name        string
		identity    fakeagent.Identity
		application string
	}{
		{
			name:        "ed25519",
			identity:    fakeagent.Ed25519SKWithApplication("hardware", "ssh:example"),
			application: "ssh:example",
		},
		{name: "ecdsa", identity: fakeagent.ECDSASK("hardware"), application: "ssh:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := fakeagent.New(tc.identity)
			service := securityKeyServiceFor(agent)
			keys, err := service.Identities(context.Background())
			if err != nil {
				t.Fatalf("Identities: %v", err)
			}
			if len(keys) != 1 {
				t.Fatalf("identities = %d, want 1", len(keys))
			}
			if !bytes.Contains(keys[0].Blob, []byte(tc.application)) {
				t.Fatalf("public blob lost the application string %q", tc.application)
			}
			if !bytes.Equal(keys[0].Blob, tc.identity.Blob) {
				t.Fatal("the public key blob was re-encoded")
			}

			challenge := []byte("session challenge")
			result, err := service.Sign(context.Background(), keys[0], challenge)
			if err != nil {
				t.Fatalf("Sign: %v", err)
			}

			var wire struct {
				Format string
				Blob   []byte
				Rest   []byte `ssh:"rest"`
			}
			if err := xssh.Unmarshal(result.Blob, &wire); err != nil {
				t.Fatalf("signature framing: %v", err)
			}
			if wire.Format != tc.identity.Algorithm {
				t.Fatalf("signature format = %q, want %q", wire.Format, tc.identity.Algorithm)
			}
			wantTail := []byte{0x01, 0, 0, 0, 7}
			if !bytes.Equal(wire.Rest, wantTail) {
				t.Fatalf("signature tail = %v, want %v (user presence + counter)", wire.Rest, wantTail)
			}

			parsed, err := ParseSecurityKeySignature(result.Blob)
			if err != nil {
				t.Fatalf("ParseSecurityKeySignature: %v", err)
			}
			if !bytes.Equal(xssh.Marshal(parsed), result.Blob) {
				t.Fatal("the parsed signature does not marshal back to the agent's bytes")
			}
			public, err := xssh.ParsePublicKey(keys[0].Blob)
			if err != nil {
				t.Fatalf("parse public key: %v", err)
			}
			if err := public.Verify(challenge, parsed); err != nil {
				t.Fatalf("the signature does not verify with the application string and flags preserved: %v", err)
			}

			requests := agent.SignRequests()
			if len(requests) != 1 {
				t.Fatalf("sign requests = %d, want 1", len(requests))
			}
			if !bytes.Equal(requests[0].KeyBlob, tc.identity.Blob) {
				t.Fatal("the sign request did not carry the agent's exact key blob")
			}
			if !bytes.Equal(requests[0].Data, challenge) {
				t.Fatal("the sign request did not carry the challenge")
			}
			if requests[0].Flags != 0 {
				t.Fatalf("sign request flags = %d, want 0 (the agent decides user presence)", requests[0].Flags)
			}
		})
	}
}

func TestParseSecurityKeySignatureRejectsBrokenFraming(t *testing.T) {
	agent := fakeagent.Default()
	service := securityKeyServiceFor(agent)
	keys, err := service.Identities(context.Background())
	if err != nil {
		t.Fatalf("Identities: %v", err)
	}
	result, err := service.Sign(context.Background(), keys[0], []byte("challenge"))
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	parsed, err := ParseSecurityKeySignature(result.Blob)
	if err != nil {
		t.Fatalf("ParseSecurityKeySignature: %v", err)
	}
	wrongFormat := appendAgentString(nil, []byte("ssh-ed25519"))
	wrongFormat = appendAgentString(wrongFormat, parsed.Blob)
	wrongFormat = append(wrongFormat, parsed.Rest...)

	cases := map[string][]byte{
		"short tail":      result.Blob[:len(result.Blob)-1],
		"missing tail":    result.Blob[:len(result.Blob)-5],
		"wrong algorithm": wrongFormat,
	}
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSecurityKeySignature(blob)
			var typed *appconnection.Error
			if !errors.As(err, &typed) || typed.Kind != appconnection.KindInternal {
				t.Fatalf("error = %v, want a typed internal protocol failure", err)
			}
			if !errors.Is(err, appconnection.ErrSecurityKeyProtocol) {
				t.Fatalf("error = %v, want ErrSecurityKeyProtocol", err)
			}
		})
	}
}

func TestSecurityKeyAgentMissingOpenSSHIsTypedUnsupported(t *testing.T) {
	unavailable := func(context.Context) (net.Conn, error) { return nil, ErrAgentUnavailable }
	service := appconnection.SecurityKeyService{
		Agent: NewSecurityKeyAgent(WithSecurityKeyAgentDial(unavailable)),
	}

	_, err := service.Identities(context.Background())
	assertTypedSecurityKeyError(t, err, appconnection.KindUnsupported, appconnection.ErrSecurityKeyUnsupported)

	_, err = service.Sign(context.Background(), appconnection.SecurityKey{
		Algorithm: appconnection.SecurityKeyAlgorithmEd25519,
		Blob:      []byte("blob"),
	}, []byte("challenge"))
	assertTypedSecurityKeyError(t, err, appconnection.KindUnsupported, appconnection.ErrSecurityKeyUnsupported)
}

func TestSecurityKeyAgentRefusedSigningStaysAnAuthFailure(t *testing.T) {
	agent := fakeagent.NewWithOptions(fakeagent.WithIdentity(fakeagent.Ed25519SK("hardware")), fakeagent.WithFailSign())
	service := securityKeyServiceFor(agent)
	keys, err := service.Identities(context.Background())
	if err != nil {
		t.Fatalf("Identities: %v", err)
	}

	_, err = service.Sign(context.Background(), keys[0], []byte("challenge"))
	assertTypedSecurityKeyError(t, err, appconnection.KindAuth, nil)
	if errors.Is(err, appconnection.ErrSecurityKeyUnsupported) {
		t.Fatal("a refused signature was reported as a missing agent")
	}
}

func TestDialAuthenticatesWithTheSecurityKeyAgent(t *testing.T) {
	agent := fakeagent.Default()
	server, dialFunc := startFixture(t, fakessh.Options{PublicKeys: agent.PublicKeys()})
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeAuto, UseAgent: true,
	})

	client, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{}, WithAgentDial(agent.Dial))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if server.PublicKeyAttempts() == 0 {
		t.Fatal("the server saw no publickey attempt")
	}
	if keyType, _ := server.LastPublicKey(); keyType != appconnection.SecurityKeyAlgorithmEd25519 {
		t.Fatalf("the server saw key type %q, want the SK algorithm", keyType)
	}
	requests := agent.SignRequests()
	if len(requests) != 1 {
		t.Fatalf("sign requests = %d, want 1", len(requests))
	}
	if requests[0].Flags != 0 {
		t.Fatalf("agent sign request flags = %d, want 0", requests[0].Flags)
	}
	if len(requests[0].Data) == 0 {
		t.Fatal("the agent signed an empty challenge")
	}
}

func TestDialWithoutOpenSSHReturnsTypedUnsupported(t *testing.T) {
	agent := fakeagent.Default()
	_, dialFunc := startFixture(t, fakessh.Options{PublicKeys: agent.PublicKeys()})
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeAuto, UseAgent: true,
	})

	_, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{},
		WithAgentDial(func(context.Context) (net.Conn, error) {
			return nil, ErrAgentUnavailable
		}))
	assertTypedSecurityKeyError(t, err, appconnection.KindUnsupported, appconnection.ErrSecurityKeyUnsupported)
}

func TestDialSecurityKeyDeviceDeadlineIsTypedTimeout(t *testing.T) {
	agent := fakeagent.NewWithOptions(fakeagent.WithIdentity(fakeagent.Ed25519SK("hardware")), fakeagent.WithBlockSign())
	_, dialFunc := startFixture(t, fakessh.Options{PublicKeys: agent.PublicKeys()})
	plan := appconnection.BuildAuthPlan(appconnection.AuthSpec{
		Username: "alice", Mode: appconnection.AuthModeAuto, UseAgent: true,
	})

	const deadline = 80 * time.Millisecond
	started := time.Now()
	_, err := dialWithPlan(t, dialFunc, plan, &fakeAuthPrompter{},
		WithAgentDial(agent.Dial), WithSecurityKeyDeadline(deadline))
	elapsed := time.Since(started)

	assertTypedSecurityKeyError(t, err, appconnection.KindTimeout, appconnection.ErrSecurityKeyDeviceTimeout)
	if elapsed < deadline/2 {
		t.Fatalf("Dial returned after %s, before the injected device deadline", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Dial returned after %s; the device deadline did not bound the request", elapsed)
	}
	if len(agent.SignRequests()) != 1 {
		t.Fatalf("sign requests = %d, want the blocked request", len(agent.SignRequests()))
	}
}

func assertTypedSecurityKeyError(t *testing.T, err error, kind appconnection.Kind, sentinel error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) {
		t.Fatalf("error = %v, want a typed connection error", err)
	}
	if typed.Kind != kind {
		t.Fatalf("error kind = %q, want %q (%v)", typed.Kind, kind, err)
	}
	if sentinel != nil && !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap %v", err, sentinel)
	}
}
