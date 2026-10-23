package transfer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// ErrorKind classifies a file-operation failure. The UI maps it to a
// localized message; the application never holds display strings.
type ErrorKind string

const (
	// KindExists reports a destination that is already present.
	KindExists ErrorKind = "exists"
	// KindNotFound reports a missing source or destination.
	KindNotFound ErrorKind = "not-found"
	// KindPermission reports a refused operation.
	KindPermission ErrorKind = "permission"
	// KindNotDir reports a path component that is not a directory.
	KindNotDir ErrorKind = "not-a-directory"
	// KindIsDir reports a file operation applied to a directory.
	KindIsDir ErrorKind = "is-a-directory"
	// KindNotEmpty reports removing a non-empty directory without recursion.
	KindNotEmpty ErrorKind = "not-empty"
	// KindInvalidName reports a name that is not one path component.
	KindInvalidName ErrorKind = "invalid-name"
	// KindInvalidMode reports an unparsable permission value.
	KindInvalidMode ErrorKind = "invalid-mode"
	// KindUnsupported reports an operation the transport cannot perform.
	KindUnsupported ErrorKind = "unsupported"
	// KindCancelled reports a user cancellation.
	KindCancelled ErrorKind = "cancelled"
	// KindConnection reports a lost or failed transport.
	KindConnection ErrorKind = "connection"
	// KindTooLarge reports a walk that exceeded its safety bound.
	KindTooLarge ErrorKind = "too-large"
	// KindInternal reports anything unclassified.
	KindInternal ErrorKind = "internal"
)

// Error is one failed file operation.
type Error struct {
	Kind  ErrorKind
	Op    domainsftp.OperationKind
	Path  string
	Cause error
}

// Error implements error. The message is for logs and tests; user-visible
// text comes from the UI catalog.
func (e *Error) Error() string {
	switch {
	case e == nil:
		return ""
	case e.Path != "":
		return fmt.Sprintf("sftp: %s %s: %v", e.Op, e.Path, e.Cause)
	default:
		return fmt.Sprintf("sftp: %s: %v", e.Op, e.Cause)
	}
}

// Unwrap exposes the underlying cause for errors.Is and errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// KindOf returns the classification of err, or KindInternal when err is not
// a file-operation error.
func KindOf(err error) ErrorKind {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	if err == nil {
		return ""
	}
	return classify(err)
}

var errCancelled = context.Canceled

// classify maps a filesystem cause onto an ErrorKind. Adapters may return
// their own *Error; this keeps a single classification path for os and
// transport errors.
func classify(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Kind
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return KindNotFound
	case errors.Is(err, fs.ErrPermission):
		return KindPermission
	case errors.Is(err, fs.ErrExist):
		return KindExists
	case errors.Is(err, errCancelled):
		return KindCancelled
	default:
		return KindInternal
	}
}

// wrap attaches an operation and path to a cause.
func wrap(op domainsftp.OperationKind, path string, err error) error {
	if err == nil {
		return nil
	}
	var typed *Error
	if errors.As(err, &typed) {
		return err
	}
	return &Error{Kind: classify(err), Op: op, Path: path, Cause: err}
}

// failure builds a typed error with an explicit kind.
func failure(kind ErrorKind, op domainsftp.OperationKind, path string, cause error) error {
	if cause == nil {
		cause = errKind(kind)
	}
	return &Error{Kind: kind, Op: op, Path: path, Cause: cause}
}

func errKind(kind ErrorKind) error {
	return errors.New(string(kind))
}
