// Package lensview draws the lens view trees the engine builds.
package lensview

// A picture's bytes may still be arriving when its block is drawn. The standard
// decoders refuse a prefix ("not enough pixel data"), which left the block
// saying "Image (cannot decode)" until the very last byte and then made the
// whole picture appear at once.
//
// So a growing picture is decoded incrementally here. PNG and BMP stream the
// way their data is laid out — both store the picture row by row — so a prefix
// is the top of the picture and the rows fill in as the command prints. JPEG
// and GIF cannot: JPEG interleaves its coefficients across the whole frame and
// its scan is entropy-coded end to end, and GIF's image data is a LZW stream
// whose rows only become usable as the code table grows; a prefix of either is
// not a scaled-down picture but unusable data. Those two show when the file is
// whole, and the block says so while it waits.

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image"
	"image/color"

	"github.com/egoist/mygo/ui"
)

// pngHeader is the signature every PNG starts with.
var pngHeader = []byte("\x89PNG\r\n\x1a\n")

// Growing is one still-arriving picture's decode state, kept across frames.
type Growing struct {
	format string // "png", "bmp", "jpeg", "webp", "" while undecided
	// streamable is false once the signature showed a format this cannot decode
	// from a prefix (GIF): the block shows its progress and the full decoder
	// draws the picture when the file is whole.
	streamable bool
	bad        bool // the prefix is not a picture at all

	png pngState
	bmp bmpState
	// jpeg holds the last result decoded from a prefix. libjpeg decodes the
	// whole buffer each call (its scan is not resumable), so the state is only
	// the output; webp is genuinely incremental (see webpDecoder).
	jpeg jpegState
	webp webpDecoder
	// webpMade says the decoder was built (its zero value is not usable: it
	// holds a C pointer).
	webpMade bool

	img  *image.RGBA
	rows int
}

// jpegState remembers how far a JPEG's decode got, so a frame that brought no
// new bytes does not decode again.
type jpegState struct {
	fed  int
	rows int
	img  *image.RGBA
}

// NewGrowing makes the decode state for a picture that is still arriving.
func NewGrowing() *Growing { return &Growing{streamable: true} }

// Rows is how many scanlines have been decoded so far.
func (g *Growing) Rows() int { return g.rows }

// Close releases the resources a decode may hold.
func (g *Growing) Close() { g.webp.close() }

// Bitmap advances the decode with data (the whole prefix received so far) and
// returns the picture. The first call that has something to draw makes a
// bitmap; later calls update those same pixels, so the GPU keeps one texture
// for the whole transfer. ok is false while nothing can be drawn yet.
func (g *Growing) Bitmap(data []byte, keep *ui.Bitmap) (*ui.Bitmap, bool) {
	if g.bad || !g.streamable {
		return nil, false
	}
	if g.format == "" && g.streamable {
		g.detect(data)
	}
	switch g.format {
	case "png":
		if !g.png.advance(data, g) {
			return nil, false
		}
	case "bmp":
		if !g.bmp.advance(data, g) {
			return nil, false
		}
	case "jpeg":
		if !g.advanceJPEG(data) {
			return nil, false
		}
	case "webp":
		if !g.advanceWebP(data) {
			return nil, false
		}
	default:
		return nil, false
	}
	if g.img == nil || g.rows == 0 {
		return nil, false
	}
	if keep == nil {
		return ui.NewBitmap(g.img), true
	}
	keep.Update(g.img)
	return keep, true
}

// advanceJPEG decodes the prefix libjpeg can handle. libjpeg's scan is not
// resumable, so each call that brought new bytes decodes the whole prefix; the
// data is small enough (its entropy coding is compact) that this stays cheap.
func (g *Growing) advanceJPEG(data []byte) bool {
	if len(data) == g.jpeg.fed && g.jpeg.img != nil {
		g.img, g.rows = g.jpeg.img, g.jpeg.rows
		return g.rows > 0
	}
	img, rows, ok := decodeJPEGTolerant(data)
	if !ok {
		return false
	}
	g.jpeg = jpegState{fed: len(data), rows: rows, img: img}
	g.img, g.rows = img, rows
	return true
}

// advanceWebP feeds only the bytes that are new, which is what the format's
// incremental decoder is for.
func (g *Growing) advanceWebP(data []byte) bool {
	if !g.webpMade {
		g.webp = newWebPDecoder()
		g.webpMade = true
	}
	img, rows, ok := g.webp.decode(data)
	if !ok {
		// Keep the rows decoded so far: a later append may add more.
		return g.rows > 0
	}
	g.img, g.rows = img, rows
	return true
}

