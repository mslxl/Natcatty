package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Cross-plugin service broker. A plugin provides a service through its
// manifest (or dynamic registration) and a consumer injects it by semver
// range. Calls travel through the host so the broker can apply the authority
// boundary, isolate-scoped resolution, the wait-for graph, bounded queues,
// deadline propagation, and a closed error envelope. Payloads are never
// logged or audited.

// ServiceChangedEvent is the payload of one service.changed host event. It is
// targeted at the consumer whose binding changed.
type ServiceChangedEvent struct {
	Service  string `json:"service"`
	Provider string `json:"provider,omitempty"`
	Version  string `json:"version,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Bound    bool   `json:"bound"`
	Optional bool   `json:"optional,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// ServiceBinding is one consumer's view of one inject declaration. Bound is
// false for an optional injection with no visible provider, and for a required
// injection while the consumer is PENDING.
type ServiceBinding struct {
	Consumer      domainplugin.FiberID `json:"consumer"`
	PluginID      string               `json:"plugin_id"`
	Service       string               `json:"service"`
	Range         string               `json:"range,omitempty"`
	Optional      bool                 `json:"optional"`
	Bound         bool                 `json:"bound"`
	Provider      string               `json:"provider,omitempty"`
	ProviderFiber domainplugin.FiberID `json:"provider_fiber,omitempty"`
	Version       string               `json:"version,omitempty"`
	Priority      int                  `json:"priority,omitempty"`
	Access        string               `json:"access,omitempty"`
	TableHash     string               `json:"table_hash,omitempty"`
	Reason        string               `json:"reason,omitempty"`
}

// ServiceCallStats is the bounded, payload-free cross-plugin counters shown in
// diagnostics.
type ServiceCallStats struct {
	Calls       uint64 `json:"calls"`
	Errors      uint64 `json:"errors"`
	Denied      uint64 `json:"denied"`
	Deadlocks   uint64 `json:"deadlocks"`
	Busy        uint64 `json:"busy"`
	Timeouts    uint64 `json:"timeouts"`
	Requests    int64  `json:"request_bytes"`
	Responses   int64  `json:"response_bytes"`
	Events      uint64 `json:"events"`
	EventsDrop  uint64 `json:"events_dropped"`
	StreamsOpen uint64 `json:"streams_opened"`
	StreamBytes int64  `json:"stream_bytes"`
}

// ServiceGraphDiagnostics is the first-party service graph view. It never
// includes payloads, schemas, or provider return data.
type ServiceGraphDiagnostics struct {
	Providers     []ServiceProvider        `json:"providers"`
	Bindings      []ServiceBinding         `json:"bindings,omitempty"`
	Pending       []ServiceBinding         `json:"pending,omitempty"`
	Static        []domainplugin.Provision `json:"-"`
	Calls         ServiceCallStats         `json:"calls"`
	Streams       int                      `json:"streams"`
	Subscriptions int                      `json:"subscriptions"`
}

// ServiceBroker owns the cross-plugin service graph for one container. It is
// safe for concurrent use. Container resolution and fiber state are read
// through the container; broker state is protected by mu and never locked
// while a guest call is in flight.
type ServiceBroker struct {
	container *Container
	authority *Authority
	audit     AuditSink
	now       func() time.Time
	onChanged func(PluginRef, ServiceChangedEvent)
	schedule  func(time.Duration, func()) func()

	mu         sync.Mutex
	dynamic    map[domainplugin.FiberID]map[string]domainplugin.Provision
	published  map[domainplugin.FiberID]map[string]bool
	bindings   map[domainplugin.FiberID]map[string]*ServiceBinding
	queues     map[domainplugin.FiberID]*providerQueue
	inflight   map[domainplugin.FiberID]map[uint64]context.CancelFunc
	nextCall   uint64
	streams    map[Handle]*serviceStream
	nextStream Handle
	subs       map[Handle]*serviceSubscription
	nextSub    Handle
	events     map[eventKey]uint64
	rate       map[domainplugin.FiberID]*eventRateWindow
	stats      ServiceCallStats
	waits      serviceWaitGraph
}

type eventKey struct {
	provider domainplugin.FiberID
	service  string
	event    string
}

type eventRateWindow struct {
	start time.Time
	count int
}

// NewServiceBroker returns a broker over one container. The container sets
// itself as the owner; callers normally use WithServiceBroker through the
// container rather than constructing one directly.
func newServiceBroker(container *Container, authority *Authority, audit AuditSink,
	now func() time.Time, onChanged func(PluginRef, ServiceChangedEvent)) *ServiceBroker {
	if now == nil {
		now = time.Now
	}
	if audit == nil {
		audit = NopAudit{}
	}
	return &ServiceBroker{
		container: container,
		authority: authority,
		audit:     audit,
		now:       now,
		onChanged: onChanged,
		dynamic:   make(map[domainplugin.FiberID]map[string]domainplugin.Provision),
		published: make(map[domainplugin.FiberID]map[string]bool),
		bindings:  make(map[domainplugin.FiberID]map[string]*ServiceBinding),
		queues:    make(map[domainplugin.FiberID]*providerQueue),
		inflight:  make(map[domainplugin.FiberID]map[uint64]context.CancelFunc),
		streams:   make(map[Handle]*serviceStream),
		subs:      make(map[Handle]*serviceSubscription),
		events:    make(map[eventKey]uint64),
		rate:      make(map[domainplugin.FiberID]*eventRateWindow),
	}
}

// Broker returns the container's cross-plugin broker. Diagnostics and tests
// use it; guest access goes through the services host module.
func (c *Container) Broker() *ServiceBroker {
	if c == nil {
		return nil
	}
	return c.broker
}

// Binding returns one consumer's inject binding for a service.
func (b *ServiceBroker) Binding(consumer domainplugin.FiberID,
	service string) (ServiceBinding, bool) {
	if b == nil {
		return ServiceBinding{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	perFiber := b.bindings[consumer]
	if perFiber == nil {
		return ServiceBinding{}, false
	}
	binding, ok := perFiber[service]
	if !ok {
		return ServiceBinding{}, false
	}
	return *binding, true
}

// Bindings returns the broker's inject bindings in stable order.
func (b *ServiceBroker) Bindings() []ServiceBinding {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ServiceBinding, 0)
	for _, perFiber := range b.bindings {
		for _, binding := range perFiber {
			out = append(out, *binding)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Consumer != out[j].Consumer {
			return out[i].Consumer < out[j].Consumer
		}
		return out[i].Service < out[j].Service
	})
	return out
}

// Provider returns one published provider row.
func (b *ServiceBroker) Provider(fiber domainplugin.FiberID, service string) (ServiceProvider, bool) {
	if b == nil {
		return ServiceProvider{}, false
	}
	for _, provider := range b.container.Services() {
		if provider.Fiber == fiber && provider.Service == service {
			return provider, true
		}
	}
	return ServiceProvider{}, false
}

// publishFiber publishes every static provision and every dynamic
// registration the fiber made before it became ACTIVE.
func (b *ServiceBroker) publishFiber(fiber *Fiber) {
	if b == nil || fiber == nil || b.container == nil {
		return
	}
	manifest := fiber.manifestSnapshot()
	for _, provision := range manifest.Provides {
		declaration := domainplugin.CloneProvision(provision)
		if declaration.Dynamic && len(declaration.Methods) == 0 && len(declaration.Events) == 0 {
			continue
		}
		b.publishDeclaration(fiber, declaration)
	}
	b.mu.Lock()
	dynamic := make([]domainplugin.Provision, 0)
	for _, declaration := range b.dynamic[fiber.id] {
		dynamic = append(dynamic, declaration)
	}
	b.mu.Unlock()
	for _, declaration := range dynamic {
		b.publishDeclaration(fiber, declaration)
	}
	b.bindConsumer(fiber)
	b.reconcile()
}

// publishDeclaration records and publishes one provision. It replaces an
// earlier provision from the same fiber for the same service. A provider
// without the services.provide grant publishes nothing; the audit row names
// the denial and required consumers stay PENDING.
func (b *ServiceBroker) publishDeclaration(fiber *Fiber, declaration domainplugin.Provision) {
	if b == nil || fiber == nil {
		return
	}
	if err := b.provideAllowed(fiber, declaration); err != nil {
		scope := serviceProvideScope(declaration.Service, declaration.EffectiveAccess())
		b.audit.Record(domainplugin.AuditEvent{
			Time:       b.now().UTC(),
			Event:      domainplugin.AuditService,
			PluginID:   fiber.ref.ID,
			Source:     fiber.ref.Source,
			Version:    fiber.ref.Version,
			Permission: domainplugin.PermissionServicesProvide,
			Scope:      &scope,
			Provider:   fiber.ref.ID,
			Method:     declaration.Service,
			Reason:     domainplugin.ServiceAuditReason(declaration.Service, "provide", ServiceCallPermissionDenied),
			Status:     ServiceCallPermissionDenied,
		})
		return
	}
	b.container.registerProvision(fiber, declaration, declaration.Dynamic)
	b.mu.Lock()
	perFiber := b.published[fiber.id]
	if perFiber == nil {
		perFiber = make(map[string]bool)
		b.published[fiber.id] = perFiber
	}
	perFiber[declaration.Service] = true
	b.mu.Unlock()
	b.emitChangedForService(declaration.Service)
}

// unpublishFiber removes every provision owned by a fiber.
func (b *ServiceBroker) unpublishFiber(fiber *Fiber) {
	if b == nil || fiber == nil || b.container == nil {
		return
	}
	b.mu.Lock()
	services := make([]string, 0, len(b.published[fiber.id]))
	for service := range b.published[fiber.id] {
		services = append(services, service)
	}
	delete(b.published, fiber.id)
	delete(b.dynamic, fiber.id)
	b.mu.Unlock()
	for _, service := range services {
		b.container.unregisterProvision(fiber, service)
	}
}

// unbindService unbinds every consumer bound to one provider's service and
// emits service_changed. A dynamic unregistration keeps the provider fiber
// ACTIVE, so this runs in addition to the fiber's own forget path.
func (b *ServiceBroker) unbindService(provider domainplugin.FiberID, service string) {
	if b == nil {
		return
	}
	unbound := make([]ServiceBinding, 0)
	b.mu.Lock()
	for _, perFiber := range b.bindings {
		binding := perFiber[service]
		if binding == nil || !binding.Bound || binding.ProviderFiber != provider {
			continue
		}
		binding.Bound = false
		binding.Provider = ""
		binding.ProviderFiber = ""
		binding.Version = ""
		binding.Priority = 0
		binding.Reason = domainplugin.ReasonServiceMissing
		unbound = append(unbound, *binding)
	}
	b.mu.Unlock()
	for _, binding := range unbound {
		b.notifyChanged(binding, false)
	}
}

// forgetFiber releases every stream, subscription, binding, queued request,
// and in-flight call associated with a fiber. It runs before teardown so a
// provider reload unblocks its consumers instead of deadlocking on the call
// slot.
func (b *ServiceBroker) forgetFiber(id domainplugin.FiberID) {
	if b == nil {
		return
	}
	b.closeStreamsForFiber(id)
	b.closeSubscriptionsForFiber(id)

	b.mu.Lock()
	if queue := b.queues[id]; queue != nil {
		queue.close()
		delete(b.queues, id)
	}
	for _, cancel := range b.inflight[id] {
		cancel()
	}
	delete(b.inflight, id)
	unbound := make([]ServiceBinding, 0)
	for _, perFiber := range b.bindings {
		for _, binding := range perFiber {
			if binding.ProviderFiber == id && binding.Bound {
				binding.Bound = false
				binding.Provider = ""
				binding.ProviderFiber = ""
				binding.Version = ""
				binding.Priority = 0
				binding.Reason = domainplugin.ReasonServiceMissing
				unbound = append(unbound, *binding)
			}
		}
	}
	delete(b.bindings, id)
	delete(b.rate, id)
	delete(b.events, eventKeyProvider(id))
	b.mu.Unlock()
	for _, binding := range unbound {
		b.notifyChanged(binding, false)
	}
}

// eventKeyProvider is a helper for deleting per-provider sequences. It is
// unused for identity comparison; the deletion below is explicit.
func eventKeyProvider(id domainplugin.FiberID) eventKey { return eventKey{provider: id} }

// bindConsumer records the consumer's inject bindings. It runs at activation
// and after the service graph changes.
func (b *ServiceBroker) bindConsumer(fiber *Fiber) {
	if b == nil || fiber == nil || b.container == nil {
		return
	}
	manifest := fiber.manifestSnapshot()
	b.mu.Lock()
	perFiber := b.bindings[fiber.id]
	if perFiber == nil {
		perFiber = make(map[string]*ServiceBinding)
		b.bindings[fiber.id] = perFiber
	}
	b.mu.Unlock()
	for _, injection := range manifest.Inject {
		provider, status := b.container.resolveService(injection.Service, fiber.group,
			injection.EffectiveRange(), "")
		binding := ServiceBinding{
			Consumer: fiber.id,
			PluginID: fiber.ref.ID,
			Service:  injection.Service,
			Range:    injection.EffectiveRange(),
			Optional: injection.Optional,
		}
		if status == serviceResolved {
			binding.Bound = true
			binding.Provider = provider.PluginID
			binding.ProviderFiber = provider.Fiber
			binding.Version = provider.Version
			binding.Priority = provider.Priority
			binding.Access = provider.Access
			binding.TableHash = provider.TableHash
			if b.authority != nil && !b.consumerMayInvoke(fiber, provider) {
				binding.Bound = false
				binding.Provider = ""
				binding.ProviderFiber = ""
				binding.Version = ""
				binding.Priority = 0
				binding.Reason = domainplugin.ReasonPermissionDenied
			}
		} else {
			binding.Reason = injectionStatusCode(status)
		}
		b.mu.Lock()
		previous := perFiber[injection.Service]
		perFiber[injection.Service] = &binding
		b.mu.Unlock()
		if previous == nil || previous.Bound != binding.Bound ||
			previous.ProviderFiber != binding.ProviderFiber ||
			previous.Version != binding.Version ||
			previous.Priority != binding.Priority ||
			previous.Reason != binding.Reason {
			b.notifyChanged(binding, binding.Bound)
		}
	}
}

// reconcile binds optional injections that gained a provider and rebinds
// bindings whose provider disappeared. Required consumers are handled by the
// container's PENDING suspension; this pass only keeps the binding view and
// target up to date.
func (b *ServiceBroker) reconcile() {
	if b == nil || b.container == nil {
		return
	}
	for _, fiber := range b.container.Fibers() {
		if fiber.State() != domainplugin.StateActive {
			continue
		}
		b.bindConsumer(fiber)
	}
}

// reconcileAfterGraphChange runs a container re-evaluation and a broker
// binding pass. The container uses it after an activation, reload, unload, or
// dynamic registration.
func (c *Container) reconcileAfterGraphChange() {
	if c == nil {
		return
	}
	c.reevaluate()
	if c.broker != nil {
		c.broker.reconcile()
	}
}

func injectionStatusCode(status serviceResolutionStatus) string {
	switch status {
	case serviceVersionMismatch:
		return domainplugin.ReasonServiceVersionMismatch
	case serviceConflict:
		return domainplugin.ReasonServiceConflict
	default:
		return domainplugin.ReasonServiceMissing
	}
}

func (b *ServiceBroker) notifyChanged(binding ServiceBinding, bound bool) {
	if b == nil || b.onChanged == nil {
		return
	}
	fiber, ok := b.container.Fiber(binding.Consumer)
	if !ok {
		return
	}
	b.onChanged(fiber.ref, ServiceChangedEvent{
		Service:  binding.Service,
		Provider: binding.Provider,
		Version:  binding.Version,
		Priority: binding.Priority,
		Bound:    bound,
		Optional: binding.Optional,
		Reason:   binding.Reason,
	})
}

// emitChangedForService notifies every consumer that injects service.
func (b *ServiceBroker) emitChangedForService(service string) {
	if b == nil {
		return
	}
	var changed []ServiceBinding
	b.mu.Lock()
	for _, perFiber := range b.bindings {
		if binding, ok := perFiber[service]; ok {
			changed = append(changed, *binding)
		}
	}
	b.mu.Unlock()
	for _, binding := range changed {
		b.notifyChanged(binding, binding.Bound)
	}
}

// consumerMayInvoke reports whether the consumer holds a services.invoke
// grant that covers the provider's service, identity, version, and any access
// class. A broker without an authority (direct container tests) skips the
// check; the manager always wires one.
func (b *ServiceBroker) consumerMayInvoke(fiber *Fiber, provider ServiceProvider) bool {
	if b == nil || b.authority == nil || fiber == nil {
		return true
	}
	return b.authority.HasServiceGrant(SubjectForRef(fiber.ref),
		domainplugin.PermissionServicesInvoke, serviceInvokeScope(provider), false)
}

// serviceInvokeScope builds the concrete scope required for one call.
func serviceInvokeScope(provider ServiceProvider) domainplugin.Scope {
	return domainplugin.Scope{
		Kind:     domainplugin.ScopeServices,
		Service:  provider.Service,
		Provider: provider.PluginID,
		Version:  provider.Version,
		Access:   provider.Access,
	}
}

// serviceMethodScope is serviceInvokeScope with the method's effective access
// class, which is what a read grant must not cover.
func serviceMethodScope(provider ServiceProvider, method domainplugin.ServiceMethodDecl) domainplugin.Scope {
	scope := serviceInvokeScope(provider)
	scope.Access = method.EffectiveAccess(provider.Access)
	return scope
}

// serviceProvideScope builds the concrete scope required to register or emit
// for a service.
func serviceProvideScope(service string, access string) domainplugin.Scope {
	return domainplugin.Scope{
		Kind:    domainplugin.ScopeServices,
		Service: service,
		Access:  access,
	}
}

// checkInvoke verifies one consumer call at the authority boundary. A caller
// outside a broker session (first-party host tests) passes a nil subject and
// is allowed; the guest path always passes the session subject.
func (b *ServiceBroker) checkInvoke(session *ServiceSession, provider ServiceProvider,
	method domainplugin.ServiceMethodDecl) error {
	if b == nil || b.authority == nil || session == nil {
		return nil
	}
	err := b.authority.CheckService(session.subject, domainplugin.PermissionServicesInvoke,
		serviceMethodScope(provider, method), false)
	if err != nil {
		b.recordDenied(session.ref, provider, method, err)
	}
	return err
}

// checkProvide verifies a services.provide grant for one service.
func (b *ServiceBroker) checkProvide(session *ServiceSession, service, access string) error {
	if b == nil || b.authority == nil || session == nil {
		return nil
	}
	err := b.authority.CheckService(session.subject, domainplugin.PermissionServicesProvide,
		serviceProvideScope(service, access), true)
	if err != nil {
		b.recordDenied(session.ref, ServiceProvider{Service: service, PluginID: session.ref.ID},
			domainplugin.ServiceMethodDecl{Name: "provide", Access: access}, err)
	}
	return err
}

// recordDenied appends one denied-call audit row without payload content.
func (b *ServiceBroker) recordDenied(caller PluginRef, provider ServiceProvider,
	method domainplugin.ServiceMethodDecl, cause error) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.stats.Denied++
	b.mu.Unlock()
	scope := serviceMethodScope(provider, method)
	b.audit.Record(domainplugin.AuditEvent{
		Time:       b.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   caller.ID,
		Source:     caller.Source,
		Version:    caller.Version,
		Permission: domainplugin.PermissionServicesInvoke,
		Scope:      &scope,
		Provider:   provider.PluginID,
		Method:     method.Name,
		Reason:     "service.invoke:denied",
		Status:     serviceCodePermissionDenied,
	})
	_ = cause
}

// auditCall appends one cross-plugin call row. It records sizes and durations,
// never payloads.
func (b *ServiceBroker) auditCall(caller PluginRef, provider ServiceProvider,
	method domainplugin.ServiceMethodDecl, requestBytes, responseBytes int,
	duration time.Duration, status string) {
	if b == nil {
		return
	}
	b.audit.Record(domainplugin.AuditEvent{
		Time:       b.now().UTC(),
		Event:      domainplugin.AuditService,
		PluginID:   caller.ID,
		Source:     caller.Source,
		Version:    caller.Version,
		Provider:   provider.PluginID,
		Method:     method.Name,
		Encoding:   method.EffectiveEncoding(),
		Bytes:      requestBytes + responseBytes,
		DurationMS: duration.Milliseconds(),
		Reason:     domainplugin.ServiceAuditReason(provider.Service, method.Name, status),
		Status:     status,
	})
}

// auditStream appends a stream lifecycle row with byte totals.
func (b *ServiceBroker) auditStream(caller PluginRef, provider ServiceProvider,
	service, method, status string, bytes int) {
	if b == nil {
		return
	}
	b.audit.Record(domainplugin.AuditEvent{
		Time:     b.now().UTC(),
		Event:    domainplugin.AuditService,
		PluginID: caller.ID,
		Source:   caller.Source,
		Version:  caller.Version,
		Provider: provider.PluginID,
		Method:   method,
		Bytes:    bytes,
		Reason:   domainplugin.ServiceAuditReason(service, method, status),
		Status:   status,
	})
}

// auditSubscription appends a subscription lifecycle row.
func (b *ServiceBroker) auditSubscription(session *ServiceSession, service, event, status string) {
	if b == nil || session == nil {
		return
	}
	b.audit.Record(domainplugin.AuditEvent{
		Time:     b.now().UTC(),
		Event:    domainplugin.AuditService,
		PluginID: session.ref.ID,
		Source:   session.ref.Source,
		Version:  session.ref.Version,
		Method:   event,
		Reason:   domainplugin.ServiceAuditReason(service, event, status),
		Status:   status,
	})
}

// ServiceGraph returns the diagnostics view of the service graph.
func (b *ServiceBroker) ServiceGraph() ServiceGraphDiagnostics {
	if b == nil {
		return ServiceGraphDiagnostics{}
	}
	graph := ServiceGraphDiagnostics{Providers: b.container.Services()}
	graph.Bindings = b.Bindings()
	for _, binding := range graph.Bindings {
		if !binding.Bound {
			graph.Pending = append(graph.Pending, binding)
		}
	}
	b.mu.Lock()
	graph.Calls = b.stats
	seen := make(map[*serviceStream]bool, len(b.streams)/2)
	for _, stream := range b.streams {
		if seen[stream] {
			continue
		}
		seen[stream] = true
		graph.Streams++
	}
	graph.Subscriptions = len(b.subs)
	b.mu.Unlock()
	return graph
}

// statsAdd applies one stats delta under the broker lock.
func (b *ServiceBroker) statsAdd(fn func(*ServiceCallStats)) {
	if b == nil {
		return
	}
	b.mu.Lock()
	fn(&b.stats)
	b.mu.Unlock()
}

// tableHash returns a stable digest of a declaration table.
func tableHash(declaration domainplugin.Provision) string {
	sum := sha256.Sum256([]byte(domainplugin.ProvisionHash(declaration)))
	return hex.EncodeToString(sum[:])
}

// ErrServiceCallUnavailable reports a provider that was unloaded or recycled
// while a call or stream depended on it.
var ErrServiceCallUnavailable = errors.New("plugin service: provider is unavailable")

// ErrServiceBusy reports a provider whose bounded service queue is full.
var ErrServiceBusy = errors.New("plugin service: provider is busy")

// ErrServiceDeadlock reports a wait-for edge that would create a cycle.
var ErrServiceDeadlock = errors.New("plugin service: deadlock")

// ErrServiceDepth reports a call that would exceed the depth bound.
var ErrServiceDepth = errors.New("plugin service: call depth limit reached")

// providerQueue bounds waiting service calls for one provider. The channel
// admits one in-flight call; waiters are counted against the queue limit and
// select on cancellation so a deadline propagates into the queue too.
type providerQueue struct {
	slot     chan struct{}
	closedCh chan struct{}
	mu       sync.Mutex
	waiting  int
	closed   bool
}

func newProviderQueue() *providerQueue {
	return &providerQueue{slot: make(chan struct{}, 1), closedCh: make(chan struct{})}
}

func (q *providerQueue) acquire(ctx context.Context) (func(), error) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return nil, ErrServiceCallUnavailable
	}
	if q.waiting >= domainplugin.MaxServiceProviderQueue {
		q.mu.Unlock()
		return nil, ErrServiceBusy
	}
	q.waiting++
	q.mu.Unlock()
	release := func() {
		<-q.slot
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
	}
	select {
	case q.slot <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
		return nil, ctx.Err()
	case <-q.closedCh:
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
		return nil, ErrServiceCallUnavailable
	}
}

