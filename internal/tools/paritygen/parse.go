package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// row is one parity-matrix entry. The schema is fixed by rewrite spec §17.
type row struct {
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

	line int
}

type matrix struct {
	Rows []row
}

// parseMatrix reads the deliberately small YAML subset the matrix uses: a
// top-level sequence of rows, scalar fields, block string sequences, and
// bracketed inline string sequences. It is not a general YAML parser, so an
// unsupported construct fails with its line number instead of being guessed.
func parseMatrix(data []byte) (*matrix, error) {
	m := &matrix{}
	var current *row
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
			m.Rows = append(m.Rows, row{line: lineNo})
			current = &m.Rows[len(m.Rows)-1]
			activeList = nil
			key, value, ok := strings.Cut(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), ":")
			if !ok {
				return nil, fmt.Errorf("%s:%d: expected a field after \"- \"", matrixPath, lineNo)
			}
			if strings.TrimSpace(key) != "id" {
				return nil, fmt.Errorf("%s:%d: a row must start with id", matrixPath, lineNo)
			}
			v, err := parseScalar(value)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", matrixPath, lineNo, err)
			}
			current.ID = v
			continue
		}
		if indent == 0 {
			return nil, fmt.Errorf("%s:%d: expected a row starting with \"- \"", matrixPath, lineNo)
		}
		if current == nil {
			return nil, fmt.Errorf("%s:%d: field outside a row", matrixPath, lineNo)
		}
		if indent >= 4 && strings.HasPrefix(trimmed, "- ") {
			if activeList == nil {
				return nil, fmt.Errorf("%s:%d: list item without a list field", matrixPath, lineNo)
			}
			v, err := parseScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %v", matrixPath, lineNo, err)
			}
			*activeList = append(*activeList, v)
			continue
		}

		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected \"key: value\"", matrixPath, lineNo)
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
				return fmt.Errorf("%s:%d: %v", matrixPath, lineNo, err)
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
			return nil, fmt.Errorf("%s:%d: unknown field %q", matrixPath, lineNo, key)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", matrixPath, err)
	}
	if len(m.Rows) == 0 {
		return nil, fmt.Errorf("%s has no rows", matrixPath)
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

type limitation struct {
	id   string
	body string
}

func parseLimitations(data []byte) []limitation {
	var out []limitation
	var current *limitation
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
			current = &limitation{id: id}
			continue
		}
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "# ") {
			flush()
			continue
		}
		if current != nil {
			current.body += line + "\n"
		}
	}
	flush()
	return out
}
