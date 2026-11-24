package rfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestSelectSecurityUsesTheDocumentedPreference(t *testing.T) {
	selected, ok := SelectSecurity([]SecurityType{SecurityNone, SecurityVNCAuth})
	if !ok || selected != SecurityVNCAuth {
		t.Fatalf("SelectSecurity = %v, %v; want VNC authentication, true", selected, ok)
	}
	// The full unrouted order, strongest first.
	order := []SecurityType{
		SecurityRA2ne, SecurityRA2_256, SecurityRA2, SecurityVeNCrypt,
		SecurityAppleDH, SecurityVNCAuth, SecurityNone,
	}
	for i := 0; i < len(order); i++ {
		selected, ok := SelectSecurity(order[i:])
		if !ok || selected != order[i] {
			t.Fatalf("SelectSecurity(%v) = %v, %v; want %v", order[i:], selected, ok, order[i])
		}
	}
	// A routed Host must not choose VeNCrypt while another non-TLS type is
	// available; it may only use VeNCrypt when nothing else is offered.
	if selected, ok := SelectSecurityRouted([]SecurityType{SecurityVeNCrypt, SecurityVNCAuth}); !ok || selected != SecurityVNCAuth {
		t.Fatalf("SelectSecurityRouted = %v, %v; want VNC authentication", selected, ok)
	}
	if selected, ok := SelectSecurityRouted([]SecurityType{SecurityVeNCrypt, SecurityNone}); !ok || selected != SecurityVeNCrypt {
		t.Fatalf("SelectSecurityRouted = %v, %v; want VeNCrypt over None", selected, ok)
	}
	if _, ok := SelectSecurity([]SecurityType{SecurityType(113)}); ok {
		t.Fatal("SelectSecurity accepted an unimplemented type")
	}
	if _, ok := SelectSecurity(nil); ok {
		t.Fatal("SelectSecurity accepted an empty offer")
	}
}

// TestSelectSecurityEveryCombination proves the strongest type wins for every
// subset of the types the client implements, not just the hand-picked pairs.
func TestSelectSecurityEveryCombination(t *testing.T) {
	order := []SecurityType{
		SecurityRA2ne, SecurityRA2_256, SecurityRA2, SecurityVeNCrypt,
		SecurityAppleDH, SecurityVNCAuth, SecurityNone,
	}
	for mask := 1; mask < 1<<len(order); mask++ {
		offered := make([]SecurityType, 0, len(order))
		want := SecurityInvalid
		for bit, security := range order {
			if mask&(1<<bit) == 0 {
				continue
			}
			offered = append(offered, security)
			if want == SecurityInvalid {
				want = security
			}
		}
		selected, ok := SelectSecurity(offered)
		if !ok || selected != want {
			t.Fatalf("offered %v: SelectSecurity = %v, %v; want %v", offered, selected, ok, want)
		}
	}
}

func TestSelectVeNCryptSubtypePreference(t *testing.T) {
	order := []VeNCryptSubtype{
		VeNCryptX509Plain, VeNCryptX509Vnc,
		VeNCryptTLSPlain, VeNCryptTLSVnc, VeNCryptPlain,
	}
	offered := append([]VeNCryptSubtype(nil), order...)
	for i := 0; i < len(order); i++ {
		selected, ok := SelectVeNCryptSubtype(offered[i:])
		if !ok || selected != order[i] {
			t.Fatalf("SelectVeNCryptSubtype(%v) = %v, %v; want %v", offered[i:], selected, ok, order[i])
		}
	}
	if selected, ok := SelectVeNCryptSubtypeRouted(offered); !ok || selected != VeNCryptPlain {
		t.Fatalf("SelectVeNCryptSubtypeRouted = %v, %v; want Plain", selected, ok)
	}
	if _, ok := SelectVeNCryptSubtypeRouted([]VeNCryptSubtype{VeNCryptTLSPlain, VeNCryptX509Plain}); ok {
		t.Fatal("a routed Host accepted a TLS-requiring VeNCrypt subtype")
	}
}

