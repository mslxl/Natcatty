package transfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"

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
	// KindSelfCopy reports a source that contains its destination.
	KindSelfCopy ErrorKind = "copy-into-self"
	// KindUnsupported reports an operation the transport cannot perform.
	KindUnsupported ErrorKind = "unsupported"
	// KindCancelled reports a user cancellation.
	KindCancelled ErrorKind = "cancelled"
	// KindConnection reports a lost or failed transport.
	KindConnection ErrorKind = "connection"
	// KindTooLarge reports a walk that exceeded its safety bound.
	KindTooLarge ErrorKind = "too-large"
	// KindReserved reports a destination another Transfer already owns. Two
	// writers must never stage and rename into one path concurrently.
	KindReserved ErrorKind = "reserved"
	// KindTypeMismatch reports a replace or merge the destination's type cannot
	// support, such as a folder over a file. It fails closed.
	KindTypeMismatch ErrorKind = "type-mismatch"
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
	case isConnectionError(err):
		return KindConnection
	default:
		return KindInternal
	}
}

// connectionTokens are the transport-loss messages a filesystem or adapter
// surfaces when the peer or the stream went away. They are matched
// case-insensitively because x/crypto/ssh, os, the Windows socket layer, and
// the SFTP adapters phrase the same loss differently.
var connectionTokens = []string{
	"econnreset", "econnrefused", "econnaborted", "etimedout", "epipe",
	"broken pipe", "connection reset", "connection refused",
	"connection aborted", "connection closed", "connection lost",
	"connection was closed", "forcibly closed",
	"socket hang up", "network is unreachable", "network is down",
	"no route to host", "use of closed network connection",
	"ssh: disconnect", "ssh: connection", "failed to send packet",
	"server closed", "stream closed", "session closed",
	"unexpected eof", "i/o timeout",
}

// isConnectionError reports whether a cause is a lost or failed transport,
// which is recoverable only by reconnecting rather than by replaying the
// request over the same dead stream. The standard sentinels come first: a
// stopped peer often surfaces as a bare EOF or a closed net.Error before any
// adapter can add wording.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, token := range connectionTokens {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
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
