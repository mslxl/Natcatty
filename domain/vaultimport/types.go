// Package vaultimport parses Host inventories from other tools. It is a pure
// domain package: it uses only the standard library and never reads files,
// touches a database, or contains user-visible text. Its records carry
// credential plaintext only in memory; the application layer hands them to
// the field-secret store and never writes them to a Vault record.
package vaultimport

import "strings"

// Format names one supported import format.
type Format string

const (
	// FormatCSV is Netcatty's own CSV export.
	FormatCSV Format = "csv"
	// FormatPuTTY is a PuTTY registry export or session block file.
	FormatPuTTY Format = "putty"
	// FormatMobaXterm is a MobaXterm INI configuration or session list.
	FormatMobaXterm Format = "mobaxterm"
	// FormatFinalShell is a FinalShell connection JSON document.
	FormatFinalShell Format = "finalshell"
	// FormatSecureCRT is a SecureCRT session configuration.
	FormatSecureCRT Format = "securecrt"
	// FormatSSHConfig is an OpenSSH ssh_config file.
	FormatSSHConfig Format = "ssh_config"
)

// Formats returns every format the importer can parse, in menu order.
func Formats() []Format {
	return []Format{
		FormatCSV, FormatPuTTY, FormatMobaXterm,
		FormatFinalShell, FormatSecureCRT, FormatSSHConfig,
	}
}

// Valid reports whether f is a supported format.
func (f Format) Valid() bool {
	switch f {
	case FormatCSV, FormatPuTTY, FormatMobaXterm,
		FormatFinalShell, FormatSecureCRT, FormatSSHConfig:
		return true
	default:
		return false
	}
}

// Severity classifies an import issue. The UI localizes the code; a severity
// only chooses between a warning and a blocking error.
type Severity string

const (
	// SeverityWarning reports a value that was skipped or degraded.
	SeverityWarning Severity = "warning"
	// SeverityError reports input that cannot be parsed at all.
	SeverityError Severity = "error"
)

// IssueCode names one import problem. It is stable data, never display text.
type IssueCode string

const (
	IssueCSVEmpty            IssueCode = "csv_empty"
	IssueCSVHeader           IssueCode = "csv_missing_hostname_column"
	IssueCSVQuoting          IssueCode = "csv_quoting"
	IssueInvalidHostname     IssueCode = "invalid_hostname"
	IssueMissingHostname     IssueCode = "missing_hostname"
	IssueInvalidPort         IssueCode = "invalid_port"
	IssueUnsupportedProtocol IssueCode = "unsupported_protocol"
	IssueUnsupportedEntry    IssueCode = "unsupported_entry"
	IssueInvalidProxy        IssueCode = "invalid_proxy"
	IssueInvalidField        IssueCode = "invalid_field"
	IssueInvalidJSON         IssueCode = "invalid_json"
	IssuePasswordUndecrypt   IssueCode = "password_undecryptable"
	IssueMasterPassword      IssueCode = "master_password_required"
	IssuePasswordsInRegistry IssueCode = "passwords_in_registry"
	IssuePasswordUnmatched   IssueCode = "password_unmatched"
	IssueX11NotImported      IssueCode = "x11_not_imported"
	IssueDuplicateInFile     IssueCode = "duplicate_in_file"
	IssueInvalidJump         IssueCode = "invalid_jump"
	IssueCyclicJump          IssueCode = "cyclic_jump"
	IssueKeyUnreadable       IssueCode = "key_unreadable"
	IssuePassphraseConflict  IssueCode = "passphrase_conflict"
	IssueFormulaNeutralized  IssueCode = "formula_neutralized"
	IssueNothingParsed       IssueCode = "nothing_parsed"
)

// Issue is one parse or validation problem tied to a source row. Row is
// 1-based; 0 means the whole document. Field, Value, and Detail are data for
// the localized message, never display text themselves.
type Issue struct {
	Severity Severity
	Code     IssueCode
	Row      int
	Field    string
	Value    string
	Detail   string
}

