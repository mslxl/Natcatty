// Package preferences owns the typed non-AI settings groups that are not
// already owned by a narrower context: appearance, layout, SFTP/editor,
// logs, sync, and the security preferences beside App Lock.
package preferences

import (
	"math"
	"net"
	"runtime"
	"strings"

	atuinmodel "github.com/mslxl/natcatty/domain/model/atuin"
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
	// GroupExternalMCP owns the device-local Streamable HTTP endpoint. It is
	// absent from Groups and from PortableSettings: the opt-in switch and bind
	// fields are currently device-local only, with no Settings surface yet.
	GroupExternalMCP Group = "externalMcp"
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
	// StartupVault opens the Vault.
	StartupVault StartupLanding = "vault"
	// StartupLast is the "last session" choice. Session restore is its own
	// switch, so with restore off it opens a local terminal like
	// StartupTerminal.
	StartupLast StartupLanding = "last"
	// StartupTerminal opens a new local terminal Tab.
	StartupTerminal StartupLanding = "terminal"
	// StartupMonitoring opens the Host monitoring page.
	StartupMonitoring StartupLanding = "monitoring"
)

// Valid reports whether landing is one the shell knows how to show.
func (landing StartupLanding) Valid() bool {
	switch landing {
	case StartupVault, StartupLast, StartupTerminal, StartupMonitoring:
		return true
	default:
		return false
	}
}

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

// HostViewMode is the Vault Hosts page's presentation: a card grid, a row
// list, or an indented group tree, matching the original's view switch.
type HostViewMode string

const (
	HostViewGrid HostViewMode = "grid"
	HostViewList HostViewMode = "list"
	HostViewTree HostViewMode = "tree"
)

// Valid reports whether the mode is one the Vault offers.
func (mode HostViewMode) Valid() bool {
	switch mode {
	case HostViewGrid, HostViewList, HostViewTree:
		return true
	default:
		return false
	}
}

// HostSortMode orders the Vault's Hosts. Manual keeps each Host Group's saved
// order; the other modes sort the visible list, as the original's sort menu.
type HostSortMode string

const (
	HostSortManual HostSortMode = "manual"
	HostSortAZ     HostSortMode = "az"
	HostSortZA     HostSortMode = "za"
	HostSortNewest HostSortMode = "newest"
	HostSortOldest HostSortMode = "oldest"
	HostSortGroup  HostSortMode = "group"
	HostSortIP     HostSortMode = "ip"
)

// Valid reports whether the mode is one the Vault offers.
func (mode HostSortMode) Valid() bool {
	switch mode {
	case HostSortManual, HostSortAZ, HostSortZA, HostSortNewest, HostSortOldest, HostSortGroup, HostSortIP:
		return true
	default:
		return false
	}
}

// HostClickBehavior is what one click on a Vault Host card does. Connect is
// the original's default: a click opens the Session immediately. Select makes
// the first click focus the card and the second activate it.
type HostClickBehavior string

const (
	HostClickConnect HostClickBehavior = "connect"
	HostClickSelect  HostClickBehavior = "select"
)

// Valid reports whether the behavior is one the Vault offers.
func (behavior HostClickBehavior) Valid() bool {
	return behavior == HostClickConnect || behavior == HostClickSelect
}

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

// InterfaceStyle is the look of the interface's shapes and materials:
// radii, sizes, translucency, and layout. It is independent of the color
// preset, which decides colors only.
type InterfaceStyle string

const (
	// InterfaceClassic is the original's shadcn look.
	InterfaceClassic InterfaceStyle = "classic"
	// InterfaceMacOS is the macOS Liquid Glass look.
	InterfaceMacOS InterfaceStyle = "macos"
)

// Valid reports whether style is a known Interface Style.
func (style InterfaceStyle) Valid() bool {
	return style == InterfaceClassic || style == InterfaceMacOS
}

// DefaultInterfaceStyle is the style a fresh profile starts with on goos: the
// macOS style on a Mac, Classic elsewhere.
func DefaultInterfaceStyle(goos string) InterfaceStyle {
	if goos == "darwin" {
		return InterfaceMacOS
	}
	return InterfaceClassic
}

