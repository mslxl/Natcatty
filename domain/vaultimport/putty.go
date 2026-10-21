package vaultimport

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	puttySectionPattern = regexp.MustCompile(`(?i)^\[HKEY_(?:CURRENT_USER|LOCAL_MACHINE)\\Software\\SimonTatham\\PuTTY\\Sessions\\(.+)\]$`)
	puttySectionHeader  = regexp.MustCompile(`(?i)^\[(.+)\]$`)
	puttyKeyValue       = regexp.MustCompile(`^"([^"]+)"=(.*)$`)
	puttyDword          = regexp.MustCompile(`(?i)^dword:([0-9a-f]{8})$`)
)

// parsePuTTY parses a PuTTY registry export. A plain "session block" file
// with a [name] header and HostName=value lines is accepted too, which is
// what the registry is usually exported to.
func parsePuTTY(text string) Result {
	result := Result{Format: FormatPuTTY}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	var current *Record
	sawSection := false
	flush := func() {
		if current == nil {
			return
		}
		if current.Hostname == "" {
			result.Issues = append(result.Issues,
				issue(IssueMissingHostname, current.Row, "hostname", current.Label))
			current = nil
			return
		}
		result.Records = append(result.Records, *current)
		current = nil
	}

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") ||
			strings.HasPrefix(trimmed, "Windows Registry Editor") {
			continue
		}

		var name string
		header := false
		if match := puttySectionPattern.FindStringSubmatch(trimmed); match != nil {
			name, header = match[1], true
		} else if match := puttySectionHeader.FindStringSubmatch(trimmed); match != nil {
			name, header = match[1], true
			// A registry export also carries the PuTTY root key and settings
			// such as SshHostKeys. Only the Sessions subtree creates entries;
			// other HKEY sections are configuration, not sessions.
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(name)), "HKEY_") {
				flush()
				sawSection = true
				continue
			}
			// An unrelated INI section still ends the current session.
		}

		if header {
			flush()
			sawSection = true
			if name == "" {
				continue
			}
			current = &Record{
				Row: index + 1, Source: "putty", Label: decodePuttyName(name),
				Protocol: "ssh",
			}
			continue
		}
		if !sawSection {
			if current == nil {
				current = &Record{Row: index + 1, Source: "putty", Protocol: "ssh"}
			}
		}
		if current == nil {
			continue
		}

		key, value, ok := puttyField(trimmed)
		if !ok {
			continue
		}
		switch key {
		case "HostName":
			current.Hostname = value
		case "UserName":
			current.Username = value
		case "PortNumber":
			if port, ok := parsePuTTYPort(value); ok {
				current.Port = port
			}
		case "Protocol":
			if protocol := normalizeProtocol(value); protocol != "" {
				current.Protocol = protocol
			}
		}
	}
	flush()

	if len(result.Records) == 0 && !hasError(result.Issues) {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	for index := range result.Records {
		record := &result.Records[index]
		if record.Label == "" {
			record.Label = record.Hostname
		}
		if record.Port == 0 {
			record.Port = defaultPort(record.Protocol)
		}
	}
	return result
}

// puttyField reads one quoted registry value, or a bare key=value line.
func puttyField(line string) (key, value string, ok bool) {
	if match := puttyKeyValue.FindStringSubmatch(line); match != nil {
		return match[1], decodeRegString(match[2]), true
	}
	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || strings.HasPrefix(key, "[") {
		return "", "", false
	}
	return key, decodeRegString(value), true
}

func decodeRegString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		inner := trimmed[1 : len(trimmed)-1]
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		return inner
	}
	return trimmed
}

func parsePuTTYPort(raw string) (int, bool) {
	value := strings.TrimSpace(raw)
	if match := puttyDword.FindStringSubmatch(value); match != nil {
		number, err := strconv.ParseInt(match[1], 16, 32)
		if err != nil || number < 1 || number > 65535 {
			return 0, false
		}
		return int(number), true
	}
	return parsePort(value)
}

func decodePuttyName(raw string) string {
	value, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return value
}
