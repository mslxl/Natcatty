package plugin_test

import (
	"context"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// dataTestR15CombinedUnit is the round-15 combined-encoding reproduction: one
// repetition mixes a Go-style escape, percent escapes, and query plus signs, so
// the guarded scan legitimately decodes it to several distinct forms. It is
// benign SQL-shaped diagnostic text and must be delivered.
const dataTestR15CombinedUnit = "SELECT\\x20public.orders%2C+width%3D36+%3E+100+ORDER%20BY+public.orders.created_at+DESC; "

// dataTestR15PlusDenseUnit and dataTestR15PlusPublicUnit are the two
// plus-sign-dense shapes from the round-15 verification. A single scan is
// comfortably affordable; one benign sibling node made the pre-fix
// field-plus-two-join accounting cross the shared work budget.
const dataTestR15PlusDenseUnit = "query+plan+node:+bitmap+index+scan+on+foreign+key+value+zip+column+with+merge+join+width+36+9+rows+120+4+using+btree+cost+8+2+"
const dataTestR15PlusPublicUnit = "query+plan+node:+bitmap+index+scan+on+public.orders+foreign+key+value+zip+column+with+merge+join+width+36+9+rows+120+4+using+btree+cost+8+2+"

// dataTestR15PlanTextNode installs a plan handler returning text as plan text
// plus one benign sibling node and runs one Plan call.
func dataTestR15PlanTextNode(env *dataEnv, lease *appplugin.DataLease,
	text, label string) error {

	env.server.handler = func(request appplugin.DataProviderRequest,
		_ context.Context) ([]byte, error) {

		if request.Method == domainpluginabi.DataProviderMethodPlan {
			return dataOK(map[string]any{"result": domainplugin.DataResult{
				Kind:  domainplugin.DataResultPlan,
				Class: domainplugin.DataClassRead,
				Plan: &domainplugin.DataPlan{
					Text:  text,
					Nodes: []domainplugin.DataPlanNode{{Label: label}},
				},
			}}), nil
		}
		return dataOK(nil), nil
	}
	_, err := lease.Plan(context.Background(), "select 1", nil)
	return err
}

// dataTestR15NodeOnlyPlan installs a plan handler distributing text across n
// sibling node labels and runs one Plan call.
func dataTestR15NodeOnlyPlan(env *dataEnv, lease *appplugin.DataLease,
	text string, n int) error {

	step := (len(text) + n - 1) / n
	nodes := make([]domainplugin.DataPlanNode, 0, n)
	for i := 0; i < n; i++ {
		start := i * step
		if start >= len(text) {
			break
		}
		end := start + step
		if end > len(text) {
			end = len(text)
		}
		nodes = append(nodes, domainplugin.DataPlanNode{Label: text[start:end]})
	}
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

// D1: every benign 4 MiB diagnostic from the availability matrix must still be
// delivered when the provider adds one ordinary sibling plan node. Before round
// 15 the same text was charged to the shared work budget three times (the field,
// the forward join, and the reverse join), so percent-encoded and base64-dense
// shapes failed closed after about 130-405 ms.
func TestDataProviderRound15DeliversBenignDiagnosticsWithOneNode(t *testing.T) {
	for _, tc := range dataTestR14BenignCases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR11Repeat(tc.unit, tc.size)
			env, lease := dataTestR13Env(t, tc.secrets)
			started := time.Now()
			err := dataTestR15PlanTextNode(env, lease, text, "benign node")
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("the benign %d-byte %s plan with one sibling node was rejected after %s: %v",
					len(text), tc.name, elapsed, err)
			}
			if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
				t.Fatalf("the benign %d-byte %s plan with one sibling node was delivered after %s, want under the public 500ms ceiling",
					len(text), tc.name, elapsed)
			}
			t.Logf("benign %d-byte %s plan with one node delivered in %s",
				len(text), tc.name, elapsed)
		})
	}
}