func TestSecurityErrorUnwrap(t *testing.T) {
	unsupported := &SecurityError{Offered: []SecurityType{SecurityVNCAuth}}
	if !errors.Is(unsupported, ErrUnsupportedSecurity) {
		t.Fatalf("unsupported error unwraps to %v", unsupported)
	}
	rejected := &SecurityError{Offered: []SecurityType{SecurityNone}, Reason: "no"}
	if !errors.Is(rejected, ErrSecurityRejected) {
		t.Fatalf("rejected error unwraps to %v", rejected)
	}
	if !strings.Contains(rejected.Error(), "no") {
		t.Fatalf("rejected error = %q", rejected.Error())
	}
}

func TestSecurityTypeString(t *testing.T) {
	if got := SecurityNone.String(); got != "None" {
		t.Fatalf("SecurityNone = %q", got)
	}
	if got := SecurityType(200).String(); !strings.Contains(got, "200") {
		t.Fatalf("unknown security type = %q", got)
	}
}

// appendReason appends a length-prefixed failure reason to wire.
func appendReason(wire []byte, reason string) []byte {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(reason)))
	wire = append(wire, length[:]...)
	return append(wire, reason...)
}

func TestClientSecurity3_7RejectionConsumesReason(t *testing.T) {
	const reason = "connection refused"
	wire := appendReason([]byte{0}, reason) // Zero security types.

	reader := bufio.NewReader(bytes.NewReader(wire))
	var out bytes.Buffer
	_, err := clientSecurity(&out, reader, Version3_7, ClientConfig{})

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("clientSecurity = %v, want *SecurityError", err)
	}
	if !errors.Is(err, ErrSecurityRejected) {
		t.Fatalf("clientSecurity = %v, want ErrSecurityRejected", err)
	}
	if errors.Is(err, ErrUnsupportedSecurity) {
		t.Fatalf("clientSecurity = %v, want it not to unwrap to ErrUnsupportedSecurity", err)
	}
	if securityErr.Reason != reason {
		t.Fatalf("reason = %q, want %q", securityErr.Reason, reason)
	}
	if reader.Buffered() != 0 {
		t.Fatalf("%d reason bytes left unread", reader.Buffered())
	}
	if out.Len() != 0 {
		t.Fatalf("client wrote %d bytes on a rejected handshake", out.Len())
	}
}

func TestClientSecurity3_3Rejection(t *testing.T) {
	const reason = "too many clients"
	wire := make([]byte, 4)
	binary.BigEndian.PutUint32(wire, uint32(SecurityInvalid))
	wire = appendReason(wire, reason)

	reader := bufio.NewReader(bytes.NewReader(wire))
	var out bytes.Buffer
	_, err := clientSecurity(&out, reader, Version3_3, ClientConfig{})

	var securityErr *SecurityError
	if !errors.As(err, &securityErr) {
		t.Fatalf("clientSecurity = %v, want *SecurityError", err)
	}
	if !errors.Is(err, ErrSecurityRejected) {
		t.Fatalf("clientSecurity = %v, want ErrSecurityRejected", err)
	}
	if errors.Is(err, ErrUnsupportedSecurity) {
		t.Fatalf("clientSecurity = %v, want it not to unwrap to ErrUnsupportedSecurity", err)
	}
	if len(securityErr.Offered) != 0 {
		t.Fatalf("offered = %v, want none for a type-0 rejection", securityErr.Offered)
	}
	if securityErr.Reason != reason {
		t.Fatalf("reason = %q, want %q", securityErr.Reason, reason)
	}
}

func TestClientSecurity3_3UnsupportedType(t *testing.T) {
	wire := make([]byte, 4)
	// UltraVNC's MSLogonII (113) is registered but not implemented. It must be
	// refused instead of falling back to None.
	binary.BigEndian.PutUint32(wire, uint32(SecurityType(113)))

	var out bytes.Buffer
	_, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_3, ClientConfig{})
	if !errors.Is(err, ErrUnsupportedSecurity) {
		t.Fatalf("clientSecurity = %v, want ErrUnsupportedSecurity", err)
	}
	if errors.Is(err, ErrSecurityRejected) {
		t.Fatalf("clientSecurity = %v, want it not to unwrap to ErrSecurityRejected", err)
	}
}

// vncChallengeWire is the deterministic challenge the VNC auth tests use.
var vncChallengeWire = [16]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

