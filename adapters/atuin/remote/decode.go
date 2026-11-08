package remote

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mslxl/natcatty/adapters/atuin/wire"
	historysync "github.com/mslxl/natcatty/application/historysync"
	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// decodePage turns one wire page into domain sync records. The outer record
// timestamp is the record's creation time and becomes DeletedAt for a
// tombstone; a history payload timestamp becomes the entry's RecordedAt.
func decodePage(records []wire.Record, seriesHost string, start int64, key wire.Key) (historysync.Page, error) {
	page := historysync.Page{}
	previous := int64(-1)
	next := int64(0)
	for _, wireRecord := range records {
		if wireRecord.Tag != domainhistory.RecordTagHistory {
			// Unknown tags are out of scope: entirely ignore them rather than
			// decrypting or rejecting a payload this client does not own.
			continue
		}
		if wireRecord.SeriesHost != seriesHost {
			return historysync.Page{}, fmt.Errorf("%w: response mixed series hosts", historysync.ErrRemote)
		}
		// Page.NextIndex is Index+1, so MaxInt64 cannot be represented without
		// overflowing. Reject the whole page rather than report a negative
		// next index.
		if wireRecord.Index >= math.MaxInt64 {
			return historysync.Page{}, fmt.Errorf("%w: record index is out of range", historysync.ErrRemote)
		}
		index := int64(wireRecord.Index)
		if index < start || previous >= 0 && index <= previous {
			return historysync.Page{}, fmt.Errorf("%w: records are out of order", historysync.ErrRemote)
		}
		record, err := decodeRecord(wireRecord, key)
		if err != nil {
			return historysync.Page{}, err
		}
		previous = index
		next = index + 1
		page.Records = append(page.Records, record)
	}
	page.NextIndex = next
	return page, nil
}

// decodeRecord decrypts and decodes one history record. A wrong key reports
// ErrKeyMismatch; a malformed payload is rejected, never skipped.
func decodeRecord(record wire.Record, key wire.Key) (historysync.SyncRecord, error) {
	version, err := wire.ParseVersion(record.Version)
	if err != nil {
		return historysync.SyncRecord{}, fmt.Errorf("%w: unsupported record version", historysync.ErrRemote)
	}
	plaintext, err := record.Decrypt(key)
	if err != nil {
		if errors.Is(err, wire.ErrKeyMismatch) {
			return historysync.SyncRecord{}, historysync.ErrKeyMismatch
		}
		return historysync.SyncRecord{}, fmt.Errorf("%w: record could not be decrypted", historysync.ErrRemote)
	}
	wrapper, err := wire.UnmarshalHistoryRecord(record.Version, plaintext)
	if err != nil {
		if errors.Is(err, wire.ErrUnsupportedVersion) {
			return historysync.SyncRecord{}, fmt.Errorf("%w: unsupported history version", historysync.ErrRemote)
		}
		return historysync.SyncRecord{}, fmt.Errorf("%w: malformed history payload", historysync.ErrRemote)
	}
	recordedAt, ok := timeFromUnixNanos(record.Timestamp)
	if !ok {
		return historysync.SyncRecord{}, fmt.Errorf("%w: record timestamp is out of range", historysync.ErrRemote)
	}

	syncRecord := historysync.SyncRecord{
		ID:         record.ID,
		SeriesHost: record.SeriesHost,
		Tag:        record.Tag,
		Index:      int64(record.Index),
		RecordedAt: recordedAt,
		Version:    versionNumber(version),
	}
	switch wrapper.Kind {
	case wire.HistoryRecordCreate:
		entry, err := entryFromWire(*wrapper.History)
		if err != nil {
			return historysync.SyncRecord{}, err
		}
		syncRecord.Kind = domainhistory.RecordKindCreate
		syncRecord.HistoryID = entry.ID
		syncRecord.Entry = &entry
	case wire.HistoryRecordDelete:
		syncRecord.Kind = domainhistory.RecordKindDelete
		syncRecord.HistoryID = wrapper.DeleteID
		// The delete wrapper carries only the history id. Atuin's outer record
		// timestamp is when the tombstone was written, which is the deletion
		// time Netcatty records.
		syncRecord.DeletedAt = recordedAt
	default:
		return historysync.SyncRecord{}, fmt.Errorf("%w: unknown history record kind", historysync.ErrRemote)
	}
	if !syncRecord.Valid() {
		return historysync.SyncRecord{}, fmt.Errorf("%w: decoded record is incomplete", historysync.ErrRemote)
	}
	return syncRecord, nil
}

