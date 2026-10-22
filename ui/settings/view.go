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
	"github.com/mslxl/natcatty/ui/locales"
)

// Config is everything the Settings window can show. A nil state leaves its
// area with whatever other sections exist, so the App Lock-only constructor
// keeps working.
type Config struct {
	Lock        *AppLockState
	Desktop     *DesktopState
	Terminal    *TerminalState
	Preferences *PreferencesState
	Shortcuts   *ShortcutsState
	Search      *SearchState
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
	search := config.Search
	if search == nil {
		search = NewSearchState()
	}
	return func(c *ui.Context) {
		m := locales.In(c.Locale())
		t := c.Theme()
		index := SearchIndex(m)
		hits := FilterHits(index, *search.Query())
		search.Clamp(len(hits))

		ui.Column(c).Fill().Padding(18).Gap(12).Children(func() {
			header(c, config, search, m, t)
			ui.TextInput(c, search.Query()).
				Label(m.SettingsSearchPlaceholder()).
				Placeholder(m.SettingsSearchPlaceholder()).
				Width(360).
				OnTextKey(func(k ui.TextKey) bool {
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
			if search.Filtering() {
				searchResults(c, config, search, hits, m, t)
				return
			}
			body(c, config, search, m, t)
		})
	}
}

func header(c *ui.Context, config Config, search *SearchState, m locales.Messages, t *ui.Theme) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
		ui.Text(c, m.NavSettings()).FontSize(24).Bold()
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if search.Filtering() {
				if ui.Button(c, m.SettingsSearchClear()).Clicked() {
					search.Clear()
					c.Invalidate()
				}
			}
			if config.Preferences != nil {
				if ui.Button(c, m.SettingsResetAll()).Clicked() {
					config.Preferences.AskResetAll()
					c.Invalidate()
				}
			}
		})
	})
	if config.Preferences != nil && config.Preferences.ResettingAll() {
		confirmResetAll(c, config.Preferences, m, t)
	}
}

func confirmResetAll(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Padding(12).Gap(8).Background(t.Surface).Border(1, t.Danger).Children(func() {
		ui.Text(c, m.SettingsResetAllTitle()).Bold()
		ui.Text(c, m.SettingsResetAllBody()).FontSize(12).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsResetAllConfirm()).Clicked() {
				state.ResetAll()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsCancel()).Clicked() {
				state.CancelResetAll()
				c.Invalidate()
			}
		})
	})
}

func searchResults(c *ui.Context, config Config, search *SearchState, hits []SearchHit,
	m locales.Messages, t *ui.Theme) {
	ui.Text(c, m.SettingsSearchResults(len(hits))).FontSize(12).TextColor(t.TextMuted)
	if len(hits) == 0 {
		ui.Text(c, m.SettingsSearchNoResults()).TextColor(t.TextMuted)
		return
	}
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			for index, hit := range hits {
				hit := hit
				selected := index == search.Selected()
				label := AreaTitle(m, hit.Area) + " · " + hit.Label
				button := ui.Button(c, label).FillWidth()
				if selected {
					button = button.Background(t.Accent).TextColor(t.AccentText)
				}
				if button.Clicked() {
					search.SetArea(hit.Area)
					c.Invalidate()
				}
			}
		})
	})
	ui.Text(c, m.SettingsSearchHint()).FontSize(11).TextColor(t.TextMuted)
}

func body(c *ui.Context, config Config, search *SearchState, m locales.Messages, t *ui.Theme) {
	ui.Row(c).Grow(1).Gap(16).AlignItems(ui.Start).Children(func() {
		sidebar(c, search, m, t)
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Gap(14).Children(func() {
				area := search.Area()
				ui.Text(c, AreaTitle(m, area)).FontSize(20).Bold()
				if description := AreaDescription(m, area); description != "" {
					ui.Text(c, description).FontSize(12).TextColor(t.TextMuted)
				}
				areaContent(c, config, area, m, t)
			})
		})
	})
}

