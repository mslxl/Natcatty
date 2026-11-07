// Package preferences owns the typed non-AI settings groups that are not
// already owned by a narrower context: appearance, layout, SFTP/editor,
// logs, sync, and the security preferences beside App Lock.
package preferences

import (
	"math"

	moshmodel "github.com/mslxl/natcatty/domain/model/mosh"
	domaintheme "github.com/mslxl/natcatty/domain/model/theme"
)

// Group names a resettable settings area.
type Group string

const (
	GroupAppearance Group = "appearance"
	GroupLayout     Group = "layout"
	GroupSFTPEditor Group = "sftpEditor"
	GroupLogs       Group = "logs"
	GroupSync       Group = "sync"
	GroupSecurity   Group = "security"
	GroupTransport  Group = "transport"
	GroupMonitor    Group = "monitor"
	GroupSysManager Group = "sysManager"
)

// Groups returns every group in Settings-window order. GroupMonitor owns
// surface configuration rather than a Settings form, so it is not listed.
func Groups() []Group {
	return []Group{GroupAppearance, GroupLayout, GroupSFTPEditor, GroupLogs,
		GroupSync, GroupSecurity, GroupTransport}
}

// ThemeMode is the original's light/dark/system appearance choice.
type ThemeMode string

const (
	ThemeSystem ThemeMode = "system"
	ThemeLight  ThemeMode = "light"
	ThemeDark   ThemeMode = "dark"
)

// AccentMode chooses between the theme's accent and a custom HSL token.
type AccentMode string

const (
	AccentTheme  AccentMode = "theme"
	AccentCustom AccentMode = "custom"
)

// TabBarPosition is where the workspace Tab strip sits.
type TabBarPosition string

const (
	TabBarTop    TabBarPosition = "top"
	TabBarBottom TabBarPosition = "bottom"
)

// StartupLanding is what the shell shows on launch.
type StartupLanding string

const (
	StartupVault StartupLanding = "vault"
	StartupLast  StartupLanding = "last"
)

// SidePanelDock is the default terminal side-panel edge.
type SidePanelDock string

const (
	DockLeft   SidePanelDock = "left"
	DockRight  SidePanelDock = "right"
	DockBottom SidePanelDock = "bottom"
)

// DoubleClickBehavior is what opening a file with a double click does in SFTP.
type DoubleClickBehavior string

const (
	DoubleClickOpen     DoubleClickBehavior = "open"
	DoubleClickTransfer DoubleClickBehavior = "transfer"
)

// ViewMode is the SFTP pane layout.
type ViewMode string

const (
	ViewList ViewMode = "list"
	ViewTree ViewMode = "tree"
)

// SyncConflictStrategy decides which side wins a cloud-sync conflict.
type SyncConflictStrategy string

const (
	SyncNewest SyncConflictStrategy = "newest"
	SyncLocal  SyncConflictStrategy = "local"
	SyncRemote SyncConflictStrategy = "remote"
)

// CursorStyle is the shape of a terminal's cursor. Programs may change it for
// their own modes; this is the shape they reset to and the one a terminal
// starts with.
type CursorStyle string

const (
	CursorBlock     CursorStyle = "block"
	CursorBar       CursorStyle = "bar"
	CursorUnderline CursorStyle = "underline"

	// DefaultCursorStyle is the original's block cursor.
	DefaultCursorStyle = CursorBlock
)

// Valid reports whether the style is one the application knows.
func (style CursorStyle) Valid() bool {
	switch style {
	case CursorBlock, CursorBar, CursorUnderline:
		return true
	default:
		return false
	}
}

