// Package settings builds the singleton Settings window. ADR 0006 keeps
// Settings out of the main window's root surfaces, so it has its own window.
package settings

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"github.com/mslxl/natcatty/application/desktop"
	"github.com/mslxl/natcatty/domain/applock"
	kb "github.com/mslxl/natcatty/domain/keybindings"
	domain "github.com/mslxl/natcatty/domain/preferences"
	"github.com/mslxl/natcatty/ui/icons"
	"github.com/mslxl/natcatty/ui/kit"
	uilayout "github.com/mslxl/natcatty/ui/layout"
	"github.com/mslxl/natcatty/ui/locales"
	uitheme "github.com/mslxl/natcatty/ui/theme"
)

// Config is everything the Settings window can show. A nil state leaves its
// area with whatever other sections exist, so the App Lock-only constructor
// keeps working.
type Config struct {
	Lock        *AppLockState
	Desktop     *DesktopState
	Terminal    *TerminalState
	Themes      *ThemesState
	Preferences *PreferencesState
	// Associations owns the default opener and the per-extension list. It is
	// separate from Preferences because it has its own add/edit form.
	Associations *AssociationsState
	Shortcuts    *ShortcutsState
	Search       *SearchState
	Sync         *SyncState
	MCP          *MCPState
	// Licenses is the About and Licenses area: the GPLv3 Appropriate Legal
	// Notices surface.
	Licenses *LicensesState
}

// viewState owns the Settings window's scroll positions for as long as the
// view closure lives, so a wheel or PageDown survives the next frame.
type viewState struct {
	content ui.ScrollState
	sidebar ui.ScrollState
	layout  *viewLayout
}

// viewLayout receives the boxes of the Settings content viewport and the Sync
// page's pinned action bar, so a same-package shell test can measure that
// primary actions stay fully visible above the scrolling content.
type viewLayout struct {
	Content     ui.Handle
	SyncActions ui.Handle
	MCPActions  ui.Handle
}

// View builds the Settings window's native view with App Lock only. The app
// uses ViewWithSettings.
func View(lock *AppLockState) func(*ui.Context) {
	return ViewWithSettings(Config{Lock: lock})
}

// ViewWithSettings builds the whole Settings window: a searchable area
// sidebar, the typed areas, the desktop integration, App Lock, and keyboard
// shortcuts.
func ViewWithSettings(config Config) func(*ui.Context) {
	return viewWithSettings(config, nil)
}

// viewWithLayout is ViewWithSettings with the content viewport and the Sync
// page's pinned action bar bound to layout for the shell test.
func viewWithLayout(config Config, layout *viewLayout) func(*ui.Context) {
	return viewWithSettings(config, layout)
}

func viewWithSettings(config Config, layout *viewLayout) func(*ui.Context) {
	search := config.Search
	if search == nil {
		search = NewSearchState()
	}
	panes := &viewState{layout: layout}
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		var sources []ShortcutSource
		if config.Shortcuts != nil {
			sources = append(sources, config.Shortcuts)
		}
		index := SearchIndex(m, sources...)
		hits := FilterHits(index, *search.Query())
		search.Clamp(len(hits))

		// The window is frameless: as the original's, its header drags it
		// and its close button sits at the header row's end.
		uilayout.SettingsLayout(c, uilayout.SettingsSlots{
			Title:        m.NavSettings(),
			SidebarWidth: sidebarWidth(c, m, t),
			Sidebar:      func() { sidebar(c, config, search, panes, hits, m, t) },
			Content:      func() { settingsContent(c, config, search, panes, m, t) },
		})
	}
}

// settingsContent is the content pane beside the nav column: the reset-all
// confirmation, the chosen area, and the area's pinned action bars.
func settingsContent(c *ui.Context, config Config, search *SearchState, panes *viewState,
	m locales.Messages, t *ui.Theme) {
	if config.Preferences != nil && config.Preferences.ResettingAll() {
		ui.Column(c).FillWidth().Shrink(0).Padding(24, 24, 0, 24).Children(func() {
			confirmResetAll(c, config.Preferences, m, t)
		})
	}
	body(c, config, search, panes, m, t)
	if config.Sync != nil && search.Area() == AreaSync {
		syncActionsPinnedBar(c, config.Sync, config.Sync.Snapshot(), m, t, panes.layout)
		syncConvergentPinnedBar(c, config.Sync, config.Sync.Snapshot(), m, t)
	}
	if config.MCP != nil && search.Area() == AreaMCP {
		mcpApprovalPinnedBar(c, config.MCP, config.MCP.Snapshot(), m, t, panes.layout)
	}
}

