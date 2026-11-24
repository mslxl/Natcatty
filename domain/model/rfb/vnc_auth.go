package rfb

import (
	"crypto/des"
	"fmt"
	"io"
)

// vncChallengeSize is the 16-byte DES challenge of the VNC password security
// type (RFB security type 2).
const vncChallengeSize = 16

// VNCAuthKey derives the 8-byte DES key from a VNC password. The password is
// truncated or NUL-padded to 8 bytes and each byte's bits are reversed, the
// convention every VNC server implements.
func VNCAuthKey(password string) [8]byte {
	var key [8]byte
	copy(key[:], password)
	for i := range key {
		key[i] = reverseBits(key[i])
	}
	return key
}

func reverseBits(b byte) byte {
	b = (b&0xf0)>>4 | (b&0x0f)<<4
	b = (b&0xcc)>>2 | (b&0x33)<<2
	b = (b&0xaa)>>1 | (b&0x55)<<1
	return b
}

// VNCAuthResponse encrypts a 16-byte challenge with the VNC password. It is
// exported for the in-process test server and for tests; Connect performs the
// same exchange.
func VNCAuthResponse(password string, challenge []byte) ([16]byte, error) {
	var response [16]byte
	if len(challenge) != vncChallengeSize {
		return response, fmt.Errorf("%w: VNC challenge is %d bytes, want %d",
			ErrMalformed, len(challenge), vncChallengeSize)
	}
	key := VNCAuthKey(password)
	block, err := des.NewCipher(key[:])
	if err != nil {
		return response, err
	}
	block.Encrypt(response[0:8], challenge[0:8])
	block.Encrypt(response[8:16], challenge[8:16])
	return response, nil
}

// vncAuthenticate performs the challenge/response half of VNC password
// authentication. It reads the server's challenge and writes the DES
// response.
func vncAuthenticate(r io.Reader, w io.Writer, password string) error {
	var challenge [vncChallengeSize]byte
	if _, err := io.ReadFull(r, challenge[:]); err != nil {
		return fmt.Errorf("%w: reading VNC challenge: %w", ErrHandshake, err)
	}
	response, err := VNCAuthResponse(password, challenge[:])
	if err != nil {
		return fmt.Errorf("%w: %w", ErrHandshake, err)
	}
	if err := writeAll(w, response[:]); err != nil {
		return fmt.Errorf("%w: writing VNC response: %w", ErrHandshake, err)
	}
	return nil
}
