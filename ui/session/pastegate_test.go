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
	pending, ok := gate.Pending()
	if !ok || pending.Text != text || pending.Lines != 2 || pending.Chars != len([]rune("first line\nsecond line")) {
		t.Fatalf("pending = %+v, ok %v", pending, ok)
	}
	if !gate.Accept() {
		t.Fatal("Accept found nothing waiting")
	}
	if len(*sent) != 1 || (*sent)[0] != text {
		t.Fatalf("sent = %q, want exactly %q", *sent, text)
	}
	if _, ok := gate.Pending(); ok {
		t.Fatal("the accepted paste is still waiting")
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

func TestPasteGateOneLineIsDirectEvenWhenProtected(t *testing.T) {
	gate, sent := newTestPasteGate(true, 2)
	if !gate.Request("one line\n") {
		t.Fatal("a single line with a trailing newline waited")
	}
	if len(*sent) != 1 || (*sent)[0] != "one line\n" {
		t.Fatalf("sent = %q", *sent)
	}
}
