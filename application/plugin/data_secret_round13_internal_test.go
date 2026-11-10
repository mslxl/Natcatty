package plugin

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// dataTestR13Repeat returns unit repeated to exactly n bytes.
func dataTestR13Repeat(unit string, n int) string {
	var text strings.Builder
	text.Grow(n)
	for text.Len() < n {
		text.WriteString(unit)
	}
	return text.String()[:n]
}

// dataTestR13MixedCase upper-cases every other ASCII letter of unit.
func dataTestR13MixedCase(unit string) string {
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

// Defect 1 (byte escapes): the escape decoder must copy \xNN and octal byte
// escapes into the decoded candidate as raw bytes. strconv.UnquoteChar reports
// them as the rune U+00NN, and writing that rune encodes values at or above
// 0x80 as two UTF-8 bytes, so a \xfe escape never matched a raw 0xFE secret.
// A \u00NN escape keeps the rune interpretation and additionally offers the
// byte interpretation, because providers use the same spelling for escaped
// latin-1 bytes.
func TestDataSecretGuardRound13DecodesByteEscapesExactly(t *testing.T) {
	raw := "ABCDEFG\xfeHJ"
	hexPrimary, hexAlt, hexOK := decodeDataTextEscapesUnchecked(
		`\x41\x42\x43\x44\x45\x46\x47\xfe\x48\x4a`)
	if !hexOK || hexPrimary != raw || hexAlt != "" {
		t.Fatalf("hex escapes decoded to %x / %x (ok=%v), want the raw bytes %x",
			[]byte(hexPrimary), []byte(hexAlt), hexOK, []byte(raw))
	}
	octalPrimary, octalAlt, octalOK := decodeDataTextEscapesUnchecked(
		`\101\102\103\104\105\106\107\376\110\112`)
	if !octalOK || octalPrimary != raw || octalAlt != "" {
		t.Fatalf("octal escapes decoded to %x / %x (ok=%v), want the raw bytes %x",
			[]byte(octalPrimary), []byte(octalAlt), octalOK, []byte(raw))
	}
	// \x80 is a byte escape even though its value is 0x80: it must not become
	// the two-byte UTF-8 encoding c2 80.
	highPrimary, highAlt, highOK := decodeDataTextEscapesUnchecked(`a\x80b`)
	if !highOK || highPrimary != "a\x80b" || highAlt != "" {
		t.Fatalf("a \\x80 escape decoded to %x / %x", []byte(highPrimary), []byte(highAlt))
	}

	// The rune interpretation is primary; the byte interpretation is returned
	// as an additional candidate only when the rune is 0x80-0xFF.
	runePrimary, runeAlt, runeOK := decodeDataTextEscapesUnchecked(
		`ABCDEFG\u00feHJ`)
	if !runeOK || runePrimary != "ABCDEFG"+string([]byte{0xC3, 0xBE})+"HJ" {
		t.Fatalf("\\u00fe primary decoded to %x", []byte(runePrimary))
	}
	if runeAlt != raw {
		t.Fatalf("\\u00fe byte interpretation decoded to %x, want %x", []byte(runeAlt), []byte(raw))
	}
	upperPrimary, upperAlt, upperOK := decodeDataTextEscapesUnchecked(
		`ABCDEFG\U000000feHJ`)
	if !upperOK || upperPrimary != runePrimary || upperAlt != raw {
		t.Fatalf("\\U000000fe decoded to %x / %x", []byte(upperPrimary), []byte(upperAlt))
	}
	// A rune below 0x80 has no distinct byte interpretation, so no second
	// candidate is built.
	asciiPrimary, asciiAlt, asciiOK := decodeDataTextEscapesUnchecked(`a\u0041b`)
	if !asciiOK || asciiPrimary != "aAb" || asciiAlt != "" {
		t.Fatalf("\\u0041 decoded to %q / %q", asciiPrimary, asciiAlt)
	}

	// Malformed or absent escapes are not reports of a decoded form.
	if _, _, ok := decodeDataTextEscapesUnchecked(`a\qb`); ok {
		t.Fatal("a malformed escape reported a decoded form")
	}
	if _, _, ok := decodeDataTextEscapesUnchecked("A\xffB"); ok {
		t.Fatal("text without a backslash reported a decoded form")
	}
}

// dataTestR13BenignCases returns the benign 4 MiB shapes whose charged work
// must stay under the shared budget. The list mirrors the public delivery
// regression and adds the two-secret case.
func dataTestR13BenignCases() []struct {
	name    string
	unit    string
	secrets map[string]string
} {
	const secret = "qvlxrmptwzbdjfhn"
	const secondSecret = "pzqjxvkfbgwmcyhs"
	const unit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const publicUnit = "query plan node: bitmap index scan on public.orders foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const plusUnit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36+9 rows 120+4 using btree cost 8+2 "
	const dotUnit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36.5 rows 120.25 using btree cost 8.75 "
	return []struct {
		name    string
		unit    string
		secrets map[string]string
	}{
		{"lowercase", unit, map[string]string{"password": secret}},
		{"uppercase", strings.ToUpper(unit), map[string]string{"password": secret}},
		{"alternating case", dataTestR13MixedCase(unit), map[string]string{"password": secret}},
		{"public qualifier", publicUnit, map[string]string{"password": secret}},
		{"mixed-case public qualifier", dataTestR13MixedCase(publicUnit), map[string]string{"password": secret}},
		{"plus signs", plusUnit, map[string]string{"password": secret}},
		{"decimal dots", dotUnit, map[string]string{"password": secret}},
		{"two secrets", unit, map[string]string{"password": secret, "token": secondSecret}},
	}
}

// Defect 2 (availability): the heaviest benign 4 MiB variants must complete
// under the shared work budget. The plus-sign shape nearly doubles the charged
// work of the plain text because the query decoder materializes a second
// multi-megabyte form; it is the construction that crossed the old 128 MiB
// bound. Every shape here must report no match, no budget stop, and a work
// total at or below the bound.
func TestDataSecretGuardRound13BenignFourMiBWorkWithinBudget(t *testing.T) {
	const size = 4190208
	for _, tc := range dataTestR13BenignCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR13Repeat(tc.unit, size)
			guard := newDataSecretGuard(tc.secrets)
			if guard == nil {
				t.Fatal("guard is nil")
			}
			budget := newDataSecretScanBudget()
			started := time.Now()
			echo := guard.echoesWithin(text, budget)
			elapsed := time.Since(started)
			if echo {
				t.Fatalf("the benign %s construction matched or exhausted the budget after %s", tc.name, elapsed)
			}
			if budget.exceeded {
				t.Fatalf("the benign %s construction exhausted the budget (%d units)", tc.name, budget.work)
			}
			if budget.work > dataSecretScanWorkBudget {
				t.Fatalf("the benign %s construction charged %d units, over the %d-unit budget",
					tc.name, budget.work, dataSecretScanWorkBudget)
			}
			if budget.work < int64(len(text)) {
				t.Fatalf("the benign %s construction charged %d units, below one %d-byte pass",
					tc.name, budget.work, len(text))
			}
			t.Logf("benign %d-byte %s charged %d units (%.1f%% of the %d-unit budget) in %s",
				len(text), tc.name, budget.work,
				100*float64(budget.work)/float64(dataSecretScanWorkBudget),
				dataSecretScanWorkBudget, elapsed)
		})
	}
}

