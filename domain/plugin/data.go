package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mslxl/natcatty/domain/model/plugindata"
)

// Data-provider limits. They are part of the generic contract: no protocol,
// query language, vendor, or product is named anywhere in this file. The
// runtime applies the same values so a guest can never widen them.
const (
	// MaxDataProviders bounds the data providers one manifest may declare.
	MaxDataProviders = 8
	// MaxDataProfileFields bounds a provider's declarative profile schema.
	MaxDataProfileFields = 32
	// MaxDataProfileFieldOptions bounds one select field's option list.
	MaxDataProfileFieldOptions = 64
	// MaxDataProviderName bounds the display name.
	MaxDataProviderName = 128
	// MaxDataProfileLabel bounds a user-visible profile name.
	MaxDataProfileLabel = 255
	// MaxDataProfileFieldValue bounds one non-secret provider-defined value.
	MaxDataProfileFieldValue = 16 << 10
	// MaxDataProfileSecretValue bounds one secret value before it is written to
	// the Vault field-secret store.
	MaxDataProfileSecretValue = 64 << 10
	// MaxDataProfileSecretRef bounds one opaque Vault reference.
	MaxDataProfileSecretRef = 512
	// MaxDataViaHost bounds the optional Host ID used for host-owned
	// tunneling.
	MaxDataViaHost = 128
	// DefaultDataMaxConnections is the per-profile connection bound a provider
	// gets when it does not declare max_connections.
	DefaultDataMaxConnections = 8
	// MaxDataConnections is the hard per-profile connection bound.
	MaxDataConnections = 64

	// MaxDataBatchRows is the largest row or document count in one batch.
	MaxDataBatchRows = 256
	// MaxDataBatchBytes is the largest table, document, or pair batch. It is
	// measured on the encoded batch without HTML escaping, so an ordinary
	// character such as '<', '>', or '&' counts as the one byte it occupies in
	// the provider payload rather than the six bytes of its JSON transport
	// spelling. Control bytes and binary chunk payloads still count their
	// encoded size, which is the transport the batch actually occupies.
	MaxDataBatchBytes = 256 << 10
	// MaxDataValueBytes is the hard bound on one tagged value, measured on the
	// raw payload bytes (the string, byte slice, decimal text, or JSON text),
	// not on its JSON-escaped transport form. A streamed chunk payload is
	// bounded by it as well.
	MaxDataValueBytes = 4 << 20
	// MaxDataEnvelopeOverhead is the documented allowance the whole-result
	// envelope adds above the per-value bound: the closed wrapper keys, the
	// per-row/column/node JSON structure, and an escaped quote here and there.
	// With the plan limit of MaxDataPlanNodes nodes the structural overhead is
	// well under this bound; an envelope above it is rejected as a whole.
	MaxDataEnvelopeOverhead = 64 << 10
	// MaxDataEnvelopeBytes is the whole-result cap. It is the per-value bound
	// plus the documented structural overhead and is measured without HTML
	// escaping, so a value at the raw 4 MiB bound is accepted regardless of
	// whether it contains '<', '>', or '&'.
	MaxDataEnvelopeBytes = MaxDataValueBytes + MaxDataEnvelopeOverhead
	// MaxDataInputBytes bounds provider-defined query input.
	MaxDataInputBytes = 1 << 20
	// MaxDataParameters bounds one query's parameter list.
	MaxDataParameters = 1024
	// MaxDataTableColumns bounds one table's column descriptors.
	MaxDataTableColumns = 1024
	// MaxDataPlanNodes bounds one plan tree node count.
	MaxDataPlanNodes = 1024
)

// DataFamily is the closed capability family of a data provider.
type DataFamily string

const (
	// DataFamilyTabular returns rows and columns.
	DataFamilyTabular DataFamily = "tabular"
	// DataFamilyDocument returns whole documents.
	DataFamilyDocument DataFamily = "document"
	// DataFamilyKeyValue returns key/value pairs and family-specific commands.
	DataFamilyKeyValue DataFamily = "keyvalue"
	// DataFamilyStream returns an ordered stream of chunks. V1 host UI and push
	// delivery are deferred; the declaration is still part of the closed set.
	DataFamilyStream DataFamily = "stream"
)

// Valid reports whether f belongs to the closed family set.
func (f DataFamily) Valid() bool {
	switch f {
	case DataFamilyTabular, DataFamilyDocument, DataFamilyKeyValue, DataFamilyStream:
		return true
	}
	return false
}

// DataFeature is one declared capability feature. Features describe the
// contract only; they never name a protocol or vendor.
type DataFeature string

const (
	DataFeatureTransactions  DataFeature = "transactions"
	DataFeatureMetadata      DataFeature = "metadata"
	DataFeatureMutations     DataFeature = "mutations"
	DataFeatureCancellation  DataFeature = "cancellation"
	DataFeatureStreaming     DataFeature = "streaming"
	DataFeatureSubscriptions DataFeature = "subscriptions"
	DataFeatureServerCursor  DataFeature = "server_cursor"
	DataFeatureTesting       DataFeature = "testing"
	DataFeaturePlans         DataFeature = "plans"
)

var dataFeatures = map[DataFeature]bool{
	DataFeatureTransactions:  true,
	DataFeatureMetadata:      true,
	DataFeatureMutations:     true,
	DataFeatureCancellation:  true,
	DataFeatureStreaming:     true,
	DataFeatureSubscriptions: true,
	DataFeatureServerCursor:  true,
	DataFeatureTesting:       true,
	DataFeaturePlans:         true,
}

// DataCapabilities is a provider's static declaration. A provider may refine
// it per connection with the capabilities method, but never above this set.
type DataCapabilities struct {
	Family DataFamily `toml:"family" json:"family"`
	// Features is the declared feature set. The host trims calls and UI to it;
	// a call outside it is rejected before reaching the guest.
	Features []DataFeature `toml:"features" json:"features,omitempty"`
	// MaxConnections bounds pooled connections per profile. Zero means
	// DefaultDataMaxConnections; the host caps the effective value.
	MaxConnections int `toml:"max_connections" json:"maxConnections,omitempty"`
}

// Supports reports whether feature was declared.
func (c DataCapabilities) Supports(feature DataFeature) bool {
	for _, declared := range c.Features {
		if declared == feature {
			return true
		}
	}
	return false
}

