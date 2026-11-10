package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	protocolmodel "github.com/mslxl/natcatty/domain/model/protocol"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// Connection-provider runtime bounds. They apply to one provider instance and
// every Session it serves.
const (
	// DefaultProviderQueueBytes is the per-direction, per-session queue bound.
	DefaultProviderQueueBytes = 256 << 10
	// ProviderChunkBytes is the largest stream chunk moved across the ABI in
	// either direction.
	ProviderChunkBytes = 64 << 10
	// DefaultProviderOpenTimeout bounds one open, including a host-owned trust
	// or authentication flow the guest drives.
	DefaultProviderOpenTimeout = 30 * time.Second
	// DefaultProviderCallTimeout bounds describe, validate, probe, resize,
	// signal, close, and one pump turn.
	DefaultProviderCallTimeout = 2 * time.Second
	// MaxProviderCallTimeout is the hard cap the wasm adapter applies to one
	// host-to-guest provider call.
	MaxProviderCallTimeout = DefaultProviderOpenTimeout
	// MaxProviderConfigBytes bounds the non-secret Host form values.
	MaxProviderConfigBytes = 64 << 10
	// MaxProviderResponseBytes bounds one provider JSON response.
	MaxProviderResponseBytes = 1 << 20
	// MaxProviderSignalName bounds one declared signal name.
	MaxProviderSignalName = 32
	// MaxProviderDimension bounds a requested or returned terminal dimension.
	MaxProviderDimension = 10000
)

// ProviderLimits overrides the runtime bounds. Zero fields keep the defaults.
type ProviderLimits struct {
	QueueBytes  int
	ChunkBytes  int
	OpenTimeout time.Duration
	CallTimeout time.Duration
}

// Normalize fills zero values with the contract defaults and caps each bound so
// a caller cannot disable backpressure or extend a deadline past the host.
func (l ProviderLimits) Normalize() ProviderLimits {
	if l.QueueBytes <= 0 {
		l.QueueBytes = DefaultProviderQueueBytes
	}
	if l.QueueBytes > DefaultProviderQueueBytes {
		l.QueueBytes = DefaultProviderQueueBytes
	}
	if l.ChunkBytes <= 0 || l.ChunkBytes > ProviderChunkBytes {
		l.ChunkBytes = ProviderChunkBytes
	}
	if l.OpenTimeout <= 0 || l.OpenTimeout > MaxProviderCallTimeout {
		l.OpenTimeout = DefaultProviderOpenTimeout
	}
	if l.CallTimeout <= 0 || l.CallTimeout > DefaultProviderCallTimeout {
		l.CallTimeout = DefaultProviderCallTimeout
	}
	return l
}

// ProviderHostOptions configures a provider host. Audit receives protocol
// claims, opens, failures, and denials; it never receives stream bytes,
// terminal state, credentials, or restore content.
type ProviderHostOptions struct {
	Audit  AuditSink
	Now    func() time.Time
	Limits ProviderLimits
}

// ProviderHost owns every protocol claim and the one provider instance per
// fiber. It is safe for concurrent use.
type ProviderHost struct {
	audit  AuditSink
	now    func() time.Time
	limits ProviderLimits
	next   requestCounter

	mu        sync.Mutex
	claims    map[protocolmodel.ID]*providerEntry
	instances map[domainplugin.FiberID]*providerInstance
	closed    bool
}

// NewProviderHost returns an empty provider host with no claimed protocol.
func NewProviderHost(options ProviderHostOptions) *ProviderHost {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &ProviderHost{
		audit:     options.Audit,
		now:       now,
		limits:    options.Limits.Normalize(),
		claims:    make(map[protocolmodel.ID]*providerEntry),
		instances: make(map[domainplugin.FiberID]*providerInstance),
	}
}

// requestCounter issues monotonic request ids without a package-level atomic.
type requestCounter struct {
	mu    sync.Mutex
	value uint64
}

func (c *requestCounter) Next() uint64 {
	c.mu.Lock()
	c.value++
	value := c.value
	c.mu.Unlock()
	return value
}

// ProviderInvoker is the fiber-side port a provider host calls. A fiber
// implements it with the same one-call serialization as dispatch, so the
// provider never races lifecycle calls on one instance.
type ProviderInvoker interface {
	InvokeProvider(ctx context.Context, request ProviderRequest) ([]byte, error)
	// SupportsProvider reports whether the live module exports
	// natcatty_provider_request.
	SupportsProvider() bool
}

// ConnectionProviderRegistrar publishes one activated provider into the
// Session-facing registry. The composition root maps the protocol onto its
// transport factory. The returned disposer removes that mapping and runs with
// the fiber's other effects in reverse registration order.
type ConnectionProviderRegistrar interface {
	RegisterConnectionProvider(ref PluginRef, contribution domainplugin.ConnectionProvider,
		provider *Provider) (Disposer, error)
}

// providerEntry is one claimed protocol on one provider instance.
type providerEntry struct {
	instance     *providerInstance
	contribution domainplugin.ConnectionProvider
	provider     *Provider
}

// Provider is one registered protocol handle. It is safe for concurrent use.
type Provider struct {
	host         *ProviderHost
	instance     *providerInstance
	contribution domainplugin.ConnectionProvider
	protocol     protocolmodel.ID
}

// Protocol returns the claimed protocol id.
func (p *Provider) Protocol() string { return string(p.protocol) }

// Contribution returns the static declaration read at activation.
func (p *Provider) Contribution() domainplugin.ConnectionProvider { return p.contribution }

