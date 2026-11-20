package autocomplete

import (
	"testing"
	"time"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func TestProgramInputPromptRecognizesOrdinaryColonLabels(t *testing.T) {
	for _, line := range []string{
		"Email: ",
		"e-mail:",
		"E-Mail Address: ",
		"Please enter your username: ",
		"enter your name: ",
		"Login name: ",
		"Account: ",
		"User ID: ",
		"Email: old@example.com",
		"Username：",
		"请输入邮箱：",
		"用户名：",
	} {
		if !ProgramInputPrompt(line) {
			t.Errorf("ProgramInputPrompt(%q) = false, want a program prompt", line)
		}
		if PromptKindOf(line) != PromptProgram {
			t.Errorf("PromptKindOf(%q) != PromptProgram", line)
		}
	}
}

func TestProgramInputPromptRefusesCredentialAndShellLabels(t *testing.T) {
	for _, line := range []string{
		"",
		"Password: ",
		"Enter your passphrase: ",
		"PIN: ",
		"Token: ",
		"API key: ",
		"One-time code (OTP): ",
		"请输入密码：",
		"口令：",
		"dev: ",
		"git: ",
		"$ ",
		"Email:old@example.com",
		"https://example.com: ",
		"Some output: 42",
		"total: 4",
	} {
		if ProgramInputPrompt(line) {
			t.Errorf("ProgramInputPrompt(%q) = true, want false", line)
		}
		if PromptKindOf(line) != PromptShell {
			t.Errorf("PromptKindOf(%q) != PromptShell", line)
		}
	}
}

func TestProgramPromptOffersSnippetsAlongsideHistory(t *testing.T) {
	journal := &journalMemory{}
	config := defaultConfig()
	snippets := []Snippet{
		{Label: "Work email", Command: "work@example.com"},
		{Label: "Personal email", Command: "home@example.com"},
	}
	service := newTestService(t, journal, config, WithSnippets(func() []Snippet { return snippets }))
	service.RecordAt("local", "wget work", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))

	got := service.SuggestionsAtPrompt("work", "local", "", PromptProgram, nil)
	if len(got) != 2 {
		t.Fatalf("program suggestions = %+v, want the snippet and the history row", got)
	}
	if got[0].Source != SourceSnippet || got[0].Text != "work@example.com" ||
		got[0].Display != "Work email" {
		t.Fatalf("first row = %+v, want the matching snippet", got[0])
	}
	if got[1].Source != SourceHistory || got[1].Text != "wget work" {
		t.Fatalf("second row = %+v, want the matching history row", got[1])
	}

	// The same query at a shell prompt is history only: a Vault Snippet is
	// never inserted as a command by an ordinary completion.
	shell := service.SuggestionsAtPrompt("work", "local", "", PromptShell, nil)
	if len(shell) != 1 || shell[0].Source != SourceHistory {
		t.Fatalf("shell suggestions = %+v, want history only", shell)
	}
}

func TestProgramPromptSnippetMatchesLabelAndCommand(t *testing.T) {
	snippets := []Snippet{
		{Label: "Deploy staging", Command: "kubectl apply -f staging.yaml"},
		{Label: "Rotate token", Command: "security rotate --service api"},
		{Label: "No command"},
	}
	service := newTestService(t, &journalMemory{}, defaultConfig(),
		WithSnippets(func() []Snippet { return snippets }))

	byLabel := service.SuggestionsAtPrompt("deploy", "local", "", PromptProgram, nil)
	if len(byLabel) != 1 || byLabel[0].Text != "kubectl apply -f staging.yaml" ||
		byLabel[0].Display != "Deploy staging" {
		t.Fatalf("label match = %+v", byLabel)
	}
	byCommand := service.SuggestionsAtPrompt("security", "local", "", PromptProgram, nil)
	if len(byCommand) != 1 || byCommand[0].Display != "Rotate token" {
		t.Fatalf("command match = %+v", byCommand)
	}
	if got := service.SuggestionsAtPrompt("zzz", "local", "", PromptProgram, nil); got != nil {
		t.Fatalf("non-matching query = %+v, want none", got)
	}
}

func TestProgramPromptKeepsSnippetsWithinTheLimit(t *testing.T) {
	journal := &journalMemory{}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 10; index++ {
		journal.entries = append(journal.entries,
			journalEntryFor("mail", "local", at.Add(time.Duration(index)*time.Minute)))
	}
	config := defaultConfig()
	config.MaxSuggestions = 1
	service := newTestService(t, journal, config, WithSnippets(func() []Snippet {
		return []Snippet{{Label: "Mailbox", Command: "mailbox"}}
	}))
	got := service.SuggestionsAtPrompt("mail", "local", "", PromptProgram, nil)
	if len(got) != 1 || got[0].Source != SourceSnippet {
		t.Fatalf("limited suggestions = %+v, want the snippet to survive the limit", got)
	}
}

