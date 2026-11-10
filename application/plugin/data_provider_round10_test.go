package plugin_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// dataTestR10PathologicalText builds a ~4 MiB diagnostic that contains every
// byte of a secret of the form "!" + N*"a" + "Z" while never presenting a
// windowed ordered spelling of it. The head is dense in "!" anchors, but every
// secret-length window inside it stops before the single "Z"; the "Z" is placed
// more than the widest documented window away from the head, so the reversed
// spellings fail as well. Nine-byte runs of a non-alphabet byte separate the
// anchors, which keeps base64 gap-joining from turning the head into one giant
// token, and the filler is separator-class punctuation so the request and
// response JSON stay small. Before the round-10 fix this text spent seconds in
// dataTextWindowedSubsequence after the guest call had returned.
func dataTestR10PathologicalText(total int) string {
	const gap = 8
	const pad = 600000
	unit := "!" + strings.Repeat("~", gap) + "a" + strings.Repeat("~", gap)
	head := total - 2*pad - 1
	var text strings.Builder
	text.Grow(total)
	for text.Len() < head {
		text.WriteString(unit)
	}
	headText := text.String()[:head]
	var out strings.Builder
	out.Grow(total)
	out.WriteString(headText)
	out.WriteString(strings.Repeat("~", pad))
	out.WriteString("Z")
	out.WriteString(strings.Repeat("~", pad))
	return out.String()
}

// dataTestR10Secret returns "!" + (size-2)*"a" + "Z". One byte of the secret
// must be a separator-class byte that also acts as an anchor and one must be
// absent from the head, which is what makes every spelling scan the head.
func dataTestR10Secret(size int) string {
	return "!" + strings.Repeat("a", size-2) + "Z"
}

// B1 regression: the anchored windowed-subsequence stage must be charged to the
// shared work budget. A crafted multi-megabyte diagnostic with a dense anchor
// field used to burn 9-18 seconds of host CPU for a long secret and then deliver
// successfully. It must now fail closed with the constant protocol error in well
// under a second. The round-11 public ceiling is half a second, which still
// catches the multi-second regression without depending on machine load while
// matching the verification's <200ms pathological bound with headroom; the
// internal guard test additionally pins the budget latch directly.
func TestDataProviderRejectsWindowedSubsequenceBurn(t *testing.T) {
	text := dataTestR10PathologicalText(4<<20 - 4096)
	for _, size := range []int{17, 257, 1024, 64 << 10} {
		size := size
		t.Run(fmt.Sprintf("%d-byte secret", size), func(t *testing.T) {
			secret := dataTestR10Secret(size)
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
					t.Fatalf("%s with a %d-byte secret failed closed after %s, want well under 500ms",
						call.name, size, elapsed)
				}
				t.Logf("%s with a %d-byte secret failed closed in %s", call.name, size, elapsed)
			}
		})
	}
}

// A benign multi-megabyte plan and summary must still be delivered: the new
// subsequence charge applies to candidates that could hold the secret, not to
// ordinary diagnostic text. The text deliberately omits one secret byte, so the
// presence prefilter skips the spellings entirely.
func TestDataProviderDeliversBenignFourMiBDiagnostics(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)

	var text strings.Builder
	for text.Len() < 4<<20-4096 {
		text.WriteString("sequential scan of orders with index lookup and hash join ")
	}
	plan := text.String()[:4<<20-4096]

	started := time.Now()
	if err := dataTestR9Plan(env, lease, plan); err != nil {
		t.Fatalf("%d-byte benign plan was rejected: %v", len(plan), err)
	}
	planElapsed := time.Since(started)
	started = time.Now()
	if err := dataTestR9Summary(env, lease, plan); err != nil {
		t.Fatalf("%d-byte benign summary was rejected: %v", len(plan), err)
	}
	summaryElapsed := time.Since(started)
	if !dataTestRaceEnabled && (planElapsed > 500*time.Millisecond || summaryElapsed > 500*time.Millisecond) {
		t.Fatalf("benign %d-byte diagnostics took %s/%s, want each under 500ms",
			len(plan), planElapsed, summaryElapsed)
	}
	t.Logf("benign %d-byte plan delivered in %s, summary in %s",
		len(plan), planElapsed, summaryElapsed)
}

// Pin the base64 gap-join boundary. A token split by a run of non-alphabet
// bytes no longer than dataSecretBase64GapBytes (8) is reassembled and the
// secret is rejected. A run of 9 bytes is the documented residual: it is treated
// as a token boundary, the two alphabet runs are decoded separately, and a
// secret encoded only across that split is not reassembled. This test states
// both directions explicitly instead of silently changing the policy; widening
// or shrinking the constant must update this expectation deliberately.
func TestDataProviderBase64GapBoundary(t *testing.T) {
	secret := "qvlxrmptwzbdjfhn"
	env, lease := dataTestR9Env(t, secret)
	token := dataTestR9Token(secret, "xyzw")
	split := func(gap int) string {
		return "plan token " + token[:10] + strings.Repeat("\x01", gap) + token[10:] + " done"
	}

	dataTestCheckSecretEchoError(t, dataTestR9Plan(env, lease, split(8)), secret, token)

	// 9 bytes is one past the documented join window: the token stays split and
	// the secret is not reconstructed. This is the accepted residual gap, not a
	// regression.
	if err := dataTestR9Plan(env, lease, split(9)); err != nil {
		t.Fatalf("a token split by 9 non-alphabet bytes was rejected; the documented gap limit is 8: %v", err)
	}
}