// Appearance group.
type Appearance struct {
	ThemeMode     ThemeMode  `json:"themeMode"`
	LightTheme    string     `json:"lightTheme"`
	DarkTheme     string     `json:"darkTheme"`
	AccentMode    AccentMode `json:"accentMode"`
	AccentColor   string     `json:"accentColor"`
	AppIcon       string     `json:"appIcon"`
	UIFont        string     `json:"uiFont"`
	WindowOpacity float64    `json:"windowOpacity"`
	// TerminalTheme persists a built-in terminal scheme ID or an imported
	// custom scheme ID. Custom IDs live in themes.json, so this field cannot
	// be validated against the built-in catalog alone.
	TerminalTheme string `json:"terminalTheme"`
	// FollowAppTerminalTheme makes terminals use the scheme matched to the
	// active UI preset, as the original's "Follow application theme", so a
	// light window gets a light terminal. TerminalTheme is then only the
	// fallback for a preset with no match.
	FollowAppTerminalTheme bool    `json:"followAppTerminalTheme"`
	TerminalFontFamily     string  `json:"terminalFontFamily"`
	TerminalFontSize       float64 `json:"terminalFontSize"`
	// TerminalCursorStyle is the shape of the terminal cursor.
	TerminalCursorStyle CursorStyle `json:"terminalCursorStyle"`
	// TerminalCursorSteady keeps the cursor from blinking. It is stored
	// inverted so a document written before the field existed, which has no
	// value, keeps the original's blinking default.
	TerminalCursorSteady bool `json:"terminalCursorSteady"`
}

// Layout group.
type Layout struct {
	ShowHostTreeSidebar         bool           `json:"showHostTreeSidebar"`
	ShowSFTPTab                 bool           `json:"showSftpTab"`
	ShowRecentHosts             bool           `json:"showRecentHosts"`
	TabBarPosition              TabBarPosition `json:"tabBarPosition"`
	ShellOnlyTabNumberShortcuts bool           `json:"shellOnlyTabNumberShortcuts"`
	ShowTabNumberBadges         bool           `json:"showTabNumberBadges"`
	RestorePreviousSession      bool           `json:"restorePreviousSession"`
	StartupLanding              StartupLanding `json:"startupLanding"`
	SidePanelDock               SidePanelDock  `json:"sidePanelDock"`
}

// SFTPEditor group.
type SFTPEditor struct {
	DoubleClickBehavior DoubleClickBehavior `json:"doubleClickBehavior"`
	AutoSync            bool                `json:"autoSync"`
	ShowHiddenFiles     bool                `json:"showHiddenFiles"`
	UseCompressedUpload bool                `json:"useCompressedUpload"`
	AutoOpenSidebar     bool                `json:"autoOpenSidebar"`
	FollowTerminalCwd   bool                `json:"followTerminalCwd"`
	DefaultViewMode     ViewMode            `json:"defaultViewMode"`
	EditorWordWrap      bool                `json:"editorWordWrap"`
	EditorFontSize      float64             `json:"editorFontSize"`
	// DefaultOpener opens a file whose extension has no association.
	DefaultOpener Opener `json:"defaultOpener"`
	// FileAssociations override the default opener per extension.
	FileAssociations []FileAssociation `json:"fileAssociations,omitempty"`
}

// Logs group holds the diagnostics settings that are not per-Session logging.
type Logs struct {
	SSHDebug bool `json:"sshDebug"`
}

// Sync group. A backend may be unavailable; the typed preferences still exist
// so they survive a future provider and reset like every other group.
type Sync struct {
	Enabled          bool                 `json:"enabled"`
	AutoSync         bool                 `json:"autoSync"`
	ConflictStrategy SyncConflictStrategy `json:"conflictStrategy"`
	Provider         string               `json:"provider"`
}

// Security group holds preferences beside App Lock itself.
type Security struct {
	ConfirmCloseSessions bool `json:"confirmCloseSessions"`
	ConfirmDestructive   bool `json:"confirmDestructive"`
}

// Transport holds the Mosh client's display settings. Both transports use
// their in-process clients, so there is no client implementation to choose.
type Transport struct {
	// MoshPredictionDisplay selects local echo for Mosh Sessions. Adaptive is
	// stock mosh's default and the value an older document implies.
	MoshPredictionDisplay moshmodel.PredictionDisplay `json:"moshPredictionDisplay"`
}

