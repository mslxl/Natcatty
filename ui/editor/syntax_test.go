package editor

// Ticket 56: the tokenizer's unit tests. Every token is checked by the text
// it covers, so an offset bug shows up as the wrong word rather than only as
// a wrong number.

import (
	"testing"
)

type wantToken struct {
	text string
	kind TokenKind
}

func tokenWants(text string, tokens []Token) []wantToken {
	runes := []rune(text)
	out := make([]wantToken, 0, len(tokens))
	for _, token := range tokens {
		if token.Start < 0 || token.End > len(runes) || token.Start > token.End {
			out = append(out, wantToken{text: "<out of range>"})
			continue
		}
		out = append(out, wantToken{text: string(runes[token.Start:token.End]), kind: token.Kind})
	}
	return out
}

func checkTokens(t *testing.T, text string, got []Token, want []wantToken) {
	t.Helper()
	gotWants := tokenWants(text, got)
	if len(gotWants) != len(want) {
		t.Fatalf("tokens = %+v, want %+v", gotWants, want)
	}
	for index := range want {
		if gotWants[index] != want[index] {
			t.Fatalf("token %d = %+v, want %+v (all: %+v)", index, gotWants[index], want[index], gotWants)
		}
	}
}

// TestNginxHighlightsCommentsStringsDirectivesAndVariables is the core
// acceptance: each line's directive, its string, and its variable are
// classified, while braces, arguments, and punctuation stay plain.
func TestNginxHighlightsCommentsStringsDirectivesAndVariables(t *testing.T) {
	text := "# leading comment\n" +
		"server {\n" +
		"    listen 80;\n" +
		"    server_name \"example.com\";\n" +
		"    location /api {\n" +
		"        proxy_pass http://$backend;\n" +
		"    }\n" +
		"}\n"
	checkTokens(t, text, nginxGrammar{}.Highlight(text), []wantToken{
		{"# leading comment", TokenComment},
		{"server", TokenDirective},
		{"listen", TokenDirective},
		{"server_name", TokenDirective},
		{"\"example.com\"", TokenString},
		{"location", TokenDirective},
		{"proxy_pass", TokenDirective},
		{"$backend", TokenVariable},
	})
}

// TestNginxStringsHideHashesAndCarryVariables checks the two interactions
// that make a line tokenizer more than a keyword list: a # inside quotes is
// text, and a $variable inside quotes is still a variable.
func TestNginxStringsHideHashesAndCarryVariables(t *testing.T) {
	text := "set $greeting \"hello # world $host\";\n" +
		"# trailing comment\n"
	checkTokens(t, text, nginxGrammar{}.Highlight(text), []wantToken{
		{"set", TokenDirective},
		{"$greeting", TokenVariable},
		{"\"hello # world $host\"", TokenString},
		{"$host", TokenVariable},
		{"# trailing comment", TokenComment},
	})
}

// TestNginxFindsEveryDirectiveAndVariableShape covers several statements on
// a line, directives only after ; { or }, and the ${name} and $1 forms.
func TestNginxFindsEveryDirectiveAndVariableShape(t *testing.T) {
	text := "listen 80; server_name example.com;\n" +
		"proxy_set_header X-Name ${host}_$1; # tail\n" +
		"rewrite ^/old/(.*)$ /new/$1 break;\n"
	checkTokens(t, text, nginxGrammar{}.Highlight(text), []wantToken{
		{"listen", TokenDirective},
		{"server_name", TokenDirective},
		{"proxy_set_header", TokenDirective},
		{"${host}", TokenVariable},
		{"$1", TokenVariable},
		{"# tail", TokenComment},
		{"rewrite", TokenDirective},
		{"$1", TokenVariable},
	})
}

// TestNginxOffsetsCountRunes keeps offsets right when a line has multi-byte
// text before a token.
func TestNginxOffsetsCountRunes(t *testing.T) {
	text := "server {\n" +
		"    server_name \"日本語\";\n" +
		"    # こんにちは\n" +
		"}\n"
	checkTokens(t, text, nginxGrammar{}.Highlight(text), []wantToken{
		{"server", TokenDirective},
		{"server_name", TokenDirective},
		{"\"日本語\"", TokenString},
		{"# こんにちは", TokenComment},
	})
}

// TestNginxEscapedQuoteStaysInTheString keeps a backslash-escaped quote from
// ending the string early.
func TestNginxEscapedQuoteStaysInTheString(t *testing.T) {
	text := "server_name \"a\\\"#b\";\n"
	checkTokens(t, text, nginxGrammar{}.Highlight(text), []wantToken{
		{"server_name", TokenDirective},
		{"\"a\\\"#b\"", TokenString},
	})
}

// TestGrammarForNameMatchesNginxFilesAndFallsBackToPlain is the grammar
// selection acceptance: caveats aside, .conf names and nginx.conf highlight;
// any other name stays plain.
func TestGrammarForNameMatchesNginxFilesAndFallsBackToPlain(t *testing.T) {
	for _, name := range []string{
		"nginx.conf", "/etc/nginx/nginx.conf", "sites-enabled/default.conf",
		`C:\nginx\conf\custom.CONF`,
	} {
		if GrammarForName(name) == nil {
			t.Errorf("%q has no grammar, want the Nginx grammar", name)
		}
	}
	for _, name := range []string{
		"", "notes.txt", "nginx.conf.bak", "conf", "sites-enabled/default",
	} {
		if grammar := GrammarForName(name); grammar != nil {
			t.Errorf("%q uses %T, want plain text", name, grammar)
		}
	}
}
