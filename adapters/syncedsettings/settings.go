// Package syncedsettings adapts the device-local settings services to the
// sync payload's flat "settings" object. Only portable documents travel:
// filesystem paths, platform app choices, and the cloud-sync provider profile
// stay on the device that owns them.
package syncedsettings

import (
	"context"
	"encoding/json"
	"fmt"

	appbrowser "github.com/mslxl/natcatty/application/browser"
	appkeybindings "github.com/mslxl/natcatty/application/keybindings"
	apppreferences "github.com/mslxl/natcatty/application/preferences"
	appterminalsettings "github.com/mslxl/natcatty/application/terminalsettings"
	appthemes "github.com/mslxl/natcatty/application/themes"
	domainbrowser "github.com/mslxl/natcatty/domain/browser"
	domainkeybindings "github.com/mslxl/natcatty/domain/keybindings"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
	domainpreferences "github.com/mslxl/natcatty/domain/preferences"
	domainterminal "github.com/mslxl/natcatty/domain/terminal"
	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// The payload's settings object is shared with the original app, so every key
// this build owns is namespaced. An older reader carries unknown keys through
// its deep merge instead of dropping them.
const (
	KeyPreferences      = "natcatty.preferences"
	KeyTerminal         = "natcatty.terminal"
	KeyKeybindings      = "natcatty.keybindings"
	KeyThemes           = "natcatty.themes"
	KeySFTPBookmarks    = "natcatty.sftpBookmarks"
	KeyBrowserBookmarks = "natcatty.browserBookmarks"
)

// SFTPBookmarks is the global remote bookmark repository. Local-pane
// bookmarks are deliberately not part of this port.
type SFTPBookmarks interface {
	LoadBookmarks(ctx context.Context) ([]domainsftp.Bookmark, error)
	SaveBookmarks(ctx context.Context, items []domainsftp.Bookmark) error
}

// Settings implements the syncvault settings port over the live services.
type Settings struct {
	preferences *apppreferences.Service
	terminal    *appterminalsettings.Service
	keybindings *appkeybindings.Service
	themes      *appthemes.Service
	sftp        SFTPBookmarks
	browser     *appbrowser.Bookmarks
}

// New returns a settings adapter. A nil service simply omits that document.
func New(
	preferences *apppreferences.Service,
	terminal *appterminalsettings.Service,
	keybindings *appkeybindings.Service,
	themes *appthemes.Service,
	sftp SFTPBookmarks,
	browser *appbrowser.Bookmarks,
) *Settings {
	return &Settings{
		preferences: preferences,
		terminal:    terminal,
		keybindings: keybindings,
		themes:      themes,
		sftp:        sftp,
		browser:     browser,
	}
}

type keybindingDocument struct {
	Overrides       domainkeybindings.Overrides `json:"overrides,omitempty"`
	SuppressedMenus []string                    `json:"suppressedMenus,omitempty"`
}

// Snapshot returns every portable document. Empty collections are present so
// a deletion on one device propagates to the others.
func (s *Settings) Snapshot(ctx context.Context) (map[string]json.RawMessage, error) {
	documents := map[string]json.RawMessage{}
	put := func(key string, value any) error {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("syncedsettings: encode %s: %w", key, err)
		}
		documents[key] = encoded
		return nil
	}
	if s.preferences != nil {
		if err := put(KeyPreferences, domainpreferences.Portable(s.preferences.Settings())); err != nil {
			return nil, err
		}
	}
	if s.terminal != nil {
		if err := put(KeyTerminal, domainterminal.Portable(s.terminal.Settings())); err != nil {
			return nil, err
		}
	}
	if s.keybindings != nil {
		state := s.keybindings.State()
		if err := put(KeyKeybindings, keybindingDocument{
			Overrides:       state.Overrides,
			SuppressedMenus: state.SuppressedMenus,
		}); err != nil {
			return nil, err
		}
	}
	if s.themes != nil {
		themes := s.themes.CustomThemes()
		if themes == nil {
			themes = []domaintheme.TerminalTheme{}
		}
		if err := put(KeyThemes, themes); err != nil {
			return nil, err
		}
	}
	if s.sftp != nil {
		items, err := s.sftp.LoadBookmarks(ctx)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []domainsftp.Bookmark{}
		}
		if err := put(KeySFTPBookmarks, items); err != nil {
			return nil, err
		}
	}
	if s.browser != nil {
		items := s.browser.All()
		if items == nil {
			items = []domainbrowser.Bookmark{}
		}
		if err := put(KeyBrowserBookmarks, items); err != nil {
			return nil, err
		}
	}
	return documents, nil
}

