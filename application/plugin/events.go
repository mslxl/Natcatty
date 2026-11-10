package plugin

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Event subscription bounds. Each subscription is one event type, so a
// per-subscription queue also preserves per-type ordering. Overflow drops the
// oldest event and reports a gap the next time the guest polls.
const (
	// MaxEventSubscriptions is the per-fiber subscription quota.
	MaxEventSubscriptions = 32
	// DefaultEventPollLimit is the number of events one poll returns when the
	// guest does not ask for a limit.
	DefaultEventPollLimit = 16
	// MaxEventPollLimit caps one poll response.
	MaxEventPollLimit = 64
)

// HostEvent is one event delivered to a subscription. Sequence is assigned per
// subscription in delivery order; Data is the event-type-specific JSON payload.
type HostEvent struct {
	Type     domainplugin.EventType `json:"type"`
	Sequence uint64                 `json:"seq"`
	Time     time.Time              `json:"time"`
	Data     json.RawMessage        `json:"data,omitempty"`
}

// NewHostEvent builds an event with a JSON payload. Marshal errors are folded
// into an empty payload; host event payloads are always marshalable.
func NewHostEvent(eventType domainplugin.EventType, at time.Time, data any) HostEvent {
	event := HostEvent{Type: eventType, Time: at.UTC()}
	if data != nil {
		payload, err := json.Marshal(data)
		if err != nil {
			payload = []byte(`{}`)
		}
		event.Data = payload
	}
	return event
}

// eventBus routes host events to per-fiber subscriptions. A targeted publish
// (for example a notification action) reaches only subscriptions owned by the
// same plugin instance.
type eventBus struct {
	mu    sync.Mutex
	depth int
	subs  map[domainplugin.EventType]map[*eventSubscription]struct{}
}

func newEventBus(depth int) *eventBus {
	if depth <= 0 {
		depth = MaxQueueDepth
	}
	return &eventBus{
		depth: depth,
		subs:  make(map[domainplugin.EventType]map[*eventSubscription]struct{}),
	}
}

func (b *eventBus) subscribe(sub *eventSubscription) {
	if b == nil || sub == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[sub.eventType]
	if set == nil {
		set = make(map[*eventSubscription]struct{})
		b.subs[sub.eventType] = set
	}
	set[sub] = struct{}{}
}

func (b *eventBus) unsubscribe(sub *eventSubscription) {
	if b == nil || sub == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	set := b.subs[sub.eventType]
	delete(set, sub)
	if len(set) == 0 {
		delete(b.subs, sub.eventType)
	}
}

// publish broadcasts event to every matching subscription.
func (b *eventBus) publish(event HostEvent) { b.publishTo("", event) }

// publishTo delivers event to matching subscriptions. A non-empty target
// restricts delivery to the plugin instance identified by the subject.
func (b *eventBus) publishTo(target AuthoritySubject, event HostEvent) {
	if b == nil || !domainplugin.IsKnownEventType(string(event.Type)) {
		return
	}
	b.mu.Lock()
	subs := make([]*eventSubscription, 0, len(b.subs[event.Type]))
	for sub := range b.subs[event.Type] {
		if target == "" || sub.owner == target {
			subs = append(subs, sub)
		}
	}
	b.mu.Unlock()
	for _, sub := range subs {
		sub.push(event)
	}
}

// eventSubscription is one generation-tagged subscription owned by a fiber.
type eventSubscription struct {
	bus       *eventBus
	owner     AuthoritySubject
	eventType domainplugin.EventType

	mu      sync.Mutex
	queue   *BoundedQueue[HostEvent]
	next    uint64
	dropped uint64
	gapAt   uint64
	gapTime time.Time
	closed  bool
}

func newEventSubscription(bus *eventBus, owner AuthoritySubject,
	eventType domainplugin.EventType) *eventSubscription {
	return &eventSubscription{
		bus:       bus,
		owner:     owner,
		eventType: eventType,
		queue:     NewBoundedQueue[HostEvent](bus.depth),
	}
}

// push enqueues one event. When the queue is full the oldest event is dropped
// and a gap is recorded at that position; the next poll emits one coalesced
// events.gap event before the surviving events.
func (s *eventSubscription) push(event HostEvent) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	event.Type = s.eventType
	event.Sequence = s.next
	s.next++
	if err := s.queue.Push(event); err != nil {
		oldest, ok := s.queue.Pop()
		if ok {
			if s.dropped == 0 {
				s.gapAt = oldest.Sequence
				s.gapTime = oldest.Time
			}
			s.dropped++
		}
		_ = s.queue.Push(event)
	}
}

// drain returns up to limit events in order. A recorded gap is emitted first,
// followed by the oldest surviving events.
func (s *eventSubscription) drain(limit int) []HostEvent {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = DefaultEventPollLimit
	}
	if limit > MaxEventPollLimit {
		limit = MaxEventPollLimit
	}
	out := make([]HostEvent, 0, limit)
	if s.dropped > 0 && len(out) < limit {
		payload, _ := json.Marshal(map[string]any{
			"type":    string(s.eventType),
			"dropped": s.dropped,
		})
		out = append(out, HostEvent{
			Type:     domainplugin.EventGap,
			Sequence: s.gapAt,
			Time:     s.gapTime,
			Data:     payload,
		})
		s.dropped = 0
		s.gapAt = 0
		s.gapTime = time.Time{}
	}
	for len(out) < limit {
		event, ok := s.queue.Pop()
		if !ok {
			break
		}
		out = append(out, event)
	}
	return out
}

func (s *eventSubscription) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	for {
		if _, ok := s.queue.Pop(); !ok {
			break
		}
	}
	s.mu.Unlock()
	s.bus.unsubscribe(s)
}

