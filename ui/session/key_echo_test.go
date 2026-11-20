package sessionui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/autocomplete"
	appsession "github.com/mslxl/natcatty/application/session"
)

// keyEchoSurface opens a Session surface whose suggestions come from suggest.
// The tester has typed "gi" at the prompt, so a provider that returns rows has
// its popup on screen.
func keyEchoSurface(t *testing.T,
	suggest func(input, scope, cwd string) []autocomplete.Suggestion) (*Surface, *recordingTransport, *ui.Tester) {
	t.Helper()
	transport := newRecordingTransport()
	service := appsession.NewService(recordingFactory{transport: transport})
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), appsession.StartSpec{Kind: appsession.KindLocal})
	if err != nil {
		t.Fatal(err)
	}
	surface, err := New(session,
		WithRedraw(func() {}),
		WithSuggestions(suggest, func() (bool, bool) { return true, false }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(surface.Close)
	tester := ui.NewTester(func(c *ui.Context) { surfaceFrame(c, surface, false) }, 720, 480)
	tester.SetLocale("en")
	tester.Frame()
	tester.ClickAt(360, 420)
	tester.Type("gi")
	if !waitFor(t, func() bool { return session.Snapshot().Input == "gi" }) {
		t.Fatalf("typed input = %q", session.Snapshot().Input)
	}
	tester.Frame()
	return surface, transport, tester
}

func historySuggestions(input, _, _ string) []autocomplete.Suggestion {
	return []autocomplete.Suggestion{
		{Text: input + " status", Source: "history"},
		{Text: input + " stash", Source: "history"},
	}
}

// A platform that commits the characters of a key after its key down must not
// get a tab through when Tab accepted a completion. No desktop commits Tab's
// text today — macOS makes it a command selector and mygo drops Windows's
// control characters — so the tester models the contract here; the real-shell
// test in app/ covers the reported flow end to end.
func TestTabAcceptsACompletionWithoutSendingATab(t *testing.T) {
	_, transport, tester := keyEchoSurface(t, historySuggestions)
	if !tester.HasText("gi status") {
		t.Fatalf("popup missing: %v", tester.Texts())
	}

	tester.TypeKey(0, ui.KeyTab, "\t")
	if !waitFor(t, func() bool { return strings.Contains(transport.input(), "\x7f\x7fgi status") }) {
		t.Fatalf("Tab did not accept the completion: %q", transport.input())
	}
	// The accepted text and the tab byte come from the same key. Give a
	// delayed duplicate a moment to show up before judging.
	time.Sleep(50 * time.Millisecond)
	if got := transport.input(); bytes.Contains([]byte(got), []byte("\t")) {
		t.Fatalf("a literal tab followed the accepted completion: %q", got)
	}
}

// With nothing to accept, Tab is the shell's own completion key and must reach
// it exactly once, not twice.
func TestTabWithoutSuggestionsReachesTheShellOnce(t *testing.T) {
	_, transport, tester := keyEchoSurface(t, func(string, string, string) []autocomplete.Suggestion {
		return nil
	})

	tester.TypeKey(0, ui.KeyTab, "\t")
	if !waitFor(t, func() bool { return strings.Count(transport.input(), "\t") >= 1 }) {
		t.Fatalf("Tab did not reach the shell: %q", transport.input())
	}
	time.Sleep(50 * time.Millisecond)
	if got := transport.input(); strings.Count(got, "\t") != 1 {
		t.Fatalf("Tab reached the shell %d times: %q", strings.Count(got, "\t"), got)
	}
}

// Ctrl-C is the only way to stop a long-running shell hook. It must always
// reach the program as one interrupt byte — even with the completion popup on
// screen — and the app must not add a second one or follow it with text. A
// shell hook that runs again on the next prompt (direnv's does) is stopped by
// the next Ctrl-C, so every press has to arrive.
func TestCtrlCReachesTheShellAsOneInterrupt(t *testing.T) {
	_, transport, tester := keyEchoSurface(t, historySuggestions)

	tester.Key(ui.Ctrl, ui.KeyC)
	if !waitFor(t, func() bool { return strings.Contains(transport.input(), "\x03") }) {
		t.Fatalf("Ctrl-C did not reach the shell: %q", transport.input())
	}
	tester.Key(ui.Ctrl, ui.KeyC)
	if !waitFor(t, func() bool { return strings.Count(transport.input(), "\x03") >= 2 }) {
		t.Fatalf("the second Ctrl-C did not reach the shell: %q", transport.input())
	}
	time.Sleep(50 * time.Millisecond)
	if got := transport.input(); got != "gi\x03\x03" {
		t.Fatalf("Ctrl-C produced %q, want one interrupt per press after the typed line", got)
	}
}
