package theme

import "github.com/egoist/mygo/ui"

// Fixed colors that read the same in every preset, so surfaces share one
// value instead of writing their own.
var (
	// Scrim dims the window under a command palette.
	Scrim = ui.RGBA(0, 0, 0, 0.4)
	// OverlayShadow is the shadow under a command palette.
	OverlayShadow = ui.RGBA(0, 0, 0, 0.3)
	// CloseHover fills the window's close button while hovered, and
	// CloseHoverText is its glyph, as the system's red close button.
	CloseHover     = ui.RGB(239, 68, 68)
	CloseHoverText = ui.RGB(255, 255, 255)
	// Bookmark marks a bookmarked folder, as the original's yellow star.
	Bookmark = ui.Hex("#EAB308")
)

// Monitoring colors, as the original's: the same hue names a metric on every
// chart and in every preset. CPU is sky, memory emerald, upload violet, and
// MonitorAttention is the amber of a stale or disconnected Host and of a
// meter near its limit.
var (
	SeriesCPU        = ui.Hex("#0EA5E9")
	SeriesMemory     = ui.Hex("#10B981")
	SeriesUpload     = ui.Hex("#8B5CF6")
	MonitorAttention = ui.Hex("#F59E0B")
)
