package rfb

import (
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"sync"
)

const (
	// maxFramebufferPixels bounds a ServerInit allocation. 1<<26 pixels is
	// enough for an 8K desktop at 32 bits per pixel (about 268 MiB) while
	// still refusing a hostile 65535x65535 size.
	maxFramebufferPixels = 1 << 26
	// maxRRESubrectangles bounds the wire count before the decoder starts
	// looping, so a hostile count cannot spin for minutes.
	maxRRESubrectangles = 1 << 22
	// maxFieldLength bounds a name, reason or cut-text allocation.
	maxFieldLength = 1 << 20
	// maxCursorPixels bounds a CursorWithAlpha allocation. Cursors are tiny;
	// this still allows a large HiDPI shape while refusing a hostile one.
	maxCursorPixels = 1 << 20
	// maxZRLEPayload bounds one ZRLE rectangle's compressed length field, so a
	// hostile length cannot stream for an unbounded time.
	maxZRLEPayload = 1 << 28
	// zrleTileSize is the ZRLE tile edge, in pixels.
	zrleTileSize = 64
)

// Framebuffer is an owned 32-bit RGBA image plus the pixel format used to
// decode wire pixels into it. Every rectangle decoder normalises to this
// representation, whatever format the server negotiated.
//
// The bounds are fixed until an ExtendedDesktopSize rectangle resizes the
// framebuffer. One decoder goroutine writes pixels; callers on other
// goroutines may read Bounds, Image and PixelFormat concurrently with a
// resize. Image returns the current bitmap pointer, which is safe because
// decode writes only into the pointer it was handed and Resize installs a new
// allocation rather than mutating the old one.
type Framebuffer struct {
	// mu guards the img and pf fields. It is not held while pixels are
	// written: the decoder is the only writer, and Resize runs on the same
	// goroutine, so field replacement is the only cross-goroutine event.
	mu  sync.RWMutex
	img *image.RGBA
	pf  PixelFormat
	// zrle is the connection's persistent ZRLE zlib stream. ZRLE rectangles
	// share one stream, so the decoder keeps it across updates.
	zrle *zrleState
}

// NewFramebuffer allocates a width by height framebuffer decoded with pf.
// Empty or excessive sizes are rejected before allocating.
func NewFramebuffer(width, height int, pf PixelFormat) (*Framebuffer, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("%w: size %dx%d", ErrInvalidRectangle, width, height)
	}
	if int64(width)*int64(height) > maxFramebufferPixels {
		return nil, fmt.Errorf("%w: %dx%d", ErrFramebufferTooLarge, width, height)
	}
	if err := pf.Validate(); err != nil {
		return nil, err
	}
	return &Framebuffer{pf: pf, img: image.NewRGBA(image.Rect(0, 0, width, height))}, nil
}

// Image returns the current RGBA bitmap. The decoder writes into it in place,
// so a caller that needs an immutable snapshot must copy it. The pointer is
// stable until the next resize; reading the previous pointer after a resize is
// safe.
func (f *Framebuffer) Image() *image.RGBA {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.img
}

// Bounds returns the framebuffer rectangle. It changes when an
// ExtendedDesktopSize rectangle resizes the framebuffer; the lock makes that
// resize safe to observe from any goroutine.
func (f *Framebuffer) Bounds() image.Rectangle {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.img.Bounds()
}

// Resize reallocates the framebuffer for a new ExtendedDesktopSize. Pixels are
// cleared; the protocol requires a resized desktop to repaint. The pixel
// format and the ZRLE stream survive the resize.
func (f *Framebuffer) Resize(width, height int) error {
	if width <= 0 || height <= 0 {
		return fmt.Errorf("%w: size %dx%d", ErrInvalidRectangle, width, height)
	}
	if int64(width)*int64(height) > maxFramebufferPixels {
		return fmt.Errorf("%w: %dx%d", ErrFramebufferTooLarge, width, height)
	}
	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	f.mu.Lock()
	f.img = resized
	f.mu.Unlock()
	return nil
}

