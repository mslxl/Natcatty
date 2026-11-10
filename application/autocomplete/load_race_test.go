package autocomplete

import (
	"context"
	"testing"
	"time"

	domainhistory "github.com/mslxl/natcatty/domain/history"
)

// blockingJournal holds Load open until release closes, so a test can record
// a completion while a journal snapshot is in flight.
type blockingJournal struct {
	started chan struct{}
	release chan struct{}
}

func (j *blockingJournal) Load(context.Context) ([]domainhistory.Entry, error) {
	close(j.started)
	<-j.release
	return []domainhistory.Entry{{
		Scope:      "local",
		Command:    "alpha",
		RecordedAt: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC),
	}}, nil
}

func (j *blockingJournal) Record(context.Context, domainhistory.Entry) error { return nil }

// TestLoadKeepsACompletionRecordedWhileItReads pins the lost-update fix: a
// journal snapshot that starts before a completion cannot replace the view
// after it, so the command recorded during the load stays suggestible.
func TestLoadKeepsACompletionRecordedWhileItReads(t *testing.T) {
	journal := &blockingJournal{started: make(chan struct{}), release: make(chan struct{})}
	service := NewService(journal, func() Config {
		return Config{Enabled: true, MinChars: 1, MaxSuggestions: 5}
	})

	loaded := make(chan struct{})
	go func() {
		service.Load(context.Background())
		close(loaded)
	}()
	<-journal.started

	recorded := make(chan struct{})
	go func() {
		service.RecordEntry(domainhistory.Entry{Scope: "local", Command: "beta"})
		close(recorded)
	}()
	select {
	case <-recorded:
		t.Fatal("RecordEntry finished while Load was reading the journal")
	case <-time.After(50 * time.Millisecond):
	}

	close(journal.release)
	<-loaded
	<-recorded

	if got := service.Suggestions("a", "local"); !suggests(got, "alpha") {
		t.Fatalf("alpha suggestions = %+v", got)
	}
	if got := service.Suggestions("b", "local"); !suggests(got, "beta") {
		t.Fatalf("beta suggestions = %+v, want the completion recorded during the load", got)
	}
}

func suggests(suggestions []Suggestion, command string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Text == command {
			return true
		}
	}
	return false
}
