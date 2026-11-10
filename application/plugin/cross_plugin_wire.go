package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
	domainpluginabi "github.com/mslxl/natcatty/domain/plugin/abi"
)

// Cross-plugin call envelope codes. The set is closed; an unknown provider
// code is reported as internal rather than passed through.
const (
	ServiceCallOK               = "ok"
	ServiceCallInvalidArgument  = "invalid-argument"
	ServiceCallNotFound         = "not-found"
	ServiceCallPermissionDenied = "permission-denied"
	ServiceCallUnavailable      = "unavailable"
	ServiceCallBusy             = "busy"
	ServiceCallTimeout          = "timeout"
	ServiceCallCancelled        = "cancelled"
	ServiceCallLimit            = "limit"
	ServiceCallUnsupported      = "unsupported"
	ServiceCallConflict         = "conflict"
	ServiceCallDeadlock         = "deadlock"
	ServiceCallReconsent        = "reconsent-required"
	ServiceCallInternal         = "internal"
)

// serviceCodePermissionDenied aliases the dispatcher vocabulary for the audit
// status of a denied cross-plugin call.
const serviceCodePermissionDenied = ServiceCallPermissionDenied

func serviceCodeAllowed(code string) bool {
	switch code {
	case ServiceCallOK, ServiceCallInvalidArgument, ServiceCallNotFound,
		ServiceCallPermissionDenied, ServiceCallUnavailable, ServiceCallBusy,
		ServiceCallTimeout, ServiceCallCancelled, ServiceCallLimit,
		ServiceCallUnsupported, ServiceCallConflict, ServiceCallDeadlock,
		ServiceCallReconsent, ServiceCallInternal:
		return true
	default:
		return false
	}
}

// callEnvelope returns a cross-plugin error envelope. The message is bounded
// so a provider cannot use the error channel as a large side channel.
func callEnvelope(code, message string) serviceEnvelope {
	if len(message) > 2048 {
		message = message[:2048]
	}
	return serviceEnvelope{Code: code, Message: message}
}

