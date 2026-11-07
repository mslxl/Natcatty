package remote

import (
	"fmt"
	"math"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// encodeRecord turns one domain sync record into an encrypted Atuin wire
// record. The caller is the local writer series, so this is also where the
// journal's fields are mapped onto the pinned V2 payload. Writes are always
// V2: the compatibility contract is "V0/V1/V2 decode, V2 write".
func encodeRecord(record historysync.SyncRecord, key wire.Key) (wire.Record, error) {
	if !record.Valid() {
		return wire.Record{}, fmt.Errorf("%w: invalid sync record", historysync.ErrRemote)
	}
	if record.Tag != domainhistory.RecordTagHistory {
		return wire.Record{}, fmt.Errorf("%w: only the history tag may be uploaded", historysync.ErrRemote)
	}
	if record.Version != int(domainhistory.RecordVersion) {
		return wire.Record{}, fmt.Errorf("%w: only V2 records may be uploaded", historysync.ErrRemote)
	}
	if record.Index < 0 {
		return wire.Record{}, fmt.Errorf("%w: negative record index", historysync.ErrRemote)
	}
	if _, ok := wire.CanonicalUUID(record.ID); !ok {
		return wire.Record{}, fmt.Errorf("%w: record id is not a UUID", historysync.ErrRemote)
	}
	if _, ok := wire.CanonicalUUID(record.SeriesHost); !ok {
		return wire.Record{}, fmt.Errorf("%w: series host is not a UUID", historysync.ErrRemote)
	}
	if _, ok := wire.CanonicalUUID(record.HistoryID); !ok {
		return wire.Record{}, fmt.Errorf("%w: history id is not a UUID", historysync.ErrRemote)
	}
	timestamp, ok := unixNanos(record.RecordedAt)
	if !ok {
		return wire.Record{}, fmt.Errorf("%w: record timestamp is out of range", historysync.ErrRemote)
	}

	outer := wire.Record{
		ID:         record.ID,
		Index:      uint64(record.Index),
		SeriesHost: record.SeriesHost,
		Timestamp:  timestamp,
		Version:    string(wire.Version2),
		Tag:        domainhistory.RecordTagHistory,
	}

	var plaintext wire.HistoryRecord
	switch record.Kind {
	case domainhistory.RecordKindCreate:
		if record.Entry == nil {
			return wire.Record{}, fmt.Errorf("%w: create record has no entry", historysync.ErrRemote)
		}
		if record.HistoryID != record.Entry.ID {
			return wire.Record{}, fmt.Errorf("%w: create record id does not match its entry", historysync.ErrRemote)
		}
		history, err := historyFromEntry(*record.Entry)
		if err != nil {
			return wire.Record{}, err
		}
		plaintext = wire.HistoryRecord{Kind: wire.HistoryRecordCreate, History: &history}
	case domainhistory.RecordKindDelete:
		plaintext = wire.HistoryRecord{Kind: wire.HistoryRecordDelete, DeleteID: record.HistoryID}
	default:
		return wire.Record{}, fmt.Errorf("%w: unknown record kind", historysync.ErrRemote)
	}

	payload, err := plaintext.MarshalBinary(wire.Version2)
	if err != nil {
		return wire.Record{}, fmt.Errorf("%w: record payload could not be encoded", historysync.ErrRemote)
	}
	encrypted, err := outer.Encrypt(payload, key)
	if err != nil {
		return wire.Record{}, fmt.Errorf("%w: record could not be encrypted", historysync.ErrRemote)
	}
	return encrypted, nil
}

// historyFromEntry builds the pinned V2 payload. The author is always present
// in V2, as the pinned client writes it (possibly empty); intent, shell, and
// author kind stay absent when the journal has no value.
func historyFromEntry(entry domainhistory.Entry) (wire.History, error) {
	if !entry.Valid() {
		return wire.History{}, fmt.Errorf("%w: entry is incomplete", historysync.ErrRemote)
	}
	timestamp, ok := unixNanos(entry.RecordedAt)
	if !ok {
		return wire.History{}, fmt.Errorf("%w: entry timestamp is out of range", historysync.ErrRemote)
	}
	duration := int64(entry.Duration)
	if duration < 0 {
		duration = 0
	}
	exit := int64(-1)
	if entry.HasExitCode {
		exit = int64(entry.ExitCode)
	}

	author := entry.Author
	history := wire.History{
		ID:        entry.ID,
		Timestamp: timestamp,
		Duration:  duration,
		Exit:      exit,
		Command:   entry.Command,
		CWD:       entry.CWD,
		Session:   entry.SessionUUID,
		Origin:    entry.Origin,
		Author:    &author,
	}
	if !entry.DeletedAt.IsZero() {
		deletedAt, ok := unixNanos(entry.DeletedAt)
		if !ok {
			return wire.History{}, fmt.Errorf("%w: entry deletion time is out of range", historysync.ErrRemote)
		}
		history.DeletedAt = &deletedAt
	}
	if entry.Intent != "" {
		intent := entry.Intent
		history.Intent = &intent
	}
	if entry.Shell != "" {
		shell := entry.Shell
		history.Shell = &shell
	}
	switch entry.AuthorKind {
	case domainhistory.AuthorKindUser:
		kind := wire.AuthorKindUser
		history.AuthorKind = &kind
	case domainhistory.AuthorKindAgent:
		kind := wire.AuthorKindAgent
		history.AuthorKind = &kind
	}
	return history, nil
}

// unixNanos converts a domain time to the wire's Unix nanoseconds. A zero time
// and a time before the epoch or beyond int64 nanoseconds are rejected.
func unixNanos(value time.Time) (uint64, bool) {
	if value.IsZero() {
		return 0, false
	}
	seconds := value.Unix()
	if seconds < 0 || seconds > math.MaxInt64/1_000_000_000 {
		return 0, false
	}
	nanos := uint64(seconds)*1_000_000_000 + uint64(value.Nanosecond())
	if nanos > math.MaxInt64 {
		return 0, false
	}
	return nanos, true
}
