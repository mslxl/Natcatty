package session

import (
	"testing"

	"github.com/mslxl/natcatty/domain/model"
)

func TestTransformComposeDefaultsToPasteWithSubmit(t *testing.T) {
	preview := TransformCompose("ls -la", ComposeOptions{})
	if preview.Text != "ls -la" {
		t.Fatalf("text = %q", preview.Text)
	}
	if preview.LineMode != ComposePaste || !preview.AutoRun {
		t.Fatalf("mode = %q autoRun = %v", preview.LineMode, preview.AutoRun)
	}
	if len(preview.Chunks) != 1 || string(preview.Chunks[0]) != "ls -la\r" {
		t.Fatalf("chunks = %q", preview.Chunks)
	}
	if preview.Bytes() != len("ls -la\r") {
		t.Fatalf("bytes = %d", preview.Bytes())
	}
}

func TestTransformComposeNormalizesLineEndingsAndKeepsOneSubmit(t *testing.T) {
	for _, input := range []string{"echo one\r\necho two", "echo one\recho two", "echo one\necho two"} {
		preview := TransformCompose(input, ComposeOptions{})
		if preview.Text != "echo one\necho two" {
			t.Fatalf("%q text = %q", input, preview.Text)
		}
		if len(preview.Chunks) != 1 || string(preview.Chunks[0]) != "echo one\necho two\r" {
			t.Fatalf("%q chunks = %q", input, preview.Chunks)
		}
	}
}

func TestTransformComposeLineByLineSubmitsEachLineSeparately(t *testing.T) {
	preview := TransformCompose("echo one\necho two\n", ComposeOptions{LineMode: ComposeLineByLine})
	if len(preview.Chunks) != 2 {
		t.Fatalf("chunks = %q", preview.Chunks)
	}
	if string(preview.Chunks[0]) != "echo one\r" || string(preview.Chunks[1]) != "echo two\r" {
		t.Fatalf("chunks = %q", preview.Chunks)
	}
	if preview.LineMode != ComposeLineByLine {
		t.Fatalf("mode = %q", preview.LineMode)
	}
}

func TestTransformComposeDecodesEscapesBeforeNormalizing(t *testing.T) {
	preview := TransformCompose(`printf 'a\nb\tc\ed\\'`, ComposeOptions{DecodeEscapes: true})
	want := "printf 'a\nb\tc\x1bd\\'"
	if preview.Text != want {
		t.Fatalf("text = %q, want %q", preview.Text, want)
	}
	if got := preview.PreviewText(); got != "printf 'a\\nb\\tc\\ed\\'" {
		t.Fatalf("preview = %q", got)
	}
	if string(preview.Chunks[0]) != want+"\r" {
		t.Fatalf("chunk = %q", preview.Chunks[0])
	}
}

func TestTransformComposeTrimsAndStripsZeroWidthInput(t *testing.T) {
	preview := TransformCompose("  echo​ hi  ", ComposeOptions{Trim: true})
	if preview.Text != "echo hi" {
		t.Fatalf("text = %q", preview.Text)
	}
}

func TestTransformComposeDoesNotSubmitAnEmptyDraft(t *testing.T) {
	for _, test := range []struct {
		text    string
		options ComposeOptions
	}{
		{"​", ComposeOptions{}},
		{"​", ComposeOptions{LineMode: ComposeLineByLine}},
		{"  ​ ", ComposeOptions{Trim: true}},
	} {
		preview := TransformCompose(test.text, test.options)
		if !preview.Empty() {
			t.Fatalf("%q with %+v produced %q", test.text, test.options, preview.Chunks)
		}
	}
}

func TestTransformComposeNoAutoRunOmitsTheCarriageReturn(t *testing.T) {
	preview := TransformCompose("echo hi", ComposeOptions{NoAutoRun: true})
	if len(preview.Chunks) != 1 || string(preview.Chunks[0]) != "echo hi" {
		t.Fatalf("chunks = %q", preview.Chunks)
	}
}

func TestComposePasteDataWrapsOnlyAMultilineBracketedTarget(t *testing.T) {
	multiline := TransformCompose("echo one\necho two", ComposeOptions{})
	if got, want := string(multiline.PasteData(true)),
		bracketedPasteStart+"echo one\necho two"+bracketedPasteEnd+"\r"; got != want {
		t.Fatalf("bracketed multiline = %q, want %q", got, want)
	}
	if got, want := string(multiline.PasteData(false)), "echo one\necho two\r"; got != want {
		t.Fatalf("plain multiline = %q, want %q", got, want)
	}

	single := TransformCompose("ls -la", ComposeOptions{})
	if got, want := string(single.PasteData(true)), "ls -la\r"; got != want {
		t.Fatalf("single line = %q, want %q; a single line is never wrapped", got, want)
	}

	noSubmit := TransformCompose("echo one\necho two", ComposeOptions{NoAutoRun: true})
	if got, want := string(noSubmit.PasteData(true)),
		bracketedPasteStart+"echo one\necho two"+bracketedPasteEnd; got != want {
		t.Fatalf("no-auto-run = %q, want %q", got, want)
	}

	lineByLine := TransformCompose("echo one\necho two", ComposeOptions{LineMode: ComposeLineByLine})
	if got := lineByLine.PasteData(true); got != nil {
		t.Fatalf("line-by-line used the paste path: %q", got)
	}
}

// TestSendComposeWrapsPerTargetPeerState proves a broadcast submission is
// encoded per Session: the paste-capable peer gets one wrapped paste and the
// other keeps the raw lines.
func TestSendComposeWrapsPerTargetPeerState(t *testing.T) {
	service, factory, sessions, ids := openTargets(t, 2)
	factory.transport("target-0").send("\x1b[?2004h")
	waitForCondition(t, "the peer's bracketed-paste mode", func() bool {
		return sessions[0].BracketedPaste()
	})
	if sessions[1].BracketedPaste() {
		t.Fatal("target-1 unexpectedly advertises bracketed paste")
	}

	preview := TransformCompose("echo one\necho two", ComposeOptions{})
	result := service.SendCompose(ids, preview)
	if !result.Complete() {
		t.Fatalf("result = %+v, want complete", result)
	}
	waitForInput(t, factory.transport("target-0"),
		bracketedPasteStart+"echo one\necho two"+bracketedPasteEnd+"\r")
	waitForInput(t, factory.transport("target-1"), "echo one\necho two\r")
}

// TestSendComposeSingleLineKeepsTheCurrentPath proves a single-line draft is
// not bracketed even when the peer advertises the mode.
func TestSendComposeSingleLineKeepsTheCurrentPath(t *testing.T) {
	service, factory, sessions, ids := openTargets(t, 1)
	factory.transport("target-0").send("\x1b[?2004h")
	waitForCondition(t, "the peer's bracketed-paste mode", func() bool {
		return sessions[0].BracketedPaste()
	})

	preview := TransformCompose("ls -la", ComposeOptions{})
	service.SendCompose([]model.SessionID{ids[0]}, preview)
	waitForInput(t, factory.transport("target-0"), "ls -la\r")
}