func confirmResetAll(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	cancelled, confirmed := kit.ConfirmBar(c, m.SettingsResetAllTitle(), func() {
		ui.Text(c, m.SettingsResetAllBody()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
	}, m.SettingsCancel(), m.SettingsResetAllConfirm())
	if cancelled {
		state.CancelResetAll()
		c.Invalidate()
	}
	if confirmed {
		state.ResetAll()
		c.Invalidate()
	}
}

// searchField is the original's expanded search: a 36-high input with a
// search glyph at its start and, while a query is active, a clear glyph at
// its end. Arrows move the highlighted match and Enter opens it.
func searchField(c *ui.Context, search *SearchState, hits []SearchHit, m locales.Messages, t *ui.Theme) {
	kit.SearchField(c, search.Query(), kit.SearchOptions{
		Role:        kit.SearchSidebar,
		Placeholder: m.SettingsSearchPlaceholder(),
		Clear:       m.SettingsSearchClear(),
		ClearKey:    "settings-search-clear",
		ShowClear:   search.Filtering(),
		OnClear:     search.Clear,
	}).OnTextKey(func(k ui.TextKey) bool {
		switch k.Key {
		case ui.KeyDown:
			if search.Move(1, len(hits)) {
				c.Invalidate()
			}
			return true
		case ui.KeyUp:
			if search.Move(-1, len(hits)) {
				c.Invalidate()
			}
			return true
		case ui.KeyEnter:
			if search.OpenHit(hits) {
				c.Invalidate()
				return true
			}
			return false
		case ui.KeyEscape:
			if search.Filtering() {
				search.Clear()
				c.Invalidate()
				return true
			}
			return false
		}
		return false
	})
}

// searchResults is the original's results list under the search field: each
// match's label over its area in small muted text, the highlighted one on a
// faint primary fill, in a bordered popover box.
func searchResults(c *ui.Context, search *SearchState, hits []SearchHit, m locales.Messages, t *ui.Theme) {
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Radius(kit.M(c).Radius.Control).Border(1, t.Border).Background(t.Surface).Clip().
			Children(func() {
				if len(hits) == 0 {
					ui.Text(c, m.SettingsSearchNoResults()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted).Padding(12)
					return
				}
				for index, hit := range hits {
					hit := hit
					option := ui.ButtonBase(c).Key("settings-hit-"+string(hit.Area)+"-"+hit.Label).
						Label(hit.Label).FillWidth().MinWidth(0).Padding(8, 12).Justify(ui.Start)
					switch {
					case index == search.Selected():
						option.Background(t.Accent.Alpha(0.15))
					case option.Hovered() || option.Pressed():
						option.Background(t.Text.Alpha(0.05))
					}
					option.Children(func() {
						ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
							ui.Text(c, hit.Label).FontSize(kit.M(c).Font.Body).FontWeight(500).TextColor(t.Text).MinWidth(0)
							ui.Text(c, AreaTitle(m, hit.Area)).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).MinWidth(0)
						})
					})
					if option.Clicked() {
						search.SetArea(hit.Area)
						c.Invalidate()
					}
				}
			})
		ui.Text(c, m.SettingsSearchHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted).Padding(6, 4, 0, 4)
	})
}

// body is the content pane: the selected area's title and description over
// its sections, in a real, bounded scroll viewport the pane stretches to the
// height left under the header, so taller content scrolls instead of
// extending past the window edge.
func body(c *ui.Context, config Config, search *SearchState, panes *viewState,
	m locales.Messages, t *ui.Theme) {
	content := ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).MinWidth(0).TrackScroll(&panes.content)
	if panes.layout != nil {
		content = content.Bind(&panes.layout.Content)
	}
	content.Children(func() {
		ui.Column(c).FillWidth().MinWidth(0).Padding(24).Gap(24).Children(func() {
			area := search.Area()
			ui.Column(c).FillWidth().MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, AreaTitle(m, area)).FontSize(kit.M(c).Font.Title).FontWeight(600)
				if description := AreaDescription(m, area); description != "" {
					ui.Text(c, description).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
				}
			})
			areaContent(c, config, area, m, t)
		})
	})
}

// scrollbarGutter is the width of the overlay scroll bar's track: the thumb
// (6 DIPs when the theme sets none) plus the 6 DIPs MyGo insets it by.
// Reserving it keeps grown fields from running under the bar.
func scrollbarGutter(t *ui.Theme) float32 {
	width := t.ScrollbarWidth
	if width <= 0 {
		width = 6
	}
	return width + 6
}

// sidebarWidth is the nav column's DIP width: the original's 224, or what
// its longest area name needs on a platform whose fonts are wider, so no
// label is ellipsized. The column's and the items' padding, the icon and its
// gap, and the scroll gutter take the rest of the width from the label.
func sidebarWidth(c *ui.Context, m locales.Messages, t *ui.Theme) float32 {
	const (
		baseSidebarWidth = 224
		columnPadding    = 2 * 12
		// MeasureText and the layout both round fractional DIPs, so leave
		// one DIP of room for the last glyph.
		roundingRoom = 1
	)
	width := float32(baseSidebarWidth)
	chrome := columnPadding + kit.NavChrome(c, kit.NavSettings) + scrollbarGutter(t) + roundingRoom
	for _, area := range Areas() {
		label := AreaTitle(m, area)
		if measured, _ := c.MeasureText(0, ui.Span{Text: label, Size: 14}); measured > 0 {
			if wanted := measured + chrome; wanted > width {
				width = wanted
			}
		}
	}
	return width
}

// areaIcon is the glyph the original shows beside each Settings tab.
func areaIcon(area Area) string {
	switch area {
	case AreaAppearance:
		return icons.Palette
	case AreaTerminal:
		return icons.SquareTerminal
	case AreaTransport:
		return icons.Cpu
	case AreaLayout:
		return icons.LayoutGrid
	case AreaSFTPEditor:
		return icons.FileText
	case AreaLogs:
		return icons.ScrollText
	case AreaSystem:
		return icons.HardDrive
	case AreaSecurity:
		return icons.Shield
	case AreaSync:
		return icons.Cloud
	case AreaShortcuts:
		return icons.Keyboard
	case AreaMCP:
		return icons.Plug
	case AreaAbout:
		return icons.Info
	}
	return icons.Settings
}