// Proxy is a parsed proxy transport. Password is plaintext in memory only.
type Proxy struct {
	Type     string
	Host     string
	Port     int
	Command  string
	Username string
	Password string
}

// Jump is one parsed ProxyJump hop.
type Jump struct {
	Username string
	Hostname string
	Port     int
}

// Record is one parsed Host before Vault validation. An InlineJump record was
// synthesized because ProxyJump named a Host the file does not declare.
type Record struct {
	Row      int
	Source   string
	Label    string
	Hostname string
	Username string
	Port     int
	// Protocol is "ssh", "telnet", or "local"; empty means SSH.
	Protocol string
	Group    string
	Tags     []string
	Notes    string
	// Password is credential plaintext in memory only.
	Password string
	// KeyPath and Passphrase feed the SSH Key importer. They are never
	// written onto a Host record.
	KeyPath    string
	Passphrase string
	Proxy      *Proxy
	// UseSSHAgent and X11Forwarding mirror ssh_config directives the Vault
	// models directly.
	UseSSHAgent   bool
	X11Forwarding bool
	// InlineJump marks a synthesized ProxyJump Host.
	InlineJump bool
	// JumpSpecs are the parsed ProxyJump hops for this record.
	JumpSpecs []Jump
	// JumpRefs indexes Result.Records for this record's ProxyJump chain.
	JumpRefs []int
}

// Result is one document's parse output.
type Result struct {
	Format  Format
	Records []Record
	Issues  []Issue
}

// Options are parser inputs that do not belong to the document itself.
type Options struct {
	// FileName labels SecureCRT sessions that declare no Session Name.
	FileName string
	// MasterPassword unlocks MobaXterm credentials.
	MasterPassword string
}

// Parse parses text in format f. A parse failure is reported through Issues
// with SeverityError; the caller can still show a preview of valid rows.
func Parse(f Format, text string, opts Options) Result {
	switch f {
	case FormatCSV:
		return parseCSV(text)
	case FormatPuTTY:
		return parsePuTTY(text)
	case FormatMobaXterm:
		return parseMobaXterm(text, opts)
	case FormatFinalShell:
		return parseFinalShell(text)
	case FormatSecureCRT:
		return parseSecureCRT(text, opts)
	case FormatSSHConfig:
		return parseSSHConfig(text)
	default:
		return Result{Format: f, Issues: []Issue{{
			Severity: SeverityError, Code: IssueUnsupportedEntry,
			Detail: string(f),
		}}}
	}
}

// normalizeGroup canonicalizes a slash-separated group path.
func normalizeGroup(raw string) string {
	raw = strings.ReplaceAll(raw, "\\", "/")
	parts := strings.Split(raw, "/")
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "/")
}

// normalizeProtocol maps external protocol spellings onto the Vault's three
// stored protocols. An empty result means the value is not recognized.
func normalizeProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ssh", "ssh2", "ssh-2", "ssh1", "ssh-1":
		return "ssh"
	case "telnet":
		return "telnet"
	case "local":
		return "local"
	default:
		return ""
	}
}

// parsePort accepts a decimal port in 1..65535.
func parsePort(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, false
		}
		value = value*10 + int(r-'0')
		if value > 65535 {
			return 0, false
		}
	}
	if value < 1 {
		return 0, false
	}
	return value, true
}

// defaultPort returns the protocol default. It is used by parsers that must
// materialize a port because the source format has no group-default concept.
func defaultPort(protocol string) int {
	if protocol == "telnet" {
		return 23
	}
	if protocol == "local" {
		return 0
	}
	return 22
}

func issue(code IssueCode, row int, field, value string) Issue {
	return Issue{Severity: SeverityWarning, Code: code, Row: row, Field: field, Value: value}
}

func errorIssue(code IssueCode, row int, detail string) Issue {
	return Issue{Severity: SeverityError, Code: code, Row: row, Detail: detail}
}
