// Package plugindata owns the host-opaque structural subset that plugin-defined
// fields use on Vault records. It validates shape, size, nesting depth, UTF-8,
// and namespace identity, but never interprets a field's meaning, resolves a
// path or URL, or dereferences a secret. Hosts and DataProfiles carry a Map
// keyed by Plugin ID; the host preserves unknown namespaces and unknown field
// names verbatim.
package plugindata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxRecordBytes bounds one record's complete pluginData map. The limit is
	// measured on the canonical JSON encoding with HTML escaping disabled.
	MaxRecordBytes = 64 << 10
	// MaxFieldBytes bounds one field value, measured on its raw JSON bytes.
	MaxFieldBytes = 16 << 10
	// MaxDepth bounds container nesting inside one field value. Scalars are
	// depth zero; each object or array adds one.
	MaxDepth = 32
	// MaxFieldKeyBytes bounds one field name.
	MaxFieldKeyBytes = 256
	// MaxSchemaVersion bounds a namespace schema version so a malformed number
	// cannot overflow int arithmetic.
	MaxSchemaVersion = 1 << 30
	// MaxDiagnosticPluginIDBytes bounds a diagnostic PluginID. An imported
	// namespace key is attacker-controlled; a bounded UTF-8 safe prefix keeps a
	// malformed probe from inflating the quarantine table.
	MaxDiagnosticPluginIDBytes = 64
	// MaxDiagnosticDetailBytes bounds a diagnostic Detail (a field name or a
	// short hint). A Detail never carries a field value.
	MaxDiagnosticDetailBytes = 256
	// MaxDiagnosticRecordIDBytes bounds a diagnostic RecordID. A record ID is
	// also attacker-controlled when an imported payload or snapshot carries it;
	// a bounded UTF-8 safe prefix keeps a malformed probe from inflating the
	// quarantine table or the sync report.
	MaxDiagnosticRecordIDBytes = 256
	// JSONField is the record property a Host or DataProfile carries.
	JSONField = "pluginData"
)

// RecordKind names the Vault record family a namespace belongs to.
type RecordKind string

const (
	// RecordHost is a Host record.
	RecordHost RecordKind = "host"
	// RecordDataProfile is a DataProfile record.
	RecordDataProfile RecordKind = "data_profile"
)

// State is the read model of one namespace.
type State string

const (
	// StateActive means the owning plugin is active and the namespace schema
	// version is understood; the provider may edit it.
	StateActive State = "active"
	// StateDormant means the owning plugin is missing, disabled, uninstalled,
	// or has an unresolved secret; the data is retained and read-only.
	StateDormant State = "dormant"
	// StateNewer means the namespace uses a schema version newer than the
	// active provider declares; the data is retained and read-only and is
	// reported in diagnostics.
	StateNewer State = "newer-schema"
	// StateQuarantined means an imported namespace was structurally invalid
	// and was retained in quarantine rather than written into the record.
	StateQuarantined State = "quarantined"
)

// Reason is the closed diagnostic vocabulary. It never carries field content.
type Reason string

const (
	// ReasonProviderUnavailable reports a namespace whose plugin is not active.
	ReasonProviderUnavailable Reason = "provider-unavailable"
	// ReasonSchemaNewer reports a namespace newer than the active provider.
	ReasonSchemaNewer Reason = "schema-newer"
	// ReasonUnresolvedSecret reports a secret reference that cannot be
	// resolved on this device; the reference is retained, never emptied.
	ReasonUnresolvedSecret Reason = "unresolved-secret"
	// ReasonNamespace reports a namespace key that is not a Plugin ID or an
	// entry that is not an object.
	ReasonNamespace Reason = "invalid-namespace"
	// ReasonSchemaVersion reports a missing or out-of-range schema version.
	ReasonSchemaVersion Reason = "invalid-schema-version"
	// ReasonFieldKey reports an empty or oversized field name.
	ReasonFieldKey Reason = "invalid-field-key"
	// ReasonFieldSize reports a field value above MaxFieldBytes.
	ReasonFieldSize Reason = "oversized-field"
	// ReasonRecordSize reports a map above MaxRecordBytes.
	ReasonRecordSize Reason = "oversized-record"
	// ReasonDepth reports a value nested deeper than MaxDepth.
	ReasonDepth Reason = "excessive-depth"
	// ReasonUTF8 reports invalid UTF-8 in a key or value.
	ReasonUTF8 Reason = "invalid-utf8"
	// ReasonJSON reports a value that is not valid JSON from the structural
	// subset.
	ReasonJSON Reason = "invalid-json"
	// ReasonRecord reports a record whose pluginData container is not a JSON
	// object.
	ReasonRecord Reason = "invalid-record"
)