// areaTint is the tint of an area's icon tile in the macOS style's sidebar,
// as System Settings colors its panes.
func areaTint(area Area) kit.Tint {
	switch area {
	case AreaAppearance:
		return kit.TintPurple
	case AreaTerminal:
		return kit.TintGraphite
	case AreaTransport:
		return kit.TintBlue
	case AreaLayout:
		return kit.TintIndigo
	case AreaSFTPEditor:
		return kit.TintTeal
	case AreaLogs:
		return kit.TintBrown
	case AreaSystem:
		return kit.TintGray
	case AreaSecurity:
		return kit.TintRed
	case AreaSync:
		return kit.TintCyan
	case AreaShortcuts:
		return kit.TintOrange
	case AreaMCP:
		return kit.TintGreen
	}
	return kit.TintGray
}

// sidebar is the original's nav column: the search at its top, then the
// areas, or the matches while a query is active. At 720x480 the entries can
// exceed the height left by the header, so the list scrolls instead of
// losing its last entry to the window edge. Resetting every setting, which
// the original keeps elsewhere, sits at the column's foot.
func sidebar(c *ui.Context, config Config, search *SearchState, panes *viewState, hits []SearchHit,
	m locales.Messages, t *ui.Theme) {
	searchField(c, search, hits, m, t)
	if search.Filtering() {
		searchResults(c, search, hits, m, t)
		return
	}
	ui.Scroll(c).FillWidth().Grow(1).MinHeight(0).PaddingEnd(scrollbarGutter(t)).
		TrackScroll(&panes.sidebar).Children(func() {
		kit.NavList(c, kit.NavSettings, func() {
			for _, area := range Areas() {
				area := area
				if kit.NavItemWith(c, kit.NavSettings, "settings-nav-"+string(area), areaIcon(area), AreaTitle(m, area),
					area == search.Area(), areaTint(area)).Clicked() {
					search.SetArea(area)
					c.Invalidate()
				}
			}
		})
	})
	if config.Preferences != nil {
		ui.Column(c).FillWidth().Shrink(0).BorderWidth(1, 0, 0, 0).
			BorderColor(t.Border).Padding(8, scrollbarGutter(t), 0, 0).Children(func() {
			if kit.NavItem(c, kit.NavSettings, "settings-reset-all", icons.RefreshCw, m.SettingsResetAll(), false).Clicked() {
				config.Preferences.AskResetAll()
				c.Invalidate()
			}
		})
	}
}

func areaContent(c *ui.Context, config Config, area Area, m locales.Messages, t *ui.Theme) {
	switch area {
	case AreaAppearance:
		appearanceArea(c, config.Preferences, config.Themes, m, t)
	case AreaTerminal:
		if config.Terminal != nil {
			terminalSection(c, config.Terminal, m, t)
			groupReset(c, m, t, m.SettingsTerminalTitle(), func() bool {
				return config.Terminal.ResetAll()
			}, true)
		}
	case AreaTransport:
		transportArea(c, config.Preferences, m, t)
	case AreaLayout:
		layoutArea(c, config.Preferences, m, t)
	case AreaSFTPEditor:
		sftpEditorArea(c, config, m, t)
	case AreaLogs:
		logsArea(c, config, m, t)
	case AreaSystem:
		if config.Desktop != nil {
			desktopSections(c, config.Desktop, m, t)
		}
	case AreaSecurity:
		securityArea(c, config, m, t)
	case AreaSync:
		syncArea(c, config, m, t)
	case AreaShortcuts:
		shortcutsArea(c, config.Shortcuts, m, t)
	case AreaMCP:
		mcpArea(c, config.MCP, m, t)
	case AreaAbout:
		licensesArea(c, config.Licenses, m, t)
	}
}

func layoutArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()
		checkboxRow(c, state, m, domain.FieldShowHostTreeSidebar, m.SettingsShowHostTreeSidebar(),
			settings.Layout.ShowHostTreeSidebar, func(s *domain.Settings, value bool) {
				s.Layout.ShowHostTreeSidebar = value
			})
		checkboxRow(c, state, m, domain.FieldShowSFTPTab, m.SettingsShowSFTPTab(),
			settings.Layout.ShowSFTPTab, func(s *domain.Settings, value bool) {
				s.Layout.ShowSFTPTab = value
			})
		checkboxRow(c, state, m, domain.FieldShowRecentHosts, m.SettingsShowRecentHosts(),
			settings.Layout.ShowRecentHosts, func(s *domain.Settings, value bool) {
				s.Layout.ShowRecentHosts = value
			})
		checkboxRow(c, state, m, domain.FieldShowOnlyUngroupedHostsInRoot,
			m.SettingsShowOnlyUngroupedHostsInRoot(), settings.Layout.ShowOnlyUngroupedHostsInRoot,
			func(s *domain.Settings, value bool) { s.Layout.ShowOnlyUngroupedHostsInRoot = value })

		clickLabels := []string{m.SettingsHostClickConnect(), m.SettingsHostClickSelect()}
		clickValues := []domain.HostClickBehavior{domain.HostClickConnect, domain.HostClickSelect}
		click := clickLabels[indexOfHostClicks(clickValues, settings.Layout.HostClickBehavior)]
		fieldRow(c, state, m, t, domain.FieldHostClickBehavior, m.SettingsHostClickBehavior(), func() {
			if ui.Select(c, &click, clickLabels).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Layout.HostClickBehavior = clickValues[indexOf(clickLabels, click)]
				})
				c.Invalidate()
			}
		})

		positions := []string{m.SettingsTabBarTop(), m.SettingsTabBarBottom()}
		values := []domain.TabBarPosition{domain.TabBarTop, domain.TabBarBottom}
		position := positions[indexOfPositions(values, settings.Layout.TabBarPosition)]
		fieldRow(c, state, m, t, domain.FieldTabBarPosition, m.SettingsTabBarPosition(), func() {
			if ui.Select(c, &position, positions).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Layout.TabBarPosition = values[indexOf(positions, position)]
				})
				c.Invalidate()
			}
		})
		checkboxRow(c, state, m, domain.FieldShellOnlyTabNumberShortcuts,
			m.SettingsShellOnlyTabNumberShortcuts(), settings.Layout.ShellOnlyTabNumberShortcuts,
			func(s *domain.Settings, value bool) { s.Layout.ShellOnlyTabNumberShortcuts = value })
		checkboxRow(c, state, m, domain.FieldShowTabNumberBadges,
			m.SettingsShowTabNumberBadges(), settings.Layout.ShowTabNumberBadges,
			func(s *domain.Settings, value bool) { s.Layout.ShowTabNumberBadges = value })
		checkboxRow(c, state, m, domain.FieldRestorePreviousSession,
			m.SettingsRestorePreviousSession(), settings.Layout.RestorePreviousSession,
			func(s *domain.Settings, value bool) { s.Layout.RestorePreviousSession = value })

		landingLabels := []string{m.SettingsStartupVault(), m.SettingsStartupLast()}
		landingValues := []domain.StartupLanding{domain.StartupVault, domain.StartupLast}
		landing := landingLabels[indexOfLandings(landingValues, settings.Layout.StartupLanding)]
		fieldRow(c, state, m, t, domain.FieldStartupLanding, m.SettingsStartupLanding(), func() {
			if ui.Select(c, &landing, landingLabels).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Layout.StartupLanding = landingValues[indexOf(landingLabels, landing)]
				})
				c.Invalidate()
			}
		})

		dockLabels := []string{m.SettingsDockLeft(), m.SettingsDockRight(), m.SettingsDockBottom()}
		dockValues := []domain.SidePanelDock{domain.DockLeft, domain.DockRight, domain.DockBottom}
		dock := dockLabels[indexOfDocks(dockValues, settings.Layout.SidePanelDock)]
		fieldRow(c, state, m, t, domain.FieldSidePanelDock, m.SettingsSidePanelDock(), func() {
			if ui.Select(c, &dock, dockLabels).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Layout.SidePanelDock = dockValues[indexOf(dockLabels, dock)]
				})
				c.Invalidate()
			}
		})
		groupReset(c, m, t, m.SettingsLayoutTitle(), func() bool {
			return state.ResetGroup(domain.GroupLayout)
		}, true)
		saveProblem(c, state, m, t)
	})
}

func sftpEditorArea(c *ui.Context, config Config, m locales.Messages, t *ui.Theme) {
	state := config.Preferences
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()
		behaviors := []string{m.SettingsDoubleClickOpen(), m.SettingsDoubleClickTransfer()}
		values := []domain.DoubleClickBehavior{domain.DoubleClickOpen, domain.DoubleClickTransfer}
		behavior := behaviors[indexOfBehaviors(values, settings.SFTPEditor.DoubleClickBehavior)]
		fieldRow(c, state, m, t, domain.FieldDoubleClickBehavior, m.SettingsDoubleClickBehavior(), func() {
			if ui.Select(c, &behavior, behaviors).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.SFTPEditor.DoubleClickBehavior = values[indexOf(behaviors, behavior)]
				})
				c.Invalidate()
			}
		})
		checkboxRow(c, state, m, domain.FieldAutoSync, m.SettingsAutoSync(),
			settings.SFTPEditor.AutoSync, func(s *domain.Settings, value bool) { s.SFTPEditor.AutoSync = value })
		checkboxRow(c, state, m, domain.FieldShowHiddenFiles, m.SettingsShowHiddenFiles(),
			settings.SFTPEditor.ShowHiddenFiles, func(s *domain.Settings, value bool) { s.SFTPEditor.ShowHiddenFiles = value })
		checkboxRow(c, state, m, domain.FieldUseCompressedUpload, m.SettingsUseCompressedUpload(),
			settings.SFTPEditor.UseCompressedUpload, func(s *domain.Settings, value bool) { s.SFTPEditor.UseCompressedUpload = value })
		checkboxRow(c, state, m, domain.FieldAutoOpenSidebar, m.SettingsAutoOpenSidebar(),
			settings.SFTPEditor.AutoOpenSidebar, func(s *domain.Settings, value bool) { s.SFTPEditor.AutoOpenSidebar = value })
		checkboxRow(c, state, m, domain.FieldFollowTerminalCwd, m.SettingsFollowTerminalCwd(),
			settings.SFTPEditor.FollowTerminalCwd, func(s *domain.Settings, value bool) { s.SFTPEditor.FollowTerminalCwd = value })

		viewLabels := []string{m.SettingsViewList(), m.SettingsViewTree()}
		viewValues := []domain.ViewMode{domain.ViewList, domain.ViewTree}
		view := viewLabels[indexOfViews(viewValues, settings.SFTPEditor.DefaultViewMode)]
		fieldRow(c, state, m, t, domain.FieldDefaultViewMode, m.SettingsDefaultViewMode(), func() {
			if ui.Select(c, &view, viewLabels).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.SFTPEditor.DefaultViewMode = viewValues[indexOf(viewLabels, view)]
				})
				c.Invalidate()
			}
		})
		checkboxRow(c, state, m, domain.FieldEditorWordWrap, m.SettingsEditorWordWrap(),
			settings.SFTPEditor.EditorWordWrap, func(s *domain.Settings, value bool) { s.SFTPEditor.EditorWordWrap = value })
		editorSize := settings.SFTPEditor.EditorFontSize
		fieldRow(c, state, m, t, domain.FieldEditorFontSize, m.SettingsEditorFontSize(), func() {
			if ui.NumberInput(c, &editorSize, domain.MinEditorFontSize, domain.MaxEditorFontSize, 1).Width(110).Changed() {
				state.Update(func(s *domain.Settings) { s.SFTPEditor.EditorFontSize = editorSize })
				c.Invalidate()
			}
		})
		associationsSection(c, state, config.Associations, m, t)
		groupReset(c, m, t, m.SettingsSFTPEditorTitle(), func() bool {
			return state.ResetGroup(domain.GroupSFTPEditor)
		}, true)
		saveProblem(c, state, m, t)
	})
}

