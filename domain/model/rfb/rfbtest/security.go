package rfbtest

import (
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net"

	"github.com/mslxl/natcatty/domain/model/rfb"
)

// exchangeAppleDH performs Apple's Diffie-Hellman server half (security type
// 30). It records the credentials and verifies them against the configured
// username and password.
func (s *Server) exchangeAppleDH(conn io.ReadWriter) error {
	keyBytes := s.cfg.AppleDHKeyBytes
	if keyBytes == 0 {
		keyBytes = 128
	}
	prime, err := rand.Prime(rand.Reader, keyBytes*8)
	if err != nil {
		return err
	}
	privateBytes := make([]byte, keyBytes)
	if _, err := io.ReadFull(rand.Reader, privateBytes); err != nil {
		return err
	}
	private := new(big.Int).SetBytes(privateBytes)
	generator := big.NewInt(2)

	primeBytes := prime.FillBytes(make([]byte, keyBytes))
	serverPublic := new(big.Int).Exp(generator, private, prime)
	serverPublicBytes := serverPublic.FillBytes(make([]byte, keyBytes))
	params := make([]byte, 0, 4+2*keyBytes)
	var header [4]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(generator.Int64()))
	binary.BigEndian.PutUint16(header[2:4], uint16(keyBytes))
	params = append(params, header[:]...)
	params = append(params, primeBytes...)
	params = append(params, serverPublicBytes...)
	if err := writeAll(conn, params); err != nil {
		return err
	}

	response := make([]byte, 128+keyBytes)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	encrypted := response[:128]
	clientPublicBytes := response[128:]
	clientPublic := new(big.Int).SetBytes(clientPublicBytes)
	shared := new(big.Int).Exp(clientPublic, private, prime)
	key := appleDHDeriveAESKey(shared, keyBytes)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	plain := make([]byte, 128)
	for offset := 0; offset < 128; offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], encrypted[offset:offset+aes.BlockSize])
	}
	username := cString(plain[:64])
	password := cString(plain[64:])
	s.mu.Lock()
	s.received.AppleDHUsername = username
	s.received.AppleDHAccepted = s.credentialsMatch(username, password)
	accepted := s.received.AppleDHAccepted
	s.mu.Unlock()
	if !accepted {
		return fmt.Errorf("rfbtest: Apple DH credentials rejected")
	}
	return nil
}

func cString(data []byte) string {
	for i, b := range data {
		if b == 0 {
			return string(data[:i])
		}
	}
	return string(data)
}

// appleDHDeriveAESKey independently implements Apple's shared-key derivation:
// MD5 over the big-endian shared secret zero-padded to keyBytes. It deliberately
// does not call the client's helper, so a bug in the production KDF cannot be
// mirrored by the test server and go unnoticed.
func appleDHDeriveAESKey(shared *big.Int, keyBytes int) [16]byte {
	return md5.Sum(shared.FillBytes(make([]byte, keyBytes)))
}

// credentialsMatch applies the configured credential checks. An empty expected
// value accepts anything, so a test can exercise one credential at a time.
func (s *Server) credentialsMatch(username, password string) bool {
	if s.cfg.Username != "" && username != s.cfg.Username {
		return false
	}
	if s.cfg.Password != "" && password != s.cfg.Password {
		return false
	}
	return true
}

func vencryptUsesTLS(subtype rfb.VeNCryptSubtype) bool {
	switch subtype {
	case rfb.VeNCryptTLSPlain, rfb.VeNCryptTLSVnc, rfb.VeNCryptX509Plain, rfb.VeNCryptX509Vnc:
		return true
	default:
		return false
	}
}

func vencryptPlainFamily(subtype rfb.VeNCryptSubtype) bool {
	return subtype == rfb.VeNCryptPlain || subtype == rfb.VeNCryptTLSPlain || subtype == rfb.VeNCryptX509Plain
}

func vencryptVNCFamily(subtype rfb.VeNCryptSubtype) bool {
	return subtype == rfb.VeNCryptTLSVnc || subtype == rfb.VeNCryptX509Vnc
}

