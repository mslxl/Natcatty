package transport

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Platform identifies one GOOS/GOARCH target. A release waiver names exactly
// one platform, so a blocker on one target never holds the others on the
// bundled client.
type Platform struct {
	OS   string
	Arch string
}

// String returns the conventional "goos/goarch" form used in errors and docs.
func (p Platform) String() string { return p.OS + "/" + p.Arch }

// CurrentPlatform returns the platform this build runs on.
func CurrentPlatform() Platform {
	return Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
}

// Gate names a release gate whose green summary is required before the
// release default may move to the in-process client. The IDs match the
// tickets that own the evidence: 32 for mosh parity and 27 for ET parity.
type Gate string

const (
	GateMoshParity Gate = "mosh-parity"
	GateETParity   Gate = "et-parity"
	// GatePackaging covers a platform-specific packaging or smoke-test
	// blocker, such as Windows arm64 artifacts.
	GatePackaging Gate = "packaging"
)

// RequiredGates must be verified green in CI before ReleasePolicy.Default may
// become InProcess. A waiver may keep one platform on the bundled client, but
// it never relaxes this global requirement.
var RequiredGates = []Gate{GateMoshParity, GateETParity}

// LegacyDefault is the transport default every build before the explicit
// choice flag wrote. The settings migration treats a version 1 document
// holding it as ambiguous (it cannot prove the user chose it) and adopts the
// release default. It stays bundled forever as a historical fact, independent
// of ReleasePolicy.
const LegacyDefault Implementation = Bundled

// WaiverReason is the typed justification for keeping one platform on the
// bundled client while the others switch. Free-form detail belongs in
// Waiver.Detail.
type WaiverReason string

const (
	// WaiverParityBlocker keeps a platform on the bundled client while a
	// parity gate is still failing there.
	WaiverParityBlocker WaiverReason = "parity-blocker"
	// WaiverPackagingBlocker keeps a platform on the bundled client while
	// its in-process build or package cannot be produced.
	WaiverPackagingBlocker WaiverReason = "packaging-blocker"
	// WaiverPlatformUnsupported keeps a platform on the bundled client while
	// the in-process client has no supported implementation there.
	WaiverPlatformUnsupported WaiverReason = "platform-unsupported"
)

// Waiver pins one platform to the bundled client for one extra release
// cycle. It is intentionally narrow: it changes only the default for that
// platform, never a Host override and never the global default. The release
// owner records the gate that is not green, a specific detail with the
// evidence, and an expiry after which the waiver stops applying on its own.
type Waiver struct {
	Platform Platform
	Reason   WaiverReason
	Gate     Gate
	// Detail names the specific blocker and links its evidence.
	Detail string
	// Expires is exclusive: the waiver applies while now is before it and
	// stops applying at that instant.
	Expires time.Time
}

// Policy is the release-level default decision. ReleasePolicy below is the
// single switch the release process flips; keeping the values in a data
// structure lets tests exercise the post-switch behavior without changing
// production code.
type Policy struct {
	// Default is the implementation a new install or a user who never made an
	// explicit choice receives.
	Default Implementation
	// VerifiedGates records the gate summaries the release owner verified
	// green before flipping Default. Validate requires both RequiredGates
	// when Default is InProcess.
	VerifiedGates []Gate
	// Waivers keep named platforms on the bundled client for one extra
	// release cycle after the global default switches.
	Waivers []Waiver
}

// ReleasePolicy is the single place the release process switches the
// in-process client from opt-in beta to the default. The flip is allowed only
// after tickets 27 (ET parity) and 32 (mosh parity) are green in CI; record
// both in VerifiedGates in the same change. See
// docs/releases/transport-rollout.md.
var ReleasePolicy = Policy{
	Default: Bundled,
}

// CurrentDefault returns the release default for the running platform. The
// Settings store resolves a document that has no explicit user choice through
// this function, so a default flip also reaches installs that never touched
// the setting, and an expired waiver stops applying without a data migration.
func CurrentDefault() Implementation {
	return ReleasePolicy.DefaultFor(CurrentPlatform(), time.Now())
}