// logsArea holds the Logs settings, including the command capture controls.
// The capture hook decides what Command History can record, so its switch
// lives beside the other logging controls rather than on the Terminal page.
func logsArea(c *ui.Context, config Config, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state := config.Preferences; state != nil {
			enabled := state.Settings().Logs.SSHDebug
			checkboxRow(c, state, m, domain.FieldSSHDebug, m.SettingsSSHDebug(), enabled,
				func(s *domain.Settings, value bool) { s.Logs.SSHDebug = value })
			groupReset(c, m, t, m.SettingsLogsTitle(), func() bool {
				return state.ResetGroup(domain.GroupLogs)
			}, true)
			saveProblem(c, state, m, t)
		}
		if config.Terminal != nil {
			renderHistoryCaptureSection(c, config.Terminal, config.Terminal.Settings(), m, t)
		}
	})
}

func syncArea(c *ui.Context, config Config, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if config.Sync != nil {
			syncSection(c, config.Sync, m, t)
		}
	})
}

func securityArea(c *ui.Context, config Config, m locales.Messages, t *ui.Theme) {
	if config.Preferences != nil {
		state := config.Preferences
		sectionBody(c, t, func() {
			settings := state.Settings()
			confirmClose := settings.Security.ConfirmCloseSessions
			checkboxRow(c, state, m, domain.FieldConfirmCloseSessions, m.SettingsConfirmCloseSessions(),
				confirmClose, func(s *domain.Settings, value bool) { s.Security.ConfirmCloseSessions = value })
			confirmDestructive := settings.Security.ConfirmDestructive
			checkboxRow(c, state, m, domain.FieldConfirmDestructive, m.SettingsConfirmDestructive(),
				confirmDestructive, func(s *domain.Settings, value bool) { s.Security.ConfirmDestructive = value })
			groupReset(c, m, t, m.SettingsSecurityTitle(), func() bool {
				return state.ResetGroup(domain.GroupSecurity)
			}, true)
			saveProblem(c, state, m, t)
		})
	}
	if config.Lock != nil {
		appLockSection(c, config.Lock, m, t)
	}
}

func shortcutsArea(c *ui.Context, state *ShortcutsState, m locales.Messages, t *ui.Theme) {
	if state == nil {
		return
	}
	sectionBody(c, t, func() {
		schemes := []string{m.SettingsSchemeDisabled(), m.SettingsSchemeMac(), m.SettingsSchemePC()}
		values := []kb.Scheme{kb.SchemeDisabled, kb.SchemeMac, kb.SchemePC}
		scheme := schemes[indexOfScheme(values, state.Scheme())]
		kit.FormRow(c, m.SettingsShortcutScheme(), func() {
			if ui.Select(c, &scheme, schemes).Width(190).Changed() {
				state.SetScheme(values[indexOf(schemes, scheme)])
				c.Invalidate()
			}
		})
		ui.Text(c, m.SettingsShortcutSchemeDescription()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)

		if pending, ok := state.Pending(); ok {
			pendingConflict(c, state, pending, m, t)
		}
		conflicts := state.Conflicts()
		if len(conflicts) > 0 {
			ui.Text(c, m.SettingsShortcutConflictTitle()).Bold().TextColor(uitheme.WarningText(t))
			for _, conflict := range conflicts {
				names := make([]string, 0, len(conflict.Bindings))
				for _, binding := range conflict.Bindings {
					names = append(names, bindingLabel(m, binding))
				}
				for _, menu := range conflict.Menu {
					names = append(names, conflictMenuName(m, menu))
				}
				ui.Text(c, m.SettingsShortcutConflictBody(conflict.Chord, joinNames(names))).
					FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.WarningText(t))
			}
		}

		ui.Row(c).Gap(8).Children(func() {
			if kit.SecondaryButton(c, "", m.SettingsShortcutResetAll()).Clicked() {
				state.AskResetAll()
				c.Invalidate()
			}
		})
		if state.ResettingAll() {
			// Shortcut overrides are their own document, so this confirmation
			// is worded apart from the preferences reset.
			ui.Column(c).Padding(10).Gap(6).Background(t.Surface).Border(1, t.Danger).Children(func() {
				ui.Text(c, m.SettingsShortcutResetAllTitle()).Bold()
				ui.Text(c, m.SettingsShortcutResetAllBody()).FontSize(kit.M(c).Font.Footnote).TextColor(t.TextMuted)
				ui.Row(c).Gap(8).Children(func() {
					if kit.SecondaryButton(c, "", m.SettingsShortcutResetAllConfirm()).Clicked() {
						state.ResetAll()
						c.Invalidate()
					}
					if kit.SecondaryButton(c, "", m.SettingsCancel()).Clicked() {
						state.CancelResetAll()
						c.Invalidate()
					}
				})
			})
		}
		ui.Text(c, m.SettingsShortcutRecordHint()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)
		// MyGo v0.5.5 applies terminal keybinds when it constructs the
		// terminal and exposes no setter, so the limitation is stated rather
		// than silently surprising the user.
		ui.Text(c, m.SettingsShortcutTerminalNote()).FontSize(kit.M(c).Font.Caption).TextColor(t.TextMuted)

		for _, category := range kb.Categories() {
			bindings := make([]kb.Binding, 0)
			for _, binding := range state.Catalog() {
				if binding.Category == category {
					bindings = append(bindings, binding)
				}
			}
			if len(bindings) == 0 {
				continue
			}
			ui.Text(c, categoryLabel(m, category)).Bold()
			for _, binding := range bindings {
				bindingRow(c, state, binding, m, t)
			}
		}
		if state.Failed() {
			ui.Text(c, m.SettingsSaveFailed()).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
		}
	})
}

