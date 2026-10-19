package vault

import "fmt"

// Kind classifies a Vault error so the UI can choose a localized message.
type Kind string

const (
	// KindValidation reports input the Vault refused.
	KindValidation Kind = "validation"
	// KindNotFound reports a Host that no longer exists.
	KindNotFound Kind = "not_found"
	// KindConflict reports input that collides with an existing Host.
	KindConflict Kind = "conflict"
	// KindPermission reports a credential the user could not unwrap, such
	// as a wrong passphrase.
	KindPermission Kind = "permission"
	// KindUnsupported reports behavior this build or platform cannot
	// provide, such as an unavailable secret store.
	KindUnsupported Kind = "unsupported"
	// KindInternal reports a storage failure.
	KindInternal Kind = "internal"
)

// Error is a typed Vault error. Cause carries the domain validation detail or
// the storage failure; it is never user-visible text.
type Error struct {
	Kind  Kind
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("vault: %s: %v", e.Kind, e.Cause)
	}
	return fmt.Sprintf("vault: %s", e.Kind)
}

func (e *Error) Unwrap() error { return e.Cause }

// ErrNotFound reports a Host that does not exist. Repositories return it, and
// the Service passes it through unchanged.
var ErrNotFound = &Error{Kind: KindNotFound}

// conflict reports a Host whose endpoint already exists.
func conflict(cause error) error { return &Error{Kind: KindConflict, Cause: cause} }

// invalid reports a draft that broke a domain rule.
func invalid(cause error) error { return &Error{Kind: KindValidation, Cause: cause} }

// permission reports a credential the user could not unwrap.
func permission(cause error) error { return &Error{Kind: KindPermission, Cause: cause} }

// unsupported reports behavior this build or platform cannot provide.
func unsupported(cause error) error { return &Error{Kind: KindUnsupported, Cause: cause} }

// internal reports a storage failure the user cannot correct.
func internal(cause error) error { return &Error{Kind: KindInternal, Cause: cause} }
