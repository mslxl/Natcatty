package plugin_test

import (
	"context"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// dataTestR13Bytes is the ten-byte binary secret from the round-13
// reproduction: "ABCDEFG", the raw byte 0xFE, and "HJ". The printable prefix
// makes the secret a plausible diagnostic fragment; the high byte is what the
// escape decoder used to re-encode as the UTF-8 rune U+00FE (c3 be) instead of
// copying the raw byte.
const dataTestR13Bytes = "ABCDEFG\xfeHJ"

// dataTestR13HexEscaped spells every byte of dataTestR13Bytes as a Go/JSON hex
// escape.
const dataTestR13HexEscaped = `\x41\x42\x43\x44\x45\x46\x47\xfe\x48\x4a`

// dataTestR13OctalEscaped spells the same bytes as three-digit octal escapes.
const dataTestR13OctalEscaped = `\101\102\103\104\105\106\107\376\110\112`

// dataTestR13EscapedByteCases lists the spellings a provider can use to write a
// raw high byte into host-visible text. Each spelling decodes to bytes carrying
// the secret or its eight-byte threshold prefix contiguously, so every
// caller-visible path must fail closed with the constant protocol error. A
// spelling that inserts a filler byte between secret bytes is the documented
// long-text letter-interleaving residual, not a contiguous echo. The
// repeated-FE case is a whole secret of high bytes; the FF cases place the high
// byte inside the threshold prefix; the UTF-8 case escapes a valid multi-byte
// rune one byte at a time. The \u00fe and \U000000fe cases write a rune whose byte
// interpretation is the high byte the provider escaped (\u00fe was also
// reported as the "thorn" spelling).
func dataTestR13EscapedByteCases() []struct{ name, secret, spelling string } {
	return []struct{ name, secret, spelling string }{
		{"hex byte escapes", dataTestR13Bytes, dataTestR13HexEscaped},
		{"octal byte escapes", dataTestR13Bytes, dataTestR13OctalEscaped},
		{"repeated FE bytes", strings.Repeat("\xfe", 16), strings.Repeat(`\xfe`, 16)},
		{"hex FF in prefix", "AB\xffDEFGHIJ", `\x41\x42\xff\x44\x45\x46\x47\x48\x49\x4a`},
		{"octal FF in prefix", "AB\xffDEFGHIJ", `\101\102\377\104\105\106\107\110\111\112`},
		{"UTF-8 bytes escaped one by one", "ABCDEF€", `\x41\x42\x43\x44\x45\x46\xe2\x82\xac`},
		{"u00fe rune escape", dataTestR13Bytes, `\x41\x42\x43\x44\x45\x46\x47\u00fe\x48\x4a`},
		{"U000000fe rune escape", dataTestR13Bytes, `\x41\x42\x43\x44\x45\x46\x47\U000000fe\x48\x4a`},
	}
}

// The byte-escape defect: a long plan or summary diagnostic whose escaped
// spelling decodes to the raw secret bytes used to be delivered because the
// decoder wrote every escape value with WriteRune. A \xfe escape became the two
// bytes c3 be and never matched a raw 0xFE secret, so the guard's decoded
// candidates could not contain the secret. Plan text, summary messages, denied
// status messages, and the profile-test server identity are all caller-visible
// and must each be replaced by the constant protocol error without echoing the
// escaped spelling.
func TestDataProviderRound13RejectsByteEscapedSecretEchoes(t *testing.T) {
	for _, tc := range dataTestR13EscapedByteCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR12Around(tc.spelling)
			env, lease := dataTestR9Env(t, tc.secret)
			for _, call := range []struct {
				name string
				run  func() error
			}{
				{"plan", func() error { return dataTestR9Plan(env, lease, text) }},
				{"summary", func() error { return dataTestR9Summary(env, lease, text) }},
			} {
				dataTestCheckSecretEchoError(t, call.run(), tc.secret, tc.spelling)
			}

			// The denied status message is a caller-visible field with a 256-byte
			// display bound; every spelling here fits.
			err := dataTestQueryStatus(env, lease, "denied", "failed at "+tc.spelling)
			dataTestCheckSecretEchoError(t, err, tc.secret, tc.spelling)

			// The profile-test identity is scanned before shape validation, so the
			// escaped value must produce the echo error rather than a shape error.
			dataTestOpenResponse(env, tc.spelling, "accepted")
			_, err = env.host.TestProfile(context.Background(), env.profile.ID)
			dataTestCheckSecretEchoError(t, err, tc.secret, tc.spelling)
		})
	}
}

