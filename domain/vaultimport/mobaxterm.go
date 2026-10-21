package vaultimport

import (
	"regexp"
	"strings"
)

var (
	mobaSection      = regexp.MustCompile(`^\[(.+)\]$`)
	mobaStandard     = regexp.MustCompile(`(?i)^(?:;\s*logout)?\s*#\d+(?:#|$)`)
	mobaBookmarkName = regexp.MustCompile(`(?i)^bookmarks(?:_\d+)?$`)
)

// parseMobaXterm parses MobaXterm.ini. Bookmarks groups, session folders, and
// saved passwords are all read; passwords are decrypted only when the file
// makes it possible, and a failure is a warning rather than a silent drop.
func parseMobaXterm(text string, opts Options) Result {
	result := Result{Format: FormatMobaXterm}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	type entry struct {
		section string
		key     string
		value   string
		row     int
	}
	var entries []entry
	sectionGroups := make(map[string]string)
	passwords := make(map[string]string)
	credentials := make(map[string]struct{ username, ciphertext string })
	misc := make(map[string]string)
	hasSesspass := false
	section := ""

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, ";") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if match := mobaSection.FindStringSubmatch(trimmed); match != nil {
			section = strings.TrimSpace(match[1])
			if strings.EqualFold(section, "SessPass") {
				hasSesspass = true
			}
			continue
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" {
			continue
		}
		isBookmark := mobaBookmarkName.MatchString(section)
		switch {
		case isBookmark && strings.EqualFold(key, "SubRep"):
			sectionGroups[section] = normalizeGroup(value)
		case isBookmark && strings.EqualFold(key, "ImgNum"):
			// Presentation metadata only.
		case strings.EqualFold(section, "Passwords") && value != "":
			passwords[key] = value
		case strings.EqualFold(section, "Credentials") && value != "":
			if colon := strings.IndexByte(value, ':'); colon > 0 {
				credentials[key] = struct {
					username   string
					ciphertext string
				}{value[:colon], value[colon+1:]}
			}
		case strings.EqualFold(section, "Misc"):
			misc[strings.ToLower(key)] = value
		default:
			entries = append(entries, entry{section: section, key: key, value: value, row: index + 1})
		}
	}

	for _, item := range entries {
		if !mobaCandidateSection(item.section) {
			continue
		}
		isBookmark := mobaBookmarkName.MatchString(item.section)
		group := ""
		label := ""
		keyParts := splitNonEmpty(strings.ReplaceAll(item.key, "\\", "/"), "/")
		if isBookmark {
			if sectionGroup, ok := sectionGroups[item.section]; ok {
				group = sectionGroup
				label = item.key
			} else if len(keyParts) > 1 {
				group = strings.Join(keyParts[:len(keyParts)-1], "/")
				label = keyParts[len(keyParts)-1]
			} else {
				label = item.key
			}
		} else if len(keyParts) > 1 {
			group = strings.Join(keyParts[:len(keyParts)-1], "/")
			label = keyParts[len(keyParts)-1]
		} else {
			label = item.key
		}
		if label == "" {
			label = item.value
		}

		record, issue := parseMobaSessionValue(item.value)
		if issue != nil {
			issue.Row = item.row
			issue.Value = label
			result.Issues = append(result.Issues, *issue)
			continue
		}
		record.Row = item.row
		record.Source = "mobaxterm"
		record.Label = label
		record.Group = group
		result.Records = append(result.Records, record)
	}

	attachMobaPasswords(&result, passwords, credentials, misc, hasSesspass, opts.MasterPassword)
	if len(result.Records) == 0 && !hasError(result.Issues) {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	return result
}

func mobaCandidateSection(section string) bool {
	switch {
	case strings.EqualFold(section, "Sessions"),
		strings.EqualFold(section, "Bookmark"),
		strings.EqualFold(section, "Bookmarks2"):
		return true
	default:
		return mobaBookmarkName.MatchString(section)
	}
}

