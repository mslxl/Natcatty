package rfb_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mslxl/natcatty/domain/model/rfb"
	"github.com/mslxl/natcatty/domain/model/rfb/rfbtest"
)

const strongTestPassword = "vencrypt-pass"

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(strings.ReplaceAll(value, " ", ""))
	if err != nil {
		t.Fatalf("hex %q: %v", value, err)
	}
	return decoded
}

// TestAESCMACReferenceVectors checks AES-CMAC against the four examples in RFC
// 4493 section 4.
func TestAESCMACReferenceVectors(t *testing.T) {
	key := mustHex(t, "2b7e1516 28aed2a6 abf71588 09cf4f3c")
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "empty",
			message: "",
			want:    "bb1d6929 e9593728 7fa37d12 9b756746",
		},
		{
			name:    "one block",
			message: "6bc1bee2 2e409f96 e93d7e11 7393172a",
			want:    "070a16b4 6b4d4144 f79bdd9d d04a287c",
		},
		{
			name: "40 bytes",
			message: "6bc1bee2 2e409f96 e93d7e11 7393172a" +
				"ae2d8a57 1e03ac9c 9eb76fac 45af8e51" +
				"30c81c46 a35ce411",
			want: "dfa66747 de9ae630 30ca3261 1497c827",
		},
		{
			name: "64 bytes",
			message: "6bc1bee2 2e409f96 e93d7e11 7393172a" +
				"ae2d8a57 1e03ac9c 9eb76fac 45af8e51" +
				"30c81c46 a35ce411 e5fbc119 1a0a52ef" +
				"f69f2445 df4f9b17 ad2b417b e66c3710",
			want: "51f0bebf 7e3b9d92 fc497417 79363cfe",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := rfb.AESCMAC(key, mustHex(t, tc.message))
			if err != nil {
				t.Fatalf("AESCMAC: %v", err)
			}
			if hex.EncodeToString(got[:]) != strings.ReplaceAll(tc.want, " ", "") {
				t.Fatalf("AESCMAC = %x, want %s", got, tc.want)
			}
		})
	}
}

// TestAESEAXReferenceVectors checks AES-EAX against the published
// Bellare/Rogaway/Wagner test vectors, which cover empty and non-empty
// associated data, the empty message and partial final blocks.
func TestAESEAXReferenceVectors(t *testing.T) {
	cases := []struct {
		name                      string
		key, nonce, header, plain string
		ciphertext, tag           string
	}{
		{
			name:       "empty message",
			key:        "233952de e4d5ed5f 9b9c6d6f f80ff478",
			nonce:      "62ec67f9 c3a4a407 fcb2a8c4 9031a8b3",
			header:     "6bfb914f d07eae6b",
			plain:      "",
			ciphertext: "",
			tag:        "e037830e 8389f27b 025a2d65 27e79d01",
		},
		{
			name:       "two byte message",
			key:        "91945d3f 4dcbee0b f45ef522 55f095a4",
			nonce:      "becaf043 b0a23d84 3194ba97 2c66debd",
			header:     "fa3bfd48 06eb53fa",
			plain:      "f7fb",
			ciphertext: "19dd",
			tag:        "5c4c9331 049d0bda b0277408 f67967e5",
		},
		{
			name:       "five byte message",
			key:        "01f74ad6 4077f2e7 04c0f60a da3dd523",
			nonce:      "70c3db4f 0d263684 00a10ed0 5d2bff5e",
			header:     "234a3463 c1264ac6",
			plain:      "1a47cb49 33",
			ciphertext: "d851d5ba e0",
			tag:        "3a59f238 a23e3919 9dc92666 26c40f80",
		},
		{
			name:       "eleven byte message",
			key:        "8395fcf1 e95bebd6 97bd010b c766aac3",
			nonce:      "22e7add9 3cfc6393 c57ec0b3 c17d6b44",
			header:     "126735fc c320d25a",
			plain:      "ca40d744 6e545ffa ed3bd12a 740a659f fbbb3cea b7",
			ciphertext: "cb8920f8 7a6c75cf f39627b5 6e3ed197 c552d295 a7",
			tag:        "cfc46afc 253b4652 b1af3795 b124ab6e",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := mustHex(t, tc.key)
			nonce := mustHex(t, tc.nonce)
			header := mustHex(t, tc.header)
			plain := mustHex(t, tc.plain)

			ciphertext, tag, err := rfb.AESEAXSeal(key, nonce, header, plain)
			if err != nil {
				t.Fatalf("AESEAXSeal: %v", err)
			}
			if got := hex.EncodeToString(ciphertext); got != strings.ReplaceAll(tc.ciphertext, " ", "") {
				t.Fatalf("ciphertext = %s, want %s", got, tc.ciphertext)
			}
			if got := hex.EncodeToString(tag[:]); got != strings.ReplaceAll(tc.tag, " ", "") {
				t.Fatalf("tag = %s, want %s", got, tc.tag)
			}

			opened, err := rfb.AESEAXOpen(key, nonce, header, ciphertext, tag[:])
			if err != nil {
				t.Fatalf("AESEAXOpen: %v", err)
			}
			if !bytes.Equal(opened, plain) {
				t.Fatalf("opened = %x, want %x", opened, plain)
			}
		})
	}
}

