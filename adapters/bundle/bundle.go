// Package bundle resolves the bundled Mosh and EternalTerminal client
// binaries. Every launch verifies the binary's size and SHA-256 against the
// manifest compiled into the app, so a replaced file is rejected instead of
// executed. A missing binary or an unsupported platform is a typed error; the
// app never falls back to a binary found on PATH.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Kind names one bundled client.
type Kind string

const (
	// Mosh is the bundled mosh-client.
	Mosh Kind = "mosh"
	// ET is the bundled EternalTerminal client.
	ET Kind = "et"
)

// Entry is one verified binary in the manifest.
type Entry struct {
	// Binary is the file name inside the target directory.
	Binary string
	// SHA256 is the hex digest of the binary.
	SHA256 string
	// Size is the binary's exact byte count.
	Size int64
}

// Asset is one release archive and the binary it contains. The fetch tool
// verifies the archive before extraction.
type Asset struct {
	Asset         string
	ArchiveSHA256 string
	Binary        string
	BinarySHA256  string
	Size          int64
}

// Spec is one bundled client's manifest and the roots searched for it.
type Spec struct {
	Kind    Kind
	Roots   []string
	Targets map[string]Entry
}

// ErrorKind classifies a resolution failure.
type ErrorKind string

const (
	// ErrorUnsupportedPlatform reports a GOOS/GOARCH pair with no build.
	ErrorUnsupportedPlatform ErrorKind = "unsupported_platform"
	// ErrorUnavailable reports that no search root contains the binary.
	ErrorUnavailable ErrorKind = "unavailable"
	// ErrorChecksumMismatch reports a binary that does not match the manifest.
	ErrorChecksumMismatch ErrorKind = "checksum_mismatch"
	// ErrorRead reports an unreadable binary.
	ErrorRead ErrorKind = "read"
)

// Error is a typed bundle failure.
type Error struct {
	Kind     ErrorKind
	Bundle   Kind
	Target   string
	Path     string
	Expected string
	Actual   string
	Searched []string
	Err      error
}

func (e *Error) Error() string {
	switch e.Kind {
	case ErrorUnsupportedPlatform:
		return fmt.Sprintf("%s: no bundled binary for %s", e.Bundle, e.Target)
	case ErrorChecksumMismatch:
		return fmt.Sprintf("%s: %s failed SHA-256 verification (want %s, got %s)",
			e.Bundle, e.Path, e.Expected, e.Actual)
	case ErrorRead:
		return fmt.Sprintf("%s: cannot read %s: %v", e.Bundle, e.Path, e.Err)
	case ErrorUnavailable:
		return fmt.Sprintf("%s: bundled binary for %s not found (searched %s)",
			e.Bundle, e.Target, strings.Join(e.Searched, ", "))
	default:
		return fmt.Sprintf("%s: bundle resolution failed", e.Bundle)
	}
}

// Unwrap exposes an underlying read error.
func (e *Error) Unwrap() error { return e.Err }

// ErrTarget reports the bundle error kind without exposing the struct.
func (e *Error) Is(target error) bool {
	kind, ok := target.(interface{ BundleErrorKind() ErrorKind })
	return ok && kind.BundleErrorKind() == e.Kind
}

// Resolved is a verified binary.
type Resolved struct {
	Path   string
	Target string
	Entry  Entry
}

// TargetKey maps a Go platform onto the release asset naming scheme. The
// original shipped one universal macOS build and no Windows arm64 build.
func TargetKey(goos, goarch string) (string, bool) {
	switch goos {
	case "windows":
		if goarch == "amd64" {
			return "win32-x64", true
		}
	case "linux":
		switch goarch {
		case "amd64":
			return "linux-x64", true
		case "arm64":
			return "linux-arm64", true
		}
	case "darwin":
		if goarch == "amd64" || goarch == "arm64" {
			return "darwin-universal", true
		}
	}
	return "", false
}

