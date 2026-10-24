package vault

import (
	"errors"
	"math"
	"strings"
	"time"

	domainvault "github.com/mslxl/natcatty/domain/vault"
)

// MaxNoteReadChars is the original's default and hard cap for one note
// excerpt. It stays below the shared tool result fitter's string limit.
const MaxNoteReadChars = 6000

// MaxNoteQueryChars bounds a literal search in UTF-16 code units.
const MaxNoteQueryChars = 200

var (
	// ErrNoteOffset reports an offset outside the note or not an integer.
	ErrNoteOffset = errors.New("note read: invalid offset")
	// ErrNoteMaxChars reports a missing or too-small excerpt length.
	ErrNoteMaxChars = errors.New("note read: invalid maxChars")
	// ErrNoteQuery reports a search string that is empty or too long.
	ErrNoteQuery = errors.New("note read: invalid query")
	// ErrNoteChanged reports that expectedUpdatedAt no longer matches.
	ErrNoteChanged = errors.New("note read: note changed")
	// ErrNoteSplit reports an offset inside a surrogate pair.
	ErrNoteSplit = errors.New("note read: offset splits a code point")
)

// NoteBound is the metadata and excerpt the original returned for one bounded
// note read. The title is limited to 120 code points so optional metadata
// cannot defeat the body limit.
type NoteBound struct {
	ID        string
	Title     string
	Content   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NoteExcerpt is the result of one bounded note read.
type NoteExcerpt struct {
	Note        NoteBound
	Offset      int
	EndOffset   int
	TotalChars  int
	NextOffset  *int
	HasMore     bool
	QuerySent   bool
	MatchOffset *int // nil with QuerySent means the literal was not found.
}

// ReadNoteExcerpt implements the original's bounded, UTF-16-offset note read.
// params are the raw tool arguments: offset, maxChars, query, and
// expectedUpdatedAt. Bounds use UTF-16 code units because that is what the
// original client's string offsets meant.
func ReadNoteExcerpt(note domainvault.Note, params map[string]any) (NoteExcerpt, error) {
	content := note.Content
	total := UTF16Length(content)

	offset := 0
	if raw, present := params["offset"]; present {
		value, ok := jsonSafeInteger(raw)
		if !ok || value < 0 || value > total {
			return NoteExcerpt{}, ErrNoteOffset
		}
		offset = value
	}

	maxChars := MaxNoteReadChars
	if raw, present := params["maxChars"]; present {
		value, ok := jsonSafeInteger(raw)
		if !ok || value < 2 {
			return NoteExcerpt{}, ErrNoteMaxChars
		}
		maxChars = value
		if maxChars > MaxNoteReadChars {
			maxChars = MaxNoteReadChars
		}
	}

	query := ""
	querySent := false
	if raw, present := params["query"]; present {
		text, ok := raw.(string)
		if !ok || UTF16Length(text) == 0 || UTF16Length(text) > MaxNoteQueryChars {
			return NoteExcerpt{}, ErrNoteQuery
		}
		query = text
		querySent = true
	}

	if raw, present := params["expectedUpdatedAt"]; present {
		expected, ok := raw.(string)
		if !ok || expected != FormatVaultTime(note.UpdatedAt) {
			return NoteExcerpt{}, ErrNoteChanged
		}
	}

	offsetByte, splits := UTF16ByteIndex(content, offset)
	if splits {
		return NoteExcerpt{}, ErrNoteSplit
	}

	var matchOffset *int
	start := offset
	if querySent {
		found := -1
		if index := strings.Index(content[offsetByte:], query); index >= 0 {
			found = offset + UTF16Length(content[offsetByte:offsetByte+index])
		}
		matchOffset = &found
		if found < 0 {
			start = total
		} else {
			start = found
		}
		if start > 0 {
			if _, splitsStart := UTF16ByteIndex(content, start); splitsStart {
				start--
			}
		}
	}

	end := start + min(maxChars, MaxNoteReadChars)
	if end > total {
		end = total
	}
	if end > 0 {
		if _, splitsEnd := UTF16ByteIndex(content, end); splitsEnd {
			end--
		}
	}

	var nextOffset *int
	if querySent {
		searchFrom := 0
		if matchOffset != nil {
			searchFrom = *matchOffset + 1
		}
		if searchFrom > 0 {
			if _, splitsSearch := UTF16ByteIndex(content, searchFrom); splitsSearch {
				searchFrom++
			}
		}
		if matchOffset != nil && *matchOffset >= 0 && searchFrom < total {
			value := searchFrom
			nextOffset = &value
		}
	} else if end < total {
		value := end
		nextOffset = &value
	}

	startByte, _ := UTF16ByteIndex(content, start)
	endByte, _ := UTF16ByteIndex(content, end)
	return NoteExcerpt{
		Note: NoteBound{
			ID:        note.ID,
			Title:     truncateCodePoints(note.Title, 120),
			Content:   content[startByte:endByte],
			CreatedAt: note.CreatedAt,
			UpdatedAt: note.UpdatedAt,
		},
		Offset:      start,
		EndOffset:   end,
		TotalChars:  total,
		NextOffset:  nextOffset,
		HasMore:     nextOffset != nil,
		QuerySent:   querySent,
		MatchOffset: matchOffset,
	}, nil
}

// FormatVaultTime renders the timestamp form the external tools exchange. It
// uses millisecond precision, matching the original's JavaScript Date JSON.
func FormatVaultTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000Z")
}

// UTF16Length counts the UTF-16 code units in s, including two for every rune
// above the basic multilingual plane.
func UTF16Length(s string) int {
	units := 0
	for _, character := range s {
		if character > 0xffff {
			units += 2
		} else {
			units++
		}
	}
	return units
}

// UTF16ByteIndex maps a UTF-16 offset to a byte index. splits reports that the
// offset falls between the two halves of a surrogate pair, which the original
// rejects for a starting offset.
func UTF16ByteIndex(s string, offset int) (index int, splits bool) {
	units := 0
	for index, character := range s {
		if units == offset {
			return index, false
		}
		size := 1
		if character > 0xffff {
			size = 2
		}
		if units < offset && offset < units+size {
			return index, true
		}
		units += size
	}
	return len(s), false
}

// jsonSafeInteger accepts a JSON number that is a safe non-negative integer.
func jsonSafeInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed {
			return 0, false
		}
		if typed < 0 || typed > 1<<53-1 {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, typed >= 0
	default:
		return 0, false
	}
}

func truncateCodePoints(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
