package plugin

import (
	"strconv"
	"strings"
)

// HostPluginAPIVersion is the Plugin API version this host implements. A
// manifest's plugin_api range must contain it.
const HostPluginAPIVersion = "0.1.0"

// Semver is the subset of SemVer 2.0.0 the plugin system needs for manifest
// and Plugin API negotiation. Prerelease precedence follows SemVer 2.0.0;
// build metadata is accepted and ignored.
type Semver struct {
	Major int
	Minor int
	Patch int
	Pre   string
}

// ParseSemver parses a strict "major.minor.patch" version with an optional
// prerelease suffix. Build metadata is accepted and ignored.
func ParseSemver(raw string) (Semver, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return Semver{}, false
	}
	if i := strings.IndexByte(value, '+'); i >= 0 {
		value = value[:i]
	}
	pre := ""
	if i := strings.IndexByte(value, '-'); i >= 0 {
		pre = value[i+1:]
		value = value[:i]
		if pre == "" {
			return Semver{}, false
		}
		for _, part := range strings.Split(pre, ".") {
			if part == "" {
				return Semver{}, false
			}
			numeric := true
			for _, r := range part {
				if !isSemverIdentRune(r) {
					return Semver{}, false
				}
				if r < '0' || r > '9' {
					numeric = false
				}
			}
			if numeric && len(part) > 1 && part[0] == '0' {
				return Semver{}, false
			}
		}
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return Semver{}, false
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Semver{}, false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return Semver{}, false
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return Semver{}, false
		}
		numbers[i] = number
	}
	return Semver{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Pre: pre}, true
}

func isSemverIdentRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-'
}

// Compare orders two versions. A prerelease sorts before its release.
func (v Semver) Compare(other Semver) int {
	if c := compareInt(v.Major, other.Major); c != 0 {
		return c
	}
	if c := compareInt(v.Minor, other.Minor); c != 0 {
		return c
	}
	if c := compareInt(v.Patch, other.Patch); c != 0 {
		return c
	}
	return comparePrerelease(v.Pre, other.Pre)
}

// comparePrerelease implements SemVer 2.0.0 prerelease precedence. An empty
// prerelease (a release) sorts above any prerelease; dot-separated
// identifiers compare left to right, numeric identifiers compare numerically
// and sort below alphanumeric ones, and a shorter identifier list sorts
// first when every shared identifier is equal.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < len(aParts) && i < len(bParts); i++ {
		if c := comparePrereleaseIdentifier(aParts[i], bParts[i]); c != 0 {
			return c
		}
	}
	return compareInt(len(aParts), len(bParts))
}

func comparePrereleaseIdentifier(a, b string) int {
	aNumber, aNumeric := prereleaseNumber(a)
	bNumber, bNumeric := prereleaseNumber(b)
	switch {
	case aNumeric && bNumeric:
		return compareInt(aNumber, bNumber)
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func prereleaseNumber(identifier string) (int, bool) {
	number, err := strconv.Atoi(identifier)
	if err != nil {
		return 0, false
	}
	return number, true
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// PluginAPIMatches reports whether the manifest's Plugin API range contains
// the host's Plugin API version. Supported constraints are caret (^), tilde
// (~), exact versions, "*", and comma-separated comparison clauses
// (>=x.y.z, <x.y.z). Full npm range grammar is deliberately out of scope for
// the first walking skeleton.
func PluginAPIMatches(declared string) bool {
	host, ok := ParseSemver(HostPluginAPIVersion)
	if !ok {
		return false
	}
	return rangeContains(declared, host)
}

// ValidRange reports whether raw is a supported semver range expression: a
// comma-separated list of caret, tilde, comparison, exact, or "*" clauses.
func ValidRange(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	for _, clause := range strings.Split(raw, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			return false
		}
		if clause == "*" {
			continue
		}
		switch {
		case strings.HasPrefix(clause, "^"), strings.HasPrefix(clause, "~"):
			if _, ok := parseRangeBase(clause[1:]); !ok {
				return false
			}
		case strings.HasPrefix(clause, ">="), strings.HasPrefix(clause, "<="):
			if _, ok := parseRangeBase(clause[2:]); !ok {
				return false
			}
		case strings.HasPrefix(clause, ">"), strings.HasPrefix(clause, "<"):
			if _, ok := parseRangeBase(clause[1:]); !ok {
				return false
			}
		default:
			if _, ok := parseRangeBase(clause); !ok {
				return false
			}
		}
	}
	return true
}

// RangeContains reports whether a version satisfies a supported range
// expression. It is the package-dependency counterpart of PluginAPIMatches.
func RangeContains(raw string, version Semver) bool {
	return rangeContains(raw, version)
}

func rangeContains(raw string, host Semver) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	for _, clause := range strings.Split(raw, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" || !clauseContains(clause, host) {
			return false
		}
	}
	return true
}

func clauseContains(clause string, host Semver) bool {
	switch {
	case clause == "*":
		return true
	case strings.HasPrefix(clause, "^"):
		base, ok := parseRangeBase(clause[1:])
		if !ok {
			return false
		}
		return host.Compare(base) >= 0 && host.Compare(caretUpper(base)) < 0
	case strings.HasPrefix(clause, "~"):
		base, ok := parseRangeBase(clause[1:])
		if !ok {
			return false
		}
		return host.Compare(base) >= 0 && host.Compare(tildeUpper(base)) < 0
	case strings.HasPrefix(clause, ">="):
		base, ok := parseRangeBase(clause[2:])
		return ok && host.Compare(base) >= 0
	case strings.HasPrefix(clause, "<="):
		base, ok := parseRangeBase(clause[2:])
		return ok && host.Compare(base) <= 0
	case strings.HasPrefix(clause, ">"):
		base, ok := parseRangeBase(clause[1:])
		return ok && host.Compare(base) > 0
	case strings.HasPrefix(clause, "<"):
		base, ok := parseRangeBase(clause[1:])
		return ok && host.Compare(base) < 0
	default:
		base, ok := parseRangeBase(clause)
		return ok && host.Compare(base) == 0
	}
}

// parseRangeBase accepts a full version or a partial "major" / "major.minor"
// form, which range operators commonly allow.
func parseRangeBase(raw string) (Semver, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Semver{}, false
	}
	parts := strings.Split(raw, ".")
	if len(parts) > 3 {
		return Semver{}, false
	}
	for len(parts) < 3 {
		parts = append(parts, "0")
	}
	return ParseSemver(strings.Join(parts, "."))
}

func caretUpper(base Semver) Semver {
	if base.Major > 0 {
		return Semver{Major: base.Major + 1}
	}
	if base.Minor > 0 {
		return Semver{Major: 0, Minor: base.Minor + 1}
	}
	return Semver{Major: 0, Minor: 0, Patch: base.Patch + 1}
}

func tildeUpper(base Semver) Semver {
	if base.Major > 0 || base.Minor > 0 {
		return Semver{Major: base.Major, Minor: base.Minor + 1}
	}
	return Semver{Major: 0, Minor: 1}
}
