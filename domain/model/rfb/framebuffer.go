package rfb

import (
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
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
)

// Framebuffer is an owned 32-bit RGBA image plus the pixel format used to
// decode wire pixels into it. Every rectangle decoder normalises to this
// representation, whatever format the server negotiated.
type Framebuffer struct {
	img *image.RGBA
	pf  PixelFormat
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

// Image returns the owned RGBA bitmap. The decoder writes into it in place, so
// a caller that needs an immutable snapshot must copy it.
func (f *Framebuffer) Image() *image.RGBA { return f.img }

// Bounds returns the framebuffer rectangle. The rectangle is fixed for the
// framebuffer's lifetime, so it may be read concurrently with decoding.
func (f *Framebuffer) Bounds() image.Rectangle { return f.img.Bounds() }

// PixelFormat returns the format currently used to decode wire pixels.
func (f *Framebuffer) PixelFormat() PixelFormat { return f.pf }

// SetPixelFormat changes the wire format, as after a client SetPixelFormat.
func (f *Framebuffer) SetPixelFormat(pf PixelFormat) error {
	if err := pf.Validate(); err != nil {
		return err
	}
	f.pf = pf
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