// exchangeVeNCrypt performs the VeNCrypt container negotiation and the selected
// inner authentication. It returns the TLS-wrapped stream when one is used.
func (s *Server) exchangeVeNCrypt(conn io.ReadWriter, version rfb.Version) (io.ReadWriter, error) {
	if err := writeAll(conn, []byte{0, 2}); err != nil {
		return nil, err
	}
	var clientVersion [2]byte
	if _, err := io.ReadFull(conn, clientVersion[:]); err != nil {
		return nil, err
	}
	if clientVersion[0] != 0 || clientVersion[1] != 2 {
		return nil, fmt.Errorf("rfbtest: unsupported VeNCrypt version %d.%d", clientVersion[0], clientVersion[1])
	}
	if err := writeAll(conn, []byte{0}); err != nil {
		return nil, err
	}

	offered := s.cfg.VeNCryptSubtypes
	if len(offered) == 0 {
		offered = []rfb.VeNCryptSubtype{rfb.VeNCryptPlain}
		if s.cfg.TLSConfig != nil {
			offered = []rfb.VeNCryptSubtype{
				rfb.VeNCryptPlain, rfb.VeNCryptTLSPlain, rfb.VeNCryptX509Plain,
				rfb.VeNCryptTLSVnc, rfb.VeNCryptX509Vnc,
			}
		}
	}
	wire := make([]byte, 1+4*len(offered))
	wire[0] = byte(len(offered))
	for i, subtype := range offered {
		binary.BigEndian.PutUint32(wire[1+4*i:], uint32(subtype))
	}
	if err := writeAll(conn, wire); err != nil {
		return nil, err
	}
	var selection [4]byte
	if _, err := io.ReadFull(conn, selection[:]); err != nil {
		return nil, err
	}
	subtype := rfb.VeNCryptSubtype(binary.BigEndian.Uint32(selection[:]))
	allowed := false
	for _, candidate := range offered {
		allowed = allowed || candidate == subtype
	}
	if !allowed {
		_ = writeSecurityResult(conn, version, false, "unsupported VeNCrypt subtype")
		return nil, fmt.Errorf("rfbtest: client selected VeNCrypt subtype %d", subtype)
	}
	s.mu.Lock()
	s.received.VeNCryptSubtype = subtype
	s.received.SecurityChoice = rfb.SecurityChoice{Type: rfb.SecurityVeNCrypt, Subtype: subtype}
	s.mu.Unlock()

	stream := conn
	if vencryptUsesTLS(subtype) {
		if err := writeAll(conn, []byte{1}); err != nil {
			return nil, err
		}
		netConn, ok := conn.(net.Conn)
		if !ok {
			return nil, fmt.Errorf("rfbtest: VeNCrypt TLS needs a net.Conn")
		}
		if s.cfg.TLSConfig == nil {
			return nil, fmt.Errorf("rfbtest: VeNCrypt TLS subtype without a TLSConfig")
		}
		tlsConn := tls.Server(netConn, s.cfg.TLSConfig)
		if err := tlsConn.Handshake(); err != nil {
			return nil, err
		}
		stream = tlsConn
		s.mu.Lock()
		s.received.TLSStarted = true
		s.mu.Unlock()
	}

	switch {
	case vencryptPlainFamily(subtype):
		if err := s.readPlainCredentials(stream); err != nil {
			_ = writeSecurityResult(stream, version, false, "authentication failed")
			return nil, err
		}
	case vencryptVNCFamily(subtype):
		if err := s.exchangeVNCAuth(stream); err != nil {
			_ = writeSecurityResult(stream, version, false, "authentication failed")
			return nil, err
		}
	default:
		_ = writeSecurityResult(stream, version, false, "unsupported VeNCrypt subtype")
		return nil, fmt.Errorf("rfbtest: cannot perform VeNCrypt subtype %d", subtype)
	}
	return stream, writeSecurityResult(stream, version, true, "")
}

// readPlainCredentials reads the U32-length-prefixed username and password of
// the VeNCrypt Plain family and records them.
func (s *Server) readPlainCredentials(conn io.Reader) error {
	var header [8]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	usernameLength := int(binary.BigEndian.Uint32(header[0:4]))
	passwordLength := int(binary.BigEndian.Uint32(header[4:8]))
	if usernameLength > 1<<16 || passwordLength > 1<<16 {
		return fmt.Errorf("rfbtest: VeNCrypt credential too long")
	}
	buffer := make([]byte, usernameLength+passwordLength)
	if _, err := io.ReadFull(conn, buffer); err != nil {
		return err
	}
	username := string(buffer[:usernameLength])
	password := string(buffer[usernameLength:])
	s.mu.Lock()
	s.received.Username = username
	s.received.Password = password
	accepted := s.credentialsMatch(username, password)
	s.mu.Unlock()
	if !accepted {
		return fmt.Errorf("rfbtest: VeNCrypt credentials rejected")
	}
	return nil
}

// ra2Params mirrors the client's variant table.
type ra2Params struct {
	bits      int
	encrypted bool
	sha256    bool
}

func ra2ParamsFor(security rfb.SecurityType) (ra2Params, bool) {
	switch security {
	case rfb.SecurityRA2ne:
		return ra2Params{bits: 128}, true
	case rfb.SecurityRA2:
		return ra2Params{bits: 128, encrypted: true}, true
	case rfb.SecurityRA2ne_256:
		return ra2Params{bits: 256, sha256: true}, true
	case rfb.SecurityRA2_256:
		return ra2Params{bits: 256, encrypted: true, sha256: true}, true
	default:
		return ra2Params{}, false
	}
}