// TestAESEAXRejectsTampering proves a bad tag, ciphertext, associated data or
// nonce is rejected without returning plaintext.
func TestAESEAXRejectsTampering(t *testing.T) {
	key := mustHex(t, "233952de e4d5ed5f 9b9c6d6f f80ff478")
	nonce := mustHex(t, "62ec67f9 c3a4a407 fcb2a8c4 9031a8b3")
	header := mustHex(t, "6bfb914f d07eae6b")
	plain := []byte("desktop")
	ciphertext, tag, err := rfb.AESEAXSeal(key, nonce, header, plain)
	if err != nil {
		t.Fatalf("AESEAXSeal: %v", err)
	}

	cases := []struct {
		name       string
		nonce      []byte
		header     []byte
		ciphertext []byte
		tag        []byte
	}{
		{"tag", nonce, header, ciphertext, func() []byte { clone := append([]byte(nil), tag[:]...); clone[0] ^= 1; return clone }()},
		{"ciphertext", nonce, header, func() []byte { clone := append([]byte(nil), ciphertext...); clone[0] ^= 1; return clone }(), tag[:]},
		{"header", nonce, func() []byte { clone := append([]byte(nil), header...); clone[0] ^= 1; return clone }(), ciphertext, tag[:]},
		{"nonce", func() []byte { clone := append([]byte(nil), nonce...); clone[0] ^= 1; return clone }(), header, ciphertext, tag[:]},
		{"nonce length", nonce[:15], header, ciphertext, tag[:]},
		{"tag length", nonce, header, ciphertext, tag[:15]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opened, err := rfb.AESEAXOpen(key, tc.nonce, tc.header, tc.ciphertext, tc.tag)
			if err == nil {
				t.Fatalf("AESEAXOpen accepted a tampered %s and returned %x", tc.name, opened)
			}
			if opened != nil {
				t.Fatalf("AESEAXOpen returned %x alongside the error", opened)
			}
		})
	}
}

// TestAESEAXWith256BitKey round-trips a message under an AES-256 key, the key
// size the SHA-256 RA2 variants use.
func TestAESEAXWith256BitKey(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	nonce := bytes.Repeat([]byte{0x17}, 16)
	header := []byte{0, 7}
	plain := []byte("RA2-256 session payload")

	ciphertext, tag, err := rfb.AESEAXSeal(key, nonce, header, plain)
	if err != nil {
		t.Fatalf("AESEAXSeal: %v", err)
	}
	opened, err := rfb.AESEAXOpen(key, nonce, header, ciphertext, tag[:])
	if err != nil {
		t.Fatalf("AESEAXOpen: %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("opened = %q, want %q", opened, plain)
	}
}

// strongAuthServer starts an in-process server with a longer deadline than the
// frame tests need; RSA and DH key generation take longer than a pipe write.
func strongAuthServer(t *testing.T, cfg rfbtest.Config) (*rfbtest.Server, net.Conn) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	if err := clientConn.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	server := rfbtest.New(cfg)
	go func() { _ = server.Serve(serverConn) }()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	return server, clientConn
}

