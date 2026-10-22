// Package vaultbackup owns the rules for local Vault backups: the record
// format version, retention and payload limits, the reasons a backup is
// written, and the canonical snapshot a backup stores.
//
// A Snapshot is a schema-versioned image of the Vault's storage tables. It is
// intentionally not the portable export document: a local backup guarantees a
// restore on the same device, including credential ciphertext that only that
// device's field key can read. The canonical bytes are what the fingerprint
// hashes and what the backup adapter encrypts.
package vaultbackup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const (
	// FormatVersion is the on-disk backup envelope version. It is independent
	// of the Snapshot schema version.
	FormatVersion = 1
	// MaxPayloadBytes caps the plaintext canonical snapshot, matching the
	// original's 25 MiB limit.
	MaxPayloadBytes = 25 << 20
	// DefaultRetention is how many backups are kept when the user has not
	// chosen a limit.
	DefaultRetention = 20
	// MinRetention and MaxRetention bound the user-configurable limit.
	MinRetention = 1
	MaxRetention = 100
)

// Reason records why a backup was written.
type Reason string

const (
	// ReasonVaultChange is an automatic backup after a committed Vault change.
	ReasonVaultChange Reason = "vault_change"
	// ReasonBeforeRestore is the protective backup written before a restore.
	ReasonBeforeRestore Reason = "before_restore"
	// ReasonAppVersionChange is the startup backup after an app upgrade.
	ReasonAppVersionChange Reason = "app_version_change"
)

// Valid reports whether r is a reason this app writes.
func (r Reason) Valid() bool {
	switch r {
	case ReasonVaultChange, ReasonBeforeRestore, ReasonAppVersionChange:
		return true
	}
	return false
}

// SanitizeRetention clamps a retention limit into the supported range.
func SanitizeRetention(value int) int {
	if value < MinRetention {
		return MinRetention
	}
	if value > MaxRetention {
		return MaxRetention
	}
	return value
}

// Table names identify the Vault families a Snapshot carries. They are the
// adapter's storage names so a restore can rewrite the exact rows; a snapshot
// never carries workspace, history, logs, or settings.
const (
	TableHosts           = "hosts"
	TableGroups          = "host_groups"
	TableKeys            = "ssh_keys"
	TableIdentities      = "identities"
	TableProxyProfiles   = "proxy_profiles"
	TableKnownHosts      = "known_hosts"
	TableForwardingRules = "forwarding_rules"
	TableSnippets        = "snippets"
	TableSnippetPackages = "snippet_packages"
	TableNotes           = "vault_notes"
	TableNoteGroups      = "note_groups"
	TableSecrets         = "secret_values"
	TableCryptoKeys      = "crypto_keys"
)

// Tables is the fixed set a Snapshot may contain, in canonical order.
var Tables = []string{
	TableCryptoKeys,
	TableForwardingRules,
	TableGroups,
	TableHosts,
	TableIdentities,
	TableKeys,
	TableKnownHosts,
	TableNoteGroups,
	TableNotes,
	TableProxyProfiles,
	TableSecrets,
	TableSnippetPackages,
	TableSnippets,
}

func knownTable(name string) bool {
	for _, table := range Tables {
		if table == name {
			return true
		}
	}
	return false
}

// ValueKind names a SQLite value's type so a round trip preserves it.
type ValueKind string

const (
	KindNull  ValueKind = "null"
	KindInt   ValueKind = "int"
	KindFloat ValueKind = "float"
	KindText  ValueKind = "text"
	KindBlob  ValueKind = "blob"
)

// Value is one typed column value. Encoding the type explicitly matters for
// BLOBs: JSON would otherwise turn a nonce into a text value and a restore
// would write it back to the wrong storage class.
type Value struct {
	Kind  ValueKind `json:"kind"`
	Int   int64     `json:"int,omitempty"`
	Float float64   `json:"float,omitempty"`
	Text  string    `json:"text,omitempty"`
	Blob  []byte    `json:"blob,omitempty"`
}

// Null returns a NULL value.
func Null() Value { return Value{Kind: KindNull} }

// Int returns an integer value.
func Int(value int64) Value { return Value{Kind: KindInt, Int: value} }

// Float returns a real value.
func Float(value float64) Value { return Value{Kind: KindFloat, Float: value} }

// Text returns a text value.
func Text(value string) Value { return Value{Kind: KindText, Text: value} }

// Blob returns a BLOB value.
func Blob(value []byte) Value { return Value{Kind: KindBlob, Blob: value} }

// Any returns the value in the form the SQLite driver accepts.
func (v Value) Any() any {
	switch v.Kind {
	case KindInt:
		return v.Int
	case KindFloat:
		return v.Float
	case KindText:
		return v.Text
	case KindBlob:
		return v.Blob
	default:
		return nil
	}
}

// Valid reports whether v has a known kind.
func (v Value) Valid() bool {
	switch v.Kind {
	case KindNull, KindInt, KindFloat, KindText, KindBlob:
		return true
	}
	return false
}

// Row is one table row keyed by column name.
type Row map[string]Value

// Table is one family's rows.
type Table struct {
	Name string `json:"name"`
	Rows []Row  `json:"rows"`
}

// Snapshot is a logical image of the Vault's tables.
type Snapshot struct {
	SchemaVersion int     `json:"schemaVersion"`
	Tables        []Table `json:"tables"`
}

