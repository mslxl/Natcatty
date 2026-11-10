package plugin

import (
	"strings"
	"testing"
)

// Defect 4a: containsSecret scans the text once per resolved value. The raw
// containment stage used to charge one scan while performing up to two (the
// threshold needle and, for a short value, the value itself). A benign text
// with three values must charge exactly three full scans.
func TestDataSecretGuardChargesRawContainmentPerValue(t *testing.T) {
	guard := newDataSecretGuard(map[string]string{
		"a": "qvlxrmptwzbdjfhn",
		"b": "qvlxrmptwzbdjfhnx",
		"c": "hunter2hunter2",
	})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	text := strings.Repeat("zz", 5000)
	budget := newDataSecretScanBudget()
	if guard.containsSecret(text, budget) {
		t.Fatal("a text without any secret byte matched raw containment")
	}
	if budget.exceeded {
		t.Fatalf("a benign raw containment pass exhausted the budget (%d units)", budget.work)
	}
	if want := int64(len(text)) * int64(len(guard.values)); budget.work != want {
		t.Fatalf("raw containment charged %d units, want one %d-byte scan per value (%d)",
			budget.work, len(text), want)
	}

	// A matching value stops the loop early, but the scan it performed is still
	// charged in full before the comparison runs.
	matching := strings.Repeat("z", 100) + guard.values[0][:dataSecretFragmentBytes] + strings.Repeat("z", 100)
	matchBudget := newDataSecretScanBudget()
	if !guard.containsSecret(matching, matchBudget) {
		t.Fatal("the raw containment pass missed a present threshold fragment")
	}
	if matchBudget.work < int64(len(matching)) {
		t.Fatalf("a matching containment pass charged %d units, below one %d-byte scan",
			matchBudget.work, len(matching))
	}
}

// Defect 4b: the decode walk probes for all three linear transform markers in
// one pass and charges that pass once, then charges each decoder pass it
// selects. A form with no markers must charge exactly the candidate pass and
// the one marker scan; a percent-bearing form selects both percent decoders
// (query and path) in addition.
func TestDataSecretGuardChargesDecodeMarkerProbes(t *testing.T) {
	plain := strings.Repeat("abc", 2000)
	plainBudget := newDataSecretScanBudget()
	if walkDataTextDecodingsWithin(plain, plainBudget, func(string) bool { return false }) {
		t.Fatal("a marker-free form reported exhaustion")
	}
	if plainBudget.exceeded {
		t.Fatalf("a marker-free form exhausted the budget (%d units)", plainBudget.work)
	}
	if want := int64(len(plain)) * 2; plainBudget.work != want {
		t.Fatalf("a marker-free form charged %d units, want the candidate pass and one %d-byte marker scan (%d)",
			plainBudget.work, len(plain), want)
	}

	percent := strings.Repeat("a%41", 2000)
	percentBudget := newDataSecretScanBudget()
	if walkDataTextDecodingsWithin(percent, percentBudget, func(string) bool { return false }) {
		t.Fatal("a percent-bearing form reported exhaustion")
	}
	if percentBudget.exceeded {
		t.Fatalf("a percent-bearing form exhausted the budget (%d units)", percentBudget.work)
	}
	// Candidate pass, marker scan, and the query and path percent decoders.
	minimum := int64(len(percent)) * 4
	if percentBudget.work < minimum {
		t.Fatalf("a percent-bearing form charged %d units, below the %d units its required passes examine",
			percentBudget.work, minimum)
	}
}

// Defect 4c: the base64 nesting-cap probe builds every orientation spelling
// through the charged walk. A cap decision over a long token-bearing candidate
// must charge the orientation scan before it reports the fail-closed limit.
func TestDataSecretGuardChargesBase64DepthCapProbe(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	tokenText := strings.Repeat("A", 1000)
	budget := newDataSecretScanBudget()
	if !guard.echoesBase64(tokenText, dataSecretBase64Depth, budget) {
		t.Fatal("a token-bearing text at the nesting cap was delivered")
	}
	if !budget.exceeded {
		t.Fatalf("the nesting cap was reported without the fail-closed latch (%d units)", budget.work)
	}
	if budget.work < int64(len(tokenText)) {
		t.Fatalf("the nesting-cap probe charged %d units, below one %d-byte orientation scan",
			budget.work, len(tokenText))
	}

	// The same probe on a text with no token-shaped run is charged for the
	// orientation copies it builds and then answers cleanly. Five letters stay
	// below the minimum decoded length even after the separator strip joins them.
	benign := "A A A A A"
	benignBudget := newDataSecretScanBudget()
	if guard.echoesBase64(benign, dataSecretBase64Depth, benignBudget) {
		t.Fatal("a text without a token-shaped run hit the nesting cap")
	}
	if benignBudget.exceeded {
		t.Fatalf("a benign nesting-cap probe exhausted the budget (%d units)", benignBudget.work)
	}
	if benignBudget.work < int64(len(benign)) {
		t.Fatalf("a benign nesting-cap probe charged %d units, below one %d-byte scan",
			benignBudget.work, len(benign))
	}
}