// readOneFrame proves the negotiated stream, including any TLS or AES-EAX
// layer, still carries the RFB session.
func readOneFrame(t *testing.T, client *rfb.Client) {
	t.Helper()
	if err := client.RequestFullUpdate(false); err != nil {
		t.Fatalf("RequestFullUpdate: %v", err)
	}
	message, err := client.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if message.Kind != rfb.MessageFramebufferUpdate {
		t.Fatalf("message = %+v, want a framebuffer update", message)
	}
}

// TestAppleDHAuthentication proves Apple's type 30 completes against the
// in-process server and carries the Host username.
func TestAppleDHAuthentication(t *testing.T) {
	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityAppleDH},
		Username:      "viewer",
		Password:      "apple-dh-pass",
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{
		Shared:   true,
		Username: "viewer",
		Password: "apple-dh-pass",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if client.SecurityChoice() != (rfb.SecurityChoice{Type: rfb.SecurityAppleDH}) {
		t.Fatalf("SecurityChoice = %v, want Apple authentication", client.SecurityChoice())
	}
	if !client.Plaintext() {
		t.Fatal("Apple DH was reported encrypted")
	}
	readOneFrame(t, client)

	received := server.Received()
	if received.Security != rfb.SecurityAppleDH {
		t.Fatalf("server saw security %v, want Apple authentication", received.Security)
	}
	if !received.AppleDHAccepted {
		t.Fatal("server did not verify the Apple DH credentials")
	}
	if received.AppleDHUsername != "viewer" {
		t.Fatalf("Apple DH username = %q, want viewer", received.AppleDHUsername)
	}
}

// TestVeNCryptSubtypesConnect exercises every implemented subtype end to end.
// The Plain family authenticates with username+password; the Vnc family uses
// only the password challenge. Plain and the plaintext subtypes are reported
// plaintext; every TLS/X509 subtype is reported encrypted.
func TestVeNCryptSubtypesConnect(t *testing.T) {
	ca := newTestCA(t)
	serverTLS := ca.serverTLS(t, "desk.test")
	cases := []struct {
		name      string
		subtype   rfb.VeNCryptSubtype
		tls       bool
		x509      bool
		plaintext bool
		sendsUser bool
	}{
		{"plain", rfb.VeNCryptPlain, false, false, true, true},
		{"tlsplain", rfb.VeNCryptTLSPlain, true, false, false, true},
		{"x509plain", rfb.VeNCryptX509Plain, true, true, false, true},
		{"tlsvnc", rfb.VeNCryptTLSVnc, true, false, false, false},
		{"x509vnc", rfb.VeNCryptX509Vnc, true, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.x509 {
				restore := rfb.SetSystemRootsForTest(ca.pool)
				t.Cleanup(restore)
			}
			server, conn := strongAuthServer(t, rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes:    []rfb.SecurityType{rfb.SecurityVeNCrypt},
				VeNCryptSubtypes: []rfb.VeNCryptSubtype{tc.subtype},
				TLSConfig:        serverTLS,
				Username:         "viewer",
				Password:         strongTestPassword,
			})
			client, err := rfb.Connect(conn, rfb.ClientConfig{
				Shared:     true,
				Username:   "viewer",
				Password:   strongTestPassword,
				ServerName: "desk.test",
			})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			want := rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: tc.subtype}
			if got := client.SecurityChoice(); got != want {
				t.Fatalf("SecurityChoice = %v, want %v", got, want)
			}
			if got := client.Plaintext(); got != tc.plaintext {
				t.Fatalf("Plaintext = %v, want %v", got, tc.plaintext)
			}
			readOneFrame(t, client)

			received := server.Received()
			if received.VeNCryptSubtype != tc.subtype {
				t.Fatalf("server saw subtype %v, want %v", received.VeNCryptSubtype, tc.subtype)
			}
			if received.TLSStarted != tc.tls {
				t.Fatalf("TLSStarted = %v, want %v", received.TLSStarted, tc.tls)
			}
			if tc.sendsUser {
				if received.Username != "viewer" {
					t.Fatalf("server saw username %q, want viewer", received.Username)
				}
				if received.Password != strongTestPassword {
					t.Fatal("server did not receive the VeNCrypt password")
				}
				if received.VNCAuthAccepted {
					t.Fatal("a Plain-family subtype ran the VNC challenge")
				}
			} else {
				if received.Username != "" {
					t.Fatalf("VNC-family subtype sent username %q", received.Username)
				}
				if received.Password != "" {
					t.Fatalf("VNC-family subtype sent the password in the clear: %q", received.Password)
				}
				if !received.VNCAuthAccepted {
					t.Fatal("server did not verify the VNC challenge response")
				}
			}
		})
	}
}

