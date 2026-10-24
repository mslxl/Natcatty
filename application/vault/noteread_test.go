package vault

import (
	"strings"
	"testing"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

func testNote(content string) domainvault.Note {
	return domainvault.Note{
		ID:        "note-1",
		Title:     "Runbook",
		Content:   content,
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		UpdatedAt: time.Date(2026, 1, 2, 3, 4, 6, 0, time.UTC),
	}
}

func TestReadNoteExcerptUsesUTF16Offsets(t *testing.T) {
	note := testNote("a😀bcdef")
	// UTF-16 layout: a=0, emoji=1..2, b=3, c=4, d=5, e=6, f=7.
	excerpt, err := ReadNoteExcerpt(note, map[string]any{
		"offset":   float64(3),
		"maxChars": float64(2),
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if excerpt.Note.Content != "bc" {
		t.Fatalf("content = %q, want bc", excerpt.Note.Content)
	}
	if excerpt.Offset != 3 || excerpt.EndOffset != 5 {
		t.Fatalf("window = %d..%d", excerpt.Offset, excerpt.EndOffset)
	}
	if excerpt.TotalChars != 8 {
		t.Fatalf("totalChars = %d, want 8", excerpt.TotalChars)
	}
	if excerpt.NextOffset == nil || *excerpt.NextOffset != 5 || !excerpt.HasMore {
		t.Fatalf("nextOffset = %v, hasMore = %v", excerpt.NextOffset, excerpt.HasMore)
	}
}

func TestReadNoteExcerptRejectsAnOffsetInsideASurrogatePair(t *testing.T) {
	note := testNote("a😀b")
	_, err := ReadNoteExcerpt(note, map[string]any{"offset": float64(2)})
	if err != ErrNoteSplit {
		t.Fatalf("err = %v, want ErrNoteSplit", err)
	}
}

func TestReadNoteExcerptNeverSplitsAPairAtTheEnd(t *testing.T) {
	note := testNote("a😀b")
	excerpt, err := ReadNoteExcerpt(note, map[string]any{"maxChars": float64(2)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if excerpt.Note.Content != "a" || excerpt.EndOffset != 1 {
		t.Fatalf("content = %q, end = %d", excerpt.Note.Content, excerpt.EndOffset)
	}
}

func TestReadNoteExcerptLiteralSearchKeepsMatchOffsets(t *testing.T) {
	note := testNote("alpha beta alpha")
	first, err := ReadNoteExcerpt(note, map[string]any{
		"query":    "alpha",
		"maxChars": float64(5),
	})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.MatchOffset == nil || *first.MatchOffset != 0 || first.Note.Content != "alpha" {
		t.Fatalf("first = %+v", first)
	}
	if first.NextOffset == nil || *first.NextOffset != 1 {
		t.Fatalf("nextOffset = %v, want 1 (past the match)", first.NextOffset)
	}

	second, err := ReadNoteExcerpt(note, map[string]any{
		"offset":   float64(*first.NextOffset),
		"query":    "alpha",
		"maxChars": float64(5),
	})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.MatchOffset == nil || *second.MatchOffset != 11 || second.Note.Content != "alpha" {
		t.Fatalf("second = %+v", second)
	}

	missing, err := ReadNoteExcerpt(note, map[string]any{"query": "absent"})
	if err != nil {
		t.Fatalf("missing read: %v", err)
	}
	if missing.MatchOffset == nil || *missing.MatchOffset != -1 {
		t.Fatalf("matchOffset = %v, want -1 (rendered as null)", missing.MatchOffset)
	}
	if missing.Note.Content != "" || missing.Offset != 16 || missing.HasMore {
		t.Fatalf("missing = %+v", missing)
	}
}

func TestReadNoteExcerptValidatesTheOriginalBounds(t *testing.T) {
	note := testNote("hello")
	cases := []struct {
		name   string
		params map[string]any
		want   error
	}{
		{"non-integer offset", map[string]any{"offset": 1.5}, ErrNoteOffset},
		{"past the end", map[string]any{"offset": float64(6)}, ErrNoteOffset},
		{"negative offset", map[string]any{"offset": float64(-1)}, ErrNoteOffset},
		{"maxChars too small", map[string]any{"maxChars": float64(1)}, ErrNoteMaxChars},
		{"maxChars not a number", map[string]any{"maxChars": "10"}, ErrNoteMaxChars},
		{"empty query", map[string]any{"query": ""}, ErrNoteQuery},
		{"long query", map[string]any{"query": strings.Repeat("q", MaxNoteQueryChars+1)}, ErrNoteQuery},
		{"changed note", map[string]any{"expectedUpdatedAt": "2020-01-01T00:00:00.000Z"}, ErrNoteChanged},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadNoteExcerpt(note, test.params); err != test.want {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
		})
	}

	if _, err := ReadNoteExcerpt(note, map[string]any{
		"expectedUpdatedAt": FormatVaultTime(note.UpdatedAt),
	}); err != nil {
		t.Fatalf("matching expectedUpdatedAt failed: %v", err)
	}
}

func TestReadNoteExcerptCapsTheExcerptAndTruncatesTheTitle(t *testing.T) {
	content := strings.Repeat("x", MaxNoteReadChars+500)
	title := strings.Repeat("é", 200)
	note := testNote(content)
	note.Title = title

	excerpt, err := ReadNoteExcerpt(note, map[string]any{"maxChars": float64(999999)})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(excerpt.Note.Content) != MaxNoteReadChars {
		t.Fatalf("content = %d chars, want %d", len(excerpt.Note.Content), MaxNoteReadChars)
	}
	if runes := []rune(excerpt.Note.Title); len(runes) != 120 {
		t.Fatalf("title = %d code points, want 120", len(runes))
	}
}

func TestFormatVaultTimeUsesJavaScriptMillisecondForm(t *testing.T) {
	value := time.Date(2026, 10, 25, 1, 2, 3, 456_000_000, time.UTC)
	if got := FormatVaultTime(value); got != "2026-10-25T01:02:03.456Z" {
		t.Fatalf("FormatVaultTime = %q", got)
	}
}
