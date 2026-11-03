// SPDX-License-Identifier: GPL-3.0-or-later

// OCB3 authenticated encryption for the mosh wire protocol.
//
// This file is adapted from mosh-go v0.5.2 (commit
// 0284bd652aab3dfff5b0b74945c23901d7537b61), Copyright (c) 2026 Unix
// Shells, which is distributed under the MIT license. The MIT notice is
// reproduced in NOTICE next to this file. The implementation follows RFC
// 7253: AES-128, a 12-byte mosh nonce, a 16-byte tag, and empty associated
// data.
package mosh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"errors"
	"fmt"
)

const (
	ocbBlockSize = 16
	ocbTagSize   = 16
)

// ErrOCB reports an unusable OCB cipher or input.
var ErrOCB = errors.New("mosh: OCB3 failure")

// OCB is an AES-128-OCB3 authenticated cipher. A zero value is not usable;
// construct one with NewOCB. Close zeroizes the key material this value
// holds.
type OCB struct {
	block   cipher.Block
	key     [ocbBlockSize]byte
	lStar   [ocbBlockSize]byte
	lDollar [ocbBlockSize]byte
	l       [32][ocbBlockSize]byte
	closed  bool
}

// NewOCB creates an OCB3 cipher from a 16-byte AES-128 key.
func NewOCB(key []byte) (*OCB, error) {
	if len(key) != ocbBlockSize {
		return nil, fmt.Errorf("%w: key must be 16 bytes, got %d", ErrOCB, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOCB, err)
	}
	o := &OCB{block: block}
	copy(o.key[:], key)

	// L_* = ENCIPHER(K, zeros)
	o.block.Encrypt(o.lStar[:], make([]byte, ocbBlockSize))

	// L_$ = double(L_*)
	o.lDollar = gfDouble(o.lStar)

	// L_0..L_31 = successive doublings.
	o.l[0] = gfDouble(o.lDollar)
	for i := 1; i < len(o.l); i++ {
		o.l[i] = gfDouble(o.l[i-1])
	}
	return o, nil
}

// Close zeroizes the key material held by the cipher. Encrypt and Decrypt
// fail afterwards.
func (o *OCB) Close() {
	if o == nil || o.closed {
		return
	}
	o.closed = true
	o.block = nil
	clear(o.key[:])
	clear(o.lStar[:])
	clear(o.lDollar[:])
	for i := range o.l {
		clear(o.l[i][:])
	}
}

// Encrypt returns ciphertext followed by the 16-byte OCB3 tag, which is the
// order mosh puts on the wire.
func (o *OCB) Encrypt(nonce, plaintext []byte) ([]byte, error) {
	if o == nil || o.closed || o.block == nil {
		return nil, fmt.Errorf("%w: cipher is closed", ErrOCB)
	}
	if len(nonce) > 15 {
		return nil, fmt.Errorf("%w: nonce must be at most 15 bytes", ErrOCB)
	}

	offset := o.initOffset(nonce)
	var checksum [ocbBlockSize]byte

	fullBlocks := len(plaintext) / ocbBlockSize
	remaining := len(plaintext) % ocbBlockSize
	ciphertext := make([]byte, len(plaintext))

	for i := 0; i < fullBlocks; i++ {
		xorInto(&offset, &o.l[ntz(i+1)])

		var pBlock [ocbBlockSize]byte
		copy(pBlock[:], plaintext[i*ocbBlockSize:])
		xorInto(&checksum, &pBlock)

		var tmp, enc [ocbBlockSize]byte
		xorBlocks(&tmp, &offset, &pBlock)
		o.block.Encrypt(enc[:], tmp[:])
		xorInto(&enc, &offset)
		copy(ciphertext[i*ocbBlockSize:], enc[:])
	}

	if remaining > 0 {
		xorInto(&offset, &o.lStar)

		var pad [ocbBlockSize]byte
		o.block.Encrypt(pad[:], offset[:])

		pStar := plaintext[fullBlocks*ocbBlockSize:]
		for i := 0; i < remaining; i++ {
			ciphertext[fullBlocks*ocbBlockSize+i] = pStar[i] ^ pad[i]
		}

		// Checksum_* = Checksum_m xor (P_* || 1 || zeros)
		var padded [ocbBlockSize]byte
		copy(padded[:], pStar)
		padded[remaining] = 0x80
		xorInto(&checksum, &padded)
	}

	// Tag = ENCIPHER(K, Checksum xor Offset xor L_$)
	xorInto(&checksum, &offset)
	xorInto(&checksum, &o.lDollar)
	var tag [ocbBlockSize]byte
	o.block.Encrypt(tag[:], checksum[:])

	result := make([]byte, len(ciphertext)+ocbTagSize)
	copy(result, ciphertext)
	copy(result[len(ciphertext):], tag[:])
	return result, nil
}

