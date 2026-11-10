package plugin

import (
	"math/rand"
	"strings"
	"testing"
)

// dataTestR11RandomText builds a text of the requested length from alphabet.
func dataTestR11RandomText(rng *rand.Rand, alphabet string, size int) string {
	var text strings.Builder
	text.Grow(size)
	for text.Len() < size {
		text.WriteByte(alphabet[rng.Intn(len(alphabet))])
	}
	return text.String()
}

// A completed benign scan charges for every byte it examines: the decode-marker
// probe, the folded-presence pass, and the base64 orientation pass each walk
// the whole candidate. The secret bytes below do not occur in any spelling of
// these texts, so the scan must finish without a budget stop and must report at
// least three text lengths of work. This is the direct "charged >= actual"
// assertion for randomized shapes; it fails if any of those linear passes runs
// uncharged.
func TestDataSecretGuardChargesAtLeastScannedBytes(t *testing.T) {
	rng := rand.New(rand.NewSource(0x11a5c0de))
	// The alphabets have at most one base64 letter and mostly separator bytes, so
	// the base64 stage sees a handful of tokens, far below the 256-token budget.
	// No byte, case fold, or fold-table expansion of these alphabets produces any
	// letter or digit used by the secret, so it cannot match any variant.
	alphabets := []string{
		"a !:;\t",
		" !:;\t\x01",
		"a\xc3\xa9!\xc3\xb8~\xcc\x81 ",
		"\x00\x01\xffa~ ",
	}
	secret := "EQ4088842510FO36"
	for trial := 0; trial < 80; trial++ {
		alphabet := alphabets[rng.Intn(len(alphabets))]
		size := 5000 + rng.Intn(60000)
		text := dataTestR11RandomText(rng, alphabet, size)
		guard := newDataSecretGuard(map[string]string{"password": secret})
		if guard == nil {
			t.Fatal("guard is nil")
		}
		budget := newDataSecretScanBudget()
		if guard.echoesWithin(text, budget) {
			t.Fatalf("trial %d: a text over %q matched the absent secret", trial, alphabet)
		}
		if budget.exceeded {
			t.Fatalf("trial %d: a benign %d-byte text exhausted the budget (%d units)", trial, size, budget.work)
		}
		if minimum := int64(len(text)) * 3; budget.work < minimum {
			t.Fatalf("trial %d: a %d-byte scan charged only %d units, below the %d bytes its required passes examine",
				trial, len(text), budget.work, minimum)
		}
	}
}

// Every long-text pass is charged to the shared budget before it runs and must
// not overshoot by more than one charge chunk. The decode-expansion pattern
// walks the whole-text escape/percent/`\u` decoding pipeline until the budget
// latches, which is the remaining unbounded-amplification channel now that the
// long-text secret comparison is linear.
func TestDataSecretGuardWorkOvershootIsBoundedByOneChunk(t *testing.T) {
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
		t.Fatal("the pathological scan was delivered instead of failing closed")
	}
	if !budget.exceeded {
		t.Fatalf("the pathological scan was rejected without a budget stop (%d units)", budget.work)
	}
	if budget.work <= dataSecretScanWorkBudget {
		t.Fatalf("the pathological scan stopped at %d units, at or below the %d-unit budget",
			budget.work, dataSecretScanWorkBudget)
	}
	if budget.work > dataSecretScanWorkBudget+dataSecretChargeChunk {
		t.Fatalf("the pathological scan overshot the budget by %d units, more than one %d-unit chunk",
			budget.work-dataSecretScanWorkBudget, dataSecretChargeChunk)
	}
	t.Logf("pathological scan stopped at %d units, %d over the %d-unit budget",
		budget.work, budget.work-dataSecretScanWorkBudget, dataSecretScanWorkBudget)
}
