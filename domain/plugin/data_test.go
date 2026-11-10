package plugin

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func testDataProvider() DataProvider {
	return DataProvider{
		ID:   "warehouse",
		Name: "Warehouse",
		Capabilities: DataCapabilities{
			Family:   DataFamilyTabular,
			Features: []DataFeature{DataFeatureTransactions, DataFeatureTesting},
		},
		ProfileFields: []DataProfileField{
			{Key: "endpoint", Label: "Endpoint", Kind: DataProfileFieldText, Required: true},
			{Key: "password", Label: "Password", Kind: DataProfileFieldText, Required: true, Secret: true},
			{Key: "tls", Label: "TLS", Kind: DataProfileFieldBoolean},
		},
	}
}

func TestDataProviderValidation(t *testing.T) {
	if err := ValidateDataProvider("acme.data", testDataProvider(), 0); err != nil {
		t.Fatalf("valid provider: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*DataProvider)
		want   string
	}{
		{"bad id", func(p *DataProvider) { p.ID = "Bad ID" }, "id"},
		{"missing name", func(p *DataProvider) { p.Name = "" }, "name"},
		{"unknown family", func(p *DataProvider) { p.Capabilities.Family = "sql" }, "family"},
		{"unknown feature", func(p *DataProvider) {
			p.Capabilities.Features = []DataFeature{"magic"}
		}, "features"},
		{"duplicate feature", func(p *DataProvider) {
			p.Capabilities.Features = []DataFeature{DataFeaturePlans, DataFeaturePlans}
		}, "duplicate"},
		{"bad connection bound", func(p *DataProvider) { p.Capabilities.MaxConnections = 1000 }, "max_connections"},
		{"duplicate field", func(p *DataProvider) { p.ProfileFields = append(p.ProfileFields, p.ProfileFields[0]) }, "duplicate"},
		{"select without options", func(p *DataProvider) {
			p.ProfileFields = append(p.ProfileFields, DataProfileField{
				Key: "mode", Label: "Mode", Kind: DataProfileFieldSelect,
			})
		}, "options"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			provider := testDataProvider()
			test.mutate(&provider)
			err := ValidateDataProvider("acme.data", provider, 0)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want a %q validation error", err, test.want)
			}
		})
	}
}

func TestDataProfileDraftValidation(t *testing.T) {
	provider := testDataProvider()
	qualified := QualifiedDataProviderID("acme.data", provider.ID)
	draft := DataProfileDraft{
		Label:    "Reporting",
		Provider: qualified,
		Mode:     DataModeReadOnly,
		Fields:   map[string]string{"endpoint": "warehouse.example.test:443", "tls": "true"},
		Secrets:  map[string]string{"password": "hunter2"},
	}
	normalized, err := ValidateDataProfileDraft(qualified, provider, draft)
	if err != nil {
		t.Fatalf("valid draft: %v", err)
	}
	if normalized.Mode != DataModeReadOnly || normalized.Fields["endpoint"] == "" {
		t.Fatalf("normalized draft = %+v", normalized)
	}

	emptyMode := draft
	emptyMode.Mode = ""
	normalized, err = ValidateDataProfileDraft(qualified, provider, emptyMode)
	if err != nil || normalized.Mode != DataModeReadOnly {
		t.Fatalf("empty mode normalized to %q, err %v", normalized.Mode, err)
	}

	cases := []struct {
		name   string
		mutate func(*DataProfileDraft)
		want   string
	}{
		{"other provider", func(d *DataProfileDraft) { d.Provider = "plugin.other.x" }, "must name"},
		{"unknown mode", func(d *DataProfileDraft) { d.Mode = "superuser" }, "mode"},
		{"unknown field", func(d *DataProfileDraft) { d.Fields["extra"] = "x" }, "not a declared"},
		{"secret in fields", func(d *DataProfileDraft) { d.Fields["password"] = "x" }, "secret field"},
		{"secret of plain field", func(d *DataProfileDraft) { d.Secrets["endpoint"] = "x" }, "not a secret"},
		{"missing required secret", func(d *DataProfileDraft) { delete(d.Secrets, "password") }, "required"},
		{"missing required field", func(d *DataProfileDraft) { delete(d.Fields, "endpoint") }, "required"},
		{"set and clear", func(d *DataProfileDraft) { d.ClearSecrets = []string{"password"} }, "set and cleared"},
		{"bad select value", func(d *DataProfileDraft) {
			d.Fields["tls"] = "maybe"
		}, "true or false"},
		{"empty secret", func(d *DataProfileDraft) { d.Secrets["password"] = "" }, "must not be empty"},
		{"oversize field", func(d *DataProfileDraft) {
			d.Fields["endpoint"] = strings.Repeat("x", MaxDataProfileFieldValue+1)
		}, "at most"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := draft
			candidate.Fields = map[string]string{}
			candidate.Secrets = map[string]string{}
			for key, value := range draft.Fields {
				candidate.Fields[key] = value
			}
			for key, value := range draft.Secrets {
				candidate.Secrets[key] = value
			}
			candidate.ClearSecrets = nil
			test.mutate(&candidate)
			_, err := ValidateDataProfileDraft(qualified, provider, candidate)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want a %q validation error", err, test.want)
			}
		})
	}
}

