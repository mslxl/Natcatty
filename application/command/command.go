// Package command owns the application command registry: the set of named
// actions that native menus, key bindings, and future command surfaces invoke.
//
// Commands are application behavior, so this package depends only on the
// standard library. The registry is safe for concurrent use; handlers run on
// the goroutine that dispatches them.
package command

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ID names a command, such as "tab.new".
type ID string

// Handler runs a command. It runs on the goroutine that dispatches the
// command, so handlers that touch UI state must be dispatched on the UI thread.
type Handler func(context.Context) error

// Errors returned by Register and Dispatch.
var (
	// ErrInvalidCommand reports an empty ID or a nil handler.
	ErrInvalidCommand = errors.New("command: invalid command")
	// ErrDuplicateCommand reports a second registration of an ID.
	ErrDuplicateCommand = errors.New("command: duplicate command")
)

// UnknownCommandError reports an ID that has no registered handler.
type UnknownCommandError struct {
	ID ID
}

func (e *UnknownCommandError) Error() string {
	return fmt.Sprintf("command: unknown command %q", e.ID)
}

// Registry maps command IDs to handlers. It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	handlers map[ID]Handler
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[ID]Handler)}
}

// Register adds a handler for id. It rejects empty IDs, nil handlers, and
// IDs that are already registered.
func (r *Registry) Register(id ID, handler Handler) error {
	if id == "" || handler == nil {
		return ErrInvalidCommand
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.handlers[id]; exists {
		return ErrDuplicateCommand
	}
	r.handlers[id] = handler
	return nil
}

// Dispatch runs the handler registered for id.
func (r *Registry) Dispatch(ctx context.Context, id ID) error {
	r.mu.RLock()
	handler, ok := r.handlers[id]
	r.mu.RUnlock()
	if !ok {
		return &UnknownCommandError{ID: id}
	}
	return handler(ctx)
}

// Has reports whether id has a handler.
func (r *Registry) Has(id ID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[id]
	return ok
}
