package transport

import (
	"errors"
	"testing"
	"time"
)

func TestReleasePolicyIsValid(t *testing.T) {
	// This is the release-owner check: flipping Default to InProcess without
	// recording both parity gates fails the build. It does not pin the default
	// itself, so the documented one-place flip does not break the test suite.
	if err := ReleasePolicy.Validate(); err != nil {
		t.Fatalf("ReleasePolicy is not shippable: %v", err)
	}
}

func TestPolicyRefusesADefaultFlipWithoutBothParityGates(t *testing.T) {
	cases := []struct {
		name     string
		verified []Gate
		wantErr  bool
	}{
		{"no evidence", nil, true},
		{"only mosh", []Gate{GateMoshParity}, true},
		{"only et", []Gate{GateETParity}, true},
		{"both", []Gate{GateMoshParity, GateETParity}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy := Policy{Default: InProcess, VerifiedGates: c.verified}
			err := policy.Validate()
			if c.wantErr {
				if !errors.Is(err, ErrGateNotVerified) {
					t.Fatalf("Validate error = %v, want ErrGateNotVerified", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestPolicyRejectsUnknownAndDuplicateVerifiedGates(t *testing.T) {
	cases := []struct {
		name    string
		policy  Policy
		wantErr error
	}{
		{
			"unknown gate while bundled",
			Policy{Default: Bundled, VerifiedGates: []Gate{"vibes"}},
			ErrInvalidPolicy,
		},
		{
			"unknown gate beside both parity gates",
			Policy{Default: InProcess, VerifiedGates: []Gate{
				GateMoshParity, GateETParity, "vibes",
			}},
			ErrInvalidPolicy,
		},
		{
			// A duplicated required gate must not satisfy the flip check by
			// accident: the evidence list itself has to be well formed.
			"duplicate parity gate",
			Policy{Default: InProcess, VerifiedGates: []Gate{
				GateMoshParity, GateETParity, GateETParity,
			}},
			ErrInvalidPolicy,
		},
		{
			"duplicate packaging gate",
			Policy{Default: InProcess, VerifiedGates: []Gate{
				GateMoshParity, GateETParity, GatePackaging, GatePackaging,
			}},
			ErrInvalidPolicy,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.policy.Validate(); !errors.Is(err, c.wantErr) {
				t.Fatalf("Validate error = %v, want %v", err, c.wantErr)
			}
		})
	}

	// Recognizing the optional packaging gate must not change the evidence
	// required for a flip.
	valid := Policy{Default: InProcess, VerifiedGates: []Gate{
		GateMoshParity, GateETParity, GatePackaging,
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate with the packaging gate: %v", err)
	}
}

func TestPolicyKeepsABlockedPlatformOnBundled(t *testing.T) {
	expires := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	linux := Platform{OS: "linux", Arch: "amd64"}
	darwin := Platform{OS: "darwin", Arch: "arm64"}
	policy := Policy{
		Default:       InProcess,
		VerifiedGates: []Gate{GateMoshParity, GateETParity},
		Waivers: []Waiver{{
			Platform: linux,
			Reason:   WaiverParityBlocker,
			Gate:     GateETParity,
			Detail:   "ET parity summary 1234 still fails on linux/amd64",
			Expires:  expires,
		}},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	before := expires.Add(-time.Hour)
	if got := policy.DefaultFor(linux, before); got != Bundled {
		t.Fatalf("waived linux default = %q, want bundled", got)
	}
	if got := policy.DefaultFor(darwin, before); got != InProcess {
		t.Fatalf("darwin default = %q, want in-process; a waiver must not hold other platforms", got)
	}

	// The expiry is exclusive, so a waiver never applies silently forever.
	if got := policy.DefaultFor(linux, expires); got != InProcess {
		t.Fatalf("linux default at expiry = %q, want in-process", got)
	}
}

func TestWaiversAreIgnoredWhileTheDefaultIsBundled(t *testing.T) {
	policy := Policy{Default: Bundled, Waivers: []Waiver{{
		Platform: Platform{OS: "windows", Arch: "arm64"},
		Reason:   WaiverPackagingBlocker,
		Gate:     GatePackaging,
		Detail:   "Windows arm64 package is not produced yet",
		Expires:  time.Now().Add(24 * time.Hour),
	}}}
	// Configuring a waiver before the flip is a no-op that Validate rejects,
	// so a release cannot leave a meaningless waiver lying around.
	if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Validate error = %v, want ErrInvalidPolicy", err)
	}
	if got := policy.DefaultFor(Platform{OS: "windows", Arch: "arm64"}, time.Now()); got != Bundled {
		t.Fatalf("default with a bundled policy = %q, want bundled", got)
	}
}

func TestPolicyRejectsIncompleteWaivers(t *testing.T) {
	platform := Platform{OS: "linux", Arch: "amd64"}
	expires := time.Now().Add(24 * time.Hour)
	valid := Waiver{
		Platform: platform,
		Reason:   WaiverParityBlocker,
		Gate:     GateMoshParity,
		Detail:   "mosh parity summary 1234 fails",
		Expires:  expires,
	}
	cases := []struct {
		name   string
		waiver Waiver
	}{
		{"no platform", func() Waiver { w := valid; w.Platform = Platform{}; return w }()},
		{"empty OS", func() Waiver { w := valid; w.Platform.OS = ""; return w }()},
		{"empty arch", func() Waiver { w := valid; w.Platform.Arch = ""; return w }()},
		{"OS trailing space", func() Waiver { w := valid; w.Platform.OS = "linux "; return w }()},
		{"OS leading space", func() Waiver { w := valid; w.Platform.OS = " linux"; return w }()},
		{"arch trailing space", func() Waiver { w := valid; w.Platform.Arch = "amd64 "; return w }()},
		{"whitespace-only OS", func() Waiver { w := valid; w.Platform.OS = "   "; return w }()},
		{"OS contains separator", func() Waiver { w := valid; w.Platform.OS = "linux/amd64"; return w }()},
		{"OS contains underscore", func() Waiver { w := valid; w.Platform.OS = "linux_amd64"; return w }()},
		{"arch contains hyphen", func() Waiver { w := valid; w.Platform.Arch = "arm-64"; return w }()},
		{"uppercase OS", func() Waiver { w := valid; w.Platform.OS = "Linux"; return w }()},
		{"unknown reason", func() Waiver { w := valid; w.Reason = "because"; return w }()},
		{"unknown gate", func() Waiver { w := valid; w.Gate = "vibes"; return w }()},
		{"no detail", func() Waiver { w := valid; w.Detail = "  "; return w }()},
		{"no expiry", func() Waiver { w := valid; w.Expires = time.Time{}; return w }()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			policy := Policy{
				Default:       InProcess,
				VerifiedGates: []Gate{GateMoshParity, GateETParity},
				Waivers:       []Waiver{c.waiver},
			}
			if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("Validate error = %v, want ErrInvalidPolicy", err)
			}
		})
	}

	// A second waiver for the same platform would make resolution depend on
	// table order, so it is rejected instead of silently picking the first.
	policy := Policy{
		Default:       InProcess,
		VerifiedGates: []Gate{GateMoshParity, GateETParity},
		Waivers:       []Waiver{valid, valid},
	}
	if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("duplicate waiver error = %v, want ErrInvalidPolicy", err)
	}
}

func TestPolicyRejectsAnUnknownDefault(t *testing.T) {
	policy := Policy{Default: Implementation("in_process_v2")}
	if err := policy.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("Validate error = %v, want ErrInvalidPolicy", err)
	}
}

func TestResolveUsesTheReleaseDefaultForAnAbsentChoice(t *testing.T) {
	previous := ReleasePolicy
	t.Cleanup(func() { ReleasePolicy = previous })
	ReleasePolicy = Policy{
		Default:       InProcess,
		VerifiedGates: []Gate{GateMoshParity, GateETParity},
	}

	got, err := Resolve(Unspecified, Unspecified)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != InProcess {
		t.Fatalf("Resolve(empty, empty) = %q, want the switched release default", got)
	}
	// An explicit Host choice still wins.
	got, err = Resolve(Unspecified, Bundled)
	if err != nil {
		t.Fatalf("Resolve override: %v", err)
	}
	if got != Bundled {
		t.Fatalf("Resolve(empty, bundled) = %q, want bundled", got)
	}
}