// HostTarget returns the current platform's target key.
func HostTarget() (string, bool) { return TargetKey(runtime.GOOS, runtime.GOARCH) }

// Resolve verifies the client for the current platform.
func (s Spec) Resolve() (Resolved, error) {
	target, ok := HostTarget()
	if !ok {
		return Resolved{}, &Error{
			Kind:   ErrorUnsupportedPlatform,
			Bundle: s.Kind,
			Target: runtime.GOOS + "-" + runtime.GOARCH,
		}
	}
	return s.ResolveTarget(target)
}

// ResolveTarget verifies the client for one target directory. The hash is
// recomputed on every call: there is no cache a replaced file could outlive.
func (s Spec) ResolveTarget(target string) (Resolved, error) {
	entry, ok := s.Targets[target]
	if !ok {
		return Resolved{}, &Error{
			Kind:   ErrorUnsupportedPlatform,
			Bundle: s.Kind,
			Target: target,
		}
	}
	searched := make([]string, 0, len(s.Roots))
	for _, root := range s.Roots {
		path := filepath.Join(root, string(s.Kind), target, entry.Binary)
		searched = append(searched, path)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Resolved{}, &Error{
				Kind: ErrorRead, Bundle: s.Kind, Target: target,
				Path: path, Searched: searched, Err: err,
			}
		}
		if info.IsDir() {
			continue
		}
		if verifyErr := verify(path, entry); verifyErr != nil {
			if typed, ok := verifyErr.(*Error); ok {
				typed.Bundle, typed.Target, typed.Searched = s.Kind, target, searched
			}
			return Resolved{}, verifyErr
		}
		return Resolved{Path: path, Target: target, Entry: entry}, nil
	}
	return Resolved{}, &Error{
		Kind: ErrorUnavailable, Bundle: s.Kind, Target: target, Searched: searched,
	}
}

// verify recomputes the digest and size of one file.
func verify(path string, entry Entry) error {
	file, err := os.Open(path)
	if err != nil {
		return &Error{Kind: ErrorRead, Path: path, Err: err}
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return &Error{Kind: ErrorRead, Path: path, Err: err}
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if size != entry.Size || !strings.EqualFold(actual, entry.SHA256) {
		return &Error{
			Kind: ErrorChecksumMismatch, Path: path,
			Expected: entry.SHA256, Actual: actual,
		}
	}
	return nil
}

// Roots assembles the search order for bundled resources: an explicit
// environment override, the toolkit's resource directory, the executable's
// directory, a sibling Resources directory (packaged macOS), and the working
// directory's resources (go build and go test). Duplicates and empty entries
// are dropped.
func Roots(resourceDir, executablePath, workingDir string) []string {
	var roots []string
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		absolute, err := filepath.Abs(root)
		if err != nil {
			absolute = root
		}
		for _, existing := range roots {
			if strings.EqualFold(existing, absolute) {
				return
			}
		}
		roots = append(roots, absolute)
	}
	if override := os.Getenv("NATCATTY_RESOURCES"); override != "" {
		add(override)
	}
	add(resourceDir)
	if executablePath != "" {
		exeDir := filepath.Dir(executablePath)
		add(exeDir)
		add(filepath.Join(exeDir, "..", "Resources"))
	}
	if workingDir != "" {
		add(filepath.Join(workingDir, "resources"))
	}
	return roots
}

// BundleError is the marker interface *Error satisfies, so callers can test
// errors.Is(err, TargetError{...}) without importing concrete errors.
type BundleError interface {
	error
	BundleErrorKind() ErrorKind
}

var _ BundleError = (*Error)(nil)

// BundleErrorKind implements BundleError.
func (e *Error) BundleErrorKind() ErrorKind { return e.Kind }

// TargetError matches bundle errors of one kind.
type TargetError struct{ Kind ErrorKind }

func (e TargetError) Error() string              { return string(e.Kind) }
func (e TargetError) BundleErrorKind() ErrorKind { return e.Kind }
