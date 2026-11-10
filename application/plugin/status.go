package plugin

import (
	"errors"
	"fmt"
	"strconv"
)

// EngineMode identifies the WebAssembly execution engine a runtime reports.
// Interpreter mode is a supported degraded mode, not an error: timeouts are
// unchanged and every plugin feature remains available.
type EngineMode string

const (
	// EngineUnknown means the runtime does not report an engine, as with the
	// in-memory fake used by application tests.
	EngineUnknown EngineMode = "unknown"
	// EngineCompiler is wazero's optimizing compiler, the default whenever
	// executable memory is available.
	EngineCompiler EngineMode = "compiler"
	// EngineInterpreter is wazero's interpreter. It is selected automatically
	// on platforms or processes where the compiler cannot allocate executable
	// memory, or when the darwin/amd64 startup self-test fails.
	EngineInterpreter EngineMode = "interpreter"
	// EngineUnavailable means the host supports no plugin engine on this
	// platform; every other app feature keeps working.
	EngineUnavailable EngineMode = "unavailable"
)

// RuntimeStatus is the diagnostics-facing state of the plugin runtime. It is
// exposed to plugin diagnostics and written to the startup log, so support can
// tell "slow because of compatibility mode" from a failure.
type RuntimeStatus struct {
	// GOOS and GOARCH are the target platform, for example "darwin" and
	// "amd64". The adapter fills them from the Go runtime.
	GOOS   string
	GOARCH string
	// Engine is the selected engine, or EngineUnavailable.
	Engine EngineMode
	// Compatibility is true when the interpreter is running as a fallback
	// instead of the compiler. Timeouts are not relaxed in that mode.
	Compatibility bool
	// Reason explains an interpreter fallback or an unavailable platform.
	Reason string
}

// Platform returns the goos/goarch pair for diagnostics.
func (s RuntimeStatus) Platform() string {
	switch {
	case s.GOOS == "" && s.GOARCH == "":
		return "unknown"
	case s.GOOS == "":
		return s.GOARCH
	case s.GOARCH == "":
		return s.GOOS
	default:
		return s.GOOS + "/" + s.GOARCH
	}
}

// Available reports whether plugins can run in this process.
func (s RuntimeStatus) Available() bool {
	return s.Engine == EngineCompiler || s.Engine == EngineInterpreter
}

// StartupLogLine is the one-line mode record written to the startup log. It is
// stable enough for support reports; the app prefixes it with "natcatty: ".
func (s RuntimeStatus) StartupLogLine() string {
	engine := s.Engine
	if engine == "" {
		engine = EngineUnknown
	}
	line := "plugin runtime: engine=" + string(engine) + " platform=" + s.Platform()
	if s.Compatibility {
		line += " compatibility=true"
	}
	if s.Reason != "" {
		line += " reason=" + strconv.Quote(s.Reason)
	}
	return line
}

// Notice is the non-blocking message plugin diagnostics show for a degraded
// or unavailable runtime. It is empty for the normal compiler path.
func (s RuntimeStatus) Notice() string {
	switch s.Engine {
	case EngineInterpreter:
		return "Plugins run in interpreter compatibility mode and may be slower. Timeouts are unchanged."
	case EngineUnavailable:
		platform := s.Platform()
		if platform == "unknown" {
			platform = "this platform"
		}
		return "Plugins are unavailable on " + platform + "; the rest of the app works normally."
	default:
		return ""
	}
}

// TimeoutHint is attached to a deadline failure in compatibility mode so the
// user can tell interpreter slowness from a plugin bug. It never changes the
// deadline itself.
func (s RuntimeStatus) TimeoutHint() string {
	if s.Engine != EngineInterpreter {
		return ""
	}
	return "plugins run in interpreter compatibility mode; timeouts are not relaxed"
}

// RuntimeStatusReporter is implemented by runtimes that can describe their
// engine. The fake runtime in application tests does not need to.
type RuntimeStatusReporter interface {
	RuntimeStatus() RuntimeStatus
}

// ErrPluginsUnavailable reports that the host platform has no supported
// WebAssembly engine. The rest of the app remains usable.
var ErrPluginsUnavailable = errors.New("plugin: plugins are unavailable on this platform")

// UnavailableError names the platform where plugins cannot run.
type UnavailableError struct {
	GOOS   string
	GOARCH string
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("plugin: plugins are unavailable on %s/%s", e.GOOS, e.GOARCH)
}

// Unwrap lets callers test with errors.Is(err, ErrPluginsUnavailable).
func (e *UnavailableError) Unwrap() error { return ErrPluginsUnavailable }
