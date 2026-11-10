package plugin_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dataTestOpenResponse installs an open handler that reports the supplied
// identity and auth verdict. Every other method succeeds.
func dataTestOpenResponse(env *dataEnv, identity, auth string) {
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method != domainpluginabi.DataProviderMethodOpen {
			return dataOK(nil), nil
		}
		return json.Marshal(map[string]any{
			"status": "ok", "serverIdentity": identity, "authResult": auth,
		})
	}
}

func dataTestInterleave(value, separator string) string {
	return strings.Join(strings.Split(value, ""), separator)
}

// A provider can make a secret look like a server identity by putting dots
// between its bytes. The identity is a valid DNS name, so shape validation
// alone accepts it. The guard normalizes the separator class and scans the
// exact identity the caller would receive; a discarded authResult, however
// large, cannot switch that scan off.
func TestDataProfileTestRejectsSeparatorInterleavedIdentities(t *testing.T) {
	longSecret := "qvlxrmptwzbdjfhn"
	shortSecret := "hunter2"
	draft := dataTestDraft()
	draft.Secrets["password"] = longSecret
	draft.Secrets["token"] = shortSecret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)

	oversized := strings.Repeat("junk", 1250) // 5000 bytes, discarded by the verdict mapping
	checkRejected := func(name, identity, auth string) {
		t.Helper()
		dataTestOpenResponse(env, identity, auth)
		result, err := env.host.TestProfile(context.Background(), env.profile.ID)
		if err == nil {
			t.Fatalf("%s was accepted as %+v", name, result)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		for _, secret := range []string{longSecret, shortSecret} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s leaked a secret: %v", name, err)
			}
		}
	}

	checkRejected("dotted long identity", dataTestInterleave(longSecret, "."), "accepted")
	checkRejected("dashed long identity", dataTestInterleave(longSecret, "-"), "accepted")
	checkRejected("underscored long identity", dataTestInterleave(longSecret, "_"), "accepted")
	checkRejected("dotted short identity with oversized discarded auth",
		dataTestInterleave(shortSecret, "."), oversized)
	checkRejected("mixed short identity", dataTestInterleave(shortSecret, "._-"), "accepted")
	escaped := strings.ReplaceAll(dataTestInterleave(shortSecret, "."), ".", "\\u002e")
	checkRejected("escaped dotted short identity", escaped, oversized)

	// Benign names and verdicts still round-trip; the separator rule must not
	// reject ordinary identities.
	for _, tc := range []struct{ identity, auth, wantAuth string }{
		{"warehouse-1", "accepted", "accepted"},
		{"db-1.example.test", "REJECTED", "rejected"},
	} {
		dataTestOpenResponse(env, tc.identity, tc.auth)
		result, err := env.host.TestProfile(context.Background(), env.profile.ID)
		if err != nil {
			t.Fatalf("benign identity %q: %v", tc.identity, err)
		}
		if result.ServerIdentity != tc.identity || result.AuthResult != tc.wantAuth {
			t.Fatalf("benign result = %+v", result)
		}
	}
}