func TestDataProfileDraftUpdateKeepsExistingSecretReference(t *testing.T) {
	provider := testDataProvider()
	qualified := QualifiedDataProviderID("acme.data", provider.ID)
	draft := DataProfileDraft{
		Label:      "Reporting",
		Provider:   qualified,
		Fields:     map[string]string{"endpoint": "warehouse.example.test:443", "tls": "true"},
		SecretRefs: map[string]string{"password": "vault:data-profile:p1:password"},
	}
	normalized, err := ValidateDataProfileDraft(qualified, provider, draft)
	if err != nil {
		t.Fatalf("draft with an existing reference: %v", err)
	}
	if normalized.SecretRefs["password"] == "" {
		t.Fatalf("secret reference was dropped: %+v", normalized)
	}

	cleared := draft
	cleared.ClearSecrets = []string{"password"}
	if _, err := ValidateDataProfileDraft(qualified, provider, cleared); err == nil {
		t.Fatal("clearing a required secret was accepted")
	}
}

func TestDataProfileRecordValidationRejectsBadViaHost(t *testing.T) {
	record := DataProfile{
		ID: "profile-1", Provider: "plugin.acme.data.warehouse",
		Label: "P", Mode: DataModeReadOnly, ViaHost: "not a host id",
	}
	if err := ValidateDataProfile(record); err == nil || !strings.Contains(err.Error(), "via_host") {
		t.Fatalf("via_host validation = %v", err)
	}
}

// TestDataProfileViaHostAcceptsRealHostIDs proves via_host accepts the
// canonical ULID shape real Host IDs use while rejecting malformed and
// foreign identifiers.
func TestDataProfileViaHostAcceptsRealHostIDs(t *testing.T) {
	provider := testDataProvider()
	qualified := QualifiedDataProviderID("acme.data", provider.ID)
	const hostID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	draft := DataProfileDraft{
		Label:    "Reporting",
		Provider: qualified,
		Mode:     DataModeReadOnly,
		ViaHost:  hostID,
		Fields:   map[string]string{"endpoint": "warehouse.example.test:443", "tls": "true"},
		Secrets:  map[string]string{"password": "hunter2"},
	}
	if _, err := ValidateDataProfileDraft(qualified, provider, draft); err != nil {
		t.Fatalf("real Host ID was rejected: %v", err)
	}
	record := DataProfile{
		ID: "profile-1", Provider: qualified, Label: "P",
		Mode: DataModeReadOnly, ViaHost: hostID,
	}
	if err := ValidateDataProfile(record); err != nil {
		t.Fatalf("stored profile with a real Host ID was rejected: %v", err)
	}

	invalid := []string{
		"host-42",                     // connection-token shape, not a Host
		"01arz3ndektsv4rrffq69g5fav",  // lowercase Crockford is not canonical
		"01ARZ3NDEKTSV4RRFFQ69G5FA",   // too short
		"01ARZ3NDEKTSV4RRFFQ69G5FAVV", // too long
		"01ARZ3NDEKTSV4RRFFQ69G5FAI",  // I is outside Crockford's alphabet
		"01ARZ3NDEKTSV4RRFFQ69G5FAL",  // L is outside Crockford's alphabet
		"81ARZ3NDEKTSV4RRFFQ69G5FAV",  // first character overflows 128 bits
		"01ARZ3NDEKTSV4RRFFQ69G5FA ",  // trailing space
	}
	for _, viaHost := range invalid {
		candidate := draft
		candidate.ViaHost = viaHost
		if _, err := ValidateDataProfileDraft(qualified, provider, candidate); err == nil ||
			!strings.Contains(err.Error(), "via_host") {
			t.Fatalf("via_host %q accepted or wrong error: %v", viaHost, err)
		}
		stored := record
		stored.ViaHost = viaHost
		if err := ValidateDataProfile(stored); err == nil ||
			!strings.Contains(err.Error(), "via_host") {
			t.Fatalf("stored via_host %q accepted or wrong error: %v", viaHost, err)
		}
	}
}

