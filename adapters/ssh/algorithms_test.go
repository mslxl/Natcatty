package ssh

import (
	"context"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/mslxl/natcatty/adapters/sshtest/fakessh"
	appconnection "github.com/mslxl/natcatty/application/connection"
)

func offerFor(t *testing.T, dialer *Dialer, policy appconnection.AlgorithmPolicy) algorithmOffer {
	t.Helper()
	offer, err := dialer.offerFor(policy)
	if err != nil {
		t.Fatalf("offerFor: %v", err)
	}
	return offer
}

func TestOfferForAppliesLegacyAlgorithmsAdditively(t *testing.T) {
	dialer := NewDialer()
	offer := offerFor(t, dialer, appconnection.AlgorithmPolicy{Legacy: true})

	for _, want := range []string{"aes128-gcm@openssh.com", "aes128-cbc", "3des-cbc"} {
		if !slices.Contains(offer.ciphers, want) {
			t.Fatalf("legacy ciphers = %v, missing %q", offer.ciphers, want)
		}
	}
	for _, want := range []string{"curve25519-sha256", "diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1"} {
		if !slices.Contains(offer.keyExchanges, want) {
			t.Fatalf("legacy key exchanges = %v, missing %q", offer.keyExchanges, want)
		}
	}
	for _, want := range []string{"ssh-ed25519", "rsa-sha2-256", "ssh-rsa", "ssh-dss"} {
		if !slices.Contains(offer.hostKeys, want) {
			t.Fatalf("legacy host keys = %v, missing %q", offer.hostKeys, want)
		}
	}
	seen := make(map[string]bool, len(offer.macs))
	for _, mac := range offer.macs {
		if seen[mac] {
			t.Fatalf("legacy MACs contain duplicate %q: %v", mac, offer.macs)
		}
		seen[mac] = true
	}
	if !slices.Contains(offer.macs, "hmac-sha1-96") {
		t.Fatalf("legacy MACs = %v, missing the insecure SHA-1-96 option", offer.macs)
	}
}

func TestOfferForRejectsAnUnimplementedOverride(t *testing.T) {
	dialer := NewDialer()
	for _, policy := range []appconnection.AlgorithmPolicy{
		{Ciphers: []string{"aes256-cbc"}},
		{KeyExchanges: []string{"diffie-hellman-group18-sha512"}},
		{MACs: []string{"hmac-md5"}},
		{HostKeys: []string{"ssh-dss2"}},
	} {
		_, err := dialer.offerFor(policy)
		var typed *appconnection.Error
		if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
			t.Fatalf("policy %+v: error = %v, want an unsupported-algorithm error", policy, err)
		}
		if !errors.Is(err, appconnection.ErrUnsupportedAlgorithm) {
			t.Fatalf("policy %+v: error = %v, want the unsupported-algorithm cause", policy, err)
		}
	}
}

func TestOfferForReplacesACategoryWhenOverridden(t *testing.T) {
	dialer := NewDialer()
	offer := offerFor(t, dialer, appconnection.AlgorithmPolicy{
		Legacy:   true,
		Ciphers:  []string{"aes256-ctr"},
		HostKeys: []string{"ssh-ed25519"},
	})

	if !slices.Equal(offer.ciphers, []string{"aes256-ctr"}) {
		t.Fatalf("ciphers = %v, want the override alone", offer.ciphers)
	}
	if !slices.Equal(offer.hostKeys, []string{"ssh-ed25519"}) {
		t.Fatalf("host keys = %v, want the override alone", offer.hostKeys)
	}
	// Untouched categories still inherit the legacy additions.
	if !slices.Contains(offer.keyExchanges, "diffie-hellman-group14-sha1") {
		t.Fatalf("key exchanges = %v, want the legacy addition", offer.keyExchanges)
	}
}

