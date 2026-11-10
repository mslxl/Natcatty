package plugin_test

import (
	"strings"
	"testing"
	"time"
)

// dataTestR12FragmentBytes mirrors the guard's threshold-fragment size; the
// guard's own constant is unexported, so the public tests state it here.
const dataTestR12FragmentBytes = 8

// dataTestR12Around pads middle with 5000 "q" bytes on both sides, the exact
// long-text shape used by the round-12 reproductions. Every construction is
// longer than the 4096-byte short-text bound, so the long-text rules apply.
func dataTestR12Around(middle string) string {
	return strings.Repeat("q", 5000) + middle + strings.Repeat("q", 5000)
}

// Defect 1: the contiguous threshold-fragment rule on long text was gated on
// the full variant's byte counts and presence, so a diagnostic that contained
// only the first eight secret bytes was delivered. The rule is part of the
// guard's contract: an 8-byte fragment of a longer secret must not be echoed.
// Forward and byte-reversed fragments are both checked, and the fragment does
// not have to contain any byte of the variant's tail.
func TestDataProviderRejectsThresholdFragmentOnLongDiagnostics(t *testing.T) {
	presenceSecret := "qvlxrmptwzbdjfhn`"
	if len(presenceSecret) < 2*dataTestR12FragmentBytes {
		t.Fatalf("presence-hole secret is only %d bytes", len(presenceSecret))
	}
	for _, tc := range []struct {
		name   string
		secret string
		middle string
	}{
		{"forward fragment", "abcdabcdabcd", "abcdabcd"},
		{"reversed fragment", "abcdabcdabcd", "dcbadcba"},
		{"fragment present, tail absent", presenceSecret, presenceSecret[:dataTestR12FragmentBytes]},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR12Around(tc.middle)
			env, lease := dataTestR9Env(t, tc.secret)
			dataTestCheckSecretEchoError(t, dataTestR9Plan(env, lease, text), tc.secret)
			dataTestCheckSecretEchoError(t, dataTestR9Summary(env, lease, text), tc.secret)
		})
	}
}

// Defect 2: the long ASCII path skipped the whole-string upper-case spelling
// family, and base64 encodings of a binary secret are not case-folded
// spellings of that secret. Three NUL bytes encode to "AAAA" (six to
// "AAAAAAAA"); a diagnostic that lower-cases the echo to "aaaa" was therefore
// delivered. The upper-case family compensation must keep the encoded form
// detectable.
func TestDataProviderRejectsLowercaseBase64EchoOfBinarySecret(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secret string
		echo   string
	}{
		{"three NUL bytes", string([]byte{0, 0, 0}), "aaaa"},
		{"six NUL bytes", string([]byte{0, 0, 0, 0, 0, 0}), "aaaaaaaa"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR12Around(tc.echo)
			env, lease := dataTestR9Env(t, tc.secret)
			dataTestCheckSecretEchoError(t, dataTestR9Plan(env, lease, text), tc.secret)
			dataTestCheckSecretEchoError(t, dataTestR9Summary(env, lease, text), tc.secret)
		})
	}
}

// dataTestR12MixedCaseUnit returns unit with every other ASCII letter
// upper-cased, the exact mixed-case shape from the round-12 verification.
func dataTestR12MixedCaseUnit(unit string) string {
	var mixed strings.Builder
	mixed.Grow(len(unit))
	for i := 0; i < len(unit); i++ {
		c := unit[i]
		if i%2 == 0 && c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		mixed.WriteByte(c)
	}
	return mixed.String()
}

// Defect 3: a benign 4 MiB diagnostic containing every secret byte but no
// spelling of the secret must be delivered. The mixed-case form was rejected
// after the prefix and case-equivalence fixes made the scan literal again; the
// work model must charge the scans it actually performs, not duplicate
// equivalent spellings, so the mixed-case text keeps the same headroom as the
// lower-case one. Both plan and summary paths are checked.
func TestDataProviderDeliversBenignMixedCaseFourMiBDiagnostics(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	const unit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const size = 4190208
	for _, tc := range []struct {
		name string
		unit string
	}{
		{"lowercase", unit},
		{"mixed-case", dataTestR12MixedCaseUnit(unit)},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR11Repeat(tc.unit, size)
			env, lease := dataTestR9Env(t, secret)
			for _, call := range []struct {
				name string
				run  func() error
			}{
				{"plan", func() error { return dataTestR9Plan(env, lease, text) }},
				{"summary", func() error { return dataTestR9Summary(env, lease, text) }},
			} {
				started := time.Now()
				err := call.run()
				elapsed := time.Since(started)
				if err != nil {
					t.Fatalf("the %d-byte benign %s %s was rejected: %v",
						len(text), tc.name, call.name, err)
				}
				if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
					t.Fatalf("the %d-byte benign %s %s was delivered after %s, want well under 500ms",
						len(text), tc.name, call.name, elapsed)
				}
				t.Logf("benign %d-byte %s %s delivered in %s", len(text), tc.name, call.name, elapsed)
			}
		})
	}
}

// The ordered-subsequence rule switches at the 4096-byte bound. At the short
// bound a secret interleaved with single filler bytes is a full subsequence and
// is rejected. One byte past the bound the linear long-text rule applies:
// arbitrary letter interleaving is the documented residual (it is not a
// contiguous, encoded, escaped, separator-interleaved, or reversed spelling),
// while separator interleaving is still caught by separator stripping. The
// contiguous threshold-fragment rule applies on both sides, so a text holding
// only the first eight secret bytes is rejected at 4096 bytes as well as 4097.
func TestDataProviderSecretRulesAcrossTheLongTextBoundary(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)
	interleaved := dataTestInterleave(secret, "q")
	separated := dataTestInterleave(secret, " ")
	fragment := secret[:dataTestR12FragmentBytes]
	cases := []struct {
		size    int
		name    string
		middle  string
		wantErr bool
	}{
		{4096, "interleaved secret at the short bound", interleaved, true},
		{4097, "interleaved secret past the short bound", interleaved, false},
		{4097, "separator-interleaved secret past the short bound", separated, true},
		{4096, "threshold fragment at the short bound", fragment, true},
		{4097, "threshold fragment past the short bound", fragment, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.middle) > tc.size {
				t.Fatalf("the %s construction is %d bytes, longer than the %d-byte text",
					tc.name, len(tc.middle), tc.size)
			}
			text := strings.Repeat("q", tc.size-len(tc.middle)) + tc.middle
			if len(text) != tc.size {
				t.Fatalf("construction is %d bytes, want %d", len(text), tc.size)
			}
			for _, call := range []struct {
				name string
				run  func() error
			}{
				{"plan", func() error { return dataTestR9Plan(env, lease, text) }},
				{"summary", func() error { return dataTestR9Summary(env, lease, text) }},
			} {
				err := call.run()
				if tc.wantErr {
					dataTestCheckSecretEchoError(t, err, secret)
					continue
				}
				if err != nil {
					t.Fatalf("the %s %s was rejected: %v", tc.name, call.name, err)
				}
			}
		})
	}
}
