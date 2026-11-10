package fakehost_test

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/internal/testutil"
	"github.com/mslxl/natcatty/sdk/fakehost"
)

// sampleDirectory is the command sample both the fake host and app
// development mode run.
func sampleDirectory(t *testing.T) string {
	t.Helper()
	return filepath.Join(testutil.ModuleRoot(t), "sdk", "samples", "hello-command")
}

func TestSamplePluginRunsDeterministically(t *testing.T) {
	opts := fakehost.Options{
		Directory: sampleDirectory(t),
		Events:    []string{"greet"},
		StartNS:   1000,
		StepNS:    10,
		Seed:      42,
	}
	first, err := fakehost.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	second, err := fakehost.Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run(second): %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("runs differ:\nfirst  = %+v\nsecond = %+v", first, second)
	}
	if first.PluginID != "acme.hello" {
		t.Fatalf("plugin id = %q", first.PluginID)
	}
	if len(first.Events) != 1 || first.Events[0].Command != "plugin.acme.hello.greet" {
		t.Fatalf("events = %+v", first.Events)
	}
	if len(first.Outputs) != 1 {
		t.Fatalf("outputs = %v", first.Outputs)
	}
	if !strings.HasPrefix(first.Outputs[0], "hello from plugin now=1000 rand=") {
		t.Fatalf("output = %q, want the virtual clock value", first.Outputs[0])
	}
	if first.ClockEnd != 1010 {
		t.Fatalf("clock end = %d, want the one-step advance", first.ClockEnd)
	}
	var sawInit, sawDispatch bool
	for _, entry := range first.Logs {
		switch entry.Message {
		case "hello init":
			sawInit = true
		case "dispatch":
			sawDispatch = true
		}
	}
	if !sawInit || !sawDispatch {
		t.Fatalf("logs = %+v, want init and dispatch", first.Logs)
	}
}

func TestRandomSeedChangesTheInjectedBytes(t *testing.T) {
	base := fakehost.Options{
		Directory: sampleDirectory(t),
		Events:    []string{"greet"},
		StartNS:   1000,
	}
	first, err := fakehost.Run(context.Background(), base)
	if err != nil {
		t.Fatalf("Run(seed 0): %v", err)
	}
	base.Seed = 1
	second, err := fakehost.Run(context.Background(), base)
	if err != nil {
		t.Fatalf("Run(seed 1): %v", err)
	}
	if first.Outputs[0] == second.Outputs[0] {
		t.Fatalf("seeds produced the same output %q", first.Outputs[0])
	}
}

func TestQualifiedEventsArePassedThrough(t *testing.T) {
	report, err := fakehost.Run(context.Background(), fakehost.Options{
		Directory: sampleDirectory(t),
		Events:    []string{"plugin.acme.hello.greet"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Events[0].Command != "plugin.acme.hello.greet" {
		t.Fatalf("event = %q", report.Events[0].Command)
	}
}

func TestMissingDirectoryIsAnError(t *testing.T) {
	if _, err := fakehost.Run(context.Background(), fakehost.Options{}); err == nil {
		t.Fatal("Run with no directory succeeded")
	}
}

func TestClockAndRandomStreamsAreStable(t *testing.T) {
	clock := fakehost.NewClock(5, 7)
	if got := clock.AdvanceNS(); got != 5 {
		t.Fatalf("first AdvanceNS = %d", got)
	}
	if got := clock.AdvanceNS(); got != 12 {
		t.Fatalf("second AdvanceNS = %d", got)
	}
	if got := clock.CurrentNS(); got != 19 {
		t.Fatalf("CurrentNS = %d", got)
	}
	first := make([]byte, 16)
	second := make([]byte, 16)
	if _, err := fakehost.NewRandom(9).Read(first); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := fakehost.NewRandom(9).Read(second); err != nil {
		t.Fatalf("Read(second): %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("seeded streams differ: %x != %x", first, second)
	}
}
