package terminal

import (
	"regexp"
	"sort"
	"strings"
)

// KeywordHighlightRule is one configured highlight rule: a name, one or more
// regular expression patterns, and the color its matches are drawn in. It
// mirrors the original global rule shape; host-specific rules are added to the
// same list by the Settings window.
type KeywordHighlightRule struct {
	ID       string
	Patterns []string
	Color    string
	Enabled  bool
}

// Highlight limits keep a pathological rule or a huge write from stalling the
// terminal. They are several times larger than any original terminal line.
const (
	maxHighlightInput = 1 << 20
	maxHighlightColor = 64
)

// DefaultKeywordHighlightRules returns the original built-in rule catalog. The
// patterns are written for RE2 (the Go regular expression engine): the original
// JavaScript lookarounds are replaced by equivalent consume-free forms so the
// shipped defaults compile here instead of being silently ignored.
func DefaultKeywordHighlightRules() []KeywordHighlightRule {
	return []KeywordHighlightRule{
		{
			ID: "error", Color: "#F87171", Enabled: true,
			Patterns: []string{`\[error\]`, `\[err\]`, `\berror\b`, `\bfail(ed)?\b`, `\bfatal\b`, `\bcritical\b`, `\bexception\b`},
		},
		{
			ID: "warning", Color: "#FBBF24", Enabled: true,
			Patterns: []string{`\[warn(ing)?\]`, `\bwarn(ing)?\b`, `\bcaution\b`, `\bdeprecated\b`},
		},
		{
			ID: "ok", Color: "#34D399", Enabled: true,
			Patterns: []string{`\[ok\]`, `\bok\b`, `\bsuccess(ful)?\b`, `\bpassed\b`, `\bcompleted\b`, `\bdone\b`},
		},
		{
			ID: "info", Color: "#3B82F6", Enabled: true,
			Patterns: []string{`\[info\]`, `\[notice\]`, `\[note\]`, `\bnotice\b`, `\bnote\b`},
		},
		{
			ID: "debug", Color: "#A78BFA", Enabled: true,
			Patterns: []string{`\[debug\]`, `\[trace\]`, `\[verbose\]`, `\bdebug\b`, `\btrace\b`, `\bverbose\b`},
		},
		{
			ID: "ip-mac", Color: "#EC4899", Enabled: true,
			Patterns: []string{
				`\bhttps?://[^\s<>"'` + "`" + `]+`,
				`\bwww\.[^\s<>"'` + "`" + `]+`,
				`\b(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(?:\.(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}\b`,
				`\b(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}\b`,
				`\b(?:[0-9A-Fa-f]{1,4}:){1,7}:(?:[0-9A-Fa-f]{1,4}:?){0,7}\b`,
				`\b(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}\b`,
			},
		},
		{
			ID: "unix-path", Color: "#A78BFA", Enabled: true,
			Patterns: []string{`(?:^|[\s"'])(?:/|~/)[^\s"']+`},
		},
	}
}

// NormalizeKeywordHighlightRules keeps usable rules and falls back to the
// built-in catalog when none survive. An invalid pattern stays in the rule so
// the user can correct it; the highlighter ignores it at compile time, as the
// original did.
func NormalizeKeywordHighlightRules(rules []KeywordHighlightRule) []KeywordHighlightRule {
	if len(rules) == 0 {
		return DefaultKeywordHighlightRules()
	}
	normalized := make([]KeywordHighlightRule, 0, len(rules))
	for _, rule := range rules {
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" || len(rule.Patterns) == 0 {
			continue
		}
		if _, _, _, ok := parseHighlightColor(rule.Color); !ok {
			continue
		}
		patterns := make([]string, 0, len(rule.Patterns))
		for _, pattern := range rule.Patterns {
			if strings.TrimSpace(pattern) != "" {
				patterns = append(patterns, pattern)
			}
		}
		if len(patterns) == 0 {
			continue
		}
		rule.Patterns = patterns
		normalized = append(normalized, rule)
	}
	if len(normalized) == 0 {
		return DefaultKeywordHighlightRules()
	}
	return normalized
}

// HighlightMatch is one accepted non-overlapping match.
type HighlightMatch struct {
	Start  int
	End    int
	Color  string
	RuleID string
}

// Text returns the matched substring of source.
func (m HighlightMatch) Text(source string) string {
	if m.Start < 0 || m.End > len(source) || m.Start > m.End {
		return ""
	}
	return source[m.Start:m.End]
}

type compiledHighlightPattern struct {
	expression *regexp.Regexp
	color      string
	ruleID     string
	priority   int
}

// Highlighter applies the enabled rules to terminal output. It is immutable
// and safe for concurrent use.
type Highlighter struct {
	patterns []compiledHighlightPattern
}

// NewHighlighter compiles the enabled rules. An invalid pattern is ignored, and
// so is a rule with an invalid color, matching the original display boundary.
func NewHighlighter(enabled bool, rules []KeywordHighlightRule) *Highlighter {
	highlighter := &Highlighter{}
	if !enabled {
		return highlighter
	}
	for priority, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if _, _, _, ok := parseHighlightColor(rule.Color); !ok {
			continue
		}
		for _, pattern := range rule.Patterns {
			expression, err := regexp.Compile("(?i)" + pattern)
			if err != nil {
				continue
			}
			highlighter.patterns = append(highlighter.patterns, compiledHighlightPattern{
				expression: expression,
				color:      rule.Color,
				ruleID:     rule.ID,
				priority:   priority,
			})
		}
	}
	return highlighter
}