// detect picks the streaming decoder a prefix can use, from its signature.
// Only GIF still has none: its LZW stream's rows depend on a table that grows
// with the data, and a prefix cannot be decoded into a partial picture the way
// PNG, BMP, JPEG and WebP can. Such a file is left as format "" and
// not-streamable: the block shows its progress and the full decoder draws it
// when the file is whole.
func (g *Growing) detect(data []byte) {
	switch {
	case len(data) >= len(pngHeader) && bytes.Equal(data[:len(pngHeader)], pngHeader):
		g.format = "png"
	case len(data) >= 2 && data[0] == 'B' && data[1] == 'M':
		g.format = "bmp"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		if !nativeJPEGAvailable {
			g.streamable = false
			return
		}
		g.format = "jpeg"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		if !nativeWebPAvailable {
			g.streamable = false
			return
		}
		g.format = "webp"
	default:
		if len(data) < 12 {
			return // the signature may not have arrived yet
		}
		g.streamable = false
	}
}

// pngState is the PNG half of the growing decode.
type pngState struct {
	width, height int
	bitDepth      int
	colorType     int
	stride        int
	bpp           int
	known         bool
	bad           bool

	idat []byte
	raw  []byte
	rows int
	pos  int
	prev []byte
	cur  []byte
}

// bmpState is the BMP half: uncompressed pixels, stored bottom-up by default.
type bmpState struct {
	width, height int
	topDown       bool
	bpp           int
	offset        int
	rowBytes      int
	known         bool
	bad           bool
}

// advance reads the new PNG chunks, inflates what arrived and paints the new
// rows.
func (p *pngState) advance(data []byte, g *Growing) bool {
	if p.bad {
		return false
	}
	if !p.known {
		if len(data) < len(pngHeader) || !bytes.Equal(data[:len(pngHeader)], pngHeader) {
			return false
		}
		p.pos = len(pngHeader)
		p.walk(data)
		if !p.known || p.bad {
			return false
		}
		g.img = image.NewRGBA(image.Rect(0, 0, p.width, p.height))
	} else {
		p.walk(data)
	}
	if !p.inflate() {
		return false
	}
	p.paint(g.img)
	g.rows = p.rows
	return true
}

// walk reads the chunk structure from where it stopped: the geometry from IHDR
// and the IDAT bytes that arrived whole.
func (p *pngState) walk(data []byte) {
	for p.pos+8 <= len(data) {
		length := int(binary.BigEndian.Uint32(data[p.pos:]))
		kind := string(data[p.pos+4 : p.pos+8])
		start := p.pos + 8
		if length < 0 || start+length+4 > len(data) {
			// This chunk has not fully arrived; its data cannot be used, since
			// the next chunk's header would be indistinguishable from payload.
			return
		}
		body := data[start : start+length]
		switch kind {
		case "IHDR":
			if len(body) >= 13 {
				p.width = int(binary.BigEndian.Uint32(body[0:]))
				p.height = int(binary.BigEndian.Uint32(body[4:]))
				p.bitDepth = int(body[8])
				p.colorType = int(body[9])
				if body[12] != 0 || !p.geometry() {
					// Adam7's seven passes mean a prefix is not the picture's top.
					p.bad = true
					return
				}
				p.known = true
			}
		case "IDAT":
			p.idat = append(p.idat, body...)
		case "IEND":
			p.pos = start + length + 4
			return
		}
		p.pos = start + length + 4
	}
}

// geometry works out a scanline's inflated size and the filters' step.
// Only 8-bit RGB (colorType 2) and RGBA (colorType 6) are painted by paintRows;
// other formats decline progressive decoding cleanly rather than returning a
// transparent blank bitmap.
func (p *pngState) geometry() bool {
	if p.width <= 0 || p.height <= 0 {
		return false
	}
	if p.bitDepth != 8 || (p.colorType != 2 && p.colorType != 6) {
		return false
	}
	channels := 3
	if p.colorType == 6 {
		channels = 4
	}
	bitsPerPixel := channels * p.bitDepth
	p.bpp = max(1, (bitsPerPixel+7)/8)
	p.stride = 1 + (p.width*bitsPerPixel+7)/8
	return true
}

// inflate decompresses the IDAT bytes collected so far, keeping whatever comes
// out before the stream turns out to be cut short. A prefix costs 14–36 ms on a
// multi-megabyte screenshot, which a frame can afford.
func (p *pngState) inflate() bool {
	if p.stride <= 0 || len(p.idat) == 0 {
		return false
	}
	r, err := zlib.NewReader(bytes.NewReader(p.idat))
	if err != nil {
		return false
	}
	defer r.Close()
	want := p.stride * p.height
	out := p.raw[:0]
	buf := make([]byte, 64<<10)
	for len(out) < want {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil || n == 0 {
			break
		}
	}
	if len(out) > want {
		out = out[:want]
	}
	p.raw = out
	p.rows = min(len(out)/p.stride, p.height)
	return true
}

// paint reverses the filters of the rows that arrived and writes them into the
// picture. out is freshly inflated each frame, so every row starts filtered.
func (p *pngState) paint(img *image.RGBA) { p.paintRows(img) }