// EffectiveMaxConnections applies the default and the hard cap.
func (c DataCapabilities) EffectiveMaxConnections() int {
	if c.MaxConnections <= 0 {
		return DefaultDataMaxConnections
	}
	if c.MaxConnections > MaxDataConnections {
		return MaxDataConnections
	}
	return c.MaxConnections
}

// DataProfileFieldKind is the closed set of provider-defined profile field
// kinds. A secret field is a string whose value lives in the Vault and is
// referenced, never embedded, in the profile record.
type DataProfileFieldKind string

const (
	DataProfileFieldText    DataProfileFieldKind = "text"
	DataProfileFieldNumber  DataProfileFieldKind = "number"
	DataProfileFieldBoolean DataProfileFieldKind = "boolean"
	DataProfileFieldSelect  DataProfileFieldKind = "select"
)

func (k DataProfileFieldKind) Valid() bool {
	switch k {
	case DataProfileFieldText, DataProfileFieldNumber, DataProfileFieldBoolean, DataProfileFieldSelect:
		return true
	}
	return false
}

// DataProfileField is one declarative profile form field. The host renders and
// validates the form; the provider receives only field names and values.
type DataProfileField struct {
	Key      string               `toml:"key" json:"key"`
	Label    string               `toml:"label" json:"label,omitempty"`
	LabelKey string               `toml:"label_key" json:"labelKey,omitempty"`
	Kind     DataProfileFieldKind `toml:"kind" json:"kind"`
	Required bool                 `toml:"required" json:"required,omitempty"`
	// Secret marks a value that is stored in the Vault. The profile record holds
	// only an opaque reference under the same key.
	Secret bool `toml:"secret" json:"secret,omitempty"`
	// Options is the closed value list for a select field.
	Options []string `toml:"options" json:"options,omitempty"`
	// Help is host-rendered help text. HelpKey optionally names a catalog
	// message and takes precedence when present.
	Help    string `toml:"help" json:"help,omitempty"`
	HelpKey string `toml:"help_key" json:"helpKey,omitempty"`
}

// DataProvider is one non-terminal data provider a plugin contributes. It is
// distinct from a Host, a Session, and a Connection Provider.
type DataProvider struct {
	// ID is plugin-local. The host addresses the provider by its qualified
	// ID, plugin.<plugin-id>.<id>.
	ID string `toml:"id" json:"id"`
	// Name is the display name. NameKey optionally names a translated catalog
	// message and takes precedence when present.
	Name    string `toml:"name" json:"name,omitempty"`
	NameKey string `toml:"name_key" json:"nameKey,omitempty"`
	// Icon names a bundled icon asset, not a path.
	Icon string `toml:"icon" json:"icon,omitempty"`
	// Capabilities is the fixed per-version capability declaration.
	Capabilities DataCapabilities `toml:"capabilities" json:"capabilities"`
	// ProfileFields is the declarative form of provider-defined profile fields.
	ProfileFields []DataProfileField `toml:"profile_fields" json:"profileFields,omitempty"`
}

// QualifiedDataProviderID returns the host-wide provider ID for one
// manifest-local declaration.
func QualifiedDataProviderID(pluginID, providerID string) string {
	return "plugin." + pluginID + "." + providerID
}

