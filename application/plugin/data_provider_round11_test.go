package plugin_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// dataTestR11Repeat returns the first n bytes of unit repeated as often as
// needed. The round-11 constructions are exact multi-megabyte texts, so they
// must be built the same way the reproductions built them: a repeated unit
// truncated at the exact byte count.
func dataTestR11Repeat(unit string, n int) string {
	var text strings.Builder
	text.Grow(n)
	for text.Len() < n {
		text.WriteString(unit)
	}
	return text.String()[:n]
}

// dataTestR11NonASCIIUnit is the exact 11-byte unit from the round-10
// verification reproduction: 61 21 c3 a9 42 7e c3 b8 (an 8-byte secret-shaped
// prefix), then the combining acute U+0301 (cc 81) and a space. The unit mixes
// ASCII with the two-byte é and ø and the combining mark, so the long-text path
// cannot use the ASCII spelling rules; its fold alternatives produce 36
// non-ASCII spellings that used to be materialized without any work charge.
func dataTestR11NonASCIIUnit() string {
	return string([]byte{0x61, 0x21, 0xc3, 0xa9, 0x42, 0x7e, 0xc3, 0xb8, 0xcc, 0x81, 0x20})
}

// B: a benign multi-megabyte plan whose text contains every byte of the secret
// but no spelling of it must be delivered. Round 10 pre-charged
// dataSecretSubsequenceASCIIWork per passing variant (16*len plus anchors), so
// the exact text below was rejected at 256 KiB and above even though zero
// contiguous, short-subsequence, or windowed matches exist. Actual-work
// charging must deliver it at 128 KiB, 256 KiB, and 4 MiB, on both the plan and
// summary paths.
func TestDataProviderDeliversBenignTextWithEverySecretByte(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	unit := "query plan node: bitmap index scan on foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	for _, size := range []int{128 << 10, 256 << 10, 4190208} {
		size := size
		t.Run(fmt.Sprintf("%d-bytes", size), func(t *testing.T) {
			text := dataTestR11Repeat(unit, size)
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
					t.Fatalf("the %d-byte benign %s was rejected: %v", len(text), call.name, err)
				}
				if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
					t.Fatalf("the %d-byte benign %s was delivered after %s, want well under 500ms",
						len(text), call.name, elapsed)
				}
				t.Logf("benign %d-byte %s delivered in %s", len(text), call.name, elapsed)
			}
		})
	}
}

// A: the exact non-ASCII construction from the round-10 verification must not
// spend an uncharged multi-second stage materializing spellings. It may either
// fail closed with the constant protocol error or be delivered, but either way
// the plan and summary calls must finish well under the pre-fix 0.78-1.07s.
// The secret is 17 bytes and absent from the text, so the scan must not find a
// match merely because the text has many non-ASCII spellings.
func TestDataProviderBoundsNonASCIISpellingMaterialization(t *testing.T) {
	unit := dataTestR11NonASCIIUnit()
	text := dataTestR11Repeat(unit, 4190208)
	secret := "qvlxrmptwzbdjfhnx"
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
			// Failing closed is allowed; it must be the constant protocol error,
			// not a leak of the candidate text.
			dataTestCheckSecretEchoError(t, err, secret)
		}
		if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
			t.Fatalf("the %d-byte non-ASCII %s finished after %s (err=%v), want well under 500ms",
				len(text), call.name, elapsed, err)
		}
		t.Logf("non-ASCII %d-byte %s finished in %s (err=%v)", len(text), call.name, elapsed, err)
	}
}

// The related Ångström! shape: a 4 MiB text that actually contains the secret
// in its non-ASCII spelling must fail closed with the constant protocol error,
// and the fail-closed decision must not wait for a seconds-long uncharged
// spelling materialization. Before round 11 this path took 0.91-1.72s.
func TestDataProviderRejectsNonASCIISpellingEchoQuickly(t *testing.T) {
	secret := "Ångström!"
	text := dataTestR11Repeat("Ångström! ", 4190208)
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
		dataTestCheckSecretEchoError(t, err, secret)
		if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
			t.Fatalf("the non-ASCII echoing %s failed closed after %s, want well under 500ms",
				call.name, elapsed)
		}
		t.Logf("non-ASCII echoing %s failed closed in %s", call.name, elapsed)
	}
}
