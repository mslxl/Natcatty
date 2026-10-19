package desktop

import "errors"

// Kind classifies a desktop use-case failure. The UI maps it to a localized
// message; this package never contains user-visible text.
type Kind string

const (
	// KindValidation reports malformed deep-link or command-line input.
	KindValidation Kind = "validation"
	// KindUnsupported reports an integration the host cannot provide, or a
	// target transport the rewrite does not implement.
	KindUnsupported Kind = "unsupported"
	// KindHotkey reports that the toggle shortcut could not be registered.
	KindHotkey Kind = "hotkey"
	// KindAutoStart reports that the operating system refused the login item
	// change.
	KindAutoStart Kind = "auto_start"
	// KindInternal reports a storage or adapter failure.
	KindInternal Kind = "internal"
)

// Error is a typed desktop failure.
type Error struct {
	Kind  Kind
	Cause error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return "desktop: " + string(e.Kind) + ": " + e.Cause.Error()
	}
	return "desktop: " + string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Cause }

// KindOf returns the classification of err, or KindInternal when it did not
// come from this package.
func KindOf(err error) Kind {
	var target *Error
	if errors.As(err, &target) {
		return target.Kind
	}
	return KindInternal
}

// Sentinel errors. Callers wrap them with %w so errors.Is keeps working while
// KindOf still classifies the failure.
var (
	ErrInvalidTarget     = &Error{Kind: KindValidation, Cause: errors.New("invalid target")}
	ErrUnsupportedScheme = &Error{Kind: KindUnsupported, Cause: errors.New("unsupported URL scheme")}
	ErrUnsupported       = &Error{Kind: KindUnsupported, Cause: errors.New("unsupported on this host")}
	ErrHotkey            = &Error{Kind: KindHotkey, Cause: errors.New("cannot register the toggle hotkey")}
	ErrAutoStart         = &Error{Kind: KindAutoStart, Cause: errors.New("cannot change launch at login")}
)
