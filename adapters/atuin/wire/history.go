package wire

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// History versions understood by the pinned client. The current client writes
// v2; v0 and v1 are decoded for compatibility with records from older writers.
type Version string

const (
	Version0 Version = "v0"
	Version1 Version = "v1"
	Version2 Version = "v2"
)

// ParseVersion validates an Atuin record version string. Only versions the
// pinned history decoder accepts are allowed.
func ParseVersion(value string) (Version, error) {
	switch Version(value) {
	case Version0, Version1, Version2:
		return Version(value), nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedVersion, value)
	}
}

func (version Version) number() uint16 {
	switch version {
	case Version1:
		return 1
	case Version2:
		return 2
	default:
		return 0
	}
}

func (version Version) minFields() uint32 {
	switch version {
	case Version0:
		return 9
	case Version1:
		return 10
	default:
		return 12
	}
}

func (version Version) maxFields() (uint32, bool) {
	switch version {
	case Version0:
		return 9, true
	case Version1:
		return 11, true
	default:
		return 0, false
	}
}

// writeOptionalAuthorKind writes the compact uint8 form rust-rmp uses for the
// V2 author kind field.
func (e *encoder) writeOptionalAuthorKind(value *AuthorKind) {
	if value == nil {
		e.writeNil()
		return
	}
	e.writeUInt8Compact(uint8(*value))
}

// History is one Atuin V2 history entry. It is the MessagePack payload inside
// a create record. Timestamps are Unix nanoseconds. DeletedAt is nil for a
// live entry; pointer fields distinguish an absent optional field from an
// empty string.
//
// The pointer fields preserve the bytes as they appear on the wire. In
// particular a V0 record has no author field, and a V1/V2 record may encode
// nil, so Author is nil there. The pinned client's History type resolves a
// missing author to the origin user while decoding; the adapter applies that
// derivation when it projects this wire value onto a domain Entry, keeping the
// codec lossless for re-encoding.
type History struct {
	ID        string
	Timestamp uint64
	Duration  int64
	Exit      int64
	Command   string
	CWD       string
	Session   string
	Origin    string

	DeletedAt *uint64
	// Author is optional in v1 and always present in v2, where the pinned
	// client writes a string (possibly empty).
	Author     *string
	Intent     *string
	Shell      *string
	AuthorKind *AuthorKind
}

// AuthorKind is the optional V2 author kind. The pinned client drops values it
// does not recognise, so a decoded value is always one of the two constants.
type AuthorKind uint8

const (
	// AuthorKindUser marks a command a human ran.
	AuthorKindUser AuthorKind = 1
	// AuthorKindAgent marks a command an AI agent ran.
	AuthorKindAgent AuthorKind = 2
)

// MarshalBinary encodes the entry with the requested version. v0 and v1 reject
// fields those versions cannot represent instead of silently dropping them.
func (history History) MarshalBinary(version Version) ([]byte, error) {
	identifier, ok := simpleUUID(history.ID)
	if !ok {
		return nil, fmt.Errorf("%w: history id is not a UUID", ErrMalformed)
	}
	for name, value := range map[string]string{
		"command": history.Command,
		"cwd":     history.CWD,
		"session": history.Session,
		"origin":  history.Origin,
	} {
		if !utf8.ValidString(value) {
			return nil, fmt.Errorf("%w: %s is not valid UTF-8", ErrMalformed, name)
		}
	}
	if history.Author != nil && !utf8.ValidString(*history.Author) ||
		history.Intent != nil && !utf8.ValidString(*history.Intent) ||
		history.Shell != nil && !utf8.ValidString(*history.Shell) {
		return nil, fmt.Errorf("%w: optional string is not valid UTF-8", ErrMalformed)
	}
	switch version {
	case Version0:
		if history.Author != nil || history.Intent != nil || history.Shell != nil ||
			history.AuthorKind != nil {
			return nil, fmt.Errorf("%w: v0 cannot carry author, intent, shell, or author kind", ErrMalformed)
		}
	case Version1:
		if history.Shell != nil || history.AuthorKind != nil {
			return nil, fmt.Errorf("%w: v1 cannot carry shell or author kind", ErrMalformed)
		}
	case Version2:
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedVersion, version)
	}

	encoder := &encoder{}
	encoder.writeU16(version.number())

	fieldCount := uint32(9)
	switch version {
	case Version1:
		fieldCount = 10
		if history.Intent != nil {
			fieldCount = 11
		}
	case Version2:
		fieldCount = 13
	}
	encoder.writeArrayLen(fieldCount)

	encoder.writeStr(identifier)
	encoder.writeU64(history.Timestamp)
	encoder.writeSInt(history.Duration)
	encoder.writeSInt(history.Exit)
	encoder.writeStr(history.Command)
	encoder.writeStr(history.CWD)
	encoder.writeStr(history.Session)
	encoder.writeStr(history.Origin)
	encoder.writeOptionalU64(history.DeletedAt)
	if version >= Version1 {
		encoder.writeOptionalStr(history.Author)
	}
	if version == Version1 && history.Intent != nil {
		encoder.writeOptionalStr(history.Intent)
	}
	if version >= Version2 {
		encoder.writeOptionalStr(history.Intent)
		encoder.writeOptionalStr(history.Shell)
		encoder.writeOptionalAuthorKind(history.AuthorKind)
	}
	return encoder.buf, nil
}

