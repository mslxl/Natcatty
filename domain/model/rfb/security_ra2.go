package rfb

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"math/big"
	"time"
)

// RealVNC's RSA-AES (RA2) family wraps an RSA key exchange, a pair of randoms
// and AES-EAX-protected credentials. The wire sequence follows TigerVNC's
// CSecurityRSAAES:
//
//	server: U32 bits, modulus[size], exponent[size]       (size = ceil(bits/8))
//	client: U32 bits, modulus[size], exponent[size]       (matching bit size)
//	server: U16 len, RSA(client-public, server-random)
//	client: U16 len, RSA(server-public, client-random)
//	then the hash exchange, subtype and credentials travel in AES-EAX messages
//	framed as U16 length || ciphertext || 16-byte MAC.
//
// RA2 and RA2_256 keep the AES-EAX layer for the whole session; RA2ne and
// RA2ne_256 drop it after the credential exchange, so only the handshake is
// encrypted.
const (
	ra2MinKeyBits = 1024
	// ra2MaxKeyBits caps the server key this client will match. The client
	// generates an RSA key of the same size, so accepting an arbitrarily large
	// server key would let a hostile peer dictate unbounded CPU work. 4096 bits
	// is the largest size in common use and matches TigerVNC's practical range.
	ra2MaxKeyBits = 4096
	// defaultRA2KeygenTimeout bounds client key generation when the caller does
	// not supply the handshake budget. Key generation is CPU-bound, so the
	// connection deadline cannot interrupt it.
	defaultRA2KeygenTimeout = 30 * time.Second
	// ra2MaxMessage bounds one AES-EAX frame, matching TigerVNC's
	// MaxMessageSize. A caller that writes more is split across frames.
	ra2MaxMessage = 8192
)

// ra2Params describes one RA2 variant.
type ra2Params struct {
	// bits is the AES key size: 128 for the SHA-1 variants, 256 for SHA-256.
	bits int
	// encrypted reports that the AES-EAX layer covers the whole session, not
	// only the handshake.
	encrypted bool
	// sha256 selects SHA-256 key derivation and hashes.
	sha256 bool
}

// ra2ParamsFor maps an implemented security type to its parameters. RA2r
// variants are deliberately absent: their second key-exchange round is not
// implemented, so they are never advertised by the preference order.
func ra2ParamsFor(security SecurityType) (ra2Params, bool) {
	switch security {
	case SecurityRA2ne:
		return ra2Params{bits: 128}, true
	case SecurityRA2:
		return ra2Params{bits: 128, encrypted: true}, true
	case SecurityRA2ne_256:
		return ra2Params{bits: 256, sha256: true}, true
	case SecurityRA2_256:
		return ra2Params{bits: 256, encrypted: true, sha256: true}, true
	default:
		return ra2Params{}, false
	}
}

