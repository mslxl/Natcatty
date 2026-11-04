// SPDX-License-Identifier: GPL-3.0-or-later

// Package terminal is the bounded, stock-compatible client-side mosh terminal:
// the UTF-8 parser and emulator behind Terminal::Complete, the Display encoder
// that turns framebuffer differences into ANSI/ECMA-48 output, and the overlays
// the client applies before drawing.
//
// The behavior is a faithful port of mosh 1.4.0's src/terminal and
// src/statesync/completeterminal.cc. It is deliberately not a general terminal
// emulator: it supports exactly the ECMA-48 subset stock mosh understands, so
// that its display output is byte-for-byte comparable with mosh-client's.
package terminal

import "strconv"

// Renditions is Terminal::Renditions: colors and the eight attributes mosh
// tracks. It is a value type; equality is structural.
type Renditions struct {
	Foreground uint32
	Background uint32
	Attributes uint8
}

// Attribute bits, in stock mosh's order.
const (
	AttrBold uint8 = 1 << iota
	AttrFaint
	AttrItalic
	AttrUnderlined
	AttrBlink
	AttrInverse
	AttrInvisible
	attrCount
)

const trueColorMask uint32 = 0x1000000

// NewRenditions returns renditions with the given background color and default
// foreground and attributes, as Renditions(background) does.
func NewRenditions(background uint32) Renditions {
	return Renditions{Background: background}
}

// MakeTrueColor packs an RGB value the way mosh's make_true_color does.
func MakeTrueColor(r, g, b uint32) uint32 {
	return trueColorMask | (r&0xff)<<16 | (g&0xff)<<8 | b&0xff
}

// IsTrueColor reports whether a packed color is a 24-bit true color.
func IsTrueColor(color uint32) bool { return color&trueColorMask != 0 }

// SetRendition applies one SGR parameter. Colors outside the 16-color set are
// ignored here, exactly as stock's Renditions::set_rendition does; 256-color
// and true-color parameters are consumed by the dispatcher.
func (r *Renditions) SetRendition(num int) {
	switch {
	case num == 0:
		r.Attributes = 0
		r.Foreground = 0
		r.Background = 0
		return
	case num == 39:
		r.Foreground = 0
		return
	case num == 49:
		r.Background = 0
		return
	case num >= 30 && num <= 37:
		r.Foreground = uint32(num)
		return
	case num >= 40 && num <= 47:
		r.Background = uint32(num)
		return
	case num >= 90 && num <= 97:
		r.Foreground = uint32(num - 90 + 38)
		return
	case num >= 100 && num <= 107:
		r.Background = uint32(num - 100 + 48)
		return
	}

	value := num < 9
	var attribute uint8
	switch num {
	case 1, 22:
		attribute = AttrBold
	case 3, 23:
		attribute = AttrItalic
	case 4, 24:
		attribute = AttrUnderlined
	case 5, 25:
		attribute = AttrBlink
	case 7, 27:
		attribute = AttrInverse
	case 8, 28:
		attribute = AttrInvisible
	default:
		return // ignore unknown rendition
	}
	if value {
		r.Attributes |= attribute
	} else {
		r.Attributes &^= attribute
	}
}

// SetForegroundColor sets an indexed (0-255) or packed true color.
func (r *Renditions) SetForegroundColor(num uint32) {
	if num <= 255 {
		r.Foreground = 30 + num
	} else if IsTrueColor(num) {
		r.Foreground = num
	}
}

// SetBackgroundColor sets an indexed (0-255) or packed true color.
func (r *Renditions) SetBackgroundColor(num uint32) {
	if num <= 255 {
		r.Background = 40 + num
	} else if IsTrueColor(num) {
		r.Background = num
	}
}

// SetAttribute turns one attribute on or off.
func (r *Renditions) SetAttribute(attribute uint8, value bool) {
	if value {
		r.Attributes |= attribute
	} else {
		r.Attributes &^= attribute
	}
}

// GetAttribute reports one attribute.
func (r *Renditions) GetAttribute(attribute uint8) bool {
	return r.Attributes&attribute != 0
}

// SGR renders the rendition exactly as stock's Renditions::sgr does, including
// its choice between ANSI, 256-color, and true-color forms.
func (r Renditions) SGR() string {
	out := "\033[0"
	if r.GetAttribute(AttrBold) {
		out += ";1"
	}
	if r.GetAttribute(AttrItalic) {
		out += ";3"
	}
	if r.GetAttribute(AttrUnderlined) {
		out += ";4"
	}
	if r.GetAttribute(AttrBlink) {
		out += ";5"
	}
	if r.GetAttribute(AttrInverse) {
		out += ";7"
	}
	if r.GetAttribute(AttrInvisible) {
		out += ";8"
	}

	if r.Foreground != 0 {
		switch {
		case IsTrueColor(r.Foreground):
			out += ";38;2;" + strconv.Itoa(int((r.Foreground>>16)&0xff)) +
				";" + strconv.Itoa(int((r.Foreground>>8)&0xff)) +
				";" + strconv.Itoa(int(r.Foreground&0xff))
		case r.Foreground > 37:
			out += ";38;5;" + strconv.Itoa(int(r.Foreground-30))
		default:
			out += ";" + strconv.Itoa(int(r.Foreground))
		}
	}
	if r.Background != 0 {
		switch {
		case IsTrueColor(r.Background):
			out += ";48;2;" + strconv.Itoa(int((r.Background>>16)&0xff)) +
				";" + strconv.Itoa(int((r.Background>>8)&0xff)) +
				";" + strconv.Itoa(int(r.Background&0xff))
		case r.Background > 47:
			out += ";48;5;" + strconv.Itoa(int(r.Background-40))
		default:
			out += ";" + strconv.Itoa(int(r.Background))
		}
	}
	return out + "m"
}