func sidebar(c *ui.Context, search *SearchState, m locales.Messages, t *ui.Theme) {
	ui.Column(c).Width(168).Gap(4).Children(func() {
		for _, area := range Areas() {
			area := area
			button := ui.Button(c, AreaTitle(m, area)).FillWidth()
			if area == search.Area() {
				button = button.Background(t.Accent).TextColor(t.AccentText)
			}
			if button.Clicked() {
				search.SetArea(area)
				c.Invalidate()
			}
		}
	})
}

func areaContent(c *ui.Context, config Config, area Area, m locales.Messages, t *ui.Theme) {
	switch area {
	case AreaAppearance:
		appearanceArea(c, config.Preferences, m, t)
	case AreaTerminal:
		if config.Terminal != nil {
			terminalSection(c, config.Terminal, m, t)
			groupReset(c, m, t, m.SettingsTerminalTitle(), func() bool {
				return config.Terminal.ResetAll()
			}, true)
		}
	case AreaLayout:
		layoutArea(c, config.Preferences, m, t)
	case AreaSFTPEditor:
		sftpEditorArea(c, config.Preferences, m, t)
	case AreaLogs:
		logsArea(c, config.Preferences, m, t)
	case AreaSystem:
		if config.Desktop != nil {
			desktopSections(c, config.Desktop, m, t)
		}
	case AreaSecurity:
		securityArea(c, config, m, t)
	case AreaSync:
		syncArea(c, config.Preferences, m, t)
	case AreaShortcuts:
		shortcutsArea(c, config.Shortcuts, m, t)
	}
}

func appearanceArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()
		themeLabels := []string{m.SettingsThemeSystem(), m.SettingsThemeLight(), m.SettingsThemeDark()}
		themeModes := []domain.ThemeMode{domain.ThemeSystem, domain.ThemeLight, domain.ThemeDark}
		themeLabel := themeLabels[indexOfModes(themeModes, settings.Appearance.ThemeMode)]
		fieldRow(c, state, m, t, domain.FieldThemeMode, m.SettingsThemeMode(), func() {
			if ui.Select(c, &themeLabel, themeLabels).Width(180).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.ThemeMode = themeModes[indexOf(themeLabels, themeLabel)]
				})
				c.Invalidate()
			}
		})

		accent := settings.Appearance.AccentMode == domain.AccentCustom
		fieldRow(c, state, m, t, domain.FieldAccentMode, m.SettingsAccentMode(), func() {
			if ui.Checkbox(c, &accent, m.SettingsAccentMode()).Changed() {
				state.Update(func(s *domain.Settings) {
					if accent {
						s.Appearance.AccentMode = domain.AccentCustom
					} else {
						s.Appearance.AccentMode = domain.AccentTheme
					}
				})
				c.Invalidate()
			}
		})
		fieldRow(c, state, m, t, domain.FieldAccentColor, m.SettingsAccentColor(), func() {
			if ui.TextInput(c, state.Field(domain.FieldAccentColor)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.AccentColor = state.FieldValue(domain.FieldAccentColor)
				})
				c.Invalidate()
			}
		})
		fieldRow(c, state, m, t, domain.FieldUIFont, m.SettingsUIFont(), func() {
			if ui.TextInput(c, state.Field(domain.FieldUIFont)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.UIFont = state.FieldValue(domain.FieldUIFont)
				})
				c.Invalidate()
			}
		})
		opacity := settings.Appearance.WindowOpacity
		fieldRow(c, state, m, t, domain.FieldWindowOpacity, m.SettingsWindowOpacity(), func() {
			if ui.NumberInput(c, &opacity, 0.5, 1, 0.05).Width(110).Changed() {
				state.Update(func(s *domain.Settings) { s.Appearance.WindowOpacity = opacity })
				c.Invalidate()
			}
		})
		fieldRow(c, state, m, t, domain.FieldTerminalTheme, m.SettingsTerminalTheme(), func() {
			if ui.TextInput(c, state.Field(domain.FieldTerminalTheme)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.TerminalTheme = state.FieldValue(domain.FieldTerminalTheme)
				})
				c.Invalidate()
			}
		})
		fieldRow(c, state, m, t, domain.FieldTerminalFontFamily, m.SettingsTerminalFontFamily(), func() {
			if ui.TextInput(c, state.Field(domain.FieldTerminalFontFamily)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Appearance.TerminalFontFamily = state.FieldValue(domain.FieldTerminalFontFamily)
				})
				c.Invalidate()
			}
		})
		size := settings.Appearance.TerminalFontSize
		fieldRow(c, state, m, t, domain.FieldTerminalFontSize, m.SettingsTerminalFontSize(), func() {
			if ui.NumberInput(c, &size, domain.MinTerminalFontSize, domain.MaxTerminalFontSize, 1).Width(110).Changed() {
				state.Update(func(s *domain.Settings) { s.Appearance.TerminalFontSize = size })
				c.Invalidate()
			}
		})
		ui.Text(c, m.SettingsThemePending()).FontSize(11).TextColor(t.TextMuted)
		groupReset(c, m, t, m.SettingsAppearanceTitle(), func() bool {
			return state.ResetGroup(domain.GroupAppearance)
		}, true)
		saveProblem(c, state, m, t)
	})
}

func layoutArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		settings := state.Settings()
		layoutCheckbox(c, state, m, t, domain.FieldShowHostTreeSidebar, m.SettingsShowHostTreeSidebar(),
			settings.Layout.ShowHostTreeSidebar, func(s *domain.Settings, value bool) {
				s.Layout.ShowHostTreeSidebar = value
			})
		layoutCheckbox(c, state, m, t, domain.FieldShowSFTPTab, m.SettingsShowSFTPTab(),
			settings.Layout.ShowSFTPTab, func(s *domain.Settings, value bool) {
				s.Layout.ShowSFTPTab = value
			})
		layoutCheckbox(c, state, m, t, domain.FieldShowRecentHosts, m.SettingsShowRecentHosts(),
			settings.Layout.ShowRecentHosts, func(s *domain.Settings, value bool) {
				s.Layout.ShowRecentHosts = value
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
		layoutCheckbox(c, state, m, t, domain.FieldShellOnlyTabNumberShortcuts,
			m.SettingsShellOnlyTabNumberShortcuts(), settings.Layout.ShellOnlyTabNumberShortcuts,
			func(s *domain.Settings, value bool) { s.Layout.ShellOnlyTabNumberShortcuts = value })
		layoutCheckbox(c, state, m, t, domain.FieldShowTabNumberBadges,
			m.SettingsShowTabNumberBadges(), settings.Layout.ShowTabNumberBadges,
			func(s *domain.Settings, value bool) { s.Layout.ShowTabNumberBadges = value })
		layoutCheckbox(c, state, m, t, domain.FieldRestorePreviousSession,
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

func layoutCheckbox(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	field domain.Field, label string, value bool, set func(*domain.Settings, bool)) {
	fieldRow(c, state, m, t, field, label, func() {
		enabled := value
		if ui.Checkbox(c, &enabled, label).Changed() {
			state.Update(func(s *domain.Settings) { set(s, enabled) })
			c.Invalidate()
		}
	})
}

func sftpEditorArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		ui.Text(c, m.SettingsSFTPUnavailable()).FontSize(11).TextColor(t.Warning)
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
		sftpCheckbox(c, state, m, t, domain.FieldAutoSync, m.SettingsAutoSync(),
			settings.SFTPEditor.AutoSync, func(s *domain.Settings, value bool) { s.SFTPEditor.AutoSync = value })
		sftpCheckbox(c, state, m, t, domain.FieldShowHiddenFiles, m.SettingsShowHiddenFiles(),
			settings.SFTPEditor.ShowHiddenFiles, func(s *domain.Settings, value bool) { s.SFTPEditor.ShowHiddenFiles = value })
		sftpCheckbox(c, state, m, t, domain.FieldUseCompressedUpload, m.SettingsUseCompressedUpload(),
			settings.SFTPEditor.UseCompressedUpload, func(s *domain.Settings, value bool) { s.SFTPEditor.UseCompressedUpload = value })
		sftpCheckbox(c, state, m, t, domain.FieldAutoOpenSidebar, m.SettingsAutoOpenSidebar(),
			settings.SFTPEditor.AutoOpenSidebar, func(s *domain.Settings, value bool) { s.SFTPEditor.AutoOpenSidebar = value })
		sftpCheckbox(c, state, m, t, domain.FieldFollowTerminalCwd, m.SettingsFollowTerminalCwd(),
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
		sftpCheckbox(c, state, m, t, domain.FieldEditorWordWrap, m.SettingsEditorWordWrap(),
			settings.SFTPEditor.EditorWordWrap, func(s *domain.Settings, value bool) { s.SFTPEditor.EditorWordWrap = value })
		editorSize := settings.SFTPEditor.EditorFontSize
		fieldRow(c, state, m, t, domain.FieldEditorFontSize, m.SettingsEditorFontSize(), func() {
			if ui.NumberInput(c, &editorSize, domain.MinEditorFontSize, domain.MaxEditorFontSize, 1).Width(110).Changed() {
				state.Update(func(s *domain.Settings) { s.SFTPEditor.EditorFontSize = editorSize })
				c.Invalidate()
			}
		})
		groupReset(c, m, t, m.SettingsSFTPEditorTitle(), func() bool {
			return state.ResetGroup(domain.GroupSFTPEditor)
		}, true)
		saveProblem(c, state, m, t)
	})
}

func sftpCheckbox(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	field domain.Field, label string, value bool, set func(*domain.Settings, bool)) {
	fieldRow(c, state, m, t, field, label, func() {
		enabled := value
		if ui.Checkbox(c, &enabled, label).Changed() {
			state.Update(func(s *domain.Settings) { set(s, enabled) })
			c.Invalidate()
		}
	})
}

func logsArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		enabled := state.Settings().Logs.SSHDebug
		fieldRow(c, state, m, t, domain.FieldSSHDebug, m.SettingsSSHDebug(), func() {
			if ui.Checkbox(c, &enabled, m.SettingsSSHDebug()).Changed() {
				state.Update(func(s *domain.Settings) { s.Logs.SSHDebug = enabled })
				c.Invalidate()
			}
		})
		groupReset(c, m, t, m.SettingsLogsTitle(), func() bool {
			return state.ResetGroup(domain.GroupLogs)
		}, true)
		saveProblem(c, state, m, t)
	})
}

