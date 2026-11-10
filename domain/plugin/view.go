package plugin

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ViewSurface is how a View's content is rendered. A native View is the
// default; a webview View is served from the plugin package in the per-plugin
// sandbox origin and reaches its backend only through the verified bridge.
type ViewSurface string

const (
	// ViewSurfaceNative renders the retained native tree.
	ViewSurfaceNative ViewSurface = "native"
	// ViewSurfaceWebview renders a sandboxed HTML page.
	ViewSurfaceWebview ViewSurface = "webview"
)

// Valid reports whether s is a declared surface. The empty surface means
// native.
func (s ViewSurface) Valid() bool {
	return s == "" || s == ViewSurfaceNative || s == ViewSurfaceWebview
}

// Effective returns the declared surface, defaulting to native.
func (s ViewSurface) Effective() ViewSurface {
	if s == "" {
		return ViewSurfaceNative
	}
	return s
}

// Webview reports whether the surface is the sandboxed HTML page.
func (s ViewSurface) Webview() bool { return s.Effective() == ViewSurfaceWebview }

const (
	// WebviewScheme is the one custom scheme every plugin page is served
	// from. The URL host is the plugin id, so each plugin is its own origin.
	WebviewScheme = "natcatty-plugin"
	// WebviewBridgePath is the reserved same-origin endpoint the pages of a
	// plugin post bridge calls to. It is not a packaged asset and is served
	// only for POST with a verified calling page.
	WebviewBridgePath = "/__bridge"
	// DefaultWebviewEntry is the page served when a webview View names none.
	DefaultWebviewEntry = "ui/index.html"
	// MaxWebviewBridgeMethods bounds the fixed method allowlist a View may
	// expose to its page.
	MaxWebviewBridgeMethods = 64
)

// webviewBridgeMethodPattern is the closed shape of a bridge method name. It
// is an identifier the backend chooses, never a name the page supplies.
var webviewBridgeMethodPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// WebviewOrigin returns the per-plugin sandbox origin. The plugin id is the
// URL host, so two plugins never share an origin.
func WebviewOrigin(pluginID string) string {
	return WebviewScheme + "://" + pluginID
}

// WebviewURL returns the URL of a page in the plugin's sandbox origin. An
// empty entry uses DefaultWebviewEntry.
func WebviewURL(pluginID, entry string) string {
	if entry == "" {
		entry = DefaultWebviewEntry
	}
	return WebviewOrigin(pluginID) + "/" + strings.TrimPrefix(entry, "/")
}

// ViewContribution is one named view declaration. The host opens it as a Pane
// content kind when the placement is allowed; the other placements are
// rendered by the placements work. ID is local to the plugin; the host
// registers it under QualifiedViewID(pluginID, ID).
type ViewContribution struct {
	ID    string `toml:"id"`
	Title string `toml:"title"`
	// TitleKey names a catalog message resolved at display time in the host
	// locale. Title stays the literal fallback.
	TitleKey   string         `toml:"title_key"`
	Placements []Placement    `toml:"placements"`
	Instances  InstancePolicy `toml:"instances"`
	// Surface is the presentation of the view. Empty means native.
	Surface ViewSurface `toml:"surface"`
	// Entry is the packaged HTML page of a webview View. It is relative to
	// the package root, must stay under ui/, and must be an .html file. An
	// empty entry uses DefaultWebviewEntry. Native views cannot name an entry;
	// an embedded webview node names its own page with the src property.
	Entry string `toml:"entry"`
	// Bridge is the fixed method allowlist the page of this view may call on
	// its backend. A native View declares it when its tree may contain an
	// embedded webview node. The page never supplies a method name.
	Bridge []string `toml:"bridge"`
}

// EffectiveEntry returns the validated entry, defaulting to the standard
// page name.
func (v ViewContribution) EffectiveEntry() string {
	if v.Entry == "" {
		return DefaultWebviewEntry
	}
	return v.Entry
}

// Placement is one host surface a View Contribution allows. The vocabulary is
// closed; the placement implementations beyond the Pane belong to the
// placements work, but the declaration is stable so a manifest never changes
// shape later.
type Placement string

const (
	PlacementPane   Placement = "pane"
	PlacementTab    Placement = "tab"
	PlacementPage   Placement = "page"
	PlacementWindow Placement = "window"
	PlacementAside  Placement = "aside"
)

// Placements returns the closed placement vocabulary in display order.
func Placements() []Placement {
	return []Placement{PlacementPane, PlacementTab, PlacementPage, PlacementWindow, PlacementAside}
}

// Valid reports whether p is a declared placement.
func (p Placement) Valid() bool {
	for _, placement := range Placements() {
		if placement == p {
			return true
		}
	}
	return false
}

// InstancePolicy is how many live View Instances one contribution may have.
// Multi-instance is opt-in; the empty policy means singleton.
type InstancePolicy string

const (
	// InstanceSingleton allows one live instance per view.
	InstanceSingleton InstancePolicy = "singleton"
	// InstanceMulti allows one live instance per (viewId, contextKey).
	InstanceMulti InstancePolicy = "multi"
)

// Valid reports whether p is a declared policy. The empty policy is valid and
// means singleton.
func (p InstancePolicy) Valid() bool {
	return p == "" || p == InstanceSingleton || p == InstanceMulti
}

