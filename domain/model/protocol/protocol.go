// Package protocol defines the stable identities of Host connection
// protocols: the built-in set the host owns and the namespaced ids a
// Connection Provider may claim.
//
// A Host's protocol is resolved through one registry so validation and
// transport selection read the same descriptor instead of repeating a switch.
// Plugin protocols use x-<publisher>-<name>; a built-in name is reserved and a
// second registration of an id is rejected. A Registry is safe for concurrent
// reads and writes, and listings are sorted so callers never depend on map
// order.
package protocol

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// ID names a Host connection protocol. Built-in values are persisted in Host
// records; a provider id is persisted the same way.
type ID string

const (
	// SSH is a Host reached over SSH.
	SSH ID = "ssh"
	// Telnet is a Host reached over Telnet.
	Telnet ID = "telnet"
	// Local is a shell that runs on this machine.
	Local ID = "local"
	// Mosh is an SSH Host that swaps to Mosh after the bootstrap.
	Mosh ID = "mosh"
	// ET is an SSH Host reached through the EternalTerminal transport.
	ET ID = "et"
	// Serial reserves the name for the serial Session transport. It is not a
	// Host protocol, but no provider may claim it.
	Serial ID = "serial"
)

// MaxIDLength bounds a protocol id. Real ids are short; the bound only rejects
// pasted garbage and keeps provider registration predictable.
const MaxIDLength = 128

// Transport names the Session transport a protocol selects. The values match
// application/session.Kind and the Workspace Session kinds, so selection needs
// no translation table.
type Transport string

const (
	// TransportLocal is a pseudo-terminal on this machine.
	TransportLocal Transport = "local"
	// TransportSSH is an interactive shell over an SSH Connection.
	TransportSSH Transport = "ssh"
	// TransportTelnet is a Telnet terminal.
	TransportTelnet Transport = "telnet"
	// TransportMosh is a UDP Mosh Session bootstrapped over SSH.
	TransportMosh Transport = "mosh"
	// TransportET is an EternalTerminal Session.
	TransportET Transport = "et"
	// TransportSerial is the serial device transport. No Host protocol selects
	// it today; it is reserved with the serial name.
	TransportSerial Transport = "serial"
	// TransportPlugin is a provider-owned raw byte stream. The host owns
	// terminal emulation, resize, authentication, and trust around it.
	TransportPlugin Transport = "plugin"
)

// Descriptor is one protocol's identity and the facts Host validation and
// transport selection need.
type Descriptor struct {
	ID        ID
	Transport Transport
	// DefaultPort is the port a Host uses when neither the Host nor its group
	// sets one. Zero means the protocol has no port.
	DefaultPort int
	// UsesSSHAuth reports whether the protocol authenticates with the shared
	// SSH credential bundle.
	UsesSSHAuth bool
	// GroupDefault reports whether a Host Group may set this protocol as the
	// default for its members.
	GroupDefault bool
	// HostProtocol reports whether a Host record may select the protocol.
	HostProtocol bool
	// Plugin is the plugin id that owns a provider protocol; empty for a
	// built-in.
	Plugin string
}

// Builtin reports whether the descriptor is a host-owned protocol.
func (d Descriptor) Builtin() bool {
	_, ok := builtinRegistry.Resolve(d.ID)
	return ok
}

// ProviderOwned reports whether the descriptor belongs to a plugin.
func (d Descriptor) ProviderOwned() bool { return ValidProviderID(d.ID) }

// Errors returned by ValidateDescriptor and Register.
var (
	// ErrInvalidProtocol reports an id or descriptor this host cannot use.
	ErrInvalidProtocol = errors.New("protocol: invalid protocol")
	// ErrReservedProtocol reports an attempt to claim a built-in name.
	ErrReservedProtocol = errors.New("protocol: reserved built-in protocol")
	// ErrDuplicateProtocol reports a second registration of an id.
	ErrDuplicateProtocol = errors.New("protocol: duplicate protocol")
)

// ValidProviderID reports whether id has the namespaced x-<publisher>-<name>
// shape plugins must use. It does not report whether a provider is registered.
// The publisher and name segments are lowercase ASCII letters and digits
// separated by hyphens; there must be at least one publisher and one name
// segment and the whole id is bounded.
func ValidProviderID(id ID) bool {
	text := string(id)
	if text == "" || len(text) > MaxIDLength || !strings.HasPrefix(text, "x-") {
		return false
	}
	segments := strings.Split(text[len("x-"):], "-")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		if segment == "" {
			return false
		}
		for _, r := range segment {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return false
			}
		}
	}
	return true
}

// ValidHostID reports whether a Host record may carry id: a built-in Host
// protocol or a syntactically valid provider protocol. Provider availability
// is a separate question resolved when the Session opens, so a Host saved by a
// build with a provider stays readable without it.
func ValidHostID(id ID) bool {
	if descriptor, ok := builtinRegistry.Resolve(id); ok {
		return descriptor.HostProtocol
	}
	return ValidProviderID(id)
}

// Reserved reports whether id belongs to the host. Built-in names, including
// serial, may never be claimed by a provider.
func Reserved(id ID) bool {
	_, ok := builtinRegistry.Resolve(id)
	return ok
}