// invokeRequest is the services.invoke request body. Exactly one of Payload
// and PayloadB64 is present.
type invokeRequest struct {
	Service    string          `json:"service"`
	Method     string          `json:"method"`
	ProviderID string          `json:"provider_id,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	PayloadB64 string          `json:"payload_b64,omitempty"`
	// Stream continues an already-open upload or duplex stream in a new
	// provider call, so the provider can drain it without a reverse channel.
	Stream string `json:"stream,omitempty"`
}

// decodeInvokeRequest parses and structurally checks one invoke body.
func decodeInvokeRequest(call ServiceCall) (invokeRequest, error) {
	var request invokeRequest
	if err := decodeServiceRequest(call, &request); err != nil {
		return request, err
	}
	if err := domainplugin.ValidateServiceName(request.Service); err != nil {
		return request, err
	}
	if request.Method == "" {
		return request, errors.New("method is required")
	}
	if len(request.Payload) > 0 && request.PayloadB64 != "" {
		return request, errors.New("payload and payload_b64 are mutually exclusive")
	}
	return request, nil
}

// dispatchServices routes one natcatty:svc/services_v1 call to the broker.
func (d serviceDispatcher) services(ctx context.Context, call ServiceCall) serviceEnvelope {
	if d.session == nil || d.session.Broker() == nil {
		return callEnvelope(ServiceCallUnavailable, "the cross-plugin broker is not configured")
	}
	return d.session.Broker().dispatch(ctx, d.session, call)
}

func (b *ServiceBroker) dispatch(ctx context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	switch call.Method {
	case domainpluginabi.ServicesMethodInvoke:
		envelope := b.invoke(ctx, session, call)
		b.statsAdd(func(stats *ServiceCallStats) {
			stats.Calls++
			if envelope.Code != ServiceCallOK {
				stats.Errors++
			}
		})
		return envelope
	case domainpluginabi.ServicesMethodStreamOpen:
		return b.streamOpen(ctx, session, call)
	case domainpluginabi.ServicesMethodStreamRead:
		return b.streamRead(ctx, session, call)
	case domainpluginabi.ServicesMethodStreamWrite:
		return b.streamWrite(ctx, session, call)
	case domainpluginabi.ServicesMethodStreamClose:
		return b.streamClose(ctx, session, call)
	case domainpluginabi.ServicesMethodSubscribe:
		return b.subscribe(ctx, session, call)
	case domainpluginabi.ServicesMethodPoll:
		return b.poll(ctx, session, call)
	case domainpluginabi.ServicesMethodUnsubscribe:
		return b.unsubscribe(ctx, session, call)
	case domainpluginabi.ServicesMethodRegister:
		return b.register(ctx, session, call)
	case domainpluginabi.ServicesMethodUnregister:
		return b.unregister(ctx, session, call)
	case domainpluginabi.ServicesMethodProviders:
		return b.providers(ctx, session, call)
	case domainpluginabi.ServicesMethodStatus:
		return b.status(ctx, session, call)
	case domainpluginabi.ServicesMethodEmit:
		return b.emit(ctx, session, call)
	default:
		return callEnvelope(ServiceCallInvalidArgument,
			fmt.Sprintf("unknown method %d for service %q", call.Method, call.Service))
	}
}

// consumerFiber returns the calling fiber. A guest session always has one.
func (b *ServiceBroker) consumerFiber(session *ServiceSession) (*Fiber, error) {
	if session == nil {
		return nil, errors.New("the plugin session is closed")
	}
	fiber, ok := b.container.Fiber(session.fiber)
	if !ok {
		return nil, errors.New("the plugin instance is not loaded")
	}
	if fiber.State() != domainplugin.StateActive {
		return nil, errors.New("the plugin instance is not active")
	}
	return fiber, nil
}

// bindingFor returns the consumer's inject declaration for a service.
func (b *ServiceBroker) bindingFor(fiberID domainplugin.FiberID,
	service string) (ServiceBinding, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	perFiber := b.bindings[fiberID]
	if perFiber == nil {
		return ServiceBinding{}, false
	}
	binding, ok := perFiber[service]
	if !ok {
		return ServiceBinding{}, false
	}
	return *binding, true
}

// resolvedProvider resolves the provider for one call. The consumer's declared
// range governs version acceptance even when a provider is named explicitly.
func (b *ServiceBroker) resolvedProvider(fiber *Fiber, service, providerID string) (ServiceProvider, serviceResolutionStatus) {
	versionRange := "*"
	if binding, ok := b.bindingFor(fiber.id, service); ok {
		if binding.Range != "" {
			versionRange = binding.Range
		}
		if providerID == "" && !binding.Optional && !binding.Bound {
			// The consumer is ACTIVE, so a required binding is bound. An
			// unbound required binding means the graph changed under it.
			return ServiceProvider{}, serviceMissing
		}
	}
	return b.container.resolveService(service, fiber.group, versionRange, providerID)
}

func resolutionEnvelope(service string, status serviceResolutionStatus) serviceEnvelope {
	switch status {
	case serviceVersionMismatch:
		return callEnvelope(ServiceCallConflict,
			fmt.Sprintf("no provider of %s satisfies the declared version range", service))
	case serviceConflict:
		return callEnvelope(ServiceCallConflict,
			fmt.Sprintf("multiple providers of %s share the highest priority", service))
	default:
		return callEnvelope(ServiceCallNotFound,
			fmt.Sprintf("no visible provider for service %s", service))
	}
}

// invoke executes one synchronous cross-plugin call.
func (b *ServiceBroker) invoke(ctx context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	request, err := decodeInvokeRequest(call)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	fiber, err := b.consumerFiber(session)
	if err != nil {
		return callEnvelope(ServiceCallUnavailable, err.Error())
	}
	provider, status := b.resolvedProvider(fiber, request.Service, request.ProviderID)
	if status != serviceResolved {
		if request.ProviderID != "" && status == serviceMissing {
			return callEnvelope(ServiceCallNotFound,
				fmt.Sprintf("provider %q does not provide %s in this scope", request.ProviderID, request.Service))
		}
		return resolutionEnvelope(request.Service, status)
	}
	method, ok := provider.Declaration.Method(request.Method)
	if !ok {
		return callEnvelope(ServiceCallNotFound,
			fmt.Sprintf("service %s has no method %q", request.Service, request.Method))
	}
	if err := b.checkInvoke(session, provider, method); err != nil {
		return denialEnvelope(serviceMethodScope(provider, method), err)
	}
	encoding := method.EffectiveEncoding()
	payload, err := serviceRequestPayload(encoding, request)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := domainplugin.ValidateServicePayload(encoding, method.ArgsSchema, payload,
		domainplugin.MaxServiceRequestBytes); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	stream, streamEnvelope := b.continueStream(request, fiber, provider, encoding)
	if streamEnvelope.Code != "" {
		return streamEnvelope
	}

	started := b.now()
	response, callErr := b.callProvider(ctx, session, fiber, provider, method, payload, stream)
	b.statsAdd(func(stats *ServiceCallStats) {
		stats.Requests += int64(len(payload))
	})
	if callErr != nil {
		return b.failedCall(session, provider, method, started, len(payload), callErr)
	}
	envelope, resultPayload, err := b.decodeProviderResponse(provider, method, response)
	if err != nil {
		b.auditCall(session.ref, provider, method, len(payload), len(response),
			b.now().Sub(started), ServiceCallInternal)
		return callEnvelope(ServiceCallInternal, err.Error())
	}
	b.statsAdd(func(stats *ServiceCallStats) {
		stats.Responses += int64(len(resultPayload))
	})
	b.auditCall(session.ref, provider, method, len(payload), len(resultPayload),
		b.now().Sub(started), envelope.Code)
	if envelope.Code != ServiceCallOK {
		return envelope
	}
	return invokeSuccess(provider, method, resultPayload)
}

// invokeSuccess wraps one validated provider result in the caller-facing data.
func invokeSuccess(provider ServiceProvider, method domainplugin.ServiceMethodDecl,
	payload []byte) serviceEnvelope {
	data := map[string]any{
		"provider": provider.PluginID,
		"version":  provider.Version,
		"encoding": method.EffectiveEncoding(),
	}
	if method.EffectiveEncoding() == domainplugin.ServiceEncodingBinary {
		data["content_type"] = method.ContentType
		data["payload_b64"] = base64.StdEncoding.EncodeToString(payload)
	} else {
		data["payload"] = json.RawMessage(payload)
	}
	return okEnvelope(data)
}

// serviceRequestPayload extracts the raw payload bytes for the declared
// encoding from an invoke request.
func serviceRequestPayload(encoding string, request invokeRequest) ([]byte, error) {
	if encoding == domainplugin.ServiceEncodingBinary {
		if request.PayloadB64 == "" {
			return nil, errors.New("binary methods take payload_b64")
		}
		payload, err := base64.StdEncoding.DecodeString(request.PayloadB64)
		if err != nil {
			return nil, errors.New("payload_b64 is not valid base64")
		}
		return payload, nil
	}
	if len(request.Payload) == 0 {
		if request.PayloadB64 != "" {
			return nil, errors.New("JSON methods take payload, not payload_b64")
		}
		// A no-argument method may omit the payload; an explicit JSON null
		// arrives as the literal "null" and stays distinguishable.
		return []byte("{}"), nil
	}
	return request.Payload, nil
}

func denialEnvelope(scope domainplugin.Scope, err error) serviceEnvelope {
	envelope := callEnvelope(ServiceCallPermissionDenied, "the services.invoke grant does not cover this call")
	envelope.Permission = domainplugin.PermissionServicesInvoke
	envelope.Scope = &scope
	_ = err
	return envelope
}

// providerRequest is the host-to-provider request envelope. It carries one
// method invocation and, for a stream, the provider endpoint handle.
type providerRequest struct {
	RequestID   uint64                `json:"request_id"`
	Service     string                `json:"service"`
	Method      string                `json:"method"`
	Version     string                `json:"version"`
	Encoding    string                `json:"encoding"`
	ContentType string                `json:"content_type,omitempty"`
	DeadlineMS  int64                 `json:"deadline_ms"`
	Payload     json.RawMessage       `json:"payload,omitempty"`
	PayloadB64  string                `json:"payload_b64,omitempty"`
	Stream      *providerStreamHandle `json:"stream,omitempty"`
}

type providerStreamHandle struct {
	Handle        string `json:"handle"`
	Direction     string `json:"direction"`
	ChunkLimit    int    `json:"chunk_limit"`
	InitialCredit int    `json:"initial_credit"`
	// Open is true on the stream_open request and omitted on a continuation.
	Open bool `json:"open,omitempty"`
}

// providerResponse is the provider's answer, written through service_respond.
type providerResponse struct {
	Code    string          `json:"code"`
	Message string          `json:"message,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
	DataB64 string          `json:"data_b64,omitempty"`
}