// The repeated-unit dedupe must skip a decoded payload already processed at the
// same or a shallower base64 depth and must rescan it when it reappears at a
// shallower depth, where the recursion allowance is wider. Canonical and
// shifted-alignment payloads are distinct modes: a completed shifted spelling
// probe cannot stand in for the recursive canonical scan. The byte cap makes
// the cache skip remembering rather than grow without bound.
func TestDataSecretGuardRound13DecodedPayloadDedupe(t *testing.T) {
	budget := newDataSecretScanBudget()
	if !budget.enterDecodedPayload("unit-payload", true, 3) {
		t.Fatal("the first shifted payload was skipped")
	}
	if budget.enterDecodedPayload("unit-payload", true, 3) {
		t.Fatal("a shifted payload at the same depth was scanned twice")
	}
	if budget.enterDecodedPayload("unit-payload", true, 9) {
		t.Fatal("a shifted payload at a deeper depth was scanned again")
	}
	if !budget.enterDecodedPayload("unit-payload", true, 1) {
		t.Fatal("a shifted payload at a shallower depth was not rescanned")
	}
	if !budget.enterDecodedPayload("unit-payload", false, 3) {
		t.Fatal("a canonical payload was suppressed by a shifted probe")
	}
	if budget.enterDecodedPayload("unit-payload", false, 3) {
		t.Fatal("a canonical payload at the same depth was scanned twice")
	}

	// A payload larger than the remaining cache budget is scanned but not
	// remembered, so the next occurrence is scanned again.
	budget.decodedSeenBytes = dataSecretDecodedSeenBytes
	if !budget.enterDecodedPayload("uncached", true, 3) {
		t.Fatal("a payload beyond the cache cap was skipped")
	}
	if !budget.enterDecodedPayload("uncached", true, 3) {
		t.Fatal("a payload beyond the cache cap was remembered")
	}
	if budget.decodedSeenBytes != dataSecretDecodedSeenBytes {
		t.Fatalf("the decoded-payload cache grew past its cap: %d", budget.decodedSeenBytes)
	}
}

