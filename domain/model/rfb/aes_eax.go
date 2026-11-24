package rfb

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"fmt"
)

// This file implements the two primitives RealVNC's RA2 security family needs
// and the standard library does not provide: AES-CMAC (RFC 4493) and AES-EAX
// (Bellare/Rogaway/Wagner) with a 128- or 256-bit AES key. Both are checked
// against published reference vectors in security_strong_test.go.

// AESCMAC returns the 128-bit AES-CMAC of message under key, as RFC 4493
// defines it. The key may be 16 or 32 bytes; the block size is always 16.
func AESCMAC(key, message []byte) ([16]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return [16]byte{}, err
	}
	return aesCMACBlock(block, message), nil
}

// aesCMACBlock is AES-CMAC over an initialized block cipher.
func aesCMACBlock(block cipher.Block, message []byte) [16]byte {
	var zero, l [16]byte
	block.Encrypt(l[:], zero[:])
	k1 := cmacDouble(l)
	k2 := cmacDouble(k1)

	blocks := (len(message) + 15) / 16
	if blocks == 0 {
		blocks = 1
	}
	var last [16]byte
	if len(message) == 0 {
		last[0] = 0x80
		xorBlock(&last, k2[:])
	} else if len(message)%16 == 0 {
		copy(last[:], message[len(message)-16:])
		xorBlock(&last, k1[:])
	} else {
		used := len(message) - (blocks-1)*16
		copy(last[:], message[(blocks-1)*16:])
		last[used] = 0x80
		xorBlock(&last, k2[:])
	}

	var x [16]byte
	for i := 0; i < blocks-1; i++ {
		xorBlock(&x, message[i*16:(i+1)*16])
		block.Encrypt(x[:], x[:])
	}
	xorBlock(&x, last[:])
	block.Encrypt(x[:], x[:])
	return x
}

// cmacDouble doubles a 128-bit value in GF(2^128), applying Rb=0x87 when the
// high bit was set, as RFC 4493 section 2.3 requires.
func cmacDouble(in [16]byte) [16]byte {
	var out [16]byte
	carry := in[0] >> 7
	for i := 0; i < 15; i++ {
		out[i] = in[i]<<1 | in[i+1]>>7
	}
	out[15] = in[15] << 1
	if carry != 0 {
		out[15] ^= 0x87
	}
	return out
}

func xorBlock(dst *[16]byte, src []byte) {
	for i := 0; i < 16; i++ {
		dst[i] ^= src[i]
	}
}

// eaxCipher seals and opens AES-EAX messages for one direction. EAX is
// stateless given a nonce; RA2 supplies a fresh 16-byte message index as the
// nonce for every message.
type eaxCipher struct {
	block cipher.Block
}

func newEAXCipher(key []byte) (*eaxCipher, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &eaxCipher{block: block}, nil
}

// seal encrypts plaintext with the given nonce and associated data and returns
// the ciphertext and 16-byte tag.
func (e *eaxCipher) seal(nonce, additionalData, plaintext []byte) ([]byte, [16]byte) {
	omac0 := e.omac(0, nonce)
	omac1 := e.omac(1, additionalData)

	ciphertext := make([]byte, len(plaintext))
	// EAX runs CTR with the derived nonce OMAC^0(N), not the raw nonce. Using
	// the raw nonce agrees with no published EAX vector and breaks RA2 interop.
	stream := cipher.NewCTR(e.block, omac0[:])
	stream.XORKeyStream(ciphertext, plaintext)

	omac2 := e.omac(2, ciphertext)
	var tag [16]byte
	for i := range tag {
		tag[i] = omac0[i] ^ omac1[i] ^ omac2[i]
	}
	return ciphertext, tag
}

// open verifies the tag and decrypts ciphertext. It returns false for a bad or
// truncated tag; no plaintext is exposed before verification.
func (e *eaxCipher) open(nonce, additionalData, ciphertext, tag []byte) ([]byte, bool) {
	if len(tag) != 16 || len(nonce) != 16 {
		return nil, false
	}
	omac0 := e.omac(0, nonce)
	omac1 := e.omac(1, additionalData)
	omac2 := e.omac(2, ciphertext)
	var computed [16]byte
	for i := range computed {
		computed[i] = omac0[i] ^ omac1[i] ^ omac2[i]
	}
	if subtle.ConstantTimeCompare(computed[:], tag) != 1 {
		return nil, false
	}
	plaintext := make([]byte, len(ciphertext))
	stream := cipher.NewCTR(e.block, omac0[:])
	stream.XORKeyStream(plaintext, ciphertext)
	return plaintext, true
}

// omac computes CMAC over the domain-separation block [domain]_128 followed by
// data, which is EAX's OMAC^domain construction. The domain is the big-endian
// 128-bit integer, so it occupies the last byte of the all-zero prefix block.
func (e *eaxCipher) omac(domain byte, data []byte) [16]byte {
	message := make([]byte, 16, 16+len(data))
	message[15] = domain
	message = append(message, data...)
	return aesCMACBlock(e.block, message)
}

// AESEAXSeal encrypts plaintext with AES-EAX, returning the ciphertext and the
// 16-byte authentication tag. It exists for tests and the in-process server;
// the RA2 client keeps one eaxCipher per direction.
func AESEAXSeal(key, nonce, additionalData, plaintext []byte) ([]byte, [16]byte, error) {
	cipher, err := newEAXCipher(key)
	if err != nil {
		return nil, [16]byte{}, err
	}
	if len(nonce) != 16 {
		return nil, [16]byte{}, fmt.Errorf("%w: AES-EAX nonce is %d bytes", ErrMalformed, len(nonce))
	}
	ciphertext, tag := cipher.seal(nonce, additionalData, plaintext)
	return ciphertext, tag, nil
}

// AESEAXOpen authenticates and decrypts an AES-EAX message.
func AESEAXOpen(key, nonce, additionalData, ciphertext, tag []byte) ([]byte, error) {
	cipher, err := newEAXCipher(key)
	if err != nil {
		return nil, err
	}
	plaintext, ok := cipher.open(nonce, additionalData, ciphertext, tag)
	if !ok {
		return nil, fmt.Errorf("%w: AES-EAX authentication failed", ErrMalformed)
	}
	return plaintext, nil
}
