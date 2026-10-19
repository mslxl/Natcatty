package session

import "testing"

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