// Counts is the record summary shown beside a backup.
type Counts struct {
	Hosts           int `json:"hosts"`
	Groups          int `json:"groups"`
	Keys            int `json:"keys"`
	Identities      int `json:"identities"`
	ProxyProfiles   int `json:"proxyProfiles"`
	KnownHosts      int `json:"knownHosts"`
	ForwardingRules int `json:"forwardingRules"`
	Snippets        int `json:"snippets"`
	SnippetPackages int `json:"snippetPackages"`
	Notes           int `json:"notes"`
	NoteGroups      int `json:"noteGroups"`
}

// Total reports how many Vault records the snapshot carries.
func (c Counts) Total() int {
	return c.Hosts + c.Groups + c.Keys + c.Identities + c.ProxyProfiles +
		c.KnownHosts + c.ForwardingRules + c.Snippets + c.SnippetPackages +
		c.Notes + c.NoteGroups
}

// Summary describes one stored backup without its encrypted payload.
type Summary struct {
	ID               string `json:"id"`
	CreatedAt        int64  `json:"createdAt"` // Unix milliseconds
	Reason           Reason `json:"reason"`
	Fingerprint      string `json:"fingerprint"`
	SizeBytes        int64  `json:"sizeBytes"`
	Counts           Counts `json:"counts"`
	SourceAppVersion string `json:"sourceAppVersion,omitempty"`
	TargetAppVersion string `json:"targetAppVersion,omitempty"`
}

// ErrInvalidSnapshot reports a structurally invalid snapshot.
var ErrInvalidSnapshot = errors.New("vaultbackup: invalid snapshot")

// Validate rejects a snapshot that is not safe to encode or restore. It
// checks structure, not row uniqueness: a duplicate primary key must reach the
// store so the restore transaction rolls back and proves the rollback path.
func Validate(snapshot Snapshot) error {
	if snapshot.SchemaVersion < 1 {
		return fmt.Errorf("%w: schema version %d", ErrInvalidSnapshot, snapshot.SchemaVersion)
	}
	seen := make(map[string]bool, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		if !knownTable(table.Name) {
			return fmt.Errorf("%w: unknown table %q", ErrInvalidSnapshot, table.Name)
		}
		if seen[table.Name] {
			return fmt.Errorf("%w: duplicate table %q", ErrInvalidSnapshot, table.Name)
		}
		seen[table.Name] = true
		for _, row := range table.Rows {
			if len(row) == 0 {
				return fmt.Errorf("%w: empty row in %q", ErrInvalidSnapshot, table.Name)
			}
			for column, value := range row {
				if column == "" {
					return fmt.Errorf("%w: empty column in %q", ErrInvalidSnapshot, table.Name)
				}
				if !value.Valid() {
					return fmt.Errorf("%w: column %q has kind %q", ErrInvalidSnapshot, column, value.Kind)
				}
			}
		}
	}
	return nil
}

// CanonicalBytes returns the deterministic encoding used for the fingerprint
// and the encrypted payload. Tables are ordered by name and rows by their
// canonical JSON, so query order can never produce a different fingerprint for
// the same data.
func CanonicalBytes(snapshot Snapshot) ([]byte, error) {
	canonical, err := canonicalize(snapshot)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("vaultbackup: encode snapshot: %w", err)
	}
	return data, nil
}

// Parse decodes canonical bytes back into a Snapshot and validates it.
func Parse(data []byte) (Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
	}
	if err := Validate(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func canonicalize(snapshot Snapshot) (Snapshot, error) {
	if err := Validate(snapshot); err != nil {
		return Snapshot{}, err
	}
	tables := make([]Table, len(snapshot.Tables))
	for index, table := range snapshot.Tables {
		rows := make([]Row, len(table.Rows))
		copy(rows, table.Rows)
		keys := make([]string, len(rows))
		for rowIndex, row := range rows {
			encoded, err := json.Marshal(row)
			if err != nil {
				return Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidSnapshot, err)
			}
			keys[rowIndex] = string(encoded)
		}
		sort.Sort(rowSorter{rows: rows, keys: keys})
		tables[index] = Table{Name: table.Name, Rows: rows}
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	return Snapshot{SchemaVersion: snapshot.SchemaVersion, Tables: tables}, nil
}

type rowSorter struct {
	rows []Row
	keys []string
}

func (s rowSorter) Len() int           { return len(s.rows) }
func (s rowSorter) Less(i, j int) bool { return s.keys[i] < s.keys[j] }
func (s rowSorter) Swap(i, j int) {
	s.rows[i], s.rows[j] = s.rows[j], s.rows[i]
	s.keys[i], s.keys[j] = s.keys[j], s.keys[i]
}

// Fingerprint returns the SHA-256 hex digest of the canonical snapshot.
func Fingerprint(snapshot Snapshot) (string, error) {
	data, err := CanonicalBytes(snapshot)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Counts summarizes the snapshot's records.
func (s Snapshot) Counts() Counts {
	var counts Counts
	for _, table := range s.Tables {
		rows := len(table.Rows)
		switch table.Name {
		case TableHosts:
			counts.Hosts = rows
		case TableGroups:
			counts.Groups = rows
		case TableKeys:
			counts.Keys = rows
		case TableIdentities:
			counts.Identities = rows
		case TableProxyProfiles:
			counts.ProxyProfiles = rows
		case TableKnownHosts:
			counts.KnownHosts = rows
		case TableForwardingRules:
			counts.ForwardingRules = rows
		case TableSnippets:
			counts.Snippets = rows
		case TableSnippetPackages:
			counts.SnippetPackages = rows
		case TableNotes:
			counts.Notes = rows
		case TableNoteGroups:
			counts.NoteGroups = rows
		}
	}
	return counts
}

// Empty reports whether the snapshot carries no Vault record. Key metadata
// and credential ciphertext alone do not make a backup meaningful.
func (s Snapshot) Empty() bool { return s.Counts().Total() == 0 }
