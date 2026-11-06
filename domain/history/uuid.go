package history

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// NetcattyNamespace is the fixed namespace for deterministic Netcatty UUIDs.
// It is UUIDv5 over the DNS namespace with the name "netcatty":
//
//	UUIDv5(6ba7b810-9dad-11d1-80b4-00c04fd430c8, "netcatty")
//
// Keeping one documented namespace makes every Session UUID and every derived
// import UUID stable across processes, releases, and devices.
const NetcattyNamespace = "0fde036d-3a30-5744-aa14-f28b09d90a8a"

// SessionUUIDPrefix separates Session identities from other values derived in
// the Netcatty namespace, so a Session id can never collide with an imported
// entry's natural key.
const SessionUUIDPrefix = "session:"

var netcattyNamespace = mustParseUUID(NetcattyNamespace)

// fallbackUUIDCounter keeps the entropy-failure fallback distinct within one
// process, so two commands in the same millisecond cannot collapse into one
// history row.
var fallbackUUIDCounter atomic.Uint64

// NewUUIDv7 returns an RFC 9562 UUIDv7 whose first 48 bits are at's Unix
// milliseconds. The remaining bits come from the platform entropy source.
// An error means the platform could not supply random bytes.
func NewUUIDv7(at time.Time) (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return formatUUIDv7(at, entropy), nil
}

// UUIDv7 returns a UUIDv7 for the current time. If the platform entropy
// source fails, it still returns a well-formed UUIDv7 with zero entropy:
// crypto/rand does not fail on supported platforms, and a malformed identity
// would be worse than a collision risk that cannot occur in practice.
func UUIDv7() string {
	at := time.Now()
	value, err := NewUUIDv7(at)
	if err == nil {
		return value
	}
	var entropy [16]byte
	binary.BigEndian.PutUint64(entropy[8:], fallbackUUIDCounter.Add(1))
	return formatUUIDv7(at, entropy)
}

// IsUUID reports whether value is a canonical UUID string. Case is accepted
// either way; the nil UUID is not an identity and is rejected. The shape check
// deliberately does not require a particular version, because imported Atuin
// rows may carry a UUID this client did not generate.
func IsUUID(value string) bool {
	parsed, ok := parseUUID(value)
	if !ok {
		return false
	}
	var zero [16]byte
	return parsed != zero
}

// DerivedUUID returns the deterministic UUIDv5 for name in the Netcatty
// namespace. The same name always produces the same UUID, so a repeated
// import maps to the same journal row.
func DerivedUUID(name string) string {
	return formatUUIDv5(netcattyNamespace, name)
}

// SessionUUID returns the stable Atuin session identity for an app Session.
// An empty Session has no session UUID.
func SessionUUID(sessionID model.SessionID) string {
	if sessionID == "" {
		return ""
	}
	return DerivedUUID(SessionUUIDPrefix + string(sessionID))
}

// DerivedEntryUUID returns the stable fallback identity for an imported or
// synced entry that did not carry a valid UUID. The natural key is the source,
// origin, Atuin session, recorded time, and command; the existing ID is
// deliberately excluded so a malformed remote identity cannot make two
// imports of the same row differ.
func DerivedEntryUUID(entry Entry) string {
	parts := []string{
		entry.Source,
		entry.Origin,
		entry.SessionUUID,
		strconv.FormatInt(entry.RecordedAt.UnixNano(), 10),
		entry.Command,
	}
	return DerivedUUID(strings.Join(parts, "\x00"))
}

func formatUUIDv7(at time.Time, entropy [16]byte) string {
	milliseconds := uint64(at.UnixMilli())
	value := entropy
	value[0] = byte(milliseconds >> 40)
	value[1] = byte(milliseconds >> 32)
	value[2] = byte(milliseconds >> 24)
	value[3] = byte(milliseconds >> 16)
	value[4] = byte(milliseconds >> 8)
	value[5] = byte(milliseconds)
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80
	return formatUUID(value)
}

func formatUUIDv5(namespace [16]byte, name string) string {
	digest := sha1.New()
	digest.Write(namespace[:])
	digest.Write([]byte(name))
	sum := digest.Sum(nil)
	var value [16]byte
	copy(value[:], sum[:16])
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return formatUUID(value)
}

func formatUUID(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:32]
}

func parseUUID(value string) ([16]byte, bool) {
	var parsed [16]byte
	if len(value) != 36 || value[8] != '-' || value[13] != '-' ||
		value[18] != '-' || value[23] != '-' {
		return parsed, false
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != 16 {
		return parsed, false
	}
	copy(parsed[:], decoded)
	return parsed, true
}

func mustParseUUID(value string) [16]byte {
	parsed, ok := parseUUID(value)
	if !ok {
		panic("history: invalid UUID constant " + value)
	}
	return parsed
}