func TestProgramPromptOffersAMultiLineSnippet(t *testing.T) {
	service := newTestService(t, &journalMemory{}, defaultConfig(),
		WithSnippets(func() []Snippet {
			return []Snippet{{Label: "Two lines", Command: "first line\nsecond line"}}
		}))
	got := service.SuggestionsAtPrompt("two", "local", "", PromptProgram, nil)
	if len(got) != 1 {
		t.Fatalf("suggestions = %+v", got)
	}
	// The popup row stays one line, while Text is the full command the safe
	// insertion seam will accept or refuse.
	if got[0].Display != "Two lines" {
		t.Fatalf("display = %q", got[0].Display)
	}
	if got[0].Text != "first line\nsecond line" {
		t.Fatalf("text = %q", got[0].Text)
	}
}

func TestSnippetRowsDoNotBorrowCommandDescriptions(t *testing.T) {
	service := newTestService(t, &journalMemory{}, defaultConfig(),
		WithSnippets(func() []Snippet { return []Snippet{{Label: "Git", Command: "git merge"}} }),
		WithCommandDescriptions(func(command string) string {
			if command == "git merge" {
				return "command description"
			}
			return ""
		}))
	got := service.SuggestionsAtPrompt("git", "local", "", PromptProgram, nil)
	if len(got) != 1 {
		t.Fatalf("suggestions = %+v", got)
	}
	if got[0].Description != "" {
		t.Fatalf("snippet description = %q, want no command description", got[0].Description)
	}
}

func TestSnippetsFollowTheAutocompleteRules(t *testing.T) {
	snippets := []Snippet{{Label: "Email", Command: "me@example.com"}}
	config := defaultConfig()
	config.Enabled = false
	disabled := newTestService(t, &journalMemory{}, config,
		WithSnippets(func() []Snippet { return snippets }))
	if got := disabled.SuggestionsAtPrompt("em", "local", "", PromptProgram, nil); got != nil {
		t.Fatalf("disabled suggestions = %+v, want none", got)
	}

	config = defaultConfig()
	config.MinChars = 3
	short := newTestService(t, &journalMemory{}, config,
		WithSnippets(func() []Snippet { return snippets }))
	if got := short.SuggestionsAtPrompt("em", "local", "", PromptProgram, nil); got != nil {
		t.Fatalf("short input = %+v, want none", got)
	}
	if got := short.SuggestionsAtPrompt("ema", "local", "", PromptProgram, nil); len(got) != 1 {
		t.Fatalf("long input = %+v, want the snippet", got)
	}
}

func TestSnippetSourceIsHostAndGlobalScopeIndependent(t *testing.T) {
	config := defaultConfig()
	config.Scope = domainterminal.AutocompleteHistoryHost
	service := newTestService(t, &journalMemory{}, config,
		WithSnippets(func() []Snippet { return []Snippet{{Label: "Email", Command: "me@example.com"}} }))
	// A Snippet is not history: asking from a different scope still offers it
	// because there is no per-Host journal row to filter.
	got := service.SuggestionsAtPrompt("em", "host:other", "", PromptProgram, nil)
	if len(got) != 1 || got[0].Source != SourceSnippet {
		t.Fatalf("suggestions = %+v", got)
	}
}

func TestAcceptEraseCountKeepsTheRestOfTheLine(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		cursor  int
		program bool
		want    int
	}{
		{"empty line", "", 0, false, 0},
		{"shell first word replaces the line", "git", 3, false, 3},
		{"shell later word still replaces the line", "git status --s", 14, false, 14},
		{"program prompt replaces the typed query", "wor", 3, true, 3},
		{"program prompt replaces the current word", "hello wor", 9, true, 3},
		{"program prompt after a space clears the prompt input", "wor ", 4, true, 4},
		{"mid-line shell only removes the left prefix", "wor", 2, false, 2},
		{"mid-line program only removes the left prefix", "wor", 2, true, 2},
		{"cursor past the end clamps", "git", 99, false, 3},
		{"negative cursor clamps", "git", -1, false, 0},
	}
	for _, testCase := range cases {
		if got := AcceptEraseCount(testCase.input, testCase.cursor, testCase.program); got != testCase.want {
			t.Errorf("%s: AcceptEraseCount(%q, %d, %v) = %d, want %d",
				testCase.name, testCase.input, testCase.cursor, testCase.program, got, testCase.want)
		}
	}
}

func TestAcceptEraseCountCountsRunes(t *testing.T) {
	// The terminal's line editor erases runes, not bytes; a multi-byte query
	// must not leave half a character behind.
	if got := AcceptEraseCount("邮箱", 2, true); got != 2 {
		t.Fatalf("AcceptEraseCount = %d, want 2 runes", got)
	}
}