// Register claims contribution.Protocol for the fiber and publishes one
// Provider handle. A reserved built-in id, an invalid declaration, a duplicate
// claim, and a module without the provider export are rejected. Registration
// is transactional: a rejected call leaves no claim behind.
func (h *ProviderHost) Register(fiberID domainplugin.FiberID, ref PluginRef,
	contribution domainplugin.ConnectionProvider, invoker ProviderInvoker) (*Provider, error) {

	if h == nil {
		return nil, errors.New("plugin: provider host is not configured")
	}
	if invoker == nil || !invoker.SupportsProvider() {
		return nil, fmt.Errorf("plugin %s: connection provider %q requires the %s export",
			ref.ID, contribution.Protocol, domainpluginabi.ExportProviderRequest)
	}
	id := protocolmodel.ID(contribution.Protocol)
	if protocolmodel.Reserved(id) {
		h.recordProviderEvent(ref.ID, contribution.Protocol, "claim", "reserved", 0)
		return nil, fmt.Errorf("%w: %q", protocolmodel.ErrReservedProtocol, id)
	}
	if err := domainplugin.ValidateConnectionProvider(ref.ID, contribution, 0); err != nil {
		h.recordProviderEvent(ref.ID, contribution.Protocol, "claim", "invalid", 0)
		return nil, err
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if _, exists := h.claims[id]; exists {
		h.mu.Unlock()
		h.recordProviderEvent(ref.ID, contribution.Protocol, "claim", "duplicate", 0)
		return nil, fmt.Errorf("%w: %q", protocolmodel.ErrDuplicateProtocol, id)
	}
	instance := h.instances[fiberID]
	if instance == nil {
		instance = newProviderInstance(h, fiberID, ref, invoker)
		h.instances[fiberID] = instance
		go instance.pumpLoop()
	} else if instance.invoker != invoker || !instance.activeState() {
		h.mu.Unlock()
		return nil, fmt.Errorf("plugin %s: provider instance for fiber %q is not usable",
			ref.ID, fiberID)
	}
	provider := &Provider{
		host: h, instance: instance, contribution: contribution, protocol: id,
	}
	h.claims[id] = &providerEntry{
		instance: instance, contribution: contribution, provider: provider,
	}
	h.mu.Unlock()
	h.recordProviderEvent(ref.ID, contribution.Protocol, "claim", "ok", 0)
	return provider, nil
}

// Unregister removes provider's claim and fails every live stream using that
// protocol, even when the instance still serves another claimed protocol.
// Removing the final claim tears the whole instance down. It is idempotent.
func (h *ProviderHost) Unregister(provider *Provider) error {
	if h == nil || provider == nil {
		return nil
	}
	h.mu.Lock()
	entry, ok := h.claims[provider.protocol]
	if !ok || entry.provider != provider {
		h.mu.Unlock()
		return nil
	}
	delete(h.claims, provider.protocol)
	remaining := 0
	for _, claim := range h.claims {
		if claim.instance == entry.instance {
			remaining++
		}
	}
	if remaining == 0 {
		delete(h.instances, entry.instance.fiberID)
	}
	h.mu.Unlock()
	if remaining == 0 {
		entry.instance.fail(domainplugin.ProviderErrorFaulted, "provider unloaded")
		return nil
	}
	// The module stays loaded for the sibling protocols, but the removed
	// protocol's Sessions cannot continue against an unregistered provider.
	entry.instance.failProtocol(string(provider.protocol),
		domainplugin.ProviderErrorFaulted, "provider unloaded")
	return nil
}

// Close unregisters every claim and tears down every instance. Containers
// normally dispose each fiber's claims, so Close is the defensive whole-host
// shutdown path.
func (h *ProviderHost) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	instances := make([]*providerInstance, 0, len(h.instances))
	for _, instance := range h.instances {
		instances = append(instances, instance)
	}
	h.claims = make(map[protocolmodel.ID]*providerEntry)
	h.instances = make(map[domainplugin.FiberID]*providerInstance)
	h.mu.Unlock()
	for _, instance := range instances {
		instance.fail(domainplugin.ProviderErrorFaulted, "provider host closed")
	}
}

// Lookup returns the provider currently claiming protocol.
func (h *ProviderHost) Lookup(protocol string) (*Provider, bool) {
	if h == nil {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.claims[protocolmodel.ID(protocol)]
	if !ok {
		return nil, false
	}
	return entry.provider, true
}

// Claimed reports whether protocol is currently claimed.
func (h *ProviderHost) Claimed(protocol string) bool {
	_, ok := h.Lookup(protocol)
	return ok
}

// Callbacks returns the host imports for one fiber's provider streams. The
// callbacks resolve the fiber's instance on every call, so they stay valid
// across registration, unload, and recycle. A fiber with no provider instance
// gets callbacks that reject every stream. The result is never nil.
func (h *ProviderHost) Callbacks(fiberID domainplugin.FiberID) *ProviderCallbacks {
	return &ProviderCallbacks{
		InputRead: func(stream uint32, capacity int) []byte {
			instance := h.instance(fiberID)
			if instance == nil {
				return nil
			}
			instance.mu.Lock()
			value := instance.streams[stream]
			instance.mu.Unlock()
			if value == nil {
				return nil
			}
			return value.readInput(capacity)
		},
		InputCredit: func(stream uint32) int {
			instance := h.instance(fiberID)
			if instance == nil {
				return -1
			}
			instance.mu.Lock()
			value := instance.streams[stream]
			instance.mu.Unlock()
			if value == nil {
				return -1
			}
			return value.inputCredit()
		},
		OutputWrite: func(stream uint32, data []byte) int32 {
			instance := h.instance(fiberID)
			if instance == nil {
				return -3
			}
			instance.mu.Lock()
			value := instance.streams[stream]
			instance.mu.Unlock()
			if value == nil {
				return -3
			}
			return value.writeOutput(data)
		},
		OutputCredit: func(stream uint32) int {
			instance := h.instance(fiberID)
			if instance == nil {
				return -1
			}
			instance.mu.Lock()
			value := instance.streams[stream]
			instance.mu.Unlock()
			if value == nil {
				return -1
			}
			return value.outputCredit()
		},
		Cancelled: func(requestID uint64) bool {
			instance := h.instance(fiberID)
			return instance != nil && instance.cancelRequested(requestID)
		},
	}
}

func (h *ProviderHost) instance(fiberID domainplugin.FiberID) *providerInstance {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.instances[fiberID]
}

// Describe asks the provider to confirm its static description and returns the
// declaration read at activation.
func (p *Provider) Describe(ctx context.Context) (domainplugin.ConnectionProvider, error) {
	if err := p.ready(); err != nil {
		return domainplugin.ConnectionProvider{}, err
	}
	response, err := p.call(ctx, domainpluginabi.ProviderMethodDescribe, struct{}{})
	if err != nil {
		return domainplugin.ConnectionProvider{}, err
	}
	if err := p.checkResponse(response); err != nil {
		return domainplugin.ConnectionProvider{}, err
	}
	return p.contribution, nil
}

// Validate forwards one non-secret configuration for the provider's extra
// validation. The host bounds the payload; the provider cannot widen a Host
// field schema it declared statically.
func (p *Provider) Validate(ctx context.Context, config []byte) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := checkProviderConfig(config, p.instance.ref.ID, string(p.protocol)); err != nil {
		return err
	}
	response, err := p.call(ctx, domainpluginabi.ProviderMethodValidate,
		map[string]json.RawMessage{"config": rawProviderConfig(config)})
	if err != nil {
		return err
	}
	return p.checkResponse(response)
}

