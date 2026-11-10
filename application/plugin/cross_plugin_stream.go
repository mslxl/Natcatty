package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Cross-plugin streams are host-owned handles with three directions. Each
// endpoint gets its own handle to the same stream so the broker can enforce
// which side may read or write. Flow control reuses StreamBuffer: a 256 KiB
// initial window, 64 KiB chunks, a 1 MiB per-stream buffer, and credit
// restored on every read.

// Stream directions.
const (
	ServiceStreamDownload = "download"
	ServiceStreamUpload   = "upload"
	ServiceStreamDuplex   = "duplex"
)

// serviceStream is one open stream. up carries consumer-to-provider bytes and
// down carries provider-to-consumer bytes. A direction closes the buffer the
// direction does not use.
type serviceStream struct {
	mu sync.Mutex

	handle         Handle
	peer           Handle
	service        string
	method         string
	direction      string
	consumer       domainplugin.FiberID
	provider       domainplugin.FiberID
	consumerHandle Handle
	providerHandle Handle
	encoding       string

	up   *StreamBuffer
	down *StreamBuffer

	providerRow ServiceProvider
	closed      bool
	unavailable bool
	reason      string
	consumerEnd bool
	providerEnd bool

	lastActivity time.Time
	timer        func()
}

// streamOpenRequest is the services.stream_open body.
type streamOpenRequest struct {
	Service    string          `json:"service"`
	Method     string          `json:"method"`
	ProviderID string          `json:"provider_id,omitempty"`
	Direction  string          `json:"direction"`
	Args       json.RawMessage `json:"args,omitempty"`
	ArgsB64    string          `json:"args_b64,omitempty"`
}

