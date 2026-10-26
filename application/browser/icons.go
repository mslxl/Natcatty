package browser

import (
	"errors"
	"fmt"
	"sync"
	"time"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

// SiteIconStore persists the favicon cache. It is consumer-owned by this
// context; the file adapter supplies the encoding.
type SiteIconStore interface {
	Load() ([]domainbrowser.SiteIcon, error)
	Save([]domainbrowser.SiteIcon) error
}

// SiteIcons caches one favicon per Host and origin. Automatic fetches never
// overwrite a custom icon, matching the original site icon store.
type SiteIcons struct {
	mu      sync.Mutex
	store   SiteIconStore
	items   []domainbrowser.SiteIcon
	now     func() time.Time
	loadErr error
}

// NewSiteIcons loads the cache and drops malformed entries.
func NewSiteIcons(store SiteIconStore) (*SiteIcons, error) {
	if store == nil {
		return nil, errors.New("browser: site icon store is required")
	}
	raw, err := store.Load()
	icons := &SiteIcons{store: store, now: time.Now, loadErr: err}
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		if item.HostID == "" || item.Origin == "" || !validIconMode(item.Mode) {
			continue
		}
		if item.Mode == domainbrowser.IconModeAuto && !domainbrowser.ValidImageDataURL(item.DataURL) {
			continue
		}
		key := iconKey(item.HostID, item.Origin)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		icons.items = append(icons.items, item)
	}
	if len(icons.items) > domainbrowser.MaxSiteIcons {
		icons.items = icons.items[:domainbrowser.MaxSiteIcons]
	}
	return icons, nil
}

func validIconMode(mode domainbrowser.IconMode) bool {
	return mode == "" || mode == domainbrowser.IconModeAuto || mode == domainbrowser.IconModeCustom
}

func iconKey(hostID model.HostID, origin string) string {
	return string(hostID) + "\n" + origin
}

// saveErrorLocked reports why persistence is unavailable. It must be called
// with s.mu held.
func (s *SiteIcons) saveErrorLocked() error {
	if s.loadErr != nil {
		return fmt.Errorf("browser: site icons could not be loaded: %w", s.loadErr)
	}
	return nil
}

// All returns a copy of the cache.
func (s *SiteIcons) All() []domainbrowser.SiteIcon {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]domainbrowser.SiteIcon(nil), s.items...)
}

// Get resolves pageURL's origin and returns the cached icon for Host hostID.
func (s *SiteIcons) Get(hostID model.HostID, pageURL string) (domainbrowser.SiteIcon, bool) {
	origin, err := domainbrowser.Origin(pageURL)
	if err != nil {
		return domainbrowser.SiteIcon{}, false
	}
	key := iconKey(hostID, origin)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		if iconKey(item.HostID, item.Origin) == key {
			return item, true
		}
	}
	return domainbrowser.SiteIcon{}, false
}

// SetAuto stores a fetched favicon. It reports false when a custom icon is
// already pinned or when the cache is full.
func (s *SiteIcons) SetAuto(hostID model.HostID, pageURL, dataURL string) (domainbrowser.SiteIcon, bool, error) {
	if hostID == "" || !domainbrowser.ValidImageDataURL(dataURL) {
		return domainbrowser.SiteIcon{}, false, nil
	}
	return s.set(hostID, pageURL, domainbrowser.IconModeAuto, dataURL, "", "")
}

// SetCustom pins a custom icon for the page's origin.
func (s *SiteIcons) SetCustom(hostID model.HostID, pageURL, dataURL string) (domainbrowser.SiteIcon, bool, error) {
	if hostID == "" || !domainbrowser.ValidImageDataURL(dataURL) {
		return domainbrowser.SiteIcon{}, false, nil
	}
	return s.set(hostID, pageURL, domainbrowser.IconModeCustom, dataURL, "", "")
}

// Reset removes one origin's icon, if present.
func (s *SiteIcons) Reset(hostID model.HostID, pageURL string) (bool, error) {
	origin, err := domainbrowser.Origin(pageURL)
	if err != nil {
		return false, err
	}
	key := iconKey(hostID, origin)
	s.mu.Lock()
	items := make([]domainbrowser.SiteIcon, 0, len(s.items))
	removed := false
	for _, item := range s.items {
		if iconKey(item.HostID, item.Origin) == key {
			removed = true
			continue
		}
		items = append(items, item)
	}
	if !removed {
		s.mu.Unlock()
		return false, nil
	}
	if err := s.saveErrorLocked(); err != nil {
		s.mu.Unlock()
		return false, err
	}
	if err := s.store.Save(items); err != nil {
		s.mu.Unlock()
		return false, err
	}
	s.items = items
	s.mu.Unlock()
	return true, nil
}

func (s *SiteIcons) set(hostID model.HostID, pageURL string, mode domainbrowser.IconMode, dataURL, glyph, color string) (domainbrowser.SiteIcon, bool, error) {
	origin, err := domainbrowser.Origin(pageURL)
	if err != nil {
		return domainbrowser.SiteIcon{}, false, err
	}
	key := iconKey(hostID, origin)
	s.mu.Lock()
	items := append([]domainbrowser.SiteIcon(nil), s.items...)
	index := -1
	for i := range items {
		if iconKey(items[i].HostID, items[i].Origin) == key {
			index = i
			break
		}
	}
	if index < 0 && len(items) >= domainbrowser.MaxSiteIcons {
		s.mu.Unlock()
		return domainbrowser.SiteIcon{}, false, nil
	}
	if index >= 0 && mode == domainbrowser.IconModeAuto && items[index].Mode == domainbrowser.IconModeCustom {
		s.mu.Unlock()
		return domainbrowser.SiteIcon{}, false, nil
	}
	next := domainbrowser.SiteIcon{
		HostID: hostID, Origin: origin, Mode: mode, DataURL: dataURL,
		Glyph: glyph, Color: color, UpdatedAt: s.now().UnixMilli(),
	}
	if index >= 0 {
		items[index] = next
	} else {
		items = append(items, next)
	}
	if err := s.saveErrorLocked(); err != nil {
		s.mu.Unlock()
		return domainbrowser.SiteIcon{}, false, err
	}
	if err := s.store.Save(items); err != nil {
		s.mu.Unlock()
		return domainbrowser.SiteIcon{}, false, err
	}
	s.items = items
	s.mu.Unlock()
	return next, true, nil
}
