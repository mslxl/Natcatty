// Package plugin is the pure plugin-system model: manifest identity and
// validation, contribution descriptors, lifecycle states, and fiber identity.
// It knows nothing about WASM, the filesystem, or the host UI.
package plugin

import "fmt"

// State is the lifecycle state of one Plugin Instance.
type State string

const (
	// StatePending means a fiber exists but no instance can run yet: a
	// required injection is missing, a permission is ungranted, or the
	// configuration is invalid.
	StatePending State = "PENDING"
	// StateLoading is the transient state while the module is validated and
	// instantiated.
	StateLoading State = "LOADING"
	// StateActive means the instance is initialized and its contributions
	// are registered.
	StateActive State = "ACTIVE"
	// StateUnloading is the transient state while the guest shuts down and
	// its effects unwind.
	StateUnloading State = "UNLOADING"
	// StateDisposed means the instance is gone and every effect was removed.
	StateDisposed State = "DISPOSED"
	// StateFailed means the instance could not be initialized or became
	// unusable. A failed fiber has a readable reason.
	StateFailed State = "FAILED"
)

// Valid reports whether s is one of the defined lifecycle states.
func (s State) Valid() bool {
	switch s {
	case StatePending, StateLoading, StateActive, StateUnloading,
		StateDisposed, StateFailed:
		return true
	default:
		return false
	}
}

// FiberID is the stable identity of one Plugin Instance. The container uses
// it for lookup and diagnostics: "group/id#instance".
type FiberID string

// NewFiberID returns the fiber identity for one instance of a plugin in a
// loader group. Instance numbers start at 1.
func NewFiberID(group, pluginID string, instance int) FiberID {
	return FiberID(fmt.Sprintf("%s/%s#%d", group, pluginID, instance))
}

// Group returns the loader namespace part of the identity.
func (id FiberID) Group() string {
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			return string(id[:i])
		}
	}
	return ""
}

// PluginID returns the plugin id part of the identity.
func (id FiberID) PluginID() string {
	start := 0
	for i := 0; i < len(id); i++ {
		if id[i] == '/' {
			start = i + 1
			break
		}
	}
	end := len(id)
	for i := start; i < len(id); i++ {
		if id[i] == '#' {
			end = i
			break
		}
	}
	return string(id[start:end])
}

// Instance returns the instance number part of the identity, or 0 when the
// identity is malformed.
func (id FiberID) Instance() int {
	marker := -1
	for i := 0; i < len(id); i++ {
		if id[i] == '#' {
			marker = i
			break
		}
	}
	if marker < 0 || marker == len(id)-1 {
		return 0
	}
	value := 0
	for i := marker + 1; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return 0
		}
		value = value*10 + int(id[i]-'0')
	}
	return value
}