// buildProviderRequest encodes one request for the provider guest.
func (b *ServiceBroker) buildProviderRequest(provider ServiceProvider,
	method domainplugin.ServiceMethodDecl, payload []byte, stream *providerStreamHandle,
	requestID uint64, ctx context.Context) ([]byte, error) {
	request := providerRequest{
		RequestID:   requestID,
		Service:     provider.Service,
		Method:      method.Name,
		Version:     provider.Version,
		Encoding:    method.EffectiveEncoding(),
		ContentType: method.ContentType,
		DeadlineMS:  serviceTimeRemaining(ctx).Milliseconds(),
		Stream:      stream,
	}
	if method.EffectiveEncoding() == domainplugin.ServiceEncodingBinary {
		request.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
	} else {
		request.Payload = json.RawMessage(payload)
	}
	return json.Marshal(request)
}

// callProvider runs one synchronous request on the provider instance. It
// applies the depth and cycle guard, the bounded provider queue, the deadline
// class, and the one-in-flight rule, and it recycles a provider that traps or
// misses the deadline.
func (b *ServiceBroker) callProvider(ctx context.Context, session *ServiceSession,
	caller *Fiber, provider ServiceProvider, method domainplugin.ServiceMethodDecl,
	payload []byte, stream *providerStreamHandle) ([]byte, error) {
	providerFiber, ok := b.container.Fiber(provider.Fiber)
	if !ok || providerFiber.State() != domainplugin.StateActive {
		return nil, ErrServiceCallUnavailable
	}
	b.mu.Lock()
	b.nextCall++
	requestID := b.nextCall
	b.mu.Unlock()

	deadline := DefaultServiceCallTimeout
	if method.EffectiveTimeout() == domainplugin.ServiceTimeoutLong {
		deadline = MaxServiceCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	if err := b.waits.enter(caller.id, providerFiber.id); err != nil {
		if errors.Is(err, ErrServiceDeadlock) {
			b.statsAdd(func(stats *ServiceCallStats) { stats.Deadlocks++ })
		}
		return nil, err
	}
	defer b.waits.leave(caller.id, providerFiber.id)

	queue := b.providerQueue(providerFiber.id)
	release, err := queue.acquire(callCtx)
	if err != nil {
		if errors.Is(err, ErrServiceBusy) {
			b.statsAdd(func(stats *ServiceCallStats) { stats.Busy++ })
		}
		return nil, err
	}
	defer release()

	request, err := b.buildProviderRequest(provider, method, payload, stream, requestID, callCtx)
	if err != nil {
		return nil, err
	}

	var response []byte
	err = providerFiber.withInstance(callCtx, func(instance Instance) error {
		capable, ok := instance.(ServiceCapableInstance)
		if !ok || !capable.SupportsService() {
			return &ErrNotServiceProvider{PluginID: provider.PluginID}
		}
		payload, err := capable.ServiceRequest(callCtx, ServiceRequest{
			ID:      requestID,
			Payload: request,
		})
		if err != nil {
			return err
		}
		response = payload
		return nil
	})
	if err != nil {
		b.recordProviderFailure(providerFiber, err)
		return nil, err
	}
	return response, nil
}

// providerQueue returns the bounded queue for one provider, creating it on
// first use. A provider reload clears it, so calls are never queued across a
// reload.
func (b *ServiceBroker) providerQueue(id domainplugin.FiberID) *providerQueue {
	b.mu.Lock()
	defer b.mu.Unlock()
	queue := b.queues[id]
	if queue == nil {
		queue = newProviderQueue()
		b.queues[id] = queue
	}
	return queue
}

// decodeProviderResponse parses and validates one provider envelope. A
// success result is checked against the declared result schema; an invalid
// result is an internal provider error.
func (b *ServiceBroker) decodeProviderResponse(provider ServiceProvider,
	method domainplugin.ServiceMethodDecl, payload []byte) (serviceEnvelope, []byte, error) {
	if len(payload) > domainplugin.MaxServiceResponseBytes*8 {
		return serviceEnvelope{}, nil, errors.New("provider response exceeds the transport cap")
	}
	var response providerResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return serviceEnvelope{}, nil, errors.New("provider response is not a valid JSON envelope")
	}
	if response.Code == "" {
		return serviceEnvelope{}, nil, errors.New("provider response has no code")
	}
	if response.Code != ServiceCallOK {
		if !serviceCodeAllowed(response.Code) {
			return serviceEnvelope{}, nil, fmt.Errorf("provider returned the unknown code %q", response.Code)
		}
		return callEnvelope(response.Code, response.Message), nil, nil
	}
	encoding := method.EffectiveEncoding()
	if encoding == domainplugin.ServiceEncodingBinary {
		if response.DataB64 == "" {
			return serviceEnvelope{}, nil, errors.New("binary method returned no data_b64")
		}
		data, err := base64.StdEncoding.DecodeString(response.DataB64)
		if err != nil {
			return serviceEnvelope{}, nil, errors.New("provider data_b64 is not valid base64")
		}
		if err := domainplugin.ValidateServicePayload(encoding, nil, data,
			domainplugin.MaxServiceResponseBytes); err != nil {
			return serviceEnvelope{}, nil, err
		}
		return serviceEnvelope{Code: ServiceCallOK}, data, nil
	}
	if len(response.Data) == 0 {
		return serviceEnvelope{}, nil, errors.New("JSON method returned no data")
	}
	if err := domainplugin.ValidateServicePayload(encoding, method.ResultSchema,
		response.Data, domainplugin.MaxServiceResponseBytes); err != nil {
		return serviceEnvelope{}, nil, err
	}
	if !utf8.Valid(response.Data) {
		return serviceEnvelope{}, nil, errors.New("provider data is not valid UTF-8")
	}
	return serviceEnvelope{Code: ServiceCallOK}, response.Data, nil
}

