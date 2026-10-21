package vaultimport

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var secureCRTField = regexp.MustCompile(`^[SDB]:"([^"]+)"=(.*)$`)

// parseSecureCRT parses SecureCRT's INI-like session files. A file may hold
// one session or several concatenated S:"Hostname" records.
func parseSecureCRT(text string, opts Options) Result {
	result := Result{Format: FormatSecureCRT}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	type session struct {
		row      int
		label    string
		hostname string
		username string
		port     int
		ssh1Port int
		ssh2Port int
		sshName  string
		protocol string
	}
	var sessions []session
	var current session
	current.row = 1

	flush := func() {
		if current.hostname != "" {
			if validHostname(current.hostname) {
				sessions = append(sessions, current)
			} else {
				result.Issues = append(result.Issues,
					issue(IssueInvalidHostname, current.row, "hostname", current.hostname))
			}
		} else if current.label != "" || current.username != "" || current.protocol != "" {
			result.Issues = append(result.Issues,
				issue(IssueMissingHostname, current.row, "hostname", current.label))
		}
		current = session{row: current.row}
	}

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") ||
			strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[") {
			continue
		}
		match := secureCRTField.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		key := strings.TrimSpace(match[1])
		value := strings.Trim(strings.TrimSpace(match[2]), `"`)
		switch key {
		case "Hostname":
			if current.hostname != "" {
				flush()
			}
			current.row = index + 1
			current.hostname = value
		case "Username":
			current.username = value
		case "Port":
			if port, ok := parseSecureCRTPort(value); ok {
				current.port = port
			}
		case "[SSH1] Port":
			if port, ok := parseSecureCRTPort(value); ok {
				current.ssh1Port = port
			}
		case "[SSH2] Port":
			if port, ok := parseSecureCRTPort(value); ok {
				current.ssh2Port = port
			}
		case "Protocol Name":
			normalized := strings.ToLower(strings.TrimSpace(value))
			current.protocol = normalizeProtocol(value)
			switch normalized {
			case "ssh1", "ssh-1":
				current.sshName = "ssh1"
			case "ssh2", "ssh-2":
				current.sshName = "ssh2"
			}
			if current.protocol == "" && current.sshName != "" {
				current.protocol = "ssh"
			}
		case "Session Name":
			current.label = value
		}
	}
	flush()

	// The file stem is user data, not display copy. When there is no usable
	// stem, fall back to the endpoint so the parser stays free of UI strings.
	fallbackLabel := secureCRTFileStem(opts.FileName)
	for index, item := range sessions {
		protocol := item.protocol
		if protocol == "" {
			protocol = "ssh"
		}
		if protocol != "ssh" && protocol != "telnet" {
			result.Issues = append(result.Issues, issue(
				IssueUnsupportedProtocol, item.row, "protocol", protocol))
			continue
		}
		port := item.port
		if protocol == "ssh" {
			switch item.sshName {
			case "ssh1":
				if item.ssh1Port != 0 {
					port = item.ssh1Port
				}
			case "ssh2":
				if item.ssh2Port != 0 {
					port = item.ssh2Port
				}
			default:
				if item.ssh2Port != 0 {
					port = item.ssh2Port
				} else if item.ssh1Port != 0 {
					port = item.ssh1Port
				}
			}
		}
		if port == 0 {
			port = defaultPort(protocol)
		}
		label := item.label
		if label == "" {
			label = fallbackLabel
			if len(sessions) > 1 && fallbackLabel != "" {
				label = fallbackLabel + " " + itoa(index+1)
			}
		}
		if label == "" {
			label = item.hostname
		}
		result.Records = append(result.Records, Record{
			Row: item.row, Source: "securecrt", Label: label,
			Hostname: item.hostname, Username: item.username,
			Port: port, Protocol: protocol,
		})
	}
	if len(result.Records) == 0 && !hasError(result.Issues) {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	return result
}

// secureCRTFileStem returns the final path element without its extension.
// SecureCRT exports may be imported from another OS, so both separator styles
// are accepted regardless of the host platform.
func secureCRTFileStem(name string) string {
	if index := strings.LastIndexAny(name, `\/`); index >= 0 {
		name = name[index+1:]
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// parseSecureCRTPort accepts SecureCRT's 8-hex-digit encoding and decimal
// ports.
func parseSecureCRTPort(raw string) (int, bool) {
	value := strings.Trim(strings.TrimSpace(raw), `"`)
	if value == "" {
		return 0, false
	}
	if len(value) == 8 {
		if number, err := strconv.ParseUint(value, 16, 32); err == nil {
			if number >= 1 && number <= 65535 {
				return int(number), true
			}
		}
	}
	return parsePort(value)
}
