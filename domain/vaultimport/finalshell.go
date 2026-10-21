package vaultimport

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseFinalShell parses one FinalShell connection document. FinalShell
// writes a typo'd "conection_type" field; type 100 is an SSH connection.
// Arrays and objects that contain nested connections are walked, so an
// exported folder works as well as a single session file.
func parseFinalShell(text string) Result {
	result := Result{Format: FormatFinalShell}
	var root any
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	if err := decoder.Decode(&root); err != nil {
		result.Issues = append(result.Issues, errorIssue(IssueInvalidJSON, 0, err.Error()))
		return result
	}

	var objects []map[string]any
	collectFinalShellObjects(root, &objects, 0)
	if len(objects) == 0 {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
		return result
	}

	for index, object := range objects {
		row := index + 1
		connectionType := numberField(object["conection_type"])
		if connectionType != 100 {
			result.Issues = append(result.Issues, issue(
				IssueUnsupportedProtocol, row, "protocol",
				strconv.FormatFloat(connectionType, 'f', -1, 64)))
			continue
		}
		hostname := strings.TrimSpace(stringField(object["host"]))
		if hostname == "" {
			result.Issues = append(result.Issues,
				issue(IssueMissingHostname, row, "hostname", ""))
			continue
		}
		record := Record{
			Row: row, Source: "finalshell", Hostname: hostname,
			Username: strings.TrimSpace(stringField(object["user_name"])),
			Label:    strings.TrimSpace(stringField(object["name"])),
			Notes:    strings.TrimSpace(stringField(object["description"])),
			Protocol: "ssh",
		}
		switch value := object["port"].(type) {
		case float64:
			if value >= 1 && value <= 65535 {
				record.Port = int(value)
			}
		case string:
			if port, ok := parsePort(value); ok {
				record.Port = port
			}
		}
		if record.Port == 0 {
			record.Port = 22
		}
		if encoded := strings.TrimSpace(stringField(object["password"])); encoded != "" {
			if password, ok := decodeFinalShellPassword(encoded); ok {
				record.Password = password
			} else {
				result.Issues = append(result.Issues,
					issue(IssuePasswordUndecrypt, row, "password", ""))
			}
		}
		if record.Label == "" {
			record.Label = record.Hostname
		}
		result.Records = append(result.Records, record)
	}
	return result
}

// collectFinalShellObjects walks a decoded JSON value and records every map
// with a host field. It bounds recursion so a malicious deeply nested file
// cannot exhaust the stack.
func collectFinalShellObjects(value any, out *[]map[string]any, depth int) {
	if depth > 64 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		if _, ok := typed["host"]; ok {
			*out = append(*out, typed)
		}
		for _, child := range typed {
			collectFinalShellObjects(child, out, depth+1)
		}
	case []any:
		for _, child := range typed {
			collectFinalShellObjects(child, out, depth+1)
		}
	}
}

func stringField(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func numberField(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case string:
		number, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return number
	default:
		return -1
	}
}
