package rfb

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"math/big"
	"testing"
)

// The fixed vector uses the RFC 2409 Oakley Group 2 prime, generator 2, and
// private exponents that are 128 repetitions of one byte. The expected
// ciphertext was computed independently of the client's KDF: the test below
// decrypts it with crypto/md5 and crypto/aes to prove the padding and strings,
// and compares the wire bytes to a constant so a KDF regression cannot hide
// behind the production helper.
const (
	appleDHTestPrimeHex = "ffffffffffffffffc90fdaa22168c234c4c6628b80dc1cd1" +
		"29024e088a67cc74020bbea63b139b22514a08798e3404dd" +
		"ef9519b3cd3a431b302b0a6df25f14374fe1356d6d51c245" +
		"e485b576625e7ec6f44c42e9a637ed6b0bff5cb6f406b7ed" +
		"ee386bfb5a899fa5ae9f24117c4b1fe649286651ece65381" +
		"ffffffffffffffff"
	appleDHTestServerPublicHex = "f5a57cef176208f2fb99ce9b1f0af9374ba069e65db042f2f4db8dbcd8058c32" +
		"59f06cce262f8604cfc718e7b7f88cdd10985b2b345e093936b8327b00e83d6f" +
		"be91599b5b409d04fb9a5458e82a5cec83763d54fab0852e44497310f98dac9f" +
		"a72b337b73c1a79068f30fc939a52d7d7fed2569ba3fe6c4e63119c4690000a8"
	appleDHTestClientPublicHex = "81ad5ee9e59af2006d508ad46dc55c47ccc101bda703c340ec11d88b97b61865" +
		"9202734dd6129dc40cde35311b9aad615d6f200fae628f18443d6d41bc0a29c1" +
		"c002cb3a3776951bbfea8cc7b5c0e1b00bd86c88974b5f5034bd778dbeeb8117" +
		"b4ed552087aa00fdaa9f96c7e1443aa658d67359d0622eddf418ced4599f62e9"
	appleDHTestCiphertextHex = "7fcc5e4c5328d53ce2385666cbbe88eb7beb2333ab1106379246036238a6dce7" +
		"7beb2333ab1106379246036238a6dce77beb2333ab1106379246036238a6dce7" +
		"ec15f77e177492c6e6950832f6138ac87beb2333ab1106379246036238a6dce7" +
		"7beb2333ab1106379246036238a6dce77beb2333ab1106379246036238a6dce7"
)

func appleDHAuthTestInt(t *testing.T, value string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(value, 16)
	if !ok {
		t.Fatalf("invalid big integer %q", value)
	}
	return n
}

func appleDHRepeatedByte(t *testing.T, value byte, size int) *big.Int {
	t.Helper()
	data := bytes.Repeat([]byte{value}, size)
	return new(big.Int).SetBytes(data)
}

// TestAppleDHKnownAnswer pins the client's encrypted credential block and
// client public value for fixed DH inputs. It independently decrypts the block
// to prove the username and password are NUL-terminated and the rest of each
// 64-byte half is zero padding.
func TestAppleDHKnownAnswer(t *testing.T) {
	const keyBytes = 128
	prime := appleDHAuthTestInt(t, appleDHTestPrimeHex)
	generator := big.NewInt(2)
	serverPrivate := appleDHRepeatedByte(t, 0x02, keyBytes)
	clientPrivate := appleDHRepeatedByte(t, 0x01, keyBytes)
	serverPublic := new(big.Int).Exp(generator, serverPrivate, prime)
	shared := new(big.Int).Exp(serverPublic, clientPrivate, prime)

	if got, want := hex.EncodeToString(serverPublic.FillBytes(make([]byte, keyBytes))), appleDHTestServerPublicHex; got != want {
		t.Fatalf("server public = %s, want %s", got, want)
	}

	var params bytes.Buffer
	var header [4]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(generator.Int64()))
	binary.BigEndian.PutUint16(header[2:4], keyBytes)
	params.Write(header[:])
	params.Write(prime.FillBytes(make([]byte, keyBytes)))
	params.Write(serverPublic.FillBytes(make([]byte, keyBytes)))

	var output bytes.Buffer
	if err := appleDHAuthenticateWithRand(
		bytes.NewReader(params.Bytes()), &output,
		"viewer", "apple-dh-pass",
		bytes.NewReader(clientPrivate.FillBytes(make([]byte, keyBytes))),
	); err != nil {
		t.Fatalf("appleDHAuthenticateWithRand: %v", err)
	}

	wantCiphertext := mustDecodeHex(t, appleDHTestCiphertextHex)
	wantClientPublic := mustDecodeHex(t, appleDHTestClientPublicHex)
	want := append(append([]byte(nil), wantCiphertext...), wantClientPublic...)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("wire response = %x, want %x", output.Bytes(), want)
	}

	// Independently decrypt with MD5(shared) and AES-ECB. The KDF here is
	// written in the test, not shared with the client or the test server.
	aesKey := md5.Sum(shared.FillBytes(make([]byte, keyBytes)))
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	plain := make([]byte, appleDHBlockSize)
	for offset := 0; offset < appleDHBlockSize; offset += aes.BlockSize {
		block.Decrypt(plain[offset:offset+aes.BlockSize], wantCiphertext[offset:offset+aes.BlockSize])
	}
	if got := string(plain[:len("viewer")]); got != "viewer" {
		t.Fatalf("username = %q, want viewer", got)
	}
	if got := string(plain[appleDHCredential : appleDHCredential+len("apple-dh-pass")]); got != "apple-dh-pass" {
		t.Fatalf("password = %q, want apple-dh-pass", got)
	}
	for i := len("viewer"); i < appleDHCredential; i++ {
		if plain[i] != 0 {
			t.Fatalf("username padding byte %d = %#x, want 0", i, plain[i])
		}
	}
	for i := appleDHCredential + len("apple-dh-pass"); i < appleDHBlockSize; i++ {
		if plain[i] != 0 {
			t.Fatalf("password padding byte %d = %#x, want 0", i, plain[i])
		}
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("hex %q: %v", value, err)
	}
	return decoded
}
