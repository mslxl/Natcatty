package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// jobTestSession is the background-job seam: it runs a scripted function with
// the exact CommandOptions the manager passes, so startup, streaming, timeout,
// and cancellation can be asserted without a real shell.
type jobTestSession struct {
	mu   sync.Mutex
	run  func(context.Context, appsession.CommandOptions) (appsession.CommandResult, error)
	seen []appsession.CommandOptions
}

func (s *jobTestSession) Command(ctx context.Context, _ model.SessionID, _ string,
	options appsession.CommandOptions) (appsession.CommandResult, error) {
	s.mu.Lock()
	s.seen = append(s.seen, options)
	run := s.run
	s.mu.Unlock()
	return run(ctx, options)
}

func (s *jobTestSession) options() []appsession.CommandOptions {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]appsession.CommandOptions(nil), s.seen...)
}

func newJobTestManager(session mcpCommandSession) *mcpJobManager {
	manager := newMCPJobManager(session)
	manager.newID = func() string { return "job_test_1" }
	return manager
}

func TestMCPJobStreamsABoundedWindowWithMonotonicOffsets(t *testing.T) {
	text := strings.Repeat("abcdefghij", 30) // 300 bytes.
	session := &jobTestSession{run: func(_ context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		options.OnProgress(appsession.CommandProgress{
			Stdout: text[:200], TotalOutputChars: 200,
		})
		options.OnProgress(appsession.CommandProgress{
			Stdout: text, TotalOutputChars: len(text),
		})
		return appsession.CommandResult{
			Stdout: text, HasExitCode: true, ExitCode: 0,
			TotalOutputChars: len(text), OutputMode: "shell",
		}, nil
	}}
	manager := newJobTestManager(session)
	manager.outputLimit = 50
	started, err := manager.Start(context.Background(), "s1", "echo")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := manager.jobs["job_test_1"]
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not finish")
	}
	_ = started
	first, err := manager.Poll("job_test_1", 0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	output, _ := first["output"].(string)
	if len(output) != 50 {
		t.Fatalf("bounded output = %d bytes, want 50", len(output))
	}
	if output != text[len(text)-50:] {
		t.Fatalf("bounded output = %q, want the newest 50 bytes", output)
	}
	if first["outputTruncated"] != true {
		t.Fatalf("outputTruncated = %v", first["outputTruncated"])
	}
	if first["outputBaseOffset"] != len(text)-50 {
		t.Fatalf("outputBaseOffset = %v, want %d", first["outputBaseOffset"], len(text)-50)
	}
	if first["totalOutputChars"] != len(text) || first["nextOffset"] != len(text) {
		t.Fatalf("offset accounting = %#v", first)
	}
	// Reading from inside the retained window returns only bytes after the
	// offset; reading the whole result again is idempotent.
	late, err := manager.Poll("job_test_1", len(text)-10)
	if err != nil {
		t.Fatalf("late Poll: %v", err)
	}
	if late["output"] != text[len(text)-10:] || late["nextOffset"] != len(text) {
		t.Fatalf("late poll = %#v", late)
	}
	if fourth, _ := manager.Poll("job_test_1", len(text)); fourth["output"] != "" {
		t.Fatalf("poll at the end returned %q", fourth["output"])
	}
	// The command got the manager's foreground bound and lifetime.
	options := session.options()[0]
	if options.MaxOutputBytes != appmcp.ForegroundOutputLimit || options.Timeout != manager.overallTimeout {
		t.Fatalf("CommandOptions = %+v", options)
	}
}

func TestMCPJobPollShowsPartialOutputWhileTheCommandRuns(t *testing.T) {
	release := make(chan struct{})
	session := &jobTestSession{run: func(_ context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		options.OnProgress(appsession.CommandProgress{
			Stdout: "partial output", TotalOutputChars: len("partial output"),
		})
		<-release
		return appsession.CommandResult{
			Stdout: "partial output done", HasExitCode: true,
			TotalOutputChars: len("partial output done"),
		}, nil
	}}
	manager := newJobTestManager(session)
	if _, err := manager.Start(context.Background(), "s1", "echo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	polled, err := manager.Poll("job_test_1", 0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if polled["completed"] != false || polled["output"] != "partial output" {
		t.Fatalf("running poll = %#v", polled)
	}
	close(release)
	select {
	case <-manager.jobs["job_test_1"].done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not finish")
	}
}

