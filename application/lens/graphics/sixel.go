package graphics

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
)

// DecodeSixel decodes a sixel DCS payload (the bytes after "DCS", including the
// "q" introducer) into PNG bytes. It supports the raster attributes, RGB and HLS
// colour definitions, colour selection, "!" repeats, "$" carriage returns, "-"
// band advances and a transparent background (P2=1).
func DecodeSixel(body string) ([]byte, bool) {
	q := strings.IndexByte(body, 'q')
	if q < 0 {
		return nil, false
	}
	params := body[:q]
	// A sixel DCS introduces itself with numeric parameters only ("0;0;0q").
	// Other DCS strings that contain a 'q' are not pictures: yazi probes sixel
	// support with DECRQSS's "$q", whose payload used to be decoded as a
	// one-column image and drawn under the command.
	if !numericSixelParams(params) {
		return nil, false
	}
	data := body[q+1:]
	transparent := false
	if fields := strings.Split(params, ";"); len(fields) >= 2 {
		if n, err := strconv.Atoi(fields[1]); err == nil && n == 1 {
			transparent = true
		}
	}
	dec := &sixelDecoder{colors: map[int]color.RGBA{}, transparent: transparent, maxW: 4096, maxH: 4096}
	if !dec.run(data) {
		return nil, false
	}
	return dec.png()
}

// numericSixelParams reports that a DCS introducer before the sixel 'q' is made
// of decimal parameters and ';' separators only, as the protocol specifies.
func numericSixelParams(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && s[i] != ';' {
			return false
		}
	}
	return true
}

type sixelDecoder struct {
	pixels      map[[2]int]color.RGBA // sparse: untouched pixels stay background
	colors      map[int]color.RGBA
	cur         color.RGBA
	transparent bool
	x, y        int // sixel origin: x in pixels, y at the band's top
	maxX, maxY  int
	declW       int
	declH       int
	maxW, maxH  int
}

// run walks the sixel data. It returns false when the image exceeds its bounds.
func (d *sixelDecoder) run(s string) bool {
	d.pixels = map[[2]int]color.RGBA{}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			nums, n := readSixelNumbers(s[i+1:], 4)
			if len(nums) >= 3 {
				d.declW = nums[2]
			}
			if len(nums) >= 4 {
				d.declH = nums[3]
			}
			i += n
		case ch == '#':
			nums, n := readSixelNumbers(s[i+1:], 5)
			i += n
			if len(nums) == 0 {
				continue
			}
			idx := nums[0]
			if len(nums) >= 5 && nums[1] == 2 {
				d.colors[idx] = color.RGBA{
					R: scale100(nums[2]), G: scale100(nums[3]), B: scale100(nums[4]), A: 255,
				}
			} else if len(nums) >= 5 && nums[1] == 1 {
				d.colors[idx] = hlsToRGB(nums[2], nums[3], nums[4])
			}
			if c, ok := d.colors[idx]; ok {
				d.cur = c
			} else {
				d.cur = defaultSixelPalette[idx%len(defaultSixelPalette)]
			}
		case ch == '!':
			nums, n := readSixelNumbers(s[i+1:], 1)
			i += n
			if len(nums) == 0 || i+1 >= len(s) {
				continue
			}
			i++
			rep := nums[0]
			if rep < 1 {
				rep = 1
			}
			if rep > 65535 {
				rep = 65535
			}
			for k := 0; k < rep; k++ {
				if !d.sixelChar(s[i]) {
					return false
				}
			}
		case ch == '$':
			d.x = 0
		case ch == '-':
			d.x = 0
			d.y += 6
		case ch >= '?' && ch <= '~':
			if !d.sixelChar(ch) {
				return false
			}
		}
	}
	return true
}

// sixelChar paints one sixel column: six vertical pixels from the current
// colour's bit mask.
func (d *sixelDecoder) sixelChar(ch byte) bool {
	bits := int(ch - '?')
	if bits < 0 || bits > 63 {
		return true
	}
	for b := 0; b < 6; b++ {
		if bits&(1<<b) == 0 {
			continue
		}
		x, y := d.x, d.y+b
		if x >= d.maxW || y >= d.maxH {
			return false
		}
		c := d.cur
		if c.A == 0 {
			c = defaultSixelPalette[0]
		}
		d.pixels[[2]int{x, y}] = c
	}
	if d.x+1 > d.maxX {
		d.maxX = d.x + 1
	}
	if d.y+6 > d.maxY {
		d.maxY = d.y + 6
	}
	d.x++
	return true
}

// png crops the image to the declared or painted size and encodes it.
func (d *sixelDecoder) png() ([]byte, bool) {
	w, h := d.maxX, d.maxY
	// The raster attributes declare the canvas exactly: a larger declared size
	// keeps background there, a smaller one clips bands past the image.
	if d.declW > 0 {
		w = d.declW
	}
	if d.declH > 0 {
		h = d.declH
	}
	if w > d.maxW || h > d.maxH {
		return nil, false
	}
	if w <= 0 || h <= 0 {
		return nil, false
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if !d.transparent {
		bg := color.RGBA{0, 0, 0, 255} // sixel's default background is black
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.SetRGBA(x, y, bg)
			}
		}
	}
	for p, c := range d.pixels {
		if p[0] < w && p[1] < h {
			img.SetRGBA(p[0], p[1], c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

// readSixelNumbers reads up to max decimal numbers separated by ';' and returns
// them with how many bytes were consumed.
func readSixelNumbers(s string, max int) ([]int, int) {
	var nums []int
	i := 0
	for len(nums) < max {
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i {
			break
		}
		n, _ := strconv.Atoi(s[i:j])
		nums = append(nums, n)
		i = j
		if i < len(s) && s[i] == ';' {
			i++
			continue
		}
		break
	}
	return nums, i
}

func scale100(v int) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 100 {
		return 255
	}
	return uint8(v * 255 / 100)
}

// hlsToRGB converts sixel's HLS (hue degrees, lightness and saturation in
// percent) to RGB.
func hlsToRGB(h, l, s int) color.RGBA {
	if h < 0 {
		h = 0
	}
	h = h % 360
	lf := float64(l) / 100
	sf := float64(s) / 100
	hh := float64(h)
	c := (1 - abs(2*lf-1)) * sf
	x := c * (1 - abs(mod(hh/60, 2)-1))
	var r, g, b float64
	switch {
	case hh < 60:
		r, g, b = c, x, 0
	case hh < 120:
		r, g, b = x, c, 0
	case hh < 180:
		r, g, b = 0, c, x
	case hh < 240:
		r, g, b = 0, x, c
	case hh < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	m := lf - c/2
	return color.RGBA{
		R: uint8((r + m) * 255), G: uint8((g + m) * 255), B: uint8((b + m) * 255), A: 255,
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func mod(a, b float64) float64 {
	for a >= b {
		a -= b
	}
	return a
}

// defaultSixelPalette is the VT340's 16 colours, the palette sixel assumes
// before any # definition.
var defaultSixelPalette = []color.RGBA{
	{0, 0, 0, 255}, {51, 51, 51, 255}, {102, 102, 102, 255}, {153, 153, 153, 255},
	{204, 204, 204, 255}, {255, 255, 255, 255}, {255, 0, 0, 255}, {0, 255, 0, 255},
	{0, 0, 255, 255}, {255, 255, 0, 255}, {0, 255, 255, 255}, {255, 0, 255, 255},
	{128, 0, 0, 255}, {0, 128, 0, 255}, {0, 0, 128, 255}, {128, 128, 0, 255},
}
