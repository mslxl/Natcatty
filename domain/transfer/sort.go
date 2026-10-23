package transfer

import (
	"slices"
	"sort"
	"strings"
)

// SortField is the column the browser orders by.
type SortField string

const (
	// SortName orders by file name (case-insensitive).
	SortName SortField = "name"
	// SortSize orders by byte size.
	SortSize SortField = "size"
	// SortModified orders by modification time.
	SortModified SortField = "modified"
	// SortType orders by extension, with directories grouped as "folder".
	SortType SortField = "type"
	// SortOwner orders by owner name.
	SortOwner SortField = "owner"
)

// SortFields returns the selectable sort columns in menu order.
func SortFields() []SortField {
	return []SortField{SortName, SortSize, SortModified, SortType, SortOwner}
}

// Valid reports whether the field is one the browser knows.
func (f SortField) Valid() bool {
	switch f {
	case SortName, SortSize, SortModified, SortType, SortOwner:
		return true
	}
	return false
}

// Sort is one pane's ordering. The zero value is name ascending with
// directories first, which is the original browser's default.
//
// MixKinds turns off the "directories first" rule, matching the original's
// folders-first option: when it is false, directories stay above files in
// both directions.
type Sort struct {
	Field      SortField `json:"field"`
	Descending bool      `json:"descending"`
	MixKinds   bool      `json:"mixKinds,omitempty"`
}

// Normalized replaces an unknown field with the default name ordering.
func (s Sort) Normalized() Sort {
	if !s.Field.Valid() {
		s.Field = SortName
	}
	return s
}

// SortEntries returns a sorted copy. The input order is preserved between
// equal entries, as the original's localeCompare sort does.
func SortEntries(entries []Entry, s Sort) []Entry {
	s = s.Normalized()
	out := slices.Clone(entries)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !s.MixKinds {
			if a.IsDir() && !b.IsDir() {
				return true
			}
			if !a.IsDir() && b.IsDir() {
				return false
			}
		}
		cmp := compareEntries(a, b, s.Field)
		if s.Descending {
			return cmp > 0
		}
		return cmp < 0
	})
	return out
}

func compareEntries(a, b Entry, field SortField) int {
	switch field {
	case SortSize:
		return compareInt64(a.Size, b.Size)
	case SortModified:
		return a.ModTime.Compare(b.ModTime)
	case SortType:
		return compareText(extension(a), extension(b))
	case SortOwner:
		return compareText(a.Owner, b.Owner)
	default:
		return compareText(a.Name, b.Name)
	}
}

// extension returns a case-folded extension for type ordering. Directories
// group together under "folder", as the original does.
func extension(entry Entry) string {
	if entry.IsDir() {
		return "folder"
	}
	name := entry.Name
	if i := strings.LastIndexByte(name, '.'); i >= 0 && i < len(name)-1 {
		return strings.ToLower(name[i+1:])
	}
	return ""
}

// compareText compares case-insensitively, falling back to the exact bytes so
// "A" and "a" keep a deterministic order.
func compareText(a, b string) int {
	lowerA, lowerB := strings.ToLower(a), strings.ToLower(b)
	if cmp := strings.Compare(lowerA, lowerB); cmp != 0 {
		return cmp
	}
	return strings.Compare(a, b)
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
