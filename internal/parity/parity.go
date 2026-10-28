// Package parity parses the editable parity matrix and its limitations file.
// The generator in internal/tools/paritygen and the ticket 60 release self-test
// share this parser so both read the same definition of done.
package parity

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

const (
	// MatrixPath is the committed parity matrix.
	MatrixPath = "docs/parity/matrix.yaml"
	// LimitationsPath is the committed accepted-limitations file.
	LimitationsPath = "docs/parity/limitations.md"
)

// Row is one parity-matrix entry. The schema is fixed by rewrite spec §17.
type Row struct {
	ID                  string
	Area                string
	Feature             string
	OriginalPaths       []string
	Module              string
	Tickets             []string
	AcceptanceTests     []string
	ManualQA            []string
	AutomationException string
	Status              string
	Notes               string

	// Line is the matrix line the row starts on, for diagnostics.
	Line int
}

// Matrix is the parsed matrix.
type Matrix struct {
	Rows []Row
}

// ParseMatrix reads the deliberately small YAML subset the matrix uses: a
// top-level sequence of rows, scalar fields, block string sequences, and
// bracketed inline string sequences. It is not a general YAML parser, so an
// unsupported construct fails with its line number instead of being guessed.
func ParseMatrix(data []byte) (*Matrix, error) {
	m := &Matrix{}
	var current *Row
	var activeList *[]string

	scanner := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))

		if indent == 0 && strings.HasPrefix(trimmed, "- ") {
			m.Rows = append(m.Rows, Row{Line: lineNo})
			current = &m.Rows[len(m.Rows)-1]
			activeList = nil
			key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), ":")
			if !ok {
				return nil, fmt.Errorf("%s:%d: expected a field after \"- \"", MatrixPath, lineNo)
			}
			if strings.TrimSpace(key) != "id" {
				return nil, fmt.Errorf("%s:%d: a row must start with id", MatrixPath, lineNo)
			}
			v, err := parseScalar(value)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", MatrixPath, lineNo, err)
			}
			current.ID = v
			continue
		}
		if indent == 0 {
			return nil, fmt.Errorf("%s:%d: expected a row starting with \"- \"", MatrixPath, lineNo)
		}
		if current == nil {
			return nil, fmt.Errorf("%s:%d: field outside a row", MatrixPath, lineNo)
		}
		if indent >= 4 && strings.HasPrefix(trimmed, "- ") {
			if activeList == nil {
				return nil, fmt.Errorf("%s:%d: list item without a list field", MatrixPath, lineNo)
			}
			v, err := parseScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", MatrixPath, lineNo, err)
			}
			*activeList = append(*activeList, v)
			continue
		}

		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected \"key: value\"", MatrixPath, lineNo)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		activeList = nil

		stringList := func(target *[]string) error {
			if value == "" {
				activeList = target
				return nil
			}
			items, err := parseInlineList(value)
			if err != nil {
				return fmt.Errorf("%s:%d: %v", MatrixPath, lineNo, err)
			}
			*target = items
			return nil
		}

		var err error
		switch key {
		case "id":
			current.ID, err = parseScalar(value)
		case "area":
			current.Area, err = parseScalar(value)
		case "feature":
			current.Feature, err = parseScalar(value)
		case "module":
			current.Module, err = parseScalar(value)
		case "status":
			current.Status, err = parseScalar(value)
		case "notes":
			current.Notes, err = parseScalar(value)
		case "automation_exception":
			current.AutomationException, err = parseScalar(value)
		case "original_paths":
			err = stringList(&current.OriginalPaths)
		case "tickets":
			err = stringList(&current.Tickets)
		case "acceptance_tests":
			err = stringList(&current.AcceptanceTests)
		case "manual_qa":
			err = stringList(&current.ManualQA)
		default:
			return nil, fmt.Errorf("%s:%d: unknown field %q", MatrixPath, lineNo, key)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", MatrixPath, err)
	}
	if len(m.Rows) == 0 {
		return nil, fmt.Errorf("%s has no rows", MatrixPath)
	}
	return m, nil
}