// Decrypt verifies and decrypts ciphertext followed by its tag. The bool is
// false when the tag does not authenticate; the returned plaintext is nil.
func (o *OCB) Decrypt(nonce, ciphertextAndTag []byte) ([]byte, bool) {
	if o == nil || o.closed || o.block == nil || len(nonce) > 15 {
		return nil, false
	}
	if len(ciphertextAndTag) < ocbTagSize {
		return nil, false
	}

	ciphertext := ciphertextAndTag[:len(ciphertextAndTag)-ocbTagSize]
	tag := ciphertextAndTag[len(ciphertextAndTag)-ocbTagSize:]

	offset := o.initOffset(nonce)
	var checksum [ocbBlockSize]byte

	fullBlocks := len(ciphertext) / ocbBlockSize
	remaining := len(ciphertext) % ocbBlockSize
	plaintext := make([]byte, len(ciphertext))

	for i := 0; i < fullBlocks; i++ {
		xorInto(&offset, &o.l[ntz(i+1)])

		var cBlock [ocbBlockSize]byte
		copy(cBlock[:], ciphertext[i*ocbBlockSize:])
		var tmp, dec [ocbBlockSize]byte
		xorBlocks(&tmp, &offset, &cBlock)
		o.block.Decrypt(dec[:], tmp[:])
		xorInto(&dec, &offset)
		copy(plaintext[i*ocbBlockSize:], dec[:])
		xorInto(&checksum, &dec)
	}

	if remaining > 0 {
		xorInto(&offset, &o.lStar)

		var pad [ocbBlockSize]byte
		o.block.Encrypt(pad[:], offset[:])

		cStar := ciphertext[fullBlocks*ocbBlockSize:]
		for i := 0; i < remaining; i++ {
			plaintext[fullBlocks*ocbBlockSize+i] = cStar[i] ^ pad[i]
		}

		var padded [ocbBlockSize]byte
		copy(padded[:], plaintext[fullBlocks*ocbBlockSize:])
		padded[remaining] = 0x80
		xorInto(&checksum, &padded)
	}

	xorInto(&checksum, &offset)
	xorInto(&checksum, &o.lDollar)
	var expectedTag [ocbBlockSize]byte
	o.block.Encrypt(expectedTag[:], checksum[:])

	if subtle.ConstantTimeCompare(tag, expectedTag[:]) != 1 {
		return nil, false
	}
	return plaintext, true
}

// initOffset computes the initial offset from a nonce (RFC 7253 Section 4.2).
func (o *OCB) initOffset(nonce []byte) [ocbBlockSize]byte {
	nonceLen := len(nonce)

	var nn [ocbBlockSize]byte
	// Tag length is 128 bits, so the first byte encodes 128 mod 128 = 0.
	nn[0] = byte((ocbTagSize * 8) % 128 & 0x7f)
	nn[ocbBlockSize-1-nonceLen] |= 0x01
	copy(nn[ocbBlockSize-nonceLen:], nonce)

	bottom := nn[15] & 0x3f
	nn[15] &= 0xc0

	var ktop [ocbBlockSize]byte
	o.block.Encrypt(ktop[:], nn[:])

	var stretch [24]byte
	copy(stretch[:ocbBlockSize], ktop[:])
	for i := 0; i < 8; i++ {
		stretch[ocbBlockSize+i] = ktop[i] ^ ktop[i+1]
	}

	var offset [ocbBlockSize]byte
	byteShift := int(bottom) >> 3
	bitShift := int(bottom) & 7
	for i := 0; i < ocbBlockSize; i++ {
		idx := byteShift + i
		if idx >= len(stretch) {
			break
		}
		offset[i] = stretch[idx] << bitShift
		if bitShift != 0 && idx+1 < len(stretch) {
			offset[i] |= stretch[idx+1] >> (8 - bitShift)
		}
	}
	return offset
}

// gfDouble doubles a block in GF(2^128) with polynomial
// x^128 + x^7 + x^2 + x + 1.
func gfDouble(block [ocbBlockSize]byte) [ocbBlockSize]byte {
	var result [ocbBlockSize]byte
	carry := byte(0)
	for i := ocbBlockSize - 1; i >= 0; i-- {
		result[i] = block[i]<<1 | carry
		carry = block[i] >> 7
	}
	if carry != 0 {
		result[ocbBlockSize-1] ^= 0x87
	}
	return result
}

// ntz returns the number of trailing zeros of a positive integer.
func ntz(n int) int {
	if n == 0 {
		return 32
	}
	count := 0
	for n&1 == 0 {
		count++
		n >>= 1
	}
	return count
}

func xorInto(a *[ocbBlockSize]byte, b *[ocbBlockSize]byte) {
	for i := range a {
		a[i] ^= b[i]
	}
}

func xorBlocks(dst, a, b *[ocbBlockSize]byte) {
	for i := range dst {
		dst[i] = a[i] ^ b[i]
	}
}