// Caller-visible provider messages go through the same normalized scan: a
// denied status whose message spells the secret as a dotted name is replaced
// by the constant protocol error, as is an invoker failure that carries the
// same text.
func TestDataProviderMessagesRejectSeparatorInterleavedSecrets(t *testing.T) {
	longSecret := "qvlxrmptwzbdjfhn"
	shortSecret := "hunter2"
	draft := dataTestDraft()
	draft.Secrets["password"] = longSecret
	draft.Secrets["token"] = shortSecret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	query := func(message string) error {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodQuery {
				return dataStatus("denied", message), nil
			}
			return dataOK(nil), nil
		}
		_, _, err := lease.Query(context.Background(),
			appplugin.DataQueryRequest{Query: "blocked"})
		return err
	}
	checkRejected := func(name, message string) {
		t.Helper()
		err := query(message)
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		for _, secret := range []string{longSecret, shortSecret} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("%s leaked a secret: %v", name, err)
			}
		}
	}

	checkRejected("dotted long status message", "denied: "+dataTestInterleave(longSecret, "."))
	checkRejected("dotted short status message",
		"provided "+dataTestInterleave(shortSecret, ".")+" was rejected")
	checkRejected("dashed long status message", dataTestInterleave(longSecret, "-"))
	checkRejected("underscored short status message", dataTestInterleave(shortSecret, "_"))
	escaped := strings.ReplaceAll(dataTestInterleave(shortSecret, "."), ".", "\\u002e")
	checkRejected("escaped dotted short status message", escaped)

	// A provider error surfaced as an invoker failure is caller-visible too.
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodQuery {
			return nil, errors.New("dial failed at " + dataTestInterleave(longSecret, "."))
		}
		return dataOK(nil), nil
	}
	_, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "blocked"})
	if err == nil {
		t.Fatal("the invoker failure was accepted")
	}
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if strings.Contains(err.Error(), longSecret) {
		t.Fatalf("the invoker failure leaked a secret: %v", err)
	}

	// Control: an ordinary provider message is still delivered with its kind.
	err = query("permission denied for table orders")
	wantDataKind(t, err, appplugin.DataErrorDenied)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != "permission denied for table orders" {
		t.Fatalf("benign message = %v", err)
	}
}

