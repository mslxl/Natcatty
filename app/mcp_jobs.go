package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
	"github.com/mslxl/natcatty/domain/model"
)

// Background terminal jobs. They mirror the original Electron MCP job bridge:
// one shell-backed command runs behind a marker wrapper, terminal_start waits
// for the start marker (30 s), the job lives at most 60 minutes, output is a
// rolling 256 KiB window with monotonic offsets, and a finished job stays
// pollable for 10 minutes.

var errMCPJobNotFound = errors.New("external MCP: background job not found")

type mcpCommandSession interface {
	Command(ctx context.Context, id model.SessionID, command string,
		opts appsession.CommandOptions) (appsession.CommandResult, error)
}

type mcpJob struct {
	id        string
	sessionID model.SessionID
	command   string
	startedAt time.Time

	mu         sync.Mutex
	status     string
	error      string
	exitCode   int
	hasExit    bool
	stdout     string
	baseOffset int
	total      int
	truncated  bool
	updatedAt  time.Time
	finishedAt time.Time

	cancel  context.CancelFunc
	started chan struct{}
	done    chan struct{}
}

// mcpJobManager owns job state. Its clock and ID generator are injectable so
// tests can cross the retention and startup boundaries without sleeping.
type mcpJobManager struct {
	sessions mcpCommandSession

	startupTimeout time.Duration
	overallTimeout time.Duration
	retention      time.Duration
	outputLimit    int

	now   func() time.Time
	newID func() string

	mu   sync.Mutex
	jobs map[string]*mcpJob
}

func newMCPJobManager(sessions mcpCommandSession) *mcpJobManager {
	return &mcpJobManager{
		sessions:       sessions,
		startupTimeout: appmcp.JobStartupTimeout,
		overallTimeout: appmcp.JobOverallTimeout,
		retention:      appmcp.JobRetention,
		outputLimit:    appmcp.JobOutputLimit,
		now:            time.Now,
		newID:          newMCPJobID,
		jobs:           make(map[string]*mcpJob),
	}
}

func newMCPJobID() string {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		random = []byte(strconv.FormatInt(time.Now().UnixNano(), 16))
	}
	return "job_" + strconv.FormatInt(time.Now().UnixNano(), 36) + "_" + hex.EncodeToString(random)
}

// Start launches one job and waits at most JobStartupTimeout for its start
// marker. It returns the job snapshot, or a typed timeout/unavailable error.
func (m *mcpJobManager) Start(ctx context.Context, sessionID model.SessionID,
	command string) (map[string]any, error) {

	m.prune()
	jobCtx, cancel := context.WithTimeout(context.Background(), m.overallTimeout)
	job := &mcpJob{
		id:        m.newID(),
		sessionID: sessionID,
		command:   command,
		startedAt: m.now(),
		status:    "running",
		cancel:    cancel,
		started:   make(chan struct{}),
		done:      make(chan struct{}),
	}
	job.updatedAt = job.startedAt
	m.mu.Lock()
	m.jobs[job.id] = job
	m.mu.Unlock()

	go m.run(jobCtx, job)

	timer := time.NewTimer(m.startupTimeout)
	defer timer.Stop()
	select {
	case <-job.started:
		return serializeMCPJob(job, 0), nil
	case <-job.done:
		cancel()
		// A command that ends before its start marker is a startup failure,
		// even when it failed because this select cancelled it first.
		started := false
		select {
		case <-job.started:
			started = true
		default:
		}
		if !started {
			return nil, appmcp.CommandTimeoutError("terminal_start", m.startupTimeout)
		}
		if jobError := m.jobError(job); jobError != nil {
			return nil, jobError
		}
		return serializeMCPJob(job, 0), nil
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	case <-timer.C:
		cancel()
		job.mu.Lock()
		job.status = "failed"
		job.error = "Background job startup timed out — start marker never arrived"
		job.updatedAt = m.now()
		job.mu.Unlock()
		return nil, appmcp.CommandTimeoutError("terminal_start", m.startupTimeout)
	}
}

func (m *mcpJobManager) run(ctx context.Context, job *mcpJob) {
	defer close(job.done)
	result, err := m.sessions.Command(ctx, job.sessionID, job.command, appsession.CommandOptions{
		Timeout:        m.overallTimeout,
		MaxOutputBytes: appmcp.ForegroundOutputLimit,
		OnStart: func() {
			select {
			case <-job.started:
			default:
				close(job.started)
			}
		},
		OnProgress: func(progress appsession.CommandProgress) {
			window, cut, truncated := boundJobOutput(progress.Stdout, m.outputLimit)
			job.mu.Lock()
			job.stdout = window
			job.baseOffset = progress.OutputBaseOffset + cut
			job.total = progress.TotalOutputChars
			if job.total < job.baseOffset+len(job.stdout) {
				job.total = job.baseOffset + len(job.stdout)
			}
			job.truncated = job.truncated || truncated || progress.Truncated
			job.updatedAt = m.now()
			job.mu.Unlock()
		},
	})
	now := m.now()
	job.mu.Lock()
	defer job.mu.Unlock()
	window, cut, truncated := boundJobOutput(result.Stdout, m.outputLimit)
	job.stdout = window
	if finalBase := result.OutputBaseOffset + cut; finalBase >= job.baseOffset {
		job.baseOffset = finalBase
	}
	finalTotal := result.TotalOutputChars
	if finalTotal < job.total {
		finalTotal = job.total
	}
	if finalTotal < job.baseOffset+len(job.stdout) {
		finalTotal = job.baseOffset + len(job.stdout)
	}
	job.total = finalTotal
	job.truncated = job.truncated || truncated || result.Truncated
	job.hasExit = result.HasExitCode
	job.exitCode = result.ExitCode
	job.updatedAt = now
	job.finishedAt = now
	switch {
	case errors.Is(err, appsession.ErrCommandCancelled):
		job.status = "cancelled"
		job.error = "Cancelled"
	case errors.Is(err, appsession.ErrCommandTimeout):
		job.status = "failed"
		job.error = "Command timed out"
	case err != nil:
		job.status = "failed"
		job.error = err.Error()
	case result.HasExitCode && result.ExitCode != 0:
		job.status = "failed"
		job.error = "Command exited with code " + strconv.Itoa(result.ExitCode)
	default:
		job.status = "completed"
	}
	// A finished job must leave memory even when no client ever polls it.
	// Start and Poll prune as well; this timer covers the abandoned case.
	m.schedulePrune()
}