// Settings is the whole persisted preference document.
type Settings struct {
	Appearance Appearance `json:"appearance"`
	Layout     Layout     `json:"layout"`
	SFTPEditor SFTPEditor `json:"sftpEditor"`
	Logs       Logs       `json:"logs"`
	Sync       Sync       `json:"sync"`
	Security   Security   `json:"security"`
	Transport  Transport  `json:"transport"`
	Monitor    Monitor    `json:"monitor"`
	SysManager SysManager `json:"sysManager"`
}

// PortableSettings is the subset of Settings that follows the user between
// devices. Platform and machine bindings stay local: the app icon variant,
// the default opener, file associations, and the cloud-sync provider profile
// all name a local platform or a device authorization. The Transport group
// stays local as well; it holds only the Mosh prediction-display preference.
type PortableSettings struct {
	Appearance Appearance `json:"appearance"`
	Layout     Layout     `json:"layout"`
	SFTPEditor SFTPEditor `json:"sftpEditor"`
	Logs       Logs       `json:"logs"`
	Security   Security   `json:"security"`
	Monitor    Monitor    `json:"monitor"`
	SysManager SysManager `json:"sysManager"`
}

// Portable strips the device-local fields from a preference document.
func Portable(settings Settings) PortableSettings {
	appearance := settings.Appearance
	appearance.AppIcon = ""
	editor := settings.SFTPEditor
	editor.DefaultOpener = Opener{}
	editor.FileAssociations = nil
	return PortableSettings{
		Appearance: appearance,
		Layout:     settings.Layout,
		SFTPEditor: editor,
		Logs:       settings.Logs,
		Security:   settings.Security,
		Monitor:    settings.Monitor,
		SysManager: settings.SysManager,
	}
}

// MergePortable overlays a synced document on the current one. Device-local
// fields keep their local values, so a download never changes an icon variant
// or an installed application path.
func MergePortable(current Settings, portable PortableSettings) Settings {
	next := current
	next.Appearance = portable.Appearance
	next.Appearance.AppIcon = current.Appearance.AppIcon
	next.Layout = portable.Layout
	next.SFTPEditor = portable.SFTPEditor
	next.SFTPEditor.DefaultOpener = current.SFTPEditor.DefaultOpener
	next.SFTPEditor.FileAssociations = current.SFTPEditor.FileAssociations
	next.Logs = portable.Logs
	next.Security = portable.Security
	next.Monitor = portable.Monitor
	next.SysManager = portable.SysManager
	return Normalize(next)
}

const (
	DefaultAccentColor      = "221.2 83.2% 53.3%"
	DefaultTerminalTheme    = "netcatty-dark"
	DefaultTerminalFontSize = 13
	DefaultEditorFontSize   = 14
	MinTerminalFontSize     = 8
	MaxTerminalFontSize     = 32
	MinEditorFontSize       = 8
	MaxEditorFontSize       = 32
	MinWindowOpacity        = 0.5
)