// The base64 orientation dedupe has the same depth and cap semantics.
func TestDataSecretGuardRound13OrientationDedupe(t *testing.T) {
	budget := newDataSecretScanBudget()
	if !budget.enterBase64Orientation("orientation", 2) {
		t.Fatal("the first orientation was skipped")
	}
	if budget.enterBase64Orientation("orientation", 2) {
		t.Fatal("an orientation at the same depth was scanned twice")
	}
	if budget.enterBase64Orientation("orientation", 5) {
		t.Fatal("an orientation at a deeper depth was scanned twice")
	}
	if !budget.enterBase64Orientation("orientation", 0) {
		t.Fatal("an orientation at a shallower depth was not rescanned")
	}
	budget.base64SeenBytes = dataSecretBase64SeenBytes
	if !budget.enterBase64Orientation("uncached", 0) {
		t.Fatal("an orientation beyond the cache cap was skipped")
	}
	if !budget.enterBase64Orientation("uncached", 0) {
		t.Fatal("an orientation beyond the cache cap was remembered")
	}
}

// dataTestR13LiteralDecision is the reference decision without the space-free
// plus-form skip and without any dedupe: walk every decoded form, run the full
// spelling scan on each form, and run the base64 stage. The production guard
// may skip the spelling scan of a plus-decoded form only when a space-free
// value cannot use the new spaces; it must reach the same decision.
func dataTestR13LiteralDecision(text string, guard *dataSecretGuard) bool {
	matched := false
	walkDataTextDecodings(text, func(form string) bool {
		if guard.echoesSpellings(form, true, nil) {
			matched = true
			return true
		}
		if guard.echoesBase64(form, 0, nil) {
			matched = true
			return true
		}
		return false
	})
	return matched
}