// exchangeRA2 performs the server half of an RA2 handshake and returns the
// AES-EAX-wrapped stream for the encrypted variants.
func (s *Server) exchangeRA2(conn io.ReadWriter, version rfb.Version, security rfb.SecurityType) (io.ReadWriter, error) {
	params, ok := ra2ParamsFor(security)
	if !ok {
		return nil, fmt.Errorf("rfbtest: cannot perform security type %d", security)
	}
	keyBits := s.cfg.RA2KeyBits
	if keyBits == 0 {
		keyBits = 1024
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, keyBits)
	if err != nil {
		return nil, err
	}
	size := (keyBits + 7) / 8
	serverModulus := serverKey.PublicKey.N.FillBytes(make([]byte, size))
	serverExponent := big.NewInt(int64(serverKey.PublicKey.E)).FillBytes(make([]byte, size))
	serverKeyWire := ra2KeyWire(uint32(keyBits), serverModulus, serverExponent)
	if err := writeAll(conn, serverKeyWire); err != nil {
		return nil, err
	}

	var clientHeader [4]byte
	if _, err := io.ReadFull(conn, clientHeader[:]); err != nil {
		return nil, err
	}
	clientBits := binary.BigEndian.Uint32(clientHeader[:])
	if clientBits != uint32(keyBits) {
		return nil, fmt.Errorf("rfbtest: client RA2 key is %d bits, want %d", clientBits, keyBits)
	}
	clientModulus := make([]byte, size)
	clientExponent := make([]byte, size)
	if _, err := io.ReadFull(conn, clientModulus); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(conn, clientExponent); err != nil {
		return nil, err
	}
	clientPublic := &rsa.PublicKey{
		N: new(big.Int).SetBytes(clientModulus),
		E: int(new(big.Int).SetBytes(clientExponent).Int64()),
	}
	clientKeyWire := ra2KeyWire(clientBits, clientModulus, clientExponent)

	randomSize := params.bits / 8
	// Read the client's random before writing the server random. TigerVNC's
	// server writes first, which works on a buffered TCP socket; an in-process
	// net.Pipe is synchronous, and reading first is equivalent because the
	// client generates its random before it reads the server's.
	var randomHeader [2]byte
	if _, err := io.ReadFull(conn, randomHeader[:]); err != nil {
		return nil, err
	}
	clientRandomLength := int(binary.BigEndian.Uint16(randomHeader[:]))
	if clientRandomLength != size {
		return nil, fmt.Errorf("rfbtest: client random is %d bytes, want %d", clientRandomLength, size)
	}
	encryptedClientRandom := make([]byte, clientRandomLength)
	if _, err := io.ReadFull(conn, encryptedClientRandom); err != nil {
		return nil, err
	}
	clientRandom, err := rsa.DecryptPKCS1v15(rand.Reader, serverKey, encryptedClientRandom)
	if err != nil {
		return nil, err
	}
	if len(clientRandom) != randomSize {
		return nil, fmt.Errorf("rfbtest: client random is %d bytes, want %d", len(clientRandom), randomSize)
	}

	serverRandom := make([]byte, randomSize)
	if _, err := io.ReadFull(rand.Reader, serverRandom); err != nil {
		return nil, err
	}
	encryptedServerRandom, err := rsa.EncryptPKCS1v15(rand.Reader, clientPublic, serverRandom)
	if err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint16(randomHeader[:], uint16(len(encryptedServerRandom)))
	if err := writeAll(conn, randomHeader[:]); err != nil {
		return nil, err
	}
	if err := writeAll(conn, encryptedServerRandom); err != nil {
		return nil, err
	}

	// The server reads with the client-to-server key H(ServerRandom ||
	// ClientRandom) and writes with the server-to-client key H(ClientRandom ||
	// ServerRandom), mirroring the client's direction split.
	readKey := ra2ServerKey(params, serverRandom, clientRandom)
	writeKey := ra2ServerKey(params, clientRandom, serverRandom)
	session := newRA2ServerSession(readKey, writeKey)

	clientHash, err := session.readMessage(conn)
	if err != nil {
		return nil, err
	}
	wantClientHash := ra2ServerHash(params, clientKeyWire, serverKeyWire)
	if subtle.ConstantTimeCompare(clientHash, wantClientHash) != 1 {
		return nil, fmt.Errorf("rfbtest: client key hash does not match")
	}
	if err := session.writeMessage(conn, ra2ServerHash(params, serverKeyWire, clientKeyWire)); err != nil {
		return nil, err
	}

	subtype := byte(2)
	if s.cfg.Username != "" {
		subtype = 1
	}
	s.mu.Lock()
	s.received.RA2Subtype = subtype
	s.mu.Unlock()
	if err := session.writeMessage(conn, []byte{subtype}); err != nil {
		return nil, err
	}
	credentials, err := session.readMessage(conn)
	if err != nil {
		return nil, err
	}
	username, password, err := parseRA2Credentials(credentials)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.received.RA2Username = username
	s.received.RA2Password = password
	s.received.RA2Accepted = s.credentialsMatch(username, password)
	accepted := s.received.RA2Accepted
	s.mu.Unlock()

	// The failure result travels over the same layer the credentials used. For
	// the encrypted variants that is the AES-EAX stream; writing it to the raw
	// connection would make the client's frame reader see malformed data instead
	// of the SecurityResult.
	stream := conn
	if params.encrypted {
		stream = &serverEAXStream{session: session, conn: conn}
	}
	if !accepted {
		_ = writeSecurityResult(stream, version, false, "authentication failed")
		return nil, fmt.Errorf("rfbtest: RA2 credentials rejected")
	}
	return stream, writeSecurityResult(stream, version, true, "")
}