func parseScalar(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	switch value[0] {
	case '"':
		out, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("invalid quoted value %s: %w", value, err)
		}
		return out, nil
	case '\'':
		if len(value) < 2 || value[len(value)-1] != '\'' {
			return "", fmt.Errorf("invalid quoted value %s", value)
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	default:
		return value, nil
	}
}

func parseInlineList(value string) ([]string, error) {
	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("expected an inline list like [a, b]")
	}
	inner := strings.TrimSpace(value[1 : len(value)-1])
	if inner == "" {
		return nil, nil
	}
	var parts []string
	var field strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		part := strings.TrimSpace(field.String())
		if part != "" {
			parts = append(parts, part)
		}
		field.Reset()
	}
	for _, r := range inner {
		switch {
		case escaped:
			field.WriteRune(r)
			escaped = false
		case r == '\\' && quote == '"':
			field.WriteRune(r)
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
			field.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
			field.WriteRune(r)
		case r == ',':
			flush()
		default:
			field.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted value in %s", value)
	}
	flush()

	out := make([]string, 0, len(parts))
	for _, part := range parts {
		scalar, err := parseScalar(part)
		if err != nil {
			return nil, err
		}
		out = append(out, scalar)
	}
	return out, nil
}

// Limitation is one accepted-limitation section of limitations.md.
type Limitation struct {
	ID   string
	Body string
}

// Audit reports the release-readiness problems the parity gate and the ticket
// 60 self-test share: every non-excluded row must be verified and name an
// acceptance test, and every excluded row must have a signed limitation entry
// that names an excluded row and carries Decision and Ticket lines.
func Audit(m *Matrix, limitations []Limitation) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	limitationByID := map[string]Limitation{}
	for _, entry := range limitations {
		if _, ok := limitationByID[entry.ID]; ok {
			add("limitations.md defines %q more than once", entry.ID)
			continue
		}
		limitationByID[entry.ID] = entry
	}

	for _, row := range m.Rows {
		switch row.Status {
		case "verified":
			if len(row.AcceptanceTests) == 0 {
				add("row %q is verified but names no acceptance test", row.ID)
			}
		case "excluded":
			limitation, ok := limitationByID[row.ID]
			if !ok {
				add("excluded row %q has no entry in %s", row.ID, LimitationsPath)
				continue
			}
			if !limitationHasLine(limitation.Body, "Decision:") {
				add("excluded row %q has no \"Decision:\" line in %s", row.ID, LimitationsPath)
			}
			if !limitationHasLine(limitation.Body, "Ticket:") {
				add("excluded row %q has no \"Ticket:\" sign-off line in %s", row.ID, LimitationsPath)
			}
		default:
			add("row %q is %s; release requires every non-excluded row to be verified", row.ID, row.Status)
		}
	}

	for _, limitation := range limitations {
		row := FindByID(m.Rows, limitation.ID)
		if row == nil {
			add("limitations.md has an entry for %q, but no matrix row uses that id", limitation.ID)
			continue
		}
		if row.Status != "excluded" {
			add("limitations.md has an entry for %q, but the row is not excluded", limitation.ID)
		}
	}
	return problems
}

// FindByID returns the row with id, or nil.
func FindByID(rows []Row, id string) *Row {
	for index := range rows {
		if rows[index].ID == id {
			return &rows[index]
		}
	}
	return nil
}

func limitationHasLine(body, prefix string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

// ParseLimitations reads the "### `id`" sections that document excluded rows.
func ParseLimitations(data []byte) []Limitation {
	var out []Limitation
	var current *Limitation
	flush := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "### ") {
			flush()
			id := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "### ")), "`")
			current = &Limitation{ID: id}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ") {
			flush()
			continue
		}
		if current != nil {
			current.Body += line + "\n"
		}
	}
	flush()
	return out
}
