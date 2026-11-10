package plugin_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// mcpToolRecorder is the container's view of the app's external MCP registry.
type mcpToolRecorder struct {
	mu      sync.Mutex
	entries map[string]*recordedMCPTool
}

type recordedMCPTool struct {
	ref    appplugin.PluginRef
	tool   domainplugin.MCPTool
	caller appplugin.ToolCaller
}

func newMCPToolRecorder() *mcpToolRecorder {
	return &mcpToolRecorder{entries: map[string]*recordedMCPTool{}}
}

func (r *mcpToolRecorder) RegisterMCPTool(ref appplugin.PluginRef,
	tool domainplugin.MCPTool, caller appplugin.ToolCaller) (appplugin.Disposer, error) {
	name := domainplugin.QualifiedMCPToolName(ref.ID, tool.Name)
	r.mu.Lock()
	r.entries[name] = &recordedMCPTool{ref: ref, tool: tool, caller: caller}
	r.mu.Unlock()
	var once sync.Once
	return func(context.Context) error {
		once.Do(func() {
			r.mu.Lock()
			delete(r.entries, name)
			r.mu.Unlock()
		})
		return nil
	}, nil
}

func (r *mcpToolRecorder) get(name string) *recordedMCPTool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[name]
}

func (r *mcpToolRecorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	return names
}

func mcpToolManifest(names ...string) domainplugin.Manifest {
	manifest := validManifest("acme.hello")
	manifest.Permissions.Required = []string{string(domainplugin.PermissionMCPTools)}
	for _, name := range names {
		manifest.Contributions.MCPTools = append(manifest.Contributions.MCPTools,
			domainplugin.MCPTool{
				Name:        name,
				Title:       "Tool " + name,
				Description: "A tool used by the container test.",
				Access:      domainplugin.MCPToolRead,
				Target:      domainplugin.MCPTargetNone,
				Timeout:     domainplugin.MCPTimeoutShort,
			})
	}
	return manifest
}

func loadMCPToolContainer(t *testing.T, manifest domainplugin.Manifest,
	recorder *mcpToolRecorder, depth int) (*appplugin.Container, *appplugin.Fiber) {
	t.Helper()
	dir := pluginDir(t, "tools", manifest)
	options := []appplugin.Option{appplugin.WithMCPToolsRegistrar(recorder)}
	if depth > 0 {
		options = append(options, appplugin.WithMCPToolQueueDepth(depth))
	}
	container := newTestContainer(t, manifest, dir, &registrar{}, options...)
	t.Cleanup(func() { _ = container.Close(context.Background()) })
	fiber, err := container.LoadDevelopment(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadDevelopment: %v", err)
	}
	return container, fiber
}

func TestContainerPublishesDeclaredMCPToolsOnlyWhileActive(t *testing.T) {
	recorder := newMCPToolRecorder()
	manifest := mcpToolManifest("echo", "progress", "empty")
	container, fiber := loadMCPToolContainer(t, manifest, recorder, 0)

	name := "plugin.acme.hello.echo"
	entry := recorder.get(name)
	if entry == nil {
		t.Fatalf("registered tools = %v", recorder.names())
	}
	if entry.ref.ID != manifest.ID {
		t.Fatalf("tool owner = %q", entry.ref.ID)
	}
	if entry.tool.Access != domainplugin.MCPToolRead || entry.tool.Timeout != domainplugin.MCPTimeoutShort {
		t.Fatalf("tool declaration = %+v", entry.tool)
	}

	var progress []uint32
	payload, err := entry.caller.CallTool(context.Background(),
		"plugin.acme.hello.progress", map[string]any{"text": "hi"}, time.Second,
		func(value uint32, _ string) { progress = append(progress, value) })
	if err != nil {
		t.Fatalf("CallTool(progress): %v", err)
	}
	if len(progress) != 1 || progress[0] != 50 {
		t.Fatalf("progress = %v", progress)
	}
	if !strings.Contains(string(payload), `"tool":"progress"`) {
		t.Fatalf("progress payload = %s", payload)
	}

	payload, err = entry.caller.CallTool(context.Background(), name,
		map[string]any{"text": "hi"}, time.Second, nil)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(string(payload), `"text":"fake:`+name+`"`) ||
		!strings.Contains(string(payload), `"arguments":{"text":"hi"}`) {
		t.Fatalf("tool payload = %s", payload)
	}

	payload, err = entry.caller.CallTool(context.Background(),
		"plugin.acme.hello.empty", nil, time.Second, nil)
	if err != nil || len(payload) != 0 {
		t.Fatalf("empty tool result = %q, %v", payload, err)
	}

	// Leaving ACTIVE removes the tool; the caller fails closed rather than
	// reaching a stale instance.
	if err := container.Unload(context.Background(), fiber.ID()); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	if names := recorder.names(); len(names) != 0 {
		t.Fatalf("tools after unload = %v", names)
	}
	if _, err := entry.caller.CallTool(context.Background(), name, nil,
		time.Second, nil); !errors.Is(err, appplugin.ErrNotActive) {
		t.Fatalf("stale call error = %v, want ErrNotActive", err)
	}
}

