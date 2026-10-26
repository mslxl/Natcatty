package browser

import "github.com/mslxl/natcatty/domain/model"

// SiteIcon is the favicon cached for one Host and page origin. The cache is
// Host-scoped so two Hosts that both serve https://localhost:8080 cannot see
// each other's icon.
type SiteIcon struct {
	HostID    model.HostID `json:"hostId"`
	Origin    string       `json:"origin"`
	Mode      IconMode     `json:"mode"`
	DataURL   string       `json:"dataUrl"`
	Glyph     string       `json:"glyph,omitempty"`
	Color     string       `json:"color,omitempty"`
	UpdatedAt int64        `json:"updatedAt"`
}
