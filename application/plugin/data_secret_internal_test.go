package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dataTestPercentQuery and dataTestPercentPath are the query/path percent
// decoders used by the malformed-input and semantics tests. Production keeps
// only decodeDataTextPercentWith; these wrappers pin the two documented modes.
func dataTestPercentQuery(text string) (string, bool) {
	return decodeDataTextPercentWith(text, true)
}

func dataTestPercentPath(text string) (string, bool) {
	return decodeDataTextPercentWith(text, false)
}

// dataTestBase64Decodings extracts maximal base64-alphabet runs from text and
// returns their decoded bytes at offset zero. It is a test aid: the guard walks
// every alignment through walkDataTextBase64Tokens. Standard and URL-safe
// alphabets are chosen from the run's characters and padding is optional; a
// malformed run is skipped.
func dataTestBase64Decodings(text string, minBytes int) [][]byte {
	if len(text) < 4 {
		return nil
	}
	decodedForms := make([][]byte, 0, 4)
	for i := 0; i < len(text); {
		if !dataTextBase64Byte(text[i]) {
			i++
			continue
		}
		start := i
		for i < len(text) && dataTextBase64Byte(text[i]) {
			i++
		}
		run := text[start:i]
		if dataTextBase64DecodedLen(run) < minBytes {
			continue
		}
		if decoded, ok := dataTextBase64DecodeToken(run); ok {
			decodedForms = append(decodedForms, decoded)
		}
	}
	return decodedForms
}

// The guard rejects a full secret and any leading fragment at or above the
// documented threshold, so a provider cannot return a long secret prefix as a
// harmless-looking substring.
func TestDataSecretGuardRejectsLongPrefixes(t *testing.T) {
	secret := strings.Repeat("s", 40)
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	if guard.echoes("a benign provider message") {
		t.Fatal("a benign message was rejected")
	}
	if guard.echoes(secret[:dataSecretFragmentBytes-1]) {
		t.Fatalf("a %d-byte fragment was rejected, below the documented bound",
			dataSecretFragmentBytes-1)
	}
	if !guard.echoes(secret[:dataSecretFragmentBytes]) {
		t.Fatalf("a %d-byte prefix was accepted", dataSecretFragmentBytes)
	}
	if !guard.echoes("before:" + secret[:24] + ":after") {
		t.Fatal("a long secret prefix inside a message was accepted")
	}
	if !guard.echoes(secret) {
		t.Fatal("a full secret was accepted")
	}
}

// A secret split across two result fields is invisible to a per-field scan;
// the field concatenation in both orders reconstructs it.
func TestDataSecretGuardRejectsSplitSecrets(t *testing.T) {
	secret := "abcdefgh" + "ijklmnop"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	if !guard.echoesFields(secret[:8], secret[8:]) {
		t.Fatal("a forward split was accepted")
	}
	if !guard.echoesFields(secret[8:], secret[:8]) {
		t.Fatal("a reversed split was accepted")
	}
	// Splits whose leading fragment is below the threshold are still caught by
	// the concatenation.
	if !guard.echoesFields(secret[:3], secret[3:]) {
		t.Fatal("a short-leading split was accepted")
	}
	if guard.echoesFields("warehouse-1", "accepted") {
		t.Fatal("a benign field pair was rejected")
	}
}

// A secret can reach a message only in its escaped form: a %q-formatted host
// error or a provider that pre-escapes its text. The guard decodes Go/JSON
// escape sequences before deciding, so quotes, backslashes, newlines, tabs, and
// NULs cannot hide a secret behind escaping.
func TestDataSecretGuardRejectsEscapedSecrets(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"quote", `p@ss"word-verifier`},
		{"backslash", `p@ss\word-verifier`},
		{"newline", "p@ss\nword-verifier"},
		{"tab", "p@ss\tword-verifier"},
		{"nul", "p@ss\x00word-verifier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(map[string]string{"password": tc.value})
			if guard == nil {
				t.Fatal("guard is nil")
			}
			goQuoted := strconv.Quote(tc.value)
			if !guard.echoes(goQuoted) {
				t.Fatalf("Go-quoted secret was accepted: %q", goQuoted)
			}
			if !guard.echoes(strconv.QuoteToASCII(tc.value)) {
				t.Fatal("ASCII-quoted secret was accepted")
			}
			encoded, err := json.Marshal(map[string]string{"message": tc.value})
			if err != nil {
				t.Fatalf("marshal secret: %v", err)
			}
			if !guard.echoes(string(encoded)) {
				t.Fatalf("JSON-escaped secret was accepted: %s", encoded)
			}
			if guard.echoes(`a benign provider message with \n an escape`) {
				t.Fatal("a benign escaped message was rejected")
			}
		})
	}
}

// A secret split across fields and interleaved with junk is a subsequence of
// the concatenation rather than a substring, so the contiguous field-join
// scan cannot see it. The guard checks both deterministic field orders as
// subsequences as defense in depth behind the result-shape validation.
func TestDataSecretGuardRejectsInterleavedSplitSecrets(t *testing.T) {
	secret := "wqzhvdrmpkceufxzjrdhvbm"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	first := "a" + secret[0:6] + "b" + secret[6:12] + "c"
	second := "d" + secret[12:18] + "e" + secret[18:] + "f"
	if guard.echoes(first) || guard.echoes(second) {
		t.Fatal("the test construction is not interleaved enough")
	}
	// echoes is deliberately stricter than a contiguous scan: the full secret
	// appears as an ordered subsequence in the joined text, so the joined scan
	// rejects it too. echoesFields remains the field-aware path that sees the
	// split before the join.
	if !guard.echoes(first + second) {
		t.Fatal("an interleaved secret was accepted by the joined scan")
	}
	if !guard.echoesFields(first, second) {
		t.Fatal("an interleaved split secret was accepted")
	}
	if guard.echoesFields("warehouse-1", "accepted") {
		t.Fatal("a benign field pair was rejected")
	}
}