// Matches returns the accepted matches in source order.
func (h *Highlighter) Matches(source string) []HighlightMatch {
	if h == nil || len(h.patterns) == 0 || source == "" || len(source) > maxHighlightInput {
		return nil
	}
	var found []HighlightMatch
	for _, pattern := range h.patterns {
		for _, location := range pattern.expression.FindAllStringIndex(source, -1) {
			if location[0] == location[1] {
				continue
			}
			found = append(found, HighlightMatch{
				Start:  location[0],
				End:    location[1],
				Color:  pattern.color,
				RuleID: pattern.ruleID,
			})
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.SliceStable(found, func(left, right int) bool {
		if found[left].Start != found[right].Start {
			return found[left].Start < found[right].Start
		}
		leftPriority := h.priorityOf(found[left].RuleID)
		rightPriority := h.priorityOf(found[right].RuleID)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return found[left].End > found[right].End
	})
	accepted := found[:0]
	for _, match := range found {
		if len(accepted) == 0 || match.Start >= accepted[len(accepted)-1].End {
			accepted = append(accepted, match)
		}
	}
	return accepted
}

func (h *Highlighter) priorityOf(ruleID string) int {
	for _, pattern := range h.patterns {
		if pattern.ruleID == ruleID {
			return pattern.priority
		}
	}
	return len(h.patterns)
}

// Apply adds a foreground SGR sequence around every match in the plain text
// runs of data. Escape sequences are copied byte for byte and are never
// searched, so a color cannot leak into or corrupt terminal control data.
// SGR 39 restores the terminal's default foreground afterwards.
func (h *Highlighter) Apply(data []byte) []byte {
	if h == nil || len(h.patterns) == 0 || len(data) == 0 || len(data) > maxHighlightInput {
		return data
	}
	var output []byte
	changed := false
	for index := 0; index < len(data); {
		if data[index] == 0x1b {
			end := escapeEnd(data, index)
			output = append(output, data[index:end]...)
			index = end
			continue
		}
		end := index
		for end < len(data) && data[end] != 0x1b {
			end++
		}
		run := string(data[index:end])
		matches := h.Matches(run)
		if len(matches) == 0 {
			output = append(output, data[index:end]...)
		} else {
			changed = true
			position := 0
			for _, match := range matches {
				output = append(output, run[position:match.Start]...)
				if red, green, blue, ok := parseHighlightColor(match.Color); ok {
					output = append(output, "\x1b[38;2;"...)
					output = append(output, itoa(red)...)
					output = append(output, ';')
					output = append(output, itoa(green)...)
					output = append(output, ';')
					output = append(output, itoa(blue)...)
					output = append(output, 'm')
				}
				output = append(output, run[match.Start:match.End]...)
				output = append(output, "\x1b[39m"...)
				position = match.End
			}
			output = append(output, run[position:]...)
		}
		index = end
	}
	if !changed {
		return data
	}
	return output
}

// escapeEnd returns the byte just past the escape sequence starting at start.
// An incomplete sequence at the end of the write is copied as-is; the terminal
// receives the same bytes it would have without highlighting.
func escapeEnd(data []byte, start int) int {
	if start+1 >= len(data) {
		return len(data)
	}
	switch data[start+1] {
	case ']':
		for index := start + 2; index < len(data); index++ {
			if data[index] == 0x07 || data[index] == 0x18 || data[index] == 0x1a {
				return index + 1
			}
			if data[index] == 0x1b && index+1 < len(data) && data[index+1] == '\\' {
				return index + 2
			}
		}
		return len(data)
	case '[':
		for index := start + 2; index < len(data); index++ {
			if data[index] >= 0x40 && data[index] <= 0x7e {
				return index + 1
			}
		}
		return len(data)
	default:
		return start + 2
	}
}

func parseHighlightColor(color string) (int, int, int, bool) {
	value := strings.TrimSpace(color)
	if len(value) == 0 || len(value) > maxHighlightColor || value[0] != '#' {
		return 0, 0, 0, false
	}
	value = value[1:]
	if len(value) == 3 {
		value = string([]byte{value[0], value[0], value[1], value[1], value[2], value[2]})
	}
	if len(value) != 6 {
		return 0, 0, 0, false
	}
	components := make([]int, 3)
	for index := 0; index < 3; index++ {
		high, ok := hexDigit(value[index*2])
		if !ok {
			return 0, 0, 0, false
		}
		low, ok := hexDigit(value[index*2+1])
		if !ok {
			return 0, 0, 0, false
		}
		components[index] = high*16 + low
	}
	return components[0], components[1], components[2], true
}

func hexDigit(value byte) (int, bool) {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0'), true
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10, true
	case value >= 'A' && value <= 'F':
		return int(value-'A') + 10, true
	}
	return 0, false
}

func itoa(value int) []byte {
	if value == 0 {
		return []byte{'0'}
	}
	var digits [3]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return digits[index:]
}
