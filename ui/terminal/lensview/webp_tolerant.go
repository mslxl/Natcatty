//go:build cgo && (darwin || linux || windows)

package lensview

/*
#cgo pkg-config: libwebp

#include <stdlib.h>
#include <string.h>
#include <webp/decode.h>

// libwebp has a real incremental decoder: WebPIAppend takes the bytes received
// so far and WebPIDecGetRGB reports the rows decoded from them. A truncated
// WebP therefore yields the picture's top, exactly like PNG does — the stream
// format itself is progressive, unlike JPEG's.

static WebPIDecoder *nc_webp_new(void) {
    return WebPINewRGB(MODE_RGBA, NULL, 0, 0);
}

// nc_webp_append feeds the bytes received so far and returns the decoder's
// status. It copies the data into the decoder, so the caller's buffer may go
// away after the call.
static int nc_webp_append(WebPIDecoder *dec, unsigned char *data, unsigned long len) {
    return (int)WebPIAppend(dec, data, (size_t)len);
}

// nc_webp_rows reports the rows decoded so far, or -1 when none are ready. The
// pointer it returns is the decoder's own RGBA buffer and stays valid until the
// next append or the delete.
static unsigned char *nc_webp_rows(WebPIDecoder *dec, int *width, int *height, int *rows, int *stride) {
    int last_y = 0;
    uint8_t *rgba = WebPIDecGetRGB(dec, &last_y, width, height, stride);
    if (rgba == NULL || last_y <= 0) {
        *rows = 0;
        return NULL;
    }
    *rows = last_y;
    return rgba;
}

static void nc_webp_free(WebPIDecoder *dec) {
    WebPIDelete(dec);
}
*/
import "C"

import (
	"image"
	"unsafe"
)

// nativeWebPAvailable says libwebp is linked in, so a WebP prefix can be drawn.
const nativeWebPAvailable = true

// webpDecoder is one WebP's incremental decode state, kept across frames: the
// decoder is told only the bytes that are new, so no byte is decoded twice.
type webpDecoder struct {
	dec *C.WebPIDecoder
	fed int
	bad bool
}

func newWebPDecoder() webpDecoder {
	dec := C.nc_webp_new()
	if dec == nil {
		return webpDecoder{bad: true}
	}
	return webpDecoder{dec: dec}
}

// close releases the decoder's memory.
func (w *webpDecoder) close() {
	if w.dec != nil {
		C.nc_webp_free(w.dec)
		w.dec = nil
	}
}

// decode feeds the bytes that arrived since the last call and returns the rows
// decoded so far. A complete file gives the whole picture.
func (w *webpDecoder) decode(data []byte) (*image.RGBA, int, bool) {
	if w.bad || w.dec == nil || len(data) <= w.fed {
		return nil, 0, false
	}
	chunk := data[w.fed:]
	w.fed = len(data)
	cdata := C.CBytes(chunk)
	defer C.free(cdata)
	status := C.nc_webp_append(w.dec, (*C.uchar)(cdata), C.ulong(len(chunk)))
	// VP8_STATUS_SUSPENDED (5) means "more data needed", which is the normal
	// state while a command is still printing.
	if status != 0 && status != 5 {
		w.bad = true
		return nil, 0, false
	}
	var width, height, rows, stride C.int
	rgba := C.nc_webp_rows(w.dec, &width, &height, &rows, &stride)
	if rgba == nil || width <= 0 || height <= 0 || rows <= 0 {
		return nil, 0, false
	}
	wInt, hInt, rInt, sInt := int(width), int(height), int(rows), int(stride)
	if sInt < wInt*4 {
		sInt = wInt * 4
	}
	if rInt > hInt {
		rInt = hInt
	}
	img := image.NewRGBA(image.Rect(0, 0, wInt, hInt))
	pixels := C.GoBytes(unsafe.Pointer(rgba), C.int(rInt*sInt))
	rowBytes := wInt * 4
	for y := 0; y < rInt; y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+rowBytes], pixels[y*sInt:y*sInt+rowBytes])
	}
	return img, rInt, true
}