// LocalShell is the program a new local Session starts instead of the OS
// login shell. An empty Path asks the platform for its login shell, and an
// empty Args starts the program without arguments.
//
// Args is stored exactly as the user typed it and parsed when a Session
// opens. Storing argv instead would force the Settings editor to re-render
// every keystroke through quote normalization, which loses a trailing space
// and an unclosed quote while they are still being typed.
//
// The value is device-local: a path names a program installed on one machine,
// so Portable strips it and MergePortable keeps the local choice.
type LocalShell struct {
	Path string `json:"path,omitempty"`
	Args string `json:"args,omitempty"`
}

// Normalized trims the path but keeps the argument draft even while the path
// is still empty. The Settings editor saves after every keystroke, and the
// user may type arguments before the path; dropping the draft then would erase
// them on the next save before the path arrives. Command ignores the draft
// until a path is set, so it still asks for the OS login shell.
func (shell LocalShell) Normalized() LocalShell {
	shell.Path = strings.TrimSpace(shell.Path)
	return shell
}

// Command returns the argv a local Session starts. exists reports whether a
// configured path names a program the OS can start; it must resolve the path
// without running the program. A nil exists skips the check.
//
// An empty path returns nil and false: the OS login shell. A configured path
// that does not exist returns nil and true, so the caller opens the OS login
// shell and can warn that the choice was ignored.
func (shell LocalShell) Command(exists func(string) bool) ([]string, bool) {
	shell = shell.Normalized()
	if shell.Path == "" {
		return nil, false
	}
	if exists != nil && !exists(shell.Path) {
		return nil, true
	}
	return append([]string{shell.Path}, ParseArgs(shell.Args)...), false
}

// ParseArgs splits a Settings-window argument string into argv words. Single
// and double quotes group spaces, a backslash escapes the next character, and
// a quoted empty word ("") is kept. It never invokes a shell.
func ParseArgs(raw string) []string {
	var args []string
	var word strings.Builder
	started := false
	var quote byte
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if quote != 0 {
			switch {
			case ch == quote:
				quote = 0
			case ch == '\\' && quote == '"' && i+1 < len(raw):
				i++
				word.WriteByte(raw[i])
			default:
				word.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			started = true
		case '\\':
			if i+1 < len(raw) {
				i++
				word.WriteByte(raw[i])
			} else {
				word.WriteByte(ch)
			}
			started = true
		case ' ', '\t', '\n', '\r':
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteByte(ch)
			started = true
		}
	}
	if started {
		args = append(args, word.String())
	}
	return args
}

// FormatArgs renders args so ParseArgs reads them back unchanged. A word is
// quoted only when it contains whitespace, a quote, or a backslash.
func FormatArgs(args []string) string {
	parts := make([]string, len(args))
	escaper := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	for i, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\r\"'\\") {
			parts[i] = arg
			continue
		}
		parts[i] = `"` + escaper.Replace(arg) + `"`
	}
	return strings.Join(parts, " ")
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
	// InterfaceStyle is device-local: the best style differs by platform, so
	// it is stripped from the synced document.
	InterfaceStyle InterfaceStyle `json:"interfaceStyle"`
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
	// TerminalShiftEnterPlain leaves Shift+Enter to the terminal's own
	// encoding instead of sending ESC CR. It is stored inverted so a document
	// written before the field existed, which has no value, keeps the default:
	// ESC CR is the legacy Alt+Enter sequence that readline, zle, and most TUI
	// CLIs read as a literal newline rather than a submit.
	TerminalShiftEnterPlain bool `json:"terminalShiftEnterPlain"`
	// LocalShell replaces the OS login shell for local Sessions. It is
	// device-local and stripped from the synced document.
	LocalShell LocalShell `json:"localShell"`
}

