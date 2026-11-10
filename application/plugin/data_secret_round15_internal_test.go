package plugin

import (
	"strings"
	"testing"
	"time"
)

// dataTestR15WorkLimit is the round-15 availability target: the worst benign
// multi-field shape must keep at least 20% of the shared work budget unused.
const dataTestR15WorkLimit = int64(dataSecretScanWorkBudget) * 8 / 10

func dataTestR15CheckDelivered(t *testing.T, name string, guard *dataSecretGuard,
	budget *dataSecretScanBudget, echo bool) {

	t.Helper()
	if echo {
		t.Fatalf("the benign %s construction matched or exhausted the budget (%d units, exceeded=%v)",
			name, budget.work, budget.exceeded)
	}
	if budget.exceeded {
		t.Fatalf("the benign %s construction exhausted the budget (%d units)", name, budget.work)
	}
	if budget.work > dataTestR15WorkLimit {
		t.Fatalf("the benign %s construction charged %d units (%.1f%% of the %d-unit budget), over the 80%% availability target",
			name, budget.work, 100*float64(budget.work)/float64(dataSecretScanWorkBudget),
			dataSecretScanWorkBudget)
	}
}

// D1 multi-field availability: every benign four-megabyte diagnostic from the
// round-14 matrix, with one ordinary sibling node, must be delivered with at
// least 20% of the work budget unused. The pre-fix scan charged each field, the
// forward join, and the reverse join, so the percent and base64 shapes latched.
func TestDataSecretGuardRound15MultiFieldWork(t *testing.T) {
	for _, tc := range dataTestR14BenignD1Cases(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(tc.secrets)
			if guard == nil {
				t.Fatal("guard is nil")
			}
			budget := newDataSecretScanBudget()
			started := time.Now()
			echo := guard.echoesFieldsWithin(budget, tc.text, "benign node")
			elapsed := time.Since(started)
			dataTestR15CheckDelivered(t, tc.name+" with one node", guard, budget, echo)
			t.Logf("benign %d-byte %s with one node charged %d units (%.1f%%) in %s",
				len(tc.text), tc.name, budget.work,
				100*float64(budget.work)/float64(dataSecretScanWorkBudget), elapsed)
		})
	}
}

// D1 multi-field availability for the node-only split shapes: the percent-SQL
// bytes distributed across 2, 8, and 64 sibling labels, with no plan text.
func TestDataSecretGuardRound15NodeOnlyWork(t *testing.T) {
	const unit = "SELECT%20public.orders.id%2C%20public.orders.customer_id%20FROM%20public.orders%20JOIN%20public.customers%20ON%20public.orders.customer_id%20%3D%20public.customers.id%20WHERE%20public.orders.total%20%3E%20100%20ORDER%20BY%20public.orders.created_at%20DESC%3B%20"
	const secret = "qvlxrmptwzbdjfhn"
	text := dataTestR13Repeat(unit, 4190208)
	for _, n := range []int{2, 8, 64} {
		n := n
		t.Run(dataTestR15NodeLabel(n), func(t *testing.T) {
			step := (len(text) + n - 1) / n
			fields := make([]string, 0, n)
			for i := 0; i < n; i++ {
				start := i * step
				if start >= len(text) {
					break
				}
				end := start + step
				if end > len(text) {
					end = len(text)
				}
				fields = append(fields, text[start:end])
			}
			guard := newDataSecretGuard(map[string]string{"password": secret})
			budget := newDataSecretScanBudget()
			started := time.Now()
			echo := guard.echoesFieldsWithin(budget, fields...)
			elapsed := time.Since(started)
			dataTestR15CheckDelivered(t, dataTestR15NodeLabel(n), guard, budget, echo)
			t.Logf("benign node-only %s charged %d units (%.1f%%) in %s",
				dataTestR15NodeLabel(n), budget.work,
				100*float64(budget.work)/float64(dataSecretScanWorkBudget), elapsed)
		})
	}
}

func dataTestR15NodeLabel(n int) string {
	switch n {
	case 2:
		return "two nodes"
	case 8:
		return "eight nodes"
	case 64:
		return "sixty-four nodes"
	}
	return "nodes"
}