// PixelFormat returns the format currently used to decode wire pixels.
func (f *Framebuffer) PixelFormat() PixelFormat {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.pf
}

// SetPixelFormat changes the wire format, as after a client SetPixelFormat.
// It must not run concurrently with a decode; the caller serialises it with
// ReadMessage. Other goroutines may read PixelFormat at any time.
func (f *Framebuffer) SetPixelFormat(pf PixelFormat) error {
	if err := pf.Validate(); err != nil {
		return err
	}
	f.mu.Lock()
	f.pf = pf
	f.mu.Unlock()
	return nil
}

// ReadRectangles reads count rectangle headers and their payloads, compositing
// each in order. A CopyRect may therefore refer to pixels delivered earlier in
// the same update.
func (f *Framebuffer) ReadRectangles(r io.Reader, count int) ([]Rectangle, error) {
	if count < 0 || count > 1<<16 {
		return nil, fmt.Errorf("%w: %d rectangles", ErrMalformed, count)
	}
	rectangles := make([]Rectangle, 0, count)
	for index := 0; index < count; index++ {
		var wire [12]byte
		if _, err := io.ReadFull(r, wire[:]); err != nil {
			return nil, fmt.Errorf("rfb: rectangle %d header: %w", index, err)
		}
		x := binary.BigEndian.Uint16(wire[0:2])
		y := binary.BigEndian.Uint16(wire[2:4])
		width := binary.BigEndian.Uint16(wire[4:6])
		height := binary.BigEndian.Uint16(wire[6:8])
		encoding := Encoding(int32(binary.BigEndian.Uint32(wire[8:12])))

		// Metadata pseudo-rectangles do not name a framebuffer region: their
		// header fields have different meanings, so they must not pass through
		// rectWithin, which would reject a status code or cursor hotspot.
		if encoding == EncodingExtendedDesktopSize {
			extended, err := f.readExtendedDesktopSize(r, x, y, width, height)
			if err != nil {
				return nil, fmt.Errorf("rfb: rectangle %d (ExtendedDesktopSize): %w", index, err)
			}
			rectangles = append(rectangles, Rectangle{
				X: x, Y: y, Width: width, Height: height,
				Encoding: encoding, Extended: extended,
			})
			continue
		}
		if encoding == EncodingCursorWithAlpha {
			cursor, err := f.readCursorWithAlpha(r, x, y, width, height)
			if err != nil {
				return nil, fmt.Errorf("rfb: rectangle %d (CursorWithAlpha): %w", index, err)
			}
			rectangles = append(rectangles, Rectangle{
				X: x, Y: y, Width: width, Height: height,
				Encoding: encoding, Cursor: cursor,
			})
			continue
		}
		if encoding == EncodingFence || encoding == EncodingContinuousUpdates || encoding == EncodingExtendedKeyEvent {
			// A server may acknowledge an extension with an empty pseudo
			// rectangle. It carries no payload. QEMU's ExtendedKeyEvent
			// acknowledgement names the whole server surface as its bounds,
			// so the dimensions are informational and must not be decoded as
			// pixels.
			rectangles = append(rectangles, Rectangle{
				X: x, Y: y, Width: width, Height: height, Encoding: encoding,
			})
			continue
		}

		rect, err := f.rectWithin(x, y, width, height)
		if err != nil {
			return nil, fmt.Errorf("rfb: rectangle %d: %w", index, err)
		}
		switch encoding {
		case EncodingRaw:
			err = f.decodeRaw(r, rect)
		case EncodingCopyRect:
			err = f.decodeCopyRect(r, rect)
		case EncodingRRE:
			err = f.decodeRRE(r, rect)
		case EncodingHextile:
			err = f.decodeHextile(r, rect)
		case EncodingZRLE:
			err = f.decodeZRLE(r, rect)
		default:
			err = fmt.Errorf("%w: %d", ErrUnsupportedEncoding, encoding)
		}
		if err != nil {
			return nil, fmt.Errorf("rfb: rectangle %d (encoding %d): %w", index, encoding, err)
		}
		rectangles = append(rectangles, Rectangle{
			X: x, Y: y, Width: width, Height: height, Encoding: encoding,
		})
	}
	return rectangles, nil
}

