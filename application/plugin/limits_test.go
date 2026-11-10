package plugin

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefaultLimitsMatchTheRuntimeContract(t *testing.T) {
	limits := DefaultLimits()
	if limits.InitTimeout != 5*time.Second {
		t.Fatalf("init timeout = %s, want 5s", limits.InitTimeout)
	}
	if limits.CallTimeout != 2*time.Second {
		t.Fatalf("call timeout = %s, want 2s", limits.CallTimeout)
	}
	if limits.UnloadTimeout != 2*time.Second {
		t.Fatalf("unload timeout = %s, want 2s", limits.UnloadTimeout)
	}
	if limits.WatchdogGrace <= 0 {
		t.Fatalf("watchdog grace = %s, want a positive backstop", limits.WatchdogGrace)
	}

	normalized := Limits{InitTimeout: -1}.Normalize()
	if normalized.InitTimeout != 5*time.Second {
		t.Fatalf("normalized init timeout = %s, want the default", normalized.InitTimeout)
	}
	if normalized.CallTimeout != 2*time.Second || normalized.UnloadTimeout != 2*time.Second {
		t.Fatalf("normalized limits = %+v, want defaults for zero fields", normalized)
	}
}

func TestQuotaConstantsMatchTheSpec(t *testing.T) {
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"logs per second", MaxLogsPerSecond, 200},
		{"effect registrations", MaxEffectRegistrations, 4096},
		{"queue depth", MaxQueueDepth, 64},
		{"stream chunk", StreamChunkSize, 64 << 10},
		{"stream initial window", StreamInitialWindow, 256 << 10},
		{"stream buffer", StreamMaxBuffered, 1 << 20},
		{"streams per plugin", MaxStreamsPerPlugin, 16},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("%s = %d, want %d", check.name, check.got, check.want)
		}
	}
	if MaxHandles <= 0 {
		t.Fatalf("handle quota = %d, want a positive cap", MaxHandles)
	}
}

func TestLogLimiterDropsAboveTheRate(t *testing.T) {
	limiter := NewLogLimiter(0)
	now := time.Unix(1_000, 0)
	for i := 0; i < MaxLogsPerSecond; i++ {
		if err := limiter.Take(now); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
	}
	err := limiter.Take(now)
	var limit *LimitError
	if !errors.As(err, &limit) {
		t.Fatalf("over-rate take = %v, want LimitError", err)
	}
	if limit.Resource != "log lines per second" || limit.Limit != MaxLogsPerSecond {
		t.Fatalf("limit = %+v, want the log rate", limit)
	}

	if err := limiter.Take(now.Add(time.Second)); err != nil {
		t.Fatalf("take after the window: %v", err)
	}
	// A clock that moves backwards must not extend the blocked window.
	backwards := NewLogLimiter(1)
	if err := backwards.Take(now); err != nil {
		t.Fatal(err)
	}
	if err := backwards.Take(now.Add(-time.Hour)); err != nil {
		t.Fatalf("take with a backwards clock: %v", err)
	}
}

func TestDeadlineErrorMatchesContextDeadline(t *testing.T) {
	err := &DeadlineError{Operation: "dispatch", Limit: 2 * time.Second}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DeadlineError does not match context.DeadlineExceeded")
	}
	if !IsUnusable(err) {
		t.Fatal("DeadlineError is not classified as unusable")
	}
	if IsUnusable(&GuestStatusError{Operation: "dispatch", Status: -1}) {
		t.Fatal("a guest status error must not make the instance unusable")
	}
	if !IsUnusable(&TrapError{Operation: "dispatch", Err: errors.New("boom")}) {
		t.Fatal("a trap is not classified as unusable")
	}
}