// Effective returns the declared policy, defaulting to singleton.
func (p InstancePolicy) Effective() InstancePolicy {
	if p == "" {
		return InstanceSingleton
	}
	return p
}

// Multi reports whether the contribution opts into multiple instances.
func (p InstancePolicy) Multi() bool { return p.Effective() == InstanceMulti }

// Allows reports whether the contribution declares placement p.
func (v ViewContribution) Allows(p Placement) bool {
	for _, placement := range v.Placements {
		if placement == p {
			return true
		}
	}
	return false
}

// QualifiedViewID returns the host view ID for a manifest-local view id. The
// "plugin." prefix keeps plugin views disjoint from built-in views.
func QualifiedViewID(pluginID, localID string) string {
	return "plugin." + pluginID + "." + localID
}

func (c Contributions) validateViews(permissions Permissions) error {
	views := make(map[string]bool, len(c.Views))
	for i, view := range c.Views {
		field := "contributions.views[" + strconv.Itoa(i) + "]"
		if !commandIDPattern.MatchString(view.ID) {
			return invalid(field+".id", "%q %s", view.ID, commandIDErrorText)
		}
		if views[view.ID] {
			return invalid(field+".id", "duplicate view %q", view.ID)
		}
		views[view.ID] = true
		if err := validateMessageKey(field+".title_key", view.TitleKey); err != nil {
			return err
		}
		if strings.TrimSpace(view.Title) == "" && view.TitleKey == "" {
			return invalid(field+".title", "is required unless title_key is set")
		}
		if len(view.Placements) == 0 {
			return invalid(field+".placements", "must declare at least one placement")
		}
		seen := make(map[Placement]bool, len(view.Placements))
		for j, placement := range view.Placements {
			if !placement.Valid() {
				return invalid(field+".placements["+strconv.Itoa(j)+"]", "unknown placement %q", placement)
			}
			if seen[placement] {
				return invalid(field+".placements["+strconv.Itoa(j)+"]", "duplicate placement %q", placement)
			}
			seen[placement] = true
		}
		if !view.Instances.Valid() {
			return invalid(field+".instances", "unknown policy %q", view.Instances)
		}
		if !view.Surface.Valid() {
			return invalid(field+".surface", "unknown surface %q", view.Surface)
		}
		if view.Entry != "" {
			if !view.Surface.Webview() {
				return invalid(field+".entry", "is only valid on a webview surface")
			}
			if !ValidWebviewEntry(view.Entry) {
				return invalid(field+".entry",
					"%q must be an .html path under ui/ with no escaping segments", view.Entry)
			}
		}
		if len(view.Bridge) > MaxWebviewBridgeMethods {
			return invalid(field+".bridge", "declares %d methods, the limit is %d",
				len(view.Bridge), MaxWebviewBridgeMethods)
		}
		methods := make(map[string]bool, len(view.Bridge))
		for j, method := range view.Bridge {
			if !webviewBridgeMethodPattern.MatchString(method) {
				return invalid(field+".bridge["+strconv.Itoa(j)+"]",
					"method %q must match %s", method, webviewBridgeMethodPattern)
			}
			if methods[method] {
				return invalid(field+".bridge["+strconv.Itoa(j)+"]",
					"duplicate method %q", method)
			}
			methods[method] = true
		}
		if (view.Surface.Webview() || len(view.Bridge) > 0) &&
			!permissions.Declares(PermissionUIWebview) {
			return invalid("permissions",
				"a webview surface or bridge requires the %q permission", PermissionUIWebview)
		}
	}
	if len(c.Views) > 0 && !permissions.Declares(PermissionViews) {
		return invalid("permissions", "contributions.views requires the %q permission", PermissionViews)
	}
	return nil
}

// ValidWebviewEntry reports whether entry is a packaged HTML page under ui/.
// It rejects absolute paths, backslashes, URL syntax, control characters,
// escaping segments, and any file outside the UI subtree, so the transport can
// serve only pages the package intentionally ships.
func ValidWebviewEntry(entry string) bool {
	if entry == "" || len(entry) > 256 || !strings.HasPrefix(entry, "ui/") {
		return false
	}
	if strings.HasSuffix(entry, "/") || !strings.HasSuffix(strings.ToLower(entry), ".html") {
		return false
	}
	if strings.ContainsAny(entry, `\:?#`) {
		return false
	}
	for _, r := range entry {
		if r == 0 || unicode.IsControl(r) {
			return false
		}
	}
	for _, segment := range strings.Split(entry, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

// ValidWebviewBridgeMethod reports whether method is a legal bridge method
// name. The page may only call names already in the View's fixed allowlist.
func ValidWebviewBridgeMethod(method string) bool {
	return webviewBridgeMethodPattern.MatchString(method)
}

// ParseWebviewOrigin returns the plugin id of an origin produced by
// WebviewOrigin. It rejects every other scheme and any user info, port, path,
// query, or fragment.
func ParseWebviewOrigin(origin string) (string, bool) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != WebviewScheme || parsed.User != nil ||
		parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Host == "" {
		return "", false
	}
	return parsed.Host, true
}

// Declares reports whether the permission is requested as required or
// optional.
func (p Permissions) Declares(permission Permission) bool {
	for _, name := range p.Required {
		if name == string(permission) {
			return true
		}
	}
	for _, name := range p.Optional {
		if name == string(permission) {
			return true
		}
	}
	return false
}
