package plugin_test

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dataTestR14BenignCases mirrors the internal D1 availability matrix. Every
// shape is a benign diagnostic that must be delivered through the public plan
// and summary paths, not just the guard, in under the 300 ms availability
// budget.
func dataTestR14BenignCases() []struct {
	name    string
	size    int
	unit    string
	secrets map[string]string
} {
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
	const sha256Unit = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa "
	const size = 4190208

	secrets2 := map[string]string{"password": secret, "token": second}
	secrets3 := map[string]string{"password": secret, "token": second, "other": third}
	rng := rand.New(rand.NewSource(0x14b64))
	var varying strings.Builder
	for varying.Len() < size {
		n := 16 + rng.Intn(65)
		for i := 0; i < n; i++ {
			varying.WriteByte(base64Alphabet[rng.Intn(len(base64Alphabet))])
		}
		varying.WriteByte(' ')
	}

	return []struct {
		name    string
		size    int
		unit    string
		secrets map[string]string
	}{
		{"lowercase", size, unit, map[string]string{"password": secret}},
		{"uppercase", size, strings.ToUpper(unit), map[string]string{"password": secret}},
		{"alternating case", size, dataTestR12MixedCaseUnit(unit), map[string]string{"password": secret}},
		{"public qualifier", size, publicUnit, map[string]string{"password": secret}},
		{"mixed-case public qualifier", size, dataTestR12MixedCaseUnit(publicUnit), map[string]string{"password": secret}},
		{"plus signs", size, plusUnit, map[string]string{"password": secret}},
		{"plus and public", size, plusPublic, map[string]string{"password": secret}},
		{"plus uppercase", size, strings.ToUpper(plusUnit), map[string]string{"password": secret}},
		{"plus mixed case", size, dataTestR12MixedCaseUnit(plusUnit), map[string]string{"password": secret}},
		{"plus two secrets", size, plusUnit, secrets2},
		{"plus three secrets", size, plusUnit, secrets3},
		{"decimal dots", size, dotUnit, map[string]string{"password": secret}},
		{"two secrets", size, unit, secrets2},
		{"percent URL", size, percentURLUnit, map[string]string{"password": secret}},
		{"percent SQL", size, percentSQLUnit, map[string]string{"password": secret}},
		{"schema-qualified SQL 64 KiB", 64 << 10, sqlUnit, map[string]string{"password": secret}},
		{"base64 continuous", size, base64Alphabet, map[string]string{"password": secret}},
		{"base64 sha256 tokens", size, sha256Unit, map[string]string{"password": secret}},
		{"base64 varying tokens", size, varying.String()[:size], map[string]string{"password": secret}},
	}
}

// D1: benign four-megabyte diagnostics that contain every secret byte, or that
// are dense in plus signs, percent escapes, or base64-alphabet tokens, must be
// delivered by both the plan and summary paths within the 300 ms availability
// budget. On the pre-fix tree these shapes fail closed because the anchored
// windowed-subsequence scan charges the whole work budget.
func TestDataProviderRound14DeliversBenignVariants(t *testing.T) {
	for _, tc := range dataTestR14BenignCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR11Repeat(tc.unit, tc.size)
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
					t.Fatalf("the benign %d-byte %s %s was rejected after %s: %v",
						len(text), tc.name, call.name, elapsed, err)
				}
				if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
					t.Fatalf("the benign %d-byte %s %s was delivered after %s, want under the public 500ms ceiling",
						len(text), tc.name, call.name, elapsed)
				}
				t.Logf("benign %d-byte %s %s delivered in %s",
					len(text), tc.name, call.name, elapsed)
			}
		})
	}
}

// dataTestR14PlanNodes installs a plan handler returning the supplied node tree
// and runs one Plan call.
func dataTestR14PlanNodes(env *dataEnv, lease *appplugin.DataLease,
	nodes []domainplugin.DataPlanNode) error {

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodPlan {
			return dataOK(map[string]any{"result": domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan:  &domainplugin.DataPlan{Nodes: nodes},
			}}), nil
		}
		return dataOK(nil), nil
	}
	_, err := lease.Plan(context.Background(), "select 1", nil)
	return err
}