func syncArea(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	sectionBody(c, t, func() {
		if state == nil {
			return
		}
		ui.Text(c, m.SettingsSyncUnavailable()).FontSize(11).TextColor(t.Warning)
		settings := state.Settings()
		enabled := settings.Sync.Enabled
		fieldRow(c, state, m, t, domain.FieldSyncEnabled, m.SettingsSyncEnabled(), func() {
			if ui.Checkbox(c, &enabled, m.SettingsSyncEnabled()).Changed() {
				state.Update(func(s *domain.Settings) { s.Sync.Enabled = enabled })
				c.Invalidate()
			}
		})
		auto := settings.Sync.AutoSync
		fieldRow(c, state, m, t, domain.FieldSyncAutoSync, m.SettingsSyncAutoSync(), func() {
			if ui.Checkbox(c, &auto, m.SettingsSyncAutoSync()).Changed() {
				state.Update(func(s *domain.Settings) { s.Sync.AutoSync = auto })
				c.Invalidate()
			}
		})
		strategies := []string{m.SettingsSyncNewest(), m.SettingsSyncLocal(), m.SettingsSyncRemote()}
		values := []domain.SyncConflictStrategy{domain.SyncNewest, domain.SyncLocal, domain.SyncRemote}
		strategy := strategies[indexOfStrategies(values, settings.Sync.ConflictStrategy)]
		fieldRow(c, state, m, t, domain.FieldSyncConflictStrategy, m.SettingsSyncConflictStrategy(), func() {
			if ui.Select(c, &strategy, strategies).Width(190).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Sync.ConflictStrategy = values[indexOf(strategies, strategy)]
				})
				c.Invalidate()
			}
		})
		fieldRow(c, state, m, t, domain.FieldSyncProvider, m.SettingsSyncProvider(), func() {
			if ui.TextInput(c, state.Field(domain.FieldSyncProvider)).Width(230).Changed() {
				state.Update(func(s *domain.Settings) {
					s.Sync.Provider = state.FieldValue(domain.FieldSyncProvider)
				})
				c.Invalidate()
			}
		})
		groupReset(c, m, t, m.SettingsSyncTitle(), func() bool {
			return state.ResetGroup(domain.GroupSync)
		}, true)
		saveProblem(c, state, m, t)
	})
}