// Probe performs a bounded reachability check. A provider without the probe
// capability rejects the call before the guest sees it.
func (p *Provider) Probe(ctx context.Context, config []byte) error {
	if err := p.ready(); err != nil {
		return err
	}
	if !p.contribution.Capabilities.Probe {
		p.host.recordProviderEvent(p.instance.ref.ID, string(p.protocol), "denial", "probe", 0)
		return fmt.Errorf("%w: probe", domainplugin.ErrProviderUnsupported)
	}
	if err := checkProviderConfig(config, p.instance.ref.ID, string(p.protocol)); err != nil {
		return err
	}
	response, err := p.call(ctx, domainpluginabi.ProviderMethodProbe,
		map[string]json.RawMessage{"config": rawProviderConfig(config)})
	if err != nil {
		return err
	}
	return p.checkResponse(response)
}

// ProviderOpenRequest is one terminal connection request. It carries endpoint
// settings, initial dimensions, and opaque host form values; it never carries
// terminal state, scrollback, or credential material.
type ProviderOpenRequest struct {
	HostID   string
	Hostname string
	Port     int
	Username string
	Label    string
	Cols     int
	Rows     int
	// Config is the non-secret Host field object. Empty means no settings.
	Config []byte
	// Restore is the bounded opaque restore descriptor from the workspace.
	Restore []byte
}

type providerOpenPayload struct {
	Stream   uint32          `json:"stream"`
	HostID   string          `json:"hostId,omitempty"`
	Hostname string          `json:"hostname,omitempty"`
	Port     int             `json:"port,omitempty"`
	Username string          `json:"username,omitempty"`
	Label    string          `json:"label,omitempty"`
	Cols     int             `json:"cols"`
	Rows     int             `json:"rows"`
	Config   json.RawMessage `json:"config,omitempty"`
	Restore  string          `json:"restore,omitempty"`
}

// Open starts one provider Session and returns its raw byte stream with the
// provider's initial dimensions and bounded opaque restore state. Cancelling
// ctx asks the guest to abandon the open through the provider_cancelled poll
// and discards any stream the guest still returns, so a half-open connection
// is released.
func (p *Provider) Open(ctx context.Context, request ProviderOpenRequest) (*ProviderStream, error) {
	if p == nil || p.host == nil {
		return nil, ErrClosed
	}
	started := p.host.now()
	stream, err := p.open(ctx, request)
	status := "ok"
	if err != nil {
		status = providerErrorStatus(err)
	}
	p.host.recordProviderEvent(p.instance.ref.ID, string(p.protocol), "open", status,
		p.host.now().Sub(started))
	return stream, err
}

