package rfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestSelectSecurityPrefersVNCPasswordOverNone(t *testing.T) {
	selected, ok := SelectSecurity([]SecurityType{SecurityNone, SecurityVNCAuth})
	if !ok || selected != SecurityVNCAuth {
		t.Fatalf("SelectSecurity = %v, %v; want VNC authentication, true", selected, ok)
	}
	if selected, ok := SelectSecurity([]SecurityType{SecurityNone}); !ok || selected != SecurityNone {
		t.Fatalf("SelectSecurity = %v, %v; want None, true", selected, ok)
	}
	if _, ok := SelectSecurity([]SecurityType{SecurityVeNCrypt, SecurityAppleDH}); ok {
		t.Fatal("SelectSecurity accepted types this ticket does not implement")
	}
	if _, ok := SelectSecurity(nil); ok {
		t.Fatal("SelectSecurity accepted an empty offer")
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
	_, err := clientSecurity(&out, reader, Version3_7, "")

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
	_, err := clientSecurity(&out, reader, Version3_3, "")

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
	binary.BigEndian.PutUint32(wire, uint32(SecurityVeNCrypt))

	var out bytes.Buffer
	_, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_3, "")
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
		// An unknown type fails closed: it is not silently treated as
		// encrypted without a decision.
		SecurityType(200),
	} {
		if !plaintext.Plaintext() {
			t.Fatalf("%v.Plaintext() = false, want true", plaintext)
		}
	}
	for _, encrypted := range []SecurityType{
		SecurityVeNCrypt, SecurityRA2, SecurityRA2r,
		SecurityRA2_256, SecurityRA2r_256,
	} {
		if encrypted.Plaintext() {
			t.Fatalf("%v.Plaintext() = true, want false", encrypted)
		}
	}
}

func TestClientSecurity3_8VNCAuthSendsResponse(t *testing.T) {
	wire := []byte{1, byte(SecurityVNCAuth)}
	wire = append(wire, vncChallengeWire[:]...)
	wire = append(wire, 0, 0, 0, 0) // Success.

	var out bytes.Buffer
	selected, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_8, "secret")
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
	selected, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_3, "secret")
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
	_, err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_8, "wrong")
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
	_, err := clientSecurity(&out, reader, Version3_8, "")
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
	_, err := clientSecurity(&out, reader, Version3_8, "")
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
