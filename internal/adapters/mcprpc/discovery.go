// Package mcprpc is the authenticated loopback JSON-RPC transport between the
// natcatty-mcp stdio process and the running app.
//
// The wire format is newline-delimited JSON-RPC 2.0 over TCP bound to
// 127.0.0.1. The first message on every connection must be
// {"method":"auth/verify","params":{"token":"..."}}; anything else, or a wrong
// token, receives -32001 and the connection closes. Once authenticated, a
// dispatch failure returns -32000. Buffers are capped at 10 MiB and connected
// clients are never re-established automatically.
//
// The package is a process-boundary adapter: it imports no application, UI, or
// MyGo package.
package mcprpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvDiscoveryFile overrides the discovery file path, exactly as the
	// original launcher did.
	EnvDiscoveryFile = "NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE"
	// EnvPort and EnvToken are the explicit launch-time overrides the original
	// stdio server honored.
	EnvPort  = "NETCATTY_MCP_PORT"
	EnvToken = "NETCATTY_MCP_TOKEN"
	// ExternalScope is the synthetic scope every external request is forced
	// into once ticket 57 enforces scopes.
	ExternalScope = "__external_mcp__"
	// appDirName is MyGo's application directory (mygo.json name).
	appDirName = "Natcatty"
	// stateDirName is the original's external-mcp state directory.
	stateDirName = "external-mcp"
)

// Discovery is the runtime pointer an MCP client reads to find the app.
type Discovery struct {
	Version        int    `json:"version"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Token          string `json:"token"`
	PID            int    `json:"pid"`
	PermissionMode string `json:"permissionMode"`
	ChatSessionID  string `json:"chatSessionId"`
	UpdatedAt      string `json:"updatedAt"`
}

// DefaultDiscoveryPath returns the per-user path beneath MyGo's application
// data directory, or the NETCATTY_EXTERNAL_MCP_DISCOVERY_FILE override.
func DefaultDiscoveryPath() (string, error) {
	if configured := strings.TrimSpace(os.Getenv(EnvDiscoveryFile)); configured != "" {
		return configured, nil
	}
	root, err := userDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, stateDirName, "discovery.json"), nil
}

// ResolveDiscoveryPath is DefaultDiscoveryPath under the original's name.
func ResolveDiscoveryPath() (string, error) { return DefaultDiscoveryPath() }

// ReadDiscovery loads and validates a discovery file, applying the original
// defaults for every optional field.
func ReadDiscovery(path string) (Discovery, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Discovery{}, fmt.Errorf("external MCP discovery: %w", err)
	}
	var parsed Discovery
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Discovery{}, fmt.Errorf("external MCP discovery: invalid JSON: %w", err)
	}
	if parsed.Port <= 0 || parsed.Port > 65535 {
		return Discovery{}, errors.New("external MCP discovery: port is missing or invalid")
	}
	parsed.Token = strings.TrimSpace(parsed.Token)
	if parsed.Token == "" {
		return Discovery{}, errors.New("external MCP discovery: token is missing")
	}
	parsed.Host = strings.TrimSpace(parsed.Host)
	if parsed.Host == "" {
		parsed.Host = "127.0.0.1"
	}
	if parsed.Version == 0 {
		parsed.Version = 1
	}
	if parsed.PermissionMode == "" {
		parsed.PermissionMode = "confirm"
	}
	if parsed.ChatSessionID == "" {
		parsed.ChatSessionID = ExternalScope
	}
	return parsed, nil
}

// WriteDiscovery writes the discovery file with owner-only permissions. It
// fills the original defaults and refuses an invalid port or token.
func WriteDiscovery(path string, discovery Discovery) (Discovery, error) {
	if discovery.Port <= 0 || discovery.Port > 65535 {
		return Discovery{}, errors.New("external MCP discovery: port must be 1-65535")
	}
	if strings.TrimSpace(discovery.Token) == "" {
		return Discovery{}, errors.New("external MCP discovery: token is required")
	}
	if discovery.Version == 0 {
		discovery.Version = 1
	}
	if strings.TrimSpace(discovery.Host) == "" {
		discovery.Host = "127.0.0.1"
	}
	if discovery.PermissionMode == "" {
		discovery.PermissionMode = "confirm"
	}
	if discovery.ChatSessionID == "" {
		discovery.ChatSessionID = ExternalScope
	}
	if discovery.UpdatedAt == "" {
		discovery.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	payload, err := json.MarshalIndent(discovery, "", "  ")
	if err != nil {
		return Discovery{}, fmt.Errorf("external MCP discovery: encode: %w", err)
	}
	payload = append(payload, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return Discovery{}, fmt.Errorf("external MCP discovery: create directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Discovery{}, fmt.Errorf("external MCP discovery: write: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return Discovery{}, fmt.Errorf("external MCP discovery: write: %w", err)
	}
	if err := file.Close(); err != nil {
		return Discovery{}, fmt.Errorf("external MCP discovery: write: %w", err)
	}
	return discovery, nil
}

// RemoveDiscovery deletes the discovery file; a missing file is not an error.
func RemoveDiscovery(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("external MCP discovery: remove: %w", err)
	}
	return nil
}

// RemoveDiscoveryIfOwned removes the file only when it still carries this
// process's port and token, so a new app instance's pointer is never deleted
// by an older one shutting down.
func RemoveDiscoveryIfOwned(path string, port int, token string) error {
	current, err := ReadDiscovery(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		// A malformed file is either ours to clean or already gone.
		return RemoveDiscovery(path)
	}
	if current.Port != port || current.Token != token {
		return nil
	}
	return RemoveDiscovery(path)
}

// PortFromEnv returns the explicit NETCATTY_MCP_PORT override.
func PortFromEnv() (int, bool) {
	raw := strings.TrimSpace(os.Getenv(EnvPort))
	if raw == "" {
		return 0, false
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// TokenFromEnv returns the explicit NETCATTY_MCP_TOKEN override.
func TokenFromEnv() (string, bool) {
	token := strings.TrimSpace(os.Getenv(EnvToken))
	return token, token != ""
}

// userDataDir mirrors MyGo's mygo.App.Path(mygo.PathUserData) for the app
// named Natcatty, which is what the standalone stdio process can resolve
// without linking MyGo.
func userDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil && runtime.GOOS != "windows" {
		return "", fmt.Errorf("external MCP discovery: resolve home: %w", err)
	}
	switch runtime.GOOS {
	case "windows":
		root := strings.TrimSpace(os.Getenv("APPDATA"))
		if root == "" {
			if home == "" {
				return "", errors.New("external MCP discovery: APPDATA is not set")
			}
			root = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(root, appDirName), nil
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", appDirName), nil
	default:
		root := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
		if root == "" {
			root = filepath.Join(home, ".config")
		}
		return filepath.Join(root, appDirName), nil
	}
}
