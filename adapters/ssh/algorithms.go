package ssh

import (
	"fmt"
	"strings"

	xssh "golang.org/x/crypto/ssh"

	appconnection "github.com/mslxl/natcatty/application/connection"
)

// Explicit algorithm offers. x/crypto/ssh's defaults are already modern, but
// naming them here makes the transport's capabilities reviewable and keeps
// SHA-1 host keys and key exchange off the wire. The negotiation error is
// surfaced as an unsupported-algorithm connection error.
var (
	defaultCiphers = []string{
		"aes128-gcm@openssh.com",
		"aes256-gcm@openssh.com",
		"chacha20-poly1305@openssh.com",
		"aes128-ctr",
		"aes192-ctr",
		"aes256-ctr",
	}

	defaultKeyExchanges = []string{
		"curve25519-sha256",
		"curve25519-sha256@libssh.org",
		"ecdh-sha2-nistp256",
		"ecdh-sha2-nistp384",
		"ecdh-sha2-nistp521",
		"diffie-hellman-group14-sha256",
		"diffie-hellman-group16-sha512",
		"diffie-hellman-group-exchange-sha256",
	}

	defaultMACs = []string{
		"hmac-sha2-256-etm@openssh.com",
		"hmac-sha2-512-etm@openssh.com",
		"hmac-sha2-256",
		"hmac-sha2-512",
		"hmac-sha1",
	}

	// RSA is offered only with the SHA-2 signature algorithms. ssh-rsa
	// (SHA-1) and ssh-dss are never proposed.
	defaultHostKeys = []string{
		"ssh-ed25519",
		"ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384",
		"ecdsa-sha2-nistp521",
		"rsa-sha2-512",
		"rsa-sha2-256",
	}
)

// Legacy additions mirror the original applyLegacyAlgorithms: they broaden an
// endpoint's offer when its Host enables legacy algorithms. x/crypto/ssh
// implements only some of the original list; unsupported names (aes256-cbc,
// hmac-md5, DH group15/17/18) cannot be offered at all.
var (
	legacyCiphers = []string{
		"aes128-cbc",
		"3des-cbc",
	}

	legacyKeyExchanges = []string{
		"diffie-hellman-group14-sha1",
		"diffie-hellman-group1-sha1",
		"diffie-hellman-group-exchange-sha1",
	}

	legacyMACs = []string{
		"hmac-sha2-256-etm@openssh.com",
		"hmac-sha2-512-etm@openssh.com",
		"hmac-sha2-256",
		"hmac-sha2-512",
		"hmac-sha1",
		"hmac-sha1-96",
	}

	legacyHostKeys = []string{
		"ssh-rsa",
		"ssh-dss",
	}
)

// algorithmOffer is the effective negotiation offer for one endpoint.
type algorithmOffer struct {
	ciphers      []string
	keyExchanges []string
	macs         []string
	hostKeys     []string
}

// offerFor applies one endpoint's policy over the Dialer's base offer: legacy
// algorithms append, a non-empty category replaces its list, and the ECDSA
// kill switch filters the final host-key list. The order mirrors the original:
// legacy, then overrides, then the skip filter. A skip that removes every
// requested algorithm is an error: x/crypto/ssh treats an empty list as its
// default offer, which would silently reintroduce ECDSA.
func (d *Dialer) offerFor(policy appconnection.AlgorithmPolicy) (algorithmOffer, error) {
	if err := validateOverrides(policy); err != nil {
		return algorithmOffer{}, err
	}
	offer := algorithmOffer{
		ciphers:      copyStrings(d.ciphers),
		keyExchanges: copyStrings(d.keyExchanges),
		macs:         copyStrings(d.macs),
		hostKeys:     copyStrings(d.hostKeyAlgos),
	}
	if policy.Legacy {
		offer.ciphers = appendUnique(offer.ciphers, legacyCiphers...)
		offer.keyExchanges = appendUnique(offer.keyExchanges, legacyKeyExchanges...)
		offer.macs = appendUnique(offer.macs, legacyMACs...)
		offer.hostKeys = appendUnique(offer.hostKeys, legacyHostKeys...)
	}
	if len(policy.Ciphers) > 0 {
		offer.ciphers = copyStrings(policy.Ciphers)
	}
	if len(policy.KeyExchanges) > 0 {
		offer.keyExchanges = copyStrings(policy.KeyExchanges)
	}
	if len(policy.MACs) > 0 {
		offer.macs = copyStrings(policy.MACs)
	}
	if len(policy.HostKeys) > 0 {
		offer.hostKeys = copyStrings(policy.HostKeys)
	}
	if policy.SkipECDSAHostKey {
		offer.hostKeys = withoutECDSA(offer.hostKeys)
		if len(offer.hostKeys) == 0 {
			return algorithmOffer{}, &appconnection.Error{
				Kind: appconnection.KindUnsupported,
				Cause: fmt.Errorf(
					"%w: skipEcdsaHostKey removes every requested host-key algorithm",
					appconnection.ErrUnsupportedAlgorithm,
				),
			}
		}
	}
	return offer, nil
}

// implementedAlgorithms is every name this transport can negotiate, including
// insecure opt-ins and the libssh Curve25519 alias. x/crypto/ssh silently drops
// unknown names from Config, so the adapter rejects them instead.
var implementedAlgorithms = func() map[string]map[string]bool {
	build := func(values ...[]string) map[string]bool {
		set := make(map[string]bool)
		for _, list := range values {
			for _, value := range list {
				set[value] = true
			}
		}
		return set
	}
	supported := xssh.SupportedAlgorithms()
	insecure := xssh.InsecureAlgorithms()
	keyExchanges := build(supported.KeyExchanges, insecure.KeyExchanges)
	// x/crypto implements the libssh alias even though SupportedAlgorithms
	// does not list it.
	keyExchanges["curve25519-sha256@libssh.org"] = true
	return map[string]map[string]bool{
		"cipher":       build(supported.Ciphers, insecure.Ciphers),
		"key exchange": keyExchanges,
		"MAC":          build(supported.MACs, insecure.MACs),
		"host key":     build(supported.HostKeys, insecure.HostKeys),
	}
}()

// validateOverrides rejects a Host override naming an algorithm this transport
// cannot negotiate. Failing here is better than letting x/crypto's SetDefaults
// silently remove it and negotiate something the user did not choose.
func validateOverrides(policy appconnection.AlgorithmPolicy) error {
	categories := []struct {
		name   string
		values []string
	}{
		{"cipher", policy.Ciphers},
		{"key exchange", policy.KeyExchanges},
		{"MAC", policy.MACs},
		{"host key", policy.HostKeys},
	}
	for _, category := range categories {
		allowed := implementedAlgorithms[category.name]
		for _, value := range category.values {
			if !allowed[value] {
				return &appconnection.Error{
					Kind: appconnection.KindUnsupported,
					Cause: fmt.Errorf("%w: %s %q is not implemented by this transport",
						appconnection.ErrUnsupportedAlgorithm, category.name, value),
				}
			}
		}
	}
	return nil
}

// appendUnique appends values that are not already present, preserving order.
func appendUnique(values []string, extra ...string) []string {
	for _, value := range extra {
		found := false
		for _, existing := range values {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			values = append(values, value)
		}
	}
	return values
}

// withoutECDSA removes plain and certificate ECDSA host-key algorithms.
func withoutECDSA(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		if strings.HasPrefix(value, "ecdsa-sha2-") {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func copyStrings(values []string) []string {
	return append([]string(nil), values...)
}