// TestContainerRejectsUndeclaredMCPTool proves the caller cannot invent a tool
// name outside the manifest's closed declaration set, even through the
// container's own ToolCaller, and that the rejection does not disturb the
// guest or the declared tools.
func TestContainerRejectsUndeclaredMCPTool(t *testing.T) {
	recorder := newMCPToolRecorder()
	manifest := mcpToolManifest("echo")
	_, fiber := loadMCPToolContainer(t, manifest, recorder, 0)
	entry := recorder.get(domainplugin.QualifiedMCPToolName(manifest.ID, "echo"))
	if entry == nil {
		t.Fatalf("registered tools = %v", recorder.names())
	}

	for _, tool := range []string{
		"plugin.acme.hello.not-declared",
		domainplugin.QualifiedMCPToolName(manifest.ID, "echo") + "_suffix",
		"plugin.other-plugin.echo",
		"echo",
	} {
		_, err := entry.caller.CallTool(context.Background(), tool, nil, time.Second, nil)
		var undeclared *appplugin.UndeclaredMCPToolError
		if !errors.As(err, &undeclared) {
			t.Fatalf("CallTool(%q) error = %v, want UndeclaredMCPToolError", tool, err)
		}
		if undeclared.Tool != tool {
			t.Fatalf("undeclared tool = %q, want %q", undeclared.Tool, tool)
		}
	}

	if fiber.State() != domainplugin.StateActive {
		t.Fatalf("state after undeclared calls = %s, want ACTIVE", fiber.State())
	}
	payload, err := entry.caller.CallTool(context.Background(),
		domainplugin.QualifiedMCPToolName(manifest.ID, "echo"),
		map[string]any{"text": "still works"}, time.Second, nil)
	if err != nil {
		t.Fatalf("declared call after rejections: %v", err)
	}
	if !strings.Contains(string(payload), `"text":"still works"`) {
		t.Fatalf("declared call payload = %s", payload)
	}
}

func TestContainerToolCallFailuresAreTyped(t *testing.T) {
	cases := []struct {
		name       string
		tool       string
		timeout    time.Duration
		failsFiber bool
		check      func(error) bool
	}{
		{
			name: "guest never answers",
			tool: "no-result",
			check: func(err error) bool {
				var typed *appplugin.ToolNoResultError
				return errors.As(err, &typed)
			},
		},
		{
			name:       "guest traps",
			tool:       "crash",
			failsFiber: true,
			check: func(err error) bool {
				var typed *appplugin.TrapError
				return errors.As(err, &typed)
			},
		},
		{
			name:       "guest exceeds the class deadline",
			tool:       "hang",
			timeout:    20 * time.Millisecond,
			failsFiber: true,
			check: func(err error) bool {
				var typed *appplugin.DeadlineError
				return errors.As(err, &typed) &&
					errors.Is(err, context.DeadlineExceeded)
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			recorder := newMCPToolRecorder()
			manifest := mcpToolManifest(test.tool)
			_, fiber := loadMCPToolContainer(t, manifest, recorder, 0)
			entry := recorder.get(domainplugin.QualifiedMCPToolName(manifest.ID, test.tool))
			if entry == nil {
				t.Fatalf("registered tools = %v", recorder.names())
			}
			timeout := test.timeout
			if timeout == 0 {
				timeout = time.Second
			}
			_, err := entry.caller.CallTool(context.Background(),
				domainplugin.QualifiedMCPToolName(manifest.ID, test.tool),
				map[string]any{}, timeout, nil)
			if !test.check(err) {
				t.Fatalf("CallTool error = %v", err)
			}
			if test.failsFiber {
				if fiber.State() != domainplugin.StateFailed {
					t.Fatalf("state after failure = %s, want FAILED", fiber.State())
				}
				if names := recorder.names(); len(names) != 0 {
					t.Fatalf("tools after a fault = %v", names)
				}
			} else {
				// A guest that answers nothing leaves the instance usable;
				// the call fails closed but the tool stays listed.
				if fiber.State() != domainplugin.StateActive {
					t.Fatalf("state after an empty reply = %s, want ACTIVE", fiber.State())
				}
				if recorder.get(domainplugin.QualifiedMCPToolName(manifest.ID, test.tool)) == nil {
					t.Fatal("the tool disappeared after a no-result call")
				}
			}
		})
	}
}

