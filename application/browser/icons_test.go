package browser

import (
	"fmt"
	"testing"

	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	"github.com/mslxl/natcatty/domain/model"
)

const testPNGDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAFgwJ/lZ1cvwAAAABJRU5ErkJggg=="

type memoryIconStore struct {
	items   []domainbrowser.SiteIcon
	saves   int
	loadErr error
}

func (m *memoryIconStore) Load() ([]domainbrowser.SiteIcon, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	return append([]domainbrowser.SiteIcon(nil), m.items...), nil
}

func (m *memoryIconStore) Save(items []domainbrowser.SiteIcon) error {
	m.saves++
	m.items = append([]domainbrowser.SiteIcon(nil), items...)
	return nil
}

func TestSiteIconStoreReadErrorBlocksSaving(t *testing.T) {
	store := &memoryIconStore{loadErr: fmt.Errorf("access denied")}
	icons, err := NewSiteIcons(store)
	if err != nil {
		t.Fatalf("NewSiteIcons: %v", err)
	}
	if _, _, err := icons.SetAuto("host-a", "https://example.com/", testPNGDataURL); err == nil {
		t.Fatal("SetAuto overwrote an icon cache that could not be read")
	}
	if store.saves != 0 {
		t.Fatalf("Save calls = %d, want 0", store.saves)
	}
}

func TestSiteIconsKeyByHostAndOrigin(t *testing.T) {
	store := &memoryIconStore{}
	icons, err := NewSiteIcons(store)
	if err != nil {
		t.Fatalf("NewSiteIcons: %v", err)
	}
	icon, ok, err := icons.SetAuto("host-a", "https://example.com/console/login", testPNGDataURL)
	if err != nil || !ok {
		t.Fatalf("SetAuto = %+v, %v, %v", icon, ok, err)
	}
	if icon.Origin != "https://example.com" || icon.Mode != domainbrowser.IconModeAuto {
		t.Fatalf("icon = %+v", icon)
	}
	// Any page on the same Host and origin shares the icon.
	got, found := icons.Get("host-a", "https://example.com/other")
	if !found || got.DataURL != testPNGDataURL {
		t.Fatalf("Get same origin = %+v, %v", got, found)
	}
	if _, found := icons.Get("host-b", "https://example.com/"); found {
		t.Fatal("another Host saw the icon")
	}
	if _, found := icons.Get("host-a", "https://other.example/"); found {
		t.Fatal("another origin saw the icon")
	}
}

func TestSiteIconsCustomProtectedAndInvalidRejected(t *testing.T) {
	store := &memoryIconStore{}
	icons, _ := NewSiteIcons(store)
	if _, ok, err := icons.SetCustom("host-a", "https://example.com/", testPNGDataURL); err != nil || !ok {
		t.Fatalf("SetCustom = %v, %v", ok, err)
	}
	if _, ok, err := icons.SetAuto("host-a", "https://example.com/page", testPNGDataURL); err != nil || ok {
		t.Fatalf("SetAuto over custom = %v, %v; want protected", ok, err)
	}
	if _, ok, err := icons.SetAuto("host-a", "https://other.example/", "data:text/html;base64,PGgxPg=="); err != nil || ok {
		t.Fatalf("invalid icon accepted: %v, %v", ok, err)
	}
	if store.saves != 1 {
		t.Fatalf("saves = %d, want 1", store.saves)
	}
}

func TestSiteIconsLimitAndPersistence(t *testing.T) {
	store := &memoryIconStore{}
	first, _ := NewSiteIcons(store)
	for i := 0; i < domainbrowser.MaxSiteIcons; i++ {
		host := model.HostID(fmt.Sprintf("host-%d", i))
		origin := fmt.Sprintf("https://h%d.example/", i)
		if _, ok, err := first.SetAuto(host, origin, testPNGDataURL); err != nil || !ok {
			t.Fatalf("SetAuto %d = %v, %v", i, ok, err)
		}
	}
	if _, ok, err := first.SetAuto("overflow", "https://overflow.example/", testPNGDataURL); err != nil || ok {
		t.Fatalf("SetAuto over limit = %v, %v; want false, nil", ok, err)
	}
	second, _ := NewSiteIcons(store)
	if len(second.All()) != domainbrowser.MaxSiteIcons {
		t.Fatalf("reloaded icons = %d", len(second.All()))
	}
}
