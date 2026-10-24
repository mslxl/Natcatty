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

// labelRecord is the simplest record whose only searchable text is its title.
func labelRecord(kind Kind, id, title string) Record {
	return Record{
		Kind: kind, ID: id, Title: title,
		Fields: []Field{{Text: title, Weight: WeightLabel}},
	}
}

// TestFieldWeightsBreakEqualQualityTies proves the weighted fields the original
// matcher used still order matches of the same quality: a label hit beats the
// same text in a hostname, which beats a tag hit. Match quality now leads, so
// every field here matches at a word start.
func TestFieldWeightsBreakEqualQualityTies(t *testing.T) {
	records := []Record{
		{Kind: KindHost, ID: "tag", Title: "tag web",
			Fields: []Field{{Text: "tag web", Weight: WeightTag}}},
		{Kind: KindHost, ID: "hostname", Title: "host web",
			Fields: []Field{{Text: "host web", Weight: WeightHostname}}},
		{Kind: KindHost, ID: "label", Title: "label web",
			Fields: []Field{{Text: "label web", Weight: WeightLabel}}},
	}
	if got := ids(Rank("web", records)); !reflect.DeepEqual(got, []string{"label", "hostname", "tag"}) {
		t.Fatalf("order = %v, want label, hostname, tag", got)
	}
}

// TestMatchQualityOrderIsExactPrefixWordStartSubstring pins the four match
// classes the Vault ranking orders by. Every record matches its label only, so
// the old flat literal bonus would tie them all.
func TestMatchQualityOrderIsExactPrefixWordStartSubstring(t *testing.T) {
	records := []Record{
		labelRecord(KindHost, "substring", "myprod"),
		labelRecord(KindHost, "word-start", "my prod box"),
		labelRecord(KindHost, "prefix", "prod-db"),
		labelRecord(KindHost, "exact", "prod"),
	}
	want := []string{"exact", "prefix", "word-start", "substring"}
	for _, query := range []string{"prod", "PROD", "Prod"} {
		if got := ids(Rank(query, records)); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q order = %v, want %v", query, got, want)
		}
	}
}

// TestVaultSearchProdRanking reproduces the retest: seeding prod, prod-db and
// my-prod-box must show the exact label first, then the prefix, then the
// substring. The old matcher tied all three and fell back to the title.
func TestVaultSearchProdRanking(t *testing.T) {
	records := []Record{
		hostRecord("h-my-prod-box", "my-prod-box", "10.0.0.3", ""),
		hostRecord("h-prod", "prod", "10.0.0.1", ""),
		hostRecord("h-prod-db", "prod-db", "10.0.0.2", ""),
	}
	want := []string{"h-prod", "h-prod-db", "h-my-prod-box"}
	for _, query := range []string{"prod", "PROD"} {
		if got := ids(Rank(query, records)); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q order = %v, want %v", query, got, want)
		}
	}
}

// TestMatchQualityOrderIsUnicodeInsensitive applies the same four classes to
// Chinese text, including a word start after a CJK separator. The titles sort
// against the wanted order, so a title tie-break cannot pass by accident.
func TestMatchQualityOrderIsUnicodeInsensitive(t *testing.T) {
	records := []Record{
		labelRecord(KindHost, "substring", "厂生产机"),
		labelRecord(KindHost, "word-start", "厂、生产机"),
		labelRecord(KindHost, "prefix", "生产库"),
		labelRecord(KindHost, "exact", "生产"),
	}
	want := []string{"exact", "prefix", "word-start", "substring"}
	if got := ids(Rank("生产", records)); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestMatchQualityOrderAppliesAcrossRecordTypes proves the tiers are shared by
// every kind, not special-cased for Hosts.
func TestMatchQualityOrderAppliesAcrossRecordTypes(t *testing.T) {
	records := []Record{
		labelRecord(KindSnippet, "snippet-substring", "myprod"),
		labelRecord(KindKey, "key-word-start", "my prod key"),
		labelRecord(KindNote, "note-prefix", "prod-db"),
		labelRecord(KindHost, "host-exact", "prod"),
	}
	want := []string{"host-exact", "note-prefix", "key-word-start", "snippet-substring"}
	if got := ids(Rank("prod", records)); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestMatchQualityLeadsFieldWeight keeps exact matches in a lighter field
// ahead of prefix matches in a heavier one, as the retest requires across
// titles, names, addresses, bodies, and labels.
func TestMatchQualityLeadsFieldWeight(t *testing.T) {
	records := []Record{
		{Kind: KindHost, ID: "prefix", Title: "prod-db",
			Fields: []Field{{Text: "prod-db", Weight: WeightLabel}}},
		{Kind: KindHost, ID: "exact", Title: "prod",
			Fields: []Field{{Text: "prod", Weight: WeightTag}}},
	}
	if got := ids(Rank("prod", records)); !reflect.DeepEqual(got, []string{"exact", "prefix"}) {
		t.Fatalf("order = %v, want the exact tag match first", got)
	}
}

// TestBodyOnlyMatchRanksLastWithinSection keeps a record found only through
// its notes after every record whose name, address, group, or tag matched.
func TestBodyOnlyMatchRanksLastWithinSection(t *testing.T) {
	records := []Record{
		{Kind: KindHost, ID: "body", Title: "notes-only",
			Fields: []Field{
				{Text: "notes-only", Weight: WeightLabel},
				{Text: "prod", Weight: WeightNote, Body: true},
			}},
		{Kind: KindHost, ID: "substring", Title: "myprod",
			Fields: []Field{{Text: "myprod", Weight: WeightLabel}}},
		{Kind: KindHost, ID: "prefix", Title: "prod-db",
			Fields: []Field{{Text: "prod-db", Weight: WeightLabel}}},
		{Kind: KindHost, ID: "exact", Title: "prod",
			Fields: []Field{{Text: "prod", Weight: WeightLabel}}},
	}
	want := []string{"exact", "prefix", "substring", "body"}
	if got := ids(Rank("prod", records)); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestBodyOnlySectionKeepsMatchQualityOrder keeps the tiers inside a section
// whose records all match through their body, such as Knowledge.
func TestBodyOnlySectionKeepsMatchQualityOrder(t *testing.T) {
	body := func(id, text string) Record {
		return Record{Kind: KindKnowledge, ID: id, Title: text,
			Fields: []Field{{Text: text, Weight: WeightContent, Body: true}}}
	}
	records := []Record{
		body("substring", "myprod"),
		body("prefix", "prod-db"),
		body("exact", "prod"),
	}
	want := []string{"exact", "prefix", "substring"}
	if got := ids(Rank("prod", records)); !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestEqualScoresUseStableIdentityTieBreaks keeps a frame deterministic when
// records share a score, a title, and a kind.
func TestEqualScoresUseStableIdentityTieBreaks(t *testing.T) {
	records := []Record{
		labelRecord(KindHost, "b", "prod"),
		labelRecord(KindHost, "a", "prod"),
		labelRecord(KindKey, "c", "prod"),
		labelRecord(KindHost, "d", "Prod"),
	}
	want := []string{"a", "b", "d", "c"}
	first := ids(Rank("prod", records))
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("order = %v, want %v", first, want)
	}
	for i := 0; i < 50; i++ {
		if got := ids(Rank("prod", records)); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d = %v, want %v", i, got, first)
		}
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