func (p *Provider) open(ctx context.Context, request ProviderOpenRequest) (*ProviderStream, error) {
	if err := p.ready(); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorCancelled, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "open was cancelled",
		}
	}
	if request.Cols <= 0 || request.Rows <= 0 ||
		request.Cols > MaxProviderDimension || request.Rows > MaxProviderDimension {
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "invalid initial dimensions",
		}
	}
	if err := checkProviderConfig(request.Config, p.instance.ref.ID, string(p.protocol)); err != nil {
		return nil, err
	}
	restoreMax := p.contribution.Capabilities.EffectiveRestoreMax()
	if len(request.Restore) > restoreMax {
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "restore descriptor is too large",
		}
	}
	stream, err := p.instance.beginStream(p)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(providerOpenPayload{
		Stream:   stream.id,
		HostID:   boundProviderText(request.HostID, 128),
		Hostname: boundProviderText(request.Hostname, 512),
		Port:     request.Port,
		Username: boundProviderText(request.Username, 256),
		Label:    boundProviderText(request.Label, 256),
		Cols:     request.Cols,
		Rows:     request.Rows,
		Config:   rawProviderConfig(request.Config),
		Restore:  base64.StdEncoding.EncodeToString(request.Restore),
	})
	if err != nil {
		p.instance.dropStream(stream)
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "encode open request",
		}
	}

	requestID := p.host.next.Next()
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.host.limits.OpenTimeout)
	defer cancel()
	watchDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			p.instance.cancelRequest(requestID)
		case <-watchDone:
		case <-callCtx.Done():
		}
	}()
	responsePayload, callErr := p.instance.invoker.InvokeProvider(callCtx, ProviderRequest{
		ID: requestID, Method: domainpluginabi.ProviderMethodOpen, Payload: payload,
	})
	close(watchDone)
	if callErr != nil {
		p.instance.dropStream(stream)
		if ctx.Err() != nil {
			return nil, &domainplugin.ProviderError{
				Kind: domainplugin.ProviderErrorCancelled, Provider: p.instance.ref.ID,
				Protocol: string(p.protocol), Message: "open was cancelled",
			}
		}
		failure := p.instance.mapInvokeError(callErr)
		if providerErrorIsFatal(callErr) {
			p.instance.fail(domainplugin.ProviderErrorFaulted, "provider became unusable")
		}
		return nil, failure
	}
	if ctx.Err() != nil {
		// The provider answered after the host stopped waiting. Discard the
		// half-open stream instead of publishing it.
		p.instance.dropStream(stream)
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorCancelled, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "open was cancelled",
		}
	}
	response, err := decodeProviderResponse(responsePayload, p.instance.ref.ID, string(p.protocol))
	if err != nil {
		p.instance.dropStream(stream)
		return nil, err
	}
	if err := p.checkResponse(response); err != nil {
		p.instance.dropStream(stream)
		return nil, err
	}
	cols, rows := response.Cols, response.Rows
	if cols <= 0 || rows <= 0 || cols > MaxProviderDimension || rows > MaxProviderDimension {
		cols, rows = request.Cols, request.Rows
	}
	var restore []byte
	if response.Restore != "" {
		decoded, decodeErr := base64.StdEncoding.DecodeString(response.Restore)
		if decodeErr != nil || len(decoded) > restoreMax {
			p.instance.dropStream(stream)
			return nil, &domainplugin.ProviderError{
				Kind: domainplugin.ProviderErrorProtocol, Provider: p.instance.ref.ID,
				Protocol: string(p.protocol), Message: "invalid restore descriptor",
			}
		}
		restore = decoded
	}
	stream.publish(cols, rows, restore)
	p.instance.schedule(stream.id)
	return stream, nil
}

// call runs one non-open provider method under the ordinary call bound.
func (p *Provider) call(ctx context.Context, method domainpluginabi.ProviderMethod,
	value any) (connectionResponse, error) {

	payload, err := json.Marshal(value)
	if err != nil {
		return connectionResponse{}, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "encode provider request",
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return connectionResponse{}, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorCancelled, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "request was cancelled",
		}
	}
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.host.limits.CallTimeout)
	defer cancel()
	requestID := p.host.next.Next()
	raw, err := p.instance.invoker.InvokeProvider(callCtx, ProviderRequest{
		ID: requestID, Method: method, Payload: payload,
	})
	if err != nil {
		if providerErrorIsFatal(err) {
			p.instance.fail(domainplugin.ProviderErrorFaulted, "provider became unusable")
		}
		return connectionResponse{}, p.instance.mapInvokeError(err)
	}
	return decodeProviderResponse(raw, p.instance.ref.ID, string(p.protocol))
}

// checkResponse maps a provider status string to the closed error vocabulary.
func (p *Provider) checkResponse(response connectionResponse) error {
	status := strings.TrimSpace(response.Status)
	if status == "" {
		status = strings.TrimSpace(response.Error)
	}
	if status == "ok" {
		return nil
	}
	return mapProviderStatus(p.instance.ref.ID, string(p.protocol), status, response.Message)
}

// mapProviderStatus is the one place a guest status becomes a typed error. An
// empty status is success; an unknown status is a protocol error.
func mapProviderStatus(providerID, protocol, status, message string) error {
	status = strings.TrimSpace(status)
	if status == "" || status == "ok" {
		return nil
	}
	kind, known := domainplugin.ProviderErrorKindByName(status)
	if !known {
		kind = domainplugin.ProviderErrorProtocol
	}
	return &domainplugin.ProviderError{
		Kind: kind, Provider: providerID, Protocol: protocol,
		Message: boundProviderText(message, 256),
	}
}

func (p *Provider) ready() error {
	if p == nil || p.host == nil || p.instance == nil {
		return ErrClosed
	}
	if !p.instance.activeState() {
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorFaulted, Provider: p.instance.ref.ID,
			Protocol: string(p.protocol), Message: "provider is not active",
		}
	}
	return nil
}

// connectionResponse is the JSON envelope every provider method returns. More
// asks the host to run another pump turn for the stream immediately.
type connectionResponse struct {
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Restore string `json:"restore,omitempty"`
	More    bool   `json:"more,omitempty"`
}

func decodeProviderResponse(payload []byte, provider, protocol string) (connectionResponse, error) {
	if len(payload) == 0 {
		return connectionResponse{}, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: provider,
			Protocol: protocol, Message: "provider returned no response",
		}
	}
	if len(payload) > MaxProviderResponseBytes {
		return connectionResponse{}, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: provider,
			Protocol: protocol, Message: "provider response is too large",
		}
	}
	var response connectionResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return connectionResponse{}, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: provider,
			Protocol: protocol, Message: "malformed provider response",
		}
	}
	return response, nil
}

func checkProviderConfig(config []byte, provider, protocol string) error {
	if len(config) > MaxProviderConfigBytes {
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: provider,
			Protocol: protocol, Message: "configuration is too large",
		}
	}
	if len(config) > 0 {
		// The Host form schema is a JSON object. Arrays, scalars, and null are
		// valid JSON but not a configuration, so reject them here rather than
		// forwarding a value the provider cannot interpret.
		var value any
		if err := json.Unmarshal(config, &value); err != nil {
			return &domainplugin.ProviderError{
				Kind: domainplugin.ProviderErrorProtocol, Provider: provider,
				Protocol: protocol, Message: "configuration is not a JSON object",
			}
		}
		if _, ok := value.(map[string]any); !ok {
			return &domainplugin.ProviderError{
				Kind: domainplugin.ProviderErrorProtocol, Provider: provider,
				Protocol: protocol, Message: "configuration is not a JSON object",
			}
		}
	}
	return nil
}