// readExtendedDesktopSize parses one ExtendedDesktopSize pseudo-rectangle. The
// rectangle's dimensions are the new desktop size and its x/y header fields
// are the change reason and status, so a successful result resizes the
// framebuffer before any following rectangles are decoded.
func (f *Framebuffer) readExtendedDesktopSize(r io.Reader, reason, status, width, height uint16) (*ExtendedDesktopSize, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	count := int(header[0])
	payload := make([]byte, count*16)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	screens := make([]Screen, count)
	for i := 0; i < count; i++ {
		wire := payload[i*16:]
		screens[i] = Screen{
			ID:     binary.BigEndian.Uint32(wire[0:4]),
			X:      binary.BigEndian.Uint16(wire[4:6]),
			Y:      binary.BigEndian.Uint16(wire[6:8]),
			Width:  binary.BigEndian.Uint16(wire[8:10]),
			Height: binary.BigEndian.Uint16(wire[10:12]),
			Flags:  binary.BigEndian.Uint32(wire[12:16]),
		}
	}
	extended := &ExtendedDesktopSize{
		Reason:  uint8(reason),
		Status:  uint8(status),
		Width:   width,
		Height:  height,
		Screens: screens,
	}
	// A failure result leaves the dimensions undefined; only a success is a
	// real resize. "Request forwarded" (4) is not final either.
	if status == 0 && width > 0 && height > 0 {
		if err := f.Resize(int(width), int(height)); err != nil {
			return nil, err
		}
	}
	return extended, nil
}

// readCursorWithAlpha parses the CursorWithAlpha payload. The rectangle's x
// and y are the hotspot and its width and height the shape's dimensions. A
// zero dimension is a hidden cursor. Only a raw bitmap is accepted: an
// unexpected sub-encoding fails the connection before the stream desynchronises.
func (f *Framebuffer) readCursorWithAlpha(r io.Reader, hotspotX, hotspotY, width, height uint16) (*Cursor, error) {
	var encodingWire [4]byte
	if _, err := io.ReadFull(r, encodingWire[:]); err != nil {
		return nil, err
	}
	encoding := Encoding(int32(binary.BigEndian.Uint32(encodingWire[:])))
	if width == 0 || height == 0 {
		return &Cursor{}, nil
	}
	if int64(width)*int64(height) > maxCursorPixels {
		return nil, fmt.Errorf("%w: cursor is %dx%d", ErrOversize, width, height)
	}
	if encoding != EncodingRaw {
		return nil, fmt.Errorf("%w: cursor encoding %d", ErrUnsupportedEncoding, encoding)
	}
	pixels := make([]byte, int(width)*int(height)*4)
	if _, err := io.ReadFull(r, pixels); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	for i := 0; i < int(width)*int(height); i++ {
		// The wire alpha is pre-multiplied, which is exactly image.RGBA's
		// representation, so the bytes are copied through unchanged.
		img.SetRGBA(i%int(width), i/int(width), color.RGBA{
			R: pixels[i*4],
			G: pixels[i*4+1],
			B: pixels[i*4+2],
			A: pixels[i*4+3],
		})
	}
	return &Cursor{
		Image:   img,
		Hotspot: image.Pt(int(hotspotX), int(hotspotY)),
	}, nil
}

func (f *Framebuffer) rectWithin(x, y, width, height uint16) (image.Rectangle, error) {
	if width == 0 || height == 0 {
		return image.Rectangle{}, fmt.Errorf("%w: zero size %dx%d", ErrInvalidRectangle, width, height)
	}
	if int(x)+int(width) > f.img.Bounds().Dx() || int(y)+int(height) > f.img.Bounds().Dy() {
		return image.Rectangle{}, fmt.Errorf("%w: %d,%d %dx%d outside %v",
			ErrInvalidRectangle, x, y, width, height, f.img.Bounds())
	}
	return image.Rect(int(x), int(y), int(x)+int(width), int(y)+int(height)), nil
}