// Layout group.
type Layout struct {
	ShowHostTreeSidebar bool `json:"showHostTreeSidebar"`
	ShowSFTPTab         bool `json:"showSftpTab"`
	ShowRecentHosts     bool `json:"showRecentHosts"`
	// ShowOnlyUngroupedHostsInRoot keeps grouped Hosts out of the Vault's root
	// list, as the original's setting of the same name. Opening a group still
	// shows its Hosts.
	ShowOnlyUngroupedHostsInRoot bool              `json:"showOnlyUngroupedHostsInRoot"`
	HostClickBehavior            HostClickBehavior `json:"hostClickBehavior"`
	TabBarPosition               TabBarPosition    `json:"tabBarPosition"`
	ShellOnlyTabNumberShortcuts  bool              `json:"shellOnlyTabNumberShortcuts"`
	ShowTabNumberBadges          bool              `json:"showTabNumberBadges"`
	RestorePreviousSession       bool              `json:"restorePreviousSession"`
	StartupLanding               StartupLanding    `json:"startupLanding"`
	SidePanelDock                SidePanelDock     `json:"sidePanelDock"`
	// HostViewMode and HostSortMode are this device's Vault layout. They are
	// persisted with the other preferences but stripped from the synced
	// document: the original kept them in per-device storage, so a phone-sized
	// card grid never follows the user to a desktop.
	HostViewMode HostViewMode `json:"hostViewMode"`
	HostSortMode HostSortMode `json:"hostSortMode"`
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

// Logs group holds the diagnostics settings and the device-local Command
// History sync settings shown in the Logs settings area. It is not per-Session
// logging.
type Logs struct {
	SSHDebug bool `json:"sshDebug"`
}

// History sync bounds. The address is the public Atuin server by default; the
// interval is clamped so a malformed file cannot disable or flood sync.
const (
	DefaultHistorySyncAddress         = "https://api.atuin.sh"
	DefaultHistorySyncIntervalMinutes = 5
	MinHistorySyncIntervalMinutes     = 1
	MaxHistorySyncIntervalMinutes     = 60
)

// History holds the device-local Atuin account and sync settings. The session
// token and master key never live here: they belong to the field-secret store
// under a dedicated entity type. These settings therefore do not travel in
// PortableSettings, Vault cloud sync, or a portable export.
//
// HistoryCaptureHookEnabled is the portable capture preference; it belongs to
// the terminal settings group because it changes Session behavior.
type History struct {
	SyncEnabled         bool                `json:"syncEnabled"`
	SyncAddress         string              `json:"syncAddress"`
	Username            string              `json:"username"`
	AuthMode            atuinmodel.AuthMode `json:"authMode"`
	SyncIntervalMinutes int                 `json:"syncIntervalMinutes"`
}

// NormalizeHistory returns a usable device-local history configuration. A
// zero interval from a document written before the field existed gets the
// default, not the one-minute minimum.
func NormalizeHistory(history History) History {
	history.SyncAddress = strings.TrimSpace(history.SyncAddress)
	if history.SyncAddress == "" {
		history.SyncAddress = DefaultHistorySyncAddress
	}
	history.Username = strings.TrimSpace(history.Username)
	history.AuthMode = atuinmodel.NormalizeAuthMode(history.AuthMode)
	if history.SyncIntervalMinutes <= 0 {
		history.SyncIntervalMinutes = DefaultHistorySyncIntervalMinutes
	}
	if history.SyncIntervalMinutes > MaxHistorySyncIntervalMinutes {
		history.SyncIntervalMinutes = MaxHistorySyncIntervalMinutes
	}
	return history
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

// ExternalMCP is the optional Streamable HTTP endpoint for the external MCP
// server. It is device-local: a bind address and port name this machine, and
// the endpoint is off until the user enables it.
type ExternalMCP struct {
	HTTPEnabled         bool   `json:"httpEnabled"`
	HTTPPort            int    `json:"httpPort"`
	HTTPAllowCustomBind bool   `json:"httpAllowCustomBind"`
	HTTPListenAddress   string `json:"httpListenAddress"`
	HTTPClientHost      string `json:"httpClientHost"`
}

const (
	// DefaultExternalMCPHTTPPort matches the original endpoint's default.
	DefaultExternalMCPHTTPPort = 47391
	// MinExternalMCPHTTPPort keeps the endpoint out of privileged ports.
	MinExternalMCPHTTPPort = 1024
	// MaxExternalMCPHTTPPort is the highest TCP port.
	MaxExternalMCPHTTPPort = 65535
	// DefaultExternalMCPHTTPListenAddress keeps the endpoint on loopback.
	DefaultExternalMCPHTTPListenAddress = "127.0.0.1"
)

// EffectiveHTTPListenAddress is the bind address the transport uses. A custom
// address applies only when the user explicitly allows a non-default bind.
func (e ExternalMCP) EffectiveHTTPListenAddress() string {
	if e.HTTPAllowCustomBind {
		if address := strings.TrimSpace(e.HTTPListenAddress); address != "" {
			return address
		}
	}
	return DefaultExternalMCPHTTPListenAddress
}

// NormalizeExternalMCP repairs an unusable endpoint configuration. The
// transport is still off after normalization; only an explicit enable turns
// it on.
func NormalizeExternalMCP(group ExternalMCP) ExternalMCP {
	if group.HTTPPort < MinExternalMCPHTTPPort || group.HTTPPort > MaxExternalMCPHTTPPort {
		group.HTTPPort = DefaultExternalMCPHTTPPort
	}
	group.HTTPListenAddress = strings.TrimSpace(group.HTTPListenAddress)
	if group.HTTPListenAddress != "localhost" && net.ParseIP(group.HTTPListenAddress) == nil {
		group.HTTPListenAddress = DefaultExternalMCPHTTPListenAddress
	}
	group.HTTPClientHost = strings.TrimSpace(group.HTTPClientHost)
	if group.HTTPClientHost != "" {
		address := net.ParseIP(group.HTTPClientHost)
		if address == nil || group.HTTPClientHost == "0.0.0.0" || group.HTTPClientHost == "::" {
			group.HTTPClientHost = ""
		}
	}
	return group
}

// Transport holds the Mosh client's display settings. Both transports use
// their in-process clients, so there is no client implementation to choose.
type Transport struct {
	// MoshPredictionDisplay selects local echo for Mosh Sessions. Adaptive is
	// stock mosh's default and the value an older document implies.
	MoshPredictionDisplay moshmodel.PredictionDisplay `json:"moshPredictionDisplay"`
}

// Proxy is the connection default used when neither a Host nor its Host
// Group selects a proxy. It is portable, so a synced settings document carries
// the same default to the user's other devices.
type Proxy struct {
	// DefaultProfileID names a saved Proxy Profile. Empty means a direct
	// connection when nothing else selects a proxy.
	DefaultProfileID string `json:"defaultProfileId,omitempty"`
}

// DefaultProxyProfileID returns the normalized app-wide proxy selection.
func (settings Settings) DefaultProxyProfileID() string {
	return strings.TrimSpace(settings.Proxy.DefaultProfileID)
}

// Settings is the whole persisted preference document.
type Settings struct {
	Appearance Appearance `json:"appearance"`
	Layout     Layout     `json:"layout"`
	SFTPEditor SFTPEditor `json:"sftpEditor"`
	Logs       Logs       `json:"logs"`
	// Proxy is the app-wide connection default, below Host and Host Group
	// selections in precedence.
	Proxy Proxy `json:"proxy"`
	// History is device-local. It is deliberately absent from
	// PortableSettings, so Vault cloud sync never carries an account address,
	// username, auth mode, or interval.
	History     History     `json:"history"`
	Sync        Sync        `json:"sync"`
	Security    Security    `json:"security"`
	Transport   Transport   `json:"transport"`
	Monitor     Monitor     `json:"monitor"`
	SysManager  SysManager  `json:"sysManager"`
	ExternalMCP ExternalMCP `json:"externalMcp"`
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
	Proxy      Proxy      `json:"proxy"`
	Security   Security   `json:"security"`
	Monitor    Monitor    `json:"monitor"`
	SysManager SysManager `json:"sysManager"`
}

// Portable strips the device-local fields from a preference document.
func Portable(settings Settings) PortableSettings {
	appearance := settings.Appearance
	appearance.AppIcon = ""
	appearance.InterfaceStyle = ""
	// A shell path names a program installed on this device.
	appearance.LocalShell = LocalShell{}
	editor := settings.SFTPEditor
	editor.DefaultOpener = Opener{}
	editor.FileAssociations = nil
	// The Vault's view and sort belong to the screen the user is sitting at,
	// as they did in the original's per-device storage.
	layout := settings.Layout
	layout.HostViewMode = ""
	layout.HostSortMode = ""
	return PortableSettings{
		Appearance: appearance,
		Layout:     layout,
		SFTPEditor: editor,
		Logs:       settings.Logs,
		Proxy:      settings.Proxy,
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
	next.Appearance.InterfaceStyle = current.Appearance.InterfaceStyle
	next.Appearance.LocalShell = current.Appearance.LocalShell
	next.Layout = portable.Layout
	next.Layout.HostViewMode = current.Layout.HostViewMode
	next.Layout.HostSortMode = current.Layout.HostSortMode
	next.SFTPEditor = portable.SFTPEditor
	next.SFTPEditor.DefaultOpener = current.SFTPEditor.DefaultOpener
	next.SFTPEditor.FileAssociations = current.SFTPEditor.FileAssociations
	next.Logs = portable.Logs
	next.Proxy = portable.Proxy
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
			ThemeMode:      ThemeSystem,
			LightTheme:     domaintheme.DefaultLightTheme,
			DarkTheme:      domaintheme.DefaultDarkTheme,
			AccentMode:     AccentTheme,
			AccentColor:    DefaultAccentColor,
			AppIcon:        domaintheme.DefaultIconVariant,
			UIFont:         "",
			WindowOpacity:  1,
			InterfaceStyle: DefaultInterfaceStyle(runtime.GOOS),
			TerminalTheme:  DefaultTerminalTheme,
			// As the original's, a fresh profile's terminal follows the app;
			// the store keeps an older profile's chosen scheme.
			FollowAppTerminalTheme: true,
			TerminalFontFamily:     DefaultTerminalFont,
			TerminalFontSize:       DefaultTerminalFontSize,
			// The original starts with a block cursor that blinks.
			TerminalCursorStyle: DefaultCursorStyle,
		},
		Layout: Layout{
			ShowHostTreeSidebar:          true,
			ShowSFTPTab:                  true,
			ShowRecentHosts:              true,
			ShowOnlyUngroupedHostsInRoot: false,
			HostClickBehavior:            HostClickConnect,
			HostViewMode:                 HostViewGrid,
			HostSortMode:                 HostSortManual,
			TabBarPosition:               TabBarTop,
			ShellOnlyTabNumberShortcuts:  false,
			ShowTabNumberBadges:          true,
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
		Logs: Logs{SSHDebug: false},
		History: History{
			SyncEnabled:         false,
			SyncAddress:         DefaultHistorySyncAddress,
			Username:            "",
			AuthMode:            atuinmodel.AuthModeSession,
			SyncIntervalMinutes: DefaultHistorySyncIntervalMinutes,
		},
		Sync:     Sync{Enabled: false, AutoSync: false, ConflictStrategy: SyncNewest, Provider: ""},
		Security: Security{ConfirmCloseSessions: true, ConfirmDestructive: true},
		Transport: Transport{
			MoshPredictionDisplay: moshmodel.PredictionAdaptive,
		},
		Monitor:    DefaultMonitor(),
		SysManager: DefaultSysManager(),
		ExternalMCP: ExternalMCP{
			// The HTTP endpoint is opt-in; a fresh profile exposes MCP over
			// stdio only, as the original did.
			HTTPEnabled:       false,
			HTTPPort:          DefaultExternalMCPHTTPPort,
			HTTPListenAddress: DefaultExternalMCPHTTPListenAddress,
		},
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
	if !settings.Appearance.InterfaceStyle.Valid() {
		settings.Appearance.InterfaceStyle = defaults.Appearance.InterfaceStyle
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
	settings.Appearance.LocalShell = settings.Appearance.LocalShell.Normalized()

	if !settings.Layout.HostViewMode.Valid() {
		settings.Layout.HostViewMode = defaults.Layout.HostViewMode
	}
	if !settings.Layout.HostSortMode.Valid() {
		settings.Layout.HostSortMode = defaults.Layout.HostSortMode
	}
	if !settings.Layout.HostClickBehavior.Valid() {
		settings.Layout.HostClickBehavior = defaults.Layout.HostClickBehavior
	}
	if settings.Layout.TabBarPosition != TabBarTop && settings.Layout.TabBarPosition != TabBarBottom {
		settings.Layout.TabBarPosition = defaults.Layout.TabBarPosition
	}
	if !settings.Layout.StartupLanding.Valid() {
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

	settings.History = NormalizeHistory(settings.History)

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
	settings.ExternalMCP = NormalizeExternalMCP(settings.ExternalMCP)
	settings.Proxy.DefaultProfileID = strings.TrimSpace(settings.Proxy.DefaultProfileID)
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
		settings.History = defaults.History
	case GroupSync:
		settings.Sync = defaults.Sync
	case GroupSecurity:
		settings.Security = defaults.Security
	case GroupTransport:
		settings.Transport = defaults.Transport
		// The app-wide default Proxy lives in its own top-level field but is
		// edited in this Settings area, so resetting the area must clear it.
		settings.Proxy = defaults.Proxy
	case GroupMonitor:
		settings.Monitor = defaults.Monitor
	case GroupSysManager:
		settings.SysManager = defaults.SysManager
	case GroupExternalMCP:
		settings.ExternalMCP = defaults.ExternalMCP
	}
	return settings
}

// Field names one resettable setting inside a group. The value is stable so
// the Settings window and a persisted UI state can refer to it.
type Field string

const (
	FieldThemeMode                    Field = "appearance.themeMode"
	FieldLightTheme                   Field = "appearance.lightTheme"
	FieldDarkTheme                    Field = "appearance.darkTheme"
	FieldAccentMode                   Field = "appearance.accentMode"
	FieldAccentColor                  Field = "appearance.accentColor"
	FieldAppIcon                      Field = "appearance.appIcon"
	FieldUIFont                       Field = "appearance.uiFont"
	FieldWindowOpacity                Field = "appearance.windowOpacity"
	FieldInterfaceStyle               Field = "appearance.interfaceStyle"
	FieldTerminalTheme                Field = "appearance.terminalTheme"
	FieldFollowAppTerminalTheme       Field = "appearance.followAppTerminalTheme"
	FieldTerminalFontFamily           Field = "appearance.terminalFontFamily"
	FieldTerminalFontSize             Field = "appearance.terminalFontSize"
	FieldTerminalCursorStyle          Field = "appearance.terminalCursorStyle"
	FieldTerminalCursorBlink          Field = "appearance.terminalCursorBlink"
	FieldTerminalShiftEnter           Field = "appearance.terminalShiftEnter"
	FieldLocalShell                   Field = "appearance.localShell"
	FieldLocalShellArgs               Field = "appearance.localShellArgs"
	FieldShowHostTreeSidebar          Field = "layout.showHostTreeSidebar"
	FieldShowSFTPTab                  Field = "layout.showSftpTab"
	FieldShowRecentHosts              Field = "layout.showRecentHosts"
	FieldShowOnlyUngroupedHostsInRoot Field = "layout.showOnlyUngroupedHostsInRoot"
	FieldHostClickBehavior            Field = "layout.hostClickBehavior"
	FieldTabBarPosition               Field = "layout.tabBarPosition"
	FieldShellOnlyTabNumberShortcuts  Field = "layout.shellOnlyTabNumberShortcuts"
	FieldShowTabNumberBadges          Field = "layout.showTabNumberBadges"
	FieldRestorePreviousSession       Field = "layout.restorePreviousSession"
	FieldStartupLanding               Field = "layout.startupLanding"
	FieldSidePanelDock                Field = "layout.sidePanelDock"
	FieldDoubleClickBehavior          Field = "sftpEditor.doubleClickBehavior"
	FieldAutoSync                     Field = "sftpEditor.autoSync"
	FieldShowHiddenFiles              Field = "sftpEditor.showHiddenFiles"
	FieldUseCompressedUpload          Field = "sftpEditor.useCompressedUpload"
	FieldAutoOpenSidebar              Field = "sftpEditor.autoOpenSidebar"
	FieldFollowTerminalCwd            Field = "sftpEditor.followTerminalCwd"
	FieldDefaultViewMode              Field = "sftpEditor.defaultViewMode"
	FieldEditorWordWrap               Field = "sftpEditor.editorWordWrap"
	FieldEditorFontSize               Field = "sftpEditor.editorFontSize"
	FieldDefaultOpener                Field = "sftpEditor.defaultOpener"
	FieldSSHDebug                     Field = "logs.sshDebug"
	FieldSyncEnabled                  Field = "sync.enabled"
	FieldSyncAutoSync                 Field = "sync.autoSync"
	FieldSyncConflictStrategy         Field = "sync.conflictStrategy"
	FieldSyncProvider                 Field = "sync.provider"
	FieldConfirmCloseSessions         Field = "security.confirmCloseSessions"
	FieldConfirmDestructive           Field = "security.confirmDestructive"
	FieldMoshPredictionDisplay        Field = "transport.moshPredictionDisplay"
	FieldDefaultProxy                 Field = "proxy.defaultProfileId"
	FieldHistorySyncEnabled           Field = "history.syncEnabled"
	FieldHistorySyncAddress           Field = "history.syncAddress"
	FieldHistoryUsername              Field = "history.username"
	FieldHistoryAuthMode              Field = "history.authMode"
	FieldHistorySyncInterval          Field = "history.syncIntervalMinutes"
)

// Fields returns every resettable field in Settings-window order.
func Fields() []Field {
	return []Field{
		FieldThemeMode, FieldLightTheme, FieldDarkTheme, FieldAccentMode,
		FieldAccentColor, FieldAppIcon, FieldUIFont,
		FieldWindowOpacity, FieldInterfaceStyle, FieldFollowAppTerminalTheme, FieldTerminalTheme, FieldTerminalFontFamily,
		FieldTerminalFontSize, FieldTerminalCursorStyle, FieldTerminalCursorBlink,
		FieldTerminalShiftEnter, FieldLocalShell, FieldLocalShellArgs,
		FieldShowHostTreeSidebar, FieldShowSFTPTab, FieldShowRecentHosts,
		FieldShowOnlyUngroupedHostsInRoot, FieldHostClickBehavior,
		FieldTabBarPosition, FieldShellOnlyTabNumberShortcuts,
		FieldShowTabNumberBadges, FieldRestorePreviousSession,
		FieldStartupLanding, FieldSidePanelDock,
		FieldDoubleClickBehavior, FieldAutoSync, FieldShowHiddenFiles,
		FieldUseCompressedUpload, FieldAutoOpenSidebar, FieldFollowTerminalCwd,
		FieldDefaultViewMode, FieldEditorWordWrap, FieldEditorFontSize,
		FieldDefaultOpener,
		FieldSSHDebug,
		FieldHistorySyncEnabled, FieldHistorySyncAddress, FieldHistoryUsername,
		FieldHistoryAuthMode, FieldHistorySyncInterval,
		FieldSyncEnabled, FieldSyncAutoSync, FieldSyncConflictStrategy,
		FieldSyncProvider,
		FieldConfirmCloseSessions, FieldConfirmDestructive,
		FieldMoshPredictionDisplay,
		FieldDefaultProxy,
	}
}

// GroupOf returns the group a field belongs to.
func GroupOf(field Field) (Group, bool) {
	switch field {
	case FieldThemeMode, FieldLightTheme, FieldDarkTheme, FieldAccentMode,
		FieldAccentColor, FieldAppIcon, FieldUIFont,
		FieldWindowOpacity, FieldInterfaceStyle, FieldFollowAppTerminalTheme, FieldTerminalTheme, FieldTerminalFontFamily,
		FieldTerminalFontSize, FieldTerminalCursorStyle, FieldTerminalCursorBlink,
		FieldTerminalShiftEnter, FieldLocalShell, FieldLocalShellArgs:
		return GroupAppearance, true
	case FieldShowHostTreeSidebar, FieldShowSFTPTab, FieldShowRecentHosts,
		FieldShowOnlyUngroupedHostsInRoot, FieldHostClickBehavior,
		FieldTabBarPosition, FieldShellOnlyTabNumberShortcuts,
		FieldShowTabNumberBadges, FieldRestorePreviousSession,
		FieldStartupLanding, FieldSidePanelDock:
		return GroupLayout, true
	case FieldDoubleClickBehavior, FieldAutoSync, FieldShowHiddenFiles,
		FieldUseCompressedUpload, FieldAutoOpenSidebar, FieldFollowTerminalCwd,
		FieldDefaultViewMode, FieldEditorWordWrap, FieldEditorFontSize,
		FieldDefaultOpener:
		return GroupSFTPEditor, true
	case FieldSSHDebug, FieldHistorySyncEnabled, FieldHistorySyncAddress,
		FieldHistoryUsername, FieldHistoryAuthMode, FieldHistorySyncInterval:
		return GroupLogs, true
	case FieldSyncEnabled, FieldSyncAutoSync, FieldSyncConflictStrategy,
		FieldSyncProvider:
		return GroupSync, true
	case FieldConfirmCloseSessions, FieldConfirmDestructive:
		return GroupSecurity, true
	case FieldMoshPredictionDisplay, FieldDefaultProxy:
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
	case FieldInterfaceStyle:
		settings.Appearance.InterfaceStyle = defaults.Appearance.InterfaceStyle
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
	case FieldTerminalShiftEnter:
		settings.Appearance.TerminalShiftEnterPlain = defaults.Appearance.TerminalShiftEnterPlain
	case FieldLocalShell:
		// Resetting the path also clears its arguments: arguments without a
		// program are meaningless.
		settings.Appearance.LocalShell = defaults.Appearance.LocalShell
	case FieldLocalShellArgs:
		settings.Appearance.LocalShell.Args = defaults.Appearance.LocalShell.Args
	case FieldShowHostTreeSidebar:
		settings.Layout.ShowHostTreeSidebar = defaults.Layout.ShowHostTreeSidebar
	case FieldShowSFTPTab:
		settings.Layout.ShowSFTPTab = defaults.Layout.ShowSFTPTab
	case FieldShowRecentHosts:
		settings.Layout.ShowRecentHosts = defaults.Layout.ShowRecentHosts
	case FieldShowOnlyUngroupedHostsInRoot:
		settings.Layout.ShowOnlyUngroupedHostsInRoot = defaults.Layout.ShowOnlyUngroupedHostsInRoot
	case FieldHostClickBehavior:
		settings.Layout.HostClickBehavior = defaults.Layout.HostClickBehavior
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
	case FieldHistorySyncEnabled:
		settings.History.SyncEnabled = defaults.History.SyncEnabled
	case FieldHistorySyncAddress:
		settings.History.SyncAddress = defaults.History.SyncAddress
	case FieldHistoryUsername:
		settings.History.Username = defaults.History.Username
	case FieldHistoryAuthMode:
		settings.History.AuthMode = defaults.History.AuthMode
	case FieldHistorySyncInterval:
		settings.History.SyncIntervalMinutes = defaults.History.SyncIntervalMinutes
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
	case FieldDefaultProxy:
		settings.Proxy.DefaultProfileID = defaults.Proxy.DefaultProfileID
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