// TestVeNCryptX509RejectsUntrustedChain proves a certificate from a CA outside
// the system trust store is rejected even with correct credentials and Host
// name. There is no client option that skips the check.
func TestVeNCryptX509RejectsUntrustedChain(t *testing.T) {
	trusted := newTestCA(t)
	rogue := newTestCA(t)
	restore := rfb.SetSystemRootsForTest(trusted.pool)
	t.Cleanup(restore)

	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes:    []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{rfb.VeNCryptX509Plain},
		TLSConfig:        rogue.serverTLS(t, "desk.test"),
		Username:         "viewer",
		Password:         "vencrypt-pass",
	})
	_, err := rfb.Connect(conn, rfb.ClientConfig{
		Username:   "viewer",
		Password:   "vencrypt-pass",
		ServerName: "desk.test",
	})
	if !errors.Is(err, rfb.ErrTLSVerification) {
		t.Fatalf("Connect = %v, want ErrTLSVerification", err)
	}
	received := server.Received()
	if received.TLSStarted {
		t.Fatal("server completed the TLS handshake for an untrusted chain")
	}
	if received.Username != "" || received.Password != "" {
		t.Fatal("credentials were sent before the certificate was verified")
	}
}

// TestVeNCryptX509RejectsWrongHostName proves the Host name is checked, not
// just the chain.
func TestVeNCryptX509RejectsWrongHostName(t *testing.T) {
	ca := newTestCA(t)
	restore := rfb.SetSystemRootsForTest(ca.pool)
	t.Cleanup(restore)

	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes:    []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{rfb.VeNCryptX509Plain},
		TLSConfig:        ca.serverTLS(t, "desk.test"),
		Username:         "viewer",
		Password:         "vencrypt-pass",
	})
	_, err := rfb.Connect(conn, rfb.ClientConfig{
		Username:   "viewer",
		Password:   "vencrypt-pass",
		ServerName: "wrong.test",
	})
	if !errors.Is(err, rfb.ErrTLSVerification) {
		t.Fatalf("Connect = %v, want ErrTLSVerification", err)
	}
	if server.Received().TLSStarted {
		t.Fatal("server completed the TLS handshake for a mismatched Host name")
	}
}

// TestVeNCryptX509MissingServerNameFailsClosed proves an empty Host name is a
// verification failure, never an implicit skip.
func TestVeNCryptX509MissingServerNameFailsClosed(t *testing.T) {
	ca := newTestCA(t)
	restore := rfb.SetSystemRootsForTest(ca.pool)
	t.Cleanup(restore)

	_, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes:    []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{rfb.VeNCryptX509Plain},
		TLSConfig:        ca.serverTLS(t, "desk.test"),
		Username:         "viewer",
		Password:         "vencrypt-pass",
	})
	_, err := rfb.Connect(conn, rfb.ClientConfig{
		Username: "viewer",
		Password: "vencrypt-pass",
	})
	if !errors.Is(err, rfb.ErrTLSVerification) {
		t.Fatalf("Connect = %v, want ErrTLSVerification", err)
	}
}