func (f *Framebuffer) readPixel(r io.Reader) (color.RGBA, error) {
	var wire [4]byte
	bpp := f.pf.BytesPerPixel()
	if bpp < 1 || bpp > len(wire) {
		return color.RGBA{}, fmt.Errorf("%w: %d bytes per pixel", ErrInvalidPixelFormat, bpp)
	}
	if _, err := io.ReadFull(r, wire[:bpp]); err != nil {
		return color.RGBA{}, err
	}
	return f.pf.decodePixel(wire[:bpp]), nil
}

func (f *Framebuffer) decodeRaw(r io.Reader, rect image.Rectangle) error {
	bpp := f.pf.BytesPerPixel()
	if bpp < 1 || bpp > 4 {
		return fmt.Errorf("%w: %d bytes per pixel", ErrInvalidPixelFormat, bpp)
	}
	row := make([]byte, rect.Dx()*bpp)
	for y := 0; y < rect.Dy(); y++ {
		if _, err := io.ReadFull(r, row); err != nil {
			return err
		}
		for x := 0; x < rect.Dx(); x++ {
			f.img.SetRGBA(rect.Min.X+x, rect.Min.Y+y, f.pf.decodePixel(row[x*bpp:(x+1)*bpp]))
		}
	}
	return nil
}

func (f *Framebuffer) decodeCopyRect(r io.Reader, rect image.Rectangle) error {
	var wire [4]byte
	if _, err := io.ReadFull(r, wire[:]); err != nil {
		return err
	}
	srcX := int(binary.BigEndian.Uint16(wire[0:2]))
	srcY := int(binary.BigEndian.Uint16(wire[2:4]))
	if srcX+rect.Dx() > f.img.Bounds().Dx() || srcY+rect.Dy() > f.img.Bounds().Dy() {
		return fmt.Errorf("%w: source %d,%d %dx%d outside %v",
			ErrInvalidRectangle, srcX, srcY, rect.Dx(), rect.Dy(), f.img.Bounds())
	}

	stride := f.img.Stride
	width := rect.Dx() * 4
	copyRow := func(dstY, srcY int) {
		dst := f.img.Pix[dstY*stride+rect.Min.X*4 : dstY*stride+rect.Min.X*4+width]
		src := f.img.Pix[srcY*stride+srcX*4 : srcY*stride+srcX*4+width]
		copy(dst, src)
	}
	if rect.Min.Y > srcY {
		// Moving down: copy bottom-up so an overlapping source row is read
		// before it is overwritten.
		for row := rect.Dy() - 1; row >= 0; row-- {
			copyRow(rect.Min.Y+row, srcY+row)
		}
	} else {
		for row := 0; row < rect.Dy(); row++ {
			copyRow(rect.Min.Y+row, srcY+row)
		}
	}
	return nil
}

func (f *Framebuffer) decodeRRE(r io.Reader, rect image.Rectangle) error {
	var wire [4]byte
	if _, err := io.ReadFull(r, wire[:]); err != nil {
		return err
	}
	count := int32(binary.BigEndian.Uint32(wire[:]))
	if count < 0 || count > maxRRESubrectangles {
		return fmt.Errorf("%w: %d RRE subrectangles", ErrMalformed, count)
	}
	background, err := f.readPixel(r)
	if err != nil {
		return err
	}
	f.fillRect(rect, background)
	for i := int32(0); i < count; i++ {
		sub, err := f.readPixel(r)
		if err != nil {
			return err
		}
		var geometry [8]byte
		if _, err := io.ReadFull(r, geometry[:]); err != nil {
			return err
		}
		sx := int(binary.BigEndian.Uint16(geometry[0:2]))
		sy := int(binary.BigEndian.Uint16(geometry[2:4]))
		sw := int(binary.BigEndian.Uint16(geometry[4:6]))
		sh := int(binary.BigEndian.Uint16(geometry[6:8]))
		if sw == 0 || sh == 0 || sx+sw > rect.Dx() || sy+sh > rect.Dy() {
			return fmt.Errorf("%w: RRE subrect %d,%d %dx%d outside %dx%d",
				ErrInvalidRectangle, sx, sy, sw, sh, rect.Dx(), rect.Dy())
		}
		f.fillRect(image.Rect(rect.Min.X+sx, rect.Min.Y+sy, rect.Min.X+sx+sw, rect.Min.Y+sy+sh), sub)
	}
	return nil
}