func rawProviderConfig(config []byte) json.RawMessage {
	if len(config) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(config)
}

func boundProviderText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max]
}

func providerErrorStatus(err error) string {
	var providerErr *domainplugin.ProviderError
	if errors.As(err, &providerErr) {
		return string(providerErr.Kind)
	}
	return string(domainplugin.ProviderErrorFaulted)
}

// providerInstance is the one module-backed provider for a fiber, shared by
// every Session and protocol it serves.
type providerInstance struct {
	host    *ProviderHost
	fiberID domainplugin.FiberID
	ref     PluginRef
	invoker ProviderInvoker
	limits  ProviderLimits
	pumps   *pumpQueue

	mu        sync.Mutex
	active    bool
	streams   map[uint32]*ProviderStream
	counts    map[protocolmodel.ID]int
	nextID    uint32
	cancelled [providerCancelWindow]uint64
	cancelAt  int
	cancelLen int
}

// providerCancelWindow bounds the ids remembered for cooperative
// cancellation. Ids are monotonic and never reused, so a stale entry can never
// report cancellation for a later open.
const providerCancelWindow = 64

func newProviderInstance(host *ProviderHost, fiberID domainplugin.FiberID,
	ref PluginRef, invoker ProviderInvoker) *providerInstance {

	return &providerInstance{
		host: host, fiberID: fiberID, ref: ref, invoker: invoker,
		limits: host.limits, pumps: newPumpQueue(), active: true,
		streams: make(map[uint32]*ProviderStream),
		counts:  make(map[protocolmodel.ID]int),
	}
}

func (i *providerInstance) activeState() bool {
	i.mu.Lock()
	active := i.active
	i.mu.Unlock()
	return active
}

// cancelRequest records the cooperative cancellation an in-flight open polls.
func (i *providerInstance) cancelRequest(requestID uint64) {
	i.mu.Lock()
	i.cancelled[i.cancelAt] = requestID
	i.cancelAt = (i.cancelAt + 1) % providerCancelWindow
	if i.cancelLen < providerCancelWindow {
		i.cancelLen++
	}
	i.mu.Unlock()
}

func (i *providerInstance) cancelRequested(requestID uint64) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	for index := 0; index < i.cancelLen; index++ {
		if i.cancelled[index] == requestID {
			return true
		}
	}
	return false
}

func (i *providerInstance) mapInvokeError(err error) error {
	var providerErr *domainplugin.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr
	}
	message := boundProviderText(err.Error(), 256)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorTimeout, Provider: i.ref.ID, Message: message,
		}
	case errors.Is(err, context.Canceled):
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorCancelled, Provider: i.ref.ID, Message: message,
		}
	case errors.Is(err, ErrNotActive), errors.Is(err, ErrClosed), IsUnusable(err):
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorFaulted, Provider: i.ref.ID, Message: message,
		}
	default:
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: i.ref.ID, Message: message,
		}
	}
}

func providerErrorIsFatal(err error) bool {
	if errors.Is(err, ErrNotActive) || errors.Is(err, ErrClosed) || IsUnusable(err) {
		return true
	}
	var providerErr *domainplugin.ProviderError
	return errors.As(err, &providerErr) && providerErr.Kind == domainplugin.ProviderErrorFaulted
}

// beginStream reserves one stream id and its queue pair before the guest sees
// the open request, so callbacks from inside open resolve. It rejects a
// provider at its declared session bound.
func (i *providerInstance) beginStream(p *Provider) (*ProviderStream, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.active {
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorFaulted, Provider: i.ref.ID,
			Protocol: p.contribution.Protocol, Message: "provider is not active",
		}
	}
	if i.counts[p.protocol] >= p.contribution.Capabilities.EffectiveMaxSessions() {
		return nil, &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: i.ref.ID,
			Protocol: p.contribution.Protocol, Message: "provider session limit reached",
		}
	}
	for {
		i.nextID++
		if i.nextID == 0 {
			i.nextID++
		}
		if _, exists := i.streams[i.nextID]; !exists {
			break
		}
	}
	stream := newProviderStream(i, p, i.nextID)
	i.streams[stream.id] = stream
	i.counts[p.protocol]++
	return stream, nil
}

func (i *providerInstance) dropStream(stream *ProviderStream) {
	if stream == nil {
		return
	}
	stream.closeQueues(io.EOF)
	i.removeStream(stream)
}

func (i *providerInstance) removeStream(stream *ProviderStream) {
	i.mu.Lock()
	if _, exists := i.streams[stream.id]; exists {
		delete(i.streams, stream.id)
		protocol := protocolmodel.ID(stream.protocol)
		count := i.counts[protocol] - 1
		if count <= 0 {
			delete(i.counts, protocol)
		} else {
			i.counts[protocol] = count
		}
	}
	i.mu.Unlock()
}

// schedule marks one stream ready for a pump turn. Coalescing keeps repeated
// input writes from queueing the same stream twice.
func (i *providerInstance) schedule(id uint32) { i.pumps.push(id) }

