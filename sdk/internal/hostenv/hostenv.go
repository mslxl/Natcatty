// Package hostenv builds the deterministic host-service environment shared by
// the CLI fake host and the behavioral conformance runner. It wires the real
// host-service dispatcher and permission authority with bounded fake ports, so
// both layers observe the same permission, scope, and envelope behavior.
package hostenv

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	appplugin "github.com/mslxl/natcatty/application/plugin"
	domainplugin "github.com/mslxl/natcatty/domain/plugin"
)

// Options configures one deterministic environment. Permissions are applied
// separately by Grant so the caller controls the plugin subject and denial
// list explicitly.
type Options struct {
	// Now is the wall-clock view of the virtual time. Nil uses time.Now.
	Now func() time.Time
	// DataRoot is the directory under which the plugin's private storage
	// lives. The caller owns cleanup.
	DataRoot string
}

// Environment is one fully wired host-service environment.
type Environment struct {
	Services      *appplugin.HostServices
	Authority     *appplugin.Authority
	Clipboard     *appplugin.MemoryClipboard
	Notifications *NotificationLog
	Hosts         *HostFixtures
	Sessions      *SessionFixtures
	Terminal      *TerminalFixtures
	Settings      appplugin.MemoryHostSettings

	now func() time.Time
}

// New builds the environment. The caller must call Close when done.
func New(opts Options) (*Environment, error) {
	authority := appplugin.NewAuthority()
	clipboard := appplugin.NewMemoryClipboard("stub clipboard")
	notifications := &NotificationLog{}
	hosts := NewHostFixtures()
	sessions := NewSessionFixtures()
	terminal := NewTerminalFixtures()
	settings := appplugin.MemoryHostSettings{Values: map[string]string{
		"app.locale":  "en",
		"app.version": "stub",
	}}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	dataRoot := opts.DataRoot
	if dataRoot == "" {
		return nil, fmt.Errorf("hostenv: data root is required")
	}
	services := appplugin.NewHostServices(appplugin.ServiceDeps{
		Authority:      authority,
		Now:            now,
		DataDir:        func(id string) string { return filepath.Join(dataRoot, id) },
		Clipboard:      clipboard,
		Notifications:  notifications,
		HostSettings:   settings,
		Hosts:          hosts,
		Sessions:       sessions,
		Terminal:       terminal,
		Auth:           stubAuthBroker{},
		TrustConfirmer: stubTrustConfirmer{},
		Locale:         "en",
	})
	return &Environment{
		Services:      services,
		Authority:     authority,
		Clipboard:     clipboard,
		Notifications: notifications,
		Hosts:         hosts,
		Sessions:      sessions,
		Terminal:      terminal,
		Settings:      settings,
		now:           now,
	}, nil
}

// Close releases every open service session.
func (e *Environment) Close() {
	if e != nil && e.Services != nil {
		e.Services.CloseAll()
	}
}

// Grant applies the manifest's permission requests to one plugin subject.
// Required requests are always granted unless explicitly denied; optional
// requests follow GrantOptional.
func (e *Environment) Grant(ref appplugin.PluginRef, manifest domainplugin.Manifest,
	grantOptional bool, denied []string) error {
	if e == nil || e.Authority == nil {
		return fmt.Errorf("hostenv: no authority")
	}
	deniedSet := make(map[string]bool, len(denied))
	for _, permission := range denied {
		deniedSet[permission] = true
	}
	required, optional, err := manifest.Permissions.Requests()
	if err != nil {
		return err
	}
	now := time.Unix(0, 0)
	if e.now != nil {
		now = e.now()
	}
	subject := appplugin.SubjectForRef(ref)
	for _, request := range required {
		if deniedSet[string(request.Permission)] {
			continue
		}
		e.Authority.Grant(subject, domainplugin.PermissionGrant{
			Permission: request.Permission,
			Scope:      request.Scope,
			GrantedAt:  now,
		})
	}
	if !grantOptional {
		return nil
	}
	for _, request := range optional {
		if deniedSet[string(request.Permission)] {
			continue
		}
		e.Authority.Grant(subject, domainplugin.PermissionGrant{
			Permission: request.Permission,
			Scope:      request.Scope,
			Optional:   true,
			GrantedAt:  now,
		})
	}
	return nil
}