func versionNumber(version wire.Version) int {
	switch version {
	case wire.Version1:
		return 1
	case wire.Version2:
		return 2
	default:
		return 0
	}
}

// entryFromWire projects one Atuin history payload onto the local journal.
// The record is never re-uploaded; this is a local view, so it follows the
// journal's trimming, bounding, and provenance rules.
func entryFromWire(history wire.History) (domainhistory.Entry, error) {
	recordedAt, ok := timeFromUnixNanos(history.Timestamp)
	if !ok {
		return domainhistory.Entry{}, fmt.Errorf("%w: history timestamp is out of range", historysync.ErrRemote)
	}
	command := strings.TrimSpace(history.Command)
	if len(command) > domainhistory.MaxCommandLength {
		command = command[:domainhistory.MaxCommandLength]
	}
	if command == "" {
		return domainhistory.Entry{}, fmt.Errorf("%w: history record has an empty command", historysync.ErrRemote)
	}
	origin := strings.TrimSpace(history.Origin)
	if origin == "" {
		origin = "unknown"
	}

	entry := domainhistory.Entry{
		ID:          history.ID,
		SessionUUID: canonicalSession(history.Session),
		Scope:       "remote:" + origin,
		Command:     command,
		CWD:         strings.TrimSpace(history.CWD),
		Shell:       optionalString(history.Shell),
		RecordedAt:  recordedAt,
		Origin:      origin,
		Intent:      optionalString(history.Intent),
		Source:      domainhistory.SourceAtuinSync,
	}
	if history.Duration >= 0 {
		entry.Duration = time.Duration(history.Duration)
		entry.FinishedAt = recordedAt.Add(entry.Duration)
	}
	if history.Exit >= 0 && history.Exit <= math.MaxInt {
		entry.ExitCode = int(history.Exit)
		entry.HasExitCode = true
	}
	if history.DeletedAt != nil && *history.DeletedAt > 0 {
		deletedAt, ok := timeFromUnixNanos(*history.DeletedAt)
		if !ok {
			return domainhistory.Entry{}, fmt.Errorf("%w: history deletion time is out of range", historysync.ErrRemote)
		}
		entry.DeletedAt = deletedAt
	}
	if history.Author != nil {
		entry.Author = strings.TrimSpace(*history.Author)
	} else {
		// The pinned decoder resolves a v0/v1 record with no author to the
		// origin's user; the domain keeps the same derivation.
		entry.Author = domainhistory.AuthorFromOrigin(entry.Origin)
	}
	if history.AuthorKind != nil {
		switch *history.AuthorKind {
		case wire.AuthorKindUser:
			entry.AuthorKind = domainhistory.AuthorKindUser
		case wire.AuthorKindAgent:
			entry.AuthorKind = domainhistory.AuthorKindAgent
		}
	}
	return entry, nil
}

func canonicalSession(value string) string {
	trimmed := strings.TrimSpace(value)
	if canonical, ok := wire.CanonicalUUID(trimmed); ok {
		return canonical
	}
	return trimmed
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

// timeFromUnixNanos converts a wire nanosecond timestamp. Zero is not a
// meaningful record or history time and is rejected rather than shown as 1970.
func timeFromUnixNanos(value uint64) (time.Time, bool) {
	if value == 0 || value > math.MaxInt64 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(value)).UTC(), true
}