func securityArea(c *ui.Context, config Config, m locales.Messages, t *ui.Theme) {
	if config.Preferences != nil {
		state := config.Preferences
		sectionBody(c, t, func() {
			settings := state.Settings()
			confirmClose := settings.Security.ConfirmCloseSessions
			fieldRow(c, state, m, t, domain.FieldConfirmCloseSessions, m.SettingsConfirmCloseSessions(), func() {
				if ui.Checkbox(c, &confirmClose, m.SettingsConfirmCloseSessions()).Changed() {
					state.Update(func(s *domain.Settings) { s.Security.ConfirmCloseSessions = confirmClose })
					c.Invalidate()
				}
			})
			confirmDestructive := settings.Security.ConfirmDestructive
			fieldRow(c, state, m, t, domain.FieldConfirmDestructive, m.SettingsConfirmDestructive(), func() {
				if ui.Checkbox(c, &confirmDestructive, m.SettingsConfirmDestructive()).Changed() {
					state.Update(func(s *domain.Settings) { s.Security.ConfirmDestructive = confirmDestructive })
					c.Invalidate()
				}
			})
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
		ui.Field(c, m.SettingsShortcutScheme(), func() {
			if ui.Select(c, &scheme, schemes).Width(190).Changed() {
				state.SetScheme(values[indexOf(schemes, scheme)])
				c.Invalidate()
			}
		})
		ui.Text(c, m.SettingsShortcutSchemeDescription()).FontSize(11).TextColor(t.TextMuted)

		if pending, ok := state.Pending(); ok {
			pendingConflict(c, state, pending, m, t)
		}
		conflicts := state.Conflicts()
		if len(conflicts) > 0 {
			ui.Text(c, m.SettingsShortcutConflictTitle()).Bold().TextColor(t.Warning)
			for _, conflict := range conflicts {
				names := make([]string, 0, len(conflict.Bindings))
				for _, binding := range conflict.Bindings {
					names = append(names, bindingLabel(m, binding))
				}
				for range conflict.Menu {
					names = append(names, m.SettingsShortcutConflictMenu())
				}
				ui.Text(c, m.SettingsShortcutConflictBody(conflict.Chord, joinNames(names))).
					FontSize(12).TextColor(t.Warning)
			}
		}

		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsShortcutResetAll()).Clicked() {
				state.AskResetAll()
				c.Invalidate()
			}
		})
		if state.ResettingAll() {
			ui.Column(c).Padding(10).Gap(6).Background(t.Surface).Border(1, t.Danger).Children(func() {
				ui.Text(c, m.SettingsResetAllTitle()).Bold()
				ui.Text(c, m.SettingsResetAllBody()).FontSize(12).TextColor(t.TextMuted)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, m.SettingsResetAllConfirm()).Clicked() {
						state.ResetAll()
						c.Invalidate()
					}
					if ui.Button(c, m.SettingsCancel()).Clicked() {
						state.CancelResetAll()
						c.Invalidate()
					}
				})
			})
		}
		ui.Text(c, m.SettingsShortcutRecordHint()).FontSize(11).TextColor(t.TextMuted)

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
			ui.Text(c, m.SettingsSaveFailed()).FontSize(12).TextColor(t.Danger)
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
		for range conflict.Menu {
			names = append(names, m.SettingsShortcutConflictMenu())
		}
	}
	ui.Column(c).Padding(10).Gap(6).Background(t.Surface).Border(1, t.Warning).Children(func() {
		ui.Text(c, m.SettingsShortcutConflictTitle()).Bold().TextColor(t.Warning)
		ui.Text(c, m.SettingsShortcutConflictBody(pending.Chord, joinNames(names))).
			FontSize(12).TextColor(t.Warning)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsShortcutConflictReplace()).Clicked() {
				state.Replace()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsShortcutConflictCancel()).Clicked() {
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
			ui.Text(c, label).FontSize(13)
			if !state.Available(binding.ID) {
				ui.Text(c, m.SettingsShortcutUnavailable()).FontSize(10).TextColor(t.Warning)
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
			if ui.Button(c, m.SettingsShortcutReset()).FontSize(11).Clicked() {
				state.ResetBinding(binding.ID)
				c.Invalidate()
			}
		}
	})
}