// ra2Authenticate performs the client half of an RA2 handshake and returns the
// stream the rest of the protocol must use. For the encrypted variants that is
// an AES-EAX stream layer whose counters continue from the handshake; for the
// "ne" variants it is the raw connection.
func ra2Authenticate(conn io.ReadWriter, r *bufio.Reader, security SecurityType, cfg ClientConfig) (io.ReadWriter, *bufio.Reader, error) {
	params, ok := ra2ParamsFor(security)
	if !ok {
		return nil, nil, &SecurityError{Offered: []SecurityType{security}}
	}
	keyBytes := params.bits / 8
	randomSize := keyBytes

	serverBits, serverModulus, serverExponent, serverKeyWire, err := readRA2PublicKey(r)
	if err != nil {
		return nil, nil, err
	}
	serverPublic, err := ra2PublicKey(serverBits, serverModulus, serverExponent)
	if err != nil {
		return nil, nil, err
	}

	// Match the server's key size, as TigerVNC does, so the random ciphertext
	// has the length the server expects. The size is capped above and the
	// generation observes the caller's context and handshake budget.
	clientKey, err := ra2GenerateKey(cfg.Context, cfg.KeygenTimeout, int(serverBits))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: generating RA2 client key: %w", ErrHandshake, err)
	}
	clientKeySize := (int(serverBits) + 7) / 8
	clientModulus := clientKey.PublicKey.N.FillBytes(make([]byte, clientKeySize))
	clientExponent := big.NewInt(int64(clientKey.PublicKey.E)).FillBytes(make([]byte, clientKeySize))
	clientKeyWire := ra2KeyWire(serverBits, clientModulus, clientExponent)
	if err := writeAll(conn, clientKeyWire); err != nil {
		return nil, nil, fmt.Errorf("%w: writing RA2 client key: %w", ErrHandshake, err)
	}

	clientRandom := make([]byte, randomSize)
	if _, err := io.ReadFull(rand.Reader, clientRandom); err != nil {
		return nil, nil, fmt.Errorf("%w: generating RA2 client random: %w", ErrHandshake, err)
	}
	encryptedClientRandom, err := rsa.EncryptPKCS1v15(rand.Reader, serverPublic, clientRandom)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encrypting RA2 client random: %w", ErrHandshake, err)
	}
	if len(encryptedClientRandom) != clientKeySize {
		return nil, nil, fmt.Errorf("%w: RA2 client random ciphertext is %d bytes, want %d",
			ErrHandshake, len(encryptedClientRandom), clientKeySize)
	}
	var randomHeader [2]byte
	binary.BigEndian.PutUint16(randomHeader[:], uint16(len(encryptedClientRandom)))
	if err := writeAll(conn, randomHeader[:]); err != nil {
		return nil, nil, fmt.Errorf("%w: writing RA2 random length: %w", ErrHandshake, err)
	}
	if err := writeAll(conn, encryptedClientRandom); err != nil {
		return nil, nil, fmt.Errorf("%w: writing RA2 client random: %w", ErrHandshake, err)
	}

	var serverRandomHeader [2]byte
	if _, err := io.ReadFull(r, serverRandomHeader[:]); err != nil {
		return nil, nil, fmt.Errorf("%w: reading RA2 random length: %w", ErrHandshake, err)
	}
	serverRandomLength := int(binary.BigEndian.Uint16(serverRandomHeader[:]))
	if serverRandomLength != clientKeySize {
		return nil, nil, fmt.Errorf("%w: RA2 server random ciphertext is %d bytes, want %d",
			ErrHandshake, serverRandomLength, clientKeySize)
	}
	encryptedServerRandom := make([]byte, serverRandomLength)
	if _, err := io.ReadFull(r, encryptedServerRandom); err != nil {
		return nil, nil, fmt.Errorf("%w: reading RA2 server random: %w", ErrHandshake, err)
	}
	serverRandom, err := rsa.DecryptPKCS1v15(rand.Reader, clientKey, encryptedServerRandom)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: decrypting RA2 server random: %w", ErrHandshake, err)
	}
	if len(serverRandom) != randomSize {
		return nil, nil, fmt.Errorf("%w: RA2 server random is %d bytes, want %d",
			ErrHandshake, len(serverRandom), randomSize)
	}

	serverSessionKey := ra2SessionKey(params, clientRandom, serverRandom)
	clientSessionKey := ra2SessionKey(params, serverRandom, clientRandom)
	session, err := newEAXSession(serverSessionKey, clientSessionKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: initializing RA2 cipher: %w", ErrHandshake, err)
	}

	// The client proves knowledge of the exchanged key by sending a hash over
	// its own public key followed by the server's, then verifies the server's
	// mirror image.
	clientHash := ra2Hash(params, clientKeyWire, serverKeyWire)
	if err := session.writeMessage(conn, clientHash); err != nil {
		return nil, nil, err
	}
	serverHash, err := session.readMessage(r)
	if err != nil {
		return nil, nil, err
	}
	want := ra2Hash(params, serverKeyWire, clientKeyWire)
	if subtle.ConstantTimeCompare(serverHash, want) != 1 {
		return nil, nil, fmt.Errorf("%w: RA2 server key hash does not match", ErrHandshake)
	}

	subtype, err := session.readMessage(r)
	if err != nil {
		return nil, nil, err
	}
	if len(subtype) != 1 {
		return nil, nil, fmt.Errorf("%w: RA2 credential subtype is %d bytes", ErrMalformed, len(subtype))
	}
	credentials, err := ra2Credentials(subtype[0], cfg.Username, cfg.Password)
	if err != nil {
		return nil, nil, err
	}
	if err := session.writeMessage(conn, credentials); err != nil {
		return nil, nil, err
	}

	if !params.encrypted {
		return conn, r, nil
	}
	stream := &eaxStream{
		session: session,
		reader:  r,
		writer:  conn,
	}
	return stream, bufio.NewReader(stream), nil
}