// Diagnostic reports one namespace's state without exposing its content.
type Diagnostic struct {
	RecordKind    RecordKind `json:"recordKind"`
	RecordID      string     `json:"recordId"`
	PluginID      string     `json:"pluginId"`
	SchemaVersion int        `json:"schemaVersion,omitempty"`
	Fields        int        `json:"fields,omitempty"`
	State         State      `json:"state"`
	Reason        Reason     `json:"reason,omitempty"`
	// Detail is a short machine-readable hint, such as a field name or byte
	// count. It never contains a field value.
	Detail string `json:"detail,omitempty"`
}

// Quarantine is one imported namespace that was structurally rejected. Raw is
// the original namespace bytes, bounded by MaxRecordBytes; it is diagnostic
// material and is never merged back into a record automatically.
type Quarantine struct {
	PluginID      string
	SchemaVersion int
	Fields        int
	SizeBytes     int
	Reason        Reason
	Detail        string
	Raw           []byte
}

// Namespace is one plugin's entry in a record's pluginData map.
type Namespace struct {
	// SchemaVersion is the provider's declared schema version. It is positive.
	SchemaVersion int `json:"schemaVersion"`
	// Fields maps provider-defined field names to structural JSON values. The
	// host preserves unknown field names.
	Fields map[string]json.RawMessage `json:"fields"`
	// Extra preserves unknown top-level namespace properties so a newer
	// provider's entry survives a round trip through an older host.
	Extra map[string]json.RawMessage `json:"-"`
}

// Map is a record's host-opaque pluginData, keyed by Plugin ID.
type Map map[string]Namespace

// ValidationError reports one rejected namespace.
type ValidationError struct {
	PluginID string
	Field    string
	Reason   Reason
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("plugindata: namespace %q field %q is %s", e.PluginID, e.Field, e.Reason)
	}
	return fmt.Sprintf("plugindata: namespace %q is %s", e.PluginID, e.Reason)
}

// ErrRecordTooLarge reports a record whose valid namespaces still exceed
// MaxRecordBytes. A single namespace is too large is handled as a quarantine.
var ErrRecordTooLarge = errors.New("plugindata: record exceeds the size limit")

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidPluginID reports whether id has the shared Plugin ID shape: lowercase
// letters, digits, dot, underscore, or hyphen, at most 64 characters, with
// exactly one separating dot.
func ValidPluginID(id string) bool {
	return pluginIDPattern.MatchString(id) && strings.Count(id, ".") == 1
}

// EffectiveSchemaVersion returns the schema version a provider declares,
// applying the default of 1 for an omitted declaration.
func EffectiveSchemaVersion(declared int) int {
	if declared <= 0 {
		return 1
	}
	return declared
}

// UnmarshalJSON decodes one namespace without interpreting the fields. An
// unknown top-level property is retained in Extra.
func (n *Namespace) UnmarshalJSON(data []byte) error {
	raw, err := decodeObject(data)
	if err != nil {
		return err
	}
	var version int
	if encoded, ok := raw["schemaVersion"]; ok {
		if err := json.Unmarshal(encoded, &version); err != nil {
			return fmt.Errorf("schemaVersion is not an integer")
		}
		delete(raw, "schemaVersion")
	}
	var fields map[string]json.RawMessage
	if encoded, ok := raw["fields"]; ok {
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return fmt.Errorf("fields is not an object")
		}
		delete(raw, "fields")
	}
	n.SchemaVersion = version
	n.Fields = fields
	if len(raw) > 0 {
		n.Extra = raw
	} else {
		n.Extra = nil
	}
	return nil
}