// fieldRow places a preference control and, when the field differs from its
// default, a reset control beside it.
func fieldRow(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme,
	field domain.Field, label string, build func()) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			build()
			if !state.IsDefault(field) {
				if ui.Button(c, m.SettingsReset()).FontSize(11).Clicked() {
					state.ResetField(field)
					c.Invalidate()
				}
			}
		})
	})
	_ = label
}

func groupReset(c *ui.Context, m locales.Messages, t *ui.Theme, group string,
	reset func() bool, visible bool) {
	if !visible {
		return
	}
	if ui.Button(c, m.SettingsResetGroup(group)).FontSize(11).Clicked() {
		if reset() {
			c.Invalidate()
		}
	}
}

func saveProblem(c *ui.Context, state *PreferencesState, m locales.Messages, t *ui.Theme) {
	if state.Failed() {
		ui.Text(c, m.SettingsSaveFailed()).FontSize(12).TextColor(t.Danger)
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

func indexOfViews(values []domain.ViewMode, value domain.ViewMode) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func indexOfStrategies(values []domain.SyncConflictStrategy, value domain.SyncConflictStrategy) int {
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
		ui.Field(c, m.SettingsHotkeyAccelerator(), func() {
			ui.TextInput(c, state.Hotkey()).Width(220)
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, m.SettingsHotkeyApply()).Clicked() {
				state.ApplyHotkey()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsHotkeyReset()).Clicked() {
				state.ResetHotkey()
				c.Invalidate()
			}
		})
		if state.Problem() == DesktopProblemHotkey {
			ui.Text(c, m.SettingsHotkeyProblem()).TextColor(t.Danger)
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

func section(c *ui.Context, title, description string, t *ui.Theme, build func()) {
	ui.Column(c).Padding(16).Gap(10).Background(t.Surface).Children(func() {
		ui.Text(c, title).FontSize(18).Bold()
		ui.Text(c, description).FontSize(12).TextColor(t.TextMuted)
		build()
	})
}

// sectionBody is a section whose title and description the area header
// already shows.
func sectionBody(c *ui.Context, t *ui.Theme, build func()) {
	ui.Column(c).Padding(16).Gap(10).Background(t.Surface).Children(build)
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
		ui.Field(c, m.SettingsAppLockCurrentPassword(), func() {
			ui.TextInput(c, lock.CurrentPassword()).Password().Width(240)
		})
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, m.SettingsAppLockDisable()).Clicked() {
				lock.Disable()
				c.Invalidate()
			}
			if ui.Button(c, m.SettingsAppLockLockNow()).Clicked() {
				lock.LockNow()
				c.Invalidate()
			}
		})
		appLockProblem(c, lock, t)
	})
}

func enableSection(c *ui.Context, lock *AppLockState, m locales.Messages) {
	ui.Text(c, m.SettingsAppLockDisabled()).TextColor(c.Theme().TextMuted)
	ui.Field(c, m.SettingsAppLockPassword(), func() {
		ui.TextInput(c, lock.Password()).Password().Width(240)
	})
	ui.Field(c, m.SettingsAppLockConfirm(), func() {
		ui.TextInput(c, lock.Confirmation()).Password().Width(240)
	})
	if ui.PrimaryButton(c, m.SettingsAppLockEnable()).Clicked() {
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
	ui.Field(c, m.SettingsAppLockTimeout(), func() {
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
	ui.Text(c, message).TextColor(t.Danger)
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
		ui.Text(c, message).TextColor(t.Danger)
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
