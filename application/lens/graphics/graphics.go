// Package graphics reads the pictures a program prints into a terminal: kitty's
// APC graphics protocol (ESC _ G … ESC \) and iTerm2's OSC 1337 File=… inline
// images. Terminal draws those on its own grid; MyGo's terminal plugin has no image
// hook, so the app replays them as image surfaces in the flow instead.
package graphics

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strconv"
	"strings"
)

// Image is one picture a program printed.
type Image struct {
	Data []byte // PNG bytes, ready for ui.DecodeBitmap
	Name string
}

// Parser turns a raw PTY stream into images. Feed it every chunk; it keeps the
// partial escape sequence at the end for the next chunk.
type Parser struct {
	buf     []byte
	kitty   map[string][]byte // chunk accumulator by image id (or "auto")
	maxSize int
	// sawGraphics is any graphics-protocol sequence, picture or not. A program
	// that queries the protocol draws with it (yazi asks before it previews),
	// and the terminal's own view is the only renderer that can show what it
	// draws; the queries themselves carry no picture to keep.
	sawGraphics bool
}

// New returns a parser that drops a single image above maxSize bytes.
func New() *Parser {
	return &Parser{kitty: map[string][]byte{}, maxSize: 32 << 20}
}

// Feed consumes a chunk and returns the images it completed.
func (p *Parser) Feed(b []byte) []Image {
	p.buf = append(p.buf, b...)
	if len(p.buf) > 16<<20 {
		// A runaway sequence (not an image we understand): keep the tail only.
		p.buf = p.buf[len(p.buf)-1<<20:]
	}
	var out []Image
	for {
		i := bytes.IndexByte(p.buf, 0x1b)
		if i < 0 {
			p.buf = p.buf[:0]
			return out
		}
		if i > 0 {
			p.buf = p.buf[i:]
		}
		if len(p.buf) < 2 {
			return out
		}
		switch p.buf[1] {
		case '_': // APC: ESC _ G <params>;<payload> ESC \
			end := bytes.Index(p.buf[2:], []byte{0x1b, '\\'})
			if end < 0 {
				return out
			}
			body := p.buf[2 : 2+end]
			p.buf = p.buf[2+end+2:]
			if len(body) > 0 && body[0] == 'G' {
				p.sawGraphics = true
				if im, ok := p.kittyChunk(string(body[1:])); ok {
					out = append(out, im)
				}
			}
		case ']': // OSC: ESC ] 1337 ; File=… BEL or ESC \
			body, n := oscBody(p.buf[2:])
			if n < 0 {
				return out
			}
			p.buf = p.buf[2+n:]
			if strings.HasPrefix(body, "1337;File=") {
				p.sawGraphics = true
			}
			if im, ok := itermFile(body); ok {
				out = append(out, im)
			}
		case 'P': // DCS: ESC P <params> q <sixel data> ESC \
			end := bytes.Index(p.buf[2:], []byte{0x1b, '\\'})
			if end < 0 {
				return out
			}
			body := string(p.buf[2 : 2+end])
			p.buf = p.buf[2+end+2:]
			// A sixel picture, or DECRQSS's "$q" sixel capability query (what
			// yazi sends). Other DCS strings (XTGETTCAP, for one) say nothing
			// about graphics and must not ask for the terminal's own view.
			if isSixelDCS(body) {
				p.sawGraphics = true
			}
			if data, ok := DecodeSixel(body); ok {
				out = append(out, Image{Data: data, Name: "sixel image"})
			}
		case '[': // CSI: skip to the final byte
			j := 2
			for j < len(p.buf) && (p.buf[j] < 0x40 || p.buf[j] > 0x7e) {
				j++
			}
			if j >= len(p.buf) {
				return out
			}
			p.buf = p.buf[j+1:]
		default:
			p.buf = p.buf[1:]
		}
	}
}

// SawGraphics reports whether the stream carried any graphics-protocol
// sequence since ForgetGraphics: a picture, a query or a delete. A program that
// asks the protocol is one that draws with it, which is what the pane needs to
// know to draw the screen with the terminal's own view.
func (p *Parser) SawGraphics() bool { return p.sawGraphics }

