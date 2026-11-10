package plugin_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dataTestR9Plan installs a plan handler that returns text as the plan text and
// runs one Plan call.
func dataTestR9Plan(env *dataEnv, lease *appplugin.DataLease, text string) error {
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

// dataTestR9Summary installs a query handler that returns message as the
// summary message and runs one Query call.
func dataTestR9Summary(env *dataEnv, lease *appplugin.DataLease, message string) error {
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

// dataTestR9Env registers a provider whose single secret is secret and returns
// the environment plus an acquired read-only lease.
func dataTestR9Env(t *testing.T, secret string) (*dataEnv, *appplugin.DataLease) {
	t.Helper()
	draft := dataTestDraft()
	draft.Secrets = map[string]string{"password": secret}
	env := newDataEnv(t, domainplugin.DataModeReadOnly, appplugin.DataLimits{})
	env.saveProfile(draft)
	lease := env.acquire(domainplugin.DataModeReadOnly)
	t.Cleanup(func() { lease.Release() })
	return env, lease
}

// dataTestR9Token returns a raw standard base64 token whose decoded bytes are
// 0x00 followed by secret and the "xyzw" suffix from the round-8 reproduction
// and then "padding".
func dataTestR9Token(secret, suffix string) string {
	payload := append([]byte{0x00}, []byte(secret)...)
	payload = append(payload, []byte(suffix)...)
	return base64.RawStdEncoding.EncodeToString(payload)
}

// N1: the base64 token budget must never skip a token and deliver it. Text
// with 256 benign tokens followed by a token that decodes to a secret must
// fail closed, on both the plan and summary diagnostic paths.
func TestDataProviderRejectsBase64SecretBeyondTokenBudget(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)

	benign := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	if benign != "AAAAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("benign token = %q", benign)
	}
	secretToken := dataTestR9Token(secret, "xyzw")
	text := strings.Join(repeatDataTestToken(benign, 256), " ") + " " + secretToken

	dataTestCheckSecretEchoError(t, dataTestR9Plan(env, lease, text), secret, secretToken)
	dataTestCheckSecretEchoError(t, dataTestR9Summary(env, lease, text), secret, secretToken)

	// A control construction with the secret inside the budget is rejected by
	// the token scan itself, not by budget exhaustion.
	control := strings.Join(repeatDataTestToken(benign, 255), " ") + " " + secretToken
	dataTestCheckSecretEchoError(t, dataTestR9Plan(env, lease, control), secret, secretToken)

	// Benign text with a handful of base64-shaped words is still delivered.
	benignText := strings.Join(repeatDataTestToken(benign, 4), " ") +
		" query completed"
	if err := dataTestR9Plan(env, lease, benignText); err != nil {
		t.Fatalf("benign base64 tokens were rejected: %v", err)
	}
}

func repeatDataTestToken(token string, count int) []string {
	tokens := make([]string, count)
	for i := range tokens {
		tokens[i] = token
	}
	return tokens
}

// N2: a control byte or an unrelated non-alphabet byte inserted into a base64
// token must not split it beyond recognition. The split token is still one
// candidate after short non-alphabet gaps are joined. This must hold on every
// caller-visible path.
func TestDataProviderRejectsSplitBase64Secrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)
	token := dataTestR9Token(secret, "xyzw")
	if len(token) < 14 {
		t.Fatalf("token is only %d bytes", len(token))
	}

	splitBytes := []struct {
		name string
		b    byte
	}{
		{"nul", 0x00},
		{"control", 0x01},
		{"del", 0x7f},
		{"high", 0x80},
	}
	for _, split := range splitBytes {
		for _, position := range []int{8, 10, 12} {
			name := fmt.Sprintf("%s at %d", split.name, position)
			splitToken := token[:position] + string([]byte{split.b}) + token[position:]
			t.Run("summary "+name, func(t *testing.T) {
				dataTestCheckSecretEchoError(t,
					dataTestR9Summary(env, lease, "token "+splitToken+" rejected"),
					secret, splitToken)
			})
			t.Run("plan "+name, func(t *testing.T) {
				dataTestCheckSecretEchoError(t,
					dataTestR9Plan(env, lease, "plan token "+splitToken+" rejected"),
					secret, splitToken)
			})
			t.Run("message "+name, func(t *testing.T) {
				err := dataTestQueryStatus(env, lease, "denied",
					"failed token "+splitToken+" rejected")
				dataTestCheckSecretEchoError(t, err, secret, splitToken)
			})
			t.Run("identity "+name, func(t *testing.T) {
				dataTestOpenResponse(env, splitToken, "accepted")
				_, err := env.host.TestProfile(context.Background(), env.profile.ID)
				dataTestCheckSecretEchoError(t, err, secret, splitToken)
			})
		}
	}

	// A benign token split the same way is still delivered.
	benign := dataTestR9Token("warehouse-one", "xyzw")
	splitBenign := benign[:10] + "\x01" + benign[10:]
	if err := dataTestR9Plan(env, lease, "token "+splitBenign+" done"); err != nil {
		t.Fatalf("a benign split token was rejected: %v", err)
	}
}

