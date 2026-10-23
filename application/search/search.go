// Package search owns the ranking rules shared by Vault search, the Quick
// Switcher, and the command palette. It depends only on the standard library,
// so a UI surface can call it without knowing where a record came from.
//
// The rules follow the original Electron search matcher: a query splits into
// whitespace-separated segments, every segment must match one weighted field,
// and a match found literally in a field outranks a match found only after
// punctuation and spaces are removed. A query that matches every segment
// literally outranks one that needs the compact form at all.
package search

import (
	"sort"
	"strings"
	"unicode"
)

// Kind identifies the family a record belongs to. The UI groups results by
// kind in a stable order and localizes the section heading.
type Kind string

const (
	KindHost           Kind = "host"
	KindHostGroup      Kind = "hostGroup"
	KindKey            Kind = "key"
	KindIdentity       Kind = "identity"
	KindSnippet        Kind = "snippet"
	KindSnippetPackage Kind = "snippetPackage"
	KindNote           Kind = "note"
	KindNoteGroup      Kind = "noteGroup"
	KindProxyProfile   Kind = "proxyProfile"
	KindKnowledge      Kind = "knowledge"
	KindSession        Kind = "session"
	KindCommand        Kind = "command"
)

// Score bonuses and weights are the original matcher's. A strict phase means
// every segment matched literally, so it must always outrank a loose match,
// however many fields the loose match touched.
const (
	strictBonus = 1_000_000
	looseBonus  = 100_000

	methodLiteral = 50
	methodCompact = 25
)

// Default field weights for a record that does not name its own.
const (
	WeightTitle    = 500
	WeightSubtitle = 280
	WeightKeyword  = 220
	WeightContent  = 160
	WeightLabel    = 500
	WeightHostname = 380
	WeightGroup    = 280
	WeightTag      = 220
	WeightUsername = 180
	WeightNote     = 160
)

// Field is one weighted piece of searchable text.
type Field struct {
	Text   string
	Weight int
}

// Record is one searchable item. The surface that owns navigation decides
// what ID means and how to open the record.
type Record struct {
	Kind     Kind
	ID       string
	Title    string
	Subtitle string
	// Group is a stable section token for a surface that groups inside one
	// kind, such as the command palette's categories. The view localizes it.
	Group string
	// Shortcut is the effective chord display, when the record has one.
	Shortcut string
	Fields   []Field
}

// Result pairs a matched record with its rank. A higher score is a better
// match.
type Result struct {
	Record Record
	Score  int
}

// Query is a prepared search query. Preparing once per keystroke avoids
// normalizing the same text for every record.
type Query struct {
	normalized string
	tokens     []string
	compact    string
	ipv4       bool
}

// Prepare normalizes query and splits it into segments.
func Prepare(query string) Query {
	normalized := Normalize(query)
	if normalized == "" {
		return Query{}
	}
	return Query{
		normalized: normalized,
		tokens:     strings.Fields(normalized),
		compact:    compact(normalized),
		ipv4:       looksLikeIPv4(normalized),
	}
}

// Empty reports that the query matches every record.
func (q Query) Empty() bool { return q.normalized == "" }

// Normalized returns the normalized query text.
func (q Query) Normalized() string { return q.normalized }

