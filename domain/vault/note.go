package vault

import (
	"strings"
	"time"
	"unicode"
)

// Note is a Vault Note: free-form Markdown documentation, optionally grouped.
// A Note Group is identified by its slash-separated path, so a Note holds a
// path, not an id, and may keep a path no group record exists for. That is the
// Vault's non-cascading reference rule: deleting a Note Group never deletes a
// Note.
type Note struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	// Group is the slash-separated Note Group path, or "".
	Group     string    `json:"group,omitempty"`
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NoteDraft is the editable part of a Vault Note.
type NoteDraft struct {
	Title   string
	Content string
	Group   string
}

// NoteGroup is one saved Note Group. Its identity is its path.
type NoteGroup struct {
	Path      string    `json:"path"`
	OrderKey  int64     `json:"orderKey"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NoteGroupDraft is the editable part of a Note Group.
type NoteGroupDraft struct {
	Path string
}

const (
	// maxNoteTitleLength bounds the title shown in the list and exported as
	// the file name.
	maxNoteTitleLength = 255
	// maxNoteContentLength bounds one Markdown document. It is generous for a
	// runbook while keeping a malformed paste out of the Vault.
	maxNoteContentLength = 1 << 20
)

// NormalizeNoteDraft trims the title and canonicalizes the group path. The
// Markdown source is kept exactly as typed, because Markdown export must
// round-trip it byte for byte.
func NormalizeNoteDraft(draft NoteDraft) (NoteDraft, error) {
	draft.Title = strings.TrimSpace(draft.Title)
	draft.Group = NormalizeGroup(draft.Group)

	if draft.Title == "" {
		return NoteDraft{}, validation(FieldNote, RuleRequired)
	}
	if err := validateText(FieldNote, draft.Title, maxNoteTitleLength, false); err != nil {
		return NoteDraft{}, err
	}
	if len(draft.Content) > maxNoteContentLength {
		return NoteDraft{}, validation(FieldNoteContent, RuleTooLong)
	}
	if err := validateNoteGroupPath(draft.Group); err != nil {
		return NoteDraft{}, err
	}
	return draft, nil
}

// NormalizeNoteGroupDraft validates a Note Group path. A Note Group path is
// required: the Vault root is not a Note Group.
func NormalizeNoteGroupDraft(draft NoteGroupDraft) (NoteGroupDraft, error) {
	draft.Path = NormalizeGroup(draft.Path)
	if draft.Path == "" {
		return NoteGroupDraft{}, validation(FieldNoteGroup, RuleRequired)
	}
	if err := validateNoteGroupPath(draft.Path); err != nil {
		return NoteGroupDraft{}, err
	}
	return draft, nil
}

func validateNoteGroupPath(path string) error {
	if path == "" {
		return nil
	}
	if len(path) > maxGroupLength {
		return validation(FieldNoteGroup, RuleTooLong)
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" {
			return validation(FieldNoteGroup, RuleInvalid)
		}
		for _, r := range segment {
			if unicode.IsControl(r) {
				return validation(FieldNoteGroup, RuleInvalid)
			}
		}
		if err := validateText(FieldNoteGroup, segment, maxLabelLength, false); err != nil {
			return err
		}
	}
	return nil
}

// NoteGroupAncestors returns every prefix of a path, root first: "A/B" yields
// ["A", "A/B"].
func NoteGroupAncestors(path string) []string {
	path = NormalizeGroup(path)
	if path == "" {
		return nil
	}
	segments := strings.Split(path, "/")
	ancestors := make([]string, 0, len(segments))
	for i := range segments {
		ancestors = append(ancestors, strings.Join(segments[:i+1], "/"))
	}
	return ancestors
}

// NoteGroupDepth returns how many levels below the Vault root a Note Group
// path sits. The root is 0 and "ops/db" is 1.
func NoteGroupDepth(path string) int {
	path = NormalizeGroup(path)
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

// NoteGroupAtOrBelow reports whether path is root or a descendant of root.
func NoteGroupAtOrBelow(path, root string) bool { return IsGroupAtOrBelow(path, root) }

// ReplaceNoteGroupPrefix rewrites path when it is root or below root.
func ReplaceNoteGroupPrefix(path, root, next string) string {
	return ReplaceGroupPrefix(path, root, next)
}

// CollectNoteGroupPaths merges saved paths with every ancestor a saved or
// referenced path implies, in first-seen order.
func CollectNoteGroupPaths(saved []string, notes []Note) []string {
	seen := make(map[string]bool)
	paths := make([]string, 0, len(saved)+len(notes))
	add := func(raw string) {
		for _, ancestor := range NoteGroupAncestors(raw) {
			if seen[ancestor] {
				continue
			}
			seen[ancestor] = true
			paths = append(paths, ancestor)
		}
	}
	for _, path := range saved {
		add(path)
	}
	for _, note := range notes {
		add(note.Group)
	}
	return paths
}

// NotesInGroup returns the Notes directly in groupPath. The Vault root is "".
func NotesInGroup(notes []Note, groupPath string) []Note {
	groupPath = NormalizeGroup(groupPath)
	inGroup := make([]Note, 0, len(notes))
	for _, note := range notes {
		if NormalizeGroup(note.Group) == groupPath {
			inGroup = append(inGroup, note)
		}
	}
	return inGroup
}

// SearchNotes returns the Notes matching every whitespace-separated token of
// query. Matching is case-insensitive over the title, the Markdown source, and
// the Note Group path. An empty query returns every Note.
func SearchNotes(notes []Note, query string) []Note {
	tokens := strings.Fields(strings.ToLower(query))
	if len(tokens) == 0 {
		return append([]Note(nil), notes...)
	}
	matched := make([]Note, 0, len(notes))
	for _, note := range notes {
		haystack := strings.ToLower(note.Title + "\n" + note.Content + "\n" + note.Group)
		all := true
		for _, token := range tokens {
			if !strings.Contains(haystack, token) {
				all = false
				break
			}
		}
		if all {
			matched = append(matched, note)
		}
	}
	return matched
}
