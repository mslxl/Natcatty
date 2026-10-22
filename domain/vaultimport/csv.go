package vaultimport

import (
	"encoding/csv"
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// CSV marked-field prefixes. They let a cell carry a value the legacy CSV
// format cannot represent directly (a key path, passphrase, proxy, or local
// shell spec) without confusing spreadsheets. A value that begins with a
// formula character or with the marker itself is percent-encoded behind the
// marker.
const (
	MarkerKeyPath    = "__netcatty_csv_keypath_v1__:"
	MarkerPassphrase = "__netcatty_csv_passphrase_v1__:"
	MarkerProxy      = "__netcatty_csv_proxy_v1__:"
	MarkerLocalShell = "__netcatty_csv_localshell_v1__:"
)

// csvHeader is the canonical Netcatty CSV header. Import accepts variants and
// additional columns; export writes this exact order.
var csvHeader = []string{
	"Groups", "Label", "Tags", "Notes", "Hostname/IP", "Protocol",
	"Port", "Username", "Password", "KeyPath", "Passphrase", "Proxy", "LocalShell",
}

// CSVHeader returns a copy of the canonical header.
func CSVHeader() []string { return append([]string(nil), csvHeader...) }

// ParseCSV splits RFC 4180 text into rows. It accepts a UTF-8 BOM, CRLF, LF,
// and bare-CR line endings, double-quoted fields with embedded commas,
// newlines, and doubled quotes. A malformed quote is reported with its line
// instead of being silently repaired.
func ParseCSV(text string) ([][]string, []Issue) {
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	if strings.TrimSpace(text) == "" {
		return nil, []Issue{errorIssue(IssueCSVEmpty, 0, "")}
	}

	// Go's CSV reader normalizes every CRLF to LF, including one inside a
	// quoted field. RFC 4180 makes that CRLF part of the value, so protect it
	// with a sentinel and restore it after parsing. Row endings outside quotes
	// still normalize, which keeps line numbers stable for issues.
	sentinel := "\x00natcatty-quoted-cr\x00"
	for strings.Contains(text, sentinel) {
		sentinel += "x"
	}
	text = protectQuotedCR(text, sentinel)

	delimiter := ','
	firstLine := text
	if index := strings.IndexByte(firstLine, '\n'); index >= 0 {
		firstLine = firstLine[:index]
	}
	if !strings.Contains(firstLine, ",") && strings.Contains(firstLine, "\t") {
		delimiter = '\t'
	}

	reader := csv.NewReader(strings.NewReader(text))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = false
	reader.TrimLeadingSpace = false
	records, err := reader.ReadAll()
	if err != nil {
		var parseErr *csv.ParseError
		row := 0
		if errors.As(err, &parseErr) {
			row = parseErr.Line
		}
		return restoreQuotedCR(records, sentinel), []Issue{errorIssue(IssueCSVQuoting, row, err.Error())}
	}
	return restoreQuotedCR(records, sentinel), nil
}

// protectQuotedCR replaces carriage returns inside quoted fields with sentinel
// while normalizing row endings outside them. Newlines inside quotes stay, so
// the reader still counts physical lines for its error positions.
func protectQuotedCR(text, sentinel string) string {
	var out strings.Builder
	out.Grow(len(text))
	inQuotes := false
	for index := 0; index < len(text); index++ {
		character := text[index]
		switch character {
		case '"':
			if inQuotes && index+1 < len(text) && text[index+1] == '"' {
				out.WriteString(`""`)
				index++
				continue
			}
			inQuotes = !inQuotes
			out.WriteByte(character)
		case '\r':
			if inQuotes {
				out.WriteString(sentinel)
				if index+1 < len(text) && text[index+1] == '\n' {
					out.WriteByte('\n')
					index++
				}
				continue
			}
			if index+1 < len(text) && text[index+1] == '\n' {
				index++
			}
			out.WriteByte('\n')
		default:
			out.WriteByte(character)
		}
	}
	return out.String()
}

func restoreQuotedCR(records [][]string, sentinel string) [][]string {
	for row := range records {
		for field := range records[row] {
			if strings.Contains(records[row][field], sentinel) {
				records[row][field] = strings.ReplaceAll(records[row][field], sentinel, "\r")
			}
		}
	}
	return records
}

// EscapeComponent implements encodeURIComponent's escape set so marked values
// keep their original cross-app representation.
func EscapeComponent(value string) string {
	const hex = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		switch {
		case b >= 'A' && b <= 'Z', b >= 'a' && b <= 'z', b >= '0' && b <= '9':
			out.WriteByte(b)
		case strings.ContainsRune("-_.!~*'()", rune(b)):
			out.WriteByte(b)
		default:
			out.WriteByte('%')
			out.WriteByte(hex[b>>4])
			out.WriteByte(hex[b&0x0f])
		}
	}
	return out.String()
}

