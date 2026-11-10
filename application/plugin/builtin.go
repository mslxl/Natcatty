package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// BuiltinGroup is the loader group every compiled-in first-party plugin is
// loaded into. It is a normal group: built-ins share the fiber tree, isolate
// scopes, and service resolution with installed and development plugins.
const BuiltinGroup = "built-in"

// Builtin is one first-party plugin compiled into the app. It uses the same
// manifest shape, contribution registry, fiber lifecycle, effect stack, and
// diagnostics as a third-party plugin, but its Setup is a direct Go call and
// its manifest needs no WASM module or wire ABI.
type Builtin struct {
	// Manifest is the compiled-in plugin manifest. It must pass
	// Manifest.ValidateBuiltin.
	Manifest domainplugin.Manifest
	// Setup initializes the plugin. It runs once per activation, after the
	// fiber's settings, permissions, and injections are satisfied, and before
	// any declared contribution is registered.
	Setup BuiltinSetup
}

// BuiltinSetup is the direct Go entry point of a first-party plugin. A nil
// error with a nil instance is rejected as a programming error. The host
// session stays valid until Shutdown; a Setup that panics or exceeds the init
// deadline leaves the fiber FAILED without affecting any sibling fiber.
type BuiltinSetup func(ctx context.Context, host *BuiltinHost) (BuiltinInstance, error)

// BuiltinInstance is one running first-party plugin. The container serializes
// Dispatch, Poll, and Shutdown with unload exactly like guest calls. A panic in
// any method is contained as a TrapError.
type BuiltinInstance interface {
	// Dispatch runs one qualified command, the same identity a WASM guest
	// receives.
	Dispatch(ctx context.Context, command string) error
	// Poll lets the plugin drain queued native UI events.
	Poll(ctx context.Context) error
	// Shutdown runs the plugin's cleanup once, within the host's unload
	// deadline.
	Shutdown(ctx context.Context) error
}

// BuiltinFuncs adapts plain functions to BuiltinInstance. A nil function is a
// no-op, which keeps small built-ins free of boilerplate.
type BuiltinFuncs struct {
	DispatchFunc func(ctx context.Context, command string) error
	PollFunc     func(ctx context.Context) error
	ShutdownFunc func(ctx context.Context) error
}

// Dispatch implements BuiltinInstance.
func (f BuiltinFuncs) Dispatch(ctx context.Context, command string) error {
	if f.DispatchFunc == nil {
		return nil
	}
	return f.DispatchFunc(ctx, command)
}

// Poll implements BuiltinInstance.
func (f BuiltinFuncs) Poll(ctx context.Context) error {
	if f.PollFunc == nil {
		return nil
	}
	return f.PollFunc(ctx)
}

// Shutdown implements BuiltinInstance.
func (f BuiltinFuncs) Shutdown(ctx context.Context) error {
	if f.ShutdownFunc == nil {
		return nil
	}
	return f.ShutdownFunc(ctx)
}

// BuiltinHost is the host-owned session handed to a BuiltinSetup. It exposes
// the same fiber-scoped facilities a guest reaches through host imports:
// validated settings, bounded logging and feedback, the native UI session, and
// the fiber effect stack. Direct Go code may of course call the rest of the
// application directly; these methods keep the lifecycle host-controlled.
type BuiltinHost struct {
	fiber    *Fiber
	settings map[string]any
	ui       *UISession
	logf     func(LogLevel, string)
	output   func([]byte)
}

// Settings returns a copy of the effective, schema-validated configuration.
func (h *BuiltinHost) Settings() map[string]any {
	if h == nil {
		return nil
	}
	return cloneSettings(h.settings)
}

// Log appends one line to the fiber's bounded log ring and the host log hook.
func (h *BuiltinHost) Log(level LogLevel, message string) {
	if h == nil || h.logf == nil {
		return
	}
	h.logf(level, message)
}

// Output shows user-visible feedback through the same host output path a guest
// uses.
func (h *BuiltinHost) Output(data []byte) {
	if h == nil || h.output == nil {
		return
	}
	h.output(data)
}

// NativeUI returns the fiber's retained native UI session, or nil when the
// host has no service session.
func (h *BuiltinHost) NativeUI() *UISession {
	if h == nil {
		return nil
	}
	return h.ui
}