// The byte interpretation of \u00NN is an addition, not a replacement: a
// secret that really is the UTF-8 rune (\u00fe here) must still be caught when the
// provider writes the rune as \u00fe.
func TestDataProviderRound13KeepsRuneEscapeInterpretation(t *testing.T) {
	const secret = "ABCDEFG\u00feHJ"
	text := dataTestR12Around(`\x41\x42\x43\x44\x45\x46\x47\u00fe\x48\x4a`)
	env, lease := dataTestR9Env(t, secret)
	dataTestCheckSecretEchoError(t, dataTestR9Plan(env, lease, text), secret)
	dataTestCheckSecretEchoError(t, dataTestR9Summary(env, lease, text), secret)
}

// dataTestR13Env registers a read-only provider profile holding every supplied
// secret and returns the environment plus an acquired lease.
func dataTestR13Env(t *testing.T, secrets map[string]string) (*dataEnv, *appplugin.DataLease) {
	t.Helper()
	draft := dataTestDraft()
	draft.Secrets = secrets
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	t.Cleanup(func() { lease.Release() })
	return env, lease
}

// The availability defect: benign 4 MiB plan and summary diagnostics that
// contain every secret byte must be delivered even when the text is upper-case,
// mixed-case, carries a public qualifier, is dense in "+" (which materializes a
// whole extra decoded form), uses decimal dots, or is checked against two
// distinct secrets. These variants crossed the old 128 MiB bound and failed
// closed after about 150 ms; all of them now stay under the shared bound.
func TestDataProviderRound13DeliversBenignFourMiBEchoGuardVariants(t *testing.T) {
	const size = 4190208
	const secret = "qvlxrmptwzbdjfhn"
	const secondSecret = "pzqjxvkfbgwmcyhs"
	const unit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const publicUnit = "query plan node: bitmap index scan on public.orders foreign key value zip column with merge join width 36 rows 120 using btree cost 8 "
	const plusUnit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36+9 rows 120+4 using btree cost 8+2 "
	const dotUnit = "query plan node: bitmap index scan on foreign key value zip column with merge join width 36.5 rows 120.25 using btree cost 8.75 "
	const plusDenseUnit = "query+plan+node:+bitmap+index+scan+on+foreign+key+value+zip+column+with+merge+join+width+36+9+rows+120+4+using+btree+cost+8+2+"

	cases := []struct {
		name    string
		unit    string
		secrets map[string]string
	}{
		{"lowercase", unit, map[string]string{"password": secret}},
		{"uppercase", strings.ToUpper(unit), map[string]string{"password": secret}},
		{"alternating case", dataTestR12MixedCaseUnit(unit), map[string]string{"password": secret}},
		{"public qualifier", publicUnit, map[string]string{"password": secret}},
		{"mixed-case public qualifier", dataTestR12MixedCaseUnit(publicUnit), map[string]string{"password": secret}},
		{"plus signs", plusUnit, map[string]string{"password": secret}},
		{"plus-dense base64 alphabet", plusDenseUnit, map[string]string{"password": secret}},
		{"decimal dots", dotUnit, map[string]string{"password": secret}},
		{"two secrets", unit, map[string]string{"password": secret, "token": secondSecret}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR11Repeat(tc.unit, size)
			env, lease := dataTestR13Env(t, tc.secrets)
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
					t.Fatalf("the %d-byte benign %s %s was delivered after %s, want under 500ms",
						len(text), tc.name, call.name, elapsed)
				}
				t.Logf("benign %d-byte %s %s delivered in %s", len(text), tc.name, call.name, elapsed)
			}
		})
	}
}

// A dense pathological text must still fail closed with the constant protocol
// error. The 23-form pattern keeps escape, percent, and plus decoding live at
// once and exhausts the decode-form budget, so it cannot be delivered. The
// plus-dense base64-alphabet shape is not pathological under the linear rule:
// it holds no echo, its alphabet runs decode to payloads that do not contain
// the secret, and the long-token walk does not fail closed merely for decoding
// many benign tokens. It is covered as a benign delivery case above.
func TestDataProviderRound13RejectsDenseDecodeDiagnostics(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	// The escape/percent pattern needs 3,584 KiB rather than the full 4 MiB:
	// the JSON envelope for it expands every backslash, and the request layer
	// rejects an oversized plan result envelope before the guard runs. This is
	// the size the round-9 pathological regression uses.
	text := dataTestR9Repeat(dataTestR9Pattern, 3584<<10)
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
			t.Fatalf("the pathological %d-byte %s failed closed after %s, want under 500ms",
				len(text), call.name, elapsed)
		}
		t.Logf("pathological %d-byte %s failed closed in %s", len(text), call.name, elapsed)
	}
}
