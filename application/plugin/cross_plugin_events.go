package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Cross-plugin events are declared in the provider's service table. Delivery
// is at-most-once and ordered per provider and event, with a bounded queue,
// oldest-dropped plus a gap report on overflow, a per-provider emission rate
// limit, and a 64 KiB payload ceiling.

// serviceSubscription is one consumer's subscription to one declared event.
// The queue is per subscription, so delivery order is stable.
type serviceSubscription struct {
	handle Handle
	owner  domainplugin.FiberID
	ref    PluginRef

	service  string
	event    string
	provider domainplugin.FiberID
	encoding string
	filter   map[string]string

	mu        sync.Mutex
	queue     *BoundedQueue[serviceEvent]
	closed    bool
	dropped   uint64
	gap       uint64
	delivered uint64
}

// serviceEvent is one delivered event. Sequence is per provider and event.
type serviceEvent struct {
	Provider   string          `json:"provider"`
	Fiber      string          `json:"fiber"`
	Service    string          `json:"service"`
	Event      string          `json:"event"`
	Sequence   uint64          `json:"sequence"`
	Encoding   string          `json:"encoding"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	PayloadB64 string          `json:"payload_b64,omitempty"`
	Time       time.Time       `json:"time"`
}

// subscribe implements services.subscribe.
func (b *ServiceBroker) subscribe(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Service    string            `json:"service"`
		Event      string            `json:"event"`
		ProviderID string            `json:"provider_id,omitempty"`
		Filter     map[string]string `json:"filter,omitempty"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := domainplugin.ValidateServiceName(request.Service); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if err := validateServiceFilter(request.Filter); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	fiber, err := b.consumerFiber(session)
	if err != nil {
		return callEnvelope(ServiceCallUnavailable, err.Error())
	}
	provider, status := b.resolvedProvider(fiber, request.Service, request.ProviderID)
	if status != serviceResolved {
		return resolutionEnvelope(request.Service, status)
	}
	declaration, ok := provider.Declaration.Event(request.Event)
	if !ok {
		return callEnvelope(ServiceCallNotFound,
			fmt.Sprintf("service %s has no event %q", request.Service, request.Event))
	}
	if b.authority != nil && !b.consumerMayInvoke(fiber, provider) {
		scope := serviceInvokeScope(provider)
		return denialEnvelope(scope, ErrScopeDenied)
	}
	if !b.subscriptionSlotAvailable(fiber.id) {
		return callEnvelope(ServiceCallLimit,
			fmt.Sprintf("a plugin may hold at most %d service subscriptions", domainplugin.MaxServiceSubscriptions))
	}
	sub := &serviceSubscription{
		owner:    fiber.id,
		ref:      session.ref,
		service:  provider.Service,
		event:    request.Event,
		provider: provider.Fiber,
		encoding: declaration.EffectiveEncoding(),
		filter:   copyFilter(request.Filter),
		queue:    NewBoundedQueue[serviceEvent](domainplugin.MaxServiceEventQueue),
	}
	b.mu.Lock()
	b.nextSub++
	sub.handle = b.nextSub
	b.subs[sub.handle] = sub
	b.mu.Unlock()
	b.auditSubscription(session, provider.Service, request.Event, "subscribe")
	return okEnvelope(map[string]any{
		"handle":   fmt.Sprintf("%d", uint64(sub.handle)),
		"provider": provider.PluginID,
		"version":  provider.Version,
		"encoding": declaration.EffectiveEncoding(),
	})
}

