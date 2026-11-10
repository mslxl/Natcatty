// Package plugin owns the plugin container: the Runtime port, the
// development-directory loader, the fiber lifecycle, and the host-side effect
// stack. Adapters implement Runtime; the composition root wires the concrete
// adapter and the contribution registries.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// LogLevel is the severity a guest passes to the host log import. It is
// generated from the plugin IDL; String and Valid come with it.
type LogLevel = domainpluginabi.LogLevel

// Log severities from the generated ABI.
const (
	LogDebug = domainpluginabi.LogLevelDebug
	LogInfo  = domainpluginabi.LogLevelInfo
	LogWarn  = domainpluginabi.LogLevelWarn
	LogError = domainpluginabi.LogLevelError
)

// HostSink receives the observable host imports: a log line, visible output,
// and the host-service dispatcher. The runtime copies guest memory before
// calling any of them, so the slices are host-owned.
type HostSink struct {
	// Log receives one guest log line. It may be nil.
	Log func(LogLevel, string)
	// Output receives bytes written through the host output import. It may
	// be nil.
	Output func([]byte)
	// Services executes one natcatty:svc/<name>_v1 call. A nil dispatcher
	// makes every service import fail with the unavailable transport status.
	Services ServiceDispatcher
	// LanguageCancelled answers the provider's cooperative cancellation poll.
	// A nil function never reports cancellation.
	LanguageCancelled func(requestID uint64) bool
	// ToolResult accepts one mcp_tool_result reply for an in-flight MCP tool
	// call. The slice is host-owned and bounded by the runtime. A nil callback
	// rejects every reply.
	ToolResult func(requestID uint32, payload []byte) int32
	// ToolProgress accepts optional progress for an in-flight MCP tool call.
	// The message is host-owned and bounded by the runtime. A nil callback
	// reports that no call is waiting.
	ToolProgress func(requestID uint32, progress uint32, message string) int32
	// Provider services the connection-provider stream imports and the
	// cooperative cancellation poll. A nil value makes every provider stream
	// import report that no provider stream exists.
	Provider *ProviderCallbacks
	// Data services the data-provider response bridge and the cooperative
	// cancellation poll. A nil value makes every data-provider import report
	// that no call is waiting.
	Data *DataProviderCallbacks
}

// ProviderCallbacks are the host-side stream operations a provider guest calls
// through the generated imports. The runtime copies guest memory before
// invoking any of them, so byte slices are host-owned. The host terminal owns
// emulation, dimensions, and rendering; these callbacks move raw bytes only.
type ProviderCallbacks struct {
	// InputRead copies up to capacity queued terminal input bytes. The
	// returned slice is copied into guest memory by the runtime. Nil means no
	// stream exists.
	InputRead func(stream uint32, capacity int) []byte
	// InputCredit reports the free bytes in the stream's input queue, or a
	// negative status when the stream does not exist.
	InputCredit func(stream uint32) int
	// OutputWrite accepts one complete output chunk. It returns 0 on success,
	// -2 when the bounded queue has no room, and -3 when the stream is closed.
	OutputWrite func(stream uint32, data []byte) int32
	// OutputCredit reports the free bytes in the stream's output queue.
	OutputCredit func(stream uint32) int
	// Cancelled reports whether the host asked an in-flight open to stop.
	Cancelled func(requestID uint64) bool
}

// ProviderRequest is one host-to-guest connection-provider call. The guest
// answers through the provider_respond host import while the call is in
// flight; the runtime returns the copied JSON response envelope.
type ProviderRequest struct {
	// ID is unique per provider call. The guest echoes it in the response
	// import so a late or mismatched response is rejected.
	ID uint64
	// Method is one member of the generated provider method set.
	Method domainpluginabi.ProviderMethod
	// Payload is the UTF-8 JSON method body. It never contains terminal state
	// or credential material.
	Payload []byte
}

// ProviderNoResponseError reports a provider that returned success without
// answering through the response import. The host treats that as a provider
// failure, not as an empty result.
type ProviderNoResponseError struct {
	Provider string
}

func (e *ProviderNoResponseError) Error() string {
	return fmt.Sprintf("plugin %s did not respond to the provider request", e.Provider)
}

