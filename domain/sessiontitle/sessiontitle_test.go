package sessiontitle

import (
	"testing"

	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
)

func TestConnectionLabelPrefersTheUserRename(t *testing.T) {
	if got := ConnectionLabel("Prod", "web-01"); got != "Prod" {
		t.Fatalf("ConnectionLabel = %q, want the rename", got)
	}
	if got := ConnectionLabel("  ", "web-01"); got != "web-01" {
		t.Fatalf("ConnectionLabel = %q, want the Host label", got)
	}
}

func TestResolveIgnoresTheDynamicTitleForNonAgentSessions(t *testing.T) {
	got := Resolve(Input{
		HostLabel:    "web-01",
		DynamicTitle: "root@host:/var/log",
	}, modelterminal.DynamicTabTitleAgents)
	if got != "web-01" {
		t.Fatalf("Resolve = %q, want the Host label", got)
	}
}

func TestResolveUsesTheDynamicTitleForAgentSessions(t *testing.T) {
	got := Resolve(Input{
		HostLabel:    "web-01",
		DynamicTitle: "claude: refactor auth",
		Agent:        true,
	}, modelterminal.DynamicTabTitleAgents)
	if got != "claude: refactor auth" {
		t.Fatalf("Resolve = %q, want the dynamic title", got)
	}
}

func TestResolveUsesTheDynamicTitleForEverySessionInAllMode(t *testing.T) {
	got := Resolve(Input{
		HostLabel:    "web-01",
		DynamicTitle: "root@host:/var/log",
	}, modelterminal.DynamicTabTitleAll)
	if got != "root@host:/var/log" {
		t.Fatalf("Resolve = %q, want the dynamic title", got)
	}
}

func TestResolveIgnoresTheDynamicTitleWhenOff(t *testing.T) {
	got := Resolve(Input{
		HostLabel:    "web-01",
		DynamicTitle: "claude: refactor auth",
		Agent:        true,
	}, modelterminal.DynamicTabTitleOff)
	if got != "web-01" {
		t.Fatalf("Resolve = %q, want the Host label", got)
	}
}

func TestResolveFallsBackWhenTheDynamicTitleIsBlank(t *testing.T) {
	got := Resolve(Input{HostLabel: "web-01", DynamicTitle: "   "},
		modelterminal.DynamicTabTitleAll)
	if got != "web-01" {
		t.Fatalf("Resolve = %q, want the Host label", got)
	}
}

func TestResolvePrefersTheUserRenameOverTheDynamicTitle(t *testing.T) {
	got := Resolve(Input{
		CustomName:   "Prod deploy",
		HostLabel:    "web-01",
		DynamicTitle: "claude: refactor auth",
		Agent:        true,
	}, modelterminal.DynamicTabTitleAll)
	if got != "Prod deploy" {
		t.Fatalf("Resolve = %q, want the rename", got)
	}
}

func TestResolveStripsAgentSpinnerPrefixes(t *testing.T) {
	got := Resolve(Input{
		HostLabel:    "web-01",
		DynamicTitle: "⠋ Droid",
		Agent:        true,
	}, modelterminal.DynamicTabTitleAgents)
	if got != "Droid" {
		t.Fatalf("Resolve = %q, want the stripped title", got)
	}
}

func TestNormalizeStripsDecorations(t *testing.T) {
	tests := map[string]string{
		"⠋ Droid":                     "Droid",
		"✳ Claude Code":               "Claude Code",
		"[!] Action Required · Build": "Build",
		"  • task":                    "task",
		"plain title":                 "plain title",
	}
	for title, want := range tests {
		if got := Normalize(title); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestAgentSessionKeepsItsDecisionAcrossAmbiguousTitles(t *testing.T) {
	tests := []struct {
		name     string
		previous bool
		title    string
		want     bool
	}{
		{"an agent signal starts the Session", false, "⠋ working", true},
		{"a named agent title starts the Session", false, "claude: task", true},
		{"an ambiguous title keeps the decision", true, "my-project", true},
		{"a shell name clears the decision", true, "zsh", false},
		{"a prompt path clears the decision", true, "root@host:/var/log", false},
		{"a Windows path clears the decision", true, `C:\Users\me`, false},
		{"an empty title clears the decision", true, "", false},
	}
	for _, test := range tests {
		if got := AgentSession(test.previous, test.title); got != test.want {
			t.Errorf("%s: AgentSession(%v, %q) = %v, want %v",
				test.name, test.previous, test.title, got, test.want)
		}
	}
}

func TestLooksLikeAgentTitle(t *testing.T) {
	agents := []string{
		"claude: refactor auth",
		"✳ Build the release",
		"⠋ Working",
		"Gemini CLI",
		"codex — running tests",
		"GitHub Copilot",
		"mimo code",
	}
	for _, title := range agents {
		if !LooksLikeAgentTitle(title) {
			t.Errorf("LooksLikeAgentTitle(%q) = false, want true", title)
		}
	}
	plain := []string{
		"",
		"root@host:/var/log",
		"user@host: ~/src",
		"claudette",
	}
	for _, title := range plain {
		if LooksLikeAgentTitle(title) {
			t.Errorf("LooksLikeAgentTitle(%q) = true, want false", title)
		}
	}
}