func TestMCPJobRetentionCleansUpWithoutAPoll(t *testing.T) {
	session := &jobTestSession{run: func(_ context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		return appsession.CommandResult{Stdout: "done", HasExitCode: true}, nil
	}}
	manager := newJobTestManager(session)
	manager.retention = 30 * time.Millisecond
	if _, err := manager.Start(context.Background(), "s1", "echo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-manager.jobs["job_test_1"].done
	deadline := time.Now().Add(5 * time.Second)
	for {
		manager.mu.Lock()
		_, exists := manager.jobs["job_test_1"]
		manager.mu.Unlock()
		if !exists {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a finished job was never pruned without a poll")
		}
		select {
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestMCPJobOutputExactlyAtTheLimitIsNotTruncated(t *testing.T) {
	text := strings.Repeat("x", 64)
	session := &jobTestSession{run: func(_ context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		return appsession.CommandResult{Stdout: text, TotalOutputChars: len(text)}, nil
	}}
	manager := newJobTestManager(session)
	manager.outputLimit = 64
	if _, err := manager.Start(context.Background(), "s1", "echo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := manager.jobs["job_test_1"]
	<-job.done
	polled, _ := manager.Poll("job_test_1", 0)
	if polled["output"] != text || polled["outputTruncated"] != false || polled["outputBaseOffset"] != 0 {
		t.Fatalf("at-limit poll = %#v", polled)
	}
}

func TestMCPJobStartupTimeoutReturnsATypedTimeout(t *testing.T) {
	session := &jobTestSession{run: func(ctx context.Context, _ appsession.CommandOptions) (appsession.CommandResult, error) {
		<-ctx.Done()
		return appsession.CommandResult{}, appsession.ErrCommandCancelled
	}}
	manager := newJobTestManager(session)
	manager.startupTimeout = 40 * time.Millisecond
	started := time.Now()
	_, err := manager.Start(context.Background(), "s1", "sleep")
	if err == nil {
		t.Fatal("Start succeeded without a start marker")
	}
	var typed *appmcp.Error
	if !errors.As(err, &typed) || typed.Code != appmcp.CodeCommandTimeout || typed.Tool != "terminal_start" {
		t.Fatalf("startup error = %#v", err)
	}
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("startup timeout fired after %s", elapsed)
	}
}

func TestMCPJobStopCancelsTheSessionAndReleasesIt(t *testing.T) {
	cancelled := make(chan struct{})
	session := &jobTestSession{run: func(ctx context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		<-ctx.Done()
		close(cancelled)
		return appsession.CommandResult{Stdout: "partial", TotalOutputChars: 7},
			appsession.ErrCommandCancelled
	}}
	manager := newJobTestManager(session)
	if _, err := manager.Start(context.Background(), "s1", "sleep"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := manager.jobs["job_test_1"]
	if _, err := manager.Stop("job_test_1"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not cancel the command context")
	}
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped job never finished")
	}
	polled, err := manager.Poll("job_test_1", 0)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if polled["status"] != "cancelled" || polled["completed"] != true {
		t.Fatalf("stopped job = %#v", polled)
	}
	if sessionID, ok := manager.SessionForJob("job_test_1"); !ok || sessionID != "s1" {
		t.Fatalf("SessionForJob = %q, %v", sessionID, ok)
	}
}

func TestMCPJobRetentionUsesTheInjectedClock(t *testing.T) {
	now := time.Unix(1_000, 0)
	session := &jobTestSession{run: func(_ context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		return appsession.CommandResult{Stdout: "done", HasExitCode: true}, nil
	}}
	manager := newJobTestManager(session)
	manager.now = func() time.Time { return now }
	manager.retention = time.Minute
	if _, err := manager.Start(context.Background(), "s1", "echo"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-manager.jobs["job_test_1"].done
	now = now.Add(59 * time.Second)
	if _, err := manager.Poll("job_test_1", 0); err != nil {
		t.Fatalf("job was pruned before its retention: %v", err)
	}
	now = now.Add(time.Second)
	if _, err := manager.Poll("job_test_1", 0); !errors.Is(err, errMCPJobNotFound) {
		t.Fatalf("job survived its retention: %v", err)
	}
	if _, ok := manager.SessionForJob("job_test_1"); ok {
		t.Fatal("SessionForJob still resolves a pruned job")
	}
}

func TestMCPJobOverallTimeoutMarksTheJobFailed(t *testing.T) {
	session := &jobTestSession{run: func(ctx context.Context, options appsession.CommandOptions) (appsession.CommandResult, error) {
		options.OnStart()
		<-ctx.Done()
		return appsession.CommandResult{Stdout: "partial", TotalOutputChars: 7},
			appsession.ErrCommandTimeout
	}}
	manager := newJobTestManager(session)
	manager.overallTimeout = 40 * time.Millisecond
	if _, err := manager.Start(context.Background(), "s1", "sleep"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	job := manager.jobs["job_test_1"]
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the job did not hit its overall timeout")
	}
	polled, _ := manager.Poll("job_test_1", 0)
	if polled["status"] != "failed" || polled["error"] != "Command timed out" ||
		polled["output"] != "partial" {
		t.Fatalf("timed-out job = %#v", polled)
	}
}

func TestMCPJobUnknownJobIsTypedNotFound(t *testing.T) {
	manager := newJobTestManager(&jobTestSession{run: func(context.Context, appsession.CommandOptions) (appsession.CommandResult, error) {
		return appsession.CommandResult{}, nil
	}})
	if _, err := manager.Poll("missing", 0); !errors.Is(err, errMCPJobNotFound) {
		t.Fatalf("Poll error = %v", err)
	}
	if _, err := manager.Stop("missing"); !errors.Is(err, errMCPJobNotFound) {
		t.Fatalf("Stop error = %v", err)
	}
}
