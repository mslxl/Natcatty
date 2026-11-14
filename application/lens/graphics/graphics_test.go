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