// Dispose registers one cleanup on the fiber's effect stack. Effects run in
// reverse registration order on unload, recycle, and app quit, and each runs
// at most once. A Setup that fails after registering an effect still gets it
// disposed by the failed-activation cleanup.
func (h *BuiltinHost) Dispose(disposer Disposer) error {
	if h == nil || h.fiber == nil {
		return errors.New("plugin: built-in host has no fiber")
	}
	return h.fiber.addEffect(disposer)
}

// LoadBuiltin loads one compiled-in first-party plugin into the same fiber
// tree as a package. The manifest is validated with ValidateBuiltin, the
// activation preconditions (settings, required permissions, injections) are
// applied unchanged, and the fiber ends ACTIVE, PENDING, or FAILED exactly like
// an installed fiber. No Runtime is required: a built-in never crosses the
// WASM ABI.
func (c *Container) LoadBuiltin(ctx context.Context, builtin Builtin) (*Fiber, error) {
	if c == nil || c.contributions == nil {
		return nil, errors.New("plugin: container is not configured")
	}
	if builtin.Setup == nil {
		return nil, fmt.Errorf("plugin %s: built-in setup is required", builtin.Manifest.ID)
	}
	if err := c.ensureGroup(BuiltinGroup); err != nil {
		return nil, err
	}
	manifest := builtin.Manifest
	ref := PluginRef{
		ID:      manifest.ID,
		Name:    manifest.Name,
		Version: manifest.Version,
		Source:  domainplugin.SourceBuiltIn,
	}
	if err := manifest.ValidateBuiltin(); err != nil {
		return c.failManifest(ctx, BuiltinGroup, 1, ref, err)
	}
	fiber := c.newFiber(BuiltinGroup, 1, manifest, ref, "")
	fiber.builtinSetup = builtin.Setup
	if err := c.addFiber(fiber); err != nil {
		return nil, err
	}
	err := c.activateFiber(ctx, fiber, SettingsInheritance{
		Group: c.groupDefaults(BuiltinGroup, manifest.ID),
	})
	return fiber, err
}

// startBuiltin is the direct-call counterpart of start. It opens the same
// host-service session, builds the same host sink, runs Setup under the init
// deadline, and then publishes contributions through the shared registry.
func (c *Container) startBuiltin(ctx context.Context, fiber *Fiber,
	settings map[string]any) error {
	sink, session, err := c.openHostSink(fiber, settings)
	if err != nil {
		return err
	}
	host := &BuiltinHost{
		fiber:    fiber,
		settings: cloneSettings(settings),
		logf:     sink.Log,
		output:   sink.Output,
	}
	if session != nil {
		host.ui = session.NativeUI()
	}
	start := c.now()
	raw, err := callBuiltinSetup(ctx, fiber.builtinSetup, host)
	fiber.observeCall("init", start, err, nil)
	if err != nil {
		c.abortBuiltinSetup(fiber, raw)
		return fmt.Errorf("plugin %s: built-in setup failed: %w", fiber.ref.ID, err)
	}
	instance := &builtinInstance{name: fiber.ref.ID, inner: raw}
	fiber.mu.Lock()
	fiber.instanceRT = instance
	fiber.mu.Unlock()
	if err := c.registerContributions(fiber); err != nil {
		return c.abortBuiltinStart(ctx, fiber, instance, err)
	}
	return nil
}

// abortBuiltinSetup unwinds a Setup that returned an error. A Setup can
// register effects and hand back a partial instance before failing, so every
// effect is still disposed in reverse order and the partial instance is shut
// down within the unload budget. The fiber is left for activateFiber to mark
// FAILED with the Setup error.
func (c *Container) abortBuiltinSetup(fiber *Fiber, raw BuiltinInstance) {
	ctx, cancel := c.unloadContext(context.Background())
	defer cancel()
	_ = c.disposeEffects(ctx, fiber)
	if raw != nil {
		instance := &builtinInstance{name: fiber.ref.ID, inner: raw}
		_ = instance.Shutdown(ctx)
	}
	c.closeServices(fiber)
}

// abortBuiltinStart unwinds a failed built-in activation: effects are disposed
// in reverse order, Shutdown is bounded by the unload timeout, and the service
// session closes. The fiber is left for activateFiber to mark FAILED.
func (c *Container) abortBuiltinStart(ctx context.Context, fiber *Fiber,
	instance *builtinInstance, cause error) error {
	cleanup := []error{cause}
	if err := c.disposeEffects(ctx, fiber); err != nil {
		cleanup = append(cleanup, err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), c.unloadTimeout)
	defer cancel()
	if err := instance.Shutdown(shutdownCtx); err != nil {
		cleanup = append(cleanup, fmt.Errorf("plugin %s: shutdown after failed registration: %w",
			fiber.ref.ID, err))
	}
	if err := instance.Close(shutdownCtx); err != nil {
		cleanup = append(cleanup, fmt.Errorf("plugin %s: close after failed registration: %w",
			fiber.ref.ID, err))
	}
	c.closeServices(fiber)
	return errors.Join(cleanup...)
}

