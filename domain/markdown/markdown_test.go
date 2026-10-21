package markdown

import (
	"strings"
	"testing"
)

func TestParseHeadingsAndParagraphs(t *testing.T) {
	doc := Parse("# Title\n\nFirst *try* and **bold**.\n\n## Sub\n\nSetext\n---\n")
	if len(doc.Blocks) != 4 {
		t.Fatalf("blocks = %d, want 4: %+v", len(doc.Blocks), doc.Blocks)
	}
	if doc.Blocks[0].Kind != HeadingBlock || doc.Blocks[0].Level != 1 {
		t.Fatalf("first block = %+v", doc.Blocks[0])
	}
	if got := inlineText(doc.Blocks[0].Inlines); got != "Title" {
		t.Fatalf("heading text = %q", got)
	}
	if doc.Blocks[1].Kind != ParagraphBlock || inlineText(doc.Blocks[1].Inlines) != "First try and bold." {
		t.Fatalf("paragraph = %+v", doc.Blocks[1])
	}
	if doc.Blocks[2].Kind != HeadingBlock || doc.Blocks[2].Level != 2 {
		t.Fatalf("second heading = %+v", doc.Blocks[2])
	}
	if doc.Blocks[3].Kind != HeadingBlock || doc.Blocks[3].Level != 2 {
		t.Fatalf("setext heading = %+v", doc.Blocks[3])
	}
}

func TestParseInlineStylesAndLinks(t *testing.T) {
	inlines := Parse("**strong** _em_ `code` [docs](https://example.com/a) ~~gone~~").Blocks
	if len(inlines) != 1 {
		t.Fatalf("blocks = %+v", inlines)
	}
	kinds := map[InlineKind]int{}
	for _, inline := range inlines[0].Inlines {
		kinds[inline.Kind]++
	}
	for _, kind := range []InlineKind{InlineStrong, InlineEmphasis, InlineCode, InlineLink, InlineStrike} {
		if kinds[kind] == 0 {
			t.Fatalf("missing inline kind %v in %+v", kind, inlines[0].Inlines)
		}
	}

	doc := Parse("[docs](https://example.com/a)")
	link := doc.Blocks[0].Inlines[0]
	if link.Kind != InlineLink || link.URL != "https://example.com/a" ||
		inlineText(link.Children) != "docs" {
		t.Fatalf("link = %+v", link)
	}
}

func TestParseListsAndQuotes(t *testing.T) {
	source := "- one\n- two\n  - nested\n\n1. first\n2. second\n\n> quoted **text**\n"
	doc := Parse(source)
	if len(doc.Blocks) != 3 {
		t.Fatalf("blocks = %d: %+v", len(doc.Blocks), doc.Blocks)
	}
	if doc.Blocks[0].Kind != ListBlock || doc.Blocks[0].Ordered || len(doc.Blocks[0].Items) != 2 {
		t.Fatalf("unordered list = %+v", doc.Blocks[0])
	}
	nested := doc.Blocks[0].Items[1].Blocks
	if len(nested) != 2 || nested[1].Kind != ListBlock || len(nested[1].Items) != 1 {
		t.Fatalf("nested list = %+v", nested)
	}
	if doc.Blocks[1].Kind != ListBlock || !doc.Blocks[1].Ordered || doc.Blocks[1].Start != 1 {
		t.Fatalf("ordered list = %+v", doc.Blocks[1])
	}
	if doc.Blocks[2].Kind != QuoteBlock || len(doc.Blocks[2].Blocks) != 1 {
		t.Fatalf("quote = %+v", doc.Blocks[2])
	}
	if !strings.Contains(inlineText(doc.Blocks[2].Blocks[0].Inlines), "quoted") {
		t.Fatalf("quote text = %+v", doc.Blocks[2].Blocks[0].Inlines)
	}
}

func TestParseCodeRuleAndTable(t *testing.T) {
	source := "```go\nfmt.Println(\"<hi>\")\n```\n\n---\n\n| Name | Value |\n| :--- | ---: |\n| one | 1 |\n"
	doc := Parse(source)
	if len(doc.Blocks) != 3 {
		t.Fatalf("blocks = %d: %+v", len(doc.Blocks), doc.Blocks)
	}
	if doc.Blocks[0].Kind != CodeBlock || doc.Blocks[0].Language != "go" ||
		doc.Blocks[0].Code != "fmt.Println(\"<hi>\")" {
		t.Fatalf("code = %+v", doc.Blocks[0])
	}
	if doc.Blocks[1].Kind != RuleBlock {
		t.Fatalf("rule = %+v", doc.Blocks[1])
	}
	table := doc.Blocks[2]
	if table.Kind != TableBlock || len(table.Header) != 2 || len(table.Rows) != 1 {
		t.Fatalf("table = %+v", table)
	}
	if table.Align[0] != AlignLeft || table.Align[1] != AlignRight {
		t.Fatalf("alignment = %+v", table.Align)
	}
}

func TestParseIsRobustAgainstArbitraryInput(t *testing.T) {
	inputs := []string{
		"", "\n\n\n", "***", "`unclosed", "[link](", "> > > >", "- ", "| | |\n|---|\n|",
		strings.Repeat("> ", 200) + "deep", strings.Repeat("#", 400),
		"<script>alert(1)</script>", "\x00\x01\x02", "```\nunterminated",
		strings.Repeat("*a*", 500), "a_b_c ** ** ~~ ~~",
	}
	for _, input := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("Parse panicked for %q: %v", input, recovered)
				}
			}()
			_ = Parse(input)
			_ = HTML(Parse(input).Blocks)
		}()
	}
}

func TestHTMLDocumentEscapesAndIsSelfContained(t *testing.T) {
	source := "# Hi\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1)) [ok](https://example.com)\n"
	page := HTMLPage("A <title>", source)
	if strings.Contains(page, "<script>alert(1)</script>") {
		t.Fatalf("raw script reached the HTML: %s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("script text is not escaped: %s", page)
	}
	if strings.Contains(page, "href=\"javascript:") {
		t.Fatalf("unsafe link reached the HTML: %s", page)
	}
	if !strings.Contains(page, "https://example.com") {
		t.Fatalf("safe link is missing: %s", page)
	}
	for _, external := range []string{"<link ", "src=\"http", "@import"} {
		if strings.Contains(page, external) {
			t.Fatalf("page is not self-contained: %q", external)
		}
	}
}

func inlineText(inlines []Inline) string {
	var builder strings.Builder
	var walk func([]Inline)
	walk = func(list []Inline) {
		for _, inline := range list {
			builder.WriteString(inline.Text)
			walk(inline.Children)
		}
	}
	walk(inlines)
	return builder.String()
}
