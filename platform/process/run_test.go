//go:build !windows

package process

import (
	"context"
	"testing"
	"time"
)

func TestRunReportsOutputAndExitCode(t *testing.T) {
	result, err := Run(context.Background(), `printf out; printf err >&2; exit 3`)
	if err != nil {
		t.Fatal(err)
	}
	if string(result.Stdout) != "out" || string(result.Stderr) != "err" || result.ExitCode != 3 {
		t.Fatalf("result = %q %q %d, want out err 3", result.Stdout, result.Stderr, result.ExitCode)
	}
}

func TestRunStopsTheCommandWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Run(ctx, `sleep 30`); err == nil {
		t.Fatal("a cancelled command reported success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run returned after %v, want it stopped with the context", elapsed)
	}
}