func TestVNCAuthKeyReversesEveryBit(t *testing.T) {
	// Each ASCII digit's bits are reversed: '1' (0x31) becomes 0x8c.
	want := [8]byte{0x8c, 0x4c, 0xcc, 0x2c, 0xac, 0x6c, 0xec, 0x1c}
	if got := VNCAuthKey("12345678"); got != want {
		t.Fatalf("VNCAuthKey = %#v, want %#v", got, want)
	}
	// A short password is NUL-padded; a long one is truncated to 8 bytes.
	if VNCAuthKey("abc")[3] != 0 {
		t.Fatalf("short password was not NUL-padded: %#v", VNCAuthKey("abc"))
	}
	if VNCAuthKey("123456789") != VNCAuthKey("12345678") {
		t.Fatal("a password longer than 8 bytes was not truncated")
	}
}

func TestSecurityTypePlaintext(t *testing.T) {
	for _, plaintext := range []SecurityType{
		SecurityNone, SecurityVNCAuth, SecurityAppleDH,
		SecurityRA2ne, SecurityRA2ne_256,
		// VeNCrypt alone cannot prove privacy: the subtype decides, so the
		// container fails closed as plaintext.
		SecurityVeNCrypt,
		// An unknown type fails closed: it is not silently treated as
		// encrypted without a decision.
		SecurityType(200),
	} {
		if !plaintext.Plaintext() {
			t.Fatalf("%v.Plaintext() = false, want true", plaintext)
		}
	}
	for _, encrypted := range []SecurityType{
		SecurityRA2, SecurityRA2r, SecurityRA2_256, SecurityRA2r_256,
	} {
		if encrypted.Plaintext() {
			t.Fatalf("%v.Plaintext() = true, want false", encrypted)
		}
	}
}

func TestSecurityChoicePlaintextIsSubtypeAware(t *testing.T) {
	plain := SecurityChoice{Type: SecurityVeNCrypt, Subtype: VeNCryptPlain}
	if !plain.Plaintext() {
		t.Fatal("VeNCrypt Plain was reported encrypted")
	}
	for _, subtype := range []VeNCryptSubtype{
		VeNCryptTLSPlain, VeNCryptTLSVnc, VeNCryptX509Plain, VeNCryptX509Vnc,
	} {
		choice := SecurityChoice{Type: SecurityVeNCrypt, Subtype: subtype}
		if choice.Plaintext() {
			t.Fatalf("%v was reported plaintext", subtype)
		}
	}
	for _, choice := range []SecurityChoice{
		{Type: SecurityRA2ne}, {Type: SecurityRA2ne_256},
		{Type: SecurityAppleDH}, {Type: SecurityVNCAuth}, {Type: SecurityNone},
	} {
		if !choice.Plaintext() {
			t.Fatalf("%v choice was reported encrypted", choice)
		}
	}
	for _, choice := range []SecurityChoice{
		{Type: SecurityRA2}, {Type: SecurityRA2_256},
	} {
		if choice.Plaintext() {
			t.Fatalf("%v choice was reported plaintext", choice)
		}
	}
}

func TestClientSecurity3_8VNCAuthSendsResponse(t *testing.T) {
	wire := []byte{1, byte(SecurityVNCAuth)}
	wire = append(wire, vncChallengeWire[:]...)
	wire = append(wire, 0, 0, 0, 0) // Success.

	var out bytes.Buffer
	selected, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_8, ClientConfig{Password: "secret"})
	if err != nil {
		t.Fatalf("clientSecurity: %v", err)
	}
	if selected != SecurityVNCAuth {
		t.Fatalf("selected = %v, want VNC authentication", selected)
	}
	want, err := VNCAuthResponse("secret", vncChallengeWire[:])
	if err != nil {
		t.Fatalf("VNCAuthResponse: %v", err)
	}
	got := out.Bytes()
	if len(got) != 1+len(want) {
		t.Fatalf("client wrote %d bytes, want selection plus a 16-byte response", len(got))
	}
	if got[0] != byte(SecurityVNCAuth) {
		t.Fatalf("selection = %d, want %d", got[0], SecurityVNCAuth)
	}
	if !bytes.Equal(got[1:], want[:]) {
		t.Fatalf("response = %x, want %x", got[1:], want)
	}
}

