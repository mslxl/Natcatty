package rfb

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// VeNCrypt (security type 19) wraps a version handshake, a subtype list and an
// inner authentication exchange. Subtypes 256-262 use the registered rfbproto
// numbers:
//
//	Plain     256  username+password in the clear (no TLS)
//	TLSNone   257  anonymous TLS, no inner authentication
//	TLSVnc    258  anonymous TLS, then the VNC password challenge
//	TLSPlain  259  anonymous TLS, then username+password
//	X509None  260  authenticated TLS, no inner authentication
//	X509Vnc   261  authenticated TLS, then the VNC password challenge
//	X509Plain 262  authenticated TLS, then username+password
//
// The TLS-prefixed subtypes are anonymous by protocol: there is no server
// identity to verify. The X509-prefixed subtypes present a certificate and are
// always verified against the system trust store and the Host name. No
// configuration can relax that check.

// systemRoots loads the platform trust store. A test may replace it to inject a
// generated CA; production always uses the system store.
var systemRoots = x509.SystemCertPool

// vencryptAuthenticate negotiates the container, selects a subtype, runs the
// inner authentication and returns the stream and reader the rest of the
// handshake must use.
func vencryptAuthenticate(conn io.ReadWriter, r *bufio.Reader, cfg ClientConfig) (io.ReadWriter, *bufio.Reader, SecurityChoice, error) {
	var version [2]byte
	if _, err := io.ReadFull(r, version[:]); err != nil {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: reading VeNCrypt version: %w", ErrHandshake, err)
	}
	if version[0] != 0 || version[1] < 2 {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: unsupported VeNCrypt version %d.%d",
			ErrHandshake, version[0], version[1])
	}
	if err := writeAll(conn, []byte{0, 2}); err != nil {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: writing VeNCrypt version: %w", ErrHandshake, err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(r, ack[:]); err != nil {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: reading VeNCrypt version ack: %w", ErrHandshake, err)
	}
	if ack[0] != 0 {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: VeNCrypt version %d.%d rejected", ErrHandshake, version[0], version[1])
	}

	var count [1]byte
	if _, err := io.ReadFull(r, count[:]); err != nil {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: reading VeNCrypt subtype count: %w", ErrHandshake, err)
	}
	if count[0] == 0 {
		return nil, nil, SecurityChoice{}, &SecurityError{Offered: []SecurityType{SecurityVeNCrypt}}
	}
	wire := make([]byte, 4*int(count[0]))
	if _, err := io.ReadFull(r, wire); err != nil {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: reading VeNCrypt subtypes: %w", ErrHandshake, err)
	}
	offered := make([]VeNCryptSubtype, int(count[0]))
	for i := range offered {
		offered[i] = VeNCryptSubtype(binary.BigEndian.Uint32(wire[4*i:]))
	}
	subtype, ok := SelectVeNCryptSubtypeFor(offered, cfg.Routed)
	if !ok {
		return nil, nil, SecurityChoice{}, &SecurityError{
			Offered:  []SecurityType{SecurityVeNCrypt},
			Subtypes: offered,
		}
	}
	choice := SecurityChoice{Type: SecurityVeNCrypt, Subtype: subtype}
	if vencryptNeedsPassword(subtype) && cfg.Password == "" {
		return nil, nil, SecurityChoice{}, &SecurityError{
			Offered:         []SecurityType{SecurityVeNCrypt},
			Subtypes:        offered,
			MissingPassword: true,
		}
	}
	// Consent is requested before the subtype is accepted and before any
	// credential or TLS handshake starts. A routed Host never adds TLS, so the
	// selection itself already excludes those subtypes.
	if err := cfg.securityGate(choice); err != nil {
		return nil, nil, SecurityChoice{}, err
	}
	var selection [4]byte
	binary.BigEndian.PutUint32(selection[:], uint32(subtype))
	if err := writeAll(conn, selection[:]); err != nil {
		return nil, nil, SecurityChoice{}, fmt.Errorf("%w: writing VeNCrypt subtype: %w", ErrHandshake, err)
	}

	stream := conn
	reader := r
	if vencryptUsesTLS(subtype) {
		var tlsAck [1]byte
		if _, err := io.ReadFull(r, tlsAck[:]); err != nil {
			return nil, nil, SecurityChoice{}, fmt.Errorf("%w: reading VeNCrypt TLS ack: %w", ErrHandshake, err)
		}
		if tlsAck[0] != 1 {
			return nil, nil, SecurityChoice{}, fmt.Errorf("%w: VeNCrypt TLS subtype %v rejected", ErrHandshake, subtype)
		}
		tlsConn, err := startVeNCryptTLS(conn, r, subtype, cfg.ServerName)
		if err != nil {
			return nil, nil, SecurityChoice{}, err
		}
		stream = tlsConn
		reader = bufio.NewReader(tlsConn)
	}

	switch subtype {
	case VeNCryptPlain, VeNCryptTLSPlain, VeNCryptX509Plain:
		if err := writeVeNCryptPlain(stream, cfg.Username, cfg.Password); err != nil {
			return nil, nil, SecurityChoice{}, err
		}
	case VeNCryptTLSVnc, VeNCryptX509Vnc:
		if err := vncAuthenticate(reader, stream, cfg.Password); err != nil {
			return nil, nil, SecurityChoice{}, err
		}
	default:
		return nil, nil, SecurityChoice{}, &SecurityError{
			Offered:  []SecurityType{SecurityVeNCrypt},
			Subtypes: offered,
		}
	}
	return stream, reader, choice, nil
}