// ForgetGraphics clears the graphics activity, which the pane does when the
// program that used it leaves its alternate screen: the next program starts
// clean.
func (p *Parser) ForgetGraphics() { p.sawGraphics = false }

// isSixelDCS reports a DCS that is a sixel picture or DECRQSS's sixel
// capability query ("$q" + the "q" request, which yazi sends before it draws).
func isSixelDCS(body string) bool {
	if strings.HasPrefix(body, "$q") {
		return strings.HasPrefix(strings.TrimLeft(body[2:], " "), "q")
	}
	q := strings.IndexByte(body, 'q')
	return q >= 0 && numericSixelParams(body[:q])
}

// oscBody returns the bytes between ESC ] and its BEL/ST terminator, and how
// many bytes (including the terminator) the sequence used; n < 0 means the
// terminator is not here yet.
func oscBody(b []byte) (string, int) {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case 0x07:
			return string(b[:i]), i + 1
		case 0x1b:
			if i+1 < len(b) && b[i+1] == '\\' {
				return string(b[:i]), i + 2
			}
		}
	}
	return "", -1
}

// kittyChunk parses one G payload: comma-separated key=value params, then ';'
// and the base64 data. Chunks with m=1 accumulate under the image id.
func (p *Parser) kittyChunk(s string) (Image, bool) {
	semi := strings.IndexByte(s, ';')
	params, data := s, ""
	if semi >= 0 {
		params, data = s[:semi], s[semi+1:]
	}
	var id, format, more, action string
	width, height := 0, 0
	for _, kv := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "a":
			action = v
		case "i":
			id = v
		case "f":
			format = v
		case "m":
			more = v
		case "s":
			width, _ = strconv.Atoi(v)
		case "v":
			height, _ = strconv.Atoi(v)
		}
	}
	// Only a transmit (the default) or a transmit-and-display carries a picture
	// to keep. A query (a=q) asks whether the terminal speaks the protocol and
	// its payload is a 1x1 sample, not something to draw: yazi probes both
	// direct and shared-memory transmission this way, and rendering the probes
	// left dots under the command that ran it. Deletes, puts and animation
	// commands carry no picture at all.
	switch action {
	case "", "t", "T":
	default:
		return Image{}, false
	}
	if id == "" {
		id = "auto"
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return Image{}, false
	}
	acc := append(p.kitty[id], raw...)
	if more == "1" {
		p.kitty[id] = acc
		return Image{}, false
	}
	delete(p.kitty, id)
	if len(acc) > p.maxSize {
		return Image{}, false
	}
	switch format {
	case "", "100": // PNG
		if !bytes.HasPrefix(acc, []byte("\x89PNG\r\n\x1a\n")) {
			return Image{}, false
		}
		return Image{Data: acc, Name: "inline image"}, true
	case "32", "24": // raw RGBA / RGB
		if width <= 0 || height <= 0 {
			return Image{}, false
		}
		bpp := 4
		if format == "24" {
			bpp = 3
		}
		if len(acc) < width*height*bpp {
			return Image{}, false
		}
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				o := (y*width + x) * bpp
				c := img.RGBAAt(x, y)
				c.R, c.G, c.B, c.A = acc[o], acc[o+1], acc[o+2], 0xff
				if bpp == 4 {
					c.A = acc[o+3]
				}
				img.SetRGBA(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return Image{}, false
		}
		return Image{Data: buf.Bytes(), Name: "inline image"}, true
	}
	return Image{}, false
}

// itermFile parses iTerm2's `1337;File=<args>:<base64>` OSC.
func itermFile(s string) (Image, bool) {
	const prefix = "1337;File="
	if !strings.HasPrefix(s, prefix) {
		return Image{}, false
	}
	rest := s[len(prefix):]
	colon := strings.LastIndexByte(rest, ':')
	if colon < 0 {
		return Image{}, false
	}
	args, b64 := rest[:colon], rest[colon+1:]
	name := "inline image"
	for _, kv := range strings.Split(args, ";") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == "name" {
			if dec, err := base64.StdEncoding.DecodeString(v); err == nil {
				name = string(dec)
			}
		}
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(data) == 0 || len(data) > 32<<20 {
		return Image{}, false
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) &&
		!bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) {
		return Image{}, false
	}
	return Image{Data: data, Name: name}, true
}