// failedCall maps one provider invocation failure to the closed call envelope
// and records it without holding broker locks.
func (b *ServiceBroker) failedCall(session *ServiceSession, provider ServiceProvider,
	method domainplugin.ServiceMethodDecl, started time.Time, requestBytes int, err error) serviceEnvelope {
	code := ServiceCallInternal
	message := err.Error()
	switch {
	case errors.Is(err, ErrServiceBusy):
		code = ServiceCallBusy
	case errors.Is(err, ErrServiceDeadlock):
		code = ServiceCallDeadlock
	case errors.Is(err, ErrServiceDepth):
		code = ServiceCallDeadlock
	case errors.Is(err, ErrServiceCallUnavailable):
		code = ServiceCallUnavailable
	case errors.Is(err, ErrNotActive), errors.Is(err, ErrClosed):
		code = ServiceCallUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		code = ServiceCallTimeout
	case errors.Is(err, context.Canceled):
		code = ServiceCallCancelled
	default:
		var notProvider *ErrNotServiceProvider
		var noResponse *ServiceNoResponseError
		if errors.As(err, &notProvider) {
			code = ServiceCallUnsupported
		}
		if errors.As(err, &noResponse) {
			code = ServiceCallInternal
		}
	}
	b.statsAdd(func(stats *ServiceCallStats) {
		if code == ServiceCallBusy {
			stats.Busy++
		}
		if code == ServiceCallTimeout {
			stats.Timeouts++
		}
	})
	b.auditCall(session.ref, provider, method, requestBytes, 0,
		b.now().Sub(started), code)
	return callEnvelope(code, message)
}

