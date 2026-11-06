package history

import "time"

// Metadata is the device-local sync state kept in the store's meta table. It
// is not part of the Vault and never travels through Vault sync or a portable
// export. The writer UUID is generated once per install; the checkpoints and
// account identity let a later sync resume without re-uploading or
// re-downloading records.
type Metadata struct {
	// WriterUUID is this install's Atuin series host. It survives restarts and
	// is reused across account reconnects, so the local series keeps its
	// indexes.
	WriterUUID string `json:"writerUuid,omitempty"`
	// ShellHistoryImported marks the one-time Shell History File import. An
	// empty set of files still sets it.
	ShellHistoryImported bool `json:"shellHistoryImported,omitempty"`
	// KeyFingerprint is a hash prefix of the local Atuin master key, never the
	// key itself.
	KeyFingerprint string `json:"keyFingerprint,omitempty"`
	// Account identifies the connected Atuin account so a same-account
	// reconnect can be told from an account change.
	Account AccountIdentity `json:"account,omitempty"`
	// HostImports holds the per-Host read-only Atuin import checkpoint.
	HostImports map[string]ImportCheckpoint `json:"hostImports,omitempty"`
	// Series holds the per-series download and upload checkpoints.
	Series map[string]SeriesCheckpoint `json:"series,omitempty"`
}

// AccountIdentity is the connected Atuin account's server and user id.
type AccountIdentity struct {
	ServerURL string `json:"serverUrl,omitempty"`
	UserID    string `json:"userId,omitempty"`
}

// ImportCheckpoint is the newest row a read-only Atuin import has seen. The
// pair orders rows that share a timestamp.
type ImportCheckpoint struct {
	Timestamp time.Time `json:"timestamp,omitempty"`
	HistoryID string    `json:"historyId,omitempty"`
}

// SeriesCheckpoint records how far a writer series has been applied locally
// and how far the server has acknowledged an upload. Negative indexes mean
// "nothing yet": index 0 is a real record.
type SeriesCheckpoint struct {
	SeriesHost    string `json:"seriesHost,omitempty"`
	Tag           string `json:"tag,omitempty"`
	AppliedIndex  int64  `json:"appliedIndex"`
	UploadedIndex int64  `json:"uploadedIndex"`
}

// SeriesKey is the metadata lookup key for one writer series. A writer UUID
// never contains a colon, so the tag stays unambiguous.
func SeriesKey(seriesHost, tag string) string {
	return seriesHost + ":" + tag
}