// TestVeNCryptUnroutedPrefersX509Plain proves the unrouted preference selects
// the authenticated TLS subtype when every subtype is offered, and that it
// verifies the certificate.
func TestVeNCryptUnroutedPrefersX509Plain(t *testing.T) {
	ca := newTestCA(t)
	restore := rfb.SetSystemRootsForTest(ca.pool)
	t.Cleanup(restore)

	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{
			rfb.VeNCryptPlain, rfb.VeNCryptTLSPlain, rfb.VeNCryptX509Plain,
			rfb.VeNCryptTLSVnc, rfb.VeNCryptX509Vnc,
		},
		TLSConfig: ca.serverTLS(t, "desk.test"),
		Username:  "viewer",
		Password:  "vencrypt-pass",
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{
		Username:   "viewer",
		Password:   "vencrypt-pass",
		ServerName: "desk.test",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	want := rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: rfb.VeNCryptX509Plain}
	if got := client.SecurityChoice(); got != want {
		t.Fatalf("SecurityChoice = %v, want %v", got, want)
	}
	if client.Plaintext() {
		t.Fatal("X509Plain was reported plaintext")
	}
	readOneFrame(t, client)
	if !server.Received().TLSStarted {
		t.Fatal("the unrouted connection did not run TLS")
	}
}

// TestRoutedVeNCryptChoosesPlainWithoutTLS proves a routed Host never starts an
// inner TLS handshake even when the server offers TLS/X509 subtypes.
func TestRoutedVeNCryptChoosesPlainWithoutTLS(t *testing.T) {
	ca := newTestCA(t)
	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{
			rfb.VeNCryptX509Plain, rfb.VeNCryptTLSPlain, rfb.VeNCryptPlain,
		},
		TLSConfig: ca.serverTLS(t, "desk.test"),
		Username:  "viewer",
		Password:  "vencrypt-pass",
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{
		Routed:     true,
		Username:   "viewer",
		Password:   "vencrypt-pass",
		ServerName: "desk.test",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	want := rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: rfb.VeNCryptPlain}
	if got := client.SecurityChoice(); got != want {
		t.Fatalf("SecurityChoice = %v, want %v", got, want)
	}
	readOneFrame(t, client)

	received := server.Received()
	if received.TLSStarted {
		t.Fatal("a routed connection performed an inner TLS handshake")
	}
	if received.Username != "viewer" {
		t.Fatalf("server saw username %q, want viewer", received.Username)
	}
}

// TestRoutedVeNCryptOnlyTLSFailsClosed proves that when no non-TLS VeNCrypt
// subtype is offered a routed Host fails with a typed error rather than adding
// a second TLS layer.
func TestRoutedVeNCryptOnlyTLSFailsClosed(t *testing.T) {
	ca := newTestCA(t)
	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityVeNCrypt},
		VeNCryptSubtypes: []rfb.VeNCryptSubtype{
			rfb.VeNCryptX509Plain, rfb.VeNCryptTLSPlain,
		},
		TLSConfig: ca.serverTLS(t, "desk.test"),
		Username:  "viewer",
		Password:  "vencrypt-pass",
	})
	_, err := rfb.Connect(conn, rfb.ClientConfig{
		Routed:     true,
		Username:   "viewer",
		Password:   "vencrypt-pass",
		ServerName: "desk.test",
	})
	if !errors.Is(err, rfb.ErrUnsupportedSecurity) {
		t.Fatalf("Connect = %v, want ErrUnsupportedSecurity", err)
	}
	var securityErr *rfb.SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("Connect = %v, want *SecurityError", err)
	}
	if len(securityErr.Subtypes) != 2 {
		t.Fatalf("reported subtypes = %v, want both TLS subtypes", securityErr.Subtypes)
	}

	received := server.Received()
	if received.TLSStarted {
		t.Fatal("a routed connection started an inner TLS handshake")
	}
	if received.VeNCryptSubtype != 0 {
		t.Fatalf("client selected subtype %v, want no selection", received.VeNCryptSubtype)
	}
	if received.Username != "" || received.Password != "" {
		t.Fatal("a routed connection sent credentials after failing closed")
	}
}