// ra2GenerateKey generates the client's RSA key while observing ctx and a
// timeout. rsa.GenerateKey has no cancellation hook, so it runs in a goroutine
// and the caller stops waiting when the deadline or cancellation wins; the
// bounded key size keeps the abandoned goroutine's work finite.
func ra2GenerateKey(ctx context.Context, timeout time.Duration, bits int) (*rsa.PrivateKey, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = defaultRA2KeygenTimeout
	}
	keyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		key *rsa.PrivateKey
		err error
	}
	done := make(chan result, 1)
	go func() {
		key, err := rsa.GenerateKey(rand.Reader, bits)
		done <- result{key: key, err: err}
	}()

	select {
	case generated := <-done:
		return generated.key, generated.err
	case <-keyCtx.Done():
		return nil, keyCtx.Err()
	}
}

// readRA2PublicKey reads the server's key wire form and returns both the parsed
// fields and the exact bytes the hash covers.
func readRA2PublicKey(r io.Reader) (uint32, []byte, []byte, []byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, nil, nil, fmt.Errorf("%w: reading RA2 key length: %w", ErrHandshake, err)
	}
	bits := binary.BigEndian.Uint32(header[:])
	if bits < ra2MinKeyBits || bits > ra2MaxKeyBits {
		return 0, nil, nil, nil, fmt.Errorf("%w: RA2 key is %d bits, want %d..%d",
			ErrHandshake, bits, ra2MinKeyBits, ra2MaxKeyBits)
	}
	size := (int(bits) + 7) / 8
	modulus := make([]byte, size)
	exponent := make([]byte, size)
	if _, err := io.ReadFull(r, modulus); err != nil {
		return 0, nil, nil, nil, fmt.Errorf("%w: reading RA2 modulus: %w", ErrHandshake, err)
	}
	if _, err := io.ReadFull(r, exponent); err != nil {
		return 0, nil, nil, nil, fmt.Errorf("%w: reading RA2 exponent: %w", ErrHandshake, err)
	}
	return bits, modulus, exponent, ra2KeyWire(bits, modulus, exponent), nil
}

// ra2KeyWire encodes one RSA public key exactly as the RA2 handshake and hash
// define it: U32 bit length, modulus, exponent, each padded to ceil(bits/8).
func ra2KeyWire(bits uint32, modulus, exponent []byte) []byte {
	wire := make([]byte, 4+len(modulus)+len(exponent))
	binary.BigEndian.PutUint32(wire[0:4], bits)
	copy(wire[4:], modulus)
	copy(wire[4+len(modulus):], exponent)
	return wire
}

