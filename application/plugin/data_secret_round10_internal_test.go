package plugin

import (
	"strings"
	"testing"
)

// dataTestR10InternalPathologicalText builds the control-byte construction for
// direct guard scans. Control-byte runs keep every base64 alphabet run too short
// to decode, and 0x01 is not a separator byte, so no normalized spelling of the
// text contains the secret contiguously.
func dataTestR10InternalPathologicalText(total int) string {
	const gap = 8
	const pad = 600000
	unit := "!" + strings.Repeat("\x01", gap) + "a" + strings.Repeat("\x01", gap)
	head := total - 2*pad - 1
	var text strings.Builder
	text.Grow(total)
	for text.Len() < head {
		text.WriteString(unit)
	}
	headText := text.String()[:head]
	var out strings.Builder
	out.Grow(total)
	out.WriteString(headText)
	out.WriteString(strings.Repeat("\x01", pad))
	out.WriteString("Z")
	out.WriteString(strings.Repeat("\x01", pad))
	return out.String()
}

// The internal control-byte interleaving is the documented letter-interleaving
// residual past the short-text bound: control bytes are not separator bytes, so
// no normalized spelling carries the secret contiguously, and the linear scan
// must deliver it without exhausting the budget for a 257-byte, a 1 KiB, and
// the 64 KiB maximum secret. The public equivalent (a separator-interleaved
// text) is still rejected by separator stripping in the round-10 public test.
func TestDataSecretGuardLongControlInterleavingStaysWithinBudget(t *testing.T) {
	text := dataTestR10InternalPathologicalText(4 << 20)
	for _, size := range []int{257, 1024, 64 << 10} {
		secret := "!" + strings.Repeat("a", size-2) + "Z"
		guard := newDataSecretGuard(map[string]string{"password": secret})
		if guard == nil {
			t.Fatalf("%d-byte secret produced a nil guard", size)
		}
		budget := newDataSecretScanBudget()
		if guard.echoesWithin(text, budget) {
			t.Fatalf("the %d-byte-secret control-interleaved text was rejected; letter interleaving past %d bytes is the documented residual",
				size, dataSecretSubsequenceBytes)
		}
		if budget.exceeded {
			t.Fatalf("the %d-byte-secret benign linear scan exhausted the budget (%d units)",
				size, budget.work)
		}
	}
}

// The cheap short-text rule is unchanged: text at or below the 4096-byte bound
// still uses the full ordered-subsequence rule, catches an interleaved secret,
// and does not need the long-text window charge.
func TestDataSecretGuardShortSubsequenceRuleIsUncharged(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	scattered := dataTestScatteredDataText(secret, strings.Repeat("q", 40))
	text := strings.Repeat("q", dataSecretSubsequenceBytes-len(scattered)) + scattered
	if len(text) != dataSecretSubsequenceBytes {
		t.Fatalf("construction is %d bytes, want %d", len(text), dataSecretSubsequenceBytes)
	}
	budget := newDataSecretScanBudget()
	if !guard.echoesWithin(text, budget) {
		t.Fatal("the full subsequence inside the short-text bound was accepted")
	}
	if budget.exceeded {
		t.Fatal("the short-text full-subsequence rule was charged past the work budget")
	}
}

// The round-9 decode-expansion rejection is budget-driven, not timing-driven.
// Pin the latch directly so the public wall-clock assertion can stay generous
// without losing the regression's meaning.
func TestDataSecretGuardChargesDecodeExpansionWork(t *testing.T) {
	const pattern = "947fA0cB2dbbfdc+aa0f%1fb7%d36%%\\fA40f\\\\fC4d0f585A75%%326dF+7%f1\\uc4"
	const size = 3584 << 10
	var text strings.Builder
	text.Grow(size)
	for text.Len() < size {
		text.WriteString(pattern)
	}
	guard := newDataSecretGuard(map[string]string{"password": "qvlxrmptwzbdjfhn"})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	budget := newDataSecretScanBudget()
	if !guard.echoesWithin(text.String()[:size], budget) {
		t.Fatal("the decode-expansion pattern was delivered instead of failing closed")
	}
	if !budget.exceeded {
		t.Fatalf("the decode-expansion pattern was rejected without a budget stop (%d units)", budget.work)
	}
}

// A benign long candidate that contains every secret byte but spreads them
// beyond the anchored window must stay below the work budget; the charge is an
// upper bound on the real scan, not a blanket rejection of multi-byte secrets.
func TestDataSecretGuardLongBenignScatterStaysUnderBudget(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	padding := strings.Repeat("plan detail ", 600)
	text := padding + dataTestScatteredDataText(secret, strings.Repeat("q", 700))
	if len(text) <= dataSecretSubsequenceBytes {
		t.Fatal("the construction is not long")
	}
	budget := newDataSecretScanBudget()
	if guard.echoesWithin(text, budget) {
		t.Fatal("benign scattered text was rejected")
	}
	if budget.exceeded {
		t.Fatalf("benign scattered text exhausted the budget (%d units)", budget.work)
	}
}