const (
	hextileRaw                 = 1 << 0
	hextileBackgroundSpecified = 1 << 1
	hextileForegroundSpecified = 1 << 2
	hextileAnySubrects         = 1 << 3
	hextileSubrectsColoured    = 1 << 4
	hextileTileSize            = 16
)

func (f *Framebuffer) decodeHextile(r io.Reader, rect image.Rectangle) error {
	var (
		background color.RGBA
		foreground color.RGBA
		haveBG     bool
		haveFG     bool
	)
	for tileY := 0; tileY < rect.Dy(); tileY += hextileTileSize {
		tileHeight := min(hextileTileSize, rect.Dy()-tileY)
		for tileX := 0; tileX < rect.Dx(); tileX += hextileTileSize {
			tileWidth := min(hextileTileSize, rect.Dx()-tileX)
			tile := image.Rect(
				rect.Min.X+tileX, rect.Min.Y+tileY,
				rect.Min.X+tileX+tileWidth, rect.Min.Y+tileY+tileHeight,
			)

			var subencoding [1]byte
			if _, err := io.ReadFull(r, subencoding[:]); err != nil {
				return err
			}
			if subencoding[0]&hextileRaw != 0 {
				if err := f.decodeRaw(r, tile); err != nil {
					return err
				}
				continue
			}

			if subencoding[0]&hextileBackgroundSpecified != 0 {
				var err error
				background, err = f.readPixel(r)
				if err != nil {
					return err
				}
				haveBG = true
			}
			if !haveBG {
				return fmt.Errorf("%w: hextile tile without a background", ErrMalformed)
			}
			if subencoding[0]&hextileForegroundSpecified != 0 {
				var err error
				foreground, err = f.readPixel(r)
				if err != nil {
					return err
				}
				haveFG = true
			}
			f.fillRect(tile, background)

			if subencoding[0]&hextileAnySubrects == 0 {
				continue
			}
			var count [1]byte
			if _, err := io.ReadFull(r, count[:]); err != nil {
				return err
			}
			for i := 0; i < int(count[0]); i++ {
				sub := foreground
				if subencoding[0]&hextileSubrectsColoured != 0 {
					var err error
					sub, err = f.readPixel(r)
					if err != nil {
						return err
					}
				} else if !haveFG {
					return fmt.Errorf("%w: hextile subrect without a foreground", ErrMalformed)
				}
				var geometry [2]byte
				if _, err := io.ReadFull(r, geometry[:]); err != nil {
					return err
				}
				sx := int(geometry[0] >> 4)
				sy := int(geometry[0] & 0x0f)
				sw := int(geometry[1]>>4) + 1
				sh := int(geometry[1]&0x0f) + 1
				if sx+sw > tileWidth || sy+sh > tileHeight {
					return fmt.Errorf("%w: hextile subrect %d,%d %dx%d outside %dx%d",
						ErrInvalidRectangle, sx, sy, sw, sh, tileWidth, tileHeight)
				}
				f.fillRect(image.Rect(
					tile.Min.X+sx, tile.Min.Y+sy,
					tile.Min.X+sx+sw, tile.Min.Y+sy+sh,
				), sub)
			}
		}
	}
	return nil
}

func (f *Framebuffer) fillRect(rect image.Rectangle, c color.RGBA) {
	rect = rect.Intersect(f.img.Bounds())
	if rect.Empty() {
		return
	}
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			f.img.SetRGBA(x, y, c)
		}
	}
}

// ZRLE subencoding values. The top bit marks run-length encoding; the bottom
// seven bits give the palette size.
const (
	zrleRaw           = 0
	zrleSolid         = 1
	zrlePackedMax     = 16
	zrlePlainRLE      = 128
	zrlePaletteRLEMin = 130
)

