package locales

import "testing"

// TestEveryShippedLocaleDescribesEveryCommand keeps the command list, the
// switch, and the five catalogs in step. A command added to the list without a
// catalog key fails here, in every locale, before it can reach the popup.
func TestEveryShippedLocaleDescribesEveryCommand(t *testing.T) {
	if len(commandNames) == 0 {
		t.Fatal("the command catalog is empty")
	}
	for _, tag := range Tags() {
		msgs := In(tag)
		for _, command := range commandNames {
			if got := CommandDescription(msgs, command); got == "" {
				t.Errorf("%s: no description for %q", tag, command)
			}
		}
	}
}

// TestCommandDescriptionReadsTheFirstWord proves the lookup describes the
// command a line runs, not the whole line: arguments, extra spaces, and an
// absolute or relative path prefix are ignored.
func TestCommandDescriptionReadsTheFirstWord(t *testing.T) {
	msgs := In("en")
	want := msgs.CommandDescriptionGit()
	for _, line := range []string{
		"git",
		"git status",
		"   git   status --short  ",
		"/usr/bin/git status",
		"./git status",
		`C:\Git\bin\git status`,
	} {
		if got := CommandDescription(msgs, line); got != want {
			t.Errorf("CommandDescription(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestUnknownCommandHasNoDescription(t *testing.T) {
	msgs := In("en")
	for _, line := range []string{"", "   ", "frobnicate --fast", "/opt/tools/frobnicate"} {
		if got := CommandDescription(msgs, line); got != "" {
			t.Errorf("CommandDescription(%q) = %q, want no description", line, got)
		}
	}
}

// TestCommandDescriptionsAreTranslated proves the catalog is localized rather
// than an English string repeated in every file. A locale the app does not
// ship falls back to English through mygo's catalog chain.
func TestCommandDescriptionsAreTranslated(t *testing.T) {
	english := CommandDescription(In("en"), "ls")
	for _, tag := range Tags() {
		if tag == "en" {
			continue
		}
		if got := CommandDescription(In(tag), "ls"); got == english {
			t.Errorf("%s: ls description is still English: %q", tag, got)
		}
	}
	if got := CommandDescription(In("fr"), "ls"); got != english {
		t.Errorf("unsupported locale = %q, want the English fallback %q", got, english)
	}
}