type applyPlan struct {
	preferences *domainpreferences.PortableSettings
	terminal    *domainterminal.Settings
	keybindings *keybindingDocument
	themes      []domaintheme.TerminalTheme
	themesSet   bool
	sftp        []domainsftp.Bookmark
	sftpSet     bool
	browser     []domainbrowser.Bookmark
	browserSet  bool
}

// Apply validates every document before writing any of them. A malformed
// remote document therefore rejects the download instead of leaving half of
// the device on the cloud copy.
func (s *Settings) Apply(ctx context.Context, documents map[string]json.RawMessage) error {
	var plan applyPlan
	if raw, ok := documents[KeyPreferences]; ok && s.preferences != nil {
		var portable domainpreferences.PortableSettings
		if err := json.Unmarshal(raw, &portable); err != nil {
			return fmt.Errorf("syncedsettings: decode preferences: %w", err)
		}
		plan.preferences = &portable
	}
	if raw, ok := documents[KeyTerminal]; ok && s.terminal != nil {
		settings, err := decodeTerminalSettings(raw)
		if err != nil {
			return fmt.Errorf("syncedsettings: decode terminal settings: %w", err)
		}
		plan.terminal = &settings
	}
	if raw, ok := documents[KeyKeybindings]; ok && s.keybindings != nil {
		var document keybindingDocument
		if err := json.Unmarshal(raw, &document); err != nil {
			return fmt.Errorf("syncedsettings: decode key bindings: %w", err)
		}
		plan.keybindings = &document
	}
	if raw, ok := documents[KeyThemes]; ok && s.themes != nil {
		if err := decodeArray(raw, &plan.themes); err != nil {
			return fmt.Errorf("syncedsettings: decode themes: %w", err)
		}
		plan.themesSet = true
	}
	if raw, ok := documents[KeySFTPBookmarks]; ok && s.sftp != nil {
		if err := decodeArray(raw, &plan.sftp); err != nil {
			return fmt.Errorf("syncedsettings: decode SFTP bookmarks: %w", err)
		}
		plan.sftpSet = true
	}
	if raw, ok := documents[KeyBrowserBookmarks]; ok && s.browser != nil {
		if err := decodeArray(raw, &plan.browser); err != nil {
			return fmt.Errorf("syncedsettings: decode browser bookmarks: %w", err)
		}
		plan.browserSet = true
	}

	if plan.preferences != nil {
		next := domainpreferences.MergePortable(s.preferences.Settings(), *plan.preferences)
		if err := s.preferences.Replace(next); err != nil {
			return fmt.Errorf("syncedsettings: store preferences: %w", err)
		}
	}
	if plan.terminal != nil {
		next := domainterminal.MergePortable(s.terminal.Settings(), *plan.terminal)
		if err := s.terminal.Replace(next); err != nil {
			return fmt.Errorf("syncedsettings: store terminal settings: %w", err)
		}
	}
	if plan.keybindings != nil {
		current := s.keybindings.State()
		if err := s.keybindings.Replace(appkeybindings.State{
			Scheme:          current.Scheme,
			Overrides:       plan.keybindings.Overrides,
			SuppressedMenus: plan.keybindings.SuppressedMenus,
		}); err != nil {
			return fmt.Errorf("syncedsettings: store key bindings: %w", err)
		}
	}
	if plan.themesSet {
		if err := s.themes.ReplaceCustom(plan.themes); err != nil {
			return fmt.Errorf("syncedsettings: store themes: %w", err)
		}
	}
	if plan.sftpSet {
		set := domainsftp.NewBookmarkSet(plan.sftp)
		if err := s.sftp.SaveBookmarks(ctx, set.Items()); err != nil {
			return fmt.Errorf("syncedsettings: store SFTP bookmarks: %w", err)
		}
	}
	if plan.browserSet {
		if err := s.browser.Replace(plan.browser); err != nil {
			return fmt.Errorf("syncedsettings: store browser bookmarks: %w", err)
		}
	}
	return nil
}

// decodeTerminalSettings decodes a synced terminal document. Documents
// written before the capture hook switch omit its key; a plain decode would
// read that as false and let a download turn a portable on-by-default setting
// off, so the field is defaulted to on only when absent.
func decodeTerminalSettings(raw json.RawMessage) (domainterminal.Settings, error) {
	var settings domainterminal.Settings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return domainterminal.Settings{}, err
	}
	var presence struct {
		HistoryCaptureHookEnabled *bool `json:"historyCaptureHookEnabled"`
	}
	if err := json.Unmarshal(raw, &presence); err != nil {
		return domainterminal.Settings{}, err
	}
	if presence.HistoryCaptureHookEnabled == nil {
		settings.HistoryCaptureHookEnabled = true
	}
	return settings, nil
}

func decodeArray[T any](raw json.RawMessage, target *[]T) error {
	if string(raw) == "null" {
		*target = nil
		return nil
	}
	return json.Unmarshal(raw, target)
}
