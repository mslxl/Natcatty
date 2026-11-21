package rfb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"
)

func extendedDesktopSizeRectangle(reason, status, width, height uint16, screens []Screen) []byte {
	data := rectHeader(reason, status, width, height, EncodingExtendedDesktopSize)
	payload := make([]byte, 4+len(screens)*16)
	payload[0] = byte(len(screens))
	for i, screen := range screens {
		offset := 4 + i*16
		binary.BigEndian.PutUint32(payload[offset:offset+4], screen.ID)
		binary.BigEndian.PutUint16(payload[offset+4:offset+6], screen.X)
		binary.BigEndian.PutUint16(payload[offset+6:offset+8], screen.Y)
		binary.BigEndian.PutUint16(payload[offset+8:offset+10], screen.Width)
		binary.BigEndian.PutUint16(payload[offset+10:offset+12], screen.Height)
		binary.BigEndian.PutUint32(payload[offset+12:offset+16], screen.Flags)
	}
	return append(data, payload...)
}

func TestDecodeExtendedDesktopSizeResizesTheFramebuffer(t *testing.T) {
	frame := newFrame(t, 4, 3, DefaultPixelFormat())
	data := extendedDesktopSizeRectangle(0, 0, 8, 6, []Screen{
		{ID: 7, Width: 8, Height: 6},
	})
	rectangles, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if err != nil {
		t.Fatalf("ReadRectangles: %v", err)
	}
	if len(rectangles) != 1 || rectangles[0].Extended == nil {
		t.Fatalf("rectangles = %+v", rectangles)
	}
	extended := rectangles[0].Extended
	if extended.Reason != 0 || extended.Status != 0 || extended.Width != 8 || extended.Height != 6 {
		t.Fatalf("extended = %+v", extended)
	}
	if len(extended.Screens) != 1 || extended.Screens[0].ID != 7 {
		t.Fatalf("screens = %+v", extended.Screens)
	}
	if got := frame.Bounds(); got != image.Rect(0, 0, 8, 6) {
		t.Fatalf("framebuffer bounds = %v, want 8x6", got)
	}
}

func TestDecodeExtendedDesktopSizeFailureKeepsTheSize(t *testing.T) {
	frame := newFrame(t, 4, 3, DefaultPixelFormat())
	data := extendedDesktopSizeRectangle(1, 1, 0, 0, nil)
	rectangles, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if err != nil {
		t.Fatalf("ReadRectangles: %v", err)
	}
	if got := rectangles[0].Extended; got == nil || got.Status != 1 || got.Reason != 1 {
		t.Fatalf("extended = %+v", got)
	}
	if got := frame.Bounds(); got != image.Rect(0, 0, 4, 3) {
		t.Fatalf("framebuffer bounds = %v, want the original 4x3", got)
	}
}

func cursorWithAlphaRectangle(hotspot image.Point, width, height uint16, encoding Encoding, pixels []byte) []byte {
	data := rectHeader(
		uint16(hotspot.X), uint16(hotspot.Y),
		width, height, EncodingCursorWithAlpha,
	)
	wire := make([]byte, 4)
	binary.BigEndian.PutUint32(wire, uint32(encoding))
	return append(append(data, wire...), pixels...)
}

func TestDecodeCursorWithAlpha(t *testing.T) {
	frame := newFrame(t, 2, 2, DefaultPixelFormat())
	// A red/green 2x1 cursor; the red channel is pre-multiplied by alpha 128.
	pixels := []byte{
		100, 0, 0, 128,
		0, 255, 0, 255,
	}
	data := cursorWithAlphaRectangle(image.Pt(1, 0), 2, 1, EncodingRaw, pixels)
	rectangles, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if err != nil {
		t.Fatalf("ReadRectangles: %v", err)
	}
	cursor := rectangles[0].Cursor
	if cursor == nil || cursor.Image == nil {
		t.Fatalf("cursor = %+v", cursor)
	}
	if cursor.Hotspot != image.Pt(1, 0) {
		t.Fatalf("hotspot = %v, want (1,0)", cursor.Hotspot)
	}
	if got := cursor.Image.RGBAAt(0, 0); got != (color.RGBA{R: 100, A: 128}) {
		t.Fatalf("premultiplied pixel = %+v, want the wire red", got)
	}
	if got := cursor.Image.RGBAAt(1, 0); got != green {
		t.Fatalf("opaque pixel = %+v, want green", got)
	}
	// The framebuffer is untouched by the cursor pseudo-rectangle.
	if got := frame.Image().RGBAAt(0, 0); got != (color.RGBA{}) {
		t.Fatalf("cursor changed the framebuffer pixel to %+v", got)
	}
}

func TestDecodeCursorWithAlphaHidden(t *testing.T) {
	frame := newFrame(t, 2, 2, DefaultPixelFormat())
	data := cursorWithAlphaRectangle(image.Pt(0, 0), 0, 0, EncodingRaw, nil)
	rectangles, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if err != nil {
		t.Fatalf("ReadRectangles: %v", err)
	}
	if cursor := rectangles[0].Cursor; cursor == nil || cursor.Image != nil {
		t.Fatalf("hidden cursor = %+v, want a nil image", cursor)
	}
}

func TestDecodeCursorWithAlphaRejectsUnsupportedEncoding(t *testing.T) {
	frame := newFrame(t, 2, 2, DefaultPixelFormat())
	data := cursorWithAlphaRectangle(image.Pt(0, 0), 1, 1, EncodingZRLE, nil)
	_, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("ReadRectangles = %v, want ErrUnsupportedEncoding", err)
	}
}

func TestDecodeUnexpectedEncodingLeavesTheFrameAlone(t *testing.T) {
	frame := newFrame(t, 2, 2, DefaultPixelFormat())
	frame.Image().SetRGBA(0, 0, red)
	before := append([]byte(nil), frame.Image().Pix...)
	data := rectHeader(0, 0, 1, 1, Encoding(9999))
	_, err := frame.ReadRectangles(bytes.NewReader(data), 1)
	if !errors.Is(err, ErrUnsupportedEncoding) {
		t.Fatalf("ReadRectangles = %v, want ErrUnsupportedEncoding", err)
	}
	if !bytes.Equal(frame.Image().Pix, before) {
		t.Fatal("an unexpected encoding corrupted the framebuffer")
	}
}