// paintRows is paint with the picture it writes into.
func (p *pngState) paintRows(img *image.RGBA) {
	if img == nil || p.stride <= 0 || p.bpp <= 0 || p.rows <= 0 {
		return
	}
	if p.bitDepth != 8 || (p.colorType != 2 && p.colorType != 6) {
		return
	}
	if p.prev == nil {
		p.prev = make([]byte, p.stride-1)
		p.cur = make([]byte, p.stride-1)
	} else {
		// Row 0's PNG filters (Up/Average/Paeth) assume an all-zero previous
		// scanline; reusing p.prev across frames without clearing corrupts row 0.
		clear(p.prev)
	}
	channels := 3
	if p.colorType == 6 {
		channels = 4
	}
	for y := 0; y < p.rows; y++ {
		off := y * p.stride
		copy(p.cur, p.raw[off+1:off+p.stride])
		if !unfilterRow(p.raw[off], p.cur, p.prev, p.bpp) {
			p.rows = y
			return
		}
		for x := 0; x < p.width; x++ {
			i := x * channels
			if i+channels > len(p.cur) {
				break
			}
			c := color.RGBA{R: p.cur[i], G: p.cur[i+1], B: p.cur[i+2], A: 0xFF}
			if channels == 4 {
				c.A = p.cur[i+3]
			}
			img.SetRGBA(x, y, c)
		}
		copy(p.prev, p.cur)
	}
}

// advance reads the BMP header and the pixel rows that arrived. A BMP stores
// its pixels uncompressed, so a prefix is a band of the picture; the header's
// height says where that band sits (bottom-up by default).
func (b *bmpState) advance(data []byte, g *Growing) bool {
	if !b.known {
		if len(data) < 54 {
			return false
		}
		if data[0] != 'B' || data[1] != 'M' {
			b.bad = true
			return false
		}
		b.offset = int(binary.LittleEndian.Uint32(data[10:]))
		headerSize := int(binary.LittleEndian.Uint32(data[14:]))
		if headerSize < 12 {
			b.bad = true
			return false
		}
		w := int(int32(binary.LittleEndian.Uint32(data[18:])))
		h := int(int32(binary.LittleEndian.Uint32(data[22:])))
		b.bpp = int(binary.LittleEndian.Uint16(data[28:]))
		comp := binary.LittleEndian.Uint32(data[30:])
		if comp != 0 && comp != 3 {
			// RLE and embedded JPEG/PNG payloads are not a row-major band.
			b.bad = true
			return false
		}
		if b.bpp != 24 && b.bpp != 32 {
			b.bad = true
			return false
		}
		if h < 0 {
			b.topDown, h = true, -h
		}
		if w <= 0 || h <= 0 {
			b.bad = true
			return false
		}
		b.width, b.height = w, h
		b.rowBytes = ((w*b.bpp + 31) / 32) * 4
		b.known = true
		g.img = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	if b.bad || b.rowBytes <= 0 {
		return false
	}
	avail := len(data) - b.offset
	if avail <= 0 {
		return false
	}
	rows := avail / b.rowBytes
	if rows > b.height {
		rows = b.height
	}
	step := b.bpp / 8
	for i := 0; i < rows; i++ {
		src := data[b.offset+i*b.rowBytes:]
		if len(src) < b.rowBytes {
			break
		}
		// Bottom-up BMPs store the last picture row first, so row i of the file
		// is row height-1-i of the picture.
		y := i
		if !b.topDown {
			y = b.height - 1 - i
		}
		for x := 0; x < b.width; x++ {
			j := x * step
			if j+3 > len(src) {
				break
			}
			g.img.SetRGBA(x, y, color.RGBA{R: src[j+2], G: src[j+1], B: src[j], A: 0xFF})
		}
	}
	// The picture has as many rows as the band covers, measured from the top:
	// a bottom-up file's prefix is its bottom, so the row count is the same
	// either way for the purpose of "how much of it is known".
	g.rows = rows
	return true
}

// unfilterRow reverses one scanline's PNG filter in place. It reports false for
// a filter byte it does not know.
func unfilterRow(filter byte, cur, prev []byte, bpp int) bool {
	switch filter {
	case 0: // none
		return true
	case 1: // sub
		for i := bpp; i < len(cur); i++ {
			cur[i] += cur[i-bpp]
		}
		return true
	case 2: // up
		for i := range cur {
			cur[i] += prev[i]
		}
		return true
	case 3: // average
		for i := range cur {
			var left int
			if i >= bpp {
				left = int(cur[i-bpp])
			}
			cur[i] += byte((left + int(prev[i])) / 2)
		}
		return true
	case 4: // paeth
		for i := range cur {
			var left, upLeft int
			if i >= bpp {
				left = int(cur[i-bpp])
				upLeft = int(prev[i-bpp])
			}
			cur[i] += byte(paeth(left, int(prev[i]), upLeft))
		}
		return true
	}
	return false
}

// paeth is the PNG filter's predictor.
func paeth(a, b, c int) int {
	p := a + b - c
	pa, pb, pc := abs(p-a), abs(p-b), abs(p-c)
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
