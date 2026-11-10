package plugin

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"time"

	domainconn "github.com/mslxl/natcatty/domain/connection"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// TrustVerifyResult is the non-secret trust decision returned to a plugin.
type TrustVerifyResult struct {
	Verdict     TrustVerdict `json:"verdict"`
	Fingerprint string       `json:"fingerprint"`
	KeyType     string       `json:"keyType,omitempty"`
}

// TrustVerify classifies SSH host keys, TLS certificates, and custom
// fingerprints against host-owned pins. Unknown material triggers the host
// confirmer; changed and revoked material block and are never confirmed.
func (s *ServiceSession) TrustVerify(ctx context.Context, hostID string,
	kind TrustKind, material []byte) (TrustVerifyResult, error) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
			domainplugin.PermissionTrustVerify, domainplugin.Scope{},
			workspaceInvalid("trust.verify requires a Host ID"))
	}
	if !trustKindKnown(kind) {
		return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
			domainplugin.PermissionTrustVerify, domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: hostID},
			workspaceInvalid("unknown trust material kind %q", kind))
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeHost, Host: hostID}
	if err := s.check(ServiceTrust, "verify", domainplugin.PermissionTrustVerify, scope); err != nil {
		return TrustVerifyResult{}, err
	}
	keyType, fingerprint, err := trustFingerprint(kind, material)
	if err != nil {
		return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
			domainplugin.PermissionTrustVerify, scope, err)
	}
	if s.host.trustPins == nil {
		return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
			domainplugin.PermissionTrustVerify, scope, externalUnavailable("trust pin store"))
	}
	// The confirmation is host work, not guest compute: it gets its own bound
	// and stops on unload or revocation.
	workCtx, cancelWork := s.workContext(ctx)
	defer cancelWork()
	pins, err := s.host.trustPins.Pins(workCtx, hostID, kind)
	if err != nil {
		return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
			domainplugin.PermissionTrustVerify, scope,
			workspaceErrorf(ServiceCodeFault, "trust pin store: %v", err))
	}
	verdict := classifyTrust(pins, kind, keyType, fingerprint)
	if verdict == TrustVerdictUnknown && s.host.trustConfirmer != nil {
		epoch := s.handleEpochSnapshot()
		confirmCtx, cancel := context.WithTimeout(workCtx, s.host.limits.TrustConfirmTimeout)
		confirmed, confirmErr := s.host.trustConfirmer.Confirm(confirmCtx, TrustRequest{
			HostID: hostID, Kind: kind, KeyType: keyType,
			Fingerprint: fingerprint, Material: append([]byte(nil), material...),
		})
		cancel()
		// A revocation or unload that raced the prompt wins. The plugin never
		// gets a trusted verdict from material reviewed under an authority it
		// no longer holds, and no pin is persisted.
		if revoked := s.capabilityRevoked(epoch, domainplugin.PermissionTrustVerify, scope); revoked != nil {
			return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
				domainplugin.PermissionTrustVerify, scope,
				revokedServiceError(ServiceTrust, "verify", s.subject,
					domainplugin.PermissionTrustVerify, scope,
					"the plugin capability was revoked during trust verification"))
		}
		if confirmErr == nil && confirmed {
			record := TrustRecord{
				HostID: hostID, Kind: kind, KeyType: keyType,
				Fingerprint: fingerprint, UpdatedAt: s.host.now().UTC(),
			}
			saveErr := s.host.trustPins.Save(workCtx, record)
			// Revocation may also land while the pin is being written. It
			// wins whether or not the write reported success; a successful
			// write is rolled back so material reviewed under a revoked
			// capability never stays trusted.
			if revoked := s.capabilityRevoked(epoch, domainplugin.PermissionTrustVerify, scope); revoked != nil {
				if saveErr == nil {
					s.rollbackTrustPin(record)
				}
				return TrustVerifyResult{}, s.recordFailure(ServiceTrust, "verify",
					domainplugin.PermissionTrustVerify, scope,
					revokedServiceError(ServiceTrust, "verify", s.subject,
						domainplugin.PermissionTrustVerify, scope,
						"the plugin capability was revoked during trust verification"))
			}
			if saveErr == nil {
				verdict = TrustVerdictTrusted
			}
		}
	}
	// Every decision is audited with names and the verdict only. Material,
	// certificate contents, and fingerprints are never written to audit.
	result := TrustVerifyResult{Verdict: verdict, Fingerprint: fingerprint, KeyType: keyType}
	s.record(ServiceTrust, "verify", domainplugin.PermissionTrustVerify, scope, string(verdict))
	return result, nil
}

// trustRollbackTimeout bounds the best-effort delete that removes a pin
// persisted just before a revocation won.
const trustRollbackTimeout = 2 * time.Second