// UnmarshalHistory decodes a history payload. version is the record's outer
// version string. Unknown trailing fields on a v2 record are ignored, as the
// pinned decoder ignores them; v0 and v1 reject trailing bytes.
func UnmarshalHistory(versionName string, data []byte) (History, error) {
	var history History
	version, err := ParseVersion(versionName)
	if err != nil {
		return history, err
	}
	decoder := newDecoder(data)

	encodedVersion, err := decoder.readU16Explicit()
	if err != nil {
		return history, err
	}
	if encodedVersion != version.number() {
		return history, fmt.Errorf("%w: record says %q but payload says v%d",
			ErrMalformed, version, encodedVersion)
	}

	fieldCount, err := decoder.readArrayLen()
	if err != nil {
		return history, err
	}
	if fieldCount < version.minFields() {
		return history, fmt.Errorf("%w: %d fields for %s", ErrMalformed, fieldCount, version)
	}
	if maximum, bounded := version.maxFields(); bounded && fieldCount > maximum {
		return history, fmt.Errorf("%w: %d fields for %s", ErrMalformed, fieldCount, version)
	}

	if history.ID, err = readHistoryID(decoder); err != nil {
		return history, err
	}
	if history.Timestamp, err = decoder.readU64Explicit(); err != nil {
		return history, err
	}
	if history.Duration, err = decoder.readInt64(); err != nil {
		return history, err
	}
	if history.Exit, err = decoder.readInt64(); err != nil {
		return history, err
	}
	if history.Command, err = decoder.readStr(); err != nil {
		return history, err
	}
	if history.CWD, err = decoder.readStr(); err != nil {
		return history, err
	}
	if history.Session, err = decoder.readStr(); err != nil {
		return history, err
	}
	if history.Origin, err = decoder.readStr(); err != nil {
		return history, err
	}
	if history.DeletedAt, err = decoder.readOptionalU64(); err != nil {
		return history, err
	}
	if version >= Version1 {
		if history.Author, err = decoder.readOptionalStr(); err != nil {
			return history, err
		}
	}
	if version >= Version2 || version == Version1 && fieldCount > version.minFields() {
		if history.Intent, err = decoder.readOptionalStr(); err != nil {
			return history, err
		}
	}
	if version >= Version2 {
		if history.Shell, err = decoder.readOptionalStr(); err != nil {
			return history, err
		}
		if fieldCount >= 13 {
			var kind *uint8
			if kind, err = decoder.readOptionalUint8(); err != nil {
				return history, err
			}
			if kind != nil {
				switch candidate := AuthorKind(*kind); candidate {
				case AuthorKindUser, AuthorKindAgent:
					history.AuthorKind = &candidate
				default:
					// The pinned enum decodes an unknown in-range value as
					// "not stated" rather than an error.
				}
			}
		}
	}
	if version != Version2 && decoder.remaining() != 0 {
		return history, fmt.Errorf("%w: trailing bytes after %s history", ErrMalformed, version)
	}
	return history, nil
}

func readHistoryID(decoder *decoder) (string, error) {
	value, err := decoder.readStr()
	if err != nil {
		return "", err
	}
	identifier, ok := canonicalUUID(value)
	if !ok {
		return "", fmt.Errorf("%w: history id is not a UUID", ErrMalformed)
	}
	return identifier, nil
}

// HistoryRecordKind is the first byte of a history record wrapper.
type HistoryRecordKind uint8

const (
	// HistoryRecordCreate wraps a serialized History payload.
	HistoryRecordCreate HistoryRecordKind = 0
	// HistoryRecordDelete is a tombstone naming the deleted history id.
	HistoryRecordDelete HistoryRecordKind = 1
)