func (q *providerQueue) close() {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.closedCh)
	}
	q.mu.Unlock()
}

// serviceWaitGraph tracks synchronous cross-plugin calls. An edge a->b means
// a is blocked waiting for b. Adding provider b to caller a fails when b can
// already reach a (a cycle) or when the longest path ending at a is already
// at the depth bound.
type serviceWaitGraph struct {
	mu    sync.Mutex
	edges map[domainplugin.FiberID]map[domainplugin.FiberID]struct{}
}

func (g *serviceWaitGraph) enter(caller, provider domainplugin.FiberID) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.edges == nil {
		g.edges = make(map[domainplugin.FiberID]map[domainplugin.FiberID]struct{})
	}
	if g.reaches(provider, caller, 0) {
		return ErrServiceDeadlock
	}
	if g.depthTo(caller, 0) >= domainplugin.MaxServiceCallDepth {
		return ErrServiceDepth
	}
	set := g.edges[caller]
	if set == nil {
		set = make(map[domainplugin.FiberID]struct{})
		g.edges[caller] = set
	}
	set[provider] = struct{}{}
	return nil
}

func (g *serviceWaitGraph) leave(caller, provider domainplugin.FiberID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	set := g.edges[caller]
	delete(set, provider)
	if len(set) == 0 {
		delete(g.edges, caller)
	}
}