func TestClientSecurity3_3VNCAuthReadsResult(t *testing.T) {
	wire := make([]byte, 4)
	binary.BigEndian.PutUint32(wire, uint32(SecurityVNCAuth))
	wire = append(wire, vncChallengeWire[:]...)
	wire = append(wire, 0, 0, 0, 0)

	var out bytes.Buffer
	selected, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_3, ClientConfig{Password: "secret"})
	if err != nil {
		t.Fatalf("clientSecurity: %v", err)
	}
	if selected != SecurityVNCAuth {
		t.Fatalf("selected = %v, want VNC authentication", selected)
	}
	if out.Len() != 16 {
		t.Fatalf("client wrote %d bytes, want only the 16-byte response", out.Len())
	}
}

func TestClientSecurityRejectsAWrongVNCPassword(t *testing.T) {
	const reason = "authentication failed"
	wire := []byte{1, byte(SecurityVNCAuth)}
	wire = append(wire, vncChallengeWire[:]...)
	wire = append(wire, 0, 0, 0, 1)
	wire = appendReason(wire, reason)

	var out bytes.Buffer
	_, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_8, ClientConfig{Password: "wrong"})
	if !errors.Is(err, ErrSecurityRejected) {
		t.Fatalf("clientSecurity = %v, want ErrSecurityRejected", err)
	}
	var securityErr *SecurityError
	if !errors.As(err, &securityErr) || securityErr.Reason != reason {
		t.Fatalf("clientSecurity = %v, want reason %q", err, reason)
	}
}

func TestClientSecurityMissingVNCPasswordSendsNothing(t *testing.T) {
	wire := []byte{1, byte(SecurityVNCAuth)}
	reader := bufio.NewReader(bytes.NewReader(wire))
	var out bytes.Buffer
	_, err := clientSecurity(&out, reader, Version3_8, ClientConfig{})
	if !errors.Is(err, ErrMissingPassword) {
		t.Fatalf("clientSecurity = %v, want ErrMissingPassword", err)
	}
	if out.Len() != 0 {
		t.Fatalf("client wrote %d bytes without a password", out.Len())
	}
	if reader.Buffered() != 0 {
		t.Fatalf("%d bytes left unread after a missing-password failure", reader.Buffered())
	}
}

func TestClientSecurity3_8RejectionReadsReason(t *testing.T) {
	const reason = "authentication failed"
	wire := []byte{1, byte(SecurityNone)} // One type: None.
	result := make([]byte, 4)
	binary.BigEndian.PutUint32(result, 1)
	wire = append(wire, result...)
	wire = appendReason(wire, reason)

	reader := bufio.NewReader(bytes.NewReader(wire))
	var out bytes.Buffer
	_, err := clientSecurity(&out, reader, Version3_8, ClientConfig{})
	if !errors.Is(err, ErrSecurityRejected) {
		t.Fatalf("clientSecurity = %v, want ErrSecurityRejected", err)
	}
	var securityErr *SecurityError
	if !errors.As(err, &securityErr) || securityErr.Reason != reason {
		t.Fatalf("clientSecurity = %v, want reason %q", err, reason)
	}
	if reader.Buffered() != 0 {
		t.Fatalf("%d reason bytes left unread", reader.Buffered())
	}
}

// TestVeNCryptTLSVerificationPolicyIsClosedByDefault proves a subtype outside
// the explicit X509 and anonymous-TLS cases cannot fall through to a TLS
// handshake that skips certificate verification.
func TestVeNCryptTLSVerificationPolicyIsClosedByDefault(t *testing.T) {
	for _, subtype := range []VeNCryptSubtype{
		VeNCryptSubtype(9999),
		VeNCryptTLSNone,
		VeNCryptX509None,
	} {
		_, err := startVeNCryptTLS(&bytes.Buffer{}, &bytes.Buffer{}, subtype, "desk.local")
		if !errors.Is(err, ErrUnsupportedSecurity) {
			t.Fatalf("startVeNCryptTLS(%v) = %v, want ErrUnsupportedSecurity", subtype, err)
		}
	}
}