// Registry holds protocol descriptors. Built-ins are installed by
// NewBuiltinRegistry; every other descriptor must use a namespaced provider id.
// A Registry is safe for concurrent use. Reads never observe a partially
// applied registration and listings are sorted.
type Registry struct {
	mu          sync.RWMutex
	descriptors map[ID]Descriptor
}

// NewRegistry returns an empty registry. It is useful for callers that install
// their own descriptors and for tests; use NewBuiltinRegistry to resolve the
// host's protocols.
func NewRegistry() *Registry {
	return &Registry{descriptors: make(map[ID]Descriptor)}
}

// NewBuiltinRegistry returns a registry containing every built-in protocol.
// Provider descriptors can be added with Register.
func NewBuiltinRegistry() *Registry {
	registry := NewRegistry()
	for _, descriptor := range builtinDescriptors() {
		registry.descriptors[descriptor.ID] = descriptor
	}
	return registry
}

// Register adds a provider descriptor. It rejects an invalid id or descriptor,
// a built-in name, and a second registration of the same id.
func (r *Registry) Register(descriptor Descriptor) error {
	if err := ValidateDescriptor(descriptor); err != nil {
		return err
	}
	if Reserved(descriptor.ID) {
		return fmt.Errorf("%w: %q", ErrReservedProtocol, descriptor.ID)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.descriptors[descriptor.ID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateProtocol, descriptor.ID)
	}
	r.descriptors[descriptor.ID] = descriptor
	return nil
}

// Resolve returns the descriptor for id.
func (r *Registry) Resolve(id ID) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptor, ok := r.descriptors[id]
	return descriptor, ok
}

// Descriptors returns every descriptor sorted by id.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	descriptors := make([]Descriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		descriptors = append(descriptors, descriptor)
	}
	slices.SortFunc(descriptors, func(a, b Descriptor) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return descriptors
}

// IDs returns every registered id sorted lexicographically.
func (r *Registry) IDs() []ID {
	descriptors := r.Descriptors()
	ids := make([]ID, len(descriptors))
	for i, descriptor := range descriptors {
		ids[i] = descriptor.ID
	}
	return ids
}

// Len reports the number of registered descriptors.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.descriptors)
}

// ValidateDescriptor reports whether descriptor can be registered. A provider
// protocol must name its owner and select the plugin transport; a built-in
// descriptor is validated by the host when it constructs the builtin registry.
func ValidateDescriptor(descriptor Descriptor) error {
	if descriptor.ID == "" || len(descriptor.ID) > MaxIDLength {
		return fmt.Errorf("%w: %q", ErrInvalidProtocol, descriptor.ID)
	}
	if !Reserved(descriptor.ID) && !ValidProviderID(descriptor.ID) {
		return fmt.Errorf("%w: %q", ErrInvalidProtocol, descriptor.ID)
	}
	switch descriptor.Transport {
	case TransportLocal, TransportSSH, TransportTelnet, TransportMosh,
		TransportET, TransportSerial, TransportPlugin:
	default:
		return fmt.Errorf("%w: unknown transport %q", ErrInvalidProtocol, descriptor.Transport)
	}
	if descriptor.DefaultPort < 0 || descriptor.DefaultPort > 65535 {
		return fmt.Errorf("%w: port %d out of range", ErrInvalidProtocol, descriptor.DefaultPort)
	}
	if ValidProviderID(descriptor.ID) && descriptor.Transport != TransportPlugin {
		return fmt.Errorf("%w: provider protocol %q must use the plugin transport",
			ErrInvalidProtocol, descriptor.ID)
	}
	return nil
}

// builtinDescriptors is the host's protocol set. Its ordering is the stable
// display order for callers that need one; Registry listings sort by id.
func builtinDescriptors() []Descriptor {
	return []Descriptor{
		{ID: SSH, Transport: TransportSSH, DefaultPort: 22, UsesSSHAuth: true, GroupDefault: true, HostProtocol: true},
		{ID: Telnet, Transport: TransportTelnet, DefaultPort: 23, GroupDefault: true, HostProtocol: true},
		{ID: Local, Transport: TransportLocal, HostProtocol: true},
		{ID: Mosh, Transport: TransportMosh, DefaultPort: 22, UsesSSHAuth: true, HostProtocol: true},
		{ID: ET, Transport: TransportET, DefaultPort: 22, UsesSSHAuth: true, HostProtocol: true},
		{ID: Serial, Transport: TransportSerial},
	}
}

// builtinRegistry is the read-only host protocol set. Package helpers resolve
// through it so validation and selection share one source.
var builtinRegistry = NewBuiltinRegistry()

// Lookup resolves a built-in protocol. A provider protocol lives in the
// registry the composition root owns, because availability is runtime state.
func Lookup(id ID) (Descriptor, bool) {
	return builtinRegistry.Resolve(id)
}

// BuiltinIDs returns the built-in ids sorted lexicographically.
func BuiltinIDs() []ID { return builtinRegistry.IDs() }

// BuiltinDescriptors returns every built-in descriptor sorted by id.
func BuiltinDescriptors() []Descriptor { return builtinRegistry.Descriptors() }
