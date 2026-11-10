package app

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	appconnection "github.com/mslxl/natcatty/application/connection"
	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainconn "github.com/mslxl/natcatty/domain/connection"
	"github.com/mslxl/natcatty/domain/model"
	domainvault "github.com/mslxl/natcatty/domain/vault"
	sessionui "github.com/mslxl/natcatty/ui/session"
)

// trustConfirmerVault resolves the one Host the trust confirmer test prompts.
type trustConfirmerVault struct {
	host domainvault.Host
}

func (v trustConfirmerVault) Get(context.Context, model.HostID) (domainvault.Host, error) {
	return v.host, nil
}

func TestPluginTLSConfigKeepsVerificationHostOwned(t *testing.T) {
	for _, mode := range []string{"", "verify", "system"} {
		config, err := pluginTLSConfig(appplugin.TLSClientPolicy{
			Mode: mode, ServerName: "db.internal", ALPN: []string{"h2"},
		})
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if config.InsecureSkipVerify {
			t.Fatalf("mode %q disabled certificate verification", mode)
		}
		if config.ServerName != "db.internal" {
			t.Fatalf("mode %q server name = %q", mode, config.ServerName)
		}
		if len(config.NextProtos) != 1 || config.NextProtos[0] != "h2" {
			t.Fatalf("mode %q ALPN = %v", mode, config.NextProtos)
		}
		if config.MinVersion != tls.VersionTLS12 {
			t.Fatalf("mode %q minimum TLS = %x", mode, config.MinVersion)
		}
	}

	// Verification is skipped only when the grant explicitly says none.
	config, err := pluginTLSConfig(appplugin.TLSClientPolicy{
		Mode: "none", ServerName: "legacy.internal",
	})
	if err != nil {
		t.Fatalf("none: %v", err)
	}
	if !config.InsecureSkipVerify {
		t.Fatal("none policy did not skip verification")
	}

	if _, err := pluginTLSConfig(appplugin.TLSClientPolicy{Mode: "bogus"}); err == nil {
		t.Fatal("unknown TLS policy was accepted")
	}
}

func TestPluginExternalSocketReleasesOnFirstClose(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	released := 0
	socket := &pluginExternalSocket{
		conn:    client,
		release: func() { released++ },
	}
	if err := socket.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := socket.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if released != 1 {
		t.Fatalf("release count = %d, want exactly 1", released)
	}
	if err := socket.CloseWrite(); !errors.Is(err, appplugin.ErrExternalUnsupported) {
		t.Fatalf("CloseWrite on a pipe = %v, want unsupported", err)
	}
	if err := socket.CloseRead(); !errors.Is(err, appplugin.ErrExternalUnsupported) {
		t.Fatalf("CloseRead on a pipe = %v, want unsupported", err)
	}
}

func TestPluginTrustPinsPersistNonSSHRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plugin-trust-pins.json")
	store := newPluginTrustPins(nil, nil, path)
	records, err := store.Pins(context.Background(), "db", appplugin.TrustKindTLSCert)
	if err != nil || len(records) != 0 {
		t.Fatalf("empty pins = %+v, %v", records, err)
	}
	when := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	if err := store.Save(context.Background(), appplugin.TrustRecord{
		HostID: "db", Kind: appplugin.TrustKindTLSCert, KeyType: "tls-cert",
		Fingerprint: "sha256:abcd", UpdatedAt: when,
	}); err != nil {
		t.Fatalf("save tls pin: %v", err)
	}
	if err := store.Save(context.Background(), appplugin.TrustRecord{
		HostID: "db", Kind: appplugin.TrustKindCustomFingerprint,
		Fingerprint: "custom-1", UpdatedAt: when,
	}); err != nil {
		t.Fatalf("save custom pin: %v", err)
	}

	reloaded := newPluginTrustPins(nil, nil, path)
	tlsPins, err := reloaded.Pins(context.Background(), "db", appplugin.TrustKindTLSCert)
	if err != nil || len(tlsPins) != 1 || tlsPins[0].Fingerprint != "sha256:abcd" {
		t.Fatalf("reloaded tls pins = %+v, %v", tlsPins, err)
	}
	customPins, err := reloaded.Pins(context.Background(), "db",
		appplugin.TrustKindCustomFingerprint)
	if err != nil || len(customPins) != 1 || customPins[0].Fingerprint != "custom-1" {
		t.Fatalf("reloaded custom pins = %+v, %v", customPins, err)
	}
	otherHost, err := reloaded.Pins(context.Background(), "other", appplugin.TrustKindTLSCert)
	if err != nil || len(otherHost) != 0 {
		t.Fatalf("pins for another Host = %+v, %v", otherHost, err)
	}

	// Delete is the rollback path: it removes exactly the matching record and
	// persists the removal without touching other pins.
	if err := reloaded.Delete(context.Background(), appplugin.TrustRecord{
		HostID: "db", Kind: appplugin.TrustKindTLSCert, KeyType: "tls-cert",
		Fingerprint: "sha256:abcd",
	}); err != nil {
		t.Fatalf("delete tls pin: %v", err)
	}
	after := newPluginTrustPins(nil, nil, path)
	tlsPins, err = after.Pins(context.Background(), "db", appplugin.TrustKindTLSCert)
	if err != nil || len(tlsPins) != 0 {
		t.Fatalf("tls pins after delete = %+v, %v", tlsPins, err)
	}
	customPins, err = after.Pins(context.Background(), "db",
		appplugin.TrustKindCustomFingerprint)
	if err != nil || len(customPins) != 1 || customPins[0].Fingerprint != "custom-1" {
		t.Fatalf("custom pins after delete = %+v, %v", customPins, err)
	}
}

