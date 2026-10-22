// Package vaultimport parses Host inventories from other tools. It is a pure
// domain package: it uses only the standard library and never reads files,
// touches a database, or contains user-visible text. Its records carry
// credential plaintext only in memory; the application layer hands them to
// the field-secret store and never writes them to a Vault record.
package vaultimport

import (
	"strings"
	"time"
)

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
	// FormatNetcattyJSON is Netcatty's own versioned JSON backup.
	FormatNetcattyJSON Format = "netcatty_json"
	// FormatSnippets is Netcatty's portable Snippets JSON document.
	FormatSnippets Format = "snippets"
	// FormatKeys is Netcatty's portable SSH Keys JSON document.
	FormatKeys Format = "keys"
	// FormatNotesZIP is Netcatty's Notes Markdown archive.
	FormatNotesZIP Format = "notes_zip"
)

// KindNetcattyVault is the JSON document kind a Netcatty JSON export writes.
const KindNetcattyVault = "netcatty.vault"

// KindSnippets, KindKeys, and KindNotes are the versioned document markers for
// the single-family exports.
const (
	KindSnippets = "netcatty.snippets"
	KindKeys     = "netcatty.keys"
	KindNotes    = "netcatty.notes"
)

// NotesManifestName is the reserved archive entry that maps a human-readable
// Notes ZIP path back to its exact Note Group, title, and identity.
const NotesManifestName = "netcatty.notes.json"

// Formats returns every format the importer can parse, in menu order.
func Formats() []Format {
	return []Format{
		FormatCSV, FormatNetcattyJSON, FormatSnippets, FormatKeys, FormatNotesZIP,
		FormatPuTTY, FormatMobaXterm, FormatFinalShell, FormatSecureCRT, FormatSSHConfig,
	}
}

// Valid reports whether f is a supported format.
func (f Format) Valid() bool {
	switch f {
	case FormatCSV, FormatNetcattyJSON, FormatSnippets, FormatKeys, FormatNotesZIP,
		FormatPuTTY, FormatMobaXterm, FormatFinalShell, FormatSecureCRT, FormatSSHConfig:
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
	IssueInvalidArchive      IssueCode = "invalid_archive"
	IssueInvalidManifest     IssueCode = "invalid_manifest"
	IssueMissingNoteEntry    IssueCode = "missing_note_entry"
	// IssueKeyWithoutMaterial reports a restored Key record whose private
	// material was not in the document (for example a secrets-off export).
	IssueKeyWithoutMaterial IssueCode = "key_without_material"
	// IssueDanglingSnippetTarget reports a Snippet target that this import
	// cannot resolve to a Host. The reference is preserved, not dropped.
	IssueDanglingSnippetTarget IssueCode = "dangling_snippet_target"
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
	// HasPassword reports that an encrypted proxy password exists. It is
	// meaningful on a Proxy Profile, which never carries plaintext outside the
	// secret store.
	HasPassword bool
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
	// KeyMaterial is inline private key material from a JSON export. It lets
	// an import create the Key without a readable file at KeyPath.
	KeyMaterial string
	// KeyRef is the source document's Key identity, used to dedupe an inline
	// Key shared by several Hosts.
	KeyRef string
	// KeyLabelHint is the exported Key label.
	KeyLabelHint string
	// SourceID is the identity of the source Host in a Netcatty JSON document.
	// It lets an import map Host references (Snippet targets, Identity and
	// Proxy Profile links) onto the identities the import creates.
	SourceID string
	// Pinned, IdentityID, KeyID, AuthMethod, UseSSHAgent, RequiresMFA,
	// X11Forwarding, X11WarningAcknowledged, ProxyProfileID,
	// LegacyAlgorithms, SkipEcdsaHostKey, and Algorithms are the Host options a
	// Netcatty JSON export carries.
	Pinned                 bool
	IdentityID             string
	KeyID                  string
	AuthMethod             string
	RequiresMFA            bool
	X11WarningAcknowledged bool
	ProxyProfileID         string
	LegacyAlgorithms       bool
	SkipEcdsaHostKey       bool
	Algorithms             *Algorithms
}

// Algorithms is a Host's advanced SSH algorithm selection. A non-empty
// category replaces the transport's offer for that category.
type Algorithms struct {
	KeyExchanges []string
	Ciphers      []string
	MACs         []string
	HostKeys     []string
}

// Family is the non-Host data a Netcatty document carries. The application
// maps these records onto the Vault domain.
type Family struct {
	Groups          []string
	SnippetPackages []string
	Snippets        []FamilySnippet
	NoteGroups      []string
	Notes           []FamilyNote
	Keys            []FamilyKey
	Identities      []FamilyIdentity
	ProxyProfiles   []FamilyProxyProfile
	KnownHosts      []FamilyKnownHost
}

// FamilySnippet is one portable Snippet.
type FamilySnippet struct {
	ID              string
	Label           string
	Command         string
	Package         string
	Tags            []string
	Targets         []string
	TargetGroups    []string
	TargetsAllHosts bool
	NoAutoRun       bool
	MultiLineMode   string
	VariableValues  map[string]string
}

// FamilyNote is one portable Vault Note.
type FamilyNote struct {
	ID      string
	Title   string
	Content string
	Group   string
}

// FamilyKey is one portable SSH Key.
type FamilyKey struct {
	ID          string
	Label       string
	Type        string
	PublicKey   string
	Certificate string
	Source      string
	FilePath    string
	PrivateKey  string
	Passphrase  string
}

// FamilyIdentity is one portable Identity. Password is plaintext in memory
// only and is written to the field-secret store by the application.
type FamilyIdentity struct {
	ID          string
	Label       string
	Username    string
	AuthMethod  string
	KeyID       string
	HasPassword bool
	Password    string
}

// FamilyProxyProfile is one portable Proxy Profile. Password is plaintext in
// memory only.
type FamilyProxyProfile struct {
	ID       string
	Label    string
	Config   Proxy
	Password string
}

// FamilyKnownHost is one recorded host-key trust decision.
type FamilyKnownHost struct {
	Hostname          string
	Port              int
	KeyType           string
	PublicKey         string
	Fingerprint       string
	DiscoveredAt      time.Time
	LastSeen          time.Time
	ConvertedToHostID string
}

// HasData reports whether the family carries any record. A document may be a
// valid empty family export.
func (f *Family) HasData() bool {
	if f == nil {
		return false
	}
	return len(f.Groups) > 0 || len(f.SnippetPackages) > 0 || len(f.Snippets) > 0 ||
		len(f.NoteGroups) > 0 || len(f.Notes) > 0 || len(f.Keys) > 0 ||
		len(f.Identities) > 0 || len(f.ProxyProfiles) > 0 || len(f.KnownHosts) > 0
}

// Result is one document's parse output.
type Result struct {
	Format  Format
	Records []Record
	Issues  []Issue
	// Family is set for a Netcatty document that carries non-Host data. A
	// family-only document has no Records and still previews.
	Family *Family
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
	case FormatNetcattyJSON:
		return parseJSON(text)
	case FormatSnippets:
		return parseSnippetsJSON(text)
	case FormatKeys:
		return parseKeysJSON(text)
	case FormatNotesZIP:
		return parseNotesZIP(text)
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
