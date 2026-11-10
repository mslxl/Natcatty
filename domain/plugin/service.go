package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Cross-plugin service declaration limits. A provider declares a small,
// closed table; the host rejects a declaration before it can consume bounded
// host resources.
const (
	// MaxServicesPerPlugin is the number of provides declarations one plugin
	// may publish.
	MaxServicesPerPlugin = 8
	// MaxMethodsPerService bounds a provider's method table.
	MaxMethodsPerService = 64
	// MaxEventsPerService bounds a provider's event table.
	MaxEventsPerService = 32
	// MaxServiceSchemaBytes bounds one argument, result, or event schema.
	MaxServiceSchemaBytes = 16 << 10
	// MaxServiceDeclarationBytes bounds the whole serialized declaration.
	MaxServiceDeclarationBytes = 256 << 10
	// MaxServiceJSONDepth is the maximum container nesting of a JSON payload.
	MaxServiceJSONDepth = 32
	// MaxServiceJSONKeyBytes bounds one JSON object key.
	MaxServiceJSONKeyBytes = 256
	// MaxServiceRequestBytes bounds one cross-plugin request payload.
	MaxServiceRequestBytes = 256 << 10
	// MaxServiceResponseBytes bounds one cross-plugin response payload.
	MaxServiceResponseBytes = 1 << 20
	// MaxServiceBinaryBytes bounds one binary request or response payload.
	MaxServiceBinaryBytes = 4 << 20
	// MaxServiceEventBytes bounds one service event payload.
	MaxServiceEventBytes = 64 << 10
	// MaxServiceFilterFields bounds an exact-match event filter.
	MaxServiceFilterFields = 8
	// MaxServiceCallDepth is the maximum synchronous cross-plugin depth.
	MaxServiceCallDepth = 4
	// MaxServiceProviderQueue bounds requests waiting for one busy provider.
	MaxServiceProviderQueue = 8
	// MaxServiceEventQueue bounds one subscriber's event queue.
	MaxServiceEventQueue = 256
	// MaxServiceEventsPerSecond bounds one provider's event emission rate.
	MaxServiceEventsPerSecond = 100
	// MaxServiceSubscriptions bounds one consumer's service event
	// subscriptions.
	MaxServiceSubscriptions = 64
	// DefaultServiceStreamIdle is the idle close for a cross-plugin stream.
	DefaultServiceStreamIdle = 30
	// DefaultServiceProvisionVersion is the version assumed when a provider
	// omits it. It keeps early manifests valid while every comparison remains
	// semver.
	DefaultServiceProvisionVersion = "1.0.0"
)

// Service encodings. The set is closed.
const (
	ServiceEncodingJSON   = "json"
	ServiceEncodingBinary = "binary"
)

// Service content types accepted for binary payloads. The allowlist is closed
// and deliberately small: the host never interprets these bytes.
var serviceContentTypes = []string{
	"application/octet-stream",
	"application/cbor",
	"application/protobuf",
}

// ServiceContentTypes returns the binary content-type allowlist.
func ServiceContentTypes() []string {
	return append([]string(nil), serviceContentTypes...)
}

// IsServiceContentType reports whether contentType is on the allowlist.
func IsServiceContentType(contentType string) bool {
	for _, allowed := range serviceContentTypes {
		if contentType == allowed {
			return true
		}
	}
	return false
}

// Service timeout classes. A class maps to the host's bounded call deadlines.
const (
	ServiceTimeoutDefault = "default"
	ServiceTimeoutLong    = "long"
)

// Service method and event name grammar. The service segment is restricted
// further than the plugin id grammar so a method cannot smuggle a path-like
// segment into a broker identity.
var (
	serviceSegmentPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	serviceMemberPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
)

// Provision declares one service this plugin offers to consumers in its
// isolate scope. A plugin may provide only names under its own
// "plugin.<plugin-id>." prefix.
type Provision struct {
	Service  string              `json:"service" toml:"service"`
	Version  string              `json:"version,omitempty" toml:"version"`
	Priority int                 `json:"priority,omitempty" toml:"priority"`
	Access   string              `json:"access,omitempty" toml:"access"`
	Dynamic  bool                `json:"dynamic,omitempty" toml:"dynamic"`
	Methods  []ServiceMethodDecl `json:"methods,omitempty" toml:"methods"`
	Events   []ServiceEventDecl  `json:"events,omitempty" toml:"events"`
}