// TestDataValueWireShapeEnforcesTheClosedTag rejects a wire value that carries
// a payload field belonging to another kind, even when that field is zero.
func TestDataValueWireShapeEnforcesTheClosedTag(t *testing.T) {
	var value DataValue
	err := json.Unmarshal([]byte(`{"kind":"string","string":"x","float":0}`), &value)
	if err == nil {
		t.Fatalf("redundant zero float was accepted: %+v", value)
	}
	err = json.Unmarshal([]byte(`{"kind":"null","bool":false}`), &value)
	if err == nil {
		t.Fatalf("redundant zero bool was accepted: %+v", value)
	}
	err = json.Unmarshal([]byte(`{"kind":"int","int":"1","string":""}`), &value)
	if err == nil {
		t.Fatalf("redundant empty string was accepted: %+v", value)
	}
	err = json.Unmarshal([]byte(`{"kind":"bool","bool":false}`), &value)
	if err != nil || value.Kind != DataValueBool || value.Bool {
		t.Fatalf("canonical false bool = %+v, %v", value, err)
	}
	err = json.Unmarshal([]byte(`{"kind":"float","float":0}`), &value)
	if err != nil || value.Kind != DataValueFloat || value.Float != 0 {
		t.Fatalf("canonical zero float = %+v, %v", value, err)
	}
	err = json.Unmarshal([]byte(`{"kind":"null"}`), &value)
	if err != nil || value.Kind != DataValueNull {
		t.Fatalf("canonical null = %+v, %v", value, err)
	}
}