func pendingConflict(c *ui.Context, state *ShortcutsState, pending PendingConflict,
	m locales.Messages, t *ui.Theme) {
	names := make([]string, 0, len(pending.Conflicts))
	for _, conflict := range pending.Conflicts {
		for _, binding := range conflict.Bindings {
			if binding.ID == pending.BindingID {
				continue
			}
			names = append(names, bindingLabel(m, binding))
		}
		for _, menu := range conflict.Menu {
			names = append(names, conflictMenuName(m, menu))
		}
	}
	ui.Column(c).Padding(10).Gap(6).Background(t.Surface).Border(1, t.Warning).Children(func() {
		ui.Text(c, m.SettingsShortcutConflictTitle()).Bold().TextColor(uitheme.WarningText(t))
		ui.Text(c, m.SettingsShortcutConflictBody(pending.Chord, joinNames(names))).
			FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.WarningText(t))
		ui.Row(c).Gap(8).Children(func() {
			if kit.PrimaryButton(c, "", m.SettingsShortcutConflictReplace()).Clicked() {
				state.Replace()
				c.Invalidate()
			}
			if kit.SecondaryButton(c, "", m.SettingsShortcutConflictCancel()).Clicked() {
				state.CancelConflict()
				c.Invalidate()
			}
		})
	})
}

func bindingRow(c *ui.Context, state *ShortcutsState, binding kb.Binding,
	m locales.Messages, t *ui.Theme) {
	label := bindingLabel(m, binding)
	recording := state.Recording() == binding.ID
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(280).Children(func() {
			ui.Text(c, label).FontSize(kit.M(c).Font.Callout)
			if !state.Available(binding.ID) {
				ui.Text(c, m.SettingsShortcutUnavailable()).FontSize(kit.M(c).Font.Caption2).TextColor(uitheme.WarningText(t))
			}
		})
		chord := state.Chord(binding.ID)
		if recording {
			chord = m.SettingsShortcutRecording()
		} else if chord == kb.Disabled {
			chord = m.SettingsShortcutDisabled()
		}
		ui.TextInput(c, &chord).Width(190).ReadOnly(true).
			Label(label + " " + m.SettingsShortcutScheme()).
			OnClick(func() {
				state.BeginRecording(binding.ID)
				c.Invalidate()
			}).
			OnTextKey(func(k ui.TextKey) bool {
				if state.Recording() != binding.ID {
					// A keyboard-only user reaches the field with Tab; let Tab
					// keep moving focus, and let the next chord start recording
					// without a pointer.
					if k.Mods == 0 && (k.Key == ui.KeyTab ||
						k.Key >= ui.KeyA && k.Key <= ui.KeyZ) {
						return false
					}
					state.BeginRecording(binding.ID)
				}
				state.RecordKey(binding.ID, k.Key, k.Mods)
				c.Invalidate()
				return true
			})
		if state.IsOverridden(binding.ID) {
			if kit.GhostButton(c, "", m.SettingsShortcutReset()).Clicked() {
				state.ResetBinding(binding.ID)
				c.Invalidate()
			}
		}
	})
}

// fieldRow places a localized label above a preference control and, when the
// field differs from its default, a reset control beside it.
func fieldRow(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	field domain.Field, label string, build func()) {
	kit.FormRowWith(c, label, build, func() { resetField(c, state, m, field) })
}

// checkboxRow is a preference's check box, with the per-field reset beside
// it.
func checkboxRow(c *ui.Context, state *PreferencesState, m locales.Messages,
	field domain.Field, label string, value bool, set func(*domain.Settings, bool)) {
	enabled := value
	if kit.FormToggle(c, label, &enabled, func() { resetField(c, state, m, field) }) {
		state.Update(func(s *domain.Settings) { set(s, enabled) })
		c.Invalidate()
	}
}

