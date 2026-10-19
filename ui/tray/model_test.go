package tray

import (
	"strings"
	"testing"

	"github.com/mslxl/natcatty/ui/locales"
)

func TestTabTextLocalizesFallbacksAndStatus(t *testing.T) {
	m := locales.In("en")
	line := TabText(m, Tab{Number: 3, Status: TabConnecting})
	if !strings.Contains(line, m.TabTitle(3)) || !strings.Contains(line, m.TrayStatusConnecting()) {
		t.Fatalf("line = %q", line)
	}
	line = TabText(m, Tab{Label: "web-1", Status: TabDisconnected})
	if !strings.Contains(line, "web-1") || !strings.Contains(line, m.TrayStatusDisconnected()) {
		t.Fatalf("line = %q", line)
	}
	line = TabText(m, Tab{Label: "web-1", Status: TabConnected})
	if !strings.Contains(line, m.TrayStatusConnected()) {
		t.Fatalf("line = %q", line)
	}
}

func TestRuleTextMarksDirectionAndStatus(t *testing.T) {
	m := locales.In("en")
	rule := Rule{Direction: "L", Label: "db", LocalPort: 5432, RemoteHost: "db.internal", RemotePort: 5432, Status: "active"}
	line := RuleText(m, rule)
	for _, want := range []string{"[L]", "db", ":5432", "db.internal:5432", m.TrayStatusActive()} {
		if !strings.Contains(line, want) {
			t.Fatalf("line = %q missing %q", line, want)
		}
	}
	line = RuleText(m, Rule{Direction: "R", LocalPort: 8080, Status: "error"})
	if !strings.Contains(line, "[R]") || !strings.Contains(line, m.TrayRuleUnnamed()) || !strings.Contains(line, m.TrayStatusError()) {
		t.Fatalf("line = %q", line)
	}
	if RuleKindLetter("remote") != "R" || RuleKindLetter("dynamic") != "D" || RuleKindLetter("local") != "L" {
		t.Fatal("direction letters do not match the original")
	}
	if RuleStatusText(m, "unknown") != m.TrayStatusInactive() {
		t.Fatal("an unknown status must render inactive")
	}
}