// A provider can spell a secret as a valid DNS name by putting dots between
// its bytes, or as an identifier with hyphens or underscores. The guard strips
// the separator class before deciding, for a long secret and for a short one
// that has no fragment above the threshold.
func TestDataSecretGuardRejectsSeparatorInterleavedSecrets(t *testing.T) {
	longSecret := "qvlxrmptwzbdjfhn"
	shortSecret := "hunter2"
	guard := newDataSecretGuard(map[string]string{
		"password": longSecret, "token": shortSecret,
	})
	interleave := func(value, separator string) string {
		return strings.Join(strings.Split(value, ""), separator)
	}
	cases := []struct {
		name string
		text string
	}{
		{"dots long", interleave(longSecret, ".")},
		{"dots short", interleave(shortSecret, ".")},
		{"hyphens long", interleave(longSecret, "-")},
		{"underscores long", interleave(longSecret, "_")},
		{"mixed short", interleave(shortSecret, "._-")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !guard.echoes(tc.text) {
				t.Fatalf("separator-interleaved secret was accepted: %q", tc.text)
			}
		})
	}
	// A secret's own separator is stripped along with the interleaved ones;
	// the raw subsequence rule still has to see it.
	if !guard.echoes(strings.Join(strings.Split(shortSecret, ""), ".-")) {
		t.Fatal("a secret with an internal hyphen was not caught")
	}
	if guard.echoes("warehouse-1") || guard.echoes("accepted") || guard.echoes("rejected") {
		t.Fatal("a benign identity or verdict was rejected")
	}
}

// Escaping the separator bytes does not defeat the normalized scan: the guard
// decodes Go/JSON escapes before removing the separator class.
func TestDataSecretGuardRejectsEscapedSeparatorInterleavedSecrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	dotted := strings.Join(strings.Split(secret, ""), ".")
	escaped := strings.ReplaceAll(dotted, ".", "\\u002e")
	if !guard.echoes(escaped) {
		t.Fatalf("escaped dotted secret was accepted: %q", escaped)
	}
	if !guard.echoes(strconv.Quote(escaped)) {
		t.Fatal("quoted escaped dotted secret was accepted")
	}
}

// Short secrets are guarded in full; they have no meaningful fragment rule
// because the whole value is shorter than the threshold.
func TestDataSecretGuardKeepsShortSecretSemantics(t *testing.T) {
	guard := newDataSecretGuard(map[string]string{"token": "tok-1"})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	if !guard.echoes("the token tok-1 was rejected") {
		t.Fatal("a full short secret was accepted")
	}
	if guard.echoes("tokens are accepted") {
		t.Fatal("benign text was rejected as a short secret fragment")
	}
}

// dataTestMixedCaseDataText alternates the case of ASCII letters so a secret
// can be returned in a case-folded spelling that is neither all lower nor all
// upper.
func dataTestMixedCaseDataText(text string) string {
	var mixed strings.Builder
	upper := true
	for i := 0; i < len(text); i++ {
		c := text[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' {
			if upper && c >= 'a' && c <= 'z' {
				c -= 'a' - 'A'
			} else if !upper && c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			upper = !upper
		}
		mixed.WriteByte(c)
	}
	return mixed.String()
}

// dataTestUnicodeEscapedDataText unicode-escapes every rune in text using
// Go/JSON \uXXXX syntax.
func dataTestUnicodeEscapedDataText(text string) string {
	var escaped strings.Builder
	for _, r := range text {
		fmt.Fprintf(&escaped, `\u%04x`, r)
	}
	return escaped.String()
}