// Defaults returns the defaults for every group. Deliberate divergences from
// the original are noted beside the field, such as session restore being
// opt-in where the original enabled it.
func Defaults() Settings {
	return Settings{
		Appearance: Appearance{
			ThemeMode:     ThemeSystem,
			LightTheme:    domaintheme.DefaultLightTheme,
			DarkTheme:     domaintheme.DefaultDarkTheme,
			AccentMode:    AccentTheme,
			AccentColor:   DefaultAccentColor,
			AppIcon:       domaintheme.DefaultIconVariant,
			UIFont:        "",
			WindowOpacity: 1,
			TerminalTheme: DefaultTerminalTheme,
			// As the original's, a fresh profile's terminal follows the app;
			// the store keeps an older profile's chosen scheme.
			FollowAppTerminalTheme: true,
			TerminalFontFamily:     DefaultTerminalFont,
			TerminalFontSize:       DefaultTerminalFontSize,
			// The original starts with a block cursor that blinks.
			TerminalCursorStyle: DefaultCursorStyle,
		},
		Layout: Layout{
			ShowHostTreeSidebar:         true,
			ShowSFTPTab:                 true,
			ShowRecentHosts:             true,
			TabBarPosition:              TabBarTop,
			ShellOnlyTabNumberShortcuts: false,
			ShowTabNumberBadges:         true,
			// Restore is opt-in: a fresh profile lands on the Vault instead
			// of reopening a stale Workspace and its connections.
			RestorePreviousSession: false,
			StartupLanding:         StartupVault,
			SidePanelDock:          DockLeft,
		},
		SFTPEditor: SFTPEditor{
			DoubleClickBehavior: DoubleClickOpen,
			AutoSync:            false,
			ShowHiddenFiles:     false,
			UseCompressedUpload: true,
			AutoOpenSidebar:     false,
			FollowTerminalCwd:   false,
			DefaultViewMode:     ViewList,
			EditorWordWrap:      false,
			EditorFontSize:      DefaultEditorFontSize,
			// The original asked on every open by default. This build must
			// choose one of the three openers, and the system association is
			// the least surprising fallback.
			DefaultOpener: Opener{Kind: OpenerSystem},
		},
		Logs:     Logs{SSHDebug: false},
		Sync:     Sync{Enabled: false, AutoSync: false, ConflictStrategy: SyncNewest, Provider: ""},
		Security: Security{ConfirmCloseSessions: true, ConfirmDestructive: true},
		Transport: Transport{
			MoshPredictionDisplay: moshmodel.PredictionAdaptive,
		},
		Monitor:    DefaultMonitor(),
		SysManager: DefaultSysManager(),
	}
}

// Normalize replaces invalid enum values and out-of-range numbers with the
// defaults so a malformed or older file cannot produce an unusable setting.
func Normalize(settings Settings) Settings {
	defaults := Defaults()
	if !validTheme(settings.Appearance.ThemeMode) {
		settings.Appearance.ThemeMode = defaults.Appearance.ThemeMode
	}
	if _, ok := domaintheme.LookupPreset(false, settings.Appearance.LightTheme); !ok {
		settings.Appearance.LightTheme = defaults.Appearance.LightTheme
	}
	if _, ok := domaintheme.LookupPreset(true, settings.Appearance.DarkTheme); !ok {
		settings.Appearance.DarkTheme = defaults.Appearance.DarkTheme
	}
	if !domaintheme.ValidIconVariant(settings.Appearance.AppIcon) {
		settings.Appearance.AppIcon = defaults.Appearance.AppIcon
	}
	if settings.Appearance.AccentMode != AccentTheme && settings.Appearance.AccentMode != AccentCustom {
		settings.Appearance.AccentMode = defaults.Appearance.AccentMode
	}
	if settings.Appearance.AccentColor == "" {
		settings.Appearance.AccentColor = defaults.Appearance.AccentColor
	}
	settings.Appearance.WindowOpacity = clampOpacity(settings.Appearance.WindowOpacity,
		defaults.Appearance.WindowOpacity)
	settings.Appearance.TerminalFontSize = clampFont(settings.Appearance.TerminalFontSize,
		DefaultTerminalFontSize, MinTerminalFontSize, MaxTerminalFontSize)
	if settings.Appearance.TerminalTheme == "" {
		settings.Appearance.TerminalTheme = defaults.Appearance.TerminalTheme
	}
	if settings.Appearance.TerminalFontFamily == "" {
		settings.Appearance.TerminalFontFamily = defaults.Appearance.TerminalFontFamily
	}
	if !settings.Appearance.TerminalCursorStyle.Valid() {
		settings.Appearance.TerminalCursorStyle = defaults.Appearance.TerminalCursorStyle
	}

	if settings.Layout.TabBarPosition != TabBarTop && settings.Layout.TabBarPosition != TabBarBottom {
		settings.Layout.TabBarPosition = defaults.Layout.TabBarPosition
	}
	if settings.Layout.StartupLanding != StartupVault && settings.Layout.StartupLanding != StartupLast {
		settings.Layout.StartupLanding = defaults.Layout.StartupLanding
	}
	switch settings.Layout.SidePanelDock {
	case DockLeft, DockRight, DockBottom:
	default:
		settings.Layout.SidePanelDock = defaults.Layout.SidePanelDock
	}

	if settings.SFTPEditor.DoubleClickBehavior != DoubleClickOpen &&
		settings.SFTPEditor.DoubleClickBehavior != DoubleClickTransfer {
		settings.SFTPEditor.DoubleClickBehavior = defaults.SFTPEditor.DoubleClickBehavior
	}
	if settings.SFTPEditor.DefaultViewMode != ViewList && settings.SFTPEditor.DefaultViewMode != ViewTree {
		settings.SFTPEditor.DefaultViewMode = defaults.SFTPEditor.DefaultViewMode
	}
	settings.SFTPEditor.EditorFontSize = clampFont(settings.SFTPEditor.EditorFontSize,
		DefaultEditorFontSize, MinEditorFontSize, MaxEditorFontSize)
	settings.SFTPEditor.DefaultOpener = NormalizeOpener(settings.SFTPEditor.DefaultOpener)
	settings.SFTPEditor.FileAssociations =
		NormalizeFileAssociations(settings.SFTPEditor.FileAssociations)

	switch settings.Sync.ConflictStrategy {
	case SyncNewest, SyncLocal, SyncRemote:
	default:
		settings.Sync.ConflictStrategy = defaults.Sync.ConflictStrategy
	}
	// Prediction is display-only, so an old or unknown value safely becomes the
	// stock default instead of failing a Session open.
	settings.Transport.MoshPredictionDisplay =
		settings.Transport.MoshPredictionDisplay.Normalized()
	settings.Monitor = NormalizeMonitor(settings.Monitor)
	settings.SysManager = NormalizeSysManager(settings.SysManager)
	return settings
}