// TestRA2VariantsConnect exercises every implemented RA2 variant. The encrypted
// variants only pass if the AES-EAX session layer covers the whole exchange,
// because the frame is requested and read after the handshake.
func TestRA2VariantsConnect(t *testing.T) {
	cases := []struct {
		name     string
		security rfb.SecurityType
	}{
		{"ra2ne", rfb.SecurityRA2ne},
		{"ra2", rfb.SecurityRA2},
		{"ra2-256", rfb.SecurityRA2_256},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, conn := strongAuthServer(t, rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes: []rfb.SecurityType{tc.security},
				Username:      "viewer",
				Password:      "ra2-pass",
			})
			client, err := rfb.Connect(conn, rfb.ClientConfig{
				Shared:   true,
				Username: "viewer",
				Password: "ra2-pass",
			})
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			want := rfb.SecurityChoice{Type: tc.security}
			if got := client.SecurityChoice(); got != want {
				t.Fatalf("SecurityChoice = %v, want %v", got, want)
			}
			wantPlaintext := tc.security == rfb.SecurityRA2ne || tc.security == rfb.SecurityRA2ne_256
			if got := client.Plaintext(); got != wantPlaintext {
				t.Fatalf("Plaintext = %v, want %v", got, wantPlaintext)
			}
			readOneFrame(t, client)

			received := server.Received()
			if received.Security != tc.security {
				t.Fatalf("server saw security %v, want %v", received.Security, tc.security)
			}
			if !received.RA2Accepted {
				t.Fatal("server did not verify the RA2 credentials")
			}
			if received.RA2Subtype != 1 {
				t.Fatalf("RA2 credential subtype = %d, want 1", received.RA2Subtype)
			}
			if received.RA2Username != "viewer" {
				t.Fatalf("RA2 username = %q, want viewer", received.RA2Username)
			}
			if received.RA2Password != "ra2-pass" {
				t.Fatal("server did not receive the RA2 password")
			}
		})
	}
}

// TestRA2PasswordOnlySubtypeIgnoresUsername proves a server that asks for the
// password-only RA2 credential subtype does not receive the configured
// username.
func TestRA2PasswordOnlySubtypeIgnoresUsername(t *testing.T) {
	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityRA2ne},
		Password:      "ra2-pass",
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{
		Username: "viewer",
		Password: "ra2-pass",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	readOneFrame(t, client)

	received := server.Received()
	if received.RA2Subtype != 2 {
		t.Fatalf("RA2 credential subtype = %d, want 2", received.RA2Subtype)
	}
	if received.RA2Username != "" {
		t.Fatalf("password-only RA2 subtype received username %q", received.RA2Username)
	}
	if !received.RA2Accepted {
		t.Fatal("server did not verify the password-only RA2 credentials")
	}
}

// TestRA2PlaintextClassification keeps the "ne" variants in the plaintext
// family even though they authenticate: they deliberately install no session
// encryption, so the unencrypted warning must still appear. RA2 and RA2-256
// cover the whole session with AES-EAX and are encrypted.
func TestRA2PlaintextClassification(t *testing.T) {
	cases := map[rfb.SecurityType]bool{
		rfb.SecurityRA2ne:     true,
		rfb.SecurityRA2ne_256: true,
		rfb.SecurityRA2:       false,
		rfb.SecurityRA2_256:   false,
	}
	for security, wantPlaintext := range cases {
		if got := security.Plaintext(); got != wantPlaintext {
			t.Errorf("%v.Plaintext() = %v, want %v", security, got, wantPlaintext)
		}
	}
}

// TestRA2RejectsOversizedServerKey proves a server cannot dictate unbounded
// key-generation work: a key larger than the accepted cap fails before the
// client generates anything, so a hostile 8192-bit offer cannot stall the
// handshake.
func TestRA2RejectsOversizedServerKey(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	if err := clientConn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}

	serverDone := make(chan error, 1)
	go func() {
		if err := writeTestAll(serverConn, []byte("RFB 003.008\n")); err != nil {
			serverDone <- err
			return
		}
		var version [12]byte
		if _, err := io.ReadFull(serverConn, version[:]); err != nil {
			serverDone <- err
			return
		}
		if err := writeTestAll(serverConn, []byte{1, byte(rfb.SecurityRA2)}); err != nil {
			serverDone <- err
			return
		}
		var selection [1]byte
		if _, err := io.ReadFull(serverConn, selection[:]); err != nil {
			serverDone <- err
			return
		}
		var bits [4]byte
		binary.BigEndian.PutUint32(bits[:], 8192)
		serverDone <- writeTestAll(serverConn, bits[:])
	}()

	started := time.Now()
	_, err := rfb.Connect(clientConn, rfb.ClientConfig{
		Password:      "ra2-pass",
		KeygenTimeout: time.Second,
	})
	if !errors.Is(err, rfb.ErrHandshake) {
		t.Fatalf("Connect = %v, want ErrHandshake", err)
	}
	if !strings.Contains(err.Error(), "8192") {
		t.Fatalf("Connect = %v, want the rejected key size", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("oversized key took %v, want a fail-fast rejection", elapsed)
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish writing the oversized key")
	}
}

