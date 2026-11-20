package graphics

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Kitty's APC graphics: ESC _ G <params>;<base64> ESC \, including a chunked
// transmission (m=1) split across FEED calls.
func TestKittyGraphicsChunked(t *testing.T) {
	data := testPNG(t)
	b64 := base64.StdEncoding.EncodeToString(data)
	half := len(b64) / 2
	p := New()
	seq := "\x1b_Ga=T,f=100,i=7,m=1;" + b64[:half] + "\x1b\\" +
		"\x1b_Gm=0,i=7;" + b64[half:] + "\x1b\\"
	var got []Image
	for i := 0; i < len(seq); i += 3 {
		end := i + 3
		if end > len(seq) {
			end = len(seq)
		}
		got = append(got, p.Feed([]byte(seq[i:end]))...)
	}
	if len(got) != 1 {
		t.Fatalf("images = %d, want 1", len(got))
	}
	if !bytes.Equal(got[0].Data, data) {
		t.Fatalf("kitty payload was not reassembled")
	}
}

// yazi checks for graphics support before it draws anything: two Kitty queries
// (a=q), one with a small direct payload, and DECRQSS's sixel "$q". None of
// them is a picture, and decoding them left small marks under the command that
// ran yazi once it exited.
func TestGraphicsIgnoresCapabilityProbes(t *testing.T) {
	seq := "\x1b_Gi=278941603,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\" +
		"\x1b_Gi=916472805,s=1,v=1,a=q,t=s,f=24,S=3;L3lhemktSEJDVlBUTENSM1lJR1hRQk80M0NXVUhI\x1b\\" +
		"\x1bP$q q\x1b\\"
	if got := New().Feed([]byte(seq)); len(got) != 0 {
		t.Fatalf("probe sequences produced %d images: %+v", len(got), got)
	}
}

// A real transmit-and-display after the queries still yields its picture.
func TestKittyTransmitDisplayAfterProbe(t *testing.T) {
	data := testPNG(t)
	b64 := base64.StdEncoding.EncodeToString(data)
	seq := "\x1b_Gi=1,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\" +
		"\x1b_Ga=T,f=100,i=9;" + b64 + "\x1b\\"
	got := New().Feed([]byte(seq))
	if len(got) != 1 || !bytes.Equal(got[0].Data, data) {
		t.Fatalf("images after probe = %+v", got)
	}
}

// The pane asks for the terminal's own view when the program uses the graphics
// protocol at all, because a program that only queries it is about to draw
// (yazi queries before its previews). Other DCS traffic is not graphics.
func TestSawGraphics(t *testing.T) {
	p := New()
	if p.Feed([]byte("plain text")); p.SawGraphics() {
		t.Fatal("plain text counted as graphics")
	}
	if p.Feed([]byte("\x1bP+q544e\x1b\\")); p.SawGraphics() {
		t.Fatal("XTGETTCAP counted as graphics")
	}
	p.Feed([]byte("\x1b_Gi=1,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"))
	if !p.SawGraphics() {
		t.Fatal("a kitty query did not count as graphics")
	}
	p.ForgetGraphics()
	if p.SawGraphics() {
		t.Fatal("ForgetGraphics did not clear the activity")
	}
	p.Feed([]byte("\x1bP$q q\x1b\\"))
	if !p.SawGraphics() {
		t.Fatal("the sixel capability query did not count as graphics")
	}
}

// iTerm2's OSC 1337 File inline image.
func TestITermInlineImage(t *testing.T) {
	data := testPNG(t)
	b64 := base64.StdEncoding.EncodeToString(data)
	seq := "\x1b]1337;File=name=" + base64.StdEncoding.EncodeToString([]byte("pic.png")) +
		";inline=1:" + b64 + "\x07"
	got := New().Feed([]byte("hello" + seq + "world"))
	if len(got) != 1 || got[0].Name != "pic.png" || !bytes.Equal(got[0].Data, data) {
		t.Fatalf("iterm image = %+v", got)
	}
}

// Ordinary text and SGR sequences produce nothing.
func TestGraphicsIgnoresTextAndSGR(t *testing.T) {
	got := New().Feed([]byte("plain \x1b[31mred\x1b[0m text\n"))
	if len(got) != 0 {
		t.Fatalf("unexpected images: %+v", got)
	}
}
