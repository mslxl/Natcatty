// Package pluginregistry reads the static plugin registry layout: a JSON
// index, one JSON metadata file per plugin, and package bodies. The registry
// is a distribution index, not a trust anchor; this adapter only fetches bytes
// and validates their shape. The application verifies the declared SHA-256 and
// size before installing anything.
package pluginregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Limits bound registry reads. The package cap matches the .natplug archive
// limit so a hostile registry cannot make the client buffer more than one
// installable archive.
const (
	MaxIndexBytes          = 8 << 20
	MaxPluginMetadataBytes = 8 << 20
	MaxPackageBytes        = 64 << 20
	// HTTPTimeout bounds one registry request. A stalled or hostile server
	// must not hang install, update, or automatic-update startup work.
	HTTPTimeout = 30 * time.Second
)

var pluginIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// Registry implements application/plugin.Registry for a local directory or an
// HTTPS base URL with the same layout.
type Registry struct {
	identity string
	root     string
	baseURL  string
	client   *http.Client
}

var _ appplugin.Registry = (*Registry)(nil)

// OpenDirectory reads a registry from a local directory. This is the
// deterministic test and side-loading form of the same static layout.
func OpenDirectory(directory string) *Registry {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		absolute = filepath.Clean(directory)
	}
	return &Registry{identity: filepath.ToSlash(absolute), root: absolute}
}

// OpenHTTP reads a registry from an HTTPS (or test HTTP) base URL. Requests
// are bounded by HTTPTimeout so a stalled server cannot hang the host.
func OpenHTTP(baseURL string) *Registry {
	baseURL = strings.TrimRight(baseURL, "/")
	return &Registry{
		identity: baseURL, baseURL: baseURL,
		client: &http.Client{Timeout: HTTPTimeout},
	}
}

// Identity returns the stable source identity used for trust-on-first-use
// pinning. It is deliberately independent of the package version.
func (r *Registry) Identity() string { return r.identity }

// Index fetches and validates the registry index.
func (r *Registry) Index(ctx context.Context) (domainplugin.RegistryIndex, error) {
	var index domainplugin.RegistryIndex
	if err := r.readJSON(ctx, "index.json", MaxIndexBytes, &index); err != nil {
		return domainplugin.RegistryIndex{}, err
	}
	if err := index.Validate(); err != nil {
		return domainplugin.RegistryIndex{}, err
	}
	return index, nil
}

// Plugin fetches and validates one plugin's full version metadata.
func (r *Registry) Plugin(ctx context.Context, id string) (domainplugin.RegistryPlugin, error) {
	if err := checkPluginID(id); err != nil {
		return domainplugin.RegistryPlugin{}, err
	}
	var plugin domainplugin.RegistryPlugin
	if err := r.readJSON(ctx, path.Join("plugins", id+".json"),
		MaxPluginMetadataBytes, &plugin); err != nil {
		return domainplugin.RegistryPlugin{}, err
	}
	if plugin.ID != id {
		return domainplugin.RegistryPlugin{}, fmt.Errorf(
			"plugin registry: metadata for %s names plugin %q", id, plugin.ID)
	}
	if err := plugin.Validate(); err != nil {
		return domainplugin.RegistryPlugin{}, err
	}
	return plugin, nil
}

// FetchPackage reads one package body by the path its metadata declares.
func (r *Registry) FetchPackage(ctx context.Context, id, version string) ([]byte, error) {
	plugin, err := r.Plugin(ctx, id)
	if err != nil {
		return nil, err
	}
	var entry *domainplugin.RegistryVersion
	for i := range plugin.Versions {
		if plugin.Versions[i].Version == version {
			entry = &plugin.Versions[i]
			break
		}
	}
	if entry == nil {
		return nil, fmt.Errorf("plugin registry: %s has no version %s", id, version)
	}
	if entry.Size > MaxPackageBytes {
		return nil, fmt.Errorf("plugin registry: %s %s package is %d bytes, above the %d byte limit",
			id, version, entry.Size, MaxPackageBytes)
	}
	data, err := r.read(ctx, entry.Package, MaxPackageBytes)
	if err != nil {
		return nil, fmt.Errorf("plugin registry: fetch %s %s: %w", id, version, err)
	}
	if int64(len(data)) != entry.Size {
		return nil, fmt.Errorf("plugin registry: %s %s package is %d bytes, metadata declares %d",
			id, version, len(data), entry.Size)
	}
	return data, nil
}

func (r *Registry) readJSON(ctx context.Context, name string, limit int64, target any) error {
	data, err := r.read(ctx, name, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("plugin registry: parse %s: %w", name, err)
	}
	return nil
}

func (r *Registry) read(ctx context.Context, name string, limit int64) ([]byte, error) {
	if r.baseURL != "" {
		return r.readHTTP(ctx, name, limit)
	}
	return r.readFile(name, limit)
}

func (r *Registry) readFile(name string, limit int64) ([]byte, error) {
	clean := path.Clean(name)
	if clean != name || clean == "." || path.IsAbs(name) ||
		strings.HasPrefix(clean, "../") || strings.Contains(name, "\\") {
		return nil, fmt.Errorf("plugin registry: unsafe path %q", name)
	}
	target := filepath.Join(r.root, filepath.FromSlash(name))
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readLimited(file, limit, name)
}

func (r *Registry) readHTTP(ctx context.Context, name string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.baseURL+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", name, response.Status)
	}
	return readLimited(response.Body, limit, name)
}

func readLimited(reader io.Reader, limit int64, name string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is above the %d byte limit", name, limit)
	}
	return data, nil
}

func checkPluginID(id string) error {
	if !pluginIDPattern.MatchString(id) || strings.Count(id, ".") != 1 {
		return fmt.Errorf("plugin registry: invalid plugin id %q", id)
	}
	return nil
}
