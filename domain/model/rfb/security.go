package rfb

import "fmt"

// SecurityType is an RFB security type number.
type SecurityType uint8

// Registered security types.
const (
	// SecurityInvalid is the 3.3 failure value.
	SecurityInvalid SecurityType = 0
	// SecurityNone is no authentication.
	SecurityNone SecurityType = 1
	// SecurityVNCAuth is the DES challenge/response VNC password type.
	SecurityVNCAuth SecurityType = 2
	// SecurityRA2 is the RSA-AES type with an encrypted session layer.
	SecurityRA2 SecurityType = 5
	// SecurityRA2ne is the RSA-AES type with no session encryption.
	SecurityRA2ne SecurityType = 6
	// SecurityRA2r is the RSA-AES type with a second key exchange round.
	SecurityRA2r SecurityType = 13
	// SecurityVeNCrypt is the VeNCrypt container type. The subtype negotiated
	// inside it decides the transport and authentication.
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
// unencrypted. None, VNC password and Apple's Diffie-Hellman type carry no
// session encryption. RA2ne and RA2ne-256 authenticate but deliberately
// provide no session encryption, so they are plaintext too: the ticket's
// shorthand "an RA2 type" must not be read as "encrypted", because these
// variants still warn for an unrouted Host. RA2, RA2r and their 256-bit
// variants install an AES-EAX layer over the whole session and are encrypted.
//
// VeNCrypt is a container whose privacy is decided by the subtype negotiated
// inside it. A bare SecurityVeNCrypt is therefore classified plaintext (fail
// closed) so a caller that forgets to consult SecurityChoice.Plaintext never
// skips the unencrypted warning. Negotiated connections must use the
// SecurityChoice returned by Connect.
//
// An unknown type is classified plaintext (fail closed): a type this client
// has not decided about must not be silently treated as encrypted and skip
// the warning.
func (t SecurityType) Plaintext() bool {
	switch t {
	case SecurityNone, SecurityVNCAuth, SecurityAppleDH,
		SecurityRA2ne, SecurityRA2ne_256, SecurityVeNCrypt:
		return true
	case SecurityRA2, SecurityRA2r, SecurityRA2_256, SecurityRA2r_256:
		return false
	default:
		// Fail closed: unknown and future types are treated as unencrypted
		// until a decision is made, so the warning is never skipped.
		return true
	}
}

// VeNCryptSubtype is the 32-bit subtype selected inside the VeNCrypt container
// (security type 19). The numbers are the registered rfbproto values, which
// noVNC, TigerVNC and RealVNC all use.
type VeNCryptSubtype uint32

// Registered VeNCrypt subtypes. Only the plain-text-credential and VNC-password
// families are implemented; the number constants for the other subtypes are
// kept so a server offer can be reported accurately.
const (
	// VeNCryptPlain sends the username and password in the clear. It is the
	// only subtype that runs without TLS.
	VeNCryptPlain VeNCryptSubtype = 256
	// VeNCryptTLSNone is TLS with no inner authentication.
	VeNCryptTLSNone VeNCryptSubtype = 257
	// VeNCryptTLSVnc is TLS carrying the VNC password challenge.
	VeNCryptTLSVnc VeNCryptSubtype = 258
	// VeNCryptTLSPlain is TLS carrying the username and password exchange.
	VeNCryptTLSPlain VeNCryptSubtype = 259
	// VeNCryptX509None is authenticated TLS with no inner authentication.
	VeNCryptX509None VeNCryptSubtype = 260
	// VeNCryptX509Vnc is authenticated TLS carrying the VNC password
	// challenge.
	VeNCryptX509Vnc VeNCryptSubtype = 261
	// VeNCryptX509Plain is authenticated TLS carrying the username and
	// password exchange.
	VeNCryptX509Plain VeNCryptSubtype = 262
)

// String names the known subtypes and prints any other in decimal.
func (s VeNCryptSubtype) String() string {
	switch s {
	case VeNCryptPlain:
		return "VeNCrypt Plain"
	case VeNCryptTLSNone:
		return "VeNCrypt TLSNone"
	case VeNCryptTLSVnc:
		return "VeNCrypt TLSVnc"
	case VeNCryptTLSPlain:
		return "VeNCrypt TLSPlain"
	case VeNCryptX509None:
		return "VeNCrypt X509None"
	case VeNCryptX509Vnc:
		return "VeNCrypt X509Vnc"
	case VeNCryptX509Plain:
		return "VeNCrypt X509Plain"
	default:
		return fmt.Sprintf("VeNCrypt subtype %d", uint32(s))
	}
}

// Plaintext reports whether the subtype leaves credentials or the session in
// the clear. Plain does; every TLS and X509 subtype does not. An unknown
// subtype fails closed as plaintext.
func (s VeNCryptSubtype) Plaintext() bool {
	switch s {
	case VeNCryptTLSVnc, VeNCryptTLSPlain, VeNCryptX509Vnc, VeNCryptX509Plain,
		VeNCryptTLSNone, VeNCryptX509None:
		return false
	default:
		return true
	}
}

// SecurityChoice is the result of security negotiation: the container type and,
// for VeNCrypt, the subtype selected inside it. Plaintext reports the effective
// privacy of the negotiated connection.
type SecurityChoice struct {
	Type    SecurityType
	Subtype VeNCryptSubtype
}

// Plaintext reports whether the negotiated connection leaves the RFB stream
// unencrypted. It is subtype-aware, unlike SecurityType.Plaintext on the
// VeNCrypt container.
func (c SecurityChoice) Plaintext() bool {
	if c.Type == SecurityVeNCrypt {
		return c.Subtype.Plaintext()
	}
	return c.Type.Plaintext()
}

// String names the negotiated type and, for VeNCrypt, its subtype.
func (c SecurityChoice) String() string {
	if c.Type == SecurityVeNCrypt && c.Subtype != 0 {
		return fmt.Sprintf("%v (%v)", c.Type, c.Subtype)
	}
	return c.Type.String()
}

// supportedSecurity is the client's preference order for an unrouted Host,
// strongest first. It follows the auth/security negotiation decision: RA2ne,
// RA2-256, RA2, VeNCrypt (X509 before TLS before Plain), Apple DH, VNC
// password, None.
var supportedSecurity = []SecurityType{
	SecurityRA2ne, SecurityRA2_256, SecurityRA2, SecurityVeNCrypt,
	SecurityAppleDH, SecurityVNCAuth, SecurityNone,
}

// supportedSecurityRouted is the preference order when the stream is already
// encrypted by a tunnel. The tunnel replaces native TLS, so the client must not
// pick a VeNCrypt TLS/X509 subtype. The non-VeNCrypt types are preferred first
// because their subtype is not negotiated behind another round trip: choosing
// type 19 commits the client to VeNCrypt before its subtype list is known, and
// a server that then offers only TLS subtypes leaves no acceptable choice. A
// routed Host may still use VeNCrypt Plain (the tunnel protects the plaintext
// credentials), but it sits just above None because preferring Apple DH or the
// VNC password first avoids that unrecoverable commitment when the server
// might offer only TLS subtypes. This ordering is the deliberate deviation
// from the unrouted table; it never falls back to adding an inner TLS layer.
var supportedSecurityRouted = []SecurityType{
	SecurityRA2ne, SecurityRA2_256, SecurityRA2,
	SecurityAppleDH, SecurityVNCAuth, SecurityVeNCrypt, SecurityNone,
}

// supportedVeNCryptSubtypes is the client's preference order inside VeNCrypt,
// strongest first: authenticated X509 variants, then anonymous TLS variants,
// then the unencrypted Plain subtype. Within a group X509Plain/TLSPlain are
// preferred because they authenticate an account name as well as a password.
var supportedVeNCryptSubtypes = []VeNCryptSubtype{
	VeNCryptX509Plain, VeNCryptX509Vnc,
	VeNCryptTLSPlain, VeNCryptTLSVnc,
	VeNCryptPlain,
}

// supportedVeNCryptSubtypesRouted is the only VeNCrypt subtype a routed Host
// may use. TLS and X509 would add a second encryption layer; Plain runs inside
// the tunnel.
var supportedVeNCryptSubtypesRouted = []VeNCryptSubtype{VeNCryptPlain}

// SelectSecurity returns the strongest security type both this client and the
// server support for a Host with no route.
func SelectSecurity(offered []SecurityType) (SecurityType, bool) {
	return selectSecurity(offered, supportedSecurity)
}

// SelectSecurityRouted returns the strongest security type this client and a
// routed server support without adding a second TLS layer.
func SelectSecurityRouted(offered []SecurityType) (SecurityType, bool) {
	return selectSecurity(offered, supportedSecurityRouted)
}

// SelectSecurityFor dispatches on the route decision.
func SelectSecurityFor(offered []SecurityType, routed bool) (SecurityType, bool) {
	if routed {
		return SelectSecurityRouted(offered)
	}
	return SelectSecurity(offered)
}

func selectSecurity(offered, preference []SecurityType) (SecurityType, bool) {
	for _, want := range preference {
		for _, got := range offered {
			if got == want {
				return want, true
			}
		}
	}
	return SecurityInvalid, false
}

// SelectVeNCryptSubtype returns the strongest subtype both this client and the
// server support for an unrouted Host.
func SelectVeNCryptSubtype(offered []VeNCryptSubtype) (VeNCryptSubtype, bool) {
	return selectVeNCryptSubtype(offered, supportedVeNCryptSubtypes)
}

// SelectVeNCryptSubtypeRouted returns the strongest VeNCrypt subtype that does
// not add a TLS layer for a routed Host.
func SelectVeNCryptSubtypeRouted(offered []VeNCryptSubtype) (VeNCryptSubtype, bool) {
	return selectVeNCryptSubtype(offered, supportedVeNCryptSubtypesRouted)
}

// SelectVeNCryptSubtypeFor dispatches on the route decision.
func SelectVeNCryptSubtypeFor(offered []VeNCryptSubtype, routed bool) (VeNCryptSubtype, bool) {
	if routed {
		return SelectVeNCryptSubtypeRouted(offered)
	}
	return SelectVeNCryptSubtype(offered)
}

func selectVeNCryptSubtype(offered, preference []VeNCryptSubtype) (VeNCryptSubtype, bool) {
	for _, want := range preference {
		for _, got := range offered {
			if got == want {
				return want, true
			}
		}
	}
	return 0, false
}
