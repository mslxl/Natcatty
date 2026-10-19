package forwarding

import (
	"errors"

	domainforward "github.com/mslxl/natcatty/domain/forwarding"
)

// Kind classifies a rule use-case failure. The UI maps it to a localized
// message; the application never contains user-visible text.
type Kind string

const (
	KindValidation Kind = "validation"
	KindNotFound   Kind = "not_found"
	KindDisabled   Kind = "disabled"
	KindInternal   Kind = "internal"
)

// Error is a typed rule use-case error.
type Error struct {
	Kind       Kind
	Validation *domainforward.ValidationError
	Cause      error
}

func (e *Error) Error() string {
	message := "forwarding: " + string(e.Kind)
	if e.Validation != nil {
		message += ": " + e.Validation.Error()
	}
	if e.Cause != nil {
		message += ": " + e.Cause.Error()
	}
	return message
}

func (e *Error) Unwrap() error { return e.Cause }

// ErrNotFound reports a rule that no longer exists.
var ErrNotFound = errors.New("forwarding: rule not found")

func invalid(err error) error {
	var validation *domainforward.ValidationError
	if errors.As(err, &validation) {
		return &Error{Kind: KindValidation, Validation: validation, Cause: err}
	}
	return &Error{Kind: KindValidation, Cause: err}
}

func notFound() error { return &Error{Kind: KindNotFound, Cause: ErrNotFound} }

func internal(err error) error { return &Error{Kind: KindInternal, Cause: err} }

func disabled() error {
	return &Error{Kind: KindDisabled, Cause: errors.New("forwarding: rule is disabled")}
}

// ErrorKind classifies a runtime forwarding failure. It is the runtime
// projection the UI localizes; Detail is a developer diagnostic and is never
// rendered as the primary message.
type ErrorKind string

const (
	ErrorNone         ErrorKind = ""
	ErrorBindConflict ErrorKind = "bind_conflict"
	ErrorAuth         ErrorKind = "auth"
	ErrorHostKey      ErrorKind = "host_key"
	ErrorNetwork      ErrorKind = "network"
	ErrorTimeout      ErrorKind = "timeout"
	ErrorUnsupported  ErrorKind = "unsupported"
	ErrorCancelled    ErrorKind = "cancelled"
	ErrorUnforward    ErrorKind = "unforward"
	ErrorInternal     ErrorKind = "internal"
)

// Hop identifies the chain endpoint that failed.
type Hop struct {
	Index int
	Total int
	Label string
}

// RuntimeError is a typed runtime failure. The connector may return it to
// preserve the connection layer's classification and hop context.
type RuntimeError struct {
	Kind   ErrorKind
	Hop    *Hop
	Detail string
	Cause  error
}

func (e *RuntimeError) Error() string {
	message := "forwarding runtime: " + string(e.Kind)
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *RuntimeError) Unwrap() error { return e.Cause }

// NewRuntimeError builds a typed runtime failure for a connector adapter.
func NewRuntimeError(kind ErrorKind, hop *Hop, detail string, cause error) error {
	return &RuntimeError{Kind: kind, Hop: hop, Detail: detail, Cause: cause}
}

// Permanent reports whether retrying cannot fix the failure. Binding
// conflicts, rejected credentials, and rejected host keys are permanent; the
// original deliberately did not retry a local bind conflict as if it were an
// SSH network failure.
func (e *RuntimeError) Permanent() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case ErrorBindConflict, ErrorAuth, ErrorHostKey, ErrorUnsupported, ErrorCancelled:
		return true
	default:
		return false
	}
}

func runtimeError(kind ErrorKind, cause error) *RuntimeError {
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	return &RuntimeError{Kind: kind, Detail: detail, Cause: cause}
}