// DefaultFor applies the release policy to platform at now. A waiver only
// matters after the default has switched; while the default is bundled every
// platform already receives the bundled client.
func (p Policy) DefaultFor(platform Platform, now time.Time) Implementation {
	if p.Default != InProcess {
		return p.Default
	}
	if _, ok := p.WaiverFor(platform, now); ok {
		return Bundled
	}
	return InProcess
}

// WaiverFor returns the unexpired waiver for platform.
func (p Policy) WaiverFor(platform Platform, now time.Time) (Waiver, bool) {
	for _, waiver := range p.Waivers {
		if waiver.Platform == platform && now.Before(waiver.Expires) {
			return waiver, true
		}
	}
	return Waiver{}, false
}

var (
	// ErrInvalidPolicy reports a release policy that must not ship, such as a
	// waiver without an expiry or a default flip with no gate evidence.
	ErrInvalidPolicy = errors.New("transport: invalid release policy")
	// ErrGateNotVerified reports a default flip attempted while a required
	// parity gate has no green summary recorded.
	ErrGateNotVerified = errors.New("transport: release gate not verified")
)

// Validate reports configuration errors that must fail the build before a
// release is cut: an unknown default, an unknown or duplicated verified gate,
// a flipped default without both parity gates, or an incomplete or duplicated
// waiver. It is asserted over ReleasePolicy by the package tests.
func (p Policy) Validate() error {
	switch p.Default {
	case Bundled, InProcess:
	default:
		return fmt.Errorf("%w: default %q", ErrInvalidPolicy, p.Default)
	}
	seenGates := make(map[Gate]bool, len(p.VerifiedGates))
	for _, gate := range p.VerifiedGates {
		switch gate {
		case GateMoshParity, GateETParity, GatePackaging:
		default:
			return fmt.Errorf("%w: unknown verified gate %q", ErrInvalidPolicy, gate)
		}
		if seenGates[gate] {
			return fmt.Errorf("%w: duplicate verified gate %q", ErrInvalidPolicy, gate)
		}
		seenGates[gate] = true
	}
	if p.Default == InProcess {
		for _, gate := range RequiredGates {
			if !slices.Contains(p.VerifiedGates, gate) {
				return fmt.Errorf("%w: %s must be green before the default flip",
					ErrGateNotVerified, gate)
			}
		}
	}
	seen := make(map[Platform]bool, len(p.Waivers))
	for index, waiver := range p.Waivers {
		if err := waiver.validate(); err != nil {
			return fmt.Errorf("%w: waiver %d: %w", ErrInvalidPolicy, index, err)
		}
		if seen[waiver.Platform] {
			return fmt.Errorf("%w: duplicate waiver for %s", ErrInvalidPolicy, waiver.Platform)
		}
		seen[waiver.Platform] = true
		if p.Default == Bundled {
			return fmt.Errorf("%w: waiver for %s has no effect while the default is bundled",
				ErrInvalidPolicy, waiver.Platform)
		}
	}
	return nil
}

func (w Waiver) validate() error {
	if !validPlatformComponent(w.Platform.OS) {
		return fmt.Errorf("waiver platform OS %q is not a lower-case GOOS name", w.Platform.OS)
	}
	if !validPlatformComponent(w.Platform.Arch) {
		return fmt.Errorf("waiver platform arch %q is not a lower-case GOARCH name", w.Platform.Arch)
	}
	switch w.Reason {
	case WaiverParityBlocker, WaiverPackagingBlocker, WaiverPlatformUnsupported:
	default:
		return fmt.Errorf("waiver for %s has unknown reason %q", w.Platform, w.Reason)
	}
	switch w.Gate {
	case GateMoshParity, GateETParity, GatePackaging:
	default:
		return fmt.Errorf("waiver for %s names unknown gate %q", w.Platform, w.Gate)
	}
	if strings.TrimSpace(w.Detail) == "" {
		return fmt.Errorf("waiver for %s needs a detail naming the specific blocker", w.Platform)
	}
	if w.Expires.IsZero() {
		return fmt.Errorf("waiver for %s needs an expiry", w.Platform)
	}
	return nil
}

// validPlatformComponent reports whether value is a canonical lower-case GOOS
// or GOARCH identifier. It deliberately does not whitelist known targets, so a
// new Go port needs no policy change; it only rejects empty, padded, or
// punctuated values such as " linux", "linux ", or "linux/amd64".
func validPlatformComponent(value string) bool {
	if value == "" || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