func ra2KeyWire(bits uint32, modulus, exponent []byte) []byte {
	wire := make([]byte, 4+len(modulus)+len(exponent))
	binary.BigEndian.PutUint32(wire[0:4], bits)
	copy(wire[4:], modulus)
	copy(wire[4+len(modulus):], exponent)
	return wire
}

func ra2ServerKey(params ra2Params, first, second []byte) []byte {
	if params.sha256 {
		sum := sha256.Sum256(append(append([]byte(nil), first...), second...))
		return sum[:]
	}
	sum := sha1.Sum(append(append([]byte(nil), first...), second...))
	return sum[:16]
}

func ra2ServerHash(params ra2Params, first, second []byte) []byte {
	var h hash.Hash
	if params.sha256 {
		h = sha256.New()
	} else {
		h = sha1.New()
	}
	h.Write(first)
	h.Write(second)
	return h.Sum(nil)
}

func parseRA2Credentials(credentials []byte) (string, string, error) {
	if len(credentials) < 2 {
		return "", "", fmt.Errorf("rfbtest: RA2 credentials are too short")
	}
	usernameLength := int(credentials[0])
	if len(credentials) < 1+usernameLength+1 {
		return "", "", fmt.Errorf("rfbtest: RA2 credentials are truncated")
	}
	username := string(credentials[1 : 1+usernameLength])
	offset := 1 + usernameLength
	passwordLength := int(credentials[offset])
	offset++
	if len(credentials) < offset+passwordLength {
		return "", "", fmt.Errorf("rfbtest: RA2 password is truncated")
	}
	return username, string(credentials[offset : offset+passwordLength]), nil
}

// ra2ServerSession is the server's AES-EAX direction pair.
type ra2ServerSession struct {
	readKey, writeKey     []byte
	readIndex, writeIndex [16]byte
}

func newRA2ServerSession(readKey, writeKey []byte) *ra2ServerSession {
	return &ra2ServerSession{readKey: readKey, writeKey: writeKey}
}

func (s *ra2ServerSession) readMessage(r io.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(header[:]))
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(r, ciphertext); err != nil {
		return nil, err
	}
	tag := make([]byte, 16)
	if _, err := io.ReadFull(r, tag); err != nil {
		return nil, err
	}
	nonce := s.readIndex
	incrementLittleEndian(&s.readIndex)
	return rfb.AESEAXOpen(s.readKey, nonce[:], header[:], ciphertext, tag)
}

func (s *ra2ServerSession) writeMessage(w io.Writer, message []byte) error {
	for len(message) > 0 {
		length := len(message)
		if length > 8192 {
			length = 8192
		}
		var header [2]byte
		binary.BigEndian.PutUint16(header[:], uint16(length))
		nonce := s.writeIndex
		incrementLittleEndian(&s.writeIndex)
		ciphertext, tag, err := rfb.AESEAXSeal(s.writeKey, nonce[:], header[:], message[:length])
		if err != nil {
			return err
		}
		if err := writeAll(w, header[:]); err != nil {
			return err
		}
		if err := writeAll(w, ciphertext); err != nil {
			return err
		}
		if err := writeAll(w, tag[:]); err != nil {
			return err
		}
		message = message[length:]
	}
	return nil
}

func incrementLittleEndian(counter *[16]byte) {
	for i := 0; i < len(counter); i++ {
		counter[i]++
		if counter[i] != 0 {
			return
		}
	}
}

// serverEAXStream is the encrypted RA2 session on the server side.
type serverEAXStream struct {
	session *ra2ServerSession
	conn    io.ReadWriter
	pending []byte
}

func (s *serverEAXStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		message, err := s.session.readMessage(s.conn)
		if err != nil {
			return 0, err
		}
		s.pending = message
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *serverEAXStream) Write(p []byte) (int, error) {
	if err := s.session.writeMessage(s.conn, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