// dataTestMixedCaseText alternates the case of ASCII letters.
func dataTestMixedCaseText(text string) string {
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

// dataTestReversedText returns text with its characters in reverse order.
func dataTestReversedText(text string) string {
	runes := []rune(text)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// dataTestEscapedText hides text behind levels escape layers: one unicode
// escape pass (Go/JSON \uXXXX) plus one backslash-doubling pass per further
// layer.
func dataTestEscapedText(text string, levels int) string {
	var escaped strings.Builder
	for _, r := range text {
		fmt.Fprintf(&escaped, `\u%04x`, r)
	}
	escapedText := escaped.String()
	for level := 1; level < levels; level++ {
		escapedText = strings.ReplaceAll(escapedText, `\`, `\\`)
	}
	return escapedText
}

// A provider can return a secret as a server identity after case-folding it,
// reversing it, or both. Each spelling is a valid DNS name, so shape
// validation alone accepts it; the guard scans the exact identity the caller
// would receive.
func TestDataProfileTestRejectsCaseFoldedAndReversedIdentities(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	reversed := dataTestReversedText(secret)
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)

	checkRejected := func(name, identity string) {
		t.Helper()
		dataTestOpenResponse(env, identity, "accepted")
		result, err := env.host.TestProfile(context.Background(), env.profile.ID)
		if err == nil {
			t.Fatalf("%s was accepted as %+v", name, result)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		if !strings.Contains(err.Error(), "echoed secret content") {
			t.Fatalf("%s error = %v", name, err)
		}
		for _, leak := range []string{
			secret, reversed,
			strings.ToUpper(secret), strings.ToUpper(reversed),
			dataTestEscapedText(secret, 3),
		} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("%s leaked a secret: %v", name, err)
			}
		}
	}

	checkRejected("upper full identity", strings.ToUpper(secret))
	checkRejected("mixed full identity", dataTestMixedCaseText(secret))
	checkRejected("reversed identity", reversed)
	checkRejected("upper reversed identity", strings.ToUpper(reversed))
	checkRejected("mixed reversed identity", dataTestMixedCaseText(reversed))
	checkRejected("upper prefix identity", strings.ToUpper(secret[:8]))
	checkRejected("reversed prefix identity", dataTestReversedText(secret[:8]))
	checkRejected("dotted upper identity", dataTestInterleave(strings.ToUpper(secret), "."))
	checkRejected("dashed reversed identity", dataTestInterleave(reversed, "-"))

	// A legitimate uppercase host name is not a secret and still round-trips
	// with its exact spelling.
	for _, identity := range []string{"DB-1.EXAMPLE.TEST", "WAREHOUSE-1"} {
		dataTestOpenResponse(env, identity, "ACCEPTED")
		result, err := env.host.TestProfile(context.Background(), env.profile.ID)
		if err != nil {
			t.Fatalf("benign identity %q: %v", identity, err)
		}
		if result.ServerIdentity != identity || result.AuthResult != "accepted" {
			t.Fatalf("benign result = %+v", result)
		}
	}
}

// Caller-visible provider messages go through the same case-folded and
// reversal-aware scan: a denied status message and an invoker failure that
// spell the secret in another case, backwards, or behind several escape
// layers are replaced by the constant protocol error instead of delivered.
func TestDataProviderMessagesRejectCaseFoldedAndReversedSecrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	reversed := dataTestReversedText(secret)
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	query := func(message string) error {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodQuery {
				return dataStatus("denied", message), nil
			}
			return dataOK(nil), nil
		}
		_, _, err := lease.Query(context.Background(),
			appplugin.DataQueryRequest{Query: "blocked"})
		return err
	}
	checkRejected := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s was accepted", name)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		if !strings.Contains(err.Error(), "echoed secret content") {
			t.Fatalf("%s error = %v", name, err)
		}
		for _, leak := range []string{
			secret, reversed,
			strings.ToUpper(secret), strings.ToUpper(reversed),
			dataTestEscapedText(secret, 4),
		} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("%s leaked a secret: %v", name, err)
			}
		}
	}

	checkRejected("upper status message",
		query("dial failed at "+strings.ToUpper(secret)))
	checkRejected("reversed status message",
		query("dial failed at "+reversed))
	checkRejected("upper reversed status message",
		query("dial failed at "+strings.ToUpper(reversed)))
	checkRejected("mixed status message",
		query("dial failed at "+dataTestMixedCaseText(secret)))
	checkRejected("dotted upper status message",
		query("dial failed at "+dataTestInterleave(strings.ToUpper(secret), ".")))
	checkRejected("reversed prefix status message",
		query("dial failed at "+dataTestReversedText(secret[:8])))
	checkRejected("escaped status message",
		query("dial failed at "+dataTestEscapedText(secret, 4)))

	// A provider error surfaced as an invoker failure is caller-visible too.
	invoke := func(message string) error {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodQuery {
				return nil, errors.New(message)
			}
			return dataOK(nil), nil
		}
		_, _, err := lease.Query(context.Background(),
			appplugin.DataQueryRequest{Query: "blocked"})
		return err
	}
	checkRejected("upper invoker failure",
		invoke("dial failed at "+strings.ToUpper(secret)))
	checkRejected("reversed invoker failure",
		invoke("dial failed at "+reversed))
	checkRejected("escaped invoker failure",
		invoke("dial failed at "+dataTestEscapedText(secret, 4)))

	// Controls: ordinary messages keep their kind and exact text.
	err := query("permission denied for table orders")
	wantDataKind(t, err, appplugin.DataErrorDenied)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != "permission denied for table orders" {
		t.Fatalf("benign message = %v", err)
	}
	err = invoke("dial failed at DB-1.EXAMPLE.TEST")
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if !strings.Contains(err.Error(), "dial failed at DB-1.EXAMPLE.TEST") {
		t.Fatalf("benign invoker failure = %v", err)
	}
}

// dataTestFullwidthText maps ASCII letters, digits, and punctuation to their
// fullwidth compatibility forms.
func dataTestFullwidthText(text string) string {
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

// dataTestPercentEncodeMixedCase percent-encodes every byte, alternating lower
// and upper hex digits.
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

// dataTestBase64Encodings returns the four base64 spellings of value.
func dataTestBase64Encodings(value string) []string {
	raw := []byte(value)
	return []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw),
		base64.URLEncoding.EncodeToString(raw),
		base64.RawURLEncoding.EncodeToString(raw),
	}
}

// dataTestCheckSecretEchoError asserts a caller-visible failure is the constant
// protocol error and carries neither the secret nor the supplied spelling.
func dataTestCheckSecretEchoError(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("the secret echo was accepted")
	}
	wantDataKind(t, err, appplugin.DataErrorProtocol)
	if !strings.Contains(err.Error(), "echoed secret content") {
		t.Fatalf("error does not name the echo guard: %v", err)
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked secret content %q: %v", secret, err)
		}
	}
}

// dataTestQueryStatus installs a query handler that answers with status and
// message, and returns the caller-visible error from one query.
func dataTestQueryStatus(env *dataEnv, lease *appplugin.DataLease,
	status, message string) error {

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodQuery {
			return dataStatus(status, message), nil
		}
		return dataOK(nil), nil
	}
	_, _, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "blocked"})
	return err
}

// A provider can percent-encode a secret with reserved characters so it looks
// like an ordinary URL or error detail. The denied status message must be
// replaced with the constant protocol error, while percent-encoded ordinary
// text still round-trips exactly.
func TestDataProviderMessagesRejectPercentEncodedSecrets(t *testing.T) {
	secret := "p@ss/w:rd x+y"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	cases := []struct{ name, message string }{
		{"query escape", "failed at " + url.QueryEscape(secret)},
		{"path escape", "failed at " + url.PathEscape(secret)},
		{"double query escape", "failed at " + url.QueryEscape(url.QueryEscape(secret))},
		{"mixed hex case", "failed at " + dataTestPercentEncodeMixedCase(secret)},
		{"malformed prefix", "%zz" + url.QueryEscape(secret) + "%2"},
		{"case folded", "failed at " + url.QueryEscape(strings.ToUpper(secret))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := dataTestQueryStatus(env, lease, "denied", tc.message)
			dataTestCheckSecretEchoError(t, err, secret, tc.message)
		})
	}

	benign := url.QueryEscape("permission denied for table orders")
	err := dataTestQueryStatus(env, lease, "denied", benign)
	wantDataKind(t, err, appplugin.DataErrorDenied)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != benign {
		t.Fatalf("benign percent-encoded message = %v", err)
	}
}

// A provider can return the base64 spelling of a secret, padded or not, in
// either alphabet, as a denied message. The token is decoded and rejected;
// benign base64 of unrelated text round-trips.
func TestDataProviderMessagesRejectBase64Secrets(t *testing.T) {
	secret := "p@ss/w:rd x+y"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	encodings := dataTestBase64Encodings(secret)
	cases := make([]struct{ name, message string }, 0, len(encodings)+4)
	for index, encoded := range encodings {
		cases = append(cases, struct{ name, message string }{
			fmt.Sprintf("variant %d", index), "failed token " + encoded + " rejected",
		})
	}
	cases = append(cases,
		struct{ name, message string }{
			"reversed token", "failed token " + dataTestReversedText(encodings[0]),
		},
		struct{ name, message string }{
			"base64 of reversed secret", "failed token " +
				base64.StdEncoding.EncodeToString([]byte(dataTestReversedText(secret))),
		},
		struct{ name, message string }{
			"percent-encoded base64", url.QueryEscape(encodings[0]),
		},
	)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := dataTestQueryStatus(env, lease, "denied", tc.message)
			dataTestCheckSecretEchoError(t, err, secret, tc.message)
		})
	}

	benign := base64.StdEncoding.EncodeToString([]byte("permission denied for table orders"))
	err := dataTestQueryStatus(env, lease, "denied", benign)
	wantDataKind(t, err, appplugin.DataErrorDenied)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != benign {
		t.Fatalf("benign base64 message = %v", err)
	}
}

// A provider can return a secret in an equivalent Unicode spelling: fullwidth
// forms, NFD, the sharp-s substitution, and the named ligature/stroke
// substitutions. The visible message must become the constant protocol error,
// and ordinary Unicode text must round-trip exactly.
func TestDataProviderMessagesRejectUnicodeFoldedSecrets(t *testing.T) {
	cases := []struct{ name, secret, message string }{
		{"fullwidth", "qvlxrmptwzbdjfhn",
			"failed at " + dataTestFullwidthText("qvlxrmptwzbdjfhn")},
		{"fullwidth upper", "qvlxrmptwzbdjfhn",
			"failed at " + strings.ToUpper(dataTestFullwidthText("qvlxrmptwzbdjfhn"))},
		{"nfd", "café-secret", "failed at café-secret"},
		{"sharp s", "straße-geheim", "denied: STRASSE-GEHEIM"},
		{"ae ligature", "æblegrød", "denied: aeblegrod"},
		{"oe ligature", "cœur-de-lion", "denied: coeur-de-lion"},
		{"o stroke", "smørrebrød", "denied: smorrebrod"},
		{"d stroke", "Đorđe", "denied: DORDE"},
		{"l stroke", "Łódź", "denied: LODZ"},
		{"thorn", "þorn", "denied: THORN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft := dataTestDraft()
			draft.Secrets["password"] = tc.secret
			env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
			env.saveProfile(draft)
			lease := env.acquire(domainplugin.DataModeReadOnly)
			defer lease.Release()

			err := dataTestQueryStatus(env, lease, "denied", tc.message)
			dataTestCheckSecretEchoError(t, err, tc.secret, tc.message)
		})
	}

	secret := "qvlxrmptwzbdjfhn"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()
	benign := "café menu unavailable"
	err := dataTestQueryStatus(env, lease, "denied", benign)
	wantDataKind(t, err, appplugin.DataErrorDenied)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != benign {
		t.Fatalf("benign Unicode message = %v", err)
	}
}

// A base64 encoding of a short ASCII secret is itself a valid DNS name, the
// exact shape a ServerIdentity accepts. Profile test must still quarantine it.
func TestDataProfileTestRejectsBase64Identities(t *testing.T) {
	secret := "aaaaaaaaaaaa"
	raw := []byte(secret)
	encodings := map[string]string{
		"std padded":   base64.StdEncoding.EncodeToString(raw),
		"raw std":      base64.RawStdEncoding.EncodeToString(raw),
		"url padded":   base64.URLEncoding.EncodeToString(raw),
		"raw url-safe": base64.RawURLEncoding.EncodeToString(raw),
	}
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)

	for name, encoded := range encodings {
		t.Run(name, func(t *testing.T) {
			dataTestOpenResponse(env, encoded, "accepted")
			result, err := env.host.TestProfile(context.Background(), env.profile.ID)
			if err == nil {
				t.Fatalf("base64 identity was accepted as %+v", result)
			}
			dataTestCheckSecretEchoError(t, err, secret, encoded)
		})
	}
}

// Plan text and summary messages are host-visible diagnostic text, so both go
// through the same guard. Table cells and other provider-owned result payloads
// remain outside echo protection by contract.
func TestDataProviderGuardsPlanAndSummaryDiagnostics(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	queryResult := func(result domainplugin.DataResult) error {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodQuery {
				return dataOK(map[string]any{"result": result}), nil
			}
			return dataOK(nil), nil
		}
		_, _, err := lease.Query(context.Background(),
			appplugin.DataQueryRequest{Query: "blocked"})
		return err
	}
	planResult := func(result domainplugin.DataResult) (domainplugin.DataResult, error) {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodPlan {
				return dataOK(map[string]any{"result": result}), nil
			}
			return dataOK(nil), nil
		}
		return lease.Plan(context.Background(), "select 1", nil)
	}

	rejections := []struct {
		name string
		call func() error
	}{
		{"summary message", func() error {
			return queryResult(domainplugin.DataResult{
				Kind:    domainplugin.DataResultSummary,
				Class:   domainplugin.DataClassRead,
				Summary: &domainplugin.DataSummary{Message: secret},
			})
		}},
		{"plan text", func() error {
			_, err := planResult(domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan:  &domainplugin.DataPlan{Text: secret},
			})
			return err
		}},
		{"plan node label", func() error {
			_, err := planResult(domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan: &domainplugin.DataPlan{Nodes: []domainplugin.DataPlanNode{
					{Label: secret},
				}},
			})
			return err
		}},
		{"plan node detail", func() error {
			_, err := planResult(domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan: &domainplugin.DataPlan{Nodes: []domainplugin.DataPlanNode{
					{Label: "scan", Detail: secret},
				}},
			})
			return err
		}},
	}
	for _, tc := range rejections {
		t.Run(tc.name, func(t *testing.T) {
			dataTestCheckSecretEchoError(t, tc.call(), secret)
		})
	}

	// Benign diagnostic text round-trips exactly.
	summaryMessage := "query completed in 12 ms"
	_, query, err := func() (*appplugin.DataQuery, domainplugin.DataResult, error) {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodQuery {
				return dataOK(map[string]any{"result": domainplugin.DataResult{
					Kind:    domainplugin.DataResultSummary,
					Class:   domainplugin.DataClassRead,
					Summary: &domainplugin.DataSummary{Message: summaryMessage},
				}}), nil
			}
			return dataOK(nil), nil
		}
		return lease.Query(context.Background(), appplugin.DataQueryRequest{Query: "q"})
	}()
	if err != nil || query.Summary == nil || query.Summary.Message != summaryMessage {
		t.Fatalf("benign summary = %+v, %v", query, err)
	}
	plan, err := planResult(domainplugin.DataResult{
		Kind:  domainplugin.DataResultPlan,
		Class: domainplugin.DataClassRead,
		Plan: &domainplugin.DataPlan{Nodes: []domainplugin.DataPlanNode{
			{Label: "scan", Detail: "sequential scan of orders"},
		}},
	})
	if err != nil || plan.Plan == nil || plan.Plan.Nodes[0].Detail != "sequential scan of orders" {
		t.Fatalf("benign plan = %+v, %v", plan, err)
	}
}

// dataTestPercentChain percent-encodes text once per layer, alternating lower
// and upper hex digits on every pass.
func dataTestPercentChain(text string, layers int) string {
	for layer := 0; layer < layers; layer++ {
		text = dataTestPercentEncodeMixedCase(text)
	}
	return text
}

// dataTestEscapeGadget returns a decoy whose escape decoding stays live for
// layers rounds: 2^(layers-1) backslashes halve once per pass and then decode
// to "A".
func dataTestEscapeGadget(layers int) string {
	if layers < 1 {
		return ""
	}
	return strings.Repeat(`\`, 1<<(layers-1)) + "u0041"
}

// dataTestPathEscapedProbe is a 16-byte secret whose first eight bytes hold a
// literal "+" and an escaped "/", and whose last eight bytes hold an escaped
// "?". url.PathEscape leaves the plus literal and escapes bytes in both halves,
// so neither half is a raw substring of the escaped text and query-only percent
// decoding loses the value.
const dataTestPathEscapedProbe = "+a/12345wxy?9+BC"

// A provider can treat a secret as a URL path segment. url.PathEscape leaves a
// literal "+" alone while escaping the reserved bytes around it, so a decoder
// that maps every "+" to a space no longer reconstructs the value. The denied
// status message, the plan text, and the test identity must each fail closed
// with the constant protocol error.
func TestDataProviderRejectsPathEscapedPlusSecrets(t *testing.T) {
	secret := dataTestPathEscapedProbe
	escaped := url.PathEscape(secret)
	if strings.Contains(escaped, secret[:8]) {
		t.Fatal("the path-escaped probe exposes its leading fragment")
	}
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	// Denied status message.
	err := dataTestQueryStatus(env, lease, "denied", "failed at "+escaped)
	dataTestCheckSecretEchoError(t, err, secret, escaped)

	// Plan text.
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodPlan {
			return dataOK(map[string]any{"result": domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan:  &domainplugin.DataPlan{Text: "plan at " + escaped},
			}}), nil
		}
		return dataOK(nil), nil
	}
	_, err = lease.Plan(context.Background(), "select 1", nil)
	dataTestCheckSecretEchoError(t, err, secret, escaped)

	// Profile-test server identity. The escaped value fails the DNS shape, so
	// the echo guard must reject it before shape validation and return the echo
	// error rather than the shape error.
	dataTestOpenResponse(env, escaped, "accepted")
	_, err = env.host.TestProfile(context.Background(), env.profile.ID)
	dataTestCheckSecretEchoError(t, err, secret, escaped)

	// Controls: benign path-escaped text still round-trips.
	benign := url.PathEscape("permission denied for table orders")
	err = dataTestQueryStatus(env, lease, "denied", benign)
	wantDataKind(t, err, appplugin.DataErrorDenied)
	var dataErr *appplugin.DataError
	if !errors.As(err, &dataErr) || dataErr.Message != benign {
		t.Fatalf("benign path-escaped message = %v", err)
	}
}

// A deep percent chain must be decoded to its fixed point even when decoy
// escape and query/path gadgets keep unrelated branches alive. The breadth of
// those decoys must not consume the chain's depth budget, on any caller-visible
// surface.
func TestDataProviderRejectsDeepPercentChainsPastDecoyGadgets(t *testing.T) {
	decoy := dataTestEscapeGadget(6) + "%5C" + dataTestEscapeGadget(4) + "+DIV+"
	// Denied messages are bounded to 256 bytes before the scan, so use the
	// short-secret reproducer that fits: 1- and 2-byte secrets behind a
	// 4-layer mixed-case percent chain.
	for _, tc := range []struct{ name, secret string }{
		{"one byte", "Z"},
		{"two bytes", "Zq"},
	} {
		t.Run("message "+tc.name, func(t *testing.T) {
			draft := dataTestDraft()
			draft.Secrets["password"] = tc.secret
			env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
			env.saveProfile(draft)
			lease := env.acquire(domainplugin.DataModeReadOnly)
			defer lease.Release()

			for _, layers := range []int{1, 4} {
				message := decoy + dataTestPercentChain(tc.secret, layers)
				if len(message) > 256 {
					t.Fatalf("the %d-layer message is %d bytes and would be truncated",
						layers, len(message))
				}
				err := dataTestQueryStatus(env, lease, "denied", message)
				dataTestCheckSecretEchoError(t, err, tc.secret, message)
			}
		})
	}

	// Plan text and server identity have no display bound, so use the 16-byte
	// secret behind a 7-layer chain (~35 KB) from the reproduction.
	secret := "qvlxrmptwzbdjfhn"
	chain := dataTestPercentChain(secret, 7)
	if len(chain) < 30_000 {
		t.Fatalf("the 7-layer chain is only %d bytes", len(chain))
	}
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	// Plan text.
	planText := dataTestEscapeGadget(8) + "%5C" + dataTestEscapeGadget(4) + "+DIV+" + chain
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodPlan {
			return dataOK(map[string]any{"result": domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan:  &domainplugin.DataPlan{Text: planText},
			}}), nil
		}
		return dataOK(nil), nil
	}
	_, err := lease.Plan(context.Background(), "select 1", nil)
	dataTestCheckSecretEchoError(t, err, secret, chain)

	// Profile-test identity.
	dataTestOpenResponse(env, planText, "accepted")
	_, err = env.host.TestProfile(context.Background(), env.profile.ID)
	dataTestCheckSecretEchoError(t, err, secret, chain)
}

// dataTestScatteredText separates every byte of value with filler, so value is
// only an ordered subsequence, never a contiguous run.
func dataTestScatteredText(value, filler string) string {
	var scattered strings.Builder
	for i := 0; i < len(value); i++ {
		scattered.WriteString(filler)
		scattered.WriteByte(value[i])
	}
	return scattered.String()
}

// Long diagnostic text is still guarded: a >4096-byte plan or summary message
// that interleaves the secret with spaces or punctuation must be rejected, and
// benign long text that merely scatters the same bytes beyond the anchored
// window is delivered.
func TestDataProviderGuardsLongPlanDiagnostics(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	planResult := func(text string) error {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodPlan {
				return dataOK(map[string]any{"result": domainplugin.DataResult{
					Kind:  domainplugin.DataResultPlan,
					Class: domainplugin.DataClassRead,
					Plan:  &domainplugin.DataPlan{Text: text},
				}}), nil
			}
			return dataOK(nil), nil
		}
		_, err := lease.Plan(context.Background(), "select 1", nil)
		return err
	}
	summaryResult := func(message string) error {
		env.server.handler = func(request appplugin.DataProviderRequest,
			_ context.Context) ([]byte, error) {

			if request.Method == domainpluginabi.DataProviderMethodQuery {
				return dataOK(map[string]any{"result": domainplugin.DataResult{
					Kind:    domainplugin.DataResultSummary,
					Class:   domainplugin.DataClassRead,
					Summary: &domainplugin.DataSummary{Message: message},
				}}), nil
			}
			return dataOK(nil), nil
		}
		_, _, err := lease.Query(context.Background(),
			appplugin.DataQueryRequest{Query: "select 1"})
		return err
	}

	// The host's long-text subsequence threshold is 4096 bytes.
	padding := strings.Repeat("plan detail ", 600)
	if len(padding) <= 4096 {
		t.Fatal("the padding is not long")
	}
	interleaved := padding + dataTestInterleave(secret, " ")
	dataTestCheckSecretEchoError(t, planResult(interleaved), secret)
	dataTestCheckSecretEchoError(t, summaryResult(interleaved), secret)
	punctuated := padding + dataTestInterleave(secret, "|")
	dataTestCheckSecretEchoError(t, planResult(punctuated), secret)

	// Benign long text whose secret bytes are spread wider than the anchored
	// window is still delivered.
	benign := padding + dataTestScatteredText(secret, strings.Repeat("q", 700))
	if err := planResult(benign); err != nil {
		t.Fatalf("benign long plan was rejected: %v", err)
	}
}

// Opaque provider result payloads are outside secret-echo protection: a
// provider owns the rows it returns and can always return them in the clear,
// so scanning them would add cost without changing the trust boundary. This
// test pins the documented split against the guarded diagnostic fields above.
func TestDataProviderOpaqueResultPayloadsAreOutsideSecretEchoProtection(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	draft := dataTestDraft()
	draft.Secrets["password"] = secret
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	defer lease.Release()

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method != domainpluginabi.DataProviderMethodQuery {
			return dataOK(nil), nil
		}
		return dataOK(map[string]any{"result": domainplugin.DataResult{
			Kind:  domainplugin.DataResultTable,
			Class: domainplugin.DataClassRead,
			Table: &domainplugin.DataTable{
				Columns: []domainplugin.DataColumn{{
					Name: "value", Type: domainplugin.DataValueString,
				}},
				Rows: [][]domainplugin.DataValue{{
					{Kind: domainplugin.DataValueString, String: secret},
				}},
			},
		}}), nil
	}
	_, result, err := lease.Query(context.Background(),
		appplugin.DataQueryRequest{Query: "select value"})
	if err != nil {
		t.Fatalf("opaque payload was rejected: %v", err)
	}
	if result.Table == nil || len(result.Table.Rows) != 1 ||
		result.Table.Rows[0][0].String != secret {
		t.Fatalf("opaque payload = %+v", result)
	}
}