// ProviderCapableInstance is implemented by runtimes that expose the optional
// connection-provider export. A module without it activates normally unless
// its manifest declares a connection contribution.
type ProviderCapableInstance interface {
	// SupportsProvider reports whether the instantiated module actually
	// exports the provider request function.
	SupportsProvider() bool
	InvokeProvider(ctx context.Context, request ProviderRequest) ([]byte, error)
}

// DataNoResponseError reports a provider that returned success without
// answering a data-provider call through the response import. The host treats
// that as a provider failure, not as an empty result.
type DataNoResponseError struct {
	Provider string
}

func (e *DataNoResponseError) Error() string {
	return fmt.Sprintf("plugin %s did not respond to the data provider request", e.Provider)
}

// DataProviderCapableInstance is implemented by runtimes that expose the
// optional data-provider export. A module without it activates normally
// unless its manifest declares a data-provider contribution.
type DataProviderCapableInstance interface {
	// SupportsDataProvider reports whether the instantiated module actually
	// exports the data provider request function.
	SupportsDataProvider() bool
	InvokeDataProvider(ctx context.Context, request DataProviderRequest) ([]byte, error)
}

// LanguageRequest is one host-to-guest editor language-service request. The
// guest answers through the language_respond host import while the call is in
// flight; the runtime returns the copied response payload.
type LanguageRequest struct {
	// ID is unique per provider session. The guest echoes it in the response
	// import so a late or mismatched response is rejected.
	ID uint64
	// Kind is one member of the closed capability set.
	Kind domainplugin.LanguageCapability
	// Payload is the UTF-8 JSON request body.
	Payload []byte
}

// LanguageNoResponseError reports a provider that returned success without
// answering through the response import. The host treats that as a provider
// failure, not as an empty result.
type LanguageNoResponseError struct {
	Provider string
	Kind     domainplugin.LanguageCapability
}

func (e *LanguageNoResponseError) Error() string {
	return fmt.Sprintf("plugin %s did not respond to the %s language request",
		e.Provider, e.Kind)
}

// LanguageCapableInstance is implemented by runtimes that expose the optional
// language provider exports. A module without them activates normally; only a
// manifest that declares a language.provider needs this interface.
type LanguageCapableInstance interface {
	// SupportsLanguage reports whether the instantiated module actually
	// exports the language request function. A runtime implements the
	// interface for every instance; this method distinguishes a module with
	// the optional export from one without it.
	SupportsLanguage() bool
	LanguageRequest(ctx context.Context, request LanguageRequest) ([]byte, error)
	// CancelLanguage asks the guest to discard a superseded request. The
	// optional guest export may be absent, in which case this is a no-op.
	CancelLanguage(ctx context.Context, requestID uint64) error
}

// ServiceRequest is one host-to-guest cross-plugin service request. The guest
// answers through the service_respond host import while the call is in
// flight; the runtime returns the copied broker envelope.
type ServiceRequest struct {
	// ID is unique per provider call. The guest echoes it in the response
	// import so a late or mismatched response is rejected.
	ID uint64
	// Payload is the broker's UTF-8 JSON request envelope.
	Payload []byte
}

// ServiceNoResponseError reports a provider that returned success without
// answering through the service_respond import. The host treats that as an
// internal provider failure, not as an empty result.
type ServiceNoResponseError struct {
	Provider string
}

func (e *ServiceNoResponseError) Error() string {
	return fmt.Sprintf("plugin %s did not respond to the service request", e.Provider)
}

// ServiceCapableInstance is implemented by runtimes that expose the optional
// service provider export. A module without it cannot provide a cross-plugin
// service; the container rejects such a manifest at activation.
type ServiceCapableInstance interface {
	// SupportsService reports whether the instantiated module actually
	// exports the service request function.
	SupportsService() bool
	ServiceRequest(ctx context.Context, request ServiceRequest) ([]byte, error)
}