// EventsSubscribe returns a generation-tagged subscription for one event type.
// The events.subscribe permission is bound to that exact type.
func (s *ServiceSession) EventsSubscribe(eventType string) (Handle, error) {
	if eventType == "" || !domainplugin.IsKnownEventType(eventType) {
		return 0, s.fail(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
			domainplugin.Scope{}, ServiceCodeInvalid,
			&ServiceError{Service: ServiceEvents, Method: "subscribe", Code: ServiceCodeInvalid,
				Permission: domainplugin.PermissionEventsSubscribe,
				Message:    fmt.Sprintf("%q is not a declared event type", eventType)})
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: eventType}
	if err := s.check(ServiceEvents, "subscribe",
		domainplugin.PermissionEventsSubscribe, scope); err != nil {
		return 0, err
	}
	s.mu.Lock()
	if s.handles == nil {
		s.mu.Unlock()
		return 0, s.fail(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
			scope, ServiceCodeFault,
			serviceError(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
				scope, ErrServiceUnavailable))
	}
	if len(s.events) >= MaxEventSubscriptions {
		s.mu.Unlock()
		limited := &LimitError{Resource: "event subscriptions", Limit: MaxEventSubscriptions}
		return 0, s.fail(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
			scope, ServiceCodeLimit,
			serviceError(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
				scope, limited))
	}
	sub := newEventSubscription(s.host.bus, s.subject, domainplugin.EventType(eventType))
	handle, err := s.handles.Issue(sub)
	if err != nil {
		s.mu.Unlock()
		return 0, s.fail(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
			scope, ServiceCodeLimit,
			serviceError(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe,
				scope, err))
	}
	s.events[handle] = sub
	s.mu.Unlock()
	s.host.bus.subscribe(sub)
	s.record(ServiceEvents, "subscribe", domainplugin.PermissionEventsSubscribe, scope, ServiceCodeOK)
	return handle, nil
}

// EventsPoll returns queued events for one subscription. It never blocks.
func (s *ServiceSession) EventsPoll(handle Handle, limit int) ([]HostEvent, error) {
	sub, err := s.subscription(handle)
	if err != nil {
		return nil, err
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: string(sub.eventType)}
	if err := s.check(ServiceEvents, "poll",
		domainplugin.PermissionEventsSubscribe, scope); err != nil {
		return nil, err
	}
	events := sub.drain(limit)
	s.record(ServiceEvents, "poll", domainplugin.PermissionEventsSubscribe, scope, ServiceCodeOK)
	return events, nil
}

// EventsUnsubscribe releases one subscription handle immediately.
func (s *ServiceSession) EventsUnsubscribe(handle Handle) error {
	sub, err := s.subscription(handle)
	if err != nil {
		return err
	}
	scope := domainplugin.Scope{Kind: domainplugin.ScopeEvents, Event: string(sub.eventType)}
	if err := s.check(ServiceEvents, "unsubscribe",
		domainplugin.PermissionEventsSubscribe, scope); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.events, handle)
	releaseErr := error(nil)
	if s.handles != nil {
		releaseErr = s.handles.Release(handle)
	}
	s.mu.Unlock()
	sub.close()
	if releaseErr != nil {
		return s.fail(ServiceEvents, "unsubscribe", domainplugin.PermissionEventsSubscribe,
			scope, ServiceCodeInvalid,
			serviceError(ServiceEvents, "unsubscribe", domainplugin.PermissionEventsSubscribe,
				scope, releaseErr))
	}
	s.record(ServiceEvents, "unsubscribe", domainplugin.PermissionEventsSubscribe, scope, ServiceCodeOK)
	return nil
}

// subscription resolves a handle to the live subscription.
func (s *ServiceSession) subscription(handle Handle) (*eventSubscription, error) {
	if s == nil {
		return nil, &ServiceError{Service: ServiceEvents, Code: ServiceCodeFault,
			Message: ErrServiceUnavailable.Error(), Cause: ErrServiceUnavailable}
	}
	s.mu.Lock()
	sub, ok := s.events[handle]
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nil, &ServiceError{Service: ServiceEvents, Code: ServiceCodeInvalid,
			Message: "the plugin instance is closed"}
	}
	if !ok {
		return nil, s.fail(ServiceEvents, "handle", domainplugin.PermissionEventsSubscribe,
			domainplugin.Scope{}, ServiceCodeInvalid,
			&ServiceError{Service: ServiceEvents, Code: ServiceCodeInvalid,
				Permission: domainplugin.PermissionEventsSubscribe,
				Message:    fmt.Sprintf("event subscription %#x is invalid or revoked", uint64(handle)),
				Cause:      &InvalidHandleError{Handle: handle}})
	}
	return sub, nil
}

// Publish broadcasts one host event to every matching subscription. A nil
// HostServices or unknown event type is a no-op.
func (h *HostServices) Publish(eventType domainplugin.EventType, data any) {
	h.publishTo("", eventType, data)
}

// PublishTo delivers one host event only to subscriptions owned by ref.
func (h *HostServices) PublishTo(ref PluginRef, eventType domainplugin.EventType, data any) {
	h.publishTo(SubjectForRef(ref), eventType, data)
}

func (h *HostServices) publishTo(target AuthoritySubject, eventType domainplugin.EventType, data any) {
	if h == nil || !domainplugin.IsKnownEventType(string(eventType)) {
		return
	}
	h.bus.publishTo(target, NewHostEvent(eventType, h.now(), data))
}

// SubscribeCount reports live subscriptions for diagnostics.
func (h *HostServices) SubscribeCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, session := range h.sessions {
		session.mu.Lock()
		total += len(session.events)
		session.mu.Unlock()
	}
	return total
}
