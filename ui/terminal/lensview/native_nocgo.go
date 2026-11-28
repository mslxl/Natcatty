//go:build !cgo || !(darwin || linux || windows)

package lensview

// A build without cgo (or one for a platform the native decoders do not cover)
// cannot call libjpeg or libwebp. PNG and BMP still stream — their decoders are
// pure Go — while JPEG and WebP fall back to the full decoder once the file is
// whole, showing their progress in between.

import "image"

const (
	// nativeJPEGAvailable and nativeWebPAvailable say whether the native
	// decoders are linked in. This build has neither.
	nativeJPEGAvailable = false
	nativeWebPAvailable = false
)

// decodeJPEGTolerant reports that this build cannot decode a JPEG prefix.
func decodeJPEGTolerant(data []byte) (*image.RGBA, int, bool) { return nil, 0, false }

// webpDecoder is the stand-in for a build without libwebp: it never decodes
// anything, so the block waits for the whole file.
type webpDecoder struct{ bad bool }

// newWebPDecoder makes the stand-in.
func newWebPDecoder() webpDecoder { return webpDecoder{} }

func (w *webpDecoder) close() {}

func (w *webpDecoder) decode(data []byte) (*image.RGBA, int, bool) { return nil, 0, false }