// MarshalJSON encodes one namespace. Unknown top-level properties are written
// back unchanged.
func (n Namespace) MarshalJSON() ([]byte, error) {
	raw := make(map[string]json.RawMessage, len(n.Extra)+2)
	for key, value := range n.Extra {
		raw[key] = value
	}
	version, err := json.Marshal(n.SchemaVersion)
	if err != nil {
		return nil, err
	}
	raw["schemaVersion"] = version
	fields := n.Fields
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	// Encode the field map without HTML escaping, then let the outer
	// no-escape encoder embed the already canonical value. Using json.Marshal
	// here would bake <, >, and & into the raw message, and the
	// outer encoder cannot undo escapes that are already part of a
	// json.RawMessage. That expansion would make a legal field exceed the
	// 16 KiB limit after a Save/Get round trip and silently quarantine it.
	encodedFields, err := marshalNoEscape(fields)
	if err != nil {
		return nil, err
	}
	raw["fields"] = encodedFields
	return marshalNoEscape(raw)
}

// decodeObject decodes a JSON object into raw properties. It rejects a
// non-object, invalid JSON, and invalid UTF-8.
func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("value is not valid UTF-8")
	}
	return decodeRawObject(data)
}

// decodeRawObject decodes a JSON object without a container-wide UTF-8 check so
// a single malformed namespace can be quarantined individually.
func decodeRawObject(data []byte) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("value is not an object")
	}
	return raw, nil
}

// DecodeNamespace decodes one raw namespace entry strictly enough to retain
// unknown properties. Structural limits are checked by ValidateNamespace.
func DecodeNamespace(data []byte) (Namespace, error) {
	var namespace Namespace
	if err := namespace.UnmarshalJSON(data); err != nil {
		return Namespace{}, err
	}
	return namespace, nil
}

// ValidateNamespace checks one namespace against the structural subset.
func ValidateNamespace(pluginID string, namespace Namespace) error {
	failure := func(field string, reason Reason) error {
		return &ValidationError{PluginID: pluginID, Field: field, Reason: reason}
	}
	if !ValidPluginID(pluginID) {
		return failure("", ReasonNamespace)
	}
	if namespace.SchemaVersion < 1 || namespace.SchemaVersion > MaxSchemaVersion {
		return failure("schemaVersion", ReasonSchemaVersion)
	}
	for key, value := range namespace.Fields {
		if key == "" || len(key) > MaxFieldKeyBytes || !utf8.ValidString(key) {
			return failure(key, ReasonFieldKey)
		}
		if err := validateValue(value); err != nil {
			var invalid *valueError
			if errors.As(err, &invalid) {
				return failure(key, invalid.reason)
			}
			return failure(key, ReasonJSON)
		}
	}
	for key, value := range namespace.Extra {
		if key == "" || len(key) > MaxFieldKeyBytes || !utf8.ValidString(key) {
			return failure(key, ReasonFieldKey)
		}
		if err := validateValue(value); err != nil {
			var invalid *valueError
			if errors.As(err, &invalid) {
				return failure(key, invalid.reason)
			}
			return failure(key, ReasonJSON)
		}
	}
	return nil
}

type valueError struct {
	reason Reason
}

func (e *valueError) Error() string { return string(e.reason) }

func validateValue(value json.RawMessage) error {
	if len(value) > MaxFieldBytes {
		return &valueError{reason: ReasonFieldSize}
	}
	if !utf8.Valid(value) {
		return &valueError{reason: ReasonUTF8}
	}
	if !json.Valid(value) {
		return &valueError{reason: ReasonJSON}
	}
	depth, err := jsonDepth(value)
	if err != nil {
		return &valueError{reason: ReasonJSON}
	}
	if depth > MaxDepth {
		return &valueError{reason: ReasonDepth}
	}
	return nil
}

// jsonDepth returns the container nesting depth of one JSON value. Scalars are
// zero; an object or array adds one at each level.
func jsonDepth(data []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	depth, maxDepth := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return maxDepth, nil
		}
		if err != nil {
			return 0, err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			continue
		}
		switch delim {
		case '{', '[':
			depth++
			if depth > maxDepth {
				maxDepth = depth
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return 0, errors.New("unbalanced value")
			}
		}
	}
}

// Validate checks every namespace and the record size. It is the strict path
// for an edit made through the host API: any invalid namespace is an error.
func Validate(m Map) error {
	for pluginID, namespace := range m {
		if err := ValidateNamespace(pluginID, namespace); err != nil {
			return err
		}
	}
	size, err := encodedSize(m)
	if err != nil {
		return err
	}
	if size > MaxRecordBytes {
		return fmt.Errorf("%w: %d > %d", ErrRecordTooLarge, size, MaxRecordBytes)
	}
	return nil
}

