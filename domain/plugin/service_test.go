package plugin

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestValidateServiceNameGrammar(t *testing.T) {
	valid := []string{
		"plugin.acme.echo",
		"plugin.acme.service-provider.echo_v1",
		"plugin.a.b",
		"plugin.acme.a1234567890123456789012345678901",
	}
	for _, name := range valid {
		if err := ValidateServiceName(name); err != nil {
			t.Fatalf("ValidateServiceName(%q) = %v", name, err)
		}
	}
	invalid := []string{
		"",
		"acme.echo",
		"plugin.acme",
		"plugin..echo",
		"plugin.acme.",
		"plugin.Acme.echo",
		"plugin.acme.Echo",
		"plugin.acme.9echo",
		"plugin.acme.echo-1",
		"plugin.acme." + strings.Repeat("a", 33),
	}
	for _, name := range invalid {
		if err := ValidateServiceName(name); err == nil {
			t.Fatalf("ValidateServiceName(%q) accepted an invalid name", name)
		}
	}
}

func TestServiceProviderIdentity(t *testing.T) {
	if got := ServiceProviderID("plugin.acme.one.echo"); got != "acme.one" {
		t.Fatalf("ServiceProviderID = %q", got)
	}
	if got := ServiceProviderID("not-a-service"); got != "" {
		t.Fatalf("ServiceProviderID(invalid) = %q", got)
	}
	if !OwnsServiceName("acme.one", "plugin.acme.one.echo") {
		t.Fatal("owner did not own its own prefix")
	}
	if OwnsServiceName("acme.one", "plugin.acme.two.echo") {
		t.Fatal("owner claimed another plugin's prefix")
	}
	if OwnsServiceName("acme.one", "plugin.acme.one.") {
		t.Fatal("an empty service segment was accepted")
	}
}

func TestProvisionEffectiveDefaults(t *testing.T) {
	provision := Provision{Service: "plugin.acme.echo"}
	if provision.EffectiveVersion() != DefaultServiceProvisionVersion {
		t.Fatalf("version = %q", provision.EffectiveVersion())
	}
	if provision.EffectiveAccess() != ServiceAccessRead {
		t.Fatalf("access = %q", provision.EffectiveAccess())
	}
	method := ServiceMethodDecl{Name: "echo"}
	if method.EffectiveEncoding() != ServiceEncodingJSON {
		t.Fatalf("encoding = %q", method.EffectiveEncoding())
	}
	if method.EffectiveTimeout() != ServiceTimeoutDefault {
		t.Fatalf("timeout = %q", method.EffectiveTimeout())
	}
	if method.EffectiveAccess(ServiceAccessWrite) != ServiceAccessWrite {
		t.Fatal("method did not inherit the service access class")
	}
	if method.EffectiveAccess("") != ServiceAccessRead {
		t.Fatal("an empty service access did not default to read")
	}
	event := ServiceEventDecl{Name: "tick"}
	if event.EffectiveEncoding() != ServiceEncodingJSON {
		t.Fatalf("event encoding = %q", event.EffectiveEncoding())
	}
}

