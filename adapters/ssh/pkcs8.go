package ssh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/asn1"
	"errors"
	"fmt"
	"hash"

	"golang.org/x/crypto/pbkdf2"
)

// errPKCS8Unsupported reports PBES2 parameters this build does not implement.
// Decryption failures with a supported scheme are reported as passphrase
// failures instead.
var errPKCS8Unsupported = errors.New("pkcs8: unsupported encryption scheme")

var (
	oidPBES2          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACWithSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACWithSHA224 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 8}
	oidHMACWithSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACWithSHA384 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 10}
	oidHMACWithSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
	oidAES128CBC      = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC      = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC      = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	oidTripleDESCBC   = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
)

type algorithmIdentifier struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type encryptedPrivateKeyInfo struct {
	Algorithm     algorithmIdentifier
	EncryptedData []byte
}

type pbes2Parameters struct {
	KeyDerivationFunc algorithmIdentifier
	EncryptionScheme  algorithmIdentifier
}

type pbkdf2Parameters struct {
	Salt           []byte
	IterationCount int
	KeyLength      int                 `asn1:"optional"`
	PRF            algorithmIdentifier `asn1:"optional"`
}

// DecryptPKCS8 decrypts a DER-encoded PKCS#8 EncryptedPrivateKeyInfo using
// PBES2 with PBKDF2 and AES-CBC or 3DES-CBC. It returns the inner
// PrivateKeyInfo DER.
//
// x/crypto/ssh can parse unencrypted PKCS#8 but not encrypted PKCS#8, so this
// closes the gap without adding a general-purpose crypto dependency.
func DecryptPKCS8(der, passphrase []byte) ([]byte, error) {
	var info encryptedPrivateKeyInfo
	if rest, err := asn1.Unmarshal(der, &info); err != nil {
		return nil, fmt.Errorf("pkcs8: malformed encrypted key: %w", err)
	} else if len(rest) != 0 {
		return nil, errors.New("pkcs8: trailing data after encrypted key")
	}
	if !info.Algorithm.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("%w: algorithm %s", errPKCS8Unsupported, info.Algorithm.Algorithm)
	}

	var params pbes2Parameters
	if err := unmarshalRaw(info.Algorithm.Parameters, &params); err != nil {
		return nil, fmt.Errorf("pkcs8: malformed PBES2 parameters: %w", err)
	}
	if !params.KeyDerivationFunc.Algorithm.Equal(oidPBKDF2) {
		return nil, fmt.Errorf("%w: key derivation %s", errPKCS8Unsupported, params.KeyDerivationFunc.Algorithm)
	}
	var kdf pbkdf2Parameters
	if err := unmarshalRaw(params.KeyDerivationFunc.Parameters, &kdf); err != nil {
		return nil, fmt.Errorf("pkcs8: malformed PBKDF2 parameters: %w", err)
	}
	if len(kdf.Salt) == 0 || kdf.IterationCount <= 0 {
		return nil, errors.New("pkcs8: invalid PBKDF2 parameters")
	}
	hashFunc, err := pbkdf2Hash(kdf.PRF.Algorithm)
	if err != nil {
		return nil, err
	}

	scheme, err := schemeForOID(params.EncryptionScheme.Algorithm)
	if err != nil {
		return nil, err
	}
	if kdf.KeyLength != 0 && kdf.KeyLength != scheme.keyLength {
		return nil, fmt.Errorf("%w: key length %d", errPKCS8Unsupported, kdf.KeyLength)
	}
	iv, err := rawOctets(params.EncryptionScheme.Parameters)
	if err != nil {
		return nil, fmt.Errorf("pkcs8: malformed IV: %w", err)
	}
	if len(iv) != scheme.blockSize {
		return nil, errors.New("pkcs8: invalid IV length")
	}
	if len(info.EncryptedData) == 0 || len(info.EncryptedData)%scheme.blockSize != 0 {
		return nil, errors.New("pkcs8: invalid ciphertext length")
	}

	key := pbkdf2.Key(passphrase, kdf.Salt, kdf.IterationCount, scheme.keyLength, hashFunc)
	block, err := scheme.newBlock(key)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(info.EncryptedData))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, info.EncryptedData)
	return unpadPKCS7(plain, block.BlockSize())
}

func unmarshalRaw(raw asn1.RawValue, target any) error {
	if len(raw.FullBytes) == 0 {
		return errors.New("missing parameters")
	}
	rest, err := asn1.Unmarshal(raw.FullBytes, target)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errors.New("trailing parameter data")
	}
	return nil
}

func rawOctets(raw asn1.RawValue) ([]byte, error) {
	var octets []byte
	if err := unmarshalRaw(raw, &octets); err != nil {
		return nil, err
	}
	return octets, nil
}

func pbkdf2Hash(oid asn1.ObjectIdentifier) (func() hash.Hash, error) {
	switch {
	case len(oid) == 0, oid.Equal(oidHMACWithSHA1):
		return sha1.New, nil
	case oid.Equal(oidHMACWithSHA224):
		return sha256.New224, nil
	case oid.Equal(oidHMACWithSHA256):
		return sha256.New, nil
	case oid.Equal(oidHMACWithSHA384):
		return sha512.New384, nil
	case oid.Equal(oidHMACWithSHA512):
		return sha512.New, nil
	default:
		return nil, fmt.Errorf("%w: PRF %s", errPKCS8Unsupported, oid)
	}
}

// cbcScheme describes a supported PBES2 encryption scheme.
type cbcScheme struct {
	keyLength int
	blockSize int
	newBlock  func(key []byte) (cipher.Block, error)
}

func schemeForOID(oid asn1.ObjectIdentifier) (cbcScheme, error) {
	switch {
	case oid.Equal(oidAES128CBC):
		return cbcScheme{16, aes.BlockSize, aes.NewCipher}, nil
	case oid.Equal(oidAES192CBC):
		return cbcScheme{24, aes.BlockSize, aes.NewCipher}, nil
	case oid.Equal(oidAES256CBC):
		return cbcScheme{32, aes.BlockSize, aes.NewCipher}, nil
	case oid.Equal(oidTripleDESCBC):
		return cbcScheme{24, des.BlockSize, des.NewTripleDESCipher}, nil
	default:
		return cbcScheme{}, fmt.Errorf("%w: cipher %s", errPKCS8Unsupported, oid)
	}
}

// unpadPKCS7 validates and removes PKCS#7 padding. Every padding byte is
// checked, not only the last, so malformed plaintext is rejected rather than
// accepted with a short key.
func unpadPKCS7(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("pkcs8: invalid padded length")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("pkcs8: invalid padding")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("pkcs8: invalid padding")
		}
	}
	return data[:len(data)-pad], nil
}