// TestRA2WrongCredentialsAreRejected proves a bad RA2 password fails as a
// typed security rejection through the AES-EAX layer of the encrypted variants,
// not as a malformed stream.
func TestRA2WrongCredentialsAreRejected(t *testing.T) {
	for _, security := range []rfb.SecurityType{
		rfb.SecurityRA2ne, rfb.SecurityRA2, rfb.SecurityRA2_256,
	} {
		t.Run(security.String(), func(t *testing.T) {
			server, conn := strongAuthServer(t, rfbtest.Config{
				Width: 2, Height: 2,
				SecurityTypes: []rfb.SecurityType{security},
				Password:      "ra2-pass",
			})
			_, err := rfb.Connect(conn, rfb.ClientConfig{
				Password: "wrong-password",
			})
			if !errors.Is(err, rfb.ErrSecurityRejected) {
				t.Fatalf("Connect = %v, want ErrSecurityRejected", err)
			}
			if server.Received().RA2Accepted {
				t.Fatal("the server accepted the wrong RA2 password")
			}
		})
	}
}

// TestRA2EncryptedSessionAuthenticatedRoundTrip exercises a whole encrypted
// RA2 session: authentication, a frame, client input through the AES-EAX
// stream, and another frame. A one-way authentication check would not catch a
// broken write direction.
func TestRA2EncryptedSessionAuthenticatedRoundTrip(t *testing.T) {
	server, conn := strongAuthServer(t, rfbtest.Config{
		Width: 2, Height: 2,
		SecurityTypes: []rfb.SecurityType{rfb.SecurityRA2},
		Username:      "viewer",
		Password:      "ra2-pass",
	})
	client, err := rfb.Connect(conn, rfb.ClientConfig{
		Shared:   true,
		Username: "viewer",
		Password: "ra2-pass",
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	readOneFrame(t, client)

	if err := client.SendKey(0x62, true); err != nil {
		t.Fatalf("SendKey down: %v", err)
	}
	if err := client.SendKey(0x62, false); err != nil {
		t.Fatalf("SendKey up: %v", err)
	}
	if err := client.SendPointer(1, 3, 4); err != nil {
		t.Fatalf("SendPointer: %v", err)
	}
	// The frame request is written after the input on the same ordered stream,
	// so receiving this update proves the server consumed the earlier messages.
	readOneFrame(t, client)

	deadline := time.Now().Add(5 * time.Second)
	for {
		received := server.Received()
		if len(received.Keys) == 2 && len(received.Pointers) == 1 {
			if received.Keys[0].Keysym != 0x62 || !received.Keys[0].Down {
				t.Fatalf("first key = %+v, want a 0x62 press", received.Keys[0])
			}
			if received.Pointers[0].X != 3 || received.Pointers[0].Y != 4 {
				t.Fatalf("pointer = %+v, want (3,4)", received.Pointers[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server received keys=%v pointers=%v, want the RA2 input round trip",
				received.Keys, received.Pointers)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeTestAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

// testCA is a generated CA plus its trust pool. It is created per test so no
// platform certificate is involved.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Natcatty Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// serverTLS builds a server TLS config with a leaf certificate for dnsName
// signed by the CA.
func (ca *testCA) serverTLS(t *testing.T, dnsName string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: dnsName},
		DNSNames:     []string{dnsName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("leaf certificate: %v", err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}
}