// recordProviderFailure recycles a provider that trapped, miss its deadline,
// or became unusable. A cancelled request does not blame the provider.
func (b *ServiceBroker) recordProviderFailure(fiber *Fiber, err error) {
	if fiber == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) && !IsUnusable(err) {
		return
	}
	var deadline *DeadlineError
	var trap *TrapError
	if errors.As(err, &deadline) || errors.As(err, &trap) {
		b.container.recycle(fiber, err)
	}
}

// providers implements services.providers: enumerate visible providers.
func (b *ServiceBroker) providers(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Service string `json:"service"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := domainplugin.ValidateServiceName(request.Service); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	fiber, err := b.consumerFiber(session)
	if err != nil {
		return callEnvelope(ServiceCallUnavailable, err.Error())
	}
	rows := make([]map[string]any, 0)
	for _, provider := range b.container.Services() {
		if provider.Service != request.Service {
			continue
		}
		if !b.container.visibleFrom(provider.Group, fiber.group) {
			continue
		}
		rows = append(rows, map[string]any{
			"plugin_id": provider.PluginID,
			"version":   provider.Version,
			"priority":  provider.Priority,
			"access":    provider.Access,
			"dynamic":   provider.Dynamic,
			"fiber":     string(provider.Fiber),
			"health":    "active",
		})
	}
	return okEnvelope(map[string]any{"providers": rows})
}

// status implements services.status: report one binding.
func (b *ServiceBroker) status(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Service string `json:"service"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := domainplugin.ValidateServiceName(request.Service); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if session == nil {
		return callEnvelope(ServiceCallUnavailable, "the plugin session is closed")
	}
	binding, ok := b.bindingFor(session.fiber, request.Service)
	if !ok {
		return okEnvelope(map[string]any{
			"service":  request.Service,
			"declared": false,
			"bound":    false,
		})
	}
	data := map[string]any{
		"service":  binding.Service,
		"declared": true,
		"optional": binding.Optional,
		"range":    binding.Range,
		"bound":    binding.Bound,
	}
	if binding.Bound {
		data["provider"] = binding.Provider
		data["version"] = binding.Version
		data["priority"] = binding.Priority
		data["access"] = binding.Access
		data["table_hash"] = binding.TableHash
	}
	if binding.Reason != "" {
		data["reason"] = binding.Reason
	}
	return okEnvelope(data)
}