// resetField offers the per-field reset beside a control once the field
// differs from its default.
func resetField(c *ui.Context, state *PreferencesState, m locales.Messages, field domain.Field) {
	if state.IsDefault(field) {
		return
	}
	if kit.GhostButton(c, icons.RefreshCw, m.SettingsReset()).Clicked() {
		state.ResetField(field)
		c.Invalidate()
	}
}

// conflictMenuName names the menu command a chord collides with. The label
// comes from the live menu, so it is already in the window's language; the
// generic fallback covers a menu item built without one.
func conflictMenuName(m locales.Messages, menu kb.MenuShortcut) string {
	if menu.Label == "" {
		return m.SettingsShortcutConflictMenu()
	}
	return m.SettingsShortcutConflictMenuItem(menu.Label)
}

func groupReset(c *ui.Context, m locales.Messages, t *ui.Theme, group string,
	reset func() bool, visible bool) {
	if !visible {
		return
	}
	if kit.GhostButton(c, icons.RefreshCw, m.SettingsResetGroup(group)).AlignSelf(ui.Start).Clicked() {
		if reset() {
			c.Invalidate()
		}
	}
}

func saveProblem(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	if state.Failed() {
		ui.Text(c, m.SettingsSaveFailed()).FontSize(kit.M(c).Font.Footnote).TextColor(uitheme.DangerText(t))
	}
}

func joinNames(names []string) string {
	seen := make([]string, 0, len(names))
	for _, name := range names {
		if name == "" || slices.Contains(seen, name) {
			continue
		}
		seen = append(seen, name)
	}
	if len(seen) == 0 {
		return ""
	}
	out := seen[0]
	for _, name := range seen[1:] {
		out += ", " + name
	}
	return out
}

func indexOfModes(values []domain.ThemeMode, value domain.ThemeMode) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfPositions(values []domain.TabBarPosition, value domain.TabBarPosition) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfLandings(values []domain.StartupLanding, value domain.StartupLanding) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfDocks(values []domain.SidePanelDock, value domain.SidePanelDock) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfBehaviors(values []domain.DoubleClickBehavior, value domain.DoubleClickBehavior) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfHostClicks(values []domain.HostClickBehavior, value domain.HostClickBehavior) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfViews(values []domain.ViewMode, value domain.ViewMode) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfScheme(values []kb.Scheme, value kb.Scheme) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func desktopSections(c *ui.Context, state *DesktopState, m locales.Messages, t *ui.Theme) {
	caps := state.Capabilities()
	settings := state.Settings()
	autostartSection(c, state, settings, caps, m, t)
	hotkeySection(c, state, settings, caps, m, t)
	traySection(c, state, settings, caps, m, t)
	deepLinkSection(c, state, settings, m, t)
	notificationSection(c, state, settings, caps, m, t)
	desktopProblem(c, state, m, t)
}

func autostartSection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsAutostartTitle(), m.SettingsAutostartDescription(), t, func() {
		if !caps.AutoStart {
			ui.Text(c, m.SettingsAutostartUnsupported()).TextColor(t.TextMuted)
			return
		}
		enabled := settings.AutoStart
		if ui.Checkbox(c, &enabled, m.SettingsAutostartEnabled()).Changed() {
			state.SetAutoStart(enabled)
			c.Invalidate()
		}
	})
}

func hotkeySection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsHotkeyTitle(), m.SettingsHotkeyDescription(), t, func() {
		if !caps.GlobalHotkey {
			ui.Text(c, m.SettingsHotkeyUnsupported()).TextColor(t.TextMuted)
			return
		}
		enabled := settings.GlobalHotkeyEnabled
		if ui.Checkbox(c, &enabled, m.SettingsHotkeyEnabled()).Changed() {
			state.SetGlobalHotkeyEnabled(enabled)
			c.Invalidate()
		}
		kit.FormRow(c, m.SettingsHotkeyAccelerator(), func() {
			ui.TextInput(c, state.Hotkey()).Width(220)
		})
		ui.Row(c).Gap(8).Children(func() {
			if kit.PrimaryButton(c, "", m.SettingsHotkeyApply()).Clicked() {
				state.ApplyHotkey()
				c.Invalidate()
			}
			if kit.SecondaryButton(c, "", m.SettingsHotkeyReset()).Clicked() {
				state.ResetHotkey()
				c.Invalidate()
			}
		})
		if state.Problem() == DesktopProblemHotkey {
			ui.Text(c, m.SettingsHotkeyProblem()).TextColor(uitheme.DangerText(t))
		}
	})
}

func traySection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsTrayTitle(), m.SettingsTrayDescription(), t, func() {
		if !caps.Tray {
			ui.Text(c, m.SettingsTrayUnsupported()).TextColor(t.TextMuted)
			return
		}
		show := settings.ShowTrayIcon
		if ui.Checkbox(c, &show, m.SettingsShowTrayIcon()).Changed() {
			state.SetShowTrayIcon(show)
			c.Invalidate()
		}
		closeToTray := settings.CloseToTray
		if ui.Checkbox(c, &closeToTray, m.SettingsCloseToTray()).Changed() {
			state.SetCloseToTray(closeToTray)
			c.Invalidate()
		}
	})
}

func deepLinkSection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsDeepLinksTitle(), m.SettingsDeepLinksDescription(), t, func() {
		enabled := settings.SSHDeepLinks
		if ui.Checkbox(c, &enabled, m.SettingsSSHDeepLinks()).Changed() {
			state.SetSSHDeepLinks(enabled)
			c.Invalidate()
		}
	})
}

