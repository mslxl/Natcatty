package history

import "time"

// The record log is the append-only local representation of the Atuin records
// this install writes and reads. It is stored in plaintext, like the history
// journal; encryption and the wire encoding belong to the adapters that talk to
// an Atuin server. One journal mutation appends exactly one record.

// RecordTagHistory is the only record tag Netcatty stores. Other Atuin tags
// (kv, scripts, dotfiles, AI) are out of scope and are never downloaded.
const RecordTagHistory = "history"

// Record kinds. A create record accompanies a newly recorded entry; a delete
// record is a tombstone for an existing history id.
const (
	RecordKindCreate = "create"
	RecordKindDelete = "delete"
)

// Record directions. Local records are in this install's writer series and may
// be uploaded; remote records were downloaded from another writer's series.
const (
	RecordDirectionLocal  = "local"
	RecordDirectionRemote = "remote"
)

// RecordVersion is the local payload format version. The Atuin adapter encodes
// the payload for the wire; this version keeps the local log self-describing
// until that adapter exists.
const RecordVersion = 2

// Record is one append-only entry in the local record log. SeriesHost is the
// writer UUID that owns the series, Tag is always RecordTagHistory, and Index
// is assigned inside the writing transaction. The unique (SeriesHost, Tag,
// Index) key makes a series append-only and a retried download idempotent. A
// zero UploadedAt means a local record still waiting to be uploaded.
type Record struct {
	ID         string    `json:"id"`
	SeriesHost string    `json:"seriesHost"`
	Tag        string    `json:"tag"`
	Index      int64     `json:"index"`
	RecordedAt time.Time `json:"recordedAt"`
	Version    int       `json:"version"`
	Kind       string    `json:"kind"`
	HistoryID  string    `json:"historyId"`
	Payload    string    `json:"payload"`
	Direction  string    `json:"direction"`
	UploadedAt time.Time `json:"uploadedAt,omitempty"`
}

// Valid reports whether a record carries the identity and routing fields the
// log needs. Index is assigned by the store, so it is not part of validity.
func (record Record) Valid() bool {
	return record.ID != "" && record.SeriesHost != "" && record.Tag != "" &&
		!record.RecordedAt.IsZero() &&
		(record.Kind == RecordKindCreate || record.Kind == RecordKindDelete) &&
		record.HistoryID != "" &&
		(record.Direction == RecordDirectionLocal || record.Direction == RecordDirectionRemote)
}

// DeleteRecordPayload is the plaintext local representation of a delete
// record's payload. Later tickets replace the payload with the encoded Atuin
// history record; the history id and deletion time are what the local log uses
// to apply a tombstone.
type DeleteRecordPayload struct {
	HistoryID string    `json:"historyId"`
	DeletedAt time.Time `json:"deletedAt"`
}