// EncodeMarkedField protects a marked value. A leading formula character or a
// value that already looks like the marker is percent-encoded, so the marker
// stays unambiguous and no spreadsheet evaluates the cell.
func EncodeMarkedField(value, marker string) string {
	if value == "" {
		return ""
	}
	if isFormulaStart(value) || strings.HasPrefix(value, marker) {
		return marker + EscapeComponent(value)
	}
	return value
}

// DecodeMarkedField reverses EncodeMarkedField. An undecodable value is
// returned unchanged rather than dropped.
func DecodeMarkedField(value, marker string) string {
	if !strings.HasPrefix(value, marker) {
		return value
	}
	decoded, err := url.PathUnescape(value[len(marker):])
	if err != nil {
		return value
	}
	return decoded
}

// isFormulaStart reports whether a spreadsheet could read the value as a
// formula. The tab and carriage return cases match the original exporter.
func isFormulaStart(value string) bool {
	if value == "" {
		return false
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return true
	default:
		return false
	}
}

// NeutralizeFormula prefixes one apostrophe to a cell a spreadsheet could
// read as a formula, the same guard the original CSV exporter uses. It never
// evaluates or rewrites the value.
func NeutralizeFormula(value string) string {
	if isFormulaStart(value) {
		return "'" + value
	}
	return value
}

// StripFormulaGuard removes the exporter's protective apostrophe, so an
// exported value round-trips unchanged. It removes at most one apostrophe and
// only when a formula character follows.
func StripFormulaGuard(value string) string {
	if len(value) >= 2 && value[0] == '\'' && isFormulaStart(value[1:]) {
		return value[1:]
	}
	return value
}

// EscapeCSVField quotes one field per RFC 4180. When guard is true a leading
// formula character is neutralized first. Password-like fields pass false so
// credentials stay byte-exact.
func EscapeCSVField(value string, guard bool) string {
	if guard {
		value = NeutralizeFormula(value)
	}
	if strings.ContainsAny(value, "\",\r\n") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

// normalizeHeaderKey reduces a header cell to lowercase alphanumerics, so
// "Host name", "HOST-NAME", and "hostname" match.
func normalizeHeaderKey(raw string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// findHeader returns the first column whose normalized name is one of the
// candidates or starts with one, matching the original importer's tolerance
// for decorated headings.
func findHeader(headers []string, candidates ...string) int {
	normalized := make([]string, len(headers))
	for i, header := range headers {
		normalized[i] = normalizeHeaderKey(header)
	}
	for _, candidate := range candidates {
		key := normalizeHeaderKey(candidate)
		for i, header := range normalized {
			if header == key || strings.HasPrefix(header, key) {
				return i
			}
		}
	}
	return -1
}

// findExactHeader returns the first column whose normalized name is exactly
// one of the candidates.
func findExactHeader(headers []string, candidates ...string) int {
	wanted := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		wanted[normalizeHeaderKey(candidate)] = true
	}
	for i, header := range headers {
		if wanted[normalizeHeaderKey(header)] {
			return i
		}
	}
	return -1
}

func cell(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return row[index]
}

// rowHasContent distinguishes a meaningful data row from a blank line or a
// row of empty cells, which must not become an invalid-row warning.
func rowHasContent(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// splitTags splits a tag cell on the separators the original accepted,
// including the full-width comma.
func splitTags(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '，'
	})
	tags := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			tags = append(tags, field)
		}
	}
	return tags
}

// parseTarget reads the hostname cell. It accepts a bare host, user@host,
// host:port, [IPv6]:port, and ssh://, telnet://, or ssh://user@host:port
// URLs. A recognized source protocol or username fills only the fields the
// dedicated columns left empty.
func parseTarget(raw string) (hostname, username string, port int, protocol string, ok bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", 0, "", false
	}

	if strings.Contains(trimmed, "://") {
		parsed, err := url.Parse(trimmed)
		if err != nil || parsed.Hostname() == "" {
			return "", "", 0, "", false
		}
		host := parsed.Hostname()
		if !validHostname(host) {
			return "", "", 0, "", false
		}
		value := 0
		if parsed.Port() != "" {
			var portOK bool
			value, portOK = parsePort(parsed.Port())
			if !portOK {
				return "", "", 0, "", false
			}
		}
		return host, parsed.User.Username(), value,
			normalizeProtocol(strings.TrimSuffix(parsed.Scheme, ":")), true
	}

	rest := trimmed
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		username = rest[:at]
		rest = rest[at+1:]
	}
	if strings.HasPrefix(rest, "[") {
		end := strings.IndexByte(rest, ']')
		if end < 0 {
			return "", "", 0, "", false
		}
		hostname = rest[1:end]
		if tail := rest[end+1:]; tail != "" {
			if !strings.HasPrefix(tail, ":") {
				return "", "", 0, "", false
			}
			value, portOK := parsePort(tail[1:])
			if !portOK {
				return "", "", 0, "", false
			}
			port = value
		}
	} else {
		hostname = rest
		if colon := strings.LastIndexByte(rest, ':'); colon >= 0 {
			if value, portOK := parsePort(rest[colon+1:]); portOK {
				hostname = rest[:colon]
				port = value
			}
		}
	}
	if !validHostname(hostname) {
		return "", "", 0, "", false
	}
	return hostname, username, port, "", true
}

