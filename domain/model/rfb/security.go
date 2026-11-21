package rfb

import "fmt"

// SecurityType is an RFB security type number.
type SecurityType uint8

// Registered security types. Only SecurityNone and SecurityVNCAuth are
// implemented by this package; the rest are carried so negotiation can report
// what a server offered and refuse a downgrade.
const (
	// SecurityInvalid is the 3.3 failure value.
	SecurityInvalid SecurityType = 0
	// SecurityNone is no authentication.
	SecurityNone SecurityType = 1
	// SecurityVNCAuth is the DES challenge/response VNC password type.
	SecurityVNCAuth SecurityType = 2
	// SecurityRA2 is the RSA-AES type.
	SecurityRA2 SecurityType = 5
	// SecurityRA2ne is the RSA-AES type with no session encryption.
	SecurityRA2ne SecurityType = 6
	// SecurityRA2r is the RSA-AES type with a second key exchange round.
	SecurityRA2r SecurityType = 13
	// SecurityVeNCrypt is the VeNCrypt container type.
	SecurityVeNCrypt SecurityType = 19
	// SecurityAppleDH is Apple's Diffie-Hellman authentication type.
	SecurityAppleDH SecurityType = 30
	// SecurityRA2_256 is the 256-bit RSA-AES type.
	SecurityRA2_256 SecurityType = 129
	// SecurityRA2ne_256 is the 256-bit RSA-AES type with no session
	// encryption.
	SecurityRA2ne_256 SecurityType = 130
	// SecurityRA2r_256 is the 256-bit RSA-AES type with a second key
	// exchange round.
	SecurityRA2r_256 SecurityType = 133
)

// String names the known types and prints any other in decimal.
func (t SecurityType) String() string {
	switch t {
	case SecurityInvalid:
		return "invalid"
	case SecurityNone:
		return "None"
	case SecurityVNCAuth:
		return "VNC authentication"
	case SecurityRA2:
		return "RA2"
	case SecurityRA2ne:
		return "RA2ne"
	case SecurityRA2r:
		return "RA2r"
	case SecurityVeNCrypt:
		return "VeNCrypt"
	case SecurityAppleDH:
		return "Apple authentication"
	case SecurityRA2_256:
		return "RA2-256"
	case SecurityRA2ne_256:
		return "RA2ne-256"
	case SecurityRA2r_256:
		return "RA2r-256"
	default:
		return fmt.Sprintf("security type %d", uint8(t))
	}
}

// Plaintext reports whether a security type leaves the RFB byte stream
// unencrypted. None and VNC password carry no session encryption; Apple's
// Diffie-Hellman type also leaves the stream in the clear. RA2ne and
// RA2ne-256 authenticate but deliberately provide no session encryption, so
// they are plaintext too.
//
// An unknown type is classified plaintext (fail closed): a type this client
// has not decided about must not be silently treated as encrypted and skip
// the warning. VeNCrypt is a container whose security depends on the subtype
// negotiated inside it; ticket 16 must classify Plain versus TLS subtypes when
// it adds them. Until then the container stays non-plaintext here.
func (t SecurityType) Plaintext() bool {
	switch t {
	case SecurityNone, SecurityVNCAuth, SecurityAppleDH,
		SecurityRA2ne, SecurityRA2ne_256:
		return true
	case SecurityRA2, SecurityRA2r, SecurityRA2_256, SecurityRA2r_256:
		return false
	case SecurityVeNCrypt:
		// TODO(ticket 16): classify the VeNCrypt subtype; Plain is
		// unencrypted and TLS is not.
		return false
	default:
		// Fail closed: unknown and future types are treated as unencrypted
		// until a decision is made, so the warning is never skipped.
		return true
	}
}

// supportedSecurity is the client's preference order, strongest first. This
// slice implements VNC password authentication and None; later security work
// prepends stronger types.
var supportedSecurity = []SecurityType{SecurityVNCAuth, SecurityNone}

// SelectSecurity returns the strongest security type both this client and the
// server support.
func SelectSecurity(offered []SecurityType) (SecurityType, bool) {
	for _, want := range supportedSecurity {
		for _, got := range offered {
			if got == want {
				return want, true
			}
		}
	}
	return SecurityInvalid, false
}
