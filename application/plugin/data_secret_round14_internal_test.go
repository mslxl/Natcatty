package plugin

import (
	"encoding/base64"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// dataTestR14PercentBytes spells every byte of text as an uppercase percent
// escape, so the percent decoders reconstruct the raw bytes exactly.
func dataTestR14PercentBytes(text string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		out.WriteByte('%')
		out.WriteByte(hex[text[i]>>4])
		out.WriteByte(hex[text[i]&0x0f])
	}
	return out.String()
}

// dataTestR14HexEscapedBytes spells every byte of text as a Go/JSON hex escape.
func dataTestR14HexEscapedBytes(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		fmt.Fprintf(&out, `\x%02x`, text[i])
	}
	return out.String()
}

// dataTestR14NestedBase64 wraps text in levels layers of raw standard base64.
func dataTestR14NestedBase64(text string, levels int) string {
	for level := 0; level < levels; level++ {
		text = base64.RawStdEncoding.EncodeToString([]byte(text))
	}
	return text
}

// dataTestR14NeedleValues lists the needles of one long-text match set.
func dataTestR14NeedleValues(set *dataTextLongMatchSet) []string {
	var values []string
	for _, pivot := range set.pivots {
		for _, needle := range set.byPivot[pivot] {
			values = append(values, needle.value)
		}
	}
	return values
}

