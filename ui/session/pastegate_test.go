package sessionui

import "testing"

func newTestPasteGate(protection bool, minLines int) (*PasteGate, *[]string) {
	sent := &[]string{}
	gate := NewPasteGate(func() (bool, int) { return protection, minLines },
		func(text string) { *sent = append(*sent, text) })
	return gate, sent
}

func TestPasteGateProtectedPasteWaitsAndSendsExactlyOnAccept(t *testing.T) {
	gate, sent := newTestPasteGate(true, 2)
	text := "first line\r\nsecond line"
	if gate.Request(text) {
		t.Fatal("a guarded paste was sent without confirmation")
	}
	if len(*sent) != 0 {
		t.Fatalf("a guarded paste reached the terminal before acceptance: %q", *sent)
	}
	want := "first line\nsecond line"
	pending, ok := gate.Pending()
	if !ok || pending.Text != want || pending.Lines != 2 || pending.Chars != len([]rune(want)) {
		t.Fatalf("pending = %+v, ok %v, want normalized %q", pending, ok, want)
	}
	if !gate.Accept() {
		t.Fatal("Accept found nothing waiting")
	}
	if len(*sent) != 1 || (*sent)[0] != want {
		t.Fatalf("sent = %q, want exactly %q", *sent, want)
	}
	if _, ok := gate.Pending(); ok {
		t.Fatal("the accepted paste is still waiting")
	}
}

// TestPasteGateNormalizesEveryLineEndingOnce is the ticket 61 regression for
// pasted Windows and old-Mac text. MyGo's vt.EncodePaste turns each LF into a
// carriage return before the bytes reach the wire, so a raw CRLF would arrive
// as CR CR (a blank line on the remote shell). The gate normalizes CRLF and a
// lone CR to LF exactly once, before the text is stored or sent, so every
// paste path (the terminal's paste key, the Session menu, and the clipboard
// banner) delivers one newline per line in SSH, serial, and Telnet Sessions.
func TestPasteGateNormalizesEveryLineEndingOnce(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"crlf", "one\r\ntwo\r\nthree\r\n", "one\ntwo\nthree\n"},
		{"lone-cr", "one\rtwo\rthree", "one\ntwo\nthree"},
		{"mixed", "one\r\ntwo\rthree\nfour", "one\ntwo\nthree\nfour"},
		{"already-lf", "one\ntwo\n", "one\ntwo\n"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Protection off: the immediate path must normalize.
			direct, sent := newTestPasteGate(false, 2)
			if !direct.Request(test.in) {
				t.Fatal("an unprotected paste waited")
			}
			if len(*sent) != 1 || (*sent)[0] != test.want {
				t.Fatalf("immediate send = %q, want %q", *sent, test.want)
			}

			// Protection on: the stored text and its preview must be the same
			// normalized text the user accepts.
			guarded, guardedSent := newTestPasteGate(true, 2)
			if guarded.Request(test.in) {
				t.Fatal("a guarded paste was sent without confirmation")
			}
			pending, ok := guarded.Pending()
			if !ok || pending.Text != test.want {
				t.Fatalf("pending text = %q (ok %v), want %q", pending.Text, ok, test.want)
			}
			if pending.Preview != test.want {
				t.Fatalf("preview = %q, want the normalized %q", pending.Preview, test.want)
			}
			if pending.Chars != len([]rune(test.want)) {
				t.Fatalf("chars = %d, want %d", pending.Chars, len([]rune(test.want)))
			}
			if !guarded.Accept() {
				t.Fatal("Accept found nothing waiting")
			}
			if len(*guardedSent) != 1 || (*guardedSent)[0] != test.want {
				t.Fatalf("accepted send = %q, want %q", *guardedSent, test.want)
			}
		})
	}
}

func TestPasteGateCancelSendsNothingAndLeavesNoPartialText(t *testing.T) {
	gate, sent := newTestPasteGate(true, 2)
	gate.Request("alpha\nbeta")
	if !gate.Cancel() {
		t.Fatal("Cancel found nothing waiting")
	}
	if len(*sent) != 0 {
		t.Fatalf("Cancel sent %q", *sent)
	}
	if gate.Accept() {
		t.Fatal("a cancelled paste was available to Accept")
	}
	if len(*sent) != 0 {
		t.Fatalf("Accept after Cancel sent %q", *sent)
	}
}

func TestPasteGateANewerPasteSupersedesTheWaitingOne(t *testing.T) {
	gate, sent := newTestPasteGate(true, 2)
	gate.Request("old\npaste")
	gate.Request("new\npaste")
	if gate.Accept() != true {
		t.Fatal("the replacement was not waiting")
	}
	if len(*sent) != 1 || (*sent)[0] != "new\npaste" {
		t.Fatalf("sent = %q, want only the replacement", *sent)
	}
}

func TestPasteGateProtectionOffSendsImmediately(t *testing.T) {
	gate, sent := newTestPasteGate(false, 2)
	if !gate.Request("first\nsecond") {
		t.Fatal("an unprotected paste waited")
	}
	if len(*sent) != 1 || (*sent)[0] != "first\nsecond" {
		t.Fatalf("sent = %q", *sent)
	}
	if _, ok := gate.Pending(); ok {
		t.Fatal("an immediate paste is pending")
	}
}

func TestPasteGateNormalizesTheWaitingTextBeforeAccept(t *testing.T) {
	gate, sent := newTestPasteGate(true, 2)
	if gate.Request("alpha\r\nbeta\r") {
		t.Fatal("a guarded paste was sent without confirmation")
	}
	if !gate.Accept() {
		t.Fatal("Accept found nothing waiting")
	}
	if len(*sent) != 1 || (*sent)[0] != "alpha\nbeta\n" {
		t.Fatalf("sent = %q, want one normalized newline per line", *sent)
	}
}

func TestPasteGateOneLineIsDirectEvenWhenProtected(t *testing.T) {
	gate, sent := newTestPasteGate(true, 2)
	if !gate.Request("one line\n") {
		t.Fatal("a single line with a trailing newline waited")
	}
	if len(*sent) != 1 || (*sent)[0] != "one line\n" {
		t.Fatalf("sent = %q", *sent)
	}
}