// PluginIDFromDataProviderID returns the Plugin ID that owns a qualified data
// provider ID. A Plugin ID carries exactly one dot, so the first two segments
// after the "plugin." prefix are the plugin; a provider-local ID may itself
// contain dots. A foreign or malformed ID reports false.
func PluginIDFromDataProviderID(qualified string) (string, bool) {
	rest, ok := strings.CutPrefix(qualified, "plugin.")
	if !ok {
		return "", false
	}
	parts := strings.SplitN(rest, ".", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[0] + "." + parts[1], true
}

// ValidateDataProvider checks one static contribution. index selects the
// manifest field path in the error.
func ValidateDataProvider(pluginID string, provider DataProvider, index int) error {
	field := fmt.Sprintf("contributions.data_providers[%d]", index)
	if !validConnectionToken(provider.ID, 64) {
		return invalid(field+".id", "%q must be a lowercase provider id", provider.ID)
	}
	if strings.TrimSpace(provider.Name) == "" && provider.NameKey == "" {
		return invalid(field+".name", "is required unless name_key is set")
	}
	if len(provider.Name) > MaxDataProviderName {
		return invalid(field+".name", "must be at most %d characters", MaxDataProviderName)
	}
	if err := validateMessageKey(field+".name_key", provider.NameKey); err != nil {
		return err
	}
	if provider.Icon != "" && !validConnectionToken(provider.Icon, 128) {
		return invalid(field+".icon", "must be a lowercase asset name of at most 128 characters")
	}
	if !provider.Capabilities.Family.Valid() {
		return invalid(field+".capabilities.family",
			"unknown capability family %q", provider.Capabilities.Family)
	}
	if len(provider.Capabilities.Features) > len(dataFeatures) {
		return invalid(field+".capabilities.features",
			"at most %d features are allowed", len(dataFeatures))
	}
	features := make(map[DataFeature]bool, len(provider.Capabilities.Features))
	for i, feature := range provider.Capabilities.Features {
		if !dataFeatures[feature] {
			return invalid(fmt.Sprintf("%s.capabilities.features[%d]", field, i),
				"unknown capability feature %q", feature)
		}
		if features[feature] {
			return invalid(fmt.Sprintf("%s.capabilities.features[%d]", field, i),
				"duplicate capability feature %q", feature)
		}
		features[feature] = true
	}
	if provider.Capabilities.MaxConnections < 0 ||
		provider.Capabilities.MaxConnections > MaxDataConnections {
		return invalid(field+".capabilities.max_connections",
			"must be between 1 and %d, or 0 for the default", MaxDataConnections)
	}
	if len(provider.ProfileFields) > MaxDataProfileFields {
		return invalid(field+".profile_fields",
			"at most %d fields are allowed", MaxDataProfileFields)
	}
	fields := make(map[string]bool, len(provider.ProfileFields))
	for i, profileField := range provider.ProfileFields {
		sub := fmt.Sprintf("%s.profile_fields[%d]", field, i)
		if !validConnectionToken(profileField.Key, 64) {
			return invalid(sub+".key", "%q must be a lowercase field key", profileField.Key)
		}
		if fields[profileField.Key] {
			return invalid(sub+".key", "duplicate field %q", profileField.Key)
		}
		fields[profileField.Key] = true
		if strings.TrimSpace(profileField.Label) == "" && profileField.LabelKey == "" {
			return invalid(sub+".label", "is required unless label_key is set")
		}
		if len(profileField.Label) > 128 {
			return invalid(sub+".label", "must be at most 128 characters")
		}
		if err := validateMessageKey(sub+".label_key", profileField.LabelKey); err != nil {
			return err
		}
		if !profileField.Kind.Valid() {
			return invalid(sub+".kind", "unknown field kind %q", profileField.Kind)
		}
		if len(profileField.Options) > MaxDataProfileFieldOptions {
			return invalid(sub+".options",
				"at most %d options are allowed", MaxDataProfileFieldOptions)
		}
		options := make(map[string]bool, len(profileField.Options))
		for j, option := range profileField.Options {
			if strings.TrimSpace(option) == "" || len(option) > 128 {
				return invalid(fmt.Sprintf("%s.options[%d]", sub, j),
					"must be a non-empty value of at most 128 characters")
			}
			if options[option] {
				return invalid(fmt.Sprintf("%s.options[%d]", sub, j),
					"duplicate option %q", option)
			}
			options[option] = true
		}
		if profileField.Kind == DataProfileFieldSelect && len(profileField.Options) == 0 {
			return invalid(sub+".options", "is required for a select field")
		}
		if profileField.Kind != DataProfileFieldSelect && len(profileField.Options) > 0 {
			return invalid(sub+".options", "is allowed only on a select field")
		}
		if err := validateMessageKey(sub+".help_key", profileField.HelpKey); err != nil {
			return err
		}
	}
	return nil
}

// DataAccessMode is the user-selected access mode of one profile.
type DataAccessMode string

const (
	// DataModeReadOnly allows read-class operations only. It is the default.
	DataModeReadOnly DataAccessMode = AccessReadOnly
	// DataModeReadWrite allows read and write, and rejects destructive or
	// unknown operations.
	DataModeReadWrite DataAccessMode = AccessReadWrite
	// DataModeFull allows every classified operation, including destructive and
	// unknown, and is required for multi-operation requests.
	DataModeFull DataAccessMode = AccessFull
)

// Valid reports whether m belongs to the closed access-mode set.
func (m DataAccessMode) Valid() bool {
	switch m {
	case DataModeReadOnly, DataModeReadWrite, DataModeFull:
		return true
	}
	return false
}

// NormalizeDataAccessMode applies the read-only default.
func NormalizeDataAccessMode(m DataAccessMode) DataAccessMode {
	if m == "" {
		return DataModeReadOnly
	}
	return m
}

// Rank orders modes from least to most privileged. An invalid mode ranks -1.
func (m DataAccessMode) Rank() int {
	switch m {
	case DataModeReadOnly:
		return 0
	case DataModeReadWrite:
		return 1
	case DataModeFull:
		return 2
	}
	return -1
}

// AllowsMode reports whether a connection opened in mode m may serve a
// requested mode. A caller can never raise the profile's mode.
func (m DataAccessMode) AllowsMode(requested DataAccessMode) bool {
	if !m.Valid() || !requested.Valid() {
		return false
	}
	return requested.Rank() <= m.Rank()
}

// AllowsClass reports whether m permits one operation class. Unknown fails
// closed under read-only and read-write; only full may serve it.
func (m DataAccessMode) AllowsClass(class DataOperationClass) bool {
	switch m {
	case DataModeReadOnly:
		return class == DataClassRead
	case DataModeReadWrite:
		return class == DataClassRead || class == DataClassWrite
	case DataModeFull:
		return class.Valid()
	}
	return false
}

// DataOperationClass is the closed classification of one provider operation.
type DataOperationClass string

const (
	DataClassRead        DataOperationClass = "read"
	DataClassWrite       DataOperationClass = "write"
	DataClassDestructive DataOperationClass = "destructive"
	DataClassUnknown     DataOperationClass = "unknown"
)

// Valid reports whether c belongs to the closed class set.
func (c DataOperationClass) Valid() bool {
	switch c {
	case DataClassRead, DataClassWrite, DataClassDestructive, DataClassUnknown:
		return true
	}
	return false
}

// DataOperationClassByName maps a guest-reported class to the closed set. An
// unknown or empty value is DataClassUnknown, never trusted as read.
func DataOperationClassByName(name string) DataOperationClass {
	switch DataOperationClass(strings.ToLower(strings.TrimSpace(name))) {
	case DataClassRead:
		return DataClassRead
	case DataClassWrite:
		return DataClassWrite
	case DataClassDestructive:
		return DataClassDestructive
	default:
		return DataClassUnknown
	}
}

// DataProfile is one persisted user-visible data configuration record. It is
// not a Host or a Session. Provider-defined non-secret values live in Fields;
// secret values live in the Vault and are referenced from SecretRefs.
type DataProfile struct {
	ID       string         `json:"id"`
	Provider string         `json:"provider"`
	Label    string         `json:"label"`
	Mode     DataAccessMode `json:"mode"`
	// ViaHost optionally names a Host whose existing trust, credentials,
	// proxies, and jump chain carry the provider's network access. The host
	// owns that tunnel; the provider never receives Host credential material.
	ViaHost    string            `json:"viaHost,omitempty"`
	Fields     map[string]string `json:"fields,omitempty"`
	SecretRefs map[string]string `json:"secretRefs,omitempty"`
	// PluginData is the host-opaque structural data provider-defined fields
	// carry, keyed by Plugin ID. It is distinct from the flat Fields map: the
	// host never interprets its contents beyond the structural subset.
	PluginData plugindata.Map `json:"pluginData,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

// DataProfileDraft is the editable part of a profile. Secrets carries
// plaintext only long enough to write it to the Vault; it is never stored in
// the profile record. ClearSecrets removes existing references.
type DataProfileDraft struct {
	Label    string            `json:"label"`
	Provider string            `json:"provider"`
	Mode     DataAccessMode    `json:"mode"`
	ViaHost  string            `json:"viaHost,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
	Secrets  map[string]string `json:"secrets,omitempty"`
	// ClearSecrets names secret fields whose stored value should be removed.
	ClearSecrets []string `json:"clearSecrets,omitempty"`
	// SecretRefs carries already-persisted references when updating. A create
	// leaves it empty. It is populated by the service from the existing record
	// so validation can see required secrets that are not being rewritten.
	SecretRefs map[string]string `json:"-"`
}

// ValidateDataProfileDraft normalizes and validates a draft against the
// provider's declared field schema. qualifiedID is the host-wide provider ID.
func ValidateDataProfileDraft(qualifiedID string, provider DataProvider,
	draft DataProfileDraft) (DataProfileDraft, error) {

	draft.Label = strings.TrimSpace(draft.Label)
	draft.ViaHost = strings.TrimSpace(draft.ViaHost)
	draft.Mode = NormalizeDataAccessMode(draft.Mode)
	if draft.Provider != "" && draft.Provider != qualifiedID {
		return DataProfileDraft{}, invalid("provider", "must name %q", qualifiedID)
	}
	draft.Provider = qualifiedID
	if draft.Label == "" {
		return DataProfileDraft{}, invalid("label", "is required")
	}
	if len(draft.Label) > MaxDataProfileLabel {
		return DataProfileDraft{}, invalid("label", "must be at most %d characters", MaxDataProfileLabel)
	}
	if !draft.Mode.Valid() {
		return DataProfileDraft{}, invalid("mode", "unknown access mode %q", draft.Mode)
	}
	if draft.ViaHost != "" && !validDataHostID(draft.ViaHost) {
		return DataProfileDraft{}, invalid("via_host", "must be a Host ID")
	}

	declared := make(map[string]DataProfileField, len(provider.ProfileFields))
	for _, field := range provider.ProfileFields {
		declared[field.Key] = field
	}
	normalizedFields := make(map[string]string, len(draft.Fields))
	for key, value := range draft.Fields {
		field, ok := declared[key]
		if !ok {
			return DataProfileDraft{}, invalid("fields."+key, "is not a declared profile field")
		}
		if field.Secret {
			return DataProfileDraft{}, invalid("fields."+key,
				"is a secret field; supply it in secrets, not fields")
		}
		if len(value) > MaxDataProfileFieldValue {
			return DataProfileDraft{}, invalid("fields."+key,
				"must be at most %d bytes", MaxDataProfileFieldValue)
		}
		if err := validateDataProfileFieldValue(field, value); err != nil {
			return DataProfileDraft{}, invalid("fields."+key, "%v", err)
		}
		normalizedFields[key] = value
	}
	secrets := make(map[string]string, len(draft.Secrets))
	for key, value := range draft.Secrets {
		field, ok := declared[key]
		if !ok {
			return DataProfileDraft{}, invalid("secrets."+key, "is not a declared profile field")
		}
		if !field.Secret {
			return DataProfileDraft{}, invalid("secrets."+key, "is not a secret field")
		}
		if value == "" {
			return DataProfileDraft{}, invalid("secrets."+key, "must not be empty")
		}
		if len(value) > MaxDataProfileSecretValue {
			return DataProfileDraft{}, invalid("secrets."+key,
				"must be at most %d bytes", MaxDataProfileSecretValue)
		}
		secrets[key] = value
	}
	cleared := make(map[string]bool, len(draft.ClearSecrets))
	for _, key := range draft.ClearSecrets {
		field, ok := declared[key]
		if !ok || !field.Secret {
			return DataProfileDraft{}, invalid("clear_secrets."+key, "is not a declared secret field")
		}
		cleared[key] = true
	}
	for key := range secrets {
		if cleared[key] {
			return DataProfileDraft{}, invalid("secrets."+key,
				"cannot be set and cleared in the same update")
		}
	}
	refs := make(map[string]string, len(draft.SecretRefs))
	for key, ref := range draft.SecretRefs {
		field, ok := declared[key]
		if !ok || !field.Secret {
			return DataProfileDraft{}, invalid("secret_refs."+key, "is not a declared secret field")
		}
		if !validDataSecretRef(ref) {
			return DataProfileDraft{}, invalid("secret_refs."+key, "is not a valid Vault reference")
		}
		refs[key] = ref
	}
	for key, field := range declared {
		if !field.Required {
			continue
		}
		if field.Secret {
			if _, ok := secrets[key]; ok && !cleared[key] {
				continue
			}
			if ref, ok := refs[key]; ok && !cleared[key] && validDataSecretRef(ref) {
				continue
			}
			return DataProfileDraft{}, invalid("secrets."+key, "is required")
		}
		if _, ok := normalizedFields[key]; !ok {
			return DataProfileDraft{}, invalid("fields."+key, "is required")
		}
	}
	for key := range normalizedFields {
		if _, ok := declared[key]; !ok {
			return DataProfileDraft{}, invalid("fields."+key, "is not a declared profile field")
		}
	}
	draft.Fields = normalizedFields
	draft.Secrets = secrets
	draft.ClearSecrets = append([]string(nil), draft.ClearSecrets...)
	draft.SecretRefs = refs
	return draft, nil
}

func validateDataProfileFieldValue(field DataProfileField, value string) error {
	switch field.Kind {
	case DataProfileFieldNumber:
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return fmt.Errorf("must be a number")
		}
	case DataProfileFieldBoolean:
		if value != "true" && value != "false" {
			return fmt.Errorf("must be true or false")
		}
	case DataProfileFieldSelect:
		for _, option := range field.Options {
			if option == value {
				return nil
			}
		}
		return fmt.Errorf("is not a declared option")
	}
	return nil
}

