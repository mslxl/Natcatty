package rfb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// readWriter joins a separate reader and writer, the shape the security tests
// use for a synthetic stream.
type readWriter struct {
	io.Reader
	io.Writer
}

// negotiateSecurity performs the security half of the handshake. It returns the
// negotiated choice, the stream and buffered reader the rest of the protocol
// must use. TLS and the encrypted RA2 variants replace the stream; every other
// type keeps the original connection.
func negotiateSecurity(conn io.ReadWriter, r *bufio.Reader, version Version, cfg ClientConfig) (SecurityChoice, io.ReadWriter, *bufio.Reader, error) {
	if version.Minor < Version3_7.Minor {
		return negotiateSecurity33(conn, r, version, cfg)
	}
	var count [1]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		return SecurityChoice{}, nil, nil, fmt.Errorf("%w: reading security count: %w", ErrHandshake, err)
	}
	if count[0] == 0 {
		// RFB 3.7 and 3.8 both send a length-prefixed reason string here.
		return SecurityChoice{}, nil, nil, &SecurityError{Rejected: true, Reason: readReason(r)}
	}
	wire := make([]byte, int(count[0]))
	if _, err := io.ReadFull(r, wire); err != nil {
		return SecurityChoice{}, nil, nil, fmt.Errorf("%w: reading security types: %w", ErrHandshake, err)
	}
	offered := make([]SecurityType, len(wire))
	for i := range offered {
		offered[i] = SecurityType(wire[i])
	}
	selected, ok := SelectSecurityFor(offered, cfg.Routed)
	if !ok {
		return SecurityChoice{}, nil, nil, &SecurityError{Offered: offered}
	}

	if selected == SecurityVeNCrypt {
		// VeNCrypt is selected before its subtype list is known.
		if err := writeAll(conn, []byte{byte(selected)}); err != nil {
			return SecurityChoice{}, nil, nil, fmt.Errorf("%w: writing security type: %w", ErrHandshake, err)
		}
		stream, reader, choice, err := vencryptAuthenticate(conn, r, cfg)
		if err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		if err := readSecurityResult(reader, version, offered); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		return choice, stream, reader, nil
	}

	choice := SecurityChoice{Type: selected}
	if selected != SecurityNone && securityNeedsPassword(selected) && cfg.Password == "" {
		return SecurityChoice{}, nil, nil, &SecurityError{Offered: offered, MissingPassword: true}
	}
	// Consent is requested before the selection byte so a declined plaintext
	// session sends nothing at all.
	if err := cfg.securityGate(choice); err != nil {
		return SecurityChoice{}, nil, nil, err
	}
	if err := writeAll(conn, []byte{byte(selected)}); err != nil {
		return SecurityChoice{}, nil, nil, fmt.Errorf("%w: writing security type: %w", ErrHandshake, err)
	}

	switch selected {
	case SecurityNone:
		// RFB 3.7 with None proceeds directly to ClientInit (RFC 6143 A.2);
		// 3.8 reports a SecurityResult.
		if version.Minor >= Version3_8.Minor {
			if err := readSecurityResult(r, version, offered); err != nil {
				return SecurityChoice{}, nil, nil, err
			}
		}
		return choice, conn, r, nil
	case SecurityVNCAuth:
		if err := vncAuthenticate(r, conn, cfg.Password); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
	case SecurityAppleDH:
		if err := appleDHAuthenticate(r, conn, cfg.Username, cfg.Password); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
	case SecurityRA2, SecurityRA2ne, SecurityRA2_256, SecurityRA2ne_256:
		stream, reader, err := ra2Authenticate(conn, r, selected, cfg)
		if err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		if err := readSecurityResult(reader, version, offered); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		return choice, stream, reader, nil
	default:
		return SecurityChoice{}, nil, nil, &SecurityError{Offered: offered}
	}
	if err := readSecurityResult(r, version, offered); err != nil {
		return SecurityChoice{}, nil, nil, err
	}
	return choice, conn, r, nil
}

// negotiateSecurity33 handles RFB 3.3, where the server dictates one security
// type with a U32 and the client does not select.
func negotiateSecurity33(conn io.ReadWriter, r *bufio.Reader, version Version, cfg ClientConfig) (SecurityChoice, io.ReadWriter, *bufio.Reader, error) {
	var wire [4]byte
	if _, err := io.ReadFull(r, wire[:]); err != nil {
		return SecurityChoice{}, nil, nil, fmt.Errorf("%w: reading security type: %w", ErrHandshake, err)
	}
	offered := SecurityType(binary.BigEndian.Uint32(wire[:]))
	if offered == SecurityInvalid {
		return SecurityChoice{}, nil, nil, &SecurityError{Rejected: true, Reason: readReason(r)}
	}
	choice := SecurityChoice{Type: offered}
	switch offered {
	case SecurityNone:
		if err := cfg.securityGate(choice); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		return choice, conn, r, nil
	case SecurityVNCAuth:
		if cfg.Password == "" {
			return SecurityChoice{}, nil, nil, &SecurityError{Offered: []SecurityType{offered}, MissingPassword: true}
		}
		if err := cfg.securityGate(choice); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		if err := vncAuthenticate(r, conn, cfg.Password); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
	case SecurityAppleDH:
		if cfg.Password == "" {
			return SecurityChoice{}, nil, nil, &SecurityError{Offered: []SecurityType{offered}, MissingPassword: true}
		}
		if err := cfg.securityGate(choice); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		if err := appleDHAuthenticate(r, conn, cfg.Username, cfg.Password); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
	case SecurityVeNCrypt:
		stream, reader, selected, err := vencryptAuthenticate(conn, r, cfg)
		if err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		if err := readSecurityResult(reader, version, []SecurityType{offered}); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		return selected, stream, reader, nil
	case SecurityRA2, SecurityRA2ne, SecurityRA2_256, SecurityRA2ne_256:
		if cfg.Password == "" {
			return SecurityChoice{}, nil, nil, &SecurityError{Offered: []SecurityType{offered}, MissingPassword: true}
		}
		if err := cfg.securityGate(choice); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		stream, reader, err := ra2Authenticate(conn, r, offered, cfg)
		if err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		if err := readSecurityResult(reader, version, []SecurityType{offered}); err != nil {
			return SecurityChoice{}, nil, nil, err
		}
		return choice, stream, reader, nil
	default:
		return SecurityChoice{}, nil, nil, &SecurityError{Offered: []SecurityType{offered}}
	}
	if err := readSecurityResult(r, version, []SecurityType{offered}); err != nil {
		return SecurityChoice{}, nil, nil, err
	}
	return choice, conn, r, nil
}

// securityNeedsPassword reports whether a top-level type authenticates with a
// password. VeNCrypt decides per subtype, so it is handled separately.
func securityNeedsPassword(security SecurityType) bool {
	switch security {
	case SecurityVNCAuth, SecurityAppleDH,
		SecurityRA2, SecurityRA2ne, SecurityRA2r,
		SecurityRA2_256, SecurityRA2ne_256, SecurityRA2r_256:
		return true
	default:
		return false
	}
}

// securityGate invokes the caller's consent callback, marking the failure as a
// decline while preserving the callback's own error for errors.Is.
func (cfg ClientConfig) securityGate(choice SecurityChoice) error {
	if cfg.SecurityGate == nil {
		return nil
	}
	if err := cfg.SecurityGate(choice); err != nil {
		return fmt.Errorf("%w: %w", ErrSecurityDeclined, err)
	}
	return nil
}
