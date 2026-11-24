package app

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	adaptervnc "github.com/mslxl/natcatty/adapters/vnc"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// vncTestTLSConfig builds a self-signed server certificate for the anonymous
// VeNCrypt TLS subtypes, which do not verify it.
func vncTestTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("TLS key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "desk.local"},
		DNSNames:     []string{"desk.local"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("TLS certificate: %v", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
}

// TestVNCEncryptedSecurityNeverWarns proves an unrouted Host that negotiates a
// session-encrypting type connects without consulting the warning store or the
// prompt. VeNCrypt TLSPlain and RA2 cover the two encryption layers.
func TestVNCEncryptedSecurityNeverWarns(t *testing.T) {
	cases := []struct {
		name string
		cfg  rfbtest.Config
	}{
		{
			name: "RA2",
			cfg: rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes: []rfb.SecurityType{rfb.SecurityRA2},
				Username:      "viewer",
				Password:      "hysu-vnra",
			},
		},
		{
			name: "VeNCrypt TLSPlain",
			cfg: rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes:    []rfb.SecurityType{rfb.SecurityVeNCrypt},
				VeNCryptSubtypes: []rfb.VeNCryptSubtype{rfb.VeNCryptTLSPlain},
				TLSConfig:        vncTestTLSConfig(t),
				Username:         "viewer",
				Password:         "hysu-vnra",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := &vncTestWarnings{}
			prompt := newVNCPlaintextTestPrompter()
			factory := newVNCFactory(adaptervnc.Factory{
				Dial: vncDialForConfig(t, tc.cfg),
			}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
				withPlaintextWarnings(warnings).
				withPlaintextPrompt(prompt).
				withVNCPasswords(&vncTestSecrets{value: "hysu-vnra"})

			transport, err := factory.Open(context.Background(), vncStartSpec())
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			t.Cleanup(func() { _ = transport.Close() })
			if _, ok := transport.(appsession.Notifier); ok {
				t.Fatal("an encrypted negotiation published a warning")
			}
			if reporter, ok := transport.(interface{ Plaintext() bool }); !ok || reporter.Plaintext() {
				t.Fatal("the encrypted transport reports a plaintext stream")
			}
			if acked, checks, acks := warnings.state(); acked || checks != 0 || acks != 0 {
				t.Fatalf("warning store = acked %v, checks %d, acks %d; an encrypted type must not consult it", acked, checks, acks)
			}
			if calls, _ := prompt.state(); calls != 0 {
				t.Fatalf("prompt calls = %d, want none", calls)
			}
		})
	}
}

// TestVNCRoutedOnlyTLSFailsClosedWithRouteHint proves a routed Host whose
// server offers only TLS-requiring VeNCrypt subtypes fails with the typed
// unsupported error and the SSH-route hint, without starting a second TLS
// handshake or sending a credential.
func TestVNCRoutedOnlyTLSFailsClosedWithRouteHint(t *testing.T) {
	warnings := &vncTestWarnings{}
	prompt := newVNCPlaintextTestPrompter()
	server := rfbtest.New(rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{
			rfb.VeNCryptTLSPlain, rfb.VeNCryptX509Plain,
		},
		TLSConfig: vncTestTLSConfig(t),
		Username:  "viewer",
		Password:  "hysu-vnra",
	})
	client, peer := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = peer.Close()
	})
	go func() { _ = server.Serve(peer) }()

	registry := appconnection.NewNetworkProviderRegistry(&vncTestNetworkFactory{
		provider: &vncTestProvider{conn: client},
	})
	routes := &vncTestRoutes{route: appconnection.Route{Network: &appconnection.NetworkRoute{
		ProfileID: "net-desktop", Kind: "tailscale", Fingerprint: "fp-vnc",
	}}}
	factory := newVNCFactory(adaptervnc.Factory{}, routes, registry, nil).
		withPlaintextWarnings(warnings).
		withPlaintextPrompt(prompt).
		withVNCPasswords(&vncTestSecrets{value: "hysu-vnra"})

	_, err := factory.Open(context.Background(), vncStartSpec())
	if !errors.Is(err, rfb.ErrUnsupportedSecurity) {
		t.Fatalf("Open = %v, want ErrUnsupportedSecurity", err)
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("Open = %v, want KindUnsupported", err)
	}
	if !strings.Contains(err.Error(), "SSH route") {
		t.Fatalf("Open = %v, want the SSH-route hint", err)
	}
	if acked, checks, acks := warnings.state(); acked || checks != 0 || acks != 0 {
		t.Fatalf("warning store = acked %v, checks %d, acks %d; a routed Host must not consult it", acked, checks, acks)
	}
	if calls, _ := prompt.state(); calls != 0 {
		t.Fatalf("prompt calls = %d, want none", calls)
	}
	received := server.Received()
	if received.TLSStarted {
		t.Fatal("a routed connection started an inner TLS handshake")
	}
	if received.Username != "" || received.Password != "" {
		t.Fatal("a routed connection sent credentials after failing closed")
	}
}