// register implements services.register for a dynamic provider declaration.
func (b *ServiceBroker) register(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var declaration domainplugin.Provision
	if err := decodeServiceRequest(call, &declaration); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	declaration.Dynamic = true
	fiber, err := b.consumerFiber(session)
	if err != nil {
		return callEnvelope(ServiceCallUnavailable, err.Error())
	}
	manifest := fiber.manifestSnapshot()
	if !declaresDynamic(manifest, declaration.Service) {
		return callEnvelope(ServiceCallPermissionDenied,
			fmt.Sprintf("%s is not declared dynamic in the manifest", declaration.Service))
	}
	if err := domainplugin.ValidateProvisions(fiber.ref.ID,
		[]domainplugin.Provision{declaration}); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if !b.dynamicSlotAvailable(fiber, declaration.Service) {
		return callEnvelope(ServiceCallLimit, "the plugin already provides the maximum number of services")
	}
	previous, hasPrevious := b.dynamicDeclaration(fiber.id, declaration.Service)
	if hasPrevious {
		oldVersion, okOld := domainplugin.ParseSemver(previous.EffectiveVersion())
		newVersion, okNew := domainplugin.ParseSemver(declaration.EffectiveVersion())
		if okOld && okNew && newVersion.Compare(oldVersion) < 0 {
			return callEnvelope(ServiceCallInvalidArgument,
				fmt.Sprintf("service %s version may not move backwards from %s to %s",
					declaration.Service, previous.EffectiveVersion(), declaration.EffectiveVersion()))
		}
	}
	if err := b.provideAllowed(fiber, declaration); err != nil {
		scope := serviceProvideScope(declaration.Service, declaration.Access)
		envelope := callEnvelope(ServiceCallReconsent,
			"the services.provide grant does not cover this registration")
		envelope.Permission = domainplugin.PermissionServicesProvide
		envelope.Scope = &scope
		b.auditCall(session.ref, ServiceProvider{PluginID: session.ref.ID, Service: declaration.Service},
			domainplugin.ServiceMethodDecl{Name: "register", Access: declaration.Access},
			0, 0, 0, ServiceCallReconsent)
		return envelope
	}
	b.mu.Lock()
	perFiber := b.dynamic[fiber.id]
	if perFiber == nil {
		perFiber = make(map[string]domainplugin.Provision)
		b.dynamic[fiber.id] = perFiber
	}
	perFiber[declaration.Service] = domainplugin.CloneProvision(declaration)
	b.mu.Unlock()
	b.publishDeclaration(fiber, declaration)
	b.container.reconcileAfterGraphChange()

	b.auditDynamic(session.ref, declaration, "service.register")
	return okEnvelope(map[string]any{
		"service":    declaration.Service,
		"version":    declaration.EffectiveVersion(),
		"table_hash": providerTableHash(declaration),
	})
}