// The plus-form spelling skip must not change any decision. A plus-decoded form
// differs from its parent only at "+" bytes replaced by spaces, so it can add a
// match only when a compared value contains a space or the form's non-space
// bytes differ; the skip is applied only when every compared value is
// space-free, and the parent spelling scan then covers all remaining rules.
// Randomized texts embed plus-transformed secrets, with and without spaces, at
// short lengths. Long-text plus handling is covered deterministically by the
// round-14 retention matrix, because the linear long-text base64 channel is
// intentionally not byte-identical to the short per-form base64 walk (bounded
// alignment of runs over the decoded-scan bound).
func TestDataSecretGuardRound13SpaceFreePlusSkipIsEquivalent(t *testing.T) {
	rng := rand.New(rand.NewSource(0x13eec0de))
	alphabets := []string{"ab+ ", "a+b", "aA+ ."}
	lengths := []int{0, 1, 2, 5, 17, 64, 300, 4000}
	for trial := 0; trial < 400; trial++ {
		alphabet := alphabets[rng.Intn(len(alphabets))]
		secret := dataTestR13RandomText(rng, alphabet, 2+rng.Intn(8))
		text := dataTestR13RandomText(rng, alphabet, lengths[rng.Intn(len(lengths))])
		switch trial % 3 {
		case 0:
			text += strings.ReplaceAll(secret, " ", "+")
		case 1:
			text = dataTestR13RandomText(rng, alphabet, 4000) +
				strings.ReplaceAll(secret, " ", "+")
		}
		guard := newDataSecretGuard(map[string]string{"password": secret})
		if guard == nil {
			continue
		}
		literal := dataTestR13LiteralDecision(text, guard)
		budget := newDataSecretScanBudget()
		fast := guard.echoesWithin(text, budget)
		if budget.exceeded {
			t.Fatalf("trial %d: the differential decision exhausted the budget", trial)
		}
		if literal != fast {
			t.Fatalf("trial %d: plus-form skip changed the decision for text %q and secret %q (literal=%v fast=%v)",
				trial, text, secret, literal, fast)
		}
	}
}

// The full decision must be unchanged for binary and escaped candidates. The
// literal reference walks every decoded form (including the byte interpretation
// of escapes), applies every spelling to every form, and gives each base64
// stage a fresh budget. The production scan shares one budget, skips the
// redundant plus spelling, and suppresses duplicate orientations and payloads;
// all of those must leave the decision identical, including for invalid UTF-8.
func TestDataSecretGuardRound13ByteEscapeAndBinaryDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(0x13b17e5))
	escapes := []string{
		`\x41`, `\xfe`, `\101`, `\376`, `\u00fe`, `\U000000fe`,
		`\\x41`, `%41`, `%fe`, `%2541`, `\/`,
	}
	alphabets := []string{
		"aA\xfe\xff01+ ",
		"ab%\\ ",
		"aA\x00\xff+. ",
	}
	for trial := 0; trial < 300; trial++ {
		alphabet := alphabets[rng.Intn(len(alphabets))]
		secret := dataTestR13RandomText(rng, alphabet, 1+rng.Intn(6))
		text := dataTestR13RandomText(rng, alphabet, rng.Intn(40)) +
			escapes[rng.Intn(len(escapes))] +
			dataTestR13RandomText(rng, alphabet, rng.Intn(40))
		guard := newDataSecretGuard(map[string]string{"password": secret})
		if guard == nil {
			continue
		}
		literal := dataTestR13LiteralDecision(text, guard)
		budget := newDataSecretScanBudget()
		fast := guard.echoesWithin(text, budget)
		if budget.exceeded {
			t.Fatalf("trial %d: the binary differential exhausted the budget", trial)
		}
		if literal != fast {
			t.Fatalf("trial %d: optimized scan changed the decision for text %q (hex %x) and secret %q (hex %x): literal=%v fast=%v",
				trial, text, []byte(text), secret, []byte(secret), literal, fast)
		}
	}
}

// dataTestR13RandomText builds a text of size bytes from alphabet.
func dataTestR13RandomText(rng *rand.Rand, alphabet string, size int) string {
	var text strings.Builder
	text.Grow(size)
	for text.Len() < size {
		text.WriteByte(alphabet[rng.Intn(len(alphabet))])
	}
	return text.String()
}