// The combined-encoding benign single field must be delivered with at least 20%
// of the work budget unused. The six decoded forms it produces share most of
// their content; scanning each one in full plus the decode walk latched the
// budget at 100.0% before round 15.
func TestDataSecretGuardRound15CombinedEncodingWork(t *testing.T) {
	const unit = "SELECT\\x20public.orders%2C+width%3D36+%3E+100+ORDER%20BY+public.orders.created_at+DESC; "
	text := dataTestR13Repeat(unit, 4190208)
	guard := newDataSecretGuard(map[string]string{"password": "qvlxrmptwzbdjfhn"})
	budget := newDataSecretScanBudget()
	started := time.Now()
	echo := guard.echoesWithin(text, budget)
	elapsed := time.Since(started)
	dataTestR15CheckDelivered(t, "combined encoding single field", guard, budget, echo)
	t.Logf("benign combined-encoding single field charged %d units (%.1f%%) in %s",
		budget.work, 100*float64(budget.work)/float64(dataSecretScanWorkBudget), elapsed)
}

// D2 both orders through the new field scan: a ten-byte secret cut 4/6 across
// two sibling labels must be rejected by detection (not by a budget stop) in
// document order and in reversed order, for raw, escaped, and percent
// spellings, including the binary secret and a parent/child split.
func TestDataSecretGuardRound15RejectsCrossFieldSplitsBothOrders(t *testing.T) {
	binarySecret := string([]byte{
		0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47, 0xfe, 0x48, 0x4a,
	})
	asciiSecret := "ABCDEFGHIJ"
	cases := []struct {
		name   string
		secret string
		fields []string
	}{
		{"ascii raw forward", asciiSecret, []string{"node one ABCD", "node two EFGHIJ"}},
		{"ascii raw reversed", asciiSecret, []string{"node one EFGHIJ", "node two ABCD"}},
		{"ascii escaped forward", asciiSecret, []string{`node one \x41\x42\x43\x44`, `node two \x45\x46\x47\x48\x49\x4a`}},
		{"ascii escaped reversed", asciiSecret, []string{`node one \x45\x46\x47\x48\x49\x4a`, `node two \x41\x42\x43\x44`}},
		{"ascii percent forward", asciiSecret, []string{"node one %41%42%43%44", "node two %45%46%47%48%49%4a"}},
		{"ascii percent reversed", asciiSecret, []string{"node one %45%46%47%48%49%4a", "node two %41%42%43%44"}},
		{"binary escaped forward", binarySecret, []string{`node one \x41\x42\x43\x44`, `node two \x45\x46\x47\xfe\x48\x4a`}},
		{"binary escaped reversed", binarySecret, []string{`node one \x45\x46\x47\xfe\x48\x4a`, `node two \x41\x42\x43\x44`}},
		{"binary percent forward", binarySecret, []string{"node one %41%42%43%44", "node two %45%46%47%fe%48%4a"}},
		{"binary percent reversed", binarySecret, []string{"node one %45%46%47%fe%48%4a", "node two %41%42%43%44"}},
		{"ascii nested forward", asciiSecret, []string{"node one ABCD", "node two EFGHIJ"}},
		{"ascii nested reversed", asciiSecret, []string{"node one EFGHIJ", "node two ABCD"}},
		{"binary escaped nested reversed", binarySecret, []string{`node one \x45\x46\x47\xfe\x48\x4a`, `node two \x41\x42\x43\x44`}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			guard := newDataSecretGuard(map[string]string{"password": tc.secret})
			budget := newDataSecretScanBudget()
			if !guard.echoesFieldsWithin(budget, tc.fields...) {
				t.Fatalf("the %s split was accepted", tc.name)
			}
			if budget.exceeded {
				t.Fatalf("the %s split was rejected only by a budget stop", tc.name)
			}
		})
	}
}

// Short multi-field text keeps the exhaustive short-text rules when the joined
// diagnostic stays at or below the short-text bound: a raw split, a
// separator-interleaved spelling, and a letter-interleaved subsequence are all
// rejected by detection.
func TestDataSecretGuardRound15ShortFieldBehavior(t *testing.T) {
	const secret = "qvlxrmptwzbdjfhn"
	guard := newDataSecretGuard(map[string]string{"password": secret})
	if guard == nil {
		t.Fatal("guard is nil")
	}
	if !guard.echoesFields("node one "+secret[:8], secret[8:]+" node two") {
		t.Fatal("a short raw cross-field split was accepted")
	}
	if !guard.echoesFields("node one "+strings.Join(strings.Split(secret, ""), " "), "node two") {
		t.Fatal("a short separator-interleaved spelling was accepted")
	}
	scattered := dataTestScatteredDataText(secret, "q")
	if !guard.echoesFields("node one "+scattered, "node two") {
		t.Fatal("a short letter-interleaved subsequence was accepted")
	}
	if guard.echoesFields("warehouse-1", "accepted") {
		t.Fatal("benign short fields were rejected")
	}
}