// GrantList returns the requested permission names in stable order, which
// makes reports deterministic.
func GrantList(manifest domainplugin.Manifest) []string {
	required, optional, err := manifest.Permissions.Requests()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(required)+len(optional))
	for _, request := range required {
		names = append(names, string(request.Permission))
	}
	for _, request := range optional {
		names = append(names, string(request.Permission))
	}
	sort.Strings(names)
	return names
}

// NotificationLog records every notification the host posts.
type NotificationLog struct {
	mu      sync.Mutex
	entries []appplugin.Notification
}

// Post implements appplugin.NotificationSink.
func (l *NotificationLog) Post(_ context.Context, notification appplugin.Notification) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, notification)
	return nil
}

// Entries returns a copy of the posted notifications.
func (l *NotificationLog) Entries() []appplugin.Notification {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]appplugin.Notification(nil), l.entries...)
}

type stubAuthBroker struct{}

func (stubAuthBroker) Begin(_ context.Context, request appplugin.AuthBeginRequest) (appplugin.AuthChallenge, error) {
	return appplugin.AuthChallenge{
		ID:         "stub-challenge",
		HostID:     request.HostID,
		Method:     request.Method,
		Prompt:     "stub authentication",
		CanRespond: false,
	}, nil
}

func (stubAuthBroker) Respond(_ context.Context, challengeID, _ string) (appplugin.AuthResult, error) {
	return appplugin.AuthResult{
		ChallengeID: challengeID,
		Status:      appplugin.AuthStatusSucceeded,
		Lease:       "stub-lease",
	}, nil
}

func (stubAuthBroker) Cancel(context.Context, string) error { return nil }

type stubTrustConfirmer struct{}

func (stubTrustConfirmer) Confirm(context.Context, appplugin.TrustRequest) (bool, error) {
	return true, nil
}

// HostFixtures is a deterministic HostCatalog.
type HostFixtures struct {
	mu    sync.Mutex
	hosts map[string]appplugin.HostSummary
	seq   int
}

// NewHostFixtures returns a catalog with one Host fixture.
func NewHostFixtures() *HostFixtures {
	return &HostFixtures{hosts: map[string]appplugin.HostSummary{
		"host-1": {
			ID: "host-1", Label: "Stub Host", Hostname: "stub.example",
			Port: 22, Protocol: "ssh", Username: "stub",
		},
	}}
}