// zrleState is the persistent zlib stream one RFB connection shares between
// ZRLE rectangles.
type zrleState struct {
	chunks *zrleChunkReader
	reader io.ReadCloser
}

func (f *Framebuffer) zlibReader(source io.Reader) (io.Reader, error) {
	if f.zrle == nil {
		chunks := &zrleChunkReader{source: source}
		reader, err := zlib.NewReader(chunks)
		if err != nil {
			return nil, err
		}
		f.zrle = &zrleState{chunks: chunks, reader: reader}
	}
	return f.zrle.reader, nil
}

// zrleChunkReader exposes the connection as an endless zlib stream by reading
// the 4-byte length prefix that begins each ZRLE rectangle. It never reads past
// the current rectangle's payload, so the persistent zlib decoder cannot
// consume bytes belonging to a later rectangle.
type zrleChunkReader struct {
	source io.Reader
	remain int
}

func (z *zrleChunkReader) Read(p []byte) (int, error) {
	for z.remain == 0 {
		var header [4]byte
		if _, err := io.ReadFull(z.source, header[:]); err != nil {
			return 0, err
		}
		length := binary.BigEndian.Uint32(header[:])
		if length > maxZRLEPayload {
			return 0, fmt.Errorf("%w: ZRLE payload is %d bytes", ErrOversize, length)
		}
		z.remain = int(length)
		if z.remain == 0 {
			continue
		}
	}
	if len(p) > z.remain {
		p = p[:z.remain]
	}
	n, err := z.source.Read(p)
	z.remain -= n
	return n, err
}

