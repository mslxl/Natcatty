//go:build cgo && (darwin || linux || windows)

package lensview

/*
#cgo pkg-config: libjpeg

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <setjmp.h>
#include <jpeglib.h>

// A truncated JPEG is what a command prints while it is still running, and a
// terminal that streams should show the rows that could be decoded rather than
// nothing. libjpeg's own behaviour does that: the entropy decoder emits warning
// messages for the missing data and keeps returning the rows it could build, so
// reading scanlines until it stops gives a picture whose top is real and whose
// bottom is filled with the last good row. The default error manager calls
// exit() on a fatal error, so errors are longjmp'd back here instead.

typedef struct {
    struct jpeg_error_mgr pub;
    jmp_buf jump;
} nc_error_mgr;

static void nc_jpeg_error(j_common_ptr cinfo) {
    nc_error_mgr *e = (nc_error_mgr *)cinfo->err;
    longjmp(e->jump, 1);
}

static void nc_jpeg_message(j_common_ptr cinfo, int level) {
    (void)cinfo;
    (void)level;
}

// nc_jpeg_decode reads as much of data as libjpeg can, into a buffer it
// allocates for height*width*3 bytes and returns (the caller frees it). rows
// reports how many scanlines carried real data. A NULL return means the header
// itself could not be read — the data is not a JPEG, or too little of it has
// arrived to know the size.
static unsigned char *nc_jpeg_decode(unsigned char *data, unsigned long len,
                                     int *width, int *height, int *rows) {
    struct jpeg_decompress_struct cinfo;
    nc_error_mgr jerr;
    unsigned char * volatile rgb = NULL;
    cinfo.err = jpeg_std_error(&jerr.pub);
    jerr.pub.error_exit = nc_jpeg_error;
    jerr.pub.emit_message = nc_jpeg_message;
    *rows = 0;
    if (setjmp(jerr.jump)) {
        jpeg_destroy_decompress(&cinfo);
        free((void *)rgb);
        return NULL;
    }
    jpeg_create_decompress(&cinfo);
    jpeg_mem_src(&cinfo, data, len);
    jpeg_read_header(&cinfo, TRUE);
    cinfo.out_color_space = JCS_RGB;
    jpeg_start_decompress(&cinfo);
    *width = (int)cinfo.output_width;
    *height = (int)cinfo.output_height;
    if (*width <= 0 || *height <= 0) {
        jpeg_destroy_decompress(&cinfo);
        return NULL;
    }
    size_t stride = (size_t)(*width) * 3;
    rgb = calloc((size_t)(*height) * stride, 1);
    if (rgb == NULL) {
        jpeg_destroy_decompress(&cinfo);
        return NULL;
    }
    while (cinfo.output_scanline < cinfo.output_height) {
        JSAMPROW row = rgb + (size_t)cinfo.output_scanline * stride;
        if (jpeg_read_scanlines(&cinfo, &row, 1) == 0) {
            break;
        }
        (*rows)++;
    }
    jpeg_finish_decompress(&cinfo);
    jpeg_destroy_decompress(&cinfo);
    return (unsigned char *)rgb;
}
*/
import "C"

import (
	"image"
	"image/color"
	"unsafe"
)

// nativeJPEGAvailable says libjpeg is linked in, so a JPEG prefix can be drawn.
const nativeJPEGAvailable = true

// decodeJPEGTolerant draws as much of a JPEG as its bytes allow. A complete
// file gives the whole picture; a prefix gives the rows libjpeg could decode,
// with the rest left transparent, which is what a terminal shows while a `cat`
// is still streaming.
//
// ok is false when not even the size is known: the header has not arrived, or
// the data is not a JPEG.
func decodeJPEGTolerant(data []byte) (*image.RGBA, int, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, 0, false
	}
	cdata := C.CBytes(data)
	defer C.free(cdata)

	var width, height, rows C.int
	out := C.nc_jpeg_decode((*C.uchar)(cdata), C.ulong(len(data)), &width, &height, &rows)
	if out == nil || width <= 0 || height <= 0 {
		return nil, 0, false
	}
	defer C.free(unsafe.Pointer(out))
	img := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	pixels := C.GoBytes(unsafe.Pointer(out), C.int(int(height)*int(width)*3))
	for y := 0; y < int(rows); y++ {
		base := y * int(width) * 3
		for x := 0; x < int(width); x++ {
			i := base + x*3
			img.SetRGBA(x, y, color.RGBA{R: pixels[i], G: pixels[i+1], B: pixels[i+2], A: 0xFF})
		}
	}
	return img, int(rows), true
}