func splitNonEmpty(value, separator string) []string {
	parts := strings.Split(value, separator)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseMobaSessionValue reads the "#109#0%host%port%user%..." session value.
// The older token-layout fallback keeps imports from hand-written files
// working.
func parseMobaSessionValue(value string) (Record, *Issue) {
	record := Record{Protocol: "ssh"}
	if mobaStandard.MatchString(value) {
		outer := strings.Split(value, "#")
		if len(outer) < 3 {
			return record, &Issue{Severity: SeverityWarning, Code: IssueUnsupportedEntry}
		}
		if outer[1] != "109" {
			return record, &Issue{
				Severity: SeverityWarning, Code: IssueUnsupportedProtocol, Field: "protocol",
				Detail: outer[1],
			}
		}
		fields := strings.Split(outer[2], "%")
		sessionType := strings.TrimSpace(fields[0])
		if sessionType != "0" && sessionType != "7" {
			return record, &Issue{
				Severity: SeverityWarning, Code: IssueUnsupportedProtocol, Field: "protocol",
				Detail: sessionType,
			}
		}
		if len(fields) < 2 || strings.TrimSpace(fields[1]) == "" {
			return record, &Issue{Severity: SeverityWarning, Code: IssueMissingHostname}
		}
		record.Hostname = strings.TrimSpace(fields[1])
		if len(fields) > 2 {
			if port, ok := parsePort(fields[2]); ok {
				record.Port = port
			}
		}
		if len(fields) > 3 {
			user := strings.TrimSpace(fields[3])
			if user != "" && !strings.EqualFold(user, "<default>") {
				record.Username = user
			}
		}
		if record.Port == 0 {
			record.Port = 22
		}
		return record, nil
	}

	tokens := splitNonEmpty(value, "#")
	protocol := ""
	for _, token := range tokens {
		if value := normalizeProtocol(strings.TrimPrefix(token, "ssh:")); value != "" {
			protocol = value
			break
		}
	}
	for _, token := range tokens {
		hostname, username, port, targetProtocol, ok := parseTarget(
			strings.TrimPrefix(token, "ssh:"))
		if !ok {
			continue
		}
		record.Hostname = hostname
		record.Username = username
		record.Port = port
		if targetProtocol != "" {
			protocol = targetProtocol
		}
		break
	}
	if protocol != "" {
		record.Protocol = protocol
	}
	if record.Port == 0 {
		for _, token := range tokens {
			if port, ok := parsePort(token); ok {
				record.Port = port
				break
			}
		}
	}
	if record.Username == "" {
		for _, token := range tokens {
			if at := strings.IndexByte(token, '@'); at > 0 {
				record.Username = token[:at]
				break
			}
		}
	}
	if record.Hostname == "" {
		return record, &Issue{Severity: SeverityWarning, Code: IssueMissingHostname}
	}
	if record.Port == 0 {
		record.Port = defaultPort(record.Protocol)
	}
	return record, nil
}

// attachMobaPasswords decrypts every credential the file carries and matches
// it to an imported session. The matching rules mirror the original: exact
// user@host keys first, then one unambiguous hostname or username match.
func attachMobaPasswords(result *Result, passwords map[string]string,
	credentials map[string]struct{ username, ciphertext string },
	misc map[string]string, hasSesspass bool, master string) {

	hasCiphertext := len(passwords) > 0 || len(credentials) > 0
	if misc["passwordsinregistry"] == "1" && !hasCiphertext {
		result.Issues = append(result.Issues, issue(IssuePasswordsInRegistry, 0, "", ""))
		return
	}
	if !hasCiphertext {
		return
	}
	if master == "" && hasSesspass && misc["sessionp"] == "" {
		result.Issues = append(result.Issues, issue(IssueMasterPassword, 0, "", ""))
		return
	}

	sessionP := misc["sessionp"]
	sysUsername := misc["mpsetaccount"]
	sysHostname := misc["mpsetcomputer"]

	decrypted := make(map[string]string)
	failed := 0
	for key, ciphertext := range passwords {
		user, host := parseMobaPasswordKey(key)
		plaintext, ok := decryptMobaStoredSecret(mobaSecretInput{
			ciphertext: ciphertext, master: master, sessionP: sessionP,
			sysUsername: sysUsername, sysHostname: sysHostname,
			connUsername: user, connHostname: host,
		})
		if ok {
			decrypted[key] = plaintext
		} else {
			failed++
		}
	}
	byUser := make(map[string][]string)
	for _, credential := range credentials {
		plaintext, ok := decryptMobaStoredSecret(mobaSecretInput{
			ciphertext: credential.ciphertext, master: master, sessionP: sessionP,
		})
		if !ok {
			failed++
			continue
		}
		byUser[credential.username] = append(byUser[credential.username], plaintext)
	}

	if master != "" && failed > 0 {
		result.Issues = append(result.Issues, issue(IssuePasswordUndecrypt, 0, "", ""))
		return
	}
	if failed > 0 && len(decrypted) == 0 && len(byUser) == 0 {
		result.Issues = append(result.Issues, issue(IssuePasswordUndecrypt, 0, "", ""))
		return
	}
	if failed > 0 {
		result.Issues = append(result.Issues, issue(IssuePasswordUndecrypt, 0, "", ""))
	}

	attached := 0
	for index := range result.Records {
		record := &result.Records[index]
		password, ok := lookupMobaPassword(*record, decrypted, byUser)
		if !ok {
			continue
		}
		record.Password = password
		attached++
	}
	if attached == 0 {
		result.Issues = append(result.Issues, issue(IssuePasswordUnmatched, 0, "", ""))
	}
}

func parseMobaPasswordKey(raw string) (username, hostname string) {
	stripped := raw
	if colon := strings.IndexByte(stripped, ':'); colon > 0 {
		candidate := stripped[:colon]
		valid := true
		for _, r := range candidate {
			if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
				valid = false
				break
			}
		}
		if valid {
			stripped = stripped[colon+1:]
		}
	}
	at := strings.LastIndexByte(stripped, '@')
	if at <= 0 || at == len(stripped)-1 {
		return "", ""
	}
	return stripped[:at], stripped[at+1:]
}

func lookupMobaPassword(record Record, passwords map[string]string,
	byUser map[string][]string) (string, bool) {

	username := strings.TrimSpace(record.Username)
	hostname := record.Hostname
	port := record.Port
	if port == 0 {
		port = 22
	}
	if username != "" {
		for _, key := range []string{
			username + "@" + hostname,
			"ssh" + itoa(port) + ":" + username + "@" + hostname,
			"ssh:" + username + "@" + hostname,
		} {
			if value, ok := passwords[key]; ok && value != "" {
				return value, true
			}
		}
	}

	var matches []string
	for key, value := range passwords {
		user, host := parseMobaPasswordKey(key)
		if !sameHost(host, hostname) {
			continue
		}
		if username != "" && user != "" && user != username {
			continue
		}
		if username != "" && user == username {
			return value, true
		}
		matches = append(matches, value)
	}
	if username == "" && len(matches) == 1 {
		return matches[0], true
	}
	if username != "" {
		if values := byUser[username]; len(values) == 1 {
			return values[0], true
		}
	}
	return "", false
}

// sameHost compares hostnames case-insensitively, as the original's accent-
// insensitive comparison does for ASCII hostnames.
func sameHost(left, right string) bool {
	return left != "" && right != "" && strings.EqualFold(left, right)
}