// dataHostIDLength is the fixed width of a canonical ULID, the shape every
// real Host ID has.
const dataHostIDLength = 26

// validDataHostID reports whether text has the shape of a real Host ID: a
// canonical Crockford base32 ULID. Host IDs are opaque to this package; the
// shape check keeps a malformed or foreign identifier (for example a
// connection token or a Session ID) out of the via_host field.
func validDataHostID(text string) bool {
	if len(text) != dataHostIDLength {
		return false
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
			// Crockford's alphabet omits I, L, O, and U.
			switch c {
			case 'I', 'L', 'O', 'U':
				return false
			}
		default:
			return false
		}
	}
	// A ULID carries 128 bits in 130 encoded bits, so its first character
	// cannot exceed 7.
	return text[0] <= '7'
}

func validDataSecretRef(ref string) bool {
	if ref == "" || len(ref) > MaxDataProfileSecretRef {
		return false
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f || r == ' ' {
			return false
		}
	}
	return true
}

// ValidateDataProfile checks one stored record against the provider schema.
// A missing provider cannot supply a schema; the record's non-secret shape is
// still validated locally, so dormant profiles remain structurally sound.
func ValidateDataProfile(record DataProfile) error {
	if strings.TrimSpace(record.ID) == "" {
		return invalid("id", "is required")
	}
	if strings.TrimSpace(record.Provider) == "" {
		return invalid("provider", "is required")
	}
	if strings.TrimSpace(record.Label) == "" {
		return invalid("label", "is required")
	}
	if len(record.Label) > MaxDataProfileLabel {
		return invalid("label", "must be at most %d characters", MaxDataProfileLabel)
	}
	record.Mode = NormalizeDataAccessMode(record.Mode)
	if !record.Mode.Valid() {
		return invalid("mode", "unknown access mode %q", record.Mode)
	}
	if record.ViaHost != "" && !validDataHostID(record.ViaHost) {
		return invalid("via_host", "must be a Host ID")
	}
	if len(record.Fields) > MaxDataProfileFields || len(record.SecretRefs) > MaxDataProfileFields {
		return invalid("fields", "at most %d fields are allowed", MaxDataProfileFields)
	}
	for key, value := range record.Fields {
		if !validConnectionToken(key, 64) {
			return invalid("fields."+key, "is not a valid field key")
		}
		if len(value) > MaxDataProfileFieldValue {
			return invalid("fields."+key, "must be at most %d bytes", MaxDataProfileFieldValue)
		}
	}
	for key, ref := range record.SecretRefs {
		if !validConnectionToken(key, 64) {
			return invalid("secret_refs."+key, "is not a valid field key")
		}
		if !validDataSecretRef(ref) {
			return invalid("secret_refs."+key, "is not a valid Vault reference")
		}
	}
	return nil
}