// ModuleSpec is one module the runtime should instantiate. MemoryMaxBytes is
// the manifest's explicit maximum; the adapter must reject a module whose
// initial memory already exceeds it.
type ModuleSpec struct {
	// Name identifies the module in diagnostics, usually the plugin id.
	Name string
	// WASM is the core WebAssembly binary.
	WASM []byte
	// MemoryMaxBytes is the explicit memory maximum in bytes. It must be
	// between 1 and 256 MiB; zero and unbounded maxima are rejected. The
	// container passes the manifest's effective value, which defaults to
	// 64 MiB when the manifest omits it.
	MemoryMaxBytes int64
}

// Runtime is the consumer-owned port over a WebAssembly execution engine.
// Implementations must be safe to call from multiple goroutines; each
// Instance allows one in-flight guest call at a time.
type Runtime interface {
	// Instantiate validates and starts one reactor-style module. ABI and
	// export validation happen here, before any lifecycle call.
	Instantiate(ctx context.Context, spec ModuleSpec, sink HostSink) (Instance, error)
	// Close releases engine resources, including compiled modules.
	Close(ctx context.Context) error
}

// Instance is one running module. Calls are serialized: a second call while
// one is in flight returns ErrCallInFlight. Host deadlines apply to every
// lifecycle call. A trap or deadline makes the instance unusable and must be
// contained rather than crashing the host; implementations release the
// module, revoke every handle, and return ErrClosed on later calls.
type Instance interface {
	Init(ctx context.Context) error
	Dispatch(ctx context.Context, command string) error
	// DispatchTimed runs one dispatch with an explicit deadline. It exists
	// for MCP tool calls, whose declared timeout class may exceed the ordinary
	// 2 s command deadline. A non-positive timeout uses the runtime call
	// deadline; adapters must clamp a caller-supplied value to the host
	// maximum, never trust it as unbounded.
	DispatchTimed(ctx context.Context, command string, timeout time.Duration) error
	Poll(ctx context.Context) error
	Shutdown(ctx context.Context) error
	Close(ctx context.Context) error
	// Handles returns the generation-tagged table for host-owned resources
	// issued to this instance. Shutdown, Close, a trap, or a deadline
	// revokes every handle in the table.
	Handles() *HandleTable
}

// ABIVersionError reports a module built for a different wire ABI. It is the
// typed error generated from the IDL and is stable across runtime adapters.
type ABIVersionError = domainpluginabi.ABIMismatchError

// MissingExportError reports a required guest export that the module omitted.
type MissingExportError struct {
	Name string
}

func (e *MissingExportError) Error() string {
	return fmt.Sprintf("plugin module does not export %s", e.Name)
}

// UnexpectedExportError reports a guest export outside the generated ABI.
type UnexpectedExportError struct {
	Name string
}

func (e *UnexpectedExportError) Error() string {
	return fmt.Sprintf("plugin module exports unsupported function %s", e.Name)
}

// ImportError reports an import outside the host's allowlist.
type ImportError struct {
	Module string
	Name   string
}

func (e *ImportError) Error() string {
	return fmt.Sprintf("plugin imports unsupported module %q function %q", e.Module, e.Name)
}

// MemoryLimitError reports a module whose initial memory exceeds the
// manifest's declared maximum.
type MemoryLimitError struct {
	Declared int64
	Required int64
}

func (e *MemoryLimitError) Error() string {
	return fmt.Sprintf("plugin module needs %d bytes of initial memory, above the declared %d byte maximum",
		e.Required, e.Declared)
}

// GuestStatusError reports a negative status returned by a guest export.
type GuestStatusError struct {
	Operation string
	Status    int32
}

func (e *GuestStatusError) Error() string {
	return fmt.Sprintf("plugin %s returned status %d", e.Operation, e.Status)
}

// TrapError reports a guest trap or host-import panic caught at the call
// boundary. The instance is unusable afterwards.
type TrapError struct {
	Operation string
	Err       error
}

func (e *TrapError) Error() string {
	return fmt.Sprintf("plugin %s trapped: %v", e.Operation, e.Err)
}

func (e *TrapError) Unwrap() error { return e.Err }

// ErrNotActive reports a call on a fiber that is not ACTIVE.
var ErrNotActive = errors.New("plugin: instance is not active")

// ErrClosed reports a call on a closed runtime or instance.
var ErrClosed = errors.New("plugin: runtime is closed")
