package vault

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"time"

	"github.com/mslxl/natcatty/domain/model"
)

// hostIDEncoding is Crockford's base32, the alphabet ULIDs use.
var hostIDEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// newHostID returns a ULID: a 48-bit millisecond timestamp and 80 random
// bits, rendered as 26 Crockford base32 characters. The ID stays opaque to
// callers; only its uniqueness and sortable prefix matter.
func newHostID() model.HostID {
	return model.HostID(newULID())
}

// newSSHKeyID returns an SSH Key identifier with the same opaque ULID shape as
// a Host identifier.
func newSSHKeyID() model.KeyID {
	return model.KeyID(newULID())
}

// newIdentityID returns an Identity identifier with the same opaque ULID shape
// as a Host identifier.
func newIdentityID() model.IdentityID {
	return model.IdentityID(newULID())
}

// newProxyProfileID returns a Proxy Profile identifier with the same opaque
// ULID shape as a Host identifier.
func newProxyProfileID() string { return newULID() }

// newSnippetID returns a Snippet identifier with the same opaque ULID shape
// as a Host identifier.
func newSnippetID() string { return newULID() }

// newNoteID returns a Vault Note identifier with the same opaque ULID shape as
// a Host identifier.
func newNoteID() string { return newULID() }

func newULID() string {
	var id [16]byte
	milliseconds := uint64(time.Now().UnixMilli())
	id[0] = byte(milliseconds >> 40)
	id[1] = byte(milliseconds >> 32)
	id[2] = byte(milliseconds >> 24)
	id[3] = byte(milliseconds >> 16)
	id[4] = byte(milliseconds >> 8)
	id[5] = byte(milliseconds)
	if _, err := rand.Read(id[6:]); err != nil {
		// crypto/rand does not fail on supported platforms, but keep IDs
		// distinct rather than panic if it ever does.
		binary.BigEndian.PutUint64(id[8:], uint64(time.Now().UnixNano()))
	}
	return hostIDEncoding.EncodeToString(id[:])
}
