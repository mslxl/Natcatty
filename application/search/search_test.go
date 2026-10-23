package search

import (
	"reflect"
	"testing"
)

func hostRecord(id, label, hostname, group string, tags ...string) Record {
	fields := []Field{
		{Text: label, Weight: WeightLabel},
		{Text: hostname, Weight: WeightHostname},
	}
	if group != "" {
		fields = append(fields, Field{Text: group, Weight: WeightGroup})
	}
	for _, tag := range tags {
		fields = append(fields, Field{Text: tag, Weight: WeightTag})
	}
	return Record{Kind: KindHost, ID: id, Title: label, Subtitle: hostname, Fields: fields}
}

func ids(results []Result) []string {
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = result.Record.ID
	}
	return out
}

// TestLabelOutranksHostnameAndGroup proves the weighted fields the original
// matcher used: a label hit beats the same text in a hostname, which beats a
// group or tag hit.
func TestLabelOutranksHostnameAndGroup(t *testing.T) {
	records := []Record{
		hostRecord("group", "other", "10.0.0.1", "web"),
		hostRecord("hostname", "other", "web.example.com", ""),
		hostRecord("label", "web", "10.0.0.2", ""),
	}
	if got := ids(Rank("web", records)); !reflect.DeepEqual(got, []string{"label", "hostname", "group"}) {
		t.Fatalf("order = %v, want label, hostname, group", got)
	}
}

// TestLiteralBeatsCompactAcrossEveryRecord proves a strict match always
// outranks a loose one, even when the loose record matched a heavier field.
func TestLiteralBeatsCompactAcrossEveryRecord(t *testing.T) {
	loose := Record{Kind: KindHost, ID: "loose", Title: "web one",
		Fields: []Field{{Text: "web-1", Weight: WeightLabel}}}
	strict := Record{Kind: KindHost, ID: "strict", Title: "web1",
		Fields: []Field{{Text: "web1", Weight: WeightTag}}}
	if got := ids(Rank("web1", []Record{loose, strict})); !reflect.DeepEqual(got, []string{"strict", "loose"}) {
		t.Fatalf("order = %v, want strict first", got)
	}
}

// TestCompactMatchesSeparatedText covers the punctuation and space folding
// that lets "web1" find "web-1" and "web 1".
func TestCompactMatchesSeparatedText(t *testing.T) {
	for _, text := range []string{"web-1", "web 1", "web_1", "web/1"} {
		record := Record{Kind: KindHost, ID: "x", Title: text}
		if _, ok := Match(Prepare("web1"), record); !ok {
			t.Errorf("%q did not match web1", text)
		}
	}
}

// TestEverySegmentMustMatch keeps the original's AND semantics: tokens may
// match different fields, but a missing token rejects the record.
func TestEverySegmentMustMatch(t *testing.T) {
	record := hostRecord("web-1", "web-1", "10.0.0.1", "prod", "web")
	if _, ok := Match(Prepare("web prod"), record); !ok {
		t.Fatal("tokens across label and group did not match")
	}
	if _, ok := Match(Prepare("web staging"), record); ok {
		t.Fatal("a missing token still matched")
	}
}

// TestIPAaddressesMatchOnlyContiguously prevents a dotted address from
// combining digits across unrelated fields.
func TestIPAddressesMatchOnlyContiguously(t *testing.T) {
	record := Record{Kind: KindHost, ID: "x", Title: "host-6",
		Fields: []Field{
			{Text: "host-6", Weight: WeightLabel},
			{Text: "10.1.2.3", Weight: WeightHostname},
		}}
	if _, ok := Match(Prepare("10.1.2.3"), record); !ok {
		t.Fatal("the address did not match its own field")
	}
	if _, ok := Match(Prepare("10.2"), record); ok {
		t.Fatal("a partial address matched across fields")
	}
}

// TestEmptyQueryKeepsInputOrder lets a surface decide the default order, as
// the Quick Switcher lists Hosts then Sessions.
func TestEmptyQueryKeepsInputOrder(t *testing.T) {
	records := []Record{
		{Kind: KindHost, ID: "a", Title: "b"},
		{Kind: KindSession, ID: "b", Title: "a"},
	}
	if got := ids(Rank("", records)); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("order = %v, want input order", got)
	}
	if got := ids(Rank("   ", records)); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("blank order = %v, want input order", got)
	}
}

// TestTiesAreDeterministic keeps repeated frames and tests stable.
func TestTiesAreDeterministic(t *testing.T) {
	records := []Record{
		{Kind: KindHost, ID: "b", Title: "web"},
		{Kind: KindHost, ID: "a", Title: "web"},
		{Kind: KindHost, ID: "c", Title: "Web"},
	}
	first := ids(Rank("web", records))
	for i := 0; i < 20; i++ {
		if got := ids(Rank("web", records)); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d = %v, want %v", i, got, first)
		}
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(first, want) {
		t.Fatalf("order = %v, want %v", first, want)
	}
}

// TestCJKMatchingWorksLiterally proves Chinese text needs no transliteration
// to match, which is the whole population of a CJK query.
func TestCJKMatchingWorksLiterally(t *testing.T) {
	record := Record{Kind: KindHost, ID: "x", Title: "生产 web-1", Subtitle: "prod"}
	if _, ok := Match(Prepare("生产"), record); !ok {
		t.Fatal("CJK label did not match")
	}
	if _, ok := Match(Prepare("生产 web"), record); !ok {
		t.Fatal("CJK token plus Latin token did not match")
	}
}

// TestUnicodeDashFoldsToHyphen reproduces the original's dash normalization.
func TestUnicodeDashFoldsToHyphen(t *testing.T) {
	record := Record{Kind: KindHost, ID: "x", Title: "web-1"}
	if _, ok := Match(Prepare("web‐1"), record); !ok {
		t.Fatal("the Unicode hyphen did not match the ASCII one")
	}
}

// TestRankedResultsAreGroupable keeps InKind order equal to the ranked
// subsequence, which the sectioned views rely on.
func TestRankedResultsAreGroupable(t *testing.T) {
	records := []Record{
		{Kind: KindHost, ID: "h1", Title: "web", Fields: []Field{{Text: "web", Weight: WeightTag}}},
		{Kind: KindKey, ID: "k1", Title: "web key", Fields: []Field{{Text: "web key", Weight: WeightLabel}}},
		{Kind: KindHost, ID: "h2", Title: "web", Fields: []Field{{Text: "web", Weight: WeightLabel}}},
	}
	ranked := Rank("web", records)
	hosts := InKind(ranked, KindHost)
	if got := ids(hosts); !reflect.DeepEqual(got, []string{"h2", "h1"}) {
		t.Fatalf("host order = %v, want h2 then h1", got)
	}
	if got := ids(InKind(ranked, KindKey)); !reflect.DeepEqual(got, []string{"k1"}) {
		t.Fatalf("key order = %v", got)
	}
}