// TestDataLimitsAcceptTheExactBoundary exercises acceptance at each literal
// limit, not only rejection one unit above it.
func TestDataLimitsAcceptTheExactBoundary(t *testing.T) {
	// Exactly 256 rows in one batch.
	rows := make([][]DataValue, MaxDataBatchRows)
	for i := range rows {
		rows[i] = []DataValue{{Kind: DataValueInt, Int: "1"}}
	}
	table := DataResult{Kind: DataResultTable, Table: &DataTable{
		Columns: []DataColumn{{Name: "n", Type: DataValueInt}},
		Rows:    rows,
	}}
	if err := table.Validate(); err != nil {
		t.Fatalf("256-row batch was rejected: %v", err)
	}

	// Exactly 256 chunks, each with a payload.
	chunks := make([]DataChunk, MaxDataBatchRows)
	for i := range chunks {
		chunks[i] = DataChunk{Offset: int64(i), Data: []byte{1}}
	}
	if err := (DataResult{Kind: DataResultChunks,
		Chunks: &DataChunks{Chunks: chunks}}).Validate(); err != nil {
		t.Fatalf("256-chunk batch was rejected: %v", err)
	}

	// MaxDataValueBytes bounds the raw payload bytes, not the JSON-encoded
	// transport size: a value at the exact raw limit is accepted, and one raw
	// byte above it is rejected. A value containing HTML-significant bytes is
	// accepted at the same raw length because the limit does not tax escaping.
	value := DataValue{Kind: DataValueString, String: strings.Repeat("x", MaxDataValueBytes)}
	if err := value.Validate(); err != nil {
		t.Fatalf("value at the exact raw limit was rejected: %v", err)
	}
	over := value
	over.String += "x"
	if err := over.Validate(); err == nil {
		t.Fatal("value one byte above the raw limit was accepted")
	}
	htmlValue := DataValue{Kind: DataValueString,
		String: strings.Repeat("<>&", MaxDataValueBytes/3) +
			strings.Repeat("<", MaxDataValueBytes%3)}
	if len(htmlValue.String) != MaxDataValueBytes {
		t.Fatalf("HTML-significant value is %d bytes, want %d",
			len(htmlValue.String), MaxDataValueBytes)
	}
	if err := htmlValue.Validate(); err != nil {
		t.Fatalf("a value of HTML-significant bytes within the raw limit was rejected: %v", err)
	}
	htmlValue.String += "x"
	if err := htmlValue.Validate(); err == nil {
		t.Fatal("a value of HTML-significant bytes one byte above the raw limit was accepted")
	}

	// A table batch whose encoded form is exactly MaxDataBatchBytes.
	columns := []DataColumn{{Name: "v", Type: DataValueString}}
	tableProbe, err := json.Marshal(DataTable{
		Columns: columns,
		Rows:    [][]DataValue{{{Kind: DataValueString, String: "x"}}},
	})
	if err != nil {
		t.Fatalf("marshal table probe: %v", err)
	}
	tableBytes := MaxDataBatchBytes - (len(tableProbe) - 1)
	exactTable := DataTable{
		Columns: columns,
		Rows: [][]DataValue{{
			{Kind: DataValueString, String: strings.Repeat("x", tableBytes)},
		}},
	}
	if encoded, _ := json.Marshal(exactTable); len(encoded) != MaxDataBatchBytes {
		t.Fatalf("table batch encodes to %d bytes, want %d",
			len(encoded), MaxDataBatchBytes)
	}
	if err := (DataResult{Kind: DataResultTable, Table: &exactTable}).Validate(); err != nil {
		t.Fatalf("table batch at the exact limit was rejected: %v", err)
	}
	overTable := DataTable{
		Columns: columns,
		Rows: [][]DataValue{{
			{Kind: DataValueString, String: strings.Repeat("x", tableBytes+1)},
		}},
	}
	if err := (DataResult{Kind: DataResultTable, Table: &overTable}).Validate(); err == nil {
		t.Fatal("table batch one byte above the limit was accepted")
	}

	// A chunked batch whose encoded form is exactly MaxDataBatchBytes.
	// Four chunks with single-digit offsets give a fixed 104-byte JSON
	// overhead congruent to the base64 quantum.
	sizes := []int{49131, 49131, 49134, 49134}
	exactChunks := make([]DataChunk, len(sizes))
	for i, size := range sizes {
		exactChunks[i] = DataChunk{Offset: int64(i), Data: make([]byte, size)}
	}
	exact := DataResult{Kind: DataResultChunks, Chunks: &DataChunks{Chunks: exactChunks}}
	encoded, err := json.Marshal(exact.Chunks)
	if err != nil {
		t.Fatalf("marshal exact chunk batch: %v", err)
	}
	if len(encoded) != MaxDataBatchBytes {
		t.Fatalf("chunk batch encodes to %d bytes, want %d", len(encoded), MaxDataBatchBytes)
	}
	if err := exact.Validate(); err != nil {
		t.Fatalf("chunk batch at the exact limit was rejected: %v", err)
	}
	exact.Chunks.Chunks[len(exact.Chunks.Chunks)-1].Data = append(
		exact.Chunks.Chunks[len(exact.Chunks.Chunks)-1].Data, 0, 0, 0)
	if err := exact.Validate(); err == nil {
		t.Fatal("chunk batch above the byte limit was accepted")
	}
}