func notificationSection(c *ui.Context, state *DesktopState, settings desktop.Settings,
	caps desktop.Capabilities, m locales.Messages, t *ui.Theme) {
	section(c, m.SettingsNotificationsTitle(), m.SettingsNotificationsDescription(), t, func() {
		if !caps.Notifications {
			ui.Text(c, m.SettingsNotificationsUnsupported()).TextColor(t.TextMuted)
			return
		}
		notificationToggle(c, state, settings.NotifySessionEnded, m.SettingsNotifySessionEnded(),
			desktop.EventSessionEnded)
		notificationToggle(c, state, settings.NotifyConnectionFailed, m.SettingsNotifyConnectionFailed(),
			desktop.EventConnectionFailed)
		notificationToggle(c, state, settings.NotifyForwardingFailed, m.SettingsNotifyForwardingFailed(),
			desktop.EventForwardingFailed)
	})
}

func notificationToggle(c *ui.Context, state *DesktopState, value bool, label string, event desktop.Event) {
	enabled := value
	if ui.Checkbox(c, &enabled, label).Changed() {
		state.SetNotification(event, enabled)
		c.Invalidate()
	}
}

// section is a titled card inside an area. Its title sits a step below the
// area's own heading, so the page reads as one hierarchy.
func section(c *ui.Context, title, description string, t *ui.Theme, build func()) {
	kit.FormCard(c, title, description, build)
}

// sectionBody is a section whose title and description the area header
// already shows.
func sectionBody(c *ui.Context, t *ui.Theme, build func()) {
	kit.FormCard(c, "", "", build)
}

func appLockSection(c *ui.Context, lock *AppLockState, m locales.Messages, t *ui.Theme) {
	snapshot := lock.Snapshot()

	section(c, m.SettingsAppLockTitle(), m.SettingsAppLockDescription(), t, func() {
		if !snapshot.Enabled {
			enableSection(c, lock, m)
			return
		}

		ui.Text(c, m.SettingsAppLockEnabled()).Bold()
		timeoutField(c, lock, snapshot.TimeoutMinutes, m)
		kit.FormRow(c, m.SettingsAppLockCurrentPassword(), func() {
			ui.TextInput(c, lock.CurrentPassword()).Password().Width(240)
		})
		ui.Row(c).Gap(8).Children(func() {
			if kit.SecondaryButton(c, "", m.SettingsAppLockDisable()).Clicked() {
				lock.Disable()
				c.Invalidate()
			}
			if kit.SecondaryButton(c, "", m.SettingsAppLockLockNow()).Clicked() {
				lock.LockNow()
				c.Invalidate()
			}
		})
		appLockProblem(c, lock, t)
	})
}

func enableSection(c *ui.Context, lock *AppLockState, m locales.Messages) {
	ui.Text(c, m.SettingsAppLockDisabled()).TextColor(c.Theme().TextMuted)
	kit.FormRow(c, m.SettingsAppLockPassword(), func() {
		ui.TextInput(c, lock.Password()).Password().Width(240)
	})
	kit.FormRow(c, m.SettingsAppLockConfirm(), func() {
		ui.TextInput(c, lock.Confirmation()).Password().Width(240)
	})
	if kit.PrimaryButton(c, "", m.SettingsAppLockEnable()).AlignSelf(ui.Start).Clicked() {
		lock.Enable()
		c.Invalidate()
	}
	appLockProblem(c, lock, c.Theme())
}

func timeoutField(c *ui.Context, lock *AppLockState, current int, m locales.Messages) {
	options := applock.TimeoutOptions
	labels := make([]string, len(options))
	for i, minutes := range options {
		labels[i] = timeoutLabel(m, minutes)
	}
	selected := timeoutLabel(m, current)
	kit.FormRow(c, m.SettingsAppLockTimeout(), func() {
		if ui.Select(c, &selected, labels).Width(180).Changed() {
			if index := slices.Index(labels, selected); index >= 0 {
				lock.SetTimeout(options[index])
				c.Invalidate()
			}
		}
	})
}

func timeoutLabel(m locales.Messages, minutes int) string {
	if minutes == 0 {
		return m.SettingsAppLockTimeoutNever()
	}
	return m.SettingsAppLockTimeoutMinutes(minutes)
}

func appLockProblem(c *ui.Context, lock *AppLockState, t *ui.Theme) {
	message := problemText(locales.In(c.Locale()), lock.Problem())
	if message == "" {
		return
	}
	ui.Text(c, message).TextColor(uitheme.DangerText(t))
}

func desktopProblem(c *ui.Context, state *DesktopState, m locales.Messages, t *ui.Theme) {
	var message string
	switch state.Problem() {
	case DesktopProblemSave:
		message = m.SettingsDesktopSaveFailed()
	case DesktopProblemApply:
		message = m.SettingsDesktopUpdateFailed()
	}
	if message != "" {
		ui.Text(c, message).TextColor(uitheme.DangerText(t))
	}
}

func problemText(m locales.Messages, problem AppLockProblem) string {
	switch problem {
	case AppLockProblemEmpty:
		return m.SettingsAppLockProblemEmpty()
	case AppLockProblemMismatch:
		return m.SettingsAppLockProblemMismatch()
	case AppLockProblemIncorrect:
		return m.SettingsAppLockProblemIncorrect()
	case AppLockProblemFailed:
		return m.SettingsAppLockProblemFailed()
	}
	return ""
}