// schedulePrune drops finished jobs once their retention elapses. Retention is
// measured on the injected clock by prune; the timer is only a wake-up call.
func (m *mcpJobManager) schedulePrune() {
	if m.retention <= 0 {
		return
	}
	time.AfterFunc(m.retention, m.prune)
}

// jobError converts a job's immediate failure into a typed tool error. A
// startup failure is the only case Start should surface instead of a job id.
func (m *mcpJobManager) jobError(job *mcpJob) error {
	job.mu.Lock()
	defer job.mu.Unlock()
	if job.status == "failed" && strings.Contains(job.error, "start marker") {
		return appmcp.CommandTimeoutError("terminal_start", m.startupTimeout)
	}
	if job.status == "failed" && job.error != "" {
		return appmcp.ToolUnavailableError("terminal_start", job.error)
	}
	return nil
}

// Poll returns the serialized job, or errMCPJobNotFound.
func (m *mcpJobManager) Poll(jobID string, offset int) (map[string]any, error) {
	m.prune()
	m.mu.Lock()
	job := m.jobs[jobID]
	m.mu.Unlock()
	if job == nil {
		return nil, errMCPJobNotFound
	}
	return serializeMCPJob(job, offset), nil
}

// Stop requests cancellation and returns the current snapshot.
func (m *mcpJobManager) Stop(jobID string) (map[string]any, error) {
	m.mu.Lock()
	job := m.jobs[jobID]
	m.mu.Unlock()
	if job == nil {
		return nil, errMCPJobNotFound
	}
	job.mu.Lock()
	if job.status == "running" {
		job.status = "stopping"
		job.error = "Cancellation requested"
		job.updatedAt = m.now()
	}
	cancel := job.cancel
	job.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return serializeMCPJob(job, 0), nil
}

// SessionForJob is the policy's terminal_stop scope/generation seam.
func (m *mcpJobManager) SessionForJob(jobID string) (model.SessionID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil {
		return "", false
	}
	return job.sessionID, true
}

// prune drops jobs whose retention elapsed.
func (m *mcpJobManager) prune() {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, job := range m.jobs {
		job.mu.Lock()
		terminal := job.status != "running" && job.status != "stopping"
		expired := terminal && !job.finishedAt.IsZero() && now.Sub(job.finishedAt) >= m.retention
		job.mu.Unlock()
		if expired {
			delete(m.jobs, id)
		}
	}
}

func boundJobOutput(stdout string, limit int) (string, int, bool) {
	if limit <= 0 || len(stdout) <= limit {
		return stdout, 0, false
	}
	cut := len(stdout) - limit
	return stdout[cut:], cut, true
}

func serializeMCPJob(job *mcpJob, offset int) map[string]any {
	job.mu.Lock()
	defer job.mu.Unlock()
	stdout := job.stdout
	base := job.baseOffset
	total := job.total
	if total < base+len(stdout) {
		total = base + len(stdout)
	}
	relative := 0
	if offset > base {
		relative = offset - base
		if relative > len(stdout) {
			relative = len(stdout)
		}
	}
	completed := job.status != "running" && job.status != "stopping"
	return map[string]any{
		"ok":                        true,
		"jobId":                     job.id,
		"sessionId":                 string(job.sessionID),
		"command":                   job.command,
		"status":                    job.status,
		"completed":                 completed,
		"exitCode":                  job.exitCode,
		"hasExitCode":               job.hasExit,
		"error":                     job.error,
		"startedAt":                 job.startedAt.UnixMilli(),
		"updatedAt":                 job.updatedAt.UnixMilli(),
		"output":                    collapseJobCarriageReturns(stdout[relative:]),
		"nextOffset":                total,
		"totalOutputChars":          total,
		"outputBaseOffset":          base,
		"outputTruncated":           job.truncated,
		"recommendedPollIntervalMs": appmcp.JobPollInterval.Milliseconds(),
	}
}

// collapseJobCarriageReturns keeps only the newest frame of a progress-bar
// redraw, matching the original poll serializer.
func collapseJobCarriageReturns(text string) string {
	var builder strings.Builder
	pendingCR := false
	for _, character := range text {
		switch character {
		case '\r':
			pendingCR = true
		case '\n':
			pendingCR = false
			builder.WriteRune(character)
		default:
			if pendingCR {
				current := builder.String()
				if index := strings.LastIndexByte(current, '\n'); index >= 0 {
					builder.Reset()
					builder.WriteString(current[:index+1])
				} else {
					builder.Reset()
				}
				pendingCR = false
			}
			builder.WriteRune(character)
		}
	}
	return builder.String()
}