// Plan text, node text, and the summary message are values too: their
// per-value limit is measured on raw bytes, so an HTML-significant string at
// the exact raw bound is accepted and one raw byte above is rejected regardless
// of the JSON escaping the transport chooses.
func TestDataDiagnosticTextUsesTheRawValueLimit(t *testing.T) {
	htmlText := strings.Repeat("<>&", MaxDataValueBytes/3) +
		strings.Repeat("<", MaxDataValueBytes%3)
	if len(htmlText) != MaxDataValueBytes {
		t.Fatalf("construction is %d bytes, want %d", len(htmlText), MaxDataValueBytes)
	}
	plan := func(text string) DataResult {
		return DataResult{Kind: DataResultPlan, Class: DataClassRead,
			Plan: &DataPlan{Text: text}}
	}
	if err := plan(htmlText).Validate(); err != nil {
		t.Fatalf("plan text at the exact raw limit was rejected: %v", err)
	}
	if err := plan(htmlText + ">").Validate(); err == nil {
		t.Fatal("plan text one byte above the raw limit was accepted")
	}
	node := func(label string) DataResult {
		return DataResult{Kind: DataResultPlan, Class: DataClassRead,
			Plan: &DataPlan{Nodes: []DataPlanNode{{Label: label}}}}
	}
	if err := node(htmlText).Validate(); err != nil {
		t.Fatalf("plan node label at the exact raw limit was rejected: %v", err)
	}
	if err := node(htmlText + ">").Validate(); err == nil {
		t.Fatal("plan node label one byte above the raw limit was accepted")
	}
	summary := func(message string) DataResult {
		return DataResult{Kind: DataResultSummary, Class: DataClassRead,
			Summary: &DataSummary{Message: message}}
	}
	if err := summary(htmlText).Validate(); err != nil {
		t.Fatalf("summary message at the exact raw limit was rejected: %v", err)
	}
	if err := summary(htmlText + ">").Validate(); err == nil {
		t.Fatal("summary message one byte above the raw limit was accepted")
	}
}

func TestDataAccessModeMatrix(t *testing.T) {
	modes := []DataAccessMode{
		DataModeReadOnly, DataModeReadWrite, DataModeFull,
	}
	classes := []DataOperationClass{
		DataClassRead, DataClassWrite,
		DataClassDestructive, DataClassUnknown,
	}
	want := map[DataAccessMode][]bool{
		DataModeReadOnly:  {true, false, false, false},
		DataModeReadWrite: {true, true, false, false},
		DataModeFull:      {true, true, true, true},
	}
	for _, mode := range modes {
		for i, class := range classes {
			if got := mode.AllowsClass(class); got != want[mode][i] {
				t.Fatalf("%s allows %s = %v, want %v", mode, class, got, want[mode][i])
			}
		}
	}
	if DataModeReadOnly.AllowsMode(DataModeFull) {
		t.Fatal("read-only allowed a full-mode request")
	}
	if !DataModeFull.AllowsMode(DataModeReadOnly) {
		t.Fatal("full did not allow a read-only request")
	}
	if DataOperationClassByName("") != DataClassUnknown ||
		DataOperationClassByName("READ") != DataClassRead {
		t.Fatal("class mapping is not fail-closed")
	}
}

