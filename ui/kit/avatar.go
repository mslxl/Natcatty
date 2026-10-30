package kit

import (
	"hash/fnv"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/ui/icons"
)

// AvatarKind selects the glyph of a Host avatar when no distribution logo
// is known.
type AvatarKind int

const (
	// AvatarServer is a remote shell Host.
	AvatarServer AvatarKind = iota
	// AvatarLocal is a local terminal.
	AvatarLocal
	// AvatarSerial is a serial port.
	AvatarSerial
	// AvatarTelnet is a Telnet Host.
	AvatarTelnet
	// AvatarGroup is a Host Group.
	AvatarGroup
)

// avatarPalette colors Hosts without a known distribution, by a hash of
// their name, so each Host keeps one recognizable color.
var avatarPalette = []string{
	"#2563EB", "#0891B2", "#059669", "#7C3AED", "#DB2777",
	"#EA580C", "#4F46E5", "#0D9488", "#9333EA", "#475569",
}

// HostAvatar draws the rounded tile in front of a Host: the logo of its
// distribution on the brand color when distro is known, else a glyph for
// kind on a color derived from seed. size is the tile's edge in DIPs.
func HostAvatar(c *ui.Context, distro string, kind AvatarKind, seed string, size float32) ui.Element {
	var fill ui.Color
	var glyph *ui.SVG
	if color, ok := icons.DistroColor(distro); ok {
		fill = color
		glyph = icons.Distro(distro)
	}
	if glyph == nil {
		switch kind {
		case AvatarLocal:
			fill, glyph = ui.Hex("#334155"), icons.Lucide(icons.SquareTerminal)
		case AvatarSerial:
			fill, glyph = ui.Hex("#D97706"), icons.Lucide(icons.Usb)
		case AvatarTelnet:
			fill, glyph = ui.Hex("#0F766E"), icons.Lucide(icons.Network)
		case AvatarGroup:
			fill, glyph = c.Theme().Accent, icons.Lucide(icons.Folder)
		default:
			fill, glyph = SeedColor(seed), icons.Lucide(icons.Server)
		}
	}
	glyphSize := size * 0.5
	return ui.Box(c).Size(size, size).Radius(size * 0.27).Background(fill).Center().Shrink(0).
		TextColor(ui.RGB(255, 255, 255)).
		Children(func() { ui.Icon(c, glyph).Size(glyphSize, glyphSize) })
}

// SeedColor picks a stable palette color for a name.
func SeedColor(seed string) ui.Color {
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return ui.Hex(avatarPalette[h.Sum32()%uint32(len(avatarPalette))])
}
