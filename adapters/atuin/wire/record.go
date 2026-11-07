package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

// Record is one Atuin record as it travels over the sync wire. The encrypted
// Data payload is opaque to the record layer; Tag says which payload decoder
// applies. Timestamp is the record creation time in Unix nanoseconds.
type Record struct {
	ID         string
	Index      uint64
	SeriesHost string
	Timestamp  uint64
	Version    string
	Tag        string
	Data       EncryptedData
}

// Assertion returns the implicit assertion bytes the pinned client binds to a
// record: serde's compact JSON for AdditionalData, with the frozen field order
// id, idx, version, tag, host.
func (record Record) Assertion() ([]byte, error) {
	identifier, ok := canonicalUUID(record.ID)
	if !ok {
		return nil, fmt.Errorf("%w: record id is not a UUID", ErrMalformed)
	}
	host, ok := canonicalUUID(record.SeriesHost)
	if !ok {
		return nil, fmt.Errorf("%w: series host is not a UUID", ErrMalformed)
	}
	if !utf8.ValidString(record.Version) || !utf8.ValidString(record.Tag) {
		return nil, fmt.Errorf("%w: record metadata is not valid UTF-8", ErrMalformed)
	}

	var assertion bytes.Buffer
	assertion.WriteString(`{"id":`)
	appendJSONString(&assertion, identifier)
	assertion.WriteString(`,"idx":`)
	assertion.WriteString(strconv.FormatUint(record.Index, 10))
	assertion.WriteString(`,"version":`)
	appendJSONString(&assertion, record.Version)
	assertion.WriteString(`,"tag":`)
	appendJSONString(&assertion, record.Tag)
	assertion.WriteString(`,"host":`)
	appendJSONString(&assertion, host)
	assertion.WriteByte('}')
	return assertion.Bytes(), nil
}

// Encrypt encrypts plaintext with a fresh CEK and binds the record metadata as
// the PASETO implicit assertion.
func (record Record) Encrypt(plaintext []byte, key Key) (Record, error) {
	assertion, err := record.Assertion()
	if err != nil {
		return record, err
	}
	data, err := EncryptData(plaintext, assertion, key)
	if err != nil {
		return record, err
	}
	record.Data = data
	return record, nil
}

// Decrypt authenticates the record metadata and decrypts its payload. A wrong
// key or tampered metadata returns ErrKeyMismatch.
func (record Record) Decrypt(key Key) ([]byte, error) {
	assertion, err := record.Assertion()
	if err != nil {
		return nil, err
	}
	return DecryptData(record.Data, assertion, key)
}

// wireHost and wireRecord freeze the JSON field order and names of the sync
// wire format.
type wireHost struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wireRecord struct {
	ID        string        `json:"id"`
	Index     uint64        `json:"idx"`
	Host      wireHost      `json:"host"`
	Timestamp uint64        `json:"timestamp"`
	Version   string        `json:"version"`
	Tag       string        `json:"tag"`
	Data      EncryptedData `json:"data"`
}

// MarshalJSON writes the canonical wire JSON: field order id, idx, host,
// timestamp, version, tag, data; host always carries its empty legacy name.
func (record Record) MarshalJSON() ([]byte, error) {
	identifier, ok := canonicalUUID(record.ID)
	if !ok {
		return nil, fmt.Errorf("%w: record id is not a UUID", ErrMalformed)
	}
	host, ok := canonicalUUID(record.SeriesHost)
	if !ok {
		return nil, fmt.Errorf("%w: series host is not a UUID", ErrMalformed)
	}
	wire := wireRecord{
		ID:        identifier,
		Index:     record.Index,
		Host:      wireHost{ID: host, Name: ""},
		Timestamp: record.Timestamp,
		Version:   record.Version,
		Tag:       record.Tag,
		Data:      record.Data,
	}
	// Disable HTML escaping so the bytes match serde_json rather than Go's
	// default < / > / & form.
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(wire); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// UnmarshalJSON reads a record defensively: required fields must be present,
// unknown fields are ignored, and the historical raw/cek aliases are accepted.
func (record *Record) UnmarshalJSON(input []byte) error {
	var fields struct {
		ID    *string `json:"id"`
		Index *uint64 `json:"idx"`
		Host  *struct {
			ID *string `json:"id"`
		} `json:"host"`
		Timestamp *uint64        `json:"timestamp"`
		Version   *string        `json:"version"`
		Tag       *string        `json:"tag"`
		Data      *EncryptedData `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(input))
	if err := decoder.Decode(&fields); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON after record", ErrMalformed)
	}
	if fields.ID == nil || fields.Index == nil || fields.Host == nil || fields.Host.ID == nil ||
		fields.Timestamp == nil || fields.Version == nil || fields.Tag == nil || fields.Data == nil {
		return fmt.Errorf("%w: record is missing a required field", ErrMalformed)
	}
	identifier, ok := canonicalUUID(*fields.ID)
	if !ok {
		return fmt.Errorf("%w: record id is not a UUID", ErrMalformed)
	}
	host, ok := canonicalUUID(*fields.Host.ID)
	if !ok {
		return fmt.Errorf("%w: series host is not a UUID", ErrMalformed)
	}
	record.ID = identifier
	record.Index = *fields.Index
	record.SeriesHost = host
	record.Timestamp = *fields.Timestamp
	record.Version = *fields.Version
	record.Tag = *fields.Tag
	record.Data = *fields.Data
	return nil
}

// appendJSONString writes value as a JSON string using serde_json's escaping:
// no HTML escaping, no U+2028/U+2029 escaping, and lowercase \u00xx for other
// control characters.
func appendJSONString(output *bytes.Buffer, value string) {
	output.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			output.WriteString(`\"`)
		case '\\':
			output.WriteString(`\\`)
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if character < 0x20 {
				const hexDigits = "0123456789abcdef"
				output.WriteString(`\u00`)
				output.WriteByte(hexDigits[byte(character)>>4])
				output.WriteByte(hexDigits[byte(character)&0x0f])
				continue
			}
			output.WriteRune(character)
		}
	}
	output.WriteByte('"')
}
