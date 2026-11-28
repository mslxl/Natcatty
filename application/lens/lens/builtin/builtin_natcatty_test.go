package builtin

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/mslxl/natcatty/application/lens/lens"
	"github.com/mslxl/natcatty/application/lens/model"
)

func TestRegisteredLensCatalogIncludesTerminalFamilies(t *testing.T) {
	registered := lens.Registered()
	ids := make([]string, 0, len(registered))
	for _, definition := range registered {
		ids = append(ids, definition.ID)
	}
	sort.Strings(ids)
	t.Logf("registered lens ids (%d): %v", len(ids), ids)
	if len(ids) != 75 {
		t.Fatalf("registered lens ids = %d, want 75", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatalf("duplicate lens registration: %s", ids[i])
		}
	}
	for _, id := range []string{
		"files.ls", "git.status", "git.diff", "net.ping", "search.grep",
		"go.build", "go.test", "rust.cargo", "docker.build", "k8s.get",
		"js.pkg", "docs.help", "pkg.outdated", "gh.view",
	} {
		found := false
		for _, definition := range registered {
			if definition.ID == id {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("lens %q is missing", id)
		}
	}
}

func TestPingCommandTriggersNetPingLens(t *testing.T) {
	for _, program := range []string{"ping", "ping6"} {
		got := lens.Lookup(model.Cmd{Program: program})
		if got == nil || got.ID() != "net.ping" {
			t.Fatalf("Lookup(%q) = %v, want net.ping", program, got)
		}
	}
	cmd := lens.Normalize("ping.exe -n 1 127.0.0.1", "", "")
	if got := lens.Lookup(cmd); got == nil || got.ID() != "net.ping" {
		t.Fatalf("Lookup(%q) = %v, want net.ping", cmd.Program, got)
	}
}

func TestWindowsPingOutputBuildsNativeView(t *testing.T) {
	l := lens.ByID("net.ping")
	if l == nil {
		t.Fatal("net.ping is not registered")
	}
	state, err := l.Open(model.Cmd{Program: "ping", Args: []string{"-n", "1", "example.com"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(
		"Pinging example.com [93.184.216.34] with 32 bytes of data:\n"+
			"Reply from 93.184.216.34: bytes=32 time=12ms TTL=56\n"+
			"\n"+
			"Ping statistics for 93.184.216.34:\n"+
			"    Packets: Sent = 1, Received = 1, Lost = 0 (0% loss),\n"+
			"Approximate round trip times in milli-seconds:\n"+
			"    Minimum = 12ms, Maximum = 12ms, Average = 12ms\n", "\n") {
		if err := l.Line(state, model.Line{Text: line}); err != nil {
			t.Fatal(err)
		}
	}
	node, err := l.View(state)
	if err != nil || node == nil {
		t.Fatalf("Windows ping view = %v, %v; want native view", node, err)
	}
}

// Camera and phone JPEGs embed an EXIF thumbnail (with its own FFD9 EOI) inside
// an APP1 segment near the start of the file. trimJPEG must skip length-prefixed
// segments instead of truncating at the thumbnail's FFD9, and Complete must
// report the real file size rather than subtracting the shell prefix skip length.
func TestViewFileJPEGWithEXIFThumbnailAndShellPrefix(t *testing.T) {
	// Encode a small real JPEG.
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 120, A: 255})
		}
	}
	var rawJPEG bytes.Buffer
	if err := jpeg.Encode(&rawJPEG, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	base := rawJPEG.Bytes()
	// Build a synthetic APP1 (EXIF) segment containing an embedded thumbnail ending in FFD9.
	exifBody := []byte("Exif\x00\x00\xff\xd8\xff\xe0fake-thumb\xff\xd9tail")
	app1Len := len(exifBody) + 2
	var jpegWithEXIF bytes.Buffer
	jpegWithEXIF.Write(base[:2]) // SOI FF D8
	jpegWithEXIF.Write([]byte{0xff, 0xe1, byte(app1Len >> 8), byte(app1Len)})
	jpegWithEXIF.Write(exifBody)
	jpegWithEXIF.Write(base[2:])
	fullJPEG := jpegWithEXIF.Bytes()

	dir := t.TempDir()
	fpath := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(fpath, fullJPEG, 0o644); err != nil {
		t.Fatal(err)
	}

	l := lens.ByID("view.file")
	sink, ok := l.(lens.ByteSink)
	if !ok {
		t.Fatal("view.file does not implement ByteSink")
	}
	state, err := l.Open(model.Cmd{Program: "cat", Args: []string{"photo.jpg"}, Cwd: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.CaptureBytes(state) {
		t.Fatal("CaptureBytes = false, want true for photo.jpg")
	}

	// Simulate starship/atuin title sequences before JPEG bytes and trailing shell noise after EOI.
	shellPrefix := []byte("\x1b]2;cat photo.jpg\x07\x1b]1;cat\x07")
	trailing := []byte("\r\n\x1b]2;zsh\x07")
	if err := sink.Bytes(state, append(append([]byte(nil), shellPrefix...), fullJPEG[:len(fullJPEG)/2]...)); err != nil {
		t.Fatal(err)
	}
	midNode, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	midImg, ok := midNode.(lens.Image)
	if !ok {
		t.Fatalf("mid view = %T, want lens.Image", midNode)
	}
	if midImg.Complete != len(fullJPEG) {
		t.Fatalf("mid Complete = %d, want full file size %d (must not subtract skip %d)", midImg.Complete, len(fullJPEG), len(shellPrefix))
	}
	midCopy := append([]byte(nil), midImg.Data...)

	if err := sink.Bytes(state, append(append([]byte(nil), fullJPEG[len(fullJPEG)/2:]...), trailing...)); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(midImg.Data, midCopy) {
		t.Fatal("subsequent Bytes() mutated previously returned lens.Image.Data")
	}
	if err := l.Finish(state, 0); err != nil {
		t.Fatal(err)
	}
	finalNode, err := l.View(state)
	if err != nil {
		t.Fatal(err)
	}
	finalImg, ok := finalNode.(lens.Image)
	if !ok {
		t.Fatalf("final view = %T, want lens.Image", finalNode)
	}
	if !bytes.Equal(finalImg.Data, fullJPEG) {
		t.Fatalf("final JPEG length = %d, want %d (EXIF thumbnail FFD9 must not truncate image)", len(finalImg.Data), len(fullJPEG))
	}
	if _, err := jpeg.Decode(bytes.NewReader(finalImg.Data)); err != nil {
		t.Fatalf("final JPEG does not decode: %v", err)
	}
}

// Remote SSH PTYs keep ONLCR enabled so normal commands do not staircase, which
// expands every 0x0a byte in `cat image.*` into 0x0d 0x0a. view.file must detect
// ONLCR and reverse it losslessly across fragmented chunks so remote SSH `cat`
// renders PNG, JPEG, and GIF images byte-for-byte intact.
func TestViewFileSSHONLCRLosslessAcrossChunkSplits(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			src.SetRGBA(x, y, color.RGBA{
				R: uint8((x*7 + y*11) % 256),
				G: uint8((x*13 + y*3) % 256),
				B: uint8((x*5 + y*17) % 256),
				A: 0xFF,
			})
		}
	}
	var pngBuf, jpgBuf, gifBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&jpgBuf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gifBuf, src, nil); err != nil {
		t.Fatal(err)
	}

	applyONLCR := func(in []byte) []byte {
		return bytes.ReplaceAll(in, []byte{0x0a}, []byte{0x0d, 0x0a})
	}

	cases := []struct {
		name   string
		orig   []byte
		decode func([]byte) error
	}{
		{"remote.png", pngBuf.Bytes(), func(b []byte) error { _, err := png.Decode(bytes.NewReader(b)); return err }},
		{"remote.jpg", jpgBuf.Bytes(), func(b []byte) error { _, err := jpeg.Decode(bytes.NewReader(b)); return err }},
		{"remote.gif", gifBuf.Bytes(), func(b []byte) error { _, err := gif.Decode(bytes.NewReader(b)); return err }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := lens.ByID("view.file")
			sink := l.(lens.ByteSink)
			// Cwd points to a nonexistent remote path so os.Stat cannot help.
			state, err := l.Open(model.Cmd{Program: "cat", Args: []string{tc.name}, Cwd: "/nonexistent/remote"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !sink.CaptureBytes(state) {
				t.Fatalf("CaptureBytes = false for %s", tc.name)
			}

			// Prepend shell title OSC + CRLF and feed in small 19-byte chunks so
			// both the OSC prefix and the magic signature split across chunks.
			wire := append([]byte("\x1b]2;cat "+tc.name+"\x07\r\n"), applyONLCR(tc.orig)...)
			for off := 0; off < len(wire); off += 19 {
				end := min(off+19, len(wire))
				if err := sink.Bytes(state, wire[off:end]); err != nil {
					t.Fatal(err)
				}
			}
			if err := l.Finish(state, 0); err != nil {
				t.Fatal(err)
			}
			node, err := l.View(state)
			if err != nil {
				t.Fatal(err)
			}
			imgNode, ok := node.(lens.Image)
			if !ok {
				t.Fatalf("View returned %T (%+v), want lens.Image", node, node)
			}
			if !bytes.Equal(imgNode.Data, tc.orig) {
				t.Fatalf("ONLCR-recovered %s mismatch: got %d bytes, want %d bytes", tc.name, len(imgNode.Data), len(tc.orig))
			}
			if err := tc.decode(imgNode.Data); err != nil {
				t.Fatalf("recovered %s failed to decode: %v", tc.name, err)
			}
		})
	}
}