// DataValueKind is the tag of one precision-safe value.
type DataValueKind string

const (
	DataValueNull    DataValueKind = "null"
	DataValueBool    DataValueKind = "bool"
	DataValueInt     DataValueKind = "int"
	DataValueFloat   DataValueKind = "float"
	DataValueDecimal DataValueKind = "decimal"
	DataValueString  DataValueKind = "string"
	DataValueBytes   DataValueKind = "bytes"
	DataValueTime    DataValueKind = "time"
	DataValueJSON    DataValueKind = "json"
)

// Valid reports whether k belongs to the closed value-tag set.
func (k DataValueKind) Valid() bool {
	switch k {
	case DataValueNull, DataValueBool, DataValueInt, DataValueFloat,
		DataValueDecimal, DataValueString, DataValueBytes, DataValueTime, DataValueJSON:
		return true
	}
	return false
}

// DataValue is one tagged value. Int and Decimal are strings so 64-bit
// integers and arbitrary-precision decimals never pass through a float. Bytes
// marshal as base64 and Time is RFC 3339 with its zone.
type DataValue struct {
	Kind    DataValueKind   `json:"kind"`
	Bool    bool            `json:"bool,omitempty"`
	Int     string          `json:"int,omitempty"`
	Float   float64         `json:"float,omitempty"`
	Decimal string          `json:"decimal,omitempty"`
	String  string          `json:"string,omitempty"`
	Bytes   []byte          `json:"bytes,omitempty"`
	Time    string          `json:"time,omitempty"`
	JSON    json.RawMessage `json:"json,omitempty"`
}

var dataDecimalPattern = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// UnmarshalJSON enforces the closed tagged-value shape on the wire. A payload
// field for another kind is rejected even when it carries the zero value (for
// example {"kind":"string","float":0}); a Go literal cannot express the
// difference between an absent zero and an explicit zero, so Validate rejects
// the same shape for constructed values by refusing non-zero stray fields.
func (v *DataValue) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw == nil {
		return fmt.Errorf("data value is not an object")
	}
	var tagged struct {
		Kind DataValueKind `json:"kind"`
	}
	if encoded, ok := raw["kind"]; ok {
		if err := json.Unmarshal(encoded, &tagged.Kind); err != nil {
			return err
		}
	}
	allowed := ""
	if tagged.Kind.Valid() {
		allowed = dataValuePayloadField(tagged.Kind)
	}
	for key := range raw {
		if key != "kind" && key != allowed {
			return fmt.Errorf("value of kind %q carries %q", tagged.Kind, key)
		}
	}
	type plainDataValue DataValue
	var decoded plainDataValue
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*v = DataValue(decoded)
	return nil
}

// dataValuePayloadField names the one payload field a kind may carry. Null
// carries none.
func dataValuePayloadField(kind DataValueKind) string {
	switch kind {
	case DataValueBool:
		return "bool"
	case DataValueInt:
		return "int"
	case DataValueFloat:
		return "float"
	case DataValueDecimal:
		return "decimal"
	case DataValueString:
		return "string"
	case DataValueBytes:
		return "bytes"
	case DataValueTime:
		return "time"
	case DataValueJSON:
		return "json"
	}
	return ""
}

// Validate checks the tag and the matching payload field. A value above the
// per-value limit is rejected.
func (v DataValue) Validate() error {
	if !v.Kind.Valid() {
		return fmt.Errorf("unknown value tag %q", v.Kind)
	}
	// The limit is on the raw payload the value carries. Measuring the marshaled
	// value would charge a string of '<', '>', or '&' five extra bytes per
	// character for HTML escaping and reject a value that is inside the stated
	// per-value bound. The whole-envelope cap in checkDataEnvelopeBytes provides
	// the bounded transport accounting.
	var payload int
	switch v.Kind {
	case DataValueString:
		payload = len(v.String)
	case DataValueBytes:
		payload = len(v.Bytes)
	case DataValueJSON:
		payload = len(v.JSON)
	case DataValueInt:
		payload = len(v.Int)
	case DataValueDecimal:
		payload = len(v.Decimal)
	case DataValueTime:
		payload = len(v.Time)
	}
	if payload > MaxDataValueBytes {
		return fmt.Errorf("value exceeds the %d-byte limit", MaxDataValueBytes)
	}
	switch v.Kind {
	case DataValueNull:
		return v.expectEmpty()
	case DataValueBool:
		return v.expectOnly("bool")
	case DataValueInt:
		if _, err := strconv.ParseInt(v.Int, 10, 64); err != nil {
			return fmt.Errorf("int is not a 64-bit decimal string")
		}
		return v.expectOnly("int")
	case DataValueFloat:
		if math.IsNaN(v.Float) || math.IsInf(v.Float, 0) {
			return fmt.Errorf("float is not finite")
		}
		return v.expectOnly("float")
	case DataValueDecimal:
		if !dataDecimalPattern.MatchString(v.Decimal) {
			return fmt.Errorf("decimal is not a plain decimal string")
		}
		return v.expectOnly("decimal")
	case DataValueString:
		return v.expectOnly("string")
	case DataValueBytes:
		return v.expectOnly("bytes")
	case DataValueTime:
		if _, err := time.Parse(time.RFC3339, v.Time); err != nil {
			return fmt.Errorf("time is not RFC 3339 with a zone")
		}
		return v.expectOnly("time")
	case DataValueJSON:
		if len(v.JSON) == 0 || !json.Valid(v.JSON) {
			return fmt.Errorf("json is not valid JSON")
		}
		return v.expectOnly("json")
	}
	return nil
}