// Sanitize removes invalid namespaces individually and returns them as
// quarantines. Valid namespaces stay in the result. A valid remainder above
// MaxRecordBytes is a record-level error: the strict edit path may reject an
// over-limit edit rather than silently dropping valid data. Imported records
// use SanitizeLenient.
func Sanitize(m Map) (Map, []Quarantine, error) {
	clean, quarantined := sanitizeNamespaces(m)
	size, err := encodedSize(clean)
	if err != nil {
		return nil, nil, err
	}
	if size > MaxRecordBytes {
		return nil, nil, fmt.Errorf("%w: %d > %d", ErrRecordTooLarge, size, MaxRecordBytes)
	}
	return clean, quarantined, nil
}

// SanitizeLenient removes invalid namespaces individually and then
// deterministically quarantines valid namespaces until the canonical remainder
// fits MaxRecordBytes. It never reports ErrRecordTooLarge: an imported record
// with one oversized namespace must not make a whole restore or sync fail.
func SanitizeLenient(m Map) (Map, []Quarantine, error) {
	clean, quarantined := sanitizeNamespaces(m)
	return fitRecord(clean, quarantined)
}

func sanitizeNamespaces(m Map) (Map, []Quarantine) {
	clean := make(Map, len(m))
	pluginIDs := make([]string, 0, len(m))
	for pluginID := range m {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)
	var quarantined []Quarantine
	for _, pluginID := range pluginIDs {
		namespace := m[pluginID]
		if err := ValidateNamespace(pluginID, namespace); err != nil {
			quarantined = append(quarantined, quarantineOf(pluginID, namespace, err))
			continue
		}
		clean[pluginID] = namespace
	}
	return clean, quarantined
}

// namespaceFit is one valid namespace with its precomputed canonical encoding
// and full map-member size: the encoded key, the colon, and the encoded
// namespace value.
type namespaceFit struct {
	pluginID  string
	namespace Namespace
	value     []byte
	member    int
}

// fitRecord removes valid namespaces until the canonical record encoding is
// within MaxRecordBytes. The largest namespace is removed first, ties by
// Plugin ID, so the same input always produces the same quarantines and the
// smallest number of namespaces is dropped.
//
// Every namespace's canonical encoding and the container's key/separator
// overhead are computed once, so the cost is O(n log n) for the deterministic
// largest-first sort plus one encoding pass per namespace. Re-encoding the
// remaining map after each removal would be quadratic, and an imported record
// may carry thousands of valid namespaces (the sync read path is unbounded).
func fitRecord(clean Map, quarantined []Quarantine) (Map, []Quarantine, error) {
	if len(clean) == 0 {
		return clean, quarantined, nil
	}
	entries := make([]namespaceFit, 0, len(clean))
	// A JSON object is "{" + members joined by "," + "}".
	size := 2
	for pluginID, namespace := range clean {
		encodedKey, err := marshalNoEscape(pluginID)
		if err != nil {
			return nil, nil, err
		}
		encodedValue, err := marshalNoEscape(namespace)
		if err != nil {
			return nil, nil, err
		}
		member := len(encodedKey) + 1 + len(encodedValue)
		entries = append(entries, namespaceFit{
			pluginID: pluginID, namespace: namespace,
			value: encodedValue, member: member,
		})
		size += member
	}
	size += len(entries) - 1
	if size <= MaxRecordBytes {
		return clean, quarantined, nil
	}
	// Largest first; equal sizes quarantine the ascending first Plugin ID,
	// matching the previous deterministic scan.
	sort.Slice(entries, func(left, right int) bool {
		if entries[left].member != entries[right].member {
			return entries[left].member > entries[right].member
		}
		return entries[left].pluginID < entries[right].pluginID
	})
	remaining := len(entries)
	for _, entry := range entries {
		if size <= MaxRecordBytes {
			break
		}
		quarantined = append(quarantined, Quarantine{
			PluginID:      BoundPluginID(entry.pluginID),
			SchemaVersion: entry.namespace.SchemaVersion,
			Fields:        len(entry.namespace.Fields),
			SizeBytes:     len(entry.value),
			Reason:        ReasonRecordSize,
			Detail: BoundText(
				fmt.Sprintf("%d>%d", size, MaxRecordBytes), MaxDiagnosticDetailBytes),
			Raw: boundRaw(entry.value),
		})
		delete(clean, entry.pluginID)
		// Removing a member also removes the comma that separated it from the
		// next member, unless it was the last one.
		size -= entry.member
		if remaining > 1 {
			size--
		}
		remaining--
	}
	return clean, quarantined, nil
}