// streamOpen opens one stream and asks the provider to accept it.
func (b *ServiceBroker) streamOpen(ctx context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request streamOpenRequest
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := domainplugin.ValidateServiceName(request.Service); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	switch request.Direction {
	case ServiceStreamDownload, ServiceStreamUpload, ServiceStreamDuplex:
	default:
		return callEnvelope(ServiceCallInvalidArgument,
			"direction must be download, upload, or duplex")
	}
	fiber, err := b.consumerFiber(session)
	if err != nil {
		return callEnvelope(ServiceCallUnavailable, err.Error())
	}
	provider, status := b.resolvedProvider(fiber, request.Service, request.ProviderID)
	if status != serviceResolved {
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
	payload, payloadErr := serviceRequestPayload(encoding, invokeRequest{
		Service: request.Service, Method: request.Method,
		Payload: request.Args, PayloadB64: request.ArgsB64,
	})
	if payloadErr != nil {
		return callEnvelope(ServiceCallInvalidArgument, payloadErr.Error())
	}
	if err := domainplugin.ValidateServicePayload(encoding, method.ArgsSchema, payload,
		domainplugin.MaxServiceRequestBytes); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if !b.streamSlotAvailable(fiber.id) || !b.streamSlotAvailable(provider.Fiber) {
		return callEnvelope(ServiceCallLimit,
			fmt.Sprintf("a plugin may open at most %d streams", MaxStreamsPerPlugin))
	}

	stream := &serviceStream{
		service:      provider.Service,
		method:       method.Name,
		direction:    request.Direction,
		consumer:     fiber.id,
		provider:     provider.Fiber,
		encoding:     encoding,
		up:           NewStreamBuffer(),
		down:         NewStreamBuffer(),
		providerRow:  provider,
		lastActivity: b.now(),
	}
	consumerHandle, providerHandle, err := b.addStream(stream)
	if err != nil {
		return callEnvelope(ServiceCallLimit, err.Error())
	}
	stream.consumerHandle = consumerHandle
	stream.providerHandle = providerHandle

	providerStream := &providerStreamHandle{
		Handle:        fmt.Sprintf("%d", uint64(providerHandle)),
		Direction:     request.Direction,
		ChunkLimit:    StreamChunkSize,
		InitialCredit: StreamInitialWindow,
		Open:          true,
	}
	response, callErr := b.callProvider(ctx, session, fiber, provider, method, payload,
		providerStream)
	if callErr != nil {
		b.closeStream(stream, "open failed", true)
		b.statsAdd(func(stats *ServiceCallStats) { stats.Errors++ })
		return b.streamFailure(session, provider, method, callErr)
	}
	envelope, _, err := b.decodeProviderResponse(provider, domainplugin.ServiceMethodDecl{
		Name:        method.Name,
		Encoding:    encoding,
		ContentType: method.ContentType,
	}, response)
	if err != nil {
		b.closeStream(stream, "provider rejected the stream", true)
		b.auditStream(session.ref, provider, provider.Service, method.Name, ServiceCallInternal, 0)
		return callEnvelope(ServiceCallInternal, err.Error())
	}
	if envelope.Code != ServiceCallOK {
		b.closeStream(stream, envelope.Code, true)
		b.auditStream(session.ref, provider, provider.Service, method.Name, envelope.Code, 0)
		return envelope
	}
	b.statsAdd(func(stats *ServiceCallStats) { stats.StreamsOpen++ })
	b.auditStream(session.ref, provider, provider.Service, method.Name, ServiceCallOK, 0)
	return okEnvelope(map[string]any{
		"handle":         fmt.Sprintf("%d", uint64(consumerHandle)),
		"provider":       provider.PluginID,
		"version":        provider.Version,
		"direction":      request.Direction,
		"chunk_limit":    StreamChunkSize,
		"initial_credit": StreamInitialWindow,
	})
}

// continueStream resolves the optional stream handle on an invoke request.
// The handle is the consumer endpoint; the provider receives its own endpoint
// so it can drain an upload or duplex stream during the call. There is no
// hidden reverse channel: a stream continuation is an ordinary authorized
// call.
func (b *ServiceBroker) continueStream(request invokeRequest, fiber *Fiber,
	provider ServiceProvider, encoding string) (*providerStreamHandle, serviceEnvelope) {
	if request.Stream == "" {
		return nil, serviceEnvelope{}
	}
	handle, err := parseServiceHandle(request.Stream)
	if err != nil {
		return nil, callEnvelope(ServiceCallInvalidArgument, "stream is not a decimal handle")
	}
	stream, failure := b.streamFor(handle, fiber.id)
	if stream == nil {
		return nil, failure
	}
	if stream.service != provider.Service || stream.provider != provider.Fiber {
		return nil, callEnvelope(ServiceCallNotFound,
			"the stream does not belong to this service provider")
	}
	if stream.encoding != encoding {
		return nil, callEnvelope(ServiceCallInvalidArgument,
			"the stream encoding does not match the method encoding")
	}
	if _, unsupported := streamReadBuffer(stream, provider.Fiber); unsupported.Code != "" {
		return nil, unsupported
	}
	stream.mu.Lock()
	closed := stream.closed
	stream.mu.Unlock()
	if closed {
		return nil, callEnvelope(ServiceCallUnavailable, "the stream is closed")
	}
	b.touch(stream)
	return &providerStreamHandle{
		Handle:        fmt.Sprintf("%d", uint64(stream.providerHandle)),
		Direction:     stream.direction,
		ChunkLimit:    StreamChunkSize,
		InitialCredit: StreamInitialWindow,
	}, serviceEnvelope{}
}

// streamFailure maps a provider failure for a stream operation.
func (b *ServiceBroker) streamFailure(session *ServiceSession, provider ServiceProvider,
	method domainplugin.ServiceMethodDecl, err error) serviceEnvelope {
	code := ServiceCallInternal
	switch {
	case errors.Is(err, ErrServiceBusy):
		code = ServiceCallBusy
	case errors.Is(err, ErrServiceDeadlock), errors.Is(err, ErrServiceDepth):
		code = ServiceCallDeadlock
	case errors.Is(err, ErrServiceCallUnavailable), errors.Is(err, ErrNotActive), errors.Is(err, ErrClosed):
		code = ServiceCallUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		code = ServiceCallTimeout
	case errors.Is(err, context.Canceled):
		code = ServiceCallCancelled
	}
	b.auditStream(session.ref, provider, provider.Service, method.Name, code, 0)
	return callEnvelope(code, err.Error())
}

// addStream allocates both endpoint handles and registers the stream.
func (b *ServiceBroker) addStream(stream *serviceStream) (Handle, Handle, error) {
	b.mu.Lock()
	if len(b.streams) >= MaxHandles {
		b.mu.Unlock()
		return 0, 0, errors.New("the broker stream table is full")
	}
	b.nextStream++
	consumer := b.nextStream
	b.nextStream++
	provider := b.nextStream
	stream.handle = consumer
	stream.peer = provider
	b.streams[consumer] = stream
	b.streams[provider] = stream
	stream.consumerHandle = consumer
	stream.providerHandle = provider
	b.mu.Unlock()
	b.armStreamIdle(stream)
	return consumer, provider, nil
}

// armStreamIdle closes a stream that is idle for the contract period.
func (b *ServiceBroker) armStreamIdle(stream *serviceStream) {
	if b.schedule == nil || stream == nil {
		return
	}
	stream.mu.Lock()
	if stream.closed || stream.unavailable || stream.timer != nil {
		stream.mu.Unlock()
		return
	}
	stream.timer = b.schedule(ServiceStreamIdleClose, func() { b.checkStreamIdle(stream) })
	stream.mu.Unlock()
}

func (b *ServiceBroker) checkStreamIdle(stream *serviceStream) {
	stream.mu.Lock()
	stream.timer = nil
	if stream.closed || stream.unavailable {
		stream.mu.Unlock()
		b.removeStream(stream)
		return
	}
	idle := b.now().Sub(stream.lastActivity)
	if idle < ServiceStreamIdleClose {
		remaining := ServiceStreamIdleClose - idle
		stream.mu.Unlock()
		if b.schedule != nil {
			stream.mu.Lock()
			stream.timer = b.schedule(remaining, func() { b.checkStreamIdle(stream) })
			stream.mu.Unlock()
		}
		return
	}
	stream.unavailable = true
	stream.reason = "stream closed after the idle period"
	_ = stream.up.Close()
	_ = stream.down.Close()
	stream.mu.Unlock()
}

// touch refreshed the idle deadline.
func (b *ServiceBroker) touch(stream *serviceStream) {
	stream.mu.Lock()
	stream.lastActivity = b.now()
	stream.mu.Unlock()
}

// streamFor resolves one endpoint handle and verifies ownership.
func (b *ServiceBroker) streamFor(handle Handle, fiberID domainplugin.FiberID) (*serviceStream, serviceEnvelope) {
	b.mu.Lock()
	stream := b.streams[handle]
	b.mu.Unlock()
	if stream == nil {
		return nil, callEnvelope(ServiceCallNotFound, "unknown stream handle")
	}
	if stream.consumerHandle != handle && stream.providerHandle != handle {
		return nil, callEnvelope(ServiceCallNotFound, "unknown stream handle")
	}
	if stream.consumer == fiberID {
		if stream.consumerHandle != handle {
			return nil, callEnvelope(ServiceCallNotFound, "unknown stream handle")
		}
	} else if stream.provider == fiberID {
		if stream.providerHandle != handle {
			return nil, callEnvelope(ServiceCallNotFound, "unknown stream handle")
		}
	} else {
		return nil, callEnvelope(ServiceCallNotFound, "unknown stream handle")
	}
	return stream, serviceEnvelope{}
}

// streamSlotAvailable enforces the 16-stream per-plugin quota across both
// endpoints.
func (b *ServiceBroker) streamSlotAvailable(id domainplugin.FiberID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	seen := make(map[*serviceStream]bool)
	for _, stream := range b.streams {
		if seen[stream] {
			continue
		}
		seen[stream] = true
		if stream.closed || stream.unavailable {
			continue
		}
		if stream.consumer == id || stream.provider == id {
			count++
		}
	}
	return count < MaxStreamsPerPlugin
}

// readBuffer returns the buffer this endpoint may read.
func streamReadBuffer(stream *serviceStream, id domainplugin.FiberID) (*StreamBuffer, serviceEnvelope) {
	if id == stream.consumer {
		if stream.direction == ServiceStreamDownload || stream.direction == ServiceStreamDuplex {
			return stream.down, serviceEnvelope{}
		}
		return nil, callEnvelope(ServiceCallUnsupported,
			"a "+stream.direction+" stream cannot be read by the consumer")
	}
	if stream.direction == ServiceStreamUpload || stream.direction == ServiceStreamDuplex {
		return stream.up, serviceEnvelope{}
	}
	return nil, callEnvelope(ServiceCallUnsupported,
		"a "+stream.direction+" stream cannot be read by the provider")
}

// writeBuffer returns the buffer this endpoint may write.
func streamWriteBuffer(stream *serviceStream, id domainplugin.FiberID) (*StreamBuffer, serviceEnvelope) {
	if id == stream.consumer {
		if stream.direction == ServiceStreamUpload || stream.direction == ServiceStreamDuplex {
			return stream.up, serviceEnvelope{}
		}
		return nil, callEnvelope(ServiceCallUnsupported,
			"a "+stream.direction+" stream cannot be written by the consumer")
	}
	if stream.direction == ServiceStreamDownload || stream.direction == ServiceStreamDuplex {
		return stream.down, serviceEnvelope{}
	}
	return nil, callEnvelope(ServiceCallUnsupported,
		"a "+stream.direction+" stream cannot be written by the provider")
}

// streamRead implements services.stream_read.
func (b *ServiceBroker) streamRead(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	handle, err := decodeStreamHandle(call)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if session == nil {
		return callEnvelope(ServiceCallUnavailable, "the plugin session is closed")
	}
	stream, failure := b.streamFor(handle, session.fiber)
	if stream == nil {
		return failure
	}
	b.touch(stream)
	stream.mu.Lock()
	unavailable, reason := stream.unavailable, stream.reason
	closed := stream.closed
	stream.mu.Unlock()
	if unavailable {
		if reason == "" {
			reason = ErrServiceCallUnavailable.Error()
		}
		return callEnvelope(ServiceCallUnavailable, reason)
	}
	buffer, failure := streamReadBuffer(stream, session.fiber)
	if buffer == nil {
		return failure
	}
	chunk, err := buffer.Read()
	switch {
	case errors.Is(err, ErrWouldBlock):
		return okEnvelope(map[string]any{"again": true})
	case errors.Is(err, ErrStreamClosed):
		b.markStreamEnd(stream, session.fiber)
		return okEnvelope(map[string]any{"end": true})
	case err != nil:
		return callEnvelope(ServiceCallInternal, err.Error())
	}
	b.statsAdd(func(stats *ServiceCallStats) { stats.StreamBytes += int64(len(chunk)) })
	_ = closed
	return okEnvelope(map[string]any{
		"chunk_b64": base64.StdEncoding.EncodeToString(chunk),
		"bytes":     len(chunk),
	})
}

// streamWrite implements services.stream_write.
func (b *ServiceBroker) streamWrite(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Handle   string `json:"handle"`
		ChunkB64 string `json:"chunk_b64"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	handle, err := parseServiceHandle(request.Handle)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	chunk, err := base64.StdEncoding.DecodeString(request.ChunkB64)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, "chunk_b64 is not valid base64")
	}
	if session == nil {
		return callEnvelope(ServiceCallUnavailable, "the plugin session is closed")
	}
	stream, failure := b.streamFor(handle, session.fiber)
	if stream == nil {
		return failure
	}
	b.touch(stream)
	stream.mu.Lock()
	unavailable, reason := stream.unavailable, stream.reason
	closed := stream.closed
	stream.mu.Unlock()
	if unavailable {
		if reason == "" {
			reason = ErrServiceCallUnavailable.Error()
		}
		return callEnvelope(ServiceCallUnavailable, reason)
	}
	if closed {
		return callEnvelope(ServiceCallUnavailable, "the stream is closed")
	}
	buffer, failure := streamWriteBuffer(stream, session.fiber)
	if buffer == nil {
		return failure
	}
	switch err := buffer.Write(chunk); {
	case errors.Is(err, ErrWouldBlock):
		return okEnvelope(map[string]any{"again": true})
	case errors.Is(err, ErrStreamClosed):
		return callEnvelope(ServiceCallUnavailable, "the stream is closed")
	case err != nil:
		var limit *LimitError
		if errors.As(err, &limit) {
			return callEnvelope(ServiceCallLimit, err.Error())
		}
		return callEnvelope(ServiceCallInternal, err.Error())
	}
	b.statsAdd(func(stats *ServiceCallStats) { stats.StreamBytes += int64(len(chunk)) })
	return okEnvelope(map[string]any{"bytes": len(chunk)})
}

// streamClose implements services.stream_close.
func (b *ServiceBroker) streamClose(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	handle, err := decodeStreamHandle(call)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if session == nil {
		return callEnvelope(ServiceCallUnavailable, "the plugin session is closed")
	}
	stream, failure := b.streamFor(handle, session.fiber)
	if stream == nil {
		return failure
	}
	provider := stream.providerRow
	b.closeStream(stream, "closed by the peer", false)
	b.auditStream(session.ref, provider, stream.service, stream.method, ServiceCallOK, 0)
	return okEnvelope(map[string]any{})
}

// closeStream closes both buffers once. Buffered chunks remain readable so a
// reader drains before seeing end.
func (b *ServiceBroker) closeStream(stream *serviceStream, reason string, unavailable bool) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	if stream.closed || stream.unavailable {
		stream.mu.Unlock()
		return
	}
	stream.closed = true
	if unavailable {
		stream.unavailable = true
		stream.reason = reason
	}
	_ = stream.up.Close()
	_ = stream.down.Close()
	stream.mu.Unlock()
}

// markStreamEnd releases one endpoint handle after the reader saw end. The
// stream leaves the table once both endpoints are done.
func (b *ServiceBroker) markStreamEnd(stream *serviceStream, id domainplugin.FiberID) {
	stream.mu.Lock()
	if id == stream.consumer {
		stream.consumerEnd = true
	}
	if id == stream.provider {
		stream.providerEnd = true
	}
	done := stream.consumerEnd && stream.providerEnd
	stream.mu.Unlock()
	if done {
		b.removeStream(stream)
	}
}

// removeStream removes both handles and stops the idle timer.
func (b *ServiceBroker) removeStream(stream *serviceStream) {
	if stream == nil {
		return
	}
	b.mu.Lock()
	delete(b.streams, stream.consumerHandle)
	delete(b.streams, stream.providerHandle)
	b.mu.Unlock()
	stream.mu.Lock()
	if stream.timer != nil {
		stream.timer()
		stream.timer = nil
	}
	stream.mu.Unlock()
}

// closeStreamsForFiber closes every stream a fiber participates in. A provider
// reload closes both ends as unavailable; a consumer unload closes its
// streams and notifies the provider.
func (b *ServiceBroker) closeStreamsForFiber(id domainplugin.FiberID) {
	b.mu.Lock()
	seen := make(map[*serviceStream]bool)
	streams := make([]*serviceStream, 0)
	for _, stream := range b.streams {
		if seen[stream] {
			continue
		}
		seen[stream] = true
		if stream.consumer == id || stream.provider == id {
			streams = append(streams, stream)
		}
	}
	b.mu.Unlock()
	for _, stream := range streams {
		b.closeStream(stream, ErrServiceCallUnavailable.Error(), true)
	}
}

// decodeStreamHandle parses the handle field of a stream request.
func decodeStreamHandle(call ServiceCall) (Handle, error) {
	var request struct {
		Handle string `json:"handle"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return 0, err
	}
	return parseServiceHandle(request.Handle)
}