// pumpLoop runs one bounded pump turn per ready stream in arrival order, so a
// slow stream cannot starve its siblings. Host input and drained output both
// enqueue a turn.
func (i *providerInstance) pumpLoop() {
	for {
		id, ok := i.pumps.pop()
		if !ok {
			return
		}
		if !i.activeState() {
			return
		}
		stream := i.streamLocked(id)
		if stream == nil || stream.isClosed() {
			continue
		}
		payload, _ := json.Marshal(struct {
			Stream uint32 `json:"stream"`
		}{Stream: id})
		callCtx, cancel := context.WithTimeout(context.Background(), i.limits.CallTimeout)
		requestID := i.host.next.Next()
		takenBefore := stream.inputTaken()
		raw, err := i.invoker.InvokeProvider(callCtx, ProviderRequest{
			ID: requestID, Method: domainpluginabi.ProviderMethodPump, Payload: payload,
		})
		cancel()
		if err != nil {
			if providerErrorIsFatal(err) {
				i.fail(domainplugin.ProviderErrorFaulted, "provider became unusable")
				return
			}
			if errors.Is(err, ErrCallInFlight) {
				// A dispatch holds the one guest call slot. Retry this stream
				// after a short cadence instead of dropping its turn.
				time.AfterFunc(5*time.Millisecond, func() { i.schedule(id) })
			}
			continue
		}
		response, decodeErr := decodeProviderResponse(raw, i.ref.ID, stream.protocol)
		if decodeErr != nil {
			stream.failWith(decodeErr)
			continue
		}
		if err := mapProviderStatus(i.ref.ID, stream.protocol,
			response.Status, response.Message); err != nil {
			stream.failWith(err)
			continue
		}
		// A guest that stopped reading because the host output queue is full
		// leaves input queued. Reschedule only after it made progress, so a
		// credit-blocked pump does not spin; a terminal read schedules it too.
		if response.More || (stream.inputPending() && stream.inputTaken() > takenBefore) {
			i.schedule(id)
		}
	}
}

func (i *providerInstance) streamLocked(id uint32) *ProviderStream {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.streams[id]
}

// fail marks the instance unusable and fails every stream it served with a
// typed status. It is idempotent, never calls the guest, and removes this
// instance's claims so a reload can register a fresh module.
func (i *providerInstance) fail(kind domainplugin.ProviderErrorKind, message string) {
	i.mu.Lock()
	if !i.active && len(i.streams) == 0 {
		i.mu.Unlock()
		return
	}
	i.active = false
	streams := make([]*ProviderStream, 0, len(i.streams))
	for _, stream := range i.streams {
		streams = append(streams, stream)
	}
	i.streams = make(map[uint32]*ProviderStream)
	i.counts = make(map[protocolmodel.ID]int)
	i.mu.Unlock()
	i.pumps.stop()
	i.host.detachInstance(i)
	for _, stream := range streams {
		stream.failWith(&domainplugin.ProviderError{
			Kind: kind, Provider: i.ref.ID, Protocol: stream.protocol, Message: message,
		})
	}
	i.host.recordProviderEvent(i.ref.ID, "", "failure", string(kind), 0)
}

// failProtocol fails every live stream using protocol while keeping the
// instance and its sibling protocols running. It is the protocol-scoped
// counterpart of fail: removing one claim cannot leave that protocol's
// Sessions attached to an unregistered provider.
func (i *providerInstance) failProtocol(protocol string,
	kind domainplugin.ProviderErrorKind, message string) {

	i.mu.Lock()
	if !i.active {
		i.mu.Unlock()
		return
	}
	streams := make([]*ProviderStream, 0)
	for _, stream := range i.streams {
		if stream.protocol == protocol {
			streams = append(streams, stream)
		}
	}
	delete(i.counts, protocolmodel.ID(protocol))
	i.mu.Unlock()
	for _, stream := range streams {
		stream.failWith(&domainplugin.ProviderError{
			Kind: kind, Provider: i.ref.ID, Protocol: protocol, Message: message,
		})
	}
	i.host.recordProviderEvent(i.ref.ID, protocol, "failure", string(kind), 0)
}

// detachInstance removes every claim and the registry entry of a failed
// instance, so a reload starts from a clean claim set.
func (h *ProviderHost) detachInstance(instance *providerInstance) {
	h.mu.Lock()
	for id, entry := range h.claims {
		if entry.instance == instance {
			delete(h.claims, id)
		}
	}
	if h.instances[instance.fiberID] == instance {
		delete(h.instances, instance.fiberID)
	}
	h.mu.Unlock()
}

// pumpQueue is the fair round-robin ready set. One stream is dispatched per
// turn; a slow stream cannot starve siblings because every ready stream is
// served in arrival order.
type pumpQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	ready  []uint32
	queued map[uint32]bool
	closed bool
}

func newPumpQueue() *pumpQueue {
	queue := &pumpQueue{queued: make(map[uint32]bool)}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *pumpQueue) push(id uint32) {
	q.mu.Lock()
	if !q.closed && !q.queued[id] {
		q.queued[id] = true
		q.ready = append(q.ready, id)
		q.cond.Signal()
	}
	q.mu.Unlock()
}

func (q *pumpQueue) pop() (uint32, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.ready) == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return 0, false
	}
	id := q.ready[0]
	q.ready = q.ready[1:]
	delete(q.queued, id)
	return id, true
}

func (q *pumpQueue) stop() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

// ProviderStream is one Session's raw provider stream. It is a byte duplex;
// the host terminal still owns emulation, dimensions, and rendering.
type ProviderStream struct {
	instance     *providerInstance
	id           uint32
	providerID   string
	protocol     string
	contribution domainplugin.ConnectionProvider
	initialCols  int
	initialRows  int
	restore      []byte
	input        *streamQueue
	output       *streamQueue
	closedCh     chan struct{}
	closeOnce    sync.Once

	mu      sync.Mutex
	closed  bool
	failure error
}

func newProviderStream(instance *providerInstance, provider *Provider,
	id uint32) *ProviderStream {

	return &ProviderStream{
		instance:     instance,
		id:           id,
		providerID:   instance.ref.ID,
		protocol:     provider.contribution.Protocol,
		contribution: provider.contribution,
		input:        newStreamQueue(instance.limits.QueueBytes),
		output:       newStreamQueue(instance.limits.QueueBytes),
		closedCh:     make(chan struct{}),
	}
}