func TestOfferForSkipECDSAFiltersTheFinalHostKeyList(t *testing.T) {
	dialer := NewDialer()
	offer := offerFor(t, dialer, appconnection.AlgorithmPolicy{SkipECDSAHostKey: true})
	for _, key := range offer.hostKeys {
		if len(key) >= len("ecdsa-sha2-") && key[:len("ecdsa-sha2-")] == "ecdsa-sha2-" {
			t.Fatalf("host keys = %v still offer ECDSA", offer.hostKeys)
		}
	}
	if !slices.Contains(offer.hostKeys, "ssh-ed25519") || !slices.Contains(offer.hostKeys, "rsa-sha2-512") {
		t.Fatalf("host keys = %v lost non-ECDSA algorithms", offer.hostKeys)
	}

	override := offerFor(t, dialer, appconnection.AlgorithmPolicy{
		SkipECDSAHostKey: true,
		HostKeys:         []string{"ecdsa-sha2-nistp256", "ssh-ed25519"},
	})
	if !slices.Equal(override.hostKeys, []string{"ssh-ed25519"}) {
		t.Fatalf("host keys = %v, want ECDSA removed after the override", override.hostKeys)
	}

	_, err := dialer.offerFor(appconnection.AlgorithmPolicy{
		SkipECDSAHostKey: true,
		HostKeys:         []string{"ecdsa-sha2-nistp256"},
	})
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("error = %v, want a typed unsupported-algorithm error instead of an empty list", err)
	}
	if !errors.Is(err, appconnection.ErrUnsupportedAlgorithm) {
		t.Fatalf("error = %v, want the unsupported-algorithm cause", err)
	}
}

func TestDialUsesEachEndpointsAlgorithmPolicy(t *testing.T) {
	// The first endpoint accepts only a legacy cipher; the target accepts only
	// a modern one. Only a per-endpoint policy can reach both.
	target := startChainFixture(t, fakessh.Options{
		Password: "target",
		Ciphers:  []string{"aes256-gcm@openssh.com"},
	})
	jump := startChainFixture(t, fakessh.Options{
		Password: "jump",
		Ciphers:  []string{"aes128-cbc"},
		ForwardDial: func(string, int) (net.Conn, error) {
			return target.client, nil
		},
	})
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return jump.client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)

	client, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "target.internal", Port: 22, Username: "app", Password: "target",
		Algorithms: appconnection.AlgorithmPolicy{Ciphers: []string{"aes256-gcm@openssh.com"}},
		Jumps: []appconnection.DialHop{{
			Label: "gateway", Hostname: "gateway.internal", Port: 22, Username: "ops",
			Password:      "jump",
			VerifyHostKey: acceptKey,
			Algorithms:    appconnection.AlgorithmPolicy{Legacy: true},
		}},
		VerifyHostKey: acceptKey,
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	if username, password := jump.server.LastCredentials(); username != "ops" || password != "jump" {
		t.Fatalf("jump credentials = %q/%q", username, password)
	}
	if username, password := target.server.LastCredentials(); username != "app" || password != "target" {
		t.Fatalf("target credentials = %q/%q", username, password)
	}
	_ = client.Close()
	waitServer(t, jump.server)
	waitServer(t, target.server)
}

func TestDialReportsAnAlgorithmMismatchAsUnsupported(t *testing.T) {
	legacyOnly := startChainFixture(t, fakessh.Options{
		Password: "right",
		Ciphers:  []string{"aes128-cbc"},
	})
	dialer := NewDialer(
		WithDialFunc(func(context.Context, string, string) (net.Conn, error) { return legacyOnly.client, nil }),
		WithTimeouts(2*time.Second, 2*time.Second),
	)

	_, err := dialer.Dial(context.Background(), appconnection.DialRequest{
		Hostname: "legacy.internal", Port: 22, Username: "app", Password: "right",
		VerifyHostKey: acceptKey,
	})
	if err == nil {
		t.Fatal("Dial succeeded without the legacy cipher")
	}
	var typed *appconnection.Error
	if !errors.As(err, &typed) || typed.Kind != appconnection.KindUnsupported {
		t.Fatalf("error = %v, want an unsupported-algorithm error", err)
	}
	if typed.Hop == nil || typed.Hop.Index != 1 || typed.Hop.Total != 1 {
		t.Fatalf("hop = %+v", typed.Hop)
	}
}