func TestContainerBoundsToolArguments(t *testing.T) {
	recorder := newMCPToolRecorder()
	manifest := mcpToolManifest("echo")
	_, _ = loadMCPToolContainer(t, manifest, recorder, 0)
	entry := recorder.get(domainplugin.QualifiedMCPToolName(manifest.ID, "echo"))
	_, err := entry.caller.CallTool(context.Background(),
		domainplugin.QualifiedMCPToolName(manifest.ID, "echo"),
		map[string]any{"text": strings.Repeat("x", appplugin.MaxMCPToolArgumentsBytes+1)},
		time.Second, nil)
	var limit *appplugin.LimitError
	if !errors.As(err, &limit) || limit.Resource != "MCP tool arguments bytes" {
		t.Fatalf("oversized arguments error = %v", err)
	}
}

// TestContainerToolQueueIsBoundedAndCancellable holds one call in the guest,
// admits one waiter, and proves the next caller gets the typed busy error.
// Cancelling the caller releases the instance and fails closed without running
// the queued call against a recycled guest.
func TestContainerToolQueueIsBoundedAndCancellable(t *testing.T) {
	recorder := newMCPToolRecorder()
	manifest := mcpToolManifest("blocking")
	_, _ = loadMCPToolContainer(t, manifest, recorder, 1)
	entry := recorder.get(domainplugin.QualifiedMCPToolName(manifest.ID, "blocking"))
	if entry == nil {
		t.Fatalf("registered tools = %v", recorder.names())
	}
	name := domainplugin.QualifiedMCPToolName(manifest.ID, "blocking")
	started := make(chan struct{}, 1)
	callCtx, cancelCall := context.WithCancel(context.Background())
	defer cancelCall()
	firstDone := make(chan error, 1)
	go func() {
		_, err := entry.caller.CallTool(callCtx, name, nil, time.Minute,
			func(uint32, string) {
				select {
				case started <- struct{}{}:
				default:
				}
			})
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the first call never reached the guest")
	}

	const waiters = 4
	waiterContext, cancelWaiter := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelWaiter()
	results := make(chan error, waiters)
	run := func(ctx context.Context) {
		_, err := entry.caller.CallTool(ctx, name, nil, time.Minute, nil)
		results <- err
	}
	for i := 0; i < waiters; i++ {
		go run(waiterContext)
	}

	// While the first call holds the executing slot, only one queue slot
	// (depth 1) is free. Every other waiter must therefore be rejected
	// immediately; the single admitted waiter stays blocked.
	busy := 0
	received := 0
	for busy < waiters-1 {
		select {
		case err := <-results:
			received++
			var full *appplugin.ToolQueueFullError
			if !errors.As(err, &full) {
				t.Fatalf("a waiter was admitted while the queue was full: %v", err)
			}
			busy++
			if full.Limit != 1 {
				t.Fatalf("busy limit = %d", full.Limit)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d waiters were rejected as busy", busy, waiters-1)
		}
	}

	cancelCall()
	var firstErr error
	select {
	case firstErr = <-firstDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call did not return")
	}
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("cancelled call error = %v", firstErr)
	}
	for received < waiters {
		select {
		case err := <-results:
			received++
			if !errors.Is(err, appplugin.ErrNotActive) &&
				!errors.Is(err, context.Canceled) {
				t.Fatalf("admitted waiter error = %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the admitted waiter did not return")
		}
	}
}
