package vault

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

// NetworkProfileFingerprint digests the non-secret configuration of a Network
// Profile and its secret-material version. The Vault sets UpdatedAt on every
// edit, including rotation of an Auth Key, a WireGuard Private Key, or a Peer
// Preshared Key, so a version change invalidates idle pooled transports even
// when no plaintext field changed.
//
// The digest never contains key material: profiles do not carry secrets, and
// only the Has* presence markers enter the hash. A profile's Label, OrderKey,
// and CreatedAt are bookkeeping rather than connection configuration and do
// not enter the digest directly. Any edit still bumps UpdatedAt, so a rename
// intentionally invalidates idle transports through the version rather than
// through its text.
func NetworkProfileFingerprint(profile NetworkProfile) string {
	digest := sha256.New()

	write := func(value string) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		digest.Write(size[:])
		digest.Write([]byte(value))
	}
	writeInt := func(value int64) { write(strconv.FormatInt(value, 10)) }
	writeBool := func(value bool) {
		if value {
			write("1")
			return
		}
		write("0")
	}
	writeList := func(values []string) {
		writeInt(int64(len(values)))
		for _, value := range values {
			write(value)
		}
	}

	// A version tag lets a future fingerprint change invalidate old transports
	// deterministically instead of colliding with today's digests.
	write("network-profile-fingerprint-v1")
	write(string(profile.Kind))
	writeInt(profile.UpdatedAt.UnixNano())

	if profile.Tailscale != nil {
		write("tailscale")
		write(profile.Tailscale.Hostname)
		write(profile.Tailscale.ControlURL)
		writeBool(profile.Tailscale.Ephemeral)
		writeBool(profile.Tailscale.HasAuthKey)
		writeList(profile.Tailscale.Tags)
	}
	if profile.WireGuard != nil {
		write("wireguard")
		writeBool(profile.WireGuard.HasPrivateKey)
		writeInt(int64(profile.WireGuard.MTU))
		writeInt(int64(profile.WireGuard.ListenPort))
		writeList(profile.WireGuard.Addresses)
		writeList(profile.WireGuard.DNS)
		writeInt(int64(len(profile.WireGuard.Peers)))
		for _, peer := range profile.WireGuard.Peers {
			write(peer.ID)
			write(peer.PublicKey)
			write(peer.Endpoint)
			writeBool(peer.HasPresharedKey)
			writeInt(int64(peer.PersistentKeepalive))
			writeList(peer.AllowedIPs)
		}
	}

	return hex.EncodeToString(digest.Sum(nil))[:32]
}