// D2: a ten-byte secret cut 4/6 across two sibling plan nodes and placed in
// reversed node order is contiguous only when the fields are concatenated in
// reverse order. The forward join alone delivers it; both deterministic orders
// must be scanned. The matrix covers raw ASCII, escaped, and percent spellings,
// both node orders, and a parent/child split, plus the binary secret from the
// round-13 reproduction.
func TestDataProviderRound14RejectsReversedCrossNodeSplits(t *testing.T) {
	binarySecret := dataTestR13Bytes
	asciiSecret := "ABCDEFGHIJ"
	node := func(label string) domainplugin.DataPlanNode {
		return domainplugin.DataPlanNode{Label: label}
	}
	cases := []struct {
		name   string
		secret string
		nodes  []domainplugin.DataPlanNode
	}{
		{"ascii raw forward order", asciiSecret,
			[]domainplugin.DataPlanNode{node("node one ABCD"), node("node two EFGHIJ")}},
		{"ascii raw reversed order", asciiSecret,
			[]domainplugin.DataPlanNode{node("node one EFGHIJ"), node("node two ABCD")}},
		{"ascii escaped forward order", asciiSecret,
			[]domainplugin.DataPlanNode{node(`node one \x41\x42\x43\x44`), node(`node two \x45\x46\x47\x48\x49\x4a`)}},
		{"ascii escaped reversed order", asciiSecret,
			[]domainplugin.DataPlanNode{node(`node one \x45\x46\x47\x48\x49\x4a`), node(`node two \x41\x42\x43\x44`)}},
		{"ascii percent forward order", asciiSecret,
			[]domainplugin.DataPlanNode{node("node one %41%42%43%44"), node("node two %45%46%47%48%49%4a")}},
		{"ascii percent reversed order", asciiSecret,
			[]domainplugin.DataPlanNode{node("node one %45%46%47%48%49%4a"), node("node two %41%42%43%44")}},
		{"binary escaped forward order", binarySecret,
			[]domainplugin.DataPlanNode{node(`node one \x41\x42\x43\x44`), node(`node two \x45\x46\x47\xfe\x48\x4a`)}},
		{"binary escaped reversed order", binarySecret,
			[]domainplugin.DataPlanNode{node(`node one \x45\x46\x47\xfe\x48\x4a`), node(`node two \x41\x42\x43\x44`)}},
		{"binary percent forward order", binarySecret,
			[]domainplugin.DataPlanNode{node("node one %41%42%43%44"), node("node two %45%46%47%fe%48%4a")}},
		{"binary percent reversed order", binarySecret,
			[]domainplugin.DataPlanNode{node("node one %45%46%47%fe%48%4a"), node("node two %41%42%43%44")}},
		{"ascii raw nested forward pieces", asciiSecret,
			[]domainplugin.DataPlanNode{{Label: "node one ABCD",
				Children: []domainplugin.DataPlanNode{node("node two EFGHIJ")}}}},
		{"ascii raw nested reversed pieces", asciiSecret,
			[]domainplugin.DataPlanNode{{Label: "node one EFGHIJ",
				Children: []domainplugin.DataPlanNode{node("node two ABCD")}}}},
		{"binary escaped nested reversed pieces", binarySecret,
			[]domainplugin.DataPlanNode{{Label: `node one \x45\x46\x47\xfe\x48\x4a`,
				Children: []domainplugin.DataPlanNode{node(`node two \x41\x42\x43\x44`)}}}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, lease := dataTestR13Env(t, map[string]string{"password": tc.secret})
			err := dataTestR14PlanNodes(env, lease, tc.nodes)
			dataTestCheckSecretEchoError(t, err, tc.secret)
		})
	}
}

// D3: the per-value limit is measured on raw payload bytes, not on the
// JSON-escaped transport. A plan or summary diagnostic at the stated 4 MiB
// bound that contains '<', '>', or '&' must be accepted; one raw byte above the
// bound must be rejected without HTML escaping making the distinction.
func TestDataProviderRound14AcceptsRawValueAtTheBound(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)

	within := dataTestR11Repeat("query plan node filter total > 100 rows 42 ", 4190208)
	if err := dataTestR9Plan(env, lease, within); err != nil {
		t.Fatalf("a %d-byte plan text within the raw 4 MiB bound was rejected: %v",
			len(within), err)
	}
	if err := dataTestR9Summary(env, lease, within); err != nil {
		t.Fatalf("a %d-byte summary within the raw 4 MiB bound was rejected: %v",
			len(within), err)
	}

	// Exactly MaxDataValueBytes raw bytes of '<', '>', and '&': accepted.
	exact := strings.Repeat("a>", domainplugin.MaxDataValueBytes/2)
	if len(exact) != domainplugin.MaxDataValueBytes {
		t.Fatalf("exact construction is %d bytes, want %d",
			len(exact), domainplugin.MaxDataValueBytes)
	}
	if err := dataTestR9Plan(env, lease, exact); err != nil {
		t.Fatalf("plan text at the exact raw limit was rejected: %v", err)
	}

	// One raw byte above the per-value bound: rejected.
	for _, over := range []int{1, 2, 3} {
		text := exact + strings.Repeat(">", over)
		err := dataTestR9Plan(env, lease, text)
		if err == nil {
			t.Fatalf("plan text %d bytes above the raw limit was accepted", over)
		}
		wantDataKind(t, err, appplugin.DataErrorProtocol)
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("the limit error leaked the secret: %v", err)
		}
	}
}
