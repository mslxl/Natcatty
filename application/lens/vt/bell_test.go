package vt

import "testing"

// A clear-screen sequence arrives in chunks as often as not; the scanner keeps
// its state across them.
func TestBellScannerFindsAClearSplitAcrossChunks(t *testing.T) {
	var s BellScanner
	if i, ev := s.NextEvent([]byte("text\x1b[2")); i >= 0 || ev != NoEvent {
		t.Fatalf("NextEvent(%q) = (%d, %v), want no event", "text\x1b[2", i, ev)
	}
	i, ev := s.NextEvent([]byte("JPROMPT> "))
	if i != 0 || ev != ClearEvent {
		t.Fatalf("NextEvent(%q) = (%d, %v), want the clear at 0", "JPROMPT> ", i, ev)
	}
}

// Only the clears that reset the screen count. Erase-above (1J) leaves the
// prompt where it is, and a "2J" that is OSC text is not a clear the terminal
// performed.
func TestBellScannerIgnoresOtherERasesAndClearText(t *testing.T) {
	var s BellScanner
	if i, ev := s.NextEvent([]byte("\x1b[1Jtext")); i >= 0 || ev != NoEvent {
		t.Fatalf("NextEvent(1J) = (%d, %v), want no event", i, ev)
	}
	if i, ev := s.NextEvent([]byte("\x1b]0;2J\x1b\\text")); i >= 0 || ev != NoEvent {
		t.Fatalf("NextEvent(OSC 2J) = (%d, %v), want no event", i, ev)
	}
}

// The bell keeps its meaning, and a clear does not hide a later bell.
func TestBellScannerStillFindsBellsAroundAClear(t *testing.T) {
	var s BellScanner
	data := []byte("\x1b[2J\x07")
	i, ev := s.NextEvent(data)
	if i != 3 || ev != ClearEvent {
		t.Fatalf("NextEvent(clear then bell) = (%d, %v), want the clear at 3", i, ev)
	}
	// A caller resumes right after the event's own byte.
	if i, ev = s.NextEvent(data[i+1:]); i != 0 || ev != BellEvent {
		t.Fatalf("NextEvent(after the clear) = (%d, %v), want the bell at 0", i, ev)
	}
}
