package plugin

import (
	"strings"
	"testing"

	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// dataBudgetBatch returns a full 256-row table batch for the default-budget
// tests. payload is the length of each string value.
func dataBudgetBatch(payload int) domainplugin.DataResult {
	value := domainplugin.DataValue{
		Kind: domainplugin.DataValueString, String: strings.Repeat("x", payload),
	}
	rows := make([][]domainplugin.DataValue, domainplugin.MaxDataBatchRows)
	for i := range rows {
		rows[i] = []domainplugin.DataValue{value}
	}
	return domainplugin.DataResult{
		Kind: domainplugin.DataResultTable,
		Table: &domainplugin.DataTable{
			Columns: []domainplugin.DataColumn{{Name: "v", Type: domainplugin.DataValueString}},
			Rows:    rows,
		},
		More:  true,
		Class: domainplugin.DataClassRead,
	}
}

// TestDataQueryApplyHonoursTheDefaultRowBudget exercises the real 100,000-row
// interactive budget at the overlay level, without a guest round trip.
func TestDataQueryApplyHonoursTheDefaultRowBudget(t *testing.T) {
	host := NewDataProviderHost(DataHostOptions{})
	if host.limits.InteractiveRows != InteractiveDataRows {
		t.Fatalf("default row budget = %d", host.limits.InteractiveRows)
	}
	query := &DataQuery{host: host}
	batch := dataBudgetBatch(1)
	fullBatches := InteractiveDataRows / domainplugin.MaxDataBatchRows
	for i := 0; i < fullBatches; i++ {
		result := query.apply(batch)
		if result.Limit || !result.More {
			t.Fatalf("batch %d ended the query early: limit %v, more %v",
				i, result.Limit, result.More)
		}
	}
	result := query.apply(batch)
	if !result.Limit || result.More {
		t.Fatalf("final batch = limit %v, more %v", result.Limit, result.More)
	}
	want := InteractiveDataRows % domainplugin.MaxDataBatchRows
	if len(result.Table.Rows) != want {
		t.Fatalf("final batch delivered %d rows, want %d",
			len(result.Table.Rows), want)
	}
	if query.rows != InteractiveDataRows {
		t.Fatalf("delivered rows = %d, want %d", query.rows, InteractiveDataRows)
	}
}

// TestDataQueryApplyHonoursTheDefaultByteBudget exercises the real 64 MiB
// interactive budget with full-size batches.
func TestDataQueryApplyHonoursTheDefaultByteBudget(t *testing.T) {
	host := NewDataProviderHost(DataHostOptions{})
	if host.limits.InteractiveBytes != InteractiveDataBytes {
		t.Fatalf("default byte budget = %d", host.limits.InteractiveBytes)
	}
	query := &DataQuery{host: host}
	// A 900-byte value keeps each 256-row batch below the 256 KiB batch cap
	// while still filling the 64 MiB budget in a few hundred batches.
	batch := dataBudgetBatch(900)
	if encoded := batch.DataResultBytes(); encoded > domainplugin.MaxDataBatchBytes {
		t.Fatalf("test batch encodes to %d bytes, above the batch cap", encoded)
	}

	result := query.apply(batch)
	batches := 1
	for result.More && !result.Limit && batches < 2000 {
		result = query.apply(batch)
		batches++
	}
	if !result.Limit || result.More {
		t.Fatalf("byte budget = limit %v, more %v after %d batches",
			result.Limit, result.More, batches)
	}
	if batches < 100 {
		t.Fatalf("byte budget ended after only %d batches", batches)
	}
	if query.bytes > InteractiveDataBytes {
		t.Fatalf("delivered %d bytes, budget %d", query.bytes, InteractiveDataBytes)
	}
}