func ra2PublicKey(bits uint32, modulus, exponent []byte) (*rsa.PublicKey, error) {
	n := new(big.Int).SetBytes(modulus)
	e := new(big.Int).SetBytes(exponent)
	if n.Sign() <= 0 || n.Bit(0) == 0 {
		return nil, fmt.Errorf("%w: invalid RA2 server modulus", ErrHandshake)
	}
	if !e.IsInt64() || e.Int64() < 3 || e.Int64() > int64(^uint32(0)>>1) {
		return nil, fmt.Errorf("%w: invalid RA2 server exponent", ErrHandshake)
	}
	if n.BitLen() > int(bits) {
		return nil, fmt.Errorf("%w: RA2 server modulus is %d bits, want at most %d",
			ErrHandshake, n.BitLen(), bits)
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
}

// ra2SessionKey derives one direction's AES key. TigerVNC hashes
// (ClientRandom || ServerRandom) for the server-to-client direction and
// (ServerRandom || ClientRandom) for the client-to-server direction; the
// 256-bit variants use the full SHA-256 digest as a 32-byte AES key.
func ra2SessionKey(params ra2Params, first, second []byte) []byte {
	if params.sha256 {
		sum := sha256.Sum256(append(append([]byte(nil), first...), second...))
		return sum[:]
	}
	sum := sha1.Sum(append(append([]byte(nil), first...), second...))
	return sum[:16]
}

// ra2Hash is the public-key hash exchanged through the EAX layer.
func ra2Hash(params ra2Params, first, second []byte) []byte {
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

// ra2Credentials frames the username and password the server's subtype asks
// for. Subtype 1 sends both; subtype 2 sends an empty username and the
// password.
func ra2Credentials(subtype byte, username, password string) ([]byte, error) {
	if subtype != 1 && subtype != 2 {
		return nil, fmt.Errorf("%w: unknown RA2 credential subtype %d", ErrHandshake, subtype)
	}
	if len(password) > 255 {
		return nil, fmt.Errorf("%w: RA2 password is %d bytes", ErrHandshake, len(password))
	}
	wire := make([]byte, 0, 2+len(username)+len(password))
	if subtype == 1 {
		if len(username) > 255 {
			return nil, fmt.Errorf("%w: RA2 username is %d bytes", ErrHandshake, len(username))
		}
		wire = append(wire, byte(len(username)))
		wire = append(wire, username...)
	} else {
		wire = append(wire, 0)
	}
	wire = append(wire, byte(len(password)))
	wire = append(wire, password...)
	return wire, nil
}

// eaxSession owns one RA2 direction's cipher and message counter. The nonce is
// a 16-byte little-endian message index starting at zero, as TigerVNC's
// AESInStream/AESOutStream implement it.
type eaxSession struct {
	read       *eaxCipher
	readIndex  [16]byte
	write      *eaxCipher
	writeIndex [16]byte
}

func newEAXSession(readKey, writeKey []byte) (*eaxSession, error) {
	read, err := newEAXCipher(readKey)
	if err != nil {
		return nil, err
	}
	write, err := newEAXCipher(writeKey)
	if err != nil {
		return nil, err
	}
	return &eaxSession{read: read, write: write}, nil
}

func (s *eaxSession) readMessage(r io.Reader) ([]byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(header[:]))
	ciphertext := make([]byte, length)
	if _, err := io.ReadFull(r, ciphertext); err != nil {
		return nil, err
	}
	var tag [16]byte
	if _, err := io.ReadFull(r, tag[:]); err != nil {
		return nil, err
	}
	nonce := s.readIndex
	incrementLittleEndian(&s.readIndex)
	plaintext, ok := s.read.open(nonce[:], header[:], ciphertext, tag[:])
	if !ok {
		return nil, fmt.Errorf("%w: RA2 message authentication failed", ErrMalformed)
	}
	return plaintext, nil
}

func (s *eaxSession) writeMessage(w io.Writer, message []byte) error {
	for len(message) > 0 {
		length := len(message)
		if length > ra2MaxMessage {
			length = ra2MaxMessage
		}
		var header [2]byte
		binary.BigEndian.PutUint16(header[:], uint16(length))
		nonce := s.writeIndex
		incrementLittleEndian(&s.writeIndex)
		ciphertext, tag := s.write.seal(nonce[:], header[:], message[:length])
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

// incrementLittleEndian advances a 128-bit little-endian counter, the RA2
// message nonce.
func incrementLittleEndian(counter *[16]byte) {
	for i := 0; i < len(counter); i++ {
		counter[i]++
		if counter[i] != 0 {
			return
		}
	}
}

// eaxStream turns the AES-EAX frames back into a byte stream for the RFB
// engine. Reads decrypt one frame at a time; writes encrypt one frame per
// Write call (splitting at the RA2 maximum).
type eaxStream struct {
	session *eaxSession
	reader  io.Reader
	writer  io.Writer
	pending []byte
}

func (s *eaxStream) Read(p []byte) (int, error) {
	for len(s.pending) == 0 {
		message, err := s.session.readMessage(s.reader)
		if err != nil {
			return 0, err
		}
		s.pending = message
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

func (s *eaxStream) Write(p []byte) (int, error) {
	if err := s.session.writeMessage(s.writer, p); err != nil {
		return 0, err
	}
	return len(p), nil
}