func quarantineOf(pluginID string, namespace Namespace, validation error) Quarantine {
	var failure *ValidationError
	reason := ReasonNamespace
	detail := ""
	if errors.As(validation, &failure) {
		reason = failure.Reason
		detail = failure.Field
	}
	// The raw encoding is diagnostic material. It is bounded by the record
	// limit and never merged back into a record automatically.
	raw, err := marshalNoEscape(namespace)
	if err != nil {
		raw = nil
	}
	return Quarantine{
		PluginID:      BoundPluginID(pluginID),
		SchemaVersion: namespace.SchemaVersion,
		Fields:        len(namespace.Fields),
		SizeBytes:     len(raw),
		Reason:        reason,
		Detail:        BoundText(detail, MaxDiagnosticDetailBytes),
		Raw:           boundRaw(raw),
	}
}

// SanitizeRecord sanitizes the pluginData property of one stored record while
// preserving every other property and every unknown property verbatim. A
// record without pluginData is returned unchanged.
//
// The raw container is only returned verbatim when it is already canonical and
// within the limits. A duplicate key, insignificant whitespace, or an escaped
// spelling could otherwise persist bytes that were never validated, so a
// non-canonical container is re-encoded from the validated map. An oversized
// valid namespace is quarantined deterministically instead of failing the
// whole restore or sync.
//
// A container with no surviving namespace, including a literal null, is
// represented by the absent property. Every producer and consumer of a stored
// or materialized record uses that one representation, so an all-quarantined
// container cannot make the v2 envelope and its inline payload disagree.
func SanitizeRecord(kind RecordKind, recordID string, data []byte) ([]byte, []Quarantine, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, fmt.Errorf("plugindata: %s %q: %w", kind, recordID, err)
	}
	if top == nil {
		return nil, nil, fmt.Errorf("plugindata: %s %q: %s", kind, recordID, ReasonRecord)
	}
	properties, err := ScanPluginDataProperties(data)
	if err != nil {
		return nil, nil, fmt.Errorf("plugindata: %s %q: %w", kind, recordID, err)
	}
	if !properties.Found {
		return data, nil, nil
	}
	// encoding/json matches a property name to a struct tag case-insensitively,
	// so "PluginData" and "PLUGINDATA" populate the typed field exactly like
	// "pluginData". The last occurrence in document order wins for every
	// spelling, and every other occurrence stays in the raw bytes unless the
	// record is re-encoded. Treat any non-canonical spelling or repeated
	// property as a reason to rewrite the record from the validated map.
	encoded := properties.Value
	variantSpellings := len(properties.Keys) > 1 || properties.Keys[0] != JSONField
	duplicateKeys := properties.DuplicateRecordProperty
	removeSpellings := func() {
		for key := range top {
			if IsPluginDataProperty(key) {
				delete(top, key)
			}
		}
	}
	if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		// The canonical representation of a container with no surviving
		// namespace is the absence of the property, including for a literal
		// null. Dropping it here keeps SanitizeRecord, the payload decoder,
		// purge, and the v2 envelope comparison byte-identical: retaining {}
		// or null on one side only would make an otherwise valid replica look
		// permanently damaged.
		removeSpellings()
		out, err := marshalNoEscape(top)
		if err != nil {
			return nil, nil, err
		}
		return out, nil, nil
	}
	// Decode the container leniently: one namespace with invalid UTF-8 must be
	// quarantined on its own, not turn every namespace in the record into a
	// record-level failure. Each namespace is validated strictly below.
	namespaces, quarantined, object, err := decodeMapLenient(encoded)
	if err != nil {
		return nil, nil, fmt.Errorf("plugindata: %s %q: %w", kind, recordID, err)
	}
	clean, err := EncodeMap(namespaces)
	if err != nil {
		return nil, nil, err
	}
	if object && len(namespaces) > 0 && len(quarantined) == 0 &&
		!duplicateKeys && !variantSpellings && bytes.Equal(encoded, clean) {
		// Every namespace is valid and the container is canonical: keep the
		// record's exact bytes so a backup, export, or sync round trip is
		// byte-for-byte verbatim rather than merely equivalent after a
		// re-encoding. An empty container is not kept: the canonical
		// representation of "no namespace survives" is the absent property, so
		// it falls through and is removed below.
		return data, nil, nil
	}
	removeSpellings()
	if object && len(namespaces) > 0 {
		top[JSONField] = clean
	}
	// A non-object container has no addressable namespace, and an object whose
	// every namespace was quarantined has no surviving namespace. Both are
	// represented by the absent property; the former also yields a
	// record-level quarantine.
	out, err := marshalNoEscape(top)
	if err != nil {
		return nil, nil, err
	}
	return out, quarantined, nil
}

