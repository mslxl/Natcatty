package terminal

import (
	"bytes"
	"testing"
)

func TestNewlineModeSequence(t *testing.T) {
	tests := []struct {
		mode NewlineMode
		want []byte
	}{
		{NewlineCR, []byte{'\r'}},
		{NewlineLF, []byte{'\n'}},
		{NewlineCRLF, []byte{'\r', '\n'}},
		// An unknown value from a newer record must keep today's byte.
		{NewlineMode("vertical-tab"), []byte{'\r'}},
	}
	for _, test := range tests {
		if got := test.mode.Sequence(); !bytes.Equal(got, test.want) {
			t.Errorf("%q.Sequence() = %q, want %q", test.mode, got, test.want)
		}
	}
}

func TestBackspaceModeByte(t *testing.T) {
	tests := []struct {
		mode BackspaceMode
		want byte
	}{
		{BackspaceDEL, 0x7f},
		{BackspaceCtrlH, 0x08},
		{BackspaceMode("ctrl-j"), 0x7f},
	}
	for _, test := range tests {
		if got := test.mode.Byte(); got != test.want {
			t.Errorf("%q.Byte() = %#x, want %#x", test.mode, got, test.want)
		}
	}
}

func TestInputCompatibilityDefaultsAreByteIdentical(t *testing.T) {
	var zero InputCompatibility
	if !zero.IsDefault() {
		t.Fatal("the zero InputCompatibility is not the default")
	}
	if got := zero.Newline.Sequence(); !bytes.Equal(got, []byte{'\r'}) {
		t.Fatalf("default Enter = %q, want CR", got)
	}
	if got := zero.Backspace.Byte(); got != 0x7f {
		t.Fatalf("default Backspace = %#x, want DEL", got)
	}
}

func TestInputCompatibilityNormalizeKeepsKnownModes(t *testing.T) {
	want := InputCompatibility{
		Newline:     NewlineCRLF,
		Backspace:   BackspaceCtrlH,
		LineEditing: LineEditingByte,
	}
	if got := want.Normalize(); got != want {
		t.Fatalf("Normalize() = %+v, want %+v", got, want)
	}
	unknown := InputCompatibility{
		Newline:     NewlineMode("unknown"),
		Backspace:   BackspaceMode("unknown"),
		LineEditing: LineEditingMode("unknown"),
	}
	if got := unknown.Normalize(); !got.IsDefault() {
		t.Fatalf("Normalize(unknown) = %+v, want the default", got)
	}
}