// Normalize lowercases text, folds the Unicode dash family onto '-', and
// trims surrounding space, as the original matcher did.
func Normalize(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Pd, r) || r == '−' || r == '﹘' ||
			r == '﹣' || r == '－' {
			b.WriteRune('-')
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// compact removes the separators the original matcher drops, so a query typed
// without spaces or dashes still finds a grouped or hyphenated value.
func compact(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if isSeparator(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isSeparator(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '-', '_', '/', '\\', '|', '.', ',', ';', ':',
		'!', '?', '(', ')', '[', ']', '{', '}', '<', '>', '《', '》', '、',
		'"', '\'', '`', '~', '·':
		return true
	}
	return unicode.IsSpace(r)
}

// looksLikeIPv4 reports a dotted numeric address. The original matched such a
// query only contiguously inside one field, so "10.6.1" cannot combine the
// "10" of a label with the "6.1" of a hostname.
func looksLikeIPv4(text string) bool {
	parts := strings.Split(text, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 3 {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// fields returns the weighted text to match, deriving sensible weights from
// Title, Subtitle, and Group when a caller supplied none.
func (r Record) fields() []Field {
	if len(r.Fields) > 0 {
		return r.Fields
	}
	out := make([]Field, 0, 3)
	if r.Title != "" {
		out = append(out, Field{Text: r.Title, Weight: WeightTitle})
	}
	if r.Subtitle != "" {
		out = append(out, Field{Text: r.Subtitle, Weight: WeightSubtitle})
	}
	if r.Group != "" {
		out = append(out, Field{Text: r.Group, Weight: WeightKeyword})
	}
	return out
}

// Match scores one record against a prepared query. It reports false when the
// record does not match. An empty query matches everything with score zero.
func Match(q Query, r Record) (int, bool) {
	if q.Empty() {
		return 0, true
	}
	fields := r.fields()
	if len(fields) == 0 {
		return 0, false
	}
	if q.ipv4 {
		return matchContiguous(q.normalized, fields)
	}

	total := 0
	strict := true
	for _, token := range q.tokens {
		best := -1
		bestStrict := false
		compactToken := compact(token)
		for _, field := range fields {
			text := Normalize(field.Text)
			if text == "" {
				continue
			}
			if strings.Contains(text, token) {
				if score := field.Weight + methodLiteral; score > best {
					best = score
					bestStrict = true
				}
				continue
			}
			if compactToken == "" {
				continue
			}
			if strings.Contains(compact(text), compactToken) {
				if score := field.Weight + methodCompact; score > best {
					best = score
					bestStrict = false
				}
			}
		}
		if best < 0 {
			return 0, false
		}
		total += best
		if !bestStrict {
			strict = false
		}
	}
	if strict {
		total += strictBonus
	} else {
		total += looseBonus
	}
	return total, true
}

func matchContiguous(query string, fields []Field) (int, bool) {
	best := -1
	strict := false
	for _, field := range fields {
		text := Normalize(field.Text)
		if strings.Contains(text, query) {
			if score := field.Weight + methodLiteral; score > best {
				best = score
				strict = true
			}
			continue
		}
		if compactQuery := compact(query); compactQuery != "" &&
			strings.Contains(compact(text), compactQuery) {
			if score := field.Weight + methodCompact; score > best {
				best = score
				strict = false
			}
		}
	}
	if best < 0 {
		return 0, false
	}
	if strict {
		return best + strictBonus, true
	}
	return best + looseBonus, true
}

// Rank returns every record matched by query, best first. An empty query
// keeps the input order, which lets a surface preselect a meaningful list.
// Ties break by title and then identity, so repeated frames never reorder.
func Rank(query string, records []Record) []Result {
	q := Prepare(query)
	results := make([]Result, 0, len(records))
	for _, record := range records {
		if score, ok := Match(q, record); ok {
			results = append(results, Result{Record: record, Score: score})
		}
	}
	if q.Empty() {
		return results
	}
	sort.SliceStable(results, func(i, j int) bool {
		left, right := results[i], results[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		leftTitle := strings.ToLower(left.Record.Title)
		rightTitle := strings.ToLower(right.Record.Title)
		if leftTitle != rightTitle {
			return leftTitle < rightTitle
		}
		if left.Record.Kind != right.Record.Kind {
			return left.Record.Kind < right.Record.Kind
		}
		return left.Record.ID < right.Record.ID
	})
	return results
}

// InKind returns the results of one kind, preserving their ranked order.
func InKind(results []Result, kind Kind) []Result {
	out := make([]Result, 0, len(results))
	for _, result := range results {
		if result.Record.Kind == kind {
			out = append(out, result)
		}
	}
	return out
}