func (s *ProviderStream) publish(cols, rows int, restore []byte) {
	s.initialCols, s.initialRows = cols, rows
	s.restore = append([]byte(nil), restore...)
}

// ID returns the guest-facing stream id (diagnostics only).
func (s *ProviderStream) ID() uint32 { return s.id }

// ProviderID returns the owning plugin id.
func (s *ProviderStream) ProviderID() string { return s.providerID }

// Protocol returns the provider-owned protocol.
func (s *ProviderStream) Protocol() string { return s.protocol }

// InitialSize returns the dimensions the provider reported at open.
func (s *ProviderStream) InitialSize() (int, int) { return s.initialCols, s.initialRows }

// RestoreDescriptor returns a copy of the bounded opaque restore state.
func (s *ProviderStream) RestoreDescriptor() []byte {
	return append([]byte(nil), s.restore...)
}

// Read blocks for output bytes. A provider failure is returned as a typed
// domain error once the already-queued bytes are drained.
func (s *ProviderStream) Read(p []byte) (int, error) {
	n, err := s.output.readBlocking(p)
	if n > 0 {
		// Reading output frees queue credit the guest may be waiting for.
		s.instance.schedule(s.id)
	}
	return n, err
}

// Write moves terminal input into the bounded provider queue, blocking while
// the provider is behind. It never coalesces or drops bytes.
func (s *ProviderStream) Write(p []byte) (int, error) {
	if err := s.failureError(); err != nil {
		return 0, err
	}
	total := 0
	for len(p) > 0 {
		n, err := s.input.writeBlocking(p)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
	}
	s.instance.schedule(s.id)
	return total, nil
}

// Resize asks the provider to change its remote window size. A provider that
// did not declare resize rejects the call before the guest sees it.
func (s *ProviderStream) Resize(cols, rows int) error {
	if !s.contribution.Capabilities.Resize {
		s.instance.host.recordProviderEvent(s.providerID, s.protocol, "denial", "resize", 0)
		return fmt.Errorf("%w: resize", domainplugin.ErrProviderUnsupported)
	}
	if cols <= 0 || rows <= 0 || cols > MaxProviderDimension || rows > MaxProviderDimension {
		return &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorProtocol, Provider: s.providerID,
			Protocol: s.protocol, Message: "invalid dimensions",
		}
	}
	if err := s.failureError(); err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		Stream uint32 `json:"stream"`
		Cols   int    `json:"cols"`
		Rows   int    `json:"rows"`
	}{s.id, cols, rows})
	return s.callMethod(domainpluginabi.ProviderMethodResize, payload)
}

// Signal asks the provider to deliver one declared signal.
func (s *ProviderStream) Signal(name string) error {
	if !s.contribution.Capabilities.SupportsSignal(name) {
		s.instance.host.recordProviderEvent(s.providerID, s.protocol, "denial", "signal", 0)
		return fmt.Errorf("%w: signal %q", domainplugin.ErrProviderUnsupported, name)
	}
	if len(name) > MaxProviderSignalName {
		return fmt.Errorf("%w: signal name is too long", domainplugin.ErrProviderUnsupported)
	}
	if err := s.failureError(); err != nil {
		return err
	}
	payload, _ := json.Marshal(struct {
		Stream uint32 `json:"stream"`
		Name   string `json:"name"`
	}{s.id, name})
	return s.callMethod(domainpluginabi.ProviderMethodSignal, payload)
}

func (s *ProviderStream) callMethod(method domainpluginabi.ProviderMethod, payload []byte) error {
	callCtx, cancel := context.WithTimeout(context.Background(), s.instance.limits.CallTimeout)
	defer cancel()
	requestID := s.instance.host.next.Next()
	raw, err := s.instance.invoker.InvokeProvider(callCtx, ProviderRequest{
		ID: requestID, Method: method, Payload: payload,
	})
	if err != nil {
		if providerErrorIsFatal(err) {
			s.instance.fail(domainplugin.ProviderErrorFaulted, "provider became unusable")
		}
		return s.instance.mapInvokeError(err)
	}
	response, err := decodeProviderResponse(raw, s.providerID, s.protocol)
	if err != nil {
		return err
	}
	return mapProviderStatus(s.providerID, s.protocol, response.Status, response.Message)
}

// Close is idempotent and invalidates the stream before asking the guest to
// release it. A provider that already failed is not called again, and the
// guest release is best effort so closing a Session never waits on a dead
// instance.
func (s *ProviderStream) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	failed := s.failure != nil
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.closedCh) })
	s.input.closeWith(io.ErrClosedPipe)
	s.output.closeWith(io.EOF)
	s.instance.removeStream(s)
	if failed || !s.instance.activeState() {
		return nil
	}
	payload, _ := json.Marshal(struct {
		Stream uint32 `json:"stream"`
	}{s.id})
	go func() {
		callCtx, cancel := context.WithTimeout(context.Background(), s.instance.limits.CallTimeout)
		defer cancel()
		requestID := s.instance.host.next.Next()
		_, _ = s.instance.invoker.InvokeProvider(callCtx, ProviderRequest{
			ID: requestID, Method: domainpluginabi.ProviderMethodClose, Payload: payload,
		})
	}()
	return nil
}

func (s *ProviderStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *ProviderStream) failureError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

func (s *ProviderStream) closeQueues(err error) {
	s.closeOnce.Do(func() { close(s.closedCh) })
	s.input.closeWith(err)
	s.output.closeWith(err)
}