// dataTestR9Pattern is the 23-form decode pattern from the round-8
// reproduction. Repeating it to a few MiB makes the escape/percent walk
// produce many multi-megabyte forms, which previously cost seconds of host CPU
// after the guest had already returned.
const dataTestR9Pattern = "947fA0cB2dbbfdc+aa0f%1fb7%d36%%\\fA40f\\\\fC4d0f585A75%%326dF+7%f1\\uc4"

// N3: a pathological multi-megabyte plan must fail closed with the constant
// protocol error instead of burning host CPU after the guest call returned.
func TestDataProviderRejectsPathologicalDiagnosticsQuickly(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)
	text := dataTestR9Repeat(dataTestR9Pattern, 3584<<10)

	started := time.Now()
	err := dataTestR9Plan(env, lease, text)
	elapsed := time.Since(started)
	dataTestCheckSecretEchoError(t, err, secret)
	// The work budget is what rejects this text (pinned directly in
	// TestDataSecretGuardChargesDecodeExpansionWork), so the ceiling only needs
	// to catch a return to multi-second decoding. It is deliberately generous:
	// a 150ms bound flaked at 163.8ms under full-package load, and adjacent
	// multi-megabyte tests can push GC work into this call.
	if !dataTestRaceEnabled && elapsed > 2*time.Second {
		t.Fatalf("pathological %d-byte plan was rejected after %s, want well under 2s",
			len(text), elapsed)
	}
	t.Logf("pathological %d-byte plan rejected in %s", len(text), elapsed)
}

func dataTestR9Repeat(pattern string, size int) string {
	var text strings.Builder
	text.Grow(size)
	for text.Len() < size {
		text.WriteString(pattern)
	}
	return text.String()[:size]
}

// R4: a secret split into four-byte chunks across sibling plan node labels and
// details is invisible to each per-field scan; the concatenated diagnostic
// text is scanned as well.
func TestDataProviderRejectsCrossNodeSplitPlanSecrets(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method != domainpluginabi.DataProviderMethodPlan {
			return dataOK(nil), nil
		}
		return dataOK(map[string]any{"result": domainplugin.DataResult{
			Kind:  domainplugin.DataResultPlan,
			Class: domainplugin.DataClassRead,
			Plan: &domainplugin.DataPlan{Nodes: []domainplugin.DataPlanNode{
				{Label: secret[0:3], Detail: secret[3:6]},
				{Label: secret[6:9], Detail: secret[9:12]},
				{Label: secret[12:16]},
			}},
		}}), nil
	}
	_, err := lease.Plan(context.Background(), "select 1", nil)
	dataTestCheckSecretEchoError(t, err, secret)

	// The same shape with unrelated text is delivered.
	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method != domainpluginabi.DataProviderMethodPlan {
			return dataOK(nil), nil
		}
		return dataOK(map[string]any{"result": domainplugin.DataResult{
			Kind:  domainplugin.DataResultPlan,
			Class: domainplugin.DataClassRead,
			Plan: &domainplugin.DataPlan{Nodes: []domainplugin.DataPlanNode{
				{Label: "scan", Detail: "orders"},
				{Label: "sort", Detail: "by id"},
			}},
		}}), nil
	}
	if _, err := lease.Plan(context.Background(), "select 1", nil); err != nil {
		t.Fatalf("a benign multi-node plan was rejected: %v", err)
	}
}