func vencryptNeedsPassword(subtype VeNCryptSubtype) bool {
	switch subtype {
	case VeNCryptPlain, VeNCryptTLSPlain, VeNCryptX509Plain,
		VeNCryptTLSVnc, VeNCryptX509Vnc:
		return true
	default:
		return false
	}
}

func vencryptUsesTLS(subtype VeNCryptSubtype) bool {
	switch subtype {
	case VeNCryptTLSPlain, VeNCryptTLSVnc, VeNCryptX509Plain, VeNCryptX509Vnc,
		VeNCryptTLSNone, VeNCryptX509None:
		return true
	default:
		return false
	}
}

// writeVeNCryptPlain sends the U32-length-prefixed username and password the
// Plain family uses.
func writeVeNCryptPlain(w io.Writer, username, password string) error {
	if uint64(len(username)) > uint64(^uint32(0)) || uint64(len(password)) > uint64(^uint32(0)) {
		return fmt.Errorf("%w: VeNCrypt credential is too long", ErrOversize)
	}
	wire := make([]byte, 8+len(username)+len(password))
	binary.BigEndian.PutUint32(wire[0:4], uint32(len(username)))
	binary.BigEndian.PutUint32(wire[4:8], uint32(len(password)))
	copy(wire[8:], username)
	copy(wire[8+len(username):], password)
	if err := writeAll(w, wire); err != nil {
		return fmt.Errorf("%w: writing VeNCrypt credentials: %w", ErrHandshake, err)
	}
	return nil
}

// startVeNCryptTLS runs the TLS handshake on the VeNCrypt stream. The reader is
// the existing buffered reader so any bytes it already pulled from the socket
// are not lost; TLS starts on a message boundary and the server never writes
// before the client's ClientHello.
func startVeNCryptTLS(conn io.ReadWriter, reader io.Reader, subtype VeNCryptSubtype, serverName string) (*tls.Conn, error) {
	config := &tls.Config{
		ServerName: serverName,
		MinVersion: tls.VersionTLS12,
	}
	switch subtype {
	case VeNCryptX509Plain, VeNCryptX509Vnc:
		if serverName == "" {
			return nil, fmt.Errorf("%w: the Host name is required to verify a VeNCrypt X509 certificate", ErrTLSVerification)
		}
		roots, err := systemRoots()
		if err != nil {
			return nil, fmt.Errorf("%w: loading the system trust store: %w", ErrTLSVerification, err)
		}
		// A nil pool means the platform has no explicit pool; crypto/tls then
		// uses its built-in system roots.
		config.RootCAs = roots
	case VeNCryptTLSPlain, VeNCryptTLSVnc:
		// Anonymous TLS: the protocol defines no server certificate to verify.
		// This is the only place the client skips certificate verification.
		config.InsecureSkipVerify = true
	default:
		// Any other subtype must not fall through to anonymous TLS. An unknown
		// or future X509 subtype fails closed rather than silently skipping the
		// certificate check.
		return nil, fmt.Errorf("%w: VeNCrypt subtype %v has no TLS verification policy",
			ErrUnsupportedSecurity, subtype)
	}

	stream := &tlsStream{reader: reader, writer: conn, raw: conn}
	tlsConn := tls.Client(stream, config)
	if err := tlsConn.Handshake(); err != nil {
		var verifyErr *tls.CertificateVerificationError
		if errors.As(err, &verifyErr) {
			return nil, fmt.Errorf("%w: %w", ErrTLSVerification, err)
		}
		return nil, fmt.Errorf("%w: VeNCrypt TLS handshake: %w", ErrHandshake, err)
	}
	return tlsConn, nil
}

// tlsStream adapts the RFB reader/writer seam to the net.Conn crypto/tls needs.
// Reads go through the existing buffered reader so buffered bytes survive the
// protocol switch; writes go straight to the connection.
type tlsStream struct {
	reader io.Reader
	writer io.Writer
	raw    io.ReadWriter
}

func (s *tlsStream) Read(p []byte) (int, error)  { return s.reader.Read(p) }
func (s *tlsStream) Write(p []byte) (int, error) { return s.writer.Write(p) }

func (s *tlsStream) Close() error {
	if closer, ok := s.raw.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func (s *tlsStream) LocalAddr() net.Addr {
	if conn, ok := s.raw.(net.Conn); ok {
		return conn.LocalAddr()
	}
	return nil
}

func (s *tlsStream) RemoteAddr() net.Addr {
	if conn, ok := s.raw.(net.Conn); ok {
		return conn.RemoteAddr()
	}
	return nil
}

func (s *tlsStream) SetDeadline(t time.Time) error {
	if conn, ok := s.raw.(interface{ SetDeadline(time.Time) error }); ok {
		return conn.SetDeadline(t)
	}
	return nil
}

func (s *tlsStream) SetReadDeadline(t time.Time) error {
	if conn, ok := s.raw.(interface{ SetReadDeadline(time.Time) error }); ok {
		return conn.SetReadDeadline(t)
	}
	return nil
}

func (s *tlsStream) SetWriteDeadline(t time.Time) error {
	if conn, ok := s.raw.(interface{ SetWriteDeadline(time.Time) error }); ok {
		return conn.SetWriteDeadline(t)
	}
	return nil
}