func (v DataValue) expectEmpty() error {
	if v.Bool || v.Int != "" || v.Float != 0 || v.Decimal != "" || v.String != "" ||
		len(v.Bytes) > 0 || v.Time != "" || len(v.JSON) > 0 {
		return fmt.Errorf("null value carries a payload")
	}
	return nil
}

func (v DataValue) expectOnly(field string) error {
	empty := map[string]bool{
		"bool":    !v.Bool,
		"int":     v.Int == "",
		"float":   v.Float == 0,
		"decimal": v.Decimal == "",
		"string":  v.String == "",
		"bytes":   len(v.Bytes) == 0,
		"time":    v.Time == "",
		"json":    len(v.JSON) == 0,
	}
	for name, isZero := range empty {
		if name == field {
			continue
		}
		if !isZero {
			return fmt.Errorf("%s value carries %s", v.Kind, name)
		}
	}
	return nil
}

// DataColumn describes one table column.
type DataColumn struct {
	Name        string        `json:"name"`
	Type        DataValueKind `json:"type"`
	Nullable    bool          `json:"nullable,omitempty"`
	DisplayType string        `json:"displayType,omitempty"`
}

// DataTable is the tabular result envelope.
type DataTable struct {
	Columns []DataColumn  `json:"columns"`
	Rows    [][]DataValue `json:"rows"`
}

// DataDocuments is the document result envelope.
type DataDocuments struct {
	Documents []map[string]DataValue `json:"documents"`
}

// DataPair is one key/value result in a key-value family.
type DataPair struct {
	Key   DataValue `json:"key"`
	Value DataValue `json:"value"`
}

// DataPairs is the key-value result envelope.
type DataPairs struct {
	Pairs []DataPair `json:"pairs"`
}

// DataChunk is one streamed byte range.
type DataChunk struct {
	Offset int64  `json:"offset"`
	Data   []byte `json:"data"`
}

// DataChunks is the streaming result envelope. Each chunk is bounded by the
// per-value limit; the stream as a whole is credit-driven.
type DataChunks struct {
	Chunks []DataChunk `json:"chunks"`
}

// DataSummary is a non-tabular completion envelope.
type DataSummary struct {
	AffectedRows int64  `json:"affectedRows,omitempty"`
	Message      string `json:"message,omitempty"`
}

// DataMetadataNodeKind is the closed node vocabulary of a metadata tree.
type DataMetadataNodeKind string

const (
	DataMetadataContainer  DataMetadataNodeKind = "container"
	DataMetadataCollection DataMetadataNodeKind = "collection"
	DataMetadataField      DataMetadataNodeKind = "field"
	DataMetadataIndex      DataMetadataNodeKind = "index"
	DataMetadataView       DataMetadataNodeKind = "view"
	DataMetadataKey        DataMetadataNodeKind = "key"
	DataMetadataDocument   DataMetadataNodeKind = "document"
)

// Valid reports whether k belongs to the closed metadata node set.
func (k DataMetadataNodeKind) Valid() bool {
	switch k {
	case DataMetadataContainer, DataMetadataCollection, DataMetadataField,
		DataMetadataIndex, DataMetadataView, DataMetadataKey, DataMetadataDocument:
		return true
	}
	return false
}

// DataMetadataNode is one node of a paged metadata tree.
type DataMetadataNode struct {
	Kind     DataMetadataNodeKind `json:"kind"`
	Name     string               `json:"name"`
	Path     string               `json:"path"`
	Detail   string               `json:"detail,omitempty"`
	Children []DataMetadataNode   `json:"children,omitempty"`
}

// DataMetadata is the paged metadata response. Next is an opaque continuation
// token; empty means the tree is complete.
type DataMetadata struct {
	Nodes []DataMetadataNode `json:"nodes"`
	Next  string             `json:"next,omitempty"`
}