// poll implements services.poll.
func (b *ServiceBroker) poll(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Handle string `json:"handle"`
		Limit  int    `json:"limit"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	handle, err := parseServiceHandle(request.Handle)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if session == nil {
		return callEnvelope(ServiceCallUnavailable, "the plugin session is closed")
	}
	sub := b.subscriptionFor(handle, session.fiber)
	if sub == nil {
		return callEnvelope(ServiceCallNotFound, "unknown subscription handle")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = DefaultEventPollLimit
	}
	if limit > MaxEventPollLimit {
		limit = MaxEventPollLimit
	}
	sub.mu.Lock()
	events := make([]serviceEvent, 0, limit)
	for len(events) < limit {
		event, ok := sub.queue.Pop()
		if !ok {
			break
		}
		events = append(events, event)
	}
	gap := sub.gap
	sub.gap = 0
	sub.delivered += uint64(len(events))
	sub.mu.Unlock()
	data := map[string]any{"events": events}
	if gap > 0 {
		data["gap"] = gap
	}
	return okEnvelope(data)
}

// unsubscribe implements services.unsubscribe.
func (b *ServiceBroker) unsubscribe(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Handle string `json:"handle"`
	}
	if err := decodeServiceRequest(call, &request); err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	handle, err := parseServiceHandle(request.Handle)
	if err != nil {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	if session == nil {
		return callEnvelope(ServiceCallUnavailable, "the plugin session is closed")
	}
	b.mu.Lock()
	sub := b.subs[handle]
	if sub == nil || sub.owner != session.fiber {
		b.mu.Unlock()
		return callEnvelope(ServiceCallNotFound, "unknown subscription handle")
	}
	delete(b.subs, handle)
	b.mu.Unlock()
	sub.mu.Lock()
	sub.closed = true
	sub.mu.Unlock()
	b.auditSubscription(session, sub.service, sub.event, "unsubscribe")
	return okEnvelope(map[string]any{})
}

// emit implements services.emit for a provider.
func (b *ServiceBroker) emit(_ context.Context, session *ServiceSession,
	call ServiceCall) serviceEnvelope {
	var request struct {
		Service    string          `json:"service"`
		Event      string          `json:"event"`
		Payload    json.RawMessage `json:"payload,omitempty"`
		PayloadB64 string          `json:"payload_b64,omitempty"`
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
	provider, ok := b.Provider(fiber.id, request.Service)
	if !ok {
		return callEnvelope(ServiceCallNotFound,
			fmt.Sprintf("this instance does not publish %s", request.Service))
	}
	if err := b.provideAllowed(fiber, provider.Declaration); err != nil {
		scope := serviceProvideScope(provider.Service, provider.Access)
		envelope := callEnvelope(ServiceCallPermissionDenied,
			"the services.provide grant does not cover this service")
		envelope.Permission = domainplugin.PermissionServicesProvide
		envelope.Scope = &scope
		return envelope
	}
	declaration, ok := provider.Declaration.Event(request.Event)
	if !ok {
		return callEnvelope(ServiceCallNotFound,
			fmt.Sprintf("service %s has no event %q", request.Service, request.Event))
	}
	if len(request.Payload) > 0 && request.PayloadB64 != "" {
		return callEnvelope(ServiceCallInvalidArgument,
			"payload and payload_b64 are mutually exclusive")
	}
	encoding := declaration.EffectiveEncoding()
	var payload []byte
	if encoding == domainplugin.ServiceEncodingBinary {
		if request.PayloadB64 == "" {
			return callEnvelope(ServiceCallInvalidArgument, "binary events take payload_b64")
		}
		payload, err = base64.StdEncoding.DecodeString(request.PayloadB64)
		if err != nil {
			return callEnvelope(ServiceCallInvalidArgument, "payload_b64 is not valid base64")
		}
		if len(payload) > domainplugin.MaxServiceEventBytes {
			return callEnvelope(ServiceCallLimit,
				fmt.Sprintf("event payload is %d bytes, above the %d byte limit",
					len(payload), domainplugin.MaxServiceEventBytes))
		}
	} else {
		if len(request.Payload) == 0 {
			return callEnvelope(ServiceCallInvalidArgument, "payload is required")
		}
		payload = request.Payload
		if err := domainplugin.ValidateServicePayload(encoding, declaration.Schema, payload,
			domainplugin.MaxServiceEventBytes); err != nil {
			return callEnvelope(ServiceCallInvalidArgument, err.Error())
		}
	}
	if !b.takeEventRate(fiber.id) {
		return callEnvelope(ServiceCallLimit,
			fmt.Sprintf("a provider may emit at most %d events per second",
				domainplugin.MaxServiceEventsPerSecond))
	}
	sequence := b.nextEventSequence(fiber.id, request.Service, request.Event)
	value, err := domainplugin.DecodeServiceJSON(payload)
	if err != nil && encoding != domainplugin.ServiceEncodingBinary {
		return callEnvelope(ServiceCallInvalidArgument, err.Error())
	}
	event := serviceEvent{
		Provider: provider.PluginID,
		Fiber:    string(provider.Fiber),
		Service:  provider.Service,
		Event:    request.Event,
		Sequence: sequence,
		Encoding: encoding,
		Time:     b.now().UTC(),
	}
	if encoding == domainplugin.ServiceEncodingBinary {
		event.PayloadB64 = base64.StdEncoding.EncodeToString(payload)
	} else {
		event.Payload = json.RawMessage(payload)
	}
	delivered, dropped := b.deliverEvent(provider, event, value)
	b.statsAdd(func(stats *ServiceCallStats) {
		stats.Events++
		stats.EventsDrop += uint64(dropped)
	})
	b.auditEventDelivery(session, provider, request.Event, delivered, dropped)
	return okEnvelope(map[string]any{"sequence": sequence})
}

// deliverEvent enqueues one event for every matching subscription.
func (b *ServiceBroker) deliverEvent(provider ServiceProvider, event serviceEvent,
	value any) (delivered, dropped int) {
	b.mu.Lock()
	subs := make([]*serviceSubscription, 0)
	for _, sub := range b.subs {
		if sub.service == provider.Service && sub.event == event.Event &&
			sub.provider == provider.Fiber {
			subs = append(subs, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range subs {
		if !filterMatches(sub.filter, value) {
			continue
		}
		sub.mu.Lock()
		if sub.closed {
			sub.mu.Unlock()
			continue
		}
		if sub.queue.Len() >= sub.queue.Cap() {
			_, _ = sub.queue.Pop()
			sub.dropped++
			sub.gap++
			dropped++
		}
		if err := sub.queue.Push(event); err != nil {
			sub.gap++
			dropped++
			sub.mu.Unlock()
			continue
		}
		sub.mu.Unlock()
		delivered++
	}
	return delivered, dropped
}

// takeEventRate applies the per-provider event rate limit.
func (b *ServiceBroker) takeEventRate(id domainplugin.FiberID) bool {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	window := b.rate[id]
	if window == nil || now.Sub(window.start) >= time.Second || now.Before(window.start) {
		window = &eventRateWindow{start: now}
		b.rate[id] = window
	}
	if window.count >= domainplugin.MaxServiceEventsPerSecond {
		return false
	}
	window.count++
	return true
}

// nextEventSequence returns the next per provider/service/event sequence.
func (b *ServiceBroker) nextEventSequence(id domainplugin.FiberID,
	service, event string) uint64 {
	key := eventKey{provider: id, service: service, event: event}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events[key]++
	return b.events[key]
}

// subscriptionFor resolves one subscription handle for its owner.
func (b *ServiceBroker) subscriptionFor(handle Handle,
	id domainplugin.FiberID) *serviceSubscription {
	b.mu.Lock()
	defer b.mu.Unlock()
	sub := b.subs[handle]
	if sub == nil || sub.owner != id {
		return nil
	}
	return sub
}

// subscriptionSlotAvailable enforces the per-plugin subscription quota.
func (b *ServiceBroker) subscriptionSlotAvailable(id domainplugin.FiberID) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, sub := range b.subs {
		if sub.owner == id {
			count++
		}
	}
	return count < domainplugin.MaxServiceSubscriptions
}

// closeSubscriptionsForFiber removes every subscription owned by a fiber or
// bound to it as provider.
func (b *ServiceBroker) closeSubscriptionsForFiber(id domainplugin.FiberID) {
	b.mu.Lock()
	removed := make([]*serviceSubscription, 0)
	for handle, sub := range b.subs {
		if sub.owner == id || sub.provider == id {
			delete(b.subs, handle)
			removed = append(removed, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range removed {
		sub.mu.Lock()
		sub.closed = true
		sub.mu.Unlock()
	}
}

// validateServiceFilter enforces the exact-match filter shape.
func validateServiceFilter(filter map[string]string) error {
	if len(filter) > domainplugin.MaxServiceFilterFields {
		return fmt.Errorf("filter has %d fields, above the limit of %d",
			len(filter), domainplugin.MaxServiceFilterFields)
	}
	for key, value := range filter {
		if key == "" || len(key) > domainplugin.MaxServiceJSONKeyBytes {
			return fmt.Errorf("filter key %q is empty or above %d bytes",
				key, domainplugin.MaxServiceJSONKeyBytes)
		}
		if len(value) > 256 {
			return fmt.Errorf("filter value for %q exceeds 256 bytes", key)
		}
	}
	return nil
}

func copyFilter(filter map[string]string) map[string]string {
	if len(filter) == 0 {
		return nil
	}
	out := make(map[string]string, len(filter))
	for key, value := range filter {
		out[key] = value
	}
	return out
}

// filterMatches compares the filter with the top-level string fields of the
// event payload. A missing or non-string field never matches.
func filterMatches(filter map[string]string, value any) bool {
	if len(filter) == 0 {
		return true
	}
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key, want := range filter {
		raw, present := object[key]
		if !present {
			return false
		}
		got, isString := raw.(string)
		if !isString || got != want {
			return false
		}
	}
	return true
}

// ServiceSubscriptions returns the live subscription rows for diagnostics.
func (b *ServiceBroker) ServiceSubscriptions() []map[string]any {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	subs := make([]*serviceSubscription, 0, len(b.subs))
	for _, sub := range b.subs {
		subs = append(subs, sub)
	}
	b.mu.Unlock()
	rows := make([]map[string]any, 0, len(subs))
	for _, sub := range subs {
		sub.mu.Lock()
		row := map[string]any{
			"owner":     string(sub.owner),
			"service":   sub.service,
			"event":     sub.event,
			"provider":  string(sub.provider),
			"queued":    sub.queue.Len(),
			"delivered": sub.delivered,
			"dropped":   sub.dropped,
			"closed":    sub.closed,
		}
		sub.mu.Unlock()
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		return fmt.Sprintf("%v", rows[i]) < fmt.Sprintf("%v", rows[j])
	})
	return rows
}