// R5: case folding must not map invalid UTF-8 bytes to U+FFFD, because that
// makes unrelated binary payloads look like each other. A binary secret must
// not reject a base64 spelling of different binary data.
func TestDataProviderDeliversBenignBinaryBase64(t *testing.T) {
	// The two payloads fold to the same string under a lossy whole-string
	// ToLower (both invalid tails become U+FFFD) but share no secret bytes.
	secret := string([]byte{0x01, 0x02, 0x03, 0xff})
	benign := []byte{0x01, 0x02, 0x03, 0xfe}
	env, lease := dataTestR9Env(t, secret)

	token := base64.StdEncoding.EncodeToString(benign)
	for _, call := range []struct {
		name string
		run  func() error
	}{
		{"plan", func() error {
			return dataTestR9Plan(env, lease, "plan token "+token+" done")
		}},
		{"summary", func() error {
			return dataTestR9Summary(env, lease, "summary token "+token+" done")
		}},
	} {
		if err := call.run(); err != nil {
			t.Fatalf("benign binary base64 %s was rejected: %v", call.name, err)
		}
	}

	// The binary secret itself is still rejected in its base64 form. The raw
	// invalid byte cannot survive the JSON transport, so the encoded spelling is
	// the caller-reachable form.
	encodedSecret := base64.StdEncoding.EncodeToString([]byte(secret))
	dataTestCheckSecretEchoError(t,
		dataTestR9Plan(env, lease, "plan token "+encodedSecret+" done"),
		secret, encodedSecret)
}

// R1: a base64 token whose decoded form is larger than the old per-token
// quarantine (4096 bytes) must not be skipped and delivered. The secret is
// placed at an unaligned offset so the token does not contain a precomputed
// base64 spelling of the secret.
func TestDataProviderRejectsOversizedDecodedBase64Token(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)
	makeToken := func(decoded int) string {
		payload := append([]byte{0x00}, []byte(secret)...)
		payload = append(payload, make([]byte, decoded-len(payload))...)
		return base64.RawStdEncoding.EncodeToString(payload)
	}
	for _, decoded := range []int{4096, 4097, 4200} {
		token := makeToken(decoded)
		dataTestCheckSecretEchoError(t,
			dataTestR9Plan(env, lease, "plan token "+token+" done"), secret, token)
	}
	// A benign token of the same size is decoded and delivered.
	benignPayload := make([]byte, 4200)
	for i := range benignPayload {
		benignPayload[i] = 'x'
	}
	benign := base64.RawStdEncoding.EncodeToString(benignPayload)
	if err := dataTestR9Plan(env, lease, "plan token "+benign+" done"); err != nil {
		t.Fatalf("a benign large decoded token was rejected: %v", err)
	}
}

// R2: nested base64 is followed beyond three layers; a secret five layers deep
// is still caught, and nesting past the documented cap fails closed rather than
// being silently delivered.
func TestDataProviderRejectsDeeplyNestedBase64(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)
	nest := func(levels int) string {
		text := secret
		for level := 0; level < levels; level++ {
			text = base64.RawStdEncoding.EncodeToString([]byte(text))
		}
		return text
	}
	for _, levels := range []int{5, 6, 7, 8, 10, 12, 16, 20} {
		token := nest(levels)
		dataTestCheckSecretEchoError(t,
			dataTestR9Plan(env, lease, "plan token "+token+" done"), secret, token)
	}
	// A benign nesting inside the cap decodes to plain text and is delivered.
	// The hard depth cap and its fail-closed behavior are pinned by the
	// internal guard test, which can reach the cap directly. The control stays
	// short because the ordered-subsequence rule is deliberately aggressive on
	// arbitrary provider text: a 16-byte secret is an ordered subsequence of
	// most several-hundred-byte base64 strings, so a heavily nested control
	// would trip that rule independently of the nesting depth under test.
	benign := "scan orders"
	for level := 0; level < 10; level++ {
		benign = base64.RawStdEncoding.EncodeToString([]byte(benign))
	}
	if err := dataTestR9Plan(env, lease, "plan token "+benign+" done"); err != nil {
		t.Fatalf("a benign deeply nested token was rejected: %v", err)
	}
}

// Benign large diagnostics must still be delivered: the work budget is for
// pathological text, not for ordinary large plans.
func TestDataProviderDeliversBenignLargeDiagnostics(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)

	var text strings.Builder
	for text.Len() < 2<<20 {
		text.WriteString("sequential scan of orders with index lookup and hash join ")
	}
	plan := text.String()[:2<<20]
	if err := dataTestR9Plan(env, lease, plan); err != nil {
		t.Fatalf("%d-byte benign plan was rejected: %v", len(plan), err)
	}
	if err := dataTestR9Summary(env, lease, plan); err != nil {
		t.Fatalf("%d-byte benign summary was rejected: %v", len(plan), err)
	}
}