// rollbackTrustPin removes one just-saved pin after a revocation raced the
// write. The result is already revoked, so a failed rollback is deliberately
// silent: the host store is the only place the material could remain, and the
// next verification sees the revocation through the authority, not the pin.
func (s *ServiceSession) rollbackTrustPin(record TrustRecord) {
	if s == nil || s.host == nil || s.host.trustPins == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), trustRollbackTimeout)
	defer cancel()
	_ = s.host.trustPins.Delete(ctx, record)
}

func classifyTrust(pins []TrustRecord, kind TrustKind, keyType,
	fingerprint string) TrustVerdict {
	changed := false
	revokedMatch := false
	for _, pin := range pins {
		if pin.Fingerprint == fingerprint {
			if pin.Revoked {
				revokedMatch = true
				continue
			}
			return TrustVerdictTrusted
		}
		if pin.Kind != kind {
			continue
		}
		switch kind {
		case TrustKindSSHHostKey:
			if keyType == "" || pin.KeyType == "" || pin.KeyType == keyType {
				changed = true
			}
		default:
			changed = true
		}
	}
	if revokedMatch {
		return TrustVerdictRevoked
	}
	if changed {
		return TrustVerdictChanged
	}
	return TrustVerdictUnknown
}

// trustFingerprint canonicalizes one material value. The host owns the
// derivation; the plugin only supplies the raw bytes.
func trustFingerprint(kind TrustKind, material []byte) (keyType, fingerprint string, err error) {
	switch kind {
	case TrustKindSSHHostKey:
		return sshHostKeyFingerprint(material)
	case TrustKindTLSCert:
		return tlsCertificateFingerprint(material)
	case TrustKindCustomFingerprint:
		fingerprint, err := customFingerprint(material)
		return "", fingerprint, err
	default:
		return "", "", workspaceInvalid("unknown trust material kind %q", kind)
	}
}

// sshHostKeyFingerprint accepts a bare base64 SSH key blob or the usual
// "keytype base64" form. The returned fingerprint matches OpenSSH's SHA-256
// display form without the prefix or padding.
func sshHostKeyFingerprint(material []byte) (string, string, error) {
	value := strings.TrimSpace(string(material))
	if value == "" {
		return "", "", workspaceInvalid("ssh host key material is empty")
	}
	if fields := strings.Fields(value); len(fields) >= 2 && isSSHKeyType(fields[0]) {
		value = fields[1]
	}
	blob, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(blob) == 0 {
		return "", "", workspaceInvalid("ssh host key material is not a base64 key blob")
	}
	keyType, ok := sshBlobKeyType(blob)
	if !ok {
		return "", "", workspaceInvalid("ssh host key material is not a well-formed key blob")
	}
	return keyType, domainconn.Fingerprint(blob), nil
}

// sshBlobKeyType reads the first SSH string, which is the key algorithm name.
func sshBlobKeyType(blob []byte) (string, bool) {
	if len(blob) < 4 {
		return "", false
	}
	length := binary.BigEndian.Uint32(blob[:4])
	if length == 0 || length > 64 || int(length)+4 > len(blob) {
		return "", false
	}
	keyType := string(blob[4 : 4+length])
	if !isSSHKeyType(keyType) {
		return "", false
	}
	return keyType, true
}

func isSSHKeyType(value string) bool {
	return strings.HasPrefix(value, "ssh-") ||
		strings.HasPrefix(value, "ecdsa-") ||
		strings.HasPrefix(value, "sk-")
}

// tlsCertificateFingerprint accepts base64 DER or a PEM certificate. The
// fingerprint is SHA-256 over the DER bytes, formatted like a certificate
// pin.
func tlsCertificateFingerprint(material []byte) (string, string, error) {
	der := material
	if block, _ := pem.Decode(material); block != nil {
		if block.Type != "CERTIFICATE" {
			return "", "", workspaceInvalid("TLS material is not a certificate")
		}
		der = block.Bytes
	} else if decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(material))); err == nil {
		der = decoded
	}
	if len(der) == 0 {
		return "", "", workspaceInvalid("TLS certificate material is empty")
	}
	if _, err := x509.ParseCertificate(der); err != nil {
		return "", "", workspaceInvalid("TLS certificate material is not a DER certificate")
	}
	sum := sha256.Sum256(der)
	return "tls-cert", "sha256:" + hex.EncodeToString(sum[:]), nil
}

// customFingerprint validates an opaque, printable fingerprint supplied by a
// host-confirmed convention. It is canonicalized to lower case so comparison
// is stable.
func customFingerprint(material []byte) (string, error) {
	value := strings.TrimSpace(string(material))
	if value == "" {
		return "", workspaceInvalid("custom fingerprint material is empty")
	}
	if len(value) > 512 {
		return "", workspaceLimit("custom fingerprint exceeds 512 bytes")
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", workspaceInvalid("custom fingerprint contains a control character")
		}
	}
	return strings.ToLower(value), nil
}
