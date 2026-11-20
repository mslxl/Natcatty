package app

// The phone terminal is built over whatever SessionHandle.Attach and Resume
// return. The terminal probes that connection for optional methods: Resize is
// how the view reports the grid the keyboard inset and rotation leave it, and
// SetScreenSizeSink is where a peer-authoritative Session (Mosh, Eternal
// Terminal) installs the terminal's Resize. The adapter that adds
// ScreenSizeFromRemote must not hide either one.

import (
	"io"
	"testing"
)

// phoneConnProbe is a Session view that records what the terminal asks of it.
type phoneConnProbe struct {
	resizes [][2]int
	sink    func(cols, rows int)
}

func (v *phoneConnProbe) Read([]byte) (int, error) { return 0, io.EOF }

func (v *phoneConnProbe) Write(p []byte) (int, error) { return len(p), nil }

func (v *phoneConnProbe) Close() error { return nil }

func (v *phoneConnProbe) Resize(cols, rows int) error {
	v.resizes = append(v.resizes, [2]int{cols, rows})
	return nil
}

func (v *phoneConnProbe) SetScreenSizeSink(sink func(cols, rows int)) { v.sink = sink }

// The wrapper must keep every method newTerminalScreen asserts on, so a
// future change to its embedding cannot silently drop one.
var _ interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
	SetScreenSizeSink(func(cols, rows int))
	ScreenSizeFromRemote() bool
} = phoneView{}

func TestPhoneViewKeepsTheTerminalsConnectionMethods(t *testing.T) {
	inner := &phoneConnProbe{}
	view := phoneView{phoneSessionView: inner, remote: true}

	remote, ok := any(view).(interface{ ScreenSizeFromRemote() bool })
	if !ok || !remote.ScreenSizeFromRemote() {
		t.Fatal("the phone view does not report a peer-owned screen size")
	}

	resize, ok := any(view).(interface{ Resize(cols, rows int) error })
	if !ok {
		t.Fatal("the phone view hides Resize, so the terminal cannot report its grid")
	}
	if err := resize.Resize(132, 43); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if len(inner.resizes) != 1 || inner.resizes[0] != [2]int{132, 43} {
		t.Fatalf("the view got resizes %v, want one 132x43", inner.resizes)
	}

	sink, ok := any(view).(interface{ SetScreenSizeSink(func(cols, rows int)) })
	if !ok {
		t.Fatal("the phone view hides SetScreenSizeSink, so a peer's size never reaches the terminal")
	}
	var applied [2]int
	sink.SetScreenSizeSink(func(cols, rows int) { applied = [2]int{cols, rows} })
	if inner.sink == nil {
		t.Fatal("the phone view did not forward the sink to the Session's view")
	}
	inner.sink(132, 43)
	if applied != [2]int{132, 43} {
		t.Fatalf("the screen-size sink applied %v, want the peer's 132x43", applied)
	}
}