// failWith delivers one typed failure to the Session. Queued output remains
// readable first, so the host can classify the end after consuming bytes it
// already received.
func (s *ProviderStream) failWith(err error) {
	var providerErr *domainplugin.ProviderError
	if !errors.As(err, &providerErr) {
		providerErr = &domainplugin.ProviderError{
			Kind: domainplugin.ProviderErrorFaulted, Provider: s.providerID,
			Protocol: s.protocol, Message: "provider failed",
		}
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.failure = providerErr
	s.mu.Unlock()
	s.closeOnce.Do(func() { close(s.closedCh) })
	s.input.closeWith(providerErr)
	s.output.closeWith(providerErr)
	// Release the session slot and both queues even when the guest fails only
	// this stream. Close then sees closed and returns without a second sweep.
	s.instance.removeStream(s)
}

// Failure returns the typed provider failure that ended the stream, or nil.
func (s *ProviderStream) Failure() error { return s.failureError() }

func (s *ProviderStream) readInput(capacity int) []byte {
	if capacity <= 0 {
		return nil
	}
	if capacity > s.instance.limits.ChunkBytes {
		capacity = s.instance.limits.ChunkBytes
	}
	return s.input.take(capacity)
}

func (s *ProviderStream) inputPending() bool { return s.input.pending() }

func (s *ProviderStream) inputTaken() uint64 { return s.input.takenBytes() }

func (s *ProviderStream) inputCredit() int { return s.input.credit() }

func (s *ProviderStream) writeOutput(data []byte) int32 {
	if len(data) > s.instance.limits.ChunkBytes {
		return -1
	}
	if s.isClosed() {
		return -3
	}
	if !s.output.append(data, s.instance.limits.ChunkBytes) {
		return -2
	}
	return 0
}

func (s *ProviderStream) outputCredit() int { return s.output.credit() }

// streamQueue is a bounded ring buffer. The host owns both directions; no
// queue can exceed its configured byte limit and no guest call moves more than
// one chunk.
type streamQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buffer []byte
	head   int
	size   int
	taken  uint64
	closed bool
	err    error
}

func newStreamQueue(limit int) *streamQueue {
	if limit <= 0 {
		limit = DefaultProviderQueueBytes
	}
	queue := &streamQueue{buffer: make([]byte, limit)}
	queue.cond = sync.NewCond(&queue.mu)
	return queue
}

func (q *streamQueue) credit() int {
	if q == nil {
		return -1
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.buffer) - q.size
}

func (q *streamQueue) pending() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.size > 0
}

// takenBytes counts how many bytes left the queue. The pump loop uses it to
// tell "the guest read and more input is waiting" from "the guest is blocked
// on output credit", without spinning either case.
func (q *streamQueue) takenBytes() uint64 {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.taken
}

// take copies up to want bytes without blocking. It is the provider input
// read path, so the guest never gets a view of host memory.
func (q *streamQueue) take(want int) []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if want > q.size {
		want = q.size
	}
	if want <= 0 {
		return nil
	}
	out := make([]byte, want)
	first := copy(out, q.buffer[q.head:min(q.head+want, len(q.buffer))])
	if first < want {
		copy(out[first:], q.buffer[:want-first])
	}
	q.head = (q.head + want) % len(q.buffer)
	q.size -= want
	q.taken += uint64(want)
	q.cond.Broadcast()
	return out
}

// writeBlocking appends bounded chunks for host input, waiting while the queue
// is full. A stream close wakes the writer.
func (q *streamQueue) writeBlocking(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.size == len(q.buffer) && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return 0, io.ErrClosedPipe
	}
	chunk := min(len(p), len(q.buffer)-q.size, ProviderChunkBytes)
	if chunk <= 0 {
		return 0, nil
	}
	q.appendLocked(p[:chunk])
	q.cond.Broadcast()
	return chunk, nil
}

// append accepts one complete output chunk without blocking. A partial write
// is never accepted: the guest retries the same chunk after reading credit.
func (q *streamQueue) append(p []byte, chunkLimit int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || len(p) > chunkLimit || len(p) > len(q.buffer)-q.size {
		return false
	}
	q.appendLocked(p)
	q.cond.Broadcast()
	return true
}

func (q *streamQueue) appendLocked(p []byte) {
	write := (q.head + q.size) % len(q.buffer)
	first := copy(q.buffer[write:], p)
	if first < len(p) {
		copy(q.buffer, p[first:])
	}
	q.size += len(p)
}

// readBlocking waits for output bytes. It drains queued bytes before reporting
// a close or a typed failure.
func (q *streamQueue) readBlocking(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.size == 0 && !q.closed {
		q.cond.Wait()
	}
	if q.size == 0 {
		if q.err != nil {
			return 0, q.err
		}
		return 0, io.EOF
	}
	n := min(len(p), q.size)
	first := copy(p, q.buffer[q.head:min(q.head+n, len(q.buffer))])
	if first < n {
		copy(p[first:], q.buffer[:n-first])
	}
	q.head = (q.head + n) % len(q.buffer)
	q.size -= n
	q.cond.Broadcast()
	return n, nil
}

func (q *streamQueue) closeWith(err error) {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.err = err
		q.cond.Broadcast()
	}
	q.mu.Unlock()
}

// recordProviderEvent records one payload-free provider audit event.
func (h *ProviderHost) recordProviderEvent(pluginID, protocol, action, status string,
	elapsed time.Duration) {

	if h == nil || h.audit == nil {
		return
	}
	event := domainplugin.AuditEvent{
		Time:     h.now().UTC(),
		Event:    domainplugin.AuditProvider,
		PluginID: pluginID,
		Protocol: protocol,
		Reason:   domainplugin.ProviderAuditReason(action, status),
		Status:   status,
	}
	if elapsed > 0 {
		event.DurationMS = elapsed.Milliseconds()
	}
	h.audit.Record(event)
}