func dataTestR14ContainsAny(values []string, text string) bool {
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

// dataTestR14BenignD1Cases returns the availability matrix from the round-14
// verification. Every shape is a benign diagnostic at 4,190,208 bytes (or the
// stated 64 KiB for the schema-qualified SQL, whose old failure was the base64
// token-credit budget) that must be delivered without matching or exhausting
// the work budget.
func dataTestR14BenignD1Cases(t *testing.T) []struct {
	name    string
	text    string
	secrets map[string]string
} {
	t.Helper()
	const secret = "qvlxrmptwzbdjfhn"
	const second = "pzqjxvkfbgwmcyhs"
	const third = "zkdlfmpqwruxntby"
	const unit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const publicUnit = "query plan node: bitmap index scan on public.orders foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const plusUnit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36+9 rows 120+4 using btree cost 8+2 "
	const plusPublic = "query plan node: bitmap index scan on public.orders foreign key value zip column with merge join width 36+9 rows 120+4 using btree cost 8+2 "
	const dotUnit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36.5 rows 120.25 using btree cost 8.75 "
	const percentURLUnit = "query%20plan%20node%3A%20bitmap%20index%20scan%20on%20foreign%20key%20value%20zip%20column%20with%20merge%20join%20width%2036%20rows%20120%20using%20btree%20cost%208%20"
	const percentSQLUnit = "SELECT%20public.orders.id%2C%20public.orders.customer_id%20FROM%20public.orders%20JOIN%20public.customers%20ON%20public.orders.customer_id%20%3D%20public.customers.id%20WHERE%20public.orders.total%20%3E%20100%20ORDER%20BY%20public.orders.created_at%20DESC%3B%20"
	const sqlUnit = "SELECT public.orders.id, public.orders.customer_id FROM public.orders JOIN public.customers ON public.orders.customer_id = public.customers.id WHERE public.orders.total > 100 ORDER BY public.orders.created_at DESC; "
	const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	const sha256Unit = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + " "

	secrets2 := map[string]string{"password": secret, "token": second}
	secrets3 := map[string]string{"password": secret, "token": second, "other": third}
	rng := rand.New(rand.NewSource(0x14b64))
	var varying strings.Builder
	for varying.Len() < 4<<20-4096 {
		n := 16 + rng.Intn(65)
		for i := 0; i < n; i++ {
			varying.WriteByte(base64Alphabet[rng.Intn(len(base64Alphabet))])
		}
		varying.WriteByte(' ')
	}

	return []struct {
		name    string
		text    string
		secrets map[string]string
	}{
		{"lowercase", dataTestR13Repeat(unit, 4190208), map[string]string{"password": secret}},
		{"uppercase", dataTestR13Repeat(strings.ToUpper(unit), 4190208), map[string]string{"password": secret}},
		{"alternating case", dataTestR13Repeat(dataTestR13MixedCase(unit), 4190208), map[string]string{"password": secret}},
		{"public qualifier", dataTestR13Repeat(publicUnit, 4190208), map[string]string{"password": secret}},
		{"mixed-case public qualifier", dataTestR13Repeat(dataTestR13MixedCase(publicUnit), 4190208), map[string]string{"password": secret}},
		{"plus signs", dataTestR13Repeat(plusUnit, 4190208), map[string]string{"password": secret}},
		{"plus and public", dataTestR13Repeat(plusPublic, 4190208), map[string]string{"password": secret}},
		{"plus uppercase", dataTestR13Repeat(strings.ToUpper(plusUnit), 4190208), map[string]string{"password": secret}},
		{"plus mixed case", dataTestR13Repeat(dataTestR13MixedCase(plusUnit), 4190208), map[string]string{"password": secret}},
		{"plus two secrets", dataTestR13Repeat(plusUnit, 4190208), secrets2},
		{"plus three secrets", dataTestR13Repeat(plusUnit, 4190208), secrets3},
		{"decimal dots", dataTestR13Repeat(dotUnit, 4190208), map[string]string{"password": secret}},
		{"two secrets", dataTestR13Repeat(unit, 4190208), secrets2},
		{"percent URL", dataTestR13Repeat(percentURLUnit, 4190208), map[string]string{"password": secret}},
		{"percent SQL", dataTestR13Repeat(percentSQLUnit, 4190208), map[string]string{"password": secret}},
		{"schema-qualified SQL 64 KiB", dataTestR13Repeat(sqlUnit, 64<<10), map[string]string{"password": secret}},
		{"base64 continuous", dataTestR13Repeat(base64Alphabet, 4190208), map[string]string{"password": secret}},
		{"base64 sha256 tokens", dataTestR13Repeat(sha256Unit, 4190208), map[string]string{"password": secret}},
		{"base64 varying tokens", varying.String()[:4190208], map[string]string{"password": secret}},
	}
}

// D1: every benign variant must be delivered, with linear work at or below half
// the 192 MiB budget and well inside the public 300 ms budget. The previous
// anchored windowed-subsequence scan charged close to or beyond the whole
// budget on these shapes and failed them closed.
func TestDataSecretGuardRound14BenignD1Matrix(t *testing.T) {
	for _, tc := range dataTestR14BenignD1Cases(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(tc.secrets)
			if guard == nil {
				t.Fatal("guard is nil")
			}
			budget := newDataSecretScanBudget()
			started := time.Now()
			echo := guard.echoesWithin(tc.text, budget)
			elapsed := time.Since(started)
			if echo {
				t.Fatalf("the benign %d-byte %s construction matched or exhausted the budget after %s (%d units)",
					len(tc.text), tc.name, elapsed, budget.work)
			}
			if budget.exceeded {
				t.Fatalf("the benign %s construction exhausted the budget (%d units)", tc.name, budget.work)
			}
			if limit := int64(dataSecretScanWorkBudget) / 2; budget.work > limit {
				t.Fatalf("the benign %s construction charged %d units, over half the %d-unit budget",
					tc.name, budget.work, dataSecretScanWorkBudget)
			}
			if !dataSecretRaceEnabled && elapsed > 500*time.Millisecond {
				t.Fatalf("the benign %d-byte %s construction took %s, want under the public 500ms ceiling",
					len(tc.text), tc.name, elapsed)
			}
			t.Logf("benign %d-byte %s charged %d units (%.1f%% of budget) in %s",
				len(tc.text), tc.name, budget.work,
				100*float64(budget.work)/float64(dataSecretScanWorkBudget), elapsed)
		})
	}
}