// HistoryRecord is the plaintext inside a history-tag record: either a create
// wrapper around a History payload or a delete wrapper naming a history id.
type HistoryRecord struct {
	Kind     HistoryRecordKind
	History  *History
	DeleteID string
}

// MarshalBinary encodes the wrapper. Create embeds the history payload at the
// requested version; delete ignores version.
func (record HistoryRecord) MarshalBinary(version Version) ([]byte, error) {
	encoder := &encoder{}
	switch record.Kind {
	case HistoryRecordCreate:
		if record.History == nil {
			return nil, fmt.Errorf("%w: create record has no history", ErrMalformed)
		}
		payload, err := record.History.MarshalBinary(version)
		if err != nil {
			return nil, err
		}
		encoder.writeU8(uint8(HistoryRecordCreate))
		encoder.writeBin(payload)
	case HistoryRecordDelete:
		identifier, ok := simpleUUID(record.DeleteID)
		if !ok {
			return nil, fmt.Errorf("%w: delete id is not a UUID", ErrMalformed)
		}
		encoder.writeU8(uint8(HistoryRecordDelete))
		encoder.writeStr(identifier)
	default:
		return nil, fmt.Errorf("%w: unknown history record kind %d", ErrMalformed, record.Kind)
	}
	return encoder.buf, nil
}

// UnmarshalHistoryRecord decodes a create or delete wrapper. version is the
// outer record version.
func UnmarshalHistoryRecord(version string, data []byte) (HistoryRecord, error) {
	var record HistoryRecord
	decoder := newDecoder(data)
	kind, err := decoder.readU8Explicit()
	if err != nil {
		return record, err
	}
	switch HistoryRecordKind(kind) {
	case HistoryRecordCreate:
		record.Kind = HistoryRecordCreate
		// The pinned decoder consumes the bin length but passes the rest of
		// the buffer to History::deserialize; it never checks that the length
		// matches. Mirror that behavior.
		if _, err := decoder.readBinLen(); err != nil {
			return record, err
		}
		history, err := UnmarshalHistory(version, decoder.data[decoder.pos:])
		if err != nil {
			return record, err
		}
		record.History = &history
		return record, nil
	case HistoryRecordDelete:
		record.Kind = HistoryRecordDelete
		identifier, err := decoder.readStr()
		if err != nil {
			return record, err
		}
		if decoder.remaining() != 0 {
			return record, fmt.Errorf("%w: trailing bytes after delete record", ErrMalformed)
		}
		canonical, ok := canonicalUUID(identifier)
		if !ok {
			return record, fmt.Errorf("%w: delete id is not a UUID", ErrMalformed)
		}
		record.DeleteID = canonical
		return record, nil
	default:
		return record, fmt.Errorf("%w: unknown history record kind %d", ErrMalformed, kind)
	}
}

// CanonicalUUID returns the lowercase hyphenated form of a UUID value the
// Atuin wire accepts (hyphenated, simple, braced, or URN form). It reports
// false for any other value. The sync adapter uses it to validate a series
// host before a request and to normalize a session identity decoded from a
// record; it is the same parser the record and history codecs use.
func CanonicalUUID(value string) (string, bool) {
	return canonicalUUID(value)
}

// canonicalUUID accepts the hyphenated and simple forms Uuid::parse_str
// accepts, and returns the lowercase hyphenated form serde emits.
func canonicalUUID(value string) (string, bool) {
	simple, ok := simpleUUID(value)
	if !ok {
		return "", false
	}
	return simple[:8] + "-" + simple[8:12] + "-" + simple[12:16] + "-" +
		simple[16:20] + "-" + simple[20:], true
}

// simpleUUID returns the lowercase 32-hex form used by the Atuin history
// payload and delete record.
func simpleUUID(value string) (string, bool) {
	// Uuid::parse_str also accepts the braced and URN forms; serde emits the
	// canonical hyphenated form.
	if len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' {
		value = value[1 : len(value)-1]
	} else if len(value) >= 9 && strings.EqualFold(value[:9], "urn:uuid:") {
		value = value[9:]
	}
	if len(value) == 36 {
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return "", false
		}
		value = value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:]
	}
	if len(value) != 32 {
		return "", false
	}
	compact := strings.ToLower(value)
	for index := 0; index < len(compact); index++ {
		character := compact[index]
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'f' {
			continue
		}
		return "", false
	}
	return compact, true
}