// reaches reports whether target is reachable from start. The graph is
// acyclic and bounded by the depth limit, so a depth-bounded DFS is enough.
func (g *serviceWaitGraph) reaches(start, target domainplugin.FiberID, depth int) bool {
	if start == target {
		return true
	}
	if depth >= domainplugin.MaxServiceCallDepth {
		return false
	}
	for next := range g.edges[start] {
		if g.reaches(next, target, depth+1) {
			return true
		}
	}
	return false
}

// depthTo returns the length of the longest path ending at target.
func (g *serviceWaitGraph) depthTo(target domainplugin.FiberID, depth int) int {
	if depth >= domainplugin.MaxServiceCallDepth {
		return depth
	}
	best := depth
	for caller, targets := range g.edges {
		if _, ok := targets[target]; !ok {
			continue
		}
		if candidate := g.depthTo(caller, depth+1); candidate > best {
			best = candidate
		}
	}
	return best
}

// Stats returns a copy of the broker counters.
func (b *ServiceBroker) Stats() ServiceCallStats {
	if b == nil {
		return ServiceCallStats{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// ErrNotServiceProvider reports a module that declares provides but does not
// implement the optional service request export.
type ErrNotServiceProvider struct {
	PluginID string
}

func (e *ErrNotServiceProvider) Error() string {
	return fmt.Sprintf("plugin %s declares provides but does not export the service request function", e.PluginID)
}
