package session

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

var internalMarkerPattern = regexp.MustCompile(`__NCMCP_[0-9a-z]+_[0-9a-f]+__`)

// waitForMarker returns the nth distinct programmatic wrapper marker the
// Session has typed, once the transport has received it.
func waitForMarker(t *testing.T, transport *fakeTransport, count int) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		seen := []string{}
		for _, marker := range internalMarkerPattern.FindAllString(transport.input(), -1) {
			if len(seen) == 0 || seen[len(seen)-1] != marker {
				seen = append(seen, marker)
			}
		}
		if len(seen) >= count {
			return seen[count-1]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("the Session never typed wrapper %d: %q", count, transport.input())
	return ""
}

// answerWrapper fakes what a hooked shell does with one wrapper line: the
// private preexec sequence reports the wrapper (including its marker), D
// finishes it, and the collector sees its own start and end markers.
func answerWrapper(t *testing.T, transport *fakeTransport, marker, output string) {
	t.Helper()
	command := " " + marker + "=0; { printf; }"
	transport.send(netcattyExec("bash", "/work", command, false))
	transport.send("\x1b]133;D;0\x07")
	transport.send("\n" + marker + "_S\n" + output + marker + "_E:0\n")
}

// Every Session.Command types a wrapped programmatic command, including the
// readiness probe. The hook captures those lines like user input; none of them
// may become a Command History row, while a concurrent user command must still
// be published.
func TestProgrammaticCommandWrappersAreNotCaptured(t *testing.T) {
	transport := newFakeTransport()
	events := make(chan CommandEvent, 8)
	service := newTestService(&fakeFactory{transport: transport},
		WithCommandCompletionHook(func(event CommandEvent) { events <- event }))
	t.Cleanup(service.CloseAll)

	session, err := service.Open(context.Background(), StartSpec{
		Kind: KindSSH, Hostname: "example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	type outcome struct {
		result CommandResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := session.Command(context.Background(), "echo programmatic",
			CommandOptions{Timeout: 5 * time.Second})
		done <- outcome{result: result, err: err}
	}()

	// The remote shell is not ready, so the first wrapper is the : probe.
	probe := waitForMarker(t, transport, 1)
	answerWrapper(t, transport, probe, "")
	real := waitForMarker(t, transport, 2)
	if real == probe {
		t.Fatal("the readiness probe and the command shared a marker")
	}
	answerWrapper(t, transport, real, "programmatic\n")

	result := <-done
	if result.err != nil {
		t.Fatalf("Command: %v", result.err)
	}
	if strings.TrimSpace(result.result.Stdout) != "programmatic" {
		t.Fatalf("stdout = %q", result.result.Stdout)
	}
	select {
	case event := <-events:
		t.Fatalf("a programmatic wrapper was journaled: %+v", event)
	case <-time.After(200 * time.Millisecond):
	}

	// A user command arriving through the hook is not suppressed by the
	// markers the Session recorded.
	transport.send(netcattyExec("bash", "/work", "echo user", false))
	transport.send("\x1b]133;D;0\x07")
	select {
	case event := <-events:
		if event.Command != "echo user" || event.CaptureTier != domainhistory.CaptureTierHook {
			t.Fatalf("user completion = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the user command was suppressed with the wrappers")
	}

	// A suppressed wrapper's marker is forgotten once its capture arrives, so
	// a long Session does not retain one marker per programmatic command.
	session.internalMu.Lock()
	remaining := len(session.internalMarkers)
	session.internalMu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d internal markers survived their completions", remaining)
	}
}