func TestPluginTrustConfirmerFailsClosedWithoutASurface(t *testing.T) {
	confirmer := newPluginTrustConfirmer(nil, nil)
	confirmed, err := confirmer.Confirm(context.Background(), appplugin.TrustRequest{
		HostID: "db", Kind: appplugin.TrustKindTLSCert,
	})
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed {
		t.Fatal("a certificate was trusted without a host confirmation surface")
	}
}

// TestPluginTrustConfirmerSupportsSSHHostKeys proves the production confirmer
// can confirm the same material kind the sample provider presents. Other
// material kinds still fail closed without a prompt.
func TestPluginTrustConfirmerSupportsSSHHostKeys(t *testing.T) {
	prompt := sessionui.NewTrustPrompt()
	confirmer := newPluginTrustConfirmer(prompt, trustConfirmerVault{
		host: domainvault.Host{ID: "host-1", Hostname: "db.internal", Port: 2222},
	})
	blob, err := base64.StdEncoding.DecodeString(sampleProviderHostKey)
	if err != nil {
		t.Fatalf("decode sample host key: %v", err)
	}
	type confirmation struct {
		confirmed bool
		err       error
	}
	done := make(chan confirmation, 1)
	go func() {
		confirmed, err := confirmer.Confirm(context.Background(), appplugin.TrustRequest{
			HostID: sampleProviderHostID, Kind: appplugin.TrustKindSSHHostKey,
			KeyType: sampleProviderHostKeyType, Fingerprint: "sha256:test",
			Material: blob,
		})
		done <- confirmation{confirmed: confirmed, err: err}
	}()

	deadline := time.Now().Add(5 * time.Second)
	for prompt.Pending() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the SSH host key never reached the host confirmation")
		}
		time.Sleep(time.Millisecond)
	}
	key, ok := prompt.Current()
	if !ok {
		t.Fatal("the trust prompt has no current key")
	}
	if key.Hostname != "db.internal" || key.Port != 2222 ||
		key.KeyType != sampleProviderHostKeyType ||
		key.Fingerprint != "sha256:test" ||
		key.PublicKey != domainconn.PublicKey(sampleProviderHostKeyType, blob) {
		t.Fatalf("prompted key = %+v", key)
	}
	if !prompt.Answer(appconnection.PromptDecision{Trust: true, Persist: true}) {
		t.Fatal("the trust prompt did not accept the decision")
	}
	got := <-done
	if got.err != nil || !got.confirmed {
		t.Fatalf("confirm = %t, %v; want trusted", got.confirmed, got.err)
	}

	// The same confirmer keeps failing closed for kinds with no prompt surface.
	confirmed, err := confirmer.Confirm(context.Background(), appplugin.TrustRequest{
		HostID: sampleProviderHostID, Kind: appplugin.TrustKindCustomFingerprint,
	})
	if err != nil || confirmed {
		t.Fatalf("custom fingerprint confirm = %t, %v; want false, nil", confirmed, err)
	}
}