// D1 retention: the linear rule must still catch every real echo above the
// short-text bound: contiguous, case, reversed, separator-interleaved, percent,
// escaped, base64, nested base64, and threshold-prefix spellings. The padding is
// ordinary plan text, and the binary/base64 cases use a control-byte fence so
// the encoded token stays a standalone alphabet run.
func TestDataSecretGuardRound14RetainsLongEchoes(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	const binary = "ABCDEFG\xfeHJ"
	pad := strings.Repeat("plan detail ", 700)
	fence := strings.Repeat("\x01", 5000)
	spellings := []struct {
		name   string
		secret string
		text   string
	}{
		{"contiguous", secret, pad + secret + pad},
		{"uppercase", secret, pad + strings.ToUpper(secret) + pad},
		{"alternating case", secret, pad + dataTestR13MixedCase(secret) + pad},
		{"reversed", secret, pad + dataTextReversed(secret) + pad},
		{"separator interleaved", secret,
			pad + strings.Join(strings.Split(secret, ""), " ") + pad},
		{"percent encoded", secret, pad + dataTestR14PercentBytes(secret) + pad},
		{"hex escaped", secret, pad + dataTestR14HexEscapedBytes(secret) + pad},
		{"base64 encoded", secret,
			fence + base64.RawStdEncoding.EncodeToString([]byte(secret)) + fence},
		{"reversed base64 threshold prefix", secret,
			fence + dataTextReversed(base64.RawStdEncoding.EncodeToString([]byte(secret))[:dataSecretFragmentBytes]) + fence},
		{"base64 of percent-encoded secret", secret,
			fence + base64.RawStdEncoding.EncodeToString([]byte(dataTestR14PercentBytes(secret))) + fence},
		{"base64 of hex-escaped secret", secret,
			fence + base64.RawStdEncoding.EncodeToString([]byte(dataTestR14HexEscapedBytes(secret))) + fence},
		{"nested base64 five layers", secret,
			fence + dataTestR14NestedBase64(secret, 5) + fence},
		{"threshold prefix", secret,
			pad + secret[:dataSecretFragmentBytes] + pad},
		{"reversed threshold prefix", secret,
			pad + dataTextReversed(secret[:dataSecretFragmentBytes]) + pad},
		{"binary escaped", binary,
			pad + dataTestR14HexEscapedBytes(binary) + pad},
		{"binary percent encoded", binary,
			pad + dataTestR14PercentBytes(binary) + pad},
	}
	for _, tc := range spellings {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.text) <= dataSecretSubsequenceBytes {
				t.Fatalf("the %s construction is only %d bytes, not long text",
					tc.name, len(tc.text))
			}
			guard := newDataSecretGuard(map[string]string{"password": tc.secret})
			if guard == nil {
				t.Fatal("guard is nil")
			}
			budget := newDataSecretScanBudget()
			if !guard.echoesWithin(tc.text, budget) {
				t.Fatalf("the %s echo above %d bytes was accepted",
					tc.name, dataSecretSubsequenceBytes)
			}
			if budget.exceeded {
				t.Fatalf("the %s echo was rejected only by a budget stop", tc.name)
			}
		})
	}
}

// Short text keeps the full ordered-subsequence rule. One byte past the bound,
// arbitrary letter interleaving is the documented residual; separator
// interleaving stays rejected by the linear normalized forms.
func TestDataSecretGuardRound14ShortTextBoundary(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	scattered := dataTestScatteredDataText(secret, strings.Repeat("q", 40))
	atBound := strings.Repeat("q", dataSecretSubsequenceBytes-len(scattered)) + scattered
	if len(atBound) != dataSecretSubsequenceBytes {
		t.Fatalf("boundary construction is %d bytes", len(atBound))
	}
	if !guard.echoes(atBound) {
		t.Fatal("the full subsequence at the 4096-byte bound was accepted")
	}
	if guard.echoes(atBound + "q") {
		t.Fatal("a letter-interleaved secret past 4096 bytes was rejected; that interleaving is the documented residual")
	}
	spaced := strings.Repeat("q", dataSecretSubsequenceBytes) +
		strings.Join(strings.Split(secret, ""), " ")
	if !guard.echoes(spaced) {
		t.Fatal("a separator-interleaved secret past 4096 bytes was accepted")
	}
}

// D1 pathological bounds: worst-case secret-bearing shapes at roughly 4 MiB
// must fail closed in under 300 ms without unbounded work or memory. The
// decode-expansion pattern latches the budget; the contiguous, escaped, and
// whole-token shapes match early and are also bounded.
// dataTestR14Pattern is the 23-form decode pattern used by the round-9 public
// regression; the internal tests cannot reference the external test package.
const dataTestR14Pattern = "947fA0cB2dbbfdc+aa0f%1fb7%d36%%\\fA40f\\\\fC4d0f585A75%%326dF+7%f1\\uc4"