func (f *Framebuffer) decodeZRLE(r io.Reader, rect image.Rectangle) error {
	stream, err := f.zlibReader(r)
	if err != nil {
		return err
	}
	cpixel := f.pf.cpixelBytes()
	for tileY := 0; tileY < rect.Dy(); tileY += zrleTileSize {
		tileHeight := min(zrleTileSize, rect.Dy()-tileY)
		for tileX := 0; tileX < rect.Dx(); tileX += zrleTileSize {
			tileWidth := min(zrleTileSize, rect.Dx()-tileX)
			tile := image.Rect(
				rect.Min.X+tileX, rect.Min.Y+tileY,
				rect.Min.X+tileX+tileWidth, rect.Min.Y+tileY+tileHeight,
			)
			if err := f.decodeZRLETile(stream, tile, cpixel); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *Framebuffer) readCPixel(r io.Reader, size int) (color.RGBA, error) {
	var wire [4]byte
	if size < 1 || size > len(wire) {
		return color.RGBA{}, fmt.Errorf("%w: CPIXEL is %d bytes", ErrInvalidPixelFormat, size)
	}
	if _, err := io.ReadFull(r, wire[:size]); err != nil {
		return color.RGBA{}, err
	}
	return f.pf.decodeValue(f.pf.cpixelValue(wire[:size])), nil
}

// setTilePixel writes one row-major pixel within a ZRLE tile.
func (f *Framebuffer) setTilePixel(tile image.Rectangle, width, index int, c color.RGBA) {
	f.img.SetRGBA(tile.Min.X+index%width, tile.Min.Y+index/width, c)
}

// readZRLELength decodes the byte sequence that follows a ZRLE run's pixel:
// the run length minus one is the sum of the bytes, where 255 continues. limit
// bounds the loop, so a decompression bomb cannot make the decoder read forever
// for a run that could never fit the tile.
func readZRLELength(r io.Reader, limit int) (int, error) {
	length := 0
	for {
		var b [1]byte
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return 0, err
		}
		length += int(b[0])
		if length >= limit {
			return 0, fmt.Errorf("%w: ZRLE run exceeds %d pixels", ErrInvalidRectangle, limit)
		}
		if b[0] != 255 {
			break
		}
	}
	return length + 1, nil
}

func (f *Framebuffer) fillTileRun(tile image.Rectangle, width, start, run int, c color.RGBA) error {
	area := tile.Dx() * tile.Dy()
	if start < 0 || run < 1 || start+run > area {
		return fmt.Errorf("%w: ZRLE run %d+%d outside tile %dx%d",
			ErrInvalidRectangle, start, run, tile.Dx(), tile.Dy())
	}
	for i := 0; i < run; i++ {
		f.setTilePixel(tile, width, start+i, c)
	}
	return nil
}

func (f *Framebuffer) decodeZRLETile(r io.Reader, tile image.Rectangle, cpixel int) error {
	var subencoding [1]byte
	if _, err := io.ReadFull(r, subencoding[:]); err != nil {
		return err
	}
	value := subencoding[0]
	width, height := tile.Dx(), tile.Dy()
	area := width * height

	switch {
	case value == zrleRaw:
		raw := make([]byte, area*cpixel)
		if _, err := io.ReadFull(r, raw); err != nil {
			return err
		}
		for i := 0; i < area; i++ {
			f.setTilePixel(tile, width, i, f.pf.decodeValue(f.pf.cpixelValue(raw[i*cpixel:(i+1)*cpixel])))
		}
		return nil

	case value == zrleSolid:
		pixel, err := f.readCPixel(r, cpixel)
		if err != nil {
			return err
		}
		f.fillRect(tile, pixel)
		return nil

	case value >= 2 && value <= zrlePackedMax:
		paletteSize := int(value)
		palette := make([]color.RGBA, paletteSize)
		for i := range palette {
			pixel, err := f.readCPixel(r, cpixel)
			if err != nil {
				return err
			}
			palette[i] = pixel
		}
		bitsPerPixel := 4
		if paletteSize == 2 {
			bitsPerPixel = 1
		} else if paletteSize <= 4 {
			bitsPerPixel = 2
		}
		rowBytes := (width*bitsPerPixel + 7) / 8
		packed := make([]byte, rowBytes*height)
		if _, err := io.ReadFull(r, packed); err != nil {
			return err
		}
		mask := byte((1 << bitsPerPixel) - 1)
		for y := 0; y < height; y++ {
			row := packed[y*rowBytes:]
			for x := 0; x < width; x++ {
				bit := x * bitsPerPixel
				shift := 8 - bitsPerPixel - bit%8
				index := int((row[bit/8] >> shift) & mask)
				if index >= paletteSize {
					return fmt.Errorf("%w: ZRLE palette index %d of %d", ErrMalformed, index, paletteSize)
				}
				f.setTilePixel(tile, width, y*width+x, palette[index])
			}
		}
		return nil

	case value == zrlePlainRLE:
		index := 0
		for index < area {
			pixel, err := f.readCPixel(r, cpixel)
			if err != nil {
				return err
			}
			run, err := readZRLELength(r, area)
			if err != nil {
				return err
			}
			if err := f.fillTileRun(tile, width, index, run, pixel); err != nil {
				return err
			}
			index += run
		}
		return nil

	case value >= zrlePaletteRLEMin:
		paletteSize := int(value) - 128
		palette := make([]color.RGBA, paletteSize)
		for i := range palette {
			pixel, err := f.readCPixel(r, cpixel)
			if err != nil {
				return err
			}
			palette[i] = pixel
		}
		index := 0
		for index < area {
			var indexWire [1]byte
			if _, err := io.ReadFull(r, indexWire[:]); err != nil {
				return err
			}
			run := 1
			paletteIndex := int(indexWire[0])
			if paletteIndex&0x80 != 0 {
				paletteIndex &= 0x7f
				var err error
				run, err = readZRLELength(r, area)
				if err != nil {
					return err
				}
			}
			if paletteIndex >= paletteSize {
				return fmt.Errorf("%w: ZRLE palette index %d of %d", ErrMalformed, paletteIndex, paletteSize)
			}
			if err := f.fillTileRun(tile, width, index, run, palette[paletteIndex]); err != nil {
				return err
			}
			index += run
		}
		return nil

	default:
		return fmt.Errorf("%w: ZRLE subencoding %d", ErrMalformed, value)
	}
}