// Validate checks the closed node set and the tree bound.
func (m DataMetadata) Validate() error {
	if len(m.Next) > MaxDataProfileSecretRef {
		return fmt.Errorf("metadata cursor is too large")
	}
	total := 0
	var walk func(nodes []DataMetadataNode, depth int) error
	walk = func(nodes []DataMetadataNode, depth int) error {
		if depth > 16 {
			return fmt.Errorf("metadata nesting is deeper than 16")
		}
		for _, node := range nodes {
			total++
			if total > MaxDataPlanNodes {
				return fmt.Errorf("metadata has more than %d nodes", MaxDataPlanNodes)
			}
			if !node.Kind.Valid() {
				return fmt.Errorf("unknown metadata node kind %q", node.Kind)
			}
			if strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Path) == "" {
				return fmt.Errorf("metadata node needs a name and a path")
			}
			if err := walk(node.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(m.Nodes, 0)
}

// DataPlanNode is one node of an optional query plan tree.
type DataPlanNode struct {
	Label    string         `json:"label"`
	Detail   string         `json:"detail,omitempty"`
	Children []DataPlanNode `json:"children,omitempty"`
}

// DataPlan is the optional plan/explain result envelope.
type DataPlan struct {
	Text  string         `json:"text,omitempty"`
	Nodes []DataPlanNode `json:"nodes,omitempty"`
}

// DataResultKind is the closed result-envelope vocabulary.
type DataResultKind string

const (
	DataResultTable     DataResultKind = "table"
	DataResultDocuments DataResultKind = "documents"
	DataResultPairs     DataResultKind = "pairs"
	DataResultChunks    DataResultKind = "chunks"
	DataResultSummary   DataResultKind = "summary"
	DataResultPlan      DataResultKind = "plan"
)

// Valid reports whether k belongs to the closed envelope set.
func (k DataResultKind) Valid() bool {
	switch k {
	case DataResultTable, DataResultDocuments, DataResultPairs,
		DataResultChunks, DataResultSummary, DataResultPlan:
		return true
	}
	return false
}

// DataResult is the one closed result envelope every data method returns. Kind
// selects the single populated payload. More asks the caller for another
// fetch; Limit reports the host or provider budget was reached; Partial marks
// rows delivered before a cancellation; WouldBlock reports that the provider
// has no batch ready and the caller must offer credit later.
type DataResult struct {
	Kind       DataResultKind `json:"kind,omitempty"`
	Table      *DataTable     `json:"table,omitempty"`
	Documents  *DataDocuments `json:"documents,omitempty"`
	Pairs      *DataPairs     `json:"pairs,omitempty"`
	Chunks     *DataChunks    `json:"chunks,omitempty"`
	Summary    *DataSummary   `json:"summary,omitempty"`
	Plan       *DataPlan      `json:"plan,omitempty"`
	More       bool           `json:"more,omitempty"`
	Limit      bool           `json:"limit,omitempty"`
	Partial    bool           `json:"partial,omitempty"`
	WouldBlock bool           `json:"wouldBlock,omitempty"`
	// Class is the provider's classification of the operation that produced
	// this result. The host audits it and never trusts an empty value.
	Class DataOperationClass `json:"class,omitempty"`
}

// Validate checks the closed envelope, the batch limits, and every tagged
// value. A would-block result carries no payload.
func (r DataResult) Validate() error {
	if r.WouldBlock {
		if r.Kind != "" || r.Table != nil || r.Documents != nil || r.Pairs != nil ||
			r.Chunks != nil || r.Summary != nil || r.Plan != nil {
			return fmt.Errorf("would-block result carries a payload")
		}
		return nil
	}
	if !r.Kind.Valid() {
		return fmt.Errorf("unknown result envelope %q", r.Kind)
	}
	if r.Class != "" && !r.Class.Valid() {
		return fmt.Errorf("unknown operation class %q", r.Class)
	}
	payloads := 0
	if r.Table != nil {
		payloads++
	}
	if r.Documents != nil {
		payloads++
	}
	if r.Pairs != nil {
		payloads++
	}
	if r.Chunks != nil {
		payloads++
	}
	if r.Summary != nil {
		payloads++
	}
	if r.Plan != nil {
		payloads++
	}
	if payloads != 1 {
		return fmt.Errorf("result must carry exactly one envelope for %q", r.Kind)
	}
	switch r.Kind {
	case DataResultTable:
		if r.Table == nil {
			return fmt.Errorf("table envelope is missing")
		}
		return validateDataTable(r.Table)
	case DataResultDocuments:
		if r.Documents == nil {
			return fmt.Errorf("documents envelope is missing")
		}
		return validateDataDocuments(r.Documents)
	case DataResultPairs:
		if r.Pairs == nil {
			return fmt.Errorf("pairs envelope is missing")
		}
		return validateDataPairs(r.Pairs)
	case DataResultChunks:
		if r.Chunks == nil {
			return fmt.Errorf("chunks envelope is missing")
		}
		return validateDataChunks(r.Chunks)
	case DataResultSummary:
		if r.Summary == nil {
			return fmt.Errorf("summary envelope is missing")
		}
		if len(r.Summary.Message) > MaxDataValueBytes {
			return fmt.Errorf("summary message exceeds the %d-byte limit", MaxDataValueBytes)
		}
	case DataResultPlan:
		if r.Plan == nil {
			return fmt.Errorf("plan envelope is missing")
		}
		if err := validateDataPlan(r.Plan, 0); err != nil {
			return err
		}
	}
	return checkDataEnvelopeBytes(r)
}

func validateDataTable(table *DataTable) error {
	if len(table.Columns) > MaxDataTableColumns {
		return fmt.Errorf("table has more than %d columns", MaxDataTableColumns)
	}
	if len(table.Rows) > MaxDataBatchRows {
		return fmt.Errorf("table has more than %d rows", MaxDataBatchRows)
	}
	for i, column := range table.Columns {
		if strings.TrimSpace(column.Name) == "" {
			return fmt.Errorf("column %d has no name", i)
		}
		if !column.Type.Valid() {
			return fmt.Errorf("column %d has unknown type %q", i, column.Type)
		}
	}
	for i, row := range table.Rows {
		if len(row) != len(table.Columns) {
			return fmt.Errorf("row %d has %d values, want %d", i, len(row), len(table.Columns))
		}
		for j := range row {
			if err := row[j].Validate(); err != nil {
				return fmt.Errorf("row %d column %d: %w", i, j, err)
			}
		}
	}
	return checkDataBatchBytes(table)
}

func validateDataDocuments(documents *DataDocuments) error {
	if len(documents.Documents) > MaxDataBatchRows {
		return fmt.Errorf("batch has more than %d documents", MaxDataBatchRows)
	}
	for i, document := range documents.Documents {
		for key, value := range document {
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("document %d has an empty key", i)
			}
			if err := value.Validate(); err != nil {
				return fmt.Errorf("document %d field %s: %w", i, key, err)
			}
		}
	}
	return checkDataBatchBytes(documents)
}

func validateDataPairs(pairs *DataPairs) error {
	if len(pairs.Pairs) > MaxDataBatchRows {
		return fmt.Errorf("batch has more than %d pairs", MaxDataBatchRows)
	}
	for i, pair := range pairs.Pairs {
		if err := pair.Key.Validate(); err != nil {
			return fmt.Errorf("pair %d key: %w", i, err)
		}
		if err := pair.Value.Validate(); err != nil {
			return fmt.Errorf("pair %d value: %w", i, err)
		}
	}
	return checkDataBatchBytes(pairs)
}

func validateDataChunks(chunks *DataChunks) error {
	if len(chunks.Chunks) > MaxDataBatchRows {
		return fmt.Errorf("batch has more than %d chunks", MaxDataBatchRows)
	}
	for i, chunk := range chunks.Chunks {
		if chunk.Offset < 0 {
			return fmt.Errorf("chunk %d has a negative offset", i)
		}
		if len(chunk.Data) > MaxDataValueBytes {
			return fmt.Errorf("chunk %d exceeds the %d-byte limit", i, MaxDataValueBytes)
		}
	}
	// A chunked batch obeys the same aggregate byte limit as every other
	// batch; streaming does not exempt a response from the ticket's cap.
	return checkDataBatchBytes(chunks)
}

func validateDataPlan(plan *DataPlan, depth int) error {
	if depth > 16 {
		return fmt.Errorf("plan nesting is deeper than 16")
	}
	if strings.TrimSpace(plan.Text) == "" && len(plan.Nodes) == 0 {
		return fmt.Errorf("plan carries neither text nor nodes")
	}
	// The per-value bound is on the raw diagnostic text, consistent with
	// DataValue. The whole-envelope cap still counts the encoded structure, but
	// a single raw string field cannot exceed the stated per-value limit.
	if len(plan.Text) > MaxDataValueBytes {
		return fmt.Errorf("plan text exceeds the %d-byte limit", MaxDataValueBytes)
	}
	total := 0
	var walk func(nodes []DataPlanNode, depth int) error
	walk = func(nodes []DataPlanNode, depth int) error {
		if depth > 16 {
			return fmt.Errorf("plan nesting is deeper than 16")
		}
		for _, node := range nodes {
			total++
			if total > MaxDataPlanNodes {
				return fmt.Errorf("plan has more than %d nodes", MaxDataPlanNodes)
			}
			if strings.TrimSpace(node.Label) == "" {
				return fmt.Errorf("plan node has no label")
			}
			if len(node.Label) > MaxDataValueBytes || len(node.Detail) > MaxDataValueBytes {
				return fmt.Errorf("plan node text exceeds the %d-byte limit", MaxDataValueBytes)
			}
			if err := walk(node.Children, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(plan.Nodes, depth)
}

// dataJSONEncodedLen returns the encoded length of value without the trailing
// newline json.Encoder appends. HTML escaping is disabled so '<', '>', and '&'
// count as one byte each: the provider payload contract is about the data, not
// about the six-byte escape sequence a JSON transport may choose for it.
func dataJSONEncodedLen(value any) (int, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 0, err
	}
	n := buf.Len()
	if n > 0 && buf.Bytes()[n-1] == '\n' {
		n--
	}
	return n, nil
}

func checkDataBatchBytes(value any) error {
	encoded, err := dataJSONEncodedLen(value)
	if err != nil {
		return err
	}
	if encoded > MaxDataBatchBytes {
		return fmt.Errorf("batch exceeds the %d-byte limit", MaxDataBatchBytes)
	}
	return nil
}

func checkDataEnvelopeBytes(result DataResult) error {
	encoded, err := dataJSONEncodedLen(result)
	if err != nil {
		return err
	}
	if encoded > MaxDataEnvelopeBytes {
		return fmt.Errorf("result exceeds the %d-byte envelope limit", MaxDataEnvelopeBytes)
	}
	return nil
}

// DataRows returns the number of rows, documents, pairs, or chunks in the
// envelope. Summary and plan results count as zero.
func (r DataResult) DataRows() int {
	switch r.Kind {
	case DataResultTable:
		if r.Table != nil {
			return len(r.Table.Rows)
		}
	case DataResultDocuments:
		if r.Documents != nil {
			return len(r.Documents.Documents)
		}
	case DataResultPairs:
		if r.Pairs != nil {
			return len(r.Pairs.Pairs)
		}
	case DataResultChunks:
		if r.Chunks != nil {
			return len(r.Chunks.Chunks)
		}
	}
	return 0
}

// DataResultBytes returns the encoded payload size without the envelope
// metadata, used for the interactive byte budget. The size is measured without
// HTML escaping, the same accounting checkDataEnvelopeBytes and the per-value
// limits use, so '<', '>', and '&' do not appear larger here than in the
// transport that actually carries them.
func (r DataResult) DataResultBytes() int {
	encoded, err := dataJSONEncodedLen(r)
	if err != nil {
		return 0
	}
	return encoded
}

// TruncateDataResult keeps at most rows rows and at most maxBytes encoded
// bytes. For a chunked result a row is one chunk. A negative rows or maxBytes
// means no bound. It returns the trimmed copy and whether anything was
// dropped. A single row that does not fit is dropped entirely; delivered rows
// are never rewritten by the caller. Chunked results obey the byte budget like
// every other batch, so a streamed batch is trimmed by whole chunks.
func TruncateDataResult(result DataResult, rows, maxBytes int) (DataResult, bool) {
	dropped := false
	switch result.Kind {
	case DataResultTable:
		if result.Table == nil {
			return result, false
		}
		table := *result.Table
		kept := table.Rows
		if rows >= 0 && len(kept) > rows {
			kept = kept[:rows]
			dropped = true
		}
		if maxBytes >= 0 {
			for len(kept) > 0 {
				candidate := result
				candidate.Table = &DataTable{Columns: table.Columns, Rows: kept}
				if candidate.DataResultBytes() <= maxBytes {
					break
				}
				kept = kept[:len(kept)-1]
				dropped = true
			}
		}
		result.Table = &DataTable{Columns: table.Columns, Rows: kept}
	case DataResultDocuments:
		if result.Documents == nil {
			return result, false
		}
		documents := *result.Documents
		kept := documents.Documents
		if rows >= 0 && len(kept) > rows {
			kept = kept[:rows]
			dropped = true
		}
		if maxBytes >= 0 {
			for len(kept) > 0 {
				candidate := result
				candidate.Documents = &DataDocuments{Documents: kept}
				if candidate.DataResultBytes() <= maxBytes {
					break
				}
				kept = kept[:len(kept)-1]
				dropped = true
			}
		}
		result.Documents = &DataDocuments{Documents: kept}
	case DataResultPairs:
		if result.Pairs == nil {
			return result, false
		}
		pairs := *result.Pairs
		kept := pairs.Pairs
		if rows >= 0 && len(kept) > rows {
			kept = kept[:rows]
			dropped = true
		}
		if maxBytes >= 0 {
			for len(kept) > 0 {
				candidate := result
				candidate.Pairs = &DataPairs{Pairs: kept}
				if candidate.DataResultBytes() <= maxBytes {
					break
				}
				kept = kept[:len(kept)-1]
				dropped = true
			}
		}
		result.Pairs = &DataPairs{Pairs: kept}
	case DataResultChunks:
		if result.Chunks == nil {
			return result, false
		}
		chunks := *result.Chunks
		kept := chunks.Chunks
		if rows >= 0 && len(kept) > rows {
			kept = kept[:rows]
			dropped = true
		}
		if maxBytes >= 0 {
			for len(kept) > 0 {
				candidate := result
				candidate.Chunks = &DataChunks{Chunks: kept}
				if candidate.DataResultBytes() <= maxBytes {
					break
				}
				kept = kept[:len(kept)-1]
				dropped = true
			}
		}
		result.Chunks = &DataChunks{Chunks: kept}
	}
	return result, dropped
}

// DataAuditReason formats a stable data-provider audit reason. It carries
// names, modes, classes, and sizes only, never query input or result content.
func DataAuditReason(action, result string) string {
	if result == "" {
		return "data." + action
	}
	return "data." + action + ":" + result
}