// callBuiltinSetup runs Setup once under the init deadline and contains a
// panic. A Setup that ignores cancellation cannot pin activation: the host
// returns at the deadline and leaves the leaked goroutine to finish.
func callBuiltinSetup(ctx context.Context, setup BuiltinSetup,
	host *BuiltinHost) (BuiltinInstance, error) {
	runCtx, cancel := builtinDeadline(ctx, DefaultInitTimeout)
	defer cancel()
	effective := effectiveLimit(runCtx, DefaultInitTimeout)
	type result struct {
		instance BuiltinInstance
		err      error
	}
	done := make(chan result, 1)
	go func() {
		out := result{}
		defer func() {
			if recovered := recover(); recovered != nil {
				out.instance = nil
				out.err = &TrapError{Operation: "init", Err: fmt.Errorf("panic: %v", recovered)}
			}
			done <- out
		}()
		out.instance, out.err = setup(runCtx, host)
	}()
	select {
	case out := <-done:
		if out.err == nil && out.instance == nil {
			return nil, errors.New("built-in setup returned no instance")
		}
		return out.instance, out.err
	case <-runCtx.Done():
		return nil, &DeadlineError{Operation: "init", Limit: effective}
	}
}

// builtinInstance adapts a first-party BuiltinInstance to the container's
// Instance port. Init is already done by Setup. Every call carries the same
// per-operation deadline as the WASM runtime and turns a panic into a
// TrapError, so the container's recycle path is identical.
type builtinInstance struct {
	name  string
	inner BuiltinInstance
}

func (b *builtinInstance) Init(context.Context) error { return nil }

func (b *builtinInstance) Dispatch(ctx context.Context, command string) error {
	return b.call(ctx, "dispatch", DefaultCallTimeout, func(runCtx context.Context) error {
		return b.inner.Dispatch(runCtx, command)
	})
}

func (b *builtinInstance) DispatchTimed(ctx context.Context, command string,
	timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	} else if timeout > MaxMCPToolTimeout {
		timeout = MaxMCPToolTimeout
	}
	return b.call(ctx, "dispatch", timeout, func(runCtx context.Context) error {
		return b.inner.Dispatch(runCtx, command)
	})
}

func (b *builtinInstance) Poll(ctx context.Context) error {
	return b.call(ctx, "poll", DefaultCallTimeout, func(runCtx context.Context) error {
		return b.inner.Poll(runCtx)
	})
}

func (b *builtinInstance) Shutdown(ctx context.Context) error {
	return b.call(ctx, "shutdown", DefaultUnloadTimeout, func(runCtx context.Context) error {
		return b.inner.Shutdown(runCtx)
	})
}

func (b *builtinInstance) Close(context.Context) error { return nil }

// Handles returns nil: a built-in owns Go objects directly and has no
// generation-tagged host handle table.
func (b *builtinInstance) Handles() *HandleTable { return nil }

// call runs one built-in method with the operation deadline, panic containment,
// and a bounded wait. The inner function receives the derived context.
func (b *builtinInstance) call(ctx context.Context, operation string,
	limit time.Duration, fn func(context.Context) error) error {
	runCtx, cancel := builtinDeadline(ctx, limit)
	defer cancel()
	effective := effectiveLimit(runCtx, limit)
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recovered := recover(); recovered != nil {
				err = &TrapError{Operation: operation, Err: fmt.Errorf("panic: %v", recovered)}
			}
			done <- err
		}()
		err = fn(runCtx)
	}()
	select {
	case err := <-done:
		return err
	case <-runCtx.Done():
		return &DeadlineError{Operation: operation, Limit: effective}
	}
}

// effectiveLimit reports the shorter of the operation limit and a caller
// deadline already on the context, so a DeadlineError names the bound that
// actually fired.
func effectiveLimit(ctx context.Context, limit time.Duration) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return limit
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > limit {
		return limit
	}
	return remaining
}

// builtinDeadline keeps a caller deadline that is already shorter than the
// operation limit; otherwise it applies the host limit.
func builtinDeadline(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = DefaultCallTimeout
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= limit {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, limit)
}