// TestVNCPlaintextAuthenticatedTypesStillWarn proves the corrected
// classification keeps the warning for RA2ne and Apple DH, which authenticate
// but do not encrypt the session.
func TestVNCPlaintextAuthenticatedTypesStillWarn(t *testing.T) {
	cases := []struct {
		name string
		cfg  rfbtest.Config
	}{
		{
			name: "RA2ne",
			cfg: rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes: []rfb.SecurityType{rfb.SecurityRA2ne},
				Username:      "viewer",
				Password:      "hysu-vnra",
			},
		},
		{
			name: "Apple DH",
			cfg: rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes: []rfb.SecurityType{rfb.SecurityAppleDH},
				Username:      "viewer",
				Password:      "hysu-vnra",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings := &vncTestWarnings{}
			prompt := newVNCPlaintextTestPrompter()
			server := rfbtest.New(tc.cfg)
			client, peer := net.Pipe()
			served := make(chan error, 1)
			go func() { served <- server.Serve(peer) }()
			t.Cleanup(func() {
				_ = client.Close()
				_ = peer.Close()
			})
			factory := newVNCFactory(adaptervnc.Factory{
				Dial: func(context.Context, string, string) (net.Conn, error) {
					return client, nil
				},
			}, &vncTestRoutes{}, appconnection.NewNetworkProviderRegistry(nil), nil).
				withPlaintextWarnings(warnings).
				withPlaintextPrompt(prompt).
				withVNCPasswords(&vncTestSecrets{value: "hysu-vnra"})

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
			// Cancel before any credential reaches the server.
			prompt.answers <- false
			if err := <-errs; !errors.Is(err, sessionui.ErrPlaintextDeclined) {
				t.Fatalf("Open after Cancel = %v, want ErrPlaintextDeclined", err)
			}
			if acked, _, acks := warnings.state(); acked || acks != 0 {
				t.Fatalf("warning store = acked %v, acks %d; Cancel must not write", acked, acks)
			}
			// Wait for the server to see the closed stream, then prove the
			// cancelled handshake sent no credential of any kind.
			select {
			case <-served:
			case <-time.After(5 * time.Second):
				t.Fatal("the server did not observe the cancelled stream")
			}
			received := server.Received()
			if received.AppleDHUsername != "" || received.AppleDHAccepted {
				t.Fatalf("Cancel sent Apple DH credentials: %+v", received)
			}
			if received.RA2Username != "" || received.RA2Password != "" || received.RA2Accepted {
				t.Fatalf("Cancel sent RA2 credentials: %+v", received)
			}
			if received.Username != "" || received.Password != "" || received.VNCAuthAccepted {
				t.Fatalf("Cancel sent a credential: %+v", received)
			}
			if len(received.Fences) != 0 {
				t.Fatalf("Cancel sent fence messages: %+v", received.Fences)
			}
		})
	}
}