// validHostname rejects empty, whitespace-containing, and control-character
// hostnames before they reach the Vault validator.
func validHostname(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// parseCSV parses Netcatty CSV. Rows without a hostname are ignored; a row
// that must be skipped reports its source row number.
func parseCSV(text string) Result {
	result := Result{Format: FormatCSV}
	rows, parseIssues := ParseCSV(text)
	result.Issues = append(result.Issues, parseIssues...)
	if len(rows) == 0 {
		if len(result.Issues) == 0 {
			result.Issues = append(result.Issues, errorIssue(IssueCSVEmpty, 0, ""))
		}
		return result
	}

	headers := rows[0]
	hostnameIdx := findHeader(headers, "hostname", "host", "server")
	if hostnameIdx < 0 {
		result.Issues = append(result.Issues, errorIssue(IssueCSVHeader, 1, ""))
		return result
	}
	groupsIdx := findHeader(headers, "groups", "group", "folder", "path")
	labelIdx := findHeader(headers, "label", "name")
	tagsIdx := findHeader(headers, "tags", "tag")
	notesIdx := findHeader(headers, "notes", "note", "remark", "description", "memo")
	protocolIdx := findHeader(headers, "protocol", "proto", "scheme")
	portIdx := findHeader(headers, "port")
	usernameIdx := findHeader(headers, "username", "user", "login")
	keyPathIdx := findExactHeader(headers, "keypath", "key path", "identityfile", "identity file")
	passphraseIdx := findExactHeader(headers, "passphrase", "keypassphrase", "key passphrase")
	proxyIdx := findExactHeader(headers, "proxy", "proxyserver", "proxy server")
	localShellIdx := findExactHeader(headers, "localshell", "local shell")
	passwordIdx := findExactHeader(headers, "password", "pass", "passwd")
	if passwordIdx < 0 {
		passwordIdx = findHeader(headers, "password", "passwd")
	}
	if passwordIdx < 0 && keyPathIdx < 0 && passphraseIdx >= 0 {
		// A lone "Passphrase" column used to mean the password before KeyPath
		// existed; keep that reading for legacy files.
		passwordIdx = passphraseIdx
		passphraseIdx = -1
	}

	seen := make(map[string]bool)
	for index := 1; index < len(rows); index++ {
		row := rows[index]
		rowNumber := index + 1
		hostnameRaw := strings.TrimSpace(cell(row, hostnameIdx))
		if hostnameRaw == "" {
			if rowHasContent(row) {
				// Do not drop a data row silently: an encoded LocalShell entry
				// has no hostname, and the preview must still name the row.
				result.Issues = append(result.Issues,
					issue(IssueMissingHostname, rowNumber, "hostname", ""))
			}
			continue
		}
		result.Records = append(result.Records, Record{})
		record := &result.Records[len(result.Records)-1]
		record.Row = rowNumber
		record.Source = "csv"

		hostname, targetUser, targetPort, targetProtocol, ok := parseTarget(
			StripFormulaGuard(hostnameRaw))
		if !ok {
			result.Issues = append(result.Issues,
				issue(IssueInvalidHostname, rowNumber, "hostname", hostnameRaw))
			result.Records = result.Records[:len(result.Records)-1]
			continue
		}
		record.Hostname = hostname
		record.Username = targetUser
		record.Port = targetPort
		record.Protocol = targetProtocol

		record.Group = normalizeGroup(StripFormulaGuard(cell(row, groupsIdx)))
		record.Label = StripFormulaGuard(strings.TrimSpace(cell(row, labelIdx)))
		record.Tags = nil
		for _, tag := range splitTags(StripFormulaGuard(cell(row, tagsIdx))) {
			record.Tags = append(record.Tags, tag)
		}
		record.Notes = StripFormulaGuard(strings.TrimSpace(cell(row, notesIdx)))

		if raw := strings.TrimSpace(cell(row, protocolIdx)); raw != "" {
			value := normalizeProtocol(raw)
			if value == "" {
				// Never coerce an unknown transport to SSH: that would import a
				// serial, RDP, or plugin Host as a different endpoint.
				result.Records = result.Records[:len(result.Records)-1]
				result.Issues = append(result.Issues,
					issue(IssueUnsupportedProtocol, rowNumber, "protocol", raw))
				continue
			}
			record.Protocol = value
		}
		if record.Protocol == "" {
			record.Protocol = "ssh"
		}
		if portRaw := strings.TrimSpace(cell(row, portIdx)); portRaw != "" {
			value, portOK := parsePort(portRaw)
			if !portOK {
				result.Issues = append(result.Issues,
					issue(IssueInvalidPort, rowNumber, "port", portRaw))
			} else {
				record.Port = value
			}
		}
		if value := strings.TrimSpace(StripFormulaGuard(cell(row, usernameIdx))); value != "" {
			record.Username = value
		}
		record.Password = cell(row, passwordIdx)
		if raw := strings.TrimSpace(cell(row, keyPathIdx)); raw != "" {
			record.KeyPath = DecodeMarkedField(raw, MarkerKeyPath)
		}
		if raw := strings.TrimSpace(cell(row, passphraseIdx)); raw != "" {
			record.Passphrase = DecodeMarkedField(raw, MarkerPassphrase)
		}
		if record.Passphrase != "" && record.KeyPath == "" {
			result.Issues = append(result.Issues,
				issue(IssuePassphraseConflict, rowNumber, "passphrase", ""))
		}
		if raw := strings.TrimSpace(cell(row, proxyIdx)); raw != "" {
			decoded := DecodeMarkedField(raw, MarkerProxy)
			proxy, ok := parseProxy(decoded)
			if !ok {
				result.Issues = append(result.Issues,
					issue(IssueInvalidProxy, rowNumber, "proxy", decoded))
			} else {
				record.Proxy = proxy
			}
		}
		if raw := strings.TrimSpace(cell(row, localShellIdx)); raw != "" {
			// The legacy format carries local-shell configuration in a marked
			// field, but the native Host model does not store that
			// configuration. Report the loss instead of importing a plain
			// Host that silently runs the default shell.
			result.Issues = append(result.Issues,
				issue(IssueUnsupportedEntry, rowNumber, "localshell", raw))
		}

		key := mergeKey(*record)
		if seen[key] {
			result.Issues = append(result.Issues,
				issue(IssueDuplicateInFile, rowNumber, "", record.Hostname))
		}
		seen[key] = true
	}
	if len(result.Records) == 0 && !hasError(result.Issues) {
		result.Issues = append(result.Issues, issue(IssueNothingParsed, 0, "", ""))
	}
	return result
}

// mergeKey mirrors domain/vault Host.MergeKey without importing that package,
// keeping the parser dependency-free. Unknown protocols keep their raw value
// so a parser bug cannot merge two different entries.
func mergeKey(record Record) string {
	protocol := record.Protocol
	if protocol == "" {
		protocol = "ssh"
	}
	parts := []string{
		protocol,
		strings.ToLower(record.Hostname),
		itoa(record.Port),
		strings.ToLower(record.Username),
		normalizeGroup(record.Group),
	}
	if protocol == "local" {
		parts = append(parts, strings.ToLower(strings.TrimSpace(record.Label)))
	}
	return strings.Join(parts, "\x1f")
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [8]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

func hasError(issues []Issue) bool {
	for _, item := range issues {
		if item.Severity == SeverityError {
			return true
		}
	}
	return false
}

// parseProxy reads the CSV proxy representation:
//
//	http://[user[:pass]@]host:port
//	socks5://[user[:pass]@]host:port
//	command://<verbatim command>
func parseProxy(raw string) (*Proxy, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "command://") {
		command := strings.TrimSpace(value[len("command://"):])
		if command == "" || strings.ContainsRune(command, 0) {
			return nil, false
		}
		return &Proxy{Type: "command", Command: command}, true
	}
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "socks5://") {
		return nil, false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" || parsed.Port() == "" {
		return nil, false
	}
	port, ok := parsePort(parsed.Port())
	if !ok {
		return nil, false
	}
	proxy := &Proxy{Type: "http", Host: parsed.Hostname(), Port: port}
	if strings.HasPrefix(lower, "socks5://") {
		proxy.Type = "socks5"
	}
	if parsed.User != nil {
		proxy.Username = parsed.User.Username()
		proxy.Password, _ = parsed.User.Password()
	}
	if proxy.Host == "" {
		return nil, false
	}
	return proxy, true
}
