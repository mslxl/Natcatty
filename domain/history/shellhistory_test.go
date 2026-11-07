package history

import (
	"testing"
	"time"
)

func TestShellHistoryDuplicateMatchesCommandAndTimestampOrScope(t *testing.T) {
	base := Entry{Scope: "local", Command: "git status", RecordedAt: at(9)}
	tests := []struct {
		name      string
		candidate Entry
		existing  Entry
		want      bool
	}{
		{
			name:      "same command at the same time",
			candidate: Entry{Scope: "local", Command: "git status", RecordedAt: at(9)},
			existing:  base, want: true,
		},
		{
			name:      "same command within one second",
			candidate: Entry{Scope: "local", Command: "git status", RecordedAt: at(9).Add(time.Second)},
			existing:  base, want: true,
		},
		{
			name:      "same command one second earlier",
			candidate: Entry{Scope: "local", Command: "git status", RecordedAt: at(9).Add(-time.Second)},
			existing:  base, want: true,
		},
		{
			name:      "same command just over one second apart",
			candidate: Entry{Scope: "local", Command: "git status", RecordedAt: at(9).Add(time.Second + time.Millisecond)},
			existing:  base, want: false,
		},
		{
			name:      "different command at the same time",
			candidate: Entry{Scope: "local", Command: "git log", RecordedAt: at(9)},
			existing:  base, want: false,
		},
		{
			name:      "candidate text is trimmed before matching",
			candidate: Entry{Scope: "local", Command: "  git status  ", RecordedAt: at(9)},
			existing:  base, want: true,
		},
		{
			name:      "empty candidate command never matches",
			candidate: Entry{Scope: "local", Command: "  ", RecordedAt: at(9)},
			existing:  base, want: false,
		},
		{
			name:      "timestampless command matches by command and scope",
			candidate: Entry{Scope: "local", Command: "git status"},
			existing:  base, want: true,
		},
		{
			name:      "timestampless command in another scope is not a duplicate",
			candidate: Entry{Scope: "host:host-1", Command: "git status"},
			existing:  base, want: false,
		},
		{
			name:      "timestampless command without a scope never matches",
			candidate: Entry{Command: "git status"},
			existing:  base, want: false,
		},
		{
			name:      "an existing row without a timestamp cannot match by time",
			candidate: Entry{Scope: "local", Command: "git status", RecordedAt: at(9)},
			existing:  Entry{Scope: "local", Command: "git status"}, want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ShellHistoryDuplicate(test.candidate, test.existing); got != test.want {
				t.Fatalf("ShellHistoryDuplicate(%+v, %+v) = %v, want %v",
					test.candidate, test.existing, got, test.want)
			}
		})
	}
}
