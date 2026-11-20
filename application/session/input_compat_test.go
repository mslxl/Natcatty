package session

import (
	"context"
	"fmt"
	"testing"

	"github.com/mslxl/natcatty/domain/model"
	modelterminal "github.com/mslxl/natcatty/domain/model/terminal"
)

func openInputCompatibilitySession(t *testing.T,
	compat modelterminal.InputCompatibility) (*Session, *fakeTransport) {
	t.Helper()
	transport := newFakeTransport()
	factory := &fakeFactory{transport: transport}
	service := newTestService(factory)
	t.Cleanup(service.CloseAll)
	session, err := service.Open(context.Background(), StartSpec{
		Kind:               KindSSH,
		Label:              "example.com",
		InputCompatibility: compat,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, transport
}

func typeAndWait(t *testing.T, session *Session, transport *fakeTransport,
	chunks []string, want string) {
	t.Helper()
	for _, chunk := range chunks {
		if _, err := session.TerminalConn().Write([]byte(chunk)); err != nil {
			t.Fatalf("Write(%q): %v", chunk, err)
		}
	}
	eventually(t, "the translated input", func() bool { return transport.input() == want })
	if got := transport.input(); got != want {
		t.Fatalf("transport input = %q, want %q", got, want)
	}
}

// TestSSHInputCompatibilityTranslatesEnterAndBackspace is the acceptance table:
// every mode produces its documented byte sequence, and the zero value is
// byte-identical to the input the Session received.
func TestSSHInputCompatibilityTranslatesEnterAndBackspace(t *testing.T) {
	tests := []struct {
		name   string
		compat modelterminal.InputCompatibility
		chunks []string
		want   string
	}{
		{
			name:   "default keeps today's bytes",
			chunks: []string{"echo hi\r", "\x7f"},
			want:   "echo hi\r\x7f",
		},
		{
			name:   "newline LF",
			compat: modelterminal.InputCompatibility{Newline: modelterminal.NewlineLF},
			chunks: []string{"echo hi\r"},
			want:   "echo hi\n",
		},
		{
			name:   "newline CRLF",
			compat: modelterminal.InputCompatibility{Newline: modelterminal.NewlineCRLF},
			chunks: []string{"echo hi\r"},
			want:   "echo hi\r\n",
		},
		{
			name:   "newline CRLF does not double an existing pair",
			compat: modelterminal.InputCompatibility{Newline: modelterminal.NewlineCRLF},
			chunks: []string{"one\r\ntwo\r"},
			want:   "one\r\ntwo\r\n",
		},
		{
			name:   "newline LF collapses an existing pair",
			compat: modelterminal.InputCompatibility{Newline: modelterminal.NewlineLF},
			chunks: []string{"one\r\ntwo\r"},
			want:   "one\ntwo\n",
		},
		{
			name:   "backspace Control-H",
			compat: modelterminal.InputCompatibility{Backspace: modelterminal.BackspaceCtrlH},
			chunks: []string{"ab\x7f"},
			want:   "ab\x08",
		},
		{
			name: "byte-oriented backspace repeats per UTF-8 byte",
			compat: modelterminal.InputCompatibility{
				LineEditing: modelterminal.LineEditingByte,
			},
			chunks: []string{"中\x7f"},
			want:   "中\x7f\x7f\x7f",
		},
		{
			name: "byte-oriented backspace follows successive characters",
			compat: modelterminal.InputCompatibility{
				LineEditing: modelterminal.LineEditingByte,
			},
			chunks: []string{"a中\x7f\x7f"},
			want:   "a中\x7f\x7f\x7f\x7f",
		},
		{
			name: "byte-oriented backspace spans writes",
			compat: modelterminal.InputCompatibility{
				LineEditing: modelterminal.LineEditingByte,
			},
			chunks: []string{"中", "\x7f"},
			want:   "中\x7f\x7f\x7f",
		},
		{
			name: "byte-oriented backspace uses the configured erase byte",
			compat: modelterminal.InputCompatibility{
				Backspace:   modelterminal.BackspaceCtrlH,
				LineEditing: modelterminal.LineEditingByte,
			},
			chunks: []string{"中文\x7f"},
			want:   "中文\x08\x08\x08",
		},
		{
			name: "all modes compose",
			compat: modelterminal.InputCompatibility{
				Newline:     modelterminal.NewlineCRLF,
				Backspace:   modelterminal.BackspaceCtrlH,
				LineEditing: modelterminal.LineEditingByte,
			},
			chunks: []string{"中\x7f\r"},
			want:   "中\x08\x08\x08\r\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session, transport := openInputCompatibilitySession(t, test.compat)
			typeAndWait(t, session, transport, test.chunks, test.want)
		})
	}
}

// TestSSHInputCompatibilityLeavesProgrammaticInputAlone proves the translation
// is a keyboard-compatibility setting: an insertion from a snippet, compose,
// or a script already carries the bytes its caller chose.
func TestSSHInputCompatibilityLeavesProgrammaticInputAlone(t *testing.T) {
	session, transport := openInputCompatibilitySession(t, modelterminal.InputCompatibility{
		Newline:   modelterminal.NewlineCRLF,
		Backspace: modelterminal.BackspaceCtrlH,
	})
	if !session.InsertInput([]byte("printf 'hi'\r")) {
		t.Fatal("InsertInput was refused")
	}
	eventually(t, "the inserted input", func() bool {
		return transport.input() == "printf 'hi'\r"
	})
	if got := transport.input(); got != "printf 'hi'\r" {
		t.Fatalf("transport input = %q, want the inserted bytes unchanged", got)
	}
}

// TestSSHInputCompatibilityLeavesBracketedPasteAlone proves the modes apply to
// Enter and Backspace keys, not to text a terminal pasted into the line
// editor: a pasted carriage return or DEL is content, not a keystroke.
func TestSSHInputCompatibilityLeavesBracketedPasteAlone(t *testing.T) {
	session, transport := openInputCompatibilitySession(t, modelterminal.InputCompatibility{
		Newline:     modelterminal.NewlineCRLF,
		Backspace:   modelterminal.BackspaceCtrlH,
		LineEditing: modelterminal.LineEditingByte,
	})
	const paste = "\x1b[200~one\r\ntwo\x7f中\x1b[201~"
	// After the paste, typed keys translate again. The tracked line was
	// cleared by the marker, so one Backspace sends one erase byte.
	typeAndWait(t, session, transport, []string{paste, "\r", "\x7f"},
		paste+"\r\n\x08")
}

// TestSSHInputCompatibilityRecognizesSplitPasteMarkers proves the scanner
// survives a terminal write boundary inside either marker.
func TestSSHInputCompatibilityRecognizesSplitPasteMarkers(t *testing.T) {
	session, transport := openInputCompatibilitySession(t, modelterminal.InputCompatibility{
		Newline:     modelterminal.NewlineCRLF,
		Backspace:   modelterminal.BackspaceCtrlH,
		LineEditing: modelterminal.LineEditingByte,
	})
	typeAndWait(t, session, transport,
		[]string{"\x1b[20", "0~x\r\x1b[20", "1~", "\r"},
		"\x1b[200~x\r\x1b[201~\r\n")
}

// TestSSHInputCompatibilityAppliesToEachBroadcastTarget proves a target's own
// mode governs its bytes even when the input was typed in another Session.
func TestSSHInputCompatibilityAppliesToEachBroadcastTarget(t *testing.T) {
	factory := newTargetFactory()
	counter := 0
	service := NewService(factory, WithIDGenerator(func() model.SessionID {
		counter++
		return model.SessionID(fmt.Sprintf("session-%d", counter))
	}))
	t.Cleanup(service.CloseAll)
	targets := []StartSpec{
		{Kind: KindSSH, Label: "plain"},
		{Kind: KindSSH, Label: "crlf",
			InputCompatibility: modelterminal.InputCompatibility{Newline: modelterminal.NewlineCRLF}},
	}
	ids := make([]model.SessionID, 0, len(targets))
	for _, spec := range targets {
		session, err := service.Open(context.Background(), spec)
		if err != nil {
			t.Fatalf("Open(%s): %v", spec.Label, err)
		}
		ids = append(ids, session.ID())
	}

	if result := service.SendInput(ids, []byte("echo hi\r")); !result.Complete() {
		t.Fatalf("SendInput = %+v, want complete", result)
	}
	eventually(t, "the plain target", func() bool {
		return factory.transport("plain").input() == "echo hi\r"
	})
	eventually(t, "the CRLF target", func() bool {
		return factory.transport("crlf").input() == "echo hi\r\n"
	})
}