// IsPluginDataProperty reports whether key is a spelling of JSONField that
// encoding/json's case-insensitive struct-field match would resolve to the
// typed pluginData field. Only ASCII case differences can match, so a Unicode
// look-alike stays an unknown property and is preserved verbatim.
func IsPluginDataProperty(key string) bool {
	if len(key) != len(JSONField) {
		return false
	}
	for index := 0; index < len(key); index++ {
		character, wanted := key[index], JSONField[index]
		if character >= 'A' && character <= 'Z' {
			character += 'a' - 'A'
		}
		if wanted >= 'A' && wanted <= 'Z' {
			wanted += 'a' - 'A'
		}
		if character != wanted {
			return false
		}
	}
	return true
}

// PluginDataProperties is the top-level pluginData view encoding/json sees in
// one record.
type PluginDataProperties struct {
	// Keys lists every matching spelling in document order.
	Keys []string
	// Value is the last matching property's raw value: the spelling and value
	// encoding/json's last-wins object decoding keeps.
	Value json.RawMessage
	// DuplicateRecordProperty reports a repeated top-level property name of
	// any spelling. The earlier duplicate is invisible to the decoder while
	// its bytes remain in the record.
	DuplicateRecordProperty bool
	// Found reports whether the record carried any pluginData spelling.
	Found bool
}

// ScanPluginDataProperties walks one record's top-level properties in document
// order and reports every spelling encoding/json would match to pluginData.
func ScanPluginDataProperties(data []byte) (PluginDataProperties, error) {
	var result PluginDataProperties
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return result, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return result, fmt.Errorf("record is not an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return result, err
		}
		key, ok := token.(string)
		if !ok {
			return result, fmt.Errorf("record property name is not a string")
		}
		if seen[key] {
			result.DuplicateRecordProperty = true
		} else {
			seen[key] = true
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return result, err
		}
		if IsPluginDataProperty(key) {
			result.Keys = append(result.Keys, key)
			result.Value = value
			result.Found = true
		}
	}
	if _, err := decoder.Token(); err != nil {
		return result, err
	}
	return result, nil
}

// DecodeMapLenient decodes one pluginData container for an imported record. A
// null container is empty; a non-object container is a record-level
// quarantine; each namespace is decoded and validated on its own. The returned
// map is canonical and within the record limit.
func DecodeMapLenient(data []byte) (Map, []Quarantine, error) {
	namespaces, quarantined, _, err := decodeMapLenient(data)
	return namespaces, quarantined, err
}

