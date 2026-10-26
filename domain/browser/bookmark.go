package browser

import (
	"encoding/base64"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mslxl/natcatty/domain/model"
)

// BookmarkScope is who may see a bookmark. Host bookmarks belong to one saved
// Host; all-Host bookmarks appear in every Host's browser panel.
type BookmarkScope string

const (
	ScopeHost     BookmarkScope = "host"
	ScopeAllHosts BookmarkScope = "all-hosts"
)

// IconMode selects between the site's favicon and a custom icon.
type IconMode string

const (
	IconModeAuto   IconMode = "auto"
	IconModeCustom IconMode = "custom"
)

const (
	// MaxBookmarks is the original bookmark store's limit.
	MaxBookmarks = 1000
	// MaxBookmarkTitle is the original 120-character title limit.
	MaxBookmarkTitle = 120
	// MaxBookmarkIcon is the original four-character glyph limit.
	MaxBookmarkIcon = 4
	// MaxBookmarkID is the original accepted identifier length.
	MaxBookmarkID = 128
	// MaxBookmarkHost is the original accepted Host identifier length.
	MaxBookmarkHost = 256
	// MaxSiteIcons is the original favicon store limit.
	MaxSiteIcons = 1000
	// MaxIconBytes is the original favicon byte limit.
	MaxIconBytes = 64 * 1024
	// DefaultBookmarkColor is the original fallback tile color.
	DefaultBookmarkColor = "#247969"
)

// Bookmark errors are classified for the UI; they carry no user-visible copy.
var (
	ErrBookmarkHost  = errors.New("browser: host bookmark is missing its host")
	ErrBookmarkID    = errors.New("browser: duplicate bookmark id")
	ErrBookmarkLimit = errors.New("browser: bookmark limit reached")
	ErrBookmarkOwner = errors.New("browser: bookmark belongs to another host")
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// BookmarkInput is an edit or creation request. Empty identity fields are
// filled from defaults, mirroring the original store.
type BookmarkInput struct {
	ID          string
	Title       string
	URL         string
	Scope       BookmarkScope
	HostID      model.HostID
	Icon        string
	IconMode    IconMode
	IconDataURL string
	Color       string
	Order       int64
	CreatedAt   int64
	UpdatedAt   int64
}

// Bookmark is a stored bookmark. HostID is empty for all-Host bookmarks.
type Bookmark struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	URL         string        `json:"url"`
	Scope       BookmarkScope `json:"scope"`
	HostID      model.HostID  `json:"hostId,omitempty"`
	Icon        string        `json:"icon"`
	IconMode    IconMode      `json:"iconMode"`
	IconDataURL string        `json:"iconDataUrl,omitempty"`
	Color       string        `json:"color"`
	Order       int64         `json:"order"`
	CreatedAt   int64         `json:"createdAt"`
	UpdatedAt   int64         `json:"updatedAt"`
}

