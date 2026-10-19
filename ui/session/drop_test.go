package sessionui

import (
	"errors"
	"testing"

	domainterminal "github.com/mslxl/natcatty/domain/terminal"
)

func TestQuoteDropPathsQuotesSpacesAndQuotesForPOSIX(t *testing.T) {
	got := QuoteDropPaths([]string{
		`/tmp/plain.txt`,
		`/tmp/with space.txt`,
		`/tmp/it's here.txt`,
	}, false)
	want := `'/tmp/plain.txt' '/tmp/with space.txt' '/tmp/it'\''s here.txt'`
	if got != want {
		t.Fatalf("quoted = %q\nwant      %q", got, want)
	}
}

func TestQuoteDropPathsUsesTheWindowsDoubleQuoteForm(t *testing.T) {
	got := QuoteDropPaths([]string{
		`C:\Users\me\Program Files\a.txt`,
		`C:\Users\me\it's.txt`,
	}, true)
	want := `"C:\Users\me\Program Files\a.txt" "C:\Users\me\it's.txt"`
	if got != want {
		t.Fatalf("quoted = %q\nwant      %q", got, want)
	}
}

func TestDropHandlerInsertsEveryPathAsOneQuotedChunk(t *testing.T) {
	var inserted string
	handler := NewDropHandler(
		func() domainterminal.DropAction { return domainterminal.DropInsert },
		func(text string) { inserted = text }, nil)
	notice := handler.Drop([]string{`C:\a b\one.txt`, `C:\two's.txt`}, true)
	if notice != DropNoticeInserted {
		t.Fatalf("notice = %q", notice)
	}
	if inserted != `"C:\a b\one.txt" "C:\two's.txt"` {
		t.Fatalf("inserted = %q", inserted)
	}
	if got, count := handler.Notice(); got != DropNoticeInserted || count != 2 {
		t.Fatalf("notice = %q, count = %d", got, count)
	}
}

func TestDropHandlerUploadUsesTheTransportWhenPresent(t *testing.T) {
	var uploaded []string
	handler := NewDropHandler(
		func() domainterminal.DropAction { return domainterminal.DropUpload },
		func(string) { t.Fatal("an upload inserted a path") },
		func(files []string) error {
			uploaded = append([]string(nil), files...)
			return nil
		})
	if notice := handler.Drop([]string{"a", "b"}, false); notice != DropNoticeUploaded {
		t.Fatalf("notice = %q", notice)
	}
	if len(uploaded) != 2 || uploaded[0] != "a" || uploaded[1] != "b" {
		t.Fatalf("uploaded = %v", uploaded)
	}
}

func TestDropHandlerUploadWithoutTransportIsExplicitAndNonDestructive(t *testing.T) {
	inserted := false
	handler := NewDropHandler(
		func() domainterminal.DropAction { return domainterminal.DropUpload },
		func(string) { inserted = true }, nil)
	if notice := handler.Drop([]string{"secret.txt"}, false); notice != DropNoticeUnavailable {
		t.Fatalf("notice = %q", notice)
	}
	if inserted {
		t.Fatal("an unavailable upload inserted a path anyway")
	}
}

func TestDropHandlerUploadFailureIsReportedWithoutInserting(t *testing.T) {
	inserted := false
	handler := NewDropHandler(
		func() domainterminal.DropAction { return domainterminal.DropUpload },
		func(string) { inserted = true },
		func([]string) error { return errors.New("connection lost") })
	if notice := handler.Drop([]string{"a.txt"}, false); notice != DropNoticeFailed {
		t.Fatalf("notice = %q", notice)
	}
	if inserted {
		t.Fatal("a failed upload inserted a path")
	}
}
