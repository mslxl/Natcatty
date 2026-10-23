package transfer

import (
	"errors"
	"fmt"
	"strings"
)

// OperationKind names one user-visible file operation. The value is a stable
// identifier used in progress text and tests, never a display string.
type OperationKind string

const (
	// OpCopy copies entries within one pane or between panes.
	OpCopy OperationKind = "copy"
	// OpMove copies entries and removes the sources.
	OpMove OperationKind = "move"
	// OpUpload copies from a local pane to a remote pane.
	OpUpload OperationKind = "upload"
	// OpDownload copies from a remote pane to a local pane.
	OpDownload OperationKind = "download"
	// OpDelete removes entries.
	OpDelete OperationKind = "delete"
	// OpRename renames one entry in place.
	OpRename OperationKind = "rename"
	// OpMkdir creates a directory.
	OpMkdir OperationKind = "mkdir"
	// OpCreateFile creates an empty file.
	OpCreateFile OperationKind = "create-file"
	// OpChmod changes permission bits.
	OpChmod OperationKind = "chmod"
)

// Valid reports whether the kind is one the application knows.
func (k OperationKind) Valid() bool {
	switch k {
	case OpCopy, OpMove, OpUpload, OpDownload, OpDelete, OpRename, OpMkdir,
		OpCreateFile, OpChmod:
		return true
	}
	return false
}

// Errors returned by ValidateName and name derivation.
var (
	// ErrEmptyName reports a missing name.
	ErrEmptyName = errors.New("sftp: the name is empty")
	// ErrInvalidName reports a name that cannot be one path component.
	ErrInvalidName = errors.New("sftp: the name contains a path separator or control character")
	// ErrReservedName reports "." and "..".
	ErrReservedName = errors.New("sftp: the name is reserved")
)

// ValidateName reports whether name can be one new path component in either
// a local or a remote pane. Both separators are refused because a local
// Windows pane is as likely as a POSIX remote one, and a control character
// would corrupt listing output.
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" {
		return ErrEmptyName
	}
	if name == "." || name == ".." {
		return ErrReservedName
	}
	if strings.ContainsAny(name, `/\`) {
		return ErrInvalidName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return ErrInvalidName
		}
	}
	return nil
}

// CopyName derives a duplicate-free name in the original's "(copy N)" form:
// "report.txt" becomes "report (copy 1).txt" and a directory "src" becomes
// "src (copy 1)". exists reports whether a name is already taken.
func CopyName(name string, exists func(string) bool) string {
	if exists == nil || !exists(name) {
		return name
	}
	stem, extension := splitExtension(name)
	for number := 1; ; number++ {
		candidate := fmt.Sprintf("%s (copy %d)%s", stem, number, extension)
		if !exists(candidate) {
			return candidate
		}
	}
}

// splitExtension separates a trailing extension, keeping dotfiles whole:
// ".bashrc" has no extension.
func splitExtension(name string) (string, string) {
	index := strings.LastIndexByte(name, '.')
	if index <= 0 || index == len(name)-1 {
		return name, ""
	}
	return name[:index], name[index:]
}