// ServiceMethodDecl is one method in a provider's table. ArgsSchema and
// ResultSchema use the host's structural schema subset, not full JSON Schema.
type ServiceMethodDecl struct {
	Name         string         `json:"name" toml:"name"`
	Encoding     string         `json:"encoding,omitempty" toml:"encoding"`
	ContentType  string         `json:"content_type,omitempty" toml:"content_type"`
	Access       string         `json:"access,omitempty" toml:"access"`
	Timeout      string         `json:"timeout,omitempty" toml:"timeout"`
	ArgsSchema   map[string]any `json:"args_schema,omitempty" toml:"args_schema"`
	ResultSchema map[string]any `json:"result_schema,omitempty" toml:"result_schema"`
}

// ServiceEventDecl is one event in a provider's table.
type ServiceEventDecl struct {
	Name        string         `json:"name" toml:"name"`
	Encoding    string         `json:"encoding,omitempty" toml:"encoding"`
	ContentType string         `json:"content_type,omitempty" toml:"content_type"`
	Schema      map[string]any `json:"schema,omitempty" toml:"schema"`
}

// EffectiveVersion returns the declared version or the default that keeps a
// minimal declaration valid.
func (p Provision) EffectiveVersion() string {
	if strings.TrimSpace(p.Version) == "" {
		return DefaultServiceProvisionVersion
	}
	return p.Version
}

// EffectiveAccess returns the declared service access class, defaulting to
// read-only.
func (p Provision) EffectiveAccess() string {
	if p.Access == "" {
		return ServiceAccessRead
	}
	return p.Access
}

// Method returns one declared method.
func (p Provision) Method(name string) (ServiceMethodDecl, bool) {
	for _, method := range p.Methods {
		if method.Name == name {
			return method, true
		}
	}
	return ServiceMethodDecl{}, false
}

// Event returns one declared event.
func (p Provision) Event(name string) (ServiceEventDecl, bool) {
	for _, event := range p.Events {
		if event.Name == name {
			return event, true
		}
	}
	return ServiceEventDecl{}, false
}

// MethodNames returns the declared method names in table order.
func (p Provision) MethodNames() []string {
	out := make([]string, 0, len(p.Methods))
	for _, method := range p.Methods {
		out = append(out, method.Name)
	}
	return out
}

// EventNames returns the declared event names in table order.
func (p Provision) EventNames() []string {
	out := make([]string, 0, len(p.Events))
	for _, event := range p.Events {
		out = append(out, event.Name)
	}
	return out
}

// EffectiveAccess returns the method's access override or the service access.
func (m ServiceMethodDecl) EffectiveAccess(serviceAccess string) string {
	if m.Access != "" {
		return m.Access
	}
	if serviceAccess != "" {
		return serviceAccess
	}
	return ServiceAccessRead
}

// EffectiveEncoding returns the method encoding, defaulting to JSON.
func (m ServiceMethodDecl) EffectiveEncoding() string {
	if m.Encoding == "" {
		return ServiceEncodingJSON
	}
	return m.Encoding
}

// EffectiveTimeout returns the declared timeout class, defaulting to the
// ordinary cross-plugin deadline.
func (m ServiceMethodDecl) EffectiveTimeout() string {
	if m.Timeout == "" {
		return ServiceTimeoutDefault
	}
	return m.Timeout
}

// EffectiveEncoding returns the event encoding, defaulting to JSON.
func (e ServiceEventDecl) EffectiveEncoding() string {
	if e.Encoding == "" {
		return ServiceEncodingJSON
	}
	return e.Encoding
}

// CloneProvision copies a declaration so callers cannot mutate the manifest
// through a provider registry.
func CloneProvision(p Provision) Provision {
	out := p
	out.Methods = make([]ServiceMethodDecl, len(p.Methods))
	for i, method := range p.Methods {
		out.Methods[i] = method
		out.Methods[i].ArgsSchema = cloneSchema(method.ArgsSchema)
		out.Methods[i].ResultSchema = cloneSchema(method.ResultSchema)
	}
	out.Events = make([]ServiceEventDecl, len(p.Events))
	for i, event := range p.Events {
		out.Events[i] = event
		out.Events[i].Schema = cloneSchema(event.Schema)
	}
	return out
}

func cloneSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	return cloneSchemaValue(schema).(map[string]any)
}

func cloneSchemaValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneSchemaValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneSchemaValue(item)
		}
		return out
	default:
		return typed
	}
}

// ValidateServiceName checks the shared plugin service grammar:
// plugin.<plugin-id>.<service>, lowercase, at most 128 characters, with a
// restricted lowercase service segment.
func ValidateServiceName(name string) error {
	if name == "" {
		return fmt.Errorf("service name is required")
	}
	if len(name) > MaxServiceNameLength {
		return fmt.Errorf("service name %q exceeds %d characters", name, MaxServiceNameLength)
	}
	if !strings.HasPrefix(name, "plugin.") {
		return fmt.Errorf("service name %q must begin with plugin.", name)
	}
	rest := strings.TrimPrefix(name, "plugin.")
	dot := strings.LastIndexByte(rest, '.')
	if dot <= 0 || dot == len(rest)-1 {
		return fmt.Errorf("service name %q must be plugin.<plugin-id>.<service>", name)
	}
	pluginID, service := rest[:dot], rest[dot+1:]
	if !pluginIDPattern.MatchString(pluginID) {
		return fmt.Errorf("service name %q has an invalid plugin id", name)
	}
	if !serviceSegmentPattern.MatchString(service) {
		return fmt.Errorf("service name %q has an invalid service segment; use [a-z][a-z0-9_]{0,31}", name)
	}
	return nil
}

// ServiceProviderID returns the plugin id encoded in a service name.
func ServiceProviderID(name string) string {
	rest := strings.TrimPrefix(name, "plugin.")
	dot := strings.LastIndexByte(rest, '.')
	if dot <= 0 {
		return ""
	}
	return rest[:dot]
}

// OwnsServiceName reports whether name is under the plugin's own prefix. A
// provider can never publish another plugin's namespace.
func OwnsServiceName(pluginID, name string) bool {
	prefix := "plugin." + pluginID + "."
	return strings.HasPrefix(name, prefix) && len(name) > len(prefix)
}

// ValidateInjections checks the declared injections for name grammar,
// semver ranges, and duplicates. Optional and required declarations of the
// same service are rejected: a service is either required or not.
func ValidateInjections(injections []Injection) error {
	seen := make(map[string]bool, len(injections))
	for i, injection := range injections {
		if err := ValidateServiceName(injection.Service); err != nil {
			return fmt.Errorf("inject[%d]: %w", i, err)
		}
		if injection.Version != "" && !ValidRange(injection.Version) {
			return fmt.Errorf("inject[%d].version: %q is not a supported semver range", i, injection.Version)
		}
		if seen[injection.Service] {
			return fmt.Errorf("inject[%d]: %q is declared twice", i, injection.Service)
		}
		seen[injection.Service] = true
	}
	return nil
}

