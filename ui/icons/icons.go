// Package icons holds the interface's SVG icons: the Lucide set the original
// drew with lucide-react (ISC), and the distribution and vendor logos the
// original showed on Host avatars.
package icons

import (
	"embed"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

//go:embed lucide/*.svg distro/*.svg
var files embed.FS

var (
	mu     sync.Mutex
	parsed = map[string]*ui.SVG{}
)

func load(path string) *ui.SVG {
	mu.Lock()
	defer mu.Unlock()
	if svg, ok := parsed[path]; ok {
		return svg
	}
	data, err := files.ReadFile(path)
	if err != nil {
		parsed[path] = nil
		return nil
	}
	svg := ui.MustParseSVG(data)
	parsed[path] = svg
	return svg
}

// Lucide returns a Lucide icon by its file name, without the extension. It
// panics on an unknown name: every name is a constant in this package.
func Lucide(name string) *ui.SVG {
	svg := load("lucide/" + name + ".svg")
	if svg == nil {
		panic("icons: unknown Lucide icon " + name)
	}
	return svg
}

// Distro returns the logo of a distribution or network vendor, or nil when
// none is bundled for it.
func Distro(id string) *ui.SVG {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" || strings.ContainsAny(id, "/\\.") {
		return nil
	}
	return load("distro/" + id + ".svg")
}

// DistroColor is the brand color of a distribution or vendor's avatar tile,
// as the original's DISTRO_COLORS. ok is false for an unknown id.
func DistroColor(id string) (ui.Color, bool) {
	hex, ok := distroColors[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return ui.Color{}, false
	}
	return ui.Hex(hex), true
}

var distroColors = map[string]string{
	"ubuntu": "#E95420", "debian": "#A81D33", "centos": "#9C27B0",
	"rocky": "#0B9B69", "fedora": "#3C6EB4", "arch": "#1793D1",
	"alpine": "#0D597F", "amazon": "#FF9900", "opensuse": "#73BA25",
	"redhat": "#EE0000", "oracle": "#C74634", "kali": "#0F6DB3",
	"almalinux": "#173B66", "alinux": "#FF6A00", "openeuler": "#002FA7",
	"macos": "#333333", "freebsd": "#AB2B28", "windows": "#0078D4",
	"linux": "#333333", "cisco": "#1BA0D7", "juniper": "#0A6EB4",
	"huawei": "#CF0A2C", "hpe": "#01A982", "mikrotik": "#293239",
	"fortinet": "#EE3124", "paloalto": "#FA582D", "zyxel": "#00497A",
}

// Names of the Lucide icons the interface uses.
const (
	Activity          = "activity"
	AppWindow         = "app-window"
	Archive           = "archive"
	ArrowDownUp       = "arrow-down-up"
	ArrowLeft         = "arrow-left"
	ArrowRight        = "arrow-right"
	ArrowUp           = "arrow-up"
	Shield            = "shield"
	Rows3             = "rows-3"
	FilePlus          = "file-plus"
	ClipboardCopy     = "clipboard-copy"
	ArrowDown         = "arrow-down"
	ArrowUpDown       = "arrow-up-down"
	Bell              = "bell"
	BookMarked        = "book-marked"
	Bookmark          = "bookmark"
	Cable             = "cable"
	Check             = "check"
	ChevronDown       = "chevron-down"
	ChevronLeft       = "chevron-left"
	ChevronRight      = "chevron-right"
	ChevronUp         = "chevron-up"
	CircleCheck       = "circle-check"
	CircleDot         = "circle-dot"
	CircleX           = "circle-x"
	Clock             = "clock"
	Cloud             = "cloud"
	Columns           = "columns-2"
	Command           = "command"
	Copy              = "copy"
	CornerDownLeft    = "corner-down-left"
	Cpu               = "cpu"
	Download          = "download"
	Ellipsis          = "ellipsis"
	EllipsisVertical  = "ellipsis-vertical"
	ExternalLink      = "external-link"
	Eye               = "eye"
	EyeOff            = "eye-off"
	File              = "file"
	FileCode          = "file-code"
	FileOutput        = "file-output"
	FileText          = "file-text"
	Filter            = "filter"
	Fingerprint       = "fingerprint"
	Folder            = "folder"
	FolderOpen        = "folder-open"
	FolderPlus        = "folder-plus"
	Gauge             = "gauge"
	Globe             = "globe"
	GripVertical      = "grip-vertical"
	HardDrive         = "hard-drive"
	Hash              = "hash"
	History           = "history"
	House             = "house"
	IDCard            = "id-card"
	Import            = "import"
	Info              = "info"
	KeyRound          = "key-round"
	Keyboard          = "keyboard"
	Languages         = "languages"
	LayoutGrid        = "layout-grid"
	Link              = "link"
	List              = "list"
	ListTree          = "list-tree"
	Loader            = "loader-circle"
	Lock              = "lock"
	LogIn             = "log-in"
	LogOut            = "log-out"
	Maximize          = "maximize-2"
	MemoryStick       = "memory-stick"
	Minimize          = "minimize-2"
	Monitor           = "monitor"
	MonitorCog        = "monitor-cog"
	Moon              = "moon"
	Network           = "network"
	NotebookPen       = "notebook-pen"
	Palette           = "palette"
	PanelLeft         = "panel-left"
	PanelRight        = "panel-right"
	Pencil            = "pencil"
	Pin               = "pin"
	Play              = "play"
	Plug              = "plug"
	Plus              = "plus"
	Power             = "power"
	Radio             = "radio"
	RefreshCw         = "refresh-cw"
	Rows              = "rows-2"
	ScrollText        = "scroll-text"
	Search            = "search"
	Server            = "server"
	Settings          = "settings"
	ShieldCheck       = "shield-check"
	SlidersHorizontal = "sliders-horizontal"
	SplitHorizontal   = "square-split-horizontal"
	SplitVertical     = "square-split-vertical"
	SquareTerminal    = "square-terminal"
	Star              = "star"
	Sun               = "sun"
	Tag               = "tag"
	Terminal          = "terminal"
	Trash             = "trash-2"
	TriangleAlert     = "triangle-alert"
	Upload            = "upload"
	Usb               = "usb"
	User              = "user"
	WandSparkles      = "wand-sparkles"
	Wifi              = "wifi"
	X                 = "x"
	Zap               = "zap"
)
