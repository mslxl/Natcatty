package rfb

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestSelectSecurityOnlyNone(t *testing.T) {
	selected, ok := SelectSecurity([]SecurityType{SecurityVNCAuth, SecurityNone})
	if !ok || selected != SecurityNone {
		t.Fatalf("SelectSecurity = %v, %v; want None, true", selected, ok)
	}
	if _, ok := SelectSecurity([]SecurityType{SecurityVNCAuth, SecurityVeNCrypt}); ok {
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
	err := clientSecurity(&out, reader, Version3_7)

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
	err := clientSecurity(&out, reader, Version3_3)

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
	binary.BigEndian.PutUint32(wire, uint32(SecurityVNCAuth))

	var out bytes.Buffer
	err := clientSecurity(&out, bufio.NewReader(bytes.NewReader(wire)), Version3_3)
	if !errors.Is(err, ErrUnsupportedSecurity) {
		t.Fatalf("clientSecurity = %v, want ErrUnsupportedSecurity", err)
	}
	if errors.Is(err, ErrSecurityRejected) {
		t.Fatalf("clientSecurity = %v, want it not to unwrap to ErrSecurityRejected", err)
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
	err := clientSecurity(&out, reader, Version3_8)
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