// ValidateProvisions checks the declared provisions against the provider's
// own namespace, the declaration limits, and the structural schema subset.
func ValidateProvisions(pluginID string, provisions []Provision) error {
	if len(provisions) > MaxServicesPerPlugin {
		return fmt.Errorf("provides: %d services exceeds the limit of %d",
			len(provisions), MaxServicesPerPlugin)
	}
	seen := make(map[string]bool, len(provisions))
	for i, provision := range provisions {
		where := fmt.Sprintf("provides[%d]", i)
		if err := ValidateServiceName(provision.Service); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
		if !OwnsServiceName(pluginID, provision.Service) {
			return fmt.Errorf("%s: %q is not under plugin.%s.",
				where, provision.Service, pluginID)
		}
		if seen[provision.Service] {
			return fmt.Errorf("%s: %q is declared twice", where, provision.Service)
		}
		seen[provision.Service] = true
		if err := provision.validate(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	return nil
}

func (p Provision) validate() error {
	version := p.EffectiveVersion()
	if _, ok := ParseSemver(version); !ok {
		return fmt.Errorf("version %q is not a SemVer 2.0.0 version", version)
	}
	if p.Access != "" && p.Access != ServiceAccessRead && p.Access != ServiceAccessWrite {
		return fmt.Errorf("access must be read or write")
	}
	if len(p.Methods) > MaxMethodsPerService {
		return fmt.Errorf("%d methods exceeds the limit of %d", len(p.Methods), MaxMethodsPerService)
	}
	if len(p.Events) > MaxEventsPerService {
		return fmt.Errorf("%d events exceeds the limit of %d", len(p.Events), MaxEventsPerService)
	}
	methods := make(map[string]bool, len(p.Methods))
	for i, method := range p.Methods {
		where := fmt.Sprintf("methods[%d]", i)
		if !serviceMemberPattern.MatchString(method.Name) {
			return fmt.Errorf("%s.name: %q must match [a-z][a-z0-9_]{0,63}", where, method.Name)
		}
		if methods[method.Name] {
			return fmt.Errorf("%s.name: duplicate method %q", where, method.Name)
		}
		methods[method.Name] = true
		if err := method.validate(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	events := make(map[string]bool, len(p.Events))
	for i, event := range p.Events {
		where := fmt.Sprintf("events[%d]", i)
		if !serviceMemberPattern.MatchString(event.Name) {
			return fmt.Errorf("%s.name: %q must match [a-z][a-z0-9_]{0,63}", where, event.Name)
		}
		if events[event.Name] {
			return fmt.Errorf("%s.name: duplicate event %q", where, event.Name)
		}
		events[event.Name] = true
		if err := event.validate(); err != nil {
			return fmt.Errorf("%s: %w", where, err)
		}
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("declaration is not serializable: %v", err)
	}
	if len(encoded) > MaxServiceDeclarationBytes {
		return fmt.Errorf("declaration is %d bytes, above the %d byte limit",
			len(encoded), MaxServiceDeclarationBytes)
	}
	return nil
}

func (m ServiceMethodDecl) validate() error {
	encoding := m.EffectiveEncoding()
	switch encoding {
	case ServiceEncodingJSON:
		if m.ContentType != "" {
			return fmt.Errorf("content_type is only valid for binary methods")
		}
	case ServiceEncodingBinary:
		if !IsServiceContentType(m.ContentType) {
			return fmt.Errorf("content_type %q is not on the binary allowlist", m.ContentType)
		}
	default:
		return fmt.Errorf("encoding %q must be json or binary", m.Encoding)
	}
	if m.Access != "" && m.Access != ServiceAccessRead && m.Access != ServiceAccessWrite {
		return fmt.Errorf("access must be read or write")
	}
	switch m.EffectiveTimeout() {
	case ServiceTimeoutDefault, ServiceTimeoutLong:
	default:
		return fmt.Errorf("timeout %q must be default or long", m.Timeout)
	}
	if err := validateSchemaField("args_schema", m.ArgsSchema); err != nil {
		return err
	}
	return validateSchemaField("result_schema", m.ResultSchema)
}

func (e ServiceEventDecl) validate() error {
	switch e.EffectiveEncoding() {
	case ServiceEncodingJSON:
		if e.ContentType != "" {
			return fmt.Errorf("content_type is only valid for binary events")
		}
	case ServiceEncodingBinary:
		if !IsServiceContentType(e.ContentType) {
			return fmt.Errorf("content_type %q is not on the binary allowlist", e.ContentType)
		}
	default:
		return fmt.Errorf("encoding %q must be json or binary", e.Encoding)
	}
	return validateSchemaField("schema", e.Schema)
}

func validateSchemaField(field string, schema map[string]any) error {
	if schema == nil {
		return nil
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("%s is not serializable: %v", field, err)
	}
	if len(encoded) > MaxServiceSchemaBytes {
		return fmt.Errorf("%s is %d bytes, above the %d byte limit",
			field, len(encoded), MaxServiceSchemaBytes)
	}
	if err := validateSchema(schema, 1); err != nil {
		return fmt.Errorf("%s: %v", field, err)
	}
	return nil
}

// validateSchema checks the host's structural schema subset. It never
// interprets formats, patterns, or references; validation is deliberately
// structural so a provider cannot make the host load arbitrary schemas.
func validateSchema(schema map[string]any, depth int) error {
	if depth > 16 {
		return fmt.Errorf("schema nesting exceeds 16 levels")
	}
	for key := range schema {
		switch key {
		case "type", "properties", "required", "items", "additionalProperties", "enum":
		default:
			return fmt.Errorf("unsupported schema keyword %q", key)
		}
	}
	if raw, ok := schema["type"]; ok {
		typeName, ok := raw.(string)
		if !ok {
			return fmt.Errorf("type must be a string")
		}
		switch typeName {
		case "object", "array", "string", "number", "integer", "boolean", "null":
		default:
			return fmt.Errorf("unsupported type %q", typeName)
		}
	}
	properties, hasProperties := schema["properties"]
	if hasProperties {
		table, ok := properties.(map[string]any)
		if !ok {
			return fmt.Errorf("properties must be an object")
		}
		for name, raw := range table {
			if name == "" {
				return fmt.Errorf("a property name must not be empty")
			}
			nested, ok := raw.(map[string]any)
			if !ok {
				return fmt.Errorf("property %q must be a schema object", name)
			}
			if err := validateSchema(nested, depth+1); err != nil {
				return fmt.Errorf("property %q: %v", name, err)
			}
		}
	}
	required, hasRequired := schema["required"]
	if hasRequired {
		list, ok := required.([]any)
		if !ok {
			return fmt.Errorf("required must be an array of property names")
		}
		seen := make(map[string]bool, len(list))
		for _, raw := range list {
			name, ok := raw.(string)
			if !ok || name == "" {
				return fmt.Errorf("required must contain non-empty strings")
			}
			if seen[name] {
				return fmt.Errorf("required repeats %q", name)
			}
			seen[name] = true
			if hasProperties {
				table, _ := properties.(map[string]any)
				if _, declared := table[name]; !declared {
					return fmt.Errorf("required names undeclared property %q", name)
				}
			}
		}
	}
	if items, ok := schema["items"]; ok {
		nested, ok := items.(map[string]any)
		if !ok {
			return fmt.Errorf("items must be a schema object")
		}
		if err := validateSchema(nested, depth+1); err != nil {
			return fmt.Errorf("items: %v", err)
		}
	}
	if raw, ok := schema["additionalProperties"]; ok {
		if _, isBool := raw.(bool); !isBool {
			return fmt.Errorf("additionalProperties must be a boolean")
		}
	}
	if raw, ok := schema["enum"]; ok {
		if _, isList := raw.([]any); !isList {
			return fmt.Errorf("enum must be an array")
		}
	}
	return nil
}

// ValidateServicePayload decodes a JSON payload, enforces the transport
// rules, and validates it against an optional structural schema. A nil schema
// accepts any structurally valid JSON value.
func ValidateServicePayload(encoding string, payload map[string]any, data []byte, limit int) error {
	switch encoding {
	case ServiceEncodingBinary:
		if len(data) > MaxServiceBinaryBytes {
			return &ServicePayloadError{
				Code:    "limit",
				Message: fmt.Sprintf("binary payload is %d bytes, above the %d byte limit", len(data), MaxServiceBinaryBytes),
			}
		}
		return nil
	default:
		if limit <= 0 {
			limit = MaxServiceRequestBytes
		}
		if len(data) > limit {
			return &ServicePayloadError{
				Code:    "limit",
				Message: fmt.Sprintf("payload is %d bytes, above the %d byte limit", len(data), limit),
			}
		}
		if !utf8.Valid(data) {
			return &ServicePayloadError{Code: "invalid-argument", Message: "payload is not valid UTF-8"}
		}
		value, err := DecodeServiceJSON(data)
		if err != nil {
			return &ServicePayloadError{Code: "invalid-argument", Message: err.Error()}
		}
		if err := ValidateServiceJSONValue(value); err != nil {
			return &ServicePayloadError{Code: "invalid-argument", Message: err.Error()}
		}
		if payload != nil {
			if err := ValidateAgainstSchema(payload, value); err != nil {
				return &ServicePayloadError{Code: "invalid-argument", Message: err.Error()}
			}
		}
		return nil
	}
}

// ServicePayloadError is one payload rule violation. Code is a member of the
// cross-plugin closed error set.
type ServicePayloadError struct {
	Code    string
	Message string
}

func (e *ServicePayloadError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// DecodeServiceJSON decodes one JSON value with json.Number preserved. It
// rejects trailing data and non-finite numbers, which encoding/json's literal
// grammar already excludes.
func DecodeServiceJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("payload is not valid JSON: %v", err)
	}
	if decoder.More() {
		return nil, fmt.Errorf("payload has trailing JSON data")
	}
	return value, nil
}

// ValidateServiceJSONValue enforces the nesting, key-size, and number rules on
// a decoded JSON value.
func ValidateServiceJSONValue(value any) error {
	return validateJSONValue(value, 0)
}

func validateJSONValue(value any, depth int) error {
	switch typed := value.(type) {
	case map[string]any:
		if depth >= MaxServiceJSONDepth {
			return fmt.Errorf("payload nests more than %d levels", MaxServiceJSONDepth)
		}
		for key, item := range typed {
			if len(key) > MaxServiceJSONKeyBytes {
				return fmt.Errorf("payload key %q exceeds %d bytes", key, MaxServiceJSONKeyBytes)
			}
			if err := validateJSONValue(item, depth+1); err != nil {
				return err
			}
		}
	case []any:
		if depth >= MaxServiceJSONDepth {
			return fmt.Errorf("payload nests more than %d levels", MaxServiceJSONDepth)
		}
		for _, item := range typed {
			if err := validateJSONValue(item, depth+1); err != nil {
				return err
			}
		}
	case json.Number:
		number := string(typed)
		if number == "NaN" || strings.ContainsAny(number, "+-Inf") && !validJSONNumber(number) {
			return fmt.Errorf("payload contains a non-finite number")
		}
		if _, err := typed.Float64(); err != nil {
			return fmt.Errorf("payload number %q is not finite", number)
		}
	case string, bool, nil:
		return nil
	default:
		return fmt.Errorf("payload contains an unsupported JSON value %T", value)
	}
	return nil
}

func validJSONNumber(number string) bool {
	if number == "" {
		return false
	}
	for i, r := range number {
		switch r {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		case '-':
			if i != 0 {
				return false
			}
		case '.', 'e', 'E', '+':
		default:
			return false
		}
	}
	return true
}

// ValidateAgainstSchema checks one decoded JSON value against the structural
// schema subset. A nil schema accepts anything.
func ValidateAgainstSchema(schema map[string]any, value any) error {
	if schema == nil {
		return nil
	}
	if raw, ok := schema["type"]; ok {
		typeName, _ := raw.(string)
		if !schemaTypeMatches(typeName, value) {
			return fmt.Errorf("want %s, got %s", typeName, jsonTypeName(value))
		}
	}
	if raw, ok := schema["enum"]; ok {
		if list, ok := raw.([]any); ok && !enumContains(list, value) {
			return fmt.Errorf("value is not one of the declared enum values")
		}
	}
	if table, ok := schema["properties"].(map[string]any); ok {
		object, isObject := value.(map[string]any)
		if isObject {
			if list, ok := schema["required"].([]any); ok {
				for _, raw := range list {
					name, _ := raw.(string)
					if _, present := object[name]; !present {
						return fmt.Errorf("required property %q is missing", name)
					}
				}
			}
			additional := true
			if raw, ok := schema["additionalProperties"]; ok {
				if flag, ok := raw.(bool); ok {
					additional = flag
				}
			}
			for name, item := range object {
				nested, declared := table[name].(map[string]any)
				if !declared {
					if !additional {
						return fmt.Errorf("property %q is not declared", name)
					}
					continue
				}
				if err := ValidateAgainstSchema(nested, item); err != nil {
					return fmt.Errorf("property %q: %v", name, err)
				}
			}
		}
	}
	if nested, ok := schema["items"].(map[string]any); ok {
		if list, isList := value.([]any); isList {
			for i, item := range list {
				if err := ValidateAgainstSchema(nested, item); err != nil {
					return fmt.Errorf("item %d: %v", i, err)
				}
			}
		}
	}
	return nil
}

func schemaTypeMatches(typeName string, value any) bool {
	switch typeName {
	case "":
		return true
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	}
	return false
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case json.Number:
		return "number"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func enumContains(list []any, value any) bool {
	for _, candidate := range list {
		if jsonValuesEqual(candidate, value) {
			return true
		}
	}
	return false
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

// ProvisionHash returns a stable digest of the method and event table, so the
// host can expose exactly which contract a consumer is bound to.
func ProvisionHash(p Provision) string {
	canonical := Provision{
		Service: p.Service,
		Version: p.EffectiveVersion(),
		Access:  p.EffectiveAccess(),
		Methods: p.Methods,
		Events:  p.Events,
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// SortedServiceNames returns service names in stable order.
func SortedServiceNames(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}