// NormalizeBookmark validates and defaults one bookmark. previous supplies the
// identity and creation time on an edit; newID is called only when the input
// has no acceptable identifier.
func NormalizeBookmark(input BookmarkInput, previous *Bookmark, now time.Time, newID func() string) (Bookmark, error) {
	normalizedURL, err := NormalizeURL(input.URL)
	if err != nil {
		return Bookmark{}, err
	}
	scope := ScopeHost
	if input.Scope == ScopeAllHosts {
		scope = ScopeAllHosts
	}
	hostID := model.HostID("")
	if scope == ScopeHost {
		hostID = model.HostID(truncate(string(input.HostID), MaxBookmarkHost))
		if hostID == "" {
			return Bookmark{}, ErrBookmarkHost
		}
	}
	parsed, _ := url.Parse(normalizedURL)
	title := truncate(strings.TrimSpace(input.Title), MaxBookmarkTitle)
	if title == "" {
		title = parsed.Hostname()
	}
	icon := truncate(strings.TrimSpace(input.Icon), MaxBookmarkIcon)
	if icon == "" {
		icon, _ = firstRune(title)
	}
	if icon == "" {
		icon = "◇"
	}
	color := input.Color
	if !colorPattern.MatchString(color) {
		color = DefaultBookmarkColor
	}
	mode := IconModeAuto
	if input.IconMode == IconModeCustom {
		mode = IconModeCustom
	}
	iconDataURL := ""
	if ValidImageDataURL(input.IconDataURL) {
		iconDataURL = input.IconDataURL
	} else if previous != nil && previous.IconDataURL != "" {
		iconDataURL = previous.IconDataURL
	}
	id := ""
	if previous != nil {
		id = previous.ID
	} else if trimmed := strings.TrimSpace(input.ID); trimmed != "" && len(trimmed) <= MaxBookmarkID {
		id = trimmed
	} else if newID != nil {
		id = newID()
	}
	if id == "" {
		return Bookmark{}, ErrBookmarkID
	}
	order := input.Order
	created := input.CreatedAt
	updated := input.UpdatedAt
	if previous != nil {
		if input.Order == 0 && previous.Order != 0 {
			order = previous.Order
		}
		created = previous.CreatedAt
		updated = now.UnixMilli()
	} else {
		if created <= 0 {
			created = now.UnixMilli()
		}
		if updated <= 0 {
			updated = created
		}
	}
	return Bookmark{
		ID: id, Title: title, URL: normalizedURL, Scope: scope, HostID: hostID,
		Icon: icon, IconMode: mode, IconDataURL: iconDataURL, Color: color,
		Order: order, CreatedAt: created, UpdatedAt: updated,
	}, nil
}

// VisibleBookmarks returns the bookmarks a Host may see: its own Host-scoped
// entries plus every all-Host entry, in stable display order.
func VisibleBookmarks(items []Bookmark, hostID model.HostID) []Bookmark {
	visible := make([]Bookmark, 0, len(items))
	for _, item := range items {
		if item.Scope == ScopeAllHosts || item.HostID == hostID {
			visible = append(visible, item)
		}
	}
	sort.SliceStable(visible, func(i, j int) bool {
		if visible[i].Order != visible[j].Order {
			return visible[i].Order < visible[j].Order
		}
		return visible[i].CreatedAt < visible[j].CreatedAt
	})
	return visible
}

// ValidateBookmarkList checks a whole list, as the original apply command did.
func ValidateBookmarkList(items []Bookmark) error {
	if len(items) > MaxBookmarks {
		return ErrBookmarkLimit
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, ok := seen[item.ID]; ok {
			return ErrBookmarkID
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

func firstRune(value string) (string, bool) {
	for _, r := range value {
		return string(r), true
	}
	return "", false
}

// ValidImageDataURL reports whether raw is a small base64 image data URL in
// one of the image types the page surface can display.
func ValidImageDataURL(raw string) bool {
	const prefix = "data:image/"
	if !strings.HasPrefix(raw, prefix) {
		return false
	}
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return false
	}
	header, payload := raw[:comma], raw[comma+1:]
	if !strings.HasSuffix(header, ";base64") {
		return false
	}
	mime := strings.TrimSuffix(strings.TrimPrefix(header, prefix), ";base64")
	switch mime {
	case "png", "jpeg", "jpg", "gif", "webp", "x-icon", "vnd.microsoft.icon":
	default:
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(decoded) == 0 || len(decoded) > MaxIconBytes {
		return false
	}
	return SniffImageMIME(decoded) != ""
}

// SniffImageMIME returns the image MIME type from its magic bytes. It accepts
// the same formats as the MyGo bitmap decoder; ICO is recognized so a caller
// can reject it explicitly.
func SniffImageMIME(bytes []byte) string {
	if len(bytes) >= 8 && string(bytes[:8]) == "\x89PNG\r\n\x1a\n" {
		return "image/png"
	}
	if len(bytes) >= 3 && bytes[0] == 0xff && bytes[1] == 0xd8 && bytes[2] == 0xff {
		return "image/jpeg"
	}
	if len(bytes) >= 6 && (string(bytes[:6]) == "GIF87a" || string(bytes[:6]) == "GIF89a") {
		return "image/gif"
	}
	if len(bytes) >= 12 && string(bytes[:4]) == "RIFF" && string(bytes[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(bytes) >= 4 && bytes[0] == 0 && bytes[1] == 0 && bytes[2] == 1 && bytes[3] == 0 {
		return "image/x-icon"
	}
	return ""
}