// unregister implements services.unregister.
func (b *ServiceBroker) unregister(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Service string `json:"service"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := domainplugin.ValidateServiceName(request.Service); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	fiber, err := b.consumerFiber(session)
	if err != nil {
		return callEnvelope(ServiceCallUnavailable, err.Error())
	}
	if !domainplugin.OwnsServiceName(fiber.ref.ID, request.Service) {
		return callEnvelope(ServiceCallPermissionDenied,
			fmt.Sprintf("%s is outside plugin.%s.", request.Service, fiber.ref.ID))
	}
	if err := b.provideAllowed(fiber, domainplugin.Provision{
		Service: request.Service, Access: domainplugin.ServiceAccessRead,
	}); err != nil {
		return callEnvelope(ServiceCallPermissionDenied,
			"the services.provide grant does not cover this service")
	}
	b.mu.Lock()
	_, existed := b.dynamic[fiber.id][request.Service]
	delete(b.dynamic[fiber.id], request.Service)
	b.mu.Unlock()
	if existed {
		b.container.unregisterProvision(fiber, request.Service)
		b.unbindService(fiber.id, request.Service)
		b.container.reconcileAfterGraphChange()
		b.auditDynamic(session.ref, domainplugin.Provision{Service: request.Service},
			"service.unregister")
	}
	return okEnvelope(map[string]any{"service": request.Service})
}

// dynamicDeclaration returns one fiber's dynamic declaration.
func (b *ServiceBroker) dynamicDeclaration(id domainplugin.FiberID,
	service string) (domainplugin.Provision, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	declaration, ok := b.dynamic[id][service]
	return declaration, ok
}

// declaresDynamic reports whether the manifest reserves the service name for
// runtime registration.
func declaresDynamic(manifest domainplugin.Manifest, service string) bool {
	for _, provision := range manifest.Provides {
		if provision.Service == service && provision.Dynamic {
			return true
		}
	}
	return false
}

// dynamicSlotAvailable enforces the per-plugin service count across static
// and dynamic declarations.
func (b *ServiceBroker) dynamicSlotAvailable(fiber *Fiber, service string) bool {
	manifest := fiber.manifestSnapshot()
	names := make(map[string]bool, len(manifest.Provides)+len(b.dynamic[fiber.id]))
	for _, provision := range manifest.Provides {
		names[provision.Service] = true
	}
	b.mu.Lock()
	for name := range b.dynamic[fiber.id] {
		names[name] = true
	}
	b.mu.Unlock()
	delete(names, service)
	return len(names) < domainplugin.MaxServicesPerPlugin
}

// provideAllowed checks the services.provide grant for a declaration,
// including the version for a dynamic registration.
func (b *ServiceBroker) provideAllowed(fiber *Fiber, declaration domainplugin.Provision) error {
	if b == nil || b.authority == nil || fiber == nil {
		return nil
	}
	scope := serviceProvideScope(declaration.Service, declaration.EffectiveAccess())
	scope.Version = declaration.EffectiveVersion()
	return b.authority.CheckService(SubjectForRef(fiber.ref),
		domainplugin.PermissionServicesProvide, scope, true)
}

func (b *ServiceBroker) auditDynamic(ref PluginRef, declaration domainplugin.Provision, action string) {
	if b == nil {
		return
	}
	scope := serviceProvideScope(declaration.Service, declaration.EffectiveAccess())
	b.audit.Record(domainplugin.AuditEvent{
		Time:       b.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   ref.ID,
		Source:     ref.Source,
		Version:    ref.Version,
		Permission: domainplugin.PermissionServicesProvide,
		Scope:      &scope,
		Provider:   ref.ID,
		Method:     action,
		Reason:     domainplugin.ServiceAuditReason(declaration.Service, action, ServiceCallOK),
		Status:     ServiceCallOK,
	})
}

// providerTableHash returns the stable hash for one declaration.
func providerTableHash(declaration domainplugin.Provision) string {
	return domainplugin.ProvisionHash(declaration)
}

// visibleFrom reports whether a provider group is in the consumer's ancestor
// chain (including the consumer's own group).
func (c *Container) visibleFrom(providerGroup, consumerGroup string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, scope := range c.groupChainLocked(consumerGroup) {
		if scope == providerGroup {
			return true
		}
	}
	return false
}

// auditEventDelivery records event counts without each payload.
func (b *ServiceBroker) auditEventDelivery(session *ServiceSession, provider ServiceProvider,
	event string, delivered, dropped int) {
	if b == nil || delivered == 0 && dropped == 0 {
		return
	}
	b.audit.Record(domainplugin.AuditEvent{
		Time:     b.now().UTC(),
		Event:    domainplugin.AuditService,
		PluginID: session.ref.ID,
		Source:   session.ref.Source,
		Version:  session.ref.Version,
		Provider: provider.PluginID,
		Method:   event,
		Bytes:    delivered,
		Reason:   domainplugin.ServiceAuditReason(provider.Service, event, ServiceCallOK),
		Status:   ServiceCallOK,
	})
	_ = dropped
}

// serviceTimeRemaining reports the remaining budget for a provider call, for
// diagnostics and the provider envelope. A zero value means no deadline.
func serviceTimeRemaining(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < 0 {
			return 0
		}
		return remaining
	}
	return 0
}