// The exact round-15 D1 multi-field reproduction: the percent-SQL unit plus one
// benign node failed with DataErrorProtocol on the pre-fix tree while the same
// text without nodes delivered.
func TestDataProviderRound15PercentSQLWithNodeReproduction(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	const unit = "SELECT%20public.orders.id%2C%20public.orders.customer_id%20FROM%20public.orders%20JOIN%20public.customers%20ON%20public.orders.customer_id%20%3D%20public.customers.id%20WHERE%20public.orders.total%20%3E%20100%20ORDER%20BY%20public.orders.created_at%20DESC%3B%20"
	text := dataTestR11Repeat(unit, 4190208)
	env, lease := dataTestR13Env(t, map[string]string{"password": secret})

	if err := dataTestR9Plan(env, lease, text); err != nil {
		t.Fatalf("the text without nodes was rejected: %v", err)
	}
	if err := dataTestR15PlanTextNode(env, lease, text, "benign node"); err != nil {
		t.Fatalf("the same text with one benign node was rejected: %v", err)
	}
	if err := dataTestR9Summary(env, lease, text); err != nil {
		t.Fatalf("the same text as a summary was rejected: %v", err)
	}
}

// D1: the plus-dense shapes with one ordinary sibling node, including the
// denser plus/public variant, delivered on the pre-fix tree at 181.9 ms and
// 217.7 ms as budget rejections.
func TestDataProviderRound15DeliversPlusDenseWithOneNode(t *testing.T) {
	const size = 4190208
	cases := []struct {
		name string
		unit string
	}{
		{"plus dense", dataTestR15PlusDenseUnit},
		{"plus public", dataTestR15PlusPublicUnit},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			text := dataTestR11Repeat(tc.unit, size)
			env, lease := dataTestR13Env(t, map[string]string{"password": "qvlxrmptwzbdjfhn"})
			started := time.Now()
			err := dataTestR15PlanTextNode(env, lease, text, "benign node")
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("the benign %s plan with one node was rejected after %s: %v",
					tc.name, elapsed, err)
			}
			if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
				t.Fatalf("the benign %s plan with one node was delivered after %s", tc.name, elapsed)
			}
			t.Logf("benign %s plan with one node delivered in %s", tc.name, elapsed)
		})
	}
}

// D1: the percent-SQL bytes distributed across node labels only (no plan text)
// must be delivered for 2, 8, and 64 sibling nodes. Each node contributes two
// fields (label and empty detail), so the pre-fix tree ran the per-field scans
// and both whole-join scans over the same bytes and latched the budget.
func TestDataProviderRound15DeliversNodeOnlySplits(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	const unit = "SELECT%20public.orders.id%2C%20public.orders.customer_id%20FROM%20public.orders%20JOIN%20public.customers%20ON%20public.orders.customer_id%20%3D%20public.customers.id%20WHERE%20public.orders.total%20%3E%20100%20ORDER%20BY%20public.orders.created_at%20DESC%3B%20"
	text := dataTestR11Repeat(unit, 4190208)
	for _, tc := range []struct {
		name string
		n    int
	}{
		{"two nodes", 2},
		{"eight nodes", 8},
		{"sixty-four nodes", 64},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			env, lease := dataTestR13Env(t, map[string]string{"password": secret})
			started := time.Now()
			err := dataTestR15NodeOnlyPlan(env, lease, text, tc.n)
			elapsed := time.Since(started)
			if err != nil {
				t.Fatalf("the benign node-only %d-node split was rejected after %s: %v",
					tc.n, elapsed, err)
			}
			if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
				t.Fatalf("the benign node-only %d-node split was delivered after %s", tc.n, elapsed)
			}
			t.Logf("benign node-only %d-node split delivered in %s", tc.n, elapsed)
		})
	}
}

// The combined-encoding benign single field: one 4 MiB repetition of the
// round-15 unit decodes to six forms. It latched the work budget (100.0%) before
// round 15 and must be delivered on both public diagnostic paths.
func TestDataProviderRound15DeliversCombinedEncodingText(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	text := dataTestR11Repeat(dataTestR15CombinedUnit, 4190208)
	env, lease := dataTestR13Env(t, map[string]string{"password": secret})
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
			t.Fatalf("the benign combined-encoding %s was rejected after %s: %v",
				call.name, elapsed, err)
		}
		if !dataTestRaceEnabled && elapsed > 500*time.Millisecond {
			t.Fatalf("the benign combined-encoding %s was delivered after %s", call.name, elapsed)
		}
		t.Logf("benign combined-encoding %s delivered in %s", call.name, elapsed)
	}
}