func TestDataResultEnvelopeValidation(t *testing.T) {
	table := DataResult{
		Kind: DataResultTable,
		Table: &DataTable{
			Columns: []DataColumn{{Name: "id", Type: DataValueInt}},
			Rows: [][]DataValue{{
				{Kind: DataValueInt, Int: "9007199254740993"},
			}},
		},
	}
	if err := table.Validate(); err != nil {
		t.Fatalf("valid table: %v", err)
	}

	values := []DataValue{
		{Kind: DataValueNull},
		{Kind: DataValueBool, Bool: true},
		{Kind: DataValueInt, Int: "-9223372036854775808"},
		{Kind: DataValueFloat, Float: 1.5},
		{Kind: DataValueDecimal, Decimal: "1234567890.000000000000000001"},
		{Kind: DataValueString, String: "hello"},
		{Kind: DataValueBytes, Bytes: []byte{0, 1, 2}},
		{Kind: DataValueTime, Time: "2026-11-08T10:00:00+08:00"},
		{Kind: DataValueJSON, JSON: []byte(`{"a":1}`)},
	}
	for _, value := range values {
		if err := value.Validate(); err != nil {
			t.Fatalf("%s value: %v", value.Kind, err)
		}
	}
	bad := []DataValue{
		{Kind: "object"},
		{Kind: DataValueBool, Bool: true, String: "x"},
		{Kind: DataValueInt, Int: "1.5"},
		{Kind: DataValueDecimal, Decimal: "1e3"},
		{Kind: DataValueTime, Time: "2026-11-08 10:00:00"},
		{Kind: DataValueJSON, JSON: []byte(`{`)},
		{Kind: DataValueNull, String: "x"},
		{Kind: DataValueString, String: "x", Int: "1"},
	}
	for _, value := range bad {
		if err := value.Validate(); err == nil {
			t.Fatalf("%+v was accepted", value)
		}
	}

	for _, envelope := range []DataResult{
		{Kind: DataResultDocuments, Documents: &DataDocuments{
			Documents: []map[string]DataValue{{"id": {Kind: DataValueInt, Int: "1"}}},
		}},
		{Kind: DataResultPairs, Pairs: &DataPairs{
			Pairs: []DataPair{{
				Key:   DataValue{Kind: DataValueString, String: "k"},
				Value: DataValue{Kind: DataValueBool, Bool: true},
			}},
		}},
		{Kind: DataResultChunks, Chunks: &DataChunks{
			Chunks: []DataChunk{{Offset: 0, Data: []byte("abc")}},
		}},
		{Kind: DataResultSummary, Summary: &DataSummary{AffectedRows: 2}},
		{Kind: DataResultPlan, Plan: &DataPlan{
			Nodes: []DataPlanNode{{Label: "scan", Children: []DataPlanNode{{Label: "filter"}}}},
		}},
		{WouldBlock: true},
	} {
		if err := envelope.Validate(); err != nil {
			t.Fatalf("%s envelope: %v", envelope.Kind, err)
		}
	}

	invalid := []DataResult{
		{Kind: "rows", Table: table.Table},
		{Kind: DataResultTable},
		{Kind: DataResultTable, Table: table.Table, Summary: &DataSummary{}},
		{Kind: DataResultTable, Table: &DataTable{
			Columns: table.Table.Columns,
			Rows:    [][]DataValue{{}},
		}},
		{Kind: DataResultTable, Table: &DataTable{
			Columns: table.Table.Columns,
			Rows:    make([][]DataValue, MaxDataBatchRows+1),
		}},
		{Kind: DataResultTable, Table: &DataTable{
			Columns: []DataColumn{{Name: "id", Type: DataValueInt}},
			Rows: [][]DataValue{{
				{Kind: DataValueString, String: strings.Repeat("x", MaxDataBatchBytes)},
			}},
		}},
		{Kind: DataResultChunks, Chunks: &DataChunks{
			Chunks: []DataChunk{{Data: make([]byte, MaxDataValueBytes+1)}},
		}},
	}
	for _, envelope := range invalid {
		if err := envelope.Validate(); err == nil {
			t.Fatalf("invalid envelope was accepted: %+v", envelope)
		}
	}
}

func TestTruncateDataResult(t *testing.T) {
	rows := make([][]DataValue, 10)
	for i := range rows {
		rows[i] = []DataValue{{Kind: DataValueString, String: strings.Repeat("x", 100)}}
	}
	result := DataResult{
		Kind: DataResultTable,
		Table: &DataTable{
			Columns: []DataColumn{{Name: "value", Type: DataValueString}},
			Rows:    rows,
		},
	}
	trimmed, dropped := TruncateDataResult(result, 4, -1)
	if !dropped || len(trimmed.Table.Rows) != 4 {
		t.Fatalf("row truncation = %d rows, dropped %v", len(trimmed.Table.Rows), dropped)
	}
	if len(result.Table.Rows) != 10 {
		t.Fatal("truncation mutated the input")
	}
	byteTrimmed, dropped := TruncateDataResult(result, -1, 350)
	if !dropped || len(byteTrimmed.Table.Rows) == 0 || byteTrimmed.DataResultBytes() > 350 {
		t.Fatalf("byte truncation = %d rows, %d bytes, dropped %v",
			len(byteTrimmed.Table.Rows), byteTrimmed.DataResultBytes(), dropped)
	}
}

