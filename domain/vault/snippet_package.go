package vault

import (
	"strings"
	"time"
	"unicode"
)

// SnippetPackage is one saved Snippet package. Its identity is its
// slash-separated path, so a rename is a path change and every Snippet holds a
// path, not an id. A Snippet may keep a package path no package record exists
// for, matching the Vault's non-cascading references.
type SnippetPackage struct {
	Path      string    `json:"path"`
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SnippetPackageDraft is the editable part of a Snippet package.
type SnippetPackageDraft struct {
	Path string
}

// SnippetPackageNamePattern describes one package path segment: letters,
// digits, underscores, and dashes, the same shape as the original
// SNIPPET_PACKAGE_NAME_PATTERN.
func validSnippetPackageSegment(segment string) bool {
	if segment == "" {
		return false
	}
	for _, r := range segment {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// NormalizeSnippetPackagePath canonicalizes a package path: backslashes become
// slashes, segments are trimmed, and empty segments are dropped. A path that
// keeps an invalid segment returns a ValidationError.
func NormalizeSnippetPackagePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	normalized := NormalizeGroup(path)
	if normalized == "" {
		return "", nil
	}
	for _, segment := range strings.Split(normalized, "/") {
		if !validSnippetPackageSegment(segment) {
			return "", validation(FieldSnippetPackage, RuleInvalid)
		}
	}
	if len(normalized) > maxGroupLength {
		return "", validation(FieldSnippetPackage, RuleTooLong)
	}
	return normalized, nil
}

// NormalizeSnippetPackageDraft validates a package draft. A package path is
// required: an empty package is the Vault root, which is not a package.
func NormalizeSnippetPackageDraft(draft SnippetPackageDraft) (SnippetPackageDraft, error) {
	path, err := NormalizeSnippetPackagePath(draft.Path)
	if err != nil {
		return SnippetPackageDraft{}, err
	}
	if path == "" {
		return SnippetPackageDraft{}, validation(FieldSnippetPackage, RuleRequired)
	}
	draft.Path = path
	return draft, nil
}

// SnippetPackageAtOrBelow reports whether path is root or a descendant of it.
func SnippetPackageAtOrBelow(path, root string) bool {
	return IsGroupAtOrBelow(path, root)
}

// SnippetPackageAncestors returns every prefix of a package path, so a tree
// can show the packages a deeper path implies.
func SnippetPackageAncestors(path string) []string {
	normalized := NormalizeGroup(path)
	if normalized == "" {
		return nil
	}
	segments := strings.Split(normalized, "/")
	ancestors := make([]string, 0, len(segments))
	for index := range segments {
		ancestors = append(ancestors, strings.Join(segments[:index+1], "/"))
	}
	return ancestors
}

// ReplaceSnippetPackagePrefix rewrites path when it is root or below root.
func ReplaceSnippetPackagePrefix(path, root, next string) string {
	return ReplaceGroupPrefix(path, root, next)
}

// CollectSnippetPackagePaths merges saved package paths with the ancestors of
// every path a Snippet references, in first-seen order.
func CollectSnippetPackagePaths(packages []string, snippets []Snippet) []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(packages))
	add := func(raw string) {
		for _, ancestor := range SnippetPackageAncestors(raw) {
			if seen[ancestor] {
				continue
			}
			seen[ancestor] = true
			paths = append(paths, ancestor)
		}
	}
	for _, path := range packages {
		add(path)
	}
	for _, snippet := range snippets {
		add(snippet.Package)
	}
	return paths
}