func TestValidateProvisions(t *testing.T) {
	valid := []Provision{{
		Service: "plugin.acme.echo",
		Version: "1.2.0",
		Access:  ServiceAccessRead,
		Methods: []ServiceMethodDecl{
			{
				Name:         "echo",
				Encoding:     ServiceEncodingJSON,
				ArgsSchema:   map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []any{"text"}},
				ResultSchema: map[string]any{"type": "string"},
			},
			{Name: "send", Encoding: ServiceEncodingBinary, ContentType: "application/cbor", Access: ServiceAccessWrite, Timeout: ServiceTimeoutLong},
		},
		Events: []ServiceEventDecl{
			{Name: "tick", Schema: map[string]any{"type": "object", "properties": map[string]any{"sequence": map[string]any{"type": "integer"}}, "required": []any{"sequence"}}},
			{Name: "blob", Encoding: ServiceEncodingBinary, ContentType: "application/octet-stream"},
		},
	}, {
		Service: "plugin.acme.dynamic",
		Dynamic: true,
	}}
	if err := ValidateProvisions("acme", valid); err != nil {
		t.Fatalf("ValidateProvisions: %v", err)
	}

	cases := []struct {
		name     string
		pluginID string
		provided []Provision
		want     string
	}{
		{"wrong prefix", "acme", []Provision{{Service: "plugin.other.echo"}}, "not under plugin.acme."},
		{"duplicate service", "acme", []Provision{{Service: "plugin.acme.echo"}, {Service: "plugin.acme.echo"}}, "declared twice"},
		{"bad version", "acme", []Provision{{Service: "plugin.acme.echo", Version: "v1.0.0"}}, "SemVer"},
		{"bad access", "acme", []Provision{{Service: "plugin.acme.echo", Access: "full"}}, "access must be read or write"},
		{"duplicate method", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo"}, {Name: "echo"}}}}, "duplicate method"},
		{"bad method name", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "Echo"}}}}, "must match"},
		{"duplicate event", "acme", []Provision{{Service: "plugin.acme.echo", Events: []ServiceEventDecl{{Name: "tick"}, {Name: "tick"}}}}, "duplicate event"},
		{"json content type", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", ContentType: "application/cbor"}}}}, "content_type is only valid for binary"},
		{"binary allowlist", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", Encoding: ServiceEncodingBinary, ContentType: "text/plain"}}}}, "allowlist"},
		{"bad encoding", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", Encoding: "xml"}}}}, "json or binary"},
		{"bad timeout", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", Timeout: "forever"}}}}, "default or long"},
		{"bad schema keyword", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", ArgsSchema: map[string]any{"pattern": "^x$"}}}}}, "unsupported schema keyword"},
		{"bad schema type", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", ArgsSchema: map[string]any{"type": "date"}}}}}, "unsupported type"},
		{"required undeclared", "acme", []Provision{{Service: "plugin.acme.echo", Methods: []ServiceMethodDecl{{Name: "echo", ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []any{"missing"}}}}}}, "undeclared property"},
		{"event content type", "acme", []Provision{{Service: "plugin.acme.echo", Events: []ServiceEventDecl{{Name: "tick", ContentType: "application/cbor"}}}}, "content_type is only valid for binary"},
		{"event schema", "acme", []Provision{{Service: "plugin.acme.echo", Events: []ServiceEventDecl{{Name: "tick", Schema: map[string]any{"items": "not-an-object"}}}}}, "items must be a schema object"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateProvisions(test.pluginID, test.provided)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateProvisionsLimits(t *testing.T) {
	services := make([]Provision, MaxServicesPerPlugin+1)
	for i := range services {
		services[i].Service = "plugin.acme.service" + strconv.Itoa(i)
	}
	if err := ValidateProvisions("acme", services); err == nil ||
		!strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("service limit error = %v", err)
	}

	methods := make([]ServiceMethodDecl, MaxMethodsPerService+1)
	for i := range methods {
		methods[i].Name = "method" + strconv.Itoa(i)
	}
	if err := ValidateProvisions("acme", []Provision{{Service: "plugin.acme.echo", Methods: methods}}); err == nil ||
		!strings.Contains(err.Error(), "exceeds the limit") {
		t.Fatalf("method limit error = %v", err)
	}
}

func TestValidateInjections(t *testing.T) {
	if err := ValidateInjections([]Injection{
		{Service: "plugin.acme.echo", Version: "^1.0"},
		{Service: "plugin.acme.other", Optional: true},
	}); err != nil {
		t.Fatalf("ValidateInjections: %v", err)
	}
	cases := []struct {
		name       string
		injections []Injection
		want       string
	}{
		{"bad name", []Injection{{Service: "acme.echo"}}, "must begin with plugin."},
		{"bad range", []Injection{{Service: "plugin.acme.echo", Version: "one"}}, "semver range"},
		{"duplicate", []Injection{{Service: "plugin.acme.echo"}, {Service: "plugin.acme.echo", Optional: true}}, "declared twice"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateInjections(test.injections)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestProvisionHashAndClone(t *testing.T) {
	provision := Provision{
		Service: "plugin.acme.echo",
		Version: "1.2.0",
		Methods: []ServiceMethodDecl{{
			Name: "echo",
			ArgsSchema: map[string]any{"type": "object", "properties": map[string]any{
				"text": map[string]any{"type": "string"},
			}},
		}},
		Events: []ServiceEventDecl{{Name: "tick", Schema: map[string]any{"type": "string"}}},
	}
	hash := ProvisionHash(provision)
	if hash == "" || len(hash) != 64 {
		t.Fatalf("hash = %q", hash)
	}
	if again := ProvisionHash(provision); again != hash {
		t.Fatal("ProvisionHash is not stable")
	}
	changed := provision
	changed.Version = "1.3.0"
	if ProvisionHash(changed) == hash {
		t.Fatal("a version change did not change the table hash")
	}

	clone := CloneProvision(provision)
	clone.Methods[0].ArgsSchema["type"] = "string"
	clone.Events[0].Schema["type"] = "integer"
	clone.Methods[0].Name = "changed"
	if provision.Methods[0].Name != "echo" {
		t.Fatal("CloneProvision shared the method table")
	}
	if provision.Methods[0].ArgsSchema["type"] != "object" ||
		provision.Events[0].Schema["type"] != "string" {
		t.Fatal("CloneProvision shared a nested schema")
	}
}

func TestValidateServicePayloadJSONRules(t *testing.T) {
	nested := any(map[string]any{})
	for i := 0; i < MaxServiceJSONDepth-1; i++ {
		nested = map[string]any{"child": nested}
	}
	if err := ValidateServiceJSONValue(nested); err != nil {
		t.Fatalf("32 nested levels: %v", err)
	}
	deeper := map[string]any{"child": nested}
	if err := ValidateServiceJSONValue(deeper); err == nil {
		t.Fatal("33 nested levels were accepted")
	}

	key := strings.Repeat("k", MaxServiceJSONKeyBytes+1)
	if err := ValidateServiceJSONValue(map[string]any{key: true}); err == nil {
		t.Fatal("an oversized key was accepted")
	}

	if _, err := DecodeServiceJSON([]byte(`{"x":NaN}`)); err == nil {
		t.Fatal("NaN was accepted")
	}
	if _, err := DecodeServiceJSON([]byte(`{"x":1e999}`)); err != nil {
		t.Fatalf("decoding 1e999 failed: %v", err)
	}
	value, _ := DecodeServiceJSON([]byte(`{"x":1e999}`))
	if err := ValidateServiceJSONValue(value); err == nil {
		t.Fatal("Infinity was accepted")
	}
	if _, err := DecodeServiceJSON([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}

	if err := ValidateServicePayload(ServiceEncodingJSON, nil, []byte(`{"ok":true}`), 0); err != nil {
		t.Fatalf("valid JSON payload: %v", err)
	}
	if err := ValidateServicePayload(ServiceEncodingJSON, nil, []byte{0xff, 0xfe}, 0); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
	payloadErr := ValidateServicePayload(ServiceEncodingJSON, nil, []byte(`{"ok":true}`), 4)
	var typed *ServicePayloadError
	if !errors.As(payloadErr, &typed) || typed.Code != "limit" {
		t.Fatalf("limit error = %v", payloadErr)
	}
	if err := ValidateServicePayload(ServiceEncodingBinary, nil, make([]byte, MaxServiceBinaryBytes+1), 0); err == nil {
		t.Fatal("an oversized binary payload was accepted")
	}
	if err := ValidateServicePayload(ServiceEncodingBinary, nil, []byte{0xff}, 1); err != nil {
		t.Fatalf("binary payload: %v", err)
	}
}

func TestValidateAgainstSchemaSubset(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"text":  map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"mode":  map[string]any{"enum": []any{"fast", "slow"}},
		},
		"required":             []any{"text"},
		"additionalProperties": false,
	}
	decode := func(raw string) any {
		value, err := DecodeServiceJSON([]byte(raw))
		if err != nil {
			t.Fatalf("decode %q: %v", raw, err)
		}
		return value
	}
	if err := ValidateAgainstSchema(schema, decode(`{"text":"a","count":2,"tags":["x"],"mode":"fast"}`)); err != nil {
		t.Fatalf("valid value: %v", err)
	}
	cases := map[string]string{
		"wrong type":       `{"text":1}`,
		"missing required": `{"count":1}`,
		"undeclared":       `{"text":"a","extra":true}`,
		"bad integer":      `{"text":"a","count":1.5}`,
		"bad item":         `{"text":"a","tags":[1]}`,
		"bad enum":         `{"text":"a","mode":"medium"}`,
	}
	for name, raw := range cases {
		if err := ValidateAgainstSchema(schema, decode(raw)); err == nil {
			t.Fatalf("%s: value %s was accepted", name, raw)
		}
	}
	if err := ValidateAgainstSchema(nil, decode(`{"anything":true}`)); err != nil {
		t.Fatalf("nil schema rejected a value: %v", err)
	}
}

func TestServiceContentTypeAllowlist(t *testing.T) {
	allowed := ServiceContentTypes()
	if len(allowed) == 0 {
		t.Fatal("the content-type allowlist is empty")
	}
	for _, contentType := range allowed {
		if !IsServiceContentType(contentType) {
			t.Fatalf("%q is not accepted", contentType)
		}
	}
	if IsServiceContentType("text/html") {
		t.Fatal("text/html was accepted")
	}
	allowed[0] = "text/html"
	if IsServiceContentType("text/html") {
		t.Fatal("ServiceContentTypes exposed the internal slice")
	}
}

func TestSortedServiceNames(t *testing.T) {
	input := []string{"plugin.acme.c", "plugin.acme.a", "plugin.acme.b"}
	got := SortedServiceNames(input)
	want := "plugin.acme.a,plugin.acme.b,plugin.acme.c"
	if strings.Join(got, ",") != want {
		t.Fatalf("sorted = %v", got)
	}
	if input[0] != "plugin.acme.c" {
		t.Fatal("SortedServiceNames mutated its input")
	}
}

// jsonNumber keeps encoding/json imported for the payload tests and proves a
// decoded number stays a json.Number rather than a float64.
func TestDecodeServiceJSONPreservesNumbers(t *testing.T) {
	value, err := DecodeServiceJSON([]byte(`{"n":9007199254740993}`))
	if err != nil {
		t.Fatalf("DecodeServiceJSON: %v", err)
	}
	object := value.(map[string]any)
	number, ok := object["n"].(json.Number)
	if !ok || string(number) != "9007199254740993" {
		t.Fatalf("number = %#v", object["n"])
	}
}