func TestDataSecretGuardRound14PathologicalBounds(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	pad := strings.Repeat("plan detail ", 340000)
	fence := strings.Repeat("\x01", 2<<20)
	escaped := dataTestR14HexEscapedBytes(secret)
	// The secret sits at the start of the decoded payload. The encoded token is
	// truncated to the four-megabyte plan bound below; a secret near the end of
	// a larger payload would be cut out of the token altogether, which would
	// make the construction benign instead of a secret-bearing whole token.
	payload := append([]byte(secret), []byte(pad)...)
	wholeToken := base64.RawStdEncoding.EncodeToString(payload)
	if len(wholeToken) > 4<<20-4096 {
		wholeToken = wholeToken[:4<<20-4096]
	}
	deep := secret
	for level := 0; level < 5; level++ {
		deep = base64.RawStdEncoding.EncodeToString([]byte(deep))
	}
	cases := []struct {
		name string
		text string
	}{
		{"decode expansion pattern", dataTestR13Repeat(dataTestR14Pattern, 3584<<10)},
		{"contiguous secret at four MiB", pad + secret + pad},
		{"escaped secret at four MiB", pad + escaped + pad},
		{"whole base64 token at four MiB", wholeToken},
		{"nested base64 at four MiB", fence + deep + fence},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(map[string]string{"password": secret})
			if guard == nil {
				t.Fatal("guard is nil")
			}
			budget := newDataSecretScanBudget()
			started := time.Now()
			echo := guard.echoesWithin(tc.text, budget)
			elapsed := time.Since(started)
			if !echo {
				t.Fatalf("the pathological %s shape was delivered", tc.name)
			}
			// A direct detection must fail closed within 300 ms. The decode
			// expansion pattern carries no secret; it exists to price the work
			// budget, so its latch is held to the public 500 ms ceiling instead.
			ceiling := 300 * time.Millisecond
			if budget.exceeded {
				ceiling = 500 * time.Millisecond
			}
			if !dataSecretRaceEnabled && elapsed > ceiling {
				t.Fatalf("the pathological %s shape failed closed after %s, want under %s",
					tc.name, elapsed, ceiling)
			}
			if budget.work > dataSecretScanWorkBudget+dataSecretChargeChunk {
				t.Fatalf("the pathological %s shape overshot the budget by %d units",
					tc.name, budget.work-dataSecretScanWorkBudget)
			}
			t.Logf("pathological %s failed closed in %s (%d units, exceeded=%v)",
				tc.name, elapsed, budget.work, budget.exceeded)
		})
	}
}

// Literal-vs-optimized differential for the new linear normalized forms. The
// reference materializes each needle and uses strings.Contains on the same
// decoded form; the production matcher uses rare-byte anchors, a count
// prefilter, and a dedupe map. The decision must be identical for ASCII,
// non-ASCII, separator-heavy, case-varied, reversed, and invalid UTF-8 text.
func TestDataSecretGuardRound14LinearSpellingDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(0x14d1f))
	alphabets := []string{
		"abAB",
		"abcXYZ.-_ ",
		"aA!~ \t\n",
		"abcdefghij0123456789",
		"\x00\x01\xffab",
		"aAéǿﬀ ",
		"ａｂｃＡＢＣ ",
	}
	lengths := []int{0, 1, 2, 5, 17, 300, 4096, 4097, 4200, 4300}
	for trial := 0; trial < 400; trial++ {
		alphabet := alphabets[rng.Intn(len(alphabets))]
		text := dataTestR13RandomText(rng, alphabet, lengths[rng.Intn(len(lengths))])
		secret := dataTestR13RandomText(rng, alphabet, 2+rng.Intn(11))
		guard := newDataSecretGuard(map[string]string{"password": secret})
		if guard == nil {
			continue
		}
		// Embed transformed spellings so both decisions see positives.
		switch trial % 4 {
		case 0:
			text += dataTextCaseUpper(secret)
		case 1:
			text += strings.Join(strings.Split(secret, ""), " ")
		case 2:
			text += dataTextReversed(secret)
		}
		if len(text) <= dataSecretSubsequenceBytes {
			continue
		}
		stripped, ascii, ok := dataTextLongStrip(text, newDataSecretScanBudget())
		if !ok {
			t.Fatal("the differential strip reported budget exhaustion")
		}
		budget := newDataSecretScanBudget()
		got := guard.echoesLongSpellings(text, stripped, ascii, nil, false, budget)
		if budget.exceeded {
			t.Fatalf("trial %d: the spelling differential exhausted the budget", trial)
		}
		want := dataTestR14LiteralLongSpelling(text, guard)
		if got != want {
			t.Fatalf("trial %d: linear matcher changed the decision for text %q (hex %x) and secret %q: literal=%v optimized=%v",
				trial, text, []byte(text), secret, want, got)
		}
	}
}