// List implements HostCatalog.
func (c *HostFixtures) List(context.Context) ([]appplugin.HostSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hosts := make([]appplugin.HostSummary, 0, len(c.hosts))
	for _, host := range c.hosts {
		hosts = append(hosts, host)
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	return hosts, nil
}

// Get implements HostCatalog.
func (c *HostFixtures) Get(_ context.Context, id string) (appplugin.HostSummary, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	host, ok := c.hosts[id]
	return host, ok, nil
}

// Create implements HostCatalog.
func (c *HostFixtures) Create(_ context.Context, draft appplugin.HostDraft) (appplugin.HostSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := fmt.Sprintf("host-%d", c.seq+1)
	host := appplugin.HostSummary{
		ID: id, Label: draft.Label, Hostname: draft.Hostname, Port: draft.Port,
		Protocol: draft.Protocol, Username: draft.Username, Tags: draft.Tags,
		CreatedBy: draft.CreatedBy,
	}
	c.hosts[id] = host
	return host, nil
}

// Update implements HostCatalog.
func (c *HostFixtures) Update(_ context.Context, id string, patch appplugin.HostPatch) (appplugin.HostSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	host, ok := c.hosts[id]
	if !ok {
		return appplugin.HostSummary{}, fmt.Errorf("stub host %q not found", id)
	}
	if patch.Label != nil {
		host.Label = *patch.Label
	}
	if patch.Hostname != nil {
		host.Hostname = *patch.Hostname
	}
	if patch.Port != nil {
		host.Port = *patch.Port
	}
	if patch.Protocol != nil {
		host.Protocol = *patch.Protocol
	}
	if patch.Username != nil {
		host.Username = *patch.Username
	}
	if patch.Tags != nil {
		host.Tags = *patch.Tags
	}
	c.hosts[id] = host
	return host, nil
}

// Import implements HostCatalog.
func (c *HostFixtures) Import(ctx context.Context, drafts []appplugin.HostDraft) ([]appplugin.HostSummary, error) {
	created := make([]appplugin.HostSummary, 0, len(drafts))
	for _, draft := range drafts {
		host, err := c.Create(ctx, draft)
		if err != nil {
			return created, err
		}
		created = append(created, host)
	}
	return created, nil
}

// Test implements HostCatalog.
func (c *HostFixtures) Test(_ context.Context, id string) (appplugin.HostTestResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.hosts[id]; !ok {
		return appplugin.HostTestResult{}, fmt.Errorf("stub host %q not found", id)
	}
	return appplugin.HostTestResult{Reachable: true, LatencyMS: 1, Detail: "stub"}, nil
}

// SessionFixtures is a deterministic SessionCatalog.
type SessionFixtures struct {
	mu       sync.Mutex
	sessions map[string]appplugin.SessionSummary
}

// NewSessionFixtures returns a catalog with one Session fixture.
func NewSessionFixtures() *SessionFixtures {
	return &SessionFixtures{sessions: map[string]appplugin.SessionSummary{
		"session-1": {
			ID: "session-1", Kind: "terminal", Title: "Stub Session",
			HostID: "host-1", State: "running", Cols: 80, Rows: 24,
		},
	}}
}

// List implements SessionCatalog.
func (c *SessionFixtures) List(context.Context) ([]appplugin.SessionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sessions := make([]appplugin.SessionSummary, 0, len(c.sessions))
	for _, session := range c.sessions {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	return sessions, nil
}

// Get implements SessionCatalog.
func (c *SessionFixtures) Get(_ context.Context, id string) (appplugin.SessionSummary, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session, ok := c.sessions[id]
	return session, ok, nil
}

// Connect implements SessionCatalog.
func (c *SessionFixtures) Connect(_ context.Context, hostID string) (appplugin.SessionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	session := appplugin.SessionSummary{
		ID: "session-1", Kind: "terminal", Title: "Stub Session",
		HostID: hostID, State: "running", Cols: 80, Rows: 24,
	}
	c.sessions[session.ID] = session
	return session, nil
}

// Reconnect implements SessionCatalog.
func (c *SessionFixtures) Reconnect(context.Context, string) error { return nil }

// Disconnect implements SessionCatalog.
func (c *SessionFixtures) Disconnect(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, id)
	return nil
}

// TerminalFixtures is a deterministic TerminalCatalog. Observations reach EOF
// immediately so a guest can never block the fake host.
type TerminalFixtures struct{}

// NewTerminalFixtures returns the terminal fixture port.
func NewTerminalFixtures() *TerminalFixtures { return &TerminalFixtures{} }

// Observe implements TerminalCatalog.
func (*TerminalFixtures) Observe(_ context.Context, sessionID string) (appplugin.TerminalObservation, error) {
	return &eofObservation{info: appplugin.TerminalInfo{
		SessionID: sessionID, Title: "Stub Session", Cols: 80, Rows: 24,
	}}, nil
}

// Write implements TerminalCatalog.
func (*TerminalFixtures) Write(context.Context, string, []byte) error { return nil }

// Info implements TerminalCatalog.
func (*TerminalFixtures) Info(_ context.Context, sessionID string) (appplugin.TerminalInfo, bool, error) {
	return appplugin.TerminalInfo{
		SessionID: sessionID, Title: "Stub Session", Cols: 80, Rows: 24,
	}, true, nil
}

type eofObservation struct {
	info appplugin.TerminalInfo
}

func (o *eofObservation) Read(context.Context, []byte) (int, error) { return 0, io.EOF }
func (o *eofObservation) Info() appplugin.TerminalInfo              { return o.info }
func (o *eofObservation) Close() error                              { return nil }

// TempDataRoot creates a temporary data root the caller removes.
func TempDataRoot() (string, error) {
	return os.MkdirTemp("", "natcatty-hostenv-")
}