func decodeMapLenient(data []byte) (Map, []Quarantine, bool, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil, nil, true, nil
	}
	rawNamespaces, err := decodeRawObject(data)
	if err != nil {
		// One malformed namespace/record is quarantined on its own: the whole
		// payload must not be rejected because its pluginData container is not
		// an object.
		return nil, []Quarantine{{
			Reason:    ReasonRecord,
			SizeBytes: len(data),
			Detail:    BoundText(err.Error(), MaxDiagnosticDetailBytes),
			Raw:       boundRaw(data),
		}}, false, nil
	}
	pluginIDs := make([]string, 0, len(rawNamespaces))
	for pluginID := range rawNamespaces {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)
	namespaces := make(Map, len(rawNamespaces))
	var quarantined []Quarantine
	for _, pluginID := range pluginIDs {
		raw := rawNamespaces[pluginID]
		namespace, decodeErr := DecodeNamespace(raw)
		if decodeErr != nil {
			quarantined = append(quarantined, Quarantine{
				PluginID:  BoundPluginID(pluginID),
				Reason:    namespaceDecodeReason(decodeErr),
				SizeBytes: len(raw),
				Detail:    BoundText(decodeErr.Error(), MaxDiagnosticDetailBytes),
				Raw:       boundRaw(raw),
			})
			continue
		}
		if validateErr := ValidateNamespace(pluginID, namespace); validateErr != nil {
			quarantined = append(quarantined, quarantineOf(pluginID, namespace, validateErr))
			continue
		}
		namespaces[pluginID] = namespace
	}
	namespaces, quarantined, err = fitRecord(namespaces, quarantined)
	if err != nil {
		return nil, nil, false, err
	}
	return namespaces, quarantined, true, nil
}

// EncodeMap returns the canonical stored encoding of one pluginData map: no
// HTML escaping and deterministic key order. An empty map encodes as {}.
func EncodeMap(m Map) ([]byte, error) {
	if m == nil {
		m = Map{}
	}
	return marshalNoEscape(m)
}

func namespaceDecodeReason(err error) Reason {
	switch {
	case strings.Contains(err.Error(), "UTF-8"):
		return ReasonUTF8
	case strings.Contains(err.Error(), "schemaVersion"):
		return ReasonSchemaVersion
	case strings.Contains(err.Error(), "fields"):
		return ReasonJSON
	default:
		return ReasonNamespace
	}
}

func boundRaw(raw []byte) []byte {
	if len(raw) <= MaxRecordBytes {
		return append([]byte(nil), raw...)
	}
	return append([]byte(nil), raw[:MaxRecordBytes]...)
}

// BoundRecordID returns the deterministic, UTF-8 safe diagnostic spelling of a
// record ID. An imported payload or snapshot controls this value, so an
// unbounded probe must not inflate a quarantine row or the sync report.
func BoundRecordID(id string) string {
	return BoundText(id, MaxDiagnosticRecordIDBytes)
}

// BoundPluginID returns the deterministic, UTF-8 safe diagnostic spelling of a
// namespace key. An imported payload or snapshot controls this value, so every
// producer and every quarantine lookup must apply the same bound or a stored
// row becomes unreachable.
func BoundPluginID(id string) string {
	return BoundText(id, MaxDiagnosticPluginIDBytes)
}

// BoundText returns a deterministic UTF-8 safe prefix of value of at most max
// bytes. It is used for untrusted diagnostic labels such as a namespace key or
// a field name, which an imported probe can make arbitrarily large.
func BoundText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) > max {
		value = value[:max]
	}
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	for len(value) > max {
		value = value[:max]
		for len(value) > 0 && !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

// RecordBytes returns the canonical encoded size of one map.
func RecordBytes(m Map) (int, error) { return encodedSize(m) }

func encodedSize(value any) (int, error) {
	data, err := marshalNoEscape(value)
	if err != nil {
		return 0, err
	}
	return len(data), nil
}

// marshalNoEscape encodes a value without HTML escaping so '<', '>', and '&'
// count as the one byte each occupies in the stored JSON, not their six-byte
// transport spelling.
func marshalNoEscape(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := buffer.Bytes()
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	return append([]byte(nil), data...), nil
}

// Clone returns a deep copy safe to store on a record.
func Clone(m Map) Map {
	if len(m) == 0 {
		return nil
	}
	out := make(Map, len(m))
	for pluginID, namespace := range m {
		fields := make(map[string]json.RawMessage, len(namespace.Fields))
		for key, value := range namespace.Fields {
			fields[key] = append(json.RawMessage(nil), value...)
		}
		var extra map[string]json.RawMessage
		if len(namespace.Extra) > 0 {
			extra = make(map[string]json.RawMessage, len(namespace.Extra))
			for key, value := range namespace.Extra {
				extra[key] = append(json.RawMessage(nil), value...)
			}
		}
		out[pluginID] = Namespace{
			SchemaVersion: namespace.SchemaVersion,
			Fields:        fields,
			Extra:         extra,
		}
	}
	return out
}