// ClampWindowOpacity bounds opacity to the original's 0.5-1 range.
func ClampWindowOpacity(opacity float64) float64 {
	if math.IsNaN(opacity) || math.IsInf(opacity, 0) {
		return 1
	}
	if opacity < MinWindowOpacity {
		return MinWindowOpacity
	}
	if opacity > 1 {
		return 1
	}
	return opacity
}

func clampFont(value, fallback, min, max float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func validTheme(mode ThemeMode) bool {
	return mode == ThemeSystem || mode == ThemeLight || mode == ThemeDark
}

// ResetGroup returns settings with one group restored to its default. The
// other groups are left exactly as they were.
func ResetGroup(settings Settings, group Group) Settings {
	defaults := Defaults()
	switch group {
	case GroupAppearance:
		settings.Appearance = defaults.Appearance
	case GroupLayout:
		settings.Layout = defaults.Layout
	case GroupSFTPEditor:
		settings.SFTPEditor = defaults.SFTPEditor
	case GroupLogs:
		settings.Logs = defaults.Logs
	case GroupSync:
		settings.Sync = defaults.Sync
	case GroupSecurity:
		settings.Security = defaults.Security
	case GroupTransport:
		settings.Transport = defaults.Transport
	case GroupMonitor:
		settings.Monitor = defaults.Monitor
	case GroupSysManager:
		settings.SysManager = defaults.SysManager
	}
	return settings
}

// Field names one resettable setting inside a group. The value is stable so
// the Settings window and a persisted UI state can refer to it.
type Field string

const (
	FieldThemeMode                   Field = "appearance.themeMode"
	FieldLightTheme                  Field = "appearance.lightTheme"
	FieldDarkTheme                   Field = "appearance.darkTheme"
	FieldAccentMode                  Field = "appearance.accentMode"
	FieldAccentColor                 Field = "appearance.accentColor"
	FieldAppIcon                     Field = "appearance.appIcon"
	FieldUIFont                      Field = "appearance.uiFont"
	FieldWindowOpacity               Field = "appearance.windowOpacity"
	FieldTerminalTheme               Field = "appearance.terminalTheme"
	FieldFollowAppTerminalTheme      Field = "appearance.followAppTerminalTheme"
	FieldTerminalFontFamily          Field = "appearance.terminalFontFamily"
	FieldTerminalFontSize            Field = "appearance.terminalFontSize"
	FieldTerminalCursorStyle         Field = "appearance.terminalCursorStyle"
	FieldTerminalCursorBlink         Field = "appearance.terminalCursorBlink"
	FieldShowHostTreeSidebar         Field = "layout.showHostTreeSidebar"
	FieldShowSFTPTab                 Field = "layout.showSftpTab"
	FieldShowRecentHosts             Field = "layout.showRecentHosts"
	FieldTabBarPosition              Field = "layout.tabBarPosition"
	FieldShellOnlyTabNumberShortcuts Field = "layout.shellOnlyTabNumberShortcuts"
	FieldShowTabNumberBadges         Field = "layout.showTabNumberBadges"
	FieldRestorePreviousSession      Field = "layout.restorePreviousSession"
	FieldStartupLanding              Field = "layout.startupLanding"
	FieldSidePanelDock               Field = "layout.sidePanelDock"
	FieldDoubleClickBehavior         Field = "sftpEditor.doubleClickBehavior"
	FieldAutoSync                    Field = "sftpEditor.autoSync"
	FieldShowHiddenFiles             Field = "sftpEditor.showHiddenFiles"
	FieldUseCompressedUpload         Field = "sftpEditor.useCompressedUpload"
	FieldAutoOpenSidebar             Field = "sftpEditor.autoOpenSidebar"
	FieldFollowTerminalCwd           Field = "sftpEditor.followTerminalCwd"
	FieldDefaultViewMode             Field = "sftpEditor.defaultViewMode"
	FieldEditorWordWrap              Field = "sftpEditor.editorWordWrap"
	FieldEditorFontSize              Field = "sftpEditor.editorFontSize"
	FieldDefaultOpener               Field = "sftpEditor.defaultOpener"
	FieldSSHDebug                    Field = "logs.sshDebug"
	FieldSyncEnabled                 Field = "sync.enabled"
	FieldSyncAutoSync                Field = "sync.autoSync"
	FieldSyncConflictStrategy        Field = "sync.conflictStrategy"
	FieldSyncProvider                Field = "sync.provider"
	FieldConfirmCloseSessions        Field = "security.confirmCloseSessions"
	FieldConfirmDestructive          Field = "security.confirmDestructive"
	FieldMoshPredictionDisplay       Field = "transport.moshPredictionDisplay"
)

// Fields returns every resettable field in Settings-window order.
func Fields() []Field {
	return []Field{
		FieldThemeMode, FieldLightTheme, FieldDarkTheme, FieldAccentMode,
		FieldAccentColor, FieldAppIcon, FieldUIFont,
		FieldWindowOpacity, FieldFollowAppTerminalTheme, FieldTerminalTheme, FieldTerminalFontFamily,
		FieldTerminalFontSize, FieldTerminalCursorStyle, FieldTerminalCursorBlink,
		FieldShowHostTreeSidebar, FieldShowSFTPTab, FieldShowRecentHosts,
		FieldTabBarPosition, FieldShellOnlyTabNumberShortcuts,
		FieldShowTabNumberBadges, FieldRestorePreviousSession,
		FieldStartupLanding, FieldSidePanelDock,
		FieldDoubleClickBehavior, FieldAutoSync, FieldShowHiddenFiles,
		FieldUseCompressedUpload, FieldAutoOpenSidebar, FieldFollowTerminalCwd,
		FieldDefaultViewMode, FieldEditorWordWrap, FieldEditorFontSize,
		FieldDefaultOpener,
		FieldSSHDebug,
		FieldSyncEnabled, FieldSyncAutoSync, FieldSyncConflictStrategy,
		FieldSyncProvider,
		FieldConfirmCloseSessions, FieldConfirmDestructive,
		FieldMoshPredictionDisplay,
	}
}

// GroupOf returns the group a field belongs to.
func GroupOf(field Field) (Group, bool) {
	switch field {
	case FieldThemeMode, FieldLightTheme, FieldDarkTheme, FieldAccentMode,
		FieldAccentColor, FieldAppIcon, FieldUIFont,
		FieldWindowOpacity, FieldFollowAppTerminalTheme, FieldTerminalTheme, FieldTerminalFontFamily,
		FieldTerminalFontSize, FieldTerminalCursorStyle, FieldTerminalCursorBlink:
		return GroupAppearance, true
	case FieldShowHostTreeSidebar, FieldShowSFTPTab, FieldShowRecentHosts,
		FieldTabBarPosition, FieldShellOnlyTabNumberShortcuts,
		FieldShowTabNumberBadges, FieldRestorePreviousSession,
		FieldStartupLanding, FieldSidePanelDock:
		return GroupLayout, true
	case FieldDoubleClickBehavior, FieldAutoSync, FieldShowHiddenFiles,
		FieldUseCompressedUpload, FieldAutoOpenSidebar, FieldFollowTerminalCwd,
		FieldDefaultViewMode, FieldEditorWordWrap, FieldEditorFontSize,
		FieldDefaultOpener:
		return GroupSFTPEditor, true
	case FieldSSHDebug:
		return GroupLogs, true
	case FieldSyncEnabled, FieldSyncAutoSync, FieldSyncConflictStrategy,
		FieldSyncProvider:
		return GroupSync, true
	case FieldConfirmCloseSessions, FieldConfirmDestructive:
		return GroupSecurity, true
	case FieldMoshPredictionDisplay:
		return GroupTransport, true
	default:
		return "", false
	}
}

// ResetField returns settings with one field restored to its default, leaving
// every other field alone.
func ResetField(settings Settings, field Field) Settings {
	defaults := Defaults()
	switch field {
	case FieldThemeMode:
		settings.Appearance.ThemeMode = defaults.Appearance.ThemeMode
	case FieldLightTheme:
		settings.Appearance.LightTheme = defaults.Appearance.LightTheme
	case FieldDarkTheme:
		settings.Appearance.DarkTheme = defaults.Appearance.DarkTheme
	case FieldAppIcon:
		settings.Appearance.AppIcon = defaults.Appearance.AppIcon
	case FieldAccentMode:
		settings.Appearance.AccentMode = defaults.Appearance.AccentMode
	case FieldAccentColor:
		settings.Appearance.AccentColor = defaults.Appearance.AccentColor
	case FieldUIFont:
		settings.Appearance.UIFont = defaults.Appearance.UIFont
	case FieldWindowOpacity:
		settings.Appearance.WindowOpacity = defaults.Appearance.WindowOpacity
	case FieldTerminalTheme:
		settings.Appearance.TerminalTheme = defaults.Appearance.TerminalTheme
	case FieldFollowAppTerminalTheme:
		settings.Appearance.FollowAppTerminalTheme = defaults.Appearance.FollowAppTerminalTheme
	case FieldTerminalFontFamily:
		settings.Appearance.TerminalFontFamily = defaults.Appearance.TerminalFontFamily
	case FieldTerminalFontSize:
		settings.Appearance.TerminalFontSize = defaults.Appearance.TerminalFontSize
	case FieldTerminalCursorStyle:
		settings.Appearance.TerminalCursorStyle = defaults.Appearance.TerminalCursorStyle
	case FieldTerminalCursorBlink:
		settings.Appearance.TerminalCursorSteady = defaults.Appearance.TerminalCursorSteady
	case FieldShowHostTreeSidebar:
		settings.Layout.ShowHostTreeSidebar = defaults.Layout.ShowHostTreeSidebar
	case FieldShowSFTPTab:
		settings.Layout.ShowSFTPTab = defaults.Layout.ShowSFTPTab
	case FieldShowRecentHosts:
		settings.Layout.ShowRecentHosts = defaults.Layout.ShowRecentHosts
	case FieldTabBarPosition:
		settings.Layout.TabBarPosition = defaults.Layout.TabBarPosition
	case FieldShellOnlyTabNumberShortcuts:
		settings.Layout.ShellOnlyTabNumberShortcuts = defaults.Layout.ShellOnlyTabNumberShortcuts
	case FieldShowTabNumberBadges:
		settings.Layout.ShowTabNumberBadges = defaults.Layout.ShowTabNumberBadges
	case FieldRestorePreviousSession:
		settings.Layout.RestorePreviousSession = defaults.Layout.RestorePreviousSession
	case FieldStartupLanding:
		settings.Layout.StartupLanding = defaults.Layout.StartupLanding
	case FieldSidePanelDock:
		settings.Layout.SidePanelDock = defaults.Layout.SidePanelDock
	case FieldDoubleClickBehavior:
		settings.SFTPEditor.DoubleClickBehavior = defaults.SFTPEditor.DoubleClickBehavior
	case FieldAutoSync:
		settings.SFTPEditor.AutoSync = defaults.SFTPEditor.AutoSync
	case FieldShowHiddenFiles:
		settings.SFTPEditor.ShowHiddenFiles = defaults.SFTPEditor.ShowHiddenFiles
	case FieldUseCompressedUpload:
		settings.SFTPEditor.UseCompressedUpload = defaults.SFTPEditor.UseCompressedUpload
	case FieldAutoOpenSidebar:
		settings.SFTPEditor.AutoOpenSidebar = defaults.SFTPEditor.AutoOpenSidebar
	case FieldFollowTerminalCwd:
		settings.SFTPEditor.FollowTerminalCwd = defaults.SFTPEditor.FollowTerminalCwd
	case FieldDefaultViewMode:
		settings.SFTPEditor.DefaultViewMode = defaults.SFTPEditor.DefaultViewMode
	case FieldEditorWordWrap:
		settings.SFTPEditor.EditorWordWrap = defaults.SFTPEditor.EditorWordWrap
	case FieldEditorFontSize:
		settings.SFTPEditor.EditorFontSize = defaults.SFTPEditor.EditorFontSize
	case FieldDefaultOpener:
		settings.SFTPEditor.DefaultOpener = defaults.SFTPEditor.DefaultOpener
	case FieldSSHDebug:
		settings.Logs.SSHDebug = defaults.Logs.SSHDebug
	case FieldSyncEnabled:
		settings.Sync.Enabled = defaults.Sync.Enabled
	case FieldSyncAutoSync:
		settings.Sync.AutoSync = defaults.Sync.AutoSync
	case FieldSyncConflictStrategy:
		settings.Sync.ConflictStrategy = defaults.Sync.ConflictStrategy
	case FieldSyncProvider:
		settings.Sync.Provider = defaults.Sync.Provider
	case FieldConfirmCloseSessions:
		settings.Security.ConfirmCloseSessions = defaults.Security.ConfirmCloseSessions
	case FieldConfirmDestructive:
		settings.Security.ConfirmDestructive = defaults.Security.ConfirmDestructive
	case FieldMoshPredictionDisplay:
		settings.Transport.MoshPredictionDisplay = defaults.Transport.MoshPredictionDisplay
	}
	return settings
}

// ResetAll returns the defaults.
func ResetAll() Settings { return Defaults() }

// clampOpacity treats the zero value as "unset" so a record written before
// the field existed gets the default, not the minimum.
func clampOpacity(value, fallback float64) float64 {
	if value == 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	return ClampWindowOpacity(value)
}