// dataTestEscapedSecretDataText returns the secret behind levels escape
// layers: the first layer unicode-escapes every rune (Go/JSON \uXXXX syntax)
// and each further layer doubles every backslash, exactly what one more Go or
// JSON encoding pass produces.
func dataTestEscapedSecretDataText(secret string, levels int) string {
	text := dataTestUnicodeEscapedDataText(secret)
	for level := 1; level < levels; level++ {
		text = strings.ReplaceAll(text, `\`, `\\`)
	}
	return text
}

// A provider can case-fold a secret, in whole or in the threshold prefix the
// guard checks, and a legitimate uppercase identity must still round-trip.
// Case folding is invisible to a byte-exact scan, so every case spelling of
// every secret spelling is checked.
func TestDataSecretGuardRejectsCaseFoldedSecrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{
		"password": secret, "token": "hunter2",
	})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	interleave := func(value, separator string) string {
		return strings.Join(strings.Split(value, ""), separator)
	}
	cases := []struct{ name, text string }{
		{"upper full", strings.ToUpper(secret)},
		{"lower full", strings.ToLower(secret)},
		{"mixed full", dataTestMixedCaseDataText(secret)},
		{"upper prefix", strings.ToUpper(secret[:dataSecretFragmentBytes])},
		{"mixed prefix", dataTestMixedCaseDataText(secret[:12])},
		{"upper short", strings.ToUpper("hunter2")},
		{"mixed short", dataTestMixedCaseDataText("hunter2")},
		{"dotted upper", interleave(strings.ToUpper(secret), ".")},
		{"dashed mixed", interleave(dataTestMixedCaseDataText(secret), "-")},
		{"underscored upper short", interleave(strings.ToUpper("hunter2"), "_")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !guard.echoes(tc.text) {
				t.Fatalf("case-folded secret was accepted: %q", tc.text)
			}
		})
	}
	for _, benign := range []string{
		"DB-1.EXAMPLE.TEST", "WAREHOUSE-1", "ACCEPTED", "db.example.test",
	} {
		if guard.echoes(benign) {
			t.Fatalf("benign text %q was rejected as a secret", benign)
		}
	}
}

// A provider can return the secret backwards, in whole, as a threshold prefix,
// or case-folded as well. Reversing the candidate as well as the secret covers
// a reversed prefix: reversing the candidate restores the forward prefix the
// fragment rule checks.
func TestDataSecretGuardRejectsReversedSecrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	reversed := dataTextReversed(secret)
	guard := newDataSecretGuard(map[string]string{
		"password": secret, "token": "hunter2",
	})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	interleave := func(value, separator string) string {
		return strings.Join(strings.Split(value, ""), separator)
	}
	cases := []struct{ name, text string }{
		{"reversed full", reversed},
		{"upper reversed full", strings.ToUpper(reversed)},
		{"mixed reversed full", dataTestMixedCaseDataText(reversed)},
		{"reversed prefix", dataTextReversed(secret[:dataSecretFragmentBytes])},
		{"upper reversed prefix", strings.ToUpper(dataTextReversed(secret[:12]))},
		{"reversed short", dataTextReversed("hunter2")},
		{"dotted reversed", interleave(reversed, ".")},
		{"dashed upper reversed", interleave(strings.ToUpper(reversed), "-")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !guard.echoes(tc.text) {
				t.Fatalf("reversed secret was accepted: %q", tc.text)
			}
		})
	}
	if guard.echoes(dataTextReversed("warehouse-1")) {
		t.Fatal("a benign reversed identity was rejected")
	}
	if guard.echoes("DB-1.EXAMPLE.TEST") {
		t.Fatal("a benign uppercase identity was rejected")
	}
}

// An escape-encoded secret can sit behind more than two decoding passes: a
// provider pre-escapes one layer per hop and the host or transport can escape
// it again. Every intermediate form is scanned up to the documented depth.
func TestDataSecretGuardRejectsDeeplyEscapedSecrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	for levels := 3; levels <= dataSecretDecodeDepth; levels++ {
		text := dataTestEscapedSecretDataText(secret, levels)
		if strings.Contains(text, secret) {
			t.Fatalf("level %d construction is not escaped", levels)
		}
		if !guard.echoes(text) {
			t.Fatalf("%d-level escaped secret was accepted: %q", levels, text)
		}
		decoded := dataTestTextDecodings(text)
		if len(decoded) != levels+1 || decoded[len(decoded)-1] != secret {
			t.Fatalf("%d-level escapes decoded to %q in %d forms",
				levels, decoded[len(decoded)-1], len(decoded))
		}
	}
	// Re-escaping every character on each pass, rather than doubling existing
	// backslashes, also hides the plaintext one layer deeper per pass. This is
	// the shape that evaded the old two-pass decoder.
	perCharacter := secret
	for level := 1; level <= 4; level++ {
		perCharacter = dataTestUnicodeEscapedDataText(perCharacter)
		if level < 3 {
			continue
		}
		if !guard.echoes(perCharacter) {
			t.Fatalf("%d-level per-character escaped secret was accepted", level)
		}
	}
	// JSON and Go string escaping also nest; each wrapper adds a layer without
	// changing the ciphertext form the guard must recognize.
	nestedJSON := dataTestEscapedSecretDataText(secret, 1)
	for wrapper := 1; wrapper < 3; wrapper++ {
		encoded, err := json.Marshal(map[string]string{"message": nestedJSON})
		if err != nil {
			t.Fatalf("marshal nested JSON: %v", err)
		}
		nestedJSON = string(encoded)
	}
	if !guard.echoes(nestedJSON) {
		t.Fatalf("JSON-nested escaped secret was accepted: %q", nestedJSON)
	}
	nestedGo := dataTestEscapedSecretDataText(secret, 1)
	for wrapper := 1; wrapper < 3; wrapper++ {
		nestedGo = strconv.Quote(nestedGo)
	}
	if !guard.echoes(nestedGo) {
		t.Fatalf("Go-quoted escaped secret was accepted: %q", nestedGo)
	}
	if guard.echoes(`a benign provider message with \n an escape`) {
		t.Fatal("a benign escaped message was rejected")
	}
}

// dataTestPercentEncodeMixedCase percent-encodes every byte, alternating lower
// and upper hex digits so the decoder cannot depend on one hex case.
func dataTestPercentEncodeMixedCase(text string) string {
	var encoded strings.Builder
	for i := 0; i < len(text); i++ {
		if i%2 == 0 {
			fmt.Fprintf(&encoded, "%%%02x", text[i])
		} else {
			fmt.Fprintf(&encoded, "%%%02X", text[i])
		}
	}
	return encoded.String()
}

// dataTestFullwidthDataText maps ASCII letters, digits, and punctuation to
// their fullwidth compatibility forms.
func dataTestFullwidthDataText(text string) string {
	var fullwidth strings.Builder
	for _, r := range text {
		switch {
		case r >= '!' && r <= '~':
			fullwidth.WriteRune(r + 0xFEE0)
		case r == ' ':
			fullwidth.WriteRune('　')
		default:
			fullwidth.WriteRune(r)
		}
	}
	return fullwidth.String()
}

// dataTestBase64DataEncodings returns the four base64 spellings of value.
func dataTestBase64DataEncodings(value string) []string {
	raw := []byte(value)
	return []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	}
}

// A provider can percent-encode a secret with reserved characters to make it
// look like an ordinary URL fragment or error detail. The guard percent-decodes
// every intermediate form: query and path semantics, "+" for space, both hex
// cases, nested encoding, and encoding composed with case folding.
func TestDataSecretGuardRejectsPercentEncodedSecrets(t *testing.T) {
	secret := "p@ss/w:rd x+y"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	cases := []struct{ name, text string }{
		{"query escape", url.QueryEscape(secret)},
		{"path escape", url.PathEscape(secret)},
		{"double query escape", url.QueryEscape(url.QueryEscape(secret))},
		{"mixed hex case", dataTestPercentEncodeMixedCase(secret)},
		{"embedded query escape", "denied at " + url.QueryEscape(secret)},
		{"plus for space", "denied at " + url.PathEscape(secret) + "+x"},
		{"percent plus case fold", url.QueryEscape(strings.ToUpper(secret))},
		{"valid sequence after malformed", "%zz" + url.QueryEscape(secret) + "%2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !guard.echoes(tc.text) {
				t.Fatalf("percent-encoded secret was accepted: %q", tc.text)
			}
		})
	}
	for _, benign := range []string{
		"permission denied for table orders",
		url.QueryEscape("permission denied for table orders"),
		url.PathEscape("warehouse-1/status"),
	} {
		if guard.echoes(benign) {
			t.Fatalf("benign percent-encoded text %q was rejected", benign)
		}
	}
}

// Malformed and truncated percent sequences must never panic, must be left
// byte-for-byte in place, and must not stop a later valid sequence from being
// decoded, under both query and path semantics.
func TestDataTextPercentDecodingMalformedInput(t *testing.T) {
	decoders := []struct {
		name   string
		decode func(string) (string, bool)
	}{
		{"query", dataTestPercentQuery},
		{"path", dataTestPercentPath},
	}
	for _, decoder := range decoders {
		t.Run(decoder.name, func(t *testing.T) {
			for _, input := range []string{"100%", "%4", "%zz", "a%2", "%%", "%g0"} {
				decoded, ok := decoder.decode(input)
				if ok || decoded != "" {
					t.Fatalf("malformed input %q decoded to %q (changed %v)", input, decoded, ok)
				}
			}
			decoded, ok := decoder.decode("%zz%41%2")
			if !ok || decoded != "%zzA%2" {
				t.Fatalf("mixed valid/malformed input decoded to %q (changed %v)", decoded, ok)
			}
		})
	}
}

// Query and path percent semantics differ in exactly one rule: a raw "+" is a
// space in a query and a literal plus in a path. Producing both forms at every
// layer is required because url.PathEscape leaves "+" unescaped while escaping
// the other reserved bytes, so a decoder that always applies query semantics
// loses a path-escaped value.
func TestDataTextPercentDecodesQueryAndPathSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, input, query, path string
		pathChanged              bool
	}{
		{"raw plus", "a+b", "a b", "", false},
		{"escaped plus", "a%2Bb", "a+b", "a+b", true},
		{"escaped space", "a%20b", "a b", "a b", true},
		{"both hex cases", "%2f%2F", "//", "//", true},
		{"path escaped reserved", "+a%2F12345wxy%3F9+BC",
			" a/12345wxy?9 BC", "+a/12345wxy?9+BC", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			query, queryOK := dataTestPercentQuery(tc.input)
			path, pathOK := dataTestPercentPath(tc.input)
			if !queryOK || query != tc.query {
				t.Fatalf("query decode of %q = %q (changed %v), want %q",
					tc.input, query, queryOK, tc.query)
			}
			if tc.pathChanged {
				if !pathOK || path != tc.path {
					t.Fatalf("path decode of %q = %q (changed %v), want %q",
						tc.input, path, pathOK, tc.path)
				}
				return
			}
			// A raw "+" is unchanged under path semantics, so the decoder
			// reports no change rather than returning an identical string.
			if pathOK || path != "" {
				t.Fatalf("path decode of %q = %q (changed %v), want no change",
					tc.input, path, pathOK)
			}
		})
	}
}

// dataTestPathEscapedSecret is a 16-byte probe whose first eight bytes hold a
// literal "+" and an escaped "/", and whose last eight bytes hold an escaped
// "?". url.PathEscape therefore leaves the plus literal and escapes bytes in
// both halves, so neither half is a raw substring of the escaped text.
const dataTestPathEscapedSecret = "+a/12345wxy?9+BC"

// A path-escaped secret is delivered by a provider that treats a value as a URL
// path segment. Query-only percent decoding maps the literal "+" to a space and
// loses the value; both semantics must be generated at every layer.
func TestDataSecretGuardRejectsPathEscapedPlusSecrets(t *testing.T) {
	guard := newDataSecretGuard(map[string]string{"password": dataTestPathEscapedSecret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	escaped := url.PathEscape(dataTestPathEscapedSecret)
	if escaped == dataTestPathEscapedSecret {
		t.Fatal("the probe is not path-escaped")
	}
	if strings.Contains(escaped, dataTestPathEscapedSecret[:8]) {
		t.Fatal("the escaped probe exposes its leading fragment")
	}
	for _, text := range []string{
		escaped,
		"denied at " + escaped,
		url.QueryEscape(escaped),
		url.PathEscape(escaped),
	} {
		if !guard.echoes(text) {
			t.Fatalf("path-escaped secret was accepted: %q", text)
		}
	}
	// The guard still rejects benign path-escaped text.
	if guard.echoes(url.PathEscape("warehouse-1/status")) {
		t.Fatal("benign path-escaped text was rejected")
	}
}

// dataTestPercentChain percent-encodes text once per layer, alternating lower
// and upper hex digits on every pass so the decoder cannot depend on one case.
func dataTestPercentChain(text string, layers int) string {
	for layer := 0; layer < layers; layer++ {
		text = dataTestPercentEncodeMixedCase(text)
	}
	return text
}

// dataTestEscapeGadget returns a decoy whose escape decoding stays live for
// layers rounds: 2^(layers-1) backslashes halve once per pass and then the
// final "A" decodes to "A".
func dataTestEscapeGadget(layers int) string {
	if layers < 1 {
		return ""
	}
	return strings.Repeat(`\`, 1<<(layers-1)) + "u0041"
}

// A pure percent/escape chain must be decoded to its fixed point regardless of
// decoys: neither the breadth of the decode search nor the escape gadgets that
// keep alternative branches alive may consume the chain's depth budget. The
// depth-1 control and the deep chain are both scanned.
func TestDataSecretGuardDecodesPercentChainsPastDecoyGadgets(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		layers int
		gadget int
	}{
		{"one byte", "Z", 4, 6},
		{"two bytes", "Zq", 4, 6},
		{"sixteen bytes", "qvlxrmptwzbdjfhn", 7, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(map[string]string{"password": tc.secret})
			if guard == nil {
				t.Fatal("guard is nil")
			}
			// The decoy keeps escape and percent decoding alive together: the
			// percent-encoded backslashes become new escape gadgets after the
			// first percent pass, so both branches stay productive at every
			// depth and the old breadth-first form cap filled before the chain.
			decoy := dataTestEscapeGadget(tc.gadget) + "%5C" + dataTestEscapeGadget(4) + "+DIV+"
			for _, layers := range []int{1, tc.layers} {
				chain := dataTestPercentChain(tc.secret, layers)
				text := decoy + chain
				if strings.Contains(text, tc.secret) {
					t.Fatalf("the %d-layer construction exposes the secret in the clear", layers)
				}
				if !guard.echoes(text) {
					t.Fatalf("%d-layer percent chain past decoys was accepted", layers)
				}
			}
			// The walk must expose a depth-1 intermediate and the final secret,
			// not just stop after a handful of decoy forms.
			chain := dataTestPercentChain(tc.secret, tc.layers)
			text := decoy + chain
			depthOne := dataTestPercentChain(tc.secret, tc.layers-1)
			forms := dataTestTextDecodings(text)
			foundDepthOne, foundSecret := false, false
			for _, form := range forms {
				if strings.Contains(form, depthOne) {
					foundDepthOne = true
				}
				if strings.Contains(form, tc.secret) {
					foundSecret = true
				}
			}
			if !foundDepthOne {
				t.Fatalf("the depth-1 intermediate %q was not scanned", depthOne)
			}
			if !foundSecret {
				t.Fatalf("the %d-layer chain never produced the secret; %d forms scanned",
					tc.layers, len(forms))
			}
			if len(forms) > dataSecretDecodeWalkBound {
				t.Fatalf("the decode walk produced %d forms, above the %d bound",
					len(forms), dataSecretDecodeWalkBound)
			}
		})
	}
}

// dataTestScatteredDataText separates every byte of value with filler, so the
// value is only an ordered subsequence, never a contiguous run.
func dataTestScatteredDataText(value, filler string) string {
	var scattered strings.Builder
	for i := 0; i < len(value); i++ {
		scattered.WriteString(filler)
		scattered.WriteByte(value[i])
	}
	return scattered.String()
}

// Long diagnostic text keeps the echo rules: a secret interleaved with spaces
// or punctuation must still be caught, while text that scatters the same bytes
// beyond the anchored window stays benign.
func TestDataSecretGuardRejectsInterleavedSecretsInLongText(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	padding := strings.Repeat("plan detail ", 600)
	interleave := func(separator string) string {
		return strings.Join(strings.Split(secret, ""), separator)
	}
	cases := []struct{ name, text string }{
		{"space interleaved", padding + interleave(" ")},
		{"tab interleaved", padding + interleave("\t")},
		{"punctuation interleaved", padding + interleave("|")},
		{"mixed punctuation", padding + interleave(" ,.")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.text) <= dataSecretSubsequenceBytes {
				t.Fatal("the construction is not long")
			}
			if !guard.echoes(tc.text) {
				t.Fatalf("interleaved secret in %d bytes was accepted", len(tc.text))
			}
		})
	}
	scattered := padding + dataTestScatteredDataText(secret, strings.Repeat("q", 700))
	if len(scattered) <= dataSecretSubsequenceBytes {
		t.Fatal("the benign construction is not long")
	}
	if guard.echoes(scattered) {
		t.Fatal("benign long text with scattered secret bytes was rejected")
	}
}

// Pin the long-text policy at its boundary: text at the documented bound uses
// the full ordered-subsequence rule, and text one byte longer uses the linear
// normalized-containment rule. Arbitrary letter interleaving above the bound is
// the documented residual of the linear rule: any short secret is almost
// certainly an ordered subsequence of a long free-form text by chance, so the
// host does not run a quadratic subsequence scan that would reject ordinary
// plan and summary prose.
func TestDataSecretGuardLongTextSubsequenceBoundary(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	// A 40-byte gap between secret bytes is well inside a 4096-byte text, so the
	// full subsequence rule catches it at the short-text bound.
	scattered := dataTestScatteredDataText(secret, strings.Repeat("q", 40))
	atBound := strings.Repeat("q", dataSecretSubsequenceBytes-len(scattered)) + scattered
	if len(atBound) != dataSecretSubsequenceBytes {
		t.Fatalf("boundary construction is %d bytes", len(atBound))
	}
	if !guard.echoes(atBound) {
		t.Fatal("the full subsequence inside the 4096-byte policy was accepted")
	}
	overBound := atBound + "q"
	// One byte past the bound the same letter-interleaved bytes are benign: the
	// documented residual of the linear long-text rule.
	if guard.echoes(overBound) {
		t.Fatal("a letter-interleaved secret past 4096 bytes was rejected; that interleaving is the documented residual")
	}
	// Separator interleaving survives the policy change because separator
	// stripping is part of the linear normalized form.
	nearBound := strings.Repeat("q", dataSecretSubsequenceBytes) +
		dataTestScatteredDataText(secret, " ")
	if !guard.echoes(nearBound) {
		t.Fatal("a separator-interleaved secret past 4096 bytes was accepted")
	}
}

// A decoded token is not quarantined at 4096 bytes any more: the guard decodes
// and inspects tokens within the decoded-byte budget, so a secret placed past
// the old quarantine is still caught. Exhausting the decoded-byte budget fails
// closed instead of skipping the token. The secret is at an unaligned offset so
// the token does not also contain a precomputed base64 spelling of it.
func TestDataTextBase64DecodedTokenBoundary(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	makeToken := func(size int) string {
		payload := append([]byte{0x00}, []byte(secret)...)
		payload = append(payload, bytes.Repeat([]byte{0x00}, size-len(payload))...)
		return base64.RawStdEncoding.EncodeToString(payload)
	}
	for _, size := range []int{4096, 4097, 4200} {
		token := makeToken(size)
		if !guard.echoes(token) {
			t.Fatalf("a secret inside a %d-byte decoded token was accepted", size)
		}
		forms := dataTestBase64Decodings(token, len(secret))
		if len(forms) != 1 || len(forms[0]) != size {
			t.Fatalf("%d decoded bytes produced %d forms", size, len(forms))
		}
	}
	// A benign token of the same size is decoded but delivered.
	benignPayload := bytes.Repeat([]byte{'x'}, 4200)
	if guard.echoes(base64.RawStdEncoding.EncodeToString(benignPayload)) {
		t.Fatal("a benign 4200-byte decoded token was rejected")
	}
	// The decoded-byte budget is the replacement bound: when it is exhausted by
	// earlier tokens, the next token fails closed rather than being skipped.
	budget := newDataSecretScanBudget()
	budget.decodedBytes = dataSecretBase64DecodedByteBudget
	small := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	if !guard.echoesBase64(small, 0, budget) {
		t.Fatal("an exhausted decoded-byte budget was skipped instead of failing closed")
	}
	if !budget.exceeded {
		t.Fatal("the budget did not record the decoded-byte exhaustion")
	}
}

// A provider can base64-encode a secret, with the standard or URL-safe
// alphabet, padded or not, and return it verbatim as a DNS-shaped identity or
// inside a message. The guard decodes tokens, compares their bytes with the raw
// secret, and scans valid decoded text through the full spelling pipeline;
// nested, reversed, and percent-encoded encodings compose as well.
func TestDataSecretGuardRejectsBase64Secrets(t *testing.T) {
	punct := "p@ss/w:rd x+y"
	dnsSafe := "aaaaaaaaaaaa"
	binary := string([]byte{0xff, 0x00, 0xfe, 0x7f, 0x80, 0x01, 0x02, 0x03})
	guard := newDataSecretGuard(map[string]string{
		"password": punct, "token": dnsSafe, "bin": binary,
	})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	encodings := dataTestBase64DataEncodings(punct)
	for index, encoded := range encodings {
		t.Run(fmt.Sprintf("variant-%d", index), func(t *testing.T) {
			if !guard.echoes(encoded) {
				t.Fatalf("base64 secret was accepted: %q", encoded)
			}
			if !guard.echoes("failed token " + encoded + " rejected") {
				t.Fatalf("embedded base64 secret was accepted: %q", encoded)
			}
		})
	}
	// A raw standard encoding of a short ASCII secret is a valid DNS name, the
	// shape a ServerIdentity accepts.
	dnsEncoded := base64.RawStdEncoding.EncodeToString([]byte(dnsSafe))
	if !guard.echoes(dnsEncoded) {
		t.Fatalf("DNS-shaped base64 identity was accepted: %q", dnsEncoded)
	}
	// Dots inserted to make a base64url token look like a DNS name must not
	// erase the URL-alphabet "-" and "_" data characters.
	urlToken := base64.RawURLEncoding.EncodeToString([]byte(binary))
	var dotted strings.Builder
	for i := 0; i < len(urlToken); i++ {
		if i > 0 && i%4 == 0 {
			dotted.WriteByte('.')
		}
		dotted.WriteByte(urlToken[i])
	}
	if !guard.echoes(dotted.String()) {
		t.Fatalf("dotted base64url identity was accepted: %q", dotted.String())
	}
	// A reversed token and an encoding of a reversed secret are both echoes.
	if !guard.echoes(dataTextReversed(encodings[0])) {
		t.Fatal("reversed base64 token was accepted")
	}
	if !guard.echoes(base64.StdEncoding.EncodeToString([]byte(dataTextReversed(dnsSafe)))) {
		t.Fatal("base64 of a reversed secret was accepted")
	}
	// Percent-encoded base64 composes with percent decoding.
	if !guard.echoes(url.QueryEscape(encodings[0])) {
		t.Fatal("percent-encoded base64 secret was accepted")
	}
	// Nested base64 is scanned up to the documented depth.
	double := base64.StdEncoding.EncodeToString(
		[]byte(base64.StdEncoding.EncodeToString([]byte(dnsSafe))))
	if !guard.echoes(double) {
		t.Fatal("double-base64 secret was accepted")
	}
	// Invalid UTF-8 secrets are caught by the raw byte comparison.
	if !guard.echoes(base64.StdEncoding.EncodeToString([]byte(binary))) {
		t.Fatal("base64 of a binary secret was accepted")
	}
	// Malformed tokens are skipped without crashing or a false positive, and a
	// benign token well past the old 4096-byte quarantine is decoded and
	// delivered.
	for _, text := range []string{
		"====", "not!base64==", "%%%%%%%%", strings.Repeat("Q", 5472),
	} {
		if guard.echoes(text) {
			t.Fatalf("non-echo %q was rejected", text)
		}
	}
	if decoded := dataTestBase64Decodings(strings.Repeat("QUJD", 2000), 1); len(decoded) != 1 || len(decoded[0]) != 6000 {
		t.Fatalf("a 6000-byte decoded token produced %d forms", len(decoded))
	}
	// Benign base64 of unrelated text is not a secret.
	for _, benign := range []string{
		base64.StdEncoding.EncodeToString([]byte("warehouse-1 accepted")),
		base64.RawURLEncoding.EncodeToString([]byte("permission denied for table orders")),
	} {
		if guard.echoes(benign) {
			t.Fatalf("benign base64 text %q was rejected", benign)
		}
	}
}

// A provider can return a secret in a Unicode spelling that is visually or
// canonically equivalent but byte-different: fullwidth forms, NFD, the
// sharp-s/ligature substitutions, and combinations with escaping or percent
// encoding. Both the secret and the candidate are folded, so either direction
// matches. Ordinary Unicode text still passes.
func TestDataSecretGuardRejectsUnicodeFoldedSecrets(t *testing.T) {
	cases := []struct{ name, secret, candidate string }{
		{"fullwidth", "qvlxrmptwzbdjfhn", dataTestFullwidthDataText("qvlxrmptwzbdjfhn")},
		{"fullwidth upper", "qvlxrmptwzbdjfhn",
			strings.ToUpper(dataTestFullwidthDataText("qvlxrmptwzbdjfhn"))},
		{"nfd candidate", "café-secret", "café-secret"},
		{"nfc candidate", "café-secret", "café-secret"},
		{"sharp s expanded", "straße-geheim", "STRASSE-GEHEIM"},
		{"sharp s contracted", "strasse-geheim", "straße-geheim"},
		{"ae ligature", "æblegrød", "aeblegrod"},
		{"oe ligature", "cœur-de-lion", "coeur-de-lion"},
		{"o stroke", "smørrebrød", "smorrebrod"},
		{"d stroke", "Đorđe", "DORDE"},
		{"l stroke", "Łódź", "LODZ"},
		{"thorn", "þorn", "THORN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(map[string]string{"password": tc.secret})
			if guard == nil {
				t.Fatal("guard is nil")
			}
			if !guard.echoes(tc.candidate) {
				t.Fatalf("Unicode-folded secret %q was accepted as %q", tc.secret, tc.candidate)
			}
			if !guard.echoes(dataTextReversed(tc.candidate)) {
				t.Fatalf("reversed Unicode-folded secret was accepted: %q", tc.candidate)
			}
		})
	}

	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	fullwidth := dataTestFullwidthDataText(secret)
	// Escaping and percent encoding compose with the Unicode folding: the
	// decoder produces the fullwidth form and NFKC folds it to the secret.
	if !guard.echoes(dataTestUnicodeEscapedDataText(fullwidth)) {
		t.Fatal("escaped fullwidth secret was accepted")
	}
	if !guard.echoes(url.QueryEscape(fullwidth)) {
		t.Fatal("percent-encoded fullwidth secret was accepted")
	}
	for _, benign := range []string{
		"café menu", "naïve façade", "Straße der Nationen",
		dataTestFullwidthDataText("warehouse-1"), "smørrebrød er godt",
	} {
		if guard.echoes(benign) {
			t.Fatalf("benign Unicode text %q was rejected", benign)
		}
	}
}

// The new decoding and folding stages must stay bounded: the form cap holds,
// decoders never expand their input, oversized base64 tokens are quarantined,
// and a 2 MiB input is scanned well inside the documented budget.
func TestDataSecretGuardBoundsEncodedExpansion(t *testing.T) {
	pathological := strings.Repeat(`%5Cu0041\`, 512)
	forms := dataTestTextDecodings(pathological)
	if len(forms) > dataSecretDecodeWalkBound {
		t.Fatalf("pathological input produced %d decoded forms, above the %d bound",
			len(forms), dataSecretDecodeWalkBound)
	}
	for _, form := range forms {
		if len(form) > len(pathological) {
			t.Fatalf("decoding expanded %d bytes to %d", len(pathological), len(form))
		}
	}
	deep := dataTestEscapedSecretDataText("qvlxrmptwzbdjfhn", dataSecretDecodeDepth+4)
	if forms := dataTestTextDecodings(deep); len(forms) > dataSecretDecodeDepth+1 {
		t.Fatalf("deeply escaped input produced %d forms", len(forms))
	}
	// Composition: a percent-encoded escape chain still reaches the secret.
	secret := "qvlxrmptwzbdjfhn"
	encoded := url.QueryEscape(dataTestEscapedSecretDataText(secret, 1))
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if !guard.echoes(encoded) {
		t.Fatalf("percent-encoded escape chain was accepted: %q", encoded)
	}
	// A max-size identity (2 MiB) is scanned in bounded time.
	big := strings.Repeat("Zk9wR2hq", 1<<18)
	started := time.Now()
	guard.echoes(big)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("2 MiB scan took %s", elapsed)
	}
}

// dataTestTextDecodings returns text plus every intermediate decoded form in
// depth-first discovery order, with the original first. Production scanning is
// lazy through walkDataTextDecodings; this helper keeps the tests that inspect
// the produced form set from depending on a test-only production surface.
func dataTestTextDecodings(text string) []string {
	candidates := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	walkDataTextDecodings(text, func(form string) bool {
		if _, ok := seen[form]; ok {
			return false
		}
		seen[form] = struct{}{}
		candidates = append(candidates, form)
		return false
	})
	return candidates
}

// Pin the base64 nesting behavior: a token at the documented cap fails closed
// instead of being skipped, because the layer above the cap was never examined.
func TestDataSecretGuardBase64DepthCapFailsClosed(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	token := base64.RawStdEncoding.EncodeToString([]byte(secret))
	budget := newDataSecretScanBudget()
	if !guard.echoesBase64(token, dataSecretBase64Depth, budget) {
		t.Fatal("a token at the nesting cap was skipped and delivered")
	}
	if !budget.exceeded {
		t.Fatal("the cap decision was not recorded as fail-closed")
	}
	// Plain text with no token-length run at the cap is not rejected.
	budget = newDataSecretScanBudget()
	if guard.echoesBase64("done", dataSecretBase64Depth, budget) {
		t.Fatal("plain text at the nesting cap was rejected")
	}
	// A five-layer nesting is caught well inside the cap.
	nested := secret
	for level := 0; level < 5; level++ {
		nested = base64.RawStdEncoding.EncodeToString([]byte(nested))
	}
	if !guard.echoes(nested) {
		t.Fatal("five-layer nested base64 was accepted")
	}
}

// Pin the work budget's fail-closed behavior without relying on wall-clock
// time: a budget whose work is already at the limit aborts every stage.
func TestDataSecretGuardWorkBudgetFailsClosed(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	budget := newDataSecretScanBudget()
	budget.work = dataSecretScanWorkBudget
	if !guard.echoesWithin("benign plan detail", budget) {
		t.Fatal("an exhausted work budget delivered the text")
	}
	if !budget.exceeded {
		t.Fatal("the work-budget stop was not recorded")
	}
	// A fresh budget still delivers the same text.
	if guard.echoes("benign plan detail") {
		t.Fatal("benign text was rejected with a fresh budget")
	}
}

// Pin the token budget's fail-closed behavior: the token after the budget is
// exhausted is never silently skipped.
func TestDataSecretGuardTokenBudgetFailsClosed(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	budget := newDataSecretScanBudget()
	budget.base64Tokens = 0
	token := base64.RawStdEncoding.EncodeToString(append([]byte{0x00}, []byte(secret)...))
	if !guard.echoesBase64(token, 0, budget) {
		t.Fatal("a token past the exhausted token budget was skipped")
	}
	if !budget.exceeded {
		t.Fatal("the token-budget stop was not recorded")
	}
}

// Long text above dataSecretSubsequenceBytes is scanned linearly. Arbitrary
// letter interleaving is the documented residual: it is not a contiguous,
// encoded, escaped, separator-interleaved, or reversed spelling, and the
// provider can already return the secret verbatim through an opaque row or
// document payload. Separator interleaving and every encoded spelling remain
// caught by the normalized linear forms and the base64/decoder stages.
func TestDataSecretGuardLongTextLetterInterleavingIsResidual(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	// A narrow and a wide letter-interleaved spread are both delivered: the
	// residual is about interleaving junk, not about the span. The filler must
	// separate every byte of the threshold prefix; a spread that leaves the
	// first eight secret bytes contiguous is still rejected by the threshold
	// rule.
	for _, gap := range []string{"z", strings.Repeat("z", 40), strings.Repeat("z", 700)} {
		scattered := strings.Repeat("z", dataSecretSubsequenceBytes) +
			dataTestScatteredDataText(secret, gap)
		if guard.echoes(scattered) {
			t.Fatalf("a secret interleaved with %d-byte letter gaps was rejected; letter interleaving past %d bytes is the documented residual",
				len(gap), dataSecretSubsequenceBytes)
		}
	}
	// Space interleaving is a separator spelling and stays rejected at the same
	// length, so the residual does not disable the separator rule.
	spaced := strings.Repeat("z", dataSecretSubsequenceBytes) +
		strings.Join(strings.Split(secret, ""), " ")
	if !guard.echoes(spaced) {
		t.Fatal("a separator-interleaved secret past 4096 bytes was accepted")
	}
}

// R5: a binary secret must not make unrelated binary payloads compare equal.
// A lossy whole-string ToLower maps every invalid byte to U+FFFD, so these two
// payloads used to fold to the same string.
func TestDataSecretGuardBinaryCaseFoldingIsByteExact(t *testing.T) {
	secret := string([]byte{0x01, 0x02, 0x03, 0xff})
	benign := string([]byte{0x01, 0x02, 0x03, 0xfe})
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	if guard.echoes(benign) {
		t.Fatal("an unrelated binary payload was rejected")
	}
	if guard.echoes(base64.StdEncoding.EncodeToString([]byte(benign))) {
		t.Fatal("base64 of an unrelated binary payload was rejected")
	}
	if !guard.echoes(secret) {
		t.Fatal("the binary secret itself was accepted")
	}
	if !guard.echoes(base64.StdEncoding.EncodeToString([]byte(secret))) {
		t.Fatal("base64 of the binary secret was accepted")
	}
	// Valid UTF-8 folding still works after the invalid-byte handling: the NFD
	// spelling of a precomposed secret is still equivalent.
	unicodeGuard := newDataSecretGuard(map[string]string{"password": "café-secret"})
	if !unicodeGuard.echoes("café-secret") {
		t.Fatal("NFD spelling was accepted")
	}
}