// TestTruncateDataResultChunks proves a chunked batch is trimmed by whole
// chunks against both the row and byte budgets, and that trimming reports the
// drop so the caller can set Limit.
func TestTruncateDataResultChunks(t *testing.T) {
	result := DataResult{
		Kind: DataResultChunks,
		Chunks: &DataChunks{Chunks: []DataChunk{
			{Offset: 0, Data: make([]byte, 600)},
			{Offset: 600, Data: make([]byte, 600)},
		}},
	}
	one, dropped := TruncateDataResult(result, 1, -1)
	if !dropped || len(one.Chunks.Chunks) != 1 {
		t.Fatalf("row truncation = %d chunks, dropped %v",
			len(one.Chunks.Chunks), dropped)
	}
	if len(result.Chunks.Chunks) != 2 {
		t.Fatal("chunk truncation mutated the input")
	}

	// The whole batch exceeds 1,200 bytes; one 600-byte chunk fits, so the
	// second is dropped and the caller sees the drop.
	byteTrimmed, dropped := TruncateDataResult(result, -1, 1000)
	if !dropped {
		t.Fatal("byte truncation did not report a drop")
	}
	if len(byteTrimmed.Chunks.Chunks) != 1 {
		t.Fatalf("byte truncation kept %d chunks", len(byteTrimmed.Chunks.Chunks))
	}
	if byteTrimmed.DataResultBytes() > 1000 {
		t.Fatalf("byte truncation delivered %d bytes, budget 1000",
			byteTrimmed.DataResultBytes())
	}
	if len(result.Chunks.Chunks) != 2 {
		t.Fatal("chunk byte truncation mutated the input")
	}

	// A budget smaller than one chunk drops every chunk and still reports it.
	empty, dropped := TruncateDataResult(result, -1, 400)
	if !dropped || len(empty.Chunks.Chunks) != 0 {
		t.Fatalf("tight byte truncation = %d chunks, dropped %v",
			len(empty.Chunks.Chunks), dropped)
	}
}

// DataResultBytes is the interactive byte-budget accounting, so it must measure
// the same transport the envelope limit measures: JSON without HTML escaping. A
// summary message full of '<', '>', and '&' is not five or six times its raw
// size, and treating it as if it were would trim rows a provider could afford.
func TestDataResultBytesUsesUnescapedAccounting(t *testing.T) {
	result := DataResult{
		Kind:    DataResultSummary,
		Class:   DataClassRead,
		Summary: &DataSummary{Message: strings.Repeat("<>&", 100)},
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		t.Fatalf("encode unescaped: %v", err)
	}
	want := buf.Len() - 1 // the encoder appends one newline
	if got := result.DataResultBytes(); got != want {
		t.Fatalf("DataResultBytes = %d, want the unescaped encoding length %d", got, want)
	}
	escaped, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode escaped: %v", err)
	}
	if len(escaped) <= want {
		t.Fatalf("escaped encoding is %d bytes, not larger than the unescaped %d; the test no longer distinguishes the accounting",
			len(escaped), want)
	}
}

func TestPluginIDFromDataProviderID(t *testing.T) {
	cases := []struct {
		qualified string
		plugin    string
		ok        bool
	}{
		{QualifiedDataProviderID("acme.data", "db"), "acme.data", true},
		{"plugin.acme.data.local.v2", "acme.data", true},
		{"acme.data.db", "", false},
		{"plugin.acme", "", false},
		{"plugin.acme.data", "", false},
		{"plugin..db", "", false},
		{"plugin.acme.", "", false},
	}
	for _, test := range cases {
		plugin, ok := PluginIDFromDataProviderID(test.qualified)
		if plugin != test.plugin || ok != test.ok {
			t.Errorf("PluginIDFromDataProviderID(%q) = %q, %v; want %q, %v",
				test.qualified, plugin, ok, test.plugin, test.ok)
		}
	}
}
