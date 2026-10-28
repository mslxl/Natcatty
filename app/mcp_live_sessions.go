// The live-Session scope the external MCP policy reads while sharing is on.
// The original renderer kept the reserved __external_mcp__ scope aligned with
// every live terminal Session through useExternalMcpSessionSync; this file is
// the native equivalent of that sync, read fresh on every policy call so a
// created, closed, or reconnected Session never leaves a stale ID behind.

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	appmcp "github.com/mslxl/natcatty/application/mcp"
	appsession "github.com/mslxl/natcatty/application/session"
)

// mcpSharingFile is the persisted sharing switch. Sharing is on by default:
// this build always runs the external listener (there is no separate server
// switch), so an unset file keeps the original "live Sessions are the scope"
// behavior rather than silently exposing nothing.
const mcpSharingFile = "external-mcp-sharing.json"

type mcpSharingRecord struct {
	Enabled *bool `json:"enabled"`
}

// mcpLiveSessions implements appmcp.LiveSessionSource over the live Session
// service and the persisted sharing preference.
type mcpLiveSessions struct {
	sessions *appsession.Service
	path     string
	redraw   func()

	mu      sync.Mutex
	enabled bool
}

func newMCPLiveSessions(sessions *appsession.Service, userData string,
	redraw func()) *mcpLiveSessions {
	provider := &mcpLiveSessions{
		sessions: sessions,
		path:     filepath.Join(userData, "external-mcp", mcpSharingFile),
		redraw:   redraw,
		enabled:  true,
	}
	provider.enabled = provider.loadEnabled()
	return provider
}

// loadEnabled reads the persisted switch. A missing, malformed, or unreadable
// file keeps sharing on, the same default a fresh install uses.
func (p *mcpLiveSessions) loadEnabled() bool {
	data, err := os.ReadFile(p.path)
	if err != nil {
		return true
	}
	var record mcpSharingRecord
	if err := json.Unmarshal(data, &record); err != nil || record.Enabled == nil {
		return true
	}
	return *record.Enabled
}

// LiveSessions projects every live Session. The label is the Session's own
// display label, so Settings and get_environment show the same identity.
func (p *mcpLiveSessions) LiveSessions() []appmcp.LiveSession {
	if p == nil || p.sessions == nil {
		return nil
	}
	descriptions := p.sessions.DescribeAll()
	sessions := make([]appmcp.LiveSession, 0, len(descriptions))
	for _, description := range descriptions {
		sessions = append(sessions, appmcp.LiveSession{
			ID:     string(description.ID),
			HostID: string(description.HostID),
			Label:  description.Label,
		})
	}
	return sessions
}

// SharingEnabled reports the current switch.
func (p *mcpLiveSessions) SharingEnabled() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enabled
}

// SetEnabled persists the switch and repaints the Settings read model. The
// policy reads the value on the next call, so no restart is needed.
func (p *mcpLiveSessions) SetEnabled(enabled bool) error {
	if p == nil {
		return errors.New("external MCP: no live Session scope")
	}
	// Persist before publishing the new value: if the write fails, the switch
	// and the policy keep the state the running app actually has.
	p.mu.Lock()
	if err := p.save(enabled); err != nil {
		p.mu.Unlock()
		return err
	}
	p.enabled = enabled
	redraw := p.redraw
	p.mu.Unlock()
	if redraw != nil {
		redraw()
	}
	return nil
}

func (p *mcpLiveSessions) save(enabled bool) error {
	if p.path == "" {
		return nil
	}
	path := p.path
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("external MCP: create the sharing directory: %w", err)
	}
	data, err := json.Marshal(mcpSharingRecord{Enabled: &enabled})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".external-mcp-sharing-*.tmp")
	if err != nil {
		return fmt.Errorf("external MCP: create the sharing file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	tmpPath = ""
	return nil
}
