package rfb

import (
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
)

// Apple's Diffie-Hellman security type (30) is username+password authentication
// protected by a DH-derived AES-ECB key. The wire format follows TigerVNC's
// CSecurityDH and rfbproto's Diffie-Hellman Authentication section:
//
//	server: U16 generator, U16 key-size (bytes), prime[key-size], public[key-size]
//	client: AES-ECB(key=MD5(shared padded to key-size), 128 bytes) || client-public[key-size]
//
// The 128-byte credential block holds the username in its first 64 bytes and
// the password in its second 64. Each is a NUL-terminated string, and the rest
// of its half is zero padding: the protocol treats everything after the NUL as
// padding, not as a second secret to randomize.
const (
	appleDHMinKeyBytes = 128
	appleDHMaxKeyBytes = 1024
	appleDHCredential  = 64
	appleDHBlockSize   = 128
)

// appleDHAuthenticate performs the client half of Apple's DH authentication. It
// reads the server parameters and writes the encrypted credentials followed by
// the client public value; the caller reads SecurityResult afterwards.
func appleDHAuthenticate(r io.Reader, w io.Writer, username, password string) error {
	return appleDHAuthenticateWithRand(r, w, username, password, rand.Reader)
}

// appleDHAuthenticateWithRand is appleDHAuthenticate with an injectable
// randomness source, so a test can pin the private exponent and the exact
// ciphertext. Production passes crypto/rand.
func appleDHAuthenticateWithRand(r io.Reader, w io.Writer, username, password string, random io.Reader) error {
	if len(username) >= appleDHCredential {
		return fmt.Errorf("%w: Apple DH username is %d bytes, want at most %d",
			ErrHandshake, len(username), appleDHCredential-1)
	}
	if len(password) >= appleDHCredential {
		return fmt.Errorf("%w: Apple DH password is %d bytes, want at most %d",
			ErrHandshake, len(password), appleDHCredential-1)
	}

	var params [4]byte
	if _, err := io.ReadFull(r, params[:]); err != nil {
		return fmt.Errorf("%w: reading Apple DH parameters: %w", ErrHandshake, err)
	}
	generator := int64(binary.BigEndian.Uint16(params[0:2]))
	keyBytes := int(binary.BigEndian.Uint16(params[2:4]))
	if keyBytes < appleDHMinKeyBytes || keyBytes > appleDHMaxKeyBytes {
		return fmt.Errorf("%w: Apple DH key is %d bytes, want %d..%d",
			ErrHandshake, keyBytes, appleDHMinKeyBytes, appleDHMaxKeyBytes)
	}
	primeBytes := make([]byte, keyBytes)
	if _, err := io.ReadFull(r, primeBytes); err != nil {
		return fmt.Errorf("%w: reading Apple DH prime: %w", ErrHandshake, err)
	}
	serverPublicBytes := make([]byte, keyBytes)
	if _, err := io.ReadFull(r, serverPublicBytes); err != nil {
		return fmt.Errorf("%w: reading Apple DH public value: %w", ErrHandshake, err)
	}

	prime := new(big.Int).SetBytes(primeBytes)
	if prime.Sign() <= 0 || generator <= 0 {
		return fmt.Errorf("%w: invalid Apple DH parameters", ErrHandshake)
	}
	serverPublic := new(big.Int).SetBytes(serverPublicBytes)
	if serverPublic.Sign() <= 0 || serverPublic.Cmp(prime) >= 0 {
		return fmt.Errorf("%w: invalid Apple DH server public value", ErrHandshake)
	}

	// TigerVNC samples a private exponent of exactly key-size random bytes.
	privateBytes := make([]byte, keyBytes)
	if _, err := io.ReadFull(random, privateBytes); err != nil {
		return fmt.Errorf("%w: generating Apple DH private key: %w", ErrHandshake, err)
	}
	private := new(big.Int).SetBytes(privateBytes)
	if private.Sign() == 0 {
		private.SetInt64(1)
	}
	shared := new(big.Int).Exp(serverPublic, private, prime)
	clientPublic := new(big.Int).Exp(big.NewInt(generator), private, prime)

	credentials, err := appleDHCredentialBlock(shared, keyBytes, username, password)
	if err != nil {
		return err
	}

	clientPublicBytes := clientPublic.FillBytes(make([]byte, keyBytes))
	response := make([]byte, 0, appleDHBlockSize+keyBytes)
	response = append(response, credentials...)
	response = append(response, clientPublicBytes...)
	if err := writeAll(w, response); err != nil {
		return fmt.Errorf("%w: writing Apple DH credentials: %w", ErrHandshake, err)
	}
	return nil
}

// appleDHCredentialBlock builds and encrypts the 128-byte credential block.
// Username and password each occupy one 64-byte half as a NUL-terminated
// string, and every byte after the terminator is zero. The AES key is MD5 over
// the shared secret, big-endian and zero-padded to keyBytes, as TigerVNC and
// Apple define it.
func appleDHCredentialBlock(shared *big.Int, keyBytes int, username, password string) ([]byte, error) {
	sharedBytes := shared.FillBytes(make([]byte, keyBytes))
	aesKey := md5.Sum(sharedBytes)
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return nil, fmt.Errorf("%w: Apple DH AES key: %w", ErrHandshake, err)
	}

	credentials := make([]byte, appleDHBlockSize)
	copy(credentials, username)
	// The block is zero-initialized, so the byte after the copied username is
	// already the NUL terminator and the rest of the half is NUL padding.
	copy(credentials[appleDHCredential:], password)
	for offset := 0; offset < appleDHBlockSize; offset += aes.BlockSize {
		block.Encrypt(credentials[offset:offset+aes.BlockSize], credentials[offset:offset+aes.BlockSize])
	}
	return credentials, nil
}