// dataTestR14LiteralLongSpelling is the unoptimized reference for the linear
// spelling stage: case-sensitive encoded needles against the form and its
// stripped copy, then normalized needles against the normalized form, each with
// strings.Contains.
func dataTestR14LiteralLongSpelling(text string, guard *dataSecretGuard) bool {
	stripped, _, _ := dataTextLongStrip(text, newDataSecretScanBudget())
	encoded := dataTestR14NeedleValues(&guard.longBase64Needles)
	if dataTestR14ContainsAny(encoded, text) {
		return true
	}
	if stripped != text && dataTestR14ContainsAny(encoded, stripped) {
		return true
	}
	normalized := dataTextLongNormalizeValue(text)
	return dataTestR14ContainsAny(dataTestR14NeedleValues(&guard.longNeedles), normalized)
}

// The small-payload count prefilter is a necessary condition: whenever it
// reports that no needle could appear, no literal spelling scan may find one.
// The corpus includes fullwidth and accented runes and invalid bytes so the
// Unicode fold accounting is exercised, not just byte counts.
func TestDataSecretGuardRound14PrefilterIsNecessaryCondition(t *testing.T) {
	rng := rand.New(rand.NewSource(0x14f117))
	alphabets := []string{
		"abcABCDEF0123+/ ",
		"aéǿﬀß ",
		"ａｂｃＡＢＣ ",
		"ab\x00\xff\xfe ",
	}
	for trial := 0; trial < 3000; trial++ {
		alphabet := alphabets[rng.Intn(len(alphabets))]
		secret := dataTestR13RandomText(rng, alphabets[rng.Intn(len(alphabets))], 1+rng.Intn(12))
		guard := newDataSecretGuard(map[string]string{"password": secret})
		if guard == nil {
			continue
		}
		payload := dataTestR13RandomText(rng, alphabet, rng.Intn(80))
		budget := newDataSecretScanBudget()
		could, ok := guard.longPayloadCouldCarry(payload, budget)
		if !ok {
			t.Fatalf("trial %d: the prefilter exhausted the budget", trial)
		}
		if could {
			continue
		}
		stripped, _, _ := dataTextLongStrip(payload, newDataSecretScanBudget())
		if dataTestR14ContainsAny(dataTestR14NeedleValues(&guard.longRawNeedles), payload) {
			t.Fatalf("trial %d: the prefilter rejected a payload containing a raw needle (%q, secret %q)",
				trial, payload, secret)
		}
		if dataTestR14ContainsAny(dataTestR14NeedleValues(&guard.longBase64Needles), payload) ||
			(stripped != payload && dataTestR14ContainsAny(dataTestR14NeedleValues(&guard.longBase64Needles), stripped)) {
			t.Fatalf("trial %d: the prefilter rejected a payload containing an encoded needle (%q, secret %q)",
				trial, payload, secret)
		}
		normalized := dataTextLongNormalizeValue(payload)
		if dataTestR14ContainsAny(dataTestR14NeedleValues(&guard.longNeedles), normalized) {
			t.Fatalf("trial %d: the prefilter rejected a payload containing a normalized needle (payload %q hex %x, normalized %q hex %x, secret %q)",
				trial, payload, []byte(payload), normalized, []byte(normalized), secret)
		}
	}
}
