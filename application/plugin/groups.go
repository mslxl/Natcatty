package plugin

import (
	"context"
	"fmt"
	"sort"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// ServiceProvider is one ACTIVE fiber's provision of a plugin service. Service
// resolution is isolate-scoped: a consumer sees providers in its own group and
// in ancestor groups, never in a child or sibling group. The nearest scope
// wins, and within one scope the highest priority wins; equal priorities are a
// conflict for the default binding.
type ServiceProvider struct {
	Service   string               `json:"service"`
	Group     string               `json:"group"`
	Fiber     domainplugin.FiberID `json:"fiber"`
	PluginID  string               `json:"plugin_id"`
	Source    domainplugin.Source  `json:"source"`
	Version   string               `json:"version"`
	Priority  int                  `json:"priority"`
	Access    string               `json:"access"`
	Dynamic   bool                 `json:"dynamic,omitempty"`
	Methods   []string             `json:"methods,omitempty"`
	Events    []string             `json:"events,omitempty"`
	TableHash string               `json:"table_hash,omitempty"`
	// Declaration is the full provider table. It never carries payload data
	// and is omitted from JSON so diagnostics stays a summary.
	Declaration domainplugin.Provision `json:"-"`
}

// serviceProviderFromDeclaration builds the registry row for one provision.
func serviceProviderFromDeclaration(fiber *Fiber, declaration domainplugin.Provision) ServiceProvider {
	return ServiceProvider{
		Service:     declaration.Service,
		Group:       fiber.group,
		Fiber:       fiber.id,
		PluginID:    fiber.ref.ID,
		Source:      fiber.ref.Source,
		Version:     declaration.EffectiveVersion(),
		Priority:    declaration.Priority,
		Access:      declaration.EffectiveAccess(),
		Dynamic:     declaration.Dynamic,
		Methods:     declaration.MethodNames(),
		Events:      declaration.EventNames(),
		TableHash:   domainplugin.ProvisionHash(declaration),
		Declaration: declaration,
	}
}

// DefineGroup creates or replaces a group definition. It is not a fiber: it
// only scopes identity, default configuration, and service resolution. An
// unknown parent is rejected so a typo cannot silently split an isolate scope.
func (c *Container) DefineGroup(group domainplugin.Group) error {
	if c == nil {
		return fmt.Errorf("plugin group: container is nil")
	}
	if err := group.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	if group.Parent != "" {
		if _, ok := c.groups[group.Parent]; !ok {
			c.mu.Unlock()
			return fmt.Errorf("plugin group %s: parent %q is not defined", group.Name, group.Parent)
		}
	}
	for parent := group.Parent; parent != ""; {
		if parent == group.Name {
			c.mu.Unlock()
			return fmt.Errorf("plugin group %s: parent chain contains a cycle", group.Name)
		}
		existing, ok := c.groups[parent]
		if !ok {
			break
		}
		parent = existing.Parent
	}
	group.Defaults = cloneGroupDefaults(group.Defaults)
	c.groups[group.Name] = group
	c.mu.Unlock()
	return nil
}

// Group returns one group definition.
func (c *Container) Group(name string) (domainplugin.Group, bool) {
	if c == nil {
		return domainplugin.Group{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	group, ok := c.groups[name]
	if !ok {
		return domainplugin.Group{}, false
	}
	group.Defaults = cloneGroupDefaults(group.Defaults)
	return group, true
}

// Groups returns every group definition in name order. Implicit groups created
// by loading a plugin are included with no parent and no defaults.
func (c *Container) Groups() []domainplugin.Group {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	out := make([]domainplugin.Group, 0, len(c.groups))
	for _, group := range c.groups {
		group.Defaults = cloneGroupDefaults(group.Defaults)
		out = append(out, group)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// SetGroupDefaults replaces the defaults one group inherits for one plugin id.
// A manifest-declared default and a stored value both win over these values.
func (c *Container) SetGroupDefaults(group, pluginID string, values map[string]any) error {
	if c == nil {
		return fmt.Errorf("plugin group: container is nil")
	}
	c.mu.Lock()
	existing, ok := c.groups[group]
	if !ok {
		c.mu.Unlock()
		return fmt.Errorf("plugin group %s: is not defined", group)
	}
	updated := existing
	updated.Defaults = cloneGroupDefaults(existing.Defaults)
	if values == nil {
		delete(updated.Defaults, pluginID)
	} else {
		updated.Defaults[pluginID] = cloneSettings(values)
	}
	err := updated.Validate()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.groups[group] = updated
	c.mu.Unlock()
	return nil
}

// ensureGroup creates an implicit root group for a namespace that was not
// declared. Loading is the only path that creates groups implicitly.
func (c *Container) ensureGroup(name string) error {
	if c == nil {
		return ErrClosed
	}
	group := domainplugin.Group{Name: name}
	if err := group.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return ErrClosed
	}
	if _, ok := c.groups[name]; !ok {
		c.groups[name] = group
	}
	return nil
}

// groupDefaults merges the defaults for one plugin from the outermost ancestor
// to the group itself, so a child group overrides its parent.
func (c *Container) groupDefaults(group, pluginID string) map[string]any {
	if c == nil {
		return map[string]any{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	chain := c.groupChainLocked(group)
	out := make(map[string]any)
	for i := len(chain) - 1; i >= 0; i-- {
		for key, value := range c.groups[chain[i]].DefaultsFor(pluginID) {
			out[key] = domainplugin.CloneSettingValue(value)
		}
	}
	return out
}

// groupChainLocked returns the isolate scope chain from the consumer group
// outward to the root group.
func (c *Container) groupChainLocked(name string) []string {
	const maxDepth = 32
	chain := make([]string, 0, 4)
	seen := make(map[string]bool, 4)
	for name != "" && len(chain) < maxDepth && !seen[name] {
		seen[name] = true
		chain = append(chain, name)
		group, ok := c.groups[name]
		if !ok {
			break
		}
		name = group.Parent
	}
	return chain
}

// serviceResolutionStatus reports why a service lookup did or did not bind.
type serviceResolutionStatus int

const (
	serviceResolved serviceResolutionStatus = iota
	serviceMissing
	serviceVersionMismatch
	serviceConflict
)

// resolveServiceLocked finds the provider visible from group. The caller holds
// c.mu. A non-empty range filters by semver compatibility; a non-empty
// providerID targets one provider plugin identity. A scope that publishes the
// service but no compatible version is a mismatch rather than permission to
// fall through to an ancestor, so resolution is deterministic.
func (c *Container) resolveServiceLocked(service, group, versionRange, providerID string) (ServiceProvider, serviceResolutionStatus) {
	for _, scope := range c.groupChainLocked(group) {
		candidates := make([]ServiceProvider, 0, len(c.provides[service]))
		for _, provider := range c.provides[service] {
			if provider.Group != scope {
				continue
			}
			if providerID != "" && provider.PluginID != providerID {
				continue
			}
			candidates = append(candidates, provider)
		}
		if len(candidates) == 0 {
			continue
		}
		compatible := candidates[:0]
		for _, provider := range candidates {
			if serviceVersionCompatible(versionRange, provider.Version) {
				compatible = append(compatible, provider)
			}
		}
		if len(compatible) == 0 {
			return ServiceProvider{}, serviceVersionMismatch
		}
		best := compatible[0]
		ties := 1
		for _, provider := range compatible[1:] {
			switch {
			case provider.Priority > best.Priority:
				best = provider
				ties = 1
			case provider.Priority == best.Priority:
				ties++
				if provider.Fiber < best.Fiber {
					best = provider
				}
			}
		}
		if ties > 1 {
			return ServiceProvider{}, serviceConflict
		}
		return best, serviceResolved
	}
	return ServiceProvider{}, serviceMissing
}

// serviceVersionCompatible reports whether a provider version satisfies a
// consumer range. An empty or "*" range accepts every provider major.
func serviceVersionCompatible(versionRange, version string) bool {
	if versionRange == "" || versionRange == "*" {
		return true
	}
	parsed, ok := domainplugin.ParseSemver(version)
	if !ok {
		return false
	}
	return domainplugin.RangeContains(versionRange, parsed)
}

// InjectionProblem is one required injection that cannot bind. Code is a
// member of the cross-plugin PENDING reason vocabulary.
type InjectionProblem struct {
	Service string
	Code    string
}

// injectionProblems returns the required injections that cannot bind and the
// typed reason for each. A resolved provider the consumer has no
// services.invoke grant for is a permission problem, not a missing service.
func (c *Container) injectionProblems(fiber *Fiber) []InjectionProblem {
	if c == nil || fiber == nil {
		return nil
	}
	injections := fiber.manifestSnapshot().Inject
	if len(injections) == 0 {
		return nil
	}
	problems := make([]InjectionProblem, 0, len(injections))
	for _, injection := range injections {
		if injection.Optional {
			continue
		}
		provider, status := c.resolveService(injection.Service, fiber.group,
			injection.EffectiveRange(), "")
		switch status {
		case serviceMissing:
			problems = append(problems, InjectionProblem{Service: injection.Service, Code: domainplugin.ReasonServiceMissing})
		case serviceVersionMismatch:
			problems = append(problems, InjectionProblem{Service: injection.Service, Code: domainplugin.ReasonServiceVersionMismatch})
		case serviceConflict:
			problems = append(problems, InjectionProblem{Service: injection.Service, Code: domainplugin.ReasonServiceConflict})
		case serviceResolved:
			if c.broker != nil && !c.broker.consumerMayInvoke(fiber, provider) {
				problems = append(problems, InjectionProblem{Service: injection.Service, Code: domainplugin.ReasonPermissionDenied})
			}
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Slice(problems, func(i, j int) bool { return problems[i].Service < problems[j].Service })
	return problems
}

// resolveService resolves one service for a consumer group.
func (c *Container) resolveService(service, group, versionRange, providerID string) (ServiceProvider, serviceResolutionStatus) {
	if c == nil {
		return ServiceProvider{}, serviceMissing
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolveServiceLocked(service, group, versionRange, providerID)
}

// missingInjections returns the required services with no usable provider.
// It is the stable view diagnostics and the ticket-10 contract expose; the
// typed cause is available through injectionProblems.
func (c *Container) missingInjections(fiber *Fiber) []string {
	problems := c.injectionProblems(fiber)
	if len(problems) == 0 {
		return nil
	}
	missing := make([]string, 0, len(problems))
	for _, problem := range problems {
		missing = append(missing, problem.Service)
	}
	return missing
}

// injectionReason formats the PENDING reason for a set of injection problems.
// Missing-only sets retain the historic "injection-missing: a,b" form; other
// causes use the cross-plugin vocabulary.
func injectionReason(problems []InjectionProblem) string {
	if len(problems) == 0 {
		return ""
	}
	allMissing := true
	names := make([]string, 0, len(problems))
	code := domainplugin.ReasonServiceMissing
	severity := map[string]int{
		domainplugin.ReasonServiceMissing:         0,
		domainplugin.ReasonServiceVersionMismatch: 1,
		domainplugin.ReasonServiceConflict:        2,
		domainplugin.ReasonPermissionDenied:       3,
	}
	bestSeverity := -1
	for _, problem := range problems {
		names = append(names, problem.Service)
		if problem.Code != domainplugin.ReasonServiceMissing {
			allMissing = false
		}
		if rank, ok := severity[problem.Code]; ok && rank > bestSeverity {
			bestSeverity = rank
			code = problem.Code
		}
	}
	if allMissing {
		return domainplugin.InjectionReason(names)
	}
	if code == domainplugin.ReasonPermissionDenied {
		return domainplugin.ServiceInjectionReason(domainplugin.ReasonPermissionDenied, names)
	}
	return domainplugin.ServiceInjectionReason(code, names)
}

// registerProvides publishes an ACTIVE fiber's provisions to its isolate
// scope. A dynamic-only declaration reserves a name but publishes nothing
// until the guest calls services.register.
func (c *Container) registerProvides(fiber *Fiber) {
	if c == nil || fiber == nil {
		return
	}
	manifest := fiber.manifestSnapshot()
	for _, provision := range manifest.Provides {
		declaration := domainplugin.CloneProvision(provision)
		if declaration.Dynamic && len(declaration.Methods) == 0 && len(declaration.Events) == 0 {
			continue
		}
		c.registerProvision(fiber, declaration, false)
	}
}

// registerProvision inserts or replaces one fiber's provision in the
// registry. The caller must not hold c.mu.
func (c *Container) registerProvision(fiber *Fiber, declaration domainplugin.Provision, dynamic bool) {
	if c == nil || fiber == nil {
		return
	}
	declaration.Dynamic = declaration.Dynamic || dynamic
	provider := serviceProviderFromDeclaration(fiber, declaration)
	c.mu.Lock()
	defer c.mu.Unlock()
	providers := c.provides[declaration.Service]
	replaced := false
	for i := range providers {
		if providers[i].Fiber == fiber.id {
			providers[i] = provider
			replaced = true
			break
		}
	}
	if !replaced {
		providers = append(providers, provider)
	}
	c.provides[declaration.Service] = providers
}

// unregisterProvision removes one fiber's provision for a service.
func (c *Container) unregisterProvision(fiber *Fiber, service string) {
	if c == nil || fiber == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	providers := c.provides[service]
	out := providers[:0]
	for _, provider := range providers {
		if provider.Fiber != fiber.id {
			out = append(out, provider)
		}
	}
	if len(out) == 0 {
		delete(c.provides, service)
		return
	}
	c.provides[service] = out
}

// releaseProvides removes every provision owned by a fiber, including dynamic
// registrations, and releases its broker state. It is idempotent and safe on a
// fiber that never published anything.
func (c *Container) releaseProvides(fiber *Fiber) {
	if c == nil || fiber == nil {
		return
	}
	if c.broker != nil {
		c.broker.unpublishFiber(fiber)
	}
	c.unregisterProvides(fiber)
}

// reconcileServices re-evaluates injection PENDING fibers and refreshes the
// broker's binding view after a provider or consumer left the graph.
func (c *Container) reconcileServices() {
	if c == nil {
		return
	}
	c.reevaluate()
	if c.broker != nil {
		c.broker.reconcile()
	}
}

// unregisterProvides removes every provision owned by a fiber. Dependent
// fibers move back to PENDING at the next re-evaluation.
func (c *Container) unregisterProvides(fiber *Fiber) {
	if c == nil || fiber == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for service, providers := range c.provides {
		out := providers[:0]
		for _, provider := range providers {
			if provider.Fiber != fiber.id {
				out = append(out, provider)
			}
		}
		if len(out) == 0 {
			delete(c.provides, service)
			continue
		}
		c.provides[service] = out
	}
}

// Services returns every currently published provision in name order.
func (c *Container) Services() []ServiceProvider {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ServiceProvider, 0)
	for _, providers := range c.provides {
		out = append(out, providers...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Fiber < out[j].Fiber
	})
	return out
}

// reevaluate reconciles fibers with the current service graph. An injection
// PENDING fiber activates once its required services resolve; an ACTIVE fiber
// whose provider disappeared goes back to PENDING after an orderly teardown.
// Configuration and permission PENDING states are not auto-retried: their
// preconditions change through the manager, which calls Retry explicitly.
func (c *Container) reevaluate() {
	if c == nil || !c.reevaluating.CompareAndSwap(false, true) {
		return
	}
	defer c.reevaluating.Store(false)

	const maxPasses = 64
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		for _, fiber := range c.Fibers() {
			switch fiber.State() {
			case domainplugin.StatePending:
				fiber.mu.Lock()
				kind := fiber.pendingKind
				fiber.mu.Unlock()
				if kind != pendingInjection {
					continue
				}
				if len(c.injectionProblems(fiber)) > 0 {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(),
					DefaultInitTimeout+DefaultUnloadTimeout)
				manifest := fiber.manifestSnapshot()
				err := c.activateFiber(ctx, fiber, SettingsInheritance{
					Group:    c.groupDefaults(fiber.group, manifest.ID),
					Volatile: volatileSettings(manifest.Settings, fiber.Settings()),
				})
				cancel()
				if err == nil {
					changed = true
				}
			case domainplugin.StateActive:
				problems := c.injectionProblems(fiber)
				if len(problems) > 0 {
					c.suspend(fiber, problems)
					changed = true
				}
			}
		}
		if !changed {
			return
		}
	}
}

// suspend moves an ACTIVE fiber back to PENDING because a required injected
// service disappeared. It is an orderly unload: effects unwind in reverse
// order and no automatic restart is scheduled.
func (c *Container) suspend(fiber *Fiber, problems []InjectionProblem) {
	fiber.mu.Lock()
	if fiber.state != domainplugin.StateActive {
		fiber.mu.Unlock()
		return
	}
	fiber.state = domainplugin.StateUnloading
	fiber.reason = ""
	fiber.pendingKind = pendingNone
	fiber.restartPending = false
	instance := fiber.instanceRT
	fiber.instanceRT = nil
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StateUnloading, "")

	if c.broker != nil {
		c.broker.forgetFiber(fiber.id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.unloadTimeout)
	fiber.callMu.Lock()
	_ = c.teardown(ctx, fiber, instance)
	fiber.callMu.Unlock()
	cancel()
	c.releaseProvides(fiber)
	if c.broker != nil {
		c.broker.reconcile()
	}

	missing := make([]string, 0, len(problems))
	for _, problem := range problems {
		missing = append(missing, problem.Service)
	}
	reason := injectionReason(problems)
	fiber.mu.Lock()
	fiber.state = domainplugin.StatePending
	fiber.reason = reason
	fiber.pendingKind = pendingInjection
	fiber.missingInjections = append([]string(nil), missing...)
	fiber.mu.Unlock()
	c.notifyLifecycle(fiber, domainplugin.StatePending, reason)
}

// Settings returns a copy of the effective settings of the last activation or
// pending evaluation.
func (f *Fiber) Settings() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return cloneSettings(f.settings)
}

// volatileSettings keeps only fields the manifest marks volatile, which is the
// only guest-visible state a reload preserves.
func volatileSettings(schema domainplugin.SettingsSchema, values map[string]any) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any)
	for _, field := range schema.Fields {
		if !field.Volatile {
			continue
		}
		if value, ok := values[field.Key]; ok {
			out[field.Key] = domainplugin.CloneSettingValue(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneGroupDefaults(defaults map[string]map[string]any) map[string]map[string]any {
	if len(defaults) == 0 {
		return map[string]map[string]any{}
	}
	out := make(map[string]map[string]any, len(defaults))
	for id, values := range defaults {
		out[id] = cloneSettings(values)
	}
	return out
}
